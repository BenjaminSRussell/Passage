package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/costacct/internal/domain"
	"github.com/BenjaminSRussell/passage/services/costacct/internal/store"
)

// registerHierarchies mounts the cost object hierarchy routes (FR-38.18).
// Hierarchies and nodes are master data (CostAccountingMaintain); collecting a
// cost and distributing a node post cost entries (CostAccountingRun).
func (a *API) registerHierarchies(r chi.Router) {
	maintain := r.With(authz.RequireDuty(DutyMaintain))
	maintain.Post("/v1/controlling/cost-object-hierarchies", a.createHierarchy)
	maintain.Post("/v1/controlling/cost-object-hierarchies/{id}/nodes", a.createHierarchyNode)
	run := r.With(authz.RequireDuty(DutyRun))
	run.Post("/v1/controlling/hierarchy-nodes/{id}:collect", a.collectAtNode)
	run.Post("/v1/controlling/hierarchy-distributions", a.runHierarchyDistribution)
	view := r.With(authz.RequireDuty(DutyView))
	view.Get("/v1/controlling/cost-object-hierarchies", a.listHierarchies)
	view.Get("/v1/controlling/cost-object-hierarchies/{id}", a.getHierarchy)
	view.Get("/v1/controlling/cost-object-hierarchies/{id}/balances", a.hierarchyBalances)
	view.Get("/v1/controlling/hierarchy-distributions", a.listDistributionRuns)
	view.Get("/v1/controlling/hierarchy-distributions/{id}", a.getDistributionRun)
}

// hierarchyBody is the wire form of a new hierarchy.
type hierarchyBody struct {
	LegalEntityID string `json:"legalEntityId"`
	Code          string `json:"code"`
	Name          string `json:"name"`
	Type          string `json:"type"`
}

func (a *API) createHierarchy(w http.ResponseWriter, r *http.Request) {
	var b hierarchyBody
	key, hash, ok := prepare(w, r, &b)
	if !ok {
		return
	}
	switch {
	case !ids.Valid(b.LegalEntityID):
		invalid(w, r, "legalEntityId must be a UUID")
	case !domain.ValidCode(b.Code):
		invalid(w, r, "code must be 2-40 characters of A-Z, 0-9, _ or -")
	case b.Name == "" || len(b.Name) > 200:
		invalid(w, r, "name is required and at most 200 characters")
	case !domain.ValidHierarchyType(b.Type):
		invalid(w, r, "type must be Reporting or Collective")
	default:
		status, raw, err := a.st.CreateHierarchy(r.Context(), key, hash, store.Hierarchy{LegalEntityID: b.LegalEntityID, Code: b.Code, Name: b.Name, Type: b.Type})
		writeCommand(w, r, status, raw, err)
	}
}

// nodeBody is the wire form of a new hierarchy node.
type nodeBody struct {
	ParentNodeID      string `json:"parentNodeId"`
	CostObjectID      string `json:"costObjectId"`
	Collects          bool   `json:"collects"`
	DistributionBasis string `json:"distributionBasis"`
}

func (a *API) createHierarchyNode(w http.ResponseWriter, r *http.Request) {
	var b nodeBody
	key, hash, ok := prepare(w, r, &b)
	if !ok {
		return
	}
	hierarchyID := chi.URLParam(r, "id")
	switch {
	case !ids.Valid(hierarchyID):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeHierarchyNotFound, "cost object hierarchy not found"))
	case !ids.Valid(b.CostObjectID):
		invalid(w, r, "costObjectId must be a UUID")
	case b.ParentNodeID != "" && !ids.Valid(b.ParentNodeID):
		invalid(w, r, "parentNodeId must be a UUID when set")
	case b.Collects && !domain.ValidDistributionBasis(b.DistributionBasis):
		invalid(w, r, "a collecting node needs distributionBasis ActualOutput, StandardCost or FixedPercent")
	case !b.Collects && b.DistributionBasis != "":
		invalid(w, r, "only a collecting node has a distributionBasis")
	default:
		status, raw, err := a.st.CreateHierarchyNode(r.Context(), key, hash, store.HierarchyNode{
			HierarchyID: hierarchyID, ParentNodeID: b.ParentNodeID, CostObjectID: b.CostObjectID, Collects: b.Collects, DistributionBasis: b.DistributionBasis,
		})
		writeCommand(w, r, status, raw, err)
	}
}

// collectBody is the wire form of a collection on a node.
type collectBody struct {
	Period        string          `json:"period"`
	Currency      string          `json:"currency"`
	CostElementID string          `json:"costElementId"`
	Amount        decimal.Decimal `json:"amount"`
}

func (a *API) collectAtNode(w http.ResponseWriter, r *http.Request) {
	var b collectBody
	key, hash, ok := prepare(w, r, &b)
	if !ok {
		return
	}
	nodeID := chi.URLParam(r, "id")
	switch {
	case !ids.Valid(nodeID):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeNodeNotFound, "hierarchy node not found"))
	case !ids.Valid(b.CostElementID):
		invalid(w, r, "costElementId must be a UUID")
	case !domain.ValidPeriod(b.Period):
		invalid(w, r, "period must be YYYY-MM")
	case !validCurrency(b.Currency):
		invalid(w, r, "currency must be a 3-letter ISO code")
	case b.Amount.IsZero() || b.Amount.Exponent() < -money.MinorUnits(b.Currency):
		invalid(w, r, "amount must be non-zero with no more decimals than the currency's minor unit; a negative amount corrects a collection")
	default:
		status, raw, err := a.st.CollectAtNode(r.Context(), key, hash, store.CollectRequest{
			NodeID: nodeID, Period: b.Period, Currency: b.Currency, CostElementID: b.CostElementID, Amount: b.Amount,
		})
		writeCommand(w, r, status, raw, err)
	}
}

// distributionBody is the wire form of a distribution run. Each child carries
// its basis: actual output quantity, output at standard cost, or a percentage.
type distributionBody struct {
	NodeID        string                  `json:"nodeId"`
	Period        string                  `json:"period"`
	Currency      string                  `json:"currency"`
	CostElementID string                  `json:"costElementId"`
	Children      []domain.HierarchyChild `json:"children"`
}

func (a *API) runHierarchyDistribution(w http.ResponseWriter, r *http.Request) {
	var b distributionBody
	key, hash, ok := prepare(w, r, &b)
	if !ok {
		return
	}
	switch {
	case !ids.Valid(b.NodeID) || !ids.Valid(b.CostElementID):
		invalid(w, r, "nodeId and costElementId must be UUIDs")
		return
	case !domain.ValidPeriod(b.Period):
		invalid(w, r, "period must be YYYY-MM")
		return
	case !validCurrency(b.Currency):
		invalid(w, r, "currency must be a 3-letter ISO code")
		return
	case len(b.Children) < 1 || len(b.Children) > maxObjects:
		invalid(w, r, "children must hold 1-500 entries")
		return
	}
	seen := map[string]bool{b.NodeID: true}
	for i, c := range b.Children {
		if !ids.Valid(c.NodeID) || seen[c.NodeID] {
			invalid(w, r, lineErr(i, "children must be distinct node UUIDs other than the distributing node"))
			return
		}
		seen[c.NodeID] = true
		if c.Basis.IsNegative() || c.Basis.Exponent() < -6 {
			invalid(w, r, lineErr(i, "basis must not be negative and has at most 6 decimal places"))
			return
		}
	}
	status, raw, err := a.st.RunHierarchyDistribution(r.Context(), key, hash, store.DistributionRequest{
		NodeID: b.NodeID, Period: b.Period, Currency: b.Currency, CostElementID: b.CostElementID, Children: b.Children,
	})
	writeCommand(w, r, status, raw, err)
}

func (a *API) listHierarchies(w http.ResponseWriter, r *http.Request) {
	le, ok := legalEntityFilter(w, r)
	if !ok {
		return
	}
	rows, err := a.st.ListHierarchies(r.Context(), le)
	list(w, r, rows, err)
}

// hierarchyID reads the {id} path parameter; a malformed id is a 404.
func hierarchyID(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !validTenant(w, r) {
		return "", false
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeHierarchyNotFound, "cost object hierarchy not found"))
		return "", false
	}
	return id, true
}

func (a *API) getHierarchy(w http.ResponseWriter, r *http.Request) {
	id, ok := hierarchyID(w, r)
	if !ok {
		return
	}
	h, err := a.st.GetHierarchy(r.Context(), id)
	one(w, r, h, err)
}

func (a *API) hierarchyBalances(w http.ResponseWriter, r *http.Request) {
	id, ok := hierarchyID(w, r)
	if !ok {
		return
	}
	period := r.URL.Query().Get("period")
	if !domain.ValidPeriod(period) {
		invalid(w, r, "period must be YYYY-MM")
		return
	}
	rows, err := a.st.HierarchyBalances(r.Context(), id, period)
	list(w, r, rows, err)
}

func (a *API) listDistributionRuns(w http.ResponseWriter, r *http.Request) {
	le, ok := legalEntityFilter(w, r)
	if !ok {
		return
	}
	h := r.URL.Query().Get("hierarchyId")
	if h != "" && !ids.Valid(h) {
		invalid(w, r, "hierarchyId must be a UUID")
		return
	}
	rows, err := a.st.ListDistributionRuns(r.Context(), le, h)
	list(w, r, rows, err)
}

func (a *API) getDistributionRun(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeHierarchyRunNotFound, "hierarchy distribution run not found"))
		return
	}
	run, err := a.st.GetDistributionRun(r.Context(), id)
	one(w, r, run, err)
}
