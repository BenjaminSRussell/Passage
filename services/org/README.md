# org

Organization service for Passage (wave 6). Slice 1 ships legal entities, the pure hierarchy as-of logic and unit-of-measure conversion. Create also writes `dev.passage.org.legalentity.created.v1` to the `outbox` table in the same transaction (an Idempotency-Key replay writes none), and PATCH writes `dev.passage.org.legalentity.updated.v1` with `{id, changed_fields, version}` in the same transaction when a value changed; operating units write `dev.passage.org.operatingunit.created.v1` on create and `dev.passage.org.operatingunit.updated.v1` on a version bump the same way; sites, which are legal-entity-scoped, write `dev.passage.org.site.created.v1` and `dev.passage.org.site.updated.v1` the same way; warehouses, which take their legal entity from their site, write `dev.passage.org.warehouse.created.v1` and `dev.passage.org.warehouse.updated.v1` the same way; locations, which take their legal entity from their warehouse, write `dev.passage.org.location.created.v1` on create; postal addresses write `dev.passage.org.address.changed.v1` on create, take their region and postal-code rules from the seeded `country` rows, and go through a geocoder that is a null adapter until a real one is configured; an address PATCH closes the current version, inserts the next and emits `address.changed` for it, and documents read a version by id; a site cannot be deactivated while an active warehouse references it; events are declared in `events/asyncapi.yaml`. Everything else is listed in `NOT-IMPLEMENTED.md`.

Run: `DATABASE_URL=postgres://... MIGRATE_ON_START=true go run ./cmd/org`. Test: `go vet ./... && go test ./...`; store tests need `PASSAGE_TEST_DATABASE_URL` (role without BYPASSRLS).

## Configuration

| Variable | Meaning |
|---|---|
| `DATABASE_URL` | required; Postgres URL, from a Secret in the cluster |
| `MIGRATE_ON_START` | `true` applies embedded migrations at start (dev only) |
| `HTTP_ADDR`, `ADMIN_ADDR` | default `:8080`, and `:9090` serving `/metrics` only |

## Reference data

Countries, currencies, regions and languages are platform-owned rows under the `sys` tenant (`00000000-0000-0000-0000-000000000000`): every tenant can read them, no tenant can insert, update or delete them. The data lives in `migrations/reference/countries.csv`, `currencies.csv`, `regions.csv` and `languages.csv`, embedded as `migrations.Reference`, and `store.SeedReference` loads it after the migrations when `MIGRATE_ON_START=true`. Regions cover US, CA, IN and MX as full ISO 3166-2 codes (`US-CA`). A malformed row fails the whole file with `<file> line <n>: <reason>`; re-running the seed changes nothing. Tenants never edit the `sys` rows: `PATCH /v1/org/{collection}/{code}` deactivates or reactivates a row for the calling tenant only, by writing `reference_status`, and every read returns the tenant's effective status.

Units of measure and their conversions are seeded the same way from `migrations/reference/units.csv` (59 units) and `uom-conversions.csv` (49 conversions) into `unit_of_measure` and `uom_conversion`. Units fall in twelve classes: the eight of FR-6.8 (`quantity`, `each`, `mass`, `length`, `volume`, `area`, `time`, `temperature`) plus `energy`, `pressure`, `power` and `speed`, which the AC-6.3 fixture needs. A conversion is stored once per unordered pair (a unique index on the sorted codes), and the inverse is computed. `store.SeedConversions` checks both files together before any write: unknown unit, two classes in one pair and a reversed duplicate fail with `uom-conversions.csv line <n>: <reason>`. `GET /v1/org/uom/convert` reads the persisted set per request through `store.ConversionGraph` (the caller's visible, active units and their conversions, at most two hops inside one class) and returns `quantity` rounded to 12 decimals plus `displayQuantity` rounded to the target unit's `decimal_precision` (BR-6.8); an unknown or inactive unit returns `ORG-UOM-NO-PATH`. The unit routes list and read units (`GET /v1/org/uom/units`, `…/{code}`; codes are case-sensitive), add a tenant-owned custom unit (`POST`, code `^[A-Za-z0-9/%]{1,10}$`, precision 0 to 6, optional `Idempotency-Key`) and deactivate or reactivate any unit for the calling tenant (`PATCH …/{code}`). Unit status for platform and tenant units lives in `reference_status` with kind `uom`, so a platform unit deactivated by one tenant stays active for the others; units are never deleted. The conversion read API and tenant-owned conversions are the next slice.

## Fiscal calendars

`domain.GenerateYear` builds the periods of one fiscal year for the patterns `calendar`, `4-4-5`, `4-5-4`, `5-4-4`, `13-period` and `custom`, with optional opening, closing and special (adjustment) periods, and `domain.ProveContiguous` proves they tile the year (`ORG-CAL-GAP`, `ORG-CAL-OVERLAP`, `ORG-CAL-PATTERN`). A year is labelled by the calendar year of its nominal start. Week-based years end on the last day of the week nearest to the nominal year end (the NRF retail-calendar rule), so they have 364 or 371 days and the 371-day year puts its extra week in the last period (`week53`). The golden CSVs are in `testdata/fiscal-patterns/`. 

Calendars are tenant-level and persisted in `fiscal_calendar` and `fiscal_period`. `POST /v1/org/fiscal-calendars` (duty `org.fiscal-calendar.write`, `Idempotency-Key`) creates one with the defaults `startMonth` 1, `weekStart` `monday`, `openingClosing` true and `specialPeriods` 0; `GET /v1/org/fiscal-calendars/{id}` reads it. `POST /v1/org/fiscal-calendars/{id}:generate` with `{fiscalYear, force?, periods?}` writes the year's periods and emits `fiscalcalendar.generated` (201, `generated` true). Calling it again for the same calendar and year is a replay: 200, `generated` false, the stored periods, no write and no event. `force` replaces the rows and emits a second event, but first asks the `PostingChecker`: postings answer 409 `ORG-CAL-LOCKED`, a checker failure answers 503 `COM-0503`. Until the ledger exists the checker reports no postings. `periods` is required for a `custom` calendar and rejected for any other pattern (`ORG-CAL-PATTERN`). `GET /v1/org/fiscal-calendars/{id}/periods?fiscalYear=` returns the periods in order 0, operating, adjustment, 99.

## Hierarchies

Hierarchies are tenant-level configuration, scoped by tenant only. `POST /v1/org/hierarchy-types` creates a type with its purposes. `POST /v1/org/hierarchy-types/{id}/versions` opens a draft version; a type has at most one open draft (`ORG-HIER-DRAFT-EXISTS`), and the draft starts as a copy of the type's latest published version, or empty when none exists. `PUT /v1/org/hierarchy-versions/{id}/nodes` replaces the draft's whole node set under the optimistic `version` lock; published versions are immutable.

Every node set is checked structurally before it is stored: at most 50,000 nodes, each org unit once, known parents, no cycle, exactly one root and at most 15 levels. `POST /v1/org/hierarchy-versions/{id}:publish` (body `version`, `effectiveFrom`, optional `reason`; `Idempotency-Key`) publishes a draft. The version must come after the type's latest published version (`ORG-HIER-OVERLAP`), which is superseded with its range closed at `effectiveFrom`; the stored node set is re-validated, and the closure table (one row per ancestor-descendant pair, no self pairs) is written in the same transaction as the `dev.passage.org.hierarchyversion.activated.v1` event. A publish with `effectiveFrom` before today (UTC) is backdated: it needs the `org.hierarchy.backdate` duty, a type with `backdateAllowed` and a `reason` of 10 to 500 characters, or it is refused with `ORG-HIER-BACKDATE-DUTY` before any state changes. `GET /v1/org/hierarchy-types/{id}/as-of?date=&node=` returns the node's parent, ancestors and descendants in the non-draft version in force on that date; superseded versions answer for dates inside their closed range.

## API

| Operation | Duty |
|---|---|
| `POST /v1/org/legal-entities` (required `timeZone`, IANA name; optional `defaultLanguage` default `en-US`, `registrationNumber`, `Idempotency-Key`; country, currency and language must be active; 500 per tenant) | `org.legal-entity.write` |
| `GET /v1/org/legal-entities` (`limit` max 100, `cursor`; filters `status` active or inactive, `country`, `currency`; returns `{items, nextCursor}`) | `org.read` |
| `PATCH /v1/org/legal-entities/{id}` (body `version` required, optional `name`, `timeZone`, `defaultLanguage`, `registrationNumber`, `status`, `primaryAddressId` (`""` clears); code, country, currency immutable) | `org.legal-entity.write` |
| `GET /v1/org/legal-entities/{id}`, `GET /v1/org/legal-entities/{id}/names` (name history) | `org.read` |
| `POST /v1/org/operating-units` (body `type` department, cost_center, business_unit or value_stream, `code` upper-cased and unique per type, `name`; optional `defaultLegalEntityId`, `managerPartyId` for cost centers, `Idempotency-Key`) and `PATCH /v1/org/operating-units/{id}` (body `version` required, optional `name`, `status`, `defaultLegalEntityId`, `managerPartyId`; `""` clears an id; type and code immutable) | `org.operating-unit.write` |
| `POST /v1/org/sites` (body `legalEntityId`, `code` upper-cased and unique per legal entity, `name`; optional `timeZone` defaulting to the legal entity's, `Idempotency-Key`) and `PATCH /v1/org/sites/{id}` (body `version` required, optional `name`, `timeZone`, `status`, `addressId` (`""` clears); legal entity and code immutable) | `org.site.write` |
| `GET /v1/org/sites` (`limit`, `cursor`; filters `legalEntityId`, `status`; returns `{items, nextCursor}`) and `GET /v1/org/sites/{id}` | `org.read` |
| `POST /v1/org/warehouses` (body `siteId`, `code` upper-cased and unique per legal entity, `name`; optional `type` standard, transit, quarantine or subcontractor, `timeZone` defaulting to the site's, `Idempotency-Key`; the legal entity is the site's) and `PATCH /v1/org/warehouses/{id}` (body `version` required, optional `siteId` within the same legal entity, `name`, `type`, `timeZone`, `status`, `addressId` (`""` clears); legal entity and code immutable) | `org.site.write` |
| `GET /v1/org/warehouses` (`limit`, `cursor`; filters `legalEntityId`, `siteId`, `type`, `status`; returns `{items, nextCursor}`) and `GET /v1/org/warehouses/{id}` | `org.read` |
| `POST /v1/org/locations` (body `warehouseId`, `code` upper-cased and unique per warehouse, 1 to 20 characters, `name`; optional `type` storage, receiving, shipping or quarantine, `Idempotency-Key`; the legal entity is the warehouse's) | `org.site.write` |
| `GET /v1/org/locations` (`limit`, `cursor`; filters `legalEntityId`, `warehouseId`, `type`, `status`; returns `{items, nextCursor}`) and `GET /v1/org/locations/{id}` | `org.read` |
| `PATCH /v1/org/locations/{id}` (body `version` required, optional `name`, `type`, `status`, `addressId` (`""` clears); warehouse and code immutable) | `org.site.write` |
| `POST /v1/org/addresses` (body `ownerType` legal_entity, operating_unit, site, warehouse, location or party, `ownerId`, `countryCode`, `city`, `street1`, `purposes` of 1 to 7 distinct values of business, delivery, invoice, remit_to, service, home, other each with optional `primary`; optional `regionCode`, `postalCode`, `street2`, `street3`, `building`, `poBox`, `validFrom`, `Idempotency-Key`) | `org.address.write` |
| `GET /v1/org/addresses/{id}` (the current version with its purposes) | `org.read` |
| `PATCH /v1/org/addresses/{id}` (body `versionId` plus any postal field, `validFrom` and `purposes`; `""` clears an optional field) | `org.address.write` |
| `GET /v1/org/address-versions/{id}` (any version of an address by its `versionId`, current or closed) | `org.read` |
| `GET /v1/org/addresses` (`ownerType`, `ownerId` required; `purpose`, `limit`, `cursor`; the owner's current addresses as `{items, nextCursor}`) | `org.read` |
| `GET /v1/org/operating-units` (`limit`, `cursor`; filters `type`, `defaultLegalEntityId`, `status`; returns `{items, nextCursor}`) and `GET /v1/org/operating-units/{id}` | `org.read` |
| `POST /v1/org/hierarchy-types` (body `code`, `name`, `purposes`; optional `backdateAllowed`, `Idempotency-Key`), `POST /v1/org/hierarchy-types/{id}/versions` (`Idempotency-Key`, no body) and `PUT /v1/org/hierarchy-versions/{id}/nodes` (body `version`, `nodes`) | `org.hierarchy.write` |
| `POST /v1/org/hierarchy-versions/{id}:publish` (body `version`, `effectiveFrom`; optional `reason`, `Idempotency-Key`; not for legal-entity-scoped callers; a backdated `effectiveFrom` also needs `org.hierarchy.backdate`) | `org.hierarchy.publish` |
| `GET /v1/org/hierarchy-types/{id}/as-of?date=&node=` | `org.read` |
| `GET /v1/org/{countries,currencies,regions,languages}` (`status` active or inactive; `country` for regions; returns `{items}`) and `GET /v1/org/{countries,currencies,regions,languages}/{code}` | `org.read` |
| `PATCH /v1/org/{countries,currencies,regions,languages}/{code}` (body `{"status":"active"\|"inactive"}`; not for legal-entity-scoped callers) | `org.reference.write` |
| `PUT /v1/org/currencies/{code}/rounding-rules/{purpose}` (purpose general, price, purchase, sales, tax or cash; body `unit`, `method`, optional `legalEntityId`; not for legal-entity-scoped callers; an identical repeat changes nothing and emits no event) | `org.reference.write` |
| `GET /v1/org/currencies/{code}/rounding-rules` (the tenant's stored rules as `{items}`) and `GET /v1/org/currencies/{code}/round?amount=&purpose=&legalEntityId=` (rounds with the legal-entity rule, else the tenant rule, else the currency's minor unit with half_even; `source` names which) | `org.read` |
| `GET /v1/org/uom/convert?from=&to=&qty=` (returns `quantity` and `displayQuantity`) | any authenticated principal |
| `GET /v1/org/uom/units` (`class`, `status`; returns `{items}`) and `GET /v1/org/uom/units/{code}` | `org.read` |
| `POST /v1/org/uom/units` (body `code`, `name`, `class`, `decimalPrecision`; optional `Idempotency-Key`; not for legal-entity-scoped callers) and `PATCH /v1/org/uom/units/{code}` (body `{"status":"active"\|"inactive"}`; not for legal-entity-scoped callers) | `org.reference.write` |
| `GET /healthz`, `GET /readyz` (database ping, 503 when down), `GET /metrics` on the admin port | public |

Contract: `api/openapi.yaml`. Paths follow the architecture spine (`/v1/<context>/<collection>`), not the PRD's `/org/v1`. The chart is `charts/org` per CONVENTIONS, not the PRD's `passage-org`. The legal-entity header is `X-Passage-Legal-Entity`.

## Error codes

| Code | Status | Meaning |
|---|---|---|
| COM-0001 | 400 | malformed body, unknown field, failed field validation (`errors` names the fields), bad `from`, `to` or `qty`, unknown `timeZone` |
| AUTH-0001, AUTH-0003, COM-0500 | 401, 403, 500 | missing tenant, duty missing, internal error with hidden detail |
| ORG-CAL-NOT-FOUND | 404 | fiscal calendar unknown, malformed id or another tenant's |
| ORG-CAL-CODE-DUPLICATE | 409 | fiscal-calendar code already used in the tenant |
| ORG-CAL-LOCKED | 409 | `force` regeneration of a fiscal year the ledger reports postings for |
| ORG-CAL-PATTERN, ORG-CAL-GAP, ORG-CAL-OVERLAP | 422 | unusable calendar spec or custom ranges that do not tile the year |
| ORG-HIER-TYPE-NOT-FOUND, ORG-HIER-VERSION-NOT-FOUND | 404 | hierarchy type or version unknown, malformed id or another tenant's |
| ORG-HIER-TYPE-CODE-DUPLICATE, ORG-HIER-DRAFT-EXISTS, ORG-HIER-VERSION-IMMUTABLE, ORG-HIER-OVERLAP | 409 | type code already used; the type has an open draft; the version is not a draft; the effective range does not start after the latest published version |
| ORG-HIER-BACKDATE-DUTY | 403 | backdated publish without the backdate duty, on a type that forbids it or without a 10 to 500 character reason |
| ORG-HIER-NO-VERSION, ORG-HIER-NODE-ABSENT | 404 | no non-draft version in force on the as-of date; the node is not in that version |
| ORG-HIER-SIZE, ORG-HIER-DUPLICATE-NODE, ORG-HIER-PARENT-UNKNOWN, ORG-HIER-CYCLE, ORG-HIER-MULTI-ROOT, ORG-HIER-DEPTH, ORG-HIER-NODE-UNKNOWN | 422 | node set fails structural validation, or an org unit is neither a legal entity nor an operating unit |
| COM-0503 | 503 | the ledger posting check failed, so `force` is refused |
| ORG-LE-CODE-DUPLICATE | 409 | legal-entity code already used in the tenant |
| ORG-LE-CODE-RESERVED | 400 | legal-entity code is DAT, SYS or ALL |
| ORG-ADDR-NOT-OWNED | 422 | an address pointer (`primaryAddressId`, `addressId`) that is not a current address owned by the record: unknown, closed version id, another owner's or out of scope |
| ORG-LE-NOT-FOUND, ORG-VERSION-CONFLICT | 404, 409 | unknown, malformed or out-of-scope legal-entity id; stale version (also a stale address `versionId`) |
| ORG-LE-REFERENCE-INACTIVE, ORG-LE-LIMIT | 422 | country, currency or default language unknown or inactive for the tenant; 500 legal entities already |
| ORG-OU-NOT-FOUND | 404 | unknown or malformed operating-unit id |
| ORG-OU-CODE-DUPLICATE | 409 | operating-unit code already used for the type in the tenant |
| ORG-OU-LEGAL-ENTITY-INVALID | 422 | default legal entity unknown, inactive or outside the caller's scope |
| ORG-SITE-NOT-FOUND | 404 | unknown, malformed or out-of-scope site id |
| ORG-SITE-CODE-DUPLICATE | 409 | site code already used in the legal entity |
| ORG-SITE-LEGAL-ENTITY-INVALID | 422 | legal entity unknown, inactive or outside the caller's scope |
| ORG-WH-NOT-FOUND | 404 | unknown, malformed or out-of-scope warehouse id |
| ORG-WH-CODE-DUPLICATE | 409 | warehouse code already used in the legal entity |
| ORG-WH-SITE-INVALID | 422 | site unknown, inactive or outside the caller's scope |
| ORG-WH-LE-MISMATCH | 422 | the new site belongs to a different legal entity than the warehouse |
| ORG-SITE-IN-USE | 409 | site deactivation refused while an active warehouse references it |
| ORG-LOC-NOT-FOUND | 404 | unknown, malformed or out-of-scope location id |
| ORG-LOC-CODE-DUPLICATE | 409 | location code already used in the warehouse |
| ORG-LOC-WAREHOUSE-INVALID | 422 | warehouse unknown, inactive or outside the caller's scope |
| idempotency_key_reuse | 422 | Idempotency-Key reused with a different body |
| ORG-ADDR-NOT-FOUND | 404 | unknown, malformed or out-of-scope address id |
| ORG-ADDR-OWNER-INVALID | 422 | owner unknown or outside the caller's scope |
| ORG-ADDR-COUNTRY-INVALID, ORG-ADDR-REGION-UNKNOWN | 422 | country or region unknown or inactive for the tenant |
| ORG-ADDR-REGION-REQUIRED, ORG-ADDR-POSTAL-FORMAT | 422 | the country requires a region, or the postal code does not match its seeded pattern; `errors` lists every failing field |
| ORG-ADDR-PRIMARY-DUPLICATE | 409 | the owner already has a primary address for a purpose |
| ORG-ADDR-VALID-FROM-INVALID | 422 | `validFrom` precedes the current version or is after today |
| ORG-REF-NOT-FOUND | 404 | unknown reference code |
| ORG-CUR-ROUNDING-UNIT | 422 | rounding unit outside 0.000001 to 1000, finer than the currency minor unit, or not a power of ten or 5 or 25 times one |
| ORG-UOM-NO-PATH | 422 | no conversion within two hops inside one unit class, or a unit that is unknown or inactive for the tenant |
| ORG-UOM-UNIT-NOT-FOUND | 404 | unit code not visible to the tenant (codes are case-sensitive) |
| ORG-UOM-CODE-DUPLICATE | 409 | unit code already used by a platform unit or one of the tenant's units |

Metrics: `org_legal_entities_created_total`, `org_legal_entities_failed_total`, `org_legal_entities_updated_total`, `org_reference_status_changed_total`, `org_uom_units_created_total`, `org_currency_rounding_rules_changed_total`, `org_operating_units_created_total`, `org_operating_units_updated_total`, `org_sites_created_total`, `org_sites_updated_total`, `org_warehouses_created_total`, `org_warehouses_updated_total`, `org_locations_created_total`, `org_locations_updated_total`, `org_addresses_created_total`, `org_addresses_revised_total` (stdlib atomics rendered as text).
