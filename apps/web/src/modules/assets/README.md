# Fixed assets (web module, PSG-37 wave 37)

Module folder: `apps/web/src/modules/assets/`. Discovered by the shell through
`module.ts` (`id: "assets"`). Pages use the shell primitives from
`src/shell/ui` and `pages.module.css` tokens only (docs/design-system.md).

## Screens (slices 1 to 6)

| Route | Page | Duty (nav) | What it does |
|---|---|---|---|
| `/assets` | `AssetsPage` | assets.asset.view | Asset register with a status filter and "Load more" over the service cursor |
| `/assets/new` | `NewAssetPage` | assets.asset.maintain | Create an asset (number, name, class, barcode, quantity) |
| `/assets/:id` | `AssetDetailPage` | assets.asset.view | Master data, books with cost / accumulated / NBV, the last depreciated period and the group asset that depreciates the book, the projected schedule of the selected book, the transaction log, additions, insurance policies (with the under-insured flag) and the transfers and splits the asset took part in |
| `/assets/:id/adjust` | `AdjustPage` | assets.journal.post | Acquisition or depreciation adjustment, revaluation or impairment on one open book; shows the AST-0512 warning when a reversal is capped |
| `/assets/:id/additions/new` | `NewAdditionPage` | assets.asset.maintain | Record an improvement, component or accessory; a financial one posts its cost on the chosen open books |
| `/assets/:id/insurance/new` | `NewPolicyPage` | assets.insurance.maintain | Record a policy with an optional index series; shows the indexed value and the AST-0531 / AST-0532 warnings |
| `/assets/:id/books/new` | `AddBookPage` | assets.asset.maintain | Add a book (method, life, convention, factor, cost); saving posts the acquisition |
| `/assets/:id/dispose` | `DisposePage` | assets.asset.dispose | Sale or scrap of a percentage on every open book, sent with the asset ETag as If-Match; shows the gain or loss per book |
| `/assets/depreciation-runs` | `RunsPage` | assets.asset.depreciate | Runs newest first, and the form to propose one for a book code, legal entity and period |
| `/assets/depreciation-runs/:id` | `RunDetailPage` | assets.asset.view | Totals, per-book lines with exceptions (AST-0503 and others), Post with the run ETag as If-Match |
| `/assets/index-series` | `IndexSeriesPage` | assets.insurance.maintain | Insured-value index series and the form that loads yearly values (`PUT`) |
| `/assets/capex` | `CapexListPage` | assets.capex-request.create | Capex requests with a status filter and "Load more" |
| `/assets/capex/new` | `NewCapexPage` | assets.capex-request.create | Create a request with one to ten variants (amounts per year as `2026=200000.00` lines) |
| `/assets/capex/approvals` | `CapexListPage` | assets.capex-request.approve | The same list opened on requests in approval, for approvers (slice 4) |
| `/assets/capex/:id` | `CapexDetailPage` | assets.capex-request.create | Request, variants with totals, and Submit of a Draft with the chosen variant (If-Match); shows the approval level it was routed to. Slice 4: the decisions each status allows (approve, postpone or reject with a required comment, return a postponed request to draft, release, close), each sent with the latest ETag as If-Match, the AST-0028 own-request refusal, and the decision trail |
| `/assets/mid-quarter-test` | `MidQuarterPage` | assets.asset.depreciate | BR-37.5 test for a legal entity, tax book and year: basis per quarter, the Q4 share, whether mid-quarter applies (AST-0511) and the books whose convention to review (slice 4) |
| `/assets/leases` | `LeasesPage` | assets.lease.maintain | Leases with liability, IBR and status |
| `/assets/leases/new` | `NewLeasePage` | assets.lease.maintain | Recognize an IFRS 16 lease, with the nominal or effective periodic rate |
| `/assets/leases/:id` | `LeaseDetailPage` | assets.lease.maintain | Recognized liability and ROU asset, the terms in force, the remeasurements, the ledger postings (slice 6: which journals are booked, with a link to post) and the liability / ROU schedule with the remeasurement column |
| `/assets/leases/posting` | `LeasePostingPage` | assets.journal.post | Slice 6: book the lease recognition and every ended schedule period in the general ledger (legal entity, ledger, post-through date, optional lease; prefilled from the lease page); lists the journals booked and those the ledger refused, with the placeholder-account warning |
| `/assets/leases/posting-profile` | `LeaseProfilePage` | assets.lease.maintain (save: assets.asset.configure) | Slice 6: the ledger accounts of the lease journals per legal entity; shows the placeholders until a profile is saved, creates it, and replaces it under its ETag |
| `/assets/leases/:id/modify` | `ModifyLeasePage` | assets.lease.maintain | Remeasure from an effective period: a payment change at the lease's rate, or a scope change (revised term and rate); sent with the lease ETag as If-Match; shows the AST-0516 message and any amount sent to profit or loss |
| `/assets/leases/:id/terminate` | `TerminateLeasePage` | assets.lease.maintain | Terminate a lease early (slice 7): last period in force, termination payment and reason; sent with the lease ETag as If-Match; shows the AST-0036 message, the derecognized liability and ROU asset and the gain or loss. The lease page shows a Termination card and a Termination row in the ledger postings |
| `/assets/count-sessions` | `CountSessionsPage` | assets.asset.count | Physical inventory sessions, newest first, with expected / found / missing / unknown counts |
| `/assets/count-sessions/new` | `NewCountSessionPage` | assets.asset.count | Record a count: legal entity, location, date and the scanned barcodes (one per line) |
| `/assets/count-sessions/:id` | `CountSessionDetailPage` | assets.asset.view | Result tiles, the AST-0525 warnings and the lines (missing, unknown, found) linking to the asset |
| `/assets/insurance-report` | `InsuranceReportPage` | assets.asset.view | Policies in force on a date for a legal entity, filterable by type and expiry window: sum insured, indexed replacement value, NBV per book, under-insured and expiring-soon flags |
| `/assets/:id/transfer` | `TransferPage` | assets.asset.maintain | Transfer the whole asset to one new asset (optionally in another legal entity) or split it into 2 to 50 new assets by share; the shares are totalled without floats and must be 100.00%; sent with the asset ETag as If-Match; shows the new assets with their book values and links (slice 5) |
| `/assets/group-assets` | `GroupAssetsPage` | assets.asset.configure | Group assets with class, tax book, tax year, member count and basis, and "Load more" (slice 5) |
| `/assets/group-assets/new` | `NewGroupAssetPage` | assets.asset.configure | Create a group asset: profile, tax book and year, and the members picked from the open assets of the legal entity and class; shows the AST-0535 membership conflict (slice 5) |
| `/assets/group-assets/:id` | `GroupAssetDetailPage` | assets.asset.view | Basis, members (linking to each asset) and the projected yearly group depreciation (AC-37.10); explains that members take no own charge in a run (slice 5) |

Every page has loading (skeleton or `DataTable loading`), empty (`EmptyState`
through `DataTable empty`) and error (`Alert` with "Try again") states. Forms
validate the same rules as the service before sending, show field errors the
service returns, and keep one Idempotency-Key per unchanged form so a retry
replays instead of duplicating.

## API used

`api/client.ts` calls the `/api/v1/assets...` (including
`/assets/depreciation-runs`, `/assets/index-series`,
`/assets/capex-requests` with its decision routes, `/assets/count-sessions`,
`/assets/insurance-report`, `/assets/mid-quarter-test`, `/assets/{id}/transfer`,
`/assets/{id}/transfers` and `/assets/group-assets`) and `/api/v1/leases...` paths of
`services/assets/api/openapi.yaml`. Amounts stay decimal strings; the page
never parses money to floats.

## Not in this slice

Transfer between locations or dimensions, the group's own run line, lease termination, the PWA barcode scanner (the count page
takes pasted or keyed scans), transfer / scrap proposals from a count, routing capex approvals to the approver of each level (wave-4
workflow), run reversal and the workspace tiles
are later slices of PSG-37 or handoffs (see `services/assets/NOT-IMPLEMENTED.md`).

## Needs

None. No npm dependencies were added.

## Run and test

```
cd apps/web
npx vitest --run src/modules/assets
npx tsc --noEmit
```

Slice 8: `/assets/leases/new` has a recognition exemption select (short-term, refused with a purchase option, or low-value with the value of the underlying asset when new stated in USD or EUR and checked against the 5,000 threshold like the service) and a purchase option checkbox; the lease list has an Exemption column; the lease page explains a lease under an election, offers neither Modify nor Terminate and lists no recognition journal on its ledger card, and the modify and terminate pages refuse such a lease when opened by address; `/assets/leases/posting-profile` has the termination gain / loss and the short-term and low-value lease expense accounts.
