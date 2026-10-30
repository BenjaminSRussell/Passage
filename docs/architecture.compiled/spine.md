# Architecture Spine: Architecture: Passage ERP Platform

## Service Inventory & Boundaries
Passage = cloud-agnostic ERP, 50 waves / 10 phases; 15 bounded contexts (12 core + 3 supporting), 31 deployable images (10 Go + 15 TS + 6 Py). One Postgres database per context (`passage_<context>`), schema = context name. `ledger.Post` is the ONLY synchronous cross-context write; everything else is an event.
- platform-kernel — Go library `platform/go` (httpx, dbx, tenant, authz, events, outbox, money, ids, testkit; new: sequence, ledgerclient) + TS equivalent — compiled into every service; waves may not edit it
- edge-identity — Envoy Gateway (TLS/JWT/rate-limit/coarse duty), Keycloak (realm per tenant + `_internal`), OPA — no business logic
- platform — `platform` (Go) + `platform-ts` (TS) — tenants, users, RBAC/SoD, metadata, number-sequence definitions, audit chain, jobs, workflow, org, policy compiler
- party — `party-ts` — parties, customer/vendor roles, dedup/merge
- product — `product`, `product-ts`, `product-py` — products, BOM, routing, ECO, configurator
- ledger — `ledger` (Go) — universal journal, FX, tax, close, statements, budgets, consolidation, period control
- inventory — `inventory` (Go) — transactions, on-hand, costing, quality; valuation owner for receipts/shipments/RAF
- procurement — `procurement`, `procurement-ts` — PO to payment, matching, AP, payment files
- manufacturing — `manufacturing`, `manufacturing-ts`, `manufacturing-py` — production orders, MRP, scheduling, OEE, settlement saga
- sales — `sales`, `sales-ts` — pricing, ATP, orders, shipping, billing, AR, cash app, rev rec; hosts IC saga worker
- warehouse — `warehouse`, `warehouse-ts`, `warehouse-py` — locations, work, waves, loads, freight, VRP
- treasury — `treasury` (Go) — bank, cash, fixed assets, leases
- workforce — `workforce`, `workforce-ts` — HR, payroll, absence, expenses
- projects — `projects-ts`, `projects-py` — project accounting, maintenance, field service
- integration — `integration-ts` — webhooks, EDI/Peppol, e-invoice, tenant egress relay
- migration-framework — migration pipeline in `integration` DB (`migration` schema) — AX/SAP packs
- analytics — `analytics-ts` + lakehouse (Iceberg/Trino/ClickHouse/Cube/dbt)
- agents — `agents-ts` (MCP gateway at `/v1/agents/mcp`, copilot, runtime), `agents-py` (evals), `model-serving-py`
- web-bff / device-bff / portal-bff — `services/<name>-ts` cookie-session token-handler BFFs
- web-shell — `apps/web` React shell + 16 module remotes + `@passage/*` packages
- pwa-apps — shopfloor, warehouse-mobile, technician, supplier-portal, customer-portal
- eventing-backbone — outbox + Debezium + Kafka + consumers + Temporal sagas (S3b settlement, S4 intercompany)
- testing-reconciliation — test strategy, REC-01..19 reconciliation, CI gates, wave-30 simulation
- deployment-cicd — monorepo, Helm/Argo CD, CI/CD, infra, encryption, SLOs

## Tech Stack Decisions
ADR-0001 fixed: Kubernetes (any conformant), Argo CD GitOps, Helm + OpenTofu; Go (ledgers/engines: chi, pgx/v5, shopspring/decimal, slog, floor 1.25), TypeScript (workflow/integration/BFF: Node 24 LTS, Fastify 5 + zod, pino, Kysely, Temporal SDK, @confluentinc/kafka-javascript), Python 3.12+ (numerical: FastAPI, OR-Tools, statsforecast); Postgres 17+ on CloudNativePG per context with RLS, PgBouncer transaction mode, Atlas expand/contract migrations; Kafka 4.x KRaft (Strimzi) + Debezium 3.x + Apicurio; Temporal; Keycloak 26+; OPA/Rego; Envoy Gateway; Vault + External Secrets; S3-compatible object store with Object Lock; Iceberg + Trino/ClickHouse + dbt + Cube; OpenTelemetry to Prometheus/Tempo/Loki/Grafana; React 18 + Vite + module federation; GraphQL read-only on web-bff. Moonaan defaults (AWS/Pulumi/DynamoDB/SNS/Cognito/X-Ray/Redis) are NOT used; Valkey (never Redis) only proposed for rate-limit counters. LLM provider default Claude via provider-agnostic layer.

## Naming Conventions
- Monorepo: `platform/go`, `services/<name>[-ts|-py]`, `apps/<app>`, `deploy/`, `roadmap/{waves,ownership}.json`. Service dirs: `cmd/ internal/{domain,store,api,events} migrations/ api/openapi.yaml events/asyncapi.yaml charts/<name>/`.
- REST: `https://api.<env>.passage.dev/v1/<context>/<collection>` plural kebab-case; JSON camelCase wire, snake_case Postgres; UUIDv7 ids; lifecycle/engine actions as colon custom methods (`{id}:post`, `journal-entries:post`); browser uses `/api/v1/<context>/...` rewritten by the BFF; BFF routes `/bff/...`; health `/healthz`, `/readyz`, `/metrics`. Privilege codes `<entity>.<action>` (read|write|post|approve|delete|export|admin); every operation carries `x-passage-duty`.
- Events: CloudEvents type `dev.passage.<context>.<entity>.<verb>.v<n>` (lowercase, past tense); topics `passage.<context>.<entity>`; snapshot `.snapshot`; DLQ `passage.<context>.<entity>.dlq.<consumer>`; consumer group `passage-<consumer-id>`; source `/passage/<context>/<service>`.
- DB: `tenant_id` first column, PK `(tenant_id, id)`; enums `text + CHECK` (never PG ENUM); money `numeric(20,6)` + `currency char(3)`; rate `numeric(28,12)`; pct `numeric(9,6)`; indexes `IX-<ctx>-nn`; entities `EN-nnn`; P3 data in `*_secure` tables.
- Posting idempotency key: `post:<context>:<documentType>:<documentId>:<purpose>[:<revision>]`. Document numbers `{LE}-<SEQ>-{FY}-{#######}` (gap-free where fiscal). Error `type` `https://passage.dev/problems/<code>`.
- Polyglot suffixes: `-ts` TypeScript, `-py` Python; tests `Test<Unit>_<Behaviour>`; Playwright ids `E<WW>-<NN>`; proof tags `@proof:wNN-k`.

## Cross-Cutting Non-Negotiables
- Tenancy: every tenant table has permissive `tenant_access` + restrictive `legal_entity_scope` RLS (data §4.5 template, supersedes Appendix E.2); `SET LOCAL passage.tenant_id|legal_entity_scopes` per tx; app role has no BYPASSRLS; CDC role outside RLS.
- Auth: Keycloak JWT RS256, `aud=passage-api`, re-verified by every service; duties live in OPA, never in JWT; OPA unavailable/bundle >5 min = 503 fail-secure; access 15 min, refresh 12 h single-use; tokens never reach browser (BFF cookie `__Host-passage_sid`, `X-Passage-CSRF`); step-up via OPA obligation `requires_acr: mfa`.
- Event envelope: CloudEvents 1.0 with `tenantid, correlationid, causationid, traceparent, actor, sequence, dataschema`; outbox in the same tx as state change, validated before insert; consumers use `consumer_inbox` + 5 attempts (1/4/16/60 s) then DLQ; order by `sequence`; additive-only schema evolution (BACKWARD_TRANSITIVE).
- Error envelope: RFC 9457 `application/problem+json` with `type,title,status,detail,instance,code,serviceCode,traceId,errors[]`.
- API rules: `Idempotency-Key` on creating/posting POST and all custom methods; `If-Match` on PUT/PATCH/DELETE/custom (428/412 `conflict_version`); ETag on reads; 202 + `statusUrl` (`/v1/platform/jobs/{id}`) for long work; cursor pagination; `RateLimit-*`, `Retry-After`, `Deprecation`/`Sunset` (>= 12 months), `X-Passage-Api-Build`; contract-first OpenAPI 3.1/AsyncAPI 3.
- Financial: posted fiscal documents immutable (corrections = reversal/credit note); balanced in 3 currencies; rounding once per document; gap-free numbers allocated last in the tx; periods checked inside posting tx.
- PII: P0-P3; P3 encrypted per column, never in events/logs/CDC/lakehouse/exports; audit via CDC hash chain + `<ctx>.audit_event` for P3; logs JSON with `correlationId`, no P2/P3 values.
- Observability: OTel in every service, W3C traceparent through HTTP, Kafka, Temporal; SLO 99.9%, p99 < 500 ms sync, ledger post p99 < 200 ms at 5,000 lines/s.
- Deployment: signed (cosign) SBOM-attested images, digest-pinned Argo CD sync, rollback <= 15 min; ownership rule (a wave edits only its `ownership.json` paths, never `platform/`); no provider-managed service in the critical path (NFR-PT1); 12-factor env config, secrets from Vault.
- AI: agents act on-behalf-of a user (OPA `agent@user`, never exceeds user); writes only through approval-gated `propose_*`; 10 s kill switch; PII pseudonymized before provider calls.

## Open Questions
- Rate-limit shared store: adopt in-cluster Valkey or per-pod limits (needs ADR addendum). Egress topic naming conflict (`passage.egress.<tenantSlug>.<context>` vs `passage.ext.<tenantslug>.<context>.<entity>`); needs ADR amendment.
- Keycloak token exchange/`act` claim for agents and `_internal` subject forwarding; delegation for non-present users (event-triggered agent runs blocked).
- Temporal namespace per tenant vs shared; registry product (Apicurio assumed); CNPG failover slot continuity; pgvector in CNPG image; GPU availability.
- Shared package location `packages/@passage/*`, `data/` root, design system choice; test-infra path ownership; `iot-edge` deployable has no owner; business-date injection for simulations.
- Unresolved review items: R4-1 year-spanning reconciliation cut; R4-2 `:override-negative` contradicts NFR-D4 (proposed delete); R4-3 missing `shipment.reversed.v1` and `reverseSource` semantics; ten minor R4-4..13.
- External adapters undecided: OCR, e-invoice access points, container registry/CI, WORM store, payment provider, outbound email, AX/SAP extraction method; sub-processor DPAs/regions.
- Many AI thresholds/budgets are [ASSUMED — companion PRD]; token metering weight for cache reads (OQ-2); EU payroll reference country DE vs UK; named accountant for finance fixtures; IR/72 h breach/SOC2 evidence store design; network segmentation detail.
- Not provided to the document authors: per-wave PRD files, `roadmap/waves.json`, `ownership.json`, coverage oracles, `platform/go`; endpoint/entity lists are derived baselines.

## Compiled Provenance
Source: /Users/benjaminrussell/Desktop/Passage/docs/architecture.md (7844 lines, sha256 7db7f82e…)
Compiled: 2026-10-23 by sdlc plugin v2.17.7
