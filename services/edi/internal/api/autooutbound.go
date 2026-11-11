package api

import (
	"errors"
	"net/http"
	"slices"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"

	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
)

type autoOutboundBody struct {
	TransactionSets []string `json:"transactionSets"`
}

var autoSets = []string{"856", "810", "DESADV", "INVOIC"}

// putAutoOutbound sets the documents a partner receives without anyone asking: the 856 or DESADV when a shipment ships and the
// 810 or INVOIC when an invoice posts (35-1). An empty list turns it off. The partner needs a trade profile; the legal entity is
// the profile's, which is the entity whose events the consumer reads.
func (a *API) putAutoOutbound(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validID(id) {
		httpx.Fail(w, r, notFound("partner"))
		return
	}
	var body autoOutboundBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	seen := map[string]bool{}
	for _, s := range body.TransactionSets {
		if !slices.Contains(autoSets, s) || seen[s] {
			httpx.Fail(w, r, validation(map[string]string{"transactionSets": "must list each of 856, 810, DESADV, INVOIC at most once"}))
			return
		}
		seen[s] = true
	}
	if _, err := a.store.GetPartner(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		httpx.Fail(w, r, notFound("partner"))
		return
	} else if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	sets, err := a.store.PutAutoOutbound(r.Context(), id, body.TransactionSets)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeNoTradeProfile, "partner has no trade profile; PUT /partners/{id}/trade-profile first"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if sets == nil {
		sets = []string{}
	}
	httpx.JSON(w, http.StatusOK, autoOutboundBody{TransactionSets: sets})
}

func (a *API) getAutoOutbound(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validID(id) {
		httpx.Fail(w, r, notFound("partner"))
		return
	}
	if _, err := a.store.GetPartner(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		httpx.Fail(w, r, notFound("partner"))
		return
	} else if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	sets, err := a.store.GetAutoOutbound(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, autoOutboundBody{TransactionSets: sets})
}
