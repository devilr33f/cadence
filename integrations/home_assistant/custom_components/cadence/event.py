"""Each device's alarm event: a ring started, and how it ended (stopped, snoozed or missed), from the live feed."""

from __future__ import annotations

from homeassistant.components.event import EventEntity
from homeassistant.core import HomeAssistant, callback
from homeassistant.helpers.entity_platform import AddConfigEntryEntitiesCallback

from .alarm_entity import CadenceAlarmEntity, has_alarms
from .coordinator import RING_OUTCOMES, RING_STARTED, CadenceConfigEntry, CadenceCoordinator
from .entity import follow_devices

KEY = "alarm"


async def async_setup_entry(
    hass: HomeAssistant, entry: CadenceConfigEntry, async_add_entities: AddConfigEntryEntitiesCallback
) -> None:
    follow_devices(
        entry,
        async_add_entities,
        lambda dev: [KEY] if has_alarms(dev) else [],
        lambda coordinator, device_id, _: CadenceAlarmEvent(coordinator, device_id),
    )


class CadenceAlarmEvent(CadenceAlarmEntity, EventEntity):
    """Fires with each ring of the device: its kind and id, a reminder's label, a snooze's end. A ring while the feed
    was down is not replayed: the server keeps none."""

    _attr_translation_key = KEY

    def __init__(self, coordinator: CadenceCoordinator, device_id: str) -> None:
        super().__init__(coordinator, device_id, KEY)
        self._attr_event_types = [RING_STARTED, *sorted(RING_OUTCOMES)]

    async def async_added_to_hass(self) -> None:
        await super().async_added_to_hass()
        self.async_on_remove(self.coordinator.async_add_alarm_listener(self.device_id, self._ring))

    @callback
    def _ring(self, event_type: str, attrs: dict[str, str]) -> None:
        self._trigger_event(event_type, attrs)
        self.async_write_ha_state()
