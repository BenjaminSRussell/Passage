package api

import (
	"net/http"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/store"
)

type issueCheckRequest struct {
	BankAccountID string `json:"bankAccountId"`
	Payee         string `json:"payee"`
	Amount        string `json:"amount"`
	Currency      string `json:"currency"`
}

type checkReasonRequest struct {
	Reason string `json:"reason"`
}

// issueCheck allocates the next check number of the account (FR-36.6).
func (a *API) issueCheck(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req issueCheckRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.BankAccountID) {
		fields["bankAccountId"] = "must be a UUID"
	}
	payee := strings.TrimSpace(req.Payee)
	if n := len(payee); n < 1 || n > 140 {
		fields["payee"] = "1 to 140 characters"
	}
	amount, ok := parseAmount(req.Amount, req.Currency)
	if !ok {
		fields["amount"] = "must be a positive decimal string with at most the currency's minor-unit places"
	}
	if !domain.ValidCurrency(req.Currency) {
		fields["currency"] = "must be an ISO 4217 code"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	c, err := a.store.IssueCheck(r.Context(), store.NewCheck{
		BankAccountID: req.BankAccountID, Payee: payee, Amount: amount.String(), Currency: req.Currency,
	}, key)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.checksIssued.Add(1)
	setETag(w, c.Version)
	httpx.JSON(w, http.StatusCreated, c)
}

// listChecks is the check register of one bank account (7.36.14), newest number first.
func (a *API) listChecks(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	account := r.URL.Query().Get("bankAccountId")
	if !ids.Valid(account) {
		validationFailed(w, r, map[string]string{"bankAccountId": "required UUID query parameter"})
		return
	}
	items, err := a.store.ListChecks(r.Context(), account)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getCheck(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodeCheckNotFound, "check not found")
	if !ok {
		return
	}
	c, err := a.store.GetCheck(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	setETag(w, c.Version)
	httpx.JSON(w, http.StatusOK, c)
}

// checkTransition runs a custom method on one check under If-Match.
func (a *API) checkTransition(status int, act func(id string, version int) (store.Check, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		id, ok := pathID(w, r, store.CodeCheckNotFound, "check not found")
		if !ok {
			return
		}
		version, ok := ifMatchVersion(w, r)
		if !ok {
			return
		}
		c, err := act(id, version)
		if err != nil {
			httpx.Fail(w, r, problemFrom(err))
			return
		}
		setETag(w, c.Version)
		httpx.JSON(w, status, c)
	}
}

func (a *API) printCheck(w http.ResponseWriter, r *http.Request) {
	a.checkTransition(http.StatusOK, func(id string, v int) (store.Check, error) {
		return a.store.PrintCheck(r.Context(), id, v)
	})(w, r)
}

// sendCheck records that a printed check left the company; from then on a
// void also voids the positive pay record (BR-36.5).
func (a *API) sendCheck(w http.ResponseWriter, r *http.Request) {
	a.checkTransition(http.StatusOK, func(id string, v int) (store.Check, error) {
		return a.store.SendCheck(r.Context(), id, v)
	})(w, r)
}

// clearCheck records that the bank paid a sent check; a cleared check can no
// longer be voided (TRS-0414).
func (a *API) clearCheck(w http.ResponseWriter, r *http.Request) {
	a.checkTransition(http.StatusOK, func(id string, v int) (store.Check, error) {
		return a.store.ClearCheck(r.Context(), id, v)
	})(w, r)
}

// stopCheck records a stop-payment order on a sent check.
func (a *API) stopCheck(w http.ResponseWriter, r *http.Request) {
	a.checkTransition(http.StatusOK, func(id string, v int) (store.Check, error) {
		return a.store.StopCheck(r.Context(), id, v)
	})(w, r)
}

// voidCheck voids a check; a reason is required (check.voidReasonRequired default true).
func (a *API) voidCheck(w http.ResponseWriter, r *http.Request) {
	reason, ok := readReason(w, r)
	if !ok {
		return
	}
	a.checkTransition(http.StatusOK, func(id string, v int) (store.Check, error) {
		c, err := a.store.VoidCheck(r.Context(), id, v, reason)
		if err == nil {
			a.checksVoided.Add(1)
		}
		return c, err
	})(w, r)
}

// reprintCheck voids the check and returns the new Printed check (AC-36.5).
// If-Match carries the old check's ETag; the response ETag is the new check's.
func (a *API) reprintCheck(w http.ResponseWriter, r *http.Request) {
	reason, ok := readReason(w, r)
	if !ok {
		return
	}
	a.checkTransition(http.StatusCreated, func(id string, v int) (store.Check, error) {
		c, err := a.store.ReprintCheck(r.Context(), id, v, reason)
		if err == nil {
			a.checksVoided.Add(1)
			a.checksIssued.Add(1)
		}
		return c, err
	})(w, r)
}

// readReason decodes and validates the reason body of void and reprint.
func readReason(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req checkReasonRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return "", false
	}
	if !domain.ValidVoidReason(req.Reason) {
		validationFailed(w, r, map[string]string{"reason": "required, 1 to 200 characters"})
		return "", false
	}
	return strings.TrimSpace(req.Reason), true
}
