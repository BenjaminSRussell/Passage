# NOT-IMPLEMENTED — PSG-50 (wave 50), slices 1 to 6

Wave 50 owns only `deploy/multi-region/`, `load/` and `docs/ops/`, and its ownership entry lists no web
module. It has no service directory, so there is no Go module, database table, OpenAPI/AsyncAPI file or
web screen in this slice. Items below are either later slices of PSG-50 or handoffs to the wave that owns
the code or the infrastructure.

Status key: **built** = in slices 1 to 6. **later slice of PSG-50** = same wave, next slice. **handoff** =
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
| Synthetic growth part of `fx-load-10k`: deterministic, balanced journal-line CSV (decimal-string amounts, one currency per journal, spread over 36 periods) with a sha256 manifest | built | `load/tools/gen_fixture.py`, tests in `load/tools/test_gen_fixture.py` |
| Rest of `fx-load-10k`: the masked copy of the first customer, the loader that brings the synthetic CSV into the ledger, the 10k users, and the k6 data pool written from it | later slice of PSG-50 | needs the first customer's masked extract (FR-48.26); `load/k6/data/pool.example.json` fixes the pool's shape |
| Fixture `fx-smoke-12` (12 core processes as ordered API steps with a final assertion) | built | `load/fixtures/smoke-12.json`; `load/tools/test_smoke12.py` checks every step against the owning service's OpenAPI file, so a renamed route fails the test |
| `fx-smoke-12` runner: runs the steps in order with `Idempotency-Key` on writes, fills `{id}` from the latest response and other placeholders from `--params`, never sends an `unverified` step, passes a process only when its steps pass and its assertion is verified | built | `load/tools/smoke12.py`, tests in `load/tools/test_smoke12_runner.py` |
| `fx-smoke-12` final-assertion verifiers (AP/AR subledger = journal, hash chain head, WIP zero, ...) and request bodies; the run in the new primary (FR-50.6 step 6) | later slice of PSG-50 | verifiers read the services' data in the campaign environment; bodies come from `fx-load-10k` ids. Until then a real run fails |
| Smoke steps with no route on main: payment run, bank statement matching, payroll run posting, copilot query | handoff (payables, treasury, payroll, agents wave 47 owners) | marked `unverified` with the reason in `smoke-12.json`; the runner fails the process without sending the step. The dashboard step now uses analytics `getDashboard` (wave 46) |
| Routes for warehouse scan, inventory close, FX revaluation and lakehouse freshness | handoff (analytics wave 46, warehouse, inventory, ledger owners) | no such route on main; `UNVERIFIED_PATHS` in `scenarios.js` keeps them so the campaign fails until they exist. The dashboard call moved to `PATHS` (`GET /api/v1/analytics/dashboards/{id}`, the pool's `dashboardId`) |
| Postgres journal partitioning (month range, hash 16) and inventory/audit partitioning | handoff (ledger, inventory, platform owners) | DDL lives in each service's migrations; out of path |
| PgBouncer transaction pooling and `pg_stat_statements` on the CNPG cluster (FR-50.2) | built | `Pooler` and parameters in `deploy/multi-region/chart/templates/cnpg-cluster.yaml` |
| Services connecting through the pooler | handoff (each service chart owner, secret management) | service charts read `DATABASE_URL` from a secret; it must name `passage-pg-pooler-rw` while `MIGRATE_DATABASE_URL` keeps the direct `-rw` service (DDL and advisory locks need a session) |
| Top-SQL review tool: top 50 by total time from a `pg_stat_statements` export, fails when a statement's mean exceeds 1 s (AC-50.2) | built | `load/tools/top_sql.py`, tests in `load/tools/test_top_sql.py` |
| `pg_stat_statements` top-50 review and index additions (FR-50.2) | later slice of PSG-50 | needs measured data from the campaign; the indexes go into the owning services' migrations (handoff when found) |
| Strimzi 6 brokers, partition sizing | handoff (wave 3 eventing / deploy/kafka owner) | `deploy/kafka` is wave 3's path; it already sets `min.insync.replicas=2` with 3 brokers |
| Pricing and policy-bundle caches, <= 5 s invalidation, TTL jitter and single-flight refill (the rules `runbooks/cache-stampede.md` assumes) | handoff (pricing, platform owners) | cache is service code; tier 2 is Valkey, proposed and not yet in ADR-0001 (architecture Q4), never Redis |
| Campaign route check: every `PATHS` call in `scenarios.js` (method and path) resolves to a service OpenAPI route and every `UNVERIFIED_PATHS` call still has none | built | `load/tools/verify_paths.py`; `load/tools/test_verify_paths.py` runs it on this branch's specs, so a renamed route or a newly published one fails the test |

## Deliverable 2: multi-region and DR
| Item | Status | Reason |
|---|---|---|
| CNPG cluster per role (PRIMARY quorum-sync, PASSIVE replica cluster, FENCED with all instances fenced) | built | `deploy/multi-region/chart`, render tests in `deploy/multi-region/test_chart.py` |
| MirrorMaker 2 (target region only), its KafkaUser, JMX lag metrics and PodMonitor | built | `templates/mirrormaker2.yaml` |
| Fence NetworkPolicy, region ConfigMap, values schema with the PRD 9.50.13 ranges | built | `templates/fence.yaml`, `region-config.yaml`, `values.schema.json` |
| Reference region pair inside one residency (eu-west, eu-central) | built | `deploy/multi-region/values/` |
| Region pair check across both files, which the per-file values schema cannot do: exactly two files, exactly one PRIMARY (BR-50.1), both files agree on the MirrorMaker 2 direction and it runs from the PRIMARY, Kafka aliases name the pair, a PASSIVE file names the primary endpoint; PRD 9.50.13 ranges on each region's values merged over the chart defaults | built | `deploy/multi-region/tools/check_region_pair.py`, tests in `test_check_region_pair.py`; failover runbook step 3 runs it |
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
| Facts collector: runs the operator's ledger, row-count, sequence and checkpoint queries through `psql` in read-only transactions, reads Kafka end offsets with the named command and cluster, and writes facts `baseline.py` accepts | built | `deploy/multi-region/gameday/collect.py`, tests in `test_collect.py` |
| Collector spec for the real schemas (ledger chain, row counts per domain, fiscal sequences) and the offsets command | later slice of PSG-50 | the tables belong to the ledger, inventory and numbering waves; the spec is written against the game-day environment |
| Cross-region Postgres failover drill with measured RTO/RPO (AC2) | later slice of PSG-50 | needs two live regions |
| Game-day scheduled Temporal workflow with human tasks | later slice of PSG-50 | needs `passage-ops` (handoff above) |
| Fail-back procedure dry run | later slice of PSG-50 | needs two live regions |
| Chaos experiments: Kafka broker loss and Postgres primary kill as Chaos Mesh `PodChaos` (one pod, 5 min budget, runbook link) | built | `deploy/multi-region/chaos/`; `test_chaos.py` checks the selectors against `deploy/kafka/kafka.yaml` and the chart values |
| Run the chaos experiments during a campaign and record "no failed postings, recovery < 5 min" (PRD 9.50.9) | later slice of PSG-50 | needs the campaign cluster |
| Chaos Mesh installation | handoff (deployment-cicd) | the operator chart is not in wave 50's paths |

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
| Capacity calculator: users, think time, journal and order lines/h, scanners, storage growth and Kafka peak to pods (x1.5 headroom, passive 50% warm), nodes, Postgres instances and GiB, Kafka brokers and partition floor; monthly cost for the parts whose provider rate is given (nodes, Postgres GiB-month, brokers, both regions, 12% overhead), listed in `costPriced` | built | `docs/ops/tools/capacity_estimate.py`; `test_capacity_estimate.py` reproduces the reference tenant of `capacity-model.md` (20 API, 14 ledger, 3 order pods; 7 + 4 nodes; 4,500 GiB; 6 brokers) |
| Measured per-pod budgets and node sizes for the capacity model (the `[ASSUMED]` constants) and a published `capacity.model_version` | later slice of PSG-50 | come from the load campaign (AC1) |
| Capacity estimate endpoint `GET /api/v1/ops/capacity/estimate` and the `capacity_model` table | handoff (ops service) | the calculator above is the rule; serving it and storing model versions needs a service |
| Cost calculator: one statement per tenant and month from the six metered dimensions, Decimal only (floats rejected), USD and EUR rounded once to 0.01 from the unrounded total, EUR at the ECB month average (BR-50.10); sum of tenant costs reconciled with the cluster invoice, COST-1901 above 5% with the catalog text (AC-50.7) | built | `docs/ops/tools/cost_meter.py`, tests in `test_cost_meter.py` |
| Monthly cost meter job: Prometheus queries with a stored query version, `tenant_cost` rows, statements as PDF/CSV, ECB rate fetch | handoff (ops service) | needs Prometheus data, provider billing and a service; the calculator above is what it runs |
| Runbooks for OPS-1704 (ledger hash chain break, Sev-1), backup verification or PITR drill failure, COST-1901 (cost meter drift) and OPS-1707 (expand/contract violation) | built | `runbooks/ledger-hash-chain-break.md`, `backup-drill-failure.md`, `cost-meter-drift.md`, `expand-contract-violation.md`; linked from the administrator guide's error-code table |
| Nightly capacity refresh, weekly SLO report, monthly backup drill, hourly license check | handoff (ops service, wave 48 backups) | scheduled jobs live in the ops service |
| Expand/contract linter (OPS-1707, BR-50.7) | built | `deploy/multi-region/tools/expand_contract_lint.py`: a release's new migrations (drops and renames of columns, tables, views) are checked against the code of this release (`--code`) and of the previous release (`--previous-code`, N-1), which still runs during the rolling deploy; `python3 -m unittest discover -s deploy/multi-region/tools` |
| Run the expand/contract linter on every release in CI | handoff (deployment-cicd CI owner) | CI pipeline is not in wave 50's paths; the job checks out the previous release tag and passes it as `--previous-code` |
| N / N-1 OpenAPI compatibility checker (FR-50.7, AC-50.5): removed operations, parameters, media types, 2xx responses and fields; new required inputs; narrowed request types and enums; widened response types; response fields made optional | built | `deploy/multi-region/tools/openapi_compat.py`, tests in `test_openapi_compat.py`. JSON input only (stdlib); `oneOf`/`anyOf` and external `$ref` are not compared |
| Release-level N/N-1 evidence: `openapi_compat` over every service's N-1 and N JSON, one evidence file per release with breaking changes and warnings | built | `deploy/multi-region/tools/compat_evidence.py`, tests in `test_compat_evidence.py`; `check_release_notes.py --compat-evidence` reads it |
| Run `compat_evidence.py` on every release between the previous release tag and the release commit | handoff (deployment-cicd CI owner) | needs the release pipeline (wave 1) to check out N-1 and convert the YAML specs to JSON; the runner's exit status is the gate |
| CI jobs for `helm lint deploy/multi-region/chart`, `test_chart.py`, the Python tools' tests (including `docs/ops/tools` and `deploy/multi-region/chaos`), `docs/ops/tools/check_docs.py` and `node --test load/k6` | handoff (deployment-cicd CI owner) | `.github/workflows` is not in wave 50's paths |
| Zero-downtime upgrade evidence: a rolling deploy of N+1 with an expand migration and zero failed requests from N clients (AC-50.5) | later slice of PSG-50 | needs the release pipeline (wave 1) and a running environment |
| Tenant provisioning API and automation (<= 15 min) | handoff (ops service) | steps documented in `tenant-provisioning.md` |
| Metrics `passage_ops_failover_in_progress`, `passage_ops_region_fenced`, `passage_license_days_to_expiry` | handoff (ops service) | alert rules reference them; the producer is the ops service |

## Deliverable 4: documentation
| Item | Status | Reason |
|---|---|---|
| Administrator guide for platform operators (regions, failover, tenants, licenses, releases, alerts, error codes): `administrator-guide.md` | built | describes operator procedures and targets; the `/api/v1/ops/*` steps route to runbooks until the ops service ships |
| Tenant administrator guide: company setup, users, roles and module admin screens | later slice of PSG-50 | needs the shipped admin surfaces of waves 1–5 and the module screens |
| Implementer guide (migration, localization, cutover) | later slice of PSG-50 | depends on waves 47–49 |
| API reference generator and undocumented-operation gate for OpenAPI: one Markdown page per service; an operation is documented with an operationId, a summary or description, `x-passage-duty`, a 2xx response and a description on every response (probe and `/internal/` routes are skipped) | built | `docs/ops/tools/api_reference.py`, tests in `test_api_reference.py` |
| 0 undocumented operations (AC-50.11). Run on main on 2026-10-29 over 106 specs: 328 of 1359 operations undocumented. 278 lack a summary or description (org 97, costacct 32, product 23, engineering 22, batch 20, eam 19, budgeting 15, ledger 14, and 36 in 11 more services); 50 purchasing operations lack `x-passage-duty`; 11 product and 1 migration operations declare only a `default` response | handoff (each service owner) | the specs are in the services' paths. `services/party/api/openapi.yaml` and `services/print/api/openapi.yaml` are not valid YAML (flow mapping at party line 260, unquoted colon at print line 124), so no reference can be generated for them |
| AsyncAPI event reference generator and undocumented-event gate: one Markdown page per service; an event is documented with a CloudEvents type `dev.passage.<context>.<entity>.<verb>.v<n>` (`x-passage-type`, else the message name), a title, summary or description, a payload schema and no duplicate type; an operation has action send or receive, a channel `$ref` and only that channel's messages, and is described by itself or by its messages | built | `docs/ops/tools/asyncapi_reference.py`, tests in `test_asyncapi_reference.py` |
| 0 undocumented events (AC-50.11). Run on main on 2026-10-29 over 51 specs (`services/*/events/asyncapi.yaml`): 144 of 258 events undocumented. 134 lack a title, summary or description (pricing 13, inventory 11, assets 9, eam 9, transport 9, sales 8, batch 8, and 67 in 21 more services); 10 purchasing messages carry their type only in the payload envelope (`type.const`), not in `x-passage-type` or the name; 50 operations in 12 services have no description and carry an undescribed message | handoff (each service owner) | the specs are in the services' paths. `services/workflow-ts/events/asyncapi.yaml` uses an external `$ref` (`../schemas/current/...`), which the gate treats as an input error |
| Rendering the reference pages in the docs site build (`passage-docs`) | handoff (deployment-cicd) | the site is not built; `--out` writes the pages the site picks up |
| Functional guides, one per module for phases 1–8 | later slice of PSG-50 | depends on module completion |
| Training curriculum, 6 role tracks with labs and assessments (FR-50.11) | built | `docs/ops/training-curriculum.md` |
| Lab verifiers and learner records against role grants | later slice of PSG-50 | the curriculum names them; they need the sandbox tier and the ops service |
| Link and runbook check for `docs/ops`: relative links and anchors resolve, every alert's `runbook_url` page exists, no stray files in `runbooks/` | built | `docs/ops/tools/check_docs.py`, tests in `test_check_docs.py` |
| Docs site link check over the whole site (0 broken links, AC-50.11) | handoff (deployment-cicd) | the docs site is not built; `check_docs.py --root` can run on any Markdown tree |
| Operations guide (this directory) | built | `docs/ops/` |

## Deliverable 5: go-live and v1.0
| Item | Status | Reason |
|---|---|---|
| Production go-live of the first customer (AC3) | later slice of PSG-50 | requires waves 47–49 done and the customer's parallel run |
| Go/no-go checklist (14 items) | built as a template | `go-live-checklist.md`; decision recording needs `cutover.decision.recorded` |
| Public v1.0 release: tag `v1.0.0`, cosign signature, SPDX SBOM, charts and images by digest (AC4) | later slice of PSG-50 | release pipeline is wave 1 / deployment-cicd |
| License state rules: VALID, GRACE (up to `license.grace_days` after expiry, or full users above the licensed count), EXPIRED; EXPIRED blocks new logins but never admin sign-in or export (BR-50.8, AC-50.8); LIC-1801 within 30 days of expiry, LIC-1802, LIC-1803 and their catalog texts; 30/7/1-day warning bands | built | `docs/ops/tools/license_state.py`, tests in `test_license_state.py` (the AC-50.8 case: expired 31 days ago is EXPIRED, login blocked, export allowed) |
| Signed license file verification (Ed25519, LIC-1804), `PUT /api/v1/ops/tenants/{id}/license` with ETag, `tenant_license` persistence, hourly check, `license.state_changed.v1`, login enforcement in the identity layer and the shell banner | handoff (ops service, wave 1 identity and shell owners) | needs a service, a crypto library and the shell; the state rules above are what they apply |
| How long over-use GRACE may last (PRD 9.50.4 makes over-use GRACE; BR-50.8's 30 days is stated for the grace period without saying whether it bounds over-use) | handoff (product decision, PRD owner) | `license_state.py` keeps over-use in GRACE until it is cleared and documents this; it does not invent a deadline |
| Release notes template and gate (PRD 9.50.15): version title, image and chart digests, features by module, migrations, breaking changes `None` (N-1), CVE fixes, rollback; no placeholder left; agrees with the compat evidence | built | `docs/ops/release-notes-template.md`, `docs/ops/tools/check_release_notes.py`, tests in `test_check_release_notes.py` |
| Release notes of v1.0.0 and their HTML and PDF renderings | later slice of PSG-50 | written at release time; rendering belongs to the docs site build (deployment-cicd) |
| Pricing publication | later slice of PSG-50 | licensing model and pricing unit are open decisions (PRD 9.50.12, questions 2 and 3) |
| Roadmap for v2 opened (AC4) | later slice of PSG-50 | after v1.0 tag |

## Acceptance criteria
| Criterion | Status | Reason |
|---|---|---|
| AC1 load results published, SLOs met | later slice of PSG-50 | see deliverable 1 |
| AC2 game day RTO <= 1 h, RPO <= 5 min | later slice of PSG-50 | see deliverable 2 |
| AC3 first customer month-end in production | later slice of PSG-50 | customer go-live |
| AC4 v1.0 tagged, signed, docs | later slice of PSG-50 | release |
| AC5 every deliverable has code or a row here | built | this file |
| AC6 go vet / go test in owned Go modules, web vitest/tsc, OpenAPI/AsyncAPI, Helm lint | partly built | no owned Go module, web module or API exists in this wave. `helm lint` passes with each region file; `deploy/multi-region/test_chart.py`, the Python tools' tests (`load/tools`, `deploy/multi-region/tools`, `deploy/multi-region/gameday`, `deploy/multi-region/chaos`, `docs/ops/tools`) and `load/k6/lib.test.mjs` pass but no platform check runs them (CI row above) |

## Coverage closure notes (PRD 9.50.21)
Inventory rows cited there (tenant provisioning, DR game days, masked refresh, license reports) map to
FR-50.8, FR-50.5/50.6, FR-48.26 and FR-3.14. Their code is in the owning waves; this wave consumes them.
