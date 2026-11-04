package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// CodeLeaseRemeasurementMismatch is raised when a schedule line's remeasurement
// does not agree with the modifications recorded for its period, so its journal
// cannot be built (AST-0610).
const CodeLeaseRemeasurementMismatch = "AST-0610"

// LeaseAccounts are the ledger accounts of the IFRS 16 journals of one legal
// entity: recognition (Dr ROU asset, Cr lease liability) and every period
// (interest, payment, ROU depreciation and the remeasurement of a modification).
type LeaseAccounts struct {
	ROUAsset            string
	ROUAccumulated      string
	Liability           string
	InterestExpense     string
	DepreciationExpense string
	PaymentClearing     string
	RemeasurementGain   string
	// TerminationGainLoss takes the gain (credit) or loss (debit) of an early termination; ExemptLeaseExpense the
	// payments of a lease under a short-term or low-value election.
	TerminationGainLoss string
	ExemptLeaseExpense  string
}

// LeasePeriodAmounts are the amounts of one schedule line to book. Remeasurement
// is the liability adjustment of the line, with the ROU adjustment and the
// profit-or-loss excess of the modifications that produced it (adjustment =
// ROU adjustment - excess, domain.Remeasure).
type LeasePeriodAmounts struct {
	Interest, Payment, ROUDepreciation     decimal.Decimal
	Remeasurement, ROUAdjustment, PLExcess decimal.Decimal
}

// LeaseRecognitionNet is the net debit (credit when negative) per account of the
// commencement journal: the ROU asset against the lease liability.
func LeaseRecognitionNet(a LeaseAccounts, liability, rou decimal.Decimal) map[string]decimal.Decimal {
	net := map[string]decimal.Decimal{}
	addNet(net, a.ROUAsset, rou)
	addNet(net, a.Liability, liability.Neg())
	return net
}

// LeasePeriodNet is the net debit (credit when negative) per account of one
// period journal. The liability moves by remeasurement + interest - payment; the
// ROU asset by its adjustment; a modification that cuts the liability below the
// ROU carrying amount credits the excess to profit or loss. The journal balances
// when remeasurement = ROUAdjustment - PLExcess, which is checked.
func LeasePeriodNet(a LeaseAccounts, p LeasePeriodAmounts) (map[string]decimal.Decimal, error) {
	if !p.Remeasurement.Equal(p.ROUAdjustment.Sub(p.PLExcess)) {
		return nil, fail(CodeLeaseRemeasurementMismatch, "the line's remeasurement does not agree with the modifications of its period")
	}
	net := map[string]decimal.Decimal{}
	addNet(net, a.InterestExpense, p.Interest)
	addNet(net, a.Liability, p.Interest.Neg())
	addNet(net, a.Liability, p.Payment)
	addNet(net, a.PaymentClearing, p.Payment.Neg())
	addNet(net, a.DepreciationExpense, p.ROUDepreciation)
	addNet(net, a.ROUAccumulated, p.ROUDepreciation.Neg())
	addNet(net, a.ROUAsset, p.ROUAdjustment)
	addNet(net, a.RemeasurementGain, p.PLExcess.Neg())
	addNet(net, a.Liability, p.Remeasurement.Neg())
	return net, nil
}

func addNet(net map[string]decimal.Decimal, account string, v decimal.Decimal) {
	net[account] = net[account].Add(v)
}

// LeasePeriodEnd is the last day of payment period period (1-based) of a lease
// that commenced on commencement: the commencement date plus period x (12 /
// paymentsPerYear) months, less one day. A month that is too short for the day
// of commencement ends on its last day.
func LeasePeriodEnd(commencement time.Time, paymentsPerYear, period int) time.Time {
	return addMonths(commencement, period*12/paymentsPerYear).AddDate(0, 0, -1)
}

func addMonths(t time.Time, months int) time.Time {
	y, m, d := t.Date()
	first := time.Date(y, m+time.Month(months), 1, 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, 1, -1).Day()
	if d > last {
		d = last
	}
	return time.Date(first.Year(), first.Month(), d, 0, 0, 0, 0, time.UTC)
}
