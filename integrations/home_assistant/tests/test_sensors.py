"""The online sensor follows presence; the vitals sensors follow each metrics round."""

from __future__ import annotations

from homeassistant.core import HomeAssistant
from homeassistant.helpers import entity_registry as er
from pytest_homeassistant_custom_component.common import MockConfigEntry

from .conftest import entity_id
from .fake_backend import HALL, KITCHEN, FakeCadence, frame, wait_for


def sensor(hass: HomeAssistant, device_id: str, key: str, platform: str = "sensor") -> str:
    found = entity_id(hass, platform, device_id, key)
    assert found is not None
    return found


def state_of(hass: HomeAssistant, entity: str) -> str:
    state = hass.states.get(entity)
    assert state is not None
    return state.state


async def test_online_follows_presence(hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence) -> None:
    kitchen, hall = sensor(hass, KITCHEN, "online", "binary_sensor"), sensor(hass, HALL, "online", "binary_sensor")
    assert (state_of(hass, kitchen), state_of(hass, hall)) == ("on", "off")
    state = hass.states.get(kitchen)
    assert state is not None
    assert state.attributes["device_class"] == "connectivity"
    assert state.attributes["friendly_name"] == "Kitchen Online"
    entry = er.async_get(hass).async_get(kitchen)
    assert entry is not None
    assert entry.entity_category == "diagnostic"

    await backend.push(frame("device_offline.json"))
    await wait_for(lambda: state_of(hass, kitchen) == "off")
    await backend.push(frame("device_online.json", HALL, name="Hall"))
    await wait_for(lambda: state_of(hass, hall) == "on")

    # unknown while the feed is down, not offline
    backend.unavailable = True
    await backend.close_feeds(1001)
    await wait_for(lambda: state_of(hass, hall) == "unavailable")
    backend.unavailable = False
    await wait_for(lambda: state_of(hass, hall) == "off")


async def test_vitals_sensors(hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence) -> None:
    cpu, memory, disk = (sensor(hass, KITCHEN, key) for key in ("cpu", "memory", "disk"))
    temperature = sensor(hass, KITCHEN, "temperature")
    # 12.5 % cpu, 412 of 1024 MB, /data the largest disk with 2 of 8 GB used, 48.5 °C
    assert [state_of(hass, e) for e in (cpu, memory, disk, temperature)] == ["12.5", "40.2", "25.0", "48.5"]
    attrs = hass.states.get(temperature).attributes  # type: ignore[union-attr]
    assert (attrs["device_class"], attrs["unit_of_measurement"], attrs["state_class"]) == (
        "temperature",
        "°C",
        "measurement",
    )
    assert hass.states.get(cpu).attributes["unit_of_measurement"] == "%"  # type: ignore[union-attr]
    for entity in (cpu, memory, disk, temperature):
        entry = er.async_get(hass).async_get(entity)
        assert entry is not None
        assert entry.entity_category == "diagnostic"

    disks = [
        {"mountpoint": "/tmp", "freeBytes": 1, "limitBytes": 10},
        {"mountpoint": "/var/lib/cadence", "freeBytes": 75, "limitBytes": 100},
    ]
    await backend.push(
        frame("device_vitals.json", cpu=0.5, memoryUsedBytes=1, memoryLimitBytes=0, disk=disks, temperatureC=None)
    )
    await wait_for(lambda: state_of(hass, cpu) == "50.0")
    # no memory limit and no temperature in this round: unknown
    assert [state_of(hass, e) for e in (memory, disk, temperature)] == ["unknown", "25.0", "unknown"]

    await backend.push(frame("device_vitals.json", disk=[]))
    await wait_for(lambda: state_of(hass, temperature) == "48.5")
    assert state_of(hass, disk) == "unknown"

    # an offline device's vitals are stale
    await backend.push(frame("device_offline.json"))
    await wait_for(lambda: state_of(hass, cpu) == "unavailable")
    assert state_of(hass, temperature) == "unavailable"


async def test_temperature_appears_once_reported(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence
) -> None:
    # hall linked, but has sent no metrics round since the server started
    await backend.push(frame("device_online.json", HALL, name="Hall"))
    cpu = sensor(hass, HALL, "cpu")
    await wait_for(lambda: state_of(hass, cpu) == "unknown")
    assert entity_id(hass, "sensor", HALL, "temperature") is None

    await backend.push(frame("device_vitals.json", HALL, temperatureC=None))
    await wait_for(lambda: state_of(hass, cpu) == "12.5")
    assert entity_id(hass, "sensor", HALL, "temperature") is None

    await backend.push(frame("device_vitals.json", HALL, temperatureC=51.0))
    await wait_for(lambda: entity_id(hass, "sensor", HALL, "temperature") is not None)
    temperature = sensor(hass, HALL, "temperature")
    await wait_for(lambda: hass.states.get(temperature) is not None and state_of(hass, temperature) == "51.0")
