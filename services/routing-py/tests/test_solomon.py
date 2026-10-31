"""Solomon VRPTW benchmarks for tms_optimizer.vrp (AC-34.1, issue #34).

AC-34.1: given Solomon C101, R101 and RC101 (100 customers), optimized with a 60 s
limit, every route satisfies capacity and time windows and total distance is within
10 % of best-known. The PRD's fixture list adds C201 and R201, which are run too.

The instances in testdata/solomon are the original files from the SINTEF VRPTW
benchmark page (https://www.sintef.no/projectweb/top/vrptw/, "100 customers").
They are solved by the production `solve` with its production search settings
(PARALLEL_CHEAPEST_INSERTION + GUIDED_LOCAL_SEARCH, BR-34.1) and default costs: the
coordinates are km at 60 km/h so a minute is a distance unit as in Solomon, the
depot due time bounds each route, and the vehicle count and capacity are the
instance's. Note that the solver rounds every travel time up to whole minutes,
which only makes its schedules more conservative than Solomon's real-valued ones.

Nothing below trusts the solver's own report: feasibility is re-simulated from the
route order with real Euclidean travel times (service starts at max(arrival, ready
time), must start by the due date, every route back at the depot by its due time,
load within capacity, every customer served exactly once, none dropped), and the
distance is recomputed from the coordinates.

Best-known distances are the SINTEF table's (hierarchical objective: vehicles
first, then distance). For R201 that solution uses 4 vehicles; distance alone can
go lower with more vehicles, so a result below the reference is not suspicious.
"""

from __future__ import annotations

import math
import os
from concurrent.futures import ProcessPoolExecutor
from pathlib import Path

import pytest

from tms_optimizer.vrp import Point, Problem, Stop, Vehicle, solve

SOLOMON = Path(__file__).resolve().parent.parent / "testdata" / "solomon"
TIME_LIMIT_S = 60  # AC-34.1

# instance -> (best-known distance, vehicles), SINTEF "Solomon benchmark, 100 customers".
BEST_KNOWN = {
    "c101": (828.94, 10),
    "r101": (1650.80, 19),
    "rc101": (1696.95, 14),
    "c201": (591.56, 3),
    "r201": (1252.37, 4),
}


def parse(name: str):
    lines = (SOLOMON / f"{name}.txt").read_text().splitlines()
    vehicles, capacity = map(int, lines[4].split())
    rows = [list(map(int, ln.split())) for ln in lines[9:] if ln.strip()]
    assert rows[0][0] == 0 and len(rows) == 101, f"{name}: depot plus 100 customers"
    return vehicles, capacity, rows


def problem_for(name: str) -> Problem:
    vehicles, capacity, rows = parse(name)
    depot = rows[0]
    stops = tuple(
        Stop(id=str(r[0]), point=Point(r[1], r[2]), earliest=r[4], latest=r[5], service_min=r[6],
             weight_kg=r[3], volume_l=0, pallets=0)
        for r in rows[1:]
    )
    fleet = tuple(
        Vehicle(id=f"V{k:02d}", capacity_kg=capacity, capacity_l=1, pallets=1, max_route_min=depot[5])
        for k in range(vehicles)
    )
    return Problem(depot=Point(depot[1], depot[2]), stops=stops, vehicles=fleet, speed_kmh=60.0,
                   time_limit_s=TIME_LIMIT_S, max_stops=len(stops))


def _solve(name: str):
    sol = solve(problem_for(name))
    return [[s.id for s in r.stops] for r in sol.routes], list(sol.dropped)


@pytest.fixture(scope="module")
def solutions() -> dict[str, tuple[list[list[str]], list[str]]]:
    """Solve the five instances side by side; each solve keeps its full 60 s."""
    with ProcessPoolExecutor(max_workers=min(len(BEST_KNOWN), os.cpu_count() or 1)) as pool:
        return dict(zip(BEST_KNOWN, pool.map(_solve, BEST_KNOWN)))


def check(name: str, routes: list[list[str]], dropped: list[str]) -> float:
    """Re-simulate the routes with Solomon's rules; return the total distance."""
    _, capacity, rows = parse(name)
    by_id = {str(r[0]): r for r in rows}
    depot = rows[0]

    def dist(a, b) -> float:
        return math.hypot(a[1] - b[1], a[2] - b[2])

    assert dropped == [], f"{name}: dropped {dropped}"
    served = [c for r in routes for c in r]
    assert sorted(served, key=int) == [str(k) for k in range(1, 101)], f"{name}: every customer exactly once"
    total = 0.0
    for route in routes:
        load, clock, prev = 0, 0.0, depot
        for cid in route:
            c = by_id[cid]
            load += c[3]
            clock += dist(prev, c)
            total += dist(prev, c)
            start = max(clock, c[4])
            assert start <= c[5] + 1e-9, f"{name}: customer {cid} starts {start:.2f} after due {c[5]}"
            clock = start + c[6]
            prev = c
        total += dist(prev, depot)
        clock += dist(prev, depot)
        assert load <= capacity, f"{name}: load {load} over capacity {capacity}"
        assert clock <= depot[5] + 1e-9, f"{name}: back at the depot at {clock:.2f} after {depot[5]}"
    return total


@pytest.mark.parametrize("name", list(BEST_KNOWN))
def test_solomon_feasible_and_within_10_percent_of_best_known(name: str, solutions) -> None:
    routes, dropped = solutions[name]
    distance = check(name, routes, dropped)
    best, best_vehicles = BEST_KNOWN[name]
    gap = (distance - best) / best * 100
    print(f"{name}: {len(routes)} routes, distance {distance:.2f}, best known {best} ({best_vehicles} vehicles), gap {gap:+.2f} %")
    assert distance <= best * 1.10, f"{name}: distance {distance:.2f} is {gap:.2f} % above best known {best}"


def test_checker_rejects_a_late_route() -> None:
    """The independent checker is not vacuous: C101's customer 1 (ready 912, due 967)
    served first leaves the depot, travels and then waits; serving customer 1 after
    customer 2 (due 870) is fine, but customer 2 after customer 1 is too late."""
    with pytest.raises(AssertionError, match="customer 2 starts"):
        check("c101", [["1", "2"], [str(k) for k in range(3, 101)]], [])
