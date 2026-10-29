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
