# forecast-py (wave 22, PSG-22, slices 1, 2 and 6)

Statistical forecast baseline for master planning (PRD FR-22.9). Numeric work
only, per the Python rules in the engineering conventions.

- **ETS** selected by AICc among A,N (simple smoothing, alpha on a 0.05..1.00
  grid), A,A,N (additive trend) and A,A,A (additive trend and season of
  `seasonLength` periods, eligible from two seasons plus one period). Trend and
  season parameters are picked on a coarse 0.1/0.3/0.6 grid; all candidates are
  scored on the same residuals. Paths are clipped at zero.
- **Croston** and **TSB**: intermittent demand. Auto selection uses TSB when the
  average demand interval (ADI) is above 1.32.
- **Naive**: last value, used when history is under 24 months (PLAN-0012 warning).
- **MAPE / wMAPE**: accuracy helpers; undefined (None) when actuals are zero.

## Layout

```
src/forecast/models.py   pure model functions and selection (no I/O)
src/forecast/api.py      FastAPI app: GET /healthz, GET /readyz, POST /v1/forecast/forecasts:generate
tests/                   table-driven unit tests, hypothesis property test, API tests
charts/forecast/         Helm chart (2 replicas, probes on /healthz and /readyz, ServiceMonitor toggle)
Dockerfile               python:3.12-slim image running uvicorn on :8080
```

## Run and test

```
cd services/forecast-py
python -m venv .venv && . .venv/bin/activate
pip install -e ".[dev]"
pytest
uvicorn forecast.api:app --port 8080
```

## Contract

`POST /v1/forecast/forecasts:generate` takes 1 to 1,000 series, each with a
non-empty, non-negative, finite `history` (at most 600 periods), `model` (`auto` |
`ets` | `croston` | `tsb` | `naive`), `horizonPeriods` (1 to 104) and
`seasonLength` (1 to 24, default 12; 1 turns seasonality off) and optionally
`historyMonths` (12 to 120, the tenant parameter "forecast history months";
only the last that many values are modelled and backtested, and a window under
24 months falls back to naive with PLAN-0012). It returns the
model used, `shortHistory`, the forecast per horizon period (flat for the level
models, trend and season for ETS), `mape` and `wmape` (holdout backtest of the
same path over the last min(12, n/4) periods; null when undefined) and warnings.
Any schema violation (negative or non-finite history, unknown model) is a 422.

## Error codes

| HTTP | Meaning |
|---|---|
| 422 | Request fails schema validation: empty batch or history, negative or non-finite value, unknown model, horizon, season length or history months out of range |
| warning `PLAN-0012` | History shorter than 24 periods; naive model used |

## Wave reference

PSG-22 (Phase 4). The Go planning service will call this endpoint once the
forecast batch job is built (see NOT-IMPLEMENTED.md). `statsforecast` is not a
dependency yet; it is the planned replacement for the in-house ETS and Croston code.
