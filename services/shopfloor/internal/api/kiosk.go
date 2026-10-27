package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

// kioskIdleTimeoutMinutes is the default of the site parameter "Kiosk idle
// timeout min" (FR-25.1); site settings are not persisted yet.
const kioskIdleTimeoutMinutes = 10

type badgeRequest struct {
	BadgeID string `json:"badgeId"`
}

// kioskSessionResponse names the worker the kiosk now acts for. The BFF holds
// the session and logs it out after IdleTimeoutMinutes without input.
type kioskSessionResponse struct {
	store.KioskSession
	IdleTimeoutMinutes int `json:"idleTimeoutMinutes"`
}

// registerKiosk mounts kiosk login and the "Who is clocked in" inquiry.
func (a *API) registerKiosk(r chi.Router) {
	r.With(authz.RequireDuty(DutyKioskLogin)).Post("/api/v1/shopfloor/sessions:badge", a.badgeLogin)
	r.With(authz.RequireDuty(DutyView)).Get("/api/v1/shopfloor/clocked-in", a.clockedIn)
}

// badgeLogin resolves a scanned badge (GS1 barcode or NFC payload) to an active
// worker (FR-25.1). It changes nothing, so it needs no Idempotency-Key. The
// badge is a personal identifier and is never logged.
func (a *API) badgeLogin(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req badgeRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	// Same rule as the worker's badgeId: 1 to 64 characters, here without
	// surrounding blanks a scanner may add.
	badge := strings.TrimSpace(req.BadgeID)
	if n := len(badge); n < 1 || n > 64 {
		validationFailed(w, r, map[string]string{"badgeId": "must be 1 to 64 characters"})
		return
	}
	sess, err := a.store.BadgeLogin(r.Context(), badge)
	var p *httpx.Problem
	if errors.As(err, &p) && p.Code == store.CodeBadgeUnknown {
		a.badgeRejected.Add(1)
	}
	if err != nil {
		a.fail(w, r, "badge login", err)
		return
	}
	httpx.JSON(w, http.StatusOK, kioskSessionResponse{KioskSession: sess, IdleTimeoutMinutes: kioskIdleTimeoutMinutes})
}

// clockedIn is the "Who is clocked in" inquiry of the supervisor board (FR-25.14).
func (a *API) clockedIn(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, ok := limitParam(w, r, 500, 1000)
	if !ok {
		return
	}
	items, err := a.store.ClockedIn(r.Context(), limit)
	if err != nil {
		a.fail(w, r, "list clocked-in workers", err)
		return
	}
	httpx.JSON(w, http.StatusOK, itemsResponse[store.ClockedWorker]{Items: items})
}
