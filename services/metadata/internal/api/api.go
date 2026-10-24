package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/metadata/internal/domain"
	"github.com/BenjaminSRussell/passage/services/metadata/internal/gen"
	"github.com/BenjaminSRussell/passage/services/metadata/internal/registry"
)

const (
	dutyRead      = "metadata.read"
	dutyWrite     = "metadata.write"
	dutyDataRead  = "metadata.data.read"
	dutyDataWrite = "metadata.data.write"
)

type API struct {
	store registry.Store
}

func New(store registry.Store) *API { return &API{store: store} }

func (api *API) Register(router chi.Router) {
	api.registerRoutes(router)
	router.Route("/api", api.registerRoutes)
}

func (api *API) registerRoutes(router chi.Router) {
	router.Get("/readyz", api.ready)
	router.With(authz.RequireDuty(dutyWrite)).Post("/v1/metadata/entities", api.publish)
	router.With(authz.RequireDuty(dutyRead)).Get("/v1/metadata/entities/{entity}/schema", api.schema)
	router.With(authz.RequireDuty(dutyWrite)).Post("/v1/metadata/extensions", api.extend)
	router.With(authz.RequireDuty(dutyDataWrite)).Post("/v1/data/{entity}", api.createRecord)
	router.With(authz.RequireDuty(dutyDataRead)).Get("/v1/data/{entity}", api.listRecords)
	router.With(authz.RequireDuty(dutyRead)).Post("/v1/metadata/rules/evaluate", api.evaluate)
}

func (api *API) ready(writer http.ResponseWriter, request *http.Request) {
	if err := api.store.Ping(request.Context()); err != nil {
		httpx.JSON(writer, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	httpx.JSON(writer, http.StatusOK, map[string]string{"status": "ready"})
}

func (api *API) publish(writer http.ResponseWriter, request *http.Request) {
	principal, ok := tenant.FromContext(request.Context())
	if !ok {
		fail(writer, request, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "tenant is required"))
		return
	}
	var entity domain.Entity
	if err := httpx.Decode(request, &entity); err != nil {
		fail(writer, request, err)
		return
	}
	version, err := api.store.Publish(request.Context(), principal.TenantID, &entity)
	if err != nil {
		fail(writer, request, err)
		return
	}
	httpx.JSON(writer, http.StatusCreated, version)
}

func (api *API) schema(writer http.ResponseWriter, request *http.Request) {
	principal, _ := tenant.FromContext(request.Context())
	entity, err := api.store.Current(request.Context(), principal.TenantID, chi.URLParam(request, "entity"))
	if err != nil {
		fail(writer, request, err)
		return
	}
	schema, err := gen.JSONSchema(entity)
	if err != nil {
		fail(writer, request, err)
		return
	}
	writer.Header().Set("Content-Type", "application/schema+json")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte(schema))
}

func (api *API) extend(writer http.ResponseWriter, request *http.Request) {
	principal, _ := tenant.FromContext(request.Context())
	var extension domain.Extension
	if err := httpx.Decode(request, &extension); err != nil {
		fail(writer, request, err)
		return
	}
	version, err := api.store.Extend(request.Context(), principal.TenantID, extension)
	if err != nil {
		fail(writer, request, err)
		return
	}
	httpx.JSON(writer, http.StatusCreated, version)
}

func (api *API) createRecord(writer http.ResponseWriter, request *http.Request) {
	principal, _ := tenant.FromContext(request.Context())
	var input map[string]any
	if err := httpx.Decode(request, &input); err != nil {
		fail(writer, request, err)
		return
	}
	record, err := api.store.CreateRecord(request.Context(), principal.TenantID, chi.URLParam(request, "entity"), input)
	if err != nil {
		fail(writer, request, err)
		return
	}
	httpx.JSON(writer, http.StatusCreated, record)
}

func (api *API) listRecords(writer http.ResponseWriter, request *http.Request) {
	principal, _ := tenant.FromContext(request.Context())
	limit := 100
	if text := request.URL.Query().Get("limit"); text != "" {
		parsed, err := strconv.Atoi(text)
		if err != nil || parsed < 1 || parsed > 100 {
			fail(writer, request, httpx.NewProblem(http.StatusBadRequest, "MET-0401", "limit must be 1..100"))
			return
		}
		limit = parsed
	}
	records, err := api.store.ListRecords(request.Context(), principal.TenantID, chi.URLParam(request, "entity"), limit)
	if err != nil {
		fail(writer, request, err)
		return
	}
	httpx.JSON(writer, http.StatusOK, records)
}

type evaluateRequest struct {
	Table domain.DecisionTable `json:"table"`
	Input map[string]any       `json:"input"`
	At    *time.Time           `json:"at,omitempty"`
}

func (api *API) evaluate(writer http.ResponseWriter, request *http.Request) {
	var input evaluateRequest
	if err := httpx.Decode(request, &input); err != nil {
		fail(writer, request, err)
		return
	}
	at := time.Now().UTC()
	if input.At != nil {
		at = *input.At
	}
	result, err := domain.Evaluate(input.Table, input.Input, at)
	if err != nil {
		fail(writer, request, err)
		return
	}
	httpx.JSON(writer, http.StatusOK, result)
}

func fail(writer http.ResponseWriter, request *http.Request, err error) {
	if errors.Is(err, registry.ErrNotFound) {
		httpx.Fail(writer, request, httpx.NewProblem(http.StatusNotFound, "MET-0404", "entity definition was not found"))
		return
	}
	var violation domain.Violation
	if errors.As(err, &violation) {
		status := http.StatusBadRequest
		if violation.Code == "MET-0201" {
			status = http.StatusConflict
		}
		httpx.Fail(writer, request, httpx.NewProblem(status, violation.Code, err.Error()))
		return
	}
	if strings.Contains(err.Error(), "MET-0201") {
		httpx.Fail(writer, request, httpx.NewProblem(http.StatusConflict, "MET-0201", err.Error()))
		return
	}
	httpx.Fail(writer, request, err)
}
