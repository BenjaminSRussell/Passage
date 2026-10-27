# Not implemented in PSG-12 (after PSG-1202 slice 2a)

| Deliverable or AC | Status | Reason |
|---|---|---|
| Deliverable 1: exchange rate resolution, inverse, triangulation | built | `internal/domain/rates.go` |
| Deliverable 1: rate audit store (versioned rows, BR-12.10 precedence, outbox event) | built | `internal/store`, `migrations/0001_init.sql` |
| Deliverable 1: rate HTTP API, historical rate API, convert endpoint | built (#93) | `internal/api/rates.go`: `POST/GET /v1/fx/rates`, `GET /v1/fx/rates/history`, `POST /v1/fx/convert`; contract in `api/openapi.yaml`, route-vs-spec test in `internal/api/contract_test.go` |
| FR-12.11 deviation above 10 percent requires confirmation | not built | the store computes the deviation and puts it on `fx.rate.published`; `POST /v1/fx/rates` has no confirm flag or dry-run yet, so the UI cannot ask before publishing |
| `If-Match` on fx writes | not applicable | the only mutable resource is the rate type (`PUT`, naturally idempotent, no version column); rates are append-only and corrected by a new idempotent `POST` |
| Rate lookup cache per pod refreshed from `fx.rate.published` (FR-12.3) | not built | `GET /v1/fx/rates` and `/convert` load the current book per request via `Store.CurrentBook`; fine for dev volumes, replace before the 5 ms cold-lookup SLO is claimed |
| HTTP request metrics (rate, latency) | not built | `/metrics` exposes only the importer series; add request counters when the fx SLO alerts are defined |
| Deliverable 2: rounding, residual allocation, three-currency conversion | built | `internal/domain/convert.go` |
| Deliverable 2: single-line residual to `rounding_difference_account` (BR-12.4 last sentence) | not built | needs ledger currency policy, owned by `services/ledger` (PSG-11, absent from the worktree) |
| Deliverable 2: realized/unrealized gain-loss accounts | not built | account configuration lives in `services/ledger` (PSG-11 absent); the calculation returns gain and loss amounts |
| Deliverable 3: ledger groups and posting layers (AC2) | slice 3 of PSG-12 | line budget; persistence needs `services/ledger` (PSG-11 absent) |
| Deliverable 4: revaluation calculation, `closing_rate` method, reversal date | built | `internal/domain/reval.go` |
| Deliverable 4: `cumulative_no_reversal` method, run table, posting, reversal entries, batch schedule | not built | posting and run state are owned by `services/ledger` (PSG-11 absent) |
| Deliverable 5: currency translation (CTA) | slice 3 of PSG-12 | line budget; posting side needs `services/ledger` |
| Deliverable 6: market data series | PSG-1202 slice 2d | migration `0002`, domain, store and API |
| Temporal schedule for provider imports | replaced by an in-process ticker (#93) | `cmd/fx/schedule.go`. A CronJob would run in a pod Prometheus never scrapes for long enough to see `fx_provider_degraded` or the last-success gauge, so the importer's metrics and the alert rules would be unobservable; the ticker keeps them in the long-lived API pod. The ticker has no cross-replica lock, so two replicas would both fetch and race `StartRun`; the chart therefore refuses `replicaCount` above 1 while `import.tenantId` is set. Startup fails when the configured rate type does not exist for the tenant, or when the provider is `openexchangerates` without `FX_OXR_APP_ID`. One tenant and one provider per process (`FX_IMPORT_*`); multi-tenant fan-out waits for the tenant registry. Move to Temporal when wave 2 lands |
| ECB SDW market data series adapter | PSG-1202 slice 2d | deferred with the market data series |
| AC4: provider outage fallback and alert | built and wired (#93) | `internal/importer` (retry, last-known fallback, degraded run, outbox event, `fx_provider_degraded` metric), `charts/fx/templates/prometheusrule.yaml` |
| AC4: importer wiring | built (#93) | `cmd/fx` mounts `/metrics` on the admin port and seeds the last-success gauge from `LastSuccess` at startup (`Metrics.Seed`). A tenant with no successful import yet has no gauge series, so the stale rules stay silent for it until the first success; `fx_provider_degraded` covers failed first imports |
| Demo entity with EUR reporting currency | handoff | needs `services/ledger` and `services/org` waves |
| OpenAPI, route contract test, `cmd/fx`, Dockerfile | built (#93) | `api/openapi.yaml`, `internal/api/contract_test.go`, `cmd/fx`, `Dockerfile` |
| Helm chart `services/fx/charts/fx` (Deployment, Service, ServiceMonitor and PrometheusRule gated on the `monitoring.coreos.com/v1` capability, NetworkPolicy, `databaseSecret`, `migrateOnStart`) | built (#93) | the PrometheusRule from #191 moved into the chart with `$labels` escaped; rules filter on the release namespace |
| Argo CD app `deploy/envs/dev/apps/fx.yaml`, database `passage_fx` (`30-fx-db.sh`, `init-rev` 3) and Postgres admit for the fx pod | built (#93) | image digest is the same placeholder as receiving until the image pipeline publishes one |
| `fx-db` Secret, and `import.*` values (tenant, rate type) in the dev app | open | the Secret (`DATABASE_URL=postgres://passage_app:<APP_PASSWORD>@postgres:5432/passage_fx?sslmode=disable`) is created outside Git in namespace `hello-ledger`, as for `receiving-db`; the importer stays off until a dev tenant UUID and rate type are chosen |
| CI image build matrix for fx | not applicable | `ci.yml` builds only `hello-ledger` (one hard-coded image job); no service has a matrix entry, so fx gets none. `helm lint` in the `charts` job now includes the fx chart |
| AsyncAPI for `fx.rate.published` and `fx.provider.degraded` | not built | no event contract tooling in the repo yet |
| Web workspace `apps/web/src/modules/fx/` | rate history, lookup and convert built (#93) | rate-type maintenance, rate entry and provider schedule screens (FR-12.10) are tracked in #198 |
| Revaluation batch scheduled in dev | handoff | `deploy/` is outside the owned paths of this ticket |
| Duty check on routes, RLS and tenant scoping of `domain.Book` | built for routes (#93) | routes require `View exchange rates` / `Maintain exchange rates` (seeded in `policies/authz/data.json`); the book is built per request from the tenant's RLS-scoped rows. `Administer exchange rates` (`POST /providers/{p}:import`) and `Override exchange rates` (ledger `rate_override`) are not seeded: their routes belong to later work |
| ISO 4217 reference data | handoff | wave 6 owns it; `money.MinorUnits` is used meanwhile |
| Append-only grants for `passage_app` (`migrations/0004_grants.sql`) | holds only with a separate owner role | the grants and REVOKEs are enforced by Postgres only when migrations run as a role other than `passage_app`. Dev sets `migrateOnStart: true` and migrates as `passage_app`, which then owns the tables and can change them; split the migration role before relying on append-only in an environment |
| Idempotent publish under concurrency | built (#93) | `Store.LockKey` takes a session advisory lock per (tenant, key) around lookup, handler and record, so concurrent requests with one key publish once; it holds one pool connection per in-flight publish |
