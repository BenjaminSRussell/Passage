# NOT-IMPLEMENTED: prediction-py (PSG-47, FR-47.15)

Built: the model, the promotion gate, scoring, the stale rule, the three routes, tenant isolation,
tests, image and chart. The rows below are not built.

| Deliverable | Status | Reason |
|---|---|---|
| Persistence of `prediction_model` and `payment_prediction` (9.47 data model) | later slice of PSG-47 | `Repository` is a port with an in-memory implementation; a restart loses models and predictions. The Postgres implementation (tenant RLS) and its migration come with the owning database |
| Pulling paid and open invoices from receivables | later slice of PSG-47 | The routes take the invoices in the body. The weekly job and the receivables export that feed them (and the 10 minute scoring window for 1M open invoices) need receivables and the scheduler |
| Weekly retrain job and recompute on dunning, dispute or partial-payment events (BR-47.12) | later slice of PSG-47 | Needs the scheduler and the receivables events |
| `dev.passage.prediction.payment.scored.v1` event | later slice of PSG-47 | Needs the outbox and its relay |
| Gateway route for `GET /api/v1/ar/invoices/{id}/prediction` and the collections agent tool `passage.ar.invoice.prediction.get` | handoff | The route and the duty are seeded in policies/authz; routing it to this service and annotating the OpenAPI with `x-passage-tool` is the owners of receivables and the gateway |
| Per-tenant `prediction.payment.*` parameters from the settings service | handoff | Config is read from the environment (service-wide), not per tenant |
| Prediction accuracy operational report, p_late calibration | later slice of PSG-47 | Needs persisted predictions and actuals |
| `dso_days` forecast tile and collections workspace consumption | handoff | Owned by treasury forecast and receivables collections |
| An AGT-0213 heuristic fallback display | handoff | The UI shows the heuristic; this service answers 422 AGT-0213 |
