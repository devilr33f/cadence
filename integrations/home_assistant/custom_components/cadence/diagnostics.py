"""Diagnostics: the entry without its token or the url's userinfo, and each device without its media metadata or its
reminders' labels (counts of its alarms alone)."""

from __future__ import annotations

from typing import Any
from urllib.parse import urlsplit, urlunsplit

from homeassistant.components.diagnostics import REDACTED
from homeassistant.config_entries import ConfigEntryState
from homeassistant.const import CONF_TOKEN, CONF_URL
from homeassistant.core import HomeAssistant

from .coordinator import CadenceConfigEntry
from .models import AlarmsState


def _without_userinfo(url: str) -> str:
    parts = urlsplit(url)
    host = parts.hostname or ""
    if ":" in host:
        host = f"[{host}]"
    try:
        port = parts.port
    except ValueError:
        port = None
    netloc = f"{host}:{port}" if port else host
    return urlunsplit((parts.scheme, netloc, parts.path, "", ""))


async def async_get_config_entry_diagnostics(hass: HomeAssistant, entry: CadenceConfigEntry) -> dict[str, Any]:
    out: dict[str, Any] = {
        "entry": {CONF_URL: _without_userinfo(entry.data.get(CONF_URL, "")), CONF_TOKEN: REDACTED},
        "connected": False,
        "devices": [],
    }
    if entry.state is not ConfigEntryState.LOADED:
        return out
    coordinator = entry.runtime_data
    out["connected"] = coordinator.connected
    out["devices"] = [
        {
            "id": dev.id,
            "model": dev.model,
            "online": dev.online,
            "capabilities": sorted(dev.capabilities),
            "volume": dev.volume,
            "media": dev.media.state if dev.media is not None else "idle",
            "vitals": dev.vitals is not None,
            "alarms": _alarms(dev.alarms),
        }
        for dev in coordinator.devices.values()
    ]
    return out


def _alarms(state: AlarmsState | None) -> dict[str, Any] | None:
    """How many of each kind and whether one rings: no time, label or id."""
    if state is None:
        return None
    counts: dict[str, int] = {}
    for item in state.items:
        counts[item.kind] = counts.get(item.kind, 0) + 1
    return {"counts": dict(sorted(counts.items())), "ringing": state.ringing is not None}
