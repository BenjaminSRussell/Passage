# search

Global record search (#506): `GET /v1/search?q=&types=&limit=` over parties, products, purchase orders, receipts, vendor invoices, sales orders, packing slips, customer invoices, production orders and workers.

## Design choice: a consumer-fed service, not an ingest route

A new `services/search` module owns a Postgres index (`pg_trgm`) kept current by a Kafka consumer (`search consume`).
The alternative, an HTTP ingest route fed by each source's outbox reporter, was rejected: it needs a new push path and
a service-to-service duty in every source, while the outbox events already exist and the consumer pattern (idempotent
inbox, dead-letter topic, `eventing.Consumer`) is the one inventory and revrec use. Handlers take `events.Event`, so
tests need no broker.

The party, product and purchasing events gained the fields the index needs (number, name, type, status, and the legal
entity on `po.cancelled`, the survivor on `party.merged`, and a source version on each), so the consumer makes no
callbacks. The flag duty `search.index.write` only names the `search-index` service principal the consumer runs as; it
grants no route.

Subscribed topics are `passage.<context>.<outbox aggregate type>`: `passage.party.party`, `passage.product.product`,
`passage.procurement.purchase_order`, `passage.receiving.receipt`, `passage.receiving.vendorinvoice`, `passage.sales.order`,
`passage.fulfillment.shipment`, `passage.billing.invoice` and `passage.hr.worker`. Sales `order.confirmed` and fulfillment `packingslip.posted` gained
`order_number` and `legal_entity_id` (additive). Documents are indexed under their legal entity; a caller searches a type
with any one of its read duties. A test ties each suffix to the aggregate type the source service enqueues.

Production is the exception to the topic rule: it writes every event under one aggregate type (`production_order`) and
names its topics after the event type's entity, so its orders are read from `passage.production.order`, the channel in
its AsyncAPI document and the one scheduling reads. A test ties that topic to the channel address and the event types.
`order.created` gained `legalEntityId` and `status`, and `order.statuschanged` gained `orderNumber` and `legalEntityId`
(additive), so a status event indexes the order without the create. The status is stored in the upper snake case the
other types use (`ReportedAsFinished` becomes `REPORTED_AS_FINISHED`). A production order has no subtitle: its delivery
date and quantity change with `order.updated`, which the index does not read.

A worker is indexed from `worker.created` by personnel number and worker type. HR events carry no personal data (the
worker's name is not on the bus), but `worker.created` names the party person the worker was created for
(`party_person_id`). The index keeps that id on the worker row (migration `0005`) and titles the worker with the person's
name from the party record: from the indexed party row when the worker arrives after it, and from every newer party event
otherwise (an older party event renames nothing). So a worker is found by personnel number or by name. Workers are
tenant-wide, as HR's worker list is.

`deploy/kafka/search-consumer.yaml` grants the consumer's Kafka user a literal read on each subscribed topic; a test in
`cmd/search` fails when a topic is subscribed without one.

## Index

- `search_record` holds the documents (purchase orders, receipts, vendor invoices, sales orders, packing slips, customer
  invoices, production orders) with a non-null `legal_entity_id`; forced RLS on tenant and legal entity.
- `search_record_tenant` holds tenant-wide records (party, product, worker); forced RLS on tenant. Two tables because a nullable
  entity column with a null-admitting policy fails `tools/rlscheck`.
- Rows carry `source_version` (the source row's version) and `source_at` (the event time). An upsert applies only when
  the incoming pair is not older than the stored one, so redelivery and reordering change nothing.
- A party merge removes the losers as tombstones that outrank every version (a late event cannot bring one back) and
  re-indexes the survivor from the snapshot on `party.merged`. A cancelled purchase order stays searchable with status
  `CANCELLED`; a cancellation of an order that was never confirmed updates an existing row and never creates one.
- `search_text` is the number and title only, so a status or type word does not match every row.

## Query

Minimum two characters, at most 100; `limit` 1 to 50 (default 20). Input is only ever a bound parameter, with `%`, `_`
and `\` escaped for LIKE. A query under three characters or shaped like a number (a digit, no space, no run of more than
three letters) is a number prefix, served by a btree range on `number_key`. Anything else is a name: it matches when the
number and title hold it (`LIKE`) or by `word_similarity` of 0.4 or more. Under forced row-level security the
planner will not push `LIKE` or the pg_trgm operators into an index (they are not leakproof), so the name arm filters
rows within the caller's tenant; the prefix arm stays indexed (`TestIndexedQueriesAvoidSequentialScans`). Ranking: exact
number, number prefix, `word_similarity`, then number. A caller searches a type
only with its source's read duty (`party.read`, `product.read`, `procurement.purchase-order.inquire`,
`receiving.receipt.maintain`, `receiving.ap.inquire`, `sales.order.read`, `fulfillment.warehouse.view`,
`billing.invoice.view`, `production.order.read`, `hr.worker.inquire`); a type the
caller may not read is dropped silently. Each result is typed and carries a web `link`.

## One matching rule for the palette and for lists

`internal/store/testdata/matching.json` states what a query finds, case by case (exact number, number prefix, name
substring, typo, and what does not match). `TestSearchMatchesTheSharedCases` runs it against Postgres. The web app's
`apps/web/src/shell/search/match.ts` is the same rule in the browser (a port of pg_trgm's `word_similarity`), runs the
same file in its tests, and is what a list uses to filter rows it has loaded. A list route that takes `q` should match
the same way; `apps/web/docs/lists.md` ("Text search") says how. Change the rule in `plan` and the cases fail on both
sides until the matcher follows. Postgres decides what a letter is by the database locale; the browser counts letters
of any script, so the shared cases stay in ASCII.

## Run

`search` serves HTTP on `:8080` and admin on `:9090`; `search consume` runs the consumer (needs `KAFKA_*` and
`DATABASE_URL`). See `NOT-IMPLEMENTED.md` for what is left.

## Search and analytics

The analytics service (wave 46) already answers governed, aggregated metric queries over the lakehouse through Cube,
scoped by legal entity. It is not a record finder: it holds no party, product or purchase order rows and its freshness
is batch. Search is a small operational index, current within seconds of an event, that returns a link to one record.
