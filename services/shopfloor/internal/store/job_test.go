package store

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
)

func decPtr(s string) *decimal.Decimal {
	d := decimal.RequireFromString(s)
	return &d
}

func mustJob(t *testing.T, s *Postgres, ctx context.Context, resource string, start int, checks ...domain.Check) Job {
	t.Helper()
	job, created, err := s.UpsertJob(ctx, Job{ID: ids.New(), OrderRef: "MO-1", OperationRef: "OP-10", ResourceID: resource,
		PlannedQty: decimal.NewFromInt(100), SequenceStart: day(start, 0), Checks: checks})
	if err != nil || !created {
		t.Fatalf("upsert = %+v, created %v, %v", job, created, err)
	}
	return job
}

// FR-25.3 to FR-25.5 and AC-25.9 through Postgres as passage_app: the dispatch list
// in start order, partial feedback with a replay that changes nothing, a mandatory
// numeric check (10 to 12) with value 13 that blocks finished (SF-0005) and stores
// nothing, the supervisor override with reason, and feedback refused once finished.
func TestJobs_DispatchFeedbackAndFinishGate(t *testing.T) {
	s, _, admin := setup(t)
	tenantID := testkit.TenantID(70)
	ctx := testkit.Ctx(tenantID)
	resource := ids.New()
	length := domain.Check{Code: "len", Name: "Length", Kind: domain.CheckNumeric, Min: decPtr("10"), Max: decPtr("12"), Mandatory: true}
	visual := domain.Check{Code: "visual", Name: "Visual", Kind: domain.CheckPassFail}
	late := mustJob(t, s, ctx, resource, 10)
	job := mustJob(t, s, ctx, resource, 8, length, visual)
	other := mustJob(t, s, ctx, ids.New(), 7)

	// The schedule re-sends the plan: plan fields change, version goes up, nothing else.
	moved, created, err := s.UpsertJob(ctx, Job{ID: job.ID, OrderRef: "MO-1", OperationRef: "OP-20", ResourceID: resource,
		PlannedQty: decimal.RequireFromString("120.5"), SequenceStart: day(8, 0), Checks: []domain.Check{length, visual}})
	if err != nil || created || moved.OperationRef != "OP-20" || moved.PlannedQty.String() != "120.5" || moved.Version != job.Version+1 || moved.Status != JobReleased {
		t.Fatalf("re-plan = %+v, created %v, %v", moved, created, err)
	}

	list, err := s.ListJobs(ctx, JobFilter{ResourceID: resource})
	if err != nil || len(list) != 2 || list[0].ID != job.ID || list[1].ID != late.ID {
		t.Fatalf("dispatch list = %+v, %v", list, err)
	}

	partial := ids.New()
	res, err := s.RecordFeedback(ctx, job.ID, FeedbackInput{ID: partial, GoodQty: decimal.NewFromInt(4),
		Checks: []domain.CheckInput{{Code: "len", Value: "11"}, {Code: "visual", Value: "fail"}}, RecordedBy: "op-1"})
	if err != nil || res.Duplicate || res.Job.Status != JobStarted || res.Job.GoodQty.String() != "4" || len(res.Job.Results) != 2 {
		t.Fatalf("partial feedback = %+v, %v", res, err)
	}
	replay, err := s.RecordFeedback(ctx, job.ID, FeedbackInput{ID: partial, GoodQty: decimal.NewFromInt(4), RecordedBy: "op-1"})
	if err != nil || !replay.Duplicate || replay.Job.GoodQty.String() != "4" {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if _, err := s.RecordFeedback(ctx, other.ID, FeedbackInput{ID: partial, GoodQty: decimal.NewFromInt(1)}); problemCode(err) != CodeFeedbackOther {
		t.Fatalf("feedback id on another job = %v, want %s", err, CodeFeedbackOther)
	}
	if n := countEvents(t, admin, tenantID, sfevents.JobReported); n != 1 {
		t.Fatalf("job.reported events after a replay = %d, want 1", n)
	}

	// AC-25.9: 13 is outside 10 to 12, so finished is refused and nothing is stored.
	blocked := ids.New()
	_, err = s.RecordFeedback(ctx, job.ID, FeedbackInput{ID: blocked, GoodQty: decimal.NewFromInt(5), Finished: true,
		Checks: []domain.CheckInput{{Code: "len", Value: "13"}}, RecordedBy: "op-1"})
	if problemCode(err) != CodeMandatoryCheck {
		t.Fatalf("blocked finish = %v, want SF-0005", err)
	}
	after, err := s.GetJob(ctx, job.ID)
	if err != nil || after.GoodQty.String() != "4" || after.Status != JobStarted || after.Results[0].Value != "11" || !after.Results[0].Pass {
		t.Fatalf("a refused finish changed the job: %+v, %v", after, err)
	}
	if _, err := s.RecordFeedback(ctx, job.ID, FeedbackInput{ID: ids.New(), Checks: []domain.CheckInput{{Code: "nope", Value: "1"}}}); problemCode(err) != CodeCheckUnknown {
		t.Fatalf("unknown check = %v", err)
	}
	if _, err := s.RecordFeedback(ctx, job.ID, FeedbackInput{ID: ids.New(), Checks: []domain.CheckInput{{Code: "len", Value: "ten"}}}); problemCode(err) != CodeCheckValue {
		t.Fatalf("malformed value = %v", err)
	}

	// The supervisor resends the same posting with an override and a reason.
	reason := "Measured again on the CMM: 11.9"
	scrapReason := "BURR"
	res, err = s.RecordFeedback(ctx, job.ID, FeedbackInput{ID: blocked, GoodQty: decimal.NewFromInt(5), ScrapQty: decimal.RequireFromString("1.5"),
		ScrapReasonID: &scrapReason, Finished: true, Checks: []domain.CheckInput{{Code: "len", Value: "13"}}, OverrideReason: &reason, RecordedBy: "sup-1"})
	if err != nil || res.Job.Status != JobFinished || res.Job.FinishedAt == nil || res.Job.GoodQty.String() != "9" || res.Job.ScrapQty.String() != "1.5" {
		t.Fatalf("override finish = %+v, %v", res, err)
	}
	var data sfevents.JobReportedData
	lastEventData(t, admin, tenantID, sfevents.JobReported, &data)
	if data.JobID != job.ID || data.FeedbackID != blocked || !data.Finished || data.OverrideReason == nil || *data.OverrideReason != reason ||
		data.QtyGood.String() != "5" || data.GoodQuantity.String() != "9" || data.RecordedBy != "sup-1" ||
		len(data.FailedChecks) != 2 || data.FailedChecks[0] != "len" || data.FailedChecks[1] != "visual" {
		t.Fatalf("job.reported = %+v", data)
	}
	var stored, by string
	inTenant(t, admin, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `select checks->0->>'value', recorded_by from job_feedback where id = $1`, blocked).Scan(&stored, &by)
	})
	if stored != "13" || by != "sup-1" {
		t.Fatalf("audit row: value %q by %q", stored, by)
	}

	if open, err := s.ListJobs(ctx, JobFilter{}); err != nil || len(open) != 2 {
		t.Fatalf("finished job must leave the dispatch list: %d, %v", len(open), err)
	}
	if done, err := s.ListJobs(ctx, JobFilter{Status: JobFinished}); err != nil || len(done) != 1 || done[0].ID != job.ID {
		t.Fatalf("finished filter = %+v, %v", done, err)
	}
	if _, err := s.RecordFeedback(ctx, job.ID, FeedbackInput{ID: ids.New(), GoodQty: decimal.NewFromInt(1)}); problemCode(err) != CodeJobFinished {
		t.Fatalf("feedback on a finished job = %v, want %s", err, CodeJobFinished)
	}
	if _, err := s.RecordFeedback(ctx, ids.New(), FeedbackInput{ID: ids.New(), Finished: true}); problemCode(err) != CodeJobNotFound {
		t.Fatalf("unknown job = %v", err)
	}
}

// Jobs, feedback and results are invisible to another tenant (forced RLS).
func TestJobs_TenantIsolation(t *testing.T) {
	s, _, _ := setup(t)
	a, b := testkit.Ctx(testkit.TenantID(71)), testkit.Ctx(testkit.TenantID(72))
	job := mustJob(t, s, a, ids.New(), 8)
	if _, err := s.GetJob(b, job.ID); problemCode(err) != CodeJobNotFound {
		t.Fatalf("other tenant read = %v", err)
	}
	if list, err := s.ListJobs(b, JobFilter{}); err != nil || len(list) != 0 {
		t.Fatalf("other tenant list = %+v, %v", list, err)
	}
	if _, err := s.RecordFeedback(b, job.ID, FeedbackInput{ID: ids.New(), GoodQty: decimal.NewFromInt(1)}); problemCode(err) != CodeJobNotFound {
		t.Fatalf("other tenant feedback = %v", err)
	}
}
