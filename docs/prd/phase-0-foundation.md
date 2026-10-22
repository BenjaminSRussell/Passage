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
  - Notes: each service owns a `Dockerfile`, `Makefile`, `CODEOWNERS` entry and a Helm chart; CI path filters build only changed services; a `go.work` and `pnpm` workspace root pin toolchain versions (Go 1.23, Node 22, Python 3.12).
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

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
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

### 0.3.8 Acceptance criteria

- **AC-3.1** Given the 10 reference entities, When the generator runs 3 times, Then the migration, OpenAPI and client snapshots are byte-identical and match the committed golden files. (FR-3.5, FR-3.6, FR-3.11)
- **AC-3.2** Given a customer package adding `x_risk_rating` (enum) and enum value `PreferredPlus (1001)` to VendorStub, When applied in dev, Then the API accepts the field within 60 s, no request fails during apply, and the wave-5 form shows it. (FR-3.9)
- **AC-3.3** Given a published v1 client, When the server publishes an additive v2, Then the v1 contract suite passes; Given a breaking change without `breaking: true`, Then CI fails naming the change. (FR-3.10, BR-3.2)
- **AC-3.4** Given 1,000 rps of `GET /v1/identity/users/{id}` for 5 minutes on the dev cluster, When measured at the gateway, Then p95 < 50 ms and error rate < 0.1%. (FR-3.12)
- **AC-3.5** Given a `validateWrite` declarative hook `amount > 0`, When a client posts `amount = -1`, Then 422 with `field=amount`. (FR-3.7)
- **AC-3.6** Given Tenant, LegalEntity and User regenerated from metadata, When the wave-2 test suites run, Then they pass unchanged and the hand-written handlers are removed from the repo. (FR-3.6)
- **AC-3.7** Given a diff request between versions 3 and 5, When called, Then the response lists each added/changed/removed field with classification additive/breaking. (FR-3.8)
- **AC-3.8** Given a field with `pii=sensitive`, When listed by a user without the field permission, Then the field is absent; when fetched by id, Then masked. (FR-3.3, BR-3.9)

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

Validation: approve/reject require a comment when the definition flags `comment_required`; delegate target must hold the step's role; a run request against a `hold` job returns 409.

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| dev.passage.workflow.instance.submitted.v1 | submit | instance_id, entity, record_id, definition_version, submitted_by | owning service (sets record status), audit |
| dev.passage.workflow.workitem.assigned.v1 | step entered / delegated | workitem_id, instance_id, assignee_id, due_at, step | shell inbox, notifier |
| dev.passage.workflow.workitem.escalated.v1 | SLA timer fired | workitem_id, from_assignee, to_assignee, action | notifier, audit |
| dev.passage.workflow.instance.completed.v1 | terminal status | instance_id, outcome (approved/rejected/canceled/recalled), completed_at | owning service, audit, shell |
| dev.passage.batch.run.started.v1 / completed.v1 / failed.v1 | run transitions | job_code, run_id, rows_total, rows_done, duration_ms, error | job monitor, alerting, audit |
| dev.passage.metadata.entity.published.v1 | registry outbox | (see wave 3) | events service (topic/schema create) |

### 0.4.6 User experience

| Screen | Primary actions | Key fields/columns | Duty required |
|---|---|---|---|
| Work items (inbox) | Approve, reject, delegate, request change, bulk approve | document, step, submitted by, due, age | Approve workflow |
| Workflow instance timeline | View steps, comments, timers; recall | status, step, assignee, acted at, SLA | submitter / Maintain workflows |
| Workflow designer | Add step, condition, SLA, escalation; version; activate | entity, legal entity, bands, assignees | Maintain workflows |
| Batch jobs | Define, schedule, hold, run now | code, handler, cron, last run, next run | Maintain batch jobs |
| Job monitor | Watch progress, cancel, resume, rerun failed, view log | run, status, rows/sec, ETA, errors by partition | View batch history |
| Topic catalog | Browse topics, schemas, consumer lag | topic, partitions, schema version, lag | Maintain batch jobs |

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

### 0.4.8 Acceptance criteria

- **AC-4.1** Given a service killed by SIGKILL between DB commit and Debezium capture, 10 times in a loop, When it restarts, Then every committed outbox row appears on Kafka exactly once (consumer dedupe table shows 0 duplicates, 0 gaps). (FR-4.2, FR-4.5)
- **AC-4.2** Given a 3-level chain (role → user → expression) with 4-hour SLAs, When step 2's assignee delegates and step 3 times out with `delegate_to admin`, Then the instance completes approved after simulated clock advance and the timeline shows 5 work items. (FR-4.7)
- **AC-4.3** Given a batch job over 1,000,000 synthetic rows in 16 hash partitions, When the worker pod is killed at ~40%, Then the run resumes and ends with rows_done = 1,000,000 and no duplicate side effects. (FR-4.9)
- **AC-4.4** Given a schema change that removes a required field, When CI registers it, Then the registry rejects it with the incompatible path. (FR-4.3)
- **AC-4.5** Given an entity publish in the registry, When the outbox is captured, Then a topic `passage.metadata.entity.v1` message with type `dev.passage.metadata.entity.published.v1` exists within 1 s. (FR-4.11)
- **AC-4.6** Given a submitter recalls after step 1 approved, When the action is sent, Then 409 `RECALL_NOT_ALLOWED`. (BR-4.1)
- **AC-4.7** Given a consumer group replay from `T-1h`, When executed, Then events are redelivered and the dedupe table suppresses side effects for already-processed ids. (FR-4.12)
- **AC-4.8** Given an exclusive job with a running instance, When a second run is requested, Then 409 `RUN_OVERLAP`. (FR-4.9)
- **AC-4.9** Given a step whose assignee expression resolves to nobody, When the step is entered, Then it escalates to the workflow administrator within 5 s and emits `workitem.escalated`. (FR-4.7)

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

---

## 0.5 Wave 5: UX Shell, Number Sequences, Audit & Observability

### 0.5.1 Scope and business context

AX users work in list pages and details forms generated from the AOT (form datasources, `ListPageInteraction`, action panes, FactBoxes, `SysQueryForm` advanced filters, `SysLastValue` personalization, `SysTableLookup` lookups, Infolog) and D365 workspaces, saved views and the Excel add-in; SAP users work in SAP GUI dynpros and ALV grids with layout variants, F4 search helps, SE16N, and the Fiori launchpad with catalogs and groups. Document numbering comes from AX `NumberSequenceTable` / `NumberSequenceReference` / `NumberSequenceScope` (shared, company, legal entity, fiscal calendar period, operating unit) with continuous and non-continuous modes and `NumberSequenceList` pre-allocation, or SAP `SNRO` number range objects with `NRIV` intervals, buffering (`NRIV_LOKAL`) and gap-free settings. Change tracking is AX `SysDatabaseLog` / `SysDatabaseLogSetup` (Insert/Update/Delete/RenameKey) or SAP change documents (`CDHDR`/`CDPOS`, `SCDO` objects, `DBTABLOG` table logging). Monitoring is LCS environment monitoring or Solution Manager / ST03N / SM21 / ST22. Wave 5 replaces all of this with a React module-federation shell, a metadata-driven form renderer, a number sequence service with a Jepsen-tested gap-free mode, an audit service that derives field-level history from CDC into a hash-chained append-only store, OpenTelemetry across every service into Prometheus/Tempo/Loki/Grafana, and CSV/XLSX import-export against any entity. At wave end the admin workspace runs at `app.<env>.passage.dev` and Phase 0 is demonstrated to stakeholders. Out of scope: full-text search across documents (wave 46), report designer (wave 15), mobile PWA (wave 32), localization of labels (wave 48), fiscal-period scopes for sequences (wave 6 supplies calendars; scope type is reserved here).

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

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| dev.passage.numseq.number.allocated.v1 | continuous allocation `used` or `released` | sequence_code, scope_value, number, status, tx_ref, actor | audit |
| dev.passage.audit.entry.recorded.v1 | entry committed | entry_id, tenant_id, entity, record_id, operation, hash, seq | shell live history, analytics (wave 46) |
| dev.passage.audit.chain.anchored.v1 | daily anchor | tenant_id, seq, hash, anchor_ref | compliance evidence (wave 48) |
| dev.passage.io.import.completed.v1 / failed.v1 | import terminal | import_id, entity, rows_ok, rows_failed, error_file_ref | shell inbox, audit |
| dev.passage.ui.view.shared.v1 | saved view shared | view_id, entity, role_id, owner | shell cache |

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
