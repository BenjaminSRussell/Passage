# NOT-IMPLEMENTED — productcost (PSG-39, wave 39)

Slice 1 is a stateless computation service: roll-up, mass roll, variance decomposition
and activation preview, with the pure helpers for PPV, mixed cost, COGS split, conversion
adjustment and revaluation. Everything below is either a later slice of PSG-39 or a
handoff to another owner. Status is one of `later slice of PSG-39` or `handoff`.

## Deliverables (ticket section 7.39.3 and the deliverables list)

| # | Deliverable / FR | Status | Reason |
|---|---|---|---|
| D1 | Cost groups and component structure CRUD (FR-39.1), ≤ 50 groups, ≤ 20 components per tenant | later slice of PSG-39 | Needs the persisted `cost_component` table and maintenance endpoints. Slice 1 takes component codes as strings in the request. |
| D2 | Costing sheet as a persisted tree per site and version (FR-39.2): Basis and Total nodes, absorption account, absorption at production end | later slice of PSG-39 | Surcharge nodes (% of named components, optionally including rolled subassemblies) and Rate nodes (per routing hour or per unit) are evaluated per item. Basis and Total nodes are refused with 400 (`items[i].sheet[j].type`) until the persisted sheet tree exists; there is no absorption posting. |
| D3 | Costing versions with the state machine Open → Blocked → Active → Closed, fallback principle, site scope (FR-39.3, BR-39.2) | later slice of PSG-39 | No version entity or persistence yet. The fallback *flag* (`fallbackToActive`) is supported per request. |
| D4 | Roll-up algorithm (FR-39.4): multilevel and single-level explosion, worked example | built | Explosion modes MakeToOrder and ByLineType are **not** built (see row D4a). |
| D4a | Explosion modes `MakeToOrder` and `ByLineType` (FR-39.4), BOM line phantom and MTO flags | later slice of PSG-39 | The API rejects them (`options.mode`). The BOM model has no line-type flags. |
| D4b | Route operation scrap that compounds backward (BR-39.6) | later slice of PSG-39 | The operation model has no scrap field. |
| D4c | Costing lot size from item default order settings (BR-39.5, `costing.lotSizeSource`) | handoff (wave 8 item settings) | The lot size is a request input (`lotSize` or `costingLot`). The item-settings lookup needs the wave-8 item data. |
| D4d | Route activity validity: CST-0705 inactive route at site and date | handoff (wave 9 route versions) | Routes come from wave 9 in later slices. Slice 1 has no route data. |
| D4e | Explanation lines per calculation and `GET .../explain` with lazy levels | later slice of PSG-39 | Explanation needs the stored calculation (`cost_calculation`). |
| D5 | Mass roll (FR-39.5): level batches on Temporal, per-level checkpoints, `run_id` persistence, `rollrun.completed` event, roll diagnostics report | later slice of PSG-39 | Slice 1 rolls in-process with worker goroutines per level. Durable runs, checkpoints and the job status URL come with persistence and the Temporal worker. |
| D6 | Activation (FR-39.6): atomic activation per version and site, advisory lock against running rolls (BR-39.7, CST-0710), reversal as a new activation, journal posting, `standardcost.activated.v1` | later slice of PSG-39 | Slice 1 provides the preview only (`POST /v1/costing/activations:preview`). Posting needs wave-16 on-hand and the wave-12 ledger. |
| D6a | On-hand revaluation from the wave-16 projection at the activation timestamp (BR-39.8) | handoff (wave 16 projection) | The preview takes quantities from the caller. |
| D6b | Open production orders keep their estimate; `newStandardForOpenOrders` flag (FR-39.6 note, parameter table) | later slice of PSG-39 | Needs production order data (wave 21). |
| D7 | Purchase price variance posting at receipt and invoice (FR-39.7), by component | later slice of PSG-39 | The PPV split helper exists in the domain (`PPV`) and is tested. Posting and wave-18 receipt/invoice events are not wired. |
| D8 | Production variance decomposition on order end (FR-39.8) | built (computation only) | The order-end event consumer, `VarianceEntry` persistence, posting profiles, and `variance.posted.v1` are later. Reopen reversal (BR-39.10) is later. |
| D9 | Variance analysis workspace: pivots by order, item, site, period, type, component; drill to consumption; monthly variance report (FR-39.9) | later slice of PSG-39 | Needs persisted variance entries. |
| D10 | Cost estimate comparison and what-if simulations (FR-39.10, `costing.whatif`) | later slice of PSG-39 | Needs versions. The compare endpoint `GET /costing/compare` is not built. |
| D11 | Standard cost reconciliation nightly (FR-39.11, job `standard-cost-reconcile`) | later slice of PSG-39 | Needs the wave-20 value report. |
| D12 | Standard cost conversion checklist (FR-39.12): ConversionRun, steps, blockers (CST-0731, CST-0732), reverse conversion | later slice of PSG-39 | The revaluation arithmetic (`ConversionAdjustment`) exists and is tested. The checklist, ConversionRun persistence and the inventory-close gate (BR-39.11) are not built. |
| D13 | Ad-hoc cost estimates with costing models (FR-39.13): CostingModel, AdHocEstimate, formula grammar shared with FR-38.17 | later slice of PSG-39 | Needs the formula grammar (wave 38) and persistence. |
| D14 | Mixed costing (FR-39.14): ProcurementAlternative, PUT alternatives, `derive-ratios` from 12 months of receipts | later slice of PSG-39 | The ratio-weighted split (`MixedCost`, 100.00% validation, CST-0741) exists and is tested. Persistence, the PUT endpoint and derivation from receipts are not built. |
| D15 | COGS and price-difference posting by component (FR-39.15, `costing.cogsComponentPosting`, `costing.ppvSplit`) | later slice of PSG-39 | The COGS split (`IssueCOGS`, residual to DM) exists and is tested. Posting to the wave-28 issue and the ledger is not wired. |
| D16 | Sales order cost estimates (FR-39.16): calculation type SalesOrder, margin, freeze at confirmation, `salesorderestimate.completed.v1` | later slice of PSG-39 | Needs the wave-27 configurator output and sales-line events. |
| D17 | Parallel valuation views Legal / Group / ProfitCenter (FR-39.17) | later slice of PSG-39 | Needs wave-40 intercompany receipts (group cost per layer) and the wave-12 layers. |
| D18 | Parallel inventory valuation ledgers, flag `costing.global-inventory-accounting` (FR-39.18) | later slice of PSG-39 | Flag-gated, Could priority. Depends on the wave-20 engine per ledger. |
| D19 | Reports and documents: explain export (PDF/XLSX), activation revaluation report, conversion record, monthly variance report, sales order estimate sheet (section 7.39.15) | later slice of PSG-39 | Need the persisted data and the document service. |
| D20 | Migration from AX/SAP (section 7.39.11): load active standards as Active calculations with diagnostic `Migrated`, comparison report vs legacy | handoff (migration-framework) | Needs the migration pipeline in the integration database. |
| D21 | Costing workspace web (Deploy): roll, activate, compare, diagnostics tile | later slice of PSG-39 (partial) | `apps/web/src/modules/productcost/` has the workspace landing page, the cost roll-up page (single roll with the own/rolled split, mass roll with per-item diagnostics), the production variance page and the activation preview page, all on the stateless endpoints. The item structure is entered as JSON because there are no stored items, BOMs or versions yet. The sheet designer, lazy explain tree, activate action with confirmation, compare view, persisted variance pivot and diagnostics tile need D3, D4e, D6, D9 and D10. The demo plant (standard cost, monthly variance report) needs D6 and D9. |
| D22 | Demo plant on standard cost with a monthly variance report (Deploy) | later slice of PSG-39 | Needs D6, D8 persistence and D9. |

## Acceptance criteria

| AC | Status | Reason |
|---|---|---|
| AC1 Roll-up fixtures: 50 multi-level products match a reference workbook across cost components | handoff (Finance) | No reference workbook is in the repository and `services/productcost/testdata/` does not exist. The worked example (AC-39.1) is an exact test. The 50-product fixture waits for the workbook. |
| AC2 Activation revalues inventory exactly and posts the revaluation to the GL | later slice of PSG-39 | The preview arithmetic is exact and tested (AC-39.4 shape: 1,000 × 0.50 = 500.00 Dr Inventory / Cr Cost change variance). Posting needs D6. |
| AC3 Variance decomposition sums to total for 200 ended production orders | partial | Identity is exact and property-tested for the decomposition (the four types sum to total per component and per order). The 200 ended fixture orders (`variance-200`) are not in the repository. |
| AC4 Mass roll of 50k items in < 15 min | partial | `TestMassRoll_FiftyThousandItemsEightLevels` rolls 50,000 items over 8 levels with 4 workers in the domain and asserts < 15 min. Over HTTP the platform caps a request body at 4 MiB, so a 50,000-item selection does not fit in one stateless `POST /v1/costing/rolls`; the real mass roll reads the selection from stored BOMs (D5). The real BOM shape, the 2 GiB worker memory limit and the benchmark on the target cluster are not measured. |
| AC5 Every deliverable has code or is listed here | built | This file lists every deliverable not built. |
| AC6 `go vet` and `go test` pass; web vitest and tsc pass; OpenAPI/AsyncAPI match code; chart lints | built | `TestOpenAPI_MatchesRoutes` checks every served route and its duty against `api/openapi.yaml`; `TestMigrationsApplyWithRLS` applies the schema and checks RLS (with `PASSAGE_TEST_DATABASE_URL`). AsyncAPI lists no channels because slice 1 publishes nothing. |

## Ticket acceptance criteria (AC-39.x) not covered by slice 1

| AC | Status | Reason |
|---|---|---|
| AC-39.1 worked SA-200 / FG-100 = 104.32, split 21.20 / 54.00 / 2.12 / 27.00 | built | Exact test in `internal/domain`. |
| AC-39.2 50 fixture products match the workbook to the cent | handoff (Finance) | See AC1. |
| AC-39.3 50k items across 8 levels, diagnostics for every error | partial | The run and the diagnostics are built and tested. Measurement on target hardware is pending. |
| AC-39.4 Activation journal Dr Inventory 500.00 / Cr Cost change variance 500.00 and the value report ties | partial | The journal and its preview are built and tested. The value report tie-out needs D6 and D11. |
| AC-39.5 Order variance: QV 40.00, SV 7.50, LSV 0.00, total 47.50 plus labor PV | built | Tested. The test order also has a labor price variance of 4.00 (total 51.50). The ticket's "plus any labor PV" is satisfied by the decomposition. |
| AC-39.6 200 ended fixture orders, Σ types = total | partial | Identity is property-tested. The fixture orders are not in the repository (AC3). |
| AC-39.7 PPV 2.00 for 10 at 5.20 vs 5.00 posts to DM | partial | The PPV helper is tested. Posting is D7. |
| AC-39.8 Cyclic BOM errors with the cycle path, the run continues | built | Tested in the planner tests and the mass-roll API test. |
| AC-39.9 Σ on-hand × standard = GL inventory per site | later slice of PSG-39 | Reconciliation job D11. |
| AC-39.10 Conversion revaluation 75.00 | partial | The arithmetic is tested in `ConversionAdjustment`. The checklist is D12. |
| AC-39.11 Mixed cost 11.00, split totals 11.00 | partial | `MixedCost` is tested. The endpoint and persistence are D14. |
| AC-39.12 COGS lines 212.00 / 540.00 / 21.20 / 270.00 total 1,043.20 | partial | `IssueCOGS` is tested for the split. Posting is D15. |
| AC-39.13 Sales order margin 540.00 (22.50%) and freeze | later slice of PSG-39 | D16. |
| AC-39.14 Group view uses 100.00 vs Legal 120.00 | later slice of PSG-39 | D17. |

## Platform and operations handoffs

| Item | Status | Owner / reason |
|---|---|---|
| Migrations apply on an empty database; RLS verified on Postgres | built | `migrations/migrations_test.go` applies `0001_costing.sql` twice and checks that every table leads with `tenant_id`, enables and forces row-level security, has the tenant policy (`passage.tenant_id`) and, when it carries `legal_entity_id`, the legal-entity scope policy. It runs when `PASSAGE_TEST_DATABASE_URL` is set. No repository writes these tables until the persistence slice. |
| Store package, Postgres repositories, idempotency (`Idempotency-Key`, `idempotency_keys`), outbox writes | later slice of PSG-39 | Stateless in slice 1, so no writes and no state change needs an outbox row. |
| Role grants for the costing duties | handoff (policies/ owner, wave 2) | Routes enforce the PRD duties (section 7.39.17): `CostingCalculate` (calculations, rolls), `CostingActivate` (activation preview), `CostingSystem` (variance compute, a service-account duty; the variance page is usable only by a user who holds it until the order-end consumer exists). The role-to-duty grants live in the policies directory, which this wave does not own. CostingView (nav only for now), CostingMaintain and CostingConfigureLedgers have no routes yet. |
| PRD endpoint paths | later slice of PSG-39 | Slice 1 has no stored versions, so it serves stateless `POST /v1/costing/rolls` and `POST /v1/costing/activations:preview`. With versions (D3) they become the PRD's `POST /costing/versions/{id}/roll` and `POST /costing/versions/{id}/activate` with `dryRun=true`, plus `GET /calculations/{id}/explain`, `GET /variances` and `GET /compare`. |
| Posting precision per currency for PPV, COGS and conversion | later slice of PSG-39 | The variance compute and the activation preview take an optional `currency` and round to its ISO 4217 minor unit (BR-39.1, `money.MinorUnits`). The unexposed helpers `PPV`, `IssueCOGS` and `ConversionAdjustment` still round to two decimals; they take the currency when their posting is wired (D7, D12, D15). |
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
