"""The client alone, without Home Assistant, against the fake server over TLS."""

from __future__ import annotations

import asyncio
import json
import subprocess
import sys
from datetime import UTC, datetime
from pathlib import Path
from unittest.mock import patch

import aiohttp
import pytest

from custom_components.cadence import client as cadence
from custom_components.cadence import feed as cadence_feed
from custom_components.cadence.client import (
    CadenceApiError,
    CadenceAuthError,
    CadenceClient,
    CadenceConnectionError,
    CadenceScopeError,
)
from custom_components.cadence.feed import Backoff, LiveFeed
from custom_components.cadence.models import FeedEvent, MediaMetadata

from .fake_backend import ART, HALL, KITCHEN, TOKEN, FakeCadence, UntrustedServer, dropped, fixture, frame, wait_for


async def test_reads_parse_what_the_server_answers(client: CadenceClient, backend: FakeCadence) -> None:
    devices = await client.list_devices()
    assert [(d.id, d.name, d.model, d.online) for d in devices] == [
        (KITCHEN, "Kitchen", "sim", True),
        (HALL, "Hall", "sim", False),
    ]

    kitchen = await client.get_device(KITCHEN)
    assert kitchen.device.name == "Kitchen"
    assert kitchen.volume == 35
    assert kitchen.capabilities == {"alarms", "audio.output", "media.player", "media.url"}
    assert kitchen.commands["media.player"] == {"pause", "resume", "stop", "seek", "next", "previous"}
    hall = await client.get_device(HALL)
    assert hall.volume is None
    assert hall.capabilities == frozenset()

    media = await client.get_media(KITCHEN)
    assert media is not None
    assert (media.session_id, media.source, media.state) == ("url-7f3a", "url", "playing")
    assert (media.position_ms, media.duration_ms, media.rate, media.volume) == (42000, 215000, 1.0, 60)
    assert media.position_at == datetime(2026, 11, 12, 9, 59, 58, 250000, tzinfo=UTC)
    assert (media.metadata.title, media.metadata.artist, media.metadata.album) == (
        "Night Drive",
        "The Examples",
        "Fixtures",
    )
    assert media.metadata.artwork_hash == fixture("media_playing.json")["metadata"]["artworkHash"]
    assert media.actions == {"pause", "stop", "seek", "next", "previous"}
    # the art's url stays on the server's side: the component reads the image through its route only
    assert "artwork_url" not in MediaMetadata.__slots__
    assert await client.get_media(HALL) is None

    vitals = await client.get_vitals(KITCHEN)
    assert vitals is not None
    assert (vitals.cpu, vitals.memory_used_bytes, vitals.memory_limit_bytes) == (0.125, 412000000, 1024000000)
    assert [(d.mountpoint, d.free_bytes, d.limit_bytes) for d in vitals.disks] == [
        ("/data", 6000000000, 8000000000),
        ("/tmp", 200000000, 256000000),
    ]
    assert vitals.temperature_c == 48.5
    assert vitals.at == datetime(2026, 11, 12, 10, 0, tzinfo=UTC)
    # no vitals until the device's next metrics round: unknown, not an error
    assert await client.get_vitals(HALL) is None

    assert backend.bearers == {f"Bearer {TOKEN}"}


async def test_refusals_carry_codes_never_the_token(
    session: aiohttp.ClientSession, backend: FakeCadence, rogue_backend: UntrustedServer
) -> None:
    errors: list[Exception] = []
    with pytest.raises(CadenceAuthError) as auth:
        await CadenceClient(session, backend.url, "cdn_wrong").list_devices()
    errors.append(auth.value)
    assert (auth.value.status, auth.value.code) == (401, "unauthorized")

    backend.refuse = 403
    with pytest.raises(CadenceScopeError) as scope:
        await CadenceClient(session, backend.url, TOKEN).list_devices()
    errors.append(scope.value)
    assert (scope.value.status, scope.value.code) == (403, "forbidden")
    backend.refuse = None

    with pytest.raises(CadenceApiError) as missing:
        await CadenceClient(session, backend.url, TOKEN).get_device("not-a-device")
    assert (missing.value.status, missing.value.code) == (404, "not_found")

    # tls is verified: a certificate from a CA the session does not trust is refused
    with pytest.raises(CadenceConnectionError) as tls:
        await CadenceClient(session, rogue_backend.url, TOKEN).list_devices()
    errors.append(tls.value)
    assert tls.value.code == "tls"
    assert rogue_backend.handshakes >= 1
    assert rogue_backend.requests == []

    backend.answer_delay = 1.0
    with patch.object(cadence, "REQUEST_TIMEOUT", 0.2), pytest.raises(CadenceConnectionError) as slow:
        await CadenceClient(session, backend.url, TOKEN).list_devices()
    assert slow.value.code == "timeout"
    for err in errors:
        assert TOKEN not in str(err)
        assert "127.0.0.1" not in str(err)


async def test_answers_that_are_no_cadence_server(session: aiohttp.ClientSession, backend: FakeCadence) -> None:
    backend.devices = {"x": {"name": "no id"}}  # type: ignore[dict-item]
    with pytest.raises(CadenceConnectionError) as err:
        await CadenceClient(session, backend.url, TOKEN).list_devices()
    assert err.value.code == "invalid"


async def test_commands_announcements_and_artwork(client: CadenceClient, backend: FakeCadence) -> None:
    backend.command_answer = (200, {"result": {"level": 40}})
    assert await client.send_command(KITCHEN, "audio.output", "set_level", {"level": 40}) == {"level": 40}
    await client.send_command(KITCHEN, "media.player", "pause")
    assert backend.commands == [
        (KITCHEN, "audio.output", "set_level", {"level": 40}),
        (KITCHEN, "media.player", "pause", {}),
    ]
    backend.command_answer = (409, {"error": "device_offline"})
    with pytest.raises(CadenceApiError) as offline:
        await client.send_command(KITCHEN, "media.player", "resume")
    assert (offline.value.status, offline.value.code) == (409, "device_offline")
    # upstream text in an error body never becomes a code
    backend.command_answer = (500, {"error": "Something broke: token=abc"})
    with pytest.raises(CadenceApiError) as odd:
        await client.send_command(KITCHEN, "media.player", "resume")
    assert odd.value.code == "http_500"

    assert await client.announce(KITCHEN, "Dinner is ready", "en") == "st-1"
    await client.announce(KITCHEN, "Ужин готов")
    assert backend.announces == [
        (KITCHEN, {"text": "Dinner is ready", "language": "en"}),
        (KITCHEN, {"text": "Ужин готов"}),
    ]

    art = await client.get_artwork(KITCHEN, ART)
    assert art is not None
    assert (art.content_type, art.data) == ("image/png", b"\x89PNG fake")
    assert backend.artwork_asks == [ART]
    # a hash that is no longer the session's: the server's 409, no image
    assert await client.get_artwork(KITCHEN, "ab" * 32) is None
    for status in (404, 409, 502):
        backend.artwork = (status, b"", "")
        assert await client.get_artwork(KITCHEN, ART) is None
    backend.artwork = (200, b"<svg/>", "image/svg+xml")
    assert await client.get_artwork(KITCHEN, ART) is None
    # a large image arrives whole, with or without its length; one past 5 MiB is none, declared or streamed
    big = bytes(range(256)) * 8192
    for chunked in (False, True):
        backend.artwork_chunked = chunked
        backend.artwork = (200, big, "image/jpeg")
        art = await client.get_artwork(KITCHEN, ART)
        assert art is not None
        assert art.data == big
        backend.artwork = (200, b"x" * (cadence.MAX_ARTWORK_BYTES + 1), "image/jpeg")
        assert await client.get_artwork(KITCHEN, ART) is None


def test_backoff_doubles_from_one_to_thirty_seconds_with_jitter() -> None:
    low, high = Backoff(rand=lambda: 0.0), Backoff(rand=lambda: 1.0)
    assert [low.next() for _ in range(7)] == [1, 2, 4, 8, 15, 15, 15]
    assert [high.next() for _ in range(7)] == [2, 4, 8, 16, 30, 30, 30]
    low.reset()
    assert low.next() == 1


def test_backoff_never_overflows() -> None:
    """A server down for days: thousands of delays, every one within 30 s."""
    for rand in (0.0, 1.0):
        backoff = Backoff(rand=lambda r=rand: r)
        delays = [backoff.next() for _ in range(2000)]
        assert all(0.5 <= d <= 30 for d in delays)
        assert delays[-1] == (15 if rand == 0.0 else 30)


class Recorder:
    """A feed handler that records what the feed told it."""

    def __init__(self, backend: FakeCadence) -> None:
        self.backend = backend
        self.calls: list[str] = []
        self.events: list[FeedEvent] = []
        self.fail_sync: Exception | None = None
        self.fail_down: Exception | None = None

    async def feed_connected(self) -> None:
        # what a handler reads now is the state after the subscription
        self.calls.append(f"connected subscribes={len(self.backend.subscribes)}")
        if self.fail_sync is not None:
            err, self.fail_sync = self.fail_sync, None
            raise err

    async def feed_event(self, event: FeedEvent) -> None:
        self.calls.append(f"event {event.type}")
        self.events.append(event)

    def feed_disconnected(self, code: str) -> None:
        self.calls.append(f"down {code}")
        if self.fail_down is not None:
            err, self.fail_down = self.fail_down, None
            raise err


class Sleeps:
    def __init__(self) -> None:
        self.delays: list[float] = []

    async def __call__(self, delay: float) -> None:
        self.delays.append(delay)
        await asyncio.sleep(0)


def feed(client: CadenceClient, handler: Recorder, sleeps: Sleeps) -> LiveFeed:
    return LiveFeed(client, handler, backoff=Backoff(rand=lambda: 0.0), sleep=sleeps)


async def test_feed_subscribes_then_has_the_state_read(client: CadenceClient, backend: FakeCadence) -> None:
    handler, sleeps = Recorder(backend), Sleeps()
    task = asyncio.create_task(feed(client, handler, sleeps).run())
    try:
        await wait_for(lambda: handler.calls == ["connected subscribes=1"])
        assert backend.subscribes == ['{"subscribe":["devices","media","vitals","groups","alarms"]}']
        assert json.loads(backend.subscribes[0]) == {"subscribe": ["devices", "media", "vitals", "groups", "alarms"]}
        await backend.push(fixture("device_volume.json"), fixture("media_state.json"), dropped(3))
        await wait_for(lambda: len(handler.events) == 3)
        volume, media, lost = handler.events
        assert (volume.type, volume.device_id, volume.data) == ("device_volume", KITCHEN, {"level": 35})
        assert (media.type, media.data["sessionId"]) == ("media_state", "url-7f3a")
        assert (lost.type, lost.device_id, lost.count) == ("dropped", None, 3)
        assert backend.bearers == {f"Bearer {TOKEN}"}
    finally:
        task.cancel()
        await asyncio.gather(task, return_exceptions=True)


async def test_feed_reconnects_with_backoff_and_reads_again(client: CadenceClient, backend: FakeCadence) -> None:
    handler, sleeps = Recorder(backend), Sleeps()
    task = asyncio.create_task(feed(client, handler, sleeps).run())
    try:
        await wait_for(lambda: handler.calls == ["connected subscribes=1"])
        # the server restarts: it goes away, and the first read after it is back fails
        handler.fail_sync = CadenceApiError("unavailable", 503)
        await backend.close_feeds(1001)
        await wait_for(lambda: handler.calls[-1] == "connected subscribes=3")
        assert handler.calls == [
            "connected subscribes=1",
            "down closed",
            "connected subscribes=2",
            "down unavailable",
            "connected subscribes=3",
        ]
        assert sleeps.delays == [1, 2]
        # a connection dropped at once (a server that accepts, serves the read and closes) keeps the delays growing
        await backend.close_feeds(1001)
        await wait_for(lambda: handler.calls[-1] == "connected subscribes=4")
        assert sleeps.delays == [1, 2, 4]
        # one that stayed up a minute (here: any) starts them over
        with patch.object(cadence_feed, "FEED_STABLE", 0.0):
            await backend.close_feeds(1001)
            await wait_for(lambda: handler.calls[-1] == "connected subscribes=5")
        assert sleeps.delays == [1, 2, 4, 1]
    finally:
        task.cancel()
        await asyncio.gather(task, return_exceptions=True)


async def test_feed_closed_1008_is_an_auth_failure_without_retry(client: CadenceClient, backend: FakeCadence) -> None:
    handler, sleeps = Recorder(backend), Sleeps()
    task = asyncio.create_task(feed(client, handler, sleeps).run())
    await wait_for(lambda: len(backend.feeds) == 1)
    await backend.close_feeds(1008)
    with pytest.raises(CadenceAuthError):
        await asyncio.wait_for(task, 5)
    assert handler.calls[-1] == "down unauthorized"
    assert sleeps.delays == []
    assert backend.connects == 1


@pytest.mark.parametrize(("refuse", "error"), [(401, CadenceAuthError), (403, CadenceScopeError)])
async def test_feed_upgrade_refused_is_an_auth_failure(
    client: CadenceClient, backend: FakeCadence, refuse: int, error: type[Exception]
) -> None:
    backend.refuse = refuse
    handler, sleeps = Recorder(backend), Sleeps()
    with pytest.raises(error):
        await asyncio.wait_for(feed(client, handler, sleeps).run(), 5)
    assert sleeps.delays == []
    assert backend.connects == 0


async def test_feed_outlives_a_fault_of_its_handler(
    client: CadenceClient, backend: FakeCadence, caplog: pytest.LogCaptureFixture
) -> None:
    handler, sleeps = Recorder(backend), Sleeps()
    handler.fail_sync = RuntimeError("state of Kitchen")
    task = asyncio.create_task(feed(client, handler, sleeps).run())
    try:
        await wait_for(lambda: handler.calls[-1:] == ["connected subscribes=2"])
        assert handler.calls == ["connected subscribes=1", "down unexpected", "connected subscribes=2"]
        assert "cadence_feed_failed code=unexpected error=RuntimeError" in caplog.text
        assert "Kitchen" not in caplog.text
    finally:
        task.cancel()
        await asyncio.gather(task, return_exceptions=True)


async def test_feed_frames_the_client_cannot_read_are_skipped(client: CadenceClient, backend: FakeCadence) -> None:
    handler, sleeps = Recorder(backend), Sleeps()
    task = asyncio.create_task(feed(client, handler, sleeps).run())
    try:
        await wait_for(lambda: len(backend.feeds) == 1)
        for ws in backend.feeds:
            await ws.send_str("not json")
            await ws.send_json(["no", "type"])
        await backend.push(frame("device_online.json"))
        await wait_for(lambda: len(handler.events) == 1)
        assert handler.events[0].type == "device_online"
        assert backend.connects == 1
    finally:
        task.cancel()
        await asyncio.gather(task, return_exceptions=True)


async def test_feed_silent_too_long_reconnects(client: CadenceClient, backend: FakeCadence) -> None:
    handler, sleeps = Recorder(backend), Sleeps()
    with patch.object(cadence_feed, "FEED_IDLE_TIMEOUT", 0.3):
        task = asyncio.create_task(feed(client, handler, sleeps).run())
        try:
            await wait_for(lambda: len(handler.calls) >= 3)
            assert handler.calls[:3] == ["connected subscribes=1", "down timeout", "connected subscribes=2"]
        finally:
            task.cancel()
            await asyncio.gather(task, return_exceptions=True)


def test_the_client_runs_without_home_assistant() -> None:
    """The e2e smoke loads models, client, feed and reads as a package of their own, with no Home Assistant."""
    component = Path(__file__).parents[1] / "custom_components" / "cadence"
    script = "\n".join(
        [
            "import importlib, sys, types",
            "class NoHomeAssistant:",
            "    def find_spec(self, name, path=None, target=None):",
            "        if name.partition('.')[0] == 'homeassistant':",
            "            raise ImportError(name)",
            "sys.meta_path.insert(0, NoHomeAssistant())",
            "package = types.ModuleType('cadence')",
            f"package.__path__ = [{str(component)!r}]",
            "sys.modules['cadence'] = package",
            "for name in ('models', 'client', 'feed', 'reads'):",
            "    importlib.import_module('cadence.' + name)",
        ]
    )
    subprocess.run([sys.executable, "-I", "-c", script], check=True, timeout=30)  # noqa: S603


class FailingBackoff(Backoff):
    """A backoff whose next delay fails once, as the old one overflowed after a long outage."""

    def __init__(self) -> None:
        super().__init__(rand=lambda: 0.0)
        self.failures = 1

    def next(self) -> float:
        if self.failures:
            self.failures -= 1
            raise OverflowError("int too large to convert to float")
        return super().next()


async def test_feed_survives_faults_in_its_reconnect_path(
    client: CadenceClient, backend: FakeCadence, caplog: pytest.LogCaptureFixture
) -> None:
    handler, sleeps = Recorder(backend), Sleeps()
    handler.fail_down = RuntimeError("state of Kitchen")
    task = asyncio.create_task(LiveFeed(client, handler, backoff=FailingBackoff(), sleep=sleeps).run())
    try:
        await wait_for(lambda: handler.calls == ["connected subscribes=1"])
        await backend.close_feeds(1001)
        await wait_for(lambda: handler.calls[-1:] == ["connected subscribes=2"])
        await backend.close_feeds(1001)
        await wait_for(lambda: handler.calls[-1:] == ["connected subscribes=3"])
        assert handler.calls == [
            "connected subscribes=1",
            "down closed",
            "connected subscribes=2",
            "down closed",
            "connected subscribes=3",
        ]
        # the failed delay falls back to the longest one, and the next is the backoff's own
        assert sleeps.delays == [cadence_feed.FEED_FALLBACK_DELAY, 1]
        assert "cadence_feed_failed code=handler error=RuntimeError" in caplog.text
        assert "cadence_feed_failed code=backoff error=OverflowError" in caplog.text
        assert "Kitchen" not in caplog.text
        assert not task.done()
    finally:
        task.cancel()
        await asyncio.gather(task, return_exceptions=True)
