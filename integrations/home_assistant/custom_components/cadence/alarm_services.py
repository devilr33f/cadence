"""The cadence.set_timer and cadence.set_reminder actions. Home Assistant never sets an alarm (rule 7), and never
sends a reminder's level: the device rings it at its own."""

from __future__ import annotations

import asyncio
import secrets
from collections.abc import Callable
from datetime import date, time, timedelta
from typing import Any

import probatio
from homeassistant.const import ATTR_DEVICE_ID, WEEKDAYS
from homeassistant.core import HomeAssistant, ServiceCall, callback
from homeassistant.exceptions import ServiceValidationError
from homeassistant.helpers import config_validation as cv

from .client import CadenceError
from .const import DOMAIN
from .errors import failure
from .models import ALARMS
from .services import Target, call_targets

SERVICE_SET_TIMER = "set_timer"
SERVICE_SET_REMINDER = "set_reminder"
ATTR_DURATION = "duration"
ATTR_TEXT = "text"
ATTR_TIME = "time"
ATTR_DATE = "date"
ATTR_DAYS = "days"
ATTR_IN_MINUTES = "in_minutes"
# the device's bounds: a timer of 1 s to a day
MAX_TIMER_SECONDS = 86400
# what the server speaks at ring time, as an announcement takes
MAX_TEXT = 500
MAX_IN_MINUTES = 7 * 24 * 60
# an id the device takes: ^[a-z0-9]{1,16}$
ID_BYTES = 6

DEVICES = probatio.All(cv.ensure_list, [cv.string])

SET_TIMER_SCHEMA = probatio.Schema(
    {
        probatio.Required(ATTR_DEVICE_ID): DEVICES,
        probatio.Required(ATTR_DURATION): cv.time_period,
    }
)

SET_REMINDER_SCHEMA = probatio.Schema(
    {
        probatio.Required(ATTR_DEVICE_ID): DEVICES,
        probatio.Required(ATTR_TEXT): probatio.All(cv.string, str.strip, probatio.Length(min=1, max=MAX_TEXT)),
        probatio.Optional(ATTR_TIME): cv.time,
        probatio.Optional(ATTR_DATE): cv.date,
        probatio.Optional(ATTR_DAYS): cv.weekdays,
        probatio.Optional(ATTR_IN_MINUTES): probatio.All(
            probatio.Coerce(int), probatio.Range(min=1, max=MAX_IN_MINUTES)
        ),
    }
)


def new_id() -> str:
    """A fresh id for a timer or reminder: 12 lowercase hex digits."""
    return secrets.token_hex(ID_BYTES)


@callback
def async_setup_alarm_services(hass: HomeAssistant) -> None:
    async def set_timer(call: ServiceCall) -> None:
        duration: timedelta = call.data[ATTR_DURATION]
        seconds = duration.total_seconds()
        if seconds != int(seconds) or not 1 <= seconds <= MAX_TIMER_SECONDS:
            raise ServiceValidationError(translation_domain=DOMAIN, translation_key="timer_duration")
        # no session id: the device takes a timer only from a session it knows, until it accepts one without
        await _send_all(call_targets(hass, call), SERVICE_SET_TIMER, lambda: {"id": new_id(), "seconds": int(seconds)})

    async def set_reminder(call: ServiceCall) -> None:
        when = _reminder_when(call.data)
        targets = call_targets(hass, call)
        for device_id, target in targets.items():
            dev = target.coordinator.device(device_id)
            if dev is None or SERVICE_SET_REMINDER not in dev.commands.get(ALARMS, frozenset()):
                raise ServiceValidationError(
                    translation_domain=DOMAIN,
                    translation_key="reminders_unsupported",
                    translation_placeholders={"target": target.name},
                )
        text = call.data[ATTR_TEXT]
        # the server speaks the text into the reminder's sound and gives the device its label
        await _send_all(targets, SERVICE_SET_REMINDER, lambda: {"id": new_id(), "text": text, **when})

    hass.services.async_register(DOMAIN, SERVICE_SET_TIMER, set_timer, schema=SET_TIMER_SCHEMA)
    hass.services.async_register(DOMAIN, SERVICE_SET_REMINDER, set_reminder, schema=SET_REMINDER_SCHEMA)


def _reminder_when(data: dict[str, Any]) -> dict[str, Any]:
    """When the reminder rings, as the device takes it: a time (once on a date, or on days, or at its next time),
    or in so many minutes."""
    at: time | None = data.get(ATTR_TIME)
    on: date | None = data.get(ATTR_DATE)
    days: list[str] = data.get(ATTR_DAYS) or []
    minutes: int | None = data.get(ATTR_IN_MINUTES)
    if (at is None) == (minutes is None) or (minutes is not None and (on or days)) or (on and days):
        raise ServiceValidationError(translation_domain=DOMAIN, translation_key="reminder_when")
    if minutes is not None:
        return {"inMinutes": minutes}
    out: dict[str, Any] = {"time": f"{at.hour:02d}:{at.minute:02d}"}  # type: ignore[union-attr]
    if on is not None:
        out["date"] = on.isoformat()
    if days:
        # the device's order, each day once
        out["days"] = [d for d in WEEKDAYS if d in days]
    return out


async def _send_all(targets: dict[str, Target], command: str, args: Callable[[], dict[str, Any]]) -> None:
    """Asks every device at once, each with its own args; the first refusal says which device and why."""
    results = await asyncio.gather(
        *(
            target.coordinator.client.send_command(device_id, ALARMS, command, args())
            for device_id, target in targets.items()
        ),
        return_exceptions=True,
    )
    for target, result in zip(targets.values(), results, strict=True):
        if isinstance(result, CadenceError):
            raise failure(result, target.name) from result
        if isinstance(result, BaseException):
            raise result
