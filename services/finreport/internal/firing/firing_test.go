package firing

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/store"
)

const ledger = "22222222-2222-2222-2222-222222222222"

type fakeStore struct {
	due       []store.DueFiring
	runErrs   []error // one per CreateRun call, nil entries succeed
	runCalls  []store.RunSpec
	recorded  []store.FiringResult
	failures  []store.FiringFailure
	recordErr error
	failErr   error
}

func (f *fakeStore) DueFirings(context.Context, time.Time) ([]store.DueFiring, error) {
	return f.due, nil
}

func (f *fakeStore) CreateRun(_ context.Context, spec store.RunSpec) (store.Run, error) {
	f.runCalls = append(f.runCalls, spec)
	if n := len(f.runCalls) - 1; n < len(f.runErrs) && f.runErrs[n] != nil {
		return store.Run{}, f.runErrs[n]
	}
	return store.Run{ID: "11111111-1111-1111-1111-111111111111"}, nil
}

func (f *fakeStore) RunOutput(_ context.Context, id string) (store.Run, domain.Definition, []domain.Cell, error) {
	run := store.Run{ID: id, DefinitionCode: "BS-STD", DefinitionVersion: 2, LedgerID: ledger, CurrencyType: "acct", PeriodYear: 2026, PeriodNo: 3,
		ResultHash: "h", HWMByLedger: map[string]int64{ledger: 7}, StartedAt: time.Date(2026, 4, 1, 2, 0, 0, 0, time.UTC)}
	def := domain.Definition{Code: "BS-STD", Name: "Balance sheet",
		Rows:    []domain.Row{{Code: "CASH", Kind: domain.RowAccountRange, Label: "Cash", NormalBalance: "debit"}},
		Columns: []domain.Column{{Code: "CUR", Label: "Closing", PeriodExpr: "P"}}}
	return run, def, []domain.Cell{{Row: "CASH", Column: "CUR", Value: decimal.RequireFromString("12.5")}}, nil
}

func (f *fakeStore) RecordFiring(_ context.Context, r store.FiringResult) (store.Firing, error) {
	if f.recordErr != nil {
		return store.Firing{}, f.recordErr
	}
	f.recorded = append(f.recorded, r)
	return store.Firing{ScheduleCode: r.Schedule.Code, LedgerID: r.LedgerID, Status: store.FiringCompleted, Attempts: r.Attempts}, nil
}

func (f *fakeStore) RecordFailure(_ context.Context, r store.FiringFailure) (store.Firing, error) {
	if f.failErr != nil {
		return store.Firing{}, f.failErr
	}
	f.failures = append(f.failures, r)
	return store.Firing{ScheduleCode: r.Schedule.Code, LedgerID: r.LedgerID, Status: store.FiringFailed, ErrorCode: r.Code, ErrorDetail: r.Detail}, nil
}

func schedule(formats ...string) domain.Schedule {
	return domain.Schedule{Code: "NIGHTLY", Name: "Pack", Definition: "BS-STD", Cron: "0 2 1 * *", Timezone: "UTC", LedgerIDs: []string{ledger},
		CurrencyType: "acct", Formats: formats, Recipients: []string{"b@example.com", "a@example.com"}, AttachmentLimitMB: 20, Enabled: true, FiscalYearStartMonth: 1}
}

var fire = time.Date(2026, 4, 1, 2, 0, 0, 0, time.UTC)

func runner(st *fakeStore) Runner {
	return Runner{Store: st, Now: func() time.Time { return fire.Add(time.Minute) }, MaxLagSeconds: 30, RetentionYears: 7}
}

func TestRun_FiresTheClosedMonthAndStoresEachRenderableFormat(t *testing.T) {
	st := &fakeStore{due: []store.DueFiring{{Schedule: schedule("pdf", "csv", "xlsx"), LedgerID: ledger, FireTime: fire}}}
	res, err := runner(st).Run(context.Background())
	if err != nil || res != (Result{Due: 1, Completed: 1}) {
		t.Fatalf("res %+v err %v", res, err)
	}
	spec := st.runCalls[0]
	if spec.PeriodYear != 2026 || spec.PeriodNo != 3 || spec.Code != "BS-STD" || spec.LedgerID != ledger || spec.CurrencyType != "acct" ||
		spec.MaxLagSeconds != 30 || spec.AllowStale || spec.RequestedBy != Actor || len(spec.Calendar) != 39 {
		t.Fatalf("run spec %+v: an April firing runs March of its fiscal year", spec)
	}
	if spec.IdempotencyKey != "schedule:NIGHTLY:"+ledger+":2026-04-01T02:00:00Z" || spec.RequestHash == "" {
		t.Fatalf("idempotency key %q", spec.IdempotencyKey)
	}
	rec := st.recorded[0]
	var formats []string
	for _, o := range rec.Outputs {
		formats = append(formats, o.Format)
		if len(o.Content) == 0 || o.ContentType == "" || !strings.HasPrefix(o.FileName, "BS-STD-22222222-2026-03.") {
			t.Errorf("output %+v", o)
		}
	}
	if strings.Join(formats, ",") != "csv,xlsx" {
		t.Fatalf("formats %v: pdf is skipped, the rest keep the schedule's order", formats)
	}
	if want := time.Date(2033, 4, 1, 2, 1, 0, 0, time.UTC); !rec.RetainUntil.Equal(want) || rec.Attempts != 1 {
		t.Fatalf("retain until %v attempts %d", rec.RetainUntil, rec.Attempts)
	}
}

func TestRun_RetriesOnceThenSucceeds(t *testing.T) {
	st := &fakeStore{runErrs: []error{errors.New("connection reset")},
		due: []store.DueFiring{{Schedule: schedule("csv"), LedgerID: ledger, FireTime: fire}}}
	res, err := runner(st).Run(context.Background())
	if err != nil || res.Completed != 1 || len(st.runCalls) != 2 || st.recorded[0].Attempts != 2 {
		t.Fatalf("res %+v err %v calls %d", res, err, len(st.runCalls))
	}
	if st.runCalls[0].IdempotencyKey != st.runCalls[1].IdempotencyKey {
		t.Fatal("the retry must reuse the run's idempotency key")
	}
}

func TestRun_ASecondFailureIsRecordedWithTheProblemCode(t *testing.T) {
	stale := httpx.NewProblem(409, "REPORT-0004", "Read model is 90 s behind")
	st := &fakeStore{runErrs: []error{stale, stale},
		due: []store.DueFiring{
			{Schedule: schedule("csv"), LedgerID: ledger, FireTime: fire},
			{Schedule: func() domain.Schedule { s := schedule("csv"); s.Code = "OTHER"; return s }(), LedgerID: ledger, FireTime: fire},
		}}
	res, err := runner(st).Run(context.Background())
	if err != nil || res != (Result{Due: 2, Completed: 1, Failed: 1}) {
		t.Fatalf("res %+v err %v: one failing firing must not stop the next", res, err)
	}
	f := st.failures[0]
	if f.Code != "REPORT-0004" || f.Stage != StageRun || f.Attempts != 2 || f.Period.Label() != "2026-03" || !strings.Contains(f.Detail, "90 s") {
		t.Fatalf("failure %+v", f)
	}
}

func TestRun_AScheduleOfOnlyPDFFailsAtRender(t *testing.T) {
	st := &fakeStore{due: []store.DueFiring{{Schedule: schedule("pdf"), LedgerID: ledger, FireTime: fire}}}
	res, _ := runner(st).Run(context.Background())
	if res.Failed != 1 || st.failures[0].Stage != StageRender || st.failures[0].Code != "REPORT-0016" || st.failures[0].RunID == "" {
		t.Fatalf("res %+v failures %+v", res, st.failures)
	}
}

func TestRun_AnUnplaceableScheduleFailsAtPeriod(t *testing.T) {
	s := schedule("csv")
	s.Timezone = "Local"
	st := &fakeStore{due: []store.DueFiring{{Schedule: s, LedgerID: ledger, FireTime: fire}}}
	res, _ := runner(st).Run(context.Background())
	if res.Failed != 1 || st.failures[0].Stage != StagePeriod || len(st.runCalls) != 0 {
		t.Fatalf("res %+v calls %d failures %+v", res, len(st.runCalls), st.failures)
	}
}

func TestRun_ARecordErrorIsRetriedThenReportedAsAStoreFailure(t *testing.T) {
	st := &fakeStore{recordErr: errors.New("db down"), due: []store.DueFiring{{Schedule: schedule("csv"), LedgerID: ledger, FireTime: fire}}}
	// The store failure is retried as a store-stage failure, then recorded as such;
	// RecordFailure works in this fake, so the pass completes with one failed firing.
	res, err := runner(st).Run(context.Background())
	if err != nil || res.Failed != 1 || st.failures[0].Stage != StageStore || st.failures[0].Code != CodeFiringFailed {
		t.Fatalf("res %+v err %v failures %+v", res, err, st.failures)
	}
}

// When even the failure cannot be recorded the pass returns an error, so the
// job fails visibly and the next pass finds the firing still due.
func TestRun_AnUnrecordableFailureFailsThePass(t *testing.T) {
	st := &fakeStore{runErrs: []error{errors.New("x"), errors.New("x")}, failErr: errors.New("db down"),
		due: []store.DueFiring{{Schedule: schedule("csv"), LedgerID: ledger, FireTime: fire}}}
	if _, err := runner(st).Run(context.Background()); err == nil || !strings.Contains(err.Error(), "NIGHTLY") {
		t.Fatalf("err = %v", err)
	}
}
