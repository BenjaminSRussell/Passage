# Runbook: tenant cost meter drift (COST-1901)

Alert source: the monthly cost meter variance check (AC-50.7). The meter must be within 5% of provider billing
for the cluster over one month.
Message: COST-1901 "Metered cost for {month} differs from provider billing by {pct}%."
Severity: ticket (warning). Owner: ops service on-call, with the finance contact for the billing side.

## Detect
- `tools/cost_meter.py` run with every tenant of the cluster (`--usage` once per tenant) and `--billed-usd`
  exits 1 and prints `variance.code = COST-1901`. The comparison is the sum of tenant costs against the cluster
  invoice; a run that leaves out a tenant under-meters and raises COST-1901 for that reason alone.
- The monthly cost meter job (ops service, not built yet) flags the same variance.

## Triage
1. Confirm the month and cluster, and that every tenant on the cluster is in the run. Restated months must use the
   stored query version (cost-model.md).
2. Check that the inputs are complete. A missing dimension is an error by design (`cost_meter` refuses a missing
   dimension), so a meter that ran must have all six dimensions.
3. Compare each dimension with the provider billing line by line:
   - `cpu_core_hours` and `memory_gb_hours`: container metrics missing for pods that were rescheduled. Check
     for gaps in the Prometheus series over the month.
   - `storage_gb`: it is GB-month, the average over the month. A single sample (first or last day) is wrong for
     a tenant whose volume grew or shrank during the month.
   - `kafka_gb`: retained bytes counted before tiered storage moved data; tiered bytes are billed differently.
   - `lakehouse_gb`: object-store prefixes that include another tenant's shared objects.
   - `ai_tokens`: compare with the model provider's own usage report; cached or batch tokens billed at a
     different price than the single `ai_tokens` rate are a known source of drift.
4. Check the overhead factor (12% by default) and the rates version used against the published capacity model.
5. Check the FX rate source. EUR figures use the ECB month average, not a spot rate.

## Fix
- Correct the query, bump the query version, and recompute the month. Never overwrite a statement already sent to
  a customer; issue a corrected statement referencing the old one.
- If the drift is provider-side (billing adjustment, credit, reserved-instance change), record it as a reconciling
  item rather than changing the meter.
- If the drift is within 5% after recomputation, close COST-1901 and note the residual.

## Evidence
Keep the variance output (`meteredUsd`, `billedUsd`, `pct`, `modelVersion`) and the per-tenant statements with
the month close record. Billing
records are kept for 7 years (cost-model.md).
