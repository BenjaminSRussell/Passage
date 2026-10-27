# Passage ledger service

The ledger is the universal journal: one append-only, double-entry journal per
ledger, the chart of accounts, the posting API every subledger calls, and the
balance projection that trial balances read. Wave 11 (PSG-11, slices 1 to 4).

Reference: `docs/prd/phase-2-financial-core.md` section 2.11. What is built and
what is not is listed in [NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md).

## What slice 1 does

- Chart of accounts: `PUT /api/v1/ledger/charts/{chart}/accounts/{number}` and
  paged listing. Account types, postable flag, suspension date and reconciliation
  account type (manual journals, source `ledger`/`manual_journal`, may not post to
  reconciliation accounts; subledgers may, BR-11.8).
- Ledgers: create and read a ledger bound to a chart, a currency and up to 20
  dimension attribute names.
- Posting: `POST /api/v1/ledger/journal-entries` posts a balanced entry atomically
  or rejects it entirely with every violation in one 422 response. Idempotency
  keys replay identical requests (200, `idempotency.replayed = true`) and reject
  reused keys with different payloads (409 `LEDGER_IDEMPOTENCY_CONFLICT`).
- Validation dry run: `POST /api/v1/ledger/validate` returns the same violations
  and the resolved dimension combinations without writing.
- Reversal: `POST /api/v1/ledger/journal-entries/{id}/reverse` writes an exact
  negating entry linked to the original line by line, dated on the original posting
  date or a later one. A second reversal is refused; reversing the reversal
  re-instates the original.
- Balances: a projection per (account, dimension combination, period) written in
  the posting transaction; `GET /api/v1/ledger/balances` is the trial balance with
  a balanced flag; `POST /api/v1/ledger/balances:rebuild` recomputes the projection
  from the journal and reports differing rows.
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
  `GET /api/v1/ledger/reason-codes`, `GET`/`PUT /api/v1/ledger/reason-codes/{code}`
  (201 created, 200 replaced). Codes are `^[A-Z0-9_]{1,10}$` with a name,
  `appliesTo` modules, `requiresComment`, `negativePostingAllowed`,
  `alternativePostingDateAllowed` and `active`. Codes are deactivated, never
  deleted, because reversing entries reference them.
- Ledger parameters `reversalReasonRequired` (default true) and
  `negativePostingEnabled` (default false), set when the ledger is created.
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

- Account structures (FR-11.3): `GET /api/v1/ledger/ledgers/{ledger}/account-structures`,
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
- Dimension value register (FR-11.2): `GET /api/v1/ledger/ledgers/{ledger}/dimension-values`
  and `PUT .../dimension-values/{attribute}/{value}` with a name, `activeFrom`/`activeTo`
  (inclusive) and `suspended`. Posting a registered value that is suspended or outside its
  dates is `LEDGER_DIMENSION_INACTIVE`; unregistered values are format-checked only.
- Web: account structure list and editor (segments, filter, constraint grid, activate,
  retire) and a dimension value list and form.

## What slice 4 adds

- Default description templates (FR-11.17): `GET /api/v1/ledger/description-templates`,
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

## Layout

```
cmd/ledger/            entrypoint: dbx.Connect -> dbx.Migrate -> httpx router -> register -> serve
internal/domain/       pure rules: validation, combination hashing, balance aggregation, reversal
internal/store/        Postgres repositories (pgx, RLS through dbx.WithTenantTx)
internal/api/          HTTP handlers, DTOs, duties (authz.RequireDuty)
internal/events/       event type constants and payloads
migrations/            0001..0008 SQL, applied in order by dbx.Migrate (0005: passage_app grants; 0006: reason codes; 0007: account structures, dimension values; 0008: description templates)
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
| `ledger.account.write` | `PUT /charts/{chart}/accounts/{number}` |
| `ledger.journal.read` | `GET /ledgers/{id}`, `GET /charts/{chart}/accounts`, `GET /journal-entries/{id}`, `GET /reason-codes[/{code}]`, `GET /ledgers/{ledger}/account-structures[/{id}]`, `GET /ledgers/{ledger}/dimension-values`, `GET /description-templates[/{id}]` |
| `ledger.journal.post` | `POST /journal-entries`, `POST /validate` |
| `ledger.journal.reverse` | `POST /journal-entries/{id}/reverse` |
| `ledger.balance.read` | `GET /balances` |
| `ledger.balance.rebuild` | `POST /balances:rebuild` |
| `ledger.reason.write` | `PUT /reason-codes/{code}` (PRD duty "Maintain reason codes") |
| `ledger.structure.write` | `PUT /ledgers/{ledger}/account-structures/{id}` (PRD duty "Maintain account structures") |
| `ledger.dimension.write` | `PUT /ledgers/{ledger}/dimension-values/{attribute}/{value}` (PRD duty "Maintain financial dimensions") |
| `ledger.setup.write` | `PUT /description-templates/{id}` (PRD duty "Maintain ledger setup") |

## Error codes

Rule failures are 422 with `violations[]` (`code`, `lineIndex`, `field`,
`message`); the first violation populates the problem's `code`. Codes raised
today:

| Code | Meaning |
|---|---|
| `LEDGER_UNBALANCED` | Debits do not equal credits in the ledger currency (BR-11.1) |
| `LEDGER_LINE_EMPTY` | Entry has no lines, or a zero-amount line without text (BR-11.2) |
| `LEDGER_LINE_LIMIT` | More than 10,000 lines (FR-11.5) |
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
| `LEDGER_DESCRIPTION_TRUNCATED` | Info notice in `notices[]` of an accepted posting: the expanded default description was cut to 255 characters (FR-11.17, LEDGER-0031) |

Platform codes: `AUTH-0001` (401, missing or malformed tenant), `AUTH-0003`
(403, missing duty or caller scoped to another legal entity), `COM-0001`
(400, malformed body or validation), `COM-0500` (500, detail hidden).

The full catalogue in PRD section 2.11.18 is the target. Codes not listed above
are not raised yet and are tracked in NOT-IMPLEMENTED.md.

## Dependencies

Only the platform module and its existing dependencies (chi, pgx/v5,
shopspring/decimal, google/uuid) are used.
