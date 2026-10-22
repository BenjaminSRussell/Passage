# Phase 1: Enterprise Structure, Master Data & Engineering Data (waves 6-10)

Model the enterprise (legal entities, sites, org hierarchies), the shared party/business-partner model, products and variants, BOMs/routings (engineering data a manufacturer needs on day one), and the migration framework every later wave uses to load legacy AX/SAP data.

## Wave 06: Enterprise Structure & Organization Model

**Phase 1 — Enterprise Structure, Master Data & Engineering Data**

**Goal.** Model tenants, legal entities, operating units, departments, sites and warehouses with effective-dated hierarchies that every module filters by.

**Replaces**

- *AX / D365:* Legal entities, organization hierarchies, operating units, sites/warehouses, DataAreaId
- *SAP:* Company codes, controlling areas, plants, storage locations, sales organizations, purchasing organizations, enterprise structure (SPRO)

**Deliverable**

- Entities: LegalEntity, OperatingUnit (department, cost center, business unit, value stream), Site, Warehouse, Location; effective-dated hierarchy with multiple purposes (reporting, procurement, security)
- Hierarchy designer UI with drag-drop and effective-date preview
- Address book with validated postal addresses, geocoding hook, and address purposes
- Fiscal calendars: multiple calendars, periods, period statuses per ledger
- Reference data: countries, currencies (ISO 4217), units of measure with conversions, languages
- Teams and team types (OMTeam) with members, effective dates and membership rules; legal-entity copy from template; currency rounding rules per purpose; organization name translations

**Proof (testable acceptance)**

- [ ] Hierarchy test: as-of queries return the right parent for 1,000 effective-dated nodes across 5 purposes
- [ ] Legal-entity switch in the UI re-scopes every list page (property test on 20 entities)
- [ ] UoM conversion round-trip precision test across 50 unit pairs
- [ ] Fiscal calendar generation for 4-4-5, 13-period, and calendar-year patterns matches fixtures

**Deploy**

Organization administration workspace live; seed data for a demo manufacturer (3 legal entities, 2 plants, 5 warehouses).

**References**

- [AX 2012 organization model](https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/organizations-and-organizational-hierarchies)
- [SAP enterprise structure (company code, plant, storage location)](https://help.sap.com/docs/SAP_S4HANA_ON-PREMISE)
- [ISO 4217 currency codes](https://www.iso.org/iso-4217-currency-codes.html)
- [UCUM units of measure](https://ucum.org/)

**Depends on:** wave 5

---

## Wave 07: Global Party & Business Partner Model

**Phase 1 — Enterprise Structure, Master Data & Engineering Data**

**Goal.** One party record per real-world organization or person, with customer/vendor/employee/contact roles layered on top, deduplicated and governed.

**Replaces**

- *AX / D365:* Global address book (DirParty), customers, vendors, contacts, parties with multiple roles
- *SAP:* Business Partner (BP) with roles, customer/vendor master (KNA1/LFA1), S/4 Customer-Vendor Integration

**Deliverable**

- Party (organization/person) with roles: Customer, Vendor, Employee, Contact, Prospect, Bank; role-specific data in separate entities
- Duplicate detection (fuzzy match on name/tax id/address) with merge workflow and survivorship rules
- Tax registration numbers with country validation (VAT, EIN, GSTIN) and VIES/IRS TIN check adapters
- Customer and vendor groups, posting profiles (hooks for the GL), payment terms, delivery terms, credit limits
- Data-governance: ownership, stewardship queue, change approval workflow on master data
- Vendor certifications with expiry-driven holds and diversity classifications; installment payment schedules (PaymSched / T052R)
- One-time customer and vendor accounts with document-level address capture; party relationships (head office/branch, alternate payer, permitted payee); address books with team security; customer hierarchies; classification code lists and hold reasons

**Proof (testable acceptance)**

- [ ] Dedup test: 10k seeded parties with 500 planted near-duplicates — precision ≥ 0.95, recall ≥ 0.90
- [ ] Merge preserves all role data and rewrites references atomically (checked by referential integrity audit)
- [ ] VAT number validation adapter returns correct results against a recorded VIES fixture set
- [ ] Change-approval workflow blocks a credit limit increase until approved (Temporal test)

**Deploy**

Customer, vendor and contact workspaces usable for data entry; import of a legacy AX CustTable/VendTable extract via the import tool.

**References**

- [AX 2012 global address book](https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/global-address-book-overview)
- [SAP Business Partner & CVI](https://help.sap.com/docs/SAP_S4HANA_ON-PREMISE)
- [VIES VAT validation](https://ec.europa.eu/taxation_customs/vies/)
- [Record linkage / Fellegi-Sunter (Splink)](https://moj-analytical-services.github.io/splink/)

**Depends on:** wave 6

---

## Wave 08: Product Master, Variants & Catalogs

**Phase 1 — Enterprise Structure, Master Data & Engineering Data**

**Goal.** Define products once (released per legal entity), with variants/dimensions, tracking dimensions, UoM conversions, attributes and catalogs.

**Replaces**

- *AX / D365:* Products and product masters, product dimensions (size/color/style/config), tracking dimensions (batch/serial), storage dimensions, released products, item model groups
- *SAP:* Material master (MARA/MARC/MARD), material types, variant configuration (LO-VC), batch/serial profiles, classification system

**Deliverable**

- Product and ProductMaster with dimension groups (product dims, storage dims, tracking dims), released-product per legal entity with site/warehouse defaults
- Attributes & classification (SAP CL-style classes) and category hierarchies
- Item groups, item model groups (costing method, negative inventory policy, physical/financial posting flags)
- Product configurator foundation: constraint-based configuration model (MiniZinc or OR-Tools) for configure-to-order
- GS1 identifiers (GTIN/SSCC), barcode generation, product images/documents
- Product relations (accessory, cross-sell, up-sell, substitute, spare part); configurable lifecycle states with per-process allow/warn/block policies; controlled group-change and mass obsolete/delete jobs
- Service products (service categories, purchasing text, service catalog); configurator decision tables and saved configurations; document info records with document types and full-text document search

**Proof (testable acceptance)**

- [ ] Variant explosion test: a master with 3 dimensions × 10 values each creates 1,000 variants in < 5 s and resolves lookups by dimension combination
- [ ] Configurator: a 50-constraint model returns valid configurations and rejects invalid ones (fixture set)
- [ ] Released-product defaults cascade correctly per site/warehouse (property test)
- [ ] GTIN check-digit validation across the GS1 test vectors

**Deploy**

Product information management workspace live; demo manufacturer's product catalog (500 items, 50 masters) loaded.

**References**

- [AX 2012 product information management](https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/about-products)
- [SAP material master](https://help.sap.com/docs/SAP_ERP)
- [GS1 General Specifications](https://www.gs1.org/standards/barcodes-epcrfid-id-keys/gs1-general-specifications)
- [Google OR-Tools CP-SAT](https://developers.google.com/optimization/cp/cp_solver)

**Depends on:** wave 6

---

## Wave 09: Engineering Data: BOMs, Routings, Resources & Change Management

**Phase 1 — Enterprise Structure, Master Data & Engineering Data**

**Goal.** A manufacturer can define multi-level BOMs, routings with operations and resources, and control changes with engineering change orders and effective dates.

**Replaces**

- *AX / D365:* BOM versions, BOM lines, routes/operations, resources & resource groups, work calendars, engineering change management (D365 ECM)
- *SAP:* Bills of material (CS01), routings (CA01), work centers (CR01), engineering change management (ECM/ECR/ECO), PLM integration

**Deliverable**

- BOM header/version/lines with effective dates, scrap, phantom items, by-products/co-products, formula (process) BOMs
- Routing versions, operations, operation relations (sequential/parallel), setup/run times, resource requirements by capability
- Resources (machine, tool, labor, vendor, location), resource groups, capabilities, work calendars with shifts
- Engineering change request/order workflow with impact analysis (where-used) and release to production
- BOM explosion/implosion API, where-used, cost roll-up skeleton (to be completed in wave 39)
- Engineering organizations and engineering versions; release product structure with release policies and product acceptance; product readiness policies and checks; engineering product categories with version nomenclature
- BOM usage/category, alternative item groups and discontinuation, serial and parameter effectivity, super BOM/routing selection conditions
- Engineering documents: document/text BOM lines, document status network with 21 CFR Part 11 signatures, ECO-controlled document versions, document structures and where-used; CAD/PLM eBOM import contract with eBOM-vs-mBOM comparison

**Proof (testable acceptance)**

- [ ] Multi-level BOM explosion for a 7-level, 2,000-component product completes in < 500 ms and matches a golden result
- [ ] Effective-date test: BOM as-of 2026-01-01 vs 2026-06-01 picks the right version
- [ ] ECO workflow: change approved → old version expired → new version active in production (Temporal test)
- [ ] Routing capacity calc: sequential/parallel operations produce correct lead times (fixtures)

**Deploy**

Engineering workspace; demo product line with BOMs and routings loaded; where-used report live.

**References**

- [AX 2012 BOMs and formulas](https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/about-bills-of-materials-and-formulas)
- [AX 2012 routes and operations](https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/about-routes-and-operations)
- [D365 engineering change management](https://learn.microsoft.com/en-us/dynamics365/supply-chain/engineering-change-management/product-engineering-overview)
- [SAP PP master data (BOM, routing, work center)](https://help.sap.com/docs/SAP_ERP)

**Depends on:** wave 8

---

## Wave 10: Data Migration Framework & Legacy Connectors

**Phase 1 — Enterprise Structure, Master Data & Engineering Data**

**Goal.** Repeatable, validated loads of AX and SAP master data into Passage with reconciliation reports — the tool every later wave uses for its own data.

**Replaces**

- *AX / D365:* Data Import Export Framework (DIXF), data management framework, data entities
- *SAP:* LSMW, SAP Migration Cockpit, BAPI/IDoc loads

**Deliverable**

- Migration pipeline: extract (ODBC from AX SQL, SAP RFC/OData), stage (Postgres staging schema), map (declarative mapping DSL), validate, load, reconcile
- AX 2012/D365 F&O connectors for master data tables (DirParty, CustTable, VendTable, InventTable, EcoResProduct, BOM, Route)
- SAP ECC/S4 connectors for KNA1/LFA1/MARA/MAST/STKO/PLKO via OData or extracted flat files
- Reconciliation: row counts, hash totals, sampled field compare, exception queue with re-run
- Dry-run and idempotent re-run; migration run ledger with lineage
- Copy entity data between legal entities; signed configuration data packages with diff-before-apply; recurring incremental import jobs until cutover; data templates
- Mapping packs for coverage-closure objects (OMTeam, VendCertification, PaymSched/T052R, KNRZE/LFZA, EcoResProductRelation, lifecycle states/T141, ASMD, CUVTAB, DRAW, ECM categories/policies, STPO alternatives, effectivity, document structures)

**Proof (testable acceptance)**

- [ ] A seeded AX-shaped dataset (50k customers, 20k vendors, 100k items, 10k BOMs) loads in < 30 min with 100% reconciliation
- [ ] Idempotency: re-running the same load produces zero changes
- [ ] Mapping DSL unit tests cover type coercion, enum mapping, default fill, and rejection reasons
- [ ] Phase 1 exit: demo manufacturer master data fully migrated from a synthetic AX extract

**Deploy**

Migration workspace with run history; connectors packaged as Helm sub-charts.

**References**

- [D365 data management framework](https://learn.microsoft.com/en-us/dynamics365/fin-ops-core/dev-itpro/data-entities/data-entities-data-packages)
- [AX 2012 DIXF](https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/data-import-export-framework-user-guide-dixf-dmf)
- [SAP S/4HANA Migration Cockpit](https://help.sap.com/docs/SAP_S4HANA_ON-PREMISE)
- [Great Expectations (data validation)](https://docs.greatexpectations.io/)

**Depends on:** wave 7, wave 8, wave 9

---
