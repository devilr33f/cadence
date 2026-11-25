"""Each device's alarm buttons: stop what rings, or snooze it for the device's own default minutes."""

from __future__ import annotations

from homeassistant.components.button import ButtonEntity
from homeassistant.core import HomeAssistant
from homeassistant.helpers.entity_platform import AddConfigEntryEntitiesCallback

from .alarm_entity import CadenceAlarmEntity
from .client import CadenceError
from .coordinator import CadenceConfigEntry, CadenceCoordinator
from .entity import follow_devices
from .errors import failure
from .models import ALARMS, CadenceDevice

# each button is its alarms command, sent without arguments: the device picks what rings and its snooze length
COMMANDS = ("stop_ring", "snooze")


async def async_setup_entry(
    hass: HomeAssistant, entry: CadenceConfigEntry, async_add_entities: AddConfigEntryEntitiesCallback
) -> None:
    follow_devices(entry, async_add_entities, _keys, CadenceAlarmButton)


def _keys(dev: CadenceDevice) -> list[str]:
    offered = dev.commands.get(ALARMS, frozenset())
    return [name for name in COMMANDS if name in offered]


class CadenceAlarmButton(CadenceAlarmEntity, ButtonEntity):
    def __init__(self, coordinator: CadenceCoordinator, device_id: str, command: str) -> None:
        super().__init__(coordinator, device_id, command)
        self._command = command
        self._attr_translation_key = command

    def _offered(self, dev: CadenceDevice) -> bool:
        return self._command in _keys(dev)

    async def async_press(self) -> None:
        try:
            await self.coordinator.client.send_command(self.device_id, ALARMS, self._command, {})
        except CadenceError as err:
            raise failure(err, self._target()) from err
