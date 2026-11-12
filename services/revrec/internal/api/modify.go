package api

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/revrec/internal/domain"
	"github.com/BenjaminSRussell/passage/services/revrec/internal/store"
)

var signedDecimalText = regexp.MustCompile(`^-?[0-9]{1,14}(\.[0-9]{1,6})?$`)

type modifyObligationDTO struct {
	ObligationID string `json:"obligationId"`
	SSP          string `json:"ssp"`
}

// modifyRequest is POST /v1/revrec/contracts/{id}:modify.
type modifyRequest struct {
	Type          string `json:"type"`
	EffectiveDate string `json:"effectiveDate"`
	// TransactionPriceChange is the signed change of the transaction price; zero is allowed when only SSPs are revised.
	TransactionPriceChange string                `json:"transactionPriceChange"`
	Obligations            []modifyObligationDTO `json:"obligations"`
	Reason                 string                `json:"reason,omitempty"`
}

type modificationLineOut struct {
	ObligationID    string  `json:"obligationId"`
	AllocatedBefore string  `json:"allocatedBefore"`
	AllocatedAfter  string  `json:"allocatedAfter"`
	SSPBefore       string  `json:"sspBefore"`
	SSPAfter        string  `json:"sspAfter"`
	Progress        *string `json:"progress,omitempty"`
	CatchUp         string  `json:"catchUp"`
}

type modificationOut struct {
	ID            string                `json:"id"`
	Type          string                `json:"type"`
	EffectiveDate string                `json:"effectiveDate"`
	PriceBefore   string                `json:"priceBefore"`
	PriceAfter    string                `json:"priceAfter"`
	CatchUp       string                `json:"catchUp"`
	Obligations   []modificationLineOut `json:"obligations"`
}

type modifyResponse struct {
	contractOut
	Modification modificationOut `json:"modification"`
}

// modifyContract re-allocates the existing obligations of a contract after a price change or an SSP revision (#29, ASC 606-10-25-10
// to 25-13): PROSPECTIVE spreads the consideration still to recognize over the performance left, CATCH_UP re-states revenue to date at
// the effective date and recognizes the cumulative catch-up. Adding goods or services is :allocate.
func (a *API) modifyContract(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeNotFound, "contract not found"))
		return
	}
	// The price change is relative, so a retry would apply it twice: the contract version the caller reviewed is required, and a repeat
	// of the same call finds the version moved on (412).
	raw := r.Header.Get("If-Match")
	if strings.TrimSpace(raw) == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusPreconditionRequired, "COM-0428", "If-Match with the contract version is required to modify a contract"))
		return
	}
	expected, ok := parseIfMatch(raw)
	if !ok || expected == 0 {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", `If-Match must be the contract version as an entity tag, for example "1"`))
		return
	}
	var req modifyRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !domain.ValidModificationType(req.Type) {
		fields["type"] = "must be PROSPECTIVE or CATCH_UP"
	}
	effective, err := time.Parse(dateLayout, req.EffectiveDate)
	if err != nil {
		fields["effectiveDate"] = "must be a date, YYYY-MM-DD"
	}
	change := decimal.Zero
	if !signedDecimalText.MatchString(req.TransactionPriceChange) {
		fields["transactionPriceChange"] = "must be a signed decimal with at most 6 fraction digits"
	} else {
		change = decimal.RequireFromString(req.TransactionPriceChange)
	}
	ssps := map[string]decimal.Decimal{}
	for i, o := range req.Obligations {
		prefix := "obligations[" + strconv.Itoa(i) + "]."
		if !ids.Valid(o.ObligationID) {
			fields[prefix+"obligationId"] = "must be an obligation id of the contract"
			continue
		}
		if !decimalText.MatchString(o.SSP) || !decimal.RequireFromString(o.SSP).IsPositive() {
			fields[prefix+"ssp"] = "must be a positive decimal with at most 6 fraction digits"
			continue
		}
		if _, dup := ssps[o.ObligationID]; dup {
			fields[prefix+"obligationId"] = "appears twice"
			continue
		}
		ssps[o.ObligationID] = decimal.RequireFromString(o.SSP)
	}
	if len(req.Reason) > 500 {
		fields["reason"] = "must be at most 500 characters"
	}
	if change.IsZero() && len(ssps) == 0 && len(fields) == 0 {
		fields["transactionPriceChange"] = "change the price or revise at least one standalone selling price"
	}
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	existing, err := a.store.GetContract(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !inScope(r, existing.LegalEntityID) {
		httpx.Fail(w, r, outOfScope())
		return
	}
	c, mod, err := a.store.ModifyContract(r.Context(), id, store.ModificationRequest{Type: req.Type, EffectiveDate: effective, PriceChange: change,
		NewSSP: ssps, Reason: strings.TrimSpace(req.Reason), ExpectedVersion: expected})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.contractsModified.Add(1)
	out := modificationOut{ID: mod.ID, Type: mod.Type, EffectiveDate: mod.EffectiveDate.Format(dateLayout), PriceBefore: fixed(mod.PriceBefore, c.Currency),
		PriceAfter: fixed(mod.PriceAfter, c.Currency), CatchUp: fixed(mod.CatchUp, c.Currency), Obligations: []modificationLineOut{}}
	for _, o := range mod.Obligations {
		line := modificationLineOut{ObligationID: o.ID, AllocatedBefore: fixed(o.AllocatedBefore, c.Currency), AllocatedAfter: fixed(o.AllocatedAfter, c.Currency),
			SSPBefore: sspText(o.SSPBefore, c.Currency), SSPAfter: sspText(o.SSPAfter, c.Currency), CatchUp: fixed(o.CatchUp, c.Currency)}
		if mod.Type == domain.ModCatchUp {
			p := o.Progress.Round(6).String()
			line.Progress = &p
		}
		out.Obligations = append(out.Obligations, line)
	}
	w.Header().Set("ETag", etag(c.Version))
	httpx.JSON(w, http.StatusOK, modifyResponse{contractOut: toContractOut(c), Modification: out})
}
