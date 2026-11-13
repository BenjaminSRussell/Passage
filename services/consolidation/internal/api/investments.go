package api

import (
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/consolidation/internal/domain"
	"github.com/BenjaminSRussell/passage/services/consolidation/internal/store"
)

// MaxLayerRows bounds one request that replaces a unit's IC layers.
const MaxLayerRows = store.MaxLayers

type icLayerBody struct {
	Ref           string `json:"ref"`
	Quantity      string `json:"quantity"`
	LegalUnitCost string `json:"legalUnitCost"`
	// GroupUnitCost is the group's cost of the unit; without it E3 falls back to the seller's margin (CNS-0908).
	GroupUnitCost string `json:"groupUnitCost,omitempty"`
}

type icLayersBody struct {
	Layers []icLayerBody `json:"layers"`
}

// setLayers handles PUT /groups/{id}/units/{unitId}/layers/{period}: it replaces the IC receipt layers the unit's goods still sit
// in at the end of the period (FR-40.16). A run over the collection of that period takes its E3 unrealized profit from them.
func (a *API) setLayers(w http.ResponseWriter, r *http.Request) {
	if !a.groupsReady(w, r) {
		return
	}
	groupID, ok := pathID(w, r, "id", domain.CodeGroupNotFound)
	if !ok {
		return
	}
	unitID, ok := pathID(w, r, "unitId", domain.CodeGroupNotFound)
	if !ok {
		return
	}
	period := chi.URLParam(r, "period")
	var body icLayersBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !periodText.MatchString(period) {
		fields["period"] = "must be YYYY-MM"
	}
	if len(body.Layers) > MaxLayerRows {
		fields["layers"] = "must hold at most 5000 layers"
	}
	f := decimals{}
	layers := make([]domain.ICLayer, 0, len(body.Layers))
	seen := map[string]bool{}
	for i, l := range body.Layers {
		at := func(n string) string { return "layers[" + itoa(i) + "]." + n }
		if n := len(l.Ref); n < 1 || n > 128 {
			fields[at("ref")] = "must be 1 to 128 characters"
		}
		if seen[l.Ref] {
			fields[at("ref")] = "appears twice"
		}
		seen[l.Ref] = true
		layer := domain.ICLayer{ID: l.Ref, Quantity: f.get(at("quantity"), l.Quantity, true), LegalUnitCost: f.get(at("legalUnitCost"), l.LegalUnitCost, true)}
		if l.GroupUnitCost != "" {
			layer.GroupUnitCost, layer.GroupCostKnown = f.get(at("groupUnitCost"), l.GroupUnitCost, true), true
		}
		if layer.Quantity.IsNegative() || layer.LegalUnitCost.IsNegative() || layer.GroupUnitCost.IsNegative() {
			fields[at("quantity")] = "quantity and costs must not be negative"
		}
		layers = append(layers, layer)
	}
	for k, v := range f {
		fields[k] = v
	}
	if !validation(w, r, fields) {
		return
	}
	if err := a.groups.SetLayers(r.Context(), groupID, unitID, period, layers); err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	httpx.JSON(w, http.StatusOK, icLayersResponse(unitID, period, layers))
}

func itoa(i int) string { return decimal.NewFromInt(int64(i)).String() }

type icLayersOut struct {
	Unit   string        `json:"unit"`
	Period string        `json:"period"`
	Layers []icLayerBody `json:"layers"`
}

func icLayersResponse(unit, period string, layers []domain.ICLayer) icLayersOut {
	out := icLayersOut{Unit: unit, Period: period, Layers: make([]icLayerBody, 0, len(layers))}
	for _, l := range layers {
		b := icLayerBody{Ref: l.ID, Quantity: l.Quantity.String(), LegalUnitCost: l.LegalUnitCost.String()}
		if l.GroupCostKnown {
			b.GroupUnitCost = l.GroupUnitCost.String()
		}
		out.Layers = append(out.Layers, b)
	}
	return out
}

// listLayers handles GET /groups/{id}/layers/{period}: the layers of every unit of the group for the period.
func (a *API) listLayers(w http.ResponseWriter, r *http.Request) {
	if !a.groupsReady(w, r) {
		return
	}
	groupID, ok := pathID(w, r, "id", domain.CodeGroupNotFound)
	if !ok {
		return
	}
	period := chi.URLParam(r, "period")
	if !periodText.MatchString(period) {
		validation(w, r, map[string]string{"period": "must be YYYY-MM"})
		return
	}
	byUnit, err := a.groups.GetLayers(r.Context(), groupID, period)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	units := make([]string, 0, len(byUnit))
	for u := range byUnit {
		units = append(units, u)
	}
	sort.Strings(units)
	out := struct {
		Items []icLayersOut `json:"items"`
	}{Items: make([]icLayersOut, 0, len(units))}
	for _, u := range units {
		out.Items = append(out.Items, icLayersResponse(u, period, byUnit[u]))
	}
	httpx.JSON(w, http.StatusOK, out)
}

type investmentBody struct {
	Unit             string `json:"unit"`
	Method           string `json:"method"`
	OwnershipPct     string `json:"ownershipPct"`
	Cost             string `json:"cost"`
	OpeningCarrying  string `json:"openingCarrying"`
	ShareOfProfit    string `json:"shareOfProfit"`
	Dividends        string `json:"dividends"`
	ClosingCarrying  string `json:"closingCarrying"`
	UnrecognizedLoss string `json:"unrecognizedLoss"`
	// A foreign associate (IAS 21.44, IAS 28): its currency, the closing carrying amount in it, and the exchange difference of the
	// retranslation at the closing rate, in the group currency, booked to the translation reserve.
	UnitCurrency        string `json:"unitCurrency,omitempty"`
	ClosingCarryingUnit string `json:"closingCarryingUnit,omitempty"`
	FXDifference        string `json:"fxDifference,omitempty"`
}

type investmentsOut struct {
	RunID    string           `json:"runId,omitempty"`
	GroupID  string           `json:"groupId,omitempty"`
	Period   string           `json:"period,omitempty"`
	Currency string           `json:"currency,omitempty"`
	Items    []investmentBody `json:"items"`
}

func toInvestmentBody(r store.InvestmentRecord, currency string) investmentBody {
	f := func(d decimal.Decimal) string { return d.StringFixed(int32(money.MinorUnits(currency))) }
	out := investmentBody{Unit: r.UnitID, Method: r.Method, OwnershipPct: r.OwnershipPct.String(), Cost: f(r.Cost), OpeningCarrying: f(r.OpeningCarrying),
		ShareOfProfit: f(r.ShareOfProfit), Dividends: f(r.Dividends), ClosingCarrying: f(r.ClosingCarrying), UnrecognizedLoss: f(r.UnrecognizedLoss)}
	if r.UnitCurrency != "" {
		out.UnitCurrency, out.FXDifference = r.UnitCurrency, f(r.FXDifference)
		out.ClosingCarryingUnit = r.ClosingCarryingUnit.StringFixed(int32(money.MinorUnits(r.UnitCurrency)))
	}
	return out
}

// getRunInvestments handles GET /runs/{id}/investments: the investments register of a run, one line per unit in the group currency: at
// cost for a consolidated subsidiary, at the equity-method carrying amount for an associate (FR-40.18).
func (a *API) getRunInvestments(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id", domain.CodeRunNotFound)
	if !ok {
		return
	}
	run, err := a.runs.GetRun(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	recs, err := a.runs.RunInvestments(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	out := investmentsOut{RunID: id, GroupID: run.GroupID, Period: run.Period, Currency: run.Currency, Items: make([]investmentBody, 0, len(recs))}
	for _, rec := range recs {
		out.Items = append(out.Items, toInvestmentBody(rec, run.Currency))
	}
	httpx.JSON(w, http.StatusOK, out)
}

// listGroupInvestments handles GET /groups/{id}/investments?period=YYYY-MM: the investments register of the group's newest run for the
// period.
func (a *API) listGroupInvestments(w http.ResponseWriter, r *http.Request) {
	if !a.groupsReady(w, r) {
		return
	}
	groupID, ok := pathID(w, r, "id", domain.CodeGroupNotFound)
	if !ok {
		return
	}
	period := r.URL.Query().Get("period")
	if !periodText.MatchString(period) {
		validation(w, r, map[string]string{"period": "is required and must be YYYY-MM"})
		return
	}
	group, err := a.groups.GetGroup(r.Context(), groupID)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	reg, found, err := a.groups.LatestInvestments(r.Context(), groupID, period)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	out := investmentsOut{GroupID: groupID, Period: period, Currency: group.Currency, Items: []investmentBody{}}
	if found {
		units := make([]string, 0, len(reg))
		for u := range reg {
			units = append(units, u)
		}
		sort.Strings(units)
		for _, u := range units {
			out.RunID = reg[u].RunID
			out.Items = append(out.Items, toInvestmentBody(reg[u], group.Currency))
		}
	}
	httpx.JSON(w, http.StatusOK, out)
}
