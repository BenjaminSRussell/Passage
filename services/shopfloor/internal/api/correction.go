package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

// Duties of the supervisor corrections (FR-25.8). Neither is held by operators.
const (
	// DutyTimeCorrect lets a supervisor change the times of a registration with a reason.
	DutyTimeCorrect = "shopfloor.time.correct"
	// DutyPayRevise lets a supervisor revise approved pay with reversal lines.
	DutyPayRevise = "shopfloor.pay.revise"

	maxCorrectionReason = 500
)

type correctionRequest struct {
	StartedAt *time.Time `json:"startedAt"`
	StoppedAt *time.Time `json:"stoppedAt"`
	Reason    string     `json:"reason"`
}

type reviseRequest struct {
	WorkerID string `json:"workerId"`
	From     string `json:"from"`
	To       string `json:"to"`
	TimeZone string `json:"timeZone"`
	Reason   string `json:"reason"`
}

func (a *API) registerCorrections(r chi.Router) {
	r.With(authz.RequireDuty(DutyTimeCorrect)).Post("/v1/shopfloor/registrations/{id}:correct", a.correctRegistration)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/registration-corrections", a.listCorrections)
	r.With(authz.RequireDuty(DutyPayRevise)).Post("/v1/shopfloor/pay:revise", a.revisePay)
}

func authenticatedUser(w http.ResponseWriter, r *http.Request, what string) (string, bool) {
	p, _ := tenant.FromContext(r.Context())
	if p.UserID == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", what+" needs an authenticated user"))
		return "", false
	}
	return p.UserID, true
}

func cleanReason(reason string) (string, string) {
	reason = strings.TrimSpace(reason)
	if n := len([]rune(reason)); n < 1 || n > maxCorrectionReason {
		return reason, fmt.Sprintf("must be 1 to %d characters", maxCorrectionReason)
	}
	return reason, ""
}

// correctRegistration changes the times of a registration with a reason (FR-25.8).
func (a *API) correctRegistration(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "correcting a registration")
	if !ok {
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeRegistrationNotFound, "registration not found"))
		return
	}
	var req correctionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	reason, bad := cleanReason(req.Reason)
	if bad != "" {
		validationFailed(w, r, map[string]string{"reason": bad})
		return
	}
	if req.StartedAt == nil && req.StoppedAt == nil {
		validationFailed(w, r, map[string]string{"startedAt": "startedAt or stoppedAt is required"})
		return
	}
	if a.replayed(w, r, idem) {
		return
	}
	c, err := a.store.CorrectRegistration(r.Context(), id, domain.CorrectionRequest{StartedAt: req.StartedAt, StoppedAt: req.StoppedAt, Reason: reason}, user, idem)
	if err != nil {
		a.fail(w, r, "correct registration", err)
		return
	}
	a.corrections.Add(1)
	httpx.JSON(w, http.StatusOK, c)
}

// listCorrections lists the corrections made in a date range, optionally of one worker.
func (a *API) listCorrections(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	win, ok := queryRange(w, r, false)
	if !ok {
		return
	}
	limit, ok := limitParam(w, r, 200, 1000)
	if !ok {
		return
	}
	items, err := a.store.ListCorrections(r.Context(), win.workerID, win.start, win.end, limit)
	if err != nil {
		a.fail(w, r, "list corrections", err)
		return
	}
	httpx.JSON(w, http.StatusOK, itemsResponse[store.Correction]{Items: items})
}

// revisePay recalculates a range that may hold approved days and applies the
// difference with reversal lines (BR-25.8, AC-25.8).
func (a *API) revisePay(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "revising pay")
	if !ok {
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	var req reviseRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	win, fields := parseRange(payRangeRequest{WorkerID: req.WorkerID, From: req.From, To: req.To, TimeZone: req.TimeZone}, maxRangeDays, true)
	reason, bad := cleanReason(req.Reason)
	if bad != "" {
		if fields == nil {
			fields = map[string]string{}
		}
		fields["reason"] = bad
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if a.replayed(w, r, idem) {
		return
	}
	days, _, err := a.recalculate(r.Context(), win)
	if err != nil {
		a.fail(w, r, "recalculate pay", err)
		return
	}
	out, err := a.store.RevisePay(r.Context(), store.PayCalculation{WorkerID: win.workerID, FromDate: win.fromDate, ToDate: win.toDate, TimeZone: win.zone, Days: days}, reason, user, idem)
	if err != nil {
		a.fail(w, r, "revise pay", err)
		return
	}
	for _, d := range out.Days {
		if d.Changed && d.Status == "approved" {
			a.revisedDays.Add(1)
		}
	}
	httpx.JSON(w, http.StatusOK, out)
}
