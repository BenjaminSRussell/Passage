// Package api serves the revrec HTTP surface. Slice 1 exposes the stateless
// allocation preview (POST /v1/revrec/allocations:preview); contracts and
// recognition runs are persisted in a later slice of PSG-29.
package api

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/revrec/internal/domain"
)

const (
	maxObligations = 50
	maxIDLength    = 64
	// dutyView is RevRecView (§5.29.17): the preview computes and stores nothing.
	dutyView         = "revrec.view"
	validationDetail = "request failed validation"
	dateLayout       = "2006-01-02"
)

var (
	currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)
	decimalText  = regexp.MustCompile(`^[0-9]{1,14}(\.[0-9]{1,6})?$`)
)

// API holds the handlers and their counters.
type API struct {
	previews atomic.Int64
}

// New returns an API with zeroed counters.
func New() *API { return &API{} }

// Register mounts the revrec routes on r.
func (a *API) Register(r chi.Router) {
	r.With(authz.RequireDuty(dutyView)).Post("/v1/revrec/allocations:preview", a.preview)
}

// Metrics serves the counters in the Prometheus text format on the admin port.
func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# HELP revrec_allocation_previews_total Allocation previews answered.\n# TYPE revrec_allocation_previews_total counter\nrevrec_allocation_previews_total %d\n", a.previews.Load())
	})
}

// Previews returns how many allocation previews were answered.
func (a *API) Previews() int64 { return a.previews.Load() }

type obligationDTO struct {
	ID        string  `json:"id"`
	SSP       string  `json:"ssp"`
	SSPSource string  `json:"sspSource"`
	Method    string  `json:"method"`
	TermStart *string `json:"termStart,omitempty"`
	TermEnd   *string `json:"termEnd,omitempty"`
}

type previewRequest struct {
	Currency         string          `json:"currency"`
	TransactionPrice string          `json:"transactionPrice"`
	ResidualRule     string          `json:"residualRule,omitempty"`
	Obligations      []obligationDTO `json:"obligations"`
}

type scheduleDTO struct {
	Start  string `json:"start"`
	End    string `json:"end"`
	Amount string `json:"amount"`
}

type allocationDTO struct {
	ID        string        `json:"id"`
	SSP       string        `json:"ssp"`
	SSPSource string        `json:"sspSource"`
	Method    string        `json:"method"`
	Allocated string        `json:"allocated"`
	Schedule  []scheduleDTO `json:"schedule,omitempty"`
}

type previewResponse struct {
	Currency         string          `json:"currency"`
	TransactionPrice string          `json:"transactionPrice"`
	ResidualRule     string          `json:"residualRule"`
	Obligations      []allocationDTO `json:"obligations"`
}

func (a *API) preview(w http.ResponseWriter, r *http.Request) {
	var req previewRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if req.ResidualRule == "" {
		req.ResidualRule = domain.ResidualLargest
	}
	if !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	if !decimalText.MatchString(req.TransactionPrice) {
		fields["transactionPrice"] = "must be a non-negative decimal with at most 6 fraction digits"
	}
	if !domain.ValidResidualRule(req.ResidualRule) {
		fields["residualRule"] = "must be LARGEST, FIRST or LAST"
	}
	if n := len(req.Obligations); n == 0 || n > maxObligations {
		fields["obligations"] = "must hold 1 to 50 obligations"
	}
	obs, terms, ok := toDomain(req.Obligations, fields)
	if !ok || len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	price := money.Amount{Value: decimal.RequireFromString(req.TransactionPrice), Currency: req.Currency}.Round()
	allocated, err := domain.Allocate(price, obs, req.ResidualRule)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	resp := previewResponse{Currency: req.Currency, TransactionPrice: fixed(price.Value, req.Currency), ResidualRule: req.ResidualRule}
	for i, o := range allocated {
		item := allocationDTO{ID: o.ID, SSP: fixed(o.SSP, req.Currency), SSPSource: o.SSPSource, Method: o.Method, Allocated: fixed(o.Allocated, req.Currency)}
		if t := terms[i]; t != nil {
			periods, err := domain.RatableSchedule(money.Amount{Value: o.Allocated, Currency: req.Currency}, t[0], t[1])
			if err != nil {
				httpx.Fail(w, r, problemFrom(err))
				return
			}
			for _, p := range periods {
				item.Schedule = append(item.Schedule, scheduleDTO{Start: p.Start.Format(dateLayout), End: p.End.Format(dateLayout), Amount: fixed(p.Amount, req.Currency)})
			}
		}
		resp.Obligations = append(resp.Obligations, item)
	}
	a.previews.Add(1)
	httpx.JSON(w, http.StatusOK, resp)
}

// toDomain converts the DTOs, recording field errors. terms[i] is the ratable
// term of obligation i, or nil.
func toDomain(in []obligationDTO, fields map[string]string) ([]domain.Obligation, []*[2]time.Time, bool) {
	out := make([]domain.Obligation, 0, len(in))
	terms := make([]*[2]time.Time, len(in))
	seen := map[string]bool{}
	valid := true
	for i, o := range in {
		key := func(name string) string { return "obligations[" + strconv.Itoa(i) + "]." + name }
		if o.ID == "" || len(o.ID) > maxIDLength || seen[o.ID] {
			fields[key("id")] = "must be present, unique and at most 64 characters"
			valid = false
		}
		seen[o.ID] = true
		ssp := decimal.Zero
		if decimalText.MatchString(o.SSP) {
			ssp = decimal.RequireFromString(o.SSP)
		}
		if !ssp.IsPositive() {
			fields[key("ssp")] = "must be a positive decimal with at most 6 fraction digits"
			valid = false
		}
		if !domain.ValidSSPSource(o.SSPSource) {
			fields[key("sspSource")] = "must be EXPLICIT, LIST, PCT_OF_LIST or RESIDUAL"
			valid = false
		}
		if !domain.ValidMethod(o.Method) {
			fields[key("method")] = "must be POINT_IN_TIME, RATABLE, MILESTONE or USAGE"
			valid = false
		}
		if o.Method == "RATABLE" {
			start, end, ok := parseTerm(o.TermStart, o.TermEnd)
			if !ok {
				fields[key("term")] = "a RATABLE obligation needs termStart and termEnd as YYYY-MM-DD"
				valid = false
			}
			if ok {
				terms[i] = &[2]time.Time{start, end}
			}
		}
		out = append(out, domain.Obligation{ID: o.ID, SSP: ssp, SSPSource: o.SSPSource, Method: o.Method})
	}
	return out, terms, valid
}

func parseTerm(start, end *string) (time.Time, time.Time, bool) {
	if start == nil || end == nil {
		return time.Time{}, time.Time{}, false
	}
	s, err1 := time.Parse(dateLayout, *start)
	e, err2 := time.Parse(dateLayout, *end)
	return s, e, err1 == nil && err2 == nil && !e.Before(s)
}

func problemFrom(err error) error {
	var de *domain.Error
	if errors.As(err, &de) {
		status := http.StatusUnprocessableEntity
		if de.Code == "COM-0001" {
			status = http.StatusBadRequest
		}
		return httpx.NewProblem(status, de.Code, de.Detail)
	}
	return err
}

func writeFields(w http.ResponseWriter, r *http.Request, fields map[string]string) {
	p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
	p.Errors = fields
	httpx.Fail(w, r, p)
}

func fixed(v decimal.Decimal, currency string) string {
	return v.StringFixed(money.MinorUnits(currency))
}
