"""A Cadence server over TLS for the tests: the routes an ha token reaches, answering with the contract fixtures the
server's Go test writes (server/internal/app/hafixtures_test.go)."""

from __future__ import annotations

import asyncio
import copy
import json
import socket
import ssl
from collections.abc import Callable
from pathlib import Path
from typing import Any

from aiohttp import WSMsgType, web

FIXTURES = Path(__file__).parent / "fixtures"
TOKEN = "cdn_test_8f2b1c"
KITCHEN = "0b6f3c52-7d1e-4a8b-9f2c-5e4d3a2b1c0d"
HALL = "5a9e7f10-2c3d-4e5f-8a6b-7c8d9e0f1a2b"


def fixture(name: str) -> Any:
    """A fixture, parsed: each call a fresh copy."""
    return json.loads((FIXTURES / name).read_text(encoding="utf-8"))


# the artworkHash of Kitchen's session in the fixtures
ART = fixture("media_playing.json")["metadata"]["artworkHash"]


def frame(fixture_name: str, device_id: str | None = None, /, **data: Any) -> dict[str, Any]:
    """A feed frame fixture, for another device or with its data's fields replaced."""
    out = fixture(fixture_name)
    if device_id is not None:
        out["deviceId"] = device_id
    if data:
        out["data"] = {**out.get("data", {}), **data}
    return out


def dropped(count: int) -> dict[str, Any]:
    """The frame the feed sends when it lost count events for its client: no fixture can catch one, so its bytes are
    pinned by server/internal/events TestFeedReportsDrops instead."""
    return {"type": "dropped", "count": count}


def _error(status: int, code: str) -> web.Response:
    return web.json_response({"error": code}, status=status)


# the catalog's shapes as the server writes them (its PageView, DetailView and Item), small and hand-written: the
# contract fixtures hold the session view alone
POSTER = "Zm1Ub0NhdENkNjRWY2hmQ2Fk"


def _item(ref: str, kind: str, title: str, **extra: Any) -> dict[str, Any]:
    return {"ref": ref, "provider": "torrplayer", "kind": kind, "title": title, "playable": True, **extra}


def home_page() -> dict[str, Any]:
    """The catalog's home: torrplayer's continue row and my list."""
    return {
        "sections": [
            {
                "id": "torrplayer/continue",
                "provider": "torrplayer",
                "title": "Продолжить",
                "path": "torrplayer/continue",
                "items": [
                    _item(
                        "c:series",
                        "title_group",
                        "Rebuilding a World",
                        subtitle="anime365 · 12 эп.",
                        artwork={"hash": POSTER, "aspect": "poster"},
                        hasChildren=True,
                    )
                ],
            },
            {
                "id": "torrplayer/my_list",
                "provider": "torrplayer",
                "title": "Мой список",
                "path": "torrplayer/my_list",
                "items": [
                    _item("c:movie", "title_group", "A Field at Night", artwork={"hash": POSTER}, hasChildren=True)
                ],
            },
        ]
    }


def feed_pages() -> dict[str, Any]:
    """The home's own sections, each browsable alone."""
    home = home_page()
    return {section["path"]: {"sections": [section]} for section in home["sections"]}


def series_detail() -> dict[str, Any]:
    """The title group's read: two source variants, a season group and its episodes."""
    episodes = [
        _item("c:ep6", "episode", "Reunion", episode={"label": "6", "ordinal": 6, "name": "Reunion"}, durationS=1420),
        _item(
            "c:ep7",
            "episode",
            "The Guild Advisor",
            episode={"label": "7", "ordinal": 7, "name": "The Guild Advisor"},
            durationS=1420,
        ),
    ]
    return {
        "item": _item("c:series", "title_group", "Rebuilding a World", artwork={"hash": POSTER}, hasChildren=True),
        "children": episodes,
        "childGroups": [{"id": "s1", "label": "Сезон 1", "count": 12}],
        "variants": [
            _item("c:anime365", "series", "Rebuilding a World", source="anime365", hasChildren=True),
            _item("c:aniliberty", "series", "Rebuilding a World", source="aniliberty", hasChildren=True),
        ],
        "selected": "c:anime365",
        "panels": [],
        "actions": [],
    }


def catalog_options(ref: str, selected: dict[str, str] | None = None) -> dict[str, Any]:
    """The title's source, voiceover and quality groups, with the selection a read or play carries."""
    picked = {"source": "anime365", "voiceover": "AniLibria", "quality": "1080", **(selected or {})}
    groups = [
        {
            "kind": "voiceover",
            "label": "Voiceover",
            "choices": [
                {"id": "AniLibria", "label": "AniLibria", "stream": {"quality": 1080}},
                {"id": "Crunchyroll", "label": "Crunchyroll", "stream": {"quality": 720}},
                {"id": "dead", "label": "Unavailable dub", "unavailable": True, "reason": "no_seeders"},
            ],
        },
        {
            "kind": "quality",
            "label": "Quality",
            "choices": [{"id": "1080", "label": "1080p"}, {"id": "720", "label": "720p"}],
        },
    ]
    if ref == "c:series":
        groups.insert(
            0,
            {
                "kind": "source",
                "label": "Source",
                "choices": [
                    {"id": "anime365", "label": "anime365", "detail": "12 ep."},
                    {"id": "aniliberty", "label": "AniLiberty", "detail": "12 ep."},
                ],
            },
        )
    return {"groups": groups, "selected": picked, "remembered": ["voiceover"] if not selected else []}


def alternatives() -> dict[str, Any]:
    return {
        "items": [
            {
                "ref": "c:anime365",
                "provider": "torrplayer",
                "source": "anime365",
                "sourceName": "anime365",
                "title": "Rebuilding a World",
                "match": "shikimori",
                "episodesAired": 12,
                "episodesTotal": 12,
                "voiceovers": [{"label": "AniLibria", "quality": 1080}],
                "quality": 1080,
                "current": True,
            },
            {
                "ref": "c:aniliberty",
                "provider": "torrplayer",
                "source": "aniliberty",
                "sourceName": "AniLiberty",
                "title": "Rebuilding a World",
                "match": "shikimori",
                "episodesAired": 12,
                "voiceovers": [],
            },
            {
                "ref": "c:torrent",
                "provider": "torrserver",
                "sourceName": "torrserver",
                "title": "Rebuilding a World",
                "match": "name",
                "quality": 2160,
                "voiceovers": [],
            },
        ],
        "asked": 3,
        "failed": 0,
    }


class FakeCadence:
    """The server's state, read through its routes: a test changes it, then pushes the event that says so."""

    def __init__(self, ssl_context: ssl.SSLContext) -> None:
        self._ssl = ssl_context
        self._runner: web.AppRunner | None = None
        self.url = ""
        self.token = TOKEN
        # 401 or 403 for every call; the feed's upgrade too
        self.refuse: int | None = None
        # answer the REST reads with this status (a server that is restarting)
        self.unavailable = False
        self.devices: dict[str, dict[str, Any]] = {d["id"]: d for d in fixture("devices.json")}
        self.details: dict[str, dict[str, Any]] = {
            KITCHEN: fixture("device_kitchen.json"),
            HALL: fixture("device_hall.json"),
        }
        self.media: dict[str, dict[str, Any]] = {KITCHEN: fixture("media_playing.json")}
        self.vitals: dict[str, dict[str, Any]] = {KITCHEN: fixture("vitals.json")}
        self.requests: list[str] = []
        self._stamps: list[tuple[float, str]] = []
        self.bearers: set[str] = set()
        self.subscribes: list[str] = []
        self.connects = 0
        self.feeds: list[web.WebSocketResponse] = []
        self.commands: list[tuple[str, str, str, Any]] = []
        self.command_answer: tuple[int, Any] = (200, {"result": {}})
        self.announces: list[tuple[str, Any]] = []
        self.announce_answer: tuple[int, Any] = (202, {"streamId": "st-1"})
        # a device's own answer, before announce_answer
        self.announce_answers: dict[str, tuple[int, Any]] = {}
        self.artwork: tuple[int, bytes, str] = (200, b"\x89PNG fake", "image/png")
        self.artwork_asks: list[str] = []
        # send the image without a Content-Length
        self.artwork_chunked = False
        # the catalog: browse pages by path ("" the home), item details by ref, the search's events, the image
        # proxy's images by id, and the plays asked of devices
        self.catalog_pages: dict[str, Any] = {"": home_page(), **feed_pages()}
        self.catalog_items: dict[str, Any] = {"c:series": series_detail()}
        for ref, source in (("c:anime365", "anime365"), ("c:aniliberty", "aniliberty")):
            detail = series_detail()
            detail["item"] = _item(ref, "series", "Rebuilding a World", source=source, hasChildren=True)
            detail["variants"] = []
            self.catalog_items[ref] = detail
        self.catalog_items["c:torrent"] = {
            "item": _item("c:torrent", "movie", "Rebuilding a World", provider="torrserver"),
            "children": [],
            "childGroups": [],
            "variants": [],
            "panels": [],
            "actions": [],
        }
        self.catalog_search_answer: Any = {"events": []}
        self.catalog_searches: list[str] = []
        self.catalog_image: tuple[int, bytes, str] = (200, b"\x89PNG poster", "image/png")
        self.catalog_image_asks: list[str] = []
        self.catalog_plays: list[tuple[str, Any]] = []
        self.catalog_play_answer: tuple[int, Any] = (200, {"sessionId": "video-1", "episode": "7"})
        self.catalog_item_choices: list[tuple[str, dict[str, str]]] = []
        self.catalog_option_asks: list[tuple[str, dict[str, str]]] = []
        self.catalog_alternative_asks: list[str] = []
        self.catalog_session_asks: list[str] = []
        self.catalog_selects: list[tuple[str, Any]] = []
        self.catalog_select_answer: tuple[int, Any] = (200, {"sessionId": "video-32", "episode": "7"})
        # held REST reads wait for this, so a test sees what the client does meanwhile
        self.hold: asyncio.Event | None = None
        self.answer_delay = 0.0
        # a server without the vitals route answers its 404 for an unknown route
        self.vitals_route = True
        # the token's scopes: the admin-only token list answers 200 to admin, 403 to the rest
        self.token_scopes: set[str] = {"ha"}
        # the token list's answer, before token_scopes
        self.tokens_answer: tuple[int, Any] | None = None
        # multiroom: each join asked (the leading device, its body) and its answer, each leave asked
        self.joins: list[tuple[str, Any]] = []
        self.join_answer: tuple[int, Any] = (200, fixture("group_joined.json"))
        self.leaves: list[str] = []

    def add_device(self, device_id: str, name: str, *, status: str = "enrolled") -> None:
        """A device just enrolled (or pending): it never linked, so it has no capabilities yet."""
        listed = {**fixture("devices.json")[1], "id": device_id, "name": name, "status": status}
        self.devices[device_id] = listed
        self.details[device_id] = {**fixture("device_hall.json"), "id": device_id, "name": name, "status": status}

    def enrol_device(self, device_id: str) -> None:
        """A pending device enrolled with its code."""
        self.devices[device_id]["status"] = "enrolled"
        self.details[device_id]["status"] = "enrolled"

    def link_device(self, device_id: str) -> None:
        """The device linked and reported the capabilities Kitchen has."""
        self.devices[device_id]["online"] = True
        self.details[device_id]["capabilities"] = fixture("device_kitchen.json")["capabilities"]

    def link_screen(self, device_id: str) -> None:
        """The device linked as a screen: the speaker's capabilities and media.video, the catalog's player."""
        self.link_device(device_id)
        self.details[device_id]["capabilities"] = [
            *self.details[device_id]["capabilities"],
            {"id": "media.video", "commands": [{"name": "play"}, {"name": "tracks"}, {"name": "select_track"}]},
        ]

    def remove_device(self, device_id: str) -> None:
        for table in (self.devices, self.details, self.media, self.vitals):
            table.pop(device_id, None)

    def rename_device(self, device_id: str, name: str) -> None:
        self.devices[device_id]["name"] = name
        self.details[device_id]["name"] = name

    def set_group(self, group: dict[str, Any] | None, *device_ids: str) -> None:
        """The devices' group field as the list and the detail show it (None: in no group)."""
        for device_id in device_ids:
            for table in (self.devices, self.details):
                if group is None:
                    table[device_id].pop("group", None)
                else:
                    table[device_id]["group"] = copy.deepcopy(group)

    def stamps(self, request: str) -> list[float]:
        """When each such request arrived, on the loop clock."""
        return [at for at, r in self._stamps if r == request]

    def detail(self, device_id: str) -> dict[str, Any]:
        out = copy.deepcopy(self.details[device_id])
        out["online"] = self.devices[device_id]["online"]
        return out

    def capability(self, device_id: str, cap_id: str) -> dict[str, Any]:
        """The device's capability as its detail lists it, to change in place."""
        return next(cap for cap in self.details[device_id]["capabilities"] if cap["id"] == cap_id)

    def set_volume(self, device_id: str, level: int) -> None:
        for cap in self.details[device_id]["capabilities"]:
            if cap["id"] == "audio.output":
                cap["state"]["level"] = level

    async def start(self) -> FakeCadence:
        app = web.Application(middlewares=[self._gate])
        app.router.add_get("/api/v1/devices", self._list)
        app.router.add_get("/api/v1/devices/{id}", self._detail)
        app.router.add_get("/api/v1/devices/{id}/media", self._media)
        app.router.add_get("/api/v1/devices/{id}/vitals", self._vitals)
        app.router.add_get("/api/v1/devices/{id}/artwork", self._artwork)
        app.router.add_get("/api/v1/catalog/browse", self._catalog_browse)
        app.router.add_get("/api/v1/catalog/items/{ref}", self._catalog_item)
        app.router.add_get("/api/v1/catalog/search", self._catalog_search)
        app.router.add_get("/api/v1/catalog/items/{ref}/options", self._catalog_options)
        app.router.add_get("/api/v1/catalog/items/{ref}/alternatives", self._catalog_alternatives)
        app.router.add_get("/api/v1/devices/{id}/catalog/session", self._catalog_session)
        app.router.add_post("/api/v1/devices/{id}/catalog/select", self._catalog_select)
        app.router.add_get("/api/v1/catalog/images/{image_id}", self._catalog_image)
        app.router.add_post("/api/v1/devices/{id}/catalog/play", self._catalog_play)
        app.router.add_post("/api/v1/devices/{id}/capabilities/{cap}/commands/{name}", self._command)
        app.router.add_post("/api/v1/devices/{id}/announce", self._announce)
        app.router.add_post("/api/v1/devices/{id}/group", self._join)
        app.router.add_delete("/api/v1/devices/{id}/group", self._leave)
        app.router.add_get("/api/v1/tokens", self._tokens)
        app.router.add_get("/api/v1/live", self._live)
        self._runner = web.AppRunner(app, handle_signals=False)
        await self._runner.setup()
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        sock.bind(("127.0.0.1", 0))
        site = web.SockSite(self._runner, sock, ssl_context=self._ssl)
        await site.start()
        self.url = f"https://127.0.0.1:{sock.getsockname()[1]}"
        return self

    async def close(self) -> None:
        await self.close_feeds(1001)
        if self._runner is not None:
            await self._runner.cleanup()

    @web.middleware
    async def _gate(self, request: web.Request, handler: Callable) -> web.StreamResponse:
        self.requests.append(f"{request.method} {request.path}")
        self._stamps.append((asyncio.get_running_loop().time(), self.requests[-1]))
        self.bearers.add(request.headers.get("Authorization", ""))
        if request.headers.get("Authorization") != f"Bearer {self.token}":
            return _error(401, "unauthorized")
        if self.refuse == 403:
            return _error(403, "forbidden")
        if self.refuse == 401:
            return _error(401, "unauthorized")
        if request.path != "/api/v1/live":
            if self.unavailable:
                return _error(503, "unavailable")
            if self.hold is not None:
                await self.hold.wait()
            if self.answer_delay:
                await asyncio.sleep(self.answer_delay)
        return await handler(request)

    def _known(self, request: web.Request) -> str | None:
        device_id = request.match_info["id"]
        return device_id if device_id in self.devices else None

    async def _tokens(self, _: web.Request) -> web.Response:
        if self.tokens_answer is not None:
            status, body = self.tokens_answer
            return web.json_response(body, status=status)
        if "admin" not in self.token_scopes:
            return _error(403, "forbidden")
        return web.json_response([{"name": "owner", "scopes": ["admin"], "createdAt": "2026-11-12T10:00:00Z"}])

    async def _list(self, _: web.Request) -> web.Response:
        return web.json_response(list(self.devices.values()))

    async def _detail(self, request: web.Request) -> web.Response:
        if (device_id := self._known(request)) is None:
            return _error(404, "not_found")
        return web.json_response(self.detail(device_id))

    async def _media(self, request: web.Request) -> web.Response:
        if (device_id := self._known(request)) is None:
            return _error(404, "not_found")
        return web.json_response(self.media.get(device_id, fixture("media_idle.json")))

    async def _vitals(self, request: web.Request) -> web.Response:
        if (device_id := self._known(request)) is None or not self.vitals_route:
            return _error(404, "not_found")
        if device_id not in self.vitals:
            return web.json_response(fixture("vitals_none.json"), status=404)
        return web.json_response(self.vitals[device_id])

    async def _artwork(self, request: web.Request) -> web.StreamResponse:
        asked = request.query.get("h", "")
        self.artwork_asks.append(asked)
        status, body, content_type = self.artwork
        # as the server orders it: no session art, then a hash that is no longer the session's, then the fetch
        current = self.media.get(request.match_info["id"], {}).get("metadata", {}).get("artworkHash", "")
        if status == 200 and not current:
            status = 404
        elif status == 200 and asked and asked != current:
            status = 409
        if status != 200:
            return _error(status, {404: "no_artwork", 409: "artwork_changed"}.get(status, "artwork_unavailable"))
        if not self.artwork_chunked:
            return web.Response(body=body, content_type=content_type)
        resp = web.StreamResponse(headers={"Content-Type": content_type})
        resp.enable_chunked_encoding()
        await resp.prepare(request)
        for at in range(0, len(body), 256 * 1024):
            await resp.write(body[at : at + 256 * 1024])
        await resp.write_eof()
        return resp

    async def _command(self, request: web.Request) -> web.Response:
        m = request.match_info
        self.commands.append((m["id"], m["cap"], m["name"], await request.json()))
        status, body = self.command_answer
        return web.json_response(body, status=status)

    async def _catalog_browse(self, request: web.Request) -> web.Response:
        path = request.query.get("path", "")
        if (page := self.catalog_pages.get(path)) is None:
            return _error(404, "not_found")
        return web.json_response(page)

    @staticmethod
    def _choices(request: web.Request) -> dict[str, str]:
        return dict(part.split("=", 1) for part in request.query.getall("choice", []) if "=" in part)

    async def _catalog_item(self, request: web.Request) -> web.Response:
        ref = request.match_info["ref"]
        if (detail := self.catalog_items.get(ref)) is None:
            return _error(404, "unknown_ref")
        selected = self._choices(request)
        self.catalog_item_choices.append((ref, selected))
        if ref == "c:series" and selected.get("source") in ("anime365", "aniliberty"):
            detail = {**detail, "selected": "c:" + selected["source"]}
        if request.query.get("group"):
            # a child group's read answers its own episodes, no groups of its own
            detail = {**detail, "childGroups": []}
        return web.json_response(detail)

    async def _catalog_options(self, request: web.Request) -> web.Response:
        ref = request.match_info["ref"]
        selected = self._choices(request)
        self.catalog_option_asks.append((ref, selected))
        return web.json_response(catalog_options(ref, selected))

    async def _catalog_alternatives(self, request: web.Request) -> web.Response:
        self.catalog_alternative_asks.append(request.match_info["ref"])
        return web.json_response(alternatives())

    async def _catalog_search(self, request: web.Request) -> web.Response:
        self.catalog_searches.append(request.query.get("q", ""))
        return web.json_response(self.catalog_search_answer)

    async def _catalog_image(self, request: web.Request) -> web.StreamResponse:
        image_id = request.match_info["image_id"]
        self.catalog_image_asks.append(image_id)
        status, body, content_type = self.catalog_image
        if status != 200:
            return _error(status, "image_unavailable")
        return web.Response(body=body, content_type=content_type)

    async def _catalog_play(self, request: web.Request) -> web.Response:
        self.catalog_plays.append((request.match_info["id"], await request.json()))
        status, body = self.catalog_play_answer
        return web.json_response(body, status=status)

    async def _catalog_session(self, request: web.Request) -> web.Response:
        device_id = request.match_info["id"]
        self.catalog_session_asks.append(device_id)
        current = self.media.get(device_id, {})
        if current.get("source") != "video":
            return _error(404, "no_catalog_session")
        answer = fixture("catalog_session.json")
        selected = {**answer["choices"], "source": "anime365"}
        # the contract's title has one source; this fake's title group adds the second source its selection path needs
        source_group = catalog_options("c:series")["groups"][0]
        answer["options"]["groups"] = [source_group, *answer["options"]["groups"]]
        answer["options"]["selected"] = selected
        answer.update(
            {
                "sessionId": current["sessionId"],
                "ref": "c:series",
                "episode": current.get("metadata", {}).get("episode", "7"),
                "choices": selected,
            }
        )
        return web.json_response(answer)

    async def _catalog_select(self, request: web.Request) -> web.Response:
        self.catalog_selects.append((request.match_info["id"], await request.json()))
        status, body = self.catalog_select_answer
        return web.json_response(body, status=status)

    async def _join(self, request: web.Request) -> web.Response:
        self.joins.append((request.match_info["id"], await request.json()))
        status, body = self.join_answer
        return web.json_response(body, status=status)

    async def _leave(self, request: web.Request) -> web.Response:
        self.leaves.append(request.match_info["id"])
        return web.Response(status=204)

    async def _announce(self, request: web.Request) -> web.Response:
        self.announces.append((request.match_info["id"], await request.json()))
        status, body = self.announce_answers.get(request.match_info["id"], self.announce_answer)
        return web.json_response(body, status=status)

    async def _live(self, request: web.Request) -> web.StreamResponse:
        ws = web.WebSocketResponse()
        await ws.prepare(request)
        self.connects += 1
        await ws.send_json(fixture("feed_open.json"))
        msg = await ws.receive()
        if msg.type is not WSMsgType.TEXT:
            return ws
        self.subscribes.append(msg.data)
        await ws.send_json(fixture("feed_subscribed.json"))
        self.feeds.append(ws)
        try:
            async for _ in ws:
                pass
        finally:
            if ws in self.feeds:
                self.feeds.remove(ws)
        return ws

    async def push(self, *frames: dict[str, Any]) -> None:
        """Sends frames on every subscribed feed."""
        for ws in list(self.feeds):
            for f in frames:
                await ws.send_json(f)

    async def close_feeds(self, code: int) -> None:
        for ws in list(self.feeds):
            await ws.close(code=code, message=b"unauthorized" if code == 1008 else b"")
        self.feeds.clear()


async def wait_for(predicate: Callable[[], bool], timeout: float = 20.0) -> None:
    """Waits until predicate holds."""
    async with asyncio.timeout(timeout):
        while not predicate():
            await asyncio.sleep(0.01)


class UntrustedServer:
    """A TLS server whose certificate nobody trusts: the handshake is all it serves, and a refused one is no fault
    the event loop reports."""

    def __init__(self, ssl_context: ssl.SSLContext) -> None:
        self._ssl = ssl_context
        self._server: asyncio.Server | None = None
        self.url = ""
        self.handshakes = 0
        # a request never gets past the handshake
        self.requests: list[str] = []

    async def start(self) -> UntrustedServer:
        self._server = await asyncio.start_server(self._handle, "127.0.0.1", 0)
        self.url = f"https://127.0.0.1:{self._server.sockets[0].getsockname()[1]}"
        return self

    async def _handle(self, _: asyncio.StreamReader, writer: asyncio.StreamWriter) -> None:
        self.handshakes += 1
        try:
            await writer.start_tls(self._ssl)
            self.requests.append("past the handshake")
        except ssl.SSLError, ConnectionError:
            pass
        finally:
            writer.close()

    async def close(self) -> None:
        if self._server is not None:
            self._server.close()
            await self._server.wait_closed()
