package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
)

// constraintBody is one constraint row of an account structure (FR-11.3):
// the allowed values of segment, applying when the Under values match.
type constraintBody struct {
	Segment string            `json:"segment"`
	Under   map[string]string `json:"under,omitempty"`
	Allowed []string          `json:"allowed"`
}

// structureBody is the PUT body of an account structure version.
type structureBody struct {
	Name          string           `json:"name"`
	Version       int              `json:"version"`
	Priority      int              `json:"priority"`
	EffectiveFrom string           `json:"effectiveFrom"`
	Status        string           `json:"status"`
	Segments      []string         `json:"segments"`
	AccountFilter []string         `json:"accountFilter"`
	Constraints   []constraintBody `json:"constraints"`
}

// structureView is an account structure version as returned by the API.
type structureView struct {
	ID            string           `json:"id"`
	LedgerID      string           `json:"ledgerId"`
	Name          string           `json:"name"`
	Version       int              `json:"version"`
	Priority      int              `json:"priority"`
	EffectiveFrom string           `json:"effectiveFrom"`
	Status        string           `json:"status"`
	Segments      []string         `json:"segments"`
	AccountFilter []string         `json:"accountFilter"`
	Constraints   []constraintBody `json:"constraints"`
	LockVersion   int              `json:"lockVersion"`
}

// dimensionValueBody is the PUT body of a registered dimension value.
type dimensionValueBody struct {
	Name       string  `json:"name"`
	ActiveFrom *string `json:"activeFrom"`
	ActiveTo   *string `json:"activeTo"`
	Suspended  bool    `json:"suspended"`
}

// dimensionValueView is a registered dimension value as returned by the API.
type dimensionValueView struct {
	Attribute   string  `json:"attribute"`
	Value       string  `json:"value"`
	Name        string  `json:"name"`
	ActiveFrom  *string `json:"activeFrom,omitempty"`
	ActiveTo    *string `json:"activeTo,omitempty"`
	Suspended   bool    `json:"suspended"`
	LockVersion int     `json:"lockVersion"`
}

// ledgerParam reads the {ledger} path segment; a malformed id cannot name a
// ledger, so it is 404 like GET /ledgers/{id}.
func ledgerParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "ledger")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "LEDGER_NOT_FOUND", "ledger not found"))
		return "", false
	}
	return id, true
}

func (a *API) listStructures(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	ledgerID, ok := ledgerParam(w, r)
	if !ok {
		return
	}
	list, err := a.store.ListStructures(r.Context(), ledgerID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	items := make([]structureView, 0, len(list))
	for _, s := range list {
		items = append(items, toStructureView(ledgerID, s))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getStructure(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	ledgerID, ok := ledgerParam(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeStructureNotFound, "account structure not found"))
		return
	}
	s, err := a.store.GetStructure(r.Context(), ledgerID, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeTagged(w, http.StatusOK, s.LockVersion, toStructureView(ledgerID, s))
}

// putStructure creates (201) or replaces (200) a structure version; status
// defaults to draft, and setting it to active activates the version. The
// definition is checked against the ledger's dimension attributes in the store
// (422 LEDGER_STRUCTURE_INVALID with every violation); changing an active or
// retired version is 409 LEDGER_STRUCTURE_LOCKED.
func (a *API) putStructure(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	ledgerID, ok := ledgerParam(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		validationFailed(w, r, map[string]string{"id": "must be a UUID"})
		return
	}
	tag, tagOK := replaceTag(w, r)
	if !tagOK {
		return
	}
	var body structureBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	from, ok := parseDate(body.EffectiveFrom)
	if !ok {
		validationFailed(w, r, map[string]string{"effectiveFrom": "must be a date (YYYY-MM-DD)"})
		return
	}
	if body.Status == "" {
		body.Status = domain.StructureDraft
	}
	in := domain.Structure{Name: body.Name, Version: body.Version, Priority: body.Priority, EffectiveFrom: from,
		Status: body.Status, Segments: body.Segments, AccountFilter: body.AccountFilter, Constraints: toDomainConstraints(body.Constraints)}
	saved, created, err := a.store.PutStructure(r.Context(), ledgerID, id, tag, in)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeTagged(w, status, saved.LockVersion, toStructureView(ledgerID, saved))
}

func (a *API) listDimensionValues(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	ledgerID, ok := ledgerParam(w, r)
	if !ok {
		return
	}
	list, err := a.store.ListDimensionValues(r.Context(), ledgerID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	items := make([]dimensionValueView, 0, len(list))
	for _, v := range list {
		items = append(items, toDimensionValueView(v))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// putDimensionValue creates (201) or replaces (200) a registered value (FR-11.2).
func (a *API) putDimensionValue(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	ledgerID, ok := ledgerParam(w, r)
	if !ok {
		return
	}
	tag, tagOK := replaceTag(w, r)
	if !tagOK {
		return
	}
	var body dimensionValueBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	from, okFrom := parseOptionalDate(body.ActiveFrom)
	if !okFrom {
		fields["activeFrom"] = "must be a date (YYYY-MM-DD) or null"
	}
	to, okTo := parseOptionalDate(body.ActiveTo)
	if !okTo {
		fields["activeTo"] = "must be a date (YYYY-MM-DD) or null"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	in := domain.DimensionValue{Attribute: chi.URLParam(r, "attribute"), Value: chi.URLParam(r, "value"), Name: body.Name,
		ActiveFrom: from, ActiveTo: to, Suspended: body.Suspended}
	saved, created, err := a.store.PutDimensionValue(r.Context(), ledgerID, tag, in)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeTagged(w, status, saved.LockVersion, toDimensionValueView(saved))
}

func parseOptionalDate(s *string) (*time.Time, bool) {
	if s == nil || *s == "" {
		return nil, true
	}
	d, ok := parseDate(*s)
	if !ok {
		return nil, false
	}
	return &d, true
}

func formatOptionalDate(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format(time.DateOnly)
	return &s
}

func toDomainConstraints(in []constraintBody) []domain.Constraint {
	out := make([]domain.Constraint, 0, len(in))
	for _, c := range in {
		out = append(out, domain.Constraint{Segment: c.Segment, Under: c.Under, Allowed: c.Allowed})
	}
	return out
}

func toStructureView(ledgerID string, s domain.Structure) structureView {
	cs := make([]constraintBody, 0, len(s.Constraints))
	for _, c := range s.Constraints {
		cs = append(cs, constraintBody{Segment: c.Segment, Under: c.Under, Allowed: c.Allowed})
	}
	return structureView{ID: s.ID, LedgerID: ledgerID, Name: s.Name, Version: s.Version, Priority: s.Priority,
		EffectiveFrom: s.EffectiveFrom.Format(time.DateOnly), Status: s.Status, Segments: s.Segments,
		AccountFilter: s.AccountFilter, Constraints: cs, LockVersion: s.LockVersion}
}

func toDimensionValueView(v domain.DimensionValue) dimensionValueView {
	return dimensionValueView{Attribute: v.Attribute, Value: v.Value, Name: v.Name,
		ActiveFrom: formatOptionalDate(v.ActiveFrom), ActiveTo: formatOptionalDate(v.ActiveTo), Suspended: v.Suspended, LockVersion: v.LockVersion}
}
