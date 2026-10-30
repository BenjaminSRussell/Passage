package api

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/transport/internal/store"
)

// DutyGate is the gate guard's duty for check-in, seal, check-out and the gate
// log (§6.34.17). The gate log holds driver PII, so TMS_VIEW does not read it.
const DutyGate = "TMS_GATE"

// walkInAllowed is the walk_in_allowed site default (§6.34.13). Site settings
// are not modelled yet, so every site accepts walk-ins (NOT-IMPLEMENTED.md).
const walkInAllowed = true

// clockSkew is how far ahead of the server clock a kiosk or handheld time may be.
const clockSkew = 5 * time.Minute

var visitStatuses = []string{domain.VisitInYard, domain.VisitCheckedOut}

type checkInRequest struct {
	SiteID          string `json:"siteId"`
	Direction       string `json:"direction"`
	LoadID          string `json:"loadId"`
	CarrierSCAC     string `json:"carrierScac"`
	TrailerNo       string `json:"trailerNo"`
	SealNo          string `json:"sealNo"`
	DriverName      string `json:"driverName"`
	DriverLicenseID string `json:"driverLicenseId"`
	AppointmentID   string `json:"appointmentId"`
	ScheduledAt     string `json:"scheduledAt"`
	ArrivedAt       string `json:"arrivedAt"`
	FreeTimeMin     *int   `json:"freeTimeMin"`
	ToleranceMin    *int   `json:"toleranceMin"`
	LegalEntityID   string `json:"legalEntityId"`
}

type checkOutRequest struct {
	VisitID    string           `json:"visitId"`
	SealNo     string           `json:"sealNo"`
	CheckOutAt string           `json:"checkOutAt"`
	HourlyRate *decimal.Decimal `json:"hourlyRate"`
}

type sealRequest struct {
	SealNo string `json:"sealNo"`
}

// gateVisitDetail is a visit with its gate log in the order it was written.
type gateVisitDetail struct {
	domain.GateVisit
	Log []domain.GateLogEntry `json:"log"`
}

// checkIn opens a gate visit (FR-34.14, BR-34.14) and publishes gate.checkedin.
func (a *API) checkIn(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	var req checkInRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	now := a.now()
	in := domain.NormalizeCheckIn(domain.GateCheckIn{
		SiteID: req.SiteID, Direction: req.Direction, LoadID: req.LoadID, CarrierSCAC: req.CarrierSCAC, TrailerNo: req.TrailerNo,
		SealNo: req.SealNo, DriverName: req.DriverName, DriverLicenseID: req.DriverLicenseID, AppointmentID: req.AppointmentID,
		ArrivedAt: now, FreeTimeMin: domain.DefaultDetentionFreeTimeMin, ToleranceMin: domain.DefaultGateToleranceMin,
		WalkInAllowed: walkInAllowed,
	})
	if req.FreeTimeMin != nil {
		in.FreeTimeMin = *req.FreeTimeMin
	}
	if req.ToleranceMin != nil {
		in.ToleranceMin = *req.ToleranceMin
	}
	fields := map[string]string{}
	if req.ScheduledAt != "" {
		if t, ok := parseInstant(req.ScheduledAt); ok {
			in.ScheduledAt = &t
		} else {
			fields["scheduledAt"] = "must be an RFC 3339 timestamp"
		}
	}
	if req.ArrivedAt != "" {
		// A paper fallback is entered later with the original time (§6.34.19).
		t, ok := parseInstant(req.ArrivedAt)
		switch {
		case !ok:
			fields["arrivedAt"] = "must be an RFC 3339 timestamp"
		case t.After(now.Add(clockSkew)):
			fields["arrivedAt"] = "must not be in the future"
		default:
			in.ArrivedAt = t
		}
	}
	for k, v := range domain.ValidateCheckIn(in) {
		fields[k] = v
	}
	if !uuidRe.MatchString(in.SiteID) {
		fields["siteId"] = "must be a lower-case UUID"
	}
	checkOptionalID(fields, "loadId", in.LoadID)
	if in.AppointmentID != "" && !uuidRe.MatchString(in.AppointmentID) {
		fields["appointmentId"] = "must be a lower-case UUID when present"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	visit, err := domain.OpenVisit(in)
	if err != nil {
		httpx.Fail(w, r, ruleError(err))
		return
	}
	visit.LegalEntityID = req.LegalEntityID
	saved, err := a.store.CheckIn(r.Context(), visit, idem)
	if err != nil {
		a.fail(w, r, "gate check-in failed", err)
		return
	}
	a.gateCheckIns.Add(1)
	httpx.JSON(w, http.StatusCreated, saved)
}

// recordSeal records the seal applied at load confirmation (BR-34.14).
func (a *API) recordSeal(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		httpx.Fail(w, r, store.NotFound("gate visit"))
		return
	}
	var req sealRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	seal := strings.TrimSpace(req.SealNo)
	if seal == "" || len(seal) > 20 {
		validationFailed(w, r, map[string]string{"sealNo": "is required, up to 20 characters"})
		return
	}
	visit, err := a.store.RecordSeal(r.Context(), id, seal, a.now())
	if err != nil {
		a.fail(w, r, "record seal failed", err)
		return
	}
	httpx.JSON(w, http.StatusOK, visit)
}

// checkOut closes a visit with seal verification and computes detention
// (BR-34.13, BR-34.14). A seal mismatch is 422 TMS-0129 and the attempt stays
// in the gate log (AC-34.12).
func (a *API) checkOut(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	var req checkOutRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	visitID := strings.TrimSpace(req.VisitID)
	if !uuidRe.MatchString(visitID) {
		fields["visitId"] = "must be a lower-case UUID"
	}
	seal := strings.TrimSpace(req.SealNo)
	if len(seal) > 20 {
		fields["sealNo"] = "must be at most 20 characters"
	}
	now := a.now()
	at := now
	if req.CheckOutAt != "" {
		t, ok := parseInstant(req.CheckOutAt)
		switch {
		case !ok:
			fields["checkOutAt"] = "must be an RFC 3339 timestamp"
		case t.After(now.Add(clockSkew)):
			fields["checkOutAt"] = "must not be in the future"
		default:
			at = t
		}
	}
	rate := decimal.Zero
	if req.HourlyRate != nil {
		if req.HourlyRate.IsNegative() {
			fields["hourlyRate"] = "must not be negative"
		}
		rate = *req.HourlyRate
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	visit, err := a.store.CheckOut(r.Context(), visitID, seal, at, rate, idem)
	var p *httpx.Problem
	if errors.As(err, &p) && p.Code == domain.CodeSealMismatch {
		a.gateBlocked.Add(1)
	}
	if err != nil {
		a.fail(w, r, "gate check-out failed", err)
		return
	}
	a.gateCheckOuts.Add(1)
	httpx.JSON(w, http.StatusOK, visit)
}

func (a *API) listGateVisits(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.GateVisitFilter{SiteID: q.Get("siteId"), Status: q.Get("status"), CarrierSCAC: strings.ToUpper(q.Get("carrierScac")), Date: q.Get("date")}
	fields := map[string]string{}
	checkOptionalID(fields, "siteId", f.SiteID)
	if f.Status != "" && !slices.Contains(visitStatuses, f.Status) {
		fields["status"] = "must be one of " + strings.Join(visitStatuses, ", ")
	}
	if f.Date != "" {
		if _, ok := parseDate(f.Date); !ok {
			fields["date"] = "must be a date as YYYY-MM-DD"
		}
	}
	if len(f.CarrierSCAC) > 4 {
		fields["carrierScac"] = "must be a 2 to 4 letter SCAC"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	visits, err := a.store.ListGateVisits(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list gate visits failed", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": visits})
}

func (a *API) getGateVisit(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		httpx.Fail(w, r, store.NotFound("gate visit"))
		return
	}
	visit, log, err := a.store.GetGateVisit(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get gate visit failed", err)
		return
	}
	if log == nil {
		log = []domain.GateLogEntry{}
	}
	httpx.JSON(w, http.StatusOK, gateVisitDetail{GateVisit: visit, Log: log})
}

// ruleError maps a domain rule rejection raised before the store is called to
// a 422 problem with its catalog code.
func ruleError(err error) error {
	var re *domain.Error
	if errors.As(err, &re) {
		return httpx.NewProblem(http.StatusUnprocessableEntity, re.Code, re.Detail)
	}
	return err
}

// parseInstant reads an RFC 3339 timestamp as UTC, to the microsecond Postgres keeps.
func parseInstant(value string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, value)
	return t.UTC().Truncate(time.Microsecond), err == nil
}
