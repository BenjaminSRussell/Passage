package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/eam/internal/domain"
	"github.com/BenjaminSRussell/passage/services/eam/internal/store"
)

// roundIn is the body of POST /rounds (FR-44.14).
type roundIn struct {
	Code   string             `json:"code"`
	Name   string             `json:"name"`
	SiteID string             `json:"siteId"`
	Stops  []domain.RoundStop `json:"stops"`
}

func (h *Handler) createRound(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body roundIn
	if err := decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	switch {
	case !planCode.MatchString(body.Code):
		fail(w, r, invalid("code must match ^[A-Z0-9][A-Z0-9_-]{1,29}$"))
		return
	case body.Name == "" || len([]rune(body.Name)) > 120:
		fail(w, r, invalid("name must be 1-120 characters"))
		return
	case !ids.Valid(body.SiteID):
		fail(w, r, invalid("siteId must be a UUID"))
		return
	}
	for _, st := range body.Stops {
		if !ids.Valid(st.AssetID) {
			fail(w, r, invalid("every stop assetId must be a UUID"))
			return
		}
	}
	// Stop order, job type codes and travel are business rules: the store
	// answers them with EAM-RND-003.
	out, err := h.store.CreateRound(r.Context(), store.Round{Code: body.Code, Name: body.Name, SiteID: body.SiteID,
		Stops: body.Stops}, key)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (h *Handler) listRounds(w http.ResponseWriter, r *http.Request) {
	opt, err := listOptions(r, nil)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := h.store.ListRounds(r.Context(), opt)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) getRound(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := h.store.GetRound(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// createRoundWorkOrder opens a ROUND work order for one extra occurrence of a
// round; ROUND plan lines generate the scheduled ones.
func (h *Handler) createRoundWorkOrder(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body occurrenceIn
	if r.ContentLength != 0 {
		if err := decode(r, &body); err != nil {
			fail(w, r, err)
			return
		}
	}
	if body.Currency == "" {
		body.Currency = "USD"
	}
	if !currencyCode.MatchString(body.Currency) {
		fail(w, r, invalid("currency must be an ISO 4217 code"))
		return
	}
	out, err := h.store.CreateRoundWorkOrder(r.Context(), id, store.RoundWorkOrderInput{Currency: body.Currency, DueAt: body.DueAt}, key)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

// stopOutcomeIn is the body of POST /work-orders/{id}/stops/{seq} (BR-44.9).
type stopOutcomeIn struct {
	Outcome string `json:"outcome"`
	Reason  string `json:"reason"`
}

// recordStop sets one stop of an IN_PROGRESS ROUND work order to DONE or SKIPPED.
func (h *Handler) recordStop(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	seq, err := strconv.Atoi(chi.URLParam(r, "seq"))
	if err != nil || seq < 1 || seq > domain.MaxRoundStops {
		fail(w, r, invalid("stop sequence must be 1-50"))
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body stopOutcomeIn
	if err := decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	if body.Outcome != domain.StopDone && body.Outcome != domain.StopSkipped {
		fail(w, r, invalid("outcome must be DONE or SKIPPED"))
		return
	}
	p, _ := tenant.FromContext(r.Context())
	out, err := h.store.RecordStopOutcome(r.Context(), id, seq, store.StopOutcome{Outcome: body.Outcome, Reason: body.Reason,
		Actor: p.UserID}, key)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// emergencyOverdue lists emergency orders not regularized within the window
// (BR-44.7): hours 1-72, default 24 (8.44.13).
func (h *Handler) emergencyOverdue(w http.ResponseWriter, r *http.Request) {
	window := domain.RegularizationWindow
	if raw := r.URL.Query().Get("hours"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 || v > 72 {
			fail(w, r, invalid("hours must be 1-72"))
			return
		}
		window = time.Duration(v) * time.Hour
	}
	out, err := h.store.EmergencyOverdue(r.Context(), window)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out, "windowHours": int(window / time.Hour)})
}

// planReviews lists the critical assets whose plans are due for review (BR-44.15).
func (h *Handler) planReviews(w http.ResponseWriter, r *http.Request) {
	out, err := h.store.PlanReviews(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

// costByAsset rolls the costs of work orders completed in [from, to) up to each
// asset (FR-44.13), optionally for one asset type.
func (h *Handler) costByAsset(w http.ResponseWriter, r *http.Request) {
	from, to, err := window(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	typ := r.URL.Query().Get("assetType")
	if typ != "" && !assetType.MatchString(typ) {
		fail(w, r, invalid("assetType must match ^[A-Z0-9_]{2,20}$"))
		return
	}
	out, err := h.store.CostByAsset(r.Context(), store.CostFilter{From: from, To: to, AssetType: typ})
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out, "from": from, "to": to})
}

// backlog reports the open work per priority (FR-44.13), optionally on one site.
func (h *Handler) backlog(w http.ResponseWriter, r *http.Request) {
	site := r.URL.Query().Get("site")
	if site != "" && !ids.Valid(site) {
		fail(w, r, invalid("site must be a UUID"))
		return
	}
	out, err := h.store.Backlog(r.Context(), site)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

// window parses the required from and to query parameters (RFC 3339, from before to).
func window(r *http.Request) (time.Time, time.Time, error) {
	q := r.URL.Query()
	from, errFrom := time.Parse(time.RFC3339, q.Get("from"))
	to, errTo := time.Parse(time.RFC3339, q.Get("to"))
	if errFrom != nil || errTo != nil || !from.Before(to) {
		return from, to, invalid("from and to must be RFC 3339 timestamps with from before to")
	}
	return from, to, nil
}
