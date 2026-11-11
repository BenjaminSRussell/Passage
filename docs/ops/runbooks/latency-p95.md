# Runbook: p95 latency above SLO

Alert: `PassageLatencyP95` (wave 5 observability rules, `deploy/observability/charts/observability/templates/alerts.yaml`).
Fires when the p95 of `http_request_duration_seconds`, over every service and route together, stays above the chart
value `slo.latencyP95Ms` (default 50 ms, the FR-50.1 entity read budget) for 10 minutes. Severity is the chart value
`pagerDuty.severity` (default `error`).

The alert is one number for the whole platform, while the binding budgets are per operation (FR-50.1). A fleet p95
above 50 ms can come from routes whose own budget is looser (dashboard loads p95 < 2 s, journal post of a 500-line
batch p99 < 2 s), and a pricing regression (p95 < 20 ms) can hide under it. Always compare the slow route with its own
row before acting:

| Operation | Budget |
|---|---|
| Entity read | p95 < 50 ms, p99 < 150 ms |
| Form save | p99 < 400 ms |
| Order intake (line) | p99 < 500 ms |
| Pricing | p95 < 20 ms |
| Journal post (500-line batch) | p99 < 2 s |
| Mobile scan round trip | p99 < 300 ms |
| Dashboard load | p95 < 2 s |

Known gaps, both handoffs to wave 5 observability (rows in `docs/ops/NOT-IMPLEMENTED.md`):

- The rule has no `runbook_url` annotation, so the alert does not link here.
- The rule reads `http_request_duration_seconds_bucket`. `platform/go/httpx` records it per `route` for every service
  built on `httpx.NewRouter` (bounds 5 ms to 10 s), exposed by the services that mount `httpx.WithMetrics` on their
  admin `/metrics`. A service that does not (budgeting, billing, analytics) still shows only its prefixed histogram, for
  example `histogram_quantile(0.95, sum by (le, route) (rate(payables_http_request_duration_seconds_bucket[5m])))`.

## First five minutes

1. Find the slow route: p95 and p99 by `route` for each service's histogram. Compare each with its row above.
2. Check for a release in the last 30 minutes. A latency regression that started at a deploy is a rollback
   candidate; roll back by digest with [release rollback](release-rollback.md). Automatic rollback is not built yet.
3. Read a slow request's trace (Tempo). Services forward `traceparent` on HTTP calls and events (architecture,
   cross-service calls); the span with the largest share is usually a SQL statement.
4. Check whether load changed: compare the request rate with the sized load in the
   [capacity model](../capacity-model.md). If the rate is above it, scale the affected pods, then review the inputs.

## Likely causes and actions

| Symptom | Likely cause | Action |
|---|---|---|
| Latency up on every route of one service, database CPU high | Missing index or a plan change after a statistics update | Export `pg_stat_statements` and run `load/tools/top_sql.py` (top 50, fails on a mean above 1 s); add the index in a new migration. |
| Latency only on pricing or ATP | Cache miss storm on the pricing and policy caches | [Cache stampede](cache-stampede.md). |
| Latency on journal posting | Lock contention on the ledger tables or replica lag on a synchronous standby | Check [Postgres replication lag](postgres-replication-lag.md); a synchronous quorum waits for a standby. |
| Latency after a Kafka broker event | Producer retries under `acks=all` | [Kafka broker replacement](kafka-broker-replacement.md). |

## Close

Record the window, the routes affected, the root cause and which operation budget was consumed. Add the route to the
load campaign in `load/` so the next release gate covers it.
