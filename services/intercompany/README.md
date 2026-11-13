# intercompany (wave 40, PSG-40, slices 1-4)

Intercompany trade, transfers and charges between legal entities of one
tenant: trading-partner rules, the due-to / due-from journal derivation, the
mirror-document saga state logic, the reconciliation classifier and
intercompany charge pricing and settlement, centralized payments and their
reversal, direct delivery chains, transfer-pricing evidence and the planning
group sequence (PRD 7.40).

## What slice 1 delivers

Pure domain logic plus two stateless HTTP previews and the web workspace.
Nothing is persisted and no event is published yet.

| Area | Code | Status |
|---|---|---|
| Due-to / due-from derivation, BR-40.4, AC-40.4, counterparty at the document rate (FR-40.5) | `internal/domain/journal.go` | built, unit and property tests |
| Journal preview `POST /v1/interco/journals:preview` (duty `intercompany.post`) | `internal/api` | built, tests |
| Reconciliation classifier, FR-40.6 / BR-40.5, 50 planted mismatches flagged and 50 clean pairs matched (AC-40.5 shape) | `internal/domain/recon.go` | built, unit tests (`TestClassify_PlantedMismatches`) |
| Reconciliation classify `POST /v1/interco/reconciliations:classify` (duty `intercompany.reconcile`) | `internal/api` | built, tests |
| Mirror saga state logic, FR-40.2 / BR-40.1 | `internal/domain/mirror.go` | built, property test (AC-40.1 / AC-40.2 shape, 1,000 random orders) |
| Partner policy validation, FR-40.1; tolerance BR-40.3 | `internal/domain/pairing.go` | built, unit tests |
| IC charge cost-plus pricing, FR-40.13 (AC-40.11 arithmetic) | `internal/domain/charge.go` | built, unit tests |
| Counters on `/metrics` (admin port) | `internal/api` | built, tests |
| Web workspace: `/intercompany`, `/intercompany/journal`, `/intercompany/reconciliation` | `apps/web/src/modules/intercompany` | built, vitest |

Browser calls go to `/api/v1/interco/...`; the web BFF rewrites them to the
`/v1/interco/...` paths above and sets `X-Passage-Tenant` and
`X-Passage-Duties`.

## What slice 2 adds

Pure rules for intercompany charges, centralized payments and planning
groups, each behind a stateless preview and a web page. Still nothing is
stored, posted or published.

| Area | Code | Status |
|---|---|---|
| Transfer price resolution by source transaction date, BR-40.12 (IC-0821; two rows effective on one date are rejected) | `internal/domain/charge_settlement.go` | built, unit tests |
| Transfer-priced charge pricing (Cost, CostPlus, Table) and Journal-mode legs in lender and borrower, FR-40.13, AC-40.11 (528.00 / 480.00 / 48.00); a table price below cost posts the IC loss as a debit | `internal/domain/charge_settlement.go` | built, unit tests |
| Charge preview `POST /v1/interco/charges:preview` (duty `intercompany.post`) | `internal/api/settlement.go` | built, tests |
| Centralized payments: pay and receive on behalf legs with due-to / due-from from the matrix, amount rounded to the minor unit, FR-40.12, AC-40.10 | `internal/domain/centralized.go` | built, unit tests |
| Centralized payment preview `POST /v1/interco/centralized-payments:preview` (duty `intercompany.configure`) | `internal/api/settlement.go` | built, tests |
| Planning group run order: buyers before suppliers, each entity once; cycles (strongly connected entities) need an explicit sequence for every member and run in sequence order while edges around them are kept, else IC-0816, FR-40.15 / BR-40.13 | `internal/domain/planning.go` | built, unit tests and rapid properties over acyclic and cyclic partner graphs |
| IC demand at requested date minus transport lead time (AC-40.16) and stale suppliers of a replanned buyer (IC-0815) | `internal/domain/planning.go` | built, unit tests |
| Planning sequence `POST /v1/interco/planning-groups:sequence` (duty `intercompany.plan`) | `internal/api/planning.go` | built, tests |
| OpenAPI matches the mounted routes and duties | `internal/api/contract_test.go` | built, test |
| Web pages: `/intercompany/charges`, `/intercompany/centralized-payments`, `/intercompany/planning` | `apps/web/src/modules/intercompany` | built, vitest |

## What slice 3 adds

Direct delivery chains, transfer-pricing evidence and the bank-return
reversal of a centralized payment, each behind a stateless endpoint and a web
page; the reconciliation summary now says whether it blocks consolidation.
Still nothing is stored, posted or published.

| Area | Code | Status |
|---|---|---|
| Direct delivery chain check: customer order and IC purchase in the buyer, IC sales order in the seller of an active pairing (else IC-0801), three distinct documents with one quantity; the seller's packing slip posts the buyer's product receipt and customer packing slip at the shipped quantity (partial allowed, never above the IC sales order), FR-40.3, AC-40.3 | `internal/domain/chain.go` | built, unit tests |
| Chain preview `POST /v1/interco/chains:preview` (duty `intercompany.view`) | `internal/api/slice3.go` | built, tests |
| Transfer-pricing evidence file per entity pair and period as CSV (seller, buyer, period, item, quantity, currency, price, cost, margin, margin %, basis), amounts at the currency minor unit, spreadsheet formula cells neutralised, FR-40.4 (PRD open question 3: CSV) | `internal/domain/transfer.go` | built, unit tests |
| Evidence export `POST /v1/interco/transfer-pricing/evidence:export` (duty `intercompany.view`, `text/csv` with a sanitised `Content-Disposition` file name) | `internal/api/slice3.go` | built, tests |
| Centralized payment reversal on a bank return: every leg flips side in both entities under the original pair id, BR-40.11 | `internal/domain/reversal.go` | built, unit tests |
| Reversal preview `POST /v1/interco/centralized-payments/reversals:preview` (duty `intercompany.configure`; takes the original payment and its pair id) | `internal/api/slice3.go` | built, tests |
| Reconciliation summary `blocking` flag: true while any item is a Mismatch (FR-40.6 gate for CNS-0901) | `internal/api/api.go` | built, tests |
| Web pages: `/intercompany/chains`, `/intercompany/transfer-pricing`, reversal on `/intercompany/centralized-payments` | `apps/web/src/modules/intercompany` | built, vitest |

## What slice 4 adds

The first stored resource: trading-partner pairings in Postgres with
row-level security, idempotent create, versioned policy changes and their
events through the outbox, plus pricing an IC line and checking a linked
order change against a stored pairing, and the partner screens.

| Area | Code | Status |
|---|---|---|
| Migration `0001_partners.sql`: `outbox`, `idempotency_keys`, `intercompany_partner` (tenant_id first, ENABLE + FORCE RLS with the `dbx.RLSPolicy` tenant policy, percentages numeric, one active pairing per direction as a partial unique index) | `migrations/` | built, store tests incl. RLS as the non-bypass role |
| Pairings, FR-40.1: seller entity's IC customer and buyer entity's IC vendor, active flag, action policy (defaults auto-create on, auto-post off), pricing policy and markup, BR-40.3 tolerance (price %, date days); the direction never changes | `internal/domain/partner_record.go`, `internal/store/partners.go` | built, unit and store tests |
| `POST /v1/interco/partners` (duty `intercompany.configure`, `Idempotency-Key` required; a retry replays the stored response, a reused key with another body is IC-0841, a second active pairing for the direction IC-0840) | `internal/api/partners.go` | built, tests (incl. concurrent same-key requests) |
| `GET /v1/interco/partners` (filters `entity`, `active`, keyset paging `after` / `limit`) and `GET /v1/interco/partners/{id}` with `ETag` (duty `intercompany.view`) | `internal/api/partners.go` | built, tests |
| `PATCH /v1/interco/partners/{id}` (duty `intercompany.configure`, `If-Match` required: 428 missing, 412 stale, IC-0843): policy change, activate, deactivate; version + 1 and `dev.passage.interco.partner.updated.v1` with the changed fields; a no-op patch keeps the version | `internal/store/partners.go` | built, tests |
| Events `dev.passage.interco.partner.created.v1` / `updated.v1` written through the outbox in the same transaction (not in the PRD event table; added for the saga worker and the sales and procurement services) | `internal/events`, `events/asyncapi.yaml` | built, contract test |
| IC line pricing from the stored policy, FR-40.4: PriceList, CostPlus with the pairing's markup, TransferPriceTable by the item row effective on the line date (IC-0821), extended amount rounded once to the minor unit, pricing basis returned as the link will record it; an inactive pairing is IC-0801. `POST /v1/interco/partners/{id}/lines:price` (duty `intercompany.view`; transfer prices are sent with the request) | `internal/domain/line_pricing.go`, `internal/api/partners.go` | built, unit and API tests |
| Linked order change decision, BR-40.3 / IC-0804: any quantity change, a price move beyond the tolerance % or a date move beyond the tolerance days sends the mirror PO for re-approval, with every breach listed. `POST /v1/interco/partners/{id}/changes:evaluate` (duty `intercompany.view`) | `internal/domain/order_change.go`, `internal/api/partners.go` | built, unit, rapid and API tests |
| Web pages: `/intercompany/partners` (list with filter, paging, loading, empty and error states), `/intercompany/partners/new` (create with an Idempotency-Key kept across retries), `/intercompany/partners/:id` (policy, edit with If-Match and conflict reload, activate / deactivate, price a line, check an order change) | `apps/web/src/modules/intercompany` | built, vitest |

Remaining deliverables and acceptance criteria are listed in
[NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md).

## Posting, charges and stored reconciliation (issue 40, subtasks 40-3 to 40-5)

| Area | Code | Status |
|---|---|---|
| `POST /v1/interco/journals` (duty `intercompany.post`, `Idempotency-Key` required): derives the journal and posts one ledger entry per entity through `platform/ledgerclient`. A later side refused (4xx) reverses the earlier sides and stores the journal as compensated (422 IC-0845); an unreachable ledger leaves it pending (503 IC-0846) and the same key resumes it without posting twice | `internal/posting`, `internal/api/journals.go` | built, tests over Postgres with the ledger fake |
| `GET /v1/interco/journals`, `GET /v1/interco/journals/{id}`; events `journal.posted.v1` and `journal.compensated.v1` | `internal/store/journals.go` | built |
| `POST /v1/interco/charges`: stored per `sourceId` and `version`, priced by the transfer price in force on the date, posted through the same two-sided path; no applicable price is 202 with status suspense and nothing posted (BR-40.12). `GET /v1/interco/charges`, `GET /v1/interco/charges/{id}`; event `charge.posted.v1` | `internal/api/charges.go`, `internal/store/charges.go` | built, tests (AC-40.11 postings, suspense then repriced, compensation) |
| `POST /v1/interco/reconciliations/run` (duty `intercompany.reconcile`, `Idempotency-Key` required), `GET /v1/interco/reconciliations`, `GET /v1/interco/reconciliations/{id}?status=`, `POST /v1/interco/reconciliations/items/{id}/resolve`; the run and items are immutable and an open item is resolved once with a note; event `reconciliation.flagged.v1` | `internal/api/recon.go`, `internal/store/recon.go`, migration `0004_reconciliation.sql` | built, tests incl. the planted-50 fixture `internal/api/testdata/ic-recon-planted-50.json` |
| Web: posted journals, charge log, reconciliation runs, posting from the journal and charge previews, saving a classification as a run | `apps/web/src/modules/intercompany` | built, vitest, demo data |

Posting needs `LEDGER_URL` and the service signing key (`PASSAGE_SERVICE_NAME=intercompany`); without
them the posting routes answer 503 and everything else works. The Helm chart takes them from
`ledgerUrl` and `serviceIdentity.signingKeySecret`. The service is granted `ledger.entry.post`,
`ledger.journal.reverse` and `ledger.ledger.lookup` on the ledger.

## Run

```
cd services/intercompany
DATABASE_URL=postgres://... MIGRATE_ON_START=true go run ./cmd/intercompany   # HTTP_ADDR defaults to :8080
go vet ./... && go test ./...                                                  # store tests skip without a database
PASSAGE_TEST_DATABASE_URL=postgres://... go test ./...                         # store tests
PASSAGE_TEST_APP_DATABASE_URL=postgres://passage_app@... # also runs the RLS test as a non-bypass role
```

The service reads `DATABASE_URL` (required; the chart takes it from the
`databaseSecret` Kubernetes secret), `MIGRATE_ON_START` (dev only),
`HTTP_ADDR` and `ADMIN_ADDR` (default `:9090`, serving `/metrics`).

## Layout

```
cmd/intercompany/        entrypoint (JSON slog, database, migrations, router, serve)
internal/domain/         pure rules, no I/O
internal/store/          Postgres repository (RLS, idempotency keys, outbox)
internal/events/         event types and payloads
internal/api/            HTTP handlers and DTOs
migrations/              NNNN_name.sql, applied in order by dbx.Migrate
api/openapi.yaml         OpenAPI 3.1 for every exposed endpoint
events/asyncapi.yaml     AsyncAPI 3 for every published event
charts/intercompany/     Helm chart
```

## Error codes

Problem details use `type: https://passage.dev/problems/<code>`.

| Code | HTTP | Meaning |
|---|---|---|
| COM-0001 | 400 | Invalid input (currency, amount, side, date, period, quantity, category, method, pair id, a missing field or header, a malformed `If-Match`) |
| IC-0801 | 422 | No active IC partner policy between the two entities |
| IC-0805 | 422 | No IC accounting matrix entry for the entity pair (due-to / due-from missing) |
| IC-0815 | 200 | Supplier plan is stale after its buyer was replanned (warning in the planning sequence response) |
| IC-0816 | 422 | Planning group has a cycle without an explicit sequence |
| IC-0821 | 422 | No transfer price for the category and role on the date |
| IC-0804 | 200 | Change exceeds the pairing's tolerance; the mirror PO goes for re-approval (in the change decision) |
| IC-0830 | 422 | Derived journal legs do not balance (defect guard; not in the PRD catalog, listed in NOT-IMPLEMENTED.md) |
| IC-0840 | 409 | An active pairing already exists for this direction (not in the PRD catalog) |
| IC-0841 | 422 | The `Idempotency-Key` was used with a different request (not in the PRD catalog) |
| IC-0842 | 404 | No pairing with this id in the tenant (not in the PRD catalog) |
| IC-0843 | 412 / 428 | `If-Match` names a version the pairing no longer has, or is missing (not in the PRD catalog) |
| IC-0844 / IC-0848 | 404 | No journal / no charge with this id (not in the PRD catalog) |
| IC-0845 | 422 | A later side was refused by its ledger and the earlier sides were reversed (not in the PRD catalog) |
| IC-0846 | 503 | A ledger did not answer; the journal is pending and the same `Idempotency-Key` resumes it (not in the PRD catalog) |
| IC-0847 | 422 | The ledger does not belong to the legal entity named for the entity code (not in the PRD catalog) |
| IC-0850 / IC-0851 | 404 | No reconciliation run / item with this id (not in the PRD catalog) |
| IC-0852 | 409 | The item is matched or already resolved, so there is nothing to resolve (not in the PRD catalog) |
| AUTH-0001 / AUTH-0003 | 401 / 403 | Missing tenant / missing duty |

## Dependencies

Only the platform module and what it already pulls in (chi, pgx/v5,
shopspring/decimal, pgregory.net/rapid for tests).

## Wave reference

PSG-40, PRD section 7.40 (Wave 40: Intercompany & Financial Consolidation).

## Invoice settlement mode consumers (#40)

`chargeinvoice.requested.v1` now has its own channel, `passage.interco.chargeinvoice`, and names `lenderLegalEntityId`,
`borrowerLegalEntityId` (from the registry) and `customerAccount`, `vendorAccount` (from the active pairing of lender and borrower).
Billing raises the lender's invoice proposal and payables takes the borrower's vendor invoice into its intake from it. The outbox
reaches Kafka through the `outbox-intercompany` connector (deploy/kafka/connect.yaml, routed by event type; CDC migrations 0007 and
0008, secret `debezium-cdc-intercompany`). The pairing's customer and vendor accounts must be party ids for the documents to be raised.
