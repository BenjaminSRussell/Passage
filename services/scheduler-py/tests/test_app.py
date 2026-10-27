"""HTTP tests for scheduler-opt. Skipped when fastapi, httpx or ortools is absent."""

from __future__ import annotations

import pytest

pytest.importorskip("fastapi")
pytest.importorskip("httpx")


def _client():
    from fastapi.testclient import TestClient

    from scheduler_opt.app import create_app

    return TestClient(create_app())


def test_health_endpoints():
    c = _client()
    assert c.get("/healthz").json() == {"status": "ok"}
    assert c.get("/readyz").json()["status"] == "ready"


def test_optimize_rejects_windows_over_limit():
    body = {"machines": 1, "jobs": [{"operations": [{"machine": 0, "minutes": 1}] * 200} for _ in range(11)]}
    resp = _client().post("/v1/optimize/job-shop", json=body)
    assert resp.status_code == 422


def test_optimize_rejects_unknown_machine():
    body = {"machines": 1, "jobs": [{"operations": [{"machine": 4, "minutes": 2}]}]}
    resp = _client().post("/v1/optimize/job-shop", json=body)
    assert resp.status_code == 422


def test_optimize_returns_feasible_schedule():
    pytest.importorskip("ortools")
    body = {
        "machines": 2,
        "jobs": [
            {"operations": [{"machine": 0, "minutes": 3}, {"machine": 1, "minutes": 2}], "due": 4, "weight": 2},
            {"operations": [{"machine": 1, "minutes": 4}, {"machine": 0, "minutes": 1}], "due": 3},
        ],
        "timeLimitSeconds": 5,
    }
    resp = _client().post("/v1/optimize/job-shop", json=body)
    assert resp.status_code == 200
    out = resp.json()
    assert out["status"] in ("OPTIMAL", "FEASIBLE")
    # Machine 1 carries 2 + 4 = 6 minutes, which is a lower bound, and it is reachable.
    assert out["makespan"] == 6
    assert len(out["starts"]) == 2
