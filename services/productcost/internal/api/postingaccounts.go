package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/domain"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/store"
)

const maxAccountLength = 64

type postingAccountsRequest struct {
	InventoryAccount  string `json:"inventoryAccount"`
	CostChangeAccount string `json:"costChangeAccount"`
}

func postingAccountsLE(w http.ResponseWriter, r *http.Request) (string, bool) {
	le := chi.URLParam(r, "legalEntityId")
	if !ids.Valid(le) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodePostingAccounts, "no posting accounts are configured for legal entity "+le))
		return "", false
	}
	if p, _ := tenant.FromContext(r.Context()); !tenant.InLegalEntityScope(p, le) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, domain.CodeLegalEntityScope, "legal entity "+le+" is outside your scope"))
		return "", false
	}
	return le, true
}

func (a *API) getPostingAccounts(w http.ResponseWriter, r *http.Request) {
	if !a.storeReady(w, r) {
		return
	}
	le, ok := postingAccountsLE(w, r)
	if !ok {
		return
	}
	pa, err := a.store.GetPostingAccounts(r.Context(), le)
	if err != nil {
		storeFailure(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, pa)
}

func (a *API) putPostingAccounts(w http.ResponseWriter, r *http.Request) {
	if !a.storeReady(w, r) {
		return
	}
	le, ok := postingAccountsLE(w, r)
	if !ok {
		return
	}
	var req postingAccountsRequest
	if !decode(w, r, &req) {
		return
	}
	in := store.PostingAccounts{LegalEntityID: le, InventoryAccount: strings.TrimSpace(req.InventoryAccount), CostChangeAccount: strings.TrimSpace(req.CostChangeAccount)}
	fields := map[string]string{}
	for key, v := range map[string]string{"inventoryAccount": in.InventoryAccount, "costChangeAccount": in.CostChangeAccount} {
		if v == "" || len(v) > maxAccountLength {
			fields[key] = "is required and must be at most 64 characters"
		}
	}
	if len(fields) == 0 && in.InventoryAccount == in.CostChangeAccount {
		fields["costChangeAccount"] = "must differ from the inventory account"
	}
	if !validation(w, r, fields) {
		return
	}
	pa, err := a.store.SetPostingAccounts(r.Context(), in)
	if err != nil {
		storeFailure(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, pa)
}
