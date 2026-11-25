"""The cadence.say action: a text the server's text-to-speech speaks on the Cadence devices it names."""

from __future__ import annotations

import asyncio
from dataclasses import dataclass
from typing import cast

import probatio
from homeassistant.config_entries import ConfigEntryState
from homeassistant.const import ATTR_DEVICE_ID, ATTR_ENTITY_ID
from homeassistant.core import HomeAssistant, ServiceCall, callback
from homeassistant.exceptions import ServiceValidationError
from homeassistant.helpers import config_validation as cv
from homeassistant.helpers import device_registry as dr
from homeassistant.helpers import entity_registry as er

from .client import CadenceError
from .const import DOMAIN
from .coordinator import CadenceConfigEntry, CadenceCoordinator
from .errors import failure

SERVICE_SAY = "say"
ATTR_TEXT = "text"
ATTR_LANGUAGE = "language"
LANGUAGES = ("en", "ru")
# the server's bound, in characters once trimmed
MAX_TEXT = 500

SAY_SCHEMA = probatio.Schema(
    {
        probatio.Optional(ATTR_DEVICE_ID): probatio.All(cv.ensure_list, [cv.string]),
        probatio.Optional(ATTR_ENTITY_ID): cv.entity_ids,
        probatio.Required(ATTR_TEXT): probatio.All(cv.string, str.strip, probatio.Length(min=1, max=MAX_TEXT)),
        probatio.Optional(ATTR_LANGUAGE): probatio.In(LANGUAGES),
    }
)


@callback
def async_setup_services(hass: HomeAssistant) -> None:
    async def say(call: ServiceCall) -> None:
        targets = call_targets(hass, call)
        text, language = call.data[ATTR_TEXT], call.data.get(ATTR_LANGUAGE)
        results = await asyncio.gather(
            *(target.coordinator.client.announce(device_id, text, language) for device_id, target in targets.items()),
            return_exceptions=True,
        )
        # every device named was asked; the first refusal says which speaker and why
        for target, result in zip(targets.values(), results, strict=True):
            if isinstance(result, CadenceError):
                raise failure(result, target.name) from result
            if isinstance(result, BaseException):
                raise result

    hass.services.async_register(DOMAIN, SERVICE_SAY, say, schema=SAY_SCHEMA)


@dataclass(frozen=True, slots=True)
class Target:
    """A device a call names: its server, and its name as home assistant shows it."""

    coordinator: CadenceCoordinator
    name: str


def call_targets(hass: HomeAssistant, call: ServiceCall) -> dict[str, Target]:
    """Each device the call names, by its Cadence id, with its server; all of them known to a loaded entry, or the
    call is refused before anything is said."""
    entities = er.async_get(hass)
    named: list[tuple[str, str | None]] = [(device, device) for device in call.data.get(ATTR_DEVICE_ID, [])]
    for entity_id in call.data.get(ATTR_ENTITY_ID, []):
        entity = entities.async_get(entity_id)
        named.append((entity_id, entity.device_id if entity is not None and entity.platform == DOMAIN else None))
    if not named:
        raise ServiceValidationError(translation_domain=DOMAIN, translation_key="no_target")
    targets: dict[str, Target] = {}
    for target, ha_device_id in named:
        device_id, resolved = _resolve(hass, target, ha_device_id)
        targets[device_id] = resolved
    return targets


def _resolve(hass: HomeAssistant, target: str, ha_device_id: str | None) -> tuple[str, Target]:
    device = dr.async_get(hass).async_get(ha_device_id) if ha_device_id else None
    device_id = next((value for domain, value in device.identifiers if domain == DOMAIN), None) if device else None
    if device is None or device_id is None:
        raise ServiceValidationError(
            translation_domain=DOMAIN, translation_key="unknown_target", translation_placeholders={"target": target}
        )
    entries = [
        cast(CadenceConfigEntry, entry)
        for entry_id in device.config_entries
        if (entry := hass.config_entries.async_get_entry(entry_id)) is not None and entry.domain == DOMAIN
    ]
    loaded = [entry for entry in entries if entry.state is ConfigEntryState.LOADED]
    if entries and not loaded:
        raise ServiceValidationError(
            translation_domain=DOMAIN, translation_key="not_loaded", translation_placeholders={"target": target}
        )
    for entry in loaded:
        if (dev := entry.runtime_data.device(device_id)) is not None:
            return device_id, Target(entry.runtime_data, device.name_by_user or device.name or dev.name)
    raise ServiceValidationError(
        translation_domain=DOMAIN, translation_key="unknown_target", translation_placeholders={"target": target}
    )
