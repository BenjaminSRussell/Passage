from fastapi import FastAPI
from pydantic import BaseModel, Field

from configurator.model import load_fixture
from configurator.solve import solve

app = FastAPI(title="passage-configurator", version="0.1.0")
FIXTURE = load_fixture()


class SolveRequest(BaseModel):
    selections: dict[str, str] = Field(default_factory=dict)


@app.get("/healthz")
def healthz() -> dict[str, str]:
    return {"status": "ok"}


@app.get("/readyz")
def readyz() -> dict[str, str]:
    return {"status": "ready"}


@app.post("/v1/configurations:solve")
def solve_endpoint(req: SolveRequest) -> dict:
    return solve(FIXTURE, req.selections)


@app.post("/v1/configurations:validate")
def validate_endpoint(req: SolveRequest) -> dict:
    return solve(FIXTURE, req.selections)
