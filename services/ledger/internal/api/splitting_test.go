package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
)

const splittingTestID = "00000000-0000-4000-8000-0000000000c1"

type splittingFake struct {
	put     []domain.SplittingRule
	putVers []int
	isNew   bool
}

func (f *fakeStore) ListSplittingRules(context.Context, string) ([]domain.SplittingRule, error) {
	return []domain.SplittingRule{{SourceType: "vendor_invoice"}}, f.err
}

func (f *fakeStore) GetSplittingRule(_ context.Context, _, id string) (domain.SplittingRule, error) {
	return domain.SplittingRule{ID: id, Version: 3}, f.err
}

func (f *fakeStore) PutSplittingRule(_ context.Context, _, id string, version int, in domain.SplittingRule) (domain.SplittingRule, bool, error) {
	f.split.put = append(f.split.put, in)
	f.split.putVers = append(f.split.putVers, version)
	in.ID, in.Version = id, 2
	return in, f.split.isNew, f.err
}

func TestPutSplittingRule_MapsAndEnforcesDuty(t *testing.T) {
	fs := &fakeStore{}
	fs.split.isNew = true
	h := server(fs)
	path := "/v1/ledger/ledgers/" + testLedgerID + "/splitting-rules/" + splittingTestID
	body := `{"sourceType":"vendor_invoice","splittingDimensions":["PROFIT_CENTER"],"zeroBalanceClearingAccount":"199999","active":true}`
	rec := call(h, http.MethodPut, path, body, dutyRulesWrite, nil)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("create: %d %q %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	got := fs.split.put[0]
	if got.SourceType != "vendor_invoice" || got.SplittingDimensions[0] != "PROFIT_CENTER" || got.ZeroBalanceClearingAccount != "199999" ||
		!got.Active || got.DefaultSplittingValue == nil {
		t.Fatalf("not mapped: %+v", got)
	}
	fs.split.isNew = false
	if rec := call(h, http.MethodPut, path, body, dutyRulesWrite, map[string]string{"If-Match": `"2"`}); rec.Code != http.StatusOK || fs.split.putVers[1] != 2 {
		t.Fatalf("replace: %d %v", rec.Code, fs.split.putVers)
	}
	if rec := call(h, http.MethodPut, path, body, dutyRulesWrite, map[string]string{"If-Match": "abc"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad If-Match: %d", rec.Code)
	}
	if rec := call(h, http.MethodPut, "/v1/ledger/ledgers/"+testLedgerID+"/splitting-rules/nope", body, dutyRulesWrite, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", rec.Code)
	}
	if rec := call(h, http.MethodPut, path, `{"sourceType":"x","unknown":1}`, dutyRulesWrite, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", rec.Code)
	}
	if rec := call(h, http.MethodPut, path, body, dutyRead, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("needs the rules duty: %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, path, "", dutyRead, nil); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"3"` {
		t.Fatalf("get: %d %q", rec.Code, rec.Header().Get("ETag"))
	}
	if rec := call(h, http.MethodGet, "/v1/ledger/ledgers/"+testLedgerID+"/splitting-rules/nope", "", dutyRead, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("get bad id: %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, "/v1/ledger/ledgers/"+testLedgerID+"/splitting-rules", "", dutyRead, nil); rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	if len(fs.split.put) != 2 {
		t.Fatalf("invalid requests reached the store: %d", len(fs.split.put))
	}
}
