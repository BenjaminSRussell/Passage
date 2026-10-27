package api

import (
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/lean/internal/domain"
)

const (
	dateLayout   = "2006-01-02"
	maxPreviewIn = 500
)

var currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)

// handle serves a stateless preview: decode the body, run fn, write the result
// or the rule's problem.
func handle[In any, Out any](fn func(In) (Out, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := principal(w, r); !ok {
			return
		}
		var in In
		if err := httpx.Decode(r, &in); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		out, err := fn(in)
		if err != nil {
			fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, out)
	}
}

func invalidf(format string, args ...any) error {
	return &domain.Error{Code: domain.CodeInvalid, Detail: fmt.Sprintf(format, args...)}
}

func previewDate(field, s string) (time.Time, error) {
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		return time.Time{}, invalidf("%s must be a date (yyyy-mm-dd)", field)
	}
	return t, nil
}

func validRef(s string) bool { return s != "" && len(s) <= maxRefLen }

type agreementDTO struct {
	VendorID  string          `json:"vendorId"`
	ItemID    string          `json:"itemId"`
	UnitPrice decimal.Decimal `json:"unitPrice"`
	Currency  string          `json:"currency"`
	ValidFrom string          `json:"validFrom"`
	ValidTo   *string         `json:"validTo"`
}

type serviceJobDTO struct {
	JobID       string          `json:"jobId"`
	VendorID    string          `json:"vendorId"`
	ItemID      string          `json:"itemId"`
	Qty         decimal.Decimal `json:"qty"`
	CompletedOn string          `json:"completedOn"`
}

type subcontractRequest struct {
	Mode       string          `json:"mode"`
	Agreements []agreementDTO  `json:"agreements"`
	Jobs       []serviceJobDTO `json:"jobs"`
}

type poLineDTO struct {
	VendorID  string          `json:"vendorId"`
	ItemID    string          `json:"itemId"`
	Qty       decimal.Decimal `json:"qty"`
	UnitPrice decimal.Decimal `json:"unitPrice"`
	Currency  string          `json:"currency"`
	PriceDate string          `json:"priceDate"`
	Amount    decimal.Decimal `json:"amount"`
	JobIDs    []string        `json:"jobIds"`
}

type subcontractResponse struct {
	Mode  string      `json:"mode"`
	Lines []poLineDTO `json:"lines"`
}

func toAgreement(i int, a agreementDTO) (domain.Agreement, error) {
	field := fmt.Sprintf("agreements[%d]", i)
	if !validRef(a.VendorID) || !validRef(a.ItemID) {
		return domain.Agreement{}, invalidf("%s needs a vendorId and an itemId of 1 to %d characters", field, maxRefLen)
	}
	if a.UnitPrice.IsNegative() || a.UnitPrice.Exponent() < -6 || a.UnitPrice.GreaterThanOrEqual(maxQty()) {
		return domain.Agreement{}, invalidf("%s.unitPrice must be from 0 below 10^12 with at most 6 decimals", field)
	}
	if !currencyCode.MatchString(a.Currency) {
		return domain.Agreement{}, invalidf("%s.currency must be an ISO 4217 code such as USD", field)
	}
	from, err := previewDate(field+".validFrom", a.ValidFrom)
	if err != nil {
		return domain.Agreement{}, err
	}
	out := domain.Agreement{VendorID: a.VendorID, ItemID: a.ItemID, UnitPrice: a.UnitPrice, Currency: a.Currency, ValidFrom: from}
	if a.ValidTo != nil {
		if out.ValidTo, err = previewDate(field+".validTo", *a.ValidTo); err != nil {
			return domain.Agreement{}, err
		}
	}
	return out, nil
}

// subcontractLines previews the purchase order lines for completed service
// jobs on vendor work cells (FR-24.20, BR-24.16): one line per job
// (po-per-kanban) or one per vendor, item and price (periodic-po), priced by the
// agreement valid on the completion date. A job without one is 422 LEAN-0005.
func subcontractLines(in subcontractRequest) (subcontractResponse, error) {
	if len(in.Agreements) > maxPreviewIn || len(in.Jobs) > maxPreviewIn {
		return subcontractResponse{}, invalidf("at most %d agreements and %d jobs", maxPreviewIn, maxPreviewIn)
	}
	ags := make([]domain.Agreement, len(in.Agreements))
	for i, a := range in.Agreements {
		ag, err := toAgreement(i, a)
		if err != nil {
			return subcontractResponse{}, err
		}
		ags[i] = ag
	}
	jobs := make([]domain.ServiceJob, len(in.Jobs))
	for i, j := range in.Jobs {
		field := fmt.Sprintf("jobs[%d]", i)
		if !validRef(j.JobID) || !validRef(j.VendorID) || !validRef(j.ItemID) {
			return subcontractResponse{}, invalidf("%s needs a jobId, vendorId and itemId of 1 to %d characters", field, maxRefLen)
		}
		if !validQty(j.Qty) {
			return subcontractResponse{}, invalidf("%s.qty must be a positive quantity below 10^12 with at most 6 decimals", field)
		}
		done, err := previewDate(field+".completedOn", j.CompletedOn)
		if err != nil {
			return subcontractResponse{}, err
		}
		jobs[i] = domain.ServiceJob{JobID: j.JobID, VendorID: j.VendorID, ItemID: j.ItemID, Qty: j.Qty, CompletedOn: done}
	}
	lines, err := domain.SubcontractLines(in.Mode, ags, jobs)
	if err != nil {
		return subcontractResponse{}, err
	}
	out := subcontractResponse{Mode: in.Mode, Lines: make([]poLineDTO, len(lines))}
	for i, l := range lines {
		out.Lines[i] = poLineDTO{VendorID: l.VendorID, ItemID: l.ItemID, Qty: l.Qty, UnitPrice: l.UnitPrice,
			Currency: l.Currency, PriceDate: l.PriceDate.Format(dateLayout), Amount: l.Amount, JobIDs: l.JobIDs}
	}
	return out, nil
}
