package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// Truck stock (FR-45.9, BR-45.7): the levels a technician's truck is kept at, and the
// replenishment that brings it back up. The truck is a warehouse of its own at a site.
const (
	dutyTruckWrite   = "inventory.truck-stock.write"
	truckLevelsPath  = "/v1/inventory/truck-stock-levels"
	truckReplenishAt = "/v1/inventory/truck-stock:replenish"
	maxTruckLevels   = 500
)

// TruckStock is what the truck routes need of the store.
type TruckStock interface {
	SetTruckLevels(ctx context.Context, ref store.TruckRef, levels []domain.TruckLevel) error
	ListTruckLevels(ctx context.Context, legalEntityID, siteID, warehouseID string) ([]store.TruckLevelRow, error)
	ReplenishTrucks(ctx context.Context, legalEntityID, siteID, warehouseID string, shipDate time.Time) ([]domain.TransferOrder, error)
}

// WithTruckStock enables the truck stock routes; without it they answer 503.
func (a *API) WithTruckStock(t TruckStock) *API {
	a.truck = t
	return a
}

func (a *API) registerTruck(r chi.Router) {
	r.With(authz.RequireDuty(dutyTruckWrite)).Put(truckLevelsPath, a.setTruckLevels)
	r.With(authz.RequireDuty(dutyRead)).Get(truckLevelsPath, a.listTruckLevels)
	r.With(authz.RequireDuty(dutyTransferWrite)).Post(truckReplenishAt, a.replenishTrucks)
}

type truckLevelRequest struct {
	ProductID string `json:"productId"`
	Unit      string `json:"unit"`
	MinQty    string `json:"minQty"`
	MaxQty    string `json:"maxQty"`
}

type truckLevelsRequest struct {
	LegalEntityID     string              `json:"legalEntityId"`
	SiteID            string              `json:"siteId"`
	WarehouseID       string              `json:"warehouseId"`
	SourceSiteID      string              `json:"sourceSiteId"`
	SourceWarehouseID string              `json:"sourceWarehouseId"`
	Levels            []truckLevelRequest `json:"levels"`
}

type truckLevelResponse struct {
	LegalEntityID     string `json:"legalEntityId"`
	SiteID            string `json:"siteId"`
	WarehouseID       string `json:"warehouseId"`
	SourceSiteID      string `json:"sourceSiteId"`
	SourceWarehouseID string `json:"sourceWarehouseId"`
	ProductID         string `json:"productId"`
	Unit              string `json:"unit"`
	MinQty            string `json:"minQty"`
	MaxQty            string `json:"maxQty"`
	OnHandQty         string `json:"onHandQty"`
	InboundQty        string `json:"inboundQty"`
	// Short is true when the stock plus what is on its way is below the minimum.
	Short bool `json:"short"`
}

func (a *API) truckUnavailable(w http.ResponseWriter, r *http.Request) bool {
	if a.truck == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "INV-TRK-004", "truck stock is not enabled on this service"))
		return true
	}
	return false
}

// truckID checks a truck's site or warehouse: a UUID (the ids field service sends), kept in
// lower case as the truck consumer books it.
func truckID(fields map[string]string, name, value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if !ids.Valid(value) {
		fields[name] = "must be a UUID"
	}
	return value
}

func sourceCode(fields map[string]string, name, value string) string {
	value = strings.TrimSpace(value)
	code(fields, name, value, false)
	return value
}

// setTruckLevels replaces the levels of one truck.
func (a *API) setTruckLevels(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) || a.truckUnavailable(w, r) {
		return
	}
	var req truckLevelsRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	ref := store.TruckRef{LegalEntityID: req.LegalEntityID, SiteID: truckID(fields, "siteId", req.SiteID), WarehouseID: truckID(fields, "warehouseId", req.WarehouseID),
		SourceSiteID: sourceCode(fields, "sourceSiteId", req.SourceSiteID), SourceWarehouseID: sourceCode(fields, "sourceWarehouseId", req.SourceWarehouseID)}
	if ref.WarehouseID != "" && ref.WarehouseID == ref.SourceWarehouseID {
		fields["sourceWarehouseId"] = "must differ from the truck's warehouse"
	}
	if len(req.Levels) > maxTruckLevels {
		fields["levels"] = fmt.Sprintf("must hold at most %d levels", maxTruckLevels)
	}
	levels := make([]domain.TruckLevel, 0, len(req.Levels))
	for i, l := range req.Levels {
		key := fmt.Sprintf("levels[%d]", i)
		min, errMin := decimal.NewFromString(l.MinQty)
		max, errMax := decimal.NewFromString(l.MaxQty)
		switch {
		case !ids.Valid(l.ProductID):
			fields[key] = "productId must be a UUID"
		case !unitCode.MatchString(l.Unit):
			fields[key] = "unit must be 1 to 12 upper-case letters, digits, _ or -"
		case errMin != nil || errMax != nil:
			fields[key] = "minQty and maxQty must be decimal numbers"
		default:
			levels = append(levels, domain.TruckLevel{ProductID: strings.ToLower(l.ProductID), Unit: l.Unit, Min: min, Max: max})
		}
	}
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	if err := a.truck.SetTruckLevels(r.Context(), ref, levels); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.respondTruckLevels(w, r, req.LegalEntityID, ref.SiteID, ref.WarehouseID)
}

func (a *API) respondTruckLevels(w http.ResponseWriter, r *http.Request, legalEntityID, siteID, warehouseID string) {
	rows, err := a.truck.ListTruckLevels(r.Context(), legalEntityID, siteID, warehouseID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items := make([]truckLevelResponse, 0, len(rows))
	for _, l := range rows {
		items = append(items, truckLevelResponse{LegalEntityID: l.LegalEntityID, SiteID: l.SiteID, WarehouseID: l.WarehouseID, SourceSiteID: l.SourceSiteID,
			SourceWarehouseID: l.SourceWarehouseID, ProductID: l.ProductID, Unit: l.Unit, MinQty: l.Min.String(), MaxQty: l.Max.String(),
			OnHandQty: l.OnHand.String(), InboundQty: l.Inbound.String(), Short: l.OnHand.Add(l.Inbound).LessThan(l.Min)})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// listTruckLevels reads the levels of a legal entity, or of one truck, with on-hand and inbound.
func (a *API) listTruckLevels(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) || a.truckUnavailable(w, r) {
		return
	}
	q := r.URL.Query()
	if !ids.Valid(q.Get("legalEntityId")) {
		fieldProblem(w, r, map[string]string{"legalEntityId": "must be a UUID"})
		return
	}
	a.respondTruckLevels(w, r, q.Get("legalEntityId"), strings.ToLower(q.Get("siteId")), strings.ToLower(q.Get("warehouseId")))
}

type truckReplenishRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	SiteID        string `json:"siteId"`
	WarehouseID   string `json:"warehouseId"`
	ShipDate      string `json:"shipDate"`
}

// replenishTrucks creates a draft transfer order for each truck below its minimum.
func (a *API) replenishTrucks(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) || a.truckUnavailable(w, r) {
		return
	}
	var req truckReplenishRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if (req.SiteID == "") != (req.WarehouseID == "") {
		fields["warehouseId"] = "siteId and warehouseId name one truck together, or neither names all trucks"
	}
	shipDate := time.Now().UTC().Truncate(24 * time.Hour)
	if req.ShipDate != "" {
		d, err := time.Parse(dateFormat, req.ShipDate)
		if err != nil {
			fields["shipDate"] = "must be a date (YYYY-MM-DD)"
		}
		shipDate = d
	}
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	orders, err := a.truck.ReplenishTrucks(r.Context(), req.LegalEntityID, strings.ToLower(req.SiteID), strings.ToLower(req.WarehouseID), shipDate)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := make([]transferResponse, 0, len(orders))
	for _, o := range orders {
		out = append(out, toTransferResponse(o))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"orders": out})
}
