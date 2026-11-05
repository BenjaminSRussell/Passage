# projects

Project operations for Passage: project master and hierarchy, stages, WBS and CPM scheduling,
POC and EVM arithmetic, billing selection rules and posting-profile resolution. Wave 41, ticket PSG-41.

**Slice 1** exposes the project master over HTTP (create with sub-project inheritance, read, list, stage
transitions), persists it with RLS and an outbox event per change, and ships the pure domain rules that later
wave-41 slices wire into persistence and APIs. **Slice 2** adds project groups (now a foreign key of the
project), posting profiles with the FR-41.5 fallback lookup, plan versions (one WORKING, one locked BASELINE
copied from it, SIMULATION copies), bulk WBS replacement and the CPM scheduler over stored WBS nodes with the
`wbs.scheduled` event. **Slice 3** adds the project contract value, change orders and claims (DRAFT →
SUBMITTED → APPROVED | REJECTED → APPLIED, creator ≠ decider) whose apply raises the contract value, creates a
new BASELINE from the current one with the effort and schedule deltas and emits `changeorder.applied`, and the
risk and issue register. **Slice 4** adds project categories, project transactions priced per line
(FR-41.4, BR-41.5) and invoice proposals built from the BR-41.3 selection under `FOR UPDATE SKIP LOCKED`
(BR-41.8), with approve, cancel, remove line, the fixed-price milestone condition, the "why not billed"
explanation and the BR-41.7 open-proposal finish gate. See [NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md) for what
is still missing and why.

## Layout

```
cmd/projects/            entrypoint: dbx.Connect → (dbx.Migrate if MIGRATE_ON_START) → httpx.NewRouter → Register → httpx.Serve
internal/domain/         pure rules, no I/O: project validation, sub-project inheritance and stages (FR-41.1),
                         POC (BR-41.1/41.2), EVM (FR-41.10), CPM forward and backward pass over working calendars
                         with late dates, slack and critical path (FR-41.2), posting-profile fallback (FR-41.5),
                         invoice-proposal selection predicate and "why not billed" (BR-41.3), finish blockers (BR-41.7),
                         retention amount and release trigger (BR-41.10), monthly interest (BR-41.12),
                         change-order lifecycle, decision rules and apply arithmetic (FR-41.18, BR-41.13),
                         risk register validation and status moves (FR-41.20), transaction validation and
                         half-even line pricing (FR-41.4, BR-41.5), proposal types by project type and the
                         proposal lifecycle (FR-41.6)
internal/store/          Postgres repository (pgx); every call runs in dbx.WithTenantTx (RLS); writes outbox rows
internal/events/         event type constants and payloads (events/asyncapi.yaml)
internal/api/            HTTP handlers, DTOs, validation, problem mapping
migrations/              0001_init.sql (project + RLS), 0002_outbox.sql (outbox, consumer inbox, idempotency),
                         0003_setup_plans.sql (project_group + project FK, posting_profile, plan_version, wbs_node,
                         wbs_dependency, all with RLS and legal-entity scope through the project),
                         0004_risk_change_order.sql (project.contract_value, superseded baselines,
                         project_change_order + lines, project_risk_issue), 0005_grants.sql and
                         0006_revoke_unused_delete.sql (passage_app privileges), 0007_transactions_billing.sql
                         (project_category, project_transaction, project_invoice_proposal + lines, with RLS,
                         legal-entity scope and passage_app grants), migrations.go (embed)
api/openapi.yaml         OpenAPI 3.1 for every endpoint
events/asyncapi.yaml     AsyncAPI 3 for every event published
charts/projects/         Helm chart (deployment, service, optional ServiceMonitor)
Dockerfile               distroless image, built from the repository root (`docker build -f services/projects/Dockerfile .`)
testdata/                poc-worked-example.csv: BR-41.1/41.2 worked example (period 1 to 3);
                         proj-estimates-12x4.csv: 12 projects x 4 estimates from the independent
                         reference gen_poc_estimates.py (run `python3 -I testdata/gen_poc_estimates.py`)
```

Web module: `apps/web/src/modules/projects/`: projects overview (counts by stage), all-projects list
(stage and type filters, load more), new project / sub-project form, project detail with stage moves,
project groups (list, add, edit), posting profiles (list, add, edit by level, account lookup), plan versions
of a project (create working plan, save baseline, new simulation) and the WBS page (scheduled dates, slack and
critical tasks; editor that saves the whole WBS under If-Match; run schedule; baselines read-only), change
orders of a project (log with type and status filters, create with impact lines on WBS nodes, detail with
submit, approve or reject with the customer approval reference, and apply showing the new contract value and
baseline), and the risks and issues register (list by score, add, edit and status moves).

## Endpoints

| Method | Path | Duty | Notes |
|---|---|---|---|
| POST | `/v1/projects` | projects.maintain | `Idempotency-Key` required (≤ 255 chars); 201 created, 200 on replay with `Idempotent-Replayed: true`. A sub-project inherits omitted group, customer and contract from its parent (FR-41.1) |
| GET | `/v1/projects` | projects.inquire | `stage`, `projectType`, `limit` (1–100), `cursor` |
| GET | `/v1/projects/{id}` | projects.inquire | `ETag: "<version>"` |
| POST | `/v1/projects/{id}/transitions` | projects.manage | body `{"to": "..."}`; `If-Match: "<version>"` required |
| GET | `/v1/projects/groups` | projects.inquire | every project group of the tenant |
| PUT | `/v1/projects/groups/{groupId}` | projects.setup.admin | no `If-Match` creates (201); replacing needs `If-Match` (428 without, 412 when stale) |
| GET | `/v1/projects/posting-profiles` | projects.inquire | ordered by key |
| PUT | `/v1/projects/posting-profiles` | projects.setup.admin | key in the body: (group, category, property), (group, \*, \*) or (\*, \*, \*); `If-Match` as for groups |
| POST | `/v1/projects/posting-profiles/resolve` | projects.inquire | concrete key; returns the row the FR-41.5 fallback picks, or PROJ-POST-001 |
| GET | `/v1/projects/{id}/plans` | projects.inquire | plan versions by sequence; 404 for an unknown project |
| POST | `/v1/projects/{id}/plans` | projects.plan | `Idempotency-Key` required; body `{"kind", "copyFromVersionId"?}` (rules below) |
| GET | `/v1/projects/{id}/plans/{ver}/wbs` | projects.inquire | plan version, nodes (WBS-code order) and dependencies; `ETag` = plan version |
| PUT | `/v1/projects/{id}/plans/{ver}/wbs` | projects.plan | replaces every node and dependency; `If-Match` required; clears scheduled dates |
| POST | `/v1/projects/{id}/plans/{ver}/schedule` | projects.plan | CPM run; stores early/late dates, slack, critical flag; writes `wbs.scheduled` |
| POST | `/v1/projects/change-orders` | projects.maintain | `Idempotency-Key` required; DRAFT with impact lines; number CO-0001… per project |
| GET | `/v1/projects/change-orders?projectId=` | projects.inquire | change order log, `type` and `status` filters; totals, no lines |
| GET | `/v1/projects/change-orders/{coId}` | projects.inquire | with lines; `ETag` |
| POST | `/v1/projects/change-orders/{coId}/submit` | projects.maintain | `If-Match`; optional body `{"customerApprovalRef"}` |
| POST | `/v1/projects/change-orders/{coId}/approve` | projects.change-order.approve | `If-Match`; approver ≠ creator; optional `customerApprovalRef` |
| POST | `/v1/projects/change-orders/{coId}/reject` | projects.change-order.approve | `If-Match`; decider ≠ creator |
| POST | `/v1/projects/change-orders/{coId}/apply` | projects.change-order.approve | `If-Match`; BR-41.13 (below); second apply PROJ-CO-001 |
| GET | `/v1/projects/{id}/risks` | projects.inquire | highest score first; `kind`, `status` filters |
| POST | `/v1/projects/{id}/risks` | projects.plan | `Idempotency-Key` required; starts OPEN |
| PUT | `/v1/projects/{id}/risks/{riskId}` | projects.plan | `If-Match`; fields and status |

`GET /healthz` is the kernel liveness check; `GET /readyz` pings the database (503 when it is down).
Browser calls use `/api/v1/projects/...`; the BFF rewrites them to `/v1/projects/...`.

Stage moves: CREATED → IN_PROCESS; IN_PROCESS ⇄ ON_HOLD; IN_PROCESS → FINISHED. FINISHED is terminal.

Plan versions (FR-41.2, FR-41.17): a project has at most one WORKING plan (empty, or a copy of another
version) and one BASELINE, which is a copy of the WORKING plan and is locked on creation (its WBS cannot be
replaced or rescheduled). A SIMULATION is a copy of the WORKING or BASELINE plan. Copies take the nodes with
their scheduled dates and the dependencies under fresh ids.

WBS rules: codes are dotted numbers (`1.2.3`), unique per plan version (PROJ-WBS-002); parents must exist and
not form a loop; durations are whole working days (0 is a milestone); constraints are ASAP or SNET (FNLT and
MSO are refused until the scheduler applies them); dependencies (FS, SS, FF, SF, lag in calendar days) link
tasks, each pair once, without cycles (PROJ-WBS-001). A node with children is a summary: the scheduler
schedules the tasks and rolls their dates, slack and critical flag up to the summary. The scheduler uses the
standard Monday-to-Friday calendar from the project start date. The domain scheduler can run each
activity on its own resource calendar (`domain.Activity.Calendar`); the API passes none until wave-8
role calendars exist.

Change orders (FR-41.18, BR-41.13): a line has a cost and revenue impact (rounded half-even to the project
currency, negative allowed) and, when it names a WBS node of any plan version of the project, an effort and
schedule delta; the node's WBS code is stored with the line. The creator submits; someone else approves or
rejects and applies (§8.41.17), and the user identity header is required for these calls. A CLAIM_CUSTOMER
needs `customerApprovalRef` to be approved (the §8.41.13 parameter defaults to on and is not yet configurable).
Apply, in one transaction: contract value += Σ revenue impact; a new locked BASELINE is copied from the current
baseline with the deltas on the nodes of the lines' codes and rescheduled; the previous baseline gets
`supersededByVersionId`; the change order becomes APPLIED with `appliedBaselineVersionId`; the outbox gets
`changeorder.applied` and `project.updated` (`changedFields: [contractValue]`). The WORKING plan is not changed.

Risk register (FR-41.20): probability and impact 1 to 5, score = probability × impact; OPEN → MITIGATED |
CLOSED, MITIGATED → OPEN | CLOSED; a CLOSED entry is final.

## Run

```
DATABASE_URL=postgres://passage_app:...@localhost:5432/passage_projects \
MIGRATE_ON_START=true HTTP_ADDR=:8080 go run ./cmd/projects
```

`MIGRATE_ON_START` is for dev only; stage and prod run migrations as the Argo PreSync Job with the owner role.

## Test

```
go vet ./... && go test ./...
PASSAGE_TEST_DATABASE_URL=postgres://... go test ./internal/store/...   # DB-backed tests
```

- `internal/domain`: table-driven and property tests (pgregory.net/rapid). No database needed.
- `internal/api`: handler tests over an in-memory store that mirrors the Postgres version and idempotency rules.
- `internal/store`: tenant and legal-entity RLS isolation, sub-project inheritance, idempotency replay
  (including concurrent requests with one key), duplicate ids, version conflicts, filters and paging, and the
  outbox CloudEvent; for slice 2 the group foreign key, group and profile upserts under If-Match, the
  table check that refuses unresolvable profile keys, plan-version kinds and copies, the locked baseline,
  concurrent WORKING creation, WBS replace and schedule with the `wbs.scheduled` outbox row, and legal-entity
  scope on plans, nodes and dependencies; for slice 3 AC-41.12 end to end (contract value, superseded and new
  rescheduled baseline, outbox payloads, second apply), rollback of a refused apply, change-order keys and a
  concurrent same-key create, WBS node resolution, the risk register lifecycle, and tenant and legal-entity
  scope on change orders, lines and risks; for slice 4 category type lock, transaction rules, keys and
  scope, proposal build, replay, remove line, cancel and the finish gate, fixed-price milestones over a
  scheduled WBS (also after a WBS replace), six concurrent builds staying disjoint (BR-41.8), and that the
  app role cannot delete transactions, proposals or categories. Skipped without `PASSAGE_TEST_DATABASE_URL`; also needs `PASSAGE_TEST_APP_DATABASE_URL`
  (uses `testkit.DBWithApp`, so the app role has no BYPASSRLS).

Web: `npx vitest --run src/modules/projects` and `npx tsc --noEmit` from `apps/web` (tests in `module.test.tsx`,
`plans.test.tsx`, `registers.test.tsx` and `billing.test.tsx`).

Chart: `helm lint charts/projects`.

## Dependencies

Only the platform module and what it already pulls in (chi, pgx/v5, shopspring/decimal, google/uuid, rapid for tests).
No new third-party modules.

## Error codes

Problem details are RFC 9457 (`application/problem+json`), `type` = `https://passage.dev/problems/<code>`.

| Code | HTTP | Meaning |
|---|---|---|
| COM-0001 | 400 | Request failed validation; `errors` names the fields (includes a missing `Idempotency-Key`, and a sub-project still incomplete after inheriting from its parent) |
| COM-0428 | 428 | `If-Match` missing on a stage move or a WBS replace, or on replacing an existing group or posting profile |
| AUTH-0001 | 401 | Missing or malformed tenant, or no user identity on a change-order write |
| AUTH-0003 | 403 | Caller lacks the route's duty (kernel `authz`), or the project's legal entity is outside the caller's scope |
| COM-0500 | 500 | Unexpected error; detail hidden (kernel `httpx.Fail`) |
| PROJ-NOT-FOUND | 404 | No project (or plan version of that project) with that id in the caller's scope |
| PROJ-CREATE-001 | 409 | A project with this id already exists |
| PROJ-PARENT-001 | 422 | Parent project does not exist, or is outside the caller's legal-entity scope |
| PROJ-PARENT-002 | 422 | Sub-project conflicts with its parent: another customer or another legal entity (FR-41.1; service-specific, not in §8.41.18) |
| PROJ-GROUP-001 | 422 | The project's group does not exist (service-specific) |
| PROJ-PLAN-001 | 422 | Plan-version rule: a second WORKING or BASELINE, a BASELINE not copied from WORKING, a SIMULATION without a WORKING or BASELINE source, or a source of another project (service-specific) |
| PROJ-PLAN-002 | 422 | The plan version is locked (a BASELINE) and cannot be replaced or rescheduled (service-specific) |
| conflict_version | 412 | `If-Match` does not match the stored version (someone else changed the row), or `If-Match` sent to create a group or profile that does not exist |
| idempotency_key_reuse | 422 | Same `Idempotency-Key` sent with a different body |
| PROJ-STAGE-001 | 422 | Stage move not allowed (FR-41.1) |
| PROJ-STAGE-002 | 422 | Project cannot be FINISHED (BR-41.7): raised on the stage move while a DRAFT or APPROVED invoice proposal is open (journals and WIP not counted yet, see NOT-IMPLEMENTED) |
| PROJ-POST-001 | 422 | No posting profile at any fallback level (FR-41.5), from `/posting-profiles/resolve` |
| PROJ-WBS-001 | 422 | Dependency cycle in a WBS replace (FR-41.2) |
| PROJ-WBS-002 | 422 | Duplicate WBS code in a WBS replace (FR-41.2) |
| PROJ-CO-001 | 422 | Change order is already APPLIED (second apply, or any move of an applied change order) (BR-41.13) |
| PROJ-CO-002 | 422 | Customer approval reference required to approve or apply a customer claim (BR-41.13) |
| PROJ-CO-003 | 422 | Change-order lifecycle move not allowed (service-specific) |
| PROJ-CO-004 | 422 | The creator of a change order cannot approve, reject or apply it (§8.41.17 SoD; service-specific) |
| PROJ-CO-005 | 422 | Apply needs a current BASELINE plan version (service-specific) |
| PROJ-CO-006 | 422 | An impact line cannot be applied: its WBS code is not in the current baseline, it changes a summary node's duration, or it makes the contract value, effort or duration negative (service-specific) |
| PROJ-RISK-001 | 422 | Risk or issue status move not allowed, or the entry is CLOSED (service-specific) |
| PROJ-CAT-001 | 422 | A category's transaction type cannot change (service-specific) |
| PROJ-TXN-001 | 422 | Transactions need an IN_PROCESS project (§8.41.5 command validation; service-specific) |
| PROJ-TXN-002 | 422 | Transaction currency differs from the project currency (service-specific) |
| PROJ-TXN-003 | 422 | The category does not exist or is for another transaction type (§8.41.5; service-specific) |
| PROJ-PROP-002 | 422 | No billable transactions matched the selection (FR-41.6) |
| PROJ-PROP-003 | 422 | Proposal lifecycle step not allowed: approve twice or without lines, move a CANCELED or POSTED proposal, remove a line from one (service-specific) |
| PROJ-PROP-004 | 422 | The project's type does not bill these transactions: a fixed-price project bills ON_ACCOUNT milestones only, INTERNAL, COST, INVESTMENT and TIME projects bill nothing (service-specific) |
| PROJ-RET-001 | — | Domain rule (BR-41.10): release before its trigger without override; raised by `domain.CheckRelease`, no HTTP path yet |

Other wave-41 codes from PRD §8.41.18 are not raised yet; they arrive with the slices that implement their rules.

## Events

`dev.passage.projects.project.created.v1` and `dev.passage.projects.project.updated.v1` are written to the outbox
in the same transaction as the change, with `subject` = project id and `actor` = the caller's user id.
`dev.passage.projects.wbs.scheduled.v1` (subject = plan version id; data `projectId`, `planVersionId`,
`criticalPath[]`, `finishDate`) is written by each scheduler run over a non-empty plan
(see `events/asyncapi.yaml`). `dev.passage.projects.changeorder.applied.v1` (subject = change order id; data
`changeOrderId`, `changeOrderNumber`, `projectId`, `type`, `currency`, `contractValueDelta`, `budgetDelta`,
`contractValue`, `newBaselineVersionId`, `vendorPartyId` for vendor claims) is written by each apply, with a
`project.updated` for the contract value. The PRD writes the type `change-order.applied`; the platform type rule
allows only `[a-z0-9]` per segment, so the entity is `changeorder`. Group, posting-profile, plan-version,
change-order create/submit/decide and risk changes publish no event yet; the PRD lists none and no consumer needs one.

## Wave reference

PRD §8.41 (`docs/prd/phase-8-projects-people-service.md`), ticket PSG-41, depends on PSG-40.
Rules implemented in the domain: BR-41.1, BR-41.2, BR-41.3 (base predicate), BR-41.7 (rule only), BR-41.10 (amount
and trigger), BR-41.12 (charge), FR-41.1 (stages, inheritance and project groups, wired to the API), FR-41.2 (plan versions, WBS and CPM, wired
to the API), FR-41.5 (posting profiles, wired to the API), FR-41.10, FR-41.17 (simulation copies only), FR-41.18 and BR-41.13 (wired to the API; budget and
vendor debit note are handoffs), FR-41.20 (risk and issue register; forecasts later).
