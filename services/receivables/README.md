# passage-receivables

Accounts receivable for Passage, wave 30 (PSG-30), slices 1 to 5. Slice 1 is
the cash-application core: open items, aging, manual payments, the scored
matcher (BR-30.1) and settlement validation (BR-30.4). Slice 2 adds BAI2 lockbox
import (FR-30.3) and collections: the risk-ranked worklist (FR-30.6, FR-30.10),
disputes and promises to pay (FR-30.9), dunning runs (FR-30.7, BR-30.2) and
interest notes (FR-30.8, BR-30.5). Slice 3 adds minimal collection cases
(FR-30.18), customer balance confirmations (FR-30.20, BR-30.13) and down
payments and security deposits (FR-30.21, BR-30.14). Slice 4 adds camt.053 bank
statement import (FR-30.5, BR-30.7) and the deductions workbench (FR-30.17,
BR-30.12). Slice 5 adds the payer-identity matcher rules R4, R5 and R7
(BR-30.1), customer statements (FR-30.13, BR-30.10) and the aging snapshot job
with `customerbalance.changed.v1` (§5.30.16, FR-30.10). Everything else in the wave is listed in
[NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md).

## What it does now

- **Open items** (`GET /v1/receivables/open-items`): customer and status filters, page limit up to 500.
- **Aging** (`GET /v1/receivables/aging?asOf=`): totals per currency and bucket (`CURRENT`, `1-30`, `31-60`, `61-90`, `91-180`, `OVER-180`), with due date as the basis.
- **Payments**: record a manual payment (`POST /payments`, idempotent; optional `payerAccount`, the IBAN or MICR account, of which only the hex SHA-256 of the normalized value is stored as `payerAccountHash`, and `shortPayReason`), list them (`GET /payments?status=&customer=&limit=`), read one (`GET /payments/{id}`, ETag = version), score candidate settlements (`POST /payments/{id}:match`, changes nothing) and apply (`POST /payments/{id}:apply`, requires `If-Match`; an optional `Idempotency-Key` replays the first response).
- **Apply rules**: settlements plus on-account residual equal the unapplied amount; every item is in the payment's legal entity and currency (no cross-currency settlement yet) and belongs to the payment's customer; an unidentified payment takes the customer of the items it settles; a cash discount needs the value date on or before the discount date and may not exceed the item's discount; write-off tolerance is the larger of 5.00 and 0.5% of the original item amount.
- **Lockbox** (`POST /lockbox/imports`, duty receivables.payment-file.import; `GET /lockbox/imports`): a BAI2 file (records 01, 02, 03, 16 with S/V/D funds-type detail, 88, 49, 98, 99; trailer counts checked; first error named by line, AR-0004). Each 16 detail with a check credit type code (default 116, Item in Lockbox Deposit; a bank profile may name other credit codes 100-399) becomes one unidentified CHECK payment with source LOCKBOX, the customer reference as remittance, and `payment.received.v1`. The same file content twice is refused by SHA-256 (AR-0003); a check re-sent in another file is refused by (source, bank reference).
- **Opening-item import** (`POST /open-items:batch`, service duty `receivables.import.write` held by the migration loader; `GET /open-item-imports`, receivables.view; `GET /open-items?importSource=`): loads a cutover snapshot of a legal entity's open items from a legacy system, up to 1000 rows per request. Rows are keyed by (`importSource`, `legacyKey`), so a rerun updates an untouched item to the snapshot instead of duplicating it and refuses one already settled, partly settled, cleared or deducted in Passage (`AR-OPENING-SETTLED`) or with an active dispute or holding promise (`AR-OPENING-IN-COLLECTION`); a loaded key cannot change legal entity, customer, currency, kind or side (`AR-OPENING-IDENTITY`). Invoices are debits; credit notes and unapplied payments are credits, stored with negative amounts, netted against the debits in aging, statements, the worklist and the balance snapshot (a customer can be net in credit), and kept out of matching, dunning and settlement. Results are per row; `atomic: true` stores nothing unless every row is accepted. Publishes `openitem.imported.v1` once per batch. The principal grant, authz seed and gateway route are handoffs (NOT-IMPLEMENTED.md).
- **Bank statements** (`POST /bank-statements/imports`, duty receivables.payment-file.import; `GET /bank-statements/imports`): an ISO 20022 camt.053 file (.001.02 to .001.08; the first malformed entry named by line, AR-0004). Each booked (`Sts` BOOK or absent), non-reversal CRDT entry becomes one unidentified WIRE payment with source CAMT053, `source_ref` = statement `Id` : `NtryRef` (or `AcctSvcrRef`), the debtor name as payer, the structured creditor reference (else the first unstructured line) as remittance, the value date (else the booking date), and `payment.received.v1`. Debits, pending entries and reversals are counted as skipped. The same file twice is refused by SHA-256 (AR-0003); an entry already imported from an overlapping statement is counted as a duplicate and not created again (BR-30.7). The clerk matches each payment with `:match`, as for lockbox payments.
- **Deductions** (`POST /deductions`, `GET /deductions?status=&type=&customer=`, `GET /deductions/{id}`, `POST /deductions/{id}:match`, `:approve`, `:deny`, `:split`, `:write-off`; duty receivables.deduction.maintain to change, receivables.view to read; every move needs `If-Match`): a deduction moves the unexplained part of a short payment off the short-paid item (locked NOWAIT; open, undisputed, at least the amount) with a type (PROMO, PRICE, SHORT_SHIP, DAMAGE, UNKNOWN) and the customer's reason, so it is aged in the workbench apart from invoices and never dunned (FR-30.17). OPEN → MATCHED (to a rebate accrual, promotion fund or chargeback claim; the whole deduction must fit the claim's remaining amount, AR-0014) → APPROVED; DENIED (with NOT_AUTHORIZED, EXPIRED or DUPLICATE) from OPEN or MATCHED re-bills the amount as a DEDUCTION debit open item due today; SPLIT from OPEN creates 2 to 20 OPEN children that sum to it; WRITTEN_OFF from OPEN or MATCHED within `deduction_write_off_limit` (50.00 USD, 45.00 EUR, zero elsewhere) or above it with receivables.write-off.approve (AR-0006), publishing Dr 6110 / Cr 1200 (BR-30.12). A deduction still being worked counts in the customer's balance-confirmation balance; an approved or written-off one does not.
- **Customer statements** (`GET /statements?legalEntityId=&customer=&asOf=`, duty receivables.view): the customer's documents open at the end of the statement date (default today) by currency, ordered by document date, with running and closing balances and an aging footer over the debits (FR-30.13). It uses the balance confirmation's as-of basis: each item less the settlements, down-payment clearings and deductions booked by then; a deduction still being worked is its own DEDUCTION line without a due date; an unapplied payment is a negative PAYMENT line. It reads only (BR-30.10).
- **Aging snapshots** (`POST /aging-snapshots`, duty receivables.aging.run; `GET /aging-snapshots?legalEntityId=&asOf=&customer=`, receivables.view): the nightly aging and risk score job for one legal entity. It stores one row per customer and currency for the key date (key = (customer, date); a re-run rewrites them; a future date is refused because the balances are those at run time) and publishes `customerbalance.changed.v1` when the balance, overdue amount, dunning level or risk score differs from the latest earlier snapshot. The balance is net of credit-side items (credits clear the oldest debits first), so a customer holding only credits, or more credit than debit, gets a negative row. A customer whose items all settled gets a zero row and one event. The list reads a date's rows (the latest by default), riskiest first.
- **Worklist** (`GET /worklist?legalEntityId=&asOf=`): one row per customer and currency that has an open debit, with total open (net of the customer's credits, which clear the oldest debits first), overdue, oldest days, open-amount-weighted days late, dunning level, open disputes, broken promises and a 0-100 risk score (overdue share 40, days late 20, dunning level 20, disputes 15, broken promises 5), riskiest first. The open amounts are summed in SQL, so the read is complete for any number of items.
- **Disputes** (`POST /disputes`, `GET /disputes?status=active|<status>&customer=&openItem=`, `GET /disputes/{id}`, `POST /disputes/{id}:review`, `:resolve`; duty receivables.collection.maintain to change): one active dispute per item, amount at most the open amount, SLA 120 hours. OPEN → UNDER_REVIEW → RESOLVED_CREDIT | RESOLVED_PAID | RESOLVED_WRITE_OFF, REJECTED from OPEN or UNDER_REVIEW (BR-30.6). While active the item carries `dispute_id` and is left out of dunning and interest.
- **Promises to pay** (`POST /promises`, `GET /promises`): amount at most the open amount, date today or later; one holding promise per item. A promise holds until its date plus 2 days (BR-30.2); after that it counts as broken on the worklist and the item is dunnable again.
- **Dunning** (`POST /dunning-runs`, `GET /dunning-runs`, `GET /dunning-runs/{id}`, `POST /dunning-runs/{id}:review`, `:issue`, `:cancel`; duty receivables.dunning.run): a run proposes letters as DRAFT per customer and currency. Level 1 once the grace days (default 5) pass, 2 at 15 days, 3 at 30 (fee 25.00 USD / 20.00 EUR), 4 at 45 (fee 50.00 / 40.00); an item rises one level per run and never falls; letter level is the highest item level; items under 1.00 are ignored; blocked, disputed and promised items, letters under 50.00 USD / 45.00 EUR, and customers dunned fewer than 10 days ago are left out with a reason. Issue needs REVIEWED (AR-0007), re-checks every letter under row locks (a letter whose items were paid, disputed, promised or raised since the proposal is SKIPPED), raises item levels, posts the fee as a FEE open item and publishes one `dunningletter.issued.v1` per letter.
- **Interest** (`POST /interest-notes:calculate` preview, `:post`, `GET /interest-notes`; duty receivables.interest.post): open amount × annual rate × days ÷ 365 (or 360), from due date + grace or from the item's last note, so no day is charged twice; disputed and promised items, INTEREST and FEE items, and amounts under 5.00 are left out. Post writes one INTEREST open item and one note per item and calendar month, serialised per legal entity by an advisory lock; `Idempotency-Key` replays the stored response.
- **Collection cases** (`POST /cases`, `GET /cases?status=&category=&customer=`, `GET /cases/{id}`, `POST /cases/{id}:assign`, `:close`; duty receivables.collection.maintain to change, receivables.view to read): category COLLECTION, DISPUTE or DEDUCTION with its SLA (72, 120, 240 hours), owner, priority, reason, an optional linked open item (same customer and legal entity; the case takes its currency) and an attached amount. `slaBreached` is computed on read (AR-0018). Assign and close need `If-Match`; a closed case cannot change.
- **Balance confirmations** (`POST /balance-confirmations`, `GET /balance-confirmations`, `GET /balance-confirmations/{id}`, `GET /balance-confirmations/{id}/differences`, `POST /balance-confirmations/{id}:reply`; duty receivables.balance-confirmation.run to run and record, receivables.view to read): a run selects each customer and currency whose balance on the key date reaches the threshold (default 10,000.00, debit or credit), zero balances only with `includeZeroBalances`, optionally a sample of n ranked by a hash seeded with the `Idempotency-Key`. The balance on the key date is every item documented by then less the settlements and down payment clearings made by then, so later payments are added back. A reply is AGREED or DISAGREED (with the customer's balance); it is immutable, a repeat of the recorded reply returns it, and a DISAGREED reply opens a 120-hour DISPUTE case with reason BALANCE_DIFFERENCE and the customer's balance attached, in the same transaction (BR-30.13). The difference list holds every DISAGREED row with both balances, the difference and the case.
- **Down payments and security deposits** (`POST /down-payments`, `GET /down-payments?customer=&status=&kind=`, `GET /down-payments/{id}`, `POST /down-payments/{id}:receive`, `:cancel` (receivables.payment.post), `:clear` (receivables.payment.settle), `:release` (receivables.payment.refund); all moves need `If-Match`): a request posts nothing; receipt records the received date and publishes Dr bank / Cr 2190 (down payment) or 2195 (deposit). `:clear` locks the item (NOWAIT), clears min(remaining, item open) or a smaller given amount (more is 422 AR-0017), lowers the item's open amount, records a clearing row and publishes Dr 2190 or 2195 / Cr 1200 (AC-30.15). A down payment clears invoices only; a deposit may offset invoice, fee and interest items. `:release` refunds what remains of a deposit (Dr 2195 / Cr bank). An unpaid request can be cancelled.
- **RMA credit notes** (`POST /rma-credits`, service duty `ar.rmacredit.post`, granted only to the sales service): sales posts the AC-30.5 credit after an RMA is dispositioned. Customer and currency come from the original invoice's open item and the ledger from `RECEIVABLES_RMA_LEDGERS` (JSON, legal entity id to ledger id); the return value (gross) may not exceed the invoice amount less the gross of earlier RMA credits on it (not its open amount: a paid or SETTLED invoice is returnable, and its credit leaves a credit balance). Optional `taxLines` (code, amount > 0) reverse the original invoice tax: Dr the account pinned in `RECEIVABLES_RMA_TAX_ACCOUNTS` (JSON, legal entity id to tax code to account), recorded as negative output transactions with the tax service (`TAX_URL`, duty `tax.transaction.record`), and the gross plus tax is bounded by the invoice amount. `invoiceId` (the billing invoice) is required with `taxLines`: receivables reads that invoice from billing (`BILLING_URL`, read-only duty `billing.invoice.read`) and refuses a tax code the invoice never billed or more tax per code than billed less billing credit notes and earlier RMA credits (422 `AR-RMA-INVOICE`). Records an `rma_credit` row (PENDING), then posts Dr 4000 gross / Dr tax / Cr 4200 restocking / Cr 1200 net plus tax under key `rma-credit:{legalEntityId}:{document}` after that commit, and creates the CREDIT_NOTE open item (negative net plus tax, `CN-{rmaNumber}`) in the transaction that marks the credit POSTED, so AR never carries a credit without its 1200 entry. A ledger outage backs off (1 min doubling to 1 h); a ledger refusal parks the credit. A repeat returns the stored values. Publishes `creditnote.posted.v1`. With `BILLING_URL` set, `invoiceLines` (line number, quantity) and `invoiceId` are required: before anything is stored the credit reserves those lines in billing (duty `billing.credit-reservation.write`, key `rma-credit:{legalEntityId}:{rmaId}`), so billing credit notes and RMA credits share one bound per line. Billing refusing the room is 422 `AR-RMA-INVOICE` with nothing stored; billing unavailable is 503 `AR-RMA-GL` with nothing stored or posted. After the credit is POSTED the reservation is committed; a failed commit leaves the credit POSTED (`reservationCommitted` false) and a repeat of the call or the sweep commits it. Receivables releases a reservation only for a PENDING credit (parked by the ledger or the tax service, or refused after reserving), never for a POSTED one; `:retry` reserves again with the same key and is refused 422 when the room is gone. Recovery after a crash: a reservation with no stored credit is adopted by the repeat of that RMA (same key); a PENDING credit is posted by the sweep; a POSTED credit with an uncommitted reservation is committed by the sweep (`GET /rma-credits?status=POSTED` shows `reservationCommitted` false until then).
- **RMA credit operations** (`GET /rma-credits?legalEntityId=&status=&parked=` receivables.view; `POST /rma-credits/{id}:retry` and `POST /rma-credits:sweep` receivables.payment.post): the operator lists PENDING and parked credits and retries one, optionally with a new `postingDate` (only a parked credit can be re-dated; the ledger key stays the same, so an entry it already holds answers a conflict instead of posting twice). The sweeper posts due PENDING credits: in process for the tenants in `RECEIVABLES_RMA_SWEEP_TENANT_IDS` every `RECEIVABLES_RMA_SWEEP_INTERVAL` (default 5m), per legal entity under a principal scoped to it, or by a scheduler calling `:sweep`. A credit becomes due 2 minutes after it was stored and when its backoff ran out; parked credits wait for `:retry`.
- **Events** via the transactional outbox: `dev.passage.receivables.payment.received.v1`, `payment.applied.v1`, `dispute.opened.v1`, `dispute.resolved.v1`, `dunningletter.issued.v1` (the PRD's `dunning-letter.issued.v1`; the kernel type rule allows no hyphen), `case.opened.v1`, `case.closed.v1`, `confirmation.replied.v1`, `downpayment.received.v1`, `.cleared.v1`, `.released.v1` (one `downpayment` entity for both kinds; the postings ride on the event until the ledger client exists), `deduction.opened.v1` and `deduction.resolved.v1` (APPROVED, DENIED, WRITTEN_OFF with its postings, or SPLIT with the child ids), and `customerbalance.changed.v1` (the PRD's `customer-balance.changed.v1`) from the aging snapshot job, and `creditnote.posted.v1` from an RMA credit. See `events/asyncapi.yaml`.

The API contract is `api/openapi.yaml`. Every operation names its duty in
`x-passage-duty`.

## Layout

```
cmd/receivables/       entrypoint: dbx.Connect -> (dbx.Migrate) -> httpx router -> Serve
internal/domain/       pure rules: match.go (BR-30.1), settle.go (BR-30.4), aging.go (FR-30.6),
                       bai2.go (FR-30.3), dunning.go (BR-30.2), interest.go (BR-30.5),
                       collections.go (FR-30.9, BR-30.6), risk.go and worklist.go (FR-30.10),
                       cases.go (FR-30.18), confirmations.go (FR-30.20, BR-30.13),
                       down_payments.go (FR-30.21, BR-30.14), camt053.go (FR-30.5),
                       deduction.go (FR-30.17, BR-30.12)
internal/store/        pgx repositories, RLS via dbx.WithTenantTx, outbox writes
internal/api/          HTTP handlers, DTOs, validation
internal/events/       event types and payloads
migrations/            0001 tables and RLS, 0002 outbox, 0003 idempotency_keys,
                       0004 dispute, promise_to_pay, dunning_run, dunning_letter, interest_note,
                       0005 lockbox_batch, 0006 collection_case, balance_confirmation_run,
                       balance_confirmation, 0007 down_payment, down_payment_clearing,
                       0008 bank_statement_import, deduction
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

Acceptance fixtures live in `testdata/` and load through `internal/fixtures`:
`cash-app-500` (AC-30.1; regenerate with `go test ./internal/fixtures -update`,
which must leave the files unchanged) and `dunning-60` (AC-30.3; letters and
exclusions written by hand from BR-30.2).

## Matching rules implemented

- R1 document number in the remittance as a whole token, so `INV-1001` does not match `INV-10012` (+60), R2 single item equal to the amount (+30) or a subset of up to 5 items (+25), R3 amount equals open minus available cash discount (+28), R6 value date within 7 days of due (+5).
- Auto-apply when the top set is the only set scoring ≥ 85 and the runner-up is ≤ winner − 20; 60–84 is suggested; below 60 goes on account. A set made only of the top set's items (one quoted invoice of a multi-invoice payment) is part of the same settlement and is neither a rival nor the runner-up.
- Refinement pending product confirmation: the top set auto-applies only when the payment's amount explains it (R2, an exact subset, R3 or R7). A reference with payer identity alone (R1, R4, R5) stays SUGGESTED, so a known payer quoting the wrong invoice is never settled against the quoted one. The same narrowing stops a known payer's reference-only partial payment or overpayment from auto-applying.
- Multi-item sets are searched among the items the remittance quotes first, then by due date, up to 12 items; a match reads at most 500 open items, the quoted ones first, so an unidentified payer's invoices are never cut off.
- R4 payer account hash bound to the item's customer (+15). Bindings are learned when a payment carrying the hash is applied to a customer; camt.053 payments carry the hash of the debtor's IBAN or other account id, manual payments the hash of `payerAccount`.
- R5 payer name Jaro-Winkler ≥ 0.92 to a name the customer paid under before (+10), compared after upper-casing and dropping punctuation; names are learned on apply like R4.
- R7 short of one item by less than 1% or less than 50.00 USD / 45.00 EUR, only with a short-pay reason (+20); not scored when R3 already explains the shortfall as the cash discount.
- R4 and R5 score a subset only when its items share one customer, and a subset spanning customers is never a candidate (apply would refuse it).

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
| `AR-0003` | 409 | Lockbox or bank statement file already imported (same SHA-256) |
| `AR-0004` | 400 | BAI2 or camt.053 file malformed; `detail` names the line |
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
| `AR-OPENING-INVALID`, `-DUPLICATE-KEY`, `-IDENTITY`, `-SETTLED`, `-IN-COLLECTION`, `-DOCUMENT-EXISTS`, `-REJECTED` | per row | Opening-item import row refusals, reported in the row's `code` (200, or 422 for an atomic batch) |
| `AR-CREDIT-ITEM` | 409 | The open item is a credit (credit note or unapplied payment); this operation settles debits only |
| `AR-0017` | 422 | Clearing exceeds the remaining down payment or the item's open amount |
| `AR-DEDUCTION-NOT-FOUND` | 404 | Deduction id unknown or unknown action |
| `AR-DEDUCTION-STATE` | 409 | The deduction's status does not allow the move, or the item is not open |
| `AR-DEDUCTION-MISMATCH` | 400 | The item is another legal entity's, or the payment is another legal entity's, currency's or customer's |
| `AR-DEDUCTION-SPLIT` | 422 | Fewer than two parts, a part not above zero, or parts not summing to the deduction |
| `AR-0014` | 422 | The deduction exceeds the claim's remaining amount; split or deny the remainder |
| `AR-0006` | 403 | (deductions) Write-off above `deduction_write_off_limit` without receivables.write-off.approve |
| `AR-RMA-CREDIT` | 422 | RMA credit amounts are negative, net is not above 0, or gross is not restocking plus net (`errors.amounts`) |
| `AR-RMA-INVOICE` | 422 | The original invoice is not an open item of the legal entity, the return value exceeds the invoice amount less earlier RMA credits, or billing has no room left on the invoice lines (credit notes and other RMA credits already cover them) |
| `AR-RMA-GL` | 503 | The ledger or the legal entity's RMA ledger is not configured, or the ledger is unavailable (the credit stays PENDING and backs off) |
| `AR-RMA-PARKED` | 409 | The ledger refused the credit's journal; it is parked until `:retry` |
| `AR-RMA-STATE` | 409 | `:retry` of a POSTED credit, re-dating a credit that is not parked, or the `CN-` document number is already an open item |
| `AR-RMA-NOT-FOUND` | 404 | RMA credit not found |

`Idempotency-Key` is enforced on create by storing the key on the payment row
(unique per tenant). On apply the key and the response are stored in
`idempotency_keys` in the same transaction, after the payment row lock, so a
retry replays the first response instead of failing with 412.

## Web

`apps/web/src/modules/receivables/`: the AR workspace (aging by currency and
bucket, payments waiting, active disputes), open items (with dispute, promise and
deduction actions), payments, record payment, the payment page with match candidates and
the apply plan; lockbox imports and the import form; bank statement imports and
the camt.053 import form with the payments it created; the collections worklist;
disputes (list, open, detail with review and resolve); promises to pay (list,
record); dunning runs (list, propose, detail with the letter preview, exclusions
and review, issue, cancel); interest notes (list, calculate and post);
collection cases (list, open, detail with assign and close); balance
confirmations (runs, new run, run detail with reply recording, counts and the
difference list); down payments and deposits (list, request, detail with
receive, cancel, clear or offset, release, and the clearings); the deductions
workbench (list with status and type filters and the age of each, record from
an open item, detail with match, approve, deny and re-bill, split, write-off
with the approval limit, and the parts of a split).

## Dependencies

Go only: the platform kernel, chi, pgx/v5, shopspring/decimal. No new libraries.
