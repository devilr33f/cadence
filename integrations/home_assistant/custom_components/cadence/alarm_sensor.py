"""Each device's next alarm, timer and reminder, as timestamps: unknown while none is set."""

from __future__ import annotations

from collections.abc import Callable
from dataclasses import dataclass
from datetime import datetime
from typing import Any

from homeassistant.components.sensor import SensorDeviceClass, SensorEntity, SensorEntityDescription

from .alarm_entity import ATTR_LABEL, CadenceAlarmEntity, has_alarms
from .coordinator import CadenceCoordinator
from .models import AlarmItem, AlarmsState, CadenceDevice

NEXT_REMINDER = "next_reminder"


def _alarm_attrs(item: AlarmItem) -> dict[str, Any]:
    # no days: a one-off alarm, on its date
    out: dict[str, Any] = {"id": item.id, "days": list(item.days)}
    if item.date:
        out["date"] = item.date
    return out


def _timer_attrs(item: AlarmItem) -> dict[str, Any]:
    return {"id": item.id, "seconds": item.seconds}


def _reminder_attrs(item: AlarmItem) -> dict[str, Any]:
    out: dict[str, Any] = {"id": item.id, ATTR_LABEL: item.label, "days": list(item.days)}
    if item.date:
        out["date"] = item.date
    return out


def _next(item: AlarmItem) -> datetime | None:
    return item.next


def _timer_end(item: AlarmItem) -> datetime | None:
    return item.ends_at or item.next


@dataclass(frozen=True, kw_only=True)
class NextDescription(SensorEntityDescription):
    kind: str
    attrs: Callable[[AlarmItem], dict[str, Any]]
    value: Callable[[AlarmItem], datetime | None] = _next


DESCRIPTIONS = {
    d.key: d
    for d in (
        NextDescription(
            key="next_alarm",
            translation_key="next_alarm",
            device_class=SensorDeviceClass.TIMESTAMP,
            kind="alarm",
            attrs=_alarm_attrs,
        ),
        NextDescription(
            key="next_timer",
            translation_key="next_timer",
            device_class=SensorDeviceClass.TIMESTAMP,
            kind="timer",
            attrs=_timer_attrs,
            value=_timer_end,
        ),
        NextDescription(
            key=NEXT_REMINDER,
            translation_key=NEXT_REMINDER,
            device_class=SensorDeviceClass.TIMESTAMP,
            kind="reminder",
            attrs=_reminder_attrs,
        ),
    )
}


def alarm_keys(dev: CadenceDevice) -> list[str]:
    """The next alarm and timer with the alarms capability; the next reminder once the device lists reminders (an
    older one does not)."""
    if not has_alarms(dev):
        return []
    keys = ["next_alarm", "next_timer"]
    if (dev.alarms is not None and dev.alarms.has_reminders) or "set_reminder" in dev.commands.get("alarms", ()):
        keys.append(NEXT_REMINDER)
    return keys


def _first(state: AlarmsState, description: NextDescription) -> AlarmItem | None:
    timed = [(when, i) for i in state.items if i.kind == description.kind and (when := description.value(i))]
    return min(timed, key=lambda pair: pair[0])[1] if timed else None


class CadenceNext(CadenceAlarmEntity, SensorEntity):
    """When the device's first alarm, timer or reminder of a kind rings next."""

    entity_description: NextDescription

    def __init__(self, coordinator: CadenceCoordinator, device_id: str, description: NextDescription) -> None:
        super().__init__(coordinator, device_id, description.key)
        self.entity_description = description

    def _item(self) -> AlarmItem | None:
        state = self._alarms
        return _first(state, self.entity_description) if state is not None else None

    @property
    def native_value(self) -> datetime | None:
        item = self._item()
        return self.entity_description.value(item) if item is not None else None

    @property
    def extra_state_attributes(self) -> dict[str, Any] | None:
        item = self._item()
        return self.entity_description.attrs(item) if item is not None else None
