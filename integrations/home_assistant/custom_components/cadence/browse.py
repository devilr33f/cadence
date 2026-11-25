"""The server's TV catalog as Home Assistant's media browser shows it: feeds, titles, every provider and source a
title is on, its voiceover and quality choices, seasons and episodes behind cadence:// ids. Search is native to HA
2026.10. Every title, ref, choice and image id is third-party data: never logged (rule 8, D11)."""

from __future__ import annotations

from collections.abc import Callable, Mapping
from urllib.parse import parse_qsl, quote, unquote, urlencode

from homeassistant.components.media_player import (
    BrowseMedia,
    MediaClass,
    MediaType,
    SearchMedia,
    SearchMediaQuery,
)

from .client import CadenceApiError, CadenceClient, CadenceError
from .models import CatalogChoice, CatalogChoiceGroup, CatalogItem, CatalogOptions

# the ids the tree's nodes carry: cadence://<kind>/<value>[?cursor=…&voiceover=…], the value quoted
SCHEME = "cadence://"
HOME = SCHEME

# an item kind as the browser plays it; a kind it does not know is a video
KINDS: dict[str, str] = {
    "episode": MediaType.EPISODE,
    "movie": MediaType.MOVIE,
    "video": MediaType.VIDEO,
    "live": MediaType.VIDEO,
    "channel": MediaType.CHANNEL,
    "track": MediaType.MUSIC,
    "podcast": MediaType.EPISODE,
}
# kinds whose items list children (episodes, tracks, files): they expand even when the row said none
EXPANDS = {"title_group", "series", "season", "playlist", "album", "artist", "torrent", "folder"}
TITLES = {"title_group", "series", "movie"}
# selection query keys; group and cursor belong to the item's own child pages, option and pick to an option node
SELECTION_GROUPS = ("source", "voiceover", "quality", "torrent", "file", "audio_language", "subtitles")
# an image id of the server's proxy, as an item's artwork carries it
IMAGE_ID_CHARS = frozenset("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-")


def node_id(kind: str, value: str, **query: str) -> str:
    """The id one of the tree's nodes carries; empty query values are left out."""
    ident = f"{SCHEME}{kind}/{quote(value, safe='')}"
    asked = {k: v for k, v in query.items() if v}
    return f"{ident}?{urlencode(asked)}" if asked else ident


def parse(media_content_id: str) -> tuple[str, str, dict[str, str]] | None:
    """The node a cadence:// id names: its kind, value and query; None when the id is not the tree's."""
    if not media_content_id.startswith(SCHEME):
        return None
    path, _, query = media_content_id[len(SCHEME) :].partition("?")
    kind, _, value = path.partition("/")
    if not kind or not value:
        return None
    return kind, unquote(value), dict(parse_qsl(query))


def _selection(query: Mapping[str, str]) -> dict[str, str]:
    return {kind: query[kind] for kind in SELECTION_GROUPS if query.get(kind)}


def play(media_content_id: str) -> tuple[str, str, dict[str, str]] | None:
    """The ref, episode label and exact choices a cadence:// id plays; None when it plays nothing."""
    if (parsed := parse(media_content_id)) is None:
        return None
    kind, value, query = parsed
    if kind not in ("item", "play"):
        return None
    return query.get("title", value), query.get("episode", ""), _selection(query)


def image_id(artwork: str) -> bool:
    """Whether artwork is an image id of the server's proxy (24 characters of its alphabet)."""
    return len(artwork) == 24 and all(c in IMAGE_ID_CHARS for c in artwork)


def item_node(
    item: CatalogItem,
    image_url: Callable[[str], str | None],
    choices: Mapping[str, str] | None = None,
    play_ref: str = "",
) -> BrowseMedia:
    """One catalog item, carrying the choices made so far into its children and its play."""
    expand = item.has_children or item.kind in EXPANDS
    title = item.title or item.ref
    if item.kind == "episode" and item.episode:
        title = f"{item.episode}. {title}"
    thumbnail = image_url(item.artwork) if image_id(item.artwork) else None
    play_query = dict(choices or {})
    if play_ref and item.episode:
        # resolving the group and label remembers the source too; the episode's own ref would remember only its stream
        play_query.update({"title": play_ref, "episode": item.episode})
    return BrowseMedia(
        media_class=MediaClass.DIRECTORY if expand else _class(item.kind),
        media_content_id=node_id("item", item.ref, **play_query),
        media_content_type=KINDS.get(item.kind, MediaType.VIDEO),
        title=title,
        can_play=item.playable and not item.unavailable,
        can_expand=expand,
        thumbnail=thumbnail,
    )


def _class(kind: str) -> MediaClass:
    played = KINDS.get(kind, MediaType.VIDEO)
    return {
        MediaType.EPISODE: MediaClass.EPISODE,
        MediaType.MOVIE: MediaClass.MOVIE,
        MediaType.VIDEO: MediaClass.VIDEO,
        MediaType.CHANNEL: MediaClass.CHANNEL,
        MediaType.MUSIC: MediaClass.TRACK,
    }.get(played, MediaClass.VIDEO)


def _directory(title: str, content_id: str, content_type: str) -> BrowseMedia:
    return BrowseMedia(
        media_class=MediaClass.DIRECTORY,
        media_content_id=content_id,
        media_content_type=content_type,
        title=title,
        can_play=False,
        can_expand=True,
    )


def _feed_node(title: str, path: str, cursor: str = "") -> BrowseMedia:
    return _directory(title or path, node_id("feed", path, cursor=cursor), "cadence/feed")


def _more(after: str) -> BrowseMedia:
    return _directory("More", after, "cadence/more")


async def tree(
    client: CadenceClient, title: str, image_url: Callable[[str], str | None], media_content_id: str | None
) -> BrowseMedia:
    """The node a cadence:// id names: the tree's root when the id is None or empty."""
    if not media_content_id or media_content_id == HOME:
        return await _home(client, title)
    parsed = parse(media_content_id)
    if parsed is None:
        raise CadenceError("invalid")
    kind, value, query = parsed
    match kind:
        case "feed":
            return await _feed(client, image_url, value, query.get("cursor", ""))
        case "item" | "group":
            return await _children(client, image_url, value, query)
        case "sources":
            # ids issued before alternatives landed still open the title group's own sources
            return await _sources(client, image_url, value)
        case "alternatives":
            return await _alternatives(client, image_url, value)
        case "options":
            return await _options(client, value, query)
        case "choice":
            picked = dict(query)
            if (option := query.get("option")) and option in SELECTION_GROUPS and query.get("pick"):
                picked[option] = query["pick"]
            return await _children(client, image_url, value, picked)
    raise CadenceError("invalid")


async def _home(client: CadenceClient, title: str) -> BrowseMedia:
    page = await client.catalog_browse("")
    children = [
        _feed_node(section.title or section.id, section.path, section.next_cursor)
        for section in page.sections
        if section.items or section.next_cursor
    ]
    return BrowseMedia(
        media_class=MediaClass.DIRECTORY,
        media_content_id=HOME,
        media_content_type="cadence",
        title=title,
        can_play=False,
        can_expand=True,
        children=children,
        can_search=True,
    )


async def _feed(client: CadenceClient, image_url: Callable[[str], str | None], path: str, cursor: str) -> BrowseMedia:
    page = await client.catalog_browse(path, cursor)
    if len(page.sections) == 1:
        section = page.sections[0]
        node = _feed_node(section.title or path, path)
        node.children = [item_node(item, image_url) for item in section.items]
        if section.next_cursor:
            node.children.append(_more(node_id("feed", path, cursor=section.next_cursor)))
        return node
    # a tree node that answers in rows: each row its own directory, as the root
    node = _feed_node(path, path)
    node.children = [
        _feed_node(section.title or section.id, section.path, section.next_cursor)
        for section in page.sections
        if section.items or section.next_cursor
    ]
    return node


async def _children(
    client: CadenceClient, image_url: Callable[[str], str | None], ref: str, query: Mapping[str, str]
) -> BrowseMedia:
    group, cursor = query.get("group", ""), query.get("cursor", "")
    selected = _selection(query)
    detail = await client.catalog_item(ref, cursor, group, selected)
    node = item_node(detail.item, image_url, selected)
    node.can_expand = True
    children: list[BrowseMedia] = []
    if not group and not cursor and detail.item.kind in TITLES:
        children.append(_directory("Available on", node_id("alternatives", ref), "cadence/alternatives"))
    if not cursor and not group:
        try:
            options = await client.catalog_options(ref, choices=selected)
        except CadenceApiError as err:
            # providers without options still browse and play with their default
            if err.status != 422 or err.code != "unsupported":
                raise
        else:
            children.extend(_option_node(ref, group, options, selected, g) for g in options.groups if g.choices)
    for child_group in detail.groups:
        children.append(
            _directory(
                child_group.label,
                node_id("group", ref, group=child_group.id, **selected),
                "cadence/group",
            )
        )
    children.extend(
        item_node(item, image_url, selected, ref if detail.item.kind == "title_group" else "")
        for item in detail.children
    )
    if detail.next_cursor:
        children.append(_more(node_id("item", ref, group=group, cursor=detail.next_cursor, **selected)))
    node.children = children
    return node


def _option_node(
    ref: str, child_group: str, options: CatalogOptions, selected: Mapping[str, str], group: CatalogChoiceGroup
) -> BrowseMedia:
    label = group.label
    if choice := _choice(group, options.selected.get(group.kind, "")):
        label = f"{label}: {choice.label}"
        if group.kind in options.remembered:
            label += " · remembered"
    return _directory(
        label,
        node_id("options", ref, option=group.kind, group=child_group, **dict(selected)),
        f"cadence/{group.kind}",
    )


def _choice(group: CatalogChoiceGroup, choice_id: str) -> CatalogChoice | None:
    return next((choice for choice in group.choices if choice.id == choice_id), None)


async def _options(client: CadenceClient, ref: str, query: Mapping[str, str]) -> BrowseMedia:
    selected = _selection(query)
    option = query.get("option", "")
    options = await client.catalog_options(ref, choices=selected)
    group = next((g for g in options.groups if g.kind == option), None)
    if group is None:
        raise CadenceError("invalid")
    node = _directory(group.label, node_id("options", ref, **dict(query)), f"cadence/{option}")
    node.children = []
    for choice in group.choices:
        label = choice.label
        if choice.quality and f"{choice.quality}p" not in label:
            label += f" · {choice.quality}p"
        if choice.detail:
            label += f" · {choice.detail}"
        if choice.id == options.selected.get(option):
            label = "✓ " + label
        if choice.unavailable:
            label += f" · {choice.reason or 'unavailable'}"
        child = _directory(
            label,
            node_id("choice", ref, option=option, pick=choice.id, group=query.get("group", ""), **selected),
            "cadence/choice",
        )
        child.can_expand = not choice.unavailable
        node.children.append(child)
    return node


async def _sources(client: CadenceClient, image_url: Callable[[str], str | None], ref: str) -> BrowseMedia:
    detail = await client.catalog_item(ref)
    node = _directory("Sources", node_id("sources", ref), "cadence/sources")
    node.children = [item_node(variant, image_url) for variant in detail.variants]
    return node


async def _alternatives(client: CadenceClient, image_url: Callable[[str], str | None], ref: str) -> BrowseMedia:
    alternatives = await client.catalog_alternatives(ref)
    node = _directory("Available on", node_id("alternatives", ref), "cadence/alternatives")
    node.children = []
    for alt in alternatives:
        details = []
        if alt.episodes_aired:
            episodes = f"{alt.episodes_aired} ep."
            if alt.episodes_total:
                episodes += f" / {alt.episodes_total}"
            details.append(episodes)
        if alt.quality:
            details.append(f"{alt.quality}p")
        label = f"{alt.source_name} — {alt.title}"
        if details:
            label += " · " + " · ".join(details)
        if alt.current:
            label = "✓ " + label
        item = CatalogItem(
            ref=alt.ref,
            kind="series",
            title=alt.title,
            provider=alt.provider,
            source=alt.source,
            playable=not alt.unavailable,
            has_children=True,
            unavailable=alt.unavailable,
        )
        child = item_node(item, image_url)
        child.title = label
        node.children.append(child)
    return node


async def search(client: CadenceClient, query: SearchMediaQuery, image_url: Callable[[str], str | None]) -> SearchMedia:
    """A text search over every provider. HA's result is flat, so each row names its provider/source, year and kind."""
    items = await client.catalog_search(query.search_query)
    result = []
    for item in items:
        node = item_node(item, image_url)
        where = " · ".join(part for part in (item.provider, item.source) if part)
        facts = [part for part in (str(item.year) if item.year else "", item.format or item.kind, where) if part]
        if item.subtitle:
            facts.insert(0, item.subtitle)
        if facts:
            node.title = f"{item.title} — {' · '.join(facts)}"
        result.append(node)
    return SearchMedia(result=result)
