"""HTTP surface of the forecast service. Only numeric work lives here (CONVENTIONS: Python)."""

from __future__ import annotations

from typing import Annotated, Literal

from fastapi import FastAPI
from pydantic import BaseModel, Field

from forecast import models

app = FastAPI(title="Passage forecast", version="0.2.0")


# A history value is a non-negative, finite quantity; anything else is a 422.
Quantity = Annotated[float, Field(ge=0, allow_inf_nan=False)]


class Series(BaseModel):
    itemId: str = Field(min_length=1, max_length=64)
    siteId: str = Field(min_length=1, max_length=64)
    history: list[Quantity] = Field(min_length=1, max_length=600)
    model: Literal["auto", "ets", "croston", "tsb", "naive"] = "auto"
    horizonPeriods: int = Field(default=12, ge=1, le=104)
    # Periods per season for the seasonal ETS candidate; 1 turns seasonality off.
    seasonLength: int = Field(default=models.DEFAULT_SEASON, ge=1, le=24)


class GenerateRequest(BaseModel):
    series: list[Series] = Field(min_length=1, max_length=1000)


class SeriesResult(BaseModel):
    itemId: str
    siteId: str
    model: str
    shortHistory: bool
    forecast: list[float]
    mape: float | None
    wmape: float | None
    warnings: list[str]


class GenerateResponse(BaseModel):
    results: list[SeriesResult]


@app.get("/healthz")
def healthz() -> dict[str, str]:
    return {"status": "ok"}


@app.get("/readyz")
def readyz() -> dict[str, str]:
    # Stateless: ready as soon as the process serves requests.
    return {"status": "ready"}


@app.post("/v1/forecast/forecasts:generate", response_model=GenerateResponse)
def generate(req: GenerateRequest) -> GenerateResponse:
    results: list[SeriesResult] = []
    for s in req.series:
        fc = models.select_model(s.history, s.model, horizon=s.horizonPeriods, season=s.seasonLength)
        warnings = ["PLAN-0012"] if fc.short_history else []
        mape, wmape = models.backtest(s.history, fc.model, season=s.seasonLength)
        results.append(
            SeriesResult(
                itemId=s.itemId,
                siteId=s.siteId,
                model=fc.model,
                shortHistory=fc.short_history,
                forecast=list(fc.path),
                mape=mape,
                wmape=wmape,
                warnings=warnings,
            )
        )
    return GenerateResponse(results=results)
