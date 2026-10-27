"""HTTP contract tests for the optimizer endpoints (camelCase wire format)."""

from __future__ import annotations

from fastapi.testclient import TestClient

from tms_optimizer.api import app

client = TestClient(app)


def _stop(stop_id: str, x: float, y: float, latest: int = 480) -> dict:
    return {
        "id": stop_id,
        "point": {"x": x, "y": y},
        "earliest": 0,
        "latest": latest,
        "serviceMin": 10,
        "weightKg": 100,
        "volumeL": 200,
        "pallets": 1,
    }


def test_healthz() -> None:
    resp = client.get("/healthz")
    assert resp.status_code == 200
    assert resp.json() == {"status": "ok"}


def test_solve_returns_camel_case_routes() -> None:
    body = {
        "depot": {"x": 0, "y": 0},
        "stops": [_stop("S1", 5, 0), _stop("S2", 0, 5)],
        "vehicles": [{"id": "V1", "capacityKg": 1000, "capacityL": 5000, "pallets": 10}],
        "timeLimitS": 5,
    }
    resp = client.post("/v1/routes:solve", json=body)
    assert resp.status_code == 200, resp.text
    data = resp.json()
    assert data["dropped"] == []
    assert len(data["routes"]) == 1
    route = data["routes"][0]
    assert route["vehicleId"] == "V1"
    assert {s["stopId"] for s in route["stops"]} == {"S1", "S2"}
    assert all("arrivalMin" in s and "lateMin" in s for s in route["stops"])


def test_solve_rejects_inverted_window_and_duplicate_ids() -> None:
    inverted = _stop("S1", 1, 1)
    inverted["earliest"] = 100
    inverted["latest"] = 10
    body = {
        "depot": {"x": 0, "y": 0},
        "stops": [inverted],
        "vehicles": [{"id": "V1", "capacityKg": 1000, "capacityL": 5000, "pallets": 10}],
    }
    assert client.post("/v1/routes:solve", json=body).status_code == 422

    dup = {
        "depot": {"x": 0, "y": 0},
        "stops": [_stop("S1", 1, 1), _stop("S1", 2, 2)],
        "vehicles": [{"id": "V1", "capacityKg": 1000, "capacityL": 5000, "pallets": 10}],
    }
    assert client.post("/v1/routes:solve", json=dup).status_code == 422

    two = {
        "depot": {"x": 0, "y": 0},
        "stops": [_stop("S1", 1, 1)],
        "vehicles": [{"id": "V1", "capacityKg": 1000, "capacityL": 5000, "pallets": 10}] * 2,
    }
    assert client.post("/v1/routes:solve", json=two).status_code == 422


def test_solve_requires_a_vehicle() -> None:
    body = {"depot": {"x": 0, "y": 0}, "stops": [_stop("S1", 1, 1)], "vehicles": []}
    assert client.post("/v1/routes:solve", json=body).status_code == 422


def test_stack_load_reports_plan_in_camel_case() -> None:
    body = {
        "trailer": {"lengthMm": 13600, "widthMm": 2480, "maxHeightMm": 2700, "maxWeightKg": 20000},
        "pallets": [
            {"id": "P1", "lengthMm": 1200, "widthMm": 1000, "heightMm": 900, "weightKg": 500, "stackable": True, "stopSeq": 2},
            {"id": "P2", "lengthMm": 1200, "widthMm": 1000, "heightMm": 900, "weightKg": 500, "stackable": False, "stopSeq": 1},
        ],
    }
    resp = client.post("/v1/load-plans:stack", json=body)
    assert resp.status_code == 200, resp.text
    data = resp.json()
    assert data["unplaced"] == []
    assert data["floorPositionsUsed"] == len(data["placements"])
    assert sorted(pid for pl in data["placements"] for pid in pl["palletIds"]) == ["P1", "P2"]
    assert data["loadSequence"] == ["P1", "P2"]
    assert data["axle"]["kingpinKg"] + data["axle"]["axleKg"] == data["totalWeightKg"] == 1000
    assert all({"lengthMm", "widthMm"} <= pl.keys() for pl in data["placements"])


def test_stack_load_rejects_non_positive_dimensions() -> None:
    body = {
        "trailer": {"lengthMm": 0, "widthMm": 2480, "maxHeightMm": 2700, "maxWeightKg": 20000},
        "pallets": [],
    }
    assert client.post("/v1/load-plans:stack", json=body).status_code == 422


def test_stack_load_rejects_kingpin_behind_axle() -> None:
    body = {
        "trailer": {"lengthMm": 13600, "widthMm": 2480, "maxHeightMm": 2700, "maxWeightKg": 20000, "kingpinMm": 9000, "axleMm": 8000},
        "pallets": [{"id": "P1", "lengthMm": 1200, "widthMm": 1000, "heightMm": 900, "weightKg": 500, "stackable": True}],
    }
    assert client.post("/v1/load-plans:stack", json=body).status_code == 422


def test_solve_keeps_an_explicit_zero_lateness_penalty() -> None:
    body = {
        "depot": {"x": 0, "y": 0},
        "stops": [_stop("S1", 5, 0)],
        "vehicles": [{"id": "V1", "capacityKg": 1000, "capacityL": 5000, "pallets": 10}],
        "latenessPerMin": 0,
        "timeLimitS": 5,
    }
    resp = client.post("/v1/routes:solve", json=body)
    assert resp.status_code == 200, resp.text
    assert resp.json()["dropped"] == []
