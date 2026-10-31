"""Dispatch model: data types and the feasibility check for BR-45.3 hard constraints.

Pure functions only (no I/O). Times are integer minutes from local midnight of the
dispatch day; travel is integer minutes between location indices.
"""

from __future__ import annotations

from dataclasses import dataclass


@dataclass(frozen=True)
class Technician:
    id: str
    skills: frozenset[str]
    territories: frozenset[str]
    shift_start: int
    shift_end: int
    home: int
    max_daily_minutes: int
    overtime_minutes: int = 0  # allowance above max_daily_minutes; 0 = overtime not allowed


@dataclass(frozen=True)
class WorkOrder:
    id: str
    territory: str
    skills: frozenset[str]
    duration: int
    location: int
    priority: int = 4  # 1 is most urgent
    window_start: int | None = None
    window_end: int | None = None


@dataclass(frozen=True)
class Assignment:
    work_order_id: str
    technician_id: str
    start: int
    end: int
    sequence: int


@dataclass(frozen=True)
class Problem:
    technicians: tuple[Technician, ...]
    work_orders: tuple[WorkOrder, ...]
    travel: tuple[tuple[int, ...], ...]
    buffer_minutes: int = 5

    def __post_init__(self) -> None:
        n = len(self.travel)
        if any(len(row) != n for row in self.travel):
            raise ValueError("travel matrix must be square")
        # Plans and routes are keyed by id; a repeated id would merge two
        # technicians' routes or schedule one work order twice.
        for kind, items in (("technician", self.technicians), ("work order", self.work_orders)):
            ids = [x.id for x in items]
            if len(set(ids)) != len(ids):
                raise ValueError(f"duplicate {kind} id")
        for t in self.technicians:
            if not 0 <= t.home < n:
                raise ValueError(f"technician {t.id} home outside matrix")
        for w in self.work_orders:
            if not 0 <= w.location < n:
                raise ValueError(f"work order {w.id} location outside matrix")
            if w.duration <= 0:
                raise ValueError(f"work order {w.id} duration must be positive")


def route_violations(
    problem: Problem,
    tech: Technician,
    route: list[Assignment],
    wos: dict[str, WorkOrder],
) -> list[str]:
    """Check one technician's route (sorted by sequence) against BR-45.3."""
    out: list[str] = []
    prev_end: int | None = None
    prev_loc = tech.home
    load = 0
    for a in route:
        wo = wos[a.work_order_id]
        if not wo.skills <= tech.skills:
            out.append(f"{wo.id}: {tech.id} lacks skills {sorted(wo.skills - tech.skills)}")
        if wo.territory not in tech.territories:
            out.append(f"{wo.id}: outside territory {wo.territory}")
        if a.end - a.start != wo.duration:
            out.append(f"{wo.id}: duration does not match window of assignment")
        if a.start < tech.shift_start or a.end > tech.shift_end:
            out.append(f"{wo.id}: outside shift of {tech.id}")
        if wo.window_start is not None and a.start < wo.window_start:
            out.append(f"{wo.id}: starts before time window")
        if wo.window_end is not None and a.end > wo.window_end:
            out.append(f"{wo.id}: ends after time window")
        base = tech.shift_start if prev_end is None else prev_end
        travel_in = problem.travel[prev_loc][wo.location]
        if a.start < base + travel_in + problem.buffer_minutes:
            out.append(f"{wo.id}: starts before travel from previous stop plus buffer")
        load += wo.duration + travel_in
        prev_end = a.end
        prev_loc = wo.location
    if route:
        load += problem.travel[prev_loc][tech.home]
    if load > tech.max_daily_minutes + tech.overtime_minutes:
        out.append(f"{tech.id}: daily load {load} exceeds limit")
    return out


def plan_violations(problem: Problem, assignments: list[Assignment]) -> list[str]:
    """Return every BR-45.3 violation in a plan; an empty list means feasible."""
    wos = {w.id: w for w in problem.work_orders}
    techs = {t.id: t for t in problem.technicians}
    out: list[str] = []
    seen: set[str] = set()
    by_tech: dict[str, list[Assignment]] = {}
    for a in assignments:
        if a.work_order_id not in wos:
            out.append(f"unknown work order {a.work_order_id}")
            continue
        if a.technician_id not in techs:
            out.append(f"unknown technician {a.technician_id}")
            continue
        if a.work_order_id in seen:
            out.append(f"{a.work_order_id} assigned more than once")
        seen.add(a.work_order_id)
        by_tech.setdefault(a.technician_id, []).append(a)
    for tid, items in by_tech.items():
        route = sorted(items, key=lambda a: a.sequence)
        if len({a.sequence for a in route}) != len(route):
            out.append(f"{tid}: duplicate sequence numbers")
            continue
        out.extend(route_violations(problem, techs[tid], route, wos))
    return out
