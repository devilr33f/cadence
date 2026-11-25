"""Each device's entities: unique ids on every platform, and entities that follow the devices without a reload."""

from __future__ import annotations

import logging

import pytest
from homeassistant.core import HomeAssistant
from homeassistant.helpers import device_registry as dr
from homeassistant.helpers import entity_registry as er
from pytest_homeassistant_custom_component.common import MockConfigEntry

from custom_components.cadence.const import DOMAIN

from .conftest import entity_id
from .fake_backend import HALL, KITCHEN, FakeCadence, frame, wait_for
from .test_init import BEDROOM, assert_private, ha_device

KITCHEN_KEYS = {
    ("media_player", "speaker"),
    ("binary_sensor", "online"),
    ("sensor", "cpu"),
    ("sensor", "memory"),
    ("sensor", "disk"),
    ("sensor", "temperature"),
    ("sensor", "next_alarm"),
    ("sensor", "next_timer"),
    ("sensor", "next_reminder"),
    ("binary_sensor", "ringing"),
    ("event", "alarm"),
    ("button", "stop_ring"),
    ("button", "snooze"),
}
HALL_KEYS = {("binary_sensor", "online"), ("sensor", "cpu"), ("sensor", "memory"), ("sensor", "disk")}


def registered(hass: HomeAssistant, entry: MockConfigEntry) -> set[tuple[str, str, str]]:
    """(platform, device id, key) of each entity the registry holds for the entry."""
    out = set()
    for ent in er.async_entries_for_config_entry(er.async_get(hass), entry.entry_id):
        device_id, key = ent.unique_id.split(":")
        assert ent.platform == DOMAIN
        out.add((ent.domain, device_id, key))
    return out


async def test_every_entity_has_a_unique_id_and_its_device(hass: HomeAssistant, loaded: MockConfigEntry) -> None:
    # the server refuses a cadence entity to its agent by the registry's platform, which needs an entry per entity
    assert registered(hass, loaded) == {(p, KITCHEN, k) for p, k in KITCHEN_KEYS} | {(p, HALL, k) for p, k in HALL_KEYS}
    registry = er.async_get(hass)
    for ent in er.async_entries_for_config_entry(registry, loaded.entry_id):
        device = dr.async_get(hass).async_get(ent.device_id or "")
        assert device is not None
        assert (DOMAIN, ent.unique_id.split(":")[0]) in device.identifiers
        assert ent.has_entity_name
        assert hass.states.get(ent.entity_id) is not None


async def test_entities_follow_the_devices(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence, caplog: pytest.LogCaptureFixture
) -> None:
    caplog.set_level(logging.DEBUG, logger="custom_components.cadence")
    player = entity_id(hass, "media_player", KITCHEN, "speaker")
    online = entity_id(hass, "binary_sensor", KITCHEN, "online")
    assert player is not None
    assert online is not None

    # renamed: names follow, entity ids stay
    backend.rename_device(KITCHEN, "Living room")
    await backend.push(frame("device_renamed.json"))
    await wait_for(lambda: hass.states.get(player).attributes["friendly_name"] == "Living room")  # type: ignore[union-attr]
    assert hass.states.get(online).attributes["friendly_name"] == "Living room Online"  # type: ignore[union-attr]
    assert entity_id(hass, "media_player", KITCHEN, "speaker") == player

    # added: its entities come at once, its speaker once it links
    backend.add_device(BEDROOM, "Bedroom")
    await backend.push(frame("device_added.json", BEDROOM, name="Bedroom"))
    await wait_for(lambda: (("binary_sensor", BEDROOM, "online")) in registered(hass, loaded))
    assert {(p, k) for p, d, k in registered(hass, loaded) if d == BEDROOM} == HALL_KEYS
    assert ha_device(hass, loaded, BEDROOM) is not None
    bedroom_online = entity_id(hass, "binary_sensor", BEDROOM, "online")
    assert bedroom_online is not None
    await wait_for(lambda: hass.states.get(bedroom_online) is not None)
    assert hass.states.get(bedroom_online).attributes["friendly_name"] == "Bedroom Online"  # type: ignore[union-attr]

    # deleted: its entities and its home assistant device go
    hall = {entity_id(hass, p, HALL, k) for p, k in HALL_KEYS}
    backend.remove_device(HALL)
    await backend.push(frame("device_deleted.json"))
    await wait_for(lambda: not any(d == HALL for _, d, _ in registered(hass, loaded)))
    await wait_for(lambda: all(hass.states.get(e or "") is None for e in hall))
    assert ha_device(hass, loaded, HALL) is None
    assert {d for _, d, _ in registered(hass, loaded)} == {KITCHEN, BEDROOM}
    assert_private(caplog, backend)
