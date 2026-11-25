"""The state of one server's devices, read at every (re)connect of the live feed and moved by its events alone."""

from __future__ import annotations

import asyncio
import logging
from collections.abc import Callable
from dataclasses import replace
from functools import partial

from homeassistant.config_entries import ConfigEntry
from homeassistant.core import CALLBACK_TYPE, HomeAssistant, callback
from homeassistant.exceptions import ConfigEntryAuthFailed
from homeassistant.helpers.device_registry import DeviceInfo
from homeassistant.helpers.update_coordinator import DataUpdateCoordinator, UpdateFailed

from . import registry
from .client import CadenceAuthError, CadenceClient, CadenceError
from .const import DOMAIN, MANUFACTURER
from .feed import Backoff, LiveFeed
from .models import (
    ALARMS,
    AlarmsState,
    CadenceDevice,
    Device,
    DeviceDetail,
    DeviceGroup,
    FeedEvent,
    MediaSession,
    Ring,
    RingEnd,
    Vitals,
)
from .reads import list_enrolled, read_devices

_LOGGER = logging.getLogger(__name__)

# the feed's reconnect delays: base and cap, in seconds (M8-D7)
FEED_BACKOFF = (1.0, 30.0)
# an event of a device the coordinator does not know reads the list again, at most once in this long
LIST_SYNC_INTERVAL = 5.0
# a device's detail is read again this long after the event that asked for it: its capability report follows its link
DETAIL_DEBOUNCE = 2.0
# how a ring ended, as the alarm event entity names it; an outcome not known yet fires nothing
RING_OUTCOMES = frozenset({"stopped", "snoozed", "missed"})
RING_STARTED = "ring_started"

# what an alarm listener is told: the event type and its attributes
type AlarmListener = Callable[[str, dict[str, str]], None]

type CadenceConfigEntry = ConfigEntry[CadenceCoordinator]


class CadenceCoordinator(DataUpdateCoordinator[dict[str, CadenceDevice]]):
    """One server's devices by id. Push only: the feed's events move them, and every (re)connect or dropped frame
    reads them all again, since the server replays nothing. Unavailable while the feed is down."""

    config_entry: CadenceConfigEntry

    def __init__(self, hass: HomeAssistant, entry: CadenceConfigEntry, client: CadenceClient) -> None:
        super().__init__(hass, _LOGGER, config_entry=entry, name=DOMAIN, update_interval=None)
        self.client = client
        self.connected = False
        self._feed_started = False
        self._device_callbacks: list[Callable[[list[str]], None]] = []
        self._device_listeners: dict[str, list[Callable[[], None]]] = {}
        self._alarm_listeners: dict[str, list[AlarmListener]] = {}
        # ids of unknown devices the list was read again for, since the last full read
        self._asked: set[str] = set()
        self._listed_at: float | None = None
        # one detail read waiting or in flight per device; the ids whose read is in flight, and those asked for again
        # while it was
        self._detail_reads: dict[str, asyncio.Task[None]] = {}
        self._detail_reading: set[str] = set()
        self._detail_again: set[str] = set()

    @property
    def devices(self) -> dict[str, CadenceDevice]:
        return self.data or {}

    def device(self, device_id: str) -> CadenceDevice | None:
        return self.devices.get(device_id)

    def device_info(self, device_id: str) -> DeviceInfo:
        """The Home Assistant device an entity of this device belongs to."""
        info = DeviceInfo(identifiers={(DOMAIN, device_id)}, manufacturer=MANUFACTURER)
        if (dev := self.device(device_id)) is not None:
            info["name"], info["model"] = dev.name, dev.model
        return info

    @callback
    def async_add_device_callback(self, add: Callable[[list[str]], None]) -> CALLBACK_TYPE:
        """Calls add with the ids of the devices known now, then with each new one's."""
        self._device_callbacks.append(add)
        if self.devices:
            add(list(self.devices))

        @callback
        def remove() -> None:
            self._device_callbacks.remove(add)

        return remove

    @callback
    def async_add_device_listener(self, device_id: str, listener: Callable[[], None]) -> CALLBACK_TYPE:
        """Calls listener whenever this device's capabilities (their command sets), name or model change, after the
        coordinator's data holds the change: a platform adds the entities a new capability needs from it."""
        self._device_listeners.setdefault(device_id, []).append(listener)

        @callback
        def remove() -> None:
            listeners = self._device_listeners.get(device_id, [])
            if listener in listeners:
                listeners.remove(listener)

        return remove

    @callback
    def async_add_alarm_listener(self, device_id: str, listener: AlarmListener) -> CALLBACK_TYPE:
        """Calls listener with each ring of this device: its start, and how it ended."""
        self._alarm_listeners.setdefault(device_id, []).append(listener)

        @callback
        def remove() -> None:
            listeners = self._alarm_listeners.get(device_id, [])
            if listener in listeners:
                listeners.remove(listener)

        return remove

    async def _async_update_data(self) -> dict[str, CadenceDevice]:
        if self._feed_started:
            # the feed owns the state now: a refresh asked for elsewhere neither reads beside it (and undoes the
            # events it applied meanwhile) nor makes a feed that is down look up
            if self.connected:
                return self.devices
            raise UpdateFailed(
                translation_domain=DOMAIN,
                translation_key="cannot_connect",
                translation_placeholders={"code": "feed_down"},
            )
        try:
            devices = await self._read_all()
        except CadenceAuthError as err:
            raise ConfigEntryAuthFailed(translation_domain=DOMAIN, translation_key="invalid_auth") from err
        except CadenceError as err:
            raise UpdateFailed(
                translation_domain=DOMAIN, translation_key="cannot_connect", translation_placeholders={"code": err.code}
            ) from err
        registry.sync(self.hass, self.config_entry.entry_id, devices)
        return devices

    async def async_run_feed(self) -> None:
        """Follows the live feed until the entry unloads; a refused token starts a reauth instead of retrying."""
        self._feed_started = True
        try:
            await LiveFeed(self.client, self, backoff=Backoff(*FEED_BACKOFF)).run()
        except CadenceAuthError:
            self.config_entry.async_start_reauth(self.hass)

    async def feed_connected(self) -> None:
        devices = await self._read_all()
        self._asked.clear()
        self._set_devices(devices)
        if not self.connected:
            _LOGGER.info("cadence_feed_up devices=%d", len(devices))
        self.connected = True

    def feed_disconnected(self, code: str) -> None:
        if self.connected or self.last_update_success:
            _LOGGER.warning("cadence_feed_down code=%s", code)
        else:
            _LOGGER.debug("cadence_feed_down code=%s", code)
        self.connected = False
        self.last_update_success = False
        self.async_update_listeners()

    async def feed_event(self, event: FeedEvent) -> None:
        if event.type == "dropped":
            _LOGGER.info("cadence_feed_dropped count=%d", event.count)
            await self.feed_connected()
            return
        if event.type == "group_changed":
            self._group_changed(event)
            return
        if event.device_id is None:
            return
        if event.type == "device_added":
            # an enrolment is always news: the id may have been asked for while it was pending (a rename)
            self._asked.discard(event.device_id)
        dev = self.devices.get(event.device_id)
        if dev is None:
            # a late event of a deleted device, or a device not read yet: never one made from the event
            if event.type != "device_deleted" and event.device_id not in self._asked:
                self._asked.add(event.device_id)
                await self._read_new_devices()
            return
        try:
            changed = self._apply(dev, event)
        except ValueError:
            _LOGGER.debug("cadence_event_invalid type=%s device_id=%s", event.type, dev.id)
            return
        if changed:
            self.async_update_listeners()

    def _apply(self, dev: CadenceDevice, event: FeedEvent) -> bool:
        data = event.data if isinstance(event.data, dict) else {}
        match event.type:
            case "device_online" | "device_offline" | "device_added" | "device_renamed":
                listed = Device.from_json({**data, "id": dev.id})
                renamed = (listed.name, listed.model) != (dev.name, dev.model)
                dev.name, dev.model, dev.online = listed.name, listed.model, listed.online
                if renamed:
                    registry.register(self.hass, self.config_entry.entry_id, dev)
                    self._device_changed(dev.id)
                # a link (a new daemon too) reports its capabilities right after its hello: read them then
                if event.type == "device_online":
                    self._read_detail_later(dev.id)
            case "device_deleted":
                self._remove(dev.id)
                # its late events (an online, held vitals) need no list read: a deleted id never comes back
                self._asked.add(dev.id)
            case "device_volume":
                level = data.get("level")
                if not isinstance(level, int) or isinstance(level, bool) or not 0 <= level <= 100:
                    raise ValueError("level")
                dev.volume = level
                if "audio.output" not in dev.commands:
                    self._read_detail_later(dev.id)
            case "media_state":
                dev.media = MediaSession.from_json(data)
                if dev.media is not None and "media.player" not in dev.commands:
                    self._read_detail_later(dev.id)
            case "device_vitals":
                dev.vitals = Vitals.from_json(data)
            case "alarms_state":
                dev.alarms = AlarmsState.from_json(data)
                if ALARMS not in dev.commands:
                    self._read_detail_later(dev.id)
            case "alarm_ringing":
                self._ring_started(dev, Ring.from_json(data))
            case "alarm_ring_ended":
                self._ring_ended(dev, RingEnd.from_json(data))
            case _:
                return False
        return True

    def _ring_started(self, dev: CadenceDevice, ring: Ring) -> None:
        state = dev.alarms or AlarmsState()
        dev.alarms = replace(state, ringing=ring)
        attrs = {"kind": ring.kind, "id": ring.id}
        if label := _ring_label(state, ring.kind, ring.id, ring.label):
            attrs["label"] = label
        self._alarm_event(dev.id, RING_STARTED, attrs)

    def _ring_ended(self, dev: CadenceDevice, end: RingEnd) -> None:
        state = dev.alarms or AlarmsState()
        ringing = state.ringing
        same = ringing is not None and (ringing.kind, ringing.id) == (end.kind, end.id)
        if same:
            dev.alarms = replace(state, ringing=None)
        if end.outcome not in RING_OUTCOMES:
            _LOGGER.debug("cadence_ring_outcome_unknown device_id=%s", dev.id)
            return
        attrs = {"kind": end.kind, "id": end.id}
        if label := _ring_label(state, end.kind, end.id, ringing.label if same and ringing is not None else ""):
            attrs["label"] = label
        if end.stopped_by:
            attrs["stopped_by"] = end.stopped_by
        if end.snoozed_until is not None:
            attrs["snoozed_until"] = end.snoozed_until.isoformat()
        self._alarm_event(dev.id, end.outcome, attrs)

    def _alarm_event(self, device_id: str, event_type: str, attrs: dict[str, str]) -> None:
        for listener in list(self._alarm_listeners.get(device_id, [])):
            _isolated(device_id, partial(listener, event_type, attrs))

    def _group_changed(self, event: FeedEvent) -> None:
        """A group's membership or config moved: its members take it at once, a device that left it drops it, and each
        of them has its detail read again, which says where it ended up."""
        data = event.data if isinstance(event.data, dict) else {}
        try:
            group = DeviceGroup.from_json(data)
            group_id = group.id if group is not None else data.get("id")
        except ValueError:
            _LOGGER.debug("cadence_event_invalid type=%s", event.type)
            return
        if not isinstance(group_id, str) or not group_id:
            _LOGGER.debug("cadence_event_invalid type=%s", event.type)
            return
        members = set(group.members) if group is not None else set()
        affected = [
            dev
            for dev in self.devices.values()
            if dev.id in members or (dev.group is not None and dev.group.id == group_id)
        ]
        for dev in affected:
            dev.group = group if dev.id in members else None
            self._read_detail_later(dev.id)
        if affected:
            self.async_update_listeners()

    @callback
    def apply_group(self, group: DeviceGroup | None, device_id: str) -> None:
        """What a join or leave answered, before its group_changed comes: the group's members take it (None: the
        device left), and a device that was in it and is no longer listed drops it."""
        if group is None:
            if (dev := self.device(device_id)) is not None and dev.group is not None:
                dev.group = None
                self.async_update_listeners()
            return
        for dev in self.devices.values():
            if dev.id in group.members:
                dev.group = group
            elif dev.group is not None and dev.group.id == group.id:
                dev.group = None
        self.async_update_listeners()

    @callback
    def _read_detail_later(self, device_id: str) -> None:
        """Reads the device's detail again after DETAIL_DEBOUNCE: one read per device waiting or in flight. An ask while
        it waits is covered by it; one while it is in flight gets one more read after it."""
        if (task := self._detail_reads.get(device_id)) is not None and not task.done():
            if device_id in self._detail_reading:
                self._detail_again.add(device_id)
            return
        self._detail_reads[device_id] = self.config_entry.async_create_background_task(
            self.hass, self._read_detail(device_id), f"{DOMAIN} device detail"
        )

    async def _read_detail(self, device_id: str) -> None:
        await asyncio.sleep(DETAIL_DEBOUNCE)
        self._detail_reading.add(device_id)
        try:
            detail: DeviceDetail | None = await self.client.get_device(device_id)
        except CadenceError as err:
            # a reconnect reads every device again; a refused token is the feed's to report
            _LOGGER.debug("cadence_detail_failed device_id=%s code=%s", device_id, err.code)
            detail = None
        finally:
            self._detail_reading.discard(device_id)
        self._detail_reads.pop(device_id, None)
        if detail is not None and (dev := self.device(device_id)) is not None:
            self._take_detail(dev, detail)
        if device_id in self._detail_again:
            self._detail_again.discard(device_id)
            if device_id in self.devices:
                self._read_detail_later(device_id)

    def _take_detail(self, dev: CadenceDevice, detail: DeviceDetail) -> None:
        changed = dict(dev.commands) != dict(detail.commands)
        dev.commands = detail.commands
        dev.group = detail.device.group
        # a device_volume that came meanwhile is newer than the level read
        if dev.volume is None:
            dev.volume = detail.volume
        # so is an alarms_state
        if dev.alarms is None:
            dev.alarms = detail.alarms
        self.async_update_listeners()
        if changed:
            self._device_changed(dev.id)

    @callback
    def _device_changed(self, device_id: str) -> None:
        for listener in list(self._device_listeners.get(device_id, [])):
            _isolated(device_id, listener)

    async def _read_all(self) -> dict[str, CadenceDevice]:
        self._listed_at = asyncio.get_running_loop().time()
        return await read_devices(self.client, await list_enrolled(self.client))

    async def _read_new_devices(self) -> None:
        """Reads the list again for a device the coordinator does not know: the new ones are read whole."""
        loop = asyncio.get_running_loop()
        if self._listed_at is not None and (wait := self._listed_at + LIST_SYNC_INTERVAL - loop.time()) > 0:
            await asyncio.sleep(wait)
        self._listed_at = loop.time()
        listed = await list_enrolled(self.client)
        known = self.devices
        devices = {
            # the list is the truth: a known device it no longer has is gone
            d.id: replace(known[d.id], name=d.name, model=d.model, online=d.online, group=d.group)
            for d in listed
            if d.id in known
        }
        devices.update(await read_devices(self.client, (d for d in listed if d.id not in known)))
        self._set_devices(devices)

    def _set_devices(self, devices: dict[str, CadenceDevice]) -> None:
        before = self.devices
        registry.sync(self.hass, self.config_entry.entry_id, devices)
        self.async_set_updated_data(devices)
        changed = [
            device_id
            for device_id, dev in devices.items()
            if (old := before.get(device_id)) is not None
            and (dict(old.commands), old.name, old.model) != (dict(dev.commands), dev.name, dev.model)
        ]
        for device_id in before.keys() - devices.keys():
            self._forget(device_id)
        for device_id in changed:
            self._device_changed(device_id)
        if new := [device_id for device_id in devices if device_id not in before]:
            for add in list(self._device_callbacks):
                _isolated(",".join(new), partial(add, new))

    def _remove(self, device_id: str) -> None:
        devices = dict(self.devices)
        devices.pop(device_id, None)
        registry.forget(self.hass, self.config_entry.entry_id, device_id)
        self._forget(device_id)
        self.data = devices

    def _forget(self, device_id: str) -> None:
        """A device that is gone: its pending detail read is dropped, and its listeners with it."""
        if (task := self._detail_reads.pop(device_id, None)) is not None:
            task.cancel()
        self._detail_reading.discard(device_id)
        self._detail_again.discard(device_id)
        self._device_listeners.pop(device_id, None)
        self._alarm_listeners.pop(device_id, None)


def _ring_label(state: AlarmsState, kind: str, item_id: str, label: str) -> str:
    """A reminder's label, which the owner allowed into Home Assistant; other kinds carry none there."""
    if kind != "reminder":
        return ""
    if label:
        return label
    item = state.item(kind, item_id)
    return item.label if item is not None else ""


def _isolated(ids: str, listener: Callable[[], None]) -> None:
    """Runs a platform's listener: one that fails is logged and the others still run, and the feed goes on."""
    try:
        listener()
    except Exception as err:
        _LOGGER.warning("cadence_listener_failed device_id=%s error=%s", ids, type(err).__name__)
