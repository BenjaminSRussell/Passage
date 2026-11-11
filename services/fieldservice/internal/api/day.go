package api

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg" // registers the decoder DecodeConfig reads
	_ "image/png"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/store"
)

func (a *API) registerDay(r chi.Router) {
	r.With(authz.RequireDuty(dutyTechnician)).Get("/v1/service/technicians/{id}/day", a.technicianDay)
	r.With(authz.RequireDuty(dutyTechnician)).Put("/v1/service/work-orders/{id}/captures/{captureId}", a.putCapture)
	r.With(authz.RequireDuty(dutyInquire)).Get("/v1/service/work-orders/{id}/captures/{captureId}", a.getCapture)
	r.With(authz.RequireDuty(dutyWorkOrder)).Put("/v1/service/work-orders/{id}/checklist", a.putChecklist)
}

// technicianDay returns the signed-in technician's route ("me") or one technician's route for a
// local date (default: today in the technician's time zone).
func (a *API) technicianDay(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if id != "me" && !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeTechnicianUnknown, "technician not found"))
		return
	}
	date := r.URL.Query().Get("date")
	if date != "" {
		if _, ok := parseDate(date); !ok {
			validationFailed(w, r, map[string]string{"date": "must be a date (YYYY-MM-DD)"})
			return
		}
	}
	day, err := a.store.TechnicianDay(r.Context(), id, date, a.now())
	if err != nil {
		a.fail(w, r, "technician day", err)
		return
	}
	httpx.JSON(w, http.StatusOK, day)
}

// putCapture stores a photo or signature image: the request body is the image, and the query
// names the kind, the technician and when and where it was taken.
func (a *API) putCapture(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", store.CodeWOUnknown, "service work order not found")
	if !ok {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	captureID := chi.URLParam(r, "captureId")
	uuidField(fields, "captureId", captureID)
	technicianID := q.Get("technicianId")
	uuidField(fields, "technicianId", technicianID)
	kind := domain.CaptureKind(q.Get("kind"))
	if kind != domain.CapturePhoto && kind != domain.CaptureSignature {
		fields["kind"] = "must be PHOTO or SIGNATURE"
	}
	capturedAt, err := time.Parse(time.RFC3339, q.Get("capturedAt"))
	if err != nil {
		fields["capturedAt"] = "must be an RFC 3339 instant"
	} else if capturedAt.After(a.now().Add(maxClockSkew)) {
		fields["capturedAt"] = "must not be ahead of the server clock"
	}
	lat, lon := optionalFloat(fields, q, "lat"), optionalFloat(fields, q, "lon")
	if (lat == nil) != (lon == nil) || (lat != nil && !validPosition(*lat, *lon)) {
		fields["lat"] = "lat and lon go together: latitude -90 to 90, longitude -180 to 180"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, domain.MaxCaptureBytes+1))
	if err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusRequestEntityTooLarge, domain.CodeCaptureInvalid, fmt.Sprintf("the image must be at most %d MB", domain.MaxCaptureBytes>>20)))
		return
	}
	// The type comes from the bytes, not the header: a PNG sent as image/jpeg is still a PNG.
	cfg, format, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeCaptureInvalid, "the body is not a PNG or JPEG image"))
		return
	}
	contentType := "image/" + format
	if err := domain.CheckImage(kind, contentType, cfg.Width, cfg.Height, len(body)); err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeCaptureInvalid, err.Error()))
		return
	}
	c, err := a.store.PutCapture(r.Context(), store.CaptureInput{ID: strings.ToLower(captureID), WorkOrderID: id, TechnicianID: technicianID, Kind: kind,
		ContentType: contentType, Width: cfg.Width, Height: cfg.Height, Data: body, CapturedAt: capturedAt.UTC(), Latitude: lat, Longitude: lon})
	if err != nil {
		a.fail(w, r, "store capture", err)
		return
	}
	status := http.StatusCreated
	if c.Duplicate {
		status = http.StatusOK
	}
	httpx.JSON(w, status, c)
}

func optionalFloat(fields map[string]string, q map[string][]string, name string) *float64 {
	raw := ""
	if v := q[name]; len(v) > 0 {
		raw = v[0]
	}
	if raw == "" {
		return nil
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		fields[name] = "must be a number"
		return nil
	}
	return &f
}

// getCapture serves a stored image with its own content type.
func (a *API) getCapture(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", store.CodeWOUnknown, "service work order not found")
	if !ok {
		return
	}
	captureID, ok := pathID(w, r, "captureId", domain.CodeCaptureUnknown, "capture not found")
	if !ok {
		return
	}
	c, data, err := a.store.GetCapture(r.Context(), id, strings.ToLower(captureID))
	if err != nil {
		a.fail(w, r, "get capture", err)
		return
	}
	w.Header().Set("Content-Type", c.ContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

type checklistRequest struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	Required bool   `json:"required"`
}

// checklistOf validates checklist items and gives each a UUID when it has none.
func checklistOf(fields map[string]string, in []checklistRequest) []domain.ChecklistItem {
	if len(in) > domain.MaxChecklistItems {
		fields["checklist"] = fmt.Sprintf("must hold at most %d items", domain.MaxChecklistItems)
		return nil
	}
	out := make([]domain.ChecklistItem, 0, len(in))
	seen := map[string]bool{}
	for i, it := range in {
		key := fmt.Sprintf("checklist[%d]", i)
		id := strings.ToLower(it.ID)
		if id == "" {
			id = ids.New()
		}
		text := strings.TrimSpace(it.Text)
		switch {
		case !ids.Valid(id) || seen[id]:
			fields[key] = "id must be a distinct UUID"
		case len([]rune(text)) < 1 || len([]rune(text)) > 200:
			fields[key] = "text must be 1 to 200 characters"
		}
		seen[id] = true
		out = append(out, domain.ChecklistItem{ID: id, Text: text, Required: it.Required})
	}
	return out
}

type checklistBody struct {
	Items []checklistRequest `json:"items"`
}

// putChecklist replaces the checklist of a work order that is not completed.
func (a *API) putChecklist(w http.ResponseWriter, r *http.Request) {
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
	var req checklistBody
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	items := checklistOf(fields, req.Items)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	wo, err := a.store.SetChecklist(r.Context(), id, version, items)
	if err != nil {
		a.fail(w, r, "set checklist", err)
		return
	}
	setETag(w, wo.Version)
	httpx.JSON(w, http.StatusOK, wo)
}
