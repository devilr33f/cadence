"""Each device's online sensor: whether it holds a link to the server; and with alarms, whether one rings now."""

from __future__ import annotations

from typing import Any

from homeassistant.components.binary_sensor import BinarySensorDeviceClass, BinarySensorEntity
from homeassistant.const import EntityCategory
from homeassistant.core import HomeAssistant
from homeassistant.helpers.entity_platform import AddConfigEntryEntitiesCallback

from .alarm_entity import ATTR_LABEL, CadenceAlarmEntity, has_alarms
from .coordinator import CadenceConfigEntry, CadenceCoordinator
from .entity import CadenceEntity, follow_devices

KEY = "online"
RINGING = "ringing"


async def async_setup_entry(
    hass: HomeAssistant, entry: CadenceConfigEntry, async_add_entities: AddConfigEntryEntitiesCallback
) -> None:
    follow_devices(
        entry,
        async_add_entities,
        lambda _: [KEY],
        lambda coordinator, device_id, _: CadenceOnline(coordinator, device_id),
    )
    follow_devices(
        entry,
        async_add_entities,
        lambda dev: [RINGING] if has_alarms(dev) else [],
        lambda coordinator, device_id, _: CadenceRinging(coordinator, device_id),
    )


class CadenceOnline(CadenceEntity, BinarySensorEntity):
    _attr_device_class = BinarySensorDeviceClass.CONNECTIVITY
    _attr_entity_category = EntityCategory.DIAGNOSTIC
    _attr_translation_key = KEY

    def __init__(self, coordinator: CadenceCoordinator, device_id: str) -> None:
        super().__init__(coordinator, device_id, KEY)

    @property
    def is_on(self) -> bool | None:
        dev = self.device
        return dev.online if dev is not None else None


class CadenceRinging(CadenceAlarmEntity, BinarySensorEntity):
    """On while an alarm, timer or reminder rings on the device: which one in its attributes."""

    _attr_translation_key = RINGING

    def __init__(self, coordinator: CadenceCoordinator, device_id: str) -> None:
        super().__init__(coordinator, device_id, RINGING)

    @property
    def is_on(self) -> bool | None:
        state = self._alarms
        return state.ringing is not None if state is not None else None

    @property
    def extra_state_attributes(self) -> dict[str, Any] | None:
        state = self._alarms
        if state is None or (ring := state.ringing) is None:
            return None
        out: dict[str, Any] = {"kind": ring.kind, "id": ring.id}
        if ring.kind == "reminder":
            item = state.item(ring.kind, ring.id)
            if label := ring.label or (item.label if item is not None else ""):
                out[ATTR_LABEL] = label
        return out
