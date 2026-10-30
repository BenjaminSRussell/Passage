package api

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

// quotaSourceRecordDTO is a stored source of a quota arrangement. Only the
// reference matching the source type is set; the others are null.
type quotaSourceRecordDTO struct {
	ID                  string  `json:"id"`
	Type                string  `json:"type"`
	VendorID            *string `json:"vendorId"`
	ProductionVersionID *string `json:"productionVersionId"`
	FromWarehouseID     *string `json:"fromWarehouseId"`
	Quota               string  `json:"quota"`
	BaseQty             string  `json:"baseQty"`
	AllocatedQty        string  `json:"allocatedQty"`
	MaxQtyPerPeriod     *string `json:"maxQtyPerPeriod"`
	PeriodDays          *int    `json:"periodDays"`
	Priority            int     `json:"priority"`
	Blocked             bool    `json:"blocked"`
	// Rating is (allocated + base) / quota rounded to 4 places (BR-22.12);
	// null for a quota-0 source, which is a fallback only.
	Rating *string `json:"rating"`
}

// quotaArrangementDTO is the wire form of a quota arrangement (FR-22.17).
// Version is also sent as the ETag.
type quotaArrangementDTO struct {
	ID          string                 `json:"id"`
	Version     int                    `json:"version"`
	ItemID      string                 `json:"itemId"`
	SiteID      string                 `json:"siteId"`
	ValidFrom   string                 `json:"validFrom"`
	ValidTo     string                 `json:"validTo"`
	Split       bool                   `json:"split"`
	MinSplitQty string                 `json:"minSplitQty"`
	Sources     []quotaSourceRecordDTO `json:"sources"`
}

func quotaArrangementDTOOf(a store.QuotaArrangement) quotaArrangementDTO {
	s := a.Spec
	out := quotaArrangementDTO{ID: a.ID, Version: a.Version, ItemID: s.ItemID, SiteID: s.SiteID,
		ValidFrom: s.ValidFrom.Format(dateLayout), ValidTo: s.ValidTo.Format(dateLayout), Split: s.Split,
		MinSplitQty: s.MinSplitQty.String(), Sources: make([]quotaSourceRecordDTO, 0, len(s.Sources))}
	for _, src := range s.Sources {
		dto := quotaSourceRecordDTO{ID: src.ID, Type: src.Type, VendorID: optionalString(src.VendorID),
			ProductionVersionID: optionalString(src.ProductionVersionID), FromWarehouseID: optionalString(src.FromWarehouseID),
			Quota: src.Quota.String(), BaseQty: src.BaseQty.String(), AllocatedQty: src.Allocated.String(),
			PeriodDays: src.PeriodDays, Priority: src.Priority, Blocked: src.Blocked}
		if src.MaxQtyPerPeriod != nil {
			v := src.MaxQtyPerPeriod.String()
			dto.MaxQtyPerPeriod = &v
		}
		if src.Quota.IsPositive() {
			v := domain.Rating(src.Calculator()).Round(4).String()
			dto.Rating = &v
		}
		out.Sources = append(out.Sources, dto)
	}
	return out
}

func writeQuotaArrangement(w http.ResponseWriter, status int, a store.QuotaArrangement) {
	w.Header().Set("ETag", etagOf(a.Version))
	httpx.JSON(w, status, quotaArrangementDTOOf(a))
}

// quotaSourceInput is one source of an arrangement. Quantities are decimal
// strings; allocatedQty may be carried over from a legacy system (SAP EQUP).
type quotaSourceInput struct {
	Type                string           `json:"type"`
	VendorID            string           `json:"vendorId"`
	ProductionVersionID string           `json:"productionVersionId"`
	FromWarehouseID     string           `json:"fromWarehouseId"`
	Quota               decimal.Decimal  `json:"quota"`
	BaseQty             decimal.Decimal  `json:"baseQty"`
	AllocatedQty        decimal.Decimal  `json:"allocatedQty"`
	MaxQtyPerPeriod     *decimal.Decimal `json:"maxQtyPerPeriod"`
	PeriodDays          *int             `json:"periodDays"`
	Priority            int              `json:"priority"`
	Blocked             bool             `json:"blocked"`
}

// quotaArrangementRequest is the body of a create or a full update. Dates are
// YYYY-MM-DD and inclusive.
type quotaArrangementRequest struct {
	LegalEntityID string             `json:"legalEntityId"`
	ItemID        string             `json:"itemId"`
	SiteID        string             `json:"siteId"`
	ValidFrom     string             `json:"validFrom"`
	ValidTo       string             `json:"validTo"`
	Split         bool               `json:"split"`
	MinSplitQty   decimal.Decimal    `json:"minSplitQty"`
	Sources       []quotaSourceInput `json:"sources"`
}

// decodeQuotaArrangement reads and validates the body; it answers the request
// itself and returns false when the body is unusable.
func decodeQuotaArrangement(w http.ResponseWriter, r *http.Request) (domain.QuotaArrangementSpec, string, bool) {
	var req quotaArrangementRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return domain.QuotaArrangementSpec{}, "", false
	}
	// An unparsable date stays zero, which Validate reports.
	from, _ := time.Parse(dateLayout, req.ValidFrom)
	to, _ := time.Parse(dateLayout, req.ValidTo)
	spec := domain.QuotaArrangementSpec{ItemID: req.ItemID, SiteID: req.SiteID, ValidFrom: from, ValidTo: to,
		Split: req.Split, MinSplitQty: req.MinSplitQty, Sources: make([]domain.QuotaSourceSpec, 0, len(req.Sources))}
	for _, s := range req.Sources {
		spec.Sources = append(spec.Sources, domain.QuotaSourceSpec{Type: s.Type, VendorID: s.VendorID,
			ProductionVersionID: s.ProductionVersionID, FromWarehouseID: s.FromWarehouseID, Quota: s.Quota,
			BaseQty: s.BaseQty, Allocated: s.AllocatedQty, MaxQtyPerPeriod: s.MaxQtyPerPeriod, PeriodDays: s.PeriodDays,
			Priority: s.Priority, Blocked: s.Blocked})
	}
	if fields := spec.Validate(); len(fields) > 0 {
		validationFailed(w, r, fields)
		return domain.QuotaArrangementSpec{}, "", false
	}
	return spec, req.LegalEntityID, true
}

// quotaArrangementID reads {id}; an ill-formed id cannot exist, so it is 404.
func quotaArrangementID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeQuotaNotFound, "quota arrangement not found"))
		return "", false
	}
	return id, true
}

// listQuotaArrangements returns the tenant's arrangements, optionally of one
// item, one site and valid on one date.
func (a *API) listQuotaArrangements(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	query := store.QuotaQuery{ItemID: q.Get("itemId"), SiteID: q.Get("siteId")}
	fields := map[string]string{}
	if query.ItemID != "" && !ids.Valid(query.ItemID) {
		fields["itemId"] = "must be a UUID"
	}
	if query.SiteID != "" && !ids.Valid(query.SiteID) {
		fields["siteId"] = "must be a UUID"
	}
	if raw := q.Get("validOn"); raw != "" {
		d, err := time.Parse(dateLayout, raw)
		if err != nil {
			fields["validOn"] = "must be a date as YYYY-MM-DD"
		} else {
			query.ValidOn = &d
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	query.Limit = limit
	arrangements, err := a.store.ListQuotaArrangements(r.Context(), query)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := make([]quotaArrangementDTO, 0, len(arrangements))
	for _, arr := range arrangements {
		out = append(out, quotaArrangementDTOOf(arr))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (a *API) getQuotaArrangement(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := quotaArrangementID(w, r)
	if !ok {
		return
	}
	arr, err := a.store.GetQuotaArrangement(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	writeQuotaArrangement(w, http.StatusOK, arr)
}

// createQuotaArrangement stores a new arrangement (Maintain planning setup).
// An overlapping validity period of the same item and site is 409
// PLAN-QUOTA-OVERLAP. An optional Idempotency-Key makes a retry return the
// first result with 200 and Idempotent-Replayed: true.
func (a *API) createQuotaArrangement(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key := r.Header.Get(idempotencyKey)
	if len(key) > 255 {
		validationFailed(w, r, map[string]string{idempotencyKey: "must be at most 255 characters"})
		return
	}
	spec, legalEntityID, ok := decodeQuotaArrangement(w, r)
	if !ok {
		return
	}
	body, _ := json.Marshal(spec)
	arr, replayed, err := a.store.CreateQuotaArrangement(r.Context(), store.QuotaCreate{
		LegalEntityID: legalEntityID, IdempotencyKey: key, InputHash: fmt.Sprintf("%x", sha256.Sum256(body)), Spec: spec,
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
		writeQuotaArrangement(w, http.StatusOK, arr)
		return
	}
	writeQuotaArrangement(w, http.StatusCreated, arr)
}

// updateQuotaArrangement replaces an arrangement and its sources under If-Match.
func (a *API) updateQuotaArrangement(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := quotaArrangementID(w, r)
	if !ok {
		return
	}
	expected, ok := ifMatch(w, r, 1)
	if !ok {
		return
	}
	spec, _, ok := decodeQuotaArrangement(w, r)
	if !ok {
		return
	}
	arr, err := a.store.UpdateQuotaArrangement(r.Context(), id, expected, spec)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	writeQuotaArrangement(w, http.StatusOK, arr)
}

// registerQuotaArrangements mounts the quota arrangement routes (FR-22.17),
// planning setup under 4.22.17.
func (a *API) registerQuotaArrangements(r chi.Router) {
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/planning/quota-arrangements", a.listQuotaArrangements)
	r.With(authz.RequireDuty(dutyMaintainSetup)).Post("/v1/planning/quota-arrangements", a.createQuotaArrangement)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/planning/quota-arrangements/{id}", a.getQuotaArrangement)
	r.With(authz.RequireDuty(dutyMaintainSetup)).Put("/v1/planning/quota-arrangements/{id}", a.updateQuotaArrangement)
}
