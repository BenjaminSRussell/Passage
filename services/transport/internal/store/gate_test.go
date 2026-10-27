package store_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	tmsevents "github.com/BenjaminSRussell/passage/services/transport/internal/events"
	"github.com/BenjaminSRussell/passage/services/transport/internal/store"
	"github.com/BenjaminSRussell/passage/services/transport/migrations"
)

var gateIn = time.Date(2026, 3, 2, 8, 0, 0, 0, time.UTC)

func checkIn(t *testing.T, site, direction, trailer, seal string) domain.GateVisit {
	t.Helper()
	sched := gateIn.Add(-15 * time.Minute)
	v, err := domain.OpenVisit(domain.GateCheckIn{SiteID: site, Direction: direction, CarrierSCAC: "ABCD", TrailerNo: trailer, SealNo: seal,
		DriverName: "Dana Driver", DriverLicenseID: "D1234567", AppointmentID: ids.New(), ScheduledAt: &sched, ArrivedAt: gateIn,
		FreeTimeMin: 120, ToleranceMin: 60, WalkInAllowed: true})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func gateLogKinds(log []domain.GateLogEntry) string {
	kinds := make([]string, len(log))
	for i, e := range log {
		kinds[i] = e.Kind
	}
	return strings.Join(kinds, ",")
}

// TestGate_CheckInStoredPublishedAndReplayed: the visit, its CHECK_IN row and
// gate.checkedin commit together; a replay writes nothing; the event carries
// no driver PII; a trailer is in a yard once.
func TestGate_CheckInStoredPublishedAndReplayed(t *testing.T) {
	s, admin := setup(t)
	ctx := tenantCtx(1)
	site := ids.New()
	in := checkIn(t, site, domain.DirectionInbound, "TR-1", "555")
	v, err := s.CheckIn(ctx, in, store.Idem{Key: "gate-1", Fingerprint: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if v.ID == "" || v.Status != domain.VisitInYard || v.SealNo != "555" || v.Arrival == nil || v.Arrival.Status != domain.ArrivalOnTime ||
		v.Arrival.DeltaMin != 15 || !v.CheckedInAt.Equal(gateIn) || v.Version != 1 || v.DriverName != "Dana Driver" {
		t.Fatalf("visit = %+v arrival %+v", v, v.Arrival)
	}
	again, err := s.CheckIn(ctx, in, store.Idem{Key: "gate-1", Fingerprint: "a"})
	if err != nil || again.ID != v.ID {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	if _, err := s.CheckIn(ctx, in, store.Idem{Key: "gate-1", Fingerprint: "b"}); problemCode(t, err) != store.CodeIdempotencyReuse {
		t.Fatalf("reuse = %v", err)
	}
	if _, err := s.CheckIn(ctx, in, store.Idem{}); problemCode(t, err) != domain.CodeVisitState {
		t.Fatalf("second check-in of the trailer = %v, want TMS-0118", err)
	}
	evts := eventsOf(t, admin, tmsevents.GateCheckedIn)
	if len(evts) != 1 || evts[0].Subject != v.ID {
		t.Fatalf("gate events = %+v", evts)
	}
	if raw := string(evts[0].Data); strings.Contains(raw, "Dana") || strings.Contains(raw, "D1234567") {
		t.Fatalf("event carries driver PII: %s", raw)
	}
	var p tmsevents.GateCheckedInPayload
	if err := json.Unmarshal(evts[0].Data, &p); err != nil || p.ArrivalStatus != domain.ArrivalOnTime || p.TrailerNo != "TR-1" || p.SiteID != site {
		t.Fatalf("payload = %+v, %v", p, err)
	}
	_, log, err := s.GetGateVisit(ctx, v.ID)
	if err != nil || gateLogKinds(log) != domain.GateLogCheckIn || log[0].RecordedBy != "user-test" || log[0].Detail["sealNo"] != "555" {
		t.Fatalf("log = %+v, %v", log, err)
	}
	// Another site's yard is another yard.
	if _, err := s.CheckIn(ctx, checkIn(t, ids.New(), domain.DirectionInbound, "TR-1", ""), store.Idem{}); err != nil {
		t.Fatalf("same trailer at another site = %v", err)
	}
}

func TestGate_ConcurrentCheckInsOfOneTrailer(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(1)
	in := checkIn(t, ids.New(), domain.DirectionInbound, "TR-RACE", "")
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.CheckIn(ctx, in, store.Idem{})
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		} else if problemCode(t, err) != domain.CodeVisitState {
			t.Fatalf("unexpected error %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d concurrent check-ins succeeded, want 1", ok)
	}
}

// TestGate_AC3412BlockedAttemptIsLogged is AC-34.12 against Postgres: the
// outbound load sealed 778812 is blocked when the driver declares 778813, the
// gate log keeps the attempt, and the right seal then checks out with the
// AC-34.11 detention.
func TestGate_AC3412BlockedAttemptIsLogged(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	ctx := tenantCtx(1)
	v, err := s.CheckIn(ctx, checkIn(t, ids.New(), domain.DirectionOutbound, "TR-2", ""), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	out := time.Date(2026, 3, 2, 11, 20, 0, 0, time.UTC)
	if _, err := s.CheckOut(ctx, v.ID, "778812", out, dec("85"), store.Idem{}); problemCode(t, err) != domain.CodeSealMismatch {
		t.Fatalf("check-out before load confirmation = %v, want TMS-0129", err)
	}
	sealed, err := s.RecordSeal(ctx, v.ID, "778812", gateIn.Add(time.Hour))
	if err != nil || sealed.SealNo != "778812" || sealed.Version != 2 {
		t.Fatalf("seal = %+v, %v", sealed, err)
	}
	if _, err := s.CheckOut(ctx, v.ID, "778813", out, dec("85"), store.Idem{}); problemCode(t, err) != domain.CodeSealMismatch {
		t.Fatalf("wrong seal = %v, want TMS-0129", err)
	}
	still, log, err := s.GetGateVisit(ctx, v.ID)
	if err != nil || still.Status != domain.VisitInYard {
		t.Fatalf("blocked visit = %+v, %v", still, err)
	}
	if got := gateLogKinds(log); got != "CHECK_IN,CHECK_OUT_BLOCKED,SEALED,CHECK_OUT_BLOCKED" {
		t.Fatalf("gate log = %s", got)
	}
	if last := log[3].Detail; last["declaredSealNo"] != "778813" || last["recordedSealNo"] != "778812" || last["code"] != domain.CodeSealMismatch {
		t.Fatalf("blocked row = %+v", last)
	}

	done, err := s.CheckOut(ctx, v.ID, "778812", out, dec("85"), store.Idem{Key: "out-1", Fingerprint: "a"})
	if err != nil {
		t.Fatal(err)
	}
	d := done.Detention
	if done.Status != domain.VisitCheckedOut || !done.CheckedOutAt.Equal(out) || d == nil || d.DwellMin != 200 || d.DetentionMin != 80 ||
		d.Hours != 2 || !d.Charge.Equal(dec("170")) || d.Code != domain.CodeDetention {
		t.Fatalf("checked out = %+v detention %+v", done, d)
	}
	if replay, err := s.CheckOut(ctx, v.ID, "778812", out, dec("85"), store.Idem{Key: "out-1", Fingerprint: "a"}); err != nil || replay.Version != done.Version {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if _, err := s.CheckOut(ctx, v.ID, "778812", out, dec("85"), store.Idem{Key: "out-1", Fingerprint: "b"}); problemCode(t, err) != store.CodeIdempotencyReuse {
		t.Fatalf("reuse = %v", err)
	}
	if _, err := s.CheckOut(ctx, v.ID, "778812", out, dec("85"), store.Idem{}); problemCode(t, err) != domain.CodeVisitState {
		t.Fatalf("second check-out = %v, want TMS-0118", err)
	}
	if _, err := s.RecordSeal(ctx, v.ID, "1", out); problemCode(t, err) != domain.CodeVisitState {
		t.Fatalf("seal after check-out = %v, want TMS-0118", err)
	}
	if _, err := s.CheckOut(ctx, v.ID, "778812", gateIn.Add(-time.Hour), dec("0"), store.Idem{}); err == nil {
		t.Fatal("check-out of a closed visit accepted")
	}
	_, log, _ = s.GetGateVisit(ctx, v.ID)
	if got := gateLogKinds(log); got != "CHECK_IN,CHECK_OUT_BLOCKED,SEALED,CHECK_OUT_BLOCKED,CHECK_OUT" {
		t.Fatalf("gate log after check-out = %s", got)
	}
	// The trailer can come back once it has left.
	if _, err := s.CheckIn(ctx, checkIn(t, done.SiteID, domain.DirectionInbound, "TR-2", ""), store.Idem{}); err != nil {
		t.Fatalf("return visit = %v", err)
	}

	// The gate log is append-only for the application role and for the owner.
	appUpdate := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update gate_event set kind = 'CHECK_OUT' where visit_id = $1`, v.ID)
		return err
	})
	if appUpdate == nil {
		t.Fatal("application role updated the gate log")
	}
	if _, err := admin.Exec(context.Background(), `delete from gate_event where visit_id = $1`, v.ID); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("owner delete = %v, want the append-only trigger", err)
	}
}

func TestGate_CheckOutBeforeCheckInAndUnknownVisit(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(1)
	v, err := s.CheckIn(ctx, checkIn(t, ids.New(), domain.DirectionInbound, "TR-3", ""), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CheckOut(ctx, v.ID, "", gateIn.Add(-time.Minute), dec("0"), store.Idem{}); problemCode(t, err) != "COM-0001" {
		t.Fatalf("check-out before check-in = %v", err)
	}
	if _, err := s.CheckOut(ctx, ids.New(), "", gateIn, dec("0"), store.Idem{}); problemCode(t, err) != domain.CodeNotFound {
		t.Fatalf("unknown visit = %v", err)
	}
	// An unsealed inbound trailer leaves without a seal check.
	if done, err := s.CheckOut(ctx, v.ID, "", gateIn.Add(30*time.Minute), dec("0"), store.Idem{}); err != nil || done.Detention.DetentionMin != 0 {
		t.Fatalf("inbound check-out = %+v, %v", done, err)
	}
}

func TestGate_ListFiltersAndTenantIsolation(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(1)
	siteA, siteB := ids.New(), ids.New()
	a, err := s.CheckIn(ctx, checkIn(t, siteA, domain.DirectionInbound, "TR-A", ""), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	walk, err := domain.OpenVisit(domain.GateCheckIn{SiteID: siteB, Direction: domain.DirectionOutbound, CarrierSCAC: "XPOL", TrailerNo: "TR-B",
		DriverName: "B", DriverLicenseID: "L", ArrivedAt: gateIn.Add(24 * time.Hour), FreeTimeMin: 120, WalkInAllowed: true})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CheckIn(ctx, walk, store.Idem{})
	if err != nil || !b.WalkIn || b.Arrival != nil {
		t.Fatalf("walk-in = %+v, %v", b, err)
	}
	all, err := s.ListGateVisits(ctx, store.GateVisitFilter{})
	if err != nil || len(all) != 2 || all[0].ID != b.ID {
		t.Fatalf("all = %+v, %v", all, err)
	}
	for name, c := range map[string]struct {
		f    store.GateVisitFilter
		want string
	}{
		"site":    {store.GateVisitFilter{SiteID: siteA}, a.ID},
		"carrier": {store.GateVisitFilter{CarrierSCAC: "XPOL"}, b.ID},
		"date":    {store.GateVisitFilter{Date: "2026-03-02"}, a.ID},
		"status":  {store.GateVisitFilter{Status: domain.VisitInYard, Date: "2026-03-03"}, b.ID},
	} {
		got, err := s.ListGateVisits(ctx, c.f)
		if err != nil || len(got) != 1 || got[0].ID != c.want {
			t.Fatalf("%s filter = %+v, %v", name, got, err)
		}
	}
	other := tenantCtx(2)
	if _, _, err := s.GetGateVisit(other, a.ID); problemCode(t, err) != domain.CodeNotFound {
		t.Fatalf("tenant 2 get = %v", err)
	}
	if _, err := s.CheckOut(other, a.ID, "", gateIn.Add(time.Hour), dec("0"), store.Idem{}); problemCode(t, err) != domain.CodeNotFound {
		t.Fatalf("tenant 2 check-out = %v", err)
	}
	if list, _ := s.ListGateVisits(other, store.GateVisitFilter{}); len(list) != 0 {
		t.Fatalf("tenant 2 list = %+v", list)
	}
}
