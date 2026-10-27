package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/eam/internal/domain"
	"github.com/BenjaminSRussell/passage/services/eam/internal/store"
)

type lifecycleIn struct {
	To string `json:"to"`
}

// changeLifecycle retires an asset to INACTIVE or DISPOSED (FR-44.2). Activation keeps its own route.
func (h *Handler) changeLifecycle(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	version, err := ifMatch(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body lifecycleIn
	if err := decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	if body.To != "INACTIVE" && body.To != "DISPOSED" {
		fail(w, r, invalid("to must be INACTIVE or DISPOSED; use /activate to return an asset to ACTIVE"))
		return
	}
	out, err := h.store.ChangeAssetLifecycle(r.Context(), id, body.To, version)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

type criticalityIn struct {
	Consequence domain.Consequences `json:"consequence"`
	Likelihood  int                 `json:"likelihood"`
}

// assessCriticality records a new criticality assessment version (FR-44.22, BR-44.15).
func (h *Handler) assessCriticality(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body criticalityIn
	if err := decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	if _, err := domain.RiskScore(body.Consequence, body.Likelihood); err != nil {
		fail(w, r, invalid("every consequence and the likelihood must be scored 1-5"))
		return
	}
	p, _ := tenant.FromContext(r.Context())
	out, err := h.store.AssessCriticality(r.Context(), id, body.Consequence, body.Likelihood, p.UserID, key)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (h *Handler) listAssessments(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := h.store.ListAssessments(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

type warrantyIn struct {
	WarrantyType  string   `json:"warrantyType"`
	VendorPartyID string   `json:"vendorPartyId"`
	StartBasis    string   `json:"startBasis"`
	StartsOn      string   `json:"startsOn"`
	Months        int      `json:"months"`
	PartsCovered  bool     `json:"partsCovered"`
	LaborCovered  bool     `json:"laborCovered"`
	Exclusions    []string `json:"exclusions,omitempty"`
}

// createWarranty records a vendor warranty on an asset (FR-44.17).
func (h *Handler) createWarranty(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var in warrantyIn
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	_, dateErr := time.Parse("2006-01-02", in.StartsOn)
	switch {
	case !assetType.MatchString(in.WarrantyType):
		fail(w, r, invalid("warrantyType must match ^[A-Z0-9_]{2,20}$"))
		return
	case !ids.Valid(in.VendorPartyID):
		fail(w, r, invalid("vendorPartyId must be a UUID"))
		return
	case !domain.ValidStartBasis(in.StartBasis):
		fail(w, r, invalid("startBasis must be PURCHASE or INSTALL"))
		return
	case dateErr != nil:
		fail(w, r, invalid("startsOn must be a date (YYYY-MM-DD)"))
		return
	case in.Months < 1 || in.Months > 240:
		fail(w, r, invalid("months must be 1-240"))
		return
	case !in.PartsCovered && !in.LaborCovered:
		fail(w, r, invalid("a warranty covers parts, labor or both"))
		return
	case len(in.Exclusions) > 20:
		fail(w, r, invalid("at most 20 excluded job types"))
		return
	}
	for _, ex := range in.Exclusions {
		if !assetType.MatchString(ex) {
			fail(w, r, invalid("excluded job types must match ^[A-Z0-9_]{2,20}$"))
			return
		}
	}
	out, err := h.store.CreateWarranty(r.Context(), id, store.Warranty{WarrantyType: in.WarrantyType,
		VendorPartyID: strings.ToLower(in.VendorPartyID), StartBasis: in.StartBasis, StartsOn: in.StartsOn,
		Months: in.Months, PartsCovered: in.PartsCovered, LaborCovered: in.LaborCovered, Exclusions: in.Exclusions}, key)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (h *Handler) listWarranties(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := h.store.ListWarranties(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}
