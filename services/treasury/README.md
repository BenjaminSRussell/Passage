# treasury

Treasury bounded context (Go). Wave 36 (PSG-36), slice 1: bank accounts and
their lifecycle, bank statement import (camt.053), the cash forecast, and the
pure matching-score engine (domain only, not yet behind an API). Slice 3:
checks with gap-free numbers, void and reprint; bank facilities with letters
of credit and guarantees under a limit; fixed-rate loan contracts with their
schedules. Slice 4: BAI2 and MT940 statement parsers beside camt.053, with the
format detected from the file's content, an optional statement account number
on the bank account (so files that name the account by its domestic number
match), and the reconciliation close rule and BR-36.3 state machine (domain
only, like the matching engine). Slice 5: stored reconciliation rules, a stored match table
(N:1, 1:N, manual match and unmatch) with run, match and unmatch routes, and an SFTP bank
channel adapter with an in-memory test double. Screens live in `apps/web/src/modules/treasury`.

## Run

```sh
export DATABASE_URL=postgres://passage_app:...@localhost:5432/passage_treasury
export MIGRATE_ON_START=true          # dev only; stage/prod run migrations as a PreSync Job
export TREASURY_MAX_FILE_MB=50        # 1-200, default 50
go run ./cmd/treasury                 # HTTP :8080, admin (/metrics) :9090
```

Env: `DATABASE_URL` (required), `MIGRATE_ON_START`, `TREASURY_MAX_FILE_MB`,
`HTTP_ADDR` (default `:8080`), `ADMIN_ADDR` (default `:9090`).

## Test

```sh
go vet ./... && go test -count=1 ./...
PASSAGE_TEST_DATABASE_URL=postgres://... go test -count=1 ./...   # runs DB tests
```

Store tests need `PASSAGE_TEST_DATABASE_URL` (admin, applies migrations) and
`PASSAGE_TEST_APP_DATABASE_URL` (a non-superuser role, so row-level security
applies); they skip without them. API tests use an in-memory fake store and
always run, including the OpenAPI-matches-routes contract test.

## Layout

- `internal/domain`: bank accounts, statement and forecast invariants, the check
  lifecycle, the facility limit rule, day counts and loan schedules (pure, no I/O).
- `internal/store`: pgx repositories, tenant-scoped transactions (RLS).
- `internal/api`: HTTP handlers, RFC 9457 problems, idempotency and `If-Match`.
- `internal/events`: event type constants and payloads.
- `internal/bankconnect`: bank file sending. `Signer` (Vault transit `sign`; private keys never leave Vault), an SFTP channel (`SFTP` over an `SFTPClient` interface; drop `.sig` and `.part`, publish by rename), and `Sender`, which audits every file in `bank_file_audit` (migration 0007) before and after it is sent. Not yet wired to a route. `Vault.EBICSKeys` is the EBICS key custody (A006 and X002 signed in transit, E002 read from KV for each unwrap).
- `internal/ebics`: EBICS H004 client. Subscriber state machine NEW, INI_SENT, HIA_SENT, BANK_KEYS_PENDING, READY (bank key hashes confirmed by a named person); INI, HIA and HPB key orders and the letters; upload (CCT, CDD, CDB, CIP) and download (C53, C52, C54, HAC, PTK, STA, VMK) transactions with C14N `AuthSignature` (X002), E002 hybrid encryption, zlib and segmentation; return codes mapped to proceed, no-data, back off or park (`Classify`). `Client.FetchStatements` feeds `Postgres.ImportStatementFile`. Migration `0009_ebics_subscriber.sql`. Not yet wired to a route or a schedule.
- `migrations/0008_reconciliation.sql`: `reconciliation_rule`, `reconciliation_match`,
  `reconciliation_match_line`, `reconciliation_match_item` (a line and an item are in at
  most one active match; an unmatch keeps the row as history) and the column-level
  `UPDATE (status, version)` grant on `bank_statement_line`.
- `migrations/0001_init.sql`: tables `bank_account`, `bank_statement`,
  `bank_statement_line`, `cash_forecast_entry`, `idempotency_keys`, `outbox`.
- `migrations/0002_checks_facilities_loans.sql`: `bank_account.next_check_number`
  and tables `bank_check`, `treasury_facility`, `treasury_facility_instrument`,
  `treasury_loan`, `treasury_loan_schedule_line`.
  Every tenant table has `tenant_id` first, RLS enabled and forced, and the
  legal-entity scope policy where it carries `legal_entity_id`.
- `api/openapi.yaml`, `events/asyncapi.yaml`: contracts.
- `charts/treasury`: Helm chart (`/healthz`, `/readyz`, ServiceMonitor toggle).

## Routes

Bank accounts, statements, cash forecast, checks, facilities and loans under
`/v1/treasury/...`. See `api/openapi.yaml` for the full list. Every `POST` that
creates something (`bank-accounts`, `cash-forecast-entries`, `checks`,
`facilities`, `facilities/{id}/instruments`, `loans`) requires
`Idempotency-Key` (stored in `idempotency_keys` in the same transaction as the
insert). Custom methods (`:activate`, `:deactivate`, `:close` on accounts;
`:print`, `:send`, `:clear`, `:stop`, `:void`, `:reprint` on checks; `:close`
on facility instruments) require `If-Match` with the resource ETag. Statement
import is idempotent on the file's SHA-256 (BR-36.8).

Checks: the number comes from `bank_account.next_check_number` under a row
lock, so numbers are gap-free per account; a void keeps the number, a reprint
voids the old check and prints a new one on the next number in one
transaction. Every void writes `dev.passage.treasury.check.voided.v1` to the
outbox; `positivePayVoid` is true when the check was sent or stopped (BR-36.5).

Facilities: utilization is the sum of open instruments; an instrument that
would take utilization past the limit is refused (TRS-0415) unless the
request sets `overrideLimit` and the caller holds `treasury.facility.override`
(AC-36.6). The facility row is locked while booking, so concurrent instruments
cannot both pass the limit.

Loans: the handler builds the schedule (`domain.BuildSchedule`) and the store
keeps it with the contract. Interest per period is opening principal × rate ×
days / basis (ACT/360, ACT/365 or 30E/360), rounded half-up to the currency
minor unit; the last period repays the remaining principal (BR-36.15).

Reconciliation: `POST /reconciliation-rules` stores a rule (criterion weights summing to 100,
amount tolerance, date window, strict flag, priority; for one bank account or the whole legal
entity). `POST /reconciliation-runs` applies the account's active rules to a statement's
unmatched lines against the open items the caller offers (ledger, AR and AP own them): unique
matches are stored and published, N:1 batch receipts match when the items sharing the line's
batch reference add up exactly, an item wanted by two matches is released from both, and other
lines with a candidate become `suggested`. `POST /reconciliation-matches` matches lines to
items by hand (reason required, must balance), `:unmatch` dissolves a match with a reason and records who did it.
A run and a manual match carry the `legalEntityId` of the statement (TRS-0488 when it differs, 403 outside the caller's scope); entity and item ids are lower-cased on the way in, so letter case never changes a scope check, an item match or the rule cap. A bank statement's MT940 `:61:` bank reference (after `//`) is stored apart as `bank_reference` and is never read as a payer reference.
A run considers one page of open lines after `afterLineNo`; `remainingLines` and `nextAfterLineNo` drive the next call.
`GET /reconciliation-matches` pages with `limit` (max 500) and `offset`, and returns `nextOffset` for a full page.
A placeholder end-to-end id (NOTPROVIDED, NONREF) is ignored and the remittance reference is scored instead.
A reference-only candidate (the reference agrees, amount, date or payer does not) is suggested and never auto-matched.
A rule that does not require the reference never auto-matches a candidate whose reference contradicts a usable reference of the line (8 or more characters, not a placeholder); it is suggested.
A bank account has at most 20 active rules, its own and the entity-wide ones (TRS-0489). Item amounts are trimmed before they are validated and stored.
All creates take `Idempotency-Key`; `:deactivate` and `:unmatch` take `If-Match`. Events:
`dev.passage.treasury.reconciliation.matched.v1` and `...unmatched.v1`.

Duties: `treasury.view`, `treasury.bank-account.write`,
`treasury.bank-account.approve`, `treasury.statement.import`,
`treasury.forecast.write`, `treasury.check.write`, `treasury.facility.write`,
`treasury.facility.override`, `treasury.loan.write`, `treasury.reconciliation.write` (the PRD's `TreasuryView`,
`TreasuryBankAccountMaintain`, `TreasuryStatementImport`,
`TreasuryCheckMaintain`, `TreasuryFacilityMaintain`,
`TreasuryFacilityOverride` and `TreasuryLoanMaintain` in the kernel's dotted
naming).

## Error codes

Problems use `type` `https://passage.dev/problems/<code>` and carry `code` and
`serviceCode`.

| code | HTTP | meaning |
|------|------|---------|
| `AUTH-0001` | 401 | missing or malformed tenant |
| `COM-0001` | 400 | request failed validation (field errors in `errors`) |
| `TRS-0400` | 404 | bank account not found |
| `TRS-0401` | 422 | IBAN fails shape or mod-97 check |
| `TRS-0403` | 422 | legal entity holds 500 bank accounts already |
| `TRS-0404` | 409 | duplicate bank account code or IBAN |
| `TRS-0405` | 200 | identical file, or the same statement id with the same closing balance, re-uploaded: returns the existing statement (replay, not an error) |
| `TRS-0406` | 409 | same statement id with a different closing balance |
| `TRS-0407` | 201 (warning) | electronic sequence number skips numbers; returned in `warnings`, the statement is stored |
| `TRS-0408` | 422 | statement currency differs from the account currency |
| `TRS-0409` | 422 | the statement's account (IBAN, or BAI2 / MT940 / camt `Othr` account number) is neither the bank account's IBAN nor its `statementAccount` |
| `TRS-0412` | 422 | statements can only be imported into, and checks issued on, an active account |
| `TRS-0411` | 409 | a statement line or item is already in an active match |
| `TRS-0413` | 404 | statement not found |
| `TRS-0414` | 422 | a cleared check cannot be voided or reprinted (BR-36.5) |
| `TRS-0415` | 422 | facility limit exceeded by the amount in `detail` (FR-36.7) |
| `TRS-0417` | 422 | account status does not allow the requested transition |
| `TRS-0420` | 422 | check status does not allow the requested action |
| `TRS-0423` | 404 | check not found |
| `TRS-0426` | 422 | check currency differs from the bank account currency |
| `TRS-0427` | 409 | duplicate facility code for the legal entity |
| `TRS-0428` | 404 | facility not found |
| `TRS-0429` | 422 | facility is not active, or expired before the instrument's issue date |
| `TRS-0430` | 422 | instrument currency differs from the facility currency |
| `TRS-0434` | 422 | instrument is not open |
| `TRS-0435` | 404 | facility instrument not found |
| `TRS-0436` | 422 | reconciliation state does not allow the transition (BR-36.3; domain rule, no endpoint yet; TRS-0410 is the close difference) |
| `TRS-0446` | 422 | loan terms cannot produce a schedule (maturity not a whole number of periods, instalment below interest) |
| `TRS-0447` | 404 | loan contract not found |
| `TRS-0480` | 422 | the bank account has no active reconciliation rule |
| `TRS-0481` | 422 | a manual match's lines and items do not add up to the same total |
| `TRS-0482` | 409 | a rule with this name exists for the legal entity |
| `TRS-0483` | 404 | reconciliation rule not found |
| `TRS-0484` | 404 | reconciliation match not found |
| `TRS-0485` | 409 | the match is already unmatched |
| `TRS-0486` | 422 | a line is not on the statement, or a line or item is not in the statement currency |
| `TRS-0487` | 409 | the reconciliation rule is already inactive |
| `TRS-0488` | 422 | the items or the request belong to a different legal entity than the statement |
| `TRS-0489` | 422 | the rule would give a bank account more than 20 active reconciliation rules |
| `TRS-0490` | 413 | statement file exceeds `TREASURY_MAX_FILE_MB` |
| `TRS-0491` | 400 | statement file could not be read |
| `TRS-0493` | 422 | statement cannot be parsed as camt.053, BAI2 or MT940, or the file holds more than one statement; `errors.line` names the BAI2 / MT940 line, `errors.entry` the camt.053 entry |
| `TRS-0494` | 422 | opening balance plus lines does not equal closing balance |
| `TRS-0495` | 404 | EBICS subscriber not found |
| `TRS-0496` | 409 | an EBICS subscriber with this host, partner and user already exists |
| `TRS-0497` | 422 | EBICS subscriber host, partner or user id, or bank URL, is not acceptable |
| `conflict_version` | 412 | `If-Match` does not match the current ETag |
| `precondition_required` | 428 | `If-Match` is required on every custom method |
| `AUTH-0003` | 403 | missing duty, including `treasury.facility.override` when `overrideLimit` is set; or a legal-entity-scoped caller creating a bank account, forecast entry, facility or loan for another legal entity |
| `idempotency_key_reuse` | 422 | `Idempotency-Key` reused with a different body |

## Dependencies

Only the platform module and what it pulls in (chi, pgx/v5, shopspring/decimal,
google/uuid). No new dependencies.
