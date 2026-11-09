package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/domain"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/inventory"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/store"
)

type reportLine struct {
	domain.RevalPreviewLine
	OnHand     *decimal.Decimal `json:"onHand"`
	Difference *decimal.Decimal `json:"difference"`
	Tied       *bool            `json:"tied"`
}

// revaluationReport leaves the on-hand fields null for a reversal: its lines
// carry the reversed quantities, which are not the stock held now.
type revaluationReport struct {
	Activation    store.Activation `json:"activation"`
	Lines         []reportLine     `json:"lines"`
	LinesTotal    decimal.Decimal  `json:"linesTotal"`
	TiedToJournal bool             `json:"tiedToJournal"`
	TiedToOnHand  *bool            `json:"tiedToOnHand"`
	OnHandAsOf    string           `json:"onHandAsOf"`
}

// revaluationReport answers the value report of an activation: its stored
// lines against the journal it booked and against the inventory on-hand read
// again for the posting date. A difference means the inventory moved the
// quantity after the activation read it (a live read after the posting date
// cannot replay, so a posting dated today is compared with the live projection).
func (a *API) revaluationReport(w http.ResponseWriter, r *http.Request) {
	if !a.storeReady(w, r) {
		return
	}
	id, ok := ids.Canonical(chi.URLParam(r, "id"))
	if !ok {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeReversalTarget, "activation "+chi.URLParam(r, "id")+" not found"))
		return
	}
	act, err := a.store.GetActivation(r.Context(), id)
	if err != nil {
		storeFailure(w, r, err)
		return
	}
	if p, _ := tenant.FromContext(r.Context()); !tenant.InLegalEntityScope(p, act.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, domain.CodeLegalEntityScope, "legal entity "+act.LegalEntityID+" is outside your scope"))
		return
	}
	report := revaluationReport{Activation: act, Lines: make([]reportLine, len(act.Lines)), LinesTotal: decimal.Zero, OnHandAsOf: act.PostingDate}
	var onHand map[inventory.Position]decimal.Decimal
	positions := make([]inventory.Position, len(act.Lines))
	for i, l := range act.Lines {
		positions[i] = inventory.Position{ItemID: l.ItemID, SiteID: l.SiteID}
	}
	if act.ReversesActivationID == nil {
		if a.onHand == nil {
			httpx.Fail(w, r, activationProblem(http.StatusServiceUnavailable, domain.CodeOnHandUnavailable, "the inventory service is not configured; the report cannot be tied to the on-hand"))
			return
		}
		if onHand, err = a.quantities(r.Context(), act.LegalEntityID, act.PostingDate, positions); err != nil {
			storeFailure(w, r, err)
			return
		}
		allTied := true
		report.TiedToOnHand = &allTied
	}
	for i, l := range act.Lines {
		line := reportLine{RevalPreviewLine: l}
		if report.TiedToOnHand != nil {
			have := onHand[positions[i]]
			diff := have.Sub(l.Qty)
			tied := diff.IsZero()
			line.OnHand, line.Difference, line.Tied = &have, &diff, &tied
			*report.TiedToOnHand = *report.TiedToOnHand && tied
		}
		report.LinesTotal = report.LinesTotal.Add(l.Amount)
		report.Lines[i] = line
	}
	report.TiedToJournal = journalTies(act, report.LinesTotal)
	httpx.JSON(w, http.StatusOK, report)
}

func journalTies(act store.Activation, linesTotal decimal.Decimal) bool {
	if !linesTotal.Equal(act.Total) {
		return false
	}
	debit, credit := decimal.Zero, decimal.Zero
	for _, l := range act.JournalLines {
		if l.Credit {
			credit = credit.Add(l.Amount)
		} else {
			debit = debit.Add(l.Amount)
		}
	}
	return debit.Equal(credit) && debit.Equal(act.Total.Abs())
}
