"""Fixtures: a throwaway CA that Home Assistant's shared session trusts, another it does not, and the fake server."""

from __future__ import annotations

import ipaddress
import ssl
from collections.abc import AsyncIterator, Iterator
from dataclasses import dataclass
from datetime import UTC, datetime, timedelta
from pathlib import Path
from unittest.mock import patch

import aiohttp
import pytest
from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.x509.oid import NameOID
from homeassistant.const import CONF_TOKEN, CONF_URL
from homeassistant.core import HomeAssistant
from homeassistant.helpers import entity_registry as er
from homeassistant.util import ssl as ssl_util
from pytest_homeassistant_custom_component.common import MockConfigEntry

from custom_components.cadence import client as cadence_client
from custom_components.cadence import coordinator as cadence_coordinator
from custom_components.cadence.const import DOMAIN

from .fake_backend import TOKEN, FakeCadence, UntrustedServer, wait_for


@dataclass(frozen=True)
class Authority:
    """A CA and a 127.0.0.1 server certificate it signed."""

    ca_pem: str
    cert: Path
    key: Path

    def server_context(self) -> ssl.SSLContext:
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(self.cert, self.key)
        return context


def _authority(directory: Path, name: str) -> Authority:
    now = datetime.now(UTC)
    ca_key = ec.generate_private_key(ec.SECP256R1())
    ca_name = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, f"{name} test CA")])
    ca = (
        x509.CertificateBuilder()
        .subject_name(ca_name)
        .issuer_name(ca_name)
        .public_key(ca_key.public_key())
        .serial_number(x509.random_serial_number())
        .not_valid_before(now - timedelta(hours=1))
        .not_valid_after(now + timedelta(days=1))
        .add_extension(x509.BasicConstraints(ca=True, path_length=0), critical=True)
        .add_extension(
            x509.KeyUsage(
                digital_signature=False,
                content_commitment=False,
                key_encipherment=False,
                data_encipherment=False,
                key_agreement=False,
                key_cert_sign=True,
                crl_sign=True,
                encipher_only=False,
                decipher_only=False,
            ),
            critical=True,
        )
        .add_extension(x509.SubjectKeyIdentifier.from_public_key(ca_key.public_key()), critical=False)
        .sign(ca_key, hashes.SHA256())
    )
    key = ec.generate_private_key(ec.SECP256R1())
    cert = (
        x509.CertificateBuilder()
        .subject_name(x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, "127.0.0.1")]))
        .issuer_name(ca_name)
        .public_key(key.public_key())
        .serial_number(x509.random_serial_number())
        .not_valid_before(now - timedelta(hours=1))
        .not_valid_after(now + timedelta(days=1))
        .add_extension(x509.SubjectAlternativeName([x509.IPAddress(ipaddress.ip_address("127.0.0.1"))]), critical=False)
        .add_extension(x509.AuthorityKeyIdentifier.from_issuer_public_key(ca_key.public_key()), critical=False)
        .sign(ca_key, hashes.SHA256())
    )
    cert_path, key_path = directory / f"{name}.pem", directory / f"{name}-key.pem"
    cert_path.write_bytes(cert.public_bytes(serialization.Encoding.PEM))
    key_path.write_bytes(
        key.private_bytes(serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8, serialization.NoEncryption())
    )
    return Authority(ca_pem=ca.public_bytes(serialization.Encoding.PEM).decode(), cert=cert_path, key=key_path)


@pytest.fixture(scope="session")
def trusted(tmp_path_factory: pytest.TempPathFactory) -> Authority:
    """The CA Home Assistant's shared client session trusts besides certifi's bundle."""
    authority = _authority(tmp_path_factory.mktemp("tls"), "trusted")
    # the shared session verifies with this cached context: TLS stays verified, the test CA is one more root
    ssl_util.get_default_context().load_verify_locations(cadata=authority.ca_pem)
    return authority


@pytest.fixture(scope="session")
def rogue(tmp_path_factory: pytest.TempPathFactory) -> Authority:
    """A CA nobody trusts."""
    return _authority(tmp_path_factory.mktemp("tls"), "rogue")


@pytest.fixture(autouse=True)
def _custom_integrations(enable_custom_integrations: None) -> None:
    """Loads custom_components/cadence through Home Assistant's own loader."""


@pytest.fixture(autouse=True)
def _fast_reconnects() -> Iterator[None]:
    """A dropped feed reconnects, an unknown device has the list read and a device its detail, within milliseconds."""
    with (
        patch.object(cadence_coordinator, "FEED_BACKOFF", (0.01, 0.05)),
        patch.object(cadence_coordinator, "LIST_SYNC_INTERVAL", 0.0),
        patch.object(cadence_coordinator, "DETAIL_DEBOUNCE", 0.05),
    ):
        yield


@pytest.fixture
async def backend(socket_enabled: None, trusted: Authority) -> AsyncIterator[FakeCadence]:
    fake = await FakeCadence(trusted.server_context()).start()
    yield fake
    await fake.close()


@pytest.fixture
async def rogue_backend(socket_enabled: None, rogue: Authority) -> AsyncIterator[UntrustedServer]:
    fake = await UntrustedServer(rogue.server_context()).start()
    yield fake
    await fake.close()


@pytest.fixture
async def session(trusted: Authority) -> AsyncIterator[aiohttp.ClientSession]:
    """A plain aiohttp session trusting the test CA, for the client without Home Assistant."""
    context = ssl.create_default_context(cadata=trusted.ca_pem)
    async with aiohttp.ClientSession(connector=aiohttp.TCPConnector(ssl=context)) as s:
        yield s


@pytest.fixture
def client(session: aiohttp.ClientSession, backend: FakeCadence) -> cadence_client.CadenceClient:
    return cadence_client.CadenceClient(session, backend.url, TOKEN)


def entity_id(hass: HomeAssistant, platform: str, device_id: str, key: str) -> str | None:
    """The entity id Home Assistant gave the device's entity of this key, by its unique id."""
    return er.async_get(hass).async_get_entity_id(platform, DOMAIN, f"{device_id}:{key}")


def config_entry(url: str, token: str = TOKEN) -> MockConfigEntry:
    return MockConfigEntry(
        domain=DOMAIN, title="127.0.0.1", unique_id=url, data={CONF_URL: url, CONF_TOKEN: token}, version=1
    )


@pytest.fixture
async def loaded(hass: HomeAssistant, backend: FakeCadence) -> MockConfigEntry:
    """The entry set up against the fake, its feed subscribed and its state read again after."""
    entry = config_entry(backend.url)
    entry.add_to_hass(hass)
    assert await hass.config_entries.async_setup(entry.entry_id)
    await hass.async_block_till_done()
    coordinator = entry.runtime_data
    await wait_for(lambda: coordinator.connected)
    return entry
