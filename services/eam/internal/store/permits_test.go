package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/eam/internal/domain"
	eamevents "github.com/BenjaminSRussell/passage/services/eam/internal/events"
	"github.com/BenjaminSRussell/passage/services/eam/migrations"
)

// as returns the fixture context acting as user, for the separation-of-duty checks.
func (f fixture) as(user string) context.Context {
	p, _ := tenant.FromContext(f.ctx)
	p.UserID = user
	return tenant.With(f.ctx, p)
}

// permitWorkOrder sets up permit type code (an isolation type when iso), makes
// job type REPAIR require it, and opens a RELEASED REPAIR work order on the
// fixture asset. It returns the work order id and version.
func (f fixture) permitWorkOrder(t *testing.T, code string, iso bool, key string) (string, int) {
	t.Helper()
	if _, err := f.s.UpsertPermitType(f.ctx, PermitType{Code: code, Name: code, ApproverRole: "Safety officer",
		MaxValidityHours: 12, RequiresIsolation: iso}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SetJobPermits(f.ctx, "REPAIR", []string{code}); err != nil {
		t.Fatal(err)
	}
	wo, err := f.s.CreateWorkOrder(f.ctx, WorkOrder{AssetID: f.asset, Type: "PREVENTIVE", Currency: "USD", JobType: "REPAIR"}, key)
	if err != nil {
		t.Fatal(err)
	}
	return wo.ID, f.advance(t, wo.ID, wo.Version, domain.StatusScheduled, domain.StatusReleased)
}

func (f fixture) isolationPoints(t *testing.T, tags ...string) []IsolationPoint {
	t.Helper()
	out := make([]IsolationPoint, 0, len(tags))
	for _, tag := range tags {
		ip, err := f.s.CreateIsolationPoint(f.ctx, f.asset, IsolationPoint{EnergyType: "ELECTRICAL", LocationText: "MCC-1",
			Method: "breaker padlock", VerificationStep: "try start", TagCode: tag}, "iso-"+tag)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, ip)
	}
	return out
}

// lock applies and verifies user's personal lock on every point.
func (f fixture) lock(t *testing.T, permitID, user string, points []IsolationPoint) {
	t.Helper()
	for _, ip := range points {
		for _, a := range []domain.LockAction{domain.LockApply, domain.LockVerify} {
			if _, err := f.s.RecordLock(f.as(user), permitID, a, ip.ID, user, ""); err != nil {
				t.Fatalf("%s %s by %s: %v", a, ip.TagCode, user, err)
			}
		}
	}
}

// TestPermit_AC4411 walks one ELECTRICAL_ISOLATION permit on an asset with 3
// isolation points: separation of duties on approval, the lock gap at issue
// and at IN_PROGRESS (EAM-PMT-002), personal locks with replay and duplicate
// refusal, the issued start, and the close guards (EAM-PMT-003, EAM-PMT-008).
func TestPermit_AC4411(t *testing.T) {
	f := setup(t)
	requester, approver := f.as("planner-1"), f.as("issuer-1")
	woID, v := f.permitWorkOrder(t, "ELECTRICAL_ISOLATION", true, "wo-permit-1")

	// No permit at all: EAM-PMT-001.
	if _, err := f.s.TransitionWorkOrder(f.ctx, woID, Transition{To: domain.StatusInProgress, Version: v}, ""); problemCode(err) != domain.CodePermitMissing {
		t.Fatalf("start without a permit: %v, want %s", err, domain.CodePermitMissing)
	}

	permit, err := f.s.RequestPermit(requester, PermitRequest{WorkOrderID: woID, PermitTypeCode: "ELECTRICAL_ISOLATION",
		ValidFrom: clock.Add(-time.Hour), ValidTo: clock.Add(4 * time.Hour)}, "permit-1")
	if err != nil {
		t.Fatal(err)
	}
	if permit.Status != "REQUESTED" || permit.RequestedBy != "planner-1" || permit.Number == "" {
		t.Fatalf("requested permit = %+v", permit)
	}
	if again, err := f.s.RequestPermit(requester, PermitRequest{WorkOrderID: woID, PermitTypeCode: "ELECTRICAL_ISOLATION",
		ValidFrom: clock, ValidTo: clock.Add(time.Hour)}, "permit-1"); err != nil || again.ID != permit.ID {
		t.Fatalf("replayed request = %+v %v, want %s", again, err, permit.ID)
	}
	if _, err := f.s.RequestPermit(requester, PermitRequest{WorkOrderID: woID, PermitTypeCode: "ELECTRICAL_ISOLATION",
		ValidFrom: clock, ValidTo: clock.Add(time.Hour)}, "permit-2"); problemCode(err) != CodeDuplicate {
		t.Fatalf("second permit of the same type: %v, want %s", err, CodeDuplicate)
	}

	if _, err := f.s.TransitionPermit(requester, permit.ID, domain.PermitApproved, permit.Version, "p-a0"); problemCode(err) != domain.CodeSelfApproval {
		t.Fatalf("self-approval: %v, want %s", err, domain.CodeSelfApproval)
	}
	approved, err := f.s.TransitionPermit(approver, permit.ID, domain.PermitApproved, permit.Version, "p-a1")
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != "APPROVED" || approved.ApprovedBy == nil || *approved.ApprovedBy != "issuer-1" || approved.Version != permit.Version+1 {
		t.Fatalf("approved permit = %+v", approved)
	}
	// The same key replays without a second transition; a stale version is refused.
	if replay, err := f.s.TransitionPermit(approver, permit.ID, domain.PermitApproved, permit.Version, "p-a1"); err != nil || replay.Version != approved.Version {
		t.Fatalf("replayed approval = %+v %v", replay, err)
	}
	if _, err := f.s.TransitionPermit(approver, permit.ID, domain.PermitIssued, permit.Version, "p-stale"); problemCode(err) != CodeVersion {
		t.Fatalf("stale version: %v, want %s", err, CodeVersion)
	}

	// Issuing an isolation permit on an asset without isolation points fails closed.
	if _, err := f.s.TransitionPermit(approver, permit.ID, domain.PermitIssued, approved.Version, ""); problemCode(err) != domain.CodeIsolationOpen {
		t.Fatalf("issue with no isolation points: %v, want %s", err, domain.CodeIsolationOpen)
	}

	points := f.isolationPoints(t, "ISO-1", "ISO-2", "ISO-3")
	const tech = "tech-1"
	first, err := f.s.RecordLock(f.as(tech), permit.ID, domain.LockApply, points[0].ID, tech, "lock-1")
	if err != nil || first.State != "APPLIED" || first.LockNumber == "" || first.TechnicianID != tech {
		t.Fatalf("applied lock = %+v %v", first, err)
	}
	if replay, err := f.s.RecordLock(f.as(tech), permit.ID, domain.LockApply, points[0].ID, tech, "lock-1"); err != nil || replay.ID != first.ID {
		t.Fatalf("replayed apply = %+v %v, want %s", replay, err, first.ID)
	}
	if _, err := f.s.RecordLock(f.as(tech), permit.ID, domain.LockApply, points[0].ID, tech, "lock-dup"); problemCode(err) != domain.CodeLockHeld {
		t.Fatalf("second lock by the same technician: %v, want %s", err, domain.CodeLockHeld)
	}
	if _, err := f.s.RecordLock(f.as(tech), permit.ID, domain.LockVerify, points[1].ID, tech, ""); problemCode(err) != domain.CodePermitState {
		t.Fatalf("verify without a lock: %v, want %s", err, domain.CodePermitState)
	}
	f.lock(t, permit.ID, tech, points[1:2])
	if _, err := f.s.RecordLock(f.as(tech), permit.ID, domain.LockVerify, points[0].ID, tech, ""); err != nil {
		t.Fatal(err)
	}

	// AC-44.11: 2 verified locks out of 3 points.
	if _, err := f.s.TransitionWorkOrder(f.ctx, woID, Transition{To: domain.StatusInProgress, Version: v}, ""); problemCode(err) != domain.CodeIsolationOpen {
		t.Fatalf("start with 2 verified locks: %v, want %s", err, domain.CodeIsolationOpen)
	}
	if _, err := f.s.TransitionPermit(approver, permit.ID, domain.PermitIssued, approved.Version, ""); problemCode(err) != domain.CodeIsolationOpen {
		t.Fatalf("issue with 2 verified locks: %v, want %s", err, domain.CodeIsolationOpen)
	}
	f.lock(t, permit.ID, tech, points[2:])

	issued, err := f.s.TransitionPermit(approver, permit.ID, domain.PermitIssued, approved.Version, "p-i1")
	if err != nil {
		t.Fatal(err)
	}
	if issued.Status != "ISSUED" || issued.IssuedBy == nil || issued.LocksOpen != 3 {
		t.Fatalf("issued permit = %+v", issued)
	}
	started, err := f.s.TransitionWorkOrder(f.ctx, woID, Transition{To: domain.StatusInProgress, Version: v}, "")
	if err != nil {
		t.Fatalf("start with an issued permit and 3 verified locks: %v", err)
	}

	// Close: refused with locks applied, then while work is in progress.
	if _, err := f.s.TransitionPermit(approver, permit.ID, domain.PermitClosed, issued.Version, ""); problemCode(err) != domain.CodeLocksOpen {
		t.Fatalf("close with locks: %v, want %s", err, domain.CodeLocksOpen)
	}
	for i, ip := range points {
		if _, err := f.s.RecordLock(f.as(tech), permit.ID, domain.LockRemove, ip.ID, tech, ""); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if _, err := f.s.TransitionPermit(approver, permit.ID, domain.PermitClosed, issued.Version, ""); problemCode(err) != domain.CodeLocksOpen {
				t.Fatalf("close with 2 locks still applied: %v, want %s", err, domain.CodeLocksOpen)
			}
		}
	}
	if _, err := f.s.TransitionPermit(approver, permit.ID, domain.PermitClosed, issued.Version, ""); problemCode(err) != domain.CodeWorkInProgress {
		t.Fatalf("close during work: %v, want %s", err, domain.CodeWorkInProgress)
	}
	f.advance(t, woID, started.Version, domain.StatusOnHold)
	closed, err := f.s.TransitionPermit(approver, permit.ID, domain.PermitClosed, issued.Version, "")
	if err != nil || closed.Status != "CLOSED" || closed.ClosedBy == nil || closed.LocksOpen != 0 {
		t.Fatalf("close after the work stopped = %+v %v", closed, err)
	}

	detail, err := f.s.GetPermit(f.ctx, permit.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.UnlockedPoints != 3 || len(detail.Locks) != 3 || len(detail.IsolationPoints) != 3 {
		t.Fatalf("detail: unlocked=%d locks=%d points=%d", detail.UnlockedPoints, len(detail.Locks), len(detail.IsolationPoints))
	}
	var path []string
	for _, h := range detail.History {
		path = append(path, h.To)
	}
	if got := len(path); got != 4 || path[0] != "REQUESTED" || path[3] != "CLOSED" {
		t.Fatalf("history = %v, want REQUESTED, APPROVED, ISSUED, CLOSED", path)
	}
	// Each lock action is logged; the replayed apply is not logged twice.
	if n := f.count(t, `select count(*) from loto_lock_action a join loto_lock l on l.tenant_id = a.tenant_id and l.id = a.lock_id
	  where l.permit_id = $1`, permit.ID); n != 9 {
		t.Fatalf("lock actions = %d, want 9 (3 apply, 3 verify, 3 remove)", n)
	}

	// Every transition wrote its event in the same transaction, with the work order.
	var events int
	f.inTenant(t, func(tx pgx.Tx) {
		rows, err := tx.Query(f.ctx, `select payload from outbox where aggregate_id = $1 and type = $2`, permit.ID, eamevents.PermitStatus)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var evt struct {
				Data eamevents.PermitStatusData `json:"data"`
			}
			if err := json.Unmarshal(raw, &evt); err != nil {
				t.Fatal(err)
			}
			if evt.Data.WorkOrderID != woID || evt.Data.PermitID != permit.ID {
				t.Fatalf("event data = %+v", evt.Data)
			}
			events++
		}
	})
	if events != 4 {
		t.Fatalf("permit events = %d, want 4", events)
	}

	// A new permit of the type can be requested once the first is closed.
	if _, err := f.s.RequestPermit(requester, PermitRequest{WorkOrderID: woID, PermitTypeCode: "ELECTRICAL_ISOLATION",
		ValidFrom: clock, ValidTo: clock.Add(time.Hour)}, "permit-3"); err != nil {
		t.Fatalf("permit after close: %v", err)
	}
}

// TestPermit_IssuerIsNotTheOnlyLockHolder covers the 8.44.17 rule "issuer
// cannot be the only lock holder" and the cancel guard on open locks.
func TestPermit_IssuerIsNotTheOnlyLockHolder(t *testing.T) {
	f := setup(t)
	woID, _ := f.permitWorkOrder(t, "ELECTRICAL_ISOLATION", true, "wo-solo")
	points := f.isolationPoints(t, "ISO-1")
	p, err := f.s.RequestPermit(f.as("planner-1"), PermitRequest{WorkOrderID: woID, PermitTypeCode: "ELECTRICAL_ISOLATION",
		ValidFrom: clock, ValidTo: clock.Add(2 * time.Hour)}, "solo")
	if err != nil {
		t.Fatal(err)
	}
	a, err := f.s.TransitionPermit(f.as("issuer-1"), p.ID, domain.PermitApproved, p.Version, "")
	if err != nil {
		t.Fatal(err)
	}
	f.lock(t, p.ID, "issuer-1", points)
	if _, err := f.s.TransitionPermit(f.as("issuer-1"), p.ID, domain.PermitIssued, a.Version, ""); problemCode(err) != domain.CodeSelfApproval {
		t.Fatalf("issuer as the only lock holder: %v, want %s", err, domain.CodeSelfApproval)
	}
	if _, err := f.s.TransitionPermit(f.as("issuer-1"), p.ID, domain.PermitCanceled, a.Version, ""); problemCode(err) != domain.CodeLocksOpen {
		t.Fatalf("cancel with a lock applied: %v, want %s", err, domain.CodeLocksOpen)
	}
	f.lock(t, p.ID, "tech-2", points)
	if out, err := f.s.TransitionPermit(f.as("issuer-1"), p.ID, domain.PermitIssued, a.Version, ""); err != nil || out.Status != "ISSUED" {
		t.Fatalf("issue with another holder = %+v %v", out, err)
	}
}

// TestPermit_ExpiryJobSuspendsAndFlags covers BR-44.11 auto-suspension: an
// ISSUED permit past valid_to moves to SUSPENDED, the IN_PROGRESS job is
// flagged with EAM-PMT-004, a rerun finds nothing, and the job cannot restart
// on the suspended permit.
func TestPermit_ExpiryJobSuspendsAndFlags(t *testing.T) {
	f := setup(t)
	woID, v := f.permitWorkOrder(t, "HOT_WORK", false, "wo-hot")
	p, err := f.s.RequestPermit(f.as("planner-1"), PermitRequest{WorkOrderID: woID, PermitTypeCode: "HOT_WORK",
		ValidFrom: clock.Add(-time.Hour), ValidTo: clock.Add(time.Hour)}, "hot")
	if err != nil {
		t.Fatal(err)
	}
	a, err := f.s.TransitionPermit(f.as("issuer-1"), p.ID, domain.PermitApproved, p.Version, "")
	if err != nil {
		t.Fatal(err)
	}
	issued, err := f.s.TransitionPermit(f.as("issuer-1"), p.ID, domain.PermitIssued, a.Version, "")
	if err != nil {
		t.Fatal(err)
	}
	v = f.advance(t, woID, v, domain.StatusInProgress)

	if got, err := f.s.ExpirePermits(f.ctx); err != nil || len(got) != 0 {
		t.Fatalf("expiry before valid_to = %+v %v, want none", got, err)
	}
	later := NewWithClock(f.pool, func() time.Time { return clock.Add(time.Hour) })
	got, err := later.ExpirePermits(f.ctx)
	if err != nil || len(got) != 1 {
		t.Fatalf("expiry at valid_to = %+v %v, want one", got, err)
	}
	if e := got[0]; e.PermitID != p.ID || e.WorkOrderID != woID || !e.WorkOrderInProgress || e.Code != domain.CodePermitExpired {
		t.Fatalf("expired permit = %+v", e)
	}
	if again, err := later.ExpirePermits(f.ctx); err != nil || len(again) != 0 {
		t.Fatalf("rerun = %+v %v, want none", again, err)
	}
	d, err := later.GetPermit(f.ctx, p.ID)
	if err != nil || d.Permit.Status != "SUSPENDED" || d.Permit.Version != issued.Version+1 {
		t.Fatalf("permit after expiry = %+v %v", d.Permit, err)
	}
	if last := d.History[len(d.History)-1]; last.Actor == nil || *last.Actor != ExpirySystemActor {
		t.Fatalf("suspension actor = %+v", last)
	}
	// Re-issuing an expired permit is refused; the held job cannot restart.
	if _, err := later.TransitionPermit(f.as("issuer-1"), p.ID, domain.PermitIssued, d.Permit.Version, ""); problemCode(err) != domain.CodePermitExpired {
		t.Fatalf("re-issue after expiry: %v, want %s", err, domain.CodePermitExpired)
	}
	v = f.advance(t, woID, v, domain.StatusOnHold)
	if _, err := later.TransitionWorkOrder(f.ctx, woID, Transition{To: domain.StatusInProgress, Version: v}, ""); problemCode(err) != domain.CodePermitMissing {
		t.Fatalf("restart on a suspended permit: %v, want %s", err, domain.CodePermitMissing)
	}
}

// TestPermit_RequestRules checks the validity maximum (BR-44.11) and that a
// closed work order takes no permit.
func TestPermit_RequestRules(t *testing.T) {
	f := setup(t)
	if _, err := f.s.UpsertPermitType(f.ctx, PermitType{Code: "HOT_WORK", Name: "Hot work",
		ApproverRole: "Area authority", MaxValidityHours: 8}); err != nil {
		t.Fatal(err)
	}
	wo, err := f.s.CreateWorkOrder(f.ctx, WorkOrder{AssetID: f.asset, Type: "PREVENTIVE", Currency: "USD"}, "wo-hot-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.RequestPermit(f.as("planner-2"), PermitRequest{WorkOrderID: wo.ID, PermitTypeCode: "HOT_WORK",
		ValidFrom: clock, ValidTo: clock.Add(9 * time.Hour)}, "permit-hot-1"); problemCode(err) != CodeValidation {
		t.Fatalf("window over maximum: %v, want %s", err, CodeValidation)
	}
	if _, err := f.s.RequestPermit(f.as("planner-2"), PermitRequest{WorkOrderID: wo.ID, PermitTypeCode: "NOPE",
		ValidFrom: clock, ValidTo: clock.Add(time.Hour)}, ""); problemCode(err) != CodeNotFound {
		t.Fatalf("unknown permit type: %v, want %s", err, CodeNotFound)
	}
	f.advance(t, wo.ID, wo.Version, domain.StatusCanceled)
	if _, err := f.s.RequestPermit(f.as("planner-2"), PermitRequest{WorkOrderID: wo.ID, PermitTypeCode: "HOT_WORK",
		ValidFrom: clock, ValidTo: clock.Add(time.Hour)}, ""); problemCode(err) != domain.CodePermitState {
		t.Fatalf("permit on a canceled work order: %v, want %s", err, domain.CodePermitState)
	}
	// Setup: unknown permit types cannot be required; requirements read back.
	if _, err := f.s.SetJobPermits(f.ctx, "WELD", []string{"NOPE"}); problemCode(err) != CodeNotFound {
		t.Fatalf("unknown required type: %v, want %s", err, CodeNotFound)
	}
	if _, err := f.s.SetJobPermits(f.ctx, "WELD", []string{"HOT_WORK"}); err != nil {
		t.Fatal(err)
	}
	if jp, err := f.s.GetJobPermits(f.ctx, "WELD"); err != nil || len(jp.PermitTypeCodes) != 1 || jp.PermitTypeCodes[0] != "HOT_WORK" {
		t.Fatalf("job permits = %+v %v", jp, err)
	}
	if _, err := f.s.CreateIsolationPoint(f.ctx, f.asset, IsolationPoint{EnergyType: "ELECTRICAL", LocationText: "MCC",
		Method: "breaker", VerificationStep: "try start", TagCode: "ISO-1"}, "ip-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.CreateIsolationPoint(f.ctx, f.asset, IsolationPoint{EnergyType: "PNEUMATIC", LocationText: "Valve",
		Method: "valve lock", VerificationStep: "bleed", TagCode: "ISO-1"}, "ip-2"); problemCode(err) != CodeDuplicate {
		t.Fatalf("duplicate tag code: %v, want %s", err, CodeDuplicate)
	}
	page, err := f.s.ListPermits(f.ctx, ListOptions{WorkOrderID: wo.ID, Limit: 10})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("register for the work order = %+v %v, want empty", page, err)
	}
}

// TestGrants_AppRolePermitsAndLocks runs the permit and LOTO path as
// passage_app (no BYPASSRLS, migration 0005 grants): numbering from the permit
// and lock sequences, row locks on permits and locks, the expiry job, and the
// append-only audit logs refusing updates and deletes.
func TestGrants_AppRolePermitsAndLocks(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := NewWithClock(app, func() time.Time { return clock })
	ctx := testkit.Ctx(testkit.TenantID(1))
	a, err := s.CreateAsset(ctx, Asset{Name: "Press", AssetType: "PRESS", SiteID: testkit.TenantID(9), Criticality: 3}, "g-asset")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActivateAsset(ctx, a.ID, a.Version); err != nil {
		t.Fatal(err)
	}
	f := fixture{ctx: ctx, pool: app, s: s, t: testkit.TenantID(1), asset: a.ID}
	woID, v := f.permitWorkOrder(t, "ELECTRICAL_ISOLATION", true, "g-wo")
	points := f.isolationPoints(t, "ISO-1")
	p, err := s.RequestPermit(f.as("planner-1"), PermitRequest{WorkOrderID: woID, PermitTypeCode: "ELECTRICAL_ISOLATION",
		ValidFrom: clock.Add(-time.Hour), ValidTo: clock.Add(time.Hour)}, "g-permit")
	if err != nil {
		t.Fatalf("permit number sequence as passage_app: %v", err)
	}
	ap, err := s.TransitionPermit(f.as("issuer-1"), p.ID, domain.PermitApproved, p.Version, "g-approve")
	if err != nil {
		t.Fatalf("permit row lock and update as passage_app: %v", err)
	}
	f.lock(t, p.ID, "tech-1", points)
	issued, err := s.TransitionPermit(f.as("issuer-1"), p.ID, domain.PermitIssued, ap.Version, "g-issue")
	if err != nil {
		t.Fatal(err)
	}
	f.advance(t, woID, v, domain.StatusInProgress)
	later := NewWithClock(app, func() time.Time { return clock.Add(time.Hour) })
	if got, err := later.ExpirePermits(ctx); err != nil || len(got) != 1 {
		t.Fatalf("expiry job as passage_app = %+v %v, want one suspension", got, err)
	}
	if d, err := later.GetPermit(ctx, p.ID); err != nil || d.Permit.Version != issued.Version+1 || len(d.Locks) != 1 {
		t.Fatalf("permit detail as passage_app = %+v %v", d, err)
	}
	for _, stmt := range []string{
		`update permit_transition set actor = actor`,
		`delete from permit_transition`,
		`update loto_lock_action set actor = actor`,
		`delete from loto_lock`,
		`update isolation_point set method = method`,
	} {
		err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, stmt)
			return err
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("%q = %v; want insufficient_privilege 42501", stmt, err)
		}
	}
}
