package api_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
)

const (
	journalCreateBody = `{"legalEntityId":"%s","name":"%s","smartRound":%s,"lines":[%s]}`
	updateLine        = `{"op":"UPDATE","recordId":"%s","expectedVersion":%d,"adjustPct":"%s"}`
)

// activeRecord creates and activates a PRICE record and returns its id (version 2 after activation).
func activeRecord(t *testing.T, h http.Handler, amount string) string {
	t.Helper()
	code, created := call(t, h, http.MethodPost, "/v1/pricing/condition-records", allDuties, fmt.Sprintf(recordBody, legalEntity, amount))
	if code != http.StatusCreated {
		t.Fatalf("create status %d: %v", code, created)
	}
	id := created["id"].(string)
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/condition-records/"+id+":activate", allDuties, `{"version":1}`); code != http.StatusOK {
		t.Fatalf("activate status %d: %v", code, out)
	}
	return id
}

func TestJournal_PostAndReverseOverHTTP(t *testing.T) {
	// AC-26.7 and AC-26.10 over HTTP: a 5% raise of 7.41 with smart rounding stores 7.99; reversal restores 7.41.
	fs := newFake()
	h := router(fs)
	id := activeRecord(t, h, "7.41")
	body := fmt.Sprintf(journalCreateBody, legalEntity, "Raise 5 percent", "true", fmt.Sprintf(updateLine, id, 2, "5"))

	code, created := call(t, h, http.MethodPost, "/v1/pricing/journals", allDuties, body)
	if code != http.StatusCreated || created["status"] != domain.JournalOpen || created["lineCount"] != float64(1) {
		t.Fatalf("create = %d %v", code, created)
	}
	jid := created["id"].(string)

	if code, out := call(t, h, http.MethodPost, "/v1/pricing/journals/"+jid+":post", allDuties, ""); code != http.StatusConflict {
		t.Fatalf("posting an unvalidated journal must be 409, got %d %v", code, out)
	}
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/journals/"+jid+":validate", allDuties, ""); code != http.StatusOK || out["status"] != domain.JournalValidated {
		t.Fatalf("validate = %d %v", code, out)
	}
	code, posted := call(t, h, http.MethodPost, "/v1/pricing/journals/"+jid+":post", allDuties, "")
	if code != http.StatusOK || posted["status"] != domain.JournalPosted {
		t.Fatalf("post = %d %v", code, posted)
	}
	if got := fs.records[id].Amount.String(); got != "7.99" {
		t.Fatalf("stored amount = %s, want 7.99", got)
	}

	code, rev := call(t, h, http.MethodPost, "/v1/pricing/journals/"+jid+":reverse", allDuties, "")
	if code != http.StatusCreated || rev["reversesJournalId"] != jid || rev["status"] != domain.JournalPosted {
		t.Fatalf("reverse = %d %v", code, rev)
	}
	if got := fs.records[id].Amount.String(); got != "7.41" {
		t.Fatalf("after reversal amount = %s, want 7.41", got)
	}
	if code, orig := call(t, h, http.MethodGet, "/v1/pricing/journals/"+jid, allDuties, ""); code != http.StatusOK || orig["status"] != domain.JournalReversed {
		t.Fatalf("original journal must be REVERSED, got %d %v", code, orig)
	}
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/journals/"+jid+":reverse", allDuties, ""); code != http.StatusConflict {
		t.Fatalf("a reversed journal must not reverse again, got %d %v", code, out)
	}
}

func TestJournal_OverlapBlocksPostAndNamesTheLine(t *testing.T) {
	fs := newFake()
	h := router(fs)
	activeRecord(t, h, "10") // same key and validity as the CREATE below
	create := `{"op":"CREATE","record":` + fmt.Sprintf(recordBody, legalEntity, "12") + `}`
	body := fmt.Sprintf(journalCreateBody, legalEntity, "Clash", "false", create)
	code, created := call(t, h, http.MethodPost, "/v1/pricing/journals", allDuties, body)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %v", code, created)
	}
	jid := created["id"].(string)
	code, out := call(t, h, http.MethodPost, "/v1/pricing/journals/"+jid+":validate", allDuties, "")
	errs, _ := out["errors"].([]any)
	if code != http.StatusOK || out["status"] != domain.JournalOpen || len(errs) != 1 {
		t.Fatalf("validate must keep the journal OPEN with one line error, got %d %v", code, out)
	}
	first := errs[0].(map[string]any)
	if first["code"] != domain.CodeOverlap || first["lineNo"] != float64(1) {
		t.Fatalf("error = %v; want overlap on line 1", first)
	}
	if code, _ := call(t, h, http.MethodPost, "/v1/pricing/journals/"+jid+":post", allDuties, ""); code != http.StatusConflict {
		t.Fatalf("a journal that has not validated clean must not post, got %d", code)
	}
}

func TestJournal_RejectsMalformedLines(t *testing.T) {
	h := router(newFake())
	cases := map[string]string{
		"restore is internal":  fmt.Sprintf(journalCreateBody, legalEntity, "x", "false", `{"op":"RESTORE","recordId":"00000000-0000-0000-0009-000000000001","expectedVersion":2}`),
		"unknown operation":    fmt.Sprintf(journalCreateBody, legalEntity, "x", "false", `{"op":"MERGE","recordId":"00000000-0000-0000-0009-000000000001","expectedVersion":2}`),
		"update without ids":   fmt.Sprintf(journalCreateBody, legalEntity, "x", "false", `{"op":"UPDATE","recordId":"nope","expectedVersion":0,"adjustPct":"5"}`),
		"five-decimal percent": fmt.Sprintf(journalCreateBody, legalEntity, "x", "false", `{"op":"UPDATE","recordId":"00000000-0000-0000-0009-000000000001","expectedVersion":1,"adjustPct":"1.00001"}`),
		"empty journal":        fmt.Sprintf(journalCreateBody, legalEntity, "x", "false", ""),
		"delete with a record": fmt.Sprintf(journalCreateBody, legalEntity, "x", "false", `{"op":"DELETE","recordId":"00000000-0000-0000-0009-000000000001","expectedVersion":1,"record":{"id":"x"}}`),
	}
	for name, body := range cases {
		if code, out := call(t, h, http.MethodPost, "/v1/pricing/journals", allDuties, body); code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: want 422, got %d %v", name, code, out)
		}
	}
	if code, _ := call(t, h, http.MethodGet, "/v1/pricing/journals/not-a-uuid", allDuties, ""); code != http.StatusNotFound {
		t.Fatalf("malformed journal id must be 404, got %d", code)
	}
	if code, _ := call(t, h, http.MethodPost, "/v1/pricing/journals", "pricing.view", fmt.Sprintf(journalCreateBody, legalEntity, "x", "false", "")); code != http.StatusForbidden {
		t.Fatalf("a viewer must not create journals, got %d", code)
	}
}

func TestRebate_AccrualAC268OverHTTP(t *testing.T) {
	// AC-26.8: a 2% monthly rebate on a 10,000.00 invoice accrues 200.00; a redelivered line is not accrued twice.
	h := router(newFake())
	agreement := `{"legalEntityId":"` + legalEntity + `","name":"Monthly 2%","customerId":"00000000-0000-0000-0005-000000000001",
		"basis":"NET_AMOUNT","period":"MONTH","tiers":[{"from":"0","ratePct":"2"}],"currency":"USD",
		"validFrom":"2026-01-01","validTo":"2026-12-31"}`
	code, created := call(t, h, http.MethodPost, "/v1/pricing/rebates/agreements", allDuties, agreement)
	if code != http.StatusCreated {
		t.Fatalf("agreement = %d %v", code, created)
	}
	aid := created["id"].(string)
	accrual := fmt.Sprintf(`{"agreementId":"%s","invoiceLineId":"INV-1-L1","invoiceDate":"2026-10-05","value":"10000.00","currency":"USD"}`, aid)
	code, first := call(t, h, http.MethodPost, "/v1/pricing/rebates/accruals", allDuties, accrual)
	if code != http.StatusCreated || first["amount"] != "200" || first["periodId"] != "2026-10" {
		t.Fatalf("accrual = %d %v; want 201 amount 200 period 2026-10", code, first)
	}
	if code, again := call(t, h, http.MethodPost, "/v1/pricing/rebates/accruals", allDuties, accrual); code != http.StatusOK || again["id"] != first["id"] {
		t.Fatalf("replayed invoice line = %d %v; want 200 and the same accrual", code, again)
	}
	code, list := call(t, h, http.MethodGet, "/v1/pricing/rebates/accruals?period=2026-10", allDuties, "")
	items, _ := list["items"].([]any)
	if code != http.StatusOK || len(items) != 1 {
		t.Fatalf("list = %d %v; want one accrual", code, list)
	}
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/rebates/accruals", allDuties,
		fmt.Sprintf(`{"agreementId":"%s","invoiceLineId":"INV-2","invoiceDate":"2026-10-06","value":"1","currency":"EUR"}`, aid)); code != http.StatusUnprocessableEntity {
		t.Fatalf("currency mismatch must be 422, got %d %v", code, out)
	}
}

func TestRebate_AgreementValidation(t *testing.T) {
	h := router(newFake())
	bad := `{"legalEntityId":"` + legalEntity + `","name":"x","basis":"NET_AMOUNT","period":"MONTH",
		"tiers":[{"from":"0","ratePct":"0"}],"currency":"USD","validFrom":"2026-01-01","validTo":"2026-12-31"}`
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/rebates/agreements", allDuties, bad); code != http.StatusUnprocessableEntity {
		t.Fatalf("agreement without a customer and a zero rate must be 422, got %d %v", code, out)
	}
}

func TestJournal_ReadShowsLinesAndTheLogListsJournals(t *testing.T) {
	fs := newFake()
	h := router(fs)
	id := activeRecord(t, h, "10")
	body := fmt.Sprintf(journalCreateBody, legalEntity, "Raise 10 percent", "false", fmt.Sprintf(updateLine, id, 2, "10"))
	_, created := call(t, h, http.MethodPost, "/v1/pricing/journals", allDuties, body)
	jid := created["id"].(string)
	call(t, h, http.MethodPost, "/v1/pricing/journals/"+jid+":validate", allDuties, "")

	// A viewer reads the journal: each line carries its before and after snapshots.
	code, got := call(t, h, http.MethodGet, "/v1/pricing/journals/"+jid, "pricing.view", "")
	lines, _ := got["lines"].([]any)
	if code != http.StatusOK || len(lines) != 1 {
		t.Fatalf("read = %d %v; want one line", code, got)
	}
	line := lines[0].(map[string]any)
	before, _ := line["before"].(map[string]any)
	after, _ := line["after"].(map[string]any)
	if line["op"] != "UPDATE" || before["amount"] != "10" || after["amount"] != "11" || after["version"] != float64(3) {
		t.Fatalf("line = %v; want UPDATE 10 -> 11 at version 3", line)
	}

	code, list := call(t, h, http.MethodGet, "/v1/pricing/journals?status=VALIDATED", "pricing.view", "")
	items, _ := list["items"].([]any)
	if code != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["id"] != jid {
		t.Fatalf("log = %d %v; want the validated journal", code, list)
	}
	if _, has := items[0].(map[string]any)["lines"]; has {
		t.Fatalf("the log must not carry lines: %v", items[0])
	}
	if code, out := call(t, h, http.MethodGet, "/v1/pricing/journals?status=DONE", "pricing.view", ""); code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown status filter must be 422, got %d %v", code, out)
	}
	if code, _ := call(t, h, http.MethodPost, "/v1/pricing/journals/"+jid+":post", "pricing.view", ""); code != http.StatusForbidden {
		t.Fatalf("a viewer must not post journals, got %d", code)
	}
}

func TestJournal_PostAndReversePassTheIdempotencyKey(t *testing.T) {
	fs := newFake()
	h := router(fs)
	id := activeRecord(t, h, "10")
	_, created := call(t, h, http.MethodPost, "/v1/pricing/journals", allDuties,
		fmt.Sprintf(journalCreateBody, legalEntity, "Raise", "false", fmt.Sprintf(updateLine, id, 2, "1")))
	jid := created["id"].(string)
	call(t, h, http.MethodPost, "/v1/pricing/journals/"+jid+":validate", allDuties, "")
	fs.keys = nil
	call(t, h, http.MethodPost, "/v1/pricing/journals/"+jid+":post", allDuties, "", "Idempotency-Key", "post-1")
	call(t, h, http.MethodPost, "/v1/pricing/journals/"+jid+":reverse", allDuties, "", "Idempotency-Key", "rev-1")
	if len(fs.keys) != 2 || fs.keys[0] != "post-1" || fs.keys[1] != "rev-1" {
		t.Fatalf("keys reaching the store = %v; want [post-1 rev-1]", fs.keys)
	}
}

func TestJournal_OneRecordOnTwoLinesIsRefused(t *testing.T) {
	h := router(newFake())
	id := activeRecord(t, h, "10")
	lines := fmt.Sprintf(updateLine, id, 2, "5") + `,{"op":"DELETE","recordId":"` + id + `","expectedVersion":2}`
	_, created := call(t, h, http.MethodPost, "/v1/pricing/journals", allDuties, fmt.Sprintf(journalCreateBody, legalEntity, "Twice", "false", lines))
	code, out := call(t, h, http.MethodPost, "/v1/pricing/journals/"+created["id"].(string)+":validate", allDuties, "")
	errs, _ := out["errors"].([]any)
	if code != http.StatusOK || out["status"] != domain.JournalOpen || len(errs) != 1 || errs[0].(map[string]any)["lineNo"] != float64(2) {
		t.Fatalf("validate = %d %v; want line 2 refused", code, out)
	}
}

func TestJournal_RecordErrorsNameTheLine(t *testing.T) {
	h := router(newFake())
	create := `{"op":"CREATE","record":{"type":"PRICE","customerKind":"all","productKind":"product","productKey":"P1","currency":"usd",
		"validFrom":"2026-01-01","validTo":"2026-12-31"}}`
	code, out := call(t, h, http.MethodPost, "/v1/pricing/journals", allDuties, fmt.Sprintf(journalCreateBody, legalEntity, "Bad", "false", create))
	errs, _ := out["errors"].(map[string]any)
	if code != http.StatusUnprocessableEntity || errs["lines[0].record.currency"] == nil {
		t.Fatalf("create = %d %v; want 422 with lines[0].record.currency", code, out)
	}
}
