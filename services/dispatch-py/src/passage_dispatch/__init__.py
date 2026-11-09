"""Passage dispatch: feasibility checks, the greedy solver and the CP-SAT optimizer (wave 45)."""

from .cpsat import SolveResult, optimize, solve_cpsat
from .greedy import solve_greedy
from .model import Assignment, Problem, Technician, WorkOrder, plan_violations
from .objective import Cost, plan_cost

__all__ = [
    "Assignment",
    "Cost",
    "Problem",
    "SolveResult",
    "Technician",
    "WorkOrder",
    "optimize",
    "plan_cost",
    "plan_violations",
    "solve_cpsat",
    "solve_greedy",
]
