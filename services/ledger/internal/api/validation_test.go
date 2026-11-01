package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
)

const ruleTestID = "00000000-0000-4000-8000-0000000000c1"

type validationFake struct {
	put      []domain.ValidationRule
	putLocks []int
	isNew    bool
}

func (f *fakeStore) ListValidationRules(context.Context, string) ([]domain.ValidationRule, error) {
	return []domain.ValidationRule{{ID: ruleTestID, Name: "r"}}, f.err
}

func (f *fakeStore) GetValidationRule(_ context.Context, _, id string) (domain.ValidationRule, error) {
	return domain.ValidationRule{ID: id, LockVersion: 4}, f.err
}

func (f *fakeStore) PutValidationRule(_ context.Context, _, id string, lock int, in domain.ValidationRule) (domain.ValidationRule, bool, error) {
	f.vrules.put = append(f.vrules.put, in)
	f.vrules.putLocks = append(f.vrules.putLocks, lock)
	in.ID, in.LockVersion = id, 2
	return in, f.vrules.isNew, f.err
}

func TestPutValidationRule_MapsAndEnforcesDuty(t *testing.T) {
	fs := &fakeStore{}
	fs.vrules.isNew = true
	h := server(fs)
	path := "/v1/ledger/ledgers/" + testLedgerID + "/validation-rules/" + ruleTestID
	body := `{"sequence":10,"name":"DEPT on expenses","level":"line","severity":"error","active":true,"message":"DEPT required",
		"prerequisite":[{"field":"AMOUNT","op":"gt","values":["1000"]}],"checks":[{"field":"DEPT","op":"notblank"}]}`
	rec := call(h, http.MethodPut, path, body, dutyRulesWrite, nil)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("create: %d %q %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	got := fs.vrules.put[0]
	if got.Name != "DEPT on expenses" || got.Level != "line" || !got.Active || got.Sequence != 10 || len(got.Prerequisite) != 1 ||
		got.Checks[0].Field != "DEPT" {
		t.Fatalf("not mapped: %+v", got)
	}
	fs.vrules.isNew = false
	if rec := call(h, http.MethodPut, path, body, dutyRulesWrite, map[string]string{"If-Match": `"2"`}); rec.Code != http.StatusOK || fs.vrules.putLocks[1] != 2 {
		t.Fatalf("replace: %d %v", rec.Code, fs.vrules.putLocks)
	}
	if rec := call(h, http.MethodPut, path, body, dutyRulesWrite, map[string]string{"If-Match": "abc"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad If-Match: %d", rec.Code)
	}
	if rec := call(h, http.MethodPut, "/v1/ledger/ledgers/"+testLedgerID+"/validation-rules/nope", body, dutyRulesWrite, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", rec.Code)
	}
	if rec := call(h, http.MethodPut, path, body, dutyRead, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("a reader must not write: %d", rec.Code)
	}
}

func TestValidationRuleReads(t *testing.T) {
	h := server(&fakeStore{})
	base := "/v1/ledger/ledgers/" + testLedgerID + "/validation-rules"
	rec := call(h, http.MethodGet, base, "", dutyRead, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), ruleTestID) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	rec = call(h, http.MethodGet, base+"/"+ruleTestID, "", dutyRead, nil)
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
