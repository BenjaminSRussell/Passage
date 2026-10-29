# NOT-IMPLEMENTED: evals (PSG-47, wave 47)

Slice 1 builds the gate, the metrics, the harness and a CLI over seed datasets,
with tests. The rows below are not built.

| Deliverable / criterion | Status | Reason |
|---|---|---|
| Full fixtures: `eval-ap-coding-v3` (2,000 invoices, 40 vendors, 3 entities), `eval-cash-app-v2` (1,500 payments, 12% partials, 5% multi-invoice) (AC-47.3, AC-47.4) | later slice of PSG-47 | Needs wave-19 fixtures and hand labels. Only small seed sets are checked in |
| `eval-mrp-triage-v1` (800 action messages), `eval-anomaly-v1` (50k journals, 400 injected anomalies), `eval-ask-data-v1` (300 questions), `adv-prompts-500` (500 adversarial prompts for refusal >= 0.99) | later slice of PSG-47 | Datasets to be labelled with the owning domains. Gate thresholds exist; the data does not |
| CI execution on every prompt, model or tool change, blocking deploy (AC-47.9 / AC1 "evals run in CI") | handoff | CI config lives outside the owned paths (`.github`, deploy pipeline). The CLI exits non-zero on failure, ready for a CI job to call |
| Eval runs persisted to `eval_run` and the eval dashboard (9.47.14) | later slice of PSG-47 | No database layer in this slice |
| Model-card export and eval report (PDF/HTML) (9.47.15) | later slice of PSG-47 | Reports need the persisted runs |
| Nightly production eval on production datasets (9.47.16) | later slice of PSG-47 | Needs the scheduler and tenant datasets |
| Provider parity test: identical results across 4 adapters (AC-47.10, FR-47.13) | later slice of PSG-47 | Adapters are not built (see services/agents-ts/NOT-IMPLEMENTED.md) |
| Payment-date prediction MAE gate (FR-47.15, AC-47.12) | later slice of PSG-47 | The model is Python work outside these owned paths |
| Ask-the-data SQL equivalence and metric selection (FR-47.9) | later slice of PSG-47 | Thresholds exist in `gate.ts`; the Cube query runner and dataset do not |
| Reconciliation: billed tokens vs `agent_run.tokens_*` (9.47.9) | later slice of PSG-47 | Needs persisted runs and provider usage |
| Python eval job runner (`passage-agent-evals`, 9.47.10) | handoff | The PRD names a Python job runner; this slice ships a TypeScript CLI so the gate logic stays in one language. Confirm the choice with the wave owner |
| Helm chart for the eval job | later slice of PSG-47 | No image yet |
