// Package api exposes the fulfillment HTTP surface described in api/openapi.yaml.
// Handlers decode and validate, read the principal from the tenant context,
// call the store, and answer with httpx.JSON or httpx.Fail.
package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/carrier"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

// Error codes owned by the API layer (service catalog FUL-*, plus kernel codes).
const (
	CodeValidation     = "FUL-0050"
	CodeIdemMissing    = "FUL-0051"
	CodePreconditionIf = "precondition_required"
	codeMalformed      = "COM-0001"
	headerIdempotency  = "Idempotency-Key"
	maxBodyBytes       = 4 << 20
	dateLayout         = "2006-01-02"
	validationDetail   = "request failed validation"
)

var (
	currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)
	unitCode     = regexp.MustCompile(`^[A-Za-z0-9]{1,8}$`)
	// logisticsCode is a carrier or pick zone code (FR-28.13).
	logisticsCode = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,32}$`)
)

// Options are the release parameters read from configuration (RELEASE_HORIZON_DAYS, FR-28.1).
type Options struct {
	ReleaseHorizonDays int
	RequireFullATP     bool
	// Carrier is the carrier-gateway client (CARRIER_GATEWAY_URL); nil answers the carrier routes 503 FUL-0070.
	Carrier carrier.Gateway
}

// API holds the store and the release parameters.
type API struct {
	store store.Store
	opts  Options
	now   func() time.Time
}

// New returns the API over s.
func New(s store.Store, opts Options) *API {
	return &API{store: s, opts: opts, now: func() time.Time { return time.Now().UTC() }}
}

// SetClock replaces the clock that dates "today" (release horizon, DNI as-of
// default, returnable aging). Tests use it; production keeps UTC wall time.
func (a *API) SetClock(now func() time.Time) { a.now = now }

// Routes mounts every fulfillment endpoint on r. Each route carries the duty
// named by its operation's x-passage-duty.
func (a *API) Routes(r chi.Router) {
	r.With(authz.RequireDuty("WarehouseRelease")).Post("/v1/fulfillment/releases:run", a.runRelease)
	r.With(authz.RequireDuty("WarehouseView")).Get("/v1/fulfillment/shipments", a.listShipments)
	r.With(authz.RequireAnyDuty("WarehouseView", "fulfillment.shipment.read")).Get("/v1/fulfillment/shipments/{id}", a.getShipment)
	r.With(authz.RequireDuty("WarehouseView")).Get("/v1/fulfillment/packing-slips", a.listPackingSlips)
	r.With(authz.RequireAnyDuty("WarehouseView", "fulfillment.packing-slip.read")).Get("/v1/fulfillment/packing-slips/{id}", a.getPackingSlip)
	r.With(authz.RequireDuty("WarehousePick")).Post("/v1/fulfillment/shipments/{id}:confirm-pick", a.confirmPick)
	r.With(authz.RequireDuty("WarehousePack")).Post("/v1/fulfillment/shipments/{id}/containers", a.packContainer)
	r.With(authz.RequireDuty("PackingSlipPost")).Post("/v1/fulfillment/shipments/{id}:post-packing-slip", a.postPackingSlip)
	r.With(authz.RequireDuty("PackingSlipPost")).Post("/v1/fulfillment/packing-slips/{id}:cancel", a.cancelPackingSlip)
	a.routesPacking(r)
	a.routesWorkbench(r)
	a.routesCarrier(r)
	a.routesReleaseRule(r)
	a.routesDeliveryTolerance(r)
	a.routesInquiry(r)
	a.routesException(r)
	a.routesTracking(r)
}

// ---- request and response DTOs -------------------------------------------

type releaseRequest struct {
	WarehouseID    string        `json:"warehouseId"`
	HorizonDays    *int          `json:"horizonDays,omitempty"`
	RequireFullATP *bool         `json:"requireFullAtp,omitempty"`
	Lines          []releaseLine `json:"lines"`
}

type releaseLine struct {
	OrderID           string `json:"orderId"`
	OrderLineID       string `json:"orderLineId"`
	CustomerID        string `json:"customerId"`
	LegalEntityID     string `json:"legalEntityId"`
	ProductID         string `json:"productId"`
	Unit              string `json:"unit"`
	Qty               string `json:"qty"`
	ConfirmedShipDate string `json:"confirmedShipDate"`
	ATPOK             bool   `json:"atpOk"`
	Held              bool   `json:"held"`
	// CarrierCode is the order's carrier and Zone the line's pick zone; the
	// picking workbench filters and groups by them (FR-28.13). Both optional.
	CarrierCode string `json:"carrierCode"`
	Zone        string `json:"zone"`
	// TrackingRequired marks a batch or serial tracked product (BR-28.10).
	TrackingRequired bool `json:"trackingRequired"`
	// OrderType is order_type of order.confirmed (JOURNAL, ORDER, SUBSCRIPTION,
	// RETURNED); empty is ORDER. A JOURNAL order never ships, so its lines are skipped.
	OrderType string `json:"orderType"`
	// Priority is the order priority, 1 (first) to 9; 5 when left out. Every
	// line of one order names the same one.
	Priority int `json:"priority"`
	// ATPQty is the quantity available to promise now, a decimal of 0 or more.
	// When the warehouse does not require full ATP and it is below qty, the
	// line is released for ATPQty and the rest stays open for a later run.
	ATPQty *string `json:"atpQty"`
}

type releaseResponse struct {
	Shipments []store.Shipment `json:"shipments"`
	Skipped   []domain.Skip    `json:"skipped"`
}

type listResponse[T any] struct {
	Items []T `json:"items"`
}

var (
	shipmentStatuses = map[string]bool{"OPEN": true, "RELEASED": true, "PICKING": true, "PICKED": true, "PACKED": true,
		"PARTIALLY_SHIPPED": true, "SHIPPED": true, "DELIVERED": true, "CANCELLED": true}
	slipStatuses = map[string]bool{"PENDING": true, "POSTED": true, "CANCELLED": true, "INVOICED": true}
)

type confirmPickRequest struct {
	Picks []pickDTO `json:"picks"`
}

type pickDTO struct {
	ShipmentLineID string `json:"shipmentLineId"`
	Picked         string `json:"picked"`
	// Reason is the short pick reason code (OUT_OF_STOCK, DAMAGED, NOT_FOUND,
	// QUALITY_HOLD, OTHER); optional, and only for a pick below plan.
	Reason string `json:"reason,omitempty"`
}

type packRequest struct {
	ContainerType string    `json:"containerType"`
	Lines         []packDTO `json:"lines"`
}

type packDTO struct {
	ShipmentLineID string `json:"shipmentLineId"`
	Qty            string `json:"qty"`
}

type postRequest struct {
	PostingDate    string `json:"postingDate"`
	Currency       string `json:"currency"`
	CogsAtShipment *bool  `json:"cogsAtShipment"`
	CountryCode    string `json:"countryCode"`
	// AsnEnabled publishes the advance ship notice with this posting (FR-28.9).
	// Omitted, cogsAtShipment and asnEnabled take the legal entity's setting.
	AsnEnabled *bool     `json:"asnEnabled"`
	Costs      []costDTO `json:"costs"`
}

type costDTO struct {
	ShipmentLineID string `json:"shipmentLineId"`
	UnitCost       string `json:"unitCost"`
	CostSnapshotID string `json:"costSnapshotId"`
}

// ---- handlers --------------------------------------------------------------

func (a *API) runRelease(w http.ResponseWriter, r *http.Request) {
	var req releaseRequest
	_, idem, err := command(r, &req)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !ids.Valid(req.WarehouseID) {
		fail(w, r, "warehouseId", "must be a UUID")
		return
	}
	lines, err := toReleaseLines(req)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	// A request value wins, then the warehouse's saved rule, then the process
	// configuration (RELEASE_HORIZON_DAYS).
	horizon, fullATP := a.opts.ReleaseHorizonDays, a.opts.RequireFullATP
	if req.HorizonDays == nil || req.RequireFullATP == nil {
		rule, err := a.store.GetReleaseRule(r.Context(), req.WarehouseID)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if rule.Saved() {
			horizon, fullATP = rule.HorizonDays, rule.RequireFullATP
		}
	}
	if req.HorizonDays != nil {
		if *req.HorizonDays < 0 || *req.HorizonDays > 30 {
			fail(w, r, "horizonDays", "must be 0 to 30")
			return
		}
		horizon = *req.HorizonDays
	}
	if req.RequireFullATP != nil {
		fullATP = *req.RequireFullATP
	}
	// The order type fulfillment stored from sales' order.confirmed.v1 overrides what the
	// caller names, so a JOURNAL order cannot ship because a caller left the type out. When
	// the two disagree on JOURNAL the stricter value wins: a caller's JOURNAL is kept even
	// over a stored ORDER (the stored type may lag a newer sales version), so release only
	// ever moves toward not shipping. An order fulfillment has not heard of keeps the
	// caller's value.
	orderIDs := make([]string, 0, len(lines))
	for _, l := range lines {
		orderIDs = append(orderIDs, l.OrderID)
	}
	stored, err := a.storedOrderTypes(r.Context(), orderIDs...)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	held, err := a.storedHolds(r.Context(), orderIDs...)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	for i := range lines {
		key := strings.ToLower(lines[i].OrderID)
		if t, ok := stored[key]; ok && lines[i].OrderType != domain.OrderTypeJournal {
			lines[i].OrderType = t
		}
		// An active hold sales told us about holds the line, whatever the caller says.
		if held[key] {
			lines[i].Held = true
		}
	}
	groups, skipped := domain.Release(lines, domain.ReleaseRule{Today: a.now(), HorizonDays: horizon, RequireFullATP: fullATP})
	res, err := a.store.RunRelease(r.Context(), req.WarehouseID, groups, idem)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := releaseResponse{Shipments: res.Shipments, Skipped: append(append([]domain.Skip{}, skipped...), res.Skipped...)}
	if out.Shipments == nil {
		out.Shipments = []store.Shipment{}
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) listShipments(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.ShipmentFilter{Status: q.Get("status"), WarehouseID: q.Get("warehouseId"), OrderID: q.Get("orderId")}
	if f.Status != "" && !shipmentStatuses[f.Status] {
		fail(w, r, "status", "must be a shipment status")
		return
	}
	if f.WarehouseID != "" && !ids.Valid(f.WarehouseID) {
		fail(w, r, "warehouseId", "must be a UUID")
		return
	}
	if f.OrderID != "" && !ids.Valid(f.OrderID) {
		fail(w, r, "orderId", "must be a UUID")
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	f.Limit = limit
	items, err := a.store.ListShipments(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.ShipmentSummary]{Items: items})
}

func (a *API) listPackingSlips(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.PackingSlipFilter{ShipmentID: q.Get("shipmentId"), OrderID: q.Get("orderId"), Status: q.Get("status")}
	if f.ShipmentID != "" && !ids.Valid(f.ShipmentID) {
		fail(w, r, "shipmentId", "must be a UUID")
		return
	}
	if f.OrderID != "" && !ids.Valid(f.OrderID) {
		fail(w, r, "orderId", "must be a UUID")
		return
	}
	if f.Status != "" && !slipStatuses[f.Status] {
		fail(w, r, "status", "must be a packing slip status")
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	f.Limit = limit
	items, err := a.store.ListPackingSlips(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.PackingSlipSummary]{Items: items})
}

func (a *API) getPackingSlip(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeSlipNotFound, "packing slip not found"))
		return
	}
	slip, err := a.store.GetPackingSlip(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, slip)
}

func (a *API) getShipment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeShipmentNotFound, "shipment not found"))
		return
	}
	sh, err := a.store.GetShipment(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	etag(w, sh.Version)
	httpx.JSON(w, http.StatusOK, sh)
}

func (a *API) confirmPick(w http.ResponseWriter, r *http.Request) {
	id, version, ok := a.shipmentCommand(w, r)
	if !ok {
		return
	}
	var req confirmPickRequest
	_, idem, err := command(r, &req)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if len(req.Picks) == 0 {
		fail(w, r, "picks", "at least one pick is required")
		return
	}
	picks, ok := pickInputs(w, r, req.Picks)
	if !ok {
		return
	}
	sh, err := a.store.ConfirmPick(r.Context(), id, version, picks, idem)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	etag(w, sh.Version)
	httpx.JSON(w, http.StatusOK, sh)
}

func (a *API) packContainer(w http.ResponseWriter, r *http.Request) {
	id, version, ok := a.shipmentCommand(w, r)
	if !ok {
		return
	}
	var req packRequest
	_, idem, err := command(r, &req)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if strings.TrimSpace(req.ContainerType) == "" || len(req.ContainerType) > 64 {
		fail(w, r, "containerType", "is required and must be at most 64 characters")
		return
	}
	if len(req.Lines) == 0 {
		fail(w, r, "lines", "at least one line is required")
		return
	}
	lines := make([]store.PackInput, 0, len(req.Lines))
	seen := map[string]bool{}
	for i, l := range req.Lines {
		if !ids.Valid(l.ShipmentLineID) {
			fail(w, r, fmt.Sprintf("lines[%d].shipmentLineId", i), "must be a UUID")
			return
		}
		if seen[l.ShipmentLineID] {
			fail(w, r, fmt.Sprintf("lines[%d].shipmentLineId", i), "appears more than once")
			return
		}
		seen[l.ShipmentLineID] = true
		q, err := positive(l.Qty)
		if err != nil {
			fail(w, r, fmt.Sprintf("lines[%d].qty", i), "must be a positive decimal with at most 6 decimals")
			return
		}
		lines = append(lines, store.PackInput{ShipmentLineID: l.ShipmentLineID, Qty: q})
	}
	res, err := a.store.PackContainer(r.Context(), id, version, strings.TrimSpace(req.ContainerType), lines, idem)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	etag(w, res.Shipment.Version)
	httpx.JSON(w, http.StatusCreated, res)
}

func (a *API) postPackingSlip(w http.ResponseWriter, r *http.Request) {
	id, version, ok := a.shipmentCommand(w, r)
	if !ok {
		return
	}
	var req postRequest
	_, idem, err := command(r, &req)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	postingDate, err := time.Parse(dateLayout, req.PostingDate)
	if err != nil {
		fail(w, r, "postingDate", "must be a date as YYYY-MM-DD")
		return
	}
	if !currencyCode.MatchString(req.Currency) {
		fail(w, r, "currency", "must be an ISO 4217 alpha-3 code")
		return
	}
	if req.CountryCode != "" && !countryRE.MatchString(req.CountryCode) {
		fail(w, r, "countryCode", "must be a 2-letter ISO 3166 code when given")
		return
	}
	costs := make(map[string]store.Cost, len(req.Costs))
	for i, c := range req.Costs {
		if !ids.Valid(c.ShipmentLineID) {
			fail(w, r, fmt.Sprintf("costs[%d].shipmentLineId", i), "must be a UUID")
			return
		}
		if _, dup := costs[c.ShipmentLineID]; dup {
			fail(w, r, fmt.Sprintf("costs[%d].shipmentLineId", i), "appears more than once")
			return
		}
		unit, err := nonNegative(c.UnitCost)
		if err != nil {
			fail(w, r, fmt.Sprintf("costs[%d].unitCost", i), "must be a non-negative decimal with at most 6 decimals")
			return
		}
		if strings.TrimSpace(c.CostSnapshotID) == "" || len(c.CostSnapshotID) > 128 {
			fail(w, r, fmt.Sprintf("costs[%d].costSnapshotId", i), "is required and must be at most 128 characters")
			return
		}
		costs[c.ShipmentLineID] = store.Cost{UnitCost: unit, CostSnapshotID: c.CostSnapshotID}
	}
	in := store.PostInput{PostingDate: postingDate, Currency: req.Currency, CogsAtShipment: req.CogsAtShipment != nil && *req.CogsAtShipment,
		CountryCode: req.CountryCode, Costs: costs, EmitASN: req.AsnEnabled != nil && *req.AsnEnabled,
		CogsFromSetting: req.CogsAtShipment == nil, AsnFromSetting: req.AsnEnabled == nil}
	slip, err := a.store.PostPackingSlip(r.Context(), id, version, in, idem)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, slip)
}

func (a *API) cancelPackingSlip(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeSlipNotFound, "packing slip not found"))
		return
	}
	_, idem, err := command(r, nil)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	slip, err := a.store.CancelPackingSlip(r.Context(), id, idem)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, slip)
}

// ---- helpers ---------------------------------------------------------------

// shipmentCommand validates the shipment id and the If-Match version of a
// state-changing shipment command (428 when If-Match is absent).
func (a *API) shipmentCommand(w http.ResponseWriter, r *http.Request) (string, int, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeShipmentNotFound, "shipment not found"))
		return "", 0, false
	}
	raw := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`)
	if raw == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusPreconditionRequired, CodePreconditionIf, "If-Match with the shipment version is required"))
		return "", 0, false
	}
	version, err := strconv.Atoi(raw)
	if err != nil || version < 1 {
		fail(w, r, "If-Match", "must be the shipment version as an integer")
		return "", 0, false
	}
	return id, version, true
}

// command reads the body into v (when v is non-nil), checks the Idempotency-Key
// and returns the key with a hash of the method, path and body. The path is in
// the hash so a key reused on another resource (for example cancelling a second
// slip, whose body is empty too) is refused instead of replaying the first answer.
func command(r *http.Request, v any) ([]byte, store.Idem, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		return nil, store.Idem{}, httpx.NewProblem(http.StatusBadRequest, codeMalformed, "body could not be read")
	}
	if v != nil && len(bytes.TrimSpace(raw)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(v); err != nil {
			return nil, store.Idem{}, httpx.NewProblem(http.StatusBadRequest, codeMalformed, "malformed JSON body: "+err.Error())
		}
	} else if v != nil {
		return nil, store.Idem{}, httpx.NewProblem(http.StatusBadRequest, codeMalformed, "a JSON body is required")
	}
	key := strings.TrimSpace(r.Header.Get(headerIdempotency))
	if key == "" || len(key) > 255 {
		return nil, store.Idem{}, httpx.NewProblem(http.StatusBadRequest, CodeIdemMissing, "Idempotency-Key header is required (1 to 255 characters)")
	}
	h := sha256.New()
	h.Write([]byte(r.Method + " " + r.URL.Path + "\n"))
	h.Write(raw)
	return raw, store.Idem{Key: key, Hash: hex.EncodeToString(h.Sum(nil))}, nil
}

// invalid builds a validation problem that names the offending field.
func invalid(field, rule string) error {
	p := httpx.NewProblem(http.StatusBadRequest, CodeValidation, validationDetail)
	p.Errors = map[string]string{field: rule}
	return p
}

func toReleaseLines(req releaseRequest) ([]domain.ReleaseLine, error) {
	if len(req.Lines) == 0 {
		return nil, invalid("lines", "at least one line is required")
	}
	out := make([]domain.ReleaseLine, 0, len(req.Lines))
	seen := map[string]bool{}
	type orderParties struct {
		customer, legalEntity, carrier, orderType string
		priority                                  int
	}
	orders := map[string]orderParties{}
	for i, l := range req.Lines {
		field := func(name string) string { return fmt.Sprintf("lines[%d].%s", i, name) }
		for _, c := range []struct{ name, id string }{{"orderId", l.OrderID}, {"orderLineId", l.OrderLineID},
			{"customerId", l.CustomerID}, {"legalEntityId", l.LegalEntityID}, {"productId", l.ProductID}} {
			if !ids.Valid(c.id) {
				return nil, invalid(field(c.name), "must be a UUID")
			}
		}
		if !unitCode.MatchString(l.Unit) {
			return nil, invalid(field("unit"), "must be 1 to 8 letters or digits")
		}
		qty, err := positive(l.Qty)
		if err != nil {
			return nil, invalid(field("qty"), "must be a positive decimal with at most 6 decimals")
		}
		ship, err := time.Parse(dateLayout, l.ConfirmedShipDate)
		if err != nil {
			return nil, invalid(field("confirmedShipDate"), "must be a date as YYYY-MM-DD")
		}
		if l.CarrierCode != "" && !logisticsCode.MatchString(l.CarrierCode) {
			return nil, invalid(field("carrierCode"), "must be 1 to 32 letters, digits, '.', '_' or '-' when given")
		}
		if l.Zone != "" && !logisticsCode.MatchString(l.Zone) {
			return nil, invalid(field("zone"), "must be 1 to 32 letters, digits, '.', '_' or '-' when given")
		}
		if !domain.ValidOrderType(l.OrderType) {
			return nil, invalid(field("orderType"), "must be JOURNAL, ORDER, SUBSCRIPTION or RETURNED when given")
		}
		if l.Priority != 0 && (l.Priority < 1 || l.Priority > 9) {
			return nil, invalid(field("priority"), "must be 1 (first) to 9 when given")
		}
		var atpQty *decimal.Decimal
		if l.ATPQty != nil {
			q, err := decimal.NewFromString(*l.ATPQty)
			if err != nil || q.IsNegative() || !q.Equal(q.Round(maxScale)) {
				return nil, invalid(field("atpQty"), "must be a decimal of 0 or more with at most 6 decimals when given")
			}
			atpQty = &q
		}
		if seen[l.OrderLineID] {
			return nil, invalid(field("orderLineId"), "appears more than once")
		}
		seen[l.OrderLineID] = true
		// A shipment belongs to one customer, one legal entity and one
		// carrier, so every line of one order must name the same ones.
		if prev, ok := orders[l.OrderID]; ok {
			if prev.customer != l.CustomerID {
				return nil, invalid(field("customerId"), "differs from another line of the same order")
			}
			if prev.legalEntity != l.LegalEntityID {
				return nil, invalid(field("legalEntityId"), "differs from another line of the same order")
			}
			if prev.carrier != l.CarrierCode {
				return nil, invalid(field("carrierCode"), "differs from another line of the same order")
			}
			if prev.priority != normalPriority(l.Priority) {
				return nil, invalid(field("priority"), "differs from another line of the same order")
			}
			if prev.orderType != domain.NormalOrderType(l.OrderType) {
				return nil, invalid(field("orderType"), "differs from another line of the same order")
			}
		} else {
			orders[l.OrderID] = orderParties{customer: l.CustomerID, legalEntity: l.LegalEntityID, carrier: l.CarrierCode, orderType: domain.NormalOrderType(l.OrderType), priority: normalPriority(l.Priority)}
		}
		out = append(out, domain.ReleaseLine{OrderID: l.OrderID, OrderLineID: l.OrderLineID, CustomerID: l.CustomerID,
			LegalEntityID: l.LegalEntityID, WarehouseID: req.WarehouseID, ProductID: l.ProductID, Unit: l.Unit, Qty: qty,
			ConfirmedShipDate: ship, ATPOK: l.ATPOK, Held: l.Held, CarrierCode: l.CarrierCode, Zone: l.Zone,
			TrackingRequired: l.TrackingRequired, OrderType: l.OrderType, Priority: l.Priority, ATPQty: atpQty})
	}
	return out, nil
}

func normalPriority(p int) int {
	if p == 0 {
		return domain.DefaultPriority
	}
	return p
}

// limitParam reads ?limit= (default 100, 1 to 500).
func limitParam(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return 100, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 500 {
		fail(w, r, "limit", "must be an integer from 1 to 500")
		return 0, false
	}
	return n, true
}

// maxScale is the number of decimals the numeric(20,6) columns keep; a value
// with more would be rounded silently by Postgres, so it is refused here.
const maxScale = 6

func positive(s string) (decimal.Decimal, error) {
	d, err := decimal.NewFromString(s)
	if err != nil || !d.IsPositive() || !d.Equal(d.Round(maxScale)) {
		return decimal.Zero, fmt.Errorf("not a positive decimal")
	}
	return d, nil
}

func nonNegative(s string) (decimal.Decimal, error) {
	d, err := decimal.NewFromString(s)
	if err != nil || d.IsNegative() || !d.Equal(d.Round(maxScale)) {
		return decimal.Zero, fmt.Errorf("not a non-negative decimal")
	}
	return d, nil
}

func fail(w http.ResponseWriter, r *http.Request, field, rule string) {
	httpx.Fail(w, r, invalid(field, rule))
}

func etag(w http.ResponseWriter, version int) {
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, version))
}

// pickInputs validates the pick lines of a confirm-pick body.
func pickInputs(w http.ResponseWriter, r *http.Request, in []pickDTO) ([]store.PickInput, bool) {
	picks := make([]store.PickInput, 0, len(in))
	seen := map[string]bool{}
	for i, p := range in {
		if !ids.Valid(p.ShipmentLineID) {
			fail(w, r, fmt.Sprintf("picks[%d].shipmentLineId", i), "must be a UUID")
			return nil, false
		}
		if seen[p.ShipmentLineID] {
			fail(w, r, fmt.Sprintf("picks[%d].shipmentLineId", i), "appears more than once")
			return nil, false
		}
		seen[p.ShipmentLineID] = true
		q, err := nonNegative(p.Picked)
		if err != nil {
			fail(w, r, fmt.Sprintf("picks[%d].picked", i), "must be a non-negative decimal with at most 6 decimals")
			return nil, false
		}
		if !store.ValidShortPickReason(p.Reason) {
			fail(w, r, fmt.Sprintf("picks[%d].reason", i), "must be one of "+strings.Join(store.ShortPickReasons, ", "))
			return nil, false
		}
		picks = append(picks, store.PickInput{ShipmentLineID: p.ShipmentLineID, Picked: q, Reason: p.Reason})
	}
	return picks, true
}
