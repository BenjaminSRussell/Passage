package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/store"
)

// dutyRequisitionCheck is held by purchasing's service token only (it checks an approved requisition and gives back the
// pre-encumbrance of a cancelled line).
const dutyRequisitionCheck = "budget.requisition.check"

// ResultNotControlled is the check result of a requisition none of whose lines is under a budget control rule.
const ResultNotControlled = "NOT_CONTROLLED"

var dimensionPart = regexp.MustCompile(`^[A-Za-z0-9._-]{1,20}$`)

type requisitionCheckLine struct {
	LineID        string          `json:"lineId"`
	GLAccount     string          `json:"glAccount"`
	CostCenter    string          `json:"costCenter"`
	Amount        decimal.Decimal `json:"amount"`
	RequestedDate string          `json:"requestedDate"`
}

type requisitionCheckRequest struct {
	LegalEntityID string                 `json:"legalEntityId"`
	RequisitionID string                 `json:"requisitionId"`
	Currency      string                 `json:"currency"`
	Lines         []requisitionCheckLine `json:"lines"`
}

// checkRequisition runs the budget check (FR-38.4) for an approved requisition and reserves its lines as pre-encumbrances
// (FR-38.5). It resolves what a caller of /checks would have to know: the control rule in force on the lines' dates, the
// dimension key (account|costCenter) and the span (YYYY-MM of the requested date). A repeat for the same lines adjusts their
// reservation instead of doubling it. The result is the check's: FAIL reserves nothing.
func (a *API) checkRequisition(w http.ResponseWriter, r *http.Request) {
	body, composite, ok := readCommand(w, r)
	if !ok {
		return
	}
	var req requisitionCheckRequest
	if err := decodeBody(body, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !ids.Valid(req.RequisitionID) {
		fields["requisitionId"] = "must be a UUID"
	}
	if !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 code"
	}
	if len(req.Lines) == 0 || len(req.Lines) > maxCheckLines {
		fields["lines"] = fmt.Sprintf("must hold 1 to %d lines", maxCheckLines)
	}
	lines := make([]store.CheckLine, 0, len(req.Lines))
	seen := map[string]bool{}
	latest := ""
	for i, l := range req.Lines {
		prefix := fmt.Sprintf("lines[%d].", i)
		if !ids.Valid(l.LineID) {
			fields[prefix+"lineId"] = "must be a UUID"
		} else if seen[l.LineID] {
			fields[prefix+"lineId"] = "appears twice in the document"
		}
		seen[l.LineID] = true
		if !dimensionPart.MatchString(l.GLAccount) {
			fields[prefix+"glAccount"] = "must be 1 to 20 letters, digits, dots, hyphens or underscores"
		}
		if !dimensionPart.MatchString(l.CostCenter) {
			fields[prefix+"costCenter"] = "must be 1 to 20 letters, digits, dots, hyphens or underscores"
		}
		date, err := time.Parse(dateLayout, l.RequestedDate)
		if err != nil {
			fields[prefix+"requestedDate"] = "must be YYYY-MM-DD"
		} else if l.RequestedDate > latest {
			latest = l.RequestedDate
		}
		if l.Amount.Sign() < 0 || l.Amount.Exponent() < -4 {
			fields[prefix+"amount"] = "must not be negative and have at most 4 decimal places"
		}
		lines = append(lines, store.CheckLine{LineID: l.LineID, DimensionKey: l.GLAccount + "|" + l.CostCenter, SpanKey: date.Format("2006-01"), Amount: l.Amount})
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	rule, found, err := a.store.ActiveRule(r.Context(), req.LegalEntityID, latest)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !found {
		raw, _ := json.Marshal(map[string]any{"result": ResultNotControlled, "documentId": req.RequisitionID, "documentType": "Requisition", "lines": []any{}})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
		return
	}
	if rule.Currency != req.Currency {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, "BUD-0080", fmt.Sprintf("the budget control rule %s is in %s but the requisition is in %s", rule.Code, rule.Currency, req.Currency)))
		return
	}
	key, hash := splitKey(composite)
	in := store.CheckRequest{RuleVersionID: rule.ID, DocumentID: req.RequisitionID, DocumentType: "Requisition", Lines: lines}
	a.replay(w, r)(a.store.CheckBudget(r.Context(), key, hash, in, false))
}

type releaseLineRequest struct {
	RequisitionID     string `json:"requisitionId"`
	RequisitionLineID string `json:"requisitionLineId"`
}

// releaseRequisitionLine gives back the pre-encumbrance of a cancelled requisition line (FR-38.5).
func (a *API) releaseRequisitionLine(w http.ResponseWriter, r *http.Request) {
	body, composite, ok := readCommand(w, r)
	if !ok {
		return
	}
	var req releaseLineRequest
	if err := decodeBody(body, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.RequisitionID) {
		fields["requisitionId"] = "must be a UUID (the cancelled requisition)"
	}
	if !ids.Valid(req.RequisitionLineID) {
		fields["requisitionLineId"] = "must be a UUID (the cancelled requisition line)"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	key, hash := splitKey(composite)
	a.replay(w, r)(a.store.ReleasePreEncumbranceByLine(r.Context(), key, hash, req.RequisitionLineID, req.RequisitionID))
}
