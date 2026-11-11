package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/store"
)

const (
	// dutyTechnician is held by field technicians (PRD 8.45.17); it covers the mobile
	// action log. It is a new duty: seeded by the policies owner after review.
	dutyTechnician = "fieldservice.work-order.execute"

	maxActions   = 200
	maxClockSkew = 5 * time.Minute
)

func (a *API) registerActions(r chi.Router) {
	r.With(authz.RequireDuty(dutyTechnician)).Post("/v1/service/work-orders/{id}/actions", a.recordActions)
	r.With(authz.RequireDuty(dutyInquire)).Get("/v1/service/work-orders/{id}/actions", a.listActions)
}

type actionRequest struct {
	ID           string          `json:"id"`
	Kind         string          `json:"kind"`
	DeviceTime   time.Time       `json:"deviceTime"`
	LineID       string          `json:"lineId"`
	Qty          decimal.Decimal `json:"qty"`
	SignedBy     string          `json:"signedBy"`
	SignatureRef string          `json:"signatureRef"`
	Text         string          `json:"text"`
	// Latitude and Longitude are where the customer signed (SIGNATURE).
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
	// CaptureIDs are the uploaded photos a PHOTO action attaches (1 to 5).
	CaptureIDs []string `json:"captureIds"`
	// ItemID and Done tick or clear a checklist item (CHECKLIST).
	ItemID string `json:"itemId"`
	Done   bool   `json:"done"`
}

type actionsRequest struct {
	TechnicianID string          `json:"technicianId"`
	Actions      []actionRequest `json:"actions"`
}

type syncResponse struct {
	store.SyncResult
	// Message is SVC-SYNC-001 when offline actions were merged or conflicts resolved.
	Message string `json:"message,omitempty"`
}

// recordActions merges a batch of technician actions (BR-45.6). Each action is judged
// on its own: the response says which were applied, superseded by a further state, or
// rejected (with the SVC code), so the device can drop its queue.
func (a *API) recordActions(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", store.CodeWOUnknown, "service work order not found")
	if !ok {
		return
	}
	var req actionsRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	uuidField(fields, "technicianId", req.TechnicianID)
	if len(req.Actions) < 1 || len(req.Actions) > maxActions {
		fields["actions"] = fmt.Sprintf("must hold 1 to %d actions", maxActions)
	}
	now := a.now()
	in := make([]store.ActionInput, 0, len(req.Actions))
	for i, x := range req.Actions {
		key := fmt.Sprintf("actions[%d]", i)
		kind := domain.ActionKind(x.Kind)
		switch {
		case !ids.Valid(x.ID):
			fields[key] = "id must be a client-generated UUID"
		case !kind.Valid():
			fields[key] = "kind must be TRAVEL_START, WORK_START, HOLD, RESUME, PART_USED, SIGNATURE, NOTE, COMPLETE, PHOTO or CHECKLIST"
		case x.DeviceTime.IsZero() || x.DeviceTime.After(now.Add(maxClockSkew)):
			fields[key] = "deviceTime must be an RFC 3339 instant not ahead of the server clock"
		case kind == domain.ActPartUsed && (!ids.Valid(x.LineID) || !x.Qty.IsPositive() || x.Qty.Exponent() < -6):
			fields[key] = "PART_USED needs lineId (a UUID) and a positive qty with at most 6 decimals"
		case kind == domain.ActSignature && (len([]rune(x.SignedBy)) < 1 || len([]rune(x.SignedBy)) > 120 || len(x.SignatureRef) < 1 || len(x.SignatureRef) > maxAddressLength):
			fields[key] = "SIGNATURE needs signedBy (1 to 120 characters) and signatureRef (1 to 500 characters)"
		case kind == domain.ActSignature && (x.Latitude == nil) != (x.Longitude == nil):
			fields[key] = "SIGNATURE takes latitude and longitude together"
		case kind == domain.ActSignature && x.Latitude != nil && !validPosition(*x.Latitude, *x.Longitude):
			fields[key] = "latitude must be -90 to 90 and longitude -180 to 180"
		case kind == domain.ActNote && len([]rune(x.Text)) > maxDescription:
			fields[key] = fmt.Sprintf("text must be at most %d characters", maxDescription)
		case kind == domain.ActPhoto && !validCaptureIDs(x.CaptureIDs):
			fields[key] = fmt.Sprintf("PHOTO needs 1 to %d distinct captureIds (UUIDs of uploaded photos)", domain.MaxPhotosPerAction)
		case kind == domain.ActChecklist && !ids.Valid(x.ItemID):
			fields[key] = "CHECKLIST needs itemId (a UUID) and done"
		}
		captures := make([]string, 0, len(x.CaptureIDs))
		for _, c := range x.CaptureIDs {
			captures = append(captures, strings.ToLower(c))
		}
		in = append(in, store.ActionInput{ID: x.ID, Kind: kind, DeviceTime: x.DeviceTime.UTC(), LineID: x.LineID, Qty: x.Qty,
			SignedBy: x.SignedBy, SignatureRef: x.SignatureRef, Text: x.Text, Latitude: x.Latitude, Longitude: x.Longitude,
			CaptureIDs: captures, ItemID: strings.ToLower(x.ItemID), Done: x.Done})
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	res, err := a.store.RecordActions(r.Context(), id, req.TechnicianID, in, now)
	if err != nil {
		a.fail(w, r, "record work order actions", err)
		return
	}
	out := syncResponse{SyncResult: res}
	if res.Superseded > 0 {
		out.Message = fmt.Sprintf("SVC-SYNC-001: %d offline actions merged; %d status conflicts resolved to %s", res.Applied+res.Superseded, res.Superseded, res.WorkOrder.Status)
	}
	setETag(w, res.WorkOrder.Version)
	httpx.JSON(w, http.StatusOK, out)
}

// listActions returns the action log of a work order in merge order.
func (a *API) listActions(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", store.CodeWOUnknown, "service work order not found")
	if !ok {
		return
	}
	items, err := a.store.ListActions(r.Context(), id)
	if err != nil {
		a.fail(w, r, "list work order actions", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func validPosition(lat, lon float64) bool {
	return lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180
}

func validCaptureIDs(in []string) bool {
	if len(in) < 1 || len(in) > domain.MaxPhotosPerAction {
		return false
	}
	seen := map[string]bool{}
	for _, id := range in {
		id = strings.ToLower(id)
		if !ids.Valid(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}
