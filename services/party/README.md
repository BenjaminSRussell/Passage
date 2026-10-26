# party

Global party and business partner service (PSG-7). This service delivers the Fellegi-Sunter duplicate engine, tax registration format validation, the party core API (create, read, update with events) and the deployable skeleton. See `NOT-IMPLEMENTED.md` for everything else.

## Run

```
DATABASE_URL=postgres://... MIGRATE_ON_START=true HTTP_ADDR=:8080 ADMIN_ADDR=:9090 go run ./cmd/party
```

`DATABASE_URL` is required. `MIGRATE_ON_START=true` applies the embedded migrations in `migrations/` at startup. `/readyz` pings the database and returns 503 `{"status":"unavailable"}` when it cannot.

`/healthz`, `/readyz` and `/v1/party/...` are served on `HTTP_ADDR`; Prometheus `/metrics` on `ADMIN_ADDR`. Tenant headers are trusted behind the gateway; JWT validation arrives with wave 2.

## Test

```
go vet ./... && go test ./...
make lint-charts
```

Database tests (`internal/store`, the serve test in `cmd/party`) are skipped unless `PASSAGE_TEST_DATABASE_URL` is set. The RLS tests also need `PASSAGE_TEST_APP_DATABASE_URL`, a connection as the non-bypass `passage_app` role, and fail without it. The database needs the `pg_trgm` extension.

## Party core

`POST /v1/party/parties` creates an organization or person; the number comes from numbering sequence `PARTY`, called with the request's `Idempotency-Key` (or a generated one) after the replay check. A replay with the same key and body returns the original party with 201 and allocates no second number; the same key with another body is 409 `idempotency_key_reuse`. `GET /v1/party/parties/{id}` follows `merged_into_id` for at most 5 hops. `PATCH /v1/party/parties/{id}` needs `If-Match: "<version>"` or a body `version`, moves status only between `active` and `inactive`, and writes nothing for a no-op. Every commit enqueues `dev.passage.party.party.created.v1` or `updated.v1` in the same transaction; see `events/asyncapi.yaml`. No event carries `dob`, `gender` or a government id, and `changed_fields` never names `dob` or `gender`.

## Merge

`store.Merge(ctx, MergeRequest{WinnerID, LoserID, Rules})` runs in one tenant transaction: survivorship per field (`keepWinner` default, `keepLoser`, `newest`, `nonEmpty`; an empty preferred value falls back to the other side), the loser's customer roles moved to the winner, `invoice_account_party_id` references rewritten, the loser marked `merged` with `merged_into_id`, a `party_merge` record, and one `dev.passage.party.party.merged.v1` outbox row. Both parties must share a type and be unmerged; a role in the same legal entity on both sides returns `ErrRoleConflict` and changes nothing. The principal must not be legal-entity scoped.

`store.Audit(ctx)` returns an `IntegrityReport`: roles owned by merged parties, references to merged parties, merged parties without a merge record. `Clean()` is true after any successful merge.

## Duties

| Duty | Grants |
|---|---|
| `party.write` | `POST /v1/party/tax-registrations:validate`, `POST /v1/party/parties`, `PATCH /v1/party/parties/{id}` |
| `party.read` | `GET /v1/party/parties/{id}` |
| `pii.read` | shows `dob` unmasked on party responses; without it `dob` reads `"••••"` when set |

## Error codes

| Code | Status | Meaning |
|---|---|---|
| `COM-0001` | 400 | malformed body, unknown field, wrong type, malformed legal entity |
| `COM-0500` | 500 | internal error, detail hidden |
| `AUTH-0001` | 401 | missing or malformed tenant |
| `AUTH-0003` | 403 | missing duty |
| `PTY-0001` | 422 | tax registration failed format validation; `errors` names the field |
| `PTY-PARTY-INVALID` | 422 | party body failed validation, or `partyType` changed; `errors` names the field |
| `PTY-PARTY-NOT-FOUND` | 404 | unknown party id, another tenant's id, or a malformed id |
| `PTY-VERSION-REQUIRED` | 428 | PATCH without a usable `If-Match` or body `version` |
| `PTY-VERSION-CONFLICT` | 409 | PATCH with a stale version (error class `conflict_version`) |
| `PTY-STATUS-TRANSITION` | 422 | status other than `active` or `inactive`, or the party is merged |
| `PTY-NUMBERING-UNAVAILABLE` | 503 | numbering failed, timed out or `NUMBERING_URL` is empty; no party is written |
| `idempotency_key_reuse` | 409 | the `Idempotency-Key` was used with a different body |

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `DATABASE_URL` | required | Postgres connection string; the chart reads it from the `existingSecret` secret (`party-db`) |
| `MIGRATE_ON_START` | `false` | `true` applies `migrations/*.sql` at startup |
| `HTTP_ADDR`, `ADMIN_ADDR` | `:8080`, `:9090` | listen addresses |
| `NUMBERING_URL` | empty | numbering service base URL; party create allocates sequence `PARTY` through it with a 3 s timeout. When empty the service starts and create returns 503 `PTY-NUMBERING-UNAVAILABLE` |
| `PARTY_DEDUP_LIKELY_WEIGHT` | `12` | likely-duplicate weight; read by the next slice |
| `PARTY_DEDUP_POSSIBLE_WEIGHT` | `6` | possible-duplicate weight; read by the next slice |
| `PARTY_VIES_TIMEOUT_MS` | `5000` | tax checker timeout; read by the tax slice |

No new Go dependency: `pgx` and `chi` were already required, and the database helpers come from `platform/go/dbx`.

## Metrics

`party_tax_validations_total{status}`, `party_tax_validations_rejected_total`, `party_tax_checker_failures_total`, `party_parties_created_total` (new parties only; an idempotent replay is not counted).

VAT numbers are format-checked only and return `unverified`; this slice has no VIES client.
