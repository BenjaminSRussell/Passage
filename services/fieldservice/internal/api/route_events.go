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
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/routefeed"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/store"
)

// CodeRouteEventsOff is the 503 when the service has no route feed: the device keeps polling.
const CodeRouteEventsOff = "SVC-RTE-001"

const routeKeepAlive = 25 * time.Second

// RouteEventsPath reports whether a request path is the route stream, which the process serves outside the router's 30 second
// request timeout (a stream that the timeout cut every 30 seconds would never stay open); mount it with RegisterStreams.
func RouteEventsPath(path string) bool {
	return strings.HasPrefix(path, "/v1/service/technicians/") && strings.HasSuffix(path, "/day/events")
}

// RegisterStreams mounts the long-lived routes. Mount them on a router without a request timeout, with tenant extraction.
func (a *API) RegisterStreams(r chi.Router) {
	r.With(authz.RequireDuty(dutyTechnician)).Get("/v1/service/technicians/{id}/day/events", a.routeEvents)
}

// WithRouteFeed turns on the route stream (GET /v1/service/technicians/{id}/day/events).
func (a *API) WithRouteFeed(f *routefeed.Feed) *API {
	a.feed = f
	return a
}

// routeEvents streams "route" events to a technician's device (server-sent events): one per change of the technician's route,
// after which the device reads its day again. It carries no route data. The stream starts with a "ready" event, sends a comment
// every 25 seconds so proxies keep it open, and ends when the client leaves or the request timeout recycles it; EventSource
// reconnects by itself and the device reads its day on every reconnect.
func (a *API) routeEvents(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if id != "me" && !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeTechnicianUnknown, "technician not found"))
		return
	}
	if a.feed == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeRouteEventsOff, "route updates are not enabled on this service; read the day again from time to time"))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeRouteEventsOff, "this connection cannot stream"))
		return
	}
	technicianID, err := a.store.ViewableTechnician(r.Context(), id)
	if err != nil {
		a.fail(w, r, "route events", err)
		return
	}
	principal, _ := tenant.FromContext(r.Context())
	ch, stop := a.feed.Subscribe(principal.TenantID, technicianID)
	defer stop()
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if _, err := fmt.Fprintf(w, "retry: 5000\nevent: ready\ndata: {\"technicianId\":%q}\n\n", technicianID); err != nil {
		return
	}
	flusher.Flush()
	tick := time.NewTicker(routeKeepAlive)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			if _, err := fmt.Fprintf(w, "event: route\ndata: {\"technicianId\":%q}\n\n", technicianID); err != nil {
				return
			}
			flusher.Flush()
		case <-tick.C:
			if _, err := fmt.Fprint(w, ":\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
