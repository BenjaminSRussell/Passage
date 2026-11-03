package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
)

// FR-25.11, BR-25.10: a machine that goes DOWN and then sends nothing raises its andon from
// the sweep after 10 minutes, once, and the sweep clears it 5 minutes after the machine ran
// again, though no event arrived to trigger either.
func TestAndonSweep_SilentMachineRaisesAndClears(t *testing.T) {
	s, app, _ := setup(t)
	tenantID := testkit.TenantID(210)
	ctx := testkit.Ctx(tenantID)
	resource := ids.New()
	mustMachine(t, s, ctx, "M-201", resource)
	t0 := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	at := func(min int) time.Time { return t0.Add(time.Duration(min) * time.Minute) }

	if _, err := s.IngestMachineEvents(ctx, at(1), []MachineEventIn{floorEvent("M-201", 1, "state", at(0), domain.MachineDown)}); err != nil {
		t.Fatal(err)
	}
	if res, err := s.SweepAndon(ctx, at(10)); err != nil || res != (AndonSweepResult{}) {
		t.Fatalf("sweep at exactly 10 min = %+v, %v", res, err)
	}
	res, err := s.SweepAndon(ctx, at(11))
	if err != nil || res.DowntimeRaised != 1 || res.Cleared != 0 {
		t.Fatalf("sweep at 11 min = %+v, %v", res, err)
	}
	if res, err := s.SweepAndon(ctx, at(12)); err != nil || res != (AndonSweepResult{}) {
		t.Fatalf("second sweep = %+v, %v", res, err)
	}
	open, err := s.OpenAndon(ctx, "")
	if err != nil || len(open) != 1 || open[0].Kind != domain.AndonDowntime || open[0].DowntimeID == nil || !open[0].StartedAt.Equal(at(0)) {
		t.Fatalf("open andon = %+v, %v", open, err)
	}
	if n := countEvents(t, app, tenantID, sfevents.AndonRaised); n != 1 {
		t.Fatalf("andon.raised rows = %d, want 1", n)
	}
	downs, _ := s.ListDowntimes(ctx, DowntimeFilter{OpenOnly: true, Limit: 5})
	if len(downs) != 1 || !downs[0].AndonRaised {
		t.Fatalf("downtime not marked: %+v", downs)
	}

	// The machine runs again at 20 min; the alert holds for 5 minutes, then the sweep clears it.
	if _, err := s.IngestMachineEvents(ctx, at(20), []MachineEventIn{floorEvent("M-201", 2, "state", at(20), domain.MachineRunning)}); err != nil {
		t.Fatal(err)
	}
	if res, err := s.SweepAndon(ctx, at(24)); err != nil || res.Cleared != 0 {
		t.Fatalf("sweep at 4 min clear = %+v, %v", res, err)
	}
	if res, err := s.SweepAndon(ctx, at(25)); err != nil || res.Cleared != 1 {
		t.Fatalf("sweep at 5 min clear = %+v, %v", res, err)
	}
	if open, _ := s.OpenAndon(ctx, ""); len(open) != 0 {
		t.Fatalf("alert not cleared: %+v", open)
	}
}

func saveBucket(t *testing.T, s *Postgres, ctx context.Context, resource string, start time.Time, total, good int64) {
	t.Helper()
	b := domain.OEEBucket{Start: start, SourceEvents: 3, TotalCount: total, GoodCount: good, OEE: domain.OEE{
		PlannedMinutes: decimal.NewFromInt(15), RunMinutes: decimal.NewFromInt(15), Availability: decimal.NewFromInt(1),
		Performance: decimal.NewFromInt(1), Quality: decimal.NewFromInt(1), OEE: decimal.NewFromInt(1)}}
	_, err := s.SaveOEEBuckets(ctx, OEERollup{ResourceIDs: []string{resource}, BucketMinutes: 15, From: start, To: start.Add(15 * time.Minute),
		Rows: []OEERow{{ResourceID: resource, IdealCycleSeconds: decimal.NewFromInt(60), Bucket: b}}})
	if err != nil {
		t.Fatal(err)
	}
}

func saveBucket60(t *testing.T, s *Postgres, ctx context.Context, resource string, start time.Time, total, good int64) {
	t.Helper()
	b := domain.OEEBucket{Start: start, SourceEvents: 3, TotalCount: total, GoodCount: good, OEE: domain.OEE{
		PlannedMinutes: decimal.NewFromInt(60), RunMinutes: decimal.NewFromInt(60), Availability: decimal.NewFromInt(1),
		Performance: decimal.NewFromInt(1), Quality: decimal.NewFromInt(1), OEE: decimal.NewFromInt(1)}}
	_, err := s.SaveOEEBuckets(ctx, OEERollup{ResourceIDs: []string{resource}, BucketMinutes: 60, From: start, To: start.Add(time.Hour),
		Rows: []OEERow{{ResourceID: resource, IdealCycleSeconds: decimal.NewFromInt(60), Bucket: b}}})
	if err != nil {
		t.Fatal(err)
	}
}

// FR-25.11: more than 5 % scrap in the latest completed bucket raises one scrap rate alert per
// resource (BR-25.10); exactly 5 % and buckets under ten pieces do not; the alert clears 5
// minutes after a bucket back under the limit has ended.
func TestAndonSweep_ScrapRate(t *testing.T) {
	s, app, _ := setup(t)
	tenantID := testkit.TenantID(211)
	ctx := testkit.Ctx(tenantID)
	bad, edge, small := ids.New(), ids.New(), ids.New()
	site := ids.New()
	if _, err := s.RegisterMachine(ctx, Machine{Code: "M-BAD", ResourceID: bad, SiteID: &site, IdealCycleSeconds: decimal.NewFromInt(60)}, Idem{}); err != nil {
		t.Fatal(err)
	}
	mustMachine(t, s, ctx, "M-EDGE", edge)
	mustMachine(t, s, ctx, "M-SMALL", small)
	b0 := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	// A 60-minute bucket of the bad resource that ends later than its 15-minute bucket is still
	// running at 08:16, so the latest completed bucket stays the 15-minute one.
	saveBucket60(t, s, ctx, bad, b0, 100, 100)
	saveBucket(t, s, ctx, bad, b0, 100, 90)
	saveBucket(t, s, ctx, edge, b0, 100, 95)
	saveBucket(t, s, ctx, small, b0, 9, 0)

	// The bucket 08:00-08:15 is not complete at 08:14.
	if res, err := s.SweepAndon(ctx, b0.Add(14*time.Minute)); err != nil || res != (AndonSweepResult{}) {
		t.Fatalf("sweep inside the bucket = %+v, %v", res, err)
	}
	now := b0.Add(16 * time.Minute)
	res, err := s.SweepAndon(ctx, now)
	if err != nil || res.ScrapRateRaised != 1 {
		t.Fatalf("sweep = %+v, %v", res, err)
	}
	if res, err := s.SweepAndon(ctx, now.Add(time.Minute)); err != nil || res.ScrapRateRaised != 0 || res.Cleared != 0 {
		t.Fatalf("repeat = %+v, %v", res, err)
	}
	open, _ := s.OpenAndon(ctx, "")
	if len(open) != 1 || open[0].Kind != domain.AndonScrapRate || open[0].ResourceID != bad || open[0].SiteID == nil || *open[0].SiteID != site ||
		open[0].Detail == nil || !open[0].StartedAt.Equal(b0.Add(15*time.Minute)) {
		t.Fatalf("open = %+v", open)
	}
	var data sfevents.AndonRaisedData
	lastEventData(t, app, tenantID, sfevents.AndonRaised, &data)
	if data.Kind != domain.AndonScrapRate || data.ResourceID != bad {
		t.Fatalf("andon.raised = %+v", data)
	}

	// A good next bucket (08:15-08:30): the alert holds until 5 minutes after that bucket ended.
	saveBucket(t, s, ctx, bad, b0.Add(15*time.Minute), 100, 98)
	if res, err := s.SweepAndon(ctx, b0.Add(34*time.Minute)); err != nil || res.Cleared != 0 {
		t.Fatalf("sweep 4 min after the good bucket = %+v, %v", res, err)
	}
	if res, err := s.SweepAndon(ctx, b0.Add(35*time.Minute)); err != nil || res.Cleared != 1 {
		t.Fatalf("sweep 5 min after the good bucket = %+v, %v", res, err)
	}
	if open, _ := s.OpenAndon(ctx, ""); len(open) != 0 {
		t.Fatalf("open after clear = %+v", open)
	}
}

// FR-25.11: a failed quality check raises one quality fail alert on the job's resource, naming
// the job and the checks; a later posting that leaves no failed result clears it; a supervisor
// clears one by hand with a reason (SF-0162 once it is gone, SF-0163 for the other kinds).
func TestAndonQualityFail_RaisedByFeedbackClearedByPassOrSupervisor(t *testing.T) {
	s, app, _ := setup(t)
	tenantID := testkit.TenantID(212)
	ctx := testkit.Ctx(tenantID)
	resource := ids.New()
	mustMachine(t, s, ctx, "M-Q", resource)
	visual := domain.Check{Code: "visual", Name: "Visual", Kind: domain.CheckPassFail}
	job := mustJob(t, s, ctx, resource, 8, visual)

	if _, err := s.RecordFeedback(ctx, job.ID, FeedbackInput{ID: ids.New(), GoodQty: decimal.NewFromInt(1), Checks: []domain.CheckInput{{Code: "visual", Value: "pass"}}}); err != nil {
		t.Fatal(err)
	}
	if open, _ := s.OpenAndon(ctx, ""); len(open) != 0 {
		t.Fatalf("a passing check raised: %+v", open)
	}
	if _, err := s.RecordFeedback(ctx, job.ID, FeedbackInput{ID: ids.New(), GoodQty: decimal.NewFromInt(1), Checks: []domain.CheckInput{{Code: "visual", Value: "fail"}}}); err != nil {
		t.Fatal(err)
	}
	open, _ := s.OpenAndon(ctx, "")
	if len(open) != 1 || open[0].Kind != domain.AndonQualityFail || open[0].JobID == nil || *open[0].JobID != job.ID ||
		open[0].Detail == nil || *open[0].Detail != "job MO-1 OP-10: failed visual" {
		t.Fatalf("open = %+v", open)
	}
	var data sfevents.AndonRaisedData
	lastEventData(t, app, tenantID, sfevents.AndonRaised, &data)
	if data.Kind != domain.AndonQualityFail || data.JobID == nil || *data.JobID != job.ID {
		t.Fatalf("andon.raised = %+v", data)
	}
	// A second failure keeps the one open alert (BR-25.10).
	if _, err := s.RecordFeedback(ctx, job.ID, FeedbackInput{ID: ids.New(), Checks: []domain.CheckInput{{Code: "visual", Value: "fail"}}}); err != nil {
		t.Fatal(err)
	}
	if n := countEvents(t, app, tenantID, sfevents.AndonRaised); n != 1 {
		t.Fatalf("andon.raised rows = %d, want 1", n)
	}
	// A pass clears it.
	if _, err := s.RecordFeedback(ctx, job.ID, FeedbackInput{ID: ids.New(), Checks: []domain.CheckInput{{Code: "visual", Value: "pass"}}}); err != nil {
		t.Fatal(err)
	}
	if open, _ := s.OpenAndon(ctx, ""); len(open) != 0 {
		t.Fatalf("a passing re-check left the alert: %+v", open)
	}

	// Raise again and clear it as a supervisor.
	if _, err := s.RecordFeedback(ctx, job.ID, FeedbackInput{ID: ids.New(), Checks: []domain.CheckInput{{Code: "visual", Value: "fail"}}}); err != nil {
		t.Fatal(err)
	}
	open, _ = s.OpenAndon(ctx, "")
	if len(open) != 1 {
		t.Fatalf("open = %+v", open)
	}
	now := time.Now().UTC()
	out, err := s.ClearAndon(ctx, open[0].ID, "Scrapped the part, re-checked", "sup-1", now, idem("clear-1"))
	if err != nil || out.ID != open[0].ID {
		t.Fatalf("clear = %+v, %v", out, err)
	}
	var by, why string
	inTenant(t, app, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `select cleared_by, clear_reason from andon_alert where id = $1 and cleared_at is not null`, open[0].ID).Scan(&by, &why)
	})
	if by != "sup-1" || why != "Scrapped the part, re-checked" {
		t.Fatalf("audit = %q %q", by, why)
	}
	if _, err := s.ClearAndon(ctx, open[0].ID, "again", "sup-1", now, Idem{}); problemCode(err) != CodeAndonNotFound {
		t.Fatalf("clear twice = %v, want %s", err, CodeAndonNotFound)
	}
	if _, err := s.ClearAndon(ctx, ids.New(), "x", "sup-1", now, Idem{}); problemCode(err) != CodeAndonNotFound {
		t.Fatalf("unknown alert = %v", err)
	}

	// A downtime alert is not cleared by hand.
	t0 := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	if _, err := s.IngestMachineEvents(ctx, t0.Add(time.Minute), []MachineEventIn{floorEvent("M-Q", 1, "state", t0, domain.MachineDown)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SweepAndon(ctx, t0.Add(12*time.Minute)); err != nil {
		t.Fatal(err)
	}
	open, _ = s.OpenAndon(ctx, "")
	if len(open) != 1 || open[0].Kind != domain.AndonDowntime {
		t.Fatalf("open = %+v", open)
	}
	if _, err := s.ClearAndon(ctx, open[0].ID, "x", "sup-1", now, Idem{}); problemCode(err) != CodeAndonNotClearable {
		t.Fatalf("clear downtime = %v, want %s", err, CodeAndonNotClearable)
	}
}

// BR-25.10 keeps one quality fail alert per resource, so a second job failing while the first
// job's alert is open raises nothing. When the first job passes its re-check the alert clears and
// the second job's failure gets its own alert, and a supervisor's clear of that one carries
// nothing further.
func TestAndonQualityFail_OtherJobsFailureSurvivesTheClear(t *testing.T) {
	s, app, _ := setup(t)
	tenantID := testkit.TenantID(213)
	ctx := testkit.Ctx(tenantID)
	resource := ids.New()
	mustMachine(t, s, ctx, "M-Q2", resource)
	visual := domain.Check{Code: "visual", Name: "Visual", Kind: domain.CheckPassFail}
	a := mustJob(t, s, ctx, resource, 8, visual)
	b := mustJob(t, s, ctx, resource, 9, visual)
	post := func(job Job, value string) {
		t.Helper()
		if _, err := s.RecordFeedback(ctx, job.ID, FeedbackInput{ID: ids.New(), Checks: []domain.CheckInput{{Code: "visual", Value: value}}}); err != nil {
			t.Fatal(err)
		}
	}
	post(a, "fail")
	post(b, "fail")
	open, _ := s.OpenAndon(ctx, "")
	if len(open) != 1 || *open[0].JobID != a.ID {
		t.Fatalf("open after two failures = %+v", open)
	}
	post(a, "pass")
	open, _ = s.OpenAndon(ctx, "")
	if len(open) != 1 || open[0].JobID == nil || *open[0].JobID != b.ID || open[0].Detail == nil || *open[0].Detail != "job MO-1 OP-10: failed visual" {
		t.Fatalf("open after the first job passed = %+v", open)
	}
	if n := countEvents(t, app, tenantID, sfevents.AndonRaised); n != 2 {
		t.Fatalf("andon.raised rows = %d, want 2", n)
	}
	if _, err := s.ClearAndon(ctx, open[0].ID, "Part scrapped", "sup-1", time.Now().UTC(), Idem{}); err != nil {
		t.Fatal(err)
	}
	if open, _ := s.OpenAndon(ctx, ""); len(open) != 0 {
		t.Fatalf("open after the supervisor clear = %+v", open)
	}
	// A supervisor clear also carries a failure folded into the cleared alert.
	post(b, "fail")
	post(a, "fail")
	open, _ = s.OpenAndon(ctx, "")
	if len(open) != 1 || *open[0].JobID != b.ID {
		t.Fatalf("open = %+v", open)
	}
	if _, err := s.ClearAndon(ctx, open[0].ID, "Reworked", "sup-1", time.Now().UTC(), Idem{}); err != nil {
		t.Fatal(err)
	}
	open, _ = s.OpenAndon(ctx, "")
	if len(open) != 1 || *open[0].JobID != a.ID {
		t.Fatalf("open after clearing b's alert = %+v", open)
	}
}

// A job with many failed checks still raises its alert: the detail is cut to 500 characters
// instead of failing the feedback posting on the column limit.
func TestAndonQualityFail_LongDetailIsClipped(t *testing.T) {
	s, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(214))
	resource := ids.New()
	mustMachine(t, s, ctx, "M-Q3", resource)
	var checks []domain.Check
	var inputs []domain.CheckInput
	for i := 0; i < 20; i++ {
		code := fmt.Sprintf("dimension-check-%016d", i)
		checks = append(checks, domain.Check{Code: code, Name: code, Kind: domain.CheckPassFail})
		inputs = append(inputs, domain.CheckInput{Code: code, Value: "fail"})
	}
	job := mustJob(t, s, ctx, resource, 8, checks...)
	if _, err := s.RecordFeedback(ctx, job.ID, FeedbackInput{ID: ids.New(), Checks: inputs}); err != nil {
		t.Fatal(err)
	}
	open, _ := s.OpenAndon(ctx, "")
	if len(open) != 1 || open[0].Detail == nil || utf8.RuneCountInString(*open[0].Detail) != 500 || !strings.HasSuffix(*open[0].Detail, "…") {
		t.Fatalf("open = %+v", open)
	}
}
