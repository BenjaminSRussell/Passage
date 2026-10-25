# Not implemented in PSG-7 (through PSG-702 slice A)

| Deliverable or AC | Status | Reason |
|---|---|---|
| AC1 dedup precision and recall | built | `internal/domain/dedup.go`; `dedup_test.go` seeds 10k parties with 500 planted duplicates. The PRD fixture `parties-10k.jsonl` is not in the repo, so the generator lives in the test |
| AC2 merge with atomic reference rewrite and integrity audit | slice 3 of PSG-7 | needs migrations, store, outbox and `party.party.merged.v1` |
| AC3 VAT results against recorded VIES responses | not built | No VIES protocol client or authentic recorded responses; VAT is format-checked and reported `unverified` |
| AC4 credit-limit change approval with Temporal test | slice 3 of PSG-7 | needs customer persistence, `pending_change` and a workflow engine decision; `go.temporal.io/sdk` is not in the kernel and needs README justification |
| Deliverable 1: persisted parties and roles | partly built | tables `party`, `payment_term`, `posting_profile`, `customer_role` with RLS exist in `migrations/0001_init.sql`; the party, role and electronic-address endpoints and the other tables follow in PSG-702 slices B to F |
| Deliverable 2: merge workflow and survivorship, `:match` endpoint | slice 3 of PSG-7 | needs the party store; the scoring engine is built |
| Deliverable 3: tax numbers | partly built | VAT (EU), EIN and GSTIN format checks. Not built: VIES protocol client and cache, alert after 3 consecutive timeouts, IRS TIN and GSTN lookups, UK mod-97, ABN, CNPJ, RFC, UTR |
| Deliverable 4: groups, posting profiles, payment and delivery terms, credit limits | partly built | tables for payment terms, posting profiles and credit-limit columns exist; groups, delivery terms and modes, endpoints and `DueDate` follow in PSG-702 slice E; posting profile references the ledger |
| Deliverable 5: data governance, stewardship queue, change approval | slice 3 of PSG-7 | needs persistence and the workflow engine |
| Deliverable 6: vendor certifications, diversity classes, installment schedules | later slice of PSG-7 | needs persistence |
| Deliverable 7: one-time accounts, relationships, address books, customer hierarchies, code lists | later slice of PSG-7 | needs persistence; hierarchies depend on PSG-6, which is absent |
| Web module `apps/web/src/modules/party/` | later slice of PSG-7 | no API to call beyond validate |
| `Idempotency-Key` replay on POST endpoints | PSG-702 slice B | the `idempotency_keys` table exists; no POST uses it yet, and validate ignores the key |
| Postal addresses and the address blocking key | PSG-6 | wave-6 addresses are absent; the postal key stays empty |
| Bank role and `PartyBankAccount` | slice 3 of PSG-7 | go with the protected-field approvals |
| OPA duty entries for `/v1/party/**` in `policies/authz/data.json` | handoff | outside the owned paths |
| Legacy import (FR-7.12, the deploy criterion) | later slice of PSG-7 | needs the persisted party model |
| PSG-702 slices B to F: party create, search, PATCH and events (B); duplicate candidates (C); tax registrations and validators (D); groups, terms and `DueDate` (E); roles (F) | PSG-702 follow-up | slice A delivered migration `0001`, the store, readiness and the chart |
| `serviceCode` in problem bodies | kernel gap | `platform/go/httpx.Problem` has no such field and `platform/` is not editable from a wave |
| Architecture sheet says party is TypeScript (`party-ts`) | deviation | the ticket, CONVENTIONS and `roadmap/ownership.json` assign Go at `services/party/` |
| Argo CD app `deploy/envs/dev/apps/party.yaml`, CI image jobs, Envoy route | handoff | outside the owned paths |
| JWT validation | wave 2 | services trust gateway headers until Envoy Gateway lands |
