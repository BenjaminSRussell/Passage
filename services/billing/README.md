# billing

Passage billing context: invoices (wave 29, PSG-29). Slice 1 delivers the free-text
invoice path end to end: create a proposal with per-line tax quoted by the tax
engine, post it (re-quote tax, gap-free `INV-` number, `invoice.posted` in the
outbox), read it back and list invoices. Slice 3 adds installment plans on the
proposal (FR-29.18) and credit notes against posted invoices (FR-29.5): whole or
by line quantity, checked against the remaining creditable amount (`BIL-0005`),
posted with a gap-free `CN-` number and `creditnote.posted`. Slice 5 builds invoices
from posted packing slips (FR-29.1): one per order or one consolidated invoice
(FR-29.2), each line keeping its packing slip, order line, product and price
explanation, a slip line invoiced once (BR-29.5), and the audit trace (FR-29.12). Slice 6
adds subscriptions (FR-29.10): a plan price per billing period, plan changes and
cancellation, billed as prorated SUBSCRIPTION invoice proposals. The screens are in
`apps/web/src/modules/billing`. The wider wave is listed in `NOT-IMPLEMENTED.md`.

## Layout

```
cmd/billing/            entrypoint: dbx.Connect -> dbx.Migrate (optional) -> httpx router -> Serve
internal/domain/        pure rules: line validation, totals (per-line rounding), tax recheck,
                        reversal rule (BR-29.7), installment split (BR-29.11), proration (BR-29.8),
                        credit planning and the remaining-creditable check (credit.go, FR-29.5),
                        shipment proposal planning: per order, consolidation, line-limit split,
                        BIL-0006 (shipment.go)
internal/store/         Postgres persistence under RLS; post = one transaction
internal/api/           HTTP handlers, DTOs, validation, idempotency, duties, metrics;
                        tax.go holds the TaxEngine seam (rate table or fail-closed);
                        credit.go the credit-note handlers; shipment.go the proposals and trace
internal/events/        event types and payloads (invoice.posted, creditnote.posted)
migrations/             0001_billing_init.sql (invoice, invoice_line, number_sequence_counter, outbox)
                        0002_credit_notes_and_installments.sql (original reference, reason,
                        original line, invoice_installment)
                        0003_shipment_proposals.sql (proposal_batch, line source references,
                        unique packing slip line)
                        0008_subscriptions.sql (subscription, subscription_term, subscription_period)
api/openapi.yaml        OpenAPI 3.1 for the eight endpoints
events/asyncapi.yaml    AsyncAPI 3 for invoice.posted and creditnote.posted
charts/billing/         Helm chart (deployment, service with admin port, ServiceMonitor toggle)
```

## Endpoints

| Method | Path | Duty | Notes |
|---|---|---|---|
| POST | `/v1/billing/free-text-invoices` | `billing.invoice.maintain` (InvoiceMaintain) | `Idempotency-Key` required; 201 on create, 200 on replay; a legal-entity-scoped caller may only invoice its own entity; optional `installmentTerms` |
| POST | `/v1/billing/proposals` | `billing.invoice.maintain` (InvoiceMaintain) | `Idempotency-Key` names the request; posted packing slips with their lines (one invoice account, currency and terms); one SHIPMENT invoice per order, or `consolidate: true` for one invoice (CONSOLIDATED when it spans orders); split at slip boundaries above `BILLING_MAX_LINES`; stored atomically (201 `{items}`), a replay returns every invoice (200) |
| GET | `/v1/billing/invoices/{id}/trace` | `billing.invoice.view` (InvoiceView) | invoice → packing slips → order lines → price explanation reference; a credit note resolves through its original |
| GET | `/v1/billing/invoices` | `billing.invoice.view` (InvoiceView) | headers only, newest first; `status`, `type`, `customerId`, `originalInvoiceId`, `limit` (1 to 200, default 100), `cursor` (the previous page's `nextCursor`) |
| GET | `/v1/billing/invoices/{id}` | `billing.invoice.view` (InvoiceView) | invoice or credit note; returns `ETag` (version); a creditable invoice carries `credited` and per-line `creditedQty` |
| POST | `/v1/billing/invoices/{id}:post` | `billing.invoice.post` (InvoicePost) | tax re-quote; idempotent by state; optional `If-Match`; refuses credit notes (409 `BIL-0101`) |
| POST | `/v1/billing/gl-postings` | `billing.invoice.post` | `{legalEntityId, retryParked?}`; books the invoice journal (FR-29.3) of every posted invoice and credit note without one: Dr receivable billed gross, Cr revenue per line account and Cr 2400 as the revrec allocation of the order's contract splits the lines (invoice lines, receivable and tax keep the contractual price; a credit note books the mirror in its original's proportions), Cr charges, Cr tax; an invoice with a line on a deferred revenue account waits (`BIL-0207`, backoff) until revrec (`REVREC_URL`) answers with the contract, and is parked (`BIL-0208`) when the allocated amounts do not add up to the billed nets; ledger key `billing-invoice-<id>` / `billing-credit-note-<id>`, entry id in `invoice.journal_posting_id`; ledger, accounts and amounts come from `BILLING_GL_PROFILES`, the stored document and revrec; a ledger refusal is recorded (attempts, next attempt with backoff) and the document parked after 5 refusals, an outage without a verdict never parks; at most 200 per call, `more` when due documents remain |
| POST | `/v1/billing/credit-notes` | `billing.invoice.maintain` (InvoiceMaintain) | `Idempotency-Key` required; whole remainder or `lines` by quantity; tax is the original's pro rata, never re-quoted |
| POST | `/v1/billing/subscriptions` | `billing.invoice.maintain` | `Idempotency-Key` required; plan code and price per period, `periodMonths` 1, 3, 6 or 12, start date; 201, or 200 replay |
| GET | `/v1/billing/subscriptions/{id}` | `billing.invoice.view` | plan terms, status, `billedThrough` (end of the last invoiced period); `ETag` = version |
| POST | `/v1/billing/subscriptions/{id}:change-plan` | `billing.invoice.maintain` | new price from `effectiveFrom`, after the current plan began and after the last invoiced period (`BIL-0107`); optional `If-Match` |
| POST | `/v1/billing/subscriptions/{id}:cancel` | `billing.invoice.maintain` | `lastServiceDay`, not inside an invoiced period (`BIL-0107`); once (`BIL-0108`) |
| POST | `/v1/billing/subscriptions/{id}:bill` | `billing.invoice.maintain` | `{periodFrom}` (first of a month); one line per plan term served in the period, prorated by calendar days; tax quoted; idempotent per period (the hash covers the invoice date only); overlap `BIL-0106`; a later period must start the day after the latest invoiced one ends or end the day before the earliest one starts (400 `COM-0001`); nothing to bill `BIL-0105` |
| POST | `/v1/billing/credit-notes/{id}:post` | `billing.creditnote.post` (CreditNotePost) | re-checks `BIL-0005` under the original's lock; `CN-` number; idempotent by state; optional `If-Match` |

Duties are enforced with `platform/authz` (401 without a principal, 403 `AUTH-0003`
without the duty). `/readyz` pings the database; `/metrics` is served on `ADMIN_ADDR`.

The browser calls these under `/api/v1/billing/...`; the BFF rewrites to `/v1`.

## Run

```
DATABASE_URL=postgres://... MIGRATE_ON_START=true HTTP_ADDR=:8080 go run ./cmd/billing
```

Configuration: `DATABASE_URL` (required), `MIGRATE_ON_START` (dev only),
`HTTP_ADDR` (default `:8080`), `ADMIN_ADDR` (default `:9090`, `/metrics`),
`BILLING_MAX_LINES` (default 2000, 1 to 10000), `BILLING_TAX_RATES` (interim tax
engine, for example `DE19=19,DE7=7,EXEMPT=0`).

Until the wave-13 tax client is wired, the tax engine is the rate table in
`BILLING_TAX_RATES`: line tax = rounded line net × the code's percent, rounded per
line. With the setting empty the engine is `api.UnavailableTax`: every proposal and
post fails closed with `BIL-0001` and nothing is booked with silent zero tax. A tax
code the table does not list is refused with `BIL-0102`.

## Test

```
go vet ./... && go test ./...
PASSAGE_TEST_DATABASE_URL=postgres://... go test ./...   # runs the store tests
```

Domain tests are table-driven plus a rapid property test (AC-29.1: gross equals the
sum of line nets, charges and tax, on the minor unit, for USD, EUR and JPY), and two
§5.29.9 fixtures whose expected amounts come from exact rational Python references
under `internal/domain/testdata/` (no shared code with the engine):
`invoices-random-1k.json` (1,000 invoices in nine currencies with 0, 2, 3 and 4
decimals, half-unit ties included; `TestComputeTotals_InvoicesRandom1k`) and
`subscription-edges-25.json` (proration around mid-period start and cancel, the leap
day, annual and quarterly periods; `TestProrate_SubscriptionEdges25` for `Prorate`, and
`TestSubscription_SubscriptionEdges25_ThroughInvoices`, which creates each case as a
subscription, cancels it and bills the period through the API against Postgres). Regenerate
them with `python3 gen_*.py` in that directory. API
tests run through the platform router (tenant headers, duties). Store tests use
`platform/testkit` and skip without the database; the row-level-security test also
needs `PASSAGE_TEST_APP_DATABASE_URL` (a non-superuser role) and skips without it.

## Error codes

| Code | HTTP | Meaning |
|---|---|---|
| `COM-0001` | 400 | Validation failed (see `errors` per field) |
| `AUTH-0001` | 401 | Missing or malformed tenant context |
| `AUTH-0003` | 403 | Missing duty, or a legal-entity-scoped caller invoicing another entity |
| `BIL-0001` | 503 | Tax engine unavailable; nothing stored or posted |
| `BIL-0002` | 409 | Tax moved beyond the 0.01 tolerance since the proposal |
| `BIL-0006` | 422 | Invoice date precedes a packing slip's date (BR-29.4) |
| `BIL-0007` | 422 | One packing slip alone holds more lines than `BILLING_MAX_LINES`; larger groups are split automatically |
| `BIL-0005` | 422 | Credit exceeds the remaining creditable amount (per line quantity, net, tax, charges and gross), or the original is not a posted invoice |
| `BIL-0004` | 400 | A line has no revenue account; reported as `COM-0001` with the field error `lines[n].revenueAccount` ending "(BIL-0004)" |
| `BIL-0008` | 422 | Reversal not allowed (BR-29.7); domain rule, reversal not yet exposed over HTTP |
| `BIL-0011` | 422 | Installment percentages total other than 100% |
| `BIL-0100` | 404 | Invoice not found |
| `BIL-0101` | 409 | Not a proposal, or a credit note sent to the invoice post (or the reverse) |
| `BIL-0102` | 422 | A line's tax code is not known to the tax engine; nothing stored or posted |
| `BIL-0103` | 412 | `If-Match` names a version the invoice no longer has |
| `BIL-0104` | 409 | A packing slip line of the request is already on an invoice or proposal (BR-29.5); nothing of the request is stored |
| `BIL-0105` | 422 | The subscription has no billable amount in the period (no zero invoice is issued) |
| `BIL-0106` | 409 | The billing period overlaps one already invoiced |
| `BIL-0200` | 503 | The general ledger is not configured (`LEDGER_URL`) |
| `BIL-0201` | 502 | The general ledger did not answer a journal posting (`errors.booked` lists the documents booked before) |
| `BIL-0202` | 422 | No GL posting profile for the legal entity |
| `BIL-0203` | 422 | The profile's ledger belongs to another legal entity |
| `BIL-0204` | n/a | A document whose lines, charges and tax do not add to its gross is parked, never posted |
| `BIL-0205` | n/a | The ledger refused a journal without a code |
| `BIL-0206` | n/a | A credit note is parked because its original invoice is parked; it books after the original does |
| `BIL-0207` | n/a | An invoice with a deferred line is not booked: revrec has no unique contract for its order or did not answer; retried with backoff, never parked for it |
| `BIL-0208` | n/a | A document is parked: the revrec allocation does not add up to the billed nets (no contract asset or liability account is named in the PRD) or does not cover a deferred line |
| `BIL-0107` | 422 | A plan change or cancellation falls inside an invoiced period; correct it with a credit note |
| `BIL-0108` | 422 | The subscription is cancelled (cancel again, or change plan) |
| `BIL-0109` | 503 | Subscriptions are not available in this deployment |
| `idempotency_key_reuse` | 422 | Same `Idempotency-Key` with a different body |
| `idempotency_in_progress` | 409 | A concurrent request holds the key |

## Notes and deviations

- `domain.TaxRecheckTolerance()` returns 0.01 (parameter `tax_recheck_tolerance`);
  it moves to legal-entity configuration later.
- `BIL-0100` to `BIL-0104` are service codes beyond the PRD catalog (§5.29.18).
- Shipment proposals take the packing slip lines from the caller until the
  `packing-slip.posted` consumer exists: fulfillment and sales own those rows, and
  billing keeps only their ids. A slip line is unique per tenant across all invoices
  and proposals (`invoice_line_slip_line_unique`); credit notes do not copy the
  references, so a credit never frees or re-takes a slip line.
- The invoice number is `INV-<posting year>-<7 digits>`, drawn from a serialized
  counter row per (tenant, entity, year) inside the posting transaction, so a
  failed or refused post consumes no number (BR-29.9).
- Credit notes store positive amounts; the type `CREDIT_NOTE` carries the sign.
  The credit-note number series is `CN-<posting year>-<7 digits>`, separate from
  `INV-`. A credit counts against its original once posted; a proposal does not,
  so two proposals may each fit but only the first to post succeeds.
- The installment plan is sent with the proposal until the wave-7 payment terms
  client exists; the invoice due date is the last installment's due date.
- The PRD's `passage.billing.credit-note.posted.v1` is published as
  `dev.passage.billing.creditnote.posted.v1` (the platform type grammar has no hyphens).
- `journal_posting_id` holds the ledger entry of the document's invoice journal (FR-29.3)
  once `POST /v1/billing/gl-postings` booked it; the event carries null because the journal
  follows the post. `BILLING_GL_PROFILES` (a JSON array, one profile per entity: `legalEntityId`, `ledgerId`,
  `receivableAccount`, `taxAccount`, `chargeAccount`, `deferredRevenueAccounts`; chart value
  `glProfiles`) is required when `LEDGER_URL` is set; the deferred account is 2400. `REVREC_URL` (chart value `revrecUrl`) names the revrec service whose
  contract allocation splits the invoice journal; the call is signed with the `revrec.allocation.read` grant. The
  profile's ledger is checked against its entity at each call. `invoice.posted` carries `intercompany=false` until wave 40.
- Dependencies: pgx, chi, shopspring/decimal, rapid (all in the platform module set).
