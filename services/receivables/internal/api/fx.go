package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

const (
	dutyFXSetting      = "receivables.fxsetting.manage"
	dutyPaymentReverse = "receivables.payment.reverse"
)

// fxRoutes mounts the realized-FX profile of a legal entity (BR-30.4): whether it settles across currencies, its functional
// currency and the gain and loss accounts.
func (a *API) fxRoutes(r chi.Router) {
	r.With(authz.RequireDuty(dutyView)).Get("/fx-settings", a.getFXSetting)
	r.With(authz.RequireDuty(dutyFXSetting)).Put("/fx-settings", a.putFXSetting)
}

func (a *API) getFXSetting(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	le := r.URL.Query().Get("legalEntityId")
	if !ids.Valid(le) {
		badQuery(w, r, "legalEntityId", "must be a UUID")
		return
	}
	if !scopeAllows(w, r, le) {
		return
	}
	out, err := a.store.GetFXSetting(r.Context(), le)
	if err != nil {
		a.fail("get fx setting", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) putFXSetting(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var in store.FXSetting
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !ids.Valid(in.LegalEntityID) {
		writeFields(w, r, map[string]string{"legalEntityId": "must be a UUID"})
		return
	}
	if !scopeAllows(w, r, in.LegalEntityID) {
		return
	}
	if err := store.ValidateFXSetting(&in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out, err := a.store.PutFXSetting(r.Context(), in)
	if err != nil {
		a.fail("save fx setting", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

type unapplyRequest struct {
	Reason string `json:"reason"`
}

// unapplyPayment reverses every live application of a payment (#30): the items it settled are open again by what it took off them
// and the cash is unapplied; If-Match carries the payment version the user reviewed.
func (a *API) unapplyPayment(w http.ResponseWriter, r *http.Request, id string) {
	if !require(w, r, dutyPaymentReverse) {
		return
	}
	version, err := ifMatch(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var in unapplyRequest
	if r.ContentLength != 0 {
		if err := httpx.Decode(r, &in); err != nil {
			httpx.Fail(w, r, err)
			return
		}
	}
	if len(in.Reason) > 500 {
		writeFields(w, r, map[string]string{"reason": "must be at most 500 characters"})
		return
	}
	out, err := a.store.UnapplyPayment(r.Context(), id, version, in.Reason)
	if err != nil {
		a.fail("unapply payment", w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Payment.Version))
	httpx.JSON(w, http.StatusOK, out)
}

// reversePayment undoes the cash receipt of a payment that was never applied; the journal it queues mirrors the receipt's own
// journal, so the bank account the receipt was booked to is the one that is credited back.
func (a *API) reversePayment(w http.ResponseWriter, r *http.Request, id string) {
	if !require(w, r, dutyPaymentReverse) {
		return
	}
	version, err := ifMatch(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out, err := a.store.ReversePayment(r.Context(), id, version)
	if err != nil {
		a.fail("reverse payment", w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}
