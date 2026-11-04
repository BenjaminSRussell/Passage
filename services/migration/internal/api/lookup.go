package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
)

const lookupPath = "/v1/integration/lookup-tables"

type createLookupRequest struct {
	Name       string          `json:"name"`
	Definition json.RawMessage `json:"definition"`
}

type updateLookupRequest struct {
	Version    *int            `json:"version"`
	Name       *string         `json:"name"`
	Status     *string         `json:"status"`
	Definition json.RawMessage `json:"definition"`
}

func (a *API) registerLookup(r chi.Router) {
	r.With(authz.RequireDuty(dutyProjectWrite)).Post(lookupPath, a.createLookup)
	r.With(authz.RequireDuty(dutyRun)).Get(lookupPath, a.listLookup)
	r.With(authz.RequireDuty(dutyRun)).Get(lookupPath+"/{id}", a.getLookup)
	r.With(authz.RequireDuty(dutyRun)).Get(lookupPath+"/{id}/versions", a.listLookupVersions)
	r.With(authz.RequireDuty(dutyProjectWrite)).Patch(lookupPath+"/{id}", a.updateLookup)
	r.With(authz.RequireDuty(dutyProjectWrite)).Post(lookupPath+"/{id}:import", a.importLookup)
	r.With(authz.RequireDuty(dutyRun)).Get(lookupPath+"/{id}/entries.csv", a.exportLookup)
}

func parseLookupDefinition(raw json.RawMessage) (domain.LookupDefinition, []byte, error) {
	if len(raw) == 0 {
		return domain.LookupDefinition{}, nil, validation(map[string]string{"definition": "is required"})
	}
	def, err := domain.ParseLookupDefinition(raw)
	if err != nil {
		return def, nil, definitionProblem(err)
	}
	canonical, err := json.Marshal(def)
	return def, canonical, err
}

func validLookupName(name string) bool { return domain.ValidLookupTableName(name) }

const lookupNameHint = "is required: start with a letter, then letters, digits, _ . -, at most 64 characters"

func (a *API) createLookup(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	var req createLookupRequest
	if err := httpx.Decode(r, &req); err != nil {
		fail(w, r, err)
		return
	}
	if !validLookupName(req.Name) {
		fail(w, r, validation(map[string]string{"name": lookupNameHint}))
		return
	}
	def, canonical, err := parseLookupDefinition(req.Definition)
	if err != nil {
		fail(w, r, err)
		return
	}
	saved, created, err := a.store.SaveLookupTable(r.Context(), store.LookupTable{Name: req.Name, Kind: def.Kind, Definition: canonical}, r.Header.Get(idempotencyHeader))
	if err != nil {
		a.logFailure(r, "create lookup table failed", err)
		fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		w.Header().Set("Location", lookupPath+"/"+saved.ID)
	}
	httpx.JSON(w, status, saved)
}

func (a *API) listLookup(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}
	if limit == 0 {
		limit = defaultPage
	}
	q := r.URL.Query()
	bad := map[string]string{}
	if v := q.Get("status"); v != "" && v != store.OutboundActive && v != store.OutboundArchivedStatus {
		bad["status"] = "must be active or archived"
	}
	if v := q.Get("kind"); v != "" && !domain.ValidLookupKind(v) {
		bad["kind"] = "must be enum, uom_conversion or price_list"
	}
	var after *store.ProjectCursor
	if v := q.Get("cursor"); v != "" {
		c, ok := decodeProjectCursor(v)
		if !ok {
			bad["cursor"] = "is not a cursor returned by this endpoint"
		}
		after = c
	}
	if len(bad) > 0 {
		fail(w, r, validation(bad))
		return
	}
	rows, err := a.store.ListLookupTables(r.Context(), store.LookupFilter{Status: q.Get("status"), Kind: q.Get("kind"), After: after, Limit: limit + 1})
	if err != nil {
		a.logFailure(r, "list lookup tables failed", err)
		fail(w, r, err)
		return
	}
	body := map[string]any{}
	if len(rows) > limit {
		rows = rows[:limit]
		body["nextCursor"] = encodeProjectCursor(store.ProjectCursor{Name: rows[limit-1].Name, ID: rows[limit-1].ID})
	}
	if rows == nil {
		rows = []store.LookupTable{}
	}
	body["items"] = rows
	httpx.JSON(w, http.StatusOK, body)
}

func (a *API) getLookup(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	version := 0
	if v := r.URL.Query().Get("version"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			fail(w, r, validation(map[string]string{"version": "must be a positive integer"}))
			return
		}
		version = n
	}
	t, err := a.store.GetLookupTable(r.Context(), chi.URLParam(r, "id"), version)
	if err != nil {
		a.logFailure(r, "get lookup table failed", err)
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, t)
}

func (a *API) listLookupVersions(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	rows, err := a.store.ListLookupVersions(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.logFailure(r, "list lookup table versions failed", err)
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": rows})
}

// updateLookup writes the next version. A definition replaces all entries; a rename or archive carries the current
// definition into the new version, so version N always means one complete table.
func (a *API) updateLookup(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	var req updateLookupRequest
	if err := httpx.Decode(r, &req); err != nil {
		fail(w, r, err)
		return
	}
	if req.Version == nil || *req.Version < 1 {
		fail(w, r, validation(map[string]string{"version": "is required and must be at least 1"}))
		return
	}
	bad := map[string]string{}
	if req.Name != nil && !validLookupName(*req.Name) {
		bad["name"] = lookupNameHint
	}
	if req.Status != nil && *req.Status != store.OutboundActive && *req.Status != store.OutboundArchivedStatus {
		bad["status"] = "must be active or archived"
	}
	if len(bad) > 0 {
		fail(w, r, validation(bad))
		return
	}
	saved, err := a.store.UpdateLookupTable(r.Context(), chi.URLParam(r, "id"), *req.Version, func(t *store.LookupTable) error {
		if t.Status == store.OutboundArchivedStatus && (req.Status == nil || *req.Status == store.OutboundArchivedStatus) {
			return httpx.NewProblem(http.StatusConflict, store.CodeLookupArchived, "an archived lookup table can only be reactivated")
		}
		if req.Name != nil {
			t.Name = *req.Name
		}
		if req.Status != nil {
			t.Status = *req.Status
		}
		raw := t.Definition
		if len(req.Definition) > 0 {
			raw = req.Definition
		}
		def, canonical, err := parseLookupDefinition(raw)
		if err != nil {
			return err
		}
		if len(req.Definition) > 0 && def.Kind != t.Kind {
			return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeLookupInvalid, "the kind of a table cannot change: "+t.Kind)
		}
		t.Kind, t.Definition = def.Kind, canonical
		return nil
	})
	if err != nil {
		var p *httpx.Problem
		if !errors.As(err, &p) {
			a.logFailure(r, "update lookup table failed", err)
		}
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, saved)
}

type importLookupRequest struct {
	Version   *int   `json:"version"`
	Mode      string `json:"mode"`
	HasHeader bool   `json:"hasHeader"`
	CSV       string `json:"csv"`
}

type importLookupResponse struct {
	Table   store.LookupTable          `json:"table"`
	Summary domain.LookupImportSummary `json:"summary"`
	// Written is false when the file changed nothing: no version was saved and Table is the current one.
	Written bool `json:"written"`
}

var errLookupImportNoChange = errors.New("lookup import changes nothing")

// importLookup merges (or replaces with) the entries of a CSV file into the table as its next version, under the same
// optimistic version as PATCH. A file that changes nothing writes no version.
func (a *API) importLookup(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	var req importLookupRequest
	if err := httpx.DecodeLimit(r, &req, 3<<20); err != nil {
		fail(w, r, err)
		return
	}
	bad := map[string]string{}
	if req.Version == nil || *req.Version < 1 {
		bad["version"] = "is required and must be at least 1"
	}
	if req.Mode != "merge" && req.Mode != "replace" {
		bad["mode"] = "must be merge (keep the entries the file does not mention) or replace"
	}
	if strings.TrimSpace(req.CSV) == "" {
		bad["csv"] = "is required"
	}
	if len(bad) > 0 {
		fail(w, r, validation(bad))
		return
	}
	var sum domain.LookupImportSummary
	saved, err := a.store.UpdateLookupTable(r.Context(), chi.URLParam(r, "id"), *req.Version, func(t *store.LookupTable) error {
		if t.Status == store.OutboundArchivedStatus {
			return httpx.NewProblem(http.StatusConflict, store.CodeLookupArchived, "an archived lookup table can only be reactivated")
		}
		cur, err := domain.ParseLookupDefinition(t.Definition)
		if err != nil {
			return definitionProblem(err)
		}
		file, err := domain.ParseLookupCSV(cur.Kind, []byte(req.CSV), req.HasHeader)
		if err != nil {
			return definitionProblem(err)
		}
		var merged map[string]string
		merged, sum = domain.ApplyLookupImport(cur.Entries, file.Entries, req.Mode == "replace")
		if sum.Added == 0 && sum.Changed == 0 && sum.Removed == 0 {
			return errLookupImportNoChange
		}
		cur.Entries = merged
		_, canonical, err := parseLookupDefinition(mustJSON(cur))
		if err != nil {
			return err
		}
		t.Definition = canonical
		return nil
	})
	written := true
	if errors.Is(err, errLookupImportNoChange) {
		written = false
		saved, err = a.store.GetLookupTable(r.Context(), chi.URLParam(r, "id"), 0)
		if err == nil && saved.Version != *req.Version {
			err = httpx.NewProblem(http.StatusConflict, store.CodeVersionConflict, "current version is "+strconv.Itoa(saved.Version))
		}
	}
	if err != nil {
		var p *httpx.Problem
		if !errors.As(err, &p) {
			a.logFailure(r, "import lookup entries failed", err)
		}
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, importLookupResponse{Table: saved, Summary: sum, Written: written})
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// exportLookup answers the entries of a version (the current one by default) as CSV for a spreadsheet.
func (a *API) exportLookup(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	version := 0
	if v := r.URL.Query().Get("version"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			fail(w, r, validation(map[string]string{"version": "must be a positive integer"}))
			return
		}
		version = n
	}
	t, err := a.store.GetLookupTable(r.Context(), chi.URLParam(r, "id"), version)
	if err != nil {
		a.logFailure(r, "export lookup table failed", err)
		fail(w, r, err)
		return
	}
	def, err := domain.ParseLookupDefinition(t.Definition)
	if err != nil {
		fail(w, r, definitionProblem(err))
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+t.Name+`-v`+strconv.Itoa(t.Version)+`.csv"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(domain.LookupCSV(def.Entries))
}
