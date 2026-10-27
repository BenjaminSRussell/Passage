# receiving

Wave 18 deployable: receiving, product receipts and three-way match. One Go module, one image (`receiving`). Delivered so far: the pure domain, the policy tester endpoint, the Postgres receipt store, and the HTTP surface for arrivals, direct receipts, registration, posting, correction, cancellation, receipt and GR/IR inquiry and vendor invoice posting, with `receipt.posted`, `receipt.corrected` and `invoice.posted` written to the outbox in the same transaction (`events/asyncapi.yaml`). Deliverables 2 to 8 are open; see `NOT-IMPLEMENTED.md`.

## Run and test

```
cd services/receiving
go vet ./... && go test ./...
DATABASE_URL=postgres://... MIGRATE_ON_START=true go run ./cmd/receiving
```

Store tests run only when `PASSAGE_TEST_DATABASE_URL` is set, against a role without BYPASSRLS. API tests use `internal/store/memstore`, which a parity test checks against Postgres.

## Configuration

| Variable | Meaning |
|---|---|
| `DATABASE_URL` | required; Postgres URL, from a Secret in the cluster |
| `MIGRATE_ON_START` | `true` applies embedded migrations at start (dev only) |
| `LEDGER_URL` | base URL of the ledger service (`POST /v1/ledger/journal-entries:post`, 3 s timeout); required |
| `CLOSE_URL` | base URL of `services/close` (`POST /v1/close/period-statuses:check`, 2 s timeout, source system `ap`); required |
| `POSTING_ACCOUNT_INVENTORY`, `POSTING_ACCOUNT_GRIR`, `POSTING_ACCOUNT_PPV`, `POSTING_ACCOUNT_VENDOR_CONTROL` | ledger account codes for receipt and invoice journals (posting profile); all four required |
| `PASSAGE_ALLOW_FAKES` | `1` lets an empty `LEDGER_URL`, `CLOSE_URL` or posting account fall back to the in-memory fakes with a warning log; local development only, any other value fails start-up |
| `HTTP_ADDR` | default `:8080` |
| `ADMIN_ADDR` | default `:9090`, serves `/metrics` only |

## API

| Route | Duty |
|---|---|
| `POST /v1/receiving/matching:evaluate` | `MatchingPolicyMaintain` |
| `POST /v1/receiving/arrivals`, `POST /v1/receiving/receipts` (direct receipt), `POST /v1/receiving/receipts/{id}:register`, `GET /v1/receiving/receipts`, `GET /v1/receiving/receipts/{id}` | `ReceivingMaintain` |
| `POST /v1/receiving/receipts/{id}:post`, `:correct`, `:cancel` | `ReceivingPost` |
| `GET /v1/receiving/grir` | `APInquire` |
| `POST /v1/receiving/invoices:post` | `VendorInvoicePost` |

Every POST except the tester requires `Idempotency-Key`; replays carry `Idempotent-Replayed: true`. Commands on an existing receipt (`:register`, `:post`, `:correct`, `:cancel`) also require `If-Match` with the receipt `version` (428 when absent, 412 when stale); every receipt response carries `version` and an `ETag`. Lists page with an opaque `nextCursor`. Also: `GET /healthz`, `GET /readyz` (database ping within 500 ms, 503 when down), `GET /metrics` on the admin port. Contract: `api/openapi.yaml`. The PRD lists the tester under `/payables/`; wave 19 owns that service, so it is hosted here and moving it is a path rename.

## Behaviour notes

- Over-delivery and tolerance percentages are percent values (2.0 means 2 percent).
- Line currency differing from the PO currency yields `PRICE_VARIANCE` with `currencyMismatch: true`.
- The header status is the worst line status; `TOTAL_VARIANCE` outranks all line statuses. Postings are produced only when the header is `MATCHED`.
- GR/IR clearing is pro-rata at the receipt-side amount; the final invoiced quantity clears the exact remainder, so a fully invoiced line leaves 0.00.
- Receipt numbers are `RCV-%08d`, allocated per tenant; a failed arrival or direct receipt burns no number.
- Posting idempotency keys: `post:receiving:productreceipt:<id>:receipt`, `post:receiving:productreceipt:<id>:correction:<seq>`, `post:receiving:productreceipt:<id>:cancel`, `post:receiving:vendorinvoice:<id>:invoice`. The invoice id derives from tenant, vendor and invoice number, so a retry after a failed commit reuses the ledger key.
- Every ledger post is first recorded in `receiving_pending_posting` (key, journal, hash) in its own committed transaction and marked done in the main one. While a posting is unfinished, any other ledger-posting command for the same receipt or invoice, or the same key with a different body, returns RCV-IDM-001; repeating the original request completes it. A definite ledger rejection (closed period) releases the row. `LEDGER_IDEMPOTENCY_CONFLICT` from the ledger also maps to RCV-IDM-001.
- Money and quantities are accepted as JSON numbers or as decimal strings (`"2.50"`); strings are validated as plain decimals.
- `:correct` and `:cancel` take an optional `postingDate` (default the receipt date, never earlier). When a correction brings a line to its invoiced quantity the amount becomes the cleared amount, so no rounding residual is left on the closed GR/IR item.
- A correction changes quantities on a POSTED receipt and leaves it POSTED; it cannot go below the invoiced quantity or to zero, and serial-tracked lines are refused. Cancelling a POSTED receipt reverses the ledger entry and closes its GR/IR items at zero; a cancelled receipt keeps its packing slip.
- The invoice endpoint lives here until wave 19 owns payables; its event uses the `payables` context the PRD names.

## Error codes

| Code | Status | Meaning |
|---|---|---|
| COM-0001 | 400 | malformed body, unknown field, or a field failing validation (`errors` names it) |
| COM-0409 | 422 | idempotency key reused with a different request body |
| COM-0410 | 409 | request with this idempotency key is in flight (`Retry-After`) |
| RCV-DUP-001 | 409 | duplicate packing slip for the tenant and vendor |
| COM-0500 | 500 | internal error, detail hidden |
| AUTH-0001 | 401 | missing or malformed tenant |
| AUTH-0003 | 403 | missing duty |
| RCV-OVR-001 | domain | receipt quantity above the over-delivery allowance |
| INV-SER-001 | domain | duplicate or empty serial |
| RCV-VAL-001 | domain | invalid receipt or invoice input |
| RCV-STA-001 | domain | invalid receipt state transition |
| PER-AP-001 | domain | AP period not open for the posting date |
| AP-QTY-001 | domain | invoice quantity above received uninvoiced quantity |
| AP-DUP-001 | 409 | invoice number already posted for the vendor (case-insensitive) |
| RCV-COR-001 | 409 | correction would take a line below its invoiced quantity |
| RCV-IDM-001 | 409 | an earlier ledger posting for the receipt or invoice is unfinished (the ledger accepted it, the local commit did not) or differs; repeat the original request first |
| RCV-NF-001 / RCV-NF-002 | 404 | receipt not found / receipt line has no open GR/IR item |
| COM-0412 | 412 | `If-Match` differs from the receipt version |
| COM-0428 | 428 | `If-Match` missing |

## Metrics

`receiving_matches_evaluated_total`, `receiving_evaluate_rejected_total`, `receiving_receipts_arrived_total`, `receiving_receipts_registered_total`, `receiving_receipts_posted_total`, `receiving_receipts_adjusted_total`, `receiving_invoices_posted_total`, `receiving_http_requests_total`, `receiving_http_request_duration_seconds`. No Prometheus client dependency.

## Needs

No npm or platform change. Tracing: the kernel has no OpenTelemetry package (ADR-0001), so `traceId` is the request id.
