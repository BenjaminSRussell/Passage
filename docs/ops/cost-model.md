# Tenant cost model (FR-50.9, BR-50.10)

Monthly metering per tenant, stored in `tenant_cost` (PK tenant_id, month). Computed from Prometheus
data with a stored query version, so a restated month is reproducible.

## Metered dimensions
| Field | Unit | Source |
|---|---|---|
| cpu_core_hours | core-hours attributed to the tenant's pods | container CPU by tenant label |
| memory_gb_hours | GiB-hours | container memory by tenant label |
| storage_gb | GB-month | CNPG volume usage per tenant schema |
| kafka_gb | GB produced, retained | topic bytes by tenant prefix |
| lakehouse_gb | GB-month | object store prefix per tenant |
| ai_tokens | tokens | model-serving metering |

## Cost
`cost_usd = sum(unit_rate[dim] x quantity[dim]) x (1 + overhead)`. Unit rates come from the published
capacity model for the provider. Overhead covers shared control-plane cost [ASSUMED 12%].
Rounding: USD and EUR to 0.01 (BR-50.10). EUR uses the ECB month-average rate (`cost.fx_source=ECB_MONTH_AVG`).

## Accuracy
AC-50.7: the meter is within 5% of provider billing for the cluster over one month. Variance above 5%
raises COST-1901 and the query version is reviewed ([cost-meter-drift](runbooks/cost-meter-drift.md)).
Reconciliation is monthly: sum of tenant costs versus the cluster invoice.

Calculator: `python3 docs/ops/tools/cost_meter.py --usage <tenant>.json ... --rates rates.json --usd-per-eur <ECB>
--month YYYY-MM --billed-usd <cluster invoice>` prints one statement per tenant and the variance; pass every
tenant on the cluster. The monthly job that reads Prometheus and writes `tenant_cost` is the ops service's.

## Statements
Monthly statement per tenant (PDF, CSV) with resource lines, USD and EUR, and `model_version`. Billing
records are kept 7 years.
