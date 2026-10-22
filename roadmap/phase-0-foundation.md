# Phase 0: Foundation & Platform Kernel (waves 1-5)

Build the platform every module stands on: Kubernetes delivery pipeline, identity and tenancy, the metadata/extensibility kernel (our AOT/DDIC), Kafka eventing and workflow, audit and the UX shell. Nothing business-facing ships until this layer can deploy, observe and roll back safely.

## Wave 01: Delivery Pipeline & Cluster Baseline

**Phase 0 — Foundation & Platform Kernel**

**Goal.** Any engineer can merge a change and watch it reach a Kubernetes environment with rollback in under 15 minutes.

**Replaces**

- *AX / D365:* LCS (Lifecycle Services) deployable packages, build/release pipelines
- *SAP:* SAP Transport Management System (STMS), CTS+

**Deliverable**

- Monorepo layout (Go services, TypeScript services, React web, Helm charts, Terraform/OpenTofu modules)
- Cloud-agnostic cluster bootstrap: Terraform module that stands up a K8s cluster on any provider (k3d locally, EKS/GKE/AKS/bare-metal via the same interface)
- Argo CD GitOps: dev/stage/prod app-of-apps, image promotion by digest
- GitHub Actions CI: lint, unit tests, container build, SBOM (Syft), vulnerability scan (Grype/Trivy), signed images (cosign)
- A `hello-ledger` placeholder service deployed end-to-end to prove the path

**Proof (testable acceptance)**

- [ ] CI is green on main and fails on an injected lint/test error (negative test committed)
- [ ] `make deploy-local` brings up k3d + Argo CD + the placeholder service on a laptop in < 10 min
- [ ] Promotion of a tagged image to stage and rollback to the prior digest both complete and are visible in Argo CD history
- [ ] SBOM and signature verification gate blocks an unsigned image (negative test)

**Deploy**

k3d local cluster + one cloud dev cluster running Argo CD, ingress, cert-manager and the placeholder service behind TLS.

**References**

- [Argo CD app-of-apps pattern](https://argo-cd.readthedocs.io/en/stable/operator-manual/cluster-bootstrapping/)
- [OpenTofu](https://opentofu.org/docs/)
- [SLSA supply-chain levels](https://slsa.dev/spec/)
- [sigstore cosign](https://docs.sigstore.dev/cosign/overview/)
- [k3d](https://k3d.io/)

**Depends on:** none (first wave)

---

## Wave 02: Identity, Tenancy & Authorization

**Phase 0 — Foundation & Platform Kernel**

**Goal.** Users log in once, land in the right legal entity, and every API call is authorized by a policy engine rather than code.

**Replaces**

- *AX / D365:* AX security roles/duties/privileges, Azure AD integration, company (DataAreaId) isolation
- *SAP:* SAP user master, PFCG roles, authorization objects, client (MANDT) isolation

**Deliverable**

- Keycloak deployed as the IdP (OIDC/SAML federation to Entra ID/Okta/Google), MFA, SCIM user provisioning
- Tenant model: tenant → legal entities → sites; every row carries tenant_id and Postgres row-level security enforces it
- Role/duty/privilege model (AX-style) compiled to OPA/Rego policies; policy decision point as a sidecar
- API gateway (Envoy Gateway) with JWT validation and per-tenant rate limits
- Admin UI: users, roles, duty assignment, effective-permission viewer

**Proof (testable acceptance)**

- [ ] Contract tests: a user with duty 'Maintain vendors' can PUT /vendors; one without gets 403 — table-driven across 20 duties
- [ ] RLS test: tenant A query with tenant B's id returns zero rows even when executed with a superuser-free app role
- [ ] Login via two federated IdPs in CI (Keycloak-to-Keycloak federation as the stand-in)
- [ ] Effective-permission report for a user matches the OPA decision for 100 sampled resources

**Deploy**

Keycloak, OPA, Envoy Gateway and the admin UI live in dev; the placeholder service now requires a token.

**References**

- [Keycloak](https://www.keycloak.org/documentation)
- [Open Policy Agent / Rego](https://www.openpolicyagent.org/docs/latest/)
- [Postgres row-level security](https://www.postgresql.org/docs/current/ddl-rowsecurity.html)
- [AX 2012 role-based security (roles/duties/privileges)](https://learn.microsoft.com/en-us/dynamicsax-2012/developer/role-based-security-in-microsoft-dynamics-ax)
- [SCIM 2.0](https://datatracker.ietf.org/doc/html/rfc7644)

**Depends on:** wave 1

---

## Wave 03: Metadata Kernel (Entities, Fields, Extensions)

**Phase 0 — Foundation & Platform Kernel**

**Goal.** Business entities are declared once in a metadata model that generates the Postgres schema, API, validation, and UI form — and customers can extend any entity without forking.

**Replaces**

- *AX / D365:* AOT: tables, EDTs, base enums, data entities, extensions; X++ table methods
- *SAP:* ABAP Data Dictionary (DDIC), domains, data elements, append structures, BAPIs/OData entities

**Deliverable**

- Entity definition language (YAML/JSON schema) for tables, typed fields (EDT-equivalents), enums, relations, indexes, and lifecycle hooks
- Code generator: entity → Postgres migration (Atlas/Goose), Go domain model, OpenAPI 3.1 spec, TypeScript client, JSON Schema validation
- Extension mechanism: customer-layer entity extensions (extra fields, enum values, hooks) stored as metadata, applied without code changes
- Metadata registry service with versioning and diff
- Field-level attributes: required, unique, default, computed, visibility, PII-flag, audit-flag

**Proof (testable acceptance)**

- [ ] Golden tests: 10 reference entities generate deterministic migrations/APIs; snapshot diff fails CI on drift
- [ ] An extension adding a field + enum value to Vendor is applied to a running environment with zero downtime and the field appears in API and form
- [ ] Backward-compat test: v1 client still works after v2 entity change (additive) and fails loudly on a breaking change in CI
- [ ] Load: generated API serves 1,000 rps on a 3-node dev cluster with p95 < 50 ms for simple reads

**Deploy**

Metadata registry + generator in the pipeline; the first three generated entities (Tenant, LegalEntity, User) replace hand-written code.

**References**

- [Atlas schema migrations](https://atlasgo.io/)
- [OpenAPI 3.1](https://spec.openapis.org/oas/v3.1.0)
- [AX 2012 AOT & data dictionary concepts](https://learn.microsoft.com/en-us/dynamicsax-2012/developer/data-dictionary)
- [SAP ABAP Dictionary overview](https://help.sap.com/docs/ABAP_PLATFORM_NEW/ec1c9c8191b64b6ea0c6a5dcc5dbeaf9/cf21ea43446011d189700000e8322d00.html)
- [D365 F&O extensibility model (extensions over overlayering)](https://learn.microsoft.com/en-us/dynamics365/fin-ops-core/dev-itpro/extensibility/extensibility-home-page)

**Depends on:** wave 2

---

## Wave 04: Event Backbone, Workflow & Batch

**Phase 0 — Foundation & Platform Kernel**

**Goal.** Every business change is an immutable event on Kafka; long-running processes (approvals, batch jobs) run on a durable workflow engine with retries, timers and human tasks.

**Replaces**

- *AX / D365:* AX workflow framework, batch framework, business events, SysOperation
- *SAP:* SAP Business Workflow, background jobs (SM36/SM37), event linkage, IDoc status processing

**Deliverable**

- Strimzi-managed Kafka; CloudEvents envelope; AsyncAPI-described topics generated from entity metadata
- Transactional outbox in every service (Debezium CDC) — no dual writes
- Temporal cluster for workflows; SDKs for Go/TS; approval workflow template (submit → approve/reject/delegate/escalate with SLA timers)
- Batch job framework: scheduled/recurring jobs with partitioning, checkpoints, retry and a job monitor UI
- Workflow designer: configuration-driven approval chains per entity (e.g. purchase requisition by amount band)

**Proof (testable acceptance)**

- [ ] Chaos test: kill the service between DB commit and publish; the outbox guarantees exactly-one delivery on restart
- [ ] Approval workflow test: 3-level chain with delegation and SLA escalation completes; timers fire after simulated clock advance
- [ ] Batch job of 1M synthetic rows checkpoints and resumes after pod kill without duplicates
- [ ] Schema registry rejects a breaking event-schema change in CI

**Deploy**

Kafka, Debezium, Temporal, schema registry, and job monitor UI running in dev; the metadata registry emits entity-changed events.

**References**

- [Strimzi](https://strimzi.io/documentation/)
- [Temporal](https://docs.temporal.io/)
- [Transactional outbox pattern](https://microservices.io/patterns/data/transactional-outbox.html)
- [Debezium outbox event router](https://debezium.io/documentation/reference/stable/transformations/outbox-event-router.html)
- [CloudEvents spec](https://github.com/cloudevents/spec)
- [AX 2012 workflow overview](https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/workflow-overview)

**Depends on:** wave 3

---

## Wave 05: UX Shell, Number Sequences, Audit & Observability

**Phase 0 — Foundation & Platform Kernel**

**Goal.** A single web shell renders any metadata-defined entity as list/detail/grid forms with personalization; every change is audited; every request is traceable.

**Replaces**

- *AX / D365:* AX client forms, list pages, FactBoxes, number sequences, database log, Excel add-in
- *SAP:* SAP GUI/Fiori launchpad, number ranges (SNRO), change documents (CDHDR/CDPOS), SAP Solution Manager monitoring

**Deliverable**

- React shell (module federation): navigation by workspace, global search, saved views, grid with inline edit, personalization, keyboard-first, WCAG 2.1 AA
- Metadata-driven form renderer: list page, detail page, line grids, lookups, validation messages — generated from entity metadata
- Number sequence service: per-legal-entity, continuous or non-continuous, with gap-free mode for fiscal documents
- Audit log: field-level before/after captured from CDC, queryable per record, immutable store (append-only table + hash chain)
- OpenTelemetry traces/metrics/logs across every service; Grafana/Tempo/Loki/Prometheus; SLO dashboards; PagerDuty-compatible alerting
- Excel/CSV import-export against any entity

**Proof (testable acceptance)**

- [ ] Playwright + axe: 100% of generated forms pass WCAG 2.1 AA checks; keyboard navigation test suite
- [ ] Number sequence: 10k concurrent requests for a gap-free sequence yield no gaps and no duplicates (Jepsen-style test)
- [ ] Audit: a field change in any entity appears in the audit viewer within 2 s with user, time, old/new, and the trace id
- [ ] Trace: a UI click can be followed through gateway → service → DB → Kafka consumer in Tempo

**Deploy**

Shell at app.<env>.passage.dev with the admin workspace; observability stack and alert routes live. Phase 0 exit: platform demo to stakeholders.

**References**

- [Module Federation](https://module-federation.io/)
- [OpenTelemetry](https://opentelemetry.io/docs/)
- [WCAG 2.1](https://www.w3.org/TR/WCAG21/)
- [AX 2012 number sequences](https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/about-number-sequences)
- [SAP change documents](https://help.sap.com/docs/SAP_NETWEAVER_750/3ee76b4eb5c44f11a5ffdd1c6e4dcc04/4d5ce0a5bbf0466aa71ae5aa8fe4ce44.html)
- [Jepsen](https://jepsen.io/)

**Depends on:** wave 4

---
