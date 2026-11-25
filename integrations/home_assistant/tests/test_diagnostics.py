"""Diagnostics: what helps debugging, never the token, the url's userinfo or media metadata (rules 6, D11)."""

from __future__ import annotations

import json

from homeassistant.const import CONF_TOKEN, CONF_URL
from homeassistant.core import HomeAssistant
from pytest_homeassistant_custom_component.common import MockConfigEntry

from custom_components.cadence.const import DOMAIN
from custom_components.cadence.diagnostics import async_get_config_entry_diagnostics

from .fake_backend import HALL, KITCHEN, TOKEN, FakeCadence


async def test_diagnostics_of_a_loaded_entry(
    hass: HomeAssistant, loaded: MockConfigEntry, backend: FakeCadence
) -> None:
    out = await async_get_config_entry_diagnostics(hass, loaded)
    text = json.dumps(out)
    assert out["entry"] == {CONF_URL: backend.url, CONF_TOKEN: "**REDACTED**"}
    assert out["connected"] is True
    by_id = {d["id"]: d for d in out["devices"]}
    assert by_id[KITCHEN] == {
        "id": KITCHEN,
        "model": "sim",
        "online": True,
        "capabilities": ["alarms", "audio.output", "media.player", "media.url"],
        "volume": 35,
        "media": "playing",
        "vitals": True,
        "alarms": {"counts": {"alarm": 1, "reminder": 1, "timer": 1}, "ringing": False},
    }
    assert by_id[HALL]["media"] == "idle"
    assert by_id[HALL]["vitals"] is False
    assert by_id[HALL]["alarms"] is None
    for private in (
        TOKEN,
        "Night Drive",
        "The Examples",
        "Fixtures",
        "artwork",
        "Kitchen",
        "url-7f3a",
        "Take the bins out",
        "07:30",
    ):
        assert private not in text


async def test_diagnostics_drop_the_urls_userinfo(hass: HomeAssistant) -> None:
    entry = MockConfigEntry(
        domain=DOMAIN,
        data={CONF_URL: "https://owner:hunter2@cadence.example.com:8443/base", CONF_TOKEN: TOKEN},
    )
    entry.add_to_hass(hass)
    out = await async_get_config_entry_diagnostics(hass, entry)
    assert out["entry"] == {CONF_URL: "https://cadence.example.com:8443/base", CONF_TOKEN: "**REDACTED**"}
    assert "hunter2" not in json.dumps(out)
    assert out["devices"] == []
