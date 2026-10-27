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
	ID            string
	WorkerID      string
	Kind          string
	JobID         *string
	BundleID      *string
	QtyGood       decimal.Decimal
	QtyScrap      decimal.Decimal
	ScrapReasonID *string
	StartedAt     time.Time
	StoppedAt     *time.Time
	DeviceID      *string
	Offline       bool
	Note          *string
	// Hours is the elapsed time of a closed registration; nil while open.
	Hours *decimal.Decimal
}

// RegistrationResult reports whether an item was stored or was a replay.
type RegistrationResult struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// PunchInput is what a pay calculation reads for one worker and window.
type PunchInput struct {
	Punches  []domain.Punch
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
func (s *Postgres) CreateRegistrations(ctx context.Context, items []Registration) ([]RegistrationResult, error) {
	p, _ := tenant.FromContext(ctx)
	order := make([]int, len(items))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return items[order[a]].StartedAt.Before(items[order[b]].StartedAt) })
	results := make([]RegistrationResult, len(items))
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		for _, i := range order {
			in := items[i]
			var hours *string
			if in.Hours != nil {
				h := in.Hours.String()
				hours = &h
			}
			var id string
			err := tx.QueryRow(ctx, `insert into time_registration (tenant_id, id, worker_id, kind, job_id, bundle_id, qty_good, qty_scrap, scrap_reason_id, started_at, stopped_at, device_id, offline, hours, note)
				select $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15
				where exists (select 1 from worker where tenant_id = $1 and id = $3 and active)
				on conflict (tenant_id, id) do nothing returning id::text`,
				p.TenantID, in.ID, in.WorkerID, in.Kind, in.JobID, in.BundleID, in.QtyGood.String(), in.QtyScrap.String(), in.ScrapReasonID,
				in.StartedAt, in.StoppedAt, in.DeviceID, in.Offline, hours, in.Note).Scan(&id)
			if errors.Is(err, pgx.ErrNoRows) {
				// Either a replayed client UUID or an unknown/inactive worker.
				var exists bool
				if err := tx.QueryRow(ctx, `select exists (select 1 from time_registration where tenant_id = $1 and id = $2)`, p.TenantID, in.ID).Scan(&exists); err != nil {
					return err
				}
				if !exists {
					return workerNotFound()
				}
				results[i] = RegistrationResult{ID: in.ID, Status: StatusDuplicate}
				continue
			}
			if err != nil {
				return mapForeignKey(err)
			}
			results[i] = RegistrationResult{ID: in.ID, Status: StatusAccepted}
			if err := enqueue(ctx, tx, "registration", in.ID, sfevents.RegistrationPosted, p.TenantID, sfevents.RegistrationPostedData{
				RegistrationID: in.ID, WorkerID: in.WorkerID, Kind: in.Kind, JobID: in.JobID, Hours: in.Hours,
				QtyGood: in.QtyGood, QtyScrap: in.QtyScrap, Offline: in.Offline,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
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
// worker's cost rate and currency.
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
		rows, err := tx.Query(ctx, `select id::text, kind, started_at from time_registration
			where tenant_id = $1 and worker_id = $2 and kind in ('clock-in', 'clock-out') and started_at >= $3 and started_at < $4
			order by started_at, id`, p.TenantID, workerID, start, end)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var pu domain.Punch
			if err := rows.Scan(&pu.ID, &pu.Kind, &pu.At); err != nil {
				return err
			}
			in.Punches = append(in.Punches, pu)
		}
		return rows.Err()
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
			flags := d.Flags
			if flags == nil {
				flags = []string{}
			}
			if _, err := tx.Exec(ctx, `insert into worker_day (tenant_id, worker_id, work_date, hours, flags, time_zone, status)
				values ($1, $2, $3::date, $4, $5, $6, 'calculated')`, p.TenantID, calc.WorkerID, d.Date.Format(dateLayout), d.Hours.String(), flags, calc.TimeZone); err != nil {
				return err
			}
			for _, l := range d.Lines {
				sources := l.Sources
				if sources == nil {
					sources = []string{}
				}
				if _, err := tx.Exec(ctx, `insert into pay_line (tenant_id, id, worker_id, work_date, pay_type, hours, rate_multiplier, amount, currency, source_registration_ids)
					values ($1, $2, $3, $4::date, $5, $6, $7, $8, $9, $10::text[]::uuid[])`,
					p.TenantID, ids.New(), calc.WorkerID, d.Date.Format(dateLayout), l.Type, l.Hours.String(), l.Multiplier.String(), l.Amount.String(), currency, sources); err != nil {
					return err
				}
			}
		}
		if idem.Key != "" {
			return remember(ctx, tx, idem, http.StatusOK, response)
		}
		return nil
	})
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
					where r.tenant_id = d.tenant_id and r.worker_id = d.worker_id and r.kind in ('clock-in', 'clock-out')
					  and r.started_at >= (d.work_date::timestamp at time zone d.time_zone)
					  and r.started_at < ((d.work_date + 1)::timestamp at time zone d.time_zone)
					  and r.server_received_at > d.calculated_at)
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
	rows, err := tx.Query(ctx, `select pay_type, hours::text, rate_multiplier::text, amount::text, currency from pay_line
		where tenant_id = $1 and worker_id = $2 and work_date = $3::date order by pay_type`, tenantID, workerID, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	lines := []PayLine{}
	for rows.Next() {
		var typ, hours, mult, amount, currency string
		if err := rows.Scan(&typ, &hours, &mult, &amount, &currency); err != nil {
			return nil, err
		}
		l, err := parseLine(typ, hours, mult, amount, currency)
		if err != nil {
			return nil, err
		}
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
