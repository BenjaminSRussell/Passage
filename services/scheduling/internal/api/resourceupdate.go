package api

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

type resourcePatchRequest struct {
	Name     *string        `json:"name"`
	TimeZone *string        `json:"timeZone"`
	Shifts   []domain.Shift `json:"shifts"`
	// EfficiencyPct is whole percent from 1 to 500 (BR-23.1).
	EfficiencyPct *int `json:"efficiencyPct"`
	// EngineeringResourceID is a UUID, or "" to remove the mapping.
	EngineeringResourceID *string `json:"engineeringResourceId"`
}

// updateResource changes a resource's name, time zone or weekly calendar and
// lists the booked jobs the new calendar no longer covers (FR-23.1, AC-23.7).
func (a *API) updateResource(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := strings.ToLower(chi.URLParam(r, "id"))
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeResourceNotFound, "resource not found"))
		return
	}
	var req resourcePatchRequest
	body, ok := decodeBody(w, r, &req)
	if !ok {
		return
	}
	idem, ok := requireIdem(w, r, body)
	if !ok {
		return
	}
	version, ok := ifMatchOf(w, r, "resource")
	if !ok {
		return
	}
	idem.Fingerprint = fingerprint(id+"|"+strconv.Itoa(version), body)
	fields := map[string]string{}
	patch := store.ResourcePatch{}
	if req.Name == nil && req.TimeZone == nil && req.Shifts == nil && req.EfficiencyPct == nil && req.EngineeringResourceID == nil {
		fields["body"] = "send at least one of name, timeZone, shifts, efficiencyPct or engineeringResourceId"
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if n := utf8.RuneCountInString(name); n < 1 || n > 120 {
			fields["name"] = "must be 1 to 120 characters"
		}
		patch.Name = &name
	}
	if req.TimeZone != nil {
		tz := *req.TimeZone
		if _, err := time.LoadLocation(tz); err != nil || tz == "" || len(tz) > maxRunTimeZone || tz == "Local" {
			fields["timeZone"] = "must be an IANA time zone name"
		}
		patch.TimeZone = &tz
	}
	if req.Shifts != nil {
		if msg := validShifts(req.Shifts); msg != "" {
			fields["shifts"] = msg
		}
		patch.Shifts = req.Shifts
	}
	if req.EfficiencyPct != nil {
		if *req.EfficiencyPct < 1 || *req.EfficiencyPct > 500 {
			fields["efficiencyPct"] = "must be a whole percent from 1 to 500"
		}
		patch.EfficiencyPct = *req.EfficiencyPct
	}
	if req.EngineeringResourceID != nil {
		eid := strings.ToLower(*req.EngineeringResourceID)
		if eid != "" && !ids.Valid(eid) {
			fields["engineeringResourceId"] = "must be a UUID, or empty to remove the mapping"
		}
		patch.EngineeringResourceID = &eid
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	res, replayed, err := a.store.UpdateResource(r.Context(), id, version, patch, time.Now().UTC(), idem)
	if err != nil {
		slog.Error("update resource failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	w.Header().Set("ETag", etag(res.Version))
	httpx.JSON(w, http.StatusOK, res)
}
