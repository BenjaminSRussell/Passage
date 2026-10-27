# NOT-IMPLEMENTED (budgeting, PSG-38 slice 1)

Every PSG-38 deliverable and acceptance criterion that slice 1 does not build, with its status and reason.

| Item | Ref | Status | Reason |
|---|---|---|---|
| Register entry workflow: Draft, Submitted, Approved, Rejected; `submit` endpoint; BR-38.2 state machine | FR-38.1, BR-38.2 | later slice of PSG-38 | Slice 1 posts entries directly; workflow runs on Temporal in a later slice. |
| Budget model hierarchies (sub-models roll up); 2M lines per model | FR-38.1 | later slice of PSG-38 | Needs the model entity; slice 1 keeps one flat combination key. |
| Budget planning: processes, stages, up to 10 scenarios, top-down and bottom-up, distribution keys, plan grid, scenario comparison | FR-38.2, AC-38.7 (AC4) | later slice of PSG-38 | Large entity set; depends on the workflow above. |
| Budget control rule activation and disable history; budget cycle time spans (year, quarter, period, custom) | FR-38.3, BR-38.5, CO-0603 | later slice of PSG-38 | Slice 1 takes the span key from the request; cycles need the fiscal calendar (wave 14). |
| User-group overrides (`BudgetControlUserGroup` mapped to Keycloak groups) | FR-38.3, FR-38.4 step 7 | partial | Slice 1 uses the `budget.override` duty only. |
| Documents under control: Expense, Project, Reservation | FR-38.3 | later slice of PSG-38 | Slice 1 accepts Requisition, PurchaseOrder, Invoice and Journal. |
| `budget.unposted-in-available` flag; `fail-open` timeout policy; CO-0608 check timeout | FR-38.4, 7.38.10 | later slice of PSG-38 | Flags need the flag service; timeout policy needs load tests. |
| Check latency SLO (p95 < 150 ms at 50 checks/s) and the concurrent-check E2E run | 7.38.9, 7.38.19 | later slice of PSG-38 | Needs a load harness. The row lock is in place. |
| Pre-encumbrance from approved requisitions (requisition checks reserve as encumbrances, not pre-encumbrances); PO cancellation release; closed-PO residual release | FR-38.5 | later slice of PSG-38 | Slice 1 reserves at check time, adjusts on a re-check of the same line (net change, BR-38.3) and relieves on invoice. |
| A revised line that moves to another control combination or rule version | FR-38.5 | later slice of PSG-38 | Refused with BUD-0003 so nothing is double reserved; moving needs a release on the old combination, which comes with PO cancellation. |
| Year-end carry-forward of open encumbrances (CarryForward entry, atomic release) | FR-38.5, BR-38.10 | later slice of PSG-38 | Needs the close checklist (wave 14). |
| Price-difference excess is reported (`excess`) but not checked automatically | FR-38.5, BR-38.4 | partial | The caller runs the new check. Automatic chaining moves into the procurement flow. |
| 5 s cache on the available-funds query | 7.38.5, 7.38.19 | later slice of PSG-38 | `GET /v1/budget/available` is built and reads the balance rows directly; the cache comes with the load harness. |
| Balance rebuild from events (`budget-balance-rebuild`) and journal-event projection | 7.38.16, FR-38.4 | handoff | Consumes journal events from the eventing backbone (wave 0 / eventing-backbone). Balances are written by postings and checks only. |
| Nightly reconciliation: budget balances equal posted register entries; Σ encumbrances equal open PO lines | 7.38.9 | later slice of PSG-38 | Reconcile job depends on the balance rebuild. |
| Reservation expiry and earmarked funds (FundsReservation, Precommitment, FundsBlocking, RevenueForecast) | FR-38.20 | later slice of PSG-38 | Separate entity and document numbering. |
| Budget plan worksheet templates and xlsx round-trip | FR-38.13 | later slice of PSG-38 | Needs the plan entity first. |
| Plan generation from GL actuals, plans, FA, demand and position forecasts | FR-38.14 | later slice of PSG-38 | Needs the plan entity and sources (waves 22, 37, 39, 41). |
| Position forecasting | FR-38.15 | later slice of PSG-38 | Wave 42 provides the HR source. |
| Investment programs, budget distribution, roll-up and carry-forward | FR-38.21 | later slice of PSG-38 | Depends on the capex requests of wave 37. |
| Management reporting cubes via CDC to the lakehouse | FR-38.12 | handoff | Star schema is owned by wave 46 and the analytics service. |
| Retention: check results 7 years, monthly partitions | 7.38.19 | later slice of PSG-38 | Results are recorded in `budget_check_result`; partitioning and the purge job are a migration on a larger table. |
| ETag on reads; If-Match on custom methods (`:relieve` takes no If-Match yet) | Platform kernel | later slice of PSG-38 | Slice 1 has no versioned read. Relief uses the row version under lock. |
| Demo entity budget loaded | Deployment | handoff | Seed data belongs in `deploy/` (deployment-cicd owns it). |
| Separation of duties (BudgetOverride vs ProcurementPurchaseOrderApprove for own POs) | 7.38.17 | handoff | Rego policy belongs in `policies/` (edge-identity owns it). |
| Budget check fixtures (`budget-check-200`) under `testdata/` | 7.38.9 | later slice of PSG-38 | Domain tests cover the step order and relief; the 200-case fixture set needs the full rule set. |
| Budget check ("budget check fixtures") acceptance AC-38.1 to AC-38.4 | 7.38.8 | partial | Domain and store tests cover the threshold outcomes, the last-dollar race and exact relief. The full fixture set is not built. |
| Web module | apps/web | built | `apps/web/src/modules/budgeting/`: rule list and create, rule detail (available funds, register entries, encumbrances, checks), register entry form, budget check form, check detail, encumbrance detail with relief. The planning grid and the check inline in the PO form are later slices. |
