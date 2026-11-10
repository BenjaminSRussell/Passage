# search

Global record search (#506, subtask 506-1): `GET /v1/search?q=&types=&limit=` over parties, products and purchase orders.

## Design choice: a consumer-fed service, not an ingest route

A new `services/search` module owns a Postgres index (`pg_trgm`) kept current by a Kafka consumer (`search consume`).
The alternative, an HTTP ingest route fed by each source's outbox reporter, was rejected: it needs a new push path and
a service-to-service duty in every source, while the outbox events already exist and the consumer pattern (idempotent
inbox, dead-letter topic, `eventing.Consumer`) is the one inventory and revrec use. Handlers take `events.Event`, so
tests need no broker.

The party, product and purchasing events gained the fields the index needs (number, name, type, status, and the legal
entity on `po.cancelled`), so the consumer makes no callbacks. A flag duty `search.index.write` seeds the
`search-index` service principal the consumer runs as.

## Index

- `search_record` holds purchase orders with a non-null `legal_entity_id`; forced RLS on tenant and legal entity.
- `search_record_tenant` holds tenant-wide records (party, product); forced RLS on tenant. Two tables because a nullable
  entity column with a null-admitting policy fails `tools/rlscheck`.
- Rows carry `source_at`, the event time. An upsert applies only when the incoming time is not older than the stored
  one, so redelivery and reordering change nothing. A merge or cancellation keeps the row as a removed tombstone, so a
  late older event cannot bring it back.

## Query

Minimum two characters, at most 100; `limit` 1 to 50 (default 20). Input is only ever a bound parameter, with `%`, `_`
and `\` escaped for LIKE. Ranking: exact number, number prefix, `word_similarity`, then number. A caller searches a type
only with its source's read duty (`party.read`, `product.read`, `procurement.purchase-order.inquire`); a type the
caller may not read is dropped silently. Each result is typed and carries a web `link`.

## Run

`search` serves HTTP on `:8080` and admin on `:9090`; `search consume` runs the consumer (needs `KAFKA_*` and
`DATABASE_URL`). See `NOT-IMPLEMENTED.md` for what is left.
