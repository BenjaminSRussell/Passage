# Not implemented in PSG-7 slice 1

| Deliverable or AC | Status | Reason |
|---|---|---|
| AC1 dedup precision and recall | built | `internal/domain/dedup.go`; `dedup_test.go` seeds 10k parties with 500 planted duplicates. The PRD fixture `parties-10k.jsonl` is not in the repo, so the generator lives in the test |
| AC2 merge with atomic reference rewrite and integrity audit | slice 2 of PSG-7 | needs migrations, store, outbox and `party.party.merged.v1` |
| AC3 VIES adapter against recorded fixture | built | `internal/vies` and `internal/vies/testdata/vies-recorded.json`; the fixture is a small hand-assembled stand-in for the PRD's 200-number recording and is served only when `VIES_FIXTURE=1`; by default VAT is format-checked and reported `unverified` |
| AC4 credit-limit change approval with Temporal test | slice 2 of PSG-7 | needs customer persistence, `pending_change` and a workflow engine decision; `go.temporal.io/sdk` is not in the kernel and needs README justification |
| Deliverable 1: persisted parties and roles | slice 2 of PSG-7 | no store, migrations or RLS yet |
| Deliverable 2: merge workflow and survivorship, `:match` endpoint | slice 2 of PSG-7 | needs the party store; the scoring engine is built |
| Deliverable 3: tax numbers | partly built | VAT (EU), EIN and GSTIN format checks and the VIES fixture adapter. Not built: live VIES HTTP client, 30-day cache, alert after 3 consecutive timeouts, IRS TIN and GSTN lookups, UK mod-97, ABN, CNPJ, RFC, UTR |
| Deliverable 4: groups, posting profiles, payment and delivery terms, credit limits | slice 2 of PSG-7 | needs persistence; posting profile references the ledger |
| Deliverable 5: data governance, stewardship queue, change approval | slice 2 of PSG-7 | needs persistence and the workflow engine |
| Deliverable 6: vendor certifications, diversity classes, installment schedules | later slice of PSG-7 | needs persistence |
| Deliverable 7: one-time accounts, relationships, address books, customer hierarchies, code lists | later slice of PSG-7 | needs persistence; hierarchies depend on PSG-6, which is absent |
| Web module `apps/web/src/modules/party/` | later slice of PSG-7 | no API to call beyond validate |
| `Idempotency-Key` storage | slice 2 of PSG-7 | validate is read-only; the key is accepted and ignored |
| Dependency-aware `/readyz`, RLS, outbox | slice 2 of PSG-7 | this slice has no database |
| `serviceCode` in problem bodies | kernel gap | `platform/go/httpx.Problem` has no such field and `platform/` is not editable from a wave |
| Architecture sheet says party is TypeScript (`party-ts`) | deviation | the ticket, CONVENTIONS and `roadmap/ownership.json` assign Go at `services/party/` |
| Argo CD app `deploy/envs/dev/apps/party.yaml`, CI image jobs, Envoy route | handoff | outside the owned paths |
| JWT validation | wave 2 | services trust gateway headers until Envoy Gateway lands |
