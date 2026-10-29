# evals (wave 47, PSG-47)

The eval harness for the copilot and agents (PRD 9.47 FR-47.9, BR-47.8). A
release that fails a threshold is blocked.

| Module | Responsibility |
|---|---|
| `src/gate.ts` | Deploy thresholds from FR-47.9 per agent: AP coding top-1 >= 0.92 and top-3 >= 0.98; cash application precision >= 0.90 and recall >= 0.75; MRP triage macro-F1 >= 0.80; anomaly AUROC >= 0.85 and FPR <= 0.05; ask-the-data selection >= 0.95 and SQL equivalence >= 0.90; refusal >= 0.99. `gate()` also fails any drop of more than 1 point against the previous release |
| `src/metrics.ts` | `accuracyTopK`, `precisionRecall`, `macroF1`, `auroc`, `falsePositiveRate`, `refusalCorrectness` and MAE (pure functions) |
| `src/harness.ts` | `evaluateApCoding`, `evaluateCashApp`, `evaluateMrpTriage` (macro-F1), `evaluateAnomaly` (AUROC, and FPR with a journal flagged when its score is above 0.8, FR-47.7) and `evaluateRefusal`: run a predictor over labelled cases and return metrics. The refusal harness also counts missed attacks and over-refusals |
| `src/askdata.ts` | Ask-the-data scoring: metric-selection accuracy and SQL equivalence on a canonical form of the semantic-layer query (member order and case ignored; measures, filters, positional date ranges and limits kept) |
| `src/rebaseline.ts` | BR-47.8: `rebaselineQueue` lists agent versions without a passing eval on the current dataset version; `canPromote` refuses promotion with AGT-0204 otherwise |
| `src/report.ts` | Markdown model card (refused for a version whose gate failed) and eval report (PRD 9.47.15) |
| `src/datasets.ts` | Dataset types, the JSONL loader and `datasetVersion` (content hash). Datasets are versioned data, not code |
| `src/baselines.ts` | Reference predictors that the gate is measured against: vendor history (AP coding), exact remittance amount (cash application), cause keywords (MRP triage) and off-hours posting (anomaly) |
| `src/run.ts` | CLI: loads the AP coding, cash application, MRP triage and anomaly seed datasets, evaluates the baselines, applies the gate, sets a non-zero exit code on failure |
| `datasets/` | Seed datasets (`ap-coding-seed.jsonl`, `cash-app-seed.jsonl`, `mrp-triage-seed.jsonl` with 9 action messages, `anomaly-seed.jsonl` with 12 journals of which 4 are anomalies, including a normal night batch and a daytime anomaly, `adversarial-seed.jsonl` with 12 attacks and 4 benign prompts). These are small seeds for the harness, not the held-out sets in FR-47.9 (see NOT-IMPLEMENTED.md) |

## Run and test

```sh
cd evals
npm install
npm test          # vitest: harness and metric tests, including gate thresholds
npm run typecheck
npm run eval      # runs the CLI over the seed datasets
```

`npm run eval` exits 1 on the seed data, and that is expected: it measures the
deterministic baselines, not an LLM agent, and the vendor-history baseline
scores top-1 0.625 against the 0.92 AP coding gate. The output names each
dataset's version so a CI job can record what it measured.

The tests need no network or database. They check that the gate fails each
threshold, that a regression of more than one point fails, and that the metrics
match hand-computed values.

## Wave reference

- FR-47.9 eval harness and thresholds (`gate.ts`)
- AC-47.3 (AP coding top-1 >= 0.92) and AC-47.4 (cash application precision >= 0.90): the gate and harness are built; the full datasets are not
- BR-47.8 dataset versioning: each dataset's version is the hash of its content (`datasetVersion`), printed with every result; the re-baseline queue and promotion gate are in `rebaseline.ts`, reading persisted `eval_run` rows is not built (no store yet)
- FR-47.9 refusal correctness >= 0.99 on the adversarial set: metric, harness and seed set. The CLI does not run it yet, because the copilot that answers the prompts is not built

## Dependencies

Development only: `typescript`, `vitest`, `@types/node`. No runtime
dependencies.
