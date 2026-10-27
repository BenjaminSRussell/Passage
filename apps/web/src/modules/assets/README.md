# Fixed assets (web module, PSG-37 wave 37)

Module folder: `apps/web/src/modules/assets/`. Discovered by the shell through
`module.ts` (`id: "assets"`). Pages use the shell primitives from
`src/shell/ui` and `pages.module.css` tokens only (docs/design-system.md).

## Screens (slices 1 and 2)

| Route | Page | Duty (nav) | What it does |
|---|---|---|---|
| `/assets` | `AssetsPage` | AssetView | Asset register with a status filter and "Load more" over the service cursor |
| `/assets/new` | `NewAssetPage` | AssetMaintain | Create an asset (number, name, class, barcode, quantity) |
| `/assets/:id` | `AssetDetailPage` | AssetView | Master data, books with cost / accumulated / NBV and the last depreciated period, the projected schedule of the selected book, the transaction log, additions and insurance policies (with the under-insured flag) |
| `/assets/:id/adjust` | `AdjustPage` | AssetPostJournal | Acquisition or depreciation adjustment, revaluation or impairment on one open book; shows the AST-0512 warning when a reversal is capped |
| `/assets/:id/additions/new` | `NewAdditionPage` | AssetMaintain | Record an improvement, component or accessory; a financial one posts its cost on the chosen open books |
| `/assets/:id/insurance/new` | `NewPolicyPage` | AssetInsuranceMaintain | Record a policy with an optional index series; shows the indexed value and the AST-0531 / AST-0532 warnings |
| `/assets/:id/books/new` | `AddBookPage` | AssetMaintain | Add a book (method, life, convention, factor, cost); saving posts the acquisition |
| `/assets/:id/dispose` | `DisposePage` | AssetDispose | Sale or scrap of a percentage on every open book, sent with the asset ETag as If-Match; shows the gain or loss per book |
| `/assets/depreciation-runs` | `RunsPage` | AssetDepreciate | Runs newest first, and the form to propose one for a book code, legal entity and period |
| `/assets/depreciation-runs/:id` | `RunDetailPage` | AssetView | Totals, per-book lines with exceptions (AST-0503 and others), Post with the run ETag as If-Match |
| `/assets/index-series` | `IndexSeriesPage` | AssetInsuranceMaintain | Insured-value index series and the form that loads yearly values (`PUT`) |
| `/assets/capex` | `CapexListPage` | CapexRequestCreate | Capex requests with a status filter and "Load more" |
| `/assets/capex/new` | `NewCapexPage` | CapexRequestCreate | Create a request with one to ten variants (amounts per year as `2026=200000.00` lines) |
| `/assets/capex/:id` | `CapexDetailPage` | CapexRequestCreate | Request, variants with totals, and Submit of a Draft with the chosen variant (If-Match); shows the approval level it was routed to |
| `/assets/leases` | `LeasesPage` | LeaseMaintain | Leases with liability, IBR and status |
| `/assets/leases/new` | `NewLeasePage` | LeaseMaintain | Recognize an IFRS 16 lease |
| `/assets/leases/:id` | `LeaseDetailPage` | LeaseMaintain | Liability, ROU asset and the full liability / ROU schedule |

Every page has loading (skeleton or `DataTable loading`), empty (`EmptyState`
through `DataTable empty`) and error (`Alert` with "Try again") states. Forms
validate the same rules as the service before sending, show field errors the
service returns, and keep one Idempotency-Key per unchanged form so a retry
replays instead of duplicating.

## API used

`api/client.ts` calls the `/api/v1/assets...` (including
`/assets/depreciation-runs`, `/assets/index-series` and
`/assets/capex-requests`) and `/api/v1/leases...` paths of
`services/assets/api/openapi.yaml`. Amounts stay decimal strings; the page
never parses money to floats.

## Not in this slice

Transfer / split, lease modification, count sessions (PWA), group assets,
capex approval (wave-4 workflow), run reversal and the workspace tiles are
later slices of PSG-37 or handoffs (see `services/assets/NOT-IMPLEMENTED.md`).

## Needs

None. No npm dependencies were added.

## Run and test

```
cd apps/web
npx vitest --run src/modules/assets
npx tsc --noEmit
```
