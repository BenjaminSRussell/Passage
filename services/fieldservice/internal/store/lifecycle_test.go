package store

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sort"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fieldservice/migrations"
)

// lifecycle is a migrated store with the BR-45.2 example profile (Mon-Fri
// 08:00-18:00 Chicago, no holidays; priority 2: response 60, resolution 480)
// and no agreement, so cases use the profile as their fallback.
type lifecycle struct {
	s            *Postgres
	ctx          context.Context
	customer, le string
	profileID    string
	loc          *time.Location
}

func newLifecycle(t *testing.T, tenantN int) lifecycle {
	t.Helper()
	admin, pool := testkit.DBWithApp(t)
	ctx := principalCtx(testkit.TenantID(tenantN))
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	l := lifecycle{s: New(pool), ctx: ctx, customer: ids.New(), le: ids.New(), loc: loc}
	prof, _, err := l.s.CreateSLAProfile(ctx, SLAProfile{Code: "GOLD", Name: "Gold", TimeZone: "America/Chicago", OpenMinute: 480, CloseMinute: 1080,
		Targets: []domain.Target{{Priority: 2, ResponseMinutes: 60, ResolutionMinutes: 480}}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	l.profileID = prof.ID
	return l
}

func (l lifecycle) at(day, hour, minute int) time.Time {
	return time.Date(2026, 10, day, hour, minute, 0, 0, l.loc)
}

func (l lifecycle) open(t *testing.T, opened time.Time) Case {
	t.Helper()
	c, _, err := l.s.OpenCase(l.ctx, OpenCaseInput{CustomerPartyID: l.customer, LegalEntityID: l.le, Channel: "PHONE", Priority: 2,
		OpenedAt: opened, FallbackSLAProfileID: &l.profileID}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (l lifecycle) move(t *testing.T, c Case, to domain.CaseStatus, now time.Time) Case {
	t.Helper()
	next, _, err := l.s.TransitionCase(l.ctx, c.ID, c.Version, to, now, "", "")
	if err != nil {
		t.Fatalf("%s -> %s: %v", c.Status, to, err)
	}
	return next
}

// eventTypes lists the outbox event types written for one aggregate, sorted.
func (l lifecycle) eventTypes(t *testing.T, aggregateID string) []string {
	t.Helper()
	var out []string
	err := dbx.WithTenantTx(l.ctx, l.s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(l.ctx, `select type from outbox where aggregate_id = $1`, aggregateID)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// AC-45.3 against Postgres: the monitor warns once at 75 % of the resolution
// target and breaches once past due, each in the outbox with its case update;
// a breached clock is never selected again and stays BREACHED when resolved.
func TestStore_SLAMonitorWarnsOnceThenBreachesOnce(t *testing.T) {
	l := newLifecycle(t, 2)
	c := l.open(t, l.at(23, 16, 0))
	c = l.move(t, c, domain.CaseInProgress, l.at(23, 16, 10)) // answered within the hour

	scan := func(now time.Time) ScanResult {
		t.Helper()
		res, err := l.s.ScanSLA(l.ctx, now, 100)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	// 360 of 480 business minutes after Fri 16:00 is Mon 12:00.
	if res := scan(l.at(26, 11, 59)); res != (ScanResult{}) {
		t.Fatalf("before 75 %%: %+v, want nothing selected", res)
	}
	if res := scan(l.at(26, 12, 30)); res != (ScanResult{Scanned: 1, Warnings: 1}) {
		t.Fatalf("at 75 %%: %+v, want one warning", res)
	}
	if res := scan(l.at(26, 12, 45)); res != (ScanResult{}) {
		t.Fatalf("after the warning: %+v, want nothing selected", res)
	}
	if res := scan(l.at(26, 14, 30)); res != (ScanResult{Scanned: 1, Breaches: 1}) {
		t.Fatalf("past due Mon 14:00: %+v, want one breach", res)
	}
	if res := scan(l.at(26, 15, 0)); res != (ScanResult{}) {
		t.Fatalf("after the breach: %+v, want nothing selected", res)
	}
	want := []string{"dev.passage.service.case.opened.v1", "dev.passage.service.sla.breached.v1", "dev.passage.service.sla.warning.v1"}
	if got := l.eventTypes(t, c.ID); !slices.Equal(got, want) {
		t.Fatalf("outbox %v, want %v", got, want)
	}

	got, err := l.s.GetCase(l.ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SLAState != domain.SLABreached || got.Version != c.Version+2 {
		t.Fatalf("after scans: state %s version %d, want BREACHED at version %d", got.SLAState, got.Version, c.Version+2)
	}
	resolved := l.move(t, got, domain.CaseResolved, l.at(26, 15, 5))
	if resolved.SLAState != domain.SLABreached || resolved.ResolvedAt == nil {
		t.Fatalf("resolved late: state %s resolvedAt %v, want BREACHED and set", resolved.SLAState, resolved.ResolvedAt)
	}
	if got := l.eventTypes(t, c.ID); !slices.Equal(got, want) {
		t.Fatalf("resolving emitted more events: %v", got)
	}
}

// AC-45.2 and BR-45.2 against Postgres: waiting on the customer pauses the
// resolution clock and resuming moves the due by the paused business minutes;
// a late first response emits the response breach; a stale version is refused;
// an Idempotency-Key replays the first answer.
func TestStore_CaseTransitionPausesAndResumes(t *testing.T) {
	l := newLifecycle(t, 3)
	c := l.open(t, l.at(23, 16, 0))
	if c.Version != 1 || !c.SLAResolutionWarnAt.Equal(l.at(26, 12, 0)) {
		t.Fatalf("opened: version %d warnAt %s", c.Version, c.SLAResolutionWarnAt)
	}

	// First response Mon 08:30 is after the Fri 17:00 response due.
	answered := l.move(t, c, domain.CaseInProgress, l.at(26, 8, 30))
	if answered.RespondedAt == nil || !answered.RespondedAt.Equal(l.at(26, 8, 30)) {
		t.Fatalf("respondedAt %v, want Mon 08:30", answered.RespondedAt)
	}
	waiting, replayed, err := l.s.TransitionCase(l.ctx, c.ID, answered.Version, domain.CaseWaitingCustomer, l.at(26, 10, 0), "pause-1", "hash-pause-1")
	if err != nil || replayed {
		t.Fatalf("pause: %v replayed=%v", err, replayed)
	}
	if waiting.SLAState != domain.SLAPaused || waiting.SLAPausedAt == nil {
		t.Fatalf("waiting: state %s pausedAt %v", waiting.SLAState, waiting.SLAPausedAt)
	}
	again, replayed, err := l.s.TransitionCase(l.ctx, c.ID, answered.Version, domain.CaseWaitingCustomer, l.at(26, 10, 5), "pause-1", "hash-pause-1")
	if err != nil || !replayed || again.Version != waiting.Version {
		t.Fatalf("replay: version %d replayed=%v err=%v, want version %d replayed", again.Version, replayed, err, waiting.Version)
	}
	if _, _, err := l.s.TransitionCase(l.ctx, c.ID, waiting.Version, domain.CaseInProgress, l.at(26, 11, 0), "pause-1", "another body"); problemCode(err) != CodeIdempotencyReuse {
		t.Fatalf("key reused with another request: %v, want %s", err, CodeIdempotencyReuse)
	}
	if _, _, err := l.s.TransitionCase(l.ctx, c.ID, answered.Version, domain.CaseInProgress, l.at(26, 12, 0), "", ""); problemCode(err) != CodeVersionConflict {
		t.Fatalf("stale version: %v, want %s", err, CodeVersionConflict)
	}

	resumed := l.move(t, waiting, domain.CaseInProgress, l.at(26, 12, 0))
	if resumed.SLAState != domain.SLARunning || resumed.SLAPausedMinutes != 120 || resumed.SLAPausedAt != nil {
		t.Fatalf("resumed: state %s paused %d pausedAt %v", resumed.SLAState, resumed.SLAPausedMinutes, resumed.SLAPausedAt)
	}
	// Fri 16:00-18:00 is 120 minutes and Mon 08:00-16:00 is 480: 480 target plus
	// 120 paused lands on Mon 16:00 (see NOT-IMPLEMENTED.md on the PRD's Tue 12:00).
	if want := l.at(26, 16, 0); !resumed.SLAResolutionDue.Equal(want) {
		t.Fatalf("resolution due %s, want %s", resumed.SLAResolutionDue, want)
	}
	if want := l.at(26, 14, 0); !resumed.SLAResolutionWarnAt.Equal(want) {
		t.Fatalf("warning at %s, want %s", resumed.SLAResolutionWarnAt, want)
	}

	met := l.move(t, l.move(t, resumed, domain.CaseResolved, l.at(26, 15, 0)), domain.CaseClosed, l.at(27, 9, 0))
	if met.SLAState != domain.SLAMet || met.Status != string(domain.CaseClosed) {
		t.Fatalf("closed: state %s status %s, want MET and CLOSED", met.SLAState, met.Status)
	}
	_, _, err = l.s.TransitionCase(l.ctx, c.ID, met.Version, domain.CaseInProgress, l.at(27, 10, 0), "", "")
	var p *httpx.Problem
	if !errors.As(err, &p) || p.Code != CodeCaseTransition || p.Status != http.StatusUnprocessableEntity {
		t.Fatalf("reopen a closed case: %v, want 422 %s", err, CodeCaseTransition)
	}

	want := []string{"dev.passage.service.case.opened.v1", "dev.passage.service.sla.breached.v1"}
	if got := l.eventTypes(t, c.ID); !slices.Equal(got, want) {
		t.Fatalf("outbox %v, want the opening and the late-response breach", got)
	}
}

// BR-45.2: a work order of a NEW case reaching DISPATCHED is the response, so
// the monitor raises no response alert for it afterwards.
func TestStore_DispatchStopsTheResponseClock(t *testing.T) {
	l := newLifecycle(t, 4)
	c := l.open(t, time.Now().Add(-10*time.Minute))
	wo, _, err := l.s.CreateWorkOrder(l.ctx, WorkOrder{CaseID: &c.ID, CustomerPartyID: l.customer, LegalEntityID: l.le, TerritoryID: ids.New(),
		EstDurationMinutes: 60, Currency: "USD"}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, to := range []domain.WOStatus{domain.WOScheduled, domain.WODispatched} {
		if wo, err = l.s.TransitionWorkOrder(l.ctx, wo.ID, wo.Version, to); err != nil {
			t.Fatal(err)
		}
	}
	got, err := l.s.GetCase(l.ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RespondedAt == nil || got.Status != string(domain.CaseNew) || got.Version != c.Version+1 {
		t.Fatalf("after dispatch: respondedAt %v status %s version %d", got.RespondedAt, got.Status, got.Version)
	}
	// A week later only the resolution clock alerts.
	if _, err := l.s.ScanSLA(l.ctx, time.Now().Add(7*24*time.Hour), 100); err != nil {
		t.Fatal(err)
	}
	if got, err = l.s.GetCase(l.ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(got.SLAAlerts, "RESPONSE_WARNING") || slices.Contains(got.SLAAlerts, "RESPONSE_BREACH") {
		t.Fatalf("response alerts %v after the dispatch answered the case", got.SLAAlerts)
	}
	if !slices.Contains(got.SLAAlerts, "RESOLUTION_BREACH") {
		t.Fatalf("alerts %v, want the resolution breach", got.SLAAlerts)
	}
}

// BR-45.5 and FR-45.11: completion sets first-time fix, a follow-up clears it on
// the parent, and the KPIs count original orders only.
func TestStore_FollowUpClearsFirstTimeFixAndFeedsKPIs(t *testing.T) {
	f := newFixture(t)
	if _, _, err := f.s.CreateFollowUp(f.ctx, ids.New(), 1, "fu-0", "hash-fu-0"); problemCode(err) != CodeWOUnknown {
		t.Fatalf("follow-up of a missing order: %v", err)
	}
	draft, _, err := f.s.CreateWorkOrder(f.ctx, WorkOrder{CustomerPartyID: f.customer, LegalEntityID: f.le, TerritoryID: f.terr,
		EstDurationMinutes: 60, Currency: "USD"}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.CreateFollowUp(f.ctx, draft.ID, draft.Version, "fu-1", "hash-fu-1"); problemCode(err) != CodeWOFollowUp {
		t.Fatalf("follow-up of an UNSCHEDULED order: %v, want %s", err, CodeWOFollowUp)
	}
	if draft.FirstTimeFix != nil {
		t.Fatalf("an unfinished order has firstTimeFix %v", *draft.FirstTimeFix)
	}

	base := WorkOrder{CustomerPartyID: f.customer, LegalEntityID: f.le, TerritoryID: f.terr, EstDurationMinutes: 90, Currency: "USD", Lines: laborHours(1)}
	fixed := f.completedOrder(t, base)
	notFixed := f.completedOrder(t, base)
	if notFixed.FirstTimeFix == nil || !*notFixed.FirstTimeFix || notFixed.CompletedAt == nil {
		t.Fatalf("completed: firstTimeFix %v completedAt %v, want true and set", notFixed.FirstTimeFix, notFixed.CompletedAt)
	}

	child, replayed, err := f.s.CreateFollowUp(f.ctx, notFixed.ID, notFixed.Version, "fu-2", "hash-fu-2")
	if err != nil || replayed {
		t.Fatalf("follow-up: %v replayed=%v", err, replayed)
	}
	if child.Status != string(domain.WOUnscheduled) || child.FollowUpOf == nil || *child.FollowUpOf != notFixed.ID ||
		child.TerritoryID != f.terr || child.EstDurationMinutes != 90 || len(child.Lines) != 0 {
		t.Fatalf("follow-up %+v, want an empty UNSCHEDULED order linked to %s in the same territory", child, notFixed.ID)
	}
	again, replayed, err := f.s.CreateFollowUp(f.ctx, notFixed.ID, 0, "fu-2", "hash-fu-2")
	if err != nil || !replayed || again.ID != child.ID {
		t.Fatalf("replay: id %s replayed=%v err=%v, want %s", again.ID, replayed, err, child.ID)
	}
	parent, err := f.s.GetWorkOrder(f.ctx, notFixed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if parent.FirstTimeFix == nil || *parent.FirstTimeFix || parent.Version != notFixed.Version+1 {
		t.Fatalf("parent: firstTimeFix %v version %d, want false at version %d", parent.FirstTimeFix, parent.Version, notFixed.Version+1)
	}
	// The follow-up completes too, but it is not a job of its own.
	for _, to := range []domain.WOStatus{domain.WOScheduled, domain.WODispatched, domain.WOTraveling, domain.WOInProgress, domain.WOCompleted} {
		if child, err = f.s.TransitionWorkOrder(f.ctx, child.ID, child.Version, to); err != nil {
			t.Fatal(err)
		}
	}
	// A posted order can still take a follow-up, at its current version.
	posted, _, err := f.s.PostWorkOrder(f.ctx, fixed.ID, fixed.Version, "post-fixed")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.CreateFollowUp(f.ctx, posted.ID, posted.Version+1, "fu-3", "hash-fu-3"); problemCode(err) != CodeVersionConflict {
		t.Fatalf("stale follow-up: %v, want %s", err, CodeVersionConflict)
	}

	now := time.Now()
	k, err := f.s.KPIs(f.ctx, KPIFilter{From: now.Add(-time.Hour), To: now.Add(time.Hour), AsOf: now})
	if err != nil {
		t.Fatal(err)
	}
	if k.WorkOrdersCompleted != 2 || k.FirstTimeFix != 1 || k.FirstTimeFixPct == nil || !k.FirstTimeFixPct.Equal(decimal.NewFromInt(50)) {
		t.Fatalf("kpis completed=%d ftf=%d pct=%v, want 2, 1, 50", k.WorkOrdersCompleted, k.FirstTimeFix, k.FirstTimeFixPct)
	}
	other := ids.New()
	k, err = f.s.KPIs(f.ctx, KPIFilter{From: now.Add(-time.Hour), To: now.Add(time.Hour), AsOf: now, Territory: &other})
	if err != nil {
		t.Fatal(err)
	}
	if k.WorkOrdersCompleted != 0 || k.FirstTimeFixPct != nil {
		t.Fatalf("another territory: completed=%d pct=%v, want 0 and no rate", k.WorkOrdersCompleted, k.FirstTimeFixPct)
	}
}

// FR-45.11: response and resolution attainment over cases opened in the window.
func TestStore_KPIsSLAAttainment(t *testing.T) {
	l := newLifecycle(t, 5)
	// On time: answered at 08:30, resolved at 10:00 (MET).
	onTime := l.open(t, l.at(19, 8, 0))
	l.move(t, l.move(t, onTime, domain.CaseInProgress, l.at(19, 8, 30)), domain.CaseResolved, l.at(19, 10, 0))
	// Late: answered at 10:00 (60-minute target missed), resolved Tue noon (BREACHED).
	late := l.open(t, l.at(19, 8, 0))
	l.move(t, l.move(t, late, domain.CaseInProgress, l.at(19, 10, 0)), domain.CaseResolved, l.at(20, 12, 0))
	// Unanswered and past its response due; resolution still within target at AsOf.
	l.open(t, l.at(21, 8, 0))
	// Outside the window.
	l.open(t, l.at(26, 8, 0))

	k, err := l.s.KPIs(l.ctx, KPIFilter{From: l.at(19, 0, 0), To: l.at(24, 0, 0), AsOf: l.at(21, 9, 30)})
	if err != nil {
		t.Fatal(err)
	}
	if k.CasesOpened != 3 || k.ResponseMet != 1 || k.ResponseMissed != 2 || k.ResolutionMet != 1 || k.ResolutionBreached != 1 {
		t.Fatalf("counts %+v", k)
	}
	if !k.ResponseAttainmentPct.Equal(decimal.RequireFromString("33.33")) || !k.ResolutionAttainmentPct.Equal(decimal.NewFromInt(50)) {
		t.Fatalf("attainment response %v resolution %v, want 33.33 and 50", k.ResponseAttainmentPct, k.ResolutionAttainmentPct)
	}
	// Wall-clock means: response (30 + 120) / 2 = 75; resolution (120 + 1680) / 2 = 900.
	if !k.MeanResponseMinutes.Equal(decimal.NewFromInt(75)) || !k.MeanResolutionMinutes.Equal(decimal.NewFromInt(900)) {
		t.Fatalf("means response %v resolution %v, want 75 and 900", k.MeanResponseMinutes, k.MeanResolutionMinutes)
	}
}

// The monitor runs in the caller's tenant only (RLS).
func TestStore_SLAScanStaysInTenant(t *testing.T) {
	l := newLifecycle(t, 6)
	l.open(t, l.at(19, 8, 0))
	other := principalCtx(testkit.TenantID(7))
	res, err := l.s.ScanSLA(other, l.at(30, 8, 0), 100)
	if err != nil || res.Scanned != 0 {
		t.Fatalf("other tenant scan: %+v %v, want nothing", res, err)
	}
	if res, err = l.s.ScanSLA(l.ctx, l.at(30, 8, 0), 100); err != nil || res.Scanned != 1 || res.Breaches != 2 {
		t.Fatalf("own tenant scan: %+v %v, want one case with response and resolution breaches", res, err)
	}
}
