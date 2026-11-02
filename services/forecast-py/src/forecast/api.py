"""HTTP surface of the forecast service. Only numeric work lives here (CONVENTIONS: Python)."""

from __future__ import annotations

from typing import Annotated, Literal

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel, Field

from forecast import batches, models

app = FastAPI(title="Passage forecast", version="0.4.0")


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
    # When true the season length is detected from the history (autocorrelation
    # of the detrended series over 3, 4, 6 and 12 periods; 1 when none stands
    # out) and seasonLength is ignored. The result reports the length used.
    autoSeason: bool = False
    # Tenant parameter "forecast history months" (4.22.13, 12 to 120; the
    # planning service sends its default of 36). Only the last historyMonths
    # values of the monthly history are modelled; omitted, all of it is.
    historyMonths: int | None = Field(default=None, ge=12, le=120)


class GenerateRequest(BaseModel):
    series: list[Series] = Field(min_length=1, max_length=1000)


class SeriesResult(BaseModel):
    itemId: str
    siteId: str
    model: str
    shortHistory: bool
    # Season length offered to the seasonal ETS candidate (detected or sent); that
    # candidate is eligible only with two seasons plus one period of history.
    seasonLength: int
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


def _compute(s: Series) -> SeriesResult:
    """Forecast one validated series."""
    history = s.history if s.historyMonths is None else s.history[-s.historyMonths :]
    season = models.detect_season(history) if s.autoSeason else s.seasonLength
    fc = models.select_model(history, s.model, horizon=s.horizonPeriods, season=season)
    warnings = ["PLAN-0012"] if fc.short_history else []
    mape, wmape = models.backtest(history, fc.model, season=season)
    return SeriesResult(
        itemId=s.itemId,
        siteId=s.siteId,
        model=fc.model,
        shortHistory=fc.short_history,
        seasonLength=season,
        forecast=list(fc.path),
        mape=mape,
        wmape=wmape,
        warnings=warnings,
    )


@app.post("/v1/forecast/forecasts:generate", response_model=GenerateResponse)
def generate(req: GenerateRequest) -> GenerateResponse:
    return GenerateResponse(results=[_compute(s) for s in req.series])


class BatchGenerateRequest(BaseModel):
    series: list[Series] = Field(min_length=1, max_length=batches.MAX_SERIES)
    batchSize: int = Field(default=batches.MAX_BATCH_SIZE, ge=1, le=batches.MAX_BATCH_SIZE)
    # Indexes of batches the caller already persisted from an earlier call over
    # the same series and batch size; they are skipped, not recomputed.
    completedBatches: list[Annotated[int, Field(ge=0)]] = Field(default_factory=list, max_length=batches.MAX_SERIES)


class BatchOut(BaseModel):
    index: int
    status: Literal["completed", "skipped", "failed"]
    resultCount: int
    results: list[SeriesResult]
    detail: str


class BatchGenerateResponse(BaseModel):
    batches: list[BatchOut]
    totalBatches: int
    # False when any batch failed; the caller must not publish a partial version.
    complete: bool


@app.post("/v1/forecast/forecasts:generate-batches", response_model=BatchGenerateResponse)
def generate_batches(req: BatchGenerateRequest) -> BatchGenerateResponse:
    """Generate a run of up to 50,000 series in batches (FR-22.9, 4.22.16).

    A batch that fails is reported as failed while the others still run, and the
    run is then not complete; the caller retries by sending the indexes of the
    batches it holds as completedBatches.
    """
    total = batches.count(len(req.series), req.batchSize)
    beyond = [i for i in req.completedBatches if i >= total]
    if beyond:
        raise HTTPException(status_code=422, detail=f"completedBatches {beyond} beyond the last batch index {total - 1}")
    outcomes = batches.run(req.series, req.batchSize, req.completedBatches, _compute)
    out = [
        BatchOut(index=o.index, status=o.status, resultCount=len(o.results), results=o.results, detail=o.detail)
        for o in outcomes
    ]
    return BatchGenerateResponse(batches=out, totalBatches=total, complete=batches.is_complete(outcomes))
