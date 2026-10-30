# Administrator guide: operating a Passage deployment (PSG-50, FR-50.11)

Audience: platform administrators and SRE on-call who run a Passage environment: regions, tenants,
licenses, releases and incidents. Tenant business administration (users, roles, company setup) is
covered by the functional guides of the owning modules, not here.

Status: this guide describes the operator procedures and their targets. The control-plane API under
`/api/v1/ops/*` and the `passage-ops` command are a handoff (see [NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md)).
Until they ship, each step is done through the runbook it links to.

## 1. Duties

Operator actions are gated by duty (PRD 9.50.17). Grant the smallest set.

| Duty | Who | Covers |
|---|---|---|
| PlatformAdmin | platform operator (break-glass for failover) | tenant provisioning and decommission (second approver), failover, license install, game day |
| PlatformView | SRE, support | region status, SLO status, capacity estimate |
| ReleaseManager | release manager | promote a release by digest, pause a ring, trigger rollback |
| OnCallResponder | SRE on-call | restart, scale, replay, incident updates |
| TenantAdmin | customer administrator | tenant cost, user-type assignment within the license |
| LicenseIssuer | commercial operations (offline) | sign license files with the Ed25519 key (HSM or Vault transit) |
| StatusPagePublisher | support lead | publish incident notices |
| CutoverDecisionOwner | customer CFO or project sponsor | record the go/no-go decision at production cutover |

Separation of duties (PRD 9.50.17):

| Never together | Severity | Rule |
|---|---|---|
| PlatformAdmin and any tenant business duty | HIGH | OPS-001: operators do not post business documents |
| PlatformAdmin and LicenseIssuer | HIGH | whoever installs a license must not be able to sign one |
| PlatformAdmin and TenantAdmin | MEDIUM | the platform operator does not administer a customer tenant |
| ReleaseManager and code approval of the same change | MEDIUM | CHG-001: four-eyes on what is released |
| CutoverDecisionOwner and the owner of a checklist item | MEDIUM | the go/no-go is not decided by whoever delivered the item |

## 2. Regions

Each region is one release of the `passage-multiregion` chart with its region file; the reference pair is
`eu-west` (PRIMARY) and `eu-central` (PASSIVE), both in the EU because failover never crosses a residency
boundary. Lint and render before applying a change:

```
helm lint deploy/multi-region/chart -f deploy/multi-region/values/eu-central.yaml
helm template passage-mr deploy/multi-region/chart -f deploy/multi-region/values/eu-central.yaml
```

The chart, its prerequisites (CNPG operator, Strimzi, Prometheus Operator, peer secrets) and the role table
are in [deploy/multi-region/README.md](../../deploy/multi-region/README.md).

Exactly one region is PRIMARY. The other is PASSIVE (warm: CNPG replica cluster, MirrorMaker 2 target,
Temporal replica) or FENCED (writes revoked). State changes follow BR-50.1:

- PASSIVE to PRIMARY by promotion. The former PRIMARY becomes FENCED in the same operation.
- FENCED to PASSIVE only after re-sync is verified.
- Never two PRIMARY regions.

Check the region state before any change:

- Region status: `GET /api/v1/ops/regions` (role, CNPG lag, MirrorMaker lag, DNS weight). Target lag
  is under 30 s for Postgres and under 100k messages for MirrorMaker (alert `mm2.lag_alert_messages`).
- Replication lag above 60 s raises `PassageReplicationLagHigh` (warning, OPS-1708); runbook
  [postgres-replication-lag](runbooks/postgres-replication-lag.md).
- MirrorMaker lag runbook: [mirrormaker-lag](runbooks/mirrormaker-lag.md).

## 3. Failover and fail-back

- Failover: [runbooks/failover.md](runbooks/failover.md). Targets: RTO <= 1 h, RPO <= 5 min, zero
  committed-transaction loss (FR-50.5).
- The command is rejected when target lag exceeds `FAILOVER_MAX_LAG_S` (300 s), with OPS-1701. Overriding
  needs `--accept-data-loss` and a second approver.
- Confirm with a second, distinct PlatformAdmin within 5 min. Without it the command does not run (OPS-1702).
- During failover the fenced region answers 503 with `Retry-After: 30` (OPS-1703). Clients retry
  idempotent requests with the same `Idempotency-Key`, so nothing posts twice (BR-50.2).
- After promotion, verify the ledger hash chain for each tenant. A break (OPS-1704) is Sev-1: keep user
  access blocked and page the ledger owner.
- Fail-back is planned, not emergency work: [runbooks/failback.md](runbooks/failback.md).
- Game day (quarterly): [runbooks/gameday.md](runbooks/gameday.md). Before injecting the failure, seal the
  baseline with `deploy/multi-region/gameday/baseline.py capture` and record the printed digest; after the
  failover, `baseline.py verify --expect-digest <digest>` checks the chain heads, row counts, offsets and
  gap-free sequences against it. Each game day produces a report kept as SOC 2 CC7.5 evidence.

## 4. Tenants

Provisioning (`POST /api/v1/ops/tenants`, `Idempotency-Key` required) creates the Keycloak realm, the
tenant schemas with row-level security, Kafka ACLs, the lakehouse namespace, object-store prefixes and the
license, in 15 minutes or less (AC-50.6). Steps and retries: [tenant-provisioning.md](tenant-provisioning.md).

- A residency region that is not deployed is rejected (OPS-1705). EU tenants stay in EU regions.
- Decommission (`DELETE /api/v1/ops/tenants/{id}`, second approver) exports data encrypted first. The
  retention period comes from the tenant's country packs (BR-50.9). Schemas are dropped only after the export
  is verified.
- Sandboxes are provisioned from masked production through the same path with a lower tier (DEV, TEST, UAT).

## 5. Licenses

Each tenant holds one signed license file (Ed25519). It sets the user counts for the full, operational and
self-service user types and an expiry. Install it with `PUT /api/v1/ops/tenants/{id}/license` (ETag required).
Runbook: [license-expiry.md](runbooks/license-expiry.md).

| State | Meaning | Logins | Data export and admin access |
|---|---|---|---|
| VALID | within limits, not expired | allowed | allowed |
| GRACE | over the full-user count, or within `license.grace_days` (30) after expiry | allowed, banner shown (LIC-1802) | allowed |
| EXPIRED | past the grace period | blocked (LIC-1803) | still allowed (BR-50.8) |

A license that expires in 30, 7 and 1 days raises LIC-1801 warnings and an alert. A signature that does not
verify is LIC-1804: reinstall the issued file. Do not edit a license file by hand.

## 6. Releases, upgrades and rollback

- Releases are rolled out by digest and are zero-downtime: expand/contract migrations, rolling pods, and
  N/N-1 API compatibility (see [upgrade-strategy.md](upgrade-strategy.md)).
- A migration that drops a column ships only after the release that stops using it (BR-50.7, OPS-1707).
  `deploy/multi-region/tools/expand_contract_lint.py` checks each release's new migrations against this
  release's code and the previous release's.
- `deploy/multi-region/tools/openapi_compat.py --old n1.json --new n.json` lists every API change an N-1
  client would notice; a release with a breaking change does not ship (AC-50.5).
- A fast burn of an SLO in the 30 minutes after a release rolls it back automatically (BR-50.6, OPS-1706).
  Manual rollback: [runbooks/release-rollback.md](runbooks/release-rollback.md). Database contract steps are
  deferred to the next release, so a rollback never runs one.

## 7. Monitoring, alerts and incidents

- SLO status and error budgets: `GET /api/v1/ops/slo`, or the embedded Grafana SLO dashboard (default window
  30 days). Targets: [load/slo/slo.json](../../load/slo/slo.json) and PRD FR-50.1.
- Burn rates: fast burn (14.4x over 1 h) pages; slow burn (6x over 6 h) opens a ticket (BR-50.6).
- Every alert links to its runbook from the alert annotation. Runbook index: [README.md](README.md).
- Severity: Sev-1 needs a response within 15 minutes (24x5 on-call); Sev-2 within 1 hour (9.50.1 hypercare).
  Sev-1 fixes are the only changes during a change freeze.
- Public status: `status_page.components` (API, UI, Analytics, Integrations, Mobile). Notices are published by
  a StatusPagePublisher and kept one year.

## 8. Performance, cost and capacity

- Before each minor release and before cutover, run the load campaign and soak from [load/README.md](../../load/README.md):
  `load/tools/verify_paths.py --gateway deploy/gateway` first (the campaign's routes exist in the services and the
  gateway forwards them), then k6, then `check_campaign.py`
  and `check_soak.py`. Any SLO miss blocks the release.
- After the campaign, export `pg_stat_statements` from the ledger cluster and run `load/tools/top_sql.py`. A
  statement with a mean above 1 s fails AC-50.2; review the top 50 and add indexes in the owning service's
  migrations.

- Capacity model: [capacity-model.md](capacity-model.md), calculator `docs/ops/tools/capacity_estimate.py`.
  Re-run it before onboarding a tenant whose user or line volume is above the reference tenant.
- Cost meter: [cost-model.md](cost-model.md). Monthly statements in USD and EUR (ECB month-average rate).
  Drift from provider billing above 5% is COST-1901. Calculator `docs/ops/tools/cost_meter.py`; a failed
  backup verification or PITR drill follows [backup-drill-failure.md](runbooks/backup-drill-failure.md).

## 9. Error codes you will see

| Code | Meaning | Where to look |
|---|---|---|
| OPS-1701 | failover rejected, target lag too high | failover runbook |
| OPS-1702 | second confirmation missing or expired | failover runbook |
| OPS-1703 | region read-only during failover; retry | client retry, BR-50.2 |
| OPS-1704 | ledger hash chain broken; user access blocked | Sev-1, [ledger hash chain break](runbooks/ledger-hash-chain-break.md) runbook, ledger owner |
| OPS-1705 | residency region not deployed | tenant provisioning |
| OPS-1706 | release rolled back by fast burn | release rollback runbook |
| OPS-1707 | migration drops a column still in use | [expand/contract violation](runbooks/expand-contract-violation.md) runbook |
| OPS-1708 | replication lag above 60 s | postgres replication lag runbook |
| LIC-1801 / 1802 / 1803 / 1804 | license expiring / in grace / expired / signature invalid | license expiry runbook |
| COST-1901 | metered cost differs from billing by more than 5% | [cost meter drift](runbooks/cost-meter-drift.md) runbook |

## 10. Escalation

Name the rota, the escalation path and the rollback decision owner before any cutover or game day (go/no-go
item 12, [go-live-checklist.md](go-live-checklist.md)). The decision to roll back a production release or fail
over belongs to the named owner, and the decision is recorded.
