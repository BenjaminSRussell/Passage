package api

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/store"
)

const (
	// maxKPIWindow bounds the KPI window so one request cannot scan years.
	maxKPIWindow = 366 * 24 * time.Hour
	// Batch size of one SLA monitor pass.
	defaultScanLimit = 500
	maxScanLimit     = 5000
)

// caseTargets are the statuses a caller may request; NEW is only the start.
var caseTargets = []string{"IN_PROGRESS", "WAITING_CUSTOMER", "WAITING_PARTS", "RESOLVED", "CLOSED", "CANCELED"}

// transitionCase moves a case along its status machine, pausing, resuming or
// stopping its SLA clocks (FR-45.5, BR-45.2). If-Match carries the version the
// caller read; an optional Idempotency-Key makes a retry return the first answer.
func (a *API) transitionCase(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", store.CodeCaseUnknown, "service case not found")
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var req transitionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !oneOf(req.To, caseTargets...) {
		validationFailed(w, r, map[string]string{"to": "must be IN_PROGRESS, WAITING_CUSTOMER, WAITING_PARTS, RESOLVED, CLOSED or CANCELED"})
		return
	}
	key := r.Header.Get(idempotencyKey)
	if len(key) > 255 {
		validationFailed(w, r, map[string]string{idempotencyKey: "must be at most 255 characters"})
		return
	}
	hash := hashOf(struct {
		Op      string `json:"op"`
		ID      string `json:"id"`
		Version int    `json:"version"`
		To      string `json:"to"`
	}{"case.transition", id, version, req.To})
	c, replayed, err := a.store.TransitionCase(r.Context(), id, version, domain.CaseStatus(req.To), a.now(), key, hash)
	if err != nil {
		a.fail(w, r, "transition case", err)
		return
	}
	setETag(w, c.Version)
	a.respond(w, replayed, http.StatusOK, c)
}

// scanSLA runs one pass of the SLA monitor for the caller's tenant (BR-45.2,
// AC-45.3; job service-sla-monitor calls it every 60 s). Each warning and breach
// is recorded on its case in the transaction that writes its event, so a repeated
// pass emits nothing new.
func (a *API) scanSLA(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit := defaultScanLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxScanLimit {
			validationFailed(w, r, map[string]string{"limit": fmt.Sprintf("must be 1 to %d", maxScanLimit)})
			return
		}
		limit = n
	}
	res, err := a.store.ScanSLA(r.Context(), a.now(), limit)
	if err != nil {
		a.fail(w, r, "sla scan", err)
		return
	}
	a.slaWarnings.Add(int64(res.Warnings))
	a.slaBreaches.Add(int64(res.Breaches))
	httpx.JSON(w, http.StatusOK, res)
}

// followUpWorkOrder opens the follow-up of a finished work order (BR-45.5).
func (a *API) followUpWorkOrder(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", store.CodeWOUnknown, "service work order not found")
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	key, ok := idempotencyKeyOf(w, r)
	if !ok {
		return
	}
	child, replayed, err := a.store.CreateFollowUp(r.Context(), id, version, key, hashOf(struct {
		FollowUpOf string `json:"followUpOf"`
	}{id}))
	if err != nil {
		a.fail(w, r, "follow-up work order", err)
		return
	}
	setETag(w, child.Version)
	a.respond(w, replayed, http.StatusCreated, child)
}

// kpis reports SLA attainment, mean response and resolution time and the
// first-time-fix rate over a window and optional territory (FR-45.11).
func (a *API) kpis(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	from, err := time.Parse(time.RFC3339, q.Get("from"))
	if err != nil {
		fields["from"] = "must be an RFC 3339 instant"
	}
	to, err := time.Parse(time.RFC3339, q.Get("to"))
	if err != nil {
		fields["to"] = "must be an RFC 3339 instant"
	}
	if len(fields) == 0 {
		if !to.After(from) {
			fields["to"] = "must be after from"
		} else if to.Sub(from) > maxKPIWindow {
			fields["to"] = "window must be at most 366 days"
		}
	}
	var territory *string
	if t := q.Get("territory"); t != "" {
		if ids.Valid(t) {
			territory = &t
		} else {
			fields["territory"] = "must be a UUID"
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	k, err := a.store.KPIs(r.Context(), store.KPIFilter{From: from.UTC(), To: to.UTC(), AsOf: a.now(), Territory: territory})
	if err != nil {
		a.fail(w, r, "kpis", err)
		return
	}
	httpx.JSON(w, http.StatusOK, k)
}
