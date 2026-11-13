# prediction-py (wave 47, PSG-47, FR-47.15)

Payment-date prediction per tenant. Numeric work only, per the Python rules in the engineering
conventions. Pure Python: a small gradient-boosted tree implementation in the standard library
(`src/prediction/gbt.py`), no numpy or scikit-learn.

- **Features** (`domain.py`): terms days, log amount, invoice day of month, dunning count, disputes,
  due month (seasonality) and the customer's mean days late (leave-one-out while training).
- **Heads:** squared-loss regression on days late (gives `predicted_payment_date` = due date + days)
  and a logistic head for `p_late_30d` (paid more than 30 days after the due date), written as
  numeric(5,4) text.
- **Retrain** (`train`): below `min_rows` paid invoices (default 500, 100 to 10,000) there is no model,
  AGT-0213. The MAE is measured on a held-out fifth the fitting never saw, then the model is refit on
  all rows. MAE within `mae_max_days` (default 6, 1 to 30) is ACTIVE and retires the previous ACTIVE
  model; above it the model stays CANDIDATE and the previous one keeps serving (AC-47.12).
- **Scoring** (`score`): `predicted_payment_date`, `p_late_30d`, `features_hash`, `model_version`,
  `scored_at`. `is_stale`: older than 7 days is stale (BR-47.12). Deterministic: same data, same model.

## API

Identity from the edge headers `x-passage-tenant`, `x-passage-user`, `x-passage-duties` (401 when
missing, malformed or the nil tenant); the tenant is never read from a body.

| Route | Duty | Notes |
|---|---|---|
| `POST /api/v1/predictions/payment/retrain` | `agents.admin` | body `paidInvoices` (up to 50,000); 201 with the model (status, rows, maeDays); 422 AGT-0213 below min_rows |
| `POST /api/v1/predictions/payment/score` | `agents.admin` | body `openInvoices` (1 to 5,000); 422 AGT-0213 without an ACTIVE model |
| `GET /api/v1/ar/invoices/{id}/prediction` | `collections.view` | the stored prediction with `stale` |
| `GET`/`PUT /api/v1/predictions/payment/settings` | `agents.admin` | the tenant's `minRows` and `maeMaxDays` (9.47.20); a tenant without its own uses the service defaults; the next retrain uses them |
| `GET /api/v1/predictions/payment/accuracy` | `agents.admin` | accuracy report over predictions whose invoices were since paid: MAE days, share within 7 days, `p_late_30d` calibration per bucket, overall and per model version |

OpenAPI: `api/openapi.yaml`. Event: `events/asyncapi.yaml`.

## Persistence

`src/prediction/store.py` (`PgRepository`) over `migrations/0001_prediction.sql`: `prediction_model` (the trees
and customer statistics as a JSON artifact, at most one ACTIVE per tenant), `payment_prediction` (latest per
invoice; `actual_paid_date` is stamped when the invoice turns up paid in a retrain), `prediction_settings` and
`outbox`. Every table has tenant row-level security (forced); each call runs in its own transaction with
`passage.tenant_id` set. Scoring writes one `dev.passage.prediction.payment.scored.v1` outbox row per invoice in
the same transaction. A retrain holds a per-tenant advisory lock while it swaps the ACTIVE model.

Env: `DATABASE_URL` (role `passage_app`; without it the process keeps everything in memory, for tests and local
runs), `MIGRATE_ON_START` (`true` runs the migrations as the owner role at start), `PREDICTION_MIN_ROWS`,
`PREDICTION_MAE_MAX_DAYS` (service defaults).

## Run and test

```
cd services/prediction-py
python -m venv .venv && . .venv/bin/activate
pip install -e ".[dev]"
pytest                      # tests/test_core.py needs only the standard library
uvicorn prediction.api:app --port 8080
```

`tests/test_api.py` needs the dev extras (fastapi, httpx) and skips without them. `tests/test_store.py` runs the
repository contract against the in-memory repository and, with `PASSAGE_TEST_DATABASE_URL` (owner) and
`PASSAGE_TEST_APP_DATABASE_URL` (`passage_app`) set, against Postgres in a private schema.
`prediction.synth` generates a deterministic synthetic receivables history (fictional Cedar Ridge Pumps
customers); the AC-47.12 test trains on 2,000 of its paid invoices.
