# Product

## Purpose & Boundaries
Context `product` (database `passage_product`; waves 8-9). Deployables: `product` (Go: BOM explosion, where-used, effective dating), `product-ts` (ECM change orders, GS1, attributes), `product-py` (constraint configurator, CP-SAT; stateless, no database). Owns products, dimension groups, released products, BOM, formula, routing, resources, calendars, change orders, configuration models.

## Interfaces
Generated CRUD for products, product masters, released products, dimension groups, item model groups, attributes, classifications, BOMs, formulas, routings, operations, resources, resource groups, calendars, change orders, configuration models. Custom:
- `POST /v1/product/product-masters/{id}:generate-variants`; `POST /released-products`; `GET /gtins/{gtin}`; `POST /gtins:validate`.
- BOM: `POST /boms/{id}/versions`, `POST /bom-versions/{id}:approve|:activate` (emits `bom.versionactivated.v1`), `GET /boms/{id}/explosion?quantity=&asOf=&levels=` (7 levels, 2,000 components < 500 ms, NFR-P7), `GET /products/{id}/where-used?asOf=`, `POST /boms:compare`, `POST /formulas/{id}:scale`.
- ECO: `POST /change-orders/{id}:submit|:release|:cancel`, `GET /change-orders/{id}/impact`; release emits `changeorder.released.v1`.
- Configurator: `POST /configurations:solve|:validate`, `POST /configurations/{id}:finalize` (creates variant product + BOM/route).

## Data Ownership
EN-018..022: product_master, product (product_number UQ per tenant shared namespace; variant_values/variant_hash; gtin with check-digit CHECK; status draft|released|blocked|obsolete), dimension_group (kind product|storage|tracking), product_identifier, item_model_group (costing_method fifo|lifo|weighted_average|moving_average|standard; allow_negative_physical/financial), released_product (ML; UQ (tenant_id, product_id, legal_entity_id)), attribute_def/value, configuration_model, bom/bom_version/bom_line (one active version per finished item x site x date range via EXCLUDE; line_type item|phantom|vendor|pegged_supply|co_product|by_product), route/route_version/route_operation (+requirements, resource, resource_group, capability, work_calendar, calendar_exception), change_order/line. Indexes IX-PR-01..35 (IX-PR-20 active BOM resolution, IX-PR-22 explosion, IX-PR-23 where-used).

## Events Produced
`product.product.released.v1` (productId, releasedProductId, legalEntityId, productNumber, productType item|service|bom_item, bomKind?, itemModelGroup, costingMethod, baseUnit, dimensionGroups{product,storage,tracking}, gtin?, ...; snapshot topic), `product.bom.versionactivated.v1` (bomId, bomNumber, versionNo, productId, legalEntityId, siteId?, effectiveFrom/To, previousActiveVersionId?, lineCount, hasPhantoms), `product.changeorder.released.v1` (changeOrderId, effectiveDate, affected[]). Generated `<entity>.created|changed|deleted`.

## Events Consumed
`product-approvals` and `product-legalentity-ref` (platform.workflowtask.completed, platform.legalentity.created); platform.numbersequence.changed for sequence definitions.

## Dependencies
internal: platform (legal entities, sites, UoM, workflow). external: OR-Tools CP-SAT (Python), GS1 vectors.

## Gotchas & Constraints
- BOM/route are SNAPSHOT-copied into production orders at release; later BOM changes do not alter orders.
- BOM version cache invalidated by `bom.versionactivated.v1`; versions immutable once active.
- Python solvers are stateless, invoked synchronously or via Temporal activity; CP-SAT exact tests use fixed seed + single worker.
- Variant explosion; configurator model storage (`configuration_model`) not in Appendix B (OQ-14); SAP VC conversion is manual in v1.
- Sequence codes ITM/BOM/RTE/PTY non-continuous (OQ-04 default).
- Formula expressed as `bomKind=formula`, not a product type.

## Source Sections
- Backend 3.2 product row (287), 5.7 (596-619); Data EN-018..022 (2230-2368), 5.3 ERD (1662-1688); Events 3.3 (4001-4003), consumer rows 9-11 (4192-4194); BOM explosion perf (2323); migration MD-PRODUCT/MD-ENG (3519-3520, 3553-3561)
