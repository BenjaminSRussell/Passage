# Not implemented in PSG-18 slice 1

Delivered: domain model (receipt state machine, registration with over-delivery and serial checks, receipt posting, GR/IR open items), matching engine, invoice posting with period gate and pro-rata GR/IR clearing, `POST /v1/receiving/matching:evaluate`, migration with RLS, chart, Dockerfile.

| Deliverable or AC | Status | Reason |
|---|---|---|
| Deliverable 1: HTTP, persistence and events for arrival, registration, receipt posting, `:correct`, `:cancel` | slice 2 of PSG-18 | line budget; the domain logic and tables exist, the store, handlers and `dev.passage.receiving.receipt.posted.v1` outbox write do not. `RCV-DUP-001` is enforced at rest by `unique (tenant_id, vendor_id, packing_slip)` only |
| Deliverable 2: vendor invoice capture, OCR adapter, UBL/Peppol | slice 2 of PSG-18; Peppol is wave 35 | OCR and e-invoice access points are undecided in the architecture; payables-side API belongs with wave 19 `services/payables/` |
| Deliverable 3: exception workflow (Temporal `InvoiceMatchException`) | slice 2 of PSG-18 | no Temporal in the tree; the engine returns the variance statuses the workflow starts from |
| Deliverable 4: GR/IR reconciliation report and daily job | slice 2 of PSG-18 | needs the store and the ledger balance read; `receiving_grir_open_item` is created |
| Deliverable 5: evaluated receipt settlement | slice 2 of PSG-18 | needs the `receipt.posted` consumer and the invoice store |
| Deliverable 6: service entry sheets | slice 2 of PSG-18 | line budget; depends on procurement service-line data not in the tree |
| Deliverable 7: stochastic invoice audit block | slice 2 of PSG-18 | line budget |
| Deliverable 8: consignment, invoicing plan, subsequent debit/credit, one-time payee | slice 2 of PSG-18 | needs wave 16 ownership events; one-time payee needs wave 2 field encryption (no PII is accepted or logged in this slice) |
| Credit notes (BR-18.12) | slice 2 of PSG-18 | `PostInvoice` rejects non-positive quantities |
| Web module `apps/web/src/modules/receiving/` | slice 2 of PSG-18 | line budget |
| `events/asyncapi.yaml` | slice 2 of PSG-18 | no event is emitted yet |
| Real `PeriodControl` and `LedgerPoster` adapters | blocked on ledger waves | the contract to satisfy is `POST /v1/ledger/journal-entries:post` with `Idempotency-Key`, 3 s timeout, `422 period_closed`; in-package fakes stand in |
| PO lines, received quantities from procurement and inventory | blocked on PSG-17 and wave 16 | evaluate takes them from the request body |
| `serviceCode` in error bodies; `passage.legal_entity_ids` and `passage.actor` GUCs | handoff to platform kernel | `platform/go/httpx` and `dbx` are not editable here; the RLS policy uses the `passage.legal_entity_scopes` GUC the kernel sets |
| Makefile image target | handoff | the root Makefile is outside wave 18 ownership |
| Argo CD Application `deploy/envs/<env>/apps/receiving.yaml` (chart `services/receiving/charts/receiving`, image digest from CI) | handoff to deploy | only hello-ledger is registered under `deploy/envs/*/apps/`; no other merged service (payables, party, org, ...) has an Application, so a platform-wide registration pattern is undecided and is not invented here |
| Database `passage_receiving`, role and `receiving-db` Secret (key consumed by the chart's `databaseSecret`) | handoff to deploy | `deploy/envs/dev/apps/postgres.yaml` provisions only the hello-ledger database today; per-service databases and secrets are not yet a platform pattern |
| JWT validation | wave 2 | the service trusts gateway headers by platform design |
