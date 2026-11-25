"""The catalog on a screen's media player: the tree the media browser walks (feeds, items, episodes, sources),
its images through the entity's own proxy path, its search, and the play that sends a ref to the screen. What the
catalog returns is third-party data: never logged (rule 8, D11)."""

from __future__ import annotations

import logging

import pytest
from homeassistant.components.media_player import MediaClass, MediaPlayerEntityFeature, SearchMediaQuery
from homeassistant.core import HomeAssistant
from homeassistant.exceptions import HomeAssistantError
from pytest_homeassistant_custom_component.common import MockConfigEntry
from pytest_homeassistant_custom_component.typing import ClientSessionGenerator

from custom_components.cadence import browse

from .conftest import entity_id
from .fake_backend import KITCHEN, POSTER, FakeCadence, fixture, frame, wait_for
from .test_init import BEDROOM, assert_private
from .test_media_player import call, player_entity

# the browse features a screen adds to the speaker's own
CATALOG_FEATURES = MediaPlayerEntityFeature.PLAY_MEDIA | MediaPlayerEntityFeature.BROWSE_MEDIA


async def screen(hass: HomeAssistant, backend: FakeCadence) -> str:
    """A linked screen device and its media player entity."""
    backend.add_device(BEDROOM, "TV")
    await backend.push(frame("device_added.json", BEDROOM, name="TV"))
    await wait_for(lambda: entity_id(hass, "binary_sensor", BEDROOM, "online") is not None)
    backend.link_screen(BEDROOM)
    await backend.push(frame("device_online.json", BEDROOM, name="TV"))
    await wait_for(lambda: entity_id(hass, "media_player", BEDROOM, "speaker") is not None)
    player = entity_id(hass, "media_player", BEDROOM, "speaker")
    await wait_for(lambda: hass.states.get(player) is not None and hass.states.get(player).state == "idle")
    return player


async def test_a_screen_browses_the_catalogs_home(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence
) -> None:
    player = await screen(hass, backend)
    entity = player_entity(hass, player)
    offered = hass.states.get(player).attributes["supported_features"]
    assert offered & (MediaPlayerEntityFeature.VOLUME_SET | CATALOG_FEATURES) == (
        MediaPlayerEntityFeature.VOLUME_SET | CATALOG_FEATURES
    )

    root = await entity.async_browse_media(None, None)
    assert (root.title, root.can_play, root.can_expand, root.can_search) == ("TV", False, True, True)
    assert [child.title for child in root.children or []] == ["Продолжить", "Мой список"]
    section = root.children[0]
    assert (section.media_class, section.can_play, section.can_expand) == (MediaClass.DIRECTORY, False, True)
    assert section.media_content_id == browse.node_id("feed", "torrplayer/continue")

    # a speaker without a screen keeps home assistant's own audio sources (test_media_player holds its filter)
    assert entity_id(hass, "media_player", KITCHEN, "speaker") is not None


async def test_a_feed_lists_its_items_with_their_art(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence
) -> None:
    player = await screen(hass, backend)
    entity = player_entity(hass, player)
    feed = await entity.async_browse_media(None, browse.node_id("feed", "torrplayer/continue"))
    assert feed.title == "Продолжить"
    (item, *rest) = feed.children or []
    assert rest == []
    assert (item.title, item.can_play, item.can_expand) == ("Rebuilding a World", True, True)
    assert item.media_content_id == browse.node_id("item", "c:series")
    assert item.thumbnail == entity.get_browse_image_url("catalog", POSTER)

    # a feed that pages offers its next page
    backend.catalog_pages["torrplayer/continue"] = {
        "sections": [
            {
                "id": "torrplayer/continue",
                "provider": "torrplayer",
                "title": "Продолжить",
                "path": "torrplayer/continue",
                "items": [],
                "next": "page-2",
            }
        ]
    }
    feed = await entity.async_browse_media(None, browse.node_id("feed", "torrplayer/continue"))
    assert [child.title for child in feed.children or []] == ["More"]
    assert feed.children[0].media_content_id == browse.node_id("feed", "torrplayer/continue", cursor="page-2")


async def test_an_item_expands_to_sources_seasons_and_episodes(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence
) -> None:
    player = await screen(hass, backend)
    entity = player_entity(hass, player)
    node = await entity.async_browse_media(None, browse.node_id("item", "c:series"))
    titles = [child.title for child in node.children or []]
    assert titles == [
        "Available on",
        "Source: anime365",
        "Voiceover: AniLibria · remembered",
        "Quality: 1080p",
        "Сезон 1",
        "6. Reunion",
        "7. The Guild Advisor",
    ]
    episode = node.children[-1]
    assert (episode.media_class, episode.media_content_type, episode.can_play, episode.can_expand) == (
        MediaClass.EPISODE,
        "episode",
        True,
        False,
    )
    assert episode.media_content_id == browse.node_id("item", "c:ep7", title="c:series", episode="7")

    sources = node.children[0]
    variants = await entity.async_browse_media(None, sources.media_content_id)
    assert [child.title for child in variants.children or []] == [
        "✓ anime365 — Rebuilding a World · 12 ep. / 12 · 1080p",
        "AniLiberty — Rebuilding a World · 12 ep.",
        "torrserver — Rebuilding a World · 2160p",
    ]
    assert [child.media_content_id for child in variants.children or []] == [
        browse.node_id("item", "c:anime365"),
        browse.node_id("item", "c:aniliberty"),
        browse.node_id("item", "c:torrent"),
    ]

    # a season group reads the same item with the group named
    season = await entity.async_browse_media(None, node.children[4].media_content_id)
    assert [child.title for child in season.children or []] == ["6. Reunion", "7. The Guild Advisor"]

    # a ref the server no longer knows is a refusal, translated
    with pytest.raises(HomeAssistantError) as err:
        await entity.async_browse_media(None, browse.node_id("item", "c:gone"))
    assert err.value.translation_key == "unknown_ref"


async def test_an_items_art_is_served_through_the_entity(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence, hass_client: ClientSessionGenerator
) -> None:
    player = await screen(hass, backend)
    entity = player_entity(hass, player)
    node = await entity.async_browse_media(None, browse.node_id("item", "c:series"))
    client = await hass_client()
    resp = await client.get(node.thumbnail)
    assert resp.status == 200
    assert await resp.read() == b"\x89PNG poster"
    assert backend.catalog_image_asks == [POSTER]

    # the server could not fetch it (502): the ask went out again, and no image came back
    backend.catalog_image = (502, b"", "")
    resp = await client.get(node.thumbnail)
    assert resp.status == 404
    assert backend.catalog_image_asks == [POSTER, POSTER]

    # a url that names no id of the proxy's asks nothing
    token = node.thumbnail.split("token=")[1]
    for bad in ("not-an-id", f"artwork/{POSTER}"):
        resp = await client.get(f"/api/media_player_proxy/{player}/browse_media/catalog/{bad}?token={token}")
        assert resp.status == 404
    assert backend.catalog_image_asks == [POSTER, POSTER]


async def test_source_voiceover_quality_and_episode_are_the_exact_play(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence
) -> None:
    player = await screen(hass, backend)
    entity = player_entity(hass, player)

    async def choose(node: browse.BrowseMedia, kind: str, label: str) -> browse.BrowseMedia:
        option = next(child for child in node.children or [] if child.media_content_type == f"cadence/{kind}")
        choices = await entity.async_browse_media(None, option.media_content_id)
        picked = next(child for child in choices.children or [] if child.title.startswith(label))
        assert picked.can_expand
        return await entity.async_browse_media(None, picked.media_content_id)

    node = await entity.async_browse_media(None, browse.node_id("item", "c:series"))
    node = await choose(node, "source", "AniLiberty")
    assert ("c:series", {"source": "aniliberty"}) in backend.catalog_item_choices
    node = await choose(node, "voiceover", "Crunchyroll")
    node = await choose(node, "quality", "720p")

    season = next(child for child in node.children or [] if child.media_content_type == "cadence/group")
    episodes = await entity.async_browse_media(None, season.media_content_id)
    episode = next(child for child in episodes.children or [] if child.title == "7. The Guild Advisor")
    await entity.async_play_media("episode", episode.media_content_id)
    assert backend.catalog_plays[-1] == (
        BEDROOM,
        {
            "ref": "c:series",
            "episode": "7",
            "choices": {"source": "aniliberty", "voiceover": "Crunchyroll", "quality": "720"},
        },
    )
    # an unavailable offer explains itself and cannot be selected into a play
    option = next(child for child in node.children or [] if child.media_content_type == "cadence/voiceover")
    choices = await entity.async_browse_media(None, option.media_content_id)
    dead = next(child for child in choices.children or [] if child.title.startswith("Unavailable dub"))
    assert (dead.title.endswith("no_seeders"), dead.can_expand, dead.can_play) == (True, False, False)


async def test_a_catalog_ref_plays_on_the_screen(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence
) -> None:
    player = await screen(hass, backend)
    await player_entity(hass, player).async_play_media("episode", browse.node_id("item", "c:ep7"))
    assert backend.catalog_plays == [(BEDROOM, {"ref": "c:ep7"})]
    assert backend.commands == []

    # a play of the title itself: the server resolves its own next episode
    await player_entity(hass, player).async_play_media("video", browse.node_id("item", "c:series"))
    assert backend.catalog_plays[-1] == (BEDROOM, {"ref": "c:series"})

    # a refused play says why, by the server's code alone
    backend.catalog_play_answer = (409, {"error": "unplayable"})
    with pytest.raises(HomeAssistantError) as err:
        await player_entity(hass, player).async_play_media("episode", browse.node_id("item", "c:ep6"))
    assert (err.value.translation_key, err.value.translation_placeholders) == ("unplayable", {"target": "TV"})
    backend.catalog_play_answer = (404, {"error": "unknown_ref"})
    with pytest.raises(HomeAssistantError) as err:
        await player_entity(hass, player).async_play_media("episode", browse.node_id("item", "c:gone"))
    assert err.value.translation_key == "unknown_ref"


async def test_the_catalog_searches_by_text(hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence) -> None:
    player = await screen(hass, backend)
    backend.catalog_search_answer = {
        "events": [
            {"pending": [{"provider": "torrplayer", "source": "anime365", "name": "anime365", "enabled": True}]},
            {
                "provider": "torrplayer",
                "source": "anime365",
                "items": [
                    {
                        "ref": "c:series",
                        "provider": "torrplayer",
                        "kind": "title_group",
                        "title": "Rebuilding a World",
                        "source": "anime365",
                        "show": {"year": 2023, "format": "TV"},
                        "artwork": {"hash": POSTER},
                        "playable": True,
                        "hasChildren": True,
                    },
                    {
                        "ref": "c:series",
                        "provider": "torrplayer",
                        "kind": "title_group",
                        "title": "again",
                        "playable": True,
                    },
                ],
            },
            {
                "provider": "torrplayer",
                "source": "aniliberty",
                "updated": [
                    {
                        "ref": "c:series",
                        "provider": "torrplayer",
                        "source": "anime365",
                        "kind": "title_group",
                        "title": "Rebuilding a World",
                        "subtitle": "2 sources",
                        "show": {"year": 2023, "format": "TV"},
                        "artwork": {"hash": POSTER},
                        "playable": True,
                        "hasChildren": True,
                    }
                ],
            },
            {
                "provider": "ytmusic",
                "items": [
                    {
                        "ref": "c:song",
                        "provider": "ytmusic",
                        "kind": "track",
                        "title": "Rebuilding Theme",
                        "playable": True,
                    }
                ],
            },
            {"done": True},
        ]
    }
    found = await player_entity(hass, player).async_search_media(SearchMediaQuery(search_query="rebuilding"))
    assert backend.catalog_searches == ["rebuilding"]
    # the same ref twice is one item, and a later source's updated event enriches its row in place
    assert [item.title for item in found.result] == [
        "Rebuilding a World — 2 sources · 2023 · TV · torrplayer · anime365",
        "Rebuilding Theme — track · ytmusic",
    ]
    assert found.result[0].media_content_id == browse.node_id("item", "c:series")


async def test_a_catalog_session_shows_its_show_episode_and_source(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence, hass_client: ClientSessionGenerator
) -> None:
    player = await screen(hass, backend)
    backend.media[BEDROOM] = fixture("media_catalog.json")
    await backend.push(frame("media_catalog_state.json", BEDROOM))
    await wait_for(lambda: hass.states.get(player).attributes.get("media_title") == "The Guild Advisor")
    attrs = hass.states.get(player).attributes
    assert attrs["media_series_title"] == "Rebuilding a World"
    assert "media_season" not in attrs
    assert attrs["media_episode"] == 7
    assert attrs["media_artist"] == "torrplayer · anime365"
    assert attrs["media_content_type"] == "episode"
    assert attrs["media_duration"] == 1422
    # d11: the episode's line never reaches the recorder's database
    assert hass.states.get(player).state_info is not None
    assert {"media_series_title", "media_episode", "media_artist"} <= hass.states.get(player).state_info[
        "unrecorded_attributes"
    ]
    # the cover of the catalog session, the server's own proxied image, read by its hash through the artwork route
    client = await hass_client()
    resp = await client.get(attrs["entity_picture"])
    assert resp.status == 200
    assert await resp.read() == b"\x89PNG fake"
    assert backend.artwork_asks == [fixture("media_catalog.json")["metadata"]["artworkHash"]]


async def test_the_playing_title_switches_source_and_voiceover(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence, caplog: pytest.LogCaptureFixture
) -> None:
    caplog.set_level(logging.DEBUG, logger="custom_components.cadence")
    player = await screen(hass, backend)
    backend.media[BEDROOM] = fixture("media_catalog.json")
    await backend.push(frame("media_catalog_state.json", BEDROOM))
    entity = player_entity(hass, player)
    await wait_for(lambda: entity.source_list is not None)

    assert (entity.source, entity.source_list) == ("anime365", ["anime365", "AniLiberty"])
    assert (entity.sound_mode, entity.sound_mode_list) == ("AniLibria", ["AniLibria", "Crunchyroll"])
    offered = hass.states.get(player).attributes["supported_features"]
    assert offered & MediaPlayerEntityFeature.SELECT_SOURCE
    assert offered & MediaPlayerEntityFeature.SELECT_SOUND_MODE

    await call(hass, "select_source", player, source="AniLiberty")
    await call(hass, "select_sound_mode", player, sound_mode="Crunchyroll")
    assert backend.catalog_selects == [
        (BEDROOM, {"group": "source", "choice": "aniliberty"}),
        (BEDROOM, {"group": "voiceover", "choice": "Crunchyroll"}),
    ]
    with pytest.raises(HomeAssistantError) as err:
        await call(hass, "select_sound_mode", player, sound_mode="Unavailable dub")
    assert err.value.translation_key == "invalid_choice"
    assert backend.commands == []
    # the selected source and voiceover are third-party metadata too: shown, never recorded
    state = hass.states.get(player)
    assert state.state_info is not None
    assert {"source", "source_list", "sound_mode", "sound_mode_list"} <= state.state_info["unrecorded_attributes"]
    assert_private(caplog, backend)
    for record in caplog.records:
        if record.name.startswith("custom_components.cadence"):
            line = record.getMessage()
            for private in ("Rebuilding a World", "c:series", "aniliberty", "Crunchyroll"):
                assert private not in line, line


async def test_a_node_the_tree_does_not_know_is_refused(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence
) -> None:
    player = await screen(hass, backend)
    with pytest.raises(HomeAssistantError):
        await player_entity(hass, player).async_browse_media(None, "cadence://elsewhere/x")
