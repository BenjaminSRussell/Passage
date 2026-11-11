package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

const dutyBankAccount = "receivables.bankaccount.manage"

// bankAccountRoutes mounts the bank account mappings (30-2): the ledger account a legal entity books the cash of each of its
// bank accounts to.
func (a *API) bankAccountRoutes(r chi.Router) {
	r.With(authz.RequireDuty(dutyView)).Get("/bank-accounts", a.listBankAccounts)
	r.With(authz.RequireDuty(dutyBankAccount)).Put("/bank-accounts", a.putBankAccount)
	r.With(authz.RequireDuty(dutyBankAccount)).Delete("/bank-accounts", a.deleteBankAccount)
}

func (a *API) listBankAccounts(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	le := r.URL.Query().Get("legalEntityId")
	if le != "" && !ids.Valid(le) {
		badQuery(w, r, "legalEntityId", "must be a UUID")
		return
	}
	if le != "" && !scopeAllows(w, r, le) {
		return
	}
	items, err := a.store.ListBankAccounts(r.Context(), le)
	if err != nil {
		a.fail("list bank accounts", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) putBankAccount(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var b store.BankAccount
	if err := httpx.Decode(r, &b); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !ids.Valid(b.LegalEntityID) {
		writeFields(w, r, map[string]string{"legalEntityId": "must be a UUID"})
		return
	}
	if !scopeAllows(w, r, b.LegalEntityID) {
		return
	}
	if err := store.ValidateBankAccount(&b); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := a.store.PutBankAccount(r.Context(), b); err != nil {
		a.fail("save bank account", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, b)
}

func (a *API) deleteBankAccount(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	le := q.Get("legalEntityId")
	if !ids.Valid(le) {
		badQuery(w, r, "legalEntityId", "must be a UUID")
		return
	}
	if len(q.Get("currency")) != 3 {
		badQuery(w, r, "currency", "must be an ISO 4217 code such as USD")
		return
	}
	if !scopeAllows(w, r, le) {
		return
	}
	if err := a.store.DeleteBankAccount(r.Context(), le, q.Get("currency"), q.Get("accountRef")); err != nil {
		a.fail("delete bank account", w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
