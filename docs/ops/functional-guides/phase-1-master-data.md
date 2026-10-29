# Functional guide: Phase 1, Enterprise Structure, Master Data & Engineering Data

Waves 6-10. Audience: implementers and power users who set up the company structure and the master data every
later module reads. Read this before the financial core guide, because every posting carries the legal entity,
dimensions and parties defined here.

This guide describes intended behaviour from the phase 1 PRD (`docs/prd/phase-1-master-data.md`). It is not a
build status. Each wave section names the service directory and the web module that own it; what is built is in
that service's `README.md` and what is not is in its `NOT-IMPLEMENTED.md`, which is authoritative where it and this
guide differ.

## Wave 6: Enterprise structure and organization model

Owning area: `services/org`. Screens: `apps/web/src/modules/org`.

What you do here:

- Define legal entities, operating units (department, cost center, business unit, value stream), sites,
  warehouses and locations.
- Maintain effective-dated hierarchies. One node can sit in several hierarchies with different purposes:
  reporting, procurement and security. Every module filters lists by the hierarchy as of the business date.
- Maintain the address book: postal addresses are validated, geocoded by a hook, and tagged with purposes.
- Set up fiscal calendars (4-4-5, 13-period and calendar-year patterns) and their periods per ledger.
- Maintain reference data: countries, ISO 4217 currencies, units of measure with conversions, and languages.
- Maintain teams, team types and membership rules, copy a legal entity from a template, and set rounding rules
  per currency purpose.

Checks you should see: the hierarchy as-of view returns the parent that was effective on the date you pick; the
legal entity switcher re-scopes every list page; unit conversions round-trip without loss.

## Wave 7: Global party and business partner model

Owning area: `services/party`. Screens: none on main yet; the `party` web module of wave 7 is not built, so the
steps below run through the party API until it ships.

What you do here:

- Create one party per real organization or person, then add roles: customer, vendor, employee, contact,
  prospect and bank. Role-specific data lives in separate records.
- Run duplicate detection before creating a party. Merge duplicates with the survivorship rules; the merge keeps
  all role data and rewrites references in one transaction.
- Record tax registration numbers. The country format is validated (VAT, EIN, GSTIN); VIES and IRS TIN checks are
  adapters that can be switched off per tenant.
- Set customer and vendor groups, posting profiles, payment terms, delivery terms and credit limits.
- Use the stewardship queue for master data changes that need approval. A credit limit increase waits for approval.
- Record vendor certifications (an expired certificate places a hold) and installment payment schedules.
- Create one-time accounts for single transactions, party relationships (head office and branch, alternate payer,
  permitted payee) and customer hierarchies.

Checks you should see: a near-duplicate is flagged before save; a merged party shows the surviving record and its
history; an expired certificate blocks purchase orders for the vendor.

## Wave 8: Product master, variants and catalogs

Owning area: `services/product`, with `services/configurator-py` for the configure-to-order constraint solver.
Screens: `apps/web/src/modules/product`.

What you do here:

- Define a product master with dimension groups (product, storage and tracking dimensions). Release the product
  per legal entity, with site and warehouse defaults.
- Generate variants from dimension values. A master with three dimensions of ten values each yields 1,000 variants.
- Set item groups and item model groups: costing method, negative inventory policy, and whether the item posts
  physically and financially.
- Classify products with attributes and category hierarchies.
- Use the configurator for configure-to-order products. Constraints reject invalid combinations before an order is
  entered.
- Maintain GS1 identifiers (GTIN and SSCC) and barcodes, plus product documents.
- Set lifecycle states per process: allow, warn or block. A mass obsolete job runs under the same policy.
- Define service products and document info records, and search documents full text.

Checks you should see: an invalid GTIN check digit is rejected; a released product shows the defaults that cascade
from its site and warehouse; a blocked lifecycle state stops purchase and sales transactions.

## Wave 9: Engineering data (BOMs, routings, resources, change management)

Owning area: `services/engineering` (BOMs, routings, resources and engineering change orders). Screens:
`apps/web/src/modules/engineering`.

What you do here:

- Maintain bills of material: header, versions and lines with effective dates, scrap, phantom items, by-products
  and co-products, and formula BOMs for process manufacturing.
- Maintain routings: versions, operations, sequential and parallel operation relations, setup and run times, and
  resource requirements by capability.
- Maintain resources (machine, tool, labor, vendor, location), resource groups and work calendars with shifts.
- Run engineering change orders. Impact analysis shows where-used results before release. Approval expires the old
  BOM version and activates the new one for production.
- Run BOM explosion and implosion, where-used, and the cost roll-up skeleton. The full cost roll-up is completed in
  the standard costing wave.
- Attach engineering documents to BOM lines; signatures on document status changes follow the 21 CFR Part 11
  pattern where the customer requires it.
- Import CAD or PLM engineering BOMs and compare them with the manufacturing BOM.

Checks you should see: the BOM as of a date picks the version effective on that date; the where-used report lists
every parent of a changed component; a routing lead time reflects parallel operations.

## Wave 10: Data migration framework and legacy connectors

Owning area: `services/migration`, with the AX and SAP source table specs in `tools/connectors` (flat-file CSV
extracts only; the ODBC, SAP RFC and OData drivers are not built). Screens: none on
main yet; the wave 10 `migration` web module is not built. The wave 49 migration factory screens are in
`apps/web/src/modules/migration-factory`. The [implementer guide](../implementer-guide.md) status table names the
migration steps that cannot run yet.

What you do here:

- Run a migration in stages: extract (from AX SQL or SAP OData and flat files), stage, map, validate, load and
  reconcile. Each run is recorded in the migration run ledger with lineage.
- Map source fields with the declarative mapping language. Coercion, enum mapping, defaults and rejections are
  recorded per row with a reason.
- Reconcile by row count, hash total and sampled field comparison. Exceptions go to a queue; fix the data and
  re-run that exception only.
- Dry-run first. A re-run of the same load makes no changes, so you can repeat a load safely up to cutover.
- Copy entity data between legal entities and apply signed configuration packages. Review the diff before you apply.
- Use the mapping packs for the coverage-closure objects (team, vendor certification, payment schedule, product
  relations, lifecycle states, effectivity and engineering document structures).

Checks you should see: the reconciliation report shows 100% of rows matched or explained before load sign-off;
a second run reports zero changes.

## Common setup order

1. Organization (wave 6), then reference data and fiscal calendars.
2. Parties with their roles (wave 7), since products and ledgers reference them.
3. Products and dimensions (wave 8), then BOMs and routings (wave 9).
4. Load legacy data through the migration framework (wave 10), reconcile, then sign off.

Related: the [tenant administrator guide](../tenant-administrator-guide.md) for assigning the roles and duties
that maintain this master data, and the [implementer guide](../implementer-guide.md) for the migration steps in the
cutover plan.
