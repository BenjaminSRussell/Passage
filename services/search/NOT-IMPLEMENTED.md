# NOT-IMPLEMENTED: search (#506)

| Item | Source | Status | Reason |
| --- | --- | --- | --- |
| Worker and production order changes after the first event | #506 | later slice | HR emits no event when a worker is renumbered or ends, so a worker row never changes. A production order's status follows `order.statuschanged`; `order.updated` (quantity, delivery date) is not read, so the record shows number and status only. |
| Draft and unconfirmed purchase orders | #506 | later slice | Purchasing emits `po.confirmed` and `po.cancelled` only; drafts are not indexed. |
| Backfill of records that existed before the consumer started | #506 | later slice | The index fills from events; a replay of the topics or a one-off export is needed for history. |
| Outbox connectors for the party, product, procurement, production and HR topics (`passage.party.party`, `passage.product.product`, `passage.procurement.purchase_order`, `passage.production.order`, `passage.hr.worker`) | deploy | infra | `deploy/kafka/connect.yaml` has connectors for receiving, sales, fulfillment and billing only; `consumer.enabled` stays false until the others exist. The consumer's read ACLs for all ten topics are in `deploy/kafka/search-consumer.yaml`. The production connector has to route by event type, as the sales one does, because production writes every event under one aggregate type. |
| Local dev stack wiring (`make dev`) | dev | infra | Run `search` against the dev database by hand. |
| Search benchmarks and typo-tolerance tuning | #506 | later slice | Threshold 0.4 on `word_similarity` is untuned. |
| Indexed name search under row-level security | #506 | later slice | pg_trgm operators are not leakproof, so the planner filters the name arm instead of using the GIN index. Upgrade path: leakproof wrappers created by a superuser, or a tenant-partitioned table. |
| Substring match inside a number | #506 | later slice | A number-shaped query matches by prefix only (`1042` does not find `PO-1042`). The browser matcher for lists follows the same rule on purpose. |
| List routes with their own `q` matching | #506 | later slice | The rule and its shared cases exist (`internal/store/testdata/matching.json`, `apps/web/src/shell/search/match.ts`, `apps/web/docs/lists.md`); each service's list route still matches `q` its own way until it is brought in line. No list page in the web app has a `text` filter yet. |
