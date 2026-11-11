# Runbook: availability burn (HTTP 5xx)

Alert: `PassageAvailabilityBurn` (wave 5 observability rules, `deploy/observability/charts/observability/templates/alerts.yaml`).
Fires when 5xx responses exceed 0.1% of all HTTP responses, summed over every service, for 5 minutes. 0.1% is the
error budget of the 99.9% availability SLO (FR-50.1), so the alert fires at a burn rate above 1x: at that rate the
whole monthly budget is used before the month ends. Severity is the chart value `pagerDuty.severity` (default
`error`). The multi-window alerts `PassageSLOFastBurn` and `PassageSLOSlowBurn` (BR-50.6) are in the multi-region
chart; their runbook is [SLO burn](slo-burn.md).

Known gaps, both handoffs to wave 5 observability (rows in `docs/ops/NOT-IMPLEMENTED.md`):

- The rule has no `runbook_url` annotation, so the alert does not link here. Responders find this page by alert name.
- The rule reads `http_requests_total{status=~"5.."}`. `platform/go/httpx` now records that series for every service
  built on `httpx.NewRouter` (labels `route` and `status`), and the services that mount `httpx.WithMetrics` on their
  admin `/metrics` expose it. A service that does not wrap its metrics handler (budgeting, billing, analytics and the
  consumers' own admin ports) still shows only its prefixed series, for example
  `sum by (__name__) (rate({__name__=~".+_http_requests_total", status=~"5.."}[5m]))`.

## First five minutes

1. Find the failing service. Group the 5xx rate by `job` (or by series name, above), then by `route`.
2. Read the errors in the service logs (Loki). A 5xx body is an RFC 9457 problem with a stable `code` (for example
   `LED-0001`); an unhandled error is logged as `unhandled error` and answered with `COM-0500`. The problem's
   `traceId` is the request id, which finds the log line.
3. Check for a release in the last 30 minutes. Automatic rollback on a fast burn (BR-50.6, OPS-1706) is not built
   yet (handoff to deployment-cicd), so if the 5xx started at a deploy, roll back by digest with
   [release rollback](release-rollback.md).
4. Check the dependencies of the failing service: Postgres (connection saturation through the PgBouncer pooler, top
   SQL with `load/tools/top_sql.py` over a `pg_stat_statements` export) and Kafka (an outbox backlog does not return
   5xx itself, but a lost primary or broker can stop the writes that do).
5. If one region is failing and the other is healthy, decide whether to fence and fail over with the
   [failover runbook](failover.md). Do not fail over on a 5xx spike alone without region evidence.

## Decide

| Finding | Action |
|---|---|
| 5xx started within 30 min of a release | Roll back ([release rollback](release-rollback.md)). Open an incident, severity from the [administrator guide](../administrator-guide.md#7-monitoring-alerts-and-incidents). |
| Database errors or connection exhaustion | [Postgres primary loss](postgres-primary-loss.md) if the primary is gone; otherwise raise the pooler limits and review top SQL. |
| 503 with `Retry-After: 30` from a fenced region | Expected during failover (BR-50.2, OPS-1703). Not a burn; confirm the failover is in progress. |
| 5xx limited to one tenant (from the logs; the metrics carry no tenant label) | Check the tenant's licence state ([licence expiry](license-expiry.md)) and recent data changes. |
| Burn continues but `PassageSLOFastBurn` has not fired | The budget is draining slowly. Follow the slow-burn step of [SLO burn](slo-burn.md) and open a ticket. |

## Close

Record the burn window, the cause, the SLO impact (remaining error budget) and the action in the incident. The
weekly SLO report raises a ticket when the remaining budget is below 25% (PRD 9.50.16); that job belongs to the ops
service and is a handoff, so until it runs, open the ticket by hand.
