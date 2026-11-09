package api

import (
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/issueclaim"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

const (
	dutyIssueClaim  = "inventory.issue.claim"
	issueClaimsPath = "/v1/inventory/issue-claims"
	maxScopeText    = 120
)

func (a *API) registerIssueClaims(r chi.Router) {
	claim := r.With(authz.RequireDuty(dutyIssueClaim))
	claim.Post(issueClaimsPath, a.claimIssue)
	claim.Post(issueClaimsPath+":release", a.releaseIssueClaim)
}

type issueClaimRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	ProductID     string `json:"productId"`
	Warehouse     string `json:"warehouse"`
	Batch         string `json:"batch"`
	Kind          string `json:"kind"`
	Qty           string `json:"qty"`
	// IssueRef names the document line the movement issues units for;
	// claims sharing it are successive steps of the same units.
	IssueRef string `json:"issueRef"`
}

func (a *API) claimIssue(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req issueClaimRequest
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
	if n := len(req.Warehouse); n < 1 || n > maxScopeText {
		fields["warehouse"] = "must be 1 to 120 characters"
	}
	if len(req.Batch) > maxScopeText {
		fields["batch"] = "must be at most 120 characters"
	}
	if !slices.Contains(domain.IssueKinds, req.Kind) {
		fields["kind"] = "must be one of " + strings.Join(domain.IssueKinds, ", ")
	}
	qty, err := decimal.NewFromString(req.Qty)
	if err != nil || !qty.IsPositive() {
		fields["qty"] = "must be a positive decimal number"
	}
	if req.IssueRef != "" && !issueclaim.CanonicalRef(req.IssueRef) {
		fields["issueRef"] = "must be SALES_LINE:<order line id> or PRODUCTION_ORDER:<order id>:<item id>, ids in lower case"
	}
	key := idempotencyKey(r, fields)
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	replayed, err := a.store.ClaimIssue(r.Context(), store.IssueClaimInput{IdempotencyKey: key, LegalEntityID: req.LegalEntityID,
		ProductID: req.ProductID, Warehouse: req.Warehouse, Batch: req.Batch, Kind: req.Kind, Qty: qty, IssueRef: req.IssueRef})
	if err != nil {
		a.failed(w, r, "claim issue", err)
		return
	}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
		w.Header().Set("Idempotent-Replayed", "true")
	}
	httpx.JSON(w, status, map[string]any{"claimed": true})
}

func (a *API) releaseIssueClaim(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	fields := map[string]string{}
	key := idempotencyKey(r, fields)
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	if err := a.store.ReleaseIssueClaim(r.Context(), key); err != nil {
		a.failed(w, r, "release issue claim", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
