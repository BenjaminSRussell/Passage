# Passage ledger service

The ledger is the universal journal: one append-only, double-entry journal per
ledger, the chart of accounts, the posting API every subledger calls, and the
balance projection that trial balances read. Wave 11 (PSG-11, slices 1 and 2).

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

## Layout

```
cmd/ledger/            entrypoint: dbx.Connect -> dbx.Migrate -> httpx router -> register -> serve
internal/domain/       pure rules: validation, combination hashing, balance aggregation, reversal
internal/store/        Postgres repositories (pgx, RLS through dbx.WithTenantTx)
internal/api/          HTTP handlers, DTOs, duties (authz.RequireDuty)
internal/events/       event type constants and payloads
migrations/            0001..0006 SQL, applied in order by dbx.Migrate (0005: passage_app grants; 0006: reason codes)
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
| `ledger.journal.read` | `GET /ledgers/{id}`, `GET /charts/{chart}/accounts`, `GET /journal-entries/{id}`, `GET /reason-codes[/{code}]` |
| `ledger.journal.post` | `POST /journal-entries`, `POST /validate` |
| `ledger.journal.reverse` | `POST /journal-entries/{id}/reverse` |
| `ledger.balance.read` | `GET /balances` |
| `ledger.balance.rebuild` | `POST /balances:rebuild` |
| `ledger.reason.write` | `PUT /reason-codes/{code}` (PRD duty "Maintain reason codes") |

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
| `LEDGER_DIMENSION_INVALID` | Dimension not defined on the ledger, or value malformed (FR-11.2) |
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

Platform codes: `AUTH-0001` (401, missing or malformed tenant), `AUTH-0003`
(403, missing duty or caller scoped to another legal entity), `COM-0001`
(400, malformed body or validation), `COM-0500` (500, detail hidden).

The full catalogue in PRD section 2.11.18 is the target. Codes not listed above
are not raised yet and are tracked in NOT-IMPLEMENTED.md.

## Dependencies

Only the platform module and its existing dependencies (chi, pgx/v5,
shopspring/decimal, google/uuid) are used.
