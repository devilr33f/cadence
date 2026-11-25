"""What the server answers, parsed leniently: unknown fields are ignored, a missing required one is a ValueError."""

from __future__ import annotations

from collections.abc import Mapping
from dataclasses import dataclass, field
from datetime import datetime
from typing import Any


@dataclass(frozen=True, slots=True)
class DeviceGroup:
    """The multiroom group a device is in: its elected leader ("" while none is online) and its members' ids in the
    group's order."""

    id: str
    name: str
    kind: str
    leader_id: str
    members: tuple[str, ...]

    @classmethod
    def from_json(cls, data: Any) -> DeviceGroup | None:
        """A device's group field (or a group_changed event's data): None for none, or for a deleted group."""
        if not isinstance(data, dict) or data.get("deleted") is True:
            return None
        return cls(
            id=_text(data, "id", required=True),
            name=_text(data, "name"),
            kind=_text(data, "kind"),
            leader_id=_text(data, "leaderId"),
            members=tuple(m for m in _list(data, "members") if isinstance(m, str) and m),
        )

    @classmethod
    def from_view(cls, data: Any) -> DeviceGroup:
        """A group as the groups routes answer it (a join's answer): its members are objects there."""
        if not isinstance(data, dict):
            raise ValueError("group")
        members = tuple(_text(m, "deviceId", required=True) for m in _list(data, "members"))
        group = cls.from_json({**data, "members": list(members)})
        if group is None:
            raise ValueError("group")
        return group


@dataclass(frozen=True, slots=True)
class Device:
    """A device as the server lists it."""

    id: str
    name: str
    model: str
    online: bool
    # enrolled, or pending: an enrol code not claimed yet
    status: str = ""
    # None: in no multiroom group
    group: DeviceGroup | None = None

    @classmethod
    def from_json(cls, data: Any) -> Device:
        return cls(
            id=_text(data, "id", required=True),
            name=_text(data, "name", required=True),
            model=_text(data, "model"),
            online=data.get("online") is True,
            status=_text(data, "status"),
            group=DeviceGroup.from_json(data.get("group")),
        )


@dataclass(frozen=True, slots=True)
class DeviceDetail:
    """A device with its capabilities: their command names and the audio.output level."""

    device: Device
    commands: Mapping[str, frozenset[str]]
    volume: int | None
    # the alarms capability's state; None without one, or one this client cannot read
    alarms: AlarmsState | None = None

    @property
    def capabilities(self) -> frozenset[str]:
        return frozenset(self.commands)

    @classmethod
    def from_json(cls, data: Any) -> DeviceDetail:
        device = Device.from_json(data)
        commands: dict[str, frozenset[str]] = {}
        volume = None
        alarms = None
        for cap in _list(data, "capabilities"):
            cap_id = _text(cap, "id", required=True)
            commands[cap_id] = frozenset(_text(c, "name", required=True) for c in _list(cap, "commands"))
            if cap_id == "audio.output" and isinstance(state := cap.get("state"), dict):
                volume = _level(state.get("level"))
            if cap_id == ALARMS and isinstance(state := cap.get("state"), dict):
                alarms = AlarmsState.from_json(state)
        return cls(device=device, commands=commands, volume=volume, alarms=alarms)


ALARMS = "alarms"
# the alarms state's lists and the kind of each one's items; another list is a kind this client does not know yet
ALARM_LISTS = {"alarms": "alarm", "timers": "timer", "reminders": "reminder"}
# the alarms state's keys that hold no items
_ALARM_FIELDS = frozenset({"ringing", "snoozed", "lastRing"})


@dataclass(frozen=True, slots=True)
class AlarmItem:
    """One alarm, timer or reminder (or an item of a kind this client does not know: its id, label and next alone).
    A reminder's label is the owner's own text: an attribute in Home Assistant, never logged."""

    kind: str
    id: str
    # when it rings next (a snoozed alarm: the snooze's end); None when the device does not say
    next: datetime | None
    time: str = ""
    days: tuple[str, ...] = ()
    date: str = ""
    label: str = ""
    seconds: int = 0
    ends_at: datetime | None = None

    @classmethod
    def from_json(cls, kind: str, data: Any) -> AlarmItem:
        return cls(
            kind=kind,
            id=_text(data, "id", required=True),
            next=_time(data.get("next")),
            time=_text(data, "time"),
            days=tuple(d for d in _list(data, "days") if isinstance(d, str) and d),
            date=_text(data, "date"),
            label=_text(data, "label"),
            seconds=_count(data.get("seconds")),
            ends_at=_time(data.get("endsAt")),
        )


@dataclass(frozen=True, slots=True)
class Ring:
    """What rings now: the state's ringing, or an alarm_ringing event (which also has its label and start)."""

    kind: str
    id: str
    label: str = ""
    started_at: datetime | None = None

    @classmethod
    def from_json(cls, data: Any) -> Ring:
        if not isinstance(data, dict):
            raise ValueError("ring")
        return cls(
            kind=_text(data, "kind", required=True),
            id=_text(data, "id", required=True),
            label=_text(data, "label"),
            started_at=_time(data.get("startedAt")),
        )


@dataclass(frozen=True, slots=True)
class RingEnd:
    """An alarm_ring_ended event: how a ring ended (stopped, snoozed, missed, or an outcome not known yet)."""

    kind: str
    id: str
    outcome: str
    stopped_by: str
    snoozed_until: datetime | None

    @classmethod
    def from_json(cls, data: Any) -> RingEnd:
        if not isinstance(data, dict):
            raise ValueError("ring_end")
        return cls(
            kind=_text(data, "kind", required=True),
            id=_text(data, "id", required=True),
            outcome=_text(data, "outcome", required=True),
            stopped_by=code_token(data.get("stoppedBy")),
            snoozed_until=_time(data.get("snoozedUntil")),
        )


@dataclass(frozen=True, slots=True)
class AlarmsState:
    """A device's alarms, timers and reminders, and what rings now. Read leniently: an item without an id is left out,
    a list of a kind not known yet is kept by its id, label and next, an older device lists no reminders."""

    items: tuple[AlarmItem, ...] = ()
    ringing: Ring | None = None
    # whether the device lists reminders at all
    has_reminders: bool = False

    @classmethod
    def from_json(cls, data: Any) -> AlarmsState:
        if not isinstance(data, dict):
            raise ValueError("alarms")
        items: list[AlarmItem] = []
        for key, value in data.items():
            if key in _ALARM_FIELDS or not isinstance(key, str) or not isinstance(value, list):
                continue
            kind = ALARM_LISTS.get(key, key.removesuffix("s"))
            for entry in value:
                try:
                    items.append(AlarmItem.from_json(kind, entry))
                except ValueError:
                    continue
        try:
            ringing = Ring.from_json(data["ringing"]) if isinstance(data.get("ringing"), dict) else None
        except ValueError:
            ringing = None
        return cls(items=tuple(items), ringing=ringing, has_reminders=isinstance(data.get("reminders"), list))

    def item(self, kind: str, item_id: str) -> AlarmItem | None:
        return next((i for i in self.items if i.kind == kind and i.id == item_id), None)


@dataclass(frozen=True, slots=True)
class MediaMetadata:
    """What a session plays. Its artwork is read through the server only (artworkHash): the url is never kept."""

    title: str
    artist: str
    album: str
    artwork_hash: str
    # what the item is (track, episode, movie, video, live, channel, podcast), "" when the device does not say
    kind: str = ""
    series: str = ""
    season: int = 0
    episode: str = ""
    episode_name: str = ""
    provider_name: str = ""
    provider_source: str = ""


@dataclass(frozen=True, slots=True)
class MediaSession:
    """A device's media session."""

    session_id: str
    source: str
    state: str
    position_ms: int
    position_at: datetime | None
    rate: float
    duration_ms: int
    volume: int | None
    metadata: MediaMetadata
    actions: frozenset[str]

    @classmethod
    def from_json(cls, data: Any) -> MediaSession | None:
        """The session, None for a device without one ({"state": "idle"})."""
        if not isinstance(data, dict):
            raise ValueError("media")
        if not data.get("sessionId"):
            return None
        meta = data.get("metadata") if isinstance(data.get("metadata"), dict) else {}
        return cls(
            session_id=_text(data, "sessionId", required=True),
            source=_text(data, "source"),
            state=_text(data, "state", required=True),
            position_ms=_count(data.get("positionMs")),
            position_at=_time(data.get("positionAt")),
            rate=_number(data.get("rate")),
            duration_ms=_count(data.get("durationMs")),
            volume=_level(data.get("volume")),
            metadata=MediaMetadata(
                title=_text(meta, "title"),
                artist=_text(meta, "artist"),
                album=_text(meta, "album"),
                artwork_hash=_text(meta, "artworkHash"),
                kind=_text(meta, "kind"),
                series=_text(meta, "series"),
                season=_count(meta.get("season")),
                episode=_text(meta, "episode"),
                episode_name=_text(meta, "episodeName"),
                provider_name=_text(meta, "providerName"),
                provider_source=_text(meta, "providerSource"),
            ),
            actions=frozenset(a for a in _list(data, "actions") if isinstance(a, str)),
        )


@dataclass(frozen=True, slots=True)
class Disk:
    mountpoint: str
    free_bytes: int
    limit_bytes: int


@dataclass(frozen=True, slots=True)
class Vitals:
    """A device's cpu (0 to 1), memory, disks and temperature from one metrics round."""

    cpu: float
    memory_used_bytes: int
    memory_limit_bytes: int
    disks: tuple[Disk, ...]
    temperature_c: float | None
    at: datetime | None

    @classmethod
    def from_json(cls, data: Any) -> Vitals:
        if not isinstance(data, dict) or not isinstance(data.get("cpu"), int | float):
            raise ValueError("vitals")
        temperature = data.get("temperatureC")
        return cls(
            cpu=_number(data.get("cpu")),
            memory_used_bytes=_count(data.get("memoryUsedBytes")),
            memory_limit_bytes=_count(data.get("memoryLimitBytes")),
            disks=tuple(
                Disk(
                    mountpoint=_text(d, "mountpoint", required=True),
                    free_bytes=_count(d.get("freeBytes")),
                    limit_bytes=_count(d.get("limitBytes")),
                )
                for d in _list(data, "disk")
            ),
            temperature_c=_number(temperature) if isinstance(temperature, int | float) else None,
            at=_time(data.get("at")),
        )


@dataclass(frozen=True, slots=True)
class Artwork:
    content_type: str
    data: bytes


@dataclass(frozen=True, slots=True)
class CatalogItem:
    """One item of the server's TV catalog: a title, an episode, a torrent. Its ref is the only name to hand back;
    its artwork an image id the server's proxy serves. Third-party text, never logged (rule 8, D11)."""

    ref: str
    kind: str
    title: str
    provider: str = ""
    source: str = ""
    subtitle: str = ""
    artwork: str = ""
    year: int = 0
    format: str = ""
    duration_s: int = 0
    playable: bool = False
    has_children: bool = False
    unavailable: bool = False
    # the episode's label, when the item is one
    episode: str = ""

    @classmethod
    def from_json(cls, data: Any) -> CatalogItem:
        if not isinstance(data, dict):
            raise ValueError("item")
        art = data.get("artwork") if isinstance(data.get("artwork"), dict) else {}
        episode = data.get("episode") if isinstance(data.get("episode"), dict) else {}
        show = data.get("show") if isinstance(data.get("show"), dict) else {}
        return cls(
            ref=_text(data, "ref", required=True),
            kind=_text(data, "kind", required=True),
            title=_text(data, "title", required=True),
            provider=_text(data, "provider"),
            source=_text(data, "source"),
            subtitle=_text(data, "subtitle"),
            artwork=_text(art, "hash"),
            year=_count(show.get("year")),
            format=_text(show, "format"),
            duration_s=_count(data.get("durationS")),
            playable=data.get("playable") is True,
            has_children=data.get("hasChildren") is True,
            unavailable=data.get("unavailable") is True,
            episode=_text(episode, "label"),
        )


@dataclass(frozen=True, slots=True)
class CatalogSection:
    """One row of a browse page: items, and the path that browses it in full."""

    id: str
    title: str
    path: str
    items: tuple[CatalogItem, ...]
    next_cursor: str = ""

    @classmethod
    def from_json(cls, data: Any) -> CatalogSection:
        if not isinstance(data, dict):
            raise ValueError("section")
        return cls(
            id=_text(data, "id", required=True),
            title=_text(data, "title"),
            path=_text(data, "path") or _text(data, "id"),
            items=tuple(CatalogItem.from_json(i) for i in _list(data, "items")),
            next_cursor=_text(data, "next"),
        )


@dataclass(frozen=True, slots=True)
class CatalogPage:
    """A browse answer: its sections, each with its own next page."""

    sections: tuple[CatalogSection, ...]

    @classmethod
    def from_json(cls, data: Any) -> CatalogPage:
        if not isinstance(data, dict):
            raise ValueError("page")
        return cls(sections=tuple(CatalogSection.from_json(s) for s in _list(data, "sections")))


@dataclass(frozen=True, slots=True)
class CatalogGroup:
    """A child group of an item: a season, an episode range."""

    id: str
    label: str

    @classmethod
    def from_json(cls, data: Any) -> CatalogGroup:
        if not isinstance(data, dict):
            raise ValueError("group")
        return cls(id=_text(data, "id", required=True), label=_text(data, "label", required=True))


@dataclass(frozen=True, slots=True)
class CatalogDetail:
    """An item read: itself, its children (a page), its child groups and its source variants."""

    item: CatalogItem
    children: tuple[CatalogItem, ...]
    groups: tuple[CatalogGroup, ...]
    variants: tuple[CatalogItem, ...]
    next_cursor: str = ""

    @classmethod
    def from_json(cls, data: Any) -> CatalogDetail:
        if not isinstance(data, dict):
            raise ValueError("detail")
        return cls(
            item=CatalogItem.from_json(data.get("item")),
            children=tuple(CatalogItem.from_json(i) for i in _list(data, "children")),
            groups=tuple(CatalogGroup.from_json(g) for g in _list(data, "childGroups")),
            variants=tuple(CatalogItem.from_json(i) for i in _list(data, "variants")),
            next_cursor=_text(data, "next"),
        )


@dataclass(frozen=True, slots=True)
class CatalogChoice:
    """One source, voiceover or quality choice; its id is what a play sends back."""

    id: str
    label: str
    detail: str = ""
    quality: int = 0
    unavailable: bool = False
    reason: str = ""

    @classmethod
    def from_json(cls, data: Any) -> CatalogChoice:
        if not isinstance(data, dict):
            raise ValueError("choice")
        stream = data.get("stream") if isinstance(data.get("stream"), dict) else {}
        torrent = data.get("torrent") if isinstance(data.get("torrent"), dict) else {}
        return cls(
            id=_text(data, "id", required=True),
            label=_text(data, "label", required=True),
            detail=_text(data, "detail"),
            quality=max(_count(stream.get("quality")), _count(torrent.get("quality"))),
            unavailable=data.get("unavailable") is True,
            reason=_text(data, "reason"),
        )


@dataclass(frozen=True, slots=True)
class CatalogChoiceGroup:
    kind: str
    label: str
    choices: tuple[CatalogChoice, ...]

    @classmethod
    def from_json(cls, data: Any) -> CatalogChoiceGroup:
        if not isinstance(data, dict):
            raise ValueError("choice_group")
        return cls(
            kind=_text(data, "kind", required=True),
            label=_text(data, "label", required=True),
            choices=tuple(CatalogChoice.from_json(c) for c in _list(data, "choices")),
        )


@dataclass(frozen=True, slots=True)
class CatalogOptions:
    groups: tuple[CatalogChoiceGroup, ...]
    selected: Mapping[str, str]
    remembered: frozenset[str]

    @classmethod
    def from_json(cls, data: Any) -> CatalogOptions:
        if not isinstance(data, dict) or not isinstance(data.get("selected"), dict):
            raise ValueError("options")
        selected = data["selected"]
        if not all(isinstance(k, str) and isinstance(v, str) for k, v in selected.items()):
            raise ValueError("selected")
        return cls(
            groups=tuple(CatalogChoiceGroup.from_json(g) for g in _list(data, "groups")),
            selected=selected,
            remembered=frozenset(g for g in _list(data, "remembered") if isinstance(g, str)),
        )


@dataclass(frozen=True, slots=True)
class CatalogAlternative:
    ref: str
    provider: str
    source: str
    source_name: str
    title: str
    episodes_aired: int
    episodes_total: int
    quality: int
    current: bool
    unavailable: bool

    @classmethod
    def from_json(cls, data: Any) -> CatalogAlternative:
        if not isinstance(data, dict):
            raise ValueError("alternative")
        return cls(
            ref=_text(data, "ref", required=True),
            provider=_text(data, "provider", required=True),
            source=_text(data, "source"),
            source_name=_text(data, "sourceName", required=True),
            title=_text(data, "title", required=True),
            episodes_aired=_count(data.get("episodesAired")),
            episodes_total=_count(data.get("episodesTotal")),
            quality=_count(data.get("quality")),
            current=data.get("current") is True,
            unavailable=data.get("unavailable") is True,
        )


@dataclass(frozen=True, slots=True)
class CatalogSession:
    """The playing catalog title and its current options, held in memory only."""

    session_id: str
    ref: str
    episode: str
    choices: Mapping[str, str]
    options: CatalogOptions

    @classmethod
    def from_json(cls, data: Any) -> CatalogSession:
        if not isinstance(data, dict):
            raise ValueError("catalog_session")
        choices = data.get("choices")
        if not isinstance(choices, dict) or not all(
            isinstance(k, str) and isinstance(v, str) for k, v in choices.items()
        ):
            raise ValueError("choices")
        return cls(
            session_id=_text(data, "sessionId", required=True),
            ref=_text(data, "ref", required=True),
            episode=_text(data, "episode"),
            choices=choices,
            options=CatalogOptions.from_json(data.get("options")),
        )


@dataclass(frozen=True, slots=True)
class FeedEvent:
    """One frame of the live feed: an event of a device, or dropped (the server lost count events for us)."""

    type: str
    device_id: str | None
    data: Any
    count: int

    @classmethod
    def from_json(cls, frame: Any) -> FeedEvent:
        if not isinstance(frame, dict) or not isinstance(frame.get("type"), str):
            raise ValueError("frame")
        device_id = frame.get("deviceId")
        return cls(
            type=frame["type"],
            device_id=device_id if isinstance(device_id, str) and device_id else None,
            data=frame.get("data"),
            count=_count(frame.get("count")),
        )


@dataclass(slots=True)
class CadenceDevice:
    """A device as the server last told it: what the coordinator keeps for each one."""

    id: str
    name: str
    model: str
    online: bool
    # capability id -> its command names
    commands: Mapping[str, frozenset[str]] = field(default_factory=dict)
    # the audio.output level, None without one
    volume: int | None = None
    # None: no session
    media: MediaSession | None = None
    # None: unknown until the device's next metrics round
    vitals: Vitals | None = None
    # None: in no multiroom group
    group: DeviceGroup | None = None
    # None: no alarms capability, or its state not read yet
    alarms: AlarmsState | None = None

    @property
    def capabilities(self) -> frozenset[str]:
        return frozenset(self.commands)


def _text(data: Any, key: str, *, required: bool = False) -> str:
    value = data.get(key) if isinstance(data, dict) else None
    if isinstance(value, str) and (value or not required):
        return value
    if required:
        raise ValueError(key)
    return ""


def _list(data: Any, key: str) -> list[Any]:
    value = data.get(key) if isinstance(data, dict) else None
    return value if isinstance(value, list) else []


def _count(value: Any) -> int:
    return value if isinstance(value, int) and not isinstance(value, bool) and value >= 0 else 0


def _number(value: Any) -> float:
    return float(value) if isinstance(value, int | float) and not isinstance(value, bool) else 0.0


def _level(value: Any) -> int | None:
    if isinstance(value, int) and not isinstance(value, bool) and 0 <= value <= 100:
        return value
    return None


def _time(value: Any) -> datetime | None:
    if not isinstance(value, str) or not value:
        return None
    try:
        parsed = datetime.fromisoformat(value)
    except ValueError:
        return None
    # the server writes rfc 3339 with an offset: a time without one is unreadable, never compared with an aware clock
    return parsed if parsed.tzinfo is not None else None


def code_token(value: Any) -> str:
    """value when it is a code token (lowercase ascii, digits, underscores), else ""."""
    if isinstance(value, str) and 0 < len(value) <= 64 and all(c.islower() or c.isdigit() or c == "_" for c in value):
        return value if value.isascii() else ""
    return ""
