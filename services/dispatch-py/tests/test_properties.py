"""Property tests: the greedy solver is always feasible and keeps pinned assignments (AC-45.4, AC-45.5).

Locations are integer points in a plane and travel is the ceiling of the Euclidean
distance, so the triangle inequality holds. That lets pinned whole routes stay
feasible when other routes change.
"""

from __future__ import annotations

import math

from hypothesis import given, settings
from hypothesis import strategies as st

from passage_dispatch import Problem, Technician, WorkOrder, plan_violations, solve_greedy

SKILLS = ["hvac", "elec", "plumb"]
TERRITORIES = ["north", "south"]


@st.composite
def problems(draw) -> Problem:
    n_loc = draw(st.integers(min_value=2, max_value=8))
    pts = draw(
        st.lists(
            st.tuples(st.integers(0, 100), st.integers(0, 100)),
            min_size=n_loc,
            max_size=n_loc,
        )
    )
    travel = tuple(tuple(math.ceil(math.dist(p, q)) for q in pts) for p in pts)

    techs = []
    for i in range(draw(st.integers(min_value=1, max_value=4))):
        shift_start = draw(st.integers(360, 600))
        techs.append(
            Technician(
                id=f"T{i}",
                skills=frozenset(draw(st.lists(st.sampled_from(SKILLS), max_size=3, unique=True))),
                territories=frozenset(
                    draw(st.lists(st.sampled_from(TERRITORIES), min_size=1, max_size=2, unique=True))
                ),
                shift_start=shift_start,
                shift_end=shift_start + draw(st.integers(240, 600)),
                home=draw(st.integers(0, n_loc - 1)),
                max_daily_minutes=draw(st.integers(240, 600)),
                overtime_minutes=draw(st.sampled_from([0, 60, 120])),
            )
        )

    wos = []
    for j in range(draw(st.integers(min_value=0, max_value=10))):
        window_start = None
        window_end = None
        if draw(st.booleans()):
            window_start = draw(st.integers(360, 900))
            window_end = window_start + draw(st.integers(60, 480))
        wos.append(
            WorkOrder(
                id=f"W{j}",
                territory=draw(st.sampled_from(TERRITORIES)),
                skills=frozenset(draw(st.lists(st.sampled_from(SKILLS), max_size=2, unique=True))),
                duration=draw(st.integers(15, 180)),
                location=draw(st.integers(0, n_loc - 1)),
                priority=draw(st.integers(1, 4)),
                window_start=window_start,
                window_end=window_end,
            )
        )
    return Problem(technicians=tuple(techs), work_orders=tuple(wos), travel=travel)


@settings(max_examples=300, deadline=None)
@given(problems())
def test_greedy_plan_is_feasible_and_partitions_work_orders(problem: Problem):
    assignments, unassigned = solve_greedy(problem)
    assert plan_violations(problem, assignments) == []
    ids = [a.work_order_id for a in assignments] + unassigned
    assert sorted(ids) == sorted(w.id for w in problem.work_orders)


@settings(max_examples=300, deadline=None)
@given(problems(), st.data())
def test_pinned_routes_are_unchanged_on_resolve(problem: Problem, data):
    first, _ = solve_greedy(problem)
    techs = sorted({a.technician_id for a in first})
    if not techs:
        return
    keep = set(data.draw(st.lists(st.sampled_from(techs), unique=True)))
    pinned = [a for a in first if a.technician_id in keep]

    second, unassigned = solve_greedy(problem, pinned=pinned)
    assert plan_violations(problem, second) == []
    assert set(pinned) <= set(second)
    assert not (set(unassigned) & {a.work_order_id for a in pinned})
