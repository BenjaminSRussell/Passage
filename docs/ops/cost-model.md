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
raises COST-1901 and the query version is reviewed. Reconciliation is monthly: sum of tenant costs
versus the cluster invoice.

## Statements
Monthly statement per tenant (PDF, CSV) with resource lines, USD and EUR, and `model_version`. Billing
records are kept 7 years.
