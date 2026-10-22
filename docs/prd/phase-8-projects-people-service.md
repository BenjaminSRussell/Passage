# Part 8 — Phase 8: Projects, People & Service (waves 41–45)

## 8.0 Phase overview

**Business outcome at phase exit.** At the end of wave 45 the demo group (manufacturer plus the engineering-services entity added in wave 41) runs project operations, HR core, payroll, expense management, plant maintenance and field service on Passage, with every financial effect posted through the universal journal (wave 11) and reconciled daily. A project manager plans a WBS, books resources, captures time and expense, bills by T&M, fixed-price milestone or retainer, and recognizes revenue by percentage-of-completion. An HR administrator manages workers, positions and jobs with effective dating; employees self-serve absence and benefits. A payroll administrator runs a US gross-to-net cycle that matches IRS Publication 15-T to the cent and posts to the GL. A maintenance planner keeps plant machines on preventive and counter-based plans whose work orders cost to the asset and GL. A service coordinator dispatches field technicians under SLA with OR-Tools, and technicians execute offline on a PWA. This phase is where Passage reaches parity with AX Project management and accounting, HR, Payroll, Expense management, D365 Asset Management and Service management, and with SAP PS, HCM PA/OM/PT, PY, Concur, PM and CS/FSM.

**Phase-exit sign-off criteria**
- Percentage-of-completion estimate fixtures (12 projects, 4 periods each) match the accountant-reviewed workbook to 0.01 USD/EUR per period.
- Property test over 10,000 generated transaction sets: every invoice proposal contains exactly the billable transactions selected by its billing rule, no duplicates, no omissions.
- Effective-dated position/manager as-of queries return the correct answer for 100% of 50,000 randomized dates across the demo workforce (1,200 workers).
- 200 US payroll fixture scenarios match Pub 15-T percentage-method computations to the cent; payroll posting reconciles to GL accounts 6100–6199 and 2300–2349 with zero variance.
- Expense policy engine flags violations exactly per rule set on a 500-report fixture; reimbursements post to AP and settle in a wave-19 payment run.
- Maintenance plan fixtures (40 plans, time and counter) generate work orders on the expected dates/readings; work order cost (parts + labor) reconciles to asset cost and GL maintenance accounts.
- IoT threshold breach creates a maintenance request in p95 < 5 s; recorded downtime reduces wave-23 scheduling capacity.
- Dispatch property test: 1,000 generated schedules are feasible (skills, time windows, shifts) with a solver time p95 < 30 s for 200 work orders × 25 technicians.
- Recorded phase-exit walkthrough: projects, HR, payroll, maintenance and field service end-to-end on `stage`, signed off by both owners.

**Cross-wave data model summary**

| Entity | Owning service | Key relationships |
|---|---|---|
| Project, WbsNode, ProjectTransaction, InvoiceProposal, ProjectEstimate | `projects` (Go, Postgres `projects`) | Customer (wave 7 party), Worker (wave 42), Journal (wave 11), Fixed asset CIP (wave 37) |
| Worker, Employment, Position, Job, Department, AbsencePlan, BenefitPlan | `hr` (TS, Postgres `hr`) | Legal entity/org hierarchy (wave 6), Time registrations (wave 25), User (Keycloak) |
| PayCycle, PayRun, PayStatement, EarningCode, DeductionCode, TaxRuleSet | `payroll` (Go, Postgres `payroll`) | Worker/Employment (wave 42), Journal (wave 11), Bank/payment file (wave 36) |
| ExpenseReport, ExpenseLine, ExpensePolicyRule, CardTransaction | `expense` (TS, Postgres `expense`) | Worker, Project (wave 41), AP invoice (wave 18), Tax (wave 13) |
| FunctionalLocation, Asset, MaintenancePlan, MaintenanceRequest, MaintWorkOrder, Counter | `eam` (Go, Postgres `eam`) | Resource (wave 22), Inventory item (wave 16), Fixed asset (wave 37), IoT reading (wave 25) |
| ServiceObject (installed base), ServiceAgreement, Case, ServiceWorkOrder, DispatchAssignment | `service` (TS) + `dispatch` (Python/OR-Tools), Postgres `service` | Customer, Sales shipment serial (wave 28), Worker (technician), Truck stock location (wave 16), Invoice (wave 29) |

**Cross-wave event catalog**

| Event (CloudEvents type) | Producer | Consumers | Payload summary |
|---|---|---|---|
| `dev.passage.projects.transaction.posted.v1` | projects | journal, analytics, expense (link-back) | projectId, wbsId, txType, qty, cost, salesPrice, currency, journalId |
| `dev.passage.hr.employment.changed.v1` | hr | payroll, projects (resource validity), service (technician), Keycloak sync | workerId, employmentId, action, effectiveFrom, effectiveTo |
| `dev.passage.hr.absence.approved.v1` | hr | time (wave 25), projects (availability), payroll (paid-leave earnings) | workerId, planId, from, to, hours |
| `dev.passage.payroll.run.posted.v1` | payroll | journal, bank (wave 36), analytics | payRunId, payCycleId, grossTotal, netTotal, employerTaxTotal, journalId |
| `dev.passage.expense.report.approved.v1` | expense | ap (wave 18), projects | reportId, workerId, totalAmount, currency, lines[] |
| `dev.passage.eam.request.created.v1` | eam (or IoT bridge) | service desk UI, notifications | requestId, assetId, source, severity, readingValue |
| `dev.passage.eam.workorder.completed.v1` | eam | journal (cost settlement), fixed assets, scheduling (wave 23) capacity release | workOrderId, assetId, partsCost, laborCost, downtimeMinutes |
| `dev.passage.service.sla.breached.v1` | service | notifications, analytics | caseId or workOrderId, slaType, dueAt, breachedAt |
| `dev.passage.service.dispatch.published.v1` | dispatch | technician PWA, service | scheduleDate, assignments[] (workOrderId, technicianId, start, end) |

**Phase-level risks**

| Risk | Likelihood | Impact | Mitigation | Owner |
|---|---|---|---|---|
| Payroll tax legal exposure (wrong withholding, late forms) | Medium | High | Table-driven rule sets versioned per tax year; 200-scenario Pub 15-T fixture gate; ADP/Gusto-style provider adapter as alternative path | Engineering/security |
| PII breach or over-exposure of HR/payroll data | Medium | High | PII fields in a separate vaulted schema with column-level encryption, OPA duty `HcmPIIRead`, masked by default, every read audited | Engineering/security |
| POC revenue disputes with auditors | Low | High | Accountant-reviewed workbook fixtures; estimate reversal/re-run is idempotent; loss-provision rule explicit | Product |
| Dispatch optimizer infeasible or slow at scale | Medium | Medium | CP-SAT with 30 s cap and greedy fallback; property test over generated instances | Engineering |
| Offline technician PWA conflicts | Medium | Medium | Per-work-order append-only action log with server-side merge; last-writer-wins only for free-text | Engineering |
| Scope of five large modules in five waves | High | High | Each wave ships the thin vertical slice in its deliverable list; talent/recruitment, subscriptions and predictive maintenance are explicitly basic | Product |

## 8.41 Wave 41: Project Operations: WBS, Time, Expense, Billing & Accounting

### 8.41.1 Scope and business context

Project-centric businesses (engineering services, contract manufacturing, construction-adjacent, professional services) need a project ledger that AX delivers through Project management and accounting (ProjTable, ProjGroup, ProjWBS, hour/expense/item journals, ProjProposalJour, ProjEstimate) and SAP through PS (PROJ/PRPS WBS, AUFK networks, CJ20N, DP90 resource-related billing, KKA2 results analysis, CJ88 settlement). At the end of this wave a user can create a project hierarchy and WBS with scheduled tasks and resource bookings, record hour/expense/item/fee transactions that cost the project and post to the GL, generate invoice proposals by T&M, fixed-price milestone or retainer, run percentage-of-completion estimates that post WIP and accrued revenue, and read committed cost, EVM and profitability. A demo engineering-services legal entity (`ES-US`) is added alongside the manufacturer.

Out of scope: grant management beyond customer funding sources with limits; project quotations (use wave 26 quotes with a project link); multi-currency funding-rule splitting beyond percentage splits; resource scheduling optimization (wave 23 engine is reused for capacity only); D365 Project Operations "Project for the web" scheduling parity.

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| AX invoice proposal (ProjProposalJour via "Create invoice proposal" dialog) takes 9–12 clicks per project and silently skips transactions whose line property changed after posting; SAP DP90 requires a DIP profile (ODP1) that most sites never get right, so billing runs produce missing lines nobody notices until the customer disputes | Selection is a batch class with no explainable predicate; eligibility state lives on the transaction and is mutated by other processes | Declarative billing rules with a stated predicate (BR-41.3), a "why not billed" explain endpoint per transaction, and proposals built in one action from the project workspace | Clicks 12 → 2 per proposal; omitted-billable-transaction rate 0 (property test); dispute rate attributable to billing errors −90% |
| Fixed-price estimates (ProjEstimate, "Create estimate" periodic job; SAP KKA2 + CJ88) run as month-end batch taking 1–4 h on 2,000 projects; reversal (ProjEstimate "Reverse") and re-run is manual and frequently leaves orphan WIP | Batch framework; estimate and settlement are two separate steps with separate postings | Estimate run as a Temporal workflow per project with idempotent reverse-and-rerun in one call; WIP projection reconciled nightly to GL | Estimate run 4 h → < 10 min for 2,000 projects; orphan WIP incidents 0; re-run effort 30 min → 1 click |
| Committed cost (ProjCommittedCost) refreshed by batch ("Update committed costs") so budget checks are hours stale; SAP availability control fires only at PO save and cannot be queried live | Polling batch over PurchLine; no event stream | Committed cost projection updated from PO/receipt events within 10 s and queryable per WBS node | Staleness 4 h batch → < 10 s; PO-over-budget surprises at month-end −95% |
| WBS scheduling in ProjWBS form recalculates the whole plan synchronously (30–90 s on 1,000 activities) and ignores worker calendars unless "Scheduling with resources" is enabled; SAP CJ20N networks require AFVC maintenance per activity | Form-bound X++ scheduler; calendars optional | CPM scheduler over role calendars, < 500 ms for 2,000 nodes, baseline versioning | Reschedule 60 s → < 0.5 s; calendar-violating task dates 0 |
| Hour journal → ledger reconciliation (ProjEmplTrans vs LedgerTrans) done in Excel each close; SAP CATS → CO → PS needs three transfer steps (CAT5/CAT7) | Separate posting paths with no shared id | Every transaction carries journal_id; subledger-to-GL reconciliation is an automated nightly check with zero tolerance | Reconciliation effort 2 person-days per close → 0; variance 0.00 |
| External systems (PLM, MES, BI) read project cost via nightly SQL extracts of ProjTable/ProjCostTrans | No supported API/event surface | REST/GraphQL endpoints plus CloudEvents on Kafka and MCP exposure (wave 47) | Integration lead time weeks → same day; data latency 24 h → < 60 s |

### 8.41.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Project master and hierarchy | ProjTable, ProjGroup, ProjType (TimeMaterial, FixedPrice, Internal, Cost, Investment, Time), form ProjTable | PROJ (project definition), PRPS (WBS elements), CJ20N Project Builder | `projects` service, `Project` entity, screen Projects › Project | full |
| WBS and scheduling | ProjWBS, ProjPlanVersion, ProjWBSActivityEstimate | PRPS, AUFK networks, AFVC activities, CJ20N | `WbsNode`, `WbsDependency`, CPM scheduler | full |
| Hour/expense/item journals | ProjJournalTable/ProjJournalTrans → ProjEmplTrans, ProjCostTrans, ProjItemTrans, ProjRevenueTrans | CATS (CATSDB) → CO postings; MM goods issue to WBS; KB21N activity allocation | `ProjectJournal`, `ProjectTransaction` (type HOUR/COST/ITEM/FEE) | full |
| Invoice proposals | ProjProposalJour, ProjProposalEmpl/Cost/Item/OnAcc, ProjInvoiceJour | DP90/DP91 resource-related billing, billing request → VF01 | `InvoiceProposal` → wave-29 `Invoice` | full |
| Fixed-price estimates | ProjEstimate, ProjPeriodTable, completed-contract / percentage-of-completion, cost template | KKA2 results analysis (RA keys, RA version), CJ88 settlement to CO-PA/GL | `ProjectEstimate` with method COMPLETED_CONTRACT / POC_COST / POC_MILESTONE | full |
| Ledger posting profiles | ProjPosting (ProjLedgerPostingSetup), ProjLinePropertySetup, ProjCategory | Cost elements, OKB9 default account assignment, settlement profile | `ProjectPostingProfile` keyed by (projectGroup, category, lineProperty) | full |
| Customer funding / grants | ProjFundingSource, ProjFundingRule, ProjFundingLimit, ProjGrant | Not native (grants management GM module) | `FundingSource`, `FundingRule` (percentage split, limits) | partial — grant compliance reporting dropped; v1.x |
| Resource roles and bookings | ResResource, ResRole, ResResourceRole, PSAProjectTeam; D365 PO bookable resources | HR mini-master in PS, capacity planning CMP2 | `ResourceRole`, `ResourceBooking` (soft/hard) | full |
| Committed cost | ProjCommittedCost, ProjCommitmentTmp | Commitment items, CJI5 | `CommittedCost` projection from PO/requisition events | full |
| EVM and reporting | Project statements, cash flow, invoice control | CJI3, S_ALR_87013532 cost/revenue reports | `/reports/projects/evm` (PV/EV/AC, CPI, SPI) | improved — live rather than batch |
| Project quotations | SalesQuotationTable (type Project), "Transfer to project" (ProjQuotationTransfer) | VA21 quotation with WBS account assignment, DP80 sales pricing on project | `ProjectQuotation` = wave-27 quotation kind PROJECT with estimate lines, convert-to-project (FR-41.12) | full |
| Weekly timesheets | TSTimesheetTable/TSTimesheetLine, TSTimesheetPeriod, TSFavorites, TSTimesheetDelegate, timesheet workflow | CATS: CAT2 (data entry profile CAC1/CAC2), CATS_APPR_LITE approval, CAT5 transfer to PS, CADO/CATC inquiries | `Timesheet`, `TimesheetPeriod`, `TimesheetFavorite`, `TimesheetDelegate` (FR-41.13) | full |
| Retention (retainage) terms | ProjRetentionTerms (customer), vendor retention terms on project PO, "Retention release" | Retention in payment terms (OBB9 installments), S/4 PO retention (ME21N "Retention" tab), special G/L indicator for retention | `RetentionTerm`, `RetentionLedger` (FR-41.14) | full |
| Indirect cost components | ProjIndirectCostComponentGroup, ProjIndirectCostComponent, ProjIndirectCostRule (R3 PSA) | Costing sheet KZS2, overhead calculation CJ44/CJ45 (KGI2) on WBS | `IndirectCostComponent`, `IndirectCostRule` (FR-41.15) | full |
| Project interest | not native | CJZ1/CJZ2/CJZ3/CJZ5 interest calculation on projects; interest profiles OPIB/OPIC/OPID | `InterestProfile`, interest run (FR-41.16) | full |
| Simulation versions | ProjPlanVersion copy (no transfer) | CJV1–CJV6 simulation versions, OPTS/OPT version profiles | `PlanVersion.kind = SIMULATION` with transfer and diff (FR-41.17) | full |
| Change orders / claims | not native (PSA change requests via workflow) | CLM1/CLM2/CLM3/CLM10/CLM11 claims, TQ80 claim notification type CL | `ProjectChangeOrder` (FR-41.18) | full |
| Project stock and deliveries | Item requirements (ProjItemRequirement), project inventory journals | Special stock Q (project stock), OPS valuated/non-valuated, MD51 project MRP, CN33 BOM transfer, CNS0 delivery from project | wave-16 ownership dimension `project_id`, wave-22 planning scope PROJECT, wave-28 shipment from project stock (FR-41.19) | full |
| Forecasts, risks and issues | ProjForecastEmpl/Cost/Item/Revenue/OnAcc forecast models | CPM (Commercial Project Management) financial planning, risk/issue management | `ProjectForecast` models, `ProjectRiskIssue` (FR-41.20) | partial — CPM multi-version financial plans reduced to forecast models |

### 8.41.3 Functional requirements

- **FR-41.1 Project master and hierarchy** — Must. The system shall maintain projects with type (TIME_MATERIAL, FIXED_PRICE, INTERNAL, COST, INVESTMENT, TIME), group, customer, contract, currency, dates, stage and an unlimited parent/child hierarchy.
  - Stages: CREATED → IN_PROCESS → FINISHED (and ON_HOLD from IN_PROCESS); transitions gated by duty `ProjManage`.
  - Sub-projects inherit group, customer and contract unless overridden; a sub-project cannot change customer.
  - Project ID from wave-5 number sequence `PROJ`, 20 chars max.
- **FR-41.2 WBS with scheduling** — Must. The system shall maintain WBS nodes per project plan version with effort, duration, dependencies (FS/SS/FF/SF with lag), constraints and a CPM scheduler respecting resource calendars.
  - Plan versions: one BASELINE (immutable after approval), one WORKING; copying creates a new version.
  - Scheduler recomputes early/late start/finish, slack and critical path in < 500 ms for 2,000 nodes.
  - Calendars come from wave-8 resource calendars; a task cannot be scheduled on a non-working day of its assigned role calendar.
- **FR-41.3 Resource roles and bookings** — Must. The system shall define roles with cost and sales prices and allow soft/hard bookings of workers (wave 42) or generic resources to WBS nodes.
  - Hard booking over 100% utilization on a day is rejected; soft booking is warned.
  - Price precedence for hours: worker-specific → role × project → role × customer → role.
- **FR-41.4 Project transactions** — Must. The system shall record HOUR, COST (expense), ITEM, FEE and ON_ACCOUNT transactions through journals or integrated documents (timesheets wave 25, expense wave 43, PO/receipt wave 17, inventory journal wave 16).
  - Each transaction carries cost price, sales price, line property (chargeable/non-chargeable), category, WBS node, quantity, three-currency amounts.
  - Posting is atomic with the universal journal through the wave-11 posting API with idempotency key `projects:txn:{journalId}:{lineNo}`.
- **FR-41.5 Posting profiles** — Must. The system shall resolve GL accounts for cost, WIP, accrued revenue, invoiced revenue and on-account by (projectGroup, category, lineProperty) with fallbacks to (group, *, *) and (*, *, *).
  - Resolution failure blocks posting with error `PROJ-POST-001` naming the missing key.
- **FR-41.6 Invoice proposals** — Must. The system shall build invoice proposals selecting billable transactions per billing rule (T&M, fixed-price milestone, retainer/on-account, unit-of-delivery) and hand them to wave-29 invoicing.
  - Selection filters: project set, date range, transaction types, line property CHARGEABLE, not previously proposed, funding-rule allocation.
  - A proposal is editable until POSTED; removing a line returns it to the pool.
- **FR-41.7 Fixed-price estimates** — Must. The system shall run periodic estimates with methods COMPLETED_CONTRACT, POC_COST (cost-to-cost) and POC_MILESTONE, posting WIP, accrued revenue and loss provisions, with reversal and re-run.
  - Period source: wave-14 fiscal periods; one estimate per project per period; re-run reverses the prior posting first.
- **FR-41.8 Committed cost** — Should. The system shall project committed costs from requisitions, POs and item requirements in near-real time (< 10 s lag) and surface remaining budget.
- **FR-41.9 Customer funding sources** — Should. The system shall split project costs/revenue among funding sources by percentage with per-source limits and a limit-exceeded hold.
- **FR-41.10 EVM and profitability reporting** — Must. The system shall compute PV, EV (POC × BAC), AC, CPI, SPI, EAC and gross margin per project and WBS node as of any date.
- **FR-41.11 Project purchase orders and subcontracts** — Must. The system shall allow wave-17 POs to reference project/WBS so that receipts create ITEM/COST transactions and open POs appear as committed cost.
- **FR-41.12 Project quotations** — Should. The system shall create project quotations as wave-27 quotations of kind PROJECT with estimate lines (hours by role, expenses, items, fees, on-account milestones) and convert a WON quotation into a project with a BASELINE plan version built from the estimate lines.
  - Conversion creates the project in stage CREATED, copies customer, contract, currency and dimensions, and keeps `quotation_id` for quote-to-actual comparison.
  - A quotation converts once; a second attempt raises `PROJ-QUO-001`.
  - Hour estimate lines are priced by role sales price (FR-41.3 precedence); margin preview shows cost, sales and margin % before sending.
- **FR-41.13 Weekly timesheets** — Must. The system shall provide per-worker timesheets per timesheet period (period types WEEK, SEMI_MONTH, MONTH per legal entity) with lines by project, WBS node, category and line property, hours per day, internal/external comments, favorites, delegates, workflow approval routed per project and posting to HOUR transactions.
  - Favorites are saved (project, WBS, category) combinations; "copy previous period" copies lines without hours.
  - A delegate (TimesheetDelegate with validity) may enter and submit on behalf of a worker; the audit log records both identities.
  - Periodic creation generates DRAFT timesheets from favorites at period start; the missing-timesheets inquiry lists workers without a SUBMITTED sheet after period end.
  - Wave-25 time registrations on project jobs appear as lines with source TIME_REG and cannot be edited in the timesheet.
  - Validation per BR-41.9; statuses DRAFT → SUBMITTED → APPROVED (per line) → POSTED; RETURNED → DRAFT.
- **FR-41.14 Retention terms** — Should. The system shall apply customer retention terms (percent held per invoice until milestone or project completion, release schedule) on invoice proposals and vendor retention terms on project purchase orders, keep a retention ledger per project and party, and release retention through release proposals (customer) or released vendor due lines (wave 18).
- **FR-41.15 Indirect cost components** — Should. The system shall define indirect cost component groups, components (basis HOUR_COST, HOUR_QTY or EXPENSE_COST; rate or amount per unit; cost and optional sales side) and rules by (legal entity, project group, category, worker group, effective date), and post INDIRECT transactions at hour posting time per BR-41.11.
- **FR-41.16 Project interest** — Could. The system shall define interest profiles (annual rate source, basis WIP or cost balance, day count ACT/365 or ACT/360, treatment CAPITALIZE or EXPENSE) assigned per project group or project, and run a monthly interest calculation that posts capitalized interest to WIP/CIP for INVESTMENT and FIXED_PRICE projects per BR-41.12.
- **FR-41.17 Simulation plan versions** — Should. The system shall allow plan versions of kind SIMULATION (copied from WORKING or BASELINE), scheduled and costed independently, compared side by side (dates, effort, cost, critical path) and transferred into WORKING with a diff and conflict resolution per BR-41.14.
- **FR-41.18 Change orders and claims** — Must. The system shall record project change orders of type SCOPE_CHANGE, CLAIM_CUSTOMER, CLAIM_VENDOR or INTERNAL with cost and revenue impact lines per WBS node, attachments, customer approval evidence, and a lifecycle DRAFT → SUBMITTED → APPROVED | REJECTED → APPLIED that adjusts contract value, budget and baseline per BR-41.13.
- **FR-41.19 Project stock and deliveries** — Should. The system shall segregate project-owned inventory through the wave-16 ownership dimension `project_id` (VALUATED, NON_VALUATED or NONE per project group), plan project demand per project in wave 22, and ship from project stock through wave-28 shipments that create ITEM transactions on the WBS node.
- **FR-41.20 Forecasts, risks and issues** — Should. The system shall maintain forecast models (hour, expense, item, fee, on-account lines by period and WBS node) with copy-to-budget and forecast-vs-actual, and a risk and issue register per project (probability 1–5, impact 1–5, owner, mitigation, due date, status).

### 8.41.4 Data model

**Project**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | text(20) | yes | PK; number sequence PROJ |
| tenant_id | uuid | yes | RLS |
| legal_entity_id | uuid | yes | FK wave 6 |
| parent_project_id | text(20) | no | FK self |
| project_group_id | text(10) | yes | FK ProjectGroup (type, posting profile) |
| project_type | enum | yes | TIME_MATERIAL, FIXED_PRICE, INTERNAL, COST, INVESTMENT, TIME |
| customer_party_id | uuid | cond | required for external types |
| contract_id | uuid | cond | FK ProjectContract (billing rules, funding) |
| currency | char(3) | yes | project currency |
| stage | enum | yes | CREATED, IN_PROCESS, ON_HOLD, FINISHED |
| start_date, end_date | date | yes/no | end_date ≥ start_date |
| dimension_set_id | uuid | yes | default financial dimensions |
| version | int | yes | optimistic concurrency |

**WbsNode**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| project_id, plan_version_id | text, uuid | yes | FK; plan version BASELINE/WORKING |
| wbs_code | text(40) | yes | unique per plan version, e.g. `1.2.3` |
| parent_id | uuid | no | FK self |
| name | text(120) | yes | |
| effort_hours | numeric(12,2) | no | ≥ 0 |
| duration_days | numeric(8,2) | no | working days |
| early_start, early_finish, late_start, late_finish | date | computed | scheduler output |
| constraint_type, constraint_date | enum, date | no | ASAP, SNET, FNLT, MSO |
| role_id | uuid | no | default resource role |
| is_billable_milestone | bool | yes | default false |

**ProjectTransaction**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| project_id, wbs_node_id | text, uuid | yes/no | |
| txn_type | enum | yes | HOUR, COST, ITEM, FEE, ON_ACCOUNT |
| category_id | text(20) | yes | FK ProjectCategory (type-bound) |
| line_property | enum | yes | CHARGEABLE, NON_CHARGEABLE |
| txn_date | date | yes | |
| quantity | numeric(14,3) | yes | hours, units |
| cost_amount, sales_amount | numeric(18,4) | yes | in project currency; plus accounting and reporting currency columns |
| currency | char(3) | yes | |
| worker_id, item_id, vendor_party_id | uuid | cond | by txn_type |
| source_doc_type, source_doc_id | text, uuid | yes | TIMESHEET, EXPENSE, PO_RECEIPT, INV_JOURNAL, MANUAL |
| journal_id | uuid | yes | universal journal reference |
| proposal_id, invoice_id | uuid | no | set when billed |
| estimate_id | uuid | no | set when captured in POC |

**InvoiceProposal**: id, project_id (or contract_id for multi-project), billing_rule_id, customer_party_id, currency, status (DRAFT, APPROVED, POSTED, CANCELED), from_date, to_date, total_cost, total_sales, tax_amount, lines[] (transaction_id, qty, unit_price, amount, funding_source_id).

**ProjectEstimate**: id, project_id, period_id, method, cost_to_date, estimated_cost_to_complete, estimated_total_cost, contract_value, poc_percent numeric(7,4), revenue_to_date, revenue_this_period, billed_to_date, accrued_revenue, deferred_revenue, loss_provision, status (DRAFT, POSTED, REVERSED), journal_id.

**Timesheet**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK; number sequence PROJ-TS |
| worker_id, legal_entity_id | uuid | yes | |
| period_id | uuid | yes | FK TimesheetPeriod (period_type WEEK/SEMI_MONTH/MONTH, from, to, status OPEN/CLOSED) |
| status | enum | yes | DRAFT, SUBMITTED, APPROVED, RETURNED, POSTED |
| entered_by_worker_id | uuid | yes | delegate when ≠ worker_id |
| lines[] | TimesheetLine | yes | (project_id, wbs_node_id, category_id, line_property, hours[7..31] by day, internal_comment, external_comment, source MANUAL/FAVORITE/TIME_REG, line_status, approver_id) |
| total_hours | numeric(8,2) | computed | |
| submitted_at, posted_at | timestamptz | no | |
| version | int | yes | optimistic concurrency |

**ProjectChangeOrder**: id (sequence PROJ-CO), project_id, co_type (SCOPE_CHANGE, CLAIM_CUSTOMER, CLAIM_VENDOR, INTERNAL), title, description, status (DRAFT, SUBMITTED, APPROVED, REJECTED, APPLIED), lines[] (wbs_node_id, cost_impact, revenue_impact, effort_hours_delta, schedule_days_delta), customer_approval_ref, vendor_party_id (claims), applied_baseline_version_id, applied_at. **RetentionTerm**: id, party_role (CUSTOMER/VENDOR), retention_pct numeric(5,2), release_trigger (MILESTONE, PROJECT_FINISHED, DATE_OFFSET), release_schedule[] (milestone_wbs_id or offset_days, release_pct). **RetentionLedger**: project_id, party_id, party_role, source_doc_id, retained_amount, released_amount, currency. **IndirectCostComponent**: group_id, code, basis (HOUR_COST, HOUR_QTY, EXPENSE_COST), rate numeric(9,4), amount_per_unit, cost_category_id, sales_applicable bool. **IndirectCostRule**: component_id, legal_entity_id, project_group_id, category_id, worker_group_id, validity. **InterestProfile**: code, rate_source (FIXED, wave-12 rate table), annual_rate numeric(7,4), basis (WIP, COST), day_count (ACT_365, ACT_360), treatment (CAPITALIZE, EXPENSE). **PlanVersion**: id, project_id, kind (BASELINE, WORKING, SIMULATION), sequence, copied_from_version_id, locked bool. **ProjectForecast**: model_id, project_id, wbs_node_id, period_id, txn_type, qty, cost_amount, sales_amount. **ProjectRiskIssue**: id, project_id, kind (RISK, ISSUE), probability smallint, impact smallint, owner_worker_id, mitigation, due_date, status (OPEN, MITIGATED, CLOSED). **ProjectQuotation**: wave-27 quotation id, project_id (after conversion), estimate_lines[] (txn_type, role_id/category_id/item_id, qty, cost_price, sales_price).

Invariants: Σ ProjectTransaction.cost_amount per project equals the project cost subledger balance and the GL WIP+cost accounts for the posting profile; a transaction appears on at most one POSTED proposal; one POSTED estimate per (project, period); WBS codes unique per plan version. Indexing: `ProjectTransaction (tenant_id, project_id, txn_date)`, partial index on `proposal_id IS NULL AND line_property = 'CHARGEABLE'` for proposal selection; `WbsNode (plan_version_id, parent_id)`; table partitioned by tenant hash (16).

### 8.41.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | `/api/v1/projects` | Create project | `Idempotency-Key` header | ProjMaintain |
| PUT | `/api/v1/projects/{id}/plans/{ver}/wbs` | Replace WBS (bulk) | version ETag | ProjPlan |
| POST | `/api/v1/projects/{id}/plans/{ver}/schedule` | Run CPM scheduler | pure | ProjPlan |
| POST | `/api/v1/projects/journals` | Create hour/cost/item/fee journal | key | ProjJournalMaintain |
| POST | `/api/v1/projects/journals/{id}/post` | Post journal to project and GL | key | ProjJournalPost |
| POST | `/api/v1/projects/invoice-proposals` | Build proposal from selection criteria | key | ProjBillingMaintain |
| POST | `/api/v1/projects/invoice-proposals/{id}/post` | Hand to invoicing | key | ProjBillingPost |
| POST | `/api/v1/projects/estimates/run` | Run estimates for period | key per (project, period) | ProjEstimatePost |
| POST | `/api/v1/projects/estimates/{id}/reverse` | Reverse estimate | key | ProjEstimatePost |
| GET | `/api/v1/projects/{id}/evm?asOf=` | EVM metrics | n/a | ProjInquire |
| POST | `/api/v1/projects/timesheets` | Create/copy timesheet for (worker, period) | key per (worker, period) | ProjTimesheetEnter |
| POST | `/api/v1/projects/timesheets/{id}/submit` | Submit for approval | key + version | ProjTimesheetEnter |
| POST | `/api/v1/projects/timesheets/{id}/lines/{lineId}/decide` | Approve/return a line | key | ProjTimesheetApprove |
| POST | `/api/v1/projects/timesheets/{id}/post` | Post approved lines as HOUR transactions | key | ProjJournalPost |
| GET | `/api/v1/projects/timesheets/missing?periodId=` | Missing timesheets | n/a | ProjInquire |
| POST | `/api/v1/projects/quotations/{id}/convert` | Convert WON project quotation to project | key | ProjMaintain |
| POST | `/api/v1/projects/change-orders` | Create change order / claim | key | ProjMaintain |
| POST | `/api/v1/projects/change-orders/{id}/apply` | Apply APPROVED change order (new baseline) | key | ProjChangeOrderApprove |
| POST | `/api/v1/projects/{id}/plans/{ver}/transfer` | Transfer SIMULATION into WORKING | key + ETag | ProjPlan |
| POST | `/api/v1/projects/interest/run` | Monthly interest calculation | key per (project, period) | ProjEstimatePost |
| POST | `/api/v1/projects/retention/release` | Build retention release proposal | key | ProjBillingMaintain |
| GET | `/api/v1/projects/{id}/forecasts/{modelId}` | Forecast vs actual | n/a | ProjInquire |

Commands validate: journal lines must reference a project in IN_PROCESS; category type must match txn_type; posting date within open period for module PROJECTS (wave 14); proposal selection requires a billing rule whose currency equals project currency.

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| `dev.passage.projects.transaction.posted.v1` | journal/document posted | see 8.0 | journal, analytics, expense |
| `dev.passage.projects.invoice-proposal.posted.v1` | proposal → POSTED | proposalId, invoiceId, lines[] | invoicing (wave 29), analytics |
| `dev.passage.projects.estimate.posted.v1` | estimate posted or reversed | estimateId, projectId, periodId, pocPercent, revenueThisPeriod, journalId, reversed | journal, analytics |
| `dev.passage.projects.wbs.scheduled.v1` | scheduler run | projectId, planVersionId, criticalPath[], finishDate | resource planning, notifications |
| `dev.passage.projects.timesheet.submitted.v1` | timesheet SUBMITTED | timesheetId, workerId, periodId, totalHours, projectIds[] | workflow (wave 4), notifications |
| `dev.passage.projects.change-order.applied.v1` | change order APPLIED | changeOrderId, projectId, contractValueDelta, budgetDelta, newBaselineVersionId | budget control (wave 38), analytics |
| `dev.passage.projects.retention.released.v1` | release proposal POSTED | projectId, partyId, releasedAmount, invoiceId | invoicing, AP |

### 8.41.6 User experience

| Screen | Primary actions | Key fields/columns | Duty |
|---|---|---|---|
| Projects workspace | tiles: proposals to approve, estimates due, over-budget, unposted journals | project, stage, budget remaining, CPI/SPI | ProjInquire |
| Project detail | stage, copy, open WBS, forecasts, statements | header fields, funding, posting profile | ProjMaintain |
| WBS Gantt | add/indent/outdent, link dependencies, schedule, baseline | code, name, effort, dates, slack, role, % complete | ProjPlan |
| Resource board | book, split, resolve over-allocation | worker/role × week heatmap | ProjPlan |
| Journals (hour/cost/item/fee) | add line, validate, post | project, WBS, category, qty, cost, sales, line property | ProjJournalMaintain |
| Invoice proposals | build, review, remove line, approve, post | transaction rows, totals, funding split | ProjBillingMaintain |
| Estimates | run, review, reverse, post | cost to date, ETC, POC, revenue, WIP | ProjEstimatePost |
| Project statements / EVM | drill to transaction and journal | PV/EV/AC, CPI, SPI, EAC, margin | ProjInquire |
| My timesheet (ESS) | add favorite, copy previous period, enter hours per day, comment, submit; delegate switch | project, WBS, category, Mon–Sun hours, period total, status per line | ProjTimesheetEnter |
| Timesheet approval (MSS / project manager) | approve/return per line or whole sheet, view comments | worker, project, hours, line property | ProjTimesheetApprove |
| Change orders | create, add impact lines, attach approval, submit, approve, apply | type, WBS, cost/revenue impact, status | ProjMaintain / ProjChangeOrderApprove |
| Plan version compare | pick two versions, diff dates/effort/cost, transfer simulation | node, Δ start, Δ finish, Δ effort, Δ cost | ProjPlan |
| Forecasts, risks and issues | add forecast lines by period, copy to budget, add risk/issue | model, period, qty, cost, sales; probability, impact, owner | ProjPlan |
| Retention ledger | view retained/released by party, build release | party, source invoice/PO, retained, released, open | ProjBillingMaintain |

Interaction rules: Gantt supports keyboard indent (Tab), outdent (Shift+Tab), link (Ctrl+L); bulk edit of category/line property on journal lines; validation on line commit with inline errors; proposal grid personalization (column set saved per user).

### 8.41.7 Business rules and edge cases

- **BR-41.1 Cost-to-cost POC** — poc = cost_to_date ÷ estimated_total_cost, rounded to 4 decimals; revenue_to_date = contract_value × poc, rounded to currency minor unit; revenue_this_period = revenue_to_date − prior revenue_to_date. Worked example (USD, fixed-price, contract 500,000.00, original estimate 400,000.00): period 1 cost to date 120,000.00, ETC 280,000.00 → EAC 400,000.00, poc 0.3000, revenue to date 150,000.00, billed to date 100,000.00 → accrued revenue 50,000.00 (debit accrued revenue, credit revenue). Period 2 cost to date 260,000.00, ETC revised to 190,000.00 → EAC 450,000.00, poc 0.5778 (260,000/450,000 = 0.57777…), revenue to date 288,888.89, period revenue 138,888.89, billed to date 300,000.00 → deferred (billed in excess) 11,111.11 as liability; gross profit to date 28,888.89. A EUR project uses the same arithmetic in EUR with accounting-currency amounts at the period-end rate.
- **BR-41.2 Loss provision** — if EAC > contract_value, the full expected loss (EAC − contract_value) is provisioned in the period discovered; later improvements release the provision, never below zero.
- **BR-41.3 Invoice proposal selection** — a transaction is selected when: line_property = CHARGEABLE, proposal_id IS NULL, txn_date ∈ [from, to], project stage ∈ {IN_PROCESS, FINISHED}, txn_type ∈ rule.types, and for FIXED_PRICE rules only ON_ACCOUNT milestones with is_billable_milestone WBS nodes whose % complete = 100 or whose date ≤ to. Transactions under a funding source whose limit is reached are selected on the next source by priority; if none, the line is HELD with reason `FUND-LIMIT`.
- **BR-41.4 Retainers** — on-account (retainer) invoices create ON_ACCOUNT transactions with negative sales; T&M proposals apply open retainer balance before invoicing net, oldest first.
- **BR-41.5 Price precedence and rounding** — hour sales price resolved per FR-41.3; amounts = qty × price rounded half-even to minor unit per line, never per total.
- **BR-41.6 Period control** — a transaction with txn_date in a closed PROJECTS period is rejected (`PROJ-PERIOD-001`); estimates run only after the period's projects sub-ledger is closed for transactions.
- **BR-41.7 Stage FINISHED** — allowed only when no open journals, no DRAFT/APPROVED proposals, WIP balance zero or an estimate with method COMPLETED_CONTRACT posted for the final period.
- **BR-41.8 Concurrency** — proposal building takes a `SELECT … FOR UPDATE SKIP LOCKED` on candidate transactions; two concurrent builds for overlapping criteria never select the same transaction.
- **BR-41.9 Timesheet validation** — one timesheet per (worker, period); the period must be OPEN and every line date inside it; Σ hours per calendar day across lines ≤ 24.00 (`PROJ-TS-003`), hours in 0.25 increments by default (parameter); the project must be IN_PROCESS (`PROJ-TS-002`); a delegate must hold a valid TimesheetDelegate row (`PROJ-TS-004`); approval routes each line to the project manager of its project (parameter "workflow by project", else to the worker's manager as-of period end per BR-42.1); a sheet is APPROVED when all lines are approved; posting creates one HOUR transaction per line per day with source TIMESHEET, idempotency key `projects:ts:{timesheetId}:{lineId}:{date}`; returned lines reopen the sheet as RETURNED; lines with source TIME_REG are read-only and pre-approved.
- **BR-41.10 Retention arithmetic** — retained amount per invoice line = round_half_even(line amount × retention_pct); retention is excluded from the invoice due amount and recorded in RetentionLedger; release is triggered by the release schedule (milestone WBS node reaches 100 % complete, project stage FINISHED, or offset days after invoice) and billed as RETENTION_RELEASE lines on a release proposal; vendor retention is withheld at wave-18 invoice posting as a separate due line whose due date is the release trigger date; a release before its trigger raises `PROJ-RET-001` unless duty ProjBillingPost overrides with a reason.
- **BR-41.11 Indirect cost application** — at HOUR posting, for each component of the project's component group the most specific matching rule (worker group > category > project group > legal entity) applies: amount = basis × rate (HOUR_COST: cost amount; HOUR_QTY: hours × amount_per_unit) rounded half-even per line; posted as INDIRECT transactions with the same WBS node and date, line property NON_CHARGEABLE unless component.sales_applicable, and with `source_doc_id` = the hour transaction; reversing the hour transaction reverses its indirect lines.
- **BR-41.12 Project interest** — monthly interest = average daily balance of the basis (WIP or cost) × annual_rate × days_in_month ÷ 365 (ACT/365) or ÷ 360 (ACT/360), rounded half-even to the minor unit; CAPITALIZE debits WIP (or CIP via wave 37 for INVESTMENT projects) and credits capitalized-interest contra; EXPENSE debits project interest expense; capitalization stops when the project reaches FINISHED or the asset is placed in service; reversal follows the estimate reversal of the same period. Example: average WIP 1,000,000.00 USD, 6.00 %, October (31 days), ACT/365 → 5,095.89.
- **BR-41.13 Change order application** — only APPROVED change orders apply; application is atomic: contract_value += Σ revenue_impact, project budget (wave 38) += Σ cost_impact, a new BASELINE plan version is created from the current baseline plus effort/schedule deltas, and the change order becomes APPLIED and immutable (`PROJ-CO-001` on a second apply); CLAIM_VENDOR creates a wave-18 vendor debit note draft for Σ cost_impact; CLAIM_CUSTOMER requires `customer_approval_ref` when the parameter "customer approval required" is on.
- **BR-41.14 Simulation transfer** — transfer computes a three-way diff (common ancestor = copied_from version, SIMULATION, current WORKING); nodes changed only in the simulation are applied; nodes changed in both since the copy are listed as conflicts (`PROJ-SIM-001`) and must be resolved node by node (keep working / take simulation); after transfer the simulation is locked; at most N simulation versions per project (parameter, default 20).

Estimate state machine: DRAFT → POSTED → REVERSED; REVERSED → (new DRAFT for same period). Proposal: DRAFT → APPROVED → POSTED; DRAFT/APPROVED → CANCELED.

### 8.41.8 Acceptance criteria

- **AC-41.1** (FR-41.7, BR-41.1) Given the fixture project in BR-41.1, when estimates run for periods 1 and 2, then revenue_this_period is 150,000.00 and 138,888.89 and the posted journal balances per the posting profile.
- **AC-41.2** (FR-41.7, BR-41.2) Given EAC revised to 520,000.00 in period 3, when the estimate runs, then loss_provision = 20,000.00 and the GL loss-provision account carries 20,000.00.
- **AC-41.3** (FR-41.6, BR-41.3) Given 10,000 generated transactions and a T&M rule for 2026-09, when a proposal builds, then every selected line satisfies the predicate and no qualifying line is omitted (property test).
- **AC-41.4** (FR-41.2) Given a WBS with FS dependency and lag 2 days on a role calendar closed on weekends, when scheduled, then successor early start is the next working day after lag.
- **AC-41.5** (FR-41.4, FR-41.5) Given an hour journal line with category LABOR and line property CHARGEABLE, when posted, then the journal debits WIP-cost and credits payroll allocation per profile and the transaction's journal_id is set.
- **AC-41.6** (FR-41.11) Given a PO line referencing WBS 1.2, when the receipt posts, then an ITEM transaction exists and committed cost decreases by the received amount within 10 s.
- **AC-41.7** (FR-41.9) Given two funding sources 70/30 with a 10,000.00 limit on source B, when proposals exceed the limit, then the excess line is HELD with reason FUND-LIMIT.
- **AC-41.8** (FR-41.10) Given BAC 400,000, poc 0.30, AC 120,000 and PV 100,000, when EVM is queried, then EV = 120,000, CPI = 1.00, SPI = 1.20.
- **AC-41.9** (FR-41.13, BR-41.9) Given a worker with two favorites and WEEK period 2026-10-19 to 2026-10-25, when periodic creation runs, then a DRAFT timesheet with two zero-hour lines exists; when 25.00 h are entered on 2026-10-21, then submit is rejected with `PROJ-TS-003`; when 8.00 h per weekday are approved and posted, then 10 HOUR transactions exist with source TIMESHEET.
- **AC-41.10** (FR-41.14, BR-41.10) Given a customer retention term of 10 % released at PROJECT_FINISHED and a T&M proposal of 20,000.00 USD, when posted, then the invoice due amount is 18,000.00 and the retention ledger shows 2,000.00 retained; when the project reaches FINISHED and the release proposal posts, then a RETENTION_RELEASE invoice of 2,000.00 exists.
- **AC-41.11** (FR-41.15, BR-41.11) Given component OVH-LABOR at 12 % of HOUR_COST for category LABOR, when an hour line of 8.00 h × 50.00 posts, then an INDIRECT transaction of 48.00 exists on the same WBS node, and reversing the hour line reverses it.
- **AC-41.12** (FR-41.18, BR-41.13) Given an APPROVED SCOPE_CHANGE with +50,000.00 revenue and +30,000.00 cost on WBS 1.3, when applied, then contract_value increases by 50,000.00, the wave-38 budget by 30,000.00, a new BASELINE version exists and a second apply returns `PROJ-CO-001`.
- **AC-41.13** (FR-41.16, BR-41.12) Given average WIP 1,000,000.00 USD and profile 6.00 % ACT/365 CAPITALIZE, when the October 2026 interest run posts, then 5,095.89 debits WIP and the estimate for October includes it in cost_to_date.
- **AC-41.14** (FR-41.12) Given a WON project quotation with 3 estimate lines, when converted, then a project in CREATED with a BASELINE plan version of 3 nodes exists and a second conversion returns `PROJ-QUO-001`.
- **AC-41.15** (FR-41.17, BR-41.14) Given a SIMULATION copied from WORKING where node 1.2 was later moved in WORKING and node 1.4 only in the simulation, when transferred, then 1.4 is applied, 1.2 is reported as a conflict, and the simulation is locked after resolution.

### 8.41.9 Test plan and fixtures

Fixtures: `proj-estimates-12x4` (12 projects × 4 periods, accountant workbook `fixtures/projects/poc-workbook.xlsx`); `proj-txn-gen` (Hypothesis generator, 10,000 transactions, seeds committed); `proj-wbs-2000` (2,000-node WBS with 3,500 dependencies); `es-us-demo` (demo entity with 25 live projects derived from wave-29 customers).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| POC arithmetic | unit | estimate engine | matches workbook to 0.01 |
| Proposal selection | property | selection predicate | 0 false positives/negatives over 10k |
| CPM scheduling | unit/perf | scheduler | correct dates; 2,000 nodes < 500 ms |
| Posting profile resolution | contract | posting API | every (group, category, property) in fixture resolves |
| Journal → GL | E2E | journals, journal service | balanced entries; event emitted |
| EVM | unit | report service | CPI/SPI per formulas |
| Proposal build concurrency | integration | two parallel builds | disjoint selections |

Reconciliation: Σ transactions cost by project = project cost subledger = GL cost/WIP accounts; Σ POSTED proposal sales = invoiced revenue per wave-29; accrued − deferred revenue = GL accrued/deferred accounts.

### 8.41.10 Deployment and operations

Helm chart `passage-projects` (API, scheduler worker, estimate worker), Postgres cluster `projects` (CloudNativePG, 3 replicas), Atlas migration `projects/0001_initial`. Config: `PROJ_SCHEDULER_MAX_NODES=5000`, `PROJ_PROPOSAL_BATCH=500`. Feature flags: `projects.intercompany-hours`, `projects.funding-limits`. Temporal schedules: nightly `projects-reconcile` 02:00 tenant-local, `committed-cost-refresh` every 60 s. SLOs: journal post p95 < 400 ms for 100 lines; proposal build p95 < 5 s for 5,000 candidates; availability 99.9%. Alerts: reconciliation variance ≠ 0; estimate run failure; outbox lag > 60 s. Runbook: re-run estimate (reverse then run), rebuild committed cost projection from events. Rollback: Argo digest rollback; migrations are additive within the wave.

### 8.41.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| ProjTable, ProjGroup, ProjType | Project, ProjectGroup | Status map: Created→CREATED, InProcess→IN_PROCESS, Finished→FINISHED |
| ProjWBS, ProjPlanVersion | WbsNode, PlanVersion | AX activity numbers → wbs_code |
| ProjEmplTrans, ProjCostTrans, ProjItemTrans, ProjRevenueTrans, ProjOnAccTrans | ProjectTransaction | open (unbilled) only; billed history as read-only archive |
| ProjPosting, ProjCategory, ProjLinePropertySetup | ProjectPostingProfile, ProjectCategory, line property | |
| ProjEstimate, ProjPeriodTable | ProjectEstimate | last posted estimate per project as opening balance |
| PROJ, PRPS, AUFK/AFVC | Project, WbsNode | PRPS.POSID → wbs_code; networks flattened to nodes |
| CATSDB (released, unbilled) | ProjectTransaction HOUR | |
| KKA2 RA results (COSB) | ProjectEstimate opening | RA version 0 only |

Validation: per-project cost to date, billed to date and WIP must equal source within 0.01; count of unbilled chargeable transactions equal. Not migrated: billed history detail (archived in lakehouse, wave 46), ProjForecast models older than current year, SAP settlement rules (CJ88) — replaced by posting profiles.

### 8.41.12 Dependencies, risks, open questions

Depends on: wave 11 posting API, wave 14 periods, wave 17 PO project references, wave 25 timesheets, wave 29 invoicing, wave 37 CIP, wave 38 budget control, wave 40 intercompany. Coverage-closure additions also depend on: wave 27 quotations (FR-41.12), wave-16 ownership dimension `project_id` and wave-22 planning scope PROJECT (FR-41.19), wave-28 shipments (FR-41.19), wave 18 vendor invoices for vendor retention and claims (FR-41.14, FR-41.18), wave 12 rate tables (FR-41.16) and wave-4 workflow for timesheet routing (FR-41.13).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| POC method disputes | Low | High | Workbook sign-off before wave close |
| Proposal performance on large backlogs | Medium | Medium | Partial index + batch 500 |

Open questions: (1) Support POC by effort (hours) in v1? Default no; decide by wave 42. (2) Grant compliance reporting (AX ProjGrant)? Default v1.x; decide by wave 46. (3) Intercompany resource hours at transfer price (wave 40 mirror documents)? Default v1.x; decide by wave 42.

### 8.41.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| Default project group per project type | legal entity | lookup | — | ProjectGroup | Pre-fills group and posting profile on project create | Project management and accounting parameters › General › Default project group (ProjParameters) | OPSA project profile (TCJ41) default values |
| Journal approval required (HOUR / COST / ITEM / FEE) | legal entity | bool per type | false | | Routes journal post through wave-4 workflow before FR-41.4 posting | PM&A parameters › Journals › Approval required | CATS approval profile (CAC2) for hours; n/a for KB21N |
| Timesheet period type | legal entity | enum | WEEK | WEEK, SEMI_MONTH, MONTH | Generates TimesheetPeriod rows (FR-41.13) | PM&A parameters › Timesheet › Period type; Timesheet periods (ProjPeriodTable) | CATS data entry profile period (CAC1) |
| Timesheet workflow by project | legal entity | bool | true | | Line approval routed to project manager (BR-41.9) | PM&A parameters › Timesheet › Enable workflow by project | CATS_APPR_LITE approver determination by WBS |
| Timesheet hour increment | legal entity | numeric | 0.25 | 0.01, 0.10, 0.25, 0.50, 1.00 | Validation on line hours | PM&A parameters › Timesheet › Hour increments | CAC1 "rounding of hours" |
| Timesheet max hours per day | legal entity | numeric | 24.00 | 1.00–24.00 | BR-41.9 `PROJ-TS-003` | PM&A parameters › Timesheet › Max hours per day | CAC1 "max. hours per day" |
| Allow timesheet delegates | tenant | bool | true | | Enables TimesheetDelegate (FR-41.13) | PM&A parameters › Timesheet › Allow delegates (TSTimesheetDelegate) | CATS substitutes (CATS_DELEGATE / HR substitution) |
| Default line property | legal entity | lookup | CHARGEABLE | CHARGEABLE, NON_CHARGEABLE | Pre-fills line property by (project group, category) | Line property setup (ProjLinePropertySetup) | DIP profile ODP1 material determination / billing relevance |
| Proposal selection includes FINISHED projects | legal entity | bool | true | | Stage set in BR-41.3 | PM&A parameters › Invoice › Include finished projects | DP90 selection variant |
| Estimate method default | project group | enum | POC_COST | COMPLETED_CONTRACT, POC_COST, POC_MILESTONE | FR-41.7 default on new projects | Project groups › Estimate › Completion method (ProjGroup) | OKG1 RA method per RA key; OKG2 RA version |
| Estimate cost template | legal entity | lookup | — | ProjectCostTemplate | Which categories count toward cost_to_date and ETC | Cost templates (ProjCostTemplate) | OKG4 line IDs / OKG5 assignment of cost elements |
| Committed cost sources | legal entity | multi-enum | PO, REQUISITION, ITEM_REQUIREMENT | + VENDOR_INVOICE_UNPOSTED | FR-41.8 projection inputs | PM&A parameters › Cost control › Committed cost sources | OPSA "commitment management" indicator; OKKP activate commitments |
| Committed cost refresh fallback interval | tenant | int (s) | 60 | 10–600 | Fallback poll when the event stream lags | "Update committed costs" batch recurrence | CJI5 batch variant |
| Customer retention default term | legal entity | lookup | — | RetentionTerm | FR-41.14 default on project contracts | Project contracts › Retention terms (ProjRetentionTerms) | OBB9 installment terms / S/4 PO retention settings |
| Vendor retention default term | legal entity | lookup | — | RetentionTerm | Applied to project POs (wave 17) | Vendor retention terms (ProjVendRetentionTerms) | S/4 MM retention (ME21N Retention tab) |
| Indirect cost component group | legal entity / project group | lookup | — | IndirectCostComponentGroup | FR-41.15 / BR-41.11 | PM&A parameters › Indirect costs › Default component group | KZS2 costing sheet in OPSA project profile |
| Interest profile | project group / project | lookup | — | InterestProfile | FR-41.16 | n/a | OPIB/OPIC/OPID interest profile in project profile |
| Interest day count | interest profile | enum | ACT_365 | ACT_365, ACT_360 | BR-41.12 | n/a | OPIC interest indicator calendar type |
| Scheduling with resource calendars | legal entity | bool | true | | FR-41.2 calendar enforcement | PM&A parameters › Scheduling › Scheduling with resources | OPUU network profile scheduling parameters |
| Max simulation versions per project | tenant | int | 20 | 1–100 | BR-41.14 | n/a | OPTS simulation profile |
| Change order customer approval required | legal entity | bool | true | | BR-41.13 for CLAIM_CUSTOMER | n/a | TQ80 claim type (notification type CL) settings |
| Project stock valuation | project group | enum | VALUATED | VALUATED, NON_VALUATED, NONE | FR-41.19 | Item requirements consumption setting (ProjParameters › Item requirements) | OPS project stock (TCJ41 valuated project stock) |
| Funding limit hold | legal entity | bool | true | | BR-41.3 `FUND-LIMIT` | Project contracts › Funding limits | n/a (GM budget) |
| Number sequences | legal entity | lookup | PROJ, PROJ-TS, PROJ-PROP, PROJ-EST, PROJ-CO | wave-5 sequences | Identifiers | PM&A parameters › Number sequences | OPSJ project number ranges; OPSK project coding mask |

### 8.41.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Projects list | list inquiry | stage, type, group, customer, project manager, legal entity | id, name, stage, budget, actual, committed, remaining, CPI, SPI | `proj_summary` | Projects list page (ProjTable) | CN41N structure overview |
| Project statement | drill-down | project (set), date range, cost/revenue/hours/WIP tabs | cost by category, revenue invoiced/accrued, hours, WIP, gross margin | `proj_statement` | Project statements (ProjStatement); SSRS Project statements | S_ALR_87013531 / 87013532 / 87013533 cost-revenue-commitment; CJI3 line items |
| Posted transactions | list inquiry | project, WBS, type, category, date, worker, source document | transaction fields, journal id, proposal id, estimate id | `proj_txn` | Posted transactions (ProjTransPosted), Hour/Cost/Item/Revenue transactions forms | CJI3 actual line items; CJI5 commitment line items |
| Invoice control | drill-down | project, customer, period | proposed, invoiced, retainer applied, retention held, open to bill | `proj_billing` | Invoice control (ProjInvoiceControl); Project invoice journals (ProjInvoiceJour) | DP90 billing request list; VF05 billing documents by WBS |
| WIP and estimates | drill-down | project group, period, method | cost to date, ETC, EAC, POC, revenue to date, accrued/deferred, loss provision | `proj_estimate` | Estimates (ProjEstimate); SSRS Project WIP | KKA2 / KKAJ results analysis; S_ALR_87013537 WIP |
| Project cash flow | operational report | project, horizon, forecast model | expected receipts (proposals, retention release), payments (POs, timesheets), net by period | `proj_cashflow` | Cash flow (ProjCashFlow); SSRS Project cash flow | S_ALR_87013567 project cash flow |
| Budget vs actual vs committed | drill-down | project, WBS, period, dimension | budget, actual, committed, remaining, % consumed | `proj_budget` with wave-38 | Project budget balances; Committed costs (ProjCommittedCost) | S_ALR_87013558 budget/actual/commitment; CJ31 budget display |
| EVM | KPI | project, as-of date, WBS level | PV, EV, AC, CPI, SPI, EAC, VAC | `proj_evm` | none native (BI cube) | CNE5 progress analysis; CNE1 progress determination |
| Missing timesheets | list inquiry | period, legal entity, department | worker, period, status, days missing, manager | `ts_missing` | Missing timesheets (TSMissingTimesheets); SSRS Timesheet report | CATC time leveling; CADO display working times |
| Timesheet hours | list inquiry | worker, project, period, line status | hours by day, line property, approver, posted | `ts_hours` | Timesheet hours inquiry (TSTimesheetTable) | CATS_DA display CATS data |
| Utilization | KPI | role, worker, department, period | billable hours, non-billable, available, utilization % | `proj_utilization` | Hour utilization (ProjUtilization); SSRS Utilization | CMP9 workforce planning evaluation |
| Retention ledger | list inquiry | project, party, role, open only | retained, released, open, next release trigger | `proj_retention` | Retention transactions (ProjRetentionTrans) | FBL5N / FBL1N with retention special G/L |
| Change order log | list inquiry | project, type, status | change order, type, cost impact, revenue impact, applied baseline | `proj_change_order` | n/a | CLM10 claim list |
| Forecast vs actual | drill-down | project, model, period | forecast qty/cost/sales, actual, variance | `proj_forecast` | Project forecasts (ProjForecastEmpl/Cost/Item) | CJ40/CJ42 cost planning vs actual (S_ALR_87013543) |
| Risks and issues | list inquiry | project, kind, status, owner | probability, impact, score, owner, due | `proj_risk_issue` | n/a | CPM risk/issue list |

### 8.41.15 Printed and electronic documents

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Project invoice | invoice proposal POSTED → wave-29 invoice | header: customer, project, contract, period, billing rule; lines: transactions (detail or summary per rule), retainer applied, retention held; footer: tax by code, totals, payment terms, bank details | PDF, e-invoice (wave-29 country formats), email | wave-29 `INV` | Fiscal invoice; retention per wave-29 country rule (US 7 years, DE 10 years) |
| Invoice proposal (pre-bill review) | proposal DRAFT/APPROVED | same as invoice plus "why not billed" exclusions list | PDF, XLSX, email | `PROJ-PROP` | none (internal) |
| Retention release invoice | release proposal POSTED | header: project, original invoices; lines: RETENTION_RELEASE per source invoice | PDF, e-invoice, email | wave-29 `INV` | fiscal invoice |
| Project quotation | wave-27 quotation SENT | header: customer, validity; lines: estimate lines by type, totals, margin hidden | PDF, email | wave-27 `QUO` | none |
| Timesheet | print from sheet or approval | header: worker, period, status; lines: project/WBS/category × days; footer: totals, approvals with timestamps | PDF | `PROJ-TS` | labor record: FLSA 3 years (US), ArbZG §16 2 years (DE) |
| Project statement | on demand | cost/revenue/hours/WIP sections per filter | PDF, XLSX | none | none |
| Change order (customer-facing) | change order SUBMITTED | header: project, contract, change order no; lines: scope, cost/revenue impact, schedule impact; footer: signature blocks | PDF, email, e-signature link (wave 35 hub) | `PROJ-CO` | contract amendment; keep with contract (10 years DE HGB §257 for commercial letters) |
| Estimate voucher | estimate POSTED | wave-11 journal voucher with estimate id, POC, revenue, WIP | PDF | wave-11 voucher | accounting record |
| Project purchase order | wave-17 PO with project reference | wave-17 PO layout plus project/WBS and vendor retention clause | PDF, EDI, email | wave-17 `PO` | per wave 17 |

### 8.41.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| `projects-reconcile` | 02:00 tenant-local daily | tenant, legal entity | run id per (tenant, date); re-run replaces results | < 10 min for 2,000 projects | retry 3×; variance report written on failure | variance ≠ 0.00 → page project accountant; run missing by 03:00 |
| `committed-cost-refresh` | event-driven; fallback every 60 s | tenant, project | Kafka consumer offset; projection rebuild from events is idempotent | < 10 s lag | consumer restart; rebuild on checksum mismatch | lag > 60 s |
| `projects-estimate-run` | on demand at period close (Temporal workflow per project) | project, period | key (project, period); reverse-and-rerun in one workflow | < 10 min for 2,000 projects | workflow retry per project; failed projects listed, others continue | any project failed; run > 30 min |
| `timesheet-periodic-create` | period start 00:00 tenant-local (Monday for WEEK) | legal entity, worker | key (worker, period) | < 2 min for 10,000 workers | per-worker retry | created count < active workers − exemptions |
| `timesheet-missing-reminder` | day after period end 09:00; repeat daily until submitted | legal entity | idempotent per (worker, period, day) | < 1 min | notification retry via wave 35 | reminders undelivered |
| `projects-interest-run` | monthly after period close | project | key (project, period) | < 5 min for 2,000 projects | per-project retry | missing interest for projects with a profile |
| `retention-release-check` | nightly 03:00 | project | evaluates triggers; creates release proposals once per trigger | < 2 min | retry | proposals not created for FINISHED projects with open retention |
| `projects-schedule-recalc` | nightly 01:00 for projects whose calendars changed; on demand | project | pure recompute | < 500 ms per 2,000 nodes | retry | runtime > 5 s per project |
| `projects-forecast-rollover` | first day of fiscal period | project, model | key (model, period) | < 1 min | retry | none rolled |

### 8.41.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles that include it |
|---|---|---|---|
| ProjMaintain | POST/PUT `/api/v1/projects`, change orders create, quotation convert; screens Project detail, Change orders | with ProjChangeOrderApprove on the same change order; with ProjSetupAdmin | Project manager, Project administrator |
| ProjPlan | WBS replace, schedule, plan version transfer, forecasts, risks; screens WBS Gantt, Resource board, Plan version compare | none | Project manager, Resource manager |
| ProjJournalMaintain | journals create/edit; screens Journals | with ProjJournalPost | Project accountant, Project coordinator |
| ProjJournalPost | journals post, timesheet post | with ProjJournalMaintain; with ProjTimesheetEnter on own sheet | Project accountant |
| ProjBillingMaintain | proposals build/edit, retention release build; screens Invoice proposals, Retention ledger | with ProjBillingPost | Project accountant |
| ProjBillingPost | proposals post, release post | with ProjBillingMaintain; with ProjMaintain (contract value) | Project controller |
| ProjEstimatePost | estimates run/reverse/post, interest run | with ProjSetupAdmin (posting profiles) | Project controller |
| ProjTimesheetEnter | timesheets create/submit for self or delegated worker; screen My timesheet | with ProjTimesheetApprove on own sheet | Employee (ESS), Contractor |
| ProjTimesheetApprove | timesheet line decide; screen Timesheet approval | with ProjTimesheetEnter (own) | Project manager, Line manager (MSS) |
| ProjChangeOrderApprove | change order approve/apply | with ProjMaintain on the same change order (creator ≠ approver) | Project controller, Account executive |
| ProjInquire | all GET inquiries and reports | none | Project manager, Project accountant, Executive |
| ProjSetupAdmin | parameters (8.41.13), posting profiles, categories, retention terms, indirect cost components, interest profiles | with ProjEstimatePost, ProjJournalPost | Project administrator |

### 8.41.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| PROJ-POST-001 | error | No posting profile for project group {group}, category {category}, line property {property} | Resolution failed at all fallback levels | Add a profile row or a wildcard fallback | FR-41.5 |
| PROJ-PERIOD-001 | error | Transaction date {date} is in a closed PROJECTS period | Period closed in wave 14 | Change the date or reopen the period | BR-41.6 |
| PROJ-STAGE-001 | error | Project {id} cannot move from {from} to {to} | Transition not allowed | Use an allowed transition | FR-41.1 |
| PROJ-STAGE-002 | error | Project {id} cannot be FINISHED: {reasons} | Open journals, proposals or non-zero WIP | Post or cancel the listed items; post a final estimate | BR-41.7 |
| PROJ-WBS-001 | error | Dependency {a} → {b} creates a cycle | Circular dependency | Remove one dependency | FR-41.2 |
| PROJ-WBS-002 | error | WBS code {code} already exists in plan version {ver} | Duplicate code | Change the code | FR-41.2 |
| PROJ-BOOK-001 | error | Hard booking exceeds 100 % on {date} for {resource} | Over-allocation | Reduce hours or use a soft booking | FR-41.3 |
| PROJ-BOOK-002 | warning | Soft booking exceeds 100 % on {date} for {resource} | Over-allocation | Review on the resource board | FR-41.3 |
| PROJ-PROP-001 | error | Billing rule currency {a} differs from project currency {b} | Mismatch | Pick a rule in the project currency | FR-41.6 |
| PROJ-PROP-002 | info | No billable transactions matched the selection | Empty candidate set | Widen the date range or check line properties with "why not billed" | FR-41.6 |
| FUND-LIMIT | warning | Line held: funding source {source} limit {limit} reached | Funding limit | Raise the limit or add a source | BR-41.3 |
| PROJ-EST-001 | error | A POSTED estimate already exists for {project} in period {period} | Duplicate run | Reverse it first (or use reverse-and-rerun) | FR-41.7 |
| PROJ-EST-002 | error | Projects sub-ledger for period {period} is not closed for transactions | Sequence | Close the sub-ledger period | BR-41.6 |
| PROJ-TS-001 | error | Timesheet period {period} is closed | Period CLOSED | Ask the administrator to reopen | BR-41.9 |
| PROJ-TS-002 | error | Project {id} is not IN_PROCESS | Stage check | Pick another project or change stage | BR-41.9 |
| PROJ-TS-003 | error | Hours on {date} total {hours}, more than {max} | Daily cap | Correct the hours | BR-41.9 |
| PROJ-TS-004 | error | You are not a delegate for {worker} on {date} | Missing or expired delegation | Ask the worker or administrator to add a delegate row | BR-41.9 |
| PROJ-TS-005 | warning | Hours on line {line} are not a multiple of {increment} | Increment parameter | Round the entry | 8.41.13 |
| PROJ-QUO-001 | error | Quotation {id} has already been converted to project {project} | Second conversion | Open the existing project | FR-41.12 |
| PROJ-CO-001 | error | Change order {id} is already APPLIED | Second apply | None required | BR-41.13 |
| PROJ-CO-002 | error | Customer approval reference required for a customer claim | Parameter on | Attach the approval evidence | BR-41.13 |
| PROJ-RET-001 | error | Retention for {invoice} cannot be released before its trigger ({trigger}) | Early release | Wait for the trigger or override with ProjBillingPost and a reason | BR-41.10 |
| PROJ-INT-001 | warning | Project {id} has no interest profile; skipped | No profile | Assign a profile on the project or group | FR-41.16 |
| PROJ-SIM-001 | warning | {n} nodes changed in both WORKING and simulation {ver}; resolve conflicts | Three-way diff conflicts | Resolve node by node | BR-41.14 |
| PROJ-STOCK-001 | error | Insufficient project stock for item {item} on project {project} | Project-owned on-hand short | Transfer to project stock or change ownership | FR-41.19 |

### 8.41.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| Journal post (100 lines) | 2,000 journals/day per tenant; peak 50/min | 150 / 400 / 800 ms | 200 lines/s sustained | ProjectTransaction ≈ 1.2 KB × 5 M rows = 6 GB | partitioned by tenant hash (16); (tenant, project, txn_date) | queue journals; posting API back-pressure; no partial posts |
| Timesheet submit + line approval | 10,000 workers × 1 sheet/week; peak Monday 09:00 2,000/min | 100 / 300 / 600 ms | 100 submits/s | Timesheet + lines ≈ 3 GB | (worker_id, period_id) unique; lines by timesheet | submit accepted, workflow routing lags; UI shows "pending routing" |
| Invoice proposal build (5,000 candidates) | 500 proposals/month | 1 / 5 / 10 s | 20 builds in parallel | small | partial index proposal_id IS NULL AND CHARGEABLE | batch of 500 per transaction; longer builds, same result |
| Estimate run | 2,000 projects per period | per project 50 / 300 / 800 ms; run < 10 min | 10 workflows in parallel | ProjectEstimate ≈ 50 MB | (project_id, period_id) unique | Temporal rate-limits; run completes later |
| CPM scheduler | 2,000 nodes, 3,500 dependencies | 120 / 500 / 900 ms | 5 runs/s | PlanVersion copies ≈ 200 MB | (plan_version_id, parent_id) | reject > 5,000 nodes (`PROJ_SCHEDULER_MAX_NODES`) |
| EVM / statement as-of query | 5,000 queries/day | 80 / 250 / 500 ms | 50 q/s | projections ≈ 1 GB | materialized daily rollup + delta | serve rollup without delta, flagged stale |
| Committed cost projection update | 20,000 PO events/day | 1 / 5 / 10 s lag | 100 events/s | ≈ 300 MB | (project_id, wbs_node_id) | consumer lag alert; projection remains consistent |
| Interest run | 2,000 projects monthly | run < 5 min | — | negligible | — | later completion |
| Change order apply | 200/month | 200 / 600 / 1,200 ms | — | negligible | — | serialized per project |

### 8.41.20 Localization and compliance hooks

- Revenue recognition: POC_COST and POC_MILESTONE map to the input and output methods of ASC 606 / IFRS 15; COMPLETED_CONTRACT remains for tax books under local GAAP (e.g. DE HGB §252 realization principle). The method is a per-project-group parameter (8.41.13) and a ledger-specific override is exposed through the wave-11 multi-ledger posting (statutory vs IFRS ledger).
- Capitalized interest: IAS 23 (mandatory capitalization on qualifying assets) vs ASC 835-20; the interest profile treatment and the stop rule ("placed in service") are per legal entity; wave-48 country packs set the default profile.
- Retention: statutory retainage caps and release deadlines vary — US state prompt-payment acts (5–10 % caps, release within 30–45 days of substantial completion), DE VOB/B §17 Sicherheitseinbehalt (5 %, bank guarantee substitution), FR retenue de garantie (5 %, 1 year); the RetentionTerm model carries `max_pct` and `release_deadline_days` that wave-48 packs populate; extension point `projects.retention.validate.<cc>`.
- Project invoices follow wave-29 e-invoicing per country (DE XRechnung/ZUGFeRD, IT SDI FatturaPA, FR Factur-X, US none); wave 41 only supplies line content and the project reference tags (buyer reference / contract number per EN 16931).
- Timesheet records retention: FLSA 29 CFR 516 (3 years payroll, 2 years time cards) in the US; EU Working Time Directive implementations (DE ArbZG §16: 2 years; FR Code du travail: 1 year for durations); retention class `LABOR_TIME` in the wave-5 retention policy with per-country durations from wave 48.
- Calendars and holidays for scheduling come from wave-8 calendars per site/country; timesheet period boundaries follow the legal entity's calendar week (ISO 8601 Monday start by default, Sunday start where the pack says so, e.g. US).
- Currency rounding: per-line rounding to the minor unit (JPY 0 decimals, CHF cash rounding to 0.05 only on printed totals per wave 29); accounting-currency amounts at the period-end rate for estimates (wave 12 rate types `CLOSING` for WIP, `AVERAGE` for revenue when the pack prescribes it).
- Language: category names, line properties and billing-rule descriptions are translatable through wave-3 metadata registry labels; invoice line texts print in the customer's language (wave 7 party language).
- Privacy: timesheet comments and worker names on external invoice detail are redacted when the customer contract flag `redact_worker_names` is set (GDPR data minimization); worker ids never print.

### 8.41.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX #88 — 12.11.1 Project quotations (SalesQuotationTable type Project, transfer to project) | PARTIAL | ADDED as FR-41.12 | Quotation document stays in wave 27; conversion, estimate lines and link live here |
| AX #89 — 12.11.1 Timesheets list page (TSTimesheetTableListPage) | PARTIAL | ADDED as FR-41.13 | Screen "My timesheet" / "Timesheet approval" in 8.41.6 |
| AX #90 — 12.11.1 Timesheet details (TSTimesheetTable) | PARTIAL | ADDED as FR-41.13 | Lines, comments, submit; BR-41.9 |
| AX #93 — 12.11.1 Project → Retention terms (ProjRetentionTerms) | PARTIAL | ADDED as FR-41.14 | Customer and vendor terms; BR-41.10 |
| AX #94 — 12.11.2 Timesheet inquiries (missing timesheets, timesheet hours) | PARTIAL | ADDED as FR-41.13 | Rows "Missing timesheets", "Timesheet hours" in 8.41.14 |
| AX #95 — 12.11.2 SSRS Timesheet report / Missing timesheets | PARTIAL | ADDED as FR-41.13 | Timesheet document in 8.41.15; missing-timesheets inquiry |
| AX #96 — 12.11.3 Timesheets → Timesheet workflow / Post timesheets / Create periodic timesheets | PARTIAL | ADDED as FR-41.13 | Jobs `timesheet-periodic-create`, `timesheet-missing-reminder`; post endpoint |
| AX #98 — 12.11.3 Project → Retention release (vendor/customer) | PARTIAL | ADDED as FR-41.14 | Job `retention-release-check`; endpoint `/retention/release` |
| AX #99 — 12.11.4 Billing → Billing rule templates / Retention terms | PARTIAL | ADDED as FR-41.14 | Billing rule templates are the wave-41 billing rules (FR-41.6) stored as reusable templates per contract; retention terms setup in 8.41.13 |
| AX #100 — 12.11.4 Timesheets → Timesheet periods / Period types / Timesheet parameters (favorites, delegates, workflow by project) | PARTIAL | ADDED as FR-41.13 | Parameters rows in 8.41.13 |
| AX #101 — 12.11.4 Indirect costs (component groups, components, rules — R3 PSA) | PARTIAL | ADDED as FR-41.15 | BR-41.11; parameter "Indirect cost component group" |
| AX #272 — 12.31.1 Timesheets (Home → Timesheets link) | PARTIAL | ADDED as FR-41.13 | ESS entry "My timesheet" |
| AX #91 — 12.11.1 Beginning balance journals (ProjJournalTable beginning balance) | PARTIAL (wave 49) | DEFERRED to wave 49 | 8.41.11 loads opening WIP/estimates through the migration pack; a user-facing beginning-balance journal type is delivered with the wave-49 migration tooling |
| SAP #310 — 13.27 CJZ1/CJZ2/CJZ3/CJZ5 Interest calculation on projects | GAP | ADDED as FR-41.16 | BR-41.12; capitalized interest IAS 23 / ASC 835-20 |
| SAP #311 — 13.27 CJV1–CJV6 Simulation versions | PARTIAL | ADDED as FR-41.17 | BR-41.14 transfer with three-way diff |
| SAP #312 — 13.27 CLM1/CLM2/CLM3/CLM10/CLM11 Claims | PARTIAL | ADDED as FR-41.18 | Change orders and claims; BR-41.13 |
| SAP #313 — 13.27 CNS0 Project deliveries | PARTIAL | ADDED as FR-41.19 | Shipment from project stock via wave 28 |
| SAP #314 — 13.27 Project stock (Q), PS material components, MD51, CN33 | PARTIAL | ADDED as FR-41.19 | Ownership dimension `project_id` in wave 16; planning scope PROJECT in wave 22; CN33 BOM transfer = copy wave-9 BOM lines as item estimate lines |
| SAP #316 — 13.27 Commercial Project Management (risks/issues, financial planning) | PARTIAL | ADDED as FR-41.20 | Forecast models and risk/issue register; CPM multi-version financial plans remain partial (parity map row) |
| SAP #318 — 13.27 CJZ2 Interest calculation run | GAP | ADDED as FR-41.16 | Job `projects-interest-run` |
| SAP #320 — 13.27 OPS project stock settings (valuated/non-valuated), OPUV, MRP for project | PARTIAL | ADDED as FR-41.19 | Parameter "Project stock valuation" |
| SAP #322 — 13.27 Claim management customizing (TQ80) | PARTIAL | ADDED as FR-41.18 | Parameter "Change order customer approval required"; co_type enum replaces claim types |
| SAP #323 — 13.27 Simulation / project version profiles (OPTS/OPT) | PARTIAL | ADDED as FR-41.17 | Parameter "Max simulation versions per project" |
| SAP #324 — 13.27 Interest profiles (OPIB/OPIC/OPID) | GAP | ADDED as FR-41.16 | InterestProfile entity; parameters "Interest profile", "Interest day count" |

## 8.42 Wave 42: Human Resources Core: Workers, Positions, Absence & Talent

### 8.42.1 Scope and business context

HR core replaces AX Human resources (HcmWorker, HcmEmployment, HcmPosition, HcmJob, HcmPositionWorkerAssignment, LeaveRequest/LeavePlan, HcmBenefit) and SAP HCM PA/OM (PA20/PA30 infotypes 0000 Actions, 0001 Org Assignment, 0002 Personal Data, 0008 Basic Pay, 2001 Absences; HRP1000/HRP1001 objects and relationships; PT time types; benefits PA-BN). After this wave, an HR administrator manages workers, employments, positions, jobs and departments with effective dating; employees request absence and enroll in benefits; managers approve and run simple reviews. The wave also establishes the PII vault used by payroll.

Out of scope: full recruitment (ATS), learning management, compensation cycles beyond fixed/variable plans, SuccessFactors-style succession, country-specific HR legal fields beyond the framework.

Coverage closure (8.42.21) narrows those exclusions as follows: an internal course catalog with sessions, registrations and completion-to-skills is in scope (FR-42.14) while external LMS content players (SCORM/xAPI) are not; a questionnaire engine (FR-42.15), injury and illness register (FR-42.16), loaned items (FR-42.17), skill mapping (FR-42.18), onboarding/offboarding checklists (FR-42.19), HR reminders (FR-42.20), requisitions and offers (FR-42.21), compensation planning cycles with budgets and manager worksheets (FR-42.22) and concurrent/global assignments (FR-42.23) are in scope; career sites, candidate sourcing and succession/talent pools stay out.

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| AX HcmPositionWorkerAssignment edits in the Position form require "Maintain versions" dialogs; as-of reporting needs SQL over ValidFrom/ValidTo and routinely returns two managers for a day because ranges are inclusive-inclusive; SAP PA30 infotype splits (BEGDA/ENDDA) produce the same overlap at delimit time | Date-effective framework with inclusive end dates and no exclusion constraints | `[from, to)` ranges with Postgres exclusion constraints; one `as-of` endpoint; org chart with a date picker | Duplicate/overlapping manager answers 0 (property test over 50,000 dates); as-of query p95 < 50 ms vs 2–5 s SSRS reports |
| Hire in AX takes 25+ clicks across Worker, Employment, Position assignment, Compensation and User relation forms; SAP PA40 hire action walks 8–12 infotype screens and aborts midway on validation, leaving a partial employee | Action not modeled as a unit; each screen commits separately | One `PersonnelAction` with a diff, workflow approval, and atomic apply (future-dated via Temporal); SCIM provisions the user automatically | Clicks 25 → 4; partial-hire records 0; user provisioning 1–2 days manual → < 60 s |
| Leave accruals (LeaveBankTransaction) in AX and SAP time evaluation (PT60, schema TM04) run as batch and silently truncate proration to whole days; carry-over expiry needs custom PCRs | Accrual arithmetic hidden in batch/schema code; no stated formula | Formula stated in BR-42.3 with proration, caps, carry-over buckets and expiry; balance change events to ESS in real time | Accrual disputes −90%; balance visible to employee at T+0 instead of next payroll run |
| Benefits eligibility in HcmBenefit relies on static "Eligibility rules" with a query form that administrators cannot test; SAP PA-BN eligibility groupings need feature trees (PESTA) | Rule engine not simulatable | CEL eligibility expressions with a simulate endpoint over the whole workforce | Eligibility errors at open enrollment 0 on 100-employee fixture; rule authoring 1 day consultant → 10 min admin |
| PII (national ID, bank) is readable by any user with HR tables access; DB log auditing (AX) off by default, SAP PA20 display not logged | PII stored inline with no masking layer | Vaulted `hr_pii` schema, column encryption, masked default, every unmasked read audited with reason | Unaudited PII reads 0; fields exposed to non-HR roles 0 |

### 8.42.2 AX / SAP parity map

| Capability | AX 2012 / D365 | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Worker/person | HcmWorker, DirPerson, HcmPersonPrivateDetails | PA0002 Personal Data, PA0006 Address | `Worker` (links wave-7 party person), `WorkerPII` vault | full |
| Employment | HcmEmployment (ValidFrom/To), HcmEmploymentDetail | PA0000 Actions, PA0001 Org Assignment | `Employment` (effective-dated) | full |
| Positions and assignments | HcmPosition, HcmPositionDetail, HcmPositionWorkerAssignment, HcmPositionHierarchy | HRP1000 (S objects), HRP1001 A/B 008 holder, A 002 reports-to | `Position`, `PositionAssignment`, `PositionHierarchy` | full |
| Jobs | HcmJob, HcmJobDetail, HcmJobType, HcmJobFunction | HRP1000 C (job) | `Job` | full |
| Departments/org | OMOperatingUnit (Department), HcmPositionHierarchyType | HRP1000 O (org unit), PPOME | wave-6 org hierarchy, type DEPARTMENT | full |
| Compensation | HcmCompensationFixedPlan, HcmCompensationVariablePlan, HcmEmploymentFixedCompensation | PA0008 Basic Pay, wage types | `CompensationPlan`, `FixedCompensation` (effective-dated) | partial — no grade/step matrices in v1 |
| Leave and absence | LeavePlan, LeaveBankTransaction, LeaveRequest; D365 HR leave types | PA2001 Absences, PT quota types (PA2006), accrual via time evaluation | `AbsencePlan`, `AbsenceBalance`, `AbsenceRequest` | full |
| Benefits | HcmBenefit, HcmBenefitPlan, HcmBenefitOption, HcmWorkerEnrolledBenefit | PA0167/0168/0169 benefit infotypes, PA-BN plans | `BenefitPlan`, `BenefitEnrollment`, `LifeEvent` | full |
| Skills/certifications | HcmSkill, HcmPersonSkill, HcmPersonCertificate | PD qualifications (Q objects), HRP1001 A 032 | `Skill`, `WorkerSkill`, `Certification` | full |
| Performance | HcmDiscussion, HcmGoal, HcmRatingModel | Performance management (PMP), appraisals | `Review`, `Goal` | partial — basics only |
| Personnel actions | HcmPersonnelAction, HcmPersonnelActionType | PA40 actions, PA0000 | `PersonnelAction` with workflow | full |
| Courses / learning | HRMCourseTable, HRMCourseSession, HRMCourseAttendee, HRMCourseInstructor, HRMCourseLocation/Classroom, agenda templates, course confirmation, "Transfer course completion to competencies" | PSV1/PSV2/PSV3 business events (training & event management), PSVP, LSO_PSV; SF Learning | `Course`, `CourseSession`, `CourseRegistration` (FR-42.14) | partial — no SCORM/xAPI content player |
| Questionnaires | KMQuestionnaire, KMQuestion, KMAnswerGroup, KMQuestionnaireSchedule, KMResultGroup, results analysis (Org admin › Questionnaires; Home › My questionnaires) | Appraisal templates (PHAP_CATALOG), HR surveys; SAP Qualtrics | `Questionnaire`, `QuestionnaireSchedule`, `AnswerSession` (FR-42.15) | full |
| Injury and illness | HRMInjuryIncident, HRMInjuryTreatment, HRMInjuryBodyPart, OSHA 300/300A/301 | EHS Incident Management (EHSM) incident recording | `IncidentReport` (FR-42.16); OSHA layouts via wave 48 | full for register; forms via US pack |
| Loaned items | HRMLoanTable, HRMLoanItem, loan types | PA0040 Objects on loan | `LoanedItem` (FR-42.17) linked to wave-44 assets | full |
| Skill mapping | HcmSkillMapping (skill mapping, skill gap analysis) | PEPM profile matchup, PPPM qualification search | skill search and gap analysis (FR-42.18) | full |
| Worker tasks / onboarding checklists | HcmWorkerTask, checklist templates | SF Onboarding/Offboarding tasks | `ChecklistTemplate`, `WorkerTask` (FR-42.19) | full |
| Reminders | Worker reminders (certificate expiry, probation end) via alerts | PA0019 Monitoring of tasks (date monitoring) | `HrReminderRule` on wave-5 alert rules (FR-42.20) | full |
| Recruitment requisitions and offers | HRMRecruitingTable, HRMApplication, HRMApplicantTable | E-Recruiting (HRRCF_*), SF Recruiting | `Requisition`, `Candidate`, `Offer` (FR-42.21) | partial — no career site |
| Compensation planning cycles | HcmCompensationProcessEvent, HcmCompensationBudget, merit guideline matrix | PECM_* enterprise compensation management (T71*); SF Compensation | `CompensationCycle`, `CompensationWorksheet` (FR-42.22) | full |
| Concurrent / global employment | several HcmEmployment per worker (per legal entity only) | HRUS_D2 / HRPAD concurrent employment, global employment (host/home) | `Employment.assignment_kind` PRIMARY/CONCURRENT/GLOBAL_HOST (FR-42.23) | full |
| COBRA (US) | n/a | HRBEN00COBRA / HRBEN00ICOBRA | qualifying-event record on enrollment end; notices in wave-48 US pack | partial — deferred |

### 8.42.3 Functional requirements

- **FR-42.1 Worker records** — Must. The system shall maintain workers (EMPLOYEE, CONTRACTOR) linked to one wave-7 person party, with personnel number from sequence `WRK`, and PII in a vaulted schema.
  - PII fields (national ID, date of birth, bank details, home address) encrypted at column level (AES-256-GCM, tenant key in Vault); masked in all reads unless duty `HcmPIIRead`.
- **FR-42.2 Effective-dated employment** — Must. The system shall maintain employments per legal entity with validity ranges and derived status (ACTIVE, ON_LEAVE, TERMINATED).
  - Multiple employments allowed across entities; at most one ACTIVE per entity at any instant.
- **FR-42.3 Positions, jobs, departments** — Must. The system shall maintain positions (FTE, job, department, reports-to position, cost dimensions) and jobs (title, type, function) with effective dating.
  - Position FTE default 1.00, range 0.05–2.00 (job-share); position assignments cannot exceed FTE in total unless `allowOverfill`.
- **FR-42.4 Position hierarchy as-of** — Must. The system shall answer manager, chain-of-command and direct reports for any worker as-of any date within p95 < 50 ms.
- **FR-42.5 Personnel actions** — Must. The system shall model hire, rehire, transfer, promotion, compensation change, leave of absence and termination as personnel actions with workflow approval and future-dated effect.
- **FR-42.6 Absence plans and accruals** — Must. The system shall accrue balances per plan (frequency MONTHLY/BIWEEKLY/ANNUAL/PER_HOUR_WORKED), with proration, caps, carry-over limits and expiry, and reconcile balances to approved requests and wave-25 time registrations.
- **FR-42.7 Absence requests** — Must. The system shall let employees request absence in hours or days, validate against balance and blackout periods, route to manager, and publish to time and payroll.
- **FR-42.8 Benefits** — Must. The system shall define plans (MEDICAL, DENTAL, VISION, LIFE, RETIREMENT_401K, HSA, FSA, OTHER) with options, eligibility rules, employee/employer rates, open enrollment windows and life events.
- **FR-42.9 Employee and manager self-service** — Must. The system shall provide ESS (personal data, absence, benefits, pay statements from wave 43) and MSS (approvals, team calendar, team positions).
- **FR-42.10 Skills and certifications** — Should. The system shall maintain skill profiles with rating scales and certifications with expiry, feeding service technician skills (wave 45).
- **FR-42.11 Performance basics** — Should. The system shall support goals, review templates and a review cycle with manager/employee sign-off.
- **FR-42.12 Compliance framework** — Must. The system shall support consent records, retention policies per data class and a country-specific field framework (extension slots per country code).
- **FR-42.13 Identity sync** — Must. The system shall provision/deprovision Keycloak users via SCIM on hire/termination actions.
- **FR-42.14 Courses and learning** — Should. The system shall maintain a course catalog (course types, groups, agenda templates), sessions (dates, location/classroom or virtual link, instructor, capacity, cost), registrations with waitlist, attendance and completion, and transfer completion into skills and certifications.
  - Registration statuses: REQUESTED → CONFIRMED | WAITLISTED → ATTENDED | NO_SHOW | CANCELED; manager approval optional per course.
  - Completion with result PASS writes `WorkerSkill` rows (rating from the course's skill outcomes) and/or a `Certification` whose expiry = completion date + validity months.
  - Course confirmation letters and attendance lists are documents (8.42.15); registering over capacity goes to the waitlist, and a full waitlist raises `HCM-CRS-001`.
- **FR-42.15 Questionnaire engine** — Should. The system shall design questionnaires (question types SINGLE, MULTI, SCALE 1–n, NUMERIC, TEXT, DATE; answer groups; result groups with weights and scoring; media attachments), schedule them to a population (CEL over worker/department/position/course session, or external respondents by wave-35 link), collect answers in ESS "My questionnaires", and analyze results by question, result group and respondent group for reuse by reviews (FR-42.11), course evaluation (FR-42.14) and wave-32 quality audits.
  - Anonymous mode stores no respondent id and refuses breakdowns with fewer than 5 respondents (`HCM-QST-002`).
- **FR-42.16 Injury and illness register** — Should. The system shall record workplace injury and illness incidents (incident type, occurred/reported timestamps, site and location, body parts, severity, treatments, days away and restricted, outcome, witnesses, root cause, corrective action, privacy-case flag, OSHA-recordable flag) and provide OSHA 300/300A/301 data extracts that the wave-48 US pack renders and submits (ITA).
- **FR-42.17 Loaned items** — Could. The system shall track items loaned to workers (loan type, free-text item or wave-44 asset, serial, issued/due/returned dates, condition out/in) with due reminders and an offboarding task that blocks the termination checklist while items are OUT.
- **FR-42.18 Skill mapping and gap analysis** — Should. The system shall search workers (and FR-42.21 candidates) by required skills with minimum ratings, certifications (unexpired) and availability, and compute the gap between a job's or position's skill profile and a worker's profile as a list of (skill, required, actual, delta).
- **FR-42.19 Worker tasks and checklists** — Should. The system shall define checklist templates (ONBOARDING, OFFBOARDING, TRANSFER) with tasks (owner role, due offset from the action's effective date, instructions, evidence required) and instantiate them as worker tasks when a personnel action applies; completion is tracked on the action, in MSS and in ESS.
- **FR-42.20 HR reminders** — Should. The system shall provide reminder rules (certificate expiry, probation end, contract end, visa/permit expiry, loaned item due, review due) on the wave-5 alert rule engine with HR selectors, recipients (worker, manager, HR partner) and lead times (default 60/30/7 days), replacing BR-42.8's fixed certification notification.
- **FR-42.21 Requisitions, candidates and offers** — Could. The system shall manage job requisitions (position, headcount, hiring manager, approval workflow, budget check against the wave-38 position forecast), a candidate pipeline (APPLIED → SCREEN → INTERVIEW → OFFER → HIRED | REJECTED), offer letters generated from templates with compensation and start date, and hire-from-offer that creates the hire personnel action and the onboarding checklist.
- **FR-42.22 Compensation planning cycles** — Should. The system shall run merit and bonus planning cycles with a budget per department, a guideline matrix (performance rating × compa-ratio band → recommended % range), manager worksheets with proposed amounts and justification, roll-up approval through the position hierarchy, and bulk application as compensation-change personnel actions on the cycle effective date.
- **FR-42.23 Concurrent and global assignments** — Should. The system shall allow a worker to hold several concurrent employments (within one legal entity or across entities) with `assignment_kind` PRIMARY, CONCURRENT or GLOBAL_HOST (home entity, assignment type, planned end date), each with its own position, compensation and pay group, while absence balances, PII and the user identity remain per worker.

### 8.42.4 Data model

**Worker**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| personnel_number | text(20) | yes | unique per tenant; sequence WRK |
| party_person_id | uuid | yes | FK wave-7 person |
| worker_type | enum | yes | EMPLOYEE, CONTRACTOR |
| preferred_name, legal_name | text | yes | legal_name from party |
| user_id | uuid | no | Keycloak subject |
| pii_ref | uuid | cond | FK WorkerPII (vault schema) |
| status | enum | computed | from employments |

**Employment**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| worker_id | uuid | yes | FK |
| legal_entity_id | uuid | yes | FK |
| validity | tstzrange | yes | `[start, end)`; exclusion constraint on (worker_id, legal_entity_id) && validity |
| employment_type | enum | yes | FULL_TIME, PART_TIME, TEMP, CONTRACTOR |
| hire_date, seniority_date, termination_date | date | yes/no/no | |
| termination_reason | text(20) | cond | code table |
| pay_group_id | uuid | no | FK payroll (wave 43) |
| work_calendar_id | uuid | yes | FK wave 8 |

**Position**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| position_number | text(20) | yes | unique; sequence POS |
| job_id | uuid | yes | FK Job |
| department_id | uuid | yes | FK org unit type DEPARTMENT |
| legal_entity_id | uuid | yes | |
| fte | numeric(4,2) | yes | 0.05–2.00 |
| validity | tstzrange | yes | position existence |
| reports_to_position_id | uuid | no | effective-dated via PositionHierarchy |
| dimension_set_id | uuid | no | default dims for payroll/projects |
| allow_overfill | bool | yes | default false |

**PositionAssignment**: id, position_id, worker_id, employment_id, validity tstzrange, fte_share numeric(4,2), is_primary bool. Exclusion constraint: (position_id && validity) unless allow_overfill; (worker_id, is_primary = true && validity).

**AbsencePlan**: id, code, unit (HOURS/DAYS), accrual_frequency, accrual_rate numeric(8,4), accrual_day (LAST_DAY/FIRST_DAY/PAYDATE), proration (DAYS_IN_PERIOD/NONE), max_balance, carry_over_max, carry_over_expiry_months, plan_year_start (MM-DD), negative_balance_allowed, min_service_days. **AbsenceBalance**: id, worker_id, plan_id, bucket (CURRENT/CARRY_OVER), period_start, accrued, used, adjusted, expired, balance. **AbsenceRequest**: id, worker_id, plan_id, from, to, quantity, status (DRAFT, SUBMITTED, APPROVED, REJECTED, CANCELED), approver_id, reason.

**BenefitPlan**: id, code, type, provider_party_id, options[] (code, coverage level, employee_rate, employer_rate, rate_basis PER_PAY_PERIOD/MONTHLY), eligibility_rule (CEL expression), enrollment_window, waiting_days, pre_tax bool, payroll_deduction_code. **BenefitEnrollment**: worker_id, plan_id, option_code, validity, dependents[], life_event_id, end_reason (TERMINATION, LIFE_EVENT, PLAN_END), cobra_qualifying_event bool (US; notices via wave 48).

**Course / CourseSession / CourseRegistration**

| Field | Type | Required | Notes |
|---|---|---|---|
| course.id, code, name | uuid, text(20), text(120) | yes | course_type_id, course_group_id, agenda_template_id |
| course.skill_outcomes[] | jsonb | no | (skill_id, rating) written on completion |
| course.certification_id, validity_months | uuid, int | no | certification issued on PASS |
| course.requires_manager_approval | bool | yes | default false |
| session.id, course_id | uuid | yes | |
| session.starts_at, ends_at | timestamptz | yes | agenda lines for multi-day sessions |
| session.location_id, classroom_id, virtual_url | uuid, uuid, text | cond | one of location or virtual |
| session.instructor_worker_id / instructor_party_id | uuid | no | internal or external instructor |
| session.capacity, waitlist_capacity | int | yes | ≥ 1 |
| session.cost_per_attendee, currency | numeric(12,2), char(3) | no | charged to the attendee's department dimension |
| registration.worker_id, session_id, status | uuid, uuid, enum | yes | REQUESTED, CONFIRMED, WAITLISTED, ATTENDED, NO_SHOW, CANCELED |
| registration.result, score | enum, numeric(5,2) | no | PASS, FAIL |

**Questionnaire**: id, code, type_id, questions[] (id, text, question_type, answer_group_id, weight, mandatory, media_ref), result_groups[] (code, question_ids[], scoring), anonymous bool, status (DRAFT, ACTIVE, RETIRED). **QuestionnaireSchedule**: id, questionnaire_id, population (CEL), opens_at, closes_at, reminder_days[], status (PLANNED, OPEN, CLOSED). **AnswerSession**: id, schedule_id, respondent_worker_id (NULL when anonymous), answers[] (question_id, value), started_at, completed_at, score_by_result_group jsonb.

**IncidentReport**: id (sequence HCM-INC), worker_id, legal_entity_id, site_id, incident_type_id, occurred_at, reported_at, location_text, body_parts[], severity_id, treatment_ids[], days_away, days_restricted, outcome_id, privacy_case bool, description, witnesses[], root_cause, corrective_action, osha_recordable bool, asset_id (wave 44, optional), status (REPORTED, UNDER_REVIEW, CLOSED). **LoanedItem**: id, worker_id, loan_type_id, item_description, asset_id, serial, issued_on, due_on, returned_on, condition_out, condition_in, status (OUT, RETURNED, LOST). **ChecklistTemplate**: id, kind (ONBOARDING, OFFBOARDING, TRANSFER), tasks[] (code, title, owner_role, due_offset_days, instructions, evidence_required). **WorkerTask**: id, worker_id, action_id, template_task_code, owner_worker_id, due_on, status (OPEN, DONE, SKIPPED), evidence_ref. **HrReminderRule**: id, selector (CERT_EXPIRY, PROBATION_END, CONTRACT_END, VISA_EXPIRY, LOAN_DUE, REVIEW_DUE), lead_days[], recipients[], alert_rule_id (wave 5). **Requisition**: id (sequence HCM-REQ), position_id, headcount, hiring_manager_worker_id, status (DRAFT, APPROVED, OPEN, FILLED, CANCELED), budget_check_ref. **Candidate**: id, party_person_id, requisition_id, stage, source, skills[]. **Offer**: id, candidate_id, compensation jsonb, start_date, template_id, status (DRAFT, SENT, ACCEPTED, DECLINED, WITHDRAWN), document_ref. **CompensationCycle**: id, name, effective_date, kind (MERIT, BONUS, BOTH), budget_lines[] (department_id, budget_amount, currency), guideline_matrix jsonb (rating × compa_ratio_band → pct_min, pct_max), status (PLANNING, MANAGER_INPUT, APPROVAL, APPLIED, CANCELED). **CompensationWorksheet**: cycle_id, manager_worker_id, rows[] (worker_id, current_pay, rating, compa_ratio, guideline_pct_min, guideline_pct_max, proposed_pct, proposed_amount, justification), status (DRAFT, SUBMITTED, APPROVED). **Employment** adds: assignment_kind (PRIMARY, CONCURRENT, GLOBAL_HOST; default PRIMARY), home_legal_entity_id, planned_end_date, global_assignment_type.

Invariants: every ACTIVE employment has exactly one primary position assignment or an explicit `unassigned` flag; balances = accrued − used + adjusted − expired per bucket; hierarchy is acyclic at every instant (checked on write with a recursive CTE as-of the new validity range). Indexing: GiST on validity ranges; `PositionHierarchy (tenant_id, child_position_id, validity)`; PII schema `hr_pii` with separate role and no RLS bypass.

### 8.42.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | `/api/v1/hr/workers` | Create worker (+ party) | key | HcmWorkerMaintain |
| POST | `/api/v1/hr/actions` | Submit personnel action (hire/transfer/terminate…) | key | HcmActionMaintain |
| GET | `/api/v1/hr/workers/{id}/as-of?date=` | Employment, position, manager as-of | n/a | HcmWorkerInquire |
| GET | `/api/v1/hr/positions/{id}/reports?asOf=&depth=` | Direct/indirect reports | n/a | HcmOrgInquire |
| POST | `/api/v1/hr/absence/requests` | Create request | key | EssAbsence |
| POST | `/api/v1/hr/absence/requests/{id}/decide` | Approve/reject | key | MssApprove |
| POST | `/api/v1/hr/absence/accrue` | Run accrual for plan/period | key per (plan, period) | HcmAbsenceAdmin |
| POST | `/api/v1/hr/benefits/enrollments` | Enroll (open enrollment or life event) | key | EssBenefits |
| GET | `/api/v1/hr/workers/{id}/pii` | Unmasked PII (audited) | n/a | HcmPIIRead |
| POST | `/api/v1/hr/courses/sessions/{id}/registrations` | Register worker (self or manager) | key per (worker, session) | EssLearning / HcmLearningAdmin |
| POST | `/api/v1/hr/courses/sessions/{id}/complete` | Record attendance and results; transfer to skills | key | HcmLearningAdmin |
| POST | `/api/v1/hr/questionnaires/schedules` | Schedule questionnaire to a population | key | HcmQuestionnaireAdmin |
| POST | `/api/v1/hr/questionnaires/answer-sessions/{id}/submit` | Submit answers | key | EssBase |
| GET | `/api/v1/hr/questionnaires/schedules/{id}/results` | Results analysis | n/a | HcmQuestionnaireAdmin |
| POST | `/api/v1/hr/incidents` | Report injury or illness incident | key | HcmIncidentReport |
| GET | `/api/v1/hr/incidents/osha-extract?year=&siteId=` | OSHA 300/300A/301 data extract | n/a | HcmIncidentAdmin |
| POST | `/api/v1/hr/loaned-items` | Issue loaned item | key | HcmWorkerMaintain |
| POST | `/api/v1/hr/loaned-items/{id}/return` | Return item | key | HcmWorkerMaintain |
| GET | `/api/v1/hr/skills/search?skills=&minRating=&certs=&availableFrom=` | Skill mapping search | n/a | HcmOrgInquire |
| GET | `/api/v1/hr/workers/{id}/skill-gap?positionId=` | Gap analysis | n/a | HcmOrgInquire |
| POST | `/api/v1/hr/worker-tasks/{id}/complete` | Complete checklist task | key | MssBase / EssBase |
| POST | `/api/v1/hr/requisitions` | Create requisition | key | HcmRecruitMaintain |
| POST | `/api/v1/hr/offers/{id}/hire` | Hire from accepted offer (creates hire action) | key | HcmActionMaintain |
| PUT | `/api/v1/hr/compensation/cycles/{id}/worksheets/{managerId}` | Save manager worksheet | ETag | MssBase |
| POST | `/api/v1/hr/compensation/cycles/{id}/apply` | Apply cycle as compensation-change actions | key | HcmCompAdmin |

Validation: action effective date may be future-dated up to 365 days; termination date ≥ hire date; absence request quantity ≤ balance + negative allowance; benefit enrollment only inside window or within 30 days of a life event.

| Event | Trigger | Payload | Consumers |
|---|---|---|---|
| `dev.passage.hr.employment.changed.v1` | action applied | see 8.0 | payroll, projects, service, SCIM |
| `dev.passage.hr.position.assignment.changed.v1` | assignment start/end | workerId, positionId, validity, managerPositionId | authorization (manager roles), workflow routing |
| `dev.passage.hr.absence.approved.v1` | request approved | workerId, planId, from, to, hours | time, projects, payroll |
| `dev.passage.hr.benefit.enrollment.changed.v1` | enrollment start/end | workerId, planId, option, validity, employeeRate | payroll deductions |
| `dev.passage.hr.course.completed.v1` | registration ATTENDED with PASS | workerId, courseId, sessionId, skills[], certificationId | wave-45 dispatch skills, notifications |
| `dev.passage.hr.incident.reported.v1` | incident created | incidentId, siteId, severity, oshaRecordable, assetId | EHS notifications, wave-44 asset history |
| `dev.passage.hr.compensation.cycle.applied.v1` | cycle APPLIED | cycleId, effectiveDate, actionIds[] | payroll (wave 43), budget (wave 38) |

### 8.42.6 User experience

| Screen | Primary actions | Key fields/columns | Duty |
|---|---|---|---|
| HR workspace | tiles: pending actions, expiring certifications, open enrollment | | HcmWorkerInquire |
| Worker detail | tabs: employment, positions, compensation, absence, benefits, skills, documents | masked PII with reveal (audited) | HcmWorkerMaintain |
| Org chart | navigate as-of date, drag worker to position (creates action) | position, incumbent, FTE, vacancies | HcmOrgInquire |
| Personnel actions | create, approve, apply | type, effective date, changes diff | HcmActionMaintain |
| Absence admin | run accrual, adjust, view balances | plan, bucket, accrued/used/balance | HcmAbsenceAdmin |
| ESS portal | request absence, view balances, update address, enroll benefits, view pay statements | | EssBase |
| MSS portal | approve requests, team calendar, team positions | | MssBase |
| Benefits enrollment wizard | select options, add dependents, confirm | per-pay-period cost preview | EssBenefits |
| Course catalog and sessions | browse, register, waitlist, print confirmation; admin: record attendance/results | course, session dates, location, instructor, seats left, cost | EssLearning / HcmLearningAdmin |
| Questionnaire designer and results | design questions, schedule, view statistics | question, type, answer group, result group, response rate | HcmQuestionnaireAdmin |
| My questionnaires (ESS) | answer, resume, submit | open questionnaires, due date | EssBase |
| Incident register | report, review, close; OSHA extract | incident, site, severity, days away/restricted, recordable | HcmIncidentReport / HcmIncidentAdmin |
| Loaned items | issue, return, mark lost | worker, item/asset, due, condition | HcmWorkerMaintain |
| Skill search and gap | search by skills/certs/availability; compare to position profile | worker, skill, rating, required, delta | HcmOrgInquire |
| Checklist tasks (MSS / ESS) | complete task, attach evidence, skip with reason | task, owner, due, status | MssBase / EssBase |
| Requisitions and candidates | create requisition, move candidate stage, generate offer, hire | requisition, position, stage, offer status | HcmRecruitMaintain |
| Compensation worksheet (MSS) | propose %, justify, submit; cycle admin: budgets, guidelines, apply | worker, current pay, rating, compa-ratio, guideline, proposed, budget used | MssBase / HcmCompAdmin |

Rules: as-of date picker persists per session in Org chart; PII reveal requires reason text (≥ 10 chars) and logs to audit; all ESS screens pass axe WCAG 2.1 AA and work at 360 px width.

### 8.42.7 Business rules and edge cases

- **BR-42.1 Effective-dated position logic** — assignments, hierarchy edges and position attributes are stored as `[valid_from, valid_to)` ranges; a change on date D closes the current row at D and opens a new row from D; as-of queries use `validity @> asOf::timestamptz`. Manager of worker W as-of D = incumbent (primary assignment) of `reports_to_position` of W's primary position as-of D; if that position is vacant as-of D, walk up until an incumbent is found, max 10 levels, else NULL (AX "vacant position" behavior).
- **BR-42.2 Future-dated actions** — applied by a Temporal timer at 00:00 tenant-local on the effective date; the action can be withdrawn until then; applied rows are immutable (corrections create new actions).
- **BR-42.3 Accrual formula** — per period: `accrual = rate × proration_factor`, proration_factor = days_employed_in_period ÷ days_in_period when proration = DAYS_IN_PERIOD, rounded half-up to 2 decimals; balance capped at max_balance (excess discarded, logged). Example plan `US-PTO-15` (DAYS, MONTHLY, rate 1.25 on LAST_DAY, max 22.50, carry_over_max 5.00, expiry 3 months, plan year 01-01): hire 2026-03-16 → March accrual 1.25 × 16/31 = 0.65; April–December 9 × 1.25 = 11.25; total accrued 11.90; used 4.00; balance 7.90 at 2026-12-31. Year-end: carry-over bucket = min(7.90, 5.00) = 5.00; 2.90 expired. 2027-01-31: CURRENT accrues 1.25, total 6.25; consumption takes CARRY_OVER first; unused carry-over expires 2027-03-31 23:59:59.
- **BR-42.4 Negative balances** — allowed only if plan.negative_balance_allowed, down to −(rate × 2); termination with negative balance raises a payroll deduction suggestion.
- **BR-42.5 Overlapping absence** — a request overlapping an APPROVED request for the same worker is rejected; overlapping a SUBMITTED one warns.
- **BR-42.6 Benefit eligibility** — evaluated as-of enrollment effective date with CEL expression over {employment_type, fte, service_days, job.type, legal_entity, age}; waiting_days counted from hire_date; a terminated employment ends enrollments at the end of the month of termination (COBRA handling is v1.x).
- **BR-42.7 Hierarchy cycles** — rejected on write (`HCM-ORG-001`).
- **BR-42.8 Certifications** — expiry generates a notification at 60/30/7 days; expired certification removes skill match from wave-45 dispatch.
- **BR-42.9 Course registration and completion** — seats are allocated in registration order; when confirmed = capacity, new registrations are WAITLISTED up to waitlist_capacity, else `HCM-CRS-001`; a cancellation promotes the oldest waitlisted registration; ATTENDED with PASS writes skill outcomes (rating = max(existing, course rating)) and a certification valid from the session end date for validity_months; session cost per attendee posts to the attendee's department dimension through the wave-11 posting API when the parameter "Post course costs" is on.
- **BR-42.10 Questionnaire scoring and anonymity** — result group score = Σ(answer value × question weight) ÷ Σ weights, rounded to 2 decimals; a schedule closes at closes_at and late submissions are rejected (`HCM-QST-001`); anonymous schedules store no respondent id and the population list is kept only as a count; any breakdown with fewer than 5 respondents returns `HCM-QST-002`.
- **BR-42.11 Incident recordability** — osha_recordable defaults to true when the incident involves death, days away, restricted work or transfer, medical treatment beyond first aid, or loss of consciousness (29 CFR 1904.7); privacy_case = true hides the worker name in extracts and lists; a CLOSED incident is immutable (corrections are new rows linked by `supersedes_id`).
- **BR-42.12 Checklist instantiation** — when a personnel action applies, tasks are created with due_on = effective date + due_offset_days (negative offsets allowed for pre-boarding) and owner = the worker holding the owner role as-of the effective date (manager via BR-42.1, HR partner from the department, IT from the role mapping); a termination action's OFFBOARDING checklist includes an automatic task "Return loaned items" that stays OPEN while any LoanedItem is OUT.
- **BR-42.13 Compensation cycle arithmetic** — compa_ratio = current annual pay ÷ job pay range midpoint, rounded to 2 decimals; guideline band from the matrix row (rating) and column (compa_ratio band); proposed_pct outside [pct_min, pct_max] requires justification (`HCM-CMP-001` warning); Σ proposed amounts per department ≤ budget_amount, else submit is blocked (`HCM-CMP-002`) unless the cycle flag `allow_over_budget` is on; apply creates one COMPENSATION_CHANGE action per row with effective date = cycle effective date and new pay = current × (1 + proposed_pct), rounded to the minor unit.
- **BR-42.14 Concurrent assignments** — a worker may have at most one PRIMARY employment per legal entity at an instant (existing exclusion constraint) plus any number of CONCURRENT or GLOBAL_HOST employments; each employment carries its own pay group and position assignment; a GLOBAL_HOST employment requires home_legal_entity_id ≠ legal_entity_id and a planned_end_date; absence balances accrue on the PRIMARY employment only unless the plan is marked per-employment.
- **BR-42.15 Requisition and offer** — a requisition requires an approved position with vacancy as-of the planned start date or `allow_overfill`; the budget check calls the wave-38 position forecast and blocks OPEN when the forecast is exceeded (`HCM-REQ-001`); an ACCEPTED offer converts to a hire action once (`HCM-OFR-001` on repeat); DECLINED or WITHDRAWN offers reopen the requisition headcount.

Absence request state machine: DRAFT → SUBMITTED → APPROVED | REJECTED; SUBMITTED/APPROVED → CANCELED (approved cancellation reverses usage if start date is in the future or within 30 days past with manager approval).

### 8.42.8 Acceptance criteria

- **AC-42.1** (FR-42.4, BR-42.1) Given 1,200 workers with 50,000 randomized as-of dates, when manager is queried, then results match the reference model 100%, p95 < 50 ms.
- **AC-42.2** (FR-42.3) Given position POS-100 with FTE 1.00 and an active assignment, when a second overlapping assignment is created, then it is rejected unless allow_overfill.
- **AC-42.3** (FR-42.6, BR-42.3) Given plan US-PTO-15 and hire 2026-03-16, when accruals run through 2027-01-31, then balances equal 0.65, 11.90, 7.90, 5.00 carry-over, 2.90 expired, 6.25 as computed.
- **AC-42.4** (FR-42.7) Given a balance of 2.00 days and a request for 3.00 on a plan that forbids negatives, when submitted, then it is rejected with `HCM-ABS-002`.
- **AC-42.5** (FR-42.8, BR-42.6) Given 100 fixture employees and eligibility rule `employment_type in ['FULL_TIME'] && service_days >= 30`, when evaluated, then eligible set matches the fixture expectation exactly.
- **AC-42.6** (FR-42.5, BR-42.2) Given a transfer action effective 2026-12-01, when the date arrives, then the old assignment closes at 2026-12-01T00:00 tenant-local and the new one opens.
- **AC-42.7** (FR-42.1) Given a user without HcmPIIRead, when reading a worker, then national ID is `***-**-1234` and the audit log records the masked read.
- **AC-42.8** (FR-42.13) Given a hire action applied, when SCIM sync runs, then a Keycloak user exists within 60 s; on termination it is disabled within 60 s.
- **AC-42.9** (FR-42.14, BR-42.9) Given a session with capacity 10 and waitlist 2, when 13 workers register, then 10 are CONFIRMED, 2 WAITLISTED and the 13th receives `HCM-CRS-001`; when one confirmed registration cancels, then the oldest waitlisted becomes CONFIRMED; when attendance is recorded with PASS for a course mapping skill FORKLIFT rating 3 and a 24-month certification, then the worker's skill rating is 3 and the certification expires 24 months after the session end.
- **AC-42.10** (FR-42.15, BR-42.10) Given an anonymous questionnaire scheduled to 12 workers with 4 responses, when results are requested by department, then the overall statistics return and the department breakdown returns `HCM-QST-002`.
- **AC-42.11** (FR-42.16, BR-42.11) Given an incident with 3 days away, when saved, then osha_recordable = true and the OSHA 300 extract for the year lists it with days away 3 and the worker name masked when privacy_case is set.
- **AC-42.12** (FR-42.19, BR-42.12) Given an ONBOARDING template with a task at offset −5 days and a hire action effective 2026-11-02, when the action is approved, then a task due 2026-10-28 is assigned to the manager as-of 2026-11-02.
- **AC-42.13** (FR-42.22, BR-42.13) Given a MERIT cycle with department budget 50,000.00 USD and a worksheet whose proposals sum to 52,000.00, when submitted, then it is blocked with `HCM-CMP-002`; at 49,500.00 it submits, and apply creates one COMPENSATION_CHANGE action per row on the cycle effective date.
- **AC-42.14** (FR-42.23, BR-42.14) Given a worker with a PRIMARY employment in US-01 and a GLOBAL_HOST employment in DE-01 with planned end 2027-06-30, when the as-of query runs for 2027-01-15, then both employments and positions are returned, and payroll (wave 43) receives separate pay-group memberships.
- **AC-42.15** (FR-42.17, BR-42.12) Given a worker with a laptop loaned item OUT, when a termination action applies, then the offboarding task "Return loaned items" stays OPEN until the item is returned, and the checklist cannot complete before that.
- **AC-42.16** (FR-42.18) Given position POS-200 requiring skills WELDING ≥ 3 and PLC ≥ 2, when the gap analysis runs for a worker rated WELDING 4, PLC 1, then the result lists (PLC, required 2, actual 1, delta −1) only.

### 8.42.9 Test plan and fixtures

Fixtures: `hr-workforce-1200` (generated workers, employments, positions across 3 entities with 5 years of actions); `hr-accrual-cases` (36 hire-date/plan combinations with expected balances); `hr-benefit-100` (100 employees, 6 plans, expected eligibility); `hr-pii-contract` (role × field masking matrix).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| As-of manager/position | property | hierarchy queries | 100% match reference, p95 < 50 ms |
| Accrual engine | unit | accrual formula | all 36 cases exact |
| Eligibility CEL | unit | rule evaluator | 100/100 |
| PII masking | security contract | OPA + API | matrix holds; every unmasked read audited |
| Action workflow | E2E | Temporal + API | future-dated transfer applies on date |
| SCIM | integration | Keycloak | user lifecycle within 60 s |

Reconciliation: Σ absence usage = approved request hours = wave-25 absence time registrations; headcount by department as-of = Σ primary assignments.

### 8.42.10 Deployment and operations

Chart `passage-hr` (API, workflow worker, SCIM worker, ESS BFF), Postgres `hr` with schema `hr_pii` under a separate role and Vault-managed key. Flags: `hr.performance`. Schedules: `hr-apply-actions` 00:00 tenant-local, `hr-accrue` per plan frequency, `hr-cert-expiry` daily. SLOs: as-of query p95 < 50 ms; ESS page p95 < 300 ms; availability 99.9%. Alerts: action application failures; PII decrypt errors; SCIM lag > 5 min. Runbook: replay actions from event log; rotate tenant PII key (re-encrypt job). Rollback by digest; `hr_pii` migrations never drop columns.

### 8.42.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| HcmWorker, DirPerson, HcmPersonPrivateDetails | Worker, WorkerPII | PII to vault at load time |
| HcmEmployment, HcmEmploymentDetail | Employment | ValidFrom/ValidTo → validity range |
| HcmPosition, HcmPositionDetail, HcmPositionDuration | Position | |
| HcmPositionWorkerAssignment | PositionAssignment | |
| HcmPositionHierarchy | PositionHierarchy | type Line only |
| HcmJob, HcmJobDetail | Job | |
| LeavePlan, LeaveBankTransaction, LeaveRequest | AbsencePlan, AbsenceBalance (opening), AbsenceRequest (future only) | |
| HcmBenefit*, HcmWorkerEnrolledBenefit | BenefitPlan, BenefitEnrollment | |
| PA0000/0001/0002/0006/0008 | Employment, Position assignment, Worker, PII, FixedCompensation | infotype BEGDA/ENDDA → ranges |
| HRP1000/HRP1001 (O, S, C; A/B 002, 003, 007, 008) | org units (wave 6), Position, Job, hierarchy, assignments | |
| PA2006 quotas, PA2001 | AbsenceBalance opening, historical absences (archive) | |

Validation: active headcount per entity equal; per-worker current position/manager equal; quota balances within 0.01. Not migrated: closed historical actions older than 5 years (archive), AX HRM recruiting history, SAP PD development plans.

### 8.42.12 Dependencies, risks, open questions

Depends on: wave 6 org hierarchy, wave 7 person party, wave 8 calendars, wave 2 Keycloak/OPA, wave 4 workflow, wave 25 time registration.

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Effective-dating bugs | Medium | High | Range types + exclusion constraints + property tests |
| Country HR field explosion | High | Medium | Extension slot framework; first countries via wave 48 |

Open questions: (1) Should contractors live in HR or only in the party model? Default HR with worker_type CONTRACTOR; decide by wave 43. (2) ESS as separate PWA or shell module? Default shell module; decide at wave 42 start.

### 8.42.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| Personnel number sequence | legal entity | lookup | WRK | wave-5 sequences | Worker ids (FR-42.1) | Human resources parameters › Number sequences (HcmSharedParameters) | Feature NUMKR, PA04 number ranges |
| Position / requisition / incident sequences | legal entity | lookup | POS, HCM-REQ, HCM-INC | wave-5 sequences | Identifiers | HR parameters › Number sequences | PA04 / OM number ranges (OONR) |
| Effective-date granularity | tenant | enum | DAY | DAY, TIMESTAMP | Range boundaries at 00:00 tenant-local or exact instant (BR-42.1) | HR shared parameters › Date-effective (ValidFrom/ValidTo) | T77S0 PLOGI/TIMCO time constraints (T777Z) |
| Max future-dating of actions (days) | legal entity | int | 365 | 0–730 | Validation in 8.42.5 | Personnel action parameters | T529A action type rules |
| Personnel action approval required per action type | legal entity | bool per type | true for HIRE, TERMINATE, COMP_CHANGE | | Workflow routing (FR-42.5) | HR parameters › Personnel actions › Enable personnel actions + workflow | T588C / T588D infogroups; dynamic actions T588Z |
| Position overfill allowed by default | legal entity | bool | false | | Default for `allow_overfill` (FR-42.3) | HR parameters › Positions | T77S0 PPVAC vacancy switch |
| Vacant-manager walk depth | tenant | int | 10 | 1–20 | BR-42.1 | n/a | Evaluation path SAP_MANG / chief determination |
| PII masking pattern per field | tenant | text per field | national ID `***-**-####`; bank `****####` | regex template | FR-42.1 | n/a (no masking) | n/a (authorization P_PERNR only) |
| PII reveal reason minimum length | tenant | int | 10 | 0–200 | 8.42.6 rule | n/a | n/a |
| Absence negative balance floor multiplier | absence plan | numeric | 2 | 0–12 | BR-42.4 | Leave plan › Allow negative balance (LeavePlan) | T559L quota deduction beyond entitlement |
| Absence request unit rounding | absence plan | numeric | 0.25 h / 0.5 day | 0.01–1 | Request quantity rounding | Leave and absence parameters › Rounding | T554S absence type counting rules (T556C) |
| Approved-absence cancellation window (days past) | legal entity | int | 30 | 0–365 | Absence state machine | Leave and absence parameters | T554S changes to past absences |
| Benefit life-event window (days) | legal entity | int | 30 | 0–90 | Validation in 8.42.5 | Benefits parameters › Life event window | T5UB* (US benefits) adjustment reasons |
| Course waitlist default size | tenant | int | 5 | 0–100 | BR-42.9 | Course parameters (HRMParameters › Course) | Business event type capacity (PV10 / T77* LSO settings) |
| Post course costs to GL | legal entity | bool | false | | BR-42.9 | Course session cost posting (not native) | PSV activity allocation / internal billing LSO |
| Questionnaire anonymity threshold | tenant | int | 5 | 3–50 | BR-42.10 | Questionnaire parameters (KMParameters) | n/a (Qualtrics) |
| Questionnaire reminder days | schedule | int[] | [3, 1] | 0–30 | Notifications before close | Questionnaire schedules | n/a |
| OSHA establishment id per site | site | text | — | 9-digit/alnum | FR-42.16 extracts | Injury and illness parameters (US) | EHS location master |
| Checklist template per action type | legal entity | lookup | — | ChecklistTemplate | FR-42.19 | Worker tasks / Checklist templates (HcmWorkerTask) | SF Onboarding process configuration |
| HR reminder lead days | rule | int[] | [60, 30, 7] | 1–365 | FR-42.20 | Worker reminders / alert rules | PA0019 task types (T531) reminder dates |
| Compensation guideline rounding | cycle | numeric | 0.1 % | 0.01–1 | BR-42.13 | Compensation parameters (HcmCompensationParameters) | T71ADM* rounding rules (ECM) |
| Allow over-budget compensation submit | cycle | bool | false | | BR-42.13 | Compensation budget control | PECM budget check |
| Concurrent employment enabled | tenant | bool | true | | FR-42.23 | n/a (multiple employments per entity) | T77S0 CCURE MAINC (concurrent employment switch) |
| Requisition budget check | legal entity | enum | BLOCK | NONE, WARN, BLOCK | BR-42.15 | Recruitment parameters | E-Recruiting requisition approval |
| Retention policy per data class | tenant | duration per class | per wave 48 pack | 1–30 years | FR-42.12 | Data retention (not native) | ILM objects HRPA_* / IRF |

### 8.42.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Headcount and FTE as-of | KPI | as-of date, legal entity, department, job, worker type | headcount, FTE, vacancies, open requisitions | `hr_headcount` | Headcount analysis (HcmWorkerHeadcount); SSRS Employee statistics | S_AHR_61016362 headcount; S_PH9_46000223 headcount changes |
| Org chart / reporting lines | drill-down | as-of date, root position, depth | position, incumbent, FTE, manager | `hr_hierarchy` | Organization chart (HcmPositionHierarchy) | PPOSE / PPOME; S_AHR_61016497 staff assignments |
| Personnel actions log | list inquiry | type, status, effective range, department | action, worker, effective date, approver, diff | `hr_actions` | Personnel actions (HcmPersonnelAction) | S_AHR_61016373 / S_AHR_61016380 entries/leavings |
| Absence balances | list inquiry | plan, bucket, as-of, department | accrued, used, adjusted, expired, balance | `hr_absence_bal` | Leave balances (LeaveBankTransaction inquiry) | PT_QTA10 quota overview; PT50 quota overview |
| Absence calendar | operational report | department, period, plan | worker × day grid of absences | `hr_absence_cal` | Leave calendar; SSRS Absence report | PT90 / PT_BAL00 absence data overview; S_AHR_61016369 |
| Benefits enrollment | list inquiry | plan, option, as-of | worker, option, dependents, employee/employer cost | `hr_benefits` | Enrolled benefits (HcmWorkerEnrolledBenefit) | HRBEN0001 participation; HRBEN0005 costs |
| Certifications expiring | list inquiry | days ahead, type, department | worker, certification, expiry | `hr_certs` | Certificates expiring (HcmPersonCertificate) | S_AHR_61015532 qualifications expiring |
| Skill search / gap | drill-down | skills, min rating, position | worker, rating, required, delta | `hr_skills` | Skill mapping / skill gap (HcmSkillMapping) | PEPM profile matchup |
| Course attendance | operational report | course, session, date range | registrations by status, attendance %, pass %, cost | `hr_courses` | Course inquiries; SSRS Attendance list, Course confirmation | S_PH9_46000424 attendance list; S_AHR_61016216 attendance statistics |
| Questionnaire results | drill-down | schedule, result group, respondent group | response rate, score by question and result group, distribution | `hr_qst_results` | Questionnaire results analysis; SSRS Questionnaire statistics | Appraisal results (APPCREATE reports) |
| Injury and illness log | statutory report | year, site, recordable only | OSHA 300 columns (case no, name or "privacy case", job, date, location, description, classification, days away/restricted, injury type) | `hr_incidents` | SSRS OSHA 300/300A/301 | EHS incident reports (EHSM) |
| Loaned items outstanding | list inquiry | loan type, overdue only | worker, item, issued, due, days overdue | `hr_loans` | Loaned items inquiry (HRMLoanItem) | S_AHR_61016399 objects on loan (IT0040) |
| Onboarding progress | KPI | action type, department, period | open tasks, overdue tasks, % complete per action | `hr_tasks` | Worker tasks | SF Onboarding dashboard |
| Compensation cycle status | KPI | cycle, department | budget, proposed, % used, worksheets submitted/approved | `hr_comp_cycle` | Compensation process results | PECM_EVALUATE_CRV / PECM_DISPLAY_BUDGETS |
| PII access audit | operational report | worker, user, period | read time, user, fields, reason | `hr_pii_audit` | Database log (not default) | S_AHR_61016380 n/a; SM20 security audit log |

### 8.42.15 Printed and electronic documents

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Personnel action letter (hire, transfer, promotion, termination) | action APPLIED (template per action type) | header: worker, entity, effective date; body: position, job, compensation, reason; footer: signature blocks | PDF, email, e-signature (wave 35) | action id | employment record; retention per data class (US 7 years after termination common practice; DE 3 years after end of employment per §195 BGB limitation) |
| Offer letter | offer SENT | candidate, position, start date, compensation, conditions, acceptance block | PDF, email, e-signature | `HCM-OFR` | candidate record (EU GDPR: delete 6 months after rejection unless consent) |
| Course confirmation letter | registration CONFIRMED | course, session dates, location/link, agenda, instructor | PDF, email, iCalendar | none | none |
| Course attendance list | session start / end | session, attendee list with signature column, result | PDF | session id | safety training evidence (OSHA 29 CFR 1910 training records per standard) |
| Course certificate | registration ATTENDED with PASS | worker, course, date, validity, issuer | PDF | certificate id | per certification body |
| OSHA 301 incident report data / 300 log / 300A summary | incident saved; year end (300A posting Feb 1–Apr 30) | OSHA form fields | data extract (CSV/JSON) to wave-48 US pack for PDF and ITA CSV | HCM-INC | 29 CFR 1904.33: 5 years after year end |
| Loaned item receipt | item issued / returned | worker, item, serial, condition, due date, signatures | PDF | loan id | until return + 1 year |
| Compensation statement (total rewards) | cycle APPLIED | current/new pay, bonus, benefits employer cost | PDF, ESS | none | employment record |
| Questionnaire | schedule OPEN (external respondents) | questions and answer controls | web form, PDF | none | per questionnaire data class |

### 8.42.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| `hr-apply-actions` | 00:00 tenant-local daily | tenant, legal entity | action id; applied actions skipped | < 2 min for 1,000 actions | per-action retry 3×; failures listed in HR workspace | any failed action; run not complete by 00:30 |
| `hr-accrue` | per plan frequency (month end, pay date) | plan, period | key (plan, period, worker) | < 5 min for 10,000 workers | per-worker retry; partial results kept | accrual missing for active workers |
| `hr-carryover-expiry` | plan-year start and carry-over expiry date | plan | key (plan, plan year) | < 5 min | retry | balances not rolled |
| `hr-cert-expiry` / `hr-reminders` | daily 06:00 | tenant | key (rule, subject, lead day) | < 1 min | wave-5 alert retry | undelivered reminders |
| `hr-questionnaire-distribute` | schedule opens_at; reminders per reminder_days | schedule | key (schedule, respondent, day) | < 2 min for 5,000 respondents | retry | distribution count ≠ population |
| `hr-course-completion-transfer` | on attendance save; nightly sweep 02:00 | session | key (registration) | < 1 min | retry | ATTENDED+PASS without skill rows |
| `hr-scim-sync` | event-driven; reconcile hourly | tenant | Keycloak subject id | < 60 s per event | retry with backoff; reconcile job fixes drift | SCIM lag > 5 min |
| `hr-retention-purge` | weekly Sunday 03:00 | data class | key (record, policy version) | < 30 min | stop on error; legal holds respected | purge errors; records past retention > 0 |
| `hr-pii-rekey` | on demand (key rotation) | tenant | per-row key version | < 1 h for 100,000 workers | resumable from checkpoint | decrypt errors |

### 8.42.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles that include it |
|---|---|---|---|
| HcmWorkerMaintain | `/hr/workers`, loaned items; Worker detail | with PayrollWorkerMaintain (bank details) for the same user | HR administrator |
| HcmActionMaintain | `/hr/actions`, hire from offer; Personnel actions | with approving own action; with PayrollRunFinalize | HR administrator, HR partner |
| HcmPIIRead | `/hr/workers/{id}/pii`; reveal control | none (audited) | HR administrator (restricted), Payroll administrator |
| HcmOrgInquire / HcmWorkerInquire | as-of, reports, skill search/gap; Org chart | none | HR partner, Manager (own subtree via OPA) |
| HcmAbsenceAdmin | accrue, adjust balances; Absence admin | with EssAbsence approval of own requests | Absence administrator |
| HcmLearningAdmin | course catalog, sessions, attendance, completion | none | Training coordinator |
| HcmQuestionnaireAdmin | questionnaire design, schedule, results | with answering an anonymous schedule's breakdown below threshold (blocked by rule) | HR partner, Quality auditor |
| HcmIncidentReport / HcmIncidentAdmin | report incidents / review, close, OSHA extract | admin cannot close an incident they reported | Supervisor (report), EHS manager (admin) |
| HcmRecruitMaintain | requisitions, candidates, offers | with HcmCompAdmin approving own offer compensation above range | Recruiter |
| HcmCompAdmin | compensation cycles, budgets, guidelines, apply | with MssBase worksheet approval of own pay | Compensation manager |
| EssBase / EssAbsence / EssBenefits / EssLearning | own data, requests, enrollments, registrations, questionnaires, tasks | none | Employee |
| MssBase / MssApprove | team approvals, worksheets, team tasks | approve own request blocked | Manager |
| HcmSetupAdmin | parameters 8.42.13, plans, templates, reminder rules | with HcmAbsenceAdmin adjustments | HR system administrator |

### 8.42.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| HCM-ORG-001 | error | Position {a} cannot report to {b}: creates a cycle on {date} | Hierarchy cycle | Choose another reports-to position | BR-42.7 |
| HCM-EMP-001 | error | Worker {w} already has an ACTIVE PRIMARY employment in {entity} on {date} | Exclusion constraint | Use CONCURRENT or end the current employment | FR-42.2, BR-42.14 |
| HCM-EMP-002 | error | Termination date {t} is before hire date {h} | Date order | Correct the date | 8.42.5 validation |
| HCM-POS-001 | error | Position {p} FTE {fte} would be exceeded | Overfill | Reduce FTE share or enable overfill | FR-42.3 |
| HCM-ACT-001 | error | Effective date is more than {n} days in the future | Future-dating limit | Use an earlier date | 8.42.13 |
| HCM-ACT-002 | info | Action {id} scheduled for {date} | Future-dated action | None | BR-42.2 |
| HCM-ABS-001 | error | Request overlaps an approved absence {id} | Overlap | Change dates | BR-42.5 |
| HCM-ABS-002 | error | Requested {q} exceeds balance {b} | Insufficient balance | Reduce quantity or choose another plan | FR-42.7 |
| HCM-ABS-003 | warning | Request overlaps submitted request {id} | Overlap with pending | Check before approving | BR-42.5 |
| HCM-BEN-001 | error | Enrollment outside the open window or life-event period | Window | Wait for the window or record a life event | 8.42.5 validation |
| HCM-BEN-002 | error | Worker not eligible for plan {plan}: {rule result} | Eligibility rule false | Review the CEL result | BR-42.6 |
| HCM-PII-001 | error | Reason of at least {n} characters is required to reveal PII | Missing reason | Enter a reason | 8.42.6 |
| HCM-CRS-001 | error | Session {s} and its waitlist are full | Capacity | Pick another session | BR-42.9 |
| HCM-CRS-002 | warning | Manager approval required for course {c} | Course setting | Await approval | FR-42.14 |
| HCM-QST-001 | error | Questionnaire closed on {date} | Late submission | Contact the owner | BR-42.10 |
| HCM-QST-002 | error | Breakdown hidden: fewer than {n} respondents | Anonymity threshold | Use a wider group | BR-42.10 |
| HCM-INC-001 | warning | Incident appears OSHA-recordable; confirm classification | BR-42.11 default | Confirm or justify override | BR-42.11 |
| HCM-LOAN-001 | warning | {n} loaned items still out for {worker} | Offboarding block | Record returns or mark lost | BR-42.12 |
| HCM-CMP-001 | warning | Proposed {pct} % is outside guideline {min}–{max} %; justification required | Guideline | Enter justification | BR-42.13 |
| HCM-CMP-002 | error | Proposals {sum} exceed budget {budget} for {department} | Budget | Reduce proposals | BR-42.13 |
| HCM-REQ-001 | error | Requisition exceeds position forecast for {department} | Budget check | Adjust forecast (wave 38) or headcount | BR-42.15 |
| HCM-OFR-001 | error | Offer {id} has already been converted to hire action {a} | Repeat conversion | Open the existing action | BR-42.15 |

### 8.42.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| As-of manager/position query | 50,000/day; peak 500/min | 10 / 50 / 120 ms | 200 q/s | n/a | GiST on validity; PositionHierarchy (tenant, child, validity) | cache by (worker, date) 5 min; never stale across an applied action |
| Personnel action submit/apply | 1,000/day | 100 / 300 / 600 ms | 20/s | ≈ 200 MB | (worker_id, effective_date) | queue apply; ordering per worker preserved |
| Absence request + balance check | 2,000/day; peak 200/min | 60 / 200 / 400 ms | 50/s | ≈ 300 MB | (worker_id, plan_id, period_start) | balance read from projection; request still validated on write |
| Accrual run | 10,000 workers per period | run < 5 min | 50 workers/s | ≈ 120k rows/year | partition AbsenceBalance by plan year | longer run; ESS shows "accrual in progress" |
| ESS page load | 10,000 workers, peak 2,000/min at open enrollment | 120 / 300 / 600 ms | 100 pages/s | n/a | BFF caching per user | shed non-critical tiles; enrollment path preserved |
| Skill search | 500/day | 100 / 400 / 800 ms | 10 q/s | n/a | GIN on WorkerSkill (skill_id, rating) | limit 500 results |
| Questionnaire submit | 5,000 respondents in 48 h | 80 / 250 / 500 ms | 50/s | ≈ 1 GB answers | AnswerSession by schedule | accept and score asynchronously |
| PII reveal (audited) | 200/day | 50 / 150 / 300 ms | 10/s | audit ≈ 50 MB | audit log append-only, monthly partitions | no degradation allowed: reveal fails closed if audit write fails |

### 8.42.20 Localization and compliance hooks

- Country fields: the extension-slot framework (FR-42.12) holds per-country worker fields (US SSN, I-9 status, EEO ethnicity/veteran codes; DE Steuer-ID, Sozialversicherungsnummer, Konfession for Kirchensteuer; UK NI number); wave-48 packs register the slot schemas, validation and masking.
- US statutory: OSHA 300/300A/301 layouts and the ITA electronic submission, EEO-1 Component 1, VETS-4212, ACA 1095-C, I-9 document tracking, FMLA eligibility (12 months / 1,250 hours / 50 employees within 75 miles) and COBRA notices are produced by the wave-48 US pack from wave-42 data (incident register, worker slots, benefit end reasons, absence plans).
- EU: GDPR Art. 9 special-category data (health data in incidents, religion for Kirchensteuer) stored only in `hr_pii` with explicit purpose; works-council (DE BetrVG §87) approval flag on questionnaires and monitoring reminders; candidate data deletion 6 months after rejection by default (retention class `CANDIDATE`).
- Absence: statutory minimum leave (DE BUrlG 20 days for a 5-day week; EU WTD 4 weeks), sick-leave certification rules and public holidays come from wave-8 calendars and country absence plan templates; extension point `hr.absence.validate.<cc>`.
- Language and formats: names follow wave-7 party name formats (family-name-first locales); dates ISO 8601 in APIs, locale formats in UI; documents print in the worker's preferred language.
- Retention: per data class (EMPLOYMENT, PAYROLL_LINK, INCIDENT 5 years after year end, CANDIDATE, TRAINING) in the wave-5 retention policy, durations from the country pack; legal holds suspend purge.
- Compensation: pay transparency (EU Directive 2023/970 pay ranges in offers and reports from 2026-06-07) supported by job pay ranges on `Job` and gender pay gap report hooks in the wave-48 pack.

### 8.42.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX #27 — 12.12.1 Courses (HRMCourseTable, sessions, registrations, instructors, locations) | GAP | ADDED as FR-42.14 | BR-42.9 |
| AX #31 — 12.12.2 Course inquiries | GAP | ADDED as FR-42.14 | "Course attendance" in 8.42.14 |
| AX #35 — 12.12.2 SSRS Course reports (Attendance, Course confirmation letters) | GAP | ADDED as FR-42.14 | Documents in 8.42.15 |
| AX #38 — 12.12.3 Course confirmations / Transfer course completion to competencies | GAP | ADDED as FR-42.14 | Job `hr-course-completion-transfer` |
| AX #40 — 12.12.4 Course types, groups, locations, classrooms, instructors, agenda templates | GAP | ADDED as FR-42.14 | Course entities in 8.42.4 |
| AX #28 — 12.12.1 Questionnaires (design, schedules, answer sessions, results) | GAP | ADDED as FR-42.15 | BR-42.10 |
| AX #32 — 12.12.2 Questionnaire results analysis | GAP | ADDED as FR-42.15 | "Questionnaire results" in 8.42.14 |
| AX #36 — 12.12.2 SSRS Questionnaire reports | GAP | ADDED as FR-42.15 | Results export PDF/XLSX |
| AX #39 — 12.12.3 Distribute questionnaires / Collect results | GAP | ADDED as FR-42.15 | Job `hr-questionnaire-distribute` |
| AX #41 — 12.12.4 Questionnaire types, question types, answer groups, result groups, media types, parameters | GAP | ADDED as FR-42.15 | Parameters in 8.42.13 |
| AX #83 — 12.29.1 Questionnaires (Org admin link) | GAP | ADDED as FR-42.15 | One engine for HR, quality (wave 32) and org admin |
| AX #89 — 12.29.4 Questionnaires setup (link) | GAP | ADDED as FR-42.15 | Same setup |
| AX #93 — 12.31.1 My questionnaires / Answer questionnaire / Results | GAP | ADDED as FR-42.15 | ESS "My questionnaires" |
| AX #96 — 12.31.2 Questionnaire statistics / Results analysis | GAP | ADDED as FR-42.15 | 8.42.14 |
| AX #97 — 12.31.2 SSRS Questionnaire reports (link) | GAP | ADDED as FR-42.15 | 8.42.14 |
| AX #99 — 12.31.3 Schedule questionnaires (batch) | GAP | ADDED as FR-42.15 | 8.42.16 |
| AX #101 — 12.31.4 Questionnaire setup (link) | GAP | ADDED as FR-42.15 | 8.42.13 |
| AX #29 — 12.12.1 Injury and illness (incidents, treatments, body parts, OSHA logs) | GAP | ADDED as FR-42.16 | BR-42.11 |
| AX #33 — 12.12.2 Injury/illness inquiries | GAP | ADDED as FR-42.16 | "Injury and illness log" |
| AX #37 — 12.12.2 SSRS OSHA 300/300A/301 | GAP | ADDED as FR-42.16 | Data extract here; form layouts and ITA submission DEFERRED to wave 48 US pack |
| AX #42 — 12.12.4 Incident types, body parts, treatments, severity levels, outcomes | GAP | ADDED as FR-42.16 | Code tables in wave-3 registry |
| AX #30 — 12.12.1 Loaned items (HRMLoanTable, HRMLoanItem) | GAP | ADDED as FR-42.17 | Linked to wave-44 assets |
| AX #34 — 12.12.2 Loaned items inquiry | GAP | ADDED as FR-42.17 | "Loaned items outstanding" |
| AX #43 — 12.12.4 Loan types | GAP | ADDED as FR-42.17 | Code table |
| AX #104 — 12.12.1 Skill mapping / skill gap analysis (HcmSkillMapping) | PARTIAL | ADDED as FR-42.18 | AC-42.16 |
| AX #106 — 12.12.1 Worker tasks / reminders / checklists (HcmWorkerTask) | PARTIAL | ADDED as FR-42.19 | BR-42.12 |
| AX #113 — 12.12.4 Worker tasks / checklist templates | PARTIAL | ADDED as FR-42.19 | ChecklistTemplate |
| AX #110 — 12.12.3 Worker reminders (certificate expiry, probation end) | PARTIAL | ADDED as FR-42.20 | On wave-5 alert rule engine |
| AX #105 — 12.12.1 Worker compliance (US) I-9, ADA, immigration/visa, union agreements | PARTIAL (wave 48) | DEFERRED to wave 48 | Extension slots and visa-expiry reminder (FR-42.20) exist here; US records in the US pack |
| AX #112 — 12.12.4 I-9 document types, ADA requirements, visa types, union agreements | PARTIAL (wave 48) | DEFERRED to wave 48 | Same |
| AX #109 — 12.12.2 SSRS EEO-1 / VETS | PARTIAL (wave 48) | DEFERRED to wave 48 | Source fields in US extension slots |
| AX #111 — 12.12.3 HR year-end US ACA 1095-C | PARTIAL (wave 48) | DEFERRED to wave 48 | Uses benefit enrollment and hours data |
| AX #118 — 12.13.4 Leave of absence / FMLA tracking (US) | PARTIAL (wave 48) | DEFERRED to wave 48 | Absence plan type FMLA with eligibility CEL is configurable here; entitlement tracking rules in US pack |
| SAP #328 — 13.28 HRBEN00COBRA / HRBEN00ICOBRA COBRA | PARTIAL-42 | DEFERRED to wave 48 | `cobra_qualifying_event` captured on BenefitEnrollment; notices and premium billing in US pack |
| SAP #329 — 13.28 E-Recruiting (HRRCF_*) / SF Recruiting & Onboarding | PARTIAL-42 | ADDED as FR-42.21 | Requisitions, candidates, offers; career site and sourcing OUT-OF-SCOPE (non-goal) |
| SAP #330 — 13.28 PPCP / PPSS succession planning / SF Succession | PARTIAL-42 | OUT-OF-SCOPE talent-suite non-goal for v1 | Skill gap (FR-42.18) and reviews (FR-42.11) cover the data; succession pools via wave-35 connector to a talent suite |
| SAP #331 — 13.28 PECM_* enterprise compensation management | PARTIAL-42 | ADDED as FR-42.22 | BR-42.13; long-term incentives OUT-OF-SCOPE |
| SAP #337 — 13.28 SPRO compensation areas, plans, guidelines, budgets (T71*) | PARTIAL-42 | ADDED as FR-42.22 | Parameters in 8.42.13 |
| SAP #332 — 13.28 PSV1/PSV2/PSV3/PSVP/LSO_PSV training & event management | PARTIAL-42 | ADDED as FR-42.14 | No SCORM/xAPI player |
| SAP #338 — 13.28 SPRO training & event management / LSO settings (T77*) | PARTIAL-42 | ADDED as FR-42.14 | 8.42.13 |
| SAP #333 — 13.28 HRUS_D2 / HRPAD concurrent / global employment | PARTIAL-42 | ADDED as FR-42.23 | BR-42.14 |
| SAP #340 — 13.28 SPRO concurrent / global employment configuration | PARTIAL-42 | ADDED as FR-42.23 | Parameter "Concurrent employment enabled" |
| SAP #334 — 13.28 SF Onboarding / Offboarding | PARTIAL-42 | ADDED as FR-42.19 | BR-42.12 |
| AX #108 — 12.12.1 Human resources → Cases (HR cases) | PARTIAL (wave 45) | DEFERRED to wave 45 | Closed by FR-45.12 generic case management with category type HR; listed in 8.45.21 |

## 8.43 Wave 43: Payroll Engine & Expense Management

### 8.43.1 Scope and business context

This wave delivers a country-pluggable gross-to-net payroll engine replacing AX 2012 Payroll (PayrollPayStatement, PayrollEarningCode, PayrollTaxCode, PayrollBenefit, PayrollPayCycle) and SAP PY (schemas, PCRs, wage types, RT results table), and expense management replacing AX Travel and expense (TrvExpTable, TrvExpTrans, TrvPolicy, TrvPerDiem, TrvCreditCardTrans) and SAP Concur. After the wave, a payroll administrator runs a pay cycle from HR and time data, reviews gross-to-net, posts to the GL and produces a payment file; employees submit expense reports with receipts, policy checks, per diems and card transactions, and get reimbursed through AP.

Out of scope: country packs beyond US and the EU reference (DE assumed, see open question 3 in the master PRD); year-end form filing (structures only); garnishment court-order interfaces; Concur-style travel booking.

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| AX 2012 payroll tax updates arrive as hotfix-delivered PayrollTaxCode data (Ceridian engine) with no diff view; SAP PY tax changes ship as HR support packs (BSI TUBs) that require a Basis transport cycle and regression of schemas/PCRs; a wrong table version is discovered only when net pay looks wrong | Rules embedded in code/transports, not versioned data | Rule sets as signed, versioned JSON packs with diff, publish approval and a frozen binding on each run (PayStatement.rule_version per line) | Tax-table deployment 1–2 weeks transport cycle → < 1 h; mis-versioned runs 0 (binding recorded on every line) |
| Gross-to-net results are opaque: AX pay statement lines do not show basis or bracket; SAP RT (PC_PAYRESULT) shows wage types only decipherable by a payroll consultant | No explain path from result to rule | Each `PayResultLine` stores basis_amount, rule_version and bracket; statement viewer drills to the rule | "Why is my tax X?" tickets resolved by admin in < 2 min vs consultant escalation; 200/200 fixtures explainable |
| Retro pay: AX PayrollRetroactiveEarning only handles earning-rate changes; SAP retro recalculation (`/551`,`/552` wage types) reruns whole periods and produces deltas nobody can reconcile | Recalculation designed around the batch, not around the delta | Retro engine replays original rule-set versions and books explicit RETRO_* delta lines | Retro reconciliation effort 4 h per run → 0; delta fixtures 25/25 |
| Payroll GL posting (AX "Generate ledger journal" → LedgerJournalTrans; SAP PC00_M99_CIPE) is a separate step, often weeks late, and rounds per-worker amounts so the journal differs from statements by cents | Separate batch with its own rounding | Posting in the same workflow as finalize, per-line rounded amounts summed (no re-rounding) through the wave-11 posting API | Statement-to-GL variance 0.00; posting lag days → same run |
| Expense policy in TrvPolicy uses X++ expressions a consultant must change; violations appear only at submit; Concur audit rules are a separate SaaS admin console with no API to test | Policy not data, evaluated late | CEL rule schema (BR-43.5) evaluated at line commit and submit with a simulate-against-history endpoint | Rule change lead time 1–2 weeks → 10 min; employee rework (returned reports) −80%; 500/500 fixture exactness |
| Card transactions (TrvCreditCardTrans) imported by batch and matched by hand; receipts re-keyed | No matching logic, no OCR | Hourly feed import, tolerance matching, OCR draft lines | Manual line keying −90%; auto-match ≥ 95% |

### 8.43.2 AX / SAP parity map

| Capability | AX 2012 / D365 | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Pay cycles and periods | PayrollPayCycle, PayrollPayPeriod | Payroll area, period parameters (T549A/T549Q) | `PayCycle`, `PayPeriod` | full |
| Earnings | PayrollEarningCode, PayrollEarningCodeRate, earning statements | Wage types (T512W), PCR rules | `EarningCode` (rule set versioned) | full |
| Deductions/benefits | PayrollBenefit, PayrollDeductionCode (AX via benefits) | Deduction wage types, PA0014/0015 | `DeductionCode` (pre/post-tax, limits) | full |
| Taxes | PayrollTaxCode, PayrollTaxEngine (Ceridian), PayrollWorkerTaxCode | Tax schemas (US: UTAX via BSI TaxFactory) | `TaxRuleSet` per country/year; US federal/state tables | full for US; EU reference |
| Gross-to-net | PayrollPayStatement, PayrollPayStatementLine | Schema U000/D000, RT results table, CRT | `PayStatement` with `PayResultLine` | full |
| Retro pay | PayrollRetroactiveEarning | Retroactive accounting (recalculation) | `RetroCalculation` | full |
| GL posting | PayrollPayStatement distribution → LedgerJournal | PC00_M99_CIPE posting run | journal via wave-11 posting API | full |
| Pay statement/payment | PayrollPayStatement, direct deposit | Pay slip (PE51), DME (PC00_M99_FPAYM) | `PayStatement` PDF; NACHA/SEPA via wave 36 | full |
| Year-end | W-2 data (PayrollW2) | Tax reporter (PU19) | `YearEndForm` structures W-2, 1099, DE Lohnsteuerbescheinigung | partial — structures, not filing |
| Expense reports | TrvExpTable, TrvExpTrans, TrvCategory | Concur expense reports; PR05 (classic) | `ExpenseReport`, `ExpenseLine` | full |
| Policies and per diems | TrvPolicy, TrvPolicyRule, TrvPerDiem | Concur audit rules, per diem tables (T706*) | `ExpensePolicyRule`, `PerDiemRate` | full |
| Card feeds | TrvCreditCardTrans import | Concur card feeds | `CardTransaction` (CSV/OFX/Visa VCF) | full |
| Cash advances | TrvCashAdvance (request, approve, pay via AP, settle against reports), cash advance parameters | PR02/PR05 advances, advance wage types, settlement on trip posting (PRRW) | `CashAdvance` (FR-43.15) | full |
| Travel requisitions (pre-approval) | TrvRequisitionTable/Line, requisition policies | TP01/TP02/TP03/PR01 travel request, T702 travel plan settings, approval workflow | `TravelRequisition` (FR-43.16) | full — booking stays out |
| Deduction arrears | Arrears recovery processing (PayrollArrears), deduction priority | ARRRS / DDNTK tables, deduction priority T51P6 | arrears ledger per deduction code (FR-43.17) | full |
| Off-cycle runs | Manual pay statements, PayrollPayStatement (off-cycle) | PUOC_10 off-cycle workbench (bonus, correction, manual check, check replacement) | `PayRun.run_type` OFF_CYCLE subtypes (FR-43.18) | full |
| Overpayment claims | Negative net carried as advance (manual) | PC00_M10_CLAIMS claims processing | `PayrollClaim` (FR-43.19) | full |
| Quarterly/annual statutory data | Payroll tax reports (941, 940, state UI) | PC00_M10_* tax reporter, PU19 | `StatutoryWageAggregate` (FR-43.20); filing via wave 48 | partial — filing in US pack |
| Workers' compensation | Workers' compensation report (US) | US workers' comp (IT0001 WC code via feature) | class code on job/position by state (FR-43.21) | full for data; state reports via wave 48 |

### 8.43.3 Functional requirements

- **FR-43.1 Pay groups, cycles, periods** — Must. The system shall define pay groups per legal entity with frequency (WEEKLY, BIWEEKLY, SEMIMONTHLY, MONTHLY), generated pay periods and pay dates, and a cutoff for time/absence.
- **FR-43.2 Versioned rule sets** — Must. The system shall express earnings, deductions, taxes and employer contributions as rule sets versioned by country and effective date; a pay run binds to the rule-set versions active on its period end.
- **FR-43.3 Earnings** — Must. The system shall compute REGULAR, OVERTIME (1.5×, 2.0×), PTO, HOLIDAY, BONUS, COMMISSION and SHIFT_DIFF from salary (FixedCompensation ÷ periods) and wave-25 time registrations.
- **FR-43.4 Deductions and contributions** — Must. The system shall compute pre-tax (401(k), Section 125 medical, HSA/FSA) and post-tax deductions with per-period, annual and percent-of-gross limits, and employer contributions (match with cap).
- **FR-43.5 US tax engine** — Must. The system shall compute federal income tax withholding per Pub 15-T percentage method (automated payroll systems), Social Security (6.2% to the annual wage base), Medicare (1.45% + 0.9% additional over 200,000), FUTA, and state income tax/SUI for the states covered by the year's table pack.
- **FR-43.6 EU reference pack (DE)** — Should. The system shall compute Lohnsteuer (via the BMF programmablauf tables), Solidaritätszuschlag, Kirchensteuer and social insurance contributions (KV/PV/RV/AV with Beitragsbemessungsgrenzen) for a demo employee set.
- **FR-43.7 Gross-to-net and pay statements** — Must. The system shall produce a pay statement per worker per period with earnings, pre-tax, taxes, post-tax, net, employer costs and YTD; statements are immutable after the run is FINALIZED.
- **FR-43.8 Retro pay** — Must. The system shall recalculate prior periods when backdated changes arrive (rate, hours, absence) and post deltas in the current period with an explanatory line.
- **FR-43.9 GL posting and payments** — Must. The system shall post the run to the GL by (earning/deduction/tax code × dimension from position) and generate NACHA/SEPA payment batches through wave 36.
- **FR-43.10 Provider adapter** — Should. The system shall export a provider payroll file (ADP/Gusto-style CSV/JSON) and import results for entities that outsource payroll, posting the same GL structure.
- **FR-43.11 Expense reports** — Must. The system shall let workers create reports with lines (category, date, amount, currency, merchant, project/WBS, attendees, receipt), split lines, itemize hotels and submit for approval.
- **FR-43.12 Policy engine** — Must. The system shall evaluate rule sets at line commit and submit, producing WARN, BLOCK, REQUIRE_JUSTIFICATION or REQUIRE_RECEIPT outcomes, with per-diem computation by location/date.
- **FR-43.13 Card feeds and receipts** — Must. The system shall import card transactions (CSV, OFX, Visa VCF 4.0) into a worker's unassigned pool, match receipts by amount/date/merchant (tolerance ±1% or 2 days), and OCR receipts into draft lines.
- **FR-43.14 Reimbursement** — Must. The system shall post approved reports as wave-18 vendor invoices against the worker's employee-vendor account (company-paid card lines against the card issuer vendor), with tax recovery lines where VAT is recoverable.
- **FR-43.15 Cash advances** — Should. The system shall let workers request cash advances (amount, currency, purpose, optional travel requisition and project), route them for approval, pay them through wave-18/19 as a prepayment to the employee vendor, and settle them against expense reports per BR-43.9 with return or payroll recovery of unused amounts.
  - Statuses: DRAFT → SUBMITTED → APPROVED → PAID → PARTIALLY_SETTLED → SETTLED; SUBMITTED → REJECTED.
  - Aging: unsettled advances older than the parameter limit (default 30 days after trip end) raise reminders and block new advances.
- **FR-43.16 Travel requisitions** — Should. The system shall capture pre-trip requisitions (purpose, destinations, dates, estimated lines by category, project/WBS, attendees) with policy checks, approval workflow, an optional linked cash advance, and comparison of the resulting expense report against the approved estimate.
- **FR-43.17 Deduction arrears recovery** — Should. The system shall, when net pay is insufficient, take deductions in priority order, record untaken amounts in an arrears ledger per deduction code (policy ARREARS, NO_ARREARS or PARTIAL), and recover arrears in later periods within a per-period recovery limit per BR-43.10.
- **FR-43.18 Off-cycle runs** — Must. The system shall run OFF_CYCLE pay runs of subtype BONUS, FINAL_PAY, MANUAL_CHECK, CHECK_REPLACEMENT and REVERSAL for single or selected workers, with supplemental-wage tax methods per BR-43.11 and the same finalize/post/pay workflow as regular runs.
  - FINAL_PAY includes accrued unused paid leave per the absence plan's payout flag (wave 42) and state final-pay deadlines from the US pack.
  - CHECK_REPLACEMENT voids the original payment (wave 36) and reissues without recalculation.
- **FR-43.19 Overpayment claims** — Should. The system shall create a payroll claim when a recalculation (retro, correction, reversal) yields negative net for a worker, with recovery as a payroll deduction schedule, direct repayment or write-off, and gross-vs-net treatment by tax year per BR-43.12.
- **FR-43.20 Statutory wage aggregates** — Should. The system shall aggregate wages and taxes per legal entity, jurisdiction and quarter/year (federal 941/940 lines, state UI wages and excess wages, local tax) from finalized statements, reconcile them to GL liability accounts, and expose them to the wave-48 country pack that renders and files the returns.
- **FR-43.21 Workers' compensation class codes** — Could. The system shall assign workers' compensation class codes per state to jobs (override per position), compute the premium base (gross minus overtime premium portion, minus excluded earnings flagged on earning codes), and report premium base by class, state and period.

### 8.43.4 Data model

**PayRun**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| pay_group_id, pay_period_id | uuid | yes | FK |
| run_type | enum | yes | REGULAR, OFF_CYCLE, CORRECTION |
| status | enum | yes | DRAFT, CALCULATED, REVIEWED, FINALIZED, POSTED, PAID, CANCELED |
| rule_set_binding | jsonb | yes | {country, taxYear, versions{}} frozen at calculate |
| gross_total, net_total, employer_cost_total | numeric(18,2) | computed | |
| journal_id, payment_batch_id | uuid | no | |
| calculated_at, finalized_by | timestamptz, uuid | no | |

**PayStatement**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| pay_run_id, worker_id, employment_id | uuid | yes | |
| gross, taxable_wages_fed, fica_wages, net | numeric(14,2) | yes | |
| lines[] | PayResultLine | yes | (code, kind EARNING/PRETAX/TAX/POSTTAX/EMPLOYER, hours, rate, amount, ytd_amount, basis_amount, rule_version) |
| w4 | jsonb | cond | {filingStatus, multipleJobs, dependentsCredit, otherIncome, deductions, extraWithholding} |
| pdf_ref | text | no | object store key |
| hash | text | yes | SHA-256 of lines; immutability check |

**TaxRuleSet**: id, country, jurisdiction (FED, state code, locality), tax_year, version, effective_from, brackets[] (filing_status, over, not_over, base_tax, rate), wage_base, rates{}, status (DRAFT, PUBLISHED). **EarningCode**: code, name, kind (HOURLY/SALARY/FLAT), multiplier, taxable flags (fed, fica, futa, state), gl_account_rule. **DeductionCode**: code, pre_tax bool, basis (FLAT/PERCENT_GROSS), limits {per_period, annual}, employer_match {percent, cap_percent}, gl_rule, priority.

**ExpenseReport**: id, worker_id, legal_entity_id, purpose, status (DRAFT, SUBMITTED, APPROVED, REJECTED, POSTED, PAID), total_amount, reimbursable_amount, company_paid_amount, currency, approver_chain[], ap_invoice_id. **ExpenseLine**: id, report_id, category_id, txn_date, amount, currency, exchange_rate, amount_accounting, merchant, city/country, project_id, wbs_node_id, card_txn_id, receipt_id, attendees[], itemizations[], policy_results[] (rule_id, outcome, justification), tax_code, personal bool. **ExpensePolicyRule** (schema in BR-43.5). **CardTransaction**: id, worker_id, card_last4, txn_date, post_date, merchant, mcc, amount, currency, status (UNASSIGNED, ASSIGNED, DISPUTED, PERSONAL).

**CashAdvance**: id (sequence EXP-ADV), worker_id, legal_entity_id, amount, currency, purpose, travel_requisition_id, project_id, status (DRAFT, SUBMITTED, APPROVED, REJECTED, PAID, PARTIALLY_SETTLED, SETTLED), paid_amount, settled_amount, returned_amount, recovered_via_payroll_amount, ap_prepayment_id, due_settlement_date. **TravelRequisition**: id (sequence EXP-TR), worker_id, purpose, destinations[] (country, city, from, to), estimate_lines[] (category_id, amount, currency), estimated_total, project_id, wbs_node_id, status (DRAFT, SUBMITTED, APPROVED, REJECTED, CLOSED), linked_report_ids[]. **ArrearsLedger**: worker_id, deduction_code, pay_run_id, amount_due, amount_taken, arrears_created, arrears_recovered, balance. **PayrollClaim**: id, worker_id, origin_pay_run_id, tax_year, gross_amount, net_amount, recovery_method (PAYROLL_DEDUCTION, DIRECT_REPAYMENT, WRITE_OFF), per_period_amount, balance, status (OPEN, RECOVERING, SETTLED, WRITTEN_OFF). **StatutoryWageAggregate**: legal_entity_id, jurisdiction, form_code (941, 940, SUI_<state>, …), period (YYYY-Qn or YYYY), line_code, amount, statement_count, reconciled_at. **WorkersCompClass**: state, class_code, description, rate_per_100, job_ids[]. PayRun adds `off_cycle_subtype` (BONUS, FINAL_PAY, MANUAL_CHECK, CHECK_REPLACEMENT, REVERSAL) and `original_pay_run_id`; DeductionCode adds `arrears_policy` and `recovery_limit_pct`; EarningCode adds `supplemental` bool and `wc_excluded` bool.

Invariants: Σ PayStatement.net = PayRun.net_total = payment batch total; Σ GL debits = gross + employer cost; a FINALIZED statement's hash never changes; a card transaction belongs to at most one line; report total = Σ lines (reimbursable + company-paid). Indexing: `PayStatement (worker_id, pay_run_id)` unique; `CardTransaction (worker_id, status)`; payroll tables partitioned by tax_year.

### 8.43.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | `/api/v1/payroll/runs` | Create run for pay period | key | PayrollRunMaintain |
| POST | `/api/v1/payroll/runs/{id}/calculate` | Gross-to-net calculation (Temporal workflow) | key + status guard | PayrollRunMaintain |
| POST | `/api/v1/payroll/runs/{id}/finalize` | Freeze statements | key | PayrollRunFinalize |
| POST | `/api/v1/payroll/runs/{id}/post` | GL post + payment batch | key | PayrollRunPost |
| PUT | `/api/v1/payroll/rule-sets/{id}/publish` | Publish rule-set version | ETag | PayrollRuleAdmin |
| POST | `/api/v1/expense/reports` | Create report | key | EssExpense |
| POST | `/api/v1/expense/reports/{id}/lines` | Add line (policy evaluated) | key | EssExpense |
| POST | `/api/v1/expense/reports/{id}/submit` | Submit | key | EssExpense |
| POST | `/api/v1/expense/card-transactions/import` | Import feed | key per file hash | ExpenseAdmin |
| POST | `/api/v1/expense/receipts` | Upload + OCR | key | EssExpense |
| POST | `/api/v1/expense/cash-advances` | Request cash advance | key | EssExpense |
| POST | `/api/v1/expense/cash-advances/{id}/decide` | Approve/reject advance | key | MssApprove |
| POST | `/api/v1/expense/cash-advances/{id}/return` | Record returned cash | key | ExpenseAdmin |
| POST | `/api/v1/expense/travel-requisitions` | Create requisition | key | EssExpense |
| POST | `/api/v1/expense/travel-requisitions/{id}/submit` | Submit requisition | key | EssExpense |
| POST | `/api/v1/payroll/runs/off-cycle` | Create off-cycle run (subtype, workers) | key | PayrollRunMaintain |
| POST | `/api/v1/payroll/claims/{id}/recovery` | Set claim recovery method/schedule | key | PayrollRunMaintain |
| GET | `/api/v1/payroll/statutory-aggregates?entity=&form=&period=` | Quarterly/annual wage aggregates | n/a | PayrollInquire |

Validation: calculate requires HR cutoff passed and all time registrations for the period approved (or policy allows unapproved as 0 hours); finalize requires zero BLOCK validation results; expense submit requires every REQUIRE_* satisfied.

| Event | Trigger | Payload | Consumers |
|---|---|---|---|
| `dev.passage.payroll.run.posted.v1` | GL post | see 8.0 | journal, bank, analytics |
| `dev.passage.payroll.statement.finalized.v1` | finalize | statementId, workerId, net, payDate | ESS, provider adapter |
| `dev.passage.expense.report.approved.v1` | approval | see 8.0 | ap, projects |
| `dev.passage.expense.policy.violated.v1` | BLOCK outcome at submit | reportId, lineId, ruleId, outcome | analytics, audit |
| `dev.passage.expense.cash-advance.paid.v1` | AP prepayment paid | advanceId, workerId, amount, currency, prepaymentId | expense settlement, analytics |
| `dev.passage.payroll.claim.created.v1` | negative net on recalculation | claimId, workerId, taxYear, gross, net | payroll admin notifications, AR (direct repayment) |
| `dev.passage.payroll.statutory-aggregate.closed.v1` | quarter/year aggregate reconciled | legalEntityId, formCode, period, totals | wave-48 country pack filing |

### 8.43.6 User experience

| Screen | Primary actions | Key fields/columns | Duty |
|---|---|---|---|
| Payroll workspace | tiles: runs by status, retro pending, exceptions | | PayrollInquire |
| Pay run detail | calculate, review exceptions, compare to prior run (variance %), finalize, post | worker, gross, taxes, net, Δ vs prior | PayrollRunMaintain |
| Statement viewer | drill line → rule version and basis | earnings, pre-tax, taxes, post-tax, net, YTD | PayrollInquire / EssPayStatement |
| Rule-set editor | edit brackets/rates, diff versions, publish | jurisdiction, tax year, brackets | PayrollRuleAdmin |
| Worker payroll profile | W-4 (2020+ fields), state forms, direct deposit (vaulted), garnishments | | PayrollWorkerMaintain |
| Expense report (ESS, mobile) | add line, attach receipt (camera), match card, itemize, submit | category, amount, policy badges | EssExpense |
| Expense approval (MSS) | approve/reject/return, view receipt side-by-side | | MssApprove |
| Policy admin | rule list, simulate against past reports | | ExpenseAdmin |
| Cash advances (ESS / admin) | request, view settlement, record return; admin aging list | amount, paid, settled, open, due date | EssExpense / ExpenseAdmin |
| Travel requisition (ESS / MSS) | create with estimates, submit, approve, link report | destinations, dates, estimate by category, actual vs estimate | EssExpense / MssApprove |
| Off-cycle workbench | pick workers and subtype, calculate, finalize, void/reissue | worker, subtype, gross, supplemental tax, net | PayrollRunMaintain |
| Claims and arrears | view balances, set recovery schedule, write off | worker, code, arrears balance, claim balance | PayrollRunMaintain |
| Statutory aggregates | review quarter totals, reconcile, release to pack | form, line, amount, GL balance, variance | PayrollInquire |

Rules: pay run variance view highlights workers whose net changed > 20% vs prior period; rule-set publish requires a second approver (SoD); expense mobile works offline for draft lines and receipt capture.

### 8.43.7 Business rules and edge cases

- **BR-43.1 US gross-to-net worked example** (rule set `US-FED-2025.1`, biweekly, 26 periods; Colorado flat 4.40% on federal taxable wages; W-4 2020+, Single, no Step 2/3/4 entries; 401(k) 6% pre-tax; employer match 50% of first 6%). Gross REGULAR 4,000.00. Pre-tax 401(k) 240.00. Federal taxable wages 3,760.00; FICA wages 4,000.00 (401(k) does not reduce FICA). Pub 15-T Step 1: adjusted annual wage = 3,760.00 × 26 = 97,760.00. Step 2 (standard withholding schedule, Single, 2025 table): bracket 54,875–115,125 → 5,578.50 + 22% × (97,760.00 − 54,875.00) = 5,578.50 + 9,434.70 = 15,013.20; ÷ 26 = 577.43. Step 3 credits 0; Step 4 extra 0 → federal withholding 577.43. Social Security 4,000.00 × 6.2% = 248.00 (YTD below wage base 176,100.00). Medicare 4,000.00 × 1.45% = 58.00. Colorado 3,760.00 × 4.40% = 165.44. Net = 4,000.00 − 240.00 − 577.43 − 248.00 − 58.00 − 165.44 = 2,711.13. Employer: match 120.00, SS 248.00, Medicare 58.00, FUTA 0.6% × 4,000 = 24.00 until YTD 7,000. GL: Dr salary expense 4,000.00, Dr employer tax expense 330.00, Dr 401(k) match expense 120.00; Cr net pay clearing 2,711.13, Cr 401(k) payable 360.00, Cr federal WH payable 577.43, Cr FICA payable 612.00, Cr FUTA payable 24.00, Cr CO WH payable 165.44.
- **BR-43.2 Rounding** — each tax computed on unrounded basis and rounded half-up to 0.01 per line; annualized federal amount divided by periods then rounded; YTD caps applied on the rounded cumulative.
- **BR-43.3 Wage base caps** — SS stops when YTD FICA wages reach the wage base, applying partial tax on the crossing period; additional Medicare starts on the crossing period for wages over 200,000.00 regardless of filing status.
- **BR-43.4 Retro** — a retro trigger is any change with effective date < current period start affecting a FINALIZED statement; recalculation re-runs the original rule-set versions, computes delta per line, and books deltas as RETRO_* lines in the current period; taxes on retro use current-period method (no amended returns).
- **BR-43.5 Expense policy rule schema** — JSON: `{ "id": "EXP-MEAL-075", "version": 3, "scope": {"legalEntities": ["ES-US"], "categories": ["MEALS"], "workerGroups": ["*"]}, "basis": "PER_DAY" | "PER_LINE" | "PER_REPORT" | "PER_TRIP", "condition": "line.amountAccounting > 75.00 && !line.attendees.size() > 0" (CEL over line, report, worker, perDiem), "threshold": {"amount": 75.00, "currency": "USD"}, "outcome": "WARN" | "BLOCK" | "REQUIRE_JUSTIFICATION" | "REQUIRE_RECEIPT", "message": "Meals over 75 USD/day need attendees", "effectiveFrom": "2026-01-01" }`. Example set: receipts required for any line ≥ 25.00 USD/EUR; lodging per night ≤ GSA/per-diem table × 1.5 else justification; mileage at IRS rate from table; alcohol category BLOCK; submission later than 60 days after txn_date BLOCK; duplicate (same merchant, amount, date within worker) BLOCK.
- **BR-43.6 Per diem** — rate by (country, city tier, date); first/last day 75%; meals provided reduce by 20/30/50% (breakfast/lunch/dinner); computed in rate currency then converted at line date.
- **BR-43.7 Finalization** — FINALIZED statements are immutable; corrections are OFF_CYCLE or CORRECTION runs that reference the original.
- **BR-43.8 Separation of duties** — the user who calculates cannot finalize; the user who publishes a rule set cannot be its author (enforced by wave-48 SoD engine, interim OPA check).
- **BR-43.9 Cash advance settlement** — at expense report approval, open PAID advances of the worker in the report currency are applied oldest first to reimbursable lines: applied = min(advance open, reimbursable total); the AP invoice for the report is reduced by the applied amount and the prepayment is settled against it (wave 18); if the report total < advance, the open remainder stays on the advance until (a) the worker records a cash return, (b) the settlement due date passes and the parameter "recover through payroll" creates a post-tax deduction in the next regular run, or (c) it is written off with ExpenseAdmin approval. Advances in another currency are converted at the report approval date rate (wave 12) and the FX difference posts to the realized FX account. Example: advance 1,000.00 USD; report 820.00 → applied 820.00, open 180.00; worker returns 180.00 → SETTLED. Report 1,150.00 → applied 1,000.00, AP invoice 150.00.
- **BR-43.10 Arrears** — deductions are taken in ascending priority (taxes and court-ordered garnishments first, then pre-tax benefits, then post-tax voluntary); when net would go below zero (or below the state minimum-net rule from the pack), the remaining deduction is: NO_ARREARS → skipped; ARREARS → full untaken amount added to the ledger; PARTIAL → taken up to net, rest to ledger. Recovery in later periods adds up to recovery_limit_pct (default 100 %) of the normal per-period deduction; pre-tax arrears recovered in a later tax year are recovered post-tax. Example: net before medical 150.00, medical 200.00 (PARTIAL) → take 150.00, arrears 50.00; next period normal 200.00 + recovery 50.00 = 250.00.
- **BR-43.11 Supplemental wage tax (US)** — earning codes flagged supplemental, paid in an OFF_CYCLE BONUS run or identified separately, are withheld at the flat supplemental rate (22 % for 2025; 37 % on the portion of YTD supplemental wages above 1,000,000.00) unless the worker's W-4 or the parameter selects the aggregate method; FICA applies as for regular wages. Example: bonus 5,000.00 USD → federal 1,100.00, SS 310.00, Medicare 72.50, state per pack (CO 4.40 % → 220.00), net 3,297.50.
- **BR-43.12 Overpayment claims** — a claim is created when the recalculated net for a worker < 0; in the same tax year the claim is recovered net (taxes reversed through the recalculation); across tax years the claim amount is gross (W-2c correction data flagged for the pack) per IRS rules; recovery as payroll deduction is limited by the pack's state rule (e.g. written consent and maximum per period); write-off posts to payroll overpayment expense.
- **BR-43.13 Travel requisition variance** — an expense report linked to an approved requisition is compared by category; when actual total > estimated total × (1 + tolerance) (parameter, default 10 %) the report requires justification (REQUIRE_JUSTIFICATION outcome); travel on dates outside the requisition's range is flagged WARN; a requisition closes when its linked report is POSTED or 60 days after the last travel date.

Pay run state machine: DRAFT → CALCULATED → REVIEWED → FINALIZED → POSTED → PAID; CALCULATED/REVIEWED → DRAFT (recalculate); DRAFT…REVIEWED → CANCELED. Expense report: DRAFT → SUBMITTED → APPROVED → POSTED → PAID; SUBMITTED → REJECTED | RETURNED (→ DRAFT).

### 8.43.8 Acceptance criteria

- **AC-43.1** (FR-43.5, BR-43.1) Given the worked-example worker, when the run calculates, then federal WH = 577.43, SS = 248.00, Medicare = 58.00, CO = 165.44, net = 2,711.13.
- **AC-43.2** (FR-43.5, BR-43.3) Given YTD FICA wages 174,000.00 and gross 4,000.00, when calculated, then SS = 130.20 (6.2% × 2,100.00).
- **AC-43.3** (FR-43.5) Given 200 fixture scenarios across filing statuses, Step 2 checkbox, Step 3/4 values and 6 states, when calculated, then all match Pub 15-T expectations to 0.01.
- **AC-43.4** (FR-43.8, BR-43.4) Given a rate increase effective two periods back, when retro evaluates and the next run calculates, then RETRO_REGULAR equals the summed delta and taxes recompute on the new gross.
- **AC-43.5** (FR-43.9) Given a posted run, when reconciliation runs, then GL payroll expense and liability accounts tie to Σ statements with 0.00 variance.
- **AC-43.6** (FR-43.12, BR-43.5) Given the 500-report fixture, when submitted, then policy outcomes equal the expected matrix exactly (0 false flags).
- **AC-43.7** (FR-43.13) Given a card feed with 1,000 transactions and 800 receipts, when matched, then ≥ 95% auto-match within tolerance and no cross-worker matches.
- **AC-43.8** (FR-43.14) Given an approved report with 300.00 USD reimbursable and 200.00 company-paid, when posted, then an AP invoice of 300.00 exists for the employee vendor and 200.00 for the issuer vendor, and the next payment run pays 300.00.
- **AC-43.9** (FR-43.15, BR-43.9) Given a PAID advance of 1,000.00 USD and an approved report of 820.00, when the report posts, then 820.00 settles against the prepayment, the advance shows open 180.00 and status PARTIALLY_SETTLED; after a recorded return of 180.00 it is SETTLED.
- **AC-43.10** (FR-43.16, BR-43.13) Given an approved requisition estimating 2,000.00 and a linked report totaling 2,300.00 with tolerance 10 %, when submitted, then the report requires justification.
- **AC-43.11** (FR-43.17, BR-43.10) Given net before medical 150.00 and a PARTIAL medical deduction of 200.00, when calculated, then 150.00 is taken, the arrears ledger holds 50.00, and the next period takes 250.00.
- **AC-43.12** (FR-43.18, BR-43.11) Given a BONUS off-cycle run of 5,000.00 for the BR-43.1 worker, when calculated, then federal 1,100.00, SS 310.00, Medicare 72.50, CO 220.00 and net 3,297.50.
- **AC-43.13** (FR-43.19, BR-43.12) Given a correction that reduces a finalized gross by 1,000.00 after net was paid, when recalculated in the same tax year, then a claim with net amount equal to the net difference is created and a recovery schedule of 4 equal deductions can be set.
- **AC-43.14** (FR-43.20) Given a quarter with 13 finalized biweekly runs, when the 941 aggregate closes, then lines 2 (wages), 3 (federal withholding), 5a/5c (SS/Medicare wages) equal Σ statements and tie to GL liability accounts with 0.00 variance.

### 8.43.9 Test plan and fixtures

Fixtures: `payroll-us-200` (200 scenarios, expected values in `fixtures/payroll/us-2025-expected.csv` derived from Pub 15-T worksheets); `payroll-de-20` (20 DE employees against BMF test cases); `payroll-retro-25`; `expense-policy-500` (reports × rules with expected outcomes); `card-feed-1000` (VCF and OFX samples).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Federal/state withholding | unit | tax engine | 200/200 to 0.01 |
| Caps and crossing periods | property | FICA/FUTA | never over-withholds; crossing period correct |
| Retro deltas | unit | retro engine | 25/25 |
| GL posting | contract | posting API | balanced; account per code rule |
| Policy engine | unit | CEL evaluator | 500/500 |
| Receipt OCR | integration | OCR service | amount/date extracted ≥ 90% on sample set |
| Pay run perf | performance | calculate workflow | 10,000 workers < 10 min |

Reconciliation: Σ statement net = payment batch; Σ tax lines by jurisdiction = liability accounts; Σ reimbursable = AP employee-vendor invoices.

### 8.43.10 Deployment and operations

Charts `passage-payroll` (Go API, Temporal calc workers, statement renderer) and `passage-expense` (TS API, OCR Python worker, feed importer); Postgres `payroll` (encrypted bank columns via `hr_pii` key pattern) and `expense`. Rule-set packs delivered as signed JSON bundles (`rulesets/us/2025.1.json`) loaded by migration job. Flags: `payroll.de-pack`, `payroll.provider-adapter`, `expense.ocr`. Schedules: `payroll-cutoff-reminder`, `expense-card-import` hourly (SFTP pull), `expense-aging` daily. SLOs: calculate 10k workers < 10 min; statement fetch p95 < 200 ms; policy evaluation p95 < 100 ms per line. Alerts: calculation failures; tax table pack missing for period end; card import failures. Runbook: re-run calculation from DRAFT; reissue payment batch; rotate SFTP keys. Rollback by digest; FINALIZED data is never altered by migrations.

### 8.43.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| PayrollPayCycle, PayrollPayPeriod | PayCycle, PayPeriod | |
| PayrollEarningCode, PayrollTaxCode, PayrollBenefit | EarningCode, TaxRuleSet mapping, DeductionCode | AX tax engine codes mapped to jurisdictions |
| PayrollWorkerTaxCode, PayrollWorkerTaxRegion, W-4 data | worker payroll profile | |
| PayrollPayStatement (current year) | PayStatement (YTD opening balances only) | historical statements archived |
| TrvExpTable/TrvExpTrans (open) | ExpenseReport/ExpenseLine | DRAFT/SUBMITTED only |
| TrvPolicy/TrvPolicyRule, TrvPerDiem | ExpensePolicyRule, PerDiemRate | X++ expression rules rewritten to CEL (manual) |
| T512W wage types, T549* | EarningCode/DeductionCode, PayCycle | |
| RT (PCL2 cluster) current year | YTD opening balances | |
| PA0210 (W-4), PA0207/0208/0209 | worker payroll profile | |
| Concur exports | ExpenseReport (open) | via CSV |

Validation: YTD gross/tax per worker equal to source within 0.01; count of open expense reports equal. Not migrated: prior-year statements (lakehouse archive), SAP schemas/PCRs (rebuilt as rule sets), closed expense reports.

### 8.43.12 Dependencies, risks, open questions

Depends on: wave 42 HR, wave 25 time, wave 18 AP, wave 19 payments, wave 36 bank files, wave 13 tax codes, wave 12 FX.

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Tax table maintenance each year | High | High | Signed rule-set packs; provider adapter path |
| OCR accuracy | Medium | Low | Draft lines always user-confirmed |

Open questions: (1) EU reference DE vs UK (master PRD Q3). Default DE; decide before wave 43 start. (2) Store direct-deposit details in payroll or wave-36 bank domain? Default payroll with vault; decide by wave 43.

### 8.43.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| Pay group frequency and pay-date offset | pay group | enum + int | BIWEEKLY, +5 days after period end | WEEKLY, BIWEEKLY, SEMIMONTHLY, MONTHLY; 0–15 | FR-43.1 period generation | Payroll parameters › Pay cycles (PayrollPayCycle) | T549A payroll area; T549Q periods; PA03 control record |
| Time/absence cutoff (hours before pay date) | pay group | int | 72 | 0–240 | Calculate precondition | Payroll parameters › Cutoff | PA03 control record "released for payroll"; T549S date modifiers |
| Unapproved time policy | pay group | enum | ZERO_HOURS | ZERO_HOURS, INCLUDE, BLOCK | 8.43.5 validation | Payroll parameters › Pay statements | CATS transfer status / PT time evaluation |
| Rule-set pack per country/year | legal entity | lookup | latest PUBLISHED | TaxRuleSet versions | FR-43.2 binding | Tax engine update (PayrollTaxEngine) | T511K constants, T5UTZ US tax authorities, BSI TaxFactory version |
| Supplemental tax method | legal entity | enum | FLAT | FLAT, AGGREGATE | BR-43.11 | Payroll parameters › Supplemental tax | T5UTN tax models / BSI method |
| Variance highlight threshold | tenant | pct | 20 % | 1–100 % | Pay run variance view | n/a | PC00_M99_PA03_CORR / payroll log thresholds n/a |
| Rounding mode | country pack | enum | HALF_UP per line | HALF_UP, HALF_EVEN | BR-43.2 | Payroll parameters › Rounding | T511K rounding constants |
| Arrears policy per deduction code | deduction code | enum | ARREARS | ARREARS, NO_ARREARS, PARTIAL | BR-43.10 | Deduction code › Arrears (PayrollDeductionCode) | T51P6 processing class / deduction priority and arrears |
| Arrears recovery limit | deduction code | pct | 100 % | 0–500 % | BR-43.10 | Arrears recovery settings | T51P6 recovery rules |
| Payment method default | pay group | enum | DIRECT_DEPOSIT | DIRECT_DEPOSIT, CHECK, PAYCARD | FR-43.9 | Payroll parameters › Payment | T042Z payment methods; IT0009 bank details |
| Payroll GL posting granularity | legal entity | enum | CODE_X_DIMENSION | SUMMARY, CODE_X_DIMENSION, WORKER | FR-43.9 | Payroll parameters › Ledger › Summarize | T52EL / T52EK symbolic accounts; PC00_M99_CIPE variant |
| Expense report number sequence and approval chain | legal entity | lookup | EXP-REP; manager → cost-center owner > 5,000.00 | wave-4 workflow | FR-43.11 | Travel and expense parameters › Number sequences, workflow | T702N trip number range; travel workflow WS20000050 |
| Receipt required threshold | legal entity | amount | 25.00 USD/EUR | ≥ 0 | BR-43.5 | Expense management parameters › Receipt required | T706B1 receipt types "receipt required" |
| Submission deadline (days after txn) | legal entity | int | 60 | 1–365 | BR-43.5 | Expense policy (TrvPolicy) | T706D trip provision variant deadlines |
| Mileage rate table | legal entity | table | IRS / BMF tables | per vehicle type, date | BR-43.5 | Mileage rates (TrvMileage) | T706K / T706F mileage rates |
| Per diem tables and meal reduction | legal entity | table | GSA / BMF | country, city tier, date | BR-43.6 | Per diems (TrvPerDiem) | T706V/T706U per diem tables |
| Card match tolerance | tenant | pct + days | ±1 %, 2 days | 0–10 %, 0–10 days | FR-43.13 | Credit card parameters | Concur card matching |
| Cash advance max per request | legal entity | amount | 2,000.00 | ≥ 0 | FR-43.15 | Expense management parameters › Cash advance limit | T706* advance limits |
| Cash advance settlement due (days after trip end) | legal entity | int | 30 | 1–180 | BR-43.9 aging | Cash advance parameters › Settlement | PR05 advance settlement |
| Recover unsettled advances through payroll | legal entity | bool | false | | BR-43.9 | Cash advance parameters › Recover via payroll | Trip transfer to payroll (PRPY) |
| Travel requisition required | legal entity / category | bool | false | | FR-43.16 | Travel requisition parameters | T702 travel plan settings: request required |
| Requisition variance tolerance | legal entity | pct | 10 % | 0–100 % | BR-43.13 | Requisition policies | Travel request estimated cost check |
| Workers' comp class code source | legal entity | enum | JOB | JOB, POSITION | FR-43.21 | Workers compensation setup | Feature for WC code (IT0001 assignment) |

### 8.43.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Payroll register | operational report | pay run, pay group, department | worker, hours, earnings, pre-tax, taxes, post-tax, net, employer cost | `pay_register` | Pay statement report (PayrollPayStatementReport) | PC00_M10_CEDT remuneration statement; PC00_M10_REC payroll reconciliation |
| Wage type / code reporter | drill-down | code, period range, entity | amount, hours, worker count by code | `pay_codes` | Earning statements inquiry | H99CWTR0 / PC00_M99_CWTR wage type reporter |
| Tax liability reconciliation | operational report | jurisdiction, period | withheld, employer tax, GL liability, variance | `pay_tax_recon` | Payroll tax reports | PC00_M10_RPCTRCU0 tax reconciliation |
| Variance vs prior run | drill-down | pay run, threshold | worker, Δ gross, Δ net, Δ % | `pay_variance` | n/a | PC00_M99_PA03_CHECK / payroll journal comparison |
| Arrears and deductions not taken | list inquiry | code, worker, open only | due, taken, arrears balance | `pay_arrears` | Arrears inquiry | ARRRS / DDNTK cluster tables via H99CWTR0 |
| Claims | list inquiry | status, tax year | worker, gross, net, balance, method | `pay_claims` | n/a | PC00_M10_CLAIMS |
| Statutory aggregates (941/940/SUI) | statutory report | entity, form, period | form lines, amounts, variance to GL | `pay_statutory` | Payroll tax forms (941, 940) | PU19 tax reporter; PC00_M10_* |
| YTD by worker | list inquiry | worker, year | YTD per code, wage bases | `pay_ytd` | Pay statement YTD | PC00_M10_CLJN payroll journal |
| Workers' comp premium base | operational report | state, class, period | premium base, overtime premium excluded, estimated premium | `pay_wc` | SSRS Workers compensation report | US workers' comp report (RPCWCOU0) |
| Expense reports | list inquiry | status, worker, period, project | report, total, reimbursable, company-paid, policy flags | `exp_reports` | Expense reports list (TrvExpTableListPage); SSRS Expense report | PRTE trip data; PR10 trip overview |
| Policy violations | KPI | rule, department, period | violation count, amount, justified % | `exp_violations` | Policy violation inquiry | Concur audit rule reports |
| Unassigned card transactions | list inquiry | worker, age | txn, merchant, amount, days unassigned | `exp_cards` | Unattached credit card transactions (TrvPBSMaindata) | Concur card feed exceptions |
| Cash advance aging | operational report | status, days overdue, worker | advance, paid, settled, open, days past due | `exp_advances` | Cash advance inquiry; SSRS Cash advance report | PR05 advances list |
| Requisition vs actual | drill-down | requisition, department | estimate by category, actual, variance | `exp_req_variance` | Travel requisition inquiry | Travel request vs trip (TP03 / PRTE) |

### 8.43.15 Printed and electronic documents

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Pay statement | statement FINALIZED | header: employer, worker, pay period, pay date; lines: earnings (hours, rate, amount, YTD), pre-tax, taxes, post-tax, employer contributions, arrears/claims; footer: net, payment split, accrued leave balances | PDF, ESS | pay run id + worker | US state pay-stub content laws (e.g. CA Labor Code §226, 3 years); DE GewO §108 Entgeltabrechnung; retain 7 years (payroll records) |
| Direct deposit advice | payment batch released | net, bank last 4, pay date | PDF, email | statement id | as pay statement |
| Payroll check | MANUAL_CHECK / CHECK payment / CHECK_REPLACEMENT | MICR line, check number, payee, amount in words, stub | PDF (check stock), positive-pay file to bank (wave 36) | check number per bank account | bank and payroll record retention |
| NACHA / SEPA payment file | run POSTED | wave-36 layouts (PPD entries, pain.001 salary category SALA) | NACHA, ISO 20022 pain.001 | wave-36 batch id | bank record |
| W-2 / 1099 data, DE Lohnsteuerbescheinigung data | year end | form box structures | data to wave-48 pack (PDF, EFW2, ELSTER) | year + worker | 4 years (IRS), 6 years (DE AO §147) |
| Payroll GL voucher | run POSTED | wave-11 voucher with code × dimension lines | PDF | voucher | accounting record |
| Expense report | report SUBMITTED / POSTED | header: worker, purpose, period; lines: date, category, merchant, amount, currency, rate, project, policy outcome; receipts appendix | PDF, ESS | EXP-REP | tax receipt retention (US IRS 3 years; DE 10 years for Belege under AO §147) |
| Cash advance request | advance SUBMITTED | worker, amount, purpose, approval, settlement | PDF | EXP-ADV | accounting record |
| Travel requisition | SUBMITTED | destinations, dates, estimates, approval | PDF | EXP-TR | none beyond 2 years |

### 8.43.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| `payroll-calculate` (Temporal) | on demand per run | pay run, worker shard of 500 | key per (run, worker); status guard | 10,000 workers < 10 min | per-shard retry; failed workers listed; run stays CALCULATED-with-errors | failure count > 0; runtime > 15 min |
| `payroll-cutoff-reminder` | cutoff − 24 h | pay group | key (group, period) | < 1 min | retry | reminders undelivered |
| `payroll-retro-detect` | on every effective-dated change event; nightly sweep | worker | key (worker, change id) | < 1 s per event | retry | retro pending > 2 periods |
| `payroll-statutory-aggregate` | quarter end + 5 days; year end | legal entity, form | key (entity, form, period); re-run replaces | < 5 min | retry; variance report | variance ≠ 0.00 |
| `payroll-ruleset-load` | on pack release (migration job) | country, year | signature + version unique | < 1 min | reject unsigned | pack missing for next period end (14 days ahead) |
| `expense-card-import` | hourly (SFTP pull) | issuer feed | key per file hash | < 2 min per 10,000 txns | quarantine bad file | import failure; no file for 24 h |
| `expense-aging` | daily 07:00 | legal entity | key (report, day) | < 1 min | retry | reports older than submission deadline |
| `cash-advance-aging` | daily 07:00 | legal entity | key (advance, day) | < 1 min | retry | advances past due; payroll recovery created |
| `travel-requisition-close` | daily 02:00 | legal entity | key (requisition) | < 1 min | retry | none closed for 7 days |

### 8.43.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles that include it |
|---|---|---|---|
| PayrollRunMaintain | runs create/calculate, off-cycle, claims recovery; Pay run detail, Off-cycle workbench | with PayrollRunFinalize (same run); with HcmActionMaintain | Payroll administrator |
| PayrollRunFinalize | `/runs/{id}/finalize` | with PayrollRunMaintain for the same run (BR-43.8) | Payroll manager |
| PayrollRunPost | `/runs/{id}/post`, payment batch release | with PayrollWorkerMaintain (bank details) | Payroll manager, Treasury |
| PayrollRuleAdmin | rule-set edit/publish | author ≠ publisher (BR-43.8) | Payroll configuration specialist |
| PayrollWorkerMaintain | W-4, state forms, direct deposit, garnishments | with PayrollRunPost | Payroll administrator |
| PayrollInquire / EssPayStatement | registers, reports, statutory aggregates; own statements | none | Payroll administrator, Controller; Employee |
| EssExpense | reports, receipts, cash advances, requisitions (own) | with MssApprove on own items | Employee |
| MssApprove | approve/reject/return reports, advances, requisitions | own items blocked | Manager |
| ExpenseAdmin | card import, policy rules, advance returns, write-offs | with EssExpense for own items | Expense administrator |
| ExpenseAuditor | read all reports, receipts, violations; return to worker | with ExpenseAdmin rule publish | Internal audit |

### 8.43.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| PAY-RUN-001 | error | Cutoff for {period} has not passed | Calculate too early | Wait for cutoff or change parameter | 8.43.5 validation |
| PAY-RUN-002 | error | {n} time registrations for {period} are not approved | Unapproved time with BLOCK policy | Approve time or change policy | 8.43.5 |
| PAY-RUN-003 | error | Run has {n} blocking validation results | Finalize precondition | Resolve exceptions | 8.43.5 |
| PAY-RUN-004 | error | You calculated this run; another user must finalize | SoD | Ask a payroll manager | BR-43.8 |
| PAY-RULE-001 | error | No published rule set for {country} {year} on {date} | Missing pack | Load and publish the pack | FR-43.2 |
| PAY-RULE-002 | error | Publisher must differ from author | SoD | Second approver | BR-43.8 |
| PAY-STMT-001 | error | Statement {id} is FINALIZED and cannot change | Immutability | Use a CORRECTION or OFF_CYCLE run | BR-43.7 |
| PAY-ARR-001 | warning | Deduction {code} short by {amount}; added to arrears | Insufficient net | Review arrears ledger | BR-43.10 |
| PAY-CLM-001 | warning | Negative net {amount} for {worker}; claim {id} created | Overpayment | Set recovery method | BR-43.12 |
| PAY-OFF-001 | error | Original payment {id} not found or already voided | Check replacement | Select the original payment | FR-43.18 |
| PAY-STAT-001 | error | Aggregate {form} {period} differs from GL by {variance} | Reconciliation | Investigate postings before release | FR-43.20 |
| EXP-POL-001 | error | {rule message} | BLOCK outcome | Correct the line | BR-43.5 |
| EXP-POL-002 | warning | {rule message} | WARN outcome | Optional justification | BR-43.5 |
| EXP-REC-001 | error | Receipt required for lines ≥ {threshold} | REQUIRE_RECEIPT | Attach receipt | BR-43.5 |
| EXP-CARD-001 | error | Card transaction {id} already linked to line {line} | Double use | Unlink first | 8.43.4 invariant |
| EXP-CARD-002 | error | Feed file {hash} was already imported | Duplicate import | None | FR-43.13 |
| EXP-ADV-001 | error | Advance exceeds limit {limit} | Parameter | Reduce amount | FR-43.15 |
| EXP-ADV-002 | error | You have advances past settlement due; new advances blocked | Aging | Settle open advances | FR-43.15 |
| EXP-ADV-003 | info | {amount} of advance {id} applied to report {report} | Settlement | None | BR-43.9 |
| EXP-TR-001 | warning | Report exceeds approved requisition by {pct} %; justification required | Variance | Justify | BR-43.13 |
| EXP-TR-002 | error | Requisition required for category {category} | Parameter | Create a requisition | FR-43.16 |

### 8.43.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| Pay run calculate | 10,000 workers per run; 4 runs/month | run < 10 min; per worker 20 / 60 / 150 ms | 20 workers/s per worker process, 8 processes | PayResultLine ≈ 40 lines × 260k statements = 10 M rows/year ≈ 4 GB | partitioned by tax_year; (worker_id, pay_run_id) unique | add Temporal workers; run takes longer, never partial-finalizes |
| Statement fetch (ESS) | 10,000 views on pay date; peak 2,000/min | 50 / 200 / 400 ms | 100/s | PDFs ≈ 150 KB × 260k = 39 GB object store | object store key per statement | serve PDF from cache; drill-down delayed |
| Policy evaluation per line | 50,000 lines/month; peak 300/min | 20 / 100 / 200 ms | 50/s | policy_results inline | rules cached per (entity, category) | evaluate at submit only, line-commit warnings deferred |
| Card import | 100,000 txns/month | per file 10,000 rows < 2 min | 100 rows/s | ≈ 200 MB | (worker_id, status); file hash unique | queue files; matching delayed |
| Receipt OCR | 40,000/month | 2 / 6 / 12 s | 5/s | images ≈ 400 KB each = 16 GB | object store | draft lines created later; user can key manually |
| Statutory aggregate | 4 quarters × 20 forms per entity | < 5 min | — | negligible | (entity, form, period) | runs later |
| Cash advance settlement at approval | 1,000/month | 100 / 300 / 600 ms | 10/s | negligible | (worker_id, status) | settlement queued; AP invoice waits |

### 8.43.20 Localization and compliance hooks

- Payroll is country-pluggable through rule-set packs (FR-43.2); wave 43 ships US (federal, 6 reference states) and DE as the EU reference; wave-48 packs add jurisdictions, statutory forms (941, 940, state UI, W-2/W-3 EFW2, 1099-NEC, DE Lohnsteueranmeldung, DEÜV/SV-Meldungen, Beitragsnachweis), e-filing (IRS FIRE/EFW2, ELSTER, sv.net/dakota) and local taxes.
- Supplemental tax rates, wage bases (SS 176,100.00 for 2025), FUTA credit-reduction states, state minimum-net and final-pay deadlines, and garnishment limits (CCPA 25 % of disposable earnings) are pack data, never code.
- New-hire reporting (state directories), workers' comp state reports and ACA hours data are rendered by the US pack from wave 42/43 data; extension point `payroll.statutory.<cc>.<form>`.
- Expense: VAT recovery per country (wave 13 tax codes; DE Vorsteuer on hotel 7 %/19 % split, UK VAT receipts), per diem tables (US GSA/DoD, DE BMF Verpflegungsmehraufwand 14/28 EUR, 2025 values), mileage (IRS 0.70 USD/mile 2025; DE 0.30 EUR/km) and fringe-benefit treatment of non-reimbursable items are pack data.
- Currency: pay in the pay group currency only; expense lines in any currency converted at the transaction-date rate type configured by the pack (card issuer rate when present).
- Privacy: pay statements and bank details use the `hr_pii` key pattern; receipt images may contain card data — OCR masks PANs to last 4 before storage (PCI DSS); EU retention limits on receipts follow the country pack.
- Calendars: pay dates shift for bank holidays per wave-8 bank calendar (US Federal Reserve, TARGET2); semi-monthly periods follow the 15th/last-day convention.

### 8.43.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX #71 — 12.10.1 Cash advances (TrvCashAdvance) | PARTIAL | ADDED as FR-43.15 | BR-43.9 |
| AX #74 — 12.10.2 Cash advance inquiry | PARTIAL | ADDED as FR-43.15 | "Cash advance aging" in 8.43.14 |
| AX #76 — 12.10.2 SSRS Cash advance report | PARTIAL | ADDED as FR-43.15 | Document in 8.43.15 |
| AX #79 — 12.10.3 Reconcile/settle cash advances | PARTIAL | ADDED as FR-43.15 | Settlement at report approval + `cash-advance-aging` |
| AX #83 — 12.10.4 Expense management parameters — Intercompany / Cash advance | PARTIAL | ADDED as FR-43.15 | Cash advance parameters in 8.43.13; intercompany part stays with wave 40 (register #73/#82) |
| AX #84 — 12.10.4 Cash advance parameters, posting | PARTIAL | ADDED as FR-43.15 | Prepayment posting via wave 18 |
| AX #72 — 12.10.1 Travel requisitions (TrvRequisitionTable) | PARTIAL | ADDED as FR-43.16 | BR-43.13 |
| AX #75 — 12.10.2 Travel requisition inquiry | PARTIAL | ADDED as FR-43.16 | "Requisition vs actual" |
| AX #85 — 12.10.4 Requisition policies / types | PARTIAL | ADDED as FR-43.16 | Policy engine (BR-43.5) evaluates requisition lines |
| AX #117 — 12.13.3 Arrears recovery processing | PARTIAL | ADDED as FR-43.17 | BR-43.10 |
| AX #115 — 12.13.2 SSRS Workers compensation report | PARTIAL (wave 48) | ADDED as FR-43.21 | Class codes and premium base here; state-specific report layouts DEFERRED to wave 48 |
| AX #116 — 12.13.2 SSRS New hire reporting (US state) | PARTIAL (wave 48) | DEFERRED to wave 48 | Source: `hr.employment.changed.v1` HIRE events; state layouts in US pack |
| SAP #326 — 13.28 PC00_M10_OSUP / PUOC_10 off-cycle workbench | PARTIAL-43 | ADDED as FR-43.18 | BR-43.11 |
| SAP #327 — 13.28 PC00_M10_CLAIMS claims processing | PARTIAL-43 | ADDED as FR-43.19 | BR-43.12 |
| SAP #335 — 13.28 PC00_M10_* US reports (941, 940, W-2, SUI, new hire, EEO-1, workers' comp) | PARTIAL-43 | ADDED as FR-43.20 | Aggregates and reconciliation here; rendering/filing DEFERRED to wave 48; EEO-1 is wave-42 data via wave 48 |
| SAP #336 — 13.28 PU19 tax reporter runs | PARTIAL-43 | ADDED as FR-43.20 | Job `payroll-statutory-aggregate` |
| SAP #341 — 13.29 TP01/TP02/TP03/PR01 travel request | PARTIAL-43 | ADDED as FR-43.16 | Booking (TP01 planning) OUT-OF-SCOPE per 8.43.1 |
| SAP #342 — 13.29 PR02/PR05 cash advances | PARTIAL-43 | ADDED as FR-43.15 | |
| SAP #343 — 13.29 Travel request / plan settings, approval workflows, substitutes (T702) | PARTIAL-43 | ADDED as FR-43.16 | Substitutes via wave-4 workflow delegation |

## 8.44 Wave 44: Enterprise Asset Management (Maintenance)

### 8.44.1 Scope and business context

EAM replaces D365 Asset Management (EntAssetObject, EntAssetFunctionalLocation, EntAssetMaintenancePlan, EntAssetWorkOrder, EntAssetCounter, EntAssetMaintenanceRequest, EntAssetBOM) and SAP PM (IFLOT functional locations, EQUI equipment, MPLA/MPOS plans with IP10 scheduling, QMEL notifications IW21, AUFK orders IW31/IW32, IMPTT measuring points, AFIH order header). After this wave a maintenance planner models the plant as functional locations and assets, sets time/counter/condition-based plans that generate work orders, dispatches technicians against wave-23 resource capacity, consumes spares from wave-16 inventory, records labor and downtime, and sees costs settle to the asset and GL. IoT readings from wave 25 trigger requests.

Out of scope: predictive ML models (hooks only), calibration management, fleet management, linear assets.

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| SAP IP10/IP30 deadline monitoring is a batch job (RISTRA20) run daily or weekly; calls are generated late or in bursts and counter-based plans need IK11 readings entered by hand; D365 "Schedule maintenance plans" is likewise a batch with a fixed horizon | Scheduling is periodic recalculation, not event-driven | Scheduler runs on every completion/reading event plus nightly; forecast from rolling usage; algorithm stated in BR-44.1 | Call latency 1–7 days batch → < 60 s after triggering event; 40/40 plan fixtures exact |
| Creating a corrective work order in D365 Asset Management from a request takes 8–10 clicks across Maintenance request → Work order → Job → Lines; SAP IW21 → IW31 → components → IW32 release is 4 transactions | Request and order are separate forms with re-keyed fields | Request triage converts to a WO in one action carrying asset, location, symptom, parts and checklist | Clicks 10 → 2; re-keyed fields 0 |
| IoT condition data lives in a separate product (D365 IoT Intelligence, SAP ASPM/IoT) and raises alerts in another UI; maintenance requests are created by people copying values | No in-ERP condition engine | Condition rules on the wave-25 stream create deduplicated requests in < 5 s | Reading-to-request 1 h–1 day manual → p95 < 5 s; duplicate requests 0 |
| Maintenance downtime never reaches production scheduling: SAP PM order has no capacity link to PP work-center scheduling except manual CM01 entries; D365 AM downtime is reporting-only | Separate modules without a shared resource model | WOs reference wave-22 resources; downtime emits a capacity exception consumed by the wave-23 scheduler | Planned-downtime scheduling conflicts −90%; capacity reflects downtime within 10 s |
| WO cost reconciliation: SAP KO88 settlement to asset/cost center with settlement rules per order; D365 AM costs post via project and need "project invoice/estimate" steps | Order cost is a separate settlement object | Cost rolls to the asset and GL on completion through the posting API; nightly reconciliation | Settlement step eliminated; WO-cost-to-GL variance 0.00 |

### 8.44.2 AX / SAP parity map

| Capability | AX / D365 | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Functional locations | EntAssetFunctionalLocation, types, structure | IFLOT, IFLOTX, IL01/IL02 | `FunctionalLocation` (ltree path) | full |
| Assets/equipment | EntAssetObject, EntAssetObjectType, asset lifecycle states | EQUI, EQKT, IE01/IE02, equipment categories | `Asset` with `AssetType` and lifecycle | full |
| Counters/measuring points | EntAssetCounter, EntAssetObjectCounter, counter registration | IMPTT measuring points, IMRG measurement documents (IK11) | `Counter`, `CounterReading` | full |
| Maintenance plans | EntAssetMaintenancePlan, plan lines (time/counter), plan job types | MPLA/MPOS, IP10 scheduling, IP30 deadline monitoring, strategy plans (IP11) | `MaintenancePlan`, `PlanLine`, scheduler | full |
| Requests/notifications | EntAssetMaintenanceRequest, request lifecycle | QMEL/QMIH notifications M1/M2/M3, IW21/IW22 | `MaintenanceRequest` | full |
| Work orders | EntAssetWorkOrder, EntAssetWorkOrderLine, job types, lifecycle states, checklists | AUFK/AFIH orders PM01/PM02/PM03, IW31/IW32, operations AFVC | `MaintWorkOrder`, `WorkOrderJob`, `Checklist` | full |
| Scheduling | Work order scheduling against resources (production resources) | Capacity planning CM01, work centers | wave-23 scheduler with `MAINTENANCE` job class | full |
| Parts and labor | Work order item requirements → inventory journal; hour journal | Component reservation (RESB), goods issue MIGO 261, confirmation IW41 (AFRU) | `WorkOrderPart` → wave-16 reservation/issue; `WorkOrderLabor` | full |
| Costing | Work order costs to asset (project-based in D365 AM) | Order settlement KO88 to cost center/asset, cost analysis | cost rollup to asset and GL via posting API; optional wave-37 capitalization | full |
| Condition monitoring | Counter thresholds; IoT Intelligence | PM-PRM, IoT via SAP IoT/ASPM | `ConditionRule` on IoT readings (wave 25) | improved |
| KPIs | Asset KPIs (D365 AM) | MCI7 MTTR/MTBF | `/reports/eam/kpis` MTBF, MTTR, availability, cost/asset | full |
| Maintenance rounds | EntAssetMaintenanceRound (route of assets inspected together) | Object list on order (OBJK), task list for multiple objects, IP41 single-cycle plan with object list | `MaintenanceRound`, plan line type ROUND (FR-44.14) | full |
| Maintenance sequences | EntAssetMaintenanceSequence (ordered job types) | Strategy plans IP11/IP42 with packages and hierarchy (T351P) | `MaintenanceSequence` with step hierarchy (FR-44.15) | full |
| Fault registration and designer | EntAssetFault (symptoms, areas, types, causes, remedies), fault designer per asset type | Catalog profiles (QS41 code groups/codes, OIS2 catalog profile), notification items damage/cause/activity (QMFE/QMUR/QMMA) | `FaultCatalog` per asset type (FR-44.16) | full |
| Asset warranties | EntAssetWarranty, warranty types, vendor warranty setup, warranty check on work order | Warranty master BGMK (BGM1), equipment warranty (IE02 warranty tab), warranty check on IW31 | `AssetWarranty` (FR-44.17) | full |
| Maintenance budgets | EntAssetMaintenanceBudget, budget calculation from plans | IP30 cost estimates / plan cost simulation (IP31 / IP-costing), investment programs | `MaintenanceBudget` (FR-44.18) → wave 38 | full |
| Permits and LOTO | not native | Permits (T357G, IPMD), Work Clearance Management WCM (WCA* objects: work approval, application, lockout/tagout) | `Permit`, `IsolationPoint`, `LotoLock` (FR-44.19) | full |
| Refurbishment | not native | IW81 refurbishment order (PM04), IW8W goods receipt, OMWC split valuation by condition (T025) | refurbishment WO type + wave-16 condition valuation (FR-44.20) | full |
| Fleet objects | Asset type attributes | IE31/IE36/IE37 fleet objects (FLEET table: license plate, VIN, fuel), MCIZ fuel analysis | asset type FLEET_VEHICLE attributes + fuel counters (FR-44.21) | full |
| Criticality / RCM | Asset criticality field | ASPM (Asset Strategy and Performance Management) criticality assessment, RCM; Predictive Asset Insights | `CriticalityAssessment` (FR-44.22); predictive models as hooks | partial — no ML models in v1 |

### 8.44.3 Functional requirements

- **FR-44.1 Functional locations** — Must. The system shall maintain a functional-location hierarchy per site (max depth 12) with types, with assets installable at locations and location history.
- **FR-44.2 Assets** — Must. The system shall maintain assets (type, manufacturer/model, serial, criticality 1–5, lifecycle state, parent asset, production resource link, fixed asset link, warranty) with install/remove history.
  - Lifecycle: CREATED → ACTIVE → INACTIVE → DISPOSED; only ACTIVE assets can receive work orders (override duty).
- **FR-44.3 Counters** — Must. The system shall maintain counters (unit, cumulative/reset, source MANUAL/IOT) with readings validated monotonic for cumulative counters and an average daily usage rate (rolling 30 days).
- **FR-44.4 Maintenance plans** — Must. The system shall define plans with lines of type TIME (interval + unit, calendar), COUNTER (interval + counter), or TIME_OR_COUNTER (whichever first) / TIME_AND_COUNTER, with job type, checklist, estimated hours and parts, early/late tolerances and generation horizon.
- **FR-44.5 Plan scheduling and generation** — Must. The system shall compute next-due dates/readings per BR-44.1 and generate work orders within the horizon, merging lines due within a merge window into one work order per asset.
- **FR-44.6 Maintenance requests** — Must. The system shall capture requests from users, mobile, IoT condition rules and wave-32 quality, with triage to work order, reject or defer.
- **FR-44.7 Work orders** — Must. The system shall manage work orders with jobs (asset, job type, trade, variant), lifecycle, checklists, planned/actual parts and hours, downtime, failure codes (symptom/cause/remedy) and completion notes.
- **FR-44.8 Scheduling** — Must. The system shall schedule work order jobs against wave-23 resources (maintenance teams, technicians via wave-42 workers) with finite capacity and record planned downtime on the production resource.
- **FR-44.9 Parts** — Must. The system shall reserve, pick and issue parts through wave-16 inventory journals with cost flowing to the work order.
- **FR-44.10 Labor** — Must. The system shall record labor via hour registrations (wave 25 kiosk or mobile) at trade cost rates.
- **FR-44.11 Costing and GL** — Must. The system shall accumulate parts, labor, external services (wave-17 PO) and overhead per work order, post to maintenance expense accounts by (asset type, job type, cost dimension of the functional location) and roll up to the asset; CAPITAL job types create wave-37 CIP postings.
- **FR-44.12 Condition rules** — Must. The system shall evaluate IoT readings against thresholds (above/below/rate-of-change over window) and create requests within 5 s of breach, with deduplication.
- **FR-44.13 KPIs** — Should. The system shall compute MTBF, MTTR, availability, cost per asset and backlog hours by asset, location and type.
- **FR-44.14 Maintenance rounds** — Should. The system shall define rounds (ordered stops of assets or functional locations, each with a job type and checklist, optional walking/driving minutes between stops) scheduled by plan lines of type ROUND that generate one work order per round occurrence with one job per stop.
  - A stop that cannot be performed is marked SKIPPED with a reason; skipped stops generate a follow-up request when the job type is mandatory.
- **FR-44.15 Maintenance sequences** — Should. The system shall define sequences of job types on an asset or asset type with interval multiples and a hierarchy (e.g. step A every 1×, B every 4×, C every 12× of a base interval of 250 h), generating for each due occurrence only the highest-hierarchy step plus steps not included in it, per BR-44.10.
- **FR-44.16 Fault catalog and analysis** — Must. The system shall maintain fault catalogs per asset type (symptoms, areas, types, causes, remedies, with allowed combinations), require fault registration on CORRECTIVE and EMERGENCY work orders at completion, and analyze faults by asset, type, symptom, cause and period (count, downtime, cost, repeat rate within 30 days).
  - Replaces the free-text failure_symptom/cause/remedy codes on `MaintWorkOrder` with FK references; existing columns stay for migration (populated from the catalog code).
- **FR-44.17 Asset warranties** — Should. The system shall record vendor/manufacturer warranties per asset (warranty type, vendor party, start from purchase/installation, duration in months or counter units, parts/labor coverage, exclusions), check coverage when a work order is created or parts are issued, flag covered lines and create a vendor warranty claim (wave-45 FR-45.15) on completion.
- **FR-44.18 Maintenance budgets** — Should. The system shall calculate maintenance budgets per asset, functional location and period from active plans over a horizon (forecast occurrences × est_hours × trade cost rate + planned parts at standard cost + external services), allow manual budget lines, transfer the result to a wave-38 budget plan, and report budget vs actual vs committed.
- **FR-44.19 Permits and lockout/tagout** — Must. The system shall define permit types (HOT_WORK, CONFINED_SPACE, ELECTRICAL_ISOLATION, WORKING_AT_HEIGHT, LINE_BREAKING, custom) required by job type, asset type or functional location; maintain isolation points per asset (energy type, location, isolation method, verification step); and manage permits with lifecycle REQUESTED → APPROVED → ISSUED → SUSPENDED → CLOSED and personal LOTO locks per technician per BR-44.11.
- **FR-44.20 Refurbishment orders** — Should. The system shall support rotable spares valued by condition (NEW, REFURBISHED, DAMAGED as a wave-16 inventory condition dimension with cost per condition) and refurbishment work orders that issue DAMAGED units, collect labor, parts and external repair cost, and receive the same serials as REFURBISHED at the condition's valuation per BR-44.12.
- **FR-44.21 Fleet attributes** — Could. The system shall provide asset type FLEET_VEHICLE with license plate, VIN, fuel type, tank capacity, assigned driver (wave-42 worker), registration and inspection expiry dates, and fuel log entries (date, quantity, cost, odometer) that update odometer and fuel-consumption counters.
- **FR-44.22 Criticality assessment** — Should. The system shall assess asset criticality by a matrix of consequence categories (SAFETY, ENVIRONMENT, PRODUCTION, QUALITY, COST, each 1–5) and failure likelihood (1–5), derive the asset criticality (1–5) per BR-44.15, version assessments, and use criticality to default work order priority and plan review frequency; predictive models remain hooks (`eam.predictive-hooks`).

### 8.44.4 Data model

**Asset**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| asset_number | text(30) | yes | unique; sequence AST |
| asset_type_id | uuid | yes | FK |
| name | text(120) | yes | |
| functional_location_id | uuid | no | current location |
| parent_asset_id | uuid | no | |
| site_id | uuid | yes | wave 6 |
| serial_number, manufacturer, model | text | no | |
| criticality | smallint | yes | 1–5 |
| lifecycle_state | enum | yes | |
| production_resource_id | uuid | no | wave 22 |
| fixed_asset_id | uuid | no | wave 37 |
| warranty_until | date | no | |
| cost_dimension_set_id | uuid | no | |

**MaintenancePlan / PlanLine**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK (line) |
| plan_id | uuid | yes | FK plan (name, asset or asset type/location scope, active) |
| line_type | enum | yes | TIME, COUNTER, TIME_OR_COUNTER, TIME_AND_COUNTER |
| interval_value, interval_unit | int, enum | cond | DAYS, WEEKS, MONTHS, YEARS |
| calendar_id | uuid | no | wave 8; default 7-day |
| counter_id, counter_interval | uuid, numeric(14,2) | cond | |
| job_type_id, trade_id, variant | uuid, uuid, text | yes/no/no | |
| checklist_id | uuid | no | |
| est_hours, parts[] | numeric, jsonb | no | |
| early_tolerance_pct, late_tolerance_pct | smallint | yes | default 10/10 (SAP tolerance analog) |
| shift_factor_early, shift_factor_late | smallint | yes | 0–100%, default 0/100 |
| horizon_days, merge_window_days | int | yes | default 90 / 7 |
| last_completed_at, last_completed_reading | timestamptz, numeric | no | updated on completion |
| next_due_at, next_due_reading, next_due_forecast_at | computed | | |

**MaintWorkOrder**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK; number WO-{seq} |
| asset_id, functional_location_id | uuid | yes/no | |
| work_order_type | enum | yes | PREVENTIVE, CORRECTIVE, INSPECTION, CAPITAL, EMERGENCY |
| status | enum | yes | CREATED, SCHEDULED, RELEASED, IN_PROGRESS, ON_HOLD, COMPLETED, CLOSED, CANCELED |
| priority | smallint | yes | 1 (emergency)–5 |
| request_id, plan_line_id | uuid | no | provenance |
| due_at, planned_start, planned_end, actual_start, actual_end | timestamptz | | |
| jobs[] | WorkOrderJob | yes | (job_type, trade, est_hours, actual_hours, checklist results) |
| parts[] | WorkOrderPart | no | (item, qty_planned, qty_issued, reservation_id, cost) |
| downtime_minutes | int | no | |
| failure_symptom, failure_cause, remedy | text(20) | no | code tables |
| cost_parts, cost_labor, cost_external, cost_overhead | numeric(14,2) | computed | |
| journal_ids[] | uuid[] | | |

**Counter**: id, asset_id, name, unit, kind (CUMULATIVE/RESETTABLE), source, last_reading, last_reading_at, avg_daily_usage numeric(12,4). **CounterReading**: id, counter_id, reading_at, value, source, iot_message_id. **MaintenanceRequest**: id, asset_id, source (USER, MOBILE, IOT, QUALITY), severity, description, reading_value, status (NEW, TRIAGED, CONVERTED, REJECTED, DEFERRED), work_order_id, dedupe_key.

**MaintenanceRound**: id, code, site_id, stops[] (sequence, asset_id or functional_location_id, job_type_id, checklist_id, travel_minutes_from_previous), est_total_minutes, active. **MaintenanceSequence**: id, scope (asset_id or asset_type_id), base_interval_value, base_interval_unit or counter_id, steps[] (code, job_type_id, multiple int, hierarchy smallint, includes_step_codes[]), last_occurrence_number. **FaultCatalog**: asset_type_id, symptoms[], areas[], types[], causes[], remedies[] (code, text), allowed_combinations[] (symptom, area, type). **WorkOrderFault**: work_order_id, symptom_code, area_code, type_code, cause_code, remedy_code, notes. **AssetWarranty**: id, asset_id, warranty_type_id, vendor_party_id, start_basis (PURCHASE, INSTALL), starts_on, months, counter_id, counter_limit, parts_covered, labor_covered, exclusions[], status. **MaintenanceBudget**: id, version, scope (asset/location), period_id, source (PLAN_CALC, MANUAL), labor_amount, parts_amount, external_amount, total, wave38_plan_id. **PermitType**: code, name, checklist_id, approver_role, max_validity_hours, requires_gas_test bool. **Permit**: id (sequence EAM-PMT), permit_type_code, work_order_id, asset_id, valid_from, valid_to, status (REQUESTED, APPROVED, ISSUED, SUSPENDED, CLOSED, CANCELED), approved_by, issued_by, checklist_results[]. **IsolationPoint**: id, asset_id, energy_type (ELECTRICAL, PNEUMATIC, HYDRAULIC, MECHANICAL, THERMAL, CHEMICAL), location_text, method, verification_step, tag_code. **LotoLock**: id, permit_id, isolation_point_id, technician_worker_id, lock_number, applied_at, verified_at, removed_at. **CriticalityAssessment**: id, asset_id, version, consequence {SAFETY, ENVIRONMENT, PRODUCTION, QUALITY, COST}, likelihood, risk_score, criticality, assessed_by, assessed_at. **Asset** adds: fleet attributes (license_plate, vin, fuel_type, tank_capacity, driver_worker_id, registration_expiry, inspection_expiry) for type FLEET_VEHICLE; `rotable` bool and `condition` for serialized rotables. MaintWorkOrder adds work_order_type REFURBISHMENT and ROUND, `fault_id`, `permit_ids[]`, `warranty_covered` bool.

Invariants: cumulative counter readings are non-decreasing in time; a COMPLETED work order has actual_start ≤ actual_end; Σ work order costs = Σ linked journal lines to maintenance accounts; one open (NEW/TRIAGED) IoT request per (asset, rule) dedupe_key. Indexing: `FunctionalLocation.path ltree` GiST; `PlanLine (next_due_at)` and `(next_due_forecast_at)`; `MaintWorkOrder (site_id, status, due_at)`; `CounterReading` partitioned monthly.

### 8.44.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | `/api/v1/eam/assets` | Create asset | key | EamAssetMaintain |
| POST | `/api/v1/eam/counters/{id}/readings` | Register reading | key (source + message id) | EamCounterRegister / IoT bridge |
| POST | `/api/v1/eam/plans/schedule` | Recompute next due and generate WOs | key per (plan, run date) | EamPlanAdmin |
| POST | `/api/v1/eam/requests` | Create request | key (dedupe_key) | EamRequestCreate |
| POST | `/api/v1/eam/requests/{id}/convert` | Convert to work order | key | EamPlanner |
| POST | `/api/v1/eam/work-orders` | Create WO | key | EamPlanner |
| POST | `/api/v1/eam/work-orders/{id}/status` | Transition status | key + version | EamPlanner / EamTechnician |
| POST | `/api/v1/eam/work-orders/{id}/parts/issue` | Issue parts (inventory journal) | key | EamTechnician |
| POST | `/api/v1/eam/work-orders/{id}/labor` | Record labor | key | EamTechnician |
| GET | `/api/v1/eam/kpis?scope=&from=&to=` | MTBF/MTTR/cost | n/a | EamInquire |
| POST | `/api/v1/eam/rounds` | Create maintenance round | key | EamPlanAdmin |
| POST | `/api/v1/eam/sequences` | Create maintenance sequence | key | EamPlanAdmin |
| PUT | `/api/v1/eam/fault-catalogs/{assetTypeId}` | Maintain fault catalog | ETag | EamPlanAdmin |
| POST | `/api/v1/eam/work-orders/{id}/faults` | Register fault | key | EamTechnician |
| POST | `/api/v1/eam/budgets/calculate` | Calculate budget from plans (scope, horizon, version) | key per (scope, version) | EamBudgetAdmin |
| POST | `/api/v1/eam/permits` | Request permit for a work order | key | EamPlanner |
| POST | `/api/v1/eam/permits/{id}/transition` | Approve / issue / suspend / close permit | key + version | EamPermitIssuer |
| POST | `/api/v1/eam/permits/{id}/locks` | Apply / verify / remove LOTO lock | key per lock action | EamTechnician |
| POST | `/api/v1/eam/work-orders/refurbishment` | Create refurbishment WO for rotable serials | key | EamPlanner |
| POST | `/api/v1/eam/assets/{id}/criticality` | Record criticality assessment | key | EamAssetMaintain |
| POST | `/api/v1/eam/assets/{id}/fuel-log` | Record fuel entry (fleet) | key | EamCounterRegister |

Validation: readings for cumulative counters must be ≥ last_reading else `EAM-CTR-001` (reset requires explicit reset flag); COMPLETED requires all checklist mandatory items answered and all reserved parts issued or released; CAPITAL work orders require a fixed_asset_id or CIP project.

| Event | Trigger | Payload | Consumers |
|---|---|---|---|
| `dev.passage.eam.request.created.v1` | request created | see 8.0 | planner UI, notifications |
| `dev.passage.eam.workorder.status.changed.v1` | transition | workOrderId, from, to, at, actor | mobile, analytics |
| `dev.passage.eam.workorder.completed.v1` | COMPLETED | see 8.0 | journal, fixed assets, scheduling |
| `dev.passage.eam.counter.reading.registered.v1` | reading | counterId, assetId, value, at, source | plan scheduler, condition rules |
| `dev.passage.eam.downtime.recorded.v1` | downtime on resource | resourceId, from, to, workOrderId | scheduling (wave 23), OEE (wave 25) |
| `dev.passage.eam.permit.status.changed.v1` | permit transition | permitId, workOrderId, from, to, at, actor | mobile, safety notifications |
| `dev.passage.eam.warranty.claimable.v1` | covered WO completed | workOrderId, assetId, warrantyId, vendorPartyId, coveredParts, coveredLabor | service (FR-45.15 supplier claims) |
| `dev.passage.eam.budget.calculated.v1` | budget calculation done | budgetVersion, scope, periods[], total | budgeting (wave 38) |

### 8.44.6 User experience

| Screen | Primary actions | Key fields/columns | Duty |
|---|---|---|---|
| Asset management workspace | tiles: requests to triage, WOs due in 7 days, overdue, backlog hours | | EamInquire |
| Plant structure tree | expand locations, install/move assets, open asset | location path, asset, state, criticality | EamStructureMaintain |
| Asset detail | counters, plans, WO history, costs, KPIs, documents | | EamAssetMaintain |
| Maintenance plan editor | add line, simulate schedule 12 months, activate | type, interval, counter, next due, forecast | EamPlanAdmin |
| Request triage | convert, reject, defer, merge | severity, source, asset, age | EamPlanner |
| Work order board (kanban + Gantt) | schedule, release, assign, print | status, priority, due, assigned team | EamPlanner |
| Technician mobile (PWA) | start/stop, checklist, parts scan, labor, photos, complete | | EamTechnician |
| KPI dashboard | filter by site/type/period | MTBF, MTTR, availability, cost | EamInquire |
| Round and sequence editor | order stops, set jobs and checklists; define steps and hierarchy; simulate 12 months | stop, asset, job type, minutes; step, multiple, hierarchy | EamPlanAdmin |
| Fault catalog designer | add codes, allowed combinations per asset type | symptom, area, type, cause, remedy | EamPlanAdmin |
| Fault analysis | filter, Pareto by symptom/cause | count, downtime, cost, repeat % | EamInquire |
| Permit board | request, approve, issue, suspend, close; LOTO lock status | permit, type, WO, valid to, locks applied/verified/removed | EamPermitIssuer |
| Maintenance budget | calculate, adjust, transfer to wave 38, budget vs actual | asset/location, period, labor, parts, external, actual, variance | EamBudgetAdmin |
| Rotables and refurbishment | view serials by condition, create refurbishment WO | item, serial, condition, location, value | EamPlanner |

Rules: board drag changes planned_start with conflict warning; checklist items support numeric with tolerance (auto-flag out of range); mobile offline for up to 72 h with action replay.

### 8.44.7 Business rules and edge cases

- **BR-44.1 Plan scheduling algorithm** — run nightly and on each completion/reading event for the affected plan lines. For each active line: (a) base = last_completed_at (or plan start if none); time due `T = calendar.add(base, interval)` — if shift factors apply and completion was late/early by Δ versus the previous due, shift base by `Δ × shift_factor_late/early` (SAP IP10 analog); (b) counter due `R = last_completed_reading + counter_interval`; forecast date `F = now + (R − counter.last_reading) ÷ avg_daily_usage` (if avg_daily_usage = 0, F = NULL); (c) next_due_at = T for TIME; = F for COUNTER (next_due_reading = R); = min(T, F) for TIME_OR_COUNTER; = max(T, F) for TIME_AND_COUNTER; (d) generate a work order when `next_due_at − early_tolerance_pct × interval ≤ now + horizon_days` and no open WO exists for the line; (e) lines for the same asset due within merge_window_days are merged into one WO with multiple jobs; (f) a counter-based WO is released only when the actual reading ≥ R − tolerance or the forecast date arrives. Example: line TIME_OR_COUNTER, 90 days or 500 hours; last completed 2026-08-01 at 12,000 h; counter 12,300 h on 2026-10-21 with avg usage 6.5 h/day → T = 2026-10-30, R = 12,500, F = 2026-10-21 + 30.8 days = 2026-11-21 → next_due_at = 2026-10-30; generated on 2026-10-21 (horizon 90) with due 2026-10-30.
- **BR-44.2 Completion resets** — completion sets last_completed_at = actual_end and last_completed_reading = reading at completion (prompted if counter-based); the line reschedules immediately.
- **BR-44.3 Status transitions** — CREATED → SCHEDULED → RELEASED → IN_PROGRESS → COMPLETED → CLOSED; IN_PROGRESS ↔ ON_HOLD; any pre-COMPLETED → CANCELED (releases reservations). CLOSED requires all costs posted (no pending inventory or hour journals) and is final; cost corrections after CLOSED use a CORRECTION WO.
- **BR-44.4 Condition rules** — rule {counter or tag, operator >, <, RATE_OVER(window), value, severity, cooldown_minutes}; breach creates one request with dedupe_key `(asset, rule)` while a request is NEW/TRIAGED or within cooldown (default 60 min); latency target from reading ingest to request event p95 < 5 s.
- **BR-44.5 Downtime** — recording downtime on an asset linked to a production resource creates a capacity exception on wave-23 for [from, to]; planned downtime from SCHEDULED work orders is tentative and becomes firm at RELEASED.
- **BR-44.6 Parts costing** — issued at wave-16 running cost at issue time; returns credit at the same cost; partial issues allowed.
- **BR-44.7 Emergency WOs** — priority 1 skips SCHEDULED and may be IN_PROGRESS before RELEASED; must be regularized within 24 h (alert).
- **BR-44.8 KPIs** — MTBF = Σ uptime ÷ number of failures (CORRECTIVE/EMERGENCY WOs with failure codes) over the window; MTTR = Σ (actual_end − actual_start) of those WOs ÷ count; availability = 1 − downtime ÷ scheduled time.
- **BR-44.9 Rounds** — a ROUND plan line schedules per BR-44.1 (TIME only); one work order per occurrence with jobs in stop sequence; the work order completes when every stop is DONE or SKIPPED with a reason; completion resets the line once for the whole round; a skipped mandatory stop creates a MaintenanceRequest (source ROUND) for that asset.
- **BR-44.10 Sequence step selection** — for occurrence number k (1-based, k = last_occurrence_number + 1), candidate steps = {s : k mod s.multiple = 0}; the selected step is the candidate with the highest hierarchy; steps listed in its `includes_step_codes` are suppressed; remaining candidates not included are added as extra jobs. Example: A×1 (h1), B×4 (h2, includes A), C×12 (h3, includes A, B): k = 4 → B; k = 12 → C; k = 6 → A.
- **BR-44.11 Permits and LOTO** — a job requiring a permit type cannot move its work order to IN_PROGRESS until a permit of that type is ISSUED and valid now (`EAM-PMT-001`); approver ≠ requester; ELECTRICAL_ISOLATION and other isolation permits are ISSUED only when every isolation point of the asset has a LotoLock applied and verified (zero-energy check recorded); each technician working the job applies a personal lock (one lock per technician per point); the permit can CLOSE only when all locks are removed and the work order is COMPLETED or the work is suspended; permits past valid_to move to SUSPENDED automatically and IN_PROGRESS jobs are flagged; all transitions are append-only audit records retained 5 years.
- **BR-44.12 Refurbishment valuation** — issue of a DAMAGED serial to a refurbishment WO credits inventory at the DAMAGED condition value; WO costs (labor, parts, external) accumulate; receipt as REFURBISHED debits inventory at the REFURBISHED condition standard (or moving average) value; the difference between (damaged value + WO costs) and the refurbished value posts to the refurbishment variance account; serials keep their identity and history across conditions.
- **BR-44.13 Budget calculation** — for each active plan line, forecast occurrences in the horizon per BR-44.1 (counter lines use avg_daily_usage; usage 0 → no occurrences); amount per occurrence = est_hours × trade cost rate (wave-42/23 rate effective at the occurrence date) + Σ planned parts × standard cost + planned external amount; amounts roll up per (asset, period); sequence lines use BR-44.10 step selection; recalculation creates a new budget version and never overwrites a version transferred to wave 38.
- **BR-44.14 Warranty check** — at WO creation and at each part issue, a warranty covers the line when the asset's warranty is ACTIVE on the WO date (and counter ≤ counter_limit when set), the coverage flag matches the line kind, and the job type/part is not in exclusions; covered lines still cost the WO, and a warranty claimable event is emitted at completion with covered amounts.
- **BR-44.15 Criticality** — risk_score = max(consequence categories) × likelihood (1–25); criticality = 5 when risk ≥ 20 or SAFETY = 5, 4 when ≥ 15, 3 when ≥ 10, 2 when ≥ 5, else 1; a new assessment version replaces asset.criticality; work orders default priority = 6 − criticality for CORRECTIVE types; assets with criticality ≥ 4 require plan review every 12 months (reminder).

### 8.44.8 Acceptance criteria

- **AC-44.1** (FR-44.5, BR-44.1) Given the BR-44.1 example, when the scheduler runs on 2026-10-21, then a WO exists with due 2026-10-30 and next_due_reading 12,500.
- **AC-44.2** (FR-44.5) Given 40 plan fixtures, when scheduled across a simulated year, then generated WO dates/readings match expected within 0 days / 0 units.
- **AC-44.3** (FR-44.11) Given a completed WO with 350.00 USD parts and 8 h labor at 45.00/h, when posted, then the GL maintenance expense is 710.00 and the asset cost history shows 710.00.
- **AC-44.4** (FR-44.12, BR-44.4) Given a bearing temperature rule > 85 °C, when a reading of 87 °C arrives, then a request exists within 5 s and a second reading of 88 °C within 60 min creates none.
- **AC-44.5** (FR-44.8, BR-44.5) Given a RELEASED WO with planned downtime 4 h on resource M-12, when wave-23 schedules, then M-12 capacity for that window is zero.
- **AC-44.6** (FR-44.3) Given a cumulative counter at 12,300, when a reading of 12,250 is posted without reset flag, then it is rejected with EAM-CTR-001.
- **AC-44.7** (FR-44.7, BR-44.3) Given a WO with an unanswered mandatory checklist item, when COMPLETED is requested, then it is rejected with `EAM-WO-004`.
- **AC-44.8** (FR-44.13, BR-44.8) Given 3 failures and 2,700 h uptime in a window, when KPIs are queried, then MTBF = 900 h.
- **AC-44.9** (FR-44.14, BR-44.9) Given a weekly round of 6 stops, when the occurrence WO is completed with stop 4 SKIPPED on a mandatory job, then the round line resets once and a MaintenanceRequest with source ROUND exists for stop 4's asset.
- **AC-44.10** (FR-44.15, BR-44.10) Given sequence A×1, B×4 (includes A), C×12 (includes A, B), when occurrences 1–12 generate, then jobs are A, A, A, B, A, A, A, B, A, A, A, C.
- **AC-44.11** (FR-44.19, BR-44.11) Given a WO whose job requires ELECTRICAL_ISOLATION on an asset with 3 isolation points, when IN_PROGRESS is requested with 2 verified locks, then it is rejected with `EAM-PMT-002`; with 3 verified locks and an ISSUED permit it succeeds; closing the permit with one lock still applied returns `EAM-PMT-003`.
- **AC-44.12** (FR-44.20, BR-44.12) Given a DAMAGED pump serial valued 400.00 USD, WO costs 650.00 and REFURBISHED standard 1,000.00, when the serial is received REFURBISHED, then inventory shows the serial at 1,000.00 and the refurbishment variance account carries 50.00 debit.
- **AC-44.13** (FR-44.17, BR-44.14) Given an asset with a 24-month parts-only vendor warranty started 2025-11-01, when a part is issued to a WO on 2026-10-21, then the part line is warranty-covered, labor is not, and completion emits `eam.warranty.claimable.v1` with the part amount.
- **AC-44.14** (FR-44.18, BR-44.13) Given a TIME plan line every 90 days with 6 h at 45.00/h and parts 120.00, when a 12-month budget calculates from 2027-01-01 with last completion 2026-12-15, then 4 occurrences budget 390.00 each (1,560.00 total) in the periods of 2027-03-15, 2027-06-13, 2027-09-11 and 2027-12-10.
- **AC-44.15** (FR-44.16) Given a CORRECTIVE WO with no fault registered, when COMPLETED is requested, then it is rejected with `EAM-FLT-001`; a symptom/area/type combination not allowed for the asset type returns `EAM-FLT-002`.
- **AC-44.16** (FR-44.22, BR-44.15) Given consequences SAFETY 3, PRODUCTION 5 and likelihood 4, when assessed, then risk_score = 20, criticality = 5 and a new CORRECTIVE WO defaults to priority 1.

### 8.44.9 Test plan and fixtures

Fixtures: `eam-plant-demo` (3 sites, 120 locations, 400 assets, 60 counters from wave-22 resources); `eam-plans-40` with a 365-day expected generation calendar; `eam-iot-stream` (10 readings/s replay from wave-25 fixture); `eam-wo-cost-50`.

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Scheduler | unit/property | next-due computation | matches calendar; monotonic under completions |
| WO generation | E2E | nightly job | 40/40 plans |
| IoT → request | performance | condition engine | p95 < 5 s at 10 readings/s |
| Cost posting | contract | posting API | balanced; asset rollup equal |
| Downtime → capacity | integration | scheduling API | capacity reflects downtime |
| Mobile offline | E2E (Playwright, offline mode) | PWA | 72 h replay without loss |

Reconciliation: Σ WO costs by account = GL maintenance accounts; Σ parts issued = inventory transactions with reference WO.

### 8.44.10 Deployment and operations

Chart `passage-eam` (Go API, scheduler worker, condition engine consumer, mobile BFF); Postgres `eam` with ltree and monthly partitions on readings. Flags: `eam.capital-wo`, `eam.predictive-hooks`. Schedules: `eam-plan-schedule` nightly 01:00 site-local plus event-driven; `eam-kpi-rollup` hourly. SLOs: condition engine p95 < 5 s; WO status API p95 < 200 ms; availability 99.9%. Alerts: scheduler run failure; condition consumer lag > 30 s; emergency WO not regularized in 24 h. Runbook: replay readings from Kafka by offset; re-run scheduler for a plan. Rollback by digest.

### 8.44.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| EntAssetFunctionalLocation | FunctionalLocation | |
| EntAssetObject, EntAssetObjectType | Asset, AssetType | lifecycle state map |
| EntAssetCounter, EntAssetObjectCounter | Counter, last reading | |
| EntAssetMaintenancePlan (+ lines) | MaintenancePlan, PlanLine | last completed from WO history |
| EntAssetWorkOrder (open) | MaintWorkOrder | closed as history |
| EntAssetMaintenanceRequest (open) | MaintenanceRequest | |
| IFLOT/IFLOTX | FunctionalLocation | TPLNR structure indicator → path |
| EQUI/EQKT/EQUZ | Asset | EQUZ for location/time slices |
| MPLA/MPOS/MHIS | MaintenancePlan, PlanLine, last call | |
| IMPTT/IMRG | Counter, CounterReading (last 12 months) | |
| QMEL (open), AUFK/AFIH PM orders (open) | MaintenanceRequest, MaintWorkOrder | |

Validation: asset count per site; open WO count and planned cost equal; next due dates within tolerance of source IP30 output. Not migrated: closed orders older than 3 years (archive), SAP strategy packages (converted to explicit lines).

### 8.44.12 Dependencies, risks, open questions

Depends on: wave 16 inventory, wave 17 PO, wave 22 resources, wave 23 scheduler, wave 25 IoT, wave 37 fixed assets, wave 42 workers. Coverage-closure additions also depend on: wave-16 inventory condition dimension with cost per condition (FR-44.20 refurbishment; if wave 16 ships without it, refurbishment runs as a valuation-adjustment journal until wave 16 is extended), wave 38 budget plans (FR-44.18), wave 42 workers for permit roles and fleet drivers (FR-44.19, FR-44.21), and the wave-45 supplier warranty claim (FR-45.15) consuming `eam.warranty.claimable.v1`.

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Counter forecast inaccuracy | Medium | Low | Rolling usage; manual override |
| IoT volume | Medium | Medium | Partitioned readings; downsampled retention 13 months |

Open questions: (1) Capitalize CAPITAL WOs directly to fixed assets or always via CIP project? Default CIP project; decide by wave 44. (2) Calibration in v1? Default no.

### 8.44.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| Asset / WO / request / permit number sequences | site | lookup | AST, WO, MR, EAM-PMT | wave-5 sequences | Identifiers | Asset management parameters › Number sequences (EntAssetParameters) | IW order number ranges (OION / T003O), notification ranges (OIN1/TQ80), equipment ranges (OIEN) |
| Work order types enabled | site | multi-enum | PREVENTIVE, CORRECTIVE, INSPECTION, EMERGENCY | + CAPITAL, ROUND, REFURBISHMENT | FR-44.7 | Asset management parameters › Work orders › Work order types | Order types PM01–PM04 (T003O, T350, T399X) |
| Request types and auto-convert priority | site | lookup + int | auto-convert priority 1 | 1–5 | FR-44.6 | Maintenance request types (EntAssetRequestType) | Notification types M1/M2/M3 (TQ80); notification-order type link (T399A) |
| Plan generation horizon / merge window | plan line | int | 90 / 7 days | 1–730 / 0–60 | BR-44.1 (d), (e) | Asset management parameters › Maintenance plans › Schedule horizon | IP10 call horizon, scheduling period (MPLA, T399W plan category) |
| Early/late tolerance and shift factors | plan line | pct | 10/10; 0/100 | 0–100 | BR-44.1 (a) | Maintenance plan line tolerances | Scheduling parameters: shift factor late/early, tolerance (IP42 / MPLA) |
| Counter avg-usage window | counter | int days | 30 | 7–365 | FR-44.3 forecast | Counter setup (EntAssetCounterType) | IK11 estimated annual performance (IMPTT) |
| Counter reading tolerance for release | plan line | pct | 5 % | 0–50 | BR-44.1 (f) | n/a | Call horizon % for performance-based plans |
| Default criticality → priority map | site | table | 6 − criticality | 1–5 | BR-44.15 | Criticality setup | Priority type PM (T356) |
| Fault registration required on completion | work order type | bool | true for CORRECTIVE, EMERGENCY | | FR-44.16 | Asset management parameters › Fault registration mandatory | Catalog profile mandatory on notification (OIS2, T352B) |
| Permit required by job type / asset type | job type / asset type | multi-lookup | — | PermitType | BR-44.11 | n/a | Permit categories (T357G) assigned to equipment/FL; WCM work clearance usage (WCA*) |
| Permit max validity (hours) | permit type | int | 12 | 1–168 | BR-44.11 auto-suspend | n/a | WCM approval validity |
| Emergency WO regularization window | site | int hours | 24 | 1–72 | BR-44.7 | n/a | Order type "immediate order" settings |
| Mobile offline limit | tenant | int hours | 72 | 8–168 | 8.44.6 rule | Mobile app (D365 Field) settings | SAP Asset Manager offline settings |
| Parts issue at release vs on demand | site | enum | ON_DEMAND | AT_RELEASE, ON_DEMAND | FR-44.9 | Work order item requirements | Reservation relevance / goods movement on release (T399X) |
| Maintenance expense posting key | site | lookup | (asset type, job type, FL dimension) | posting profiles | FR-44.11 | Asset management parameters › Journals / project link | Settlement profile (OKO7) and default settlement rule (KO88 replacement) |
| Condition valuation for rotables | item group | bool + table | false | conditions NEW/REFURBISHED/DAMAGED with value basis | BR-44.12 | n/a | Split valuation by condition (OMWC, T025 valuation types) |
| Budget calculation horizon and version | site | int months + text | 12, "BUD-{year}" | 1–60 | BR-44.13 | Maintenance budget parameters | IP30/IP31 cost simulation horizon |
| Warranty check on WO create | site | bool | true | | BR-44.14 | Warranty parameters › Check on work order | Warranty check on order (IW31 warranty indicator, BGMK) |
| Condition rule cooldown default | rule | int minutes | 60 | 1–1,440 | BR-44.4 | n/a | n/a (ASPM / IoT rules) |

### 8.44.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Work order list | list inquiry | site, status, type, priority, due range, team, asset/location | WO, asset, status, due, planned/actual dates, est/actual hours, cost | `eam_wo` | All work orders (EntAssetWorkOrderListPage) | IW38 change / IW39 display orders |
| Request list | list inquiry | status, source, severity, age | request, asset, source, severity, age, WO | `eam_req` | Maintenance requests (EntAssetRequestTable) | IW28 / IW29 notifications |
| Scheduling overview | operational report | plan, date range | plan line, next due, forecast, generated WO, status | `eam_schedule` | Maintenance schedule (EntAssetMaintenanceSchedule) | IP24 scheduling overview list; IP19 scheduling overview graphic |
| Asset structure and history | drill-down | site, location path, type | asset, location history, WO history, counters | `eam_structure` | Functional location / asset views | IH01 structural display; IH06 FL list; IH08 equipment list |
| Measurement documents | list inquiry | counter, period, source | readings, deltas, source | `eam_readings` | Counter registrations | IK17 measurement documents list |
| Cost per asset | drill-down | asset, type, period | parts, labor, external, overhead, total vs budget | `eam_cost` | Asset cost (EntAssetObjectCost) | MCI3 cost analysis (PMIS); KOB1 order line items |
| MTBF / MTTR / availability | KPI | site, type, criticality, window | MTBF, MTTR, availability, failures | `eam_kpi` | Asset KPIs | MCI7 breakdown statistics; MCI1 object statistics |
| Fault analysis | KPI | asset type, symptom, cause, period | count, downtime, cost, repeat rate 30 days, Pareto | `eam_faults` | Fault analysis; SSRS Fault report | IW69 notification items; MCI5 damage analysis |
| Backlog | KPI | site, trade, priority | open WOs, backlog hours, weeks of backlog | `eam_backlog` | Work order backlog | IW38 with capacity; CM01 capacity load |
| Budget vs actual | drill-down | scope, version, period | budget, actual, committed, variance | `eam_budget` | Budget vs actual (maintenance) | IP31 cost simulation vs KOB1 actuals; S_ALR_87013611 |
| Warranty inquiry | list inquiry | vendor, expiring within, asset type | asset, warranty, vendor, ends, covered cost YTD | `eam_warranty` | Warranty inquiry (EntAssetWarranty) | BGM3 display warranty; IE03 warranty tab |
| Permit register | list inquiry | status, type, site, valid range | permit, WO, asset, valid from/to, locks open | `eam_permits` | n/a | WCM work clearance document lists (WCM_WCD) |
| Round compliance | KPI | round, period | occurrences, stops done, skipped %, on-time % | `eam_rounds` | Maintenance rounds results | IW38 for object-list orders |
| Rotables by condition | list inquiry | item, condition, location | serial, condition, value, refurbishment count | `eam_rotables` | n/a | MMBE / MB52 split valuation stock; IW8W history |
| Fuel consumption (fleet) | KPI | vehicle, driver, period | liters/gallons, cost, distance, consumption per 100 km / mpg | `eam_fuel` | n/a | MCIZ fleet consumption analysis |

### 8.44.15 Printed and electronic documents

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Work order package | WO RELEASED (print from board) | header: WO, asset, location, priority, due, permits required; jobs: instructions, checklist, safety notes; parts pick list; labor sign-off; footer: QR code to mobile | PDF, mobile | WO | maintenance record: life of asset + 3 years; regulated assets per country (e.g. pressure vessels DE BetrSichV records) |
| Permit to work | permit ISSUED | permit type, WO, asset, hazards, isolation points with lock numbers, gas test results, validity, signatures (requester, approver, issuer, each technician) | PDF (posted at worksite), mobile | EAM-PMT | OSHA 29 CFR 1910.147 periodic inspection certification records; retain 5 years |
| LOTO lock tag | lock applied | technician name, lock number, point, date/time, contact | label PDF (thermal printer) | lock number | per 29 CFR 1910.147(c)(5) |
| Round checklist | round WO released | ordered stops, checklist items, readings fields | PDF, mobile | WO | maintenance record |
| Maintenance request acknowledgment | request created (user/mobile) | request no, asset, description, status link | email, notification | MR | none |
| Refurbishment shipment to repair vendor | external refurbishment step | serials, condition, vendor PO reference | PDF with wave-28 shipment / wave-17 PO | wave-17 PO | per wave 17/28 |
| Fault report | on demand | fault Pareto per filter | PDF, XLSX | none | none |
| Maintenance budget | version transferred | scope × period budget lines | XLSX, PDF | budget version | budget record |

### 8.44.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| `eam-plan-schedule` | nightly 01:00 site-local + on completion/reading events | site, plan line | key per (plan line, run date); open-WO check prevents duplicates | < 5 min for 10,000 lines | per-line retry; failures listed | run failure; lines with next_due in past and no WO |
| `eam-condition-engine` | streaming (Kafka consumer) | asset hash | dedupe_key + cooldown | p95 < 5 s per reading | consumer restart from offset | consumer lag > 30 s |
| `eam-kpi-rollup` | hourly | site | rollup per (site, hour) replace | < 2 min | retry | stale > 3 h |
| `eam-permit-expiry` | every 5 min | site | key (permit, transition) | < 10 s | retry | permits ISSUED past valid_to |
| `eam-emergency-regularize-check` | hourly | site | key (WO, hour) | < 10 s | retry | emergency WO not regularized within window |
| `eam-budget-calculate` | on demand; annual budget cycle | site, version | key (scope, version) | < 10 min for 10,000 lines × 12 months | resumable per site | calculation failed |
| `eam-warranty-expiry` | daily 06:00 | site | key (warranty, lead day) | < 1 min | retry | none |
| `eam-readings-retention` | monthly | counter partition | drop/downsample partitions older than 13 months | < 30 min | retry next day | partition count over budget |

### 8.44.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles that include it |
|---|---|---|---|
| EamStructureMaintain / EamAssetMaintain | functional locations, assets, warranties, criticality; Plant structure tree, Asset detail | none | Maintenance engineer |
| EamPlanAdmin | plans, rounds, sequences, fault catalogs, schedule run | none | Maintenance planner (senior) |
| EamPlanner | requests convert, WO create/schedule/release, permits request, refurbishment WOs | with EamPermitIssuer on the same permit | Maintenance planner, Supervisor |
| EamTechnician | WO status (IN_PROGRESS/COMPLETED), parts issue, labor, faults, LOTO lock actions; Technician mobile | with EamPermitIssuer approving a permit for own job | Technician |
| EamPermitIssuer | permit approve/issue/suspend/close | requester ≠ approver; issuer cannot be the only lock holder | Area authority, Safety officer |
| EamCounterRegister | readings, fuel log | none | Operator, IoT bridge service account |
| EamBudgetAdmin | budget calculate, adjust, transfer | with BudgetApprove (wave 38) on the same plan | Maintenance manager |
| EamInquire | all GET reports and KPIs | none | Maintenance manager, Plant manager |
| EamSetupAdmin | parameters 8.44.13, permit types, isolation points, posting keys | with EamPermitIssuer | EAM system administrator |

### 8.44.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| EAM-CTR-001 | error | Reading {v} is below last reading {last} for cumulative counter {c} | Non-monotonic | Set the reset flag or correct the value | FR-44.3 |
| EAM-AST-001 | error | Asset {a} is {state}; work orders need ACTIVE | Lifecycle | Activate or use override duty | FR-44.2 |
| EAM-FL-001 | error | Functional location depth exceeds 12 | Hierarchy limit | Flatten the structure | FR-44.1 |
| EAM-PLN-001 | warning | Counter {c} has zero average usage; forecast unavailable | No usage | Register readings or set manual usage | BR-44.1 |
| EAM-PLN-002 | info | {n} work orders generated, {m} merged | Scheduler result | None | FR-44.5 |
| EAM-WO-001 | error | Transition {from} → {to} not allowed | State machine | Use an allowed transition | BR-44.3 |
| EAM-WO-002 | error | Work order cannot CLOSE: {n} pending journals | Costs not posted | Post or cancel journals | BR-44.3 |
| EAM-WO-003 | warning | Emergency work order {wo} not regularized within {h} h | BR-44.7 | Complete planning fields | BR-44.7 |
| EAM-WO-004 | error | Mandatory checklist item {i} unanswered | Completion check | Answer the item | 8.44.5 validation |
| EAM-WO-005 | error | CAPITAL work order needs a fixed asset or CIP project | Validation | Link a fixed asset or project | FR-44.11 |
| EAM-FLT-001 | error | Register a fault before completing {type} work orders | Fault mandatory | Add symptom/cause/remedy | FR-44.16 |
| EAM-FLT-002 | error | Combination {symptom}/{area}/{type} not allowed for {assetType} | Catalog | Choose an allowed combination | FR-44.16 |
| EAM-PMT-001 | error | Job requires an ISSUED {permitType} permit | No valid permit | Request and issue the permit | BR-44.11 |
| EAM-PMT-002 | error | {n} isolation points lack a verified lock | LOTO incomplete | Apply and verify locks | BR-44.11 |
| EAM-PMT-003 | error | Permit cannot close: {n} locks still applied | Locks open | Remove locks | BR-44.11 |
| EAM-PMT-004 | warning | Permit {p} expired and was suspended | Validity passed | Revalidate or reissue | BR-44.11 |
| EAM-PMT-005 | error | Approver must differ from requester | SoD | Another approver | BR-44.11 |
| EAM-REF-001 | error | Serial {s} is not in condition DAMAGED | Refurbishment input | Select a damaged unit | BR-44.12 |
| EAM-WAR-001 | info | Line covered by warranty {w} ({vendor}) | Warranty check | Claim created at completion | BR-44.14 |
| EAM-BUD-001 | error | Budget version {v} already transferred; create a new version | Immutability | Recalculate as new version | BR-44.13 |
| EAM-IOT-001 | info | Duplicate breach suppressed for {asset}/{rule} | Dedupe/cooldown | None | BR-44.4 |

### 8.44.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| IoT reading → request | 10 readings/s per site, 30 sites; peak 1,000/s | 1 / 5 / 8 s | 1,000 readings/s | CounterReading ≈ 9.5 B rows raw → downsampled to 1/min after 30 days ≈ 200 GB | monthly partitions; (counter_id, reading_at) | evaluate thresholds on every reading, rate-of-change rules sampled at 1/s; lag alert |
| Plan scheduling (nightly) | 10,000 plan lines per tenant | run < 5 min; per line 5 / 20 / 50 ms | 100 lines/s | negligible | PlanLine (next_due_at), (next_due_forecast_at) | event-driven part continues; nightly sweep extends |
| WO status transition (mobile) | 20,000 transitions/day; peak 300/min | 50 / 200 / 400 ms | 50/s | WO ≈ 2 KB × 150k/year = 300 MB | (site_id, status, due_at) | mobile action log buffers; server applies in order |
| Work order board load | 200 planners, 2,000 open WOs per site | 300 / 800 / 1,500 ms | 20 loads/s | n/a | projection per site | Gantt lazy-loads by week |
| Permit transition | 1,000/day | 80 / 250 / 500 ms | 10/s | permits + locks ≈ 100 MB | (work_order_id), (status, valid_to) | none permitted: permit checks fail closed |
| Budget calculation | 10,000 lines × 12 months | < 10 min | 20 lines/s per worker | MaintenanceBudget ≈ 120k rows/version | (version, scope, period) | longer run |
| KPI query | 2,000/day | 100 / 400 / 900 ms | 20 q/s | rollups ≈ 2 GB | hourly rollup tables | serve last rollup |

### 8.44.20 Localization and compliance hooks

- Lockout/tagout: US OSHA 29 CFR 1910.147 (energy control procedures, periodic inspection at least annually, lock per authorized employee), EU Directive 2009/104/EC and DE BetrSichV / TRBS 1112 (Instandhaltung), UK PUWER 1998; permit types and checklists are pack templates per country; extension point `eam.permit.template.<cc>`.
- Statutory inspections: pressure equipment (EU PED, DE BetrSichV §§15–16 intervals), lifting equipment (UK LOLER 6/12 months), fire systems (NFPA 25), vehicle inspection (DE HU §29 StVZO, US DOT 49 CFR 396.17 annual) delivered as plan templates with mandatory checklists by the wave-48 packs.
- Environmental: refrigerant leak checks (EU F-gas Regulation 2024/573 intervals by CO2-equivalent; US EPA Section 608 for ≥ 50 lb appliances) as plan templates using counter "refrigerant charge".
- Records retention: maintenance and inspection records retained for the asset life plus country-specific periods (US OSHA 1910.147 certification records; DE BetrSichV records until next inspection; aviation/rail regulated sectors out of scope).
- Units: counters support metric and US customary units with wave-8 UoM conversion; temperature thresholds stored in SI with display conversion.
- Fleet: fuel tax and mileage logs for company vehicles (DE Fahrtenbuch per §6 EStG; US IRS Pub 463 substantiation) use fuel log and odometer counters; wave-48 packs format the log.
- Language: checklists, permit texts and LOTO procedures are translatable (wave-3 labels); permits print in the site language plus English when the site parameter requests a second language.

### 8.44.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX #299 — 12.35.1 Maintenance rounds (EntAssetMaintenanceRound) | PARTIAL | ADDED as FR-44.14 | BR-44.9 |
| AX #300 — 12.35.1 Maintenance sequences (EntAssetMaintenanceSequence) | PARTIAL | ADDED as FR-44.15 | BR-44.10 |
| AX #301 — 12.35.1 Work order fault registration (EntAssetFault) | PARTIAL | ADDED as FR-44.16 | WorkOrderFault |
| AX #302 — 12.35.1 Warranties (EntAssetWarranty, warranty check on WO) | PARTIAL | ADDED as FR-44.17 | BR-44.14; vendor claim in FR-45.15 |
| AX #303 — 12.35.1 Maintenance budgets (EntAssetMaintenanceBudget) | PARTIAL | ADDED as FR-44.18 | BR-44.13 |
| AX #304 — 12.35.2 Fault analysis | PARTIAL | ADDED as FR-44.16 | "Fault analysis" in 8.44.14 |
| AX #305 — 12.35.2 Warranty inquiry | PARTIAL | ADDED as FR-44.17 | "Warranty inquiry" |
| AX #306 — 12.35.2 Budget vs actual (maintenance) | PARTIAL | ADDED as FR-44.18 | "Budget vs actual" |
| AX #307 — 12.35.2 SSRS Fault report | PARTIAL | ADDED as FR-44.16 | Document in 8.44.15 |
| AX #308 — 12.35.3 Schedule maintenance rounds / sequences | PARTIAL | ADDED as FR-44.14 | Covered by `eam-plan-schedule` with ROUND and sequence lines (also FR-44.15) |
| AX #309 — 12.35.3 Maintenance budget calculation | PARTIAL | ADDED as FR-44.18 | Job `eam-budget-calculate` |
| AX #310 — 12.35.4 Plan types, plan line defaults, rounds setup, sequences setup | PARTIAL | ADDED as FR-44.14 | Parameters in 8.44.13; sequences setup via FR-44.15 |
| AX #311 — 12.35.4 Fault symptoms, areas, types, causes, remedies, fault designer | PARTIAL | ADDED as FR-44.16 | Fault catalog designer screen |
| AX #312 — 12.35.4 Warranty types, vendor warranty setup | PARTIAL | ADDED as FR-44.17 | AssetWarranty.warranty_type_id |
| AX #313 — 12.35.4 Maintenance budget parameters | PARTIAL | ADDED as FR-44.18 | Parameter "Budget calculation horizon and version" |
| SAP #297 — 13.25 IE31/IE36/IE37 fleet objects | PARTIAL-44 | ADDED as FR-44.21 | Fuel log and odometer counters |
| SAP #298 — 13.25 IW81/IW8W refurbishment order / GR for refurbishment | PARTIAL-44 | ADDED as FR-44.20 | BR-44.12; needs wave-16 condition dimension (flagged in 8.44.12) |
| SAP #299 — 13.25 IW32 permits (IPMD) / WCM lockout/tagout | PARTIAL-44 | ADDED as FR-44.19 | BR-44.11 |
| SAP #300 — 13.25 Asset Intelligence Network / Predictive Asset Insights / ASPM | PARTIAL-44 | ADDED as FR-44.22 | Criticality/RCM-lite here; predictive ML OUT-OF-SCOPE for v1 (hooks only; models candidate for wave 47); AIN manufacturer network OUT-OF-SCOPE |
| SAP #301 — 13.25 SPRO refurbishment valuation types (OMWC), order type PM04 | PARTIAL-44 | ADDED as FR-44.20 | Parameter "Condition valuation for rotables"; WO type REFURBISHMENT |
| SAP #302 — 13.25 SPRO permits (T357G), WCM objects/applications/approvals | PARTIAL-44 | ADDED as FR-44.19 | PermitType, IsolationPoint setup |

## 8.45 Wave 45: Service Management & Field Service

### 8.45.1 Scope and business context

Service management replaces AX Service management (SMAServiceAgreementTable, SMAServiceOrderTable, SMAServiceObjectTable, SMADispatchBoard, SMARepair, service subscriptions), D365 Field Service (work orders, bookings, agreements, IoT alerts, mobile) and SAP CS/FSM (service notifications, service orders, IBase, warranties, SAP Field Service Management). After this wave: an installed base is built from shipped serials; agreements define entitlements, SLAs and pricing; cases become service work orders; a dispatcher schedules technicians with OR-Tools respecting skills, territories, time windows and travel; technicians execute offline on a PWA with photos, signatures and truck-stock parts; billing posts to the customer or warranty. This is the phase-exit wave.

Out of scope: customer-facing self-scheduling portal (v1.x), subscription billing beyond wave-29 recurring invoices, remote assist/AR, depot repair RMA beyond wave-30 RMA linkage.

Coverage closure (8.45.21) adds the cross-module case framework that earlier waves deferred to this wave: generic case management with categories, processes, activities and associations to any record (FR-45.12), a knowledge base of symptom/solution articles (FR-45.13), activities on cases, projects and other records (FR-45.14), supplier warranty claims (FR-45.15) and email-to-case intake (FR-45.16). Telephony/CTI and omnichannel contact-center routing remain out of scope (CRM non-goal).

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| AX SMADispatchBoard is a manual Gantt: a dispatcher drags each SMAServiceOrderTable row by hand, with no skill or travel check; SAP FSM/RSO optimization is a separate licensed product with its own master data sync | No optimizer in the ERP; duplicated technician master | OR-Tools CP-SAT dispatch inside the service domain using wave-42 skills and wave-16 truck stock (BR-45.3) | Dispatch time per territory-day 2–3 h → < 30 s solve + 10 min review; skill/window violations 0 |
| Entitlement: AX service orders pick the agreement the user chooses (SMAServiceOrderTable.ServiceAgreementId) with no SLA clock; SAP CS warranty check (IW31 "warranty" pop-up) is informational and SLA profiles live in CRM only | No deterministic entitlement or SLA engine in ERP | Resolver (BR-45.1) and business-hour SLA clocks with pause states and breach events (BR-45.2) | Wrong-agreement billing 0 on fixtures; SLA breaches discovered after the fact → warning at 75% |
| Installed base built by hand: AX service objects created manually after shipment; SAP IBase needs IB51/IB52 maintenance or a custom user exit from delivery | No event from shipment to service | Service objects created from wave-28 serialized shipment events with warranty start | Manual IBase upkeep 100% → 0; serial-to-object lag days → < 60 s |
| Technician execution on paper or a disconnected app; parts used from vans re-keyed into inventory journals days later, causing negative stock and wrong COGS | No mobile execution or truck-location model | Offline PWA with append-only action log; TRUCK locations with consumption at completion | Parts re-keying 0; inventory accuracy on trucks ≥ 99%; completion-to-invoice 5–10 days → same day |
| Service billing in AX goes through project invoice proposals (service orders post to ProjEmplTrans/ProjItemTrans), a second module to learn; SAP DP90 again | Service reuses project billing plumbing | Work order lines carry coverage (BILLABLE/WARRANTY/AGREEMENT) and post directly to wave-29 invoicing | Billing steps 6 → 1; warranty-vs-billable misclassification 0 on 30 fixtures |

### 8.45.2 AX / SAP parity map

| Capability | AX / D365 | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Installed base / service objects | SMAServiceObjectTable, SMAServiceObjectRelationTable; D365 FS customer assets | IBase (IBIN/IBIB), equipment with customer, serial numbers (SERI) | `ServiceObject` from wave-28 shipment serials | full |
| Warranties | Limited (SMAServiceObject + agreement) ; D365 FS warranties | Warranty master (BGMK), warranty check on order | `Warranty` (parts/labor/travel, start trigger) | improved |
| Service agreements | SMAServiceAgreementTable/Line, SMAAgreementLine, service intervals | Service contracts (VA41, VBAK type WV/SC), SLA profiles | `ServiceAgreement`, `Entitlement`, `SlaProfile` | full |
| Cases/notifications | smmCase (CRM case), D365 FS cases | QMEL service notifications (IW51), CRM cases | `Case` | full |
| Service orders | SMAServiceOrderTable/Line, stages | Service orders (IW31 type SM01/SM02), CS order | `ServiceWorkOrder` | full |
| Dispatch board | SMADispatchBoard (Gantt), SMAServiceTechnician | SAP FSM planning board / Resource Scheduling Optimizer; CRM MRS | `dispatch` service (OR-Tools CP-SAT) + board | improved |
| Technician mobile | D365 FS mobile (Power Apps) | SAP FSM mobile | technician PWA | full |
| Parts from trucks | D365 FS warehouse per resource | Van stock (storage location per technician) | wave-16 location type TRUCK per technician | full |
| Billing | SMAServiceOrder → project invoice (service orders post via projects) | DP90 to billing, warranty claim | `ServiceWorkOrder` → wave-29 invoice; WARRANTY lines no-charge | full |
| KPIs | Service management reports | CS reports | SLA attainment, first-time fix, utilization | improved |
| Generic case management | CaseDetail, CaseCategory (category types: HR, customer service, collections, product change, audit, service, project…), CaseProcess (stages), case associations, case category security | QMEL notifications by type, S/4 Service cases, CRM case management (CRMD_ORDER case), dispute cases (UDM) | `Case` with `CaseCategory`, `CaseProcess`, `CaseAssociation` (FR-45.12) | full |
| Knowledge base | CaseKnowledgeArticle (knowledge articles on cases) | Solution database IS01/IS02 (symptoms/solutions), CRM knowledge articles | `KnowledgeArticle` (FR-45.13) | full |
| Activities | smmActivities (tasks, appointments) on cases and projects | Notification tasks/activities (QMSM/QMMA), CRM activities | `Activity` (FR-45.14) | full |
| Supplier warranty claims | n/a | WTY warranty claim processing (WTY1, claim types, vendor recovery) | `SupplierWarrantyClaim` (FR-45.15) | full |
| Front office intake | Case via email (manual) | CIC0 customer interaction center, CRM IC email | email-to-case via wave-35 hub (FR-45.16) | partial — no CTI/telephony |
| Service subscriptions | SMASubscriptionTable, fee transactions, indexation | Service contract billing plans (VA42 billing plan), periodic billing | wave-29 subscription billing with agreement billing_model SUBSCRIPTION | improved — single engine |

### 8.45.3 Functional requirements

- **FR-45.1 Installed base** — Must. The system shall create service objects automatically from wave-28 shipments of serialized items (and manually), with customer, site address, parent/child structure, install date and current status.
- **FR-45.2 Warranties** — Must. The system shall attach warranty terms (parts/labor/travel coverage flags, duration from SHIP/INSTALL/REGISTRATION, exclusions) to service objects and evaluate coverage per work order line.
- **FR-45.3 Service agreements** — Must. The system shall define agreements per customer with lines covering service objects or categories, entitlements (included visits/hours, discounts, price lists), SLA profile, validity and billing (prepaid, per-visit, subscription via wave 29).
- **FR-45.4 SLA profiles** — Must. The system shall define response and resolution targets by priority with business-hour calendars and pause states per BR-45.2.
- **FR-45.5 Cases** — Must. The system shall capture cases (phone, email, portal, IoT from wave 44/25) and resolve entitlement to the best agreement per BR-45.1, starting the SLA clock.
- **FR-45.6 Service work orders** — Must. The system shall create work orders from cases or agreements (preventive visits) with incident type, required skills, estimated duration, time window, service object, address, and lines (labor, parts, travel, fees) each billable, covered-by-warranty or covered-by-agreement.
- **FR-45.7 Dispatch optimization** — Must. The system shall produce daily schedules per territory with OR-Tools CP-SAT satisfying BR-45.3 constraints, with manual override on the board and incremental re-optimization on changes.
- **FR-45.8 Technician mobile PWA** — Must. The system shall provide an offline-capable PWA: today's route, navigate, start travel/work, checklists, photos, parts from truck stock, signature capture, customer-facing summary.
- **FR-45.9 Truck stock** — Must. The system shall treat each technician's vehicle as a wave-16 location (type TRUCK) with replenishment transfer orders and consumption on work order completion.
- **FR-45.10 Billing** — Must. The system shall post completed work orders to wave-29 invoicing: billable lines at agreement/standard prices, warranty lines at zero with a warranty cost record, agreement-covered lines consuming entitlement.
- **FR-45.11 Service KPIs** — Should. The system shall compute SLA attainment, first-time-fix rate, mean time to respond/resolve, technician utilization and travel ratio.
- **FR-45.12 Generic case management** — Must. The system shall generalize `Case` to any business domain through case categories (hierarchical, with category type SERVICE, CUSTOMER_SERVICE, HR, COLLECTIONS, PRODUCT_CHANGE, AUDIT, PROJECT, VENDOR, INTERNAL), case processes (ordered stages with entry/exit criteria and required activities), associations to any record (entity type + id from the wave-3 metadata registry: customer, vendor, worker, invoice, item, project, asset, work order, audit finding), a case log, resolution codes, per-category SLA profile and per-category OPA security.
  - SERVICE and CUSTOMER_SERVICE categories keep the entitlement and SLA behavior of FR-45.5 (BR-45.1, BR-45.2); other categories use the category's SLA profile (or none).
  - customer_party_id becomes optional; required for SERVICE/CUSTOMER_SERVICE/COLLECTIONS categories.
  - Minimal case entities used by earlier waves (wave-30 disputes, wave-9 product change requests) migrate into categories COLLECTIONS and PRODUCT_CHANGE with their ids kept as associations (migration job `case-consolidate`).
- **FR-45.13 Knowledge base** — Should. The system shall maintain knowledge articles (title, symptom, cause, solution steps, applicability to item, item group, incident type, asset type and case category, language, attachments) with versioning and lifecycle DRAFT → IN_REVIEW → PUBLISHED → RETIRED, full-text and structured search, suggestions on case creation, linking of articles to cases, and usage and helpfulness statistics.
- **FR-45.14 Activities** — Should. The system shall record activities (TASK, APPOINTMENT, CALL_NOTE, EMAIL_NOTE) with owner, due/start/end, status (OPEN, DONE, CANCELED) and reminders on cases, projects (wave 41), service work orders and any associable record, with a personal "My activities" list and calendar export (iCalendar).
- **FR-45.15 Supplier warranty claims** — Should. The system shall create supplier warranty claims from WARRANTY-covered service work order lines and from `eam.warranty.claimable.v1` events, with claim lines (part, serial, failure code, labor hours at the vendor's agreed rate), submission to the vendor (PDF/email or wave-35 EDI), vendor decision (ACCEPTED, PARTIALLY_ACCEPTED, REJECTED), failed-part return through a wave-30/17 vendor return, and settlement by matching the vendor credit note (wave 18) per BR-45.12.
- **FR-45.16 Email-to-case intake** — Should. The system shall create or update cases from inbound email to category mailboxes through the wave-35 integration hub, threading replies by a case token in the subject (`[CS-{seq}]`) or the Message-ID chain, storing attachments, matching the sender to a customer contact, sending an acknowledgment, and logging outbound replies on the case.

### 8.45.4 Data model

**ServiceAgreement**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK; number SA-{seq} |
| customer_party_id | uuid | yes | |
| legal_entity_id | uuid | yes | |
| validity | daterange | yes | |
| sla_profile_id | uuid | yes | |
| billing_model | enum | yes | PREPAID, PER_VISIT, SUBSCRIPTION |
| price_list_id, discount_pct | uuid, numeric(5,2) | no | |
| priority_rank | smallint | yes | entitlement tie-break |
| lines[] | AgreementLine | yes | (service_object_id or category, included_visits, included_hours, parts_covered bool, labor_covered bool, travel_covered bool) |
| status | enum | yes | DRAFT, ACTIVE, SUSPENDED, EXPIRED, CANCELED |

**Case**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK; number CS-{seq} |
| customer_party_id, service_object_id | uuid | yes/no | |
| channel | enum | yes | PHONE, EMAIL, PORTAL, IOT, INTERNAL |
| priority | smallint | yes | 1–4 |
| agreement_id, entitlement_result | uuid, jsonb | no | resolution trace |
| sla_response_due, sla_resolution_due | timestamptz | computed | |
| sla_state | enum | yes | RUNNING, PAUSED, MET, BREACHED |
| sla_paused_minutes | int | yes | |
| status | enum | yes | NEW, IN_PROGRESS, WAITING_CUSTOMER, WAITING_PARTS, RESOLVED, CLOSED, CANCELED |
| opened_at, responded_at, resolved_at | timestamptz | | |

**ServiceWorkOrder**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK; number SWO-{seq} |
| case_id, agreement_id, service_object_id | uuid | no/no/no | |
| customer_party_id, address_id, geo (lat, lon) | | yes | |
| territory_id | uuid | yes | |
| incident_type_id, required_skills[] | uuid, uuid[] | yes | |
| est_duration_minutes | int | yes | |
| time_window | tstzrange | no | customer-promised |
| priority, sla_due | smallint, timestamptz | | |
| status | enum | yes | UNSCHEDULED, SCHEDULED, DISPATCHED, TRAVELING, IN_PROGRESS, ON_HOLD, COMPLETED, POSTED, CANCELED |
| assignment (technician_id, start, end, sequence) | | no | from dispatch |
| lines[] | WorkOrderLine | | (kind LABOR/PART/TRAVEL/FEE, qty, unit_price, coverage BILLABLE/WARRANTY/AGREEMENT, amount) |
| signature_ref, photos[] | | no | object store |
| first_time_fix | bool | no | set at completion |

**CaseCategory**: id, code, parent_id, category_type, process_id, sla_profile_id, default_priority, opa_policy_ref, resolution_codes[]. **CaseProcess**: id, stages[] (code, sequence, entry_criteria CEL, exit_criteria CEL, required_activity_templates[]). Case adds: category_id, stage_code, resolution_code, resolution_text, case_log[] (at, actor, kind, text), and customer_party_id becomes conditional. **CaseAssociation**: case_id, entity_type (registry name), entity_id, role (SUBJECT, RELATED). **KnowledgeArticle**: id (sequence KB-{seq}), version, title, symptom, cause, solution, applicability {item_ids[], item_group_ids[], incident_type_ids[], asset_type_ids[], category_ids[]}, language, status (DRAFT, IN_REVIEW, PUBLISHED, RETIRED), author_id, reviewer_id, published_at, tsv tsvector, views, helpful_yes, helpful_no. **CaseArticleLink**: case_id, article_id, article_version, outcome (SOLVED, NOT_HELPFUL). **Activity**: id, kind, subject, owner_worker_id, due_at, start_at, end_at, status, reminder_minutes, associations[] (entity_type, entity_id). **SupplierWarrantyClaim**: id (sequence SWC-{seq}), vendor_party_id, source (SERVICE_WO, EAM_WO), source_ids[], lines[] (item_id, serial, failure_code, part_amount, labor_hours, labor_rate, claimed_amount, accepted_amount), status (DRAFT, SUBMITTED, ACCEPTED, PARTIALLY_ACCEPTED, REJECTED, SETTLED, CLOSED), submitted_at, vendor_reference, return_order_id, credit_note_id, recovered_amount. **InboundMessage**: id, mailbox, message_id, in_reply_to, from_address, subject, received_at, case_id, attachments[].

**ServiceObject**: id, item_id, serial, customer_party_id, site_address_id, parent_id, installed_at, status (ACTIVE, INACTIVE, RETURNED), shipment_line_id, warranty_id. **Warranty**: id, template, starts_on (SHIP/INSTALL/REGISTRATION), months, parts_covered, labor_covered, travel_covered, exclusions[]. **Technician**: worker_id, territory_ids[], skills[] (from wave 42), shift calendar, home geo, truck_location_id, max_daily_minutes, overtime_allowed. **DispatchRun**: id, territory_id, date, status, solver_seconds, objective, assignments[].

Invariants: a case has at most one resolved agreement; SLA due timestamps are recomputed whenever paused minutes change; a work order has at most one active assignment; parts lines must reference the technician's truck location or a wave-16 reservation; invoiced amount = Σ BILLABLE lines. Indexing: `ServiceWorkOrder (territory_id, status, sla_due)`; geo via PostGIS point index; `Case (sla_state, sla_resolution_due)` for the breach monitor.

### 8.45.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | `/api/v1/service/agreements` | Create agreement | key | SvcAgreementMaintain |
| POST | `/api/v1/service/cases` | Create case (entitlement + SLA start) | key | SvcCaseCreate |
| POST | `/api/v1/service/cases/{id}/status` | Transition (pauses/resumes SLA) | key + version | SvcCaseMaintain |
| POST | `/api/v1/service/work-orders` | Create WO | key | SvcWoMaintain |
| POST | `/api/v1/service/dispatch/runs` | Optimize territory/date | key per (territory, date, version) | SvcDispatch |
| POST | `/api/v1/service/dispatch/runs/{id}/publish` | Publish schedule | key | SvcDispatch |
| POST | `/api/v1/service/work-orders/{id}/actions` | Mobile action log (travel start, work start, part, photo, signature, complete) | key per action id | SvcTechnician |
| POST | `/api/v1/service/work-orders/{id}/post` | Bill / warranty post | key | SvcBillingPost |
| GET | `/api/v1/service/kpis?territory=&from=&to=` | KPIs | n/a | SvcInquire |
| POST | `/api/v1/cases` | Create case in any category (generic) | key | CaseCreate (category-scoped by OPA) |
| POST | `/api/v1/cases/{id}/stage` | Advance case process stage | key + version | CaseMaintain |
| POST | `/api/v1/cases/{id}/associations` | Associate a record | key | CaseMaintain |
| GET | `/api/v1/knowledge/articles/search?q=&itemId=&incidentTypeId=&lang=` | Search / suggest articles | n/a | KbRead |
| POST | `/api/v1/knowledge/articles/{id}/publish` | Publish article version | key + ETag | KbPublish |
| POST | `/api/v1/activities` | Create activity on any record | key | ActivityMaintain |
| POST | `/api/v1/service/supplier-claims` | Create supplier warranty claim | key | SvcClaimMaintain |
| POST | `/api/v1/service/supplier-claims/{id}/decision` | Record vendor decision | key | SvcClaimMaintain |

Validation: case creation requires a customer; dispatch run requires all WOs to have geo and skills; complete action requires signature unless incident type exempts; post requires COMPLETED and all parts lines consumed.

| Event | Trigger | Payload | Consumers |
|---|---|---|---|
| `dev.passage.service.case.opened.v1` | case created | caseId, customerId, priority, agreementId, responseDue, resolutionDue | notifications, analytics |
| `dev.passage.service.sla.breached.v1` | breach monitor | see 8.0 | notifications, analytics |
| `dev.passage.service.dispatch.published.v1` | publish | see 8.0 | PWA, customer notifications |
| `dev.passage.service.workorder.completed.v1` | complete action | workOrderId, technicianId, lines[], firstTimeFix, durationMinutes | billing, inventory (truck consumption), KPIs |
| `dev.passage.service.workorder.posted.v1` | billing post | workOrderId, invoiceId, billableTotal, warrantyCost, entitlementConsumed | invoicing, analytics |
| `dev.passage.case.stage.changed.v1` | stage transition (any category) | caseId, categoryId, from, to, at, actor | workflow, notifications, domain owners (HR, collections, audit) |
| `dev.passage.knowledge.article.published.v1` | article PUBLISHED | articleId, version, language, applicability | search index, copilot retrieval (wave 47) |
| `dev.passage.service.supplier-claim.settled.v1` | credit note matched | claimId, vendorId, claimedAmount, recoveredAmount | analytics, GL warranty recovery |

### 8.45.6 User experience

| Screen | Primary actions | Key fields/columns | Duty |
|---|---|---|---|
| Service workspace | tiles: SLA at risk (> 75% elapsed), unscheduled WOs, today's completion rate | | SvcInquire |
| Case detail | entitle, create WO, pause/resume, resolve | SLA timers, agreement, object, timeline | SvcCaseMaintain |
| Agreement detail | lines, entitlement usage, SLA profile, billing | | SvcAgreementMaintain |
| Dispatch board (Gantt + map) | optimize, drag assign, lock, publish, re-optimize | technician rows, WO blocks, travel segments, SLA due markers | SvcDispatch |
| Work order detail | lines, coverage, parts, photos, signature, post | | SvcWoMaintain |
| Technician PWA | route, navigate, start/stop, checklist, parts, photos, signature, complete | offline indicator, sync queue | SvcTechnician |
| Installed base explorer | search by customer/serial, hierarchy, history | | SvcInquire |
| KPI dashboard | filter by territory/technician | SLA %, FTF %, utilization | SvcInquire |
| My cases / case list (any category) | create, filter by category/stage/SLA, bulk assign | case, category, stage, owner, SLA due, associations | CaseMaintain |
| Case category and process setup | define categories, stages, criteria, SLA, security | category tree, process stages | CaseSetupAdmin |
| Knowledge base | search, view, rate helpful, author, review, publish | title, applicability, language, version, status, helpful % | KbRead / KbAuthor / KbPublish |
| My activities | list, calendar, complete, reschedule | activity, related record, due, status | ActivityMaintain |
| Supplier warranty claims | create from WO lines, submit, record decision, match credit note | claim, vendor, lines, claimed, accepted, recovered | SvcClaimMaintain |
| Case mailbox | view unmatched inbound email, attach to case, create case | sender, subject, received, match suggestion | CaseMaintain |

Rules: board locks (pinned assignments) survive re-optimization; dragging a WO outside skill/time window shows a hard-block explanation; PWA photo max 5 per action at 1,600 px long edge; signature stored as PNG with timestamp and geo.

### 8.45.7 Business rules and edge cases

- **BR-45.1 Entitlement resolution** — candidates = ACTIVE agreements of the customer (or parent party) whose validity covers opened_at and whose lines match the service object (exact object > object category > any) ; rank by match specificity, then priority_rank, then latest start; the chosen agreement sets the SLA profile and coverage; if none, the default SLA profile of the legal entity applies and all lines are BILLABLE unless warranty covers them.
- **BR-45.2 SLA clock rules** — response target measured from opened_at to first of (responded_at, assignment DISPATCHED); resolution target from opened_at to resolved_at. Clocks count only business minutes of the SLA profile calendar (e.g. Mon–Fri 08:00–18:00 in the service site's time zone, holidays excluded), unless the profile is 24×7. Pause states: WAITING_CUSTOMER pauses both clocks; WAITING_PARTS pauses resolution only if profile.pause_on_parts = true. Due timestamps are stored as absolute times and recomputed on each pause/resume: due = add_business_minutes(opened_at, target_minutes + paused_minutes). Warning event at 75% elapsed; breach event when now > due while RUNNING; a breached SLA stays BREACHED even if resolved later (attainment reporting counts it as missed). Example: priority 2 resolution target 480 business minutes, opened Fri 2026-10-23 16:00 (America/Chicago), paused 120 minutes Mon → due Tue 2026-10-27 12:00.
- **BR-45.3 Dispatch optimization constraints** — decision: assign each WO to at most one technician and a start time. Hard constraints: required_skills ⊆ technician.skills (certifications unexpired per BR-42.8); WO.territory ∈ technician.territory_ids; start ≥ time_window.lower and end ≤ time_window.upper if set; start/end within technician shift; sequence feasibility with travel time from an OSRM/Valhalla matrix (self-hosted) plus 5-minute buffer; Σ (work + travel) ≤ max_daily_minutes (+ overtime up to 120 min if overtime_allowed); pinned assignments fixed; parts required must be on the truck or a reservation picked. Objective (lexicographic weights): minimize SLA lateness minutes × 100, unassigned priority-weighted WOs × 1,000, travel minutes × 1, overtime minutes × 5, technician count × 50. Solver: CP-SAT with 30 s time limit, 8 workers; fallback greedy insertion when no feasible solution in time; incremental re-optimization only moves WOs not yet DISPATCHED or with the dispatcher's consent. Scale target: 200 WOs × 25 technicians per territory-day.
- **BR-45.4 Coverage per line** — PART: WARRANTY if warranty.parts_covered and within term and not excluded; else AGREEMENT if line.parts_covered; else BILLABLE. LABOR/TRAVEL analogous. Agreement included_hours consumed FIFO; when exhausted, remaining hours BILLABLE at agreement price list.
- **BR-45.5 Work order status machine** — UNSCHEDULED → SCHEDULED → DISPATCHED → TRAVELING → IN_PROGRESS → COMPLETED → POSTED; IN_PROGRESS ↔ ON_HOLD; any pre-COMPLETED → CANCELED; a COMPLETED WO with follow-up creates a new WO linked by `follow_up_of` and sets first_time_fix = false.
- **BR-45.6 Offline merge** — mobile actions are append-only with client-generated UUIDs and device timestamps; server orders by (device timestamp, receipt order); conflicting status transitions resolve to the furthest valid state; free text last-writer-wins; parts consumption is idempotent per action id.
- **BR-45.7 Truck stock** — consumption posts an inventory issue from the TRUCK location at completion; negative truck stock blocked unless policy `service.allow-negative-truck`; replenishment transfer orders generated when below min per item.
- **BR-45.8 Pricing** — BILLABLE labor at agreement price list → customer price group → standard rate; parts via wave-27 pricing engine with agreement discount; rounding per line half-even.
- **BR-45.9 Case process stages** — a case starts at the first stage of its category's process; advancing requires the current stage's exit criteria (CEL over case, associations, activities) to be true and all required activities of the stage DONE (`CASE-STG-001`); moving back one stage is allowed with a reason; RESOLVED requires a resolution code; CLOSED is final, and reopening creates a new case linked by `reopened_from` unless the category allows reopen within N days (parameter, default 14).
- **BR-45.10 Case category security** — read and write access to a case is the union of: owner, members of the category's owning team, and users whose OPA policy for the category type grants access (e.g. HR cases only to HR partners and the case subject's manager chain; COLLECTIONS to the customer's collector); associations to records the user cannot read are shown as "restricted record"; HR category cases store free text in the `hr_pii`-protected column set.
- **BR-45.11 Knowledge article lifecycle and suggestion** — only one PUBLISHED version per (article, language); publishing a new version retires the previous one; reviewer ≠ author; suggestions on case creation rank PUBLISHED articles by ts_rank of the case description against the article tsvector × 0.6 + applicability match (item 0.25, incident type 0.10, asset type 0.05) and return the top 5; helpfulness % = helpful_yes ÷ (helpful_yes + helpful_no) shown when ≥ 10 votes.
- **BR-45.12 Supplier warranty claims** — claimable amount per line = part cost at issue (wave 16) + labor_hours × vendor labor_rate (from the vendor warranty agreement; default 0 when labor not covered); claims must be submitted within the vendor's claim window (default 30 days after WO completion, `SVC-SWC-001` after); accepted_amount ≤ claimed_amount; settlement matches the wave-18 vendor credit note by vendor_reference, and the recovered amount credits the warranty expense account; rejected claims close with reason and no posting; a failed-part return is required when the vendor flag `return_required` is set before SUBMITTED.
- **BR-45.13 Email threading** — an inbound message is attached to case X when the subject contains `[CS-X]` and the case is not CLOSED, or when In-Reply-To matches a message already on case X; otherwise a new case is created in the mailbox's default category with the sender matched to a party contact by email (exact, case-insensitive); messages to a CLOSED case create a new case linked by `reopened_from`; auto-acknowledgment is sent once per new case and suppressed for auto-replies (headers `Auto-Submitted`, `X-Autoreply`).

### 8.45.8 Acceptance criteria

- **AC-45.1** (FR-45.5, BR-45.1) Given 50 entitlement fixtures (overlapping agreements, categories, parent parties), when cases open, then each resolves to the expected agreement and SLA profile.
- **AC-45.2** (FR-45.4, BR-45.2) Given the BR-45.2 example, when the case is paused 120 minutes on Monday, then resolution due = 2026-10-27T12:00-05:00.
- **AC-45.3** (BR-45.2) Given a RUNNING case at 75% elapsed, when the monitor runs, then a warning notification fires; at 100% a `sla.breached` event fires once.
- **AC-45.4** (FR-45.7, BR-45.3) Given 1,000 generated territory-days (≤ 200 WOs, ≤ 25 technicians), when optimized, then every assignment satisfies skills, windows, shifts and travel feasibility, and solver time p95 < 30 s (property test).
- **AC-45.5** (FR-45.7) Given a pinned assignment, when re-optimization runs, then it is unchanged.
- **AC-45.6** (FR-45.10, BR-45.4) Given a WO with a part covered by warranty and 3 labor hours under an agreement with 1 included hour, when posted, then the invoice contains 2 billable labor hours, the part is zero-priced with warranty cost recorded, and entitlement usage shows 1 hour consumed.
- **AC-45.7** (FR-45.8, BR-45.6) Given a technician offline for 8 h performing start/part/complete actions, when reconnected, then the server state equals the device state and inventory shows one issue per part action.
- **AC-45.8** (FR-45.1) Given a wave-28 shipment of 10 serialized units, when confirmed, then 10 ACTIVE service objects exist with warranty starting on ship date.
- **AC-45.9** (phase exit) Given the demo group, when the recorded walkthrough runs projects → HR → payroll → maintenance → field service, then every step completes on `stage` with reconciliations at zero variance.
- **AC-45.10** (FR-45.12, BR-45.9) Given an HR category with stages INTAKE → INVESTIGATION → RESOLUTION and a required "Interview" activity in INVESTIGATION, when the case advances to RESOLUTION with the activity OPEN, then it is rejected with `CASE-STG-001`; after the activity is DONE it advances.
- **AC-45.11** (FR-45.12, BR-45.10) Given an HR case on worker W, when a collector without HR policy queries it, then 403 is returned; the subject's manager (as-of today per BR-42.1) can read it.
- **AC-45.12** (FR-45.13, BR-45.11) Given 3 PUBLISHED articles of which one applies to the case item, when a case is created with matching description text, then that article ranks first in the suggestions; a reviewer equal to the author cannot publish (`KB-PUB-001`).
- **AC-45.13** (FR-45.15, BR-45.12) Given a WO with a WARRANTY part line of 180.00 USD and 1.5 labor hours at a vendor rate of 60.00, when a claim is created and the vendor accepts 250.00 with credit note matched, then recovered_amount = 250.00, the warranty expense account is credited 250.00 and the claim is SETTLED.
- **AC-45.14** (FR-45.16, BR-45.13) Given an open case CS-1042, when an email with subject "Re: [CS-1042] pump noise" arrives, then it is logged on CS-1042 and no new case or acknowledgment is created; an email with no token from an unknown sender creates a new case in the mailbox's default category.
- **AC-45.15** (FR-45.14) Given a project (wave 41) and a case each with an open TASK for the same owner, when "My activities" loads, then both appear with their related records, and completing one sets status DONE with timestamp.

### 8.45.9 Test plan and fixtures

Fixtures: `svc-installed-base` (derived from wave-28 shipped serials, 2,000 objects); `svc-entitlement-50`; `svc-dispatch-gen` (Hypothesis generator for WOs/technicians with seeded travel matrix); `svc-mobile-replay` (offline action logs); `svc-billing-30`.

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Entitlement | unit | resolver | 50/50 |
| SLA calendar arithmetic | property | business-minute functions | add/subtract inverse; DST-safe |
| Dispatch feasibility | property | CP-SAT model | 1,000/1,000 feasible; p95 < 30 s |
| Offline merge | unit/E2E | action log merge | deterministic result |
| Billing coverage | contract | invoicing API | 30/30 |
| PWA | E2E (Playwright, offline) | technician flow | complete with signature offline |
| Breach monitor | performance | 100k open cases | scan < 60 s |

Reconciliation: Σ POSTED billable = wave-29 invoices with source SWO; Σ warranty cost = GL warranty expense; truck stock issues = Σ part actions.

### 8.45.10 Deployment and operations

Charts `passage-service` (TS API, breach monitor, mobile BFF) and `passage-dispatch` (Python OR-Tools worker, OSRM sidecar with regional extracts); Postgres `service` with PostGIS. Flags: `service.allow-negative-truck`. Schedules: `service-sla-monitor` every 60 s; `service-dispatch-auto` 18:00 territory-local for next day; `service-truck-replenish` nightly. SLOs: case create p95 < 300 ms; dispatch run ≤ 30 s; PWA sync p95 < 2 s per 100 actions; availability 99.9%. Alerts: SLA monitor lag > 2 min; solver fallback rate > 10%; OSRM unavailable. Runbook: re-run dispatch with greedy; replay mobile actions; refresh OSRM extract. Rollback by digest; dispatch runs are versioned and republishable.

### 8.45.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| SMAServiceObjectTable, SMAServiceObjectRelationTable | ServiceObject | link to inventory serials where found |
| SMAServiceAgreementTable/Line, SMAAgreementLine | ServiceAgreement, lines | interval lines → preventive visit WOs |
| SMAServiceOrderTable/Line (open) | ServiceWorkOrder | stage map to status |
| SMAServiceTechnician | Technician | links to wave-42 workers |
| D365 FS msdyn_workorder, msdyn_agreement, msdyn_customerasset | ServiceWorkOrder, ServiceAgreement, ServiceObject | via Dataverse export |
| IBase (IBIN/IBIB/IBST), EQUI with customer | ServiceObject | |
| BGMK warranties | Warranty | |
| VBAK/VBAP contracts type WV/SC, SLA profiles | ServiceAgreement, SlaProfile | |
| QMEL service notifications (open), CS orders (open) | Case, ServiceWorkOrder | |
| SAP FSM activities (open) | ServiceWorkOrder assignments | via FSM API |

Validation: installed-base count per customer equal; open WO count and value equal; active agreement count equal. Not migrated: closed service orders (archive), AX subscriptions (recreated as wave-29 recurring invoices), FSM historical GPS traces.

### 8.45.12 Dependencies, risks, open questions

Depends on: wave 16 locations (TRUCK), wave 27 pricing, wave 28 serial shipments, wave 29 invoicing, wave 35 notification hub, wave 42 workers/skills, wave 44 condition rules, wave 23 scheduler patterns.

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Travel matrix accuracy/availability | Medium | Medium | Self-hosted OSRM; cached matrix; straight-line fallback × 1.3 |
| Offline conflict edge cases | Medium | Medium | Append-only actions; replay fixtures |
| Phase-exit demo breadth | High | High | Walkthrough script frozen at wave start; dry run at week 4 |

Open questions: (1) Customer self-scheduling portal in v1? Default v1.x; decide by wave 46. (2) Should IoT-originated cases bypass the case stage and create WOs directly? Default create case with auto-conversion for priority 1; decide by wave 45 start.

### 8.45.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| Agreement / case / WO / claim number sequences | legal entity | lookup | SA, CS, SWO, SWC, KB | wave-5 sequences | Identifiers | Service management parameters › Number sequences (SMAParameters) | Service order number ranges (T003O SM01/SM02), notification ranges (TQ80 S1/S2/S3) |
| Default SLA profile | legal entity | lookup | — | SlaProfile | BR-45.1 fallback | Service management parameters › Service level agreements | SLA profiles (CRM service profile / response profile) |
| SLA warning threshold | tenant | pct | 75 % | 50–95 % | BR-45.2 warning event | n/a | Response profile escalation |
| Pause resolution on WAITING_PARTS | SLA profile | bool | false | | BR-45.2 | n/a | Status-dependent SLA pause (CRM date profile) |
| Entitlement match includes parent party | legal entity | bool | true | | BR-45.1 | Service agreement › Customer hierarchy | Contract determination (VA41 partner functions) |
| Warranty start trigger default | item group | enum | SHIP | SHIP, INSTALL, REGISTRATION | FR-45.2 | Service object › Warranty dates | Warranty start (BGMK master warranty; IE01 warranty tab) |
| Installed base auto-creation from shipments | legal entity | bool | true | | FR-45.1 | Service management parameters › Create service objects | IB creation from delivery (IBase user exit / VL02N serial) |
| Dispatch auto-run time | territory | time | 18:00 | 00:00–23:59 | `service-dispatch-auto` | Dispatch board parameters | FSM planning settings |
| Solver time limit / workers | tenant | int s / int | 30 / 8 | 5–300 / 1–32 | BR-45.3 | n/a | RSO optimization run settings |
| Travel buffer minutes | territory | int | 5 | 0–60 | BR-45.3 | Service management parameters › Travel time | FSM travel time setup |
| Overtime allowance | technician | int minutes | 120 | 0–240 | BR-45.3 | Technician setup | FSM workforce rules |
| Signature required by incident type | incident type | bool | true | | 8.45.5 validation | Service order › Sign-off (SMASignOff) | Service confirmation completion |
| Allow negative truck stock | tenant | bool | false | | BR-45.7 | n/a | Van stock storage location settings (T001L negative stock) |
| Truck replenishment min/max per item | truck location | numeric | from item | ≥ 0 | BR-45.7 | Warehouse per resource (D365 FS) | MRP area per storage location (OMIZ) |
| Case category default and reopen window | category | lookup + int days | 14 | 0–365 | BR-45.9 | Case management › Case categories, Case parameters (Organization administration › Setup › Cases) | Notification type settings (TQ80), CRM case types |
| Case process per category | category | lookup | — | CaseProcess | BR-45.9 | Case processes (CaseProcess) | Notification status profile (BS02) / task lists (QMSM) |
| Knowledge article review required | tenant | bool | true | | BR-45.11 | Knowledge article parameters | Solution database settings (IS01 symptom/solution types) |
| Suggestion count and weights | tenant | int + weights | 5; 0.6/0.25/0.10/0.05 | 1–20 | BR-45.11 | n/a | n/a |
| Supplier claim window (days) | vendor | int | 30 | 1–365 | BR-45.12 | n/a | WTY claim types and deadlines (WTY customizing) |
| Case mailboxes and default categories | legal entity | list | — | email addresses | BR-45.13 | Email to case (not native) | CIC email routing (CRM ERMS) |

### 8.45.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Service work order list | list inquiry | territory, status, priority, SLA due, technician, agreement | WO, customer, object, status, window, SLA due, assigned | `svc_wo` | Service orders (SMAServiceOrderTable list) | IW72 change / IW73 display service orders |
| Case list (any category) | list inquiry | category, stage, owner, SLA state, association | case, category, stage, priority, SLA due, age | `case_list` | Cases list page (CaseDetailListPage); Case inquiries | IW58 / IW59 notifications; CRM case search |
| Case report / case activities | operational report | category, period | opened, resolved, backlog, mean time to resolve, activities per case | `case_stats` | SSRS Case report / Case activities | IW65 notification activities; IW69 notification items |
| SLA attainment | KPI | territory, category, priority, period | response %, resolution %, breaches, mean minutes | `svc_sla` | n/a (SSRS service level reports) | CRM SLA analysis |
| First-time fix and utilization | KPI | technician, territory, period | FTF %, utilization %, travel ratio | `svc_kpi` | Service management reports | FSM analytics |
| Installed base | drill-down | customer, item, serial, warranty status | object, structure, install date, warranty end, history | `svc_ib` | Service objects (SMAServiceObjectTable) | IB53 IBase display; IH08 equipment with customer |
| Agreement entitlement usage | list inquiry | agreement, period | included vs consumed visits/hours, revenue | `svc_entitlement` | Service agreements › Lines | IW73 by contract; VA45 contract list |
| Warranty cost and recovery | KPI | item, vendor, period | warranty cost, claimed, recovered, recovery % | `svc_warranty` | n/a | WTY claim worklist and claim analysis |
| Supplier claims | list inquiry | vendor, status, age | claim, lines, claimed, accepted, recovered, days open | `svc_claims` | n/a | WTY claim worklist (WTY) |
| Knowledge article usage | KPI | category, language, period | views, links to cases, solved %, helpful % | `kb_stats` | Knowledge articles (CaseKnowledgeArticle) | IS02 solution usage |
| My activities | list inquiry | owner, due range, status | activity, related record, due | `activities` | Activities (smmActivities) | CRM activity search |
| Truck stock | list inquiry | technician, item, below min | on hand, min, max, open transfers | `svc_truck` | On-hand by resource warehouse | MMBE by van storage location |

### 8.45.15 Printed and electronic documents

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Service report (customer-facing summary) | WO COMPLETED | header: customer, site, object, WO; work performed, parts used, labor and travel, coverage per line, photos (optional), signature with timestamp/geo | PDF, email, PWA | SWO | service record; consumer-repair disclosure laws where applicable (e.g. EU right-to-repair directive 2024/1799 information) |
| Service invoice | WO POSTED | wave-29 invoice with WO reference, BILLABLE lines only | PDF, e-invoice, email | wave-29 `INV` | fiscal invoice |
| Service agreement | agreement ACTIVE | parties, validity, covered objects, entitlements, SLA targets, pricing, billing model, terms | PDF, e-signature | SA | contract record, 10 years (DE HGB §257) |
| Case acknowledgment / resolution notice | case created / RESOLVED | case no, category, summary, SLA targets or resolution | email, portal | CS | none |
| Dispatch schedule (technician day sheet) | schedule published | route order, windows, addresses, required parts | PWA, PDF | dispatch run id | none |
| Supplier warranty claim | claim SUBMITTED | vendor, claim no, lines (item, serial, failure code, part amount, labor), return reference | PDF, email, EDI (wave 35) | SWC | supporting document for credit note; accounting retention |
| Knowledge article | on demand | title, applicability, solution steps, version | PDF, portal | KB-{seq} version | none |

### 8.45.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| `service-sla-monitor` | every 60 s | tenant, category | event once per (case, threshold) | 100k open cases < 60 s | retry; skip-locked batches | lag > 2 min |
| `service-dispatch-auto` | 18:00 territory-local | territory, date | key per (territory, date, version) | ≤ 30 s solve per territory | greedy fallback | fallback rate > 10 % |
| `service-truck-replenish` | nightly 02:00 | truck location | key (location, date) | < 5 min | retry | below-min items without transfer |
| `service-ib-from-shipments` | event-driven | shipment | key (shipment line, serial) | < 60 s per shipment | retry from offset | serial without object after 5 min |
| `service-agreement-visits` | daily 03:00 | agreement | key (agreement line, visit date) | < 5 min | retry | missed preventive visit WOs |
| `case-email-ingest` | every 1 min (wave-35 mailbox poll or push) | mailbox | Message-ID unique | < 30 s per batch | quarantine parse failures | mailbox backlog > 100 |
| `case-consolidate` (one-off per tenant at cut-in) | on demand | source entity | key (source id) | < 30 min for 100k | resumable | unmapped source cases |
| `supplier-claim-deadline` | daily 07:00 | vendor | key (claim, day) | < 1 min | retry | drafts within 5 days of window end |
| `kb-reindex` | on publish; nightly verify | language | article version | < 10 s per article | retry | index lag > 5 min |

### 8.45.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles that include it |
|---|---|---|---|
| SvcAgreementMaintain | agreements create/change; Agreement detail | with SvcBillingPost for the same customer | Service contract manager |
| SvcCaseCreate / SvcCaseMaintain | service cases create, transitions | none | Service agent |
| CaseCreate / CaseMaintain (category-scoped) | `/api/v1/cases`, stage, associations; case list, mailbox | per-category OPA policy (BR-45.10) | Agent of the owning team (HR partner, collector, auditor) |
| CaseSetupAdmin | categories, processes, mailboxes, SLA profiles | with CaseMaintain on HR categories (setup admin cannot read HR cases by default) | Case administrator |
| SvcWoMaintain / SvcDispatch | WOs, dispatch runs, publish | none | Service coordinator, Dispatcher |
| SvcTechnician | mobile action log, parts, signatures | none | Field technician |
| SvcBillingPost | WO post to invoicing | with SvcAgreementMaintain | Service billing clerk |
| SvcClaimMaintain | supplier claims create, submit, decision | with AP credit note posting (wave 18) for the same claim | Warranty administrator |
| KbRead / KbAuthor / KbPublish | search, author, publish | author ≠ publisher (BR-45.11) | All users / Senior agent / Knowledge manager |
| ActivityMaintain | activities on readable records | none | All internal users |
| SvcInquire | KPIs, lists, reports | none | Service manager |

### 8.45.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| SVC-ENT-001 | info | No agreement matched; default SLA {profile} applied, lines billable | Entitlement | Check agreements | BR-45.1 |
| SVC-SLA-001 | warning | SLA {type} at {pct} % for case {case} | Warning threshold | Prioritize | BR-45.2 |
| SVC-SLA-002 | error | SLA {type} breached for {case} | Breach | Escalate | BR-45.2 |
| SVC-DSP-001 | error | Work order {wo} lacks geo or skills; cannot optimize | Precondition | Complete address/skills | 8.45.5 validation |
| SVC-DSP-002 | warning | Solver fell back to greedy for {territory} {date} | Time limit | Review assignments | BR-45.3 |
| SVC-DSP-003 | error | Technician {t} lacks skill {s} or certification expired | Hard constraint on drag | Choose another technician | BR-45.3 |
| SVC-WO-001 | error | Signature required to complete | Incident type rule | Capture signature | 8.45.5 validation |
| SVC-WO-002 | error | Parts lines not consumed; cannot post | Post precondition | Consume or remove parts | 8.45.5 validation |
| SVC-TRK-001 | error | Truck stock for {item} would go negative | BR-45.7 | Transfer stock or use reservation | BR-45.7 |
| SVC-SYNC-001 | warning | {n} offline actions merged; {m} status conflicts resolved to {state} | Offline merge | Review timeline | BR-45.6 |
| CASE-STG-001 | error | Stage {stage} exit criteria not met: {reasons} | Process gate | Complete required activities | BR-45.9 |
| CASE-SEC-001 | error | You do not have access to cases in category {category} | Category security | Request access | BR-45.10 |
| CASE-RES-001 | error | Resolution code required | RESOLVED transition | Pick a code | BR-45.9 |
| CASE-MAIL-001 | info | Email attached to case {case} | Threading | None | BR-45.13 |
| KB-PUB-001 | error | Reviewer must differ from author | SoD | Assign another reviewer | BR-45.11 |
| KB-PUB-002 | warning | Publishing retires version {v} | Versioning | None | BR-45.11 |
| SVC-SWC-001 | error | Claim window for vendor {v} ended on {date} | Deadline | Request vendor exception | BR-45.12 |
| SVC-SWC-002 | error | Failed-part return required before submission | Vendor flag | Create return | BR-45.12 |
| SVC-SWC-003 | error | Accepted amount exceeds claimed amount | Validation | Correct decision | BR-45.12 |

### 8.45.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| Case create (entitlement + SLA) | 5,000/day; peak 100/min | 100 / 300 / 600 ms | 20/s | Case + log ≈ 5 KB × 1.8 M = 9 GB | (category_id, status), (sla_state, sla_resolution_due) | entitlement result cached per (customer, object) 5 min |
| SLA monitor scan | 100k open cases | < 60 s per cycle | — | n/a | partial index on RUNNING | cycle extends; warning events late by ≤ 1 cycle |
| Dispatch optimization | 200 WOs × 25 technicians per territory-day; 50 territories | ≤ 30 s per territory | 8 territories in parallel | DispatchRun ≈ 1 GB | (territory_id, date) | greedy fallback; staggered start times |
| Mobile sync | 1,000 technicians × 200 actions/day | 2 s p95 per 100 actions | 500 actions/s | action log ≈ 40 GB incl. photos refs | (work_order_id, action_id) | queue; device retries with backoff |
| Knowledge search | 20,000/day; peak 50/s | 50 / 150 / 300 ms | 50 q/s | articles ≈ 50k × 20 KB = 1 GB | GIN on tsvector per language | return top 5 from title-only index |
| Email ingest | 10,000 messages/day | < 30 s to case | 20 msg/s | attachments ≈ 200 GB object store | Message-ID unique | backlog processed in order; ack delayed |
| Supplier claim lifecycle | 2,000/month | 100 / 300 / 600 ms | 5/s | ≈ 100 MB | (vendor_party_id, status) | none required |

### 8.45.20 Localization and compliance hooks

- Consumer warranty law: EU Directive 2019/771 (2-year legal guarantee, 1-year burden-of-proof reversal), DE BGB §§437–439, UK Consumer Rights Act 2015, US Magnuson-Moss Warranty Act; legal minimum coverage is a pack-provided warranty template that `Warranty` cannot undercut for consumer customers; extension point `service.warranty.minimum.<cc>`.
- Right to repair: EU Directive 2024/1799 repair information obligations reflected in the service report fields (repair price, parts availability) when the pack enables them.
- SLA calendars: business hours and public holidays per service site from wave-8 calendars; time zone of the service site, DST-safe arithmetic (8.45.9 property test).
- Privacy: technician location (geo on signatures, travel traces) is personal data; retention 90 days for traces by default, works-council approval flag (DE BetrVG §87(1) 6) before enabling live tracking; HR-category cases inherit HR retention and `hr_pii` protection; email intake honors data-subject requests by purging InboundMessage bodies with the case.
- Language: case acknowledgments, service reports and knowledge articles per customer language (articles are per-language versions; fallback to English).
- E-invoicing for service invoices follows wave 29 per country; supplier claims to EU vendors can be sent as EN 16931 credit requests when the vendor supports it (pack data).
- Tax: warranty replacements free of charge are non-taxable supplies in most jurisdictions, but recovered supplier claim amounts follow the vendor's tax treatment (wave 13 tax codes on the credit note).

### 8.45.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX #233 — 12.29.1 Cases list page / case details (CaseDetailListPage / CaseDetail) | PARTIAL | ADDED as FR-45.12 | Categories, processes, associations, log, resolution |
| AX #234 — 12.29.1 Cases → Knowledge articles (CaseKnowledgeArticle) | PARTIAL | ADDED as FR-45.13 | CaseArticleLink |
| AX #241 — 12.29.1 Case processes / case categories (CaseProcess, CaseCategory) | PARTIAL | ADDED as FR-45.12 | BR-45.9 |
| AX #242 — 12.29.2 Case inquiries (by category/status, SLA) | PARTIAL | ADDED as FR-45.12 | "Case list (any category)", "SLA attainment" |
| AX #244 — 12.29.2 SSRS Case report / Case activities | PARTIAL | ADDED as FR-45.12 | "Case report / case activities" |
| AX #248 — 12.29.3 Close cases / case SLA breach notifications | PARTIAL | ADDED as FR-45.12 | `service-sla-monitor` extended to all categories; BR-45.9 close rules |
| AX #254 — 12.29.4 Case categories / category types / processes / category security | PARTIAL | ADDED as FR-45.12 | BR-45.10 OPA per category |
| AX #271 — 12.31.1 Cases (My cases) | PARTIAL | ADDED as FR-45.12 | Screen "My cases / case list" |
| AX #275 — 12.31.4 Cases → Case setup (link) | PARTIAL | ADDED as FR-45.12 | Screen "Case category and process setup" |
| AX #108 — 12.12.1 Human resources → Cases (HR cases) | PARTIAL | ADDED as FR-45.12 | Category type HR with `hr_pii` protection (BR-45.10) |
| AX #92 — 12.11.1 Project → Activities (smmActivities) | PARTIAL | ADDED as FR-45.14 | Activities associate to projects (wave 41) |
| AX #227 — 12.28.1 Service subscriptions (SMASubscriptionTable, fee transactions, index) | PARTIAL (wave 29) | SUPERSEDED by wave-29 subscription billing | Agreement billing_model SUBSCRIPTION drives wave-29 recurring invoices; fee indexation uses wave-29 price escalation |
| AX #228 — 12.28.2 Subscription inquiries | PARTIAL (wave 29) | SUPERSEDED by wave-29 subscription billing | wave-29 subscription inquiries filtered by agreement |
| AX #229 — 12.28.2 SSRS Subscription report / fee transactions | PARTIAL (wave 29) | SUPERSEDED by wave-29 subscription billing | |
| AX #230 — 12.28.3 Create subscription fee transactions / accrue subscription revenue / index subscriptions | PARTIAL (wave 29) | SUPERSEDED by wave-29 subscription billing | Revenue accrual by wave-29 revenue recognition schedules |
| AX #231 — 12.28.4 Subscription groups, fee categories, index rules, periods | PARTIAL (wave 29) | SUPERSEDED by wave-29 subscription billing | |
| SAP #303 — 13.26 Warranty claim processing (WTY1, vendor recovery) | PARTIAL-45 | ADDED as FR-45.15 | BR-45.12; dealer-to-OEM claims (automotive WTY inbound) OUT-OF-SCOPE |
| SAP #307 — 13.26 SPRO warranty claim processing (WTY) | PARTIAL-45 | ADDED as FR-45.15 | Parameter "Supplier claim window" |
| SAP #304 — 13.26 Solution database IS01/IS02 (knowledge base) | GAP | ADDED as FR-45.13 | BR-45.11; wave-47 copilot consumes published articles |
| SAP #308 — 13.26 Solution database / knowledge base settings | GAP | ADDED as FR-45.13 | Parameters "Knowledge article review required", "Suggestion count and weights" |
| SAP #305 — 13.26 CIC0 / CRM Service / S/4 Service front office | PARTIAL-45 | ADDED as FR-45.16 | Email-to-case; telephony/CTI OUT-OF-SCOPE (CRM non-goal); service quotations via wave-27 quotations linked to cases |
| SAP #309 — 13.26 CIC / contact center framework | PARTIAL-45 | OUT-OF-SCOPE CRM/contact-center non-goal | Mailbox routing covered by FR-45.16; CTI, agent desktop and channel routing are external (integrate via wave 35) |

