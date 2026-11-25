"""Alarms: the state parsed leniently, the next alarm, timer and reminder sensors, the ringing sensor, the alarm event,
the stop and snooze buttons, and the set_timer and set_reminder actions. Nothing sets an alarm (rule 7)."""

from __future__ import annotations

import logging
import re
from typing import Any

import probatio
import pytest
from homeassistant.core import HomeAssistant
from homeassistant.exceptions import HomeAssistantError, ServiceValidationError
from pytest_homeassistant_custom_component.common import MockConfigEntry

from custom_components.cadence.alarm_entity import CadenceAlarmEntity
from custom_components.cadence.const import DOMAIN
from custom_components.cadence.models import AlarmsState, DeviceDetail

from .conftest import config_entry, entity_id
from .fake_backend import HALL, KITCHEN, FakeCadence, fixture, frame, wait_for
from .test_init import assert_private, ha_device

LABEL = "Take the bins out"
ID = re.compile(r"[a-z0-9]{1,16}")


def state_data() -> dict[str, Any]:
    return fixture("alarms_state.json")["data"]


def entity(hass: HomeAssistant, platform: str, key: str, device_id: str = KITCHEN) -> str:
    found = entity_id(hass, platform, device_id, key)
    assert found is not None, (platform, key)
    return found


def state(hass: HomeAssistant, entity_id_: str) -> Any:
    current = hass.states.get(entity_id_)
    assert current is not None
    return current


async def load(hass: HomeAssistant, backend: FakeCadence) -> MockConfigEntry:
    entry = config_entry(backend.url)
    entry.add_to_hass(hass)
    assert await hass.config_entries.async_setup(entry.entry_id)
    await hass.async_block_till_done()
    await wait_for(lambda: entry.runtime_data.connected)
    return entry


def device_of(hass: HomeAssistant, entry: MockConfigEntry, device_id: str = KITCHEN) -> str:
    device = ha_device(hass, entry, device_id)
    assert device is not None
    return device.id


# parsing


def test_the_state_parses_every_kind() -> None:
    parsed = AlarmsState.from_json(state_data())
    assert parsed.has_reminders
    assert parsed.ringing is None
    alarm, timer, reminder = (
        parsed.item(kind, i) for kind, i in (("alarm", "a1"), ("timer", "t1"), ("reminder", "r1"))
    )
    assert alarm is not None
    assert (alarm.time, alarm.days, alarm.next.isoformat()) == (  # type: ignore[union-attr]
        "07:30",
        ("mon", "tue", "wed", "thu", "fri"),
        "2026-11-13T07:30:00+00:00",
    )
    assert timer is not None
    assert (timer.seconds, timer.ends_at.isoformat()) == (600, "2026-11-12T10:10:00+00:00")  # type: ignore[union-attr]
    assert reminder is not None
    assert (reminder.label, reminder.date, reminder.time) == (LABEL, "2026-11-12", "18:00")


def test_the_state_of_an_older_device_or_a_newer_one() -> None:
    older = state_data()
    del older["reminders"]
    parsed = AlarmsState.from_json(older)
    assert not parsed.has_reminders
    assert {i.kind for i in parsed.items} == {"alarm", "timer"}

    newer = {
        **state_data(),
        # a kind this client does not know, an item without an id, a malformed ring
        "bedtimes": [{"id": "b1", "label": "Lights out", "next": "2026-11-12T22:00:00Z", "mood": "calm"}, {"x": 1}],
        "timers": [{"seconds": 5}, "junk", {"id": "t2", "seconds": "600", "endsAt": "soon"}],
        "ringing": {"kind": "alarm"},
        "snoozed": [{"kind": "alarm", "id": "a1", "until": "2026-11-13T07:39:00Z"}],
        "lastRing": {"kind": "alarm", "id": "a1", "at": "2026-11-12T10:00:00Z", "stoppedBy": "button"},
    }
    parsed = AlarmsState.from_json(newer)
    bedtime = parsed.item("bedtime", "b1")
    assert bedtime is not None
    assert (bedtime.label, bedtime.next.isoformat()) == ("Lights out", "2026-11-12T22:00:00+00:00")  # type: ignore[union-attr]
    timer = parsed.item("timer", "t2")
    assert timer is not None
    assert (timer.seconds, timer.ends_at) == (0, None)
    assert [i.id for i in parsed.items if i.kind == "timer"] == ["t2"]
    assert parsed.ringing is None

    with pytest.raises(ValueError):
        AlarmsState.from_json(None)


def test_the_detail_carries_the_alarms_state() -> None:
    detail = DeviceDetail.from_json(fixture("device_kitchen.json"))
    assert detail.alarms is not None
    assert detail.alarms.has_reminders
    assert "stop_ring" in detail.commands["alarms"]
    hall = DeviceDetail.from_json(fixture("device_hall.json"))
    assert hall.alarms is None


# entities


async def test_the_sensors_show_what_rings_next(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence, caplog: pytest.LogCaptureFixture
) -> None:
    caplog.set_level(logging.DEBUG, logger="custom_components.cadence")
    alarm, timer, reminder = (entity(hass, "sensor", k) for k in ("next_alarm", "next_timer", "next_reminder"))
    # the detail's alarm is snoozed to 07:39
    assert state(hass, alarm).state == "2026-11-13T07:39:00+00:00"
    assert state(hass, alarm).attributes["device_class"] == "timestamp"
    assert state(hass, alarm).attributes["id"] == "a1"
    assert state(hass, alarm).attributes["days"] == ["mon", "tue", "wed", "thu", "fri"]
    assert state(hass, alarm).attributes["friendly_name"] == "Kitchen Next alarm"
    assert state(hass, timer).state == "2026-11-12T10:10:00+00:00"
    assert (state(hass, timer).attributes["id"], state(hass, timer).attributes["seconds"]) == ("t1", 600)
    assert state(hass, reminder).state == "2026-11-12T18:00:00+00:00"
    assert state(hass, reminder).attributes["label"] == LABEL
    assert entity_id(hass, "sensor", HALL, "next_alarm") is None

    await backend.push(frame("alarms_state.json"))
    await wait_for(lambda: state(hass, alarm).state == "2026-11-13T07:30:00+00:00")

    # the earliest of several; none left: unknown
    data = state_data()
    data["timers"].append({"id": "t2", "seconds": 60, "endsAt": "2026-11-12T10:01:00Z", "next": "2026-11-12T10:01:00Z"})
    data["alarms"], data["reminders"] = [], []
    await backend.push(frame("alarms_state.json", **data))
    await wait_for(lambda: state(hass, timer).state == "2026-11-12T10:01:00+00:00")
    assert state(hass, timer).attributes["id"] == "t2"
    assert (state(hass, alarm).state, state(hass, reminder).state) == ("unknown", "unknown")
    assert "label" not in state(hass, reminder).attributes

    # the server keeps the state while the device is offline: its alarms still ring
    await backend.push(frame("device_offline.json"))
    await wait_for(lambda: state(hass, entity(hass, "binary_sensor", "online")).state == "off")
    assert state(hass, timer).state == "2026-11-12T10:01:00+00:00"
    assert LABEL not in caplog.text
    assert_private(caplog, backend)


async def test_an_older_device_gets_its_reminder_sensor_once_it_lists_them(
    hass: HomeAssistant, backend: FakeCadence
) -> None:
    del backend.capability(KITCHEN, "alarms")["state"]["reminders"]
    await load(hass, backend)
    assert entity_id(hass, "sensor", KITCHEN, "next_alarm") is not None
    assert entity_id(hass, "sensor", KITCHEN, "next_reminder") is None

    await backend.push(frame("alarms_state.json"))
    await wait_for(lambda: entity_id(hass, "sensor", KITCHEN, "next_reminder") is not None)
    reminder = entity(hass, "sensor", "next_reminder")
    await wait_for(lambda: hass.states.get(reminder) is not None)
    assert state(hass, reminder).state == "2026-11-12T18:00:00+00:00"


async def test_a_device_that_links_with_alarms_gets_them(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence
) -> None:
    assert entity_id(hass, "binary_sensor", HALL, "ringing") is None
    backend.link_device(HALL)
    await backend.push(frame("device_online.json", HALL, name="Hall"))
    await wait_for(lambda: entity_id(hass, "event", HALL, "alarm") is not None)
    wanted = (("binary_sensor", "ringing"), ("button", "stop_ring"), ("button", "snooze"), ("sensor", "next_alarm"))
    await wait_for(lambda: all(entity_id(hass, p, HALL, k) is not None for p, k in wanted))


async def test_a_ring_turns_ringing_on_and_fires_the_event(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence, caplog: pytest.LogCaptureFixture
) -> None:
    caplog.set_level(logging.DEBUG, logger="custom_components.cadence")
    ringing, event = entity(hass, "binary_sensor", "ringing"), entity(hass, "event", "alarm")
    assert state(hass, ringing).state == "off"
    assert state(hass, event).state == "unknown"
    assert state(hass, event).attributes["event_types"] == ["ring_started", "missed", "snoozed", "stopped"]

    await backend.push(frame("alarm_ringing.json"))
    await wait_for(lambda: state(hass, ringing).state == "on")
    assert {k: state(hass, ringing).attributes.get(k) for k in ("kind", "id", "label")} == {
        "kind": "alarm",
        "id": "a1",
        "label": None,
    }
    await wait_for(lambda: state(hass, event).attributes.get("event_type") == "ring_started")
    assert (state(hass, event).attributes["kind"], state(hass, event).attributes["id"]) == ("alarm", "a1")

    await backend.push(frame("alarm_ring_ended.json"))
    await wait_for(lambda: state(hass, ringing).state == "off")
    await wait_for(lambda: state(hass, event).attributes.get("event_type") == "snoozed")
    attrs = state(hass, event).attributes
    assert (attrs["snoozed_until"], attrs["stopped_by"]) == ("2026-11-13T07:39:00+00:00", "snooze")
    assert "label" not in attrs

    # a reminder rings with its label, which the event and the sensor carry
    await backend.push(frame("alarm_ringing.json", kind="reminder", id="r1"))
    await wait_for(lambda: state(hass, ringing).attributes.get("label") == LABEL)
    await wait_for(lambda: state(hass, event).attributes.get("label") == LABEL)
    await backend.push(frame("alarm_ring_ended.json", kind="reminder", id="r1", outcome="missed"))
    await wait_for(lambda: state(hass, event).attributes.get("event_type") == "missed")
    assert state(hass, event).attributes["label"] == LABEL
    assert state(hass, ringing).state == "off"

    # an outcome not known yet ends the ring and fires nothing; a ring of another item leaves this one
    await backend.push(frame("alarm_ringing.json"))
    await wait_for(lambda: state(hass, ringing).state == "on")
    await backend.push(frame("alarm_ring_ended.json", id="other", outcome="stopped"))
    await wait_for(lambda: state(hass, event).attributes.get("event_type") == "stopped")
    assert state(hass, ringing).state == "on"
    fired = state(hass, event).state
    await backend.push(frame("alarm_ring_ended.json", outcome="dismissed_by_cat"))
    await wait_for(lambda: state(hass, ringing).state == "off")
    assert state(hass, event).state == fired

    # a malformed event is dropped
    await backend.push({"type": "alarm_ringing", "deviceId": KITCHEN, "data": {"kind": "alarm"}})
    await backend.push(frame("device_volume.json", level=12))
    await wait_for(lambda: loaded.runtime_data.device(KITCHEN).volume == 12)
    assert state(hass, ringing).state == "off"
    assert LABEL not in caplog.text
    assert_private(caplog, backend)


def test_labels_stay_out_of_the_recorder() -> None:
    assert "label" in CadenceAlarmEntity._unrecorded_attributes


async def test_the_buttons_stop_and_snooze(hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence) -> None:
    for key in ("stop_ring", "snooze"):
        await hass.services.async_call("button", "press", {"entity_id": entity(hass, "button", key)}, blocking=True)
    assert backend.commands == [(KITCHEN, "alarms", "stop_ring", {}), (KITCHEN, "alarms", "snooze", {})]

    backend.command_answer = (409, {"error": "command_failed", "detail": "not_ringing"})
    with pytest.raises(HomeAssistantError) as err:
        await hass.services.async_call(
            "button", "press", {"entity_id": entity(hass, "button", "snooze")}, blocking=True
        )
    assert (err.value.translation_key, err.value.translation_placeholders) == (
        "command_failed",
        {"target": "Kitchen", "detail": "not_ringing"},
    )


# actions


async def test_set_timer_sends_its_seconds(hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence) -> None:
    kitchen = device_of(hass, loaded)
    await hass.services.async_call(
        DOMAIN, "set_timer", {"device_id": kitchen, "duration": {"minutes": 10}}, blocking=True
    )
    [(device, cap, name, args)] = backend.commands
    assert (device, cap, name, args["seconds"], set(args)) == (KITCHEN, "alarms", "set_timer", 600, {"id", "seconds"})
    assert ID.fullmatch(args["id"])

    # the device refuses a timer without a session it knows: said as its refusal
    backend.command_answer = (409, fixture("command_failed.json"))
    with pytest.raises(HomeAssistantError) as err:
        await hass.services.async_call(
            DOMAIN, "set_timer", {"device_id": kitchen, "duration": "00:00:05"}, blocking=True
        )
    assert not isinstance(err.value, ServiceValidationError)
    assert (err.value.translation_key, err.value.translation_placeholders) == (
        "command_failed",
        {"target": "Kitchen", "detail": "no_session"},
    )


@pytest.mark.parametrize("duration", [{"seconds": 0}, {"hours": 24, "seconds": 1}, {"seconds": 1.5}])
async def test_set_timer_refuses_a_duration_out_of_bounds(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence, duration: dict[str, float]
) -> None:
    with pytest.raises(ServiceValidationError) as err:
        await hass.services.async_call(
            DOMAIN, "set_timer", {"device_id": device_of(hass, loaded), "duration": duration}, blocking=True
        )
    assert err.value.translation_key == "timer_duration"
    assert backend.commands == []


async def test_no_action_sets_an_alarm(hass: HomeAssistant, loaded: MockConfigEntry) -> None:
    assert not hass.services.has_service(DOMAIN, "set_alarm")
    assert {"say", "set_timer", "set_reminder"} <= set(hass.services.async_services_for_domain(DOMAIN))


async def test_set_reminder_needs_a_device_that_offers_it(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence
) -> None:
    with pytest.raises(ServiceValidationError) as err:
        await hass.services.async_call(
            DOMAIN,
            "set_reminder",
            {"device_id": device_of(hass, loaded), "text": LABEL, "time": "18:00"},
            blocking=True,
        )
    assert (err.value.translation_key, err.value.translation_placeholders) == (
        "reminders_unsupported",
        {"target": "Kitchen"},
    )
    assert backend.commands == []


@pytest.mark.parametrize(
    ("data", "when"),
    [
        ({"time": "18:00"}, {"time": "18:00"}),
        ({"time": "07:05:30", "date": "2026-11-20"}, {"time": "07:05", "date": "2026-11-20"}),
        ({"time": "18:00", "days": ["fri", "mon", "fri"]}, {"time": "18:00", "days": ["mon", "fri"]}),
        ({"in_minutes": 30}, {"inMinutes": 30}),
    ],
)
async def test_set_reminder_sends_when_and_its_text(
    hass: HomeAssistant,
    backend: FakeCadence,
    caplog: pytest.LogCaptureFixture,
    data: dict[str, Any],
    when: dict[str, Any],
) -> None:
    caplog.set_level(logging.DEBUG, logger="custom_components.cadence")
    backend.capability(KITCHEN, "alarms")["commands"].append({"name": "set_reminder"})
    entry = await load(hass, backend)
    await hass.services.async_call(
        DOMAIN, "set_reminder", {"device_id": device_of(hass, entry), "text": f" {LABEL} ", **data}, blocking=True
    )
    [(device, cap, name, args)] = backend.commands
    assert (device, cap, name) == (KITCHEN, "alarms", "set_reminder")
    reminder_id = args.pop("id")
    assert ID.fullmatch(reminder_id)
    # never a level: the device rings it at its own
    assert args == {"text": LABEL, **when}
    assert LABEL not in caplog.text


@pytest.mark.parametrize(
    "data",
    [
        {},
        {"time": "18:00", "in_minutes": 5},
        {"in_minutes": 5, "days": ["mon"]},
        {"time": "18:00", "date": "2026-11-20", "days": ["mon"]},
    ],
)
async def test_set_reminder_needs_one_when(hass: HomeAssistant, backend: FakeCadence, data: dict[str, Any]) -> None:
    backend.capability(KITCHEN, "alarms")["commands"].append({"name": "set_reminder"})
    entry = await load(hass, backend)
    with pytest.raises(ServiceValidationError) as err:
        await hass.services.async_call(
            DOMAIN, "set_reminder", {"device_id": device_of(hass, entry), "text": LABEL, **data}, blocking=True
        )
    assert err.value.translation_key == "reminder_when"
    assert backend.commands == []


@pytest.mark.parametrize(
    "data",
    [
        {"label": "", "time": "18:00"},
        {"label": "x" * 81, "time": "18:00"},
        {"text": LABEL, "in_minutes": 0},
        {"text": LABEL, "time": "18:00", "level": 40},
        {"text": LABEL, "time": "18:00", "days": ["someday"]},
    ],
)
async def test_set_reminder_validates_its_fields(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence, data: dict[str, Any]
) -> None:
    with pytest.raises(probatio.Invalid):
        await hass.services.async_call(
            DOMAIN, "set_reminder", {"device_id": device_of(hass, loaded), **data}, blocking=True
        )
    assert backend.commands == []
