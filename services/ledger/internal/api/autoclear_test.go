package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/store"
)

const autoRuleID = "00000000-0000-4000-8000-0000000000a1"

type autoFake struct {
	rules   []store.AutoClearingRule
	puts    []store.AutoClearingRule
	putVers []int
	ruleNew bool
	runs    []store.AutoClearingInput
	run     store.AutoClearingRun
	runNew  bool
}

func (f *fakeStore) ListAutoClearingRules(context.Context, string) ([]store.AutoClearingRule, error) {
	return f.auto.rules, f.err
}

func (f *fakeStore) GetAutoClearingRule(_ context.Context, _, id string) (store.AutoClearingRule, error) {
	return store.AutoClearingRule{ID: id, Version: 3}, f.err
}

func (f *fakeStore) PutAutoClearingRule(_ context.Context, _, id string, version int, in store.AutoClearingRule) (store.AutoClearingRule, bool, error) {
	f.auto.puts = append(f.auto.puts, in)
	f.auto.putVers = append(f.auto.putVers, version)
	in.ID, in.Version = id, 2
	return in, f.auto.ruleNew, f.err
}

func (f *fakeStore) RunAutoClearing(_ context.Context, in store.AutoClearingInput) (store.AutoClearingRun, bool, error) {
	f.auto.runs = append(f.auto.runs, in)
	return f.auto.run, f.auto.runNew, f.err
}

func (f *fakeStore) ListAutoClearingRuns(context.Context, string) ([]store.AutoClearingRun, error) {
	return []store.AutoClearingRun{f.auto.run}, f.err
}

func (f *fakeStore) GetAutoClearingRun(context.Context, string) (store.AutoClearingRun, error) {
	return f.auto.run, f.err
}

func TestPutAutoRule_MapsValidatesAndEnforcesDuty(t *testing.T) {
	fs := &fakeStore{}
	fs.auto.ruleNew = true
	h := server(fs)
	path := "/v1/ledger/ledgers/" + testLedgerID + "/auto-clearing-rules/" + autoRuleID
	body := `{"name":"GR/IR","accountFrom":"2100","accountTo":"2199","criteria":["assignment","dim.DEPT"],"allowTolerance":true,"toleranceGroup":"STD"}`
	rec := call(h, http.MethodPut, path, body, dutyClearingRules, nil)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("create: %d %q %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	r := fs.auto.puts[0]
	if r.Sequence != 100 || !r.Active || r.ToleranceGroup != "STD" || len(r.Criteria) != 2 || r.AccountFrom != "2100" {
		t.Fatalf("not mapped: %+v", r)
	}
	fs.auto.ruleNew = false
	if rec := call(h, http.MethodPut, path, body, dutyClearingRules, map[string]string{"If-Match": `"2"`}); rec.Code != http.StatusOK || fs.auto.putVers[1] != 2 {
		t.Fatalf("replace: %d %v", rec.Code, fs.auto.putVers)
	}
	for _, bad := range []string{
		`{"name":"","accountFrom":"1","accountTo":"2","criteria":["assignment"]}`,
		`{"name":"X","accountFrom":"2","accountTo":"1","criteria":["assignment"]}`,
		`{"name":"X","accountFrom":"1","accountTo":"2","criteria":[]}`,
		`{"name":"X","accountFrom":"1","accountTo":"2","criteria":["assignment","assignment"]}`,
		`{"name":"X","accountFrom":"1","accountTo":"2","criteria":["a","b","c","d","e","f"]}`,
		`{"name":"X","accountFrom":"1","accountTo":"2","criteria":["colour"]}`,
		`{"name":"X","accountFrom":"1","accountTo":"2","criteria":["assignment"],"allowTolerance":true}`,
		`{"name":"X","accountFrom":"1","accountTo":"2","criteria":["assignment"],"toleranceGroup":"STD"}`,
		`{"name":"X","accountFrom":"1","accountTo":"2","criteria":["assignment"],"sequence":10000}`,
	} {
		if rec := call(h, http.MethodPut, path, bad, dutyClearingRules, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
	if rec := call(h, http.MethodPut, "/v1/ledger/ledgers/"+testLedgerID+"/auto-clearing-rules/nope", body, dutyClearingRules, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}
	if rec := call(h, http.MethodPut, path, body, dutyRead, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("needs the rules duty: %d", rec.Code)
	}
	if len(fs.auto.puts) != 2 {
		t.Fatalf("invalid requests reached the store: %d", len(fs.auto.puts))
	}
}

func TestRunAutoClearing_SimulateAndClear(t *testing.T) {
	fs := &fakeStore{}
	fs.auto.run = store.AutoClearingRun{ID: autoRuleID, Mode: "clear", GroupsCleared: 2}
	h := server(fs)
	sim := `{"ledgerId":"` + testLedgerID + `","mode":"simulate","clearingDate":"2026-03-31"}`
	if rec := call(h, http.MethodPost, "/v1/ledger/auto-clearing:run", sim, dutyClearingRun, nil); rec.Code != http.StatusOK {
		t.Fatalf("simulate needs no key: %d %s", rec.Code, rec.Body.String())
	}
	if in := fs.auto.runs[0]; in.Mode != "simulate" || in.Key != "" || in.ClearingDate.Format("2006-01-02") != "2026-03-31" {
		t.Fatalf("not mapped: %+v", in)
	}
	clear := `{"ledgerId":"` + testLedgerID + `","ruleId":"` + autoRuleID + `","mode":"clear"}`
	if rec := call(h, http.MethodPost, "/v1/ledger/auto-clearing:run", clear, dutyClearingRun, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("clear needs a key: %d", rec.Code)
	}
	fs.auto.runNew = true
	rec := call(h, http.MethodPost, "/v1/ledger/auto-clearing:run", clear, dutyClearingRun, map[string]string{"Idempotency-Key": "run-1"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("clear: %d %s", rec.Code, rec.Body.String())
	}
	in := fs.auto.runs[1]
	if in.Key != "run-1" || in.RuleID != autoRuleID || len(in.Hash) != 32 || in.ClearingDate.IsZero() {
		t.Fatalf("not mapped: %+v", in)
	}
	fs.auto.runNew = false
	if rec := call(h, http.MethodPost, "/v1/ledger/auto-clearing:run", clear, dutyClearingRun, map[string]string{"Idempotency-Key": "run-1"}); rec.Code != http.StatusOK {
		t.Fatalf("replay: %d", rec.Code)
	}
	for _, bad := range []string{`{"ledgerId":"x","mode":"clear"}`, `{"ledgerId":"` + testLedgerID + `","mode":"dry"}`,
		`{"ledgerId":"` + testLedgerID + `","mode":"simulate","clearingDate":"31/03/2026"}`, `{"ledgerId":"` + testLedgerID + `","mode":"simulate","ruleId":"x"}`} {
		if rec := call(h, http.MethodPost, "/v1/ledger/auto-clearing:run", bad, dutyClearingRun, map[string]string{"Idempotency-Key": "k"}); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
	if rec := call(h, http.MethodPost, "/v1/ledger/auto-clearing:run", sim, dutyClearingWrite, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("manual clearing duty must not run automatic clearing: %d", rec.Code)
	}
}

func TestAutoClearingReads(t *testing.T) {
	fs := &fakeStore{}
	fs.auto.run = store.AutoClearingRun{ID: autoRuleID}
	h := server(fs)
	for _, p := range []string{"/v1/ledger/ledgers/" + testLedgerID + "/auto-clearing-rules", "/v1/ledger/ledgers/" + testLedgerID + "/auto-clearing-rules/" + autoRuleID,
		"/v1/ledger/auto-clearing-runs?ledger=" + testLedgerID, "/v1/ledger/auto-clearing-runs/" + autoRuleID} {
		if rec := call(h, http.MethodGet, p, "", dutyRead, nil); rec.Code != http.StatusOK {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
	if rec := call(h, http.MethodGet, "/v1/ledger/auto-clearing-runs", "", dutyRead, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("ledger is required: %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, "/v1/ledger/auto-clearing-runs/bad", "", dutyRead, nil); rec.Code != http.StatusNotFound {
		t.Errorf("bad id: %d", rec.Code)
	}
}

// A clear repeated without clearingDate on a later day hashes the same body, so
// the store replays the stored run instead of answering 409.
func TestRunAutoClearing_HashIsTheBodyAsSent(t *testing.T) {
	fs := &fakeStore{}
	r := httpx.NewRouter()
	a := New(fs)
	day := time.Date(2026, 3, 15, 13, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return day }
	a.Register(r)
	body := `{"ledgerId":"` + testLedgerID + `","mode":"clear"}`
	key := map[string]string{"Idempotency-Key": "nightly"}
	call(r, http.MethodPost, "/v1/ledger/auto-clearing:run", body, dutyClearingRun, key)
	day = day.AddDate(0, 0, 1)
	call(r, http.MethodPost, "/v1/ledger/auto-clearing:run", body, dutyClearingRun, key)
	withDate := `{"ledgerId":"` + testLedgerID + `","mode":"clear","clearingDate":"2026-03-16"}`
	call(r, http.MethodPost, "/v1/ledger/auto-clearing:run", withDate, dutyClearingRun, key)
	if len(fs.auto.runs) != 3 {
		t.Fatalf("%d runs reached the store", len(fs.auto.runs))
	}
	first, second, dated := fs.auto.runs[0], fs.auto.runs[1], fs.auto.runs[2]
	if string(first.Hash) != string(second.Hash) || first.ClearingDate.Equal(second.ClearingDate) {
		t.Fatalf("same body on another day: hashes equal %v, dates %s %s", string(first.Hash) == string(second.Hash), first.ClearingDate, second.ClearingDate)
	}
	if string(dated.Hash) == string(second.Hash) {
		t.Fatal("an explicit clearing date is another body")
	}
}
