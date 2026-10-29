"""Tests for the job-shop model, parser, validator and CP-SAT solver."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from hypothesis import given, settings
from hypothesis import strategies as st

from scheduler_opt import Instance, Job, Operation, check_schedule, parse_orlib, validate_instance

ORLIB = Path(__file__).resolve().parent.parent / "testdata" / "orlib"
FT06 = ORLIB / "ft06.txt"
BENCHMARKS = json.loads((ORLIB / "manifest.json").read_text())["instances"]
BENCHMARK_TIME_LIMIT_S = 30
FT06_OPTIMUM = 55  # published optimum (OR-Library)


def _two_by_two() -> Instance:
    return Instance(
        machines=2,
        jobs=[
            Job(operations=[Operation(0, 3), Operation(1, 2)]),
            Job(operations=[Operation(1, 4), Operation(0, 1)]),
        ],
    )


def test_parse_ft06_reads_jobs_and_machines():
    inst = parse_orlib(FT06.read_text())
    assert inst.machines == 6
    assert len(inst.jobs) == 6
    assert inst.operation_count == 36
    assert inst.jobs[0].operations[0] == Operation(machine=2, minutes=1)
    assert validate_instance(inst) == []


@pytest.mark.parametrize(
    "text",
    [
        "",
        "2 2\n0 1 1 1\n",  # too few job lines
        "1 2\n0 1 1\n",  # wrong number of numbers on a job line
        "1 2\n0 1 5 1\n",  # machine 5 out of range
    ],
)
def test_parse_rejects_malformed_instances(text):
    with pytest.raises(ValueError):
        parse_orlib(text)


def test_check_schedule_accepts_feasible_and_flags_overlap_and_precedence():
    inst = _two_by_two()
    # job 0: m0 0-3, m1 4-6; job 1: m1 0-4, m0 4-5 (the draft's [[0, 3], [0, 4]]
    # double-booked machine 1 from 3 to 4 and was not feasible)
    assert check_schedule(inst, [[0, 4], [0, 4]]) == []
    # job 1 op 0 runs on machine 1 at the same time as job 0 op 1 (machine 1 from 3..5)
    overlap = check_schedule(inst, [[0, 3], [4, 8]])
    assert any("overlaps" in p for p in overlap)
    bad_precedence = check_schedule(inst, [[0, 2], [0, 4]])
    assert any("predecessor" in p for p in bad_precedence)
    same_machine = check_schedule(inst, [[0, 0], [0, 4]])
    assert any("overlaps" in p for p in same_machine)


def test_validate_instance_reports_bad_machine_and_zero_duration():
    inst = Instance(machines=1, jobs=[Job(operations=[Operation(3, 0)])])
    problems = validate_instance(inst)
    assert len(problems) == 2


def test_ft06_cpsat_reaches_known_optimum():
    pytest.importorskip("ortools")
    from scheduler_opt import solve

    inst = parse_orlib(FT06.read_text())
    sol = solve(inst, time_limit_s=60)
    assert sol.status in ("OPTIMAL", "FEASIBLE")
    assert check_schedule(inst, sol.starts) == []
    # AC1: within 5% of the known optimum (in practice CP-SAT proves 55).
    assert sol.makespan <= FT06_OPTIMUM * 1.05
    assert sol.status == "OPTIMAL" and sol.makespan == FT06_OPTIMUM


def test_orlib_manifest_lists_30_instances_with_fixtures():
    assert len(BENCHMARKS) == 30
    for entry in BENCHMARKS:
        inst = parse_orlib((ORLIB / f"{entry['name']}.txt").read_text())
        assert (len(inst.jobs), inst.machines) == (entry["jobs"], entry["machines"])


@pytest.mark.parametrize("entry", BENCHMARKS, ids=[e["name"] for e in BENCHMARKS])
def test_orlib_instance_cpsat_within_5_percent_of_optimum(entry):
    pytest.importorskip("ortools")
    from scheduler_opt import solve

    inst = parse_orlib((ORLIB / f"{entry['name']}.txt").read_text())
    sol = solve(inst, time_limit_s=BENCHMARK_TIME_LIMIT_S)
    assert sol.status in ("OPTIMAL", "FEASIBLE")
    assert check_schedule(inst, sol.starts) == []
    assert sol.makespan <= entry["optimum"] * 1.05


def test_solve_refuses_windows_above_limit():
    pytest.importorskip("ortools")
    from scheduler_opt import solve
    from scheduler_opt.jobshop import MAX_OPERATIONS

    ops = [Operation(0, 1)] * (MAX_OPERATIONS + 1)
    with pytest.raises(ValueError):
        solve(Instance(machines=1, jobs=[Job(operations=ops)]), time_limit_s=5)


@st.composite
def small_instances(draw):
    machines = draw(st.integers(min_value=2, max_value=3))
    n_jobs = draw(st.integers(min_value=2, max_value=4))
    jobs = []
    for _ in range(n_jobs):
        order = draw(st.permutations(list(range(machines))))
        durations = draw(st.lists(st.integers(min_value=1, max_value=9), min_size=machines, max_size=machines))
        jobs.append(Job(operations=[Operation(m, d) for m, d in zip(order, durations)]))
    return Instance(machines=machines, jobs=jobs)


@settings(max_examples=25, deadline=None)
@given(inst=small_instances())
def test_property_cpsat_schedules_never_double_book(inst):
    pytest.importorskip("ortools")
    from scheduler_opt import solve

    sol = solve(inst, time_limit_s=5)
    assert sol.status in ("OPTIMAL", "FEASIBLE")
    assert check_schedule(inst, sol.starts) == []
    total = sum(op.minutes for job in inst.jobs for op in job.operations)
    assert sol.makespan <= total



def test_jobs_without_due_date_add_no_lateness():
    # Regression: due defaulted to 0, so every job counted as late from minute 0
    # and the objective minimised the sum of completion times, not makespan.
    assert Job(operations=[Operation(0, 1)]).due is None
    pytest.importorskip("ortools")
    from scheduler_opt import solve

    inst = _two_by_two()
    sol = solve(inst, time_limit_s=5)
    assert sol.total_lateness == 0
    assert check_schedule(inst, sol.starts) == []
    assert sol.makespan == 6  # machine 1 carries 2 + 4 minutes


def test_due_dates_weight_lateness():
    pytest.importorskip("ortools")
    from scheduler_opt import solve

    inst = Instance(machines=1, jobs=[Job(operations=[Operation(0, 5)], due=5), Job(operations=[Operation(0, 5)], due=10, weight=3)])
    sol = solve(inst, time_limit_s=5)
    assert sol.status == "OPTIMAL"
    assert sol.total_lateness == 0
    assert sol.starts == [[0], [5]]
