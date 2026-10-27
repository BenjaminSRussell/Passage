"""HTTP surface of scheduler-opt: one optimize endpoint plus health probes.

POST /v1/optimize/job-shop solves one window of at most 2,000 operations and
reports the CP-SAT status. The caller keeps its heuristic schedule when the
status is UNKNOWN (BR-23.9). The Go scheduling service does not call this yet
(see services/scheduling/NOT-IMPLEMENTED.md). Errors use FastAPI's default 422
body.
"""

from __future__ import annotations

from typing import Any

from pydantic import BaseModel, Field

from scheduler_opt import jobshop

SERVICE_NAME = "scheduler-opt"


class OperationIn(BaseModel):
    machine: int = Field(ge=0, le=499)
    minutes: int = Field(ge=1, le=100000)


class JobIn(BaseModel):
    operations: list[OperationIn] = Field(min_length=1, max_length=200)
    due: int | None = Field(default=None, ge=0, description="Due minute; omit when the job has no due date.")
    weight: int = Field(default=1, ge=0, le=1000)


class OptimizeIn(BaseModel):
    machines: int = Field(ge=1, le=500)
    jobs: list[JobIn] = Field(min_length=1, max_length=jobshop.MAX_OPERATIONS)
    timeLimitSeconds: float = Field(default=jobshop.DEFAULT_TIME_LIMIT_S, ge=5, le=1800)
    lambdaTenths: int = Field(default=jobshop.DEFAULT_LAMBDA_TENTHS, ge=0, le=1000)


class OptimizeOut(BaseModel):
    status: str
    starts: list[list[int]]
    makespan: int
    totalLateness: int
    objective: float | None


def _to_instance(req: OptimizeIn) -> jobshop.Instance:
    jobs = [
        jobshop.Job(
            operations=[jobshop.Operation(machine=o.machine, minutes=o.minutes) for o in j.operations],
            due=j.due,
            weight=j.weight,
        )
        for j in req.jobs
    ]
    return jobshop.Instance(machines=req.machines, jobs=jobs)


def create_app() -> Any:
    """Build the FastAPI application. Imported lazily so tests can skip without it."""
    from fastapi import FastAPI, HTTPException

    app = FastAPI(title="scheduler-opt", version="0.1.0")

    @app.get("/healthz")
    def healthz() -> dict[str, str]:
        return {"status": "ok"}

    @app.get("/readyz")
    def readyz() -> dict[str, str]:
        return {"status": "ready", "service": SERVICE_NAME}

    @app.post("/v1/optimize/job-shop", response_model=OptimizeOut)
    def optimize(req: OptimizeIn) -> OptimizeOut:
        inst = _to_instance(req)
        if inst.operation_count > jobshop.MAX_OPERATIONS:
            raise HTTPException(status_code=422, detail="window exceeds 2000 operations; split the run")
        problems = jobshop.validate_instance(inst)
        if problems:
            raise HTTPException(status_code=422, detail="; ".join(problems))
        sol = jobshop.solve(inst, time_limit_s=req.timeLimitSeconds, lambda_tenths=req.lambdaTenths)
        return OptimizeOut(
            status=sol.status,
            starts=sol.starts,
            makespan=sol.makespan,
            totalLateness=sol.total_lateness,
            objective=sol.objective,
        )

    return app
