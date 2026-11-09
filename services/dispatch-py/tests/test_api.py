"""Optimizer API: service-token auth, async runs with status, tenant isolation, idempotency and failure paths."""

from __future__ import annotations

import base64
import json
import time
import uuid
from pathlib import Path

import pytest
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from fastapi.testclient import TestClient

from passage_dispatch import auth, plan_violations
from passage_dispatch.api import build_problem, create_app, RunIn
from passage_dispatch.auth import ServiceVerifier
from passage_dispatch.cpsat import SolveResult
from passage_dispatch.model import Assignment
from passage_dispatch.objective import Cost
from passage_dispatch.runs import RunStore

TENANT = "11111111-1111-4111-8111-111111111111"
ENTITY = "22222222-2222-4222-8222-222222222222"
OTHER_ENTITY = "33333333-3333-4333-8333-333333333333"
KEY = Ed25519PrivateKey.generate()
PUBLIC = base64.b64encode(KEY.public_key().public_bytes(serialization.Encoding.Raw, serialization.PublicFormat.Raw)).decode()


def b64(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode()


def token(duties=(auth.DUTY_CREATE, auth.DUTY_READ), tenant=TENANT, entity=ENTITY, key=KEY, **over) -> str:
    now = int(time.time())
    claims = {"iss": "fieldservice", "kid": "", "aud": "dispatch", "tid": tenant, "le": entity, "act": "user-1", "duties": list(duties),
              "iat": now, "exp": now + 60, "jti": uuid.uuid4().hex, **over}
    body = b64(json.dumps(claims).encode())
    return body + "." + b64(key.sign(auth.TOKEN_CONTEXT + body.encode()))


def headers(**kw) -> dict[str, str]:
    return {auth.HEADER: token(**kw), "X-Request-Id": "req-1"}


def verifier() -> ServiceVerifier:
    return ServiceVerifier.from_env({"PASSAGE_SERVICE_PUBLIC_KEYS": f"fieldservice={PUBLIC}"}.get)


TRAVEL = [[0, 10, 30], [10, 0, 20], [30, 20, 0]]
BODY = {
    "technicians": [{"id": "T1", "skills": ["HVAC"], "territories": ["north"], "shiftStart": 480, "shiftEnd": 1020, "home": 0, "maxDailyMinutes": 480}],
    "workOrders": [
        {"id": "W1", "territory": "north", "skills": ["HVAC"], "duration": 60, "location": 1, "priority": 1},
        {"id": "W2", "territory": "north", "skills": ["HVAC"], "duration": 30, "location": 2, "priority": 2},
    ],
    "travel": TRAVEL,
    "timeLimitSeconds": 5,
    "workers": 2,
}


def client(store: RunStore | None = None, ver: ServiceVerifier | None = None) -> TestClient:
    return TestClient(create_app(ver or verifier(), store))


def wait_for(c: TestClient, run_id: str, **kw) -> dict:
    for _ in range(200):
        got = c.get(f"/v1/dispatch/runs/{run_id}", headers=headers(**kw))
        assert got.status_code == 200
        if got.json()["status"] in ("SUCCEEDED", "FAILED"):
            return got.json()
        time.sleep(0.05)
    raise AssertionError("run did not finish")


def test_run_is_accepted_solved_and_readable():
    c = client()
    created = c.post("/v1/dispatch/runs", json=BODY, headers=headers())
    assert created.status_code == 202
    run = created.json()
    assert created.headers["Location"] == f"/v1/dispatch/runs/{run['id']}"
    assert run["status"] in ("QUEUED", "RUNNING", "SUCCEEDED")
    done = wait_for(c, run["id"])
    assert done["status"] == "SUCCEEDED" and done["error"] is None
    result = done["result"]
    assert result["solver"] == "cpsat" and result["fallbackReason"] is None and result["unassigned"] == []
    plan = [Assignment(a["workOrderId"], a["technicianId"], a["start"], a["end"], a["sequence"]) for a in result["assignments"]]
    problem, _ = build_problem(RunIn(**BODY))
    assert plan_violations(problem, plan) == []
    assert result["cost"]["total"] > 0


def test_pinned_assignment_is_kept_in_the_plan():
    body = {**BODY, "pinned": [{"workOrderId": "W1", "technicianId": "T1", "start": 700, "end": 760, "sequence": 0}]}
    c = client()
    done = wait_for(c, c.post("/v1/dispatch/runs", json=body, headers=headers()).json()["id"])
    w1 = next(a for a in done["result"]["assignments"] if a["workOrderId"] == "W1")
    assert (w1["technicianId"], w1["start"], w1["end"]) == ("T1", 700, 760)


def test_run_of_another_legal_entity_or_tenant_is_not_found():
    c = client()
    run_id = c.post("/v1/dispatch/runs", json=BODY, headers=headers()).json()["id"]
    for kw in ({"entity": OTHER_ENTITY}, {"tenant": OTHER_ENTITY}):
        refused = c.get(f"/v1/dispatch/runs/{run_id}", headers=headers(**kw))
        assert refused.status_code == 404 and refused.json()["code"] == "SVC-DSP-013"
    assert c.get("/v1/dispatch/runs/not-a-run", headers=headers()).status_code == 404


def test_idempotency_key_replays_the_run_and_refuses_a_different_body():
    c = client()
    first = c.post("/v1/dispatch/runs", json=BODY, headers={**headers(), "Idempotency-Key": "k1"})
    again = c.post("/v1/dispatch/runs", json=BODY, headers={**headers(), "Idempotency-Key": "k1"})
    assert again.status_code == 200 and again.headers["Idempotent-Replayed"] == "true" and again.json()["id"] == first.json()["id"]
    other = c.post("/v1/dispatch/runs", json={**BODY, "bufferMinutes": 10}, headers={**headers(), "Idempotency-Key": "k1"})
    assert other.status_code == 422 and other.json()["code"] == "idempotency_key_reuse"
    elsewhere = c.post("/v1/dispatch/runs", json=BODY, headers={**headers(tenant=OTHER_ENTITY), "Idempotency-Key": "k1"})
    assert elsewhere.json()["id"] != first.json()["id"]


@pytest.mark.parametrize(
    "mutate",
    [
        lambda b: b.update(travel=[[0, 1], [1, 0, 2]]),
        lambda b: b["workOrders"][0].update(location=9),
        lambda b: b.update(pinned=[{"workOrderId": "W1", "technicianId": "T1", "start": 100, "end": 160, "sequence": 0}]),
    ],
)
def test_infeasible_input_is_422_before_anything_is_queued(mutate):
    body = json.loads(json.dumps(BODY))
    mutate(body)
    refused = client().post("/v1/dispatch/runs", json=body, headers=headers())
    assert refused.status_code == 422 and refused.json()["code"] == "SVC-DSP-001"
    assert refused.headers["content-type"].startswith("application/problem+json") and refused.json()["traceId"] == "req-1"


def test_schema_violations_are_400_with_field_errors():
    refused = client().post("/v1/dispatch/runs", json={**BODY, "timeLimitSeconds": 0}, headers=headers())
    assert refused.status_code == 400 and refused.json()["code"] == "COM-0001"
    assert "timeLimitSeconds" in refused.json()["errors"]


def test_token_failures_are_401_and_a_missing_duty_is_403():
    c = client()
    other = Ed25519PrivateKey.generate()
    now = int(time.time())
    for bad in (
        {},
        {auth.HEADER: "garbage"},
        {auth.HEADER: token(key=other)},
        {auth.HEADER: token(aud="ledger")},
        {auth.HEADER: token(exp=now - 1, iat=now - 61)},
        {auth.HEADER: token(exp=now + 600)},
        {auth.HEADER: token(tid="not-a-uuid")},
        {auth.HEADER: token(le="00000000-0000-0000-0000-000000000000")},
        {auth.HEADER: token(iss="billing")},
        {auth.HEADER: token(duties=("ledger.entry.post",))},
        {auth.HEADER: token(duties=())},
    ):
        refused = c.post("/v1/dispatch/runs", json=BODY, headers=bad)
        assert refused.status_code == 401 and refused.json()["code"] == "AUTH-0001", bad
    read_only = c.post("/v1/dispatch/runs", json=BODY, headers=headers(duties=(auth.DUTY_READ,)))
    assert read_only.status_code == 403 and read_only.json()["code"] == "AUTH-0003"
    create_only = c.get("/v1/dispatch/runs/x", headers=headers(duties=(auth.DUTY_CREATE,)))
    assert create_only.status_code == 403


def test_a_replayed_token_is_refused():
    c = client()
    h = headers()
    assert c.post("/v1/dispatch/runs", json=BODY, headers=h).status_code == 202
    assert c.post("/v1/dispatch/runs", json=BODY, headers=h).status_code == 401


def test_every_request_is_refused_when_no_keys_are_configured():
    c = TestClient(create_app(None))
    assert c.get("/healthz").status_code == 200
    assert c.get("/readyz").status_code == 503
    assert c.post("/v1/dispatch/runs", json=BODY, headers=headers()).status_code == 401
    assert client().get("/readyz").json()["status"] == "ready"


def test_queue_full_is_429():
    refused = client(RunStore(max_queued=0)).post("/v1/dispatch/runs", json=BODY, headers=headers())
    assert refused.status_code == 429 and refused.json()["code"] == "SVC-DSP-012"


def test_a_solver_crash_fails_the_run_and_is_counted():
    def boom(*a, **k):
        raise RuntimeError("secret detail")

    store = RunStore(solve=boom)
    c = client(store)
    done = wait_for(c, c.post("/v1/dispatch/runs", json=BODY, headers=headers()).json()["id"])
    assert done["status"] == "FAILED" and done["error"] == {"code": "solver_error", "detail": "RuntimeError"} and done["result"] is None
    assert 'dispatch_runs_total{outcome="FAILED"} 1' in c.get("/metrics").text


def test_fallbacks_are_reported_and_counted():
    cost = Cost(0, 0, 0, 0, 0)
    store = RunStore(solve=lambda *a, **k: SolveResult([], [], "greedy", cost, "UNKNOWN", "timeout", 0.5))
    c = client(store)
    done = wait_for(c, c.post("/v1/dispatch/runs", json=BODY, headers=headers()).json()["id"])
    assert done["result"]["fallbackReason"] == "timeout" and done["result"]["solver"] == "greedy"
    text = c.get("/metrics").text
    assert 'dispatch_solver_fallbacks_total{reason="timeout"} 1' in text and 'dispatch_runs_total{outcome="SUCCEEDED"} 1' in text


def test_finished_runs_are_evicted_beyond_the_cap():
    store = RunStore(max_runs=2)
    c = client(store)
    ids = []
    for _ in range(4):
        ids.append(c.post("/v1/dispatch/runs", json=BODY, headers=headers()).json()["id"])
        wait_for(c, ids[-1])
    assert c.get(f"/v1/dispatch/runs/{ids[0]}", headers=headers()).status_code == 404
    assert c.get(f"/v1/dispatch/runs/{ids[-1]}", headers=headers()).status_code == 200


def test_public_keys_must_be_well_formed():
    with pytest.raises(ValueError):
        ServiceVerifier.from_env({"PASSAGE_SERVICE_PUBLIC_KEYS": "fieldservice=notbase64!"}.get)
    with pytest.raises(ValueError):
        ServiceVerifier.from_env({"PASSAGE_SERVICE_PUBLIC_KEYS": f"fieldservice={PUBLIC},fieldservice={PUBLIC}"}.get)
    assert ServiceVerifier.from_env({}.get) is None


def test_service_grant_matches_the_authz_policy_data():
    data = json.loads((Path(__file__).parents[3] / "policies" / "authz" / "data.json").read_text())
    assert data["service_grants"]["fieldservice"]["dispatch"] == list(auth.SERVICE_GRANTS["fieldservice"])
    for duty in auth.SERVICE_GRANTS["fieldservice"]:
        assert data["duties"][duty]["service_only"] is True
