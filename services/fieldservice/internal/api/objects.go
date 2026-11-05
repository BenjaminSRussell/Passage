package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/store"
)

// dutyObject guards installed-base writes (FR-45.1). The PRD duty table
// (8.45.17) names no installed-base duty; NOT-IMPLEMENTED.md hands the mapping
// to the policy owner.
const dutyObject = "fieldservice.service-object.maintain"

const (
	maxSerial          = 100
	maxSerialsPerLine  = 500
	maxShipmentSerials = 2000
)

var objectStatuses = []string{domain.ObjectActive, domain.ObjectInactive, domain.ObjectReturned}

func (a *API) registerObjects(r chi.Router) {
	r.With(authz.RequireDuty(dutyObject)).Post("/v1/service/service-objects", a.createServiceObject)
	r.With(authz.RequireDuty(dutyObject)).Post("/v1/service/service-objects:receive-shipment", a.receiveShipment)
	r.With(authz.RequireDuty(dutyInquire)).Get("/v1/service/service-objects", a.listServiceObjects)
	r.With(authz.RequireDuty(dutyInquire)).Get("/v1/service/service-objects/{id}", a.getServiceObject)
	r.With(authz.RequireDuty(dutyObject)).Post("/v1/service/service-objects/{id}:install", a.installServiceObject)
}

type serviceObjectRequest struct {
	ItemID          string              `json:"itemId"`
	Serial          string              `json:"serial"`
	CustomerPartyID string              `json:"customerPartyId"`
	LegalEntityID   string              `json:"legalEntityId"`
	SiteAddressID   *string             `json:"siteAddressId"`
	ParentID        *string             `json:"parentId"`
	ShippedOn       *string             `json:"shippedOn"`
	InstalledOn     *string             `json:"installedOn"`
	Warranty        domain.WarrantyTerm `json:"warranty"`
}

// createServiceObject registers one installed unit by hand (FR-45.1).
func (a *API) createServiceObject(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req serviceObjectRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	uuidField(fields, "itemId", req.ItemID)
	uuidField(fields, "customerPartyId", req.CustomerPartyID)
	uuidField(fields, "legalEntityId", req.LegalEntityID)
	serialField(fields, "serial", req.Serial)
	optionalUUID(fields, "siteAddressId", req.SiteAddressID)
	optionalUUID(fields, "parentId", req.ParentID)
	optionalDate(fields, "shippedOn", req.ShippedOn)
	optionalDate(fields, "installedOn", req.InstalledOn)
	warrantyField(fields, "warranty", req.Warranty)
	if req.Warranty.StartTrigger == domain.StartShip && req.Warranty.Months > 0 && req.ShippedOn == nil {
		fields["shippedOn"] = "is required for a warranty that starts at shipment"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	key, ok := idempotencyKeyOf(w, r)
	if !ok {
		return
	}
	obj, replayed, err := a.store.CreateServiceObject(r.Context(), store.ServiceObject{
		ItemID: req.ItemID, Serial: req.Serial, CustomerPartyID: req.CustomerPartyID, LegalEntityID: req.LegalEntityID,
		SiteAddressID: req.SiteAddressID, ParentID: req.ParentID, ShippedOn: req.ShippedOn, InstalledOn: req.InstalledOn,
		Warranty: req.Warranty,
	}, key, hashOf(req))
	if err != nil {
		a.fail(w, r, "create service object", err)
		return
	}
	setETag(w, obj.Version)
	a.respond(w, replayed, http.StatusCreated, obj)
}

func (a *API) getServiceObject(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", store.CodeObjectNotFound, "service object not found")
	if !ok {
		return
	}
	obj, err := a.store.GetServiceObject(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get service object", err)
		return
	}
	setETag(w, obj.Version)
	httpx.JSON(w, http.StatusOK, obj)
}

// listServiceObjects is the installed base explorer (PRD 8.45.6): search by
// customer, item, serial or parent, newest first.
func (a *API) listServiceObjects(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	for _, name := range []string{"customerPartyId", "itemId", "parentId"} {
		if v := q.Get(name); v != "" {
			uuidField(fields, name, v)
		}
	}
	if v := q.Get("status"); v != "" && !oneOf(v, objectStatuses...) {
		fields["status"] = "must be one of " + strings.Join(objectStatuses, ", ")
	}
	if len(q.Get("serial")) > maxSerial {
		fields["serial"] = fmt.Sprintf("must be at most %d characters", maxSerial)
	}
	cursor, limit := pageParams(fields, q.Get("cursor"), q.Get("limit"))
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	page, err := a.store.ListServiceObjects(r.Context(), store.ObjectFilter{Customer: q.Get("customerPartyId"), Item: q.Get("itemId"),
		Serial: strings.TrimSpace(q.Get("serial")), Parent: q.Get("parentId"), Status: q.Get("status"), Before: cursor, Limit: limit})
	if err != nil {
		a.fail(w, r, "list service objects", err)
		return
	}
	httpx.JSON(w, http.StatusOK, page)
}

type installRequest struct {
	InstalledOn   string  `json:"installedOn"`
	SiteAddressID *string `json:"siteAddressId"`
}

// installServiceObject records the installation day; an INSTALL warranty starts
// that day (FR-45.1, FR-45.2). If-Match carries the version the caller read.
func (a *API) installServiceObject(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", store.CodeObjectNotFound, "service object not found")
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var req installRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if _, ok := parseDate(req.InstalledOn); !ok {
		fields["installedOn"] = "must be a date as YYYY-MM-DD"
	}
	optionalUUID(fields, "siteAddressId", req.SiteAddressID)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	obj, err := a.store.InstallServiceObject(r.Context(), id, version, req.InstalledOn, req.SiteAddressID)
	if err != nil {
		a.fail(w, r, "install service object", err)
		return
	}
	setETag(w, obj.Version)
	httpx.JSON(w, http.StatusOK, obj)
}

type shipmentRequest struct {
	ShipmentID      string                `json:"shipmentId"`
	ShipDate        string                `json:"shipDate"`
	CustomerPartyID string                `json:"customerPartyId"`
	LegalEntityID   string                `json:"legalEntityId"`
	SiteAddressID   *string               `json:"siteAddressId"`
	Lines           []shipmentLineRequest `json:"lines"`
}

type shipmentLineRequest struct {
	ItemID   string              `json:"itemId"`
	Serials  []string            `json:"serials"`
	Warranty domain.WarrantyTerm `json:"warranty"`
}

// receiveShipment creates one service object per serial of a confirmed
// serialized shipment, the warranty starting on the ship day for the SHIP
// trigger (FR-45.1, AC-45.8). It stands in for the wave-28 shipment event; a
// re-delivered shipment creates nothing twice.
func (a *API) receiveShipment(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req shipmentRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	uuidField(fields, "shipmentId", req.ShipmentID)
	uuidField(fields, "customerPartyId", req.CustomerPartyID)
	uuidField(fields, "legalEntityId", req.LegalEntityID)
	optionalUUID(fields, "siteAddressId", req.SiteAddressID)
	shipDate, ok := parseDate(req.ShipDate)
	if !ok {
		fields["shipDate"] = "must be a date as YYYY-MM-DD"
	}
	if len(req.Lines) < 1 || len(req.Lines) > maxLines {
		fields["lines"] = fmt.Sprintf("must hold 1 to %d lines", maxLines)
	}
	seen := map[string]bool{}
	total := 0
	for i, ln := range req.Lines {
		name := "lines[" + strconv.Itoa(i) + "]"
		uuidField(fields, name+".itemId", ln.ItemID)
		if len(ln.Serials) < 1 || len(ln.Serials) > maxSerialsPerLine {
			fields[name+".serials"] = fmt.Sprintf("must hold 1 to %d serials", maxSerialsPerLine)
		}
		for _, s := range ln.Serials {
			total++
			serialField(fields, name+".serials", s)
			if seen[ln.ItemID+"|"+s] {
				fields[name+".serials"] = "serials must be unique per item"
			}
			seen[ln.ItemID+"|"+s] = true
		}
		warrantyField(fields, name+".warranty", ln.Warranty)
	}
	if total > maxShipmentSerials {
		fields["lines"] = fmt.Sprintf("must hold at most %d serials in all", maxShipmentSerials)
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	in := store.ShipmentReceipt{ShipmentID: req.ShipmentID, ShipDate: shipDate, CustomerPartyID: req.CustomerPartyID,
		LegalEntityID: req.LegalEntityID, SiteAddressID: req.SiteAddressID}
	for _, ln := range req.Lines {
		in.Lines = append(in.Lines, store.ShipmentLine{ItemID: ln.ItemID, Serials: ln.Serials, Warranty: ln.Warranty})
	}
	res, err := a.store.ReceiveShipment(r.Context(), in)
	if err != nil {
		a.fail(w, r, "receive shipment", err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

// --- helpers shared by the slice-3 handlers ---

// pageParams reads the cursor (a row id) and page size of a list.
func pageParams(fields map[string]string, cursor, rawLimit string) (string, int) {
	if cursor != "" && !ids.Valid(cursor) {
		fields["cursor"] = "must be a UUID"
	}
	limit := defaultPageSize
	if rawLimit != "" {
		n, err := strconv.Atoi(rawLimit)
		if err != nil || n < 1 || n > maxPageSize {
			fields["limit"] = fmt.Sprintf("must be 1 to %d", maxPageSize)
		}
		limit = n
	}
	return cursor, limit
}

func optionalDate(fields map[string]string, name string, v *string) {
	if v == nil {
		return
	}
	if _, ok := parseDate(*v); !ok {
		fields[name] = "must be a date as YYYY-MM-DD"
	}
}

// serialField checks a serial number: 1 to 100 characters without surrounding spaces.
func serialField(fields map[string]string, name, v string) {
	if v == "" || len([]rune(v)) > maxSerial || strings.TrimSpace(v) != v {
		fields[name] = fmt.Sprintf("must be 1 to %d characters with no surrounding spaces", maxSerial)
	}
}

func warrantyField(fields map[string]string, name string, w domain.WarrantyTerm) {
	if !w.Valid() {
		fields[name] = fmt.Sprintf("needs startTrigger SHIP, INSTALL or REGISTRATION and months 0 to %d", domain.MaxWarrantyMonths)
	}
}
