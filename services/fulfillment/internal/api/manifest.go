package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

// routesManifest mounts the carrier manifest, the end-of-day close-out of the
// parcels a carrier collects from a warehouse (FR-28.6). Closing and cancelling
// are shipping acts (fulfillment.warehouse.ship, section 5.28.5 WarehouseShip).
func (a *API) routesManifest(r chi.Router) {
	r.With(authz.RequireDuty("fulfillment.warehouse.view")).Get("/v1/fulfillment/manifest-candidates", a.listManifestCandidates)
	r.With(authz.RequireDuty("fulfillment.warehouse.ship")).Post("/v1/fulfillment/manifests", a.closeManifest)
	r.With(authz.RequireDuty("fulfillment.warehouse.view")).Get("/v1/fulfillment/manifests", a.listManifests)
	r.With(authz.RequireDuty("fulfillment.warehouse.view")).Get("/v1/fulfillment/manifests/{id}", a.getManifest)
	r.With(authz.RequireDuty("fulfillment.warehouse.ship")).Post("/v1/fulfillment/manifests/{id}:cancel", a.cancelManifest)
}

const (
	maxProviderManifestID = 128
	// manifestDateWindow is how far from today a pickup date may be, in days.
	manifestDateWindow = 7
)

type manifestRequest struct {
	WarehouseID        string `json:"warehouseId"`
	LegalEntityID      string `json:"legalEntityId"`
	Carrier            string `json:"carrier"`
	PickupDate         string `json:"pickupDate"`
	ProviderManifestID string `json:"providerManifestId"`
}

func (a *API) listManifestCandidates(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.ManifestCandidateFilter{WarehouseID: q.Get("warehouseId"), LegalEntityID: q.Get("legalEntityId"), Carrier: q.Get("carrier")}
	if f.WarehouseID != "" && !ids.Valid(f.WarehouseID) {
		fail(w, r, "warehouseId", "must be a UUID")
		return
	}
	if f.LegalEntityID != "" && !ids.Valid(f.LegalEntityID) {
		fail(w, r, "legalEntityId", "must be a UUID")
		return
	}
	if f.Carrier != "" && !providerRE.MatchString(f.Carrier) {
		fail(w, r, "carrier", "must be 2 to 32 lowercase letters, digits or hyphens")
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	f.Limit = limit
	items, err := a.store.ManifestCandidates(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.ManifestCandidate]{Items: items})
}

func (a *API) closeManifest(w http.ResponseWriter, r *http.Request) {
	var req manifestRequest
	_, idem, err := command(r, &req)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !ids.Valid(req.WarehouseID) {
		fail(w, r, "warehouseId", "must be a UUID")
		return
	}
	if !ids.Valid(req.LegalEntityID) {
		fail(w, r, "legalEntityId", "must be the UUID of the legal entity the parcels' shipments belong to")
		return
	}
	if !providerRE.MatchString(req.Carrier) {
		fail(w, r, "carrier", "must be 2 to 32 lowercase letters, digits or hyphens")
		return
	}
	pickup, err := time.Parse(dateLayout, req.PickupDate)
	if err != nil {
		fail(w, r, "pickupDate", "is required, as a date YYYY-MM-DD")
		return
	}
	today := a.now().Truncate(24 * time.Hour)
	if pickup.Before(today.AddDate(0, 0, -manifestDateWindow)) || pickup.After(today.AddDate(0, 0, manifestDateWindow)) {
		fail(w, r, "pickupDate", "must be within 7 days of today")
		return
	}
	ref := strings.TrimSpace(req.ProviderManifestID)
	if utf8.RuneCountInString(ref) > maxProviderManifestID {
		fail(w, r, "providerManifestId", "must be at most 128 characters")
		return
	}
	m, err := a.store.CloseManifest(r.Context(), store.ManifestInput{WarehouseID: req.WarehouseID, LegalEntityID: req.LegalEntityID, Carrier: req.Carrier,
		PickupDate: pickup, ProviderManifestID: ref}, idem)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	etag(w, m.Version)
	httpx.JSON(w, http.StatusCreated, m)
}

func (a *API) listManifests(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.ManifestFilter{WarehouseID: q.Get("warehouseId"), Carrier: q.Get("carrier"), Status: q.Get("status")}
	if f.WarehouseID != "" && !ids.Valid(f.WarehouseID) {
		fail(w, r, "warehouseId", "must be a UUID")
		return
	}
	if f.Carrier != "" && !providerRE.MatchString(f.Carrier) {
		fail(w, r, "carrier", "must be 2 to 32 lowercase letters, digits or hyphens")
		return
	}
	if f.Status != "" && f.Status != store.ManifestClosed && f.Status != store.ManifestCancelled {
		fail(w, r, "status", "must be CLOSED or CANCELLED")
		return
	}
	for name, dst := range map[string]**time.Time{"pickupFrom": &f.PickupFrom, "pickupTo": &f.PickupTo} {
		if v := q.Get(name); v != "" {
			t, err := time.Parse(dateLayout, v)
			if err != nil {
				fail(w, r, name, "must be a date as YYYY-MM-DD")
				return
			}
			*dst = &t
		}
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	f.Limit = limit
	items, err := a.store.ListManifests(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.Manifest]{Items: items})
}

func manifestID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeManifestNotFound, "manifest not found"))
		return "", false
	}
	return id, true
}

func (a *API) getManifest(w http.ResponseWriter, r *http.Request) {
	id, ok := manifestID(w, r)
	if !ok {
		return
	}
	m, err := a.store.GetManifest(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	etag(w, m.Version)
	httpx.JSON(w, http.StatusOK, m)
}

func (a *API) cancelManifest(w http.ResponseWriter, r *http.Request) {
	id, ok := manifestID(w, r)
	if !ok {
		return
	}
	raw := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`)
	if raw == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusPreconditionRequired, CodePreconditionIf, "If-Match with the manifest version is required"))
		return
	}
	version, err := strconv.Atoi(raw)
	if err != nil || version < 1 {
		fail(w, r, "If-Match", "must be the manifest version as an integer")
		return
	}
	_, idem, err := command(r, nil)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	m, err := a.store.CancelManifest(r.Context(), id, version, idem)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	etag(w, m.Version)
	httpx.JSON(w, http.StatusOK, m)
}
