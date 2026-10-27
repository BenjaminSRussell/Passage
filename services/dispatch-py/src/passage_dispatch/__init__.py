"""Passage dispatch: feasibility checks and the greedy fallback solver (wave 45, slice 1)."""

from .greedy import solve_greedy
from .model import Assignment, Problem, Technician, WorkOrder, plan_violations

__all__ = [
    "Assignment",
    "Problem",
    "Technician",
    "WorkOrder",
    "plan_violations",
    "solve_greedy",
]
