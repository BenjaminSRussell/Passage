package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
)

// Designer parts (FR-15.2, FR-15.3, PRD §2.15.5 /row-sets and /column-sets):
// reusable row sets and column sets, the comparative column templates the
// service ships, and composing a definition draft from them.

func (a *API) listRowSets(w http.ResponseWriter, r *http.Request) {
	if !principal(r) {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	items, err := a.store.ListRowSets(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getRowSet(w http.ResponseWriter, r *http.Request) {
	if !principal(r) {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	rs, rev, err := a.store.GetRowSet(r.Context(), chi.URLParam(r, "code"))
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(rev))
	httpx.JSON(w, http.StatusOK, rs)
}

// putRowSet creates or replaces a row set. The rows are checked with the rules
// of a definition (REPORT-0038 for the part, REPORT-0002 for expressions and
// references); replacing needs If-Match. Repeating a PUT with the current
// revision gives the same row set (PUT idempotent).
func (a *API) putRowSet(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.FromContext(r.Context())
	if !ok {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	var rs domain.RowSet
	if !readPart(w, r, &rs) {
		return
	}
	code := chi.URLParam(r, "code")
	if rs.Code == "" {
		rs.Code = code
	}
	if rs.Code != code {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodePartInvalid, "code: body code does not match the path"))
		return
	}
	if issues := rs.Validate(); len(issues) > 0 {
		httpx.Fail(w, r, issueProblem(issues))
		return
	}
	ifMatch, err := parseIfMatch(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	rs.UpdatedBy = p.UserID
	saved, rev, err := a.store.SaveRowSet(r.Context(), rs, ifMatch)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(rev))
	httpx.JSON(w, http.StatusOK, saved)
}

func (a *API) listColumnSets(w http.ResponseWriter, r *http.Request) {
	if !principal(r) {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	items, err := a.store.ListColumnSets(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getColumnSet(w http.ResponseWriter, r *http.Request) {
	if !principal(r) {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	cs, rev, err := a.store.GetColumnSet(r.Context(), chi.URLParam(r, "code"))
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(rev))
	httpx.JSON(w, http.StatusOK, cs)
}

// putColumnSet creates or replaces a column set, with the column rules of a
// definition; replacing needs If-Match.
func (a *API) putColumnSet(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.FromContext(r.Context())
	if !ok {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	var cs domain.ColumnSet
	if !readPart(w, r, &cs) {
		return
	}
	code := chi.URLParam(r, "code")
	if cs.Code == "" {
		cs.Code = code
	}
	if cs.Code != code {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodePartInvalid, "code: body code does not match the path"))
		return
	}
	if issues := cs.Validate(); len(issues) > 0 {
		httpx.Fail(w, r, issueProblem(issues))
		return
	}
	ifMatch, err := parseIfMatch(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	cs.UpdatedBy = p.UserID
	saved, rev, err := a.store.SaveColumnSet(r.Context(), cs, ifMatch)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(rev))
	httpx.JSON(w, http.StatusOK, saved)
}

// readPart decodes a part body strictly; updatedBy is read-only and replaced by
// the caller, so a body copied from GET is accepted.
func readPart(w http.ResponseWriter, r *http.Request, v any) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0002", "unreadable body"))
		return false
	}
	if err := decodeStrict(raw, v); err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0002", err.Error()))
		return false
	}
	return true
}

// listColumnTemplates returns the comparative column sets the service ships
// (FR-15.3). They are read-only; saving one under a tenant code makes it editable.
func (a *API) listColumnTemplates(w http.ResponseWriter, r *http.Request) {
	if !principal(r) {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": domain.ColumnTemplates()})
}

type composeRequest struct {
	Code                     string      `json:"code"`
	Name                     string      `json:"name"`
	Kind                     domain.Kind `json:"kind"`
	IncludeAdjustmentPeriods bool        `json:"includeAdjustmentPeriods"`
	RowSet                   string      `json:"rowSet"`
	ColumnSet                string      `json:"columnSet"`
}

// composeDefinition builds the draft body of a definition from a stored row set
// and a column set (a stored one, else a shipped template of that code). It
// stores nothing: the designer loads the body and saves it as a draft, which
// validates it again. Later edits of the sets do not change saved drafts.
func (a *API) composeDefinition(w http.ResponseWriter, r *http.Request) {
	if !principal(r) {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0002", "unreadable body"))
		return
	}
	var in composeRequest
	if err := decodeStrict(raw, &in); err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0002", err.Error()))
		return
	}
	if in.Kind == "" {
		in.Kind = domain.KindCustom
	}
	rows, _, err := a.store.GetRowSet(r.Context(), in.RowSet)
	if err != nil {
		fail(w, r, err)
		return
	}
	cols, _, err := a.store.GetColumnSet(r.Context(), in.ColumnSet)
	if err != nil {
		var p *httpx.Problem
		if !errors.As(err, &p) || p.Code != domain.CodePartNotFound {
			fail(w, r, err)
			return
		}
		found := false
		for _, tpl := range domain.ColumnTemplates() {
			if tpl.Code == in.ColumnSet {
				cols, found = tpl, true
			}
		}
		if !found {
			fail(w, r, err)
			return
		}
	}
	def, issues := domain.Compose(in.Code, in.Name, in.Kind, in.IncludeAdjustmentPeriods, rows, cols)
	if len(issues) > 0 {
		httpx.Fail(w, r, issueProblem(issues))
		return
	}
	httpx.JSON(w, http.StatusOK, def)
}

// Reporting trees (FR-15.4, PRD §2.15.5 /trees): reporting units over the
// values of one dimension attribute, with roll-up; a statement run takes a unit.

func (a *API) listTrees(w http.ResponseWriter, r *http.Request) {
	if !principal(r) {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	items, err := a.store.ListTrees(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getTree(w http.ResponseWriter, r *http.Request) {
	if !principal(r) {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	t, rev, err := a.store.GetTree(r.Context(), chi.URLParam(r, "code"))
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(rev))
	httpx.JSON(w, http.StatusOK, t)
}

// putTree creates or replaces a reporting tree. The units are checked with the
// tree rules (REPORT-0039); replacing needs If-Match. Repeating a PUT with the
// current revision gives the same tree (PUT idempotent).
func (a *API) putTree(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.FromContext(r.Context())
	if !ok {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	var t domain.Tree
	if !readPart(w, r, &t) {
		return
	}
	code := chi.URLParam(r, "code")
	if t.Code == "" {
		t.Code = code
	}
	if t.Code != code {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeTreeInvalid, "code: body code does not match the path"))
		return
	}
	if issues := t.Validate(); len(issues) > 0 {
		httpx.Fail(w, r, issueProblem(issues))
		return
	}
	ifMatch, err := parseIfMatch(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	t.UpdatedBy = p.UserID
	saved, rev, err := a.store.SaveTree(r.Context(), t, ifMatch)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(rev))
	httpx.JSON(w, http.StatusOK, saved)
}

// listTreeRevisions lists the stored revisions of a tree, newest first, so a
// maintainer can see what a run made on an older revision used.
func (a *API) listTreeRevisions(w http.ResponseWriter, r *http.Request) {
	if !principal(r) {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	items, err := a.store.ListTreeRevisions(r.Context(), chi.URLParam(r, "code"))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// getTreeRevision returns the tree as it was at one revision.
func (a *API) getTreeRevision(w http.ResponseWriter, r *http.Request) {
	if !principal(r) {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	code := chi.URLParam(r, "code")
	rev, err := strconv.Atoi(chi.URLParam(r, "revision"))
	if err != nil || rev < 1 {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodePartNotFound, fmt.Sprintf("tree %s has no stored revision %s", code, chi.URLParam(r, "revision"))))
		return
	}
	t, err := a.store.GetTreeRevision(r.Context(), code, rev)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(rev))
	httpx.JSON(w, http.StatusOK, t)
}

// runTree returns the reporting tree a run was limited to, as it was when the
// run was made: the units behind the run's unit, even after the tree changed.
func (a *API) runTree(w http.ResponseWriter, r *http.Request) {
	id, ok := runID(w, r)
	if !ok {
		return
	}
	v, err := a.store.RunTree(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}
