package api

import (
	"net/http"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

// reviseAgreementRequest is the body of PATCH /v1/sales/agreements/{id}. Every
// field is optional; an empty validFrom or validTo clears that bound.
type reviseAgreementRequest struct {
	// CommitmentQty changes a QUANTITY agreement's commitment, CommitmentAmount an
	// AMOUNT agreement's; the other one is refused.
	CommitmentQty    *string `json:"commitmentQty"`
	CommitmentAmount *string `json:"commitmentAmount"`
	UnitPrice        *string `json:"unitPrice"`
	CumTolerance     *string `json:"cumTolerance"`
	// OverdeliveryPct changes the tolerance above the commitment (BR-27.9).
	OverdeliveryPct *string `json:"overdeliveryPct"`
	ValidFrom       *string `json:"validFrom"`
	ValidTo         *string `json:"validTo"`
	Reason          string  `json:"reason"`
}

// closeAgreementRequest is the body of POST /v1/sales/agreements/{id}:close.
type closeAgreementRequest struct {
	Reason string `json:"reason"`
}

// buildRevision validates a partial change. Field errors are keyed by JSON path.
func buildRevision(req reviseAgreementRequest) (store.AgreementRevision, map[string]string) {
	f := map[string]string{}
	out := store.AgreementRevision{ValidFrom: req.ValidFrom, ValidTo: req.ValidTo, Reason: req.Reason}
	if req.CommitmentQty != nil {
		q, err := decimal.NewFromString(*req.CommitmentQty)
		if err != nil {
			f["commitmentQty"] = "must be a decimal"
		} else if err := domain.ValidateQuantity(q); err != nil {
			f["commitmentQty"] = err.Error()
		} else {
			out.Commitment = &q
		}
	}
	if req.CommitmentAmount != nil {
		q, err := decimal.NewFromString(*req.CommitmentAmount)
		switch {
		case err != nil:
			f["commitmentAmount"] = "must be a decimal"
		case !q.IsPositive() || !q.Round(4).Equal(q):
			f["commitmentAmount"] = "must be greater than 0 with at most 4 decimal places"
		case req.CommitmentQty != nil:
			f["commitmentAmount"] = "send commitmentQty or commitmentAmount, not both"
		default:
			out.Commitment, out.CommitmentIsAmount = &q, true
		}
	}
	if req.UnitPrice != nil {
		price, err := decimal.NewFromString(*req.UnitPrice)
		if err != nil || price.IsNegative() || !price.Round(4).Equal(price) {
			f["unitPrice"] = "must be a decimal of at least 0 with at most 4 places"
		} else {
			out.UnitPrice = &price
		}
	}
	if req.CumTolerance != nil {
		tol, err := decimal.NewFromString(*req.CumTolerance)
		if err != nil {
			f["cumTolerance"] = "must be a decimal of at least 0"
		} else if err := domain.ValidateCumulative(tol); err != nil {
			f["cumTolerance"] = err.Error()
		} else {
			out.CumTolerance = &tol
		}
	}
	if req.OverdeliveryPct != nil {
		pct, err := decimal.NewFromString(*req.OverdeliveryPct)
		if err != nil {
			f["overdeliveryPct"] = "must be a decimal from 0 to 100 with at most 2 places"
		} else if err := domain.ValidateOverdelivery(pct); err != nil {
			f["overdeliveryPct"] = err.Error()
		} else {
			out.OverdeliveryPct = &pct
		}
	}
	for name, v := range map[string]*string{"validFrom": req.ValidFrom, "validTo": req.ValidTo} {
		if v != nil && *v != "" {
			if _, err := time.Parse(dayLayout, *v); err != nil {
				f[name] = "must be a date such as 2027-03-04, or empty to clear"
			}
		}
	}
	if len(req.Reason) > 500 {
		f["reason"] = "must be at most 500 characters"
	}
	if req.CommitmentQty == nil && req.CommitmentAmount == nil && req.UnitPrice == nil && req.CumTolerance == nil && req.OverdeliveryPct == nil && req.ValidFrom == nil && req.ValidTo == nil {
		f["body"] = "at least one of commitmentQty, commitmentAmount, unitPrice, cumTolerance, overdeliveryPct, validFrom or validTo is required"
	}
	return out, f
}

// reviseAgreement changes a commitment at its ETag (FR-27.10). A commitment
// below the released quantity is 422 SLS-0053 and a closed agreement 422
// SLS-0052.
func (a *API) reviseAgreement(w http.ResponseWriter, r *http.Request) {
	agreementRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		version, ok := ifMatch(w, r)
		if !ok {
			return
		}
		var req reviseAgreementRequest
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		in, fields := buildRevision(req)
		if len(fields) > 0 {
			validationFailed(w, r, fields)
			return
		}
		p, _ := tenant.FromContext(r.Context())
		saved, err := a.store.ReviseAgreement(r.Context(), id, version, in, p.UserID, a.now().UTC().Truncate(time.Microsecond))
		if err != nil {
			a.fail(w, r, "revise agreement", err)
			return
		}
		setETag(w, saved.Version)
		httpx.JSON(w, http.StatusOK, saved)
	})(w, r)
}

// closeAgreement ends an agreement early at its ETag with a reason.
func (a *API) closeAgreement(w http.ResponseWriter, r *http.Request) {
	agreementRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		version, ok := ifMatch(w, r)
		if !ok {
			return
		}
		var req closeAgreementRequest
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if n := len(req.Reason); n == 0 || n > 500 {
			validationFailed(w, r, map[string]string{"reason": "must be 1 to 500 characters"})
			return
		}
		p, _ := tenant.FromContext(r.Context())
		saved, err := a.store.CloseAgreement(r.Context(), id, version, req.Reason, p.UserID, a.now().UTC().Truncate(time.Microsecond))
		if err != nil {
			a.fail(w, r, "close agreement", err)
			return
		}
		setETag(w, saved.Version)
		httpx.JSON(w, http.StatusOK, saved)
	})(w, r)
}

// listAgreementVersions returns the agreement's revisions, close and returned
// quantity, newest first.
func (a *API) listAgreementVersions(w http.ResponseWriter, r *http.Request) {
	agreementRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		items, err := a.store.ListAgreementVersions(r.Context(), id)
		if err != nil {
			a.fail(w, r, "list agreement versions", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
	})(w, r)
}
