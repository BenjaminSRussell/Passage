package store

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
)

// worker registers a technician linked to a worker id, the user id its device signs in as.
func (d dispatchFixture) worker(t *testing.T, code string) Technician {
	t.Helper()
	worker := ids.New()
	tech, _, err := d.s.CreateTechnician(d.ctx, Technician{Code: code, Name: "Tech " + code, WorkerID: &worker, LegalEntityID: d.le, TerritoryIDs: []string{d.territory},
		TimeZone: "America/Chicago", ShiftDays: []int{1, 2, 3, 4, 5}, ShiftStartMinute: 480, ShiftEndMinute: 1020, MaxDailyMinutes: 480, Active: true}, "key-"+code, "hash-"+code)
	if err != nil {
		t.Fatal(err)
	}
	return tech
}

// as is the fixture's tenant signed in as user with only the given duties.
func (d dispatchFixture) as(user string, duties ...string) context.Context {
	p, _ := tenant.FromContext(d.ctx)
	p.UserID = user
	p.Duties = map[string]struct{}{tenant.DutyAllScope: {}}
	for _, duty := range duties {
		p.Duties[duty] = struct{}{}
	}
	return tenant.With(context.Background(), p)
}

// device is the technician's own device: its worker id and fieldservice.work-order.execute.
func (d dispatchFixture) device(tech Technician) context.Context {
	return d.as(*tech.WorkerID, "fieldservice.work-order.execute")
}

// dispatched returns a DISPATCHED work order with the given lines, assigned to a new
// technician through :assign and publishing, the only road to those states. The order
// is dated before the fixed device times of these tests.
func (d dispatchFixture) dispatched(t *testing.T, code string, lines []domain.WorkLine, exempt bool) (WorkOrder, Technician) {
	t.Helper()
	tech := d.worker(t, code)
	wo, _, err := d.s.CreateWorkOrder(d.ctx, WorkOrder{CustomerPartyID: d.customer, LegalEntityID: d.le, TerritoryID: d.territory,
		EstDurationMinutes: 60, Currency: "USD", Lines: lines, SignatureExempt: exempt}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := dbx.WithTenantTx(d.ctx, d.s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(d.ctx, `update service_work_order set created_at = '2026-10-20T00:00:00Z' where tenant_id = $1 and id = $2`, tenantOf(d.ctx), wo.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 26, 15, 0, 0, 0, time.UTC)
	wo = d.assign(t, wo, tech, start)
	if _, _, err := d.s.PublishDispatch(d.ctx, PublishInput{LegalEntityID: d.le, TerritoryID: d.territory, Date: "2026-10-26", TimeZone: "America/Chicago"}, start, "", ""); err != nil {
		t.Fatal(err)
	}
	wo, err = d.s.GetWorkOrder(d.ctx, wo.ID)
	if err != nil || wo.Status != "DISPATCHED" {
		t.Fatalf("dispatched order %+v %v", wo, err)
	}
	return wo, tech
}

func at(m int) time.Time {
	return time.Date(2026, 10, 26, 15, 0, 0, 0, time.UTC).Add(time.Duration(m) * time.Minute)
}

func act(kind domain.ActionKind, m int) ActionInput {
	return ActionInput{ID: ids.New(), Kind: kind, DeviceTime: at(m)}
}

func outcomes(r SyncResult) []string {
	var out []string
	for _, x := range r.Results {
		out = append(out, x.Outcome)
	}
	return out
}

// BR-45.6, AC-45.7 against Postgres as passage_app: a technician offline for hours syncs
// start, part and complete actions out of order; the server orders them by device time,
// the state equals the device's, each part action counts once, a replayed batch changes
// nothing, and exactly one completed event carries the lines and the working minutes.
func TestStore_OfflineActionsMergeToDeviceState(t *testing.T) {
	d := newDispatch(t, 17)
	part := domain.WorkLine{ID: ids.New(), Kind: domain.KindPart, Qty: decimal.NewFromInt(3), UnitPrice: decimal.NewFromInt(20)}
	labor := domain.WorkLine{ID: ids.New(), Kind: domain.KindLabor, Qty: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(100)}
	wo, tech := d.dispatched(t, "OFF", []domain.WorkLine{part, labor}, false)

	p1 := ActionInput{ID: ids.New(), Kind: domain.ActPartUsed, DeviceTime: at(50), LineID: part.ID, Qty: decimal.NewFromInt(2)}
	p2 := ActionInput{ID: ids.New(), Kind: domain.ActPartUsed, DeviceTime: at(70), LineID: part.ID, Qty: decimal.NewFromInt(1)}
	sign := ActionInput{ID: ids.New(), Kind: domain.ActSignature, DeviceTime: at(80), SignedBy: "Pat Customer", SignatureRef: "s3://sig/1.png"}
	note := ActionInput{ID: ids.New(), Kind: domain.ActNote, DeviceTime: at(75), Text: "replaced the pump"}
	batch := []ActionInput{act(domain.ActComplete, 90), p2, sign, note, p1, act(domain.ActWorkStart, 20), act(domain.ActTravelStart, 5)}

	res, err := d.s.RecordActions(d.device(tech), wo.ID, tech.ID, batch, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied != 7 || res.Rejected != 0 || res.WorkOrder.Status != "COMPLETED" || res.WorkOrder.CompletedAt == nil {
		t.Fatalf("merge %v status %s", outcomes(res), res.WorkOrder.Status)
	}
	if got := res.WorkOrder; got.TechnicianNote != "replaced the pump" || got.SignedBy == nil || *got.SignedBy != "Pat Customer" || got.FirstTimeFix == nil || !*got.FirstTimeFix {
		t.Fatalf("completion data %+v", got)
	}
	if res.Results[0].ID != batch[6].ID || res.Results[len(res.Results)-1].ID != batch[0].ID {
		t.Fatalf("results are not in device-time order: %v", res.Results)
	}

	// The same batch again is a replay: every action is a duplicate and nothing changes.
	again, err := d.s.RecordActions(d.device(tech), wo.ID, tech.ID, batch, time.Now())
	if err != nil || again.Duplicates != 7 || again.Applied != 0 || again.WorkOrder.Version != res.WorkOrder.Version {
		t.Fatalf("replay %+v %v", again, err)
	}
	if n := outboxCount(t, fixture{ctx: d.ctx, s: d.s}, "dev.passage.service.workorder.completed.v1"); n != 1 {
		t.Fatalf("completed events: %d, want 1", n)
	}
	var minutes int
	var consumed string
	if err := dbx.WithTenantTx(d.ctx, d.s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(d.ctx, `select (payload->'data'->>'durationMinutes')::int, payload->'data'->'lines'->0->>'consumedQty' from outbox where type = 'dev.passage.service.workorder.completed.v1'`).Scan(&minutes, &consumed)
	}); err != nil {
		t.Fatal(err)
	}
	if minutes != 70 || consumed != "3" {
		t.Fatalf("event duration %d consumed %s, want 70 and 3", minutes, consumed)
	}
	log, err := d.s.ListActions(d.ctx, wo.ID)
	if err != nil || len(log) != 7 || log[0].Kind != "TRAVEL_START" || log[6].Kind != "COMPLETE" {
		t.Fatalf("log %+v %v", log, err)
	}
	// Posting now passes SVC-WO-002 because every part was consumed.
	if _, _, err := d.s.PostWorkOrder(d.ctx, wo.ID, res.WorkOrder.Version, "post-offline"); err != nil {
		t.Fatalf("post: %v", err)
	}
}

// Conflicting status moves resolve to the furthest state, and a late action behind it is
// stored as superseded, not applied (BR-45.6).
func TestStore_ConflictingStatusActionsResolveToFurthestState(t *testing.T) {
	d := newDispatch(t, 18)
	wo, tech := d.dispatched(t, "CON", nil, true)
	res, err := d.s.RecordActions(d.device(tech), wo.ID, tech.ID, []ActionInput{act(domain.ActWorkStart, 10)}, time.Now())
	if err != nil || res.WorkOrder.Status != "IN_PROGRESS" {
		t.Fatalf("work start %+v %v", res, err)
	}
	late, err := d.s.RecordActions(d.device(tech), wo.ID, tech.ID, []ActionInput{act(domain.ActTravelStart, 2), act(domain.ActHold, 30), act(domain.ActResume, 31)}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	got := outcomes(late)
	if got[0] != domain.OutcomeSuperseded || got[1] != domain.OutcomeApplied || got[2] != domain.OutcomeApplied || late.Superseded != 1 || late.WorkOrder.Status != "IN_PROGRESS" {
		t.Fatalf("late batch %v status %s", got, late.WorkOrder.Status)
	}
	// An exempt order completes without a signature.
	done, err := d.s.RecordActions(d.device(tech), wo.ID, tech.ID, []ActionInput{act(domain.ActComplete, 60)}, time.Now())
	if err != nil || done.WorkOrder.Status != "COMPLETED" {
		t.Fatalf("complete %+v %v", done, err)
	}
	after, err := d.s.RecordActions(d.device(tech), wo.ID, tech.ID, []ActionInput{act(domain.ActWorkStart, 70)}, time.Now())
	if err != nil || after.Results[0].Outcome != domain.OutcomeSuperseded || after.WorkOrder.Status != "COMPLETED" {
		t.Fatalf("after completion %+v %v", after, err)
	}
}

// SVC-WO-001, SVC-WO-002 and the part and technician checks, each refusing one action
// while the rest of the batch still merges.
func TestStore_ActionValidations(t *testing.T) {
	d := newDispatch(t, 19)
	part := domain.WorkLine{ID: ids.New(), Kind: domain.KindPart, Qty: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(20)}
	wo, tech := d.dispatched(t, "VAL", []domain.WorkLine{part}, false)
	other := d.worker(t, "OTH")

	if r, err := d.s.RecordActions(d.device(other), wo.ID, other.ID, []ActionInput{act(domain.ActTravelStart, 1)}, time.Now()); err != nil ||
		r.Results[0].Outcome != domain.OutcomeRejected || r.Results[0].Code != domain.CodeActionTechnician {
		t.Fatalf("another technician %+v %v", r, err)
	}
	if _, err := d.s.RecordActions(d.device(tech), wo.ID, ids.New(), []ActionInput{act(domain.ActTravelStart, 1)}, time.Now()); problemCode(err) != CodeTechnicianUnknown {
		t.Fatalf("unknown technician: %v", err)
	}
	res, err := d.s.RecordActions(d.device(tech), wo.ID, tech.ID, []ActionInput{
		act(domain.ActWorkStart, 10),
		{ID: ids.New(), Kind: domain.ActPartUsed, DeviceTime: at(11), LineID: ids.New(), Qty: decimal.NewFromInt(1)},
		{ID: ids.New(), Kind: domain.ActPartUsed, DeviceTime: at(12), LineID: part.ID, Qty: decimal.NewFromInt(2)},
		act(domain.ActComplete, 20),
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Results[1].Code != domain.CodeActionPart || res.Results[2].Code != domain.CodeActionPart || res.Results[3].Code != domain.CodeSignatureRequired || res.WorkOrder.Status != "IN_PROGRESS" {
		t.Fatalf("validations %+v", res.Results)
	}
	// Signed, the completion goes through; posting is then refused until the part is used.
	res, err = d.s.RecordActions(d.device(tech), wo.ID, tech.ID, []ActionInput{
		{ID: ids.New(), Kind: domain.ActSignature, DeviceTime: at(25), SignedBy: "Pat", SignatureRef: "ref"}, act(domain.ActComplete, 26)}, time.Now())
	if err != nil || res.WorkOrder.Status != "COMPLETED" {
		t.Fatalf("signed completion %+v %v", res, err)
	}
	if _, _, err := d.s.PostWorkOrder(d.ctx, wo.ID, res.WorkOrder.Version, "post-1"); problemCode(err) != domain.CodePartsNotConsumed {
		t.Fatalf("post with an unconsumed part: %v", err)
	}
	// An id reused for another kind is refused outright.
	reuse := ActionInput{ID: res.Results[0].ID, Kind: domain.ActNote, DeviceTime: at(30), Text: "x"}
	if _, err := d.s.RecordActions(d.device(tech), wo.ID, tech.ID, []ActionInput{reuse}, time.Now()); problemCode(err) != domain.CodeActionReuse {
		t.Fatalf("id reuse: %v", err)
	}
}

// The hand-made transition can no longer reach SCHEDULED or DISPATCHED, and a manual
// completion needs the signature (or the exemption) and writes the completed event, with
// no working minutes when there is no action log.
func TestStore_TransitionBypassClosed(t *testing.T) {
	d := newDispatch(t, 20)
	wo := d.order(t, 60, nil, nil)
	for _, to := range []domain.WOStatus{domain.WOScheduled, domain.WODispatched} {
		if _, err := d.s.TransitionWorkOrder(d.ctx, wo.ID, wo.Version, to); problemCode(err) != CodeWOInvalid {
			t.Fatalf("by hand to %s: %v", to, err)
		}
	}
	got, err := d.s.GetWorkOrder(d.ctx, wo.ID)
	if err != nil || got.Status != "UNSCHEDULED" || got.Version != wo.Version {
		t.Fatalf("the refused moves changed the order: %+v %v", got, err)
	}
	walk := func(id string) WorkOrder {
		cur, err := d.s.GetWorkOrder(d.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		for _, to := range []domain.WOStatus{domain.WOScheduled, domain.WODispatched, domain.WOTraveling, domain.WOInProgress} {
			if cur, err = d.s.moveWorkOrder(d.ctx, id, cur.Version, to); err != nil {
				t.Fatal(err)
			}
		}
		return cur
	}
	cur := walk(wo.ID)
	if _, err := d.s.TransitionWorkOrder(d.ctx, wo.ID, cur.Version, domain.WOCompleted); problemCode(err) != domain.CodeSignatureRequired {
		t.Fatalf("manual completion without a signature: %v", err)
	}
	exempt, _, err := d.s.CreateWorkOrder(d.ctx, WorkOrder{CustomerPartyID: d.customer, LegalEntityID: d.le, TerritoryID: d.territory,
		EstDurationMinutes: 30, Currency: "USD", SignatureExempt: true}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	cur = walk(exempt.ID)
	done, err := d.s.TransitionWorkOrder(d.ctx, exempt.ID, cur.Version, domain.WOCompleted)
	if err != nil || done.Status != "COMPLETED" || done.CompletedAt == nil {
		t.Fatalf("exempt completion %+v %v", done, err)
	}
	if n := outboxCount(t, fixture{ctx: d.ctx, s: d.s}, "dev.passage.service.workorder.completed.v1"); n != 1 {
		t.Fatalf("completed events: %d, want 1", n)
	}
}

// Only the technician's own worker, or office staff with fieldservice.work-order.maintain, may record actions
// for a technician; claiming another technician's id in the body is refused outright.
func TestStore_ActionsAreBoundToTheTechniciansWorker(t *testing.T) {
	d := newDispatch(t, 21)
	wo, tech := d.dispatched(t, "BND", nil, true)
	other := d.worker(t, "BN2")
	for name, ctx := range map[string]context.Context{
		"another technician's device":  d.device(other),
		"a user with no worker":        d.as(ids.New(), "fieldservice.work-order.execute"),
		"the technician's id, no user": d.as("", "fieldservice.work-order.execute"),
	} {
		_, err := d.s.RecordActions(ctx, wo.ID, tech.ID, []ActionInput{act(domain.ActTravelStart, 1)}, time.Now())
		var problem *httpx.Problem
		if problemCode(err) != domain.CodeActionTechnician || !errors.As(err, &problem) || problem.Status != http.StatusForbidden {
			t.Fatalf("%s: %v", name, err)
		}
	}
	log, err := d.s.ListActions(d.ctx, wo.ID)
	if err != nil || len(log) != 0 {
		t.Fatalf("a refused batch left %d actions: %v", len(log), err)
	}
	office := d.as(ids.New(), "fieldservice.work-order.execute", "fieldservice.work-order.maintain")
	res, err := d.s.RecordActions(office, wo.ID, tech.ID, []ActionInput{act(domain.ActTravelStart, 1)}, time.Now())
	if err != nil || res.Applied != 1 || res.WorkOrder.Status != "TRAVELING" {
		t.Fatalf("office entry %+v %v", res, err)
	}
}

// Device times are not trusted blindly: a clock ahead of the server is refused at the API,
// one before the work order existed is rejected here, a retried id must carry the same
// action, and a completion from a device whose clock lags is not dated before the work
// already applied, so the working minutes stay honest.
func TestStore_DeviceClockAndRetries(t *testing.T) {
	d := newDispatch(t, 22)
	wo, tech := d.dispatched(t, "CLK", nil, true)
	ctx := d.device(tech)
	backdated := ActionInput{ID: ids.New(), Kind: domain.ActWorkStart, DeviceTime: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)}
	res, err := d.s.RecordActions(ctx, wo.ID, tech.ID, []ActionInput{backdated}, time.Now())
	if err != nil || res.Results[0].Code != domain.CodeActionClock || res.WorkOrder.Status != "DISPATCHED" {
		t.Fatalf("backdated %+v %v", res, err)
	}
	start := act(domain.ActWorkStart, 10)
	if _, err := d.s.RecordActions(ctx, wo.ID, tech.ID, []ActionInput{start}, time.Now()); err != nil {
		t.Fatal(err)
	}
	// The same id with another device time or payload is not a retry.
	moved := start
	moved.DeviceTime = at(11)
	if _, err := d.s.RecordActions(ctx, wo.ID, tech.ID, []ActionInput{moved}, time.Now()); problemCode(err) != domain.CodeActionReuse {
		t.Fatalf("id reused with another time: %v", err)
	}
	note := ActionInput{ID: ids.New(), Kind: domain.ActNote, DeviceTime: at(12), Text: "a"}
	if _, err := d.s.RecordActions(ctx, wo.ID, tech.ID, []ActionInput{note}, time.Now()); err != nil {
		t.Fatal(err)
	}
	note.Text = "b"
	if _, err := d.s.RecordActions(ctx, wo.ID, tech.ID, []ActionInput{note}, time.Now()); problemCode(err) != domain.CodeActionReuse {
		t.Fatalf("id reused with another text: %v", err)
	}
	// A lagging second device completes "before" the work started: the completion is
	// dated at the start instead, and the event reports no negative or phantom minutes.
	done, err := d.s.RecordActions(ctx, wo.ID, tech.ID, []ActionInput{act(domain.ActComplete, 5)}, time.Now())
	if err != nil || done.WorkOrder.Status != "COMPLETED" || done.WorkOrder.CompletedAt == nil || !done.WorkOrder.CompletedAt.Equal(at(10)) {
		t.Fatalf("lagging completion %+v %v", done.WorkOrder, err)
	}
	// Actions that arrive after the completion stay in the log: an earlier status move as
	// superseded, a part as rejected (the completion already reported the consumption).
	late, err := d.s.RecordActions(ctx, wo.ID, tech.ID, []ActionInput{act(domain.ActTravelStart, 1),
		{ID: ids.New(), Kind: domain.ActPartUsed, DeviceTime: at(6), LineID: ids.New(), Qty: decimal.NewFromInt(1)}}, time.Now())
	if err != nil || late.Results[0].Outcome != domain.OutcomeSuperseded || late.Results[1].Code != domain.CodeActionStatus {
		t.Fatalf("late actions %+v %v", late, err)
	}
	if n := outboxCount(t, fixture{ctx: d.ctx, s: d.s}, "dev.passage.service.workorder.completed.v1"); n != 1 {
		t.Fatalf("completed events: %d, want 1", n)
	}
}

// Two batches that both complete the same work order race for its lock: exactly one
// completes it and exactly one completed event is written.
func TestStore_ConcurrentCompletionsWriteOneEvent(t *testing.T) {
	d := newDispatch(t, 23)
	wo, tech := d.dispatched(t, "RACE", nil, true)
	ctx := d.device(tech)
	if _, err := d.s.RecordActions(ctx, wo.ID, tech.ID, []ActionInput{act(domain.ActWorkStart, 10)}, time.Now()); err != nil {
		t.Fatal(err)
	}
	const n = 6
	errs := make(chan error, n)
	applied := make(chan int, n)
	for i := 0; i < n; i++ {
		go func(m int) {
			r, err := d.s.RecordActions(ctx, wo.ID, tech.ID, []ActionInput{act(domain.ActComplete, 20+m)}, time.Now())
			errs <- err
			applied <- r.Applied
		}(i)
	}
	total := 0
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		total += <-applied
	}
	if total != 1 {
		t.Fatalf("%d batches completed the order, want 1", total)
	}
	if got := outboxCount(t, fixture{ctx: d.ctx, s: d.s}, "dev.passage.service.workorder.completed.v1"); got != 1 {
		t.Fatalf("completed events: %d, want 1", got)
	}
}
