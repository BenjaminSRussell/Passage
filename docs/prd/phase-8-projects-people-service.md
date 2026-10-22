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

Commands validate: journal lines must reference a project in IN_PROCESS; category type must match txn_type; posting date within open period for module PROJECTS (wave 14); proposal selection requires a billing rule whose currency equals project currency.

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| `dev.passage.projects.transaction.posted.v1` | journal/document posted | see 8.0 | journal, analytics, expense |
| `dev.passage.projects.invoice-proposal.posted.v1` | proposal → POSTED | proposalId, invoiceId, lines[] | invoicing (wave 29), analytics |
| `dev.passage.projects.estimate.posted.v1` | estimate posted or reversed | estimateId, projectId, periodId, pocPercent, revenueThisPeriod, journalId, reversed | journal, analytics |
| `dev.passage.projects.wbs.scheduled.v1` | scheduler run | projectId, planVersionId, criticalPath[], finishDate | resource planning, notifications |

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

Depends on: wave 11 posting API, wave 14 periods, wave 17 PO project references, wave 25 timesheets, wave 29 invoicing, wave 37 CIP, wave 38 budget control, wave 40 intercompany.

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| POC method disputes | Low | High | Workbook sign-off before wave close |
| Proposal performance on large backlogs | Medium | Medium | Partial index + batch 500 |

Open questions: (1) Support POC by effort (hours) in v1? Default no; decide by wave 42. (2) Grant compliance reporting (AX ProjGrant)? Default v1.x; decide by wave 46. (3) Intercompany resource hours at transfer price (wave 40 mirror documents)? Default v1.x; decide by wave 42.

## 8.42 Wave 42: Human Resources Core: Workers, Positions, Absence & Talent

### 8.42.1 Scope and business context

HR core replaces AX Human resources (HcmWorker, HcmEmployment, HcmPosition, HcmJob, HcmPositionWorkerAssignment, LeaveRequest/LeavePlan, HcmBenefit) and SAP HCM PA/OM (PA20/PA30 infotypes 0000 Actions, 0001 Org Assignment, 0002 Personal Data, 0008 Basic Pay, 2001 Absences; HRP1000/HRP1001 objects and relationships; PT time types; benefits PA-BN). After this wave, an HR administrator manages workers, employments, positions, jobs and departments with effective dating; employees request absence and enroll in benefits; managers approve and run simple reviews. The wave also establishes the PII vault used by payroll.

Out of scope: full recruitment (ATS), learning management, compensation cycles beyond fixed/variable plans, SuccessFactors-style succession, country-specific HR legal fields beyond the framework.

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

**BenefitPlan**: id, code, type, provider_party_id, options[] (code, coverage level, employee_rate, employer_rate, rate_basis PER_PAY_PERIOD/MONTHLY), eligibility_rule (CEL expression), enrollment_window, waiting_days, pre_tax bool, payroll_deduction_code. **BenefitEnrollment**: worker_id, plan_id, option_code, validity, dependents[], life_event_id.

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

Validation: action effective date may be future-dated up to 365 days; termination date ≥ hire date; absence request quantity ≤ balance + negative allowance; benefit enrollment only inside window or within 30 days of a life event.

| Event | Trigger | Payload | Consumers |
|---|---|---|---|
| `dev.passage.hr.employment.changed.v1` | action applied | see 8.0 | payroll, projects, service, SCIM |
| `dev.passage.hr.position.assignment.changed.v1` | assignment start/end | workerId, positionId, validity, managerPositionId | authorization (manager roles), workflow routing |
| `dev.passage.hr.absence.approved.v1` | request approved | workerId, planId, from, to, hours | time, projects, payroll |
| `dev.passage.hr.benefit.enrollment.changed.v1` | enrollment start/end | workerId, planId, option, validity, employeeRate | payroll deductions |

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

Validation: calculate requires HR cutoff passed and all time registrations for the period approved (or policy allows unapproved as 0 hours); finalize requires zero BLOCK validation results; expense submit requires every REQUIRE_* satisfied.

| Event | Trigger | Payload | Consumers |
|---|---|---|---|
| `dev.passage.payroll.run.posted.v1` | GL post | see 8.0 | journal, bank, analytics |
| `dev.passage.payroll.statement.finalized.v1` | finalize | statementId, workerId, net, payDate | ESS, provider adapter |
| `dev.passage.expense.report.approved.v1` | approval | see 8.0 | ap, projects |
| `dev.passage.expense.policy.violated.v1` | BLOCK outcome at submit | reportId, lineId, ruleId, outcome | analytics, audit |

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

Validation: readings for cumulative counters must be ≥ last_reading else `EAM-CTR-001` (reset requires explicit reset flag); COMPLETED requires all checklist mandatory items answered and all reserved parts issued or released; CAPITAL work orders require a fixed_asset_id or CIP project.

| Event | Trigger | Payload | Consumers |
|---|---|---|---|
| `dev.passage.eam.request.created.v1` | request created | see 8.0 | planner UI, notifications |
| `dev.passage.eam.workorder.status.changed.v1` | transition | workOrderId, from, to, at, actor | mobile, analytics |
| `dev.passage.eam.workorder.completed.v1` | COMPLETED | see 8.0 | journal, fixed assets, scheduling |
| `dev.passage.eam.counter.reading.registered.v1` | reading | counterId, assetId, value, at, source | plan scheduler, condition rules |
| `dev.passage.eam.downtime.recorded.v1` | downtime on resource | resourceId, from, to, workOrderId | scheduling (wave 23), OEE (wave 25) |

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

### 8.44.8 Acceptance criteria

- **AC-44.1** (FR-44.5, BR-44.1) Given the BR-44.1 example, when the scheduler runs on 2026-10-21, then a WO exists with due 2026-10-30 and next_due_reading 12,500.
- **AC-44.2** (FR-44.5) Given 40 plan fixtures, when scheduled across a simulated year, then generated WO dates/readings match expected within 0 days / 0 units.
- **AC-44.3** (FR-44.11) Given a completed WO with 350.00 USD parts and 8 h labor at 45.00/h, when posted, then the GL maintenance expense is 710.00 and the asset cost history shows 710.00.
- **AC-44.4** (FR-44.12, BR-44.4) Given a bearing temperature rule > 85 °C, when a reading of 87 °C arrives, then a request exists within 5 s and a second reading of 88 °C within 60 min creates none.
- **AC-44.5** (FR-44.8, BR-44.5) Given a RELEASED WO with planned downtime 4 h on resource M-12, when wave-23 schedules, then M-12 capacity for that window is zero.
- **AC-44.6** (FR-44.3) Given a cumulative counter at 12,300, when a reading of 12,250 is posted without reset flag, then it is rejected with EAM-CTR-001.
- **AC-44.7** (FR-44.7, BR-44.3) Given a WO with an unanswered mandatory checklist item, when COMPLETED is requested, then it is rejected with `EAM-WO-004`.
- **AC-44.8** (FR-44.13, BR-44.8) Given 3 failures and 2,700 h uptime in a window, when KPIs are queried, then MTBF = 900 h.

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

Depends on: wave 16 inventory, wave 17 PO, wave 22 resources, wave 23 scheduler, wave 25 IoT, wave 37 fixed assets, wave 42 workers.

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Counter forecast inaccuracy | Medium | Low | Rolling usage; manual override |
| IoT volume | Medium | Medium | Partitioned readings; downsampled retention 13 months |

Open questions: (1) Capitalize CAPITAL WOs directly to fixed assets or always via CIP project? Default CIP project; decide by wave 44. (2) Calibration in v1? Default no.

## 8.45 Wave 45: Service Management & Field Service

### 8.45.1 Scope and business context

Service management replaces AX Service management (SMAServiceAgreementTable, SMAServiceOrderTable, SMAServiceObjectTable, SMADispatchBoard, SMARepair, service subscriptions), D365 Field Service (work orders, bookings, agreements, IoT alerts, mobile) and SAP CS/FSM (service notifications, service orders, IBase, warranties, SAP Field Service Management). After this wave: an installed base is built from shipped serials; agreements define entitlements, SLAs and pricing; cases become service work orders; a dispatcher schedules technicians with OR-Tools respecting skills, territories, time windows and travel; technicians execute offline on a PWA with photos, signatures and truck-stock parts; billing posts to the customer or warranty. This is the phase-exit wave.

Out of scope: customer-facing self-scheduling portal (v1.x), subscription billing beyond wave-29 recurring invoices, remote assist/AR, depot repair RMA beyond wave-30 RMA linkage.

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

Validation: case creation requires a customer; dispatch run requires all WOs to have geo and skills; complete action requires signature unless incident type exempts; post requires COMPLETED and all parts lines consumed.

| Event | Trigger | Payload | Consumers |
|---|---|---|---|
| `dev.passage.service.case.opened.v1` | case created | caseId, customerId, priority, agreementId, responseDue, resolutionDue | notifications, analytics |
| `dev.passage.service.sla.breached.v1` | breach monitor | see 8.0 | notifications, analytics |
| `dev.passage.service.dispatch.published.v1` | publish | see 8.0 | PWA, customer notifications |
| `dev.passage.service.workorder.completed.v1` | complete action | workOrderId, technicianId, lines[], firstTimeFix, durationMinutes | billing, inventory (truck consumption), KPIs |
| `dev.passage.service.workorder.posted.v1` | billing post | workOrderId, invoiceId, billableTotal, warrantyCost, entitlementConsumed | invoicing, analytics |

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
