"""What every Cadence entity shares, and how a platform follows the devices: their entities come and go with them."""

from __future__ import annotations

from collections.abc import Callable, Iterable
from functools import partial

from homeassistant.core import callback
from homeassistant.helpers.entity_platform import AddConfigEntryEntitiesCallback
from homeassistant.helpers.update_coordinator import CoordinatorEntity

from .coordinator import CadenceConfigEntry, CadenceCoordinator
from .models import CadenceDevice


class CadenceEntity(CoordinatorEntity[CadenceCoordinator]):
    """One entity of one device. It looks its device up by id at every read: the coordinator replaces device objects.
    Unavailable while the feed is down, and once the device is gone."""

    _attr_has_entity_name = True
    # true when the state is the device's own report, stale while the device is offline
    _needs_link = False

    def __init__(self, coordinator: CadenceCoordinator, device_id: str, key: str) -> None:
        super().__init__(coordinator)
        self.device_id = device_id
        # the server refuses its agent any entity of the cadence platform by its registry entry, which needs this id
        self._attr_unique_id = f"{device_id}:{key}"
        self._attr_device_info = coordinator.device_info(device_id)

    @property
    def device(self) -> CadenceDevice | None:
        return self.coordinator.device(self.device_id)

    def _offered(self, dev: CadenceDevice) -> bool:
        """Whether the device still has the capabilities this entity needs: one it lost leaves it unavailable."""
        return True

    @property
    def available(self) -> bool:
        dev = self.device
        return super().available and dev is not None and (dev.online or not self._needs_link) and self._offered(dev)


@callback
def follow_devices(
    entry: CadenceConfigEntry,
    add_entities: AddConfigEntryEntitiesCallback,
    keys: Callable[[CadenceDevice], Iterable[str]],
    make: Callable[[CadenceCoordinator, str, str], CadenceEntity],
    *,
    on_state: bool = False,
) -> None:
    """Adds each device's entities as keys says it needs them: for the devices known now and each new one, again when
    a device's capabilities change, and with on_state at every update too (an entity that comes once a value is
    reported). An entity no longer needed stays, unavailable; a deleted device's entities go with its device."""
    coordinator = entry.runtime_data
    made: set[str] = set()

    @callback
    def add_missing(device_id: str) -> None:
        if (dev := coordinator.device(device_id)) is None:
            return
        new: list[CadenceEntity] = []
        for key in keys(dev):
            unique_id = f"{device_id}:{key}"
            if unique_id in made:
                continue
            made.add(unique_id)
            entity = make(coordinator, device_id, key)
            # removed with its device: one that comes back is made again
            entity.async_on_remove(partial(made.discard, unique_id))
            new.append(entity)
        if new:
            add_entities(new)

    @callback
    def add_devices(device_ids: list[str]) -> None:
        for device_id in device_ids:
            entry.async_on_unload(coordinator.async_add_device_listener(device_id, partial(add_missing, device_id)))
            add_missing(device_id)

    @callback
    def add_reported() -> None:
        for device_id in list(coordinator.devices):
            add_missing(device_id)

    entry.async_on_unload(coordinator.async_add_device_callback(add_devices))
    if on_state:
        entry.async_on_unload(coordinator.async_add_listener(add_reported))
