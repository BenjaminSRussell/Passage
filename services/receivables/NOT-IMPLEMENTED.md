# NOT-IMPLEMENTED: receivables (PSG-30, wave 30)

Slice 1 builds the cash-application core of the receivables service: open items,
aging, manual payments, the scored matcher (BR-30.1), settlement validation
(BR-30.4), the payment events, the OpenAPI and AsyncAPI contracts, the Helm
chart, and the web module (AR workspace, open items, payments, record payment,
payment page with match and apply). Slice 2 adds BAI2 lockbox import, the
risk-ranked collections worklist, disputes, promises to pay, dunning runs with
letters and fees, interest notes, their events, and web pages for each.
Everything below is absent, with the reason and where it goes. Status is either "later slice of PSG-30" (inside
this service or the web module, in a later increment) or "handoff" (needs another
owner or a kernel change).

## Ticket deliverables

| # | Deliverable | Status | Reason |
|---|---|---|---|
| 1 | Cash application: lockbox, BAI lockbox transmission format | later slice of PSG-30 | Slice 2 imports BAI2 cash-management files (01-99 records; each type-16 check credit is one payment, the customer reference is its remittance; duplicate files refused by SHA-256). FR-30.3 names the bank lockbox transmission (record types 1-9, with type 4 overflow carrying several invoice numbers per check); that fixed-width format is bank-specific and is not parsed. Lockbox payments are not matched or auto-applied on import: the clerk opens each one and uses `:match` / `:apply`. |
| 1 | Cash application: camt.053 auto-match | later slice of PSG-30 | Statement import and entry uniqueness (statement id, entry ref) not built. |
| 1 | Cash application: ML-assisted suggestions (Python ranker) | later slice of PSG-30 | The ranker is a separate Python service (`matcher-py`), not in this owned path. Rule score only in slice 1. |
| 1 | Remittance advice matching (CSV, JSON, EDI 820) | later slice of PSG-30 | Only the manual payment's `remittance[]` is accepted; file formats are not parsed. |
| 2 | Aging basis option and nightly aging job | later slice of PSG-30 | The worklist and aging are computed on request, by due date. The 01:30 job, stored snapshots, invoice-date basis and per-entity bucket definitions are not built. |
| 2 | Dunning: letters rendered through docgen, fees and levels to the ledger, level-4 hold | handoff | Runs, letters, exclusions, levels, intervals and FEE open items are built. `document_ref` is a placeholder (`DL-<letter id>`) until docgen (wave 5) renders the letter; the fee is an open item but is not posted to GL 4300 (ledger posting, see below); the level-4 credit-hold recommendation to sales (CR-3) is only carried in `dunningletter.issued.v1`. |
| 2 | Dunning procedures per customer group | later slice of PSG-30 | Grace days come from the run request (default 5). Customer-group procedures (`dunning_procedure`, interval and minimum overrides), `collection_letter_basis = CUSTOMER`, legal text and a customer-level dunning block (customer master, wave 6/26) are not built; only item-level `dunning_block` is honoured. |
| 2 | Interest: rate configuration and GL posting | later slice of PSG-30 | Notes and INTEREST open items are built. Rate, basis and grace come from the request, not from `interest_rate` / `interest_basis` per legal entity and customer group; the interest is not posted to GL 4310 (ledger posting). |
| 2 | Disputes and promises: Temporal workflow, resolution postings | handoff | Disputes (one active per item, SLA 120 h, BR-30.6 moves) and promises are built as REST state. The `collections-workflows` TS service (Temporal namespace `collections`), SLA timers and escalation, `workflow_run_id`, evidence and timeline, and the CLOSED state after resolution are not. RESOLVED_CREDIT does not post a credit note (billing, wave 29) and RESOLVED_WRITE_OFF does not write the item off (ledger); both only release the item. AC-30.9's credit-note half is therefore not met. A promise excludes its whole item, not only the promised amount. |
| 2 | Customer risk score published to credit | later slice of PSG-30 | The 0-100 score is computed on the worklist. `customer-balance.changed.v1` to sales credit is not published. "Average days to pay" is approximated by the open-amount-weighted days late of the open items; settled history is not used yet. |
| 3 | Return orders / RMA dispositions, credit note, restocking | handoff | The `/api/v1/sales/return-orders` routes and inventory receipt belong to sales and inventory. Receivables only settles the credit note once those exist. |
| 4 | Rebate settlement from wave 26 accruals | handoff | Needs `dev.passage.pricing.rebate.settled.v1` from wave 26 and the AP payment hook from wave 19. Not built here. |
| 4 | Customer statements (monthly, on demand, PDF) | later slice of PSG-30 | Not built. Needs docgen (wave 5). |
| 5 | Customer portal (orders, invoices, payments, returns, documents) | later slice of PSG-30 | See `apps/customer-portal/README.md`. Portal BFF, Keycloak `portal` realm and OPA scoping are handoffs. |
| 5 | Payment adapter (Stripe/Adyen sandbox) and `payments-gateway` | handoff | A separate TS service (`payments-gateway`) with Vault-held keys. It is not in this owned path. |
| 6 | SEPA direct debit mandates and pain.008 runs | later slice of PSG-30 | Needs the wave 19 ISO 20022 generator (handoff). Mandate and run tables are not built. |
| 7 | Deductions workbench (BR-30.12) | later slice of PSG-30 | Not built. Needs wave 26 claims. |
| 7 | Distributor chargeback claim validation (BR-30.15) | later slice of PSG-30 | Not built. Needs wave 26 agreements; EDI 844/849 is wave 35. |
| 8 | Correspondence framework and email templates | later slice of PSG-30 | Templates and request log not built. Delivery depends on the FR-29.19 routing and SMTP transport (wave 4/5, handoff). |
| 8 | Customer balance confirmations | later slice of PSG-30 | Not built. |
| 9 | Minimal collection cases (`collection_case`) | later slice of PSG-30 | Not built. The generic case framework is wave 45. |
| 9 | Down payments and security deposits (accounts 2190 / 2195) | later slice of PSG-30 | Not built. |
| 9 | Vendor dunning over AP debit items | handoff | Reads AP open items from wave 19. Not built. |
| 9 | GL and intercompany interest | later slice of PSG-30 | Not built. Intercompany pairing is wave 40. |
| 10 | Portal guided configuration (w8 configurator) | later slice of PSG-30 | Portal work, not built. |
| 10 | Returnable packaging charges | later slice of PSG-30 | Not built. Depends on wave 28 balances. |
| - | Deploy: AR workspace, customer portal at `customer.<env>.passage.dev`, payment adapter in sandbox | handoff | Ingress, DNS and sandbox credentials belong to deployment-cicd. The chart here covers the receivables API only. |

## Acceptance criteria

| AC | Status | Reason |
|---|---|---|
| AC1 cash-application fixtures: 500 payments, ≥ 90% auto-match, zero false matches | later slice of PSG-30 | The `testdata/cash-app-500` fixture and the answer key are not created. The rules and the subset-sum bound are unit-tested, but the fixture-level accuracy is not measured. |
| AC2 dunning run respects levels, grace and blocks | partly met | Levels, grace, minimum, interval, blocks, disputes and promises are unit-tested (`domain/dunning_test.go`) and tested end to end against Postgres (`store/collections_test.go`, including AC-30.4's 10-day interval and AC-30.9's dispute exclusion). The `dunning-60` fixture of AC-30.3 (60 customers, 31 expected letters) is not created. |
| AC3 RMA E2E: return, inspection, credit note posts to inventory and AR | handoff | Depends on sales, inventory and ledger. |
| AC4 Phase 5 exit: one-month simulation, reconciliation pack signed | handoff | `sim/phase5-month` spans waves 3-5 and needs the accountant sign-off. Owned by testing-reconciliation. |
| AC5 every deliverable has code or a reason in this file | met | This file lists every item not built. |
| AC6 `go vet` and `go test` clean; web vitest and tsc; OpenAPI/AsyncAPI match code; chart lints | met for slices 1 and 2 | Go vet and tests pass, including the store tests against Postgres (non-superuser role, so RLS applies); the web module tests and tsc pass; the chart is unchanged in slice 2. The OpenAPI and AsyncAPI cover the routes and events in code. |

## Functional requirements not in this slice

- **FR-30.1 Open items and settlement.** Settlement and apply are built, including cash discount, write-off and residual. The following are not: credit-side open items (`CREDIT_NOTE`, `PAYMENT`, with negative amounts; the slice-1 schema requires `amount > 0`) and settling credits against debits (`settlements:batch`, ARSettle); FX gain/loss at settlement (BR-30.4 7100/7200), automatic settlement on post (`automatic_settlement`), and open-item creation from invoices. Open items are fed by wave 29 `invoice` events, and that consumer is not built. `CreateOpenItem` exists in the store for tests only.
- **FR-30.2 Manual payments.** Built for CHECK, ACH, WIRE, CARD and CASH. The unapplied residual posts to 1205 only through the ledger, which is not wired (see Ledger posting below).
- **FR-30.4 Remittance advice files.** Not built (see deliverable 1).
- **FR-30.5 camt.053 import.** Not built (see deliverable 1).
- **FR-30.6 Aging worklist and nightly job.** Snapshot and risk-ranked worklist on demand are built. The 01:30 job and invoice-date basis are not. Saved worklist filters and "log contact" / "hold order" actions are not built.
- **FR-30.13 Statements.** Not built.
- **FR-30.14 Customer portal.** Not built (see `apps/customer-portal/README.md`).
- **FR-30.15 Phase-exit simulation.** Handoff to testing-reconciliation.
- **FR-30.23 to FR-30.26.** Not built (see the deliverables table).

## Payment lifecycle not in this slice

- `:on-account` and `:reverse` endpoints (BR-30.6): the apply path handles residual on account, but there is no explicit route, no reversal and no re-apply of ON_ACCOUNT payments.
- `SUGGESTED` is never persisted. `:match` returns the decision without storing it, and nothing auto-applies on its own: the clerk applies the AUTO_APPLY candidate from the payment page. Auto-application on import arrives with the importers.
- `REVERSED` status and the reversal of an auto-application within the open period (BR-30.1) are not built.
- Source types LOCKBOX, CAMT053, PORTAL and REMITTANCE are rejected by validation. Only MANUAL is accepted.
- Settlements are not partitioned by month (budget §5.30.19). A plain table is used.
- Matcher rules R4 (bank account hash), R5 (payer name Jaro-Winkler) and R7 (short pay with reason) need data the slice does not carry.

## Events not in this slice

- Published and in `events/asyncapi.yaml`: `payment.received.v1` (manual and lockbox), `payment.applied.v1`, `dispute.opened.v1`, `dispute.resolved.v1`, and `dunningletter.issued.v1` (one per issued letter; the PRD names it `dunning-letter.issued.v1`, but the kernel event-type rule `[a-z][a-z0-9]*` allows no hyphen).
- Not built: `customer-balance.changed.v1`, `return-order.*` (sales), `pricing.rebate.settled.v1` (consumed from wave 26).
- Consumers of `invoice.*` and `credit-note.*` from wave 29 are not built.

## Ledger posting

- Settlement and payment postings (Dr 1000/1010, Cr 1200; Dr 4100 for cash discount; Dr 6100 for write-off; FX 7100/7200) are not posted through `ledger.Post`. `journal_posting_id` is never set.
- Dunning fees (Cr 4300) and interest (Cr 4310) are open items only; their journal postings wait for the same ledger client. Reconciliation of fees and interest to GL is therefore not yet checked.
- The `ledgerclient` package is a kernel item (new, not yet present). Handoff to the platform kernel and to the ledger wave (w11/w12). Reconciliation Σ open_amount = GL 1200 is therefore not yet checked.

## Platform and kernel gaps (handoffs)

- **Readiness.** `httpx.NewRouter` serves a static `/readyz`. The service's `API.Ready` pings Postgres but is not mounted, because chi rejects a second route. Handoff: the kernel should accept a readiness hook.
- **Idempotency.** Create uses the `idempotency_key` column on `payment` (unique per tenant). `:apply` stores the key and response in `idempotency_keys` and replays them. The key is optional on apply (If-Match already guards it); a kernel middleware would make it uniform across services.
- **Rate-limit, `Deprecation` and `X-Passage-Api-Build` headers** are kernel middleware, not added here.
- **Authentication.** Identity comes from the `tenant` middleware headers until the wave 2 JWT and OPA path is wired at the gateway. Handoff to edge-identity.

## Schema and data

- Tables: `open_item`, `payment`, `settlement` (0001); `outbox` (0002); `idempotency_keys` (0003); `dispute`, `promise_to_pay`, `dunning_run`, `dunning_letter`, `interest_note` (0004); `lockbox_batch` (0005). Every table has `tenant_id`, ENABLE and FORCE row-level security, a tenant policy and (except outbox and idempotency_keys) a restrictive legal-entity policy. Not built: `bank_statement`, `collection_case`, `dd_mandate`, `direct_debit_run`, `deduction`, `chargeback_claim`, `balance_confirmation`, `correspondence_*`, `rebate_accrual`, `credit_profile`, `return_order`. Each goes into its later slice's migration.
- `open_item` misses fields from §5.30.4: `invoice_account_id`, `amount_entity_currency`, `special_gl_type`. `dunning_run.parameters` is reduced to `grace_days`.
- Migrations were applied to an empty Postgres 16 database by the store tests in the review run.

## Testdata and fixtures

- `testdata/` is absent. The PRD fixtures `cash-app-500`, `dunning-60`, `rma-30`, `disputes-20`, `rebate-settle-12` and `portal-e2e` are not created. The BAI2 parser is tested against inline spec-shaped samples and a property test (never panics; dropping a record breaks a count).

## Web module (`apps/web/src/modules/receivables/`)

- Built: the AR workspace (aging by currency and bucket with an as-of date, tiles for payments to apply and on account), the open items list, the payments list, the record-payment form with remittance lines, and the payment page (match candidates with score and rules, the apply plan with amount, cash discount and write-off per item, on-account residual, If-Match and Idempotency-Key). Vitest tests cover loading, empty and error states and axe AA.
- Built in slice 2: lockbox imports (list, import a file by upload or paste, the payments it created); collections worklist; disputes (list with status filter, open from an open item, detail with review and resolve or reject, If-Match); promises to pay (list, record); dunning runs (list, propose, detail with the letter preview, exclusions and their reasons, review, issue, cancel); interest notes (list, calculate preview, post with Idempotency-Key); "Dispute" and "Promise" actions on open items; an active-disputes tile on the workspace. Vitest tests cover loading, empty and error states, the commands' headers and bodies, and axe AA for every new page.
- Not built: the cash application keyboard flow (J/K, Enter, A) and bulk accept of every candidate ≥ 85, settlements between credits and debits, the rendered dunning letter (needs docgen), worklist saved filters and contact log, RMA screen, the unapplied-cash tile, and the Playwright E2E tests.
- Needs for the shell owner (wave 5): none for this slice. The module uses only React and the registry type.
