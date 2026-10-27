package api

import (
	"fmt"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/domain"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/store"
)

const (
	maxDescription  = 500
	typeList        = "must be FundsReservation, Precommitment, FundsBlocking or RevenueForecast"
	reservationGone = "reservation not found"
)

type reservationLineRequest struct {
	DimensionKey string          `json:"dimensionKey"`
	SpanKey      string          `json:"spanKey"`
	Amount       decimal.Decimal `json:"amount"`
}

type reservationRequest struct {
	RuleVersionID string                   `json:"ruleVersionId"`
	Type          string                   `json:"type"`
	Description   string                   `json:"description"`
	ExpiryDate    string                   `json:"expiryDate"`
	Lines         []reservationLineRequest `json:"lines"`
}

type consumeRequest struct {
	LineID         string          `json:"lineId"`
	DocumentType   string          `json:"documentType"`
	DocumentID     string          `json:"documentId"`
	DocumentLineID string          `json:"documentLineId"`
	Amount         decimal.Decimal `json:"amount"`
}

type expireRequest struct {
	AsOf string `json:"asOf"`
}

// positiveAmount reports amount > 0 with at most 4 decimal places (numeric(19,4)).
func positiveAmount(d decimal.Decimal) bool { return d.Sign() > 0 && d.Exponent() >= -4 }

func (a *API) createReservation(w http.ResponseWriter, r *http.Request) {
	body, composite, ok := readCommand(w, r)
	if !ok {
		return
	}
	var req reservationRequest
	if err := decodeBody(body, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.RuleVersionID) {
		fields["ruleVersionId"] = "must be a UUID"
	}
	typ, typeOK := domain.ParseReservationType(req.Type)
	if !typeOK {
		fields["type"] = typeList
	}
	if utf8.RuneCountInString(req.Description) > maxDescription {
		fields["description"] = fmt.Sprintf("must be at most %d characters", maxDescription)
	}
	if req.ExpiryDate != "" {
		if _, err := time.Parse(dateLayout, req.ExpiryDate); err != nil {
			fields["expiryDate"] = "must be a date as YYYY-MM-DD"
		} else if typ == domain.FundsBlocking {
			fields["expiryDate"] = "must be empty: funds blocking does not expire"
		}
	}
	if len(req.Lines) == 0 || len(req.Lines) > maxCheckLines {
		fields["lines"] = fmt.Sprintf("must hold 1 to %d lines", maxCheckLines)
	}
	lines := make([]store.ReservationLineInput, 0, len(req.Lines))
	for i, l := range req.Lines {
		prefix := fmt.Sprintf("lines[%d].", i)
		if l.DimensionKey == "" || len(l.DimensionKey) > 200 {
			fields[prefix+"dimensionKey"] = "must be 1 to 200 characters"
		}
		if l.SpanKey == "" || len(l.SpanKey) > 40 {
			fields[prefix+"spanKey"] = "must be 1 to 40 characters"
		}
		if !positiveAmount(l.Amount) {
			fields[prefix+"amount"] = "must be positive with at most 4 decimal places"
		}
		lines = append(lines, store.ReservationLineInput{DimensionKey: l.DimensionKey, SpanKey: l.SpanKey, Amount: l.Amount})
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	p, _ := tenant.FromContext(r.Context())
	key, hash := splitKey(composite)
	in := store.ReservationRequest{RuleVersionID: req.RuleVersionID, Type: typ, Description: req.Description, ExpiryDate: req.ExpiryDate, Lines: lines}
	a.replay(w, r)(a.store.CreateReservation(r.Context(), key, hash, in, p.HasDuty(dutyOverride)))
}

func (a *API) consumeReservation(w http.ResponseWriter, r *http.Request) {
	body, composite, ok := readCommand(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeReservationNotFound, reservationGone))
		return
	}
	var req consumeRequest
	if err := decodeBody(body, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LineID) {
		fields["lineId"] = "must be a UUID"
	}
	if !docTypes[req.DocumentType] {
		fields["documentType"] = "must be Requisition, PurchaseOrder, Invoice or Journal"
	}
	if !ids.Valid(req.DocumentID) {
		fields["documentId"] = "must be a UUID"
	}
	if !ids.Valid(req.DocumentLineID) {
		fields["documentLineId"] = "must be a UUID"
	}
	if !positiveAmount(req.Amount) {
		fields["amount"] = "must be positive with at most 4 decimal places"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	key, hash := splitKey(composite)
	a.replay(w, r)(a.store.ConsumeReservation(r.Context(), key, hash, store.ConsumeRequest{
		ReservationID: id, LineID: req.LineID, DocumentType: req.DocumentType, DocumentID: req.DocumentID,
		DocumentLineID: req.DocumentLineID, Amount: req.Amount,
	}))
}

func (a *API) closeReservation(w http.ResponseWriter, r *http.Request) {
	body, composite, ok := readCommand(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeReservationNotFound, reservationGone))
		return
	}
	if len(body) > 0 {
		var empty struct{}
		if err := decodeBody(body, &empty); err != nil {
			httpx.Fail(w, r, err)
			return
		}
	}
	key, hash := splitKey(composite)
	a.replay(w, r)(a.store.CloseReservation(r.Context(), key, hash, id))
}

func (a *API) expireReservations(w http.ResponseWriter, r *http.Request) {
	body, composite, ok := readCommand(w, r)
	if !ok {
		return
	}
	var req expireRequest
	if err := decodeBody(body, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if _, err := time.Parse(dateLayout, req.AsOf); err != nil {
		validationFailed(w, r, map[string]string{"asOf": "must be a date as YYYY-MM-DD"})
		return
	}
	key, hash := splitKey(composite)
	a.replay(w, r)(a.store.ExpireReservations(r.Context(), key, hash, req.AsOf))
}

func (a *API) listReservations(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.ReservationFilter{RuleVersionID: q.Get("rule"), Type: q.Get("type"), Status: q.Get("status")}
	fields := map[string]string{}
	if f.RuleVersionID != "" && !ids.Valid(f.RuleVersionID) {
		fields["rule"] = "must be a rule version UUID"
	}
	if _, ok := domain.ParseReservationType(f.Type); f.Type != "" && !ok {
		fields["type"] = typeList
	}
	if f.Status != "" && !domain.ValidReservationStatus(f.Status) {
		fields["status"] = "must be Open, PartiallyConsumed, Consumed, Closed or Expired"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	rows, err := a.store.ListReservations(r.Context(), f)
	list(w, r, rows, err)
}

func (a *API) getReservation(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeReservationNotFound, reservationGone))
		return
	}
	res, err := a.store.GetReservation(r.Context(), id)
	one(w, r, res, err)
}
