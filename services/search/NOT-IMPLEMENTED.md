# NOT-IMPLEMENTED: search (#506 subtask 506-1)

| Item | Source | Status | Reason |
| --- | --- | --- | --- |
| Production orders and workers | #506 506-2 | later slice | Receipts, vendor invoices, sales orders, packing slips and customer invoices (with credit notes) are indexed; production and HR events still need their number, name and legal entity added. |
| Draft and unconfirmed purchase orders | #506 | later slice | Purchasing emits `po.confirmed` and `po.cancelled` only; drafts are not indexed. |
| Backfill of records that existed before the consumer started | #506 | later slice | The index fills from events; a replay of the topics or a one-off export is needed for history. |
| Outbox connectors and topic ACLs for the search topics (`passage.party.party`, `passage.product.product`, `passage.procurement.purchase_order`, `passage.sales.order`, `passage.fulfillment.shipment`, `passage.billing.invoice`) | deploy | infra | `deploy/kafka/connect.yaml` does not publish these topics yet; `consumer.enabled` stays false until it does. |
| Local dev stack wiring (`make dev`) | dev | infra | Run `search` against the dev database by hand. |
| Search benchmarks and typo-tolerance tuning | #506 | later slice | Threshold 0.4 on `word_similarity` is untuned. |
| Indexed name search under row-level security | #506 | later slice | pg_trgm operators are not leakproof, so the planner filters the name arm instead of using the GIN index. Upgrade path: leakproof wrappers created by a superuser, or a tenant-partitioned table. |
| Substring match inside a number | #506 | later slice | A number-shaped query matches by prefix only (`1042` does not find `PO-1042`). |
