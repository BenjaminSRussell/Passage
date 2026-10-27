# NOT-IMPLEMENTED — productcost (PSG-39, wave 39)

Slices 1 to 4 are a stateless computation service. Slice 1: roll-up, mass roll, variance
decomposition and activation preview. Slice 2: the MakeToOrder and ByLineType explosion modes
with phantom and make-to-order BOM lines, route scrap, mixed cost over procurement
alternatives, COGS and price-difference posting previews by cost component, sales order cost
estimates, baseline-versus-candidate comparison and the standard cost reconciliation. Slice 3:
costing sheet Basis and Total nodes, the Group valuation view, the costing version lifecycle and
the standard cost conversion checklist, still stateless. Slice 4: the ProfitCenter valuation
view, ad-hoc estimates from costing models and the sales order estimate versions (refresh and
freeze at confirmation), also stateless.
Everything below is either a later slice of PSG-39 or a handoff to another owner. Status is
one of `built`, `partial`, `later slice of PSG-39` or `handoff`.

## Deliverables (ticket section 7.39.3 and the deliverables list)

| # | Deliverable / FR | Status | Reason |
|---|---|---|---|
| D1 | Cost groups and component structure CRUD (FR-39.1), ≤ 50 groups, ≤ 20 components per tenant | later slice of PSG-39 | Needs the persisted `cost_component` table and maintenance endpoints. Slices 1–2 take component codes as strings in the request. |
| D2 | Costing sheet as a persisted tree per site and version (FR-39.2): absorption account, absorption at production end, sheet designer | partial | Slice 3 evaluates all four node types per item in sheet order: Basis and Total subtotals (returned in `subtotals`), Surcharge on named components or on an earlier subtotal (`basisRef`), Rate per routing hour or per unit; references are validated (400, or CST-0708 in the domain). The sheet is still sent with each item (no parent/child tree, no per-site-and-version storage, no `POST /costing/sheets`), there is no absorption account or absorption posting, and `normalCapacityHours` on rate nodes is not built. |
| D3 | Costing versions with the state machine Open → Blocked → Active → Closed, fallback principle, site scope (FR-39.3, BR-39.2) | partial | Slice 3 applies the state machine statelessly: `POST /v1/costing/versions:transition` (block, unblock, close; CostingMaintain) and `POST /v1/costing/versions:activate` (CostingActivate), with CST-0711 for Simulation, CST-0715 for other moves and CST-0716 for a SpecifiedVersion fallback. Not built: `POST /costing/versions` with persistence and Idempotency-Key, effective date, per-site activation, blocking edits of pending prices while Blocked, the SpecifiedVersion fallback lookup during a roll (rolls take `fallbackToActive` only), and the SoD check that the author of pending prices cannot activate them (needs stored pending prices with their author). |
| D4 | Roll-up algorithm (FR-39.4): multilevel, single-level, make-to-order and by-line-type explosion, phantoms in place (BR-39.3), BOM and route scrap (BR-39.6), worked example | built | Mode semantics are in the README and under "Interpretations" below. |
| D4c | Costing lot size from item default order settings (BR-39.5, `costing.lotSizeSource`) | handoff (wave 8 item settings) | The lot size is a request input (`lotSize` or `costingLot`). The item-settings lookup needs the wave-8 item data. |
| D4d | Route activity validity: CST-0705 inactive route at site and date; BOM line to operation links | handoff (wave 9 route versions) | Routes come from wave 9 in later slices; slices 1–2 take operations in the request. Without the line-to-operation link, all BOM material is treated as issued before the first operation (see "Interpretations"). |
| D4e | Explanation lines per calculation and `GET .../explain` with lazy levels | later slice of PSG-39 | Explanation needs the stored calculation (`cost_calculation`). |
| D5 | Mass roll (FR-39.5): level batches on Temporal, per-level checkpoints, `run_id` persistence, `rollrun.completed` event, roll diagnostics report | later slice of PSG-39 | Slice 1 rolls in-process with worker goroutines per level. Durable runs, checkpoints and the job status URL come with persistence and the Temporal worker. |
| D6 | Activation (FR-39.6): atomic activation per version and site, advisory lock against running rolls (BR-39.7, CST-0710), reversal as a new activation, journal posting, `standardcost.activated.v1` | later slice of PSG-39 | Slice 1 provides the preview only (`POST /v1/costing/activations:preview`). Posting needs wave-16 on-hand and the wave-12 ledger. |
| D6a | On-hand revaluation from the wave-16 projection at the activation timestamp (BR-39.8) | handoff (wave 16 projection) | The preview takes quantities from the caller. |
| D6b | Open production orders keep their estimate; `newStandardForOpenOrders` flag (FR-39.6 note, parameter table) | later slice of PSG-39 | Needs production order data (wave 21). |
| D7 | Purchase price variance posting at receipt and invoice (FR-39.7), by component | partial | `POST /v1/costing/postings:price-difference` returns the PPV split by the standard's components (FR-39.15) as a balanced journal preview. Posting to the ledger, the receipt and invoice adjustment from wave-18 events, the `costing.ppvSplit` site option and `VarianceEntry` rows of type PurchasePrice are later. |
| D8 | Production variance decomposition on order end (FR-39.8) | built (computation only) | The order-end event consumer, `VarianceEntry` persistence, posting profiles, and `variance.posted.v1` are later. Reopen reversal (BR-39.10) is later. |
| D9 | Variance analysis workspace: pivots by order, item, site, period, type, component; drill to consumption; monthly variance report (FR-39.9) | later slice of PSG-39 | Needs persisted variance entries. |
| D10 | Cost estimate comparison and what-if simulations (FR-39.10, `costing.whatif`) | partial | `POST /v1/costing/comparisons` rolls a baseline and a candidate (what-if) selection supplied by the caller and compares them item by item with component deltas. Comparing stored versions or dates (`GET /costing/compare?a=&b=`), persisted Simulation versions and the `costing.whatif` flag need versions (D3). |
| D11 | Standard cost reconciliation nightly (FR-39.11, job `standard-cost-reconcile`) | partial | `POST /v1/costing/reconciliations` reconciles Σ on-hand × standard to GL inventory per site from caller-supplied lines and balances, and counts differences in `productcost_reconciliation_differences_total`. The nightly job, reading on-hand and active standards from the wave-20 value report and GL balances from the ledger, and the alert rule are later. |
| D12 | Standard cost conversion checklist (FR-39.12): ConversionRun, steps, blockers (CST-0731, CST-0732), reverse conversion | partial | Slice 3: `POST /v1/costing/conversions:validate` (blockers per `costing.conversionBlockers`, revaluation preview per position in the currency, AC-39.10) and `POST /v1/costing/conversions:advance` (steps in order, user from the principal, server timestamp, CST-0731 on close and activate per BR-39.11, CST-0735, CST-0736). Not built: ConversionRun persistence and Idempotency-Key on `POST /costing/conversions`, reading on-hand, values, in-transit transfers and started orders from waves 16, 20, 21 (the caller supplies them), running the inventory close itself (wave 20), posting the revaluation journal, `conversion.activated.v1` (so the wave-20 engine does not yet stop settling converted items), the Reversed status and reverse conversion, and the conversion record document. |
| D13 | Ad-hoc cost estimates with costing models (FR-39.13): CostingModel, AdHocEstimate, formula grammar shared with FR-38.17 | partial | Slice 4: `POST /v1/costing/estimates` prices a caller-supplied costing model (characteristics with defaults; Item, Activity, Subcontract and Overhead lines with quantity and optional price formulas) for a Quote, InternalOrder or ProjectTask, applies the costing sheet and rounds the split and total to the currency (CST-0702, CST-0703, CST-0764). The formula grammar is local (`formula.go`) because wave 38 does not yet publish FR-38.17 as a shared package. Not built: CostingModel and AdHocEstimate persistence with versions and `Idempotency-Key` (the header is not read), resolving prices from a stored costing version with its fallback principle (the caller sends `prices` with their source), comparing estimates through FR-39.10, and copying an estimate as plan to an internal order (wave 38), a project estimate (wave 41) or quote cost (wave 27). |
| D14 | Mixed costing (FR-39.14): ProcurementAlternative, PUT alternatives, `derive-ratios` from 12 months of receipts | partial | `POST /v1/costing/mixed-costs` rolls each alternative (supplied as a catalog item) and returns the ratio-weighted cost and split, with 100.00% validation (CST-0741) and 0.00% exclusion (BR-39.12). `DeriveRatios` computes ratios from receipt quantities but no route or receipt feed uses it. Persistence of alternatives per item, site and version, `PUT /costing/procurement-alternatives/{itemId}`, the `costing.mixedCosting` and `costing.mixingRatioSource` options, the quarterly `mixing-ratio-derive` job and the mixed-cost composition inquiry are later. |
| D15 | COGS and price-difference posting by component (FR-39.15, `costing.cogsComponentPosting`, `costing.ppvSplit`) | partial | `POST /v1/costing/postings:cogs` and `POST /v1/costing/postings:price-difference` return balanced journal previews in the request currency (components sum to the total, BR-39.13). Posting on wave-28 sales issues, the per-site options (off by default) and the ledger accounts from posting profiles are later. |
| D16 | Sales order cost estimates (FR-39.16): calculation type SalesOrder, margin, freeze at confirmation, `salesorderestimate.completed.v1` | partial | `POST /v1/costing/sales-order-estimates` rolls the caller-supplied order structure at the order quantity and returns type `SalesOrder`, cost, split and margin (AC-39.13). Slice 4 adds the version lifecycle statelessly: `POST /v1/costing/sales-order-estimates:refresh` versions the estimate by (lineId, configHash), superseding Pending and Frozen versions on a configuration change with CST-0751, and `:freeze` freezes the current version at confirmation (idempotent; CST-0763 without a usable estimate). Not built: storing the versions as `cost_calculation` rows of type SalesOrder linked to the line (the caller holds the history), the `sales-order-estimate-refresh` job on the wave-27 configuration-change event, `costing.orderEstimateAsStandard` / `use-order-estimate-as-standard` for MTO production variances, `salesorderestimate.completed.v1`, the sales order margin inquiry and the estimate document. |
| D17 | Parallel valuation views Legal / Group / ProfitCenter (FR-39.17) | partial | Slice 3: `options.view` Legal or Group. The Group view costs an `intercompany` purchased item at its supplier's Legal-view cost (`legalCost`) and warns CST-0761 without it (BR-39.15, AC-39.14). Slice 4: the ProfitCenter view costs a `profitCenterSource` purchased item at its `transferPrice`, or at `legalCost` plus `costPlusPct`, and fails it with CST-0762 without either. Not built: the transfer-price table per profit-center pair (the caller sends the price or cost-plus % on the item), storing the Group and ProfitCenter standards alongside Legal per item and site (`valuation_view` on `cost_calculation`), group cost per IC receipt layer (wave 40), the `valuation-view-roll` job, posting to ledger layers under `costing.valuation-views-post`, the `costing.valuationViews` entity option, and the currency conversion at the receipt rate (the caller sends `legalCost` converted). |
| D18 | Parallel inventory valuation ledgers, flag `costing.global-inventory-accounting` (FR-39.18) | later slice of PSG-39 | Flag-gated, Could priority. Depends on the wave-20 engine per ledger. |
| D19 | Reports and documents: explain export (PDF/XLSX), activation revaluation report, conversion record, monthly variance report, sales order estimate sheet (section 7.39.15) | later slice of PSG-39 | Need the persisted data and the document service. |
| D20 | Migration from AX/SAP (section 7.39.11): load active standards as Active calculations with diagnostic `Migrated`, comparison report vs legacy | handoff (migration-framework) | Needs the migration pipeline in the integration database. |
| D21 | Costing workspace web (Deploy): roll, activate, compare, diagnostics tile | later slice of PSG-39 (partial) | `apps/web/src/modules/productcost/` has the workspace landing page, the cost roll-up page (single roll with the own/rolled split, mass roll with per-item diagnostics, all four explosion modes, and from slice 3 the Legal/Group valuation view and the costing sheet subtotals), the production variance page, the activation preview page, from slice 2 the mixed cost, sales order estimate, component postings, estimate comparison and standard reconciliation pages, from slice 3 the costing versions page (create, block, unblock, activate, close) and the standard cost conversion checklist page (validate, revaluation preview, steps with user and time), and from slice 4 the ad-hoc estimate page (`/productcost/estimates`), the sales order estimate versions page (`/productcost/sales-order-versions`: refresh, version history, confirm) and the ProfitCenter view on the roll-up page, all on the stateless endpoints. Item structures are entered as JSON and the version list lasts for the session because nothing is stored yet. The sheet designer, lazy explain tree, activate action with revaluation confirmation and posting, persisted variance pivot and diagnostics tile need D2, D4e, D6 and D9. The demo plant (standard cost, monthly variance report) needs D6 and D9. |
| D22 | Demo plant on standard cost with a monthly variance report (Deploy) | later slice of PSG-39 | Needs D6, D8 persistence and D9. |

## Acceptance criteria

| AC | Status | Reason |
|---|---|---|
| AC1 Roll-up fixtures: 50 multi-level products match a reference workbook across cost components | handoff (Finance) | No reference workbook is in the repository and `services/productcost/testdata/` does not exist. The worked example (AC-39.1) is an exact test. The 50-product fixture waits for the workbook. |
| AC2 Activation revalues inventory exactly and posts the revaluation to the GL | later slice of PSG-39 | The preview arithmetic is exact and tested (AC-39.4 shape: 1,000 × 0.50 = 500.00 Dr Inventory / Cr Cost change variance). Posting needs D6. |
| AC3 Variance decomposition sums to total for 200 ended production orders | partial | Identity is exact and property-tested for the decomposition (the four types sum to total per component and per order). The 200 ended fixture orders (`variance-200`) are not in the repository. |
| AC4 Mass roll of 50k items in < 15 min | partial | `TestMassRoll_FiftyThousandItemsEightLevels` rolls 50,000 items over 8 levels with 4 workers in the domain and asserts < 15 min. Over HTTP the platform caps a request body at 4 MiB, so a 50,000-item selection does not fit in one stateless `POST /v1/costing/rolls`; the real mass roll reads the selection from stored BOMs (D5). The real BOM shape, the 2 GiB worker memory limit and the benchmark on the target cluster are not measured. |
| AC5 Every deliverable has code or is listed here | built | This file lists every deliverable not built. |
| AC6 `go vet` and `go test` pass; web vitest and tsc pass; OpenAPI/AsyncAPI match code; chart lints | built | `TestOpenAPI_MatchesRoutes` checks every served route and its duty against `api/openapi.yaml`; `TestMigrationsApplyWithRLS` applies the schema and checks RLS (with `PASSAGE_TEST_DATABASE_URL`). AsyncAPI lists no channels because slices 1–3 publish nothing. |

## Ticket acceptance criteria (AC-39.x)

| AC | Status | Reason |
|---|---|---|
| AC-39.1 worked SA-200 / FG-100 = 104.32, split 21.20 / 54.00 / 2.12 / 27.00 | built | Exact test in `internal/domain`. |
| AC-39.2 50 fixture products match the workbook to the cent | handoff (Finance) | See AC1. |
| AC-39.3 50k items across 8 levels, diagnostics for every error | partial | The run and the diagnostics are built and tested. Measurement on target hardware is pending. |
| AC-39.4 Activation journal Dr Inventory 500.00 / Cr Cost change variance 500.00 and the value report ties | partial | The journal and its preview are built and tested. The value report tie-out needs D6 and D11. |
| AC-39.5 Order variance: QV 40.00, SV 7.50, LSV 0.00, total 47.50 plus labor PV | built | Tested. The test order also has a labor price variance of 4.00 (total 51.50). The ticket's "plus any labor PV" is satisfied by the decomposition. |
| AC-39.6 200 ended fixture orders, Σ types = total | partial | Identity is property-tested. The fixture orders are not in the repository (AC3). |
| AC-39.7 PPV 2.00 for 10 at 5.20 vs 5.00 posts to DM | partial | The price-difference preview returns Dr PPV-DM 2.00 / Cr PriceDifferenceClearing 2.00 (tested over HTTP). Posting is D7. |
| AC-39.8 Cyclic BOM errors with the cycle path, the run continues | built | Tested in the planner tests and the mass-roll API test. |
| AC-39.9 Σ on-hand × standard = GL inventory per site | partial | The reconciliation computation is tested over HTTP. The month-end run on stored data is D11. |
| AC-39.10 Conversion revaluation 75.00 | partial | The checklist validation returns Dr Inventory 75.00 / Cr Cost change variance 75.00 for 500 at FIFO 4,925.00 and standard 10.00 (tested in the domain and over HTTP), and activation is refused before the October close. Posting the journal and "later issues use 10.00" need D12 persistence and the wave-20 engine. |
| AC-39.11 Mixed cost 11.00, split totals 11.00 | built (computation) | `RollMixed` and `POST /v1/costing/mixed-costs` are tested with the AC values. Persisted alternatives are D14. |
| AC-39.12 COGS lines 212.00 / 540.00 / 21.20 / 270.00 total 1,043.20 | partial | The COGS preview returns the four lines and the Inventory credit (tested). Posting on the issue is D15. |
| AC-39.13 Sales order margin 540.00 (22.50%) and freeze | built (computation) | Margin 540.00 and 22.50% are tested in the domain and over HTTP, and slice 4 freezes that estimate on confirmation (`:freeze`, tested in the domain and over HTTP). Storing the frozen version on the line is D16. |
| AC-39.14 Group view uses 100.00 vs Legal 120.00 | built (computation) | Tested in the domain and over HTTP: the parent's Group-view material is 100.00 and its Legal-view material 120.00. Storage per view is D17. |

## Platform and operations handoffs

| Item | Status | Owner / reason |
|---|---|---|
| Migrations apply on an empty database; RLS verified on Postgres | built | `migrations/migrations_test.go` applies `0001_costing.sql` twice and checks that every table leads with `tenant_id`, enables and forces row-level security, has the tenant policy (`passage.tenant_id`) and, when it carries `legal_entity_id`, the legal-entity scope policy. It runs when `PASSAGE_TEST_DATABASE_URL` is set. No repository writes these tables until the persistence slice. |
| Store package, Postgres repositories, idempotency (`Idempotency-Key`, `idempotency_keys`), outbox writes | later slice of PSG-39 | Stateless in slices 1–2, so no writes and no state change needs an outbox row. |
| Role grants for the costing duties | handoff (policies/ owner, wave 2) | Routes enforce the PRD duties (section 7.39.17): `CostingCalculate` (calculations, rolls, mixed costs, sales order estimates, posting previews), `CostingView` (comparisons, reconciliations), `CostingActivate` (activation preview), `CostingSystem` (variance compute, a service-account duty; the variance page is usable only by a user who holds it until the order-end consumer exists). The role-to-duty grants live in the policies directory, which this wave does not own. `CostingMaintain` (version block, unblock, close) and `CostingActivate` (version activation, conversion checklist) from slice 3. `CostingCalculate` also covers the slice-4 ad-hoc estimates and the sales order estimate refresh and freeze. CostingConfigureLedgers has no routes yet. |
| PRD endpoint paths | later slice of PSG-39 | Slices 1–2 have no stored versions, so they serve stateless `POST` routes: `/v1/costing/rolls`, `/activations:preview`, `/comparisons`, `/mixed-costs`, `/reconciliations` and the two posting previews. With versions (D3) they become the PRD's `POST /costing/versions/{id}/roll`, `POST /costing/versions/{id}/activate` with `dryRun=true`, `GET /costing/compare?a=&b=` and `PUT /costing/procurement-alternatives/{itemId}`, plus `GET /calculations/{id}/explain` and `GET /variances`. `POST /v1/costing/sales-order-estimates` and `POST /v1/costing/estimates` already have the PRD paths; the estimates `Idempotency-Key` comes with storage. Slice 4's `:refresh` honours the (lineId, configHash) key statelessly against the history the caller sends, and `:refresh` and `:freeze` become writes on the stored line estimate. Slice 3 adds `POST /v1/costing/versions:transition`, `/versions:activate`, `/conversions:validate` and `/conversions:advance`, which become `POST /costing/versions`, `POST /costing/versions/{id}/activate`, `POST /costing/conversions` and `POST /costing/conversions/{id}/steps/{step}` with storage. |
| Posting precision per currency | built | The variance compute, the activation preview, the COGS and price-difference previews, the sales order estimate, the reconciliation and the conversion preview take an optional `currency` and round to its ISO 4217 minor unit (BR-39.1, `money.MinorUnits`). |
| Helm: ServiceMonitor template | built | `templates/servicemonitor.yaml` behind `serviceMonitor.enabled` scrapes `/metrics` on the admin port. |
| Helm: umbrella chart and Argo CD app | handoff (deploy/ owner) | `deploy/` is not owned by this wave. |
| Event publication: six events in section 7.39.5 | later slice of PSG-39 | AsyncAPI declares no channels until the outbox writes exist. |
| Metrics on the admin port | built | Counters on `Metrics()`; the entrypoint wires the admin port. |
| Reference workbook and fixture directory `testdata/` (rollup-50, variance-200, activation-demo) | handoff (Finance / test-infra owner) | The fixtures and reference workbook are not in the repository. |
| Legacy AX/SAP migration (section 7.39.11) | handoff (migration-framework) | D20. |

## Interpretations of the PRD (for the business owner)

- **Lot size variance sign.** FR-39.8 writes LSV = standard per unit × Q_raf − estimate per lot ÷ Q_order × Q_raf,
  which is negative when a short order absorbs a full setup. The PRD also says unfavourable is positive, so the
  service computes LSV = estimate per lot ÷ Q_order × Q_raf − standard per unit × Q_raf (a short order is
  unfavourable). The identity LSV + QV + PV + SV = total holds either way.
- **Rounding residual.** FR-39.8 assigns the residual to the QV of the largest planned line. The service rounds per
  cost component and assigns each component's residual to that component's QV, so the identity also holds per
  component. Per-line detail (VarianceEntry.detail) arrives with persistence (D8).
- **Setup on a short order.** In the AC-39.5 shape, the setup line's actual 40.00 against its 36.00 share for 90
  units is reported as price variance on the labour component (the "plus any labor PV" of AC-39.5), giving a total
  of 51.50.
- **Rate nodes per hour** use routing hours per unit: run time plus setup time spread over the costing lot.
- **Explosion modes.** FR-39.4 names the modes without defining them. The service reads
  them as: `SingleLevel` takes every child at its active standard ("children at their active
  cost"); `MakeToOrder` explodes the phantom and make-to-order lines; `ByLineType` explodes
  phantom lines only. In `MakeToOrder` and `ByLineType` a purchased child takes its version
  price (it has no structure to explode), so only manufactured children fall back to their
  active standard. The business owner should confirm `ByLineType`.
- **Phantoms in place.** BR-39.3 applies in every mode. The phantom's direct costs and
  routing hours join the parent's own level and the parent's costing sheet applies to them;
  a sheet on the phantom item itself is not applied when it is exploded in place.
- **Route scrap and material.** BR-39.6 compounds operation scrap backward. Because BOM
  lines are not linked to operations yet (D4d), all material is treated as issued before the
  first operation and is scaled by that operation's accumulated factor; run time and routing
  hours use each operation's own factor; setup is not scaled.
- **Basis and Total nodes are positional.** FR-39.2 names the node types without an
  evaluation order. The service evaluates the sheet top to bottom, so a Basis node is the
  value of its components at its position (it includes an earlier surcharge only if that
  surcharge charges one of its components) and a reference must point upward. This is the
  AX costing sheet reading; the business owner should confirm it.
- **Activate from Open.** BR-39.2 draws Open → Blocked → Active. Because
  `costing.blockPending` defaults to false, a version need not be blocked first, so
  activation is allowed from Open or Blocked.
- **Inventory close in the checklist.** FR-39.12 runs validation and the preview before the
  inventory close, and BR-39.11 rejects activation without the close. So validation reports a
  missing close as a CST-0731 warning (`closeComplete: false`), and the close and activate
  steps refuse it.
- **ProfitCenter cost basis.** FR-39.17 says ProfitCenter transfer prices come from a
  transfer-price table or a cost-plus % per profit-center pair. Without the table, the item
  carries the pair's `transferPrice`, or `costPlusPct` on the supplying profit center's cost,
  which the service reads from `legalCost` (inside one entity the supplier's Legal-view cost).
  The table entry wins over cost-plus. No basis is an item error (`CST-0762`, new code) rather
  than a fallback to the external price, so a missing transfer price is not hidden.
- **Ad-hoc estimate prices.** The PRD's CostingModel line carries a `priceFormula` for every
  line type. The service uses it when it is set and otherwise looks the line's `ref` up in the
  prices resolved from the costing version, so an overhead line normally sets a rate formula
  and an item line normally takes its version price.
- **Sales order estimate after confirmation.** FR-39.16 recomputes the estimate "before
  confirmation" and BR-39.14 says later configuration changes create a new version and
  supersede the old one. The service applies BR-39.14: a changed configuration supersedes the
  Frozen version too, and the new Pending version must be confirmed again.
