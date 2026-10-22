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
| Teams and team types | OMTeam, OMTeamType, OMTeamMembershipCriterion (Organization administration → Teams) | HR OM work groups (PP01 organizational units), S/4 Responsibility Management teams (Fiori "Manage Teams and Responsibilities") | org-service `TeamType`, `Team`, `TeamMember` | full |
| Legal entity copy / template | Legal entity copy (SysDataExport/SysDataImport), D365 "Copy into legal entity" configuration project | EC01 copy company code, EC02 copy plant (organizational object copy) | `POST /org/v1/legal-entities/{id}:copy` (FR-6.15) | improved (invariants checked at copy) |
| Currency rounding rules | Currency form: general, price, purchase, sales rounding with round-off type normal/downward/rounding-up | TCURX decimals, OB90 rounding rules per company code and currency | `CurrencyRounding` per (currency, purpose, legal entity) (FR-6.16) | full |
| Organization name translations | Organization translations on OMOperatingUnit / CompanyInfo | T001 / CSKT / CEPCT texts per SPRAS | `org_unit_translation` (FR-6.17) | full |

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
- **FR-6.14 Teams and team types** — Should. The system shall maintain team types (code, name, allowed member kinds `user` / `party`) and teams (code unique per tenant, name, description, team type, optional owning operating unit, `effective_from`/`effective_to`, status) with members (Keycloak user id or wave-7 party id, member role `member` / `lead`, `valid_from`/`valid_to`).
  - Teams are cross-organization groups, not hierarchy nodes; they are referenced by stewardship ownership (FR-7.11 `owner_team_id`), wave-4 work-item queues, address-book access (FR-7.18) and readiness-check assignment (FR-9.17).
  - Limits: 5,000 teams per tenant, 2,000 members per team; membership changes publish `dev.passage.org.team.membership.changed.v1` (team_id, added[], removed[], as_of).
  - Membership criteria (AX OMTeamMembershipCriterion) are an optional Rego filter over user attributes (e.g. `department == "D100"`), evaluated nightly by job ORG-J5; manual members are never removed by the rule.
- **FR-6.15 Legal-entity copy from template** — Should. The system shall create a legal entity by copying an existing legal entity or a template package: number sequences (reset to start values), fiscal calendar assignment, parameters (§1.6.13), address purposes, and optionally sites and warehouses; code, name, country and accounting currency are supplied by the user.
  - Runs as Temporal workflow `copy-le-{target_code}`; every copied object receives a new id; transactional data is never copied.
  - Copy of a 5-site, 20-warehouse entity completes in < 30 s; the result is validated against the same invariants as a manual create before commit.
- **FR-6.16 Currency rounding rules** — Must. The system shall store, per currency, purpose (general, price, purchase, sales, tax, cash) and optional legal entity, a rounding unit (0.000001–1,000) and method (half_even, half_up, down, up); the default is the ISO 4217 minor unit with half_even, and consumers call `GET /org/v1/currencies/{code}/round?amount=&purpose=`.
  - Cash rounding (CHF 0.05, SEK 1.00) uses the `cash` purpose; consumed by waves 26 and 30.
  - Legal-entity rows override tenant rows for the same (currency, purpose).
- **FR-6.17 Organization name translations** — Should. The system shall store translated names of legal entities, operating units, sites and warehouses per BCP 47 language, with fallback to the base name; printed documents (wave 46) render the name in the document language.

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

**Team / TeamMember / CurrencyRounding (FR-6.14, FR-6.16)** — team: id, tenant_id, code text(20) unique per tenant, name text(60), team_type_id FK, operating_unit_id FK null, effective_from date, effective_to date null, status (active, inactive), membership_rule text null (Rego), version; team_member: team_id, member_kind (user, party), member_id uuid, member_role (member, lead), valid_from date, valid_to date null, source (manual, rule), unique (team_id, member_kind, member_id, valid_from); currency_rounding: currency_code char(3), purpose enum, legal_entity_id uuid null, unit numeric(16,6) > 0, method enum, PK (currency_code, purpose, coalesce(legal_entity_id, nil-uuid)). org_unit_translation: owner_type, owner_id, language text(5), name text(100), PK (owner_type, owner_id, language).

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
- **BR-6.12** A team member's validity must lie inside the team's effective range (`ORG-TEAM-MEMBER-RANGE`); a team whose `effective_to` is in the past accepts no new members (`ORG-TEAM-EXPIRED`); deactivating a team that owns parties reassigns ownership to the team lead or, with no lead, raises a stewardship item per party.
- **BR-6.13** Rounding: `round(a, u, m) = u × R_m(a / u)` computed in numeric(28,12); half_even sends exact ties to the even multiple; a unit that is not a power-of-ten multiple of the minor unit or 0.05/0.25/0.5 multiples thereof is rejected (`ORG-CUR-ROUNDING-UNIT`).
- **BR-6.14** Legal-entity copy keeps site and warehouse codes (unique per legal entity) and rewrites tenant-unique codes with the target code as prefix; a collision aborts the whole copy with no partial result.

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
- **AC-6.10** (FR-6.14, BR-6.12) Given team "MD stewards" effective 2026-01-01..2026-12-31, when a member is added with `valid_from` 2027-01-15, then 422 `ORG-TEAM-MEMBER-RANGE`; given a party owned by the team, then it appears in each current member's stewardship queue.
- **AC-6.11** (FR-6.16, BR-6.13) Given CHF cash rounding 0.05 half_up, when 10.024 and 10.025 are rounded, then 10.00 and 10.05; given EUR general 0.01 half_even, then 2.345 → 2.34 and 2.355 → 2.36.
- **AC-6.12** (FR-6.15) Given USMF with 2 sites, 5 warehouses and 12 number sequences, when copied to CAMF (CA, CAD), then CAMF has 2 sites, 5 warehouses, 12 sequences at their start values, 0 transactional rows, and completes in < 30 s.

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

### 1.6.13 Parameters and setup

Organization parameters live in the wave-3 metadata registry as typed settings; each row below is one setting key in `org.*`.

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| `org.le.code_pattern` | tenant | regex | `^[A-Z0-9]{2,8}$` | regex ≤ 40 chars | Validates legal entity codes (FR-6.1, BR-6.1) | DataAreaId (4 chars) on CompanyInfo | BUKRS (4 chars), OX02 |
| `org.le.default_language` | legal entity | BCP 47 | `en-US` | seeded languages | Default UI and document language | CompanyInfo.LanguageId | T001-SPRAS |
| `org.time_zone` | legal entity / site / warehouse | IANA zone | tenant default | IANA list | Date boundaries for postings, cut-offs, timers | Legal entities → Time zone (D365) | ADRC-TIME_ZONE on plant address, system time zone STZAC |
| `org.le.accounting_currency` | legal entity | ISO 4217 | none (required) | active currencies | Ledger currency; immutable after first posting | Ledger form → Accounting currency | T001-WAERS (OB22 for parallel currencies) |
| `org.le.fiscal_calendar` | legal entity | FK | none (required before wave 11 posting) | calendars with ≥ 1 generated year | Period resolution for every posting | Ledger form → Fiscal calendar | OB37 (T001-PERIV) |
| `org.cal.opening_closing_periods` | calendar | bool | true | true / false | Generates period 0 and 99 per year (FR-6.7) | FiscalCalendar → Create year (opening/closing periods) | — (SAP uses special periods) |
| `org.cal.special_periods` | calendar | int | 0 | 0–4 | Adds adjustment periods 13–16 (open question 2) | — | T009-ANZBP via OB29 |
| `org.cal.week_start` | calendar | enum | Monday | Monday–Sunday | Anchors 4-4-5 / 13-period generation | FiscalCalendar week start | Week-based fiscal year variant (OB29) |
| `org.hier.max_depth` | tenant | int | 15 | 5–25 | Publish validation (FR-6.3) | — | Standard hierarchy levels (OKEON) |
| `org.hier.max_nodes` | tenant | int | 50,000 | 1,000–200,000 | Publish validation; `ORG_HIERARCHY_MAX_NODES` | — | — |
| `org.hier.backdate_allowed` | hierarchy type | bool | false | true / false | When false, BR-6.3 duty is not grantable for that type | Hierarchy designer publish date rule | — |
| `org.addr.format` | country | template | UPU-style template for US, UK, DE, FR, IN, MX, CA | template tokens `{street_1}` … `{postal_code}` | Renders `formatted` (FR-6.6) | Address setup → Address format (LogisticsAddressFormatHeading) | Address layout key T005-LANDA (ADDRESS_INTO_PRINTFORM) |
| `org.addr.postal_check` | country | enum | strict for 7 v1 countries, off otherwise | off / warn / strict | Postal code regex enforcement | Address parameters → ZIP/postal code validation | T005 postal code check rule (OY17) |
| `org.addr.geocoder` | tenant | adapter id | `null` | `null` / registered adapter | Geocoding hook (FR-6.6) | Address parameters → geocoding (Bing Maps, D365) | Geographical Enablement Framework / DQM |
| `org.cur.rounding` | currency × purpose (× legal entity) | unit + method | ISO minor unit, half_even | BR-6.13 | Monetary rounding service (FR-6.16) | Currency → Rounding rules | OB90, TCURX |
| `org.uom.decimal_precision` | unit | int | per seed (EA 0, KG 3) | 0–6 | Display rounding (BR-6.8) | Units → Decimal precision | T006-ANDEC |
| `org.ou.number_sequence` | tenant | sequence | `OU-{#####}` | wave-5 sequences | Operating unit numbers | Organization administration parameters → Number sequences | Cost center / profit center manual keys (KS01/KE51) |
| `org.team.rule_evaluation` | tenant | bool | false | true / false | Enables job ORG-J5 | OMTeamMembershipCriterion | — |
| `org.switcher.all_scope` | tenant | bool | false | true / false | Allows read-only `ALL` legal-entity scope behind a duty (FR-6.10) | Cross-company queries | — |

### 1.6.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Legal entities | list inquiry | status, country, currency | code, name, country, currency, calendar, time zone, status | `org.legal_entity` | OMLegalEntity list; SSRS Legal entities list | OX02 overview (T001) |
| Organization hierarchy as of date | drill-down | hierarchy type, purpose, as-of date, root node | tree, depth, node type, effective range, child count | closure table | Organization hierarchy inquiry; SSRS Organization chart | OKENN, KSH3, KCH3 |
| Hierarchy version comparison | drill-down | type, version A, version B | added, removed, moved nodes with old/new parent | `hierarchy_node` | Hierarchy designer "view changes" | Change documents on SETHEADER/SETNODE (RSSCD100) |
| Operating units | list inquiry | type, default legal entity, status, hierarchy membership | number, name, type, manager, legal entity, hierarchies | `org.operating_unit` | SSRS Operating units list | KS13 cost center report, KE5X profit center list |
| Sites, warehouses and locations | list inquiry | legal entity, type, status | site, warehouse, type, address, time zone, location count | `org.site`, `org.warehouse` | InventSite / InventLocation lists | OX10 / OX09 (T001W, T001L) |
| Fiscal periods | list inquiry | calendar, year | period no, name, start, end, type, week53 | `org.fiscal_period` | Fiscal calendars → periods | OB29 periods (T009B) |
| Unit conversions | operational report | class, unit, product-specific yes/no | from, to, factor, offset, inverse, round-trip check | `org.uom_conversion` | SSRS Unit conversions list | CUNI (T006) |
| Unassigned structure | KPI | legal entity | warehouses without address, legal entities without calendar, operating units in no hierarchy, sites without warehouse | integrity projection | — | 1KE1 profit center assignment monitor |
| Address validation exceptions | operational report | country, owner type, rule | address, failed rule, owner, age | `org.postal_address` | Address validation (D365) | DQM address check results |
| Teams and members | list inquiry | team type, as-of date, member | team, member, role, source, valid from/to | `org.team_member` | Teams (OMTeam) | Responsibility Management teams |
| Organization change log | drill-down | entity, user, date range | field, old, new, user, timestamp | wave-5 audit | Database log (SysDatabaseLog) | Change documents (KS05, OX02 table logs) |

### 1.6.15 Printed and electronic documents

| Document | Trigger | Layout elements (header/line/footer fields) | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Organization chart | on demand from hierarchy designer | header: hierarchy type, purpose, as-of date; body: node boxes (code, name, manager); footer: version no, printed by, timestamp | PDF, SVG | none | none |
| Fiscal period schedule | on calendar generation or on demand | header: calendar, year, pattern; lines: period no, name, start, end, type; footer: generated by | PDF, XLSX | none | retained with ledger configuration for 10 years (HGB §257, Code de commerce L123-22) |
| Organization model package | export (FR-6.12) | manifest (schema version, sha256, counts) + JSON bodies | JSON (electronic) | `ORGPKG-{yyyy}-{####}` | 7 years as configuration evidence |

### 1.6.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| ORG-J1 Closure-table integrity check | daily 02:00 tenant local | hierarchy_type_id | read-only; checkpoint per type | < 5 min for 10 types × 50k nodes | report only, never auto-repair; runbook "Rebuild closure table" | `org_closure_integrity_failures > 0` |
| ORG-J2 Future-dated hierarchy activation | Temporal timer at `effective_from` 00:00 legal-entity time | version_id | one timer per version; activation idempotent | < 30 s | 5 retries with backoff, then alert; previous version stays active | activation lag > 5 min |
| ORG-J3 Fiscal year roll-forward | daily check; generates next year 90 days before the last generated year ends | calendar_id | idempotent per (calendar, year) | < 10 s per calendar | custom-pattern calendars raise a stewardship item instead | no next year 30 days before year end |
| ORG-J4 Address re-validation and geocode refresh | weekly Sunday 03:00 | country_code | checkpoint per 1,000 addresses | < 1 h per 1M addresses | adapter outage leaves status unchanged | adapter error rate > 20% |
| ORG-J5 Team membership rule evaluation | nightly 01:30 | team_id | diff-based; re-run yields no change | < 10 min for 5,000 teams | rule error disables the rule and notifies the team lead | rule failures > 0 |
| ORG-J6 Reference data refresh (ISO 3166/4217/639 updates) | quarterly, applied after admin approval | dataset | versioned package; re-apply is a no-op | < 1 min | rejected package keeps prior version | package pending > 14 days |

### 1.6.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles that include it |
|---|---|---|---|
| View organization | `GET /org/v1/**`; Organization administration workspace; all org list pages | none | every role |
| Maintain legal entities | `POST/PATCH /org/v1/legal-entities`, `:copy`; Legal entities screen | Administer security roles (wave 2) | Organization administrator |
| Maintain operating units and teams | `/org/v1/operating-units`, `/org/v1/teams`, `/org/v1/team-types`; job ORG-J5 config | none | Organization administrator, HR administrator |
| Maintain organization hierarchies | draft create, `PUT …/nodes`; Hierarchy designer | Publish organization hierarchies for `security` purpose (maker-checker) | Organization administrator |
| Publish organization hierarchies | `POST …:publish`; ORG-J2 timers | Maintain organization hierarchies on security-purpose types | Organization administrator lead, Security administrator |
| Backdate organization hierarchies | publish with `effective_from` < today | Maintain organization hierarchies | Controller |
| Maintain sites and warehouses | `/org/v1/sites`, `/warehouses`, `/locations` | none | Organization administrator, Warehouse manager |
| Maintain fiscal calendars | `/org/v1/fiscal-calendars`, `:generate`; ORG-J3 | Close fiscal periods (wave 14) | Accounting manager |
| Maintain reference data | countries, currencies, rounding, UoM, languages; ORG-J6 approval | none | Master data steward |
| Administer organization | `/org/v1/export`, `/org/v1/import` | Approve migration sign-off (wave 10) | System administrator |

### 1.6.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| ORG-LE-CODE-RESERVED | error | "Code {code} is reserved." | DAT, SYS or ALL used | Choose another code | BR-6.1 |
| ORG-LE-DEFAULT-IN-USE | error | "{n} users have this legal entity as default." | Deactivation while users default to it | Change users' default entity | BR-6.11 |
| ORG-HIER-CYCLE | error | "Moving {node} under {target} creates a cycle." | Drop onto own descendant | Drop on a non-descendant | BR-6.4 |
| ORG-HIER-MULTI-ROOT | error | "A version must have exactly one root; found {n}." | Publish with 0 or ≥ 2 roots | Attach orphans to the root | FR-6.3 |
| ORG-HIER-OVERLAP | error | "Effective range overlaps version {v}." | Overlapping ranges per type | Adjust `effective_from` | FR-6.3 |
| ORG-HIER-BACKDATE-DUTY | error | "Backdated publish requires the Backdate duty and a reason." | Past `effective_from` without duty | Request duty or use today | BR-6.3 |
| ORG-CAL-LOCKED | error | "Fiscal year {y} has postings and cannot be regenerated." | Regenerate with postings | Create adjustment periods instead | BR-6.7 |
| ORG-CAL-GAP | error | "Periods leave a gap on {date}." | Custom boundaries not contiguous | Fix boundaries | FR-6.7 |
| ORG-ADDR-REGION-REQUIRED | error | "Region is required for {country}." | Missing region for US/CA/IN/MX | Enter region | FR-6.6 |
| ORG-ADDR-POSTAL-FORMAT | error | "Postal code {code} is not valid for {country}." | Regex failure in strict mode | Correct code or set mode warn | FR-6.6 |
| ORG-WH-LE-MISMATCH | error | "Warehouse and site belong to different legal entities." | Site change across entities | Pick a site in the same entity | FR-6.5 |
| ORG-UOM-NO-PATH | error | "No conversion from {from} to {to} within two steps." | Missing conversion | Add a conversion | FR-6.9 |
| ORG-TEAM-MEMBER-RANGE | error | "Member validity must be inside the team's effective range." | Out-of-range dates | Adjust dates | BR-6.12 |
| ORG-TEAM-EXPIRED | error | "Team {code} has ended." | Member added to ended team | Extend the team | BR-6.12 |
| ORG-CUR-ROUNDING-UNIT | error | "Rounding unit {u} is not allowed for {currency}." | Unit not a permitted multiple | Use a permitted unit | BR-6.13 |
| ORG-VERSION-CONFLICT | warning | "This record changed since you opened it." | Stale `version` | Merge or reload | BR-6.10 |

### 1.6.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| As-of hierarchy query | 2M/day; peak 3,000/min | 4 / 20 / 40 ms | 500 req/s per replica | closure ≈ 400k rows per 50k-node version, ~30 MB/yr | PK (ancestor_id, descendant_id) per version; partial index on published | served from current closure; p95 ≤ 40 ms; exports queued first |
| Legal-entity switch (shell re-scope) | 20k/day; peak 200/min | 80 / 300 / 500 ms | 50 req/s | none | OPA decision cache per (user, entity) 60 s | cache hit ratio keeps p95 ≤ 600 ms |
| Hierarchy publish (50k nodes) | 20/day | 8 / 30 / 45 s | 1 concurrent per hierarchy type | ~5 MB per version | Temporal activity; bulk insert closure | queued FIFO per type; no timeout |
| UoM conversion | 5M/day; peak 10k/min | 1 / 5 / 10 ms | 2,000 req/s per replica | negligible | in-memory conversion graph, invalidated by event | unchanged (CPU-bound, cached) |
| Address create with validation | 50k/day; peak 500/min | 20 / 80 / 150 ms (adapter excluded) | 100 req/s | ~200 MB per 1M addresses | GIN trigram on `formatted`; btree (owner_type, owner_id) | adapter calls skipped, status `unverified` |
| Fiscal year generation | 100/yr | 0.5 / 2 / 5 s | n/a | < 1 MB | (calendar_id, fiscal_year, period_no) unique | unchanged |
| Organization export package | 10/day | 2 / 10 / 20 s | 2 concurrent per tenant | 1–20 MB per package | n/a | requests beyond 2 queued |

### 1.6.20 Localization and compliance hooks

| Item | What varies | Country packs touching it (wave 48) | Extension point |
|---|---|---|---|
| Address format and postal validation | Line order, region requirement, postal regex (US ZIP+4, UK postcode, CA A1A 1A1, IN 6-digit PIN, MX 5-digit CP) | US, UK, DE, FR, IN, MX, CA in this wave; others via pack | `AddressFormatProvider` registry keyed by country |
| Legal entity registration identifiers | DE Handelsregister (HRB + court), FR SIREN/SIRET + NAF, IN CIN + PAN + TAN, MX RFC + régimen fiscal, UK Companies House number, US EIN + state of incorporation, CA BN | all seven | typed `legal_entity_registration` list contributed by pack metadata |
| Fiscal year conventions | IN April–March statutory year; UK companies any year end; MX statutory calendar year (CFF Art. 11); US 52/53-week allowed (IRC §441(f)) | IN, MX, US | calendar templates shipped by pack; FR-6.7 generator |
| Currency and cash rounding | CHF/SEK cash rounding; CAD penny elimination (cash to 0.05); MXN 2 decimals; INR 2 decimals, invoice totals often to 1.00 | CA, IN | `org.cur.rounding` rows seeded by pack |
| Holidays and time zones | Bank holidays for due-date shifting (wave 19), DST rules | all | IANA zones; holiday calendars provided as pack data, consumed by FR-9.6 and wave 19 |
| Language of documents | FR Loi Toubon (French on documents for French counterparties); CA Quebec Charter of the French language (Bill 96) | FR, CA | FR-6.17 translations; document language on legal entity |
| Privacy | Home addresses of persons are personal data (GDPR, UK GDPR, CCPA/CPRA, India DPDP Act 2023, Mexico LFPDPPP, PIPEDA) | all | `home` purpose flagged PII; masking per BR-7.9 |
| Retention | Organization master and calendars kept for tenant life + 10 years (DE HGB §257 10 y, FR 10 y, US IRS 7 y, IN Companies Act §128 8 y) | all | retention class `org-config` in wave-5 audit store |

### 1.6.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX §12.12.1 Human resources, Common — Teams (Org admin: Teams, Team types), OMTeam; register §12.99.4 #107 | PARTIAL-6 | ADDED as FR-6.14 | Team, team type, members, membership criteria; HR uses the same entity |
| AX §12.12.4 Human resources, Setup — Teams → Team types, Teams, OMTeam; register §12.99.4 #114 | PARTIAL-6 | ADDED as FR-6.14 | Setup screen is the Teams list with type filter (§1.6.17 duty "Maintain operating units and teams") |
| AX §12.29.1 Organization administration, Common — Teams / Team types, OMTeam; register §12.99.4 #232 | PARTIAL-6 | ADDED as FR-6.14 | Inquiry "Teams and members" in §1.6.14 |
| AX §12.29.4 Organization administration, Setup — Organization → Teams / Team types; register §12.99.4 #251 | PARTIAL-6 | ADDED as FR-6.14 | Rule evaluation job ORG-J5; parameter `org.team.rule_evaluation` |

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
| Vendor certifications and diversity | VendCertification, VendCertificationType (Vendors → Certification types), Diversity classifications (D365 procurement) | No ECC master field; SAP SLC/Ariba SLP supplier certificates; BP attributes | `CertificationType`, `VendorCertification`, `DiversityClassification` (FR-7.14) | full |
| Installment / payment schedules | PaymSched, PaymSchedLine (Payment schedules form) | T052R installment terms (OBB9) linked to T052 ZTERM | `PaymentScheduleLine` on `PaymentTerm` (FR-7.15) | full |
| One-time accounts | One-time customer/supplier (AR/AP parameters → One-time customer account / One-time supplier account; CustTable.OneTimeCustomer) | Account groups CPD/CPDL (T077D/T077K XCPDK), document data in BSEC | `one_time` role flag + document-level address (FR-7.16) | full |
| Head office, alternate payer, permitted payee | CustTable/VendTable.InvoiceAccount, DirPartyRelationship | KNA1-KNRZE alternate payer, KNB1-KNRZB head office (branch/head office), LFA1-LNRZA, LFZA permitted payees | `PartyRelationship` (FR-7.17) | full |
| Address books | DirAddressBook, DirAddressBookParty, address book team security (Global address book parameters) | BP authorization group BU_BEGRU, BP grouping | `AddressBook` with team access (FR-7.18) | full |
| Customer hierarchies | TAMCustomerCategory hierarchy (D365 trade allowance) | KNVH customer hierarchy (VDH1N/VDH2N), hierarchy types OVH1 | hierarchy purpose `customer_*` over Customer roles (FR-7.19) | improved (effective-dated versions) |
| Classification code lists and hold reasons | CustClassificationGroup, LineOfBusiness, smmSegment/smmSubsegment, SalesDistrict, Vendor/Customer hold reasons | KNA1-BRSCH industry, KNA1-KUKLA classification, KNVV-KDGRP, KNVV-BZIRK, block reasons | `ClassificationCode` lists (FR-7.20) | full |

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
- **FR-7.14 Vendor certifications and diversity classifications** — Should. The system shall maintain certification types (code, name, issuing body, `requires_expiry`, `reminder_days` default 30, `attachment_required`) and vendor certifications (vendor role, type, certificate number, issuer, `valid_from`/`valid_to`, document id from FR-8.20, status) plus diversity classifications per vendor (seeded: SB, WOSB, EDWOSB, MBE, VOSB, SDVOSB, HUBZone, 8(a), LGBTBE, DOBE) each optionally linked to the certification that evidences it.
  - Vendor groups may flag certification types as mandatory; an expired mandatory certification sets vendor hold `requisition` through job PTY-J6 and notifies the data owner.
  - Endpoints: `POST /party/v1/vendors/{roleId}/certifications`, `GET /party/v1/certifications?status=expiring&days=30`.
- **FR-7.15 Installment payment schedules** — Must. The system shall attach payment schedules to payment terms with 1–24 lines, each line carrying a percent or fixed amount, its own net-days or day-of-month rule and an optional cash discount; allocation modes mirror AX PaymSched: `percent`, `fixed_amount`, and `count` (N equal installments every M days or months).
  - `GET /party/v1/payment-terms/{id}/due-date` returns one (due_date, amount, discount_date, discount_amount) tuple per installment when `amount` is supplied.
  - SAP T052R rows (ZTERM, RATNR, RATPZ, RATZT) map one-to-one to schedule lines.
- **FR-7.16 One-time customers and vendors** — Must. The system shall provide one or more one-time Customer and Vendor roles per legal entity (role flag `one_time = true`, from a group with `one_time_default = true`) whose payee/sold-to name, address, tax id and bank details are captured on each document instead of on the role.
  - Consumers (purchasing 17, AP 19, sales 26, AR 30) store the captured data on the document header, the BSEC equivalent; `POST /party/v1/one-time-data:validate` runs FR-6.6 address and FR-7.4 tax-id validation and returns normalized values.
  - One-time roles are excluded from duplicate scoring; a document counterparty name/tax id used on ≥ 3 documents in 12 months raises a "convert to regular account" stewardship item (job PTY-J7).
- **FR-7.17 Party relationships: head office, alternate payer, permitted payee** — Must. The system shall maintain typed, effective-dated relationships between parties or roles: `invoice_account`, `head_office_of` / `branch_of`, `alternate_payer`, `permitted_payee`, `contact_for`, `parent_company` / `subsidiary_of`, plus tenant-defined types (AX DirRelationshipType).
  - `head_office_of` carries `decentralized_processing` (KNB1-XDEZV equivalent): when false, AR/AP open items of the branch post to the head-office account in waves 19 and 30.
  - Payment proposals (wave 19) pay only to the role's own bank accounts or to active permitted payees; adding a permitted payee is a protected-field change (FR-7.10).
  - `head_office_of` chains are acyclic and at most 5 levels deep (`PTY-REL-CYCLE`).
- **FR-7.18 Address books** — Should. The system shall maintain named address books (e.g. "US vendors", "EMEA customers") that group parties; a party may belong to many books; books carry team access lists (FR-6.14) enforced by OPA on party reads when the tenant enables `party.address_book_security`; each user has a default book applied to parties they create.
- **FR-7.19 Customer hierarchies** — Should. The system shall build effective-dated customer hierarchies with purposes `customer_pricing`, `customer_rebate` and `customer_reporting` whose nodes are Customer roles, reusing the wave-6 hierarchy engine (versions, closure table, as-of API); pricing (wave 26) and rebates (wave 29) resolve ancestors as of the document date in < 20 ms p95.
- **FR-7.20 Classification code lists and hold reasons** — Must. The system shall maintain tenant code lists for line of business, segment and subsegment, sales district, customer classification group, industry (NAICS 2022 / NACE Rev. 2.1 seeded), customer hold reasons and vendor hold reasons; role fields reference them, and every block or unblock records reason code, user, timestamp and free text.
  - Vendor `blocked` values are extended to the AX set: none, invoice, payment, requisition, all, never (never = AX "Never", exempt from inactivity blocking job PTY-J9).

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

**VendorCertification / PaymentScheduleLine / PartyRelationship / AddressBook (FR-7.14 – FR-7.18)** — vendor_certification: id, vendor_role_id FK, certification_type_id FK, number text(60), issuer text(100), valid_from date, valid_to date (required when type requires_expiry), document_id uuid null, status (valid, expiring, expired, revoked), revoked_reason text; vendor_diversity: vendor_role_id, classification_code, certification_id null, valid_from, valid_to. payment_schedule_line: payment_term_id FK, line_no int (1–24), allocation (percent, fixed_amount, count), value numeric(18,6), net_days int, day_of_month int null, offset_months int, discount_pct numeric(5,2) null, discount_days int null. party_relationship: id, type_code FK relationship_type, from_party_id, from_role_id null, to_party_id, to_role_id null, legal_entity_id null, valid_from, valid_to, attributes jsonb (e.g. decentralized_processing); unique (type_code, from_role_id, to_role_id, valid_from). address_book: id, code text(20), name, team_ids uuid[]; address_book_party (address_book_id, party_id) PK.

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
- **BR-7.13** Installment amounts: `amount_i = round(total × pct_i / 100)` using the currency's general rounding (FR-6.16); the last installment absorbs the remainder (`total − Σ previous`); percent lines must sum to exactly 100.00 (`PTY-SCHED-PCT-SUM`); each due date applies BR-7.7 to the baseline (invoice) date.
- **BR-7.14** Certification status is computed daily: `valid` while today < `valid_to − reminder_days`; `expiring` inside the window; `expired` after `valid_to`; `revoked` only manually with reason; each change emits `dev.passage.party.certification.status.changed.v1`.
- **BR-7.15** One-time roles cannot carry credit limits, bank accounts, tax registrations or posting-profile overrides on the role (`PTY-ONETIME-FIELD`); they cannot be merged as survivors.
- **BR-7.16** `head_office_of` requires both ends to hold the same role type (Customer–Customer or Vendor–Vendor) in the same legal entity; changing the head office while the branch has open items is refused through a wave-30/19 callback (returns "no open items" until those waves exist).
- **BR-7.17** A permitted payee must be an active party with at least one verified bank account; removal takes effect immediately, addition only after approval.

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
- **AC-7.11** (FR-7.15, BR-7.13) Given term "3 installments 40/30/30 at net 30/60/90" and an invoice of USD 1,000.01 dated 2026-03-01, when due dates are computed, then the amounts are 400.00 / 300.00 / 300.01 and the due dates 2026-03-31 / 2026-04-30 / 2026-05-30.
- **AC-7.12** (FR-7.14, BR-7.14) Given a vendor in group "Regulated" whose mandatory ISO 13485 certificate expires 2026-11-15 with 30 reminder days, when PTY-J6 runs on 2026-10-16, then status is `expiring` and the owner is notified; on 2026-11-16 status is `expired` and the vendor hold is `requisition`.
- **AC-7.13** (FR-7.16, BR-7.15) Given one-time vendor OTV-USMF, when a party with an identical name is created, then no duplicate candidate references OTV-USMF; when a credit limit is set on OTV-USMF, then 422 `PTY-ONETIME-FIELD`.
- **AC-7.14** (FR-7.17) Given branch B whose head office is H, when H is made a branch of B, then 422 `PTY-REL-CYCLE`; given permitted payee P added to vendor V, then P is not payable until the pending change is approved.

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

### 1.7.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| `party.dedup.mode` | tenant | enum | block_likely | off / warn / block_likely | Create behavior with candidates (FR-7.5) | Global address book parameters → Duplicate check (DirParameters) | BP duplicate check (Business Address Services / MDG matching) |
| `party.dedup.likely_weight` | tenant | decimal | 12 | 8–20 | Likely-duplicate threshold | — | MDG match threshold |
| `party.dedup.possible_weight` | tenant | decimal | 6 | 3–12 | Possible-duplicate threshold | — | — |
| `party.number_sequence` | tenant | sequence | `PARTY-{########}` | wave-5 sequences | Party numbers (FR-7.1) | Global address book parameters → Number sequences (Party ID) | BP number ranges (BUCF) and groupings (BUC2) |
| `party.customer.account_sequence` | legal entity | sequence | `CUST-{LE}-{######}` | sequences | Customer account numbers | AR parameters → Number sequences → Customer account | XDN1 number ranges per account group (OBD2) |
| `party.vendor.account_sequence` | legal entity | sequence | `VEND-{LE}-{######}` | sequences | Vendor account numbers | AP parameters → Number sequences → Vendor account | XKN1 number ranges per account group (OBD3) |
| `party.person.name_sequence` | tenant / country | enum | given_family | given_family / family_given / family_comma_given | Display name of persons | Name sequences (DirNameSequence) | BP name format rules per country (address layout key) |
| `party.tax.online_validation` | tenant | bool | true | true / false | Calls VIES/TIN adapters (FR-7.4) | EU VAT number validation (D365 sales tax parameters) | VIES check on BP tax numbers (DFKKBPTAXNUM) |
| `party.tax.cache_days` | tenant | int | 30 | 1–365 | Validation result reuse | — | — |
| `party.protected_fields` | legal entity | field set | bank account, tax registration, credit limit, vendor payment term, remit-to address, permitted payee | any role field | Fields routed to approval (FR-7.10) | Vendor approval workflow fields (D365 AP parameters → Vendor approval) | T055F sensitive fields (S_ALR_87003378), FK08/FD08 |
| `party.approval.sla_days` | legal entity | int (business days) | 2 | 1–10 | Escalation timer | Workflow escalation | — |
| `party.group.mandatory_fields` | customer/vendor group | field set | primary address; tax id for organizations | role and party fields | Save-time required fields | Customer/vendor group mandatory fields (AR/AP parameters → mandatory fields) | Field status groups per account group (OBD2/OBD3, T077D/T077K) |
| `party.group.one_time_default` | group | bool | false | true / false | Roles in group are one-time (FR-7.16) | AR/AP parameters → One-time customer/supplier | Account group one-time indicator (XCPDK) |
| `party.customer.default_group` | legal entity | FK | none | customer groups | Default on create | AR parameters → General → Default customer group | Default account group |
| `party.credit.check_type` | legal entity | enum | balance | off / balance / balance_incl_open_orders | Credit check basis for wave 27 | AR parameters → Credit rating → Credit limit type | OVA8 credit control (FSCM UKM in S/4) |
| `party.merge.revert_days` | tenant | int | 30 | 0–90 | Revert window (FR-7.6) | — | — |
| `party.cert.reminder_days` | certification type | int | 30 | 0–180 | Expiring window (BR-7.14) | Vendor certification types | — |
| `party.address_book_security` | tenant | bool | false | true / false | Enforce book team access (FR-7.18) | Global address book parameters → Use address book security | BP authorization groups (B_BUPA_GRP) |
| `party.inactivity_block_days` | legal entity | int | 0 (off) | 0–1,825 | PTY-J9 blocks roles without transactions | Vendor inactivation (D365 AP periodic "Vendor inactivation") | — |
| `party.pii_masking` | tenant | bool | true | true / false | BR-7.9 masking | Personal data / privacy (D365 GDPR) | ILM blocking / end-of-purpose check |

### 1.7.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Global address book | list inquiry | role, legal entity, type, address book, status | party number, name, type, roles, primary address, status | `party.party` | DirPartyTableListPage | BP search, S_ALR_87012180 address list |
| Customer list | list inquiry | legal entity, group, blocked, classification | account, name, group, terms, currency, credit limit, blocked | `party.customer_role` | CustTableListPage; SSRS Customer base data | S_ALR_87012179, XD03 |
| Vendor list | list inquiry | legal entity, group, hold, diversity | account, name, group, terms, hold, certifications | `party.vendor_role` | VendTableListPage; SSRS Vendor base data | S_ALR_87012086, MKVZ |
| Duplicate candidates | operational report | weight range, status, age | pair, weight, probability, level detail, age | `party.duplicate_candidate` | Duplicate check results (DirDuplicateCheck) | MDG duplicate check results |
| Critical changes | operational report | field, legal entity, status, requester | field, old (masked), new (masked), requester, approver, SLA | `party.pending_change` | Vendor change proposals (D365) | S_ALR_87012090, S_ALR_87012183 (FK09/FD09) |
| Payment terms and schedules | list inquiry | term code | net rule, discount tiers, installments, due-date preview | `party.payment_term` | SSRS Terms of payment list; Cash discount list | OBB8 / OBB9 listing (T052, T052R) |
| Certification expiry | KPI | days to expiry, type, vendor group | expiring count, expired count, vendors on hold | `party.vendor_certification` | Vendor certifications (D365) | — |
| Diversity vendor base | KPI | classification, legal entity | vendor count per classification (spend added in wave 17) | `party.vendor_diversity` | Vendor diversity (D365) | — |
| Tax registration status | operational report | status, country, type | number, status, source, validated at | `party.tax_registration` | VAT number validation log | VIES check log |
| Hold history | drill-down | role, reason, date range | hold, reason, user, timestamp | wave-5 audit | Customer/Vendor hold history | XD04 / XK04 change documents |
| Relationships and head offices | list inquiry | type, party | from, to, type, validity, decentralized flag | `party.party_relationship` | Party relationships | KNA1-KNRZE / KNB1-KNRZB lists, VDH2N |
| Merge history | drill-down | date range, status | case, survivor, merged ids, ack count, revert | `party.merge_case` | — | — |

### 1.7.15 Printed and electronic documents

| Document | Trigger | Layout elements (header/line/footer fields) | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Party master data sheet | on demand | header: party number, name, type, primary address; lines: roles per legal entity, terms, tax ids (masked); footer: printed by, timestamp | PDF | none | none |
| Bank-detail change confirmation | approval of a protected bank-account or permitted-payee change | header: party, role, change id; body: old/new IBAN masked to last 4, effective date, approvers; footer: callback-verification statement | PDF, email to the party's verified contact | `PCHG-{yyyy}-{#####}` | 10 years as payment-fraud control evidence (SOX ITGC) |
| Data subject access export | privacy officer request for a person party | header: request id, subject, date; body: all personal fields, addresses, roles, processing purposes | JSON + PDF | `DSAR-{yyyy}-{####}` | respond within 1 month (GDPR Art. 12(3)), 45 days (CCPA); request log 3 years |
| Merge certificate | merge case completed | header: case id; lines: merged ids → survivor, consumer acks; footer: operator, timestamp | PDF | merge case number | 10 years |
| Mailing labels | none | — | — | — | none: mailing labels are CRM scope (PRD non-goal) |

### 1.7.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| PTY-J1 Full duplicate re-score | weekly Sunday 01:00 tenant local | blocking key hash (16 partitions) | candidates upserted by (a, b); checkpoint per partition | < 2 h for 1M parties on 8 workers | partition retried 3×; partial results kept | queue > 1,000 open |
| PTY-J2 VIES re-validation | daily 04:00 | country | max 5,000 per run; cursor by `validated_at` | < 30 min | backoff on rate limit; leaves `unverified` | failure rate > 20% / 15 min |
| PTY-J3 Stale merge sweeper | hourly | merge_case_id | idempotent | < 1 min | alerts on cases awaiting ack > 10 min | stale cases > 0 |
| PTY-J4 Approval SLA escalation | every 15 min | legal_entity_id | Temporal timers; idempotent per task | < 1 min | escalation to approver's manager | SLA breaches per day |
| PTY-J5 Alias usage log purge | monthly | tenant | deletes rows > 12 months | < 5 min | retried next run | none |
| PTY-J6 Certification status update | daily 00:30 | vendor_role_id range | status recomputed; events only on change | < 10 min for 100k certificates | retried; holds applied only after status commit | expired-mandatory count |
| PTY-J7 One-time conversion candidates | weekly | legal_entity_id | candidate upsert by normalized name/tax id | < 15 min | none | candidates created |
| PTY-J8 Personal data end-of-purpose check | monthly | party_id range | flags candidates; never deletes without approval | < 1 h per 1M persons | report only | candidates > 0 |
| PTY-J9 Role inactivity blocking | monthly (off by default) | legal_entity_id | idempotent; skips `never` | < 15 min | dry-run report first | blocked count |

### 1.7.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles that include it |
|---|---|---|---|
| View parties | `GET /party/v1/**` (PII masked); Global address book | none | every business role |
| Maintain parties | `POST/PATCH /party/v1/parties`, tax registrations, relationships, address books | Approve master data changes | Master data steward, AR clerk, AP clerk |
| Maintain customers | customer role endpoints; Customers workspace | Post customer payments (wave 30) | AR clerk, Sales administrator |
| Maintain vendors | vendor role endpoints; certifications; Vendors workspace | Approve vendor invoices (wave 19), Release payments (wave 19) | AP clerk, Purchasing agent |
| Maintain payment and delivery terms | terms, schedules, delivery terms/modes | none | AR manager, AP manager |
| Steward master data | duplicates, stewardship queue, PTY-J1/J7 results | none | Master data steward |
| Merge parties | merge cases, `:revert` | Maintain vendors (same party), to prevent payee substitution | Master data steward lead |
| Approve master data changes | `pending-changes/{id}:approve/:reject` | Maintain vendors / Maintain customers (no self-approval, BR-7.8) | AP manager, Credit manager, Controller |
| Override duplicate check | `X-Passage-Override-Duplicate` | none | Master data steward lead |
| Read personal data | `pii.read`; DSAR export | Administer migration (bulk export risk) | Privacy officer, HR administrator |

### 1.7.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| PTY-DUP-LIKELY | error | "{n} likely duplicates found." | Weight ≥ likely threshold on create | Review candidates or override with duty | FR-7.5 |
| PTY-DUP-OVERRIDE-DUTY | error | "You are not allowed to override the duplicate check." | Override header without duty | Ask a steward | AC-7.7 |
| PTY-TAX-FORMAT | error | "{type} {number} does not match the {country} format." | Regex failure | Correct the number | FR-7.4 |
| PTY-TAX-CHECKDIGIT | error | "Check digit of {number} is wrong." | Algorithm failure (mod-97, mod-89, mod-36) | Correct the number | FR-7.4 |
| PTY-TAX-UNVERIFIED | warning | "Online validation unavailable; saved as unverified." | VIES/TIN timeout | None; PTY-J2 retries | FR-7.4 |
| PTY-ROLE-LE-HEADER | error | "Select a legal entity before adding a role." | Missing `X-Passage-LegalEntity` | Set the entity | FR-7.2 |
| PTY-ROLE-GROUP-MISSING | error | "Group {code} does not exist in {entity}." | Unknown group | Create or pick a group | FR-7.3 |
| PTY-MERGE-RUNNING | error | "Merge case {id} is already running for this party." | Concurrent merge | Wait for the running case | BR-7.11 |
| PTY-MERGE-REVERT-BLOCKED | error | "{service} cannot split post-merge transactions." | Consumer refuses revert | Correct manually | BR-7.2 |
| PTY-PENDING-SELF-APPROVAL | error | "You cannot approve your own change." | Requester = approver | Second approver | BR-7.8 |
| PTY-TERM-DISCOUNT-AFTER-DUE | error | "Discount date is after the due date." | Invalid term | Fix days | BR-7.7 |
| PTY-SCHED-PCT-SUM | error | "Installment percentages total {x}, not 100." | Percent lines ≠ 100 | Adjust lines | BR-7.13 |
| PTY-ONETIME-FIELD | error | "One-time accounts cannot hold {field}." | Forbidden field on one-time role | Capture on the document | BR-7.15 |
| PTY-REL-CYCLE | error | "This relationship creates a cycle: {path}." | Head-office loop | Choose another head office | FR-7.17 |
| PTY-CERT-EXPIRED | warning | "Mandatory certificate {type} expired on {date}; vendor on hold." | BR-7.14 | Upload renewed certificate | FR-7.14 |
| PTY-INACTIVE-ROLE | error | "Party is inactive; roles cannot be added or unblocked." | BR-7.10 | Reactivate the party | BR-7.10 |
| PTY-SEARCH-MIN-CHARS | info | "Type at least 2 characters." | Short query | Type more | FR-7.13 |

### 1.7.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| Party create with dedup | 5k/day; peak 200/min (imports via bulk path) | 120 / 400 / 800 ms | 50 req/s | ~2 KB per party; 1M parties ≈ 2 GB incl. indexes | GIN trigram `search_name`; blocking indexes | scoring moves to async; create returns 202 with candidates later |
| Typeahead search | 500k/day; peak 2,000/min | 25 / 100 / 180 ms | 300 req/s per replica | none | trigram + (tenant_id, status) | result cap drops 20 → 10 |
| Single-party scoring | per create/update | 60 / 300 / 500 ms at 1M | 100/s on 2 workers | candidates ~5% of parties | blocking keys | queue grows; HPA to 6 workers |
| Weekly full re-score | 1M parties | — | 140 parties/s per worker | — | 16 hash partitions | runtime extends; no user impact |
| Due-date / schedule computation | 2M/day from AR/AP | 1 / 3 / 6 ms | 2,000 req/s | none | term cache | unchanged |
| Merge workflow | 50/day | 20 s / 5 min / 15 min (ack-bound) | 10 concurrent | snapshot ~50 KB per case | workflow id per min(party) | queued; acks are the bottleneck |
| VIES validation | 5k/day | 300 ms / 2 s / 5 s (external) | 20 concurrent | cache rows | — | `unverified` returned immediately |

### 1.7.20 Localization and compliance hooks

| Item | What varies | Country packs touching it (wave 48) | Extension point |
|---|---|---|---|
| Tax identifiers | US EIN/SSN (masked) and 1099 vendor flag (box type consumed by wave 19); UK VAT (HMRC VAT API after VIES exit) and UTR; DE USt-IdNr and Steuernummer; FR TVA intracommunautaire and SIREN/SIRET; IN GSTIN (state code + PAN) and PAN; MX RFC with CFDI 4.0 receptor data (régimen fiscal, código postal); CA BN with GST/HST and QST | all seven | `TaxIdValidator` registry; `TaxIdAdapter` (VIES, HMRC, IRS TIN Matching, GSTN, SAT LCO) |
| Name and address conventions | Person name order, Hindi/Devanagari names, Spanish double surnames (MX), Quebec bilingual | IN, MX, CA | `party.person.name_sequence`; transliteration attribute |
| Diversity classifications | US SBA programs (8(a), HUBZone, WOSB, SDVOSB); CA Indigenous business registry; UK none statutory | US, CA | classification code list seeded by pack |
| Payment term conventions | FR LME late-payment cap (60 days from invoice or 45 days end of month, Code de commerce L441-10); DE §286 BGB 30-day default; IN MSMED Act 45-day limit for MSE suppliers; UK Late Payment Act | FR, DE, IN, UK | term validation rule per country pack (warn on terms exceeding statutory maximum) |
| Privacy and retention | GDPR/UK GDPR erasure vs retention (HGB/AO 10 y for accounting-relevant data), CCPA/CPRA, DPDP Act 2023, LFPDPPP ARCO rights, PIPEDA | all | PII flags (BR-7.9), DSAR export, PTY-J8 end-of-purpose check |
| Sanctions screening | OFAC SDN (US), UK OFSI, EU consolidated list on vendor/customer create | all | screening adapter interface (wave 48 compliance) called at role creation; result stored on role |

### 1.7.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX §12.16.1 Procurement and sourcing, Common — Vendor certifications / Vendor diversity (D365), VendCertification; register §12.99.4 #158 | PARTIAL-7 | ADDED as FR-7.14 | Expiry job PTY-J6, KPI "Certification expiry" |
| AX §12.16.4 Procurement and sourcing, Setup — Vendor certification types / Diversity classifications; register §12.99.4 #165 | PARTIAL-7 | ADDED as FR-7.14 | Parameter `party.cert.reminder_days`; seeded diversity list |
| SAP §13.1 FI-GL Customizing — OBB8 / OBB9 Terms of payment / installment terms (T052, T052R); register §13.99.2 #17 | PARTIAL-7 | ADDED as FR-7.15 | Installments via payment schedule lines; BR-7.13 rounding |
| SAP §13.2 FI-AP Master data — One-time vendor (account group CPD); register §13.99.2 #23 | PARTIAL-7 | ADDED as FR-7.16 | Document-level capture stored by waves 17/19 |
| SAP §13.2 FI-AP Customizing — OBB9 Installment terms (T052R); register §13.99.2 #35 | PARTIAL-7 | ADDED as FR-7.15 | Same schedule model for vendor terms |
| SAP §13.2 FI-AP Customizing — OBD3 "one-time account" (T077K); register §13.99.2 #37 | PARTIAL-7 | ADDED as FR-7.16 | Parameter `party.group.one_time_default` |
| SAP §13.3 FI-AR Customizing — OBB9 Installment terms (customers) (T052R); register §13.99.2 #47 | PARTIAL-7 | ADDED as FR-7.15 | AR due dates per installment in wave 30 |
| SAP §13.3 FI-AR Customizing — Define alternate payer / head office–branch (KNA1-KNRZE, KNB1); register §13.99.2 #50 | PARTIAL-7 | ADDED as FR-7.17 | `head_office_of` with decentralized flag; `alternate_payer`; posting effect in waves 19/30 |

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
| Related products | EcoResProductRelation, EcoResProductRelationType (Released product → Related products) | SD cross-selling via material determination (VB41/VB42/VB43), cross-selling profile | `ProductRelation`, `ProductRelationType` (FR-8.13) | full |
| Configurable lifecycle states | EcoResProductLifecycleState with enabled business processes (D365) | MARA-MSTAE / MARC-MMSTA with status control table T141 (OMS4) | `LifecycleState`, `LifecycleProcessPolicy` (FR-8.14) | full |
| Controlled group conversion | Change product dimension group / Change item model group (D365 periodic tasks), mass "Delete released products" | MM17 mass maintenance; MM06 flag for deletion; MMDE archive deletion | `group-change` and `obsolete` Temporal jobs (FR-8.15, FR-8.16) | improved (preconditions checked by callback) |
| Service master | EcoResProduct product type Service; procurement categories for services | AC01–AC06 service master (ASMD), service categories, standard service catalog | `Product` type service with `ServiceAttributes` (FR-8.17) | full |
| Variant tables and configuration templates | Table constraints (PCTableConstraint, user-defined/system-defined), configuration reuse | CU60–CU62 variant tables (CUVTAB/CUVTAB_VALC), CU7/CUCPT configuration templates | `DecisionTable`, `SavedConfiguration` (FR-8.18, FR-8.19) | full |
| Document info records | DocuRef, DocuType (Document management → Document types) | CV01N–CV04N document info record (DRAW, DRAT, DRAD), DC10 document types (TDWA) | `DocumentType`, `DocumentInfoRecord`, `DocumentOriginal`, `DocumentObjectLink` (FR-8.20, FR-8.21) | full |

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
- **FR-8.13 Product relations** — Should. The system shall maintain relation types (seeded: accessory, cross_sell, up_sell, substitute, spare_part, consumable, same_family; tenant-extensible) with a `symmetric` flag, and relations from a product or variant to another product or variant with sort order and `valid_from`/`valid_to`.
  - Maximum 200 relations per product; symmetric types create and delete the inverse in the same transaction.
  - Sales order entry (waves 26–27) reads `GET /product/v1/products/{id}/relations?type=` for suggestions; event `dev.passage.product.relation.changed.v1`.
- **FR-8.14 Configurable lifecycle states with process policies** — Must. The system shall let tenants define lifecycle states (code, name, category ∈ {draft, active, phase_out, obsolete} from FR-8.10, `default_for_new`, applies to product / released product / engineering version) and, per state, a policy of `allow`, `warn` or `block` for each business process: purchase_requisition, purchase_order, sales_quotation, sales_order, transfer_order, production_order, bom_line_use, master_planning, inventory_receipt, inventory_issue, counting, return_order.
  - `GET /product/v1/released-products/{id}/process-policy?process=` answers in < 10 ms p95; consumers cache the result and refresh on `releasedproduct.updated.v1`.
  - FR-8.10 boolean blocks stay as overrides (BR-8.12).
  - Seed mirrors D365 defaults: Prototype (draft: allows bom_line_use and planning only), Active, Phase out (blocks purchase_order and master_planning, warns on sales_order), Obsolete (blocks all except inventory_issue, counting and return_order).
  - Engineering versions (FR-9.14) carry their own state with the same policy model; the most restrictive of product, released-product and version policy applies.
- **FR-8.15 Mass obsolete and delete of released products** — Should. The system shall run a job that sets selected released products to an obsolete-category state (validated by BR-8.1) and deletes a released product only when every registered reference callback (inventory, purchasing, sales, production, engineering, pricing) reports zero references; otherwise the item is set obsolete and reported as "obsolete instead of deleted".
  - Max 10,000 items per run; a dry-run report precedes execution; deletion removes the released product only, never the shared product.
- **FR-8.16 Controlled change of dimension and model groups** — Should. The system shall change the item model group, storage dimension group or tracking dimension group of selected released products at an effective date through Temporal job `group-change-{id}`, after verifying through callbacks: on-hand = 0 (wave 16), no open purchase, sales, transfer or production orders (waves 16, 17, 21, 26), and inventory closed up to the effective date for costing-method changes (wave 20).
  - Before those waves exist the callbacks return "no references" and the change applies directly; each change writes an audit record and emits `releasedproduct.updated.v1` with `changed_fields`.
  - This job is the only path around BR-8.5 and FR-8.3 immutability.
- **FR-8.17 Service products** — Must. The system shall extend products of type `service` with: service category (code list with its own number sequence, e.g. `SVC-{cat}-{#####}`), service unit (hour, day, each, lump_sum, or any time-class unit), long purchasing text (≤ 4,000 characters, translatable), membership in standard service catalogs (category hierarchy role `service_catalog`), valuation class placeholder (bound in waves 11 and 20) and `requires_service_entry` flag read by purchasing (wave 17).
  - Service products never have storage or tracking dimension groups and never use a stocked item model group (BR-8.17).
- **FR-8.18 Configurator decision tables** — Should. The system shall maintain decision tables (columns = configuration attributes, rows = allowed value combinations; ≤ 50 columns, ≤ 100,000 rows) in a grid with CSV import/export, referenced from constraints as `table(name, attr1, …, attrN)` and pinned to the model version on activation.
  - LO-VC variant functions (ABAP function modules) are replaced by the sanctioned expression functions `round`, `min`, `max`, `abs`, `lookup`, `concat`, `substr`.
- **FR-8.19 Saved configurations and templates** — Should. The system shall save a solved configuration as a named `SavedConfiguration` (model id and version, selections, derived attribute values) at tenant scope or for a customer party, reuse it as the starting point of later solves, and re-validate saved configurations when a new model version activates; incompatible ones are flagged `stale` with the violated constraints.
- **FR-8.20 Document info records and document types** — Should. The system shall maintain document types (code, name, number sequence, allowed file types, max originals default 5 / max 20, version scheme numeric or alpha, part numbering on/off, `change_controlled` flag enforced by FR-9.25, status network id from FR-9.24, attribute set from FR-8.6, allowed object-link targets: product, released product, BOM version, route version, party, ECO) and document info records (DIR: document number, part `000`, version `00`, description, status, owner, originals with object-storage key, sha256 and size ≤ 200 MB, object links, attribute values).
  - Product images and documents from FR-8.11 become DIRs of system types `IMG` and `DOC`; the FR-8.11 endpoints are unchanged.
  - Endpoints: `POST /product/v1/documents`, `POST /product/v1/documents/{id}/versions`, `PUT /product/v1/documents/{id}/links`.
- **FR-8.21 Document search** — Should. The system shall search DIRs by number, description, type, status, attribute values, object link and full text of PDF, DOCX and TXT originals (text extracted at upload by a Python worker, ≤ 50 MB of text per original, Postgres `tsvector` with language-specific dictionaries), p95 < 300 ms at 2M DIRs.

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

**Addendum entities (FR-8.13 – FR-8.21)** — product_relation: id, type_code FK, from_product_id, to_product_id, sort_order int, valid_from, valid_to, unique (type_code, from_product_id, to_product_id). lifecycle_state: id, code text(20), name, category enum, default_for_new bool, applies_to text[]; lifecycle_process_policy: state_id, process enum, policy (allow, warn, block), PK (state_id, process). service_attributes: product_id PK/FK, service_category_code, service_uom_id, purchasing_text_id (translation), requires_service_entry bool, valuation_class text(10) null. decision_table: id, model_id, name, columns jsonb, row_count int, version int, checksum; rows in decision_table_row (table_id, row_no, values jsonb) with a GIN index. saved_configuration: id, model_id, model_version, scope (tenant, customer), party_id null, name, selections jsonb, status (valid, stale). document_type: code PK per tenant, settings jsonb; document_info_record: id, type_code, doc_number text(25), part char(3), doc_version text(2), description text(200), status_code, owner_user_id, unique (type_code, doc_number, part, doc_version); document_original: dir_id, seq, storage_key, sha256 bytea, size_bytes, mime, text_tsv tsvector; document_object_link: dir_id, target_type, target_id. group_change_job: id, change jsonb, effective_date, status, items jsonb.

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
- **BR-8.12** Process policy precedence: an FR-8.10 block flag set to true wins; otherwise the most restrictive of the released-product state, product state and (when versions are tracked) engineering-version state applies; `warn` returns success with `PRD-LC-WARN`, `block` returns `PRD-LC-BLOCKED` naming state and process.
- **BR-8.13** Each tenant state maps to exactly one category; transitions between states follow the BR-8.1 category machine; a state in use by any product or released product cannot be deleted; exactly one state per `applies_to` target is `default_for_new`.
- **BR-8.14** Symmetric relations are stored as two rows created and deleted together; self-relations are rejected (`PRD-REL-SELF`); a relation to an obsolete-category product is rejected for new relations and kept for existing ones.
- **BR-8.15** Group-change preconditions are evaluated at scheduling and again at the effective date; a product failing at the effective date is skipped with its reason code; no product is ever partially changed.
- **BR-8.16** A new DIR version copies object links and attribute values but not originals; at most one version per (type, number, part) may be in a status with the release flag at a time.
- **BR-8.17** Setting a storage or tracking dimension group, or a stocked item model group, on a service product is rejected (`PRD-SVC-STOCKED`).

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
- **AC-8.10** (FR-8.14, BR-8.12) Given state "Phase out" with purchase_order = block and sales_order = warn, when the process policy is queried for purchase_order, then `PRD-LC-BLOCKED`; for sales_order, then allowed with `PRD-LC-WARN`; with `blocked_sales = true`, then sales_order is blocked.
- **AC-8.11** (FR-8.13, BR-8.14) Given symmetric type `substitute`, when A→B is created, then B→A exists; when B→A is deleted, then A→B is gone; when A→A is created, then 422 `PRD-REL-SELF`.
- **AC-8.12** (FR-8.16, BR-8.15) Given 100 released products scheduled to move to model group STD effective 2026-12-01 and an on-hand callback returning 5 units for 3 of them on that date, when the job runs, then 97 are changed, 3 are skipped with `PRD-GRPCHG-ONHAND`, and 97 update events are published.
- **AC-8.13** (FR-8.20, FR-8.21) Given the 2M-DIR fixture, when searching attribute `drawing_size = A3` with text "torque", then p95 < 300 ms; when version 01 of DRW-0000123-000 is created, then it has the 3 object links of version 00 and 0 originals.
- **AC-8.14** (FR-8.18) Given a decision table of 10,000 allowed (frame, fork, wheel) rows, when a selection outside the table is solved, then the IIS names the table constraint and the response time is < 200 ms.
- **AC-8.15** (FR-8.17, BR-8.17) Given service product "Calibration – hourly" in category CAL, when a tracking dimension group is assigned, then 422 `PRD-SVC-STOCKED`; its number is `SVC-CAL-00001`.

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

### 1.8.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| `product.number_mode` | tenant | enum | sequence | sequence / manual / both | How product numbers are assigned (FR-8.1) | PIM parameters → Number sequences (Product number) | Material number ranges per material type (MMNR) |
| `product.number_pattern` | tenant | regex | `^[A-Z0-9][A-Z0-9._-]{0,39}$` | regex | Manual number validation | Product number format | OMSL material number format (TMCNM: length, leading zeros, lexicographic) |
| `product.variant_nomenclature` | tenant / master | template | `{master}-{size}-{color}-{style}-{config}` | tokens, separators, max 40 chars | Variant numbers (FR-8.2, BR-8.11) | Product nomenclature (EcoResProductNomenclature, D365) | Material variant number assignment |
| `product.max_variants` | tenant | int | 10,000 | 100–10,000 | Explosion cap | — | — |
| `product.explode_checkpoint` | tenant | int | 500 | 100–2,000 | Checkpoint size | — | — |
| `product.default_dimension_groups` | tenant / item group | FK × 3 | none | groups | Pre-fill storage/tracking groups at create | PIM parameters → default dimension groups | Material type defaults (OMS2), batch management default |
| `product.release_profile` | legal entity × item group | FK | none | release profiles | Defaults at release (FR-8.4) | Default order settings templates / released product templates (D365) | Reference material on MM01 / material type views (T134) |
| `product.lifecycle.default_state` | tenant × target | FK | Prototype (product), Active (released) | lifecycle states | State on create (FR-8.14) | Product lifecycle states → Default for new products | MARA-MSTAE default |
| `product.lifecycle.process_policy` | state × process | enum | seeded per FR-8.14 | allow / warn / block | Process enablement | Product lifecycle state → enabled business processes | OMS4 material status (T141: purchasing, BOM, routing, MRP, inventory messages) |
| `product.gs1.company_prefix` | tenant | digits | none | 6–12 digits | GTIN allocation (FR-8.9) | GTIN setup / Bar code setup | EAN categories (T006M) and EAN number ranges (W4ES) |
| `product.barcode.symbologies` | tenant | set | EAN-13, GS1-128, DataMatrix | + UPC-A, ITF-14, Code 128, Code 39, QR | Rendering choices | Bar code setup (BarcodeSetup) | — |
| `product.cfg.time_limit_ms` | tenant | int | 2,000 | 200–10,000 | Solver limit (BR-8.8) | Product configuration parameters (solver strategy) | Configuration profile settings (CUCO) |
| `product.media.limits` | tenant | size / count | 10 MB, 20 per product | 1–50 MB, 1–100 | FR-8.11 limits | Product image settings | DC10 originals per document |
| `product.translation.fallback` | tenant | BCP 47 | tenant base language | languages | BR-8.10 | Product translations | MAKT fallback to logon language |
| `product.attr.required_enforcement` | tenant | enum | on_activate | on_create / on_activate / off | BR-8.9 | Category attribute "Mandatory" | CT04 "entry required" |
| `product.doc.types` | tenant | records | IMG, DOC, DRW, SPC | FR-8.20 fields | Document behavior | Document types (DocuType) | DC10 (TDWA, TDWO, TDWP) |
| `product.service.category_sequence` | service category | sequence | `SVC-{cat}-{#####}` | sequences | Service numbers (FR-8.17) | — | Service master number ranges and service categories (IMG MM-SRV) |
| `product.relation.types` | tenant | list | 7 seeded types | + custom | FR-8.13 | Product relation types (EcoResProductRelationType) | Cross-selling profiles (SD material determination) |

### 1.8.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| All products and masters | list inquiry | subtype, type, state, category, attribute | number, name, subtype, dimension groups, state | `product.product` | EcoResProductListPage | MM60 material list, MM03 |
| Released products per legal entity | list inquiry | legal entity, item group, model group, state, blocks | item number, groups, units, site, warehouse, state | `product.released_product` | Released products; SSRS Item base data | MM60 by plant |
| Variants of a master | list inquiry | master, dimension values | variant number, dimension values, released entities | `product.product` (variant) | EcoResProductVariantListPage | MM60 filtered by SATNR |
| Incomplete products | KPI | category, legal entity | missing attributes, missing defaults, counts by category | completeness projection | Incomplete attribute report (nightly) | MM50 list of extendable materials |
| Lifecycle and process policy | KPI | state, process | products per state, blocked processes | `released_product` × policy | Product lifecycle state inquiry (D365) | MM60 by MSTAE/MMSTA |
| GTIN register | list inquiry | prefix, type, unit | GTIN, product, unit, primary, check digit | `product.product_identifier` | Bar codes / GTIN codes | MEAN / MARM-EAN11 list |
| Attribute search | drill-down | attribute, value range, category | product, attribute values | attribute value projection | Product attributes inquiry | CL30N, CT06 |
| Configuration models and solves | drill-down | model, version, date range | solves, feasible %, p95 time, IIS frequency | solve log | Configuration inquiries; SSRS Product configuration model | CU50 simulation, CUTRACE |
| Item model and dimension group usage | list inquiry | group | products per group, costing method, flags | `product.item_model_group` | SSRS Item model group / Dimension group listings | OMS2, T134 overview |
| Document search | list inquiry | type, status, attribute, link, text | number, part, version, description, status, links | `product.document_info_record` | Document handling (DocuView) | CV04N |
| Product relations | list inquiry | type, product | from, to, type, validity | `product.product_relation` | Related products | VB43 material determination display |
| Group change and obsolete job history | drill-down | job, date | items changed, skipped (reason), deleted, obsolete | job projection | Periodic task history | MM17 log |

### 1.8.15 Printed and electronic documents

| Document | Trigger | Layout elements (header/line/footer fields) | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Product data sheet | on demand | header: product number, name, primary image; lines: attributes, units, conversions, GTINs; footer: legal entity, date, translation language | PDF, email | none | none |
| Product barcode label | on demand or job PRD-J4 | GTIN, human-readable interpretation, description, unit, optional batch/expiry AIs for GS1-128 | PDF, ZPL (printer routing in wave 31) | none | GS1 General Specifications: symbol X-dimension, quiet zones, HRI |
| Product catalog export | on demand per catalog | header: catalog, validity, supplier; lines: product, description, unit, GTIN, attributes, price placeholder | BMEcat 2005 XML, CSV | `CAT-{yyyy}-{####}` | none |
| Configuration summary | solve saved as configuration | header: model, version, customer; lines: attribute selections; footer: constraint check result | PDF | saved configuration id | retained with the sales document (wave 27) |
| GDSN item publication | none in this wave | — | — | — | GDSN data-pool synchronization deferred to wave 35 integration hub |

### 1.8.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| PRD-J1 Variant explosion | on demand | master_id | 500-row checkpoints; existing combinations skipped (BR-8.3) | 1,000 variants < 5 s; 10,000 < 60 s | resume from checkpoint | job failed |
| PRD-J2 Incomplete-attribute report | nightly 03:00 | category_id | read-only | < 10 min at 1M products | retried next night | none |
| PRD-J3 Orphan media sweep | weekly Saturday 02:00 | storage prefix | marks then deletes after 7 days | < 30 min | stops on storage errors | bucket errors |
| PRD-J4 Barcode batch generation | on demand / nightly for new GTINs | product_id range | output keyed by (GTIN, symbology) | 10,000 labels < 5 min | per-item error list | failures > 1% |
| PRD-J5 Group change conversion | at effective date (Temporal timer) | released_product_id | per-item idempotent; BR-8.15 | 1,000 items < 2 min | item skipped with reason | skipped > 0 |
| PRD-J6 Mass obsolete / delete | on demand after dry run | legal_entity_id | per-item; delete only with zero references | 10,000 items < 10 min | item set obsolete instead | none |
| PRD-J7 Document text extraction | on upload (queue) | original id | content hash | < 20 s per 10 MB PDF | retried 3×; DIR searchable by metadata | queue depth > 500 |
| PRD-J8 Saved-configuration revalidation | on model activation | model_id | stale flag recomputed | 10,000 configurations < 2 min | per-item error | stale count |

### 1.8.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles that include it |
|---|---|---|---|
| View products | `GET /product/v1/**`; PIM workspace | none | every business role |
| Maintain products | product create/edit, variants, attributes, relations, identifiers | none | Product manager, Master data steward |
| Release products | `:release`, release profiles | none | Product manager, Master data steward |
| Maintain released products | released-product edits, site defaults, blocks | none | Inventory manager, Purchasing manager |
| Maintain product setup | item groups, model groups, dimension groups, lifecycle states and policies | Post inventory adjustments (wave 16) | Product manager lead, Cost accountant |
| Run product group conversions | PRD-J5, PRD-J6 | Maintain product setup (maker-checker on costing-method change) | Cost accountant |
| Configure products | configuration models, decision tables, `:solve` test | none | Product configurator modeler |
| Maintain product categories | category hierarchies, attribute sets | none | Category manager |
| Maintain documents | DIR create, versions, originals, links | Release engineering documents (FR-9.24) on the same DIR | Engineer, Document controller |
| Allocate GTINs | `identifiers?allocate=true`, prefix settings | none | Master data steward |

### 1.8.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| PRD-NUM-FORMAT | error | "Product number {n} does not match the pattern." | Regex failure | Correct number | FR-8.1 |
| PRD-NUM-IMMUTABLE | error | "Product number cannot change after release." | Edit after release | Create a new product | FR-8.1 |
| PRD-VAR-CAP | error | "Explosion would create {n} variants; limit is {max}." | Cartesian product above cap | Reduce values | FR-8.2 |
| PRD-VAR-DIMVAL-UNKNOWN | error | "{value} is not a value of dimension {dim}." | Unknown dimension value | Add the value first | FR-8.2 |
| PRD-RELEASE-DRAFT | error | "Draft products cannot be released." | Release of draft | Activate first | FR-8.4 |
| PRD-RELEASE-DEFAULT-MISSING | error | "Missing {field} for {entity}/{site}." | No profile or site default | Add the default | FR-8.4 |
| PRD-DIMGRP-LOCKED | error | "Dimension group is locked by inventory transactions." | Change after transactions | Use the group-change job | FR-8.3, BR-8.5 |
| PRD-UOM-UNDERFLOW | error | "Conversion rounds {qty} to zero." | Precision too low | Increase precision | BR-8.6 |
| PRD-GTIN-CHECKDIGIT | error | "GTIN {v} has an invalid check digit." | mod-10 failure | Correct GTIN | BR-8.7 |
| PRD-GTIN-DUPLICATE | error | "GTIN {v} already belongs to {product}." | Duplicate | Use another GTIN | BR-8.7 |
| PRD-CFG-TIMEOUT | error | "Configuration not solved within {ms} ms." | Solver limit | Simplify or retry | BR-8.8 |
| PRD-CFG-INFEASIBLE | error | "Selections conflict: {constraints}." | IIS found | Change selections | FR-8.8 |
| PRD-ATTR-REQUIRED | error | "Required attributes missing: {list}." | Activation incomplete | Fill attributes | BR-8.9 |
| PRD-ATTR-RANGE | error | "{attr} must be between {min} and {max}." | Out of range | Correct value | FR-8.6 |
| PRD-LC-BLOCKED | error | "{process} is blocked for state {state}." | Policy block | Change state or use another product | BR-8.12 |
| PRD-LC-WARN | warning | "{process} is discouraged for state {state}." | Policy warn | Confirm | BR-8.12 |
| PRD-REL-SELF | error | "A product cannot be related to itself." | Self-relation | Choose another product | BR-8.14 |
| PRD-GRPCHG-ONHAND | warning | "Skipped: on-hand {qty} on {date}." | Precondition failed | Empty stock, reschedule | BR-8.15 |
| PRD-SVC-STOCKED | error | "Service products cannot be stocked." | Stocking setup on service | Remove group | BR-8.17 |
| PRD-DOC-ORIGINAL-LIMIT | error | "Document type {t} allows {n} originals." | Too many files | Remove a file or new version | FR-8.20 |

### 1.8.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| Product read | 3M/day; peak 5,000/min | 8 / 50 / 90 ms | 500 req/s per replica | ~3 KB per product + attributes | PK; (tenant_id, product_number) unique | read replicas (CNPG) absorb load |
| Variant lookup | 10M/day from order lines | 2 / 10 / 20 ms | 2,000 req/s | none | unique (master_id, dimension hash) | unchanged |
| Release 1,000 products × 3 entities | 20/day | 25 / 60 / 90 s | 2 concurrent per tenant | ~2 KB per released product | (legal_entity_id, item_number) unique | queued; no timeout |
| Process-policy query | 20M/day | 1 / 10 / 20 ms | 5,000 req/s (cached) | none | in-memory policy cache | stale cache ≤ 60 s |
| Typeahead search | 1M/day | 25 / 100 / 180 ms at 1M | 300 req/s | trigram ~1 GB at 1M | GIN trigram on search_name, GTIN | results capped at 10 |
| Configurator solve | 20k/day; peak 100/min | 60 / 200 / 500 ms (50 constraints) | 20/s per worker | solve log ~1 KB | worker queue | HPA to 6 workers; queue > 100 alerts |
| Document search | 50k/day | 80 / 300 / 600 ms at 2M DIRs | 50 req/s | originals 2–5 TB/yr in object storage; tsvector ~5% | GIN tsvector; attribute jsonb GIN | full-text limited to 1,000 hits |

### 1.8.20 Localization and compliance hooks

| Item | What varies | Country packs touching it (wave 48) | Extension point |
|---|---|---|---|
| Commodity and tax classification | HS/CN8 for Intrastat (DE, FR); HSN/SAC codes mandatory on IN GST invoices (6 digits above INR 5 crore turnover); SAT c_ClaveProdServ and c_ClaveUnidad on MX CFDI 4.0; UNSPSC optional | DE, FR, IN, MX | attribute sets supplied by pack; `ProductTaxClassificationHook` read by wave 13 |
| Units and labeling | US FPLA dual metric/US customary net quantity; CA Consumer Packaging and Labelling Act bilingual; EU Directive 76/211/EEC ℮ mark | US, CA, DE, FR | unit display rules; translation requirements per country |
| GS1 prefixes | Prefix issued by GS1 US, UK, Germany, France, India, Mexico, Canada | all | prefix per legal entity allowed |
| Product names and descriptions | FR/CA-QC French-language requirement; MX Spanish CFDI description | FR, CA, MX | FR-8.12 translations; BR-8.10 fallback |
| Regulated attributes | Hazardous substance data (REACH, GHS), export control classification (ECCN/EAR, EU 2021/821), RoHS | all | attribute sets contributed by wave 48 compliance packs |
| Document retention | Technical documentation kept 10 years after last unit placed on market (EU MDR Art. 10(8), Machinery Regulation 2023/1230) | DE, FR | retention class per document type (FR-8.20) |

### 1.8.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX §12.15.1 PIM, Common — Released product → Related products, EcoResProductRelation; register §12.99.4 #141 | PARTIAL-8 | ADDED as FR-8.13 | Symmetric types, sales suggestions read in waves 26–27 |
| AX §12.15.1 PIM, Common — Released product → Product lifecycle state (D365), EcoResProductLifecycleState; register §12.99.4 #143 | PARTIAL-8 | ADDED as FR-8.14 | Per-process allow/warn/block |
| AX §12.15.3 PIM, Periodic — Change product dimension groups / Change item model group; register §12.99.4 #146 | PARTIAL-8 | ADDED as FR-8.16 | Callback-checked conversion job PRD-J5 |
| AX §12.15.3 PIM, Periodic — Delete released products / Mark obsolete; register §12.99.4 #147 | PARTIAL-8 | ADDED as FR-8.15 | Job PRD-J6 with reference callbacks |
| AX §12.15.4 PIM, Setup — Product lifecycle states (D365); register §12.99.4 #148 | PARTIAL-8 | ADDED as FR-8.14 | Parameters `product.lifecycle.*` |
| AX §12.16.4 Procurement, Setup — Categories → category vendor assignment, category questionnaires; register §12.99.4 #162 | PARTIAL-8 | DEFERRED to wave 17 | Category hierarchies exist here (FR-8.6); vendor-per-category assignment and questionnaires belong to sourcing in wave 17, as the register proposes |
| AX §12.34.1 ECM, Common — Product lifecycle states (per version, controlling process enablement); register §12.99.4 #283 | PARTIAL-8 | ADDED as FR-8.14 | Engineering-version states applied by FR-9.14 |
| AX §12.34.4 ECM, Setup — Product lifecycle states (enabled business processes per state, default states); register §12.99.4 #297 | PARTIAL-8 | ADDED as FR-8.14 | `default_for_new` per target |
| SAP §13.15 MM-SRV Master data — AC01/AC02/AC03/AC04/AC06 service master; register §13.99.2 #163 | PARTIAL-8 | ADDED as FR-8.17 | Service categories, purchasing text, service catalog |
| SAP §13.15 MM-SRV Customizing — Service master number ranges, service categories, organizational status, field selection (ASMD); register §13.99.2 #181 | PARTIAL-8 | ADDED as FR-8.17 | Number range per category; field selection via wave-3 metadata field rules |
| SAP §13.32 LO-VC Master data — CU60/CU61/CU62/CU65/CU66/CU67 variant tables and functions; register §13.99.2 #356 | PARTIAL-8 | ADDED as FR-8.18 | Table grid added; ABAP variant functions replaced by expression functions |
| SAP §13.32 LO-VC Master data — CU7 / CUCPT configuration templates; register §13.99.2 #359 | PARTIAL-8 | ADDED as FR-8.19 | Tenant and customer scope, stale detection |
| SAP §13.35 DMS Master data — CV01N/CV02N/CV03N document info record; register §13.99.2 #374 | PARTIAL-8 | ADDED as FR-8.20 | Status network, ECO versioning and signatures in FR-9.24/FR-9.25 |
| SAP §13.35 DMS Master data — CV04N find document; register §13.99.2 #375 | PARTIAL-8 | ADDED as FR-8.21 | Attribute, link and full-text search |
| SAP §13.35 DMS Master data — Fiori Manage Documents / Manage Document Info Records; register §13.99.2 #384 | PARTIAL-8 | ADDED as FR-8.20 | Document screens in the PIM and engineering workspaces |
| SAP §13.35 DMS Reports — CV04N document search; register §13.99.2 #385 | PARTIAL-8 | ADDED as FR-8.21 | "Document search" in §1.8.14 |
| SAP §13.35 DMS Customizing — DC10 document types (TDWA, TDWS, TDWO, TDWP); register §13.99.2 #392 | PARTIAL-8 | ADDED as FR-8.20 | Parameter `product.doc.types` |

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
| Engineering organization and release product structure | EngChgEngineeringOrganization, EngChgReleaseProductStructure, product release policies, Open product releases (acceptance) | ALE distribution of material and BOM (BD10, BD30, distribution model BD64, status BD87); MDG-M replication | `EngineeringOrganization` flag, `ReleasePolicy`, `ReleaseSession` / `ReleaseLine` (FR-9.14 – FR-9.16) | improved (one workflow with per-line status) |
| Product readiness | Product readiness policies and checks (D365 ECM) | No direct equivalent (MM50 extension lists, material status) | `ReadinessPolicy`, `ReadinessCheckResult` (FR-9.17) | improved |
| Engineering categories and version nomenclature | EngChgEngineeringProductCategory, version nomenclature | Revision levels (OS13 material revision levels, CC01 revision) | `EngineeringCategory`, `VersionNomenclature` (FR-9.18) | full |
| BOM usage and category | BOMTable (production BOM vs formula), no usage concept | STLAN usage (OS20), STLTY category M/D/E/T/K/P | `BomHeader.usage`, `BomHeader.category` (FR-9.19) | partial (order, project, equipment BOM instances in waves 27, 41, 44) |
| Alternative items and discontinuation | Plan material substitution (D365 planned-order substitution), BOM line substitution | STPO-ALPGR alternative item group with ALPST strategy / ALPRF priority / EWAHR probability; discontinuation STPO-KZAUS / NFMAT follow-up material | `BomLine.alt_*`, `BomLine.discontinuation_*` (FR-9.20) | full (MRP logic in wave 22) |
| Serial and parameter effectivity | Date effectivity only (BOMVersion/BOM FromDate/ToDate) | ECM effectivity types: date, serial number, parameter (IMG ECM → Effectivity) | `Effectivity` record on ECO, version and line (FR-9.21) | improved over AX, full vs SAP |
| Super BOM and super routing | Constraint-based configuration BOM line and route operation templates (PCBOMLineTemplate, PCRouteOperationTemplate) | CS40–CS42 super BOM with selection conditions (CU01), CA01 operation dependencies | `selection_condition` and `qty_expr` on BomLine / Operation (FR-9.22) | full |
| Engineering documents | DocuRef attached to engineering versions | DIR status network (DC20), document BOM item category D (CS01), document structures (CV11–CV15), ECM-controlled versions | DIR status network, signatures, ECO-controlled versions, document lines, structures (FR-9.23 – FR-9.26) | full |
| CAD / PLM engineering BOM synchronization | PLM integration via Dataverse (Teamcenter, Windchill partner connectors) | ECTR Engineering Control Center (CAD BOM transfer, CAD desktop CDESK) | eBOM import contract, comparison report, polling job (FR-9.27) | partial (vendor connectors in wave 35) |

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
- **FR-9.14 Engineering organization and engineering products** — Must. The system shall let a legal entity be flagged `engineering_organization` (one or more per tenant); products whose engineering category (FR-9.18) has `track_versions = true` are engineering products owned by one engineering organization, and their engineering versions (version code from the nomenclature, effective range, lifecycle state from FR-8.14, attached BOM and route versions) are maintained only in that organization.
  - Operational legal entities receive products, BOMs and routes by release (FR-9.15) and cannot edit engineering-owned fields (`ENG-OWNED-FIELD`); local fields (site defaults, local BOM site) stay editable.
  - When the category sets `version_as_dimension = true`, the engineering version is an additional product dimension `version` so that inventory (wave 16) and orders track it (D365 "version" product dimension).
- **FR-9.15 Release policies and release product structure** — Must. The system shall release engineering products to operational legal entities through `ReleaseSession` workflows (Temporal `release-{id}`), created manually, by job ENG-J7, or automatically on ECO release, governed by a release policy that lists what is copied: product, released-product template (FR-8.4 profile), BOM versions (copy yes/no, activate yes/no), route versions (copy yes/no, activate yes/no), attributes, default order settings, document links (FR-8.20).
  - Each (product, entity) line moves through BR-9.13; resubmission creates a new line; a site-mapping table per receiving entity maps engineering sites to operational sites.
  - Releasing a 1,000-component structure to 3 entities completes in < 2 min.
- **FR-9.16 Product acceptance** — Should. The system shall let a receiving entity whose policy sets `require_acceptance = true` review release lines in "Open product releases", adjust site defaults and BOM/route site, and accept or reject with reason; accepted lines create or update the released product and activate copied versions; entities without acceptance accept automatically.
- **FR-9.17 Product readiness policies and checks** — Must. The system shall evaluate readiness policies (per engineering category, item group or release policy) made of automated checks — item model group set, storage/tracking groups set, default order settings per site, active BOM for manufactured items, active route, tax group (wave 13 hook), coverage group (wave 22 hook), purchase price agreement (wave 17 hook), sales price (wave 26 hook), warehouse setup (wave 31 hook) — and manual checks (text, assigned user or team from FR-6.14, due in N days, mandatory flag).
  - A released product stays in a state with category draft named `readiness_pending` (blocks transactional processes via FR-8.14) until every mandatory check is passed or not applicable, then moves to the policy's target state (default Active).
  - Hooks of waves not yet live return `not_applicable`; checks re-evaluate on relevant events and by job ENG-J9.
- **FR-9.18 Engineering product categories and version nomenclature** — Must. The system shall maintain engineering categories (code, name, product type, `track_versions`, `version_as_dimension`, nomenclature, default readiness policy, default release policy, attribute set, `change_controlled` = ECO mandatory) and version nomenclatures made of ordered segments: number (width, start), letter (A–Z, option to skip I and O), constant text, date (`yyyyMMdd` or `yyMM`), with separators; e.g. `V-{NN}` → V-01, V-02; `{A}` → A … Z, AA.
- **FR-9.19 BOM usage and category** — Must. The system shall carry `usage` ∈ {production, engineering, costing, maintenance, sales, universal} (SAP STLAN 1–6) and `category` ∈ {material, document, equipment, functional_location, sales_order, project} on BOM headers; this wave maintains material and document BOMs, while equipment/functional-location (wave 44), sales-order (wave 27) and project/WBS (wave 41) BOMs reuse the same structures with an owner reference and the same explosion API.
  - `bom:resolve` and `bom:explode` accept `usage` with a configurable fallback order (default production → universal).
- **FR-9.20 Alternative item groups and discontinuation** — Should. The system shall store on BOM lines an alternative group (`alt_group` 2 chars, `alt_priority` 1–99, `alt_strategy` priority or usage_probability, `usage_probability_pct`) and discontinuation data (`discontinuation_group`, `discontinue_type` single or simultaneous, `follow_up_group`, `follow_up_component_id`, `discontinue_on` date); explosion returns groups unresolved with this metadata and planning (wave 22) resolves them against availability.
- **FR-9.21 Serial and parameter effectivity** — Should. The system shall support effectivity type date (default), serial (end-item product plus serial range from–to) and parameter (tenant-defined parameters such as `model_year`, `customer_party_id`, `order_type`, with values or ranges) on ECOs and on the versions and lines they release; `bom:resolve` and `bom:explode` accept `effectivity_context` (e.g. `{"serial":"SN100045","parameters":{"model_year":2027}}`).
  - Without context, date effectivity applies and serial/parameter-conditioned lines are excluded unless `include_unconditional = true`.
  - Maximum 20 effectivity parameters per tenant; serial ranges compare lexicographically after zero-padding to the product's serial mask length.
- **FR-9.22 Configurable (super) BOM and routing** — Should. The system shall let BOM lines and route operations carry a `selection_condition` in the FR-8.8 expression grammar over the parent master's configuration attributes and an optional `qty_expr` (e.g. `length_m * 2`); exploding with a solved configuration includes only lines whose condition is true and evaluates quantity expressions; activation validates conditions against the active model version (`ENG-COND-UNKNOWN-ATTR`).
- **FR-9.23 Document and text BOM lines** — Should. The system shall add BOM line types `document` (references a DIR number/part/version, no consumption, printed on production papers in wave 21) and `text` (≤ 2,000 characters, translatable); planning and costing explosion modes skip both, `mode=full` includes them.
- **FR-9.24 Document status network and release with electronic signature** — Should. The system shall let document types (FR-8.20) define a status network (statuses with release flag, allowed transitions, required duty, signature required, meaning text); default network `in_work → in_review → released → obsolete` plus `in_work → cancelled`; a signature transition requires Keycloak step-up re-authentication (password or WebAuthn) and records signer, printed name, timestamp, meaning and a hash of all originals, satisfying 21 CFR 11.50 (manifestation) and 11.70 (signature/record linking).
- **FR-9.25 Document versions under ECO** — Should. For document types with `change_controlled = true`, the system shall release a new DIR version only as an affected object of an ECO; ECO release sets the new version to the released status and the previous one to obsolete in the same compensated transaction as BOM/route versions (BR-9.9); ECO impact analysis lists documents linked to the affected products.
- **FR-9.26 Document structures and where-used** — Could. The system shall let a DIR carry a document structure (ordered child DIRs with optional item text, versioned with the parent) and answer where-used for a document across parent documents and BOM document lines (`GET /engineering/v1/documents/{id}:where-used`); cycles are rejected (`ENG-DOC-CYCLE`).
- **FR-9.27 CAD/PLM engineering BOM synchronization contract** — Should. The system shall accept engineering BOM payloads (`ebom.published`, JSON schema in the registry: header part number and revision; lines with find number, component part number, quantity, unit, reference designators, file URIs) through the import tool or `POST /engineering/v1/ebom-imports`, resolve part numbers via product number or `ProductIdentifier` type `cad_part`, create a draft BOM version plus an ECR, and produce an eBOM-versus-active-mBOM comparison (added, removed, changed lines).
  - Source configuration: application name (SolidWorks PDM, Inventor Vault, Teamcenter, Windchill, other), transport (SFTP or REST pull), mapping rules (unit codes, find number → line_no, phantom attribute), auto-ECR on/off; job ENG-J8 polls every 15 min.
  - Native CAD file management and 3D viewing are not provided; vendor-specific connectors ship in wave 35.

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

**Addendum entities (FR-9.14 – FR-9.27)** — engineering_category: id, code text(20), name, product_type, track_versions bool, version_as_dimension bool, nomenclature_id FK, readiness_policy_id FK null, release_policy_id FK null, attribute_set_id FK null, change_controlled bool. version_nomenclature: id, code, segments jsonb [{type: number|letter|constant|date, width, start, skip_io, format, text}], separator text(3). engineering_version: id, product_id, version_code text(20), effective_from, effective_to, lifecycle_state_id, engineering_le_id, unique (product_id, version_code). release_policy: id, code, copy_bom bool, activate_bom bool, copy_route bool, activate_route bool, copy_attributes bool, copy_order_settings bool, copy_documents bool, require_acceptance jsonb {legal_entity_id: bool}; release_session: id, number `REL-{yyyy}-{#####}`, created_by, source (manual, eco, job), workflow_id; release_line: session_id, product_id, engineering_version_id, legal_entity_id, status (pending, sent, accepted, rejected, failed), reason, accepted_by. readiness_policy: id, code, checks jsonb [{key, kind auto|manual, mandatory, assignee, due_days}], target_state_id, continuous bool; readiness_check_result: released_product_id, check_key, status (pending, passed, failed, not_applicable), evaluated_at, signed_by. effectivity: id, type (date, serial, parameter), product_id null, serial_from, serial_to, parameters jsonb; referenced by eco, bom_version and bom_line via effectivity_id. bom_header gains usage, category, owner_ref; bom_line gains alt_group, alt_priority, alt_strategy, usage_probability_pct, discontinuation_group, discontinue_type, follow_up_group, follow_up_component_id, discontinue_on, selection_condition text, qty_expr text, document_ref (dir_id), text_id. document_status_network: id, statuses jsonb, transitions jsonb; document_signature: dir_id, version, transition, signer_id, meaning, signed_at, originals_hash. ebom_import: id, source_code, part_number, revision, payload jsonb, status, bom_version_id, ecr_id, unique (source_code, part_number, revision).

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
- **BR-9.13** Release line machine: `pending → sent → accepted | rejected`; `sent → failed` after 5 retries; `rejected` and `failed` are terminal for the line and resubmission creates a new line; an ECO release triggers release lines only for entities that previously accepted the product.
- **BR-9.14** A released product is ready when every mandatory check is `passed` or `not_applicable`; a passed automated check that later fails (e.g. the BOM expired) reverts the product to `readiness_pending` only when the policy sets `continuous = true` (default false); manual checks cannot be signed by the user who created the release session.
- **BR-9.15** Nomenclature increments: a number segment that overflows its width is rejected (`ENG-NOMEN-OVERFLOW`); letters roll Z → AA; date segments use the release date in the engineering organization's time zone; a version code is never reused for the same product.
- **BR-9.16** Alternative groups: with `usage_probability`, probabilities in a group sum to exactly 100; with `priority`, priorities are unique in the group; a line may not be both an alternative-group member and a follow-up component in the same group (`ENG-ALT-CONFLICT`).
- **BR-9.17** Effectivity precedence at resolution is serial > parameter > date; two serial-effective versions with overlapping ranges for the same (product, site) are rejected at ECO approval (`ENG-EFF-OVERLAP`).
- **BR-9.18** A signature binds DIR id, version, transition and the sha256 of all originals; changing originals after signature is refused (`ENG-DOC-SIGNED`); where the type requires independent review the signer cannot be the last editor.
- **BR-9.19** eBOM import is idempotent on (source, part number, revision): re-import updates the draft while it is still draft; once released, the same revision is rejected (`ENG-EBOM-REVISION-RELEASED`).

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
- **AC-9.11** (FR-9.15, FR-9.16, BR-9.13) Given engineering organization ENG1 and a policy that copies and activates BOM and route, requiring acceptance in USMF and not in DEMF, when product P version V-02 is released to both, then the DEMF line is `accepted` with active BOM and route on the mapped DEMF site and the USMF line waits in Open product releases; after acceptance, USMF has the released product and both events are published.
- **AC-9.12** (FR-9.17, BR-9.14) Given a readiness policy with 3 automated checks and 1 mandatory manual check, when P is released to USMF, then the released product is `readiness_pending` and a sales-order policy query returns `PRD-LC-BLOCKED`; after the manual check is signed and the BOM is active, the state becomes Active.
- **AC-9.13** (FR-9.18, BR-9.15) Given nomenclature `V-{NN}` with last version V-99, when the next version is requested, then `ENG-NOMEN-OVERFLOW`; given `{A}` with last Z, then AA.
- **AC-9.14** (FR-9.21, BR-9.17) Given an ECO with serial effectivity SN1000–SN1999 replacing line 30, when exploding with serial SN1500, then the new component is returned; with SN2000, the old component.
- **AC-9.15** (FR-9.22) Given a super BOM line with `frame == "carbon"` and a wheel line with `qty_expr = wheel_count`, when exploded with frame = carbon and wheel_count = 2, then only the carbon frame line appears and wheel quantity is 2.
- **AC-9.16** (FR-9.24, FR-9.25, BR-9.18) Given change-controlled type DRW with signature on release, when version 02 is released outside an ECO, then 409 `ENG-DOC-ECO-REQUIRED`; through an ECO, a signature record is stored, version 02 is released and version 01 obsolete.
- **AC-9.17** (FR-9.27, BR-9.19) Given an eBOM of 120 lines with 5 unknown part numbers, when imported, then a draft BOM version with 115 lines and an ECR exist, 5 lines are exceptions `ENG-EBOM-PART-UNKNOWN`, and the comparison counts equal the fixture.
- **AC-9.18** (FR-9.20, BR-9.16) Given alternative group A1 with probabilities 60/40, when exploded, then both lines return with group metadata; when probabilities are saved as 60/30, then 422 `ENG-ALT-CONFLICT`.

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

### 1.9.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| `eng.bom.approval_required` | tenant / site | bool | true | true / false | Activation requires approval (BR-9.1) | Inventory and warehouse management parameters → Bills of materials → BOM approval check | OS28 BOM modification parameters |
| `eng.direct_activation` | tenant | bool | false in production tenants | true / false | Activation outside ECO (feature flag) | — (in-place BOM edits) | OS28 "ECM mandatory" setting |
| `eng.bom.max_depth` | tenant | int | 25 | 5–25 | BR-9.6 | BOM levels setting | CS12 explosion level limit |
| `eng.bom.max_lines` | tenant | int | 5,000 | 100–10,000 | FR-9.2 | — | — |
| `eng.bom.usage_fallback` | tenant | list | production → universal | ordered usages | FR-9.19 resolution | — | OPJI/OPJM selection ID, OS20 usages |
| `eng.bom.scrap_default` | item group | percent | 0 | 0–100 | Default variable scrap on new lines | BOM line ScrapVar default | OS27 default values, MARC-AUSSS assembly scrap |
| `eng.route.time_unit` | tenant | enum | hours (4 dec) | hours / minutes | Operation time storage and display | Production control parameters → time unit | Standard value keys (OP19) |
| `eng.route.default_link_type` | tenant | enum | soft | hard / soft | Default operation relation | RouteOpr.LinkType | PLAS relationship type |
| `eng.cal.horizon_months` | resource group | int | 36 | 1–60 | Calendar generation window (FR-9.6) | Compose working times period | Factory calendar validity (SCAL) |
| `eng.cal.expiry_warning_days` | tenant | int | 60 | 7–365 | Job ENG-J4 warning | Update calendars | SCAL valid-to |
| `eng.eco.number_sequence` | tenant | sequence | `ECO-{######}` | sequences | ECO numbers | ECM parameters → Number sequences | Change number number-range object (CC01) |
| `eng.eco.max_horizon_days` | tenant | int | 365 | 30–1,095 | BR-9.10 | — | — |
| `eng.effectivity.types` | tenant | set | {date} | date, serial, parameter | FR-9.21 | — | IMG ECM → Effectivity types and parameters |
| `eng.org.engineering` | legal entity | bool | false | true / false | FR-9.14 | ECM parameters → Engineering organization | — |
| `eng.release.default_policy` | engineering category | FK | none | release policies | FR-9.15 | Product release policies | ALE distribution model (BD64) |
| `eng.release.require_acceptance` | release policy × legal entity | bool | false | true / false | FR-9.16 | Release policy "require acceptance" | — |
| `eng.readiness.policy` | engineering category / item group | FK | none | readiness policies | FR-9.17 | Product readiness policies | — |
| `eng.nomenclature` | engineering category | FK | `V-{NN}` | nomenclatures | FR-9.18 | Version nomenclature | Revision levels (OS13) |
| `eng.doc.signature_meanings` | document status | text | "Approved for release" | ≤ 100 chars | FR-9.24 | Electronic signature requirements (D365) | Digital signature strategy on DC20 status |
| `eng.ebom.sources` | tenant | records | none | SFTP / REST source definitions | FR-9.27 | PLM integration (Dataverse) | ECTR configuration |
| `eng.explode.cache_ttl_s` | tenant | int | 300 | 0–3,600 | Explosion cache | — | — |

### 1.9.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| BOM versions per product | list inquiry | product, site, status, usage, date | version, site, dates, qty range, status, ECO, approved by | `engineering.bom_version` | BOM versions; SSRS BOM versions | CS03, CS05 |
| Multi-level explosion | drill-down | product, site, date, qty, usage, effectivity context, configuration | level, line, component, qty, scrap, alt group, effectivity | explode API | BOM designer; SSRS BOM lines | CS11 / CS12 / CS13 |
| Where-used | drill-down | component, date, levels, site | parent, level, qty, version status, top-level count | product closure | BOMWhereUsed; SSRS BOM where-used | CS15 |
| BOM version comparison | drill-down | version A, version B | added, removed, changed fields per line | `bom_line` | Engineering version comparison (D365) | CS14 |
| Route operations and lead time | operational report | route version, qty | op, resource req., setup, run, queue, transit, lead time | route projection | SSRS Routes / Operations listings | CA03, CA97 |
| Resource where-used | list inquiry | resource, group, capability | route versions, operations, site | operation requirements | Resource → Route versions | CA80, CR15 |
| Work calendar | list inquiry | calendar, date range | day, open, shifts, hours, efficiency | `calendar_day` | SSRS Calendars / Working time | SCAL, CR03 capacity |
| ECR/ECO by status | KPI | status, priority, category, age | count, median cycle time, overdue | `engineering.eco` | Change request / change order inquiries | CC04 change master information system |
| ECO impact report | operational report | ECO | affected products, sites, versions, documents, open-order counts (hooks) | impact snapshot | Impact analysis report | CC04, CS15 |
| Release status (product × legal entity) | list inquiry | product, entity, status, session | line status, version, accepted by, reason | `release_line` | Release status inquiry; SSRS Product release report | BD87 IDoc status for BD10/BD30 |
| Readiness check status | operational report | entity, policy, check, status | pending checks, assignee, due date, age | `readiness_check_result` | Readiness check status inquiry | — |
| Engineering versions by category | list inquiry | category, product, state | version code, effective range, state, BOM/route | `engineering_version` | Engineering version inquiry | Revision level display (CC03) |
| eBOM vs mBOM comparison | drill-down | eBOM import, active BOM | added, removed, changed lines | `ebom_import` | — | ECTR CAD BOM comparison |
| Document structure and where-used | drill-down | DIR | children, parents, BOM document lines | document structure | — | CV13, CV15 |

### 1.9.15 Printed and electronic documents

| Document | Trigger | Layout elements (header/line/footer fields) | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Indented BOM report | on demand, attached to ECO | header: product, version, site, effective range, usage; lines: level, line no, component, qty/series, unit, scrap, position, operation; footer: approved by, ECO, page x/y | PDF, XLSX | none | kept with ECO record |
| Route sheet | on demand | header: product, route version, site; lines: op no, resource requirement, setup, run per qty, queue, transit; footer: lead time for reference qty | PDF | none | kept with ECO record |
| ECR / ECO document | submission and release | header: number, title, reason, priority, release date, effectivity; lines: affected versions with diff summary, affected documents, impact counts; footer: approvals with signature manifestation | PDF, email to approvers | `ECO-{######}` | 10 years after product becomes obsolete (ISO 9001 §7.5.3; ISO 13485 §4.2.5 and 21 CFR 820 for medical-device tenants) |
| Product release report | release session completion | header: session number, policy; lines: product, version, entity, line status, reason | PDF | `REL-{yyyy}-{#####}` | 7 years |
| Signed engineering document rendition | released status with signature | original plus signature page: printed name, date/time (UTC and local), meaning, hash | PDF/A-2b | DIR number/part/version | per document-type retention class; 21 CFR 11.10(c) record retention |
| Work calendar printout | on demand | calendar, month grid, shifts, closed days | PDF | none | none |

### 1.9.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| ENG-J1 Active-version overlap check | nightly 01:00 | product_id hash (8 partitions) | read-only | < 10 min at 1M versions | report only | rows > 0 |
| ENG-J2 Version expiry | nightly 00:05 site time | site_id | sets `expired` where `effective_to` < today; idempotent | < 5 min | retried hourly | failures > 0 |
| ENG-J3 Expiry warning digest | weekly Monday 06:00 | owner team | read-only | < 2 min | none | none |
| ENG-J4 Calendar horizon extension | monthly 1st 02:00 | calendar_id | generate idempotent per range | < 2 s per calendar | per-calendar retry | calendars within warning window |
| ENG-J5 Scheduled ECO release | Temporal timer at release_date 00:00 | eco_id | workflow id per ECO | < 60 s p95 | compensation BR-9.9 | ECO in `scheduled` > release_date + 10 min |
| ENG-J6 Product closure integrity | nightly 03:30 | tenant | read-only | < 15 min | runbook "Rebuild product closure" | mismatches > 0 |
| ENG-J7 Release product structures | every 15 min for pending sessions; on ECO release | session_id | line-level idempotent | 1,000 components × 3 entities < 2 min | line `failed` after 5 retries | failed lines > 0 |
| ENG-J8 eBOM source poll | every 15 min | source_code | idempotent per (source, part, revision) | < 1 min per 100 payloads | payload to exceptions with reason | source unreachable 3 polls |
| ENG-J9 Readiness evaluation | hourly and on events | legal_entity_id | recompute per (product, check) | < 5 min for 100k released products | check left `pending` | overdue manual checks |

### 1.9.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles that include it |
|---|---|---|---|
| View engineering data | `GET /engineering/v1/**`, explode, where-used, diff | none | every manufacturing role |
| Maintain BOMs | BOM headers, draft versions, lines; BOM designer | Approve engineering changes on the same ECO | Design engineer |
| Maintain routes and resources | route versions, operations, resources, calendars | none | Manufacturing engineer |
| Activate BOM without ECO | `bom-versions/{id}:activate` | Maintain BOMs (when `change_controlled`) | Engineering manager |
| Maintain engineering changes | ECR/ECO create, submit, eBOM import | Approve engineering changes | Design engineer, Manufacturing engineer |
| Approve engineering changes | `ecos/{id}:approve` | Maintain engineering changes on the same ECO (no self-approval) | Engineering manager, Quality manager |
| Release engineering changes | `ecos/{id}:release`, backdate with separate duty | none | Engineering manager |
| Release product structures | release sessions, ENG-J7, release policies | Accept product releases in the same receiving entity | Engineering administrator |
| Accept product releases | Open product releases accept/reject | Release product structures | Plant master data steward |
| Maintain readiness policies and sign checks | readiness policies; manual check sign-off | sign-off conflicts with creating the release session (BR-9.14) | Plant master data steward, Quality manager |
| Sign engineering documents | signature transitions (FR-9.24) | Maintain documents on the same DIR when independent review is required | Quality manager, Engineering manager |
| Administer eBOM sources | `eng.ebom.sources`, ENG-J8 | none | Engineering administrator |

### 1.9.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| ENG-BOM-CYCLE | error | "Adding {c} creates a cycle: {path}." | Recursive structure | Remove the line | FR-9.11 |
| ENG-BOM-DEPTH | error | "Structure exceeds {max} levels." | Depth cap | Flatten or raise limit | BR-9.6 |
| ENG-BOM-OVERLAP | error | "Version overlaps active version {v} for this site, date and quantity." | Overlap on activation | Adjust ranges | FR-9.1 |
| ENG-BOM-COMP-OBSOLETE | error | "Component {c} is obsolete." | Obsolete component | Replace component | FR-9.11 |
| ENG-BOM-PHANTOM-NOBOM | error | "Phantom {c} has no active BOM on {date}." | Phantom without BOM | Activate its BOM | BR-9.5 |
| ENG-ROUTE-UNSATISFIED | error | "No resource satisfies operation {op} requirement {req}." | Requirement unmet | Add resource or capability | FR-9.5 |
| ENG-ROUTE-NEXT-MISSING | error | "Next operation {n} does not exist." | Bad reference | Fix operation number | FR-9.4 |
| ENG-ROUTE-SECONDARY-NOPRIMARY | error | "Secondary operation {op} has no primary at its join." | BR-9.7 | Add primary | BR-9.7 |
| ENG-ECO-NOT-APPROVED | error | "ECO {n} must be approved before release." | Wrong status | Approve first | FR-9.7 |
| ENG-ECO-BACKDATE-DUTY | error | "Backdated release requires the Backdate duty." | Past release date | Use today or request duty | BR-9.10 |
| ENG-ECO-HORIZON | error | "Release date is more than {d} days ahead." | Horizon exceeded | Choose an earlier date | BR-9.10 |
| ENG-RES-IN-USE | error | "Resource {r} is required by active route {v}." | Delete in use | Change routes first | BR-9.11 |
| ENG-OWNED-FIELD | error | "{field} is owned by engineering organization {eo}." | Local edit of engineering data | Raise an ECR | FR-9.14 |
| ENG-READINESS-PENDING | warning | "{n} readiness checks are pending." | FR-9.17 | Complete checks | FR-9.17 |
| ENG-NOMEN-OVERFLOW | error | "Version numbering exhausted for {product}." | BR-9.15 | Change nomenclature width | BR-9.15 |
| ENG-ALT-CONFLICT | error | "Alternative group {g} is inconsistent: {reason}." | BR-9.16 | Correct priorities/probabilities | BR-9.16 |
| ENG-EFF-OVERLAP | error | "Serial ranges overlap with ECO {n}." | BR-9.17 | Adjust ranges | BR-9.17 |
| ENG-COND-UNKNOWN-ATTR | error | "Condition references unknown attribute {a}." | FR-9.22 | Fix condition or model | FR-9.22 |
| ENG-DOC-ECO-REQUIRED | error | "Document type {t} is change-controlled; release through an ECO." | FR-9.25 | Add to an ECO | FR-9.25 |
| ENG-DOC-SIGNED | error | "Originals are locked by signature on {date}." | BR-9.18 | Create a new version | BR-9.18 |
| ENG-EBOM-PART-UNKNOWN | warning | "Part {p} is not a known product." | FR-9.27 | Create product or map identifier | FR-9.27 |
| ENG-EBOM-REVISION-RELEASED | error | "Revision {r} of {p} is already released." | BR-9.19 | Send a new revision | BR-9.19 |

### 1.9.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| Explosion (7 levels, 2,000 components) | 200k/day from planning and costing; peak 1,000/min | 120 / 500 / 900 ms | 50 req/s per replica | cache only | product closure; cache keyed by active version set | cache hit ratio rises; uncached p95 ≤ 1.2 s |
| As-of resolve | 5M/day | 5 / 30 / 60 ms | 1,000 req/s | none | (product_id, site_id, effective_from, status) | unchanged |
| Where-used (100k lines) | 20k/day | 60 / 300 / 600 ms | 50 req/s | none | btree bom_line(component_product_id) | levels capped at request value |
| BOM line save with cycle check | 50k/day | 20 / 80 / 150 ms | 100 req/s | ~200 B per line; 10M lines ≈ 3 GB | closure lookup | unchanged |
| ECO release (500 versions) | 100/day | 10 / 60 / 120 s | 5 concurrent | ECO snapshots ~100 KB | Temporal workflow | queued; timers unaffected |
| Release session (1,000 components × 3 entities) | 50/day | 40 s / 2 min / 4 min | 4 concurrent sessions | release lines ~1 KB | session_id partition of work | lines queued, no failure |
| eBOM import (500 lines) | 500/day | 2 / 8 / 15 s | 10 concurrent | payloads ~200 KB | unique (source, part, revision) | ENG-J8 extends poll interval to 30 min |

### 1.9.20 Localization and compliance hooks

| Item | What varies | Country packs touching it (wave 48) | Extension point |
|---|---|---|---|
| Public holidays in work calendars | US federal; UK bank holidays per nation; DE holidays per Land; FR 11 jours fériés plus Alsace-Moselle; IN national and state holidays; MX mandatory rest days (Ley Federal del Trabajo Art. 74); CA federal and provincial | all seven | `HolidayCalendarProvider` feeding FR-9.6 closed days |
| Electronic records and signatures | US 21 CFR Part 11; EU GMP Annex 11; EU eIDAS for qualified signatures (optional) | US, DE, FR | signature policy per document type (FR-9.24) |
| Design and change control | ISO 9001 §8.3/§8.5.6; ISO 13485 §7.3.9; 21 CFR 820 (QMSR effective 2026-02-02); EU MDR Annex II technical documentation | US, DE, FR | `change_controlled` engineering categories; ECO record retention |
| Export control on technical data | US EAR/ITAR technical data, EU dual-use Regulation 2021/821 | US, DE, FR | export classification attribute on engineering category; access restricted through OPA attributes (wave 48) |
| Units on drawings and BOMs | US customary vs metric | US | unit conversion (FR-6.9, FR-8.7) at display |
| Working time | Shift patterns must respect local limits (EU Working Time Directive 2003/88/EC, IN Factories Act 1948 §51 48 h/week); enforced for workers in wave 42, warned here when calendar shifts exceed 48 h/week | DE, FR, IN | calendar validation hook (warning only) |

### 1.9.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX §12.15.1 PIM, Common — Product readiness checks (D365), EcoResProductReadiness; register §12.99.4 #144 | PARTIAL-9 | ADDED as FR-9.17 | Automated and manual checks; blocks via FR-8.14 |
| AX §12.15.4 PIM, Setup — Product readiness policies (D365); register §12.99.4 #151 | PARTIAL-9 | ADDED as FR-9.17 | Parameter `eng.readiness.policy` |
| AX §12.34.1 ECM, Common — Release product structure, EngChgReleaseProductStructure; register §12.99.4 #280 | PARTIAL-9 | ADDED as FR-9.15 | Engineering vs operational split in FR-9.14 |
| AX §12.34.1 ECM, Common — Product readiness checks (per LE checklist), EngChgProductReadiness; register §12.99.4 #281 | PARTIAL-9 | ADDED as FR-9.17 | Per receiving legal entity |
| AX §12.34.1 ECM, Common — Product acceptance, EngChgProductAcceptance; register §12.99.4 #282 | PARTIAL-9 | ADDED as FR-9.16 | Open product releases screen |
| AX §12.34.1 ECM, Common — Engineering product categories, EngChgEngineeringProductCategory; register §12.99.4 #284 | PARTIAL-9 | ADDED as FR-9.18 | Category carries nomenclature, readiness and release policies |
| AX §12.34.2 ECM, Inquiries — Release status inquiry (product × legal entity); register §12.99.4 #286 | PARTIAL-9 | ADDED as FR-9.15 | §1.9.14 "Release status" |
| AX §12.34.2 ECM, Inquiries — Readiness check status inquiry; register §12.99.4 #287 | PARTIAL-9 | ADDED as FR-9.17 | §1.9.14 "Readiness check status" |
| AX §12.34.2 ECM, Reports — SSRS Product release report; register §12.99.4 #288 | PARTIAL-9 | ADDED as FR-9.15 | §1.9.15 "Product release report", rendered by wave 46 |
| AX §12.34.3 ECM, Periodic — Release product structures (batch); register §12.99.4 #289 | PARTIAL-9 | ADDED as FR-9.15 | Job ENG-J7 |
| AX §12.34.3 ECM, Periodic — Readiness check evaluation (batch); register §12.99.4 #290 | PARTIAL-9 | ADDED as FR-9.17 | Job ENG-J9 |
| AX §12.34.4 ECM, Setup — Engineering organizations; register §12.99.4 #292 | PARTIAL-9 | ADDED as FR-9.14 | Parameter `eng.org.engineering` |
| AX §12.34.4 ECM, Setup — Engineering product categories (version nomenclature, attributes, readiness policy, release policy); register §12.99.4 #293 | PARTIAL-9 | ADDED as FR-9.18 | — |
| AX §12.34.4 ECM, Setup — Version nomenclature (number/letter/date segments); register §12.99.4 #294 | PARTIAL-9 | ADDED as FR-9.18 | BR-9.15 increment rules |
| AX §12.34.4 ECM, Setup — Product readiness policies / readiness checks; register §12.99.4 #295 | PARTIAL-9 | ADDED as FR-9.17 | Hooks to waves 13, 17, 22, 26, 31 return not_applicable until live |
| AX §12.34.4 ECM, Setup — Product release policies; register §12.99.4 #296 | PARTIAL-9 | ADDED as FR-9.15 | Parameter `eng.release.default_policy` |
| SAP §13.21 PP Master data — CS51/CS52/CS53/CS61/CS62/CS63/CS71 BOM categories; register §13.99.2 #238 | PARTIAL-9 | ADDED as FR-9.19 | Usage and category on header; sales-order BOM instances in wave 27, WBS BOMs in wave 41, equipment/FL BOMs in wave 44 reuse the model |
| SAP §13.21 PP Master data — alternative items, discontinuation / follow-up material; register §13.99.2 #243 | PARTIAL-9 | ADDED as FR-9.20 | Resolution logic in MRP wave 22 |
| SAP §13.21 PP Customizing — OS40/OS41/OS44 ECM effectivity types (date, serial, parameter); register §13.99.2 #250 | PARTIAL-9 | ADDED as FR-9.21 | Parameter `eng.effectivity.types` |
| SAP §13.32 LO-VC Master data — PSM / iPPE product variant structures; register §13.99.2 #360 | PARTIAL-9 | SUPERSEDED by FR-9.22 super BOM/routing with selection conditions | iPPE-specific objects (PVS nodes, access diagrams, CMP) have no equivalent; multi-level configurable structures are modeled as super BOMs over configuration attributes |
| SAP §13.32 LO-VC Master data — VC with batch/serial effectivity, parameter effectivity; register §13.99.2 #362 | PARTIAL-9 | ADDED as FR-9.21 | Effectivity context combined with configuration in explode |
| SAP §13.35 DMS Master data — CV11/CV12/CV13/CV15 document structure; register §13.99.2 #376 | GAP | ADDED as FR-9.26 | Structures versioned with parent DIR |
| SAP §13.35 DMS Master data — CV01N status network with release workflow and digital signature; register §13.99.2 #378 | PARTIAL-9 | ADDED as FR-9.24 | 21 CFR 11 manifestation and linking |
| SAP §13.35 DMS Master data — CV01N version / part / change number (ECM); register §13.99.2 #379 | PARTIAL-9 | ADDED as FR-9.25 | ECO-controlled DIR versions |
| SAP §13.35 DMS Master data — ECTR CAD integration, CAD BOM derivation; register §13.99.2 #381 | GAP | ADDED as FR-9.27 | Contract and import path here; native CAD file management out of scope; vendor connectors deferred to wave 35 |
| SAP §13.35 DMS Master data — Document BOM items (CS01 item category D); register §13.99.2 #382 | PARTIAL-9 | ADDED as FR-9.23 | Line types `document` and `text` |
| SAP §13.35 DMS Master data — PLM suite (Engineering Record, PSM, Visual Enterprise, Recipe Development, Specification management, Labeling); register §13.99.2 #383 | PARTIAL-9 | DEFERRED to wave 24 | Recipe development and specifications go to wave 24 as the register proposes; global label management to wave 48; Visual Enterprise 3D viewing is out of scope; Engineering Record is covered by ECR/ECO (FR-9.7) |
| SAP §13.35 DMS Reports — CV13 / CV15 document structure display / where-used; register §13.99.2 #386 | GAP | ADDED as FR-9.26 | §1.9.14 "Document structure and where-used" |
| SAP §13.35 DMS Reports — ECTR reports (CAD BOM comparison); register §13.99.2 #388 | GAP | ADDED as FR-9.27 | §1.9.14 "eBOM vs mBOM comparison" |
| SAP §13.35 DMS Periodic — ECTR sync jobs; register §13.99.2 #391 | GAP | ADDED as FR-9.27 | Job ENG-J8 |
| SAP §13.35 DMS Customizing — DC20 document status definitions; register §13.99.2 #393 | PARTIAL-9 | ADDED as FR-9.24 | Status network per document type |
| SAP §13.35 DMS Customizing — ECTR configuration (CAD applications, templates, BOM sync rules); register §13.99.2 #396 | GAP | ADDED as FR-9.27 | Parameter `eng.ebom.sources` |

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
| Copy data between legal entities | DMFCopyEntity (Copy entity data between companies), D365 "Copy into legal entity" | EC01/EC02 organizational object copy; LSMW cross-company reload | `copy` project type (FR-10.14) | full |
| Configuration data packages | D365 configuration data projects, data templates, LCS asset library | Customizing transports (SE09/SE10, SCC1), BC Sets (SCPR20), cross-system viewer SCU0 | signed configuration packages with diff (FR-10.15) | improved (diff before apply) |
| Recurring imports | D365 recurring data jobs (recurring integrations) | LSMW periodic runs, IDoc inbound processing (BD87) | scheduled incremental runs until cutover (FR-10.16) | full |

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
- **FR-10.14 Copy entity data between legal entities** — Should. The system shall support a project type `copy` whose source is a tenant legal entity and whose target is another legal entity in the same tenant, reusing the mapping DSL with identity rules plus key-rewrite rules (source code → target code), dry run, idempotent load and reconciliation; supported objects: customer and vendor roles, released products with site defaults, BOM and route versions, resources and calendars.
- **FR-10.15 Configuration data packages** — Should. The system shall export setup data (the parameters of §1.6.13–§1.9.13, groups, terms, lifecycle states, document types, nomenclatures, readiness and release policies, mapping specs) as a versioned package (JSON bodies, manifest with schema version, dependency order, sha256 per file, signature by the tenant key) and import it into another tenant or environment after a dry-run diff showing create / update / unchanged per record; deletes are never applied.
- **FR-10.16 Recurring import jobs** — Should. The system shall let a project schedule incremental `load` runs (cron, minimum interval 15 min) from a connector or a file drop (SFTP or S3-compatible object storage path) during the coexistence and cutover window, using incremental extraction (FR-10.2); scheduling stops automatically at the project's `cutover_at`.
- **FR-10.17 Mapping packs for coverage-closure objects** — Must. The system shall extend the standard AX and SAP mapping packs, with tests, to the objects added by the phase-1 coverage closure: AX OMTeam/OMTeamMember → Team (FR-6.14); VendCertification/VendCertificationType → VendorCertification (FR-7.14); PaymSched/PaymSchedLine and SAP T052R → PaymentScheduleLine (FR-7.15); KNA1-KNRZE, KNB1-KNRZB/XDEZV, LFA1-LNRZA, LFZA and AX DirPartyRelationship → PartyRelationship (FR-7.17); DirAddressBook/DirAddressBookParty → AddressBook (FR-7.18); KNVH → customer hierarchy (FR-7.19); EcoResProductRelation → ProductRelation (FR-8.13); EcoResProductLifecycleState and MARA-MSTAE/MARC-MMSTA with T141 → LifecycleState (FR-8.14); ASMD/ASMDT → service products (FR-8.17); CUVTAB/CUVTAB_VALC → DecisionTable (FR-8.18); DRAW/DRAT/DRAD and DocuRef/DocuType → DocumentInfoRecord and links (FR-8.20); EngChgEngineeringProductCategory, version nomenclature, release and readiness policies (FR-9.15–FR-9.18); STPO ALPGR/ALPST/ALPRF/EWAHR/KZAUS/NFMAT (FR-9.20); AENR effectivity data (FR-9.21); STKO/STPO with STLTY = D document structures (FR-9.26).
  - The STLTY = D load supersedes the "not migrated: SAP document BOMs" note in §1.9.11.
- **FR-10.18 Data templates** — Could. The system shall let a project attach per-object default-value templates (D365 data templates / default templates per module) applied before mapping rules, so that fields absent in the source receive project-specific defaults recorded as `default_from_template` in lineage.

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

**Addendum entities (FR-10.14 – FR-10.18)** — migration_project gains `project_type` (migrate, copy, config_package), `source_legal_entity_id`, `target_legal_entity_id`, `cutover_at`, `schedule_cron`; key_rewrite_rule: project_id, field, from_value, to_value; config_package: id, version text(20), schema_version int, manifest jsonb, signature bytea, created_by, created_at, applied jsonb [{tenant_id, applied_at, diff_counts}]; data_template: project_id, object, defaults jsonb.

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
- **BR-10.13** Copy projects rewrite every legal-entity-scoped natural key; a rewritten key that already exists in the target is rejected with `COPY-KEY-EXISTS` unless the project selects `upsert`.
- **BR-10.14** Configuration packages apply in manifest dependency order; a package whose schema version is newer than the target's is refused (`MIG-PKG-SCHEMA-NEWER`); a package whose signature does not verify is refused (`MIG-PKG-SIGNATURE`).
- **BR-10.15** Recurring runs never overlap (BR-10.2); a trigger that fires while a run is active is skipped and logged, never queued.

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
- **AC-10.11** (FR-10.15, BR-10.14) Given a package of 240 setup records from tenant A, when it is dry-run against tenant B where 40 records are identical and 10 differ, then the diff shows 190 create, 10 update, 40 unchanged; after apply, a second apply reports 0 changes.
- **AC-10.12** (FR-10.14, BR-10.13) Given 500 released products in USMF, when a copy project loads them into CAMF, then the run is `reconciled`, every item number exists in CAMF, and a second run skips 500.
- **AC-10.13** (FR-10.17) Given the mapping test suites for the 15 added objects, when CI runs, then all pass; an AX PaymSched with 3 percent lines maps to 3 schedule lines summing 100.00.

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

### 1.10.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| `mig.workers` | project | int | 8 | 1–16 | Loader partitions (FR-10.6) | Data management parameters → entity execution threads | Migration Cockpit number of background jobs |
| `mig.batch_size` | project | int | 500 | 50–5,000 | Rows per API call | Set-based processing / bundle size | LTMC transfer package size |
| `mig.reject_threshold_pct` | project | decimal | 0.5 | 0–10 | Load gate after validation (FR-10.4) | — | — |
| `mig.sample_pct` | project | decimal | 1 (min 100, max 5,000 rows) | 0.1–100 | Reconciliation sample (FR-10.8) | Compare data | — |
| `mig.widened_sample_pct` | project | decimal | 10 | 1–100 | BR-10.9 | — | — |
| `mig.staging_retention_days` | project | int | 30 | 7–365 | Purge after sign-off (BR-10.11) | Staging cleanup (job history cleanup) | Migration Cockpit staging table deletion |
| `mig.source.codepage` | connector | enum | UTF-8 | UTF-8, Windows-1252, ISO-8859-1, UTF-16LE, SAP 1100, SAP 1160 | Decode source bytes | Source data formats → code page | LSMW file code page, LTMC file encoding |
| `mig.source.timezone` | connector | IANA | source server zone | IANA | `utcdatetime` / DATS+TIMS conversion | Source data formats → time zone | User / system time zone of extract |
| `mig.retire_missing` | project | bool | false | true / false | FR-10.7 | — | — |
| `mig.overwrite_drift` | project | bool | false | true / false | BR-10.8 | — | — |
| `mig.allow_unreconciled_deps` | project | bool | false | true / false | Run-start dependency check | Entity sequencing | Migration object dependencies |
| `mig.recurring.min_interval_min` | tenant | int | 15 | 15–1,440 | FR-10.16 | Recurring data jobs → recurrence | Job scheduling (SM36) |
| `mig.cutover_at` | project | timestamptz | none | future timestamp | Stops recurring runs | — | — |
| `mig.signoff.approvers` | project | int | 2 | 1–3 | Required signatures (§1.10.15) | — | — |
| `mig.connector.paging` | connector | record | ax-odbc 10,000 RecIds; d365 `$top` 10,000; RFC_READ_TABLE ROWCOUNT 50,000 | per connector | Extraction page size | Entity paging / skip staging | RFC_READ_TABLE ROWCOUNT / ROWSKIPS |
| `mig.pii_key_ring` | project | Vault path | `passage/mig/{project}` | Vault paths | FR-10.13 | — | — |
| `mig.package.signing_key` | tenant | Vault path | `passage/config-signing` | Vault paths | FR-10.15 | — | — |

### 1.10.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Run history | list inquiry | project, mode, status, date | run no, mode, status, start, duration, counts | `migration_run` | Execution history (DMFExecution); job history | Migration Cockpit monitor; SM37 |
| Run stage progress | drill-down | run, object | stage, rows, rows/s, ETA, errors | stage events | Execution details | Migration Cockpit "Monitoring" |
| Reconciliation report | operational report (sign-off evidence) | run, object | counts per stage, hash totals per column, sample mismatches | `reconciliation_result` | Compare data results | Migration object statistics |
| Exception queue | operational report | run, object, reason code | source key, reason, mapped payload, target response | `staging_row` (rejected, mismatched) | Staging data with errors | Migration Cockpit messages; LSMW error log |
| Mapping catalog | list inquiry | project, object, version | rules, enum tables, tests passed, checksum | `mapping_spec` | Entity mapping inquiry | LTMOM migration object modeler |
| Record lineage | drill-down | target entity and id | run, source object, source key, loaded at, hashes | `lineage_entry` | — | — |
| Throughput KPI | KPI | project, object, worker | rows/s, retries, batch failures | run metrics | — | — |
| Configuration package diff | drill-down | package, target tenant | create / update / unchanged per record | `config_package` | Configuration data project comparison | SCU0 customizing cross-system viewer |
| Recurring schedule | list inquiry | project | next run, last result, skipped triggers | schedule projection | Recurring data jobs | SM37 |
| Copy project summary | operational report | copy project | objects, rewritten keys, collisions | copy run | Copy entity data between companies log | EC01 log |

### 1.10.15 Printed and electronic documents

| Document | Trigger | Layout elements (header/line/footer fields) | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Reconciliation sign-off report | `:sign-off` | header: project, run no, mode, mapping and connector versions, signers; lines: per object counts, hash totals, sample size and mismatches, accepted differences with reasons; footer: signatures, sha256 of the report | PDF, XLSX | `{project}-RUN-{run_no}` | data-conversion control evidence for SOX ITGC; retain 7 years minimum (10 years where HGB/AO applies) |
| Exception export | on demand | run, object, reason, source key, payload (PII masked) | XLSX, CSV | none | follows staging retention |
| Configuration package | export (FR-10.15) | manifest, dependency order, signatures | JSON (zip) | package version | 7 years as change evidence |
| Migration evidence bundle | after sign-off | zip: sign-off report, mapping checksums, lineage summary, connector probe results | ZIP | `{project}-EVID-{run_no}` | 7–10 years with go-live audit file |

### 1.10.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| MIG-J1 Staging purge timer | hourly | run_id | drops partitions once; re-run no-op | < 1 min per run | retried next hour | purge overdue > 24 h |
| MIG-J2 Lineage integrity | nightly 02:30 | target_entity | read-only | < 20 min for 10M lineage rows | report only | orphans > 0 |
| MIG-J3 Recurring incremental runs | per project cron (min 15 min) | project_id | BR-10.15; incremental watermark per object | < 5 min for 10k changed rows | run `failed`, next trigger proceeds | consecutive failures ≥ 3 |
| MIG-J4 Staging tablespace monitor | every 5 min | tablespace | read-only | < 5 s | none | usage > 80% |
| MIG-J5 Connector credential expiry check | daily 06:00 | connector | read-only | < 1 min | none | expiry within 14 days |
| MIG-J6 Orphaned staging partition sweep | weekly Sunday 04:00 | partition | drops partitions with no run row | < 10 min | none | orphans found |

### 1.10.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles that include it |
|---|---|---|---|
| View migrations | `GET /migration/v1/**` (PII masked) | none | Implementation consultant, Data migration lead, Auditor |
| Administer migration | projects, connectors, `:test`, schedules, copy projects | Approve migration sign-off | Data migration lead |
| Maintain migration mappings | mapping-spec upload, data templates | Approve migration sign-off | Implementation consultant |
| Run migrations | start, cancel, rerun-exceptions | Approve migration sign-off | Data migration lead, Implementation consultant |
| Resolve migration exceptions | `PATCH /exceptions/{rowId}`, bulk accept difference | Approve migration sign-off | Master data steward |
| Approve migration sign-off | `:sign-off` | Run migrations, Resolve migration exceptions, Maintain migration mappings on the same project | Business process owner, Controller |
| Apply configuration packages | config package import and apply | Maintain migration mappings (package author cannot apply to production) | System administrator |
| Read staged personal data | `pii.read` on staging and exceptions | Administer migration | Privacy officer |

### 1.10.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| MIG-RUN-ACTIVE | error | "Run {n} is already active for this project." | Second start | Wait or cancel | BR-10.2 |
| MIG-MAP-TEST-FAIL | error | "{n} mapping tests failed for {object}." | CI or upload test failure | Fix mapping | FR-10.3 |
| MIG-MAP-UNKNOWN-FN | error | "Unknown mapping function {fn}." | Upload validation | Use sanctioned function | FR-10.3 |
| MAP-ENUM-UNMAPPED | error | "Value {v} of {field} has no mapping." | Enum without default | Add enum row or default | BR-10.4 |
| MAP-PRECISION-LOSS | warning | "Rounding changed {field} by {delta}." | Scale reduction | Review target scale | BR-10.5 |
| VAL-FK-MISSING | error | "{field} references {key}, which does not exist." | FK expectation | Load parent or fix value | FR-10.4 |
| VAL-DEP-FAILED | error | "Parent {key} failed in this run." | Dependency failure | Fix parent row | BR-10.10 |
| API-422 | error | "Target rejected the row: {detail}." | Business validation | Edit row | FR-10.5 |
| TARGET-DRIFT | warning | "Target changed after load; not overwritten." | User edit after load | Set overwrite_drift or accept | BR-10.8 |
| MIG-THRESHOLD-EXCEEDED | error | "Rejects {x}% exceed threshold {t}%." | Validation gate | Resolve rejects | FR-10.4 |
| MIG-DEP-UNRECONCILED | error | "Dependency {object} has no reconciled run." | Run-start check | Reconcile dependency | §1.10.5 |
| MIG-HASH-MISMATCH | warning | "Hash total differs for {column}; sample widened." | BR-10.9 | Review mismatches | BR-10.9 |
| MIG-PKG-SCHEMA-NEWER | error | "Package schema {v} is newer than this environment." | Version skew | Upgrade target | BR-10.14 |
| MIG-PKG-SIGNATURE | error | "Package signature is invalid." | Tampered or wrong key | Re-export | BR-10.14 |
| COPY-KEY-EXISTS | error | "{key} already exists in {target}." | Copy collision | Choose upsert or rewrite rule | BR-10.13 |
| MIG-CONN-AUTH | error | "Connector {c} authentication failed." | Bad credentials | Update Vault secret | FR-10.2 |
| MIG-RFC-ROWWIDTH | info | "Rows of {table} exceed 512 bytes; read in {n} field groups." | RFC_READ_TABLE limit | none | FR-10.2 |

### 1.10.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| Full phase-1 load (170k master records) | 1–5 runs per project | end-to-end < 30 min | ≥ 100 rows/s per worker; 8 workers | staging ~2 KB per row, dropped after purge | staging list-partitioned by run_id | HPA to 16 workers; runtime grows linearly |
| Extraction per connector worker | 20k rows/s target | — | 20,000 rows/s | none | source paging | backpressure on staging writes |
| Re-run with no changes | per run | < 5 min for 170k rows | hash compare 5,000 rows/s | none | (run_id, object, status) | unchanged |
| Run monitor update | SSE every 2 s | lag 0.5 / 2 / 4 s | 200 concurrent viewers | none | event fan-out | refresh drops to 5 s |
| Reconciliation sample compare | 1% sample, min 100, max 5,000 per object | < 60 s per object | 500 target reads/s | results ~1 MB per run | — | sample compare serialized per object |
| Lineage lookup (audit tab) | 50k/day | 5 / 30 / 60 ms | 200 req/s | ~150 B per loaded row; 10M rows ≈ 2 GB | (target_entity, target_id) | unchanged |
| Configuration package apply (5,000 records) | 10/day | 20 / 60 / 120 s | 1 concurrent per tenant | packages 1–10 MB | dependency order | queued |

### 1.10.20 Localization and compliance hooks

| Item | What varies | Country packs touching it (wave 48) | Extension point |
|---|---|---|---|
| Code pages and scripts | AX 2012 non-Unicode collations (Windows-1252 for US/UK/DE/FR/MX/CA data), SAP non-Unicode code pages 1100/1160, Devanagari and other Indic scripts in IN data | IN | `mig.source.codepage`; `codepage_normalize` function |
| Date and number formats in files | DD.MM.YYYY and decimal comma (DE, FR), DD/MM/YYYY (UK, IN, FR), MM/DD/YYYY (US), YYYY-MM-DD (CA ISO) | all | `coerce` rule with explicit format per source column |
| Country-specific source fields | SAP KNA1-STCD1..5 meaning per country (MX RFC, IN PAN/GSTIN in J_1I fields, FR SIRET in STCD2), AX country-specific tables (e.g. Mexico CFDI fields, India tax registration) | IN, MX, FR | mapping pack overlays keyed `country: XX` layered over the base pack |
| Data residency and transfer | GDPR Chapter V transfers, India DPDP Act 2023 cross-border rules, Mexico LFPDPPP transfer notice; staging stays in the tenant's region | all | per-tenant region pinning of migration-db; no cross-region connectors |
| Retention and erasure | staging purge (BR-10.11); sign-off evidence kept 7–10 years; personal data in exceptions masked | all | retention classes `mig-staging`, `mig-evidence` |
| Statutory master-data checks on load | MX RFC structure and SAT list checks, IN GSTIN state code vs address state, EU VAT format | MX, IN, DE, FR | target-API dry run (FR-10.4) runs the wave-7 validators |

### 1.10.21 Coverage closure

No row in the AX gap register (§12.99) or the SAP gap register (§13.99) carries wave 10 as its current or proposed wave. Every AX and SAP inventory row tagged for wave 10 is `COVERED-10`. Three of those rows are covered in the inventory but were not named in this section until now: DMFCopyEntity, D365 configuration data projects and data templates, and recurring data jobs. They are now FR-10.14, FR-10.15, FR-10.18 and FR-10.16. The table below lists register-derived consequences for this wave: migration packs needed by the capabilities that waves 6–9 added to close their register rows.

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| (none assigned to wave 10) | — | — | 0 register rows |
| Consequence of §1.6.21 (AX OMTeam rows #107, #114, #232, #251) | n/a (consequential) | ADDED as FR-10.17 | OMTeam / membership pack |
| Consequence of §1.7.21 (AX #158, #165; SAP #17, #23, #35, #37, #47, #50) | n/a (consequential) | ADDED as FR-10.17 | VendCertification, PaymSched/T052R, KNRZE/KNRZB/LFZA, CPD account groups flagged one-time |
| Consequence of §1.8.21 (AX #141, #143, #146, #147, #148, #283, #297; SAP #163, #181, #356, #359, #374, #375, #384, #385, #392) | n/a (consequential) | ADDED as FR-10.17 | EcoResProductRelation, lifecycle states/T141, ASMD, CUVTAB, DRAW/DRAT/DRAD |
| Consequence of §1.9.21 (AX #144–#296 ECM rows; SAP #238, #243, #250, #362, #376, #378, #379, #381, #382, #386, #388, #391, #393, #396) | n/a (consequential) | ADDED as FR-10.17 | Engineering categories, policies, STPO alternative/discontinuation fields, effectivity, STLTY = D structures |
