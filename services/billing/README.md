# billing

Passage billing context: invoices (wave 29, PSG-29). Slice 1 delivers the free-text
invoice path end to end: create a proposal with per-line tax quoted by the tax
engine, post it (re-quote tax, gap-free `INV-` number, `invoice.posted` in the
outbox), read it back and list invoices. The screens are in
`apps/web/src/modules/billing`. The wider wave is listed in `NOT-IMPLEMENTED.md`.

## Layout

```
cmd/billing/            entrypoint: dbx.Connect -> dbx.Migrate (optional) -> httpx router -> Serve
internal/domain/        pure rules: line validation, totals (per-line rounding), tax recheck,
                        reversal rule (BR-29.7), installment split (BR-29.11), proration (BR-29.8)
internal/store/         Postgres persistence under RLS; post = one transaction
internal/api/           HTTP handlers, DTOs, validation, idempotency, duties, metrics;
                        tax.go holds the TaxEngine seam (rate table or fail-closed)
internal/events/        event type and payload (dev.passage.billing.invoice.posted.v1)
migrations/             0001_billing_init.sql (invoice, invoice_line, number_sequence_counter, outbox)
api/openapi.yaml        OpenAPI 3.1 for the four endpoints
events/asyncapi.yaml    AsyncAPI 3 for invoice.posted
charts/billing/         Helm chart (deployment, service with admin port, ServiceMonitor toggle)
```

## Endpoints

| Method | Path | Duty | Notes |
|---|---|---|---|
| POST | `/v1/billing/free-text-invoices` | `billing.invoice.maintain` (InvoiceMaintain) | `Idempotency-Key` required; 201 on create, 200 on replay; a legal-entity-scoped caller may only invoice its own entity |
| GET | `/v1/billing/invoices` | `billing.invoice.view` (InvoiceView) | headers only, newest first; `status`, `customerId`, `limit` (1 to 200, default 100) |
| GET | `/v1/billing/invoices/{id}` | `billing.invoice.view` (InvoiceView) | returns `ETag` (version) |
| POST | `/v1/billing/invoices/{id}:post` | `billing.invoice.post` (InvoicePost) | tax re-quote; idempotent by state; optional `If-Match` |

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
sum of line nets, charges and tax, on the minor unit, for USD, EUR and JPY). API
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
| `BIL-0004` | 400 | A line has no revenue account; reported as `COM-0001` with the field error `lines[n].revenueAccount` ending "(BIL-0004)" |
| `BIL-0008` | 422 | Reversal not allowed (BR-29.7); domain rule, reversal not yet exposed over HTTP |
| `BIL-0011` | 422 | Installment percentages invalid; domain rule, not yet exposed over HTTP |
| `BIL-0100` | 404 | Invoice not found |
| `BIL-0101` | 409 | Invoice is not a proposal and cannot be posted |
| `BIL-0102` | 422 | A line's tax code is not known to the tax engine; nothing stored or posted |
| `BIL-0103` | 412 | `If-Match` names a version the invoice no longer has |
| `idempotency_key_reuse` | 422 | Same `Idempotency-Key` with a different body |
| `idempotency_in_progress` | 409 | A concurrent request holds the key |

## Notes and deviations

- `domain.TaxRecheckTolerance()` returns 0.01 (parameter `tax_recheck_tolerance`);
  it moves to legal-entity configuration later.
- `BIL-0100` to `BIL-0103` are service codes beyond the PRD catalog (§5.29.18).
- The invoice number is `INV-<posting year>-<7 digits>`, drawn from a serialized
  counter row per (tenant, entity, year) inside the posting transaction, so a
  failed or refused post consumes no number (BR-29.9).
- The posting journal (`journal_posting_id`) is null until the ledger.Post
  integration lands; `invoice.posted` carries `intercompany=false` until wave 40.
- Dependencies: pgx, chi, shopspring/decimal, rapid (all in the platform module set).
