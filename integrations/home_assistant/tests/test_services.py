"""cadence.say: a text spoken on the devices it names, refusals that say why, nothing of the text logged."""

from __future__ import annotations

import logging
from typing import Any

import probatio
import pytest
from homeassistant.core import HomeAssistant
from homeassistant.exceptions import HomeAssistantError, ServiceValidationError
from homeassistant.helpers import device_registry as dr
from pytest_homeassistant_custom_component.common import MockConfigEntry

from custom_components.cadence.const import DOMAIN

from .conftest import entity_id
from .fake_backend import HALL, KITCHEN, FakeCadence
from .test_init import assert_private, ha_device

TEXT = "Dinner is ready"


def device_of(hass: HomeAssistant, entry: MockConfigEntry, device_id: str) -> str:
    device = ha_device(hass, entry, device_id)
    assert device is not None
    return device.id


async def say(hass: HomeAssistant, **data: Any) -> None:
    await hass.services.async_call(DOMAIN, "say", data, blocking=True)


async def test_say_on_a_device(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence, caplog: pytest.LogCaptureFixture
) -> None:
    caplog.set_level(logging.DEBUG, logger="custom_components.cadence")
    await say(hass, device_id=device_of(hass, loaded, KITCHEN), text=f"  {TEXT} ", language="en")
    assert backend.announces == [(KITCHEN, {"text": TEXT, "language": "en"})]
    for record in caplog.records:
        if record.name.startswith("custom_components.cadence"):
            assert TEXT not in record.getMessage()
    assert_private(caplog, backend)


async def test_say_on_an_entity_or_several_devices(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence
) -> None:
    player = entity_id(hass, "media_player", KITCHEN, "speaker")
    await say(hass, entity_id=player, text="Привет")
    assert backend.announces == [(KITCHEN, {"text": "Привет"})]

    # the same device named twice speaks once; each device named speaks
    backend.announces.clear()
    kitchen, hall = device_of(hass, loaded, KITCHEN), device_of(hass, loaded, HALL)
    await say(hass, device_id=[kitchen, hall], entity_id=[player], text=TEXT, language="ru")
    assert sorted(backend.announces) == sorted(
        [(HALL, {"text": TEXT, "language": "ru"}), (KITCHEN, {"text": TEXT, "language": "ru"})]
    )


async def test_say_refuses_an_unknown_target(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence
) -> None:
    other = MockConfigEntry(domain="other")
    other.add_to_hass(hass)
    stranger = dr.async_get(hass).async_get_or_create(config_entry_id=other.entry_id, identifiers={("other", "x")})
    for target, data in (
        ("nothing", {}),
        ("missing", {"device_id": "0123456789abcdef0123456789abcdef"}),
        ("stranger", {"device_id": stranger.id}),
        ("other entity", {"entity_id": "media_player.somebody_elses"}),
    ):
        with pytest.raises(ServiceValidationError) as err:
            await say(hass, text=TEXT, **data)
        assert err.value.translation_key == ("no_target" if target == "nothing" else "unknown_target"), target
    assert backend.announces == []


async def test_say_refuses_a_device_of_an_unloaded_entry(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence
) -> None:
    kitchen = device_of(hass, loaded, KITCHEN)
    player = entity_id(hass, "media_player", KITCHEN, "speaker")
    assert await hass.config_entries.async_unload(loaded.entry_id)
    for data in ({"device_id": kitchen}, {"entity_id": player}):
        with pytest.raises(ServiceValidationError) as err:
            await say(hass, text=TEXT, **data)
        assert err.value.translation_key == "not_loaded"
    assert backend.announces == []


@pytest.mark.parametrize(
    ("status", "code"),
    [
        (409, "device_offline"),
        (409, "no_audio"),
        (409, "busy"),
        (409, "tts_unconfigured"),
        (409, "superseded"),
        (502, "tts_failed"),
        (504, "tts_timeout"),
        (422, "announce_invalid"),
        (403, "forbidden"),
        (404, "not_found"),
    ],
)
async def test_say_maps_each_server_code(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence, status: int, code: str
) -> None:
    backend.announce_answer = (status, {"error": code})
    with pytest.raises(HomeAssistantError) as err:
        await say(hass, device_id=device_of(hass, loaded, KITCHEN), text=TEXT)
    assert not isinstance(err.value, ServiceValidationError)
    assert (err.value.translation_domain, err.value.translation_key) == (DOMAIN, code)
    # the speaker by its name in home assistant
    assert err.value.translation_placeholders == {"target": "Kitchen"}


async def test_say_names_an_unknown_code_or_a_lost_server(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence
) -> None:
    kitchen = device_of(hass, loaded, KITCHEN)
    backend.announce_answer = (409, {"error": "brand_new"})
    with pytest.raises(HomeAssistantError) as err:
        await say(hass, device_id=kitchen, text=TEXT)
    assert (err.value.translation_key, err.value.translation_placeholders) == (
        "request_failed",
        {"target": "Kitchen", "code": "brand_new"},
    )

    await backend.close()
    with pytest.raises(HomeAssistantError) as err:
        await say(hass, device_id=kitchen, text=TEXT)
    assert (err.value.translation_key, err.value.translation_placeholders) == ("cannot_connect", {"code": "connect"})


@pytest.mark.parametrize(
    "data",
    [
        {"text": ""},
        {"text": "   "},
        {"text": "x" * 501},
        {"text": TEXT, "language": "de"},
        {"text": TEXT, "volume": 100},
    ],
)
async def test_say_validates_its_fields(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence, data: dict[str, Any]
) -> None:
    with pytest.raises(probatio.Invalid):
        await say(hass, device_id=device_of(hass, loaded, KITCHEN), **data)
    assert backend.announces == []


async def test_say_takes_500_characters(hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence) -> None:
    await say(hass, device_id=device_of(hass, loaded, KITCHEN), text="ы" * 500)
    assert backend.announces == [(KITCHEN, {"text": "ы" * 500})]


async def test_say_names_the_speaker_that_refused(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence
) -> None:
    hall = device_of(hass, loaded, HALL)
    dr.async_get(hass).async_update_device(hall, name_by_user="Hallway")
    backend.announce_answers[HALL] = (409, {"error": "device_offline"})
    with pytest.raises(HomeAssistantError) as err:
        await say(hass, device_id=[device_of(hass, loaded, KITCHEN), hall], text=TEXT)
    assert (err.value.translation_key, err.value.translation_placeholders) == ("device_offline", {"target": "Hallway"})
    # the other speaker was asked all the same
    assert sorted(device for device, _ in backend.announces) == sorted([KITCHEN, HALL])
