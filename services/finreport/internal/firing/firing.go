// Package firing makes the scheduled statement runs of FR-15.8: for each
// schedule and ledger that is due, it runs the definition for the period the
// firing belongs to, renders the schedule's formats, stores the files in the
// report library and hands them to distribution through the outbox.
package firing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/outputs"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/store"
)

// Actor is the user id the job runs as. It is not a service principal: the job
// calls no other service and no route, like the lag check.
const Actor = "system:finreport-statement-schedules"

// MaxAttempts is the attempts made for one firing: one try and one retry
// (PRD §2.15.9 statement-schedules: "retry once").
const MaxAttempts = 2

// CodeFiringFailed is REPORT-0044, the code of a scheduled firing that failed
// for a reason that is not a problem of the run (a database or other error).
const CodeFiringFailed = "REPORT-0044"

// Stages a failed firing can stop at (statement.failed.v1 stage).
const (
	StagePeriod = "period"
	StageRun    = "run"
	StageRender = "render"
	StageStore  = "store"
)

// Store is what the job needs of the store.
type Store interface {
	DueFirings(ctx context.Context, now time.Time) ([]store.DueFiring, error)
	CreateRun(ctx context.Context, spec store.RunSpec) (store.Run, error)
	RunOutput(ctx context.Context, runID string) (store.Run, domain.Definition, []domain.Cell, error)
	RecordFiring(ctx context.Context, f store.FiringResult) (store.Firing, error)
	RecordFailure(ctx context.Context, f store.FiringFailure) (store.Firing, error)
}

// Runner fires due schedules.
type Runner struct {
	Store Store
	Now   func() time.Time
	// MaxLagSeconds is the lag gate a scheduled run is subject to (REPORTING_MAX_LAG_S).
	MaxLagSeconds int
	// RetentionYears is how long the library keeps the files (REPORTING_RETENTION_CLOSED_YEARS).
	RetentionYears int
}

// Result counts what one pass did.
type Result struct {
	Due       int `json:"due"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
}

// Run fires every schedule that is due now, in the tenant of ctx. A firing that
// fails is recorded and reported with statement.failed.v1 and does not stop the
// others; an error is returned only when the due list or a record cannot be
// written, since then the next pass would repeat the work.
func (r Runner) Run(ctx context.Context) (Result, error) {
	now := r.Now()
	due, err := r.Store.DueFirings(ctx, now)
	if err != nil {
		return Result{}, fmt.Errorf("list due firings: %w", err)
	}
	res := Result{Due: len(due)}
	var recordErr error
	for _, d := range due {
		f, err := r.fire(ctx, d)
		if err != nil {
			recordErr = errors.Join(recordErr, fmt.Errorf("record firing %s/%s: %w", d.Schedule.Code, d.LedgerID, err))
			continue
		}
		if f.Status == store.FiringCompleted {
			res.Completed++
		} else {
			res.Failed++
			slog.WarnContext(ctx, "scheduled statement failed", "schedule", f.ScheduleCode, "ledgerId", f.LedgerID,
				"fireTime", f.FireTime, "code", f.ErrorCode, "detail", f.ErrorDetail)
		}
	}
	return res, recordErr
}

// stepError is a failure with the stage it happened at and a problem code.
type stepError struct {
	stage, code, detail string
	runID               string
}

func (e *stepError) Error() string { return e.stage + ": " + e.code + ": " + e.detail }

func (r Runner) fire(ctx context.Context, d store.DueFiring) (store.Firing, error) {
	started := r.Now()
	period, err := d.Schedule.FiringPeriod(d.FireTime)
	if err != nil {
		return r.Store.RecordFailure(ctx, store.FiringFailure{Schedule: d.Schedule, LedgerID: d.LedgerID, FireTime: d.FireTime,
			Stage: StagePeriod, Code: domain.CodeScheduleInvalid, Detail: err.Error(), Attempts: 1, StartedAt: started})
	}
	var last *stepError
	for attempt := 1; attempt <= MaxAttempts; attempt++ {
		run, outs, serr := r.attempt(ctx, d, period)
		if serr == nil {
			res := store.FiringResult{Schedule: d.Schedule, LedgerID: d.LedgerID, FireTime: d.FireTime, Period: period, Run: run, Outputs: outs,
				RetainUntil: started.AddDate(r.RetentionYears, 0, 0), Attempts: attempt, StartedAt: started}
			f, err := r.Store.RecordFiring(ctx, res)
			if err == nil {
				return f, nil
			}
			serr = &stepError{stage: StageStore, code: problemCode(err, CodeFiringFailed), detail: err.Error(), runID: run.ID}
		}
		last = serr
		slog.WarnContext(ctx, "scheduled statement attempt failed", "schedule", d.Schedule.Code, "ledgerId", d.LedgerID,
			"attempt", attempt, "stage", serr.stage, "code", serr.code, "detail", serr.detail)
	}
	return r.Store.RecordFailure(ctx, store.FiringFailure{Schedule: d.Schedule, LedgerID: d.LedgerID, FireTime: d.FireTime, Period: period,
		RunID: last.runID, Stage: last.stage, Code: last.code, Detail: last.detail, Attempts: MaxAttempts, StartedAt: started})
}

// attempt runs the definition and renders the schedule's formats. The run's
// Idempotency-Key is made of the schedule, ledger and cron time, so the retry
// and a repeat of the job find the run the first attempt stored.
func (r Runner) attempt(ctx context.Context, d store.DueFiring, period domain.FiringPeriod) (store.Run, []store.FiringOutput, *stepError) {
	sc := d.Schedule
	key := fmt.Sprintf("schedule:%s:%s:%s", sc.Code, d.LedgerID, d.FireTime.UTC().Format(time.RFC3339))
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%s|%s", key, sc.Definition, sc.CurrencyType, period.Label(), sc.Timezone)))
	run, err := r.Store.CreateRun(ctx, store.RunSpec{
		Code: sc.Definition, LedgerID: d.LedgerID, CurrencyType: sc.CurrencyType, PeriodYear: period.Year, PeriodNo: period.No,
		Calendar: period.Calendar, RequestedBy: Actor, IdempotencyKey: key, RequestHash: hex.EncodeToString(sum[:]), MaxLagSeconds: r.MaxLagSeconds,
	})
	if err != nil {
		return store.Run{}, nil, &stepError{stage: StageRun, code: problemCode(err, CodeFiringFailed), detail: err.Error()}
	}
	run, def, cells, err := r.Store.RunOutput(ctx, run.ID)
	if err != nil {
		return run, nil, &stepError{stage: StageRun, code: problemCode(err, CodeFiringFailed), detail: err.Error(), runID: run.ID}
	}
	stamp := outputs.Stamp(run)
	var files []store.FiringOutput
	for _, format := range sc.Formats {
		out, ok := outputs.Formats[format]
		if !ok {
			continue // pdf: needs the renderer sidecar (NOT-IMPLEMENTED.md)
		}
		body, err := out.Render(def, cells, stamp)
		if err != nil {
			return run, nil, &stepError{stage: StageRender, code: "REPORT-0016", runID: run.ID,
				detail: fmt.Sprintf("%s rendering failed for run %s: %v", format, run.ID, err)}
		}
		files = append(files, store.FiringOutput{Format: format, ContentType: out.ContentType, Content: body,
			FileName: fmt.Sprintf("%s-%s-%s.%s", sc.Definition, ledgerTag(d.LedgerID), period.Label(), format)})
	}
	if len(files) == 0 {
		return run, nil, &stepError{stage: StageRender, code: "REPORT-0016", runID: run.ID,
			detail: "none of the schedule's formats can be rendered yet (pdf needs the renderer sidecar); add csv, html or xlsx"}
	}
	return run, files, nil
}

// ledgerTag is the short ledger id in a file name: its first eight characters.
func ledgerTag(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// problemCode is the code of a problem error, or def for any other error.
func problemCode(err error, def string) string {
	var p *httpx.Problem
	if errors.As(err, &p) && p.Code != "" {
		return p.Code
	}
	return def
}
