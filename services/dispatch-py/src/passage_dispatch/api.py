"""HTTP surface of dispatch: start an optimizer run (202) and read its status and plan.

POST /v1/dispatch/runs validates the territory-day, answers 202 and solves in the background with
CP-SAT, falling back to the greedy solver on timeout or infeasibility (SVC-DSP-002). GET
/v1/dispatch/runs/{id} reads the run. Calls carry a signed service token (X-Passage-Service-Token)
whose duties come from the fieldservice grant; a run is visible only to its tenant and legal entity.
"""

from __future__ import annotations

import hashlib
import json
import logging
import uuid
from typing import Annotated, Any

from fastapi import Depends, FastAPI, Header, Request
from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse, PlainTextResponse
from pydantic import BaseModel, Field

from . import auth
from .auth import Caller, ServiceVerifier, TokenError
from .cpsat import DEFAULT_TIME_LIMIT_S, DEFAULT_WORKERS
from .model import Assignment, Problem, Technician, WorkOrder, plan_violations
from .runs import FAILED, IdempotencyReuse, QueueFull, RunStore, SUCCEEDED

SERVICE = "dispatch"
PROBLEM_TYPE = "https://passage.dev/problems/"
MAX_MINUTE = 100_000


class TechnicianIn(BaseModel):
    id: str = Field(min_length=1, max_length=64)
    skills: list[str] = Field(default_factory=list, max_length=40)
    territories: list[str] = Field(min_length=1, max_length=50)
    shiftStart: int = Field(ge=0, le=MAX_MINUTE)
    shiftEnd: int = Field(ge=0, le=MAX_MINUTE)
    home: int = Field(ge=0, le=999)
    maxDailyMinutes: int = Field(ge=0, le=MAX_MINUTE)
    overtimeMinutes: int = Field(default=0, ge=0, le=1440)


class WorkOrderIn(BaseModel):
    id: str = Field(min_length=1, max_length=64)
    territory: str = Field(min_length=1, max_length=64)
    skills: list[str] = Field(default_factory=list, max_length=30)
    duration: int = Field(ge=1, le=1440)
    location: int = Field(ge=0, le=999)
    priority: int = Field(default=4, ge=1, le=9)
    windowStart: int | None = Field(default=None, ge=0, le=MAX_MINUTE)
    windowEnd: int | None = Field(default=None, ge=0, le=MAX_MINUTE)
    due: int | None = Field(default=None, ge=0, le=MAX_MINUTE)


class PinnedIn(BaseModel):
    workOrderId: str = Field(min_length=1, max_length=64)
    technicianId: str = Field(min_length=1, max_length=64)
    start: int = Field(ge=0, le=MAX_MINUTE)
    end: int = Field(ge=0, le=MAX_MINUTE)
    sequence: int = Field(ge=0, le=999)


class RunIn(BaseModel):
    technicians: list[TechnicianIn] = Field(max_length=100)
    workOrders: list[WorkOrderIn] = Field(max_length=500)
    travel: list[list[Annotated[int, Field(ge=0, le=1440)]]] = Field(max_length=600)
    bufferMinutes: int = Field(default=5, ge=0, le=60)
    pinned: list[PinnedIn] = Field(default_factory=list, max_length=500)
    timeLimitSeconds: float = Field(default=DEFAULT_TIME_LIMIT_S, ge=1, le=300)
    workers: int = Field(default=DEFAULT_WORKERS, ge=1, le=16)


class ProblemError(Exception):
    def __init__(self, status: int, code: str, detail: str, errors: dict[str, str] | None = None) -> None:
        self.status, self.code, self.detail, self.errors = status, code, detail, errors


def _problem(request: Request, exc: ProblemError) -> JSONResponse:
    title = {400: "Bad Request", 401: "Unauthorized", 403: "Forbidden", 404: "Not Found", 422: "Unprocessable Entity",
             429: "Too Many Requests"}.get(exc.status, "Error")
    body: dict[str, Any] = {
        "type": PROBLEM_TYPE + exc.code, "title": title, "status": exc.status, "detail": exc.detail, "code": exc.code,
        "traceId": request.headers.get("x-request-id") or str(uuid.uuid4()),
    }
    if exc.errors:
        body["errors"] = exc.errors
    return JSONResponse(body, status_code=exc.status, media_type="application/problem+json")


def build_problem(req: RunIn) -> tuple[Problem, list[Assignment]]:
    try:
        problem = Problem(
            technicians=tuple(
                Technician(t.id, frozenset(t.skills), frozenset(t.territories), t.shiftStart, t.shiftEnd, t.home, t.maxDailyMinutes, t.overtimeMinutes)
                for t in req.technicians
            ),
            work_orders=tuple(
                WorkOrder(w.id, w.territory, frozenset(w.skills), w.duration, w.location, w.priority, w.windowStart, w.windowEnd, w.due)
                for w in req.workOrders
            ),
            travel=tuple(tuple(row) for row in req.travel),
            buffer_minutes=req.bufferMinutes,
        )
    except ValueError as exc:
        raise ProblemError(422, "SVC-DSP-001", str(exc)) from exc
    pinned = [Assignment(p.workOrderId, p.technicianId, p.start, p.end, p.sequence) for p in req.pinned]
    violations = plan_violations(problem, pinned)
    if violations:
        raise ProblemError(422, "SVC-DSP-001", "pinned assignments are infeasible", {"pinned": "; ".join(violations)})
    return problem, pinned


def create_app(verifier: ServiceVerifier | None = None, runs: RunStore | None = None) -> FastAPI:
    app = FastAPI(title="passage-dispatch", version="0.2.0")
    store = runs or RunStore()
    log = logging.getLogger("passage_dispatch.api")

    def require(duty: str):
        def dependency(request: Request) -> Caller:
            token = request.headers.get(auth.HEADER)
            if not token or verifier is None:
                raise ProblemError(401, "AUTH-0001", "missing service token" if verifier else "service tokens are not configured")
            try:
                caller = verifier.verify(token)
            except TokenError as exc:
                log.warning("service token refused", extra={"reason": str(exc), "correlationId": request.headers.get("x-request-id")})
                raise ProblemError(401, "AUTH-0001", "invalid service token") from exc
            if duty not in caller.duties:
                raise ProblemError(403, "AUTH-0003", f"missing duty {duty}")
            return caller

        return dependency

    @app.exception_handler(ProblemError)
    async def problem_handler(request: Request, exc: ProblemError) -> JSONResponse:
        return _problem(request, exc)

    @app.exception_handler(RequestValidationError)
    async def validation_handler(request: Request, exc: RequestValidationError) -> JSONResponse:
        errors = {".".join(str(p) for p in e["loc"] if p != "body"): e["msg"] for e in exc.errors()}
        return _problem(request, ProblemError(400, "COM-0001", "request failed validation", errors))

    @app.get("/healthz")
    def healthz() -> dict[str, str]:
        return {"status": "ok"}

    @app.get("/readyz")
    def readyz() -> JSONResponse:
        if verifier is None:
            return JSONResponse({"status": "unavailable", "service": SERVICE}, status_code=503)
        return JSONResponse({"status": "ready", "service": SERVICE})

    @app.get("/metrics")
    def metrics() -> PlainTextResponse:
        lines = ["# HELP dispatch_runs_total Optimizer runs finished, by outcome.", "# TYPE dispatch_runs_total counter"]
        lines += [f'dispatch_runs_total{{outcome="{k}"}} {store.outcomes[k]}' for k in (SUCCEEDED, FAILED)]
        lines += ["# HELP dispatch_solver_fallbacks_total Runs answered by the greedy solver (SVC-DSP-002), by reason.",
                  "# TYPE dispatch_solver_fallbacks_total counter"]
        lines += [f'dispatch_solver_fallbacks_total{{reason="{k}"}} {v}' for k, v in sorted(store.fallbacks.items())]
        return PlainTextResponse("\n".join(lines) + "\n")

    @app.post("/v1/dispatch/runs", status_code=202)
    def create_run(
        req: RunIn,
        request: Request,
        caller: Caller = Depends(require(auth.DUTY_CREATE)),
        idempotency_key: str | None = Header(default=None, alias="Idempotency-Key", max_length=255),
    ) -> JSONResponse:
        problem, pinned = build_problem(req)
        request_hash = hashlib.sha256(json.dumps(req.model_dump(), sort_keys=True).encode()).hexdigest()
        try:
            run, replayed = store.submit(
                caller, problem, pinned, req.timeLimitSeconds, req.workers, idempotency_key, request_hash,
                request.headers.get("x-request-id") or str(uuid.uuid4()),
            )
        except IdempotencyReuse as exc:
            raise ProblemError(422, "idempotency_key_reuse", "Idempotency-Key was already used with a different request body") from exc
        except QueueFull as exc:
            raise ProblemError(429, "SVC-DSP-012", "too many optimizer runs are waiting; retry later") from exc
        headers = {"Location": f"/v1/dispatch/runs/{run.id}"}
        if replayed:
            headers["Idempotent-Replayed"] = "true"
        return JSONResponse(run.view(), status_code=200 if replayed else 202, headers=headers)

    @app.get("/v1/dispatch/runs/{run_id}")
    def get_run(run_id: str, caller: Caller = Depends(require(auth.DUTY_READ))) -> dict[str, Any]:
        run = store.get(caller, run_id)
        if run is None:
            raise ProblemError(404, "SVC-DSP-013", "dispatch run not found")
        return run.view()

    return app
