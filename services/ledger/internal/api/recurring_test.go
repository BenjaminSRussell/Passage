package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/ledger/internal/store"
)

const (
	testRecurring = "0190a000-0000-7000-8000-0000000000b1"
	testLedgerID  = "0190a000-0000-7000-8000-0000000000c1"
)

// recurringFake records the recurring journal calls of fakeStore.
type recurringFake struct {
	journal   store.RecurringJournal
	list      []store.RecurringJournal
	runs      []store.RecurringRun
	created   bool
	put       []store.RecurringInput
	putVer    []int
	status    []string
	generated []store.GenerateRecurringInput
	genNew    bool
}

func (f *fakeStore) ListRecurring(_ context.Context, _ string) ([]store.RecurringJournal, error) {
	return f.rec.list, f.err
}

func (f *fakeStore) GetRecurring(_ context.Context, _ string) (store.RecurringJournal, error) {
	return f.rec.journal, f.err
}

func (f *fakeStore) PutRecurring(_ context.Context, _ string, version int, in store.RecurringInput) (store.RecurringJournal, bool, error) {
	f.rec.put = append(f.rec.put, in)
	f.rec.putVer = append(f.rec.putVer, version)
	return f.rec.journal, f.rec.created, f.err
}

func (f *fakeStore) SetRecurringStatus(_ context.Context, _ string, _ int, status string, _ time.Time) (store.RecurringJournal, error) {
	f.rec.status = append(f.rec.status, status)
	return f.rec.journal, f.err
}

func (f *fakeStore) GenerateRecurring(_ context.Context, in store.GenerateRecurringInput) (store.RecurringRun, bool, error) {
	f.rec.generated = append(f.rec.generated, in)
	return store.RecurringRun{ID: "r1", BatchID: "b1", ScheduledDate: "2026-04-30"}, f.rec.genNew, f.err
}

func (f *fakeStore) ListRecurringRuns(_ context.Context, _ string) ([]store.RecurringRun, error) {
	return f.rec.runs, f.err
}

const recurringBody = `{"ledgerId":"` + testLedgerID + `","journalName":"GJ","name":"Monthly rent","frequency":"monthly","dayOfMonth":0,
"startDate":"2026-04-01","endDate":"2026-12-31","baseAmount":"1000.00","lines":[
{"account":"6100","debitCredit":"D","amountKind":"percent","amount":{"currency":"EUR","value":"60"}},
{"account":"6200","debitCredit":"D","amountKind":"fixed","amount":{"currency":"EUR","value":"100.00"}},
{"account":"2100","debitCredit":"C","amountKind":"percent","amount":{"currency":"EUR","value":"100"}}]}`

func TestPutRecurring_CreatedThenReplacedWithIfMatch(t *testing.T) {
	fs := &fakeStore{}
	fs.rec.created = true
	fs.rec.journal = store.RecurringJournal{ID: testRecurring, Version: 1}
	h := server(fs)
	rec := call(h, http.MethodPut, "/v1/ledger/recurring-journals/"+testRecurring, recurringBody, dutyJournalWrite, nil)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("create: %d etag %q: %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	in := fs.rec.put[0]
	if in.Frequency != "monthly" || in.DayOfMonth != 0 || in.BaseAmount == nil || *in.BaseAmount != "1000" ||
		len(in.Lines) != 3 || in.Lines[0].AmountKind != "percent" || in.Lines[0].Amount != "60" || in.EndDate == nil {
		t.Fatalf("request not mapped: %+v", in)
	}
	fs.rec.created = false
	rec = call(h, http.MethodPut, "/v1/ledger/recurring-journals/"+testRecurring, recurringBody, dutyJournalWrite,
		map[string]string{"If-Match": `"1"`})
	if rec.Code != http.StatusOK || fs.rec.putVer[1] != 1 {
		t.Fatalf("replace: %d version %v", rec.Code, fs.rec.putVer)
	}
}

func TestPutRecurring_RequiresJournalWriteDuty(t *testing.T) {
	fs := &fakeStore{}
	rec := call(server(fs), http.MethodPut, "/v1/ledger/recurring-journals/"+testRecurring, recurringBody, dutyRead, nil)
	if rec.Code != http.StatusForbidden || len(fs.rec.put) != 0 {
		t.Fatalf("status %d, writes %d", rec.Code, len(fs.rec.put))
	}
}

func TestPutRecurring_Validation(t *testing.T) {
	line := func(kind, value string) string {
		return `"lines":[{"account":"6100","debitCredit":"D","amountKind":"` + kind + `","amount":{"currency":"EUR","value":"` + value + `"}}]`
	}
	head := `"ledgerId":"` + testLedgerID + `","journalName":"GJ","name":"x","frequency":"monthly","dayOfMonth":5,"startDate":"2026-04-01",`
	tests := []struct {
		name, body, field string
	}{
		{"bad frequency", `{` + head[:len(head)-1] + `,"frequency":"weekly",` + line("fixed", "1") + `}`, "frequency"},
		{"day out of range", `{` + head + `"dayOfMonth":32,` + line("fixed", "1") + `}`, "dayOfMonth"},
		{"end before start", `{` + head + `"endDate":"2026-03-01",` + line("fixed", "1") + `}`, "endDate"},
		{"percent over 1000", `{` + head + line("percent", "1000.5") + `}`, "lines[0].amount.value"},
		{"fixed over scale", `{` + head + line("fixed", "1.005") + `}`, "lines[0].amount.value"},
		{"unknown kind", `{` + head + line("ratio", "1") + `}`, "lines[0].amountKind"},
		{"no lines", `{` + head + `"lines":[]}`, "lines"},
		{"bad base", `{` + head + `"baseAmount":"-5",` + line("fixed", "1") + `}`, "baseAmount"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := &fakeStore{}
			rec := call(server(fs), http.MethodPut, "/v1/ledger/recurring-journals/"+testRecurring, tc.body, dutyJournalWrite, nil)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			errs, _ := decode(t, rec)["errors"].(map[string]any)
			if _, ok := errs[tc.field]; !ok {
				t.Fatalf("want error on %s, got %v", tc.field, errs)
			}
			if len(fs.rec.put) != 0 {
				t.Fatal("an invalid template must not reach the store")
			}
		})
	}
}

func TestRecurring_PauseResumeGenerateAndReads(t *testing.T) {
	fs := &fakeStore{}
	fs.rec.journal = store.RecurringJournal{ID: testRecurring, Version: 3}
	fs.rec.list = []store.RecurringJournal{fs.rec.journal}
	fs.rec.runs = []store.RecurringRun{{ID: "r1", ScheduledDate: "2026-04-30"}}
	h := server(fs)
	base := "/v1/ledger/recurring-journals/" + testRecurring
	if rec := call(h, http.MethodPost, base+"/pause", "", dutyJournalWrite, nil); rec.Code != http.StatusOK {
		t.Fatalf("pause %d", rec.Code)
	}
	if rec := call(h, http.MethodPost, base+"/resume", "", dutyJournalWrite, nil); rec.Code != http.StatusOK {
		t.Fatalf("resume %d", rec.Code)
	}
	if got := fs.rec.status; len(got) != 2 || got[0] != "paused" || got[1] != "active" {
		t.Fatalf("statuses %v", got)
	}
	// Generate: 201 for a new run, 200 for a replay; the date and base are mapped.
	fs.rec.genNew = true
	rec := call(h, http.MethodPost, base+"/generate", `{"scheduledDate":"2026-04-30","baseAmount":"250.50"}`, dutyJournalWrite, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("generate %d: %s", rec.Code, rec.Body.String())
	}
	g := fs.rec.generated[0]
	if g.ScheduledDate == nil || g.ScheduledDate.Format(time.DateOnly) != "2026-04-30" || g.BaseAmount == nil || *g.BaseAmount != "250.5" {
		t.Fatalf("generate input %+v", g)
	}
	fs.rec.genNew = false
	if rec := call(h, http.MethodPost, base+"/generate", `{"scheduledDate":"2026-04-30"}`, dutyJournalWrite, nil); rec.Code != http.StatusOK {
		t.Fatalf("replay %d", rec.Code)
	}
	// Without a date a retry could generate the following period, so the date is required.
	calls := len(fs.rec.generated)
	for _, body := range []string{"", `{}`, `{"baseAmount":"10"}`} {
		rec := call(h, http.MethodPost, base+"/generate", body, dutyJournalWrite, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("generate %q without a date: %d", body, rec.Code)
		}
	}
	if rec := call(h, http.MethodPost, base+"/generate", `{"scheduledDate":"2026-04-30"}`, dutyRead, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("generate without the write duty: %d", rec.Code)
	}
	if len(fs.rec.generated) != calls {
		t.Fatal("a refused generate must not reach the store")
	}
	if rec := call(h, http.MethodPost, base+"/generate", `{"scheduledDate":"30/04/2026"}`, dutyJournalWrite, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad date %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, "/v1/ledger/recurring-journals", "", dutyRead, nil); rec.Code != http.StatusOK ||
		len(decode(t, rec)["items"].([]any)) != 1 {
		t.Fatalf("list %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, "/v1/ledger/recurring-journals?status=done", "", dutyRead, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad status %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, base, "", dutyRead, nil); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"3"` {
		t.Fatalf("get %d %q", rec.Code, rec.Header().Get("ETag"))
	}
	if rec := call(h, http.MethodGet, base+"/runs", "", dutyRead, nil); rec.Code != http.StatusOK {
		t.Fatalf("runs %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, "/v1/ledger/recurring-journals/nope", "", dutyRead, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id %d", rec.Code)
	}
}
