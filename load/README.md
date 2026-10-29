# Load campaign — PSG-50, wave 50

Performance campaign for FR-50.1: scenarios S1–S7 in k6, the SLO table and throughput targets as data, and
a checker that fails a campaign when any SLO is missed or unmeasured.

## Layout
- `k6/scenarios.js` — S1 journal posting (1M lines/h, 500-line entries), S2 order intake with pricing and ATP
  (100k lines/h), S3 interactive mix (10k users, 60/25/10/5, 10 s think time), S4 mobile scans (400/s, the
  rate of 2k scanners at one scan per 5 s), S5 MRP over the pool's items, S6 month-end jobs alongside S1,
  S7 lakehouse freshness.
- `k6/lib.js` — threshold generation and the journal entry builder; `k6/lib.test.mjs` tests them.
- `k6/data/pool.example.json` — shape of the data pool (ids of ledgers, accounts, customers, products, MRP
  items). The real pool comes from the `fx-load-10k` generator.
- `slo/slo.json` — the FR-50.1 table plus the 1M/100k lines-per-hour targets. Thresholds are generated from
  it; the checker reads it. `notChecked` names the targets a campaign cannot measure, with the reason.
- `tools/check_campaign.py` — evaluates a k6 `--summary-export` file. Stdlib only, Python 3.11+.
- `tools/test_check_campaign.py` — unit tests for the checker.
- `k6/soak.js` — 24 h soak running the campaign's S1 and S3 functions at the `soak` profile of `slo/slo.json`
  (50%, 24 h). `LOAD_SCALE` shrinks it further and `SOAK_DURATION` shortens it for smoke runs. Thresholds are the
  SLO rows those scenarios measure (`soak.slos`); `check_campaign.py --profile soak` checks the same rows.
- `tools/check_soak.py` — memory growth gate over per-service working-set CSVs (`timestamp,bytes`); fails when the
  median of the last 10% of samples exceeds the median of the first 10% by more than 10%, with fewer than 10
  samples, or when the series spans less than 24 h (`--min-span-hours 0` for smoke runs).
  `tools/test_check_soak.py` covers it.
- `fixtures/smoke-12.json` — fx-smoke-12: the twelve PRD 9.50.9 processes as ordered steps with a final assertion.
  `tools/test_smoke12.py` checks the PRD names and order and resolves every step against the services' OpenAPI
  files; steps with no route yet carry an `unverified` reason.
- `tools/verify_paths.py` — resolves every `http.<method>` call in `k6/scenarios.js` (method and path, through
  the gateway's `/api/v1` -> `/v1` rewrite) against `services/*/api/openapi.yaml`. Exit 1 when a `PATHS` call has
  no route, or when an `UNVERIFIED_PATHS` call now has one (move it to `PATHS`). `tools/test_verify_paths.py`
  runs it on this branch, so the campaign cannot drift from the services. `tools/openapi_routes.py` is the
  route reader it shares with `test_smoke12.py`.
- `tools/top_sql.py` — reads a `pg_stat_statements` CSV export, prints the top 50 by total time as a Markdown
  table, and exits 1 when any statement's mean time exceeds 1000 ms (AC-50.2). Maxima over the limit are
  printed as warnings: one maximum is kept per statement since the last reset, so a single outlier is reviewed,
  not failed. `tools/test_top_sql.py` covers it.

## Run
```
python3 load/tools/verify_paths.py   # before the campaign: routes still match the services
k6 run --env BASE_URL=https://api.perf.passage.dev --env TOKEN="$TOKEN" --env POOL_FILE=/data/pool.json \
  --env CAMPAIGN_ID=c-2026-10 --summary-export=results/summary.json load/k6/scenarios.js
python3 load/tools/check_campaign.py results/summary.json
python3 -m unittest discover -s load/tools
node --test load/k6

# soak, 24 h at 50% (memory series exported per service as timestamp,bytes CSV)
k6 run --env BASE_URL=https://api.perf.passage.dev --env TOKEN="$TOKEN" --env POOL_FILE=/data/pool.json \
  --env CAMPAIGN_ID=soak-2026-10 --summary-export=results/soak-summary.json load/k6/soak.js
python3 load/tools/check_campaign.py --profile soak results/soak-summary.json
python3 load/tools/check_soak.py results/soak-memory-*.csv

# top SQL after the campaign (FR-50.2, AC-50.2), on the ledger cluster's primary
psql "$LEDGER_DIRECT_URL" -c "\copy (select query, calls, total_exec_time, mean_exec_time, max_exec_time, rows from pg_stat_statements) to 'results/stats.csv' csv header"
python3 load/tools/top_sql.py results/stats.csv --top 50 > results/top-sql.md
```
Reset the statistics (`select pg_stat_statements_reset()`) right before the campaign so the export covers only
the campaign window.

`LOAD_SCALE` (default 1.0) scales rates and VU counts for smoke runs; pass the same value to the checker as
`--scale` so the throughput targets scale with it. The published campaign (AC1) uses 1.0 for 2 h.
Campaigns run against a masked copy of the first customer's data, never production (BR-50.5).

## Conventions
- Every request is tagged `op:<name>`; the tag must match an `op` in `slo.json`.
- Creating POSTs send a unique `Idempotency-Key`.
- Money and quantities are decimal strings, never floats.
- Auth is a bearer token from `TOKEN`; no credential is stored in this repo.
- Throughput is counted from 2xx responses only (`journal_lines_posted`, `order_lines_accepted`), so failed
  or dropped iterations lower it.
- The export carries `p(99)` because the script sets `summaryTrendStats`; without it k6 stops at `p(95)`.

## Routes
`PATHS` in `scenarios.js` lists the routes that exist in `services/<svc>/api/openapi.yaml`, with request
bodies that follow those schemas. `UNVERIFIED_PATHS` (dashboards, warehouse scans, inventory close, FX
revaluation, lakehouse freshness) have no route on main yet; they show as failed requests, so a campaign
cannot pass until the owning waves publish them (see `docs/ops/NOT-IMPLEMENTED.md`). `tools/verify_paths.py`
enforces both lists; run it before a campaign.
`soak.js` reuses the S1 and S3 functions of `scenarios.js`, so it hits the same routes; its dashboard
requests fail the same way until the analytics route exists.

## Not built yet
- k6 writes tagged sub-metrics (`http_req_duration{op:...}`) to the summary export. The checker also accepts the
  `values` layout used by `handleSummary`.
- Fixture `fx-smoke-12` is defined (`fixtures/smoke-12.json`) but its runner is not built. `fx-load-10k` is not
  generated (see `docs/ops/NOT-IMPLEMENTED.md`).
