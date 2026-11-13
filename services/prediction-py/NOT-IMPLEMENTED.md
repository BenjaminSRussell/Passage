# NOT-IMPLEMENTED: prediction-py (PSG-47, FR-47.15)

Built: the model, the promotion gate, scoring, the stale rule, the routes (retrain, score, read, per-tenant
settings, accuracy report), Postgres persistence under tenant RLS, the `prediction.payment.scored` outbox rows,
tenant isolation, tests, image and chart. The rows below are not built.

| Deliverable | Status | Reason |
|---|---|---|
| Pulling paid and open invoices from receivables | later slice of PSG-47 | The routes take the invoices in the body. The weekly job and the receivables export that feed them (and the 10 minute scoring window for 1M open invoices) need receivables and the scheduler |
| Weekly retrain job and recompute on dunning, dispute or partial-payment events (BR-47.12) | later slice of PSG-47 | Needs the scheduler and the receivables events |
| Relay of the outbox to the broker (Debezium connector and its `debezium_cdc_prediction` role) | later slice of PSG-47 | Scoring writes `dev.passage.prediction.payment.scored.v1` to the outbox in the scoring transaction; the connector that publishes it is deployment work |
| Gateway route for `GET /api/v1/ar/invoices/{id}/prediction` and the collections agent tool `passage.ar.invoice.prediction.get` | handoff | The route and the duty are seeded in policies/authz; routing it to this service and annotating the OpenAPI with `x-passage-tool` is the owners of receivables and the gateway |
| Prediction accuracy screen and a scheduled accuracy job | later slice of PSG-47 | The report is served (`GET /api/v1/predictions/payment/accuracy`); outcomes are recorded when paid invoices arrive in a retrain, so the report fills only as fast as the weekly retrain runs |
| `dso_days` forecast tile and collections workspace consumption | handoff | Owned by treasury forecast and receivables collections |
| An AGT-0213 heuristic fallback display | handoff | The UI shows the heuristic; this service answers 422 AGT-0213 |
