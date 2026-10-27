"""VRP feasibility tests for tms_optimizer.vrp (BR-34.1)."""

from __future__ import annotations

import json
import random
from pathlib import Path

from tms_optimizer.vrp import (
    HARD_LATENESS_MIN,
    MAX_STOPS_PER_ROUTE,
    Point,
    Problem,
    Stop,
    Vehicle,
    solve,
)

FIXTURES = Path(__file__).resolve().parent.parent / "testdata"


def load_fixture(name: str) -> Problem:
    raw = json.loads((FIXTURES / name).read_text())
    return Problem(
        depot=Point(raw["depot"]["x"], raw["depot"]["y"]),
        stops=tuple(
            Stop(
                id=s["id"],
                point=Point(s["x"], s["y"]),
                earliest=s["earliest"],
                latest=s["latest"],
                service_min=s["service_min"],
                weight_kg=s["weight_kg"],
                volume_l=s["volume_l"],
                pallets=s["pallets"],
            )
            for s in raw["stops"]
        ),
        vehicles=tuple(
            Vehicle(
                id=v["id"],
                capacity_kg=v["capacity_kg"],
                capacity_l=v["capacity_l"],
                pallets=v["pallets"],
                fixed_cost=v["fixed_cost"],
                max_route_min=v["max_route_min"],
            )
            for v in raw["vehicles"]
        ),
        speed_kmh=raw["speed_kmh"],
        time_limit_s=5,
    )


def assert_feasible(problem: Problem, sol) -> None:
    by_id = {s.id: s for s in problem.stops}
    vehicles = {v.id: v for v in problem.vehicles}
    served: list[str] = []
    for route in sol.routes:
        veh = vehicles[route.vehicle_id]
        assert len(route.stops) <= MAX_STOPS_PER_ROUTE
        assert sum(by_id[r.id].weight_kg for r in route.stops) <= veh.capacity_kg
        assert sum(by_id[r.id].volume_l for r in route.stops) <= veh.capacity_l
        assert sum(by_id[r.id].pallets for r in route.stops) <= veh.pallets
        assert route.end_min <= veh.max_route_min
        for stop in route.stops:
            spec = by_id[stop.id]
            assert stop.arrival_min >= spec.earliest
            assert stop.arrival_min <= spec.latest + HARD_LATENESS_MIN
            assert stop.late_min == max(0, stop.arrival_min - spec.latest)
            served.append(stop.id)
    assert sorted(served + list(sol.dropped)) == sorted(by_id), "every stop is served once or dropped"


def test_small_fixture_is_fully_served_and_feasible() -> None:
    problem = load_fixture("small-6.json")
    sol = solve(problem)
    assert sol.dropped == ()
    assert_feasible(problem, sol)
    assert sum(len(r.stops) for r in sol.routes) == len(problem.stops)


def test_unreachable_stop_is_reported_as_dropped() -> None:
    problem = load_fixture("small-6.json")
    far = Stop(
        id="FAR",
        point=Point(500, 500),
        earliest=0,
        latest=5,
        service_min=10,
        weight_kg=10,
        volume_l=10,
        pallets=0,
    )
    problem = Problem(
        depot=problem.depot,
        stops=problem.stops + (far,),
        vehicles=problem.vehicles,
        speed_kmh=problem.speed_kmh,
        time_limit_s=3,
    )
    sol = solve(problem)
    assert "FAR" in sol.dropped
    assert_feasible(problem, sol)


def test_random_instances_are_feasible() -> None:
    rng = random.Random(34)
    for _ in range(3):
        stops = []
        for k in range(20):
            earliest = rng.randint(0, 200)
            stops.append(
                Stop(
                    id=f"R{k}",
                    point=Point(rng.uniform(-30, 30), rng.uniform(-30, 30)),
                    earliest=earliest,
                    latest=earliest + rng.randint(60, 240),
                    service_min=rng.randint(5, 20),
                    weight_kg=rng.randint(50, 400),
                    volume_l=rng.randint(100, 900),
                    pallets=rng.randint(0, 2),
                )
            )
        problem = Problem(
            depot=Point(0, 0),
            stops=tuple(stops),
            vehicles=tuple(
                Vehicle(id=f"V{v}", capacity_kg=1500, capacity_l=4000, pallets=8) for v in range(4)
            ),
            time_limit_s=2,
        )
        assert_feasible(problem, solve(problem))


def test_distant_feasible_stop_is_served_not_dropped() -> None:
    """A 150 km detour costs 300 money units at 1 per km, far below the 10,000
    drop penalty, so the solver must serve the stop (BR-34.1: drop only when
    infeasible). This guards against mixing metres with money in the objective."""
    far = Stop(id="FAR", point=Point(150, 0), earliest=0, latest=600, service_min=10, weight_kg=10, volume_l=10, pallets=0)
    problem = Problem(
        depot=Point(0, 0),
        stops=(far,),
        vehicles=(Vehicle(id="V1", capacity_kg=1000, capacity_l=3000, pallets=6),),
        time_limit_s=2,
    )
    sol = solve(problem)
    assert sol.dropped == ()
    assert [s.id for r in sol.routes for s in r.stops] == ["FAR"]
    assert_feasible(problem, sol)


def test_capacity_splits_stops_across_vehicles() -> None:
    """Six stops of 400 kg cannot fit one 1,000 kg vehicle: three vehicles are needed."""
    stops = tuple(
        Stop(id=f"S{k}", point=Point(k, 1), earliest=0, latest=600, service_min=5, weight_kg=400, volume_l=10, pallets=0)
        for k in range(6)
    )
    problem = Problem(
        depot=Point(0, 0),
        stops=stops,
        vehicles=tuple(Vehicle(id=f"V{v}", capacity_kg=1000, capacity_l=3000, pallets=6) for v in range(3)),
        time_limit_s=2,
    )
    sol = solve(problem)
    assert sol.dropped == ()
    assert len(sol.routes) == 3
    assert_feasible(problem, sol)
