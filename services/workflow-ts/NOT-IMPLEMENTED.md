# Not implemented in workflow-ts

| Deliverable | Status | Reason |
|---|---|---|
| Payment run workflow (#102) | built | `src/paymentRunWorkflow.ts`, `src/paymentRunActivities.ts`, `src/startPaymentRun.ts`; crash and re-run proof in `test/paymentRun.test.ts` |
| ECO approval workflow (wave 09 AC3) | built | `src/ecoApprovalWorkflow.ts`, `src/ecoActivities.ts`; approve, reject, timeout, scheduled release and lost-reply proof in `test/ecoApproval.test.ts` |
| CAPA workflow (wave 33 AC4) | built | `src/capaWorkflow.ts`, `src/capaActivities.ts`; effectiveness check, reopen, escalation and stall proof in `test/capa.test.ts` |
| Trigger that starts `ecoApprovalWorkflow` and `capaWorkflow` | not built | nothing starts them on `eco.submitted` or an approved nonconformance yet; the caller supplies the approver, timeouts and (for CAPA) the starting user |
| Gateway route and OPA row for `/v1/workflows/eco-approvals/*` and `/v1/workflows/capa/*` | not built | the decision routes exist in the worker (`src/decisionApi.ts`) but the gateway does not route to them yet, and they check the duties themselves rather than through the gateway policy |
| Delegation or escalation of the ECO approver | not built | any user with `engineering.eco.approve` other than the creator may decide; a missed decision cancels the ECO as `system:eco-timeout` instead of escalating |
| Authenticated actor on `approvalWorkflow` (party credit-limit changes) | not built, known weakness | its `approve`, `reject` and `delegate` signals still trust the actor in the payload (TODO #226) and nothing authenticated sends them; the same fix applies: a decision route that sets the actor from the principal and a workflow that decides nothing from it. `paymentRunWorkflow` takes no signals; its `executedBy` is supplied at start |
| Temporal frontend access beyond the NetworkPolicy | not built | no mTLS or Temporal authorizer; the policy limits the network path only |
| Test against live engineering and quality | not built | the workflow tests run on the SDK time-skipping server against stateful fakes of the two contracts; the Go tests in `services/engineering` and `services/quality` cover the endpoints |
| Trigger that starts `paymentRunWorkflow` | not built | no API route or event calls `startPaymentRunWorkflow`; the caller supplies only tenant, legal entity, proposal and the starting user |
| Test against a live payables and Postgres | not built | the workflow tests run on the SDK time-skipping server against a stateful fake of the payables contract; the Go tests in `services/payables` cover the endpoints against Postgres |
| Service identity for the party calls | #226 | the party activity still sends header identity and a hardcoded duty; the payment run activities use a signed service token (#303) |
| Compensation when settlement fails after the journal posted | not built | the workflow retries settlement forward; a reversing journal needs a payables reversal endpoint |
| Live Kafka consumption of party change events | #223, #106 | see README |
