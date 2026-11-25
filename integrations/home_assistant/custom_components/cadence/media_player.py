"""Each device's speaker: its session, volume and cover from the feed, its commands to the device's capabilities.
A device with a screen browses the server's TV catalog and plays it there, through the same play the screen
itself runs. Home Assistant is an authenticated caller, so it may set the volume (rule 7)."""

from __future__ import annotations

import asyncio
import logging
import re
from collections.abc import Awaitable, Callable
from datetime import datetime
from typing import Any

from homeassistant.components import media_source
from homeassistant.components.media_player import (
    ATTR_INPUT_SOURCE,
    ATTR_INPUT_SOURCE_LIST,
    ATTR_MEDIA_ALBUM_NAME,
    ATTR_MEDIA_ARTIST,
    ATTR_MEDIA_EPISODE,
    ATTR_MEDIA_SEASON,
    ATTR_MEDIA_SERIES_TITLE,
    ATTR_MEDIA_TITLE,
    ATTR_SOUND_MODE,
    ATTR_SOUND_MODE_LIST,
    BrowseMedia,
    MediaPlayerDeviceClass,
    MediaPlayerEnqueue,
    MediaPlayerEntity,
    MediaPlayerEntityFeature,
    MediaPlayerState,
    MediaType,
    SearchMedia,
    SearchMediaQuery,
    async_process_play_media_url,
)
from homeassistant.core import HomeAssistant, callback
from homeassistant.exceptions import HomeAssistantError, ServiceValidationError
from homeassistant.helpers import entity_registry as er
from homeassistant.helpers.entity_platform import AddConfigEntryEntitiesCallback
from homeassistant.util import dt as dt_util
from yarl import URL

from . import browse
from .client import CadenceError
from .const import DOMAIN
from .coordinator import CadenceConfigEntry, CadenceCoordinator
from .entity import CadenceEntity, follow_devices
from .errors import failure
from .models import Artwork, CadenceDevice, CatalogChoice, CatalogChoiceGroup, CatalogSession, MediaSession

_LOGGER = logging.getLogger(__name__)

KEY = "speaker"
AUDIO, PLAYER, URL_PLAYER = "audio.output", "media.player", "media.url"
# a screen's player: its play runs a catalog ref on the screen, as the screen's own remote does
SCREEN_PLAYER = "media.video"
# the speaker's own volume buttons step 5 %
VOLUME_STEP = 5
# media.url play takes http and https urls up to this long
MAX_URL = 2047
TTS_PROXY = "/api/tts_proxy/"
STATES = {
    "idle": MediaPlayerState.IDLE,
    "playing": MediaPlayerState.PLAYING,
    "paused": MediaPlayerState.PAUSED,
    "buffering": MediaPlayerState.BUFFERING,
}
# each offered while the device's media.player has the command and the current session lists it as an action: what
# the session's source can do now (the device refuses the rest)
TRANSPORT_FEATURES = {
    "pause": MediaPlayerEntityFeature.PAUSE,
    "resume": MediaPlayerEntityFeature.PLAY,
    "stop": MediaPlayerEntityFeature.STOP,
    "seek": MediaPlayerEntityFeature.SEEK,
    "next": MediaPlayerEntityFeature.NEXT_TRACK,
    "previous": MediaPlayerEntityFeature.PREVIOUS_TRACK,
}
# the image url's kind for a cover: its id is the artworkHash
ARTWORK = "artwork"
ARTWORK_HASH = re.compile(r"[0-9a-f]{64}")
# the image url's kind for a catalog item's art: its id is the server's image proxy id
CATALOG_IMAGE = "catalog"
URL_FEATURES = (
    MediaPlayerEntityFeature.PLAY_MEDIA
    | MediaPlayerEntityFeature.MEDIA_ANNOUNCE
    | MediaPlayerEntityFeature.BROWSE_MEDIA
)
# what a screen offers for the catalog: browsing it, searching it and playing its refs
CATALOG_FEATURES = MediaPlayerEntityFeature.PLAY_MEDIA | MediaPlayerEntityFeature.BROWSE_MEDIA


async def async_setup_entry(
    hass: HomeAssistant, entry: CadenceConfigEntry, async_add_entities: AddConfigEntryEntitiesCallback
) -> None:
    follow_devices(
        entry, async_add_entities, _keys, lambda coordinator, device_id, _: CadenceSpeaker(coordinator, device_id)
    )


def _keys(dev: CadenceDevice) -> list[str]:
    return [KEY] if {AUDIO, PLAYER, URL_PLAYER} & dev.capabilities else []


def session_position(session: MediaSession, now: datetime) -> tuple[int, datetime | None]:
    """The position in seconds and when it held. Home Assistant moves a playing position on at rate 1 from its stamp,
    so at any other rate it is worked out for now."""
    if session.position_at is None:
        return session.position_ms // 1000, None
    if session.state != "playing" or session.rate == 1:
        return session.position_ms // 1000, session.position_at
    elapsed_ms = (now - session.position_at).total_seconds() * 1000
    position_ms = max(0.0, session.position_ms + session.rate * elapsed_ms)
    if session.duration_ms:
        position_ms = min(position_ms, session.duration_ms)
    return int(position_ms // 1000), now


def _playable(url: str) -> bool:
    try:
        parsed = URL(url)
    except ValueError:
        return False
    return len(url) <= MAX_URL and parsed.scheme in ("http", "https") and bool(parsed.host)


def _speech(media_id: str, url: str) -> bool:
    """Text-to-speech media: from the tts media source, or a url of its proxy."""
    match = media_source.URI_SCHEME_REGEX.match(media_id)
    return (match is not None and match.group("domain") == "tts") or URL(url).path.startswith(TTS_PROXY)


def _audio(item: BrowseMedia) -> bool:
    return item.media_content_type.startswith("audio/")


# a session's kind as home assistant names what plays
CONTENT_TYPES = {
    "episode": MediaType.EPISODE,
    "movie": MediaType.MOVIE,
    "video": MediaType.VIDEO,
    "live": MediaType.VIDEO,
    "channel": MediaType.CHANNEL,
    "podcast": MediaType.EPISODE,
    "track": MediaType.MUSIC,
}


class CadenceSpeaker(CadenceEntity, MediaPlayerEntity):
    """A device's media session and volume; with a screen, the server's TV catalog to browse and play."""

    _attr_name = None
    _attr_device_class = MediaPlayerDeviceClass.SPEAKER
    _attr_volume_step = VOLUME_STEP / 100
    _needs_link = True
    # d11: what plays stays in memory, out of the recorder's database
    _unrecorded_attributes = frozenset(
        {
            ATTR_MEDIA_TITLE,
            ATTR_MEDIA_ARTIST,
            ATTR_MEDIA_ALBUM_NAME,
            ATTR_MEDIA_SERIES_TITLE,
            ATTR_MEDIA_SEASON,
            ATTR_MEDIA_EPISODE,
            ATTR_INPUT_SOURCE,
            ATTR_INPUT_SOURCE_LIST,
            ATTR_SOUND_MODE,
            ATTR_SOUND_MODE_LIST,
        }
    )

    def __init__(self, coordinator: CadenceCoordinator, device_id: str) -> None:
        super().__init__(coordinator, device_id, KEY)
        self._positioned: tuple[MediaSession, tuple[int, datetime | None]] | None = None
        # a video session's source and voiceover choices, read once per session and kept in memory only
        self._catalog_session: CatalogSession | None = None
        self._catalog_session_id = ""
        self._catalog_task: asyncio.Task[None] | None = None

    def _offered(self, dev: CadenceDevice) -> bool:
        return bool(_keys(dev))

    def _catalog(self) -> bool:
        """Whether the device is a screen the server plays catalog items on."""
        dev = self.device
        return dev is not None and "play" in dev.commands.get(SCREEN_PLAYER, frozenset())

    @property
    def _session(self) -> MediaSession | None:
        dev = self.device
        return dev.media if dev is not None else None

    async def async_added_to_hass(self) -> None:
        await super().async_added_to_hass()
        self.async_on_remove(self._cancel_catalog_read)
        self._sync_catalog_session()

    @callback
    def _handle_coordinator_update(self) -> None:
        self._sync_catalog_session()
        super()._handle_coordinator_update()

    @callback
    def _cancel_catalog_read(self) -> None:
        if self._catalog_task is not None:
            self._catalog_task.cancel()
            self._catalog_task = None

    @callback
    def _sync_catalog_session(self) -> None:
        session = self._session
        session_id = session.session_id if session is not None and session.source == "video" and self._catalog() else ""
        if session_id == self._catalog_session_id:
            return
        self._cancel_catalog_read()
        self._catalog_session_id = session_id
        self._catalog_session = None
        if session_id:
            self._read_catalog_session()

    @callback
    def _read_catalog_session(self) -> None:
        self._cancel_catalog_read()
        if not self._catalog_session_id:
            return
        self._catalog_task = self.coordinator.config_entry.async_create_background_task(
            self.hass, self._catalog_read(self._catalog_session_id), f"{DOMAIN} catalog session"
        )

    async def _catalog_read(self, session_id: str) -> None:
        try:
            current = await self.coordinator.client.catalog_session(self.device_id)
        except CadenceError as err:
            _LOGGER.debug("cadence_catalog_session_failed device_id=%s code=%s", self.device_id, err.code)
            current = None
        if session_id != self._catalog_session_id:
            return
        # a switch can keep the session id; a newer one is applied by its feed event instead
        if current is not None and current.session_id != session_id:
            return
        self._catalog_session = current
        self.async_write_ha_state()

    def _option_group(self, kind: str) -> CatalogChoiceGroup | None:
        current = self._catalog_session
        session = self._session
        if current is None or session is None or current.session_id != session.session_id:
            return None
        return next((group for group in current.options.groups if group.kind == kind), None)

    @staticmethod
    def _choice_label(group: CatalogChoiceGroup | None, choice_id: str) -> str | None:
        if group is None:
            return None
        choice = next((choice for choice in group.choices if choice.id == choice_id), None)
        return choice.label if choice is not None else None

    def _option_labels(self, kind: str) -> list[str] | None:
        if (group := self._option_group(kind)) is None:
            return None
        return [choice.label for choice in group.choices if not choice.unavailable]

    def _position(self) -> tuple[int | None, datetime | None]:
        """Worked out once per session the feed sent, so the position and its stamp agree."""
        if (session := self._session) is None:
            return None, None
        if self._positioned is None or self._positioned[0] is not session:
            self._positioned = (session, session_position(session, dt_util.utcnow()))
        return self._positioned[1]

    @property
    def state(self) -> MediaPlayerState | None:
        if (session := self._session) is None:
            return MediaPlayerState.IDLE
        return STATES.get(session.state, MediaPlayerState.ON)

    @property
    def supported_features(self) -> MediaPlayerEntityFeature:
        features = MediaPlayerEntityFeature(0)
        if (dev := self.device) is None:
            return features
        player = dev.commands.get(PLAYER, frozenset())
        offered = dev.media.actions if dev.media is not None else frozenset()
        for command, feature in TRANSPORT_FEATURES.items():
            if command in player and command in offered:
                features |= feature
        volume = dev.commands.get(AUDIO, frozenset())
        if "set_level" in volume:
            features |= MediaPlayerEntityFeature.VOLUME_SET
        if "change_level" in volume:
            features |= MediaPlayerEntityFeature.VOLUME_STEP
        if "play" in dev.commands.get(URL_PLAYER, frozenset()):
            # a speaker that plays urls plays a group's stream too
            features |= URL_FEATURES | MediaPlayerEntityFeature.GROUPING
        if "play" in dev.commands.get(SCREEN_PLAYER, frozenset()):
            features |= CATALOG_FEATURES
        if self._option_group("source") is not None:
            features |= MediaPlayerEntityFeature.SELECT_SOURCE
        if self._option_group("voiceover") is not None:
            features |= MediaPlayerEntityFeature.SELECT_SOUND_MODE
        return features

    @property
    def volume_level(self) -> float | None:
        dev = self.device
        return dev.volume / 100 if dev is not None and dev.volume is not None else None

    @property
    def media_content_type(self) -> MediaType | None:
        session = self._session
        if session is None:
            return None
        return CONTENT_TYPES.get(session.metadata.kind, MediaType.MUSIC)

    @property
    def media_title(self) -> str | None:
        session = self._session
        if session is None:
            return None
        # an episode's own name is its title; the show's name is media_series_title beside it
        return session.metadata.episode_name or session.metadata.title or None

    @property
    def media_series_title(self) -> str | None:
        session = self._session
        return session.metadata.series or None if session is not None else None

    @property
    def media_season(self) -> int | None:
        session = self._session
        return session.metadata.season or None if session is not None else None

    @property
    def media_episode(self) -> int | str | None:
        session = self._session
        if session is None:
            return None
        # the label as its provider keys it ("13", "5.5", "OVA 1"): a number when it is one
        label = session.metadata.episode
        try:
            number = float(label)
        except ValueError:
            return label or None
        return int(number) if number.is_integer() else number

    @property
    def media_artist(self) -> str | None:
        session = self._session
        if session is None:
            return None
        meta = session.metadata
        if meta.artist:
            return meta.artist
        # a video's line under the title: where it plays from (torrplayer · anime365)
        source = " · ".join(part for part in (meta.provider_name, meta.provider_source) if part)
        return source or None

    @property
    def media_album_name(self) -> str | None:
        session = self._session
        return session.metadata.album or None if session is not None else None

    @property
    def media_duration(self) -> int | None:
        # a live stream has none
        session = self._session
        return session.duration_ms // 1000 or None if session is not None else None

    @property
    def media_position(self) -> int | None:
        return self._position()[0]

    @property
    def media_position_updated_at(self) -> datetime | None:
        return self._position()[1]

    @property
    def source(self) -> str | None:
        """The catalog source playing, as select_source's list labels it."""
        current = self._catalog_session
        return (
            self._choice_label(self._option_group("source"), current.options.selected.get("source", ""))
            if current
            else None
        )

    @property
    def source_list(self) -> list[str] | None:
        return self._option_labels("source")

    @property
    def sound_mode(self) -> str | None:
        """The voiceover is an audio presentation mode, unlike video quality, so HA's sound mode fits it."""
        current = self._catalog_session
        return (
            self._choice_label(self._option_group("voiceover"), current.options.selected.get("voiceover", ""))
            if current
            else None
        )

    @property
    def sound_mode_list(self) -> list[str] | None:
        return self._option_labels("voiceover")

    async def async_select_source(self, source: str) -> None:
        await self._select_catalog("source", source)

    async def async_select_sound_mode(self, sound_mode: str) -> None:
        await self._select_catalog("voiceover", sound_mode)

    async def _select_catalog(self, group_kind: str, label: str) -> None:
        group = self._option_group(group_kind)
        choice: CatalogChoice | None = None
        if group is not None:
            choice = next((c for c in group.choices if not c.unavailable and c.label == label), None)
        if choice is None:
            raise ServiceValidationError(
                translation_domain=DOMAIN,
                translation_key="invalid_choice",
                translation_placeholders={"target": self._target()},
            )
        try:
            await self.coordinator.client.select_catalog(self.device_id, group_kind, choice.id)
        except CadenceError as err:
            raise failure(err, self._target()) from err
        # a same-id switch has no new feed key to wait for; a new session id is read by its feed event
        self._read_catalog_session()

    @property
    def media_image_hash(self) -> str | None:
        session = self._session
        return session.metadata.artwork_hash or None if session is not None else None

    @property
    def media_image_local(self) -> str | None:
        # the url names the cover's hash and is answered by that hash alone: a browser that keeps it for an hour never
        # keeps another track's cover under it (m8-r3)
        if (artwork_hash := self.media_image_hash) is None:
            return None
        return self.get_browse_image_url(ARTWORK, artwork_hash)

    async def async_get_browse_image(
        self, media_content_type: str, media_content_id: str, media_image_id: str | None = None
    ) -> tuple[bytes | None, str | None]:
        """The image the url names: the session's cover by its hash (409 otherwise: no image), or a catalog item's
        art by its proxy id."""
        if media_content_type == CATALOG_IMAGE and browse.image_id(media_content_id):
            return await self._image(lambda: self.coordinator.client.catalog_image(media_content_id))
        if media_content_type != ARTWORK or not ARTWORK_HASH.fullmatch(media_content_id):
            return None, None
        return await self._artwork(media_content_id)

    async def async_get_media_image(self) -> tuple[bytes | None, str | None]:
        """The cover of the hash the state shows now, for home assistant's other readers."""
        if (artwork_hash := self.media_image_hash) is None:
            return None, None
        return await self._artwork(artwork_hash)

    async def _artwork(self, artwork_hash: str) -> tuple[bytes | None, str | None]:
        return await self._image(lambda: self.coordinator.client.get_artwork(self.device_id, artwork_hash))

    async def _image(self, fetch: Callable[[], Awaitable[Artwork | None]]) -> tuple[bytes | None, str | None]:
        """Read through the server every time, by its id: no bytes are kept, so none can stand for another id."""
        try:
            art = await fetch()
        except CadenceError as err:
            _LOGGER.debug("cadence_image_failed device_id=%s code=%s", self.device_id, err.code)
            return None, None
        return (art.data, art.content_type) if art is not None else (None, None)

    @property
    def group_members(self) -> list[str]:
        """The group's speakers as entity ids, its leader first; [] while the device is in no group. A member without
        a speaker of this integration (one that never reported one) is left out."""
        dev = self.device
        if dev is None or dev.group is None:
            return []
        group = dev.group
        leader = group.leader_id if group.leader_id in group.members else (group.members[0] if group.members else "")
        ordered = [leader, *(m for m in group.members if m != leader)] if leader else []
        registry = er.async_get(self.hass)
        found = (registry.async_get_entity_id("media_player", DOMAIN, f"{m}:{KEY}") for m in ordered)
        return [entity for entity in found if entity is not None]

    async def async_join_players(self, group_members: list[str]) -> None:
        """This speaker leads a group with the others: its own group extended, or a new one named after it."""
        registry = er.async_get(self.hass)
        others: list[str] = []
        for member in group_members:
            if member == self.entity_id:
                continue
            entry = registry.async_get(member)
            unique_id = entry.unique_id if entry is not None else ""
            if (
                entry is None
                or entry.platform != DOMAIN
                or entry.domain != "media_player"
                or entry.config_entry_id != self.coordinator.config_entry.entry_id
                or not unique_id.endswith(f":{KEY}")
            ):
                raise ServiceValidationError(
                    translation_domain=DOMAIN,
                    translation_key="not_a_group_member",
                    translation_placeholders={"target": self._target(), "entity": member},
                )
            device_id = unique_id.removesuffix(f":{KEY}")
            if device_id != self.device_id and device_id not in others:
                others.append(device_id)
        if not others:
            return
        try:
            group = await self.coordinator.client.join_group(self.device_id, others)
        except CadenceError as err:
            raise failure(err, self._target()) from err
        self.coordinator.apply_group(group, self.device_id)

    async def async_unjoin_player(self) -> None:
        """This speaker leaves its group; a group left with one member, or a stereo pair, is gone."""
        try:
            await self.coordinator.client.leave_group(self.device_id)
        except CadenceError as err:
            raise failure(err, self._target()) from err
        self.coordinator.apply_group(None, self.device_id)

    async def async_media_pause(self) -> None:
        await self._send(PLAYER, "pause")

    async def async_media_play(self) -> None:
        await self._send(PLAYER, "resume")

    async def async_media_stop(self) -> None:
        await self._send(PLAYER, "stop")

    async def async_media_seek(self, position: float) -> None:
        await self._send(PLAYER, "seek", {"positionMs": round(position * 1000)})

    async def async_media_next_track(self) -> None:
        await self._send(PLAYER, "next")

    async def async_media_previous_track(self) -> None:
        await self._send(PLAYER, "previous")

    async def async_set_volume_level(self, volume: float) -> None:
        await self._send(AUDIO, "set_level", {"level": min(100, max(0, round(volume * 100)))})

    async def async_volume_up(self) -> None:
        await self._step(1)

    async def async_volume_down(self) -> None:
        await self._step(-1)

    async def _step(self, sign: int) -> None:
        dev = self.device
        if dev is None or "change_level" not in dev.commands.get(AUDIO, frozenset()):
            await (super().async_volume_up() if sign > 0 else super().async_volume_down())
            return
        # the device steps from its own level: a press meanwhile never turns a step down into a raise
        await self._send(AUDIO, "change_level", {"delta": sign * VOLUME_STEP})

    async def async_play_media(
        self,
        media_type: MediaType | str,
        media_id: str,
        enqueue: MediaPlayerEnqueue | None = None,
        announce: bool | None = None,
        **kwargs: Any,
    ) -> None:
        """Plays a url, a catalog ref of the server's TV catalog, or what a media source resolves to: as the
        device's session, or over it as an announcement (text-to-speech too)."""
        # a speaker has no queue: it plays now or not at all
        if enqueue not in (None, MediaPlayerEnqueue.REPLACE, MediaPlayerEnqueue.PLAY):
            raise ServiceValidationError(translation_domain=DOMAIN, translation_key="enqueue_unsupported")
        if (item := browse.play(media_id)) is not None:
            await self._play_catalog(*item)
            return
        url = media_id
        if media_source.is_media_source_id(media_id):
            sourced = await media_source.async_resolve_media(self.hass, media_id, self.entity_id)
            # the device has no session of home assistant's: an absolute url, signed where it needs one
            url = async_process_play_media_url(self.hass, sourced.url)
        if not _playable(url):
            raise ServiceValidationError(translation_domain=DOMAIN, translation_key="media_unsupported")
        overlay = bool(announce) or _speech(media_id, url)
        await self._send(URL_PLAYER, "play", {"url": url, "mode": "overlay" if overlay else "session"})

    async def _play_catalog(self, ref: str, episode: str, choices: dict[str, str]) -> None:
        """Plays the exact choices on the screen; its successful resolve remembers them for the title."""
        try:
            await self.coordinator.client.play_catalog(self.device_id, ref, episode, choices)
        except CadenceError as err:
            raise failure(err, self._target()) from err

    async def async_browse_media(
        self, media_content_type: MediaType | str | None = None, media_content_id: str | None = None
    ) -> BrowseMedia:
        """A screen browses the server's TV catalog; a speaker home assistant's own audio (media sources)."""
        if not self._catalog() or (media_content_id is not None and media_source.is_media_source_id(media_content_id)):
            return await media_source.async_browse_media(self.hass, media_content_id, content_filter=_audio)
        try:
            return await browse.tree(self.coordinator.client, self._target(), self._catalog_image_url, media_content_id)
        except CadenceError as err:
            raise failure(err, self._target()) from err

    async def async_search_media(self, query: SearchMediaQuery) -> SearchMedia:
        """A text search over every catalog provider, as the media browser's search field asks."""
        if not self._catalog():
            raise HomeAssistantError(
                translation_domain=DOMAIN,
                translation_key="no_catalog",
                translation_placeholders={"target": self._target()},
            )
        try:
            return await browse.search(self.coordinator.client, query, self._catalog_image_url)
        except CadenceError as err:
            raise failure(err, self._target()) from err

    def _catalog_image_url(self, image: str) -> str | None:
        # the entity's own proxy path: the browser asks home assistant, which asks the server with the token
        return self.get_browse_image_url(CATALOG_IMAGE, image)

    async def _send(self, capability: str, name: str, args: dict[str, Any] | None = None) -> None:
        try:
            await self.coordinator.client.send_command(self.device_id, capability, name, args)
        except CadenceError as err:
            raise failure(err, self._target()) from err

    def _target(self) -> str:
        """The speaker's name as home assistant shows it, for an error."""
        if (entry := self.device_entry) is not None and (name := entry.name_by_user or entry.name):
            return name
        dev = self.device
        return dev.name if dev is not None else self.device_id
