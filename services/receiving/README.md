# receiving

Wave 18 deployable: receiving, product receipts and three-way match. One Go module, one image (`receiving`). Slice 1 delivers the pure domain and the policy tester endpoint; see `NOT-IMPLEMENTED.md` for the rest.

## Run and test

```
cd services/receiving
go vet ./... && go test ./...
DATABASE_URL=postgres://... MIGRATE_ON_START=true go run ./cmd/receiving
```

Store tests run only when `PASSAGE_TEST_DATABASE_URL` is set, against a role without BYPASSRLS.

## Configuration

| Variable | Meaning |
|---|---|
| `DATABASE_URL` | required; Postgres URL, from a Secret in the cluster |
| `MIGRATE_ON_START` | `true` applies embedded migrations at start (dev only) |
| `HTTP_ADDR` | default `:8080` |
| `ADMIN_ADDR` | default `:9090`, serves `/metrics` only |

## API

`POST /v1/receiving/matching:evaluate` (duty `MatchingPolicyMaintain`), `GET /healthz`, `GET /readyz` (database ping within 500 ms, 503 when down), `GET /metrics` on the admin port. Contract: `api/openapi.yaml`. The PRD lists the tester under `/payables/`; wave 19 owns that service, so it is hosted here and moving it is a path rename.

## Behaviour notes

- Over-delivery and tolerance percentages are percent values (2.0 means 2 percent).
- Line currency differing from the PO currency yields `PRICE_VARIANCE` with `currencyMismatch: true`.
- The header status is the worst line status; `TOTAL_VARIANCE` outranks all line statuses. Postings are produced only when the header is `MATCHED`.
- GR/IR clearing is pro-rata at the receipt-side amount; the final invoiced quantity clears the exact remainder, so a fully invoiced line leaves 0.00.
- Posting idempotency keys: `post:receiving:productreceipt:<id>:receipt`, `post:receiving:vendorinvoice:<id>:invoice`.

## Error codes

| Code | Status | Meaning |
|---|---|---|
| COM-0001 | 400 | malformed body, unknown field, or a field failing validation (`errors` names it) |
| COM-0500 | 500 | internal error, detail hidden |
| AUTH-0001 | 401 | missing or malformed tenant |
| AUTH-0003 | 403 | missing duty |
| RCV-OVR-001 | domain | receipt quantity above the over-delivery allowance |
| INV-SER-001 | domain | duplicate or empty serial |
| RCV-VAL-001 | domain | invalid receipt or invoice input |
| RCV-STA-001 | domain | invalid receipt state transition |
| PER-AP-001 | domain | AP period not open for the posting date |
| AP-QTY-001 | domain | invoice quantity above received uninvoiced quantity |

## Metrics

`receiving_matches_evaluated_total`, `receiving_evaluate_rejected_total`, `receiving_http_requests_total`, `receiving_http_request_duration_seconds`. No Prometheus client dependency.

## Needs

No npm or platform change. Tracing: the kernel has no OpenTelemetry package (ADR-0001), so `traceId` is the request id.
