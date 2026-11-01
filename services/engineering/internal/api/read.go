package api

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"sync/atomic"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
)

type requirementBody struct {
	Kind     string `json:"kind"`
	RefID    string `json:"refId"`
	Level    int    `json:"level"`
	Quantity int    `json:"quantity"`
}

type routeOperationBody struct {
	No            int               `json:"no"`
	OperationID   string            `json:"operationId,omitempty"`
	NextNo        int               `json:"nextNo"`
	LinkType      string            `json:"linkType"`
	Priority      string            `json:"priority"`
	SetupH        decimal.Decimal   `json:"setupH"`
	RunH          decimal.Decimal   `json:"runH"`
	ProcessQty    decimal.Decimal   `json:"processQty"`
	QueueBeforeH  decimal.Decimal   `json:"queueBeforeH"`
	QueueAfterH   decimal.Decimal   `json:"queueAfterH"`
	TransitH      decimal.Decimal   `json:"transitH"`
	EfficiencyPct decimal.Decimal   `json:"efficiencyPct"`
	OverlapQty    decimal.Decimal   `json:"overlapQty"`
	ScrapPercent  decimal.Decimal   `json:"scrapPercent"`
	Requirements  []requirementBody `json:"requirements"`
}

type routeVersionHeaderBody struct {
	ID             string `json:"id"`
	ProductID      string `json:"productId"`
	SiteID         string `json:"siteId"`
	RouteCode      string `json:"routeCode"`
	VersionNo      int    `json:"versionNo"`
	EffectiveFrom  string `json:"effectiveFrom"`
	EffectiveTo    string `json:"effectiveTo,omitempty"`
	Status         string `json:"status"`
	OperationCount int    `json:"operationCount"`
	CopiedFrom     string `json:"copiedFromVersionId,omitempty"`
	// Version is the draft's optimistic-locking counter (BR-9.3); PUT requires the value the editor loaded.
	Version int `json:"version,omitempty"`
}

type routeVersionBody struct {
	routeVersionHeaderBody
	Operations []routeOperationBody `json:"operations"`
}

func headerBody(v domain.RouteVersion, operations int) routeVersionHeaderBody {
	return routeVersionHeaderBody{
		ID: v.ID, ProductID: v.ProductID, SiteID: v.SiteID, RouteCode: v.RouteCode, VersionNo: v.VersionNo,
		EffectiveFrom: formatDate(v.EffectiveFrom), EffectiveTo: formatDate(v.EffectiveTo), Status: v.Status, OperationCount: operations, CopiedFrom: v.CopiedFrom, Version: v.Version,
	}
}

func bodyFromRouteVersion(v domain.RouteVersion) routeVersionBody {
	body := routeVersionBody{routeVersionHeaderBody: headerBody(v, len(v.Operations)), Operations: make([]routeOperationBody, len(v.Operations))}
	for i, o := range v.Operations {
		op := routeOperationBody{
			No: o.No, OperationID: o.OperationID, NextNo: o.NextNo, LinkType: o.LinkType, Priority: o.Priority, SetupH: o.SetupH, RunH: o.RunH,
			ProcessQty: o.ProcessQty, QueueBeforeH: o.QueueBeforeH, QueueAfterH: o.QueueAfterH, TransitH: o.TransitH, EfficiencyPct: o.EfficiencyPct,
			OverlapQty: o.OverlapQty, ScrapPercent: o.ScrapPercent, Requirements: make([]requirementBody, len(o.Requirements)),
		}
		for j, r := range o.Requirements {
			op.Requirements[j] = requirementBody{Kind: r.Kind, RefID: r.RefID, Level: r.Level, Quantity: r.Quantity}
		}
		body.Operations[i] = op
	}
	return body
}

func listPage[T, B any](a *API, w http.ResponseWriter, r *http.Request, operation string, failures *atomic.Int64,
	fetch func(ctx context.Context, after string, limit int) (store.Page[T], error), convert func(T) B) {
	limit, after, err := pageParams(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	result, err := fetch(r.Context(), after, limit)
	if err != nil {
		a.fail(w, r, operation, err, failures)
		return
	}
	body := page[B]{Items: make([]B, len(result.Items))}
	for i, item := range result.Items {
		body.Items[i] = convert(item)
	}
	if result.NextID != "" {
		body.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(result.NextID))
	}
	httpx.JSON(w, http.StatusOK, body)
}

func (a *API) listRouteVersions(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	f := store.RouteVersionFilter{ProductID: q.Get("productId"), SiteID: q.Get("siteId"), Status: q.Get("status")}
	for field, value := range map[string]string{"productId": f.ProductID, "siteId": f.SiteID} {
		if len(value) > maxIDLength {
			fields[field] = fmt.Sprintf("must be at most %d characters", maxIDLength)
		}
	}
	switch f.Status {
	case "", domain.StatusDraft, domain.StatusActive, domain.StatusExpired, domain.StatusCancelled:
	default:
		fields["status"] = "must be draft, active, expired or cancelled"
	}
	f.AsOf = optionalDate("asOf", q.Get("asOf"), fields)
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	listPage(a, w, r, "list route versions", &a.routeFailed, func(ctx context.Context, after string, limit int) (store.Page[store.RouteVersionHeader], error) {
		return a.store.ListRouteVersions(ctx, f, after, limit)
	}, func(h store.RouteVersionHeader) routeVersionHeaderBody { return headerBody(h.Version, h.Operations) })
}

func (a *API) activeRouteVersion(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	validID("productId", q.Get("productId"), fields)
	validID("siteId", q.Get("siteId"), fields)
	asOf := a.today()
	if raw := q.Get("asOf"); raw != "" {
		asOf = optionalDate("asOf", raw, fields)
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	v, err := a.store.ActiveRouteVersion(r.Context(), q.Get("productId"), q.Get("siteId"), asOf)
	if err != nil {
		a.fail(w, r, "resolve active route version", err, &a.routeFailed)
		return
	}
	httpx.JSON(w, http.StatusOK, bodyFromRouteVersion(v))
}

func (a *API) getRouteVersion(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	v, err := a.store.GetRouteVersion(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get route version", err, &a.routeFailed)
		return
	}
	httpx.JSON(w, http.StatusOK, bodyFromRouteVersion(v))
}

func (a *API) activateRouteVersion(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	v, err := a.store.ActivateRouteVersion(r.Context(), id)
	if err != nil {
		a.fail(w, r, "activate route version", err, &a.routeFailed)
		return
	}
	a.routeActivated.Add(1)
	httpx.JSON(w, http.StatusOK, bodyFromRouteVersion(v))
}

func (a *API) getBomVersion(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	v, err := a.store.GetBomVersion(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get bom version", err, &a.bomFailed)
		return
	}
	httpx.JSON(w, http.StatusOK, bodyFromVersion(v))
}

func (a *API) listEcos(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	state := r.URL.Query().Get("state")
	switch domain.EcoState(state) {
	case "", domain.EcoDraft, domain.EcoSubmitted, domain.EcoApproved, domain.EcoScheduled, domain.EcoReleased, domain.EcoRejected, domain.EcoCancelled:
	default:
		invalid(w, r, map[string]string{"state": "must be draft, submitted, approved, scheduled, released, rejected or cancelled"})
		return
	}
	listPage(a, w, r, "list ecos", &a.ecosFailed, func(ctx context.Context, after string, limit int) (store.Page[domain.Eco], error) {
		return a.store.ListEcos(ctx, state, after, limit)
	}, bodyFromEco)
}

func (a *API) getEco(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	record, err := a.store.GetEco(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get eco", err, &a.ecosFailed)
		return
	}
	httpx.JSON(w, http.StatusOK, bodyFromEco(record.Eco))
}

func (a *API) activateBomVersion(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	v, err := a.store.ActivateBomVersion(r.Context(), id, func(existing store.Snapshot, draft domain.BomVersion) error {
		draft.Status = domain.StatusActive
		return checkVersion(draft)(existing)
	})
	if err != nil {
		a.fail(w, r, "activate bom version", err, &a.bomFailed)
		return
	}
	a.bomActivated.Add(1)
	httpx.JSON(w, http.StatusOK, bodyFromVersion(v))
}

func (a *API) cancelBomVersion(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	v, err := a.store.CancelBomVersion(r.Context(), id)
	if err != nil {
		a.fail(w, r, "cancel bom version", err, &a.bomFailed)
		return
	}
	a.bomCancelled.Add(1)
	httpx.JSON(w, http.StatusOK, bodyFromVersion(v))
}
