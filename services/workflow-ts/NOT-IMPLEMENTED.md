# Not implemented in workflow-ts

| Deliverable | Status | Reason |
|---|---|---|
| Payment run workflow (#102) | built | `src/paymentRunWorkflow.ts`, `src/paymentRunActivities.ts`, `src/startPaymentRun.ts`; crash and re-run proof in `test/paymentRun.test.ts` |
| Trigger that starts `paymentRunWorkflow` | not built | no API route or event calls `startPaymentRunWorkflow`; the caller supplies only tenant, legal entity, proposal and the starting user |
| Test against a live payables and Postgres | not built | the workflow tests run on the SDK time-skipping server against a stateful fake of the payables contract; the Go tests in `services/payables` cover the endpoints against Postgres |
| Service identity for the party calls | #226 | the party activity still sends header identity and a hardcoded duty; the payment run activities use a signed service token (#303) |
| Compensation when settlement fails after the journal posted | not built | the workflow retries settlement forward; a reversing journal needs a payables reversal endpoint |
| Live Kafka consumption of party change events | #223, #106 | see README |
