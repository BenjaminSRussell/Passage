package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/costacct/internal/domain"
	"github.com/BenjaminSRussell/passage/services/costacct/internal/store"
)

// currencyAmount reports amount > 0 with no more decimals than the currency's minor unit.
func currencyAmount(amount decimal.Decimal, currency string) bool {
	return amount.Sign() > 0 && amount.Exponent() >= -money.MinorUnits(currency)
}

// topDownBody is the wire form of a top-down distribution request (FR-38.19).
type topDownBody struct {
	LegalEntityID      string                 `json:"legalEntityId"`
	Period             string                 `json:"period"`
	ReferencePeriod    string                 `json:"referencePeriod"`
	Currency           string                 `json:"currency"`
	SourceCostObjectID string                 `json:"sourceCostObjectId"`
	CostElementID      string                 `json:"costElementId"`
	Amount             decimal.Decimal        `json:"amount"`
	Segments           []store.TopDownSegment `json:"segments"`
}

func (a *API) runTopDown(w http.ResponseWriter, r *http.Request) {
	var b topDownBody
	key, hash, ok := prepare(w, r, &b)
	if !ok {
		return
	}
	switch {
	case !ids.Valid(b.LegalEntityID) || !ids.Valid(b.SourceCostObjectID) || !ids.Valid(b.CostElementID):
		invalid(w, r, "legalEntityId, sourceCostObjectId and costElementId must be UUIDs")
		return
	case !domain.ValidPeriod(b.Period):
		invalid(w, r, "period must be YYYY-MM")
		return
	case b.ReferencePeriod != "" && !domain.ValidPeriod(b.ReferencePeriod):
		invalid(w, r, "referencePeriod must be YYYY-MM")
		return
	case !validCurrency(b.Currency):
		invalid(w, r, "currency must be a 3-letter ISO code")
		return
	case !currencyAmount(b.Amount, b.Currency):
		invalid(w, r, "amount must be positive with no more decimals than the currency's minor unit")
		return
	case len(b.Segments) < 1 || len(b.Segments) > maxObjects:
		invalid(w, r, "segments must hold 1-500 entries")
		return
	}
	seen := map[string]bool{b.SourceCostObjectID: true}
	for i, sg := range b.Segments {
		if !ids.Valid(sg.CostObjectID) || seen[sg.CostObjectID] {
			invalid(w, r, lineErr(i, "segment cost objects must be distinct UUIDs and differ from the source"))
			return
		}
		seen[sg.CostObjectID] = true
		if sg.Basis.Sign() < 0 {
			invalid(w, r, lineErr(i, "basis must not be negative"))
			return
		}
	}
	status, raw, err := a.st.RunTopDown(r.Context(), key, hash, store.TopDownRequest{
		LegalEntityID: b.LegalEntityID, Period: b.Period, ReferencePeriod: b.ReferencePeriod, Currency: b.Currency,
		SourceCostObjectID: b.SourceCostObjectID, CostElementID: b.CostElementID, Amount: b.Amount, Segments: b.Segments,
	})
	writeCommand(w, r, status, raw, err)
}

func (a *API) listTopDownRuns(w http.ResponseWriter, r *http.Request) {
	le, ok := legalEntityFilter(w, r)
	if !ok {
		return
	}
	rows, err := a.st.ListTopDownRuns(r.Context(), le)
	list(w, r, rows, err)
}

func (a *API) getTopDownRun(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validTenant(w, r) {
		return
	}
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeTopDownRunNotFound, "top-down run not found"))
		return
	}
	run, err := a.st.GetTopDownRun(r.Context(), id)
	one(w, r, run, err)
}

// settleBody is the wire form of an internal order settlement (FR-38.9).
type settleBody struct {
	Period             string          `json:"period"`
	Currency           string          `json:"currency"`
	ReceiverCostObject string          `json:"receiverCostObjectId"`
	SecondaryElementID string          `json:"secondaryElementId"`
	Amount             decimal.Decimal `json:"amount"`
}

func (a *API) settle(w http.ResponseWriter, r *http.Request) {
	orderID := chi.URLParam(r, "id")
	var b settleBody
	key, hash, ok := prepare(w, r, &b)
	if !ok {
		return
	}
	switch {
	case !ids.Valid(orderID):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeObjectNotFound, "internal order not found"))
	case !ids.Valid(b.ReceiverCostObject) || !ids.Valid(b.SecondaryElementID):
		invalid(w, r, "receiverCostObjectId and secondaryElementId must be UUIDs")
	case b.ReceiverCostObject == orderID:
		invalid(w, r, "receiverCostObjectId must differ from the internal order")
	case !domain.ValidPeriod(b.Period):
		invalid(w, r, "period must be YYYY-MM")
	case !validCurrency(b.Currency):
		invalid(w, r, "currency must be a 3-letter ISO code")
	case !currencyAmount(b.Amount, b.Currency):
		invalid(w, r, "amount must be positive with no more decimals than the currency's minor unit")
	default:
		status, raw, err := a.st.SettleInternalOrder(r.Context(), key, hash, store.SettlementRequest{
			OrderID: orderID, Period: b.Period, Currency: b.Currency, ReceiverID: b.ReceiverCostObject,
			SecondaryElementID: b.SecondaryElementID, Amount: b.Amount,
		})
		writeCommand(w, r, status, raw, err)
	}
}

func (a *API) listSettlements(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validTenant(w, r) {
		return
	}
	period := r.URL.Query().Get("period")
	if !ids.Valid(id) || (period != "" && !domain.ValidPeriod(period)) {
		invalid(w, r, "internal order id must be a UUID and period must be YYYY-MM")
		return
	}
	rows, err := a.st.ListSettlements(r.Context(), id, period)
	list(w, r, rows, err)
}
