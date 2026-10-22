# Passage — Product Requirements Document

| | |
|---|---|
| **Status** | Draft v0.2 — for review by Ben Russell and Zach Demanche |
| **Date** | 2026-10-21 |
| **Owners** | @BenjaminSRussell (product), @zdemanche (engineering/security) |
| **Method** | Moonaan Scribe requirements workflow (understand → research → draft → validate → publish). Jira/Confluence research was unavailable this session; research covered public AX/SAP documentation and both owners' GitHub accounts (no prior ERP work found). |
| **Companion** | [roadmap/waves.json](../roadmap/waves.json) is the machine-readable source for the 50 waves; [roadmap/README.md](../roadmap/README.md) is generated from it. |
| **Thesis** | Emulate the top-tier manufacturing ERPs (S/4HANA, D365 F&SCM, Infor LN, Epicor Kinetic, IFS, Plex) feature-for-feature, then be **10× easier, faster, more accurate and more open** — see [Part 0A](prd/part-00-thesis.md). |

## Document map

This master document (Part 0) holds the problem, goals, scope, delivery model, phase-level requirements and NFRs. The detailed parts hold wave-level requirements and are assembled into `docs/PRD-full.md` by `scripts/build_prd.py`.

| Part | File | Content |
|---|---|---|
| 0 | `docs/PRD.md` (this file) | Problem, goals, personas, principles, scope map, delivery model, phase-level FRs, NFRs, risks |
| 0A | [prd/part-00-thesis.md](prd/part-00-thesis.md) | The 10× thesis: pain-point ledger (usability, speed, accuracy, openness), interoperability contract, what we keep from AX and SAP |
| 1 | [prd/phase-0-foundation.md](prd/phase-0-foundation.md) | Waves 1–5: pipeline, identity/tenancy, metadata kernel, events/workflow, shell/audit/observability |
| 2 | [prd/phase-1-master-data.md](prd/phase-1-master-data.md) | Waves 6–10: enterprise structure, party model, products, engineering data, migration framework |
| 3 | [prd/phase-2-financial-core.md](prd/phase-2-financial-core.md) | Waves 11–15: universal journal, currencies/ledgers, tax, close, reporting |
| 4 | [prd/phase-3-procure-to-pay.md](prd/phase-3-procure-to-pay.md) | Waves 16–20: inventory ledger, purchasing, receiving/matching, AP/payments, costing/supplier portal |
| 5 | [prd/phase-4-manufacturing.md](prd/phase-4-manufacturing.md) | Waves 21–25: production orders, MRP, scheduling, process/lean, shop floor/IoT |
| 6 | [prd/phase-5-order-to-cash.md](prd/phase-5-order-to-cash.md) | Waves 26–30: pricing, orders/ATP, fulfillment, billing/rev rec, AR/collections — go-live candidate |
| 7 | [prd/phase-6-logistics.md](prd/phase-6-logistics.md) | Waves 31–35: WMS, mobile, quality, transportation, EDI/e-invoicing/integration hub |
| 8 | [prd/phase-7-treasury-controlling.md](prd/phase-7-treasury-controlling.md) | Waves 36–40: cash/bank, fixed assets, budgeting/cost accounting, product costing, intercompany/consolidation |
| 9 | [prd/phase-8-projects-people-service.md](prd/phase-8-projects-people-service.md) | Waves 41–45: projects, HR, payroll/expenses, EAM, field service |
| 10 | [prd/phase-9-intelligence-golive.md](prd/phase-9-intelligence-golive.md) | Waves 46–50: data platform, AI agents, localization/compliance, migration factory, scale/go-live |
| 11 | [prd/appendices.md](prd/appendices.md) | Glossary, cross-module data model, event/API conventions, security model, NFR detail, numbering, localization, testing standards, traceability, references, competitive benchmark |

Each wave section follows the fixed template in [prd/TEMPLATE.md](prd/TEMPLATE.md): scope and pain points solved → AX/SAP parity map → functional requirements → data model → APIs/events → UX → business rules → acceptance criteria → test plan → deployment → migration → dependencies/risks/open questions.

---

## 1. Problem statement

Microsoft Dynamics AX (Axapta → AX 2012 → D365 Finance & Supply Chain) and SAP (ECC → S/4HANA) are the systems of record for most mid-to-large manufacturers, distributors and services firms. Both are:

- **Closed and expensive to change** — customization lives in proprietary languages (X++, ABAP) and licensing/upgrade costs dominate TCO.
- **Architecturally dated** — monolithic application servers, synchronous batch frameworks, and vendor-locked cloud operation (LCS, RISE).
- **Hard to integrate and extend with AI** — bolted-on copilots that can't act safely across the system.

Passage is a ground-up rebuild of the complete feature surface of AX/SAP as an open-architecture, cloud-agnostic platform: Kubernetes-native, Postgres-per-domain, Kafka-eventing, metadata-driven, with AI agents as first-class, governed actors.

## 2. Goals and non-goals

### Goals
1. **Feature parity**: every AX 2012 / D365 F&SCM and SAP ECC / S/4 module in scope (§5) has an equivalent in Passage, mapped wave-by-wave.
2. **Provable delivery**: every one of the 50 waves ends with an artifact that is (a) **provable** — automated tests and fixtures that demonstrate correctness, (b) **testable** — a stakeholder can exercise it in a running environment, and (c) **deployable** — it ships to a Kubernetes environment through the GitOps pipeline.
3. **Sequential, top-down**: waves are strictly ordered; each wave depends only on earlier waves (dependency graph is a chain with stated back-references). A manufacturer can go live at the end of wave 30; full AX/SAP parity at wave 50.
4. **Cloud-agnostic and self-hostable**: runs on any conformant Kubernetes (EKS/GKE/AKS/bare-metal/k3d) with no provider-specific managed services in the critical path.
5. **Extensible without forking**: customers extend entities, workflows, forms and reports through metadata and sanctioned extension points, never by overlayering code.
6. **Financially rigorous**: a single append-only universal journal is the only source of truth; every subledger reconciles to it by automated check.
7. **10× on four measured axes** (defined in Part 0A): easier (≤ 1/10 the clicks per task; new user productive in < 30 min), faster (MRP incremental < 2 min, continuous close, real-time statements), more accurate (zero unexplained subledger-to-GL breaks, < 0.5% duplicate master data), open (any system on the customer's network connects in < 1 day through REST/GraphQL/Kafka/webhooks/MCP/EDI/OPC-UA/SFTP with one identity and one policy engine).
8. **Best-of-breed manufacturing**: planning, scheduling, shop floor, quality, costing and warehouse are benchmarked against S/4HANA, D365 SCM, Infor LN, Epicor Kinetic, IFS Cloud and Plex (Appendix L), not only against AX 2012.

### Non-goals (v1)
- Replacing CRM/marketing automation suites (we integrate with Salesforce/HubSpot; a native CRM is v2).
- Retail POS and e-commerce storefront (we integrate with Shopify/Medusa; native POS is v2).
- Country packs beyond the first set (US, UK, DE, FR, IN, MX, CA) — framework ships in wave 48; more packs are v1.x.
- Supporting SAP's or Microsoft's proprietary extension languages. Migration tooling moves **data**, not X++/ABAP code.

## 3. Users and personas

| Persona | Primary jobs | Waves that serve them first |
|---|---|---|
| Platform engineer / SRE | Deploy, observe, upgrade, scale, secure | 1–5, 48, 50 |
| System administrator | Tenants, users, roles, extensions, number sequences, audit | 2, 3, 5, 48 |
| Master-data steward | Parties, products, BOMs, org structure, migration | 6–10 |
| Controller / accountant | Journal, close, statements, tax, FX, subledgers | 11–15, 19, 30, 36–40 |
| Buyer / AP clerk | Requisition to payment | 17–20 |
| Production planner / supervisor | Orders, MRP, scheduling, shop floor | 21–25 |
| Sales rep / order desk / AR clerk | Quote to cash | 26–30 |
| Warehouse manager / operator | WMS, mobile, shipping, quality, transport | 28, 31–35 |
| Project manager / HR / payroll admin | Projects, people, payroll, assets, service | 41–45 |
| Analyst / executive | Dashboards, KPIs, copilots | 46–47 |
| Implementation partner | Migration, localization, cutover | 10, 48–50 |

## 4. Product principles

1. **Journal first.** Nothing is financially true until it is in the universal journal. Subledgers are projections; they reconcile automatically every day.
2. **Metadata over code.** Entities, forms, workflows, reports and extensions are data. Code generators produce schema, API, clients and UI from it.
3. **Events are the integration surface.** Every state change is an immutable CloudEvent on Kafka via transactional outbox — no dual writes, no polling.
4. **Everything is tenant-scoped and policy-authorized.** Postgres RLS + OPA on every request; agents inherit (never exceed) their user's permissions.
5. **Idempotent, replayable, auditable.** Posting APIs take idempotency keys; audit is derived from CDC, not from application code that can forget.
6. **Ship the thin vertical slice.** A wave is done when a stakeholder can run the business process on a deployed environment with reconciled numbers — not when the code is written.

## 5. Scope map — AX/SAP modules → Passage phases

| AX / D365 module | SAP module | Passage phase (waves) |
|---|---|---|
| AOT, security, workflow, batch, LCS, number sequences, DB log | DDIC, PFCG, Business Workflow, SM36, STMS, SNRO, CDHDR | **0 Foundation (1–5)** |
| Org model, global address book, products, BOM/route, DIXF | Enterprise structure, Business Partner, material master, CS/CA, LSMW | **1 Master data (6–10)** |
| General ledger, dimensions, currencies, tax, period close, financial reporting | FI-GL/ACDOCA, ledgers, FTXP, OB52, FSV | **2 Financial core (11–15)** |
| Inventory management, procurement & sourcing, AP, inventory costing | MM-IM, MM-PUR, LIV, FI-AP, F110, material ledger | **3 Inventory & P2P (16–20)** |
| Production control, master planning, scheduling, process/lean, MES | PP, PP-PI, REM, KANBAN, PP/DS, MII | **4 Manufacturing & planning (21–25)** |
| Trade agreements, sales, shipping, invoicing, rev rec, AR, collections | SD pricing, SD orders, LE-SHP, billing, RAR, FI-AR, FSCM | **5 Order-to-cash (26–30)** |
| Warehouse management, mobile app, quality, TMS, AIF/ER/EDI | EWM, RF, QM, TM, IDoc/PI, DRC | **6 Warehouse, quality & logistics (31–35)** |
| Cash & bank, fixed assets, budgeting/cost accounting, costing sheets, intercompany/consolidation | Cash mgmt, FI-AA, CO, product costing, Group Reporting | **7 Treasury, assets & controlling (36–40)** |
| Projects, HR, payroll, expense, asset management, service | PS, HCM/SF, PY, Concur, PM, CS/FSM | **8 Projects, people & service (41–45)** |
| Entity store/Power BI, Copilot, localizations/SoD, upgrade tooling, LCS prod | BW/SAC, Joule, country versions/GRC, conversion tools, Basis | **9 Intelligence, compliance & go-live (46–50)** |

Full per-wave mapping with references is in [roadmap/waves.json](../roadmap/waves.json) (`replaces` field).

## 6. Delivery model — what a "wave" is

A wave is a 4–8 week increment with four mandatory outputs, enforced by the wave issue template and the Definition of Done:

| Output | Definition | Evidence required to close the wave |
|---|---|---|
| **Deliverable** | The capabilities listed in the wave's `deliverable` array, built on the agreed stack. | Merged PRs linked from the wave issue. |
| **Proof** | The automated tests, fixtures, property tests and performance targets in `proof`. | CI run link; fixture datasets committed; perf report. |
| **Deploy** | What is running where, as stated in `deploy`. | Argo CD app healthy in `stage`; demo URL; recorded walkthrough for phase-exit waves. |
| **References** | The AX/SAP documentation and open-source/standards we built against. | Linked in the wave issue; ADRs for any deviation. |

**Sequencing rules**
- Wave *n* may start when wave *n−1* has passed Proof and Deploy; overlapping *build* is allowed, overlapping *close* is not.
- Every 5th wave is a **phase-exit wave** with a stakeholder sign-off (demo + reconciliation evidence).
- Wave 30 is the **first go-live candidate** (manufacturer buy → make → sell → collect). Wave 50 is **production go-live + v1.0**.

## 7. Functional requirements (phase level)

Phase-level requirements below are the contract; wave-level detail (deliverables, proofs, references) is normative in `roadmap/waves.json` and rendered in `roadmap/README.md`.

### FR-0 Foundation (waves 1–5)
- **FR-0.1** The platform deploys to any conformant Kubernetes via GitOps with signed images, SBOMs and digest-pinned rollback.
- **FR-0.2** Identity is federated (OIDC/SAML/SCIM); authorization is AX-style roles/duties/privileges compiled to OPA policies; tenant isolation is enforced by Postgres RLS.
- **FR-0.3** Business entities are declared in metadata that generates schema, API, clients, validation and UI; customers extend entities without code.
- **FR-0.4** All state changes publish CloudEvents via transactional outbox; approvals and batch jobs run on a durable workflow engine with timers, retries and human tasks.
- **FR-0.5** A single web shell renders any entity as accessible (WCAG 2.1 AA) list/detail/grid; number sequences support gap-free mode; audit is field-level and immutable; every request is traceable end-to-end.

### FR-1 Master data (waves 6–10)
- **FR-1.1** Legal entities, operating units, sites, warehouses and effective-dated multi-purpose hierarchies; fiscal calendars; ISO reference data; UoM conversions.
- **FR-1.2** One party per real-world organization/person with layered roles, duplicate detection and merge, tax-id validation, governance workflow.
- **FR-1.3** Products with product/storage/tracking dimension groups, released products per entity, attributes/classification, GS1 identifiers, configurator.
- **FR-1.4** Multi-level versioned BOMs and formulas, routings/operations/resources/calendars, engineering change management with where-used.
- **FR-1.5** A migration framework with AX and SAP connectors, declarative mapping, validation, reconciliation and idempotent re-runs.

### FR-2 Financial core (waves 11–15)
- **FR-2.1** Append-only double-entry universal journal with dimensions, account structures, idempotent atomic posting, balance projections rebuilt from the journal.
- **FR-2.2** Three-currency amounts per line, rate types/providers, parallel ledgers, FX revaluation and translation.
- **FR-2.3** Tax determination/calculation/posting for sales & use, VAT/GST and withholding; pluggable external providers; settlement and statutory extracts (SAF-T).
- **FR-2.4** Period control per ledger × module, close checklists, allocations, accrual schemes, year-end close.
- **FR-2.5** Statement designer and standard statements with drill-down to source document; audit trail reporting.

### FR-3 Inventory & P2P (waves 16–20)
- **FR-3.1** Immutable inventory transactions with physical/financial status and dimensions; journals; reservations; cycle counting; GL integration.
- **FR-3.2** Requisition → RFQ → agreement → PO with policy-driven approvals, versioned change management and vendor pricing.
- **FR-3.3** Receiving with GR/IR accrual; vendor invoice capture; 2/3-way matching with tolerances and exception workflow; ERS.
- **FR-3.4** AP open-item settlement, payment proposals, ISO 20022/NACHA/check payment media, positive pay, prepayments.
- **FR-3.5** FIFO/LIFO/weighted-average/standard costing with inventory close and GL reconciliation; supplier portal.

### FR-4 Manufacturing & planning (waves 21–25)
- **FR-4.1** Production order lifecycle with picking, route consumption, RAF, WIP and variance settlement; subcontracting.
- **FR-4.2** MRP/MPS with coverage groups, planned orders, action/delay messages, pegging, statistical forecasting.
- **FR-4.3** Finite/infinite capacity scheduling with Gantt and optional CP-SAT optimization.
- **FR-4.4** Process manufacturing (formulas, potency, co/by-products, catch weight, batch attributes) and lean (production flows, kanban).
- **FR-4.5** Shop-floor kiosk with time registration, IoT ingestion (OPC-UA/MQTT), OEE, offline tolerance.

### FR-5 Order-to-cash (waves 26–30)
- **FR-5.1** Deterministic pricing engine with condition tables/access sequences, discounts, promotions, rebates, explain.
- **FR-5.2** Quotations, sales orders, ATP/CTP, credit management, holds, multi-channel intake.
- **FR-5.3** Release, pick/pack/ship, packing slips with COGS, carrier integration, drop-ship.
- **FR-5.4** Invoicing (one-off, consolidated, subscription), ASC 606/IFRS 15 revenue recognition.
- **FR-5.5** Cash application, collections/dunning/disputes, RMA, rebate settlement, customer portal with payments. **Go-live candidate.**

### FR-6 Warehouse, quality & logistics (waves 31–35)
- **FR-6.1** Location directives, work templates, waves, license plates, replenishment, load planning.
- **FR-6.2** Mobile scanner app with configurable flows, GS1 parsing, offline queue, label printing.
- **FR-6.3** Quality orders by rule, sampling, usage decisions, inventory blocking, NCR/CAPA, CoA, supplier scorecards.
- **FR-6.4** Load/route planning, rating, tendering, tracking, freight reconciliation, landed cost.
- **FR-6.5** EDI (X12/EDIFACT), Peppol e-invoicing, integration hub (APIs, webhooks, catalog, developer portal).

### FR-7 Treasury, assets & controlling (waves 36–40)
- **FR-7.1** Bank accounts, statement import and auto-reconciliation, cash positioning/forecasting, bank connectivity with vaulted keys.
- **FR-7.2** Fixed assets with multi-book depreciation, full transaction set, CIP from projects, IFRS 16 leases.
- **FR-7.3** Budgeting with workflow and budget control on commitments; cost accounting ledger with allocations and ABC.
- **FR-7.4** Costing sheets, BOM cost roll-up, standard cost activation and revaluation, variance decomposition.
- **FR-7.5** Intercompany trade/accounting with mirror documents and reconciliation; group consolidation with eliminations and translation. **Finance parity.**

### FR-8 Projects, people & service (waves 41–45)
- **FR-8.1** WBS, resources, hour/expense/item journals, T&M/fixed-price/milestone billing, WIP and POC accounting, EVM.
- **FR-8.2** Workers/positions/jobs with effective dating, absence, benefits, self-service, talent basics, PII controls.
- **FR-8.3** Country-pluggable gross-to-net payroll (US + one EU reference) and expense management with policies and card feeds.
- **FR-8.4** Functional locations/assets, preventive and condition-based plans, work orders with costs to asset/GL, IoT triggers.
- **FR-8.5** Installed base, agreements/SLAs, cases, field work orders, dispatch optimization, technician mobile app.

### FR-9 Intelligence, compliance & go-live (waves 46–50)
- **FR-9.1** CDC lakehouse (Iceberg) + semantic layer with governed metrics, embedded dashboards, document designer, catalog/lineage.
- **FR-9.2** MCP-exposed APIs; copilot UI; governed agents with approval gates, evals in CI, cost budgets, model-agnostic provider layer.
- **FR-9.3** Localization framework and first country packs; electronic reporting designer; SoD engine; SOC 2 control evidence; privacy requests.
- **FR-9.4** Transactional migration packs for AX and SAP, parallel-run diff harness, cutover runbooks, coexistence adapters.
- **FR-9.5** Scale/perf targets met, multi-region DR, operations and documentation complete, first customer in production, v1.0 released.

## 8. Non-functional requirements

| Area | Requirement |
|---|---|
| Availability | 99.9% per tenant (v1), 99.95% target with multi-region (wave 50). RPO 5 min, RTO 1 h. |
| Performance | Journal posting 5k lines/s sustained; generated entity reads p95 < 50 ms; pricing p95 < 20 ms; order intake p99 < 500 ms; dashboard lag p95 < 60 s. |
| Scale | 10k concurrent users; 1M journal lines/h; 100k order lines/h; 50k-item MRP in < 10 min. |
| Security | OIDC/SAML/SCIM; OPA authorization on every request; Postgres RLS; signed images/SBOM; secrets in Vault; SoD engine; pen-test clean at high/critical before wave 48 exit. |
| Compliance | SOC 2 TSC control mapping, GDPR/CCPA subject requests, SAF-T, Peppol, ISO 20022, GS1. |
| Accessibility | WCAG 2.1 AA for every generated form and every hand-built workspace (axe in CI). |
| Observability | OpenTelemetry traces/metrics/logs for 100% of services; SLO dashboards; alerting. |
| Portability | No provider-managed service in the critical path; Helm + Terraform/OpenTofu modules per provider; k3d local parity. |
| Extensibility | Entity/enum/field/workflow/form/report extensions as metadata; versioned public APIs with additive-change guarantees. |
| Data integrity | Every subledger reconciles to the universal journal daily by automated check; no negative inventory unless policy allows; gap-free fiscal sequences. |
| Auditability | Field-level immutable audit from CDC; every agent action logged with inputs/outputs/approver. |

## 9. Technical architecture (summary — see ADR-0001)

- **Runtime**: Kubernetes (any conformant distribution), Argo CD GitOps, Envoy Gateway, cert-manager.
- **Services**: Go for ledgers/engines (journal, inventory, pricing, MRP, scheduling), TypeScript (Node) for workflow/integration/UI-adjacent services, Python for forecasting/ML/agent evals.
- **Data**: Postgres per domain (CloudNativePG), RLS, Atlas migrations generated from metadata; Apache Iceberg lakehouse with Trino/ClickHouse for analytics.
- **Eventing**: Kafka (Strimzi), Debezium CDC + transactional outbox, CloudEvents + AsyncAPI, schema registry.
- **Workflow**: Temporal for approvals, batch, agents.
- **Identity/authz**: Keycloak, OPA/Rego.
- **Frontend**: React + module federation shell; PWA for shop floor, warehouse and field mobile.
- **AI**: MCP server over Passage APIs; Claude as default model via a provider-agnostic layer; eval harness in CI.
- **Observability**: OpenTelemetry → Prometheus/Tempo/Loki/Grafana.

## 10. Success metrics

| Milestone | Metric |
|---|---|
| Wave 5 | Platform demo: deploy → login → extend entity → approve workflow → trace a request, all live. |
| Wave 15 | A period closed in Passage with statements that tie to the trial balance to the cent. |
| Wave 30 | Demo manufacturer runs one simulated month buy → make → sell → collect; all subledgers reconcile; stakeholder sign-off. |
| Wave 40 | Group consolidation produced; finance parity sign-off by a practicing controller. |
| Wave 50 | First customer month-end in production; load/DR targets met; v1.0 released. |
| Ongoing | Every wave: CI green, proofs committed, Argo app healthy in stage, references linked. |

## 11. Risks and mitigations

| Risk | Mitigation |
|---|---|
| Scope is enormous (50 waves) | Strict sequencing; each wave independently demoable; phase-exit sign-offs; `waves.json` is the single plan of record and is re-estimated each phase exit. |
| Accounting correctness | Fixture sets reviewed by a practicing accountant per finance wave; daily automated subledger-to-GL reconciliation from wave 16 onward. |
| Postgres-per-domain vs cross-domain transactions | Sagas over Temporal + outbox; the journal posting API is the only cross-domain financial truth; no distributed 2PC. |
| Cloud-agnostic ambition increases ops burden | Reference stack is entirely CNCF/open-source with Helm charts; provider-specific Terraform modules are thin and optional. |
| Payroll/tax/localization legal exposure | Ship provider adapters (Avalara/ADP-style) alongside internal engines; country packs gated by regulator-schema fixtures. |
| AI agent misuse | Agents inherit user permissions via OPA, approval gates, evals in CI, kill switch, cost budgets. |
| Two-person team | GitHub Projects + wave issues as the operating system; AI-assisted generation (Moonaan sdlc `generate-architecture` → `generate-backlog` → `generate-code`) per wave. |

## 12. Open questions

1. Licensing model for v1.0 (open-core vs. source-available vs. commercial) — decide before wave 35 (integration hub/public APIs).
2. First real customer: identify by wave 20 to shape go-live scope at wave 30.
3. Which EU country is the payroll/localization reference (DE vs. UK)?
4. Hosting of the demo environments (which provider first, even though the stack is agnostic)?

## 13. Next steps in the Moonaan SDLC pipeline

1. **This PRD** is the requirements source.
2. Run `/sdlc:generate-architecture docs/PRD.md docs/architecture/` per phase to produce the architecture document and compiled fact sheets.
3. Run `/sdlc:generate-backlog` per wave to turn `waves.json` entries into epics/stories with dependency ordering (GitHub Issues are the backlog store for now; Jira when the Moonaan proxy is reachable).
4. Run `/sdlc:generate-code` per story.

---

### Validation checklist (Scribe step 4)

- [x] Every functional requirement is traceable to a wave with a provable, testable, deployable output.
- [x] Every wave names the AX and SAP features it replaces (grounding).
- [x] Every wave links external references (standards, vendor docs, open-source implementations).
- [x] Non-functional requirements are measurable.
- [x] Sequencing and go-live milestones are explicit.
- [ ] Reviewed by @zdemanche.
- [ ] Accountant review of finance-wave fixtures (owner TBD).

\newpage

# Part 0A — The 10× Thesis: What Top-Tier Manufacturing ERPs Get Wrong and How Passage Fixes It

Passage is not a cheaper AX or a friendlier SAP. It emulates the full capability surface of the best manufacturing ERPs — SAP S/4HANA, Dynamics 365 Finance & Supply Chain, Infor LN / CloudSuite Industrial, Epicor Kinetic, IFS Cloud and Plex — and then removes the structural reasons those systems are slow, error-prone, expensive to use and closed. "10×" is measured, per wave, on four axes:

| Axis | What we measure | Baseline (typical AX 2012 / D365 / S/4 deployment) | Passage target |
|---|---|---|---|
| **Easier** | Clicks and screens per task; training hours to competence; share of tasks completed without a super-user | Sales order with 5 lines: 40–60 clicks across 4 forms; AP clerk training 3–5 days | ≤ 1/10 of clicks; a new user completes a guided task in < 30 min |
| **Faster** | Latency of the business answer (not the screen): MRP, inventory close, ATP, period close, report | MRP regeneration 2–8 h nightly; inventory close 1–6 h; month-end close 5–10 business days | MRP incremental < 2 min, full < 10 min; close continuous; statements real-time |
| **More accurate** | Reconciliation breaks per month; silent rounding/FX errors; duplicate master data; manual journal share | Subledger-to-GL breaks every close; 3–8% duplicate customers/vendors; 20–40% of journals are corrections | Zero unexplained breaks (checked daily by machine); < 0.5% duplicates; corrections < 5% |
| **Open** | Days to connect a new system; share of data reachable by API/event; integration projects per year | Weeks per interface (AIF/IDoc mapping projects); large share of logic reachable only from the UI | Any system on the network connects in < 1 day with no middleware; 100% of entities and events reachable |

## 0A.1 The structural pain-point ledger

Each pain point below names the AX/SAP mechanism that causes it, the root cause, the Passage design that removes it, and the wave where it ships. Wave files repeat this at wave granularity under "Pain points solved (10× lens)".

### A. Usability

| # | Pain point today | Where it lives in AX / SAP | Root cause | Passage answer | Wave |
|---|---|---|---|---|---|
| U1 | Dozens of forms to complete one business task; fields scattered across tabs and FastTabs | AX `SalesTable` form with 20+ tabs; SAP `VA01` header/item/schedule-line screens | Forms mirror tables, not tasks | Task-oriented screens generated from metadata with the *task* as the unit (order intake, receipt, close task); line grids with inline edit; the full document on one scrollable page | 5, 27 |
| U2 | Lookups require knowing the code (vendor account, main account, item number) | `MainAccount` lookup in `LedgerJournalTrans`; SAP GL account search help | Keys are the primary identifier; search is secondary | Global typeahead on name/description/any attribute, ranked by recency and role; keys shown but never typed | 5 |
| U3 | Personalization is per-user, fragile, lost on upgrade | AX usage data (`SysLastValue`); SAP GUI variants | Personalization stored outside metadata | Saved views are first-class, versioned, shareable, role-defaulted; survive upgrades because they target metadata ids | 5 |
| U4 | Batch jobs hide status; users poll "Batch jobs" form to see if posting finished | `BatchJob`/`BatchJobHistory`; SAP `SM37` | Fire-and-forget batch framework with no UI contract | Every long-running action returns a tracked workflow with progress, ETA and a notification; the originating screen updates live | 4, 5 |
| U5 | Error messages name internal objects ("Account structure does not allow this combination") | AX infolog; SAP message classes | Validation written for developers | Validation messages are metadata with user text, remediation link and the exact field; errors shown at the field as you type | 3, 5 |
| U6 | Shop-floor and warehouse UIs are RF text menus or require a PC | AX `JmgRegistration` terminal; SAP ITSmobile/RF | UI frameworks predate touch and offline | Touch-first PWA kiosk and scanner apps with offline queues; one scan completes a step | 25, 32 |
| U7 | Excel round-trips to do bulk edits | AX Excel add-in; SAP GUI copy/paste | No bulk-edit grid | Every list page supports multi-row inline edit, paste from spreadsheet with validation preview, and undo | 5 |
| U8 | Approval requires logging in to the ERP | AX workflow work items; SAP Business Workplace (`SBWP`) | Workflow inbox is inside the client | Approvals in email/Slack/Teams/mobile with one-tap approve, full context and the same audit trail | 4 |
| U9 | Copilot features are bolted on and read-only | D365 Copilot summaries; SAP Joule Q&A | AI layer has no safe action surface | Every API is an MCP tool under the user's permissions; agents draft, users approve; explain-this-record everywhere | 47 |

### B. Speed

| # | Pain point today | Where it lives in AX / SAP | Root cause | Passage answer | Wave |
|---|---|---|---|---|---|
| S1 | MRP runs overnight; planners work on stale data; a changed order waits until tomorrow | AX `ReqCalc` regeneration; SAP `MD01` total planning | Full regeneration over all items; pegging recomputed from scratch | Net-change planning engine keyed by events (order/receipt/BOM change) with incremental re-plan of affected low-level codes; full regen < 10 min for 50k items | 22 |
| S2 | Inventory close/recalculation blocks posting for hours | AX `InventClosing`; SAP material ledger `CKMLCP` | Settlement of issues to receipts is a batch over all transactions | Running costing (continuous settlement on each issue for FIFO/avg) so close is a verification pass, not a computation; partitioned by item | 20 |
| S3 | ATP is computed per line with locks; order entry slows under load | AX `InventOnHand`/CTP; SAP ATP with `VBBE` requirements | Row-level locking on on-hand and requirement tables | Cumulative ATP projection maintained from the inventory event stream; reads lock-free; promise computed in memory | 27 |
| S4 | Month-end close takes 5–10 days of manual steps | AX period close checklist is a list; SAP closing cockpit sequences manual transactions | Postings are batch; reconciliations are manual | Continuous close: every subledger reconciles to the universal journal daily by machine; close checklist tasks auto-complete when their check passes | 14, 16+ |
| S5 | Reports run against OLTP and time out, or against a cube that is a day old | AX SSRS on `GeneralJournalAccountEntry`; SAP BW loads | No streaming path from transaction to analytics | CDC → Iceberg lakehouse with p95 < 60 s lag; statements served from pre-aggregated balances rebuilt from the journal | 11, 46 |
| S6 | Posting a 10k-line journal takes minutes and locks the period | `LedgerJournalCheckPost`; SAP `FB50` with `BSEG` inserts | Row-by-row posting through class hierarchies; synchronous number sequences | Append-only journal with batch insert, idempotency keys and asynchronous balance projection: 5,000 lines/s sustained | 11 |
| S7 | Deploying a customization takes a release window | AX model store deploy with AOS restart; SAP transports with downtime | Code and metadata are compiled into the runtime | Metadata extensions apply live; code ships via GitOps with digest rollback in < 15 min | 1, 3 |

### C. Accuracy

| # | Pain point today | Where it lives in AX / SAP | Root cause | Passage answer | Wave |
|---|---|---|---|---|---|
| A1 | Subledgers drift from the GL (inventory value ≠ inventory account; AR aging ≠ AR control) | `InventSum` vs `GeneralJournalAccountEntry`; SAP `BSID` vs `SKB1` balances | Subledgers post to the GL through separate code paths with their own rounding | One posting API; subledgers are projections of the same event stream; daily machine reconciliation with zero unexplained breaks as a release gate | 11, 16, 19, 30 |
| A2 | Rounding and FX differences appear as "penny" entries | AX `LedgerJournalTrans` rounding to accounting currency; SAP `BSEG` rounding differences | Rounding at line level then summing | Three-currency amounts stored per line; rounding applied once at document level with explicit rounding lines; ISO 4217 minor units enforced | 12 |
| A3 | Duplicate customers/vendors/items | `CustTable` created freely; SAP `KNA1` duplicates across sales orgs | No dedup at creation; roles not layered on one party | One party per real-world entity with fuzzy-match blocking at creation (precision ≥ 0.95) and governed merge | 7 |
| A4 | Wrong tax because tax group ↔ item tax group intersections are configured by hand | `TaxGroup`/`TaxItemGroup` intersection; SAP `FTXP` condition records | Determination is a configuration lattice with no test harness | Determination engine with a 500-scenario fixture pack reviewed by accountants; explain-this-tax on every line | 13 |
| A5 | Negative inventory and phantom on-hand | `InventSum` allows negative physical when `InventModelGroup` permits; SAP `MMBE` vs `MSEG` timing | Projection updated by application code that can skip | On-hand is a projection of immutable inventory events; policy enforced at the event write; projection rebuilt from events and compared nightly | 16 |
| A6 | Costing variances nobody can explain | `InventCostTrans` variances; SAP `KKS1` | Variance types computed after the fact with no linkage | Variance decomposition attached to each ended order with lot-size, quantity, price and substitution components that sum exactly | 21, 39 |
| A7 | Master data changed without approval or trace | Direct edit of `VendTable` bank account; SAP `XK02` | Audit log optional (`SysDatabaseLog`) and slow | Field-level immutable audit from CDC for every entity; sensitive fields (bank accounts, credit limits) require workflow | 5, 7 |

### D. Openness and interoperability

| # | Pain point today | Where it lives in AX / SAP | Root cause | Passage answer | Wave |
|---|---|---|---|---|---|
| O1 | Each interface is a project (AIF service, IDoc mapping, custom data entity) | AX AIF/`DMF` entities; SAP `WE20` partner profiles, PI/PO | Integration surface is bespoke per object | Every entity and event is published automatically (OpenAPI, AsyncAPI, MCP) from metadata; new entity ⇒ new API with no work | 3, 4, 35 |
| O2 | External systems poll for changes | `DMF` change tracking; SAP change pointers (`BDCP2`) | No event stream | Kafka topics per entity with CloudEvents, replay from any offset, webhooks for partners | 4 |
| O3 | MES/PLM/SCADA need a separate middleware product | SAP MII, PI/PO; AX via BizTalk/Logic Apps | ERP speaks only its own protocols | Native OPC-UA/MQTT ingestion, EDI engine, Peppol, SFTP, MCP — eight integration surfaces with one auth model | 25, 35 |
| O4 | Vendor cloud lock-in (LCS, RISE) | D365 only on Microsoft-managed Azure; S/4 Cloud on RISE | Platform tied to the vendor's operations | Any conformant Kubernetes; Helm charts; self-host or managed | 1 |
| O5 | Customizations block upgrades | X++ overlayering; ABAP modifications | Extension model is code in the vendor's layer | Extensions are metadata and sanctioned hooks; additive API guarantees; upgrades are image promotions | 3 |
| O6 | Analytics requires exporting to BYOD/BW | Entity store; BW extractors | No open analytical surface | Iceberg tables readable by any engine (Trino, Spark, DuckDB, Power BI, Tableau) with governed metrics | 46 |

## 0A.2 Interoperability contract ("all systems on the network can use it")

Passage treats the ERP as a service on the customer's network, not a destination. Every capability is reachable through the same eight surfaces with one identity and one policy engine:

| Surface | Used by | Auth | Guarantee |
|---|---|---|---|
| REST (OpenAPI 3.1, generated per entity) | Apps, scripts, partners | OIDC bearer token or API key, OPA-authorized | p95 < 50 ms simple reads; additive versioning |
| GraphQL (read model) | BI tools, portals | Same | Cross-entity reads without N+1 |
| Kafka topics (CloudEvents, AsyncAPI) | MES, WMS, data platforms, other ERPs | mTLS + ACLs per tenant | At-least-once, ordered per key, 7-day replay |
| Webhooks | SaaS partners | Signed (HMAC) deliveries | Retries with backoff, dead-letter, replay |
| MCP | AI agents (Claude and others) | Delegated user token; agent ≤ user permissions | Every tool call audited |
| EDI (X12/EDIFACT via AS2/SFTP) and Peppol | Trading partners, tax authorities | Partner certificates | Acknowledgments, SLA monitor |
| OPC-UA / MQTT | Machines, PLCs, SCADA | Device certificates | 10k events/min per plant, no loss on broker restart |
| File / SFTP | Banks, legacy systems | Keys in Vault | ISO 20022, BAI2, NACHA, CSV with schema validation |

Rule: **if it is not reachable through these surfaces, it does not ship.** Each wave's API/event section is reviewed against this rule before close.

## 0A.3 How "deep knowledge of AX and SAP" is applied

We deliberately keep what those systems got right and name it in every wave's parity map:

- **From AX:** the role/duty/privilege security model; inventory dimensions (product/storage/tracking) as a first-class concept; `InventTrans` status semantics; trade-agreement search order; number sequences per legal entity; financial dimensions with account structures; coverage groups; lean production flows.
- **From SAP:** the universal journal (ACDOCA) as single source of truth; parallel ledgers and currency types; condition technique for pricing and tax; posting periods per module; EWM's location-directive rigor; QM inspection lots and usage decisions; results analysis for projects; Group Reporting eliminations.

And we remove the parts that exist only because of their era: batch-first processing, table-shaped forms, code-based extensions, closed integration layers, vendor-bound operations.

\newpage

