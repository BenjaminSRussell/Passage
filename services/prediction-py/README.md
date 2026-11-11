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

Env: `PREDICTION_MIN_ROWS`, `PREDICTION_MAE_MAX_DAYS`.

## Run and test

```
cd services/prediction-py
python -m venv .venv && . .venv/bin/activate
pip install -e ".[dev]"
pytest                      # tests/test_core.py needs only the standard library
uvicorn prediction.api:app --port 8080
```

`tests/test_api.py` needs the dev extras (fastapi, httpx) and skips without them.
`prediction.synth` generates a deterministic synthetic receivables history (fictional Cedar Ridge Pumps
customers); the AC-47.12 test trains on 2,000 of its paid invoices.
