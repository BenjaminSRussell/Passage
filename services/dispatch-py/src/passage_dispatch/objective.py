from __future__ import annotations

from dataclasses import dataclass

from .model import Assignment, Problem

LATENESS_WEIGHT = 100
UNASSIGNED_WEIGHT = 1000
TRAVEL_WEIGHT = 1
OVERTIME_WEIGHT = 5
TECHNICIAN_WEIGHT = 50


def priority_weight(priority: int) -> int:
    return max(1, 5 - priority)


@dataclass(frozen=True)
class Cost:
    unassigned_priority: int
    lateness_minutes: int
    travel_minutes: int
    overtime_minutes: int
    technicians: int

    @property
    def total(self) -> int:
        return (
            UNASSIGNED_WEIGHT * self.unassigned_priority
            + LATENESS_WEIGHT * self.lateness_minutes
            + TRAVEL_WEIGHT * self.travel_minutes
            + OVERTIME_WEIGHT * self.overtime_minutes
            + TECHNICIAN_WEIGHT * self.technicians
        )


def plan_cost(problem: Problem, assignments: list[Assignment]) -> Cost:
    wos = {w.id: w for w in problem.work_orders}
    techs = {t.id: t for t in problem.technicians}
    assigned = {a.work_order_id for a in assignments}
    unassigned = sum(priority_weight(w.priority) for w in problem.work_orders if w.id not in assigned)
    routes: dict[str, list[Assignment]] = {}
    for a in assignments:
        routes.setdefault(a.technician_id, []).append(a)
    lateness = travel_total = overtime = 0
    for tid, items in routes.items():
        tech = techs[tid]
        at, travel, work = tech.home, 0, 0
        for a in sorted(items, key=lambda a: a.sequence):
            w = wos[a.work_order_id]
            travel += problem.travel[at][w.location]
            work += w.duration
            at = w.location
            if w.due is not None:
                lateness += max(0, a.end - w.due)
        travel += problem.travel[at][tech.home]
        travel_total += travel
        overtime += max(0, work + travel - tech.max_daily_minutes)
    return Cost(unassigned, lateness, travel_total, overtime, len(routes))
