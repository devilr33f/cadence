"""Reading the server's devices whole: the list, then each one's detail, media and vitals at once."""

from __future__ import annotations

import asyncio
from collections.abc import Coroutine, Iterable, Iterator
from typing import Any, cast

from .client import CadenceApiError, CadenceAuthError, CadenceClient
from .models import CadenceDevice, Device, DeviceDetail, MediaSession, Vitals

# the server lists a device whose enrol code was never claimed too; it is no device yet
ENROLLED = "enrolled"


async def list_enrolled(client: CadenceClient) -> list[Device]:
    return [d for d in await client.list_devices() if d.status == ENROLLED]


async def read_devices(client: CadenceClient, listed: Iterable[Device]) -> dict[str, CadenceDevice]:
    """Each listed device read whole, by id; one deleted meanwhile is left out."""
    devices = await together(read_device(client, d) for d in listed)
    return {dev.id: dev for dev in devices if dev is not None}


async def read_device(client: CadenceClient, listed: Device) -> CadenceDevice | None:
    """The device's detail, media and vitals; None when the device's own route says it is gone."""
    results = await asyncio.gather(
        client.get_device(listed.id), client.get_media(listed.id), client.get_vitals(listed.id), return_exceptions=True
    )
    detail, media, vitals = results
    # only the device's own route says it is gone: a 404 of another (an older server, a proxy) fails the read
    if isinstance(detail, CadenceApiError) and detail.status == 404 and detail.code == "not_found":
        return None
    if errors := [r for r in results if isinstance(r, BaseException)]:
        raise first_error(errors)
    detail = cast(DeviceDetail, detail)
    return CadenceDevice(
        id=listed.id,
        name=detail.device.name,
        model=detail.device.model,
        online=listed.online,
        commands=detail.commands,
        volume=detail.volume,
        media=cast(MediaSession | None, media),
        vitals=cast(Vitals | None, vitals),
        group=detail.device.group,
        alarms=detail.alarms,
    )


def first_error(errors: list[BaseException]) -> BaseException:
    """A refused token before any other failure."""
    return next((e for e in errors if isinstance(e, CadenceAuthError)), errors[0])


async def together(calls: Iterable[Coroutine[Any, Any, Any]]) -> list[Any]:
    """Runs the calls at once; the first failure cancels the rest and is raised, a refused token before any other."""
    try:
        async with asyncio.TaskGroup() as group:
            tasks = [group.create_task(call) for call in calls]
    except ExceptionGroup as group:
        raise first_error(list(_leaves(group))) from None
    return [task.result() for task in tasks]


def _leaves(group: BaseExceptionGroup) -> Iterator[BaseException]:
    for err in group.exceptions:
        if isinstance(err, BaseExceptionGroup):
            yield from _leaves(err)
        else:
            yield err
