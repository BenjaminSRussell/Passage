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
| Number sequence `PROD` / `SVC-{cat}` | in-package | `services/numbering` now exists; `numberingclient`, `NUMBERING_BASE_URL`, allocation from `PROD` / `SVC-{cat}` and the 502 `PRD-NUMBERING-INVALID` response are the remaining PSG-802 slice (AC4-AC6); service numbers still use `SVC-{cat}-{#####}` |
| Argo CD app, `passage_product` database, CNPG | handoff | `deploy/` is outside wave 8 ownership |
| services/org/NOT-IMPLEMENTED.md rows "Product-specific conversions (`uom_conversion.product_id`)" and the matching `productSpecific` filter row | handoff delivered | done in this wave: `product_uom_conversion` and its product -> class -> error resolution are built in `services/product`; wiring the org report's `productSpecific` filter is wave 6's own work (the originating wave's next planner treats this as DONE) |
| Org membership check when `ORG_BASE_URL` is empty | disabled | local and test runs log one startup warning and skip the check; the chart default is `http://org:8080`; fallback policy for production is a deploy handoff |
