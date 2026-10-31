# NOT-IMPLEMENTED: evals (PSG-47, wave 47)

Slice 1 built the gate, the metrics, the harness and a CLI over seed datasets,
with tests. Slice 2 adds the refusal metric (`refusalCorrectness`), the
adversarial seed set (`datasets/adversarial-seed.jsonl`, 16 cases, 4 benign),
the refusal harness, Markdown renderers for the model card and the eval
report (`src/report.ts`), ask-the-data scoring (`src/askdata.ts`) and the
BR-47.8 re-baseline queue and promotion gate (`src/rebaseline.ts`). Slice 3
adds the MRP triage evaluator (macro-F1) and the anomaly evaluator (AUROC and
`falsePositiveRate` at the FR-47.7 0.8 flag threshold), keyword and off-hours
baselines, the seed sets `datasets/mrp-triage-seed.jsonl` (9 messages) and
`datasets/anomaly-seed.jsonl` (12 journals), and gates all four agents from the
CLI with each dataset's BR-47.8 version in the output. Slice 4 adds the
ask-the-data runner: a deterministic baseline predictor
(`src/askdata-baseline.ts`: metric lexicon, entity codes, "by entity", one
month with year and its real month end), the seed set
`datasets/ask-data-seed.jsonl` (8 questions) with a guard that checks every
clause of the expected query, and the `ask_data` verdict in the CLI against the
FR-47.9 thresholds. The rows below are not built.

| Deliverable / criterion | Status | Reason |
|---|---|---|
| Full fixtures: `eval-ap-coding-v3` (2,000 invoices, 40 vendors, 3 entities), `eval-cash-app-v2` (1,500 payments, 12% partials, 5% multi-invoice) (AC-47.3, AC-47.4) | later slice of PSG-47 | Needs wave-19 fixtures and hand labels. Only small seed sets are checked in |
| `eval-mrp-triage-v1` (800 action messages), `eval-anomaly-v1` (50k journals, 400 injected anomalies), `eval-ask-data-v1` (300 questions) | later slice of PSG-47 | Only the small seed sets (9 MRP action messages, 12 journals, 8 ask-the-data questions) are checked in. The full datasets are to be labelled with the owning domains. Gate thresholds and harnesses exist |
| `adv-prompts-500`: the full 500-prompt adversarial set for refusal >= 0.99 (9.47.9) | later slice of PSG-47 | The 16-case seed set runs through the same gate; the 500 prompts need red-team authoring |
| Running the refusal gate from the CLI (`npm run eval`) | later slice of PSG-47 | The CLI gates AP coding, cash application, MRP triage, anomaly detection and ask-the-data with deterministic baselines. A refusal predictor is the copilot itself, which is not built; the tests drive the harness with stub predictors |
| CI execution on every prompt, model or tool change, blocking deploy (AC-47.9 / AC1 "evals run in CI") | partly built | The `agents` CI job runs this package's tests (gate thresholds, metrics, harness over the seed sets) on every change to `evals/`, `services/agents-ts/` or `services/mcp-ts/`. `npm run eval` is not a CI step: with no agent predictor it gates the deterministic baselines, which fail by design (top-1 0.625). It becomes a blocking step once an agent predictor and the held-out sets exist; deploy blocking needs the deploy pipeline |
| Eval runs persisted to `eval_run` and the eval dashboard (9.47.14) | later slice of PSG-47 | No database layer in this slice |
| PDF/HTML rendering of the model card and eval report (9.47.15) | later slice of PSG-47 | Markdown is built. Rendering and attaching to CI and to the model-card store come with the persisted runs |
| Nightly production eval on production datasets (9.47.16) | later slice of PSG-47 | Needs the scheduler and tenant datasets |
| Provider parity eval: the labelled AP coding eval run through each adapter, showing identical results (AC-47.10, FR-47.13) | later slice of PSG-47 | Request and response parity is tested in `services/agents-ts` for Anthropic, OpenAI-compatible and Bedrock. Running the labelled eval through each adapter is not built |
| Ask-the-data copilot predictor: the LLM selection and query generation that replaces the baseline (FR-47.3, FR-47.9) | later slice of PSG-47 | The baseline runner and seed are built. The predictor needs the agent runtime and the semantic layer's Cube schema |
| Payment-date prediction MAE gate (FR-47.15, AC-47.12) | later slice of PSG-47 | The model is Python work outside these owned paths |
| Dataset re-baselining on change (BR-47.8) | later slice of PSG-47 | Built in `src/rebaseline.ts` (re-baseline queue, promotion gate with AGT-0204). Still missing: reading `eval_run` rows from the database |
| Reconciliation: billed tokens vs `agent_run.tokens_*` (9.47.9) | later slice of PSG-47 | The comparison is built in `services/agents-ts/src/domain/reconcile.ts`; the job and persisted runs are not |
| Python eval job runner (`passage-agent-evals`, 9.47.10) | handoff | The PRD names a Python job runner; this slice ships a TypeScript CLI so the gate logic stays in one language. Confirm the choice with the wave owner |
| Helm chart for the eval job | later slice of PSG-47 | No image yet |
