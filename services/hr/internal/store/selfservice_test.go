package store

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
)

type orgFixture struct {
	s                           *Postgres
	admin                       context.Context
	ceo, mgr, rep, deep, peer   Worker
	mgrCtx, repCtx, outsiderCtx context.Context
}

func userCtx(user string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: user, Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
}

func linkedWorker(t *testing.T, s *Postgres, ctx context.Context, name, user string) Worker {
	t.Helper()
	w, _, err := s.CreateWorker(ctx, WorkerInput{
		Worker:     Worker{PersonnelNumber: "WRK-" + name, WorkerType: "EMPLOYEE", PartyPersonID: ids.New(), LegalName: name, UserID: &user},
		NationalID: "123-45-6789",
	}, "")
	if err != nil {
		t.Fatalf("create worker %s: %v", name, err)
	}
	return w
}

// newOrg builds: ceo <- mgr <- (rep, vacant <- deep) and ceo <- peer. The
// vacant position between mgr and deep has no incumbent.
func newOrg(t *testing.T) orgFixture {
	t.Helper()
	s, admin := newStore(t)
	at(s, day(time.March, 1))
	le, dept := ids.New(), ids.New()
	from := day(time.January, 1)
	f := orgFixture{s: s, admin: admin}
	users := map[string]string{"ceo": ids.New(), "mgr": ids.New(), "rep": ids.New(), "deep": ids.New(), "peer": ids.New()}
	f.ceo, f.mgr, f.rep = linkedWorker(t, s, admin, "ceo", users["ceo"]), linkedWorker(t, s, admin, "mgr", users["mgr"]), linkedWorker(t, s, admin, "rep", users["rep"])
	f.deep, f.peer = linkedWorker(t, s, admin, "deep", users["deep"]), linkedWorker(t, s, admin, "peer", users["peer"])

	top := mustPosition(t, s, admin, "P-CEO", dept, le, "1.00", false, from, nil)
	mgrPos := mustPosition(t, s, admin, "P-MGR", dept, le, "1.00", false, from, &top.ID)
	vacant := mustPosition(t, s, admin, "P-VAC", dept, le, "1.00", false, from, &mgrPos.ID)
	repPos := mustPosition(t, s, admin, "P-REP", dept, le, "1.00", false, from, &mgrPos.ID)
	deepPos := mustPosition(t, s, admin, "P-DEEP", dept, le, "1.00", false, from, &vacant.ID)
	peerPos := mustPosition(t, s, admin, "P-PEER", dept, le, "1.00", false, from, &top.ID)
	for _, seat := range []struct {
		pos Position
		w   Worker
	}{{top, f.ceo}, {mgrPos, f.mgr}, {repPos, f.rep}, {deepPos, f.deep}, {peerPos, f.peer}} {
		e := mustEmployment(t, s, admin, seat.w.ID, le, "PRIMARY", from)
		mustAssignment(t, s, admin, seat.pos.ID, seat.w.ID, e.ID, "1.00", from, nil)
	}
	f.mgrCtx, f.repCtx, f.outsiderCtx = userCtx(users["mgr"]), userCtx(users["rep"]), userCtx(ids.New())
	return f
}

func workerIDs(ws []Worker) map[string]bool {
	out := map[string]bool{}
	for _, w := range ws {
		out[w.ID] = true
	}
	return out
}

func TestTeamIsDirectReportsSkippingVacancies(t *testing.T) {
	f := newOrg(t)
	on := day(time.March, 10)

	mgrTeam, err := f.s.Team(f.mgrCtx, f.mgr.ID, on, "", 10)
	if got := workerIDs(mgrTeam.Items); err != nil || len(got) != 2 || !got[f.rep.ID] || !got[f.deep.ID] {
		t.Fatalf("manager team = %+v err=%v, want rep and deep (vacancy skipped)", mgrTeam.Items, err)
	}
	ceoTeam, err := f.s.Team(f.mgrCtx, f.ceo.ID, on, "", 10)
	if got := workerIDs(ceoTeam.Items); err != nil || len(got) != 2 || !got[f.mgr.ID] || !got[f.peer.ID] {
		t.Fatalf("ceo team = %+v err=%v, want mgr and peer only", ceoTeam.Items, err)
	}
	repTeam, err := f.s.Team(f.repCtx, f.rep.ID, on, "", 10)
	if err != nil || len(repTeam.Items) != 0 {
		t.Fatalf("rep team = %+v err=%v, want empty", repTeam.Items, err)
	}
	before, err := f.s.Team(f.mgrCtx, f.mgr.ID, day(time.December, 31).AddDate(-2, 0, 0), "", 10)
	if err != nil || len(before.Items) != 0 {
		t.Fatalf("team before any assignment = %+v err=%v, want empty", before.Items, err)
	}

	first, err := f.s.Team(f.mgrCtx, f.mgr.ID, on, "", 1)
	if err != nil || len(first.Items) != 1 || first.NextAfter == "" {
		t.Fatalf("first page = %+v err=%v, want one row and a cursor", first, err)
	}
	second, err := f.s.Team(f.mgrCtx, f.mgr.ID, on, first.NextAfter, 1)
	if err != nil || len(second.Items) != 1 || second.Items[0].ID == first.Items[0].ID || second.NextAfter != "" {
		t.Fatalf("second page = %+v err=%v", second, err)
	}
}

func TestIsDirectReportDeniesEveryoneElse(t *testing.T) {
	f := newOrg(t)
	on := day(time.March, 10)
	cases := []struct {
		name    string
		manager Worker
		target  string
		want    bool
	}{
		{"direct report", f.mgr, f.rep.ID, true},
		{"report below a vacancy", f.mgr, f.deep.ID, true},
		{"peer", f.mgr, f.peer.ID, false},
		{"own manager", f.mgr, f.ceo.ID, false},
		{"self", f.mgr, f.mgr.ID, false},
		{"grandchild of ceo", f.ceo, f.rep.ID, false},
		{"unknown id", f.mgr, ids.New(), false},
	}
	for _, c := range cases {
		got, err := f.s.IsDirectReport(f.mgrCtx, c.manager.ID, c.target, on)
		if err != nil || got != c.want {
			t.Errorf("%s: IsDirectReport = %v err=%v, want %v", c.name, got, err, c.want)
		}
	}
}

func TestSelfWorkerIDComesFromTheSignIn(t *testing.T) {
	f := newOrg(t)
	got, err := f.s.SelfWorkerID(f.repCtx)
	if err != nil || got != f.rep.ID {
		t.Fatalf("rep resolves to %q err=%v, want %s", got, err, f.rep.ID)
	}
	_, err = f.s.SelfWorkerID(f.outsiderCtx)
	requireCode(t, err, CodeNoLinkedWorker)
	_, err = f.s.SelfWorkerID(userCtx(""))
	requireCode(t, err, CodeNoLinkedWorker)

	shared := ids.New()
	linkedWorker(t, f.s, f.admin, "twin-a", shared)
	linkedWorker(t, f.s, f.admin, "twin-b", shared)
	_, err = f.s.SelfWorkerID(userCtx(shared))
	requireCode(t, err, CodeAmbiguousWorker)
}

func TestTeamAbsenceRequestsAreTheTeamsAndTheManagersOnly(t *testing.T) {
	f := newOrg(t)
	p := usPTO15("US-PTO-TEAM")
	p.NegativeBalanceAllowed = true
	plan := mustPlan(t, f.s, f.admin, p)
	mine, _ := request(t, f.s, f.admin, f.mgr.ID, plan.ID, day(time.March, 2), day(time.March, 2), "1.00")
	rep, _ := request(t, f.s, f.admin, f.rep.ID, plan.ID, day(time.March, 3), day(time.March, 3), "1.00")
	deep, _ := request(t, f.s, f.admin, f.deep.ID, plan.ID, day(time.March, 4), day(time.March, 4), "1.00")
	peer, _ := request(t, f.s, f.admin, f.peer.ID, plan.ID, day(time.March, 5), day(time.March, 5), decimal.NewFromInt(1).String())
	ceo, _ := request(t, f.s, f.admin, f.ceo.ID, plan.ID, day(time.March, 6), day(time.March, 6), "1.00")

	page, err := f.s.TeamAbsenceRequests(f.mgrCtx, f.mgr.ID, RequestFilter{}, "", 10)
	got := map[string]bool{}
	for _, r := range page.Items {
		got[r.ID] = true
	}
	if err != nil || len(got) != 3 || !got[mine.ID] || !got[rep.ID] || !got[deep.ID] || got[peer.ID] || got[ceo.ID] {
		t.Fatalf("team requests = %+v err=%v, want the manager's own plus rep and deep", page.Items, err)
	}

	narrowed, err := f.s.TeamAbsenceRequests(f.mgrCtx, f.mgr.ID, RequestFilter{WorkerID: f.peer.ID}, "", 10)
	if err != nil || len(narrowed.Items) != 0 {
		t.Fatalf("filtering to a non-report returned %+v err=%v, want nothing", narrowed.Items, err)
	}
	paged, err := f.s.TeamAbsenceRequests(f.mgrCtx, f.mgr.ID, RequestFilter{}, "", 2)
	if err != nil || len(paged.Items) != 2 || paged.NextAfter == "" {
		t.Fatalf("first page = %+v err=%v", paged, err)
	}
	rest, err := f.s.TeamAbsenceRequests(f.mgrCtx, f.mgr.ID, RequestFilter{}, paged.NextAfter, 2)
	if err != nil || len(rest.Items) != 1 {
		t.Fatalf("second page = %+v err=%v", rest.Items, err)
	}
	_, err = f.s.TeamAbsenceRequests(f.mgrCtx, f.mgr.ID, RequestFilter{}, "garbage", 2)
	requireCode(t, err, "COM-0001")

	owner, err := f.s.AbsenceRequestWorker(f.mgrCtx, peer.ID)
	if err != nil || owner != f.peer.ID {
		t.Fatalf("request owner = %q err=%v, want %s", owner, err, f.peer.ID)
	}
	_, err = f.s.AbsenceRequestWorker(f.mgrCtx, ids.New())
	requireCode(t, err, CodeAbsenceRequestNotFound)
}
