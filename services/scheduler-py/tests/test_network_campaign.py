"""Resource networks (FR-23.13, BR-23.11) and campaign sequencing (FR-23.14, BR-23.12)."""

from __future__ import annotations

import itertools

import pytest
from hypothesis import assume, given, settings
from hypothesis import strategies as st

from scheduler_opt import (
    Edge,
    Instance,
    Job,
    Operation,
    changeover_matrix,
    check_schedule,
    validate_instance,
    validate_matrix,
)

# Machines: 0 = vessel V1, 1 = filler F1, 2 = filler F2, 3 = filler F3.
V1, F1, F2, F3 = 0, 1, 2, 3


def _fill_network(max_wait: int | None = 120, transfer: int = 0) -> tuple[Edge, ...]:
    """AC-23.10: V1 feeds F1 and F2 only."""
    return (Edge(V1, F1, transfer, max_wait), Edge(V1, F2, transfer, max_wait))


def _mix_then_fill(max_wait: int | None = 120, transfer: int = 0) -> Job:
    return Job(
        operations=[
            Operation(V1, 10),
            Operation(F1, 20, candidates=(F1, F2, F3), network=_fill_network(max_wait, transfer)),
        ]
    )


def test_validate_rejects_bad_networks_and_candidates():
    cases = [
        (Job(operations=[Operation(V1, 5, network=_fill_network())]), "no previous operation"),
        (
            Job(operations=[Operation(V1, 5), Operation(F1, 5, candidates=(F1, F2), network=(Edge(V1, F3),))]),
            "no allowed network transition",
        ),
        (Job(operations=[Operation(V1, 5), Operation(F1, 5, network=(Edge(V1, F1, 30, 10),))]), "transfer <= max wait"),
        (Job(operations=[Operation(F1, 5, candidates=(F2, F1))]), "first among its candidates"),
        (Job(operations=[Operation(F1, 5, candidates=(F1, F1))]), "candidate machine twice"),
        (Job(operations=[Operation(F1, 5, candidates=(F1, 9))]), "candidate machine 9 outside"),
        (
            # Each step alone has an edge, but no machine choice satisfies both.
            Job(
                operations=[
                    Operation(V1, 5),
                    Operation(F1, 5, candidates=(F1, F2), network=(Edge(V1, F1),)),
                    Operation(F3, 5, network=(Edge(F2, F3),)),
                ]
            ),
            "no chain of machines",
        ),
    ]
    for job, expected in cases:
        problems = validate_instance(Instance(machines=4, jobs=[job]))
        assert any(expected in p for p in problems), (expected, problems)


def test_check_schedule_flags_off_network_steps_and_wait_limits():
    inst = Instance(machines=4, jobs=[_mix_then_fill(max_wait=30, transfer=5)])
    assert any("outside the network" in p for p in check_schedule(inst, [[0, 15]], [[V1, F3]]))
    assert any("waits 60 min" in p for p in check_schedule(inst, [[0, 70]], [[V1, F1]]))
    assert any("waits 0 min" in p for p in check_schedule(inst, [[0, 10]], [[V1, F1]]))  # transfer not respected
    assert any("not one of its options" in p for p in check_schedule(inst, [[0, 15]], [[F1, F1]]))
    assert check_schedule(inst, [[0, 15]], [[V1, F2]]) == []


def test_network_restricts_the_successor_to_allowed_fillers():
    pytest.importorskip("ortools")
    from scheduler_opt import solve

    inst = Instance(machines=4, jobs=[_mix_then_fill(max_wait=None, transfer=5)])
    sol = solve(inst, time_limit_s=10)
    assert sol.status in ("OPTIMAL", "FEASIBLE")
    assert sol.machines[0][1] in (F1, F2)
    assert sol.starts[0][1] - (sol.starts[0][0] + 10) >= 5
    assert check_schedule(inst, sol.starts, sol.machines) == []


def test_ac_23_10_max_wait_delays_the_mixing():
    """F1 and F2 are busy for 240 minutes and the mix may wait 120: mixing is
    delayed so filling starts within 120 minutes, and nothing fills on F3."""
    pytest.importorskip("ortools")
    from scheduler_opt import solve

    blockers = [Job(operations=[Operation(F1, 240)]), Job(operations=[Operation(F2, 240)])]
    inst = Instance(machines=4, jobs=[*blockers, _mix_then_fill(max_wait=120)])
    sol = solve(inst, time_limit_s=20)
    assert sol.status in ("OPTIMAL", "FEASIBLE")
    assert check_schedule(inst, sol.starts, sol.machines) == []
    mix_end = sol.starts[2][0] + 10
    assert 0 <= sol.starts[2][1] - mix_end <= 120
    assert sol.machines[2][1] in (F1, F2)
    assert sol.starts[2][0] >= 240 - 120 - 10


@st.composite
def flexible_instances(draw):
    machines = draw(st.integers(min_value=2, max_value=4))
    jobs = []
    for _ in range(draw(st.integers(min_value=1, max_value=4))):
        ops = []
        for k in range(draw(st.integers(min_value=1, max_value=3))):
            cands = draw(st.lists(st.integers(0, machines - 1), min_size=1, max_size=machines, unique=True))
            network = None
            if k > 0 and draw(st.booleans()):
                prev = ops[-1].candidates or (ops[-1].machine,)
                # At least one edge from a previous option to a current one keeps it valid.
                edges = {(draw(st.sampled_from(prev)), draw(st.sampled_from(cands)))}
                network = tuple(Edge(a, b, draw(st.integers(0, 5)), draw(st.none() | st.integers(5, 30))) for a, b in edges)
            ops.append(Operation(cands[0], draw(st.integers(1, 9)), candidates=tuple(cands), network=network))
        jobs.append(Job(operations=ops))
    return Instance(machines=machines, jobs=jobs)


@settings(max_examples=20, deadline=None)
@given(inst=flexible_instances())
def test_property_flexible_network_schedules_are_feasible(inst):
    pytest.importorskip("ortools")
    from scheduler_opt import solve

    # A generated chain may have no machine path; validation must catch that.
    assume(validate_instance(inst) == [])
    sol = solve(inst, time_limit_s=5)
    assert sol.status in ("OPTIMAL", "FEASIBLE")
    assert check_schedule(inst, sol.starts, sol.machines) == []


# --- campaign sequencing -------------------------------------------------------

BR_23_12_TABLE = {("A", "B"): 30, ("A", "C"): 90, ("B", "C"): 20, ("C", "B"): 60}


def test_changeover_matrix_builds_the_worked_example():
    assert changeover_matrix("A", ["B", "C"], BR_23_12_TABLE) == [[0, 30, 90], [0, 0, 20], [0, 60, 0]]
    with pytest.raises(ValueError):
        changeover_matrix("A", ["B", "C"], {("A", "B"): 30})


def test_validate_matrix_rejects_ragged_negative_and_oversized():
    assert validate_matrix([[0]]) != []
    assert any("square" in p for p in validate_matrix([[0, 1], [0]]))
    assert any("0 to" in p for p in validate_matrix([[0, -1], [0, 0]]))
    big = [[0] * 52 for _ in range(52)]
    assert any("limit" in p for p in validate_matrix(big))


def test_ac_23_11_sequence_is_b_then_c_with_50_minutes():
    pytest.importorskip("ortools")
    from scheduler_opt import sequence_campaign

    res = sequence_campaign(changeover_matrix("A", ["C", "B"], BR_23_12_TABLE), time_limit_s=10)
    assert res.status in ("OPTIMAL", "FEASIBLE")
    assert res.sequence == [1, 0]  # B (index 1) then C (index 0)
    assert res.changeover_minutes == 50


def _brute_force(changeover: list[list[int]]) -> int:
    n = len(changeover) - 1
    return min(
        sum(changeover[a][b] for a, b in zip((0, *perm), perm)) for perm in itertools.permutations(range(1, n + 1))
    )


@st.composite
def campaign_matrices(draw):
    size = draw(st.integers(min_value=1, max_value=5)) + 1
    cells = draw(st.lists(st.integers(0, 120), min_size=size * size, max_size=size * size))
    return [[cells[i * size + j] if i != j else 0 for j in range(size)] for i in range(size)]


@settings(max_examples=25, deadline=None)
@given(matrix=campaign_matrices())
def test_property_campaign_sequence_is_optimal(matrix):
    pytest.importorskip("ortools")
    from scheduler_opt import sequence_campaign

    res = sequence_campaign(matrix, time_limit_s=10)
    assert res.status == "OPTIMAL"
    assert sorted(res.sequence) == list(range(len(matrix) - 1))
    assert res.changeover_minutes == _brute_force(matrix)


def _client():
    pytest.importorskip("fastapi")
    pytest.importorskip("httpx")
    pytest.importorskip("ortools")
    from fastapi.testclient import TestClient

    from scheduler_opt.app import create_app

    return TestClient(create_app())


def test_campaign_endpoint_round_trip_and_rejects_bad_matrix():
    client = _client()
    body = {"changeover": changeover_matrix("A", ["B", "C"], BR_23_12_TABLE), "timeLimitSeconds": 5}
    out = client.post("/v1/optimize/campaign-sequence", json=body).json()
    assert out["sequence"] == [0, 1]
    assert out["changeoverMinutes"] == 50
    bad = client.post("/v1/optimize/campaign-sequence", json={"changeover": [[0, 1, 2], [0, 0]]})
    assert bad.status_code == 422


def test_job_shop_endpoint_accepts_networks_and_returns_machines():
    client = _client()
    body = {
        "machines": 4,
        "jobs": [
            {
                "operations": [
                    {"machine": V1, "minutes": 10},
                    {
                        "machine": F1,
                        "minutes": 20,
                        "candidates": [F1, F2, F3],
                        "network": [
                            {"fromMachine": V1, "toMachine": F1, "maxWaitMinutes": 120},
                            {"fromMachine": V1, "toMachine": F2, "transferMinutes": 5},
                        ],
                    },
                ]
            }
        ],
        "timeLimitSeconds": 5,
    }
    out = client.post("/v1/optimize/job-shop", json=body).json()
    assert out["status"] in ("OPTIMAL", "FEASIBLE")
    assert out["machines"][0][1] in (F1, F2)
    body["jobs"][0]["operations"][1]["network"] = [{"fromMachine": V1, "toMachine": F3, "transferMinutes": 30, "maxWaitMinutes": 10}]
    assert client.post("/v1/optimize/job-shop", json=body).status_code == 422
