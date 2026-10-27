# Part 0 — Phase 0: Foundation & Platform Kernel (waves 1–5)

## 0.0 Phase overview

**Business outcome at phase exit.** At the end of wave 5 nothing business-facing exists, but everything a business module needs does: a change merged to `main` reaches a Kubernetes environment with digest-pinned rollback in under 15 minutes; a user signs in once through a federated IdP and lands in a legal entity; every API call is authorized by OPA against AX-style roles/duties/privileges and fenced by Postgres row-level security; an entity declared in YAML becomes a table, an API, a client and a form without hand-written code; every state change is a CloudEvent on Kafka via a transactional outbox; approvals and batch jobs run on Temporal; number sequences support gap-free mode; every field change is in an immutable, hash-chained audit log; and a click in the shell can be followed through gateway, service, database and Kafka consumer in one trace. This layer is what AX calls the AOT, SysSecurity, batch and workflow frameworks, number sequences and SysDatabaseLog, and what SAP calls DDIC, PFCG, SM36/SM37, Business Workflow, SNRO and CDHDR/CDPOS — rebuilt as separately deployable services on the ADR-0001 stack.

**Phase-exit sign-off criteria**
- `make deploy-local` on a clean laptop produces a working k3d environment with all Phase 0 services in under 10 minutes; the same Helm/Argo manifests are healthy in the cloud `stage` cluster.
- Stakeholder demo executes, live, the wave-5 success metric from PRD §10: deploy a change → log in via a federated IdP → extend the Vendor-placeholder entity with a field and an enum value → run a 3-level approval → open the audit entry → open the trace in Tempo.
- Table-driven authorization suite (≥ 20 duties × allow/deny) and RLS cross-tenant suite are green; effective-permission report equals OPA decision for 100 sampled resources.
- Golden-generation suite for 10 reference entities is deterministic across 3 consecutive CI runs; additive v2 change passes, breaking change fails.
- Outbox chaos test shows zero lost and zero duplicated events over 10 kill cycles; 1M-row batch resumes after pod kill with 0 duplicates.
- Gap-free number sequence Jepsen-style test (10k concurrent requests) shows 0 gaps, 0 duplicates.
- 100% of generated forms pass axe WCAG 2.1 AA; audit entry visible within 2 s (p95); every service exports OTel traces, metrics and logs.
- SLO dashboards exist for every Phase 0 service with alert routes to the PagerDuty-compatible webhook.

**Cross-wave data model summary**

| Entity | Owning service | Key relationships |
|---|---|---|
| Release, Environment | platform (wave 1, GitOps repo + registry DB) | Release → Environment (promotion history) |
| Tenant, LegalEntity | identity (wave 2; regenerated from metadata in wave 3) | Tenant 1→N LegalEntity; every row in every domain carries tenant_id |
| User, Role, Duty, Privilege, RoleAssignment | identity / authz (wave 2) | User N↔N Role via RoleAssignment scoped to LegalEntity; Role 1→N Duty 1→N Privilege |
| EntityDefinition, FieldDefinition, EnumDefinition, TypeDefinition, ExtensionPackage | metadata-registry (wave 3) | Entity 1→N Field; Field → Type or Enum; ExtensionPackage → Entity (additive) |
| OutboxEvent, TopicDefinition, SchemaVersion | every service / events (wave 4) | Outbox row → CloudEvent; TopicDefinition → SchemaVersion (from EntityDefinition) |
| WorkflowDefinition, WorkflowInstance, WorkItem, BatchJob, BatchRun, BatchTask | workflow (wave 4) | Instance → Definition version; WorkItem → Instance + assignee User; BatchRun 1→N BatchTask |
| NumberSequence, NumberSequenceAllocation | numseq (wave 5) | Sequence scoped to Tenant/LegalEntity; Allocation → Sequence (gap-free audit) |
| AuditEntry, AuditField | audit (wave 5) | Entry → any entity/record; hash chain per tenant |
| SavedView, Personalization, ImportJob | shell-bff / importexport (wave 5) | SavedView → EntityDefinition + User; ImportJob → EntityDefinition |
| RuleSet, DecisionTable, DecisionRow, UsageSnapshot | metadata-registry (wave 3) | RuleSet 1→N DecisionTable 1→N DecisionRow; rows may come from an ExtensionPackage |
| OutboundMessage, MessageTemplate, NotificationPreference | notify (wave 4) | Message → Template version + recipient User; Preference per User × event class |
| Attachment, AttachmentVersion, AttachmentLink, DocumentType, FileType | docs (wave 5) | Attachment 1→N Version; Attachment N↔N any record via Link; Attachment → DocumentType |
| AlertRule, AlertEvent, StandardText | shell-bff (wave 5) | AlertRule → EntityDefinition; AlertEvent → Rule + record + recipient; StandardText referenced by MessageTemplate |

**Cross-wave event catalog**

| Event (CloudEvents type) | Producer | Consumers | Payload summary |
|---|---|---|---|
| dev.passage.identity.user.created.v1 / .deactivated.v1 | identity | audit, workflow (assignee cache), shell | user_id, tenant_id, email, status |
| dev.passage.authz.role-assignment.granted.v1 / .revoked.v1 | authz | audit, OPA bundle builder | user_id, role_id, legal_entity_ids, effective_from/to |
| dev.passage.authz.policy.compiled.v1 | authz | OPA sidecars (bundle pull), audit | bundle_version, sha256, role_count |
| dev.passage.metadata.entity.published.v1 | metadata-registry | generator, events (topic/schema), shell (form cache) | entity_name, version, diff summary |
| dev.passage.metadata.extension.applied.v1 | metadata-registry | generator, audit, shell | package_id, entity_name, fields_added, enum_values_added |
| dev.passage.workflow.instance.submitted.v1 / .completed.v1 | workflow | owning entity service, audit, shell inbox | instance_id, entity, record_id, outcome |
| dev.passage.workflow.workitem.assigned.v1 / .escalated.v1 | workflow | shell inbox, notifications | workitem_id, assignee_id, due_at |
| dev.passage.batch.run.completed.v1 / .failed.v1 | workflow (batch) | shell job monitor, alerting | job_id, run_id, rows_processed, error |
| dev.passage.audit.entry.recorded.v1 | audit | shell audit viewer (live), analytics (wave 46) | entry_id, entity, record_id, operation, hash |
| dev.passage.rules.ruleset.published.v1 | metadata-registry | every service holding compiled rule trees, audit | code, version, tables, row_count |
| dev.passage.notify.message.sent.v1 / .failed.v1 / .bounced.v1 | notify | audit, alerting, identity | message_id, channel, template_code, recipient_user_id |
| dev.passage.docs.attachment.available.v1 / .purged.v1 | docs | shell, audit, OCR capture (wave 18) | attachment_id, version_no, document_type, links |
| dev.passage.alerts.alert.fired.v1 | shell-bff (alert evaluator) | notify, shell | rule_id, entity, record_id, recipient_id |

**Phase-level risks**

| Risk | Likelihood | Impact | Mitigation | Owner |
|---|---|---|---|---|
| Metadata kernel under-specified before business waves need it | Medium | High | 10 reference entities in wave 3 include the shapes of Party, Product, JournalLine; golden tests frozen at phase exit | Product |
| Two-person team cannot operate Kafka + Temporal + Keycloak + CNPG | Medium | High | All operators installed via Helm umbrella; runbooks per component; k3d parity so every incident reproduces locally | Engineering |
| OPA policy compile from roles produces unexpected denies | Medium | Medium | Effective-permission viewer and `decision` debug endpoint; deny-reason strings in every 403 | Engineering |
| RLS misconfiguration leaks tenant data | Low | Critical | `FORCE ROW LEVEL SECURITY` on every table; app role NOBYPASSRLS; CI test queries every table cross-tenant | Security |
| Gap-free sequence becomes a throughput bottleneck | Medium | Medium | Continuous mode only where fiscal law requires it; non-continuous is the default with block pre-allocation | Engineering |
| Audit volume from CDC overwhelms Postgres | Medium | Medium | Monthly partitions; audit flag per field; PII masking at capture; 10-year retention tiered to object storage from wave 46 | Engineering |

---

## 0.1 Wave 1: Delivery Pipeline & Cluster Baseline

### 0.1.1 Scope and business context

AX ships code as LCS deployable packages built on a build VM and applied with AxUpdateInstaller; model store exports (`AxUtil export`) and database restores are the rollback. SAP moves ABAP through STMS transport requests (SE09/SE10 workbench requests, DEV → QAS → PRD routes, `tp import`), with back-out by transport of copies. Both couple release to a vendor tool and give no software bill of materials, no cryptographic signature and no sub-hour rollback. Wave 1 replaces this with a GitOps pipeline on any conformant Kubernetes. At the end of the wave an engineer can merge a pull request and watch the change reach `dev`, promote the same image digest to `stage`, and roll back to the previous digest, each in under 15 minutes. Out of scope: multi-region, production-grade HA Postgres, secrets rotation policy, cost reporting, and any business service other than the `hello-ledger` placeholder.

#### Pain points solved (10x lens)

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| Applying an LCS deployable package to a D365 sandbox takes 2–4 h of downtime and a service request for production; a failed apply means a database restore | Package = whole-system binary; environment managed by vendor tooling | Per-service OCI image + Helm chart, promoted by digest through Argo CD; rolling update with no downtime | Merge → running in env: 4 h → < 15 min; downtime per release: hours → 0 |
| STMS transport import to PRD (STMS_IMPORT) is a manual queue with no automatic back-out; a bad transport is reversed by hand-building a transport of copies | Transports carry object deltas, not immutable artifacts | Immutable digests; rollback is a one-line overlay revert | Rollback: 1–3 h and a Basis ticket → < 5 min, self-service |
| No SBOM or signature for an AX model or SAP transport; security teams cannot answer "which CVE is in production" | Build tooling predates supply-chain attestation | Syft SBOM, Grype/Trivy gate, cosign signature verified at admission | CVE-to-affected-environment lookup: days of manual inventory → one query; unsigned artifacts in prod: 0 |
| OneBox/VHD developer environments (AX) or NetWeaver dev editions (SAP) take days to provision and drift from production | Monolithic app server image per developer | `make deploy-local` on k3d with the same charts as cloud | New engineer to a running environment: 2–5 days → < 10 min |
| Customers cannot host on their own infrastructure without LCS (D365) or RISE (S/4) | Vendor-managed cloud is the only supported topology | One OpenTofu interface across k3d/EKS/GKE/AKS/bare-metal; nothing provider-specific in the critical path | Supported deployment targets: 1 → 5 with identical manifests |

### 0.1.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Package a release | LCS deployable package, `AxUtil export` model store, AXModel files | STMS transport request (E070/E071), `tp export` | OCI image (digest) + Helm chart version in `deploy/` | improved |
| Build and validate | Build VM, `AxBuild.exe`, Best Practice checks | SE80 activation, ATC (code inspector SCI), ABAP unit | GitHub Actions: lint, unit, container build, SBOM, scan | improved |
| Environment tiers | LCS dev/test/UAT/prod topology | STMS transport route DEV→QAS→PRD, STMS_QA | Argo CD app-of-apps per environment: dev, stage, prod | full |
| Promote between tiers | LCS "apply package" to the next environment | STMS import queue, `tp import`, STMS_IMPORT | `passage promote --to stage --digest sha256:…` opens a PR against the env overlay | full |
| Rollback | Restore DB + model store, "AOT revert" | Transport of copies back-out, SNOTE de-implement | Argo CD rollback to prior digest, `passage rollback --env stage` | improved |
| Code signing / trust | Strong-name model signing (`AxUtil … /key`) | Digitally signed SAP Notes, TMS trusted systems | cosign keyless signatures, policy-controller admission denies unsigned images | improved |
| Hotfix path | KB hotfix via LCS, binary update | SNOTE note implementation, SPAU | Hotfix branch → cherry-pick → same pipeline, same gates | full |
| Deployment log / history | LCS environment history, AxUpdateInstaller logs | STMS import log, SLOG/ALOG | Argo CD history + GitHub Deployments API + `platform.release` table | improved |
| Local dev environment | OneBox VM / VHD from LCS | SAP NetWeaver developer edition, mini-basis | `make deploy-local` on k3d, same charts as cloud | improved |

### 0.1.3 Functional requirements

- **FR-1.1 Monorepo layout** — Must. The system shall keep all Passage source in one repository with the layout `services/go/<svc>`, `services/ts/<svc>`, `services/py/<svc>`, `web/shell`, `web/remotes/<name>`, `deploy/charts/<svc>`, `deploy/envs/{dev,stage,prod}`, `infra/modules/<provider>`, `metadata/entities`, `docs/`.
  - Notes: each service owns a `Dockerfile`, `Makefile`, `CODEOWNERS` entry and a Helm chart; CI path filters build only changed services; a `go.work` and `pnpm` workspace root pin toolchain versions (Go 1.25, Node 24, Python 3.12).
- **FR-1.2 Cluster bootstrap module** — Must. The system shall provide an OpenTofu module `infra/modules/cluster` with one variable interface (`provider ∈ {k3d, eks, gke, aks, baremetal}`, `name`, `node_pools[]`, `kubernetes_version`) that outputs a kubeconfig and installs the base add-ons.
  - Notes: base add-ons are Argo CD, Envoy Gateway, cert-manager, External Secrets Operator, CloudNativePG operator, sigstore policy-controller, metrics-server; provider modules contain only networking, node pools and storage classes; k3d module runs with 3 agent nodes and 8 GB RAM.
- **FR-1.3 Argo CD app-of-apps** — Must. The system shall define a root Application per environment that reconciles child Applications from `deploy/envs/<env>/apps/*.yaml`, each pinning a chart version and an image digest.
  - Notes: sync policy automated for dev, manual-approve PR for stage and prod; prune and self-heal enabled; sync waves order operators (−2), databases (−1), services (0), gateway routes (1).
- **FR-1.4 Digest-only promotion** — Must. The system shall promote images by immutable digest; tags are informational only.
  - Notes: `passage promote` rewrites `image.digest` in the target overlay and opens a PR with the SBOM diff and scan summary; promotion to prod requires the digest to have run in stage for ≥ 60 minutes with no SLO burn alert.
- **FR-1.5 CI pipeline** — Must. The system shall run on every PR and on `main`: lint (golangci-lint, eslint, ruff), unit tests with coverage ≥ 70% per service, container build (BuildKit, reproducible), Syft SBOM (SPDX 2.3), Grype and Trivy scan, cosign sign and attest.
  - Notes: required checks block merge; `main` builds push to the registry under `ghcr.io/passage/<svc>@sha256:…`; median PR pipeline ≤ 8 minutes; cache keyed by lockfile hash.
- **FR-1.6 Vulnerability gate** — Must. The system shall fail the build on any CRITICAL finding, or any HIGH finding older than 14 days with a fix available, unless an exception file `.vuln-exceptions.yaml` lists the CVE with an expiry ≤ 30 days.
- **FR-1.7 Signature verification admission** — Must. The system shall reject any pod whose image lacks a valid cosign signature from the repository identity (`https://github.com/passage/*` OIDC issuer) in namespaces labelled `passage.dev/verified=true`.
  - Notes: SLSA provenance attestation level 2 at wave 1, level 3 target by wave 48; a negative test deploys an unsigned image and asserts the admission denial message.
- **FR-1.8 `hello-ledger` placeholder** — Must. The system shall deploy a Go service with one Postgres table that proves the path: migration on start, REST endpoints, health probes, TLS ingress, metrics endpoint.
- **FR-1.9 Local parity** — Must. `make deploy-local` shall stand up k3d, Argo CD and `hello-ledger` behind `https://app.local.passage.dev` with a locally trusted cert-manager CA in under 10 minutes on a 16 GB laptop.
- **FR-1.10 Rollback** — Must. The system shall roll back any environment to the previous release by digest in ≤ 5 minutes via `passage rollback --env <env>` (which reverts the overlay commit) or the Argo CD history UI.
  - Notes: database migrations in Phase 0 are additive-only so rollback never requires a down migration; a rollback records a `Release` row with `kind=rollback`.
- **FR-1.11 Secrets and config** — Must. The system shall source all secrets from Vault through External Secrets Operator; no secret value appears in Git, Helm values or CI logs.
  - Notes: config via ConfigMap generated from `deploy/envs/<env>/values.yaml`; secret names follow `passage/<env>/<svc>/<key>`.

### 0.1.4 Data model

**Release** (`platform.release`, registry DB, also materialized in Git history)

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| service | text | yes | matches chart name |
| image_digest | text | yes | `sha256:` + 64 hex; unique with service |
| git_sha | char(40) | yes | commit that produced the build |
| chart_version | text | yes | semver |
| sbom_url | text | yes | SPDX artifact in registry |
| signed | bool | yes | cosign verification result |
| status | enum | yes | built, scanned, signed, deployed_dev, deployed_stage, deployed_prod, superseded, rolled_back |
| created_at | timestamptz | yes | |

**Environment** (`platform.environment`)

| Field | Type | Required | Notes |
|---|---|---|---|
| name | text | yes | PK; dev, stage, prod, local-<user> |
| cluster_provider | enum | yes | k3d, eks, gke, aks, baremetal |
| argo_root_app | text | yes | app-of-apps name |
| promotion_policy | enum | yes | auto, pr_approval |
| soak_minutes | int | yes | default 0 dev, 60 stage→prod |
| base_url | text | yes | `https://app.<env>.passage.dev` |

**HelloLedgerEntry** (`hello_ledger.entry`)

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| memo | varchar(200) | yes | |
| amount_minor | bigint | yes | integer minor units, USD/EUR examples: 12345 = $123.45 |
| currency | char(3) | yes | ISO 4217 |
| created_at | timestamptz | yes | default now() |
| created_by | text | yes | `anonymous` until wave 2 |

Invariants: one `Release` row per (service, image_digest); at most one `deployed_<env>` release per service per environment; `Environment.name` is immutable. Indexing: `release(service, created_at desc)`; `entry(created_at)`.

### 0.1.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| GET | /healthz, /readyz | Liveness / readiness | n/a | none |
| GET | /v1/version | git sha, digest, chart version | n/a | none |
| GET | /v1/hello-ledger/entries?limit=&cursor= | List entries | n/a | none (token required from wave 2) |
| POST | /v1/hello-ledger/entries | Create entry | `Idempotency-Key` header, 24 h window | Maintain hello ledger |
| GET | /v1/hello-ledger/entries/{id} | Fetch one | n/a | none |
| GET | /v1/platform/releases?service=&env= | Release history | n/a | View platform releases |
| POST | /v1/platform/releases/{id}/promote | Open promotion PR | release id + env | Maintain platform releases |

CLI commands `passage promote`, `passage rollback`, `passage status` call these endpoints or edit the overlay directly.

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| dev.passage.platform.release.built.v1 | CI publishes signed image | release_id, service, image_digest, git_sha, sbom_url | registry DB writer |
| dev.passage.platform.release.promoted.v1 | Argo sync Healthy in target env | release_id, environment, actor, previous_digest | audit (wave 5), shell notifications |
| dev.passage.platform.release.rolledback.v1 | Rollback sync Healthy | release_id, environment, actor, reason | audit, alerting |
| dev.passage.hello.entry.created.v1 | Entry insert | entry_id, amount_minor, currency | none (proves outbox path in wave 4) |

In wave 1 events are written as CloudEvents JSON to stdout and to the registry DB; Kafka transport replaces the sink in wave 4 without changing types.

### 0.1.6 User experience

| Screen | Primary actions | Key fields/columns | Duty required |
|---|---|---|---|
| Argo CD UI (per env) | Sync, view history, rollback | app, health, sync status, revision, digest | Platform engineer (Argo RBAC via GitHub team) |
| GitHub PR checks | View gate results, SBOM diff | lint, unit, build, scan, sign | repository write |
| hello-ledger page `/` | Create entry, list entries | memo, amount, currency, created_at | none |

Interaction rules: promotion PRs are templated with a checklist (soak time met, scan clean, rollback target named); CI annotates failing lines inline; `make deploy-local` prints a single URL table at the end.

### 0.1.7 Business rules and edge cases

- **BR-1.1** Release status machine: built → scanned → signed → deployed_dev → deployed_stage → deployed_prod → superseded; any deployed_* may go to rolled_back; no transition skips `signed`.
- **BR-1.2** A tag may be rewritten; a digest may not. Any overlay referencing a tag fails the `deploy-lint` CI check.
- **BR-1.3** Promotion to prod requires soak ≥ `Environment.soak_minutes` in stage with zero `SLOBurnRateHigh` alerts; the check is computed in UTC with ISO 8601 timestamps.
- **BR-1.4** Two concurrent promotions of the same service to the same environment: the second PR is auto-closed with a comment pointing at the first.
- **BR-1.5** A vulnerability exception past its expiry date fails the build as if no exception existed.
- **BR-1.6** Rollback target is always the last `deployed_<env>` release with a different digest; if none exists the command refuses.
- **BR-1.7** Images are retained for 180 days or while referenced by any environment, whichever is longer; SBOMs retained 7 years.
- **BR-1.8** A hotfix branch must be cut from the prod digest's `git_sha`; CI refuses `hotfix/*` branches whose merge base is not a deployed_prod release.

### 0.1.8 Acceptance criteria

- **AC-1.1** Given a PR with an injected lint error, When CI runs, Then the `lint` check fails and merge is blocked. (FR-1.5)
- **AC-1.2** Given a PR with a failing unit test, When CI runs, Then `unit` fails and the failing assertion is annotated inline. (FR-1.5)
- **AC-1.3** Given a clean macOS or Linux laptop with Docker, When `make deploy-local` runs, Then Argo CD and hello-ledger are reachable over TLS within 10 minutes. (FR-1.9)
- **AC-1.4** Given a signed image in dev, When `passage promote --to stage` is merged, Then Argo shows Healthy with that digest within 15 minutes of merge. (FR-1.3, FR-1.4)
- **AC-1.5** Given stage running digest B after A, When `passage rollback --env stage` runs, Then Argo history shows A active within 5 minutes and a `rolled_back` Release row exists. (FR-1.10)
- **AC-1.6** Given an unsigned image applied to a verified namespace, When the pod is created, Then admission denies it with a message naming the missing signature. (FR-1.7)
- **AC-1.7** Given an image with one CRITICAL CVE, When CI runs, Then the scan gate fails; adding a valid exception with expiry in 10 days passes it. (FR-1.6)
- **AC-1.8** Given a secret in Vault, When hello-ledger starts, Then it reads `DATABASE_URL` via ESO and the value is absent from Git, values files and CI logs (grep check). (FR-1.11)

### 0.1.9 Test plan and fixtures

Fixtures: `fixtures/pipeline/negative-lint.go` (committed lint failure behind a build tag), `fixtures/pipeline/unsigned-image.yaml`, `fixtures/pipeline/cve-exception.yaml` (expired and valid), `fixtures/hello-ledger/entries-100.json` (100 entries in USD and EUR).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Lint/unit negative | CI contract | pipeline | both injected failures block merge |
| Local bootstrap | E2E | `make deploy-local` | < 10 min, all pods Ready, TLS valid |
| Promote and rollback | E2E | Argo CD | both < 15 min, history correct |
| Admission unsigned | Negative | policy-controller | pod denied |
| SBOM presence | Contract | registry | SPDX attached to every digest |
| hello-ledger API | Unit + contract | handlers | 100% of OpenAPI paths covered |
| Build reproducibility | Property | BuildKit | same source → same digest twice |

Reconciliation: `Release` rows with status `deployed_<env>` equal the digests in each env overlay and the running pods' image IDs (nightly check).

### 0.1.10 Deployment and operations

Charts: `deploy/charts/platform-base` (umbrella for operators), `deploy/charts/hello-ledger`. Config: `values.yaml` per env; env vars `PASSAGE_ENV`, `OTEL_EXPORTER_OTLP_ENDPOINT` (used from wave 5), `DATABASE_URL` (ESO). Migrations: Atlas, additive-only, run as an init job. Feature flags: none. Scheduled jobs: nightly release reconciliation. SLOs: pipeline p50 ≤ 8 min, promotion merge→Healthy p95 ≤ 15 min, hello-ledger availability 99.9%. Alerts: `ArgoAppDegraded` > 10 min, `PipelineFailureRate` > 20% over 1 h, `AdmissionDeniedSpike`. Runbook entries: Argo out of sync, failed migration job, expired local CA. Rollback: `passage rollback`, then verify `/v1/version`.

### 0.1.11 Migration from AX / SAP

Nothing is migrated. LCS packages, AX model store layers and SAP transport requests contain code (X++/ABAP), which PRD §2 excludes. The only carry-over is organizational: the STMS DEV→QAS→PRD route maps to dev→stage→prod and LCS environment names are recorded in `Environment.description` for operator familiarity.

### 0.1.12 Dependencies, risks, open questions

Depends on: nothing (first wave).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| k3d cannot run the full stack in 16 GB by wave 5 | Medium | Medium | Profiles: `make deploy-local PROFILE=min` omits observability |
| Keyless cosign requires outbound access to Fulcio/Rekor | Medium | Low | Private sigstore instance documented for air-gapped installs |
| GitHub Actions minutes cost | Low | Low | Self-hosted runner module in `infra/` |

Open questions: (1) First cloud provider for `dev`/`stage` (PRD §12 Q4) — default EKS; decide by wave 2 start. (2) Private registry vs ghcr.io for customers — default ghcr.io with mirror instructions; decide by wave 5. (3) Mesh (none vs Linkerd) for mTLS — default none, gateway-to-service mTLS only; decide by wave 4.

### 0.1.13 Parameters and setup

Platform parameters live in Git (`deploy/envs/<env>/values.yaml`, `infra/modules/cluster/variables.tf`, repository settings) rather than in a database form; "scope" below names the file or object that owns the value.

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| `cluster.provider` | cluster (OpenTofu variable) | enum | `k3d` | k3d, eks, gke, aks, baremetal | selects the provider sub-module for networking, node pools, storage classes | LCS environment topology (cloud-hosted / on-premises) | SAP system landscape (SLD, STMS system overview) |
| `cluster.kubernetes_version` | cluster | semver | 1.31 | n−2 … n of upstream | pins the control plane and node pools | LCS platform update version | Kernel / SAP_BASIS release (System → Status) |
| `cluster.node_pools[]` | cluster | list of {name, size, count, labels} | 3 × 8 GB (k3d) | provider-specific | sizes compute; `passage.dev/workload` label routes stateful sets | LCS environment sizing (tier) | SM51 application servers, RZ12 logon groups |
| `environment.promotion_policy` | environment (`platform.environment`) | enum | auto (dev), pr_approval (stage, prod) | auto, pr_approval | auto-sync vs. PR with required reviewers | LCS "apply package" approval | STMS_QA quality assurance approval, STMS transport route target group |
| `environment.soak_minutes` | environment | int | 0 dev, 60 stage | 0–1,440 | minimum time a digest must run in the prior tier before promotion | none | none (manual import scheduling in STMS_IMPORT) |
| `argo.sync.self_heal`, `argo.sync.prune` | environment overlay | bool | true, true | true/false | drift corrected automatically; removed manifests deleted | none | `tp` profile parameter `repeatonerror` (closest) |
| `argo.sync_waves` | overlay annotation | ordered ints | operators −2, databases −1, services 0, routes 1 | −10 … 10 | start order per sync | AxUpdateInstaller step order | STMS import order / import all |
| `ci.unit_coverage_min_pct` | repository (`.passage-ci.yaml`) | int | 70 | 0–100 | fails the `unit` check below threshold | Best Practice check severity | ATC check variant (SCI) priority |
| `ci.vuln.fail_on` | repository | enum set | critical, high_with_fix_older_than_14d | critical, high, high_with_fix_older_than_14d, medium | scan gate policy (FR-1.6) | none | none (SAP Security Notes via SysRec) |
| `ci.vuln.exception_max_days` | repository | int | 30 | 1–90 | maximum validity of a `.vuln-exceptions.yaml` entry | none | none |
| `ci.sbom.format` | repository | enum | spdx-2.3 | spdx-2.3, cyclonedx-1.6 | SBOM attestation format | none | none |
| `admission.verified_namespace_label` | cluster (policy-controller `ClusterImagePolicy`) | string | `passage.dev/verified=true` | label selector | namespaces where unsigned images are denied | strong-name signing requirement for models (`AxUtil /key`) | TMS trusted systems (SE06, SMT1) |
| `admission.oidc_identity` | cluster | regex | `https://github.com/passage/.*` | issuer + subject regex | identity accepted by cosign keyless verification | none | none |
| `registry.image_retention_days` | registry | int | 180 | 30–3,650 | untagged, unreferenced digests deleted after this | LCS asset library retention | none (transport files kept in `/usr/sap/trans`) |
| `registry.sbom_retention_years` | registry | int | 7 | 1–10 | SBOM and attestation retention | none | none |
| `secrets.path_template` | cluster (ESO `ClusterSecretStore`) | string | `passage/<env>/<svc>/<key>` | Vault KV v2 path | where every service secret is read from | none (web.config / AOS config) | SECSTORE (secure storage), RSECADMIN |
| `local.profile` | developer (`make deploy-local PROFILE=`) | enum | full | min, full | `min` omits observability and Temporal UI to fit 16 GB | OneBox VHD size | NetWeaver developer edition install profile |
| `hello_ledger.idempotency_window_hours` | service values | int | 24 | 1–168 | replay window for `Idempotency-Key` | none | none |

### 0.1.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Release history | list inquiry | service, environment, status, date range | service, digest (short), git sha, chart version, status, created_at, signed, sbom link | `platform.release` | LCS Environment details → History / Applied updates | STMS import history (STMS_IMPORT → Goto → Import history), SE10 request log |
| Environment status | drill-down | environment | app, health, sync status, revision, digest, last sync, soak elapsed | Argo CD Application CRs + `platform.environment` | LCS environment monitoring page | STMS system overview, SM51 |
| Deployment reconciliation | operational report (nightly) | environment | service, overlay digest, running digest, release status, mismatch flag | overlay files vs. pod image IDs vs. `platform.release` | LCS "Environment details → Version" check | STMS import queue vs. SE03 object directory comparison |
| CVE exposure by environment | operational report | severity, environment, fixed-available flag | CVE id, package, version, fixed version, digest, environment, first seen | Grype/Trivy results attached to each digest | none | Solution Manager System Recommendations (SysRec) for security notes |
| Vulnerability exceptions | list inquiry | expiry window | CVE, service, justification, approver, expires_at, days left | `.vuln-exceptions.yaml` (parsed in CI) | none | SNOTE note status / RSECNOTE |
| Pipeline statistics | KPI | period (day/week), branch | runs, median duration, p95 duration, failure rate by check | GitHub Actions API | Azure DevOps build pipeline analytics (D365) | STMS import log statistics, ChaRM cycle reports |
| Admission denials | operational report | namespace, period | pod, image, reason (unsigned / wrong issuer), time | policy-controller Kubernetes events | none | TP system log (SLOG/ALOG) rejected imports |
| Service version page | drill-down | service | git sha, digest, chart version, build time, Go/Node version | `GET /v1/version` of each service | Help → About (kernel and application build numbers) | System → Status (component versions, SPAM/SAINT level) |
| Hotfix lineage | list inquiry | service | hotfix branch, base prod digest, cherry-picked commits, status | Git refs + `platform.release` | LCS hotfix KB list, "Applied hotfixes" | SNOTE implemented notes, SPAU |
| Image retention candidates | operational report | registry | digest, service, last referenced env, age, SBOM present | registry API + `platform.release` | LCS asset library | none |

### 0.1.15 Printed and electronic documents

No printed documents. Electronic artifacts produced by the wave:

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| SBOM | every `main` build | SPDX document: creator, package list (name, version, license, PURL), relationships, digest it describes | SPDX 2.3 JSON, attached to the image as a cosign attestation | identified by image digest, no sequence | retained 7 years (BR-1.7); evidence for wave 48 SOC 2 CC8.1 |
| Provenance attestation | every signed build | in-toto statement: builder id, source repo + sha, materials, build config | in-toto JSON (SLSA v1), Rekor transparency log entry | Rekor log index | retained with the image; immutable in Rekor |
| Promotion PR summary | `passage promote` | title (service, from → to env, digest), checklist (soak met, scan clean, rollback target), SBOM diff table, scan summary | GitHub PR body (Markdown) | PR number | retained with repository history |
| Release notes (per promotion to prod) | PR merge to prod overlay | commits since previous prod digest grouped by Conventional Commit type, linked issues | Markdown in GitHub Deployment description | release id | none |

### 0.1.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| Release reconciliation (overlay vs. running vs. DB) | nightly 02:00 UTC | environment | re-runnable; writes a report row per (env, service) keyed by date | < 2 min | report row `mismatch=true`; no automatic correction | `ReleaseDriftDetected` (ticket) |
| Image retention sweep | weekly Sunday 03:00 UTC | service | deletes only digests not referenced by any `platform.environment`; dry-run list stored first | < 10 min | abort on registry 5xx; rerun next week | `RegistryRetentionFailed` (notify) |
| Deployed-digest re-scan | daily 04:00 UTC | environment × service | scan result keyed by (digest, scanner db version) | < 15 min for 30 images | partial results kept; missing scans listed | `NewCriticalCVEInProd` (page during business hours) |
| Vulnerability exception expiry check | daily 05:00 UTC | repository | stateless | < 1 min | opens a GitHub issue per expiring exception (≤ 7 days) | `VulnExceptionExpiring` (notify) |
| Local CA renewal (k3d) | on `make deploy-local`, and every 300 days | developer machine | reissues only when < 30 days left | < 30 s | prints instruction to re-trust the CA | n/a |
| Rekor inclusion verification | weekly | service | verifies each prod digest's signature entry is still present | < 5 min | alert; no deployment change | `SignatureVerificationFailed` (page) |

### 0.1.17 Security duties and privileges

Wave 1 has no OPA-enforced API yet (wave 2); the duties below are enforced by GitHub branch protection, CODEOWNERS, Argo CD RBAC (mapped from GitHub teams) and Vault policies, and are re-declared as Passage duties in wave 2 so the same names hold later.

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles that include it |
|---|---|---|---|
| View platform releases | `GET /v1/platform/releases`, Argo CD read-only UI, `passage status` | none | Platform engineer, Release manager, Auditor |
| Maintain platform releases | `POST /v1/platform/releases/{id}/promote` (dev → stage), `passage promote --to stage`, merge to `deploy/envs/dev`, `deploy/envs/stage` | with Approve production promotion (same person cannot author and sole-approve the prod PR; enforced by required reviewers ≠ author) | Platform engineer |
| Approve production promotion | review + merge of PRs touching `deploy/envs/prod` (CODEOWNERS `@passage/release-managers`) | with Maintain vulnerability exceptions | Release manager |
| Rollback environment | `passage rollback --env <env>`, Argo CD rollback action | none (rollback is always allowed to anyone who can promote to that env) | Platform engineer (dev, stage), Release manager (prod) |
| Maintain vulnerability exceptions | edit `.vuln-exceptions.yaml` (CODEOWNERS `@passage/security`) | with Approve production promotion | Security engineer |
| Maintain cluster configuration | `tofu apply` on `infra/`, edit `deploy/charts/platform-base` | with Maintain secrets (one person should not hold both cluster admin and Vault root) | Platform engineer |
| Maintain secrets | Vault policy `passage/<env>/*` write; ESO `ClusterSecretStore` edit | with Maintain cluster configuration | Security engineer |
| Maintain hello ledger | `POST /v1/hello-ledger/entries` (token-gated from wave 2) | none | Platform engineer (demo only) |

### 0.1.18 Message catalog

Codes `PLT-01xx` are emitted by CI checks, the `passage` CLI, the admission controller and the hello-ledger service; CI messages are also written as GitHub check annotations.

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| PLT-0101 | error | Overlay references image by tag `{tag}`; digests are required | `image.tag` or `image:tag` found in `deploy/envs/**` | replace with `image.digest: sha256:…` from the release history | BR-1.2, FR-1.4 |
| PLT-0102 | error | Promotion to prod blocked: digest ran {minutes} of {required} soak minutes in stage | soak not met | wait, or lower `soak_minutes` with Release manager approval | BR-1.3 |
| PLT-0103 | error | Promotion to prod blocked: SLO burn alert `{alert}` fired during soak | `SLOBurnRateHigh` within the window | investigate the alert; re-soak after fix | BR-1.3 |
| PLT-0104 | warning | A promotion of {service} to {env} is already open (#{pr}); this PR was closed | concurrent promotion | wait for the first PR | BR-1.4 |
| PLT-0105 | error | No rollback target: no earlier `deployed_{env}` release with a different digest | first release in the environment | deploy an earlier digest explicitly with `passage promote --digest` | BR-1.6 |
| PLT-0106 | error | Vulnerability exception for {cve} expired on {date} | exception past expiry | renew with new justification (≤ 30 days) or fix | BR-1.5, FR-1.6 |
| PLT-0107 | error | Scan gate failed: {n} CRITICAL / {m} actionable HIGH findings | FR-1.6 policy | upgrade the package or add a justified exception | FR-1.6 |
| PLT-0108 | error | Pod denied: image {image} has no valid cosign signature from `{identity}` | admission policy | build through CI; never push hand-built images to verified namespaces | FR-1.7 |
| PLT-0109 | error | Hotfix branch `{branch}` is not based on a deployed_prod release | wrong merge base | `git rebase --onto <prod sha>` | BR-1.8 |
| PLT-0110 | error | Unit coverage {pct}% below minimum {min}% for {service} | `ci.unit_coverage_min_pct` | add tests | FR-1.5 |
| PLT-0111 | error | Possible secret committed: {rule} at {path}:{line} | gitleaks rule hit | remove, rotate the secret in Vault, force-push is not allowed; add to history rewrite ticket | FR-1.11 |
| PLT-0112 | error | Migration job for {service} failed: {atlas error} | Atlas apply error | fix migration; additive-only rule means no down migration is needed | FR-1.10 |
| PLT-0113 | warning | Local bootstrap needs 16 GB RAM; {gb} GB detected — using PROFILE=min | insufficient memory | accept reduced profile or free memory | FR-1.9 |
| PLT-0114 | info | Idempotent replay: returning the entry created at {time} for this Idempotency-Key | duplicate POST within 24 h | none | FR-1.8 |
| PLT-0115 | error | Build not reproducible: digest {a} ≠ {b} for the same source | non-deterministic build input | pin the input (timestamp, dependency) | 0.1.9 reproducibility test |

### 0.1.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| PR pipeline (lint → sign) | 40 runs/day, peak 10 concurrent | 6 min / 8 min / 12 min | 20 concurrent runners | CI cache ≤ 20 GB (LRU) | path-filtered per service; cache keyed by lockfile hash | queued runs wait; no timeouts under 60 min |
| Promotion merge → Argo Healthy | 10/day across envs | 5 min / 15 min / 20 min | n/a | `platform.release` ≈ 10 k rows/year (≈ 5 MB) | `release(service, created_at desc)` | Argo reconciles in order; sync waves prevent partial start |
| Rollback | 1/week | 2 min / 5 min / 8 min | n/a | n/a | n/a | same as promotion |
| Admission webhook (signature verify) | 500 pod creations/day, 50/min at rollout | 50 ms / 200 ms / 500 ms | 20/s | none | verification result cached per digest for 1 h | failurePolicy=Fail in verified namespaces; cached digests still admitted |
| hello-ledger `POST /entries` | 10 k/day (demo) | 10 ms / 40 ms / 80 ms | 200 rps | ≈ 1 GB/year at 10 M rows | `entry(created_at)` | Postgres connection pool 20; 503 beyond queue depth 100 |
| Registry pulls at rollout | 30 images × 3 nodes per env | 5 s / 20 s / 40 s per image | 100 MB/s | images ≈ 200 GB/year before retention | retention sweep (0.1.16) | node image cache; rollouts slower, not failed |
| `make deploy-local` | 5/day per engineer | 7 min / 10 min / 12 min | n/a | 15 GB disk per environment | n/a | PROFILE=min |

### 0.1.20 Localization and compliance hooks

- Nothing in this wave varies by country or language; all timestamps are UTC ISO 8601 and all CLI/CI messages are English only (label packs for CLI output are not planned).
- Data residency: a customer runs the `infra/modules/cluster` module in the region of their choice; nothing in `deploy/` assumes a region. Wave 48 country packs do not touch this wave; wave 50 (multi-region) adds region-aware overlays under `deploy/envs/<env>-<region>`.
- Air-gapped and export-controlled sites: registry mirror instructions (open question 2) and a private sigstore (Fulcio/Rekor) profile in `infra/modules/sigstore-private`; `admission.oidc_identity` is the extension point.
- Supply-chain regulation (EU Cyber Resilience Act, US EO 14028): SBOM + provenance per digest (0.1.15) are the evidence artifacts; retention 7 years.
- Privacy: no personal data is processed by this wave except Git author identities in commit history (retained as part of source control; excluded from erasure requests under wave 48 privacy tooling, documented).

### 0.1.21 Coverage closure

No `GAP` or `PARTIAL` row in coverage-ax.md §12.99 or coverage-sap.md §13.99 is assigned to wave 1 (both registers parsed on 2026-10-21). Inventory rows tagged `COVERED-1` (SAP §13.30: SE09/SE10/SE01/SE03/STMS/STMS_IMPORT/SE06/SE07 transport organizer and management; STRUST/SSFA/SSO2/SAML2/SNCWIZARD trust manager; STMS import history and SE10 request logs; STMS import jobs / transport of copies / ChaRM cycles) are met by FR-1.1–FR-1.11 and 0.1.14.

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| none | — | — | Wave 1 has no register rows; see paragraph above for the `COVERED-1` confirmation |

---

## 0.2 Wave 2: Identity, Tenancy & Authorization

### 0.2.1 Scope and business context

AX 2012 authorizes through `SecurityRole` → `SecurityDuty` → `SecurityPrivilege` → `SecurityEntryPoint` with access levels (NoAccess, Read, Update, Create, Correct, Delete), table permissions in the AOT, extensible data security policies (XDS) and `UserInfo` rows tied to Active Directory; companies are isolated by `DataAreaId` on every per-company table. SAP uses `SU01` user masters (USR02), `PFCG` roles (AGR_DEFINE, AGR_1251 field values, AGR_USERS), authorization objects such as `S_TCODE`, `F_BKPF_BUK` (company code) and `M_MATE_WRK` (plant), `SU24` proposals, `SU53` failed-check traces, and `MANDT` for client isolation. Wave 2 gives Passage one identity provider (Keycloak) federated to Entra ID, Okta or Google, SCIM provisioning, an AX-shaped role model compiled to Rego, OPA as a sidecar on every service, Envoy Gateway JWT validation with per-tenant rate limits, and Postgres RLS on every table. At the end of the wave a user logs in once, lands in a legal entity, and every API call is allowed or denied by policy, not code. Out of scope: segregation-of-duties conflict analysis (wave 48), position hierarchies (wave 42), record-level XDS-style data policies beyond tenant and legal entity (wave 15 adds dimension security), customer-facing portals (waves 20, 30).

#### Pain points solved (10x lens)

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| Diagnosing "no authorization" in SAP needs SU53 on the user's session, then SUIM, then PFCG edit, role generation and user comparison (PFCG → Authorizations → Generate → User tab → User comparison): 6 screens and often a transport | Authorization data lives in profiles generated from roles; failures are only visible in the user's own buffer | Every 403 carries `duty_missing`; the effective-permission viewer shows the decision trace for any user × entity × action; assignment takes effect in ≤ 30 s without a transport | Access-fix cycle: 1–3 days (ticket + transport window) → < 5 min; screens: 6 → 1 |
| AX security development: a new entry point needs a privilege, duty and role in the AOT, synchronization and an AOS restart; D365 needs a build and package | Security objects are AOT metadata compiled into the model store | Role/duty/privilege are data compiled to a Rego bundle on save | Role change to live: 1–2 h (AX) / 4 h (D365 package) → ≤ 5 s compile + 10 s propagation |
| Company isolation relies on every X++ query honoring `DataAreaId` (and `crossCompany` misuse) or every ABAP select carrying MANDT; leaks are code bugs | Isolation enforced in application code, not the database | Postgres RLS with `FORCE ROW LEVEL SECURITY`; the app role cannot bypass it | Code paths that can leak a tenant: every query → 0; cross-tenant CI test on 100% of tables |
| Joiners/leavers: SU10 mass maintenance or D365 user import run manually; leavers keep access for days | No standard provisioning protocol in ECC; D365 ties to Entra only | SCIM 2.0 server with deprovision revoking sessions in ≤ 60 s | Deprovision latency: days → < 60 s; manual user admin steps per joiner: ~10 → 0 |
| MFA and federation to Okta/Google are unsupported on AX 2012; S/4 needs SAP IAS/SNC projects | IdP coupling to AD/Entra or SAP's own stack | Keycloak brokering any OIDC/SAML IdP with TOTP/WebAuthn | Federation to a new IdP: a multi-week project → < 1 h of configuration |

### 0.2.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| User master | `UserInfo`, SysUserInfoPage form, AD import wizard | SU01, USR02/USR21, SU10 mass maintenance | identity `User` entity, Admin UI Users, SCIM /Users | full |
| Role | `SecurityRole`, SysSecRoles form | PFCG single/composite/derived roles, AGR_DEFINE | authz `Role` (+ `derived_from` for org-level variants) | full |
| Duty / privilege | `SecurityDuty`, `SecurityPrivilege`, `SecurityRoleTaskGrant`, `SecurityTaskEntryPoint` | Authorization objects/fields (TOBJ, AGR_1251), SU24 defaults | `Duty`, `Privilege`, `Permission(entity, action, scope)` | full |
| Access levels | NoAccess/Read/Update/Create/Correct/Delete on entry points and tables | ACTVT 01/02/03/06 etc. per object | action enum: read, create, update, correct, delete, post, approve | full |
| Company isolation | `DataAreaId`, `CompanyInfo`, `changecompany()` | MANDT client, BUKRS org level in roles | tenant_id RLS + legal_entity_id scope on RoleAssignment | improved |
| Org-scoped roles | `OMUserRoleOrganization` (role valid for selected legal entities) | Derived roles with org-level values (BUKRS, WERKS) | `RoleAssignment.legal_entity_ids[]` or `all` | full |
| Effective permissions | Security development tool, "View permissions" | SUIM, SU56 user buffer, SU53 trace | `GET /v1/authz/effective-permissions`, decision debug endpoint | improved |
| Identity federation / MFA | Entra ID (AAD) only on D365; AD on AX 2012 | SAML via Identity Authentication Service, SNC | Keycloak OIDC/SAML brokering, TOTP/WebAuthn MFA | improved |
| Provisioning | AD sync, D365 user import | SAP IPS SCIM, CUA | SCIM 2.0 server (RFC 7644) | full |

### 0.2.3 Functional requirements

- **FR-2.1 Keycloak as IdP** — Must. The system shall deploy Keycloak with one realm per tenant, brokering OIDC and SAML 2.0 to external IdPs, issuing RS256 JWTs with claims `sub`, `tid` (tenant), `email`, `preferred_username`, `pv` (policy bundle version), `amr`.
  - Notes: access token TTL 10 minutes, refresh 8 hours, absolute session 12 hours; MFA (TOTP or WebAuthn) required for roles flagged `mfa_required`; realm export committed as Helm values for reproducibility.
- **FR-2.2 SCIM provisioning** — Must. The system shall expose `/scim/v2/Users` and `/scim/v2/Groups` per tenant with filter, PATCH and bulk (≤ 1,000 operations) per RFC 7644; group membership maps to role assignment via a configurable mapping table.
  - Notes: deprovision sets `status=deprovisioned`, revokes sessions within 60 s, never deletes the row (audit needs it).
- **FR-2.3 Tenant and legal entity model** — Must. The system shall model `Tenant` → `LegalEntity` → `Site` (shell only; detailed in wave 6) and stamp `tenant_id` on every table in every domain database.
  - Notes: `legal_entity_id` is stamped on per-company tables (AX `SaveDataPerCompany=Yes` equivalent, declared in metadata in wave 3); a user's current legal entity is a session attribute, not a token claim, so switching does not require re-login.
- **FR-2.4 Row-level security** — Must. Every table shall have `ENABLE ROW LEVEL SECURITY` and `FORCE ROW LEVEL SECURITY` with policy `tenant_id = current_setting('app.tenant_id')::uuid`; services connect as role `passage_app` with `NOBYPASSRLS` and set the variable with `SET LOCAL` inside every transaction.
  - Notes: a missing `app.tenant_id` yields zero rows, not an error; migrations run as `passage_migrate`, which is also NOBYPASSRLS; a CI check fails if any table lacks the policy.
- **FR-2.5 Role/duty/privilege model** — Must. The system shall store Role → Duty → Privilege → Permission where Permission = (entity, action, field_mask?, condition?), and compile the closure into one Rego bundle per tenant.
  - Notes: actions are read, create, update, correct, delete, post, approve, export; `correct` retains AX semantics (edit date-effective rows); 20 seeded duties ship (Maintain users, Maintain security roles, Maintain vendors, View vendors, Maintain entity metadata, Apply extensions, Maintain workflows, Approve workflow, Maintain batch jobs, View batch history, Maintain number sequences, View audit log, Import data, Export data, Maintain platform releases, View platform releases, Maintain tenants, Maintain legal entities, Maintain saved views, Maintain hello ledger).
- **FR-2.6 Policy compilation** — Must. The system shall compile a bundle within 5 s of any role/duty/privilege/assignment change and distribute it to OPA sidecars via bundle polling (interval 10 s) with the version in `pv`.
  - Notes: a token whose `pv` is older than the active bundle by more than 2 versions is still honored (bundles are backward compatible); deny reasons are returned as `{"code":"AUTHZ_DENIED","duty_missing":"Maintain vendors"}`.
- **FR-2.7 OPA decision point** — Must. Every service shall call its OPA sidecar with `input = {subject, resource, action, context}` and enforce the decision before any database call; p95 decision latency ≤ 2 ms, p99 ≤ 5 ms.
  - Notes: `resource` carries entity, id, tenant_id, legal_entity_id and the field list for field-level masks; the gateway performs coarse checks (token validity, tenant match), services perform fine checks.
- **FR-2.8 Gateway** — Must. Envoy Gateway shall validate JWT signature, issuer and audience against the tenant realm's JWKS (cached 5 min), reject tokens with `tid` not matching the host's tenant, and enforce per-tenant rate limits (default 1,000 rps sustained, 2,000 burst; 429 with `Retry-After`).
- **FR-2.9 Role assignment with scope and effective dates** — Must. The system shall assign roles to users with `legal_entity_ids` (or `all`) and `effective_from`/`effective_to` (ISO 8601 dates, tenant time zone, inclusive).
  - Notes: expired assignments are excluded at compile time; a nightly job recompiles bundles for tenants with assignments crossing midnight.
- **FR-2.10 Admin UI** — Must. The system shall provide screens for users, roles, duties, privileges, assignments and an effective-permission viewer that renders the OPA decision for a chosen user × entity × action × legal entity.
- **FR-2.11 Service accounts and agents** — Should. The system shall support non-human principals (`kind=service|agent`) with client-credentials tokens and role assignments; an agent principal may hold only roles that are a subset of its sponsoring user's roles.

### 0.2.4 Data model

**User** (`identity.user`)

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK; equals Keycloak `sub` |
| tenant_id | uuid | yes | FK tenant; RLS |
| username | varchar(100) | yes | unique per tenant, case-insensitive |
| email | varchar(254) | yes | unique per tenant; PII |
| display_name | varchar(200) | yes | PII |
| kind | enum | yes | human, service, agent |
| status | enum | yes | invited, active, locked, disabled, deprovisioned |
| default_legal_entity_id | uuid | no | FK legal_entity |
| external_id | varchar(200) | no | SCIM externalId |
| mfa_enrolled | bool | yes | default false |
| last_login_at | timestamptz | no | |
| created_at, updated_at, version | timestamptz, timestamptz, int | yes | optimistic lock |

**Role / Duty / Privilege / Permission** (`authz.*`)

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK (each table) |
| tenant_id | uuid | yes | `sys` rows have the reserved system tenant and are read-only |
| code | varchar(64) | yes | unique per tenant; e.g. `VendMaintain` |
| name, description | varchar(120), text | yes/no | localized labels in wave 48 |
| layer | enum | yes | sys, partner, customer |
| parent_id | uuid | no | Role→Duty, Duty→Privilege via link tables `role_duty`, `duty_privilege`, `privilege_permission` |
| entity, action | varchar(64), enum | yes (Permission) | action ∈ read, create, update, correct, delete, post, approve, export |
| field_mask | text[] | no (Permission) | fields hidden when present |
| condition | jsonb | no (Permission) | Rego-compatible expression, e.g. `{"legal_entity_in":"$subject.legal_entities"}` |
| mfa_required | bool | yes (Role) | |

**RoleAssignment** (`authz.role_assignment`)

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| tenant_id | uuid | yes | RLS |
| user_id | uuid | yes | FK user |
| role_id | uuid | yes | FK role |
| legal_entity_ids | uuid[] | yes | empty array means all |
| effective_from | date | yes | default today |
| effective_to | date | no | null = open-ended |
| granted_by | uuid | yes | FK user |
| reason | text | no | required when `role.break_glass=true` |

**Tenant** / **LegalEntity**: id, code (varchar 10, unique), name, status (active, suspended), default_currency (char 3), time_zone (IANA), created_at; LegalEntity adds tenant_id and `country` (ISO 3166-1 alpha-2).

Invariants: a user has at most one active assignment per (role, legal entity set) overlapping in time; `sys` layer rows are immutable in customer tenants; every Permission entity name must exist in the metadata registry from wave 3. Indexes: `user(tenant_id, lower(email))` unique; `role_assignment(tenant_id, user_id)`; `role_assignment(tenant_id, effective_to)` for expiry sweeps. Keycloak keeps its own Postgres (CNPG) per environment.

### 0.2.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| GET/POST | /v1/identity/users | List / create users | POST: Idempotency-Key | Maintain users |
| PATCH | /v1/identity/users/{id} | Update status, default legal entity | version check (If-Match) | Maintain users |
| GET/POST/PUT | /scim/v2/Users, /scim/v2/Groups | SCIM provisioning | SCIM semantics | SCIM client credential |
| GET/POST/PUT | /v1/authz/roles, /duties, /privileges | Maintain the model | PUT by code | Maintain security roles |
| GET/POST/DELETE | /v1/authz/assignments | Grant / revoke | POST: natural key | Maintain security roles |
| GET | /v1/authz/effective-permissions?user_id=&legal_entity_id= | Flattened permissions | n/a | Maintain security roles or self |
| POST | /v1/authz/decision | Dry-run an OPA decision (debug) | n/a | Maintain security roles |
| POST | /v1/authz/policies/compile | Force recompile | bundle version | Maintain security roles |
| GET/POST | /v1/tenants, /v1/legal-entities | Tenancy shells | POST: code | Maintain tenants / Maintain legal entities |
| POST | /v1/session/legal-entity | Switch current legal entity | n/a | any active user |

Commands validate: email RFC 5322; role code `^[A-Za-z][A-Za-z0-9]{2,63}$`; assignment dates `effective_from ≤ effective_to`; a revoke of a `sys`-layer role from the last `Tenant owner` is rejected (409).

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| dev.passage.identity.user.created.v1 | User insert (UI, SCIM, federation first login) | user_id, tenant_id, email, kind, source | workflow, audit, shell |
| dev.passage.identity.user.status-changed.v1 | status transition | user_id, from, to, actor, reason | audit, session revoker |
| dev.passage.authz.role-assignment.granted.v1 / revoked.v1 | assignment insert/delete | assignment_id, user_id, role_id, legal_entity_ids, effective_from, effective_to | policy compiler, audit |
| dev.passage.authz.policy.compiled.v1 | bundle built | tenant_id, bundle_version, sha256 | OPA sidecars, audit |
| dev.passage.identity.login.succeeded.v1 / failed.v1 | Keycloak event listener | user_id, ip, idp, amr | audit, anomaly alert |

### 0.2.6 User experience

| Screen | Primary actions | Key fields/columns | Duty required |
|---|---|---|---|
| Users list/detail | Invite, lock, disable, assign roles, set default legal entity | username, email, status, kind, last login, MFA | Maintain users |
| Roles | Create, add duties, mark MFA/break-glass | code, name, layer, duty count, user count | Maintain security roles |
| Duties / Privileges | Create, add privileges / permissions | code, entity, action, field mask | Maintain security roles |
| Role assignment | Grant with legal entity picker and dates | user, role, legal entities, from, to, reason | Maintain security roles |
| Effective permissions | Pick user × legal entity, inspect decision trace | entity, action, allow/deny, via role → duty → privilege | Maintain security roles |
| Legal entity switcher (shell header) | Switch current legal entity | code, name | any |

Rules: all grids are keyboard navigable (wave 5 shell completes this); role edits are saved explicitly, not on blur; a decision trace shows the first allowing path and all denying reasons; the switcher remembers the last legal entity per user in `Personalization` (wave 5).

### 0.2.7 Business rules and edge cases

- **BR-2.1** User status machine: invited → active; active ↔ locked (5 failed logins in 15 min locks for 30 min, admin may unlock); active → disabled → active; any → deprovisioned (terminal).
- **BR-2.2** Deny by default: no matching Permission means deny; explicit deny permissions do not exist (AX semantics), so removing access means removing the grant.
- **BR-2.3** Access level ordering for merge: delete > correct > update > create > read; the highest level across all roles wins per entity (AX `SecurityEntryPoint` union behavior).
- **BR-2.4** Legal-entity scoping: a permission is effective only in legal entities listed on the assignment; `all` includes legal entities created later.
- **BR-2.5** Effective dates are evaluated in the tenant time zone at compile time and at 00:05 nightly; a grant with `effective_from` in the future compiles into the bundle inactive.
- **BR-2.6** Revoking a session: status change to locked/disabled/deprovisioned revokes refresh tokens; access tokens remain valid at most 10 minutes (TTL), which is the documented worst case.
- **BR-2.7** Cross-tenant request (token `tid` ≠ host tenant) returns 404 rather than 403 to avoid tenant enumeration.
- **BR-2.8** Rate limit is per tenant per gateway replica, aggregated through the gateway's shared rate-limit service; service accounts count against the same bucket.
- **BR-2.9** Concurrency: two admins editing the same role use optimistic locking (`version`); the loser gets 409 with the current version.
- **BR-2.10** Agent principals cannot be granted `Tenant owner`, `Maintain security roles` or break-glass roles.

### 0.2.8 Acceptance criteria

- **AC-2.1** Given a user with duty "Maintain vendors", When they `PUT /vendors/{id}` (stub entity), Then 200; Given a user without it, Then 403 with `duty_missing`. Table-driven across 20 duties. (FR-2.5, FR-2.7)
- **AC-2.2** Given tenant A's app session, When a query filters `tenant_id = <tenant B>`, Then zero rows are returned under `passage_app`. (FR-2.4)
- **AC-2.3** Given two Keycloak realms federated to a stand-in Keycloak IdP in CI, When a user logs in through each, Then both sessions carry the correct `tid` and land in the user's default legal entity. (FR-2.1, FR-2.3)
- **AC-2.4** Given 100 random (user, entity, action, legal entity) tuples, When effective-permission report and `POST /v1/authz/decision` are compared, Then they agree 100%. (FR-2.6, FR-2.10)
- **AC-2.5** Given a SCIM PATCH deactivating a user, When processed, Then `status=deprovisioned`, refresh tokens are revoked within 60 s and the row persists. (FR-2.2)
- **AC-2.6** Given an assignment with `effective_to` yesterday, When the nightly recompile runs, Then the user's requests for that role's permissions are denied. (FR-2.9)
- **AC-2.7** Given 1,500 rps from one tenant for 10 s, When routed through the gateway, Then requests above the limit receive 429 with `Retry-After`. (FR-2.8)
- **AC-2.8** Given a role flagged `mfa_required`, When a user without MFA logs in, Then Keycloak forces enrollment before issuing a token. (FR-2.1)
- **AC-2.9** Given an agent principal sponsored by user U, When an assignment outside U's roles is attempted, Then 422 with `AGENT_EXCEEDS_SPONSOR`. (FR-2.11)

### 0.2.9 Test plan and fixtures

Fixtures: `fixtures/authz/duties-20.yaml` (20 duties with allow/deny expectations, 240 rows), `fixtures/identity/users-50.json` (two tenants, three legal entities), `fixtures/identity/keycloak-realm-ci.json` (two realms + broker), `fixtures/authz/decision-sample-100.json`.

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Duty matrix | Contract (table-driven) | gateway + OPA + stub service | 240/240 expected codes |
| RLS cross-tenant | Integration | every table (generated list) | 0 rows for foreign tenant; CI fails on any table without policy |
| Federation login | E2E (Playwright) | Keycloak broker | 2 IdPs, correct claims |
| Effective vs decision | Property | compiler | 100/100 agree |
| Bundle compile latency | Performance | compiler | ≤ 5 s for 500 roles / 2,000 duties |
| OPA decision latency | Performance | sidecar | p95 ≤ 2 ms at 2,000 rps |
| SCIM conformance | Contract | SCIM server | RFC 7644 test suite subset (filter, PATCH, bulk) |

Reconciliation: count of Keycloak users per realm equals `identity.user` rows with status ≠ deprovisioned; sum of permissions in the compiled bundle equals the flattened SQL closure.

### 0.2.10 Deployment and operations

Charts: `keycloak` (with CNPG database, 2 replicas), `identity`, `authz` (compiler + bundle server), `opa` as a sidecar template in `deploy/charts/_lib`, `envoy-gateway` policies (`SecurityPolicy` for JWT, `BackendTrafficPolicy` for rate limits). Config: `KEYCLOAK_ISSUER_TEMPLATE`, `OPA_BUNDLE_URL`, `AUTHZ_BUNDLE_POLL_SECONDS=10`. Migrations: additive Atlas. Feature flags: `authz.enforce` (log-only mode for the first week in dev). Scheduled jobs: nightly assignment expiry recompile 00:05 tenant time; JWKS refresh. SLOs: login success ≥ 99.9%, token issuance p95 ≤ 300 ms, OPA p95 ≤ 2 ms, bundle propagation ≤ 30 s. Alerts: `AuthzDenySpike` (> 5× baseline), `BundleStale` (> 5 min), `KeycloakDown`. Runbook: stale bundle, locked-out tenant owner (break-glass via Keycloak admin console), JWKS rotation. Rollback: digest rollback; bundles are versioned so a rollback of authz re-serves the prior bundle.

### 0.2.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| AX `UserInfo` (id, name, networkAlias, enable) | User | `networkAlias` → username; `enable=0` → disabled |
| AX `SecurityUserRole` (user, securityRole, validFrom/validTo) | RoleAssignment | dates carried; `OMUserRoleOrganization` rows → `legal_entity_ids` |
| AX `SecurityRole` / `SecurityDuty` / `SecurityPrivilege` + `SecurityRoleTaskGrant`, `SecurityTaskEntryPoint` | Role / Duty / Privilege | custom (CUS/VAR layer) roles only; SYS roles are mapped by name to Passage seeds |
| SAP USR02 (BNAME, UFLAG, GLTGV/GLTGB) | User | UFLAG 64/128 → locked; GLTGV/GLTGB → status dates |
| SAP AGR_USERS (AGR_NAME, UNAME, FROM_DAT, TO_DAT) | RoleAssignment | dates carried |
| SAP AGR_DEFINE / AGR_1251 (OBJECT, FIELD, LOW/HIGH) | Role / Duty / Privilege | authorization objects map via a crosswalk file to Passage permissions; BUKRS values → legal_entity_ids |

Transformations run through the wave 10 migration framework; this wave ships only the crosswalk format and a dry-run report. Not migrated: passwords (federated), SU24 proposals, AX entry-point grants on forms that have no Passage equivalent (reported as "unmapped" with counts).

### 0.2.12 Dependencies, risks, open questions

Depends on: wave 1 (cluster, Argo, Vault/ESO, gateway).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Keycloak realm-per-tenant does not scale past ~500 tenants | Low (v1) | Medium | Organizations feature evaluated by wave 48 |
| Rego closure bundle size for large role models | Medium | Medium | Partial evaluation; bundle per tenant; limit 50k permissions |
| SAML IdPs with non-standard attribute names | Medium | Low | Mapper templates for Entra ID, Okta, ADFS |

Open questions: (1) Field-level masks in P0 or deferred to wave 15 — default: masks compiled but enforced only for PII-flagged fields; decide by wave 3. (2) Session legal entity in a cookie vs header — default header `X-Passage-Legal-Entity`; decide by wave 5. (3) Tenant realm per tenant vs shared realm with groups — default per tenant; decide by wave 2 mid-point.

### 0.2.13 Parameters and setup

Tenant-scoped values are rows of `identity.tenant_settings` (edited on the Admin workspace → Tenant settings form, the AX "System parameters → Security" and SAP RZ10 `login/*` equivalent); realm values are Keycloak realm settings exported to Helm values.

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| `auth.access_token_ttl_minutes` | tenant (realm) | int | 10 | 5–60 | JWT lifetime; worst-case access after revoke (BR-2.6) | none (AD Kerberos ticket) | `rdisp/gui_auto_logout`, `login/ticket_expiration_time` |
| `auth.refresh_token_ttl_hours` | tenant (realm) | int | 8 | 1–24 | silent re-login window | none | `login/ticket_expiration_time` |
| `auth.session_max_hours` | tenant (realm) | int | 12 | 1–24 | absolute session cap | System parameters → Security: session timeout (D365 `SysUserLogTimeout`) | `rdisp/max_wprun_time`, SICF session timeout |
| `auth.lockout.failures`, `auth.lockout.window_minutes`, `auth.lockout.duration_minutes` | tenant (realm brute-force detection) | int, int, int | 5, 15, 30 | 3–10, 5–60, 5–1,440 | BR-2.1 lock behavior | none (AD policy) | `login/fails_to_user_lock`, `login/failed_user_auto_unlock` |
| `auth.mfa.required_roles` | tenant | role code list | `TenantOwner`, `SecurityAdmin` | any role codes | roles whose holders must enroll TOTP/WebAuthn | none | SNC / SAML2 enforced for user groups |
| `auth.mfa.allowed_methods` | tenant | enum set | totp, webauthn | totp, webauthn, recovery_code | enrollment options | none | none |
| `auth.local_password_policy` | tenant | {min_length, history, max_age_days} | 14, 5, 0 (no expiry) | min_length 12–64 | applies only to break-glass local accounts (federated users have none) | none | `login/min_password_lng`, `login/password_history_size`, `login/password_expiration_time`, USR40 |
| `auth.idp_brokers[]` | tenant | list {alias, protocol oidc/saml, issuer/metadata URL, attribute mappers, first-login flow} | empty | — | federation targets; first login auto-links by verified email | Users → Active Directory import wizard, Claims users / Identity providers (D365 Entra tenant) | SAML2 trusted providers, SNC configuration, SM30 `VUSREXTID` |
| `scim.enabled`, `scim.group_role_mapping` | tenant | bool, map {group → role code} | false, {} | — | SCIM endpoints on/off; group → role | AD group import | SAP IPS mapping, CUA |
| `scim.bulk_max_operations` | tenant | int | 1,000 | 100–1,000 | RFC 7644 bulk limit | none | none |
| `authz.enforce` | environment / tenant feature flag | enum | enforce (log_only in dev first week) | enforce, log_only | log_only records would-be denies without blocking | none | `auth/no_check_in_some_cases` (SU24 proposals) |
| `authz.bundle_poll_seconds` | environment | int | 10 | 5–60 | OPA bundle polling | none (AOS cache refresh) | PFCG user comparison (PFUD) |
| `authz.bundle_max_permissions` | tenant | int | 50,000 | ≤ 100,000 | compile guard (risk table) | none | none |
| `authz.recompile_time` | tenant | HH:MM tenant time zone | 00:05 | any | nightly effective-date recompile (BR-2.5) | none | PFUD / PFCG_TIME_DEPENDENCY job |
| `gateway.rate_limit.sustained_rps`, `gateway.rate_limit.burst` | tenant | int, int | 1,000, 2,000 | 100–50,000 | FR-2.8 limits; 429 with `Retry-After` | none | ICM `icm/max_conn`, `rdisp/tm_max_no` |
| `gateway.jwks_cache_minutes` | environment | int | 5 | 1–60 | JWKS refresh | none | none |
| `session.legal_entity_header` | environment | string | `X-Passage-Legal-Entity` | header name | carries the current legal entity (open question 2) | `changecompany()` / company switch | SU3 default parameter BUK |
| `tenant.time_zone`, `tenant.default_currency` | tenant | IANA tz, ISO 4217 | `UTC`, `USD` | valid values | effective-date evaluation, defaults for later waves | System parameters → General (time zone), CompanyInfo currency | SCC4 client, OY01 country, OB22 company code currency |
| `legal_entity.country` | legal entity | ISO 3166-1 alpha-2 | required | valid code | country-pack hook (0.2.20) | Legal entities form → Addresses (country/region) | OX02 company code country |
| `agent.max_sponsor_subset` | tenant | bool | true | true | agents may hold only a subset of the sponsor's roles (FR-2.11) | none | SU01 reference user |
| `identity.deprovision_revoke_seconds` | tenant | int | 60 | 10–300 | session revocation deadline after SCIM deactivation | none | SU10 mass lock |

### 0.2.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Users | list inquiry | status, kind, role, legal entity, last login before | username, email, display name, kind, status, MFA, last login, default legal entity | `identity.user` | Users list (SysUserInfoPage) | SU01 / SU10 list, RSUSR002 users by complex criteria |
| Users by role | list inquiry | role, legal entity, as-of date | user, role, legal entities, effective from/to, granted by | `authz.role_assignment` ⋈ user | Security → Users by role (SSRS "User role assignments") | SUIM → User → by roles; AGR_USERS |
| Roles by user | drill-down | user | role, duties, legal entities, effective dates, break-glass flag | same | User details → Roles tab | SU01 → Roles tab, SUIM roles by user |
| Role permissions (closure) | drill-down | role | duty → privilege → permission (entity, action, field mask, condition) | compiled closure | Security roles → View permissions; SSRS "Security role report" | PFCG → Authorizations (display), SUIM → Roles → by authorization object |
| Effective permissions | drill-down | user × legal entity (× entity, action) | allow/deny, first allowing path, all denying reasons, bundle version | `GET /v1/authz/effective-permissions`, `POST /v1/authz/decision` | Security development tool → View permissions | SU53, SU56, STAUTHTRACE |
| Login history | list inquiry | user, outcome, IdP, date range, IP | time, user, outcome, IdP alias, `amr`, IP, user agent | Keycloak event listener → `identity.login_event` | Users → User log (SysUserLog) | SM20 security audit log (logon events), RSUSR200 logon dates |
| Online sessions | list inquiry | tenant, user | user, session start, last activity, IdP, client; action: revoke | Keycloak sessions API | Online users (SysUsersOnline) | SM04, AL08 |
| Expiring role assignments | operational report | horizon (default 30 days) | user, role, effective_to, granted by | `authz.role_assignment` | none | PFCG_TIME_DEPENDENCY log |
| Authorization denials | KPI / operational | period, duty, entity, user | denials per duty per hour, top users, top entities | OPA decision logs (Loki) → `authz.decision_stat` | none | SU53 (per user), SM20 RFC/auth failures |
| SCIM reconciliation | operational report | last run | users in IdP not in Passage, deprovisioned in IdP still active, group → role mismatches | SCIM sync job output | AD import wizard log | SAP IPS job log, CUA distribution log (SCUL) |
| Policy bundle versions | list inquiry | tenant | version, sha256, compiled at, role count, permission count, trigger | `authz.bundle` | none | PFCG profile generation log |
| Tenant and legal entity list | list inquiry | status | code, name, country, currency, time zone, user count | `identity.tenant`, `identity.legal_entity` | Legal entities (OMLegalEntity) | OX02 company codes, SCC4 clients |

### 0.2.15 Printed and electronic documents

No printed documents. Electronic documents:

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| User invitation email | user created with `status=invited` (UI or SCIM without federation link) | tenant name, inviter, link (24 h validity), IdP instructions | HTML + text email sent by Keycloak "execute actions" mail (tenant SMTP settings in the realm until wave 4 moves outbound email to FR-4.13) | none | link token single-use; email retained in Keycloak event log 30 days |
| MFA reset / enrollment email | admin resets MFA; `mfa_required` role granted to an unenrolled user | user, action, link | same | none | same |
| Effective-permission export | administrator action on the viewer | user, legal entity, bundle version, rows (entity, action, decision, path) | CSV, JSON | none | kept by the requester; wave 48 access reviews re-generate on demand |
| Security role report | administrator action | role, duties, privileges, permissions, assigned users as of date | CSV, PDF (wave 15 report designer; CSV only in wave 2) | none | SOC 2 evidence (wave 48) retained 7 years |

### 0.2.16 Periodic and batch jobs

Jobs run as Kubernetes CronJobs in wave 2 and are re-registered on the wave 4 batch framework without behavior change.

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| Effective-date recompile | daily 00:05 tenant time (BR-2.5) | tenant | compile is deterministic; skipped when no assignment crosses midnight | < 5 s per tenant | retry 3×; last good bundle keeps serving | `BundleStale` (> 5 min) |
| SCIM reconciliation | daily 01:00 UTC | tenant | compares IdP export to `identity.user`; writes report only | < 2 min per 10 k users | report with `errors[]`; no automatic deprovision | `ScimDriftDetected` (ticket) |
| Session cleanup | hourly | realm | Keycloak offline-session purge | < 30 s | n/a | none |
| Login anomaly aggregation | every 15 min | tenant | window keyed by (tenant, bucket) | < 10 s | skipped bucket recomputed next run | `LoginFailureSpike` (> 50 failures / 15 min / tenant) |
| Bundle garbage collection | daily 03:00 UTC | tenant | keeps last 10 versions + any referenced by a live `pv` | < 10 s | none | none |
| JWKS refresh | every 5 min (gateway) | realm | cache keyed by `kid` | < 200 ms | previous keys kept until rotation grace (24 h) | `JwksFetchFailed` |
| Keycloak realm export (backup) | daily 02:30 UTC | realm | export to object storage keyed by date | < 1 min | alert | `RealmBackupFailed` (ticket) |

### 0.2.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles that include it |
|---|---|---|---|
| Maintain users | `GET/POST /v1/identity/users`, `PATCH /v1/identity/users/{id}`, Users screen (invite, lock, disable), revoke session | with Maintain security roles (a holder of both can grant anything to anyone; self-grant is rejected with IAM-0416 regardless) | Security administrator |
| View users | `GET /v1/identity/users`, Users screen read-only, Login history, Online sessions | none | Security administrator, Auditor, Help desk |
| Maintain security roles | `/v1/authz/roles`, `/duties`, `/privileges`, `/assignments` (POST/DELETE), `/policies/compile`, Roles/Duties/Privileges/Assignment screens | with Maintain users; with Approve workflow for the same entity (wave 4) | Security administrator |
| View security reports | `GET /v1/authz/effective-permissions`, `POST /v1/authz/decision`, Role permissions, Users by role, Authorization denials | none | Security administrator, Auditor |
| Maintain identity providers | realm broker settings (`auth.idp_brokers[]`), mappers, MFA policy | with Maintain users | Tenant owner |
| Maintain SCIM clients | create/rotate SCIM client credentials; `scim.*` settings | none | Tenant owner |
| Maintain tenants | `GET/POST /v1/tenants`, tenant settings form | with Maintain legal entities (only in multi-tenant operator context) | Platform operator (system tenant) |
| Maintain legal entities | `GET/POST /v1/legal-entities`, Legal entities screen | none | Tenant owner, Organization administrator |
| Tenant owner (break-glass) | all of the above plus Keycloak admin console for the realm; every use requires `reason` (RoleAssignment.reason) and raises an audit event | with everything; limited to ≤ 2 human users per tenant, never agents (BR-2.10) | Tenant owner |

### 0.2.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| IAM-0401 | error | You do not have the duty "{duty}" required for this action | OPA deny; `duty_missing` | request the role containing the duty from a Security administrator | FR-2.6, FR-2.7 |
| IAM-0402 | error | Not found | token `tid` ≠ host tenant (returned as 404) | sign in to the correct tenant host | BR-2.7 |
| IAM-0403 | error | Too many requests; retry after {seconds} s | tenant rate limit | back off; service accounts share the bucket | FR-2.8, BR-2.8 |
| IAM-0404 | error | Session expired; sign in again | access and refresh tokens expired | sign in | FR-2.1 |
| IAM-0405 | warning | Your permissions were updated; refresh to apply | token `pv` more than 2 bundle versions behind | refresh the page (silent re-login) | FR-2.6 |
| IAM-0406 | info | Multi-factor enrollment is required for your role "{role}" | `mfa_required` role without enrolled factor | enroll TOTP or WebAuthn | FR-2.1, AC-2.8 |
| IAM-0407 | error | Account locked for {minutes} minutes after {n} failed sign-ins | lockout policy | wait or ask an administrator to unlock | BR-2.1 |
| IAM-0408 | error | Cannot revoke: {user} is the last Tenant owner | last-owner protection (409) | grant Tenant owner to another user first | 0.2.5 validation |
| IAM-0409 | error | Agent cannot hold "{role}": not within sponsor {user}'s roles | FR-2.11 subset rule (422 `AGENT_EXCEEDS_SPONSOR`) | grant the role to the sponsor first or choose another sponsor | FR-2.11, BR-2.10 |
| IAM-0410 | error | This role was changed by {user} at {time}; reload to see the current version | optimistic lock (409) | reload and reapply | BR-2.9 |
| IAM-0411 | error | Role code must match `^[A-Za-z][A-Za-z0-9]{2,63}$` | validation | rename | 0.2.5 validation |
| IAM-0412 | error | Effective from {from} is after effective to {to} | validation | correct the dates | FR-2.9 |
| IAM-0413 | error | SCIM bulk request exceeds {max} operations | RFC 7644 limit | split the request | FR-2.2 |
| IAM-0414 | error (alert) | Policy bundle compile failed for tenant {tenant}: {reason} | compiler error; previous bundle still serves | fix the offending permission condition | FR-2.6 |
| IAM-0415 | warning | Sign-in attempt by deprovisioned user {user} from {ip} | post-deprovision login | none; recorded in audit and login anomaly report | FR-2.2 |
| IAM-0416 | error | You cannot grant a role to yourself | self-grant attempt | ask another Security administrator | 0.2.17 SoD |
| IAM-0417 | error | Reason is required when granting a break-glass role | `RoleAssignment.reason` missing | provide a reason | 0.2.4 RoleAssignment |

### 0.2.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| Interactive login (federated) | 5,000 users/tenant, 2 logins/user/day; peak 300/min at 08:00 | 400 ms / 1.2 s / 2 s (excluding IdP) | 50 logins/s per Keycloak replica | login events ≈ 2.5 M rows (≈ 500 MB) per tenant | `login_event(tenant_id, occurred_at)` monthly partitions | Keycloak HPA 2 → 6 replicas; IdP latency dominates |
| Token refresh | 5,000 users × 6 refreshes/day | 50 ms / 150 ms / 300 ms | 500/s | none | n/a | same |
| OPA decision (sidecar) | 2,000 decisions/s per service at peak | 0.5 ms / 2 ms / 5 ms | 5,000/s per sidecar | none | bundle in memory, partial evaluation per entity | CPU-bound; sidecar requests 100 m / limit 500 m; latency rises linearly, no errors |
| Gateway JWT validation | 1,000 rps sustained per tenant | 0.3 ms / 1 ms / 2 ms | 20,000 rps per gateway replica | none | JWKS cache 5 min | rate limit 429 beyond `burst` |
| Bundle compile | 500 roles, 2,000 duties, 50 k permissions | 1 s / 5 s / 8 s | 10 compiles/min/tenant (debounced 2 s) | bundles ≈ 2 MB × 10 kept per tenant | one bundle per tenant | compile queue; stale bundle serves (`BundleStale` alert at 5 min) |
| Effective-permission report | 100/day | 200 ms / 800 ms / 1.5 s | 10/s | none | closure computed from bundle, cached 60 s | cached result served |
| SCIM bulk (1,000 ops) | 10/day | 5 s / 15 s / 30 s | 100 ops/s | `identity.user` ≈ 1 MB per 5 k users | `user(tenant_id, lower(email))` | queued; 429 to the SCIM client with `Retry-After` |
| Role assignment table | 5,000 users × 4 roles | n/a | n/a | ≈ 20 k rows/tenant (≈ 5 MB) | `role_assignment(tenant_id, user_id)`, `(tenant_id, effective_to)` | n/a |

### 0.2.20 Localization and compliance hooks

- Privacy: `identity.user.email`, `display_name` and login IPs are personal data (GDPR Art. 4, CCPA). Erasure requests (wave 48 privacy tooling) pseudonymize these columns (`user-<id>@erased.invalid`) and purge `login_event` rows older than the tenant's retention (`identity.login_event_retention_days`, default 365; EU tenants typically 180); the row itself is never deleted (audit integrity). `GET /v1/identity/users/{id}/export` serves the data-subject access export (JSON).
- Residency: Keycloak and the identity database run in the customer's chosen region (0.1.20); realm per tenant lets a multi-tenant operator pin a tenant's realm database to a regional CNPG cluster from wave 50.
- Country and legal entity: `LegalEntity.country` is the key the wave 48 country packs use to attach statutory roles (for example, a German "Datenschutzbeauftragter" read-only role) and password/MFA minimums (for example, NIS2-driven MFA for all administrative roles). Extension point: `TenantSettings.overrides` jsonb keyed by country, consumed by the Keycloak values template and the compiler.
- Language and formats: the Keycloak login theme ships English only in wave 2; locale files are loaded from the wave 48 label packs; dates in effective-date fields are stored as dates (no time) and rendered per user preference from wave 5.
- Time zone: effective dates are evaluated in `tenant.time_zone` (BR-2.5), not per legal entity; multi-time-zone tenants are an accepted limitation until wave 6 calendars.
- Accessibility: Keycloak login pages are themed to meet WCAG 2.1 AA (checked by the wave 5 axe suite).
- Audit and evidence: role grants, revokes, break-glass use and IdP configuration changes are events consumed by wave 5 audit; wave 48 SOC 2 CC6.1–CC6.3 evidence is generated from them.

### 0.2.21 Coverage closure

No `GAP` or `PARTIAL` row in coverage-ax.md §12.99 or coverage-sap.md §13.99 is assigned to wave 2 (both registers parsed on 2026-10-21). Inventory rows tagged `COVERED-2` (AX §12.30 users, user groups, security roles/duties/privileges, automatic role assignment rules, online users, system parameters, user log, security inquiries, SSRS security reports, AD import / claims users / identity providers, password/MFA policies; AX §12.39 external user roles and provisioning; AX §12.7 cost accounting ledger security; AX §12.26 mobile device user authentication; SAP §13.30 SU01/SU10, SUGR, PFCG, SU53/SU56/ST01/STAUTHTRACE, SUIM, SECPOL/RZ10, SU01 user types, SAML/OIDC/SNC, UCON, profile parameters, PFCG org levels, RSUSR* reports, PFUD, SU10 mass lock; SAP §13.2 OBA4 tolerance groups; SAP §13.12 OMET buyer authorizations and authorization groups) are met by FR-2.1–FR-2.11, 0.2.14 and 0.2.17.

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| none | — | — | Wave 2 has no register rows; see paragraph above for the `COVERED-2` confirmation. The automatic role-assignment rule (`SysSecRoleAssignmentRule`, query-based) is met by `scim.group_role_mapping` plus the wave 4 batch job that evaluates a saved filter over users; the SAP OBA4/OMET amount limits are met by `Permission.condition` |

---

## 0.3 Wave 3: Metadata Kernel (Entities, Fields, Extensions)

### 0.3.1 Scope and business context

In AX every business object is an AOT artifact: a Table with properties (`TableGroup`, `SaveDataPerCompany`, `PrimaryIndex`, `ClusteredIndex`, `TitleField1/2`, `CacheLookup`), Fields typed by Extended Data Types and Base Enums, Relations with `DeleteActions`, Indexes with `AlternateKey`, and table methods (`validateField`, `validateWrite`, `validateDelete`, `initValue`, `insert`, `update`) in X++; D365 adds data entities for OData and the extension model that replaced overlayering (SYS/GLS/FPK/SLN/ISV/VAR/CUS/USR layers). In SAP the equivalents are DDIC objects: tables (DD02L), fields (DD03L), domains (DD01L), data elements (DD04L), foreign keys (DD05S/DD08L), search helps (DD30L), append structures and `CI_*` customer includes, delivery classes and `SE11` activation, with BAPIs and CDS/OData exposing them. Wave 3 replaces both with a YAML entity definition language and a generator: declare an entity once and get the Postgres migration, Go model and repository, REST handlers, OpenAPI 3.1, TypeScript client and JSON Schema. Customers extend any entity with fields, enum values and declarative rules as metadata applied to a running environment. At wave end `Tenant`, `LegalEntity` and `User` from wave 2 are regenerated from metadata and the hand-written code is deleted. Out of scope: generated forms (wave 5), AsyncAPI topics (wave 4), scripted customer hooks (open question), views/maps/table collections (wave 46 analytics covers views).

#### Pain points solved (10x lens)

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| Adding one field to VendTable in D365: table extension, EDT, form extension, data entity extension, label, build, deployable package, LCS apply — a 2–5 day developer task with an environment outage | Schema, API, form and entity are separate artifacts kept in sync by hand | One `x_` field in an `ExtensionPackage`: migration, API, client, JSON Schema and (wave 5) form generated and applied with zero downtime | Field addition: 2–5 days + outage → < 60 s, no outage; artifacts touched by hand: 6 → 1 |
| SAP append structure on LFA1 needs SE11, activation (table conversion risk on large tables), CMOD/BAdI for validation, SE51 screen exit, and a transport through DEV→QAS→PRD | DDIC activation rewrites tables; UI exits are separate from DDIC | Additive `ADD COLUMN` with fast default, `CREATE INDEX CONCURRENTLY`, declarative validation in the same package | Lock time on a 100M-row table: minutes → < 2 s; transport cycle: 1–2 weeks → one PR |
| Integration partners (MES, PLM, WMS) must reverse-engineer OData entities or BAPI parameter lists; no machine-readable contract versioning | APIs are hand-exposed per entity (data entities, SEGW projects) | Every entity publishes OpenAPI 3.1 and JSON Schema with additive-change guarantees enforced in CI | Time for an external team to consume a new entity: weeks → the day it is published; breaking changes reaching partners: 0 |
| Silent type drift: AX `real` fields and SAP `CURR` with DECIMALS=2 round amounts differently in X++/ABAP arithmetic vs. the database | Type semantics defined in two places (EDT/domain and language runtime) | One type system (`decimal(p,s)`, `money` + currency, `half_even`) enforced at the API and column | Rounding discrepancies between API and storage: possible → 0 (property-tested) |
| Finding "where is this field used" requires cross-reference tools (AX cross-reference update takes hours; SAP SE11 where-used across packages) | Reflection is an offline build artifact | Registry `/v1/metadata/*` answers entity, field, relation and extension queries live | Where-used answer: hours (after a cross-reference build) → < 20 ms |

### 0.3.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Table definition | AOT Tables node, `TableGroup`, `SaveDataPerCompany`, `RecId`, `RecVersion`, `DataAreaId`, `Partition` | DD02L, delivery class, MANDT, SE11 | `EntityDefinition` YAML; system fields id, tenant_id, legal_entity_id, version, created_*, updated_* | full |
| Typed fields | Extended Data Types (StringSize, Label, HelpText), `SysDictField` | Domains (DD01L) + data elements (DD04L) | `TypeDefinition` (EDT) + `FieldDefinition` | full |
| Enumerations | Base Enums, `SysDictEnum`, extensible enums (D365) | Domain fixed values (DD07L), check tables | `EnumDefinition` / `EnumValue`, customer values ≥ 1000 | full |
| Relations / FK | Relations node, `ForeignKey`, `DeleteActions` None/Cascade/Restricted | DD05S/DD08L foreign keys, check tables, cardinality | `RelationDefinition` with on_delete restrict/cascade/set_null | full |
| Indexes | Indexes node, `AllowDuplicates`, `AlternateKey` | DD12L secondary indexes, unique flag | `IndexDefinition` unique/non-unique, partial predicate | full |
| Validation hooks | `validateField`, `validateWrite`, `validateDelete`, `initValue`, `modifiedField` | Table maintenance events, BAdIs, check tables | lifecycle hooks: initValue, validateField, validateWrite, validateDelete, afterWrite, afterDelete | full (declarative + registered code) |
| API exposure | Data entities (IsPublic, PublicCollectionName), OData, custom services | BAPIs, RFC, OData via SEGW / RAP, CDS views | generated REST (OpenAPI 3.1) per entity; GraphQL deferred | partial (no OData $batch) |
| Extensions | Extensions (`.extension`), Chain of Command, extensible enums | Append structures, CI_* includes, Z/Y namespace | `ExtensionPackage` (customer layer), `x_` field prefix | improved (zero-downtime apply) |
| Versioning / diff | Model store versions, compare tool | Version management (SE09 versions), transport diffs | `MetadataVersion` + `/diff` endpoint, snapshot golden tests | improved |
| Business rules / decision tables | Policy rules framework (`SysPolicy`, `SysPolicyRuleType`), audit policy rules, X++ conditions in code | BRF+ (`FDT_WORKBENCH`): applications, functions, rulesets, decision tables, expressions; Decision Service Management | `RuleSet` / `DecisionTable` / `DecisionRow` metadata, `POST /v1/rules/{code}/evaluate`, `rules.eval()` in hooks and workflow conditions | full |
| Usage and license measurement | License inquiries, named user license counts (`SysUserLicenseCount`), D365 "License usage" report | USMM / SLAW system measurement, ST03N user profile, SUIM users by logon date | `metadata.usage_snapshot` (nightly), Usage report, CSV export | full |

### 0.3.3 Functional requirements

- **FR-3.1 Entity definition language** — Must. The system shall accept entity definitions in YAML validated by a published JSON Schema (`metadata/schema/entity.v1.json`) with keys `name`, `domain`, `table_group` (main, group, transaction, worksheet_header, worksheet_line, parameter, reference), `per_legal_entity`, `title_fields[]`, `fields[]`, `enums[]`, `relations[]`, `indexes[]`, `hooks[]`, `audit`, `api`.
  - Notes: `name` is UpperCamelCase ≤ 48 chars; `domain` selects the owning Postgres database; one entity per file under `metadata/entities/<domain>/`.
- **FR-3.2 Type system** — Must. The system shall support field types string(n ≤ 4000), text, int32, int64, decimal(p ≤ 38, s ≤ 12), money (decimal(28,6) + currency reference), bool, date, datetime (timestamptz), time, uuid, enum, json, bytes, and named `TypeDefinition`s (EDT-equivalents) that bundle type, length, label, help, format and lookup entity.
  - Notes: `money` fields always pair with a `currency` char(3); `decimal` default scale 6 for quantities, 2 for displayed amounts, with rounding `half_even`.
- **FR-3.3 Field attributes** — Must. Each field shall declare `required`, `unique`, `default` (literal or `now()`, `current_user()`, `numseq(<code>)` from wave 5), `computed` (expression over sibling fields, evaluated server-side), `visibility` (always, advanced, hidden), `pii` (none, personal, sensitive), `audit` (bool, default inherits entity), `allow_edit`, `allow_edit_on_create`, `label`, `help`, `min`, `max`, `pattern`.
- **FR-3.4 System fields** — Must. Every generated table shall carry `id uuid`, `tenant_id uuid`, `legal_entity_id uuid` (when `per_legal_entity=true`), `version int` (RecVersion), `created_at`, `created_by`, `updated_at`, `updated_by`, and optional `deleted_at` for soft delete.
  - Notes: RLS policy generated per FR-2.4; `version` increments on every update and is required in `If-Match`.
- **FR-3.5 Migration generation** — Must. The generator shall emit Atlas migrations that are deterministic for a given definition version; additive changes (new nullable field, new enum value, new index) generate forward-only migrations; destructive changes require an explicit `breaking: true` flag and a new API major version.
  - Notes: `ADD COLUMN … DEFAULT` uses Postgres fast default; index creation uses `CONCURRENTLY`; migrations run as a pre-sync Argo hook job.
- **FR-3.6 Code generation** — Must. For each entity the generator shall emit a Go struct, repository (list with filter/sort/cursor pagination, get, create, update, patch, delete), HTTP handlers, OpenAPI 3.1 document, TypeScript client (fetch-based, typed), and JSON Schema for request validation.
  - Notes: list filter grammar `?filter=field op value` with ops eq, ne, gt, ge, lt, le, in, like, is_null; max page size 500, default 50; cursor is opaque base64.
- **FR-3.7 Lifecycle hooks** — Must. The system shall execute hooks in order initValue → validateField (per changed field) → validateWrite → write → afterWrite, and validateDelete → delete → afterDelete; declarative hooks are expressions returning `{ok, code, message, field}`; coded hooks are Go/TS functions registered by name in the owning service.
  - Notes: validation errors return 422 with `errors[]{field, code, message}`; a coded hook that panics rolls back the transaction and returns 500 with a trace id.
- **FR-3.8 Metadata registry** — Must. The system shall store every definition version, expose diff between versions, publish an immutable version on approval, and serve the current version to generators, the gateway and the shell with ETag caching.
  - Notes: registry DB is `metadata`; publishing emits `dev.passage.metadata.entity.published.v1`; registry read p95 ≤ 20 ms.
- **FR-3.9 Extension packages** — Must. Customers shall create an `ExtensionPackage` (layer customer or partner) that adds fields (prefixed `x_`), enum values (code ≥ 1000), indexes, declarative hooks and labels to any `sys` entity; applying a package generates and runs the migration, regenerates OpenAPI/JSON Schema and invalidates the shell form cache with zero downtime.
  - Notes: limits: 200 extension fields per entity, 50 enum values per enum per package; packages are versioned and reversible only while additive.
- **FR-3.10 Backward compatibility** — Must. CI shall run the previous published version's generated TypeScript client against the new server and fail on any breaking change (removed field, type change, required added, enum value removed); additive changes pass.
- **FR-3.11 Golden tests** — Must. Ten reference entities (Tenant, LegalEntity, User, Role, VendorStub, ProductStub, JournalLineStub, Address, Note, Attachment) shall have committed snapshots of migration, OpenAPI and client; CI fails on any byte-level drift.
- **FR-3.12 Performance** — Must. A generated entity API shall serve 1,000 rps of simple reads with p95 < 50 ms on the 3-node dev cluster and 200 rps of creates with p95 < 120 ms.
- **FR-3.13 Decision tables and rule sets** — Should. The system shall provide a `RuleSet` metadata object (same draft → review → published lifecycle, layers and extension packages as entities) containing one or more `DecisionTable`s with typed input columns (bound to a `TypeDefinition` or an entity field), condition cells (operators eq, ne, lt, le, gt, ge, between, in, like, any), typed result columns, a hit policy (`first`, `priority`, `all`, `collect_sum`), and per-row `effective_from`/`effective_to`; published tables shall be evaluable by `POST /v1/rules/{code}/evaluate`, from declarative hooks via `rules.eval('<code>', {inputs})` and from wave 4 workflow conditions, with an `explain` mode listing the rows that matched.
  - Notes: limits 10,000 rows and 20 columns per table, 50 tables per rule set; a published version is compiled to an in-memory decision tree per service (p95 ≤ 5 ms for a 1,000-row table); customers add or override rows through an `ExtensionPackage` (`layer=customer` rows evaluate before `sys` rows under `priority`); rows are imported and exported as CSV through wave 5 import/export; later consumers are tax determination (wave 13), output determination (wave 15), credit rules (wave 30), substitution and delegation rules (wave 4), audit policies (wave 48) — none of them may ship a private rules engine.
- **FR-3.14 Usage metering** — Should. The system shall compute, nightly per tenant, a `UsageSnapshot` with active named users (distinct humans with a login or token use in the last 30 and 90 days) per legal entity and per role, principals by kind (human, service, agent), API requests per entity per day (from gateway metrics), and row counts per entity; expose it as the Usage report (0.3.14) with CSV export; retain snapshots 3 years.
  - Notes: a user counted in several legal entities counts once at tenant level; service and agent principals are reported but never counted as named users (PRD §12 Q1: no per-seat charge for shop-floor, portal or agent users); the snapshot is the input to the licensing decision and to capacity planning, not an enforcement point — nothing is blocked by a count.

### 0.3.4 Data model

**EntityDefinition** (`metadata.entity`)

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| tenant_id | uuid | yes | `sys` tenant for shipped entities |
| name | varchar(48) | yes | unique with domain and version |
| domain | varchar(32) | yes | Postgres database / service |
| version | int | yes | monotonically increasing |
| status | enum | yes | draft, review, published, deprecated |
| table_group | enum | yes | FR-3.1 values |
| per_legal_entity | bool | yes | default true for main/transaction |
| title_fields | text[] | no | ≤ 2 |
| audit | bool | yes | default true for main/transaction |
| api_major | int | yes | default 1 |
| spec | jsonb | yes | canonical YAML as JSON |
| spec_sha256 | char(64) | yes | drift detection |
| published_at, published_by | timestamptz, uuid | no | |

**FieldDefinition** (`metadata.field`)

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| entity_id | uuid | yes | FK entity version |
| name | varchar(48) | yes | snake_case; `x_` reserved for extensions |
| type | enum | yes | FR-3.2 |
| type_def_id | uuid | no | FK TypeDefinition |
| enum_id | uuid | no | FK EnumDefinition when type=enum |
| length, precision, scale | int | no | per type |
| required, unique | bool | yes | |
| default_expr | text | no | |
| computed_expr | text | no | |
| visibility | enum | yes | always, advanced, hidden |
| pii | enum | yes | none, personal, sensitive |
| audit | bool | yes | |
| layer | enum | yes | sys, partner, customer |
| extension_package_id | uuid | no | FK when layer ≠ sys |
| ordinal | int | yes | form/column order default |

**ExtensionPackage** (`metadata.extension_package`)

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| tenant_id | uuid | yes | |
| code | varchar(32) | yes | unique per tenant |
| layer | enum | yes | partner, customer |
| version | int | yes | |
| status | enum | yes | draft, validated, applied, failed, retired |
| targets | jsonb | yes | entities, fields, enum values, hooks, labels |
| applied_at, applied_by | timestamptz, uuid | no | |
| migration_id | text | no | Atlas migration name |

**EnumDefinition / EnumValue / TypeDefinition / RelationDefinition / IndexDefinition** follow the same shape: id, entity or enum FK, name/code, layer, ordinal; EnumValue has `code int` (sys < 1000, extensions ≥ 1000) and `label`; RelationDefinition has `from_field`, `to_entity`, `to_field`, `on_delete`; IndexDefinition has `fields[]`, `unique`, `where`.

**RuleSet / DecisionTable / DecisionRow** (`metadata.rule_set`, `metadata.decision_table`, `metadata.decision_row`)

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK (each table) |
| tenant_id | uuid | yes | `sys` tenant for shipped rule sets |
| code | varchar(48) | yes (RuleSet) | unique per tenant and version; e.g. `TaxGroupDetermination` |
| version, status | int, enum | yes (RuleSet) | draft, review, published, deprecated (BR-3.1 semantics) |
| rule_set_id, name | uuid, varchar(48) | yes (DecisionTable) | name unique within the set |
| hit_policy | enum | yes (DecisionTable) | first, priority, all, collect_sum |
| input_columns, result_columns | jsonb | yes (DecisionTable) | `[{name, type_def_id | field_ref, operators_allowed}]`, `[{name, type}]`; ≤ 20 columns in total |
| table_id, ordinal, priority | uuid, int, int | yes (DecisionRow) | ordinal = evaluation order under `first`; priority used under `priority` |
| conditions | jsonb | yes (DecisionRow) | `{column: {op, value | [low, high] | [values]}}`; a missing column means `any` |
| results | jsonb | yes (DecisionRow) | typed result values |
| effective_from, effective_to | date | no (DecisionRow) | evaluated in the tenant time zone |
| layer, extension_package_id | enum, uuid | yes / no (DecisionRow) | sys, partner, customer |

**UsageSnapshot** (`metadata.usage_snapshot`)

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| tenant_id | uuid | yes | |
| snapshot_date | date | yes | unique with tenant_id |
| active_users_30d, active_users_90d | int | yes | distinct humans |
| by_legal_entity | jsonb | yes | `{legal_entity_id: {active_30d, active_90d}}` |
| by_role | jsonb | yes | `{role_code: active_30d}` |
| principals_by_kind | jsonb | yes | `{human, service, agent}` |
| api_requests_by_entity | jsonb | yes | `{entity: count}` for the day |
| row_counts | jsonb | yes | `{entity: rows}` from `pg_class.reltuples` (estimate) |
| computed_at | timestamptz | yes | |

Invariants: a published version is immutable; field names unique per entity across layers; an extension cannot change a `sys` field's type or required flag; every `enum` field references an enum; `title_fields` must be string type. Indexing: `entity(tenant_id, domain, name, version)` unique; `field(entity_id, ordinal)`. Partitioning: none (metadata volume < 1M rows).

### 0.3.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| GET | /v1/metadata/entities?domain=&status= | List entity definitions | n/a | any authenticated |
| GET | /v1/metadata/entities/{name} | Current published version (ETag) | n/a | any |
| PUT | /v1/metadata/entities/{name}/versions/{v} | Create/replace a draft version | by version | Maintain entity metadata |
| POST | /v1/metadata/entities/{name}/versions/{v}/publish | Publish | version | Maintain entity metadata |
| GET | /v1/metadata/entities/{name}/diff?from=&to= | Structured diff | n/a | any |
| POST | /v1/metadata/compat-check | Classify a diff as additive/breaking | n/a | Maintain entity metadata |
| GET/POST | /v1/metadata/extensions | List / create packages | POST: code+version | Apply extensions |
| POST | /v1/metadata/extensions/{id}/validate, /apply, /retire | Lifecycle | package version | Apply extensions |
| GET | /v1/metadata/openapi/{entity} | Generated OpenAPI 3.1 | n/a | any |
| GET | /v1/metadata/types, /v1/metadata/enums | EDT and enum catalogs | n/a | any |
| GET/POST/PATCH/DELETE | /v1/<domain>/<entities>/{id} | Generated entity API | POST: Idempotency-Key; PATCH: If-Match | per Permission(entity, action) |
| GET/PUT/POST | /v1/rules/sets, /{code}/versions/{v}, /{code}/versions/{v}/publish | Maintain and publish rule sets | by version | Maintain business rules |
| POST | /v1/rules/{code}/evaluate?table=&explain=true | Evaluate a published table; `explain` returns matched rows | n/a (pure function) | any authenticated (results filtered by the calling service) |
| POST | /v1/rules/{code}/versions/{v}/rows:import | CSV row import (wave 5 import framework) | file sha256 | Maintain business rules |
| GET | /v1/metadata/usage?from=&to=&legal_entity_id= | Usage snapshots | n/a | View usage reports |

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| dev.passage.rules.ruleset.published.v1 | rule set publish | code, version, tables[], row_count | services holding compiled trees (reload), audit |
| dev.passage.metadata.usage.snapshot.v1 | nightly snapshot | tenant_id, snapshot_date, active_users_30d, active_users_90d | analytics (wave 46), licensing report (PRD §12 Q1) |
| dev.passage.metadata.entity.published.v1 | publish | entity, domain, version, api_major, diff_summary{added, changed, removed} | generator pipeline, events (wave 4), shell |
| dev.passage.metadata.entity.deprecated.v1 | deprecate | entity, version, sunset_date | shell, API docs |
| dev.passage.metadata.extension.applied.v1 | apply success | package_id, code, version, entities[], fields_added, enum_values_added, migration_id | audit, shell, generator |
| dev.passage.metadata.extension.failed.v1 | apply failure | package_id, stage, error | alerting |
| dev.passage.metadata.schema.migrated.v1 | Atlas job completes | domain, migration_id, duration_ms | audit |

### 0.3.6 User experience

| Screen | Primary actions | Key fields/columns | Duty required |
|---|---|---|---|
| Entity catalog | Browse, open, view diff, publish | name, domain, version, status, field count, extensions | Maintain entity metadata |
| Entity designer (YAML + form view) | Add field/enum/relation/index/hook, validate, save draft | name, type, required, default, pii, visibility | Maintain entity metadata |
| Extension packages | Create, validate, apply, retire, view migration log | code, layer, version, status, targets | Apply extensions |
| Type and enum catalogs | Create EDT, add enum values | code, base type, length, label | Maintain entity metadata |
| Rule set designer | Add table, define columns, edit rows in a grid with typed cells, test panel (inputs → result + matched rows), publish | code, version, status, table, hit policy, rows, effective dates | Maintain business rules |
| Usage report | Pick date range and legal entity, export CSV | active users 30/90 d, by role, by legal entity, principals by kind, API requests by entity | View usage reports |

Rules: the designer validates against the JSON Schema on every keystroke (debounced 300 ms) and against the registry on save; apply shows a dry-run migration SQL with an estimated lock time and refuses if > 2 s; keyboard: `Ctrl+S` save draft, `Ctrl+Shift+P` publish.

### 0.3.7 Business rules and edge cases

- **BR-3.1** Entity version status machine: draft → review → published → deprecated; published versions are immutable; a deprecated version serves for 180 days after `sunset_date` announcement.
- **BR-3.2** Breaking vs additive: removing a field, narrowing a type, adding `required` without default, removing an enum value, or renaming are breaking; everything else is additive. Breaking requires `api_major + 1` and a parallel `/v{n+1}` route for ≥ 1 release.
- **BR-3.3** Enum value codes are permanent; retiring a value sets `retired=true` and the value remains readable but not assignable.
- **BR-3.4** Extension apply is transactional per domain database: migration, registry update and cache invalidation succeed together or the package goes to `failed` with the SQL error.
- **BR-3.5** Concurrent publishes of the same entity: second publish gets 409 and must rebase on the new version.
- **BR-3.6** Decimal rounding at write uses the field's scale with `half_even`; values exceeding precision return 422 `VALUE_OUT_OF_RANGE`.
- **BR-3.7** Soft-deleted rows (`deleted_at` set) are excluded from list and get by default; `?include_deleted=true` requires the `delete` action.
- **BR-3.8** `on_delete: restrict` returns 409 `REFERENCED_BY` with the referencing entity and count; cascade is limited to 10,000 child rows per request.
- **BR-3.9** PII `sensitive` fields are masked (`***`) in responses unless the Permission grants the field explicitly; they are never included in list endpoints.
- **BR-3.10** Decision table hit policies: `first` returns the lowest-ordinal matching row; `priority` returns the highest `priority` (ties broken by layer customer > partner > sys, then ordinal); `all` returns every match in ordinal order; `collect_sum` sums numeric result columns across matches. A table with no matching row returns `{matched: false}` and the caller decides (hooks treat it as validation code `RULE_NO_MATCH`). `between` is inclusive on both ends; string comparison is case-insensitive; dates and `effective_*` are evaluated in the tenant time zone.
- **BR-3.11** A named user is "active" on a day when a human principal obtained a token or used one (gateway request) on that day; counts are distinct per tenant, never summed across legal entities; the snapshot for day D is computed at 01:30 tenant time on D+1 and is never recomputed except by an explicit rebuild with the reason recorded.

### 0.3.8 Acceptance criteria

- **AC-3.1** Given the 10 reference entities, When the generator runs 3 times, Then the migration, OpenAPI and client snapshots are byte-identical and match the committed golden files. (FR-3.5, FR-3.6, FR-3.11)
- **AC-3.2** Given a customer package adding `x_risk_rating` (enum) and enum value `PreferredPlus (1001)` to VendorStub, When applied in dev, Then the API accepts the field within 60 s, no request fails during apply, and the wave-5 form shows it. (FR-3.9)
- **AC-3.3** Given a published v1 client, When the server publishes an additive v2, Then the v1 contract suite passes; Given a breaking change without `breaking: true`, Then CI fails naming the change. (FR-3.10, BR-3.2)
- **AC-3.4** Given 1,000 rps of `GET /v1/identity/users/{id}` for 5 minutes on the dev cluster, When measured at the gateway, Then p95 < 50 ms and error rate < 0.1%. (FR-3.12)
- **AC-3.5** Given a `validateWrite` declarative hook `amount > 0`, When a client posts `amount = -1`, Then 422 with `field=amount`. (FR-3.7)
- **AC-3.6** Given Tenant, LegalEntity and User regenerated from metadata, When the wave-2 test suites run, Then they pass unchanged and the hand-written handlers are removed from the repo. (FR-3.6)
- **AC-3.7** Given a diff request between versions 3 and 5, When called, Then the response lists each added/changed/removed field with classification additive/breaking. (FR-3.8)
- **AC-3.8** Given a field with `pii=sensitive`, When listed by a user without the field permission, Then the field is absent; when fetched by id, Then masked. (FR-3.3, BR-3.9)
- **AC-3.9** Given a published rule set `VendorRiskBand` with a 1,000-row `priority` table (inputs country, spend_band, payment_terms), When evaluated 10,000 times with random inputs from the fixture, Then every result equals the reference evaluator's result, `explain` lists the matched rows, and p95 ≤ 5 ms. (FR-3.13, BR-3.10)
- **AC-3.10** Given a customer extension package adding 3 rows to `VendorRiskBand`, When applied, Then a hook calling `rules.eval` returns the customer row for the overlapping inputs and the sys row otherwise. (FR-3.9, FR-3.13)
- **AC-3.11** Given 50 users of whom 30 obtained tokens in the last 30 days (2 of them in two legal entities) plus 5 service accounts, When the nightly snapshot runs, Then `active_users_30d = 30`, the legal-entity breakdown sums to 32, and `principals_by_kind.service = 5`. (FR-3.14, BR-3.11)

### 0.3.9 Test plan and fixtures

Fixtures: `metadata/entities/_reference/*.yaml` (10 entities), `fixtures/metadata/golden/` (snapshots), `fixtures/metadata/ext-vendor-risk.yaml`, `fixtures/metadata/breaking-change-cases.yaml` (12 breaking, 12 additive cases), `fixtures/load/users-100k.sql` (100k users for the read benchmark).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Golden generation | Snapshot | generator | zero drift |
| Schema validation | Unit | JSON Schema | 100% of invalid fixtures rejected with path |
| Compat classifier | Table-driven | compat-check | 24/24 |
| Extension apply | E2E | registry + Atlas + API | applied, 0 failed requests during apply (k6 background at 100 rps) |
| Hook ordering | Property | repository | hooks fire in FR-3.7 order for random mutations |
| Read benchmark | Performance (k6) | generated API | p95 < 50 ms @ 1,000 rps |
| RLS on generated tables | Contract | every generated table | policy present and effective |

Reconciliation: registry field count per published entity equals `information_schema.columns` for the generated table minus system fields; OpenAPI property count equals field count minus hidden fields.

### 0.3.10 Deployment and operations

Charts: `metadata-registry` (Go, CNPG `metadata` database), `passage-gen` runs in CI and as an in-cluster Job for extension apply. Config: `REGISTRY_URL`, `GEN_OUTPUT_DIR`, `ATLAS_DEV_URL` (scratch database for migration planning). Migrations: generated; applied by Argo pre-sync hook with `atlas migrate apply --tx-mode all`. Feature flags: `metadata.extensions.enabled` per tenant. Scheduled jobs: nightly drift check (registry vs live schema). SLOs: registry availability 99.9%, read p95 ≤ 20 ms, extension apply ≤ 60 s. Alerts: `SchemaDrift`, `ExtensionApplyFailed`, `MigrationJobFailed`. Runbook: failed migration (mark package failed, run `atlas migrate down` only in dev), registry restore from CNPG backup. Rollback: registry and generated services roll back together by digest; because migrations are additive the previous digest runs against the newer schema.

### 0.3.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| AX AOT Tables (via `SysDictTable` export or AxModel XML) with `TableGroup`, `SaveDataPerCompany` | EntityDefinition | only customer-layer (CUS/VAR/USR) tables and fields are candidates; SYS tables map to Passage entities by crosswalk |
| AX EDTs, Base Enums | TypeDefinition, EnumDefinition | enum values keep their integer codes where < 1000 |
| AX table extensions (`.extension` XML) | ExtensionPackage | field additions become `x_` fields |
| SAP DD02L/DD03L for Z*/Y* tables, append structures, CI_* includes | EntityDefinition / ExtensionPackage | DD04L data elements → TypeDefinition; DD07L fixed values → EnumValue |

Transformation is a definition import (`passage-gen import-ax`, `import-sap`) producing draft YAML for review; nothing is auto-published. Not migrated: X++ table methods and ABAP user exits (code), SAP delivery-class semantics, AX `CacheLookup` and `Partition`.

### 0.3.12 Dependencies, risks, open questions

Depends on: wave 2 (tenant_id/RLS, duties `Maintain entity metadata`, `Apply extensions`; the three entities to regenerate).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Generator becomes a bottleneck for every later wave | High | High | Generator is itself golden-tested; business waves add entities by YAML only; escape hatch for hand-written endpoints alongside generated ones |
| Expression language for hooks grows into a scripting language | Medium | Medium | CEL-style expression subset frozen at wave 3; scripted hooks decided in OQ 2 |
| Large-table `ADD COLUMN` with non-constant default locks | Low | Medium | Dry-run lock estimate; non-constant defaults applied by backfill job |

Open questions: (1) GraphQL alongside REST — default no until wave 35; decide by wave 10. (2) Customer scripted hooks (sandboxed TS or WASM) — default declarative only through wave 30; decide by wave 20. (3) Soft delete default for main tables — default on for master data groups, off for transactions; decide by wave 6.

### 0.3.13 Parameters and setup

Registry parameters are rows of `metadata.parameter` edited on Admin workspace → Metadata → Parameters (the AX "System parameters" / SAP SE11 technical-settings equivalent for the kernel); entity-level attributes are part of the YAML and are listed in FR-3.1–FR-3.4.

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| `metadata.extensions.enabled` | tenant (feature flag) | bool | true | true/false | allows `ExtensionPackage` apply in the tenant | Feature management (D365) | SCC4 client role "customizing allowed", SE06 system change option |
| `metadata.extension.max_fields_per_entity` | tenant | int | 200 | 1–500 | FR-3.9 limit | none | append structure field limit (practical) |
| `metadata.extension.max_enum_values_per_package` | tenant | int | 50 | 1–200 | FR-3.9 limit | none | DD07L fixed values |
| `metadata.extension.max_lock_seconds` | environment | decimal | 2 | 0.5–10 | apply refuses when the dry-run lock estimate exceeds this | none | SE14 table conversion (no guard) |
| `metadata.decimal.default_scale_qty`, `metadata.decimal.default_scale_amount` | tenant | int, int | 6, 2 | 0–12 | FR-3.2 defaults | EDT `NoOfDecimals` | Domain DECIMALS (CURR/QUAN) |
| `metadata.rounding_mode` | tenant | enum | half_even | half_even, half_away_from_zero | write-time rounding (BR-3.6); wave 11 ledger may override per legal entity | `round()` X++ semantics | `ROUND` ABAP semantics per currency (TCURX) |
| `metadata.api.page_size_default`, `metadata.api.page_size_max` | environment | int, int | 50, 500 | 10–200, 100–2,000 | FR-3.6 list paging | none (`maxbuffersize`) | OData `$top` limits (/IWFND/MAINT_SERVICE) |
| `metadata.api.deprecated_version_days` | tenant | int | 180 | 30–365 | BR-3.1 sunset window | none | none |
| `metadata.soft_delete_default` | tenant | enum per table group | on for main/group, off for transaction (OQ 3) | on/off per table_group | default of `deleted_at` generation | none | deletion flag (LOEVM) convention |
| `metadata.pii.sensitive_mask` | tenant | string | `***` | ≤ 8 chars | BR-3.9 mask text | none | none (UI masking via SU24 field auth) |
| `metadata.registry.cache_ttl_seconds` | environment | int | 60 | 10–600 | ETag cache in gateway/shell | AOS metadata cache | SE11 buffer settings (technical settings → buffering) |
| `metadata.golden.entities[]` | repository | list | 10 reference entities (FR-3.11) | entity names | golden snapshot set | none | none |
| `metadata.hooks.expression_timeout_ms` | environment | int | 50 | 10–500 | declarative hook evaluation guard | none | none |
| `metadata.hooks.coded_timeout_ms` | environment | int | 2,000 | 100–10,000 | coded hook guard; exceeded → 500 with trace id | none | none |
| `rules.table.max_rows`, `rules.table.max_columns` | tenant | int, int | 10,000, 20 | ≤ 50,000, ≤ 30 | FR-3.13 limits | none | BRF+ decision table limits (none enforced) |
| `rules.compile_on_publish` | environment | bool | true | true | compiled tree built on `ruleset.published` | none | BRF+ generation (FDT_GENERATE) |
| `usage.snapshot_time` | tenant | HH:MM tenant time | 01:30 | any | FR-3.14 / BR-3.11 | License count refresh batch | USMM schedule |
| `usage.retention_years` | tenant | int | 3 | 1–10 | snapshot retention | none | none |
| `usage.active_window_days[]` | tenant | int list | 30, 90 | 7–365 | windows reported | "Named user license count" window (D365: 30 days) | ST03N month aggregates |

### 0.3.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Entity catalog | list inquiry | domain, status, layer, has extensions | name, domain, version, status, table group, field count, extension count, published at | `metadata.entity` | AOT Tables node, Data entities list (D365) | SE11 table list, SE16 DD02L |
| Field list | drill-down | entity, layer, pii, audit | field, type, length/precision, required, default, pii, audit, visibility, layer | `metadata.field` | Table → Fields (AOT), SysDictField | SE11 → Fields, DD03L |
| Where-used | drill-down | field, type definition, enum | referencing entities, relations, indexes, hooks, rule sets, saved views (wave 5) | registry cross-reference | Cross-reference (xRef) | SE11 → Where-used list (RSWBO052) |
| Entity diff | drill-down | entity, from version, to version | added / changed / removed items with classification additive/breaking | `/diff` endpoint | Model store compare | SE09 version management, SE39 split-screen |
| Extension package log | list inquiry | status, entity, date | package, layer, version, status, migration id, applied by/at, error | `metadata.extension_package` | Models form (installed models) | SE03 object directory, transport log |
| Schema drift report | operational report (nightly) | domain | entity, column, registry type, live type, drift kind | registry vs. `information_schema` | none | SE14 consistency check (database utility) |
| Type and enum catalogs | list inquiry | base type, layer | code, base type, length, label, usages | `metadata.type_definition`, `metadata.enum_definition` | EDTs, Base Enums (AOT) | SE11 domains / data elements, DD07L |
| API catalog | list inquiry | domain, api_major, status | entity, OpenAPI link, AsyncAPI link (wave 4), deprecated until | `/v1/metadata/openapi/{entity}` | Data entities (IsPublic) | /IWFND/MAINT_SERVICE (OData services), SEGW projects |
| Generated-API performance | KPI | entity, period | rps, p50/p95/p99, error rate | gateway metrics (Prometheus) | SQL insights (LCS) | ST03N transaction profile, STAD |
| Rule set catalog and row inquiry | list inquiry / drill-down | rule set, version, table, effective date, layer | code, version, status, tables, rows; per row: conditions, results, effective dates, layer | `metadata.rule_set`, `metadata.decision_row` | Policy rules list | FDT_WORKBENCH object list, decision table display |
| Rule evaluation trace | drill-down | rule set, inputs | matched rows, hit policy applied, result, duration | `evaluate?explain=true` | none | BRF+ simulation (FDT_WORKBENCH → Simulate), trace (FDT_TRACE) |
| Usage report | operational report | date range, legal entity | active users 30/90 d, by role, by legal entity, principals by kind, API requests by entity, row counts | `metadata.usage_snapshot` | License inquiries → Named user license counts, License usage by role; SSRS License report | USMM / SLAW, ST03N user profile, RSUSR200 |

### 0.3.15 Printed and electronic documents

No printed documents. Electronic documents:

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| OpenAPI 3.1 document per entity | publish | info (entity, version, api_major), paths, components (schemas from JSON Schema), security scheme | JSON, YAML | version = entity version | retained for every published version (immutable) |
| JSON Schema per entity | publish | `$id`, properties with types/formats, required, enum values | JSON Schema 2020-12 | same | same |
| TypeScript client package | publish | typed fetch client, request/response types | npm tarball `@passage/client-<domain>@<version>` | semver from version | retained in registry 7 years (same as images) |
| Migration file | publish / extension apply | Atlas SQL with header (entity, version, package) | `.sql` under `migrations/<domain>/` | timestamped Atlas name | permanent in Git |
| Extension package manifest | package validated | code, layer, version, targets, dry-run SQL, lock estimate | YAML (`passage-gen export`) | package code + version | permanent in `metadata.extension_package` |
| Rule set export | administrator action | tables with columns, rows (conditions, results, effective dates, layer) | CSV per table, YAML for the whole set | rule set code + version | retained with the version |
| Usage report export | administrator action | snapshot rows | CSV, JSON | none | retained by requester; snapshots 3 years |

### 0.3.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| Schema drift check | nightly 01:00 UTC | domain | compares registry to `information_schema`; report rows keyed by (date, domain) | < 1 min per domain | report with drift rows; no automatic change | `SchemaDrift` (ticket) |
| Extension apply job | on demand (`/apply`) | package | transactional per domain (BR-3.4); package status is the checkpoint | ≤ 60 s | package → `failed` with SQL error; rerun after fix | `ExtensionApplyFailed` (notify) |
| Non-constant default backfill | after an apply that needs it | entity, id range (10 k rows) | checkpoint per range in `metadata.backfill` | 100 k rows/min | resume from checkpoint (wave 4 framework after wave 4; CronJob loop before) | `BackfillStalled` (> 30 min no progress) |
| Deprecated version sunset | daily 02:00 UTC | entity | removes `/v{n}` routes whose sunset passed; idempotent | < 10 s | none | none |
| Golden snapshot verification | on every CI run and nightly | entity | deterministic | < 2 min | CI failure | CI status |
| Rule set compile | on `ruleset.published` | rule set | compiled tree keyed by (code, version) | < 5 s for 10 k rows | publish rolled back with error | `RuleCompileFailed` (notify) |
| Usage snapshot | daily 01:30 tenant time | tenant | one row per (tenant, date); rebuild only with explicit reason (BR-3.11) | < 30 s per tenant | retry 3×; missing day flagged in the report | `UsageSnapshotMissing` (ticket) |
| Usage snapshot retention | monthly | tenant | deletes rows older than `usage.retention_years` | < 10 s | none | none |

### 0.3.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles that include it |
|---|---|---|---|
| Maintain entity metadata | `PUT /v1/metadata/entities/{name}/versions/{v}`, `/publish`, `/compat-check`, Entity designer, Type and enum catalogs | with Apply extensions in production tenants (designer of a sys change should not also apply customer packages without review) | Platform engineer, Solution architect |
| View entity metadata | `GET /v1/metadata/*`, Entity catalog, Field list, Where-used, Entity diff, API catalog | none | all authenticated users (read of the published version is `any`), Auditor |
| Apply extensions | `POST /v1/metadata/extensions`, `/validate`, `/apply`, `/retire`, Extension packages screen | with Maintain entity metadata; with Approve workflow for the extended entity (wave 4) | Customer administrator, Partner consultant |
| Maintain business rules | `/v1/rules/sets`, versions, publish, `rows:import`, Rule set designer | with the business duties that consume a rule set (for example Maintain tax setup, wave 13): a rule author should not also post the documents the rule governs; enforced by wave 48 SoD rule `RULE_AUTHOR_VS_POSTER` | Solution architect, Business administrator |
| Evaluate business rules | `POST /v1/rules/{code}/evaluate` | none | all services (internal), any authenticated user for `explain` on rules they may read |
| View usage reports | `GET /v1/metadata/usage`, Usage report | none | Tenant owner, Auditor, Platform operator |
| Run metadata jobs | trigger drift check, backfill, snapshot rebuild | none | Platform engineer |

### 0.3.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| MDK-0301 | error | Entity definition invalid at `{json_path}`: {schema error} | JSON Schema validation | fix the YAML | FR-3.1 |
| MDK-0302 | error | Breaking change `{change}` on {entity} requires `breaking: true` and api_major {n+1} | compat classifier | mark breaking and bump, or make the change additive | FR-3.5, BR-3.2 |
| MDK-0303 | error | Version {v} of {entity} is published and immutable | PUT on a published version | create a new draft version | BR-3.1 |
| MDK-0304 | error | {entity} version {v} was published by {user} after your draft; rebase | concurrent publish (409) | reload and reapply | BR-3.5 |
| MDK-0305 | error | Extension field names must start with `x_` | naming rule | rename | FR-3.9 |
| MDK-0306 | error | Enum value code {code} must be ≥ 1000 for extensions | layer rule | choose a code ≥ 1000 | FR-3.9 |
| MDK-0307 | error | Extension cannot change type or required flag of sys field {field} | invariant | add a new field instead | 0.3.4 invariants |
| MDK-0308 | error | Apply refused: estimated lock {s} s exceeds {max} s on {table} ({rows} rows) | dry-run lock estimate | schedule a backfill or split the change | 0.3.6 rules |
| MDK-0309 | error | Package {code} v{v} failed at {stage}: {sql error}; nothing was applied | transactional apply | fix and rerun | BR-3.4 |
| MDK-0310 | error | Value for {field} exceeds precision ({p},{s}) | VALUE_OUT_OF_RANGE (422) | correct the value | BR-3.6 |
| MDK-0311 | error | Cannot delete {entity} {id}: referenced by {n} {child_entity} rows | REFERENCED_BY (409) | delete or reassign children | BR-3.8 |
| MDK-0312 | error | Cascade delete limit of 10,000 child rows exceeded | BR-3.8 limit | delete in batches (wave 4 job) | BR-3.8 |
| MDK-0313 | error | Validation failed: {message} (field {field}) | declarative/coded hook returned ok=false | correct the field | FR-3.7 |
| MDK-0314 | error | Internal error in hook {name}; trace {trace_id} | coded hook panic / timeout | report with trace id | FR-3.7 |
| MDK-0315 | warning | Record changed by {user} at {time}; your version {v} is stale | If-Match mismatch (409) | reload | FR-3.4 |
| MDK-0316 | error | Decision table {table} has {n} rows; limit {max} | FR-3.13 limit | split the table or raise `rules.table.max_rows` | FR-3.13 |
| MDK-0317 | warning | No row matched in {table} for the given inputs | `{matched:false}` | add a catch-all row or handle `RULE_NO_MATCH` | BR-3.10 |
| MDK-0318 | error | Row {ordinal}: condition on column {column} uses operator {op} not allowed for type {type} | typed cell validation | choose an allowed operator | FR-3.13 |
| MDK-0319 | error | Rule set {code} v{v} is referenced by hook {hook} on {entity}; deprecate instead of retiring | reference check | deprecate with sunset date | FR-3.13 |
| MDK-0320 | info | Usage snapshot for {date} rebuilt by {user}: {reason} | explicit rebuild | none | BR-3.11 |

### 0.3.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| Registry read (current version, ETag) | 5,000/min at shell cold start; 200/min steady | 5 ms / 20 ms / 40 ms | 500 rps | `metadata.*` < 1 M rows total (< 1 GB) | `entity(tenant_id, domain, name, version)` unique; ETag served from cache | cache serves stale ≤ 60 s; registry HPA 2 → 4 |
| Generated entity read (`GET /{id}`) | 1,000 rps on dev cluster (FR-3.12) | 10 ms / 50 ms / 90 ms | 1,000 rps per 3 pods | per entity: rows × avg 1 KB | PK + declared indexes; `(tenant_id, …)` leading column everywhere | pool exhaustion → 503 with `Retry-After`; p95 may reach 150 ms |
| Generated entity create | 200 rps | 30 ms / 120 ms / 200 ms | 200 rps | same | hooks inline; outbox row (wave 4) | same |
| List with filter/sort, page 50 | 300 rps | 20 ms / 80 ms / 150 ms | 300 rps | n/a | cursor pagination on (sort key, id) | large offsets refused (cursor only) |
| Publish (generate + migrate) | 20/day | 10 s / 30 s / 60 s | 2 concurrent per domain | migrations ≈ 2 MB/year | one Atlas job per domain | queued; CI concurrency limit |
| Extension apply (100 M-row table) | 5/month | 15 s / 45 s / 60 s | 1 at a time per domain | n/a | `ADD COLUMN` fast default; `CREATE INDEX CONCURRENTLY` | refused above lock estimate (MDK-0308) |
| Rule evaluation (1,000-row table) | 2,000/s across services at peak | 0.5 ms / 5 ms / 10 ms | 5,000/s per service instance | rule tables ≤ 50 MB per tenant | compiled tree in memory per (code, version) | CPU-bound; no errors |
| Usage snapshot | 1/day/tenant | 5 s / 30 s / 60 s | n/a | 365 rows × ≈ 20 KB per tenant per year | `(tenant_id, snapshot_date)` unique | runs later; `UsageSnapshotMissing` after 24 h |

### 0.3.20 Localization and compliance hooks

- Labels and help text: `label` and `help` on entities, fields, enum values and rule columns are stored as keys in the registry; English is the only language in wave 3. Wave 48 label packs supply translations per country/language and the shell (wave 5) resolves them per user locale; the extension point is `metadata.label` keyed by (object, locale).
- Formats: dates, decimals and money are stored canonically (ISO 8601, `decimal`, `money` + ISO 4217) and formatted in the shell per user preference (wave 5); country packs never change storage types. Currency minor-unit scale comes from the wave 11 currency table; until then `money` uses scale 6 for storage and 2 for display.
- PII classification (`pii=personal|sensitive`) is the hook used by wave 48 privacy tooling (GDPR/CCPA erasure, data-subject export) and by wave 5 audit masking; country packs may raise a field's classification but never lower it.
- Statutory fields: country packs add fields through `ExtensionPackage` with `layer=partner` (for example Brazil `x_cnpj`, India `x_gstin`) — the same mechanism customers use; validation patterns go in `pattern` and in rule sets (FR-3.13).
- Decision tables are the extension point for country-dependent determinations (tax groups, document types, numbering formats): a country pack ships `sys`-layer rows with a `country` input column; customers override with `customer`-layer rows.
- Retention: metadata versions are never deleted (audit trail of schema), which satisfies SOX/ITGC change-management evidence (wave 48) together with the Git history of `metadata/entities`.
- Usage metering stores no personal data beyond counts; per-user activity lives in wave 2 login events and wave 5 audit.

### 0.3.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX §12.30.2 System administration → Inquiries & Reports: License inquiries (Named user license counts, License usage by role) (register row 265) | PARTIAL-3 | ADDED as FR-3.14 | Usage report in 0.3.14; `UsageSnapshot` entity; named users distinct per tenant (BR-3.11); counts inform PRD §12 Q1, never block |
| AX §12.30.2 System administration → SSRS: License report / Named user counts (register row 266) | PARTIAL-3 | ADDED as FR-3.14 | CSV/JSON export in 0.3.15; PDF layout arrives with the wave 15 report designer |
| AX §12.30.3 System administration → Periodic: Licensing → Named user license count refresh (register row 268) | PARTIAL-3 | ADDED as FR-3.14 | nightly Usage snapshot job (0.3.16) at `usage.snapshot_time`; rebuild with reason (MDK-0320) |
| SAP §13.31 Cross-Application → BRF+ (BRFPLUS / FDT_WORKBENCH) / Decision Service Management: business rules framework (register row 348) | PARTIAL-3 | ADDED as FR-3.13 | `RuleSet` / `DecisionTable` / `DecisionRow`; hit policies in BR-3.10; evaluate and explain endpoints; consumers in waves 4, 13, 15, 30, 48 must use it |
| SAP §13.31 Cross-Application → SAP Business Rules (BRF+ in GRC, tax, dispute, output) (register row 352) | PARTIAL-3 | ADDED as FR-3.13 | same mechanism; per-domain rule sets are authored in the consuming waves (tax 13, output 15, credit/dispute 30, GRC 48) |
| SAP §13.31 Customizing (SPRO) → BRF+ applications, functions, rulesets, decision tables, expressions (FDT_*) (register row 354) | PARTIAL-3 | ADDED as FR-3.13 | application = rule set, function = table evaluation, ruleset/decision table = `DecisionTable`, expression = condition cells and `computed_expr` from FR-3.3; formulas beyond the expression subset are deferred with open question 2 |

---

## 0.4 Wave 4: Event Backbone, Workflow & Batch

### 0.4.1 Scope and business context

AX runs approvals through the workflow framework (`WorkflowTable`, `WorkflowVersionTable`, `WorkflowWorkItemTable`, `WorkflowTrackingStatusTable`, `WorkflowTrackingTable`; approval, task and conditional-decision elements; work item statuses Pending/Delegated/Completed/Canceled; escalation and due dates) and background work through the batch framework (`BatchJob`, `Batch`, `BatchJobHistory`, `BatchGroup`, `RunBaseBatch`, `SysOperationServiceController` with `SysOperationExecutionMode` Synchronous/Asynchronous/ReliableAsynchronous/ScheduledBatch, job statuses Waiting/Executing/Ended/Error/Hold/Canceling/Canceled); D365 adds business events. SAP has Business Workflow (SWDD, work items SWWWIHEAD, SWIA/SWI1, SBWP inbox, event linkage SWE2, deadline monitoring), background jobs (SM36 definition, SM37 monitor, TBTCO/TBTCP, statuses Scheduled/Released/Ready/Active/Finished/Canceled, job classes A/B/C, SM62 events) and IDoc status processing (EDIDC/EDIDS status 03/51/53/64, BD87). Wave 4 replaces these with Strimzi Kafka carrying CloudEvents, a transactional outbox in every service via Debezium, a schema registry fed by entity metadata, Temporal for approvals and batch, a configuration-driven approval chain, a partitioned checkpointing batch framework and a job monitor. Out of scope: notification channels beyond in-app and email (wave 46 adds Teams/Slack), position-hierarchy assignee resolution (wave 42), line-item workflows (wave 17 first consumer), IDoc/EDI payload semantics (wave 35).

#### Pain points solved (10x lens)

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| Downstream systems (MES, WMS, BI) poll `ModifiedDateTime` on AX tables or wait for the nightly IDoc/BD21 change-pointer job; IDocs stuck in status 51/64 are reprocessed by hand in BD87 | No reliable push of changes; outbound integration is batch | Every committed change is a CloudEvent on Kafka via Debezium outbox within 500 ms; consumers subscribe, never poll | Change-to-consumer latency: 15 min–24 h → < 2 s p95; manual reprocessing runs per week: dozens → 0 (DLQ with reason, replayable) |
| AX workflow approvals: the workflow batch job (`WorkflowMessageProcessingTask`) runs every minute and the work-item list is refreshed manually; escalation "due dates" silently fail when the batch server is down | Workflow runtime is a polling batch job on the AOS | Temporal workflows with durable timers; work item pushed to the inbox by event | Submit-to-inbox latency: 1–5 min → < 2 s; missed escalations due to batch outage: possible → 0 (timers survive restarts) |
| SAP Business Workflow (SWDD) changes need an ABAP workflow developer, a transport and SWU3 checks; amount bands for release strategies live in customizing tables (CL20N/CL24N) | Chains are code and classification objects, not configuration | Designer with amount bands, conditions and SLAs as versioned data, activated per legal entity | Approval chain change: 1–3 weeks → < 30 min by an administrator; developer involvement: required → none |
| A failed AX batch job over 1M rows (e.g. inventory recalculation) restarts from zero; SM37 jobs show "Canceled" with a dump in ST22 and no restart point | Batch frameworks have no checkpoints or partitions | Partitioned tasks with checkpoints every 10k rows; resume from last checkpoint; rerun failed partitions only | Rework after a failure at 90%: 90% of the run → < 1%; mean time to resume: hours → < 1 min |
| Exactly-once delivery requires custom dedupe tables in X++/ABAP and still double-posts under AOS/work-process crashes | Dual writes (DB + queue) in application code | Outbox in the same transaction; consumer library dedupes on CloudEvents `id` | Duplicate side effects under chaos: observed → 0 over 10 kill cycles |

### 0.4.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Change notification | Business events (D365 `BusinessEventsBase`), AIF outbound ports, alerts (`EventInbox`) | Event type linkage SWE2, change pointers BD21, IDoc outbound | CloudEvents on Kafka via outbox; AsyncAPI per topic | improved |
| Reliable publish | AIF queue (`AifGatewayQueue`), dual-write risk in X++ | tRFC/qRFC (SM58), IDoc status 03 | Debezium outbox event router; exactly-once effect | improved |
| Workflow definition | Workflow editor, `WorkflowTypeTable`, `WorkflowDocument` class, approval/task elements | SWDD workflow builder, tasks TS*, agent rules PFAC | `WorkflowDefinition` JSON (steps, conditions, SLAs), designer UI | full |
| Work items | `WorkflowWorkItemTable`, Work list, Delegate/Approve/Reject/Request change | SWWWIHEAD, SBWP inbox, forward/resubmit | `WorkItem` with approve/reject/delegate/request_change/recall | full |
| Escalation and deadlines | Time limits, escalation path, `WorkflowEscalation*` | Deadline monitoring (latest end), SWWDHEX | Temporal timers; SLA + escalation action | full |
| Workflow history | `WorkflowTrackingTable`, tracking status form | SWIA, workflow log (SWI1/SWEL) | Temporal history + `WorkflowInstance` timeline API | improved |
| Background jobs | `BatchJob`, `Batch`, `BatchGroup`, `SysOperation` data contracts, recurrence (`SysRecurrence`) | SM36/SM37, TBTCO/TBTCP, variants, job classes A/B/C | `BatchJob` definition, `BatchRun`, `BatchTask`, cron recurrence, priority | full |
| Job monitoring | Batch job history, `BatchJobHistory`, Infolog | SM37, job log (TBTC5), SM50 | Job monitor UI, run log, Prometheus metrics | improved |
| Restart / reprocess | Rerun batch job, no checkpoints | BD87 IDoc reprocess, job copy in SM37 | checkpoint per partition; resume from last checkpoint | improved |
| Outbound email | Email parameters (`SysEmailParameters`: SMTP, Exchange, mail sending mode batch/client), Email distributor batch (`SysEmailBatch`), Outbound email status (`SysOutgoingEmailTable`), Retry failed | SAPconnect (SCOT) nodes, send job RSCONN01, SOST send status, SOSV/SOSG | notify service: `OutboundMessage` queue, provider adapter (SMTP / HTTP API), distributor job, status inquiry, retry, bounce ingest | full |
| Message templates and notification settings | Email templates (`SysEmailTable` / `SysEmailMessageTable`, per language), Workflow approval email templates, Workflow parameters → Notifications, User options → Workflow notifications (email, in-app, digest), Alert email templates | SO10 standard texts in workflow tasks, SWNCONFIG extended notifications (digest schedule, delivery), SBWP settings, SU3 user defaults | `MessageTemplate` (channel, language, placeholders, layout), sys templates per workflow event, `NotificationPreference` per user, per-definition notify flags | full |

### 0.4.3 Functional requirements

- **FR-4.1 Kafka and topics** — Must. The system shall run Strimzi-managed Kafka (3 brokers in cloud, 1 in k3d, KRaft) with topics named `passage.<domain>.<aggregate>.v<n>`, partition key = aggregate id, 12 partitions default, retention 7 days (entity-state topics compacted), and CloudEvents 1.0 JSON envelopes with extensions `tenantid`, `legalentityid`, `traceparent`, `actor`.
  - Notes: no per-tenant topics; consumers filter by `tenantid` header; max message 1 MB, larger payloads use a claim-check reference (object storage URL).
- **FR-4.2 Transactional outbox** — Must. Every service shall write domain changes and an `outbox_event` row in the same transaction; Debezium (one connector per domain database, pgoutput) with the outbox event router publishes and never writes to Kafka from application code.
  - Notes: outbox columns id, aggregate_type, aggregate_id, event_type, payload (jsonb), headers (jsonb), created_at; rows deleted after 24 h by a sweep; publish lag p95 ≤ 500 ms.
- **FR-4.3 Schema registry** — Must. Event payload schemas shall be JSON Schema derived from the metadata registry (`dev.passage.<domain>.<entity>.<verb>.v<n>` ↔ entity version) registered with BACKWARD_TRANSITIVE compatibility; CI rejects an incompatible schema.
- **FR-4.4 AsyncAPI generation** — Must. The generator from wave 3 shall emit an AsyncAPI 3.0 document per domain listing topics, event types and schemas; published at `/v1/events/asyncapi/{domain}`.
- **FR-4.5 Consumer library** — Must. Go and TS libraries shall provide idempotent consumption keyed by CloudEvents `id` (dedupe table per consumer group, 7-day window), ordered processing per partition, retry with exponential backoff (1 s → 5 min, 10 attempts) and a per-topic dead-letter topic `…dlq`.
- **FR-4.6 Temporal cluster** — Must. The system shall run Temporal (CNPG persistence, 1 namespace per environment, `tenant_id` as a search attribute) with Go and TS SDK wrappers that inject tenant, actor and trace context into every workflow and activity.
  - Notes: workflow ids `wf-{tenant}-{entity}-{record_id}-{seq}`; history retention 90 days; activity timeouts default 30 s start-to-close, heartbeat for long activities.
- **FR-4.7 Approval workflow template** — Must. The system shall provide a configurable approval workflow with steps (assignee resolution: user, role, expression, `record.owner`, manager placeholder), conditions (expression over the document, e.g. `amount_minor > 1000000`), completion policy (single, all, majority, percentage n), SLA (ISO 8601 duration) and escalation action (delegate_to, auto_approve, auto_reject, notify_only), and actions submit, approve, reject, delegate, request_change, recall, cancel.
  - Notes: a step with zero resolved assignees escalates immediately to the workflow administrator; delegation may chain at most 3 levels; delegation records keep the original assignee for audit.
- **FR-4.8 Workflow designer** — Must. Administrators shall configure approval chains per entity with amount bands and conditions in a form (no code), version them, and activate one version per entity × legal entity.
- **FR-4.9 Batch job framework** — Must. The system shall define jobs (`BatchJob`: handler name, parameters JSON Schema, partition strategy range|hash|list, partition count ≤ 256, concurrency ≤ 32, priority A/B/C, retry policy), schedule them by cron (tenant time zone) or on demand, execute each partition as a Temporal activity with checkpoints every N rows (default 10,000) and resume from the last checkpoint after failure.
  - Notes: statuses waiting, scheduled, executing, ended, error, hold, canceling, canceled; max run time default 6 h; a job may declare `exclusive=true` to prevent overlapping runs.
- **FR-4.10 Job monitor** — Must. The system shall show runs, tasks, progress (rows/sec, ETA), logs and errors; allow cancel, hold, resume and rerun-failed-partitions; retain history 90 days.
- **FR-4.11 Metadata-changed events** — Must. The metadata registry shall emit `dev.passage.metadata.entity.published.v1` through its own outbox (first production consumer of the backbone).
- **FR-4.12 Chaos and replay** — Should. The platform shall support topic replay from an offset or timestamp per consumer group and ship a chaos test that kills a service between commit and publish.
- **FR-4.13 Outbound email and message delivery** — Must. The system shall provide a `notify` service (TS) with a provider adapter interface (`Send`, `GetStatus`, `IngestBounce`) implemented for SMTP (STARTTLS or implicit TLS, credentials via ESO) and for an HTTP API provider (the SMTP relay is the P0 default per open question 2); a per-tenant sender identity (from address, display name, reply-to, envelope domain whose SPF/DKIM/DMARC are the customer's responsibility and are checked by a "verify sender" action); an `OutboundMessage` queue (channel email | in_app | webhook; status queued → sending → sent | failed | bounced; attempts; provider message id); a distributor job (every 60 s, batch 500, 5 attempts with backoff 1 min → 1 h); a mail sending mode per tenant (`immediate` for workflow and alerts, `batch` for digests and reports); attachments ≤ 10 MB total per message; an outbound status inquiry; manual retry; and a bounce webhook/IMAP ingest that marks the message `bounced` and, after 3 hard bounces, flags the recipient user (`identity.user.email_status=bouncing`).
  - Notes: in-app messages are rows in the same table read by the shell notification tray (wave 5) via SSE; webhook deliveries sign the body with HMAC-SHA256 (`X-Passage-Signature`) and retry on non-2xx; messages are retained 90 days (body) and 3 years (metadata); a tenant with no sender identity falls back to the platform default `no-reply@<env>.passage.dev` with a warning on the parameters form; the service never generates email in test environments unless `notify.allowlist_domains` matches the recipient.
- **FR-4.14 Message templates and notification settings** — Must. The system shall provide `MessageTemplate` (code, channel, language, subject, body in Mustache syntax with placeholders resolved from the entity record via metadata (`{{record.number}}`, `{{record.amount | money}}`), the workflow context (`{{workitem.due_at | date}}`, `{{instance.submitted_by.display_name}}`, `{{link}}` deep link to the work item or record) and standard texts from wave 5 (`{{text:CODE}}`); an HTML layout wrapper per tenant (logo, footer); versioning and `sys`/`customer` layers so customers override shipped templates per language); shipped `sys` templates for `workflow.workitem.assigned`, `.escalated`, `.delegated`, `instance.completed`, `instance.change_requested`, `batch.run.failed`, `identity.user.invited`; per-user `NotificationPreference` (per event class: in_app, email, none; digest none | daily at HH:MM | weekly on day; language; quiet hours); per-`WorkflowDefinition` flags (notify_on_assign, notify_on_escalate, notify_submitter_on_complete, comment_required); and a "preview with sample record" action.
  - Notes: rendering is fail-closed — a missing placeholder or render error marks the message `failed` with NTF-0407 instead of sending a blank; templates are rendered in the recipient's language, falling back to the tenant default then `en`; digests aggregate pending in-app items into one email at the chosen time (BR-4.12); user-level workflow delegation stays in `WorkItem` (FR-4.7) and is referenced, not duplicated, by preferences.

### 0.4.4 Data model

**OutboxEvent** (`<domain>.outbox_event`, every service)

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK; becomes CloudEvents `id` |
| tenant_id | uuid | yes | RLS; copied to header `tenantid` |
| aggregate_type | varchar(64) | yes | entity name; routes to topic |
| aggregate_id | uuid | yes | partition key |
| event_type | varchar(128) | yes | `dev.passage.<domain>.<entity>.<verb>.v<n>` |
| payload | jsonb | yes | CloudEvents `data` |
| headers | jsonb | yes | traceparent, actor, legalentityid, schema_version |
| created_at | timestamptz | yes | CloudEvents `time` |

**WorkflowInstance** (`workflow.instance`)

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK; equals Temporal workflow id suffix |
| tenant_id, legal_entity_id | uuid | yes | |
| definition_id, definition_version | uuid, int | yes | FK WorkflowDefinition |
| entity, record_id | varchar(48), uuid | yes | document under approval |
| status | enum | yes | submitted, in_review, change_requested, approved, rejected, recalled, canceled, failed |
| submitted_by, submitted_at | uuid, timestamptz | yes | |
| current_step | int | no | 1-based |
| completed_at, outcome_reason | timestamptz, text | no | |
| temporal_run_id | varchar(64) | yes | |

**WorkItem** (`workflow.work_item`)

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| instance_id | uuid | yes | FK |
| step | int | yes | |
| assignee_id | uuid | yes | FK user |
| original_assignee_id | uuid | no | set when delegated |
| status | enum | yes | pending, claimed, approved, rejected, delegated, escalated, change_requested, canceled, expired |
| due_at | timestamptz | yes | SLA |
| acted_at, comment | timestamptz, text | no | |

**BatchJob / BatchRun / BatchTask** (`workflow.batch_*`): BatchJob has id, tenant_id, code (unique), handler, params_schema jsonb, partition_strategy, partition_count, concurrency, priority, cron, time_zone, exclusive, retry_policy jsonb, status (active, hold); BatchRun has id, job_id, params jsonb, status (FR-4.9), scheduled_at, started_at, ended_at, rows_total, rows_done, error, triggered_by; BatchTask has id, run_id, partition_no, status, checkpoint jsonb, attempts, last_error, rows_done.

**OutboundMessage** (`notify.outbound_message`, partitioned monthly)

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| tenant_id | uuid | yes | RLS |
| channel | enum | yes | email, in_app, webhook |
| recipient_user_id, recipient_address | uuid, varchar(254) | one of | user for in_app/email; address for external email; URL for webhook |
| template_code, template_version, language | varchar(48), int, char(5) | no | null when sent raw by a service |
| subject, body_html, body_text | varchar(255), text, text | yes (email) | rendered content; body retained 90 days |
| related_entity, related_record_id | varchar(48), uuid | no | deep link target |
| status | enum | yes | queued, sending, sent, failed, bounced, read (in_app only) |
| attempts, next_attempt_at, last_error | int, timestamptz, text | yes/no/no | BR-4.8-style backoff |
| provider_message_id | varchar(200) | no | for bounce correlation |
| priority | enum | yes | immediate, batch |
| created_at, sent_at | timestamptz | yes/no | |

**MessageTemplate** (`notify.message_template`)

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| tenant_id | uuid | yes | `sys` tenant for shipped templates |
| code | varchar(64) | yes | e.g. `workflow.workitem.assigned`; unique with language, layer, version |
| channel | enum | yes | email, in_app, webhook |
| language | char(5) | yes | BCP 47 (`en`, `de-DE`) |
| layer | enum | yes | sys, customer |
| version, status | int, enum | yes | draft, published, retired |
| subject, body | varchar(255), text | yes | Mustache; placeholders validated against the event's payload schema at publish |
| layout_id | uuid | no | FK tenant HTML layout |
| sample_payload | jsonb | no | for preview |

**NotificationPreference** (`notify.preference`)

| Field | Type | Required | Notes |
|---|---|---|---|
| user_id | uuid | yes | PK with event_class |
| tenant_id | uuid | yes | |
| event_class | enum | yes | workflow_assigned, workflow_escalated, workflow_completed, batch_failed, alert (wave 5), system |
| channels | enum set | yes | in_app, email; default in_app + email for workflow_assigned |
| digest | enum | yes | none, daily, weekly |
| digest_time, digest_day | time, int | no | tenant time zone; day 1–7 |
| quiet_hours | int4range | no | hours suppressed for email (digest still sent) |
| language | char(5) | no | null = user locale |

Invariants: exactly one active `WorkflowDefinition` version per (entity, legal_entity); a WorkItem is terminal once acted; `BatchRun` of an exclusive job cannot overlap another run of the same job; outbox rows are never updated. Indexing: `outbox_event(created_at)`; `work_item(tenant_id, assignee_id, status)`; `instance(tenant_id, entity, record_id)`; `batch_run(job_id, scheduled_at desc)`. Partitioning: `work_item` and `batch_task` by month from wave 30.

### 0.4.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| GET | /v1/events/topics, /v1/events/asyncapi/{domain} | Topic catalog, AsyncAPI | n/a | any |
| GET/POST | /v1/events/schemas/{type} | Read / register schema | by type+version | Maintain entity metadata |
| POST | /v1/events/replay | Replay topic for consumer group from offset/time | request id | Maintain batch jobs |
| GET/POST/PUT | /v1/workflows/definitions, /{id}/versions/{v}, /{id}/activate | Definitions | by version | Maintain workflows |
| POST | /v1/workflows/instances | Submit a document | entity+record_id+seq | per entity `post`/`update` |
| GET | /v1/workflows/instances/{id}, /{id}/timeline | Status and history | n/a | submitter, assignee, Maintain workflows |
| POST | /v1/workflows/instances/{id}/actions | recall, cancel | action+instance version | submitter / Maintain workflows |
| GET | /v1/workitems?assignee=me&status=pending | Inbox | n/a | any |
| POST | /v1/workitems/{id}/actions | approve, reject, delegate, request_change | workitem id + action | assignee (Approve workflow) |
| GET/POST/PUT | /v1/batch/jobs, /{id} | Define jobs | by code | Maintain batch jobs |
| POST | /v1/batch/jobs/{id}/runs | Start run | Idempotency-Key | Maintain batch jobs |
| GET/POST | /v1/batch/runs/{id}, /{id}/cancel, /{id}/resume, /{id}/rerun-failed | Monitor and control | run id + action | View batch history / Maintain batch jobs |
| GET/POST/PUT | /v1/notify/templates, /{code}/versions/{v}, /{code}/versions/{v}/publish, /{code}/preview | Templates and preview | by version | Maintain message templates |
| POST | /v1/notify/messages | Enqueue a message (internal services; template or raw) | Idempotency-Key (24 h) | internal / Send notifications |
| GET/POST | /v1/notify/messages?status=&recipient=&from=&to=, /{id}/retry | Outbound status and retry | retry: message id | View outbound messages / Maintain notification settings |
| GET/PUT | /v1/notify/preferences/me, /v1/notify/preferences/{user_id} | Notification preferences | last write wins | self / Maintain users |
| GET/PUT/POST | /v1/notify/settings, /v1/notify/settings/verify-sender, /test-send | Tenant sender identity, provider, mode; test | n/a | Maintain notification settings |
| POST | /v1/notify/bounces | Provider bounce webhook (HMAC-verified) | provider message id | provider credential |
| GET | /v1/notify/inbox?unread=true | In-app messages for the shell tray (SSE at `/v1/notify/inbox/stream`) | n/a | self |

Validation: approve/reject require a comment when the definition flags `comment_required`; delegate target must hold the step's role; a run request against a `hold` job returns 409; a template publish fails when a placeholder is not in the event's payload schema (NTF-0406); `test-send` is limited to 10 per hour per tenant.

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| dev.passage.workflow.instance.submitted.v1 | submit | instance_id, entity, record_id, definition_version, submitted_by | owning service (sets record status), audit |
| dev.passage.workflow.workitem.assigned.v1 | step entered / delegated | workitem_id, instance_id, assignee_id, due_at, step | shell inbox, notifier |
| dev.passage.workflow.workitem.escalated.v1 | SLA timer fired | workitem_id, from_assignee, to_assignee, action | notifier, audit |
| dev.passage.workflow.instance.completed.v1 | terminal status | instance_id, outcome (approved/rejected/canceled/recalled), completed_at | owning service, audit, shell |
| dev.passage.batch.run.started.v1 / completed.v1 / failed.v1 | run transitions | job_code, run_id, rows_total, rows_done, duration_ms, error | job monitor, alerting, audit |
| dev.passage.metadata.entity.published.v1 | registry outbox | (see wave 3) | events service (topic/schema create) |
| dev.passage.notify.message.sent.v1 / failed.v1 / bounced.v1 | distributor outcome | message_id, channel, template_code, recipient_user_id (no address in payload), attempts, error | audit, alerting (`NotifyFailureRate`), identity (bounce flag) |
| dev.passage.notify.template.published.v1 | template publish | code, version, language, layer | notify workers (cache reload), audit |

### 0.4.6 User experience

| Screen | Primary actions | Key fields/columns | Duty required |
|---|---|---|---|
| Work items (inbox) | Approve, reject, delegate, request change, bulk approve | document, step, submitted by, due, age | Approve workflow |
| Workflow instance timeline | View steps, comments, timers; recall | status, step, assignee, acted at, SLA | submitter / Maintain workflows |
| Workflow designer | Add step, condition, SLA, escalation; version; activate | entity, legal entity, bands, assignees | Maintain workflows |
| Batch jobs | Define, schedule, hold, run now | code, handler, cron, last run, next run | Maintain batch jobs |
| Job monitor | Watch progress, cancel, resume, rerun failed, view log | run, status, rows/sec, ETA, errors by partition | View batch history |
| Topic catalog | Browse topics, schemas, consumer lag | topic, partitions, schema version, lag | Maintain batch jobs |
| Message templates | Create, edit (subject/body with placeholder picker), preview with sample record, publish per language | code, channel, language, layer, version, status | Maintain message templates |
| Outbound messages | Filter by status/recipient/date, open rendered message, retry, cancel queued | sent at, channel, recipient, subject, status, attempts, last error, provider id | View outbound messages |
| Notification settings (tenant) | Sender identity, provider, mode, allowlist, verify sender, test send | from, reply-to, provider, mode, SPF/DKIM check result | Maintain notification settings |
| My notification preferences | Per event class choose channels, digest, quiet hours, language | event class, in-app, email, digest, time | self |

Rules: bulk approve is limited to 100 items per action and requires a confirmation listing document totals; the inbox refreshes from events within 2 s; designer validates expressions against the entity's current metadata; keyboard `a` approve, `r` reject, `d` delegate on a focused row.

### 0.4.7 Business rules and edge cases

- **BR-4.1** Instance status machine: submitted → in_review → (approved | rejected | change_requested → submitted on resubmit); submitted/in_review → recalled (by submitter, before any approval) or canceled (by administrator); any → failed on unrecoverable activity error (alert raised).
- **BR-4.2** Work item status machine: pending → claimed → approved | rejected | change_requested; pending/claimed → delegated (new item created) | escalated (on SLA) | expired (SLA with `auto_reject`) | canceled (instance terminal).
- **BR-4.3** SLA timers use the tenant time zone and the tenant work calendar when one exists (wave 6); in P0 they are wall-clock; the timer survives Temporal worker restarts.
- **BR-4.4** Completion policy `majority` rounds up (3 of 4); `percentage 60` on 5 assignees needs 3; a single reject under `all` rejects the step immediately.
- **BR-4.5** Exactly-once effect: a consumer that sees a CloudEvents `id` already in its dedupe table acknowledges without side effects; the dedupe table is keyed (consumer_group, event_id) and swept after 7 days.
- **BR-4.6** Ordering is guaranteed per aggregate id only; consumers must not assume cross-aggregate order.
- **BR-4.7** Batch checkpoint is committed in the same transaction as the processed rows; after resume, rows with ids ≤ checkpoint are skipped, so handlers must process in key order within a partition.
- **BR-4.8** Retry policy: activity failures retry up to 5 times with backoff 2 s × 2^n capped at 5 min; after that the task is `error` and the run continues other partitions; run ends `error` if any task is `error`.
- **BR-4.9** A workflow definition change never alters running instances; they finish on their version.
- **BR-4.10** Poison events after 10 delivery attempts go to the DLQ with the last error in header `dlqreason`; a DLQ depth > 0 for 10 min alerts.
- **BR-4.11** Outbound message status machine: queued → sending → sent | failed; sent → bounced (provider bounce); queued → canceled (administrator); in_app: queued → sent → read. Rendering happens at enqueue time (the stored body is what was sent); a render error is `failed` with NTF-0407 and never a partially rendered send. Retries: attempts 1–5 at 1 min, 5 min, 15 min, 1 h, 1 h; after the fifth failure the message stays `failed` and only a manual retry resets `attempts`.
- **BR-4.12** Digest: for a user with `digest=daily 08:00`, every in-app item of the configured event classes created since the previous digest is listed in one email at 08:00 tenant time; items already read in the shell are omitted; an empty digest is not sent; `workflow_escalated` always sends immediately regardless of digest (escalations are time-critical); quiet hours delay immediate email to the end of the window but never delay in-app.
- **BR-4.13** Bounce handling: a hard bounce (5xx, unknown recipient) marks the message `bounced`; the third hard bounce for the same address within 30 days sets `identity.user.email_status=bouncing`, suppresses further email to that user (in-app continues) and notifies Security administrators; a soft bounce (4xx, mailbox full) is retried per BR-4.11.

### 0.4.8 Acceptance criteria

- **AC-4.1** Given a service killed by SIGKILL between DB commit and Debezium capture, 10 times in a loop, When it restarts, Then every committed outbox row appears on Kafka exactly once (consumer dedupe table shows 0 duplicates, 0 gaps). (FR-4.2, FR-4.5)
- **AC-4.2** Given a 3-level chain (role → user → expression) with 4-hour SLAs, When step 2's assignee delegates and step 3 times out with `delegate_to admin`, Then the instance completes approved after simulated clock advance and the timeline shows 5 work items. (FR-4.7)
- **AC-4.3** Given a batch job over 1,000,000 synthetic rows in 16 hash partitions, When the worker pod is killed at ~40%, Then the run resumes and ends with rows_done = 1,000,000 and no duplicate side effects. (FR-4.9)
- **AC-4.4** Given a schema change that removes a required field, When CI registers it, Then the registry rejects it with the incompatible path. (FR-4.3)
- **AC-4.5** Given an entity publish in the registry, When the outbox is captured, Then a topic `dev.passage.metadata.entity.v1` message with type `dev.passage.metadata.entity.published.v1` exists within 1 s. (FR-4.11)
- **AC-4.6** Given a submitter recalls after step 1 approved, When the action is sent, Then 409 `RECALL_NOT_ALLOWED`. (BR-4.1)
- **AC-4.7** Given a consumer group replay from `T-1h`, When executed, Then events are redelivered and the dedupe table suppresses side effects for already-processed ids. (FR-4.12)
- **AC-4.8** Given an exclusive job with a running instance, When a second run is requested, Then 409 `RUN_OVERLAP`. (FR-4.9)
- **AC-4.9** Given a step whose assignee expression resolves to nobody, When the step is entered, Then it escalates to the workflow administrator within 5 s and emits `workitem.escalated`. (FR-4.7)
- **AC-4.10** Given a tenant with a verified SMTP sender and a user whose `workflow_assigned` preference is in_app + email, When a work item is assigned, Then an in-app message appears in the tray within 2 s and an email rendered from the `sys` template (subject contains the document number, body contains the deep link) reaches the CI mail sink within 60 s. (FR-4.13, FR-4.14)
- **AC-4.11** Given the SMTP relay is down for 20 minutes, When 50 messages are enqueued, Then all 50 show `queued`/`sending` with attempts in the outbound inquiry, none is lost, and all are `sent` within 10 minutes of the relay returning. (FR-4.13, BR-4.11)
- **AC-4.12** Given a user with `digest=daily 08:00` and 7 unread assignments of which 2 were read in the shell, When 08:00 tenant time passes, Then exactly one email lists 5 items and no per-item emails were sent. (FR-4.14, BR-4.12)
- **AC-4.13** Given a customer template override for `workflow.workitem.assigned` in `de-DE` referencing placeholder `{{record.nonexistent}}`, When published, Then the publish is rejected with NTF-0406 naming the placeholder. (FR-4.14)
- **AC-4.14** Given the provider reports a hard bounce three times for the same address within 30 days, When the third bounce is ingested, Then the user's `email_status` is `bouncing`, subsequent messages to that user are in-app only, and Security administrators receive a notification. (BR-4.13)

### 0.4.9 Test plan and fixtures

Fixtures: `fixtures/events/chaos-outbox.yaml` (kill schedule), `fixtures/workflow/chain-3-level.json`, `fixtures/workflow/completion-policies.json` (12 cases), `fixtures/batch/rows-1m.sql` (synthetic), `fixtures/events/schema-breaking-cases/` (8 breaking, 8 compatible).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Outbox chaos | Chaos / integration | Debezium + consumer lib | 0 dup, 0 loss over 10 kills |
| Approval chain | Temporal test server (time-skipping) | workflow | expected timeline, timers fire |
| Completion policy | Property | step evaluator | 12/12 fixtures; random assignee sets obey rounding |
| Batch resume | Integration | batch framework | 1M rows, resume, idempotent |
| Schema compatibility | Contract | registry | 16/16 classified correctly |
| Publish lag | Performance | outbox → Kafka | p95 ≤ 500 ms at 2,000 events/s |
| DLQ routing | Negative | consumer lib | poison event lands in DLQ with reason |

Reconciliation: count of outbox rows created in a window equals Kafka messages for that domain in the same window (nightly check, tolerance 0); sum of `BatchTask.rows_done` equals `BatchRun.rows_done`.

### 0.4.10 Deployment and operations

Charts: `strimzi-kafka` (cluster CR), `debezium-connect` (one connector per domain DB, secrets via ESO), `schema-registry`, `temporal` (frontend/history/matching/worker, CNPG persistence), `workflow` (TS workers + API), `events` (catalog API). Config: `KAFKA_BOOTSTRAP`, `SCHEMA_REGISTRY_URL`, `TEMPORAL_ADDRESS`, `TEMPORAL_NAMESPACE`, `OUTBOX_SWEEP_HOURS=24`. Migrations: outbox table added to every domain by generator; `logical_decoding` and `wal_level=logical` set in CNPG clusters. Feature flags: `workflow.enforce` per entity. Scheduled jobs: outbox sweep hourly, DLQ report daily, Temporal history archival. SLOs: publish lag p95 ≤ 500 ms, consumer lag ≤ 10 s p95, workflow action p95 ≤ 300 ms, batch scheduler drift ≤ 30 s. Alerts: `DebeziumConnectorFailed`, `ConsumerLagHigh`, `DLQNonEmpty`, `TemporalWorkflowFailedRate`, `BatchRunError`. Runbook: connector restart with slot reset, replay procedure, stuck workflow reset via Temporal CLI, DLQ reprocess. Rollback: digest rollback; schemas are backward compatible so older consumers keep working; Temporal workers are versioned with build ids.

### 0.4.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| AX `WorkflowTable` / `WorkflowVersionTable` (approval elements, conditions) | WorkflowDefinition | exported via workflow export XML; steps mapped where assignee type is user/role; hierarchy-based assignees flagged for wave 42 |
| AX `BatchJob` / `Batch` / `SysRecurrence` | BatchJob | only jobs whose handler has a Passage equivalent (listed per later wave); recurrence → cron |
| SAP TBTCO/TBTCP (job name, program, variant, period) | BatchJob | same rule; variants → params |
| SAP SWDD workflow templates (WS*), PFAC rules | WorkflowDefinition | manual rebuild from a generated inventory report |
| In-flight work items (`WorkflowWorkItemTable`, SWWWIHEAD) | not migrated | cut over by completing or canceling in the source system (wave 49 runbook) |

Not migrated: workflow history (kept read-only in the source for audit), AIF/IDoc queues, batch job logs.

### 0.4.12 Dependencies, risks, open questions

Depends on: wave 3 (entity metadata for schemas and outbox generation, registry as first producer), wave 2 (actor identity, duties `Approve workflow`, `Maintain workflows`, `Maintain batch jobs`, `View batch history`).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Debezium replication slot bloat when connector is down | Medium | High | Slot lag alert at 1 GB; max WAL retention 20 GB; runbook |
| Temporal operational complexity on k3d | Medium | Medium | Single-binary dev mode for `PROFILE=min` |
| Expression language in conditions diverges from metadata hooks | Medium | Medium | Same expression engine as wave 3 |

Open questions: (1) JSON vs Avro/Protobuf for payloads — default JSON Schema (matches metadata kernel); decide by wave 4 start. (2) Email provider in P0 — default SMTP relay via ESO secret; decide by wave 5. (3) Temporal namespace per tenant vs shared with search attribute — default shared; decide by wave 48.

### 0.4.13 Parameters and setup

Tenant values live on Admin workspace → Workflow parameters, Batch parameters and Notification settings (the AX "Workflow parameters", "Email parameters" and "System parameters → Alerts" forms; SAP SWU3, SWNCONFIG, SCOT and SM36 settings).

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| `events.topic.partitions_default` | environment | int | 12 | 1–96 | FR-4.1 default partition count | none | qRFC queue count (SMQR) |
| `events.topic.retention_days` | environment | int | 7 | 1–30 (compacted topics unlimited) | Kafka retention | AIF message retention | IDoc reorganization (RSETESTD / SARA IDOC) |
| `events.max_message_bytes` | environment | int | 1,048,576 | ≤ 1 MB | larger payloads use claim-check | none | IDoc segment limits |
| `outbox.sweep_hours` | environment | int | 24 | 1–168 | outbox row deletion after capture | none | tRFC LUW deletion (SM58 reorg) |
| `consumer.dedupe_window_days` | environment | int | 7 | 1–30 | BR-4.5 | none | none |
| `consumer.max_attempts`, `consumer.backoff_max_seconds` | environment | int, int | 10, 300 | 1–50, 10–3,600 | FR-4.5 retry policy before DLQ | Business events retry (D365) | tRFC retry (SM59 `Connection attempts`) |
| `workflow.enforce` | tenant × entity (flag) | bool | false until a definition is activated | true/false | submit required before posting | Workflow configuration → Activate | Flexible workflow scenario activation |
| `workflow.admin_user_id` | tenant | uuid | Tenant owner | any active user | escalation target when nobody resolves (FR-4.7) | Workflow parameters → Workflow administrator | SWU3 workflow system administrator; SWIA |
| `workflow.delegation_max_depth` | tenant | int | 3 | 1–5 | FR-4.7 chain limit | Workflow delegation | HRUS_D2 substitution chain |
| `workflow.default_sla` | tenant | ISO 8601 duration | P2D | PT1H–P30D | default due date per step | Workflow → time limits (default) | Deadline monitoring latest end |
| `workflow.history_retention_days` | environment | int | 90 | 30–3,650 | Temporal history retention | Workflow cleanup batch (`WorkflowCleanup`) | SWWL / RSWWWIDE deletion |
| `batch.max_runtime_hours` | tenant | int | 6 | 1–48 | FR-4.9 run timeout | Batch job "Execution time" alerts | SM37 job runtime / `rdisp/max_wprun_time` |
| `batch.default_checkpoint_rows` | environment | int | 10,000 | 1,000–1,000,000 | FR-4.9 | none | none |
| `batch.max_concurrency_per_tenant` | tenant | int | 32 | 1–128 | total concurrent tasks per tenant | Batch server groups / max batch threads | SM61 server groups, `rdisp/wp_no_btc` |
| `batch.history_retention_days` | tenant | int | 90 | 30–730 | FR-4.10 | Batch job history cleanup | SM37 → delete old jobs (RSBTCDEL2) |
| `notify.provider` | tenant | enum | smtp | smtp, http_api | FR-4.13 adapter | Email parameters → SMTP / Exchange | SCOT SMTP node |
| `notify.smtp.host`, `.port`, `.tls`, `.credential_ref` | tenant | string, int, enum, secret ref | platform relay, 587, starttls, ESO path | — | outbound SMTP | Email parameters → SMTP server, port, SSL, user/password | SCOT node → host, port, TLS, logon data |
| `notify.sender.from`, `.display_name`, `.reply_to` | tenant (legal entity override) | email, string, email | `no-reply@<env>.passage.dev` | verified domain | sender identity | Email parameters → "Send from" | SCOT default domain, SO_USER_AUTOMATIC |
| `notify.mode` | tenant | enum per event class | immediate (workflow, alerts), batch (digests) | immediate, batch | when the distributor sends | Mail sending mode: batch / client | SCOT send job RSCONN01 period |
| `notify.distributor_interval_seconds` | environment | int | 60 | 15–900 | distributor polling | Email distributor batch recurrence | RSCONN01 job period |
| `notify.max_attempts` | tenant | int | 5 | 1–10 | BR-4.11 | Email distributor "Retries" | SCOT retry settings |
| `notify.allowlist_domains` | environment | domain list | empty in prod, `passage.dev` in dev/stage | domains | non-prod recipient guard | none | SCOT address area restrictions |
| `notify.body_retention_days`, `notify.metadata_retention_years` | tenant | int, int | 90, 3 | 7–365, 1–10 | outbound history retention | Outbound email status cleanup | SOST reorg (RSBCS_REORG) |
| `notify.default_language` | tenant | BCP 47 | en | any | template fallback | Email templates default language | SO10 language key fallback |

### 0.4.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Work items (by user, queue, status, overdue) | list inquiry | assignee, status, entity, due before, legal entity | document, step, assignee, original assignee, due, age, SLA state | `workflow.work_item` | Work items assigned to me / Work item inquiries | SBWP, SWI5, SWI2_DEAD |
| Workflow history per document | drill-down | entity, record | steps, assignees, actions, comments, timers, durations | `workflow.instance` + Temporal history | Workflow history / tracking details | SWIA, SWI1, workflow log |
| Workflow performance | KPI | definition, period | submitted, approved, rejected, median cycle time, % escalated, % within SLA | `workflow.*` projections | SSRS Workflow performance | SWI2_DURA, SWI2_FREQ |
| Delegation inquiry | list inquiry | user, date range | delegator, delegate, scope, from, to | `WorkItem.original_assignee_id` + delegation settings | Delegation inquiry (WorkflowDelegation) | HRUS_D2 substitutes |
| Batch job list | list inquiry | status, priority, handler | code, handler, cron, next run, last run status, exclusive | `workflow.batch_job` | Batch jobs list | SM37 job overview |
| Batch run history and errors | drill-down | job, status, date range | run, status, rows, duration, failed partitions, error | `workflow.batch_run`, `batch_task` | Batch job history / Batch job errors; SSRS Batch job report | SM37 job log (TBTC5) |
| Topic and consumer lag | operational report | topic, consumer group | partitions, end offset, committed offset, lag, lag seconds | Kafka admin API | Business events catalog / AIF queue | SMQ1/SMQ2, SM58, WE05 |
| DLQ report | operational report (daily) | topic, period | event type, count, first/last seen, top `dlqreason` | `…dlq` topics | AIF exceptions | BD87 IDoc status 51 list, SM58 errors |
| Outbound message status | list inquiry | status, channel, recipient, template, date | sent at, recipient, subject, status, attempts, last error, provider id | `notify.outbound_message` | Outbound email status (E-mail sending status) | SOST |
| Notification delivery KPI | KPI | period, template | sent, failed, bounced, median send latency | `notify.outbound_message` | none | SOSV send statistics |
| Template catalog | list inquiry | code, language, layer, status | code, channel, languages present, customer override flag, version | `notify.message_template` | Email templates (`SysEmailTable`) | SO10 text list, SE75 text objects |

### 0.4.15 Printed and electronic documents

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Work item notification email | `workitem.assigned` / `.delegated` (preference allows email) | header: tenant layout logo; subject: `{entity} {record.number} awaits your approval`; body: submitter, amount, due, comment, deep link, approve/reject links (signed, single-use, 72 h); footer: preference link | email (HTML + text) | none (message id) | body 90 days, metadata 3 years |
| Escalation email | `workitem.escalated` | from/to assignee, overdue by, deep link | email | none | same |
| Completion email to submitter | `instance.completed` with `notify_submitter_on_complete` | outcome, approver, comment, link | email | none | same |
| Daily / weekly digest | digest schedule (BR-4.12) | list of pending items grouped by entity with due dates | email | none | same |
| Batch failure email | `batch.run.failed` to job owner | job, run, failed partitions, first error, link to monitor | email | none | same |
| Webhook delivery | subscribed event class | CloudEvents JSON body, `X-Passage-Signature` | HTTPS POST | CloudEvents `id` | delivery log 3 years |
| AsyncAPI document | metadata publish | channels, messages, schemas | AsyncAPI 3.0 YAML/JSON | entity version | retained per version |

### 0.4.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| Outbox sweep | hourly | domain database | deletes captured rows older than `outbox.sweep_hours` (by `created_at` and Debezium LSN) | < 1 min | retry next hour | `OutboxBacklog` (> 100 k rows) |
| Dedupe table sweep | daily 02:00 UTC | consumer group | deletes rows older than window | < 5 min | retry | none |
| DLQ report | daily 06:00 UTC | topic | report keyed by date | < 1 min | none | `DLQNonEmpty` (10 min) |
| Temporal history archival | daily | namespace | Temporal archival to object storage | < 30 min | retried by Temporal | `TemporalArchivalFailed` |
| Workflow SLA timers | event-driven (durable timers, not polled) | workflow id | Temporal timer | fires within 1 s of due | survives restarts | `TemporalWorkflowFailedRate` |
| Email distributor | every 60 s | tenant × priority | `SELECT … FOR UPDATE SKIP LOCKED` batch 500; status is the checkpoint | < 10 s per batch | BR-4.11 retries | `NotifyFailureRate` (> 5% over 15 min), `NotifyQueueAge` (oldest queued > 10 min) |
| Digest builder | every 15 min (sends users whose digest time fell in the window) | tenant | one digest per (user, period) keyed in `notify.digest_log` | < 1 min | missing digest sent next window | `DigestBacklog` |
| Bounce ingest (IMAP mode) | every 5 min | tenant mailbox | provider message id unique | < 30 s | retry | `BounceIngestFailed` |
| Outbound message retention | daily 03:30 UTC | month partition | nulls bodies after 90 days; detaches partitions after 3 years | < 5 min | retry | none |
| Batch history cleanup | daily 04:00 UTC | tenant | deletes runs older than `batch.history_retention_days` | < 5 min | retry | none |

### 0.4.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles that include it |
|---|---|---|---|
| Maintain workflows | `/v1/workflows/definitions*`, activate, cancel instances, Workflow designer | with Approve workflow for the same entity (designing your own approval chain) | Workflow administrator |
| Approve workflow | `/v1/workitems/{id}/actions` (approve, reject, delegate, request_change), Inbox | with the entity's create/post duty on the same record (submitter cannot approve own submission; enforced at runtime, NTF-0410) | every business role with approval steps |
| Submit for approval | `POST /v1/workflows/instances`, recall | none | entity maintainers |
| Maintain batch jobs | `/v1/batch/jobs*`, run, cancel, hold, resume, rerun-failed, `/v1/events/replay` | none | Platform engineer, Batch operator |
| View batch history | `GET /v1/batch/runs*`, Job monitor, Topic catalog read | none | Batch operator, Auditor |
| Maintain message templates | `/v1/notify/templates*`, preview, publish | none | Workflow administrator, Communications administrator |
| Maintain notification settings | `/v1/notify/settings*`, verify sender, test send, retry, cancel queued, other users' preferences | with View outbound messages is acceptable; with Maintain secrets (SMTP credentials are ESO references, never visible) | Tenant owner, Workflow administrator |
| View outbound messages | `GET /v1/notify/messages*`, rendered body view | message bodies may contain document data — masked for `pii=sensitive` placeholders | Workflow administrator, Help desk, Auditor |
| Send notifications | `POST /v1/notify/messages` | none | internal service principals only |

### 0.4.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| WFL-0401 | error | Recall is not allowed after step {n} was approved | RECALL_NOT_ALLOWED (409) | ask the workflow administrator to cancel | BR-4.1, AC-4.6 |
| WFL-0402 | error | A comment is required to {action} | `comment_required` | add a comment | 0.4.5 validation |
| WFL-0403 | error | {user} cannot receive this item: missing role {role} | delegate target check | pick a user who holds the role | 0.4.5 validation |
| WFL-0404 | error | Delegation chain limit ({max}) reached | FR-4.7 | approve or reject directly | FR-4.7 |
| WFL-0405 | warning | No approver resolved for step {n}; escalated to the workflow administrator | empty assignee set | fix the assignee rule | FR-4.7, AC-4.9 |
| WFL-0406 | error | This document is already in approval (instance {id}) | duplicate submit | open the existing instance | FR-4.7 |
| WFL-0407 | error | Bulk approve is limited to 100 items | UX rule | approve in batches | 0.4.6 rules |
| WFL-0408 | error | Job {code} is on hold | run on hold job (409) | release the hold | 0.4.5 validation |
| WFL-0409 | error | Job {code} already has a running instance | RUN_OVERLAP (409) | wait or cancel the running run | FR-4.9, AC-4.8 |
| WFL-0410 | error | You cannot approve a document you submitted | runtime SoD | delegate to another approver | 0.4.17 |
| WFL-0411 | error | Run {id} exceeded the maximum runtime of {h} h and was canceled | `batch.max_runtime_hours` | raise the limit or add partitions | FR-4.9 |
| EVT-0401 | error | Schema change for {type} is incompatible at `{path}` | BACKWARD_TRANSITIVE check | make the change additive or bump `v<n+1>` | FR-4.3, AC-4.4 |
| EVT-0402 | warning | Event {id} moved to DLQ after {n} attempts: {reason} | poison event | fix consumer, then replay | BR-4.10 |
| EVT-0403 | error | Payload exceeds 1 MB; use a claim-check reference | message size | store payload in object storage | FR-4.1 |
| NTF-0401 | error | Sender {address} is not verified for tenant {tenant} | SPF/DKIM check failed | publish DNS records and re-verify | FR-4.13 |
| NTF-0402 | warning | Delivery to {recipient} failed (attempt {n} of {max}): {provider error}; retrying at {time} | provider/transient error | none; watch the outbound inquiry | BR-4.11 |
| NTF-0403 | error | Delivery to {recipient} failed permanently after {max} attempts | retries exhausted | fix the cause and retry manually | BR-4.11 |
| NTF-0404 | warning | {user}'s email address is bouncing; email suppressed, in-app delivery continues | 3 hard bounces | correct the address in the IdP | BR-4.13 |
| NTF-0405 | info | Recipient {address} is outside the non-production allowlist; message not sent | `notify.allowlist_domains` | add the domain in dev/stage only | FR-4.13 |
| NTF-0406 | error | Template {code} ({language}) uses unknown placeholder `{{placeholder}}` | publish-time validation | use a placeholder from the picker | FR-4.14, AC-4.13 |
| NTF-0407 | error | Message not sent: template render failed ({reason}) | runtime render error | fix the template; retry | FR-4.14, BR-4.11 |
| NTF-0408 | error | Attachments exceed 10 MB for one message | size limit | send a link instead | FR-4.13 |

### 0.4.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| Outbox publish (commit → Kafka) | 2 M events/day/tenant, peak 2,000/s | 100 ms / 500 ms / 1 s | 6,000 events/s per Kafka cluster | Kafka 7-day retention ≈ 1.2 TB at 1 KB × 170 M; outbox rows transient | partition key = aggregate id; 12 partitions | lag grows; no loss; `ConsumerLagHigh` at 10 s |
| Consumer processing | same | 5 ms / 50 ms / 200 ms per event | scale by partitions | dedupe table ≈ 14 M rows per group (7 days) | dedupe PK (consumer_group, event_id) | lag, not errors |
| Workflow action (approve) | 20 k/day/tenant, peak 50/s | 80 ms / 300 ms / 600 ms | 200/s | `work_item` ≈ 7 M rows/year/tenant | `work_item(tenant_id, assignee_id, status)`; monthly partitions from wave 30 | Temporal task queue backlog; UI shows "processing" |
| Inbox query | 5,000 users × 20 views/day | 20 ms / 80 ms / 150 ms | 300 rps | n/a | same index | cached 2 s per user |
| Batch run (1 M rows, 16 partitions) | 50 runs/day/tenant | n/a | ≥ 20 k rows/s aggregate | `batch_task` ≈ 300 k rows/year | `batch_run(job_id, scheduled_at desc)` | concurrency capped per tenant; runs queue by priority A > B > C |
| Email enqueue + render | 50 k messages/day/tenant, peak 100/s | 10 ms / 40 ms / 100 ms | 500/s | ≈ 18 M rows/year/tenant; bodies ≈ 100 GB/year before 90-day nulling | monthly partitions; `(tenant_id, status, next_attempt_at)` | queue grows; immediate priority served first |
| Email send (enqueue → provider accepted) | same | 5 s / 60 s / 3 min | provider limit (default 50/s per tenant) | n/a | n/a | throttled to provider limit; `NotifyQueueAge` alert |
| In-app delivery (event → tray) | same | 300 ms / 2 s / 4 s | 1,000/s | same table | SSE fan-out by user | SSE reconnect with `Last-Event-ID` |

### 0.4.20 Localization and compliance hooks

- Language: templates exist per BCP 47 language (FR-4.14); recipient language → tenant default → `en`. Wave 48 label packs ship `sys` template translations for each supported language; formatting filters (`money`, `date`, `number`) use the recipient's locale and the legal entity currency.
- Calendars and time zones: SLA timers are wall-clock in P0 (BR-4.3); wave 6 adds work calendars per legal entity so `P2D` means two working days; digest times and cron schedules use the tenant time zone (DST transitions: a 02:30 job on a spring-forward day runs at 03:00; on fall-back it runs once).
- Email compliance: CAN-SPAM / CASL / EU ePrivacy do not apply to transactional workflow mail, but every email carries a preference link and the sender's postal address from the legal entity footer when `notify.include_legal_footer=true` (default true for marketing-like digests in EU legal entities). Wave 48 adds country-specific footer content (for example German Impressum fields) through the tenant HTML layout.
- Privacy: message bodies may contain personal data; bodies are nulled after 90 days (`notify.body_retention_days`), event payloads carry user ids not addresses, and erasure requests (wave 48) redact `recipient_address` and bodies for the subject.
- Workflow evidence: approval history (Temporal + `workflow.instance`) is the SOX approval-control evidence; retention follows the record's table group (wave 5 audit) because approval actions are also audited entries.
- Data residency: Kafka and Temporal run in the tenant's region; email providers are configurable so EU tenants can choose an EU-hosted relay.

### 0.4.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX §12.29.4 Organization administration → Setup: Organization → Workflow approval email templates (register row 261) | PARTIAL-4 | ADDED as FR-4.14 | `MessageTemplate` with sys templates per workflow event and customer overrides per language |
| AX §12.30.1 System administration → Alerts → Alert parameters / Email templates / Email parameters (SMTP) / Outbound email status (`EventParameters`, `SysEmailTable`, `SysEmailParameters`) (register row 262) | PARTIAL-4 | ADDED as FR-4.13 | SMTP/provider parameters (0.4.13), templates (FR-4.14), outbound status inquiry (0.4.14); alert parameters themselves are in wave 5 (FR-5.17) |
| AX §12.30.1 System administration → Email → Email distributor batch (`SysEmailBatch`) (register row 263) | PARTIAL-4 | ADDED as FR-4.13 | distributor job every 60 s (0.4.16) |
| AX §12.30.3 System administration → Periodic: Email → Send email distributor batch / Retry failed (register row 267) | PARTIAL-4 | ADDED as FR-4.13 | distributor + BR-4.11 retries + manual `/retry` |
| AX §12.30.4 System administration → Setup: Alerts → Alert parameters (polling intervals, retention, drill-down links) / Email templates / Email parameters (SMTP, Exchange, mail sending mode: batch/client) / Email distributor (register row 269) | PARTIAL-4 | ADDED as FR-4.13 | `notify.mode` = mail sending mode; Exchange via SMTP submission or HTTP API adapter; alert polling/retention parameters in 0.5.13 (FR-5.17) |
| AX §12.31.4 Home / role centers / workflow → Setup: Workflow → Notification settings (email, in-app, digest) / Workflow email templates (register row 274) | PARTIAL-4 | ADDED as FR-4.14 | `NotificationPreference` (channels, digest, quiet hours) and per-definition notify flags; BR-4.12 |

---

## 0.5 Wave 5: UX Shell, Number Sequences, Audit & Observability

### 0.5.1 Scope and business context

AX users work in list pages and details forms generated from the AOT (form datasources, `ListPageInteraction`, action panes, FactBoxes, `SysQueryForm` advanced filters, `SysLastValue` personalization, `SysTableLookup` lookups, Infolog) and D365 workspaces, saved views and the Excel add-in; SAP users work in SAP GUI dynpros and ALV grids with layout variants, F4 search helps, SE16N, and the Fiori launchpad with catalogs and groups. Document numbering comes from AX `NumberSequenceTable` / `NumberSequenceReference` / `NumberSequenceScope` (shared, company, legal entity, fiscal calendar period, operating unit) with continuous and non-continuous modes and `NumberSequenceList` pre-allocation, or SAP `SNRO` number range objects with `NRIV` intervals, buffering (`NRIV_LOKAL`) and gap-free settings. Change tracking is AX `SysDatabaseLog` / `SysDatabaseLogSetup` (Insert/Update/Delete/RenameKey) or SAP change documents (`CDHDR`/`CDPOS`, `SCDO` objects, `DBTABLOG` table logging). Monitoring is LCS environment monitoring or Solution Manager / ST03N / SM21 / ST22. Wave 5 replaces all of this with a React module-federation shell, a metadata-driven form renderer, a number sequence service with a Jepsen-tested gap-free mode, an audit service that derives field-level history from CDC into a hash-chained append-only store, OpenTelemetry across every service into Prometheus/Tempo/Loki/Grafana, and CSV/XLSX import-export against any entity. At wave end the admin workspace runs at `app.<env>.passage.dev` and Phase 0 is demonstrated to stakeholders. Out of scope: full-text search across business documents and analytics content beyond attachment text (wave 46), report designer (wave 15), mobile PWA (wave 32), localization of labels (wave 48), fiscal-period scopes for sequences (wave 6 supplies calendars; scope type is reserved here).

#### Pain points solved (10x lens)

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| Editing 50 vendor records in AX means 50 details-form round trips (open, edit, save) or an Excel add-in session that fails on the first validation error; SAP needs an LSMW recording or XK02 one by one | Forms are record-at-a-time; bulk edit is an external tool | Grid inline edit, bulk edit of a field across ≤ 1,000 selected rows, validated per row with per-row error reporting | Clicks for 50 edits: ~300 → ~6; failed bulk sessions due to one bad row: all-or-nothing → partial with error file |
| Continuous number sequences in AX serialize on `NumberSequenceTable` for the whole transaction, causing lock waits on sales-order entry; SAP gap-free NRIV without buffering shows as `NRIV` lock contention in SM12 | Lock held for the caller's full transaction | Allocation row with reserved → used/released lifecycle; lock held only for the allocation statement | Gap-free allocation p99: 100s of ms under load → ≤ 30 ms; buffered p99 ≤ 2 ms; gaps and duplicates: 0 (Jepsen-tested) |
| AX `SysDatabaseLog` must be enabled per table/field in `SysDatabaseLogSetup` and slows every write via triggers in application code; SAP change documents exist only where the developer called `CHANGEDOCUMENT_*` function modules | Audit is opt-in application code that can be forgotten | Audit derived from CDC on every `audit=true` field; no code path can skip it; hash chain proves integrity | Fields with audit coverage: whatever developers remembered → 100% of flagged fields; write-path overhead: trigger cost → 0 |
| Tracing a slow save: AX trace parser / SQL profiler sessions, SAP ST05/SAT/STAD per work process; no end-to-end id across UI, server, DB and interfaces | No distributed trace context | W3C `traceparent` from the shell through gateway, service, Postgres, outbox, Kafka consumer and Temporal | Time to locate the slow span of one user action: hours of expert time → < 1 min from the audit entry's trace link |
| SAP GUI and AX client forms fail basic accessibility; keyboard navigation differs per form; ALV variants and `SysLastValue` are per-user and non-shareable | Forms hand-built per screen; no accessibility gate | Generated forms with ARIA grid semantics, one keyboard model, axe in CI; saved views shareable by role | WCAG 2.1 AA violations on generated forms: unknown → 0 in CI; shared views: 0 → any role |

### 0.5.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Navigation / workspaces | Navigation pane, area pages, D365 workspaces and tiles | Fiori launchpad (FLP), catalogs/groups, SAP menu (SE43) | Shell navigation by workspace, tiles bound to saved views | full |
| List page | List page framework, `ListPageInteraction`, action pane, quick filter | ALV grid (SALV), SE16N, layout variants | Generated list page with grid, filters, saved views | full |
| Details form / line grids | Details form, `FormRun`, datasources, line grid | Dynpro header/item screens, table controls | Generated detail page with header + line grids | full |
| Lookups | `SysTableLookup`, EDT relation lookups | F4 search helps (DD30L), personal value list | Generated lookup from `RelationDefinition` with recent values | full |
| Advanced filter / query | `SysQueryForm`, `Query` ranges, saved queries | Dynamic selections, SE16N selection screen | Filter builder over the list filter grammar; saved as view | full |
| Personalization | `SysLastValue`, usage data, D365 personalization | Layout variants, user parameters (SU3) | `SavedView`, `Personalization` entities per user and shared | full |
| Number sequences | `NumberSequenceTable`, `NumberSequenceReference`, `NumberSequenceScope`, `NumberSeq::newGetNum`, continuous flag | SNRO objects, NRIV intervals, buffering NRIV_LOKAL, gap-free flag | numseq service: sequence, scope, continuous/non-continuous, block pre-allocation | full |
| Database log / change documents | `SysDatabaseLog`, `SysDatabaseLogSetup`, database log viewer | CDHDR/CDPOS, SCDO, DBTABLOG (SCU3) | audit service from CDC; `AuditEntry`/`AuditField`; viewer per record | improved (hash chain) |
| Excel integration | Excel add-in (OData data entities), Open in Excel | ALV export, Analysis for Office, LSMW upload | CSV/XLSX import-export per entity with staging and validation | partial (no live Excel binding) |
| Monitoring | LCS environment monitoring, Event Viewer, SQL insights | Solution Manager, ST03N, SM21, ST22, STAD | OpenTelemetry → Grafana/Tempo/Loki/Prometheus; SLO dashboards | improved |
| Document management | Document handling (`DocuRef`, `DocuValue`, `DocuView`), Document types (`DocuType`: file/note/URL/image/template, group, location archive/database/SharePoint), File types (`DocuFileTypes`), Active document tables, Document management parameters (`DocuParameters`: archive directory, max file size) | GOS attachments (SOOD/SOFFCONT1), ArchiveLink (OAC0 content repositories, OAC3 links, TOA01), DMS document info records (CV01N, DRAW) with originals check-in/check-out, DC30 workstation applications (TDWP), conversion runs | `Attachment` / `AttachmentVersion` on any record, `DocumentType`, `FileType`, S3-compatible storage locations, check-out/check-in, previews, retention and purge, content search | full (native CAD viewing dropped: PLM owns CAD) |
| Alerts | Alert rules (`EventRule`, `EventRuleField`), change-based and due-date alerts (`EventJobCUD`, `EventJobDueDate` batch), Notification list / My alerts (`EventInbox`), Alert parameters (`EventParameters`) | Alert Management (ALRTCATDEF, ALRTPROC), S/4 Situation Handling (SIT_TEMPLATE), SWE2 change-document event → workflow, Fiori My Notifications | `AlertRule` (change-based / due-date) evaluated from CDC events and a due-date job; alert inbox in the shell; delivery via wave 4 notify | improved (event-driven, no polling batch) |
| Standard texts | Notes with document type "Note" and restriction internal/external; sales/purchase "Form notes" (`FormLetterRemarks`) | SO10 standard texts (STXH/STXL), SE75 text objects and IDs, SO72 | `StandardText` library with language versions and placeholders, referenced by templates and document layouts | full |

### 0.5.3 Functional requirements

- **FR-5.1 Shell** — Must. The system shall ship a React host (module federation, `web/shell`) that loads remotes by manifest, renders workspace navigation, a global search box (title fields of any entity the user may read, p95 ≤ 300 ms), the legal entity switcher, the work item inbox badge, and a notification tray; first contentful paint ≤ 1.5 s on a 10 Mbps connection.
  - Notes: remotes are versioned by digest and pinned per environment; the shell degrades to an error tile when a remote fails to load.
- **FR-5.2 Metadata-driven forms** — Must. The renderer shall generate, from `EntityDefinition`, a list page (grid, quick filter, filter builder, saved views, column chooser), a detail page (sections by `visibility`, tabs when > 24 fields, line grids for child relations, lookups from relations, attachments and notes panels) and dialogs for create; no entity needs hand-written UI to be usable.
  - Notes: field order from `ordinal`; `advanced` visibility collapses into a "More" section; `hidden` never renders; `pii=sensitive` renders masked with a reveal action gated by permission.
- **FR-5.3 Grid** — Must. The grid shall support virtual scrolling to 100,000 rows, inline edit with per-cell validation, multi-row select, bulk edit of one field across selected rows (≤ 1,000), column sort/resize/reorder/pin, group by one column, and export of the current view.
- **FR-5.4 Keyboard-first** — Must. Every action shall be reachable by keyboard: `/` focus search, `n` new, `Ctrl+S` save, `Ctrl+Enter` save and close, `Esc` cancel, `Ctrl+Shift+F` filter builder, arrows and `Tab` for cell navigation, `F2` edit cell, `Enter` open row, `Ctrl+Shift+V` saved views; focus order follows reading order; shortcuts are listed in a `?` overlay.
- **FR-5.5 Accessibility** — Must. All shell screens and generated forms shall pass axe-core WCAG 2.1 AA rules with zero violations in CI (Playwright + axe), support 200% zoom without horizontal scroll, and expose ARIA grid semantics.
- **FR-5.6 Validation timing** — Must. Field-level validation runs on blur (client JSON Schema, then `validateField` server call when the field has a hook); write validation runs on save; server `422 errors[]` map to fields and unresolved errors show in a summary list with focus jump.
- **FR-5.7 Saved views and personalization** — Must. Users shall save filter + sort + columns + grouping as a `SavedView` (personal or shared with a role), set one as default per entity, and have column widths/order and last legal entity persisted as `Personalization`.
- **FR-5.8 Number sequences** — Must. The numseq service shall define sequences with `code`, `scope_type` (shared, legal_entity, legal_entity_fiscal_period (reserved), operating_unit (reserved)), `format` segments (constant, number with fixed width 1–18, year `YYYY`/`YY`, legal entity code, scope value), `smallest`, `largest`, `next`, `continuous` flag, `manual` flag, `allow_to_lower`, `block_size` (non-continuous, default 50, max 1,000), `wrap` flag, and `in_use` flag; total formatted length ≤ 40.
  - Notes: references bind (entity, field, legal entity) to a sequence, mirroring `NumberSequenceReference`; `numseq(<code>)` is a valid field default from wave 3.
- **FR-5.9 Gap-free and buffered allocation** — Must. Continuous sequences shall allocate inside the caller's transaction with a `SELECT … FOR UPDATE` on the sequence/scope row and record an `Allocation` row (status reserved → used | released); a rollback releases the number to be reused by the next caller; p99 ≤ 30 ms at 500 rps per sequence. Non-continuous sequences shall pre-allocate a block per service instance and serve from memory, p99 ≤ 2 ms; unused numbers in a block are lost on restart (documented, like `NRIV_LOKAL`).
- **FR-5.10 Audit capture** — Must. The audit service shall consume Debezium change events for every table whose entity or field has `audit=true`, and write `AuditEntry` (operation insert/update/delete, actor from `updated_by`, `trace_id` from the transaction's outbox header or `pg_current_xact_id` correlation, `occurred_at`) and `AuditField` rows for changed fields only, with `pii=sensitive` values stored as SHA-256 digests plus masked display.
  - Notes: entry visible in the viewer within 2 s (p95) of commit; the audit database is `audit`, separate from domain databases; role `passage_app` has INSERT and SELECT only.
- **FR-5.11 Immutable hash chain** — Must. Each `AuditEntry` shall store `prev_hash` (previous entry for the same tenant) and `hash = SHA-256(prev_hash ‖ canonical_json(entry + fields))`; a daily job writes the chain head per tenant to an anchor table and to object storage; `GET …/verify` recomputes a range.
- **FR-5.12 Audit viewer** — Must. From any record the user shall open "History" showing entries with user, time, trace id, operation and before/after per field, filterable by field and date, exportable; a global audit workspace searches by entity, record, user, date range.
- **FR-5.13 OpenTelemetry** — Must. Every service shall emit traces (W3C `traceparent` propagated gateway → service → Postgres spans → outbox header → Kafka consumer → Temporal activities), RED metrics (`http_server_requests`, `db_query_duration`, `kafka_consume_lag`, `temporal_activity_duration`) and structured JSON logs with `trace_id`, `tenant_id`, `actor`; the shell emits browser spans for navigation and API calls.
  - Notes: sampling 100% in dev, tail-based 10% + 100% of errors and > 1 s in stage/prod; PII never in span attributes (lint rule).
- **FR-5.14 Dashboards and alerting** — Must. Grafana shall ship per-service SLO dashboards (availability, latency, error budget burn), platform dashboards (Kafka lag, Temporal, Postgres, Argo), and Alertmanager routes to a PagerDuty-compatible webhook with severities page/ticket/notify.
- **FR-5.15 Import/export** — Must. Users shall import CSV or XLSX into any entity through staging (header mapping saved as a template per entity, type coercion, validation using the entity's JSON Schema and hooks, error file with row and field) and export any list view to CSV/XLSX respecting RLS, permissions and PII masks; 100,000 rows import ≤ 5 min; runs as a wave-4 batch job.
- **FR-5.16 Document management** — Must. The system shall attach documents to any record of any entity whose metadata sets `attachments: true` (default true for main and transaction table groups), and one document may be linked to many records. Each attachment has a `DocumentType` (code, kind file | note | url | image | template, group, storage location, allowed file types, max size, restriction internal | external, retention class, `active_entities[]`), versions (`AttachmentVersion`: file, sha256, size, mime, uploaded by/at), and an optional check-out lock. Storage locations are S3-compatible buckets (per tenant prefix; credentials via ESO) or `database` for notes; uploads use pre-signed URLs (≤ 100 MB default, `FileType.max_bytes` per type); every upload is malware-scanned before it becomes `available`; previews (PDF first page, image thumbnail 320 px, Office documents converted to PDF) are generated by a conversion job; text is extracted from PDF, Office and text files into a Postgres `tsvector` so global search (FR-5.1) and the documents inquiry find attachment content.
  - Notes: check-out sets `checked_out_by/at` and blocks new versions by others until check-in or an administrator force-release (lock expires after 7 days); `external` restriction marks documents that may print or send with external documents (wave 15/46 layouts); retention classes (`retain_years`, `on_expiry` purge | archive) drive the purge/archive job, which never purges a document under legal hold (flag set by wave 48) or linked to a record whose own retention has not expired; deleting a record leaves its attachments for the retention job; `active_entities[]` reproduces AX "Active document tables"; native CAD 2D/3D viewing is not provided (PLM systems keep CAD and link by URL document type).
- **FR-5.17 Alert rules** — Must. Users shall create `AlertRule`s on any entity the user may read: `change-based` (field changes to / from a value, any change of a field, record created, record deleted; optional filter in the list filter grammar) or `due-date` (a date field is within N days before/after today, evaluated daily); with subject and message from a wave 4 `MessageTemplate` or inline text with placeholders, recipients (self, users, role, record owner field), delivery (in-app, email, both), legal entity scope (current or all), active from/to, and "organization-wide" rules (maintained by an administrator for all users of a role). Fired alerts appear in the shell Alerts list with a drill-down link to the record and can be marked read or deleted.
  - Notes: change-based rules evaluate from the CDC stream already consumed by audit (no polling job; AX change-based batch is replaced); due-date rules evaluate in a daily job at `alerts.due_date_time` tenant time; the recipient must hold read on the entity at evaluation time, otherwise the alert is dropped and counted; a rule firing more than `alerts.max_per_rule_per_hour` (default 500) is suspended and its owner notified (ALR-0505); alerts are retained `alerts.retention_days` (default 90).
- **FR-5.18 Standard texts** — Should. The system shall provide a `StandardText` library (code, purpose: document header / document footer / line / email / workflow, language versions, body in plain or limited Markdown with placeholders `{{record.field}}`, legal entity scope, effective dates, layer sys | customer) that wave 4 message templates reference as `{{text:CODE}}`, users insert into notes on any record (expanded at insert time), and wave 15/46 document layouts reference by code with the recipient's language.
  - Notes: language fallback recipient → legal entity default → `en`; a text referenced by a published template or layout cannot be retired (STX-0503); edits create a new version, documents already issued keep the version they printed (layouts store the version id).

### 0.5.4 Data model

**NumberSequence** (`numseq.sequence`) and **NumberSequenceAllocation** (`numseq.allocation`)

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| tenant_id | uuid | yes | RLS |
| code | varchar(20) | yes | unique per tenant (`Vend_Acct`, `SO_Num`) |
| scope_type | enum | yes | shared, legal_entity, legal_entity_fiscal_period, operating_unit |
| scope_value | varchar(32) | no | legal entity code etc.; unique with code |
| format | jsonb | yes | segments; e.g. `[{"const":"SO-"},{"year":"YYYY"},{"const":"-"},{"number":6}]` |
| smallest, largest, next | bigint | yes | 1 ≤ smallest ≤ next ≤ largest ≤ 10^18 |
| continuous, manual, allow_to_lower, wrap, in_use | bool | yes | defaults false except in_use |
| block_size | int | yes | 1 for continuous; 50 default |
| version | int | yes | optimistic lock |
| Allocation: id, sequence_id, number, status (reserved, used, released), tx_ref, requested_by, requested_at, used_at | — | — | only for continuous sequences |

**AuditEntry** (`audit.entry`, partitioned monthly) and **AuditField** (`audit.field`)

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| tenant_id, legal_entity_id | uuid | yes/no | |
| entity | varchar(48) | yes | |
| record_id | uuid | yes | |
| operation | enum | yes | insert, update, delete |
| actor_id | uuid | yes | from updated_by / created_by; `system` for jobs |
| occurred_at | timestamptz | yes | commit time |
| trace_id | char(32) | no | W3C trace id |
| tx_id | bigint | yes | Postgres xid for grouping |
| seq | bigint | yes | per-tenant monotonic |
| prev_hash, hash | char(64) | yes | SHA-256 hex |
| AuditField: entry_id, field, old_value jsonb, new_value jsonb, pii enum, digest char(64) | — | — | old/new null when masked |

**SavedView** (`ui.saved_view`) and **Personalization** (`ui.personalization`)

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| tenant_id, owner_id | uuid | yes | |
| entity | varchar(48) | yes | |
| name | varchar(60) | yes | unique per owner + entity |
| filter, sort, columns, group_by | jsonb | yes/no | list grammar from FR-3.6 |
| shared_with_role_id | uuid | no | null = personal |
| is_default | bool | yes | one per owner + entity |
| version | int | yes | |
| Personalization: user_id, key (varchar 80), value jsonb, updated_at | — | — | e.g. `grid.Vendor.columns`, `session.legal_entity` |

**ImportJob** (`io.import_job`): id, tenant_id, entity, template_id, file_ref, status (uploaded, validating, validated, loading, completed, failed, canceled), rows_total, rows_ok, rows_failed, error_file_ref, batch_run_id, created_by, created_at.

**Attachment / AttachmentVersion / AttachmentLink** (`docs.attachment`, `docs.attachment_version`, `docs.attachment_link`)

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK (each table) |
| tenant_id, legal_entity_id | uuid | yes / no | RLS |
| document_type_id | uuid | yes (Attachment) | FK DocumentType |
| name, description | varchar(255), text | yes / no | |
| restriction | enum | yes | internal, external |
| status | enum | yes | uploading, scanning, available, quarantined, archived, purged |
| current_version_id | uuid | no | FK AttachmentVersion |
| checked_out_by, checked_out_at | uuid, timestamptz | no | lock (FR-5.16) |
| legal_hold | bool | yes | default false; set by wave 48 |
| retain_until | date | no | computed from retention class and linked record |
| note_body | text | no | kind=note (location database) |
| url | varchar(2,000) | no | kind=url |
| Version: attachment_id, version_no, storage_key, sha256, size_bytes, mime, preview_key, extracted_text tsvector, uploaded_by, uploaded_at | — | — | storage_key = `t/<tenant>/docs/<attachment>/<version>` |
| Link: attachment_id, entity, record_id, linked_by, linked_at | — | — | unique (attachment_id, entity, record_id) |

**DocumentType / FileType** (`docs.document_type`, `docs.file_type`)

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| tenant_id | uuid | yes | `sys` defaults: File, Note, URL, Image, Scanned invoice |
| code | varchar(20) | yes | unique per tenant |
| kind | enum | yes | file, note, url, image, template |
| group | enum | yes | file, note, url, image, other |
| storage_location | enum | yes | object_storage, database |
| bucket_ref | varchar(120) | no | ESO secret path for a customer bucket; default platform bucket |
| allowed_file_types | text[] | no | FileType codes; empty = all active |
| max_bytes | bigint | yes | default 104,857,600 |
| retention_years, on_expiry | int, enum | yes | default 10 / archive; purge, archive |
| active_entities | text[] | no | empty = all entities with `attachments: true` |
| FileType: code (extension), mime, max_bytes, preview (pdf_first_page, thumbnail, office_to_pdf, none), extract_text bool, blocked bool | — | — | blocked defaults: exe, bat, js, msi, ps1, scr |

**AlertRule / AlertEvent** (`ui.alert_rule`, `ui.alert_event`)

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| tenant_id | uuid | yes | |
| owner_id | uuid | yes | creator |
| entity | varchar(48) | yes | |
| kind | enum | yes | change_based, due_date |
| event | enum | yes | field_changed, field_changed_to, field_changed_from, record_created, record_deleted, due_in, overdue_by |
| field, value | varchar(48), jsonb | no | per event |
| days | int | no | due-date offset 0–365 |
| filter | jsonb | no | list grammar (FR-3.6) |
| recipients | jsonb | yes | `{self, user_ids[], role_ids[], owner_field}` |
| delivery | enum set | yes | in_app, email |
| template_code, subject, message | varchar(64), varchar(255), text | no | template or inline |
| legal_entity_scope | enum | yes | current, all |
| org_wide | bool | yes | administrator-maintained |
| active_from, active_to, status | date, date, enum | yes/no/yes | active, suspended, expired |
| Event: rule_id, entity, record_id, recipient_id, fired_at, subject, message, read_at, message_id (wave 4 OutboundMessage) | — | — | retained 90 days |

**StandardText** (`ui.standard_text`): id, tenant_id, code (varchar 32, unique per tenant/version), purpose (enum), language (BCP 47), version, body (text ≤ 8,000), legal_entity_id (nullable = all), effective_from, effective_to, layer (sys, customer), status (draft, published, retired).

Invariants: `next ≤ largest` unless `wrap`; for continuous sequences the set of `used` allocation numbers has no gaps between `smallest` and `next−1`; `hash` of entry n equals SHA-256(prev_hash of n ‖ canonical(n)) and `prev_hash` of n+1 equals `hash` of n; one default SavedView per (owner, entity). Indexing: `sequence(tenant_id, code, scope_value)` unique; `allocation(sequence_id, number)` unique; `entry(tenant_id, entity, record_id, occurred_at desc)`; `entry(tenant_id, seq)` unique; `saved_view(tenant_id, entity, owner_id)`. Partitioning: `audit.entry` and `audit.field` by month; retention 10 years for `transaction` table-group entities, 3 years otherwise, enforced by partition detachment to object storage from wave 46.

### 0.5.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| GET/POST/PUT | /v1/numseq/sequences, /{id} | Maintain sequences and references | PUT: If-Match | Maintain number sequences |
| POST | /v1/numseq/sequences/{code}/next?scope= | Allocate (continuous: inside caller tx via `tx_ref`) | `Idempotency-Key` returns the same number | internal / per entity create |
| POST | /v1/numseq/allocations/{id}/use, /release | Finalize or release a continuous allocation | allocation id | internal |
| GET | /v1/audit/entries?entity=&record_id=&actor=&from=&to= | Query history | n/a | View audit log (or owner of record) |
| GET | /v1/audit/entries/{id} | Entry with fields | n/a | View audit log |
| POST | /v1/audit/verify?tenant=&from_seq=&to_seq= | Recompute chain | n/a | View audit log |
| GET/POST/PUT/DELETE | /v1/ui/views, /{id} | Saved views | PUT: If-Match | Maintain saved views (own) |
| GET/PUT | /v1/ui/personalization/{key} | Personalization | last write wins | self |
| GET | /v1/ui/forms/{entity} | Form definition (ETag) | n/a | any |
| GET | /v1/ui/search?q=&limit= | Global search | n/a | any (results filtered by read permission) |
| POST/GET | /v1/io/imports, /{id}, /{id}/validate, /{id}/load, /{id}/errors | Import lifecycle | POST: file sha256 | Import data |
| POST | /v1/io/exports | Export a view (async, returns file ref) | request id | Export data |
| POST | /v1/docs/attachments (returns pre-signed upload URL), /{id}/versions, /{id}/links | Create attachment, add version, link to another record | Idempotency-Key; version: sha256 | entity update + Maintain documents |
| GET | /v1/docs/attachments?entity=&record_id=&type=&q= | List by record, type, or content search | n/a | entity read |
| GET | /v1/docs/attachments/{id}/content?version=, /preview | Pre-signed download (5 min), preview | n/a | entity read; `external` restriction irrelevant for read |
| POST | /v1/docs/attachments/{id}/checkout, /checkin, /release | Lock management | attachment version | Maintain documents (release: Administer document management) |
| GET/POST/PUT | /v1/docs/document-types, /v1/docs/file-types, /v1/docs/parameters | Setup | PUT: If-Match | Administer document management |
| GET/POST/PUT/DELETE | /v1/alerts/rules, /{id} | Maintain alert rules (own; org-wide needs admin duty) | PUT: If-Match | Maintain alert rules / Administer alerts |
| GET/POST | /v1/alerts/events?unread=, /{id}/read, /{id}/delete | Alert inbox | event id | self |
| GET/PUT/POST | /v1/texts, /{code}/versions/{v}, /publish, /render?language=&record= | Standard texts | by version | Maintain standard texts (render: any) |

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| dev.passage.numseq.number.allocated.v1 | continuous allocation `used` or `released` | sequence_code, scope_value, number, status, tx_ref, actor | audit |
| dev.passage.audit.entry.recorded.v1 | entry committed | entry_id, tenant_id, entity, record_id, operation, hash, seq | shell live history, analytics (wave 46) |
| dev.passage.audit.chain.anchored.v1 | daily anchor | tenant_id, seq, hash, anchor_ref | compliance evidence (wave 48) |
| dev.passage.io.import.completed.v1 / failed.v1 | import terminal | import_id, entity, rows_ok, rows_failed, error_file_ref | shell inbox, audit |
| dev.passage.ui.view.shared.v1 | saved view shared | view_id, entity, role_id, owner | shell cache |
| dev.passage.docs.attachment.available.v1 / quarantined.v1 | scan outcome | attachment_id, version_no, document_type, links[], sha256, size | shell (refresh panel), audit, OCR capture (wave 18) |
| dev.passage.docs.attachment.purged.v1 / archived.v1 | retention job | attachment_id, retention class, reason | audit, compliance evidence (wave 48) |
| dev.passage.alerts.alert.fired.v1 | rule match | rule_id, entity, record_id, recipient_id, delivery | notify (wave 4), shell alert badge |
| dev.passage.texts.standard-text.published.v1 | publish | code, language, version | notify template cache, layouts (wave 46) |

### 0.5.6 User experience

| Screen | Primary actions | Key fields/columns | Duty required |
|---|---|---|---|
| Admin workspace (home) | Tiles: users, roles, entities, extensions, workflows, jobs, sequences, audit, imports | counts, failed runs, pending work items | per tile duty |
| Generated list page (any entity) | New, open, inline edit, bulk edit, filter, save view, export, history | columns from metadata, status, updated | entity read/update |
| Generated detail page | Save, save and close, delete, open related, history, attachments | sections, tabs, line grids, lookups | entity actions |
| Number sequences | Create, set format, test next, mark in use | code, scope, format preview, next, continuous | Maintain number sequences |
| Audit viewer (record and global) | Filter, verify chain, export | time, actor, operation, field, old, new, trace link | View audit log |
| Import/export | Upload, map, validate, load, download errors | entity, template, rows, status | Import data / Export data |
| Observability (Grafana, linked) | Open trace from any record history; SLO dashboards | trace id, service, latency, error budget | Platform engineer |
| Attachments panel (on any detail page) | Upload (drag-drop), new version, check out/in, link existing, preview, download, delete link | name, type, version, size, uploaded by/at, checked out by, restriction | entity read/update; Maintain documents |
| Documents workspace | Search by type, record, content; bulk re-link; release locks; view quarantined | name, type, linked records, versions, status, retain until, legal hold | View documents / Administer document management |
| Document management parameters | Document types, file types, storage locations, max sizes, retention classes | code, kind, location, max size, retention | Administer document management |
| Alerts (shell bell) | Open record, mark read, delete, create rule from a field ("Notify me when…") | fired at, rule, record, message | self |
| Alert rules | Create, edit, suspend, test against a sample record; org-wide rules | entity, event, field, recipients, delivery, active dates, status, fires/24 h | Maintain alert rules / Administer alerts |
| Standard texts | Create, translate, preview with record, publish | code, purpose, language, version, used by | Maintain standard texts |

Rules: inline edit commits per row on `Enter` or row blur with optimistic locking (409 shows a diff and lets the user reload or overwrite with permission); bulk edit previews affected rows and runs as a batch job above 200 rows; validation messages appear under the field and in a summary list; personalization never changes required-field visibility; the trace link from the audit viewer deep-links into Tempo with the `trace_id`.

### 0.5.7 Business rules and edge cases

- **BR-5.1** Continuous allocation lifecycle: reserved → used (on caller commit) | released (on caller rollback or 10-minute reservation timeout); a released number is the next number served, preserving gap-freedom; `next` only advances when no released numbers exist.
- **BR-5.2** Two concurrent continuous requests serialize on the row lock; the lock is held only for the allocation statement, not the caller's whole transaction, which is why `Allocation` rows exist.
- **BR-5.3** Non-continuous blocks are per pod; a pod restart loses at most `block_size − 1` numbers per sequence; `allow_to_lower=false` forbids editing `next` downward.
- **BR-5.4** Year segment is evaluated in the legal entity's time zone at allocation time; a sequence with a year segment and `continuous=true` resets `next` to `smallest` on 1 January only when `reset_on_year=true` (default false).
- **BR-5.5** `in_use=false` sequences reject allocation with 409 `SEQUENCE_NOT_IN_USE`; `manual=true` sequences require the caller to supply the number and enforce uniqueness per scope.
- **BR-5.6** Reaching `largest` with `wrap=false` returns 409 `SEQUENCE_EXHAUSTED` and raises an alert at 90% consumption.
- **BR-5.7** Audit entries are never updated or deleted by the application; a verification mismatch raises `AuditChainBroken` (page severity) and freezes further anchoring until acknowledged.
- **BR-5.8** Updates that change only `updated_at`/`version` produce no audit entry; updates to `audit=false` fields produce an entry with no `AuditField` rows only if another audited field changed.
- **BR-5.9** Deleting a record writes an entry with all audited fields as `old_value`; soft delete is an update with field `deleted_at`.
- **BR-5.10** Saved view sharing: a shared view is read-only to others; copying creates a personal view; deleting a shared default view reverts affected users to the system default.
- **BR-5.11** Import loads are all-or-nothing per file by default; `partial=true` loads valid rows and produces the error file for the rest; re-importing the same file sha256 is a no-op with 200 and the previous import id.
- **BR-5.12** Attachment lifecycle: uploading → scanning → available | quarantined; available → archived (retention `on_expiry=archive`: object moved to the archive storage class, metadata kept) → purged; available → purged (retention `purge`). A version upload while `checked_out_by ≠ caller` returns 423 DOC-0504. `retain_until` = max over linked records of (record retention per its table group, wave 5 audit retention) and the document type's `retention_years` from upload; legal hold blocks archive and purge regardless.
- **BR-5.13** Content dedupe: an upload whose sha256 equals an existing version in the same tenant stores no new object (the version points to the existing key); a purge deletes the object only when no version references it.
- **BR-5.14** Change-based alert evaluation: a rule fires once per (rule, record, committed transaction) when its event matches the before/after values in the CDC change and the record passes `filter` after the change; changes by the alert's own recipient fire too unless `exclude_own_changes=true` (default true); due-date rules fire once per (rule, record, due date value) so re-running the daily job never duplicates; a rule on a field the recipient cannot read (field mask) is rejected at save (ALR-0502).
- **BR-5.15** Standard text expansion: placeholders resolve against the record at insert or render time; an unknown placeholder fails publish (STX-0502); a text with `effective_to` in the past is not offered for insertion but still renders for documents that reference its version.

### 0.5.8 Acceptance criteria

- **AC-5.1** Given the 10 reference entities plus 5 extension cases, When Playwright + axe runs list, detail and create dialogs, Then 0 WCAG 2.1 AA violations and the keyboard suite completes every action without a mouse. (FR-5.2, FR-5.4, FR-5.5)
- **AC-5.2** Given a continuous sequence and 10,000 concurrent allocation requests with 10% random rollbacks (Jepsen-style with clock skew and partition injection), When the run ends, Then used numbers are contiguous, unique and `next − smallest = count(used)`. (FR-5.9, BR-5.1)
- **AC-5.3** Given a non-continuous sequence under 2,000 rps across 3 pods, When measured, Then p99 ≤ 2 ms and no duplicates. (FR-5.9)
- **AC-5.4** Given a field change on any audited entity, When committed, Then the audit viewer shows user, time, old/new and the trace id within 2 s (p95 over 1,000 changes). (FR-5.10, FR-5.12)
- **AC-5.5** Given a tampered `AuditField.new_value` (edited directly in the DB in test), When `verify` runs, Then it reports the first broken seq and `AuditChainBroken` fires. (FR-5.11, BR-5.7)
- **AC-5.6** Given a click "Save" on a vendor stub, When the request flows, Then one trace in Tempo contains spans for gateway, service, Postgres, outbox publish, Kafka consumer (audit) and the shell. (FR-5.13)
- **AC-5.7** Given a CSV of 100,000 vendor stubs with 1% invalid rows, When imported with `partial=true`, Then 99,000 rows load in ≤ 5 min and the error file lists 1,000 rows with field and code. (FR-5.15)
- **AC-5.8** Given a user saves a view with filter and columns and marks it default, When they reopen the list, Then the view applies; another user in the shared role sees it read-only. (FR-5.7, BR-5.10)
- **AC-5.9** Given the SLO dashboard for `identity`, When error rate is forced to 2% for 10 min in stage, Then `SLOBurnRateHigh` reaches the webhook with severity page. (FR-5.14)
- **AC-5.10** Given a PDF uploaded to a VendorStub record with document type "Scanned invoice", When the upload completes, Then the attachment is `available` within 30 s with a first-page preview, its text is found by global search, and linking the same attachment to a second record shows it on both. (FR-5.16)
- **AC-5.11** Given the EICAR test file uploaded with an allowed extension, When scanning completes, Then the attachment is `quarantined`, no download URL is issued, and DOC-0503 is shown. (FR-5.16, BR-5.12)
- **AC-5.12** Given user A has checked out an attachment, When user B uploads a new version, Then 423 DOC-0504 names A; after A checks in, B's upload succeeds as version n+1. (FR-5.16, BR-5.12)
- **AC-5.13** Given a document type with `retention_years=1, on_expiry=purge` and one attachment under legal hold, When the retention job runs past expiry, Then the unheld attachment's object is deleted and `attachment.purged` is emitted, while the held one remains `available`. (FR-5.16, BR-5.12)
- **AC-5.14** Given a change-based rule "VendorStub.status changes to Blocked, notify role AP clerk by in-app + email", When a vendor is blocked, Then each role member with read access receives one alert in the bell within 5 s and one email via wave 4 notify, and the user who made the change does not. (FR-5.17, BR-5.14)
- **AC-5.15** Given a due-date rule "due_date within 3 days" on 1,000 records, When the daily job runs twice on the same day, Then each qualifying record fires exactly once per recipient. (FR-5.17, BR-5.14)
- **AC-5.16** Given a standard text `PAY_TERMS_FOOTER` in `en` and `de-DE` referenced as `{{text:PAY_TERMS_FOOTER}}` in a workflow template, When rendered for a German-language recipient, Then the German version appears with placeholders resolved, and retiring the text is refused with STX-0503. (FR-5.18, BR-5.15)

### 0.5.9 Test plan and fixtures

Fixtures: `fixtures/numseq/jepsen-config.yaml` (10k clients, nemesis schedule), `fixtures/audit/tamper-cases.sql`, `fixtures/io/vendors-100k.csv` (1% invalid), `fixtures/ui/keyboard-script.json` (every shortcut), `fixtures/ui/entities-15.yaml` (10 reference + 5 extension variants), `fixtures/otel/trace-expectation.json` (span list for AC-5.6).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| axe + keyboard | E2E (Playwright) | shell + renderer | 0 violations, 100% shortcuts |
| Gap-free Jepsen | Property / chaos | numseq | 0 gaps, 0 dups under partitions and skew |
| Buffered throughput | Performance | numseq | p99 ≤ 2 ms @ 2,000 rps |
| Audit latency | Integration | CDC → audit | p95 ≤ 2 s |
| Chain verify | Negative | audit | tamper detected at exact seq |
| Trace continuity | Integration | OTel pipeline | all expected spans share one trace id |
| Import 100k | Performance | importexport | ≤ 5 min, error file correct |
| Form renderer snapshot | Snapshot | renderer | 15 entities × 3 pages stable |
| Grid virtualization | Performance (browser) | grid | 100k rows scroll at ≥ 50 fps on reference laptop |

Reconciliation: for each continuous sequence, `count(used allocations) = next − smallest − count(released outstanding)`; audit entries per table per day equal Debezium change count for audited tables; import `rows_ok + rows_failed = rows_total`.

### 0.5.10 Deployment and operations

Charts: `shell-web` (static assets behind the gateway, remotes manifest in ConfigMap), `forms-renderer` (remote), `shell-bff` (TS: views, personalization, search, SSE), `numseq` (Go, CNPG `numseq`), `audit` (Go consumer + API, CNPG `audit`, monthly partition job), `importexport` (TS, object storage bucket via ESO credentials), `observability` umbrella (OTel Collector DaemonSet + gateway, Prometheus, Tempo, Loki, Grafana, Alertmanager). Config: `REMOTES_MANIFEST_URL`, `OTEL_TRACES_SAMPLER=parentbased_traceidratio`, `AUDIT_RETENTION_YEARS`, `NUMSEQ_RESERVATION_TIMEOUT_SECONDS=600`, `ALERT_WEBHOOK_URL` (ESO). Migrations: additive; `audit.entry` partitions created 3 months ahead by a scheduled job. Feature flags: `ui.inline_edit`, `ui.bulk_edit`, `audit.capture` per domain. Scheduled jobs: audit anchor 00:30 UTC, partition maintenance weekly, sequence exhaustion check hourly, import staging cleanup after 7 days. SLOs: shell availability 99.9%, form load p95 ≤ 800 ms, numseq continuous p99 ≤ 30 ms, audit visibility p95 ≤ 2 s, trace ingestion loss < 0.1%. Alerts: `SLOBurnRateHigh` (multi-window 1h/6h), `AuditLagHigh` (> 10 s), `AuditChainBroken`, `SequenceNearExhaustion`, `RemoteLoadFailure`, `OTelCollectorDropping`. Runbook: audit consumer rebuild from Kafka retention, sequence reservation stuck, remote rollback (manifest pin), Grafana datasource auth. Rollback: digest rollback per chart; shell remotes roll back by manifest pin without redeploying the host.

### 0.5.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| AX `NumberSequenceTable` (NumberSequence, Txt, Format, Lowest, Highest, NextRec, Continuous, Manual, InUse, Wrap) + `NumberSequenceScope` | NumberSequence | `Format` `###-######` converted to segments; `NextRec` → `next` after cutover freeze; scope `DataAreaId` → legal entity code |
| AX `NumberSequenceReference` (DataTypeId → NumberSequenceId) | sequence reference | EDT name mapped to Passage entity/field by crosswalk |
| SAP NRIV (OBJECT, SUBOBJECT, NRRANGENR, FROMNUMBER, TONUMBER, NRLEVEL, EXTERNIND) | NumberSequence | SUBOBJECT (BUKRS) → scope value; EXTERNIND='X' → manual; buffering settings from TNRO → block_size |
| AX `SysDatabaseLog` (table, recId, logType, createdBy, createdDateTime, data container) | AuditEntry/AuditField (read-only import) | loaded as historical entries with `source=ax`, excluded from the hash chain (separate chain `legacy`) |
| SAP CDHDR/CDPOS (OBJECTCLAS, OBJECTID, CHANGENR, USERNAME, UDATE, UTIME, TABNAME, FNAME, VALUE_OLD, VALUE_NEW) | AuditEntry/AuditField | same treatment; OBJECTID resolved to Passage record ids via the migration run ledger (wave 10) |
| D365 saved views / AX `SysLastValue` usage data; SAP ALV layout variants (LTDX) | SavedView | optional; only named, shared variants are imported |
| AX `DocuRef` (RefTableId, RefRecId, TypeId, Name, Notes, Restriction) + `DocuValue` (FileName, FileType, Path / File container) + `DocuType` | Attachment / AttachmentVersion / AttachmentLink / DocumentType | file content copied from the archive directory, database container or SharePoint location to object storage; RefTableId/RefRecId resolved via the wave 10 run ledger; notes → kind=note |
| SAP GOS (SRGBTBREL, SOOD, SOFFCONT1), ArchiveLink (TOA01–TOA03, TOAAT), DMS (DRAW, DRAD object links, originals in KPro) | same | content fetched via `SO_DOCUMENT_READ_API1` / ArchiveLink HTTP or KPro export; DRAD links → AttachmentLink; DMS versions → AttachmentVersion |
| AX `EventRule` (alert rules) | AlertRule | change-based and due-date rules mapped when the table/field crosswalk resolves; others reported unmapped |
| SAP SO10 texts (STXH/STXL via `READ_TEXT`, TDOBJECT=TEXT) | StandardText | per language; SAPscript formatting reduced to plain text with line breaks |

Not migrated: AX Infolog history, SAP SM21/ST22 logs, SAP DBTABLOG (table logging) beyond customizing tables, personal (unnamed) usage data. Number sequence `next` values are taken at cutover (wave 49) and never earlier.

### 0.5.12 Dependencies, risks, open questions

Depends on: wave 4 (CDC stream for audit, batch jobs for import/export, events for inbox), wave 3 (form definitions from metadata, `numseq()` defaults), wave 2 (duties `Maintain number sequences`, `View audit log`, `Import data`, `Export data`, `Maintain saved views`), wave 1 (gateway, TLS, charts).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Generated forms are usable but ugly; stakeholders judge the platform by them | High | Medium | Design tokens and a 15-entity visual review before the demo; hand-built workspaces for admin home |
| Audit consumer falls behind on bulk loads | Medium | Medium | Partitioned consumer group (12), batching 500 rows per commit, `AuditLagHigh` alert |
| Gap-free sequences used where not required (throughput) | Medium | Medium | Default non-continuous; continuous requires a documented legal reason in the sequence description |
| Tail sampling hides a rare slow path | Low | Medium | 100% sampling of > 1 s and errors; exemplar links from metrics |

Open questions: (1) Grid implementation (in-house vs. open-source data grid library) — default open-source MIT grid with virtualization; decide by wave 5 start. (2) Audit anchor target (object storage only vs. external timestamping) — default object storage with WORM policy; decide by wave 48. (3) SSE vs WebSocket for the inbox — default SSE; decide by wave 5 mid-point. (4) Whether `legal_entity_fiscal_period` scope ships in wave 5 or wave 6 — default wave 6 with the enum reserved now; decide by wave 6 start.

### 0.5.13 Parameters and setup

Values live on Admin workspace → Shell parameters, Number sequence parameters, Audit parameters, Document management parameters, Alert parameters and Import/export parameters (AX "Number sequence parameters", "Database log setup", "Document management parameters", "Alert parameters", "System parameters"; SAP SNRO/TNRO, SCDO/SCU3, OAC0/DC10, SM19 and SPRO → SAP NetWeaver → General settings).

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| `ui.inline_edit`, `ui.bulk_edit` | tenant (feature flags) | bool | true, true | true/false | grid editing modes (FR-5.3) | Form personalization "Edit mode" (System parameters → default view/edit mode) | ALV edit mode (layout) |
| `ui.bulk_edit.max_rows`, `ui.bulk_edit.batch_threshold` | tenant | int, int | 1,000, 200 | 10–10,000, 10–1,000 | bulk edit cap; above threshold runs as batch job | Excel add-in row limits | MASS transaction selection limits |
| `ui.grid.page_fetch_rows` | environment | int | 200 | 50–1,000 | virtual-scroll fetch size | `FormDataSource` cache size | ALV `maxrows` |
| `ui.search.entities[]`, `ui.search.max_results` | tenant | entity list, int | all with title fields, 20 | — | global search scope (FR-5.1) | Search configuration (SysSearchConfiguration) | ESH_COCKPIT search models |
| `ui.user_defaults` (language, date format, number format, time zone, default legal entity, start workspace) | user (tenant default) | jsonb | tenant defaults | locale codes, IANA tz | My user options / personalization | User options → Preferences (SysUserSetup) | SU3 defaults (date format, decimal notation, time zone) |
| `numseq.reservation_timeout_seconds` | environment | int | 600 | 60–3,600 | BR-5.1 release of unconfirmed numbers | Number sequence parameters → "Clean up" interval (continuous) | none (NRIV commit-bound) |
| `numseq.block_size_default` | tenant | int | 50 | 1–1,000 | non-continuous pre-allocation | Number sequence → Performance → Preallocation quantity | SNRO → number of numbers in buffer (TNRO-NOIVBUFFER) |
| `numseq.exhaustion_warn_pct` | tenant | int | 90 | 50–99 | `SequenceNearExhaustion` alert | Number sequence → "Warning at" | SNRO → warning % (TNRO-PERCENTAGE) |
| `numseq.reset_on_year` | sequence | bool | false | true/false | BR-5.4 | Segment configuration with fiscal calendar period scope | year-dependent intervals (NRIV-TOYEAR), OBH1 copy to fiscal year |
| `audit.capture` | domain (flag) | bool | true | true/false | CDC audit capture on/off per domain | Database log setup (SysDatabaseLogSetup) per table | SCDO change document objects; table logging `rec/client`, SE13 log flag |
| `audit.retention_years.transaction`, `.other` | tenant | int, int | 10, 3 | 1–30 | partition retention (0.5.4) | Database log cleanup (SysDatabaseLogCleanup) | SCU3 / SARA CHANGEDOCU archiving |
| `audit.anchor_time_utc` | environment | HH:MM | 00:30 | any | daily hash anchor (FR-5.11) | none | none |
| `docs.max_upload_bytes` | tenant | bigint | 104,857,600 | ≤ 2 GB | upload ceiling across types | Document management parameters → Maximum file size | OAC0 repository settings, `SO_DOCUMENT` size limits |
| `docs.default_bucket_ref`, `docs.archive_storage_class` | tenant | ESO path, enum | platform bucket, `GLACIER_IR`-equivalent cold class | provider classes | storage location and archive tier | Document management parameters → Archive directory; DocuType location (archive / database / SharePoint) | OAC0 content repositories, OACT categories |
| `docs.blocked_extensions` | tenant | text[] | exe, bat, cmd, js, msi, ps1, scr, vbs | any | uploads refused (DOC-0502) | File types (DocuFileTypes) | DC30 workstation applications (TDWP) |
| `docs.checkout_lock_days` | tenant | int | 7 | 1–90 | auto-release of stale check-outs | none | CV01N check-out status (no expiry) |
| `docs.preview.enabled`, `docs.extract_text.enabled` | tenant | bool, bool | true, true | true/false | conversion and content indexing | none (AX 2012); D365 Office preview | DMS conversion server, TREX/HANA full-text activation |
| `alerts.due_date_time` | tenant | HH:MM tenant tz | 06:00 | any | daily due-date evaluation | Alert parameters → Due-date alert batch recurrence (`EventJobDueDate`) | ALRTCATDEF scheduling, SWE2 deadline events |
| `alerts.max_per_rule_per_hour` | tenant | int | 500 | 10–10,000 | runaway-rule suspension (ALR-0505) | none | none |
| `alerts.retention_days` | tenant | int | 90 | 7–365 | alert inbox retention | Alert parameters → "Alerts retention" (EventParameters) | ALRTPROC cleanup (SALRT_DELETE) |
| `alerts.drilldown_base_url` | environment | URL | `https://app.<env>.passage.dev` | URL | deep-link target in alert email | Alert parameters → Drill-down target | Fiori launchpad intent base |
| `io.max_import_rows`, `io.staging_retention_days` | tenant | int, int | 1,000,000, 7 | ≤ 5 M, 1–30 | import size and staging cleanup | Data management parameters (DMFParameters) → staging cleanup | LSMW / Migration Cockpit project retention |
| `otel.sampling.ratio`, `otel.sampling.latency_threshold_ms` | environment | decimal, int | 1.0 dev / 0.1 stage-prod, 1,000 | 0–1, 100–10,000 | FR-5.13 tail sampling | LCS trace capture settings | ST05 / SAT trace settings |
| `alerting.webhook_url`, `alerting.routes` | environment | ESO ref, YAML | PagerDuty-compatible | — | Alertmanager routing (FR-5.14) | LCS monitoring alerts | Solution Manager alerting (CCMS RZ20 thresholds) |

### 0.5.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Number sequence list and status | list inquiry | scope, continuous, in use | code, scope value, format preview, next, largest, % consumed, continuous, block size | `numseq.sequence` | Number sequences list page; SSRS Number sequence list / references | SNRO intervals, NRIV display |
| Continuous allocation status (gaps audit) | operational report | sequence, date range, status | number, status (reserved/used/released), requested by, tx_ref, age | `numseq.allocation` | Number sequence → Status list (`NumberSequenceList`) | S_ALR_87012342 gaps in document number assignment; S_ALR_87012341 numbers allocated twice |
| Record history | drill-down | entity, record, field, date | time, actor, operation, field, old, new, trace link | `audit.entry`, `audit.field` | Database log viewer (per record) | change documents per object (RSSCD100), S_ALR_87012308 |
| Global audit search | list inquiry | entity, user, date range, operation | same plus record id | same | Database log inquiry; SSRS Database log report | SCU3, RSSCD100, S_ALR_87012293 |
| Audit chain verification | operational report | tenant, seq range | range, entries verified, first broken seq, anchor ref | `POST /v1/audit/verify` | none | none (SM20 integrity is file-based) |
| Import/export job history | list inquiry | entity, status, user, date | job, entity, rows total/ok/failed, duration, error file | `io.import_job` | Data management → Job history (DMFDefinitionGroupExecution) | LSMW / Migration Cockpit logs |
| Documents by type / record | list inquiry | document type, entity, record, uploaded by, date, content text | name, type, linked records, version, size, status, restriction, retain until | `docs.attachment*` | Document management inquiries (DocuRef list) | GOS attachment list, CV04N document search, OAOR |
| Document retention preview | operational report | retention class, horizon (days) | attachment, type, retain until, action (archive/purge), legal hold | `docs.attachment` | none (archive purge not standard in AX) | ILM retention (IRMPOL) preview, SARA write variant test |
| Checked-out documents | list inquiry | user, age | attachment, checked out by, since, linked record | `docs.attachment` | none | CV04N with status "checked out" |
| My alerts / Alert inquiry by rule or user | list inquiry | rule, user, read status, date | fired at, rule, record, recipient, message, read at, delivery status | `ui.alert_event` | Notification list / My alerts (EventInbox); alert inquiry | ALRTINBOX, SALRT1 alert inbox |
| Alert rule statistics | KPI | rule, period | fires per day, suspended flag, recipients, email failures | `ui.alert_rule` + events | none | Situation Handling monitor |
| Standard text where-used | drill-down | text code | templates and layouts referencing it, versions | `ui.standard_text` + notify templates | none | SO10 where-used (none standard) |
| SLO and service dashboards | KPI | service, window | availability, p95 latency, error budget remaining, burn rate | Prometheus | LCS environment monitoring | Solution Manager technical monitoring, ST03N |
| Trace explorer | drill-down | trace id, service, duration | spans, attributes, errors | Tempo | Trace parser | ST05, SAT, STAD |
| Log explorer | list inquiry | service, level, trace id, tenant | time, level, message, trace id | Loki | Event Viewer / Infolog history | SM21 system log, SLG1 application log, ST22 dumps |

### 0.5.15 Printed and electronic documents

No printed business documents (wave 15/46 layouts consume FR-5.18 texts and FR-5.16 `external` attachments). Electronic documents:

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| List export | user export of a view (FR-5.15) | header row (column labels), rows per view, footer row with export time, user, filter, legal entity | CSV (UTF-8, RFC 4180), XLSX | none | not retained by Passage after 7 days (download link expiry) |
| Import error file | import validation | original columns + `row`, `field`, `code`, `message` | CSV, XLSX | import id | 7 days |
| Audit history export | audit viewer export | entity, record, entries, chain seq range, verification result | CSV, JSON (with hashes for third-party verification) | none | requester's custody; source entries retained per 0.5.4 |
| Audit chain anchor | daily job | tenant, seq, hash, timestamp | JSON object in WORM bucket | per-tenant seq | 10 years, object lock (compliance mode) |
| Alert email | alert fired with email delivery | subject (rule or template), record title, changed field old → new or due date, deep link, unsubscribe-from-rule link | email via wave 4 notify | none | wave 4 retention |
| Attachment preview | conversion job | first page PDF or 320 px thumbnail | PDF, PNG | attachment version | deleted with the version |

### 0.5.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| Audit chain anchor | daily 00:30 UTC | tenant | anchor row keyed by (tenant, date) | < 1 min for 1,000 tenants | retry 3×; freeze on mismatch (BR-5.7) | `AuditChainBroken` (page), `AuditAnchorMissing` |
| Audit partition maintenance | weekly | month | creates partitions 3 months ahead; idempotent | < 1 min | alert | `AuditPartitionMissing` |
| Audit retention detach | monthly | month partition | detaches partitions older than retention to object storage (from wave 46) | < 10 min | retry | none |
| Sequence exhaustion check | hourly | sequence | stateless | < 10 s | none | `SequenceNearExhaustion` |
| Stale reservation release | every minute | sequence | releases `reserved` older than timeout (BR-5.1) | < 1 s | retry | `ReservationStuck` (> 3 timeouts/h) |
| Import staging cleanup | daily 03:00 UTC | tenant | deletes staging and files older than 7 days | < 5 min | retry | none |
| Notification and alert cleanup | daily 03:15 UTC | tenant | deletes alert events older than retention | < 1 min | retry | none |
| Due-date alert evaluation | daily at `alerts.due_date_time` | tenant × rule | fired key (rule, record, due value) (BR-5.14) | < 2 min per 100 rules × 100 k records | resume per rule | `AlertJobFailed` |
| Change-based alert evaluator | continuous (CDC consumer) | Kafka partition | consumer dedupe (BR-4.5) | ≤ 5 s event → alert | DLQ | `AlertLagHigh` (> 30 s) |
| Attachment scan and preview | on upload (queue) | attachment version | status is the checkpoint | ≤ 30 s for 20 MB | 3 retries, then `quarantined` with reason `scan_failed` | `DocScanBacklog` (> 100 queued) |
| Attachment text extraction | on `available` | attachment version | writes `extracted_text` once per version | ≤ 60 s for 200 pages | skip with warning DOC-0507 | none |
| Document retention (archive / purge) | weekly Sunday 01:00 tenant time | tenant × document type | dry-run list stored first; each attachment processed once (status) | < 30 min per 1 M attachments | per-item errors logged, run continues | `DocRetentionErrors` |
| Stale check-out release | daily | tenant | releases locks older than `docs.checkout_lock_days` | < 1 min | none | none |
| Personalization / usage data reset | on demand (administrator) | user | deletes Personalization rows for selected users | < 10 s | none | none |

### 0.5.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles that include it |
|---|---|---|---|
| Maintain number sequences | `/v1/numseq/sequences*`, Number sequences screen, test next | with posting duties for fiscal documents in production (lowering `next` on a gap-free sequence is a fraud vector; `allow_to_lower=false` by default) | Financial administrator, Platform engineer |
| View audit log | `/v1/audit/entries*`, `/verify`, audit workspace, history on any record the user can read | with Maintain users / Maintain security roles for the security auditor role (auditors should not administer what they audit) | Auditor, Security administrator (own scope) |
| Import data | `/v1/io/imports*`, Import screen | per entity: also requires `create`/`update` on the target entity | Data steward, Migration consultant |
| Export data | `/v1/io/exports`, export button on lists | export of `pii=sensitive` columns requires explicit field permission | most business roles |
| Maintain saved views | `/v1/ui/views*` own; share with role | none | all users (own); Business administrator (shared, publish to role) |
| Maintain documents | upload, version, check out/in, link on records the user may update | none | all entity maintainers |
| View documents | download, preview, documents workspace search (filtered by read on linked records) | none | all users with entity read |
| Administer document management | `/v1/docs/document-types`, `/file-types`, `/parameters`, force-release, view quarantined, retention preview | with legal-hold management (wave 48 duty) | Document administrator |
| Maintain alert rules | `/v1/alerts/rules*` own rules | none | all users |
| Administer alerts | org-wide rules, suspend/resume any rule, alert statistics | none | Business administrator |
| Maintain standard texts | `/v1/texts*`, Standard texts screen | none | Communications administrator, Business administrator |
| Platform observability | Grafana, Tempo, Loki (Keycloak SSO group) | log access may expose payload fragments; lint keeps PII out of spans | Platform engineer, SRE |

### 0.5.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| NSQ-0501 | error | Number sequence {code} is not in use | `in_use=false` (409 SEQUENCE_NOT_IN_USE) | mark in use or bind another sequence | BR-5.5 |
| NSQ-0502 | error | Number sequence {code} is exhausted at {largest} | SEQUENCE_EXHAUSTED (409) | raise `largest` or enable wrap where legal | BR-5.6 |
| NSQ-0503 | warning | Number sequence {code} is {pct}% consumed | exhaustion threshold | plan a new range | BR-5.6 |
| NSQ-0504 | error | {code} is manual: supply a number | `manual=true` without number | enter the number | BR-5.5 |
| NSQ-0505 | error | Number {n} already used in {code} for scope {scope} | manual duplicate | choose another number | BR-5.5 |
| NSQ-0506 | error | Next number cannot be lowered for {code} | `allow_to_lower=false` | none (gap-free protection) | BR-5.3 |
| NSQ-0507 | error | Formatted number exceeds 40 characters | format length | shorten segments | FR-5.8 |
| AUD-0501 | error (page) | Audit chain broken for tenant {tenant} at seq {seq} | hash mismatch | follow runbook; anchoring frozen | BR-5.7, FR-5.11 |
| AUD-0502 | warning | Audit is {s} s behind; recent changes may not show yet | consumer lag | none; wait | FR-5.10 |
| UIX-0501 | error | This record was changed by {user} at {time}; reload or overwrite | optimistic lock (409) | reload, or overwrite with permission | 0.5.6 rules |
| UIX-0502 | error | {n} fields need attention | client/server validation summary | jump to each field | FR-5.6 |
| UIX-0503 | error | Bulk edit is limited to {max} rows | `ui.bulk_edit.max_rows` | narrow the selection | FR-5.3 |
| UIX-0504 | info | Bulk edit of {n} rows started as job {run}; you will be notified | above batch threshold | none | 0.5.6 rules |
| UIX-0505 | warning | Module {remote} failed to load; other areas still work | remote load failure | retry; platform rolls back pin | FR-5.1 |
| IOX-0501 | error | Row {row}, column {column}: {message} | import validation | fix and re-import or use partial | FR-5.15 |
| IOX-0502 | info | This file was already imported (import {id}) | same sha256 | none | BR-5.11 |
| IOX-0503 | error | Import exceeds {max} rows | `io.max_import_rows` | split the file | FR-5.15 |
| DOC-0501 | error | File exceeds the {max} MB limit for document type {type} | size check | compress or split | FR-5.16 |
| DOC-0502 | error | File type .{ext} is not allowed | blocked or not in allowed list | convert to an allowed type | FR-5.16 |
| DOC-0503 | error | File quarantined: malware detected ({signature}) | scan result | contact the sender; file is not downloadable | FR-5.16, BR-5.12 |
| DOC-0504 | error | Checked out by {user} since {time}; new versions are blocked | check-out lock (423) | ask {user} to check in, or request a release | BR-5.12 |
| DOC-0505 | error | Document is under legal hold and cannot be deleted or purged | legal hold | none | BR-5.12 |
| DOC-0506 | warning | Preview not available for this file type | FileType.preview=none | download to view | FR-5.16 |
| DOC-0507 | warning | Text could not be extracted; the document is not content-searchable | extraction failure (scanned image, encrypted PDF) | OCR arrives in wave 18 capture | FR-5.16 |
| ALR-0501 | info | Alert: {subject} | rule fired | open the record | FR-5.17 |
| ALR-0502 | error | You cannot create an alert on {field}: you do not have read access to it | field mask | choose another field | BR-5.14 |
| ALR-0503 | error | Due-date rules require a date or datetime field | wrong field type | choose a date field | FR-5.17 |
| ALR-0504 | warning | {n} recipients were skipped because they cannot read {entity} | recipient lacks read | adjust recipients | FR-5.17 |
| ALR-0505 | warning | Alert rule {name} was suspended after {n} alerts in one hour | runaway rule | narrow the filter and resume | FR-5.17 |
| STX-0501 | error | Standard text {code} has no version in {language} or the fallback language | missing translation | add the language version | FR-5.18 |
| STX-0502 | error | Unknown placeholder `{{placeholder}}` in {code} | publish validation | correct the placeholder | BR-5.15 |
| STX-0503 | error | {code} is used by {n} published templates or layouts and cannot be retired | where-used check | update the references first | FR-5.18 |

### 0.5.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| Shell first contentful paint | 5,000 users, 2 sessions/day | 0.8 s / 1.5 s / 2.5 s (10 Mbps) | CDN-cached assets | n/a | remotes cached by digest | static assets unaffected |
| Form load (metadata + record) | 200 k/day, peak 100/s | 250 ms / 800 ms / 1.5 s | 300/s | n/a | form definition ETag cache | p95 ≤ 2 s; skeleton rendering |
| Global search | 50 k/day, peak 30/s | 80 ms / 300 ms / 600 ms | 100/s | title + content index ≈ 2 GB/tenant/year | GIN on `tsvector` per entity; attachment content GIN | results truncated to 20; timeouts at 1 s return partial |
| Gap-free allocation | 50 k/day per sequence, peak 500/s | 5 ms / 15 ms / 30 ms | 500/s per sequence | `allocation` ≈ 18 M rows/year for a busy sequence | `(sequence_id, number)` unique; monthly cleanup of used rows > 13 months to archive | row-lock queueing; p99 rises to ≈ 90 ms, no gaps |
| Buffered allocation | 2,000/s across pods | 0.1 ms / 1 ms / 2 ms | 2,000/s | none | in-memory block | block refill contention; still < 5 ms |
| Audit capture (commit → visible) | 5 M changed rows/day/tenant, peak 2,000/s | 500 ms / 2 s / 5 s | 3,000 rows/s with 12 consumers | ≈ 1.8 B entries/year at peak volume (≈ 900 GB) | monthly partitions; `(tenant_id, entity, record_id, occurred_at desc)` | lag grows; `AuditLagHigh` at 10 s; no loss (Kafka retention 7 days) |
| Record history query | 20 k/day | 30 ms / 120 ms / 300 ms | 50/s | — | same index, partition pruning by date | paging at 100 entries |
| Import 100 k rows | 50/day/tenant | 2 min / 5 min / 8 min | 400 rows/s per job | staging transient | batch insert 1,000 rows | jobs queue by priority |
| Attachment upload (20 MB) | 20 k/day/tenant, peak 20/s | 1 s / 4 s / 8 s to `scanning`; ≤ 30 s to `available` | 100 MB/s per tenant to object storage | ≈ 4 TB/year/tenant at 20 MB average (before dedupe) | object key per version; DB rows ≈ 7 M/year | scan queue grows; uploads still accepted |
| Change-based alert evaluation | 500 rules/tenant over the audit stream | 1 s / 5 s / 10 s event → alert | 3,000 changes/s | `alert_event` ≈ 10 M rows/year | rules indexed by (entity, field) in memory; events by (tenant_id, recipient_id, read_at) | lag; suspension of runaway rules |
| Trace ingestion | 2 k spans/s per service at 10% sampling | n/a | 50 k spans/s per collector gateway | Tempo ≈ 5 TB/year (14-day retention by default) | object storage backend | collector drops lowest-priority spans; `OTelCollectorDropping` |

### 0.5.20 Localization and compliance hooks

- Number sequences and fiscal law: gap-free continuous numbering is the hook for invoice-numbering rules (EU VAT Directive Art. 226(2) "sequential number", France and Italy chronological numbering, Mexico CFDI folio series, India GST invoice series ≤ 16 characters per financial year). Country packs (wave 48) ship sequence templates per document type (format, `reset_on_year`, scope `legal_entity_fiscal_period` from wave 6) and validate `continuous=true` for fiscal documents of their country; the extension point is the sequence template + decision table `NumberingPolicy` (FR-3.13).
- Audit retention: defaults 10/3 years; country packs raise minimums (Germany GoBD 10 years for booking records, France 6–10, US SOX 7); `audit.retention_years.*` can only be raised by a country pack, not lowered below its minimum.
- Document retention: retention classes per document type are the hook for GoBD/HGB §257, US IRS records retention and similar rules; legal hold (wave 48) overrides purge; archive storage class with object lock satisfies WORM requirements where mandated.
- Privacy: audit stores SHA-256 digests for `pii=sensitive` fields; erasure requests (wave 48) pseudonymize actor display names in the viewer but never rewrite audit rows (lawful basis: legal obligation, GDPR Art. 17(3)(b)); attachments linked to an erased subject are listed for the privacy officer; alert events and personalization are deleted on erasure.
- Language and formats: shell strings, form labels, alert subjects and standard texts are resolved per user locale; English ships in wave 5, wave 48 label packs add languages; right-to-left layout support is a wave 48 deliverable but the shell uses CSS logical properties from wave 5. Date, number and currency formatting follow `ui.user_defaults` via `Intl` APIs.
- Calendar: due-date alerts and year segments use the legal entity time zone; fiscal-period scopes arrive in wave 6.
- Accessibility law: WCAG 2.1 AA (FR-5.5) covers EN 301 549 (EU Accessibility Act, from 2025-06-28) and US Section 508 for the generated UI.
- E-invoicing: attachments with `restriction=external` and document type "E-invoice original" are the storage hook for wave 35 Peppol/Factur-X archives (original XML retained unchanged, sha256 recorded).

### 0.5.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX §12.29.1 Organization administration → Alerts (org-level alert rules) (register row 82) | GAP | ADDED as FR-5.17 | org-wide rules (`org_wide=true`) maintained by Administer alerts |
| AX §12.29.4 Organization administration → Setup: Alerts → Alert rules (change-based, due-date; event rule setup, alert email templates) (register row 88) | GAP | ADDED as FR-5.17 | rule kinds per FR-5.17; email via wave 4 templates (FR-4.14) |
| AX §12.30.3 System administration → Periodic: Alerts → Change-based alerts / Due date alerts (batch processors) (register row 91) | GAP | ADDED as FR-5.17 | change-based evaluated continuously from CDC; due-date daily job (0.5.16) |
| AX §12.31.1 Home / role centers / workflow → Alerts (My alerts / Notification list) (register row 94) | GAP | ADDED as FR-5.17 | shell Alerts bell and list (0.5.6), `ui.alert_event` |
| AX §12.31.2 Home / role centers / workflow → Inquiries: Alert inquiry (alerts by rule/user) (register row 95) | GAP | ADDED as FR-5.17 | My alerts / Alert inquiry and Alert rule statistics (0.5.14) |
| AX §12.31.3 Home / role centers / workflow → Periodic: Alerts → Process change-based alerts / Due-date alerts (register row 98) | GAP | ADDED as FR-5.17 | same jobs as row 91 |
| AX §12.31.4 Home / role centers / workflow → Setup: Alerts → Alert rules (field/record changes, due date; subject/message; recipients; organization-wide) / Alert rule templates (register row 100) | GAP | ADDED as FR-5.17 | rule templates = `MessageTemplate` codes; "Notify me when…" creates a rule from a field |
| AX §12.29.1 Organization administration → Document management → Document handling (attachments on any record), Document types, File types (`DocuView`, `DocuType`) (register row 235) | PARTIAL-5 | ADDED as FR-5.16 | `Attachment`, `DocumentType`, `FileType` entities |
| AX §12.29.2 Organization administration → Document management inquiries (documents by type/record) (register row 243) | PARTIAL-5 | ADDED as FR-5.16 | Documents by type / record inquiry (0.5.14) |
| AX §12.29.3 Organization administration → Periodic: Document management → Archive documents / Purge by retention (register row 249) | PARTIAL-5 | ADDED as FR-5.16 | retention job (0.5.16), BR-5.12, legal hold |
| AX §12.29.4 Organization administration → Setup: Document management parameters (archive directory, SharePoint, file types, max size), Document types (file, note, URL, image, template; group; location), Active document tables, File types (register row 252) | PARTIAL-5 | ADDED as FR-5.16 | parameters in 0.5.13; `active_entities[]` = active document tables; location = object storage or database; SharePoint location is not a storage backend (see row 253) |
| AX §12.29.4 Organization administration → Setup: Document management → OneDrive/SharePoint integration (D365), Attachment migration (register row 253) | PARTIAL-5 | ADDED as FR-5.16 | S3-compatible storage with customer bucket (`bucket_ref`) and attachment migration from `DocuRef`/`DocuValue` and SharePoint paths (0.5.11); live SharePoint/OneDrive storage as a document location is not offered — files are copied into object storage at migration, and a `url` document type links to SharePoint items |
| AX §12.30.1 System administration → Document handling (link) (register row 264) | PARTIAL-5 | ADDED as FR-5.16 | same capability as row 235 |
| AX §12.30.4 System administration → Setup: Document management (link) (register row 270) | PARTIAL-5 | ADDED as FR-5.16 | same as row 252 |
| AX §12.31.1 Home / role centers / workflow → Document handling on any record (`DocuView`) (register row 273) | PARTIAL-5 | ADDED as FR-5.16 | attachments panel on every generated detail page (0.5.6) |
| SAP §13.31 Cross-Application → SO10 / SO72 / SE75 (text objects/IDs): standard texts, text management (register row 349) | PARTIAL-5 | ADDED as FR-5.18 | register proposed wave 46 text blocks; added here because wave 4 templates (FR-4.14) and wave 46 layouts both consume it; layouts reference by code |
| SAP §13.35 DMS → CV01N originals (check-in/check-out, KPro storage categories, viewer, conversion) (register row 377) | PARTIAL-5 | ADDED as FR-5.16 | check-out/check-in, multiple versions per attachment, storage locations, previews; native CAD 2D/3D viewing is dropped (PLM keeps CAD; wave 9 engineering change links PLM documents by URL type) |
| SAP §13.35 DMS → Periodic: CV conversion runs (originals → PDF/neutral formats) (register row 389) | PARTIAL-5 | ADDED as FR-5.16 | attachment scan and preview job (Office → PDF, PDF first page, image thumbnails); CAD neutral-format conversion dropped with the viewer |
| SAP §13.35 DMS → Customizing: DC30 workstation applications (file types, viewer/editor, conversion) (TDWP) (register row 394) | PARTIAL-5 | ADDED as FR-5.16 | `FileType` (mime, max size, preview method, extract_text, blocked) |
| SAP §13.35 DMS → Customizing: full-text search activation (TREX/HANA) for originals (register row 398) | PARTIAL-5 | ADDED as FR-5.16 | text extraction to Postgres `tsvector`, searchable from global search and the documents inquiry; OCR of scanned images is wave 18 |
