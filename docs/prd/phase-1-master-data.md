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

## 1.8 Wave 8: Product Master, Variants & Catalogs

### 1.8.1 Scope and business context

AX defines a shared `EcoResProduct` (subtyped `EcoResDistinctProduct` and `EcoResProductMaster`) with variants in `EcoResDistinctProductVariant`, dimension values in `EcoResSize/Color/Style/Configuration`, and releases it per company into `InventTable` with `InventDimGroup`-style product/storage/tracking dimension groups, `InventItemGroup` and `InventModelGroup`. SAP keeps `MARA` (client-level), `MARC` (plant), `MARD` (storage location), `MVKE` (sales), `MBEW` (valuation), material types (`T134`), variant configuration in LO-VC (`CUKB`, `CUOB`, `CABN`/`CAWN` characteristics) and the classification system (`KLAH`/`KSML`). Wave 8 defines the product once at tenant level, explodes variants from dimension groups, releases per legal entity with site/warehouse defaults, and adds attributes, categories, item groups, item model groups, GS1 identifiers and a constraint-based configurator foundation. At the end of the wave the product information management workspace holds the demo manufacturer's 500 items and 50 masters.

Out of scope: on-hand, costing values and inventory postings (waves 16, 20), sales/purchase prices (waves 17, 26), BOMs (wave 9), batch/serial *instances* (wave 16; this wave defines only whether an item is tracked), quality specs (wave 33).

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| Releasing an AX product to 10 companies means 10 passes through "Release products" plus editing InventTable defaults (item group, model group, site, warehouse) in each company; a missed default blocks the first PO with "Item model group not specified" | Release is a per-company copy with no template | Release profiles: one call releases to N entities with defaults derived from item group and site; missing-default check at release (FR-8.4) | Clicks per 10-entity release 60→3; first-transaction failures from missing defaults →0 |
| SAP MM01 walks 15–20 views (basic, purchasing, MRP 1–4, accounting, costing); a material is "created" but unusable in a plant until every view is extended (MM01 extend) | View-based material master with per-plant/storage segments (MARC/MARD) each needing separate maintenance | One product record plus released-product per entity; plant-level data derives from site defaults (FR-8.4) | Time to a usable material in a new plant 25 min→<1 min |
| AX variant creation (EcoResProductVariant "Variant suggestions") is a modal that times out above ~500 combinations; D365 batch job needed | Synchronous form-driven explosion | Explosion is a Temporal job with checkpoints; 1,000 variants in <5 s (FR-8.2) | 10k variants: 40 min batch→<60 s with progress |
| GTIN entry in AX (InventItemGTIN) and SAP (MARM-EAN11) accepts any 13 digits; bad check digits break EDI 856/ASN and retailer onboarding | No check-digit validation at entry | GS1 check-digit and prefix validation, generation from a GS1 company prefix (FR-8.9) | Invalid GTINs stored 0; retailer rejections for GTIN →0 |
| Product attributes in AX (EcoResProductAttribute) require defining attribute types, attributes, categories and category attributes across four forms before a value can be entered; SAP class/characteristic (CL02/CT04) is similar | Classification metadata modeled as a separate hierarchy from products | Attribute sets attached to categories with inline attribute creation and typed validation (FR-8.6) | Setup of a 10-attribute class 30 min→3 min |

### 1.8.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Shared product | EcoResProduct, EcoResDistinctProduct, EcoResProductMaster, Product details form, ProductNumber | MARA (MATNR, MTART, MEINS, MATKL), MM01/MM02/MM03 | product-service `Product` (product_type item/service, subtype product/master) | full |
| Variants and dimensions | EcoResProductDimensionGroup, EcoResSize/Color/Style/Configuration, EcoResDistinctProductVariant, EcoResProductVariantDimensionValue | Generic material + variants (MARA-SATNR, KZKFG), characteristics CABN/CAWN | `DimensionGroup`, `DimensionValue`, `ProductVariant` | full |
| Storage / tracking dimension groups | EcoResStorageDimensionGroup, EcoResTrackingDimensionGroup (batch/serial active, physical/financial inventory flags) | MARA-XCHPF batch management, serial profile MARC-SERNP, valuation level | `StorageDimensionGroup`, `TrackingDimensionGroup` | full |
| Released product per company | InventTable (ItemId, ItemGroupId, ModelGroupId, DataAreaId), InventTableModule, InventItemSetupFilter, default order settings (InventItemInventSetup/PurchSetup/SalesSetup) | MARC (plant), MARD (storage location), MVKE (sales org), MBEW (valuation) | `ReleasedProduct` with `ReleasedProductSiteDefault` | full |
| Item groups / model groups | InventItemGroup, InventModelGroup (InventModel FIFO/LIFO/WAvg/Std, PhysicalNegative, FinancialNegative, PostPhysical, PostFinancial) | Material type T134, material group T023, valuation class T025, price control MBEW-VPRSV | `ItemGroup`, `ItemModelGroup` | full |
| Units and conversions | UnitOfMeasureConversion product-specific, InventTable unit per module | MARA-MEINS, MARM alternative units | `ProductUomConversion`, unit per module (inventory/purchase/sales/BOM) | full |
| Attributes / classification | EcoResAttribute, EcoResAttributeType, EcoResCategory, EcoResCategoryHierarchy, EcoResProductCategory | Classification: KLAH, KSML, CABN, CAWN, AUSP (CL01/CL02/CT04) | `AttributeType`, `Attribute`, `AttributeSet`, `Category`, `CategoryHierarchy`, `ProductAttributeValue` | full |
| Configurator | Product configuration models (PCConfiguration, PCComponent, PCConstraint — D365 constraint-based configurator) | LO-VC: CU41 configuration profile, dependencies CUKB, object dependencies CUOB | `ConfigurationModel` solved by OR-Tools CP-SAT in a Python worker | partial (pricing and BOM/route generation from configuration in waves 26 and 9) |
| GS1 identifiers | InventItemGTIN, InventItemBarcode, BarcodeSetup | MARM-EAN11, EAN category T006M (NUMTP) | `ProductIdentifier` (GTIN-8/12/13/14, SSCC generation) | improved (validation and generation) |
| Images / documents | DocuRef attachments on EcoResProduct, product image | DMS (CV01N) links | `ProductDocument` (object storage key, type) | full |
| Product lifecycle | D365 product lifecycle state (EcoResProductLifecycleState) | MARA-MSTAE cross-plant status, MARC-MMSTA plant status | `lifecycle_state` on Product and ReleasedProduct | full |

### 1.8.3 Functional requirements

- **FR-8.1 Product and product master** — Must. The system shall create tenant-level products (subtype `product`) and product masters (subtype `master`) with product number (sequence `PROD` or manual), name, description, search name, product type (item/service), base unit, and lifecycle state.
  - Product number regex `^[A-Z0-9][A-Z0-9._-]{0,39}$`; immutable after release.
  - Masters carry a `dimension_group_id` with 1–4 product dimensions chosen from size, color, style, configuration (named per tenant, e.g. "length").
- **FR-8.2 Variant explosion** — Must. The system shall generate variants for a master from the Cartesian product of selected dimension values, or from an explicit list, as a Temporal job with 500-row checkpoints.
  - Hard cap 10,000 variants per master; 1,000 variants in < 5 s.
  - Variant number is `{master}-{size}-{color}-{style}-{config}` with empty segments collapsed; override pattern per tenant.
  - Lookup by (master_id, dimension values) resolves with a unique composite index.
- **FR-8.3 Storage and tracking dimension groups** — Must. The system shall define storage dimension groups (site, warehouse, location, inventory status, license plate — each active, mandatory, primary stocking, physical/financial inventory flags) and tracking dimension groups (batch active/mandatory, serial active/mandatory, serial capture at sale/receipt), assigned to a product and immutable once a released product has inventory transactions (callback from wave 16).
- **FR-8.4 Released products with site/warehouse defaults** — Must. The system shall release a product to one or more legal entities, creating `ReleasedProduct` with item group, item model group, units per module, default site and warehouse, and per-site default warehouse and order settings (minimum/multiple/standard order quantities, lead time) for inventory, purchase and sales.
  - Release profiles: a named template of defaults per (legal entity, item group); release of 1,000 products to 3 entities completes in < 60 s.
  - Cascade: a per-site override wins over the entity default; absent both, the request is rejected with the missing field named.
- **FR-8.5 Item groups and item model groups** — Must. The system shall maintain item groups (posting hooks for GL arrive in wave 11) and item model groups with costing method (FIFO, LIFO, weighted_average, weighted_average_date, standard, moving_average), negative physical/financial inventory flags, post physical/financial inventory flags, stocked flag, and quarantine/registration/picking requirements.
- **FR-8.6 Attributes and categories** — Must. The system shall maintain attribute types (text, integer, decimal, boolean, date, enum, currency, with unit and min/max), attributes, attribute sets, and category hierarchies (procurement, sales, retail, reporting) where a category carries attribute sets; a product in a category inherits required attributes and is incomplete until they are filled.
  - Maximum 200 attributes per product; enum attributes have up to 500 values.
- **FR-8.7 Product-specific UoM conversions** — Must. The system shall store per-product conversions (e.g. 1 BOX = 12 EA for product X) that override class conversions; resolution order product → class → error.
- **FR-8.8 Configurator foundation** — Should. The system shall define configuration models per master: components, attribute domains, constraints (expression language with `==, !=, <, >, in, implies, allDifferent, sum`), default values, and a `solve` endpoint that returns a valid configuration or the minimal conflicting constraint set, using CP-SAT with a 2 s time limit.
  - A 50-constraint model solves in < 200 ms; the result is a variant or a configured-product instance with attribute values.
- **FR-8.9 GS1 identifiers** — Must. The system shall store GTIN-8/12/13/14 per (product/variant, unit) with check-digit validation (mod-10 weighting 3-1), allocate GTINs from a tenant GS1 company prefix with a next-number counter, generate SSCC-18 with extension digit, and render barcodes (EAN-13, GS1-128, DataMatrix) as SVG/PNG.
- **FR-8.10 Product lifecycle and blocking** — Must. The system shall enforce lifecycle states `draft → active → phase_out → obsolete` per product and per released product, with module-level blocks (purchase, sales, inventory, production) on released products.
- **FR-8.11 Images and documents** — Should. The system shall attach images (max 10 MB each, 20 per product) and documents to products in object storage with a primary image used in lists.
- **FR-8.12 Product search and catalogs** — Should. The system shall provide typeahead over product number, name, GTIN and attribute values, filtered by legal entity and category, p95 < 100 ms at 1M products, and allow catalogs as named category subtrees with a validity range.

### 1.8.4 Data model

**Product**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| tenant_id | uuid | yes | RLS |
| product_number | text(40) | yes | unique per tenant |
| subtype | enum | yes | product, master, variant |
| master_id | uuid | conditional | FK for variants |
| product_type | enum | yes | item, service |
| name / description / search_name | text(60)/text(1000)/text(60) | name yes | translations in product_translation |
| base_uom_id | uuid | yes | FK |
| dimension_group_id | uuid | masters only | product dimensions |
| storage_dimension_group_id / tracking_dimension_group_id | uuid | yes at release | immutable after first transaction |
| lifecycle_state | enum | yes | draft, active, phase_out, obsolete |
| dimension_values | jsonb | variants | {size, color, style, config}; unique with master_id |
| primary_image_key | text | no | object storage |
| version | int | yes | |

**ReleasedProduct**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| product_id | uuid | yes | FK; unique (product_id, legal_entity_id) |
| legal_entity_id | uuid | yes | |
| item_number | text(40) | yes | defaults to product_number; unique per legal entity |
| item_group_id / item_model_group_id | uuid | yes | FK |
| inventory_uom_id / purchase_uom_id / sales_uom_id / bom_uom_id | uuid | inventory yes | convertible to base |
| default_site_id / default_warehouse_id | uuid | yes for stocked | warehouse must belong to site |
| lifecycle_state | enum | yes | same enum as product |
| blocked_purchase / blocked_sales / blocked_inventory / blocked_production | bool | yes | default false |
| net_weight / gross_weight / volume + units | numeric(18,6) | no | |
| site_defaults | 1:N | | ReleasedProductSiteDefault(site_id, warehouse_id, min_qty, multiple_qty, std_qty, lead_time_days per module) |
| version | int | yes | |

**ItemModelGroup**

| Field | Type | Required | Notes |
|---|---|---|---|
| id / tenant_id / code / name | uuid, uuid, text(10), text(60) | yes | code unique per tenant |
| costing_method | enum | yes | fifo, lifo, weighted_average, weighted_average_date, standard, moving_average |
| stocked | bool | yes | default true |
| physical_negative / financial_negative | bool | yes | default false |
| post_physical / post_financial | bool | yes | default true |
| registration_required / receiving_required / picking_required / deduction_required | bool | yes | |
| quarantine_required | bool | yes | default false |
| include_physical_value | bool | yes | used by costing (wave 20) |

**ConfigurationModel** — id, master_id, version, status (draft/active/retired), components jsonb, attributes jsonb (name, domain), constraints jsonb (expression list, ≤ 2,000), defaults jsonb. **ProductIdentifier** — id, product_id, uom_id, type (GTIN8/12/13/14, SSCC, UPC, custom), value text(18), is_primary, check_digit_valid bool.

Invariants: a variant's dimension_values keys equal its master's dimension group; every released stocked product has item model group and default site/warehouse; GTIN values pass check digit; `obsolete` products have no released product in `active`. Indexing: unique `(master_id, dimension_values)` via a generated hash column; GIN trigram on `search_name`; `product_identifier(value)` unique per tenant; `released_product(legal_entity_id, item_number)` unique; attribute values in `product_attribute_value(product_id, attribute_id)` with a GIN index on a jsonb projection for filtered search.

### 1.8.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | /product/v1/products | Create product or master | `Idempotency-Key` | Maintain products |
| GET | /product/v1/products/{id} | Read with variants summary | n/a | View products |
| POST | /product/v1/products/{id}/variants:explode | Start explosion job; returns job id | key header; job id keyed by request hash | Maintain products |
| GET | /product/v1/products/{id}/variants:lookup?size=M&color=RED | Resolve variant | n/a | View products |
| POST | /product/v1/products/{id}:release | Release to legal entities with profile | idempotent per (product, entity) | Release products |
| PATCH | /product/v1/released-products/{id} | Defaults, blocks, lifecycle | version check | Maintain released products |
| PUT | /product/v1/released-products/{id}/site-defaults/{siteId} | Per-site defaults | full replace | Maintain released products |
| POST | /product/v1/products/{id}/attributes | Set attribute values (validated against type) | full replace of set | Maintain products |
| POST | /product/v1/products/{id}/identifiers | Add GTIN; `allocate=true` draws from prefix | key header | Maintain products |
| POST | /product/v1/configuration-models/{id}:solve | Solve with given selections | pure function | Configure products |
| GET | /product/v1/products:search?q=&legalEntity=&category= | Typeahead | n/a | View products |
| GET | /product/v1/uom/convert?product=&from=&to=&qty= | Product-aware conversion | n/a | any authenticated |

Validation: dimension values must exist in the dimension group; release rejects a `draft` product; identifier type must match length; `solve` rejects selections outside domains with the offending attribute.

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| dev.passage.product.created.v1 | commit | product_id, product_number, subtype, product_type, base_uom | engineering, search |
| dev.passage.product.variant.created.v1 | per explosion checkpoint (batched, ≤ 500 ids) | master_id, variant_ids[], dimension_values[] | engineering, pricing, inventory |
| dev.passage.product.released.v1 | release commit | product_id, legal_entity_id, item_number, item_group_id, item_model_group_id, default_site_id, default_warehouse_id, uoms | inventory, purchasing, sales, costing, planning |
| dev.passage.product.releasedproduct.updated.v1 | PATCH | released_product_id, changed_fields[], blocks, lifecycle_state | all transactional services |
| dev.passage.product.identifier.added.v1 | commit | product_id, type, value, uom_id | WMS (wave 31), EDI (wave 35) |
| dev.passage.product.configuration.solved.v1 | solve returning a variant | model_id, master_id, selections, result_product_id | sales (wave 27), engineering |

### 1.8.6 User experience

| Screen | Primary actions | Key fields/columns | Duty required |
|---|---|---|---|
| Product information management workspace | Tiles: products, masters, released per entity, incomplete attributes, pending releases | counts, incomplete badges | View products |
| All products and masters | New, release, explode variants, lifecycle change | number, name, subtype, type, dimension groups, state | Maintain products |
| Product detail | Tabs: general, dimensions/variants grid, attributes, identifiers, images, releases, translations | | Maintain products |
| Variant explosion dialog | Select values per dimension, preview count, start job, progress bar | dimension values, count, job status | Maintain products |
| Released products (per legal entity) | Edit defaults grid (inline), block, site defaults sub-grid, bulk edit by item group | item number, item group, model group, units, site, warehouse, blocks | Maintain released products |
| Item groups / item model groups | Edit, usage count | code, name, costing method, flags | Maintain product setup |
| Categories and attributes | Hierarchy tree, attribute set editor, assign products | category path, attributes, required | Maintain product categories |
| Configurator model editor | Add components/attributes/constraints, test solve, activate version | constraints, domains, solve result | Configure products |
| GS1 identifiers | Allocate, validate, print barcode | type, value, uom, valid | Maintain products |

Interaction rules: the released-products grid supports bulk edit of selected rows (max 2,000) with one save; attribute inputs validate on blur against type and range; the explosion dialog warns above 2,000 variants and requires confirmation; barcode print uses the browser print dialog with an SVG preview; the legal-entity switcher filters the released-product views only.

### 1.8.7 Business rules and edge cases

- **BR-8.1** Lifecycle machine for Product and ReleasedProduct: `draft → active`, `active → phase_out`, `phase_out → active | obsolete`, `obsolete` terminal; a released product cannot be more "alive" than its product (product `obsolete` forces released `obsolete`).
- **BR-8.2** A master cannot change its dimension group once any variant exists; a dimension value cannot be deleted while any variant uses it.
- **BR-8.3** Explosion is idempotent: existing variants for a combination are skipped and reported; the job result lists created/skipped counts.
- **BR-8.4** Release copies defaults at release time; later changes to the release profile do not propagate (explicit "re-apply profile" action does, with a diff preview).
- **BR-8.5** Changing `costing_method` on an item model group is refused once any released product in it has a financial inventory transaction (wave-16 callback; returns false before wave 16).
- **BR-8.6** UoM conversion quantities round half-even to the target unit's `decimal_precision`; conversions that would produce zero from a non-zero input are rejected (`PRD-UOM-UNDERFLOW`).
- **BR-8.7** GTIN check digit: weights 3,1,3,… from the right of the payload; GTIN-12 stored as GTIN-14 with leading zeros for uniqueness; the same GTIN may not belong to two products in one tenant.
- **BR-8.8** Configurator time limit 2 s; on timeout the response is 409 `PRD-CFG-TIMEOUT` with the partial assignment; infeasible selections return the irreducible infeasible subset (IIS) of at most 10 constraints.
- **BR-8.9** Required attributes from a category are enforced at `active` transition, not at create, so drafts can be saved incomplete.
- **BR-8.10** Translations fall back language → base language of the tenant → product name; never empty.
- **BR-8.11** Variant numbers longer than 40 characters are truncated to 32 plus an 8-character hash, and the full dimension values remain the lookup key.

### 1.8.8 Acceptance criteria

- **AC-8.1** (FR-8.2) Given a master with 3 dimensions × 10 values, when explosion runs, then 1,000 variants exist in < 5 s, lookup by any combination returns the right variant in < 10 ms, and a second run creates 0 and skips 1,000.
- **AC-8.2** (FR-8.8) Given the 50-constraint fixture model and 100 selection sets (60 valid, 40 invalid), when solved, then valid sets return a configuration consistent with every constraint and invalid sets return an IIS, each in < 200 ms.
- **AC-8.3** (FR-8.4) Given release profile P for item group "FG" in USMF with default site S1/warehouse W1 and a site default for S2/W3, when 1,000 products are released and 10,000 random (product, site) default lookups run, then each returns the site override if present, else the entity default (property test).
- **AC-8.4** (FR-8.9) Given the 40 GS1 published test vectors plus 40 mutated ones, when validated, then 40 accepted and 40 rejected; given a prefix 0614141 with next number 123457, when allocated, then GTIN-13 `0614141123457x` has the correct check digit and the counter increments.
- **AC-8.5** (FR-8.3, BR-8.5) Given a released product with a financial transaction (simulated callback true), when its tracking dimension group is changed, then 409 `PRD-DIMGRP-LOCKED`.
- **AC-8.6** (FR-8.10) Given a released product with `blocked_sales = true`, then `releasedproduct.updated.v1` carries `blocks.sales = true` and a later wave's contract stub refuses a sales line.
- **AC-8.7** (FR-8.6) Given category "Fasteners" requiring attributes thread_pitch (decimal, 0.2–6.0 mm) and material (enum), when a product in it is activated without `material`, then 422 lists the missing attribute; with `thread_pitch = 7`, then 422 names the range.
- **AC-8.8** (FR-8.7) Given product X with 1 BOX = 12 EA and class conversion 1 CASE = 24 EA, when converting 2 BOX → CASE, then result is 1 CASE; when converting for product Y with no product conversion, then the class conversion applies.
- **AC-8.9** (FR-8.12) Given 1M products, when searched by a GTIN fragment, then p95 < 100 ms.

### 1.8.9 Test plan and fixtures

Fixtures: `product/demo-catalog.json` (500 items, 50 masters, 12 item groups, 4 model groups, 3 category hierarchies); `product/master-3x10x10.json`; `product/configurator-50.yaml` (bicycle model: frame, wheel, gear, color with 50 constraints; 100 selection sets labeled); `product/gs1-vectors.csv` (40 valid from GS1 check-digit calculator documentation, 40 mutated); `product/uom-product.csv` (30 product conversions); `product/mara-extract.csv` and `product/inventtable-extract.csv` (10,000 rows each).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Explosion performance and idempotency | performance + unit | explode job | < 5 s for 1,000; second run 0 creates |
| Variant lookup | property | lookup index | 100% hits, p95 < 10 ms |
| Configurator fixture | table-driven | CP-SAT worker | 100/100 expected outcomes, each < 200 ms |
| Release cascade | property | defaults resolver | 10k lookups, 0 mismatches |
| GTIN vectors | unit | validator/allocator | 80/80 |
| Lifecycle transitions | state-machine property | product/released | every illegal transition rejected |
| Attribute validation | table-driven | attribute types | 120 cases |
| Search latency | performance | typeahead | p95 < 100 ms at 1M |
| Event contracts | contract | 6 events | registry accepts; consumer stubs decode |

Reconciliation: count(variants) = |size| × |color| × |style| × |config| for fully exploded masters; count(released products) per entity = count in demo catalog; count(GTINs) = count(products with identifiers) in fixtures.

### 1.8.10 Deployment and operations

Helm chart `passage-product` (Go service ×2, CloudNativePG `product-db`, Debezium connector, `product-configurator` Python worker with OR-Tools, 2 replicas, 1 CPU / 1 GiB each). Config: `PRODUCT_MAX_VARIANTS=10000`, `PRODUCT_EXPLODE_CHECKPOINT=500`, `PRODUCT_CFG_TIME_LIMIT_MS=2000`, `PRODUCT_GS1_PREFIX` per tenant in the tenant settings entity, object storage bucket `passage-product-media`. Feature flags: `product.configurator`, `product.gs1_allocation`, `product.catalogs`. Scheduled jobs: nightly incomplete-attribute report; weekly orphan media sweep. SLOs: read p95 < 50 ms; release of 1,000 products < 60 s; solve p95 < 500 ms. Alerts: explosion job failed; configurator worker queue depth > 100; media bucket errors. Runbook: "Explosion job stuck" (Temporal `explode-{master_id}` inspect, resume from checkpoint), "Re-apply release profile". Rollback: previous digest; additive migrations only.

### 1.8.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| AX EcoResProduct (DisplayProductNumber, ProductType, InstanceRelationType), EcoResProductTranslation (Name, Description) | Product, translations | InstanceRelationType EcoResProductMaster → subtype master |
| AX EcoResProductMaster + EcoResProductDimensionGroup, EcoResProductMasterDimensionValue (Size/Color/Style/Configuration tables) | DimensionGroup, DimensionValue | dimension names kept as AX names unless renamed in mapping |
| AX EcoResDistinctProductVariant + EcoResProductVariantDimensionValue | ProductVariant | dimension_values from the four value tables |
| AX EcoResStorageDimensionGroup / EcoResTrackingDimensionGroup + *DimensionGroupFldSetup | storage/tracking groups | field flags per dimension |
| AX InventTable (ItemId, Product, ItemGroupId, ModelGroupId, DataAreaId, NetWeight, UnitVolume), InventTableModule (UnitId per module), InventItemSetupFilter / InventItemInventSetup (default site/warehouse), InventItemPurchSetup, InventItemSalesSetup | ReleasedProduct, ReleasedProductSiteDefault | one row per DataAreaId; InventDim resolved for site/warehouse defaults |
| AX InventItemGroup, InventModelGroup (InventModel, PhysicalNegative, FinancialNegative, PostPhysical, PostFinancial, Stocked) | ItemGroup, ItemModelGroup | InventModel enum → costing_method |
| AX UnitOfMeasureConversion with Product ≠ 0 | ProductUomConversion | |
| AX EcoResAttribute*, EcoResCategory*, EcoResProductCategory, EcoResProductInstanceValue | attributes, categories, values | typed values from EcoResTextValue/IntValue/FloatValue/BooleanValue/DateTimeValue |
| AX InventItemGTIN (GTIN, ItemId, UnitId) | ProductIdentifier | check digit validated; failures to exception queue |
| SAP MARA (MATNR, MTART, MATKL, MEINS, MSTAE, XCHPF, SATNR, KZKFG, BRGEW/NTGEW/VOLUM) | Product, dimension/tracking hints | MTART → item group suggestion; XCHPF → batch active; SATNR → master link |
| SAP MAKT (MAKTX by SPRAS) | translations | |
| SAP MARC (WERKS, DISMM, PLIFZ, BSTMI, BSTRF, MMSTA, SERNP), MARD (LGORT), MVKE (VKORG, VTWEG), MBEW (BWKEY, VPRSV, BKLAS) | ReleasedProduct per company code of the plant; site defaults per plant | PLIFZ → lead_time_days; BSTMI/BSTRF → min/multiple qty; VPRSV S/V → standard/moving_average |
| SAP MARM (MEINH, UMREZ, UMREN), MEAN/MARM-EAN11 | ProductUomConversion, ProductIdentifier | factor = UMREZ/UMREN |
| SAP KLAH/KSML/CABN/CAWN/AUSP (class type 001) | AttributeSet, Attribute, values | characteristic data types CHAR/NUM/DATE mapped; multi-value → enum set |
| SAP CUKB/CUOB/CUKN (object dependencies) | ConfigurationModel | only `precondition`/`selection condition` dependency types with simple syntax are translated; others logged to exception queue |

Validation: every released product resolves an item group and model group; units per module convertible to base; variants resolve to a master. Not migrated: AX `InventSum`/on-hand and `InventTrans` (wave 16 opening balances); SAP `MBEW` prices (wave 20); SAP `MLAN` tax classification (wave 13); AX retail-specific product tables (non-goal); LO-VC procedures and functions (no equivalent; re-authored as constraints).

### 1.8.12 Dependencies, risks, open questions

Depends on: wave 6 (sites, warehouses, UoM classes, legal entities), wave 5 (number sequences, import tool, media attachments), wave 4 (Temporal jobs, outbox). Does not depend on wave 7 (parties) except for vendor-item references, deferred to wave 17.

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Configurator expression language grows into LO-VC complexity | Medium | Medium | Freeze grammar at 8 operators in v1; extensions via sanctioned functions only |
| Customers with 5+ product dimensions (apparel) exceed the 4-dimension model | Medium | Medium | Extra dimensions modeled as required attributes; evaluate a 5th dimension slot by wave 26 |
| Release profiles diverge from AX default order settings semantics | Low | Medium | Fixture comparison against InventItemInventSetup exports |

Open questions: (1) Should `item_number` be allowed to differ from `product_number` per entity (AX allows it)? Proposed: allowed but defaults equal; decide by wave 10. (2) Is moving-average a separate costing method or a parameter of weighted average? Proposed: separate, matching AX; decide by wave 20. (3) Catalogs: product-level or released-product-level membership? Proposed: product-level with entity filter at read; decide by wave 26.

## 1.9 Wave 9: Engineering Data: BOMs, Routings, Resources & Change Management

### 1.9.1 Scope and business context

AX stores bills of material as `BOMTable` (BOM id) with `BOMVersion` (which product/site/date range uses which BOM) and `BOM` lines; routes as `RouteTable`/`RouteVersion`/`RouteOpr` with `RouteOprTable` operations, `WrkCtrTable`/`WrkCtrResourceGroup` resources, `WrkCtrCapability` and `WorkCalendarTable`; D365 adds engineering change management (ECR/ECO, product versions). SAP keeps `MAST` (material-to-BOM link), `STKO` (BOM header), `STPO` (BOM items), `STAS` (alternatives), routings in `PLKO`/`PLPO`/`PLAS` with `MAPL` assignment, work centers in `CRHD`/`CRCA`/`KAKO` (capacity), and change numbers in `AENR` (CC01). Wave 9 delivers BOM headers with versions and lines (effective dates, scrap, phantoms, by-/co-products, process formulas), routing versions with operations, relations and resource requirements by capability, resources with calendars, an ECR/ECO workflow with where-used impact analysis, and an explosion/implosion API. The cost roll-up only records the structure the wave-39 roll-up needs.

Out of scope: production orders (wave 21), capacity scheduling and Gantt (wave 23), potency/catch weight execution (wave 24), costing sheets and standard-cost roll (wave 39), PLM adapters beyond the event contract.

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| AX BOM designer (BOMDesigner form) loads a 7-level, 2,000-component BOM in 30–90 s and the "Calculate" explosion runs as a batch (BOMCalcJob) that users wait minutes for | Recursive X++ explosion row by row over BOMVersion/BOM | Closure-table-backed explosion service; 2,000 components in < 500 ms (FR-9.9) | Explosion 60 s–5 min→<0.5 s |
| SAP CS03 shows one BOM alternative at one date; "what is active on 2026-06-01" means CS03 with a key date, CS80 where-used, and CC03 change number, three transactions and still no diff | BOM validity scattered across STKO/STPO DATUV with AENNR change numbers | Versioned BOMs with as-of resolution, side-by-side version diff and effective-date preview (FR-9.1, FR-9.3) | Transactions to answer "what is active when" 3→1; clicks 15→2 |
| Engineering change in AX 2012 is an unaudited edit of BOM lines; two engineers editing the same BOM overwrite each other; D365 ECM exists but is a separate product-version model many customers never turn on | No change object in the core BOM model | ECR→ECO workflow is the only path to change an active version; optimistic locking on drafts (FR-9.7, BR-9.3) | Unaudited changes to active BOMs 100%→0; lost-update incidents 0 |
| Where-used (AX BOMWhereUsed, SAP CS15) is a flat list; an engineer cannot see which finished goods, orders, and sites a component change impacts before releasing | No impact analysis in the change flow | ECO impact analysis: multi-level where-used across sites with affected product count and open-order hooks (FR-9.8) | Impact assessment effort 2 h→<10 s |
| Work center capacity in SAP (CR02 capacities, KAKO) and AX (WrkCtrTable calendar + efficiency %) is keyed in several places; routing lead time calculated by hand in spreadsheets | Calendar, efficiency and operation times modeled separately | One calendar model with shifts and efficiency; routing lead-time calculation API (FR-9.6, FR-9.10) | Lead-time calculation manual→API, fixture-verified |

### 1.9.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| BOM header / version | BOMTable (BOMId, Name, ItemGroupId, SiteId), BOMVersion (ItemId, BOMId, FromDate, ToDate, FromQty, ToQty, Active, Approved) | STKO (STLNR, STLAL, BMENG, DATUV), MAST (MATNR, WERKS, STLNR, STLAL), STAS, CS01/CS02/CS03 | engineering-service `BomHeader`, `BomVersion` | full |
| BOM lines | BOM (LineNum, ItemId, BOMQty, BOMQtySerie, UnitId, FromDate, ToDate, ScrapConst, ScrapVar, BOMType Item/Phantom/Pegged/Vendor, Position, RoundUp, Formula) | STPO (POSNR, IDNRK, MENGE, MEINS, AUSCH, DUMPS phantom, ALPOS, POSTP item category, DATUV) | `BomLine` | full |
| Phantoms, by-/co-products | BOMType Phantom; co-products/by-products in PmfCoByProduct (process industries) | DUMPS, co-products MARC-KZKUP / STPO negative qty by-products | `BomLine.line_type` ∈ {item, phantom, pegged_supply, vendor, co_product, by_product} | full |
| Formulas (process) | PmfFormula, formula versions, scalable, yield percent | PP-PI master recipes (C201), STKO BMENG base qty | `BomHeader.kind = formula`, `yield_percent`, `scalable` flag | partial (batch execution in wave 24) |
| Routing header / version | RouteTable (RouteId), RouteVersion (ItemId, RouteId, FromDate, ToDate, Active, Approved) | PLKO (PLNNR, PLNAL, DATUV), MAPL (MATNR, WERKS, PLNNR), CA01/CA02/CA03 | `RouteHeader`, `RouteVersion` | full |
| Operations and times | RouteOpr (OprNum, OprId, SetUpTime, ProcessTime, ProcessPerQty, TransitTime, QueueTimeBefore/After, OprPriority, NextOprNum, LinkType), RouteOprTable | PLPO (VORNR, ARBPL, VGW01..06 standard values, LAR01..06 activity types, PLIFZ), PLAS | `Operation` with times, `OperationRelation` (sequential/parallel via `next_operation_no` + priority) | full |
| Resources / work centers | WrkCtrTable (WrkCtrId, WrkCtrType Vendor/Machine/HumanResources/Tool/Location, Efficiency, CalendarId), WrkCtrResourceGroup, WrkCtrCapability, WrkCtrCapabilityResource | CRHD (ARBPL, VERWE category, KAPID), CRCA capacities, KAKO (capacity header, utilization), CRHD-STEUS control key | `Resource`, `ResourceGroup`, `Capability`, `ResourceCapability` | full |
| Calendars | WorkCalendarTable, WorkCalendarDate, WorkCalendarDateLine (shifts), WorkCalendarTemplate | Factory calendar TFACD/TFACS (SCAL), shift definitions | `WorkCalendar`, `CalendarDay`, `Shift` | full |
| Engineering change | D365 ECM: EngChgRequest, EngChgOrder, product versions, impact analysis, release to legal entities | ECM: AENR change master (CC01), ECR/ECO (CC31/CC32), effectivity | `EngineeringChangeRequest`, `EngineeringChangeOrder`, Temporal workflow | full |
| Where-used / explosion | BOMWhereUsed, BOM calculation (BOMCalcTable), BOM designer | CS11/CS12/CS13 (explosion), CS15 (where-used) | `/engineering/v1/bom:explode`, `:implode`, `:where-used` | improved (latency, as-of) |
| Cost roll-up | BOMCalcTable, costing versions | CK11N/CK40N | structure hooks only; roll-up in wave 39 | partial by design |

### 1.9.3 Functional requirements

- **FR-9.1 BOM headers and versions** — Must. The system shall create BOM headers (bom_id from sequence `BOM`, name, kind item/formula, site optional) and versions binding a header to a product (released product optional for site-specific), with `effective_from`/`effective_to`, `from_qty`/`to_qty` ranges, `approved` and `active` flags.
  - At most one active version per (product, site, overlapping date range, overlapping quantity range); activation checks the overlap.
  - Active versions are immutable; changes go through an ECO (FR-9.7) or by creating a new draft version.
- **FR-9.2 BOM lines** — Must. The system shall maintain lines with line number, component product/variant, quantity per `series` (per N parents), unit, line type (item, phantom, pegged_supply, vendor, co_product, by_product), position/reference designator, constant and variable scrap percent, `round_up` policy (none, quantity, measurement, consumption), effective dates per line, resource consumption flag and operation number.
  - Maximum 5,000 lines per version; quantity numeric(18,6); scrap 0–100% with 2 decimals.
  - Co-/by-product lines carry a cost allocation percent (sum ≤ 100 across co-products) used in wave 24.
- **FR-9.3 As-of resolution and diff** — Must. The system shall resolve the applicable BOM and route version for (product, site, date, quantity) and produce a line-level diff between any two versions.
- **FR-9.4 Routing headers, versions and operations** — Must. The system shall create routes (route_id from sequence `ROUTE`), versions bound to a product/site with effective dates, and operations with operation number, operation id (reusable definition), setup time, run time per `process_qty`, queue before/after, transit time, overlap quantity, `next_operation_no` and `priority` (primary, secondary1..3) for parallel branches, scrap percent, and link type (hard/soft).
  - Maximum 500 operations per version; times in hours with 4 decimals.
- **FR-9.5 Resource requirements by capability** — Must. The system shall express each operation's requirement as resource, resource group, capability (with minimum level), resource type, or skill, with quantity of resources (default 1); scheduling (wave 23) resolves requirements, this wave validates that at least one resource satisfies each requirement at activation.
- **FR-9.6 Resources, groups, capabilities, calendars** — Must. The system shall maintain resources (type machine, tool, labor, vendor, location), resource groups per site, capabilities with levels and effective dates, and work calendars with templates (7 day patterns), shifts (start, end, efficiency percent), closed days, and a generator for a date range.
  - Calendar generation for 3 years × 5 shifts completes in < 2 s; calendars are inherited group → resource unless overridden.
- **FR-9.7 ECR/ECO workflow** — Must. The system shall create engineering change requests (title, reason, priority, affected products, requester) that move through a Temporal workflow `draft → submitted → under_review → approved | rejected`, and change orders that bundle draft BOM/route versions, go through approval, and on release atomically expire old versions (`effective_to = release_date − 1`) and activate new ones with `effective_from = release_date`.
  - Release date may be future; the workflow holds a timer until then.
  - An ECO may affect up to 500 versions; release across them is one transaction per service and compensated on failure.
- **FR-9.8 Where-used and impact analysis** — Must. The system shall return single- and multi-level where-used for a component as of a date, across sites, with distinct top-level products; an ECO impact report lists affected products, sites, active versions, and counts from consumer hooks (open production and planned orders — stubs return 0 until waves 21–22).
- **FR-9.9 Explosion and implosion API** — Must. The system shall explode a product to N levels (default all, max 25) as of a date and quantity, resolving phantoms inline, applying series and scrap, returning a tree and a flattened summed-component list; and implode (where-used tree) in the reverse direction.
  - 7-level, 2,000-component explosion < 500 ms; results are cacheable by (version set hash, date, qty).
- **FR-9.10 Routing lead-time calculation** — Should. The system shall compute operation and total lead time for a quantity: setup + run × qty / process_qty, queue and transit times added, parallel branches taking the max, and efficiency applied from the calendar; results in hours and in calendar days against the site calendar.
- **FR-9.11 Cycle and integrity checks** — Must. The system shall reject a line whose component's own active BOM (recursively) contains the parent (`ENG-BOM-CYCLE`), using the product-level closure table, and reject activation when any component is `obsolete`.
- **FR-9.12 Copy and template** — Should. The system shall copy BOM and route versions to another product or site, with optional line filtering, and keep a `copied_from_version_id` lineage.
- **FR-9.13 PLM event contract** — Could. The system shall publish ECO lifecycle events in a shape that a PLM adapter can consume and accept inbound `eco.proposed` payloads through the import tool.

### 1.9.4 Data model

**BomVersion**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| tenant_id | uuid | yes | RLS |
| bom_header_id | uuid | yes | FK bom_header(bom_id, name, kind, site_id) |
| product_id | uuid | yes | FK product (variant allowed) |
| site_id | uuid | no | null = all sites |
| effective_from / effective_to | date | from yes | inclusive |
| from_qty / to_qty | numeric(18,6) | no | quantity-dependent versions |
| status | enum | yes | draft, active, expired, superseded |
| approved_by / approved_at | uuid, timestamptz | no | |
| base_qty / yield_percent | numeric(18,6), numeric(5,2) | yes / formulas | base_qty default 1 |
| eco_id | uuid | no | ECO that activated it |
| copied_from_version_id | uuid | no | lineage |
| version | int | yes | optimistic lock |

**BomLine**

| Field | Type | Required | Notes |
|---|---|---|---|
| id / bom_version_id | uuid | yes | FK |
| line_no | int | yes | unique per version, step 10 |
| component_product_id | uuid | yes | FK product |
| quantity / series / uom_id | numeric(18,6), numeric(18,6), uuid | yes | quantity per `series` parents |
| line_type | enum | yes | item, phantom, pegged_supply, vendor, co_product, by_product |
| scrap_const / scrap_var_pct | numeric(18,6), numeric(5,2) | yes | default 0 |
| round_up | enum | yes | none, quantity, measurement, consumption |
| position / reference_designators | text(20), text[] | no | |
| operation_no | int | no | consumption at operation |
| effective_from / effective_to | date | no | line-level |
| cost_allocation_pct | numeric(5,2) | co/by only | |
| warehouse_id / resource_consumption | uuid, bool | no | |

**Operation**

| Field | Type | Required | Notes |
|---|---|---|---|
| id / route_version_id | uuid | yes | FK route_version (same shape as BomVersion minus qty range) |
| operation_no | int | yes | unique per version |
| operation_id | text(20) | yes | reusable operation definition |
| next_operation_no / priority / link_type | int, enum, enum | no | priority primary/secondary1..3; link hard/soft |
| setup_time_h / run_time_h / process_qty | numeric(10,4), numeric(10,4), numeric(18,6) | yes | run time per process_qty |
| queue_before_h / queue_after_h / transit_h | numeric(10,4) | no | |
| overlap_qty / scrap_pct | numeric(18,6), numeric(5,2) | no | |
| requirements | 1:N | yes | OperationRequirement(kind resource/group/capability/type/skill, ref_id, level, quantity) |
| cost_category_setup_id / run_id | uuid | no | bound in wave 39 |

**Resource / WorkCalendar** — resource: id, code, name, type, site_id, group_id, calendar_id, efficiency_pct (default 100), capacity_per_day_h, exclusive bool, vendor_party_id (type vendor); calendar: id, code, template jsonb (7 days × shifts), generated_from/to; calendar_day: calendar_id, date, open bool, shifts jsonb [{start, end, efficiency_pct}], total_h.

**EngineeringChangeOrder** — id, eco_number (sequence `ECO`), title, reason, priority, status (draft, submitted, approved, scheduled, released, rejected, cancelled), release_date, affected_versions uuid[] (draft versions to activate, with the versions they supersede), impact jsonb snapshot, workflow_id.

Invariants: no two active BOM versions overlap for the same (product, site, date, qty); every active version's lines reference non-obsolete components; product closure table has no cycles; a released ECO's versions are all `active` and their predecessors `expired`; operation numbers referenced by `next_operation_no` exist in the same version. Indexing: `bom_version(product_id, site_id, effective_from, status)`; `bom_line(component_product_id)` for where-used; product closure (parent_product_id, child_product_id, depth) maintained on activation; `calendar_day(calendar_id, date)` PK.

### 1.9.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | /engineering/v1/bom-headers | Create header | `Idempotency-Key` | Maintain BOMs |
| POST | /engineering/v1/bom-versions | Create draft version (optionally copy from) | key header | Maintain BOMs |
| PUT | /engineering/v1/bom-versions/{id}/lines | Replace line set of a draft | full replace | Maintain BOMs |
| POST | /engineering/v1/bom-versions/{id}:activate | Activate outside ECO (requires duty "Activate BOM without ECO") | key header | Activate BOMs |
| GET | /engineering/v1/bom:resolve?product=&site=&date=&qty= | As-of version | n/a | View engineering data |
| POST | /engineering/v1/bom:explode | Body: product, site, date, qty, levels | pure | View engineering data |
| GET | /engineering/v1/bom:where-used?component=&date=&levels= | Implosion | n/a | View engineering data |
| GET | /engineering/v1/bom-versions/{a}:diff?against={b} | Line diff | n/a | View engineering data |
| POST | /engineering/v1/route-versions, PUT …/{id}/operations | Routes | as BOM | Maintain routes |
| POST | /engineering/v1/route-versions/{id}:lead-time?qty= | Lead-time calc | pure | View engineering data |
| POST | /engineering/v1/resources, /resource-groups, /capabilities, /calendars/{id}:generate | Resources and calendars | key header; generate idempotent per range | Maintain resources |
| POST | /engineering/v1/ecrs, /ecos, /ecos/{id}:submit, :approve, :release | Change management | key header | Maintain / Approve / Release engineering changes |
| GET | /engineering/v1/ecos/{id}/impact | Impact analysis | n/a | View engineering data |

Validation: line set replacement runs cycle, unit-convertibility and component-state checks; activation validates overlap and resource satisfiability; `:release` requires `approved` status and all affected drafts to pass activation checks in a dry run before the transaction.

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| dev.passage.engineering.bom.version.activated.v1 | activation (direct or ECO) | bom_version_id, product_id, site_id, effective_from, effective_to, line_count, supersedes_version_id | planning, production, costing |
| dev.passage.engineering.bom.version.expired.v1 | expiry by ECO | bom_version_id, effective_to, eco_id | planning, production |
| dev.passage.engineering.route.version.activated.v1 | activation | route_version_id, product_id, site_id, operations[{no, resource requirements}] | planning, scheduling, production |
| dev.passage.engineering.resource.changed.v1 | resource/calendar commit | resource_id, site_id, calendar_id, efficiency_pct, changed_fields[] | scheduling, shop floor |
| dev.passage.engineering.eco.submitted.v1 / approved.v1 / released.v1 | workflow transitions | eco_id, eco_number, affected_versions[], release_date, approver_id | notification, audit, PLM adapter |
| dev.passage.engineering.calendar.generated.v1 | generate | calendar_id, from, to, open_days | scheduling |

### 1.9.6 User experience

| Screen | Primary actions | Key fields/columns | Duty required |
|---|---|---|---|
| Engineering workspace | Tiles: BOMs, routes, resources, open ECRs/ECOs, versions expiring in 30 days | counts | View engineering data |
| BOM designer | Tree with inline line grid, add/remove, drag to reorder, as-of date picker, explode/implode toggle, version diff | line no, component, qty, series, unit, type, scrap, position, operation | Maintain BOMs |
| BOM versions | List per product, activate (with ECO or direct), copy | version, site, dates, qty range, status, ECO | Maintain BOMs |
| Route editor | Operation grid, network view (parallel branches), lead-time panel | op no, op id, next, priority, setup, run, process qty, requirements | Maintain routes |
| Resources and groups | New, capabilities tab, calendar assignment, utilization preview | code, type, site, group, calendar, efficiency | Maintain resources |
| Work calendars | Template editor, generate range, closed days | day, shifts, hours | Maintain resources |
| ECR / ECO | Create, add affected products/versions, impact report, submit/approve/release, schedule release date | number, title, status, affected, release date | Maintain / Approve / Release engineering changes |
| Where-used | Component picker, as-of date, levels, export CSV | parent, qty, level, site, version status | View engineering data |

Interaction rules: BOM designer supports keyboard line entry (`Tab` through component/qty/unit, `Enter` adds next line at +10); component lookup searches wave-8 products filtered by lifecycle `active`; validation of cycles runs on line commit, overlap on activation; designer shows an "as of" ribbon with a date slider; network view in the route editor validates `next_operation_no` references live; personalization persists tree expansion depth.

### 1.9.7 Business rules and edge cases

- **BR-9.1** BOM/route version machine: `draft → active` (direct activation or ECO release), `active → expired` (effective_to passes or ECO), `active → superseded` (newer version activated for the same range by ECO), `draft → cancelled`; `active` is immutable.
- **BR-9.2** ECO machine: `draft → submitted → approved → scheduled → released`; `submitted | approved → rejected`; `draft | submitted | approved | scheduled → cancelled`; `released` terminal. `scheduled` waits on a Temporal timer for `release_date`.
- **BR-9.3** Draft versions use `version` optimistic locking; two engineers editing the same draft get 409 on the second save and a line-level merge view.
- **BR-9.4** Scrap: consumed quantity = `ceil_policy((qty_parent / series) × quantity × (1 + scrap_var_pct/100) + scrap_const)`; `round_up = quantity` rounds to the next whole unit, `measurement` to the unit's `decimal_precision`, `consumption` rounds the total across the order, not per line.
- **BR-9.5** Phantom lines are replaced by their own active BOM at explosion time, as of the same date; a phantom with no active BOM is reported as an explosion error, not silently skipped.
- **BR-9.6** Explosion depth cap is 25; exceeding it returns 422 `ENG-BOM-DEPTH`; cycle detection on insert makes this reachable only for legitimately deep structures.
- **BR-9.7** Parallel operations: an operation with `priority = secondary1` runs alongside the primary with the same `next_operation_no`; lead time takes the max of the branches; a secondary without a primary at the same join is rejected.
- **BR-9.8** Lead time with efficiency: run hours ÷ (resource efficiency × calendar shift efficiency); setup is not scaled by quantity; queue and transit are calendar-elapsed, not capacity-consuming.
- **BR-9.9** ECO release is all-or-nothing within engineering-service; failure after partial activation triggers Temporal compensation that reverts activated versions to `draft` and restores expired ones to `active`, then marks the ECO `approved` again with an error note.
- **BR-9.10** A release date earlier than today requires the duty "Backdate engineering changes" and is audited; a release date more than 365 days ahead is rejected.
- **BR-9.11** Deleting a resource is refused while any active route version requires it directly; capability-based requirements are unaffected.
- **BR-9.12** Co-product allocation percents on a version must sum to ≤ 100; by-products carry no allocation and may have negative cost in wave 24.

### 1.9.8 Acceptance criteria

- **AC-9.1** (FR-9.9) Given the 7-level, 2,000-component golden product, when exploded for qty 10 as of 2026-03-01, then the flattened component list equals `engineering/golden-explosion.csv` and p95 over 100 runs < 500 ms.
- **AC-9.2** (FR-9.3) Given BOM versions V1 (2025-01-01..2026-05-31) and V2 (2026-06-01..), when resolved as of 2026-01-01, then V1; as of 2026-06-01, then V2; as of 2026-05-31 with qty 1,000 where V3 covers qty ≥ 500, then V3.
- **AC-9.3** (FR-9.7, BR-9.2) Given an approved ECO replacing V1 with draft V2 at release date 2026-07-01, when the Temporal test clock passes 2026-07-01, then V1 is `expired` with effective_to 2026-06-30, V2 is `active`, both events are published, and the ECO is `released`.
- **AC-9.4** (FR-9.10) Given the 12 routing fixtures (sequential, two-branch parallel, overlap), when lead time is computed for qty 100, then results match fixture hours to 4 decimals.
- **AC-9.5** (FR-9.11) Given product A whose active BOM contains B, when a line adding A to B's draft is saved, then 422 `ENG-BOM-CYCLE` naming the path A→B→A.
- **AC-9.6** (FR-9.8) Given component C used in 30 products across 2 sites, when where-used is run as of today with levels = all, then 30 distinct top-level products and the ECO impact report shows the same set.
- **AC-9.7** (FR-9.5) Given an operation requiring capability "welding ≥ level 3" and no resource with it, when the route version is activated, then 422 `ENG-ROUTE-UNSATISFIED` names the operation.
- **AC-9.8** (BR-9.9) Given an ECO with 3 versions where the third fails activation, when released, then none are active, the ECO returns to `approved` with the error, and the audit shows the compensation.
- **AC-9.9** (FR-9.6) Given a 3-year calendar with 2 shifts and 10 closed days, when generated, then open-day count equals the fixture and generation < 2 s.
- **AC-9.10** (BR-9.4) Given a line qty 3 per series 2 with scrap_var 5% and scrap_const 1, round_up quantity, when the parent qty is 7, then consumption = ceil(7/2×3×1.05 + 1) = 13.

### 1.9.9 Test plan and fixtures

Fixtures: `engineering/golden-product.json` (7 levels, 2,000 components, 40 phantoms, 3 sites) with `golden-explosion.csv`; `engineering/version-dates.json` (12 overlapping-version scenarios); `engineering/routes-12.yaml` with lead-time oracle; `engineering/calendars.csv`; `engineering/eco-scenarios.yaml` (8 workflows including compensation); `engineering/bom-extract-ax.csv` (BOMVersion/BOM 10k rows) and `engineering/stko-stpo-extract.csv`.

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Golden explosion | golden + performance | explode | byte-equal, p95 < 500 ms |
| As-of resolution | table-driven | resolve | 12/12 |
| Cycle detection | property (random DAGs + injected cycles) | line insert | 0 cycles accepted, 0 false rejections |
| ECO workflow | Temporal test server | ECO | 8/8 scenarios incl. compensation |
| Lead time | unit | calc | 12/12 to 4 decimals |
| Scrap rounding | table-driven | consumption | 40 cases |
| Calendar generation | unit + performance | generate | fixture equal, < 2 s |
| Where-used scale | performance | implode | 100k lines, < 300 ms |
| Event contracts | contract | 7 events | registry accepts |

Reconciliation: Σ flattened explosion quantities = Σ over tree leaves; active version count per (product, site, date) ≤ 1 (nightly check); ECO affected versions = activated + expired events emitted.

### 1.9.10 Deployment and operations

Helm chart `passage-engineering` (Go service ×2, CloudNativePG `engineering-db`, Debezium connector, Temporal namespace `engineering`). Config: `ENG_BOM_MAX_LINES=5000`, `ENG_BOM_MAX_DEPTH=25`, `ENG_ROUTE_MAX_OPS=500`, `ENG_ECO_MAX_VERSIONS=500`, `ENG_EXPLODE_CACHE_TTL_S=300`. Feature flags: `engineering.direct_activation` (default off in production tenants), `engineering.formulas`, `engineering.plm_inbound`. Scheduled jobs: nightly overlap check; nightly expiry job setting `expired` on versions whose `effective_to` passed; 30-day expiry warning digest. SLOs: explode p95 < 500 ms; resolve p95 < 30 ms; ECO release p95 < 60 s. Alerts: overlap check finds > 0 rows; ECO stuck in `scheduled` past release_date + 10 min; explosion errors > 1% of calls. Runbook: "ECO compensation ran" (review error note, re-validate drafts, resubmit), "Rebuild product closure" (`passagectl eng rebuild-closure`). Rollback: previous digest; migrations additive.

### 1.9.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| AX BOMTable (BOMId, Name, SiteId, ItemGroupId) | BomHeader | |
| AX BOMVersion (ItemId, BOMId, FromDate, ToDate, FromQty, ToQty, Active, Approved, ApprovedBy, InventDimId→site) | BomVersion | Active=Yes → active; Approved without Active → draft with approved_by; date 1900-01-01 → null |
| AX BOM (BOMId, LineNum, ItemId, BOMQty, BOMQtySerie, UnitId, BOMType, ScrapConst, ScrapVar, Position, RoundUp, FromDate, ToDate, OprNum, InventDimId) | BomLine | BOMType enum Item/Phantom/Pegged/Vendor; co/by-products from PmfCoByProduct |
| AX RouteTable, RouteVersion, RouteOpr (OprNum, OprId, SetUpTime, ProcessTime, ProcessPerQty, TransitTime, QueueTimeBefore/After, OprPriority, NextOprNum, LinkType, Overlap), RouteOprTable (WrkCtrIdCost, requirements) | RouteHeader, RouteVersion, Operation, OperationRequirement | OprPriority Primary/Secondary1..3; requirements from RouteOprTable WrkCtrId / WrkCtrGroupId / WrkCtrCapability |
| AX WrkCtrTable (WrkCtrId, WrkCtrType, Efficiency, CalendarId, WrkCtrGroupId, VendAccount), WrkCtrResourceGroup, WrkCtrCapability, WrkCtrCapabilityResource (Level, FromDate, ToDate) | Resource, ResourceGroup, Capability, ResourceCapability | VendAccount → vendor_party_id via wave-7 alias |
| AX WorkCalendarTable, WorkCalendarDate, WorkCalendarDateLine (FromTime, ToTime, Efficiency), WorkCalendarTemplate | WorkCalendar, CalendarDay, Shift | times in seconds → hh:mm |
| D365 EngChgRequest / EngChgOrder (if present) | ECR / ECO | historical ECOs loaded as `released` with their versions |
| SAP MAST (MATNR, WERKS, STLNR, STLAL, STLAN usage) + STKO (STLAL, BMENG, BMEIN, DATUV, STLST status, AENNR) + STAS | BomHeader, BomVersion | one header per STLNR; one version per STLAL × DATUV; STLAN 1 (production) only; base_qty from BMENG |
| SAP STPO (POSNR, IDNRK, MENGE, MEINS, AUSCH scrap, DUMPS phantom, ALPOS, POSTP L/N/R, DATUV, DATUB, SANKA costing relevance) | BomLine | POSTP N (non-stock) → vendor; negative MENGE → by_product |
| SAP PLKO (PLNTY N, PLNNR, PLNAL, DATUV, VERWE, STATU) + MAPL + PLAS (sequence) + PLPO (VORNR, ARBPL, STEUS, VGW01..06, VGE0x units, PLIFZ, ZMERH, UEMUS overlap) | RouteHeader, RouteVersion, Operation | VGW01 setup, VGW02 machine, VGW03 labor → setup_time_h / run_time_h (labor kept as attribute); PLAS alternative sequences → parallel branches |
| SAP CRHD (OBJID, ARBPL, WERKS, VERWE, KAPID), CRCA, KAKO (KAPAZ, NGRAD utilization), CRTX | Resource, ResourceGroup, capacity_per_day_h | NGRAD → efficiency_pct |
| SAP TFACD/TFACS factory calendar, shift definitions | WorkCalendar | holiday calendars expanded for the migration range |
| SAP AENR (AENNR, DATUV, AETXT, AESTA) | ECO (historical, released) | effectivity dates recreated on versions |

Validation: every line's component exists as a product in wave 8 and is not obsolete; every version's product is released at the version's site; no overlapping active versions after load (overlaps go to the exception queue with both versions listed); route requirements satisfiable. Not migrated: AX `BOMCalcTable` results (recomputed in wave 39), SAP `PLPO` activity-type costing fields `LAR0x` (wave 39), SAP document BOMs (STLTY D) and sales-order BOMs (wave 27 configured products), classification-based variant BOMs that depend on LO-VC procedures.

### 1.9.12 Dependencies, risks, open questions

Depends on: wave 8 (products, variants, UoM conversions, lifecycle), wave 6 (sites, calendars reference data), wave 7 (vendor parties for vendor-type resources), wave 4 (Temporal, outbox), wave 5 (number sequences `BOM`/`ROUTE`/`ECO`, audit).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Customers with un-versioned AX BOMs rely on in-place edits; forcing ECOs slows them | High | Medium | `engineering.direct_activation` flag per tenant with audit; ECO required only for products flagged `controlled` |
| SAP alternative sequences and parallel sequences map imperfectly to priority branches | Medium | Medium | Fixture comparison; unmapped PLAS structures reported, not silently flattened |
| Explosion cache serves stale results after ECO release | Low | High | Cache key includes the set of active version ids; release invalidates by product |

Open questions: (1) Should BOM lines reference product variants directly or master plus dimension expressions (AX "dimension-based" lines)? Proposed: direct variant with an optional "inherit parent dimension" flag; decide by wave 21. (2) Formula yield at header or per line? Proposed: header yield plus line scrap; decide by wave 24. (3) Do ECOs cover released-product changes (item model group) too? Proposed: no in v1; decide by wave 10.

## 1.10 Wave 10: Data Migration Framework & Legacy Connectors

### 1.10.1 Scope and business context

AX ships the Data Import Export Framework (DIXF/DMF: `DMFDefinitionGroup`, `DMFEntity`, staging tables such as `DMFCustomerEntity`, `DMFVendorEntity`, `DMFProductEntity`, `DMFBOMEntity`) and D365 the data management framework with data entities (`CustCustomerV3Entity`, `VendVendorV2Entity`, `EcoResReleasedProductV2Entity`, `BOMEntity`), data projects and packages; SAP ships LSMW (recording, batch input, BAPI, IDoc), the S/4HANA Migration Cockpit (LTMC/LTMOM with migration objects such as `Customer`, `Supplier`, `Material`, `Bill of material`, `Routing`, `Work center`) and BAPIs (`BAPI_MATERIAL_SAVEDATA`, `BAPI_CUSTOMER_CREATEFROMDATA1`). Wave 10 builds the pipeline every later wave reuses: extract from AX SQL (ODBC) or SAP (RFC/OData or flat files), stage into a Postgres staging schema, map with a declarative DSL, validate, load through the public APIs of waves 6–9, and reconcile with row counts, hash totals and sampled field compares. Dry-run and idempotent re-run, a run ledger with lineage, and an exception queue with re-run close the loop. Phase 1 exits when the demo manufacturer's master data is fully migrated from a synthetic AX extract.

Out of scope: transactional data packs (open items, on-hand, open orders — wave 49), parallel-run diff harness (wave 49), live coexistence adapters (wave 49), X++/ABAP code conversion (non-goal).

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| DIXF staging-to-target fails at row 48,213 of 100,000 with "Function DMFEntityWriter... has been incorrectly called"; the partial load leaves half-created customers and the fix is manual SQL cleanup before re-run | Non-idempotent target writes; errors swallowed into DMFExecution log | Every load is idempotent by natural key and run id; failed rows go to the exception queue, successful rows stay; re-run touches only changed rows (FR-10.6, FR-10.7) | Re-run cost after failure: hours of cleanup→0; duplicate rows after re-run 0 |
| LSMW requires recording a transaction (XD01) screen by screen; a screen layout change in a support pack breaks the recording silently | Batch-input emulation of GUI screens | Loads go through versioned public APIs with contract tests (FR-10.5) | Recording breakage per upgrade: recurring→0 |
| Reconciliation in both tools is "count rows in source, count in target, eyeball"; field-level drift (truncated names, dropped leading zeros in account numbers) is discovered weeks later | No hash or sampled compare | Row counts + per-column hash totals + stratified sampled field compare with tolerance per type (FR-10.8) | Field-level drift found at load: 0%→100% of sampled columns; sign-off evidence auto-generated |
| Mapping lives in Excel and in the heads of consultants; the enum mapping for CustTable.Blocked or MARA-MTART is re-discovered on every project | Mappings not declarative or versioned | Mapping DSL in git with unit tests, enum tables and rejection reasons (FR-10.3) | Mapping reuse across projects: 0→100% of standard objects; mapping defects found by tests before load |
| A 100k-item load through DIXF takes 6–10 hours; SAP LTMC similar | Row-at-a-time batch processing | Partitioned parallel loaders (Go) against bulk endpoints with checkpoints (FR-10.6) | 170k master records in < 30 min vs 6–10 h |

### 1.10.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Migration project / definition | DMFDefinitionGroup, D365 data project, data package (manifest + package header) | LSMW project/subproject/object; LTMC migration project | migration-service `MigrationProject` with `MappingSpec` versions | full |
| Source extraction | DIXF ODBC/file source, AX SQL direct, D365 entity export | RFC (RFC_READ_TABLE, BAPIs), OData, SE16 extracts, IDoc outbound | `Connector` (ax-odbc, d365-odata, sap-rfc, sap-odata, csv/parquet) | full |
| Staging | DMF*Entity staging tables | LSMW read/convert data files; LTMC staging tables (DB staging) | Postgres schema `staging` with typed tables per source object + raw jsonb | full |
| Mapping / transformation | DMF mapping (source → staging → target), X++ transforms | LSMW field mapping and conversion rules (ABAP), LTMOM | Mapping DSL (YAML) with functions, lookups, enum tables | improved (tested, versioned) |
| Validation | Entity validation on target write | LTMC simulate; BAPI return messages | Validation stage with declarative expectations plus target-API dry run | improved |
| Load | DMFEntityWriter, data entity insert | Batch input / BAPI / IDoc posting | Parallel loaders against wave 6–9 bulk endpoints | full |
| Reconciliation | DMF execution log counts | LTMC monitoring counts | Row counts, hash totals, sampled compare, exception queue | improved |
| Run history / lineage | DMFExecution, job history | LSMW logs, LTMC run history | `MigrationRun` ledger with per-row lineage | improved |
| Standard objects | DMF customer, vendor, product, BOM, route entities | Migration Cockpit objects Customer, Supplier, Material, BOM, Routing, Work center | Connector packs for DirParty/CustTable/VendTable/InventTable/EcoResProduct/BOM/Route and KNA1/LFA1/BUT000/MARA/MAST-STKO-STPO/PLKO-PLPO/CRHD | full for Phase 1 scope |
| Scheduling / monitoring | Batch jobs, Data management workspace | SM37, LTMC monitor | Temporal workflows, migration workspace | full |

### 1.10.3 Functional requirements

- **FR-10.1 Migration projects and runs** — Must. The system shall define projects (name, source system type ax2012/d365fo/sapecc/s4, target tenant, object list in dependency order, mapping spec versions) and execute runs with modes `dry_run`, `load`, and `reconcile_only`; each run records start/end, user, counts per stage, and the mapping and connector versions used.
  - Object dependency order for Phase 1 is fixed: reference data → org → parties → products → released products → resources/calendars → BOMs → routes → ECOs.
- **FR-10.2 Connectors** — Must. The system shall provide connectors: `ax-odbc` (read-only SQL over the AX 2012 database with DataAreaId and RecId-based paging), `d365-odata` (data entities with `$skiptoken` paging and change tracking), `sap-rfc` (RFC_READ_TABLE with 512-byte row limit handling, BAPI reads), `sap-odata` (API_BUSINESS_PARTNER, API_PRODUCT_SRV, API_BILL_OF_MATERIAL_SRV), and `file` (CSV, XLSX, Parquet with declared schema and codepage).
  - Each connector supports incremental extraction by `ModifiedDateTime`/`RecVersion` (AX) or `AEDAT`/`LAEDA` (SAP) when available; otherwise full extract.
  - Extraction throughput target 20,000 rows/s per connector worker on the dev cluster.
- **FR-10.3 Mapping DSL** — Must. The system shall express mappings as versioned YAML: source object, target entity, key definition (natural key columns), field rules (`copy`, `const`, `concat`, `lookup` against staged or target tables, `enum` with explicit table and default, `coerce` with type and format, `expr` in a restricted expression language, `default`), and `reject_when` conditions with reason codes.
  - Type coercions: AX `utcdatetime`/SAP `DATS`+`TIMS` to timestamptz with source zone; `NoYes` and `X`/blank to boolean; decimals with explicit scale; trim and codepage normalization.
  - Every mapping ships unit tests (input row → expected output or rejection) runnable in CI.
- **FR-10.4 Validation stage** — Must. The system shall validate staged, mapped rows with declarative expectations (not null, unique, FK-exists in target or staging, regex, range, set membership, row-count within ±N% of previous run) and a target-API dry run that calls the wave 6–9 endpoints with `X-Passage-Dry-Run: true` to collect business validation errors without writing.
  - Validation output: per-row status (ok, warn, reject) with reason codes; a run proceeds to load only when rejects ≤ the project's threshold (default 0.5%).
- **FR-10.5 Load through public APIs** — Must. The system shall load exclusively through versioned bulk endpoints (`POST /org/v1/import`, `/party/v1/parties:bulk`, `/product/v1/products:bulk`, `/engineering/v1/bom-versions:bulk`, …) with batches of 500 and `Idempotency-Key = sha256(run_id, object, natural_key)`, never by direct SQL into domain databases.
- **FR-10.6 Parallel, checkpointed, resumable loads** — Must. The system shall partition each object by hash of the natural key into N workers (default 8), checkpoint every batch in the run ledger, and resume from the last checkpoint after worker or pod failure without duplicates.
  - Target: 50k customers + 20k vendors + 100k items + 10k BOMs in < 30 min including reconciliation.
- **FR-10.7 Idempotent re-run** — Must. The system shall compute a content hash per mapped row; on re-run, rows with an unchanged hash and a successful prior load are skipped, changed rows are upserted by natural key, and rows absent from the source are reported (never deleted) unless the project enables `retire_missing` which sets the target to `inactive`.
- **FR-10.8 Reconciliation** — Must. The system shall produce, per object and run: source count, staged, mapped, rejected, loaded, skipped; per-column hash totals (sum of `hashtext` over normalized values for 1–20 chosen columns) on source-mapped vs target-read; and a stratified random sample (default 1%, min 100, max 5,000 rows) with field-by-field compare and per-type tolerance (decimals to scale, strings after trim/casefold, dates exact).
  - A run is `reconciled` only when counts tie, hash totals match, and sample mismatches = 0; otherwise `reconciled_with_exceptions` with every mismatch in the exception queue.
- **FR-10.9 Exception queue and re-run** — Must. The system shall list rejected and mismatched rows with reason, source key, mapped payload and target response; a user may edit the staged row, add a mapping override, or mark `accepted_difference` with a reason, then re-run only the exception set.
- **FR-10.10 Run ledger and lineage** — Must. The system shall keep an append-only ledger: for every loaded target record, the run id, source object, source key, content hash and mapped payload hash, queryable from the target record's audit tab ("migrated from AX CustTable US-00123 in run 42").
- **FR-10.11 Migration workspace** — Must. The system shall provide a workspace with project list, run history, per-stage progress, reconciliation report (downloadable as XLSX/PDF for sign-off), and exception queue.
- **FR-10.12 Packaging** — Should. The system shall package connectors and standard mapping packs as Helm sub-charts and OCI artifacts (`passage-migration-ax`, `passage-migration-sap`) versioned independently of the core service.
- **FR-10.13 PII handling in staging** — Must. The system shall encrypt PII-flagged staging columns at rest with a per-project key, purge staging data after a configurable retention (default 30 days after the run is signed off), and exclude PII from exception-queue exports unless the user holds `pii.read`.

### 1.10.4 Data model

**MigrationRun**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | yes | PK |
| tenant_id / project_id | uuid | yes | FK project(name, source_type, objects[], thresholds) |
| run_no | int | yes | sequence per project |
| mode | enum | yes | dry_run, load, reconcile_only, exceptions_only |
| status | enum | yes | created, extracting, staging, mapping, validating, loading, reconciling, reconciled, reconciled_with_exceptions, failed, cancelled |
| mapping_versions / connector_versions | jsonb | yes | pinned at start |
| started_at / ended_at / started_by | timestamptz, uuid | yes | |
| counts | jsonb | yes | per object: source, staged, mapped, rejected, loaded, skipped, mismatched |
| parent_run_id | uuid | no | for exceptions_only re-runs |
| workflow_id | text | yes | Temporal |
| signed_off_by / signed_off_at | uuid, timestamptz | no | enables staging purge timer |

**StagingRow** (one physical table per source object, common columns)

| Field | Type | Required | Notes |
|---|---|---|---|
| run_id / object | uuid, text | yes | partition key |
| source_key | text(200) | yes | e.g. `USMF|US-00123` or `0000100123` |
| raw | jsonb | yes | source columns verbatim (PII columns encrypted) |
| mapped | jsonb | no | output of mapping |
| content_hash | bytea(32) | no | sha256 of normalized mapped payload |
| status | enum | yes | staged, mapped, rejected, validated, loaded, skipped, failed, mismatched, accepted_difference |
| reason_code / reason_detail | text | no | e.g. `MAP-ENUM-UNMAPPED`, `VAL-FK-MISSING`, `API-422` |
| target_id | uuid | no | id returned by load |
| attempt | int | yes | increments per re-run |

**MappingSpec**

| Field | Type | Required | Notes |
|---|---|---|---|
| id / project_id | uuid | yes | |
| source_object / target_entity | text | yes | e.g. `CustTable` → `party.customer_role` |
| version | int | yes | immutable once a run pinned it |
| natural_key | text[] | yes | columns of the mapped payload |
| rules | jsonb | yes | ordered field rules |
| enum_tables | jsonb | no | {field: {source_value: target_value}, default} |
| reject_when | jsonb | no | [{expr, reason_code}] |
| tests | jsonb | yes | at least 1 case; CI fails on 0 |
| checksum | text | yes | sha256 of the YAML |

**LineageEntry** — run_id, object, source_key, target_entity, target_id, content_hash, loaded_at, idempotency_key (unique). **ReconciliationResult** — run_id, object, counts jsonb, hash_totals jsonb {column: {source, target, match}}, sample jsonb {size, mismatches[]}, status.

Invariants: a run's mapping versions never change after `extracting`; `loaded` rows have a lineage entry and a target_id; idempotency keys are unique across all runs; a `reconciled` status requires mismatches = 0 and rejects = 0 or all `accepted_difference`. Indexing: staging tables partitioned by `run_id` (list partitioning, dropped on purge); `(run_id, object, status)` btree; `lineage(target_entity, target_id)` for the audit tab; `lineage(idempotency_key)` unique.

### 1.10.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | /migration/v1/projects | Create project | `Idempotency-Key` | Administer migration |
| POST | /migration/v1/projects/{id}/mapping-specs | Upload YAML; returns version and test results | checksum-idempotent | Maintain migration mappings |
| POST | /migration/v1/projects/{id}/runs | Start run (`mode`, `objects[]`, `from_checkpoint`) | key header; one active run per project | Run migrations |
| GET | /migration/v1/runs/{id} | Status and counts per stage | n/a | View migrations |
| POST | /migration/v1/runs/{id}:cancel | Cancel after current batch | key header | Run migrations |
| GET | /migration/v1/runs/{id}/reconciliation | Report (JSON; `Accept: application/vnd.openxmlformats…` for XLSX) | n/a | View migrations |
| GET | /migration/v1/runs/{id}/exceptions?object=&reason= | Exception queue | n/a | View migrations |
| PATCH | /migration/v1/exceptions/{rowId} | Edit staged row, set accepted_difference | version check | Resolve migration exceptions |
| POST | /migration/v1/runs/{id}:rerun-exceptions | Child run over exception set | key header | Run migrations |
| POST | /migration/v1/runs/{id}:sign-off | Sign-off; starts purge timer | key header | Approve migration sign-off |
| GET | /migration/v1/lineage?entity=party&id= | Lineage for a target record | n/a | View migrations |
| POST | /migration/v1/connectors/{type}:test | Connectivity and schema probe | pure | Administer migration |

Validation: run start rejects if any object's mapping tests fail or a dependency object has no reconciled run in the project (override with `allow_unreconciled_deps=true` and the Administer duty); mapping upload rejects unknown functions, unmapped enum values without a default, and natural keys not produced by the rules.

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| dev.passage.migration.run.started.v1 | run start | run_id, project_id, mode, objects[], mapping_versions | notification, audit |
| dev.passage.migration.run.stage.completed.v1 | each stage | run_id, object, stage, counts | workspace progress, audit |
| dev.passage.migration.run.completed.v1 | terminal status | run_id, status, counts{staged, loaded, rejected, skipped, mismatched}, hash_match bool | notification, reconciliation UI, audit |
| dev.passage.migration.exception.raised.v1 | reject or mismatch | run_id, object, source_key, reason_code | stewardship queue (wave 7 style) |
| dev.passage.migration.run.signedoff.v1 | sign-off | run_id, signed_off_by, purge_at | audit, purge job |

### 1.10.6 User experience

| Screen | Primary actions | Key fields/columns | Duty required |
|---|---|---|---|
| Migration workspace | Tiles: projects, active runs, exceptions open, last reconciliation status | counts, status chips | View migrations |
| Project detail | Edit objects/order, upload mapping, view test results, start run (mode picker), connector test | object, mapping version, tests passed, last run | Administer migration |
| Run monitor | Per-object stage pipeline with counts and throughput, cancel, logs link (Tempo trace) | stage, rows/s, ETA, errors | Run migrations |
| Reconciliation report | Counts table, hash totals per column, sample mismatches grid, download XLSX/PDF, sign-off | object, source/target counts, hash match, mismatch fields | View / Approve migration sign-off |
| Exception queue | Filter by reason, edit row inline, bulk accept difference with reason, re-run selection | source key, reason code, detail, mapped payload, target response | Resolve migration exceptions |
| Mapping editor (read-mostly) | View rules, enum tables, test cases; edit opens YAML with schema validation | rule, source, target, function | Maintain migration mappings |
| Lineage tab (on any migrated record) | Open run, open staged row | run no, source object, source key, loaded at | View migrations |

Interaction rules: run monitor auto-refreshes every 2 s via SSE; exception grid supports keyboard navigation and inline JSON edit with schema hints; bulk accept limited to 1,000 rows per action and requires a reason of ≥ 10 characters; reconciliation report is read-only once signed off; PII columns render masked unless `pii.read`.

### 1.10.7 Business rules and edge cases

- **BR-10.1** Run state machine: `created → extracting → staging → mapping → validating → loading → reconciling → reconciled | reconciled_with_exceptions`; any stage `→ failed` after exhausting retries (5 per batch, exponential backoff 1–60 s); `→ cancelled` from any non-terminal state after the current batch commits; `dry_run` stops after `validating` with a simulated load report.
- **BR-10.2** Only one active run per project; a second start returns 409 with the active run id.
- **BR-10.3** Natural keys are normalized before hashing: trim, casefold for codes that the target treats case-insensitively, leading-zero preservation for SAP numbers (never cast to integer).
- **BR-10.4** Enum mapping with no match and no default rejects the row with `MAP-ENUM-UNMAPPED` and the value; mapping with default records a `warn` and the default used.
- **BR-10.5** Decimal coercion: AX `real` values are read as numeric(32,16) and rounded half-even to the target scale; a row whose rounding changes the value by more than 0.5 units of the last target decimal is flagged `warn` with code `MAP-PRECISION-LOSS`.
- **BR-10.6** Dates: AX `1900-01-01` and SAP `00000000` / `99991231` are nulls or open-ended markers by rule, never literal dates.
- **BR-10.7** Load batches are atomic per batch on the target side; a batch with one failing row is retried as singletons so one bad row rejects only itself.
- **BR-10.8** Re-run skip rule: skip when `content_hash` equals the last `loaded` lineage hash and the target record's version has not changed since (target `updated_at` ≤ lineage `loaded_at`); otherwise upsert; a target changed by a user after the load is reported as `TARGET-DRIFT` and not overwritten unless `overwrite_drift=true`.
- **BR-10.9** Hash totals use the same normalization on both sides; a hash mismatch with matching counts triggers an automatic widened sample (10%) before the run is marked with exceptions.
- **BR-10.10** Dependency between objects: a party role row whose party failed in the same run is rejected with `VAL-DEP-FAILED` referencing the parent source key, not retried blindly.
- **BR-10.11** Staging purge runs 30 days after sign-off (configurable 7–365); purge drops the run's partitions; lineage and reconciliation results are never purged.
- **BR-10.12** Concurrency with live users: loads run as a service account under OPA with the duties of the project owner; records created carry `created_by = migration:{run_id}` for audit.

### 1.10.8 Acceptance criteria

- **AC-10.1** (FR-10.6, FR-10.8) Given the seeded AX-shaped dataset (50k CustTable, 20k VendTable, 100k InventTable/EcoResProduct, 10k BOMVersion/BOM), when a `load` run executes on the dev cluster, then it reaches `reconciled` in < 30 min with counts tied, all hash totals matching and 0 sample mismatches.
- **AC-10.2** (FR-10.7) Given the completed run, when the identical run executes again, then loaded = 0, skipped = 170,000, and no target `updated.v1` events are published.
- **AC-10.3** (FR-10.3) Given the mapping test suites for the 12 standard Phase 1 objects, when CI runs, then every test passes including type coercion (utcdatetime, NoYes, DATS), enum mapping (Blocked, MTART), default fill and rejection reasons.
- **AC-10.4** (FR-10.6) Given a load in progress, when a loader pod is killed at batch 137, then the resumed run continues from checkpoint 136, finishes, and the lineage table has no duplicate idempotency keys.
- **AC-10.5** (FR-10.4) Given a `dry_run` with 10 rows pointing at a non-existent customer group, when validated, then 10 rows are `reject` with `VAL-FK-MISSING` and nothing is written to party-db.
- **AC-10.6** (FR-10.9) Given 25 exceptions, when 20 are edited and 5 marked accepted_difference, then `rerun-exceptions` loads 20, skips 5, and the parent run moves to `reconciled`.
- **AC-10.7** (BR-10.8) Given a customer modified by a user after load, when the run is repeated with changed source data, then the row is `TARGET-DRIFT` and the user's change is preserved.
- **AC-10.8** (FR-10.2) Given the `sap-rfc` connector against the recorded RFC_READ_TABLE fixture with 600-byte rows, when extracting KNA1, then all columns are reassembled across two field groups with 0 truncation.
- **AC-10.9** (FR-10.13) Given a signed-off run, when 30 days pass on the Temporal test clock, then staging partitions are dropped and lineage rows remain.
- **AC-10.10** (FR-10.1) Phase 1 exit: given the synthetic AX extract of the demo manufacturer, when the full project runs, then every object is `reconciled`, the wave 6–9 workspaces show the migrated data, and the reconciliation XLSX is attached to the sign-off record.

### 1.10.9 Test plan and fixtures

Fixtures: `migration/ax-seed/` (generated AX-shaped CSVs with real column names: DirPartyTable, DirPerson, DirOrganization, LogisticsPostalAddress, CustTable 50k, VendTable 20k, EcoResProduct/InventTable 100k, EcoResProductMaster 2k, BOMVersion/BOM 10k, RouteVersion/RouteOpr 3k, WrkCtrTable 200; generator seed 10); `migration/sap-seed/` (KNA1/KNB1 10k, LFA1/LFB1 5k, BUT000/BUT100 15k, MARA/MARC/MARD/MARM 20k, MAST/STKO/STPO 5k, PLKO/PLPO/MAPL 2k, CRHD 100); `migration/mapping-tests/*.yaml` (≥ 10 cases per object); `migration/rfc-recorded.json`; `migration/demo-manufacturer-ax/` (the phase-exit extract).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Mapping DSL | unit | every rule function | 100% branch coverage on functions; all object test cases pass |
| Full load performance | performance | end-to-end run | < 30 min, reconciled |
| Idempotent re-run | E2E | run twice | 0 loaded second time |
| Pod-kill resume | chaos | loaders | no duplicate idempotency keys |
| Dry run isolation | contract | target APIs | 0 writes on `X-Passage-Dry-Run` |
| Connector contracts | contract (recorded) | ax-odbc, d365-odata, sap-rfc, sap-odata, file | schema probes and paging match fixtures |
| Reconciliation math | property | hash totals and sampling | injected drift in 1 of 10k rows detected ≥ 99% with widened sample |
| Exception re-run | E2E | exceptions_only | parent reaches reconciled |
| PII at rest | security | staging | encrypted columns unreadable with DB role; purge verified |
| Event contracts | contract | 5 events | registry accepts |

Reconciliation checks: source count = staged; staged = mapped + rejected; mapped = loaded + skipped + failed; Σ per-object loaded = lineage rows for the run; target row count delta (before/after run) = loaded − upserts of existing keys.

### 1.10.10 Deployment and operations

Helm chart `passage-migration` (TS orchestration service ×2, Go loader workers as a scalable Deployment 2–16 replicas via HPA on queue depth, CloudNativePG `migration-db` with staging partitions on a separate tablespace, Temporal namespace `migration`); sub-charts `passage-migration-ax` (ODBC driver image with FreeTDS, mapping packs) and `passage-migration-sap` (RFC SDK not redistributable: image expects the customer-mounted NW RFC library; OData path works without it). Config: `MIG_WORKERS_DEFAULT=8`, `MIG_BATCH_SIZE=500`, `MIG_REJECT_THRESHOLD_PCT=0.5`, `MIG_SAMPLE_PCT=1`, `MIG_STAGING_RETENTION_DAYS=30`, `MIG_PII_KEY_RING` (Vault path), connector secrets in Vault. Feature flags: `migration.retire_missing`, `migration.overwrite_drift`, `migration.sap_rfc`. Scheduled jobs: hourly purge timer check; nightly lineage integrity check. SLOs: loader throughput ≥ 100 rows/s per worker sustained; run monitor update lag < 2 s; availability 99.5% (non-critical path). Alerts: run `failed`; batch retry rate > 5%; staging tablespace > 80%; connector auth failure. Runbook: "Resume a failed run" (`passagectl mig resume --run`), "Rotate PII key" (re-encrypt staging for active runs), "Widen sample manually". Rollback: previous digest; staging schema per run is unaffected; mapping packs are OCI artifacts pinned by digest in the project.

### 1.10.11 Migration from AX / SAP

This wave is the migration tool; its own "migration" is the catalog of standard objects it ships with, each a mapping pack with tests:

| Source object (connector) | Target entity (wave) | Pack notes |
|---|---|---|
| AX CompanyInfo/OMLegalEntity, OMOperatingUnit, OMHierarchy*, InventSite, InventLocation, LogisticsPostalAddress, FiscalCalendar*, UnitOfMeasure* (ax-odbc) | wave 6 entities | natural keys DataAreaId, OMOperatingUnitNumber, SiteId, InventLocationId |
| AX DirPartyTable, DirPerson, DirOrganization, DirPartyLocation, LogisticsElectronicAddress, CustTable, VendTable, ContactPerson, CustGroup, VendGroup, PaymentTerm, DlvTerm, DlvMode (ax-odbc) | wave 7 entities | natural keys PartyNumber, `DataAreaId|AccountNum` |
| AX EcoResProduct*, InventTable, InventTableModule, InventItemSetupFilter, InventItemGroup, InventModelGroup, UnitOfMeasureConversion, EcoResAttribute*/Category*, InventItemGTIN (ax-odbc) | wave 8 entities | natural keys DisplayProductNumber, `DataAreaId|ItemId` |
| AX BOMTable, BOMVersion, BOM, RouteTable, RouteVersion, RouteOpr, RouteOprTable, WrkCtrTable, WrkCtrResourceGroup, WrkCtrCapability*, WorkCalendar* (ax-odbc) | wave 9 entities | natural keys BOMId, `BOMId|LineNum`, RouteId, `RouteId|OprNum`, WrkCtrId |
| D365 F&O entities CustCustomerV3Entity, VendVendorV2Entity, EcoResReleasedProductV2Entity, BOMEntity, RouteEntity, WorkCenterEntity (d365-odata) | waves 7–9 | same natural keys via entity key fields |
| SAP T001, T001W, T001L, CSKS, ADRC, T009/T009B, T006 (sap-rfc / file) | wave 6 | keys BUKRS, WERKS, `WERKS|LGORT` |
| SAP KNA1/KNB1, LFA1/LFB1, BUT000/BUT100/BUT020, ADR2/ADR6, KNVK, T052, KNKK (sap-rfc / sap-odata API_BUSINESS_PARTNER) | wave 7 | keys KUNNR, LIFNR, PARTNER; CVI links honored |
| SAP MARA, MAKT, MARC, MARD, MARM, MEAN, KLAH/KSML/CABN/CAWN/AUSP (sap-rfc / API_PRODUCT_SRV) | wave 8 | keys MATNR, `MATNR|WERKS` |
| SAP MAST/STKO/STPO/STAS, PLKO/PLPO/PLAS/MAPL, CRHD/CRCA/KAKO, AENR (sap-rfc / API_BILL_OF_MATERIAL_SRV) | wave 9 | keys `STLNR|STLAL`, `PLNNR|PLNAL`, ARBPL|WERKS |

Transformation notes are owned by the waves above; this wave contributes the shared functions (`ax_datetime`, `ax_noyes`, `sap_dats`, `sap_lead_zero`, `codepage_normalize`, `lookup_party_alias`). Validation rules are the union of each target wave's rules plus cross-object dependency checks. Not migrated by this wave: any transactional table (CustTrans, VendTrans, InventTrans, InventSum, BKPF/BSEG, MSEG, VBAK/VBAP, EKKO/EKPO — wave 49 packs), AX security roles (re-modeled in wave 2's admin UI), DMF/LSMW definitions themselves (replaced by mapping packs), X++/ABAP customizations (non-goal).

### 1.10.12 Dependencies, risks, open questions

Depends on: waves 6, 7, 8 and 9 (bulk endpoints with `X-Passage-Dry-Run` support and natural-key upsert semantics), wave 4 (Temporal, outbox), wave 5 (audit tab integration for lineage, import/export tool), wave 2 (service-account duties, Vault secrets).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Bulk endpoints in waves 6–9 lack dry-run or upsert semantics when wave 10 starts | Medium | High | Contract requirement added to each wave's Definition of Done; wave 10 contract tests run against waves 6–9 in CI |
| SAP RFC SDK licensing blocks a self-contained connector image | High | Medium | OData and flat-file paths are first-class; RFC is a customer-mounted library |
| Synthetic AX fixture does not resemble real data quality | Medium | Medium | Fixture generator injects 2% noise (codepage, trailing spaces, invalid enums); first real customer extract (PRD open question 2) replaces it by wave 20 |
| Reconciliation sampling misses rare drift | Low | Medium | Hash totals per column catch systematic drift; widened sample on mismatch |

Open questions: (1) Is the mapping DSL YAML plus a restricted expression language enough, or do packs need a plug-in hook in TypeScript? Proposed: YAML only in v1, hooks behind a flag by wave 49; decide by wave 16. (2) Should lineage be stored in each domain database (next to the record) or only in migration-db? Proposed: migration-db with the audit tab querying it; decide by wave 11. (3) Who owns sign-off on reconciliation for the phase-exit demo — product or the implementation partner persona? Proposed: both sign; decide at the phase-1 exit review.
