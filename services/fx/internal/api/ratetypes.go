package api

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/fx/internal/store"
)

type rateTypeBody struct {
	Name         string `json:"name"`
	AllowInverse bool   `json:"allowInverse"`
	Provider     string `json:"provider"`
}

func (a *API) fail(w http.ResponseWriter, r *http.Request, msg string, err error) {
	slog.Error(msg, "err", err, "correlationId", middleware.GetReqID(r.Context()))
	httpx.Fail(w, r, toProblem(err))
}

func (a *API) listRateTypes(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	types, err := a.store.ListRateTypes(r.Context())
	if err != nil {
		a.fail(w, r, "list rate types failed", err)
		return
	}
	httpx.JSON(w, http.StatusOK, types)
}

func (a *API) getRateType(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	t, err := a.store.GetRateType(r.Context(), chi.URLParam(r, "code"))
	if err != nil {
		a.fail(w, r, "get rate type failed", err)
		return
	}
	httpx.JSON(w, http.StatusOK, t)
}

func (a *API) putRateType(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var body rateTypeBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	t, err := a.store.PutRateType(r.Context(), store.RateType{Code: chi.URLParam(r, "code"), Name: body.Name, AllowInverse: body.AllowInverse, Provider: body.Provider})
	if err != nil {
		a.fail(w, r, "put rate type failed", err)
		return
	}
	httpx.JSON(w, http.StatusOK, t)
}
