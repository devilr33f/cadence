"""Cadence: one config entry per Cadence v2 server, each of its devices a Home Assistant device (M8-D1)."""

from __future__ import annotations

from homeassistant.const import CONF_TOKEN, CONF_URL, Platform
from homeassistant.core import HomeAssistant
from homeassistant.helpers import config_validation as cv
from homeassistant.helpers.aiohttp_client import async_get_clientsession
from homeassistant.helpers.typing import ConfigType

from .alarm_services import async_setup_alarm_services
from .client import CadenceClient
from .const import DOMAIN
from .coordinator import CadenceConfigEntry, CadenceCoordinator
from .services import async_setup_services

PLATFORMS: list[Platform] = [
    Platform.BINARY_SENSOR,
    Platform.BUTTON,
    Platform.EVENT,
    Platform.MEDIA_PLAYER,
    Platform.SENSOR,
]
CONFIG_SCHEMA = cv.config_entry_only_config_schema(DOMAIN)


async def async_setup(hass: HomeAssistant, config: ConfigType) -> bool:
    # registered without an entry, so an action naming a device of an unloaded server says why it failed
    async_setup_services(hass)
    async_setup_alarm_services(hass)
    return True


async def async_setup_entry(hass: HomeAssistant, entry: CadenceConfigEntry) -> bool:
    # home assistant's shared session: tls verified against its roots, never turned off
    client = CadenceClient(async_get_clientsession(hass), entry.data[CONF_URL], entry.data[CONF_TOKEN])
    coordinator = CadenceCoordinator(hass, entry, client)
    await coordinator.async_config_entry_first_refresh()
    entry.runtime_data = coordinator
    await hass.config_entries.async_forward_entry_setups(entry, PLATFORMS)
    # cancelled when the entry unloads
    entry.async_create_background_task(hass, coordinator.async_run_feed(), f"{DOMAIN} live feed")
    return True


async def async_unload_entry(hass: HomeAssistant, entry: CadenceConfigEntry) -> bool:
    return await hass.config_entries.async_unload_platforms(entry, PLATFORMS)
