"""Layer-based load stacking tests (FR-34.18, AC-34.18)."""

from __future__ import annotations

import pytest
from hypothesis import given, settings
from hypothesis import strategies as st

from tms_optimizer.stacking import Pallet, Trailer, plan_load

TRAILER_53 = Trailer(length_mm=13_600, width_mm=2_480, max_height_mm=2_700, max_weight_kg=20_000)


def fixture_24() -> list[Pallet]:
    """24 pallets for a 53-ft trailer; every fourth pallet is non-stackable (6 total)."""
    return [
        Pallet(
            id=f"P{k:02d}",
            length_mm=1_200,
            width_mm=1_000,
            height_mm=900,
            weight_kg=600,
            stackable=k % 4 != 0,
            stop_seq=k % 6,
        )
        for k in range(24)
    ]


def test_ac_34_18_no_pallet_stacked_on_non_stackable() -> None:
    pallets = fixture_24()
    by_id = {p.id: p for p in pallets}
    plan = plan_load(TRAILER_53, pallets)
    assert plan.unplaced == ()
    placed = [pid for pl in plan.placements for pid in pl.pallet_ids]
    assert sorted(placed) == sorted(by_id)
    for pl in plan.placements:
        for below in pl.pallet_ids[:-1]:
            assert by_id[below].stackable, f"{below} carries a pallet but is not stackable"
    assert 0 < plan.floor_positions_used < 24
    assert plan.layers >= 2
    # Each stop unloads from the top: no pallet for a later stop is under one
    # for an earlier stop.
    for pl in plan.placements:
        seqs = [by_id[pid].stop_seq for pid in pl.pallet_ids]
        assert seqs == sorted(seqs, reverse=True), pl


def test_load_sequence_is_reverse_stop_order_from_the_nose() -> None:
    pallets = [
        Pallet(id="A", length_mm=1_200, width_mm=1_000, height_mm=900, weight_kg=100, stackable=False, stop_seq=1),
        Pallet(id="B", length_mm=1_200, width_mm=1_000, height_mm=900, weight_kg=100, stackable=False, stop_seq=3),
        Pallet(id="C", length_mm=1_200, width_mm=1_000, height_mm=900, weight_kg=100, stackable=False, stop_seq=2),
    ]
    plan = plan_load(TRAILER_53, pallets)
    assert plan.load_sequence == ("B", "C", "A")
    by_base = {pl.pallet_ids[0]: pl for pl in plan.placements}
    # The last stop is at the nose, the first at the door.
    assert (by_base["B"].y_mm, by_base["B"].x_mm) <= (by_base["C"].y_mm, by_base["C"].x_mm)
    assert (by_base["C"].y_mm, by_base["C"].x_mm) <= (by_base["A"].y_mm, by_base["A"].x_mm)


def test_later_stop_never_stacked_on_earlier_stop() -> None:
    # The small pallet for stop 3 would fit on the large one for stop 1, but it
    # would then have to come off before stop 1 unloads.
    pallets = [
        Pallet(id="BIG", length_mm=1_200, width_mm=1_000, height_mm=900, weight_kg=300, stackable=True, stop_seq=1),
        Pallet(id="SMALL", length_mm=800, width_mm=600, height_mm=900, weight_kg=100, stackable=True, stop_seq=3),
    ]
    plan = plan_load(TRAILER_53, pallets)
    assert plan.floor_positions_used == 2
    assert plan.load_sequence == ("SMALL", "BIG")
    # Reversed stops do stack: the stop-1 pallet on top of the stop-3 one.
    pallets = [
        Pallet(id="BIG", length_mm=1_200, width_mm=1_000, height_mm=900, weight_kg=300, stackable=True, stop_seq=3),
        Pallet(id="SMALL", length_mm=800, width_mm=600, height_mm=900, weight_kg=100, stackable=True, stop_seq=1),
    ]
    plan = plan_load(TRAILER_53, pallets)
    assert [pl.pallet_ids for pl in plan.placements] == [("BIG", "SMALL")]
    assert plan.load_sequence == ("BIG", "SMALL")


def test_axle_estimate_splits_weight_by_position() -> None:
    trailer = Trailer(length_mm=12_000, width_mm=1_000, max_height_mm=2_700, max_weight_kg=20_000)
    nose = Pallet(id="N", length_mm=1_000, width_mm=1_000, height_mm=900, weight_kg=1_200, stackable=False, stop_seq=2)
    plan = plan_load(trailer, [nose])
    # Centre at 500 mm of a 12,000 mm beam: 1,200 x 500 / 12,000 = 50 kg on the axle.
    assert (plan.kingpin_kg, plan.axle_kg) == (1_150, 50)
    door = Pallet(id="D", length_mm=1_000, width_mm=1_000, height_mm=900, weight_kg=600, stackable=False, stop_seq=1)
    plan = plan_load(trailer, [nose, door])
    # D sits behind N at 1,000 mm: centre 1,500 mm, 600 x 1,500 / 12,000 = 75 kg.
    assert plan.kingpin_kg + plan.axle_kg == plan.total_weight_kg == 1_800
    assert plan.axle_kg == 125
    moved = Trailer(length_mm=12_000, width_mm=1_000, max_height_mm=2_700, max_weight_kg=20_000, kingpin_mm=1_000, axle_mm=11_000)
    plan = plan_load(moved, [nose])
    # Ahead of the kingpin: all on the kingpin.
    assert (plan.kingpin_kg, plan.axle_kg) == (1_200, 0)


def test_weight_limit_of_base_blocks_stacking() -> None:
    pallets = [
        Pallet(id="A", length_mm=1_200, width_mm=1_000, height_mm=900, weight_kg=600, stackable=True, max_stack_kg=1_000),
        Pallet(id="B", length_mm=1_200, width_mm=1_000, height_mm=900, weight_kg=600, stackable=True),
    ]
    plan = plan_load(TRAILER_53, pallets)
    assert plan.floor_positions_used == 2
    assert plan.layers == 1


def test_height_limit_caps_stack_levels() -> None:
    pallets = [
        Pallet(id=f"H{k}", length_mm=1_200, width_mm=1_000, height_mm=1_000, weight_kg=100, stackable=True)
        for k in range(3)
    ]
    plan = plan_load(TRAILER_53, pallets)
    assert plan.layers == 2
    assert plan.floor_positions_used == 2
    assert all(pl.height_mm <= TRAILER_53.max_height_mm for pl in plan.placements)


def test_pallet_taller_than_trailer_is_reported_unplaced() -> None:
    tall = Pallet(id="TALL", length_mm=1_200, width_mm=1_000, height_mm=3_000, weight_kg=100, stackable=False)
    plan = plan_load(TRAILER_53, [tall])
    assert plan.unplaced == ("TALL",)
    assert plan.placements == ()


@pytest.mark.parametrize(
    "pallets",
    [
        [Pallet(id="X", length_mm=1, width_mm=1, height_mm=1, weight_kg=1, stackable=True)] * 2,
        [Pallet(id="X", length_mm=0, width_mm=1, height_mm=1, weight_kg=1, stackable=True)],
        [Pallet(id="X", length_mm=1, width_mm=1, height_mm=1, weight_kg=1, stackable=True, stop_seq=-1)],
    ],
)
def test_invalid_input_is_rejected(pallets: list[Pallet]) -> None:
    with pytest.raises(ValueError):
        plan_load(TRAILER_53, pallets)


@st.composite
def pallet_lists(draw: st.DrawFn) -> list[Pallet]:
    count = draw(st.integers(min_value=0, max_value=40))
    out: list[Pallet] = []
    for i in range(count):
        out.append(
            Pallet(
                id=f"P{i}",
                length_mm=draw(st.integers(min_value=400, max_value=1_400)),
                width_mm=draw(st.integers(min_value=400, max_value=1_300)),
                height_mm=draw(st.integers(min_value=200, max_value=2_000)),
                weight_kg=draw(st.integers(min_value=0, max_value=1_500)),
                stackable=draw(st.booleans()),
                stop_seq=draw(st.integers(min_value=0, max_value=10)),
                max_stack_kg=draw(st.none() | st.integers(min_value=0, max_value=4_000)),
            )
        )
    return out


@settings(max_examples=200, deadline=None)
@given(pallet_lists())
def test_plan_invariants_hold_for_random_loads(pallets: list[Pallet]) -> None:
    plan = plan_load(TRAILER_53, pallets)
    by_id = {p.id: p for p in pallets}
    placed = [pid for pl in plan.placements for pid in pl.pallet_ids]
    # Each pallet is placed exactly once or reported unplaced.
    assert len(placed) == len(set(placed))
    assert sorted(placed + list(plan.unplaced)) == sorted(by_id)
    rects = []
    for pl in plan.placements:
        base = by_id[pl.pallet_ids[0]]
        assert pl.height_mm <= TRAILER_53.max_height_mm
        assert pl.height_mm == sum(by_id[pid].height_mm for pid in pl.pallet_ids)
        assert pl.weight_kg <= TRAILER_53.max_weight_kg
        for below in pl.pallet_ids[:-1]:
            assert by_id[below].stackable
        for top in pl.pallet_ids[1:]:
            assert by_id[top].length_mm <= base.length_mm
            assert by_id[top].width_mm <= base.width_mm
        seqs = [by_id[pid].stop_seq for pid in pl.pallet_ids]
        assert seqs == sorted(seqs, reverse=True)
        assert (pl.length_mm, pl.width_mm) == (base.length_mm, base.width_mm)
        assert pl.x_mm + base.width_mm <= TRAILER_53.width_mm
        assert pl.y_mm + base.length_mm <= TRAILER_53.length_mm
        rects.append((pl.x_mm, pl.y_mm, base.width_mm, base.length_mm))
    for i, a in enumerate(rects):
        for b in rects[i + 1 :]:
            disjoint = (
                a[0] + a[2] <= b[0] or b[0] + b[2] <= a[0] or a[1] + a[3] <= b[1] or b[1] + b[3] <= a[1]
            )
            assert disjoint, f"floor positions overlap: {a} {b}"
    assert plan.total_weight_kg == sum(by_id[pid].weight_kg for pid in placed)
    assert sorted(plan.load_sequence) == sorted(placed)
    assert plan.kingpin_kg >= 0 and plan.axle_kg >= 0
    assert plan.kingpin_kg + plan.axle_kg == plan.total_weight_kg
    # Stacks load bottom first and in placement order.
    assert list(plan.load_sequence) == [pid for pl in plan.placements for pid in pl.pallet_ids]
