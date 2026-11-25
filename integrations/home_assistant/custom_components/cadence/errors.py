"""A refused call as a translated Home Assistant error: by its code and the speaker it was for, never upstream text."""

from __future__ import annotations

from homeassistant.exceptions import HomeAssistantError

from .client import CadenceApiError, CadenceAuthError, CadenceConnectionError, CadenceError
from .const import DOMAIN

# the codes the server answers a command or an announcement with, each with its own message
SERVER_CODES = frozenset(
    {
        # commands and announcements
        "device_offline",
        "busy",
        "forbidden",
        "not_found",
        # commands
        "command_failed",
        "invalid_arguments",
        "timeout",
        "unknown_capability",
        "unknown_command",
        # announcements
        "no_audio",
        "tts_unconfigured",
        "superseded",
        "tts_failed",
        "tts_timeout",
        "announce_invalid",
        # the catalog (a browse, a search, a play)
        "unknown_ref",
        "unplayable",
        "sign_in_required",
        "unavailable",
        "invalid_choice",
        "no_catalog_session",
        # multiroom: a join that would extend a stereo pair, one of an unknown device
        "stereo_pair",
        "unknown_device",
    }
)


def failure(err: CadenceError, target: str) -> HomeAssistantError:
    """The error a failed command or announcement raises; target is the speaker's name, so a call to several says
    which one refused."""
    if isinstance(err, CadenceConnectionError):
        return _error("cannot_connect", code=err.code)
    if isinstance(err, CadenceAuthError) and err.status == 401:
        return _error("invalid_auth")
    if err.code == "command_failed":
        # the device's own code, a token, says why it refused
        detail = err.detail if isinstance(err, CadenceApiError) and err.detail else "unknown"
        return _error("command_failed", target=target, detail=detail)
    if err.code in SERVER_CODES:
        return _error(err.code, target=target)
    return _error("request_failed", target=target, code=err.code)


def _error(key: str, **placeholders: str) -> HomeAssistantError:
    return HomeAssistantError(
        translation_domain=DOMAIN, translation_key=key, translation_placeholders=placeholders or None
    )
