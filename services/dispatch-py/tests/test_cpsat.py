"""CP-SAT optimizer: the BR-45.3 oracle of test_feasibility_proof applied to its plans, the
shared objective, pinned assignments, the greedy fallback and a seeded benchmark against greedy."""

from __future__ import annotations

import math
import random

import pytest
from hypothesis import HealthCheck, given, settings
from hypothesis import strategies as st

from passage_dispatch import (
    Assignment,
    Problem,
    Technician,
    WorkOrder,
    optimize,
    plan_cost,
    plan_violations,
    solve_cpsat,
    solve_greedy,
)
from passage_dispatch import cpsat as cpsat_module
from test_feasibility_proof import oracle, metric_problems, tight_problems

PROOF = settings(max_examples=40, deadline=None, derandomize=True, suppress_health_check=[HealthCheck.too_slow])
LIMIT = 1.0
WORKERS = 2


def check(problem: Problem, result) -> None:
    assert oracle(problem, result.assignments) == set()
    assert plan_violations(problem, result.assignments) == []
    assert sorted([a.work_order_id for a in result.assignments] + result.unassigned) == sorted(w.id for w in problem.work_orders)
    assert result.cost == plan_cost(problem, result.assignments)
    greedy_plan, _ = solve_greedy(problem)
    assert result.cost.total <= plan_cost(problem, greedy_plan).total


@PROOF
@given(metric_problems())
def test_cpsat_plans_pass_the_independent_oracle(problem: Problem):
    check(problem, optimize(problem, time_limit_s=LIMIT, workers=WORKERS))


@PROOF
@given(tight_problems())
def test_cpsat_plans_pass_the_oracle_on_adversarial_problems(problem: Problem):
    result = optimize(problem, time_limit_s=LIMIT, workers=WORKERS)
    check(problem, result)
    wos = {w.id: w for w in problem.work_orders}
    for a in result.assignments:
        assert "gas" not in wos[a.work_order_id].skills


@PROOF
@given(metric_problems(), st.data())
def test_cpsat_keeps_pinned_assignments(problem: Problem, data):
    first = optimize(problem, time_limit_s=LIMIT, workers=WORKERS)
    if not first.assignments:
        return
    pinned = data.draw(st.lists(st.sampled_from(first.assignments), unique=True))
    second = optimize(problem, pinned=pinned, time_limit_s=LIMIT, workers=WORKERS)
    check(problem, second)
    kept = {(a.work_order_id, a.technician_id, a.start, a.end) for a in second.assignments}
    assert {(a.work_order_id, a.technician_id, a.start, a.end) for a in pinned} <= kept


TRAVEL = ((0, 10, 30), (10, 0, 20), (30, 20, 0))


def T(id="T1", skills=("hvac",), max_daily=480, overtime=0, territories=("north",)):
    return Technician(id, frozenset(skills), frozenset(territories), 480, 1020, 0, max_daily, overtime)


def W(id="W1", skills=("hvac",), duration=60, location=1, priority=4, window=(None, None), territory="north", due=None):
    return WorkOrder(id, territory, frozenset(skills), duration, location, priority, window[0], window[1], due)


def test_objective_counts_every_component():
    problem = Problem((T(max_daily=100, overtime=60),), (W(duration=90, due=500), W("W2", location=2, priority=1, skills=("elec",))), TRAVEL)
    plan = [Assignment("W1", "T1", 495, 585, 0)]
    cost = plan_cost(problem, plan)
    assert (cost.unassigned_priority, cost.lateness_minutes, cost.travel_minutes, cost.overtime_minutes, cost.technicians) == (4, 85, 20, 10, 1)
    assert cost.total == 4000 + 8500 + 20 + 50 + 50


def test_cpsat_prefers_less_travel_than_greedy():
    # Greedy gives W1 to the technician that finishes earliest; the optimizer sees both visits at location 2 belong together.
    problem = Problem((T("T1"), T("T2")), (W("W1", location=2, priority=1), W("W2", location=2, priority=2)), TRAVEL)
    result = optimize(problem, time_limit_s=5, workers=WORKERS)
    assert result.solver == "cpsat" and result.cpsat_status == "OPTIMAL" and result.fallback_reason is None
    assert {a.technician_id for a in result.assignments} == {"T1"}
    assert result.cost.travel_minutes == 60 and result.cost.technicians == 1


def test_cpsat_serves_the_urgent_order_when_only_one_fits():
    problem = Problem((T(max_daily=140),), (W("W1", duration=90, priority=4), W("W2", duration=90, priority=1)), TRAVEL)
    result = optimize(problem, time_limit_s=5, workers=WORKERS)
    assert [a.work_order_id for a in result.assignments] == ["W2"]
    assert result.unassigned == ["W1"]


def test_cpsat_minimizes_lateness_against_due_minutes():
    problem = Problem((T(),), (W("W1", due=1000, location=2, duration=30), W("W2", due=540, location=1, duration=30)), TRAVEL)
    result = optimize(problem, time_limit_s=5, workers=WORKERS)
    assert result.cost.lateness_minutes == 0
    assert [a.work_order_id for a in sorted(result.assignments, key=lambda a: a.sequence)] == ["W2", "W1"]


def test_cpsat_overtime_is_used_only_when_it_saves_an_assignment():
    job = W(duration=90)
    plan = optimize(Problem((T(max_daily=100, overtime=10),), (job,), TRAVEL), time_limit_s=5, workers=WORKERS)
    assert [a.work_order_id for a in plan.assignments] == ["W1"] and plan.cost.overtime_minutes == 10
    assert optimize(Problem((T(max_daily=100),), (job,), TRAVEL), time_limit_s=5, workers=WORKERS).unassigned == ["W1"]


def test_cpsat_respects_skill_territory_and_window():
    problem = Problem(
        (T("T1"), T("T2", skills=("elec",), territories=("south",))),
        (W("W1", skills=("elec",), territory="south"), W("W2", territory="south"), W("W3", window=(480, 540), location=2)),
        TRAVEL,
    )
    result = optimize(problem, time_limit_s=5, workers=WORKERS)
    assert {a.work_order_id for a in result.assignments} == {"W1"}
    assert sorted(result.unassigned) == ["W2", "W3"]


def test_pinned_assignment_is_fixed_and_others_work_around_it():
    problem = Problem((T(),), (W("W1"), W("W2", location=2, duration=30)), TRAVEL)
    pinned = [Assignment("W1", "T1", 600, 660, 0)]
    result = optimize(problem, pinned=pinned, time_limit_s=5, workers=WORKERS)
    by_id = {a.work_order_id: a for a in result.assignments}
    assert by_id["W1"].start == 600 and by_id["W1"].end == 660
    assert "W2" in by_id and oracle(problem, result.assignments) == set()


def test_infeasible_pins_raise_like_greedy():
    problem = Problem((T(),), (W("W1"),), TRAVEL)
    with pytest.raises(ValueError):
        optimize(problem, pinned=[Assignment("W1", "T1", 100, 160, 0)])


def test_solve_cpsat_reports_status_and_a_feasible_plan():
    problem = Problem((T(),), (W("W1"),), TRAVEL)
    outcome = solve_cpsat(problem, time_limit_s=5, workers=WORKERS)
    assert outcome.status == "OPTIMAL"
    assert plan_violations(problem, outcome.assignments) == []


@pytest.mark.parametrize(
    ("status", "reason"),
    [("UNKNOWN", "timeout"), ("INFEASIBLE", "infeasible"), ("MODEL_INVALID", "model_invalid")],
)
def test_falls_back_to_greedy_when_cpsat_yields_no_plan(monkeypatch, status, reason):
    monkeypatch.setattr(cpsat_module, "solve_cpsat", lambda *a, **k: cpsat_module.CpSatOutcome(status, None))
    problem = Problem((T(),), (W("W1"),), TRAVEL)
    result = optimize(problem)
    assert result.solver == "greedy" and result.fallback_reason == reason and result.cpsat_status == status
    assert result.assignments == solve_greedy(problem)[0]


def test_falls_back_when_the_model_is_too_large(monkeypatch):
    def too_large(*a, **k):
        raise cpsat_module.ModelTooLarge

    monkeypatch.setattr(cpsat_module, "solve_cpsat", too_large)
    result = optimize(Problem((T(),), (W("W1"),), TRAVEL))
    assert (result.solver, result.fallback_reason) == ("greedy", "too_large")


def test_falls_back_when_ortools_is_missing(monkeypatch):
    def missing(*a, **k):
        raise ImportError

    monkeypatch.setattr(cpsat_module, "solve_cpsat", missing)
    assert optimize(Problem((T(),), (W("W1"),), TRAVEL)).fallback_reason == "solver_unavailable"


def test_a_plan_that_fails_the_feasibility_check_is_never_returned(monkeypatch):
    broken = [Assignment("W1", "T1", 0, 60, 0)]
    monkeypatch.setattr(cpsat_module, "solve_cpsat", lambda *a, **k: cpsat_module.CpSatOutcome("FEASIBLE", broken))
    problem = Problem((T(),), (W("W1"),), TRAVEL)
    result = optimize(problem)
    assert result.fallback_reason == "invalid_plan" and plan_violations(problem, result.assignments) == []


def test_a_plan_worse_than_greedy_is_replaced_by_greedy(monkeypatch):
    problem = Problem((T("T1"), T("T2")), (W("W1", due=600),), TRAVEL)
    worse = [Assignment("W1", "T2", 700, 760, 0)]
    monkeypatch.setattr(cpsat_module, "solve_cpsat", lambda *a, **k: cpsat_module.CpSatOutcome("FEASIBLE", worse))
    monkeypatch.setattr(cpsat_module, "_compact", lambda problem, plan, pinned: plan)
    greedy_cost = plan_cost(problem, solve_greedy(problem)[0])
    result = optimize(problem)
    assert result.fallback_reason == "not_better_than_greedy" and result.cost.total <= greedy_cost.total


def benchmark_problem(seed: int) -> Problem:
    rng = random.Random(seed)
    pts = [(rng.randint(0, 60), rng.randint(0, 60)) for _ in range(9)]
    travel = tuple(tuple(math.ceil(math.dist(p, q)) for q in pts) for p in pts)
    skills = ["hvac", "elec", "plumb"]
    techs = tuple(
        Technician(f"T{i}", frozenset(rng.sample(skills, rng.randint(2, 3))), frozenset(["north"]), 480, 1020, rng.randint(0, 8), 420, rng.choice([0, 60]))
        for i in range(3)
    )
    wos = []
    for j in range(12):
        ws = we = None
        if rng.random() < 0.4:
            ws = rng.randint(480, 840)
            we = ws + rng.randint(90, 300)
        wos.append(WorkOrder(f"W{j}", "north", frozenset(rng.sample(skills, rng.randint(0, 1))), rng.choice([30, 45, 60, 90]), rng.randint(0, 8),
                             rng.randint(1, 4), ws, we, rng.choice([None, rng.randint(540, 900)])))
    return Problem(techs, tuple(wos), travel)


def test_cpsat_beats_or_equals_greedy_on_a_seeded_benchmark():
    greedy_total = cpsat_total = improved = 0
    for seed in range(12):
        problem = benchmark_problem(seed)
        greedy_cost = plan_cost(problem, solve_greedy(problem)[0])
        result = optimize(problem, time_limit_s=5, workers=WORKERS)
        assert oracle(problem, result.assignments) == set()
        assert result.cost.total <= greedy_cost.total, seed
        greedy_total += greedy_cost.total
        cpsat_total += result.cost.total
        improved += result.cost.total < greedy_cost.total
    assert cpsat_total < greedy_total
    assert improved >= 6
