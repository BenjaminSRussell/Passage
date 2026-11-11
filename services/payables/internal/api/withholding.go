package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/payables/internal/tax"
)

type WithholdingCalculator interface {
	Calculate(ctx context.Context, legalEntityID string, in tax.Request) (tax.Result, error)
}

// WithholdingRecorder tells the tax service which withholding a payment run made (#13); *tax.Client implements it.
type WithholdingRecorder interface {
	RecordWithholding(ctx context.Context, legalEntityID string, entries []tax.Entry) error
}

type PaidSource interface {
	PaidToDate(ctx context.Context, legalEntityID, vendorID, currency string, year int) (decimal.Decimal, error)
}

type withholdingDeps struct {
	calc     WithholdingCalculator
	paid     PaidSource
	profiles PostingProfiles
	faults   atomic.Int64
	rejected atomic.Int64
}

// WithWithholdingRecorder sets where a payment run records the tax it withheld; without it nothing is recorded.
func (a *API) WithWithholdingRecorder(rec WithholdingRecorder) *API {
	a.withholdingRecorder = rec
	return a
}

func (a *API) WithWithholding(c WithholdingCalculator, paid PaidSource, profiles PostingProfiles) *API {
	a.withholding = &withholdingDeps{calc: c, paid: paid, profiles: profiles}
	return a
}

func (a *API) applyWithholding(w http.ResponseWriter, r *http.Request, le string, runDate time.Time, req proposalRequest, groups []domain.ProposalGroup) bool {
	if len(req.WithholdingCodes) == 0 {
		return true
	}
	if a.withholding == nil || a.withholding.calc == nil {
		a.problem(w, r, http.StatusServiceUnavailable, codeNotConfigured, "withholding calculation is not configured")
		return false
	}
	profile, _ := a.withholding.profiles.For(le)
	defined := map[string]tax.Code{}
	for _, c := range profile.WithholdingCodes {
		defined[c.Code] = c
	}
	for vendor, code := range req.WithholdingCodes {
		if _, ok := defined[code]; !ok {
			a.problem(w, r, http.StatusUnprocessableEntity, domain.CodeWithholding, "withholding code "+code+" for vendor "+vendor+" is not configured for the legal entity")
			return false
		}
	}
	ctx := r.Context()
	minor := money.MinorUnits(req.LegalEntityCurrency)
	paidSoFar, external := map[string]decimal.Decimal{}, map[string]decimal.Decimal{}
	for gi := range groups {
		g := &groups[gi]
		code, ok := req.WithholdingCodes[g.VendorID]
		if !ok {
			continue
		}
		key := g.VendorID + "|" + g.Currency
		base, seen := paidSoFar[key]
		if !seen {
			var err error
			if base, err = a.withholding.paid.PaidToDate(ctx, le, g.VendorID, g.Currency, runDate.Year()); err != nil {
				a.fault(w, r, "withholding paid-to-date failed", err)
				return false
			}
			external[key] = base
		}
		tracked := external[key]
		g.PaidBase = &tracked
		for li := range g.Lines {
			pay := g.Lines[li].Pay
			res, err := a.withholding.calc.Calculate(ctx, le, tax.Request{
				Currency: g.Currency, PaymentDate: runDate.Format(time.DateOnly), Code: defined[code], Gross: pay, PaidToDate: base,
			})
			if err != nil {
				a.withholdingFailure(w, r, err)
				return false
			}
			base = base.Add(pay)
			if res.Withheld.IsPositive() {
				g.Withhold(li, code, res.Withheld, minor)
				g.Lines[li].WithheldBase = res.TaxableBase
			}
		}
		paidSoFar[key] = base
	}
	return true
}

func (a *API) withholdingFailure(w http.ResponseWriter, r *http.Request, err error) {
	var rule *tax.RuleError
	if errors.As(err, &rule) {
		a.withholding.rejected.Add(1)
		a.problem(w, r, http.StatusUnprocessableEntity, domain.CodeWithholding, "the tax service rejected the withholding calculation: "+rule.Code)
		return
	}
	a.withholding.faults.Add(1)
	a.upstreamFailure(w, r, "withholding calculation", err, false)
}

func (a *API) writeWithholdingMetrics(w http.ResponseWriter) {
	if a.withholding == nil {
		return
	}
	fmt.Fprintf(w, "# HELP payables_withholding_rejected_total Withholding calculations the tax service refused as a rule problem.\n# TYPE payables_withholding_rejected_total counter\npayables_withholding_rejected_total %d\n", a.withholding.rejected.Load())
	fmt.Fprintf(w, "# HELP payables_withholding_faults_total Withholding calculations that failed on a server or transport fault.\n# TYPE payables_withholding_faults_total counter\npayables_withholding_faults_total %d\n", a.withholding.faults.Load())
}
