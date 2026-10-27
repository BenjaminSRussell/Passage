package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/transport/internal/store"
)

const (
	maxRef      = 64
	maxLocation = 100
	maxStopNo   = 999
)

type trackingRequest struct {
	LoadID     string `json:"loadId"`
	StopNo     int    `json:"stopNo"`
	Code       string `json:"code"`
	OccurredAt string `json:"occurredAt"`
	Location   string `json:"location"`
	Source     string `json:"source"`
	Ref        string `json:"ref"`
}

// loadTrackingResponse is the tracking timeline of one load: the events in
// occurred_at order and the stop states projected from them (BR-34.9).
type loadTrackingResponse struct {
	LoadID string                 `json:"loadId"`
	Events []domain.TrackingEvent `json:"events"`
	Stops  []domain.StopState     `json:"stops"`
}

func (a *API) ingestTracking(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req trackingRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	ev := domain.TrackingEvent{
		LoadID:   req.LoadID,
		StopNo:   req.StopNo,
		Code:     strings.ToUpper(strings.TrimSpace(req.Code)),
		Location: strings.TrimSpace(req.Location),
		Source:   req.Source,
		Ref:      strings.TrimSpace(req.Ref),
	}
	fields := map[string]string{}
	if !uuidRe.MatchString(ev.LoadID) {
		fields["loadId"] = "must be a lower-case UUID"
	}
	if ev.StopNo < 1 || ev.StopNo > maxStopNo {
		fields["stopNo"] = "must be 1 to 999"
	}
	if !domain.IsTrackingCode(ev.Code) {
		fields["code"] = "must be one of " + strings.Join(domain.TrackingCodes, ", ")
	}
	if !domain.TrackingSources[ev.Source] {
		fields["source"] = "must be EDI214, API, TELEMATICS or DRIVER"
	}
	if ev.Ref == "" || len(ev.Ref) > maxRef {
		fields["ref"] = "is required, up to 64 characters; source and ref are the idempotency key"
	}
	if len(ev.Location) > maxLocation {
		fields["location"] = "must be at most 100 characters"
	}
	occurred, err := time.Parse(time.RFC3339, req.OccurredAt)
	if err != nil {
		fields["occurredAt"] = "must be an RFC 3339 timestamp with a zone"
	} else if occurred.After(a.now().Add(24 * time.Hour)) {
		fields["occurredAt"] = "must not be more than a day in the future"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	// Postgres keeps microseconds; truncating here makes a redelivery compare equal.
	ev.OccurredAt = occurred.UTC().Truncate(time.Microsecond)
	saved, inserted, err := a.store.RecordTrackingEvent(r.Context(), ev)
	if err != nil {
		a.fail(w, r, "record tracking event failed", err)
		return
	}
	if !inserted {
		w.Header().Set("Idempotent-Replayed", "true")
		httpx.JSON(w, http.StatusOK, saved)
		return
	}
	a.trackingIngested.Add(1)
	httpx.JSON(w, http.StatusCreated, saved)
}

func (a *API) listTracking(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	f := store.TrackingFilter{LoadID: r.URL.Query().Get("loadId")}
	fields := map[string]string{}
	checkOptionalID(fields, "loadId", f.LoadID)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	events, err := a.store.ListTrackingEvents(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list tracking events failed", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": events})
}

func (a *API) loadTracking(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	loadID := chi.URLParam(r, "id")
	if !uuidRe.MatchString(loadID) {
		validationFailed(w, r, map[string]string{"id": "must be a lower-case UUID"})
		return
	}
	events, err := a.store.ListTrackingEvents(r.Context(), store.TrackingFilter{LoadID: loadID})
	if err != nil {
		a.fail(w, r, "load tracking timeline failed", err)
		return
	}
	httpx.JSON(w, http.StatusOK, loadTrackingResponse{LoadID: loadID, Events: events, Stops: domain.ProjectStops(events)})
}
