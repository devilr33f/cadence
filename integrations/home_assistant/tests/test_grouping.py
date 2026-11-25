"""Multiroom grouping: a speaker's group as Home Assistant's group_members, its join and unjoin, and the feed's
group events."""

from __future__ import annotations

import logging
from typing import Any

import pytest
from homeassistant.components.media_player import MediaPlayerEntityFeature
from homeassistant.core import HomeAssistant
from homeassistant.exceptions import HomeAssistantError, ServiceValidationError
from homeassistant.helpers import entity_registry as er
from pytest_homeassistant_custom_component.common import MockConfigEntry

from custom_components.cadence.models import Device, DeviceGroup

from .conftest import config_entry, entity_id
from .fake_backend import HALL, KITCHEN, FakeCadence, fixture, frame, wait_for
from .test_init import assert_private

GROUP = fixture("group_joined.json")["id"]


def group_field(leader: str = KITCHEN) -> dict[str, Any]:
    """The device list's group field of the fixtures' group, with this leader."""
    return {**fixture("devices_grouped.json")[0]["group"], "leaderId": leader}


def speaker(hass: HomeAssistant, device_id: str) -> str:
    found = entity_id(hass, "media_player", device_id, "speaker")
    assert found is not None
    return found


def members(hass: HomeAssistant, player: str) -> Any:
    state = hass.states.get(player)
    return state.attributes.get("group_members") if state is not None else None


async def setup(hass: HomeAssistant, backend: FakeCadence) -> MockConfigEntry:
    entry = config_entry(backend.url)
    entry.add_to_hass(hass)
    assert await hass.config_entries.async_setup(entry.entry_id)
    await hass.async_block_till_done()
    await wait_for(lambda: entry.runtime_data.connected)
    return entry


@pytest.fixture
async def speakers(hass: HomeAssistant, backend: FakeCadence) -> MockConfigEntry:
    """Kitchen and Hall, both speakers, in no group."""
    backend.link_device(HALL)
    return await setup(hass, backend)


@pytest.fixture
async def grouped(hass: HomeAssistant, backend: FakeCadence) -> MockConfigEntry:
    """Kitchen and Hall in the fixtures' group, Kitchen its leader."""
    backend.link_device(HALL)
    backend.set_group(group_field(), KITCHEN, HALL)
    return await setup(hass, backend)


def test_the_models_read_the_group_fields() -> None:
    listed = [Device.from_json(d) for d in fixture("devices_grouped.json")]
    assert listed[0].group == DeviceGroup(
        id=GROUP, name="Kitchen", kind="group", leader_id=KITCHEN, members=(KITCHEN, HALL)
    )
    assert listed[1].group == listed[0].group
    assert DeviceGroup.from_json(fixture("group_changed.json")["data"]) == listed[0].group
    # a deleted group is none
    assert DeviceGroup.from_json(fixture("group_deleted.json")["data"]) is None
    for name in ("group_joined.json", "group.json"):
        assert DeviceGroup.from_view(fixture(name)) == listed[0].group
    assert DeviceGroup.from_view(fixture("groups.json")[0]) == listed[0].group
    # a device the server lists without one
    assert Device.from_json(fixture("devices.json")[0]).group is None


async def test_a_grouped_speaker_lists_its_group_leader_first(
    hass: HomeAssistant, grouped: MockConfigEntry, backend: FakeCadence
) -> None:
    kitchen, hall = speaker(hass, KITCHEN), speaker(hass, HALL)
    assert hass.states.get(kitchen).attributes["supported_features"] & MediaPlayerEntityFeature.GROUPING
    assert members(hass, kitchen) == [kitchen, hall]
    assert members(hass, hall) == [kitchen, hall]

    # hall elected: it comes first on both
    backend.set_group(group_field(HALL), KITCHEN, HALL)
    await backend.push(frame("group_changed.json", leaderId=HALL))
    await wait_for(lambda: members(hass, kitchen) == [hall, kitchen])
    assert members(hass, hall) == [hall, kitchen]


async def test_join_asks_the_server_to_group_the_other_speakers(
    hass: HomeAssistant, speakers: MockConfigEntry, backend: FakeCadence, caplog: pytest.LogCaptureFixture
) -> None:
    caplog.set_level(logging.DEBUG, logger="custom_components.cadence")
    kitchen, hall = speaker(hass, KITCHEN), speaker(hass, HALL)
    assert members(hass, kitchen) == []

    await hass.services.async_call(
        "media_player", "join", {"entity_id": kitchen, "group_members": [kitchen, hall]}, blocking=True
    )

    # the leading device is the route's own; it never lists itself
    assert backend.joins == [(KITCHEN, {"members": [HALL]})]
    # the answer is applied before the feed says so
    await wait_for(lambda: members(hass, kitchen) == [kitchen, hall])
    assert members(hass, hall) == [kitchen, hall]
    assert_private(caplog, backend)


async def test_join_refuses_what_is_no_cadence_speaker(
    hass: HomeAssistant, speakers: MockConfigEntry, backend: FakeCadence
) -> None:
    kitchen, hall = speaker(hass, KITCHEN), speaker(hass, HALL)
    other = er.async_get(hass).async_get_or_create("media_player", "demo", "living-room").entity_id
    online = entity_id(hass, "binary_sensor", KITCHEN, "online")
    for stranger in (other, "media_player.not_registered", online):
        with pytest.raises(ServiceValidationError) as err:
            await hass.services.async_call(
                "media_player", "join", {"entity_id": kitchen, "group_members": [hall, stranger]}, blocking=True
            )
        assert err.value.translation_key == "not_a_group_member"
        assert err.value.translation_placeholders["entity"] == stranger
    assert backend.joins == []


async def test_a_refused_stereo_pair_extension_surfaces_its_error(
    hass: HomeAssistant, speakers: MockConfigEntry, backend: FakeCadence, caplog: pytest.LogCaptureFixture
) -> None:
    caplog.set_level(logging.DEBUG, logger="custom_components.cadence")
    kitchen, hall = speaker(hass, KITCHEN), speaker(hass, HALL)
    backend.join_answer = (409, {"error": "stereo_pair"})

    with pytest.raises(HomeAssistantError) as err:
        await hass.services.async_call(
            "media_player", "join", {"entity_id": kitchen, "group_members": [hall]}, blocking=True
        )

    assert err.value.translation_key == "stereo_pair"
    assert err.value.translation_placeholders == {"target": "Kitchen"}
    assert members(hass, kitchen) == []
    assert_private(caplog, backend)


async def test_unjoin_takes_the_speaker_out_of_its_group(
    hass: HomeAssistant, grouped: MockConfigEntry, backend: FakeCadence, caplog: pytest.LogCaptureFixture
) -> None:
    caplog.set_level(logging.DEBUG, logger="custom_components.cadence")
    kitchen, hall = speaker(hass, KITCHEN), speaker(hass, HALL)

    await hass.services.async_call("media_player", "unjoin", {"entity_id": hall}, blocking=True)

    assert backend.leaves == [HALL]
    await wait_for(lambda: members(hass, hall) == [])
    # the group the server is left with is its feed's to say
    backend.set_group(None, KITCHEN, HALL)
    await backend.push(fixture("group_deleted.json"))
    await wait_for(lambda: members(hass, kitchen) == [])
    assert_private(caplog, backend)


async def test_group_changed_refreshes_the_devices_it_names(
    hass: HomeAssistant, speakers: MockConfigEntry, backend: FakeCadence, caplog: pytest.LogCaptureFixture
) -> None:
    caplog.set_level(logging.DEBUG, logger="custom_components.cadence")
    kitchen, hall = speaker(hass, KITCHEN), speaker(hass, HALL)
    reads = backend.requests.count(f"GET /api/v1/devices/{HALL}")

    # made in the dashboard: the feed alone tells
    backend.set_group(group_field(), KITCHEN, HALL)
    await backend.push(fixture("group_changed.json"))
    await wait_for(lambda: members(hass, kitchen) == [kitchen, hall])
    assert members(hass, hall) == [kitchen, hall]
    # each member's detail is read again
    await wait_for(lambda: backend.requests.count(f"GET /api/v1/devices/{HALL}") > reads)

    # a status report changes nothing of the membership
    await backend.push(fixture("group_state.json"))
    # hall left: the group's next change no longer names it, and hall drops it
    backend.set_group(None, HALL)
    smaller = {**group_field(), "members": [KITCHEN]}
    backend.set_group(smaller, KITCHEN)
    await backend.push(frame("group_changed.json", members=[KITCHEN]))
    await wait_for(lambda: members(hass, hall) == [])
    assert members(hass, kitchen) == [kitchen]

    backend.set_group(None, KITCHEN)
    await backend.push(fixture("group_deleted.json"))
    await wait_for(lambda: members(hass, kitchen) == [])
    assert_private(caplog, backend)
