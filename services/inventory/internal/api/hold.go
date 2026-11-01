package api

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

const (
	dutyHoldWrite = "inventory.hold.write"
	dutyHoldApply = "inventory.hold.apply"
	holdsPath     = "/v1/inventory/holds"
)

func (a *API) registerHolds(r chi.Router) {
	either := authz.RequireAnyDuty(dutyHoldWrite, dutyHoldApply)
	r.With(either).Post(holdsPath, a.placeHold)
	r.With(either).Post(holdsPath+":release-source", a.releaseHoldSource)
	r.With(authz.RequireDuty(dutyHoldWrite)).Post(holdsPath+"/{id}:release", a.releaseHold)
	r.With(authz.RequireDuty(dutyRead)).Get(holdsPath, a.listHolds)
}

type holdRequest struct {
	LegalEntityID string            `json:"legalEntityId"`
	ProductID     string            `json:"productId"`
	Dimensions    map[string]string `json:"dimensions"`
	Qty           string            `json:"qty"`
	WholeLot      bool              `json:"wholeLot"`
	AllowPartial  bool              `json:"allowPartial"`
	Reason        string            `json:"reason"`
	SourceType    string            `json:"sourceType"`
	SourceRef     string            `json:"sourceRef"`
	Note          string            `json:"note"`
}

type holdReleaseRequest struct {
	Qty *string `json:"qty"`
}

type holdSourceReleaseRequest struct {
	SourceType string `json:"sourceType"`
	SourceRef  string `json:"sourceRef"`
	KeepQty    string `json:"keepQty"`
}

type holdResponse struct {
	ID            string            `json:"id"`
	LegalEntityID string            `json:"legalEntityId"`
	ProductID     string            `json:"productId"`
	Dimensions    map[string]string `json:"dimensions"`
	Qty           string            `json:"qty"`
	ReleasedQty   string            `json:"releasedQty"`
	HeldQty       string            `json:"heldQty"`
	WholeLot      bool              `json:"wholeLot"`
	Reason        string            `json:"reason"`
	SourceType    string            `json:"sourceType"`
	SourceRef     string            `json:"sourceRef"`
	Note          string            `json:"note,omitempty"`
	State         string            `json:"state"`
	Version       int               `json:"version"`
}

func toHoldResponse(h domain.Hold) holdResponse {
	return holdResponse{ID: h.ID, LegalEntityID: h.LegalEntityID, ProductID: h.ProductID, Dimensions: h.Dims, Qty: h.Qty.String(),
		ReleasedQty: h.ReleasedQty.String(), HeldQty: h.Held().String(), WholeLot: h.WholeLot, Reason: h.Reason, SourceType: h.SourceType,
		SourceRef: h.SourceRef, Note: h.Note, State: h.State, Version: h.Version}
}

func holdItems(holds []domain.Hold) []holdResponse {
	items := make([]holdResponse, 0, len(holds))
	for _, h := range holds {
		items = append(items, toHoldResponse(h))
	}
	return items
}

func (a *API) placeHold(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req holdRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !ids.Valid(req.ProductID) {
		fields["productId"] = "must be a UUID"
	}
	qty := decimal.Zero
	switch {
	case req.WholeLot && req.Qty != "":
		fields["qty"] = "must be omitted for a whole-lot hold"
	case !req.WholeLot:
		var err error
		if qty, err = decimal.NewFromString(req.Qty); err != nil || !qty.IsPositive() {
			fields["qty"] = "must be a positive decimal number"
		}
	}
	if !slices.Contains(domain.BlockReasons, req.Reason) {
		fields["reason"] = "must be one of " + strings.Join(domain.BlockReasons, ", ")
	}
	if !domain.HoldSourceType.MatchString(req.SourceType) {
		fields["sourceType"] = "must be 2 to 32 characters of A-Z, 0-9 or _"
	}
	if n := len(req.SourceRef); n < 1 || n > 60 {
		fields["sourceRef"] = "must be 1 to 60 characters"
	}
	req.Note = strings.TrimSpace(req.Note)
	if utf8.RuneCountInString(req.Note) > maxNoteLength {
		fields["note"] = "must be at most 500 characters"
	}
	key := idempotencyKey(r, fields)
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	res, err := a.store.PlaceHold(r.Context(), store.HoldInput{IdempotencyKey: key, LegalEntityID: req.LegalEntityID, ProductID: req.ProductID,
		Dims: domain.Dimensions(req.Dimensions), Qty: qty, WholeLot: req.WholeLot, AllowPartial: req.AllowPartial, Reason: req.Reason,
		SourceType: req.SourceType, SourceRef: req.SourceRef, Note: req.Note})
	if err != nil {
		a.failed(w, r, "place hold", err)
		return
	}
	status := http.StatusCreated
	if res.Replayed {
		status = http.StatusOK
		w.Header().Set("Idempotent-Replayed", "true")
	}
	httpx.JSON(w, status, map[string]any{"items": holdItems(res.Holds), "shortfall": res.Shortfall.String()})
}

func (a *API) releaseHold(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeHoldNotFound, "hold not found"))
		return
	}
	version, err := ifMatchVersion(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var req holdReleaseRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var qty *decimal.Decimal
	if req.Qty != nil {
		q, err := decimal.NewFromString(*req.Qty)
		if err != nil || !q.IsPositive() {
			fieldProblem(w, r, map[string]string{"qty": "must be a positive decimal number"})
			return
		}
		qty = &q
	}
	h, err := a.store.ReleaseHold(r.Context(), id, version, qty)
	if err != nil {
		a.failed(w, r, "release hold", err)
		return
	}
	w.Header().Set("ETag", strconv.Quote(strconv.Itoa(h.Version)))
	httpx.JSON(w, http.StatusOK, toHoldResponse(h))
}

func (a *API) releaseHoldSource(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req holdSourceReleaseRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !domain.HoldSourceType.MatchString(req.SourceType) {
		fields["sourceType"] = "must be 2 to 32 characters of A-Z, 0-9 or _"
	}
	if n := len(req.SourceRef); n < 1 || n > 60 {
		fields["sourceRef"] = "must be 1 to 60 characters"
	}
	keep := decimal.Zero
	if req.KeepQty != "" {
		var err error
		if keep, err = decimal.NewFromString(req.KeepQty); err != nil || keep.Sign() < 0 {
			fields["keepQty"] = "must be a non-negative decimal number"
		}
	}
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	released, err := a.store.ReleaseHoldsBySource(r.Context(), req.SourceType, req.SourceRef, keep)
	if err != nil {
		a.failed(w, r, "release holds by source", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": holdItems(released)})
}

func (a *API) listHolds(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.HoldFilter{ProductID: q.Get("product"), SourceType: q.Get("sourceType"), SourceRef: q.Get("sourceRef"), State: q.Get("state")}
	fields := map[string]string{}
	if f.State != "" && f.State != domain.HoldActive && f.State != domain.HoldReleased {
		fields["state"] = "must be ACTIVE or RELEASED"
	}
	if f.ProductID != "" && !ids.Valid(f.ProductID) {
		fields["product"] = "must be a UUID"
	}
	f.Limit = pageLimit(r, 100, 200, fields)
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	found, err := a.store.ListHolds(r.Context(), f)
	if err != nil {
		a.failed(w, r, "list holds", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": holdItems(found)})
}
