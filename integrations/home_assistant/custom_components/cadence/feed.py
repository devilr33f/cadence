"""The server's live feed: subscribed, read again after every (re)connect, reconnected with backoff (M8-D7)."""

from __future__ import annotations

import asyncio
import json
import logging
import random
from collections.abc import Awaitable, Callable
from typing import Any, Protocol

import aiohttp

from .client import (
    REQUEST_TIMEOUT,
    CadenceAuthError,
    CadenceClient,
    CadenceConnectionError,
    CadenceError,
    connection_error,
)
from .models import FeedEvent, code_token

_LOGGER = logging.getLogger(__name__)

# the server pings its feed every 20 s: one silent this long is gone
FEED_IDLE_TIMEOUT = 60.0
TOPICS = ("devices", "media", "vitals", "groups", "alarms")
SUBSCRIBE = json.dumps({"subscribe": list(TOPICS)}, separators=(",", ":"))
# the server closes a feed whose credential no longer holds with this
CLOSE_POLICY_VIOLATION = 1008
# a connection that stayed up this long starts the delays over: one the server accepts and drops at once does not
FEED_STABLE = 60.0
# the delay when the backoff itself failed
FEED_FALLBACK_DELAY = 30.0


class FeedHandler(Protocol):
    """What a live feed tells: it connected (state must be read again), an event, it went down."""

    async def feed_connected(self) -> None: ...

    async def feed_event(self, event: FeedEvent) -> None: ...

    def feed_disconnected(self, code: str) -> None: ...


class Backoff:
    """Reconnect delays from 1 s doubling to 30 s, each a random point of its upper half."""

    def __init__(self, base: float = 1.0, cap: float = 30.0, rand: Callable[[], float] = random.random) -> None:
        self._base, self._cap, self._rand = base, cap, rand
        self._ceiling = 0.0

    def next(self) -> float:
        # the ceiling doubles and stops at the cap: no exponent grows, however long the outage
        self._ceiling = min(self._cap, self._ceiling * 2 if self._ceiling else self._base * 2)
        return self._ceiling / 2 + self._rand() * self._ceiling / 2

    def reset(self) -> None:
        self._ceiling = 0.0


class LiveFeed:
    """Keeps the server's live feed: subscribes, has the handler read the state again after every (re)connect, hands
    it each event, and reconnects with backoff, whose delays start over once a connection stayed up a minute. Ends
    only on an auth failure, which it raises: never a retry loop."""

    def __init__(
        self,
        client: CadenceClient,
        handler: FeedHandler,
        *,
        backoff: Backoff | None = None,
        sleep: Callable[[float], Awaitable[None]] = asyncio.sleep,
    ) -> None:
        self._client = client
        self._handler = handler
        self._backoff = backoff or Backoff()
        self._sleep = sleep
        # when the current connection's state was read
        self._up_since: float | None = None

    async def run(self) -> None:
        """Only an auth failure (raised) or a cancellation ends it: any other fault is logged by code and retried."""
        while True:
            code = "closed"
            try:
                await self._follow()
            except CadenceAuthError as err:
                self._down(err.code)
                raise
            except CadenceError as err:
                code = err.code
            except (aiohttp.ClientError, TimeoutError, OSError) as err:
                code = connection_error(err).code
            except Exception as err:
                # a fault of ours must not end the feed for good; its type alone, an error's text may carry data
                _LOGGER.error("cadence_feed_failed code=unexpected error=%s", type(err).__name__)
                code = "unexpected"
            self._down(code)
            await self._pause()

    def _down(self, code: str) -> None:
        try:
            self._handler.feed_disconnected(code)
        except Exception as err:
            _LOGGER.error("cadence_feed_failed code=handler error=%s", type(err).__name__)

    async def _pause(self) -> None:
        loop = asyncio.get_running_loop()
        if self._up_since is not None and loop.time() - self._up_since >= FEED_STABLE:
            self._backoff.reset()
        self._up_since = None
        try:
            delay = self._backoff.next()
        except Exception as err:
            _LOGGER.error("cadence_feed_failed code=backoff error=%s", type(err).__name__)
            delay = FEED_FALLBACK_DELAY
        try:
            await self._sleep(delay)
        except Exception as err:
            _LOGGER.error("cadence_feed_failed code=sleep error=%s", type(err).__name__)
            await asyncio.sleep(FEED_FALLBACK_DELAY)

    async def _follow(self) -> None:
        ws = await self._client.connect_feed()
        try:
            async with asyncio.timeout(REQUEST_TIMEOUT):
                await self._subscribe(ws)
            await self._handler.feed_connected()
            self._up_since = asyncio.get_running_loop().time()
            while True:
                frame = await _receive(ws, FEED_IDLE_TIMEOUT)
                try:
                    event = FeedEvent.from_json(frame)
                except ValueError:
                    _LOGGER.debug("cadence_feed_frame_invalid")
                    continue
                await self._handler.feed_event(event)
        finally:
            await ws.close()

    async def _subscribe(self, ws: aiohttp.ClientWebSocketResponse) -> None:
        await _receive(ws)
        await ws.send_str(SUBSCRIBE)
        while True:
            frame = await _receive(ws)
            if not isinstance(frame, dict):
                continue
            if frame.get("type") == "subscribed":
                return
            if isinstance(error := frame.get("error"), str):
                # the subscription before stays: an ha token's default topics
                _LOGGER.warning("cadence_feed_subscribe_refused code=%s", code_token(error) or "invalid")
                return
            # an event before the subscription is answered: the read that follows covers it


async def _receive(ws: aiohttp.ClientWebSocketResponse, idle: float | None = None) -> Any:
    """The next text frame, decoded; a close raises (1008: the token no longer holds), and so does idle seconds of
    silence."""
    while True:
        # each frame, the server's pings too, starts the wait again
        msg = await ws.receive(timeout=idle)
        if msg.type is aiohttp.WSMsgType.TEXT:
            try:
                return json.loads(msg.data)
            except ValueError:
                _LOGGER.debug("cadence_feed_frame_invalid")
                continue
        if msg.type is aiohttp.WSMsgType.BINARY:
            continue
        code = ws.close_code if msg.type is not aiohttp.WSMsgType.CLOSE else msg.data
        if code == CLOSE_POLICY_VIOLATION:
            raise CadenceAuthError("unauthorized", None)
        raise CadenceConnectionError("closed")
