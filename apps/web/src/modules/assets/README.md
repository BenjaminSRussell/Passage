# Fixed assets (web module, PSG-37 wave 37)

Module folder: `apps/web/src/modules/assets/`. Discovered by the shell through
`module.ts` (`id: "assets"`). Pages use the shell primitives from
`src/shell/ui` and `pages.module.css` tokens only (docs/design-system.md).

## Screens (slice 1)

| Route | Page | Duty (nav) | What it does |
|---|---|---|---|
| `/assets` | `AssetsPage` | AssetView | Asset register with a status filter and "Load more" over the service cursor |
| `/assets/new` | `NewAssetPage` | AssetMaintain | Create an asset (number, name, class, barcode, quantity) |
| `/assets/:id` | `AssetDetailPage` | AssetView | Master data, books with cost / accumulated / NBV, the projected schedule of the selected book, the transaction log |
| `/assets/:id/books/new` | `AddBookPage` | AssetMaintain | Add a book (method, life, convention, factor, cost); saving posts the acquisition |
| `/assets/:id/dispose` | `DisposePage` | AssetDispose | Sale or scrap of a percentage on every open book, sent with the asset ETag as If-Match; shows the gain or loss per book |
| `/assets/leases` | `LeasesPage` | LeaseMaintain | Leases with liability, IBR and status |
| `/assets/leases/new` | `NewLeasePage` | LeaseMaintain | Recognize an IFRS 16 lease |
| `/assets/leases/:id` | `LeaseDetailPage` | LeaseMaintain | Liability, ROU asset and the full liability / ROU schedule |

Every page has loading (skeleton or `DataTable loading`), empty (`EmptyState`
through `DataTable empty`) and error (`Alert` with "Try again") states. Forms
validate the same rules as the service before sending, show field errors the
service returns, and keep one Idempotency-Key per unchanged form so a retry
replays instead of duplicating.

## API used

`api/client.ts` calls the `/api/v1/assets...` and `/api/v1/leases...` paths of
`services/assets/api/openapi.yaml`. Amounts stay decimal strings; the page
never parses money to floats.

## Not in this slice

Depreciation run, adjustments, transfer / split, revaluation / impairment,
lease modification, count sessions (PWA), insurance, group assets and capex
requests are later slices of PSG-37 (see `services/assets/NOT-IMPLEMENTED.md`).

## Needs

None. No npm dependencies were added.

## Run and test

```
cd apps/web
npx vitest --run src/modules/assets
npx tsc --noEmit
```
