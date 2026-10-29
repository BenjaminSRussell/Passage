# NOT-IMPLEMENTED — PSG-50 (wave 50), slices 1 and 2

Wave 50 owns only `deploy/multi-region/`, `load/` and `docs/ops/`, and its ownership entry lists no web
module. It has no service directory, so there is no Go module, database table, OpenAPI/AsyncAPI file or
web screen in this slice. Items below are either later slices of PSG-50 or handoffs to the wave that owns
the code or the infrastructure.

Status key: **built** = in slice 1 or 2. **later slice of PSG-50** = same wave, next slice. **handoff** =
needs another owner or an out-of-path edit.

## Deliverable 1: performance campaign
| Item | Status | Reason |
|---|---|---|
| k6 scenarios S1–S7, thresholds from `slo.json`, p99 in the export, throughput counters | built | `load/k6/scenarios.js`, helpers tested by `node --test load/k6` |
| Campaign checker: latency, error budget, 1M journal lines/h, 100k order lines/h, MRP completion | built | `load/tools/check_campaign.py`, `python3 -m unittest discover -s load/tools` |
| Execute 2 h campaign S1–S7 at 10k VUs and publish the results (AC1) | later slice of PSG-50 | needs the production-sized cluster and the masked fixture; this slice ships the harness and checker only |
| Journal post "sustained 5k lines/s" (FR-50.1 SLO table) | later slice of PSG-50 | 18x the S1 rate; needs a separate peak-throughput run, so `slo.json` lists it under `notChecked` |
| Soak harness: `load/k6/soak.js` (S1 and S3 at 50%, 24 h), `soak` profile in `slo.json`, `check_campaign.py --profile soak` and the memory gate `load/tools/check_soak.py` (growth <= 10%, run spans 24 h) | built | `python3 -m unittest discover -s load/tools`, `node --test load/k6` |
| Execute the 24 h soak and publish the result | later slice of PSG-50 | needs the campaign environment and a per-service `container_memory_working_set_bytes` export |
| Fixture `fx-load-10k` (masked copy plus synthetic growth to 10k users, 40M lines) and the k6 data pool it writes | later slice of PSG-50 | needs the first customer's masked extract (FR-48.26); `load/k6/data/pool.example.json` fixes the pool's shape |
| Fixture `fx-smoke-12` (12 core processes as ordered API steps with a final assertion) | built | `load/fixtures/smoke-12.json`; `load/tools/test_smoke12.py` checks every step against the owning service's OpenAPI file, so a renamed route fails the test |
| `fx-smoke-12` runner (executes the steps, checks the final assertions) | later slice of PSG-50 | needs request bodies from `fx-load-10k` ids and a target environment |
| Smoke steps with no route on main: payment run, bank statement matching, payroll run posting, dashboard, copilot query | handoff (payables, treasury, payroll, analytics wave 46, agents wave 47 owners) | marked `unverified` with the reason in `smoke-12.json`; the runner must treat them as failed until the routes exist |
| Routes for dashboard load, warehouse scan, inventory close, FX revaluation and lakehouse freshness | handoff (analytics wave 46, warehouse, inventory, ledger owners) | no such route on main; `UNVERIFIED_PATHS` in `scenarios.js` keeps them so the campaign fails until they exist. The other scenario routes and bodies follow the services' OpenAPI files |
| Postgres journal partitioning (month range, hash 16) and inventory/audit partitioning | handoff (ledger, inventory, platform owners) | DDL lives in each service's migrations; out of path |
| PgBouncer transaction pooling and `pg_stat_statements` on the CNPG cluster (FR-50.2) | built | `Pooler` and parameters in `deploy/multi-region/chart/templates/cnpg-cluster.yaml` |
| Services connecting through the pooler | handoff (each service chart owner, secret management) | service charts read `DATABASE_URL` from a secret; it must name `passage-pg-pooler-rw` while `MIGRATE_DATABASE_URL` keeps the direct `-rw` service (DDL and advisory locks need a session) |
| `pg_stat_statements` top-50 review and index additions | later slice of PSG-50 | needs measured data from the campaign |
| Strimzi 6 brokers, partition sizing | handoff (wave 3 eventing / deploy/kafka owner) | `deploy/kafka` is wave 3's path; it already sets `min.insync.replicas=2` with 3 brokers |
| Pricing and policy-bundle caches, <= 5 s invalidation, TTL jitter and single-flight refill (the rules `runbooks/cache-stampede.md` assumes) | handoff (pricing, platform owners) | cache is service code; tier 2 is Valkey, proposed and not yet in ADR-0001 (architecture Q4), never Redis |

## Deliverable 2: multi-region and DR
| Item | Status | Reason |
|---|---|---|
| CNPG cluster per role (PRIMARY quorum-sync, PASSIVE replica cluster, FENCED with all instances fenced) | built | `deploy/multi-region/chart`, render tests in `deploy/multi-region/test_chart.py` |
| MirrorMaker 2 (target region only), its KafkaUser, JMX lag metrics and PodMonitor | built | `templates/mirrormaker2.yaml` |
| Fence NetworkPolicy, region ConfigMap, values schema with the PRD 9.50.13 ranges | built | `templates/fence.yaml`, `region-config.yaml`, `values.schema.json` |
| Reference region pair inside one residency (eu-west, eu-central) | built | `deploy/multi-region/values/` |
| Cross-region reachability: Kafka external TLS listener, Postgres replication endpoint, copied peer secrets | handoff (wave 3 deploy/kafka, deployment-cicd) | `deploy/kafka` has internal listeners only; the chart README lists the secrets to copy |
| Cross-region synchronous ledger replication (`cnpg.cross_region_sync.ledger`) | handoff (platform owner, decision) | CNPG replica clusters are asynchronous; synchronous cross-region needs one stretched cluster. The flag is published to `passage-ops` only |
| `passage-ops` Go control plane: `failover`, region state machine, `/api/v1/ops/*`, `/healthz/region` | handoff (platform owner, wave 1 or new ops service) | a Go service needs a `services/<name>` path; not in wave 50's owned paths |
| `ops` database migrations (`region`, `failover_event`, `tenant_license`, `tenant_cost`, `capacity_model`) and their grants | handoff (ops service owner) | same reason as above |
| Events `dev.passage.ops.failover.completed.v1`, `tenant.provisioned.v1`, `slo.breached.v1`, `license.state_changed.v1` and their AsyncAPI | handoff (ops service owner) | written through the ops service's outbox |
| Operations console, tenant management, cost and capacity screens (PRD 9.50.6) | handoff (ops service owner) | wave 50's ownership entry has no web module; the screens need the ops API |
| DNS failover: health-checked records, TTL 60 s, per-provider Terraform module | handoff (deployment-cicd owner) | DNS provider not chosen (cloud-agnostic ADR); `DNS_PROVIDER` is a config value only |
| Temporal multi-cluster replication | handoff (wave 4 workflow owner) | Temporal is wave 4's deploy |
| Object-store replication for lakehouse and documents | handoff (deployment-cicd, wave 46 analytics) | out of path |
| Game-day baseline manifest and post-failover verifier (`deploy/multi-region/gameday/baseline.py`, fx-gameday) | built | captures chain head, row counts, standby-cluster offsets, sequences; verifies extension, not replacement, against the digest recorded at capture (BR-50.3/50.4) |
| Facts collector that reads chain heads, row counts, offsets and sequences from a live region into the facts JSON | later slice of PSG-50 | needs two live regions and read access to each service's database; the shape is fixed by `validate_facts` in `baseline.py` |
| Cross-region Postgres failover drill with measured RTO/RPO (AC2) | later slice of PSG-50 | needs two live regions |
| Game-day scheduled Temporal workflow with human tasks | later slice of PSG-50 | needs `passage-ops` (handoff above) |
| Fail-back procedure dry run | later slice of PSG-50 | needs two live regions |
| Chaos tests: Kafka broker loss, Postgres primary kill | later slice of PSG-50 | needs a cluster |

## Deliverable 3: operations
| Item | Status | Reason |
|---|---|---|
| Runbooks for the alerts in this chart | built | 9 pages under `runbooks/`; `test_chart.py` checks every `runbook_url` resolves |
| Runbooks named in PRD 9.50.10 without an alert in this chart: Kafka broker replacement, Postgres primary loss, cache stampede, Temporal replication lag, certificate expiry, object-store replication lag | built | 6 pages under `runbooks/`. Their alerts belong to the owning waves (next row) |
| Alerts that link to those 6 pages: broker loss and under-replicated partitions, CNPG primary loss, cache hit ratio, Temporal replication lag, certificate expiry (daily scan, 9.50.16), object-store replication backlog | handoff (wave 3 eventing, wave 4 workflow, wave 5 observability, deployment-cicd) | the metrics come from charts outside wave 50's paths; each alert must carry `runbook_url` to its page (FR-50.10) |
| Runbooks for all 30 platform alert rules | later slice of PSG-50 | the other alert rules belong to the observability rule set (wave 5) |
| Availability burn-rate recording rules `passage:slo_burn_rate:{5m,30m,1h,6h}` | built | `prometheusrule.yaml`, from `http_requests_total` as the observability chart's alerts use |
| Per-operation latency SLO recording rules (FR-50.1 rows) and SLO dashboard | handoff (wave 5 observability) | needs per-route histograms from the service metrics |
| Automatic rollback on fast burn in the 30-minute release window (BR-50.6, OPS-1706) | handoff (deployment-cicd, wave 1 release pipeline) | Argo CD rollout analysis is not in wave 50's paths |
| Status page (`passage-status-page`) and public subscription | handoff (deployment-cicd, wave 5 observability) | separate chart and public domain |
| Capacity estimate endpoint `GET /api/v1/ops/capacity/estimate` | handoff (ops service) | model is documented; code needs a service |
| Monthly cost meter job and `tenant_cost` | handoff (ops service) | needs Prometheus data and provider billing |
| Nightly capacity refresh, weekly SLO report, monthly backup drill, hourly license check | handoff (ops service, wave 48 backups) | scheduled jobs live in the ops service |
| Expand/contract linter (OPS-1707, BR-50.7) | built | `deploy/multi-region/tools/expand_contract_lint.py`: a release's new migrations (drops and renames of columns, tables, views) are checked against the code of this release (`--code`) and of the previous release (`--previous-code`, N-1), which still runs during the rolling deploy; `python3 -m unittest discover -s deploy/multi-region/tools` |
| Run the expand/contract linter on every release in CI | handoff (deployment-cicd CI owner) | CI pipeline is not in wave 50's paths; the job checks out the previous release tag and passes it as `--previous-code` |
| CI jobs for `helm lint deploy/multi-region/chart`, `test_chart.py`, the Python tools' tests and `node --test load/k6` | handoff (deployment-cicd CI owner) | `.github/workflows` is not in wave 50's paths |
| Zero-downtime upgrade evidence and N/N-1 contract test suite | later slice of PSG-50 | depends on release pipeline (wave 1) |
| Tenant provisioning API and automation (<= 15 min) | handoff (ops service) | steps documented in `tenant-provisioning.md` |
| Metrics `passage_ops_failover_in_progress`, `passage_ops_region_fenced`, `passage_license_days_to_expiry` | handoff (ops service) | alert rules reference them; the producer is the ops service |

## Deliverable 4: documentation
| Item | Status | Reason |
|---|---|---|
| Administrator guide | later slice of PSG-50 | needs the shipped admin surface (waves 1–5) |
| Implementer guide (migration, localization, cutover) | later slice of PSG-50 | depends on waves 47–49 |
| API reference generated from OpenAPI/AsyncAPI, 0 undocumented operations | later slice of PSG-50 | generation runs in the docs site build (`passage-docs`), which is not built |
| Functional guides, one per module for phases 1–8 | later slice of PSG-50 | depends on module completion |
| Training curriculum, 6 role tracks with labs and assessments (FR-50.11) | built | `docs/ops/training-curriculum.md` |
| Lab verifiers and learner records against role grants | later slice of PSG-50 | the curriculum names them; they need the sandbox tier and the ops service |
| Docs site link check (0 broken links) | handoff (deployment-cicd) | the docs site is not built |
| Operations guide (this directory) | built | `docs/ops/` |

## Deliverable 5: go-live and v1.0
| Item | Status | Reason |
|---|---|---|
| Production go-live of the first customer (AC3) | later slice of PSG-50 | requires waves 47–49 done and the customer's parallel run |
| Go/no-go checklist (14 items) | built as a template | `go-live-checklist.md`; decision recording needs `cutover.decision.recorded` |
| Public v1.0 release: tag `v1.0.0`, cosign signature, SPDX SBOM, charts and images by digest (AC4) | later slice of PSG-50 | release pipeline is wave 1 / deployment-cicd |
| Licensing: signed license file verification (Ed25519), user types, GRACE/EXPIRED states | handoff (ops service) | verification code needs a service; the state rules are in the runbook |
| Release notes, pricing publication | later slice of PSG-50 | licensing model and pricing unit are open decisions (PRD 9.50.12, questions 2 and 3) |
| Roadmap for v2 opened (AC4) | later slice of PSG-50 | after v1.0 tag |

## Acceptance criteria
| Criterion | Status | Reason |
|---|---|---|
| AC1 load results published, SLOs met | later slice of PSG-50 | see deliverable 1 |
| AC2 game day RTO <= 1 h, RPO <= 5 min | later slice of PSG-50 | see deliverable 2 |
| AC3 first customer month-end in production | later slice of PSG-50 | customer go-live |
| AC4 v1.0 tagged, signed, docs | later slice of PSG-50 | release |
| AC5 every deliverable has code or a row here | built | this file |
| AC6 go vet / go test in owned Go modules, web vitest/tsc, OpenAPI/AsyncAPI, Helm lint | partly built | no owned Go module, web module or API exists in this wave. `helm lint` passes with each region file; `deploy/multi-region/test_chart.py`, the Python tools' tests (`load/tools`, `deploy/multi-region/tools`, `deploy/multi-region/gameday`) and `load/k6/lib.test.mjs` pass but no platform check runs them (CI row above) |

## Coverage closure notes (PRD 9.50.21)
Inventory rows cited there (tenant provisioning, DR game days, masked refresh, license reports) map to
FR-50.8, FR-50.5/50.6, FR-48.26 and FR-3.14. Their code is in the owning waves; this wave consumes them.
