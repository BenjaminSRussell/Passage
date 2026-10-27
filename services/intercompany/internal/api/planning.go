package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/domain"
)

// ---- planning group run sequence (FR-40.15, BR-40.13, AC-40.16) ----

type planningMember struct {
	EntityID string `json:"entityId"`
	Sequence int    `json:"sequence"`
}

type planningEdge struct {
	Buyer        string `json:"buyer"`
	Supplier     string `json:"supplier"`
	LeadTimeDays int    `json:"leadTimeDays"`
}

type plannedOrder struct {
	Buyer         string `json:"buyer"`
	Supplier      string `json:"supplier"`
	Item          string `json:"item"`
	Quantity      string `json:"quantity"`
	RequestedDate string `json:"requestedDate"`
}

type planningRequest struct {
	Members        []planningMember `json:"members"`
	Edges          []planningEdge   `json:"edges"`
	PlannedOrders  []plannedOrder   `json:"plannedOrders"`
	ReplannedBuyer string           `json:"replannedBuyer"`
}

type icDemand struct {
	Buyer    string `json:"buyer"`
	Supplier string `json:"supplier"`
	Item     string `json:"item"`
	Quantity string `json:"quantity"`
	DueDate  string `json:"dueDate"`
}

type warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type planningResponse struct {
	Order          []string   `json:"order"`
	Demand         []icDemand `json:"demand"`
	StaleSuppliers []string   `json:"staleSuppliers"`
	Warnings       []warning  `json:"warnings"`
}

func (a *API) sequencePlanning(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	var req planningRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	members, edges, orders, fields := parsePlanning(req)
	if !validation(w, r, fields) {
		return
	}
	order, err := domain.PlanRunOrder(members, edges)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	out := planningResponse{Order: order, Demand: make([]icDemand, 0, len(orders)), StaleSuppliers: []string{}, Warnings: []warning{}}
	for _, o := range orders {
		d, err := domain.ICDemandFor(o, edges)
		if err != nil {
			httpx.Fail(w, r, problemFrom(err))
			return
		}
		out.Demand = append(out.Demand, icDemand{Buyer: o.Buyer, Supplier: d.Supplier, Item: d.Item, Quantity: d.Quantity.String(), DueDate: d.DueDate.Format(dateLayout)})
	}
	if req.ReplannedBuyer != "" {
		out.StaleSuppliers = domain.StaleSuppliers(order, edges, req.ReplannedBuyer)
		for _, s := range out.StaleSuppliers {
			out.Warnings = append(out.Warnings, warning{Code: domain.CodeStalePlan, Message: fmt.Sprintf("Supplier plan for %s is stale after buyer replanning", s)})
		}
	}
	a.planningSequences.Add(1)
	httpx.JSON(w, http.StatusOK, out)
}

func parsePlanning(req planningRequest) ([]domain.PlanningMember, []domain.PlanningEdge, []domain.PlannedICOrder, map[string]string) {
	f := fieldParser{}
	switch {
	case len(req.Members) == 0:
		f["members"] = "must contain at least one entity"
	case len(req.Members) > MaxPlanningMembers:
		f["members"] = fmt.Sprintf("must contain at most %d entities", MaxPlanningMembers)
	}
	if len(req.Edges) > MaxPlanningRows {
		f["edges"] = fmt.Sprintf("must contain at most %d edges", MaxPlanningRows)
	}
	if len(req.PlannedOrders) > MaxPlanningRows {
		f["plannedOrders"] = fmt.Sprintf("must contain at most %d orders", MaxPlanningRows)
	}
	if len(f) > 0 {
		return nil, nil, nil, f
	}
	known := map[string]bool{}
	members := make([]domain.PlanningMember, 0, len(req.Members))
	for i, m := range req.Members {
		id := f.text(fmt.Sprintf("members[%d].entityId", i), m.EntityID)
		if id != "" && known[id] {
			f[fmt.Sprintf("members[%d].entityId", i)] = "appears more than once"
		}
		known[id] = true
		if m.Sequence < 0 {
			f[fmt.Sprintf("members[%d].sequence", i)] = "must not be negative"
		}
		members = append(members, domain.PlanningMember{EntityID: id, Sequence: m.Sequence})
	}
	member := func(name, v string) string {
		v = f.text(name, v)
		if v != "" && !known[v] {
			f[name] = "must be a member of the planning group"
		}
		return v
	}
	edges := make([]domain.PlanningEdge, 0, len(req.Edges))
	for i, e := range req.Edges {
		at := func(name string) string { return fmt.Sprintf("edges[%d].%s", i, name) }
		buyer, supplier := member(at("buyer"), e.Buyer), member(at("supplier"), e.Supplier)
		if buyer != "" && buyer == supplier {
			f[at("supplier")] = "must differ from buyer"
		}
		if e.LeadTimeDays < 0 {
			f[at("leadTimeDays")] = "must not be negative"
		}
		edges = append(edges, domain.PlanningEdge{Buyer: buyer, Supplier: supplier, LeadTimeDays: e.LeadTimeDays})
	}
	hasEdge := func(b, s string) bool {
		for _, e := range edges {
			if e.Buyer == b && e.Supplier == s {
				return true
			}
		}
		return false
	}
	orders := make([]domain.PlannedICOrder, 0, len(req.PlannedOrders))
	for i, o := range req.PlannedOrders {
		at := func(name string) string { return fmt.Sprintf("plannedOrders[%d].%s", i, name) }
		buyer, supplier := member(at("buyer"), o.Buyer), member(at("supplier"), o.Supplier)
		if _, bad := f[at("supplier")]; !bad && buyer != "" && supplier != "" && !hasEdge(buyer, supplier) {
			f[at("supplier")] = "has no planning edge from the buyer"
		}
		var qty decimal.Decimal
		if amountText.MatchString(o.Quantity) {
			qty = decimal.RequireFromString(o.Quantity)
		}
		if !qty.IsPositive() {
			f[at("quantity")] = "must be a positive decimal with at most 6 decimals"
		}
		orders = append(orders, domain.PlannedICOrder{
			Buyer: buyer, Supplier: supplier, Item: f.text(at("item"), o.Item), Quantity: qty,
			RequestedDate: f.date(at("requestedDate"), o.RequestedDate),
		})
	}
	if rb := req.ReplannedBuyer; rb != "" && (strings.TrimSpace(rb) != rb || !known[rb]) {
		f["replannedBuyer"] = "must be a member of the planning group"
	}
	return members, edges, orders, f
}
