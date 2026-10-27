package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/consolidation/internal/domain"
)

// Request bounds for the slice-2 previews.
const (
	// MaxLayers bounds the IC receipt layers of one profit-in-inventory preview.
	MaxLayers = 10000
	// MaxICPairs bounds the partner pairs of one two-sided IC elimination.
	MaxICPairs = 10000
	// MaxUnits is the upper bound of consol.maxUnits (PRD 7.40.13).
	MaxUnits = 2000
)

type warningBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func entriesOf(entries []domain.Entry, places int32) []entryBody {
	out := make([]entryBody, 0, len(entries))
	for _, e := range entries {
		out = append(out, entryBody{Rule: e.RuleCode, Account: e.Account, Debit: e.Debit.StringFixed(places), Credit: e.Credit.StringFixed(places)})
	}
	return out
}

func currencyField(f decimals, v string) {
	if !currencyCode.MatchString(v) {
		f["currency"] = "must be an ISO 4217 code"
	}
}

// ---- unrealized profit per IC receipt layer (FR-40.16, BR-40.7, AC-40.12) ----

type layerBody struct {
	ID            string `json:"id"`
	Quantity      string `json:"quantity"`
	LegalUnitCost string `json:"legalUnitCost"`
	GroupUnitCost string `json:"groupUnitCost"`
}

type profitRequest struct {
	Currency        string      `json:"currency"`
	SellerMarginPct string      `json:"sellerMarginPct"`
	Layers          []layerBody `json:"layers"`
}

type layerProfitBody struct {
	ID     string `json:"id"`
	Basis  string `json:"basis"`
	Profit string `json:"profit"`
}

type profitResponse struct {
	Currency      string            `json:"currency"`
	Layers        []layerProfitBody `json:"layers"`
	Total         string            `json:"total"`
	FallbackCount int               `json:"fallbackCount"`
	Warnings      []warningBody     `json:"warnings"`
}

func (a *API) previewProfitInInventory(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	var req profitRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	f := decimals{}
	currencyField(f, req.Currency)
	margin := f.get("sellerMarginPct", req.SellerMarginPct, false)
	switch {
	case len(req.Layers) == 0:
		f["layers"] = "must contain at least one layer"
	case len(req.Layers) > MaxLayers:
		f["layers"] = fmt.Sprintf("must contain at most %d layers", MaxLayers)
	}
	var layers []domain.ICLayer
	if _, bad := f["layers"]; !bad {
		for i, l := range req.Layers {
			at := func(name string) string { return fmt.Sprintf("layers[%d].%s", i, name) }
			layer := domain.ICLayer{
				ID:             strings.TrimSpace(l.ID),
				Quantity:       f.get(at("quantity"), l.Quantity, true),
				LegalUnitCost:  f.get(at("legalUnitCost"), l.LegalUnitCost, true),
				GroupUnitCost:  f.get(at("groupUnitCost"), l.GroupUnitCost, false),
				GroupCostKnown: l.GroupUnitCost != "",
			}
			if layer.ID == "" {
				layer.ID = fmt.Sprintf("layer-%d", i+1)
			}
			if !layer.GroupCostKnown && req.SellerMarginPct == "" {
				f["sellerMarginPct"] = "is required when a layer has no group cost"
			}
			layers = append(layers, layer)
		}
	}
	if !validation(w, r, f) {
		return
	}
	res, err := domain.UnrealizedProfitLayers(layers, req.Currency, margin)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.profitPreviews.Add(1)
	places := money.MinorUnits(req.Currency)
	out := profitResponse{Currency: req.Currency, Layers: make([]layerProfitBody, 0, len(res.Layers)),
		Total: res.Total.StringFixed(places), FallbackCount: res.FallbackCount, Warnings: []warningBody{}}
	for _, l := range res.Layers {
		out.Layers = append(out.Layers, layerProfitBody{ID: l.ID, Basis: l.Basis, Profit: l.Profit.StringFixed(places)})
	}
	if wn := res.Warning(); wn != nil {
		out.Warnings = append(out.Warnings, warningBody{Code: wn.Code, Message: wn.Detail})
	}
	httpx.JSON(w, http.StatusOK, out)
}

// ---- equity method (FR-40.18, AC-40.14) ----

type equityRequest struct {
	Currency          string `json:"currency"`
	OwnershipPct      string `json:"ownershipPct"`
	OpeningCarrying   string `json:"openingCarrying"`
	NetIncome         string `json:"netIncome"`
	DividendsReceived string `json:"dividendsReceived"`
}

type equityResponse struct {
	Currency         string      `json:"currency"`
	ShareOfProfit    string      `json:"shareOfProfit"`
	UnrecognizedLoss string      `json:"unrecognizedLoss"`
	ClosingCarrying  string      `json:"closingCarrying"`
	Entries          []entryBody `json:"entries"`
	Balanced         bool        `json:"balanced"`
}

func (a *API) previewEquityMethod(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	var req equityRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	f := decimals{}
	currencyField(f, req.Currency)
	in := domain.Associate{
		Currency:          req.Currency,
		OwnershipPct:      f.get("ownershipPct", req.OwnershipPct, true),
		OpeningCarrying:   f.get("openingCarrying", req.OpeningCarrying, true),
		NetIncome:         f.get("netIncome", req.NetIncome, true),
		DividendsReceived: f.get("dividendsReceived", req.DividendsReceived, false),
	}
	if !validation(w, r, f) {
		return
	}
	res, err := domain.EquityMethod(in)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.equityPreviews.Add(1)
	places := money.MinorUnits(req.Currency)
	httpx.JSON(w, http.StatusOK, equityResponse{
		Currency:         req.Currency,
		ShareOfProfit:    res.ShareOfProfit.StringFixed(places),
		UnrecognizedLoss: res.UnrecognizedLoss.StringFixed(places),
		ClosingCarrying:  res.ClosingCarrying.StringFixed(places),
		Entries:          entriesOf(res.Entries, places),
		Balanced:         domain.CheckBalanced(res.Entries) == nil,
	})
}

// ---- divestiture gain (FR-40.18) ----

type divestitureRequest struct {
	Currency          string `json:"currency"`
	Proceeds          string `json:"proceeds"`
	NetAssetsCarrying string `json:"netAssetsCarrying"`
	Goodwill          string `json:"goodwill"`
	NCICarrying       string `json:"nciCarrying"`
	CTARecycled       string `json:"ctaRecycled"`
}

type divestitureResponse struct {
	Currency       string `json:"currency"`
	CarryingAmount string `json:"carryingAmount"`
	Gain           string `json:"gain"`
}

func (a *API) previewDivestiture(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	var req divestitureRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	f := decimals{}
	currencyField(f, req.Currency)
	in := domain.Divestiture{
		Currency:          req.Currency,
		Proceeds:          f.get("proceeds", req.Proceeds, true),
		NetAssetsCarrying: f.get("netAssetsCarrying", req.NetAssetsCarrying, true),
		Goodwill:          f.get("goodwill", req.Goodwill, false),
		NCICarrying:       f.get("nciCarrying", req.NCICarrying, false),
		CTARecycled:       f.get("ctaRecycled", req.CTARecycled, false),
	}
	for name, v := range map[string]decimal.Decimal{"proceeds": in.Proceeds, "goodwill": in.Goodwill, "nciCarrying": in.NCICarrying} {
		if _, bad := f[name]; !bad && v.IsNegative() {
			f[name] = "must not be negative"
		}
	}
	if !validation(w, r, f) {
		return
	}
	gain, err := domain.DivestitureGain(in)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.divestiturePreviews.Add(1)
	places := money.MinorUnits(req.Currency)
	carrying := in.NetAssetsCarrying.Add(in.Goodwill).Sub(in.NCICarrying)
	httpx.JSON(w, http.StatusOK, divestitureResponse{Currency: req.Currency, CarryingAmount: carrying.StringFixed(places), Gain: gain.StringFixed(places)})
}

// ---- two-sided IC elimination with threshold (BR-40.15, CNS-0905) ----

type icPairBody struct {
	Pair       string `json:"pair"`
	Receivable string `json:"receivable"`
	Payable    string `json:"payable"`
}

type icDifferenceRequest struct {
	Currency  string       `json:"currency"`
	Threshold string       `json:"threshold"`
	Pairs     []icPairBody `json:"pairs"`
}

type icPairResult struct {
	Pair       string      `json:"pair"`
	Receivable string      `json:"receivable"`
	Payable    string      `json:"payable"`
	Difference string      `json:"difference"`
	Status     string      `json:"status"`
	Code       string      `json:"code,omitempty"`
	Message    string      `json:"message,omitempty"`
	Entries    []entryBody `json:"entries"`
}

type icDifferenceResponse struct {
	Currency   string         `json:"currency"`
	Threshold  string         `json:"threshold"`
	TaskStatus string         `json:"taskStatus"`
	Pairs      []icPairResult `json:"pairs"`
}

// Pair statuses of the two-sided IC elimination.
const (
	pairEliminated = "Eliminated"
	pairError      = "Error"
)

func (a *API) previewICDifferences(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	var req icDifferenceRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	f := decimals{}
	currencyField(f, req.Currency)
	threshold := domain.DefaultICThreshold
	if req.Threshold != "" {
		threshold = f.get("threshold", req.Threshold, true)
		if _, bad := f["threshold"]; !bad && threshold.IsNegative() {
			f["threshold"] = "must not be negative"
		}
	}
	switch {
	case len(req.Pairs) == 0:
		f["pairs"] = "must contain at least one partner pair"
	case len(req.Pairs) > MaxICPairs:
		f["pairs"] = fmt.Sprintf("must contain at most %d pairs", MaxICPairs)
	}
	type pairIn struct{ recv, pay decimal.Decimal }
	var ins []pairIn
	if _, bad := f["pairs"]; !bad {
		for i, p := range req.Pairs {
			at := func(name string) string { return fmt.Sprintf("pairs[%d].%s", i, name) }
			if strings.TrimSpace(p.Pair) == "" {
				f[at("pair")] = "is required"
			}
			ins = append(ins, pairIn{f.get(at("receivable"), p.Receivable, true), f.get(at("payable"), p.Payable, true)})
		}
	}
	if !validation(w, r, f) {
		return
	}
	places := money.MinorUnits(req.Currency)
	out := icDifferenceResponse{Currency: req.Currency, Threshold: threshold.StringFixed(places), TaskStatus: string(domain.TaskCompleted),
		Pairs: make([]icPairResult, 0, len(ins))}
	for i, in := range ins {
		entries, diff, err := domain.EliminateICPair(in.recv, in.pay, threshold, req.Currency)
		res := icPairResult{Pair: req.Pairs[i].Pair, Receivable: in.recv.StringFixed(places), Payable: in.pay.StringFixed(places),
			Difference: diff.StringFixed(places), Status: pairEliminated, Entries: entriesOf(entries, places)}
		var de *domain.Error
		switch {
		case err == nil:
		case errors.As(err, &de) && de.Code == domain.CodeICThreshold:
			res.Status, res.Code, res.Message = pairError, de.Code, fmt.Sprintf("IC difference %s exceeds threshold %s for %s", diff.Abs().StringFixed(places), threshold.StringFixed(places), res.Pair)
			out.TaskStatus = string(domain.TaskError)
		default:
			httpx.Fail(w, r, problemFrom(err))
			return
		}
		out.Pairs = append(out.Pairs, res)
	}
	a.icDifferencePreviews.Add(1)
	httpx.JSON(w, http.StatusOK, out)
}

// ---- unit validation rules and the monitor gate (FR-40.20, AC-40.15) ----

type unitFSBody struct {
	UnitID                string `json:"unitId"`
	TotalAssets           string `json:"totalAssets"`
	TotalLiabilities      string `json:"totalLiabilities"`
	TotalEquity           string `json:"totalEquity"`
	OpeningRetained       string `json:"openingRetained"`
	NetIncome             string `json:"netIncome"`
	Dividends             string `json:"dividends"`
	ClosingRetained       string `json:"closingRetained"`
	ICItemsWithoutPartner int    `json:"icItemsWithoutPartner"`
}

type validationRequest struct {
	Units []unitFSBody `json:"units"`
}

type ruleResultBody struct {
	Rule       string `json:"rule"`
	Severity   string `json:"severity"`
	Tolerance  string `json:"tolerance"`
	Difference string `json:"difference"`
	Failed     bool   `json:"failed"`
	Code       string `json:"code,omitempty"`
	Message    string `json:"message,omitempty"`
}

type unitValidationBody struct {
	UnitID string           `json:"unitId"`
	Status string           `json:"status"`
	Rules  []ruleResultBody `json:"rules"`
}

type validationResponse struct {
	Units            []unitValidationBody `json:"units"`
	GroupTranslation string               `json:"groupTranslation"`
}

func (a *API) previewValidation(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	var req validationRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	f := decimals{}
	switch {
	case len(req.Units) == 0:
		f["units"] = "must contain at least one unit"
	case len(req.Units) > MaxUnits:
		f["units"] = fmt.Sprintf("must contain at most %d units", MaxUnits)
	}
	var fss []domain.UnitFS
	if _, bad := f["units"]; !bad {
		seen := map[string]bool{}
		for i, u := range req.Units {
			at := func(name string) string { return fmt.Sprintf("units[%d].%s", i, name) }
			id := strings.TrimSpace(u.UnitID)
			switch {
			case id == "":
				f[at("unitId")] = "is required"
			case seen[id]:
				f[at("unitId")] = "appears more than once"
			}
			seen[id] = true
			if u.ICItemsWithoutPartner < 0 {
				f[at("icItemsWithoutPartner")] = "must not be negative"
			}
			fss = append(fss, domain.UnitFS{
				TotalAssets:           f.get(at("totalAssets"), u.TotalAssets, true),
				TotalLiabilities:      f.get(at("totalLiabilities"), u.TotalLiabilities, true),
				TotalEquity:           f.get(at("totalEquity"), u.TotalEquity, true),
				OpeningRetained:       f.get(at("openingRetained"), u.OpeningRetained, true),
				NetIncome:             f.get(at("netIncome"), u.NetIncome, true),
				Dividends:             f.get(at("dividends"), u.Dividends, false),
				ClosingRetained:       f.get(at("closingRetained"), u.ClosingRetained, true),
				ICItemsWithoutPartner: u.ICItemsWithoutPartner,
			})
		}
	}
	if !validation(w, r, f) {
		return
	}
	rules := domain.DefaultValidationRules()
	tolerance := map[string]decimal.Decimal{}
	for _, rule := range rules {
		tolerance[rule.ID] = rule.Tolerance
	}
	out := validationResponse{Units: make([]unitValidationBody, 0, len(fss))}
	statuses := make([]domain.TaskStatus, 0, len(fss))
	for i, fs := range fss {
		unit := req.Units[i].UnitID
		results, err := domain.EvaluateRules(rules, fs)
		if err != nil {
			httpx.Fail(w, r, problemFrom(err))
			return
		}
		status := domain.UnitTaskStatus(results)
		statuses = append(statuses, status)
		body := unitValidationBody{UnitID: unit, Status: string(status), Rules: make([]ruleResultBody, 0, len(results))}
		for _, res := range results {
			rb := ruleResultBody{Rule: res.RuleID, Severity: string(res.Severity), Tolerance: tolerance[res.RuleID].String(),
				Difference: res.Difference.String(), Failed: res.Failed}
			if fail := res.Failure(unit); fail != nil {
				rb.Code, rb.Message = fail.Code, fail.Detail
			}
			body.Rules = append(body.Rules, rb)
		}
		out.Units = append(out.Units, body)
	}
	out.GroupTranslation = string(domain.TaskNotStarted)
	if domain.GroupTranslationBlocked(statuses) {
		out.GroupTranslation = string(domain.TaskBlocked)
	}
	a.validationPreviews.Add(1)
	httpx.JSON(w, http.StatusOK, out)
}
