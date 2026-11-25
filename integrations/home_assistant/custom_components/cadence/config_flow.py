"""The config flow: a Cadence server's https url and an ha-scoped token, checked by listing its devices (M8-D1)."""

from __future__ import annotations

import logging
from collections.abc import Mapping
from typing import Any
from urllib.parse import urlsplit

import voluptuous as vol
from homeassistant.config_entries import ConfigFlow, ConfigFlowResult
from homeassistant.const import CONF_TOKEN, CONF_URL
from homeassistant.helpers.aiohttp_client import async_get_clientsession
from homeassistant.helpers.selector import TextSelector, TextSelectorConfig, TextSelectorType

from .client import CadenceApiError, CadenceAuthError, CadenceClient, CadenceConnectionError, CadenceScopeError
from .const import DOMAIN

_LOGGER = logging.getLogger(__name__)

URL_SELECTOR = TextSelector(TextSelectorConfig(type=TextSelectorType.URL))
TOKEN_SELECTOR = TextSelector(TextSelectorConfig(type=TextSelectorType.PASSWORD))


class InvalidUrl(ValueError):
    """Not a url the integration connects to."""


def normalize_url(raw: str) -> str:
    """The server's url as the entry keeps it and its unique id: https, host lowercased, no default port, no
    trailing slash. A token never travels in a url, so userinfo, a query or a fragment are refused."""
    parts = urlsplit(raw.strip())
    try:
        port = parts.port
    except ValueError:
        raise InvalidUrl from None
    if (
        parts.scheme.lower() != "https"
        or not parts.hostname
        or parts.username is not None
        or parts.password is not None
    ):
        raise InvalidUrl
    if parts.query or parts.fragment or "?" in raw or "#" in raw:
        raise InvalidUrl
    host = f"[{parts.hostname}]" if ":" in parts.hostname else parts.hostname
    netloc = host if port in (None, 443) else f"{host}:{port}"
    return f"https://{netloc}{parts.path.rstrip('/')}"


def title_of(url: str) -> str:
    return url.removeprefix("https://")


def _token(user_input: Mapping[str, Any]) -> str:
    """The token as pasted, without the white space a copy picks up around it."""
    return str(user_input.get(CONF_TOKEN) or "").strip()


class CadenceConfigFlow(ConfigFlow, domain=DOMAIN):
    VERSION = 1

    async def _check(self, url: str, token: str) -> str | None:
        """The error key for what the server answers to listing its devices with this token, None when it lists and
        the token is no wider than ha: Home Assistant keeps it in the clear, so an admin one would expose the
        server."""
        client = CadenceClient(async_get_clientsession(self.hass), url, token)
        try:
            await client.list_devices()
            if await client.reaches_tokens():
                return "token_too_broad"
        except CadenceScopeError:
            return "insufficient_scope"
        except CadenceAuthError:
            return "invalid_auth"
        except CadenceConnectionError as err:
            _LOGGER.debug("cadence_check_failed code=%s", err.code)
            return "cannot_connect"
        except CadenceApiError as err:
            _LOGGER.debug("cadence_check_failed code=%s", err.code)
            return "cannot_connect" if (err.status or 0) >= 500 else "unknown"
        except Exception as err:
            _LOGGER.error("cadence_check_failed code=unexpected error=%s", type(err).__name__)
            return "unknown"
        return None

    async def async_step_user(self, user_input: dict[str, Any] | None = None) -> ConfigFlowResult:
        errors: dict[str, str] = {}
        if user_input is not None:
            try:
                url = normalize_url(user_input[CONF_URL])
            except InvalidUrl:
                errors[CONF_URL] = "invalid_url"
            else:
                await self.async_set_unique_id(url)
                self._abort_if_unique_id_configured()
                token = _token(user_input)
                if (error := await self._check(url, token)) is None:
                    return self.async_create_entry(title=title_of(url), data={CONF_URL: url, CONF_TOKEN: token})
                errors["base"] = error
        schema = vol.Schema({vol.Required(CONF_URL): URL_SELECTOR, vol.Required(CONF_TOKEN): TOKEN_SELECTOR})
        return self.async_show_form(
            step_id="user",
            # the url only: a token is never sent back to the form
            data_schema=self.add_suggested_values_to_schema(
                schema, {CONF_URL: user_input[CONF_URL]} if user_input else {}
            ),
            errors=errors,
        )

    async def async_step_reauth(self, entry_data: Mapping[str, Any]) -> ConfigFlowResult:
        return await self.async_step_reauth_confirm()

    async def async_step_reauth_confirm(self, user_input: dict[str, Any] | None = None) -> ConfigFlowResult:
        entry = self._get_reauth_entry()
        errors: dict[str, str] = {}
        if user_input is not None:
            token = _token(user_input)
            if (error := await self._check(entry.data[CONF_URL], token)) is None:
                return self.async_update_reload_and_abort(entry, data_updates={CONF_TOKEN: token})
            errors["base"] = error
        return self.async_show_form(
            step_id="reauth_confirm",
            data_schema=vol.Schema({vol.Required(CONF_TOKEN): TOKEN_SELECTOR}),
            description_placeholders={"server": entry.title},
            errors=errors,
        )

    async def async_step_reconfigure(self, user_input: dict[str, Any] | None = None) -> ConfigFlowResult:
        entry = self._get_reconfigure_entry()
        errors: dict[str, str] = {}
        if user_input is not None:
            try:
                url = normalize_url(user_input[CONF_URL])
            except InvalidUrl:
                errors[CONF_URL] = "invalid_url"
            else:
                if url != entry.unique_id:
                    await self.async_set_unique_id(url)
                    self._abort_if_unique_id_configured()
                # no token given: the stored one stays (it is never sent back to the form), but only for the host it
                # was given for: a mistyped look-alike host would receive it
                token = _token(user_input)
                if not token and urlsplit(url).hostname != urlsplit(entry.data[CONF_URL]).hostname:
                    errors[CONF_TOKEN] = "token_required"
                else:
                    token = token or entry.data[CONF_TOKEN]
                    if (error := await self._check(url, token)) is None:
                        return self.async_update_reload_and_abort(
                            entry, unique_id=url, title=title_of(url), data={CONF_URL: url, CONF_TOKEN: token}
                        )
                    errors["base"] = error
        schema = vol.Schema({vol.Required(CONF_URL): URL_SELECTOR, vol.Optional(CONF_TOKEN): TOKEN_SELECTOR})
        suggested = {CONF_URL: user_input[CONF_URL] if user_input else entry.data[CONF_URL]}
        return self.async_show_form(
            step_id="reconfigure", data_schema=self.add_suggested_values_to_schema(schema, suggested), errors=errors
        )
