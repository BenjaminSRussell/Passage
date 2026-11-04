package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
)

// partStore keeps row sets and column sets in memory and answers like the
// Postgres store: REPORT-0037 for an unknown code.
type partStore struct {
	Store
	rows    map[string]domain.RowSet
	cols    map[string]domain.ColumnSet
	ifMatch *int
}

func newPartStore() *partStore {
	return &partStore{rows: map[string]domain.RowSet{}, cols: map[string]domain.ColumnSet{}}
}

func missing(noun, code string) error {
	return httpx.NewProblem(http.StatusNotFound, domain.CodePartNotFound, noun+" "+code+" does not exist")
}

func (s *partStore) ListRowSets(context.Context) ([]domain.RowSet, error) {
	out := []domain.RowSet{}
	for _, v := range s.rows {
		out = append(out, v)
	}
	return out, nil
}

func (s *partStore) GetRowSet(_ context.Context, code string) (domain.RowSet, int, error) {
	if v, ok := s.rows[code]; ok {
		return v, 2, nil
	}
	return domain.RowSet{}, 0, missing("row set", code)
}

func (s *partStore) SaveRowSet(_ context.Context, rs domain.RowSet, ifMatch *int) (domain.RowSet, int, error) {
	s.rows[rs.Code], s.ifMatch = rs, ifMatch
	return rs, 3, nil
}

func (s *partStore) ListColumnSets(context.Context) ([]domain.ColumnSet, error) {
	out := []domain.ColumnSet{}
	for _, v := range s.cols {
		out = append(out, v)
	}
	return out, nil
}

func (s *partStore) GetColumnSet(_ context.Context, code string) (domain.ColumnSet, int, error) {
	if v, ok := s.cols[code]; ok {
		return v, 1, nil
	}
	return domain.ColumnSet{}, 0, missing("column set", code)
}

func (s *partStore) SaveColumnSet(_ context.Context, cs domain.ColumnSet, ifMatch *int) (domain.ColumnSet, int, error) {
	s.cols[cs.Code], s.ifMatch = cs, ifMatch
	return cs, 1, nil
}

const rowSetBody = `{"code":"REV","name":"Revenue","rows":[
	{"code":"R","kind":"account_range","label":"Revenue","accountFrom":"400000","accountTo":"499999","normalBalance":"credit","format":{}}]}`

const columnSetBody = `{"code":"MY-CMP","name":"Mine","columns":[
	{"code":"C1","label":"Period","periodExpr":"P"},{"code":"C2","label":"Prior","periodExpr":"PY"},
	{"code":"C3","label":"Var","periodExpr":"CALC","calcExpression":"C1-C2"}]}`

func TestRowSets_PutGetListAndDuty(t *testing.T) {
	st := newPartStore()
	r := scheduleRouter(st)
	w := call(r, http.MethodPut, "/v1/reporting/row-sets/REV", rowSetBody, map[string]string{"If-Match": `"2"`}, DutyMaintain)
	if w.Code != http.StatusOK || w.Header().Get("ETag") != `"3"` {
		t.Fatalf("put = %d %s etag %q", w.Code, w.Body.String(), w.Header().Get("ETag"))
	}
	if st.ifMatch == nil || *st.ifMatch != 2 || st.rows["REV"].UpdatedBy != "u1" {
		t.Fatalf("saved %+v If-Match %v, want the caller as editor and If-Match 2", st.rows["REV"], st.ifMatch)
	}
	if w := call(r, http.MethodGet, "/v1/reporting/row-sets/REV", "", nil, DutyMaintain); w.Code != http.StatusOK || w.Header().Get("ETag") != `"2"` {
		t.Fatalf("get = %d etag %q", w.Code, w.Header().Get("ETag"))
	}
	var list struct{ Items []domain.RowSet }
	w = call(r, http.MethodGet, "/v1/reporting/row-sets", "", nil, DutyMaintain)
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Items) != 1 {
		t.Fatalf("list = %d %s", w.Code, w.Body.String())
	}
	if w := call(r, http.MethodGet, "/v1/reporting/row-sets/NOPE", "", nil, DutyMaintain); w.Code != http.StatusNotFound || problemCode(t, w) != domain.CodePartNotFound {
		t.Fatalf("missing row set = %d %s", w.Code, w.Body.String())
	}
	for _, duty := range []string{DutyView, DutyRun} {
		if w := call(r, http.MethodPut, "/v1/reporting/row-sets/REV", rowSetBody, nil, duty); w.Code != http.StatusForbidden {
			t.Fatalf("put with %s = %d, want 403", duty, w.Code)
		}
	}
}

func TestRowSets_RejectBadBodies(t *testing.T) {
	r := scheduleRouter(newPartStore())
	cases := map[string]struct {
		path, body, code string
		status           int
	}{
		"path and body code differ": {"/v1/reporting/row-sets/OTHER", rowSetBody, domain.CodePartInvalid, 422},
		"unknown reference": {"/v1/reporting/row-sets/REV", `{"code":"REV","name":"Revenue","rows":[{"code":"T","kind":"total","label":"T","references":["X"],"normalBalance":"debit","format":{}}]}`,
			domain.CodeExpression, 422},
		"no rows":       {"/v1/reporting/row-sets/REV", `{"code":"REV","name":"Revenue","rows":[]}`, domain.CodeTooLarge, 422},
		"unknown field": {"/v1/reporting/row-sets/REV", `{"code":"REV","name":"Revenue","rows":[],"colour":"red"}`, "COM-0002", 400},
		"bad code":      {"/v1/reporting/row-sets/rev", `{"name":"Revenue","rows":[]}`, domain.CodePartInvalid, 422},
	}
	for name, c := range cases {
		w := call(r, http.MethodPut, c.path, c.body, nil, DutyMaintain)
		if w.Code != c.status || problemCode(t, w) != c.code {
			t.Errorf("%s: %d %s, want %d %s", name, w.Code, w.Body.String(), c.status, c.code)
		}
	}
}

func TestColumnSets_PutRejectsUnknownColumnReference(t *testing.T) {
	st := newPartStore()
	r := scheduleRouter(st)
	if w := call(r, http.MethodPut, "/v1/reporting/column-sets/MY-CMP", columnSetBody, nil, DutyMaintain); w.Code != http.StatusOK {
		t.Fatalf("put = %d %s", w.Code, w.Body.String())
	}
	bad := strings.Replace(columnSetBody, `"C1-C2"`, `"C1-C9"`, 1)
	w := call(r, http.MethodPut, "/v1/reporting/column-sets/MY-CMP", bad, nil, DutyMaintain)
	if w.Code != http.StatusUnprocessableEntity || problemCode(t, w) != domain.CodeExpression {
		t.Fatalf("bad reference = %d %s", w.Code, w.Body.String())
	}
	if w := call(r, http.MethodGet, "/v1/reporting/column-sets/NOPE", "", nil, DutyMaintain); w.Code != http.StatusNotFound {
		t.Fatalf("missing column set = %d", w.Code)
	}
	if w := call(r, http.MethodGet, "/v1/reporting/column-sets", "", nil, DutyMaintain); w.Code != http.StatusOK {
		t.Fatalf("list = %d", w.Code)
	}
}

func TestColumnTemplates_ListedForDesigners(t *testing.T) {
	r := scheduleRouter(newPartStore())
	w := call(r, http.MethodGet, "/v1/reporting/column-set-templates", "", nil, DutyMaintain)
	var out struct{ Items []domain.ColumnSet }
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != http.StatusOK || len(out.Items) != 4 || out.Items[1].Code != "CMP-PRIOR-PERIOD" {
		t.Fatalf("templates = %d %s", w.Code, w.Body.String())
	}
	if w := call(r, http.MethodGet, "/v1/reporting/column-set-templates", "", nil, DutyView); w.Code != http.StatusForbidden {
		t.Fatalf("view duty = %d, want 403", w.Code)
	}
}

func TestCompose_UsesStoredSetsThenTemplates(t *testing.T) {
	st := newPartStore()
	st.rows["REV"] = domain.RowSet{Code: "REV", Name: "Revenue", Rows: []domain.Row{
		{Code: "R", Kind: domain.RowAccountRange, Label: "Revenue", AccountFrom: "4", AccountTo: "5", NormalBalance: "credit"}}}
	st.cols["MY-CMP"] = domain.ColumnSet{Code: "MY-CMP", Name: "Mine", Columns: []domain.Column{{Code: "C1", Label: "Period", PeriodExpr: "P"}}}
	r := scheduleRouter(st)
	body := func(cols string) string {
		return `{"code":"REV-CMP","name":"Revenue comparison","kind":"income_statement","rowSet":"REV","columnSet":"` + cols + `"}`
	}
	var def domain.Definition
	w := call(r, http.MethodPost, "/v1/reporting/definitions:compose", body("MY-CMP"), nil, DutyMaintain)
	if err := json.Unmarshal(w.Body.Bytes(), &def); err != nil || w.Code != http.StatusOK || len(def.Columns) != 1 || def.Status != "draft" || def.Kind != domain.KindIncomeStatement {
		t.Fatalf("compose from stored sets = %d %s", w.Code, w.Body.String())
	}
	w = call(r, http.MethodPost, "/v1/reporting/definitions:compose", body("CMP-PY"), nil, DutyMaintain)
	if err := json.Unmarshal(w.Body.Bytes(), &def); err != nil || w.Code != http.StatusOK || len(def.Columns) != 4 {
		t.Fatalf("compose from a template = %d %s", w.Code, w.Body.String())
	}
	w = call(r, http.MethodPost, "/v1/reporting/definitions:compose", body("NO-SUCH"), nil, DutyMaintain)
	if w.Code != http.StatusNotFound || problemCode(t, w) != domain.CodePartNotFound {
		t.Fatalf("unknown column set = %d %s", w.Code, w.Body.String())
	}
	w = call(r, http.MethodPost, "/v1/reporting/definitions:compose", strings.Replace(body("MY-CMP"), `"REV-CMP"`, `"bad code"`, 1), nil, DutyMaintain)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid definition code = %d %s", w.Code, w.Body.String())
	}
	if w := call(r, http.MethodPost, "/v1/reporting/definitions:compose", body("MY-CMP"), nil, DutyRun); w.Code != http.StatusForbidden {
		t.Fatalf("run duty = %d, want 403", w.Code)
	}
}
