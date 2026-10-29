# Runbook: SLO burn

Alerts: `PassageSLOFastBurn` (> 14.4x over 1 h and over 5 m, page) and `PassageSLOSlowBurn` (> 6x over
6 h and over 30 m, ticket). The short window clears the alert once the burn stops. Burn rate is the 5xx
ratio per `job` divided by the error budget (`1 - slo.availabilityTarget`), recorded as
`passage:slo_burn_rate:<window>` by `deploy/multi-region/chart`. Budgets: FR-50.1 table (`load/slo/slo.json`).

1. Identify the service from the `job` label; open the SLO dashboard (default window 30 days).
2. Check for a recent release. A fast burn in the 30-minute window after a release triggers automatic
   rollback (BR-50.6, OPS-1706). If it did not roll back, roll back by digest ([release rollback](release-rollback.md)).
3. If no release is involved, check dependencies: database (`pg_stat_statements` top SQL), Kafka consumer lag, caches.
4. Slow burn: create a ticket and review the error budget weekly (SLO report, Monday).
