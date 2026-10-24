# Not implemented in PSG-8

Delivered: product/master/variant domain and persistence, Cartesian explosion with hash lookup, released-product cascade, item/item-model groups, GS1 validation and allocation, lifecycle process policies, attributes, product UoM conversions, relations, service-product rules, document version copy, group-change/obsolete jobs as in-process functions, EAN-13 SVG, in-process configurator plus `configurator-py` CP-SAT worker, PIM web workspace, demo-catalog fixture (500 items / 50 masters).

| Deliverable or AC | Status | Reason |
|---|---|---|
| Temporal job `explode-{master_id}` with durable checkpoints | in-process | Wave 4 Temporal is being built in parallel; explosion is synchronous with 500-row outbox batches (AC-8.1 still holds) |
| Temporal `group-change-{id}` at an effective date | in-process | same; `RunGroupChange` is the callback-driven core (AC-8.12) |
| Live wave-16/17/20/21 reference callbacks | fake | callbacks return zero references until those waves exist (FR-8.15/8.16) |
| Object-storage originals, malware scan, 200 MB uploads | deferred | DIR metadata, version copy and tsvector column exist; bucket `passage-product-media` is a wave-1 deploy handoff |
| 1M-product typeahead p95 and 2M-DIR full-text p95 | not measured | search endpoints exist; the volume fixtures are a later capacity run |
| GDSN publication, ZPL print routing | later waves | wave 35 / 31 |
| Org site/warehouse membership check | interface | wave 6 org is not imported; site IDs are accepted as UUIDs |
| Number sequence `PROD` / `SVC-{cat}` | in-package | wave 5 numbering is not imported; service numbers use `SVC-{cat}-{#####}` |
| Argo CD app, `passage_product` database, CNPG | handoff | `deploy/` is outside wave 8 ownership |
