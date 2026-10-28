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
| `LEDGER_URL` | base URL of the ledger service (`POST /v1/ledger/journal-entries`, 3 s timeout); required |
| `CLOSE_URL` | base URL of `services/close` (`POST /v1/close/period-statuses:check`, 2 s timeout, source system `ap`); required |
| `PURCHASING_URL` | base URL of `services/purchasing` (`GET /v1/procurement/purchase-orders/{id}`, 2 s timeout); the caller's own tenant, legal entity, user and duties are forwarded, so the caller needs `PurchaseOrderInquire`; required |
| `POSTING_LEDGER_ID` | UUID of the ledger that receipt journals post to (`ledgerId` in the ledger's entry request); invoices carry their own `ledgerId`; required |
| `POSTING_ACCOUNT_INVENTORY`, `POSTING_ACCOUNT_GRIR`, `POSTING_ACCOUNT_PPV`, `POSTING_ACCOUNT_VENDOR_CONTROL` | ledger account codes for receipt and invoice journals (posting profile); all four required |
| `PASSAGE_ALLOW_FAKES` | `1` lets an empty `LEDGER_URL`, `CLOSE_URL`, `PURCHASING_URL` or posting account fall back to the in-memory fakes with a warning log; local development only, any other value fails start-up |
| `HTTP_ADDR` | default `:8080` |
| `ADMIN_ADDR` | default `:9090`, serves `/metrics` only |

## API

| Route | Duty |
|---|---|
| `POST /v1/receiving/matching:evaluate` | `MatchingPolicyMaintain` |
| `POST /v1/receiving/arrivals`, `POST /v1/receiving/receipts` (direct receipt), `POST /v1/receiving/receipts/{id}:register`, `GET /v1/receiving/receipts`, `GET /v1/receiving/receipts/{id}` | `ReceivingMaintain` |
| `POST /v1/receiving/receipts/{id}:post`, `:correct`, `:cancel` | `ReceivingPost` |
| `GET /v1/receiving/grir` | `APInquire` |
| `GET /v1/receiving/pending-postings`, `POST /v1/receiving/pending-postings/{key}/resolve` | `ReceivingAdmin` |
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
| RCV-NF-001 / RCV-NF-002 / RCV-NF-003 | 404 | receipt not found / receipt line has no open GR/IR item / pending posting not found |
| RCV-LDG-001 | 422 | the ledger refused the reversal made by a resolve; not retryable |
| COM-0503 | 503 | the ledger could not be reached while resolving a pending posting |
| COM-0412 | 412 | `If-Match` differs from the receipt version |
| COM-0428 | 428 | `If-Match` missing |

## Pending ledger postings (runbook)

Each ledger post is first recorded in `receiving_pending_posting` (one row per ledger key) with the subjects it touches in `receiving_pending_posting_subject`: the receipt for a receipt command; the invoice id and every referenced receipt for an invoice. The row is marked done in the same transaction that applies the effect. While a row is open, any other ledger-posting command on one of its subjects, or the same key with a different body, returns 409 `RCV-IDM-001`; the problem's `errors.pendingKey` and `errors.createdAt` name the row. Repeating the original request completes it.

A row stays open when the ledger call ended in an unknown outcome (timeout, transport error, 5xx, 408, or a 409 that is not a conflict) or the local commit failed after the ledger accepted the entry. Any other 4xx from the ledger, and a closed period, release the row at once.

When the original request cannot be repeated (the client is gone, the data changed):
1. `GET /v1/receiving/pending-postings` (duty `ReceivingAdmin`) lists open rows, oldest first.
2. `POST /v1/receiving/pending-postings/{key}/resolve` (duty `ReceivingAdmin`, `Idempotency-Key` required) re-sends the stored journal under its key. If the ledger returns an entry id it is reversed through `POST /v1/ledger/journal-entries/{id}/reverse` and the row becomes `REVERSED`, which frees the receipt and invoice; a definite rejection makes it `REJECTED`. Resolve first claims the row (`claimed_at`), so a second resolve gets 409 `RCV-IDM-001`, and the original command's commit then fails with 409 `RCV-IDM-001` instead of recording a posting that was reversed. A row younger than the ledger timeout plus the request timeout (33 s) is refused with 409 `RCV-IDM-001` because its command may still be running. A reversal into a closed period answers 422 `PER-AP-001`, any other ledger refusal 422 `RCV-LDG-001` (the reversal is dated like the original posting and carries no reason code, so a ledger that requires one refuses it). It answers 503 `COM-0503` when the ledger cannot be reached or the reversal failed; the claim is released, so call it again. A claim is a lease of 2 x ledger timeout + request timeout (36 s): a resolve process that dies mid-way leaves the row claimed only until the lease ends, after which the next resolve takes it over. Releasing and finishing run detached from the client connection, so a disconnect cannot strand a claim.
3. The user repeats the command. Its ledger key carries a generation suffix (`:r1`, `:r2`) so it does not replay the reversed entry.

`ReceivingAdmin` is not in `policies/authz/data.json` yet. Alerting: the gauge `receiving_pending_postings_oldest_age_seconds` (and `receiving_pending_postings_open`) is kept per process, so it is lost on restart and each replica only sees the postings it started. With `replicas` above 1 the gauge needs a DB-derived source (the gauge handler has no tenant context, and the application role sees one tenant at a time, so it cannot be built from `ListPending`); until then rely on the query below. The `ReceivingPendingPostingStuck` PrometheusRule fires above 900 s. After a crash use the database as the source of truth:
`SELECT tenant_id, ledger_key, subject, created_at FROM receiving_pending_posting WHERE done_at IS NULL AND created_at < now() - interval '15 minutes';` (run as the table owner, because the application role sees one tenant at a time).

## Metrics

`receiving_matches_evaluated_total`, `receiving_evaluate_rejected_total`, `receiving_receipts_arrived_total`, `receiving_receipts_registered_total`, `receiving_receipts_posted_total`, `receiving_receipts_adjusted_total`, `receiving_invoices_posted_total`, `receiving_pending_postings_open`, `receiving_pending_postings_oldest_age_seconds`, `receiving_http_requests_total`, `receiving_http_request_duration_seconds`. No Prometheus client dependency.

## Needs

No npm or platform change. Tracing: the kernel has no OpenTelemetry package (ADR-0001), so `traceId` is the request id.
