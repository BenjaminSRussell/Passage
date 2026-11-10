package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/mobile/internal/flows"
	"github.com/BenjaminSRussell/passage/services/mobile/internal/gs1"
	"github.com/BenjaminSRussell/passage/services/mobile/migrations"
)

const (
	dutyExecute   = "warehouse.work.execute"
	dutySupervise = "warehouse.work.supervise"
)

func setup(t *testing.T) (*Store, *pgxpool.Pool, context.Context) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	ctx := testkit.Ctx(testkit.TenantID(1), dutyExecute)
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return New(app), admin, ctx
}

func as(ctx context.Context, user string, duties ...string) context.Context {
	p, _ := tenant.FromContext(ctx)
	p.UserID = user
	p.Duties = map[string]struct{}{}
	for _, d := range duties {
		p.Duties[d] = struct{}{}
	}
	return tenant.With(ctx, p)
}

func key(prefix string) Idem { return Idem{Key: prefix + "-" + ids.New(), Hash: prefix} }

func TestEveryTableIsTenantIsolated(t *testing.T) {
	_, admin, _ := setup(t)
	testkit.AssertTenantRLS(t, admin)
}

func TestCrossTenantIsolation(t *testing.T) {
	st, _, _ := setup(t)
	const (
		deviceID  = "d1"
		sessionID = "5e5510a0-0000-4000-8000-000000000001"
		whID      = "5e5510a0-0000-4000-8000-0000000000a1"
	)
	only := func(tenantID, _ string) []any { return []any{tenantID} }
	fresh := func(tenantID, _ string) []any { return []any{tenantID, ids.New()} }
	testkit.CrossTenant(t, st.pool, []testkit.RLSCase{
		testkit.IdempotencyCase(),
		{Table: "mobile_flow", Insert: `insert into mobile_flow (tenant_id, flow_id, code, version, status, entry_step_id, steps) values ($1, $2, 'PICK', 1, 'DRAFT', 'a', '[]')`, Args: fresh},
		{Table: "mobile_menu", Insert: `insert into mobile_menu (tenant_id, menu_id, key, name) values ($1, $2, 'default', 'Default')`, Args: fresh},
		{Table: "mobile_device", Insert: `insert into mobile_device (tenant_id, device_id) values ($1, '` + deviceID + `')`, Args: only},
		{Table: "device_session",
			Before: `insert into mobile_device (tenant_id, device_id) values ($1, 'd2')`,
			Insert: `insert into device_session (tenant_id, session_id, device_id, user_id, warehouse_id, status) values ($1, '` + sessionID + `', 'd2', 'u', '` + whID + `', 'ACTIVE')`,
			Args:   only},
		{Table: "offline_transaction", NoUpdate: true,
			Insert: `insert into offline_transaction (tenant_id, idempotency_key, device_id, user_id, seq, command, payload, payload_hash, captured_at, replay_status)
				values ($1, $2, 'd1', 'u', 1, 'ConfirmWorkLine', '{}', 'h', now(), 'PENDING')`,
			Args: fresh},
	})
}

func TestAppRoleCannotDeleteOfflineLogOrRewriteOutbox(t *testing.T) {
	st, _, ctx := setup(t)
	for _, stmt := range []string{`delete from offline_transaction`, `delete from device_session`, `delete from mobile_flow`, `update outbox set type = type`, `delete from outbox`} {
		err := dbx.WithTenantTx(ctx, st.pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, stmt)
			return err
		})
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != "42501" {
			t.Errorf("%q = %v, want insufficient_privilege", stmt, err)
		}
	}
}

func scanFlowDraft(code string) flows.Draft {
	return flows.Draft{Code: code, EntryStepID: "scan_lp", Steps: []flows.Step{
		{ID: "scan_lp", Type: flows.StepScan, Prompt: "Scan", ExpectedScan: []gs1.ScanClass{gs1.ClassLP}, Next: map[string]*string{"default": nil}},
	}}
}

func TestFlowDraftPublishVersioning(t *testing.T) {
	st, admin, ctx := setup(t)
	d := scanFlowDraft("PICK_X")
	first, replayed, err := st.SaveDraft(ctx, key("d1"), d)
	if err != nil || replayed || first.Version != 1 || first.Status != FlowDraft {
		t.Fatalf("first draft: %+v %v %v", first, replayed, err)
	}
	d.Steps[0].Prompt = "Scan again"
	again, _, err := st.SaveDraft(ctx, key("d2"), d)
	if err != nil || again.FlowID != first.FlowID || again.Steps[0].Prompt != "Scan again" {
		t.Fatalf("a second save must update the open draft: %+v %v", again, err)
	}
	if _, _, err := st.Publish(ctx, key("p0"), first.FlowID, 99); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("wrong If-Match: %v", err)
	}
	pub, _, err := st.Publish(ctx, key("p1"), first.FlowID, 1)
	if err != nil || pub.Status != FlowPublished || pub.PublishedAt == nil {
		t.Fatalf("publish: %+v %v", pub, err)
	}
	next, _, err := st.SaveDraft(ctx, key("d3"), d)
	if err != nil || next.Version != 2 || next.FlowID == first.FlowID {
		t.Fatalf("a draft after publish is version 2: %+v %v", next, err)
	}
	pub2, _, err := st.Publish(ctx, key("p2"), next.FlowID, 2)
	if err != nil || pub2.Version != 2 {
		t.Fatalf("publish v2: %+v %v", pub2, err)
	}
	old, err := st.FlowVersion(ctx, "PICK_X", 1)
	if err != nil || old.Status != FlowRetired {
		t.Fatalf("v1 must be retired but readable for runs pinned to it: %+v %v", old, err)
	}
	latest, err := st.LatestPublished(ctx, "PICK_X")
	if err != nil || latest.Version != 2 {
		t.Fatalf("latest: %+v %v", latest, err)
	}
	for _, stmt := range []string{
		fmt.Sprintf(`update mobile_flow set steps = '[]' where flow_id = '%s'`, pub2.FlowID),
		fmt.Sprintf(`update mobile_flow set status = 'DRAFT' where flow_id = '%s'`, pub2.FlowID),
		fmt.Sprintf(`update mobile_flow set status = 'PUBLISHED' where flow_id = '%s'`, old.FlowID),
	} {
		if _, err := admin.Exec(ctx, stmt); err == nil {
			t.Errorf("a published flow version is immutable, but %q succeeded", stmt)
		}
	}
}

func TestPublishRejectsInvalidGraphAndReplaysKey(t *testing.T) {
	st, _, ctx := setup(t)
	bad := flows.Draft{Code: "BAD_FLOW", EntryStepID: "a", Steps: []flows.Step{
		{ID: "a", Type: flows.StepConfirm, Next: map[string]*string{"default": nil}},
		{ID: "orphan", Type: flows.StepConfirm, Next: map[string]*string{"default": nil}},
	}}
	saved, _, err := st.SaveDraft(ctx, key("bad"), bad)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = st.Publish(ctx, key("pb"), saved.FlowID, 0)
	var ge *GraphError
	if !errors.As(err, &ge) || ge.Issues[0].Code != flows.CodeGraph {
		t.Fatalf("an unreachable step must be refused with MOB-0113: %v", err)
	}
	good := scanFlowDraft("GOOD_FLOW")
	g, _, _ := st.SaveDraft(ctx, key("g"), good)
	idem := key("once")
	a, _, err := st.Publish(ctx, idem, g.FlowID, 0)
	b, replayed, err2 := st.Publish(ctx, idem, g.FlowID, 0)
	if err != nil || err2 != nil || !replayed || a.Version != b.Version {
		t.Fatalf("a repeated publish key must replay: %+v %+v %v %v", a, b, replayed, err2)
	}
	if _, _, err := st.Publish(ctx, Idem{Key: idem.Key, Hash: "other"}, g.FlowID, 0); !errors.Is(err, ErrIdempotencyReuse) {
		t.Fatalf("a key reused for another request: %v", err)
	}
}

func TestInstallCoreIsIdempotentAndEmitsPublishEvents(t *testing.T) {
	st, admin, ctx := setup(t)
	core := flows.CoreFlows()
	first, _, err := st.InstallCore(ctx, key("c1"), core)
	if err != nil || len(first) != len(core) {
		t.Fatalf("install: %d %v", len(first), err)
	}
	second, _, err := st.InstallCore(ctx, key("c2"), core)
	if err != nil || len(second) != len(core) || second[0].Version != 1 || second[0].FlowID != first[0].FlowID {
		t.Fatalf("a second install must keep the published versions: %+v %v", second, err)
	}
	var events int
	if err := admin.QueryRow(ctx, `select count(*) from outbox where type = 'dev.passage.mobile.flow.published.v1'`).Scan(&events); err != nil || events != len(core) {
		t.Fatalf("flow.published events = %d, want %d (%v)", events, len(core), err)
	}
}

func TestAC324PublishIsVisibleOnNextMenuLoad(t *testing.T) {
	st, _, ctx := setup(t)
	wh := ids.New()
	d := scanFlowDraft("RECEIVE_LOT")
	s, _, _ := st.SaveDraft(ctx, key("a"), d)
	if _, _, err := st.Publish(ctx, key("b"), s.FlowID, 0); err != nil {
		t.Fatal(err)
	}
	before, err := st.PublishedFor(ctx, wh, nil)
	if err != nil || len(before) != 1 || before[0].Version != 1 || len(before[0].Steps) != 1 {
		t.Fatalf("menu before: %+v %v", before, err)
	}
	d.Steps = append([]flows.Step{{ID: "scan_lot", Type: flows.StepScan, Prompt: "Scan the lot", ExpectedScan: d.Steps[0].ExpectedScan, Next: map[string]*string{"default": &d.Steps[0].ID}}}, d.Steps...)
	d.EntryStepID = "scan_lot"
	s2, _, err := st.SaveDraft(ctx, key("c"), d)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Publish(ctx, key("d"), s2.FlowID, 0); err != nil {
		t.Fatal(err)
	}
	after, err := st.PublishedFor(ctx, wh, nil)
	if err != nil || len(after) != 1 || after[0].Version != 2 || after[0].EntryStepID != "scan_lot" || len(after[0].Steps) != 2 {
		t.Fatalf("the next menu load must serve v2 with the lot step and no app release: %+v %v", after, err)
	}
}

func TestPublishedForFiltersBySite(t *testing.T) {
	st, _, ctx := setup(t)
	siteA, siteB := ids.New(), ids.New()
	for code, sites := range map[string][]string{"EVERYWHERE": nil, "ONLY_A": {siteA}} {
		d := scanFlowDraft(code)
		d.Warehouses = sites
		s, _, err := st.SaveDraft(ctx, key(code), d)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.Publish(ctx, key("p"+code), s.FlowID, 0); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := st.PublishedFor(ctx, siteA, nil)
	b, _ := st.PublishedFor(ctx, siteB, nil)
	one, _ := st.PublishedFor(ctx, siteA, []string{"ONLY_A"})
	if len(a) != 2 || len(b) != 1 || b[0].Code != "EVERYWHERE" || len(one) != 1 {
		t.Fatalf("site filter: A=%d B=%d codes=%d", len(a), len(b), len(one))
	}
}

func TestMenusResolveByRoleAndSite(t *testing.T) {
	st, _, ctx := setup(t)
	siteA, siteB := ids.New(), ids.New()
	menus := []Menu{
		{Key: "default", Name: "General", FlowCodes: []string{"PICK"}},
		{Key: "default", Name: "Site A", WarehouseID: siteA, FlowCodes: []string{"PICK", "PACK"}},
		{Key: "default", Name: "Supervisor", Role: dutySupervise, FlowCodes: []string{"PICK", "COUNT"}},
	}
	for i, m := range menus {
		if _, _, err := st.UpsertMenu(ctx, key(fmt.Sprint("m", i)), m); err != nil {
			t.Fatal(err)
		}
	}
	worker := as(ctx, "w", dutyExecute)
	boss := as(ctx, "b", dutyExecute, dutySupervise)
	cases := []struct {
		name string
		ctx  context.Context
		site string
		want string
	}{
		{"worker at other site", worker, siteB, "General"},
		{"worker at site A", worker, siteA, "Site A"},
		{"supervisor outranks site", boss, siteA, "Supervisor"},
		{"supervisor elsewhere", boss, siteB, "Supervisor"},
	}
	for _, c := range cases {
		m, err := st.ResolveMenu(c.ctx, "default", c.site)
		if err != nil || m.Name != c.want {
			t.Errorf("%s: %+v %v, want %s", c.name, m, err, c.want)
		}
	}
	if _, err := st.ResolveMenu(worker, "nope", siteA); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown menu: %v", err)
	}
	again, _, err := st.UpsertMenu(ctx, key("m-again"), Menu{Key: "default", Name: "General v2", FlowCodes: []string{"PICK", "MOVE"}})
	if err != nil || again.Version != 2 || again.Name != "General v2" {
		t.Errorf("upsert of the same scope must bump the version: %+v %v", again, err)
	}
	if _, err := st.ResolveMenu(as(ctx, "x"), "default", siteB); err != nil {
		t.Errorf("a user with no duties still gets the general menu: %v", err)
	}
}

func TestSessionLifecycleResumeTakeoverAndForce(t *testing.T) {
	st, _, ctx := setup(t)
	wh := ids.New()
	now := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	idle := 30 * time.Minute
	alice, bob := as(ctx, "alice", dutyExecute), as(ctx, "bob", dutyExecute)
	in := func(at time.Time) StartInput {
		return StartInput{DeviceID: "dev-1", WarehouseID: wh, MenuKey: "default", Now: at, Idle: idle}
	}
	a, resumed, err := st.StartSession(alice, in(now))
	if err != nil || resumed || a.Status != "ACTIVE" {
		t.Fatalf("start: %+v %v %v", a, resumed, err)
	}
	same, resumed, err := st.StartSession(alice, in(now.Add(time.Minute)))
	if err != nil || !resumed || same.SessionID != a.SessionID {
		t.Fatalf("the same user logging in again resumes: %+v %v %v", same, resumed, err)
	}
	if _, _, err := st.StartSession(bob, in(now.Add(10*time.Minute))); !errors.Is(err, ErrSessionActive) {
		t.Fatalf("another user while the session is live: %v", err)
	}
	b, _, err := st.StartSession(bob, in(now.Add(31*time.Minute)))
	if err != nil || b.SessionID == a.SessionID || b.UserID != "bob" {
		t.Fatalf("AC-32.8 takeover of a session silent for 31 min: %+v %v", b, err)
	}
	old, _ := st.GetSession(ctx, a.SessionID)
	if old.Status != "ENDED" || old.EndReason != "TAKEOVER" || old.CurrentWorkID != "" {
		t.Fatalf("the taken-over session must end and release its lock: %+v", old)
	}
	if _, _, err := st.Heartbeat(alice, a.SessionID, now.Add(32*time.Minute), nil, 30*time.Minute); !errors.Is(err, ErrSessionEnded) {
		t.Fatalf("heartbeat of an ended session: %v", err)
	}
	if _, _, err := st.Heartbeat(alice, b.SessionID, now.Add(32*time.Minute), nil, 30*time.Minute); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("heartbeat by another user: %v", err)
	}
	if _, err := st.EndSession(alice, b.SessionID, "SIGN_OUT", false, now); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("sign-out of another user's session: %v", err)
	}
	forced, err := st.EndSession(as(ctx, "sup", dutySupervise), b.SessionID, "FORCE_SIGNOUT", true, now.Add(33*time.Minute))
	if err != nil || forced.Status != "ENDED" || forced.EndReason != "FORCE_SIGNOUT" {
		t.Fatalf("force sign-out: %+v %v", forced, err)
	}
	c, resumed, err := st.StartSession(alice, in(now.Add(34*time.Minute)))
	if err != nil || resumed || c.Status != "ACTIVE" {
		t.Fatalf("a device is free after sign-out: %+v %v %v", c, resumed, err)
	}
}

func TestStaleSweepResumeAndSkew(t *testing.T) {
	st, _, ctx := setup(t)
	wh := ids.New()
	now := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	alice := as(ctx, "alice", dutyExecute)
	a, _, err := st.StartSession(alice, StartInput{DeviceID: "dev-2", WarehouseID: wh, MenuKey: "default", Now: now, Idle: 30 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveRun(alice, a.SessionID, a.Version, []byte(`{"stepId":"scan_lp"}`), "work-1", now, 30*time.Minute); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.SweepStale(ctx, now.Add(29*time.Minute), 30*time.Minute); n != 0 {
		t.Fatalf("29 minutes is not stale: %d", n)
	}
	if n, err := st.SweepStale(ctx, now.Add(31*time.Minute), 30*time.Minute); err != nil || n != 1 {
		t.Fatalf("31 minutes is stale: %d %v", n, err)
	}
	held, _ := st.SessionsForWork(ctx, "work-1")
	if len(held) != 1 || held[0].Status != "STALE" || held[0].CurrentWorkID != "work-1" {
		t.Fatalf("a stale session keeps its work for resume: %+v", held)
	}
	deviceClock := now.Add(32 * time.Minute).Add(-10 * time.Minute)
	resumed, wasStale, err := st.Heartbeat(alice, a.SessionID, now.Add(32*time.Minute), &deviceClock, 30*time.Minute)
	if err != nil || !wasStale || resumed.Status != "ACTIVE" {
		t.Fatalf("resume: %+v %v %v", resumed, wasStale, err)
	}
	if skew, _ := st.DeviceSkew(ctx, "dev-2"); skew != 10*time.Minute {
		t.Fatalf("skew = %s, want the 10 minutes the device clock is behind", skew)
	}
	if _, err := st.SaveRun(alice, a.SessionID, 1, []byte(`{}`), "", now, 30*time.Minute); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("a stale version of the run must conflict: %v", err)
	}
}

func TestDevicesRegisterAndList(t *testing.T) {
	st, _, ctx := setup(t)
	d, err := st.RegisterDevice(ctx, Device{DeviceID: "tc52-01", Model: "TC52", ScannerProfile: "ZEBRA_DATAWEDGE", MenuKey: "default"})
	if err != nil || d.Model != "TC52" {
		t.Fatalf("register: %+v %v", d, err)
	}
	d2, err := st.RegisterDevice(ctx, Device{DeviceID: "tc52-01", Model: "TC52", ScannerProfile: "HONEYWELL_DC", MenuKey: "pick"})
	if err != nil || d2.ScannerProfile != "HONEYWELL_DC" || d2.MenuKey != "pick" {
		t.Fatalf("re-register updates: %+v %v", d2, err)
	}
	list, _ := st.ListDevices(ctx, 10)
	if len(list) != 1 {
		t.Fatalf("list: %d", len(list))
	}
	if _, err := st.RegisterDevice(ctx, Device{DeviceID: "x", ScannerProfile: "BOGUS"}); err == nil {
		t.Fatal("an unknown scanner profile must be refused by the database")
	}
}

func TestOfflineRecordGapReuseAndSettle(t *testing.T) {
	st, admin, ctx := setup(t)
	rec := func(seq int64, payload string) (OfflineTx, error) {
		return st.Record(ctx, OfflineIn{DeviceID: "dev-3", Seq: seq, Command: "ConfirmWorkLine", Payload: []byte(payload), CapturedAt: time.Now(), Hash: payload})
	}
	if _, err := rec(1, `{"a":1}`); err != nil {
		t.Fatal(err)
	}
	again, err := rec(1, `{"a":1}`)
	if err != nil || again.Status != TxPending {
		t.Fatalf("recording the same key twice returns the stored row: %+v %v", again, err)
	}
	if _, err := rec(1, `{"a":2}`); !errors.Is(err, ErrIdempotencyReuse) {
		t.Fatalf("same key, other body: %v", err)
	}
	var gap *GapError
	if _, err := rec(3, `{}`); !errors.As(err, &gap) || gap.Expected != 2 || gap.Got != 3 {
		t.Fatalf("MOB-0108 gap: %v", err)
	}
	for seq := int64(2); seq <= 3; seq++ {
		if _, err := rec(seq, fmt.Sprintf(`{"s":%d}`, seq)); err != nil {
			t.Fatal(err)
		}
	}
	n1, _ := st.NextPending(ctx, "dev-3")
	if n1 == nil || n1.Seq != 1 {
		t.Fatalf("next pending: %+v", n1)
	}
	if _, err := st.Settle(ctx, n1.IdempotencyKey, TxConflict, "line was CANCELLED by alice"); err != nil {
		t.Fatal(err)
	}
	if blocked, _ := st.NextPending(ctx, "dev-3"); blocked != nil {
		t.Fatalf("an unresolved conflict blocks later transactions (BR-32.3): %+v", blocked)
	}
	queue, _ := st.ListConflicts(ctx, time.Time{}, "", 10)
	if len(queue) != 1 || queue[0].ConflictReason != "line was CANCELLED by alice" {
		t.Fatalf("supervisor queue: %+v", queue)
	}
	var conflictEvents int
	_ = admin.QueryRow(ctx, `select count(*) from outbox where type = 'dev.passage.mobile.offline.conflict.v1'`).Scan(&conflictEvents)
	if conflictEvents != 1 {
		t.Fatalf("conflict events = %d", conflictEvents)
	}
	settledAgain, _ := st.Settle(ctx, n1.IdempotencyKey, TxApplied, "")
	if settledAgain.Status != TxConflict {
		t.Fatalf("a settled transaction does not change again: %+v", settledAgain)
	}
	if _, _, err := st.Resolve(as(ctx, "sup", dutySupervise), key("r0"), n1.IdempotencyKey, ResolveReject, 99); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale If-Match: %v", err)
	}
	res, _, err := st.Resolve(as(ctx, "sup", dutySupervise), key("r1"), n1.IdempotencyKey, ResolveReject, queue[0].Version)
	if err != nil || res.Status != TxRejected || res.Resolution != ResolveReject || res.ResolvedBy != "sup" {
		t.Fatalf("resolve: %+v %v", res, err)
	}
	if _, _, err := st.Resolve(as(ctx, "sup2", dutySupervise), key("r2"), n1.IdempotencyKey, ResolveReject, queue[0].Version); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("a second supervisor cannot resolve it again: %v", err)
	}
	if _, _, err := st.Resolve(ctx, key("r3"), "dev-3:99", ResolveReject, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown conflict: %v", err)
	}
	next, _ := st.NextPending(ctx, "dev-3")
	if next == nil || next.Seq != 2 {
		t.Fatalf("resolving unblocks the device: %+v", next)
	}
}

func TestResolveReapplyRecordsDecisionForConsumers(t *testing.T) {
	st, admin, ctx := setup(t)
	tx, _ := st.Record(ctx, OfflineIn{DeviceID: "dev-4", Seq: 1, Command: "ConfirmWorkLine", Payload: []byte(`{}`), CapturedAt: time.Now(), Hash: "h"})
	c, _ := st.Settle(ctx, tx.IdempotencyKey, TxConflict, "line was COMPLETED by bob")
	res, _, err := st.Resolve(as(ctx, "sup", dutySupervise), key("ra"), c.IdempotencyKey, ResolveReapply, c.Version)
	if err != nil || res.Status != TxConflict || res.Resolution != ResolveReapply {
		t.Fatalf("reapply: %+v %v", res, err)
	}
	var n int
	_ = admin.QueryRow(ctx, `select count(*) from outbox where type = 'dev.passage.mobile.offline.resolved.v1' and payload->'data'->>'action' = 'REAPPLY'`).Scan(&n)
	if n != 1 {
		t.Fatalf("resolved events = %d", n)
	}
	if queue, _ := st.ListConflicts(ctx, time.Time{}, "", 10); len(queue) != 0 {
		t.Fatalf("a resolved conflict leaves the queue: %+v", queue)
	}
}

func TestOnlyAStatusChangeMovesTheSessionVersion(t *testing.T) {
	st, _, ctx := setup(t)
	const idle = 30 * time.Minute
	now := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	alice := as(ctx, "alice", dutyExecute)
	a, _, err := st.StartSession(alice, StartInput{DeviceID: "dev-v", WarehouseID: ids.New(), MenuKey: "default", Now: now, Idle: idle})
	if err != nil {
		t.Fatal(err)
	}
	beat, wasStale, err := st.Heartbeat(alice, a.SessionID, now.Add(time.Minute), nil, idle)
	if err != nil || wasStale || beat.Version != a.Version {
		t.Fatalf("a heartbeat between a read and SaveRun must not fail the save: %d -> %d stale=%v %v", a.Version, beat.Version, wasStale, err)
	}
	if n, _ := st.SweepStale(ctx, now.Add(time.Hour), idle); n != 1 {
		t.Fatalf("sweep marked %d", n)
	}
	swept, _ := st.GetSession(ctx, a.SessionID)
	if swept.Status != "STALE" || swept.Version != a.Version+1 {
		t.Fatalf("the sweep is a status change: %+v", swept)
	}
	resumed, wasStale, err := st.Heartbeat(alice, a.SessionID, now.Add(time.Hour), nil, idle)
	if err != nil || !wasStale || resumed.Version != swept.Version+1 {
		t.Fatalf("coming back from STALE is a status change: %+v stale=%v %v", resumed, wasStale, err)
	}
}

func TestSaveRunRefusesASessionThatWentIdleUntilItBeats(t *testing.T) {
	const idle = 30 * time.Minute
	for name, swept := range map[string]bool{"swept to STALE": true, "past idle but still ACTIVE": false} {
		t.Run(name, func(t *testing.T) {
			st, _, ctx := setup(t)
			now := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
			alice := as(ctx, "alice", dutyExecute)
			a, _, err := st.StartSession(alice, StartInput{DeviceID: "dev-s", WarehouseID: ids.New(), MenuKey: "default", Now: now, Idle: idle})
			if err != nil {
				t.Fatal(err)
			}
			later := now.Add(31 * time.Minute)
			cur := a
			if swept {
				if n, _ := st.SweepStale(ctx, later, idle); n != 1 {
					t.Fatalf("sweep marked %d", n)
				}
				cur, _ = st.GetSession(ctx, a.SessionID)
			}
			if _, err := st.SaveRun(alice, a.SessionID, cur.Version, []byte(`{}`), "", later, idle); !errors.Is(err, ErrSessionStale) {
				t.Fatalf("a session idle past the timeout cannot save a step: %v", err)
			}
			beat, wasStale, err := st.Heartbeat(alice, a.SessionID, later, nil, idle)
			if err != nil || !wasStale {
				t.Fatalf("heartbeat: %+v %v %v", beat, wasStale, err)
			}
			if _, err := st.SaveRun(alice, a.SessionID, beat.Version, []byte(`{}`), "", later, idle); err != nil {
				t.Fatalf("after the heartbeat the step saves: %v", err)
			}
			if _, err := st.SaveRun(alice, a.SessionID, cur.Version, []byte(`{}`), "", later, idle); !errors.Is(err, ErrVersionConflict) {
				t.Fatalf("the version read before the save is spent: %v", err)
			}
		})
	}
}

func TestSessionOnDeviceIsTheCallersOwnOpenSession(t *testing.T) {
	st, _, ctx := setup(t)
	now := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	alice, bob := as(ctx, "alice", dutyExecute), as(ctx, "bob", dutyExecute)
	a, _, err := st.StartSession(alice, StartInput{DeviceID: "dev-o", WarehouseID: ids.New(), MenuKey: "default", Now: now, Idle: 30 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := st.SessionOnDevice(alice, "dev-o", ""); err != nil || got.SessionID != a.SessionID {
		t.Fatalf("own session: %+v %v", got, err)
	}
	if got, err := st.SessionOnDevice(alice, "dev-o", a.SessionID); err != nil || got.SessionID != a.SessionID {
		t.Fatalf("own session by id: %+v %v", got, err)
	}
	for name, call := range map[string]func() error{
		"another user":    func() error { _, err := st.SessionOnDevice(bob, "dev-o", ""); return err },
		"another device":  func() error { _, err := st.SessionOnDevice(alice, "dev-x", ""); return err },
		"another session": func() error { _, err := st.SessionOnDevice(alice, "dev-o", ids.New()); return err },
	} {
		if err := call(); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := st.EndSession(alice, a.SessionID, "SIGN_OUT", false, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SessionOnDevice(alice, "dev-o", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an ended session is not open: %v", err)
	}
}

func TestIdempotentRunsOncePerKeyAndKeepsFailuresRetryable(t *testing.T) {
	st, _, ctx := setup(t)
	runs := 0
	fn := func() (map[string]int, error) { runs++; return map[string]int{"n": runs}, nil }
	first, replayed, err := Idempotent(ctx, st, Idem{Key: "idem-1", Hash: "h"}, 200, fn)
	again, replayedAgain, err2 := Idempotent(ctx, st, Idem{Key: "idem-1", Hash: "h"}, 200, fn)
	if err != nil || err2 != nil || replayed || !replayedAgain || runs != 1 || first["n"] != 1 || again["n"] != 1 {
		t.Fatalf("%v %v %v %v runs=%d %v %v", err, err2, replayed, replayedAgain, runs, first, again)
	}
	if _, _, err := Idempotent(ctx, st, Idem{Key: "idem-1", Hash: "another"}, 200, fn); !errors.Is(err, ErrIdempotencyReuse) {
		t.Fatalf("same key, another request: %v", err)
	}
	boom := errors.New("warehouse down")
	if _, _, err := Idempotent(ctx, st, Idem{Key: "idem-2", Hash: "h"}, 200, func() (int, error) { return 0, boom }); !errors.Is(err, boom) {
		t.Fatalf("failure: %v", err)
	}
	if out, replayed, err := Idempotent(ctx, st, Idem{Key: "idem-2", Hash: "h"}, 200, func() (int, error) { return 7, nil }); err != nil || replayed || out != 7 {
		t.Fatalf("a failed attempt stores nothing, so the retry runs: %d %v %v", out, replayed, err)
	}
}

func smallPool(t *testing.T, admin *pgxpool.Pool, conns int32) *Store {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(os.Getenv(testkit.EnvAppURL))
	if err != nil {
		t.Fatal(err)
	}
	var schema string
	if err := admin.QueryRow(context.Background(), "select current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	cfg.MaxConns = conns
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return New(pool)
}

func TestIdempotentHoldsNoConnectionWhileItsFunctionRuns(t *testing.T) {
	_, admin, ctx := setup(t)
	st := smallPool(t, admin, 2)
	done := make(chan error, 4)
	for i := range 4 {
		go func() {
			_, _, err := Idempotent(ctx, st, Idem{Key: fmt.Sprintf("pool-%d", i), Hash: "h"}, 200, func() (int, error) {
				_, err := st.ListDevices(ctx, 1)
				return i, err
			})
			done <- err
		}()
	}
	for range 4 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("four commands on a pool of two deadlocked")
		}
	}
}

func TestIdempotentRepeatWhileRunningIsInProgressAndAStaleClaimIsRetaken(t *testing.T) {
	st, admin, ctx := setup(t)
	idem := Idem{Key: "same-key", Hash: "h"}
	started, release := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() {
		_, _, err := Idempotent(ctx, st, idem, 200, func() (int, error) {
			close(started)
			<-release
			return 1, nil
		})
		first <- err
	}()
	<-started
	if _, _, err := Idempotent(ctx, st, idem, 200, func() (int, error) { return 2, nil }); !errors.Is(err, ErrInProgress) {
		t.Fatalf("a repeat while the first runs: %v", err)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if out, replayed, err := Idempotent(ctx, st, idem, 200, func() (int, error) { return 3, nil }); err != nil || !replayed || out != 1 {
		t.Fatalf("after it finishes the repeat replays: %d %v %v", out, replayed, err)
	}

	crashed := Idem{Key: "crashed", Hash: "h"}
	if _, err := admin.Exec(ctx, `insert into idempotency_keys (tenant_id, key, request_hash, response_status, created_at) values ($1, $2, 'h', 0, now())`, testkit.TenantID(1), crashed.Key); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Idempotent(ctx, st, crashed, 200, func() (int, error) { return 4, nil }); !errors.Is(err, ErrInProgress) {
		t.Fatalf("a fresh claim blocks: %v", err)
	}
	if _, err := admin.Exec(ctx, `update idempotency_keys set created_at = now() - interval '2 minutes' where key = $1`, crashed.Key); err != nil {
		t.Fatal(err)
	}
	if out, replayed, err := Idempotent(ctx, st, crashed, 200, func() (int, error) { return 4, nil }); err != nil || replayed || out != 4 {
		t.Fatalf("a claim left by a crash is retryable after its time: %d %v %v", out, replayed, err)
	}
}

func TestARetakenClaimIsNotOverwrittenOrReleasedByTheRunnerThatLostIt(t *testing.T) {
	st, admin, ctx := setup(t)
	age := func(key string) {
		if _, err := admin.Exec(ctx, `update idempotency_keys set created_at = now() - interval '2 minutes' where key = $1`, key); err != nil {
			t.Fatal(err)
		}
	}
	idem := Idem{Key: "retaken", Hash: "h"}
	started, release := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() {
		_, _, err := Idempotent(ctx, st, idem, 200, func() (int, error) {
			close(started)
			<-release
			return 1, nil
		})
		first <- err
	}()
	<-started
	age(idem.Key)
	if out, replayed, err := Idempotent(ctx, st, idem, 200, func() (int, error) { return 2, nil }); err != nil || replayed || out != 2 {
		t.Fatalf("the retaking runner finishes: %d %v %v", out, replayed, err)
	}
	close(release)
	if err := <-first; !errors.Is(err, ErrInProgress) {
		t.Fatalf("the first runner must not store over the retaken key: %v", err)
	}
	if out, replayed, err := Idempotent(ctx, st, idem, 200, func() (int, error) { return 3, nil }); err != nil || !replayed || out != 2 {
		t.Fatalf("the retaking runner's answer stands: %d %v %v", out, replayed, err)
	}

	failing := Idem{Key: "retaken-fail", Hash: "h"}
	started, release = make(chan struct{}), make(chan struct{})
	boom := errors.New("late failure")
	go func() {
		_, _, err := Idempotent(ctx, st, failing, 200, func() (int, error) {
			close(started)
			<-release
			return 0, boom
		})
		first <- err
	}()
	<-started
	age(failing.Key)
	secondStarted, secondRelease, second := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, _, err := Idempotent(ctx, st, failing, 200, func() (int, error) {
			close(secondStarted)
			<-secondRelease
			return 4, nil
		})
		second <- err
	}()
	<-secondStarted
	close(release)
	if err := <-first; !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if _, _, err := Idempotent(ctx, st, failing, 200, func() (int, error) { return 5, nil }); !errors.Is(err, ErrInProgress) {
		t.Fatalf("the first runner's release must not delete the retaken claim: %v", err)
	}
	close(secondRelease)
	if err := <-second; err != nil {
		t.Fatal(err)
	}
}

// #522: a reject of a PENDING transaction a replay is applying waits for the apply; once it settled the reject is a
// version conflict, and a lease a dead replay left runs out.
func TestSupervisorRejectWaitsForAnApplyInProgress(t *testing.T) {
	st, admin, ctx := setup(t)
	alice := as(ctx, "alice", dutyExecute)
	if _, err := st.Record(alice, OfflineIn{DeviceID: "dev-apply", Seq: 1, Command: "ConfirmWorkLine", Payload: []byte(`{}`), CapturedAt: time.Now(), Hash: "a"}); err != nil {
		t.Fatal(err)
	}
	sup := as(ctx, "sup", dutySupervise)
	if next, err := st.NextPending(alice, "dev-apply"); err != nil || next == nil {
		t.Fatalf("claim: %v %v", next, err)
	}
	if _, _, err := st.Resolve(sup, key("ra1"), "dev-apply:1", ResolveReject, 0); !errors.Is(err, ErrApplyInProgress) {
		t.Fatalf("a reject during the apply: %v", err)
	}
	if _, err := st.Settle(alice, "dev-apply:1", TxApplied, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Resolve(sup, key("ra2"), "dev-apply:1", ResolveReject, 0); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("a reject after the apply settled: %v", err)
	}

	if _, err := st.Record(alice, OfflineIn{DeviceID: "dev-apply", Seq: 2, Command: "ConfirmWorkLine", Payload: []byte(`{}`), CapturedAt: time.Now(), Hash: "b"}); err != nil {
		t.Fatal(err)
	}
	if next, err := st.NextPending(alice, "dev-apply"); err != nil || next == nil {
		t.Fatalf("claim 2: %v %v", next, err)
	}
	if _, err := admin.Exec(context.Background(), `update offline_transaction set apply_until = now() - interval '1 second' where idempotency_key = 'dev-apply:2'`); err != nil {
		t.Fatal(err)
	}
	if out, _, err := st.Resolve(sup, key("ra3"), "dev-apply:2", ResolveReject, 0); err != nil || out.Status != TxRejected {
		t.Fatalf("a reject after the lease ran out: %+v %v", out, err)
	}
}

func TestSupervisorRejectFreesADeviceQueueHeldByAnotherUsersPendingEntry(t *testing.T) {
	st, _, ctx := setup(t)
	alice := as(ctx, "alice", dutyExecute)
	if _, err := st.Record(alice, OfflineIn{DeviceID: "dev-held", Seq: 1, Command: "ConfirmWorkLine", Payload: []byte(`{}`), CapturedAt: time.Now(), Hash: "a"}); err != nil {
		t.Fatal(err)
	}
	sup := as(ctx, "sup", dutySupervise)
	if _, _, err := st.Resolve(sup, key("rp"), "dev-held:1", ResolveReapply, 0); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("only a reject closes a pending entry: %v", err)
	}
	out, _, err := st.Resolve(sup, key("rp2"), "dev-held:1", ResolveReject, 0)
	if err != nil || out.Status != TxRejected || out.ResolvedBy != "sup" {
		t.Fatalf("%+v %v", out, err)
	}
	if next, _ := st.NextPending(sup, "dev-held"); next != nil {
		t.Fatalf("the queue is free: %+v", next)
	}
}
