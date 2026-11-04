# Passage ledger service

The ledger is the universal journal: one append-only, double-entry journal per
ledger, the chart of accounts, the posting API every subledger calls, and the
balance projection that trial balances read. Wave 11 (PSG-11, slices 1 to 10).

Reference: `docs/prd/phase-2-financial-core.md` section 2.11. What is built and
what is not is listed in [NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md).

## What slice 1 does

- Chart of accounts: `PUT /v1/ledger/charts/{chart}/accounts/{number}` and
  paged listing. Account types, postable flag, suspension date and reconciliation
  account type (manual journals, source `ledger`/`manual_journal`, may not post to
  reconciliation accounts; subledgers may, BR-11.8).
- Ledgers: create and read a ledger bound to a chart, a currency and up to 20
  dimension attribute names.
- Posting: `POST /v1/ledger/journal-entries` posts a balanced entry atomically
  or rejects it entirely with every violation in one 422 response. Idempotency
  keys replay identical requests (200, `idempotency.replayed = true`) and reject
  reused keys with different payloads (409 `LEDGER_IDEMPOTENCY_CONFLICT`). The
  entry keeps its key for good (unique per legal entity), so a verbatim resend
  after the key record expired (`LEDGER_IDEMPOTENCY_TTL_DAYS`, default 400) returns the original entry as a replay
  rather than booking a second one, and a changed body is the same 409.
- Validation dry run: `POST /v1/ledger/validate` returns the same violations
  and the resolved dimension combinations without writing.
- Reversal: `POST /v1/ledger/journal-entries/{id}/reverse` writes an exact
  negating entry linked to the original line by line, dated on the original posting
  date or a later one. A second reversal is refused; reversing the reversal
  re-instates the original.
- Balances: a projection per (account, dimension combination, period) written in
  the posting transaction; `GET /v1/ledger/balances` is the trial balance with
  a balanced flag; `POST /v1/ledger/balances:rebuild` recomputes the projection
  from the journal and reports differing rows. Statistical quantities (BR-11.2)
  have their own projection per unit (`balance_quantity`); the trial balance
  returns `quantities` (debit, credit, net per unit) per row and in total.
  **Upgrade step for migration 0027:** quantity lines posted before it are not
  in the new projection (row-level security keeps the migration from reading
  the journal). Every ledger that existed before 0027 is flagged
  (`ledger.quantity_rebuild_pending`) and its trial balance returns
  `quantitiesPending: true` (the web screen shows a warning) until
  `POST /v1/ledger/balances:rebuild` runs once for it. That first rebuild
  reports `quantityBackfill: true` and does not count the backfilled quantity
  rows in `rowsDiffering`, so it raises no drift alert; later rebuilds count
  quantity drift as usual. A dimension realignment run on a flagged ledger reads
  the quantities to move from the journal instead of the projection, so it still
  empties the old combinations.
- Ledger transactions (FR-11.14): `GET /v1/ledger/journal-lines?ledger=` lists
  posted lines in posting order (posting sequence, line number) with the entry's
  voucher, date, description, source and trace id. Filters: `account_from`/`account_to`
  (text range, inclusive), `date_from`/`date_to` (posting date, inclusive), `voucher`,
  `source_system` and `dim.{ATTRIBUTE}=value` (all pairs must match). Keyset pages of
  up to 1,000 lines (default 100): pass `nextCursor` back as `cursor`. Migration 0010
  indexes the date and account access paths.
- Events through the outbox (same transaction): `dev.passage.ledger.journal.posted.v1`,
  `dev.passage.ledger.journal.reversed.v1`, `dev.passage.ledger.balance.rebuilt.v1`.
- Database guarantees (FR-11.6, BR-11.1): every table has `tenant_id` with ENABLE and
  FORCE row-level security and a tenant policy (plus the legal-entity scope policy
  where the table carries `legal_entity_id`); triggers reject UPDATE, DELETE and
  TRUNCATE on `journal_entry`/`journal_line` for every role; the `passage_app` role
  holds only SELECT and INSERT on them; a deferred constraint trigger re-checks at
  commit that each entry's debits equal its credits.

## What slice 2 adds

- Shared financial reason codes (FR-11.15): one list per tenant,
  `GET /v1/ledger/reason-codes`, `GET`/`PUT /v1/ledger/reason-codes/{code}`
  (201 created, 200 replaced). Codes are `^[A-Z0-9_]{1,10}$` with a name,
  `appliesTo` modules, `requiresComment`, `negativePostingAllowed`,
  `alternativePostingDateAllowed` and `active`. Codes are deactivated, never
  deleted, because reversing entries reference them.
- Ledger parameters `reversalReasonRequired` (default true) and
  `negativePostingEnabled` (default false), set when the ledger is created.
  `allowReinstate` (default true, migration 0030) is set at creation or by
  `PATCH /v1/ledger/ledgers/{id}` (with `If-Match`): when false, reversing a
  reversing entry is refused with 422 `LEDGER_REINSTATEMENT_NOT_ALLOWED`.
- Journal names also carry `postingRestriction` (`none` or `own_journals_only`:
  403 `LEDGER_POSTING_RESTRICTED` when another user posts the batch) and
  `descriptionLanguage` (`legal_entity` or `user`: with `user` the batch post
  reads `Accept-Language` for the default description template, falling back to
  the base language and then `en`).
- Reversal reasons: the reverse request takes `reasonCode` and `reasonComment`
  (at most 255 characters). The code must exist, be active and apply to `ledger`;
  a comment is needed when the code requires one; a code that forbids an
  alternative posting date refuses any date but the original. Both fields are
  stored on the reversing entry and carried by `journal.reversed`.
- Negative postings (storno, BR-11.16): when the code allows negative posting and
  the ledger enables it, the reversing lines keep D/C with positive amounts and
  `negativePosting = true`, and the balance projection (and its rebuild) reduces
  the original side, so trial balance debit and credit turnover return to their
  pre-posting values (AC-11.11). Re-instating a storno writes ordinary lines.
- Web: a reason-code list and edit form, and a reverse form with reason on the
  journal entry screen (`apps/web/src/modules/ledger`).

## What slice 3 adds

- Account structures (FR-11.3): `GET /v1/ledger/ledgers/{ledger}/account-structures`,
  `GET`/`PUT .../account-structures/{id}` (201 created, 200 replaced). A version has a
  name, version number, priority, `effectiveFrom`, status (`draft`, `active`, `retired`),
  ordered dimension segments after the implicit `MAIN_ACCOUNT`, a main-account filter
  (exact numbers, `from..to` ranges, `*`; empty covers every account) and constraint rows.
  A row restricts one segment to `allowed` tokens (exact, range, `*` any non-blank,
  `blank`) under conditions on `MAIN_ACCOUNT` or earlier segments (`under`); the most
  specific matching row governs, and a segment with no matching row is free.
- Versioning and activation: drafts change freely; `PUT` with `status: active` activates.
  An active version can only be retired and a retired one is final (409
  `LEDGER_STRUCTURE_LOCKED`); `(name, version)` is unique per ledger (409
  `LEDGER_STRUCTURE_VERSION_EXISTS`); malformed definitions are 422
  `LEDGER_STRUCTURE_INVALID` with every violation.
- Posting and `POST /validate` check every line against the structures (pipeline stage
  after account checks): per structure name the active version with the latest
  `effectiveFrom` on or before the posting date is in effect; structures are tried by
  priority then name and the first whose filter admits the account governs. No match is
  `LEDGER_STRUCTURE_NOT_FOUND`; a dimension that is not a segment of the governing
  structure, or a value a row does not allow, is `LEDGER_DIMENSION_INVALID`; a blank a row
  does not allow is `LEDGER_DIMENSION_REQUIRED` (AC-11.4). A ledger with no active
  structure is open: only the format checks apply, as before.
- Combinations (FR-11.4): a line validated under a structure is interned under a
  combination whose hash leads with the structure version id;
  `dimension_combination.account_structure_id` records it.
- Dimension value register (FR-11.2): `GET /v1/ledger/ledgers/{ledger}/dimension-values`
  and `PUT .../dimension-values/{attribute}/{value}` with a name, `activeFrom`/`activeTo`
  (inclusive) and `suspended`. Posting a registered value that is suspended or outside its
  dates is `LEDGER_DIMENSION_INACTIVE`; unregistered values are format-checked only.
- Web: account structure list and editor (segments, filter, constraint grid, activate,
  retire) and a dimension value list and form.

## What slice 4 adds

- Default description templates (FR-11.17): `GET /v1/ledger/description-templates`,
  `GET`/`PUT .../description-templates/{id}` (201 created, 200 replaced; duty
  `ledger.setup.write`, PRD "Maintain ledger setup"). A template has a scope (`tenant`, or
  `legal_entity` with `legalEntityId`), a `sourceType` (the posting's `source.type`, such as
  `manual_journal`), a `language` (default `en`), text of at most 255 characters with
  placeholders `%1`..`%9`, and `fieldBindings` from placeholder to one of `voucher`,
  `party_name`, `document_date`, `amount_txn`, `currency_txn`, `source_id`, `period`. Every
  placeholder must be bound and every binding used. One template per scope, legal entity,
  source type and language (409 `LEDGER_DESCRIPTION_TEMPLATE_DUPLICATE`).
- Legal-entity scope: tenant templates are visible to every caller; a legal entity's own
  templates only inside the caller's scope (restrictive RLS policy). A caller scoped to one
  legal entity may write only that entity's templates (403 `AUTH-0003`).
- Posting stage (after the balance check, before sequence assignment): when the request
  omits `description` or a line omits `text`, the template for the source type is expanded
  (the legal entity's template first, then the tenant's) and fills only the empty fields
  (AC-11.19). `voucher` is the voucher the entry receives; `amount_txn` is the debit total.
  An expansion longer than 255 characters is cut with an ellipsis and the response carries
  the info notice `LEDGER_DESCRIPTION_TRUNCATED` in `notices[]` (LEDGER-0031).
- Web: description template list and create/edit form with a live preview.

## What slice 5 adds

- Journal names (FR-11.9, AX `LedgerJournalName`): `GET /v1/ledger/journal-names`,
  `GET`/`PUT .../journal-names/{code}` (201 created, 200 replaced; duty `ledger.setup.write`).
  A name carries `requiresApproval`, `selfApprovalAllowed`, `reversalReasonRequired`
  (FR-11.15), `allowReconAccounts` (BR-11.8), `linesLimit` (default 5,000, at most 10,000)
  and `active`. Names are deactivated, never deleted.
- Journal batches (FR-11.9, BR-11.10): `GET`/`POST /journal-batches`, `GET`/`PATCH
  /journal-batches/{id}` and the actions `/submit`, `/approve`, `/reject`, `/post`, `/cancel`.
  A batch has a ledger, an active journal name, description, posting date, optional document
  date and lines (account, dimensions, D/C, amount, text). Responses carry the batch `version`
  as `ETag`; `If-Match` on `PATCH` and the actions is checked when sent (412
  `LEDGER_VERSION_CONFLICT`).
- State machine (`internal/domain/batch.go`): submit validates every line with the posting
  rules and moves `draft -> validated`, then `-> in_approval` when the journal name requires
  approval; approve or reject decides (the submitter may not approve unless the name allows
  self-approval, 403 `LEDGER_SELF_APPROVAL`); any edit of a validated, approved or rejected
  batch returns it to draft; draft, validated and rejected batches can be cancelled; posted
  and cancelled are terminal.
- Posting (`/post`, `Idempotency-Key` required): the batch row lock is the `posting` lock.
  The entry (source `ledger`/`manual_journal`, `documentId` = batch id) goes through the
  posting pipeline and the batch is marked posted in the same transaction, so a batch is
  posted exactly when its entry exists. An empty description is filled from the
  `manual_journal` template (AC-11.19). A rejected post rolls back, leaves the batch approved
  (or validated) and records `lastError`. The same key on the posted batch replays.
  Posting a validated batch (journal name without workflow) also needs
  `ledger.journal.postdirect` ("Post ledger journals without approval").
- Ledger sets (FR-12.7): the PRD's ledger group is implemented as the ledger set, under `/ledger-sets` (`POST /ledger-sets`, `GET /ledger-sets`, `GET /ledger-sets/{id}`, `GET /ledgers/{id}/account-mappings`, `POST /ledger-sets/{setId}/journal-entries`, `POST /ledgers/{id}/account-mappings`). The PRD's `ledger-groups` naming is not adopted (issue #222). Posting to one non-leading ledger on its own needs `ledger.journal.postledger` (FB50L). `POST /ledger-sets/{setId}/journal-entries` also takes `ledgerLines` (BR-12.6 `ledger_lines`): per-ledger line groups posted to that ledger only, unmapped, on top of the common `lines` (which go to every ledger through its account mappings). Each ledger's entry must balance by itself; otherwise 422 `LEDGER_UNBALANCED` names the ledger (`ledger TAX: entry does not balance ...`) and nothing is written. A ledger outside the set is 422 `LEDGER_LINES_LEDGER_NOT_IN_SET`. Ledger lines for a non-leading ledger need `ledger.journal.postledger` (403 `AUTH-0003`), since they are ledger-specific adjustments; common lines and leading-ledger lines do not (decision recorded on issue #222). Ledgers carry optional `calendarCode`, `accountingPrinciple` (IFRS, US_GAAP, LOCAL_GAAP, TAX, MANAGEMENT) and `reportingCurrency` (FR-12.7; migration 0024; stored and returned, not yet consumed by posting), changed with `PATCH /ledgers/{id}` (duty `ledger.ledger.write`, `If-Match` on the ledger `version`, announced as `rules.changed` kind `ledger_settings`). Members of a set may be kept in different currencies: the set post translates the common lines into a member's currency at the posting date's spot rate from `services/fx` (`FX_URL`; the ledger's service identity holds the `fx.rate.lookup` grant; one lookup per currency, made before the posting transaction), rounds to the member's minor units, and moves any difference between the translated net and the rounded net of the source lines onto the largest line that can take it (flagged `roundingAdjustment`, BR-12.4), so each ledger balances alone. No rate is 422 `LEDGER_RATE_MISSING` (also without `FX_URL`), another fx rejection is relayed as 422 with its code, a refused lookup 502 `LEDGER_FX_MISCONFIGURED`, an unreachable fx service 503 `LEDGER_FX_UNAVAILABLE`; nothing is written. `POST /journal-entries` takes `ledgerLines` for its own ledger only (added to `lines`); a group for another ledger is 422 `LEDGER_LINES_OWN_LEDGER_ONLY`. Journal batches do not take `ledgerLines`: a batch is a single-ledger document, so its lines already are that ledger's lines.
- Reversal: an entry posted from a batch whose journal name sets `reversalReasonRequired`
  needs a reason code even when the ledger does not (FR-11.15, AC-11.11).
- Events: every transition writes `dev.passage.ledger.batch.statuschanged.v1` (aggregate
  `journal_batch`, topic `passage.ledger.batch`) to the outbox in the same transaction.
- Web: journal name list and form, journal batch list (status filter), new batch form with a
  line grid and totals, and the batch screen with submit, approve, reject, post, cancel and
  line editing.

## Layout

```
cmd/ledger/            entrypoint: dbx.Connect -> dbx.Migrate -> httpx router -> register -> serve
internal/domain/       pure rules: validation, combination hashing, balance aggregation, reversal
internal/store/        Postgres repositories (pgx, RLS through dbx.WithTenantTx)
internal/api/          HTTP handlers, DTOs, duties (authz.RequireDuty)
internal/events/       event type constants and payloads
migrations/            0001..0013 SQL, applied in order by dbx.Migrate (0005: passage_app grants; 0006: reason codes; 0007: account structures, dimension values; 0008: description templates; 0009: journal names and batches; 0013: recurring journals)
api/openapi.yaml       OpenAPI 3.1 for every route
events/asyncapi.yaml   AsyncAPI 3 for every event
charts/ledger/         Helm chart (ports 8080 http, 9090 admin; readiness on admin /readyz)
Dockerfile             distroless image; build from the repository root (docker build -f services/ledger/Dockerfile .)
```

Ports: the main port serves the API and the platform's `/healthz`. The admin
port serves `/metrics` and the database-backed `/readyz`.

## Run

```
export DATABASE_URL=postgres://passage_app:...@localhost:5432/passage_ledger
export MIGRATE_ON_START=true   # dev only; stage and prod run migrations as a PreSync Job
go run ./cmd/ledger
```

Required tenant context on each request comes from the kernel's trusted headers
(`X-Passage-Tenant`, plus the principal headers the platform gateway sets) until
wave 2 replaces them with JWT validation.

## Test

```
go vet ./... && go test ./...
PASSAGE_TEST_DATABASE_URL=postgres://admin@... PASSAGE_TEST_APP_DATABASE_URL=postgres://passage_app@... go test ./...
```

Domain tests are pure (table-driven plus `pgregory.net/rapid` properties). API
tests run the handlers over a fake store, and a drift test checks that every
mounted route and its duty is in `api/openapi.yaml`. Store tests use
`platform/go/testkit`: migrations run as the admin URL, queries run as
`passage_app` so RLS and the journal grants apply. They are skipped when
`PASSAGE_TEST_DATABASE_URL` is unset.

## Duties

| Duty | Routes |
|---|---|
| `ledger.ledger.write` | `POST /ledgers` |
| `ledger.account.write` | `PUT /charts/{chart}/accounts/{number}`, `PUT /ledgers/{ledger}/open-item-accounts/{account}` |
| `ledger.journal.read` | `GET /ledgers/{id}`, `GET /charts/{chart}/accounts` (also `ledger.account.lookup`, the service duty close signs to read the chart), `GET /journal-entries/{id}`, `GET /reason-codes[/{code}]`, `GET /ledgers/{ledger}/account-structures[/{id}]`, `GET /ledgers/{ledger}/dimension-values`, `GET /description-templates[/{id}]`, `GET /journal-names[/{code}]`, `GET /journal-batches[/{id}]`, `GET /journal-lines`, `GET /recurring-journals[/{id}[/runs]]`, `GET /ledgers/{ledger}/open-item-accounts`, `GET /ledgers/{ledger}/open-item-activations`, `GET /ledgers/{ledger}/tolerance-groups[/{code}]`, `GET /open-items`, `GET /clearings[/{id}]`, `GET /ledgers/{ledger}/auto-clearing-rules[/{id}]`, `GET /auto-clearing-runs[/{id}]` |
| `ledger.journal.post` | Umbrella: `POST /journal-entries`, `POST /validate`, `POST /journal-batches/{id}/post`, `POST /ledger-sets/{setId}/journal-entries` (PRD "Post ledger journals"). Each route also accepts its own narrow duty: `ledger.entry.post` (service only), `ledger.entry.validate`, `ledger.batch.post`, `ledger.set.post` |
| `ledger.entry.lookup`, `ledger.ledger.lookup` | Service-only narrow reads, accepted beside `ledger.journal.read` on `GET /journal-lines` (which filters by `documentId`, the posting document's UUID) and `GET /ledgers/{id}` |
| `ledger.journal.postledger` | Flag duty checked with `ledger.journal.post` on `POST /journal-entries` and `POST /journal-batches/{id}/post` when the entry's ledger is a non-leading member of a ledger set (FB50L, PRD "Post to ledger-specific ledger"); without it the post is refused 403 `AUTH-0003` |
| `ledger.period.postclosed` | Flag duty (the close adjustment). Every posting path (`POST /journal-entries`, `/validate`, reversal, `/journal-batches/{id}/submit` and `/post`, `/ledger-sets/{setId}/journal-entries`) asks the close service (`GET /v1/close/periods:resolve`, signed `period.check`; non-open answers cached 30 s per ledger, module and date; an open answer is reused only within one request) for the posting date's period status and answers 422 `LEDGER_PERIOD_CLOSED` unless it is `open`. The duty overrides `closed` and `on_hold` only (the ledger has no user group to pass to close's hold exception); `permanently_closed` and `never_opened` always refuse. A date close has no registered period for is governed by `first_open_date` alone. A close service that cannot confirm the status gives 503 `LEDGER_PERIOD_STATUS_UNAVAILABLE` and nothing is posted. The service refuses to start unless `CLOSE_URL` is set (with a service signing key) or `LEDGER_PERIOD_CHECK=disabled` (dev and test; logs a warning). A reversal asks about the original entry's source module |
| `ledger.journal.postdirect` | Checked inside `POST /journal-batches/{id}/post` for a validated batch whose journal name has no workflow (PRD "Post ledger journals without approval") |
| `ledger.journal.write` | `POST /journal-batches`, `PATCH /journal-batches/{id}`, `POST .../submit`, `POST .../cancel`, `PUT /recurring-journals/{id}`, `POST /recurring-journals/{id}/pause`, `/resume`, `/generate`, `PUT` and `DELETE /journal-batches/{id}/attachments/{attachmentId}` (PRD "Maintain ledger journals") |
| `ledger.journal.approve` | `POST .../approve`, `POST .../reject` (PRD "Approve ledger journals") |
| `ledger.journal.reverse` | `POST /journal-entries/{id}/reverse` |
| `ledger.balance.read` | `GET /balances` |
| `ledger.balance.rebuild` | `POST /balances:rebuild`, `POST /ledgers/{ledger}/open-item-accounts/{account}:activate` (PRD duty "Administer ledger") |
| `ledger.reason.write` | `PUT /reason-codes/{code}` (PRD duty "Maintain reason codes") |
| `ledger.structure.write` | `PUT /ledgers/{ledger}/account-structures/{id}`, `PUT /ledgers/{ledger}/advanced-rules/{id}` (PRD duty "Maintain account structures") |
| `ledger.dimension.write` | `PUT /ledgers/{ledger}/dimension-values/{attribute}/{value}` (PRD duty "Maintain financial dimensions") |
| `ledger.setup.write` | `PUT /description-templates/{id}`, `PUT /journal-names/{code}` (PRD duty "Maintain ledger setup") |
| `ledger.clearing.write` | `POST /clearings`, `POST /clearings:simulate` (PRD duty "Clear open items"; not yet in the policy bundle, issue #313) |
| `ledger.clearing.reset` | `POST /clearings/{id}/reset` (PRD duty "Reset clearings"; issue #313) |
| `ledger.clearing.rules` | `PUT /ledgers/{ledger}/tolerance-groups/{code}`, `PUT /ledgers/{ledger}/auto-clearing-rules/{id}` (PRD duty "Maintain clearing rules"; issue #313) |
| `ledger.clearing.run` | `POST /auto-clearing:run` (PRD duty "Run automatic clearing"; issue #313) |
| `ledger.rules.write` | `PUT /ledgers/{ledger}/lookup-tables/{id}`, `PUT /ledgers/{ledger}/derivation-strategies/{version}`, `POST .../derivation-strategies/{version}/activate` (PRD duty "Maintain validation and derivation rules"; issue #313) |

## Recurring journals (slice 7, FR-11.10)

- `PUT /v1/ledger/recurring-journals/{id}` creates (201) or replaces (200) a template of lines
  (`fixed` amounts, or `percent` of a base amount) with a monthly, quarterly or yearly schedule on
  a day of month (0 is the last day). `If-Match` is checked when sent. `POST .../pause`,
  `.../resume` (skips runs missed while paused), `GET` list and read, `GET .../runs`.
- `POST .../generate` creates the draft batch for a required `scheduledDate`, which must be one of
  the schedule's dates (quarterly and yearly schedules keep the month of the start date as anchor),
  and records the run in one transaction; a date that already has a run replays it (200). Codes:
  `LEDGER_RECURRING_NOT_FOUND`, `_PAUSED`, `_DATE_INVALID`, `_BASE_REQUIRED`, `_LEDGER_FIXED`,
  `_ID_CONFLICT`.
  Event `dev.passage.ledger.recurring.generated.v1`.
- Replacing a template keeps its next run date while frequency, day and start date are unchanged
  (runs skipped by a resume stay skipped); a changed schedule recomputes it from the last run.
- The web batch screen has "Save as recurring journal", which opens the new recurring journal form
  filled from the batch (fixed amounts, its ledger, journal name, description and posting date).
- A template line may carry a fixed statistical `quantity` and `unit` (BR-11.2, migration 0029),
  set together like on batch lines; generation copies them unscaled onto the draft batch line, so a
  zero-amount template line is a recurring statistical posting (for example a monthly headcount).

## Open items and manual clearing (slice 8, FR-11.19, BR-11.12, BR-11.13, BR-11.18)

- `PUT /v1/ledger/ledgers/{ledger}/open-item-accounts/{account}` `{enabled}` switches open-item
  management of an account in one ledger (`open_item_setting`). Only postable balance-sheet accounts
  that are not customer or vendor reconciliation accounts qualify (`LEDGER_OI_ACCOUNT_INVALID`). An
  account with a non-zero balance in the ledger is refused (`LEDGER_OI_ACTIVATION_BALANCE`): it needs
  the activation run below. Switching off needs zero open items (`LEDGER_OI_OPEN_ITEMS_EXIST`).
- Activation run (slice 17): `POST /ledgers/{ledger}/open-item-accounts/{account}:activate`
  `{activationDate}` with `Idempotency-Key`, duty `ledger.balance.rebuild` (PRD "Administer ledger").
  Under the ledger lock (which every posting takes) it reads the account's lines that have no open item
  yet: lines dated on or before `activationDate` (all periods) become one opening item per (dimension
  combination, transaction currency) that does not net to zero (`sourceType` `activation`, assignment
  `ACTIVATION`, dated `activationDate`); each later line becomes an ordinary `gl_line` item. The open
  items then add up to the account balance. It stores `open_item_activation_run`, records the run id on
  the setting and the items, switches the setting on and announces `openitem.activated.v1`. 409
  `LEDGER_OI_ALREADY_ACTIVE` / `LEDGER_OI_OPEN_ITEMS_EXIST`; a replay returns the stored run (200).
  No journal entry is written, so the period status of `activationDate` is not checked.
  `GET /ledgers/{ledger}/open-item-activations` lists the runs.
- While on, `writeEntry` creates one `open_item` per non-zero line of every posted entry on that
  account, in the posting transaction: signed amount (debit positive) in the ledger currency, the
  line's dimension combination and posting date, and `assignment` = the first 50 characters of the
  line's `sourceLineRef`. `GET /open-items` lists them (ledger, account, status, assignment,
  `dim.{ATTRIBUTE}`, keyset `cursor`/`limit`); status `open` includes `reset_open`.
- `POST /clearings:simulate` and `POST /clearings` take 2 to 10,000 items of one account, a
  `clearingDate` (not before the latest item posting date, `LEDGER_CLEARING_DATE_INVALID`), an optional
  `toleranceGroup` and `residualItem`. Net zero clears. A residual within the group's limit (BR-11.12:
  the debit limit for a debit residual, the credit limit for a credit residual, and the percentage of
  the selection's absolute total Σ|amount| when set) is posted as an entry of source type
  `clearing_difference` (a line on the item account that brings the selection to zero against a line
  on the group's difference account) and its item is cleared with the selection. A larger residual
  becomes a new `residual` open item (chain at most 20 deep) when `residualItem` is true, otherwise
  `LEDGER_CLEARING_UNBALANCED`. Items already cleared give `LEDGER_OPEN_ITEM_ALREADY_CLEARED` (409). The
  group's difference account may not be open-item managed (`LEDGER_CLEARING_DIFFERENCE_ACCOUNT`).
  The `Idempotency-Key` is scoped to the ledger; a replay returns the document (200).
- `POST /clearings/{id}/reset` resets the document as a whole and always needs an active ledger reason
  code (`LEDGER_REASON_REQUIRED` otherwise): items become `reset_open`, the
  difference entry is reversed with the given reason code (the ledger's reversal rules apply) and its
  two items are voided, an open residual item is voided (`LEDGER_CLEARING_RESIDUAL_USED` if it was
  cleared since). `GET /clearings[/{id}]` reads documents (the list returns the newest 200).
- `POST /journal-entries/{id}/reverse` refuses an entry with an item in an active clearing, including a
  clearing's own difference entry (409 `LEDGER_OPEN_ITEM_ALREADY_CLEARED`): reset the clearing first.
- `PUT/GET /ledgers/{ledger}/tolerance-groups[/{code}]` maintain the groups (`ETag`, optional `If-Match`).
- Event `dev.passage.ledger.openitem.cleared.v1` (status `cleared` or `reset`), aggregate type `clearing`.
- Web: open items (filters, selection, preview, clear), clearing documents list and detail with
  reset, tolerance group list and form, open-item account list with switch.

## Automatic clearing (slice 9, FR-11.20, AC-11.13)

- `PUT/GET /ledgers/{ledger}/auto-clearing-rules[/{id}]` maintain rules under a caller-chosen id (`ETag`,
  optional `If-Match`, 412 `LEDGER_VERSION_CONFLICT`; deactivated, never deleted). A rule has an account
  range (compared as text), 1 to 5 ordered criteria (`assignment`, `source_line_ref`, `document_id` as the entry's `documentId`,
  `amount_txn` as the absolute amount, `posting_date` as the month, `dim.{ATTRIBUTE}`), an optional
  tolerance group (`allowTolerance`) and a `sequence` that orders the rules.
- `POST /auto-clearing:run` (`ledgerId`, optional `ruleId`, `mode` simulate or clear, optional `clearingDate`)
  groups the open and reset-open items of each active rule by account and criteria; an item with no value for a
  criterion, and a group of one item, are left alone (`unmatchedItems`). A group that nets to zero, or within
  the rule's tolerance group, clears as one `clearing_document` of mode `auto` through the manual clearing
  code (same date, tolerance and difference-account checks) in its own transaction, idempotency key
  `auto:{rule}:{hash of account and item ids}` (with a `:{n}` suffix when the same items were cleared under it and
  that clearing was reset, so they clear again as a new document). It never makes a residual item. A group that cannot clear is
  logged as skipped with the reason and the run goes on. Items taken by an earlier rule are not offered to a
  later one. Simulation reads only. A rule reads at most 100,000 items per run (`truncated`).
- A committed run (`Idempotency-Key` required) is stored in `auto_clearing_run` with its group log
  (`auto_clearing_run_group`, migration 0015, RLS, grants); `GET /auto-clearing-runs[/{id}]` reads them (the log
  returns the first 1,000 groups). Each cleared group emits `dev.passage.ledger.openitem.cleared.v1` with mode `auto`.
- Web: automatic clearing rule list and form, run page (simulate, clear, earlier runs) and run detail.

## Dimension derivation (slice 10, FR-11.22, BR-11.14, AC-11.15)

- A ledger has versioned derivation strategies: `PUT/GET /ledgers/{ledger}/derivation-strategies[/{version}]`
  (the next number is created as a draft with 201; only a draft is replaced; `ETag` is `lockVersion`, optional
  `If-Match`, 412 `LEDGER_VERSION_CONFLICT`) and `POST .../{version}/activate`, which validates the draft against the
  ledger's dimensions and lookup tables (422 `LEDGER_DERIVATION_INVALID`), retires the active version and activates
  this one, one active version per ledger (partial unique index). Versions are never deleted.
- Steps (at most 50, strictly increasing `sequence`) are `table_lookup`, `move`, `clear` and `constant`, each with an
  optional condition (`eq`, `ne`, `in`, `blank`, `notblank` on `ACCOUNT`, `ACCOUNT_TYPE` or a dimension) and an
  `overwrite` flag. A step may not read the dimension it writes; `clear` needs `overwrite`. The PRD's `expression`
  (CEL) kind is not built.
- Posting runs the active strategy after the account lookup and before structure validation, in `POST /journal-entries`,
  batch submit and posting, set posting and `POST /validate`. Precedence (FR-11.22): a value the posting request
  carries is never changed by any step (not even with `overwrite`, nor by `clear`); a step with `overwrite` replaces a
  value set by an earlier step; a step without `overwrite` only fills a blank. Step ids are assigned by the service
  and stay unique across versions (a draft keeps its own on replace). The stored line carries `derived_by_step_ids` (migration 0016, `derivedByStepIds` on entry lines),
  and `/validate` returns each line's `dimensions`, `derivedByStepIds` and a `derivation` trace (step, before, after,
  matched lookup row). Derived values go through the same dimension checks as typed ones.
- Lookup tables: `PUT/GET /ledgers/{ledger}/lookup-tables[/{id}]` map a source tuple (`ACCOUNT`, `ACCOUNT_TYPE`,
  dimensions) to one target dimension. A PUT replaces all rows (at most 5,000; `validFrom`, optional `validTo`).
  Rows match on the full tuple with `*` wildcards: fewest wildcards wins, then the latest `validFrom`; a blank source
  value matches only `*`. A table used by a step keeps its definition (409 `LEDGER_LOOKUP_TABLE_IN_USE`).
- Activation and table replacement write `dev.passage.ledger.rules.changed.v1` through the outbox. Duty
  `ledger.rules.write` maintains rules; reading uses `ledger.journal.read`.
- Web: lookup table list and form (rows pasted as comma or tab separated lines), strategy version list and
  step editor with save draft and save-and-activate; the journal entry screen marks lines a step derived
  (`apps/web/src/modules/ledger`).

## Error codes

Rule failures are 422 with `violations[]` (`code`, `lineIndex`, `field`,
`message`); the first violation populates the problem's `code`. Codes raised
today:

| Code | Meaning |
|---|---|
| `LEDGER_DERIVATION_INVALID` | A derivation strategy has a shape or reference problem (BR-11.14) |
| `LEDGER_LOOKUP_TABLE_INVALID` | A lookup table definition or row is invalid for the ledger |
| `LEDGER_UNBALANCED` | Debits do not equal credits in the ledger currency (BR-11.1) |
| `LEDGER_LINE_EMPTY` | Entry has no lines, or a zero-amount line without text or quantity (BR-11.2) |
| `LEDGER_QUANTITY_INVALID` | Statistical `quantity` is negative, over 6 decimals or too large, or `quantity` and `unit` (A-Z, 0-9, up to 10) are not set together (BR-11.2) |
| `LEDGER_LINE_LIMIT` | More than 10,000 lines (FR-11.5); 422 problem: more batch lines than the journal name's `linesLimit` (FR-11.9) |
| `LEDGER_AMOUNT_SCALE` | Amount has more decimals than the currency allows (BR-11.3) |
| `LEDGER_CURRENCY_MISMATCH` | Line currency differs from the ledger currency |
| `LEDGER_ACCOUNT_NOT_FOUND` | Account number not in the ledger's chart |
| `LEDGER_ACCOUNT_NOT_POSTABLE` | Total or reporting account (FR-11.1) |
| `LEDGER_ACCOUNT_SUSPENDED` | Posting date on or after `suspended_from` (FR-11.1) |
| `LEDGER_RECON_ACCOUNT_MANUAL` | Manual line on a reconciliation account (BR-11.8) |
| `LEDGER_DIMENSION_INVALID` | Dimension not defined on the ledger, value malformed (FR-11.2), dimension not a segment of the governing structure, or value not allowed by its constraint row (FR-11.3); also a malformed or unknown dimension value register entry |
| `LEDGER_DIMENSION_REQUIRED` | Blank segment the governing constraint row does not allow (FR-11.3, AC-11.4) |
| `LEDGER_DIMENSION_INACTIVE` | Registered value suspended or outside `activeFrom`..`activeTo` on the posting date (FR-11.2) |
| `LEDGER_STRUCTURE_NOT_FOUND` | No active structure in effect covers the account on the posting date (FR-11.3); 404 for an unknown structure id |
| `LEDGER_STRUCTURE_INVALID` | Malformed account structure definition |
| `LEDGER_STRUCTURE_LOCKED` | 409: change to an active or retired structure version |
| `LEDGER_STRUCTURE_VERSION_EXISTS` | 409: the structure name and version already exist on the ledger |
| `LEDGER_DATE_INVALID` | Posting date more than `futurePostingDays` ahead (BR-11.4), a reversal dated before the original (FR-11.8), or a reversal on another date with a reason code that forbids it (FR-11.15) |
| `LEDGER_PERIOD_CLOSED` | Posting date before the ledger's `firstOpenDate` (BR-11.4; full period status is wave 14) |
| `LEDGER_REVERSAL_ALREADY_REVERSED` | Entry already has a reversing entry (FR-11.8) |
| `LEDGER_REINSTATEMENT_NOT_ALLOWED` | 422: reversing a reversing entry on a ledger with `allowReinstate` false (FR-11.8) |
| `LEDGER_POSTING_RESTRICTED` | 403: a user other than the creator posts a batch of a journal name with `postingRestriction` `own_journals_only` (FR-11.9) |
| `LEDGER_REASON_REQUIRED` | Reversal without a reason code on a ledger that requires one, an unknown or inactive code, or a missing comment the code requires (FR-11.15) |
| `LEDGER_REASON_INVALID_MODULE` | Reason code does not apply to `ledger` (FR-11.15) |
| `LEDGER_NEGATIVE_POSTING_DISABLED` | Reason code allows negative posting but the ledger does not (BR-11.16) |
| `LEDGER_IDEMPOTENCY_CONFLICT` | 409: key reused with a different payload (BR-11.6) |
| `LEDGER_IDEMPOTENCY_IN_PROGRESS` | 409 with `Retry-After: 1`: the same key is being committed by another request (FR-11.5) |
| `LEDGER_NOT_FOUND` | 404: ledger does not exist or is outside the caller's tenant or legal-entity scope |
| `LEDGER_ENTRY_NOT_FOUND` | 404: journal entry does not exist or is outside the caller's scope |
| `LEDGER_REASON_NOT_FOUND` | 404: reason code does not exist in the tenant |
| `LEDGER_CODE_DUPLICATE` | 409: a ledger with this code already exists in the tenant |
| `LEDGER_DESCRIPTION_TEMPLATE_NOT_FOUND` | 404: description template does not exist or belongs to a legal entity outside the caller's scope |
| `LEDGER_DESCRIPTION_TEMPLATE_DUPLICATE` | 409: a template already exists for the scope, legal entity, source type and language (FR-11.17) |
| `LEDGER_JOURNAL_NAME_NOT_FOUND` | 404: journal name does not exist in the tenant |
| `LEDGER_JOURNAL_NAME_INVALID` | 422: a batch names an unknown or inactive journal name (create, submit) |
| `LEDGER_BATCH_NOT_FOUND` | 404: journal batch does not exist or is outside the caller's tenant or legal-entity scope |
| `LEDGER_BATCH_STATE` | 409: the batch's status does not allow the action (BR-11.10), or a posted batch is posted again with another key |
| `LEDGER_ATTACHMENT_INVALID` | 422: a malformed attachment or note (name, document type, retention class, store reference, content type, size over 25 MB), with `violations[]` |
| `LEDGER_ATTACHMENT_RETAINED` | 409: the file of a posted batch is retained until its retention class expires (counted from the posting date) |
| `LEDGER_ATTACHMENT_LIMIT` / `LEDGER_ATTACHMENT_CONFLICT` / `LEDGER_ATTACHMENT_NOT_FOUND` | 409 for the 51st file or 201st note of a batch, 409 for an id re-used with other content, 404 for an unknown id |
| `LEDGER_BATCH_APPROVAL_REQUIRED` | 409: posting a validated batch whose journal name now requires approval; submit it for approval |
| `LEDGER_SELF_APPROVAL` | 403: the submitter approves a batch of a journal name that does not allow self-approval |
| `LEDGER_VERSION_CONFLICT` | 412: `If-Match` names an older batch version |
| `LEDGER_OI_ACCOUNT_INVALID` | 422: account cannot be open-item managed (BR-11.18) |
| `LEDGER_OI_ACTIVATION_BALANCE` | 422: account has a balance; an activation run is needed (FR-11.19, LEDGER-0023) |
| `LEDGER_OI_OPEN_ITEMS_EXIST` | 409: switching off an account that still has open items |
| `LEDGER_OPEN_ITEM_NOT_FOUND` | 404: an item does not exist in the ledger |
| `LEDGER_OPEN_ITEM_ALREADY_CLEARED` | 409: an item is already cleared or voided (BR-11.13, LEDGER-0021); also a reversal of an entry whose items are cleared |
| `LEDGER_CLEARING_UNBALANCED` | 422: residual above tolerance and no residual item requested (BR-11.12, LEDGER-0020) |
| `LEDGER_CLEARING_ACCOUNT_MIXED` | 422: selected items are on more than one account |
| `LEDGER_CLEARING_DATE_INVALID` | 422: clearing date before the latest item posting date |
| `LEDGER_CLEARING_RESIDUAL_DEPTH` | 422: the residual chain would exceed 20 items |
| `LEDGER_CLEARING_DIFFERENCE_ACCOUNT` | 422: the tolerance group's difference account is open-item managed |
| `LEDGER_OPEN_ITEM_ACCOUNT_REQUIRED` | 422: the items' account is no longer open-item managed |
| `LEDGER_CLEARING_NOT_FOUND` | 404: clearing document does not exist or is outside the caller's scope |
| `LEDGER_CLEARING_ALREADY_RESET` | 409: the document was reset by another request |
| `LEDGER_CLEARING_RESIDUAL_USED` | 409: the residual item of the clearing is already cleared |
| `LEDGER_TOLERANCE_GROUP_NOT_FOUND` | 404: tolerance group does not exist in the ledger |
| `LEDGER_TOLERANCE_GROUP_INACTIVE` | 422: the named tolerance group is inactive |
| `LEDGER_DESCRIPTION_TRUNCATED` | Info notice in `notices[]` of an accepted posting: the expanded default description was cut to 255 characters (FR-11.17, LEDGER-0031) |

Platform codes: `AUTH-0001` (401, missing or malformed tenant), `AUTH-0003`
(403, missing duty or caller scoped to another legal entity), `COM-0001`
(400, malformed body or validation), `COM-0500` (500, detail hidden).

The full catalogue in PRD section 2.11.18 is the target. Codes not listed above
are not raised yet and are tracked in NOT-IMPLEMENTED.md.

## Dependencies

Only the platform module and its existing dependencies (chi, pgx/v5,
shopspring/decimal, google/uuid) are used.
