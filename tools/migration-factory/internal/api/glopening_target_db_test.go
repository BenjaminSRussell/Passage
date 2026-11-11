package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
)

func newPlanID(t *testing.T, h http.Handler) string {
	t.Helper()
	rec := do(t, h, request{method: "POST", path: "/api/v1/cutover/plans/generate", tenant: tenantA, duties: "migration.cutover.manage",
		body: generateBody(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), 36), user: "manager"})
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("plan: %d %s", rec.Code, rec.Body)
	}
	return decode[planDTO](t, rec).PlanID
}

const adminRelease = DutyOpeningTargetRelease + ",tenant.administer"

func mapBody(legalEntity, ledger string) string {
	return `{"legalEntityId":"` + legalEntity + `","ledgerId":"` + ledger + `"}`
}

func putTarget(t *testing.T, h http.Handler, plan, company, body string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, h, request{method: "PUT", path: "/api/v1/cutover/plans/" + plan + "/opening-targets/AX2012/" + company, tenant: tenantA, duties: allDuties, body: body, user: "manager", le: le1 + "," + le2})
}

func releasePin(t *testing.T, h http.Handler, duties, body string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, h, request{method: "POST", path: "/api/v1/migration/opening-targets/AX2012/USMF/release", tenant: tenantA, duties: duties, body: body, user: "admin"})
}

// #549: the cutover plan names the legal entity and ledger of a company's opening journal, so the first
// caller no longer decides it; another target is refused before the ledger is called.
func TestOpeningJournal_MappingFromTheCutoverPlanDecidesTheTarget(t *testing.T) {
	h, admin, _, fake, _ := ledgerServer(t)
	run := startGL(t, h, "", cleanGL...)
	plan := newPlanID(t, h)
	if rec := putTarget(t, h, plan, "USMF", mapBody(le2, "L-OTHER")); rec.Code != http.StatusOK {
		t.Fatalf("map: %d %s", rec.Code, rec.Body)
	}
	rec := postOpening(t, h, run.RunID, openingBody(le1, "L-US"))
	if rec.Code != http.StatusConflict || problemCode(t, rec) != CodeOpeningTarget || strings.Contains(rec.Body.String(), le2) {
		t.Fatalf("first caller on an unmapped target: %d %s", rec.Code, rec.Body)
	}
	if len(fake.Entries()) != 0 || count(t, admin, `select count(*) from gl_opening_target`) != 0 {
		t.Fatal("a refused target reached the ledger or pinned")
	}
	if rec := postOpeningAs(t, h, run.RunID, openingBody(le2, "L-OTHER"), le2); rec.Code != http.StatusOK {
		t.Fatalf("mapped target: %d %s", rec.Code, rec.Body)
	}
	list := do(t, h, request{method: "GET", path: "/api/v1/cutover/plans/" + plan + "/opening-targets", tenant: tenantA, duties: allDuties, user: "manager", le: le1})
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "L-OTHER") {
		t.Fatalf("list: %d %s", list.Code, list.Body)
	}
	if rec := putTarget(t, h, plan, "USMF", mapBody(le1, "L-US")); rec.Code != http.StatusConflict || problemCode(t, rec) != CodeOpeningTarget {
		t.Fatalf("remap after posting: %d %s", rec.Code, rec.Body)
	}
	if rec := putTarget(t, h, plan, "USMF", `{"legalEntityId":"nope","ledgerId":"L"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad entity: %d %s", rec.Code, rec.Body)
	}
	if rec := putTarget(t, h, "00000000-0000-7000-8000-0000000000ff", "USMF", mapBody(le1, "L-US")); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown plan: %d %s", rec.Code, rec.Body)
	}
}

// #549: an administrator releases a pin that holds no posting; the reason and person are kept, and a pin
// with postings stays.
func TestOpeningJournal_AdminReleasesAnUnpostedPinWithAnAuditRecord(t *testing.T) {
	h, admin, _, fake, _ := ledgerServer(t)
	run := startGL(t, h, "", cleanGL...)
	fake.LoseAnswers(&ledgerclient.Error{Op: "post", Status: http.StatusBadGateway})
	if rec := postOpening(t, h, run.RunID, openingBody(le1, "L-US")); rec.Code != http.StatusBadGateway {
		t.Fatalf("lost answer: %d %s", rec.Code, rec.Body)
	}
	fake.LoseAnswers(nil)
	if n := count(t, admin, `select count(*) from gl_opening_target`); n != 1 {
		t.Fatalf("pin after a lost answer: %d", n)
	}
	if rec := releasePin(t, h, DutyOpeningTargetRelease, `{"reason":"no tenant admin"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("without tenant.administer: %d %s", rec.Code, rec.Body)
	}
	if rec := releasePin(t, h, allDuties, `{"reason":"wrong entity"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("without the release duty: %d %s", rec.Code, rec.Body)
	}
	if rec := releasePin(t, h, adminRelease, `{"reason":"  "}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("without a reason: %d %s", rec.Code, rec.Body)
	}
	if rec := releasePin(t, h, adminRelease, `{"reason":"booked to the wrong entity"}`); rec.Code != http.StatusOK {
		t.Fatalf("release: %d %s", rec.Code, rec.Body)
	}
	if n := count(t, admin, `select count(*) from gl_opening_target`); n != 0 {
		t.Fatalf("pin after release: %d", n)
	}
	if n := count(t, admin, `select count(*) from gl_opening_target_release where reason = 'booked to the wrong entity' and released_by = 'admin'`); n != 1 {
		t.Fatalf("audit rows: %d", n)
	}
	if rec := releasePin(t, h, adminRelease, `{"reason":"again"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("release of nothing: %d %s", rec.Code, rec.Body)
	}
	if rec := postOpeningAs(t, h, run.RunID, openingBody(le2, "L-OTHER"), le2); rec.Code != http.StatusOK {
		t.Fatalf("post after release: %d %s", rec.Code, rec.Body)
	}
	if rec := releasePin(t, h, adminRelease, `{"reason":"too late"}`); rec.Code != http.StatusConflict {
		t.Fatalf("release with postings: %d %s", rec.Code, rec.Body)
	}
	if n := count(t, admin, `select count(*) from gl_opening_target`) + count(t, admin, `select count(*) from gl_opening_target_release`); n != 2 {
		t.Fatalf("pin or audit changed by a refused release: %d", n)
	}
}

// blockingLedger holds Post until released, to stand for a posting that is in flight.
type blockingLedger struct {
	*ledgerclient.Fake
	entered chan struct{}
	release chan struct{}
}

func (b *blockingLedger) Post(ctx context.Context, e ledgerclient.Entry) (string, error) {
	b.entered <- struct{}{}
	select {
	case <-b.release:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return b.Fake.Post(ctx, e)
}

// #560 (from #549): the release of a company's pin cannot slip between the ledger booking a journal and the posting row being
// recorded. A posting call holds a lease for that time; the release refuses until it ends, and an expired lease (a crashed
// process) does not block it.
func TestOpeningJournal_ReleaseWaitsForAnInFlightPosting(t *testing.T) {
	_, admin, st, fake, c := ledgerServer(t)
	blocking := &blockingLedger{Fake: fake, entered: make(chan struct{}, 1), release: make(chan struct{})}
	h := New(st).WithLedger(blocking).WithClock(c.now).Routes()
	run := startGL(t, h, "", cleanGL...)

	posted := make(chan int, 1)
	go func() { posted <- postOpening(t, h, run.RunID, openingBody(le1, "L-US")).Code }()
	select {
	case <-blocking.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the posting never reached the ledger")
	}
	if n := count(t, admin, `select count(*) from gl_opening_posting_lease`); n != 1 {
		t.Fatalf("leases while posting: %d", n)
	}
	if n := count(t, admin, `select count(*) from gl_opening_target`); n != 1 {
		t.Fatalf("pins while posting: %d", n)
	}
	rec := releasePin(t, h, adminRelease, `{"reason":"racing the posting"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "being posted") {
		t.Fatalf("release during a posting: %d %s", rec.Code, rec.Body)
	}
	if n := count(t, admin, `select count(*) from gl_opening_target`) + count(t, admin, `select count(*) from gl_opening_target_release`); n != 1 {
		t.Fatalf("a refused release changed the pin or the audit trail: %d", n)
	}
	close(blocking.release)
	if code := <-posted; code != http.StatusOK {
		t.Fatalf("the posting after the refused release: %d", code)
	}
	if n := count(t, admin, `select count(*) from gl_opening_posting_lease`); n != 0 {
		t.Fatalf("the lease stays after the posting: %d", n)
	}
	if n := count(t, admin, `select count(*) from gl_opening_posting`); n != 1 {
		t.Fatalf("postings: %d", n)
	}
	// The pin now holds a posting, so it stays whatever the lease says.
	if rec := releasePin(t, h, adminRelease, `{"reason":"too late"}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "already has opening journals") {
		t.Fatalf("release with postings: %d %s", rec.Code, rec.Body)
	}
}

func TestOpeningJournal_AnExpiredLeaseDoesNotBlockTheRelease(t *testing.T) {
	h, admin, _, fake, _ := ledgerServer(t)
	run := startGL(t, h, "", cleanGL...)
	fake.LoseAnswers(&ledgerclient.Error{Op: "post", Status: http.StatusBadGateway})
	if rec := postOpening(t, h, run.RunID, openingBody(le1, "L-US")); rec.Code != http.StatusBadGateway {
		t.Fatalf("lost answer: %d %s", rec.Code, rec.Body)
	}
	fake.LoseAnswers(nil)
	if n := count(t, admin, `select count(*) from gl_opening_posting_lease`); n != 0 {
		t.Fatalf("a failed posting keeps its lease: %d", n)
	}
	// A process that died mid-posting left a lease behind; once it has expired, the administrator can release.
	if _, err := admin.Exec(context.Background(), `insert into gl_opening_posting_lease (tenant_id, source_system, source_company, holder, taken_by, expires_at)
		select tenant_id, source_system, source_company, gen_random_uuid(), 'ghost', now() + interval '5 minutes' from gl_opening_target`); err != nil {
		t.Fatal(err)
	}
	if rec := releasePin(t, h, adminRelease, `{"reason":"cut off"}`); rec.Code != http.StatusConflict {
		t.Fatalf("release with a live lease: %d %s", rec.Code, rec.Body)
	}
	if _, err := admin.Exec(context.Background(), `update gl_opening_posting_lease set expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if rec := releasePin(t, h, adminRelease, `{"reason":"cut off"}`); rec.Code != http.StatusOK {
		t.Fatalf("release with an expired lease: %d %s", rec.Code, rec.Body)
	}
}
