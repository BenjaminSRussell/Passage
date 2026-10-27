"""HTTP layer for the optimizer (FR-34.2, FR-34.18), called by services/transport.

Wire format is camelCase JSON (platform convention). Routes:

- ``POST /v1/routes:solve``  VRP over stops and vehicles (BR-34.1).
- ``POST /v1/load-plans:stack``  layer-based load plan for a trailer (FR-34.18).
- ``GET /healthz``  liveness.

Coordinates are planar kilometres in a local projection; the OSRM matrix
adapter is a later slice. Errors use HTTP status codes: 422 for invalid input,
503 when the solver returns no solution within the time limit.
"""

from __future__ import annotations

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel, ConfigDict, Field, model_validator
from pydantic.alias_generators import to_camel

from tms_optimizer import stacking, vrp

app = FastAPI(title="passage-tms-optimizer", version="0.1.0")


class _Model(BaseModel):
    model_config = ConfigDict(alias_generator=to_camel, populate_by_name=True, extra="forbid")


class PointIn(_Model):
    x: float
    y: float


class StopIn(_Model):
    id: str = Field(min_length=1, max_length=64)
    point: PointIn
    earliest: int = Field(ge=0)
    latest: int = Field(ge=0)
    service_min: int = Field(ge=0)
    weight_kg: int = Field(ge=0)
    volume_l: int = Field(ge=0)
    pallets: int = Field(ge=0)

    @model_validator(mode="after")
    def _window_order(self) -> StopIn:
        if self.earliest > self.latest:
            raise ValueError("earliest must not exceed latest")
        return self


class VehicleIn(_Model):
    id: str = Field(min_length=1, max_length=64)
    capacity_kg: int = Field(ge=0)
    capacity_l: int = Field(ge=0)
    pallets: int = Field(ge=0)
    fixed_cost: int = Field(default=0, ge=0)
    max_route_min: int = Field(default=vrp.DEFAULT_MAX_ROUTE_MIN, gt=0)


class SolveIn(_Model):
    depot: PointIn
    stops: list[StopIn] = Field(max_length=500)
    vehicles: list[VehicleIn] = Field(min_length=1, max_length=50)
    speed_kmh: float = Field(default=60.0, gt=0)
    lateness_per_min: int = Field(default=vrp.DEFAULT_LATENESS_PER_MIN, ge=0)
    time_limit_s: int = Field(default=vrp.DEFAULT_TIME_LIMIT_S, ge=5, le=600)


class TrailerIn(_Model):
    length_mm: int = Field(gt=0)
    width_mm: int = Field(gt=0)
    max_height_mm: int = Field(gt=0)
    max_weight_kg: int = Field(gt=0)
    kingpin_mm: int = Field(default=0, ge=0)
    axle_mm: int = Field(default=0, ge=0)


class PalletIn(_Model):
    id: str = Field(min_length=1, max_length=64)
    length_mm: int = Field(gt=0)
    width_mm: int = Field(gt=0)
    height_mm: int = Field(gt=0)
    weight_kg: int = Field(ge=0)
    stackable: bool
    stop_seq: int = Field(default=0, ge=0)
    max_stack_kg: int | None = Field(default=None, ge=0)


class LoadPlanIn(_Model):
    trailer: TrailerIn
    pallets: list[PalletIn] = Field(max_length=200)


@app.get("/healthz")
def healthz() -> dict:
    return {"status": "ok"}


@app.post("/v1/routes:solve")
def solve_routes(body: SolveIn) -> dict:
    ids = [s.id for s in body.stops]
    if len(set(ids)) != len(ids):
        raise HTTPException(status_code=422, detail="duplicate stop id")
    vehicle_ids = [v.id for v in body.vehicles]
    if len(set(vehicle_ids)) != len(vehicle_ids):
        raise HTTPException(status_code=422, detail="duplicate vehicle id")
    problem = vrp.Problem(
        depot=vrp.Point(body.depot.x, body.depot.y),
        stops=tuple(
            vrp.Stop(
                id=s.id,
                point=vrp.Point(s.point.x, s.point.y),
                earliest=s.earliest,
                latest=s.latest,
                service_min=s.service_min,
                weight_kg=s.weight_kg,
                volume_l=s.volume_l,
                pallets=s.pallets,
            )
            for s in body.stops
        ),
        vehicles=tuple(
            vrp.Vehicle(
                id=v.id,
                capacity_kg=v.capacity_kg,
                capacity_l=v.capacity_l,
                pallets=v.pallets,
                fixed_cost=v.fixed_cost,
                max_route_min=v.max_route_min,
            )
            for v in body.vehicles
        ),
        speed_kmh=body.speed_kmh,
        lateness_per_min=body.lateness_per_min,
        time_limit_s=body.time_limit_s,
    )
    try:
        sol = vrp.solve(problem)
    except vrp.NoSolutionError as exc:
        raise HTTPException(status_code=503, detail=str(exc)) from exc
    return {
        "routes": [
            {
                "vehicleId": r.vehicle_id,
                "stops": [
                    {"stopId": s.id, "arrivalMin": s.arrival_min, "lateMin": s.late_min} for s in r.stops
                ],
                "distanceKm": r.distance_km,
                "endMin": r.end_min,
            }
            for r in sol.routes
        ],
        "dropped": list(sol.dropped),
    }


@app.post("/v1/load-plans:stack")
def stack_load(body: LoadPlanIn) -> dict:
    trailer = stacking.Trailer(
        length_mm=body.trailer.length_mm,
        width_mm=body.trailer.width_mm,
        max_height_mm=body.trailer.max_height_mm,
        max_weight_kg=body.trailer.max_weight_kg,
        kingpin_mm=body.trailer.kingpin_mm,
        axle_mm=body.trailer.axle_mm,
    )
    pallets = [
        stacking.Pallet(
            id=p.id,
            length_mm=p.length_mm,
            width_mm=p.width_mm,
            height_mm=p.height_mm,
            weight_kg=p.weight_kg,
            stackable=p.stackable,
            stop_seq=p.stop_seq,
            max_stack_kg=p.max_stack_kg,
        )
        for p in body.pallets
    ]
    try:
        plan = stacking.plan_load(trailer, pallets)
    except ValueError as exc:
        raise HTTPException(status_code=422, detail=str(exc)) from exc
    return {
        "placements": [
            {
                "palletIds": list(pl.pallet_ids),
                "xMm": pl.x_mm,
                "yMm": pl.y_mm,
                "lengthMm": pl.length_mm,
                "widthMm": pl.width_mm,
                "heightMm": pl.height_mm,
                "weightKg": pl.weight_kg,
            }
            for pl in plan.placements
        ],
        "unplaced": list(plan.unplaced),
        "floorPositionsUsed": plan.floor_positions_used,
        "lengthUsedMm": plan.length_used_mm,
        "layers": plan.layers,
        "totalWeightKg": plan.total_weight_kg,
        "loadSequence": list(plan.load_sequence),
        "axle": {"kingpinKg": plan.kingpin_kg, "axleKg": plan.axle_kg},
    }
