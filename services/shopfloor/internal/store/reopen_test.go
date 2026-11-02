package store

import (
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
)

// FR-25.3 through Postgres as passage_app: a finished job is reopened with a reason and the
// version the supervisor read; it returns to the dispatch list as started with its totals and
// check results, takes feedback again, and the finish still passes the mandatory-check gate.
// A reopening writes one audit row and one job.reopened event, a repeated key changes nothing,
// a stale version is SF-0143 and a job that is not finished is SF-0155.
func TestReopenJob_FinishedJobTakesFeedbackAgain(t *testing.T) {
	s, _, admin := setup(t)
	tenantID := testkit.TenantID(73)
	ctx := testkit.Ctx(tenantID)
	length := domain.Check{Code: "len", Name: "Length", Kind: domain.CheckNumeric, Min: decPtr("10"), Max: decPtr("12"), Mandatory: true}
	job := mustJob(t, s, ctx, ids.New(), 8, length)

	if _, err := s.ReopenJob(ctx, job.ID, job.Version, "not started", "sup-1", Idem{Key: "k0", Fingerprint: "f0"}); problemCode(err) != CodeJobNotFinished {
		t.Fatalf("reopen of a released job = %v, want %s", err, CodeJobNotFinished)
	}
	if _, err := s.RecordFeedback(ctx, job.ID, FeedbackInput{ID: ids.New(), GoodQty: decimal.NewFromInt(90), Finished: true,
		Checks: []domain.CheckInput{{Code: "len", Value: "11"}}, RecordedBy: "op-1"}); err != nil {
		t.Fatal(err)
	}
	done, err := s.GetJob(ctx, job.ID)
	if err != nil || done.Status != JobFinished || done.FinishedAt == nil {
		t.Fatalf("finished job = %+v, %v", done, err)
	}

	if _, err := s.ReopenJob(ctx, job.ID, done.Version-1, "late count", "sup-1", Idem{Key: "k1", Fingerprint: "f1"}); problemCode(err) != CodeVersionMismatch {
		t.Fatalf("stale version = %v, want %s", err, CodeVersionMismatch)
	}
	if _, err := s.ReopenJob(ctx, ids.New(), 1, "x", "sup-1", Idem{Key: "k1", Fingerprint: "f1"}); problemCode(err) != CodeJobNotFound {
		t.Fatalf("unknown job = %v, want %s", err, CodeJobNotFound)
	}
	idem := Idem{Key: "k2", Fingerprint: "f2"}
	reopened, err := s.ReopenJob(ctx, job.ID, done.Version, "Late count of 12 pieces found at the packing bench", "sup-1", idem)
	if err != nil || reopened.Status != JobStarted || reopened.FinishedAt != nil || reopened.Version != done.Version+1 ||
		reopened.GoodQty.String() != "90" || len(reopened.Results) != 1 || !reopened.Results[0].Pass {
		t.Fatalf("reopened = %+v, %v", reopened, err)
	}
	// The same version again is refused (the version moved), the repeated key is answered from the store.
	if _, err := s.ReopenJob(ctx, job.ID, done.Version, "again", "sup-1", Idem{Key: "k3", Fingerprint: "f3"}); problemCode(err) != CodeVersionMismatch {
		t.Fatalf("second reopen on the old version = %v", err)
	}
	if _, err := s.ReopenJob(ctx, job.ID, reopened.Version, "again", "sup-1", Idem{Key: "k3", Fingerprint: "f3"}); problemCode(err) != CodeJobNotFinished {
		t.Fatalf("reopen of an open job = %v, want %s", err, CodeJobNotFinished)
	}
	rep, err := s.LookupReplay(ctx, idem)
	if err != nil || rep == nil || rep.Status != 200 {
		t.Fatalf("stored outcome = %+v, %v", rep, err)
	}
	if n := countEvents(t, admin, tenantID, sfevents.JobReopened); n != 1 {
		t.Fatalf("job.reopened events = %d, want 1", n)
	}
	var data sfevents.JobReopenedData
	lastEventData(t, admin, tenantID, sfevents.JobReopened, &data)
	if data.JobID != job.ID || data.Reason != "Late count of 12 pieces found at the packing bench" || data.ReopenedBy != "sup-1" ||
		data.GoodQuantity.String() != "90" || !data.PreviousFinishedAt.Equal(*done.FinishedAt) || data.ReopeningID == "" {
		t.Fatalf("job.reopened = %+v", data)
	}

	if open, err := s.ListJobs(ctx, JobFilter{}); err != nil || len(open) != 1 || open[0].ID != job.ID {
		t.Fatalf("dispatch list after reopen = %+v, %v", open, err)
	}
	res, err := s.RecordFeedback(ctx, job.ID, FeedbackInput{ID: ids.New(), GoodQty: decimal.NewFromInt(12), RecordedBy: "op-1"})
	if err != nil || res.Job.GoodQty.String() != "102" || res.Job.Status != JobStarted {
		t.Fatalf("feedback after reopen = %+v, %v", res, err)
	}
	// A new out-of-limit value fails the mandatory check again, so the finish is blocked.
	if _, err := s.RecordFeedback(ctx, job.ID, FeedbackInput{ID: ids.New(), Finished: true, Checks: []domain.CheckInput{{Code: "len", Value: "13"}}}); problemCode(err) != CodeMandatoryCheck {
		t.Fatalf("finish after reopen = %v, want SF-0005", err)
	}
	if res, err := s.RecordFeedback(ctx, job.ID, FeedbackInput{ID: ids.New(), Finished: true, RecordedBy: "op-1"}); err != nil || res.Job.Status != JobFinished {
		t.Fatalf("second finish = %+v, %v", res, err)
	}
	// The second finish reports only what this posting adds (nothing); the totals include the 90
	// reported before the reopening, so a consumer that posts totals on a finish would count them twice.
	var second sfevents.JobReportedData
	lastEventData(t, admin, tenantID, sfevents.JobReported, &second)
	if !second.Finished || !second.QtyGood.IsZero() || !second.QtyScrap.IsZero() || second.GoodQuantity.String() != "102" {
		t.Fatalf("second finish job.reported = %+v", second)
	}
	if n := countEvents(t, admin, tenantID, sfevents.JobReported); n != 3 {
		t.Fatalf("job.reported events = %d, want 3 (the blocked finish publishes nothing)", n)
	}

	// Two reopenings are listed newest first, with the finish each one undid.
	last, err := s.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReopenJob(ctx, job.ID, last.Version, "second", "sup-2", Idem{Key: "k5", Fingerprint: "f5"}); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListJobReopenings(ctx, job.ID, 10)
	if err != nil || len(list) != 2 || list[0].ReopenedBy != "sup-2" || list[1].ReopenedBy != "sup-1" || list[1].GoodQty.String() != "90" || list[0].GoodQty.String() != "102" {
		t.Fatalf("reopenings = %+v, %v", list, err)
	}
	if _, err := s.ListJobReopenings(ctx, ids.New(), 10); problemCode(err) != CodeJobNotFound {
		t.Fatalf("reopenings of an unknown job = %v", err)
	}
	if other, err := s.ListJobReopenings(testkit.Ctx(testkit.TenantID(74)), job.ID, 10); problemCode(err) != CodeJobNotFound || len(other) != 0 {
		t.Fatalf("other tenant = %+v, %v", other, err)
	}
}
