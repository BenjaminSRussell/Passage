package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
)

func glCurRec(id, account, currency, balance string) string {
	return fmt.Sprintf(`{"legacyId":%q,"account":%q,"dimensions":"","currency":%q,"balance":%q}`, id, account, currency, balance)
}

func padEUR(pairs int) []string {
	var out []string
	for i := 0; i < pairs; i++ {
		out = append(out, fmt.Sprintf(`{"legacyId":"GL-P%da","account":"1100","dimensions":"D%d","currency":"EUR","balance":"5"}`, i, i),
			fmt.Sprintf(`{"legacyId":"GL-P%db","account":"3000","dimensions":"D%d","currency":"EUR","balance":"-5"}`, i, i))
	}
	return out
}

func openingBody(legalEntity, ledger string) string {
	return fmt.Sprintf(`{"legalEntityId":%q,"ledgerId":%q,"postingDate":"2027-01-01"}`, legalEntity, ledger)
}

func startGL(t *testing.T, h http.Handler, extra string, records ...string) runDetailDTO {
	t.Helper()
	rec := do(t, h, request{method: "POST", path: glPath, tenant: tenantA, duties: allDuties, body: glBody("USMF", extra, records...), user: "engineer"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("gl run: %d %s", rec.Code, rec.Body)
	}
	return decode[runDetailDTO](t, rec)
}

func postOpeningAs(t *testing.T, h http.Handler, runID, body, le string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, h, request{method: "POST", path: "/api/v1/migration/runs/" + runID + "/opening-journal", tenant: tenantA, duties: allDuties, body: body, user: "engineer", le: le})
}

func postOpening(t *testing.T, h http.Handler, runID, body string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, h, request{method: "POST", path: "/api/v1/migration/runs/" + runID + "/opening-journal", tenant: tenantA, duties: allDuties, body: body, user: "engineer", le: le1})
}

// FR-49.1, FR-49.2: the balances post as one journal per currency through the ledger; posting again,
// also from a re-run of the same import, books nothing twice.
func TestOpeningJournal_PostsPerCurrencyAndIsIdempotent(t *testing.T) {
	h, admin, _, fake, _ := ledgerServer(t)
	records := append(append([]string{}, cleanGL...), glCurRec("GL-4", "1100", "EUR", "100.50"), glCurRec("GL-5", "3000", "EUR", "-100.50"))
	run := startGL(t, h, "", records...)

	rec := postOpening(t, h, run.RunID, openingBody(le1, "L-US"))
	if rec.Code != http.StatusOK {
		t.Fatalf("post: %d %s", rec.Code, rec.Body)
	}
	out := decode[openingJournalsDTO](t, rec)
	if len(out.Journals) != 2 || out.Journals[0].Currency != "EUR" || out.Journals[1].Currency != "USD" || out.Journals[1].LineCount != 3 ||
		out.Journals[1].Debit != "1250" || out.Journals[1].Credit != "1250" || out.Journals[0].EntryID == "" {
		t.Fatalf("journals %+v", out.Journals)
	}
	entries := fake.Entries()
	if len(entries) != 2 || entries[1].SourceType != "opening_balance" || entries[1].LedgerID != "L-US" || entries[1].PostingDate != "2027-01-01" {
		t.Fatalf("ledger entries %+v", entries)
	}

	again := decode[openingJournalsDTO](t, postOpening(t, h, run.RunID, openingBody(le1, "L-US")))
	if len(fake.Entries()) != 2 || !again.Journals[0].AlreadyPosted || !again.Journals[1].AlreadyPosted || again.Journals[1].EntryID != out.Journals[1].EntryID {
		t.Fatalf("second post %+v, ledger holds %d entries", again.Journals, len(fake.Entries()))
	}
	rerun := startGL(t, h, "", records...)
	if rerun.RunID == run.RunID || rerun.RecordsLoaded != 0 {
		t.Fatalf("re-run %+v", rerun.runDTO)
	}
	if rec := postOpening(t, h, rerun.RunID, openingBody(le1, "L-US")); rec.Code != http.StatusOK || len(fake.Entries()) != 2 {
		t.Fatalf("post from the re-run: %d %s, ledger holds %d entries", rec.Code, rec.Body, len(fake.Entries()))
	}
	if n := count(t, admin, `select count(*) from gl_opening_posting`); n != 2 {
		t.Fatalf("posting rows %d", n)
	}
	list := decode[openingJournalsDTO](t, do(t, h, request{method: "GET", path: "/api/v1/migration/runs/" + run.RunID + "/opening-journal", tenant: tenantA, duties: allDuties, le: le1}))
	if len(list.Journals) != 2 || list.Journals[0].PostedAt == nil {
		t.Fatalf("posted journals %+v", list.Journals)
	}
}

// A currency whose debits differ from its credits refuses the whole post and names the difference;
// the currency that balances is not posted either.
func TestOpeningJournal_UnbalancedRefusedWholeNamingTheDifference(t *testing.T) {
	h, admin, _, fake, _ := ledgerServer(t)
	records := append([]string{glCurRec("GL-1", "1100", "USD", "800.00"), glCurRec("GL-2", "3000", "USD", "-550.00"), glCurRec("GL-3", "9999", "USD", "-250.00")}, padEUR(5)...)
	run := startGL(t, h, `,"quarantineMaxPct":"10"`, records...)
	if run.Status != "LOADED" || run.RecordsQuarantined != 1 {
		t.Fatalf("run %+v", run.runDTO)
	}
	rec := postOpening(t, h, run.RunID, openingBody(le1, "L-US"))
	body := rec.Body.String()
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != CodeOpeningUnbalanced || !strings.Contains(body, "MIG-1540") || !strings.Contains(body, "USD debits 800, credits 550, difference 250") || strings.Contains(body, "EUR") {
		t.Fatalf("unbalanced: %d %s", rec.Code, body)
	}
	if len(fake.Entries()) != 0 || count(t, admin, `select count(*) from gl_opening_posting`) != 0 {
		t.Fatal("an unbalanced opening journal posted something")
	}
}

// The ledger's own checks apply: a closed period refuses the journal, nothing is recorded, and the same
// call posts once the period is open.
func TestOpeningJournal_LedgerRefusalRecordsNothingAndRetries(t *testing.T) {
	h, admin, _, fake, _ := ledgerServer(t)
	run := startGL(t, h, "", cleanGL...)
	fake.RefuseClosedThrough("2027-01-31")
	rec := postOpening(t, h, run.RunID, openingBody(le1, "L-US"))
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != CodeOpeningRejected || !strings.Contains(rec.Body.String(), ledgerclient.CodePeriodClosed) {
		t.Fatalf("closed period: %d %s", rec.Code, rec.Body)
	}
	if count(t, admin, `select count(*) from gl_opening_posting`) != 0 {
		t.Fatal("a refused journal was recorded as posted")
	}
	fake.RefuseClosedThrough("")
	if rec := postOpening(t, h, run.RunID, openingBody(le1, "L-US")); rec.Code != http.StatusOK || len(fake.Entries()) != 1 {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body)
	}
}

// The same balances cannot be posted again to another ledger or date, and the request is checked before
// the ledger is called.
func TestOpeningJournal_RefusalsBeforeAndAfterPosting(t *testing.T) {
	h, _, _, fake, _ := ledgerServer(t)
	run := startGL(t, h, "", cleanGL...)
	cases := []struct {
		name, body string
		status     int
		code       string
	}{
		{"ledger of another legal entity", openingBody(le1, "L-OTHER"), http.StatusUnprocessableEntity, CodeLedgerForeign},
		{"unknown ledger", openingBody(le1, "L-NONE"), http.StatusUnprocessableEntity, CodeLedgerForeign},
		{"legal entity outside the caller's scope", openingBody(le2, "L-OTHER"), http.StatusUnprocessableEntity, CodeInvalid},
		{"no posting date", fmt.Sprintf(`{"legalEntityId":%q,"ledgerId":"L-US"}`, le1), http.StatusUnprocessableEntity, CodeInvalid},
	}
	for _, c := range cases {
		if rec := postOpening(t, h, run.RunID, c.body); rec.Code != c.status || problemCode(t, rec) != c.code {
			t.Fatalf("%s: %d %s", c.name, rec.Code, rec.Body)
		}
	}
	if len(fake.Entries()) != 0 {
		t.Fatal("a refused request reached the ledger")
	}
	if rec := postOpening(t, h, run.RunID, openingBody(le1, "L-US")); rec.Code != http.StatusOK {
		t.Fatalf("post: %d %s", rec.Code, rec.Body)
	}
	other := `{"legalEntityId":"` + le1 + `","ledgerId":"L-US","postingDate":"2027-02-01"}`
	if rec := postOpening(t, h, run.RunID, other); rec.Code != http.StatusConflict || problemCode(t, rec) != CodeTransition || len(fake.Entries()) != 1 {
		t.Fatalf("another date: %d %s", rec.Code, rec.Body)
	}
}

func TestOpeningJournal_OnlyForALoadedGLRun(t *testing.T) {
	h, _, _, _, _ := ledgerServer(t)
	if rec := postOpening(t, h, "00000000-0000-7000-8000-000000000001", openingBody(le1, "L-US")); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown run: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, request{method: "POST", path: "/api/v1/migration/runs/00000000-0000-7000-8000-000000000001/opening-journal", tenant: tenantA,
		duties: "migration.factory.view", body: openingBody(le1, "L-US"), le: le1}); rec.Code != http.StatusForbidden {
		t.Fatalf("view duty only: %d", rec.Code)
	}
}

// Once every currency of the company's balances is posted, the go/no-go no longer holds the gl_balances
// pack as staged in the factory only.
func TestOpeningJournal_PostedBalancesCountForTheGoNoGoChecklist(t *testing.T) {
	h, admin, _, _, _ := ledgerServer(t)
	plan, t0 := readyPlan(t, h, admin)
	run := startGL(t, h, "", cleanGL...)
	if _, err := admin.Exec(context.Background(), `update pack_run set created_at = $2 where run_id = $1`, run.RunID, t0.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	detail := func() string {
		rec := do(t, h, request{method: "GET", path: "/api/v1/cutover/plans/" + plan.PlanID + "/go-no-go", tenant: tenantA, duties: "migration.cutover.manage", user: "viewer", le: le1})
		for _, it := range decode[readinessDTO](t, rec).Items {
			if it.Code == "packs_reconciled" {
				return it.Detail
			}
		}
		t.Fatal("no packs_reconciled item")
		return ""
	}
	if d := detail(); !strings.Contains(d, "gl_balances/USMF is RECONCILED but staged in the factory only") {
		t.Fatalf("before posting: %s", d)
	}
	if rec := postOpening(t, h, run.RunID, openingBody(le1, "L-US")); rec.Code != http.StatusOK {
		t.Fatalf("post: %d %s", rec.Code, rec.Body)
	}
	if d := detail(); strings.Contains(d, "gl_balances/USMF") {
		t.Fatalf("after posting: %s", d)
	}
}

// One company is booked to one legal entity and ledger: the first posting fixes it, a caller who
// cannot see that legal entity is refused all the same and learns nothing of it.
func TestOpeningJournal_CompanyIsPinnedToOneLegalEntity(t *testing.T) {
	h, admin, _, fake, _ := ledgerServer(t)
	run := startGL(t, h, "", cleanGL...)
	if rec := postOpening(t, h, run.RunID, openingBody(le1, "L-US")); rec.Code != http.StatusOK {
		t.Fatalf("A: %d %s", rec.Code, rec.Body)
	}
	rec := postOpeningAs(t, h, run.RunID, openingBody(le2, "L-OTHER"), le2)
	if rec.Code != http.StatusConflict || problemCode(t, rec) != CodeOpeningTarget || strings.Contains(rec.Body.String(), le1) || strings.Contains(rec.Body.String(), "L-US") {
		t.Fatalf("B: %d %s", rec.Code, rec.Body)
	}
	if rec := postOpening(t, h, run.RunID, openingBody(le1, "L-OTHER")); rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusConflict {
		t.Fatalf("A on another ledger: %d %s", rec.Code, rec.Body)
	}
	if len(fake.Entries()) != 1 || count(t, admin, `select count(*) from gl_opening_posting`) != 1 {
		t.Fatal("the company was booked twice")
	}
}

func TestOpeningJournal_ConcurrentFirstPostingsEndWithOneTarget(t *testing.T) {
	h, admin, _, fake, _ := ledgerServer(t)
	run := startGL(t, h, "", cleanGL...)
	codes := make(chan int, 2)
	for _, c := range [][2]string{{le1, "L-US"}, {le2, "L-OTHER"}} {
		go func() { codes <- postOpeningAs(t, h, run.RunID, openingBody(c[0], c[1]), c[0]).Code }()
	}
	a, b := <-codes, <-codes
	if !(a == http.StatusOK && b == http.StatusConflict || a == http.StatusConflict && b == http.StatusOK) {
		t.Fatalf("outcomes %d %d", a, b)
	}
	if len(fake.Entries()) != 1 || count(t, admin, `select count(*) from gl_opening_target`) != 1 || count(t, admin, `select count(*) from gl_opening_posting`) != 1 {
		t.Fatalf("entries %d", len(fake.Entries()))
	}
}

// The checklist counts the pack as posted only for a posting that matches the balances held now.
func TestOpeningJournal_StalePostingDoesNotCountForTheChecklist(t *testing.T) {
	h, admin, _, _, _ := ledgerServer(t)
	plan, t0 := readyPlan(t, h, admin)
	run := startGL(t, h, "", cleanGL...)
	if _, err := admin.Exec(context.Background(), `update pack_run set created_at = $2 where run_id = $1`, run.RunID, t0.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	staged := func() bool {
		rec := do(t, h, request{method: "GET", path: "/api/v1/cutover/plans/" + plan.PlanID + "/go-no-go", tenant: tenantA, duties: "migration.cutover.manage", user: "viewer", le: le1})
		return strings.Contains(rec.Body.String(), "gl_balances/USMF is RECONCILED but staged")
	}
	if rec := postOpening(t, h, run.RunID, openingBody(le1, "L-US")); rec.Code != http.StatusOK || staged() {
		t.Fatalf("matching posting: %d staged=%v", rec.Code, staged())
	}
	if _, err := admin.Exec(context.Background(), `update gl_opening_balance set balance = balance + 1 where account = '1100' and dimensions = 'USMF-100'`); err != nil {
		t.Fatal(err)
	}
	if !staged() {
		t.Fatal("a posting that no longer matches the balances counted as posted")
	}
	if _, err := admin.Exec(context.Background(), `update gl_opening_balance set balance = balance - 1 where account = '1100' and dimensions = 'USMF-100'; delete from gl_opening_target`); err != nil {
		t.Fatal(err)
	}
	if !staged() {
		t.Fatal("a posting without a pinned target counted as posted")
	}
}

// A lost answer leaves nothing recorded; the retry reaches the same ledger entry and posts exactly once.
func TestOpeningJournal_LostLedgerAnswerThenRetryPostsOnce(t *testing.T) {
	h, admin, _, fake, _ := ledgerServer(t)
	run := startGL(t, h, "", cleanGL...)
	fake.LoseAnswers(&ledgerclient.Error{Op: "post", Status: http.StatusBadGateway})
	if rec := postOpening(t, h, run.RunID, openingBody(le1, "L-US")); rec.Code != http.StatusBadGateway || problemCode(t, rec) != CodeLedgerUnavailable {
		t.Fatalf("lost answer: %d %s", rec.Code, rec.Body)
	}
	if count(t, admin, `select count(*) from gl_opening_posting`) != 0 {
		t.Fatal("a lost answer was recorded as posted")
	}
	// The journal may have been booked, so the company stays pinned to its target.
	if n := count(t, admin, `select count(*) from gl_opening_target`); n != 1 {
		t.Fatalf("pins after a lost answer: %d", n)
	}
	if rec := postOpeningAs(t, h, run.RunID, openingBody(le2, "L-OTHER"), le2); rec.Code != http.StatusConflict || problemCode(t, rec) != CodeOpeningTarget {
		t.Fatalf("another entity after a lost answer: %d %s", rec.Code, rec.Body)
	}
	fake.LoseAnswers(nil)
	if rec := postOpening(t, h, run.RunID, openingBody(le1, "L-US")); rec.Code != http.StatusOK || len(fake.Entries()) != 1 {
		t.Fatalf("retry: %d %s, ledger holds %d", rec.Code, rec.Body, len(fake.Entries()))
	}
}

// A definite ledger refusal books nothing, so the company is not left pinned to the refused target and
// can be posted to the right legal entity (#549). A pin with postings cannot be released.
func TestOpeningJournal_RefusedFirstPostingReleasesTheTarget(t *testing.T) {
	h, admin, _, fake, _ := ledgerServer(t)
	run := startGL(t, h, "", cleanGL...)
	fake.RefuseClosedThrough("2027-01-31")
	if rec := postOpening(t, h, run.RunID, openingBody(le1, "L-US")); rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != CodeOpeningRejected {
		t.Fatalf("refused: %d %s", rec.Code, rec.Body)
	}
	if n := count(t, admin, `select count(*) from gl_opening_target`); n != 0 {
		t.Fatalf("the refused target is still pinned: %d", n)
	}
	fake.RefuseClosedThrough("")
	if rec := postOpeningAs(t, h, run.RunID, openingBody(le2, "L-OTHER"), le2); rec.Code != http.StatusOK {
		t.Fatalf("the other legal entity after release: %d %s", rec.Code, rec.Body)
	}
	if n := count(t, admin, `select count(*) from gl_opening_target`); n != 1 {
		t.Fatalf("pins after a posting: %d", n)
	}
}

// After one currency posted and the other was refused, the retry skips the posted one, and the refusal names it.
func TestOpeningJournal_RetrySkipsPostedCurrencyAndRefusalNamesIt(t *testing.T) {
	h, _, _, fake, _ := ledgerServer(t)
	run := startGL(t, h, "", append(append([]string{}, cleanGL...), glCurRec("GL-4", "1100", "EUR", "10"), glCurRec("GL-5", "3000", "EUR", "-10"))...)
	fake.FailPostsIn("USD", &ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: ledgerclient.CodePeriodClosed})
	rec := postOpening(t, h, run.RunID, openingBody(le1, "L-US"))
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != CodeOpeningRejected || !strings.Contains(rec.Body.String(), "already posted: EUR") || len(fake.Entries()) != 1 {
		t.Fatalf("refused USD: %d %s", rec.Code, rec.Body)
	}
	fake.FailPostsIn("USD", nil)
	out := decode[openingJournalsDTO](t, postOpening(t, h, run.RunID, openingBody(le1, "L-US")))
	if len(fake.Entries()) != 2 || !out.Journals[0].AlreadyPosted || out.Journals[1].AlreadyPosted {
		t.Fatalf("retry %+v, ledger holds %d", out.Journals, len(fake.Entries()))
	}
}

// A ledger conflict on the key is a 409 in plain words; the unbalanced refusal carries MIG-1540; a posted
// currency that left the balances is a change since posting.
func TestOpeningJournal_ConflictCodeAndPostedCurrencyGone(t *testing.T) {
	h, admin, _, fake, _ := ledgerServer(t)
	run := startGL(t, h, "", cleanGL...)
	fake.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusConflict, Code: ledgerclient.CodeIdempotencyConflict})
	if rec := postOpening(t, h, run.RunID, openingBody(le1, "L-US")); rec.Code != http.StatusConflict || problemCode(t, rec) != CodeOpeningConflict {
		t.Fatalf("conflict: %d %s", rec.Code, rec.Body)
	}
	fake.FailPosts(nil)
	if rec := postOpening(t, h, run.RunID, openingBody(le1, "L-US")); rec.Code != http.StatusOK {
		t.Fatalf("post: %d %s", rec.Code, rec.Body)
	}
	if _, err := admin.Exec(context.Background(), `update gl_opening_balance set currency = 'EUR'`); err != nil {
		t.Fatal(err)
	}
	if rec := postOpening(t, h, run.RunID, openingBody(le1, "L-US")); rec.Code != http.StatusConflict || problemCode(t, rec) != CodeTransition {
		t.Fatalf("currency gone: %d %s", rec.Code, rec.Body)
	}
}
