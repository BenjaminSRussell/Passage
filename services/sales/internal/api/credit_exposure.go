package api

import (
	"errors"
	"net/http"
	"sort"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

const (
	creditExposure = "/v1/sales/credit-exposure"
	// exposureBatch is how many profiles the report reads at a time.
	exposureBatch = 500
	// maxExposureProfiles bounds the profiles one report measures; past it the
	// response says the ranking is partial.
	maxExposureProfiles = 50000
)

// errExposureCap stops the profile scan once the cap is passed.
var errExposureCap = errors.New("credit exposure: profile cap reached")

var utilizationStatuses = map[string]bool{domain.UtilNoLimit: true, domain.UtilWithin: true,
	domain.UtilOverLimit: true, domain.UtilOverTolerance: true}

// creditExposureRow is one customer of the credit exposure report (PRD
// §5.27.14): the profile's limit, the exposure behind it and how much of the
// limit it uses.
type creditExposureRow struct {
	LegalEntityID string          `json:"legalEntityId"`
	CustomerID    string          `json:"customerId"`
	Currency      string          `json:"currency"`
	LimitType     string          `json:"limitType"`
	Limit         decimal.Decimal `json:"limit"`
	// Threshold is the limit with the tolerance: above it CR-1 holds an order.
	Threshold decimal.Decimal `json:"threshold"`
	Exposure  domain.Exposure `json:"exposure"`
	// Balance is what the limit type counts against the limit.
	Balance        decimal.Decimal  `json:"balance"`
	UtilizationPct *decimal.Decimal `json:"utilizationPct,omitempty"`
	Status         string           `json:"status"`
	// Understated marks a balance that leaves out other-currency amounts.
	Understated bool    `json:"understated"`
	ReviewDate  *string `json:"reviewDate,omitempty"`
}

// listCreditExposure ranks customers by the share of their credit limit that
// their exposure uses, highest first. The figures are the ones the credit check
// would use now: open AR and unapplied cash as stored on the profile, the open
// orders and delivered-not-invoiced goods computed from the orders.
func (a *API) listCreditExposure(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	legalEntity, ok := creditLegalEntity(w, r, false)
	if !ok {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	customerID, status := q.Get("customerId"), q.Get("status")
	if customerID != "" && !ids.Valid(customerID) {
		fields["customerId"] = "must be a UUID"
	}
	if status != "" && !utilizationStatuses[status] {
		fields["status"] = "must be NO_LIMIT, WITHIN, OVER_LIMIT or OVER_TOLERANCE"
	}
	var minPct *decimal.Decimal
	if raw := q.Get("minUtilizationPct"); raw != "" {
		pct, err := decimal.NewFromString(raw)
		if err != nil || pct.IsNegative() || pct.Exponent() < -2 {
			fields["minUtilizationPct"] = "must be a percent of 0 or more with up to 2 decimals"
		} else {
			minPct = &pct
		}
	}
	limit, ok := pageLimit(q.Get("limit"))
	if !ok {
		fields["limit"] = "must be 1 to 200"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	// Every profile in scope is measured (paged by key) and only the best page is
	// kept, so the top of the ranking is right however many profiles there are,
	// up to maxExposureProfiles.
	var rows []creditExposureRow
	total, scanned, truncated := 0, 0, false
	err := a.store.ScanCreditProfiles(r.Context(), legalEntity, customerID, exposureBatch, func(page []store.CreditProfile) error {
		for _, c := range page {
			if scanned == maxExposureProfiles {
				truncated = true
				return errExposureCap
			}
			scanned++
			u := domain.Utilize(c.CreditProfile, c.Exposure)
			if status != "" && u.Status != status {
				continue
			}
			if minPct != nil && (u.Pct == nil || u.Pct.LessThan(*minPct)) {
				continue
			}
			total++
			rows = append(rows, creditExposureRow{LegalEntityID: c.LegalEntityID, CustomerID: c.CustomerID, Currency: c.Currency,
				LimitType: c.LimitType, Limit: c.Limit, Threshold: u.Threshold, Exposure: c.Exposure, Balance: u.Balance,
				UtilizationPct: u.Pct, Status: u.Status, Understated: u.Understated, ReviewDate: dayPtr(c.ReviewDate)})
		}
		if len(rows) > 2*limit {
			rows = rankExposure(rows)[:limit]
		}
		return nil
	})
	if err != nil && !errors.Is(err, errExposureCap) {
		a.fail(w, r, "list credit exposure", err)
		return
	}
	rows = rankExposure(rows)
	if len(rows) > limit {
		rows = rows[:limit]
	}
	if rows == nil {
		rows = []creditExposureRow{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": rows, "total": total, "truncated": truncated})
}

// rankExposure orders rows by the share of the limit used, highest first, with
// customers without a limit last. The sort is stable, so ties keep the scan
// order (legal entity, then customer).
func rankExposure(rows []creditExposureRow) []creditExposureRow {
	sort.SliceStable(rows, func(i, j int) bool {
		pi, pj := rows[i].UtilizationPct, rows[j].UtilizationPct
		if pi == nil || pj == nil {
			return pi != nil && pj == nil
		}
		return pi.GreaterThan(*pj)
	})
	return rows
}
