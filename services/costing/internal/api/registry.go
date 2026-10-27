package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/services/costing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/costing/internal/store"
)

const (
	// dutyValuationWrite maintains valuation ledgers and categories (PRD duty ValuationSetupMaintain).
	dutyValuationWrite = "costing.valuation.write"
	maxValuationTypes  = 50
	maxAccount         = 64
)

var rateTypes = map[string]bool{"SPOT": true, "AVERAGE": true, "HISTORICAL": true}

// ---- valuation ledgers (FR-20.15) ----

type valuationLedgerRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	LedgerGroupID string `json:"ledgerGroupId"`
	Code          string `json:"code"`
	Method        string `json:"method"`
	Currency      string `json:"currency"`
	RateType      string `json:"rateType"`
	Primary       bool   `json:"primary"`
}

type valuationLedgerList struct {
	Items []store.ValuationLedger `json:"items"`
}

// createValuationLedger registers a valuation ledger of a legal entity bound to
// a wave 12 ledger group. The ledger group id is not checked against the
// ledger service yet (no cross-service read in this slice).
func (a *API) createValuationLedger(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req valuationLedgerRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.LegalEntityID, req.LedgerGroupID = strings.ToLower(req.LegalEntityID), strings.ToLower(req.LedgerGroupID)
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !ids.Valid(req.LedgerGroupID) {
		fields["ledgerGroupId"] = "must be the UUID of a ledger group"
	}
	if !codePattern.MatchString(req.Code) {
		fields["code"] = "must be 2 to 32 characters of A-Z, 0-9, _ or -"
	}
	if !domain.Method(req.Method).Valid() {
		fields["method"] = "must be FIFO, LIFO, LIFO_DATE, WEIGHTED_AVG_RUNNING, WEIGHTED_AVG_PERIODIC, MOVING_AVG or STANDARD"
	}
	if !currencyPattern.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	if !rateTypes[req.RateType] {
		fields["rateType"] = "must be SPOT, AVERAGE or HISTORICAL"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if !legalEntityInScope(w, r, req.LegalEntityID) {
		return
	}
	v, err := a.store.CreateValuationLedger(r.Context(), store.ValuationLedger{LegalEntityID: req.LegalEntityID,
		LedgerGroupID: req.LedgerGroupID, Code: req.Code, Method: req.Method, Currency: req.Currency,
		RateType: req.RateType, Primary: req.Primary})
	if err != nil {
		a.fail(w, r, "create valuation ledger", err)
		return
	}
	httpx.JSON(w, http.StatusCreated, v)
}

func (a *API) listValuationLedgers(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	le, ok := legalEntityFilter(w, r, map[string]string{})
	if !ok {
		return
	}
	items, err := a.store.ListValuationLedgers(r.Context(), le)
	if err != nil {
		a.fail(w, r, "list valuation ledgers", err)
		return
	}
	httpx.JSON(w, http.StatusOK, valuationLedgerList{Items: items})
}

// ---- valuation categories (FR-20.16) ----

type valuationCategoryRequest struct {
	Code  string                `json:"code"`
	Batch bool                  `json:"batch"`
	Types []store.ValuationType `json:"types"`
}

type valuationCategoryList struct {
	Items []store.ValuationCategory `json:"items"`
}

func (a *API) createValuationCategory(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req valuationCategoryRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !codePattern.MatchString(req.Code) {
		fields["code"] = "must be 2 to 32 characters of A-Z, 0-9, _ or -"
	}
	if len(req.Types) > maxValuationTypes {
		fields["types"] = fmt.Sprintf("must hold at most %d valuation types", maxValuationTypes)
	}
	specs := make([]domain.ValuationTypeSpec, 0, len(req.Types))
	for i, t := range req.Types {
		key := fmt.Sprintf("types[%d]", i)
		req.Types[i].Account = strings.TrimSpace(t.Account)
		if !codePattern.MatchString(t.Code) {
			fields[key+".code"] = "must be 2 to 32 characters of A-Z, 0-9, _ or -"
		}
		if n := len(req.Types[i].Account); n == 0 || n > maxAccount {
			fields[key+".account"] = fmt.Sprintf("must be the inventory account of the type, 1 to %d characters", maxAccount)
		}
		specs = append(specs, domain.ValuationTypeSpec{Code: t.Code, Account: req.Types[i].Account})
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if err := domain.CheckCategoryTypes(req.Batch, specs); err != nil {
		a.fail(w, r, "valuation category", err)
		return
	}
	c, err := a.store.CreateValuationCategory(r.Context(), store.ValuationCategory{Code: req.Code, Batch: req.Batch, Types: req.Types})
	if err != nil {
		a.fail(w, r, "create valuation category", err)
		return
	}
	httpx.JSON(w, http.StatusCreated, c)
}

func (a *API) listValuationCategories(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	items, err := a.store.ListValuationCategories(r.Context())
	if err != nil {
		a.fail(w, r, "list valuation categories", err)
		return
	}
	httpx.JSON(w, http.StatusOK, valuationCategoryList{Items: items})
}
