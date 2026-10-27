# NOT-IMPLEMENTED (PSG-37, Wave 37 slices 1 and 2)

Every ticket deliverable and acceptance criterion that slices 1 and 2 do not
build, or build only in part. Status is `built`, `partial`, `later slice of
PSG-37` (in this wave, not yet built) or `handoff` (needs another wave or the
kernel owner).

## Deliverables

| # | Deliverable | Status | Reason |
|---|---|---|---|
| D1a | Books: tax / IFRS / local with per-book profiles, up to `ASSET_MAX_BOOKS` (default 10) per asset | partial | Built: books with method, life, salvage, convention and factor per book; the book limit (AST-0013). Not built: book definitions (ledger, posting layer, posting allowed), the `assets.bookDefinition` table, default books per class (`assets.defaultBooksByClass`) and depreciation profiles as master data; a book carries its method inline |
| D1b | Methods: straight-line life-remaining variant, units of production, manual, Factor and Consumption | later slice of PSG-37 | Built: straight line (months) and declining balance (years, factor 1 to 3, with or without switch to straight line). Units of production and manual need usage inputs and a manual schedule store |
| D1c | MACRS GDS 3/5/7/10/15/20 and ADS tables checked against IRS Pub 946 percentage tables; bonus and §179 first-year adjustments | later slice of PSG-37 | MACRS is the declining-balance-with-switch formula only, verified on the PRD 5-year example (AC-37.2). The Pub 946 tables, ADS and the 100-asset `dep-100` set are not loaded |
| D1d | Conventions: mid-month (1st), mid-quarter (with the BR-37.5 test, AST-0511), half-year next year, half-year on straight-line books | later slice of PSG-37 | Built: None, FullMonth and MidMonth15 for straight line; None and HalfYear for declining balance. Any other convention is rejected (AST-0010), never silently treated as a full month |
| D1e | Asset master: class (FK to AssetClass), location, responsible worker, parent/child (≤ 3 levels), default dimensions | later slice of PSG-37 | Class is a validated code, not an AssetClass table. Location, hierarchy and dimensions are not modelled. Responsible worker is personal data and needs the HR masking policy (wave 42) |
| D1f | Asset number from number sequences (`assets.numberSequence`) | later slice of PSG-37 | The caller supplies `assetNumber` (unique per legal entity); wave-5 sequence allocation is not wired |
| D1g | Prospective change of life or salvage after acquisition (BR-37.4) | partial | Built: after an adjustment, revaluation, impairment or partial disposal a run spreads the remaining basis (cost − salvage − accumulated) over the remaining periods (`domain.RunAmount`). Not built: editing a book's life or salvage; books cannot be edited yet |
| D2a | Acquisition from AP invoice line flagged as asset | handoff | Needs the AP invoice asset flag (wave 18); the service only exposes manual acquisition |
| D2b | Acquisition from project CIP capitalization | handoff | Needs projects (wave 41) CIP events |
| D2c | Acquisition from inventory journal issue | handoff | Needs inventory journal issue events (wave 16/20) |
| D2d | Manual acquisition at book creation | built | `POST /v1/assets/{id}/books` posts an Acquisition transaction for the cost on the in-service date and emits `asset.acquired` |
| D2e | Depreciation run: propose / post, Temporal workflow, per-asset checkpoints, one journal per book and period, advisory lock, rerun rejection, AST-0501..0504 | partial | Built: `POST /v1/assets/depreciation-runs` (one book code of a legal entity, one period), `GET` list and read with per-book lines, `POST .../{id}/post` with If-Match; per-book Due / Exception decisions so one bad book never blocks the others, the sequential rule (AST-0503), rerun rejection (AST-0502), the per-(entity, book, period) advisory lock (AST-0504), stale-book exceptions at posting (AST-0018), `last_depreciated_period`, the `depreciation.posted` event. Not built: the Temporal workflow and per-asset checkpoints (the run is one database transaction), catch-up (`assets.catchUp`), reversal of a posted run, AST-0501 period control (wave 14), `assets.depreciationSummarization`, `ASSET_RUN_BATCH_SIZE`, 500-line pages and the per-asset formula tooltip, the 1M asset-book-periods/h target |
| D2f | Adjustment (`AcquisitionAdj`, `DepreciationAdj`) through `POST /assets/{id}/transactions` | built | One Open book per request, Idempotency-Key, `asset.adjusted` event; 0 ≤ accumulated ≤ cost is kept. Bonus / §179 first-year adjustments stay with D1c |
| D2g | Transfer and split (incl. IC transfer, waves 36/40), AST-0507 | later slice of PSG-37 | Not built |
| D2h | Revaluation (OCI or P&L) and impairment with reversal cap (FR-37.9, AST-0512) | partial | Built: revaluation as a signed cost change and impairment (write-down, and a reversal capped at the impairment recorded and at the carrying amount without impairment, with an AST-0512 warning). Not built: the OCI reserve vs P&L split (a GL posting, D2j) and revaluation methods that restate accumulated depreciation (gross / elimination) |
| D2i | Disposal: sale and scrap, full or partial percentage, gain/loss | built | `POST /v1/assets/{id}/dispose` disposes the same share on every Open book, with If-Match, a disposal date and one `asset.disposed` event per book. The BR-37.6 guard is built: a disposal dated before the end of a book's last depreciated period is AST-0508. Not built: depreciate-to-disposal-date (`assets.depreciateOnDisposal`), the offered reversal set for a backdated disposal, disposal approval (`assets.disposalApproval`), the sale invoice link (wave 29) and reversal of a disposal |
| D2j | GL journal for every transaction (acquisition, adjustment, depreciation run, disposal) | handoff | Needs journal posting through the ledger (waves 11/12) and account determination (`assets.postingProfile`). `asset_transaction.journal_entry_id` and `depreciation_run.journal_entry_id` stay NULL until then |
| D3a | Asset budgeting: depreciation forecast and `depreciation-forecast-publish` | later slice of PSG-37 (forecast) / handoff (budget source to wave 38) | Forecast depends on the run and planned acquisitions (FR-37.17) |
| D3b | CIP / under-construction from projects | handoff | Wave 41 projects |
| D4a | Lease create with schedule, liability and ROU (IFRS 16, arrears and advance, 1/2/4/12 payments a year) | built | `POST /v1/leases`, `GET /v1/leases`, `GET /v1/leases/{id}`, `lease.recognized` event |
| D4b | Lease modification / remeasurement (BR-37.8, AST-0516) | later slice of PSG-37 | `POST /leases/{id}/modify` not built |
| D4c | Termination, short-term and low-value elections, options, ASC 842 operating single-cost (flag), lessor party | later slice of PSG-37 | Only the IFRS 16 path is built; the ROU asset is not created as an asset record |
| D4d | Monthly lease schedule posting (`lease-schedule-post`) and GL postings for ROU and liability | handoff | GL posting needs the ledger (waves 11/12). Schedule lines are stored with `posted_journal_id` NULL |
| D4e | Effective monthly rate option (`lease.periodicRateMethod`) | later slice of PSG-37 | The periodic rate is nominal / payments per year only |
| D4f | `lease-25` accountant workbook fixtures | later slice of PSG-37 | Not in the repo; AC-37.6 is verified |
| D5 | Physical inventory: count sessions, PWA barcode scanning, AST-0525 | later slice of PSG-37 (API) / handoff (PWA app) | Barcodes are stored and unique per legal entity; the count API is not built and the PWA lives in another app |
| D6a | Additions and components (FR-37.14) | partial | Built: `POST` / `GET /v1/assets/{id}/additions` (Improvement, Component, Accessory; description, quantity, serial, date; at most 500, AST-0020); a financial addition posts an AcquisitionAdj on the selected or every Open book in the same transaction, linked by `source_type = addition` (AC-37.12). Not built: promote-to-sub-asset with the pro-rata move of cost and accumulated depreciation (BR-37.13; needs the parent/child hierarchy of D1e and split of D2g) and linking an addition to an existing acquisition adjustment |
| D6b | Insurance policies and index series (FR-37.15) | partial | Built: `PUT /v1/assets/index-series/{code}` and `GET /v1/assets/index-series` (per country and class, yearly values); `POST` / `GET /v1/assets/{id}/insurance-policies` with the indexed value for a valuation year (AC-37.9), the latest-year fallback (AST-0531) and the under-insured flag (AST-0532) as warnings. Not built: insurance types and insurance companies as master data (types are the four seeded codes, the insurer is a party UUID not checked against parties), the `insurance-index-update` job (the indexed value is computed when the policy is recorded, not re-indexed yearly), the `insurance-expiry-check` job and the `insurance.expiring` event, the cross-asset insurance report (7.37.14), choosing the series from the asset's class and country automatically |
| D7 | Group assets for pooled tax depreciation (FR-37.16) | later slice of PSG-37 | Not built. DE / UK / US pool profiles come from the wave-48 packs |
| D8 | Capex appropriation requests (FR-37.17) | partial | Built: `POST` / `GET /v1/assets/capex-requests`, `GET .../{id}` with variants (amounts per year, in-service date, profile assumption), and `POST .../{id}/submit` with If-Match: Draft → Submitted → InApproval with the selected variant and the approval level by its total (AC-37.11 routes 320,000 to CFO). Not built: the wave-4 approval workflow and the approve / reject / postpone / release / close commands (handoff, wave 4), the amount change after approval that returns a request to Submitted (BR-37.12, AST-0542; `domain.NeedsReapproval` exists without an API), the `capexrequest.approved` event and planned acquisitions for the forecast (D3a), NPV / IRR on variants, the program position (FR-38.21), linked assets / project and spend tracking with the overrun warning (AST-0541, `capex-overrun-check`), per-entity `capex.approvalLevels` and currency conversion (the default bands are compared in the request currency). Interpretation: BR-37.12 is read as Postponed → Draft only; Rejected is terminal |
| Deploy | Fixed assets workspace UI | partial | `apps/web/src/modules/assets` ships the register, new asset, asset detail (books, schedule, transactions, additions, insurance policies), add book, adjust, new addition, new insurance policy, dispose, depreciation runs (list, propose, review and post), index series, capex requests (list, new with variants, detail and submit), and lease list / new / detail screens. Not built: workspace tiles (pending acquisitions, unreconciled difference), count session |
| Deploy | Demo entity opening NBVs migrated | later slice of PSG-37 | Needs the `migration-nbv` loader and the migration framework (wave 49 pipeline) |
| Reports | Inquiries and reports of 7.37.14 (history sheet, forecast, acquisitions / retirements, tax 4562 data, lease roll-forward, insurance, count list, reconciliation, group members, capex pipeline) | later slice of PSG-37 | Built: the asset register list, the per-asset transaction list, the per-asset insurance list and the capex request list by status |
| Documents | Printed documents of 7.37.15 (asset tag label, transfer slip, scrap certificate, history sheet, lease schedule, count sheet, capex record) | later slice of PSG-37 / handoff (wave 48 layouts) | Not built |
| Security | Duties AssetConfigure, AssetDisposeApprove, AssetCount, CapexRequestApprove and the segregation-of-duties conflicts of 7.37.17 | later slice of PSG-37 / handoff (SoD needs the wave-2 policy engine) | Built: AssetView, AssetMaintain, AssetDispose, LeaseMaintain, AssetDepreciate, AssetPostJournal, AssetInsuranceMaintain and CapexRequestCreate on their routes. Lease reads use LeaseMaintain because the PRD lists no lease view duty; run, insurance and capex reads accept AssetView or the maintaining duty |
| Ops | Jobs: `asset-gl-reconcile`, `mid-quarter-test`, `depreciation-forecast-publish`, `insurance-index-update`, `insurance-expiry-check`, `capex-overrun-check` | later slice of PSG-37 / handoff (reconcile needs ledger balances) | Not built |
| Ops | Flags `assets.physical-inventory`, `assets.asc842-operating`; `ASSET_RUN_BATCH_SIZE` | later slice of PSG-37 | No feature-flag client in the kernel; the run posts in one transaction without batching, so the chart sets only `ASSET_MAX_BOOKS` |
| Ops | Rollback via posted-run reversal API | later slice of PSG-37 | Runs post, but the reversal API is not built (see D2e) |
| Ops | Atlas expand/contract migrations; yearly partitioning of `asset_transaction` | handoff | The service uses `dbx.Migrate` with versioned SQL; Atlas and partitioning are deployment concerns |

## Acceptance criteria

| AC | Status | Reason |
|---|---|---|
| AC1 | partial | The PRD worked examples (AC-37.1 straight-line mid-month, AC-37.2 5-year MACRS) match to the cent (`testdata/worked-examples.json`) and a property test proves Σ depreciation = cost − salvage. The 100-asset `dep-100` set against the Pub 946 tables is not built |
| AC2 | partial | AC-37.5 verified in the domain and on two books in the store test; successive partial disposals keep the projections equal to Σ transactions. The `disposal-30` fixtures are not built |
| AC3 | partial | AC-37.6 verified (liability 43,294.77, Y5 interest 476.18, closing 0.00) plus properties for random terms; the `lease-25` workbook is not in the repo |
| AC4 | partial / handoff | The subledger half is tested: Σ transaction amounts per book equal the book's cost and accumulated depreciation (`TestStore_DisposalAcrossBooksReconciles`, and with runs and adjustments in `TestStore_DepreciationRunLifecycle`). Reconciling to the GL needs journals and GL balances (waves 11/12) and the reconcile job |
| AC5 | built | This file lists every deliverable and AC not built |
| AC6 | built | `go vet ./... && go test ./...` for services/assets; the web module passes `vitest --run` and `tsc --noEmit`; OpenAPI and AsyncAPI are checked against the router and event payloads by tests; the Helm chart lints |

PRD acceptance criteria of this slice: AC-37.4 is verified on a small set (one
run per book code and period, Σ lines = run total, rerun rejected), not on
10,000 assets; AC-37.9 and AC-37.12 are verified end to end in the store tests;
AC-37.11 is verified up to the routing (CFO level) — the approval and the
forecast half need the wave-4 workflow and D3a.

Test-plan items not built: run throughput (1M asset-book-periods/h), the
`dep-100`, `disposal-30`, `lease-25` and `migration-nbv` fixtures, the schedule
API latency SLO, contract tests against the journal API, and the projection ⇄
events reconciliation for the run.

## Notes for the kernel owner (wave 0) and other waves

- Idempotency uses the `idempotency_keys` table (`dbx.IdempotencyDDL`) from
  the store, claimed at the start of the command's transaction. The kernel has
  no `httpx` idempotency middleware yet; when it lands, the store helper can
  move behind it.
- `ETag` / `If-Match` are set by the service (`"<version>"`) on assets,
  depreciation runs and capex requests; leases have no update command yet, so
  they carry no ETag.
- `depreciation.posted` carries `bookCode` in place of the PRD's
  `bookDefinitionId` until book definitions exist (D1a).
- `asset.adjusted` is an event the PRD table does not list; it is published so
  consumers see every change of a book's balances.
