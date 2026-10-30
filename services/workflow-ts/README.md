# Workflow service

Wave 04 Temporal worker and event-schema compatibility gate.

## Approval workflow

`approvalWorkflow` accepts a configuration-driven list of approval levels. Each
level has an approver, an escalation approver, and an SLA timer. Signals approve,
reject, or delegate the current task. Workflow IDs are deterministic:
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

Not built: live consumption. No Kafka client is wired; waits on #223 and #106.
The call to party sends header identity plus `party.approve`; a real service
identity waits on #226. Approval levels are passed in by the caller.

## Payment run workflow

`paymentRunWorkflow` (`src/paymentRunWorkflow.ts`) executes an approved payables proposal: `startPaymentRun` reserves it (proposal EXECUTING, lines re-verified), `generatePaymentFile` (pain.001, five attempts), `postPaymentJournal` (ledger, idempotency key per run), `settlePaymentRun` and `completePaymentRun`. Input: tenant, legal entity, proposal and the user who starts the run (`executedBy`); the ledger, accounts, paying bank and payee bank details all live server-side in payables, so the workflow carries none. Workflow id `payment-run:<legal entity>:<proposal>`; `startPaymentRunWorkflow` (`src/startPaymentRun.ts`) uses `REJECT_DUPLICATE`.

The activities (`src/paymentRunActivities.ts`) call payables with a short-lived Ed25519 service token (`src/serviceToken.ts`, the #303 format: `base64url(claims).base64url(signature)` over `passage.service-token.v1.` plus the body) minted per call as `workflow-ts`, audience `payables`, the run's tenant and legal entity, `act` = `executedBy` and the single duty `payment-run.execute` (the grant in `policies/authz/data.json` and `platform/go/tenant/service.go`). `platform/go/tenant/testdata/workflow_ts_token_vector.json` holds a token this code minted that the Go verifier accepts; the test reproduces it byte for byte. Every endpoint is idempotent, so a retry after a lost reply or a killed worker repeats a step without a second effect. A terminal rejection or exhausted file attempts before the journal call `failPaymentRun`, which fails the run and releases the proposal's lines; once the journal was requested payables refuses to fail the run and the workflow only retries forward. 4xx other than 408 and 429 are non-retryable (`PaymentRunRejected`); a ledger 409 in-progress reaches the activity as a retryable 502. The failure reason is cut to 500 bytes (UTF-8 aware) to match payables' limit.

Configuration: `PAYABLES_BASE_URL` (default `http://payables.passage.svc`), `PASSAGE_SERVICE_SIGNING_KEY_FILE` (or `PASSAGE_SERVICE_SIGNING_KEY`, a base64 32-byte seed; without one the worker logs a warning, registers only the party approval activity and keeps running) and `PASSAGE_SERVICE_KEY_ID`. Payables must list the public key as `PASSAGE_SERVICE_PUBLIC_KEYS=workflow-ts[:kid]=<base64>`. Not built: a trigger that starts the workflow (no API or event calls `startPaymentRunWorkflow` yet).

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
