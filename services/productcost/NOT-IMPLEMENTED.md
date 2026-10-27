# NOT-IMPLEMENTED — productcost (PSG-39, wave 39)

Slices 1 and 2 are a stateless computation service. Slice 1: roll-up, mass roll, variance
decomposition and activation preview. Slice 2: the MakeToOrder and ByLineType explosion modes
with phantom and make-to-order BOM lines, route scrap, mixed cost over procurement
alternatives, COGS and price-difference posting previews by cost component, sales order cost
estimates, baseline-versus-candidate comparison and the standard cost reconciliation.
Everything below is either a later slice of PSG-39 or a handoff to another owner. Status is
one of `built`, `partial`, `later slice of PSG-39` or `handoff`.

## Deliverables (ticket section 7.39.3 and the deliverables list)

| # | Deliverable / FR | Status | Reason |
|---|---|---|---|
| D1 | Cost groups and component structure CRUD (FR-39.1), ≤ 50 groups, ≤ 20 components per tenant | later slice of PSG-39 | Needs the persisted `cost_component` table and maintenance endpoints. Slices 1–2 take component codes as strings in the request. |
| D2 | Costing sheet as a persisted tree per site and version (FR-39.2): Basis and Total nodes, absorption account, absorption at production end | later slice of PSG-39 | Surcharge nodes (% of named components, optionally including rolled subassemblies) and Rate nodes (per routing hour or per unit) are evaluated per item. Basis and Total nodes are refused with 400 (`items[i].sheet[j].type`) until the persisted sheet tree exists; there is no absorption posting. |
| D3 | Costing versions with the state machine Open → Blocked → Active → Closed, fallback principle, site scope (FR-39.3, BR-39.2) | later slice of PSG-39 | No version entity or persistence yet. The fallback *flag* (`fallbackToActive`) is supported per request. |
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
| D12 | Standard cost conversion checklist (FR-39.12): ConversionRun, steps, blockers (CST-0731, CST-0732), reverse conversion | later slice of PSG-39 | The revaluation arithmetic (`ConversionAdjustment`) exists and is tested. The checklist, ConversionRun persistence and the inventory-close gate (BR-39.11) are not built. |
| D13 | Ad-hoc cost estimates with costing models (FR-39.13): CostingModel, AdHocEstimate, formula grammar shared with FR-38.17 | later slice of PSG-39 | Needs the formula grammar (wave 38) and persistence. |
| D14 | Mixed costing (FR-39.14): ProcurementAlternative, PUT alternatives, `derive-ratios` from 12 months of receipts | partial | `POST /v1/costing/mixed-costs` rolls each alternative (supplied as a catalog item) and returns the ratio-weighted cost and split, with 100.00% validation (CST-0741) and 0.00% exclusion (BR-39.12). `DeriveRatios` computes ratios from receipt quantities but no route or receipt feed uses it. Persistence of alternatives per item, site and version, `PUT /costing/procurement-alternatives/{itemId}`, the `costing.mixedCosting` and `costing.mixingRatioSource` options, the quarterly `mixing-ratio-derive` job and the mixed-cost composition inquiry are later. |
| D15 | COGS and price-difference posting by component (FR-39.15, `costing.cogsComponentPosting`, `costing.ppvSplit`) | partial | `POST /v1/costing/postings:cogs` and `POST /v1/costing/postings:price-difference` return balanced journal previews in the request currency (components sum to the total, BR-39.13). Posting on wave-28 sales issues, the per-site options (off by default) and the ledger accounts from posting profiles are later. |
| D16 | Sales order cost estimates (FR-39.16): calculation type SalesOrder, margin, freeze at confirmation, `salesorderestimate.completed.v1` | partial | `POST /v1/costing/sales-order-estimates` rolls the caller-supplied order structure at the order quantity and returns type `SalesOrder`, cost, split and margin (AC-39.13). Storing the estimate against the sales line, recompute on configuration change, freeze at confirmation and supersede (BR-39.14, CST-0751), `use-order-estimate-as-standard`, the event, and reading the wave-27 configurator output are later. |
| D17 | Parallel valuation views Legal / Group / ProfitCenter (FR-39.17) | later slice of PSG-39 | Needs wave-40 intercompany receipts (group cost per layer) and the wave-12 layers. |
| D18 | Parallel inventory valuation ledgers, flag `costing.global-inventory-accounting` (FR-39.18) | later slice of PSG-39 | Flag-gated, Could priority. Depends on the wave-20 engine per ledger. |
| D19 | Reports and documents: explain export (PDF/XLSX), activation revaluation report, conversion record, monthly variance report, sales order estimate sheet (section 7.39.15) | later slice of PSG-39 | Need the persisted data and the document service. |
| D20 | Migration from AX/SAP (section 7.39.11): load active standards as Active calculations with diagnostic `Migrated`, comparison report vs legacy | handoff (migration-framework) | Needs the migration pipeline in the integration database. |
| D21 | Costing workspace web (Deploy): roll, activate, compare, diagnostics tile | later slice of PSG-39 (partial) | `apps/web/src/modules/productcost/` has the workspace landing page, the cost roll-up page (single roll with the own/rolled split, mass roll with per-item diagnostics, all four explosion modes), the production variance page, the activation preview page, and from slice 2 the mixed cost, sales order estimate, component postings, estimate comparison and standard reconciliation pages, all on the stateless endpoints. Item structures are entered as JSON because there are no stored items, BOMs or versions yet. The sheet designer, lazy explain tree, activate action with confirmation, persisted variance pivot and diagnostics tile need D3, D4e, D6 and D9. The demo plant (standard cost, monthly variance report) needs D6 and D9. |
| D22 | Demo plant on standard cost with a monthly variance report (Deploy) | later slice of PSG-39 | Needs D6, D8 persistence and D9. |

## Acceptance criteria

| AC | Status | Reason |
|---|---|---|
| AC1 Roll-up fixtures: 50 multi-level products match a reference workbook across cost components | handoff (Finance) | No reference workbook is in the repository and `services/productcost/testdata/` does not exist. The worked example (AC-39.1) is an exact test. The 50-product fixture waits for the workbook. |
| AC2 Activation revalues inventory exactly and posts the revaluation to the GL | later slice of PSG-39 | The preview arithmetic is exact and tested (AC-39.4 shape: 1,000 × 0.50 = 500.00 Dr Inventory / Cr Cost change variance). Posting needs D6. |
| AC3 Variance decomposition sums to total for 200 ended production orders | partial | Identity is exact and property-tested for the decomposition (the four types sum to total per component and per order). The 200 ended fixture orders (`variance-200`) are not in the repository. |
| AC4 Mass roll of 50k items in < 15 min | partial | `TestMassRoll_FiftyThousandItemsEightLevels` rolls 50,000 items over 8 levels with 4 workers in the domain and asserts < 15 min. Over HTTP the platform caps a request body at 4 MiB, so a 50,000-item selection does not fit in one stateless `POST /v1/costing/rolls`; the real mass roll reads the selection from stored BOMs (D5). The real BOM shape, the 2 GiB worker memory limit and the benchmark on the target cluster are not measured. |
| AC5 Every deliverable has code or is listed here | built | This file lists every deliverable not built. |
| AC6 `go vet` and `go test` pass; web vitest and tsc pass; OpenAPI/AsyncAPI match code; chart lints | built | `TestOpenAPI_MatchesRoutes` checks every served route and its duty against `api/openapi.yaml`; `TestMigrationsApplyWithRLS` applies the schema and checks RLS (with `PASSAGE_TEST_DATABASE_URL`). AsyncAPI lists no channels because slices 1–2 publish nothing. |

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
| AC-39.10 Conversion revaluation 75.00 | partial | The arithmetic is tested in `ConversionAdjustment`. The checklist is D12. |
| AC-39.11 Mixed cost 11.00, split totals 11.00 | built (computation) | `RollMixed` and `POST /v1/costing/mixed-costs` are tested with the AC values. Persisted alternatives are D14. |
| AC-39.12 COGS lines 212.00 / 540.00 / 21.20 / 270.00 total 1,043.20 | partial | The COGS preview returns the four lines and the Inventory credit (tested). Posting on the issue is D15. |
| AC-39.13 Sales order margin 540.00 (22.50%) and freeze | partial | Margin 540.00 and 22.50% are tested in the domain and over HTTP. Freeze on confirmation needs the stored estimate (D16). |
| AC-39.14 Group view uses 100.00 vs Legal 120.00 | later slice of PSG-39 | D17. |

## Platform and operations handoffs

| Item | Status | Owner / reason |
|---|---|---|
| Migrations apply on an empty database; RLS verified on Postgres | built | `migrations/migrations_test.go` applies `0001_costing.sql` twice and checks that every table leads with `tenant_id`, enables and forces row-level security, has the tenant policy (`passage.tenant_id`) and, when it carries `legal_entity_id`, the legal-entity scope policy. It runs when `PASSAGE_TEST_DATABASE_URL` is set. No repository writes these tables until the persistence slice. |
| Store package, Postgres repositories, idempotency (`Idempotency-Key`, `idempotency_keys`), outbox writes | later slice of PSG-39 | Stateless in slices 1–2, so no writes and no state change needs an outbox row. |
| Role grants for the costing duties | handoff (policies/ owner, wave 2) | Routes enforce the PRD duties (section 7.39.17): `CostingCalculate` (calculations, rolls, mixed costs, sales order estimates, posting previews), `CostingView` (comparisons, reconciliations), `CostingActivate` (activation preview), `CostingSystem` (variance compute, a service-account duty; the variance page is usable only by a user who holds it until the order-end consumer exists). The role-to-duty grants live in the policies directory, which this wave does not own. CostingMaintain and CostingConfigureLedgers have no routes yet. |
| PRD endpoint paths | later slice of PSG-39 | Slices 1–2 have no stored versions, so they serve stateless `POST` routes: `/v1/costing/rolls`, `/activations:preview`, `/comparisons`, `/mixed-costs`, `/reconciliations` and the two posting previews. With versions (D3) they become the PRD's `POST /costing/versions/{id}/roll`, `POST /costing/versions/{id}/activate` with `dryRun=true`, `GET /costing/compare?a=&b=` and `PUT /costing/procurement-alternatives/{itemId}`, plus `GET /calculations/{id}/explain` and `GET /variances`. `POST /v1/costing/sales-order-estimates` already has the PRD path; its idempotency key (lineId, configVersion) comes with storage. |
| Posting precision per currency | built (except conversion) | The variance compute, the activation preview, the COGS and price-difference previews, the sales order estimate and the reconciliation take an optional `currency` and round to its ISO 4217 minor unit (BR-39.1, `money.MinorUnits`). The unexposed `ConversionAdjustment` still rounds to two decimals; it takes the currency with the conversion checklist (D12). |
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
