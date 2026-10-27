// Package api exposes the transport HTTP routes: rate cards, rate shopping,
// freight bills and their approval, carrier invoices, tenders, tracking events,
// landed-cost voyages and goods in transit, load building, DG checks,
// trade-compliance screening and its holds, driver check-in and check-out at
// the gate, route optimization and load-plan stacking.
package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/transport/internal/store"
)

// Duties (PRD §6.34.17).
const (
	DutyView        = "TMS_VIEW"
	DutyRate        = "TMS_RATE"
	DutyRateApprove = "TMS_RATE_APPROVE"
	DutyReconcile   = "TMS_RECONCILE"
	DutyApprove     = "TMS_RECONCILE_APPROVE"
	DutyPlan        = "TMS_PLAN"
	DutyTender      = "TMS_TENDER"
	DutyRespond     = "TMS_CARRIER_RESPOND"
	DutyTrackIngest = "TMS_TRACK_INGEST"
	DutyLandedCost  = "TMS_LANDED_COST"
	// DutyTradeRelease releases screening holds (BR-34.17). It conflicts with
	// SALES_ORDER_MAINTAIN (§6.34.17); role design keeps them apart.
	DutyTradeRelease = "TRADE_COMPLIANCE_RELEASE"
	idempotencyKey   = "Idempotency-Key"
	validationDetail = "request failed validation"
	maxCodeLength    = 20
	maxKeyLength     = 200
	maxBody          = 4 << 20
	prefix           = "/v1/transport"
)

// uuidRe accepts only the canonical lower-case form, so filters and keys
// compare as stored.
var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

var billStatuses = []string{domain.BillUnmatched, domain.MatchMatched, domain.MatchVariance, domain.BillDisputed, domain.BillApproved, domain.BillSettled}

var invoiceStatuses = []string{domain.MatchMatched, domain.MatchVariance, domain.MatchUnexpected}

// API holds the store and serves the transport routes. The counters feed the
// admin /metrics endpoint.
type API struct {
	store     store.Store
	clock     func() time.Time
	screening domain.ScreeningProvider
	optimizer Optimizer
	rates     *rateCache

	rateShops        atomic.Int64
	rateCardsCreated atomic.Int64
	billsCreated     atomic.Int64
	invoicesMatched  atomic.Int64
	invoicesVariance atomic.Int64
	invoicesUnexpect atomic.Int64
	tendersStarted   atomic.Int64
	tenderResponses  atomic.Int64
	tendersExpired   atomic.Int64
	trackingIngested atomic.Int64
	voyagesPosted    atomic.Int64
	loadsBuilt       atomic.Int64
	dgChecks         atomic.Int64
	screenings       atomic.Int64
	billsApproved    atomic.Int64
	billsDisputed    atomic.Int64
	gateCheckIns     atomic.Int64
	gateCheckOuts    atomic.Int64
	gateBlocked      atomic.Int64
	gitCreated       atomic.Int64
	gitShipped       atomic.Int64
	rateCacheHits    atomic.Int64
	holdsOpened      atomic.Int64
	holdsReleased    atomic.Int64
	routesOptimized  atomic.Int64
	loadPlans        atomic.Int64
	optimizerErrors  atomic.Int64
	serverErrors     atomic.Int64
}

// New returns the API over st, reading time from the system clock, with the
// default 10-minute rate-shop cache.
func New(st store.Store) *API {
	return &API{store: st, clock: time.Now, rates: newRateCache(defaultRatingCacheMin * time.Minute)}
}

// WithRatingCacheMinutes sets the rate-shop cache window (rating_cache_min,
// FR-34.4). Values outside 0 to 60 are clamped; 0 turns the cache off.
func (a *API) WithRatingCacheMinutes(m int) *API {
	a.rates = newRateCache(time.Duration(clampInt(m, 0, maxRatingCacheMin)) * time.Minute)
	return a
}

// WithClock replaces the time source; tests use it to step through tender deadlines.
func (a *API) WithClock(now func() time.Time) *API {
	a.clock = now
	return a
}

// WithScreeningProvider plugs in a ComplianceScreeningProvider (FR-34.19).
// Without one, cross-border loads screen as NOT_SCREENED (TMS-0134) until the
// wave-48 providers exist.
func (a *API) WithScreeningProvider(p domain.ScreeningProvider) *API {
	a.screening = p
	return a
}

func (a *API) now() time.Time { return a.clock().UTC().Truncate(time.Microsecond) }

// Register mounts every route with its duty check. Browser calls arrive through
// the web BFF at /api/v1/transport/..., rewritten to these /v1/transport/... paths.
func (a *API) Register(r chi.Router) {
	r.With(authz.RequireDuty(DutyRate)).Post(prefix+"/shipments/{id}:rate", a.rateShipment)
	r.With(authz.RequireDuty(DutyRateApprove)).Post(prefix+"/rate-cards", a.createRateCard)
	r.With(authz.RequireDuty(DutyRate)).Get(prefix+"/rate-cards", a.listRateCards)
	r.With(authz.RequireDuty(DutyReconcile)).Post(prefix+"/freight-bills", a.createFreightBill)
	r.With(authz.RequireDuty(DutyView)).Get(prefix+"/freight-bills", a.listFreightBills)
	r.With(authz.RequireDuty(DutyView)).Get(prefix+"/freight-bills/{id}", a.getFreightBill)
	r.With(authz.RequireDuty(DutyApprove)).Post(prefix+"/freight-bills/{id}:approve", a.approveFreightBill)
	r.With(authz.RequireDuty(DutyReconcile)).Post(prefix+"/freight-bills/{id}:dispute", a.disputeFreightBill)
	r.With(authz.RequireDuty(DutyReconcile)).Post(prefix+"/carrier-invoices", a.registerCarrierInvoice)
	r.With(authz.RequireDuty(DutyReconcile)).Get(prefix+"/carrier-invoices", a.listCarrierInvoices)
	r.With(authz.RequireDuty(DutyReconcile)).Get(prefix+"/carrier-invoices/{id}", a.getCarrierInvoice)
	r.With(authz.RequireDuty(DutyPlan)).Post(prefix+"/loads:build", a.buildLoads)
	r.With(authz.RequireDuty(DutyView)).Get(prefix+"/loads", a.listLoads)
	r.With(authz.RequireDuty(DutyView)).Get(prefix+"/loads/{id}", a.getLoad)
	r.With(authz.RequireDuty(DutyPlan)).Post(prefix+"/loads/{id}:dg-check", a.checkLoadDG)
	r.With(authz.RequireDuty(DutyPlan)).Post(prefix+"/loads/{id}:screen", a.screenLoad)
	r.With(authz.RequireDuty(DutyView)).Get(prefix+"/transfer-rules/{incoterm}", a.transferRule)
	r.With(authz.RequireDuty(DutyTender)).Post(prefix+"/loads/{id}:tender", a.startTender)
	r.With(authz.RequireDuty(DutyTender)).Get(prefix+"/tenders", a.listTenders)
	r.With(authz.RequireDuty(DutyTender)).Post(prefix+"/tenders:expire", a.expireTenders)
	r.With(authz.RequireDuty(DutyTender)).Get(prefix+"/tenders/{id}", a.getTender)
	r.With(authz.RequireDuty(DutyRespond)).Post(prefix+"/tenders/{id}:respond", a.respondTender)
	r.With(authz.RequireDuty(DutyTrackIngest)).Post(prefix+"/tracking-events", a.ingestTracking)
	r.With(authz.RequireDuty(DutyView)).Get(prefix+"/tracking-events", a.listTracking)
	r.With(authz.RequireDuty(DutyView)).Get(prefix+"/loads/{id}/tracking", a.loadTracking)
	r.With(authz.RequireDuty(DutyLandedCost)).Post(prefix+"/voyages", a.createVoyage)
	r.With(authz.RequireDuty(DutyLandedCost)).Get(prefix+"/voyages", a.listVoyages)
	r.With(authz.RequireDuty(DutyLandedCost)).Get(prefix+"/voyages/{id}", a.getVoyage)
	r.With(authz.RequireDuty(DutyLandedCost)).Post(prefix+"/landed-cost/voyages/{id}:allocate", a.allocateVoyage)
	r.With(authz.RequireDuty(DutyLandedCost)).Post(prefix+"/goods-in-transit", a.createGoodsInTransit)
	r.With(authz.RequireDuty(DutyLandedCost)).Get(prefix+"/goods-in-transit", a.listGoodsInTransit)
	r.With(authz.RequireDuty(DutyLandedCost)).Get(prefix+"/goods-in-transit/{id}", a.getGoodsInTransit)
	r.With(authz.RequireDuty(DutyLandedCost)).Post(prefix+"/goods-in-transit/{id}:ship", a.shipGoodsInTransit)
	r.With(authz.RequireDuty(DutyGate)).Post(prefix+"/gate/check-in", a.checkIn)
	r.With(authz.RequireDuty(DutyGate)).Post(prefix+"/gate/check-out", a.checkOut)
	r.With(authz.RequireDuty(DutyGate)).Get(prefix+"/gate/visits", a.listGateVisits)
	r.With(authz.RequireDuty(DutyGate)).Get(prefix+"/gate/visits/{id}", a.getGateVisit)
	r.With(authz.RequireDuty(DutyGate)).Post(prefix+"/gate/visits/{id}:seal", a.recordSeal)
	r.With(authz.RequireDuty(DutyView)).Get(prefix+"/screening-holds", a.listScreeningHolds)
	r.With(authz.RequireDuty(DutyView)).Get(prefix+"/screening-holds/{id}", a.getScreeningHold)
	r.With(authz.RequireDuty(DutyTradeRelease)).Post(prefix+"/screening-holds/{id}:release", a.releaseScreeningHold)
	r.With(authz.RequireDuty(DutyPlan)).Post(prefix+"/routes:optimize", a.optimizeRoutes)
	r.With(authz.RequireDuty(DutyPlan)).Get(prefix+"/routes", a.listRoutePlans)
	r.With(authz.RequireDuty(DutyPlan)).Get(prefix+"/routes/{id}", a.getRoutePlan)
	r.With(authz.RequireDuty(DutyPlan)).Post(prefix+"/load-plans:stack", a.stackLoadPlan)
}

type rateCardRequest struct {
	CarrierID     string               `json:"carrierId"`
	ServiceID     string               `json:"serviceId"`
	Basis         string               `json:"basis"`
	Currency      string               `json:"currency"`
	ValidFrom     string               `json:"validFrom"`
	ValidTo       *string              `json:"validTo"`
	MinimumCharge decimal.Decimal      `json:"minimumCharge"`
	DimDivisor    int                  `json:"dimDivisor"`
	FuelPct       decimal.Decimal      `json:"fuelPct"`
	Rows          []domain.RateRow     `json:"rows"`
	Accessorials  []domain.Accessorial `json:"accessorials"`
}

type rateRequest struct {
	OriginZone       string          `json:"originZone"`
	DestinationZone  string          `json:"destinationZone"`
	WeightKg         decimal.Decimal `json:"weightKg"`
	VolumeM3         decimal.Decimal `json:"volumeM3"`
	Pallets          int             `json:"pallets"`
	DistanceKm       decimal.Decimal `json:"distanceKm"`
	ShipDate         string          `json:"shipDate"`
	AccessorialCodes []string        `json:"accessorialCodes"`
}

type rateResponse struct {
	ShipmentID string         `json:"shipmentId"`
	Options    []domain.Quote `json:"options"`
	Chosen     *domain.Quote  `json:"chosen,omitempty"`
}

type freightBillRequest struct {
	LoadID        string        `json:"loadId"`
	CarrierID     string        `json:"carrierId"`
	Currency      string        `json:"currency"`
	ExpectedLines []domain.Line `json:"expectedLines"`
}

type carrierInvoiceRequest struct {
	CarrierID     string        `json:"carrierId"`
	InvoiceNo     string        `json:"invoiceNo"`
	InvoiceDate   string        `json:"invoiceDate"`
	Source        string        `json:"source"`
	FreightBillID *string       `json:"freightBillId"`
	Currency      string        `json:"currency"`
	Lines         []domain.Line `json:"lines"`
}

type invoiceResponse struct {
	Invoice     domain.CarrierInvoice `json:"invoice"`
	FreightBill *domain.FreightBill   `json:"freightBill,omitempty"`
}

// freightBillDetail is a bill with its carrier invoice, the line-by-line
// comparison the reconciliation grid shows (expected vs invoiced per charge)
// and its approvals and disputes.
type freightBillDetail struct {
	domain.FreightBill
	CarrierInvoice *domain.CarrierInvoice `json:"carrierInvoice,omitempty"`
	Match          *domain.MatchResult    `json:"match,omitempty"`
	Decisions      []domain.Decision      `json:"decisions"`
}

func (a *API) createRateCard(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	var req rateCardRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !uuidRe.MatchString(req.CarrierID) {
		fields["carrierId"] = "must be a lower-case UUID"
	}
	if !uuidRe.MatchString(req.ServiceID) {
		fields["serviceId"] = "must be a lower-case UUID"
	}
	from, ok := parseDate(req.ValidFrom)
	if !ok {
		fields["validFrom"] = "must be a date as YYYY-MM-DD"
	}
	var to *time.Time
	if req.ValidTo != nil {
		t, ok := parseDate(*req.ValidTo)
		if !ok {
			fields["validTo"] = "must be a date as YYYY-MM-DD"
		}
		to = &t
	}
	dim := req.DimDivisor
	if dim == 0 {
		dim = domain.DefaultDimDivisor
	}
	card := domain.RateCard{
		CarrierID:     req.CarrierID,
		ServiceID:     req.ServiceID,
		Basis:         domain.Basis(req.Basis),
		Currency:      req.Currency,
		ValidFrom:     from,
		ValidTo:       to,
		MinimumCharge: req.MinimumCharge,
		DimDivisor:    dim,
		FuelPct:       req.FuelPct,
		Rows:          req.Rows,
		Accessorials:  req.Accessorials,
	}
	for k, v := range card.Validate() {
		if _, set := fields[k]; !set {
			fields[k] = v
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	created, err := a.store.CreateRateCard(r.Context(), card, idem)
	if err != nil {
		a.fail(w, r, "create rate card failed", err)
		return
	}
	a.rateCardsCreated.Add(1)
	p, _ := tenant.FromContext(r.Context())
	a.rates.invalidate(p.TenantID)
	httpx.JSON(w, http.StatusCreated, created)
}

func (a *API) listRateCards(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.RateCardFilter{CarrierID: q.Get("carrierId"), ServiceID: q.Get("serviceId")}
	fields := map[string]string{}
	checkOptionalID(fields, "carrierId", f.CarrierID)
	checkOptionalID(fields, "serviceId", f.ServiceID)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	cards, err := a.store.ListRateCards(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list rate cards failed", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": cards})
}

func (a *API) rateShipment(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	shipmentID := chi.URLParam(r, "id")
	if !uuidRe.MatchString(shipmentID) {
		validationFailed(w, r, map[string]string{"id": "must be a lower-case UUID"})
		return
	}
	var req rateRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if req.OriginZone == "" || len(req.OriginZone) > maxCodeLength {
		fields["originZone"] = "is required, up to 20 characters"
	}
	if req.DestinationZone == "" || len(req.DestinationZone) > maxCodeLength {
		fields["destinationZone"] = "is required, up to 20 characters"
	}
	if req.WeightKg.IsNegative() {
		fields["weightKg"] = "must not be negative"
	}
	if req.VolumeM3.IsNegative() {
		fields["volumeM3"] = "must not be negative"
	}
	if req.Pallets < 0 {
		fields["pallets"] = "must not be negative"
	}
	if req.DistanceKm.IsNegative() {
		fields["distanceKm"] = "must not be negative"
	}
	ship, ok := parseDate(req.ShipDate)
	if !ok {
		fields["shipDate"] = "must be a date as YYYY-MM-DD"
	}
	for _, code := range req.AccessorialCodes {
		if code == "" || len(code) > maxCodeLength {
			fields["accessorialCodes"] = "each code is 1 to 20 characters"
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	p, _ := tenant.FromContext(r.Context())
	key, err := rateCacheKey(req)
	if err != nil {
		a.fail(w, r, "hash rate request failed", err)
		return
	}
	a.rateShops.Add(1)
	if quotes, hit := a.rates.get(p.TenantID, key, a.clock()); hit {
		a.rateCacheHits.Add(1)
		w.Header().Set(headerRateCache, "HIT")
		writeRate(w, shipmentID, quotes)
		return
	}
	cards, err := a.store.ListRateCards(r.Context(), store.RateCardFilter{})
	if err != nil {
		a.fail(w, r, "load rate cards failed", err)
		return
	}
	s := domain.Shipment{
		OriginZone:      req.OriginZone,
		DestinationZone: req.DestinationZone,
		WeightKg:        req.WeightKg,
		VolumeM3:        req.VolumeM3,
		Pallets:         req.Pallets,
		DistanceKm:      req.DistanceKm,
		ShipDate:        ship,
		Accessorials:    req.AccessorialCodes,
	}
	quotes := domain.ShopRates(cards, s)
	a.rates.put(p.TenantID, key, quotes, a.clock())
	w.Header().Set(headerRateCache, "MISS")
	writeRate(w, shipmentID, quotes)
}

// writeRate answers a rate shop with the ranked options and the chosen one.
func writeRate(w http.ResponseWriter, shipmentID string, quotes []domain.Quote) {
	resp := rateResponse{ShipmentID: shipmentID, Options: quotes}
	if len(quotes) > 0 && quotes[0].Status == domain.QuoteOK {
		resp.Chosen = &quotes[0]
	}
	httpx.JSON(w, http.StatusOK, resp)
}

func (a *API) createFreightBill(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	var req freightBillRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	bill := domain.FreightBill{
		LoadID:        req.LoadID,
		CarrierID:     req.CarrierID,
		Currency:      req.Currency,
		ExpectedLines: req.ExpectedLines,
	}
	fields := domain.ValidateFreightBill(bill)
	if !uuidRe.MatchString(bill.LoadID) {
		fields["loadId"] = "must be a lower-case UUID"
	}
	if !uuidRe.MatchString(bill.CarrierID) {
		fields["carrierId"] = "must be a lower-case UUID"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	created, err := a.store.CreateFreightBill(r.Context(), bill, idem)
	if err != nil {
		a.fail(w, r, "create freight bill failed", err)
		return
	}
	a.billsCreated.Add(1)
	httpx.JSON(w, http.StatusCreated, created)
}

func (a *API) listFreightBills(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.FreightBillFilter{MatchStatus: q.Get("matchStatus"), CarrierID: q.Get("carrierId")}
	fields := map[string]string{}
	if f.MatchStatus != "" && !slices.Contains(billStatuses, f.MatchStatus) {
		fields["matchStatus"] = "must be one of " + strings.Join(billStatuses, ", ")
	}
	checkOptionalID(fields, "carrierId", f.CarrierID)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	bills, err := a.store.ListFreightBills(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list freight bills failed", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": bills})
}

func (a *API) getFreightBill(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		httpx.Fail(w, r, store.NotFound("freight bill"))
		return
	}
	bill, err := a.store.GetFreightBill(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get freight bill failed", err)
		return
	}
	detail := freightBillDetail{FreightBill: bill}
	if bill.CarrierInvoiceID != nil {
		inv, err := a.store.GetCarrierInvoice(r.Context(), *bill.CarrierInvoiceID)
		if err != nil {
			a.fail(w, r, "get matched carrier invoice failed", err)
			return
		}
		match := domain.MatchBill(bill, inv)
		detail.CarrierInvoice, detail.Match = &inv, &match
	}
	detail.Decisions, err = a.store.ListBillDecisions(r.Context(), id)
	if err != nil {
		a.fail(w, r, "list freight bill decisions failed", err)
		return
	}
	if detail.Decisions == nil {
		detail.Decisions = []domain.Decision{}
	}
	w.Header().Set("ETag", `"`+strconv.Itoa(bill.Version)+`"`)
	httpx.JSON(w, http.StatusOK, detail)
}

func (a *API) registerCarrierInvoice(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req carrierInvoiceRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	inv := domain.CarrierInvoice{
		CarrierID:     req.CarrierID,
		InvoiceNo:     strings.TrimSpace(req.InvoiceNo),
		InvoiceDate:   req.InvoiceDate,
		Source:        req.Source,
		Currency:      req.Currency,
		Lines:         req.Lines,
		FreightBillID: req.FreightBillID,
	}
	fields := domain.ValidateCarrierInvoice(inv)
	if !uuidRe.MatchString(inv.CarrierID) {
		fields["carrierId"] = "must be a lower-case UUID"
	}
	if _, ok := parseDate(req.InvoiceDate); !ok {
		fields["invoiceDate"] = "must be a date as YYYY-MM-DD"
	}
	if inv.FreightBillID != nil && !uuidRe.MatchString(*inv.FreightBillID) {
		fields["freightBillId"] = "must be a lower-case UUID when present"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	saved, bill, err := a.store.RegisterCarrierInvoice(r.Context(), inv)
	if err != nil {
		a.fail(w, r, "register carrier invoice failed", err)
		return
	}
	switch saved.MatchStatus {
	case domain.MatchMatched:
		a.invoicesMatched.Add(1)
	case domain.MatchVariance:
		a.invoicesVariance.Add(1)
	default:
		a.invoicesUnexpect.Add(1)
	}
	httpx.JSON(w, http.StatusCreated, invoiceResponse{Invoice: saved, FreightBill: bill})
}

func (a *API) listCarrierInvoices(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.InvoiceFilter{MatchStatus: q.Get("matchStatus"), CarrierID: q.Get("carrierId")}
	fields := map[string]string{}
	if f.MatchStatus != "" && !slices.Contains(invoiceStatuses, f.MatchStatus) {
		fields["matchStatus"] = "must be one of " + strings.Join(invoiceStatuses, ", ")
	}
	checkOptionalID(fields, "carrierId", f.CarrierID)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	invoices, err := a.store.ListCarrierInvoices(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list carrier invoices failed", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": invoices})
}

func (a *API) getCarrierInvoice(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		httpx.Fail(w, r, store.NotFound("carrier invoice"))
		return
	}
	inv, err := a.store.GetCarrierInvoice(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get carrier invoice failed", err)
		return
	}
	httpx.JSON(w, http.StatusOK, inv)
}

// Metrics serves the counters in the Prometheus text format on the admin port.
func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		for _, m := range []struct {
			name, help string
			value      int64
		}{
			{"transport_rate_shops_total", "Shipments rated across carrier services.", a.rateShops.Load()},
			{"transport_rate_cards_created_total", "Rate card create requests answered with a card, replays included.", a.rateCardsCreated.Load()},
			{"transport_freight_bills_created_total", "Freight bill create requests answered with a bill, replays included.", a.billsCreated.Load()},
			{"transport_carrier_invoices_matched_total", "Carrier invoices matched within tolerance.", a.invoicesMatched.Load()},
			{"transport_carrier_invoices_variance_total", "Carrier invoices matched with a variance outside tolerance.", a.invoicesVariance.Load()},
			{"transport_carrier_invoices_unexpected_total", "Carrier invoices without a freight bill (UNEXPECTED_INVOICE).", a.invoicesUnexpect.Load()},
			{"transport_tenders_started_total", "Tender start requests answered with a tender, replays included.", a.tendersStarted.Load()},
			{"transport_tender_responses_total", "Carrier accept or decline responses applied or replayed.", a.tenderResponses.Load()},
			{"transport_tenders_expired_total", "Tenders cascaded by the expiry sweep.", a.tendersExpired.Load()},
			{"transport_tracking_events_total", "Tracking events stored (replays excluded).", a.trackingIngested.Load()},
			{"transport_voyages_posted_total", "Voyage allocate requests answered with a posted voyage, replays included.", a.voyagesPosted.Load()},
			{"transport_loads_built_total", "Loads returned by rule-based load builds, replays included.", a.loadsBuilt.Load()},
			{"transport_dg_checks_total", "Dangerous-goods checks run and stored.", a.dgChecks.Load()},
			{"transport_screenings_total", "Load screening requests answered with a decision.", a.screenings.Load()},
			{"transport_freight_bills_approved_total", "Freight bill approvals applied or replayed.", a.billsApproved.Load()},
			{"transport_freight_bills_disputed_total", "Freight bill disputes applied or replayed.", a.billsDisputed.Load()},
			{"transport_gate_check_ins_total", "Gate check-in requests answered with a visit, replays included.", a.gateCheckIns.Load()},
			{"transport_gate_check_outs_total", "Gate check-outs applied or replayed.", a.gateCheckOuts.Load()},
			{"transport_gate_check_outs_blocked_total", "Gate check-outs blocked by a seal mismatch or an unconfirmed load (TMS-0129).", a.gateBlocked.Load()},
			{"transport_goods_in_transit_created_total", "Goods-in-transit order create requests answered with an order, replays included.", a.gitCreated.Load()},
			{"transport_goods_in_transit_shipped_total", "Goods-in-transit ship requests answered with a shipped order, replays included.", a.gitShipped.Load()},
			{"transport_rate_shop_cache_hits_total", "Rate shops answered from the rate-shop cache.", a.rateCacheHits.Load()},
			{"transport_screening_holds_opened_total", "Load screens that opened or refreshed a screening hold.", a.holdsOpened.Load()},
			{"transport_screening_holds_released_total", "Screening hold releases applied or replayed.", a.holdsReleased.Load()},
			{"transport_routes_optimized_total", "Route optimizations solved and stored, concurrent duplicates included.", a.routesOptimized.Load()},
			{"transport_load_plans_total", "Load plans stacked.", a.loadPlans.Load()},
			{"transport_optimizer_errors_total", "Optimizer calls that failed or returned something that is not a plan (TMS-0123).", a.optimizerErrors.Load()},
			{"transport_server_errors_total", "Requests that failed with a server error.", a.serverErrors.Load()},
		} {
			fmt.Fprintf(w, "# HELP %[1]s %[2]s\n# TYPE %[1]s counter\n%[1]s %[3]d\n", m.name, m.help, m.value)
		}
	})
}

// readIdem reads the body once (so it can still be decoded) and fingerprints
// method, path and body. The Idempotency-Key header is optional; when present
// it must be 1 to 200 characters.
func readIdem(w http.ResponseWriter, r *http.Request) (store.Idem, bool) {
	key := r.Header.Get(idempotencyKey)
	if len(key) > maxKeyLength {
		validationFailed(w, r, map[string]string{idempotencyKey: "header must be at most 200 characters"})
		return store.Idem{}, false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request body could not be read"))
		return store.Idem{}, false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	if key == "" {
		return store.Idem{}, true
	}
	sum := sha256.Sum256([]byte(r.Method + " " + r.URL.Path + "\n" + string(body)))
	return store.Idem{Key: key, Fingerprint: hex.EncodeToString(sum[:])}, true
}

func checkOptionalID(fields map[string]string, name, value string) {
	if value != "" && !uuidRe.MatchString(value) {
		fields[name] = "must be a lower-case UUID when present"
	}
}

func validTenant(w http.ResponseWriter, r *http.Request) bool {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return false
	}
	return true
}

func validationFailed(w http.ResponseWriter, r *http.Request, fields map[string]string) {
	p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
	p.Errors = fields
	httpx.Fail(w, r, p)
}

// fail writes err as a problem and logs it when it is a server fault; rule
// violations (4xx problems) are expected outcomes and are not logged as errors.
func (a *API) fail(w http.ResponseWriter, r *http.Request, msg string, err error) {
	var p *httpx.Problem
	if !errors.As(err, &p) || p.Status >= http.StatusInternalServerError {
		a.serverErrors.Add(1)
		slog.Error(msg, "err", err, "correlationId", middleware.GetReqID(r.Context()))
	}
	httpx.Fail(w, r, err)
}

func parseDate(value string) (time.Time, bool) {
	d, err := time.Parse(time.DateOnly, value)
	return d, err == nil
}
