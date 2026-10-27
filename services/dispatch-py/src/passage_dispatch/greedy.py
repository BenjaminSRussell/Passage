"""Greedy insertion solver: the fallback used when CP-SAT yields no solution in time.

Work orders are appended to technician routes in priority order. Every candidate is
checked with the same feasibility rules as plan_violations, so the output is
feasible by construction. Pinned assignments are kept verbatim (AC-45.5).
"""

from __future__ import annotations

from collections.abc import Iterable

from .model import Assignment, Problem, WorkOrder, plan_violations, route_violations


def solve_greedy(
    problem: Problem,
    pinned: Iterable[Assignment] = (),
) -> tuple[list[Assignment], list[str]]:
    """Return (assignments, unassigned work order ids).

    Raises ValueError when the pinned assignments are themselves infeasible.
    """
    pinned_list = list(pinned)
    errors = plan_violations(problem, pinned_list)
    if errors:
        raise ValueError("pinned assignments are infeasible: " + "; ".join(errors))

    wos = {w.id: w for w in problem.work_orders}
    techs = sorted(problem.technicians, key=lambda t: t.id)
    routes: dict[str, list[Assignment]] = {t.id: [] for t in techs}
    for a in sorted(pinned_list, key=lambda a: (a.technician_id, a.sequence)):
        routes[a.technician_id].append(a)

    pinned_ids = {a.work_order_id for a in pinned_list}
    order = sorted(
        (w for w in problem.work_orders if w.id not in pinned_ids),
        key=_queue_key,
    )

    unassigned: list[str] = []
    for w in order:
        best: tuple[tuple[int, str], str, Assignment] | None = None
        for t in techs:
            route = routes[t.id]
            cand = _append_candidate(problem, t.id, route, w, wos)
            if route_violations(problem, t, route + [cand], wos):
                continue
            key = (cand.end, t.id)
            if best is None or key < best[0]:
                best = (key, t.id, cand)
        if best is None:
            unassigned.append(w.id)
        else:
            routes[best[1]].append(best[2])

    assignments = [a for t in techs for a in routes[t.id]]
    return assignments, unassigned


def _queue_key(w: WorkOrder) -> tuple[int, int, str]:
    return (w.priority, w.window_start if w.window_start is not None else 0, w.id)


def _append_candidate(
    problem: Problem,
    tech_id: str,
    route: list[Assignment],
    w: WorkOrder,
    wos: dict[str, WorkOrder],
) -> Assignment:
    tech = next(t for t in problem.technicians if t.id == tech_id)
    if route:
        prev_end = route[-1].end
        prev_loc = wos[route[-1].work_order_id].location
        sequence = route[-1].sequence + 1
    else:
        prev_end = tech.shift_start
        prev_loc = tech.home
        sequence = 0
    start = prev_end + problem.travel[prev_loc][w.location] + problem.buffer_minutes
    if w.window_start is not None:
        start = max(start, w.window_start)
    return Assignment(
        work_order_id=w.id,
        technician_id=tech_id,
        start=start,
        end=start + w.duration,
        sequence=sequence,
    )
