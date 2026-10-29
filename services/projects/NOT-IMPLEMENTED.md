# NOT-IMPLEMENTED — projects (PSG-41, wave 41)

Slice 1 delivers: the project master over HTTP (create with sub-project inheritance, read, list, stage
transitions) with RLS, idempotency, optimistic concurrency and outbox events; the domain rules for POC, EVM,
CPM scheduling (forward and backward pass), posting-profile resolution, invoice-proposal selection,
retention and interest arithmetic and stage/finish rules; the store and API tests; OpenAPI, AsyncAPI, Helm
chart; and the projects web module (overview, list, create form, detail with stage moves).

Slice 2 (PSG-4102) delivers: project groups (table, API, project foreign key), posting profiles (table, API,
resolve endpoint over the FR-41.5 fallbacks), plan versions (WORKING, locked BASELINE copy, SIMULATION copies),
bulk WBS replacement, the CPM scheduler over stored nodes with early/late dates, slack, critical flag and
summary roll-up, the `wbs.scheduled` event, and web pages for groups, posting profiles, plan versions and the WBS.

Slice 3 (PSG-4103) delivers: the project contract value (set on create, raised by change orders); change
orders and claims (four types, impact lines on WBS nodes, DRAFT → SUBMITTED → APPROVED | REJECTED → APPLIED,
creator ≠ decider, customer approval evidence for customer claims) whose apply raises the contract value,
creates a new rescheduled BASELINE from the current one with the effort and schedule deltas, supersedes the old
baseline and emits `changeorder.applied`; the risk and issue register (probability, impact, score, owner,
mitigation, due date, status lifecycle); and web pages for the change order log, change order entry and detail
(submit, approve, reject, apply) and the risks and issues register.

Slice 4 (PSG-4104) delivers: project categories bound to one transaction type (`project_category`, `GET /categories`,
`PUT /categories/{categoryId}`, PROJ-CAT-001); the project transaction subledger (`project_transaction`) with
manual HOUR, COST, ITEM, FEE and ON_ACCOUNT entry priced per line half-even (BR-41.5), IN_PROCESS and currency
checks (PROJ-TXN-001/002), the type-bound category (PROJ-TXN-003, also a composite FK) and the WBS code stored
with the node; invoice proposals built from the BR-41.3 predicate with `FOR UPDATE SKIP LOCKED` (BR-41.8),
including the fixed-price milestone condition, with approve, cancel and remove-line returning transactions to
the pool; the "why not billed" explanation per transaction; BR-41.7 now refuses FINISHED while a DRAFT or
APPROVED proposal is open; `passage_app` grants for the new tables; and web pages for categories, transactions
(with the billing check), the invoice proposal list and build form, and proposal detail.

Status values: **later slice of PSG-41** (builds in this wave, not yet) or **handoff** (needs another
owner or a kernel change; see the reason).

## Ticket deliverables

| # | Deliverable | Status | Reason |
|---|---|---|---|
| D1a | Project hierarchy: sub-project inheritance of group, customer and contract; sub-project cannot change customer | **built** at create | `domain.InheritFromParent` runs in `CreateProject`; a different customer or legal entity is `PROJ-PARENT-002`. There is no update or re-parent endpoint yet (later slice of PSG-41), so "cannot change customer" is enforced only at create. |
| D1b | Project group entity (type, posting profile) and `ProjectGroup` FK | **built** (slice 2) | `project_group` table, `GET /groups`, `PUT /groups/{groupId}` (ProjSetupAdmin, If-Match), and `project.project_group_id` is a foreign key (`PROJ-GROUP-001`). The FK is added `NOT VALID`, so slice-1 rows created before groups existed are not re-checked. The group carries an optional default project type; it does not yet force the project's type, and the group-scoped parameters of §8.41.13 (estimate method default, project stock valuation, indirect cost component group, interest profile) are later slices. The legal-entity parameter "default project group per project type" is not built (parameters table later slice). |
| D1c | Stage ON_HOLD and transition gating by duty | **built** | `POST /v1/projects/{id}/transitions` with `ProjManage`. |
| D1d | WBS nodes, plan versions (BASELINE/WORKING), replace-WBS API, `schedule` API | **built** (slice 2), with gaps | `plan_version`, `wbs_node`, `wbs_dependency`; `POST/GET /{id}/plans`, `GET/PUT /{id}/plans/{ver}/wbs`, `POST /{id}/plans/{ver}/schedule`; `wbs.scheduled` event. Gaps: FNLT and MSO constraints are refused at validation (the scheduler has no finish or must-start constraint and no negative slack yet; later slice of PSG-41). `duration_days` is whole working days (integer), not the PRD's `numeric(8,2)`, because the CPM works in days. `wbs_node` has no `project_id` column; the project is reached through `plan_version`. "Approval" of a baseline is the copy itself (the baseline is locked on creation); there is no approval workflow. One current BASELINE per project is enforced by a unique index; since slice 3 an applied change order creates a new baseline and marks the old one `superseded_by_version_id` (BR-41.13). The scheduler always uses the standard Monday-to-Friday calendar without holidays (see D1e). |
| D1e | Scheduler over wave-8 role calendars | handoff (wave 8) | Slice uses a `Calendar` value (weekday mask and holidays); wave-8 calendars do not exist yet. |
| D1f | Resource roles and soft/hard bookings, over-allocation checks, price precedence | later slice of PSG-41 | Needs worker records from wave 42. Role/booking tables not built. |
| D1g | Forecasts and budgets (`ProjectForecast`, budget copy) | later slice of PSG-41 | Budget control needs wave 38; forecast tables not built. |
| D2a | Hour, cost, item, fee and on-account transactions and journals; `ProjectTransaction` table | **partly built** (slice 4) | Built: `project_transaction`, `project_category` (type-bound, composite FK), `POST /transactions` (ProjJournalMaintain, `Idempotency-Key`), `GET /{id}/transactions`, amounts = quantity × unit price rounded half-even per line in the minor unit (BR-41.5), IN_PROCESS and project-currency checks, HOUR needs a worker and ITEM an item, a `wbsNodeId` must be a node of the project and its WBS code is stored. Not built: journals as documents (header, line numbers, validate, post) and their approval parameter (later slice of PSG-41, with D2b); the accounting- and reporting-currency columns (needs wave-12 rates; handoff); period control BR-41.6 `PROJ-PERIOD-001` (handoff, wave 14); hour price precedence FR-41.3 (later slice, with D1f; the caller gives the prices); the "default line property" parameter; integrated documents from timesheets, expenses, PO receipts and inventory journals (handoffs, waves 25, 43, 17, 16; only MANUAL is entered here); `worker_id`, `item_id` and `vendor_party_id` foreign keys (handoffs, waves 42, 15, 7); `estimate_id` (with D4b). Recorded transactions cannot be edited or deleted; corrections wait for journals. |
| D2b | Posting to the universal journal with key `projects:txn:{journalId}:{lineNo}` | handoff (wave 11) | Wave-11 posting API not available. `journal_id` stays empty and `dev.passage.projects.transaction.posted.v1` is not published, because nothing is posted yet. |
| D2c | Project purchase orders and subcontracts referencing WBS | handoff (wave 17) | Needs wave-17 PO model. |
| D3a | Invoice proposals: build from selection, edit, remove line, post to invoicing | **built** (slice 4) except posting, with gaps | `project_invoice_proposal` + lines; `POST /invoice-proposals` (ProjBillingMaintain, `Idempotency-Key`) builds a DRAFT for one project from the BR-41.3 predicate under `FOR UPDATE SKIP LOCKED` (BR-41.8), `GET /invoice-proposals?projectId=&status=`, `GET /invoice-proposals/{id}`, `approve` and `cancel` (If-Match), `DELETE /invoice-proposals/{id}/lines/{txnId}` (returns the transaction to the pool; an APPROVED proposal goes back to DRAFT), and `GET /transactions/{txnId}/billing` ("why not billed"). A TIME_MATERIAL project bills the requested types; a FIXED_PRICE project bills only ON_ACCOUNT lines on billable milestones whose scheduled early finish in the WORKING plan is on or before `to` (BR-41.3; the "% complete = 100" alternative waits for progress capture, later slice); other project types do not bill (service code PROJ-PROP-004). Gaps: POSTED and the hand-off to invoicing (D3c, handoff wave 29) and with it `dev.passage.projects.invoice-proposal.posted.v1` and the ProjBillingPost duty; editing a line's quantity or price (only removal); funding-source routing and `FUND-LIMIT` (D3d); tax amount (wave-10 tax, handoff); multi-project proposals by contract; the `PROJ-PROP` number (kernel sequence, handoff); the printed pre-bill proposal document (wave 35, handoff). Approve takes ProjBillingMaintain (the PRD gives approval no separate duty). No events for build, approve, cancel or line removal (the PRD lists only `posted`). |
| D3b | Billing rules and T&M, fixed-price milestone, retainer rule types | later slice of PSG-41 (T&M and fixed-price milestone selection built by project type) | There is no BillingRule entity: the selection's types come from the request and the project type (D3a), so PROJ-PROP-001 (rule currency ≠ project currency) cannot arise yet. Retainers (BR-41.4: on-account invoices as ON_ACCOUNT lines with negative sales, applied oldest first on T&M proposals) and unit-of-delivery rules are not built; a negative ON_ACCOUNT sales price can be recorded but is not netted automatically. |
| D3c | Hand-off of proposals to invoicing | handoff (wave 29) | Wave-29 invoicing not available. |
| D3d | Customer funding sources, percentage split, limits, `FUND-LIMIT` hold | later slice of PSG-41 | Not built. Multi-currency splitting is out of scope per PRD. |
| D4a | POC (cost-to-cost) arithmetic, loss provision | **built (domain)**; persistence and API later slice of PSG-41 | `domain.ComputePOC` implements BR-41.1/41.2 and matches `testdata/poc-worked-example.csv`. |
| D4b | Estimate runs per (project, period), reverse-and-rerun, Temporal workflow | later slice of PSG-41 | Needs estimate tables, period control (wave 14) and Temporal (kernel and eventing workers). |
| D4c | WIP and accrued/deferred revenue postings | later slice of PSG-41 | Needs D2b posting and D4b. |
| D4d | Cost-to-complete tracking | later slice of PSG-41 | Part of D4b. |
| D4e | Posting profiles: resolution with fallbacks, `PROJ-POST-001` | **built** (slice 2) | `posting_profile` table, `GET/PUT /posting-profiles` (ProjSetupAdmin, If-Match) and `POST /posting-profiles/resolve`. Only resolvable keys can be stored (API check and table CHECK). The category is free text until the ProjectCategory table exists (later slice of PSG-41, with D2a), and a concrete project group in a key is not checked against `project_group` (rows may be set up before the group). Accounts are free text until the wave-11 chart of accounts can be validated (handoff, wave 11). |
| D5a | Committed-cost projection from PO/requisition events | handoff (wave 17) and later slice of PSG-41 | Needs PO events from wave 17. |
| D5b | EVM (PV/EV/AC/CPI/SPI/EAC/VAC) | **built (domain)**; API later slice of PSG-41 | `domain.ComputeEVM` implements FR-41.10. `GET /v1/projects/{id}/evm` is not built; PV needs a plan baseline (D1d). |
| D5c | Profitability and project statement reports | later slice of PSG-41 | Needs D2a and D4c. |
| D6a | Weekly timesheets, periods, favorites, delegates, line approval workflow, missing-timesheet inquiry | later slice of PSG-41 | Needs the wave-4 workflow service (handoff) and the wave-25 time registrations (handoff). |
| D6b | Project quotations converted to projects (`PROJ-QUO-001`) | handoff (wave 27) and later slice of PSG-41 | Needs wave-27 quotations. |
| D7a | Customer and vendor retention terms, retention ledger, release proposals | later slice of PSG-41 (arithmetic built) | `domain.RetainedAmount` (BR-41.10 half-even amount) and `domain.CheckRelease` (trigger check, `PROJ-RET-001`, override) are built and tested. Terms, ledger, release proposals and APIs are not. Vendor side needs wave 18. |
| D7b | Indirect cost components, rules, INDIRECT transactions | later slice of PSG-41 | Needs D2a. |
| D7c | Project interest (IAS 23 capitalization) | later slice of PSG-41 (rate tables handoff, wave 12; CIP handoff, wave 37) | `domain.InterestCharge` (BR-41.12, ACT/365 and ACT/360; the rate is a fraction, 0.06 for 6 %) is built and matches the 5,095.89 example. Profiles, the average-balance source, the monthly run and postings are not. |
| D8a | Change orders and claims lifecycle; apply creates new baseline | **built** (slice 3), with gaps | `project_change_order` + lines, `POST/GET /change-orders`, `GET /change-orders/{coId}`, `submit`/`approve`/`reject`/`apply`; BR-41.13 apply (contract value, new BASELINE with deltas, superseded old baseline, `changeorder.applied`); AC-41.12 is a store and API test. Gaps: the wave-38 project budget is not changed here (handoff, wave 38: the event carries `budgetDelta` for budget control to apply); the CLAIM_VENDOR vendor debit note draft is not created (handoff, wave 18: the event carries `vendorPartyId` and the cost); attachments are not stored (handoff, document storage owner; the approval evidence is the `customerApprovalRef` text); the printed customer-facing change order document `PROJ-CO` (§8.41.15) is not built (handoff, wave 35 document hub); the "customer approval required" parameter is fixed at its default true (later slice of PSG-41, with the parameters table); the number is CO-0001 per project until the kernel sequence `PROJ-CO` exists (handoff, platform kernel); `vendorPartyId` has no party FK (handoff, wave 7). Apply changes the BASELINE only, not the WORKING plan (the PRD says baseline). Apply is guarded by `If-Match` and is idempotent by state (a second apply is PROJ-CO-001) rather than by an `Idempotency-Key`. Create, submit and decide publish no event (the PRD lists only `applied`). |
| D8b | Simulation plan versions, three-way transfer | partly built (slice 2); rest later slice of PSG-41 | SIMULATION versions copied from WORKING or BASELINE are built (with `copied_from_version_id`), and can be edited and scheduled independently. Side-by-side compare, the three-way transfer with `PROJ-SIM-001` conflicts (BR-41.14), locking after transfer, costing of a simulation and the "max simulation versions per project" parameter (default 20) are not built. |
| D8c | Project stock and deliveries (`project_id` ownership dimension, planning scope, shipments) | handoff (waves 16, 22, 28) | Those waves own the dimension, scope and shipment. |
| D8d | Forecast models and risk and issue register | risk and issue register **built** (slice 3); forecasts later slice of PSG-41 | `project_risk_issue`, `GET/POST /{id}/risks`, `PUT /{id}/risks/{riskId}` (ProjPlan, If-Match), score = probability × impact, status lifecycle with `PROJ-RISK-001`. `ownerWorkerId` is a uuid without a worker FK (handoff, wave 42). No risk events (none in the PRD). Forecast models, copy-to-budget and forecast-vs-actual (FR-41.20 first half, `GET /{id}/forecasts/{modelId}`) are not built: they need D1g and D2a. |
| Deploy | Projects workspace in the shell; demo `ES-US` entity with 25 live projects | partly built; handoff (deploy owner) | The projects module registers through `module.ts` (shell `import.meta.glob`). The demo data loader and Argo app are outside this wave's owned paths. |

## Acceptance criteria

| # | Criterion | Status | Reason |
|---|---|---|---|
| AC1 | Estimate fixtures match an accountant-reviewed workbook | later slice of PSG-41 + handoff (accountant sign-off) | `testdata/poc-worked-example.csv` covers the BR-41.1/41.2 worked example. The PRD workbook `fixtures/projects/poc-workbook.xlsx` and sign-off are not in this repo. Domain test `poc_test.go` checks the periods. |
| AC2 | Invoice proposals capture exactly the billable transactions (property test) | **built** (slice 4) | `TestSelect_NoFalsePositivesOrNegatives` (rapid, up to 200 candidates per run, now with the fixed-price milestone condition) compares `domain.Select` with an independent formula; the store re-checks every SQL-selected row with `domain.Select`. Store tests `TestProposals_BuildRemoveCancelAndFinishGate`, `TestProposals_FixedPriceSelectsDueMilestones` and `TestProposals_ConcurrentBuildsAreDisjoint` (six parallel builds, BR-41.8) run the persisted build. The PRD's 10,000-row generated database fixture is not in `testdata/`. |
| AC3 | WBS scheduling respects dependencies and resource calendars | partly built | Domain CPM with dependencies and working calendars is tested in `schedule_test.go`; the persisted WBS is scheduled through the API (store and API tests). Role calendars (wave 8, handoff) are not wired: the API schedules on the standard calendar. |
| AC4 | Project subledger reconciles to GL WIP and revenue | later slice of PSG-41 + handoff (wave 11) | Needs D2b and D4c. |
| AC5 | Every deliverable has code or is listed here | **this file** | |
| AC6 | `go vet`/`go test`, web `vitest`/`tsc`, OpenAPI/AsyncAPI match, chart lints | **built** for slices 1 to 4 (`TestOpenAPIMatchesRoutes` checks the routes against `api/openapi.yaml`) | Store tests run only with `PASSAGE_TEST_DATABASE_URL` and `PASSAGE_TEST_APP_DATABASE_URL`; they were run against Postgres 16 as the `passage_app` role (real grants) in the slice-1 to slice-4 reviews. |

## Acceptance scenarios from PRD §8.41.8 (not yet testable end to end)

| Scenario | Status | Reason |
|---|---|---|
| AC-41.1 POC revenue over periods 1 and 2 | partly built | Domain values are tested; the journal balance check needs D4c. |
| AC-41.2 Loss provision 20,000 | partly built | Domain `LossProvision` is tested; the GL account needs D4e. |
| AC-41.3 Proposal selection, 10,000 transactions | partly built | Property test over generated candidates and store tests over persisted transactions (AC2); the 10,000-row database fixture is not built. |
| AC-41.4 FS lag 2 days on weekend calendar | built (standard calendar) | Domain test covers the rule (lag counts calendar days, then rolls to the next working day); the store and API tests schedule a stored FS + 2 link through `POST .../schedule`. Holiday calendars per role need wave 8. |
| AC-41.5 Hour journal to WIP and payroll allocation | later slice of PSG-41 | Needs D2a and D4e. |
| AC-41.6 PO receipt creates ITEM and lowers committed cost | handoff (wave 17) and later slice | Needs D5a. |
| AC-41.7 FUND-LIMIT hold | later slice of PSG-41 | Needs D3d. |
| AC-41.8 EVM example (CPI 1.00, SPI 1.20) | partly built | Domain `ComputeEVM` implements the formulas; the query endpoint is not built. |
| AC-41.9 Timesheets | later slice of PSG-41 | Needs D6a. |
| AC-41.10 Retention 10 % | later slice of PSG-41 | Needs D7a. |
| AC-41.11 Indirect cost 12 % | later slice of PSG-41 | Needs D7b. |
| AC-41.12 Change order apply | **built** except the budget | Store test `TestChangeOrders_ApplyAC4112` and API test `TestChangeOrder_ApplyAC4112`: contract value +50,000.00, `budgetDelta` 30,000.00 on the event, a new BASELINE, second apply PROJ-CO-001. The wave-38 budget itself is a handoff (D8a). |
| AC-41.13 Interest 5,095.89 | later slice of PSG-41 | Needs D7c. |
| AC-41.14 Quotation conversion | handoff (wave 27) and later slice | Needs D6b. |
| AC-41.15 Simulation transfer conflicts | later slice of PSG-41 | Simulation copies exist (slice 2); the transfer does not. |

## Known gaps that matter now

| Item | Status | Reason |
|---|---|---|
| BR-41.7 FINISHED blockers (open journals, open proposals, non-zero WIP) | partly built (slice 4) | `TransitionProject` counts DRAFT and APPROVED proposals under the project lock and refuses FINISHED with PROJ-STAGE-002 (a proposal build holds the project row `FOR SHARE`, so a concurrent build cannot slip in). Open journals and the WIP balance are not passed yet (D2a journals, D4c). |
| Project id from number sequence `PROJ` (wave 5) | handoff (platform kernel) | The kernel `sequence` package does not exist yet. Slice 1 takes the id from the caller, validated against `^[A-Z0-9][A-Z0-9-]{0,19}$`. |
| `dimension_set_id` (default financial dimensions) | later slice of PSG-41 + handoff (dimensions wave) | Column not in the migration. |
| `customer_party_id` and `contract_id` foreign keys | handoff (party, wave 7; contracts) | Stored as uuid without FK until party and contract tables exist. |
| Tenant hash partitioning (16) for `ProjectTransaction` | later slice of PSG-41 | `project_transaction` exists (slice 4) as a plain table with the `(tenant_id, project_id, txn_date)` index and the partial index on unbilled chargeable rows; partitioning by tenant hash is not done. |
| Transaction list pagination | later slice of PSG-41 | `GET /{id}/transactions` returns the newest 500 (`store.BillingListLimit`) without a cursor; the Posted transactions inquiry filters of §8.41.14 (WBS, type, category, worker, source) are not built. |
| `GET /v1/projects/openapi.yaml`, `/asyncapi.yaml`, `/error-codes` | handoff (platform kernel) | Kernel-standard endpoints; the files exist under `api/` and `events/`. |
| Idempotency through the `idempotency_keys` table and kernel middleware | handoff (platform kernel) | Slice 1 stores the key on the project row (`idempotency_key`, unique per tenant, race-safe through `ON CONFLICT`), as the reference service does; the `idempotency_keys` table from `0002_outbox.sql` is created but unused. Stage moves are guarded by `If-Match` and take no key. Slice 2 does the same for plan versions (`plan_version.idempotency_key`, checked under the project row lock); group and profile upserts and the WBS replace are PUTs guarded by `If-Match`, and the schedule run is repeatable, so they take no key. The kernel middleware is not used. |
| Events for project-group, posting-profile and plan-version changes | later slice of PSG-41 | The PRD event table (§8.41.5) lists none and no consumer needs them yet; only `wbs.scheduled` is published for plans. Likewise categories, recorded (unposted) transactions and proposal build, approve and cancel publish nothing in slice 4. |
| `/metrics` on the admin port (9090) | later slice of PSG-41 | The chart declares the admin port and an optional ServiceMonitor (off by default), but the binary serves no metrics yet. |
| `DATABASE_URL_FILE`, OTel, Kafka, OPA sidecar configuration | handoff (platform kernel, deploy) | The entrypoint reads `DATABASE_URL` and `HTTP_ADDR` only. |
| Gateway duty checks via OPA (`x-passage-duty` enforcement at the edge) | handoff (edge-identity) | This service checks duties in-process through `authz.RequireDuty`. |
| BFF rewrite of `/api/v1/projects/*` to `/v1/projects/*` | handoff (web-bff) | Browser module calls `/api/v1/projects`; the rewrite lives in the BFF. |
| Argo CD application, image digest, ES-US demo data | handoff (deploy) | Outside this wave's owned paths. |

## Web screens (PRD §8.41.6) not built yet

The projects module has the Projects workspace (counts by stage), the Projects list (stage and type filters),
project entry (project and sub-project), Project detail (header, stage moves, link to plans), Project groups,
Posting profiles (with an account lookup), the plan versions of a project and the WBS page (scheduled table
with dates, slack and critical tasks, a row editor that saves the whole WBS, and the scheduler run), and since
slice 3 the contract value on Project detail, the Change orders log, change order entry and detail (submit,
approve, reject, apply) and the Risks and issues register, and since slice 4 Project categories, the project's
Transactions (list, single-line entry, "check billing" for a window), Invoice proposals (list by status, build
from a window and type) and proposal detail (lines, remove line, approve, cancel). Everything below needs the
service slice that backs it.

| Screen or element | Status | Reason |
|---|---|---|
| Workspace tiles: proposals to approve, estimates due, over-budget, unposted journals | later slice of PSG-41 | Proposals exist (slice 4) but there is no cross-project proposal query for a tile yet; the others need D4b, D1g and D2a. |
| Projects list columns name, budget, actual, committed, remaining, CPI, SPI; filters group, customer, project manager | later slice of PSG-41 | The project has no name or manager field yet (not in the §8.41.4 data model either); the measures need D1g, D2a, D5a and D5b. |
| Project detail: copy, funding, forecasts, statements | later slice of PSG-41 | Need the copy command, D3d, D1g and D5c. |
| WBS Gantt bars and the Gantt keyboard rules (Tab indent, Shift+Tab outdent, Ctrl+L link) | later slice of PSG-41 | The WBS page is a table editor: the tree comes from the dotted codes and dependencies are entered as rows. A bar chart needs a charting approach the shell owner (wave 5) has not provided (no new npm dependencies). Role and % complete columns wait for D1f and D2a. |
| Project detail: posting profile that applies to the project | later slice of PSG-41 | Needs the project category of a posting (D2a); the Posting profiles page has a lookup. |
| Change orders: attach approval as a file | handoff (document storage) | The detail page takes the approval reference as text. |
| Forecasts (forecast lines by period, copy to budget) on the risks page | later slice of PSG-41 | Needs D8d forecasts. |
| Owner picker on risks | handoff (wave 42) | The owner is a worker id typed as a UUID until worker records exist. |
| Journals as batches (add lines, validate, post) and bulk edit of category or line property | later slice of PSG-41 | Transactions are entered one line at a time until journals exist (D2a, D2b). |
| Invoice proposals: funding split column, grid personalization, post | later slice of PSG-41 | Need D3d, a per-user view store, and D3c. |
| Resource board, Estimates, Project statements / EVM, My timesheet, Timesheet approval, Plan version compare, Retention ledger | later slice of PSG-41 | Each needs its deliverable above (D1f, D4b, D5b, D6a, D8b, D7a). |
