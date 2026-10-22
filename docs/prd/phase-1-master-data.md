# Part 1 — Phase 1: Enterprise Structure, Master Data & Engineering Data (waves 6–10)

## 1.0 Phase overview

**Business outcome at phase exit.** A master-data steward can model the demo manufacturer end to end on Passage without touching AX or SAP: three legal entities, two plants, five warehouses and their hierarchies; one governed party record per customer, vendor and contact; a 500-item, 50-master product catalog released per legal entity; multi-level BOMs and routings under engineering change control; and a migration framework that has loaded all of it from a synthetic AX extract with 100% reconciliation. Every later phase (journal in wave 11, inventory in wave 16, orders in wave 26) filters by this enterprise structure, posts against these parties and products, and loads its own legacy data through this framework.

**Phase-exit sign-off criteria**
- `make demo-seed` loads the demo manufacturer (3 legal entities, 2 sites, 5 warehouses, 3 hierarchy purposes) in under 2 minutes and every list page re-scopes on legal-entity switch (property test over 20 entities, 0 leaks).
- Duplicate detection on the 10k-party fixture achieves precision ≥ 0.95 and recall ≥ 0.90; merge leaves zero dangling references (referential-integrity audit returns 0 rows).
- Variant explosion of a 3×10×10 master creates 1,000 variants in < 5 s; GS1 check-digit tests pass for all 40 published GS1 vectors.
- 7-level, 2,000-component BOM explosion completes in < 500 ms and equals the golden result byte for byte.
- Synthetic AX extract (50k customers, 20k vendors, 100k items, 10k BOMs) migrates in < 30 min; re-run produces 0 changes; reconciliation report shows 0 unexplained exceptions.
- Stakeholder walkthrough recorded: create party → release product → build BOM → approve ECO → run migration.

**Cross-wave data model summary**

| Entity | Owning service | Key relationships |
|---|---|---|
| LegalEntity | org-service (Go) | tenant 1:N legal_entity; legal_entity 1:N fiscal_calendar_assignment |
| OperatingUnit | org-service | type ∈ {department, cost_center, business_unit, value_stream}; node of HierarchyVersion |
| Site / Warehouse / Location | org-service | legal_entity 1:N site 1:N warehouse 1:N location |
| HierarchyType / HierarchyVersion / HierarchyNode | org-service | purpose-bound, effective-dated tree over OrgUnit ids |
| PostalAddress / AddressPurpose | org-service | referenced by Party, Site, Warehouse, LegalEntity |
| FiscalCalendar / FiscalPeriod | org-service | consumed by ledger (wave 11) for period status |
| UnitOfMeasure / UomConversion | org-service | product-specific conversions live in product-service |
| Party (Person/Organization) | party-service (Go) | 1:N PartyRole; 1:N PartyAddress; 1:N TaxRegistration |
| Customer / Vendor / Contact / Employee role | party-service | role rows keyed (party_id, legal_entity_id) |
| DuplicateCandidate / MergeCase | party-service | pairs of party_id with score; Temporal workflow id |
| Product / ProductMaster / ProductVariant | product-service (Go) | master 1:N variant; dimension_group FK |
| ReleasedProduct | product-service | (product_id, legal_entity_id) with site/warehouse defaults |
| ItemGroup / ItemModelGroup / Category | product-service | referenced from ReleasedProduct |
| ConfigurationModel | product-service (Python solver) | master 1:N model; constraints in JSON |
| BomHeader / BomVersion / BomLine | engineering-service (Go) | version → (product_id, site_id, effective range) |
| RouteHeader / RouteVersion / Operation / OperationRelation | engineering-service | operation N:1 resource requirement |
| Resource / ResourceGroup / Capability / WorkCalendar | engineering-service | calendar consumed by scheduling (wave 23) |
| EngineeringChangeRequest / EngineeringChangeOrder | engineering-service | ECO 1:N affected BomVersion/RouteVersion |
| MigrationProject / MigrationRun / MappingSpec / StagingRow / ReconciliationResult | migration-service (TS + Go loaders) | run N:1 project; row N:1 run |

**Cross-wave event catalog**

| Event name | Producer | Consumers | Payload summary |
|---|---|---|---|
| dev.passage.org.legalentity.created.v1 | org-service | party, product, ledger (w11), shell | legal_entity_id, code, name, country, currency, fiscal_calendar_id |
| dev.passage.org.hierarchy.version.activated.v1 | org-service | security (OPA bundle), reporting, procurement | hierarchy_type_id, version_id, effective_from, node_count |
| dev.passage.org.site.created.v1 / warehouse.created.v1 | org-service | product (released defaults), inventory (w16) | id, legal_entity_id, parent ids, address_id |
| dev.passage.org.fiscalcalendar.generated.v1 | org-service | ledger (w11), period close (w14) | calendar_id, pattern, period_count, year |
| dev.passage.party.created.v1 / updated.v1 | party-service | search index, migration, audit | party_id, type, name, primary_address_id |
| dev.passage.party.customer.created.v1 / vendor.created.v1 | party-service | AR (w30), AP (w19), sales (w26), purchasing (w17) | party_id, legal_entity_id, account_number, group, terms |
| dev.passage.party.merged.v1 | party-service | every service holding party_id FKs | survivor_id, merged_ids[], reference_rewrite_count |
| dev.passage.party.change.approved.v1 | party-service (Temporal) | audit, notification | party_id, field, old, new, approver |
| dev.passage.product.created.v1 / variant.created.v1 | product-service | engineering, inventory, pricing (w26) | product_id, master_id, dimension_values |
| dev.passage.product.released.v1 | product-service | inventory, purchasing, sales, costing | product_id, legal_entity_id, item_model_group_id, defaults |
| dev.passage.engineering.bom.version.activated.v1 | engineering-service | planning (w22), production (w21), costing (w39) | bom_version_id, product_id, site_id, effective_from, line_count |
| dev.passage.engineering.route.version.activated.v1 | engineering-service | planning, scheduling (w23), production | route_version_id, operations[] |
| dev.passage.engineering.eco.released.v1 | engineering-service | notification, audit, PLM adapter | eco_id, affected_versions[], release_date |
| dev.passage.migration.run.completed.v1 | migration-service | notification, reconciliation UI, audit | run_id, project_id, counts{staged, loaded, rejected}, hash_match |

**Phase-level risks**

| Risk | Likelihood | Impact | Mitigation | Owner |
|---|---|---|---|---|
| Party dedup thresholds tuned to synthetic data miss real-world noise | Medium | High | Splink model trained on 10k fixture plus 2k hand-labeled real-shaped records; thresholds adjustable per tenant | Product |
| Effective-dated hierarchies become slow under as-of queries at 50k nodes | Medium | Medium | Closure table per version, materialized on activation; p95 target 20 ms enforced in CI | Engineering |
| Product dimension explosion makes variant counts unbounded | Low | High | Hard cap 10,000 variants per master; explosion is a Temporal job with checkpointing | Engineering |
| BOM cycles or phantom recursion break explosion | Medium | High | Cycle check on every line insert (closure table), depth cap 25 | Engineering |
| AX/SAP extracts arrive with codepage and enum drift | High | Medium | Mapping DSL with explicit enum tables and reject-with-reason; dry-run before load | Implementation partner |
| Wave 10 depends on three prior waves landing on time | Medium | Medium | Connectors for DirParty/CustTable built against wave 7 as soon as it passes Proof | Product |

## 1.6 Wave 6: Enterprise Structure & Organization Model

### 1.6.1 Scope and business context

AX scopes everything by `DataAreaId` (CompanyInfo) and organizes units in `OMOperatingUnit` with hierarchies in `OMHierarchyType`/`OMHierarchyRelationship`; SAP scopes by `BUKRS` (T001) with plants (T001W), storage locations (T001L), sales organizations (TVKO) and purchasing organizations (T024E) wired together in SPRO. Wave 6 builds the single organization model every later service filters by. At the end of the wave an administrator can create a tenant's legal entities, operating units, sites, warehouses and locations; build effective-dated hierarchies for reporting, procurement and security purposes; attach validated postal addresses; generate fiscal calendars; and maintain countries, currencies, units of measure and languages. The shell's legal-entity switcher re-scopes every list page.

Out of scope: ledger period *statuses* (wave 14 owns open/closed per module), warehouse layout detail such as zones and location directives (wave 31), intercompany relationships (wave 40), and any transactional data.

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| AX organization hierarchy designer (OMHierarchyRelationship) only shows one effective date at a time; previewing "what changes on 2026-07-01" means publishing and re-opening; a mistaken publish cannot be undone | Hierarchy relationships are stored as flat ValidFrom/ValidTo rows with no version concept | Versioned trees with draft/published states, as-of slider and a diff view before publish (FR-6.3, FR-6.11) | Preview clicks 9→1; wrong-date publishes recoverable in 100% of cases via superseding version |
| SAP enterprise structure (OX02/OX10/OX09/OX18/OB29) requires 6+ IMG transactions plus assignments to define one plant; a missing assignment surfaces only when the first goods movement fails | Configuration spread across customizing tables (T001, T001W, T001K, T001L, T024E) with assignment tables between them | One `Site` create carries its legal entity, calendar and default warehouse; invariants are checked at save, not at first transaction (FR-6.5) | Transactions 6→1; structural errors found at save: 100% vs ~0% |
| AX company switch (DataAreaId) reloads the whole client and loses open forms; cross-company inquiries need `crossCompany` X++ | Company is a session property baked into every table | Header-scoped entity context; the shell switches without reload; read-only `ALL` scope behind a duty (FR-6.10) | Switch time 8–15 s→<300 ms; open views preserved 100% |
| Fiscal calendar period creation in AX (FiscalCalendarPeriod) and SAP (OB29 period tables) is manual per year; 4-4-5 week-53 years are keyed by hand and frequently wrong | No pattern generator | Pattern-driven generation with contiguity proof and golden fixtures (FR-6.7) | Setup effort 45 min/year→<10 s; boundary errors 0 against fixture |
| UoM conversion rounding silent in AX (UnitOfMeasureConversion stores Numerator/Denominator with decimal drift) and SAP (T006 integer ratios) | Ratio stored as scaled integers, rounded at storage | numeric(28,12) factor+offset, rounding only at display (FR-6.9, BR-6.8) | Round-trip error 1e-6→0 at 12 decimals across 50 pairs |

### 1.6.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Legal entity | CompanyInfo, `OMLegalEntity` form, DataAreaId | T001 company code (OX02) | org-service `LegalEntity`; Legal entities screen | full |
| Operating units | OMOperatingUnit (dept, cost center, BU, value stream) | Cost center CSKS (KS01), profit center CEPC, controlling area TKA01 | `OperatingUnit` with `type` enum | full |
| Org hierarchies | OMHierarchyType, OMHierarchyPurpose, OMHierarchyRelationship, Organization hierarchy designer | Standard hierarchy KSH1/KCH1, enterprise organization | `HierarchyType`/`HierarchyVersion`/`HierarchyNode`; Hierarchy designer | improved (multiple purposes with effective-dated versions and as-of API) |
| Sites | InventSite, Sites form | T001W plant (OX10) | `Site` | full |
| Warehouses / locations | InventLocation, WMSLocation | T001L storage location (OX09); LAGP bins | `Warehouse`, `Location` (bin-level detail deferred to wave 31) | partial |
| Addresses | LogisticsPostalAddress, LogisticsLocation, LogisticsLocationRole, DirPartyLocation | ADRC / ADRCT, address version | `PostalAddress`, `AddressPurpose` | full |
| Fiscal calendars | FiscalCalendar, FiscalCalendarYear, FiscalCalendarPeriod, Ledger calendar | T009 fiscal year variant (OB29), T009B periods | `FiscalCalendar`, `FiscalYear`, `FiscalPeriod` | full (status per ledger deferred to wave 14) |
| Currencies | Currency table, ISO 4217 | TCURC, TCURX decimals | `Currency` reference with minor units | full |
| Units of measure | UnitOfMeasure, UnitOfMeasureConversion (standard + product-specific) | T006 (CUNI), T006A | `UnitOfMeasure`, `UomConversion`; product-specific in wave 8 | full |
| Countries / regions / languages | LogisticsAddressCountryRegion, LogisticsAddressState, LanguageTable | T005, T005S, T002 | `Country`, `Region`, `Language` | full |
| Company-level parameters | CompanyInfo fields, Ledger (currency, calendar) | T001 fields (WAERS, PERIV, KTOPL) | `LegalEntity` fields + ledger linkage in wave 11 | partial (chart of accounts linkage arrives in wave 11) |

### 1.6.3 Functional requirements

- **FR-6.1 Tenant and legal-entity creation** — Must. The system shall create legal entities under a tenant with a unique 4–8 character code, legal name, country, accounting currency, default language and time zone.
  - Code is immutable after creation; names are effective-dated (name history table).
  - A legal entity cannot be deleted; it can be set `inactive`, which hides it from the switcher but keeps all references.
  - Maximum 500 legal entities per tenant in v1.
- **FR-6.2 Operating units** — Must. The system shall maintain operating units of type department, cost_center, business_unit and value_stream, each with a code unique within tenant and type.
  - Operating units are tenant-scoped, not legal-entity-scoped (mirrors AX, where OMOperatingUnit is shared), with an optional default legal entity.
  - A cost center may carry an optional `manager_party_id` resolved after wave 7.
- **FR-6.3 Multi-purpose effective-dated hierarchies** — Must. The system shall support hierarchy types with one or more purposes (reporting, procurement, expenditure, security, centralized_payments, custom) and versioned trees with `effective_from`/`effective_to` ranges that cannot overlap for the same type.
  - A node is any LegalEntity or OperatingUnit; one node appears at most once per version.
  - Draft versions may be edited freely; published versions are immutable; a new version is created by copying the current one.
  - Maximum depth 15, maximum 50,000 nodes per version.
- **FR-6.4 As-of hierarchy queries** — Must. The system shall answer parent, ancestors, descendants and path-to-root for any node as of any date with p95 < 20 ms at 50,000 nodes.
  - Implemented with a closure table materialized at publish time.
- **FR-6.5 Sites, warehouses and locations** — Must. The system shall maintain sites per legal entity, warehouses per site, and locations per warehouse, each with address, time zone and type (`standard`, `transit`, `quarantine`, `subcontractor` for warehouses).
  - Warehouse code unique per legal entity (as in AX InventLocationId per company); site code unique per legal entity.
  - A warehouse cannot move between sites once referenced by an inventory transaction (enforced from wave 16 via a reference-count check).
- **FR-6.6 Postal addresses with purposes** — Must. The system shall store structured postal addresses (ISO 3166-1 alpha-2 country, region, city, postal code, street lines, building, PO box) and attach them to any owner with purposes (business, delivery, invoice, remit_to, service, home, other) and a primary flag per purpose.
  - Country-specific formatting rules for the first seven countries (US, UK, DE, FR, IN, MX, CA); postal code regex per country.
  - Geocoding hook: an adapter interface returning lat/long and a confidence; default adapter is a no-op.
  - Addresses are effective-dated; an address edit creates a new version and keeps the old one for documents already printed.
- **FR-6.7 Fiscal calendars** — Must. The system shall generate fiscal years and periods from patterns: calendar-year (12 periods), 4-4-5 / 4-5-4 / 5-4-4 (12 periods + 53rd week handling), 13-period, and custom (user-entered period boundaries).
  - Each year has exactly one opening period and one closing period in addition to operating periods (AX convention), configurable off for SAP-style special periods 13–16.
  - Periods within a year are contiguous and non-overlapping; the generator proves this before saving.
- **FR-6.8 Reference data** — Must. The system shall seed and maintain ISO 3166 countries and regions, ISO 4217 currencies with minor units (JPY 0, KWD 3, USD/EUR 2), ISO 639-1 languages, and UCUM-aligned units of measure in classes (quantity, mass, length, volume, area, time, temperature, each).
  - Reference data is tenant-visible but platform-owned; tenants may add custom units and deactivate rows, not delete.
- **FR-6.9 UoM conversions** — Must. The system shall store conversions as `factor` and `offset` with numeric(28,12) precision and compute any-to-any within a class by path search of at most two hops.
  - Round-trip of 1 unit through A→B→A must return exactly 1 to 12 decimal places for the 50-pair fixture.
  - Temperature uses the offset (°C→°F: factor 1.8, offset 32).
- **FR-6.10 Legal-entity context switch** — Must. The system shall carry `X-Passage-LegalEntity` on every request; generated list pages filter by it, and the OPA policy denies requests for entities the user has no role assignment in.
- **FR-6.11 Hierarchy designer UI** — Should. The system shall provide a drag-and-drop tree editor with an effective-date slider that previews the tree as of a chosen date and highlights nodes that change between versions.
- **FR-6.12 Seed and export** — Should. The system shall export and import the complete organization model as a versioned JSON package (used by `make demo-seed` and by the migration framework in wave 10).
- **FR-6.13 Address validation adapter** — Could. The system shall expose an address-validation adapter interface (normalize, suggest) with a null default; third-party adapters are out of scope for this wave.

### 1.6.4 Data model

**LegalEntity**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| tenant_id | uuid | yes | RLS key |
| code | text(8) | yes | unique per tenant, immutable, `^[A-Z0-9]{2,8}$` |
| name | text(100) | yes | current name; history in legal_entity_name |
| country_code | char(2) | yes | FK country |
| accounting_currency | char(3) | yes | FK currency; immutable after first posting (wave 11) |
| default_language | text(5) | yes | BCP 47, default `en-US` |
| time_zone | text | yes | IANA zone |
| primary_address_id | uuid | no | FK postal_address |
| registration_number | text(40) | no | company registry number |
| status | enum | yes | active, inactive |
| created_at / updated_at / version | timestamptz, int | yes | optimistic lock on `version` |

**HierarchyVersion / HierarchyNode**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| hierarchy_type_id | uuid | yes | FK; type carries purposes[] |
| version_no | int | yes | monotonic per type |
| effective_from | date | yes | inclusive |
| effective_to | date | no | exclusive; null = open-ended |
| status | enum | yes | draft, published, superseded |
| node.id | uuid | yes | PK of hierarchy_node |
| node.version_id | uuid | yes | FK |
| node.org_unit_id | uuid | yes | LegalEntity or OperatingUnit id; unique per version |
| node.parent_node_id | uuid | no | null for root; exactly one root per version |
| node.sort_order | int | yes | sibling order |
| closure(ancestor_id, descendant_id, depth) | table | yes | rebuilt on publish |

**PostalAddress**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| tenant_id | uuid | yes | RLS |
| country_code | char(2) | yes | FK |
| region_code | text(10) | conditional | required for US, CA, IN, MX |
| city | text(60) | yes | |
| postal_code | text(12) | conditional | validated by country regex |
| street_1 / street_2 / street_3 | text(100) | street_1 yes | |
| building / po_box | text(20) | no | |
| latitude / longitude | numeric(9,6) | no | from geocoding hook |
| valid_from / valid_to | date | yes/no | effective dating |
| formatted | text | computed | rendered per country template |

**FiscalCalendar / FiscalPeriod** — calendar: id, tenant_id, code, name, pattern (enum), week_start; period: id, calendar_id, fiscal_year, period_no (0 = opening, 1–13 operating, 99 = closing), name, start_date, end_date, type (opening/operating/closing/adjustment).

**UnitOfMeasure / UomConversion** — uom: id, code (`^[A-Za-z0-9/%]{1,10}$`), name, class, decimal_precision (0–6), system_flag; conversion: from_uom_id, to_uom_id, factor numeric(28,12), offset numeric(28,12), product_id (null here, used in wave 8).

Invariants: exactly one root per published hierarchy version; no overlapping effective ranges per hierarchy type; fiscal periods of a year tile the year with no gap or overlap; a warehouse's legal entity equals its site's legal entity; every `primary` address purpose is unique per (owner, purpose). Indexing: `hierarchy_node(version_id, org_unit_id)` unique; closure table PK (ancestor_id, descendant_id); `postal_address` GIN trigram on `formatted` for search; all tables partitioned logically by tenant_id with RLS policies, no physical partitioning in this wave.

### 1.6.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | /org/v1/legal-entities | Create legal entity | `Idempotency-Key` header, 24 h | Maintain legal entities |
| GET | /org/v1/legal-entities/{id} | Read | n/a | View organization |
| PATCH | /org/v1/legal-entities/{id} | Update name/addresses/status | version check (409 on mismatch) | Maintain legal entities |
| POST | /org/v1/operating-units | Create operating unit | key header | Maintain operating units |
| POST | /org/v1/hierarchy-types/{id}/versions | Create draft version (copy of current) | key header | Maintain organization hierarchies |
| PUT | /org/v1/hierarchy-versions/{id}/nodes | Replace the full node set of a draft | full replace, naturally idempotent | Maintain organization hierarchies |
| POST | /org/v1/hierarchy-versions/{id}:publish | Publish; sets effective_from; builds closure | key header | Publish organization hierarchies |
| GET | /org/v1/hierarchy-types/{id}/as-of?date=2026-06-01&node=... | Ancestors/descendants/path as of a date | n/a | View organization |
| POST | /org/v1/sites, /org/v1/warehouses, /org/v1/locations | Create physical structure | key header | Maintain sites and warehouses |
| POST | /org/v1/addresses | Create address; `owner_type`, `owner_id`, `purposes[]` | key header | Maintain addresses |
| POST | /org/v1/fiscal-calendars/{id}:generate | Generate years from pattern | idempotent per (calendar, year) | Maintain fiscal calendars |
| GET | /org/v1/uom/convert?from=KG&to=LB&qty=10 | Convert quantities | n/a | any authenticated |
| POST | /org/v1/export, /org/v1/import | Org model package | import idempotent by code | Administer organization |

Commands validate: codes against regex; address against country rules; hierarchy publish rejects cycles, duplicate nodes, multiple roots, overlapping ranges; fiscal generation rejects a year already generated unless `force=true` and the year has no postings (checked via a ledger callback that returns `true` until wave 11 exists).

| Event (CloudEvents type) | Trigger | Payload fields | Consumers |
|---|---|---|---|
| dev.passage.org.legalentity.created.v1 | commit of legal entity | id, code, name, country_code, accounting_currency, time_zone | party, product, ledger, shell switcher |
| dev.passage.org.legalentity.updated.v1 | PATCH commit | id, changed_fields[], version | shell, search |
| dev.passage.org.operatingunit.created.v1 | commit | id, type, code, name | reporting, ledger dimensions |
| dev.passage.org.hierarchy.version.activated.v1 | publish | hierarchy_type_id, purposes[], version_id, effective_from, node_count | OPA bundle builder, reporting, procurement |
| dev.passage.org.site.created.v1 / warehouse.created.v1 / location.created.v1 | commit | id, legal_entity_id, parent_id, type, address_id | product, inventory |
| dev.passage.org.address.changed.v1 | new address version | address_id, owner_type, owner_id, purposes[], valid_from | document services |
| dev.passage.org.fiscalcalendar.generated.v1 | generate | calendar_id, fiscal_year, periods[] | ledger, period close |

### 1.6.6 User experience

| Screen | Primary actions | Key fields/columns | Duty required |
|---|---|---|---|
| Organization administration workspace | Tiles: legal entities, operating units, hierarchies, sites/warehouses, calendars, reference data | counts, last-changed, pending drafts | View organization |
| Legal entities list/detail | New, edit, deactivate, addresses tab, name history | code, name, country, currency, status | Maintain legal entities |
| Operating units list | New, bulk import (CSV), type filter | type, code, name, default legal entity | Maintain operating units |
| Hierarchy designer | Drag-drop nodes, add from picker, effective-date slider, diff vs previous version, publish | tree, effective range, node count, change badges | Maintain / Publish organization hierarchies |
| Sites and warehouses | New site, new warehouse under site, locations grid | code, name, type, address, time zone | Maintain sites and warehouses |
| Fiscal calendars | New calendar, generate years, period grid | pattern, year, period no, start, end | Maintain fiscal calendars |
| Reference data (countries, currencies, UoM, languages) | Deactivate, add custom UoM, add conversion, test conversion | code, name, class, factor, offset | Maintain reference data |

Interaction rules: all grids support inline edit with validation on blur and a single save per row; `Ctrl+S` saves, `Esc` cancels, `Alt+N` new; the hierarchy designer validates structurally on every drop and semantically (effective range) on publish only; personalization (column order, saved filters) is stored per user per screen by the wave-5 shell; the legal-entity switcher is in the top bar and persists the last choice.

### 1.6.7 Business rules and edge cases

- **BR-6.1** Legal entity codes are upper-cased on save and compared case-insensitively; `DAT`, `SYS` and `ALL` are reserved.
- **BR-6.2** Hierarchy version state machine: `draft → published → superseded`; `draft` may be deleted; `published` becomes `superseded` only when a later version publishes with `effective_from` greater than its own; a published version can be ended early by publishing a successor, never by editing.
- **BR-6.3** Publishing a version whose `effective_from` is earlier than today requires the duty "Backdate organization hierarchies" and is audited with a reason text (min 10 chars).
- **BR-6.4** Moving a node moves its subtree; dropping a node onto its own descendant is rejected with `ORG-HIER-CYCLE`.
- **BR-6.5** A security-purpose hierarchy triggers an OPA bundle rebuild within 60 s of activation; until the rebuild completes, the previous bundle stays in force (no window with no policy).
- **BR-6.6** Fiscal 4-4-5 years ending on a 53-week boundary add the extra week to the last period of the year; the generator records `week53 = true` on that period.
- **BR-6.7** A fiscal year may be regenerated only while no period of it has any posting; otherwise `ORG-CAL-LOCKED` (409).
- **BR-6.8** UoM conversions are stored once in a canonical direction; the inverse is computed as 1/factor with the offset negated and divided; results are rounded only at display, using the target unit's `decimal_precision`.
- **BR-6.9** Address edits are copy-on-write: documents hold the address version id, not the address id, so a printed invoice never changes retroactively.
- **BR-6.10** Concurrent edits use the `version` column; a stale PATCH returns 409 with the current representation; the UI offers merge or reload.
- **BR-6.11** Deactivating a legal entity is refused while any user has it as their default entity (`ORG-LE-DEFAULT-IN-USE`).

### 1.6.8 Acceptance criteria

- **AC-6.1** (FR-6.3, FR-6.4) Given 1,000 effective-dated nodes across 5 purposes with 3 versions each, when the as-of API is called for 10,000 random (node, date) pairs, then every parent matches the fixture oracle and p95 latency < 20 ms.
- **AC-6.2** (FR-6.10) Given a user assigned to legal entities USMF and DEMF, when they switch from USMF to DEMF, then all 20 generated list pages show only DEMF rows and a direct API call with `X-Passage-LegalEntity: FRMF` returns 403.
- **AC-6.3** (FR-6.9) Given the 50-pair conversion fixture, when each quantity is converted A→B→A, then the result equals the input to 12 decimal places.
- **AC-6.4** (FR-6.7) Given calendar patterns 4-4-5, 13-period and calendar-year for fiscal 2026, when generated, then period boundaries equal the committed fixture files exactly, including the 53rd week in the 4-4-5 variant for 2028.
- **AC-6.5** (FR-6.3, BR-6.2) Given a published version effective 2026-01-01, when a draft with `effective_from` 2025-12-01 is published without the backdate duty, then the request is rejected with 403 and no version changes state.
- **AC-6.6** (FR-6.6) Given a US address without `region_code`, when saved, then validation fails with field-level error `ORG-ADDR-REGION-REQUIRED`; with `region_code = CA` and postal code `9410`, then error `ORG-ADDR-POSTAL-FORMAT`.
- **AC-6.7** (FR-6.5) Given warehouse WH1 under site S1 in USMF, when a PATCH attempts to set `site_id` to a site in DEMF, then 422 `ORG-WH-LE-MISMATCH`.
- **AC-6.8** (FR-6.12) Given the demo seed package, when imported twice, then the second import reports 0 creates, 0 updates.
- **AC-6.9** (FR-6.11) Given the designer with the slider at 2026-06-01, when a node moved only in the draft version is viewed, then it renders with a "changed" badge and the previous parent shown in a tooltip.

### 1.6.9 Test plan and fixtures

Fixtures: `org/demo-manufacturer.json` (3 legal entities USMF/DEMF/FRMF, 2 sites, 5 warehouses, 40 operating units, 3 hierarchy types, generated); `org/hierarchy-1k.json` (1,000 nodes, 5 purposes, 3 versions each, generated with seed 6); `org/uom-pairs.csv` (50 pairs, hand-checked); `org/fiscal-patterns/*.csv` (4-4-5, 4-5-4, 5-4-4, 13-period, calendar for 2025–2028, hand-checked against the NRF retail calendar); `org/addresses-7-countries.csv` (140 valid, 70 invalid).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Hierarchy as-of oracle | property | as-of API | 10k samples, 0 mismatches, p95 < 20 ms |
| Closure rebuild | unit | publish | closure row count = Σ depth over nodes |
| Entity re-scope | property (Playwright) | 20 list pages | 0 cross-entity rows |
| UoM round-trip | unit | convert | 12-decimal equality |
| Fiscal generation | golden | generator | byte-equal CSV |
| Address validation | table-driven | validator | 210/210 expected outcomes |
| Publish under load | performance | 50k-node publish | < 30 s, as-of p95 < 20 ms after |
| Event contract | contract (AsyncAPI) | 7 events | schema registry accepts; consumer stubs decode |

Reconciliation: node count per published version = closure rows with depth 0; count of warehouses per legal entity in export = count in database; every legal entity has exactly one `primary` business address or none.

### 1.6.10 Deployment and operations

Helm chart `passage-org` (Deployment ×2 replicas, CloudNativePG cluster `org-db` with 3 instances, Debezium connector CR for the outbox table). Config: `ORG_HIERARCHY_MAX_NODES=50000`, `ORG_GEOCODER=null`, `ORG_SEED_PACKAGE=demo-manufacturer`. Migrations via Atlas from entity metadata, applied by an Argo CD pre-sync Job. Feature flags: `org.hierarchy_designer`, `org.address_validation_adapter`. Scheduled job: nightly closure-table integrity check (Temporal cron `0 2 * * *`). SLOs: read p95 < 50 ms, as-of p95 < 20 ms, availability 99.9%. Alerts: outbox lag > 60 s, closure integrity failure, OPA bundle rebuild older than 5 min after a security hierarchy activation. Runbook: "Hierarchy publish stuck" (check Temporal workflow `publish-hierarchy-{id}`), "Rebuild closure table" (`passagectl org rebuild-closure --version`). Rollback: Argo rollback to the previous digest; migrations in this wave are additive only, so no down-migration is needed.

### 1.6.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| AX CompanyInfo / OMLegalEntity (DataAreaId, Name, LanguageId, CurrencyCode via Ledger) | LegalEntity | DataAreaId → code (upper-cased); Ledger.AccountingCurrency → accounting_currency |
| AX OMOperatingUnit (OMOperatingUnitNumber, OMOperatingUnitType) | OperatingUnit | type enum 1..4 → department/cost_center/business_unit/value_stream |
| AX OMHierarchyType, OMHierarchyPurpose, OMHierarchyRelationship (ValidFrom/ValidTo) | HierarchyType, HierarchyVersion, HierarchyNode | each distinct ValidFrom in AX becomes a version; ValidTo max date → null |
| AX InventSite, InventLocation | Site, Warehouse | InventLocation.InventSiteId → site FK; warehouse type from InventLocation.WMSLocationId usage |
| AX LogisticsPostalAddress, LogisticsLocation, LogisticsLocationRole, DirPartyLocation | PostalAddress, AddressPurpose | roles Business/Delivery/Invoice/RemitTo map 1:1; others → other |
| AX FiscalCalendar, FiscalCalendarYear, FiscalCalendarPeriod | FiscalCalendar, FiscalPeriod | Type Opening/Operating/Closing → period type |
| AX UnitOfMeasure, UnitOfMeasureConversion (non product-specific) | UnitOfMeasure, UomConversion | Factor/Numerator/Denominator → factor; InnerOffset/OuterOffset → offset |
| SAP T001 (BUKRS, BUTXT, LAND1, WAERS, SPRAS, PERIV) | LegalEntity | PERIV → fiscal_calendar assignment |
| SAP T001W (WERKS, NAME1, ADRNR) / T001L (LGORT) | Site + Warehouse | plant → site; a default warehouse per plant is created when T001L is empty |
| SAP CSKS / CEPC / T001K | OperatingUnit | cost center, profit center; KOKRS stored as attribute |
| SAP ADRC (ADDRNUMBER, NAME1, STREET, POST_CODE1, CITY1, COUNTRY, REGION) | PostalAddress | DATE_FROM → valid_from |
| SAP T009 / T009B | FiscalCalendar, FiscalPeriod | special periods 13–16 → adjustment periods when `sap_special_periods=true` |
| SAP T006 / T006A / TCURC / TCURX / T005 | reference data | matched to ISO seed by code; unmatched custom units created |

Validation: every migrated warehouse resolves to a site; every address passes country rules or lands in the exception queue; hierarchy versions must tile without gaps. Not migrated: AX `OMInternalOrganization` team records (no Passage equivalent in v1), SAP sales/purchasing organizations (modeled in waves 17 and 26 as procurement and sales hierarchies), SAP client (MANDT) which maps to tenant by configuration.

### 1.6.12 Dependencies, risks, open questions

Depends on: wave 2 (tenant model, RLS, OPA duties), wave 3 (entity metadata and generator), wave 4 (outbox, Temporal for publish jobs), wave 5 (shell, generated forms, audit, number sequences for operating unit numbers).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Closure-table rebuild blocks publishing large versions | Medium | Medium | Rebuild runs as a Temporal activity; version stays `publishing` until done |
| Country address rules incomplete beyond 7 countries | High | Low | Generic fallback rule; country packs in wave 48 |
| Shell switcher semantics conflict with cross-entity screens (consolidation) | Low | Medium | Switcher supports `ALL` for read-only cross-entity views behind a duty |

Open questions: (1) Should operating units be legal-entity-scoped as an option for SAP-style cost centers tied to a controlling area? Proposed default: tenant-scoped with optional default legal entity; decide by wave 11. (2) Do we store SAP special periods as adjustment periods or as flags on period 12? Proposed: adjustment periods; decide by wave 14. (3) Is UCUM the canonical UoM code set or ISO 80000? Proposed: UCUM codes with ISO display names; decide by wave 8.

## 1.7 Wave 7: Global Party & Business Partner Model

### 1.7.1 Scope and business context

AX keeps one `DirPartyTable` (subtyped into `DirPerson` and `DirOrganization`) under the global address book, with `CustTable` and `VendTable` as company-specific roles pointing at the party; SAP ECC keeps separate `KNA1`/`KNB1` (customer general/company-code) and `LFA1`/`LFB1` (vendor) masters, and S/4 forces them into Business Partner (`BUT000`, roles in `BUT100`, addresses in `BUT020`/`ADRC`) through Customer-Vendor Integration. Wave 7 builds the party model once: one Party per real-world organization or person, with Customer, Vendor, Employee, Contact, Prospect and Bank roles layered per legal entity. At the end of the wave a steward can create and govern parties and roles, run duplicate detection and merge, validate tax registrations, and import a legacy CustTable/VendTable extract. Posting profiles, payment terms and credit limits are captured now as data; the GL hooks that use them arrive in waves 11, 19 and 30.

Out of scope: customer/vendor transactions and balances, bank account verification (wave 36), employee HR attributes beyond the role stub (wave 42), CRM pipeline (non-goal).

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| In ECC the same supplier who is also a customer exists as LFA1 and KNA1 with two addresses that drift; S/4 CVI (`CVI_FS_CHECK_CUSTOMIZING`, `MDS_LOAD_COCKPIT`) is a months-long synchronization project | Two masters designed independently, linked by KNA1-LIFNR / LFA1-KUNNR cross references | Party is the only identity; roles reference it; addresses are shared by purpose (FR-7.1, FR-7.2) | Duplicate identities per real-world entity 2+→1; address drift incidents 0 |
| AX duplicate check on DirPartyTable is exact-name only (`DirPartyTable.checkDuplicate`); near-duplicates ("ACME Corp." vs "Acme Corporation") pass and are found at year-end | No probabilistic matching | Fellegi-Sunter model (Splink) on name/tax id/address/phone with blocking (FR-7.5) | Planted near-duplicates caught: recall ≥ 0.90 vs ≈0.30 for exact match |
| Merging two vendors in AX is manual: re-point every open PO, settlement, and address, or leave one blocked forever; SAP has no merge, only block (XK05) | No merge primitive; FKs scattered across modules | Merge workflow rewrites references atomically through a consumer contract and keeps full role data (FR-7.6) | Merge effort 2–4 h→<1 min; dangling references 0 |
| VAT number entry (CustTable.VATNum / KNA1-STCEG) is free text; wrong numbers surface as EU sales list rejections months later | No validation at entry | Country-specific format checks plus VIES/TIN adapters at save (FR-7.4) | Invalid tax ids at save-time rejected 100%; downstream filing rejections →0 |
| Creating a customer in AX touches CustTable form, address tab, contact info, posting profile, payment terms: 12+ clicks across 4 tabs; SAP XD01 walks 3 views × 5 tabs | Role and party data mixed in one wide form | One create flow with inline role panels and defaults from customer group (FR-7.3, FR-7.7) | Clicks 12→3; time-to-create 4 min→30 s |

### 1.7.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Party | DirPartyTable, DirPerson, DirOrganization, DirPartyTable form (Global address book) | BUT000 (BP), BUT100 roles; ECC KNA1/LFA1 as separate masters | party-service `Party` with `party_type` person/organization | full |
| Customer role | CustTable (AccountNum, CustGroup, PaymentTermId, CreditMax, InvoiceAccount), CustGroup, Customers list page | KNA1 (KUNNR), KNB1 (company code), KNVV (sales area), XD01/FD01 | `CustomerRole` (party_id, legal_entity_id) | full (sales-area data KNVV arrives in wave 26) |
| Vendor role | VendTable (AccountNum, VendGroup, PaymentTermId), VendGroup, Vendors list page | LFA1, LFB1, LFM1 (purchasing org), XK01/FK01 | `VendorRole` | full (LFM1 in wave 17) |
| Contacts / persons | ContactPerson, DirPersonName (first/middle/last), DirPersonUser | KNVK contact person, BP person category | `ContactRole`, `PersonName` | full |
| Employee / worker | HcmWorker, DirPerson link | PA0001/PA0002, BP employee role | `EmployeeRole` stub (personnel number, legal entity) | partial (HR data in wave 42) |
| Addresses and contact info | DirPartyLocation, LogisticsPostalAddress, LogisticsElectronicAddress (phone/email/URL) | ADRC, ADR2 (phone), ADR6 (email), BUT020 | `PartyAddress` (wave-6 address + purposes), `ElectronicAddress` | full |
| Tax registrations | TaxRegistration, TaxRegistrationType, CustTable.VATNum, VendTable.VATNum | KNA1-STCEG/STCD1..5, LFA1-STCEG, DFKKBPTAXNUM (BP tax numbers) | `TaxRegistration` (type, country, number, validated_at, result) | improved (validation adapters) |
| Duplicate detection / merge | DirPartyTable duplicate check (exact), Merge parties (D365 "Merge" on DirPartyTable, limited) | None in ECC; S/4 MDG-BP matching (separate license) | `DuplicateCandidate`, `MergeCase`, Temporal merge workflow | improved |
| Groups, posting profiles, terms | CustGroup/VendGroup, CustLedger/VendLedger posting profiles, PaymentTerm, CustPaymMode, DlvTerm, DlvMode | KNB1-AKONT reconciliation account, T052 payment terms (ZTERM), TINC Incoterms, KNVV-VSBED | `CustomerGroup`, `VendorGroup`, `PostingProfile` (account fields filled in wave 11), `PaymentTerm`, `DeliveryTerm`, `DeliveryMode` | full (GL accounts bound in wave 11) |
| Credit management | CustTable.CreditMax, CreditRating, blocked status (CustBlocked) | KNKK credit limit (FD32), UKM in S/4 | `CreditProfile` (limit, currency, rating, hold) | partial (credit checks run in wave 27) |
| Bank role | BankAccountTable per customer/vendor (CustBankAccount/VendBankAccount) | BP bank details BUT0BK, LFBK/KNBK | `BankRole` plus `PartyBankAccount` (IBAN/BIC format check only) | partial (verification in wave 36) |
| Governance | Workflow on vendor changes (VendTable change proposals in D365), DirPartyTable data owner | MDG change requests, SAP workflow | Stewardship queue, change-approval workflow on protected fields | improved |

### 1.7.3 Functional requirements

- **FR-7.1 Party core** — Must. The system shall create parties of type organization (legal name, trade name, registration number, industry code NAICS/NACE) or person (given, middle, family name, name prefix/suffix, date of birth optional, gender optional) with a tenant-wide party number from a number sequence.
  - Name search uses trigram similarity on `search_name` (normalized: casefold, diacritics stripped, punctuation removed).
  - Party `status` ∈ {active, inactive, merged}; merged parties keep a `merged_into_id` and all reads redirect.
- **FR-7.2 Roles per legal entity** — Must. The system shall attach Customer, Vendor, Contact, Employee, Prospect and Bank roles to a party; Customer and Vendor roles are keyed by (party_id, legal_entity_id) and carry an account number from the legal entity's customer/vendor number sequence.
  - A party may hold Customer and Vendor roles in the same legal entity (the AX "one party, two accounts" case).
  - Role deactivation keeps the row and sets `blocked = all` with a reason.
- **FR-7.3 Group defaults** — Must. The system shall maintain customer groups and vendor groups carrying default payment term, delivery term, delivery mode, posting profile, price/discount group placeholders and tax group placeholders; creating a role copies group defaults into the role and records `defaulted_from_group`.
- **FR-7.4 Tax registrations with validation** — Must. The system shall store tax registration numbers by type (VAT, EIN, SSN-masked, GSTIN, ABN, CNPJ, RFC, UTR) with per-country format validation (regex plus check digit where defined: GSTIN mod-36, ABN mod-89, CNPJ/RFC algorithms, UK VAT mod-97) and optional online validation through adapters.
  - VIES adapter calls `checkVatService` with a 5 s timeout; result cached 30 days; outage yields `validation_status = unverified`, never a save failure.
  - IRS TIN matching adapter is an interface with a recorded fixture implementation in v1.
- **FR-7.5 Duplicate detection** — Must. The system shall score candidate pairs on every create/update and on demand, using blocking on (normalized name first 4 chars | tax id | postal code+street number | phone last 7 | email domain) and a Fellegi-Sunter model with comparison levels for name (Jaro-Winkler ≥ 0.92 / ≥ 0.85 / else), tax id (exact), address (street+postal exact / postal only / else), phone, email.
  - Match weight ≥ 12 (probability ≈ 0.99) auto-flags as `likely_duplicate`; 6–12 flags `possible`; below 6 ignored.
  - Create with a `likely_duplicate` candidate returns 409 with the candidates unless `X-Passage-Override-Duplicate: true` and the duty "Override duplicate check".
  - Scoring of a single party against 1M parties completes in < 300 ms using blocking indexes.
- **FR-7.6 Merge workflow and survivorship** — Must. The system shall merge N parties into one survivor through a Temporal workflow: pick survivor, resolve field conflicts by survivorship rules (most-recently-verified tax id, most-recently-updated address, non-empty wins, manual override), union roles (conflicting roles in the same legal entity require explicit choice), then rewrite references in every consuming service via `party.merged.v1` with acknowledgement tracking.
  - Merge is reversible for 30 days through a stored pre-merge snapshot.
  - The workflow does not complete until every registered consumer acknowledges rewrite; stragglers alert after 10 min.
- **FR-7.7 Electronic addresses** — Must. The system shall store phone (E.164 normalized), email (RFC 5322 syntax check), URL, fax, with purposes and a primary flag per type.
- **FR-7.8 Credit profile** — Should. The system shall store credit limit with currency, credit rating, hold flag, and hold reason on the Customer role; changes to limit are protected fields under FR-7.10.
- **FR-7.9 Payment, delivery terms and modes** — Must. The system shall maintain payment terms (net days, day-of-month, cash discount percent and days, up to 3 discount tiers), delivery terms (Incoterms 2020 list seeded), and delivery modes; terms compute due dates deterministically (`due = invoice_date + net_days`, or next day-of-month ≥ invoice_date + offset_months).
- **FR-7.10 Change-approval workflow on protected fields** — Must. The system shall route changes to protected fields (bank account, tax registration, credit limit, payment term on vendor, remit-to address) through an approval workflow configured per legal entity; the change is held in `pending_change` and not visible to consumers until approved.
  - SLA 2 business days then escalation; rejection records a reason.
- **FR-7.11 Stewardship queue and ownership** — Should. The system shall assign a data owner (user or team) per party and show stewards a queue of duplicates, unverified tax ids, pending changes and incomplete records (missing primary address or primary email), sorted by age.
- **FR-7.12 Legacy import** — Must. The system shall import CustTable/VendTable/DirPartyTable and KNA1/LFA1 extracts (CSV) through the wave-5 import tool with the wave-10 mapping specs pre-seeded; duplicates found during import go to the stewardship queue rather than blocking the load.
- **FR-7.13 Party search API** — Must. The system shall expose typeahead search returning up to 20 parties in < 100 ms p95 over 1M rows, filtered by role and legal entity.

### 1.7.4 Data model

**Party**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| tenant_id | uuid | yes | RLS |
| party_number | text(20) | yes | number sequence `PARTY`, unique per tenant |
| party_type | enum | yes | organization, person |
| name | text(200) | yes | display name; computed for persons |
| search_name | text(200) | yes | normalized; trigram index |
| org.legal_name / org.trade_name / org.registration_number / org.industry_code | text | legal_name yes | organization subtype |
| person.given_name / middle_name / family_name / prefix / suffix / dob | text, date | given+family yes | person subtype; PII-flagged |
| status | enum | yes | active, inactive, merged |
| merged_into_id | uuid | no | set when status = merged |
| owner_user_id / owner_team_id | uuid | no | stewardship |
| language_code / time_zone | text | no | |
| version, created_at, updated_at | int, timestamptz | yes | |

**CustomerRole** (VendorRole is symmetric with `vendor_group_id`, `vendor_hold`)

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| party_id | uuid | yes | FK party |
| legal_entity_id | uuid | yes | FK; unique (party_id, legal_entity_id) |
| account_number | text(20) | yes | sequence `CUST-{LE}`; unique per legal entity |
| customer_group_id | uuid | yes | FK |
| payment_term_id / delivery_term_id / delivery_mode_id | uuid | payment yes | FK |
| posting_profile_id | uuid | yes | FK; GL accounts attached in wave 11 |
| invoice_account_party_id | uuid | no | bill-to party (AX InvoiceAccount) |
| currency_code | char(3) | yes | default accounting currency |
| credit_limit / credit_currency / credit_rating / credit_hold | numeric(18,2), char(3), text, bool | no | protected fields |
| blocked | enum | yes | none, invoice, all |
| sales_tax_group_id | uuid | no | bound in wave 13 |
| version | int | yes | |

**TaxRegistration**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| party_id | uuid | yes | FK |
| registration_type | enum | yes | VAT, EIN, GSTIN, ABN, CNPJ, RFC, UTR, OTHER |
| country_code | char(2) | yes | |
| number | text(30) | yes | stored normalized (no spaces); unique (type, country, number) per tenant |
| valid_from / valid_to | date | no | |
| validation_status | enum | yes | unverified, valid, invalid, format_only |
| validated_at / validation_source | timestamptz, text | no | VIES, IRS_TIN, manual |
| is_primary | bool | yes | one per (party, country) |

**DuplicateCandidate / MergeCase** — candidate: id, party_a_id, party_b_id (a < b), match_weight numeric(8,4), probability, level_detail jsonb, status (open, dismissed, merged), created_at; merge_case: id, survivor_id, merged_ids uuid[], survivorship jsonb, snapshot jsonb, workflow_id, status (draft, running, awaiting_ack, completed, reverted, failed), consumer_acks jsonb.

Invariants: a party with status `merged` has no active roles; `account_number` never reused after merge (merged role numbers are retained as aliases in `customer_alias`); at most one primary tax registration per (party, country); every role has a posting profile. Indexing: `party` GIN trigram on `search_name`; btree on `(tenant_id, party_type, status)`; blocking indexes on `left(search_name,4)`, `tax_registration.number`, `electronic_address.normalized`; `duplicate_candidate(status, match_weight desc)`.

### 1.7.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | /party/v1/parties | Create party (runs dedup) | `Idempotency-Key` | Maintain parties |
| GET | /party/v1/parties/{id} | Read (follows merged_into) | n/a | View parties |
| PATCH | /party/v1/parties/{id} | Update; protected fields → pending change | version check | Maintain parties |
| GET | /party/v1/parties:search?q=&role=customer&legalEntity= | Typeahead | n/a | View parties |
| POST | /party/v1/parties/{id}/roles/customer | Add customer role in `X-Passage-LegalEntity` | key header | Maintain customers |
| POST | /party/v1/parties/{id}/roles/vendor | Add vendor role | key header | Maintain vendors |
| POST | /party/v1/parties/{id}/tax-registrations | Add and validate | key header | Maintain parties |
| POST | /party/v1/tax-registrations/{id}:validate | Re-run adapter | idempotent (cached 30 d) | Maintain parties |
| GET | /party/v1/duplicates?status=open&minWeight=6 | Stewardship list | n/a | Steward master data |
| POST | /party/v1/merge-cases | Start merge workflow | key header | Merge parties |
| POST | /party/v1/merge-cases/{id}:revert | Revert within 30 days | key header | Merge parties |
| POST | /party/v1/pending-changes/{id}:approve / :reject | Workflow decisions | key header | Approve master data changes |
| GET | /party/v1/payment-terms/{id}/due-date?invoiceDate=2026-03-31 | Compute due date | n/a | any authenticated |

Validation: tax registration format per country before adapter call; role creation requires the legal entity header and an existing group; merge requires all parties active and in the same tenant; search `q` minimum 2 characters.

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| dev.passage.party.created.v1 | commit | party_id, party_number, party_type, name, primary_address_id | search index, migration, audit |
| dev.passage.party.updated.v1 | commit of non-protected change | party_id, changed_fields[], version | search, consumers caching names |
| dev.passage.party.customer.created.v1 | role commit | party_id, legal_entity_id, account_number, customer_group_id, payment_term_id, posting_profile_id, currency_code | AR, sales, pricing, credit |
| dev.passage.party.vendor.created.v1 | role commit | party_id, legal_entity_id, account_number, vendor_group_id, payment_term_id | AP, purchasing |
| dev.passage.party.duplicate.flagged.v1 | score ≥ 6 | candidate_id, party_a_id, party_b_id, match_weight | stewardship queue, notification |
| dev.passage.party.merged.v1 | merge workflow enters awaiting_ack | survivor_id, merged_ids[], role_map{old_account→new_account}, merge_case_id | every service with party FKs; each replies on `dev.passage.party.merge.ack.v1` |
| dev.passage.party.change.approved.v1 / rejected.v1 | workflow decision | party_id, field, old, new, approver_id, reason | audit, notification |
| dev.passage.party.taxregistration.validated.v1 | adapter result | registration_id, status, source, validated_at | tax (wave 13), audit |

### 1.7.6 User experience

| Screen | Primary actions | Key fields/columns | Duty required |
|---|---|---|---|
| Global address book (parties list) | New party, search, filter by role/entity, open duplicates | party number, name, type, roles, primary address, status | View parties |
| Party detail | Edit, add role panels (customer/vendor/contact), addresses, electronic addresses, tax registrations, relationships, audit tab | all party fields; role panels collapsed by default | Maintain parties |
| Customers workspace (per legal entity) | New customer (party picker or inline create), block, edit terms, credit tab | account, name, group, terms, credit limit, hold, blocked | Maintain customers |
| Vendors workspace | New vendor, block, bank accounts (pending-change aware) | account, name, group, terms, bank verified flag | Maintain vendors |
| Duplicate review | Side-by-side compare, dismiss, start merge, pick survivor per field | match weight, field-level comparison levels | Steward master data |
| Merge case monitor | Progress, consumer acks, revert | consumers acked / pending, elapsed | Merge parties |
| Stewardship queue | Claim, resolve, bulk dismiss | item type, party, age, owner | Steward master data |
| Pending changes (approvals) | Approve/reject with reason | field, old/new, requester, SLA remaining | Approve master data changes |

Interaction rules: the create-party dialog runs dedup after name and country are entered (debounced 400 ms) and shows candidates inline before save; `Ctrl+Enter` saves and opens the role panel; bulk block/unblock from the list is limited to 500 rows per action; protected fields render with a lock badge and show "pending" with the proposed value until approval; personalization of role panels (which are expanded) is per user.

### 1.7.7 Business rules and edge cases

- **BR-7.1** Party status machine: `active ↔ inactive`; `active → merged` only through a merge case; `merged` is terminal except via revert within 30 days, which restores the snapshot and sets `active`.
- **BR-7.2** Merge case machine: `draft → running → awaiting_ack → completed`; `running` or `awaiting_ack → failed` on activity error after 5 retries; `completed → reverted` within 30 days only when no consumer reports a post-merge transaction referencing the survivor that cannot be split (consumer rejects revert with reason).
- **BR-7.3** Survivorship default order per field: manual override > most recently validated (tax ids) > most recently updated > non-empty > survivor's value.
- **BR-7.4** Account numbers of merged roles become aliases; lookups by alias resolve to the survivor role and are logged for 12 months to catch stale integrations.
- **BR-7.5** Dedup weights are recomputed for an updated party only against candidates from its blocking keys; a full re-score job runs weekly (Temporal cron, Sunday 01:00 tenant local).
- **BR-7.6** Tax registration numbers are normalized before uniqueness checks (strip spaces, hyphens, dots; uppercase); two parties with the same VAT number in the same country are auto-flagged with weight +15.
- **BR-7.7** Payment term due date: `net_days` adds calendar days; `day_of_month` moves to that day in the month `offset_months` ahead, clamped to the last day; cash discount date computed the same way per tier; if discount date > due date the term is invalid at save.
- **BR-7.8** Protected-field changes by a user holding "Approve master data changes" still require a second approver (no self-approval).
- **BR-7.9** A person party's `dob` and government ids are PII-flagged: masked in lists, visible in detail only to duties with `pii.read`, excluded from search index and from CloudEvent payloads.
- **BR-7.10** Deleting a party is never allowed; `inactive` parties cannot receive new roles; roles on an `inactive` party cannot be unblocked.
- **BR-7.11** Concurrent merges touching the same party are serialized by a Temporal workflow id `merge-{tenant}-{min(party_ids)}`; the second request receives 409 with the running case id.
- **BR-7.12** Import rows with a `likely_duplicate` candidate are loaded with status `active` and a candidate record; they are not rejected, because migrations must be complete and reviewed afterward.

### 1.7.8 Acceptance criteria

- **AC-7.1** (FR-7.5) Given 10,000 seeded parties with 500 planted near-duplicates (name typos, swapped address lines, formatted tax ids), when the full re-score job runs, then flagged pairs with weight ≥ 6 achieve precision ≥ 0.95 and recall ≥ 0.90 against the labels.
- **AC-7.2** (FR-7.6) Given parties A and B each with a Customer role in USMF and B with a Vendor role in DEMF, when merged into A, then A holds all three roles, B's account numbers are aliases, every registered consumer acks, and the referential-integrity audit reports 0 rows referencing B.
- **AC-7.3** (FR-7.4) Given the recorded VIES fixture of 200 VAT numbers, when validated, then results equal the fixture; given the adapter stub returns a timeout, then the registration saves with `unverified` and an alert is raised after 3 consecutive timeouts.
- **AC-7.4** (FR-7.10) Given a customer with credit limit USD 50,000, when a user raises it to USD 120,000, then the stored limit stays 50,000, a pending change exists, the Temporal workflow has an open approval task, and after approval the limit is 120,000 with an `approved` event.
- **AC-7.5** (FR-7.2) Given a party with a Customer role in USMF, when a Vendor role is created in USMF, then both exist with distinct account numbers from the `CUST-USMF` and `VEND-USMF` sequences.
- **AC-7.6** (FR-7.9) Given payment term "Net 30 EOM+1" (day_of_month 31, offset 1) and invoice date 2026-01-31, when due date is computed, then it equals 2026-02-28; with cash discount 2% at 10 days, discount date is 2026-02-10.
- **AC-7.7** (FR-7.5) Given a `likely_duplicate` candidate at create, when the client retries with the override header but without the override duty, then 403.
- **AC-7.8** (FR-7.12) Given a 1,000-row CustTable extract with 20 duplicates of existing parties, when imported, then 1,000 roles exist, 20 duplicate candidates are open, and 0 rows are rejected for duplication.
- **AC-7.9** (FR-7.13) Given 1M parties, when typeahead `q=acm` is called 1,000 times, then p95 < 100 ms and results are ordered by similarity then name.
- **AC-7.10** (BR-7.9) Given a person with `dob`, when the list page is rendered by a user without `pii.read`, then the column shows `••••` and the CloudEvent payload has no `dob` key.

### 1.7.9 Test plan and fixtures

Fixtures: `party/parties-10k.jsonl` (generated with Faker seed 7, 500 labeled duplicate pairs in `party/dup-labels.csv`); `party/vies-recorded.json` (200 numbers, 150 valid, 50 invalid, recorded 2026-09); `party/tax-format-vectors.csv` (300 valid/invalid across 8 types); `party/custtable-extract.csv` and `party/kna1-extract.csv` (1,000 rows each, AX/SAP column names verbatim); `party/payment-terms.csv` (25 terms with 100 invoice-date → due-date oracle rows).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Dedup precision/recall | property | Splink model | P ≥ 0.95, R ≥ 0.90 |
| Single-party scoring latency | performance | scorer | < 300 ms at 1M |
| Merge integrity | E2E (Temporal test server) | merge workflow | 0 dangling FKs, all acks |
| Merge revert | E2E | revert | snapshot restored byte-equal |
| Tax format vectors | table-driven | validators | 300/300 |
| VIES adapter | contract (recorded) | adapter | matches fixture; timeout path |
| Change approval | Temporal workflow test | protected fields | blocked until approve; SLA timer fires after 2 business days |
| Due-date oracle | unit | payment terms | 100/100 |
| PII masking | contract | API + events | no PII fields in list or events |
| Typeahead | performance | search | p95 < 100 ms |

Reconciliation: count(roles) per legal entity in import = source row count; Σ open candidates + dismissed + merged = total pairs ever flagged; every merged party has exactly one merge case.

### 1.7.10 Deployment and operations

Helm chart `passage-party` (service ×2, CloudNativePG `party-db`, Debezium connector, Splink scoring worker as a Python Deployment with 2 replicas, Temporal namespace `party`). Config: `PARTY_DEDUP_LIKELY_WEIGHT=12`, `PARTY_DEDUP_POSSIBLE_WEIGHT=6`, `PARTY_VIES_TIMEOUT_MS=5000`, `PARTY_MERGE_ACK_TIMEOUT_MIN=10`, `PARTY_APPROVAL_SLA_BUSINESS_DAYS=2`. Migrations additive via Atlas. Feature flags: `party.online_tax_validation`, `party.merge_revert`, `party.pii_masking` (default on). Scheduled jobs: weekly full re-score; daily VIES re-validation of registrations older than 30 days (max 5,000 per run); hourly stale-merge sweeper. SLOs: create p95 < 400 ms including dedup; search p95 < 100 ms; merge completion p95 < 5 min. Alerts: merge awaiting ack > 10 min; VIES failure rate > 20% over 15 min; duplicate queue > 1,000 open. Runbook entries: "Consumer not acking merge" (list registered consumers, `passagectl party merge-status`), "Re-train Splink model" (export labeled pairs, run `party-train`, publish model version). Rollback: previous digest; the Splink model version is pinned in config so a rollback restores scoring behavior.

### 1.7.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| AX DirPartyTable (PartyNumber, Name, NameAlias, InstanceRelationType, LanguageId) | Party | InstanceRelationType DirPerson/DirOrganization → party_type |
| AX DirPerson (FirstName, MiddleName, LastName, Gender, BirthDate via DirPersonName) | Party.person | names from DirPersonName valid-time current row |
| AX DirOrganization (OrgNumber), DirOrganizationName | Party.org | OrgNumber → registration_number |
| AX CustTable (AccountNum, Party, CustGroup, PaymentTermId, DlvTerm, DlvMode, Currency, CreditMax, CreditRating, Blocked, InvoiceAccount, VATNum, DataAreaId) | CustomerRole, TaxRegistration | DataAreaId → legal_entity; Blocked enum No/Invoice/All → blocked; VATNum → VAT registration with country from primary address |
| AX VendTable (AccountNum, Party, VendGroup, PaymentTermId, Blocked, VATNum, DataAreaId) | VendorRole | same pattern |
| AX ContactPerson (ContactPersonId, Party, ContactForParty) | ContactRole | ContactForParty → relationship row |
| AX DirPartyLocation + LogisticsLocationRole | PartyAddress with purposes | reuses wave-6 address load; IsPrimary → primary flag |
| AX LogisticsElectronicAddress (Type, Locator) | ElectronicAddress | Type Phone/Email/URL/Fax; phones normalized E.164 using party country |
| AX CustGroup/VendGroup, PaymentTerm, DlvTerm, DlvMode | groups, terms, modes | PaymentTerm.NumOfDays/PaymMethod (Net/CurrentMonth/…) mapped to net_days/day_of_month |
| SAP KNA1 (KUNNR, NAME1, LAND1, STCEG, STCD1, SPERR, LOEVM, ADRNR) + KNB1 (BUKRS, AKONT, ZTERM, ZUAWA) | Party, CustomerRole, TaxRegistration | one Party per KUNNR unless matched to an LFA1 by STCEG or ADRNR; BUKRS → legal_entity; ZTERM → payment term by code |
| SAP LFA1 (LIFNR, NAME1, STCEG, SPERR, SPERM) + LFB1 (BUKRS, AKONT, ZTERM) | Party, VendorRole | KNA1-LIFNR / LFA1-KUNNR cross references pre-merge the party |
| SAP BUT000 (PARTNER, TYPE, NAME_ORG1, NAME_FIRST, NAME_LAST) + BUT100 (RLTYP FLCU01/FLVN01/BUP001) + DFKKBPTAXNUM | Party, roles, TaxRegistration | roles map FLCU01→customer, FLVN01→vendor, BUP001→contact |
| SAP ADRC / ADR2 / ADR6 / KNVK | PartyAddress, ElectronicAddress, ContactRole | ADR2 TEL_NUMBER + TELNR_CALL merged |
| SAP T052 (ZTERM, ZTAGG, ZTAG1..3, ZPRZ1..2), TINC, KNKK (KLIMK, CTLPC) | PaymentTerm, DeliveryTerm, CreditProfile | T052 day-limit variants produce one Passage term per variant |

Transformation notes: AX `Party` RecId joins are resolved through DirPartyTable.PartyNumber; SAP customer and vendor numbers are kept as `account_number` when they fit 20 characters, else a new sequence number is assigned and the SAP number stored as alias. Validation: every role must resolve to a legal entity and a group; tax ids failing format go to the stewardship queue, not rejection. Not migrated: AX `DirPartyRelationship` types other than contact-for; SAP sales-area (KNVV) and purchasing-org (LFM1) data (waves 26 and 17); SAP dunning data KNB5 (wave 30); AX `CustTrans`/`VendTrans` open items (transactional packs in wave 49).

### 1.7.12 Dependencies, risks, open questions

Depends on: wave 6 (legal entities, addresses, countries, number sequences per entity), wave 4 (Temporal approval template, outbox), wave 5 (import tool, audit, PII attributes from wave-3 metadata).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Consumers added in later waves forget to implement merge ack | Medium | High | Consumer registry with contract test required for any service subscribing to party events |
| VIES rate limits or outages block daily re-validation | High | Low | Backoff and cap per run; unverified is a non-blocking state |
| Splink model drifts for non-Latin names | Medium | Medium | Name normalization with Unicode NFKD; per-tenant threshold override |

Open questions: (1) Do Prospect roles need a number sequence or stay numberless until conversion to Customer? Proposed: numberless; decide by wave 26. (2) Is the employee role created here or only in wave 42 with HcmWorker migration? Proposed: stub here (personnel number only); decide by wave 10. (3) Which survivorship rules are tenant-configurable in v1? Proposed: order list editable, levels fixed; decide by wave 10.
