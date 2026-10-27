# inventory

Passage inventory service. Wave 16 (PSG-16), slice 1: the immutable inventory
ledger, the dimension hash, the on-hand projection, inventory journals
(MOVEMENT, ADJUSTMENT, OPENING_BALANCE) with synchronous posting, the negative
stock policy, serial uniqueness, the journal list and the on-hand inquiry grouped
by any dimension subset. The web screens live in `apps/web/src/modules/inventory`.

Requirements source: `docs/prd/phase-3-procure-to-pay.md` section 3.16.
What is not in this slice is listed in `NOT-IMPLEMENTED.md`.

## Layout

```
cmd/inventory/            entrypoint: config, connect, migrate, router, admin port, serve
internal/domain/          journal rules, valuation, status model, dimensions, on-hand buckets, FEFO (pure, no I/O)
internal/store/           pgx repositories: journals, posting, on-hand projection (RLS-aware)
internal/api/             HTTP handlers, DTOs, validation, Problem responses
internal/events/          event type constants and payloads
migrations/               0001_inventory_ledger.sql (embedded, applied by dbx.Migrate)
api/openapi.yaml          OpenAPI 3.1 for every endpoint (parity is tested)
events/asyncapi.yaml      AsyncAPI 3 for every event published (payload parity is tested)
charts/inventory/         Helm chart (Deployment, Service, HPA, ServiceMonitor toggle)
```

## Endpoints and duties

| Method | Path | Duty | PRD duty |
|---|---|---|---|
| POST | `/v1/inventory/journals` | `inventory.journal.write` | InventoryJournalMaintain |
| GET | `/v1/inventory/journals` | `inventory.read` | InventoryInquire |
| GET | `/v1/inventory/journals/{id}` | `inventory.read` | InventoryInquire |
| POST | `/v1/inventory/journals/{id}/lines:bulk` | `inventory.journal.write` | InventoryJournalMaintain |
| POST | `/v1/inventory/journals/{id}:post` | `inventory.journal.post` | InventoryJournalPost |
| GET | `/v1/inventory/onhand` | `inventory.read` | InventoryInquire |

Duty names follow the repository's `<context>.<entity>.<action>` convention; the
right-hand column is the PRD 3.16.17 name each one stands for.

## Posting model

`POST /v1/inventory/journals/{id}:post` runs in one Postgres transaction:

1. Load the journal `FOR UPDATE`; a POSTED journal is returned unchanged (the journal
   id is the idempotency key); otherwise the version must match `If-Match`.
2. Validate the header and every line (site mandatory, serial ±1, precision).
3. Plan each line: receipts (qty > 0) post at `PURCHASED`, issues (qty < 0) at `SOLD`.
4. Take an advisory lock per received serial, then lock the on-hand rows in ascending
   (product, dimension set hash) order (`SELECT ... FOR UPDATE`, BR-16.4).
5. Value each line under the lock: receipts at qty x cost price, issues at the row's
   running average (posted value / posted quantity); the issue that empties a row takes
   its whole value, so no rounding residue stays. Every value is rounded to the
   currency's minor unit (BR-16.5) and the same amount goes to the transaction, the
   projection and the GL entry.
6. Apply the deltas and check the negative policy against `avail_physical` /
   `avail_ordered`: a posting that leaves either negative and lower than before is
   rejected with `INV-NEG-001` / `INV-NEG-002` (a receipt into an already-negative row is
   accepted). A row valued in another currency rejects the posting (`INV-CUR-001`).
7. Reject a received serial that is now in physical stock more than once in the legal
   entity (`INV-SER-001`, BR-16.9).
8. Write `inventory_transaction` and `inventory_transaction_origin` rows (append-only,
   enforced by trigger), build the balanced GL entry (inventory account against the
   MOVEMENT offset account, or the adjustment / opening clearing account) and store it
   on the journal.
9. Enqueue `transaction.posted` (per line), `onhand.changed` (per row) and
   `journal.posted` into the outbox in the same transaction.

The on-hand primary key is (tenant, legal entity, product, dimension set), so legal
entities that reuse dimension values never share stock.

## Run

```
export DATABASE_URL=postgres://passage_app:...@localhost:5432/passage_inventory
export MIGRATE_ON_START=true              # dev only
export INV_INVENTORY_ACCOUNT=1300 INV_ADJUSTMENT_ACCOUNT=5100 INV_OPENING_CLEARING_ACCOUNT=3900
go run ./cmd/inventory
```

Optional: `INV_NEGATIVE_DEFAULT` (`forbid`, default, or `allow`), `INV_JOURNAL_MAX_LINES`
(1000 to 50000, default 10000), `HTTP_ADDR` (`:8080`), `ADMIN_ADDR` (`:9090`).
Configuration is validated at startup; the process exits non-zero when a required
variable is missing. The HTTP port serves the API and `/healthz`; the admin port serves
`/readyz` (checks Postgres) and `/metrics`.

## Test

```
go vet ./...
go test ./...                              # domain, API and contract tests run without a database
PASSAGE_TEST_DATABASE_URL=postgres://owner@.../db \
PASSAGE_TEST_APP_DATABASE_URL=postgres://passage_app@.../db go test ./...   # store tests under RLS
```

Store tests use `platform/go/testkit` (`DBWithApp`): migrations run as the owner and every
query runs as `passage_app` (NOSUPERUSER, NOBYPASSRLS), so row-level security is exercised.
They skip without `PASSAGE_TEST_DATABASE_URL`. Domain property tests use `pgregory.net/rapid`.

## Error codes

| Code | Status | Meaning |
|---|---|---|
| `COM-0001` | 400 | Validation failure (field errors in `errors[]`), malformed body or cursor, or an invalid line rule |
| `COM-0428` | 428 | `If-Match` (journal version) missing on `:post` |
| `COM-0500` | 500 | Unexpected error (detail hidden) |
| `AUTH-0001` | 401 | Missing or malformed tenant on the request |
| `AUTH-0003` | 403 | Missing duty, or a legal entity outside the caller's scope |
| `INV-JNL-NOT-FOUND` | 404 | Journal id not found in the tenant / legal-entity scope |
| `INV-JNL-001` | 422 | Journal exceeds the maximum number of lines |
| `INV-JNL-002` | 422 | Movement journal requires an offset account (store and posting check; the create endpoint reports it earlier as a `COM-0001` field error on `offsetAccountId`) |
| `INV-JNL-003` | 409 | Journal is already posting (reserved for the Temporal workflow) |
| `INV-JNL-004` | 409 | Journal is not in a state that allows the change (lines on a non-DRAFT journal, posting from another status) |
| `INV-NEG-001` | 422 | Issue would make available physical stock negative (policy forbids it); detail names product, dimensions and shortfall |
| `INV-NEG-002` | 422 | Issue would make financial (ordered) stock negative |
| `INV-SER-001` | 422 | Serial already in physical stock in the legal entity |
| `INV-SER-002` | 422 | Serial-tracked line quantity is not ±1 |
| `INV-DIM-001` | 422 | Mandatory dimension (site) missing |
| `INV-UOM-002` | 422 | Quantity has more decimals than the inventory unit allows (6) |
| `INV-CUR-001` | 422 | Posting currency differs from the currency the on-hand row is valued in |
| `conflict_version` | 412 | `If-Match` does not match the journal version |
| `idempotency_key_reuse` | 422 | Idempotency-Key (or a line `clientKey`) reused with a different body |

Problems are RFC 9457 `application/problem+json` with `type` =
`https://passage.dev/problems/<code>`.

## Wave reference

- Wave 16 / PSG-16 slice 1. Dependencies: wave 6 (sites, warehouses, UoM), wave 8
  (products, dimension groups, item model groups), wave 11 (`Post(JournalEntry)`),
  wave 14 (period control), wave 4 (Temporal, outbox), wave 10 (migration framework).
- Slice 1 posts synchronously in the request. The Temporal `PostInventoryJournal`
  workflow, the wave 11 ledger call, and period control are later slices (see
  `NOT-IMPLEMENTED.md`).

## Dependencies

No dependency beyond the platform module and what it already pulls in (chi, pgx/v5,
shopspring/decimal, rapid for tests).
