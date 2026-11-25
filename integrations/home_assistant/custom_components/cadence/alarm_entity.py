"""What the alarm entities share: they come with a device's alarms capability and read its state, which the server
keeps while the device is offline (its alarms ring offline too). Setting an alarm from Home Assistant is never offered
(rule 7)."""

from __future__ import annotations

from .entity import CadenceEntity
from .models import ALARMS, AlarmsState, CadenceDevice

# a reminder's label is the owner's text: shown in Home Assistant, kept out of the recorder's database
ATTR_LABEL = "label"


def has_alarms(dev: CadenceDevice) -> bool:
    return ALARMS in dev.capabilities


class CadenceAlarmEntity(CadenceEntity):
    _unrecorded_attributes = frozenset({ATTR_LABEL})

    def _offered(self, dev: CadenceDevice) -> bool:
        return has_alarms(dev)

    @property
    def _alarms(self) -> AlarmsState | None:
        dev = self.device
        return dev.alarms if dev is not None else None

    def _target(self) -> str:
        """The device's name as home assistant shows it, for an error."""
        if (entry := self.device_entry) is not None and (name := entry.name_by_user or entry.name):
            return name
        dev = self.device
        return dev.name if dev is not None else self.device_id
