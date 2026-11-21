# Depot UX standards (UXS-01 to UXS-20)

Each standard below is a rule a reviewer can check. Every standard has the same parts:

- **Rule**: what must be true, in one or two sentences.
- **Why**: the user problem it removes, and the thesis promise it delivers (`docs/prd/part-00-thesis.md`).
- **Do / Don't**: examples from real Depot screens (routes and files as of commit `cba4ebe`, audit run 1).
- **Build or reuse**: the shared code that carries the rule. Paths are under `apps/web/src/` unless stated.
- **Acceptance**: what a reviewer checks before calling the standard met.
- **Enforcement**: the automated check that keeps it met, and where it lives. "Source scan" means a Vitest test that reads source files. `apps/web` has no ESLint; its lint-style rules are source scans (`shell/lookup/rawIdInputs.test.ts`, `shell/ui/consistency.test.ts`, `shell/plainLanguage.test.ts` are the models). Each source scan carries an allowlist of files that do not meet the rule yet. The allowlist may only shrink: the test fails when a listed file now passes (remove it) and when an unlisted file fails.

Findings are the ids from `docs/sdlc/run-1/ux-audit.md` (UX-001 to UX-050).

Words used here:

- **Screen**: a route in the web app or portals, or one full-screen step on a floor device. Dialogs and side panels are not screens.
- **Interaction**: one click or tap, one scan, one key chord (Ctrl+S), or one field value entered (typing then Tab or Enter counts once).
- **Workspace entry route**: a route listed for a workspace in `shell/workspaces/workspaces.json`.

---

## UXS-01 Honest screens

**Rule.** A screen shows only what it got from a real data source. A page never shows an empty state, count, status or progress that it did not fetch. A page or module with no working data source is not in the navigation, workspaces, command palette or home page; if something must link to it, the link opens `NotAvailablePage` ("isn't available yet"). Sample data lives only under `src/demo/` and only runs in demo builds.

**Why.** People stop trusting a system that lies once. Today `/admin/audit` always says "No audit entries yet" (UX-001), `/admin/sequences` says "No sequences set up yet" while sequences exist (UX-005), `/admin/import` has a disabled "Coming soon" button (UX-004), and the Job monitor shows an invented 400,000 of 1,000,000 rows job in production builds (UX-006). For an auditable product a false empty audit page is worse than no page. Thesis: A7 (trace is trustworthy), U4 (job status is real), S4 (close is visible, not hidden).

**Do**
- `/admin/audit` fetches from the audit service and shows real rows, or is hidden until it does.
- `/workflow/jobs` lists batch runs from the batch service; in the demo build, the demo handler supplies them.
- `/admin/sequences` lists the sequences `GET /api/v1/numbering/sequences` returns (code, legal entity, format, next value), read-only at first. The setup checklist's "No number sequences yet" link (`modules/setup/checks.ts:35`) then lands on real data instead of a false empty state or a dead end. Create and edit follow later.
- `/admin/import` opens the import that already works: the setup guide's "Import your first records" step (`modules/setup/ImportStep.tsx`: template download, preview rows with per-row errors, start the import), as `docs/user/tenant-admin.md` describes.
- A task whose start page does not exist yet is marked planned only until its first cut ships, and the planned entry names that slice and date (CT4 "Close last month" is planned until the read-only close workspace, rollout slice 4).
- The demo tour (`src/demo/DemoTour.tsx`) names the workspaces that have sample data, and a screen without sample data says "This screen has no sample data in the demo" instead of the tour's blanket "most screens are empty in the demo".

**Don't**
- `AuditViewer({ items = [] })` in `modules/admin/pages.tsx`: a default prop that guarantees an empty table.
- `WorkflowHome({ counts = { running: 1, failed: 0 } })` and the `Sample data` badge in `modules/workflow/pages.tsx`.
- `SequencePage`, `AlertInbox`, `DocumentsPage`, `TextsPage` in `modules/admin/pages.tsx`: static `EmptyState` with no fetch.
- `WorkspacePlaceholder` ("Coming soon") inside a workspace nav.

**Build or reuse**
- Reuse `shell/pages/NotAvailablePage.tsx` and the tenant feature check in `shell/Shell.tsx` (`isPathEnabled`).
- Add `shell/plannedRoutes.json`: routes that exist in code but are not ready. Each entry has a reason, an issue number, the rollout slice that ships it and a target date. The shell renders `NotAvailablePage` for them and strips them from nav, workspaces and the palette. A route may not stay planned past its date (see Enforcement).
- Move the workflow and admin sample rows into `src/demo/handlers/` so the demo build still shows them through the normal fetch path.

**Acceptance**
1. Every page that renders `EmptyState`, `DataTable empty=`, `StatTile` or a progress value gets that value from a fetch hook (`useQuery`, `useInfiniteQuery`, `useResource`, `useLoad`, `usePagedRows`, a `useRows` passed to `ListPage`) or from props passed by a `*Route` wrapper that fetches.
2. No page component has a default prop holding literal rows, counts or statuses.
3. No string "Sample data" or "Coming soon" renders outside `src/demo/`.
4. No nav entry, workspace entry or palette entry points to a route in `plannedRoutes.json`.
5. An empty state, when it shows, names what the list is for and gives the next step (`lists.md`, "first-run empty").

**Enforcement**
- New source scan `shell/honestScreens.test.ts`: checks 1 to 3 over `modules/**/*.tsx` (the shrinking allowlists sit at the top of the test file, each entry with its reason). It also covers check 4: the shell drops `plannedRoutes.json` paths from the sidebar, workspaces, All features, home and palette (`shell/pages/PlannedPage.tsx`, `withoutPlannedNav`).
- Extend `modules/registry.test.ts` and `shell/workspaces/workspaces.test.tsx`: check 4, and every `plannedRoutes.json` entry has an issue, a slice and a target date that has not passed (the test fails on the day after the date, so a planned route cannot sit there by default; moving the date is a reviewed change).
- Change the tests that today assert fake data: `modules/admin/module.test.tsx` ("Audit entries" contains `user-1`) and `modules/workflow/module.test.tsx` ("Batch runs" contains `rows-1m`) must assert a fetch and its result instead. `apps/web/docs/design-system.md` section 8 lists both as "preserve"; this standard supersedes those two lines.
- Review checklist: "Does any number, status or empty message on this page come from somewhere other than a fetch?"
- Coverage report (information, not a gate): new `apps/web/scripts/api-coverage.mjs` lists service operations in `services/*/api/openapi.yaml` that no client in `apps/` calls, grouped by persona workspace, committed with each SDLC run next to `shell/workspaces/navAudit.json`. It is how gaps like the close service (UX-003) and device administration (UX-044) are found before a usability task hits them.

**Resolves:** UX-001 (interim), UX-003 (task route until the first cut), UX-004 (import on the working setup step), UX-005 (read-only sequences on the numbering API), UX-006 (interim), UX-044 (found by the coverage report; the device administration screen and conflict filters ship in rollout slices 12 and 17), UX-049 (demo tour copy). Wiring each page to its real API is in `rollout.md` slices 1, 3, 4, 16 and 17.

---

## UXS-02 Names, not ids

**Rule.** Wherever a person enters or reads a reference to another record, they see and pick it by name and number. Inputs use `EntityLookup` or `LookupFormField`; displayed references use `EntityName` or `UserName`. People never type, paste or read a UUID. User-visible text never contains the word "UUID", "Enter the id of", a dotted duty code (`sales.order.approve`), or a raw problem code outside the `ProblemMessage` "Details" disclosure.

**Why.** Hundreds of fields still ask for UUIDs: "Enter the legal entity id (a UUID)." appears 69 times (UX-009); the drop-ship form asks for eight UUIDs (UX-007); 149 table columns print ids (UX-021). The current guard, `rawIdInputs.test.ts`, matches labels ending in " id", so renaming the label to "Legal entity" while keeping a monospace UUID box passes (UX-008). Thesis U2: "keys shown but never typed".

**Do**
- `sales/NewOrderPage.tsx`: the customer and product are `LookupFormField`s; picking the product fills the price.
- `ap-invoices/InvoicesPage.tsx`: the vendor column renders the vendor's name with `EntityName`, sorts as not sortable and exports the name with `entityText("vendor", id)`.
- An approval message says "Needs the Approve sales orders duty. Ask your administrator." using `dutyName("sales.order.approve")`.

**Don't**
- `fulfillment/DropShipsPage.tsx:41-52`: "Enter the order id as a UUID."
- `sales/OrderProgressPages.tsx:172-175`: a "Legal entity" text box validated with `UUID.test`.
- `planning/RunPlanPage.tsx:378`: "Item ids — UUIDs separated by commas or spaces".
- `admin/security/RoleAssignmentsPage.tsx:172`: the User column shows the raw user id.
- `sales/NewOrderPage.tsx:226`: "Needs the sales.order.approve duty".
- `ledger/JournalBatchPage.tsx:235`: "Enter the id of the ledger the batch posts to." next to a ledger picker.

**Build or reuse**
- Reuse `shell/lookup/EntityLookup.tsx`, `LookupFormField.tsx`, `EntityName.tsx`, `UserName.tsx` and the 70 entity definitions in `shell/lookup/entities.ts`. Add an entity definition when a lookup is missing; do not fall back to a text box.
- Add `multiple` and paste support to `EntityLookup`: a person can paste a column of item numbers and each resolves to a chip, with unresolved lines listed (replaces the planning "Item ids" box).
- Add `shell/shared/dutyName.ts`: duty code to display name, read from the identity service's role catalogue (`/admin/security/roles` already shows duty names), with a test that every duty used in a module `nav` or `handle` has a name.
- Add "Copy id" to `EntityName` (it exists beside names today as the copy-id button) as the only place a raw id appears outside admin and integration screens.

**Acceptance**
1. No plain `Input` in a module is validated with a UUID check (`UUID.test`, `uuidPattern.test`, `isUuid(`). About 800 such checks exist today.
2. No `DataTable` or `ListPage` column whose key ends in `Id` lacks a `render` that shows a name.
3. No user-visible string matches the banned patterns in `shell/plainLanguageRules.ts` (below), except on screens under `modules/integration`, `modules/metadata`, `modules/migration`, `modules/migration-factory` and `modules/admin/security`, which `ux-writing.md` allows to show raw values.
4. In the demo build, no visible text node on any workspace entry route matches the UUID shape `[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`.

Banned patterns added to `shell/plainLanguageRules.ts` (shared by this standard and UXS-07). Two of them match against real catalogues rather than a word shape, because a shape either misses real codes or flags ordinary text:

| Name | How it matches | Must catch (positive fixtures) | Must not catch (negative fixtures) |
|---|---|---|---|
| `UUID_WORD` | `\bUUIDs?\b`, case-insensitive | "Enter the item id as a UUID."; "Enter the legal entity id (a UUID)."; "UUID of the customer party." | "Use the copy button to share the link." |
| `ID_PROMPT` | `\b(?:enter\|type\|paste) the (?:[a-z]+ )*ids?\b`, case-insensitive (zero or more words between "the" and "id" or "ids"). All fixtures in this table were run against the patterns while writing this revision | "Enter the id of the ledger the batch posts to." (`ledger/JournalBatchPage.tsx:235`); "Enter the id of the legal entity the warehouse belongs to." (`fulfillment/WarehouseSettingsPage.tsx:70`); "Enter the order id as a UUID."; "Paste the item ids" | "Enter the identifier printed on the label"; "Type the IBAN"; "Enter the idle timeout" |
| `DUTY_CODE` | Any whole token equal to a duty in the duty catalogue: every key of `duties` in `policies/authz/data.json` (748 today, including two-part codes such as `party.write`, `period.reopen`, `inventory.admin`, and every final verb: `inquire`, `admin`, `run`, `submit`, `self`, `operate`, `create`, `release` and the rest) plus every `duty` value in module `nav` and route `handle`s. The token boundary is "not preceded or followed by a letter, digit, dot or hyphen". As a fallback for codes not yet in the catalogue, a dotted lower-case token (`\b[a-z][a-z-]*(?:\.[a-z][a-z-]*)+\b`) in a string that also contains "duty", "permission" or "role"; the abbreviations `e.g.`, `i.e.` and `etc.` are never tokens (review R-21) | "Needs the sales.order.approve duty"; "the party.write duty" (`party/NewPartyPage.tsx:69`, `party/TaxRegistrationsPage.tsx:135`); "tenant.administer"; "receivables.write-off.approve"; "identity.security-role.maintain" | "e.g. a late count."; "Choose a role, e.g. clerk"; "Contoso Ltd."; "file.csv"; "Ask for the Approve sales orders duty."; "version 1.2" |
| `PROBLEM_CODE` | `\b[A-Z]{2,6}(?:-[A-Z]{2,6})*-\d{3,4}\b`, counted only when the prefix is a known problem-code prefix: the prefixes of the codes in `shell/errors/catalog.ts` and of the codes named in `services/*/api/openapi.yaml` (SF, MOB, TMS, WMS, REPORT, SCHED, AUD, ORG and the rest). Moved here from `shell/help/routeHelp.test.ts` | "(REPORT-0010)"; "(TMS-0106)"; "MOB-0101"; "SCHED-0012" | "PO-1042"; "INV-2026-0001"; "ISO 4217" |
| `RAW_ENUM` | `\b[A-Z]{2,}(?:_[A-Z0-9]+)+\b` | "LOW_CONFIDENCE, TOTAL_MISMATCH, NO_VENDOR_TAX_ID"; "IN_REVIEW" | "IBAN"; "SSCC"; "ISO 4217" |

Every cell in the two fixture columns is a test case in `shell/plainLanguage.test.ts`: positives must be flagged, negatives must not. A pattern change that breaks a fixture fails the build, so a guard cannot quietly stop catching the copy it was written for.

**Enforcement**
- Replace the label-text rule in `shell/lookup/rawIdInputs.test.ts` with two behavioural source scans: (a) a module file that contains a plain `<Input` and a UUID check; (b) a column definition `key: "<x>Id"` without `render`. Keep `rawIdAllowlist.json` as the shrinking allowlist, seeded with today's files.
- Extend `shell/plainLanguage.test.ts` to apply the five patterns to every user-facing string (it already parses string literals and JSX text), with the screen exceptions above. The test loads the duty catalogue from `policies/authz/data.json` and the module registry at run time, so a new duty is banned from user text the day it is added. It also runs the positive and negative fixtures from the table.
- New e2e `e2e/names-not-ids.demo.spec.ts`: visits every workspace entry route in the demo build at 1366 px and fails on a visible UUID-shaped text node (check 4). Text inside the copy-id control is skipped.

**Resolves:** UX-007, UX-008, UX-009, UX-020, UX-021, UX-030, UX-032, UX-033, and the User column of UX-001.

---

## UXS-03 Record detail template with a History panel

**Rule.** Every record detail page (party, product, purchase order, receipt, vendor invoice, sales order, packing slip, customer invoice, production order, worker, journal entry, and every other `*DetailPage`) uses one template: a header, the body, and a side area with **History** and **Related documents**. The History panel reads the audit service's per-record endpoint and shows, for each change: who, when, what changed (field label, old value, new value), why (the reason, when one was captured), and a link to the business event or approval that caused it.

**Why.** There is no shared way to answer "who changed this vendor's bank account, when and why" (UX-002). PO and sales order have bespoke version tables; vendor, customer, AP invoice, product and GL entry pages show nothing. Thesis A7: "field-level immutable audit for every entity". Auditors and clerks need the answer on the record, not in a separate admin tool.

**Template anatomy**

```
+--------------------------------------------------------------------------------+
| Breadcrumb > Record type                                                       |
| PO-1042  Contoso Supplies                    [Status pill]   [Primary] [More v] |
| Waiting on: Ana Ruiz (since 12 Nov, 2 days)   Last change: Ben Ode, 13 Nov 09:12 |
| Why: "Price agreed by phone on 12 Nov" (reason on the last change, if any)     |
+-------------------------------------------------+------------------------------+
| Body: sections in task order                    | [History] [Related] [Files]  |
|  - summary fields                               | 13 Nov 09:12  Ben Ode        |
|  - lines (LineGrid, UXS-05)                     |  Unit price, line 2          |
|  - totals                                       |  12.50 -> 11.90              |
|                                                 |  Why: price agreed by phone  |
|                                                 |  From: Purchase order changed|
|                                                 |  (version 3)  [View event]   |
|                                                 | 12 Nov 16:40  Ana Ruiz       |
|                                                 |  Status: Draft -> Submitted  |
|                                                 | ...  [Export history]        |
+-------------------------------------------------+------------------------------+
```

Below 960 px the side area becomes tabs under the body. The History panel lists newest first, groups field changes of one save into one entry, and loads 50 entries at a time with "Load more changes".

**History entry content**

| Part | Source (audit `Record`) | Shown as |
|---|---|---|
| Who | `userId` (always the verified caller, AUD-API-1), plus `actorKind` and `onBehalfOf` (new) | A person acting directly: `UserName`. A service recording a change it made for a person: "Recorded by Billing service for Ana Ruiz", so a reader can tell a verified person from an identity a service passed on. A service acting on its own: "System (nightly reconciliation)". An agent: "Assistant for Ana Ruiz" |
| When | `occurredAt` | `formatDateTime` in the person's time zone with the zone abbreviation ("13 Nov 2026, 09:12 CET"), and the exact UTC time in the `title` |
| What | `field`, `oldValue`, `newValue` | Field label from the entity's metadata (`shell/metadata.ts`) or the panel's label map; values formatted by type (money, date, reference as `EntityName`). Sensitive fields (bank account, tax id) show masked values (last 4) |
| Why | `reason` (new) | The reason text, or "No reason was needed for this change" |
| Caused by | `eventType`, `eventId`, `workflowInstanceId` (new) | A plain event name ("Purchase order changed") linking to the related document or approval |
| Integrity | `hash` | Not shown per row; the export states whether the chain verified |

**States the panel must show (and nothing else):**

| State | Text |
|---|---|
| Loading | Skeleton rows, `aria-busy` |
| Not recorded | "Change history isn't recorded for {record type} yet." (the entity is not in the audit service's audited-entity list) |
| Recorded, no changes since creation | "No changes since {person} created this on {date}." |
| No access | "You don't have access to this record's history." |
| Error | `ProblemMessage` with "Try again" |

The "Not recorded" state exists because today only `services/engineering` sends changes to the audit service (`docs/sdlc/run-1/architecture-delta.md`, decision D3). The panel must not show "No changes" for an entity whose changes are not captured: that would break UXS-01.

**Do**
- `sales/OrderDetailPage.tsx` keeps its `OrderHistory` versions as a second source in the same panel (`sources={[auditSource, orderVersionSource]}`), labelled "Order versions".
- A vendor's bank account change shows "IBAN ••••4821 -> ••••7710, Why: vendor letter dated 3 Nov, Approved by Ana Ruiz".

**Don't**
- A bespoke "Versions" table per module (`purchasing/PurchaseOrderPages.tsx:1370`) as the only history.
- Field names from the database (`bank_account_iban`) or user ids in the panel.

**Build or reuse**
- New `shell/ui/RecordDetail.tsx` (template: header, body, side area).
- New `shell/audit/RecordHistory.tsx`, `shell/audit/api.ts` (client for `/api/v1/audit/records/{entity}/{recordId}`), `shell/audit/fieldLabels.ts`, `shell/audit/historySources.ts` (adapter type so module version endpoints can feed the panel).
- Reuse `shell/documents/DocumentFlow.tsx` for the Related tab, `UserName`, `EntityName`, `StatusPill`, `formatDateTime`.
- Entity keys: use `RECORD_TYPES` in `shell/search/records.ts` as the starting list.

**Audit API needs** (for the stage 5 backlog; `services/audit/api/openapi.yaml` today has `Record` = entity, recordId, field, oldValue, newValue, userId, traceId, occurredAt, hash):

| Id | Need | Why the UI needs it |
|---|---|---|
| AUD-API-1 | Add `reason`, `changeSetId`, `eventType`, `eventId`, `workflowInstanceId`, `actorKind` (person, service, agent), `onBehalfOf` to `Change` and `Record`. **The actor is the verified principal of the request**: a body `userId` is refused (as `POST /v1/audit/reads` already refuses it with AUD-0002); today `services/audit/internal/api/api.go` uses the verified principal only when the body's `userId` is empty. `onBehalfOf` is accepted only from a verified service principal that carries propagated user context (architecture delta D3, "actor propagation"), and is stored with `actorKind = service` | Why, grouping one save, link to the business event, and a "who" that is verified rather than asserted |
| AUD-API-2 | `GET /v1/audit/records` with filters `entity`, `recordId`, `userId`, `field`, `from`, `to`, plus `limit` and `cursor`; `limit`, `cursor` and a newest-first `order` on the per-record route too | The cross-entity audit page (`/admin/audit`) and "50 at a time" in the History panel |
| AUD-API-3 | `GET /v1/audit/records/{entity}/{recordId}:export?format=csv\|ndjson` with chain-verification headers and completion trailers, mirroring `reads:export`. Columns, in order: `seq`, `occurredAt` (UTC, RFC 3339), `actorId`, `actorName`, `actorKind`, `onBehalfOf`, `entity`, `recordId`, `recordName`, `fieldKey`, `fieldLabel`, `oldValue`, `newValue` (masked as for the caller), `reason`, `eventType`, `eventId`, `workflowInstanceId`, `prevHash`, `hash`. Each export is itself recorded in the trail (who exported which record and when) | Export of a record's trail that an auditor can recompute (UXS-13) |
| AUD-API-4 | `GET /v1/audit/records:verify` | The export and audit page can say "verified" |
| AUD-API-5 | `GET /v1/audit/entities`: entity types that feed the chain | The "Not recorded" state |
| AUD-API-6 | Authorize the per-record route by the record's own read duty, with field masking, not only tenant-wide `audit.read` | Clerks see history on records they can open; legal-entity scoping (architecture delta D17) |
| AUD-API-7 | Ingestion coverage: changes from every service reach the chain (architecture delta D3). The first, blocking set is the A7 set below | Without it the panel says "Not recorded" for most entities |
| AUD-API-8 | Viewing the History of a record marked sensitive, and revealing a masked value, are recorded (the PHI read log pattern, `POST /v1/audit/reads`, extended beyond PHI) | Auditors can see who looked, not only who changed |
| AUD-API-9 | Retention, legal hold and erasure of personal data in an append-only trail (for example, keep the hash and erase the value with a tombstone entry) | An architecture decision for stage 5; the UI shows "Value removed under the retention policy on {date}" for an erased value |

**The A7 set: proven on real services, not the demo.** The owners' auditability ask is met only when these changes reach the chain end to end from the services that own them, with a verified who, when, old and new value, and reason:

| Entity and field | Owning service | Change path |
|---|---|---|
| Vendor bank account (add, change, remove) | `services/party` | `/v1/party/vendors/{id}/bank-accounts` |
| Customer credit limit | `services/party` | `/v1/party/customers/{id}/credit-limit` |
| Vendor and customer payment terms | `services/party` | vendor and customer role updates |
| Product price (sales price list) and standard cost | `services/pricing`, `services/costing` | price list and cost version activation |
| User role assignment (assign, end) | `services/identity` | role assignments |
| AP invoice (post, hold, release, match override) | `services/receiving` | `/v1/receiving/invoices` |
| Journal entry (post, reverse) | `services/ledger` | journal batches and entries |

Until all seven rows pass their contract test (Enforcement), milestone M2 in `rollout.md` is not met, whatever the demo build shows. `/admin/audit` shows the audited-entity list (AUD-API-5) as "Recorded" and "Not recorded yet" so auditors can see the coverage.

**Acceptance**
1. Every `*DetailPage.tsx` renders `RecordDetail` (or `RecordHistory` directly).
2. `RecordHistory` shows the five parts of an entry and only the five states above.
3. The panel works by keyboard, its list is a labelled region, and "Load more changes" keeps focus position.
4. Values of fields marked sensitive are masked; revealing one is itself audited (architecture delta "audited-reveal", AUD-API-8).
5. Every row of the A7 set shows in the History panel of a real (non-demo) environment with a verified actor, old and new value and reason.
6. Entries whose actor was passed on by a service are labelled differently from entries made directly by a person.

**Enforcement**
- Extend `shell/ui/consistency.test.ts` with rule 4: every `*DetailPage.tsx` in `modules/` uses `RecordDetail` or `RecordHistory`; shrinking allowlist `shell/ui/detailPageBacklog.json` (about 100 files today).
- New `shell/audit/RecordHistory.test.tsx`: each state, entry rendering with names and labels, masking, the in-repo AA checks (`checkAxeAA`).
- New e2e `e2e/record-history.demo.spec.ts`: in the demo build change a vendor's credit limit with a reason, open History, assert who, when, old to new, reason and event link. This proves the UI only.
- New contract tests per owning service for the A7 set (for example `services/party/internal/api/audit_contract_test.go`, and the same in `identity`, `pricing`, `costing`, `receiving`, `ledger`): make the change through the public route as a verified person with a reason, then read `GET /v1/audit/records/{entity}/{id}` and assert actor, field, old, new and reason. They run in the existing Go test job with the audit service in the test harness.
- New contract test in `services/audit`: a `POST /v1/audit/changes` whose body names a `userId` is refused; `onBehalfOf` from a person's token is refused; from a verified service principal it is stored with `actorKind = service`.
- New `e2e/record-history.live.spec.ts` (the `login.live.spec.ts` pattern, run against a deployed environment before a milestone is declared): one A7 change per row, seen in the History panel.

**Resolves:** UX-002, UX-001 (with UXS-13). Thesis A7, U1 ("full document on one page"). Depends on architecture gap D3 (audit ingestion) for the A7 set; that dependency blocks milestone M2.

---

## UXS-04 List pages: selection, bulk actions, paste, undo

**Rule.** Every list of records uses `ListPage`. Where people act on more than one row, the list offers row selection and a bulk-action bar. Where people edit many rows, it offers inline edit of the editable columns, paste from a spreadsheet with a validation preview, and undo of the last bulk change. Saved views are stored on the server and can be shared and set as a role default.

**Why.** The shared list has no selection, bulk bar, inline edit, paste or undo; twelve modules hand-roll selection; 674 module files render `DataTable` directly against 85 on `ListPage` (UX-016, UX-017). An AP clerk cannot pay two chosen invoices (UX-015). Saved views live in browser storage and are lost on a new device (UX-026). Thesis U7 ("every list page supports multi-row inline edit, paste from spreadsheet with validation preview, and undo") and U3 (shareable, role-defaulted views).

**Behaviour**
- **Selection.** A checkbox column; Shift+click selects a range; a header checkbox selects the rows loaded. The bar says "3 selected" and, when more rows exist, offers "Select all 240 matching the filters" (the bulk action then runs on the server by filter, not by a client id list).
- **Bulk-action bar.** Appears above the table when one or more rows are selected; holds the actions the page passes (`bulkActions`), each with a verb from `ux-writing.md` ("Pay selected", "Send reminders", "Release selected"). An action that needs a reason opens `ReasonDialog` (UXS-12). The result lists what succeeded and what failed per row, in plain words, with "Show failed rows" filtering the list to them.
- **Inline edit.** Columns marked `editable` turn into fields on "Edit rows". Arrow keys move between cells; Enter saves the row; Escape cancels the cell. Validation shows at the cell (UXS-08).
- **Paste.** "Paste rows" opens a dialog: paste from Excel or Sheets (tab-separated); the first row may be headers, which are matched to columns by label; references (vendor, product) resolve by number or name through the lookup service. A preview lists each row as Will add / Will change / Error (with the reason in the row). Nothing is saved until "Apply N rows". Rows with errors can be fixed in the preview or skipped.
- **Undo.** After a bulk change or paste, a status message offers "Undo" for 30 seconds and the change stays in the page's undo stack until the person leaves the page. Undo sends the reverse change (for posted documents it is not offered; posting is reversed with Reverse, per `ux-writing.md`).
- **Views.** `PreferenceStorage` gets a server implementation (the seam in `shell/preferenceStorage.ts` exists); the Views menu adds "Share with…" and, for people holding the admin duty, "Set as default for role".

**Do**
- `/payables/open-items`: filter by vendor, tick two invoices, "Pay selected", confirm: one payment covers both (AP4).
- `/receivables/open-items`: select overdue items, "Send reminders" (UX-016).
- `/planning/planned-orders`: select, "Release selected" replaces the bespoke selection in `planning/Firming.tsx`.
- `/inventory/count-sheets/:id`: counts typed into the grid, one "Save counts" for the sheet (UX-031).

**Don't**
- A proposal that "selects every vendor invoice that is due" with only whole-vendor exclusion (`payables/NewProposalPage.tsx:110-140`) as the only way to pay.
- One `size="sm"` Save button per row (`inventory/CountSheetPage.tsx:52-61`).
- A module's own filter bar ("Show jobs", "Filter") above a raw `DataTable`.

**Build or reuse**
- Extend `shell/ui/ListPage.tsx`: props `selection`, `bulkActions`, `editableColumns`, `onSaveRows`, `paste` (column mapping and a `validateRows` hook), `undo`.
- New `shell/ui/PasteRowsDialog.tsx`, `shell/ui/BulkActionBar.tsx`, `shell/ui/useUndo.ts`.
- New server-backed `PreferenceStorage` (`shell/preferenceStorage.ts`) over a preferences API (backlog item; the identity or metadata service hosts it).
- Reuse `shell/ui/csv.ts` for parsing (it already neutralises formula cells on export; the same rule applies on paste).

**Acceptance**
1. Every list screen in a workspace uses `ListPage`.
2. A list whose page holds a multi-record action offers it as a bulk action, not by per-row buttons only.
3. Paste shows a preview and saves nothing until confirmed; every error row says what is wrong and how to fix it.
4. Undo restores the previous values for non-posted changes.
5. All of it works by keyboard and is announced (selection count in the polite live region).

**Enforcement**
- Extend `shell/ui/ListPage.test.tsx` with selection, range select, bulk bar, paste preview (valid, invalid, unknown reference), undo and keyboard cases.
- Extend `shell/ui/consistency.test.ts` rule 2 from the eight procure-to-pay and order-to-cash modules to all modules; seed `shell/ui/listPageBacklog.json` (empty today) with the unconverted list pages, ordered as in `rollout.md`.
- New source scan in `consistency.test.ts`: a module file that keeps its own selection state (`Set<string>` state plus a checkbox inside a `DataTable` column) is listed in `shell/ui/handRolledSelection.json` (the twelve known files) and must leave it when converted.
- Extend `e2e/payables.demo.spec.ts` with AP4 (pay two chosen invoices in one payment).

**As built in `ListPage` (selection, bulk, paste, undo)**
- `bulk: BulkAction[]` adds a checkbox column (`DataTable` `selection`) and the bulk bar. An action is `{ id, label, run(rows), confirm?, undo?(done) }`; `run` gets only the selected rows that are still shown and resolves `{ done: string[], failed: { key, reason }[] }`. The page never calls an endpoint.
- Select all selects the rows shown (after search and filter). Failed keys stay selected so a retry targets only them; a rejected `run` shows the problem and keeps the selection.
- `paste: { fields, onCommit, undo? }` adds "Paste rows". The dialog parses tab-separated text (a header row is skipped, 500 rows at most), previews every row with its problem, and applies nothing while a row is invalid.
- `undo` is offered once after a run that changed rows and cleared when the selection changes. `rowLabel(row)` names each checkbox and failure line; it defaults to the row key.
- Server `PreferenceStorage`, shared and role-default views, and the module bulk actions are later slices of B-28.

**Resolves:** UX-015, UX-016, UX-017, UX-026, UX-031 (web half), UX-032 (paste of item numbers, with UXS-02).

---

## UXS-05 Keyboard line grid that starts from a source document

**Rule.** Every document with lines (sales order, purchase order, requisition, vendor invoice, receipt, free-text invoice, journal batch, transfer order, count sheet) edits its lines in one shared keyboard grid. A document that follows another document starts from it: "Start from purchase order", "Start from receipt", "Start from sales order line". Starting from a source loads all its open lines with quantity, price, unit, currency and dimensions filled in; the person changes only what differs.

**Why.** Lines are stacked fieldsets: a five-line order is 30+ Tab stops (UX-050). Posting an invoice cold means picking a receipt per line, typing the receipt line number, quantity, price and ticking "standard cost" (UX-014). Receiving takes three screens and one PO line at a time (UX-028). A drop-ship asks for eight UUIDs instead of starting from the order line (UX-007). Thesis U1: "line grids with inline edit; the full document on one scrollable page"; thesis "Easier" axis (one tenth of the clicks).

**Keyboard map** (shown in the grid's help and in the Help panel)

| Key | Action |
|---|---|
| Arrow keys | Move between cells (in a text cell being edited, Left and Right move the caret) |
| Enter | Edit the cell; Enter again commits and moves down. Enter on the last row's last cell, when that row is complete, adds a line |
| Tab / Shift+Tab | Next / previous editable cell, wrapping to the next row |
| Ctrl+Enter (Cmd+Enter) | Add a line below the current row |
| Shift+F10 or the Menu key | Open the row's actions: Fill down from the row above, Duplicate line, Remove line (with undo) |
| Ctrl+S (Cmd+S) | Save the document (the page's Save button does the same; the browser's own save-page is suppressed only while focus is inside the form) |
| Ctrl+Shift+Enter | Submit, post or confirm (the page's primary action), after the error summary is clear |
| Escape | Cancel the cell edit; a second Escape leaves the grid to the next control |

Every action also has a visible button or menu item, so no one depends on a chord. Not used, because screen readers or browsers own them: Insert and Caps Lock (the NVDA and JAWS modifier keys), Ctrl+Option (VoiceOver), single-character keys (WCAG 2.1.4), Ctrl+D and Ctrl+Shift+D (bookmarks), Ctrl+L, Ctrl+T, Ctrl+W, Ctrl+N, Ctrl+P, Ctrl+F, F6 and F7. Product and account cells are `EntityLookup` cells; picking a product fills unit, price and tax from the existing price logic (`pickProductPatch` in `shell/lookup/entities.ts`).

**Start from a source document**

```
[Post vendor invoice]
  Start from:  (o) Receipt  ( ) Purchase order  ( ) Nothing (free lines)
  [Receipt lookup: "RC-2209  Contoso, 14 Nov"]  -> loads 4 open lines
  Line grid: product | received | invoiced qty (= received) | unit price (= PO price) | amount
  Vendor invoice number [______]  Invoice date [14 Nov]  Total on invoice [_____]
  Difference: 0.00                                  [Save draft]  [Post invoice]
```

- The costing method decides price-variance handling; the "standard cost" checkbox goes away (UX-014).
- Currency comes from the source document (UXS-06).
- Receiving from a PO shows all open lines with quantity defaulted to the open quantity and location from put-away rules; "Post receipt" posts in the same step (UX-028).
- A drop-ship starts from the sales order line ("Ship from vendor") with customer, product, quantity and address carried over (UX-007).

**Do**
- `/ap-invoices/post?receipt=<id>` already pre-fills when arriving from GR/IR; make that the page's own first step.
- The sales order page lets an order desk user add two products and quantities with the keyboard only.

**Don't**
- `sales/LinesEditor.tsx:33` "one fieldset per line".
- `ap-invoices/PostInvoicePage.tsx:229-262` per-line receipt, receipt line number and standard-cost checkbox.
- `receiving/NewArrivalPage.tsx`: currency, over-delivery percent and batch/serial-tracked flags asked per line although the PO and product hold them.

**Build or reuse**
- New `shell/ui/LineGrid.tsx` (ARIA grid pattern: `role="grid"`, `aria-rowindex`, roving tabindex) and `shell/ui/LineGrid.test.tsx`.
- New `shell/documents/SourcePicker.tsx` ("Start from").
- Reuse `shell/documents/GuidedFlow.tsx` and `useFlow.ts`, which already carry a PO into a receipt and a receipt into an invoice in the guided flows.

**Acceptance**
1. The listed documents edit lines in `LineGrid`.
2. A person can enter a five-line sales order with the keyboard only in at most 20 interactions: 1 customer + 3 per line (product, quantity, Enter) + 1 confirm, plus up to 3 header changes. Budgets per task live in `personas-and-journeys.md` until `e2e/taskBudgets.json` exists; then that file is the single source.
3. Starting from a source loads every open line with defaults; nothing the source holds is asked again.

**Enforcement**
- `shell/ui/LineGrid.test.tsx`: the keyboard map, add/remove/undo, lookup cells, error per cell, AA checks. The map lives in `shell/ui/lineGridKeymap.ts`; a test fails if it binds any key in the denylist above.
- Manual assistive-technology pass on the grid before it ships (UXS-15): NVDA with Firefox, JAWS with Chrome, VoiceOver with Safari, keyboard only, 400% zoom.
- Extend `e2e/p2p-keyboard.demo.spec.ts`: a five-line sales order and a vendor invoice from a receipt, keyboard only, with interaction counts asserted against `personas-and-journeys.md` budgets.
- New source scan in `consistency.test.ts`: the document pages above render `LineGrid`; shrinking allowlist `shell/ui/lineGridBacklog.json`.

**Resolves:** UX-050, UX-014, UX-028, UX-007 (with UXS-02).

---

## UXS-06 Context defaults

**Rule.** Forms never hard-code a legal entity, site, ledger or currency. They read defaults from one hook, `useDefaults`, which resolves in this order: the source document (UXS-05), then the party (vendor's or customer's currency, payment terms, invoice account), then the person's work context (legal entity and site in the top bar), then the legal entity's settings (accounting currency, ledger, fiscal calendar). When a person changes a defaulted value to something unusual (a currency other than the party's), the form shows a warning, not an error.

**Why.** 80 module forms default `currency: "USD"`; the demo company has a US and two euro legal entities, so wrong-currency documents are easy to create (UX-034). "Enter the id of the ledger" appears next to a ledger picker that could default (UX-033). Thesis U1 and the ACR's claim for WCAG 3.3.7 (redundant entry) depend on it.

**Do**
- New sales order for a German customer: currency EUR, payment terms and invoice account from the customer; legal entity from the top bar.
- New journal batch: ledger from the legal entity; the person never picks it unless the entity has more than one.

**Don't**
- `sales/NewOrderPage.tsx:107`, `receiving/NewArrivalPage.tsx:69`, `ap-invoices/PostInvoicePage.tsx:56`: `currency: "USD"`.
- A free-text three-letter currency box ("ISO 4217, such as USD"). Use the `currency` lookup.

**Build or reuse**
- New `shell/defaults/useDefaults.ts` and `useDefaults.test.ts`, built on `shell/lookup/workContext.tsx` (legal entity, site) and the existing lookups for party and legal entity.
- The `currency` entity in `shell/lookup/entities.ts`.

**Acceptance**
1. No module form initialises a currency, legal entity, site or ledger from a literal.
2. Changing the party re-applies party defaults to fields the person has not edited (the `useFormBaseline` freeze rule in `forms.md` decides which fields count as edited).
3. A currency different from the party's shows "This customer is usually invoiced in EUR." under the field.

**Enforcement**
- New source scan `shell/defaults/noLiteralDefaults.test.ts`: flags `currency: "<AAA>"`, and `legalEntityId`, `siteId` or `ledgerId` set to a non-empty literal, in module form state; shrinking allowlist seeded with today's files (80 for currency).
- `useDefaults.test.ts` covers the resolution order.

**Resolves:** UX-034, UX-033 (ledger), parts of UX-014 and UX-028.

---

## UXS-07 Error, status and number text

**Rule.** Every message says what happened and what to do in plain words first; a code, if any, comes second and only inside the `ProblemMessage` "Details" disclosure. Every status is shown through `StatusPill` or a label map, never as a raw enum. Every date, time, amount and quantity is formatted by one set of formatters. The same formatters, status labels, problem catalog and field-error mapping are used by the web app, the floor apps and both portals.

**Why.** Titles and cells carry codes ("(REPORT-0010)", "(TMS-0106)", "SCHED-0012"; UX-019); badges print "finished", "started" (UX-022); the supplier portal shows `LOW_CONFIDENCE, TOTAL_MISMATCH, NO_VENDOR_TAX_ID` (UX-037); portals show "1234.5 USD" and ISO dates (UX-035); the technician header shows "2026-11-17" (UX-045). Thesis U5: "validation messages are metadata with user text, remediation link and the exact field".

**Do**
- "The quantity is more than the purchase order line allows. Receive no more than the open quantity plus its tolerance." with Details: `RCV-0031`, request id.
- Supplier portal invoice review: "The total does not match the sum of the lines. Check the lines and send the invoice again."
- Technician header: "Jordan Avery, Tuesday 17 November".
- Customer portal: "$1,234.50".

**Don't**
- `finreport/RunPage.tsx:92` "(REPORT-0010)" in a page description.
- `mobile-flows/DeviceApp.tsx:249` and `ClusterPanel.tsx:102`: codes in device notices.
- `apps/customer-portal/src/ui.tsx:82-87`: `iso.slice(0, 10)` and `${amount} ${currency}`.
- `production/JobsPage.tsx:38`: a badge printing the raw status.

**Build or reuse**
- Reuse `shell/errors/ProblemMessage.tsx`, `catalog.ts`, `problem.ts`; `shell/ui/status.ts` (`statusTone`, `standardLabel`); `shell/format.ts`.
- New `shell/shared/` folder holding the portable, framework-light parts: `format.ts`, `status.ts`, `problem.ts`, `catalog.ts`, `fieldErrors.ts` (server `errors` map to labelled field messages), `dutyName.ts`. `shell/shared/` imports nothing outside itself, so the portals and floor apps can import it by relative path the way `apps/technician/src/App.tsx` imports the technician module today. A later `@depot/ui` package can lift the folder unchanged.
- Floor devices show the plain message large and log the code; the code is reachable under "Details" for a supervisor.

**Acceptance**
1. No user-visible string matches `PROBLEM_CODE` or `RAW_ENUM` (UXS-02 table) outside `ProblemMessage` Details and the admin/integration screens.
2. Every status rendering goes through `StatusPill` or `statusLabel`.
3. No module or portal formats a date with `slice`, `toISOString` or string building, or an amount with concatenation, `toFixed` or `toLocaleString`.
4. Every problem code a screen can hit has a catalog entry with `message` and `action` (already checked for P2P/O2C by `catalogCoverage.test.ts`).
5. Portals render server field errors at the field with an error summary (with UXS-08), never as `camelKey: message`.

**Enforcement**
- Extend `shell/plainLanguage.test.ts` (patterns from UXS-02) and `shell/errors/bareCode.test.ts` (bare `{problem.code}` renders) to all modules.
- Extend `shell/errors/catalogCoverage.test.ts` from the flow modules to every module that calls a service.
- New source scan `shell/shared/formatting.test.ts`: banned formatting idioms in `modules/**`, `apps/customer-portal/src/**`, `apps/supplier-portal/src/**`, `apps/warehouse-mobile/src/**`; shrinking allowlist.
- New `shell/shared/boundary.test.ts`: `shell/shared/**` imports only from itself.
- Extend `apps/customer-portal/test/portal.test.tsx` and `apps/supplier-portal/test/portal.test.tsx`: amounts, dates, status labels, review reasons and inline field errors.
- Extend `modules/technician/technicianApp.test.tsx`: header date.

**Resolves:** UX-019, UX-020 (with `dutyName`), UX-022, UX-035, UX-036 (with UXS-08), UX-037, UX-045, part of UX-048.

---

## UXS-08 Forms: validate early, never lose work

**Rule.** Every create or edit form in every module follows `apps/web/docs/forms.md` (error summary, required/optional marks, unsaved-changes guard) and also: format rules (required, number, date, length, pattern) are checked when the person leaves the field and while they correct a field already in error; server rules are checked on submit and shown at the field. The session warns two minutes before it expires and offers "Stay signed in"; a form's draft survives a forced sign-in.

**Why.** 713 module files contain a form; 57 use the error summary and unsaved guard; the consistency test only checks P2P/O2C create pages (UX-018). Errors appear only after submit, against the thesis U5 promise "errors shown at the field as you type" (UX-040). The session can end with no warning and drafts are lost (UX-041, ACR 2.2.1). Portals join field errors into one notice (UX-036).

**Do**
- `receivables/NewPaymentPage.tsx`: error summary and unsaved guard (already on the standard).
- Quantity field: typing "12x" and leaving shows "Enter a number, for example 12" at once.

**Don't**
- A shop-floor or ledger form that shows nothing until submit and loses input on navigation.
- An error shown while the person is still typing their first value in an empty field (validate on blur, then live while correcting).

**Build or reuse**
- Reuse `FormErrorSummary`, `summaryItems`, `useSubmitAttempt`, `useUnsavedChanges`, `useFormBaseline` (`shell/ui`).
- New `shell/ui/useFieldValidation.ts`: per-field rules, blur-then-live timing, feeds `FormField error`.
- New `shell/auth/SessionExpiryWarning.tsx`: reads token expiry from `shell/auth/auth.ts`; dialog with "Stay signed in" and "Sign out"; drafts saved to session storage per route before a forced sign-in and restored after.
- Portals use `shell/shared/fieldErrors.ts` (UXS-07).

**Acceptance**
1. Every module page exported as `New…Page`, `…CreatePage`, `Edit…Page` or containing a `<form>` that saves a record renders `FormErrorSummary` and calls `useUnsavedChanges`.
2. Format errors appear on blur; the summary lists them on submit.
3. The expiry warning appears at T-2 min, is announced, and extending keeps all input.

**Enforcement**
- Extend `shell/ui/consistency.test.ts` rule 3 from `FLOW_MODULES` to all modules and to `Edit…Page`; shrinking allowlist `shell/ui/formStandardBacklog.json`.
- Extend `shell/ui/forms.test.tsx` with blur and live-correction cases.
- New `shell/auth/SessionExpiryWarning.test.tsx` with fake timers.
- Update the ACR row 2.2.1 when shipped.

**Resolves:** UX-018, UX-040, UX-041, UX-036 (with UXS-07).

---

## UXS-09 Floor layout: kiosk and warehouse device

**Rule.** Screens used on the shop floor and in the warehouse (the kiosk, the warehouse device app, and the technician app) run full-screen in a floor layout: no sidebar, no work-context pickers, no Compact view. Work is directed: each step shows where to go, what to handle and how many, in large type; one scan of the expected value completes the step; quantity defaults to the expected quantity. Targets are at least 48 px (64 px in gloves mode). Every action works offline through a queue whose state is always visible. Sign-in is by badge or PIN on an enrolled device, and the device signs the person out when idle.

**Why.** The kiosk is a page inside the desktop shell with legal-entity and site pickers on a shared device (UX-011); device prompts say "Scan the pick location" without saying which (UX-010); the cart is built by typing `workId,orderId,LP` lines; the device has no sign-in and records work as "unassigned"; scrap reasons are free text (UX-029); counts need one small Save per line (UX-031); prompts use "LP" (UX-043). Thesis U6: "touch-first PWA kiosk and scanner apps with offline queues; one scan completes a step". The technician app (`modules/technician/TechnicianApp.tsx`) already meets most of this and is the model.

**Do / Don't**, with the full design, states, scan grammar and sign-in, are in [floor-apps.md](floor-apps.md).

**Build or reuse**
- New `shell/floor/FloorLayout.tsx`, `ScanField.tsx`, `BigButton.tsx`, `ReasonButtons.tsx`, `QueueStatus.tsx`, `floor.css` (tokens from `theme.css`; sizes from `modules/mobile-flows/domain/layout.ts`).
- Reuse `modules/shopfloor/KioskJobs.tsx`, `KioskPiList.tsx`, `KioskSyncStatus.tsx`, `offlineQueue.ts`; `modules/mobile-flows/domain/{scanRouting,gs1,flowRunner,offlineQueue,sessionKeeper,exceptions,layout}.ts`; the technician app's header and status pattern.
- The technician app stays the reference implementation and is not rebuilt on `shell/floor/`. When the floor layout ships (rollout slice 8) the technician app takes its tokens and `QueueStatus` component only, so the three apps look and behave alike; its screens do not move.

**Acceptance** (summary; floor-apps.md section 9 has the full list)
1. `/kiosk` and the warehouse device render no element of the desktop shell (no `navigation "Workspaces"`, no context picker, no Compact view).
2. Every directed step shows location, item and quantity before the scan; a matching scan completes it with no tap when the quantity equals the expected quantity.
3. No control under 48 px tall; body text at least 16 px; no horizontal scroll at 320 px.
4. Offline: actions queue, the queue count and limit are visible, and replay results are shown in plain words.
5. Scrap, downtime and short-pick reasons are buttons from the reason master, not free text.

**Enforcement**
- New e2e `e2e/floor-layout.spec.ts` (Playwright Pixel 5 and an 1280x800 touch kiosk profile): checks 1 and 3 on `/kiosk`, `/kiosk/jobs/:id`, the device menu and each device flow step.
- Extend `e2e/warehouse-mobile.spec.ts`: directed pick shows location, item, quantity; one scan per step when quantities match; cart built from assigned work.
- Extend `e2e/kiosk-labor.spec.ts` and `e2e/kiosk-offline.spec.ts` to the new route.
- Extend `modules/mobile-flows/domain/coreFlows.test.ts`: every scan step's prompt names its expected value (`{location}`, `{item}`, `{quantity}` placeholders filled from the work line).
- Extend `modules/mobile-flows/domain/layout.test.ts` to assert the CSS custom properties match the constants.

**Resolves:** UX-010, UX-011, UX-029, UX-030 (kiosk part), UX-031 (device part), UX-043.

---

## UXS-10 Responsive shell

**Rule.** Every shell page works at 390 CSS px wide (a phone) and reflows at 320 px with no horizontal page scroll (WCAG 1.4.10). Below 640 px the top bar shows the menu button, the page title and one "Context" button that opens a sheet holding legal entity, site, Compact view, Help and saved views. Wide data tables scroll inside their own labelled region; the page itself never scrolls sideways.

**Why.** At 390 px every shell page is 481 px wide; the site picker and Help are off-screen (UX-012, measured on `/shopfloor/kiosk`, `/receiving/receipts/new`, `/shopfloor/jobs`, `/inventory/count-sheets`, `/fulfillment/workbench`). The ACR lists 1.4.10 as "Partially supports" for other reasons only.

**Do / Don't**
- Do: the technician app at 390 px (`ux-scratch/shots/m_technician.png`): no overflow.
- Don't: `shell/TopBar.tsx:54` and `shell/Shell.module.css:326` placing the context pickers and Help in one non-wrapping row (`ux-scratch/shots/m_shopfloor_kiosk.png`).

**Build or reuse**
- Change `shell/TopBar.tsx` and `Shell.module.css`; new `shell/ContextSheet.tsx` (a dialog using `useFocusTrap`). CSS-only breakpoints, per design-system rule R6.

**Acceptance**
1. `document.documentElement.scrollWidth <= clientWidth` at 390 and 320 px on every workspace entry route.
2. Help, legal entity and site are reachable at 390 px within two taps.
3. Focus order and the Help button position stay consistent (WCAG 3.2.6).

**Enforcement**
- New e2e `e2e/reflow.demo.spec.ts`: every workspace entry route at 390x844 and 320x640 (touch); fails on horizontal overflow or Help not in the viewport after opening the Context sheet. It uses the same overflow check as `e2e/tablet-layout.demo.spec.ts`.
- Correct the ACR row 1.4.10 to cite the 390 px overflow until this ships.

**Resolves:** UX-012.

---

## UXS-11 Help on every route

**Rule.** Every route has help: at least a generated entry (the page's purpose and its primary actions, from route metadata), and for each persona's top five tasks a hand-written entry naming the steps. Guides open inside Depot, not on GitHub. Every persona has a role guide. User guides never contradict the screens.

**Why.** The Help panel says "There is no help for this page yet." on manufacturing, warehouse, ledger, shop floor, planning, HR and admin pages: `shell/help/routeHelp.ts` has 151 entries for 1,274 routes (UX-023). Guide links go to `github.com/.../blob/main`, which a customer may not be able to open. Seven personas have no guide (UX-024). Two guides describe a "location ID" field and a sequences page that is a placeholder (UX-046). Thesis "Easier": a new user completes a guided task in under 30 minutes.

**Do / Don't**
- Do: `/purchasing/requisitions/new` help ("Choose the legal entity and currency. Select Add line…") with a guide link to `buyer.md#ask-for-what-you-need`.
- Don't: "There is no help for this page yet." (`shell/help/HelpPanel.tsx:10`).

**Build or reuse**
- New `shell/help/defaultHelp.ts`: builds an entry from the route's `handle.title`, the page's `PageHeader` description and its primary actions (declared in a new optional `handle.actions` list).
- New route `/help/guides/:guide` (`shell/help/GuidePage.tsx`) rendering `docs/user/*.md` bundled at build time (the routeHelp test already imports them with `import.meta.glob`). `VITE_USER_GUIDE_BASE` stays as an override.
- New guides in `docs/user/`: operator, warehouse worker, controller, production planner, technician, customer portal, supplier portal (and HR, supervisor as secondary).

**Acceptance**
1. No route resolves to "no help". A generated entry counts only if it has a purpose sentence taken from the page description and at least one action; a title alone does not.
2. Every workspace's top-five task routes have a curated entry.
3. Every guide link resolves to a heading that exists (the routeHelp test already checks headings for flow modules).
4. Each persona in `personas-and-journeys.md` has a guide.

**Enforcement**
- Extend `shell/help/routeHelp.test.ts` from `FLOW_MODULES` to all modules: every route resolves (generated or curated) to an entry with a purpose and at least one action; curated entries exist for the task routes in `apps/web/docs/usability-tasks.md`; plain-language patterns apply to help text.
- Extend `shell/help/HelpPanel.test.tsx`: guide links are in-app.
- New `docs/user` check in the same test: each persona has a guide file; guides do not mention "ID" for fields that are lookups (a word list kept with the test).
- Release checklist line: "Guides changed for every changed task path" (UX-046).

**Resolves:** UX-023, UX-024, UX-046.

---

## UXS-12 Approvals, reasons and step-up for audited actions

**Rule.** Every action that changes a record in a way an auditor would question has a declared tier, and the UI behaves by tier:

| Tier | Applies to (examples) | UI |
|---|---|---|
| 0 Ordinary | Save a draft, edit a description | No extra step; History records who and when |
| 1 Reason | Reverse, write off, reopen, delete, cancel a released document, release a hold, correct a time entry, override a tolerance, force sign-out, revoke a certificate | `ReasonDialog`: reason code (from a reason master when one exists) and a free-text note; the reason is sent with the request and stored on the audit record |
| 2 Approval (four eyes) | Sensitive master data: vendor bank account, credit limit, payment terms, tax ids; role assignments with admin duties; price changes above a limit | The change is saved as pending; the record shows a pending-change banner with the diff and the approver; it takes effect on approval; the requester cannot approve their own change, and the message says so plainly |
| 3 Step-up (signature) | Electronic signatures (21 CFR Part 11 PI sheets, batch release), payment release, approving tier 2 changes above a limit | `StepUpGate`: states the meaning of the signature ("I approve batch B-2209 for release"), sends the person to sign in again, returns to the same page with the input restored, then completes |

Floor devices use the supervisor PIN override (BR-32.7: one action within 60 seconds, logged with the supervisor) as their tier 3.

**Why.** Seventy-five module files hand-roll a "Reason" field; there is no shared component and no guarantee the reason reaches the audit trail (the audit `Change` schema has no reason field). Approval messages show raw duty codes (UX-020). The step-up pattern exists once, in `modules/shopfloor/PiSheetRunner.tsx` (problem `SF-0188` then `account.reauthenticate()`), and the server side is an auth-age check on `X-Depot-Auth-Time` (`platform/go/tenant/tenant.go`) rather than the designed `requires_acr` obligation (architecture delta C5). Thesis A7 ("sensitive fields require workflow"), U8 (approvals with the same audit trail).

**Do**
- Write off a customer deduction (`receivables/Deductions.tsx`): `ReasonDialog` with reason codes; the History entry shows the reason; the approval message names "the Approve write-offs duty".
- Change a vendor's IBAN on `/party/:id`: saved as a pending change (the existing `/party/pending-changes` flow), banner on the vendor: "Bank account change waiting for approval by a second person. Requested by Ben Ode on 13 Nov."
- Release a PI sheet signature: the existing `PiSheetRunner` flow moved onto `StepUpGate`.

**Don't**
- A free-text "Reason code" field with "Free text until you define downtime reasons" (`shopfloor/DowntimesPage.tsx:190`) on an audited action.
- A reason typed into a field that the API drops.
- "Needs the receivables.write-off.approve duty" (`receivables/Deductions.tsx:541`).

**Build or reuse**
- New `shell/audit/ReasonDialog.tsx` (extends `shell/ui/ConfirmDialog.tsx` with a reason select and note; minimum note length configurable per action).
- New `shell/audit/StepUpGate.tsx` (generalises `PiSheetRunner`; uses `account.reauthenticate()` from `shell/auth/account.tsx`; saves the pending input to session storage).
- New `shell/audit/PendingChangeBanner.tsx` (reuses the diff rendering of `party/PendingChangeDetailPage.tsx`).
- New `shell/audit/auditedActions.ts`: a registry of action id, tier, label, reason codes source, and the service route it calls.
- New `shell/audit/sensitiveFields.json`: the list that drives completeness. One entry per entity and field that needs more than an ordinary save, with its tier and the registry action that changes it. It starts with the A7 set (UXS-03): vendor bank account, customer credit limit, vendor and customer payment terms, tax registration ids, product sales price and standard cost, user role assignment with an admin duty, AP invoice hold release and match override, journal entry reversal, period reopen. Entity metadata (`shell/metadata.ts`) can carry the same flag later; the JSON stays the test's source until then.
- Server needs (backlog): the reason accepted on every tier 1 to 3 route and written to the audit record (AUD-API-1); one shared step-up problem code returned by every service when the sign-in is too old (today only `SF-0188`), or the designed `requires_acr` obligation; reason masters for scrap, downtime, write-off, short pick.

**When it ships.** The core of this standard ships with the History panel in rollout slice 3, not at the end: the registry, `sensitiveFields.json`, `ReasonDialog` for the tier 1 actions on the persona task routes, `PendingChangeBanner` and four-eyes for the A7 fields, and `StepUpGate` for payment release and PI sheet signatures. Slice 19 then covers the remaining modules (the 75 files that hand-roll a reason field).

**Acceptance**
1. Every action in the registry renders its tier's UI; every tier 1+ action's request carries the reason.
2. Every field in `sensitiveFields.json` is changed only through its registry action. No form lets a person change a sensitive field through a plain Save (for example, editing a vendor's IBAN on `/party/:id` goes through the pending-change flow).
3. The History panel shows the reason for tier 1+ changes.
4. Self-approval is refused with "You can't approve a change you requested." before the request is sent where the UI knows the requester.
5. After step-up, the person lands on the same page with their input intact and the action completes without re-entry.

**Enforcement**
- New `shell/audit/auditedActions.test.ts`:
  - (a) Completeness from data: every entry in `sensitiveFields.json` names a registry action that exists; and a source scan fails when a module form sends a sensitive field's key (from the JSON) in a create or update request without going through that action's component (`ReasonDialog`, `PendingChangeBanner` flow or `StepUpGate`). Shrinking allowlist.
  - (b) Labels: the verbs come from the registry's own labels (Reverse, Write off, Reopen, Release hold, Override, Revoke, Force sign out, Correct time, Unblock, Reject, Void, and any label added later), so a new tier 1 action is scanned by being registered. Buttons labelled with those verbs in files that do not use `ReasonDialog` or `StepUpGate` fail. "Cancel", "Delete" and "Remove" count only when followed by a record noun (`^(Cancel|Delete|Remove) [A-Za-z]`, as in "Cancel sales order"), so a dialog's plain "Cancel" button is not flagged. Fixtures: "Cancel" and "Delete" alone (not flagged), "Cancel sales order", "Correct time", "Void invoice" (flagged).
  - (c) Every registry entry has a tier and a reason source.
- New `shell/audit/ReasonDialog.test.tsx` and `StepUpGate.test.tsx`.
- Extend `modules/shopfloor/kiosk-pages.test.tsx` or the PI sheet tests to run through `StepUpGate`.
- Service contract tests (stage 5 backlog, written with the A7 contract tests of UXS-03): a tier 1 request without a reason is refused with a plain problem; a direct write to a tier 2 field is saved as pending (or refused) and takes effect only on a second person's approval; the requester's own approval is refused; the reason round-trips to `GET /v1/audit/records/...`.

**Resolves:** part of UX-002 (the "why"), UX-020 (with UXS-07), UX-029 (reason buttons). Thesis A7, U8.

---

## UXS-13 Auditability for end users

**Rule.** People can answer "who did this, when, and why" from where they work, without an administrator:

1. **Visible why.** Every record header shows the current state, who put it there, when, and the reason if one was captured ("Released from credit hold by Ana Ruiz on 12 Nov: customer paid"). Every document waiting for a decision shows who it is waiting on and since when.
2. **Immutable history.** History entries cannot be edited or deleted in any screen. A correction appears as a new entry (a reversal links to what it reverses). There is no "edit" or "delete" control on a history row.
3. **Export of a record's trail.** "Export history" on every record downloads the record's trail as CSV (and NDJSON for auditors), with field labels, people's names and ids, reasons, event names, and a line stating whether the audit chain verified.
4. **Cross-record search for auditors.** `/admin/audit` searches changes by record type, record, person, field and date range, links to each record, and exports the result.
5. **Waiting on others.** The inbox has tabs "Waiting for you", "Waiting on others" (things you sent) and "Done".
6. **An auditor role.** A read-only "Auditor" role in the identity role catalogue: the read duties of the finance, party, product, purchasing and sales areas, `audit.read`, a new `audit.export` duty for AUD-API-3, and `audit.read-log.view` where protected health information is in scope. No write duty of any kind. An assignment to an external auditor must carry an end date (at most 90 days, renewable), shown on `/admin/security/assignments`, and every export the auditor makes is in the trail (AUD-API-3).

**Why.** The audit page never fetches (UX-001); there is no record history outside PO and sales order (UX-002); the inbox only lists your own pending work, so "who is this waiting on" (AP6) cannot be answered (UX-027). The owners asked for Depot to be auditable; an audit trail people cannot see or export does not meet that ask.

**Do**
- PO header: "Waiting on: Ana Ruiz (Purchasing manager), since 12 Nov, 2 days".
- `/admin/audit`: filter record type = Vendor, field = Bank account, last 30 days; rows show person name, time, vendor name (link), old to new (masked), reason; "Export".

**Don't**
- `modules/admin/pages.tsx` `auditColumns`: raw `userId`, raw ISO time, a trace id column.
- A history row with an edit icon.

**Build or reuse**
- `RecordHistory` and `RecordDetail` (UXS-03); new `shell/audit/HistoryExportButton.tsx` (AUD-API-3, AUD-API-4); rebuild `AuditViewer` in `modules/admin/pages.tsx` as a `ListPage` over AUD-API-2 with entity, user and date filters.
- `modules/workflow/InboxPage.tsx`: add tabs; the workflow service needs a "sent by me" query (backlog).
- PHI read log: for records holding protected health information (HR), people with `audit.read-log.view` see a "Viewed by" tab backed by the existing `GET /v1/audit/reads?entity=&recordId=`.
- `/admin/audit` shows the audited-entity coverage (AUD-API-5): each record type marked "Recorded" or "Not recorded yet".
- The auditor role is a data change in the identity role catalogue (`policies/authz/data.json` assignments and the role pages under `/admin/security/roles`), plus the end-date rule on role assignments.

**Acceptance**
1. TA3 ("find who changed a setting last week") passes from `/admin/audit` in a usability session.
2. AP6 ("find who a pending approval is waiting on") passes from the record header or the inbox.
3. An exported trail opens in a spreadsheet with readable columns and no formula execution (reuse `csv.ts` neutralisation), and states the verification result.
4. No screen offers to change or remove a history entry.
5. A person holding only the Auditor role can do AU1 and AU2 and cannot change any record; an external auditor's assignment without an end date is refused.

**Enforcement**
- Rewrite `modules/admin/module.test.tsx` audit assertions: fetch with filters, names and formatted times, export link.
- Extend `modules/workflow/inbox.test.tsx`: the three tabs.
- New e2e `e2e/record-history.demo.spec.ts` (shared with UXS-03): export contains the change with names.
- Source scan in `RecordHistory.test.tsx`: the component exposes no edit or delete callback.
- Usability tasks TA3, AP6, CT5, AU1 to AU3 (`usability-programme.md`).
- Extend the identity service's role tests and `policies/authz/authz_test.rego`: the Auditor role grants no write method on any resource.

**Resolves:** UX-001, UX-002 (with UXS-03), UX-027.

---

## UXS-14 Navigation and workspaces

**Rule.** Every persona has a workspace (or, for the floor and portals, an app) whose home starts with that persona's tasks. A workspace's primary navigation holds at most 25 entries; everything else is reachable through All features and the command palette. The workspace a person chose, and the views they saved, follow them across devices.

**Why.** All features lists 666 entries, 189 in Finance; there is no workspace for the shop-floor operator, the maintenance planner, the master-data steward, approvers or auditors; "Sales rep or order desk" is one workspace; the choice lives in `localStorage` only (UX-025, UX-026).

**Build or reuse**
- `shell/workspaces/workspaces.json`: add `operator` (home `/kiosk`), `maintenance` (the maintenance planner and technician lead: `/eam/work-orders`, `/eam/requests`, `/eam/plans`, `/service/dispatch`, `/service/technicians`), `master-data` (the steward: `/party/pending-changes`, `/party/duplicates`, `/product/items`, `/pricing/price-list`, `/engineering/boms`), `approver` (any manager: `/workflow/inbox` first) and `auditor` (read-only: `/admin/audit`, `/finreport`). Split nothing else until usability data asks for it. Use `navAudit.json` "keep / advanced / merge" to cut each workspace's primary nav. A person may hold several workspaces (an approver is usually also a buyer or a controller); the switcher already supports that.
- Home (`shell/pages/HomePage.tsx`, `shell/home/AttentionFeed.tsx`): first block is "Your tasks" with the workspace's top five task routes and their counts.
- Store the chosen workspace through the server `PreferenceStorage` (UXS-04).

**Acceptance**
1. Each of the 16 designed-for personas in `personas-and-journeys.md` maps to a workspace or app.
2. No workspace has more than 25 primary entries.
3. Each workspace's five task routes exist (or are in `plannedRoutes.json`, UXS-01).
4. The home page of each workspace starts with a "Your tasks" block linking those five routes.

**Enforcement**
- Extend `shell/workspaces/workspaces.test.tsx`: checks 1 to 3 (the persona map is a JSON file next to `workspaces.json`).
- Extend `shell/pages/home.test.tsx`: check 4 for every workspace.
- Extend `shell/workspaces/navAudit.test.ts`: the entry cap.

**Resolves:** UX-025, UX-011 (operator workspace), UX-026 (with UXS-04). Also the review's persona gap (R-06).

---

## UXS-15 Automated accessibility coverage

**Rule.** Every workspace entry route, every floor-app screen and every portal route is scanned with axe at WCAG 2.2 A and AA tags at 1366 px and 390 px on every CI run, and the run fails on serious or critical violations. Every shared component is scanned in all its states from the day it is added. Automation is not enough: a release that adds or changes a shared component, and every milestone, also gets a manual assistive-technology pass.

**Why.** Automated scanning covers the 114 P2P/O2C routes (`e2e/flowRoutes.ts`) and shell routes at 2.1 tags (`e2e/a11y.spec.ts`); manufacturing, warehouse, ledger, HR and admin are unscanned, which is how UX-012 went unnoticed (UX-042).

**Build or reuse**
- Extend `e2e/a11y.spec.ts`: tags `wcag2a, wcag2aa, wcag21a, wcag21aa, wcag22aa`; routes generated from `workspaces.json` plus module nav (replacing the hand-kept `e2e/routes.ts` list); a 390 px project.
- Extend `e2e/flows-a11y.demo.spec.ts` the same way for the demo build; add the floor apps and portals.
- Shared components: every component exported from `shell/ui/index.ts` and from the new `shell/audit/`, `shell/floor/` and `shell/jobs/` folders gets an entry on the living catalog page `/design-system` (`shell/pages/ComponentCatalogPage.tsx`, design-system rule R9) showing each state (empty, loading, error, selected, editing, offline). `/design-system` is in the axe route list at both viewports, so a new component is scanned the day it lands, starting with rollout slice 1.
- Manual pass, recorded as `apps/web/docs/at-pass-YYYY-MM-DD.md` (template in the same folder): NVDA with Firefox and JAWS with Chrome (desktop), VoiceOver with Safari on macOS and iOS (floor and portals), keyboard only, 200% and 400% zoom and 320 px reflow, Windows text spacing, and a voice-control spot check (Voice Access or Dragon) on the main task of each changed screen. Findings go to the backlog with severity; a blocker stops the release like a usability blocker.
- Usability rounds recruit at least one participant who uses assistive technology every day for each desktop persona round ([usability-programme.md](usability-programme.md)).

**Acceptance**
1. The route list is generated, so a new workspace route is scanned without editing the spec.
2. Both viewports pass.
3. Every exported shared component has a catalog entry and passes axe there.
4. Each release that adds or changes a shared component links an AT pass report with no open blocker; rollout slices 3, 7, 8, 9 and 10 list it in their "Done when".

**Enforcement**
- The specs above, in the `web` and `web-demo-flows` CI jobs (`.github/workflows/ci.yml`), from rollout slice 2 (milestone M1). The portals and the technician app (which has no CI job today) get their own axe run in slice 20.
- Extend `shell/pages/ComponentCatalogPage.test.tsx`: every export of the shared component folders appears in the catalog.
- Release checklist: the AT pass link (`check-usability-gate.mjs` also checks that a release changing `shell/ui`, `shell/audit`, `shell/floor` or `shell/jobs` links one).

**Resolves:** UX-042, and keeps UX-012 fixed.

---

## UXS-16 Task language and expert fields

**Rule.** Clerk and floor screens use task words. Domain terms a clerk may not know (GR/IR, PPV, invoice account, order type, license plate) are wrapped in `<Term>` (glossary tooltip and link) or replaced by plain words. Expert or rarely used fields go under `FormSection collapse="advanced"`. Page descriptions say what the person can do, not how the system works. Device prompts use full words ("pallet label", not "LP") and sentence case ("Menu", not "menu").

**Why.** "clears GR/IR … to PPV for standard-cost lines" heads the AP clerk's main form (UX-014, UX-047); "Order type: Legal entity default / Journal" and "Invoice account" sit unexplained on the order desk form (UX-047); device prompts say "cart position LP" and flow names come from codes (UX-043).

**Build or reuse**
- `shell/glossary/Term.tsx` and `terms.ts` (37 terms today; add the ones above).
- `FormSection` (`shell/ui/FormSection.tsx`).
- Flow metadata labels in `modules/mobile-flows/domain/coreFlows.ts` (human `title` per flow and step).

**Acceptance**
1. On the workspace task routes of the AP clerk, AR clerk, buyer, order desk and warehouse personas, every glossary term in user text is inside `<Term>`.
2. Order type and invoice account are under Advanced on the new sales order form.

**Enforcement**
- New source scan in `shell/glossary/glossary.test.tsx`: a configured list of terms (`GR/IR`, `PPV`, `license plate`, `\bLP\b`, `invoice account`) must not appear in module JSX text outside `<Term>`; shrinking allowlist.
- Copy review checklist in `apps/web/docs/ux-writing.md`.

**Resolves:** UX-047, UX-043, the copy part of UX-014.

---

## UXS-17 Portal parity

**Rule.** The customer and supplier portals use the same tokens, formatters, status labels, problem catalog and field-error handling as the web app (from `shell/shared/`, UXS-07), the same form standard (UXS-08), and searchable lookups instead of native selects for catalogs. All portal strings are extracted into a message file so they can be translated.

**Why.** Each portal re-implements Page, Field, Badge and State with its own styles and no catalog (UX-048); field errors show as `camelKey: message` (UX-036); products are picked from a native select with free-text units (UX-039); strings are inline, English only (UX-038).

**Build or reuse**
- `shell/shared/` (UXS-07); tokens from `shell/theme.css` copied at build time or imported by path.
- Portal `ui.tsx` files keep their layout but render `FormField`-equivalent markup from a small shared set (`shell/shared/` can hold framework-light primitives once the boundary test exists).
- A searchable product combobox for `apps/customer-portal/src/pages/NewOrder.tsx`, with the unit chosen from the listing's units.

**Acceptance**
1. No portal file contains its own date, money or status formatting.
2. Field errors render at the field with a summary.
3. All user strings are in the portal's message file.
4. Portal styles use the design tokens (the `--color-*`, `--space-*`, `--radius-*` and `--text-*` variables of `apps/web/src/shell/theme.css`); no hard-coded colour values in portal CSS.

**Enforcement**
- Formatting source scan (UXS-07) covers the portals.
- Extend `apps/customer-portal/test/portal.test.tsx`, `writes.test.tsx` and `apps/supplier-portal/test/portal.test.tsx`.
- New source scan in each portal's tests: no JSX text literal outside the message file (shrinking allowlist).
- New token scan in each portal's tests: `src/app.css` contains no hex, `rgb()` or `hsl()` colour literal outside the token declarations, and declares the shared token names.

**Resolves:** UX-048, UX-035 (with UXS-07), UX-036, UX-039 (combobox and units), UX-038 (string extraction; see the waiver in the README for translation).

---

## UXS-18 Long-running work is tracked

**Rule.** An action that runs in the background (planning run, posting a large journal, payment run, import, close task, depreciation run, report run) returns a tracked job. The originating screen shows its progress, ETA when known, and result in place; the job monitor (`/workflow/jobs`) lists the same job with a link back to the record that started it; the person is notified when it ends.

**Why.** The job monitor shows invented data (UX-006); the planning run's progress is not tracked (UX-032 context). Thesis U4: "every long-running action returns a tracked workflow with progress, ETA and a notification; the originating screen updates live".

**Build or reuse**
- New `shell/jobs/JobProgress.tsx` (progress bar with text, live region updates at most every 10 seconds, result summary and link).
- `/workflow/jobs` rebuilt as a `ListPage` over the batch service.
- Inbox notification through `shell/inbox`.

**Acceptance**
1. PP1 ("run planning") shows progress on `/planning/runs/new` after Run, then on the run's row in `/planning/runs` (there is no run detail route today; add `/planning/runs/:id` if the result needs its own page), and in the job monitor.
2. No job row exists without a link to its origin.
3. When a job ends, the person who started it gets an inbox notification (success or failure, with a link), even if they left the page.

**Enforcement**
- Unit tests for `JobProgress` and the job monitor fetch (`modules/workflow/module.test.tsx`).
- Review checklist: "Does any action here return 202 Accepted or start a run? Then it shows `JobProgress`."
- New `shell/jobs/JobProgress.test.tsx`: progress announcements are throttled, the result links back, and the end of a job adds an inbox notification (`shell/inbox/inbox.test.ts` extended).

**Resolves:** UX-006 (wiring), UX-032 (planning run progress). Thesis U4.

---

## UXS-19 Measured usability gate

**Rule.** Usability is measured, not asserted. Every persona has task scripts with a screen and interaction budget; a baseline is recorded; a release that changes a task path cannot pass the usability gate without a measured comparison; and a missing measurement counts as a failure, not a pass.

**Why.** No baseline round has ever been run, so the gate's "not lower than the reference" compares against nothing; the release-notes template's default line is "Not in scope", so the gate passes by default (UX-013). The operator, technician and portal users have no task scripts. The demo data used for sessions does not yet cover every persona's tasks (UX-049).

**Detail:** [usability-programme.md](usability-programme.md).

**Acceptance**
1. Gate rules G1 to G8 in [usability-programme.md](usability-programme.md) section 6 hold: scope computed from the change, no reference means fail (except the bounded provisional reference of G8), numbers required for "Passed", no default text, budgets hold, task routes exist, narrow waivers.
2. A baseline report exists for every persona whose stage in the staged baseline (usability-programme.md section 3) has been reached.
3. Every designed-for persona has task scripts with budgets.

**Enforcement**
- New `apps/web/scripts/usability-scope.mjs` and `check-usability-gate.mjs`, run by `.github/workflows/record-release.yml`; the scripts have their own unit tests (`apps/web/scripts/usability-gate.test.mjs`) with release-notes fixtures for each rule.
- New `shell/usabilityTasks.test.ts`: every task's route exists in the route table or is planned; every task has a budget.
- New `e2e/task-budgets.demo.spec.ts`: each task's scripted happy path stays within its interaction budget (shrinking allowlist `e2e/taskBudgets.json` for tasks over budget today).

**Resolves:** UX-013, UX-049 (measurement and demo-data readiness; the guided flows and first-run journeys are UXS-20).

---

## UXS-20 Guided flows and first-run journeys

**Rule.** A new person can complete a real task on their first day without a super-user. Each workspace with a cross-document process offers a guided flow that walks the documents in order and carries each one forward. Every first contact has a designed first-run journey: the first web sign-in, the first kiosk or device use, and the first portal visit from an invitation. The demo company holds data for every designed-for persona's top five tasks, so training and usability sessions start from a realistic state.

**Why.** Guided flows exist only for procure-to-pay and order-to-cash (`modules/purchasing/ProcureToPayPage.tsx`, `modules/sales/OrderToCashPage.tsx`, both on `shell/documents/GuidedFlow.tsx`). The audit asked for receive-to-stock, make-to-order and record-to-report too, and for a training company that covers every workspace (UX-049). The supplier portal's `/register` and `/accept-invite` (in `apps/supplier-portal/src/main.tsx`) have no task script; the customer portal has no invitation route at all; a first web sign-in drops people on a generic home; the demo tour says "most screens are empty in the demo". Thesis "Easier": a new user completes a guided task in under 30 minutes, and the share of tasks done without a super-user is measured.

**Guided flows to add** (same component, same pattern: one step per document, each step shows its state, the next action, and opens the document prefilled from the previous one)

| Flow | Workspace | Steps (documents) |
|---|---|---|
| Receive to stock | `warehouse` | Purchase order or ASN → arrival and receipt (all open lines, UXS-05) → put-away work → on hand |
| Make to order | `production-planner`, `sales-rep` | Sales order line → planned order → production order released → jobs reported on the kiosk → finished goods received → shipment |
| Record to report | `controller` | Journal batch posted → subledger reconciliation status → trial balance → period close checklist (first cut of `/close`) → statement |

**First-run journeys**

| Who | Journey | Route | Status |
|---|---|---|---|
| Any web user, first sign-in | Choose a workspace (pre-selected from their roles), see "Your tasks" (UXS-14) and a one-paragraph "Start here" with the workspace's guided flow; dismissible, remembered on the server | `/` | Exists, needs change |
| Tenant admin, new tenant | Setup wizard and checklist; "Try the demo company" | `/setup`, `/setup/checklist`, `/setup/demo-company` | Exists |
| Operator, first kiosk use | Station not set up → supervisor enrolment → badge sign-in with a three-step on-screen guide the first time a badge is used | `/kiosk` | Missing (floor-apps.md section 5) |
| Warehouse worker, first device use | Device enrolment → badge and PIN set-up → "Next task" with the first step explained | Device app | Missing (floor-apps.md section 5) |
| Supplier portal user | Accept the invitation (or register), set up the account, land on open purchase orders with a short "What you can do here" | `/accept-invite`, `/register`, then `/purchase-orders` | Routes exist; journey needs the script and copy |
| Customer portal user | Open the invitation email, sign in, land on the home page with open orders and invoices and "Reorder" | Customer portal `/` after sign-in | No invitation route exists in the customer portal; the invite is an identity-provider email today. A `/welcome` first-run page is missing |

**Do**
- The demo tour lists the workspaces with sample data and says plainly which screens have none (UXS-01).
- A first-time kiosk user sees "Scan your badge. Then tap a job to start it. Tap Report when you have pieces to count." once, with "Got it".

**Don't**
- A first sign-in that opens All features with 666 entries.
- A guided flow that only links to blank create pages (each step must open prefilled from the previous document).

**Build or reuse**
- Reuse `shell/documents/GuidedFlow.tsx`, `useFlow.ts`, `useStageCount.ts`, `DocumentFlow.tsx`.
- New `shell/pages/FirstRun.tsx` (workspace choice and "Start here"), stored through the server `PreferenceStorage` (UXS-04).
- Demo data: extend `apps/web/src/demo/handlers/` so every task in `usability-programme.md` has its start record (checked by `e2e/task-readiness.demo.spec.ts`); add the supplier portal demo mode.
- New customer portal `/welcome` route.

**Acceptance**
1. The three guided flows exist and each step opens its document prefilled from the previous one.
2. Task scripts NU1 to NU3 (a new user completes each guided flow), CP0 and SP0 (first portal visit) and OP0 (first kiosk use) pass in usability rounds; NU1 to NU3 within 30 minutes for at least 4 of 5 participants with no Depot experience.
3. Every designed-for persona's top five tasks are ready in the demo data.

**Enforcement**
- New `shell/documents/guidedFlows.test.tsx`: each flow's steps, prefill and stage counts.
- `e2e/task-readiness.demo.spec.ts` (UXS-19): demo data for every task.
- Extend `apps/supplier-portal/test/portal.test.tsx`: invitation and registration render the first-run copy and land on purchase orders.
- Usability tasks NU1 to NU3, CP0, SP0, OP0 ([usability-programme.md](usability-programme.md)).

**Resolves:** UX-049 (guided flows, first-run journeys, training data). Thesis "Easier" axis.
