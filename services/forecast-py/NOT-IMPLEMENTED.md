# NOT-IMPLEMENTED (PSG-22, forecast-py, slices 1, 2, 6, 7 and 26)

| Deliverable or AC | Status | Reason |
|---|---|---|
| FR-22.9 ETS with trend and seasonality (seasonal selection by AICc) | built | A,N / A,A,N / A,A,A by AICc on common residuals (`ets_fit`), `seasonLength` on the request, tests in `tests/test_trend_seasonal.py`. |
| ETS damped trend | built | Slice 26: A,Ad,N and A,Ad,A with phi from (0.8, 0.9, 0.98), one more parameter in the AICc, so the damped form wins only on a flattening trend (`tests/test_damped_autoseason.py`). |
| ETS multiplicative error, trend or season | later slice of PSG-22 | Additive forms only; the smoothing grid for trend, season and damping is coarse (0.1, 0.3, 0.6) rather than optimised. |
| Automatic season-length detection | built | Slice 26: `autoSeason: true` on a series detects the length (3, 4, 6 or 12) from the autocorrelation of the detrended history, 1 when none stands out, and every result reports the `seasonLength` it used. Without it the caller's `seasonLength` (default 12) applies. Not built: lengths for weekly buckets, and the planning service does not send `autoSeason` yet (the batch job that calls this service is not built). |
| FR-22.9 `statsforecast` wrapper | later slice of PSG-22 | Not added as a dependency; the in-house models cover the selection rules. Adopt once the dependency review is done. |
| FR-22.9 batch generation of 50k items in under 15 min, batches of 1,000, resumable per batch | partly built | Slice 7: `POST /v1/forecast/forecasts:generate-batches` takes up to 50,000 series, computes them in batches of up to 1,000, reports each batch completed, skipped (listed in `completedBatches`) or failed (a failing series fails its batch only), and `complete` false while any batch failed (`src/forecast/batches.py`, tests in `tests/test_batches.py` with hypothesis properties and `tests/test_api.py`). Not built: the Temporal job in the planning service that calls it weekly, persists completed batches and retries failed ones, parallel batch execution, and a measured 50k-series run under 15 minutes (batches run one after another in the request). |
| FR-22.9 forecast version per run, unpublished on partial failure | later slice of PSG-22 | No persistence in this service; the planning service owns versions. This service reports `complete: false` so the caller can withhold a partial version. |
| FR-22.9 manual override | built in `services/planning` | Forecast cells with If-Match and reduction (`PUT /v1/planning/forecasts/{modelCode}/lines`). |
| FR-22.9 consensus workflow | built in `services/planning` | Submit, publish and reject with segregation of duties (slice 6); the Temporal approval step is listed there. |
| AC-22.7 Croston/TSB chosen for ADI 2.1 history, MAPE reported | built | ADI > 1.32 selects TSB; each result carries `mape` and `wmape` from a holdout backtest (last min(12, n/4) periods). Test `test_ac_22_7_intermittent_history_picks_tsb_and_reports_mape` (24 months, ADI 2.18). |
| PLAN-0012 model fallback message | built | Returned as a warning on short histories. |
| Model selection parameter `forecast history months` (12 to 120) | partly built | `historyMonths` on each series keeps only the last months (tests in `tests/test_api.py`). Not built: reading the tenant parameter (default 36); the caller sends it until the batch job in the planning service does. |
| Tests with the `forecast-history-2k` fixture | later slice of PSG-22 | Fixture dataset not provided in this slice. |
| Python test run in CI | handoff (deployment-cicd) | Pytest is configured and passes locally (Python 3.12); CI wiring belongs to the platform owner. |
| `/metrics` for the ServiceMonitor toggle | later slice of PSG-22 | The chart ships the toggle (off by default) like the other Python services; the endpoint is not exposed yet. |
| `forecast-history-2k` 24-month fixture and weekly Sunday 22:00 schedule | later slice of PSG-22 | Depends on the batch job. |
