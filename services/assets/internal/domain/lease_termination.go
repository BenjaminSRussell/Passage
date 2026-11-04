package domain

import (
	"strconv"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// CodeLeaseTermination is raised when a lease cannot be terminated as asked (AST-0036).
const CodeLeaseTermination = "AST-0036"

// TerminationInput are the balances of a lease at the end of the last period it stays in force
// (AfterPeriod). Liability is the closing liability of that period; ROUAccumulated the ROU
// depreciation to date; ROURemaining the ROU carrying amount that would have been depreciated
// later; Payment what the lessee pays to end the lease early (0 or more).
type TerminationInput struct {
	Currency                  string
	Liability, ROUAccumulated decimal.Decimal
	ROURemaining, Payment     decimal.Decimal
}

// Termination is the derecognition of an early-terminated lease (IFRS 16.46 and the IAS 16 style
// derecognition of the ROU asset): the liability and the ROU asset leave the books and the
// difference, less the termination payment, is a gain (positive) or loss (negative).
type Termination struct {
	Liability, ROUGross, ROUAccumulated, ROUCarrying decimal.Decimal
	Payment, GainLoss                                decimal.Decimal
}

// ComputeTermination derecognizes the liability and ROU asset. GainLoss = Liability - ROU carrying
// - Payment, so a lease whose liability exceeds what the lessee gives up (the ROU asset and the
// penalty) gives a gain.
func ComputeTermination(in TerminationInput) (Termination, error) {
	if len(in.Currency) != 3 {
		return Termination{}, fail(CodeLeaseTermination, "currency must be an ISO 4217 code")
	}
	if in.Payment.IsNegative() {
		return Termination{}, fail(CodeLeaseTermination, "the termination payment cannot be negative")
	}
	if in.Liability.IsNegative() || in.ROURemaining.IsNegative() || in.ROUAccumulated.IsNegative() {
		return Termination{}, fail(CodeLeaseTermination, "the lease balances at termination cannot be negative")
	}
	mu := money.MinorUnits(in.Currency)
	if !in.Payment.Equal(in.Payment.Round(mu)) {
		return Termination{}, fail(CodeLeaseTermination, "the termination payment has more decimals than "+in.Currency+" allows")
	}
	t := Termination{Liability: in.Liability.Round(mu), ROUAccumulated: in.ROUAccumulated.Round(mu), ROUCarrying: in.ROURemaining.Round(mu), Payment: in.Payment.Round(mu)}
	t.ROUGross = t.ROUAccumulated.Add(t.ROUCarrying)
	t.GainLoss = t.Liability.Sub(t.ROUCarrying).Sub(t.Payment)
	return t, nil
}

// LeaseTerminationNet is the net debit (credit when negative) per account of the termination
// journal: the liability and the accumulated depreciation are cleared against the gross ROU asset
// and the termination payment, the difference going to the termination gain / loss account of
// the posting profile (a gain is a credit, a loss a debit).
func LeaseTerminationNet(a LeaseAccounts, t Termination) map[string]decimal.Decimal {
	net := map[string]decimal.Decimal{}
	addNet(net, a.Liability, t.Liability)
	addNet(net, a.ROUAccumulated, t.ROUAccumulated)
	addNet(net, a.ROUAsset, t.ROUGross.Neg())
	addNet(net, a.PaymentClearing, t.Payment.Neg())
	addNet(net, a.TerminationGainLoss, t.GainLoss.Neg())
	return net
}

// ValidTerminationPeriod checks the last period in force: at least 1 and before the last period of
// the schedule (a lease that ran its whole term expires, it is not terminated).
func ValidTerminationPeriod(afterPeriod, schedulePeriods int) error {
	if afterPeriod < 1 || afterPeriod >= schedulePeriods {
		return fail(CodeLeaseTermination, "the last period in force must be at least 1 and before the last period of the schedule")
	}
	return nil
}

// LeaseTerminationDate is the date a lease ends when it stays in force through afterPeriod: the
// last day of that payment period.
func LeaseTerminationDate(commencement time.Time, paymentsPerYear, afterPeriod int) time.Time {
	return LeasePeriodEnd(commencement, paymentsPerYear, afterPeriod)
}

// TerminationNotAhead checks that the termination takes effect at the latest at the end of the
// payment period running on asOf: period afterPeriod must have started by then. Ending a lease at
// a later period agreed today is a change of the lease term, which IFRS 16.44-46 accounts for as a
// modification remeasured today (POST /v1/leases/{id}/modify with the shorter term), not as a
// derecognition at that later date with the original schedule running until then.
func TerminationNotAhead(commencement time.Time, paymentsPerYear, afterPeriod int, asOf time.Time) error {
	start := LeasePeriodEnd(commencement, paymentsPerYear, afterPeriod-1).AddDate(0, 0, 1)
	day := time.Date(asOf.Year(), asOf.Month(), asOf.Day(), 0, 0, 0, 0, time.UTC)
	if start.After(day) {
		return fail(CodeLeaseTermination, "period "+strconv.Itoa(afterPeriod)+" starts on "+start.Format("2006-01-02")+
			", after today; a termination takes effect at the latest at the end of the period running now. To end the lease at a later date, modify it with the shorter term")
	}
	return nil
}

// CanTerminate checks the lease status allows an early termination.
func CanTerminate(status string) error {
	if status != LeaseActive && status != LeaseModified {
		return fail(CodeLeaseTermination, "only an Active or Modified lease can be terminated; this lease is "+status)
	}
	return nil
}
