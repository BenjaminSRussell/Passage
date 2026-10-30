package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

// Workbench defaults: WAVE_MAX_LINES and WAVE_MAX_ORDERS (section 5.28.13).
const (
	defaultWaveMaxLines  = 200
	defaultWaveMaxOrders = 50
)

var (
	providerRE     = regexp.MustCompile(`^[a-z0-9-]{2,32}$`)
	trackingStatus = map[string]bool{"QUOTED": true, "LABELLED": true, "MANIFESTED": true, "IN_TRANSIT": true, "DELIVERED": true, "EXCEPTION": true}
)

// routesWorkbench mounts shipment cancellation (BR-28.6), the picking
// workbench and waves (FR-28.13, BR-28.14), carrier tracking (FR-28.10) and the
// GS1-128 container label (FR-28.3).
func (a *API) routesWorkbench(r chi.Router) {
	r.With(authz.RequireDuty("WarehouseRelease")).Post("/v1/fulfillment/shipments/{id}:cancel", a.cancelShipment)
	r.With(authz.RequireDuty("WarehouseRelease")).Post("/v1/fulfillment/shipments/{id}:close-remainder", a.closeRemainder)
	r.With(authz.RequireDuty("WarehousePack")).Post("/v1/fulfillment/shipments/{id}/containers/{cid}:unpack", a.unpackContainer)
	r.With(authz.RequireDuty("WarehouseRelease")).Post("/v1/fulfillment/waves:preview", a.previewWaves)
	r.With(authz.RequireDuty("WarehouseRelease")).Post("/v1/fulfillment/waves", a.createWaves)
	r.With(authz.RequireDuty("WarehouseView")).Get("/v1/fulfillment/waves", a.listWaves)
	r.With(authz.RequireDuty("WarehouseView")).Get("/v1/fulfillment/waves/{id}", a.getWave)
	r.With(authz.RequireDuty("WarehousePick")).Post("/v1/fulfillment/waves/{id}:confirm-pick", a.confirmWavePick)
	r.With(authz.RequireDuty("WarehouseView")).Get("/v1/fulfillment/shipments/{id}/tracking", a.listTracking)
	r.With(authz.RequireDuty("WarehouseShip")).Post("/v1/fulfillment/shipments/{id}/tracking", a.recordTracking)
	r.With(authz.RequireDuty("WarehouseView")).Get("/v1/fulfillment/shipments/{id}/containers/{cid}/label", a.containerLabel)
}

// ---- DTOs ----------------------------------------------------------------------

type waveRequest struct {
	WarehouseID string `json:"warehouseId"`
	CustomerID  string `json:"customerId"`
	Carrier     string `json:"carrier"`
	Zone        string `json:"zone"`
	ShipFrom    string `json:"shipFrom"`
	ShipTo      string `json:"shipTo"`
	GroupBy     string `json:"groupBy"`
	MaxLines    int    `json:"maxLines"`
	MaxOrders   int    `json:"maxOrders"`
}

// wavesResponse answers a preview (waves are plans) and a create (waves were
// created); message is the FUL-0016 summary.
type wavesResponse[T any] struct {
	WarehouseID string `json:"warehouseId"`
	GroupBy     string `json:"groupBy"`
	WaveCount   int    `json:"waveCount"`
	LineCount   int    `json:"lineCount"`
	Message     string `json:"message"`
	Waves       []T    `json:"waves"`
}

type trackingRequest struct {
	Provider       string `json:"provider"`
	EventKey       string `json:"eventKey"`
	TrackingNumber string `json:"trackingNumber"`
	Status         string `json:"status"`
	Description    string `json:"description"`
	EventTime      string `json:"eventTime"`
}

type trackingResponse struct {
	Created bool `json:"created"`
	store.TrackingEvent
}

// ---- helpers -------------------------------------------------------------------

// decodeQuery reads the JSON body of a read-only POST (the workbench preview),
// which takes no Idempotency-Key. Unknown fields are refused.
func decodeQuery(r *http.Request, v any) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return httpx.NewProblem(http.StatusBadRequest, codeMalformed, "a JSON body is required")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return httpx.NewProblem(http.StatusBadRequest, codeMalformed, "malformed JSON body: "+err.Error())
	}
	return nil
}

// workbenchRequest validates the workbench filter, group key and limits (FR-28.13).
func workbenchRequest(in waveRequest) (store.WaveRequest, error) {
	if !ids.Valid(in.WarehouseID) {
		return store.WaveRequest{}, invalid("warehouseId", "must be a UUID")
	}
	if in.CustomerID != "" && !ids.Valid(in.CustomerID) {
		return store.WaveRequest{}, invalid("customerId", "must be a UUID when given")
	}
	if in.Carrier != "" && !logisticsCode.MatchString(in.Carrier) {
		return store.WaveRequest{}, invalid("carrier", "must be 1 to 32 letters, digits, '.', '_' or '-' when given")
	}
	if in.Zone != "" && !logisticsCode.MatchString(in.Zone) {
		return store.WaveRequest{}, invalid("zone", "must be 1 to 32 letters, digits, '.', '_' or '-' when given")
	}
	group := in.GroupBy
	if group == "" {
		group = domain.GroupCustomer
	}
	if _, err := domain.GroupKey(domain.WaveCandidate{}, group); err != nil {
		return store.WaveRequest{}, invalid("groupBy", "must be CUSTOMER, CARRIER, ZONE or PRODUCT")
	}
	out := store.WaveRequest{WarehouseID: in.WarehouseID, CustomerID: in.CustomerID, Carrier: in.Carrier, Zone: in.Zone, GroupBy: group,
		Limits: domain.WaveLimits{MaxLines: defaultWaveMaxLines, MaxOrders: defaultWaveMaxOrders}}
	if in.ShipFrom != "" {
		t, err := time.Parse(dateLayout, in.ShipFrom)
		if err != nil {
			return store.WaveRequest{}, invalid("shipFrom", "must be a date as YYYY-MM-DD")
		}
		out.ShipFrom = &t
	}
	if in.ShipTo != "" {
		t, err := time.Parse(dateLayout, in.ShipTo)
		if err != nil {
			return store.WaveRequest{}, invalid("shipTo", "must be a date as YYYY-MM-DD")
		}
		out.ShipTo = &t
	}
	if out.ShipFrom != nil && out.ShipTo != nil && out.ShipFrom.After(*out.ShipTo) {
		return store.WaveRequest{}, invalid("shipTo", "must not be before shipFrom")
	}
	if in.MaxLines != 0 {
		if in.MaxLines < 1 || in.MaxLines > 2000 {
			return store.WaveRequest{}, invalid("maxLines", "must be 1 to 2000")
		}
		out.Limits.MaxLines = in.MaxLines
	}
	if in.MaxOrders != 0 {
		if in.MaxOrders < 1 || in.MaxOrders > 500 {
			return store.WaveRequest{}, invalid("maxOrders", "must be 1 to 500")
		}
		out.Limits.MaxOrders = in.MaxOrders
	}
	return out, nil
}

func waveTotals[T any](req store.WaveRequest, waves []T, lines int) wavesResponse[T] {
	return wavesResponse[T]{WarehouseID: req.WarehouseID, GroupBy: req.GroupBy, WaveCount: len(waves), LineCount: lines, Waves: waves}
}

// ---- handlers ------------------------------------------------------------------

func (a *API) cancelShipment(w http.ResponseWriter, r *http.Request) {
	id, version, ok := a.shipmentCommand(w, r)
	if !ok {
		return
	}
	_, idem, err := command(r, nil)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	sh, err := a.store.CancelShipment(r.Context(), id, version, idem)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	etag(w, sh.Version)
	httpx.JSON(w, http.StatusOK, sh)
}

func (a *API) closeRemainder(w http.ResponseWriter, r *http.Request) {
	id, version, ok := a.shipmentCommand(w, r)
	if !ok {
		return
	}
	_, idem, err := command(r, nil)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	sh, err := a.store.CloseRemainder(r.Context(), id, version, idem)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	etag(w, sh.Version)
	httpx.JSON(w, http.StatusOK, sh)
}

func (a *API) unpackContainer(w http.ResponseWriter, r *http.Request) {
	id, version, ok := a.shipmentCommand(w, r)
	if !ok {
		return
	}
	cid := chi.URLParam(r, "cid")
	if !ids.Valid(cid) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeContainerNotFound, "container not found on this shipment"))
		return
	}
	_, idem, err := command(r, nil)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	sh, err := a.store.UnpackContainer(r.Context(), id, version, cid, idem)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	etag(w, sh.Version)
	httpx.JSON(w, http.StatusOK, sh)
}

func (a *API) previewWaves(w http.ResponseWriter, r *http.Request) {
	var in waveRequest
	if err := decodeQuery(r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req, err := workbenchRequest(in)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	plans, err := a.store.PreviewWaves(r.Context(), req)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	lines := 0
	for _, p := range plans {
		lines += p.LineCount
	}
	out := waveTotals(req, plans, lines)
	out.Message = "Preview: " + plural(len(plans), "wave") + " from " + plural(lines, "line")
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) createWaves(w http.ResponseWriter, r *http.Request) {
	var in waveRequest
	_, idem, err := command(r, &in)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req, err := workbenchRequest(in)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	waves, err := a.store.CreateWaves(r.Context(), req, idem)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	lines := 0
	for _, wv := range waves {
		lines += wv.LineCount
	}
	out := waveTotals(req, waves, lines)
	out.Message = "FUL-0016: Workbench created " + plural(len(waves), "wave") + " from " + plural(lines, "line")
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) listWaves(w http.ResponseWriter, r *http.Request) {
	f := store.WaveFilter{WarehouseID: r.URL.Query().Get("warehouseId")}
	if f.WarehouseID != "" && !ids.Valid(f.WarehouseID) {
		fail(w, r, "warehouseId", "must be a UUID")
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	f.Limit = limit
	items, err := a.store.ListWaves(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.WaveSummary]{Items: items})
}

func (a *API) getWave(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeWaveNotFound, "wave not found"))
		return
	}
	wave, err := a.store.GetWave(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, wave)
}

func (a *API) listTracking(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeShipmentNotFound, "shipment not found"))
		return
	}
	events, err := a.store.ListTracking(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.TrackingEvent]{Items: events})
}

// recordTracking stores one carrier tracking event: from carrier-gateway after
// it verified the provider webhook, or a manual tracking entry (BR-28.9). The
// provider event key is the idempotency key, so no Idempotency-Key header is
// needed: a redelivery answers 200 with created = false.
func (a *API) recordTracking(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeShipmentNotFound, "shipment not found"))
		return
	}
	var in trackingRequest
	if err := decodeQuery(r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !providerRE.MatchString(in.Provider) {
		fail(w, r, "provider", "must be 2 to 32 lowercase letters, digits or hyphens")
		return
	}
	if strings.TrimSpace(in.EventKey) == "" || len(in.EventKey) > 128 {
		fail(w, r, "eventKey", "is required and must be at most 128 characters")
		return
	}
	if strings.TrimSpace(in.TrackingNumber) == "" || len(in.TrackingNumber) > 64 {
		fail(w, r, "trackingNumber", "is required and must be at most 64 characters")
		return
	}
	if !trackingStatus[in.Status] {
		fail(w, r, "status", "must be QUOTED, LABELLED, MANIFESTED, IN_TRANSIT, DELIVERED or EXCEPTION")
		return
	}
	if len(in.Description) > 500 {
		fail(w, r, "description", "must be at most 500 characters")
		return
	}
	at, err := time.Parse(time.RFC3339, in.EventTime)
	if err != nil {
		fail(w, r, "eventTime", "must be an RFC 3339 timestamp")
		return
	}
	ev := store.TrackingEvent{Provider: in.Provider, EventKey: in.EventKey, TrackingNumber: strings.TrimSpace(in.TrackingNumber),
		Status: in.Status, Description: in.Description, EventTime: at.UTC()}
	stored, created, err := a.store.RecordTracking(r.Context(), id, ev)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, trackingResponse{Created: created, TrackingEvent: stored})
}

// containerLabel renders the GS1-128 SSCC label of a container as ZPL (FR-28.3,
// BR-28.5, section 5.28.15). The SSCC is validated before it is printed.
func (a *API) containerLabel(w http.ResponseWriter, r *http.Request) {
	id, cid := chi.URLParam(r, "id"), chi.URLParam(r, "cid")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeShipmentNotFound, "shipment not found"))
		return
	}
	if !ids.Valid(cid) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeContainerNotFound, "container not found on this shipment"))
		return
	}
	sh, err := a.store.GetShipment(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	for _, c := range sh.Containers {
		if c.ID != cid {
			continue
		}
		zpl, err := domain.ContainerLabelZPL(c.SSCC, sh.ShipmentNumber, c.ContainerType)
		if err != nil {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeSSCCInvalid, "SSCC "+c.SSCC+" failed its check: "+err.Error()))
			return
		}
		w.Header().Set("Content-Type", "application/zpl; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+c.SSCC+`.zpl"`)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, zpl)
		return
	}
	httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeContainerNotFound, "container not found on this shipment"))
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

type confirmWavePickRequest struct {
	Picks []pickDTO `json:"picks"`
	// PickRest picks every other unconfirmed line of the wave in full.
	PickRest bool `json:"pickRest"`
}

// confirmWavePick answers POST /v1/fulfillment/waves/{id}:confirm-pick: the
// picks of a whole wave in one command, optionally picking the rest in full.
func (a *API) confirmWavePick(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeWaveNotFound, "wave not found"))
		return
	}
	var req confirmWavePickRequest
	_, idem, err := command(r, &req)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if len(req.Picks) == 0 && !req.PickRest {
		fail(w, r, "picks", "at least one pick is required unless pickRest is true")
		return
	}
	picks, ok := pickInputs(w, r, req.Picks)
	if !ok {
		return
	}
	res, err := a.store.ConfirmWavePick(r.Context(), id, picks, req.PickRest, idem)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}
