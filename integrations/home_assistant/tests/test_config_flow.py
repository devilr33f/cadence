"""The config flow: a server url and an ha token, checked against the fake server through Home Assistant's session."""

from __future__ import annotations

from collections.abc import Iterator
from unittest.mock import AsyncMock, patch

import pytest
from homeassistant.config_entries import SOURCE_USER
from homeassistant.const import CONF_TOKEN, CONF_URL
from homeassistant.core import HomeAssistant
from homeassistant.data_entry_flow import FlowResultType

from custom_components.cadence.const import DOMAIN

from .conftest import config_entry
from .fake_backend import TOKEN, FakeCadence, UntrustedServer


@pytest.fixture(autouse=True)
def setup_entry() -> Iterator[AsyncMock]:
    """The flow alone: a created or updated entry is not set up."""
    with patch("custom_components.cadence.async_setup_entry", return_value=True) as mock:
        yield mock


async def start(hass: HomeAssistant) -> str:
    result = await hass.config_entries.flow.async_init(DOMAIN, context={"source": SOURCE_USER})
    assert result["type"] is FlowResultType.FORM
    assert result["step_id"] == "user"
    return result["flow_id"]


async def test_user_step_creates_one_entry_per_server(
    hass: HomeAssistant, backend: FakeCadence, setup_entry: AsyncMock
) -> None:
    flow = await start(hass)
    result = await hass.config_entries.flow.async_configure(
        # pasted: the url in another spelling, the token with the white space a copy picks up
        flow,
        {CONF_URL: f" {backend.url.replace('https', 'HTTPS')}/ ", CONF_TOKEN: f" {TOKEN}\n"},
    )
    assert result["type"] is FlowResultType.CREATE_ENTRY
    assert result["title"] == backend.url.removeprefix("https://")
    assert result["data"] == {CONF_URL: backend.url, CONF_TOKEN: TOKEN}
    assert result["result"].unique_id == backend.url
    # the list, then the admin-only token list, which refuses an ha token (403)
    assert backend.requests == ["GET /api/v1/devices", "GET /api/v1/tokens"]
    assert setup_entry.call_count == 1

    # the same server again, written another way
    flow = await start(hass)
    result = await hass.config_entries.flow.async_configure(flow, {CONF_URL: backend.url + "/", CONF_TOKEN: TOKEN})
    assert result["type"] is FlowResultType.ABORT
    assert result["reason"] == "already_configured"
    assert backend.requests == ["GET /api/v1/devices", "GET /api/v1/tokens"]


@pytest.mark.parametrize(
    ("token", "refuse", "error"),
    [("cdn_wrong", None, "invalid_auth"), (TOKEN, 403, "insufficient_scope")],
)
async def test_user_step_refused_tokens(
    hass: HomeAssistant, backend: FakeCadence, token: str, refuse: int | None, error: str
) -> None:
    backend.refuse = refuse
    flow = await start(hass)
    result = await hass.config_entries.flow.async_configure(flow, {CONF_URL: backend.url, CONF_TOKEN: token})
    assert result["type"] is FlowResultType.FORM
    assert result["errors"] == {"base": error}

    backend.refuse = None
    result = await hass.config_entries.flow.async_configure(flow, {CONF_URL: backend.url, CONF_TOKEN: TOKEN})
    assert result["type"] is FlowResultType.CREATE_ENTRY


async def test_user_step_refuses_a_token_wider_than_ha(hass: HomeAssistant, backend: FakeCadence) -> None:
    """An admin token lists the devices too, but Home Assistant keeps its token in the clear: only ha is taken."""
    backend.token_scopes = {"admin"}
    flow = await start(hass)
    result = await hass.config_entries.flow.async_configure(flow, {CONF_URL: backend.url, CONF_TOKEN: TOKEN})
    assert result["type"] is FlowResultType.FORM
    assert result["errors"] == {"base": "token_too_broad"}
    assert backend.requests == ["GET /api/v1/devices", "GET /api/v1/tokens"]

    # ha with metrics reaches no admin route either
    backend.token_scopes = {"ha", "metrics"}
    result = await hass.config_entries.flow.async_configure(flow, {CONF_URL: backend.url, CONF_TOKEN: TOKEN})
    assert result["type"] is FlowResultType.CREATE_ENTRY


@pytest.mark.parametrize(
    ("status", "error"),
    [(503, "cannot_connect"), (500, "cannot_connect"), (401, "invalid_auth"), (404, "unknown")],
)
async def test_user_step_maps_the_token_probes_other_answers(
    hass: HomeAssistant, backend: FakeCadence, status: int, error: str
) -> None:
    backend.tokens_answer = (status, {"error": "whatever"})
    flow = await start(hass)
    result = await hass.config_entries.flow.async_configure(flow, {CONF_URL: backend.url, CONF_TOKEN: TOKEN})
    assert result["type"] is FlowResultType.FORM
    assert result["errors"] == {"base": error}


async def test_reauth_refuses_a_token_wider_than_ha(hass: HomeAssistant, backend: FakeCadence) -> None:
    entry = config_entry(backend.url, token="cdn_old")
    entry.add_to_hass(hass)
    backend.token_scopes = {"admin", "ha"}
    result = await entry.start_reauth_flow(hass)
    result = await hass.config_entries.flow.async_configure(result["flow_id"], {CONF_TOKEN: TOKEN})
    assert result["type"] is FlowResultType.FORM
    assert result["errors"] == {"base": "token_too_broad"}
    assert entry.data[CONF_TOKEN] == "cdn_old"

    backend.token_scopes = {"ha"}
    result = await hass.config_entries.flow.async_configure(result["flow_id"], {CONF_TOKEN: TOKEN})
    assert result["reason"] == "reauth_successful"


async def test_reconfigure_refuses_a_token_wider_than_ha(hass: HomeAssistant, backend: FakeCadence) -> None:
    """Also the stored token, when the field is left empty: one stored before the check was added is not kept."""
    entry = config_entry(backend.url)
    entry.add_to_hass(hass)
    backend.token_scopes = {"admin"}
    result = await entry.start_reconfigure_flow(hass)
    result = await hass.config_entries.flow.async_configure(result["flow_id"], {CONF_URL: backend.url})
    assert result["type"] is FlowResultType.FORM
    assert result["errors"] == {"base": "token_too_broad"}

    backend.token_scopes = {"ha"}
    result = await hass.config_entries.flow.async_configure(result["flow_id"], {CONF_URL: backend.url})
    assert result["reason"] == "reconfigure_successful"


async def test_user_step_untrusted_certificate_cannot_connect(
    hass: HomeAssistant, rogue_backend: UntrustedServer, caplog: pytest.LogCaptureFixture
) -> None:
    flow = await start(hass)
    result = await hass.config_entries.flow.async_configure(flow, {CONF_URL: rogue_backend.url, CONF_TOKEN: TOKEN})
    assert result["type"] is FlowResultType.FORM
    assert result["errors"] == {"base": "cannot_connect"}
    assert rogue_backend.handshakes >= 1
    assert rogue_backend.requests == []
    assert TOKEN not in caplog.text


async def test_user_step_unreachable_server(hass: HomeAssistant, backend: FakeCadence) -> None:
    url = backend.url
    await backend.close()
    flow = await start(hass)
    result = await hass.config_entries.flow.async_configure(flow, {CONF_URL: url, CONF_TOKEN: TOKEN})
    assert result["errors"] == {"base": "cannot_connect"}


@pytest.mark.parametrize(
    "url",
    [
        "http://cadence.example.com",
        "https://user:pass@cadence.example.com",
        "https://cadence.example.com/?token=x",
        "https://cadence.example.com/#x",
        "cadence.example.com",
        "https://",
        "https://cadence.example.com:99999",
    ],
)
async def test_user_step_refuses_urls(hass: HomeAssistant, url: str) -> None:
    flow = await start(hass)
    result = await hass.config_entries.flow.async_configure(flow, {CONF_URL: url, CONF_TOKEN: TOKEN})
    assert result["type"] is FlowResultType.FORM
    assert result["errors"] == {CONF_URL: "invalid_url"}


async def test_reauth_takes_a_new_token(hass: HomeAssistant, backend: FakeCadence) -> None:
    entry = config_entry(backend.url, token="cdn_old")
    entry.add_to_hass(hass)
    result = await entry.start_reauth_flow(hass)
    assert result["type"] is FlowResultType.FORM
    assert result["step_id"] == "reauth_confirm"

    result = await hass.config_entries.flow.async_configure(result["flow_id"], {CONF_TOKEN: "cdn_still_wrong"})
    assert result["errors"] == {"base": "invalid_auth"}
    result = await hass.config_entries.flow.async_configure(result["flow_id"], {CONF_TOKEN: f"{TOKEN}  "})
    assert result["type"] is FlowResultType.ABORT
    assert result["reason"] == "reauth_successful"
    assert entry.data == {CONF_URL: backend.url, CONF_TOKEN: TOKEN}


async def test_reconfigure_moves_the_server_and_keeps_the_token(
    hass: HomeAssistant, backend: FakeCadence, rogue_backend: UntrustedServer
) -> None:
    entry = config_entry(rogue_backend.url)
    entry.add_to_hass(hass)
    result = await entry.start_reconfigure_flow(hass)
    assert result["type"] is FlowResultType.FORM
    assert result["step_id"] == "reconfigure"

    # no token given and the same host on another port: the stored one is checked against the new url
    result = await hass.config_entries.flow.async_configure(result["flow_id"], {CONF_URL: backend.url})
    assert result["type"] is FlowResultType.ABORT
    assert result["reason"] == "reconfigure_successful"
    assert entry.data == {CONF_URL: backend.url, CONF_TOKEN: TOKEN}
    assert entry.unique_id == backend.url
    assert entry.title == backend.url.removeprefix("https://")


async def test_reconfigure_to_another_host_needs_the_token_again(hass: HomeAssistant, backend: FakeCadence) -> None:
    """The stored token never goes to a host it was not given for: a mistyped look-alike would get it."""
    entry = config_entry("https://cadence.example.com")
    entry.add_to_hass(hass)
    result = await entry.start_reconfigure_flow(hass)
    result = await hass.config_entries.flow.async_configure(result["flow_id"], {CONF_URL: backend.url})
    assert result["type"] is FlowResultType.FORM
    assert result["errors"] == {CONF_TOKEN: "token_required"}
    assert backend.requests == []
    assert entry.data[CONF_URL] == "https://cadence.example.com"

    result = await hass.config_entries.flow.async_configure(
        result["flow_id"], {CONF_URL: backend.url, CONF_TOKEN: TOKEN}
    )
    assert result["reason"] == "reconfigure_successful"
    assert entry.data == {CONF_URL: backend.url, CONF_TOKEN: TOKEN}


async def test_reconfigure_checks_the_token_and_refuses_another_entrys_server(
    hass: HomeAssistant, backend: FakeCadence, rogue_backend: UntrustedServer
) -> None:
    entry = config_entry(backend.url)
    entry.add_to_hass(hass)
    other = config_entry(rogue_backend.url)
    other.add_to_hass(hass)

    result = await entry.start_reconfigure_flow(hass)
    result = await hass.config_entries.flow.async_configure(
        result["flow_id"], {CONF_URL: backend.url, CONF_TOKEN: "cdn_wrong"}
    )
    assert result["errors"] == {"base": "invalid_auth"}
    result = await hass.config_entries.flow.async_configure(result["flow_id"], {CONF_URL: rogue_backend.url})
    assert result["type"] is FlowResultType.ABORT
    assert result["reason"] == "already_configured"
    assert entry.data[CONF_URL] == backend.url

    result = await entry.start_reconfigure_flow(hass)
    result = await hass.config_entries.flow.async_configure(
        result["flow_id"], {CONF_URL: backend.url, CONF_TOKEN: TOKEN}
    )
    assert result["reason"] == "reconfigure_successful"
