# Functional guide: Phase 7, Treasury, Assets and Controlling

Waves 36-40. Audience: treasury analysts, the fixed-asset accountant, cost controllers, the budget owner, product
costing analysts and the group reporting lead. The phase covers cash and bank, the asset register, cost accounting and
budget control, standard costs and variances, and intercompany trade with consolidation. At the end of the phase the
finance function is at full parity with AX and SAP for these areas.

This guide describes intended behaviour from the phase 7 PRD (`docs/prd/phase-7-treasury-controlling.md`). It is
not a build status. Each wave has its own service and web module, named at the top of its section; what is built is
in that service's `README.md` and what is not is in its `NOT-IMPLEMENTED.md`, which is authoritative where it and
this guide differ. Where a later wave completes an item, the text says so.

## Wave 36: Cash and bank management, reconciliation and treasury

Owning area: `services/treasury`. Screens: `apps/web/src/modules/treasury`.

What you do here:

- Maintain the bank account master: signatories, approval limits and the bank connection. Check management covers
  voids and reprints. Letters of credit and guarantees are tracked to their expiry.
- Import statements (camt.053, BAI2 or MT940) and run auto-reconciliation with matching rules. Suggested matches from
  the model are reviewed before they post.
- Read the cash position through the day (intraday camt.052) and the cash flow forecast built from AP, AR, payroll and
  project cash flows. Each forecast bucket equals the sum of its sources.
- Net intercompany positions, run cash pools and in-house cash current accounts, and calculate balance interest on GL
  and intercompany accounts.
- Record bills of exchange and promissory notes (draw, remit, protest, settle) and postdated checks.
- Keep the petty cash desk: business transactions, the daily count and close, and the cash book.
- Analyse bank fees from camt.086 against the agreed fee terms.
- Book FX spot, forward and swap deals and fixed-term deposits. Confirm them, follow the settlement flows, and value them
  on key dates. The FX exposure view shows the open positions.
- Track loan contracts (bank and intercompany) with their schedules and interest accruals.
- Raise treasury payment requests and bank-to-bank transfers. They need four-eyes approval before they are sent.
- Connect to banks through SFTP, EBICS or an open-banking API. Keys stay in Vault. Every bank file that is sent is
  audited.

Rules you can rely on:

- Keys never leave Vault. Every bank file sent has an audit entry.
- A treasury payment request above the four-eyes threshold needs an approver other than its creator and last editor;
  an edit after approval returns it to draft (BR-36.16).
- Auto-match needs a unique top candidate at least 5 points above the next; otherwise the match is only suggested
  (BR-36.2).

## Wave 37: Fixed assets and leases

Owning area: `services/assets`. Screens: `apps/web/src/modules/assets`.

What you do here:

- Create the asset master with its books (tax, IFRS and local). Choose the depreciation profile (straight-line,
  declining balance, units of production, MACRS tables) and the convention.
- Acquire an asset from an AP invoice, a project or inventory. Run the depreciation run for the period.
- Transfer, split, revalue, impair or dispose of an asset. A disposal books the gain or loss, including a partial
  disposal.
- Track assets under construction from projects, and capitalise them when they are ready.
- Count physical assets with the scanner app. Found, missing and moved assets become transfer or scrap proposals.
- Group assets of one class and tax year for a tax book (US general asset accounts, the German Sammelposten, UK
  capital allowance pools). The group depreciates on its total basis; other books keep per-asset depreciation.
- Record leases under IFRS 16 or ASC 842: the schedule, the right-of-use asset and the lease liability.
- Request capital appropriations with variants and value-based approval. An approved request feeds the depreciation
  forecast and the budget.

Check before sign-off: the asset subledger reconciles to the GL asset and accumulated depreciation accounts, to the
cent.

## Wave 38: Cost accounting, budgeting and budget control

Owning area: `services/budgeting` (budget register, planning, budget control) and `services/costacct` (cost
accounting ledger, allocations). Screens: `apps/web/src/modules/budgeting` and `apps/web/src/modules/costacct`.

What you do here:

- Enter the budget register and plan budgets with workflow. A plan can have several scenarios, top-down or bottom-up,
  and versions.
- Download a budget worksheet template, fill it in and upload it back. The round trip is validated. Plans can also be
  generated from GL actuals, the asset forecast and the demand forecast.
- Control the budget on commitments. Requisitions and purchase orders reserve budget. A budget check blocks or warns
  on each order, under the tolerance and override rules. The commitment is released as the invoice clears it.
- Set up cost elements, cost objects, dimension hierarchies, allocation policies (assessment and distribution) and
  statistical measures. Run the allocation cycle; cost in equals cost out.
- Run activity-based costing with internal orders.
- Read the cost control workspace for variance analysis by cost centre and object.
- Reserve funds for planned spend with budget reservation documents (reservations, precommitments, blocks and revenue
  forecasts).
- Manage investment programmes with a position hierarchy, budget distribution, roll-up and carry-forward.

Check before sign-off: the cost accounting ledger reconciles to the GL for primary costs; a PO over budget was blocked
or warned as the policy said; encumbrances clear exactly on invoice.

## Wave 39: Product costing, cost roll-up and variance analysis

Owning area: `services/productcost`. Screens: `apps/web/src/modules/productcost`.

What you do here:

- Set up the costing sheet: cost groups, indirect cost nodes (surcharge or rate) and the cost component split.
- Roll up the cost of a BOM by costing version, site and quantity. Mass rolls run for many items at once.
- Activate the standard cost. Activation revalues the on-hand inventory and posts the revaluation to the GL. Converting
  from actual to standard cost needs a completed inventory close the day before (BR-39.11).
- Analyse variances: purchase price, production (lot size, quantity, price, substitution), and planned against actual
  by cost component. The parts add up to the total variance.
- Compare cost estimates and run what-if simulations. Work through the actual-to-standard conversion checklist, and
  preview the revaluation before you run it.
- Post COGS and price differences by cost component. A parallel valuation view (legal, group or profit centre) can be
  kept alongside.
- Estimate the cost of a sales order line that is configured or made to order.

Check before sign-off: the roll-up of a sample multi-level product matches the reference workbook by component, and
variance decomposition sums to the total for a sample of ended production orders.

## Wave 40: Intercompany and financial consolidation

Owning area: `services/intercompany` and `services/consolidation`. Screens: `apps/web/src/modules/intercompany` and
`apps/web/src/modules/consolidation`.

What you do here:

- Pair the customer and vendor accounts of two legal entities. An intercompany sales order creates the mirror purchase
  order automatically, and a direct delivery chain runs from the supplier to the end customer.
- Post intercompany journals and due-to and due-from entries. The matching and reconciliation workspace shows each
  mismatch; a planted mismatch is always flagged.
- Set up the group structure, the ownership percentage and the consolidation method. Elimination rules remove
  intercompany balances and profit in inventory. Currency translation follows IAS 21, and minority interest is shown
  separately.
- Produce the consolidated statements through financial reporting, and drill from a group line to the entity.
- Charge expenses, timesheets and vendor invoice lines between entities at transfer prices. Centralised payments pay or
  receive on behalf of another entity.
- Record investments: first and later acquisitions, ownership changes, divestitures and the equity method.
- Post top-side group journals and run the consolidation monitor with its validation rules.

Check before sign-off (phase 7 exit): the demo group (three entities, two currencies) produces consolidated
statements that the group controller signs off. Intercompany mirror documents always balance across entities.

## Month-end sequence for this phase

1. Reconcile the bank accounts, the cash positions and the petty cash desks to the GL.
2. Run the depreciation and the lease schedules; reconcile the asset subledger.
3. Run the cost allocation cycle and the variance analysis for the period.
4. Clear the intercompany mismatches; run the eliminations; produce the consolidated draft for review.

Related: [phase 2 guide](phase-2-financial-core.md) for the ledger, period control and close,
[phase 3 guide](phase-3-procure-to-pay.md) for the commitments and the AP invoices that clear them, and the
[tenant administrator guide](../tenant-administrator-guide.md) for the cost statement the platform itself produces.
