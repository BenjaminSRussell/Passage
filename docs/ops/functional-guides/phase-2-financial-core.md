# Functional guide: Phase 2, Financial Core

Waves 11-15. Audience: finance controllers, GL administrators and implementers. The universal journal is the single
source of truth: every subledger (inventory, procurement, sales, treasury, payroll) posts through the same posting
API, so the rules in this guide apply to every module.

This guide describes intended behaviour from the phase 2 PRD (`docs/prd/phase-2-financial-core.md`). It is not a
build status. Each wave has its own service and web module, named at the top of its section; what is built is in
that service's `README.md` and what is not is in its `NOT-IMPLEMENTED.md`, which is authoritative where it and this
guide differ.

## Wave 11: Universal journal and chart of accounts

Owning area: `services/ledger`. Screens: `apps/web/src/modules/ledger`.

What you do here:

- Define accounts and account structures. An account structure validates which dimension values may appear with an
  account (for example, a revenue account requires a product line dimension).
- Set up up to 20 financial dimensions per ledger and default dimensions on master data (customers, vendors, cost
  centers). Defaults fill in when a document is created; validation runs on every line.
- Create manual, recurring and template journals. Manual journals can require approval before they post.
- Attach vouchers with a document type and retention class.
- Set open-item managed accounts for clearing: manual clearing, post-with-clearing, reset, and automatic clearing
  rules with tolerance groups.
- Define user validation rules and dimension derivation strategies (for example, deriving the functional area for
  cost of sales).
- Split documents so that balance sheet and tax lines inherit the dimensions of the posting line and clear to zero.

Rules you can rely on:

- Every journal balances in the transaction currency. Unbalanced entries are rejected.
- Posted entries are immutable. A correction is a reversal, which creates a line-for-line negating entry linked to
  the original, or a credit note in a subledger.
- A duplicate posting with the same idempotency key does nothing and returns the original result.
- Posting into a closed period is rejected (see wave 14).

Common mistakes: forgetting a required dimension on a manual journal (the rejection reason names the dimension);
reversing with no reason code (reversal reasons are mandatory).

## Wave 12: Multi-currency, parallel ledgers and revaluation

Owning area: `services/fx`. Screens: `apps/web/src/modules/fx`.

What you do here:

- Maintain exchange rate types and rates. Rates come from provider adapters (for example ECB) with triangulation
  through a base currency; historical rates are kept with an audit trail.
- Read every journal line in three amounts: transaction, accounting and reporting currency. Rounding follows the
  ISO 4217 minor unit of the currency.
- Post one document to several ledgers (ledger groups) when the accounting principle differs, for example IFRS and
  local GAAP. Each ledger has its own trial balance and must sum to zero.
- Run foreign currency revaluation for open GL, AP and AR items. The revaluation reverses in the next period.
- Restate to reporting currency using the translation method for the period.
- Maintain market data series (interest rates, indices) with date lookup.

Behaviour to know: the rate import retries three times with back-off. If the provider is still unavailable, no
rate changes, the last known rates stay valid, `fx.provider.degraded` is emitted and a P2 alert fires (AC-12.6).
Postings continue; a rate older than `max_rate_age_days` (default 5 business days) raises a posting warning, not a
rejection.

## Wave 13: Tax engine

Owning area: `services/tax`. Screens: `apps/web/src/modules/tax`.

What you do here:

- Configure tax groups (customer and item) that map to tax codes through a jurisdiction hierarchy with effective
  rates.
- Record exemption certificates against a customer and check them at invoice time.
- Choose the calculation mode per code: inclusive or exclusive, compound, reverse charge, use tax, and partial
  recoverability for input tax.
- Set withholding tax for vendors and customers, and run remittances with bank references.
- Run the tax settlement job, the VAT return, the EU sales list, Intrastat and SAF-T extracts.
- Set recovery rules for expenses: the recoverable share per category and country.

Behaviour to know: if an external tax provider times out, the internal rules engine calculates the tax and the
document is flagged so that the tax team can review it. The settlement run reconciles to the tax accounts in the
general ledger to the cent; a difference blocks the return.

## Wave 14: Period close, year-end and allocations

Owning area: `services/close`. Screens: `apps/web/src/modules/close`.

What you do here:

- Set period status per ledger, module and user group: open, on hold or closed. The posting API enforces the
  status on every subledger path.
- Build the close checklist from a template, with task dependencies, owners and due dates. Blocking dependencies
  prevent a task from starting until its predecessor is complete.
- Run allocations: fixed, basis-driven (statistical measure) and iterative (repeated until the results converge).
  Each allocation reverses cleanly.
- Set accrual schemes with automatic reversal in the next period, and schedule recurring journals.
- Run year-end close: the P&L moves to retained earnings, balances carry forward per dimension, and the opening
  transactions post for the new year.
- Run the balance-sheet regrouping for remaining term and for debit vendors and credit customers, auto-reversed.

Behaviour to know: a closed period cannot be reopened silently. `closed → open` runs only through the reopen
workflow (FR-14.2, FR-14.3): two distinct approvers, and an audit event that the audit trail shows (see wave 15).
There is no API bypass.

## Wave 15: Financial reporting, statements and audit trail

Owning area: `services/finreport`. Screens: `apps/web/src/modules/finreport`.

What you do here:

- Design report definitions as rows, columns and trees over account structures and dimensions. Export and import
  definitions as JSON.
- Produce the standard statements: trial balance, balance sheet, income statement, cash flow (indirect), and
  comparative and consolidated-by-entity views.
- Drill down from a statement amount to the balance, to the journal lines, and to the source document. The trace id
  of the posting is shown so an auditor can follow it into the event log.
- Schedule distribution as PDF, XLSX or e-mail. Apply row-level security on dimensions so a manager sees only their
  cost centers.
- Read the audit trail: who posted what, approvals, reversals and period-control overrides.
- Use the functional expense view (P&L by function of expense) over the functional area dimension.

Check before sign-off: the statements tie to the trial balance to the cent; the cash flow reconciles to the change in
cash; drilling down from a balance returns exactly the lines that sum to it.

## Month-end sequence

1. Subledgers post and close their own periods (inventory, AP, AR, payroll).
2. Run the revaluation, allocations and accruals.
3. Run the tax settlement and reconcile the tax accounts.
4. Complete the close checklist for each legal entity.
5. Close the period. Produce the statement pack and the audit trail for the controller.

Related: [phase 1 guide](phase-1-master-data.md) for the dimensions and parties the journal uses, and the
[tenant administrator guide](../tenant-administrator-guide.md) for assigning the roles that post, close and
approve a reopen, and the segregation-of-duties checks on them.
