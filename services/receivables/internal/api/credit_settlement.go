package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

func (a *API) creditSettlementRoutes(r chi.Router) {
	r.With(authz.RequireDuty(dutySettle)).Post("/credit-settlements", a.settleCredit)
}

type creditDebitRequest struct {
	OpenItemID string          `json:"openItemId"`
	Amount     decimal.Decimal `json:"amount"`
	// ItemAmount is what comes off a debit in another currency than the credit, in the debit's currency; omitted, it is priced.
	ItemAmount decimal.Decimal `json:"itemAmount"`
}

type settleCreditRequest struct {
	LegalEntityID   string               `json:"legalEntityId"`
	CreditItemID    string               `json:"creditItemId"`
	Debits          []creditDebitRequest `json:"debits"`
	AgainstOriginal bool                 `json:"againstOriginal"`
}

const maxCreditDebits = 100

// settleCredit offsets a credit item (credit note, adjustment, unapplied payment) against debit items of the same customer
// and currency, either the named debits or the open items of the invoice the credit note credits (30-3).
func (a *API) settleCredit(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req settleCreditRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !ids.Valid(req.CreditItemID) {
		fields["creditItemId"] = "must be a UUID"
	}
	switch {
	case req.AgainstOriginal && len(req.Debits) > 0:
		fields["debits"] = "name the debits or ask for againstOriginal, not both"
	case !req.AgainstOriginal && len(req.Debits) == 0:
		fields["debits"] = "name at least one debit, or set againstOriginal"
	case len(req.Debits) > maxCreditDebits:
		fields["debits"] = "at most 100 debits per settlement"
	}
	debits := make([]store.CreditDebit, len(req.Debits))
	for i, d := range req.Debits {
		if !ids.Valid(d.OpenItemID) || !d.Amount.IsPositive() || d.ItemAmount.IsNegative() {
			fields["debits"] = "every debit needs an openItemId and an amount greater than 0 (itemAmount, when given, too)"
		}
		debits[i] = store.CreditDebit{OpenItemID: d.OpenItemID, Amount: d.Amount, ItemAmount: d.ItemAmount}
	}
	if len(fields) > 0 {
		p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
		p.Errors = fields
		httpx.Fail(w, r, p)
		return
	}
	if !scopeAllows(w, r, req.LegalEntityID) {
		return
	}
	out, err := a.store.SettleCredit(r.Context(), store.CreditSettlementRequest{LegalEntityID: req.LegalEntityID,
		CreditItemID: req.CreditItemID, Debits: debits, AgainstOriginal: req.AgainstOriginal}, r.Header.Get(idempotencyKey))
	if err != nil {
		a.fail("settle credit", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
