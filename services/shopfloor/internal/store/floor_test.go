package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
)

func floorEvent(code string, seq int64, kind string, at time.Time, state string) MachineEventIn {
	return MachineEventIn{MachineCode: code, Event: domain.MachineEvent{Seq: seq, Kind: kind, At: at, State: state}}
}

func mustMachine(t *testing.T, s *Postgres, ctx context.Context, code, resource string) Machine {
	t.Helper()
	m, err := s.RegisterMachine(ctx, Machine{Code: code, ResourceID: resource, IdealCycleSeconds: decimal.NewFromInt(60)}, idem("machine-"+code))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// FR-25.9, FR-25.11, BR-25.4, BR-25.10, AC-25.6: a machine DOWN for 11 minutes
// raises one andon and emits andon.raised once; coding the reason keeps the
// alert open; the alert clears once the machine has run for 5 minutes.
// Duplicates are dropped and missing sequence runs are reported.
func TestFloor_IngestDowntimeAndonAndGaps(t *testing.T) {
	s, app, _ := setup(t)
	tenantID := testkit.TenantID(20)
	ctx := testkit.Ctx(tenantID)
	resource := ids.New()
	m := mustMachine(t, s, ctx, "M-101", resource)
	if _, err := s.RegisterMachine(ctx, Machine{Code: "M-102", ResourceID: resource, IdealCycleSeconds: decimal.NewFromInt(60)}, Idem{}); problemCode(err) != CodeMachineExists {
		t.Fatalf("second machine on one resource: %v", err)
	}
	if _, err := s.RegisterMachine(ctx, Machine{Code: "M-101", ResourceID: ids.New(), IdealCycleSeconds: decimal.NewFromInt(60)}, Idem{}); problemCode(err) != CodeMachineExists {
		t.Fatalf("duplicate machine code: %v", err)
	}

	t0 := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	at := func(min int) time.Time { return t0.Add(time.Duration(min) * time.Minute) }

	res, err := s.IngestMachineEvents(ctx, at(10), []MachineEventIn{
		floorEvent("M-101", 1, "state", at(0), domain.MachineRunning),
		{MachineCode: "M-101", Event: domain.MachineEvent{Seq: 2, Kind: "count", At: at(5), GoodCount: 5, RejectCount: 1}},
		floorEvent("M-101", 3, "state", at(10), domain.MachineDown),
	})
	if err != nil || res.Accepted != 3 || res.DowntimesOpen != 1 || res.AndonsRaised != 0 {
		t.Fatalf("batch1 = %+v, %v", res, err)
	}
	// Ten minutes down is not yet an andon; eleven is, even with no new event.
	if res, err := s.IngestMachineEvents(ctx, at(20), []MachineEventIn{floorEvent("M-101", 3, "state", at(10), domain.MachineDown)}); err != nil || res.AndonsRaised != 0 || res.Dropped != 1 {
		t.Fatalf("at 10 min down = %+v, %v", res, err)
	}
	res, err = s.IngestMachineEvents(ctx, at(21), []MachineEventIn{floorEvent("M-101", 4, "alarm", at(21), "")})
	if err != nil || res.AndonsRaised != 1 {
		t.Fatalf("at 11 min down = %+v, %v", res, err)
	}
	if res, err := s.IngestMachineEvents(ctx, at(22), []MachineEventIn{floorEvent("M-101", 5, "alarm", at(22), "")}); err != nil || res.AndonsRaised != 0 {
		t.Fatalf("second raise = %+v, %v", res, err)
	}
	open, err := s.OpenAndon(ctx, "")
	if err != nil || len(open) != 1 || open[0].ResourceID != resource || open[0].DowntimeID == nil {
		t.Fatalf("open andon = %+v, %v", open, err)
	}
	if n := countEvents(t, app, tenantID, sfevents.AndonRaised); n != 1 {
		t.Fatalf("andon.raised rows = %d, want 1", n)
	}

	downs, err := s.ListDowntimes(ctx, DowntimeFilter{OpenOnly: true, Limit: 10})
	if err != nil || len(downs) != 1 || !downs[0].AndonRaised || downs[0].MachineID != m.ID {
		t.Fatalf("open downtimes = %+v, %v", downs, err)
	}
	d, err := s.CodeDowntime(ctx, downs[0].ID, "JAM", "", idem("code-1"))
	if err != nil || d.ReasonCode == nil || *d.ReasonCode != "JAM" || d.EndedAt != nil || d.Category != "unplanned" {
		t.Fatalf("code downtime = %+v, %v", d, err)
	}
	if _, err := s.CodeDowntime(ctx, ids.New(), "JAM", "", Idem{}); problemCode(err) != CodeDowntimeNotFound {
		t.Fatalf("unknown downtime: %v", err)
	}
	if open, _ := s.OpenAndon(ctx, ""); len(open) != 1 {
		t.Fatalf("coding the reason cleared the alert: %+v", open)
	}

	// RUNNING at 32 min ends the downtime; the alert holds until 5 min clear.
	if _, err := s.IngestMachineEvents(ctx, at(33), []MachineEventIn{floorEvent("M-101", 6, "state", at(32), domain.MachineRunning)}); err != nil {
		t.Fatal(err)
	}
	if open, _ := s.OpenAndon(ctx, ""); len(open) != 1 {
		t.Fatalf("alert cleared too early: %+v", open)
	}
	// A replay of seq 6 and a count at 40 min: the duplicate is dropped, seq 7-8 are a gap, and the alert clears.
	res, err = s.IngestMachineEvents(ctx, at(40), []MachineEventIn{
		floorEvent("M-101", 6, "state", at(32), domain.MachineRunning),
		{MachineCode: "M-101", Event: domain.MachineEvent{Seq: 9, Kind: "count", At: at(40), GoodCount: 1}},
	})
	if err != nil || res.Dropped != 1 || res.Accepted != 1 || res.DowntimesOpen != 0 {
		t.Fatalf("batch at 40 = %+v, %v", res, err)
	}
	if len(res.Gaps) != 1 || res.Gaps[0] != (GapReport{MachineID: "M-101", From: 7, To: 8}) {
		t.Fatalf("gaps = %+v", res.Gaps)
	}
	if open, _ := s.OpenAndon(ctx, ""); len(open) != 0 {
		t.Fatalf("alert not cleared: %+v", open)
	}
	if downs, _ := s.ListDowntimes(ctx, DowntimeFilter{OpenOnly: true, Limit: 10}); len(downs) != 0 {
		t.Fatalf("downtime still open: %+v", downs)
	}

	// An unknown machine rejects the whole batch, including the known machine's events.
	if _, err := s.IngestMachineEvents(ctx, at(41), []MachineEventIn{
		floorEvent("M-101", 10, "state", at(41), domain.MachineIdle),
		floorEvent("M-999", 1, "state", at(41), domain.MachineRunning),
	}); problemCode(err) != CodeMachineNotFound {
		t.Fatalf("unknown machine: %v", err)
	}
	machines, err := s.ListMachines(ctx, 10)
	if err != nil || len(machines) != 1 || machines[0].LastSeq != 9 {
		t.Fatalf("machines = %+v, %v", machines, err)
	}

	// OEE stream for 08:00-08:40: RUNNING 0-10 (count at 5), DOWN 10-32, RUNNING 32-40.
	// The default idle threshold is 300 s and the next count comes at 40 (the
	// horizon), so 32-40 is 8 minutes without a count: IDLE (BR-25.6).
	streams, err := s.LoadOEEStreams(ctx, []string{resource, ids.New()}, t0, at(40))
	if err != nil || len(streams) != 1 {
		t.Fatalf("streams = %+v, %v", streams, err)
	}
	if st := streams[resource]; st.IdleThreshold != domain.DefaultIdleThreshold || !st.HistoryFrom.Equal(t0.Add(-5*time.Minute)) || !st.Horizon.Equal(at(40)) {
		t.Fatalf("idle threshold %s, history from %s, horizon %s", st.IdleThreshold, st.HistoryFrom, st.Horizon)
	}
	buckets, err := domain.BuildBuckets(streams[resource], t0, at(40), 40*time.Minute)
	if err != nil || len(buckets) != 1 {
		t.Fatalf("buckets = %+v, %v", buckets, err)
	}
	if !buckets[0].RunMinutes.Equal(decimal.NewFromInt(10)) || !buckets[0].PlannedMinutes.Equal(decimal.NewFromInt(40)) ||
		buckets[0].TotalCount != 6 || buckets[0].GoodCount != 5 {
		t.Fatalf("bucket = %+v", buckets[0])
	}
	// Coding the downtime as planned takes its 22 minutes out of planned time.
	if _, err := s.CodeDowntime(ctx, downs[0].ID, "CHANGEOVER", "planned", Idem{}); err != nil {
		t.Fatal(err)
	}
	streams, err = s.LoadOEEStreams(ctx, []string{resource}, t0, at(40))
	if err != nil || len(streams[resource].PlannedDown) != 1 {
		t.Fatalf("planned downtime = %+v, %v", streams, err)
	}
	if buckets, err = domain.BuildBuckets(streams[resource], t0, at(40), 40*time.Minute); err != nil || !buckets[0].PlannedMinutes.Equal(decimal.NewFromInt(18)) || !buckets[0].Availability.Equal(decimal.RequireFromString("0.5556")) {
		t.Fatalf("planned bucket = %+v, %v", buckets, err)
	}
	// A window starting mid-downtime takes the DOWN state in effect at its start.
	late, err := s.LoadOEEStreams(ctx, []string{resource}, at(20), at(40))
	if err != nil || late[resource].Initial != domain.MachineDown {
		t.Fatalf("initial state = %+v, %v", late, err)
	}

	// Another tenant sees none of it (RLS).
	other := testkit.Ctx(testkit.TenantID(22))
	if ms, _ := s.ListMachines(other, 10); len(ms) != 0 {
		t.Fatalf("machines leak across tenants: %+v", ms)
	}
	if _, err := s.IngestMachineEvents(other, at(42), []MachineEventIn{floorEvent("M-101", 11, "state", at(42), domain.MachineRunning)}); problemCode(err) != CodeMachineNotFound {
		t.Fatalf("other tenant ingest into M-101: %v", err)
	}
}

// FR-25.8: approved pay lines export once, with the batch id on every CSV row
// and the lines in payday.exported; a second export with nothing new is
// SF-0111; the batch is listed and its CSV can be read back unchanged.
func TestFloor_PayExportOnlyOnce(t *testing.T) {
	s, app, _ := setup(t)
	tenantID := testkit.TenantID(21)
	ctx := testkit.Ctx(tenantID)
	worker := mustWorker(t, s, ctx, "B-21", nil).ID
	if _, err := s.CreateRegistrations(ctx, []Registration{clock(worker, "clock-in", day(9, 0)), clock(worker, "clock-out", day(19, 30))}); err != nil {
		t.Fatal(err)
	}
	if err := calculate(t, s, ctx, worker, "2026-10-20", Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExportPay(ctx, "2026-10-20", "2026-10-20", "payroll-1", Idem{}); problemCode(err) != CodeNothingToExport {
		t.Fatalf("export before approval: %v", err)
	}
	if _, err := s.ApprovePay(ctx, worker, "2026-10-20", "2026-10-20", "supervisor-1", Idem{}); err != nil {
		t.Fatal(err)
	}

	first, err := s.ExportPay(ctx, "2026-10-19", "2026-10-25", "payroll-1", idem("export-1"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(first.CSV), "\n")
	if first.LineCount != 2 || len(lines) != 3 || lines[1] != first.BatchID+","+worker+",2026-10-20,base,8.00,1.000000,160.000000,USD" {
		t.Fatalf("export = %+v", first)
	}
	if _, err := s.ExportPay(ctx, "2026-10-19", "2026-10-25", "payroll-1", Idem{}); problemCode(err) != CodeNothingToExport {
		t.Fatalf("second export: %v, want %s", err, CodeNothingToExport)
	}
	if n := countEvents(t, app, tenantID, sfevents.PaydayExported); n != 1 {
		t.Fatalf("payday.exported rows = %d, want 1", n)
	}
	var lineCount int
	inTenant(t, app, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `select jsonb_array_length(payload->'data'->'lines') from outbox where type = $1`, sfevents.PaydayExported).Scan(&lineCount)
	})
	if lineCount != 2 {
		t.Fatalf("event lines = %d, want 2", lineCount)
	}

	list, err := s.ListPayExports(ctx, 10)
	if err != nil || len(list) != 1 || list[0].BatchID != first.BatchID || list[0].CSV != "" || list[0].CreatedBy != "payroll-1" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	got, err := s.GetPayExport(ctx, first.BatchID)
	if err != nil || got.CSV != first.CSV {
		t.Fatalf("read back = %+v, %v", got, err)
	}
	if _, err := s.GetPayExport(ctx, ids.New()); problemCode(err) != CodeExportNotFound {
		t.Fatalf("unknown batch: %v", err)
	}
}
