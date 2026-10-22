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
| 12 | [prd/coverage-ax.md](prd/coverage-ax.md) | Completeness oracle: every AX 2012 R3 / D365 F&SCM module → forms, inquiries/reports, periodic jobs, setup forms, each mapped to a wave or flagged GAP; gap register |
| 13 | [prd/coverage-sap.md](prd/coverage-sap.md) | Completeness oracle: every SAP ECC / S/4 component → transactions, reports, periodic processing, IMG customizing, each mapped to a wave or flagged GAP; gap register |

Each wave section follows the fixed template in [prd/TEMPLATE.md](prd/TEMPLATE.md): scope and pain points solved → AX/SAP parity map → functional requirements → data model → APIs/events → UX → business rules → acceptance criteria → test plan → deployment → migration → dependencies/risks/open questions → parameters and setup → inquiries and reports → printed/electronic documents → periodic jobs → duties and privileges → message catalog → performance budgets → localization hooks.

**Completeness rule.** A wave is not specified until every row of Parts 12 and 13 that maps to it is either covered by a numbered FR in that wave's section or explicitly marked SUPERSEDED/OUT-OF-SCOPE with a reason. The gap registers (12.99, 13.99) must be empty before Phase 0 starts.

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
