package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
)

const advancedTestID = "00000000-0000-4000-8000-0000000000e1"

type advancedFake struct {
	put      []domain.AdvancedRule
	putLocks []int
	isNew    bool
	filter   string
}

func (f *fakeStore) ListAdvancedRules(_ context.Context, _, structureID string) ([]domain.AdvancedRule, error) {
	f.arules.filter = structureID
	return []domain.AdvancedRule{{ID: advancedTestID, Name: "r"}}, f.err
}

func (f *fakeStore) GetAdvancedRule(_ context.Context, _, id string) (domain.AdvancedRule, error) {
	return domain.AdvancedRule{ID: id, LockVersion: 4}, f.err
}

func (f *fakeStore) PutAdvancedRule(_ context.Context, _, id string, lock int, in domain.AdvancedRule) (domain.AdvancedRule, bool, error) {
	f.arules.put = append(f.arules.put, in)
	f.arules.putLocks = append(f.arules.putLocks, lock)
	in.ID, in.LockVersion = id, 2
	return in, f.arules.isNew, f.err
}

func TestPutAdvancedRule_MapsAndEnforcesDuty(t *testing.T) {
	fs := &fakeStore{}
	fs.arules.isNew = true
	h := server(fs)
	path := "/v1/ledger/ledgers/" + testLedgerID + "/advanced-rules/" + advancedTestID
	body := `{"structureId":"` + testStructure + `","name":"PROJECT_ON_D100","priority":5,"accounts":["5000..5999"],
		"dimensions":{"DEPT":"D100"},"segments":["PROJECT"],"constraints":[{"segment":"PROJECT","allowed":["*"]}]}`
	rec := call(h, http.MethodPut, path, body, dutyStructureWrite, nil)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("create: %d %q %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	got := fs.arules.put[0]
	if got.StructureID != testStructure || got.Priority != 5 || got.Dimensions["DEPT"] != "D100" || got.Segments[0] != "PROJECT" ||
		got.Constraints[0].Allowed[0] != "*" || got.Accounts[0] != "5000..5999" {
		t.Fatalf("not mapped: %+v", got)
	}
	fs.arules.isNew = false
	if rec := call(h, http.MethodPut, path, body, dutyStructureWrite, map[string]string{"If-Match": `"2"`}); rec.Code != http.StatusOK || fs.arules.putLocks[1] != 2 {
		t.Fatalf("replace: %d %v", rec.Code, fs.arules.putLocks)
	}
	if rec := call(h, http.MethodPut, path, body, dutyStructureWrite, map[string]string{"If-Match": "abc"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad If-Match: %d", rec.Code)
	}
	if rec := call(h, http.MethodPut, path, strings.Replace(body, testStructure, "nope", 1), dutyStructureWrite, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad structure id: %d", rec.Code)
	}
	if rec := call(h, http.MethodPut, "/v1/ledger/ledgers/"+testLedgerID+"/advanced-rules/nope", body, dutyStructureWrite, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", rec.Code)
	}
	if rec := call(h, http.MethodPut, path, body, dutyRead, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("a reader must not write: %d", rec.Code)
	}
}

func TestAdvancedRuleReads(t *testing.T) {
	fs := &fakeStore{}
	h := server(fs)
	base := "/v1/ledger/ledgers/" + testLedgerID + "/advanced-rules"
	rec := call(h, http.MethodGet, base+"?structureId="+testStructure, "", dutyRead, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), advancedTestID) || fs.arules.filter != testStructure {
		t.Fatalf("list: %d %s %q", rec.Code, rec.Body.String(), fs.arules.filter)
	}
	if rec := call(h, http.MethodGet, base+"?structureId=zz", "", dutyRead, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad filter: %d", rec.Code)
	}
	rec = call(h, http.MethodGet, base+"/"+advancedTestID, "", dutyRead, nil)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("get: %d %q", rec.Code, rec.Header().Get("ETag"))
	}
	if rec := call(h, http.MethodGet, base+"/nope", "", dutyRead, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, base, "", "", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("no duty: %d", rec.Code)
	}
}
