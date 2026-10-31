package api

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/store"
)

// dutyRulesWrite is "Maintain validation and derivation rules" (PRD §2.11.17).
const dutyRulesWrite = "ledger.rules.write"

// registerDerivation mounts lookup tables and derivation strategies (FR-11.22).
func (a *API) registerDerivation(r chi.Router) {
	r.With(authz.RequireDuty(dutyRead)).Get("/ledgers/{ledger}/lookup-tables", a.listLookupTables)
	r.With(authz.RequireDuty(dutyRead)).Get("/ledgers/{ledger}/lookup-tables/{id}", a.getLookupTable)
	r.With(authz.RequireDuty(dutyRulesWrite)).Put("/ledgers/{ledger}/lookup-tables/{id}", a.putLookupTable)
	r.With(authz.RequireDuty(dutyRead)).Get("/ledgers/{ledger}/derivation-strategies", a.listStrategies)
	r.With(authz.RequireDuty(dutyRead)).Get("/ledgers/{ledger}/derivation-strategies/{version}", a.getStrategy)
	r.With(authz.RequireDuty(dutyRulesWrite)).Put("/ledgers/{ledger}/derivation-strategies/{version}", a.putStrategy)
	r.With(authz.RequireDuty(dutyRulesWrite)).Post("/ledgers/{ledger}/derivation-strategies/{version}/activate", a.activateStrategy)
}

// --- lookup tables --------------------------------------------------------------

type lookupRequest struct {
	Name         string             `json:"name"`
	SourceFields []string           `json:"sourceFields"`
	Target       string             `json:"target"`
	Rows         []domain.LookupRow `json:"rows"`
}

func writeLookup(w http.ResponseWriter, status int, t domain.LookupTable) {
	w.Header().Set("ETag", strconv.Quote(strconv.Itoa(t.Version)))
	httpx.JSON(w, status, t)
}

func (a *API) listLookupTables(w http.ResponseWriter, r *http.Request) {
	ledgerID, ok := pathLedger(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListLookupTables(r.Context(), ledgerID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func lookupID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeLookupNotFound, "lookup table not found"))
		return "", false
	}
	return id, true
}

func (a *API) getLookupTable(w http.ResponseWriter, r *http.Request) {
	ledgerID, ok := pathLedger(w, r)
	if !ok {
		return
	}
	id, ok := lookupID(w, r)
	if !ok {
		return
	}
	t, err := a.store.GetLookupTable(r.Context(), ledgerID, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeLookup(w, http.StatusOK, t)
}

// putLookupTable creates (201) or replaces (200, checked against If-Match when sent)
// a lookup table with all its rows. Row and field rules are checked in the store
// against the ledger's dimension attributes (422 LEDGER_LOOKUP_TABLE_INVALID).
func (a *API) putLookupTable(w http.ResponseWriter, r *http.Request) {
	ledgerID, ok := pathLedger(w, r)
	if !ok {
		return
	}
	var req lookupRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		fields["id"] = "must be a UUID"
	}
	version, versionOK := parseIfMatch(r.Header.Get("If-Match"))
	if !versionOK {
		fields["If-Match"] = `must be the lookup table version as an entity tag, for example "3"`
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if req.SourceFields == nil {
		req.SourceFields = []string{}
	}
	t, created, err := a.store.PutLookupTable(r.Context(), ledgerID, id, version, domain.LookupTable{Name: req.Name,
		SourceFields: req.SourceFields, Target: req.Target, Rows: req.Rows})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeLookup(w, status, t)
}

// --- strategies -----------------------------------------------------------------

type strategyRequest struct {
	Name  string                  `json:"name"`
	Steps []domain.DerivationStep `json:"steps"`
}

func writeStrategy(w http.ResponseWriter, status int, d domain.DerivationStrategy) {
	w.Header().Set("ETag", strconv.Quote(strconv.Itoa(d.LockVersion)))
	httpx.JSON(w, status, d)
}

// strategyVersion reads the {version} path segment, a positive integer.
func strategyVersion(w http.ResponseWriter, r *http.Request) (int, bool) {
	n, err := strconv.Atoi(chi.URLParam(r, "version"))
	if err != nil || n < 1 {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeStrategyNotFound, "derivation strategy version not found"))
		return 0, false
	}
	return n, true
}

func (a *API) listStrategies(w http.ResponseWriter, r *http.Request) {
	ledgerID, ok := pathLedger(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListDerivationStrategies(r.Context(), ledgerID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getStrategy(w http.ResponseWriter, r *http.Request) {
	ledgerID, ok := pathLedger(w, r)
	if !ok {
		return
	}
	version, ok := strategyVersion(w, r)
	if !ok {
		return
	}
	d, err := a.store.GetDerivationStrategy(r.Context(), ledgerID, version)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeStrategy(w, http.StatusOK, d)
}

// putStrategy creates (201) the next draft version or replaces (200) a draft.
func (a *API) putStrategy(w http.ResponseWriter, r *http.Request) {
	ledgerID, ok := pathLedger(w, r)
	if !ok {
		return
	}
	version, ok := strategyVersion(w, r)
	if !ok {
		return
	}
	var req strategyRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !domain.ValidDerivationName(req.Name) {
		fields["name"] = "must be 1 to 80 characters"
	}
	for i, st := range req.Steps {
		if st.ID != "" && !ids.Valid(st.ID) {
			fields["steps["+strconv.Itoa(i)+"].id"] = "must be a UUID when given"
		}
		if req.Steps[i].SourceFields == nil {
			req.Steps[i].SourceFields = []string{}
		}
	}
	lockVersion, lockOK := parseIfMatch(r.Header.Get("If-Match"))
	if !lockOK {
		fields["If-Match"] = `must be the strategy lockVersion as an entity tag, for example "3"`
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	d, created, err := a.store.PutDerivationStrategy(r.Context(), ledgerID, version, lockVersion,
		domain.DerivationStrategy{Name: req.Name, Steps: req.Steps})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeStrategy(w, status, d)
}

// activateStrategy makes a draft the ledger's active strategy (BR-11.14).
func (a *API) activateStrategy(w http.ResponseWriter, r *http.Request) {
	ledgerID, ok := pathLedger(w, r)
	if !ok {
		return
	}
	version, ok := strategyVersion(w, r)
	if !ok {
		return
	}
	lockVersion, lockOK := parseIfMatch(r.Header.Get("If-Match"))
	if !lockOK {
		validationFailed(w, r, map[string]string{"If-Match": `must be the strategy lockVersion as an entity tag, for example "3"`})
		return
	}
	d, err := a.store.ActivateDerivationStrategy(r.Context(), ledgerID, version, lockVersion)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeStrategy(w, http.StatusOK, d)
}
