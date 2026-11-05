package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func rekeyPostingsBody(lines ...string) string {
	return "[" + strings.Join(lines, ",") + "]"
}

func posting(kind, entity, ref, at, account, amount string) string {
	return fmt.Sprintf(`{"kind":%q,"entity":%q,"documentRef":%q,"postedAt":%q,"account":%q,"currency":"USD","amount":%q,"dimensions":"DEPT=SALES","text":"Re-key, %s"}`,
		kind, entity, ref, at, account, amount, ref)
}

// AC-49.7 (FR-49.9): when a rehearsal cutover rolls back, the Passage postings since T-0 are
// exported as re-keying files, kept with the plan and served as CSV.
func TestRollback_ExportsPostingsSinceT0AsRekeyingFiles(t *testing.T) {
	h, admin := dbServer(t)
	t0 := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	plan := decode[planDTO](t, do(t, h, request{method: "POST", path: "/api/v1/cutover/plans/generate", tenant: tenantA, duties: "migration.cutover.manage", body: generateBody(t0, 36), user: "manager"}))
	base := "/api/v1/cutover/plans/" + plan.PlanID
	rb := base + "/rollback"
	journals := base + "/rekeying/journals.csv"

	if r := do(t, h, request{method: "GET", path: journals, tenant: tenantA, duties: "migration.cutover.manage"}); r.Code != http.StatusConflict || problemCode(t, r) != CodeTransition {
		t.Fatalf("a plan that is not rolled back has no files: %d %s", r.Code, r.Body)
	}

	good := []string{
		posting("JOURNAL", "USMF", "JV-2", "2027-01-02T10:00:00Z", "1100", "250.00"),
		posting("JOURNAL", "USMF", "JV-2", "2027-01-02T10:00:00Z", "4000", "-250.00"),
		posting("DOCUMENT", "USMF", "SI-9", "2027-01-02T08:00:00Z", "1200", "125.50"),
		posting("JOURNAL", "USMF", "JV-1", "2027-01-01T09:00:00Z", "6100", "80"),
		posting("JOURNAL", "USMF", "JV-1", "2027-01-01T09:00:00Z", "2000", "-80"),
		// Made in legacy before T-0: counted and left out.
		posting("DOCUMENT", "USMF", "SI-0", "2026-12-31T17:00:00Z", "1200", "10"),
	}
	rejected := []struct {
		name, body, want string
	}{
		{"entity outside the plan", fmt.Sprintf(`{"postings":%s}`, rekeyPostingsBody(append(good, posting("DOCUMENT", "GBSI", "SI-7", "2027-01-02T08:00:00Z", "1200", "1"))...)), "does not cover"},
		{"unbalanced journal", fmt.Sprintf(`{"postings":%s}`, rekeyPostingsBody(posting("JOURNAL", "USMF", "JV-3", "2027-01-02T08:00:00Z", "1100", "1"))), "does not balance"},
	}
	for _, c := range rejected {
		r := do(t, h, request{method: "POST", path: rb, tenant: tenantA, duties: "migration.cutover.manage", body: c.body, user: "alice"})
		if r.Code != http.StatusUnprocessableEntity || !strings.Contains(r.Body.String(), c.want) {
			t.Fatalf("%s: %d %s", c.name, r.Code, r.Body)
		}
	}
	// A rejected export leaves the plan as it was: not rolled back, no lines, no event.
	if p := decode[planDTO](t, do(t, h, request{method: "GET", path: base, tenant: tenantA, duties: "migration.cutover.manage"})); p.RolledBack || p.Version != 1 || p.Rekeying != nil {
		t.Fatalf("plan after rejected rollbacks %+v", p)
	}
	if n := count(t, admin, `select count(*) from rekey_line`); n != 0 {
		t.Fatalf("rekey lines after rejected rollbacks = %d", n)
	}

	body := fmt.Sprintf(`{"postings":%s}`, rekeyPostingsBody(good...))
	done := rollbackBoth(t, h, rb, body, "rb-rekey")
	if done.Code != http.StatusOK {
		t.Fatalf("rollback: %d %s", done.Code, done.Body)
	}
	p := decode[planDTO](t, done)
	if !p.RolledBack || p.Rekeying == nil || *p.Rekeying != (rekeyingDTO{JournalLines: 4, DocumentLines: 1, Excluded: 1}) {
		t.Fatalf("rolled back plan %+v rekeying %+v", p, p.Rekeying)
	}
	if r := do(t, h, request{method: "POST", path: rb, tenant: tenantA, duties: "migration.cutover.manage", body: body, key: "rb-rekey-2", user: "bob"}); r.Code != http.StatusOK || r.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: %d %s", r.Code, r.Body)
	}
	if n := count(t, admin, `select count(*) from rekey_line where plan_id = $1`, plan.PlanID); n != 5 {
		t.Fatalf("stored rekey lines = %d, want 5 (the replay stores nothing)", n)
	}
	var payload []byte
	if err := admin.QueryRow(context.Background(), `select payload from outbox where type = 'dev.passage.cutover.plan.rolledback.v1' and aggregate_id = $1`, plan.PlanID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var evt struct {
		Data struct {
			Journal  int `json:"rekey_journal_lines"`
			Document int `json:"rekey_document_lines"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &evt); err != nil || evt.Data.Journal != 4 || evt.Data.Document != 1 {
		t.Fatalf("rolledback event %s (%v)", payload, err)
	}
	if got := decode[planDTO](t, do(t, h, request{method: "GET", path: base, tenant: tenantA, duties: "migration.cutover.manage"})); got.Rekeying == nil || got.Rekeying.JournalLines != 4 {
		t.Fatalf("stored plan rekeying %+v", got.Rekeying)
	}

	file := do(t, h, request{method: "GET", path: journals, tenant: tenantA, duties: "migration.cutover.manage"})
	if file.Code != http.StatusOK || !strings.HasPrefix(file.Header().Get("Content-Type"), "text/csv") ||
		!strings.Contains(file.Header().Get("Content-Disposition"), "rekey-"+plan.PlanID+"-journals.csv") {
		t.Fatalf("journals file: %d %v", file.Code, file.Header())
	}
	rows, err := csv.NewReader(file.Body).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, row := range rows[1:] {
		got = append(got, strings.Join(row, "|"))
	}
	want := []string{
		"USMF|JV-1|1|2027-01-01T09:00:00Z|6100|DEPT=SALES|USD|80.000000||Re-key, JV-1",
		"USMF|JV-1|2|2027-01-01T09:00:00Z|2000|DEPT=SALES|USD||80.000000|Re-key, JV-1",
		"USMF|JV-2|1|2027-01-02T10:00:00Z|1100|DEPT=SALES|USD|250.000000||Re-key, JV-2",
		"USMF|JV-2|2|2027-01-02T10:00:00Z|4000|DEPT=SALES|USD||250.000000|Re-key, JV-2",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("journal rows\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	docs := do(t, h, request{method: "GET", path: base + "/rekeying/documents.csv", tenant: tenantA, duties: "migration.cutover.manage"})
	if docs.Code != http.StatusOK || strings.Contains(docs.Body.String(), "SI-0") || !strings.Contains(docs.Body.String(), "USMF,SI-9,1,2027-01-02T08:00:00Z,1200,DEPT=SALES,USD,125.500000,") {
		t.Fatalf("documents file: %d %s", docs.Code, docs.Body)
	}
	if r := do(t, h, request{method: "GET", path: journals, tenant: tenantB, duties: "migration.cutover.manage"}); r.Code != http.StatusNotFound {
		t.Fatalf("another tenant must not read the files: %d %s", r.Code, r.Body)
	}
}

// A rollback with nothing posted since T-0 still produces two files, each with its header only.
func TestRollback_WithoutPostingsHasEmptyFiles(t *testing.T) {
	h, _ := dbServer(t)
	plan := decode[planDTO](t, do(t, h, request{method: "POST", path: "/api/v1/cutover/plans/generate", tenant: tenantA, duties: "migration.cutover.manage",
		body: generateBody(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), 36)}))
	base := "/api/v1/cutover/plans/" + plan.PlanID
	p := decode[planDTO](t, rollbackBoth(t, h, base+"/rollback", `{}`, ""))
	if p.Rekeying == nil || *p.Rekeying != (rekeyingDTO{}) {
		t.Fatalf("rekeying %+v", p.Rekeying)
	}
	r := do(t, h, request{method: "GET", path: base + "/rekeying/documents.csv", tenant: tenantA, duties: "migration.cutover.manage"})
	if r.Code != http.StatusOK || r.Body.String() != "entity,document_ref,line,posted_at,account,dimensions,currency,amount,text\n" {
		t.Fatalf("empty documents file: %d %q", r.Code, r.Body)
	}
}
