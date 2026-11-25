"""One Home Assistant device per Cadence device, by its id: created, renamed and removed as the server says."""

from __future__ import annotations

from collections.abc import Mapping

from homeassistant.core import HomeAssistant
from homeassistant.helpers import device_registry as dr

from .const import DOMAIN, MANUFACTURER
from .models import CadenceDevice


def register(hass: HomeAssistant, entry_id: str, dev: CadenceDevice) -> None:
    """Creates the device's Home Assistant device, or gives it the device's name and model (a name the user gave it
    stays: that is name_by_user)."""
    dr.async_get(hass).async_get_or_create(
        config_entry_id=entry_id,
        identifiers={(DOMAIN, dev.id)},
        manufacturer=MANUFACTURER,
        name=dev.name,
        model=dev.model,
    )


def forget(hass: HomeAssistant, entry_id: str, device_id: str) -> None:
    """Removes the device's Home Assistant device from the entry, and with it its entities."""
    registry = dr.async_get(hass)
    if (entry := registry.async_get_device_by_identifier((DOMAIN, device_id), entry_id)) is not None:
        registry.async_update_device(entry.id, remove_config_entry_id=entry_id)


def sync(hass: HomeAssistant, entry_id: str, devices: Mapping[str, CadenceDevice]) -> None:
    """Registers every device and removes the entry's Home Assistant devices the server no longer has."""
    for dev in devices.values():
        register(hass, entry_id, dev)
    registry = dr.async_get(hass)
    for entry in dr.async_entries_for_config_entry(registry, entry_id):
        ids = {value for domain, value in entry.identifiers if domain == DOMAIN}
        if not ids & devices.keys():
            registry.async_update_device(entry.id, remove_config_entry_id=entry_id)
