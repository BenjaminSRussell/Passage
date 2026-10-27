package api

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/transport/internal/store"
)

var gitStatuses = []string{domain.GITPending, domain.GITInTransit}

type goodsInTransitRequest struct {
	VoyageID     string `json:"voyageId"`
	Incoterm     string `json:"incoterm"`
	SiteID       string `json:"siteId"`
	ReceiptLines []int  `json:"receiptLines"`
}

type shipRequest struct {
	ShippedAt string `json:"shippedAt"`
}

// createGoodsInTransit opens a goods-in-transit order for receipt lines of a
// voyage under an origin Incoterm (FR-34.12, BR-34.11).
func (a *API) createGoodsInTransit(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	var req goodsInTransitRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := domain.ValidateReceiptLineList(req.ReceiptLines)
	if !uuidRe.MatchString(req.VoyageID) {
		fields["voyageId"] = "must be a lower-case UUID"
	}
	if !uuidRe.MatchString(req.SiteID) {
		fields["siteId"] = "must be a lower-case UUID"
	}
	incoterm := strings.ToUpper(strings.TrimSpace(req.Incoterm))
	if _, err := domain.TransferFor(incoterm); errors.Is(err, domain.ErrUnknownIncoterm) {
		fields["incoterm"] = "must be an Incoterms 2020 rule: EXW, FCA, FAS, FOB, CFR, CIF, CPT, CIP, DAP, DPU or DDP"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	g, err := a.store.CreateGoodsInTransit(r.Context(), req.VoyageID, incoterm, req.SiteID, req.ReceiptLines, idem)
	if err != nil {
		a.fail(w, r, "create goods in transit failed", err)
		return
	}
	a.gitCreated.Add(1)
	httpx.JSON(w, http.StatusCreated, g)
}

func (a *API) listGoodsInTransit(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.GoodsInTransitFilter{VoyageID: q.Get("voyageId"), Status: q.Get("status")}
	fields := map[string]string{}
	checkOptionalID(fields, "voyageId", f.VoyageID)
	if f.Status != "" && !slices.Contains(gitStatuses, f.Status) {
		fields["status"] = "must be one of " + strings.Join(gitStatuses, ", ")
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	orders, err := a.store.ListGoodsInTransit(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list goods in transit failed", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": orders})
}

func (a *API) getGoodsInTransit(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		httpx.Fail(w, r, store.NotFound("goods-in-transit order"))
		return
	}
	g, err := a.store.GetGoodsInTransit(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get goods in transit failed", err)
		return
	}
	httpx.JSON(w, http.StatusOK, g)
}

// shipGoodsInTransit posts the order at the transfer event: ownership moves to
// the in-transit warehouse (TMS-0121). shippedAt is the departure the forwarder
// reported, default now. An order already IN_TRANSIT is returned unchanged.
func (a *API) shipGoodsInTransit(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		httpx.Fail(w, r, store.NotFound("goods-in-transit order"))
		return
	}
	var req shipRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	now := a.now()
	at := now
	if req.ShippedAt != "" {
		t, ok := parseInstant(req.ShippedAt)
		switch {
		case !ok:
			validationFailed(w, r, map[string]string{"shippedAt": "must be an RFC 3339 timestamp"})
			return
		case t.After(now.Add(clockSkew)):
			validationFailed(w, r, map[string]string{"shippedAt": "must not be in the future"})
			return
		}
		at = t
	}
	g, err := a.store.ShipGoodsInTransit(r.Context(), id, at)
	if err != nil {
		a.fail(w, r, "ship goods in transit failed", err)
		return
	}
	a.gitShipped.Add(1)
	httpx.JSON(w, http.StatusOK, g)
}
