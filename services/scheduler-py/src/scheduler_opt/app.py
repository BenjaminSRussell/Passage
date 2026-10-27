"""HTTP surface of scheduler-opt: two optimize endpoints plus health probes.

POST /v1/optimize/job-shop solves one window of at most 2,000 operations and
reports the CP-SAT status; operations may choose among candidate machines and
follow resource-network edges (FR-23.13). The caller keeps its heuristic
schedule when the status is UNKNOWN (BR-23.9). POST
/v1/optimize/campaign-sequence orders a campaign's orders by changeover
minutes with AddCircuit (FR-23.14). The Go scheduling service does not call
either yet (see services/scheduling/NOT-IMPLEMENTED.md). Errors use FastAPI's
default 422 body.
"""

from __future__ import annotations

from typing import Any

from pydantic import BaseModel, Field

from scheduler_opt import campaign, jobshop

SERVICE_NAME = "scheduler-opt"


class EdgeIn(BaseModel):
    fromMachine: int = Field(ge=0, le=499)
    toMachine: int = Field(ge=0, le=499)
    transferMinutes: int = Field(default=0, ge=0, le=10080)
    maxWaitMinutes: int | None = Field(default=None, ge=0, le=10080)


class OperationIn(BaseModel):
    machine: int = Field(ge=0, le=499)
    minutes: int = Field(ge=1, le=100000)
    candidates: list[int] = Field(
        default_factory=list, max_length=20, description="Machines the operation may run on, machine first."
    )
    network: list[EdgeIn] | None = Field(
        default=None,
        min_length=1,
        max_length=500,
        description="Edges governing the step from the previous operation into this one (FR-23.13).",
    )


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
    machines: list[list[int]]
    makespan: int
    totalLateness: int
    objective: float | None


class CampaignIn(BaseModel):
    # n+1 rows: row and column 0 are the resource's current setup state.
    changeover: list[list[int]] = Field(min_length=2, max_length=campaign.MAX_CAMPAIGN_ORDERS + 1)
    timeLimitSeconds: float = Field(default=campaign.DEFAULT_CAMPAIGN_LIMIT_S, ge=1, le=300)


class CampaignOut(BaseModel):
    status: str
    sequence: list[int]
    changeoverMinutes: int


def _to_operation(o: OperationIn) -> jobshop.Operation:
    network = None
    if o.network is not None:
        network = tuple(
            jobshop.Edge(e.fromMachine, e.toMachine, transfer=e.transferMinutes, max_wait=e.maxWaitMinutes)
            for e in o.network
        )
    return jobshop.Operation(machine=o.machine, minutes=o.minutes, candidates=tuple(o.candidates), network=network)


def _to_instance(req: OptimizeIn) -> jobshop.Instance:
    jobs = [
        jobshop.Job(operations=[_to_operation(o) for o in j.operations], due=j.due, weight=j.weight) for j in req.jobs
    ]
    return jobshop.Instance(machines=req.machines, jobs=jobs)


def create_app() -> Any:
    """Build the FastAPI application. Imported lazily so tests can skip without it."""
    from fastapi import FastAPI, HTTPException

    app = FastAPI(title="scheduler-opt", version="0.2.0")

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
            machines=sol.machines,
            makespan=sol.makespan,
            totalLateness=sol.total_lateness,
            objective=sol.objective,
        )

    @app.post("/v1/optimize/campaign-sequence", response_model=CampaignOut)
    def sequence(req: CampaignIn) -> CampaignOut:
        problems = campaign.validate_matrix(req.changeover)
        if problems:
            raise HTTPException(status_code=422, detail="; ".join(problems))
        res = campaign.sequence_campaign(req.changeover, time_limit_s=req.timeLimitSeconds)
        return CampaignOut(status=res.status, sequence=res.sequence, changeoverMinutes=res.changeover_minutes)

    return app
