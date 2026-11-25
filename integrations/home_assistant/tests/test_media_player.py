"""The speaker: its session, volume and cover from the feed, and every command it sends (rule 7: HA may set volume)."""

from __future__ import annotations

import logging
from datetime import UTC, datetime
from typing import Any
from unittest.mock import patch

import pytest
from homeassistant.components import media_source
from homeassistant.components.media_player import BrowseMedia, MediaClass, MediaPlayerEntityFeature
from homeassistant.core import HomeAssistant
from homeassistant.core_config import async_process_ha_core_config
from homeassistant.exceptions import HomeAssistantError, ServiceNotSupported, ServiceValidationError
from pytest_homeassistant_custom_component.common import MockConfigEntry
from pytest_homeassistant_custom_component.typing import ClientSessionGenerator
from yarl import URL

from custom_components.cadence.media_player import session_position
from custom_components.cadence.models import MediaSession

from .conftest import entity_id
from .fake_backend import ART, HALL, KITCHEN, FakeCadence, dropped, fixture, frame, wait_for
from .test_init import BEDROOM, assert_private

HASH = ART
# what a speaker offers with or without a session
ALWAYS = (
    MediaPlayerEntityFeature.VOLUME_SET
    | MediaPlayerEntityFeature.VOLUME_STEP
    | MediaPlayerEntityFeature.PLAY_MEDIA
    | MediaPlayerEntityFeature.MEDIA_ANNOUNCE
    | MediaPlayerEntityFeature.BROWSE_MEDIA
    | MediaPlayerEntityFeature.GROUPING
)
# each session action, the feature it offers, and the service that uses it
TRANSPORT = {
    "pause": (MediaPlayerEntityFeature.PAUSE, "media_pause", {}),
    "resume": (MediaPlayerEntityFeature.PLAY, "media_play", {}),
    "stop": (MediaPlayerEntityFeature.STOP, "media_stop", {}),
    "seek": (MediaPlayerEntityFeature.SEEK, "media_seek", {"seek_position": 1}),
    "next": (MediaPlayerEntityFeature.NEXT_TRACK, "media_next_track", {}),
    "previous": (MediaPlayerEntityFeature.PREVIOUS_TRACK, "media_previous_track", {}),
}
SKIPS = MediaPlayerEntityFeature.NEXT_TRACK | MediaPlayerEntityFeature.PREVIOUS_TRACK


def speaker(hass: HomeAssistant, device_id: str = KITCHEN) -> str:
    found = entity_id(hass, "media_player", device_id, "speaker")
    assert found is not None
    return found


def player_entity(hass: HomeAssistant, player: str) -> Any:
    return hass.data["media_player"].get_entity(player)


async def call(hass: HomeAssistant, service: str, player: str, **data: Any) -> None:
    await hass.services.async_call("media_player", service, {"entity_id": player, **data}, blocking=True)


def features(hass: HomeAssistant, player: str) -> int:
    state = hass.states.get(player)
    assert state is not None
    return state.attributes["supported_features"]


async def offer(hass: HomeAssistant, backend: FakeCadence, player: str, actions: list[str]) -> None:
    """A session that lists these actions, as the speaker shows it."""
    await backend.push(frame("media_state.json", actions=actions))
    want = ALWAYS
    for action in actions:
        want |= TRANSPORT[action][0]
    await wait_for(lambda: features(hass, player) == want)


def without(backend: FakeCadence, capability: str, *commands: str) -> None:
    """The device reports the capability without these commands (read at the next full read)."""
    for cap in backend.details[KITCHEN]["capabilities"]:
        if cap["id"] == capability:
            cap["commands"] = [c for c in cap["commands"] if c["name"] not in commands]


async def test_the_speaker_shows_the_session(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence
) -> None:
    player = speaker(hass)
    state = hass.states.get(player)
    assert state is not None
    assert state.state == "playing"
    attrs = state.attributes
    assert attrs["friendly_name"] == "Kitchen"
    assert attrs["device_class"] == "speaker"
    assert (attrs["media_title"], attrs["media_artist"], attrs["media_album_name"]) == (
        "Night Drive",
        "The Examples",
        "Fixtures",
    )
    assert attrs["media_content_type"] == "music"
    assert attrs["media_duration"] == 215
    assert attrs["media_position"] == 42
    assert attrs["media_position_updated_at"] == datetime(2026, 11, 12, 9, 59, 58, 250000, tzinfo=UTC)
    # the audio.output level, not the session's own volume (60)
    assert attrs["volume_level"] == 0.35
    # the image url names its hash: it serves that cover or none
    assert f"/browse_media/artwork/{HASH}?" in attrs["entity_picture"]
    # pause, stop, seek, next and previous: what the session lists
    assert attrs["supported_features"] == ALWAYS | MediaPlayerEntityFeature.PAUSE | MediaPlayerEntityFeature.STOP | (
        MediaPlayerEntityFeature.SEEK | SKIPS
    )
    # d11: what plays is shown, never written to the recorder's database
    assert state.state_info is not None
    assert {"media_title", "media_artist", "media_album_name", "entity_picture"} <= state.state_info[
        "unrecorded_attributes"
    ]
    # a device that never reported an audio capability has no speaker
    assert entity_id(hass, "media_player", HALL, "speaker") is None

    await backend.push(frame("device_volume.json", level=50))
    await wait_for(lambda: hass.states.get(player).attributes.get("volume_level") == 0.5)

    paused = {"state": "paused", "positionAt": None, "actions": ["resume", "stop", "seek"]}
    await backend.push(frame("media_state.json", **paused))
    await wait_for(lambda: hass.states.get(player).state == "paused")
    attrs = hass.states.get(player).attributes
    assert attrs["media_position"] == 42
    assert "media_position_updated_at" not in attrs
    assert attrs["supported_features"] == ALWAYS | MediaPlayerEntityFeature.PLAY | MediaPlayerEntityFeature.STOP | (
        MediaPlayerEntityFeature.SEEK
    )

    await backend.push(frame("media_state_idle.json"))
    await wait_for(lambda: hass.states.get(player).state == "idle")
    attrs = hass.states.get(player).attributes
    for gone in ("media_title", "media_artist", "media_album_name", "media_duration", "entity_picture"):
        assert gone not in attrs
    assert attrs["volume_level"] == 0.5
    # no session, nothing to pause or skip; the volume and play_media stay
    assert attrs["supported_features"] == ALWAYS


def test_the_position_moves_at_the_sessions_rate() -> None:
    session = MediaSession.from_json(fixture("media_playing.json"))
    assert session is not None
    at = datetime(2026, 11, 12, 9, 59, 58, 250000, tzinfo=UTC)
    now = datetime(2026, 11, 12, 10, 0, 0, 250000, tzinfo=UTC)
    # at its own rate the position is as stamped: home assistant moves it on while playing
    assert session_position(session, now) == (42, at)
    # at another rate it is worked out for now
    faster = MediaSession.from_json({**fixture("media_playing.json"), "rate": 1.5})
    assert faster is not None
    assert session_position(faster, now) == (45, now)
    stalled = MediaSession.from_json({**fixture("media_playing.json"), "rate": 0})
    assert stalled is not None
    assert session_position(stalled, now) == (42, now)
    # never past the end
    late = datetime(2026, 11, 12, 11, 0, 0, tzinfo=UTC)
    assert session_position(faster, late) == (215, late)
    paused = MediaSession.from_json({**fixture("media_playing.json"), "state": "paused", "rate": 0})
    assert paused is not None
    assert session_position(paused, now) == (42, at)
    # a stamp without an offset is no stamp: never compared with an aware clock
    naive = MediaSession.from_json(
        {**fixture("media_playing.json"), "positionAt": "2026-11-12T09:59:58.250", "rate": 2}
    )
    assert naive is not None
    assert naive.position_at is None
    assert session_position(naive, now) == (42, None)


@pytest.mark.parametrize(
    ("service", "data", "command"),
    [
        ("media_pause", {}, ("media.player", "pause", {})),
        ("media_play", {}, ("media.player", "resume", {})),
        ("media_stop", {}, ("media.player", "stop", {})),
        ("media_seek", {"seek_position": 61.5}, ("media.player", "seek", {"positionMs": 61500})),
        ("media_next_track", {}, ("media.player", "next", {})),
        ("media_previous_track", {}, ("media.player", "previous", {})),
        ("volume_set", {"volume_level": 0.42}, ("audio.output", "set_level", {"level": 42})),
        ("volume_up", {}, ("audio.output", "change_level", {"delta": 5})),
        ("volume_down", {}, ("audio.output", "change_level", {"delta": -5})),
    ],
)
async def test_each_command_sends_its_body(
    hass: HomeAssistant,
    loaded: MockConfigEntry,
    backend: FakeCadence,
    service: str,
    data: dict[str, Any],
    command: tuple[str, str, dict[str, Any]],
) -> None:
    player = speaker(hass)
    await offer(hass, backend, player, list(TRANSPORT))
    await call(hass, service, player, **data)
    assert backend.commands == [(KITCHEN, *command)]


async def test_transport_follows_the_sessions_actions(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence
) -> None:
    player = speaker(hass)
    for offered in [[], *([action] for action in TRANSPORT)]:
        await offer(hass, backend, player, offered)
        for action, (_, service, data) in TRANSPORT.items():
            if action not in offered:
                with pytest.raises(ServiceNotSupported):
                    await call(hass, service, player, **data)
    assert backend.commands == []


async def test_a_skip_the_device_cannot_make_is_hidden(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence
) -> None:
    # the session lists next and previous, but the device's media.player has neither
    player = speaker(hass)
    without(backend, "media.player", "next", "previous")
    await backend.push(dropped(1))
    await wait_for(lambda: not features(hass, player) & SKIPS)
    assert features(hass, player) & MediaPlayerEntityFeature.PAUSE


async def test_volume_steps_through_set_level_without_change_level(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence
) -> None:
    player = speaker(hass)
    without(backend, "audio.output", "change_level")
    await backend.push(dropped(1))
    await wait_for(lambda: not features(hass, player) & MediaPlayerEntityFeature.VOLUME_STEP)
    await call(hass, "volume_up", player)
    await call(hass, "volume_down", player)
    assert backend.commands == [
        (KITCHEN, "audio.output", "set_level", {"level": 40}),
        (KITCHEN, "audio.output", "set_level", {"level": 30}),
    ]


async def test_a_refused_command_says_why(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence, caplog: pytest.LogCaptureFixture
) -> None:
    caplog.set_level(logging.DEBUG, logger="custom_components.cadence")
    player = speaker(hass)
    # the server's own refusal, as its commands handler writes it
    backend.command_answer = (409, fixture("command_failed.json"))
    with pytest.raises(HomeAssistantError) as err:
        await call(hass, "media_pause", player)
    assert not isinstance(err.value, ServiceValidationError)
    assert (err.value.translation_key, err.value.translation_placeholders) == (
        "command_failed",
        {"target": "Kitchen", "detail": "no_session"},
    )

    backend.command_answer = (409, {"error": "device_offline"})
    with pytest.raises(HomeAssistantError) as err:
        await call(hass, "volume_set", player, volume_level=0.3)
    assert (err.value.translation_key, err.value.translation_placeholders) == ("device_offline", {"target": "Kitchen"})

    # free text in a refusal is never shown: only codes
    backend.command_answer = (409, {"error": "command_failed", "detail": "the device said Night Drive"})
    with pytest.raises(HomeAssistantError) as err:
        await call(hass, "media_stop", player)
    assert err.value.translation_placeholders == {"target": "Kitchen", "detail": "unknown"}

    backend.command_answer = (418, {"error": "something_new"})
    with pytest.raises(HomeAssistantError) as err:
        await call(hass, "media_stop", player)
    assert (err.value.translation_key, err.value.translation_placeholders) == (
        "request_failed",
        {"target": "Kitchen", "code": "something_new"},
    )
    assert_private(caplog, backend)


async def test_play_media_plays_a_url_as_a_session_and_an_announcement_over_it(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence
) -> None:
    player = speaker(hass)
    url = "https://radio.example.com/stream.mp3"
    await call(hass, "play_media", player, media_content_id=url, media_content_type="music")
    await call(hass, "play_media", player, media_content_id=url, media_content_type="music", enqueue="replace")
    await call(hass, "play_media", player, media_content_id=url, media_content_type="music", enqueue="play")
    await call(hass, "play_media", player, media_content_id=url, media_content_type="music", announce=True)
    # speech by its proxy url alone
    speech = "http://ha.example.com:8123/api/tts_proxy/c3d4.mp3"
    await call(hass, "play_media", player, media_content_id=speech, media_content_type="music")
    play = ("media.url", "play")
    assert backend.commands == [
        (KITCHEN, *play, {"url": url, "mode": "session"}),
        (KITCHEN, *play, {"url": url, "mode": "session"}),
        (KITCHEN, *play, {"url": url, "mode": "session"}),
        (KITCHEN, *play, {"url": url, "mode": "overlay"}),
        (KITCHEN, *play, {"url": speech, "mode": "overlay"}),
    ]


async def test_play_media_resolves_media_sources(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence
) -> None:
    await async_process_ha_core_config(hass, {"internal_url": "http://ha.example.com:8123"})
    player = speaker(hass)
    resolved = {
        "media-source://media_source/local/song.mp3": media_source.PlayMedia("/media/local/song.mp3", "audio/mpeg"),
        "media-source://tts/cloud?message=hello": media_source.PlayMedia("/api/tts_proxy/a1b2.mp3", "audio/mpeg"),
        # speech by its media source alone
        "media-source://tts/other?message=hi": media_source.PlayMedia("https://tts.example.com/e5f6.mp3", "audio/mpeg"),
    }
    asked: list[tuple[str, Any]] = []

    async def resolve(_: HomeAssistant, media_id: str, target: Any) -> media_source.PlayMedia:
        asked.append((media_id, target))
        return resolved[media_id]

    with patch.object(media_source, "async_resolve_media", resolve):
        for media_id in resolved:
            await call(hass, "play_media", player, media_content_id=media_id, media_content_type="music")
    assert asked == [(media_id, player) for media_id in resolved]
    (song, tts, elsewhere) = backend.commands
    assert song[:3] == (KITCHEN, "media.url", "play")
    # signed for the device, which has no session of home assistant's
    assert song[3]["url"].startswith("http://ha.example.com:8123/media/local/song.mp3?authSig=")
    assert song[3]["mode"] == "session"
    # speech plays over the music, as an announcement does
    assert tts[3] == {"url": "http://ha.example.com:8123/api/tts_proxy/a1b2.mp3", "mode": "overlay"}
    assert elsewhere[3]["url"].startswith("https://tts.example.com/e5f6.mp3")
    assert elsewhere[3]["mode"] == "overlay"


@pytest.mark.parametrize(
    ("data", "key"),
    [
        ({"media_content_id": "https://radio.example.com/a.mp3", "enqueue": "add"}, "enqueue_unsupported"),
        ({"media_content_id": "https://radio.example.com/a.mp3", "enqueue": "next"}, "enqueue_unsupported"),
        ({"media_content_id": "rtsp://camera.example.com/feed"}, "media_unsupported"),
        ({"media_content_id": "https://radio.example.com/" + "a" * 2030}, "media_unsupported"),
    ],
)
async def test_play_media_refuses_what_a_speaker_cannot_play(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence, data: dict[str, Any], key: str
) -> None:
    with pytest.raises(ServiceValidationError) as err:
        await call(hass, "play_media", speaker(hass), media_content_type="music", **data)
    assert err.value.translation_key == key
    assert backend.commands == []


async def test_browse_media_offers_audio_from_media_sources(hass: HomeAssistant, loaded: MockConfigEntry) -> None:
    filters: list[Any] = []
    root = BrowseMedia(
        media_class=MediaClass.DIRECTORY,
        media_content_id="media-source://",
        media_content_type="",
        title="Media",
        can_play=False,
        can_expand=True,
    )

    async def browse(_: HomeAssistant, media_id: str | None, *, content_filter: Any = None) -> BrowseMedia:
        filters.append((media_id, content_filter))
        return root

    entity = player_entity(hass, speaker(hass))
    with patch.object(media_source, "async_browse_media", browse):
        assert await entity.async_browse_media(None, "media-source://media_source/local") is root
    ((media_id, content_filter),) = filters
    assert media_id == "media-source://media_source/local"

    def item(content_type: str) -> BrowseMedia:
        return BrowseMedia(
            media_class=MediaClass.MUSIC,
            media_content_id="x",
            media_content_type=content_type,
            title="x",
            can_play=True,
            can_expand=False,
        )

    assert content_filter(item("audio/mpeg"))
    assert not content_filter(item("video/mp4"))
    assert not content_filter(item("image/png"))


async def test_the_cover_is_read_by_its_hash_every_time(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence, hass_client: ClientSessionGenerator
) -> None:
    player = speaker(hass)
    entity = player_entity(hass, player)
    assert await entity.async_get_media_image() == (b"\x89PNG fake", "image/png")
    assert backend.artwork_asks == [HASH]

    # through home assistant's proxy, as a dashboard reads it
    client = await hass_client()
    resp = await client.get(hass.states.get(player).attributes["entity_picture"])
    assert resp.status == 200
    assert await resp.read() == b"\x89PNG fake"

    # another cover by now (409), none (404), the server could not fetch it (502), or another failure: no image for
    # this hash, and the next ask goes to the server again: no bytes are kept for any hash
    for status in (409, 404, 502, 500):
        backend.artwork = (status, b"", "")
        assert await entity.async_get_media_image() == (None, None)
    assert backend.artwork_asks == [HASH] * 6

    await backend.push(frame("media_state_idle.json"))
    await wait_for(lambda: hass.states.get(player).state == "idle")
    assert await entity.async_get_media_image() == (None, None)
    assert len(backend.artwork_asks) == 6


async def test_a_cover_url_serves_only_its_own_hash(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence, hass_client: ClientSessionGenerator
) -> None:
    player = speaker(hass)
    client = await hass_client()
    old = hass.states.get(player).attributes["entity_picture"]  # type: ignore[union-attr]
    # the track changes before the dashboard reads the cover the state showed
    other = "c0" * 32
    session = fixture("media_playing.json")
    session["metadata"]["artworkHash"] = other
    backend.media[KITCHEN] = session
    await backend.push(frame("media_state.json", metadata=session["metadata"]))
    await wait_for(lambda: other in hass.states.get(player).attributes["entity_picture"])  # type: ignore[union-attr]
    new = hass.states.get(player).attributes["entity_picture"]  # type: ignore[union-attr]

    # the old url asks for its own hash: the server's 409, so no image, never the new track's
    resp = await client.get(old)
    assert resp.status == 404
    assert backend.artwork_asks == [HASH]
    resp = await client.get(new)
    assert resp.status == 200
    assert await resp.read() == b"\x89PNG fake"
    assert backend.artwork_asks == [HASH, other]

    # a url that names no hash, or another kind of image, asks nothing
    token = URL(old).query["token"]
    for path in ("artwork/not-a-hash", f"music/{other}"):
        resp = await client.get(f"/api/media_player_proxy/{player}/browse_media/{path}?token={token}")
        assert resp.status == 404
    assert backend.artwork_asks == [HASH, other]


async def test_the_speaker_is_unavailable_while_the_feed_is_down_or_the_device_offline(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence
) -> None:
    player = speaker(hass)
    backend.unavailable = True
    await backend.close_feeds(1001)
    await wait_for(lambda: hass.states.get(player).state == "unavailable")
    backend.unavailable = False
    await wait_for(lambda: hass.states.get(player).state == "playing")

    await backend.push(frame("device_offline.json"))
    await wait_for(lambda: hass.states.get(player).state == "unavailable")
    await backend.push(frame("device_online.json"))
    await wait_for(lambda: hass.states.get(player).state == "playing")


async def test_a_capability_that_goes_away_leaves_the_speaker_unavailable(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence
) -> None:
    player = speaker(hass)
    capabilities = backend.details[KITCHEN]["capabilities"]
    backend.details[KITCHEN]["capabilities"] = []
    await backend.push(dropped(1))
    await wait_for(lambda: hass.states.get(player).state == "unavailable")
    # kept, not removed: the device may report it again
    assert speaker(hass) == player

    backend.details[KITCHEN]["capabilities"] = capabilities
    await backend.push(dropped(1))
    await wait_for(lambda: hass.states.get(player).state == "playing")


async def test_a_new_device_gets_its_speaker_when_it_links(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence
) -> None:
    backend.add_device(BEDROOM, "Bedroom")
    await backend.push(frame("device_added.json", BEDROOM, name="Bedroom"))
    await wait_for(lambda: entity_id(hass, "binary_sensor", BEDROOM, "online") is not None)
    assert entity_id(hass, "media_player", BEDROOM, "speaker") is None

    backend.link_device(BEDROOM)
    await backend.push(frame("device_online.json", BEDROOM, name="Bedroom"))
    await wait_for(lambda: entity_id(hass, "media_player", BEDROOM, "speaker") is not None)
    player = speaker(hass, BEDROOM)
    await wait_for(lambda: hass.states.get(player) is not None and hass.states.get(player).state == "idle")
    assert hass.states.get(player).attributes["volume_level"] == 0.35
