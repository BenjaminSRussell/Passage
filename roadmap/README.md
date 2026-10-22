# Passage Roadmap — 50 Waves

Generated from `waves.json` and `phases.json` by `scripts/build_roadmap.py`. Edit the JSON, not this file.

Each wave ships a **deliverable**, a **proof** (automated tests/fixtures/perf targets), a **deploy** (what runs where), and **references** (AX/SAP docs and standards/open-source we build against). Waves are strictly sequential; every 5th wave is a phase-exit with stakeholder sign-off. Wave 30 is the first go-live candidate; wave 50 is production go-live and v1.0.

## Phase 0: Foundation & Platform Kernel (waves 1-5)

Build the platform every module stands on: Kubernetes delivery pipeline, identity and tenancy, the metadata/extensibility kernel (our AOT/DDIC), Kafka eventing and workflow, audit and the UX shell. Nothing business-facing ships until this layer can deploy, observe and roll back safely.

| Wave | Name | Goal |
|---|---|---|
| 1 | [Delivery Pipeline & Cluster Baseline](phase-0-foundation.md#wave-01-delivery-pipeline) | Any engineer can merge a change and watch it reach a Kubernetes environment with rollback in under 15 minutes. |
| 2 | [Identity, Tenancy & Authorization](phase-0-foundation.md#wave-02-identity-tenancy) | Users log in once, land in the right legal entity, and every API call is authorized by a policy engine rather than code. |
| 3 | [Metadata Kernel (Entities, Fields, Extensions)](phase-0-foundation.md#wave-03-metadata-kernel) | Business entities are declared once in a metadata model that generates the Postgres schema, API, validation, and UI form — and customers can extend any entity without forking. |
| 4 | [Event Backbone, Workflow & Batch](phase-0-foundation.md#wave-04-events-workflow) | Every business change is an immutable event on Kafka; long-running processes (approvals, batch jobs) run on a durable workflow engine with retries, timers and human tasks. |
| 5 | [UX Shell, Number Sequences, Audit & Observability](phase-0-foundation.md#wave-05-ux-shell-audit-observability) | A single web shell renders any metadata-defined entity as list/detail/grid forms with personalization; every change is audited; every request is traceable. |

## Phase 1: Enterprise Structure, Master Data & Engineering Data (waves 6-10)

Model the enterprise (legal entities, sites, org hierarchies), the shared party/business-partner model, products and variants, BOMs/routings (engineering data a manufacturer needs on day one), and the migration framework every later wave uses to load legacy AX/SAP data.

| Wave | Name | Goal |
|---|---|---|
| 6 | [Enterprise Structure & Organization Model](phase-1-master-data.md#wave-06-enterprise-structure) | Model tenants, legal entities, operating units, departments, sites and warehouses with effective-dated hierarchies that every module filters by. |
| 7 | [Global Party & Business Partner Model](phase-1-master-data.md#wave-07-global-party-model) | One party record per real-world organization or person, with customer/vendor/employee/contact roles layered on top, deduplicated and governed. |
| 8 | [Product Master, Variants & Catalogs](phase-1-master-data.md#wave-08-product-master) | Define products once (released per legal entity), with variants/dimensions, tracking dimensions, UoM conversions, attributes and catalogs. |
| 9 | [Engineering Data: BOMs, Routings, Resources & Change Management](phase-1-master-data.md#wave-09-engineering-data) | A manufacturer can define multi-level BOMs, routings with operations and resources, and control changes with engineering change orders and effective dates. |
| 10 | [Data Migration Framework & Legacy Connectors](phase-1-master-data.md#wave-10-migration-framework) | Repeatable, validated loads of AX and SAP master data into Passage with reconciliation reports — the tool every later wave uses for its own data. |

## Phase 2: Financial Core (waves 11-15)

Stand up a single append-only universal journal (one source of truth, like SAP ACDOCA / AX GeneralJournalAccountEntry) with dimensions, multi-currency, parallel ledgers, tax, period close and financial reporting. Every later module posts through this engine.

| Wave | Name | Goal |
|---|---|---|
| 11 | [Universal Journal & Chart of Accounts](phase-2-financial-core.md#wave-11-universal-journal) | One append-only, double-entry journal with financial dimensions is the single source of truth for every posting; it balances by construction. |
| 12 | [Multi-Currency, Parallel Ledgers & Revaluation](phase-2-financial-core.md#wave-12-currency-parallel-ledgers) | Every posting carries transaction, accounting and reporting currency amounts; parallel ledgers (IFRS/local GAAP/tax) post simultaneously; FX revaluation is automated. |
| 13 | [Tax Engine (Sales/Use, VAT/GST, Withholding)](phase-2-financial-core.md#wave-13-tax-engine) | Any document line gets the right tax determination, calculation and posting across jurisdictions, with pluggable external providers and audit-ready reporting. |
| 14 | [Period Close, Year-End & Allocations](phase-2-financial-core.md#wave-14-period-close) | Controllers run a checklist-driven close across entities with period controls per module, automated allocations and accruals, and a closed period that cannot be reopened silently. |
| 15 | [Financial Reporting, Statements & Audit Trail](phase-2-financial-core.md#wave-15-financial-reporting) | Balance sheet, income statement and cash flow are generated from the journal with drill-down to the source document, plus a report designer for custom statements. |

## Phase 3: Inventory & Procure-to-Pay (waves 16-20)

Inventory ledger and costing, purchasing, receiving with three-way match, accounts payable and payments, supplier collaboration. Physical goods start moving and valuing correctly into the GL.

| Wave | Name | Goal |
|---|---|---|
| 16 | [Inventory Ledger, Dimensions & On-Hand](phase-3-procure-to-pay.md#wave-16-inventory-ledger) | Every quantity movement is an immutable inventory transaction with physical/financial status, storage and tracking dimensions, and real-time on-hand at any dimension level. |
| 17 | [Purchasing: Requisitions, RFQs, Agreements & Purchase Orders](phase-3-procure-to-pay.md#wave-17-purchasing) | Buyers run requisition → sourcing → PO with approval policies, agreements and change management, and suppliers receive orders electronically. |
| 18 | [Receiving, Product Receipts & Three-Way Match](phase-3-procure-to-pay.md#wave-18-receiving-invoice-matching) | Goods receipt updates inventory and accrues liability; vendor invoices match to PO and receipt within tolerances or route to resolution. |
| 19 | [Accounts Payable, Settlement & Payments](phase-3-procure-to-pay.md#wave-19-accounts-payable-payments) | Open vendor items settle against payments with discounts and withholding; payment runs produce bank files in ISO 20022 and post to cash. |
| 20 | [Inventory Costing & Supplier Collaboration](phase-3-procure-to-pay.md#wave-20-inventory-costing-supplier-portal) | Inventory is valued correctly under FIFO/LIFO/weighted-average/standard with closing/adjustment runs that reconcile to the GL; suppliers self-serve via a portal. |

## Phase 4: Manufacturing & Planning (waves 21-25)

Production orders and WIP, MRP/MPS, capacity and scheduling, process and lean manufacturing, shop-floor execution and IoT. A discrete or process manufacturer can build product on Passage.

| Wave | Name | Goal |
|---|---|---|
| 21 | [Production Orders, WIP & Order-Based Costing](phase-4-manufacturing.md#wave-21-production-orders) | Create, estimate, release, start, report and end production orders with material issues and route consumption posting WIP and variances to the GL. |
| 22 | [Master Planning: MRP, MPS & Forecasting](phase-4-manufacturing.md#wave-22-mrp-master-planning) | Net requirements calculation across demand, supply and BOM levels generates planned purchase/production/transfer orders with action and delay messages. |
| 23 | [Capacity Planning & Finite Scheduling](phase-4-manufacturing.md#wave-23-capacity-scheduling) | Operations are scheduled against finite resource capacity with calendars, Gantt visualization and rescheduling, feeding realistic dates back to planning. |
| 24 | [Process Manufacturing & Lean (Formulas, Batches, Kanban)](phase-4-manufacturing.md#wave-24-process-lean-manufacturing) | Process manufacturers run formula-based batch orders with potency, co/by-products and catch weight; lean plants run kanban and production flows. |
| 25 | [Shop Floor Execution, Time Registration & IoT](phase-4-manufacturing.md#wave-25-shop-floor-execution) | Operators clock on jobs from a touch UI, machines report counts via OPC-UA/MQTT, and labor/machine time flows to production costing and payroll. |

## Phase 5: Order-to-Cash (waves 26-30)

Pricing, sales orders with ATP, fulfillment, billing and revenue recognition, accounts receivable and collections. At the end of wave 30 a manufacturer runs the full buy-make-sell-collect loop on Passage — the first go-live milestone.

| Wave | Name | Goal |
|---|---|---|
| 26 | [Pricing, Discounts & Trade Agreements](phase-5-order-to-cash.md#wave-26-pricing-trade-agreements) | A deterministic pricing engine resolves prices and discounts from layered agreements, promotions and rebates for any customer/product/date/quantity. |
| 27 | [Sales Orders, Quotations, ATP & Credit](phase-5-order-to-cash.md#wave-27-sales-orders-atp) | Sales reps quote and take orders with real-time available-to-promise, credit checks, and configured products, with order holds and change control. |
| 28 | [Fulfillment: Picking, Packing & Shipping](phase-5-order-to-cash.md#wave-28-fulfillment-shipping) | Orders release to the warehouse, pick/pack/ship with packing slips and carrier labels, and inventory deducts with COGS posting on shipment. |
| 29 | [Billing, Invoicing & Revenue Recognition](phase-5-order-to-cash.md#wave-29-billing-revenue-recognition) | Invoices (one-off, consolidated, subscription, milestone) post revenue, tax and AR, with ASC 606 / IFRS 15 recognition schedules for bundled and deferred revenue. |
| 30 | [Accounts Receivable, Collections, Returns & Customer Portal](phase-5-order-to-cash.md#wave-30-accounts-receivable-collections) | Cash application settles customer items (lockbox, remittance, bank), collections manages dunning and disputes, returns flow through RMA — and the manufacturer go-live loop closes. |

## Phase 6: Warehouse, Quality & Logistics (waves 31-35)

Advanced warehouse management, mobile execution, quality management, transportation and B2B/EDI/e-invoicing integration.

| Wave | Name | Goal |
|---|---|---|
| 31 | [Advanced Warehouse Management](phase-6-logistics.md#wave-31-advanced-warehouse) | Location-directed work (put-away, replenishment, wave picking, cluster/zone picking) driven by configurable rules, with license plates and work templates. |
| 32 | [Mobile Warehouse Execution (Scanners, RF, Voice)](phase-6-logistics.md#wave-32-mobile-warehouse) | Warehouse workers execute all work on handheld scanners with configurable menus, barcode/RFID capture and offline tolerance. |
| 33 | [Quality Management & Nonconformance](phase-6-logistics.md#wave-33-quality-management) | Quality orders are generated by rules at receipt, production and shipment; results drive disposition, nonconformance, corrective action and supplier scorecards. |
| 34 | [Transportation Management & Freight](phase-6-logistics.md#wave-34-transportation) | Plan loads and routes, rate-shop carriers, tender, track and reconcile freight invoices against expected charges. |
| 35 | [B2B Integration: EDI, e-Invoicing & Integration Hub](phase-6-logistics.md#wave-35-b2b-integration) | Trading partners exchange orders, ASNs and invoices via EDI X12/EDIFACT and Peppol, and any external system integrates through a governed API/event hub. |

## Phase 7: Treasury, Assets & Controlling (waves 36-40)

Cash and bank, fixed assets, cost accounting and budgeting, standard-cost variance analysis, intercompany and consolidation. The finance function reaches full AX/SAP parity.

| Wave | Name | Goal |
|---|---|---|
| 36 | [Cash & Bank Management, Reconciliation & Treasury](phase-7-treasury-controlling.md#wave-36-cash-bank-treasury) | Bank accounts, statements and reconciliation are automated via ISO 20022; cash positioning and forecasting give treasury a daily view. |
| 37 | [Fixed Assets & Leases](phase-7-treasury-controlling.md#wave-37-fixed-assets) | Assets are acquired from AP/projects/inventory, depreciated under multiple books and methods, transferred, split, revalued and disposed with full GL integration; leases follow IFRS 16. |
| 38 | [Cost Accounting, Budgeting & Budget Control](phase-7-treasury-controlling.md#wave-38-cost-accounting-budgeting) | Controllers plan and control costs by cost center/object with budgets, budget control on commitments, and management reporting separate from statutory. |
| 39 | [Product Costing, Cost Roll-Up & Variance Analysis](phase-7-treasury-controlling.md#wave-39-product-costing-variances) | Standard costs roll up from BOMs/routings with cost groups and overhead sheets; production and purchase variances are analyzed by cause. |
| 40 | [Intercompany & Financial Consolidation](phase-7-treasury-controlling.md#wave-40-intercompany-consolidation) | Intercompany trade, transfers and expenses post mirror documents automatically across legal entities; group consolidation eliminates and translates for the consolidated statements. |

## Phase 8: Projects, People & Service (waves 41-45)

Project operations, HR core, payroll and expenses, enterprise asset management and field service.

| Wave | Name | Goal |
|---|---|---|
| 41 | [Project Operations: WBS, Time, Expense, Billing & Accounting](phase-8-projects-people-service.md#wave-41-project-operations) | Project-centric businesses plan WBS and resources, capture time/expense/items, bill by T&M/fixed-price/milestone, and recognize revenue and WIP. |
| 42 | [Human Resources Core: Workers, Positions, Absence & Talent](phase-8-projects-people-service.md#wave-42-hr-core) | Workers, positions, jobs and departments are managed with effective dating; employees self-serve absence, benefits enrollment and personal data; managers handle reviews. |
| 43 | [Payroll Engine & Expense Management](phase-8-projects-people-service.md#wave-43-payroll-expenses) | A country-pluggable gross-to-net payroll engine runs pay cycles from time and HR data and posts to the GL; employees submit expenses with policy checks and reimbursement. |
| 44 | [Enterprise Asset Management (Maintenance)](phase-8-projects-people-service.md#wave-44-enterprise-asset-management) | Maintain plant assets with preventive plans, condition-based triggers from IoT, work orders with parts and labor, and asset lifecycle costing. |
| 45 | [Service Management & Field Service](phase-8-projects-people-service.md#wave-45-service-management) | Service agreements, cases and work orders are dispatched to field technicians with parts, skills, SLA tracking and mobile execution; warranty and installed base are tracked. |

## Phase 9: Intelligence, Compliance & Go-Live (waves 46-50)

Data platform and analytics, AI copilots and agents, globalization and compliance, then the migration factory, scale hardening and production cutover off AX/SAP.

| Wave | Name | Goal |
|---|---|---|
| 46 | [Data Platform, Analytics & Embedded BI](phase-9-intelligence-golive.md#wave-46-data-platform-analytics) | Every domain streams into a governed lakehouse with semantic models; users get embedded dashboards, self-service analytics and operational KPIs without touching OLTP. |
| 47 | [AI Copilots, Agents & Automation](phase-9-intelligence-golive.md#wave-47-ai-copilots-agents) | Users ask questions and delegate work in natural language; governed agents execute multi-step processes (cash application, planning exceptions, AP coding) with human-in-the-loop and full audit. |
| 48 | [Globalization, Localization & Compliance Controls](phase-9-intelligence-golive.md#wave-48-globalization-compliance) | Passage runs legally in target countries (statutory reports, e-invoicing mandates, localized documents) and passes SOC 2 / ISO 27001-style controls with segregation of duties. |
| 49 | [Migration Factory: Full AX/SAP Transactional Cutover Tooling](phase-9-intelligence-golive.md#wave-49-migration-factory) | A customer on AX 2012/D365 F&O or SAP ECC/S4 can be migrated — master data, open transactions, balances, history — with automated reconciliation and parallel-run comparison. |
| 50 | [Scale Hardening, Multi-Region & Production Go-Live](phase-9-intelligence-golive.md#wave-50-scale-hardening-golive) | Passage runs a real customer in production at enterprise scale with multi-region resilience, documented operations and a public release. |
