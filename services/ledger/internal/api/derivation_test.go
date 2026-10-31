package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
)

const lookupTestID = "00000000-0000-4000-8000-0000000000b1"

type derivationFake struct {
	tables       []domain.LookupTable
	putTables    []domain.LookupTable
	putTableVers []int
	tableNew     bool
	putStrats    []domain.DerivationStrategy
	putStratVers []int
	activated    []int
	stratNew     bool
}

func (f *fakeStore) ListLookupTables(context.Context, string) ([]domain.LookupTable, error) {
	return f.deriv.tables, f.err
}

func (f *fakeStore) GetLookupTable(_ context.Context, _, id string) (domain.LookupTable, error) {
	return domain.LookupTable{ID: id, Version: 4}, f.err
}

func (f *fakeStore) PutLookupTable(_ context.Context, _, id string, version int, in domain.LookupTable) (domain.LookupTable, bool, error) {
	f.deriv.putTables = append(f.deriv.putTables, in)
	f.deriv.putTableVers = append(f.deriv.putTableVers, version)
	in.ID, in.Version = id, 2
	return in, f.deriv.tableNew, f.err
}

func (f *fakeStore) ListDerivationStrategies(context.Context, string) ([]domain.DerivationStrategy, error) {
	return []domain.DerivationStrategy{{Version: 1}}, f.err
}

func (f *fakeStore) GetDerivationStrategy(_ context.Context, _ string, version int) (domain.DerivationStrategy, error) {
	return domain.DerivationStrategy{Version: version, LockVersion: 5}, f.err
}

func (f *fakeStore) PutDerivationStrategy(_ context.Context, _ string, version, lock int, in domain.DerivationStrategy) (domain.DerivationStrategy, bool, error) {
	in.Version = version
	f.deriv.putStrats = append(f.deriv.putStrats, in)
	f.deriv.putStratVers = append(f.deriv.putStratVers, lock)
	in.LockVersion = 2
	return in, f.deriv.stratNew, f.err
}

func (f *fakeStore) ActivateDerivationStrategy(_ context.Context, _ string, version, _ int) (domain.DerivationStrategy, error) {
	f.deriv.activated = append(f.deriv.activated, version)
	return domain.DerivationStrategy{Version: version, Status: domain.StrategyActive, LockVersion: 9}, f.err
}

func TestPutLookupTable_MapsAndEnforcesDuty(t *testing.T) {
	fs := &fakeStore{}
	fs.deriv.tableNew = true
	h := server(fs)
	path := "/v1/ledger/ledgers/" + testLedgerID + "/lookup-tables/" + lookupTestID
	body := `{"name":"Function by account and centre","sourceFields":["ACCOUNT","COST_CENTER"],"target":"FUNC_AREA",
		"rows":[{"source":["5010","C200"],"target":"PROD","validFrom":"2026-01-01"}]}`
	rec := call(h, http.MethodPut, path, body, dutyRulesWrite, nil)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("create: %d %q %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	got := fs.deriv.putTables[0]
	if got.Target != "FUNC_AREA" || len(got.SourceFields) != 2 || len(got.Rows) != 1 || got.Rows[0].Target != "PROD" {
		t.Fatalf("not mapped: %+v", got)
	}
	fs.deriv.tableNew = false
	if rec := call(h, http.MethodPut, path, body, dutyRulesWrite, map[string]string{"If-Match": `"2"`}); rec.Code != http.StatusOK || fs.deriv.putTableVers[1] != 2 {
		t.Fatalf("replace: %d %v", rec.Code, fs.deriv.putTableVers)
	}
	if rec := call(h, http.MethodPut, path, body, dutyRulesWrite, map[string]string{"If-Match": "abc"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad If-Match: %d", rec.Code)
	}
	if rec := call(h, http.MethodPut, "/v1/ledger/ledgers/"+testLedgerID+"/lookup-tables/nope", body, dutyRulesWrite, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", rec.Code)
	}
	if rec := call(h, http.MethodPut, path, body, dutyRead, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("needs the rules duty: %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, path, "", dutyRead, nil); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("get: %d %q", rec.Code, rec.Header().Get("ETag"))
	}
	if rec := call(h, http.MethodGet, "/v1/ledger/ledgers/"+testLedgerID+"/lookup-tables/nope", "", dutyRead, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("get bad id: %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, "/v1/ledger/ledgers/"+testLedgerID+"/lookup-tables", "", dutyRead, nil); rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	if len(fs.deriv.putTables) != 2 {
		t.Fatalf("invalid requests reached the store: %d", len(fs.deriv.putTables))
	}
}

func TestPutStrategy_MapsStepsAndActivates(t *testing.T) {
	fs := &fakeStore{}
	fs.deriv.stratNew = true
	h := server(fs)
	base := "/v1/ledger/ledgers/" + testLedgerID + "/derivation-strategies/"
	body := `{"name":"Cost of sales","steps":[
		{"sequence":10,"kind":"table_lookup","sourceFields":["ACCOUNT","COST_CENTER"],"target":"FUNC_AREA","lookupTableId":"` + lookupTestID + `"},
		{"sequence":20,"kind":"constant","target":"SEGMENT","value":"MFG","condition":{"field":"ACCOUNT_TYPE","op":"eq","values":["expense"]}}]}`
	rec := call(h, http.MethodPut, base+"1", body, dutyRulesWrite, nil)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("create: %d %q %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	d := fs.deriv.putStrats[0]
	if d.Version != 1 || len(d.Steps) != 2 || d.Steps[1].Condition == nil || d.Steps[1].SourceFields == nil {
		t.Fatalf("not mapped: %+v", d)
	}
	for _, bad := range []struct{ path, body string }{
		{base + "0", body},
		{base + "x", body},
		{base + "1", `{"name":"","steps":[]}`},
		{base + "1", `{"name":"X","steps":[{"id":"nope","sequence":1,"kind":"clear","target":"A","overwrite":true}]}`},
		{base + "1", `{"name":"X","unknown":1}`},
	} {
		if rec := call(h, http.MethodPut, bad.path, bad.body, dutyRulesWrite, nil); rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: %d", bad.path, strings.TrimSpace(bad.body), rec.Code)
		}
	}
	if rec := call(h, http.MethodPut, base+"1", body, dutyRead, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("needs the rules duty: %d", rec.Code)
	}
	if len(fs.deriv.putStrats) != 1 {
		t.Fatalf("invalid requests reached the store: %d", len(fs.deriv.putStrats))
	}
	if rec := call(h, http.MethodPost, base+"1/activate", "", dutyRulesWrite, nil); rec.Code != http.StatusOK || fs.deriv.activated[0] != 1 || rec.Header().Get("ETag") != `"9"` {
		t.Fatalf("activate: %d %v", rec.Code, fs.deriv.activated)
	}
	if rec := call(h, http.MethodPost, base+"1/activate", "", dutyRead, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("activate needs the rules duty: %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, base+"3", "", dutyRead, nil); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"5"` {
		t.Fatalf("get: %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, base[:len(base)-1], "", dutyRead, nil); rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
}
