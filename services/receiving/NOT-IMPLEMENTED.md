# Not implemented in PSG-18 (state after slices through PSG-1806 slice 6a)

Delivered: domain model (receipt state machine, registration with over-delivery and serial checks, receipt posting, GR/IR open items), matching engine, invoice posting with period gate and pro-rata GR/IR clearing, `POST /v1/receiving/matching:evaluate`, migrations `0001` to `0004` with RLS, chart, Dockerfile. Since slice 1: the Postgres receipt store, an in-memory fake store, `POST /v1/receiving/arrivals`, the `receipt.posted` outbox write in the store, the ledger and period-control HTTP adapters, and store-level idempotency primitives with the API idempotency wrapper.

| Deliverable or AC | Status | Reason |
|---|---|---|
| Receipt store (`internal/store/receipts.go`): `CreateArrival`, `DirectReceipt`, `Register`, `Post`, `GetReceipt`, `ListReceipts`, `RecordedSerials`, per-tenant `RCV-%08d` numbering, `INV-SER-001` and `RCV-DUP-001` mapping | built (PRs #110, #117, #118) | migrations `0002` and `0003`; tenant-scoped via `dbx.WithTenantTx`; Postgres tests run only with `PASSAGE_TEST_DATABASE_URL` |
| `Post` writes GR/IR open items and the `dev.passage.receiving.receipt.posted.v1` outbox row in one transaction | built (PR #117) | `outbox.Enqueue` in `internal/store/receipts.go`; no HTTP endpoint calls `Post` yet, so nothing is emitted in a running service |
| GR/IR inquiry store (`ListGrir`, keyset paging, per-currency open totals) | built at store level (PR #118) | migration `0004`; no HTTP endpoint yet |
| Idempotency primitives (`ClaimKey`, `CompleteKey`, `ReleaseKey`) and the API wrapper (request hash, 422 `COM-0409`, 409 `COM-0410` with `Retry-After`, replay header) | built (PRs #118, #127) | wrapper applied to arrivals only |
| In-memory `store.Store` fake with Postgres parity test (`internal/store/memstore`) | built (PR #125) | test support; not to be imported by `cmd/receiving` |
| `POST /v1/receiving/arrivals` (duty `ReceivingMaintain`, field-level 400, legal-entity 403, 201 with `Location`) | built (PR #127) | in `api/openapi.yaml`; metric `receiving_receipts_arrived_total` |
| Registration and direct-receipt endpoint (`POST /v1/receiving/receipts`) | open | store `Register` and `DirectReceipt` exist; no handler or route; `receiving_receipts_registered_total` is declared with no caller |
| Receipt posting, `:correct`, `:cancel` endpoints | open | store `Post` exists; correct and cancel have neither store nor handler |
| Receipt and GR/IR inquiry endpoints (`GET`) | open | store `GetReceipt`, `ListReceipts`, `ListGrir` exist; no handlers |
| Invoice posting store and endpoint | open | `domain.PostInvoice` exists; no persistence (no invoice tables in the migrations), no route |
| `events/asyncapi.yaml` | open | the `receipt.posted` event is written to the outbox by the store but not documented; there is no `events/` directory |
| Real `LedgerPoster` adapter (`internal/ledger`) | wired (#96) | HTTP client for `POST /v1/ledger/journal-entries:post` with `Idempotency-Key`, 3 s timeout, typed `ErrPeriodClosed` on `422 period_closed`; built from `LEDGER_URL` in `cmd/receiving` and held by `api.New`. No HTTP route posts a vendor invoice yet, so `API.PostInvoice` is the only caller |
| Real `PeriodControl` adapter (`internal/periods`) | wired (#96) | calls `services/close` `POST /v1/close/period-statuses:check` (source `ap`, 2 s timeout) from `CLOSE_URL`; empty URLs fall back to fakes only with `PASSAGE_ALLOW_FAKES=1`. No service maps a posting date to the `fiscalPeriodId` UUID close requires (org periods have no id), so the caller supplies `LedgerID` and `FiscalPeriodID` on `VendorInvoice` |
| Deliverable 2: vendor invoice capture, OCR adapter, UBL/Peppol | open; Peppol is wave 35 | OCR and e-invoice access points are undecided in the architecture; payables-side API belongs with wave 19 `services/payables/` |
| Deliverable 3: exception workflow (Temporal `InvoiceMatchException`) | open | no Temporal in the tree; the engine returns the variance statuses the workflow starts from |
| Deliverable 4: GR/IR reconciliation report and daily job | open | needs the ledger balance read; the GR/IR store and inquiry exist |
| Deliverable 5: evaluated receipt settlement | open | needs the `receipt.posted` consumer and the invoice store; `consumer_inbox` table exists, no consumer |
| Deliverable 6: service entry sheets | open | depends on procurement service-line data not in the tree |
| Deliverable 7: stochastic invoice audit block | open | not started |
| Deliverable 8: consignment, invoicing plan, subsequent debit/credit, one-time payee | open | needs wave 16 ownership events; one-time payee needs wave 2 field encryption (no PII is accepted or logged) |
| Credit notes (BR-18.12) | open | `PostInvoice` rejects non-positive quantities |
| Web module `apps/web/src/modules/receiving/` | open | not started |
| PO lines, received quantities from procurement and inventory | blocked on PSG-17 and wave 16 | no procurement service exists, so no `POLines` port is defined; evaluate takes the lines from the request body, and the port and its HTTP adapter belong with PSG-17 |
| `serviceCode` in error bodies; `passage.legal_entity_ids` and `passage.actor` GUCs | handoff to platform kernel | `platform/go/httpx` and `dbx` are not editable here; the RLS policy uses the `passage.legal_entity_scopes` GUC the kernel sets |
| Makefile image target | handoff | the root Makefile is outside wave 18 ownership |
| Argo CD Application `deploy/envs/dev/apps/receiving.yaml` (chart `services/receiving/charts/receiving`) | built (dev) | placeholder zero digest; no CI job builds or signs a receiving image (`ci.yml` covers only hello-ledger), so that handoff is open, as are stage registration and live sync; it deploys into the `hello-ledger` namespace, which is temporary until a shared data namespace exists |
| Database `passage_receiving` and `receiving-db` Secret | database built, Secret open | `postgres.yaml` creates `passage_receiving` (idempotently, owned by the superuser role; `passage_app` gets CONNECT, CREATE and schema `public` ALL, enough for the migrations since it owns the tables it creates) and admits the receiving pod; the `receiving-db` Secret (`DATABASE_URL=postgres://passage_app:<APP_PASSWORD>@postgres:5432/passage_receiving?sslmode=disable`) is created outside Git in namespace `hello-ledger`, as for `hello-ledger-db` |
| JWT validation | wave 2 | the service trusts gateway headers by platform design |
