"""Setup, the coordinator's state, and how it follows the live feed: events, devices, reconnects, a revoked token."""

from __future__ import annotations

import asyncio
import logging
from unittest.mock import patch

import pytest
from homeassistant.config_entries import ConfigEntryState
from homeassistant.core import HomeAssistant
from homeassistant.helpers import device_registry as dr
from pytest_homeassistant_custom_component.common import MockConfigEntry

from custom_components.cadence import coordinator as cadence_coordinator
from custom_components.cadence.const import DOMAIN
from custom_components.cadence.feed import SUBSCRIBE

from .conftest import config_entry
from .fake_backend import HALL, KITCHEN, TOKEN, FakeCadence, UntrustedServer, dropped, fixture, frame, wait_for

BEDROOM = "9c1d2e3f-4a5b-4c6d-8e7f-0a1b2c3d4e5f"
STRANGER = "4b3a2c1d-0000-4000-8000-000000000001"
LISTS = "GET /api/v1/devices"


def ha_device(hass: HomeAssistant, entry: MockConfigEntry, device_id: str) -> dr.DeviceEntry | None:
    return dr.async_get(hass).async_get_device_by_identifier((DOMAIN, device_id), entry.entry_id)


def assert_private(caplog: pytest.LogCaptureFixture, backend: FakeCadence) -> None:
    """Rule 6, 8 and D11: no token, url, name or media metadata in any log line of the component."""
    assert TOKEN not in caplog.text
    assert "Night Drive" not in caplog.text
    for record in caplog.records:
        if record.name.startswith("custom_components.cadence"):
            line = record.getMessage()
            for private in (backend.url, "127.0.0.1", "Kitchen", "Hall", "Living room", "Bedroom"):
                assert private not in line, line


async def test_setup_reads_every_device_and_registers_it(
    hass: HomeAssistant, backend: FakeCadence, caplog: pytest.LogCaptureFixture
) -> None:
    caplog.set_level(logging.DEBUG, logger="custom_components.cadence")
    entry = config_entry(backend.url)
    entry.add_to_hass(hass)
    # deleted from the server while home assistant was away
    stale = dr.async_get(hass).async_get_or_create(
        config_entry_id=entry.entry_id, identifiers={(DOMAIN, "1f2e3d4c-0000-4000-8000-000000000000")}, name="Old"
    )
    assert await hass.config_entries.async_setup(entry.entry_id)
    coordinator = entry.runtime_data
    await wait_for(lambda: coordinator.connected)

    kitchen, hall = coordinator.data[KITCHEN], coordinator.data[HALL]
    assert (kitchen.name, kitchen.model, kitchen.online, kitchen.volume) == ("Kitchen", "sim", True, 35)
    assert kitchen.capabilities == {"alarms", "audio.output", "media.player", "media.url"}
    assert kitchen.media is not None
    assert kitchen.media.state == "playing"
    assert kitchen.vitals is not None
    assert kitchen.vitals.cpu == 0.125
    # no vitals until the device's next round: unknown, not unavailable
    assert (hall.name, hall.online, hall.volume, hall.media, hall.vitals) == ("Hall", False, None, None, None)
    assert coordinator.last_update_success

    device = ha_device(hass, entry, KITCHEN)
    assert device is not None
    assert (device.name, device.model, device.manufacturer) == ("Kitchen", "sim", "Cadence")
    assert ha_device(hass, entry, HALL) is not None
    assert dr.async_get(hass).async_get(stale.id) is None
    assert coordinator.device_info(KITCHEN)["identifiers"] == {(DOMAIN, KITCHEN)}

    # the first read, then the read after the feed subscribed; nothing polls after
    assert backend.subscribes == [SUBSCRIBE]
    assert backend.requests.count(LISTS) == 2
    await asyncio.sleep(0.1)
    assert backend.requests.count(LISTS) == 2
    assert_private(caplog, backend)


async def test_events_move_the_state(hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence) -> None:
    coordinator = loaded.runtime_data
    reads = len(backend.requests)
    updates: list[None] = []
    unsubscribe = coordinator.async_add_listener(lambda: updates.append(None))

    await backend.push(
        frame("device_volume.json", level=50),
        frame("media_state_idle.json"),
        frame("device_vitals.json", HALL),
        frame("device_offline.json"),
    )
    await wait_for(lambda: not coordinator.data[KITCHEN].online)
    kitchen, hall = coordinator.data[KITCHEN], coordinator.data[HALL]
    assert (kitchen.volume, kitchen.media) == (50, None)
    # presence carries the device's name now
    assert kitchen.name == "Living room"
    device = ha_device(hass, loaded, KITCHEN)
    assert device is not None
    assert device.name == "Living room"
    assert hall.vitals is not None
    assert hall.vitals.temperature_c == 48.5

    await backend.push(frame("media_state.json"), frame("device_online.json"))
    await wait_for(lambda: coordinator.data[KITCHEN].online)
    assert coordinator.data[KITCHEN].media is not None
    # pushed, never polled: the one read is the linked device's detail, for the capabilities it reports
    await wait_for(lambda: len(backend.requests) > reads)
    await asyncio.sleep(0.2)
    assert backend.requests[reads:] == [f"GET /api/v1/devices/{KITCHEN}"]
    assert len(updates) == 7
    unsubscribe()


async def test_devices_follow_the_server_without_a_reload(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence, caplog: pytest.LogCaptureFixture
) -> None:
    caplog.set_level(logging.DEBUG, logger="custom_components.cadence")
    coordinator = loaded.runtime_data
    added: list[list[str]] = []
    unsubscribe = coordinator.async_add_device_callback(added.append)
    assert [sorted(ids) for ids in added] == [sorted([KITCHEN, HALL])]

    backend.rename_device(KITCHEN, "Living room")
    await backend.push(frame("device_renamed.json"))
    await wait_for(lambda: coordinator.data[KITCHEN].name == "Living room")
    assert ha_device(hass, loaded, KITCHEN).name == "Living room"  # type: ignore[union-attr]

    # an event never makes a device: the list is read again, and the device comes from it
    lists = backend.requests.count(LISTS)
    backend.add_device(BEDROOM, "Bedroom")
    await backend.push(frame("device_added.json", BEDROOM, name="Bedroom"))
    await wait_for(lambda: BEDROOM in coordinator.data)
    assert added[-1] == [BEDROOM]
    assert ha_device(hass, loaded, BEDROOM).name == "Bedroom"  # type: ignore[union-attr]
    assert backend.requests.count(LISTS) == lists + 1

    backend.remove_device(HALL)
    await backend.push(frame("device_deleted.json"))
    await wait_for(lambda: HALL not in coordinator.data)
    assert ha_device(hass, loaded, HALL) is None

    # the deleted device's late online and held vitals: ignored, nothing read, nothing comes back
    lists = backend.requests.count(LISTS)
    await backend.push(
        frame("device_online.json", HALL), frame("device_vitals.json", HALL), frame("device_volume.json", level=20)
    )
    await wait_for(lambda: coordinator.data[KITCHEN].volume == 20)
    assert HALL not in coordinator.data
    assert ha_device(hass, loaded, HALL) is None
    assert backend.requests.count(LISTS) == lists

    # events of a device the server does not list: the list is read once for it, and nothing is made
    await backend.push(
        frame("device_online.json", STRANGER),
        frame("media_state.json", STRANGER),
        frame("device_volume.json", level=25),
    )
    await wait_for(lambda: coordinator.data[KITCHEN].volume == 25)
    assert STRANGER not in coordinator.data
    assert ha_device(hass, loaded, STRANGER) is None
    assert backend.requests.count(LISTS) == lists + 1
    assert added[-1] == [BEDROOM]
    unsubscribe()
    assert_private(caplog, backend)


async def test_list_reads_for_unknown_devices_are_rate_limited(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence
) -> None:
    coordinator = loaded.runtime_data
    with patch.object(cadence_coordinator, "LIST_SYNC_INTERVAL", 0.4):
        backend.add_device(BEDROOM, "Bedroom")
        await backend.push(frame("device_added.json", BEDROOM))
        await wait_for(lambda: BEDROOM in coordinator.data)
        first = backend.stamps(LISTS)[-1]
        await backend.push(frame("device_online.json", STRANGER))
        await backend.push(frame("device_volume.json", level=20))
        await wait_for(lambda: coordinator.data[KITCHEN].volume == 20)
        assert backend.stamps(LISTS)[-1] - first >= 0.35


async def test_reconnect_reads_the_state_again(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence, caplog: pytest.LogCaptureFixture
) -> None:
    caplog.set_level(logging.DEBUG, logger="custom_components.cadence")
    coordinator = loaded.runtime_data
    # the server restarts: the feed goes away and the reads fail a while
    backend.unavailable = True
    await backend.close_feeds(1001)
    await wait_for(lambda: not coordinator.connected)
    assert not coordinator.last_update_success
    # nothing is replayed: what changed meanwhile is only seen by reading it
    backend.media.pop(KITCHEN)
    backend.set_volume(KITCHEN, 10)
    backend.vitals[HALL] = fixture("vitals.json")
    backend.devices[HALL]["online"] = True
    await wait_for(lambda: backend.connects >= 3)
    assert not coordinator.connected
    backend.unavailable = False
    await wait_for(lambda: coordinator.connected)

    kitchen, hall = coordinator.data[KITCHEN], coordinator.data[HALL]
    assert (kitchen.media, kitchen.volume) == (None, 10)
    assert hall.online
    assert hall.vitals is not None
    assert coordinator.last_update_success
    assert_private(caplog, backend)


async def test_dropped_events_read_the_state_again(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence
) -> None:
    coordinator = loaded.runtime_data
    lists = backend.requests.count(LISTS)
    backend.media.pop(KITCHEN)
    backend.set_volume(KITCHEN, 70)
    await backend.push(dropped(5))
    await wait_for(lambda: coordinator.data[KITCHEN].volume == 70)
    assert coordinator.data[KITCHEN].media is None
    assert backend.requests.count(LISTS) == lists + 1
    assert backend.connects == 1


@pytest.mark.parametrize("revoke", ["close_1008", "refuse_401"])
async def test_a_revoked_token_asks_for_reauth_without_retrying(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence, caplog: pytest.LogCaptureFixture, revoke: str
) -> None:
    coordinator = loaded.runtime_data
    if revoke == "close_1008":
        await backend.close_feeds(1008)
    else:
        backend.refuse = 401
        await backend.close_feeds(1001)
    await wait_for(lambda: bool(hass.config_entries.flow.async_progress_by_handler(DOMAIN)))
    await hass.async_block_till_done()
    (flow,) = hass.config_entries.flow.async_progress_by_handler(DOMAIN)
    assert flow["context"]["source"] == "reauth"
    assert flow["context"]["entry_id"] == loaded.entry_id
    connects, reads = backend.connects, len(backend.requests)
    await asyncio.sleep(0.3)
    assert (backend.connects, len(backend.requests)) == (connects, reads)
    assert not coordinator.connected
    assert not coordinator.last_update_success
    assert TOKEN not in caplog.text


async def test_setup_with_a_refused_token_asks_for_reauth(hass: HomeAssistant, backend: FakeCadence) -> None:
    entry = config_entry(backend.url, token="cdn_wrong")
    entry.add_to_hass(hass)
    assert not await hass.config_entries.async_setup(entry.entry_id)
    await hass.async_block_till_done()
    assert entry.state is ConfigEntryState.SETUP_ERROR
    (flow,) = hass.config_entries.flow.async_progress_by_handler(DOMAIN)
    assert flow["context"]["source"] == "reauth"
    assert backend.connects == 0


async def test_setup_with_the_server_down_is_retried(hass: HomeAssistant, backend: FakeCadence) -> None:
    entry = config_entry(backend.url)
    await backend.close()
    entry.add_to_hass(hass)
    assert not await hass.config_entries.async_setup(entry.entry_id)
    assert entry.state is ConfigEntryState.SETUP_RETRY


async def test_unload_closes_the_feed(hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence) -> None:
    assert backend.feeds
    assert await hass.config_entries.async_unload(loaded.entry_id)
    await wait_for(lambda: not backend.feeds)
    assert loaded.state is ConfigEntryState.NOT_LOADED
    connects = backend.connects
    await asyncio.sleep(0.2)
    assert backend.connects == connects


def detail_reads(backend: FakeCadence, device_id: str) -> int:
    return backend.requests.count(f"GET /api/v1/devices/{device_id}")


async def test_a_new_device_gains_its_capabilities_when_it_links(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence
) -> None:
    coordinator = loaded.runtime_data
    added: list[list[str]] = []
    coordinator.async_add_device_callback(added.append)
    # enrolled: the server publishes device_added before the device ever linked, so it has no capabilities yet
    backend.add_device(BEDROOM, "Bedroom")
    await backend.push(frame("device_added.json", BEDROOM, name="Bedroom"))
    await wait_for(lambda: BEDROOM in coordinator.data)
    assert coordinator.data[BEDROOM].capabilities == frozenset()
    assert added[-1] == [BEDROOM]
    changes: list[str] = []
    coordinator.async_add_device_listener(BEDROOM, lambda: changes.append(BEDROOM))
    others: list[str] = []
    coordinator.async_add_device_listener(KITCHEN, lambda: others.append(KITCHEN))

    # it links and reports its capabilities a moment after its hello
    backend.link_device(BEDROOM)
    await backend.push(frame("device_online.json", BEDROOM, name="Bedroom"))
    await wait_for(lambda: bool(coordinator.data[BEDROOM].capabilities))
    bedroom = coordinator.data[BEDROOM]
    assert bedroom.capabilities == {"alarms", "audio.output", "media.player", "media.url"}
    assert bedroom.volume == 35
    assert bedroom.online
    assert changes == [BEDROOM]
    assert others == []
    # the same device, not a new one
    assert added[-1] == [BEDROOM]
    assert len(added) == 2
    assert detail_reads(backend, BEDROOM) == 2


async def test_an_event_that_implies_a_capability_reads_the_detail_once(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence
) -> None:
    coordinator = loaded.runtime_data
    reads = detail_reads(backend, HALL)
    changes: list[str] = []
    coordinator.async_add_device_listener(HALL, lambda: changes.append(HALL))
    # its online came before this home assistant knew it: the volume and the session are what tell
    backend.link_device(HALL)
    await backend.push(
        frame("device_volume.json", HALL, level=20),
        frame("media_state.json", HALL),
        frame("device_volume.json", HALL, level=25),
    )
    await wait_for(lambda: "audio.output" in coordinator.data[HALL].capabilities)
    await asyncio.sleep(0.2)
    hall = coordinator.data[HALL]
    assert hall.capabilities == {"alarms", "audio.output", "media.player", "media.url"}
    # the event's level is newer than the one read
    assert hall.volume == 25
    assert detail_reads(backend, HALL) == reads + 1
    assert changes == [HALL]

    # with the capabilities known, the same events read nothing
    await backend.push(frame("device_volume.json", HALL, level=30), frame("media_state.json", HALL))
    await wait_for(lambda: coordinator.data[HALL].volume == 30)
    await asyncio.sleep(0.2)
    assert detail_reads(backend, HALL) == reads + 1


async def test_a_full_read_tells_which_devices_changed(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence
) -> None:
    coordinator = loaded.runtime_data
    changes: list[str] = []
    for device_id in (KITCHEN, HALL):
        coordinator.async_add_device_listener(device_id, lambda device_id=device_id: changes.append(device_id))
    backend.link_device(HALL)
    lists = backend.requests.count(LISTS)
    await backend.push(dropped(2))
    await wait_for(lambda: backend.requests.count(LISTS) == lists + 1 and bool(coordinator.data[HALL].capabilities))
    assert changes == [HALL]


async def test_pending_devices_are_no_devices(hass: HomeAssistant, backend: FakeCadence) -> None:
    backend.add_device(BEDROOM, "Spare", status="pending")
    entry = config_entry(backend.url)
    entry.add_to_hass(hass)
    assert await hass.config_entries.async_setup(entry.entry_id)
    coordinator = entry.runtime_data
    await wait_for(lambda: coordinator.connected)
    assert set(coordinator.data) == {KITCHEN, HALL}
    assert ha_device(hass, entry, BEDROOM) is None


async def test_a_pending_device_renamed_before_it_enrols_appears_when_it_does(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence
) -> None:
    coordinator = loaded.runtime_data
    added: list[list[str]] = []
    coordinator.async_add_device_callback(added.append)
    # the server renames a pending device too: the list it reads for the event leaves it out
    backend.add_device(BEDROOM, "Spare", status="pending")
    backend.rename_device(BEDROOM, "Bedroom")
    lists = backend.requests.count(LISTS)
    await backend.push(frame("device_renamed.json", BEDROOM, name="Bedroom"))
    await wait_for(lambda: backend.requests.count(LISTS) == lists + 1)
    assert BEDROOM not in coordinator.data

    # an enrolment is always news: its device_added reads the list again
    backend.enrol_device(BEDROOM)
    await backend.push(frame("device_added.json", BEDROOM, name="Bedroom"))
    await wait_for(lambda: BEDROOM in coordinator.data)
    assert added[-1] == [BEDROOM]
    assert ha_device(hass, loaded, BEDROOM).name == "Bedroom"  # type: ignore[union-attr]
    assert backend.requests.count(LISTS) == lists + 2


async def test_another_routes_404_never_reads_as_a_deleted_device(hass: HomeAssistant, backend: FakeCadence) -> None:
    # an older server, or a proxy that does not forward the vitals route
    backend.vitals_route = False
    entry = config_entry(backend.url)
    entry.add_to_hass(hass)
    known = dr.async_get(hass).async_get_or_create(
        config_entry_id=entry.entry_id, identifiers={(DOMAIN, KITCHEN)}, name="Kitchen"
    )
    assert not await hass.config_entries.async_setup(entry.entry_id)
    assert entry.state is ConfigEntryState.SETUP_RETRY
    assert dr.async_get(hass).async_get(known.id) is not None


async def test_a_refresh_asked_for_elsewhere_reads_nothing(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence
) -> None:
    coordinator = loaded.runtime_data
    await backend.push(frame("device_volume.json", level=55))
    await wait_for(lambda: coordinator.data[KITCHEN].volume == 55)
    reads = len(backend.requests)
    # what homeassistant.update_entity does: it neither reads beside the feed nor undoes the event
    await coordinator.async_refresh()
    assert len(backend.requests) == reads
    assert coordinator.data[KITCHEN].volume == 55
    assert coordinator.last_update_success

    # while the feed is down, a refresh leaves the entities unavailable
    backend.unavailable = True
    await backend.close_feeds(1001)
    await wait_for(lambda: not coordinator.connected)
    await coordinator.async_refresh()
    assert not coordinator.last_update_success


async def test_setup_verifies_tls(hass: HomeAssistant, rogue_backend: UntrustedServer) -> None:
    entry = config_entry(rogue_backend.url)
    entry.add_to_hass(hass)
    assert not await hass.config_entries.async_setup(entry.entry_id)
    assert entry.state is ConfigEntryState.SETUP_RETRY
    assert rogue_backend.handshakes >= 1
    assert rogue_backend.requests == []


async def test_a_failing_listener_leaves_the_others_and_the_feed(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence, caplog: pytest.LogCaptureFixture
) -> None:
    caplog.set_level(logging.DEBUG, logger="custom_components.cadence")
    coordinator = loaded.runtime_data
    calls: list[str] = []

    def failing() -> None:
        calls.append("failing")
        raise RuntimeError("Living room")

    def adding(ids: list[str]) -> None:
        if BEDROOM in ids:
            raise RuntimeError("Bedroom")

    coordinator.async_add_device_listener(KITCHEN, failing)
    coordinator.async_add_device_listener(KITCHEN, lambda: calls.append("other"))
    coordinator.async_add_device_callback(adding)
    added: list[list[str]] = []
    coordinator.async_add_device_callback(added.append)

    backend.rename_device(KITCHEN, "Living room")
    await backend.push(frame("device_renamed.json"))
    await wait_for(lambda: "other" in calls)
    backend.add_device(BEDROOM, "Bedroom")
    await backend.push(frame("device_added.json", BEDROOM, name="Bedroom"))
    await wait_for(lambda: added[-1] == [BEDROOM])
    await backend.push(frame("device_volume.json", level=15))
    await wait_for(lambda: coordinator.data[KITCHEN].volume == 15)

    assert calls == ["failing", "other"]
    assert backend.connects == 1
    assert coordinator.connected
    assert caplog.text.count("cadence_listener_failed") == 2
    assert "RuntimeError" in caplog.text
    assert_private(caplog, backend)


async def test_a_deleted_devices_pending_detail_read_is_dropped(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence
) -> None:
    coordinator = loaded.runtime_data
    reads = detail_reads(backend, HALL)
    with patch.object(cadence_coordinator, "DETAIL_DEBOUNCE", 0.3):
        await backend.push(frame("device_online.json", HALL, name="Hall"))
        await wait_for(lambda: coordinator.data[HALL].online)
        backend.remove_device(HALL)
        await backend.push(frame("device_deleted.json"))
        await wait_for(lambda: HALL not in coordinator.data)
        await asyncio.sleep(0.5)
    assert detail_reads(backend, HALL) == reads
