package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
)

const (
	codeInvalidRelease = "SLS-0026"
	codeInvalidRun     = "SLS-0027"
	codeCumMismatch    = "SLS-0014"
	cumMismatchReason  = "CUM_QTY_MISMATCH"
)

type releaseBucketRequest struct {
	Date        string `json:"date"`
	CumRequired string `json:"cumRequired"`
}

// scheduleReleaseRequest is one scheduling-agreement release (FR-27.20). The
// customer's cumulative received quantity is reconciled against what was
// shipped since the cumulative reset.
type scheduleReleaseRequest struct {
	CumReceived string                 `json:"cumReceived"`
	CumShipped  string                 `json:"cumShipped"`
	Tolerance   *string                `json:"tolerance"`
	Buckets     []releaseBucketRequest `json:"buckets"`
}

// releaseCheckResponse reports the reconciliation. A held release (mismatch)
// returns 200 with held=true and no schedule lines: it changes nothing (BR-27.12).
type releaseCheckResponse struct {
	Held         bool                 `json:"held"`
	Code         string               `json:"code,omitempty"`
	ServiceCode  string               `json:"serviceCode,omitempty"`
	Difference   decimal.Decimal      `json:"difference"`
	OpenQuantity decimal.Decimal      `json:"openQuantity"`
	Lines        []domain.ReleaseLine `json:"lines"`
}

// parseDecimal parses a required decimal field, recording a field error.
func parseDecimal(f map[string]string, key, s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		f[key] = "must be a decimal"
	}
	return d
}

// parseBuckets validates the requirement buckets of a release, recording field
// errors keyed by buckets[i].date and buckets[i].cumRequired.
func parseBuckets(f map[string]string, in []releaseBucketRequest) []domain.ReleaseBucket {
	if len(in) == 0 || len(in) > maxPeriods {
		f["buckets"] = fmt.Sprintf("must have 1 to %d buckets", maxPeriods)
	}
	buckets := make([]domain.ReleaseBucket, 0, len(in))
	for i, b := range in {
		if _, err := time.Parse(dayLayout, b.Date); err != nil {
			f[fmt.Sprintf("buckets[%d].date", i)] = "must be a date such as 2027-04-15"
		}
		key := fmt.Sprintf("buckets[%d].cumRequired", i)
		cum := parseDecimal(f, key, b.CumRequired)
		if err := domain.ValidateCumulative(cum); err != nil && f[key] == "" {
			f[key] = err.Error()
		}
		buckets = append(buckets, domain.ReleaseBucket{Date: b.Date, CumRequired: cum})
	}
	return buckets
}

// checkRelease reconciles a scheduling-agreement release and returns the open
// schedule lines it would create. It is stateless: nothing is stored.
func (a *API) checkRelease(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req scheduleReleaseRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	f := map[string]string{}
	cumReceived := parseDecimal(f, "cumReceived", req.CumReceived)
	cumShipped := parseDecimal(f, "cumShipped", req.CumShipped)
	tolerance := decimal.Zero
	if req.Tolerance != nil {
		tolerance = parseDecimal(f, "tolerance", *req.Tolerance)
	}
	buckets := parseBuckets(f, req.Buckets)
	if len(f) > 0 {
		validationFailed(w, r, f)
		return
	}
	res, err := domain.CheckRelease(cumReceived, cumShipped, tolerance, buckets)
	if err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, codeInvalidRelease, err.Error()))
		return
	}
	out := releaseCheckResponse{Held: res.Mismatch, Difference: res.Difference, OpenQuantity: res.OpenQuantity, Lines: res.Lines}
	if res.Mismatch {
		out.Code = cumMismatchReason
		out.ServiceCode = codeCumMismatch
	}
	httpx.JSON(w, http.StatusOK, out)
}
