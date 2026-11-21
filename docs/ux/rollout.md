# Rollout: backlog slices

The standards in [patterns.md](patterns.md) broken into slices that can each be built, reviewed and shipped on their own, ordered by user impact. Each slice names the standards it delivers, the audit findings it closes (`docs/sdlc/run-1/ux-audit.md`), what it depends on, and the check that proves it is done. Stage 5 turns each slice into one or more GitHub issues labelled `sdlc-run-1` and `ux`.

**Sizes.** S: up to a week for one engineer. M: two to four weeks. L: more than four weeks or several people; split further in stage 5.

**Platform work (P).** A slice marked P builds a shared component, rule or test that other slices and modules reuse. P slices unblock many findings at once and go first when two slices are otherwise equal.

**Ordering rule.** First, stop showing people things that are false or unreachable, and put the guard rails in place (milestone M1). Then give every record its "who, when, why", with reasons and second-person approval for sensitive changes, proven on real services (M2), because that is the owners' auditability ask. Then remove the friction that costs the most people the most time each day, including a first cut for each floor app (M3). Then complete coverage (M4, M5).

**Every High finding lands early.** Each of the 16 High findings has a first cut that closes the user-facing harm in slices 0 to 9 (milestones M1 to M3). Where the full fix is larger, the rest follows in a later slice, named in the table. Revised after the stage 4 review (R-01).

| High finding | First cut (slice) | Rest (slice) |
|---|---|---|
| UX-001 audit page always empty | 1 (honest), 3 (wired to the audit service) | — |
| UX-002 no record history | 3 | 19 (all modules) |
| UX-003 period close has no screen | 4 (read-only status, blockers, checklist run with task completion) | 18 (reopen approval, year-end, allocations, accruals) |
| UX-004 import is a stub | 1 (opens the working setup import step) | — |
| UX-005 sequences false empty state | 1 (read-only list on the numbering service; setup checklist link keeps working) | 17 (create, edit) |
| UX-006 job monitor fake data | 1 (fake data removed) | 16 (wired to the batch service) |
| UX-007, 008, 009 raw ids | 2 (copy rules), 5 (drop-ship from the order line), 6 (lookups) | — |
| UX-010 device not directed | 9 (sign-in; directed receive, put-away and pick on the existing mobile service calls) | 12 (count, move, pack, cluster, device administration) |
| UX-011 kiosk in the desktop shell | 8 | — |
| UX-012 overflow at 390 px | 2 | — |
| UX-013 no usability baseline | 0 (stage A) | Stages B and C before slices 5 and 11 |
| UX-014 AP invoice cold start | 5 (start from receipt, all open lines, no standard-cost box) | 10 (line grid) |
| UX-015, 016 no bulk actions | 7 | — |

## Summary

| # | Slice | Size | P | Standards | Findings closed | Depends on |
|---|---|---|---|---|---|---|
| 0 | Usability baseline stage A, task scripts, and a gate that cannot pass by default (or freeze releases) | M (process, about 6 weeks elapsed) + S (scripts) | P | UXS-19 | UX-013, 049 (measurement and demo-data readiness) | Recruiting lead time; supplier portal demo mode (for stage C) |
| 1 | Honest screens, with first real admin pages | S-M | P | UXS-01 | UX-001, 003, 004, 005, 006 (interim), 049 (demo tour copy) | Numbering service (built) |
| 2 | Guard rails: responsive shell, plain-text rules, generated accessibility scan | M | P | UXS-10, UXS-15, UXS-07 (text rules), UXS-02 (banned copy), UXS-16 (copy) | UX-012, 042, 019, 020, 022, 037, 043, 045, 047, 009, 014 (copy) | — |
| 3 | Record history, auditability for end users, and the audited-action core (reasons, four eyes, step-up) | L | P | UXS-03, UXS-13, UXS-12 | UX-001, 002, 027, 020, 029 (reason masters) | AUD-API-1 to 8; ingestion for the A7 set (architecture D3); workflow "sent by me" query; reason accepted on tier 1+ routes; shared step-up code |
| 4 | Close workspace, first cut | M | — | UXS-01, UXS-03, UXS-18 | UX-003 | Close service (built) |
| 5 | Defaults and start from the source document (first cut) | M | P | UXS-06, UXS-05 (start-from part), UXS-16 (Advanced sections) | UX-034, 033, 014, 028, 007, 047 | — |
| 6 | Names, not ids | L | P | UXS-02 | UX-007, 008, 009, 021, 030, 032, 033 | Lookup definitions for missing entities |
| 7 | List selection, bulk actions, paste, undo | L | P | UXS-04 | UX-015, 016, 017, 026, 031 (web), 032 (paste) | Preferences API; bulk endpoints (pay selected, send reminders, approve selected) |
| 8 | Floor layout and kiosk; operator workspace | L | P | UXS-09, UXS-14 (operator) | UX-011, 029, 030 | Station enrolment; reason masters |
| 9 | Warehouse device first cut: sign-in, directed receive, put-away and pick | L | — | UXS-09 | UX-010, 043 | Slice 8 layout; edge-identity device flow; mobile service pick and receive calls (built) |
| 10 | Keyboard line grid | L | P | UXS-05 | UX-014, 028, 050 | Slice 5 |
| 11 | Guided flows and first-run journeys | M | P | UXS-20 | UX-049 | Slices 4, 5, 8, 9; demo data per task |
| 12 | Warehouse device complete: count, move, pack, cluster, device administration | L | — | UXS-09 | UX-010, 031 (device), 044 | Mobile service work calls for count, move, pack, cluster; server PIN check; `services/mobile` gateway route |
| 13 | Form standard everywhere | M | P | UXS-08 | UX-018, 040, 041 | — |
| 14 | Help on every route and role guides | M | P | UXS-11 | UX-023, 024, 046 | — |
| 15 | Portal parity | M | P | UXS-07 (shared), UXS-17 | UX-035, 036, 038 (string extraction), 039, 048 | `shell/shared/` (slice 2) |
| 16 | Long-running work tracked | M | P | UXS-18 | UX-006, 032 (run progress) | Batch service list API with origin links |
| 17 | Admin capabilities complete | M | — | UXS-01 | UX-005, 044 | Numbering write routes; audit alerts; documents and texts services |
| 18 | Close workspace complete | L | — | UXS-01, UXS-12, UXS-18 | UX-003 | Close service (built) |
| 19 | Audited actions and History in every module | M | P | UXS-12, UXS-03 | UX-002, 020 | Slice 3 |
| 20 | Navigation, workspaces, and portal and technician accessibility in CI | M | P | UXS-14, UXS-15 | UX-025, 026, 042 | Slice 1 (planned routes), slice 7 (server preferences) |

Every one of the 50 findings appears in at least one row. UX-038 is closed only in part; see the waiver in [README.md](README.md).

## Milestones

| Milestone | Slices | What a user notices | Declared when |
|---|---|---|---|
| M1 "Nothing false" | 0, 1, 2 | No fake or empty-by-design pages; sequences and import work from the admin menu; the shell works on a phone; no codes, UUID prompts or raw enums in text; every shared component scanned | Slices done; baseline stage A report committed |
| M2 "Who, when, why" | 3, 4 | History on every persona-critical record, reasons on sensitive actions, second-person approval for bank accounts, credit limits and role assignments, an audit page that answers TA3 and AU1 to AU5, the approver on every document, a read-only close workspace | Slices done **and** the A7 contract tests (UXS-03) pass against the real services; the demo build alone does not count |
| M3 "Fewer keystrokes, and the floor" | 5 to 9 | Defaults that fit, invoices and receipts from their source, names everywhere, select and act on many rows, a full-screen kiosk, directed receive and pick on the handheld | Slices done; milestone round for the changed personas |
| M4 "Complete the core" | 10 to 13 | Keyboard line entry, guided flows and first-run journeys, the full warehouse device, the form standard everywhere | Slices done; milestone round; baseline stage C before slice 11 |
| M5 "Complete" | 14 to 20 | Help on every page, portals on the shared standard, tracked background work, complete admin and close, audited actions in every module, trimmed navigation | Slices done; milestone round |

Every milestone round includes the manual assistive-technology pass of UXS-15.

---

## Slice details

Each slice's "Done when" includes, without repeating it: the slice's tests pass in CI with their allowlists; the docs in the alignment table below are updated; the change-management steps at the end of this file are done.

### Slice 0. Usability baseline stage A, scripts and gate (M + S, P)

- Round 0 expert walkthrough of every task script; baseline stage A (tenant admin, controller, AP clerk, master-data steward, auditor, approver: 30 sessions) on the current commit ([usability-programme.md](usability-programme.md) sections 2 and 3). Start recruiting for stage B at the same time.
- Add the task scripts (OP0 to OP6, WH6 to WH9, TE, CP0 to CP5, SP0 to SP5, SR6, TA6, WS, MD, AM, MT, AU, NU); mark CT4 planned until slice 4; fix the #503 sentence in `apps/web/docs/usability-tasks.md`.
- Add `shell/usabilityTasks.test.ts`, `e2e/task-readiness.demo.spec.ts`, `e2e/task-budgets.demo.spec.ts` with `e2e/taskBudgets.json`, `scripts/usability-scope.mjs`, `scripts/check-usability-gate.mjs` (rules G1 to G8, with the bounded provisional reference) and its fixture tests; wire it into `.github/workflows/record-release.yml`; remove the default line from `docs/ops/release-notes-template.md`.
- Start the supplier portal demo mode (needed for stage C).
- **Done when:** the stage A report is committed with numbers for its six personas; Round 0 numbers exist for every other task; the gate script fails a release-notes file that says "Not in scope" for a change under `apps/web/src/modules/ap-invoices/`, and accepts a correctly signed provisional entry.

### Slice 1. Honest screens, with first real admin pages (S-M, P)

- `/admin/sequences`: a read-only list on `GET /api/v1/numbering/sequences` (code, legal entity, format, next value), so the setup checklist's sequences link (`modules/setup/checks.ts:35`) lands on real data.
- `/admin/import`: opens the working import, the setup guide's "Import your first records" step (`modules/setup/ImportStep.tsx`).
- Hide from nav, workspaces and the palette, or render `NotAvailablePage` for: `/admin/audit` (until slice 3), `/admin/alerts`, `/admin/documents`, `/admin/texts` (until slice 17), `/workflow/designer`, `/workflow/templates`, `/workflow/jobs` (until slice 16). Each entry in `shell/plannedRoutes.json` has a slice and a target date.
- Move workflow and admin sample rows into `src/demo/handlers/`; remove default sample props and "Sample data" badges; fix the demo tour copy.
- Add `shell/honestScreens.test.ts`; update `modules/admin/module.test.tsx` and `modules/workflow/module.test.tsx`, which assert the fake data today, and the preserve list in `apps/web/docs/design-system.md` section 8.
- Shared components added from here on get a `/design-system` catalog entry, which slice 2's scan covers (UXS-15).
- **Done when:** the honest-screens scan passes with its allowlist; no workspace links a planned route; TA5 and the sequences step of the setup checklist complete in Round 0.

### Slice 2. Guard rails: responsive shell, plain-text rules, accessibility scan (M, P)

- Collapse the top bar below 640 px into a Context sheet (`shell/TopBar.tsx`, `Shell.module.css`, new `shell/ContextSheet.tsx`); add `e2e/reflow.demo.spec.ts` (390 and 320 px, every workspace entry route); correct ACR row 1.4.10.
- Add `UUID_WORD`, `ID_PROMPT`, `DUTY_CODE` (from the duty catalogue in `policies/authz/data.json`), `PROBLEM_CODE` (known prefixes) and `RAW_ENUM` to `shell/plainLanguageRules.ts` with the positive and negative fixtures of UXS-02; extend `shell/plainLanguage.test.ts` and `shell/errors/bareCode.test.ts` to all modules with shrinking allowlists.
- Fix the copy the audit lists: codes in titles and descriptions (UX-019), duty codes via `dutyName` (UX-020), raw status badges (UX-022), supplier review reasons (UX-037), device prompts and flow names (UX-043), technician date (UX-045), AP and sales page descriptions with `<Term>` (UX-047). The 547 "UUID" strings sit on raw id fields; they are rewritten as those fields are converted in slice 6 and stay in the allowlist until then.
- Create `shell/shared/` (format, status, problem, catalog, fieldErrors, dutyName) with its boundary test.
- Generate the axe route list from workspaces and module nav, move to the WCAG 2.2 tags, add the 390 px project, and include `/design-system` (UXS-15). Portals and the technician app join in slice 20.
- **Done when:** the reflow, plain-language and axe specs pass on the generated list at both viewports; every pattern fixture passes.

### Slice 3. Record history, auditability and the audited-action core (L, P)

UI:
- `shell/ui/RecordDetail.tsx`, `shell/audit/RecordHistory.tsx` with its sources adapter and the actor labels of UXS-03, `HistoryExportButton.tsx`.
- `/admin/audit` rebuilt as a `ListPage` over the cross-record query, with the audited-entity coverage list; restored to nav.
- `RecordDetail` on the persona-critical detail pages: vendor and customer (`party/PartyDetailPage.tsx`), product, PO, receipt, AP invoice, sales order, customer invoice, journal entry, production order, worker. Seed `detailPageBacklog.json` with the rest (slice 19).
- Inbox tabs "Waiting on others" and "Done"; "Waiting on" in document headers.
- The audited-action core (UXS-12): `shell/audit/auditedActions.ts` registry, `shell/audit/sensitiveFields.json` starting with the A7 set, `ReasonDialog` for the tier 1 actions on the persona task routes (reverse, write off, reopen, release hold, correct time, override), `PendingChangeBanner` and four-eyes for the A7 fields, `StepUpGate` for payment release and PI sheet signatures (replacing the PI-sheet-specific flow), the completeness test.
- The Auditor role with end-dated assignments for external auditors (UXS-13).

Backend (stage 5 issues; this slice cannot be declared done without them):
- `services/audit`: AUD-API-1 (with the verified actor and refusal of a body `userId`), AUD-API-2 to AUD-API-6, AUD-API-8.
- Ingestion for the A7 set from `party`, `pricing`, `costing`, `identity`, `receiving`, `ledger` (architecture D3, actor propagation).
- Every tier 1 route accepts and stores a reason; tier 2 fields are saved as pending; one shared step-up problem code or the `requires_acr` obligation.
- `workflow`: a "sent by me" query for the inbox tab.

**Done when:** `RecordHistory.test.tsx`, `auditedActions.test.ts`, `ReasonDialog.test.tsx`, `StepUpGate.test.tsx` and `e2e/record-history.demo.spec.ts` pass; the A7 contract tests in each owning service and the audit actor contract test pass; `e2e/record-history.live.spec.ts` passes against a deployed environment; a manual assistive-technology pass on RecordDetail, RecordHistory, ReasonDialog and StepUpGate has no open blocker; TA3, AP6, MD1 and AU1 to AU5 pass in Round 0.

### Slice 4. Close workspace, first cut (M)

- `/close` added to the controller workspace: the period by module status grid (open, closed, closing) from `GET /v1/close/periods` and `/v1/close/period-statuses:check`, with what blocks each module in plain words; the current checklist run (`/v1/close/runs`, `/runs/{runId}/tasks`) with each task's owner and state, and Start, Complete and Skip (with a reason) on tasks; History on each period (UXS-03). Closing a module period is in this slice; reopen requests are slice 18.
- Remove CT4 from `plannedRoutes.json`.
- **Done when:** CT4 ("close last month or name what blocks it") is within budget in `task-budgets` and passes in Round 0.

### Slice 5. Defaults and start from the source document, first cut (M, P)

- `shell/defaults/useDefaults.ts`; replace `currency: "USD"` and other literal defaults, starting with sales order, PO, AP invoice, receipt arrival, AR payment, free-text invoice; default the ledger from the legal entity; add `noLiteralDefaults.test.ts`.
- `shell/documents/SourcePicker.tsx` on the existing line layouts: AP invoice "Start from receipt or PO" loads all open lines with quantity and price, and drops the standard-cost checkbox (UX-014); receipt from a PO loads all open lines at their open quantity and posts in the same step, with master-data flags hidden (UX-028); "Ship from vendor" on a sales order line replaces the eight-UUID drop-ship form as the entry point (UX-007). The line grid replaces the line editing itself in slice 10.
- Order type and invoice account move under Advanced on the new sales order form (UX-047).
- **Done when:** AP1, WH1 and SR6 are within budget; the defaults scan has an empty allowlist for the workspace task routes.

### Slice 6. Names, not ids (L, P)

- Replace the label-based rules in `shell/lookup/rawIdInputs.test.ts` with the behavioural scans; add `e2e/names-not-ids.demo.spec.ts`.
- Convert in workspace order: order desk (`sales/OrderProgressPages.tsx`, `DeliveryTolerancesPage.tsx`, `ChangePolicyPage.tsx`, `AllocationQuotasPage.tsx`), buyer (`purchasing/OpenSupplyPage.tsx`), planner (`planning/RunPlanPage.tsx` with multi-select paste, `planning/Firming.tsx` columns), tenant admin (`admin/security/RoleAssignmentsPage.tsx`, `fulfillment/WarehouseSettingsPage.tsx`), shop floor (`shopfloor/JobsPage.tsx`), controller (`ledger/JournalBatchPage.tsx`), then the rest (60 input files, 149 columns).
- `EntityLookup` multi-select with paste; missing entity definitions.
- **Done when:** both scans pass with an allowlist limited to admin, integration and migration screens; the e2e finds no visible UUID on workspace entry routes.
- **Status:** first part delivered: both scans (`rawIdInputs.test.ts`, `idColumns.test.ts`) with their `pending` allowlist groups, `EntityMultiLookup` with paste of item numbers, `planning/RunPlanPage.tsx` and `planning/SupplySchedulePage.tsx` converted, party search in similarity order (FR-7.13), `e2e/names-not-ids.demo.spec.ts` for the planner, order desk and buyer entry routes. Left: every screen and table listed in the `pending` groups of `rawIdAllowlist.json` and `rawIdColumnAllowlist.json`, in workspace order; each slice removes its entries, and the groups are empty when the slice is done.

### Slice 7. Lists: selection, bulk, paste, undo (L, P)

- Extend `ListPage`; build the server `PreferenceStorage` with Share and role default.
- First bulk actions by user impact: Pay selected (`/payables/open-items`, AP4), Send reminders (`/receivables/open-items`), Approve selected (`/workflow/inbox`, AM4), Release selected (`/planning/planned-orders`), Save counts (`/inventory/count-sheets/:id`), Paste rows on the price list (MD3).
- Replace the 12 hand-rolled selections; widen consistency rule 2 to all modules and convert lists in workspace order.
- **Done when:** `ListPage.test.tsx` covers the new behaviour; AP4, AM4 and MD3 are within budget; `handRolledSelection.json` is empty; a manual assistive-technology pass on the bulk bar and paste dialog has no open blocker.

### Slice 8. Floor layout and kiosk (L, P for the layout)

- `shell/floor/` (FloorLayout, BigButton, ScanField, ReasonButtons with icon and text, QueueStatus) from the technician app's pattern; the technician app adopts the tokens and `QueueStatus` only.
- `/kiosk`, `/kiosk/jobs/:id`, `/kiosk/pi/:id` outside `Shell`; `/shopfloor/kiosk` and `/shopfloor/kiosk/pi/:id` redirect; job reporting with undo and correction, andon and downtime on the kiosk with reason buttons; station enrolment at `/shopfloor/stations`.
- The `operator` workspace and the operator guide.
- `e2e/floor-layout.spec.ts`; `kiosk-labor.spec.ts` and `kiosk-offline.spec.ts` on the new routes; a kiosk queue unit test (`modules/shopfloor/offlineQueue.test.ts`).
- **Done when:** floor-apps.md checks F1 to F3, F7 to F9 and F13 pass for the kiosk; OP1 to OP6 are within budget.

### Slice 9. Warehouse device first cut (L)

- Device enrolment (device flow) and worker sign-in (badge and PIN, re-auth after 8 hours); remove the `"unassigned"` user.
- Directed receive, put-away and pick on the mobile service calls that already exist against the warehouse work API; "confirm by scan" defaults per floor-apps.md section 4; "Undo last line"; glossary wording.
- The other flows stay on the menu, labelled "Not directed yet: scan what you handle" (UXS-01) until slice 12.
- **Done when:** floor-apps.md checks F1 to F8b, F10 and F13 pass for receive, put-away and pick; WH6, WH7 and WH8 are within budget.

### Slice 10. Keyboard line grid (L, P)

- `shell/ui/LineGrid.tsx` with the keymap of UXS-05 (no Insert, no Ctrl+D, row actions on Shift+F10) and its denylist test. Delivered for the component (`shell/ui/LineGrid.tsx`, `LineGrid.test.tsx`); the page rollout below is still to do.
- Apply in this order: AP invoice (AP1), receipt (WH1), sales order (SR1), PO (BU1), journal batch (CT1), requisition, free-text invoice, transfer order, BOM lines (MD5).
- Extend `e2e/p2p-keyboard.demo.spec.ts`.
- **Done when:** the listed pages render `LineGrid`; AP1, WH1, SR1, BU1, CT1 and MD5 are within budget; a manual assistive-technology pass on the grid (NVDA, JAWS, VoiceOver, keyboard only, 400% zoom) has no open blocker.

### Slice 11. Guided flows and first-run journeys (M, P)

- Guided flows on `shell/documents/GuidedFlow.tsx`: receive to stock (warehouse), make to order (order desk and planner), record to report (controller, using the slice 4 close workspace).
- First-run journeys (UXS-20): `shell/pages/FirstRun.tsx` for the first web sign-in; the kiosk and device first-use guides; the supplier portal's `/accept-invite` and `/register` landing on purchase orders with first-run copy; a customer portal `/welcome` page.
- Demo data for every designed-for persona's top five tasks (task readiness green); the supplier portal demo mode finished. Baseline stage C runs before this slice ships.
- **Done when:** `guidedFlows.test.tsx` passes; NU1 to NU3, OP0, CP0 and SP0 pass in the milestone round, NU tasks within 30 minutes for at least 4 of 5 participants.

### Slice 12. Warehouse device complete (L)

- Directed count, move, pack and cluster as their mobile service calls land; the cart built from assigned work.
- Device administration at `/mobile-flows/devices` (register, active sessions, force sign-out, stale sweep); filters on `/mobile-flows/conflicts`.
- **Done when:** floor-apps.md checks F1 to F13 pass for every device flow; WH9, WS1 and WS2 are within budget.

### Slice 13. Forms everywhere (M, P)

- Widen consistency rule 3 to all modules and edit pages; `useFieldValidation` (blur, then live); `SessionExpiryWarning` with draft restore; update ACR 2.2.1.
- **Done when:** `formStandardBacklog.json` covers only pages scheduled in later slices, and shrinks each release.

### Slice 14. Help and guides (M, P)

- `shell/help/defaultHelp.ts` (purpose and at least one action required); curated entries for every task route; `/help/guides/:guide` in the app; guides for operator, warehouse worker, controller, planner, technician, customer portal, supplier portal, master-data steward, approver, maintenance planner and auditor (and HR, supervisors); fix `docs/user/ap-clerk.md` "location ID" and `tenant-admin.md` sequences text.
- **Done when:** `routeHelp.test.ts` passes for all modules; every persona has a guide.

### Slice 15. Portal parity (M, P)

- Portals import `shell/shared/`; inline field errors with a summary; status labels; design tokens; searchable product combobox and unit select in the customer portal; strings extracted to message files in both portals.
- **Done when:** portal tests assert formatting, labels, tokens and inline errors; the formatting scan covers the portals with an empty allowlist.

### Slice 16. Long-running work (M, P)

- `shell/jobs/JobProgress.tsx` with the end-of-job inbox notification; `/workflow/jobs` on the batch service with links to the origin; planning run, payment run, depreciation run, report runs and close tasks show progress in place; `/workflow/jobs` restored to nav.
- **Done when:** PP1 shows tracked progress; the job monitor and notification tests pass.

### Slice 17. Admin capabilities complete (M)

- `/admin/sequences` create and edit; alerts on `GET /v1/audit/alerts`; documents and standard texts on their services. Each restored to nav as it ships.
- **Done when:** each page is off `plannedRoutes.json`.

### Slice 18. Close workspace complete (L)

- Reopen request and approval (tier 2, reason), year-end run with progress (UXS-18), allocation, accrual and regrouping runs with post and reverse (tier 1).
- **Done when:** the remaining close task scripts pass in a milestone round.

### Slice 19. Audited actions and History in every module (M, P)

- `ReasonDialog` across the 75 files that hand-roll a reason field; `RecordDetail` on the remaining detail pages (`detailPageBacklog.json`); new sensitive fields added to `sensitiveFields.json` as modules are reviewed; ingestion for every remaining entity (architecture D3).
- **Done when:** `auditedActions.test.ts` and consistency rule 4 have empty allowlists, or allowlists limited to pages with a dated plan.

### Slice 20. Navigation, workspaces and full accessibility coverage (M, P)

- Trim each workspace's primary nav to 25 using `navAudit.json`; "Your tasks" on each workspace home; the `master-data`, `approver`, `maintenance` and `auditor` workspaces (added earlier with their personas' slices if needed); the workspace choice stored on the server.
- The portals and the technician app join the axe run in CI.
- **Done when:** `workspaces.test.tsx`, `home.test.tsx` and `navAudit.test.ts` enforce the cap and the "Your tasks" block; the axe specs cover the portals and technician app.

## Change management for existing users

People who already use Depot lose time when screens move under them. Every slice that changes a task path does three things in the same release (R-19):

1. **Says what changed.** An entry in "What's new" (`apps/web/src/shell/help/whatsNew.ts`, `/whats-new`, and `docs/user/whats-new.md`) naming the screen, what moved and why, in plain words. Kiosk and device changes also show a one-time notice on the floor app ("Reporting scrap has moved to the job screen").
2. **Keeps old addresses working.** A moved route redirects: `/shopfloor/kiosk` and `/shopfloor/kiosk/pi/:id` to `/kiosk…`; `/admin/import` to the setup import step; any nav entry removed by the 25-entry trim stays reachable through All features and the palette, and bookmarks to it still open. A test in `modules/registry.test.ts` fails when a route that existed in the previous release disappears without a redirect.
3. **Updates the guide.** The user guide section for the changed task is updated in the same pull request (UXS-11).

## Documentation alignment (part of the slice that changes the behaviour)

| Doc in `apps/web/docs/` | Change | Slice |
|---|---|---|
| `design-system.md` | Add floor layout, RecordDetail, RecordHistory, LineGrid, ReasonDialog, StepUpGate, PendingChangeBanner, JobProgress, ContextSheet to sections 2 and 4 and the `/design-system` catalog (rule R9); drop the two fake-data "preserve" assertions in section 8 | 1, 2, 3, 8, 10, 16 |
| `forms.md` | Validation on blur; scope widened to all modules; session warning; defaults; start from source | 5, 13 |
| `lists.md` | Selection, bulk, paste, undo, server views; rollout checklist points to this file | 7 |
| `ux-writing.md` | Replace "Identifiers a person must type or paste … until a picker replaces the field" with the UXS-02 rule; add the banned patterns; floor glossary | 2, 6, 9 |
| `usability-tasks.md`, `usability-gate.md`, `usability-baseline-report.md` | Sections 3, 4 and 6 of usability-programme.md | 0 |
| `accessibility-conformance-report.md` | 1.4.10 (390 px overflow, then fixed), 2.2.1 (warning shipped), scope (all workspace routes), the manual pass reports | 2, 13, 20 |
| New `at-pass-template.md` | The manual assistive-technology pass template (UXS-15) | 1 |
| Each doc | A first line: "Extended by docs/ux/ (UXS-nn); where they differ, docs/ux wins." | 0 |
