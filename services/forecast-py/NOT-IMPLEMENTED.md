# NOT-IMPLEMENTED (PSG-22, forecast-py, slice 1)

| Deliverable or AC | Status | Reason |
|---|---|---|
| FR-22.9 ETS with trend and seasonality (seasonal selection by AICc) | later slice of PSG-22 | Slice 1 implements ETS(A,N) only. |
| FR-22.9 `statsforecast` wrapper | later slice of PSG-22 | Not added as a dependency; the in-house models cover the slice-1 selection rules. Adopt once the dependency review is done. |
| FR-22.9 batch generation of 50k items in under 15 min, batches of 1,000, resumable per batch | later slice of PSG-22 | The endpoint takes up to 1,000 series per call. The Temporal batch job and the partial-version rule are not built. |
| FR-22.9 forecast version per run, unpublished on partial failure | later slice of PSG-22 | No persistence in this service; the planning service owns versions. |
| FR-22.9 manual override and consensus workflow | later slice of PSG-22 | Owned by the planning service and Temporal. |
| AC-22.7 Croston/TSB chosen for ADI 2.1 history, MAPE reported | built | ADI > 1.32 selects TSB; each result carries `mape` and `wmape` from a holdout backtest (last min(12, n/4) periods). Test `test_ac_22_7_intermittent_history_picks_tsb_and_reports_mape` (24 months, ADI 2.18). |
| PLAN-0012 model fallback message | built | Returned as a warning on short histories. |
| Model selection parameter `forecast history months` (12 to 120) | later slice of PSG-22 | Window is set by the caller through the history length. |
| Tests with the `forecast-history-2k` fixture | later slice of PSG-22 | Fixture dataset not provided in this slice. |
| Python test run in CI | handoff (deployment-cicd) | Pytest is configured and passes locally (Python 3.12); CI wiring belongs to the platform owner. |
| `/metrics` for the ServiceMonitor toggle | later slice of PSG-22 | The chart ships the toggle (off by default) like the other Python services; the endpoint is not exposed yet. |
| `forecast-history-2k` 24-month fixture and weekly Sunday 22:00 schedule | later slice of PSG-22 | Depends on the batch job. |
