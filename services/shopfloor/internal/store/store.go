// Package store persists time registrations, worker-days and pay lines in the
// shopfloor database. Every method runs in a tenant-scoped transaction so row
// level security applies, and every state change that other contexts consume
// is written to the outbox in the same transaction.
package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
)

// Catalog codes raised by the store (see README).
const (
	CodeWorkerNotFound = "SF-0101"
	CodeDayApproved    = "SF-0102"
	CodeNoDays         = "SF-0103"
	CodeDayStale       = "SF-0104"
	CodeBadgeTaken     = "SF-0105"
	CodeOwnApproval    = "SF-0106"
	CodeDayFlagged     = "SF-0006"

	StatusAccepted  = "accepted"
	StatusDuplicate = "duplicate"

	eventSource     = "shopfloor"
	dateLayout      = "2006-01-02"
	foreignKey      = "23503"
	uniqueViolation = "23505"
	workerSource    = "worker"
)

// Registration is one clock or job registration. ID is the client UUID.
type Registration struct {
	ID       string
	WorkerID string
	Kind     string
	JobID    *string
	BundleID *string
	// AbsenceCodeID names the absence code of an absence registration (FR-25.2).
	AbsenceCodeID *string
	QtyGood       decimal.Decimal
	QtyScrap      decimal.Decimal
	ScrapReasonID *string
	StartedAt     time.Time
	StoppedAt     *time.Time
	DeviceID      *string
	Offline       bool
	Note          *string
	// Hours is the elapsed time of a closed registration, or its share of a
	// bundle's time (BR-25.1); nil while open.
	Hours *decimal.Decimal
	// ClockOffsetSeconds is the device clock correction applied to the times
	// (server minus device, BR-25.7); nil when no correction was needed.
	ClockOffsetSeconds *int64
}

// RegistrationResult reports whether an item was stored or was a replay. For an
// accepted job-start it also carries the review flags stored on it, the jobs it
// stopped automatically and the matching catalog warnings (FR-25.3, BR-25.7). A
// replay changes nothing, so it carries none of them.
type RegistrationResult struct {
	ID          string     `json:"id"`
	Status      string     `json:"status"`
	Flags       []string   `json:"flags"`
	AutoStopped []AutoStop `json:"autoStopped"`
	Warnings    []Warning  `json:"warnings"`
}

// PunchInput is what a pay calculation reads for one worker and window.
type PunchInput struct {
	Punches  []domain.Punch
	Absences []domain.Absence
	Rate     decimal.Decimal
	Currency string
}

// PayCalculation is one calculated range ready to persist. Days holds only the
// days inside [FromDate, ToDate]; TimeZone is the zone they were built in.
type PayCalculation struct {
	WorkerID, FromDate, ToDate, TimeZone string
	Days                                 []domain.DayPay
}

// PayLine is a stored pay line as returned by approval.
type PayLine struct {
	Type       string          `json:"type"`
	Hours      decimal.Decimal `json:"hours"`
	Multiplier decimal.Decimal `json:"multiplier"`
	Amount     decimal.Decimal `json:"amount"`
	Currency   string          `json:"currency"`
	// Revision is the worker-day revision that added the line (0 for the original
	// calculation); Reversal marks a line that cancels an earlier one (AC-25.8).
	Revision int  `json:"revision"`
	Reversal bool `json:"reversal"`
}

// ApprovedDay is one worker-day in an approval response.
type ApprovedDay struct {
	WorkDate string          `json:"workDate"`
	Hours    decimal.Decimal `json:"hours"`
	Status   string          `json:"status"`
	// NewlyApproved is false when the day was already approved (idempotent replay).
	NewlyApproved bool      `json:"newlyApproved"`
	PayLines      []PayLine `json:"payLines"`
}

// Postgres is the shopfloor repository.
type Postgres struct {
	pool *pgxpool.Pool
}

// New returns a repository over pool.
func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Ping checks the database for readiness.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func enqueue(ctx context.Context, tx pgx.Tx, aggregateType, aggregateID, eventType, tenantID string, data any) error {
	evt, err := events.New(eventType, eventSource, aggregateID, tenantID, data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, aggregateType, aggregateID, evt)
}

func workerNotFound() error {
	return httpx.NewProblem(http.StatusUnprocessableEntity, CodeWorkerNotFound, "worker not found")
}

// mapForeignKey turns a missing worker reference into SF-0101.
func mapForeignKey(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == foreignKey {
		return workerNotFound()
	}
	return err
}

// CreateRegistrations stores a batch in one transaction. Items are written in
// client timestamp order (BR-25.7), so an offline replay posts its events in
// the order the work happened; results come back in request order. An item
// whose client UUID already exists is a no-op reported as duplicate; a new item
// emits dev.passage.shopfloor.registration.posted.v1 in the same transaction.
// A new job-start drives dispatch (FR-25.3): it is flagged job-finished-conflict
// when its job is already finished (BR-25.7), moves a released job to started,
// and, when open, auto-stops the worker's other open job or bundle.
func (s *Postgres) CreateRegistrations(ctx context.Context, items []Registration) ([]RegistrationResult, error) {
	p, _ := tenant.FromContext(ctx)
	order := make([]int, len(items))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return items[order[a]].StartedAt.Before(items[order[b]].StartedAt) })
	results := make([]RegistrationResult, len(items))
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := guardBundles(ctx, tx, p.TenantID, items); err != nil {
			return err
		}
		if err := lockJobWorkers(ctx, tx, p.TenantID, items); err != nil {
			return err
		}
		if err := checkAbsenceCodes(ctx, tx, p.TenantID, items); err != nil {
			return err
		}
		for _, i := range order {
			in := items[i]
			jobStatus, flags, err := jobStartFlags(ctx, tx, p.TenantID, in)
			if err != nil {
				return err
			}
			var hours *string
			if in.Hours != nil {
				h := in.Hours.String()
				hours = &h
			}
			var id string
			err = tx.QueryRow(ctx, `insert into time_registration (tenant_id, id, worker_id, kind, job_id, bundle_id, qty_good, qty_scrap, scrap_reason_id, started_at, stopped_at, device_id, offline, hours, note, clock_offset_s, flags, absence_code_id)
				select $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18
				where exists (select 1 from worker where tenant_id = $1 and id = $3 and active)
				on conflict (tenant_id, id) do nothing returning id::text`,
				p.TenantID, in.ID, in.WorkerID, in.Kind, in.JobID, in.BundleID, in.QtyGood.String(), in.QtyScrap.String(), in.ScrapReasonID,
				in.StartedAt, in.StoppedAt, in.DeviceID, in.Offline, hours, in.Note, in.ClockOffsetSeconds, flags, in.AbsenceCodeID).Scan(&id)
			if errors.Is(err, pgx.ErrNoRows) {
				// Either a replayed client UUID or an unknown/inactive worker.
				var exists bool
				if err := tx.QueryRow(ctx, `select exists (select 1 from time_registration where tenant_id = $1 and id = $2)`, p.TenantID, in.ID).Scan(&exists); err != nil {
					return err
				}
				if !exists {
					return workerNotFound()
				}
				results[i] = newResult(in.ID, StatusDuplicate, []string{})
				continue
			}
			if err != nil {
				return mapForeignKey(err)
			}
			res := newResult(in.ID, StatusAccepted, flags)
			if err := enqueue(ctx, tx, "registration", in.ID, sfevents.RegistrationPosted, p.TenantID, sfevents.RegistrationPostedData{
				RegistrationID: in.ID, WorkerID: in.WorkerID, Kind: in.Kind, JobID: in.JobID, BundleID: in.BundleID, AbsenceCodeID: in.AbsenceCodeID, Hours: in.Hours,
				QtyGood: in.QtyGood, QtyScrap: in.QtyScrap, Offline: in.Offline, Flags: flags,
			}); err != nil {
				return err
			}
			if len(flags) > 0 {
				res.Warnings = append(res.Warnings, Warning{Code: domain.CodeJobFinishedOffline,
					Message: fmt.Sprintf("Job %s was finished on the server while you were offline.", *in.JobID)})
			}
			if in.Kind == KindJobStart && in.JobID != nil {
				if err := applyJobStart(ctx, tx, p.TenantID, in, jobStatus, items, &res); err != nil {
					return err
				}
			}
			if err := supersedeAutoStop(ctx, tx, p.TenantID, in, items); err != nil {
				return err
			}
			results[i] = res
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

func newResult(id, status string, flags []string) RegistrationResult {
	return RegistrationResult{ID: id, Status: status, Flags: flags, AutoStopped: []AutoStop{}, Warnings: []Warning{}}
}

// guardBundles refuses closed bundle jobs when the bundle already holds closed
// jobs stored by another batch. The BR-25.1 split covers the jobs of one batch,
// so a second batch would count the bundle's time again (SF-0113). A resend whose
// items are all stored already is a replay and passes. Concurrent batches of one
// bundle serialize on a transaction advisory lock.
func guardBundles(ctx context.Context, tx pgx.Tx, tenantID string, items []Registration) error {
	byBundle := map[string][]string{}
	for _, in := range items {
		if in.BundleID != nil && in.StoppedAt != nil {
			byBundle[*in.BundleID] = append(byBundle[*in.BundleID], in.ID)
		}
	}
	bundles := make([]string, 0, len(byBundle))
	for b := range byBundle {
		bundles = append(bundles, b)
	}
	sort.Strings(bundles) // one lock order, so two batches cannot deadlock
	for _, b := range bundles {
		regIDs := byBundle[b]
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('shopfloor.bundle:' || $1 || ':' || $2, 0))`, tenantID, b); err != nil {
			return err
		}
		var others bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from time_registration where tenant_id = $1 and bundle_id = $2
			and stopped_at is not null and not (id = any($3::uuid[])))`, tenantID, b, regIDs).Scan(&others); err != nil {
			return err
		}
		if !others {
			continue
		}
		var stored int
		if err := tx.QueryRow(ctx, `select count(*) from time_registration where tenant_id = $1 and id = any($2::uuid[])`,
			tenantID, regIDs).Scan(&stored); err != nil {
			return err
		}
		if stored < len(regIDs) {
			return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeBundleInvalid,
				fmt.Sprintf("bundle %s already has closed jobs from another batch; send every job of a bundle in one batch", b))
		}
	}
	return nil
}

// lockWorker takes a row lock on the worker so calculation and approval for one
// worker serialize, and reports SF-0101 when the worker is unknown or inactive.
// It returns the worker's sign-in user id ("" when none is linked).
func lockWorker(ctx context.Context, tx pgx.Tx, tenantID, workerID string) (userID string, err error) {
	err = tx.QueryRow(ctx, `select coalesce(user_id, '') from worker where tenant_id = $1 and id = $2 and active for update`,
		tenantID, workerID).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", workerNotFound()
	}
	return userID, err
}

// LoadPunches reads the clock registrations of a worker in [start, end) and the
// worker's cost rate and currency. An auto clock-out without a correction (a
// supervisor's confirmation or edit) is marked unconfirmed (BR-25.3).
func (s *Postgres) LoadPunches(ctx context.Context, workerID string, start, end time.Time) (PunchInput, error) {
	p, _ := tenant.FromContext(ctx)
	var in PunchInput
	var rate string
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `select cost_rate::text, currency from worker where tenant_id = $1 and id = $2 and active`,
			p.TenantID, workerID).Scan(&rate, &in.Currency)
		if errors.Is(err, pgx.ErrNoRows) {
			return workerNotFound()
		}
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select t.id::text, t.kind, t.started_at,
				t.kind = 'clock-out' and $5 = any(t.flags)
				and not exists (select 1 from registration_correction c where c.tenant_id = t.tenant_id and c.registration_id = t.id)
			from time_registration t
			where t.tenant_id = $1 and t.worker_id = $2 and t.kind in ('clock-in', 'clock-out') and t.started_at >= $3 and t.started_at < $4
			order by t.started_at, t.id`, p.TenantID, workerID, start, end, domain.FlagAutoClockOut)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var pu domain.Punch
			if err := rows.Scan(&pu.ID, &pu.Kind, &pu.At, &pu.Unconfirmed); err != nil {
				return err
			}
			in.Punches = append(in.Punches, pu)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		in.Absences, err = loadAbsences(ctx, tx, p.TenantID, workerID, start, end)
		return err
	})
	if err != nil {
		return PunchInput{}, err
	}
	in.Rate, err = decimal.NewFromString(rate)
	return in, err
}

// SavePay replaces the calculated worker-days and pay lines of a worker in
// [FromDate, ToDate] and stores response under the Idempotency-Key. Approved
// days are never rewritten: if any day in the range is approved the call fails
// with SF-0102 (a correction with reversal is required, BR-25.8).
func (s *Postgres) SavePay(ctx context.Context, calc PayCalculation, idem Idem, response any) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := lockWorker(ctx, tx, p.TenantID, calc.WorkerID); err != nil {
			return err
		}
		var currency string
		if err := tx.QueryRow(ctx, `select currency from worker where tenant_id = $1 and id = $2`, p.TenantID, calc.WorkerID).Scan(&currency); err != nil {
			return err
		}
		var approved int
		if err := tx.QueryRow(ctx, `select count(*) from worker_day where tenant_id = $1 and worker_id = $2
			and work_date between $3::date and $4::date and status = 'approved'`, p.TenantID, calc.WorkerID, calc.FromDate, calc.ToDate).Scan(&approved); err != nil {
			return err
		}
		if approved > 0 {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeDayApproved, "approved days cannot be recalculated; a correction is required")
		}
		if _, err := tx.Exec(ctx, `delete from pay_line where tenant_id = $1 and worker_id = $2 and work_date between $3::date and $4::date`,
			p.TenantID, calc.WorkerID, calc.FromDate, calc.ToDate); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from worker_day where tenant_id = $1 and worker_id = $2 and work_date between $3::date and $4::date`,
			p.TenantID, calc.WorkerID, calc.FromDate, calc.ToDate); err != nil {
			return err
		}
		for _, d := range calc.Days {
			if err := insertDay(ctx, tx, p.TenantID, calc.WorkerID, currency, calc.TimeZone, d); err != nil {
				return err
			}
		}
		if idem.Key != "" {
			return remember(ctx, tx, idem, http.StatusOK, response)
		}
		return nil
	})
}

// insertDay stores a calculated worker-day and its pay lines.
func insertDay(ctx context.Context, tx pgx.Tx, tenantID, workerID, currency, zone string, d domain.DayPay) error {
	flags := d.Flags
	if flags == nil {
		flags = []string{}
	}
	if _, err := tx.Exec(ctx, `insert into worker_day (tenant_id, worker_id, work_date, hours, flags, time_zone, status)
		values ($1, $2, $3::date, $4, $5, $6, 'calculated')`, tenantID, workerID, d.Date.Format(dateLayout), d.Hours.String(), flags, zone); err != nil {
		return err
	}
	for _, l := range d.Lines {
		sources := l.Sources
		if sources == nil {
			sources = []string{}
		}
		if _, err := tx.Exec(ctx, `insert into pay_line (tenant_id, id, worker_id, work_date, pay_type, hours, rate_multiplier, amount, currency, source_registration_ids)
			values ($1, $2, $3, $4::date, $5, $6, $7, $8, $9, $10::text[]::uuid[])`,
			tenantID, ids.New(), workerID, d.Date.Format(dateLayout), l.Type, l.Hours.String(), l.Multiplier.String(), l.Amount.String(), currency, sources); err != nil {
			return err
		}
	}
	return nil
}

// ApproveResult is the outcome of an approval, stored for Idempotency-Key replays.
type ApproveResult struct {
	WorkerID string        `json:"workerId"`
	From     string        `json:"from"`
	To       string        `json:"to"`
	Days     []ApprovedDay `json:"days"`
}

// ApprovePay approves every calculated day in [fromDate, toDate] for a worker,
// then emits dev.passage.shopfloor.payday.approved.v1 per newly approved day.
// Days already approved are returned unchanged, so a replay is a no-op. The
// whole approval fails when any day has an open flag (SF-0006), when clock
// registrations for a calculated day arrived after its calculation (SF-0104,
// recalculate first) or when the approver is the worker (SF-0106).
func (s *Postgres) ApprovePay(ctx context.Context, workerID, fromDate, toDate, approvedBy string, idem Idem) (ApproveResult, error) {
	p, _ := tenant.FromContext(ctx)
	out := ApproveResult{WorkerID: workerID, From: fromDate, To: toDate, Days: []ApprovedDay{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		userID, err := lockWorker(ctx, tx, p.TenantID, workerID)
		if err != nil {
			return err
		}
		if userID != "" && userID == approvedBy {
			return httpx.NewProblem(http.StatusForbidden, CodeOwnApproval, "you cannot approve your own time registrations")
		}
		type dayRow struct {
			date, hours, status string
			flags               []string
			stale               bool
		}
		var rowsIn []dayRow
		rows, err := tx.Query(ctx, `select d.work_date::text, d.hours::text, d.flags, d.status,
				d.status = 'calculated' and exists (
					select 1 from time_registration r
					where r.tenant_id = d.tenant_id and r.worker_id = d.worker_id and r.kind in ('clock-in', 'clock-out', 'absence')
					  and r.started_at >= (d.work_date::timestamp at time zone d.time_zone)
					  and r.started_at < ((d.work_date + 1)::timestamp at time zone d.time_zone)
					  and (r.server_received_at > d.calculated_at
					       or exists (select 1 from registration_correction c where c.tenant_id = r.tenant_id and c.registration_id = r.id and c.corrected_at > d.calculated_at)))
					or (d.status = 'calculated' and exists (
					select 1 from registration_correction c join time_registration r on r.tenant_id = c.tenant_id and r.id = c.registration_id
					where c.tenant_id = d.tenant_id and c.worker_id = d.worker_id and r.kind in ('clock-in', 'clock-out', 'absence') and c.corrected_at > d.calculated_at
					  and ((c.old_started_at >= (d.work_date::timestamp at time zone d.time_zone) and c.old_started_at < ((d.work_date + 1)::timestamp at time zone d.time_zone))
					    or (c.new_started_at >= (d.work_date::timestamp at time zone d.time_zone) and c.new_started_at < ((d.work_date + 1)::timestamp at time zone d.time_zone)))))
			from worker_day d
			where d.tenant_id = $1 and d.worker_id = $2 and d.work_date between $3::date and $4::date
			order by d.work_date for update of d`,
			p.TenantID, workerID, fromDate, toDate)
		if err != nil {
			return err
		}
		for rows.Next() {
			var r dayRow
			if err := rows.Scan(&r.date, &r.hours, &r.flags, &r.status, &r.stale); err != nil {
				rows.Close()
				return err
			}
			rowsIn = append(rowsIn, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(rowsIn) == 0 {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeNoDays, "no calculated days in the range")
		}
		open := 0
		var stale []string
		for _, r := range rowsIn {
			open += len(r.flags)
			if r.stale {
				stale = append(stale, r.date)
			}
		}
		if open > 0 {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeDayFlagged, fmt.Sprintf("Day cannot be approved: %d flags open.", open))
		}
		if len(stale) > 0 {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeDayStale,
				fmt.Sprintf("registrations arrived after calculation on %s; recalculate before approving", strings.Join(stale, ", ")))
		}
		for _, r := range rowsIn {
			day := ApprovedDay{WorkDate: r.date, Status: r.status, PayLines: []PayLine{}}
			day.Hours, err = decimal.NewFromString(r.hours)
			if err != nil {
				return err
			}
			if day.PayLines, err = loadLines(ctx, tx, p.TenantID, workerID, r.date); err != nil {
				return err
			}
			if r.status == "calculated" {
				if _, err := tx.Exec(ctx, `update worker_day set status = 'approved', approved_by = $4, approved_at = now()
					where tenant_id = $1 and worker_id = $2 and work_date = $3::date`, p.TenantID, workerID, r.date, approvedBy); err != nil {
					return err
				}
				day.Status, day.NewlyApproved = "approved", true
				if err := enqueue(ctx, tx, workerSource, workerID, sfevents.PaydayApproved, p.TenantID, paydayData(workerID, day, approvedBy)); err != nil {
					return err
				}
			}
			out.Days = append(out.Days, day)
		}
		if idem.Key != "" {
			return remember(ctx, tx, idem, http.StatusOK, out)
		}
		return nil
	})
	return out, err
}

func loadLines(ctx context.Context, tx pgx.Tx, tenantID, workerID, date string) ([]PayLine, error) {
	rows, err := tx.Query(ctx, `select pay_type, hours::text, rate_multiplier::text, amount::text, currency, revision, reversal_of is not null from pay_line
		where tenant_id = $1 and worker_id = $2 and work_date = $3::date order by revision, pay_type, id`, tenantID, workerID, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	lines := []PayLine{}
	for rows.Next() {
		var typ, hours, mult, amount, currency string
		var revision int
		var reversal bool
		if err := rows.Scan(&typ, &hours, &mult, &amount, &currency, &revision, &reversal); err != nil {
			return nil, err
		}
		l, err := parseLine(typ, hours, mult, amount, currency)
		if err != nil {
			return nil, err
		}
		l.Revision, l.Reversal = revision, reversal
		lines = append(lines, l)
	}
	return lines, rows.Err()
}

func parseLine(typ, hours, mult, amount, currency string) (PayLine, error) {
	l := PayLine{Type: typ, Currency: currency}
	var err error
	if l.Hours, err = decimal.NewFromString(hours); err != nil {
		return PayLine{}, err
	}
	if l.Multiplier, err = decimal.NewFromString(mult); err != nil {
		return PayLine{}, err
	}
	if l.Amount, err = decimal.NewFromString(amount); err != nil {
		return PayLine{}, err
	}
	return l, nil
}

func paydayData(workerID string, d ApprovedDay, approvedBy string) sfevents.PaydayApprovedData {
	data := sfevents.PaydayApprovedData{WorkerID: workerID, WorkDate: d.WorkDate, PayLines: []sfevents.PayLineData{}, ApprovedBy: approvedBy}
	for _, l := range d.PayLines {
		data.PayLines = append(data.PayLines, sfevents.PayLineData{Type: l.Type, Hours: l.Hours, Multiplier: l.Multiplier, Amount: l.Amount, Currency: l.Currency})
	}
	return data
}
