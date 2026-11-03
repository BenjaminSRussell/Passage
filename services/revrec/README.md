# revrec

Passage revenue recognition context (wave 29, PSG-29). It allocates a contract's
transaction price across its performance obligations by standalone selling price
(SSP, ASC 606 / IFRS 15, BR-29.1 to BR-29.3), plans and recognizes the revenue
schedules, and reports the deferred revenue roll-forward (FR-29.7 to FR-29.9).

- **Slice 1**: the pure allocation and ratable schedule, served as a stateless preview.
- **Slice 2**: persisted revenue contracts with obligations and schedule lines,
  milestone satisfaction, idempotent period recognition runs, the roll-forward
  (AC-29.4 reproduced against Postgres), events through the outbox, and the
  billing web module's revenue screens.
- **Slice 4**: the ratable basis (DAILY or MONTHLY), the roll-forward drill-down to
  one contract, the obligation-method filter and cursor pagination on the
  contracts list, and the screens for them.

- **Slice 5 (AC-29.3)**: recognized revenue is posted to the general ledger as
  Dr 2400 / Cr 4000 through its journal API with the service token, and the
  roll-forward is reconciled to the ledger's 2400 balance.

What is still missing is listed in `NOT-IMPLEMENTED.md`.

## Layout

```
cmd/revrec/             entrypoint: dbx.Connect → (MIGRATE_ON_START) dbx.Migrate → router on HTTP_ADDR, /metrics on ADMIN_ADDR
internal/domain/        pure rules: Allocate, RatableSchedule, ScheduleLines, CheckSatisfy, CheckSSPSources, FullyRecognized, NewRollForward
internal/store/         Postgres repository (pgx, RLS through dbx.WithTenantTx, outbox in the same transaction)
internal/api/           HTTP handlers, DTOs, validation, duties
internal/events/        event types and payloads
migrations/             0001_revrec_init.sql: revenue_contract, performance_obligation, revenue_schedule, recognition_run, outbox
                        0002_ratable_basis.sql: revenue_contract.ratable_basis (DAILY | MONTHLY)
                        0004_outbox_and_child_scope.sql: outbox tenant RLS and legal-entity scope inherited by child rows
                        0006_journal_posting.sql: journal_posting (one ledger entry per entity, currency and month)
api/openapi.yaml        OpenAPI 3.1 for every endpoint
events/asyncapi.yaml    AsyncAPI 3 for contract.allocated and schedule.recognized
charts/revrec/          Helm chart (DATABASE_URL from the revrec-db secret, admin port, ServiceMonitor toggle)
```

## Endpoints

| Method | Path | Duty | Notes |
|---|---|---|---|
| POST | `/v1/revrec/allocations:preview` | `revrec.view` (RevRecView) | pure computation; stores nothing |
| POST | `/v1/revrec/contracts` | `revrec.maintain` (RevRecMaintain) | `Idempotency-Key` required; 201, or 200 replay; emits `contract.allocated` |
| GET | `/v1/revrec/contracts` | `revrec.view` | newest first; filters `legalEntityId`, `customerId`, `status`, `method` (contracts with an obligation of that method); `limit` ≤ 200 and `cursor` = the previous page's `nextCursor`; recognized and remaining per contract |
| GET | `/v1/revrec/contracts/{id}` | `revrec.view` | obligations with their schedule lines; `ETag` = version |
| POST | `/v1/revrec/milestones/{obligationId}:satisfy` | `revrec.maintain` | recognizes a MILESTONE; optional `If-Match`; same-date retry replays; emits `schedule.recognized` |
| POST | `/v1/revrec/recognition-runs` | `revrec.post` (RevRecPost) | key = (legal entity, period end); 201, or 200 replay; emits `schedule.recognized` per line |
| GET | `/v1/revrec/recognition-runs` | `revrec.view` | latest period first |
| GET | `/v1/revrec/roll-forward?legalEntityId=&currency=&period=YYYY-MM` | `revrec.view` | opening, additions, recognized, adjustments, closing |
| GET | `/v1/revrec/roll-forward?contractId=&period=YYYY-MM` | `revrec.view` | the same for one contract, in its currency (drill-down); 404 when unknown or out of scope |
| POST | `/v1/revrec/journal-postings` | `revrec.post` | `{legalEntityId, through?, retryRejected?}`; posts every recognized, unposted RATABLE and MILESTONE line through `through` (default and upper bound the last completed month end) as one ledger entry per currency and month, Dr 2400 / Cr 4000 dated the month end, to the ledger configured for the entity in `REVREC_LEDGER_IDS` (never the request); a pending post is retried with the same ledger idempotency key; a post the ledger refuses with a 4xx is marked rejected, its lines released, and listed under `rejected` while the other postings continue; 503 `REV-0200` without `LEDGER_URL`, 422 `REV-0202` for an entity with no configured ledger, 502 `REV-0201` when the ledger does not answer |
| GET | `/v1/revrec/gl-reconciliation?legalEntityId=&currency=&period=YYYY-MM` | `revrec.view` | revenue recognized through the month end against the ledger's 2400 debits through the same period (`difference`), and the roll-forward's closing deferred balance (allocated deferred amounts less recognized) against the ledger's closing 2400 balance, credits less debits (`closingDifference`); `reconciled` needs both differences at zero |

Duties are enforced with `platform/authz` (401 without a principal, 403 `AUTH-0003`
without the duty). A principal scoped to one legal entity (`X-Passage-Legal-Entity`)
gets 403 `AUTH-0003` when it creates a contract, runs recognition or reads a
roll-forward for another entity, and row-level security hides other entities'
contracts and runs from its reads.

## Rules

- **Allocation** (BR-29.1, BR-29.3): shares proportional to SSP, truncated to the
  minor unit; the residual goes to the obligation chosen by `residualRule`
  (LARGEST by default). The BR-29.1 bundle gives 7,200.00 / 1,350.00 / 2,250.00;
  at 10,799.99 the hardware takes 7,200.01.
- **SSP source** (BR-29.2): the caller sends the resolved SSP and its source. At
  most one obligation per contract may be RESIDUAL (REV-0003). The engine derives
  a RESIDUAL obligation's SSP as the transaction price less the other SSPs (its
  input `ssp` may be omitted; one that is sent must still be a decimal, COM-0001,
  and is ignored) and refuses the contract when that is not positive (REV-0008).
  Responses give an SSP at the currency's minor unit, or with every fraction
  digit beyond it, so a derived 299.876544 is not rounded to 299.88. A RESIDUAL
  obligation is returned with the BIL-0009 warning; the web form does not ask
  for its SSP.
- **Schedules** (FR-29.8): POINT_IN_TIME is one line recognized at `satisfiedOn`,
  the transfer date, when the contract is created; the invoice credits that
  revenue to 4000, so no `schedule.recognized` is emitted for it. MILESTONE is
  one undated line recognized by `:satisfy` (on or after the contract date,
  REV-0006). RATABLE is one line per calendar month of the term, dated at the
  period end; the last line absorbs the residual. On the contract's
  `ratableBasis` DAILY (default) each month weighs the share of its days the term
  covers; on MONTHLY every month the term touches takes an equal share (2,250.00
  over 2027-03-10 .. 2028-03-09: 133.06 first on DAILY, 12 × 173.08 + 173.04 on
  MONTHLY). The PRD makes the basis a legal-entity setting; until that is
  readable the caller sends it and it is stored on the contract. USAGE has no line until usage posts (later slice).
- **Recognition run**: recognizes every SCHEDULED RATABLE line of the entity
  dated on or before the period end, on the period end. One run per (entity,
  period end); a rerun returns the recorded run. A period end after today is
  refused (REV-0007). A line that falls due in a period already run (a contract
  entered late) is recognized by the next run, on that run's period end.
- **Roll-forward** (FR-29.9): only deferred methods (RATABLE, MILESTONE) enter it.
  Additions are their allocations on contracts dated in the month; recognized are
  their lines recognized in the month; opening is everything before the month.
  Adjustments are 0.00 until credit notes reverse schedules (BR-29.6). AC-29.4:
  March 2027 additions 3,600.00, recognized 1,483.06 (1,350.00 + 133.06),
  closing 2,116.94. With `contractId` the same sums cover one contract; the
  contracts of an entity add up to its roll-forward.
- **Versions**: a contract starts at version 1; every satisfaction and every run
  that recognizes one of its lines bumps it. A contract becomes COMPLETE when no
  line is SCHEDULED and it has no USAGE obligation.

## Events

| Type | When |
|---|---|
| `dev.passage.revrec.contract.allocated.v1` | contract created |
| `dev.passage.revrec.schedule.recognized.v1` | milestone satisfied; each line of a recognition run |

Both are written to the outbox in the same transaction and keyed by contract id.
`revenue_schedule.journal_posting_id` is set when `POST /v1/revrec/journal-postings`
claims a recognized line for a `journal_posting` row; the row keeps the ledger entry
id once the ledger answers.

## Ledger

`LEDGER_URL` (and `LEDGER_TIMEOUT`) with the service signing key
(`PASSAGE_SERVICE_NAME=revrec`, `PASSAGE_SERVICE_SIGNING_KEY[_FILE]`) enable the
posting and the reconciliation through `platform/ledgerclient`; the ledger must trust
revrec's public key (`PASSAGE_SERVICE_PUBLIC_KEYS`). The service holds only
`ledger.entry.post` and `ledger.balance.read` (`service_grants`). A line is claimed
for a posting in the transaction that creates the posting row, so no line is posted
twice; an unanswered post leaves the posting pending for the next call, a post the
ledger refuses for good is marked rejected and its lines are released with
`rejected_posting_id`, then skipped until a request sets `retryRejected`. `REVREC_LEDGER_IDS`
(`<legalEntityId>=<ledgerId>,...`, chart value `ledgerIds`) names the ledger of each entity and
is required when `LEDGER_URL` is set; it must name the same ledger as billing's `BILLING_GL_PROFILES` for the entity (a
different one leaves 2400 without billing's credits and shows as a `closingDifference` on the reconciliation). A pending posting on another ledger than the configured
one is logged and listed under `otherLedger`, never re-targeted. Operator procedure for
those: if the mapping changed by mistake, restore it and call the endpoint to drain them.
If the move to the new ledger is intended, first confirm in the old ledger that no entry
with idempotency key `revrec-posting-<id>` exists, then, as the schema owner in one
transaction, run `update journal_posting set rejected_code = 'OPERATOR_RELEASED' where id = '<id>' and entry_id is null`
and `update revenue_schedule set journal_posting_id = null, rejected_posting_id = '<id>' where journal_posting_id = '<id>'`,
and post again with `retryRejected` so the lines are claimed for the configured ledger.
When a ledger acceptance arrives after its posting was rejected and its lines were
already retried into a new pending posting, completing it supersedes that new posting
(`rejected_code = 'SUPERSEDED'`, its other lines released for retry) and gives the
lines back to the accepted one; if the new posting was already accepted by the ledger the
lines are in the ledger twice, which is logged at error level with both posting ids
for a manual reversal. The response echoes the effective `through` date.
The reconciliation compares the cumulative recognized revenue of the roll-forward
with the ledger's 2400 debits: the credit side of 2400 is the invoice's posting
(billing), not revrec's.

## Run

```
DATABASE_URL=postgres://... MIGRATE_ON_START=true HTTP_ADDR=:8080 ADMIN_ADDR=:9090 go run ./cmd/revrec
```

## Test

```
go vet ./... && go test ./...
PASSAGE_TEST_DATABASE_URL=postgres://... go test ./internal/store/   # Postgres tests (skipped without the URL)
```

The domain tests cover the BR-29.1 example, the residual cases, the schedule
plan, the satisfaction rules and the roll-forward arithmetic. The API tests use an
in-memory store and cover validation, idempotent replays, If-Match, scope, duties
and readiness. The store tests (Postgres, non-superuser role so RLS applies)
reproduce AC-29.4 end to end, April's carry-forward, contract completion, the
outbox payloads, tenant and legal-entity isolation, the duplicate-key race, keyset
pages, the method filter, child-row scope and rejected cross-tenant outbox writes.
`internal/api/postings_test.go` runs recognition, the journal postings and the
reconciliation against Postgres with the `platform/ledgerclient` fake as the ledger: the
entry is Dr 2400 / Cr 4000, a refused post is retried without double posting, and the
reconciliation shows a difference until the ledger holds the entries.
The `asc606-40` fixture (`internal/domain/testdata/asc606-40.json`, regenerated by
the exact rational reference `gen_asc606_40.py`, which shares no code with the
engine) holds 40 bundle scenarios. `TestASC606Fixture40_AllocateAndSchedule` checks
allocation and every schedule line. `TestASC606Fixture40_RecognizeAndReconcileGL2400`
replays them through Postgres (milestones, monthly runs). It checks recognized revenue
and the roll-forward, and that each month's closing equals the 2400 balance of a
ledger fed only by the outbox events. The fixture is an engineering reference, not the
accountant-reviewed workbook.
`TestOpenAPI_MatchesRoutes` keeps `api/openapi.yaml` and the mounted routes and
duties in step.

## Error codes

| Code | HTTP | Meaning |
|---|---|---|
| `COM-0001` | 400 | Validation failed (see `errors` per field), missing Idempotency-Key, malformed If-Match |
| `AUTH-0001` | 401 | Missing tenant context |
| `AUTH-0003` | 403 | Missing duty, or a legal entity outside the caller's scope |
| `REV-0001` | 422 | Rule violation: no obligations, non-positive SSP, negative price, missing transfer date |
| `REV-0002` | 422 | Ratable term missing or ending before it starts |
| `REV-0003` | 422 | More than one RESIDUAL obligation in a contract (BR-29.2) |
| `REV-0008` | 422 | RESIDUAL SSP (transaction price less the other SSPs) is not positive (BR-29.2) |
| `REV-0004` | 409 | Milestone already satisfied on another date (or reversed) |
| `REV-0005` | 422 | Only a MILESTONE obligation can be satisfied |
| `REV-0006` | 422 | Milestone satisfied before the contract date |
| `REV-0007` | 422 | Recognition run for a period that has not ended |
| `REV-0200` | 503 | The general ledger is not configured (`LEDGER_URL` unset) |
| `REV-0201` | 502 | The general ledger did not answer, refused a balance read, or reported another currency |
| `REV-0202` | 422 | The legal entity has no ledger in `REVREC_LEDGER_IDS` |
| `REV-0100` | 404 | Contract or obligation not found |
| `REV-0103` | 412 | If-Match names a contract version that is no longer current |
| `idempotency_key_reuse` | 422 | Idempotency-Key reused with a different body |
| `idempotency_in_progress` | 409 | A concurrent request holds the same key, or a concurrent run the same period |
| `BIL-0009` | warning | SSP missing for an obligation; residual method used (returned in `warnings`) |

## Notes

- Money is `numeric(20,6)` with `currency char(3)` and `shopspring/decimal` in Go; amounts leave the API at the currency's minor unit.
- Dependencies: chi, pgx, shopspring/decimal, rapid (tests), platform kernel only.
