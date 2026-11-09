package api

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/mobile/internal/flows"
	"github.com/BenjaminSRussell/passage/services/mobile/internal/gs1"
	"github.com/BenjaminSRussell/passage/services/mobile/internal/store"
)

func userOf(r *http.Request) string {
	p, _ := tenant.FromContext(r.Context())
	return p.UserID
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func (a *API) registerDevice(w http.ResponseWriter, r *http.Request) {
	var d store.Device
	if err := httpx.Decode(r, &d); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if d.DeviceID == "" || len(d.DeviceID) > maxDeviceIDLen || strings.Contains(d.DeviceID, ":") {
		validationFailed(w, r, map[string]string{"deviceId": "is required, at most 80 characters, without ':'"})
		return
	}
	if d.ScannerProfile == "" {
		d.ScannerProfile = "ZEBRA_DATAWEDGE"
	}
	if d.ScannerProfile != "ZEBRA_DATAWEDGE" && d.ScannerProfile != "HONEYWELL_DC" && d.ScannerProfile != "KEYBOARD_WEDGE" {
		validationFailed(w, r, map[string]string{"scannerProfile": "must be ZEBRA_DATAWEDGE, HONEYWELL_DC or KEYBOARD_WEDGE"})
		return
	}
	out, err := a.st.RegisterDevice(r.Context(), d)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) listDevices(w http.ResponseWriter, r *http.Request) {
	items, err := a.st.ListDevices(r.Context(), limitOf(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": nonNil(items)})
}

type menuResponse struct {
	ID      string       `json:"id"`
	Name    string       `json:"name"`
	Version int          `json:"version"`
	Flows   []store.Flow `json:"flows"`
}

// getMenu serves the menu of a key for the caller's roles and the requested
// site: the flows published at that moment, so a publish reaches the device on
// its next load. The default key always exists and offers every published flow.
func (a *API) getMenu(w http.ResponseWriter, r *http.Request) {
	ctx, key, site := r.Context(), chi.URLParam(r, "id"), r.URL.Query().Get("warehouseId")
	m, err := a.st.ResolveMenu(ctx, key, site)
	if errors.Is(err, store.ErrNotFound) && key == defaultMenuKey {
		m, err = store.Menu{Key: key, Name: "Default", Version: 1}, nil
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	published, err := a.st.PublishedFor(ctx, site, m.FlowCodes)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, menuResponse{ID: m.Key, Name: m.Name, Version: m.Version, Flows: nonNil(published)})
}

func (a *API) listMenus(w http.ResponseWriter, r *http.Request) {
	items, err := a.st.ListMenus(r.Context(), limitOf(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": nonNil(items)})
}

func (a *API) upsertMenu(w http.ResponseWriter, r *http.Request) {
	var m store.Menu
	fields := map[string]string{}
	idem, err := command(r, &m, fields)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if m.Key == "" {
		fields["key"] = "is required"
	}
	if m.Name == "" || len(m.Name) > 80 {
		fields["name"] = "is required, at most 80 characters"
	}
	if m.WarehouseID != "" {
		uuidField(fields, "warehouseId", m.WarehouseID)
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, replayed, err := a.st.UpsertMenu(r.Context(), idem, m)
	if err != nil {
		fail(w, r, err)
		return
	}
	answer(w, http.StatusOK, replayed, out)
}

func (a *API) saveDraft(w http.ResponseWriter, r *http.Request) {
	var d flows.Draft
	fields := map[string]string{}
	idem, err := command(r, &d, fields)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	for k, v := range flows.ValidShape(d) {
		fields[k] = v
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, replayed, err := a.st.SaveDraft(r.Context(), idem, d)
	if err != nil {
		fail(w, r, err)
		return
	}
	answer(w, http.StatusOK, replayed, out)
}

func (a *API) listFlows(w http.ResponseWriter, r *http.Request) {
	items, err := a.st.ListFlows(r.Context(), r.URL.Query().Get("code"), limitOf(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": nonNil(items)})
}

func (a *API) getFlow(w http.ResponseWriter, r *http.Request) {
	f, err := a.st.GetFlow(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, f)
}

func (a *API) publishFlow(w http.ResponseWriter, r *http.Request) {
	fields := map[string]string{}
	idem, err := command(r, nil, fields)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	version := ifMatch(r, fields, false)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	f, replayed, err := a.st.Publish(r.Context(), idem, chi.URLParam(r, "id"), version)
	if err != nil {
		fail(w, r, err)
		return
	}
	if !replayed {
		a.stats.published.Add(1)
		a.hub.publish(tenantOf(r), flowEvent{FlowID: f.FlowID, Code: f.Code, Version: f.Version})
	}
	answer(w, http.StatusOK, replayed, f)
}

func (a *API) installCore(w http.ResponseWriter, r *http.Request) {
	fields := map[string]string{}
	idem, err := command(r, nil, fields)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, replayed, err := a.st.InstallCore(r.Context(), idem, flows.CoreFlows())
	if err != nil {
		fail(w, r, err)
		return
	}
	answer(w, http.StatusOK, replayed, map[string]any{"items": out})
}

type scanRequest struct {
	Raw                string `json:"raw"`
	RequireSymbologyID bool   `json:"requireSymbologyId"`
	FNC1Substitute     string `json:"fnc1Substitute"`
	Expect             string `json:"expect"`
}

type scanResponse struct {
	gs1.Classified
	Verdict *gs1.Verdict `json:"verdict,omitempty"`
}

func (a *API) parseScan(w http.ResponseWriter, r *http.Request) {
	var req scanRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.Raw == "" || len(req.Raw) > 512 {
		validationFailed(w, r, map[string]string{"raw": "is required, at most 512 characters"})
		return
	}
	if req.Expect != "" && !gs1.ValidClass(gs1.ScanClass(req.Expect)) {
		validationFailed(w, r, map[string]string{"expect": "must be LP, GTIN, LOT, LOCATION or ANY"})
		return
	}
	c := gs1.Classify(req.Raw, gs1.ClassifyOptions{Options: gs1.Options{FNC1Substitute: req.FNC1Substitute, Now: a.now()}, RequireSymbologyID: req.RequireSymbologyID})
	out := scanResponse{Classified: c}
	if req.Expect != "" {
		out.Verdict = gs1.Check(gs1.ScanClass(req.Expect), c)
	}
	a.stats.scans.Add(1)
	if (c.GS1 != nil && !c.GS1.OK) || out.Verdict != nil {
		a.stats.scanErrors.Add(1)
	}
	httpx.JSON(w, http.StatusOK, out)
}

func encodeCursor(t store.OfflineTx) string {
	return base64.RawURLEncoding.EncodeToString([]byte(t.CapturedAt.UTC().Format(time.RFC3339Nano) + "|" + t.IdempotencyKey))
}

func decodeCursor(s string) (time.Time, string, bool) {
	if s == "" {
		return time.Time{}, "", true
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, "", false
	}
	at, key, ok := strings.Cut(string(raw), "|")
	t, perr := time.Parse(time.RFC3339Nano, at)
	return t, key, ok && perr == nil
}
