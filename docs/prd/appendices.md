# Part 10 — Appendices

These appendices are normative for every phase file (`docs/prd/phase-N-*.md`). Where a phase file and an appendix disagree, the appendix wins for conventions (naming, envelopes, error codes, security, testing) and the phase file wins for business behavior. The stack is fixed by [ADR-0001](../adr/0001-platform-stack.md); nothing here proposes infrastructure outside it.

## A. Glossary

Terms are drawn from AX 2012 / D365 F&SCM, SAP ECC / S/4HANA, the standards Passage implements, and Passage itself. The last column is the name used in Passage metadata, APIs and UI.

| Term | Origin | Definition | Passage term |
|---|---|---|---|
| AOT | AX | Application Object Tree: the AX metadata repository for tables, classes, forms, menus. | Metadata registry |
| DDIC | SAP | ABAP Data Dictionary: domains, data elements, tables, append structures. | Metadata registry |
| EDT | AX | Extended Data Type: a typed, reusable field definition (e.g. `CustAccount`). | Field type |
| Base enum | AX | Fixed enumeration stored as an integer; extensible via enum extensions. | Enum (metadata) |
| Extension (vs overlayering) | AX | D365 model that adds to standard objects instead of editing them. | Entity extension / hook |
| DataAreaId | AX | The company (legal entity) column present on every company-specific table. | `legal_entity_id` |
| MANDT | SAP | Client key present on every client-dependent table. | `tenant_id` |
| Tenant | Passage | Top-level isolation boundary: one customer organization, one RLS scope, one Keycloak realm. | Tenant |
| Legal entity | AX / Passage | A company that produces its own financial statements. SAP: company code (BUKRS). | LegalEntity |
| Operating unit | AX | Department, cost center, business unit, value stream: non-legal organization units. | OperatingUnit |
| Company code (BUKRS) | SAP | Smallest organizational unit with a complete self-contained set of accounts. | LegalEntity |
| Plant (WERKS) | SAP | Physical production/logistics location. AX: site. | Site |
| Storage location (LGORT) | SAP | Sub-location of a plant where stock is held. AX: warehouse. | Warehouse |
| Role / duty / privilege | AX | Three-layer security model: privileges grant entry points, duties group privileges, roles group duties. | Role / Duty / Privilege |
| Authorization object | SAP | Named set of fields (e.g. `F_BKPF_BUK`) checked at runtime. | Privilege |
| SoD | AX / SAP GRC | Segregation of duties: rules preventing one user from holding conflicting duties. | SoD rule |
| Number sequence | AX | Generator of document identifiers per scope; continuous (gap-free) or not. SAP: number range object (SNRO). | NumberSequence |
| Database log | AX | Table/field change log. SAP: change documents (CDHDR/CDPOS). | Audit log (CDC) |
| Workflow | AX / SAP | Approval and process orchestration (AX workflow framework, SAP Business Workflow). | Temporal workflow |
| DIXF / DMF | AX | Data Import Export Framework / Data Management Framework. SAP: LSMW, Migration Cockpit. | Migration pipeline |
| LCS | AX | Lifecycle Services: Microsoft's hosted deployment/upgrade portal. SAP: STMS/CTS+. | Argo CD GitOps pipeline |
| DirParty / Global Address Book | AX | Shared party model with customer/vendor/contact roles. | Party |
| Business Partner (BP) | SAP | S/4 single party record with roles; CVI links to KNA1/LFA1. | Party |
| CustTable / KNA1 | AX / SAP | Customer master table. | Customer (party role) |
| VendTable / LFA1 | AX / SAP | Vendor master table. | Vendor (party role) |
| Posting profile | AX | Mapping from a customer/vendor/item group to GL control accounts. SAP: reconciliation account. | PostingProfile |
| Product master / variant | AX | A product with dimensions (size, color, style, configuration) that explode into distinct products. SAP: configurable material. | ProductMaster / Product |
| Released product | AX | A product made available to one legal entity with entity-specific defaults. SAP: MARC plant view. | ReleasedProduct |
| MARA / MARC / MARD | SAP | Material master: general, plant and storage-location views. | Product / ReleasedProduct / on-hand |
| Dimension group | AX | Product, storage, and tracking dimension groups controlling which dimensions apply to an item. | DimensionGroup |
| Item model group | AX | Costing method, negative inventory policy, physical/financial posting flags. SAP: valuation class + MRP/accounting views. | ItemModelGroup |
| GTIN / SSCC | GS1 | Global Trade Item Number; Serial Shipping Container Code (18-digit pallet/case id). | GTIN / SSCC |
| BOM | AX / SAP | Bill of materials: component list by version with effective dates. SAP: STKO/STPO. | BOM |
| Formula | AX | Process-industry BOM with potency, co-/by-products, scalable quantities. SAP: master recipe. | Formula |
| Route / routing | AX / SAP | Operation sequence with resources and times. SAP: PLKO/PLPO. | Routing |
| ECM / ECO | AX / SAP | Engineering change management; engineering change order. | ChangeOrder |
| Universal journal | SAP | ACDOCA: single line-item table for FI, CO, AA and ML. AX: GeneralJournalAccountEntry. | JournalEntry / JournalLine |
| Financial dimension | AX | Analytical attribute on a ledger line (department, cost center, project). SAP: CO object / profit center / segment. | Dimension |
| Account structure | AX | Rules for which dimension combinations are valid per main account. | AccountStructure |
| Posting layer | AX | Parallel accounting layer (Current, Operations, Tax). SAP: ledger group / non-leading ledger. | Ledger (parallel) |
| Currency types | SAP | 10 company code, 30 group, 40 hard currency amounts per line. AX: accounting/reporting currency. | Transaction / accounting / reporting currency |
| OB52 | SAP | Posting period control per account type and ledger. AX: ledger calendar period status per module. | PeriodControl |
| Allocation rule | AX | Basis/fixed distribution of balances across dimensions. SAP: assessment/distribution (KSU5/KSV5). | AllocationRule |
| FSV | SAP | Financial statement version: hierarchical statement definition (OB58). AX: Management Reporter row/column definitions. | StatementDefinition |
| InventTrans | AX | Immutable inventory transaction with issue/receipt status. SAP: MSEG material document line. | InventoryTransaction |
| InventSum | AX | On-hand aggregate per dimension combination. SAP: MARD/MCHB stock tables. | OnHand (projection) |
| Physical / financial status | AX | Transaction lifecycle: ordered → reserved → picked → deducted / received → registered → posted (financially). | InventoryTransaction status |
| Reservation | AX / SAP | Allocation of on-hand or expected receipts to a demand. | Reservation |
| Inventory close | AX | Periodic settlement of issues against receipts to finalize cost. SAP: material ledger closing (CKMLCP). | Cost close |
| Standard cost / costing version | AX | Frozen unit cost by version and site, activated per date. SAP: standard price, costing run (CK40N). | CostingVersion |
| Costing sheet | AX / SAP | Indirect cost (overhead) rule tree applied during cost roll-up. | CostingSheet |
| Product receipt | AX | Posting that records goods received against a PO. SAP: goods receipt (MIGO, movement type 101). | ProductReceipt |
| GR/IR | SAP | Goods-receipt / invoice-receipt clearing account; accrues liability between receipt and invoice. AX: accrued purchases. | GR/IR accrual account |
| Three-way match | AX / SAP | Invoice compared to PO and receipt within price/quantity tolerances (MIRO, tolerance keys). | Matching |
| ERS | SAP | Evaluated receipt settlement: self-billing from receipts without a vendor invoice. | ERS |
| Open item | SAP | Unsettled AP/AR line (FBL1N/FBL5N). AX: customer/vendor transaction. | OpenItem |
| Settlement | AX | Application of payment or credit against open items. SAP: clearing. | Settlement |
| F110 | SAP | Automatic payment program. AX: payment proposal. | PaymentProposal |
| pain.001 / camt.053 | ISO 20022 | Credit-transfer initiation message / end-of-day bank statement message. | Payment file / BankStatement |
| NACHA | Standard | US ACH file format. | Payment file (US) |
| Production order | AX / SAP | Order to make a quantity of an item via BOM and route (CO01). | ProductionOrder |
| RAF | AX | Report as finished: posts finished goods receipt from a production order. SAP: order confirmation (CO11N) + GR 101. | ReportAsFinished |
| Coverage group | AX | Planning parameters per item: requirement/period/min-max coverage, time fences, safety stock. SAP: MRP type + lot-sizing procedure. | CoverageGroup |
| MRP / MPS | Standard | Material requirements planning; master production schedule. | Planning run |
| Planned order | AX / SAP | MRP proposal for purchase, production or transfer, to be firmed. | PlannedOrder |
| Action / delay message | AX | Planner advice: advance, postpone, cancel; or dates that cannot be met. SAP: exception messages. | PlanningMessage |
| Finite / infinite capacity | AX / SAP | Scheduling that respects or ignores resource load (CM01, PP/DS). | Scheduling mode |
| Catch weight | AX | Dual-UoM inventory (nominal units plus actual weight). | CatchWeight |
| Potency | AX | Active-ingredient concentration used to scale formula consumption. | Potency |
| OEE | ISO 22400 | Overall equipment effectiveness = availability × performance × quality. | OEE |
| Trade agreement | AX | Price/discount list by account/group/all × item/group/all with effective dates. SAP: condition record. | PriceCondition |
| Condition technique | SAP | Pricing procedure of condition types resolved via access sequences (VK11). | Pricing procedure / access sequence |
| ATP / CTP | AX / SAP | Available-to-promise from on-hand and supply; capable-to-promise considers capacity. | ATP |
| Packing slip | AX | Delivery document posting inventory deduction and COGS. SAP: outbound delivery + post goods issue. | PackingSlip (Shipment posting) |
| Handling unit / license plate | SAP / AX | Physical container (pallet, case) with its own id holding nested items. | LicensePlate |
| Location directive | AX | WMS rule choosing put-away/pick locations. SAP: storage type search strategy. | LocationDirective |
| Quality order | AX | Inspection with tests and results leading to a disposition. SAP: inspection lot + usage decision. | QualityOrder |
| NCR / CAPA | Quality | Nonconformance report; corrective and preventive action. SAP: quality notification. | Nonconformance / CAPA |
| AQL | ANSI/ASQ Z1.4 | Acceptable quality level sampling plan. | SamplingPlan |
| Landed cost | Standard | Freight, duty and handling allocated to inventory cost. | LandedCost |
| EDI 850 / 856 / 810 | X12 | Purchase order / advance ship notice / invoice transaction sets. EDIFACT: ORDERS / DESADV / INVOIC. | EDI message |
| Peppol / UBL | Standard | Pan-European e-invoicing network; Universal Business Language XML. | E-invoice |
| Dunning | SAP | Reminder letters by escalation level (F150). AX: collection letters. | CollectionLetter |
| RMA | Standard | Return merchandise authorization. AX: return order; SAP: RE order. | ReturnOrder |
| SSP | ASC 606 | Standalone selling price used to allocate transaction price across performance obligations. | SSP |
| Performance obligation | ASC 606 / IFRS 15 | Distinct promise in a contract to which revenue is allocated. | PerformanceObligation |
| ROU asset | IFRS 16 / ASC 842 | Right-of-use asset recognized for a lease. | Lease |
| Budget control / encumbrance | AX / SAP | Reservation of budget by requisitions and POs (commitments). | Encumbrance |
| Elimination | Consolidation | Removal of intercompany balances and unrealized profit in group statements. | EliminationRule |
| POC | Accounting | Percentage-of-completion revenue estimate. SAP: results analysis. | ProjectEstimate |
| Functional location | SAP PM | Hierarchical place where equipment is installed. D365: functional location. | FunctionalLocation |
| Installed base | SAP CS | Serialized equipment at customer sites covered by service. | InstalledBase |
| Entity store | AX | D365 aggregate measurement store for Power BI. SAP: BW/CDS views. | Lakehouse (Iceberg) |
| MCP | Standard | Model Context Protocol: tool interface for AI agents. | MCP server |
| Copilot / agent | Passage | Copilot answers and drafts; an agent executes multi-step work under approval gates. | Copilot / Agent |
| Outbox | Pattern | Table written in the same transaction as the state change and relayed to Kafka by Debezium. | Outbox |
| CloudEvent | CNCF | Standard event envelope (specversion, id, source, type, subject, time, data). | Event |
| RLS | Postgres | Row-level security policy evaluated on every statement. | Tenant isolation |
| Wave (delivery) | Passage | A 4–8 week increment with deliverable, proof, deploy and references. Not to be confused with a WMS pick wave. | Wave |

## B. Cross-module data model

### B.1 Bounded contexts

Passage has 12 core bounded contexts. Each is a deployable set of services with exactly one Postgres database (CloudNativePG cluster) whose schema is generated from entity metadata. Three supporting contexts (`integration`, `analytics`, `agents`) own only operational tables (partner profiles, message logs, prompt registry, eval runs) and never master or transactional business data.

| Context | Database | Owns | Language | Waves |
|---|---|---|---|---|
| `platform` | `passage_platform` | Tenants, users, roles/duties/privileges, metadata registry, number sequences, audit log, workflow/batch definitions, legal entities, operating units, sites, warehouses, hierarchies, fiscal calendars, ISO reference data, UoM | Go (sequences, audit), TS (admin, workflow) | 1–6 |
| `party` | `passage_party` | Party, roles (customer, vendor, contact, employee link), addresses, tax registrations, groups, payment and delivery terms, credit limits | TS | 7 |
| `product` | `passage_product` | Product, product master, released product, dimension groups, attributes, BOM, formula, routing, resources, calendars, change orders | Go (explosion), TS | 8–9 |
| `ledger` | `passage_ledger` | Accounts, account structures, dimensions, journal entries/lines, balances, ledgers, exchange rates, tax codes and transactions, period control, allocations, accruals, statements, budgets, cost accounting ledger, consolidation groups | Go | 11–15, 38, 40 |
| `inventory` | `passage_inventory` | Inventory transactions, on-hand projection, reservations, inventory journals, counting, cost layers, costing versions, costing sheets, quality orders, nonconformances | Go | 16, 20, 33, 39 |
| `procurement` | `passage_procurement` | Requisitions, RFQs, purchase agreements, purchase orders, product receipts, vendor invoices, match results, AP open items, payment proposals, payments | Go (matching, settlement), TS | 17–20 |
| `manufacturing` | `passage_manufacturing` | Production/batch orders, picking lists, route cards, planning runs, planned orders, planning messages, schedules, kanbans, time registrations, machine events | Go; Python (forecasting) | 21–25 |
| `sales` | `passage_sales` | Price conditions, quotations, sales orders, holds, shipments/packing slips, customer invoices, revenue schedules, AR open items, cash applications, collection cases, return orders, rebate agreements | Go (pricing, ATP, settlement), TS | 26–30 |
| `warehouse` | `passage_warehouse` | Locations, license plates, location directives, work templates, work, waves, replenishment, loads, routes, carrier rates, freight invoices, mobile flow definitions | Go (directives), TS | 28, 31–34 |
| `treasury` | `passage_treasury` | Bank accounts, statements, reconciliations, cash forecasts, checks, letters of credit, fixed assets, asset books, asset transactions, leases | Go | 36–37 |
| `workforce` | `passage_workforce` | Workers, positions, jobs, employment, absence, benefits, payroll runs, pay rules, expense reports | TS; Go (payroll calc) | 25 (time), 42–43 |
| `projects` | `passage_projects` | Projects, WBS, project transactions, estimates, invoice proposals, functional locations, maintenance assets, maintenance plans, maintenance work orders, installed base, service agreements, cases, service work orders | TS | 41, 44–45 |

### B.2 Cross-context reference rule

A row in one database may reference an entity in another context only by its identifier (UUIDv7 or, for user-facing documents, the number-sequence id) in a column named `<entity>_id` plus, when needed for display, a denormalized `<entity>_number` and `<entity>_name` snapshot. There are no foreign keys, views or `dblink` connections across databases. The owning context publishes `created`/`changed`/`deleted` events; consumers maintain a local read model (`<context>_ref.<entity>` tables) refreshed from those events and never query another context's database. Referential existence is validated at write time by calling the owning context's API (`HEAD /v1/<context>/<collection>/{id}`) or against the local read model when staleness of up to 5 s is acceptable (documents, not postings). The universal journal is the only synchronous cross-context write: subledgers call `ledger.Post` and store the returned `journal_entry_id`.

Identity rules: every table has `id uuid` (UUIDv7, time-ordered), `tenant_id uuid NOT NULL`, `legal_entity_id uuid` where the entity is company-specific, `version int` for optimistic concurrency, `created_at/created_by/updated_at/updated_by`. Fiscal documents additionally have `document_number text` (from a number sequence) and `posted_at timestamptz`.

### B.3 Core entities

| Entity | Context | Database | Key identifiers | Main relationships |
|---|---|---|---|---|
| Tenant | platform | passage_platform | `id`, `slug` | has many LegalEntity, User, Role |
| User | platform | passage_platform | `id`, `keycloak_sub`, `email` | many-to-many Role scoped by LegalEntity with `valid_from/to` |
| Role / Duty / Privilege | platform | passage_platform | `code` (e.g. `AP.Clerk`, `MaintainVendors`, `vendor.write`) | Role ⊃ Duty ⊃ Privilege; compiled to OPA data |
| EntityDefinition | platform | passage_platform | `name`, `version` | has Fields, Enums, Relations, Extensions |
| NumberSequence | platform | passage_platform | `code`, `legal_entity_id`, `scope` | referenced by every document type (Appendix G) |
| AuditEntry | platform | passage_platform | `id`, `entity`, `record_id`, `trace_id`, `hash` | hash-chained to previous entry; immutable |
| LegalEntity | platform | passage_platform | `id`, `code` (e.g. `USMF`), `country` | has Sites, FiscalCalendar, ledger set |
| OperatingUnit | platform | passage_platform | `id`, `type`, `code` | effective-dated Hierarchy nodes by purpose |
| Site / Warehouse | platform | passage_platform | `id`, `code`, `legal_entity_id` | Warehouse belongs to Site; referenced by inventory and warehouse contexts |
| FiscalCalendar / Period | platform | passage_platform | `id`, `fiscal_year`, `period_no` | PeriodControl rows per Ledger × module |
| Party | party | passage_party | `id`, `party_number` | has Roles, Addresses, TaxRegistrations, Contacts |
| Customer | party | passage_party | `id`, `party_id`, `customer_account` | belongs to CustomerGroup, PostingProfile; credit limit |
| Vendor | party | passage_party | `id`, `party_id`, `vendor_account` | belongs to VendorGroup, PostingProfile, payment terms |
| Product / ProductMaster | product | passage_product | `id`, `product_number`, `gtin` | Master has Variants by dimension values; DimensionGroups |
| ReleasedProduct | product | passage_product | `product_id`, `legal_entity_id` | ItemModelGroup, defaults per Site/Warehouse |
| BOM / BOMLine | product | passage_product | `id`, `bom_number`, `version` | lines reference Product ids; effective dates; phantom, scrap |
| Routing / Operation | product | passage_product | `id`, `routing_number`, `version` | Operations require Resource capabilities |
| Account / AccountStructure | ledger | passage_ledger | `main_account`, `structure_id` | Dimensions allowed per structure |
| JournalEntry / JournalLine | ledger | passage_ledger | `id`, `document_number`, `idempotency_key` | lines carry 3 currency amounts, dimension set, `source_document_id`; `reverses_id` |
| Balance | ledger | passage_ledger | (`ledger_id`, `account`, `dimension_hash`, `period`) | projection rebuilt from JournalLine |
| Ledger / RateType / ExchangeRate | ledger | passage_ledger | `ledger_code`, `rate_type`, `from/to`, `valid_from` | LegalEntity has leading + parallel Ledgers |
| TaxCode / TaxTransaction | ledger | passage_ledger | `tax_code`, `jurisdiction_id` | TaxTransaction references source line id and JournalLine |
| InventoryTransaction | inventory | passage_inventory | `id`, `lot_id`, `reference_type/id` | dimensions (site, warehouse, location, batch, serial, LP); status pair |
| OnHand | inventory | passage_inventory | (`product_id`, dimension hash) | projection = Σ InventoryTransaction |
| CostLayer / CostingVersion | inventory | passage_inventory | `id`, `receipt_transaction_id`, `version_code` | settlements link issue to receipt layers |
| QualityOrder | inventory | passage_inventory | `id`, `qo_number` | triggered by receipt/production/shipment reference; blocks OnHand |
| PurchaseRequisition / RFQ / PurchaseAgreement | procurement | passage_procurement | `document_number` | Requisition lines → PO lines; Agreement releases |
| PurchaseOrder / Line | procurement | passage_procurement | `document_number`, `version` | `vendor_id`, `product_id`, `site/warehouse_id`; delivery schedules |
| ProductReceipt | procurement | passage_procurement | `document_number` | references PO lines, creates InventoryTransaction ids and JournalEntry id |
| VendorInvoice / MatchResult | procurement | passage_procurement | `document_number`, `vendor_invoice_number` | matches PO + Receipt lines; posts JournalEntry; creates APOpenItem |
| APOpenItem / Payment | procurement | passage_procurement | `id`, `payment_number` | Settlement links Payment to OpenItems; payment file id |
| ProductionOrder | manufacturing | passage_manufacturing | `document_number`, `status` | BOM/Routing snapshot; PickingList, RouteCard, JournalEntry ids for WIP |
| PlannedOrder / PlanningRun | manufacturing | passage_manufacturing | `id`, `run_id` | pegged to demand ids (SalesOrderLine, ProductionOrder, Forecast) |
| TimeRegistration / MachineEvent | manufacturing | passage_manufacturing | `id`, `worker_id`, `resource_id` | pay types exported to PayrollRun; OEE aggregates |
| PriceCondition | sales | passage_sales | `id`, `condition_type`, access key | resolved by access sequence; effective dates |
| SalesOrder / Line | sales | passage_sales | `document_number`, `status` | `customer_id`, `product_id`; Holds; ATP results; linked ProductionOrder/PlannedOrder ids |
| Shipment / PackingSlip | sales | passage_sales | `document_number`, `sscc` | references SalesOrder lines, Load id, InventoryTransaction ids, COGS JournalEntry id |
| CustomerInvoice / RevenueSchedule | sales | passage_sales | `document_number` | invoice lines → PerformanceObligations → schedule lines → JournalEntry ids |
| AROpenItem / CashApplication | sales | passage_sales | `id`, `receipt_number` | settlement to invoices; BankStatement line id |
| CollectionCase / ReturnOrder | sales | passage_sales | `case_number`, `rma_number` | customer, disputed invoice ids; RMA disposition → InventoryTransaction |
| Location / LicensePlate | warehouse | passage_warehouse | `location_code`, `lp_number` | Zone ⊃ Aisle ⊃ Rack ⊃ Bin; LP nests LPs |
| Work / PickWave | warehouse | passage_warehouse | `work_id`, `wave_id` | work lines reference order lines; directives; assigned user |
| BankAccount / BankStatement | treasury | passage_treasury | `id`, `iban/account_number` (P3) | statement lines reconcile to JournalLines, Payments, CashApplications |
| FixedAsset / AssetBook / AssetTransaction | treasury | passage_treasury | `asset_number`, `book_code` | acquisition from VendorInvoice/Project ids; depreciation JournalEntry ids |
| Worker / Position / Job | workforce | passage_workforce | `personnel_number`, `position_id` | effective-dated Employment; Position reports to Position; `party_id` |
| PayrollRun / ExpenseReport | workforce | passage_workforce | `run_id`, `report_number` | pay statements; JournalEntry ids; reimbursement → VendorInvoice id |
| Project / WBS / ProjectTransaction | projects | passage_projects | `project_number`, `wbs_code` | transactions reference TimeRegistration, ExpenseReport, PO, inventory ids; JournalEntry ids |
| MaintenanceAsset / MaintenanceWorkOrder | projects | passage_projects | `asset_number`, `work_order_number` | FunctionalLocation tree; parts → InventoryTransaction; labor → TimeRegistration |
| ServiceAgreement / Case / ServiceWorkOrder | projects | passage_projects | `agreement_number`, `case_number` | InstalledBase serials from Shipment; billing → CustomerInvoice id |

## C. Event catalog conventions

### C.1 Envelope

Every event is a CloudEvents 1.0 message in structured JSON mode on Kafka with the following attributes. Payload schemas are JSON Schema 2020-12 registered in the schema registry and described in AsyncAPI 3.0 documents generated from entity metadata.

| Attribute | Rule |
|---|---|
| `specversion` | `1.0` |
| `id` | UUIDv7; equals the outbox row id, so redelivery carries the same id |
| `source` | `/passage/<context>/<service>` e.g. `/passage/ledger/posting` |
| `type` | `dev.passage.<context>.<entity>.<verb>.v<n>` — lowercase, no hyphens; verb is past tense (`posted`, `confirmed`, `changed`) |
| `subject` | the aggregate id (UUIDv7) |
| `time` | commit time of the producing transaction, RFC 3339 UTC |
| `datacontenttype` | `application/json` |
| `dataschema` | registry URL `https://schemas.passage.dev/<type>.json` |
| `tenantid` (ext) | tenant UUID; always present |
| `legalentityid` (ext) | legal entity UUID when company-scoped |
| `correlationid` (ext) | id of the originating request or workflow; propagated unchanged |
| `causationid` (ext) | id of the event or command that directly caused this event |
| `traceparent` (ext) | W3C trace context from the producing span |
| `actor` (ext) | `user:<sub>`, `agent:<agent-id>@user:<sub>`, or `system:<job>` |
| `sequence` (ext) | monotonically increasing per aggregate (`version` after the change) |

### C.2 Topics and partitions

- Topic: `passage.<context>.<entity>` (one topic per entity, all verbs and versions share it); 12 partitions default, 48 for `ledger.journalentry`, `inventory.transaction`, `manufacturing.machineevent`.
- Partition key: `tenantid + ":" + subject` so events for one aggregate are ordered; `machineevent` uses `tenantid:resource_id`.
- Retention: 30 days for transactional topics, 7 days for `machineevent`, compacted `passage.<context>.<entity>.snapshot` topics for reference-data read models.
- Dead letter: `passage.<context>.<entity>.dlq.<consumer>` with the original event, error and attempt count in headers.
- Access: ACLs per consumer service principal (SCRAM-SHA-512); cross-tenant consumers (analytics) run under a dedicated principal and are the only ones allowed to read all tenants.

### C.3 Schema evolution

1. Changes within a version are additive only: new optional fields, new enum values flagged `x-passage-added-in`. Removing, renaming or retyping a field, or making an optional field required, requires a new `v<n+1>` type.
2. Schema registry compatibility mode is `BACKWARD_TRANSITIVE`; CI rejects a non-additive change (wave 4 proof).
3. When `v<n+1>` ships, the producer emits both `v<n>` and `v<n+1>` for a deprecation window of two minor releases of the producing service, then drops `v<n>`. Consumers must migrate inside the window; a consumer still subscribed to a dropped type fails its startup health check.
4. Enum consumers must treat unknown values as `other` and log at warn, never fail.

### C.4 Outbox table DDL

Every service database contains exactly this table; Debezium's outbox event router relays rows to Kafka and deletes them after acknowledgment.

```sql
CREATE TABLE outbox (
  id              uuid        PRIMARY KEY,                      -- UUIDv7, becomes CloudEvents id
  tenant_id       uuid        NOT NULL,
  aggregate_type  text        NOT NULL,                         -- e.g. 'journalentry'
  aggregate_id    uuid        NOT NULL,                         -- CloudEvents subject
  event_type      text        NOT NULL,                         -- full CloudEvents type
  partition_key   text        NOT NULL,                         -- tenant_id:aggregate_id
  sequence        bigint      NOT NULL,
  headers         jsonb       NOT NULL DEFAULT '{}'::jsonb,     -- correlationid, causationid, traceparent, actor
  payload         jsonb       NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX outbox_created_at_idx ON outbox (created_at);
ALTER TABLE outbox REPLICA IDENTITY FULL;
```

Rules: the outbox insert happens in the same transaction as the state change; the payload is the event `data` only (envelope attributes come from columns); payload size limit 256 KiB (larger content is stored and referenced by URL); the `outbox` table is excluded from RLS because only the service role writes it.

### C.5 Consumer idempotency

Every consumer keeps an inbox table and processes each event inside one database transaction:

```sql
CREATE TABLE consumer_inbox (
  consumer   text        NOT NULL,
  event_id   uuid        NOT NULL,
  processed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (consumer, event_id)
);
```

1. `INSERT INTO consumer_inbox ... ON CONFLICT DO NOTHING`; if zero rows were inserted, skip the event (duplicate delivery).
2. Apply the effect and write any outgoing outbox rows in the same transaction; commit; then acknowledge the Kafka offset.
3. Out-of-order protection: consumers compare `sequence` with the stored aggregate version and park events that arrive early for up to 60 s before sending to the DLQ.
4. Inbox rows are pruned after 30 days (the topic retention) by the batch framework.
5. Retries: 5 attempts with exponential backoff (1 s → 60 s), then DLQ; DLQ depth per consumer is an alert at 100 and a page at 1,000.

### C.6 Replay procedure

1. Open a change ticket naming consumer, topic, time window and reason; replay of a fiscal consumer (ledger, AP, AR) requires controller approval via the platform workflow.
2. Pause the consumer group (`passage-ctl consumer pause <group>`), snapshot its inbox table.
3. Reset the offset to the timestamp (`kafka-consumer-groups --reset-offsets --to-datetime`) or to a DLQ range; for a rebuild, truncate the read model and reset to earliest on the compacted snapshot topic.
4. Resume; the inbox guarantees already-processed events are skipped, so replay is safe by construction. A replay that must reprocess (bug fix) deletes inbox rows for the window first, inside the ticket.
5. Verify with the reconciliation checks in Appendix I.6 and close the ticket with the before/after totals.

### C.7 Principal event types

| Event type | Producer | Wave | Key consumers |
|---|---|---|---|
| `dev.passage.platform.entitydefinition.changed.v1` | platform/metadata | 3 | generators, shell, analytics |
| `dev.passage.platform.user.provisioned.v1` | platform/identity | 2 | workforce, audit |
| `dev.passage.platform.workflowtask.completed.v1` | platform/workflow | 4 | originating context, notifications |
| `dev.passage.platform.legalentity.created.v1` | platform/org | 6 | ledger, party, product, number sequences |
| `dev.passage.platform.periodcontrol.changed.v1` | platform/org | 14 | all posting services (cache invalidation) |
| `dev.passage.party.party.created.v1` | party | 7 | sales, procurement, workforce read models |
| `dev.passage.party.party.merged.v1` | party | 7 | every context holding `party_id` (rewrite references) |
| `dev.passage.party.customer.creditchanged.v1` | party | 7 | sales (credit checks) |
| `dev.passage.product.product.released.v1` | product | 8 | inventory, sales, procurement, manufacturing |
| `dev.passage.product.bom.versionactivated.v1` | product | 9 | manufacturing, inventory (cost roll) |
| `dev.passage.product.changeorder.released.v1` | product | 9 | manufacturing, warehouse, projects |
| `dev.passage.ledger.journalentry.posted.v1` | ledger | 11 | balances, analytics, audit, budgets |
| `dev.passage.ledger.journalentry.reversed.v1` | ledger | 11 | subledgers, analytics |
| `dev.passage.ledger.exchangerate.published.v1` | ledger | 12 | sales, procurement, treasury |
| `dev.passage.ledger.period.closed.v1` | ledger | 14 | all posting services, analytics |
| `dev.passage.ledger.taxsettlement.posted.v1` | ledger | 13 | statutory reporting, analytics |
| `dev.passage.inventory.transaction.posted.v1` | inventory | 16 | on-hand, sales (ATP), planning, analytics |
| `dev.passage.inventory.reservation.changed.v1` | inventory | 16 | sales, warehouse, manufacturing |
| `dev.passage.inventory.costclose.completed.v1` | inventory | 20 | ledger reconciliation, analytics |
| `dev.passage.inventory.qualityorder.decided.v1` | inventory | 33 | warehouse, procurement (scorecards), sales |
| `dev.passage.procurement.purchaseorder.confirmed.v1` | procurement | 17 | planning, budgets (encumbrance), integration (EDI 850) |
| `dev.passage.procurement.productreceipt.posted.v1` | procurement | 18 | inventory, ledger (GR/IR), quality, warehouse |
| `dev.passage.procurement.vendorinvoice.posted.v1` | procurement | 18 | ledger, treasury (forecast), budgets, analytics |
| `dev.passage.procurement.payment.posted.v1` | procurement | 19 | ledger, treasury, integration (payment file) |
| `dev.passage.manufacturing.productionorder.released.v1` | manufacturing | 21 | inventory (reservations), warehouse, shop floor |
| `dev.passage.manufacturing.productionorder.reportedasfinished.v1` | manufacturing | 21 | inventory, quality, sales (ATP) |
| `dev.passage.manufacturing.productionorder.ended.v1` | manufacturing | 21 | ledger (variances), inventory (cost), analytics |
| `dev.passage.manufacturing.plannedorder.firmed.v1` | manufacturing | 22 | procurement (PO creation), manufacturing orders |
| `dev.passage.manufacturing.schedule.changed.v1` | manufacturing | 23 | production orders, planning, shop floor |
| `dev.passage.manufacturing.machineevent.received.v1` | manufacturing/iot | 25 | OEE, projects (condition monitoring) |
| `dev.passage.sales.salesorder.confirmed.v1` | sales | 27 | inventory (reservation), planning, warehouse, integration (EDI 855) |
| `dev.passage.sales.shipment.posted.v1` | sales | 28 | inventory, ledger (COGS), billing, integration (ASN 856) |
| `dev.passage.sales.invoice.posted.v1` | sales | 29 | ledger, AR, revenue schedules, integration (810/Peppol), analytics |
| `dev.passage.sales.payment.applied.v1` | sales | 30 | ledger, collections, treasury |
| `dev.passage.sales.returnorder.received.v1` | sales | 30 | inventory, quality, billing (credit note) |
| `dev.passage.warehouse.work.completed.v1` | warehouse | 31 | inventory (status change), sales, procurement |
| `dev.passage.warehouse.load.shipped.v1` | warehouse | 34 | sales (shipment posting), integration (EDI 214), treasury (freight accrual) |
| `dev.passage.treasury.bankstatement.reconciled.v1` | treasury | 36 | ledger, AR, AP |
| `dev.passage.treasury.asset.depreciated.v1` | treasury | 37 | ledger, analytics |
| `dev.passage.workforce.payrollrun.posted.v1` | workforce | 43 | ledger, treasury (payments), projects |
| `dev.passage.projects.transaction.posted.v1` | projects | 41 | ledger (WIP), billing, analytics |
| `dev.passage.agents.action.executed.v1` | agents | 47 | audit, cost guard, analytics |

## D. API conventions

### D.1 Resources and naming

- Base path `https://api.<env>.passage.dev/v1/<context>/<collection>`; collections are plural kebab-case (`/v1/ledger/journal-entries`, `/v1/sales/sales-orders`, `/v1/product/released-products`). Ids in paths are UUIDv7; document numbers are reachable through `?filter=documentNumber eq 'INV-USMF-2026-000123'`.
- Sub-resources only for composition (`/sales-orders/{id}/lines`). Lifecycle actions are custom methods with a colon: `POST /v1/ledger/journal-entries/{id}:post`, `:reverse`, `:confirm`, `:release`, `:cancel`. Actions are the only way to change status; `PATCH` on a `status` field returns 422 `state_transition_invalid`.
- JSON field names are camelCase in the wire format and snake_case in Postgres; the generator maintains the mapping. Money is `{ "amount": "1234.56", "currency": "USD" }` with a decimal string; dates are ISO 8601 (`2026-10-21`, `2026-10-21T14:03:00Z`); quantities are decimal strings with `unit`.
- Every entity declared in metadata gets `GET` list, `GET` by id, `POST`, `PUT`, `PATCH` (JSON Merge Patch), `DELETE` (soft, unless metadata says hard), plus `:batch` and `:export`, generated. Hand-written endpoints are limited to engines (post, price, plan, schedule, match, settle).

### D.2 Versioning

`/v1` is the only version in v1.0. Within a major version changes are additive only (new optional fields, new endpoints, new enum values). Breaking changes ship as `/v2` for the affected context with both versions served for at least 12 months; the `Sunset` and `Deprecation` headers (RFC 8594) announce retirement. The `X-Passage-Api-Build` response header carries the service build for support.

### D.3 Pagination, filtering, sorting, shape

- Cursor pagination: `?limit=50&cursor=<opaque>`; `limit` max 500. Response: `{ "items": [...], "nextCursor": "...", "estimatedTotal": 1234 }`. Cursors encode the sort key and are valid for 24 h; a cursor from a different filter returns 400 `invalid_cursor`.
- Filter grammar (subset of OData): `field op value` with `eq ne gt ge lt le in like isnull`, combined with `and or not` and parentheses; nested fields by dot (`customer.group eq 'WHOLESALE'`); dimension filters via `dimensions.costCenter eq 'CC-100'`. Max 20 predicates; filters on non-indexed fields over 100k rows are rejected with 422 `filter_not_indexed`.
- `?sort=-postedAt,documentNumber`; `?fields=id,documentNumber,total` for projection; `?expand=lines,customer` for one level of embedded reads (never across contexts; cross-context fields come from the local read model and are marked `x-passage-snapshot` in OpenAPI).
- `?asOf=2026-01-01` on effective-dated entities (hierarchies, BOMs, positions, prices).

### D.4 Error envelope

Errors are RFC 9457 problem details (`Content-Type: application/problem+json`):

```json
{
  "type": "https://passage.dev/problems/period_closed",
  "title": "Period is closed",
  "status": 422,
  "detail": "Ledger USMF-IFRS period 2026-09 is closed for module AP since 2026-10-05T18:00:00Z.",
  "instance": "/v1/procurement/vendor-invoices/0192f3a1-...:post",
  "code": "period_closed",
  "traceId": "4bf92f3577b34da6a3ce929d0e0e4736",
  "errors": [ { "field": "postingDate", "code": "period_closed", "message": "..." } ]
}
```

| Code | HTTP | Meaning |
|---|---|---|
| `validation_failed` | 400 | Payload fails JSON Schema; `errors[]` lists fields |
| `required_field_missing` | 400 | A metadata-required field is absent |
| `invalid_enum_value` | 400 | Value not in the enum (including extensions) |
| `invalid_cursor` | 400 | Cursor expired or from another query |
| `filter_not_indexed` | 422 | Filter would scan an unindexed column on a large table |
| `unauthenticated` | 401 | Missing or invalid token |
| `forbidden_duty` | 403 | OPA denied: principal lacks the duty/privilege |
| `forbidden_tenant` | 403 | Resource belongs to another tenant or legal entity outside scope |
| `sod_conflict` | 403 | Action would violate a segregation-of-duties rule |
| `approval_required` | 403 | Action requires an approval workflow; a task was created |
| `not_found` | 404 | No such resource in the caller's scope |
| `conflict_version` | 412 | `If-Match` does not equal the current ETag |
| `precondition_required` | 428 | Mutating request without `If-Match` |
| `idempotency_key_reuse` | 422 | Same `Idempotency-Key` with a different payload |
| `idempotency_in_progress` | 409 | Original request with this key is still executing |
| `unique_violation` | 409 | Unique constraint (e.g. vendor invoice number per vendor) |
| `state_transition_invalid` | 422 | Action not allowed from the current status |
| `period_closed` | 422 | Posting date in a closed/on-hold period for this module |
| `unbalanced_entry` | 422 | Debits ≠ credits in any currency |
| `dimension_invalid` | 422 | Dimension combination violates the account structure |
| `inventory_insufficient` | 422 | On-hand would go negative and the item model group forbids it |
| `credit_hold` | 422 | Customer on hold or limit exceeded; hold created |
| `number_sequence_exhausted` | 422 | Sequence reached its upper bound or is stopped |
| `rate_limited` | 429 | Tenant tier quota exceeded; `Retry-After` set |
| `payload_too_large` | 413 | Body over 10 MiB (bulk) or 1 MiB (single) |
| `upstream_unavailable` | 503 | Tax/rate/carrier/bank provider failed and no fallback applied |
| `internal_error` | 500 | Unexpected; `traceId` for support |

### D.5 Idempotency

`Idempotency-Key` (UUID or ≤ 128-char string) is required on every `POST` that creates or posts a document and on every custom method; it is optional on `PUT/PATCH` (which rely on ETags). The key is scoped to tenant + principal + method + path. The server stores the request body hash and the final response for 24 h: a repeat with the same hash returns the stored response with `Idempotent-Replayed: true`; a repeat with a different hash returns 422 `idempotency_key_reuse`; a repeat while the first is in flight returns 409 `idempotency_in_progress`. Posting engines additionally persist the key on the document (`idempotency_key` column, unique per tenant) so the guarantee survives the 24 h cache.

### D.6 Concurrency

Every readable resource returns a strong `ETag` derived from `version` (e.g. `"v17"`). `PUT`, `PATCH`, `DELETE` and custom methods require `If-Match`; a missing header is 428, a mismatch is 412 `conflict_version` with the current representation in `detail`. List responses include per-item `etag`.

### D.7 Rate limits

| Tier | Sustained (requests/min per tenant) | Burst | Bulk rows/day | Webhook deliveries/min | Concurrent exports |
|---|---|---|---|---|---|
| Starter | 600 | 100 | 200k | 60 | 2 |
| Professional | 3,000 | 500 | 2M | 300 | 5 |
| Enterprise | 12,000 (negotiable) | 2,000 | 20M | 1,200 | 20 |

Enforced at Envoy Gateway per tenant and per API key; headers `RateLimit-Limit`, `RateLimit-Remaining`, `RateLimit-Reset` (IETF draft) on every response. Agents share the tenant budget and have a separate per-agent cap (Appendix E). Shop-floor and mobile apps use a dedicated limit class (`device`) of 120 requests/min per device.

### D.8 Bulk endpoints

`POST /v1/<context>/<collection>:batch` accepts up to 1,000 items with `"atomic": true|false`; atomic batches roll back on the first error, non-atomic batches return per-item results with the same problem-details shape. Larger loads use NDJSON upload to `POST /v1/platform/jobs` (type `import`), which runs in the batch framework with checkpoints and returns a job id, progress and an error file. `:export` streams NDJSON or CSV with the same filter grammar. Postings via bulk are still one journal entry per document; the batch framework never bypasses posting rules.

### D.9 OpenAPI and SDKs

The metadata generator emits one OpenAPI 3.1 document per context (`openapi/<context>.v1.yaml`) with schemas, examples, error codes and `x-passage-duty` on every operation; hand-written engine endpoints are merged from annotated Go/TS handlers. CI runs Spectral (style) and oasdiff (breaking-change detection) against the last released spec; the developer portal (wave 35) publishes specs and the AsyncAPI documents. SDKs generated per release: TypeScript (first-party, used by the shell), Go, Python in v1.0; C# and Java in v1.x for AX and SAP shops respectively. SDKs expose idempotency keys, ETags and pagination as first-class helpers and never hide problem details.

## E. Security and authorization model

### E.1 Tenancy

A tenant is one customer organization: one Keycloak realm, one `tenant_id`, one row-level security scope across all 12 databases, one Kafka ACL principal set, one Vault namespace, one cost budget. Legal entities are scopes inside the tenant; users are assigned roles per legal entity (or `*`). Data residency is per tenant (wave 48): a tenant is pinned to a region's cluster set and its Iceberg warehouse. Cross-tenant access does not exist in the application; only the analytics and platform-operations principals can read across tenants and only through audited service accounts.

The request path sets the scope: Envoy validates the JWT, the service opens a Postgres connection from a pool bound to the non-superuser application role and executes `SET LOCAL passage.tenant_id = '<uuid>'; SET LOCAL passage.legal_entity_scopes = '<uuid>,<uuid>'; SET LOCAL passage.actor = '<sub>'` inside the transaction. The application role has no `BYPASSRLS`.

### E.2 RLS policy template

Generated for every tenant-scoped table; the generator adds the `legal_entity_scope` policy only when the entity declares `legal_entity_id`.

```sql
ALTER TABLE ledger.journal_entry ENABLE ROW LEVEL SECURITY;
ALTER TABLE ledger.journal_entry FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON ledger.journal_entry
  AS RESTRICTIVE FOR ALL TO passage_app
  USING      (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE POLICY legal_entity_scope ON ledger.journal_entry
  AS RESTRICTIVE FOR ALL TO passage_app
  USING (
    current_setting('passage.legal_entity_scopes', true) = '*'
    OR legal_entity_id = ANY (string_to_array(current_setting('passage.legal_entity_scopes', true), ',')::uuid[])
  )
  WITH CHECK (
    legal_entity_id = ANY (string_to_array(current_setting('passage.legal_entity_scopes', true), ',')::uuid[])
  );

-- Batch and CDC roles: separate role with the same policies; Debezium reads the WAL and is outside RLS by design,
-- so the outbox payload carries tenant_id and the relay sets the CloudEvents tenantid from it.
```

A missing `passage.tenant_id` makes `current_setting(..., true)` return NULL and the policy returns no rows: the fail-safe is empty, never cross-tenant.

### E.3 Role / duty / privilege schema

```sql
CREATE TABLE platform.privilege (
  code text PRIMARY KEY,                  -- 'vendor.write', 'journalentry.post', 'payment.approve'
  resource text NOT NULL,                 -- entity or endpoint family
  action text NOT NULL,                   -- read | write | post | approve | delete | export | admin
  description text NOT NULL
);
CREATE TABLE platform.duty (
  code text PRIMARY KEY,                  -- 'MaintainVendors', 'PostVendorInvoices', 'ApprovePayments'
  tenant_id uuid,                         -- NULL = shipped by Passage, else customer-defined
  description text NOT NULL
);
CREATE TABLE platform.duty_privilege (duty_code text REFERENCES platform.duty, privilege_code text REFERENCES platform.privilege, PRIMARY KEY (duty_code, privilege_code));
CREATE TABLE platform.role (code text PRIMARY KEY, tenant_id uuid, description text NOT NULL);
CREATE TABLE platform.role_duty (role_code text REFERENCES platform.role, duty_code text REFERENCES platform.duty, PRIMARY KEY (role_code, duty_code));
CREATE TABLE platform.user_role (
  user_id uuid NOT NULL, role_code text NOT NULL, tenant_id uuid NOT NULL,
  legal_entity_id uuid,                   -- NULL = all legal entities in the tenant
  valid_from date NOT NULL DEFAULT current_date, valid_to date,
  granted_by uuid NOT NULL, PRIMARY KEY (user_id, role_code, legal_entity_id, valid_from)
);
CREATE TABLE platform.sod_rule (
  code text PRIMARY KEY, duty_a text NOT NULL, duty_b text NOT NULL,
  severity text NOT NULL CHECK (severity IN ('high','medium','low')), description text NOT NULL
);
```

Passage ships ~60 roles, ~400 duties and ~2,500 privileges, named after AX (e.g. role `AccountsPayableClerk`, duty `MaintainVendorInvoices`). Customers add duties and roles; they cannot add privileges, which are generated from metadata (`<entity>.<action>`) and engine endpoints.

### E.4 OPA policy structure

The compiled policy bundle contains `data.passage.grants[tenant][user] = {duty: [legal_entity_ids]}` and `data.passage.duty_privileges[duty] = [privileges]`, rebuilt by the platform service on every role change (push to the OPA sidecars via bundle server, ≤ 10 s propagation). The input is `{ "principal": {sub, tenant, agent?, on_behalf_of?}, "action": "<privilege>", "resource": {context, entity, id?, legal_entity_id?, amount?, owner?} }`. Every service calls `POST /v1/data/passage/authz/allow` on its sidecar; the gateway performs a coarse check (token, tenant, route duty) and the service performs the fine-grained one (record scope).

Example 1 — duty-based allow with legal-entity scope:

```rego
package passage.authz

default allow := false

allow if {
  some duty
  priv_set := data.passage.duty_privileges[duty]
  priv_set[_] == input.action
  scopes := data.passage.grants[input.principal.tenant][input.principal.sub][duty]
  scope_ok(scopes, input.resource.legal_entity_id)
}

scope_ok(scopes, le) if scopes[_] == "*"
scope_ok(scopes, le) if scopes[_] == le
```

Example 2 — agents never exceed the user they act for, and are additionally limited by their own allow-list:

```rego
allow if {
  input.principal.agent != null
  user_input := object.union(input, {"principal": {"sub": input.principal.on_behalf_of, "tenant": input.principal.tenant}})
  data.passage.authz.allow with input as user_input
  data.passage.agents[input.principal.agent].allowed_actions[_] == input.action
  not data.passage.agents[input.principal.agent].kill_switch
}
```

Example 3 — amount-banded approval (payment release above a threshold needs `ApprovePaymentsHighValue`, and the approver may not be the preparer):

```rego
deny contains msg if {
  input.action == "payment.approve"
  input.resource.amount_usd > 50000
  not has_duty("ApprovePaymentsHighValue")
  msg := "payments over USD 50,000 require ApprovePaymentsHighValue"
}

deny contains msg if {
  input.action == "payment.approve"
  input.resource.prepared_by == input.principal.sub
  msg := "sod: preparer cannot approve own payment"
}

has_duty(d) if data.passage.grants[input.principal.tenant][input.principal.sub][d]
```

### E.5 Segregation-of-duties enforcement points

| Point | Behavior |
|---|---|
| Role design (wave 48 SoD engine) | Creating or editing a role whose duties conflict is blocked (high) or warned (medium/low) |
| User assignment | Assigning a role that creates a conflict with the user's existing roles opens a mitigation workflow; the assignment is inactive until a mitigating control (compensating review, time-boxed access) is approved |
| Runtime (OPA `deny`) | Same-person checks on document pairs: prepare/approve journals, create vendor/approve vendor invoice, create PO/post receipt over threshold, enter payment/release payment, maintain bank account/run payments |
| Agents | An agent's action set is checked against SoD rules at registration; an agent cannot hold both sides of any high-severity rule |
| Certification | Quarterly access-certification campaigns; uncertified roles expire after 30 days |
| Evidence | Every conflict, mitigation and certification decision is an audit entry exported to the SOC 2 evidence store |

### E.6 PII classification and handling

| Level | Examples | Storage | Logging / tracing | Display | Export / analytics | Retention |
|---|---|---|---|---|---|---|
| P0 Public | Product catalog, public prices, legal entity name | Plain | Allowed | Full | Allowed | Business need |
| P1 Internal | Order numbers, amounts, dimension values, internal notes | Plain, RLS | Allowed (ids only, no free text) | Full with duty | Tenant-scoped | Business need |
| P2 Personal | Person names, business emails, phone, postal address, personnel number | Plain, RLS, `pii: true` in metadata | Never in logs; hashed in traces | Full with duty; masked in list exports by default | Lakehouse with column tags; erasure propagates by `party_id` | Per GDPR/CCPA policy; erasure request honored within 30 days except fiscal documents (pseudonymized) |
| P3 Sensitive | National IDs, tax IDs of persons, bank account/IBAN, card tokens, salary, health/absence reasons, biometrics, dates of birth | Column-level encryption (pgcrypto, key per tenant in Vault transit), separate `*_secure` tables, no full-text index | Never; access itself is audited | Masked (`****1234`) unless a `ViewSensitive*` duty is held; reveal is an audited action | Excluded from lakehouse except aggregated; excluded from exports except statutory formats | Minimum legal; vaulted in `workforce` with per-field retention |

Metadata flags (`pii: P2|P3`) drive generation: masking in generated APIs and forms, exclusion from the Debezium CDC allow-list for P3, and the subject-request tooling (wave 48) that enumerates every P2/P3 field per `party_id`.

### E.7 Secrets management

HashiCorp Vault is the only secret store: Kubernetes auth per service account, one namespace per tenant for customer-specific credentials (bank, tax provider, carrier). External Secrets Operator materializes only the minimum into Kubernetes secrets with 1 h TTL. Database credentials are dynamic (Vault database engine, 24 h leases). Bank signing keys and EBICS keys live in Vault transit and never leave it; files are signed by calling transit. Rotation: 90 days for static secrets, automatic on lease expiry for dynamic ones, immediate on offboarding. Git is scanned by gitleaks in CI; a leak fails the build. Keycloak client secrets rotate per release.

### E.8 Audit and retention

Audit is derived from Debezium CDC into an append-only `platform.audit_entry` table with a per-tenant SHA-256 hash chain (`hash = sha256(prev_hash || canonical_json(entry))`) verified nightly; the chain head is written to the lakehouse and to object storage with object lock. Retention: audit entries for fiscal documents (journal entries, invoices, payments, receipts, packing slips, production orders, asset transactions, payroll) 7 years from fiscal-year end, extended by country pack (DE and FR 10 years, IN 8 years); other entities 3 years; agent actions 7 years. Audit entries cannot be deleted; GDPR erasure replaces P2/P3 values with pseudonyms and records the erasure itself.

### E.9 Encryption

In transit: TLS 1.3 at the gateway (cert-manager, ACME or customer CA), mTLS between services and to Postgres and Kafka with cert-manager-issued certificates rotated every 30 days; Kafka SCRAM-SHA-512 plus TLS; Debezium and Temporal over mTLS. At rest: encrypted storage class on every persistent volume (provider KMS or LUKS on bare metal), CloudNativePG backups and WAL archives encrypted with a per-tenant key via Vault transit (envelope), Kafka tiered storage and Iceberg objects encrypted server-side; P3 columns encrypted additionally at the application level. Key rotation re-wraps envelopes without rewriting data; a tenant's crypto-shred (key deletion) is the final step of tenant offboarding.

## F. Non-functional requirements detail

| ID | Area | Metric | Target | Measurement | First enforced |
|---|---|---|---|---|---|
| NFR-A1 | Availability | Monthly availability per tenant (API + shell) | ≥ 99.9% (v1), ≥ 99.95% with multi-region | Synthetic probes every 30 s from two regions; SLO in Grafana | Wave 5 (measured), wave 50 (99.95%) |
| NFR-A2 | Availability | RPO | ≤ 5 min | Backup/restore drill; WAL archive lag metric | Wave 48 |
| NFR-A3 | Availability | RTO | ≤ 1 h | DR game day, timed | Wave 48, region failover wave 50 |
| NFR-A4 | Availability | Zero-downtime deploys and migrations | 0 failed requests during rollout | Argo rollout with k6 traffic; expand/contract migrations only | Wave 3 |
| NFR-A5 | Availability | Rollback time | ≤ 15 min to prior digest | Timed in Argo CD history | Wave 1 |
| NFR-P1 | Performance | Journal posting throughput | ≥ 5,000 lines/s sustained, p99 < 200 ms | k6 against ledger on 3-node stage | Wave 11 |
| NFR-P2 | Performance | Generated entity simple read | p95 < 50 ms at 1,000 rps | k6 | Wave 3 |
| NFR-P3 | Performance | Price resolution (10-line order) | p95 < 20 ms | Go benchmark + k6 | Wave 26 |
| NFR-P4 | Performance | Order intake API | 500 orders/min, p99 < 500 ms | k6 | Wave 27 |
| NFR-P5 | Performance | MRP run | 50k items × 12 periods × 5 levels < 10 min | Scheduled benchmark in CI nightly | Wave 22 |
| NFR-P6 | Performance | Scheduling | 5k operations < 60 s | Benchmark fixtures | Wave 23 |
| NFR-P7 | Performance | BOM explosion (7 levels, 2,000 components) | < 500 ms | Go benchmark | Wave 9 |
| NFR-P8 | Performance | Inventory close (1M transactions) | < 20 min | Batch job timing | Wave 20 |
| NFR-P9 | Performance | Cost roll (50k items) | < 15 min | Batch job timing | Wave 39 |
| NFR-P10 | Performance | Dashboard freshness (OLTP → lakehouse) | p95 < 60 s | Debezium/Iceberg lag metric | Wave 46 |
| NFR-P11 | Performance | Shell first contentful paint (warm) | < 1.5 s on a mid-range laptop | Lighthouse in CI | Wave 5 |
| NFR-P12 | Performance | Warehouse task assignment | p95 < 1 s | Simulation | Wave 31 |
| NFR-S1 | Scale | Concurrent users | 10,000 | k6 load campaign | Wave 50 |
| NFR-S2 | Scale | Journal lines per hour | 1M | Load campaign | Wave 50 |
| NFR-S3 | Scale | Order lines per hour | 100k | Load campaign | Wave 50 |
| NFR-S4 | Scale | IoT ingest | 10k events/min per plant, no loss on broker restart | Simulator + chaos | Wave 25 |
| NFR-S5 | Scale | Migration load | 50k customers, 20k vendors, 100k items, 10k BOMs < 30 min | Migration run ledger | Wave 10 |
| NFR-S6 | Scale | Tenants per cluster set | 100 with noisy-neighbor limits | Capacity model, rate limits | Wave 50 |
| NFR-SEC1 | Security | Authorization on every request | 100% of routes carry `x-passage-duty`; 0 unprotected | OpenAPI lint + gateway audit | Wave 2 |
| NFR-SEC2 | Security | Tenant isolation | 0 cross-tenant rows in RLS test suite | Automated RLS test per table | Wave 2 |
| NFR-SEC3 | Security | Supply chain | 100% images signed, SBOM attached, 0 critical CVEs at deploy | cosign verify, Grype gate | Wave 1 |
| NFR-SEC4 | Security | Penetration test | 0 high/critical open at wave 48 exit | External test report | Wave 48 |
| NFR-SEC5 | Security | Secrets in source | 0 | gitleaks | Wave 1 |
| NFR-SEC6 | Security | Agent permission ceiling | 0 actions exceeding the user's OPA decision | Contract tests | Wave 47 |
| NFR-C1 | Compliance | SOC 2 TSC mapping | 100% of in-scope controls with automated evidence | Compliance workspace | Wave 48 |
| NFR-C2 | Compliance | Subject access/erasure request | Fulfilled ≤ 30 days; export ≤ 24 h | Workflow SLA timer | Wave 48 |
| NFR-C3 | Compliance | Statutory outputs validate | SAF-T, Peppol, pain.001, X12 pass schema/schematron | CI fixture validation | Waves 13, 19, 35 |
| NFR-C4 | Compliance | Fiscal document immutability | 0 updates to posted documents (DB trigger) | Trigger + CDC audit test | Wave 11 |
| NFR-ACC1 | Accessibility | WCAG 2.1 AA on generated forms | 100% pass axe, 0 serious/critical | Playwright + axe in CI | Wave 5 |
| NFR-ACC2 | Accessibility | Keyboard operability | 100% of workspaces navigable without a mouse | Keyboard test suite | Wave 5 |
| NFR-ACC3 | Accessibility | Mobile/kiosk apps | WCAG 2.1 AA, touch targets ≥ 44 px | axe on emulated devices | Waves 25, 32 |
| NFR-O1 | Observability | Trace coverage | 100% of services emit OTel traces; one trace id per request end-to-end | Tempo query in CI smoke | Wave 5 |
| NFR-O2 | Observability | SLO dashboards and alerts | Every service has latency/error/saturation SLOs with burn-rate alerts | Grafana provisioning review | Wave 5 |
| NFR-O3 | Observability | Audit latency | Field change visible in audit viewer ≤ 2 s | E2E test | Wave 5 |
| NFR-PT1 | Portability | Provider-managed services in the critical path | 0 | Architecture review per wave | Wave 1 |
| NFR-PT2 | Portability | Local parity | `make deploy-local` on k3d < 10 min | Timed in CI | Wave 1 |
| NFR-PT3 | Portability | Provider modules | EKS, GKE, AKS, bare-metal Terraform modules with identical interface | Module tests | Wave 1 (k3d + one cloud), wave 50 (all) |
| NFR-D1 | Data integrity | Subledger-to-GL reconciliation | 0 unexplained variance, daily | CI reconciliation job (Appendix I.6) | Wave 16 |
| NFR-D2 | Data integrity | Balance projection = journal re-aggregation | Exact | Nightly rebuild compare | Wave 11 |
| NFR-D3 | Data integrity | Gap-free sequences | 0 gaps, 0 duplicates under 10k concurrent requests | Jepsen-style test | Wave 5 |
| NFR-D4 | Data integrity | Negative inventory | 0 unless item model group allows | Concurrency test | Wave 16 |
| NFR-D5 | Data integrity | Exactly-once event effect | 0 duplicate effects after chaos kill | Outbox/inbox chaos test | Wave 4 |
| NFR-D6 | Data integrity | Migration reconciliation | 100% control totals match | Migration run ledger | Waves 10, 49 |

## G. Numbering and document conventions

### G.1 Number sequence service

Sequences are defined per tenant with a scope of `tenant`, `legal_entity`, `legal_entity+fiscal_year` or `site`. Format strings use `{LE}` (legal entity code), `{FY}` (fiscal year), `{YY}`/`{YYYY}`, `{SITE}`, and `{######}` (zero-padded counter). Two modes:

- **Continuous (gap-free)**: the number is allocated inside the posting transaction from a per-sequence row locked with `SELECT ... FOR UPDATE`; if the transaction aborts the number is never consumed. Throughput is bounded (≈ 2,000/s per sequence on stage); used only where the law or audit requires it.
- **Non-continuous**: numbers are pre-allocated in blocks of 100 per service instance (cached); gaps are possible on restart. Used for everything that is not a fiscal document.

A sequence can be `stopped` (posting returns `number_sequence_exhausted`), has `lowest/highest` bounds, an optional yearly reset (only with `{FY}` in the format) and an alert at 90% consumption.

### G.2 Formats per document type

| Document | Sequence code | Format (default) | Example | Mode | Scope / reset |
|---|---|---|---|---|---|
| General journal entry (posted) | `GJ` | `{LE}-GJ-{FY}-{#######}` | `USMF-GJ-2026-0001234` | Gap-free | Legal entity × fiscal year |
| Journal batch (unposted) | `JB` | `JB-{########}` | `JB-00004521` | Non-continuous | Tenant |
| Customer invoice / credit note | `INV` / `CN` | `{LE}-INV-{FY}-{######}` | `DEMF-INV-2026-000087` | Gap-free | Legal entity × fiscal year (legal requirement in DE, FR, IN, MX) |
| Vendor invoice (internal number) | `VINV` | `{LE}-VINV-{FY}-{######}` | `USMF-VINV-2026-003310` | Gap-free | Legal entity × fiscal year |
| Purchase requisition | `REQ` | `REQ-{######}` | `REQ-001204` | Non-continuous | Tenant |
| RFQ | `RFQ` | `RFQ-{######}` | `RFQ-000051` | Non-continuous | Tenant |
| Purchase order | `PO` | `{LE}-PO-{######}` | `USMF-PO-000912` | Non-continuous | Legal entity |
| Product receipt | `PR` | `{LE}-PR-{FY}-{######}` | `USMF-PR-2026-000455` | Gap-free | Legal entity × fiscal year |
| Quotation | `QUO` | `QUO-{######}` | `QUO-000210` | Non-continuous | Tenant |
| Sales order | `SO` | `{LE}-SO-{######}` | `USMF-SO-004417` | Non-continuous | Legal entity |
| Packing slip / delivery note | `PS` | `{LE}-PS-{FY}-{######}` | `USMF-PS-2026-002031` | Gap-free | Legal entity × fiscal year |
| Return order (RMA) | `RMA` | `RMA-{######}` | `RMA-000078` | Non-continuous | Tenant |
| Vendor payment | `PAY` | `{LE}-PAY-{FY}-{######}` | `USMF-PAY-2026-000660` | Gap-free | Legal entity × fiscal year |
| Customer receipt / cash application | `RCPT` | `{LE}-RCPT-{FY}-{######}` | `USMF-RCPT-2026-001102` | Gap-free | Legal entity × fiscal year |
| Check number | `CHK-<bank>` | `{######}` | `100245` | Gap-free | Bank account (void keeps the number) |
| Production / batch order | `PRD` | `{SITE}-PRD-{######}` | `S1-PRD-000318` | Non-continuous | Site |
| Planned order | `PLN` | `PLN-{########}` | `PLN-00019902` | Non-continuous | Tenant (reset per planning run) |
| Warehouse work | `WRK` | `WRK-{########}` | `WRK-00031177` | Non-continuous | Tenant |
| Load | `LD` | `LD-{######}` | `LD-000504` | Non-continuous | Tenant |
| Quality order | `QO` | `QO-{######}` | `QO-000912` | Non-continuous | Tenant |
| Fixed asset | `FA` | `{LE}-FA-{#####}` | `USMF-FA-00317` | Non-continuous | Legal entity |
| Asset transaction | `FAT` | `{LE}-FAT-{FY}-{######}` | `USMF-FAT-2026-000045` | Gap-free | Legal entity × fiscal year |
| Maintenance work order | `MWO` | `MWO-{######}` | `MWO-001560` | Non-continuous | Tenant |
| Service case / service work order | `CASE` / `SWO` | `CASE-{######}` | `CASE-000731` | Non-continuous | Tenant |
| Project | `PRJ` | `PRJ-{######}` | `PRJ-000142` | Non-continuous | Tenant |
| Payroll run | `PAYR` | `{LE}-PAYR-{FY}-{###}` | `USMF-PAYR-2026-021` | Gap-free | Legal entity × fiscal year |
| Expense report | `EXP` | `EXP-{######}` | `EXP-004012` | Non-continuous | Tenant |
| Bank statement | `BST` | `{bank}-{YYYY}-{####}` | `CHASE01-2026-0210` | Non-continuous | Bank account |

Country packs may override formats and modes (Appendix H), for example a per-series gap-free invoice number for MX CFDI (`serie` + `folio`) or the FR requirement that credit notes use a separate chronological sequence.

### G.3 Fiscal document immutability

A document is fiscal when it posts to the universal journal or is legally required to be retained (invoices, credit notes, receipts, packing slips, payments, journal entries, asset transactions, payroll statements, production order settlements). Rules:

1. After `posted_at` is set, the header and lines are immutable: a database trigger rejects `UPDATE`/`DELETE` on posted rows except the status columns `settled_amount`, `settlement_status` and `printed_at`, and the metadata generator marks all other fields read-only in the API.
2. Corrections are new documents: reversal (an exact negating journal entry linked by `reverses_id`), credit note, correcting invoice (country packs that require it), or an adjustment journal. The original stays and shows its correction links.
3. The rendered PDF/XML of a fiscal document is stored at posting time in object storage with object lock for the retention period and its SHA-256 is written to the document; re-rendering produces a copy marked `Duplicate`.
4. The posting date determines the fiscal period; the document date may differ. Both are immutable after posting.
5. Document numbers are unique per tenant forever; a sequence reset per fiscal year includes the year in the number.

### G.4 Attachments

Attachments are stored in S3-compatible object storage (bucket per tenant, server-side encrypted) and referenced by `platform.attachment` rows (`entity`, `record_id`, `kind`, `content_sha256`, `size`, `retention_class`). Limits: 25 MiB per file, 100 files per record, file types allow-listed (PDF, images, Office, XML, CSV, TXT, ZIP ≤ 25 MiB); antivirus scan (ClamAV) before the attachment becomes visible. Attachments on fiscal documents inherit the document's retention and object lock; attachments on other records follow the entity's retention. Deleting a record soft-deletes attachments; hard purge runs after the retention window. Inbound documents (OCR invoices, EDI, e-invoices) keep the original payload as a `source` attachment.

## H. Localization scope

Country packs are metadata and code bundles installed per legal entity (wave 48). Every pack ships tax determination data, statutory report formats for the electronic reporting designer, document layouts, bank formats, calendar/number defaults and a regulator-schema fixture set. The matrix below is the v1.0 scope; items marked v1.x ship after go-live.

| Capability | US | UK | DE | FR | IN | MX | CA |
|---|---|---|---|---|---|---|---|
| Tax model | Sales & use tax by jurisdiction (state/county/city/district), nexus, exemption certificates, use-tax accrual; Avalara/Vertex adapter | VAT 20/5/0%, reverse charge, postponed import VAT, CIS withholding, Northern Ireland protocol | USt 19/7%, §13b reverse charge, intra-EU supplies, withholding for construction (Bauabzugsteuer) | TVA 20/10/5.5/2.1%, autoliquidation, intra-EU, DAS2 withholding hooks | GST (CGST/SGST/IGST/UTGST), HSN/SAC codes, reverse charge, TDS/TCS withholding sections | IVA 16/8/0%, ISR and IVA retentions, IEPS, SAT product/service catalogs | GST/HST 5–15%, provincial PST/QST, input tax credits, non-resident rules |
| Statutory reports (v1.0) | 1099-MISC/NEC/INT, sales tax returns by state (data extract), W-2 structure | MTD VAT return (HMRC API), VAT100 boxes, EC sales list for NI | UStVA (ELSTER XML), ZM (EC sales list), Intrastat, GoBD-compliant export (GDPdU), E-Bilanz v1.x | CA3 VAT return, DEB/Intrastat, FEC (fichier des écritures comptables), liasse fiscale v1.x | GSTR-1, GSTR-3B, e-way bill payload, TDS Form 26Q structure, 26AS reconciliation hook | DIOT, Contabilidad Electrónica (catálogo, balanza, pólizas XML), ISR provisional data | GST/HST return (GST34 boxes), T4/T4A/T5018 structures, QST return |
| E-invoicing mandate | None; Peppol/DBNAlliance optional | None in v1.0 (B2B mandate consultation); Peppol BIS optional | B2G XRechnung mandatory; B2B receive (2025) and issue (2027) via XRechnung/ZUGFeRD 2.x | B2B mandate via PDP: receive 2026, issue phased 2026–2027; Factur-X profiles | IRP e-invoice (IRN + signed QR) mandatory above turnover threshold; GSP adapter | CFDI 4.0 via PAC mandatory for every invoice, payment complement, carta porte v1.x; cancellation workflow | None; Peppol optional |
| Bank formats | NACHA ACH (PPD/CCD/CTX), check printing (MICR), positive pay, BAI2 statements, lockbox BAI2, ISO 20022 for wires | BACS Standard 18, Faster Payments via ISO 20022 pain.001, CHAPS, MT940/camt.053 statements | SEPA SCT/SDD (pain.001/pain.008), camt.052/053/054, EBICS 3.0 | SEPA SCT/SDD, camt.053, EBICS TS (signed) | NEFT/RTGS/IMPS bank-specific layouts via adapter, ISO 20022 pain.001 for H2H banks, MT940 | SPEI bank layouts via adapter, ISO 20022 for multinational banks, BAI2/camt.053 | CPA Standard 005 EFT, Lynx wires via ISO 20022, cheque printing, camt.053 |
| Payroll | Full engine: federal/state/local withholding (Pub. 15-T), FICA, FUTA/SUTA, garnishments, W-2/941 structures | PAYE RTI (FPS/EPS), NI, auto-enrolment — engine only if chosen as EU/UK reference (open question 3), else provider adapter | Lohnsteuer, Sozialversicherung (DEÜV), ELStAM — reference EU engine candidate (open question 3), else provider adapter | DSN monthly declaration — v1.x, provider adapter in v1.0 | PF, ESI, professional tax, Form 16 — v1.x, provider adapter in v1.0 | CFDI nómina, IMSS, ISR tables — v1.x, provider adapter in v1.0 | CRA/RQ source deductions, CPP/EI, ROE — v1.x, provider adapter in v1.0 |
| Calendar and formats | Fiscal year any; MM/DD/YYYY display; USD 2 decimals; Letter paper | Tax year 6 April (payroll), company FY any; DD/MM/YYYY; GBP; A4 | Calendar FY default; DD.MM.YYYY; EUR with `1.234,56`; A4; DIN 5008 letters | Calendar FY default; DD/MM/YYYY; EUR with `1 234,56`; A4 | FY 1 April–31 March; DD/MM/YYYY; INR lakh/crore grouping (`12,34,567.00`) | Calendar FY; DD/MM/YYYY; MXN; Letter | FY any; YYYY-MM-DD; CAD; Letter; bilingual EN/FR documents (Quebec) |
| Document layouts | Invoice, PO, packing slip, check, 1099 | Invoice with VAT breakdown, credit note, remittance | Rechnung with §14 UStG mandatory fields, Gutschrift, Mahnung levels | Facture with mentions obligatoires, avoir, relance | Tax invoice with GSTIN/HSN/IRN QR, debit/credit note, e-way bill | CFDI PDF representation with UUID/QR, complemento de pago | Invoice with GST/HST registration, bilingual variants |
| Fiscal retention | 7 years | 6 years | 10 years (GoBD) | 10 years | 8 years | 5 years | 6 years |

Common to every pack: ISO 3166/4217/639 reference data, address formats and postal validation rules, tax-registration validators (EIN, UTR/VAT, USt-IdNr, SIREN/SIRET/TVA, GSTIN/PAN, RFC, BN/GST), number-sequence overrides, holiday calendars, and a regulator-schema fixture that must validate in CI before the pack can be released.

## I. Testing standards

### I.1 Test pyramid per service type

| Service type | Unit | Property | Contract | Integration | E2E | Performance |
|---|---|---|---|---|---|---|
| Go engine (ledger, inventory, pricing, MRP, scheduling, costing, matching, settlement) | `go test`, table-driven, ≥ 85% line coverage on domain packages | `rapid` on invariants (balance, conservation, determinism, state machines) | OpenAPI contract via `oapi-codegen` validators; Pact for consumer contracts between contexts | Testcontainers Postgres + Kafka; outbox/inbox chaos | Through the shell for the wave demo path | Go benchmarks in CI (`benchstat` regression gate ±10%); k6 for throughput targets |
| TypeScript service (workflow, integration, BFF, admin) | Vitest, ≥ 80% coverage | `fast-check` for mappers, parsers (EDI, GS1, bank files), policy routing | OpenAPI + AsyncAPI schema validation; Pact | Testcontainers + Temporal test server | Playwright | k6 |
| Python service (forecasting, ML, agent evals) | pytest, ≥ 80% | `hypothesis` for numeric routines and parsers | JSON Schema on inputs/outputs | Docker compose fixtures | n/a | Benchmark notebooks exported to CI metrics |
| React shell and module-federated workspaces | Vitest + Testing Library | `fast-check` for form state reducers | Storybook interaction tests; generated-form snapshot per entity | MSW-backed integration | Playwright + axe across every generated form and workspace | Lighthouse CI |
| PWA (shop floor, warehouse, field) | Vitest | `fast-check` for the offline queue (commutativity, idempotence) | Same as shell | Device emulation | Playwright on emulated Android and tablet, offline toggling | Scan-to-ack latency budget 300 ms |

The ratio target per service is roughly 70% unit/property, 20% contract/integration, 10% E2E/performance by count; every wave's proof items map to at least one row.

### I.2 Fixture naming

```
fixtures/<context>/w<NN>-<slug>/<nnn>-<scenario>.<json|csv|ndjson>
fixtures/<context>/w<NN>-<slug>/<nnn>-<scenario>.expected.<json|csv>
fixtures/<context>/w<NN>-<slug>/README.md               -- what the set covers, source, size
fixtures/<context>/w<NN>-<slug>/REVIEW.yaml              -- accountant review record (I.5)
```

Example: `fixtures/ledger/w12-currency-parallel-ledgers/017-revaluation-eur-usd-unrealized.json`. Scenario numbers are stable once published (a retired scenario keeps its number with `retired: true`). Shared master-data seeds live in `fixtures/seed/<demo-company>/` (`USMF` manufacturer, `DEMF` process plant, `GBSI` services entity, `MXDS` distributor) and are loaded by the migration framework, so fixtures and demos use the same data.

### I.3 Property-based testing

Libraries are fixed: Go `pgregory.net/rapid`, TypeScript `fast-check`, Python `hypothesis`. Every engine declares its invariants in a `PROPERTIES.md` next to the code, and each invariant has one property test with a minimum of 1,000 generated cases in CI (10,000 nightly). Required invariant families: conservation (Σ debits = Σ credits; Σ inventory transactions = on-hand; Σ allocations = source), determinism (same input → same output across runs and goroutines), idempotence (apply twice = apply once), monotonicity (sequences, versions), state machines (every illegal transition rejected), and round-trips (serialize/parse, UoM conversions, currency rounding). Shrunken counterexamples are committed as regression fixtures with the originating seed.

### I.4 Golden files

Golden outputs (generated migrations, OpenAPI specs, rendered documents, statement packs, bank files, EDI messages, Rego bundles) live in `testdata/golden/` next to the test. A golden test fails on any byte difference. Updates happen only through `make update-golden PKG=<path>`, which rewrites files and records the reason in the commit; a PR that touches `testdata/golden/` must carry the label `golden-update` and a reviewer other than the author must approve (enforced by CODEOWNERS). Rendered PDFs are compared as rasterized pages with a 0.1% pixel tolerance; XML and JSON are canonicalized before comparison; timestamps and ids are normalized by the test harness.

### I.5 Accountant-reviewed fixtures

Finance waves (11–15, 18–20, 29, 36–41, 43) require fixture sets reviewed by a practicing accountant before the wave can close. Process: (1) the engineer drafts scenarios and expected results in a workbook (`.xlsx`) exported to `.expected.csv`; (2) the reviewer works the scenarios independently and signs `REVIEW.yaml` (`reviewer`, `credential`, `reviewed_on`, `standard` such as ASC 606, IAS 21, IRS Pub. 946, `scope`, `exceptions[]`); (3) CI fails if a finance fixture set lacks a `REVIEW.yaml` dated after its last change; (4) any later change to the set re-opens review. The reviewer owner is tracked in the PRD validation checklist.

### I.6 Reconciliation checks in CI

A scheduled job (`reconcile-daily`) runs at 02:00 UTC on stage against the demo companies and on every merge to `main` against the fixture databases; any non-zero variance fails the job and pages the wave owner.

| Check | Ties | Tolerance | From wave |
|---|---|---|---|
| Trial balance | Σ debits − Σ credits per ledger per period | 0.00 | 11 |
| Balance projection | `Balance` table vs re-aggregated `JournalLine` | 0.00 | 11 |
| Parallel ledgers | Each ledger's trial balance sums to zero | 0.00 | 12 |
| Tax | Σ `TaxTransaction` per tax code vs GL tax accounts | 0.00 | 13 |
| Statements | Balance sheet balances; P&L net = retained earnings movement; cash flow = Δ cash | 0.00 | 15 |
| Inventory quantity | Σ `InventoryTransaction` per dimension vs `OnHand` | 0 | 16 |
| Inventory value | Inventory value report vs GL inventory accounts by item group | 0.00 | 20 |
| GR/IR | Σ receipts − Σ invoices per PO line vs GR/IR account | 0.00 | 18 |
| AP | Σ open vendor items vs AP control accounts by posting profile | 0.00 | 19 |
| WIP | Open production order cost vs GL WIP accounts | 0.00 | 21 |
| AR and deferred revenue | Σ open customer items vs AR control; revenue schedule balance vs deferred revenue | 0.00 | 29–30 |
| Bank | Reconciled statement balance vs GL bank account | 0.00 | 36 |
| Fixed assets | Σ NBV per book vs GL asset and accumulated depreciation | 0.00 | 37 |
| Cost accounting | Primary costs in cost ledger vs GL | 0.00 | 38 |
| Intercompany | Due-to/due-from pairs net to zero per pair | 0.00 | 40 |
| Projects | Project WIP/accrued revenue vs GL | 0.00 | 41 |
| Payroll | Payroll register vs GL payroll accounts | 0.00 | 43 |
| Semantic layer | Revenue/COGS metrics vs GL | 0.00 | 46 |
| Audit chain | Hash chain verifies end-to-end | 0 breaks | 5 |

### I.7 Performance and E2E harness

k6 scripts live in `perf/<context>/<scenario>.js` with thresholds encoded in the script (`http_req_duration{scenario:post}: p(99)<200`); CI runs smoke profiles on every merge and full profiles nightly on stage, publishing results to Grafana with a trend alert at +10% over 7 days. Playwright suites live in `e2e/<workspace>/` with page objects per generated form type and run against the stage URL after Argo reports healthy; every suite calls `axe` on each page it visits and fails on serious/critical violations. Flaky tests are quarantined by label for at most one wave before deletion or repair.

## J. Traceability matrix

Each master-PRD functional requirement maps to its primary wave (FR-p.n → wave 5p + n), the later waves that extend it, the phase-exit criterion that proves it, and the test types that are primary evidence. Phase-exit criteria are the proof items of waves 5, 10, 15, 20, 25, 30, 35, 40, 45 and 50.

| FR | Requirement (short) | Primary wave | Extended by | Phase-exit criterion | Primary test types |
|---|---|---|---|---|---|
| FR-0.1 | GitOps deploy, signed images, SBOM, digest rollback | 1 | 48, 50 | Wave 5 platform demo: deploy → login → extend → approve → trace | CI negative tests, timed rollback, cosign/Grype gates |
| FR-0.2 | Federated identity; roles/duties/privileges → OPA; RLS | 2 | 47, 48 | Wave 5 demo (login + permission viewer) | Contract (20 duties), RLS per table, federation in CI |
| FR-0.3 | Metadata-declared entities generate schema/API/UI; no-code extensions | 3 | 5, 46 | Wave 5 demo (extend entity live) | Golden generation, backward-compat, load 1,000 rps |
| FR-0.4 | CloudEvents via outbox; Temporal workflows and batch | 4 | 47 | Wave 5 demo (approval + trace) | Chaos outbox, Temporal timer tests, schema-registry gate |
| FR-0.5 | Shell (WCAG AA), gap-free sequences, immutable audit, tracing | 5 | 46 | Wave 5 demo signed off | Playwright + axe, Jepsen-style sequence test, audit E2E |
| FR-1.1 | Enterprise structure, hierarchies, calendars, ISO data, UoM | 6 | 14, 40 | Wave 10: demo master data fully migrated | Property (as-of hierarchy), UoM round-trip, calendar fixtures |
| FR-1.2 | Single party model, dedup/merge, tax-id validation, governance | 7 | 30, 42 | Wave 10 | Dedup precision/recall, merge integrity audit, Temporal approval |
| FR-1.3 | Products, dimension groups, released products, GS1, configurator | 8 | 24, 27 | Wave 10 | Variant explosion perf, configurator fixtures, GTIN vectors |
| FR-1.4 | BOMs, formulas, routings, resources, ECM | 9 | 21, 24, 39 | Wave 10 | Explosion golden + perf, effective-date tests, ECO workflow |
| FR-1.5 | Migration framework with AX/SAP connectors and reconciliation | 10 | 49 | Wave 10: synthetic AX extract loads at 100% reconciliation | Load timing, idempotent re-run, mapping DSL units |
| FR-2.1 | Append-only universal journal, dimensions, idempotent posting, balances | 11 | 14, 15, 40 | Wave 15: closed period with statements tying to TB | Property (1M entries), negative posting tests, 5k lines/s k6 |
| FR-2.2 | Three currencies, rate providers, parallel ledgers, revaluation | 12 | 40 | Wave 15 | Rounding property, parallel-ledger fixtures, revaluation workbook |
| FR-2.3 | Tax determination/calculation/posting, providers, SAF-T | 13 | 35, 48 | Wave 15 | 500-scenario matrix, settlement reconciliation, XSD validation |
| FR-2.4 | Period control, close checklist, allocations, accruals, year-end | 14 | 38 | Wave 15: recorded close walkthrough | Contract (closed period on every path), allocation fixtures |
| FR-2.5 | Statement designer, standard statements, drill-down, audit report | 15 | 40, 46 | Wave 15 sign-off by finance stakeholders | Golden statements, drill-down property, designer round-trip |
| FR-3.1 | Immutable inventory transactions, journals, reservations, counting | 16 | 31, 33 | Wave 20: buy → receive → pay → value walkthrough | Concurrency (no negative), rebuild compare, FEFO test |
| FR-3.2 | Requisition → RFQ → agreement → PO with policies and versions | 17 | 35, 38 | Wave 20 | Approval matrix, versioning tests, price-lookup property |
| FR-3.3 | Receiving with GR/IR, invoice capture, 2/3-way match, ERS | 18 | 35 | Wave 20 | 200-scenario matching fixtures, GR/IR nets to zero |
| FR-3.4 | AP settlement, payment proposals, ISO 20022/NACHA/check, positive pay | 19 | 36 | Wave 20 | 300 settlement fixtures, pain.001 XSD, idempotent run |
| FR-3.5 | FIFO/LIFO/average/standard costing, close, GL reconciliation, portal | 20 | 39 | Wave 20 | Costing workbooks, close timing, value report ties to GL |
| FR-4.1 | Production order lifecycle, WIP, variances, subcontracting | 21 | 24, 25, 39 | Wave 25: planned order → finished goods with full costing | State-machine property, costing fixtures, backflush test |
| FR-4.2 | MRP/MPS, coverage groups, messages, pegging, forecasting | 22 | 23, 27 | Wave 25 | 40 APICS scenarios, scale 50k items, pegging completeness |
| FR-4.3 | Finite/infinite scheduling, Gantt, CP-SAT | 23 | 44, 45 | Wave 25 | OR-Library fixtures, no-double-booking property, 5k ops perf |
| FR-4.4 | Process manufacturing and lean | 24 | 33 | Wave 25 | Potency/co-product fixtures, kanban simulation, catch-weight tests |
| FR-4.5 | Shop-floor kiosk, time registration, IoT, OEE, offline | 25 | 43, 44 | Wave 25 | Kiosk E2E, 10k events/min ingest, 30-min offline test |
| FR-5.1 | Deterministic pricing engine with explain | 26 | 30, 40 | Wave 30: one simulated month buy → make → sell → collect, all subledgers reconcile | 500 pricing fixtures, determinism property, p95 < 20 ms |
| FR-5.2 | Quotations, orders, ATP/CTP, credit, holds, intake API | 27 | 35 | Wave 30 | ATP fixtures + over-promise property, credit workflow, 500 orders/min |
| FR-5.3 | Release, pick/pack/ship, packing slips with COGS, carriers, drop-ship | 28 | 31, 32, 34 | Wave 30 | Posting contract tests, 1,000-order E2E, SSCC validation |
| FR-5.4 | Invoicing variants and ASC 606/IFRS 15 | 29 | 40, 41 | Wave 30 | Totals property, 40 ASC 606 fixtures, roll-forward reconciliation |
| FR-5.5 | Cash application, collections, RMA, rebates, customer portal | 30 | 36, 47 | Wave 30 GO-LIVE CANDIDATE sign-off | Cash-application fixtures, dunning tests, RMA E2E |
| FR-6.1 | Location directives, work, waves, license plates, replenishment, loads | 31 | 34 | Wave 35: EDI PO → ASN → invoice between demo partners | 100 directive fixtures, 10k-line simulation, capacity property |
| FR-6.2 | Mobile scanner app, flow designer, GS1, offline, labels | 32 | 45 | Wave 35 | Device E2E, GS1 parser suite, 200-transaction offline sync |
| FR-6.3 | Quality orders, sampling, usage decisions, NCR/CAPA, CoA | 33 | 44 | Wave 35 | Trigger fixtures, blocking property, Z1.4 tables |
| FR-6.4 | Load/route planning, rating, tendering, freight reconciliation, landed cost | 34 | 40 | Wave 35 | Solomon VRP fixtures, 200 rating fixtures, tender timeouts |
| FR-6.5 | EDI X12/EDIFACT, Peppol, integration hub, developer portal | 35 | 48 | Wave 35 | Partner-simulator round trips, schematron, webhook at-least-once |
| FR-7.1 | Bank accounts, statements, reconciliation, cash forecast, connectivity | 36 | 48 | Wave 40: group consolidated statements, finance parity sign-off | 1,000-line reconciliation, forecast property, EBICS contract tests |
| FR-7.2 | Fixed assets, multi-book depreciation, CIP, IFRS 16 | 37 | 41 | Wave 40 | 100 asset fixtures vs MACRS tables, lease workbook, subledger tie |
| FR-7.3 | Budgeting, budget control, cost accounting, ABC | 38 | 41 | Wave 40 | Budget-check fixtures, allocation convergence, cost ledger tie |
| FR-7.4 | Costing sheets, roll-up, standard cost activation, variances | 39 | — | Wave 40 | 50-product workbook, activation revaluation, variance sums |
| FR-7.5 | Intercompany trade/accounting, consolidation with eliminations | 40 | — | Wave 40 sign-off by a practicing controller | IC balance property, 3-entity eliminations, mismatch detection |
| FR-8.1 | WBS, resources, journals, billing modes, WIP/POC, EVM | 41 | 45 | Wave 45: projects, HR, payroll, maintenance, field service walkthrough | POC workbook, invoice-proposal property, WBS scheduling |
| FR-8.2 | Workers/positions/jobs, absence, benefits, self-service, PII | 42 | 43 | Wave 45 | Effective-date property, accrual fixtures, PII contract tests |
| FR-8.3 | Country-pluggable payroll, expense management | 43 | 48 | Wave 45 | 200 Pub. 15-T scenarios, retro fixtures, payroll GL tie |
| FR-8.4 | Functional locations, maintenance plans, work orders, IoT triggers | 44 | — | Wave 45 | Plan fixtures, cost posting tie, 5 s IoT trigger |
| FR-8.5 | Installed base, agreements/SLAs, cases, dispatch, technician app | 45 | — | Wave 45 | Entitlement fixtures, dispatch property, warranty billing |
| FR-9.1 | Lakehouse, semantic layer, dashboards, document designer, catalog | 46 | — | Wave 50: first customer month-end in production; load and DR targets met | Lag < 60 s, metric = GL check, RLS in dashboards, visual regression |
| FR-9.2 | MCP APIs, copilot, governed agents, evals, cost budgets | 47 | — | Wave 50 | Eval thresholds in CI, permission-ceiling contracts, audit of actions |
| FR-9.3 | Localization framework, country packs, ER designer, SoD, SOC 2, privacy | 48 | — | Wave 50 | Regulator-schema fixtures, 50 SoD conflicts, restore drill, pen test |
| FR-9.4 | Transactional migration packs, parallel-run diff, cutover, coexistence | 49 | — | Wave 50 | 100% control totals, zero unexplained variance, 48 h rehearsal |
| FR-9.5 | Scale targets, multi-region DR, operations, first customer, v1.0 | 50 | — | Wave 50 | k6 campaign, DR game day, production close, signed release |

## K. Reference index

Consolidated from the `references` arrays in `roadmap/waves.json` (183 distinct URLs, deduplicated by URL). Wave numbers in parentheses. SAP Help URLs are the identifiers recorded in `waves.json`; verify against the current SAP Help portal before citing in ADRs.

### K.1 AX 2012 / D365 documentation (Microsoft Learn)

- AX 2012 AOT & data dictionary concepts — https://learn.microsoft.com/en-us/dynamicsax-2012/developer/data-dictionary (3)
- AX 2012 role-based security — https://learn.microsoft.com/en-us/dynamicsax-2012/developer/role-based-security-in-microsoft-dynamics-ax (2)
- AX 2012 workflow overview — https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/workflow-overview (4)
- AX 2012 number sequences — https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/about-number-sequences (5)
- AX 2012 organization model — https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/organizations-and-organizational-hierarchies (6)
- AX 2012 global address book — https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/global-address-book-overview (7)
- AX 2012 product information management — https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/about-products (8)
- AX 2012 BOMs and formulas — https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/about-bills-of-materials-and-formulas (9)
- AX 2012 routes and operations — https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/about-routes-and-operations (9)
- AX 2012 DIXF — https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/data-import-export-framework-user-guide-dixf-dmf (10)
- AX 2012 financial dimensions & account structures — https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/about-financial-dimensions-and-account-structures (11)
- AX 2012 foreign currency revaluation — https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/foreign-currency-revaluation-general-ledger (12)
- AX 2012 sales tax overview — https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/about-sales-tax (13)
- AX 2012 allocation rules — https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/about-ledger-allocation-rules (14)
- AX 2012 inventory transactions and statuses — https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/about-inventory-transactions (16)
- AX 2012 procurement and sourcing — https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/procurement-and-sourcing (17)
- AX 2012 invoice matching — https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/about-accounts-payable-invoice-matching (18)
- AX 2012 accounts payable — https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/accounts-payable (19)
- AX 2012 inventory close and costing — https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/about-inventory-close (20)
- AX 2012 production control — https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/production-control (21)
- AX 2012 master planning — https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/master-planning (22)
- AX 2012 scheduling — https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/about-scheduling (23)
- AX 2012 process manufacturing — https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/process-manufacturing-production-and-logistics (24)
- AX 2012 lean manufacturing — https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/lean-manufacturing (24)
- AX 2012 trade agreements — https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/about-trade-agreements (26)
- AX 2012 sales and marketing — https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/sales-and-marketing (27)
- AX 2012 shipping and picking — https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/about-picking-and-shipping (28)
- AX 2012 quality management — https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/about-quality-management (33)
- AX 2012 BOM calculations and costing sheets — https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/about-bom-calculations (39)
- AX 2012 production variances — https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/about-production-variances (39)
- AX 2012 project management and accounting — https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/project-management-and-accounting (41)
- AX 2012 payroll — https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/payroll (43)
- AX 2012 service management — https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/service-management (45)
- D365 F&O extensibility model — https://learn.microsoft.com/en-us/dynamics365/fin-ops-core/dev-itpro/extensibility/extensibility-home-page (3)
- D365 engineering change management — https://learn.microsoft.com/en-us/dynamics365/supply-chain/engineering-change-management/product-engineering-overview (9)
- D365 data management framework — https://learn.microsoft.com/en-us/dynamics365/fin-ops-core/dev-itpro/data-entities/data-entities-data-packages (10)
- D365 financial period close workspace — https://learn.microsoft.com/en-us/dynamics365/finance/general-ledger/financial-period-close-workspace (14)
- D365 financial reporting (Management Reporter) — https://learn.microsoft.com/en-us/dynamics365/finance/general-ledger/financial-reporting-getting-started (15)
- D365 production floor execution — https://learn.microsoft.com/en-us/dynamics365/supply-chain/production-control/production-floor-execution-use (25)
- D365 revenue recognition — https://learn.microsoft.com/en-us/dynamics365/finance/accounts-receivable/revenue-recognition-overview (29)
- D365 collections — https://learn.microsoft.com/en-us/dynamics365/finance/accounts-receivable/collections-overview (30)
- D365 warehouse management overview — https://learn.microsoft.com/en-us/dynamics365/supply-chain/warehousing/warehouse-management-overview (31)
- D365 warehouse management mobile app — https://learn.microsoft.com/en-us/dynamics365/supply-chain/warehousing/install-configure-warehouse-management-app (32)
- D365 transportation management — https://learn.microsoft.com/en-us/dynamics365/supply-chain/transportation/transportation-management-overview (34)
- D365 electronic reporting — https://learn.microsoft.com/en-us/dynamics365/fin-ops-core/dev-itpro/analytics/general-electronic-reporting (35)
- D365 cash and bank management — https://learn.microsoft.com/en-us/dynamics365/finance/cash-bank-management/cash-bank-management-overview (36)
- D365 fixed assets — https://learn.microsoft.com/en-us/dynamics365/finance/fixed-assets/fixed-assets (37)
- D365 budget control — https://learn.microsoft.com/en-us/dynamics365/finance/budgeting/budget-control-overview-configuration (38)
- D365 cost accounting — https://learn.microsoft.com/en-us/dynamics365/finance/cost-accounting/cost-accounting-home-page (38)
- D365 intercompany trade — https://learn.microsoft.com/en-us/dynamics365/supply-chain/sales-marketing/intercompany-trade (40)
- D365 consolidation — https://learn.microsoft.com/en-us/dynamics365/finance/budgeting/consolidation-elimination-overview (40)
- D365 Project Operations — https://learn.microsoft.com/en-us/dynamics365/project-operations (41)
- D365 Human Resources — https://learn.microsoft.com/en-us/dynamics365/human-resources/hr-admin-overview (42)
- D365 expense management — https://learn.microsoft.com/en-us/dynamics365/project-operations/prod-exp/expense-overview (43)
- D365 Asset Management — https://learn.microsoft.com/en-us/dynamics365/supply-chain/asset-management (44)
- D365 Field Service — https://learn.microsoft.com/en-us/dynamics365/field-service (45)
- D365 analytics / entity store — https://learn.microsoft.com/en-us/dynamics365/fin-ops-core/dev-itpro/analytics/power-bi-integration-entity-store (46)
- D365 Copilot overview — https://learn.microsoft.com/en-us/dynamics365/fin-ops-core/fin-ops/copilot/copilot-for-finance-operations (47)
- D365 localization and regulatory features — https://learn.microsoft.com/en-us/dynamics365/finance/localizations/global/regulatory-updates (48)
- D365 F&O upgrade from AX 2012 — https://learn.microsoft.com/en-us/dynamics365/fin-ops-core/dev-itpro/migration-upgrade/upgrade-overview-2012 (49)

### K.2 SAP documentation (SAP Help Portal)

- SAP ABAP Dictionary overview — https://help.sap.com/docs/ABAP_PLATFORM_NEW (3)
- SAP change documents — https://help.sap.com/docs/SAP_NETWEAVER_750 (5)
- SAP enterprise structure (company code, plant, storage location) — https://help.sap.com/docs/SAP_S4HANA_ON-PREMISE (6)
- SAP Business Partner & CVI — https://help.sap.com/docs/SAP_S4HANA_ON-PREMISE (7)
- SAP material master — https://help.sap.com/docs/SAP_ERP (8)
- SAP PP master data (BOM, routing, work center) — https://help.sap.com/docs/SAP_ERP (9)
- SAP S/4HANA Migration Cockpit — https://help.sap.com/docs/SAP_S4HANA_ON-PREMISE (10)
- SAP ACDOCA universal journal — https://help.sap.com/docs/SAP_S4HANA_ON-PREMISE (11)
- SAP parallel ledgers in New GL — https://help.sap.com/docs/SAP_ERP (12)
- SAP tax determination (FI) — https://help.sap.com/docs/SAP_ERP (13)
- SAP Financial Closing Cockpit — https://help.sap.com/docs/SAP_S4HANA_ON-PREMISE (14)
- SAP financial statement versions — https://help.sap.com/docs/SAP_ERP (15)
- SAP MM inventory management — https://help.sap.com/docs/SAP_ERP (16)
- SAP MM purchasing — https://help.sap.com/docs/SAP_ERP (17)
- SAP logistics invoice verification — https://help.sap.com/docs/SAP_ERP (18)
- SAP automatic payment program — https://help.sap.com/docs/SAP_ERP (19)
- SAP material ledger / actual costing — https://help.sap.com/docs/SAP_S4HANA_ON-PREMISE (20)
- SAP PP production orders — https://help.sap.com/docs/SAP_ERP (21)
- SAP MRP — https://help.sap.com/docs/SAP_ERP (22)
- SAP PP/DS — https://help.sap.com/docs/SAP_S4HANA_ON-PREMISE (23)
- SAP PP-PI — https://help.sap.com/docs/SAP_ERP (24)
- SAP SD condition technique — https://help.sap.com/docs/SAP_ERP (26)
- SAP SD sales order processing — https://help.sap.com/docs/SAP_ERP (27)
- SAP advanced ATP (aATP) — https://help.sap.com/docs/SAP_S4HANA_ON-PREMISE (27)
- SAP LE shipping — https://help.sap.com/docs/SAP_ERP (28)
- SAP Revenue Accounting and Reporting — https://help.sap.com/docs/SAP_REVENUE_ACCOUNTING_AND_REPORTING (29)
- SAP FSCM collections & dispute management — https://help.sap.com/docs/SAP_ERP (30)
- SAP EWM — https://help.sap.com/docs/SAP_EWM (31)
- SAP QM — https://help.sap.com/docs/SAP_ERP (33)
- SAP Transportation Management — https://help.sap.com/docs/SAP_TRANSPORTATION_MANAGEMENT (34)
- SAP Cash Management — https://help.sap.com/docs/SAP_S4HANA_ON-PREMISE (36)
- SAP asset accounting — https://help.sap.com/docs/SAP_S4HANA_ON-PREMISE (37)
- SAP CO overhead cost controlling — https://help.sap.com/docs/SAP_ERP (38)
- SAP product cost planning — https://help.sap.com/docs/SAP_ERP (39)
- SAP Group Reporting — https://help.sap.com/docs/SAP_S4HANA_FINANCE_FOR_GROUP_REPORTING (40)
- SAP PS — https://help.sap.com/docs/SAP_ERP (41)
- SAP SuccessFactors Employee Central — https://help.sap.com/docs/SAP_SUCCESSFACTORS_EMPLOYEE_CENTRAL (42)
- SAP Payroll — https://help.sap.com/docs/SAP_ERP (43)
- SAP PM — https://help.sap.com/docs/SAP_ERP (44)
- SAP Field Service Management — https://help.sap.com/docs/SAP_FIELD_SERVICE_MANAGEMENT (45)
- SAP Document and Reporting Compliance — https://help.sap.com/docs/SAP_DOCUMENT_AND_REPORTING_COMPLIANCE (48)
- SAP S/4HANA conversion guide — https://help.sap.com/docs/SAP_S4HANA_ON-PREMISE/conversion-guide (49)

### K.3 Standards, regulations and reference data

- CloudEvents specification — https://github.com/cloudevents/spec (4)
- SCIM 2.0 (RFC 7644) — https://datatracker.ietf.org/doc/html/rfc7644 (2)
- OpenAPI 3.1 — https://spec.openapis.org/oas/v3.1.0 (3)
- WCAG 2.1 — https://www.w3.org/TR/WCAG21 (5)
- ISO 4217 currency codes — https://www.iso.org/iso-4217-currency-codes.html (6)
- UCUM units of measure — https://ucum.org (6)
- VIES VAT validation — https://ec.europa.eu/taxation_customs/vies (7)
- GS1 General Specifications — https://www.gs1.org/standards/barcodes-epcrfid-id-keys/gs1-general-specifications (8)
- GS1-128 / SSCC — https://www.gs1.org/standards/id-keys/sscc (28)
- GS1 application identifiers — https://ref.gs1.org/ai (32)
- ECB exchange rates API — https://data.ecb.europa.eu/help/api/overview (12)
- IAS 21 effects of changes in FX rates — https://www.ifrs.org/issued-standards/list-of-standards/ias-21-the-effects-of-changes-in-foreign-exchange-rates (12)
- OECD SAF-T — https://www.oecd.org/tax/forum-on-tax-administration/publications-and-products/guidance-for-the-standard-audit-file-tax-version-2-0.htm (13)
- IAS 7 statement of cash flows — https://www.ifrs.org/issued-standards/list-of-standards/ias-7-statement-of-cash-flows (15)
- Peppol BIS Billing 3.0 — https://docs.peppol.eu/poacc/billing/3.0 (18, 35)
- ISO 20022 pain.001 — https://www.iso20022.org/iso-20022-message-definitions (19, 30)
- NACHA ACH file format — https://achdevguide.nacha.org/ach-file-overview (19)
- IAS 2 inventories — https://www.ifrs.org/issued-standards/list-of-standards/ias-2-inventories (20)
- OEE definition (ISO 22400) — https://www.iso.org/standard/54497.html (25)
- OPC UA — https://opcfoundation.org/about/opc-technologies/opc-ua (25)
- MQTT — https://mqtt.org (25)
- ASC 606 / IFRS 15 — https://www.ifrs.org/issued-standards/list-of-standards/ifrs-15-revenue-from-contracts-with-customers (29)
- BAI2 lockbox format — https://www.bai.org (30)
- ANSI/ASQ Z1.4 sampling — https://asq.org/quality-resources/sampling (33)
- X12 EDI standards — https://x12.org (35)
- EBICS — https://www.ebics.org/en/home (36)
- IRS Publication 946 (MACRS) — https://www.irs.gov/publications/p946 (37)
- IFRS 16 leases — https://www.ifrs.org/issued-standards/list-of-standards/ifrs-16-leases (37)
- IFRS 10 consolidated financial statements — https://www.ifrs.org/issued-standards/list-of-standards/ifrs-10-consolidated-financial-statements (40)
- IRS Publication 15-T — https://www.irs.gov/publications/p15t (43)
- NIST AI RMF — https://www.nist.gov/itl/ai-risk-management-framework (47)
- SOC 2 Trust Services Criteria — https://www.aicpa-cima.com/topic/audit-assurance/audit-and-assurance-greater-than-soc-2 (48)
- ISO/IEC 27001 — https://www.iso.org/standard/27001 (48)
- GDPR — https://gdpr.eu (48)
- SLSA supply-chain levels — https://slsa.dev/spec (1)
- Google SRE workbook — https://sre.google/workbook/table-of-contents (50)

### K.4 Open-source implementations, platform components and design references

- Argo CD app-of-apps pattern — https://argo-cd.readthedocs.io/en/stable/operator-manual/cluster-bootstrapping (1)
- OpenTofu — https://opentofu.org/docs (1)
- sigstore cosign — https://docs.sigstore.dev/cosign/overview (1)
- k3d — https://k3d.io (1)
- Keycloak — https://www.keycloak.org/documentation (2)
- Open Policy Agent / Rego — https://www.openpolicyagent.org/docs/latest (2)
- Postgres row-level security — https://www.postgresql.org/docs/current/ddl-rowsecurity.html (2)
- Atlas schema migrations — https://atlasgo.io (3)
- Strimzi — https://strimzi.io/documentation (4)
- Debezium outbox event router — https://debezium.io/documentation/reference/stable/transformations/outbox-event-router.html (4)
- Debezium — https://debezium.io/documentation (46)
- Transactional outbox pattern — https://microservices.io/patterns/data/transactional-outbox.html (4)
- Temporal — https://docs.temporal.io (4, 47)
- Module Federation — https://module-federation.io (5)
- OpenTelemetry — https://opentelemetry.io/docs (5)
- Jepsen — https://jepsen.io (5)
- Record linkage / Fellegi-Sunter (Splink) — https://moj-analytical-services.github.io/splink (7)
- Google OR-Tools CP-SAT — https://developers.google.com/optimization/cp/cp_solver (8)
- OR-Tools job-shop scheduling — https://developers.google.com/optimization/scheduling/job_shop (23)
- OR-Tools vehicle routing — https://developers.google.com/optimization/routing (34)
- OR-Library job-shop instances — http://people.brunel.ac.uk/~mastjjb/jeb/orlib/jobshopinfo.html (23)
- Great Expectations (data validation) — https://docs.greatexpectations.io (10, 49)
- Martin Fowler: Accounting patterns — https://martinfowler.com/apsupp/accounting.pdf (11)
- Modern Treasury: double-entry ledgers — https://www.moderntreasury.com/journal/accounting-for-developers-part-i (11)
- TigerBeetle debit/credit ledger design — https://docs.tigerbeetle.com/concepts/debit-credit (11)
- Avalara AvaTax API — https://developer.avalara.com/api-reference/avatax/rest/v2 (13)
- Apache OFBiz inventory data model — https://cwiki.apache.org/confluence/display/OFBIZ/Data+Model+Diagrams (16)
- Odoo purchase module (reference flows) — https://www.odoo.com/documentation/18.0/applications/inventory_and_mrp/purchase.html (17)
- Odoo MRP (reference flows) — https://www.odoo.com/documentation/18.0/applications/inventory_and_mrp/manufacturing.html (21)
- frePPLe (open-source planning) — https://frepple.com/docs/current (22)
- statsforecast (ETS/Croston) — https://nixtlaverse.nixtla.io/statsforecast (22)
- OpenBoxes (open-source WMS) — https://docs.openboxes.com (31)
- Zebra ZPL — https://www.zebra.com/us/en/support-downloads/knowledge-articles/zpl-command-information-and-details.html (32)
- Backstage API catalog — https://backstage.io/docs/features/software-catalog (35)
- HashiCorp Vault — https://developer.hashicorp.com/vault/docs (36)
- Frappe HRMS (open-source reference) — https://docs.frappe.io/hr (42)
- openMAINT (open-source CMMS) — https://www.openmaint.org/en (44)
- Apache Iceberg — https://iceberg.apache.org/docs/latest (46)
- Cube semantic layer — https://cube.dev/docs (46)
- OpenMetadata — https://docs.open-metadata.org (46)
- Model Context Protocol — https://modelcontextprotocol.io (47)
- Anthropic Claude API — https://docs.anthropic.com (47)
- CloudNativePG — https://cloudnative-pg.io/documentation (50)
- k6 load testing — https://grafana.com/docs/k6/latest (50)
- Kafka MirrorMaker 2 — https://kafka.apache.org/documentation/#georeplication (50)

## L. Competitive benchmark — top-tier manufacturing ERPs

### L.1 Capability matrix

The matrix compares the six systems a mid-to-large manufacturer shortlists against the Passage v1.0 target. Cells name the actual feature or module and give typical numbers where they are public or widely reported by customers and analysts; "n/a" means the capability is absent or only available through a partner product. Figures marked "typical" are customer-reported ranges, not vendor commitments, and should be re-validated per deal.

| Capability | SAP S/4HANA | D365 F&SCM | Infor LN / CloudSuite Industrial | Epicor Kinetic | IFS Cloud | Plex | Passage target |
|---|---|---|---|---|---|---|---|
| MRP full regeneration, 50k items | MRP Live on HANA: minutes (typically < 10 min; MD01N) | Planning Optimization (Azure service): typically 5–30 min; legacy master planning: hours | LN: batch overnight typical; CSI (SyteLine) APS: minutes to hours | MRP regeneration batch, typically overnight; net-change intraday | MRP/MS batch, typically hours; incremental supported | Net-change MRP continuous, minutes | < 10 min for 50k items × 12 periods × 5 BOM levels; deterministic; net-change continuous |
| Live pegging / explain plan | MD04 stock/requirements list; PP/DS pegging | Planning Optimization pegging view | LN order pegging (project pegging); CSI APS pegging | Time Phase inquiry pegging | Supply/demand pegging in Supply Chain Planning | Limited (order-level links) | Pegging tree API explaining 100% of any planned order's quantity |
| Finite capacity scheduling | Embedded PP/DS with optimizer (extra license) | Finite capacity in master planning + Gantt; no optimizer | Infor Scheduling / CSI APS finite scheduling | APS module (add-on), finite with rules | Constraint-Based Scheduling (CBS) module | Production Scheduling (finite, drag-drop) | Finite/infinite + CP-SAT optimizer built in; 5k operations < 60 s |
| Shop-floor execution UX | SAP Digital Manufacturing (separate cloud, extra license) or MII | Production floor execution (touch, job cards) | Infor Factory Track (separate) | Kinetic MES (Advanced MES, ex-Mattec) | Shop Floor Workbench; IFS MES | Native MES, machine-integrated; strongest of the group | Touch PWA kiosk, 30-min offline, time → costing and payroll in one flow |
| Machine / IoT connectivity | SAP DM Edge, SAP IoT | Sensor Data Intelligence (Azure IoT Hub) | Infor OS IoT + Factory Track | Advanced MES direct PLC monitoring | IoT Business Connector | Native PLC data collection | OPC-UA/MQTT edge connector → Kafka; 10k events/min per plant, no loss on broker restart |
| OEE out of the box | In SAP DM | Via Sensor Data Intelligence / Power BI; limited native | Factory Track | Advanced MES real-time OEE | Via MES; limited native | Native real-time OEE | Native, ISO 22400 definitions, 5 s refresh |
| Product configurator (CTO) | Variant Configuration (LO-VC / AVC) | Product configuration models (constraint-based) | Infor CPQ (separate); LN configurator | Kinetic Configurator (rules-based) | Sales Configurator | Basic option selection | Constraint-based (OR-Tools CP-SAT), 50-constraint fixtures, drives BOM/route |
| Process manufacturing: formula, potency, co/by-products, catch weight | PP-PI recipes, batch management, active ingredient | Full: formulas, potency, co/by-products, catch weight, batch attributes | LN: limited; CloudSuite Food & Beverage (M3) strong | Limited in Kinetic | Recipe/formula, batch; limited potency | Lot, catch weight; formulas for food/bev | Wave 24: formulas, potency, co/by allocation, catch weight, shelf life, batch disposition |
| Lean / kanban | KANBAN (PK*), REM | Lean manufacturing: production flows, kanban rules/boards | CSI kanban basic | Kanban basic | Kanban/repetitive basic | Kanban native | Production flows, event/fixed/withdrawal kanban, backflush costing |
| Quality management | QM: inspection lots, usage decisions, notifications; CAPA via QIM | Quality orders, nonconformances, CoA, test groups | Infor Quality / LN quality basic | Advanced Quality (AQM) with NCR/CAPA | Quality Management: control plans, nonconformance | Native: control plans, SPC, gage R&R, PPAP | Rule-triggered quality orders, ANSI Z1.4 AQL, 8D CAPA, SPC, CoA, supplier scorecards, calibration |
| Advanced WMS | EWM (basic embedded or advanced, extra license) | Advanced warehouse: location directives, waves, LPs | Infor WMS (separate); LN warehouse basic | Kinetic warehouse basic + Advanced Mobile Warehouse | Warehouse data collection (WADACO) basic | Native basic/mid WMS | Location directives, work templates, waves, LPs, replenishment, slotting, dock scheduling |
| WMS mobile | EWM RF framework / Fiori | Warehouse Management mobile app (Android, configurable flows) | Factory Track mobile | Mobile Warehouse | Aurena mobile scanning | Native mobile | PWA + Android shell, flow designer, offline queue, GS1 AI parsing, ZPL printing |
| Transportation management | SAP TM (embedded) | TMS native (rating, routing, load building) | Infor Nexus (separate) | n/a (partner) | Limited | n/a | VRP load/route planning, rate shopping, tendering, freight reconciliation, landed cost |
| Inventory costing methods | Standard, moving average, actual (material ledger) | FIFO, LIFO, weighted average, standard, moving average | Standard, actual, FIFO/LIFO (lot) | Standard, average, last, FIFO, lot FIFO | Standard, weighted average, FIFO (lot), actual | Standard, actual (average) | FIFO, LIFO, weighted average (running/periodic), standard, moving average; close ties to GL |
| Universal journal (single line-item truth) | ACDOCA: one table for FI/CO/AA/ML | GeneralJournalAccountEntry + separate subledger tables | GL with integration transactions | GL with posting (booking) rules | GL vouchers | GL | Append-only journal; balances are rebuilt projections; every subledger reconciles daily by automated check |
| Parallel ledgers / multi-GAAP | Leading + non-leading ledgers, currency types 10/30/40 | Posting layers (current/operations/tax + custom) | Limited (parallel companies / dimensions) | Multiple GL books | Parallel accounting (IFRS/local) | n/a | Ledger groups per legal entity; 3 currency amounts per line |
| Month-end close, typical | 3–5 days with Advanced Financial Closing | 5–8 days | 5–10 days | 5–10 days | 5–10 days | 5–10 days | ≤ 3 days; continuous close via daily reconciliation (Appendix I.6) |
| ASC 606 / IFRS 15 | Revenue Accounting and Reporting (RAR) | Revenue recognition module (being replaced by subscription billing) | n/a (partner) | n/a | Project-based rev rec | n/a | Native: performance obligations, SSP allocation, ratable/milestone/usage schedules |
| Intercompany & consolidation | Cross-company postings; Group Reporting | Intercompany trade; consolidation | CloudSuite consolidation | Multi-company, consolidation | Group consolidation | Multi-plant; limited consolidation | Mirror documents, due-to/due-from matching, eliminations, IAS 21 translation |
| EAM and field service | PM + Field Service Management | Asset Management + Field Service (separate app) | Infor EAM (now HxGN EAM, separate) | Maintenance basic | Strongest EAM/FSM of the group | Basic maintenance | Native EAM (plans, work orders, IoT triggers) + field service (dispatch optimization, technician app) |
| Localization coverage | 60+ country versions | 40+ countries | 40+ (LN) | ~20 country packs | 50+ countries | US-centric, ~10 | 7 packs in v1.0 (US, UK, DE, FR, IN, MX, CA); framework for v1.x |
| Extensibility model | ABAP (key-user, developer, BTP side-by-side) | X++ extensions (no overlayering), Power Platform | ION, Mongoose (CSI), LN Extensions | Application Studio (low-code), BPM, C# | Aurena low-code configuration + PL/SQL | Configuration; limited custom code | Metadata extensions (entities, enums, fields, workflows, forms, reports) + sanctioned hooks; no proprietary language |
| API openness | OData/SOAP on SAP Business Accelerator Hub (thousands, uneven coverage) | OData data entities (2,000+), custom services | ION APIs (REST), BODs | REST API v2 (OData) across the product | REST projections (OData) across the product | REST/SOAP via developer portal | OpenAPI 3.1 generated for every entity and engine; GraphQL read layer; additive-only within /v1 |
| Event streaming | Event Mesh / Advanced Event Mesh (CloudEvents) | Business events → Event Grid / Service Bus / Dataverse | ION BOD publishing (XML) | Limited (Epicor Functions, webhooks) | IFS Connect events | Limited webhooks | Every state change is a replayable CloudEvent on Kafka via outbox; AsyncAPI published |
| Analytics / data access | Embedded CDS analytics; Datasphere | Entity store; Synapse/Fabric link | Birst; Data Lake | Epicor Data Analytics (Grow) | Analysis models; IFS Analytics | Plex Intelligence | Debezium CDC → Iceberg lakehouse, Cube semantic layer, p95 lag < 60 s, open to any BI tool |
| AI agents | Joule agents; Business AI | Copilot; autonomous agents (reconciliation, supplier comms) | Infor GenAI / Industry AI agents | Prism agents | IFS.ai agents | Copilot features | MCP-exposed APIs; agents inherit user OPA permissions; evals gate deploys; model-agnostic |
| Audit trail | Change documents (per table, configurable); AIS | Database log (opt-in per table, performance cost) | Configurable audit trail | Change log per table (opt-in) | History logging (opt-in) | Change logs | Field-level from CDC on every table, hash-chained, 7-year fiscal retention |
| Segregation of duties | GRC Access Control (separate) | Native SoD rules | Infor GRC (separate) | n/a | n/a | n/a | Native SoD engine with mitigation workflow and certification campaigns |
| Deployment model | Public cloud, private cloud (RISE), on-premise | Microsoft-managed Azure only (on-premise LBD retired) | Multi-tenant AWS (CloudSuite) or on-premise LN | Epicor-managed Azure or on-premise | IFS-managed Azure or customer-managed Kubernetes (remote deployment) | SaaS only, multi-tenant | Any conformant Kubernetes (EKS/GKE/AKS/bare-metal/k3d); self-hosted or managed; no provider-managed service in the critical path |
| Upgrade cadence and effort | Annual (on-prem) / biannual (public cloud) mandatory; on-prem upgrades are multi-month projects | 4 mandatory service updates per year (One Version); regression testing each | Monthly cloud updates; LN on-prem by project | Biannual releases; cloud updates mandatory | Biannual releases + monthly service updates (evergreen) | Continuous (weekly) | Continuous; zero-downtime expand/contract migrations; digest rollback < 15 min; customer-controlled cadence |
| Licensing | Named users (Professional/Functional), FUE under RISE; list prices not public | Per user per month (Finance / SCM list ≈ USD 180–210 per full user; Activity/Team tiers lower) | Subscription per user; not public | Subscription per user; not public | Subscription per user; not public | Subscription per plant and user; not public | Open question 1 (decide by wave 35): open-core or source-available; no per-seat charge for shop-floor, portal or agent users |
| Implementation, mid-size manufacturer (typical) | 12–24 months | 9–18 months | 6–12 months (CSI) | 6–12 months | 9–18 months | 6–12 months | 3–6 months using the migration factory (wave 49) and parallel-run diff |
| Accessibility | Fiori: WCAG 2.1 AA for most apps | WCAG 2.1 AA | Partial | Partial | Partial | Partial | WCAG 2.1 AA on every generated form and workspace, axe in CI |

Reading the matrix: the incumbents win today on breadth of localization, installed-base references and depth in a few modules (SAP finance, IFS EAM, Plex MES). Passage does not try to out-feature them row by row in v1.0; it competes on the rows where every incumbent is structurally weak — one journal with automated reconciliation, metadata extensibility without a proprietary language, event streaming and open APIs as the default integration surface, cloud-agnostic deployment, continuous zero-downtime upgrades, and governed AI agents that act under the same authorization as users.

### L.2 Interoperability contract

Principle: any system on the customer's network — MES, PLM, SCADA/OPC-UA, a third-party WMS, EDI trading partners, BI tools, AI agents — consumes Passage through the same APIs, events and MCP tools that Passage's own modules use. There is no separate "integration layer" product, no licensed middleware, and no integration project to stand up before the first interface works. What SAP sells as Integration Suite, Microsoft as dual-write plus Logic Apps, and Infor as ION is, in Passage, the integration hub shipped in wave 35: a catalog, keys and quotas, a message monitor and replay over surfaces that already exist from wave 3 (REST), wave 4 (Kafka) and wave 47 (MCP).

The contract every surface honors:

1. **One authorization model.** Every call, event subscription, webhook and tool invocation is authorized by the same OPA bundle against the same roles/duties/privileges (Appendix E). Partner credentials are users or service accounts in the tenant's Keycloak realm; there is no integration back door.
2. **One schema source.** OpenAPI, AsyncAPI, GraphQL SDL, MCP tool definitions and EDI mappings are generated from the same entity metadata, so a field added by a customer extension appears on every surface in the same release.
3. **Additive evolution.** `/v1` and `v<n>` event types change additively only; breaking changes ship as a new version with a 12-month (API) or two-minor-release (event) overlap, announced with `Deprecation`/`Sunset` headers and registry flags.
4. **Correlation and idempotency everywhere.** Every inbound request carries or receives a `correlationid`; every outbound event and webhook carries it; every mutating call is idempotent by key (Appendix D.5); every consumer is expected to be idempotent by event id (Appendix C.5).
5. **Replayability.** Events are retained 30 days and replayable per consumer; webhooks can be re-driven from the message monitor; file and EDI inbound payloads are kept as source attachments.
6. **Sandbox and conformance.** Every tenant gets a sandbox tenant with the demo companies; a conformance kit (Postman/k6/Pact suites) lets a partner prove their integration before production keys are issued.
7. **Observability shared with the customer.** Per-key traces, delivery logs, DLQ contents and SLA dashboards are visible in the integration hub, not only to Passage operators.
8. **No data hostage.** Full export (NDJSON per entity, Iceberg tables, event archive) is a standard API, not a service engagement.

| Surface | Typical consumers | Protocol / format | Authentication and authorization | SLA (per tenant, measured) | Notes |
|---|---|---|---|---|---|
| REST | MES, PLM, custom apps, iPaaS, mobile | HTTPS/JSON, OpenAPI 3.1, RFC 9457 errors | OAuth 2.0 (Keycloak): authorization code for users, client credentials for services; JWT validated at Envoy, OPA per request; tenant API keys for partner quotas | 99.9% availability; p95 < 50 ms simple reads, p99 < 500 ms document posts; rate limits per tier (D.7) | Idempotency keys, ETags, cursor pagination, bulk `:batch`/`:export` |
| GraphQL | BI tools, portals, composite screens, low-code builders | HTTPS, GraphQL over the generated SDL (read-only in v1.0; mutations via REST) | Same JWT and OPA; field-level masking for P2/P3 per duty | 99.9%; p95 < 200 ms for queries ≤ 1,000 cost points; depth ≤ 8, cost-limited | Persisted queries for portals; mirrors REST filter grammar |
| Kafka topics | MES, third-party WMS, data platform, CRM sync, customer event consumers | Kafka protocol, CloudEvents JSON, AsyncAPI 3.0, schema registry | mTLS + SCRAM-SHA-512 per principal; topic ACLs per tenant; cross-tenant principals only for the customer's own analytics | 99.9% broker availability; p95 commit-to-consumer < 5 s; 30-day retention; replay on request | External consumers connect through the tenant's dedicated consumer group; DLQ visible in hub |
| Webhooks | SaaS partners, iPaaS (Workato, Make), Salesforce/Shopify connectors | HTTPS POST of the CloudEvent, JSON | HMAC-SHA256 signature in `X-Passage-Signature` with per-endpoint secret (rotation supported), optional mTLS, IP allow-list | At-least-once; p95 first delivery < 10 s; retries with exponential backoff for 24 h, then DLQ; per-tier delivery quotas | Subscriptions filtered by event type and legal entity; re-drive from monitor |
| MCP | AI agents (Passage copilot, Claude, customer-built agents, partner agents) | MCP over HTTPS (streamable), tool/resource definitions generated from OpenAPI and the semantic layer | OAuth 2.1 with user-delegated token; agent identity registered in the tenant; OPA evaluates `agent@user` so the agent never exceeds the user (E.4 example 2); kill switch | 99.9%; p95 tool call < 1 s excluding downstream posting time; per-tenant token and cost budgets | Every tool call audited with inputs/outputs; approval gates for posting tools |
| EDI | Trading partners, 3PLs, carriers, retailers | X12 (850/855/856/810/820/214) and EDIFACT (ORDERS/DESADV/INVOIC) over AS2 or SFTP; Peppol UBL 2.1 via access point | AS2 certificates per partner, SFTP key pairs, Peppol access-point credentials; partner profile binds to a service account with scoped duties | 997/CONTRL functional acknowledgment within 15 min; inbound processing p95 < 5 min; partner SLA monitor with alerting | Mappings versioned per partner; replay from message monitor; partner simulator in sandbox |
| OPC-UA / MQTT | SCADA, PLCs, machines, sensors, historians, Andon displays | OPC-UA client (Sign & Encrypt) and MQTT 3.1.1/5 over TLS via the edge connector; events normalized to CloudEvents on Kafka | X.509 device certificates issued per plant; per-device MQTT credentials; connector runs under a plant service account | 10k events/min per plant; store-and-forward on edge for 24 h; end-to-end p95 < 5 s to OEE/condition monitoring; no loss on broker restart | Tag/topic mapping is metadata; thresholds raise maintenance requests (wave 44) |
| File / SFTP | Banks, payroll providers, tax providers, legacy systems, lockbox | SFTP (ISO 20022 pain/camt, NACHA, BAI2, MT940, CSV/NDJSON), optional PGP | SFTP key authentication per partner, PGP keys and bank signing keys in Vault (never exported), checksum manifests | Polling every 5 min; inbound processing p95 < 15 min; outbound files signed and audited (E.7); bank acknowledgments (pain.002) ingested | Idempotent by file hash; duplicates rejected and logged; retention per fiscal rules |

A customer therefore adds an MES by subscribing it to `passage.manufacturing.productionorder` and `passage.inventory.transaction`, pointing it at `/v1/manufacturing/production-orders/{id}:report` for confirmations, and issuing it a service account with the `ReportProduction` duty — the same three artifacts Passage's own shop-floor kiosk uses. Replacing that MES later means revoking one principal and one subscription, not unwinding a middleware project.
