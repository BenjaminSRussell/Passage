package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/store"
)

const (
	testBatch     = "0190a000-0000-7000-8000-000000000010"
	batchPath     = "/api/v1/ledger/journal-batches/" + testBatch
	journalDuties = dutyRead + "," + dutyJournalWrite + "," + dutyJournalApprove + "," + dutyPost + "," + dutySetupWrite
	batchBody     = `{"ledgerId":"` + testLedger + `","journalName":"GJ","description":"Accrual","postingDate":"2026-03-31",
		"documentDate":"2026-03-30",
		"lines":[{"account":"6010","dimensions":{"DEPT":"D100"},"debitCredit":"D","amount":{"currency":"USD","value":"10.50"},"text":"Freight"},
		         {"account":"2100","debitCredit":"C","amount":{"currency":"USD","value":"10.5"}}]}`
)

func TestCheckBatchLines_TableDriven(t *testing.T) {
	ok := batchLineDTO{Account: "6010", DebitCredit: "D", Amount: amountDTO{"USD", "10.00"}}
	tests := []struct {
		name  string
		edit  func(l *batchLineDTO)
		field string
	}{
		{"valid debit", func(*batchLineDTO) {}, ""},
		{"zero amount is left to the posting rules", func(l *batchLineDTO) { l.Amount.Value = "0" }, ""},
		{"bad side", func(l *batchLineDTO) { l.DebitCredit = "X" }, "lines[0].debitCredit"},
		{"negative amount", func(l *batchLineDTO) { l.Amount.Value = "-1.00" }, "lines[0].amount.value"},
		{"not a number", func(l *batchLineDTO) { l.Amount.Value = "ten" }, "lines[0].amount.value"},
		{"too many decimals for USD", func(l *batchLineDTO) { l.Amount.Value = "1.001" }, "lines[0].amount.value"},
		{"JPY has no decimals", func(l *batchLineDTO) { l.Amount = amountDTO{"JPY", "1.5"} }, "lines[0].amount.value"},
		{"beyond numeric(20,6)", func(l *batchLineDTO) { l.Amount.Value = "100000000000000" }, "lines[0].amount.value"},
		{"bad account", func(l *batchLineDTO) { l.Account = "60 10" }, "lines[0].account"},
		{"bad currency", func(l *batchLineDTO) { l.Amount.Currency = "us" }, "lines[0].amount.currency"},
		{"bad dimension attribute", func(l *batchLineDTO) { l.Dimensions = map[string]string{"dept": "D1"} }, "lines[0].dimensions"},
		{"empty dimension value", func(l *batchLineDTO) { l.Dimensions = map[string]string{"DEPT": ""} }, "lines[0].dimensions"},
		{"text too long", func(l *batchLineDTO) { l.Text = strings.Repeat("é", 256) }, "lines[0].text"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line := ok
			tt.edit(&line)
			fields := map[string]string{}
			out := checkBatchLines([]batchLineDTO{line}, fields)
			if tt.field == "" {
				if len(fields) != 0 || len(out) != 1 {
					t.Fatalf("unexpected fields %v", fields)
				}
				return
			}
			if fields[tt.field] == "" || len(fields) != 1 {
				t.Fatalf("want exactly %q, got %v", tt.field, fields)
			}
		})
	}
	fields := map[string]string{}
	if out := checkBatchLines(nil, fields); out != nil || fields["lines"] == "" {
		t.Fatalf("an empty batch must be refused, got %v %v", out, fields)
	}
	fields = map[string]string{}
	checkBatchLines(make([]batchLineDTO, domain.MaxLinesPerEntry+1), fields)
	if !strings.Contains(fields["lines"], "10000") {
		t.Fatalf("more lines than one entry can hold must be refused, got %v", fields)
	}
}

func TestCreateBatch_ValidationReturns400WithEveryField(t *testing.T) {
	fs := &fakeStore{}
	body := `{"ledgerId":"not-a-uuid","journalName":"bad name!","postingDate":"2026-13-40","documentDate":"31/03/2026","lines":[]}`
	rec := call(server(fs), http.MethodPost, "/api/v1/ledger/journal-batches", body, journalDuties, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	errs, _ := decode(t, rec)["errors"].(map[string]any)
	for _, field := range []string{"ledgerId", "journalName", "postingDate", "documentDate", "lines"} {
		if errs[field] == nil {
			t.Errorf("missing field error %q in %v", field, errs)
		}
	}
	if len(fs.batchCalls) != 0 {
		t.Fatalf("an invalid request must not reach the store: %+v", fs.batchCalls)
	}
}

func TestCreateBatch_PassesNormalisedLinesAndSetsETag(t *testing.T) {
	fs := &fakeStore{batch: store.Batch{ID: testBatch, Status: domain.BatchDraft, Version: 1}}
	rec := call(server(fs), http.MethodPost, "/api/v1/ledger/journal-batches", batchBody, journalDuties, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("ETag = %q, want \"1\"", rec.Header().Get("ETag"))
	}
	in := fs.batchCalls[0].create
	if in.LedgerID != testLedger || in.JournalName != "GJ" || in.Description != "Accrual" ||
		in.PostingDate.Format("2006-01-02") != "2026-03-31" || in.DocumentDate == nil || in.DocumentDate.Format("2006-01-02") != "2026-03-30" {
		t.Fatalf("header passed to the store: %+v", in)
	}
	if len(in.Lines) != 2 || in.Lines[0].Amount != "10.5" || in.Lines[1].Amount != "10.5" || in.Lines[0].Currency != "USD" ||
		in.Lines[0].Dimensions["DEPT"] != "D100" || in.Lines[0].Text != "Freight" || in.Lines[1].DebitCredit != "C" {
		t.Fatalf("lines passed to the store: %+v", in.Lines)
	}
}

func TestBatchActions_PassIfMatchVersion(t *testing.T) {
	for _, op := range []string{"submit", "approve", "reject", "cancel"} {
		fs := &fakeStore{batch: store.Batch{ID: testBatch, Version: 4}}
		rec := call(server(fs), http.MethodPost, batchPath+"/"+op, "", journalDuties, map[string]string{"If-Match": `"3"`})
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d, body %s", op, rec.Code, rec.Body.String())
		}
		got := fs.batchCalls[0]
		if got.op != op || got.id != testBatch || got.version != 3 || rec.Header().Get("ETag") != `"4"` {
			t.Fatalf("%s: store call %+v, ETag %q", op, got, rec.Header().Get("ETag"))
		}
	}
	// Without If-Match the store gets version 0 (no check); a malformed tag is a 400.
	fs := &fakeStore{}
	if rec := call(server(fs), http.MethodPost, batchPath+"/submit", "", journalDuties, nil); rec.Code != http.StatusOK || fs.batchCalls[0].version != 0 {
		t.Fatalf("no If-Match: status %d, call %+v", rec.Code, fs.batchCalls)
	}
	fs = &fakeStore{}
	rec := call(server(fs), http.MethodPost, batchPath+"/submit", "", journalDuties, map[string]string{"If-Match": "*"})
	if rec.Code != http.StatusBadRequest || len(fs.batchCalls) != 0 {
		t.Fatalf("malformed If-Match: status %d, calls %+v", rec.Code, fs.batchCalls)
	}
}

func TestPatchBatch_PartialEditAndClearDocumentDate(t *testing.T) {
	fs := &fakeStore{batch: store.Batch{ID: testBatch, Version: 2}}
	rec := call(server(fs), http.MethodPatch, batchPath, `{"description":"Corrected","documentDate":""}`, journalDuties,
		map[string]string{"If-Match": `W/"1"`})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	c := fs.batchCalls[0]
	if c.version != 1 || c.patch.Description == nil || *c.patch.Description != "Corrected" || !c.patch.ClearDocumentDate ||
		c.patch.PostingDate != nil || c.patch.Lines != nil {
		t.Fatalf("patch passed to the store: %+v", c)
	}
	fs = &fakeStore{}
	rec = call(server(fs), http.MethodPatch, batchPath, `{"postingDate":"tomorrow","lines":[]}`, journalDuties, nil)
	errs, _ := decode(t, rec)["errors"].(map[string]any)
	if rec.Code != http.StatusBadRequest || errs["postingDate"] == nil || errs["lines"] == nil || len(fs.batchCalls) != 0 {
		t.Fatalf("invalid patch: status %d, errors %v", rec.Code, errs)
	}
}

func TestPostBatch_KeyDutyAndReplay(t *testing.T) {
	fs := &fakeStore{}
	rec := call(server(fs), http.MethodPost, batchPath+"/post", "", journalDuties, nil)
	if rec.Code != http.StatusBadRequest || len(fs.batchCalls) != 0 {
		t.Fatalf("missing Idempotency-Key: status %d", rec.Code)
	}

	fs = &fakeStore{}
	rec = call(server(fs), http.MethodPost, batchPath+"/post", "", journalDuties,
		map[string]string{"Idempotency-Key": "  post-1  ", "If-Match": `"5"`})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	in := fs.batchCalls[0].post
	if in.Key != "post-1" || in.Version != 5 || in.CanPostDirect || len(in.TraceID) != 32 || in.Today.Format("2006-01-02") != "2026-03-15" {
		t.Fatalf("post input %+v", in)
	}

	fs = &fakeStore{replay: true}
	rec = call(server(fs), http.MethodPost, batchPath+"/post", "", journalDuties+","+store.DutyPostDirect,
		map[string]string{"Idempotency-Key": "post-1"})
	if rec.Code != http.StatusOK || !fs.batchCalls[0].post.CanPostDirect {
		t.Fatalf("replay: status %d, input %+v", rec.Code, fs.batchCalls[0].post)
	}
}

func TestBatchErrors_AreProblems(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"state", httpx.NewProblem(http.StatusConflict, store.CodeBatchState, "no"), http.StatusConflict, store.CodeBatchState},
		{"version", httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale"), http.StatusPreconditionFailed, store.CodeVersionConflict},
		{"self approval", httpx.NewProblem(http.StatusForbidden, store.CodeSelfApproval, "no"), http.StatusForbidden, store.CodeSelfApproval},
		{"rule violations", &domain.RejectedError{Violations: []domain.Violation{{Code: "LEDGER_UNBALANCED", Message: "x"}}},
			http.StatusUnprocessableEntity, "LEDGER_UNBALANCED"},
	}
	for _, tt := range tests {
		fs := &fakeStore{err: tt.err}
		rec := call(server(fs), http.MethodPost, batchPath+"/submit", "", journalDuties, nil)
		if rec.Code != tt.status || decode(t, rec)["code"] != tt.code {
			t.Errorf("%s: status %d body %s", tt.name, rec.Code, rec.Body.String())
		}
	}
	rec := call(server(&fakeStore{}), http.MethodGet, "/api/v1/ledger/journal-batches/not-a-uuid", "", journalDuties, nil)
	if rec.Code != http.StatusNotFound || decode(t, rec)["code"] != store.CodeBatchNotFound {
		t.Fatalf("malformed id: status %d", rec.Code)
	}
}

func TestListBatches_FiltersAndValidation(t *testing.T) {
	fs := &fakeStore{batch: store.Batch{ID: testBatch}}
	rec := call(server(fs), http.MethodGet, "/api/v1/ledger/journal-batches?ledger="+testLedger+"&status=in_approval&journalName=GJ",
		"", journalDuties, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if f := fs.batchCalls[0].filter; f.LedgerID != testLedger || f.Status != "in_approval" || f.JournalName != "GJ" {
		t.Fatalf("filter %+v", f)
	}
	fs = &fakeStore{}
	rec = call(server(fs), http.MethodGet, "/api/v1/ledger/journal-batches?ledger=x&status=parked", "", journalDuties, nil)
	errs, _ := decode(t, rec)["errors"].(map[string]any)
	if rec.Code != http.StatusBadRequest || errs["ledger"] == nil || errs["status"] == nil || len(fs.batchCalls) != 0 {
		t.Fatalf("bad filter: status %d errors %v", rec.Code, errs)
	}
}

func TestJournalNames_PutDefaultsAndValidation(t *testing.T) {
	fs := &fakeStore{created: true}
	rec := call(server(fs), http.MethodPut, "/api/v1/ledger/journal-names/GJ", `{"name":" General journal ","requiresApproval":true}`, journalDuties, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	got := fs.putNames[0]
	if got.Code != "GJ" || got.Name != "General journal" || !got.RequiresApproval || got.LinesLimit != domain.DefaultBatchLinesLimit || !got.Active {
		t.Fatalf("defaults not applied: %+v", got)
	}

	fs = &fakeStore{}
	rec = call(server(fs), http.MethodPut, "/api/v1/ledger/journal-names/G%20J",
		`{"name":"","selfApprovalAllowed":true,"linesLimit":10001}`, journalDuties, nil)
	errs, _ := decode(t, rec)["errors"].(map[string]any)
	for _, field := range []string{"code", "name", "selfApprovalAllowed", "linesLimit"} {
		if errs[field] == nil {
			t.Errorf("missing field error %q in %v", field, errs)
		}
	}
	if rec.Code != http.StatusBadRequest || len(fs.putNames) != 0 {
		t.Fatalf("status %d, writes %+v", rec.Code, fs.putNames)
	}

	fs = &fakeStore{journalNames: []store.JournalName{{Code: "GJ", Name: "General journal"}}}
	if rec := call(server(fs), http.MethodGet, "/api/v1/ledger/journal-names/GJ", "", journalDuties, nil); rec.Code != http.StatusOK {
		t.Fatalf("get: status %d", rec.Code)
	}
	if rec := call(server(fs), http.MethodGet, "/api/v1/ledger/journal-names/NOPE", "", journalDuties, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("get unknown: status %d", rec.Code)
	}
}

func TestBatchRoutes_RequireDuties(t *testing.T) {
	routes := []struct {
		method, path, body, duty string
	}{
		{http.MethodGet, "/api/v1/ledger/journal-names", "", dutyRead},
		{http.MethodPut, "/api/v1/ledger/journal-names/GJ", `{"name":"x"}`, dutySetupWrite},
		{http.MethodGet, "/api/v1/ledger/journal-batches", "", dutyRead},
		{http.MethodPost, "/api/v1/ledger/journal-batches", batchBody, dutyJournalWrite},
		{http.MethodGet, batchPath, "", dutyRead},
		{http.MethodPatch, batchPath, `{}`, dutyJournalWrite},
		{http.MethodPost, batchPath + "/submit", "", dutyJournalWrite},
		{http.MethodPost, batchPath + "/cancel", "", dutyJournalWrite},
		{http.MethodPost, batchPath + "/approve", "", dutyJournalApprove},
		{http.MethodPost, batchPath + "/reject", "", dutyJournalApprove},
		{http.MethodPost, batchPath + "/post", "", dutyPost},
	}
	for _, rt := range routes {
		fs := &fakeStore{}
		others := strings.ReplaceAll(journalDuties, rt.duty, "")
		rec := call(server(fs), rt.method, rt.path, rt.body, others, map[string]string{"Idempotency-Key": "k"})
		if rec.Code != http.StatusForbidden || len(fs.batchCalls)+len(fs.putNames) != 0 {
			t.Errorf("%s %s without %s: status %d", rt.method, rt.path, rt.duty, rec.Code)
		}
	}
}
