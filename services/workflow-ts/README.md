# Workflow service

Wave 04 Temporal worker and event-schema compatibility gate.

## Approval workflow

`approvalWorkflow` accepts a configuration-driven list of approval levels. Each
level has an approver, an escalation approver, and an SLA timer. One signal,
`approval-decision` `{ action: approve|reject|delegate, actor, scope, delegate? }`,
approves, rejects, or delegates the current task; it counts only when `actor` is the
current approver and not the requester, and for a party change only when `scope`
contains the change's legal entity. Only the decision route below sends it, with the
actor and scope of the authenticated caller. Workflow IDs are deterministic:
`approval:<request-id>`.

The workflow deliberately consumes plain `ApprovalInput` rather than importing
Wave 03 metadata code. A future metadata adapter only needs to map a decision
table result to that interface.

## Credit-limit change consumer

`handleChangeRequested` (`src/changeRequested.ts`) maps a party
`dev.passage.party.change.requested.v1` event to `approvalWorkflow` with
`requestId` = `change_id` (workflow id `approval:<change_id>`), `entityType`
`credit_limit`, the event tenant, legal entity and requester. Starts use
`REJECT_DUPLICATE`, so a redelivered event never starts a second workflow, even
after the first completed. When the workflow ends, the `decidePartyChange`
activity (`src/activities.ts`) calls party `:approve` or `:reject` as the
deciding user through an injected `PartyClient`. The requester can never approve,
reject, or be delegated to (BR-7.8); party enforces the same rule.

The call to party carries a `workflow-ts` service token for audience `party` with
the grant `party.approve` (#426), `act` = the approver the decision route
authenticated and the change's legal entity, minted only when that entity is in the
approver's scope; party records the actor as the decider and refuses the requester.
A refused token (401, or 403 with an `AUTH-` code) is retried; any other 4xx is
final. Party must list the public key in `PASSAGE_SERVICE_PUBLIC_KEYS` (chart
`serviceIdentity.publicKeys`); `PARTY_BASE_URL` is the chart's `partyUrl`.

`load.released.v1` is accepted only from `/passage/tms/transport`; its contract (`events/asyncapi.yaml`, `schemas/current/tms.load.released.v1.json`) requires transport to fill `releasedBy` and `legalEntityScope` from the gateway-verified principal of the release, never from the request body, because the tender's `scoped=true` token is minted from them. Not built: live consumption. No Kafka client is wired; waits on #223 and #106.
Approval levels are passed in by the caller.

## ECO approval workflow

`ecoApprovalWorkflow` (`src/ecoApprovalWorkflow.ts`, activities in `src/ecoActivities.ts`) gives a submitted engineering change order an approver step. Input: tenant, legal entity, `ecoId`, `requester` (the ECO's creator, supplied by the starter) and `approvalTimeoutMs`; workflow id `eco-approval:<ecoId>`.

Who decides is not decided here. A person decides through the decision route below, which authenticates them, checks the `engineering.eco.approve` duty and that they are not the creator, and only then sends the `eco-decision` signal `{ decision, actor, reason? }` with `actor` taken from the request principal. The workflow takes no authorization decision from that payload: it passes the actor to engineering as the `act` of a service token and engineering re-checks (`ENG-ECO-SELF-APPROVAL` for the creator, 409 for a state that cannot be decided). A decision engineering refuses is counted in `refusedDecisions` and the workflow keeps waiting. Approve calls `:approve` then `:release`, so the old BOM version expires and the new one goes live in engineering's release transaction; a future release date makes engineering answer `scheduled`, and the workflow sleeps to that date and releases again. Reject calls `:reject` with the optional reason. No accepted decision before the timeout calls `:cancel` as `system:eco-timeout` with a reason and ends `timed_out`. A release engineering refuses (for example `ENG-ECO-DRYRUN`) ends `release_failed` with the ECO left approved and counts `passage_eco_release_failed`. Every call repeats safely. The `eco-approval-state` query reports status, tenant and requester.

The calls carry a `workflow-ts` token for audience `engineering` with the single service-only duty `engineering.eco.workflow` (grant in `policies/authz/data.json` and `platform/go/tenant/service.go`); the human `engineering.eco.approve` duty cannot call `:approve`, `:reject` or `:cancel`. Configuration: `ENGINEERING_BASE_URL` (default `http://engineering.passage.svc:8080`); engineering must list the public key in `PASSAGE_SERVICE_PUBLIC_KEYS`.

## CAPA workflow

`capaWorkflow` (`src/capaWorkflow.ts`, activities in `src/capaActivities.ts`) drives the 8D correction of an approved nonconformance in services/quality. Input: tenant, legal entity, `ncrId`, `startedBy`, optional `effectivenessCheckDays`, `stepTimeoutMs`, `effectivenessTimeoutMs` and `maxReopens`; workflow id `capa:<ncrId>`. It starts the correction, then applies each `capa-step-done` signal (`{ step, notes, actor }`) for the current step, in order. After D7 it sleeps until the service's `effectivenessDueAt`, then opens the check (`awaiting_effectiveness`): a `capa-effectiveness` signal (`{ result, note, actor }`) that arrives before then, or while no check is open, is dropped, and the queue is cleared when a round ends. EFFECTIVE closes the NCR (`closed`); NOT_EFFECTIVE reopens at D4 and the loop repeats until `maxReopens` is exceeded (`escalated`). A step or check that never arrives ends `stalled` (`stalledAt`) and counts `passage_capa_stalled`. As with the ECO, the actor comes from the authenticated decision route and the workflow decides nothing from it: quality re-checks the segregation of duties (`QM-0175`, the D7 implementer cannot make the check) and a refused check is counted in `refusedChecks` while the workflow keeps waiting. Each activity reads the NCR first, sends its ETag as `If-Match` and skips a write the service already applied, so retries, lost replies and a stale ETag (412, retried) are safe; `QM-0174` (not due yet) is retried and any other 4xx fails the activity. The `capa-state` query reports status, current step, reopen count and the D7 implementer.

The calls carry a `workflow-ts` token for audience `quality` with the single service-only duty `quality.capa.execute`, which reaches `GET /v1/quality/nonconformances/{id}`, `POST .../corrections`, `:complete-step` and `:check-effectiveness` and nothing else. Configuration: `QUALITY_BASE_URL` (default `http://quality.passage.svc:8080`); quality must list the public key in `PASSAGE_SERVICE_PUBLIC_KEYS`.

## Tender workflow

`tenderWorkflow` (`src/tenderWorkflow.ts`, activities in `src/tenderActivities.ts`) drives the carrier tender cascade of one load in services/transport; workflow id `tender:<tenantId>:<loadId>`. Input: tenant, legal entity, `legalEntityScope` (the releasing user's legal entities, which must include the load's), `loadId`, `requestedBy`, the carriers in rank order (optional `rateQuoted`), `currency` and `timeoutMin`; invalid input fails the workflow at once (non-retryable `InvalidTenderInput`). It starts the tender (`POST /v1/transport/loads/{id}:tender`, `Idempotency-Key` from the first execution run id, so a retry after a lost reply or a reset run returns the same tender; a new run for a load whose tender is still open, for example one a planner started, gets 409 `TMS-0114` and adopts that tender from `GET /v1/transport/tenders?loadId=` instead of failing), then sleeps until the pending attempt's `pendingExpiresAt` plus a grace. On wake, or on a `tender-response` signal, it reads the tender; a signal that arrives while a read is in flight is kept for the next wait. An overdue attempt triggers the expiry sweep `POST /v1/transport/tenders:expire`, and transport itself moves to the next carrier; a decline cascades in transport at response time. The start, a sweep or a read that runs out of its one-hour activity window (transport down) is retried five minutes later (`OUTAGE_RETRY_MS`) and does not fail the workflow; after seven days of workflow time without a successful call (`MAX_OUTAGE_MS`) it ends `stalled`. The workflow ends `accepted` when a carrier accepts, `escalated` when the tender fails (every carrier declined or timed out; transport marks `plannerTask`) and counts `passage_tender_escalated`, or `stalled` (`passage_tender_stalled` with `reason` `sweep_stuck` or `transport_unreachable`) after five sweeps that do not advance an overdue attempt or the outage above. The `tender-state` query reports the pending carrier and the carriers tried.

The calls carry a `workflow-ts` token for audience `transport` with the single service-only duty `transport.tender.execute` (accepted only from the `workflow-ts` issuer), which reaches `POST .../loads/{id}:tender`, `GET .../tenders/{id}`, `GET .../tenders?loadId=` (the open-tender lookup; transport refuses the list without `loadId` for this duty) and `POST .../tenders:expire` and nothing else (the human `TMS_TENDER` duty still works on those routes). Configuration: `TRANSPORT_BASE_URL` (default `http://transport.passage.svc:8080`, set from the chart's `transportUrl`); transport must list the public key in `PASSAGE_SERVICE_PUBLIC_KEYS` (chart value `serviceIdentity.publicKeys`).

`src/tenderTriggers.ts` is the starter, shaped like `handleChangeRequested`: `handleLoadReleased` maps a `dev.passage.tms.load.released.v1` event (camelCase like transport's events: `loadId`, `legalEntityId`, `releasedBy`, `legalEntityScope`, `carriers[{carrierId, rateQuoted?}]`, `currency?`, `timeoutMin?`) to `tenderWorkflow` with `ALLOW_DUPLICATE` (a load still being tendered is reported as `started: false`; a re-release after the tender closed starts a new run, and the start's idempotency key follows the first execution run so a reset replays the same tender but a new run gets a new one), and `handleTenderOutcome` sends the `tender-response` signal to `tender:<tenantId>:<loadId>` (from the event's own tenant, so one tenant cannot signal another's tender) when transport publishes `tender.accepted.v1` or `tender.declined.v1` (no running workflow is ignored; `tender.expired.v1` is the workflow's own sweep). Not built: live consumption (no Kafka client is wired, #223 and #106) and the producer of `load.released.v1` (transport has no load release step yet).

## Decision routes

`src/decisionApi.ts` is the only way a person's decision reaches the ECO, CAPA and party-change approval workflows; the worker serves it on `HTTP_PORT` (default 8080, Service `workflow`). It reads the caller from the gateway-set headers like every service (`X-Passage-Tenant`, `X-Passage-User`, `X-Passage-Duties`, `X-Passage-Legal-Entity`) and refuses a service token. The gateway is the only source of those headers: `deploy/gateway/httproutes.yaml` routes `/v1/workflows` and `/api/v1/workflows` to Service `workflows` behind the `passage-api-jwt` SecurityPolicy, the `ClientTrafficPolicy` strips client-supplied `X-Passage-*` headers, and the chart's NetworkPolicy lets only the `passage-api` gateway pods reach port 8080 (the Temporal worker makes outbound calls only, so the port serves nothing else). The caller's legal-entity scope must contain the workflow's legal entity (403 otherwise), and the scope travels in the signal so the token signer can refuse to mint for an entity outside it, as Go `Mint` does. Routes: `POST /v1/workflows/eco-approvals/{ecoId}:approve|:reject` (duty `engineering.eco.approve`, not the creator, body `{ reason? }` on reject) and `POST /v1/workflows/capa/{ncrId}:step-done` (`{ step, notes }`) and `:effectiveness` (`{ result, note }`) (duty `QM_CAPA_MAINTAIN`; the effectiveness check also needs a user other than the D7 implementer and an open check) and `POST /v1/workflows/party-changes/{changeId}:approve|:reject|:delegate` (duty `party.approve`, only the level's current approver and never the requester, body `{ delegate }` on delegate; an `approval:` workflow that is not a party change is 404) and `POST /v1/workflows/party-merges/{mergeRequestId}:approve|:reject|:delegate` (#450: duty `party.merge.approve` plus `tenant.administer` and no legal-entity claim, never the requester, and never a holder of `payment-proposal.approve` or `payment-run.execute`, 403 `PTY-MERGE-SOD`; workflow id `approval:party-merge:<id>`). The merge activity `decidePartyMerge` calls party `merge-requests/{id}:approve|:reject` with a tenant-level token: duty `party.merge.execute` only and no `le` claim, which the Go verifier accepts only for tenant-level duties (`tenantLevelDuties` in `platform/go/tenant/service.go`); a stale merge (`PTY-MERGE-STALE`) is rejected instead. Each route queries the workflow, requires the workflow's tenant to equal the caller's, rejects a state that cannot take the decision (409), sets `actor` from the principal and ignores any actor in the body, then signals.

Temporal itself has no per-user authentication, so the signal is only as trustworthy as who can reach the Temporal frontend. `deploy/temporal-postgres/templates/temporal-frontend-networkpolicy.yaml` (synced into the `temporal` namespace) allows port 7233 only from the Temporal pods and from pods in the `passage` namespace labelled `passage.dev/temporal-client: "true"`, which the workflow chart sets on the worker pods.

## Payment run workflow

`paymentRunWorkflow` (`src/paymentRunWorkflow.ts`) executes an approved payables proposal: `startPaymentRun` reserves it (proposal EXECUTING, lines re-verified), `generatePaymentFile` (pain.001, five attempts), `postPaymentJournal` (ledger, idempotency key per run), `settlePaymentRun` and `completePaymentRun`. Input: tenant, legal entity, proposal and the user who starts the run (`executedBy`); the ledger, accounts, paying bank and payee bank details all live server-side in payables, so the workflow carries none. Workflow id `payment-run:<legal entity>:<proposal>`; `startPaymentRunWorkflow` (`src/startPaymentRun.ts`) uses `REJECT_DUPLICATE`.

The activities (`src/paymentRunActivities.ts`) call payables with a short-lived Ed25519 service token (`src/serviceToken.ts`, the #303 format: `base64url(claims).base64url(signature)` over `passage.service-token.v1.` plus the body) minted per call as `workflow-ts`, audience `payables`, the run's tenant and legal entity, `act` = `executedBy` and the single duty `payment-run.execute` (the grant in `policies/authz/data.json` and `platform/go/tenant/service.go`). `platform/go/tenant/testdata/workflow_ts_token_vector.json` holds a token this code minted that the Go verifier accepts; the test reproduces it byte for byte. Every endpoint is idempotent, so a retry after a lost reply or a killed worker repeats a step without a second effect. A terminal rejection or exhausted file attempts before the journal call `failPaymentRun`, which fails the run and releases the proposal's lines; once the journal was requested payables refuses to fail the run and the workflow only retries forward. 4xx other than 408 and 429 are non-retryable (`PaymentRunRejected`); a ledger 409 in-progress reaches the activity as a retryable 502. The failure reason is cut to 500 bytes (UTF-8 aware) to match payables' limit.

Retry budget: 401 and authorization 403 are retryable because a rotated key or a late deploy clears them, but before the journal was requested (`startPaymentRun`, `failPaymentRun`) an auth fault is retried for at most `PRE_JOURNAL_AUTH_BUDGET_MS` (15 minutes from the activity's first scheduling) and then fails terminally, so a mis-keyed worker surfaces in minutes, not after the 24 hour budget (and not twice, once for the step and once for `failPaymentRun`). `generatePaymentFile` runs under the bounded proxy (five attempts, backoff from 1 second to 1 minute, no schedule-to-close window), so it gives up after those attempts and the 15 minute auth budget never applies to it. `failPaymentRun` after a ledger rejection (`AP-RUN-014`, a terminal 422 from `postPaymentJournal`) is a post-request call: payables accepts it because it recorded the rejection (`journal_rejected_at`) and nothing posted, which is the only case where `:fail` is allowed once the journal was requested; it still runs under the 15 minute pre-journal auth budget, so a key fault there fails the activity and counts `passage_payment_run_stuck`. Server faults (5xx, 408, 429) and every post-journal step (`postPaymentJournal`, `settlePaymentRun`, `completePaymentRun`) keep retrying forward for 24 hours, because failing the run there is refused by payables. `needs an identified user` is `AP-RUN-016`, a business 403, so it is terminal and not an auth fault.

Alerts: page on `passage_payment_run_stuck` above 0 (a run that could not finish after its journal was requested, or whose `failPaymentRun` failed; it is emitted from the workflow, so it appears only after the 24 hour post-journal budget or a 15 minute pre-journal auth budget plus the failing `failPaymentRun`), on `payables_payment_run_stuck_total` increasing in payables, and on Temporal activity failures for the `paymentRunWorkflow` task queue (`temporal_activity_execution_failed` or the namespace's failed-activity metric, filtered to activity types `startPaymentRun`, `generatePaymentFile`, `postPaymentJournal`, `settlePaymentRun`, `completePaymentRun`, `failPaymentRun`) sustained for more than 15 minutes, which is the early signal for an auth or key fault. A workflow that ends failed with its run still RUNNING needs manual action in payables.

Configuration: `PAYABLES_BASE_URL` (default `http://payables.passage.svc:8080`), `PASSAGE_SERVICE_SIGNING_KEY_FILE` (or `PASSAGE_SERVICE_SIGNING_KEY`, a base64 32-byte seed; the mounted file wins when both are set, as in the shared Go loader; without one the worker logs a warning, registers no activities and keeps running) and `PASSAGE_SERVICE_KEY_ID`. Payables must list the public key as `PASSAGE_SERVICE_PUBLIC_KEYS=workflow-ts[:kid]=<base64>`. Not built: a trigger that starts the workflow (no API or event calls `startPaymentRunWorkflow` yet).

## Schema compatibility

`npm run check:schemas` compares `schemas/current` with the committed baseline
and rejects type changes, new required fields, removed closed-schema properties,
and narrowed enums. The checked metadata event contract is described by
`events/asyncapi.yaml`; Apicurio is configured for `BACKWARD_TRANSITIVE`
compatibility in `deploy/kafka/registry.yaml`.

## Run and test

```sh
npm ci
npm test -- --run
npm run typecheck
npm run build
TEMPORAL_ADDRESS=localhost:7233 npm start
```
