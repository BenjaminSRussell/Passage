# passage-receivables

Accounts receivable for Passage, wave 30 (PSG-30), slices 1 to 3. Slice 1 is
the cash-application core: open items, aging, manual payments, the scored
matcher (BR-30.1) and settlement validation (BR-30.4). Slice 2 adds BAI2 lockbox
import (FR-30.3) and collections: the risk-ranked worklist (FR-30.6, FR-30.10),
disputes and promises to pay (FR-30.9), dunning runs (FR-30.7, BR-30.2) and
interest notes (FR-30.8, BR-30.5). Slice 3 adds minimal collection cases
(FR-30.18), customer balance confirmations (FR-30.20, BR-30.13) and down
payments and security deposits (FR-30.21, BR-30.14). Everything else in the wave is listed in
[NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md).

## What it does now

- **Open items** (`GET /api/v1/receivables/open-items`): customer and status filters, page limit up to 500.
- **Aging** (`GET /api/v1/receivables/aging?asOf=`): totals per currency and bucket (`CURRENT`, `1-30`, `31-60`, `61-90`, `91-180`, `OVER-180`), with due date as the basis.
- **Payments**: record a manual payment (`POST /payments`, idempotent), list them (`GET /payments?status=&customer=&limit=`), read one (`GET /payments/{id}`, ETag = version), score candidate settlements (`POST /payments/{id}:match`, changes nothing) and apply (`POST /payments/{id}:apply`, requires `If-Match`; an optional `Idempotency-Key` replays the first response).
- **Apply rules**: settlements plus on-account residual equal the unapplied amount; every item is in the payment's legal entity and currency (no cross-currency settlement yet) and belongs to the payment's customer; an unidentified payment takes the customer of the items it settles; a cash discount needs the value date on or before the discount date and may not exceed the item's discount; write-off tolerance is the larger of 5.00 and 0.5% of the original item amount.
- **Lockbox** (`POST /lockbox/imports`, duty ARImport; `GET /lockbox/imports`): a BAI2 file (records 01, 02, 03, 16 with S/V/D funds-type detail, 88, 49, 98, 99; trailer counts checked; first error named by line, AR-0004). Each 16 detail with a check credit type code (default 116, Item in Lockbox Deposit; a bank profile may name other credit codes 100-399) becomes one unidentified CHECK payment with source LOCKBOX, the customer reference as remittance, and `payment.received.v1`. The same file content twice is refused by SHA-256 (AR-0003); a check re-sent in another file is refused by (source, bank reference).
- **Worklist** (`GET /worklist?legalEntityId=&asOf=`): one row per customer and currency with total open, overdue, oldest days, open-amount-weighted days late, dunning level, open disputes, broken promises and a 0-100 risk score (overdue share 40, days late 20, dunning level 20, disputes 15, broken promises 5), riskiest first. The open amounts are summed in SQL, so the read is complete for any number of items.
- **Disputes** (`POST /disputes`, `GET /disputes?status=active|<status>&customer=&openItem=`, `GET /disputes/{id}`, `POST /disputes/{id}:review`, `:resolve`; duty CollectionsMaintain to change): one active dispute per item, amount at most the open amount, SLA 120 hours. OPEN → UNDER_REVIEW → RESOLVED_CREDIT | RESOLVED_PAID | RESOLVED_WRITE_OFF, REJECTED from OPEN or UNDER_REVIEW (BR-30.6). While active the item carries `dispute_id` and is left out of dunning and interest.
- **Promises to pay** (`POST /promises`, `GET /promises`): amount at most the open amount, date today or later; one holding promise per item. A promise holds until its date plus 2 days (BR-30.2); after that it counts as broken on the worklist and the item is dunnable again.
- **Dunning** (`POST /dunning-runs`, `GET /dunning-runs`, `GET /dunning-runs/{id}`, `POST /dunning-runs/{id}:review`, `:issue`, `:cancel`; duty DunningRun): a run proposes letters as DRAFT per customer and currency. Level 1 once the grace days (default 5) pass, 2 at 15 days, 3 at 30 (fee 25.00 USD / 20.00 EUR), 4 at 45 (fee 50.00 / 40.00); an item rises one level per run and never falls; letter level is the highest item level; items under 1.00 are ignored; blocked, disputed and promised items, letters under 50.00 USD / 45.00 EUR, and customers dunned fewer than 10 days ago are left out with a reason. Issue needs REVIEWED (AR-0007), re-checks every letter under row locks (a letter whose items were paid, disputed, promised or raised since the proposal is SKIPPED), raises item levels, posts the fee as a FEE open item and publishes one `dunningletter.issued.v1` per letter.
- **Interest** (`POST /interest-notes:calculate` preview, `:post`, `GET /interest-notes`; duty InterestPost): open amount × annual rate × days ÷ 365 (or 360), from due date + grace or from the item's last note, so no day is charged twice; disputed and promised items, INTEREST and FEE items, and amounts under 5.00 are left out. Post writes one INTEREST open item and one note per item and calendar month, serialised per legal entity by an advisory lock; `Idempotency-Key` replays the stored response.
- **Collection cases** (`POST /cases`, `GET /cases?status=&category=&customer=`, `GET /cases/{id}`, `POST /cases/{id}:assign`, `:close`; duty CollectionsMaintain to change, ARView to read): category COLLECTION, DISPUTE or DEDUCTION with its SLA (72, 120, 240 hours), owner, priority, reason, an optional linked open item (same customer and legal entity; the case takes its currency) and an attached amount. `slaBreached` is computed on read (AR-0018). Assign and close need `If-Match`; a closed case cannot change.
- **Balance confirmations** (`POST /balance-confirmations`, `GET /balance-confirmations`, `GET /balance-confirmations/{id}`, `GET /balance-confirmations/{id}/differences`, `POST /balance-confirmations/{id}:reply`; duty BalanceConfirmationRun to run and record, ARView to read): a run selects each customer and currency whose balance on the key date reaches the threshold (default 10,000.00, debit or credit), zero balances only with `includeZeroBalances`, optionally a sample of n ranked by a hash seeded with the `Idempotency-Key`. The balance on the key date is every item documented by then less the settlements and down payment clearings made by then, so later payments are added back. A reply is AGREED or DISAGREED (with the customer's balance); it is immutable, a repeat of the recorded reply returns it, and a DISAGREED reply opens a 120-hour DISPUTE case with reason BALANCE_DIFFERENCE and the customer's balance attached, in the same transaction (BR-30.13). The difference list holds every DISAGREED row with both balances, the difference and the case.
- **Down payments and security deposits** (`POST /down-payments`, `GET /down-payments?customer=&status=&kind=`, `GET /down-payments/{id}`, `POST /down-payments/{id}:receive`, `:cancel` (ARPaymentPost), `:clear` (ARSettle), `:release` (ARRefund); all moves need `If-Match`): a request posts nothing; receipt records the received date and publishes Dr bank / Cr 2190 (down payment) or 2195 (deposit). `:clear` locks the item (NOWAIT), clears min(remaining, item open) or a smaller given amount (more is 422 AR-0017), lowers the item's open amount, records a clearing row and publishes Dr 2190 or 2195 / Cr 1200 (AC-30.15). A down payment clears invoices only; a deposit may offset invoice, fee and interest items. `:release` refunds what remains of a deposit (Dr 2195 / Cr bank). An unpaid request can be cancelled.
- **Events** via the transactional outbox: `dev.passage.receivables.payment.received.v1`, `payment.applied.v1`, `dispute.opened.v1`, `dispute.resolved.v1`, `dunningletter.issued.v1` (the PRD's `dunning-letter.issued.v1`; the kernel type rule allows no hyphen), `case.opened.v1`, `case.closed.v1`, `confirmation.replied.v1` and `downpayment.received.v1`, `.cleared.v1`, `.released.v1` (one `downpayment` entity for both kinds; the postings ride on the event until the ledger client exists). See `events/asyncapi.yaml`.

The API contract is `api/openapi.yaml`. Every operation names its duty in
`x-passage-duty`.

## Layout

```
cmd/receivables/       entrypoint: dbx.Connect -> (dbx.Migrate) -> httpx router -> Serve
internal/domain/       pure rules: match.go (BR-30.1), settle.go (BR-30.4), aging.go (FR-30.6),
                       bai2.go (FR-30.3), dunning.go (BR-30.2), interest.go (BR-30.5),
                       collections.go (FR-30.9, BR-30.6), risk.go and worklist.go (FR-30.10),
                       cases.go (FR-30.18), confirmations.go (FR-30.20, BR-30.13),
                       down_payments.go (FR-30.21, BR-30.14)
internal/store/        pgx repositories, RLS via dbx.WithTenantTx, outbox writes
internal/api/          HTTP handlers, DTOs, validation
internal/events/       event types and payloads
migrations/            0001 tables and RLS, 0002 outbox, 0003 idempotency_keys,
                       0004 dispute, promise_to_pay, dunning_run, dunning_letter, interest_note,
                       0005 lockbox_batch, 0006 collection_case, balance_confirmation_run,
                       balance_confirmation, 0007 down_payment, down_payment_clearing
api/openapi.yaml       REST contract
events/asyncapi.yaml   event contract
charts/receivables/    Helm chart (passage-receivables)
```

## Run

```sh
export DATABASE_URL=postgres://passage_app:...@localhost:5432/passage_receivables
export MIGRATE_ON_START=true   # dev only; stage and prod run migrations as a PreSync Job
export HTTP_ADDR=:8080
go run ./cmd/receivables
```

Required: `DATABASE_URL`. Optional: `HTTP_ADDR` (default `:8080`), `ADMIN_ADDR`
(default `:9090`, serves `/metrics`), `MIGRATE_ON_START`.

## Test

```sh
go vet ./... && go test ./...
```

Domain tests are table-driven plus `pgregory.net/rapid` properties. Store and API
integration tests use `platform/go/testkit` and skip unless
`PASSAGE_TEST_DATABASE_URL` is set. Set it to run them against Postgres 17.

## Matching rules implemented

- R1 document number in the remittance as a whole token, so `INV-1001` does not match `INV-10012` (+60), R2 single item equal to the amount (+30) or a subset of up to 5 items (+25), R3 amount equals open minus available cash discount (+28), R6 value date within 7 days of due (+5).
- Auto-apply when the top set scores ≥ 85 and the runner-up is ≤ winner − 20; 60–84 is suggested; below 60 goes on account.
- R4, R5 and R7 need bank-account hashes, name similarity and short-pay reasons that slice 1 does not carry yet.

## Error codes

Errors are RFC 9457 problem details (`application/problem+json`) with a stable `code`.
The `type` is `https://passage.dev/problems/<code>`.

| Code | HTTP | Meaning |
|---|---|---|
| `AUTH-0001` | 401 | No principal, or tenant missing or malformed |
| `AUTH-0003` | 403 | Missing duty, or legal entity outside the caller's scope |
| `COM-0001` | 400 | Request failed validation (`errors` lists fields) |
| `COM-0428` | 428 | `If-Match` required on apply and on dispute, dunning-run, case and down payment moves |
| `conflict_version` | 412 | `If-Match` does not match the resource version |
| `idempotency_key_reuse` | 422 | Same `Idempotency-Key` on a create, apply or interest post with a different body |
| `idempotency_in_progress` | 409 | Two creates with the same `Idempotency-Key` raced; retry to replay |
| `AR-PAYMENT-NOT-FOUND` | 404 | Payment id unknown or unknown action |
| `AR-PAYMENT-DUPLICATE` | 409 | Payment or open item reference already recorded (unique violation) |
| `AR-PAYMENT-STATE` | 409 | Payment state does not allow apply (only RECEIVED, SUGGESTED, ON_ACCOUNT) |
| `AR-OPEN-ITEM-NOT-FOUND` | 404 | Open item in a settlement is unknown |
| `AR-0001` | 400 | Settlements plus residual must equal the payment amount, or the plan is invalid (other legal entity, currency or customer; discount not available or above terms) |
| `AR-0002` | 409 | Open item is locked by another settlement (BR-30.7) |
| `AR-0006` | 422 | Write-off exceeds tolerance |
| `AR-0003` | 409 | Lockbox file already imported (same SHA-256) |
| `AR-0004` | 400 | BAI2 file malformed; `detail` names the line |
| `AR-0007` | 409 | Dunning run must be REVIEWED before issue |
| `AR-DUNNING-STATE` | 409 | Dunning run status does not allow the move |
| `AR-DUNNING-RUN-NOT-FOUND` | 404 | Dunning run id unknown or unknown action |
| `AR-DISPUTE-NOT-FOUND` | 404 | Dispute id unknown or unknown action |
| `AR-DISPUTE-STATE` | 409 | Dispute status does not allow the move, or the item is no longer open |
| `AR-DISPUTE-EXISTS` | 409 | The item already has an active dispute |
| `AR-PROMISE-EXISTS` | 409 | The item already has a promise that has not broken |
| `AR-PROMISE-STATE` | 409 | The item is no longer open |
| `AR-AMOUNT-RANGE` | 400 / 422 | Disputed or promised amount is not positive, or exceeds the open amount |
| `AR-CASE-NOT-FOUND` | 404 | Case id unknown or unknown action |
| `AR-CASE-STATE` | 409 | The case is closed |
| `AR-CASE-MISMATCH` | 400 | The linked open item belongs to another customer or legal entity, or is in another currency |
| `AR-CONFIRMATION-NOT-FOUND` | 404 | Run id unknown, or the confirmation is not in this run |
| `AR-CONFIRMATION-REPLIED` | 409 | A different reply is already recorded; replies are immutable |
| `AR-CONFIRMATION-REPLY` | 400 / 422 | DISAGREED without the customer's balance, or AGREED with a different balance |
| `AR-DOWN-PAYMENT-NOT-FOUND` | 404 | Down payment id unknown or unknown action |
| `AR-DOWN-PAYMENT-STATE` | 409 | The status does not allow the move, or the item is no longer open |
| `AR-DOWN-PAYMENT-MISMATCH` | 400 | The item is another customer's, legal entity's or currency's, or of a type the kind cannot clear |
| `AR-0017` | 422 | Clearing exceeds the remaining down payment or the item's open amount |

`Idempotency-Key` is enforced on create by storing the key on the payment row
(unique per tenant). On apply the key and the response are stored in
`idempotency_keys` in the same transaction, after the payment row lock, so a
retry replays the first response instead of failing with 412.

## Web

`apps/web/src/modules/receivables/`: the AR workspace (aging by currency and
bucket, payments waiting, active disputes), open items (with dispute and promise
actions), payments, record payment, the payment page with match candidates and
the apply plan; lockbox imports and the import form; the collections worklist;
disputes (list, open, detail with review and resolve); promises to pay (list,
record); dunning runs (list, propose, detail with the letter preview, exclusions
and review, issue, cancel); interest notes (list, calculate and post);
collection cases (list, open, detail with assign and close); balance
confirmations (runs, new run, run detail with reply recording, counts and the
difference list); down payments and deposits (list, request, detail with
receive, cancel, clear or offset, release, and the clearings).

## Dependencies

Go only: the platform kernel, chi, pgx/v5, shopspring/decimal. No new libraries.
