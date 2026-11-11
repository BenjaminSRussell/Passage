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

`store.Merge(ctx, MergeRequest{WinnerID, LoserID, Rules})` runs in one tenant transaction: survivorship per field (`keepWinner` default, `keepLoser`, `newest`, `nonEmpty`; an empty preferred value falls back to the other side), the loser's customer roles moved to the winner, `invoice_account_party_id` references rewritten, the loser marked `merged` with `merged_into_id`, a `party_merge` record, and one `dev.passage.party.party.merged.v1` outbox row. Both parties must share a type and be unmerged; a role in the same legal entity on both sides returns `ErrRoleConflict` and changes nothing. The principal must not be legal-entity scoped. No route calls `store.Merge` directly.

Merges go through approval (#450). `POST /v1/party/parties/{id}:merge` (gateway duty `party.write`, handler flag `party.merge`, a `tenant.administer` user with no legal-entity claim; optional `Idempotency-Key`) checks the parties and records a `merge_request` (`migrations/0013_merge_request.sql`), answers 202 with it, merges nothing and emits `dev.passage.party.merge.requested.v1`. The workflow-ts merge approval starts from that event; a different user decides it at `POST /v1/workflows/party-merges/{id}:approve|:reject|:delegate` (`party.merge.approve`, `tenant.administer`, no legal-entity claim, and neither `payment-proposal.approve` nor `payment-run.execute`: a merge moves vendor roles and their bank accounts, so merge approvers are kept apart from payment-run approvers). workflow-ts then calls `POST /v1/party/merge-requests/{id}:approve` or `:reject` with a tenant-level `workflow-ts` token (service-only `party.merge.execute`, no `le` claim, `act` = the approver); no other caller is accepted. Approve runs the merge above and records the approver in one transaction; the requester is refused (`PTY-MERGE-SELF-APPROVAL`, and a table CHECK), a second decision is 409 `PTY-MERGE-DECIDED`, the same approver repeating the same decision gets the stored request, and parties that changed since the request give 409 `PTY-MERGE-STALE` with nothing merged (workflow-ts then rejects it). `GET /v1/party/merge-requests/{id}` (`party.read`) reads one. Rejection emits `dev.passage.party.merge.rejected.v1`.

`store.Audit(ctx)` returns an `IntegrityReport`: roles owned by merged parties, references to merged parties, merged parties without a merge record. `Clean()` is true after any successful merge.

## Credit limit change approval (FR-7.10, AC-7.4)

`POST /v1/party/customers/{id}/credit-limit` with `{"creditLimit":"120000.00"}` (a decimal string). A value above the stored limit never touches `customer_role.credit_limit`: it inserts a `pending_change` row (one open row per customer and field), answers 202 with the unchanged limit and the pending change, and enqueues `dev.passage.party.change.requested.v1` in the same transaction. A duplicate increase returns 409 `PTY-CHANGE-PENDING` with the existing id in `errors.pendingChangeId`; `GET /v1/party/pending-changes/{id}` reads a change within the caller's tenant and legal-entity scope. `POST /v1/party/pending-changes/{id}:approve` writes the limit and enqueues `change.approved.v1`; `:reject` leaves the limit alone and enqueues `change.rejected.v1`. The requester cannot decide their own change (`PTY-PENDING-SELF-APPROVAL`). A decrease is applied at once and emits `customer.updated.v1`. `pending_change` carries RLS on tenant and legal entity; `0004_grants.sql` grants `passage_app`.

Workflow engine: Temporal, per ADR 0001. Party stays Temporal-free and adds no `go.temporal.io/sdk` dependency. The `change.requested.v1` outbox event is the approval request: a `services/workflow-ts` worker consumes it, starts `approvalWorkflow` with `requestId` = `change_id` and `entityType` = `credit_limit`, and the workflow's outcome calls `:approve` or `:reject` as the deciding user. Because the pending row and the request event commit together, a limit change cannot be lost or applied without a recorded request, and the rule that the limit is unchanged until approved is enforced by party alone, whatever the workflow does. The consumer in workflow-ts is not part of this change (follow-up issue). Decisions reach the workflow only through its authenticated route `POST /v1/workflows/party-changes/{changeId}:approve|:reject|:delegate`, and the workflow calls party with a `workflow-ts` service token holding `party.approve` and acting for that approver (#426); list the workflow-ts public key in `serviceIdentity.publicKeys`. Once the consumer exists, only the workflow's service identity should hold `party.approve`.

Rules worth knowing: a `NULL` credit limit counts as 0, so any first limit needs approval. Requesters cannot withdraw their own request. Requests and decisions need a non-blank `X-Passage-User` (401 `AUTH-0001` otherwise), and the database refuses a decider equal to the requester. If the stored limit changes after the request (for example a decrease), `:approve` answers 409 `PTY-PENDING-STALE` and the row stays pending; it can then only be rejected, after which a new request may be made. The same decider repeating the same decision gets 200 with the originally stored change and reason; a replay with a different reason is ignored and does not increment decision metrics or write another event. `:approve` and `:reject` accept an optional `reason` (max 500 Unicode characters), stored on `pending_change` and carried on the decided event. `pending_change` is never deleted (grants are SELECT, INSERT, UPDATE). Payables can reuse the same shape: pending row, request event, decision routes.

Protected fields per legal entity (FR-7.10, `party.protected_fields`): `GET /v1/party/legal-entities/{id}/protected-fields` (`party.read`) returns `{"fields":[...]}`; `PUT` (`party.approval-policy.write`, a duty separate from `party.approve` so the approver of a change cannot also switch protection off) replaces the set; `fields` is required and `[]` is an explicit unprotect-all (a missing or null `fields` is 422). Every PUT enqueues `party.protectedfields.updated.v1` with before, after, actor and time in the same transaction. A credit limit request from the user who last changed the config, within 24 hours of unprotecting `credit_limit`, is still held for approval. The legal entity id is not checked against the org service (party has no legal entity table); a legal-entity-scoped caller can only address its own. Only `credit_limit` is protectable today; an unknown field is 422 `PTY-PROTECTED-FIELDS-INVALID`, and a caller scoped to another legal entity gets 403 `PTY-LEGAL-ENTITY-OUT-OF-SCOPE`. A legal entity with no row defaults to `["credit_limit"]`, the behaviour above. When `credit_limit` is not in the set, an increase is applied at once like a decrease (emits `customer.updated.v1`, no pending row). Changes already pending stay decidable. The table `protected_field_config` (`0007`) carries tenant and legal-entity RLS; `0006` tightens `pending_change` so `status = 'pending'` holds exactly when `decided_by IS NULL`.

## Customer role

`POST /v1/party/parties/{id}/customer` adds the `customer_role` (`0001`) to a party in the caller's single legal entity (422 `PTY-LEGAL-ENTITY-REQUIRED` otherwise): `accountNumber`, `customerGroupId`, `paymentTermId` (must exist), `postingProfileId` (must belong to the legal entity), `currencyCode`, and optional `deliveryTermId`, `deliveryModeId`, `invoiceAccountPartyId`, `creditLimit` (decimal string), `creditCurrency`, `creditRating`, `creditHold` and `blocked`. One role per party per legal entity and one account number per legal entity: 409 `PTY-CUSTOMER-EXISTS`. `Idempotency-Key` makes a retry return the original role (the key is scoped to the legal entity and the party and stored in `idempotency_keys`); another body under the same key is 409 `idempotency_key_reuse`. Emits `dev.passage.party.customer.created.v1` through the outbox in the same transaction; the payload carries ids, account number, group, currency, credit limit and blocked status, no personal data. Initial `creditLimit` (FR-7.10): a person (`party.write`) may set a positive limit only where `credit_limit` is not protected in the legal entity (same 24-hour rule as the credit-limit route), otherwise 422 `PTY-CUSTOMER-INVALID` on `creditLimit`, so a limit cannot skip approval by being set at create; `0` or no limit is always accepted. The migration loader (`party.import.write`) loads the legacy limit as given: it was approved in the source system, the migration run is the control, and the limit is on the `customer.created.v1` event. `creditHold` and `blocked` only restrict and are accepted from both. `paymentTermId` is checked in the tenant, `postingProfileId` in the caller's legal entity and `invoiceAccountPartyId` as a live party, all as `passage_app` under RLS; `customerGroupId`, `deliveryTermId` and `deliveryModeId` have no master table yet and are stored unchecked. Held by `party.write` or `party.import.write`.

A caller holding only `party.import.write` (no `party.read` or `party.write`) gets a write receipt (`id`, `partyNumber`, `partyType`, `status`, `version`) from `POST` and `PATCH /v1/party/parties`, not the party record, because that duty opens no read route; role creates answer with the role, which carries no personal data.

## Vendor master

`vendor_role` (`0008`) is the vendor side of a party, one row per party and legal entity, with tenant and legal-entity RLS. It carries `vendor_group_id` (the vendor group other services reconcile against, for example payables aging and `invoice.posted` in receiving), `account_number`, `payment_term_id`, `currency_code` and `blocked`. Routes: `POST /v1/party/parties/{id}/vendor` (`party.write` or `party.import.write`; the caller must carry exactly one legal entity, 409 `PTY-VENDOR-EXISTS` for a second role or a taken account number; `Idempotency-Key` replays the original role, scoped to the legal entity and party, column `vendor_role.idempotency_key` from `0010`), `GET /v1/party/vendors/{id}` and `GET /v1/party/parties/{id}/vendors` (`party.read`; the lookup receiving calls at invoice posting).

Bank details live in `vendor_bank_account`. Account and routing numbers are sealed with `platform/go/fieldcrypt` under the tenant key, with tenant and field name (`vendor_bank_account.account_number`, `vendor_bank_account.routing_number`) as additional data, so a value moved to another field or tenant does not open. `POST /v1/party/vendors/{id}/bank-accounts` (`party.vendor-bank.write`, needs a user) stores one; `GET .../bank-accounts` (`party.read`) returns holder, country, currency and `accountLast4` only; `GET .../bank-accounts:reveal` (`party.vendor-bank.reveal`, for the payments service identity) returns the numbers and logs the reveal with the user and vendor id. With no `FIELDCRYPT_KEYS` the service starts, but bank writes and reveals return 503 `PTY-BANK-ENCRYPTION-UNAVAILABLE`. Bank accounts are insert-only; a bank change approval flow is not built. Merge moves vendor roles (and so their bank accounts) to the survivor, rejects the same legal entity on both sides, and `Audit` counts vendor roles still owned by a merged party.

## Supplier portal remainders (#511, migration `0015`)

Profile changes. A vendor's `PROFILE_CHANGE` request (`POST /v1/party/vendor-portal/requests`) now carries `changes`: the party fields it asks for, from `legalName`, `tradeName`, `registrationNumber`, `industryCode`, `languageCode`, `timeZone` (an empty value clears a field, except `legalName`). Bank details, tax registrations, the vendor group and payment terms are never part of it. `POST /v1/party/vendor-requests/{id}:approve` (duty `party.vendor-request.decide`, and for a profile change that carries changes also `party.write`, because the party is tenant-wide and can be a customer in another legal entity; without it the approval is refused with 403 `PTY-SUP-PARTY-WRITE` and the request stays `SUBMITTED`) writes the changes to the vendor's party in the approval's own transaction: the party row (version bumped, name and search name recomputed), `dev.passage.party.party.updated.v1`, the audit entry `profile.applied` naming the fields, and `applied_at` on the request. A change the party cannot take (it was merged, or the values no longer validate) fails the approval with the party's problem and leaves the request `SUBMITTED` for the buyer to reject with a note. Reject, withdraw and approving an `OTHER` request touch nothing.

Prospect registration. `POST /v1/party/vendor-portal/prospects` is the one portal route that carries no vendor: the supplier portal signs a token with no `vnd` and the single duty `party.vendor-portal.register` (`platform/go/tenant` refuses any other vendorless supplier-portal token). It stores a `vendor_prospect` (company, contact, email, country, optional phone, tax id, website, message; at most 2000 waiting per legal entity, else 429 `PTY-PROSPECT-LIMIT`) and answers 202 whether or not the address was already waiting. Nothing is in the master data until a buyer acts: `GET /v1/party/vendor-prospects[/{id}]` (`party.read`), `POST .../{id}:accept` (`party.write`; body `accountNumber`, `vendorGroupId`, `currencyCode`, optional `paymentTermId`, plus `questionnaireId`) first claims the registration (it turns `ACCEPTED`, decided by the caller, with no links yet, so a second buyer accepting at the same time gets 409 `PTY-PROSPECT-STATE`), then creates the party, a vendor role blocked until the onboarding is approved, and the existing `ONBOARDING` request, each step idempotent on the registration id, and links them. An accept that stopped part-way is finished by the same buyer repeating it (or by another buyer after 10 minutes), and a repeat that finds the onboarding already open links that request; `POST .../{id}:reject` (`party.vendor-request.decide`, a note) rejects it. The questionnaire and its approval by someone other than the buyer who accepted are the existing onboarding flow.

Vendor-admin invitations. `POST /v1/party/vendor-portal/invites` (`party.vendor-portal.admin`; `email`, optional `displayName`) stores an invitation for the verified vendor and returns its token once: `inv1.<tenant>.<legal entity>.<vendor>.<invite>.<secret>`; only the SHA-256 of the secret is stored, it expires after 7 days, and a new invitation to the same address replaces the open one. `GET .../invites` lists, `POST .../invites/{id}:revoke` revokes, both also `party.vendor-portal.admin`. Every signed-in supplier user's token carries `party.vendor-portal.read` and `.write`, so those duties do not open these three routes: the supplier portal signs `party.vendor-portal.admin` only for a session holding the `supplier-admin` role and only for these routes (its signer refuses the duty for anyone else), and also checks the role before it calls them. A token with the duty is still refused unless it is a supplier portal token naming a vendor (403 `PTY-SUP-SCOPE`). `POST .../invites/{id}:verify` and `:accept` (duty `party.vendor-portal.invite-accept`, signed only for these two calls) take `{"token"}`; the token must name the vendor of the verified caller and the invitation in the path and its secret must match, and every failure (unknown, wrong secret, expired, revoked, used) is the same 404 `PTY-INVITE-INVALID`. `verify` changes nothing so the portal can create the identity-provider user first; `accept` spends the invitation. The role an invitation grants is always `supplier-user`. `dev.passage.party.vendorinvite.created.v1` and `.accepted.v1` carry the address and expiry and never the token: no notification service exists yet, so the vendor administrator hands the link over.

## Duties

| Duty | Grants |
|---|---|
| `party.approve` | (PRD name: Approve master data changes) `POST /v1/party/pending-changes/{id}:approve`, `:reject`; seeded in `policies/authz/data.json` |
| `party.write` | `POST /v1/party/tax-registrations:validate`, `POST /v1/party/parties`, `POST /v1/party/parties/{id}/customer`, `POST /v1/party/customers/{id}/credit-limit`, `POST /v1/party/parties/{id}/vendor`, `PATCH /v1/party/parties/{id}` |
| `party.approval-policy.write` | `PUT /v1/party/legal-entities/{id}/protected-fields`; seeded in `policies/authz/data.json` |
| `party.vendor-portal.admin` | `GET` and `POST /v1/party/vendor-portal/invites`, `POST /v1/party/vendor-portal/invites/{id}:revoke`; `service_only`, granted to `supplier-portal` in `platform/go/tenant` and `policies/authz/data.json`, and signed by it only for a vendor administrator (`supplier-admin`) |
| `party.vendor-bank.write` | `POST /v1/party/vendors/{id}/bank-accounts` |
| `party.vendor-bank.reveal` | `GET /v1/party/vendors/{id}/bank-accounts:reveal` |
| `party.read` | `GET /v1/party/vendors/{id}`, `GET /v1/party/parties/{id}/vendors`, `GET /v1/party/vendors/{id}/bank-accounts` (masked), `GET /v1/party/parties/{id}`, `GET /v1/party/pending-changes/{id}`, `GET /v1/party/legal-entities/{id}/protected-fields`; seeded in `policies/authz/data.json` |
| `party.import.write` | (service token from the migration service, principal `service:migration-loader`) alternative to `party.write` on `POST /v1/party/parties`, `PATCH /v1/party/parties/{id}`, `POST /v1/party/parties/{id}/vendor` and `POST /v1/party/parties/{id}/customer`; also the only duty that may send a caller-supplied `id` on `POST /v1/party/parties` (409 `PTY-PARTY-ID-TAKEN` when taken). It opens no read route, and party writes answer such a caller with a receipt, not the record. `service_only`; granted to the `migration` service in `platform/go/tenant` and `policies/authz/data.json` |
| `pii.read` | shows `dob` unmasked on party responses; without it `dob` reads `"••••"` when set |

## Error codes

| Code | Status | Meaning |
|---|---|---|
| `COM-0001` | 400 | malformed body, unknown field, wrong type, malformed legal entity |
| `COM-0500` | 500 | internal error, detail hidden |
| `AUTH-0001` | 401 | missing or malformed tenant |
| `AUTH-0003` | 403 | missing duty |
| `PTY-PROSPECT-NOT-FOUND` | 404 | no such registration |
| `PTY-PROSPECT-STATE` | 409 | the registration was already accepted or rejected |
| `PTY-PROSPECT-LIMIT` | 429 | too many registrations are waiting in the legal entity |
| `PTY-INVITE-INVALID` | 404 | an invitation token is unknown, wrong, expired, revoked or used |
| `PTY-0001` | 422 | tax registration failed format validation; `errors` names the field |
| `PTY-PARTY-INVALID` | 422 | party body failed validation, or `partyType` changed; `errors` names the field |
| `PTY-PARTY-NOT-FOUND` | 404 | unknown party id, another tenant's id, or a malformed id |
| `PTY-VERSION-REQUIRED` | 428 | PATCH without a usable `If-Match` or body `version` |
| `PTY-VERSION-CONFLICT` | 409 | PATCH with a stale version (error class `conflict_version`) |
| `PTY-STATUS-TRANSITION` | 422 | status other than `active` or `inactive`, or the party is merged |
| `PTY-LEGAL-ENTITY-REQUIRED` | 422 | party create needs a caller scoped to exactly one legal entity (the party number is allocated per entity); none or several is refused |
| `PTY-CUSTOMER-NOT-FOUND` | 404 | unknown customer id, another tenant or legal entity, or a malformed id |
| `PTY-CHANGE-NOT-FOUND` | 404 | unknown pending change, another tenant or legal entity, or a malformed id |
| `PTY-CHANGE-PENDING` | 409 | a credit limit change is already awaiting approval for this customer |
| `PTY-CHANGE-DECIDED` | 409 | already decided differently or by another user |
| `PTY-PENDING-STALE` | 409 | the limit changed after the request; only reject remains |
| `PTY-PENDING-SELF-APPROVAL` | 403 | the requester tried to decide their own change |
| `PTY-CREDIT-INVALID` | 422 | `creditLimit` is not a non-negative amount with at most 2 decimals |
| `PTY-REASON-TOO-LONG` | 422 | decision reason exceeds 500 Unicode characters |
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
| `FIELDCRYPT_KEYS`, `FIELDCRYPT_CURRENT_KEY_ID` | empty | `id=base64(32 bytes)` pairs and the current id (`platform/go/fieldcrypt`); the chart reads them optionally from the `existingSecret` secret |
| `PARTY_VIES_URL` | empty | VIES REST base, `https://ec.europa.eu/taxation_customs/vies/rest-api`; when empty VAT numbers stay `unverified` |
| `PARTY_VIES_TIMEOUT_MS` | `5000` | VIES request timeout |
| `PASSAGE_SERVICE_NAME`, `PASSAGE_SERVICE_PUBLIC_KEYS` | empty | party accepts signed service tokens (`caller[:kid]=base64pub,...`) from receiving (`party.read`) and payables (`party.vendor-bank.reveal`); the chart sets them from `serviceIdentity.publicKeys`. Without keys only gateway headers are trusted |

No new Go dependency: `pgx` and `chi` were already required, and the database helpers come from `platform/go/dbx`.

## Metrics

`party_tax_validations_total{status}`, `party_tax_validations_rejected_total`, `party_tax_checker_failures_total`, `party_parties_created_total` (new parties only; an idempotent replay is not counted), `party_credit_limit_changes_total{outcome}` (requested, applied, approved, rejected), and the shared service-token replay counters (`passage_service_token_replays_refused_total`, `passage_service_token_replay_store_failures_total`).

Service tokens: migration `0009_service_token_replay.sql` (identical to `dbx.ServiceTokenReplayDDL`) records each accepted token id so a second replica refuses a replay. With public keys configured the service refuses to start until that table and `service_token_replay_purge()` exist, so migrate before rolling out. Deploy party and set its `publicKeys` before receiving or payables sign calls to it (see `deploy/gateway/README.md`).

VAT numbers are format-checked, then looked up against VIES (`internal/vies`) when `PARTY_VIES_URL` is set: `VALID` is `valid`, `INVALID` is `invalid`, and any other verdict (`MS_UNAVAILABLE`, `TIMEOUT`, a non-200, a body that is not JSON) leaves the number `unverified`. `internal/vies/testdata` holds responses recorded from the live VIES REST service on 2026-10-31.
