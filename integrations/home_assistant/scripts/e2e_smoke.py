"""The e2e smoke (M8-D10): the component's client, feed and models, without Home Assistant, against a Cadence server.

deploy/dev/e2e.sh runs it in make ha-test's image on the dev stack's network, with the three modules copied into the
--package directory, which is loaded as a package of its own (the component's __init__.py imports Home Assistant and
never runs). The ha token is stdin's first line, never an argument. The steps, one line each as it passes:

    devices ok           the device listed, online, with its name and model, and the commands Home Assistant uses
    connected            the live feed subscribed
    playing source=url   a url session the client started, as the feed's media_state, with its metadata
    paused               its pause, as the feed's media_state
    said status=202      an announcement (cadence.say) accepted
    offline / online     device_offline, then device_online, as the e2e restarts the device
    vitals               a device_vitals of the device after its device_online
    closed code=1008     the feed closed once the e2e revoked the token
    refused status=401   the next read refused

A step that fails prints `failed step=<step> code=<code>` and exits 1. Lines carry codes and counts alone: never the
token, a url, a name or metadata.
"""

from __future__ import annotations

import argparse
import asyncio
import importlib
import sys
import types
from collections.abc import Callable
from pathlib import Path
from typing import Any

import aiohttp

DEVICE_NAME = "sim-e2e"
DEVICE_MODEL = "sim"
SONG = {"title": "Cadence E2E HA Song", "artist": "Cadence E2E HA Artist"}
SAY = "Cadence E2E ha client say"
# from its step's start; the e2e's own waits are shorter wherever it acts in between
CONNECT_WAIT = 15.0
MEDIA_WAIT = 15.0
# the e2e checks the announcement played, then restarts the device
RESTART_WAIT = 90.0
# a device's vitals reach the feed at most once in 30 s
VITALS_WAIT = 60.0
# the server checks a feed's token at every ping, 20 s apart
REVOKE_WAIT = 60.0


class StepFailed(Exception):
    def __init__(self, code: str) -> None:
        super().__init__(code)
        self.code = code


def load(package_dir: Path) -> tuple[Any, Any, Any]:
    """client, feed and models, as the package `cadence` without its __init__.py."""
    package = types.ModuleType("cadence")
    package.__path__ = [str(package_dir)]
    sys.modules["cadence"] = package
    return tuple(importlib.import_module(f"cadence.{name}") for name in ("client", "feed", "models"))


class Recorder:
    """The feed's handler: keeps the device's events in order and tells each one to whoever waits."""

    def __init__(self, device_id: str) -> None:
        self.device_id = device_id
        self.events: list[Any] = []
        self.connected = asyncio.Event()
        self._news = asyncio.Event()

    async def feed_connected(self) -> None:
        self.connected.set()

    async def feed_event(self, event: Any) -> None:
        if event.device_id == self.device_id:
            self.events.append(event)
            self._news.set()

    def feed_disconnected(self, code: str) -> None:
        pass

    async def first(self, test: Callable[[Any], bool], seconds: float, since: int = 0) -> int:
        """The index of the first event from since on that passes test."""
        at = since
        async with asyncio.timeout(seconds):
            while True:
                while at < len(self.events):
                    if test(self.events[at]):
                        return at
                    at += 1
                self._news.clear()
                await self._news.wait()


class Smoke:
    def __init__(self, args: argparse.Namespace, token: str) -> None:
        self.args = args
        self.token = token
        self.client_mod, self.feed_mod, self.models = load(Path(args.package))
        self.statuses: list[tuple[str, int]] = []
        self.step = "start"

    def passed(self, line: str) -> None:
        print(line, flush=True)

    async def run(self) -> None:
        trace = aiohttp.TraceConfig()
        trace.on_request_end.append(self._ended)
        async with aiohttp.ClientSession(trace_configs=[trace]) as session:
            client = self.client_mod.CadenceClient(session, self.args.url, self.token)
            await self.devices(client)
            recorder = Recorder(self.args.device)
            feed = asyncio.create_task(self.feed_mod.LiveFeed(client, recorder).run())
            try:
                await self.follow(client, recorder, feed)
            finally:
                feed.cancel()
                await asyncio.gather(feed, return_exceptions=True)

    async def _ended(self, _session: Any, _ctx: Any, params: aiohttp.TraceRequestEndParams) -> None:
        self.statuses.append((params.url.path, params.response.status))

    async def devices(self, client: Any) -> None:
        self.step = "devices"
        listed = [d for d in await client.list_devices() if d.id == self.args.device]
        if len(listed) != 1 or not listed[0].online:
            raise StepFailed("not_listed")
        if (listed[0].name, listed[0].model) != (DEVICE_NAME, DEVICE_MODEL):
            raise StepFailed("other_device")
        detail = await client.get_device(self.args.device)
        commands = detail.commands
        if not (
            {"pause", "resume"} <= commands.get("media.player", frozenset())
            and "play" in commands.get("media.url", frozenset())
            and "set_level" in commands.get("audio.output", frozenset())
            and detail.volume is not None
        ):
            raise StepFailed("no_commands")
        self.passed("devices ok")

    async def follow(self, client: Any, recorder: Recorder, feed: asyncio.Task[None]) -> None:
        device = self.args.device
        self.step = "connected"
        waiter = asyncio.create_task(recorder.connected.wait())
        await asyncio.wait({feed, waiter}, timeout=CONNECT_WAIT, return_when=asyncio.FIRST_COMPLETED)
        waiter.cancel()
        if not recorder.connected.is_set():
            if feed.done():
                # its error's code: a refused token, say
                await feed
            raise StepFailed("timeout")
        self.passed("connected")

        self.step = "playing"
        mark = len(recorder.events)
        await client.send_command(
            device, "media.url", "play", {"url": self.args.media, "mode": "session", "metadata": SONG}
        )
        at = await recorder.first(lambda e: self.media(e, "playing", title=SONG["title"]), MEDIA_WAIT, mark)
        self.passed("playing source=url")

        self.step = "paused"
        await client.send_command(device, "media.player", "pause")
        await recorder.first(lambda e: self.media(e, "paused"), MEDIA_WAIT, at + 1)
        self.passed("paused")

        self.step = "said"
        if not await client.announce(device, SAY):
            raise StepFailed("no_stream")
        said = [status for path, status in self.statuses if path.endswith("/announce")]
        if said != [202]:
            raise StepFailed("status")
        self.passed("said status=202")

        self.step = "offline"
        mark = len(recorder.events)
        at = await recorder.first(lambda e: e.type == "device_offline", RESTART_WAIT, mark)
        self.passed("offline")
        self.step = "online"
        at = await recorder.first(lambda e: e.type == "device_online", RESTART_WAIT, at + 1)
        self.passed("online")

        # one after the relink, so vitals are shown to flow after a reconnect: up to 30 s more than an earlier one
        self.step = "vitals"
        await recorder.first(self.vitals, VITALS_WAIT, at + 1)
        self.passed("vitals")

        self.step = "closed"
        async with asyncio.timeout(REVOKE_WAIT):
            done = await asyncio.gather(feed, return_exceptions=True)
        err = done[0]
        # a 1008 close carries no http status; a refused upgrade on a reconnect would carry 401
        if not isinstance(err, self.client_mod.CadenceAuthError) or err.status is not None:
            raise StepFailed("not_1008")
        self.passed("closed code=1008")

        self.step = "refused"
        try:
            await client.list_devices()
        except self.client_mod.CadenceAuthError as refused:
            if refused.status != 401:
                raise StepFailed(f"http_{refused.status}") from None
        else:
            raise StepFailed("accepted")
        self.passed("refused status=401")

    def media(self, event: Any, state: str, title: str | None = None) -> bool:
        if event.type != "media_state":
            return False
        session = self.models.MediaSession.from_json(event.data)
        return (
            session is not None
            and session.source == "url"
            and session.state == state
            and (title is None or session.metadata.title == title)
        )

    def vitals(self, event: Any) -> bool:
        if event.type != "device_vitals":
            return False
        vitals = self.models.Vitals.from_json(event.data)
        return 0 <= vitals.cpu <= 1 and 0 < vitals.memory_used_bytes <= vitals.memory_limit_bytes


async def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.partition("\n")[0])
    parser.add_argument("--url", required=True, help="the server's base url")
    parser.add_argument("--device", required=True, help="the device's id")
    parser.add_argument("--package", required=True, help="the directory holding client.py, feed.py and models.py")
    parser.add_argument("--media", required=True, help="the url the session plays")
    args = parser.parse_args()
    token = sys.stdin.readline().strip()
    smoke = Smoke(args, token)
    try:
        await smoke.run()
    except StepFailed as err:
        code = err.code
    except smoke.client_mod.CadenceError as err:
        code = err.code
    except TimeoutError:
        code = "timeout"
    except Exception as err:
        # its type alone: an error's text may carry data
        code = type(err).__name__
    else:
        return 0
    print(f"failed step={smoke.step} code={code}", flush=True)
    return 1


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
