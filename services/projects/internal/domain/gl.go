package domain

import (
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

const (
	CodeGLFailed          = "PROJ-GL-001"
	CodeNoEstimate        = "PROJ-GL-002"
	CodePeriodOrder       = "PROJ-GL-003"
	CodeLedgerPinned      = "PROJ-GL-005"
	CodeLedgerForeign     = "PROJ-GL-006"
	CodeEstimateCurrency  = "PROJ-GL-007"
	CodeBalanceCurrency   = "PROJ-GL-008"
	CodeProfileIncomplete = "PROJ-POST-002"
	GLKindTxn             = "TXN"
	GLKindWIP             = "WIP"
)

// GLLine is one line of a stored ledger entry; Amount is a decimal string in the currency's minor unit. Control marks a
// line on a control account (WIP, accrued revenue, invoiced revenue, loss provision) that only project postings feed:
// the reconciliation compares those accounts with the ledger; the others are shared with other modules.
type GLLine struct {
	Account string `json:"account"`
	Credit  bool   `json:"credit"`
	Amount  string `json:"amount"`
	Control bool   `json:"control,omitempty"`
}

// Position is a project's cumulative estimate position at a period end, as booked.
type Position struct {
	Revenue, Billed, Provision, Cost decimal.Decimal
}

func (p PostingProfile) controls() map[string]bool {
	return map[string]bool{p.WIPAccount: true, p.AccruedRevenueAccount: true, p.InvoicedRevenueAccount: true, p.LossProvisionAccount: true}
}

func (p PostingProfile) require(names, values []string) error {
	var missing []string
	for i, v := range values {
		if v == "" {
			missing = append(missing, names[i])
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return rule(CodeProfileIncomplete, "the posting profile for project group %s, category %s, line property %s has no %s",
		p.Key.ProjectGroup, p.Key.Category, p.Key.LineProperty, strings.Join(missing, ", "))
}

// move books amount as a debit to debit and a credit to credit; a negative amount reverses the sides.
func (p PostingProfile) move(debit, credit string, amount decimal.Decimal, currency string) []GLLine {
	mu := money.MinorUnits(currency)
	amount = amount.RoundBank(mu)
	if amount.IsZero() {
		return nil
	}
	if amount.IsNegative() {
		debit, credit = credit, debit
	}
	a := amount.Abs().StringFixed(mu)
	c := p.controls()
	return []GLLine{{Account: debit, Amount: a, Control: c[debit]}, {Account: credit, Credit: true, Amount: a, Control: c[credit]}}
}

// TransactionLines books the cost of a project transaction: Dr work in progress, Cr the profile's offset account of the
// category (payroll allocation for hours, expense clearing for expenses, inventory for items; AC-41.5).
func TransactionLines(p PostingProfile, cost decimal.Decimal, currency string) ([]GLLine, error) {
	if err := p.require([]string{"offsetAccount", "wipAccount"}, []string{p.OffsetAccount, p.WIPAccount}); err != nil {
		return nil, err
	}
	return p.move(p.WIPAccount, p.OffsetAccount, cost, currency), nil
}

// RequiredProvision is the loss provision a fixed-price project needs: the expected loss on the whole contract (EAC above
// the contract value) less the loss the period margin already shows (cost of sales above revenue), so the loss is
// booked once, whichever way it reaches the books (BR-41.2).
func RequiredProvision(eac, contract, cost, revenue decimal.Decimal) decimal.Decimal {
	expected := decimal.Max(eac.Sub(contract), decimal.Zero)
	shown := decimal.Max(cost.Sub(revenue), decimal.Zero)
	return decimal.Max(expected.Sub(shown), decimal.Zero)
}

// EstimateLines books the movement of a project's estimate position since the prior booked period:
//   - revenue earned: Dr accrued revenue / Cr invoiced revenue;
//   - billed: Dr on-account / Cr accrued revenue, so the accrued revenue account carries earned minus billed (accrued when a
//     debit, deferred when a credit) and the on-account account holds the billings until invoicing settles them;
//   - cost incurred: Dr cost of sales / Cr WIP, moving the cumulative cost to date out of WIP as the revenue is recognized;
//   - loss provision: Dr loss expense / Cr loss provision (a liability), released when the estimate improves.
func EstimateLines(p PostingProfile, prior, now Position, currency string) ([]GLLine, error) {
	err := p.require(
		[]string{"wipAccount", "accruedRevenueAccount", "invoicedRevenueAccount", "onAccountAccount", "costOfSalesAccount", "lossProvisionAccount", "lossExpenseAccount"},
		[]string{p.WIPAccount, p.AccruedRevenueAccount, p.InvoicedRevenueAccount, p.OnAccountAccount, p.CostOfSalesAccount, p.LossProvisionAccount, p.LossExpenseAccount})
	if err != nil {
		return nil, err
	}
	lines := p.move(p.AccruedRevenueAccount, p.InvoicedRevenueAccount, now.Revenue.Sub(prior.Revenue), currency)
	lines = append(lines, p.move(p.OnAccountAccount, p.AccruedRevenueAccount, now.Billed.Sub(prior.Billed), currency)...)
	lines = append(lines, p.move(p.CostOfSalesAccount, p.WIPAccount, now.Cost.Sub(prior.Cost), currency)...)
	return append(lines, p.move(p.LossExpenseAccount, p.LossProvisionAccount, now.Provision.Sub(prior.Provision), currency)...), nil
}
