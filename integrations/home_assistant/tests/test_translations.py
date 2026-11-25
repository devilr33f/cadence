"""Translations: en is strings.json, ru has the same keys and placeholders and none of the speaker's command words, and
every error the component raises has its message."""

from __future__ import annotations

import json
import re
import string
from pathlib import Path
from typing import Any

import yaml

from custom_components.cadence.errors import SERVER_CODES

COMPONENT = Path(__file__).parents[1] / "custom_components" / "cadence"
# what the speaker would act on in its own reply (AGENTS.md, M8-D11)
COMMAND_WORDS = ("стоп", "хватит", "пауза", "тише", "громче")


def load(name: str) -> Any:
    return json.loads((COMPONENT / name).read_text(encoding="utf-8"))


def leaves(tree: dict[str, Any], prefix: str = "") -> dict[str, str]:
    out: dict[str, str] = {}
    for key, value in tree.items():
        if isinstance(value, dict):
            out |= leaves(value, f"{prefix}{key}.")
        else:
            out[prefix + key] = value
    return out


def placeholders(text: str) -> set[str]:
    return {name for _, name, _, _ in string.Formatter().parse(text) if name}


def test_translations_cover_the_strings() -> None:
    strings = load("strings.json")
    assert load("translations/en.json") == strings
    ru = leaves(load("translations/ru.json"))
    assert ru.keys() == leaves(strings).keys()
    for key, text in leaves(strings).items():
        assert placeholders(ru[key]) == placeholders(text), key


def test_russian_says_no_command_word() -> None:
    for key, text in leaves(load("translations/ru.json")).items():
        for word in COMMAND_WORDS:
            assert word not in text.lower(), (key, word)


def test_every_error_has_its_message() -> None:
    raised = set(SERVER_CODES)
    for source in COMPONENT.glob("*.py"):
        text = source.read_text(encoding="utf-8")
        raised |= set(re.findall(r'(?:translation_domain=DOMAIN,\s*translation_key=|_error\()"(\w+)"', text))
    assert raised <= load("strings.json")["exceptions"].keys()
    assert {
        "no_target",
        "unknown_target",
        "not_loaded",
        "enqueue_unsupported",
        "media_unsupported",
        "request_failed",
        "cannot_connect",
    } <= raised


def test_the_actions_fields_are_described() -> None:
    services = yaml.safe_load((COMPONENT / "services.yaml").read_text(encoding="utf-8"))
    described = load("strings.json")["services"]
    assert services.keys() == described.keys()
    for name, service in services.items():
        assert service["fields"].keys() == described[name]["fields"].keys()
