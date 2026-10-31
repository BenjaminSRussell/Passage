# Changelog

All notable changes to Passage are recorded in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html). Until 1.0.0 any minor
release may change public APIs.

## [0.1.0] - 2026-10-31

First tagged release of Passage, cut from `main` at `bf74ec27`. It is a **source-only
pre-release**:

- GitHub Actions is blocked by the repository owner's billing, so CI did not run on the
  release commit and **no container images or Helm charts were built, signed or published**.
- Every Passage service image digest in `deploy/envs/{dev,stage,prod}/apps/*.yaml` is a
  placeholder (`sha256:0000…0000`). Only pinned third-party images, such as Postgres, carry real
  digests. Nothing in
  `deploy/envs` can be rolled out as-is.
- The release-notes gate (`docs/ops/tools/check_release_notes.py`, PRD 9.50.15) requires real
  `sha256` image digests, so it applies from the first release that publishes images. This
  changelog does not take its place.
- Each service lists what it does not do yet in its own `NOT-IMPLEMENTED.md`
  (`services/<svc>/NOT-IMPLEMENTED.md`; platform-wide gaps are in `docs/ops/NOT-IMPLEMENTED.md`).
  Read it before relying on a module.

Most of the code came from Ben Russell's automated wave pipeline: one `PSG-NNxx` ticket per
slice of the 50-wave roadmap (`roadmap/waves.json`), 357 slices across all 50 waves. Many of the
later slices are marked "Haiku swarm draft, Opus review" in their commit subjects. Follow-up pull
requests #154 to #350 then hardened, wired and proved those slices. Each was reviewed on its own
before it was merged.

### Security

- Gateway: Envoy Gateway validates the JWT, strips caller-supplied identity headers and
  rate-limits per tenant (#82, #271). `/api` is stripped at the edge (#202).
- Tenancy: tenant JWTs are verified before OPA authorization, remote JWKS endpoints must use
  HTTPS, and a denial reports the least-privileged missing duty (Wave 2, #56). Nil and malformed
  tenant headers are rejected (#257).
- Legal-entity scope fails closed when empty (#256), through one shared helper that every
  service now uses (#315, #318). Bootstrap scope for org is in `org/0032`.
- Row-level security: tenant and legal-entity policies were added or hardened on revrec,
  purchasing, billing, pricing, finreport, planning, transport, metadata usage and sales number
  sequences (#206, #232, #243, #241, #246, #283, #292, #299, #310, #319). Identity tables use
  forced RLS (#249). There are `passage_app` cross-tenant tests for six services (#250),
  isolation tests for receivables, purchasing and revrec (#194, #201), and a repo-wide RLS
  coverage check (`tools/rlscheck`, #300).
- Database grants: explicit `passage_app` grants for every service schema (#195, #204, #255,
  #268, #269). Unused `DELETE` grants were revoked (#221).
- `SECURITY DEFINER` functions pin `search_path` against `pg_temp` table shadowing (#311,
  #314).
- Service-to-service calls use signed Ed25519 service-principal tokens (#226, #303, #305,
  #320), with a replay cache shared through Postgres that fails closed (#321, #336). Ledger
  and numbering duties were split so a caller signs only the exact duty it uses (#333).
  Payables' service grant on tax covers only the `Calculate withholding tax` duty (#350).
- Authorization policy: every service's duties are seeded in `policies/authz` and scanned
  (#313, #337). Duty contract tests run against the real Rego with `opa test` in CI (#78,
  #187). There is a dedicated tenant-admin duty for tenant-level writers (#298, #326), and
  the web shell gates nav entries and routes by duty (#297, #312).
- Supply chain: a Kyverno admission policy rejects unsigned or tag-referenced images, including
  through `pods/ephemeralcontainers`, and stage promotion verifies cosign signatures (#170,
  #172).
- Data protection: HR PII and one-time payee data are sealed with `platform/go/fieldcrypt`
  (#285, #98, #330). Compliance exports follow a server-side policy, and erasure pseudonyms
  are keyed (#282, #325).

### Added

Each wave line gives the number of `PSG-NNxx` slices that landed and the follow-up PRs that
extended that wave.

#### Phase 0 - Foundation & Platform Kernel

- **Wave 1 - Delivery Pipeline & Cluster Baseline** (1 slice): k3d and Argo CD baseline,
  `make deploy-local` (#58), stage digest promotion and rollback (#59). Follow-ups: Argo CD
  apps and dev databases for receiving and org (#186, #205), prod app-of-apps (#75 part a,
  #207).
- **Wave 2 - Identity, Tenancy & Authorization** (1 slice, #56): OPA authorization client and
  bundle. Follow-ups: tenant, user and legal-entity schema (#249), effective-permission engine
  with OPA parity over 100 resources (#81, #272), tenant, legal-entity and role-assignment APIs
  (#83, #288), user list, role and duty catalogue APIs and the `cmd/identity` server (#329),
  SCIM 2.0 Users and Groups provisioning (#339), admin-security web screens (#296, #338).
- **Wave 3 - Metadata Kernel** (1 slice, #54/#64): registry extensions, generated runtime,
  metadata-driven entity forms and the published entity-language schema. Follow-ups:
  `entity.published` through the outbox (#188), Debezium outbox connector (#190, #216), CDC
  publications and prerequisites (#223, #259, #331).
- **Wave 4 - Event Backbone, Workflow & Batch** (3 slices, #53/#104): Temporal, schema gate,
  partitioned batch runner, workflow API, notify and job monitor. Follow-ups: product explode
  and group-change jobs with checkpointed paging, plus a run dispatcher (#105, #280, #308),
  notify adapter credentials from ExternalSecret, and the notify distributor and event-backbone
  Argo apps (#106, #295, #328), a credit-limit approval workflow started from party change
  events (#212, #260).
- **Wave 5 - UX Shell, Number Sequences, Audit & Observability** (4 slices, #66): number
  sequence service, audit log, alerts and texts, UX shell and admin workspace, observability
  stack and CSV import/export. Follow-ups: web design system with tokens, primitives and a
  command palette (#165), Playwright and axe accessibility suite (#168).

#### Phase 1 - Enterprise Structure, Master Data & Engineering Data

- **Wave 6 - Enterprise Structure & Organization Model** (37 slices): reference data
  (countries, currencies, regions, languages, units and conversions, currency rounding),
  legal-entity lifecycle, operating units, sites, warehouses and locations, versioned postal
  addresses, fiscal calendars and period resolution, effective-dated hierarchies with closure
  table, as-of API and maker-checker, teams, name translations, and the org web module,
  reference-data screen and hierarchy designer. Follow-ups: 13-period 53-week FY2028 fixture
  (#164), demo seed package and address normalize and suggest adapter (#87, #209), editable
  hierarchy designer (#86, #211, #248).
- **Wave 7 - Global Party & Business Partner Model** (3 slices): party persistence, roles, tax
  registrations, terms and the core party API. Follow-ups: atomic merge with survivorship rules
  and a referential-integrity audit (#173), credit-limit increase approval with per-legal-entity
  configuration (#90, #208, #212, #230, #251).
- **Wave 8 - Product Master, Variants & Catalogs** (2 slices): product master and configurator,
  org site and warehouse check, numbering sequences.
- **Wave 9 - Engineering Data: BOMs, Routings, Resources & Change Management** (14 slices):
  resources, capabilities and work calendars, routings and route versions, BOM versions, ECOs
  with impact report, and the engineering screens.
- **Wave 10 - Data Migration Framework & Legacy Connectors** (8 slices): mapping DSL, versioned
  mapping specs, data templates with lineage, `rejectWhen` reason codes, source formats and
  code pages, signed configuration packages with diff and apply, and legacy key
  cross-reference lookup.

#### Phase 2 - Financial Core

- **Wave 11 - Universal Journal & Chart of Accounts** (10 slices): journal core, posting,
  reversal and storno, balances, account structures and dimensions, journal names and batches
  with approval, recurring journals, open-item accounts with manual and automatic clearing,
  dimension derivation, and the ledger transactions inquiry.
- **Wave 12 - Multi-Currency, Parallel Ledgers & Revaluation** (4 slices): fx service, rate
  store and providers, rate API and importer with outage fallback. Follow-ups: fx hardening and
  a deployable service with chart, OpenAPI and web screens (#92, #93, #166, #191, #196, #198,
  #220, #279, #324), atomic parallel-ledger posting, a ledger-specific posting duty with the
  20-document AC-12.4 fixture, and `ledger_lines` with per-ledger balancing (#91, #222, #214,
  #273, #287).
- **Wave 13 - Tax Engine** (1 slice): sales and use and VAT/GST calculation, provider failover
  and the settlement domain. Follow-up: a stateless `POST /v1/tax/withholding/calculate`
  endpoint with effective-dated rates, cumulative thresholds and minor-unit rounding. It sits
  behind the new duty `Calculate withholding tax` so payables can call it (#103, #350). The same
  PR audited the Wave 13 proof criteria: the 500-scenario matrix and provider failover are met,
  while settlement-to-GL and the SAF-T XSD check remain open.
- **Wave 14 - Period Close, Year-End & Allocations** (2 slices): period maintenance, reopen
  approval and the close checklist API. Follow-up: period resolver, with receiving deriving the
  fiscal period from the posting date (#96, #307).
- **Wave 15 - Financial Reporting, Statements & Audit Trail** (6 slices): statement
  definitions, runs and drill-down, derived hierarchies, cash flow statement, HTML, XLSX and CSV
  output, schedules, and the audit trail report. Proof: demo-year statements tie to the trial
  balance and the cash flow reconciles to the change in cash (Wave 15 AC1, #344).

#### Phase 3 - Inventory & Procure-to-Pay

- **Wave 16 - Inventory Ledger, Dimensions & On-Hand** (14 slices): inventory ledger and
  journals, reservations, cycle counting with freeze and review, transfer orders, daily
  inventory-to-ledger reconciliation, reports, as-of on-hand, and the inventory workspace.
- **Wave 17 - Purchasing** (9 slices): requisitions, purchase orders with versioned change
  management, agreements, RFQs and sealed bids, repricing, scheduling agreements, purchasing
  policy and PO four-eyes. Follow-up: invoicing-plan rows are marked invoiced on settlement
  (#289, #316).
- **Wave 18 - Receiving, Product Receipts & Three-Way Match** (6 slices): receipt lifecycle,
  arrival, registration and posting, GR/IR inquiry and the `receipt.posted` event. Follow-ups:
  real ledger and period-control adapters (#174), receipt and invoice routes with receiving and
  AP web workspaces (#95, #197), HTTP purchase-order adapter and registration checked against
  the PO line (#96, #215, #327), subsequent debit and credit, invoicing-plan and consignment
  settlement (#98, #286), evaluated receipt settlement with stochastic audit weighting (#97,
  #304, #334), one-time payees (#330), observability (#169).
- **Wave 19 - Accounts Payable, Settlement & Payments** (6 slices): ISO 20022 pain.001
  generation and pain.002 parsing with XSD conformance, NACHA ACH and returns, and payment-file
  previews. Follow-ups: metrics and settlement invariants (#167), preview and status endpoints
  (#219, #229, #242), vendor open items, FX valuation and payment proposals (#103, #254, #284),
  vendor and vendor-group aging reconciled to the AP control account (#101, #270, #301), an
  idempotent payment run through a Temporal workflow (#102, #306, #317, #323), holds, vendor
  block and proposal list routes (#332), distroless runtime image (#294).
- **Wave 20 - Inventory Costing & Supplier Collaboration** (6 slices): costing settlement,
  marking, valuation ledgers, costing versions and standard cost, cost transactions with GL
  tie-out, manual adjustments and reconciliation runs.

#### Phase 4 - Manufacturing & Planning

- **Wave 21 - Production Orders, WIP & Order-Based Costing** (16 slices): production orders,
  journals, WIP and variance, rework and genealogy, split and merge, order change management,
  co-products and by-products, subcontracting, KPIs and per-site parameters.
- **Wave 22 - Master Planning: MRP, MPS & Forecasting** (19 slices): MRP engine and pegging,
  forecasting and consensus, time fences, firming, DDMRP buffers, quota arrangements, coverage
  groups, plan version diff and simulation scenarios.
- **Wave 23 - Capacity Planning & Finite Scheduling** (13 slices): finite scheduling with a
  CP-SAT optimizer, Gantt, resource networks, setup matrices, campaigns, capacity overrides and
  backward scheduling. Proof: 30 OR-Library job-shop instances solved within a 5% gap (#23,
  #261).
- **Wave 24 - Process Manufacturing & Lean** (11 slices): formulas and batch orders, batch
  derivation and FEFO, lean flows and flow costing, kanban board and rules, and batch quality
  records.
- **Wave 25 - Shop Floor Execution, Time Registration & IoT** (12 slices): time registration
  and pay calculation, machine ingest, andon and OEE, process messages, kiosk flows,
  supervisor corrections and pay agreements.

#### Phase 5 - Order-to-Cash

- **Wave 26 - Pricing, Discounts & Trade Agreements** (10 slices): price and discount
  agreements with resolver and explain, price journals, rebates, commissions, trade funds,
  campaigns and free goods.
- **Wave 27 - Sales Orders, Quotations, ATP & Credit** (13 slices): sales orders with ATP and
  credit holds, quotations, supplementary items, allocation quotas, schedule lines and
  backorders, and per-legal-entity sales parameters.
- **Wave 28 - Fulfillment: Picking, Packing & Shipping** (11 slices): release, pick, pack and
  post, auto-pack and returnable packaging, waves, ASN and labels, the SSCC register, and
  delivered-not-invoiced and backorder reports.
- **Wave 29 - Billing, Invoicing & Revenue Recognition** (5 slices): free-text invoices,
  revenue contracts and recognition runs, credit notes, installment plans, and invoices from
  packing slips.
- **Wave 30 - Accounts Receivable, Collections, Returns & Customer Portal** (5 slices): cash
  application, collections and dunning, lockbox and bank statement import, deductions, payer
  matching and customer statements.

#### Phase 6 - Warehouse, Quality & Logistics

- **Wave 31 - Advanced Warehouse Management** (4 slices): location directive engine and
  simulator, hazmat and cross-dock checks, work execution, dock appointments.
- **Wave 32 - Mobile Warehouse Execution** (6 slices): print service, GS1 labels and scanning,
  PDF and PCL transports, offline queue and device flows.
- **Wave 33 - Quality Management & Nonconformance** (6 slices): quality orders and sampling,
  SPC and stability studies, nonconformance and 8D, supplier certificates and certificates of
  analysis.
- **Wave 34 - Transportation Management & Freight** (5 slices): rating, freight bills, tender
  cascade, load building, gate check, screening holds and route optimization.
- **Wave 35 - B2B Integration: EDI, e-Invoicing & Integration Hub** (5 slices): X12
  interchanges (855/856/810/997), UBL and Peppol invoices, message monitor and segment viewer
  with PII masking.

#### Phase 7 - Treasury, Assets & Controlling

- **Wave 36 - Cash & Bank Management, Reconciliation & Treasury** (4 slices): bank accounts,
  BAI2 and MT940 import, cash forecast, checks, facilities and loans.
- **Wave 37 - Fixed Assets & Leases** (5 slices): asset register, depreciation books and runs,
  disposal, leases and remeasurement, capex approvals, transfers and splits.
- **Wave 38 - Cost Accounting, Budgeting & Budget Control** (5 slices): budget control and
  encumbrances, allocations, earmarked funds, investment programs, cost object hierarchies.
- **Wave 39 - Product Costing, Cost Roll-Up & Variance Analysis** (5 slices): cost roll-up,
  production variances, costing sheets, profit-center valuation, stored cost components.
- **Wave 40 - Intercompany & Financial Consolidation** (4 slices): intercompany journals,
  charges and reconciliation, consolidation eliminations and translation, transfer-pricing
  evidence, stored trading partners. Proof (interim): a 3-entity group elimination fixture
  asserting gross debit and credit (#345). It is marked interim until accountant review (#349).

#### Phase 8 - Projects, People & Service

- **Wave 41 - Project Operations** (5 slices): projects and CPM scheduling, WBS, change orders,
  risk register, transactions and invoice proposals, forecast models.
- **Wave 42 - Human Resources Core** (5 slices): workers, positions and assignments, absence
  and accrual, benefits, skills and certificates, courses. Proof: 36-case accrual fixture for
  AC2 (#42, #258).
- **Wave 43 - Payroll Engine & Expense Management** (5 slices): payroll and expense engines,
  per diem, cash advances, travel requisitions and card statement matching.
- **Wave 44 - Enterprise Asset Management** (5 slices): maintenance assets, plans and work
  orders, fault catalogs, work permits and lockout/tagout, functional locations, rounds and
  KPIs.
- **Wave 45 - Service Management & Field Service** (5 slices): service cases, agreements and
  SLA clocks, installed base, generic case management, warranty claims and email-to-case.

#### Phase 9 - Intelligence, Compliance & Go-Live

- **Wave 46 - Data Platform, Analytics & Embedded BI** (4 slices): metric catalog, scoped Cube
  queries, data quality, archive, dashboards and lineage. Follow-up: a read-classified POST
  allowlist so analytics can serve all-scope reads (#293, #322).
- **Wave 47 - AI Copilots, Agents & Automation** (5 slices): MCP tool gateway, agent run
  engine with governance and an eval gate, model adapters with failover, Bedrock adapter, kill
  switch and copilot screens.
- **Wave 48 - Globalization, Localization & Compliance Controls** (5 slices): segregation of
  duties, country packs, breach clocks, e-signature, sanctions screening, privacy subject
  requests and access certification.
- **Wave 49 - Migration Factory** (5 slices): pack runs with control totals, cutover runbooks,
  quarantine release, rollback re-keying export, GL opening balances pack.
- **Wave 50 - Scale Hardening, Multi-Region & Production Go-Live** (11 slices): multi-region
  chart with fencing and MirrorMaker 2, k6 load and soak campaigns, expand/contract migration
  linter, OpenAPI and AsyncAPI compatibility gates, game-day and chaos tooling, the
  release-notes gate, cost and capacity calculators, runbooks, and implementer, administrator
  and functional guides.

### Changed

- CI: GitHub Actions run volume was reduced (#150). Postgres-backed store tests run in CI, with
  an org idempotency-key cap and NetworkPolicy (#88, #193). A golangci-lint matrix covers the
  clean Go modules (#169), xmllint is installed so the pain.001 XSD tests run (#171), and the
  configurator's Python suite runs in CI (#154).
- `services/ledger` serves its API under `/v1/ledger` (#199, #202).
- The `warehouse-mobile` toolchain matches `apps/web`, with a lockfile and a CI build (#281,
  #309).
- Fixes: the configurator's CP-SAT `OnlyEnforceIf` now works with OR-Tools 9.11+ (#154). The
  dev DB secret is recreated when `APP_PASSWORD` is missing (#156). Migration catalog checks are
  scoped by schema (#224, #233). Org seed and postal replay gaps were fixed (#235, #253). Ledger
  sets enforce their constraints and take locks in a consistent order (#237, #245). Payables
  classifies XSD validator interruptions and has a preview timeout and 503 contract (#239,
  #242). Smaller fx, org-web and receiving follow-ups landed in #227/#228, #234, #236, #238,
  #240, #244, #247 and #252.
- Docs: README getting-started guide for newcomers (#192).

### Dependencies

- Web: React 19 and react-router 7 (#189, supersedes #162 and #163). Bundled Dependabot bumps
  for Go modules, GitHub Actions and web test tooling (#185), and for `apps/web` (#274).
- Dependabot is configured for Go modules, npm, pip and GitHub Actions (#155).
- Configurator: OR-Tools 9.11+ supported (#154).

### Upgrade notes / operator actions

There is no earlier release, so this is a first install rather than an upgrade. Before any
environment runs 0.1.0:

1. **Build, sign and pin images.** Once Actions billing is restored, re-run CI on the tag, then
   build every service image and sign it with cosign. Replace each placeholder digest in
   `deploy/envs/{dev,stage,prod}/apps/*.yaml` with the real digest. The Kyverno admission
   policy (#170, #172) rejects unsigned or tag-referenced images.
2. **Apply each service's migrations in filename order** (`services/<svc>/migrations`). Pay
   particular attention to the security-relevant ones:
   - Identity schema and SCIM: `identity/0001_identity.sql`, `identity/0002_grants.sql`,
     `identity/0007_scim.sql`.
   - Grants: the `NNNN_grants.sql` file in every service, for example `pricing/0008_grants.sql`.
   - Legal-entity RLS: `org/0032_legal_entity_bootstrap_scope.sql`,
     `billing/0006_child_legal_entity_scope.sql`, `pricing/0007_child_legal_entity_scope.sql`,
     `purchasing/0010_child_legal_entity_scope.sql`, `revrec/0004_outbox_and_child_scope.sql`,
     `revrec/0005_revenue_schedule_direct_scope.sql`, `finreport/0008_legal_entity_scope.sql`,
     `planning/0012_legal_entity_scope.sql`,
     `planning/0013_legal_entity_scope_buffers_coverage.sql`,
     `transport/0006_legal_entity_scope.sql`, `transport/0007_legal_entity_scope_children.sql`,
     `transport/0008_legal_entity_scope_voyages_routes.sql`,
     `metadata/0005_usage_legal_entity_scope.sql`,
     `sales/0010_number_sequence_legal_entity_scope.sql`.
   - `SECURITY DEFINER` search path: `close/0004_definer_search_path.sql`.
   - Service-token replay tables. Apply these before deploying a target build, because a target
     refuses to start without its table: `close/0005`, `fx/0006`, `inventory/0014`,
     `ledger/0017`, `numbering/0003`, `payables/0011`, `product/0002` and `purchasing/0013`,
     each named `*_service_token_replay.sql`.
   - CDC: `metadata/0004_cdc_publication.sql`, `metadata/0006_cdc_role_grants.sql`,
     `batch/0004_cdc_publication.sql`, `batch/0011_cdc_role_grants.sql`. The role-grant
     migrations grant to `debezium_cdc` only if that role already exists. If a service migrated
     before its database had the role, delete that `schema_migrations` row and restart (see
     `deploy/postgres/README.md`).
   - One-time payees: `receiving/0010_one_time_payee.sql`.
3. **Service-token keys** (`deploy/gateway/README.md`). Give each caller (receiving, payables,
   party) an Ed25519 seed Secret (`serviceIdentity.signingKeySecret`, key `signing-key`) and a
   `serviceIdentity.keyId`. Give each target (ledger, close, numbering, purchasing, inventory,
   fx) the callers' public keys in `serviceIdentity.publicKeys`. Roll out targets first, then
   their replay migrations, then callers. Roll back in reverse order (#321).
4. **Field-encryption keys.** Set `FIELDCRYPT_KEYS` or `FIELDCRYPT_KEYS_FILE` and
   `FIELDCRYPT_CURRENT_KEY_ID` for hr (it refuses to start without them), for receiving and
   payables (they share a service key; without it, one-time payees and payables proposals with bank
   details or a payment-run start return 503) and for compliance
   (erasure preview returns 503 `PRV-1211` without it). Keep the legacy `HR_PII_KEY` until every
   HR row is re-sealed. Do not rotate the compliance current key while erasure requests are open.
5. **CDC credentials.** Each CDC database needs Vault entries
   `<prefix>/<release>-postgres/{postgres-password,app-password,cdc-password}` and a matching
   `debezium-cdc-<release>` Secret in the `kafka` namespace (`deploy/envs/<env>/kafka-secrets`).
6. **Notify adapters.** Enabling `NOTIFY_SMTP_ENABLED` or `NOTIFY_WEBHOOK_ENABLED` needs that
   adapter's credentials, from the batch chart's ExternalSecret or `NOTIFY_SECRETS_DIR`. Startup
   fails if they are missing.
7. **SCIM (optional).** SCIM is mounted only when `IDENTITY_SCIM_ISSUER_BASE`,
   `IDENTITY_SCIM_AUDIENCE` and `IDENTITY_OPA_URL` are all set. Assign the duty `Provision users
   via SCIM` to the IdP's service account.

### Known limitations

- **No published artifacts.** No images, charts, SBOMs or signatures exist for 0.1.0, and the
  digests in `deploy/envs` are placeholders. CI did not run on the release commit because
  Actions is billing-blocked.
- **Per-service gaps.** Each service's `NOT-IMPLEMENTED.md` lists what is missing. Many slices
  are partial implementations of their wave's functional requirements.
- **Identity is incomplete.** The Keycloak realm and push (#80), OIDC/SAML federation and MFA,
  OPA bundle build and distribution, and gateway host-to-tenant mapping (#213) are not
  implemented. mTLS between pods is a follow-up to the service tokens.
- **Wave proof criteria are mostly open.** Only these are proved on this commit: Wave 15 AC1
  (#344), Wave 23 (#261), Wave 42 AC2 (#258), Wave 12 AC-12.4 (#273), two of the four Wave 13
  criteria (#350) and the interim Wave 40 fixture (#345). No phase-exit stakeholder sign-off
  has taken place.
- **Tax is not deployable yet.** The tax service has no Helm chart, Dockerfile or store (slice
  B in `services/tax/NOT-IMPLEMENTED.md`). Once it is deployed, it needs payables' service-token
  public key before payables can call withholding.
- **Proof PRs not merged into 0.1.0:** #352 (Wave 7 VIES adapter and vendor master), #346 (Wave
  36 treasury), #347 (Wave 20 costing), #348 (Wave 31 warehouse), #351 (Wave 14 close) and
  #262 (platform-health release persistence and promote/rollback recording, #75).
- **Open follow-up issues:**
  - #342: costing and productcost mount the same `/v1/costing` routes with different duties.
  - #343: SCIM follow-ups from the #339 review (admin-end deadlock, silent cap on group members).
  - #349: the IAS 21 FX treatment in the FR-40.9 consolidation eliminations needs accountant
    review. Until then the Wave 40 fixture (#345) stays interim.

[0.1.0]: https://github.com/BenjaminSRussell/Passage/releases/tag/v0.1.0
