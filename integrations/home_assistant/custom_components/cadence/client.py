"""A Cadence v2 server's REST API for an ha token: its reads and calls.

client.py, feed.py and models.py import nothing from Home Assistant, so the e2e smoke runs them without it. Errors
carry a code, never the token, a url or upstream text.
"""

from __future__ import annotations

import asyncio
from collections.abc import Callable, Mapping
from typing import Any
from urllib.parse import quote

import aiohttp

from .models import (
    Artwork,
    CatalogAlternative,
    CatalogDetail,
    CatalogItem,
    CatalogOptions,
    CatalogPage,
    CatalogSession,
    Device,
    DeviceDetail,
    DeviceGroup,
    MediaSession,
    Vitals,
    code_token,
)

API = "/api/v1"
# each REST call, and a feed's handshake up to its subscription
REQUEST_TIMEOUT = 10.0
MAX_ARTWORK_BYTES = 5 * 1024 * 1024
ARTWORK_TYPES = frozenset({"image/jpeg", "image/png", "image/webp", "image/gif"})


class CadenceError(Exception):
    """A failed call, by code."""

    def __init__(self, code: str) -> None:
        super().__init__(code)
        self.code = code


class CadenceConnectionError(CadenceError):
    """The server could not be reached, or answered nothing the client can read."""


class CadenceApiError(CadenceError):
    """The server refused a call: its status, error code and, when it gave one, the detail token that refines it (a
    device's own code)."""

    def __init__(self, code: str, status: int | None, detail: str = "") -> None:
        super().__init__(code)
        self.status = status
        self.detail = detail


class CadenceAuthError(CadenceApiError):
    """The token is refused: 401, or a live feed the server closed with 1008."""


class CadenceScopeError(CadenceAuthError):
    """The token lacks the scope a call needs (403)."""


def _segment(value: str) -> str:
    return quote(value, safe="")


def _choice_params(choices: Mapping[str, str]) -> list[str]:
    return [f"{kind}={choice}" for kind, choice in sorted(choices.items())]


class CadenceClient:
    """One Cadence server reached with one token, through the caller's session (which verifies TLS)."""

    def __init__(self, session: aiohttp.ClientSession, url: str, token: str) -> None:
        self._session = session
        self._base = url.rstrip("/")
        self._headers = {"Authorization": f"Bearer {token}"}

    async def list_devices(self) -> list[Device]:
        body = await self._json("GET", "/devices")
        if not isinstance(body, list):
            raise CadenceConnectionError("invalid")
        return [_parsed(Device.from_json, d) for d in body]

    async def reaches_tokens(self) -> bool:
        """Whether the token reaches the admin-only token list, i.e. it is wider than ha: False for the 403 an ha token
        gets; any other refusal raises. The list's body is dropped unread."""
        try:
            await self._json("GET", "/tokens")
        except CadenceScopeError:
            return False
        return True

    async def get_device(self, device_id: str) -> DeviceDetail:
        return _parsed(DeviceDetail.from_json, await self._json("GET", f"/devices/{_segment(device_id)}"))

    async def get_media(self, device_id: str) -> MediaSession | None:
        return _parsed(MediaSession.from_json, await self._json("GET", f"/devices/{_segment(device_id)}/media"))

    async def get_vitals(self, device_id: str) -> Vitals | None:
        """The device's latest vitals, None until its next metrics round (404 no_vitals)."""
        try:
            body = await self._json("GET", f"/devices/{_segment(device_id)}/vitals")
        except CadenceApiError as err:
            if err.status == 404 and err.code == "no_vitals":
                return None
            raise
        return _parsed(Vitals.from_json, body)

    async def send_command(
        self, device_id: str, capability: str, name: str, args: Mapping[str, Any] | None = None
    ) -> Any:
        """Runs a capability command on the device and returns its result."""
        path = f"/devices/{_segment(device_id)}/capabilities/{_segment(capability)}/commands/{_segment(name)}"
        body = await self._json("POST", path, dict(args or {}))
        return body.get("result") if isinstance(body, dict) else None

    async def join_group(self, device_id: str, members: list[str]) -> DeviceGroup:
        """Makes the device lead a group with these devices (its own group extended, or a new one); each leaves the
        group it was in. The group as it is now."""
        body = await self._json("POST", f"/devices/{_segment(device_id)}/group", {"members": list(members)})
        return _parsed(DeviceGroup.from_view, body)

    async def leave_group(self, device_id: str) -> None:
        """Takes the device out of its group (a device in no group too)."""
        await self._json("DELETE", f"/devices/{_segment(device_id)}/group", empty=True)

    async def announce(self, device_id: str, text: str, language: str | None = None) -> str:
        """Speaks text on the device; returns the stream's id."""
        payload: dict[str, str] = {"text": text}
        if language:
            payload["language"] = language
        body = await self._json("POST", f"/devices/{_segment(device_id)}/announce", payload)
        stream = body.get("streamId") if isinstance(body, dict) else None
        return stream if isinstance(stream, str) else ""

    async def get_artwork(self, device_id: str, artwork_hash: str) -> Artwork | None:
        """The image of the device's session art, if it still is the one artwork_hash names: None for no image."""
        return await self._image(f"/devices/{_segment(device_id)}/artwork", {"h": artwork_hash})

    async def catalog_image(self, image_id: str) -> Artwork | None:
        """An image of the catalog's proxy by the id an item carried: None for no image."""
        return await self._image(f"/catalog/images/{_segment(image_id)}")

    # a longer read: a browse fans out to every provider, a search waits for each
    CATALOG_TIMEOUT = 30.0

    async def catalog_browse(self, path: str, cursor: str = "") -> CatalogPage:
        """The catalog's Home (an empty path: every provider's root sections) or one provider's feed or tree node."""
        params: dict[str, str] = {}
        if path:
            params["path"] = path
        if cursor:
            params["cursor"] = cursor
        body = await self._json("GET", "/catalog/browse", params=params, seconds=self.CATALOG_TIMEOUT)
        return _parsed(CatalogPage.from_json, body)

    async def catalog_item(
        self, ref: str, cursor: str = "", group: str = "", choices: Mapping[str, str] | None = None
    ) -> CatalogDetail:
        """An item read: its children (a page), child groups and variants, at the source choices already made."""
        params: dict[str, str | list[str]] = {}
        if cursor:
            params["cursor"] = cursor
        if group:
            params["group"] = group
        if choices:
            params["choice"] = _choice_params(choices)
        body = await self._json("GET", f"/catalog/items/{_segment(ref)}", params=params, seconds=self.CATALOG_TIMEOUT)
        return _parsed(CatalogDetail.from_json, body)

    async def catalog_options(
        self, ref: str, episode: str = "", choices: Mapping[str, str] | None = None
    ) -> CatalogOptions:
        """The choices an item offers for an episode, at the selection already made."""
        params: dict[str, str | list[str]] = {}
        if episode:
            params["episode"] = episode
        if choices:
            params["choice"] = _choice_params(choices)
        body = await self._json(
            "GET", f"/catalog/items/{_segment(ref)}/options", params=params, seconds=self.CATALOG_TIMEOUT
        )
        return _parsed(CatalogOptions.from_json, body)

    async def catalog_alternatives(self, ref: str) -> list[CatalogAlternative]:
        """Every provider and sub-source the title is available on, with its offer."""
        body = await self._json("GET", f"/catalog/items/{_segment(ref)}/alternatives", seconds=self.CATALOG_TIMEOUT)
        items = body.get("items") if isinstance(body, dict) else None
        if not isinstance(items, list):
            raise CadenceConnectionError("invalid")
        return _parsed(lambda _: [CatalogAlternative.from_json(i) for i in items], body)

    async def catalog_session(self, device_id: str) -> CatalogSession | None:
        """The device's current catalog session and options; None for playback the catalog does not own."""
        try:
            body = await self._json(
                "GET", f"/devices/{_segment(device_id)}/catalog/session", seconds=self.CATALOG_TIMEOUT
            )
        except CadenceApiError as err:
            if (err.status == 404 and err.code == "no_catalog_session") or (
                err.status == 422 and err.code == "unsupported"
            ):
                return None
            raise
        return _parsed(CatalogSession.from_json, body)

    async def select_catalog(self, device_id: str, group: str, choice: str) -> None:
        """Switch the playing title's source, voiceover or quality, keeping its position and remembering the choice."""
        await self._json(
            "POST",
            f"/devices/{_segment(device_id)}/catalog/select",
            {"group": group, "choice": choice},
            seconds=self.CATALOG_TIMEOUT,
        )

    async def catalog_search(self, query: str) -> list[CatalogItem]:
        """A search over every provider, its events' items in the order they came."""
        body = await self._json("GET", "/catalog/search", params={"q": query}, seconds=self.CATALOG_TIMEOUT)
        events = body.get("events") if isinstance(body, dict) else None
        if not isinstance(events, list):
            raise CadenceConnectionError("invalid")
        items: list[CatalogItem] = []
        by_ref: dict[str, int] = {}
        for event in events:
            if not isinstance(event, dict):
                raise CadenceConnectionError("invalid")
            if not isinstance(event.get("items"), list) and not isinstance(event.get("updated"), list):
                # a search's first event names the sources asked, its last marks the end
                continue
            # updated carries a title group enriched by a later source; replace its first row in place
            batches = [event[key] for key in ("items", "updated") if isinstance(event.get(key), list)]
            for batch in batches:
                for it in _item_list(batch):
                    if (at := by_ref.get(it.ref)) is not None:
                        items[at] = it
                    else:
                        by_ref[it.ref] = len(items)
                        items.append(it)
        return items

    async def play_catalog(
        self, device_id: str, ref: str, episode: str = "", choices: Mapping[str, str] | None = None
    ) -> None:
        """Plays a catalog ref with the exact choices made; a successful resolve remembers them for the title."""
        payload: dict[str, Any] = {"ref": ref}
        if episode:
            payload["episode"] = episode
        if choices:
            payload["choices"] = dict(choices)
        await self._json("POST", f"/devices/{_segment(device_id)}/catalog/play", payload, seconds=self.CATALOG_TIMEOUT)

    async def _image(self, path: str, params: dict[str, str] | None = None) -> Artwork | None:
        try:
            async with asyncio.timeout(REQUEST_TIMEOUT):
                async with self._session.get(
                    self._url(path), headers=self._headers, params=params, allow_redirects=False
                ) as resp:
                    if resp.status in (404, 409, 502):
                        return None
                    if resp.status != 200:
                        raise _refused(resp.status, *await _error_body(resp))
                    content_type = resp.content_type
                    if content_type not in ARTWORK_TYPES or (resp.content_length or 0) > MAX_ARTWORK_BYTES:
                        return None
                    data = bytearray()
                    async for chunk in resp.content.iter_chunked(64 * 1024):
                        data += chunk
                        if len(data) > MAX_ARTWORK_BYTES:
                            return None
                    return Artwork(content_type=content_type, data=bytes(data))
        except (aiohttp.ClientError, TimeoutError, OSError) as err:
            raise connection_error(err) from None

    def _url(self, path: str) -> str:
        return f"{self._base}{API}{path}"

    async def _json(
        self,
        method: str,
        path: str,
        payload: Any = None,
        params: dict[str, str | list[str]] | None = None,
        seconds: float | None = None,
        empty: bool = False,
    ) -> Any:
        try:
            async with asyncio.timeout(REQUEST_TIMEOUT if seconds is None else seconds):
                async with self._session.request(
                    method, self._url(path), headers=self._headers, json=payload, params=params, allow_redirects=False
                ) as resp:
                    if resp.status >= 300:
                        raise _refused(resp.status, *await _error_body(resp))
                    if empty:
                        # a 204: there is no body to read
                        return None
                    try:
                        return await resp.json(content_type=None)
                    except ValueError:
                        raise CadenceConnectionError("invalid") from None
        except (aiohttp.ClientError, TimeoutError, OSError) as err:
            raise connection_error(err) from None

    async def connect_feed(self) -> aiohttp.ClientWebSocketResponse:
        """Opens the live feed (no subscription yet)."""
        try:
            async with asyncio.timeout(REQUEST_TIMEOUT):
                return await self._session.ws_connect(self._url("/live"), headers=self._headers)
        except aiohttp.WSServerHandshakeError as err:
            raise _refused(err.status, "") from None
        except (aiohttp.ClientError, TimeoutError, OSError) as err:
            raise connection_error(err) from None


def _parsed[T](parse: Callable[[Any], T], data: Any) -> T:
    try:
        return parse(data)
    except ValueError, AttributeError, TypeError:
        raise CadenceConnectionError("invalid") from None


def _item_list(value: Any) -> list[CatalogItem]:
    if not isinstance(value, list):
        raise CadenceConnectionError("invalid")
    return [CatalogItem.from_json(i) for i in value]


def _refused(status: int, code: str, detail: str = "") -> CadenceApiError:
    if status == 401:
        return CadenceAuthError(code or "unauthorized", status, detail)
    if status == 403:
        return CadenceScopeError(code or "forbidden", status, detail)
    return CadenceApiError(code or f"http_{status}", status, detail)


def connection_error(err: BaseException) -> CadenceConnectionError:
    if isinstance(err, aiohttp.ClientSSLError):
        return CadenceConnectionError("tls")
    if isinstance(err, TimeoutError):
        return CadenceConnectionError("timeout")
    return CadenceConnectionError("connect")


async def _error_body(resp: aiohttp.ClientResponse) -> tuple[str, str]:
    """The error model's code and detail, each only when it is a token (never free text)."""
    try:
        body = await resp.json(content_type=None)
    except ValueError, aiohttp.ClientError:
        return "", ""
    if not isinstance(body, dict):
        return "", ""
    return code_token(body.get("error")), code_token(body.get("detail"))
