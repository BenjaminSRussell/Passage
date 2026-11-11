package api

import (
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
