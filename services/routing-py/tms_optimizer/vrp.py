"""Capacitated vehicle routing with time windows on OR-Tools (BR-34.1).

Slice 1 subset: vehicle capacity in weight, volume and pallets (hard), stop
time windows with a soft lateness penalty up to 60 minutes and a hard bound
beyond that, maximum route duration, maximum stops per route, heterogeneous
fixed vehicle cost, and dropped stops reported with a penalty instead of
being silently discarded.

Every term of the objective is money in minor units (cents), so the drop
penalty, lateness penalty, fixed vehicle cost and distance cost compare on
one scale (BR-34.1 item 9): a stop is dropped only when serving it would cost
more than DROP_PENALTY, never because metres and dollars were mixed. Pickup/delivery precedence, driver-hours rule sets
and equipment compatibility are later slices (see services/transport
NOT-IMPLEMENTED.md).
"""

from __future__ import annotations

import math
from dataclasses import dataclass

from ortools.constraint_solver import pywrapcp, routing_enums_pb2

DROP_PENALTY = 10_000
"""Penalty for an unserved stop, in money units (BR-34.1)."""
MAX_STOPS_PER_ROUTE = 25
DEFAULT_TIME_LIMIT_S = 60
DEFAULT_LATENESS_PER_MIN = 50
"""Soft lateness penalty per minute, in money units (lateness_penalty_per_min)."""
DEFAULT_COST_PER_KM = 1
"""Distance cost per km, in money units."""
DEFAULT_COST_PER_HOUR = 0
"""Time cost per hour of travel and service, in money units."""
MINOR = 100
"""Minor units per money unit; the solver works in integer cents."""
HARD_LATENESS_MIN = 60
DEFAULT_MAX_ROUTE_MIN = 11 * 60


class NoSolutionError(RuntimeError):
    """The solver returned no solution within the time limit."""


@dataclass(frozen=True)
class Point:
    x: float
    y: float


@dataclass(frozen=True)
class Stop:
    id: str
    point: Point
    earliest: int
    latest: int
    service_min: int
    weight_kg: int
    volume_l: int
    pallets: int


@dataclass(frozen=True)
class Vehicle:
    id: str
    capacity_kg: int
    capacity_l: int
    pallets: int
    fixed_cost: int = 0
    """Money units per vehicle used."""
    max_route_min: int = DEFAULT_MAX_ROUTE_MIN


@dataclass(frozen=True)
class Problem:
    depot: Point
    stops: tuple[Stop, ...]
    vehicles: tuple[Vehicle, ...]
    speed_kmh: float = 60.0
    lateness_per_min: int = DEFAULT_LATENESS_PER_MIN
    cost_per_km: float = DEFAULT_COST_PER_KM
    cost_per_hour: float = DEFAULT_COST_PER_HOUR
    time_limit_s: int = DEFAULT_TIME_LIMIT_S
    max_stops: int = MAX_STOPS_PER_ROUTE


@dataclass(frozen=True)
class RouteStop:
    id: str
    arrival_min: int
    late_min: int


@dataclass(frozen=True)
class Route:
    vehicle_id: str
    stops: tuple[RouteStop, ...]
    distance_km: float
    end_min: int


@dataclass(frozen=True)
class Solution:
    routes: tuple[Route, ...]
    dropped: tuple[str, ...]


def km_between(a: Point, b: Point) -> float:
    return math.hypot(a.x - b.x, a.y - b.y)


def travel_min(a: Point, b: Point, speed_kmh: float) -> int:
    """Travel minutes, rounded up so that the solver never under-estimates time."""
    return math.ceil(km_between(a, b) / speed_kmh * 60)


def solve(problem: Problem) -> Solution:
    """Solve the routing problem and return routes plus explicitly dropped stops."""
    if not problem.vehicles:
        raise ValueError("at least one vehicle is required")
    points = [problem.depot, *(s.point for s in problem.stops)]
    n = len(points)
    vehicles = problem.vehicles
    nv = len(vehicles)
    travel = [[travel_min(points[i], points[j], problem.speed_kmh) for j in range(n)] for i in range(n)]
    # Distances in metres as integers keep the objective exact for the solver.
    metres = [[round(km_between(points[i], points[j]) * 1000) for j in range(n)] for i in range(n)]
    service = [0, *(s.service_min for s in problem.stops)]
    weight = [0, *(s.weight_kg for s in problem.stops)]
    volume = [0, *(s.volume_l for s in problem.stops)]
    pallets = [0, *(s.pallets for s in problem.stops)]
    count = [0, *([1] * len(problem.stops))]

    manager = pywrapcp.RoutingIndexManager(n, nv, [0] * nv, [0] * nv)
    routing = pywrapcp.RoutingModel(manager)

    # Arc cost in cents: distance cost plus time cost for the leg and the
    # service at its origin.
    km_cents = problem.cost_per_km * MINOR
    min_cents = problem.cost_per_hour * MINOR / 60
    arc = [
        [round(metres[i][j] * km_cents / 1000 + (travel[i][j] + service[i]) * min_cents) for j in range(n)]
        for i in range(n)
    ]

    def arc_cost(i: int, j: int) -> int:
        return arc[manager.IndexToNode(i)][manager.IndexToNode(j)]

    arc_idx = routing.RegisterTransitCallback(arc_cost)
    routing.SetArcCostEvaluatorOfAllVehicles(arc_idx)
    for v, veh in enumerate(vehicles):
        routing.SetFixedCostOfVehicle(veh.fixed_cost * MINOR, v)

    def time_cost(i: int, j: int) -> int:
        a, b = manager.IndexToNode(i), manager.IndexToNode(j)
        return travel[a][b] + service[a]

    time_idx = routing.RegisterTransitCallback(time_cost)
    horizon = max(
        max(s.latest + HARD_LATENESS_MIN for s in problem.stops) if problem.stops else 0,
        max(v.max_route_min for v in vehicles),
    )
    routing.AddDimension(time_idx, horizon, horizon, True, "Time")
    time_dim = routing.GetDimensionOrDie("Time")

    for k, stop in enumerate(problem.stops):
        index = manager.NodeToIndex(k + 1)
        time_dim.CumulVar(index).SetRange(stop.earliest, stop.latest + HARD_LATENESS_MIN)
        time_dim.SetCumulVarSoftUpperBound(index, stop.latest, problem.lateness_per_min * MINOR)
        routing.AddDisjunction([index], DROP_PENALTY * MINOR)

    for v, veh in enumerate(vehicles):
        time_dim.CumulVar(routing.End(v)).SetMax(veh.max_route_min)

    _add_capacity(routing, manager, "WeightKg", weight, [v.capacity_kg for v in vehicles])
    _add_capacity(routing, manager, "VolumeL", volume, [v.capacity_l for v in vehicles])
    _add_capacity(routing, manager, "Pallets", pallets, [v.pallets for v in vehicles])
    _add_capacity(routing, manager, "Stops", count, [problem.max_stops] * nv)

    params = pywrapcp.DefaultRoutingSearchParameters()
    params.first_solution_strategy = routing_enums_pb2.FirstSolutionStrategy.PARALLEL_CHEAPEST_INSERTION
    params.local_search_metaheuristic = routing_enums_pb2.LocalSearchMetaheuristic.GUIDED_LOCAL_SEARCH
    params.time_limit.seconds = problem.time_limit_s
    solution = routing.SolveWithParameters(params)
    if solution is None:
        raise NoSolutionError("no solution within the time limit")

    routes: list[Route] = []
    for v, veh in enumerate(vehicles):
        index = routing.Start(v)
        seq: list[RouteStop] = []
        prev = points[0]
        distance = 0.0
        while not routing.IsEnd(index):
            node = manager.IndexToNode(index)
            if node != 0:
                stop = problem.stops[node - 1]
                arrival = solution.Value(time_dim.CumulVar(index))
                seq.append(RouteStop(stop.id, arrival, max(0, arrival - stop.latest)))
                distance += km_between(prev, stop.point)
                prev = stop.point
            index = solution.Value(routing.NextVar(index))
        if seq:
            distance += km_between(prev, problem.depot)
            routes.append(
                Route(veh.id, tuple(seq), round(distance, 3), solution.Value(time_dim.CumulVar(index)))
            )

    dropped = tuple(
        s.id
        for k, s in enumerate(problem.stops)
        if solution.Value(routing.ActiveVar(manager.NodeToIndex(k + 1))) == 0
    )
    return Solution(routes=tuple(routes), dropped=dropped)


def _add_capacity(routing, manager, name: str, demand: list[int], capacities: list[int]) -> None:
    def demand_cb(index: int) -> int:
        return demand[manager.IndexToNode(index)]

    idx = routing.RegisterUnaryTransitCallback(demand_cb)
    routing.AddDimensionWithVehicleCapacity(idx, 0, capacities, True, name)
