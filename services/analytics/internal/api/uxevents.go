package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/domain"
)

// Duties for UX telemetry (#510). Anyone who uses the web app is given record;
// view is for the people who read the funnels.
const (
	DutyUXRecord = "analytics.ux.record"
	DutyUXView   = "analytics.ux.view"
)

func (a *API) registerUX(r chi.Router) {
	r.With(authz.RequireDuty(DutyUXRecord)).Post("/v1/analytics/ux-events", a.addUXEvents)
	r.Group(func(g chi.Router) {
		g.Use(authz.RequireDuty(DutyUXView))
		g.Get("/v1/analytics/ux-funnels", a.getUXFunnel)
		g.Get("/v1/analytics/ux-signals", a.getUXSignals)
	})
}

// UXEventsRequest is the body of POST /analytics/ux-events.
type UXEventsRequest struct {
	Events []domain.UXEvent `json:"events"`
}

// UXEventsResponse says how many events were stored. Dropped counts search
// events whose query looked personal and was not kept.
type UXEventsResponse struct {
	Accepted int `json:"accepted"`
	Dropped  int `json:"dropped"`
}

func (a *API) addUXEvents(w http.ResponseWriter, r *http.Request) {
	var req UXEventsRequest
	if err := httpx.Decode(r, &req); err != nil {
		refuse(w, r, "request body is not valid JSON", nil)
		return
	}
	events, dropped, verr := domain.PrepareUXEvents(req.Events, a.now())
	if verr != nil {
		fail(w, r, verr)
		return
	}
	if len(events) > 0 {
		if err := a.store.AddUXEvents(r.Context(), events); err != nil {
			fail(w, r, err)
			return
		}
	}
	httpx.JSON(w, http.StatusAccepted, UXEventsResponse{Accepted: len(events), Dropped: dropped})
}

func (a *API) getUXFunnel(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	flow := q.Get("flow")
	if _, ok := domain.Funnels[flow]; !ok {
		refuse(w, r, "request failed validation", map[string]string{"flow": "one of " + strings.Join(domain.FunnelNames(), ", ")})
		return
	}
	from, to, verr := domain.ValidateUXPeriod(q.Get("from"), q.Get("to"), q.Get("release"))
	if verr != nil {
		fail(w, r, verr)
		return
	}
	out, err := a.store.GetFunnel(r.Context(), flow, q.Get("release"), from, to)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) getUXSignals(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, to, verr := domain.ValidateUXPeriod(q.Get("from"), q.Get("to"), q.Get("release"))
	if verr != nil {
		fail(w, r, verr)
		return
	}
	out, err := a.store.GetUXSignals(r.Context(), q.Get("release"), from, to)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
