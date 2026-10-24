package generated

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/BenjaminSRussell/passage/platform/httpx"
)

func RegisterVendorStubRoutes(router chi.Router, repository VendorStubRepository) {
	router.Get("/v1/vendor/vendor-stubs", func(w http.ResponseWriter, r *http.Request) {
		rows, err := repository.List(r.Context(), 100)
		if err != nil { httpx.Fail(w, r, err); return }
		httpx.JSON(w, http.StatusOK, rows)
	})
	router.Post("/v1/vendor/vendor-stubs", func(w http.ResponseWriter, r *http.Request) {
		var input VendorStub
		if err := httpx.Decode(r, &input); err != nil { httpx.Fail(w, r, err); return }
		row, err := repository.Create(r.Context(), input)
		if err != nil { httpx.Fail(w, r, err); return }
		httpx.JSON(w, http.StatusCreated, row)
	})
	router.Patch("/v1/vendor/vendor-stubs/{id}", func(w http.ResponseWriter, r *http.Request) {
		var patch map[string]any
		if err := httpx.Decode(r, &patch); err != nil { httpx.Fail(w, r, err); return }
		version, err := strconv.Atoi(r.Header.Get("If-Match"))
		if err != nil { httpx.Fail(w, r, httpx.NewProblem(428, "MET-0008", "If-Match is required")); return }
		row, err := repository.Update(r.Context(), chi.URLParam(r, "id"), patch, version)
		if err != nil { httpx.Fail(w, r, err); return }
		httpx.JSON(w, http.StatusOK, row)
	})
}
