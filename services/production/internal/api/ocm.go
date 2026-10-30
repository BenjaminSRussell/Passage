package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

// DutyApplyChange is "Apply order change" (PRD 4.21.17): previewing, applying and
// dismissing an OCM change. Applying to a Released order also needs
// production.order.bom-after-release, the duty of any BOM edit after release.
const DutyApplyChange = "production.order.apply-change"

const (
	maxEcoID        = 100
	maxChangeNote   = 500
	maxSurchargePct = 1000
)

// ---- OCM profiles ----

type ocmProfileList struct {
	LegalEntityID string             `json:"legalEntityId"`
	Items         []store.OCMProfile `json:"items"`
	Resolved      *domain.OCMProfile `json:"resolved,omitempty"`
}

func (a *API) listOCMProfiles(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	le := q.Get("legalEntityId")
	if le == "" {
		le = p.LegalEntityID
	}
	fields := map[string]string{}
	if !ids.Valid(le) {
		fields["legalEntityId"] = "must be a UUID (defaults to the caller's legal entity)"
	} else if !tenant.InLegalEntityScope(p, le) {
		fields["legalEntityId"] = "must be the legal entity of the caller"
	}
	site, group := q.Get("siteId"), q.Get("itemGroup")
	if site != "" && !ids.Valid(site) {
		fields["siteId"] = "must be a UUID"
	}
	if group != "" && site == "" {
		fields["siteId"] = "is required to resolve a profile"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	items, err := a.store.ListOCMProfiles(r.Context(), le)
	if err != nil {
		a.fail(w, r, "list OCM profiles", err)
		return
	}
	out := ocmProfileList{LegalEntityID: le, Items: items}
	if site != "" {
		profiles := make([]domain.OCMProfile, len(items))
		for i, it := range items {
			profiles[i] = it.OCMProfile
		}
		resolved := domain.ResolveOCMProfile(profiles, group, site)
		out.Resolved = &resolved
	}
	httpx.JSON(w, http.StatusOK, out)
}

type ocmProfileRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	domain.OCMProfile
}

func (a *API) saveOCMProfile(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	ifMatch := 0
	if raw := r.Header.Get("If-Match"); raw != "" {
		v, err := parseIfMatch(raw)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		ifMatch = v
	}
	var req ocmProfileRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.LegalEntityID == "" {
		req.LegalEntityID = p.LegalEntityID
	}
	if req.ItemGroup == "" {
		req.ItemGroup = domain.AllKey
	}
	if req.SiteID == "" {
		req.SiteID = domain.AllKey
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	} else if !tenant.InLegalEntityScope(p, req.LegalEntityID) {
		fields["legalEntityId"] = "must be the legal entity of the caller"
	}
	if len(req.ItemGroup) > maxItemGroup {
		fields["itemGroup"] = fmt.Sprintf("at most %d characters, or All", maxItemGroup)
	}
	if req.SiteID != domain.AllKey && !ids.Valid(req.SiteID) {
		fields["siteId"] = "must be a UUID or All"
	}
	if !req.Mode.Valid() {
		fields["mode"] = "must be automatic, manual or none"
	}
	checkQuantity("surchargeFixed", req.SurchargeFixed, false, fields)
	checkQuantity("surchargePct", req.SurchargePct, false, fields)
	if req.SurchargePct.GreaterThan(decimal.NewFromInt(maxSurchargePct)) {
		fields["surchargePct"] = fmt.Sprintf("at most %d", maxSurchargePct)
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	saved, created, err := a.store.SaveOCMProfile(r.Context(), store.OCMProfileSave{LegalEntityID: req.LegalEntityID, Profile: req.OCMProfile,
		IfMatch: ifMatch, Actor: p.UserID})
	if err != nil {
		a.fail(w, r, "save OCM profile", err)
		return
	}
	w.Header().Set("ETag", etag(saved.Version))
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, saved)
}

// ---- detection and worklist ----

type detectRequest struct {
	EcoID      string                 `json:"ecoId"`
	ProductID  string                 `json:"productId"`
	SiteID     string                 `json:"siteId"`
	BOM        *[]domain.BOMComponent `json:"bom"`
	Operations *[]domain.Operation    `json:"operations"`
}

// detectChanges records the open orders an ECO's new BOM and route supersede (FR-21.16).
func (a *API) detectChanges(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req detectRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if e := strings.TrimSpace(req.EcoID); e == "" || e != req.EcoID || len(e) > maxEcoID {
		fields["ecoId"] = fmt.Sprintf("is required, at most %d characters without surrounding spaces", maxEcoID)
	}
	if !ids.Valid(req.ProductID) {
		fields["productId"] = "must be a UUID"
	}
	if req.SiteID != "" && !ids.Valid(req.SiteID) {
		fields["siteId"] = "must be a UUID"
	}
	if req.BOM == nil && req.Operations == nil {
		fields["bom"] = "the ECO needs a new bom, operations or both"
	}
	if req.BOM != nil {
		validateBOMSnapshot(*req.BOM, fields)
	}
	if req.Operations != nil {
		if len(*req.Operations) == 0 || len(*req.Operations) > maxOperations {
			fields["operations"] = fmt.Sprintf("1 to %d operations", maxOperations)
		}
		validateRouteSnapshot(*req.Operations, fields)
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	res, err := a.store.DetectOrderChanges(r.Context(), store.ChangeDetect{EcoID: req.EcoID, ProductID: req.ProductID, SiteID: req.SiteID,
		BOM: req.BOM, Operations: req.Operations, Actor: p.UserID, TraceID: middleware.GetReqID(r.Context()), RequestHash: hashOf(req)})
	if err != nil {
		a.refuse(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

var changeStatuses = map[string]bool{"pending": true, "applied": true, "dismissed": true}

// listChanges serves the order change management worklist (PRD 4.21.14).
func (a *API) listChanges(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	f := store.ChangeFilter{Status: q.Get("status"), SiteID: q.Get("siteId"), OrderID: q.Get("orderId"), EcoID: q.Get("ecoId"), Cursor: q.Get("cursor")}
	if f.Status != "" && !changeStatuses[f.Status] {
		fields["status"] = "must be pending, applied or dismissed"
	}
	if f.SiteID != "" && !ids.Valid(f.SiteID) {
		fields["siteId"] = "must be a UUID"
	}
	if f.OrderID != "" && !ids.Valid(f.OrderID) {
		fields["orderId"] = "must be a UUID"
	}
	switch q.Get("needsReview") {
	case "":
	case "true", "false":
		v := q.Get("needsReview") == "true"
		f.NeedsReview = &v
	default:
		fields["needsReview"] = "must be true or false"
	}
	f.Limit = reportLimit(q.Get("limit"), fields)
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	page, err := a.store.ListOrderChanges(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list order changes", err)
		return
	}
	httpx.JSON(w, http.StatusOK, page)
}

func changeID(w http.ResponseWriter, r *http.Request, raw string) (string, bool) {
	if !ids.Valid(raw) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeChangeNotFound, "order change not found"))
		return "", false
	}
	return raw, true
}

func (a *API) getChange(w http.ResponseWriter, r *http.Request) {
	id, ok := changeID(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	c, err := a.store.GetOrderChange(r.Context(), id)
	if err != nil {
		a.fail(w, r, "read order change", err)
		return
	}
	w.Header().Set("ETag", etag(c.Version))
	httpx.JSON(w, http.StatusOK, c)
}

// changeCommand serves `{id}:preview`, `{id}:apply` and `{id}:dismiss`, all with the apply-change duty.
func (a *API) changeCommand(w http.ResponseWriter, r *http.Request) {
	raw, name, found := strings.Cut(chi.URLParam(r, "id"), ":")
	if !found || !ids.Valid(raw) || (name != "preview" && name != "apply" && name != "dismiss") {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, unknownActionCode, "use :preview, :apply or :dismiss on an order change"))
		return
	}
	authz.RequireDuty(DutyApplyChange)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch name {
		case "preview":
			a.previewChange(w, r, raw)
		case "apply":
			a.applyChange(w, r, raw)
		default:
			a.dismissChange(w, r, raw)
		}
	})).ServeHTTP(w, r)
}

func (a *API) previewChange(w http.ResponseWriter, r *http.Request, id string) {
	if _, ok := principal(w, r); !ok {
		return
	}
	if err := decodeOptional(r, &struct{}{}); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	pv, err := a.store.PreviewOrderChange(r.Context(), id)
	if err != nil {
		a.fail(w, r, "preview order change", err)
		return
	}
	w.Header().Set("ETag", etag(pv.Change.Version))
	httpx.JSON(w, http.StatusOK, pv)
}

func (a *API) applyChange(w http.ResponseWriter, r *http.Request, id string) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	ifMatch, err := parseIfMatch(r.Header.Get("If-Match"))
	if err != nil {
		a.refuse(w, r, err)
		return
	}
	if err := decodeOptional(r, &struct{}{}); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	key, fields := idempotencyKey(r)
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	res, err := a.store.ApplyOrderChange(r.Context(), store.ChangeApply{ChangeID: id, IfMatch: ifMatch, IdempotencyKey: key,
		ReleaseDuty: p.HasDuty(DutyBOMAfterRelease), Actor: p.UserID, TraceID: middleware.GetReqID(r.Context())})
	if err != nil {
		a.refuse(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(res.Change.Version))
	if res.Replayed {
		a.replayed.Add(1)
		w.Header().Set("Idempotent-Replayed", "true")
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"change": res.Change, "order": res.Order})
}

type dismissRequest struct {
	Note string `json:"note"`
}

func (a *API) dismissChange(w http.ResponseWriter, r *http.Request, id string) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	ifMatch, err := parseIfMatch(r.Header.Get("If-Match"))
	if err != nil {
		a.refuse(w, r, err)
		return
	}
	var req dismissRequest
	if err := decodeOptional(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if len(req.Note) > maxChangeNote {
		invalid(w, r, map[string]string{"note": fmt.Sprintf("at most %d characters", maxChangeNote)})
		return
	}
	c, err := a.store.DismissOrderChange(r.Context(), store.ChangeDismiss{ChangeID: id, IfMatch: ifMatch, Note: strings.TrimSpace(req.Note),
		Actor: p.UserID, TraceID: middleware.GetReqID(r.Context())})
	if err != nil {
		a.refuse(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(c.Version))
	httpx.JSON(w, http.StatusOK, c)
}
