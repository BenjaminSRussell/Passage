# Not implemented in PSG-7 (through PSG-703)

| Deliverable or AC | Status | Reason |
|---|---|---|
| AC1 dedup precision and recall | built | `internal/domain/dedup.go`; `dedup_test.go` seeds 10k parties with 500 planted duplicates. The PRD fixture `parties-10k.jsonl` is not in the repo, so the generator lives in the test |
| AC2 merge with atomic reference rewrite and integrity audit | built (store) | `internal/store/merge.go`, `audit.go`, `migrations/0002_party_merge.sql`; `store_test` covers rewrite, rollback with empty outbox, and a clean audit. Covers `customer_role` only (the one role table that exists); vendor and other roles extend `moveRoles` and `Audit` when they land. Handoff: `POST /v1/party/parties/{id}:merge` with duty `party.merge` is not wired, the API layer has no store dependency yet; it maps `ErrNotFound` to 404, `ErrNotMergeable`/`ErrRoleConflict` to 409, `domain.RuleError` to 422. Same-legal-entity role conflicts are rejected, not auto-resolved; the approval workflow is slice 3 |
| AC3 VAT results against recorded VIES responses | not built | No VIES protocol client or authentic recorded responses; VAT is format-checked and reported `unverified` |
| AC4 credit-limit change approval with Temporal test | built (party side) | `internal/store/credit.go`, `migrations/0003_pending_change.sql`; `credit_test.go` proves the limit stays unchanged until approved. Temporal-free by design (README, "Credit limit change approval"): the `change.requested.v1` event is the approval request. Not built: the `services/workflow-ts` consumer that starts `approvalWorkflow` from that event and calls `:approve` or `:reject`; customer create and read endpoints |
| Deliverable 1: persisted parties and roles | partly built | party core is built (PSG-703): create, read with merged redirect, PATCH with optimistic concurrency, `party.created` and `party.updated` events. Tables `party`, `payment_term`, `posting_profile`, `customer_role` with RLS exist in `migrations/0001_init.sql`; role and electronic-address endpoints and the other tables follow in the PSG-702 follow-up |
| Deliverable 2: merge workflow and survivorship, `:match` endpoint | partly built | merge and survivorship built (see AC2); the `:match` endpoint and merge approval workflow follow in slice 3; the scoring engine is built |
| Deliverable 3: tax numbers | partly built | VAT (EU), EIN and GSTIN format checks. Not built: VIES protocol client and cache, alert after 3 consecutive timeouts, IRS TIN and GSTN lookups, UK mod-97, ABN, CNPJ, RFC, UTR |
| Deliverable 4: groups, posting profiles, payment and delivery terms, credit limits | partly built | tables for payment terms, posting profiles and credit-limit columns exist; groups, delivery terms and modes, endpoints and `DueDate` follow in PSG-702 slice E; posting profile references the ledger |
| Deliverable 5: data governance, stewardship queue, change approval | slice 3 of PSG-7 | needs persistence and the workflow engine |
| Deliverable 6: vendor certifications, diversity classes, installment schedules | later slice of PSG-7 | needs persistence |
| Deliverable 7: one-time accounts, relationships, address books, customer hierarchies, code lists | later slice of PSG-7 | needs persistence; hierarchies depend on PSG-6, which is absent |
| Web module `apps/web/src/modules/party/` | later slice of PSG-7 | no API to call beyond validate |
| Postal addresses and the address blocking key | PSG-6 | wave-6 addresses are absent; the postal key stays empty |
| Bank role and `PartyBankAccount` | slice 3 of PSG-7 | go with the protected-field approvals |
| OPA duty entries for `/v1/party/**` in `policies/authz/data.json` | handoff | outside the owned paths |
| Legacy import (FR-7.12, the deploy criterion) | later slice of PSG-7 | needs the persisted party model |
| PSG-702 follow-up: electronic addresses (FR-7.7, migration `0003` because `0002_party_merge.sql` exists, E.164 and RFC 5322 normalization); duplicate detection on create and update with the `duplicate_candidate` table, `PTY-DUP-LIKELY` 409 with override, and `GET /duplicates` with `:dismiss` (C); persisted tax registrations and the remaining validators (D); groups, delivery terms and modes, payment-term endpoints and `DueDate` (E); vendor, contact, employee and prospect roles (F) | PSG-702 follow-up | party core (B) is delivered by PSG-703 |
| `GET /v1/party/parties:search?q=` typeahead (FR-7.13): `q` of at least 2 characters, `search_name % q` or `ILIKE q\|\|'%'`, similarity order, limit 1 to 20, `PTY-SEARCH-MIN-CHARS` | later slice of PSG-703 | deferred by the ticket's line budget; the `search_name` trigram index already exists in `0001` |
| Search filters `role=` and `legalEntity=` on `parties:search` | roles slice (F) | need the role tables |
| `POST /v1/party/parties:batch` import endpoint that `services/migration/NOT-IMPLEMENTED.md` waits on ("needs target contexts' `:batch` endpoints (PSG-7 ...)") | later slice of PSG-7, with legacy import | needs the persisted party model, now built |
| `serviceCode` in problem bodies | kernel gap | `platform/go/httpx.Problem` has no such field and `platform/` is not editable from a wave |
| Architecture sheet says party is TypeScript (`party-ts`) | deviation | the ticket, CONVENTIONS and `roadmap/ownership.json` assign Go at `services/party/` |
| Argo CD app `deploy/envs/dev/apps/party.yaml`, CI image jobs, Envoy route | handoff | outside the owned paths |
| JWT validation | wave 2 | services trust gateway headers until Envoy Gateway lands |
