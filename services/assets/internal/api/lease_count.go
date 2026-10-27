package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/assets/internal/domain"
	"github.com/BenjaminSRussell/passage/services/assets/internal/store"
)

const (
	// maxCountScans bounds the scans of one count session (FR-37.11).
	maxCountScans = 5000
	// maxBarcodeLength matches asset.barcode.
	maxBarcodeLength = 64
	// maxExpiryWindow bounds ?expiringWithinDays= of the insurance report.
	maxExpiryWindow = 366
)

var validInsuranceType = map[string]bool{"Property": true, "MachineryBreakdown": true, "Transit": true, "Fleet": true}

type leaseModifyRequest struct {
	EffectivePeriod int     `json:"effectivePeriod"`
	PaymentAmount   string  `json:"paymentAmount"`
	TermMonths      *int    `json:"termMonths"`
	ScopeChange     bool    `json:"scopeChange"`
	IBRRate         *string `json:"ibrRate"`
}

func leaseID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeLeaseNotFound, "no lease with id "+id))
		return "", false
	}
	return id, true
}

// modifyLease remeasures a lease from effectivePeriod on (FR-37.10, BR-37.8).
// A payment change keeps the lease's rate; a scope change (which a revised
// term always is) takes the revised rate in ibrRate.
func (a *API) modifyLease(w http.ResponseWriter, r *http.Request) {
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	id, ok := leaseID(w, r)
	if !ok {
		return
	}
	var req leaseModifyRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if req.EffectivePeriod < 1 {
		fields["effectivePeriod"] = "must be the schedule period (1 or later) the revised terms start from"
	}
	payment, err := decimal.NewFromString(req.PaymentAmount)
	if err != nil || !payment.IsPositive() {
		fields["paymentAmount"] = "must be a positive decimal"
	}
	term := 0
	if req.TermMonths != nil {
		term = *req.TermMonths
		if term < 1 || term > domain.MaxLeaseTermMonths {
			fields["termMonths"] = "must be 1 to 600 months, or omitted to keep the term"
		} else if !req.ScopeChange {
			fields["termMonths"] = "a revised term is a scope change: set scopeChange and give the revised ibrRate"
		}
	}
	var rate *decimal.Decimal
	switch {
	case req.ScopeChange && req.IBRRate == nil:
		fields["ibrRate"] = "is required for a scope change (the revised discount rate)"
	case !req.ScopeChange && req.IBRRate != nil:
		fields["ibrRate"] = "applies only to a scope change; a payment change keeps the lease's rate"
	case req.IBRRate != nil:
		v, err := decimal.NewFromString(*req.IBRRate)
		if err != nil {
			fields["ibrRate"] = "must be a decimal such as 0.05"
		} else {
			rate = &v
		}
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	res, err := a.store.ModifyLease(r.Context(), id, store.LeaseModificationInput{Version: version, EffectivePeriod: req.EffectivePeriod,
		PaymentAmount: payment, TermMonths: term, ScopeChange: req.ScopeChange, IBRRate: rate}, key)
	if err != nil {
		writeDomain(w, r, err)
		return
	}
	a.remeasured.Add(1)
	setETag(w, res.Lease.Version)
	httpx.JSON(w, http.StatusCreated, res)
}

type countSessionRequest struct {
	LegalEntityID string   `json:"legalEntityId"`
	Location      string   `json:"location"`
	CountedDate   *string  `json:"countedDate"`
	Barcodes      []string `json:"barcodes"`
}

// createCountSession reconciles a physical inventory scan (FR-37.11). Unknown
// barcodes come back as AST-0525 warnings; the register is not changed.
func (a *API) createCountSession(w http.ResponseWriter, r *http.Request) {
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req countSessionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	location := strings.TrimSpace(req.Location)
	if location == "" || len(location) > 80 {
		fields["location"] = "must be 1 to 80 characters"
	}
	date := store.NewDate(a.now())
	if req.CountedDate != nil {
		var err error
		if date, err = store.ParseDate(*req.CountedDate); err != nil {
			fields["countedDate"] = "must be a YYYY-MM-DD date"
		}
	}
	scans := make([]string, 0, len(req.Barcodes))
	if req.Barcodes == nil {
		fields["barcodes"] = "is required; an empty list records a count where nothing was found"
	} else if len(req.Barcodes) > maxCountScans {
		fields["barcodes"] = "must hold at most 5000 scans per session"
	} else {
		for i, raw := range req.Barcodes {
			code := strings.TrimSpace(raw)
			if code == "" || len(code) > maxBarcodeLength {
				fields["barcodes"] = "scan " + strconv.Itoa(i+1) + " must be 1 to 64 characters"
				break
			}
			scans = append(scans, code)
		}
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	if !inScope(w, r, req.LegalEntityID) {
		return
	}
	session, err := a.store.CreateCountSession(r.Context(), store.CountSessionInput{LegalEntityID: req.LegalEntityID, Location: location,
		CountedDate: date, Barcodes: scans}, key)
	if err != nil {
		writeDomain(w, r, err)
		return
	}
	a.counted.Add(1)
	httpx.JSON(w, http.StatusCreated, session)
}

func (a *API) listCountSessions(w http.ResponseWriter, r *http.Request) {
	fields := map[string]string{}
	limit, after := pageLimit(r, fields)
	legalEntity := r.URL.Query().Get("legalEntityId")
	if legalEntity != "" && !ids.Valid(legalEntity) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	page, err := a.store.ListCountSessions(r.Context(), store.CountFilter{LegalEntityID: legalEntity, After: after, Limit: limit})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, page)
}

func (a *API) getCountSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeCountSessionNotFound, "no count session with id "+id))
		return
	}
	session, err := a.store.GetCountSession(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, session)
}

// insuranceReport lists the policies of a legal entity in force on asOf
// (default today) with insured, indexed and book values (7.37.14).
func (a *API) insuranceReport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fields := map[string]string{}
	limit, after := pageLimit(r, fields)
	legalEntity := q.Get("legalEntityId")
	if !ids.Valid(legalEntity) {
		fields["legalEntityId"] = "must be a UUID"
	}
	asOf := store.NewDate(a.now())
	if raw := q.Get("asOf"); raw != "" {
		var err error
		if asOf, err = store.ParseDate(raw); err != nil {
			fields["asOf"] = "must be a YYYY-MM-DD date"
		}
	}
	typ := q.Get("insuranceType")
	if typ != "" && !validInsuranceType[typ] {
		fields["insuranceType"] = "must be Property, MachineryBreakdown, Transit or Fleet"
	}
	var within *int
	if raw := q.Get("expiringWithinDays"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > maxExpiryWindow {
			fields["expiringWithinDays"] = "must be 0 to 366"
		} else {
			within = &n
		}
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	if !inScope(w, r, legalEntity) {
		return
	}
	report, err := a.store.InsuranceReport(r.Context(), store.InsuranceReportFilter{LegalEntityID: legalEntity, AsOf: asOf, InsuranceType: typ,
		ExpiringWithinDays: within, After: after, Limit: limit})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, report)
}
