"""Each device's vitals from its latest metrics round: cpu, memory and disk in use, and temperature once reported;
and its next alarm, timer and reminder (alarm_sensor.py)."""

from __future__ import annotations

from collections.abc import Callable
from dataclasses import dataclass

from homeassistant.components.sensor import (
    SensorDeviceClass,
    SensorEntity,
    SensorEntityDescription,
    SensorStateClass,
)
from homeassistant.const import PERCENTAGE, EntityCategory, UnitOfTemperature
from homeassistant.core import HomeAssistant
from homeassistant.helpers.entity_platform import AddConfigEntryEntitiesCallback

from . import alarm_sensor
from .coordinator import CadenceConfigEntry, CadenceCoordinator
from .entity import CadenceEntity, follow_devices
from .models import CadenceDevice, Vitals

TEMPERATURE = "temperature"


@dataclass(frozen=True, kw_only=True)
class VitalsDescription(SensorEntityDescription):
    value: Callable[[Vitals], float | None]


def _percent(part: float, whole: float) -> float | None:
    return round(part / whole * 100, 1) if whole > 0 else None


def _disk(vitals: Vitals) -> float | None:
    # the largest mount holds the device's data
    if not vitals.disks:
        return None
    disk = max(vitals.disks, key=lambda d: d.limit_bytes)
    return _percent(disk.limit_bytes - disk.free_bytes, disk.limit_bytes)


DESCRIPTIONS = {
    d.key: d
    for d in (
        VitalsDescription(
            key="cpu",
            translation_key="cpu",
            native_unit_of_measurement=PERCENTAGE,
            state_class=SensorStateClass.MEASUREMENT,
            entity_category=EntityCategory.DIAGNOSTIC,
            suggested_display_precision=0,
            value=lambda v: round(v.cpu * 100, 1),
        ),
        VitalsDescription(
            key="memory",
            translation_key="memory",
            native_unit_of_measurement=PERCENTAGE,
            state_class=SensorStateClass.MEASUREMENT,
            entity_category=EntityCategory.DIAGNOSTIC,
            suggested_display_precision=0,
            value=lambda v: _percent(v.memory_used_bytes, v.memory_limit_bytes),
        ),
        VitalsDescription(
            key="disk",
            translation_key="disk",
            native_unit_of_measurement=PERCENTAGE,
            state_class=SensorStateClass.MEASUREMENT,
            entity_category=EntityCategory.DIAGNOSTIC,
            suggested_display_precision=0,
            value=_disk,
        ),
        VitalsDescription(
            key=TEMPERATURE,
            translation_key=TEMPERATURE,
            device_class=SensorDeviceClass.TEMPERATURE,
            native_unit_of_measurement=UnitOfTemperature.CELSIUS,
            state_class=SensorStateClass.MEASUREMENT,
            entity_category=EntityCategory.DIAGNOSTIC,
            suggested_display_precision=1,
            value=lambda v: v.temperature_c,
        ),
    )
}


async def async_setup_entry(
    hass: HomeAssistant, entry: CadenceConfigEntry, async_add_entities: AddConfigEntryEntitiesCallback
) -> None:
    # the temperature sensor comes with the first round that reports one: read at every update
    follow_devices(
        entry,
        async_add_entities,
        _keys,
        lambda coordinator, device_id, key: CadenceVitals(coordinator, device_id, DESCRIPTIONS[key]),
        on_state=True,
    )
    # the next reminder comes once the device lists reminders: read at every update too
    follow_devices(
        entry,
        async_add_entities,
        alarm_sensor.alarm_keys,
        lambda coordinator, device_id, key: alarm_sensor.CadenceNext(
            coordinator, device_id, alarm_sensor.DESCRIPTIONS[key]
        ),
        on_state=True,
    )


def _keys(dev: CadenceDevice) -> list[str]:
    keys = ["cpu", "memory", "disk"]
    if dev.vitals is not None and dev.vitals.temperature_c is not None:
        keys.append(TEMPERATURE)
    return keys


class CadenceVitals(CadenceEntity, SensorEntity):
    """One vital of a device: unknown until its next metrics round (the server keeps none across a restart)."""

    entity_description: VitalsDescription
    _needs_link = True

    def __init__(self, coordinator: CadenceCoordinator, device_id: str, description: VitalsDescription) -> None:
        super().__init__(coordinator, device_id, description.key)
        self.entity_description = description

    @property
    def native_value(self) -> float | None:
        dev = self.device
        if dev is None or dev.vitals is None:
            return None
        return self.entity_description.value(dev.vitals)
