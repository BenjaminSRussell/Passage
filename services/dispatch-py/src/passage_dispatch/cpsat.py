from __future__ import annotations

import logging
import time
from collections.abc import Iterable
from dataclasses import dataclass

from .greedy import solve_greedy
from .model import Assignment, Problem, plan_violations
from .objective import (
    OVERTIME_WEIGHT,
    TECHNICIAN_WEIGHT,
    TRAVEL_WEIGHT,
    UNASSIGNED_WEIGHT,
    LATENESS_WEIGHT,
    Cost,
    plan_cost,
    priority_weight,
)

log = logging.getLogger("passage_dispatch")

DEFAULT_TIME_LIMIT_S = 20.0
DEFAULT_WORKERS = 8
MAX_ARCS = 400_000
SOLVED = ("OPTIMAL", "FEASIBLE")

FALLBACK_TIMEOUT = "timeout"
FALLBACK_INFEASIBLE = "infeasible"
FALLBACK_TOO_LARGE = "too_large"
FALLBACK_UNAVAILABLE = "solver_unavailable"
FALLBACK_INVALID_PLAN = "invalid_plan"
FALLBACK_NOT_BETTER = "not_better_than_greedy"
FALLBACK_MODEL_INVALID = "model_invalid"


class ModelTooLarge(Exception):
    pass


@dataclass(frozen=True)
class CpSatOutcome:
    status: str
    assignments: list[Assignment] | None


@dataclass(frozen=True)
class SolveResult:
    assignments: list[Assignment]
    unassigned: list[str]
    solver: str
    cost: Cost
    cpsat_status: str | None
    fallback_reason: str | None
    elapsed_seconds: float


def solve_cpsat(
    problem: Problem,
    pinned: Iterable[Assignment] = (),
    hint: Iterable[Assignment] = (),
    time_limit_s: float = DEFAULT_TIME_LIMIT_S,
    workers: int = DEFAULT_WORKERS,
) -> CpSatOutcome:
    from ortools.sat.python import cp_model

    pin_of = {a.work_order_id: a for a in pinned}
    buffer = problem.buffer_minutes
    model = cp_model.CpModel()
    unassigned = {w.id: model.NewBoolVar(f"un_{w.id}") for w in problem.work_orders}
    placed: dict[str, list] = {w.id: [] for w in problem.work_orders}
    late = {}
    objective = [UNASSIGNED_WEIGHT * priority_weight(w.priority) * unassigned[w.id] for w in problem.work_orders]
    horizon = max([t.shift_end for t in problem.technicians] + [0]) + 1
    for w in problem.work_orders:
        if w.due is not None:
            late[w.id] = model.NewIntVar(0, horizon, f"late_{w.id}")
            objective.append(LATENESS_WEIGHT * late[w.id])

    starts: dict[tuple[str, str], object] = {}
    presence: dict[tuple[str, str], object] = {}
    arc_lit: dict[tuple[str, int, int], object] = {}
    used: dict[str, object] = {}
    nodes: dict[str, list] = {}
    arcs_total = 0

    for t in problem.technicians:
        elig = []
        for w in problem.work_orders:
            pin = pin_of.get(w.id)
            if not w.skills <= t.skills or w.territory not in t.territories or (pin and pin.technician_id != t.id):
                continue
            lo = max(t.shift_start, w.window_start if w.window_start is not None else t.shift_start)
            hi = min(t.shift_end, w.window_end if w.window_end is not None else t.shift_end) - w.duration
            if lo <= hi:
                elig.append((w, lo, hi))
        if not elig:
            continue
        nodes[t.id] = [w for w, _, _ in elig]
        used[t.id] = model.NewBoolVar(f"used_{t.id}")
        arcs = [(0, 0, used[t.id].Not())]
        travel_terms = []
        work_terms = []
        for j, (w, lo, hi) in enumerate(elig, start=1):
            x = model.NewBoolVar(f"x_{t.id}_{w.id}")
            presence[(t.id, w.id)] = x
            placed[w.id].append(x)
            start = model.NewIntVar(lo, hi, f"s_{t.id}_{w.id}")
            starts[(t.id, w.id)] = start
            arcs.append((j, j, x.Not()))
            work_terms.append(w.duration * x)
            pin = pin_of.get(w.id)
            if pin is not None:
                model.Add(x == 1)
                model.Add(start == pin.start)
            if w.id in late:
                model.Add(late[w.id] >= start + w.duration - w.due).OnlyEnforceIf(x)
            leg_in = problem.travel[t.home][w.location]
            if t.shift_start + leg_in + buffer <= hi:
                lit = model.NewBoolVar(f"a_{t.id}_0_{j}")
                arc_lit[(t.id, 0, j)] = lit
                arcs.append((0, j, lit))
                model.Add(start >= t.shift_start + leg_in + buffer).OnlyEnforceIf(lit)
                travel_terms.append(leg_in * lit)
            leg_out = problem.travel[w.location][t.home]
            lit = model.NewBoolVar(f"a_{t.id}_{j}_0")
            arc_lit[(t.id, j, 0)] = lit
            arcs.append((j, 0, lit))
            travel_terms.append(leg_out * lit)
        for i, (wi, lo_i, _) in enumerate(elig, start=1):
            for j, (wj, _, hi_j) in enumerate(elig, start=1):
                if i == j:
                    continue
                leg = problem.travel[wi.location][wj.location]
                if lo_i + wi.duration + leg + buffer > hi_j:
                    continue
                arcs_total += 1
                if arcs_total > MAX_ARCS:
                    raise ModelTooLarge
                lit = model.NewBoolVar(f"a_{t.id}_{i}_{j}")
                arc_lit[(t.id, i, j)] = lit
                arcs.append((i, j, lit))
                model.Add(starts[(t.id, wj.id)] >= starts[(t.id, wi.id)] + wi.duration + leg + buffer).OnlyEnforceIf(lit)
                travel_terms.append(leg * lit)
        model.AddCircuit(arcs)
        load = sum(travel_terms) + sum(work_terms)
        model.Add(load <= t.max_daily_minutes + t.overtime_minutes)
        overtime = model.NewIntVar(0, t.overtime_minutes, f"ot_{t.id}")
        model.Add(overtime >= load - t.max_daily_minutes)
        objective.append(TRAVEL_WEIGHT * sum(travel_terms))
        objective.append(OVERTIME_WEIGHT * overtime)
        objective.append(TECHNICIAN_WEIGHT * used[t.id])

    for w in problem.work_orders:
        model.Add(sum(placed[w.id]) + unassigned[w.id] == 1)
    model.Minimize(sum(objective))

    _add_hint(model, hint, nodes, presence, starts, arc_lit, used, unassigned, problem)

    solver = cp_model.CpSolver()
    solver.parameters.max_time_in_seconds = time_limit_s
    solver.parameters.num_workers = workers
    solver.parameters.random_seed = 1
    status = solver.StatusName(solver.Solve(model))
    if status not in SOLVED:
        return CpSatOutcome(status, None)

    by_tech: dict[str, list[tuple[int, str]]] = {}
    for (tid, wid), x in presence.items():
        if solver.BooleanValue(x):
            by_tech.setdefault(tid, []).append((solver.Value(starts[(tid, wid)]), wid))
    durations = {w.id: w.duration for w in problem.work_orders}
    out: list[Assignment] = []
    for t in problem.technicians:
        for seq, (start, wid) in enumerate(sorted(by_tech.get(t.id, []))):
            out.append(Assignment(wid, t.id, start, start + durations[wid], seq))
    return CpSatOutcome(status, out)


def _add_hint(model, hint, nodes, presence, starts, arc_lit, used, unassigned, problem) -> None:
    routes: dict[str, list[Assignment]] = {}
    for a in hint:
        routes.setdefault(a.technician_id, []).append(a)
    hinted = {a.work_order_id for items in routes.values() for a in items}
    for wid, var in unassigned.items():
        model.AddHint(var, int(wid not in hinted))
    arcs: dict[str, list[tuple[int, int, object]]] = {}
    for (kt, i, j), lit in arc_lit.items():
        arcs.setdefault(kt, []).append((i, j, lit))
    for tid, orders in nodes.items():
        route = sorted(routes.get(tid, []), key=lambda a: a.sequence)
        index = {w.id: j for j, w in enumerate(orders, start=1)}
        on_route = [index[a.work_order_id] for a in route if a.work_order_id in index]
        chain = [0, *on_route, 0] if on_route else []
        walked = {(chain[k], chain[k + 1]) for k in range(len(chain) - 1)}
        model.AddHint(used[tid], int(bool(on_route)))
        for i, j, lit in arcs.get(tid, []):
            model.AddHint(lit, int((i, j) in walked))
        start_of = {a.work_order_id: a.start for a in route}
        for w in orders:
            model.AddHint(presence[(tid, w.id)], int(w.id in start_of and w.id in index))
            if w.id in start_of:
                model.AddHint(starts[(tid, w.id)], start_of[w.id])


def _compact(problem: Problem, plan: list[Assignment], pinned_ids: set[str]) -> list[Assignment]:
    wos = {w.id: w for w in problem.work_orders}
    out: list[Assignment] = []
    for t in problem.technicians:
        free_at, at = t.shift_start, t.home
        for a in sorted((a for a in plan if a.technician_id == t.id), key=lambda a: a.sequence):
            w = wos[a.work_order_id]
            start = a.start
            if a.work_order_id not in pinned_ids:
                earliest = free_at + problem.travel[at][w.location] + problem.buffer_minutes
                if w.window_start is not None:
                    earliest = max(earliest, w.window_start)
                start = min(a.start, earliest)
            out.append(Assignment(a.work_order_id, t.id, start, start + w.duration, a.sequence))
            free_at, at = start + w.duration, w.location
    return out


def optimize(
    problem: Problem,
    pinned: Iterable[Assignment] = (),
    time_limit_s: float = DEFAULT_TIME_LIMIT_S,
    workers: int = DEFAULT_WORKERS,
) -> SolveResult:
    began = time.monotonic()
    pinned = list(pinned)
    greedy_plan, greedy_unassigned = solve_greedy(problem, pinned)
    greedy_cost = plan_cost(problem, greedy_plan)
    pinned_ids = {a.work_order_id for a in pinned}

    def greedy(reason: str | None, cpsat_status: str | None = None) -> SolveResult:
        if reason:
            log.warning("dispatch fell back to greedy", extra={"code": "SVC-DSP-002", "reason": reason, "cpsatStatus": cpsat_status})
        return SolveResult(greedy_plan, greedy_unassigned, "greedy", greedy_cost, cpsat_status, reason, time.monotonic() - began)

    if all(w.id in pinned_ids for w in problem.work_orders):
        return greedy(None)
    try:
        outcome = solve_cpsat(problem, pinned, greedy_plan, time_limit_s, workers)
    except ModelTooLarge:
        return greedy(FALLBACK_TOO_LARGE)
    except ImportError:
        return greedy(FALLBACK_UNAVAILABLE)
    if outcome.assignments is None:
        reasons = {"INFEASIBLE": FALLBACK_INFEASIBLE, "MODEL_INVALID": FALLBACK_MODEL_INVALID}
        return greedy(reasons.get(outcome.status, FALLBACK_TIMEOUT), outcome.status)
    plan = _compact(problem, outcome.assignments, pinned_ids)
    assigned = {a.work_order_id for a in plan}
    if plan_violations(problem, plan) or not pinned_ids <= assigned:
        log.error("cp-sat plan failed the feasibility check", extra={"cpsatStatus": outcome.status})
        return greedy(FALLBACK_INVALID_PLAN, outcome.status)
    cost = plan_cost(problem, plan)
    if cost.total > greedy_cost.total:
        return greedy(FALLBACK_NOT_BETTER, outcome.status)
    unassigned = [w.id for w in problem.work_orders if w.id not in assigned]
    return SolveResult(plan, unassigned, "cpsat", cost, outcome.status, None, time.monotonic() - began)
