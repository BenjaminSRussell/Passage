package api

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
)

const (
	dutyIntrastatFeed = "tax.intrastat.feed"
	maxMovements      = 500
)

func (a *API) registerIntrastatFeed(r chi.Router) {
	read, master, manage, feed := authz.RequireDuty(dutyIntrastatRead), authz.RequireDuty(dutyIntrastatMaster), authz.RequireDuty(dutyIntrastatManage), authz.RequireDuty(dutyIntrastatFeed)
	r.With(read).Get("/v1/tax/intrastat/products", a.listIntrastatProducts)
	r.With(master).Put("/v1/tax/intrastat/products/{product_id}", a.putIntrastatProduct)
	r.With(master).Delete("/v1/tax/intrastat/products/{product_id}", a.deleteIntrastatProduct)
	r.With(feed).Post("/v1/tax/intrastat/movements", a.stageMovements)
	r.With(read).Get("/v1/tax/intrastat/movements", a.listMovements)
	r.With(manage).Post("/v1/tax/intrastat/journals/{id}/transfer-movements", a.transferMovements)
}

func (a *API) listIntrastatProducts(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	out, err := a.config.ListIntrastatProducts(r.Context())
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (a *API) putIntrastatProduct(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	var p domain.IntrastatProduct
	if err := httpx.Decode(r, &p); err != nil {
		a.reject(w, r, err)
		return
	}
	id := chi.URLParam(r, "product_id")
	if p.ProductID != "" && p.ProductID != id {
		a.reject(w, r, invalid("product_id", "the product_id in the body must match the one in the path"))
		return
	}
	p.ProductID = id
	err := domain.ValidateIntrastatProduct(p)
	if err == nil {
		err = a.config.PutIntrastatProduct(r.Context(), p)
	}
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, p)
}

func (a *API) deleteIntrastatProduct(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	if err := a.config.DeleteIntrastatProduct(r.Context(), chi.URLParam(r, "product_id")); err != nil {
		a.reject(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type movementsRequest struct {
	Movements []domain.Movement `json:"movements"`
}

func (a *API) stageMovements(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	var req movementsRequest
	if err := httpx.Decode(r, &req); err != nil {
		a.reject(w, r, err)
		return
	}
	if len(req.Movements) == 0 || len(req.Movements) > maxMovements {
		a.reject(w, r, invalid("movements", "movements must hold between 1 and %d entries", maxMovements))
		return
	}
	seen := map[string]bool{}
	for i, m := range req.Movements {
		if err := domain.ValidateMovement(m); err != nil {
			de := err.(*domain.Error)
			a.reject(w, r, invalid(fmt.Sprintf("movements[%d].%s", i, de.Field), "%s", de.Detail))
			return
		}
		if seen[m.SourceRef] {
			a.reject(w, r, invalid(fmt.Sprintf("movements[%d].source_ref", i), "source_ref %s appears twice", m.SourceRef))
			return
		}
		seen[m.SourceRef] = true
	}
	n, err := a.config.StageMovements(r.Context(), req.Movements)
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]int{"staged": n, "duplicates": len(req.Movements) - n})
}

func (a *API) listMovements(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	status := r.URL.Query().Get("status")
	if status != "" && status != "staged" && status != "transferred" && status != "ignored" {
		a.reject(w, r, invalid("status", "status must be staged, transferred or ignored"))
		return
	}
	out, err := a.config.ListMovements(r.Context(), status)
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (a *API) transferMovements(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	var d domain.MovementDefaults
	if r.ContentLength != 0 {
		if err := httpx.Decode(r, &d); err != nil {
			a.reject(w, r, err)
			return
		}
	}
	out, err := a.config.TransferMovements(r.Context(), chi.URLParam(r, "id"), d)
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
