package domain

import (
	"sort"

	"github.com/shopspring/decimal"
)

// Verification statuses (FR-15.1 nightly verify, slice 10).
const (
	VerifyMatched      = "matched"
	VerifyDiffers      = "differs"
	VerifyInconclusive = "inconclusive"

	// MaxVerifyDiffs bounds the differing accounts stored with one check;
	// DiffCount is always the full number.
	MaxVerifyDiffs = 500
)

// AccountDiff is one account whose net differs between the read model and the
// ledger's trial balance. An account one side lacks counts as zero there.
// Difference = ReadModel - Ledger.
type AccountDiff struct {
	Account    string          `json:"account"`
	ReadModel  decimal.Decimal `json:"readModel"`
	Ledger     decimal.Decimal `json:"ledger"`
	Difference decimal.Decimal `json:"difference"`
}

// VerifyResult is the outcome of comparing the read model with the ledger.
type VerifyResult struct {
	Status           string
	HWMBefore        int64
	HWMAfter         int64
	EntriesPending   int
	AccountsCompared int
	DiffCount        int
	Diffs            []AccountDiff
}

// Verify compares the net per account of the read model with the ledger's
// trial balance (debit minus credit, accounting currency, all periods).
// hwmBefore and hwmAfter are the read model's high-water marks taken around
// the ledger call: if they differ the read model moved while the ledger was
// read, so any difference may be that movement and the result is inconclusive.
// entriesPending is the number of the ledger's entries the read model holds
// back because they arrived ahead of the mark (seen before or after the ledger
// call): the ledger is then known to be ahead, so the result is inconclusive too.
// A difference with a steady mark and nothing waiting is reported as differs;
// it can still be an entry the consumer has not received yet, because the
// ledger's trial balance does not say which posting sequence it reflects
// (handoff to wave 11 in NOT-IMPLEMENTED.md).
// Diffs are ordered by account and capped at MaxVerifyDiffs.
func Verify(readModel, ledger map[string]decimal.Decimal, hwmBefore, hwmAfter int64, entriesPending int) VerifyResult {
	accounts := make(map[string]struct{}, len(readModel)+len(ledger))
	for a := range readModel {
		accounts[a] = struct{}{}
	}
	for a := range ledger {
		accounts[a] = struct{}{}
	}
	names := make([]string, 0, len(accounts))
	for a := range accounts {
		names = append(names, a)
	}
	sort.Strings(names)
	res := VerifyResult{HWMBefore: hwmBefore, HWMAfter: hwmAfter, EntriesPending: entriesPending, AccountsCompared: len(names), Diffs: []AccountDiff{}}
	for _, a := range names {
		rm, lg := readModel[a], ledger[a]
		if rm.Equal(lg) {
			continue
		}
		res.DiffCount++
		if len(res.Diffs) < MaxVerifyDiffs {
			res.Diffs = append(res.Diffs, AccountDiff{Account: a, ReadModel: rm, Ledger: lg, Difference: rm.Sub(lg)})
		}
	}
	switch {
	case hwmBefore != hwmAfter || entriesPending > 0:
		res.Status = VerifyInconclusive
	case res.DiffCount > 0:
		res.Status = VerifyDiffers
	default:
		res.Status = VerifyMatched
	}
	return res
}
