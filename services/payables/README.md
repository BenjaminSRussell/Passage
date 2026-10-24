# payables

Wave 19 deployable: accounts payable settlement and payments. This slice ships the settlement engine and a stateless preview endpoint. One Go module, one image (`payables`). Deferred deliverables are listed in `NOT-IMPLEMENTED.md`.

## Run and test

```
cd services/payables
go vet ./... && go test ./...
DATABASE_URL=postgres://... go run ./cmd/payables
```

## Configuration

| Variable | Meaning |
|---|---|
| `DATABASE_URL` | required; Postgres URL, from a Secret in the cluster |
| `HTTP_ADDR` | default `:8080` |
| `ADMIN_ADDR` | default `:9090`, serves `/metrics` only |

## API

`POST /v1/payables/settlements:preview` (duty `settlement.read`), `GET /healthz`, `GET /readyz` (database ping, 503 when down), `GET /metrics` on the admin port. Contract: `api/openapi.yaml`.

The preview settles one credit against open invoices per PRD FR-19.2: priority order, cash discount (pro-rated on partial settlement), realized FX gain or loss, write-off within the limit, and a balanced journal. Amounts, rates and percentages are decimal strings; rates come from the request until the wave 12 rate lookup exists. A cross-currency credit settles in the accounting currency, so its FX difference is zero by construction.

## Error codes

| Code | Status | Meaning |
|---|---|---|
| PAY-0001 | 400 | invalid field: date, decimal string, currency, discount, duplicate or empty invoices |
| PAY-0002 | 422 | rate missing for a foreign currency, or not 1 for the accounting currency |
| COM-0001 | 400 | malformed body, unknown field or wrong JSON type |
| COM-0500 | 500 | internal error, detail hidden |
| AUTH-0001 | 401 | missing or malformed tenant (kernel header middleware) |
| AUTH-0003 | 403 | caller lacks the duty (kernel authz) |

Problem bodies from this service carry `code` (a stable slug such as `validation_failed`) and `serviceCode` (the table above).

## Metrics

`payables_settlement_previews_total`, `payables_settlement_preview_failures_total`, `payables_http_requests_total`. No Prometheus client dependency: counters are stdlib atomics rendered as text.
