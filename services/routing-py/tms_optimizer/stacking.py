"""Layer-based load stacking for a trailer or container (FR-34.18).

Pallets are grouped into floor stacks by first-fit decreasing on footprint. A
pallet goes on top of a stack when the stack's top pallet is stackable, its
footprint fits within the base, the stack stays within the trailer height and
the base's stack weight limit, and its stop is not later than the stop of the
pallet below it: a pallet for an earlier stop never sits under one for a later
stop, so each stop unloads from the top (load sequence in reverse stop order).
Non-stackable pallets are never stacked on.

Floor stacks are laid out from the nose (y = 0) towards the rear door in rows
across the trailer width, latest stop first, so the first stop's pallets are at
the door. The load sequence lists the pallets in that order, each stack bottom
to top. The axle-weight estimate treats the floor as a beam on two supports, the
kingpin and the rear axle group.

Pallets are loaded as given (no rotation search); DG segregation inside the
trailer and free-form 3D carton packing are out of scope. Every pallet is either
placed exactly once or reported in ``unplaced``; nothing is dropped silently.
"""

from __future__ import annotations

from collections.abc import Sequence
from dataclasses import dataclass


@dataclass(frozen=True)
class Trailer:
    length_mm: int
    width_mm: int
    max_height_mm: int
    max_weight_kg: int
    # Support points of the axle estimate, from the nose. 0 for axle_mm means
    # the rear door (the trailer length).
    kingpin_mm: int = 0
    axle_mm: int = 0


@dataclass(frozen=True)
class Pallet:
    id: str
    length_mm: int
    width_mm: int
    height_mm: int
    weight_kg: int
    stackable: bool
    # Position of the pallet's stop on the route. Later stops are unloaded
    # last, so they are loaded first.
    stop_seq: int = 0
    # Maximum total weight of a stack built on this pallet as its base.
    max_stack_kg: int | None = None


@dataclass(frozen=True)
class Placement:
    pallet_ids: tuple[str, ...]  # bottom to top
    x_mm: int
    y_mm: int
    length_mm: int
    width_mm: int
    height_mm: int
    weight_kg: int


@dataclass(frozen=True)
class LoadPlan:
    placements: tuple[Placement, ...]
    unplaced: tuple[str, ...]
    floor_positions_used: int
    length_used_mm: int
    layers: int
    total_weight_kg: int
    load_sequence: tuple[str, ...]
    kingpin_kg: int
    axle_kg: int


class _Stack:
    """A floor position with the pallets stacked on it, bottom first."""

    def __init__(self, base: Pallet, trailer: Trailer) -> None:
        self.base = base
        self.members: list[Pallet] = [base]
        self.height = base.height_mm
        self.weight = base.weight_kg
        cap = base.max_stack_kg if base.max_stack_kg is not None else trailer.max_weight_kg
        self.limit = min(cap, trailer.max_weight_kg)

    def fits(self, p: Pallet, trailer: Trailer) -> bool:
        top = self.members[-1]
        return (
            top.stackable
            and p.stop_seq <= top.stop_seq
            and p.length_mm <= self.base.length_mm
            and p.width_mm <= self.base.width_mm
            and self.height + p.height_mm <= trailer.max_height_mm
            and self.weight + p.weight_kg <= self.limit
        )

    def add(self, p: Pallet) -> None:
        self.members.append(p)
        self.height += p.height_mm
        self.weight += p.weight_kg


def _validate(trailer: Trailer, pallets: Sequence[Pallet]) -> None:
    if min(trailer.length_mm, trailer.width_mm, trailer.max_height_mm, trailer.max_weight_kg) <= 0:
        raise ValueError("trailer dimensions and limits must be positive")
    axle = trailer.axle_mm or trailer.length_mm
    if not 0 <= trailer.kingpin_mm < axle <= trailer.length_mm:
        raise ValueError("kingpin and axle must lie on the trailer, axle behind the kingpin")
    ids = [p.id for p in pallets]
    if len(set(ids)) != len(ids):
        raise ValueError("duplicate pallet id")
    for p in pallets:
        if min(p.length_mm, p.width_mm, p.height_mm) <= 0 or p.weight_kg < 0:
            raise ValueError(f"pallet {p.id} has invalid dimensions or weight")
        if p.stop_seq < 0 or (p.max_stack_kg is not None and p.max_stack_kg < 0):
            raise ValueError(f"pallet {p.id} has an invalid stop or stack limit")


def _axle_estimate(trailer: Trailer, placements: Sequence[Placement]) -> tuple[int, int]:
    """Split the weight between kingpin and rear axle as a beam on two supports.

    Weight ahead of the kingpin or behind the axle is taken by the nearer
    support, so neither share is ever negative.
    """
    kingpin = trailer.kingpin_mm
    axle = trailer.axle_mm or trailer.length_mm
    span = axle - kingpin
    moment = 0
    total = 0
    for pl in placements:
        centre = pl.y_mm + pl.length_mm / 2
        lever = min(max(centre - kingpin, 0), span)
        moment += pl.weight_kg * lever
        total += pl.weight_kg
    axle_kg = round(moment / span) if span else 0
    return total - axle_kg, axle_kg


def plan_load(trailer: Trailer, pallets: Sequence[Pallet]) -> LoadPlan:
    """Stack the pallets onto the trailer floor and return the layered plan."""
    _validate(trailer, pallets)
    order = sorted(pallets, key=lambda p: (-(p.length_mm * p.width_mm), -p.stop_seq, p.id))

    stacks: list[_Stack] = []
    unplaced: list[str] = []
    for p in order:
        if p.height_mm > trailer.max_height_mm or p.weight_kg > trailer.max_weight_kg:
            unplaced.append(p.id)
            continue
        host = next((s for s in stacks if s.fits(p, trailer)), None)
        if host is None:
            stacks.append(_Stack(p, trailer))
        else:
            host.add(p)

    # Latest stop at the nose; a stack's base carries its latest stop.
    stacks.sort(key=lambda s: (-s.base.stop_seq, -(s.base.length_mm * s.base.width_mm), s.base.id))

    placements: list[Placement] = []
    x = 0
    y = 0
    shelf_len = 0
    length_used = 0
    weight_total = 0
    for s in stacks:
        width, length = s.base.width_mm, s.base.length_mm
        nx, ny, nshelf = x, y, shelf_len
        if nx + width > trailer.width_mm:
            nx, ny, nshelf = 0, y + shelf_len, 0
        if (
            width > trailer.width_mm
            or ny + length > trailer.length_mm
            or weight_total + s.weight > trailer.max_weight_kg
        ):
            unplaced.extend(m.id for m in s.members)
            continue
        placements.append(
            Placement(tuple(m.id for m in s.members), nx, ny, length, width, s.height, s.weight)
        )
        x, y, shelf_len = nx + width, ny, max(nshelf, length)
        length_used = max(length_used, ny + length)
        weight_total += s.weight

    kingpin_kg, axle_kg = _axle_estimate(trailer, placements)
    return LoadPlan(
        placements=tuple(placements),
        unplaced=tuple(sorted(unplaced)),
        floor_positions_used=len(placements),
        length_used_mm=length_used,
        layers=max((len(pl.pallet_ids) for pl in placements), default=0),
        total_weight_kg=weight_total,
        load_sequence=tuple(pid for pl in placements for pid in pl.pallet_ids),
        kingpin_kg=kingpin_kg,
        axle_kg=axle_kg,
    )
