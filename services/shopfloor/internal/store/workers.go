package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// Worker is the shop floor projection of an employee (PRD 4.25.4).
type Worker struct {
	ID        string          `json:"id"`
	PartyID   string          `json:"partyId"`
	Name      string          `json:"name"`
	BadgeID   *string         `json:"badgeId"`
	UserID    *string         `json:"userId"`
	CostRate  decimal.Decimal `json:"costRate"`
	Currency  string          `json:"currency"`
	Active    bool            `json:"active"`
	Version   int             `json:"version"`
	CreatedAt time.Time       `json:"createdAt"`
	// ProfileID and PayAgreementID link the worker to a time profile and a pay
	// agreement (FR-25.7); nil means the calculation defaults.
	ProfileID      *string `json:"profileId"`
	PayAgreementID *string `json:"payAgreementId"`
	// DefaultResourceID is the resource (machine or work centre) the worker normally runs; the
	// dispatch list for the worker ("my jobs", GET jobs?workerId=) is that resource's jobs. Nil
	// means the worker sees every job.
	DefaultResourceID *string `json:"defaultResourceId"`
}

const workerColumns = `id::text, party_id::text, name, badge_id, user_id, cost_rate::text, currency, active, version, created_at, profile_id::text, pay_agreement_id::text, default_resource_id::text`

func scanWorker(row pgx.Row) (Worker, error) {
	var w Worker
	var rate string
	if err := row.Scan(&w.ID, &w.PartyID, &w.Name, &w.BadgeID, &w.UserID, &rate, &w.Currency, &w.Active, &w.Version, &w.CreatedAt, &w.ProfileID, &w.PayAgreementID, &w.DefaultResourceID); err != nil {
		return Worker{}, err
	}
	var err error
	w.CostRate, err = decimal.NewFromString(rate)
	return w, err
}

// CreateWorker registers a worker and stores the outcome under the
// Idempotency-Key. A badge already used by another worker is SF-0105.
func (s *Postgres) CreateWorker(ctx context.Context, in Worker, idem Idem) (Worker, error) {
	p, _ := tenant.FromContext(ctx)
	var out Worker
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanWorker(tx.QueryRow(ctx, `insert into worker (tenant_id, id, party_id, name, badge_id, user_id, cost_rate, currency)
			values ($1, $2, $3, $4, $5, $6, $7, $8) returning `+workerColumns,
			p.TenantID, ids.New(), in.PartyID, in.Name, in.BadgeID, in.UserID, in.CostRate.String(), in.Currency))
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return httpx.NewProblem(http.StatusConflict, CodeBadgeTaken, "badge is already assigned to another worker")
		}
		if err != nil {
			return err
		}
		return remember(ctx, tx, idem, http.StatusCreated, out)
	})
	return out, err
}

// GetWorker returns one worker, or SF-0101 (404) when it does not exist.
func (s *Postgres) GetWorker(ctx context.Context, id string) (Worker, error) {
	var out Worker
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanWorker(tx.QueryRow(ctx, `select `+workerColumns+` from worker where id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeWorkerNotFound, "worker not found")
		}
		return err
	})
	return out, err
}

// ListWorkers returns workers ordered by name, at most limit rows. activeOnly
// hides deactivated workers.
func (s *Postgres) ListWorkers(ctx context.Context, activeOnly bool, limit int) ([]Worker, error) {
	out := []Worker{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+workerColumns+` from worker where ($1 = false or active) order by name, id limit $2`, activeOnly, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			w, err := scanWorker(rows)
			if err != nil {
				return err
			}
			out = append(out, w)
		}
		return rows.Err()
	})
	return out, err
}

// RegistrationView is a stored registration as listed for review.
type RegistrationView struct {
	ID               string           `json:"id"`
	WorkerID         string           `json:"workerId"`
	Kind             string           `json:"kind"`
	JobID            *string          `json:"jobId"`
	BundleID         *string          `json:"bundleId"`
	AbsenceCodeID    *string          `json:"absenceCodeId"`
	QtyGood          decimal.Decimal  `json:"qtyGood"`
	QtyScrap         decimal.Decimal  `json:"qtyScrap"`
	ScrapReasonID    *string          `json:"scrapReasonId"`
	StartedAt        time.Time        `json:"startedAt"`
	StoppedAt        *time.Time       `json:"stoppedAt"`
	Hours            *decimal.Decimal `json:"hours"`
	ServerReceivedAt time.Time        `json:"serverReceivedAt"`
	DeviceID         *string          `json:"deviceId"`
	Offline          bool             `json:"offline"`
	Note             *string          `json:"note"`
	// ClockOffsetSeconds is the device clock correction applied (BR-25.7).
	ClockOffsetSeconds *int64 `json:"clockOffsetSeconds"`
	// Flags are review flags: job-finished-conflict (BR-25.7), auto-stopped and
	// auto-stop-capped (FR-25.3).
	Flags []string `json:"flags"`
}

// ListRegistrations returns a worker's registrations that start in [start, end),
// oldest first, at most limit rows.
func (s *Postgres) ListRegistrations(ctx context.Context, workerID string, start, end time.Time, limit int) ([]RegistrationView, error) {
	return s.listRegistrations(ctx, `worker_id = $1 and started_at >= $2 and started_at < $3`, limit, workerID, start, end)
}

// ListBundleRegistrations returns every registration of one bundle (BR-25.1),
// oldest first, at most limit rows.
func (s *Postgres) ListBundleRegistrations(ctx context.Context, bundleID string, limit int) ([]RegistrationView, error) {
	return s.listRegistrations(ctx, `bundle_id = $1`, limit, bundleID)
}

// listRegistrations reads registrations matching where; its arguments are $1..$n
// and the limit is appended as the last parameter.
func (s *Postgres) listRegistrations(ctx context.Context, where string, limit int, args ...any) ([]RegistrationView, error) {
	out := []RegistrationView{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, fmt.Sprintf(`select id::text, worker_id::text, kind, job_id::text, bundle_id::text, absence_code_id::text, qty_good::text, qty_scrap::text,
				scrap_reason_id, started_at, stopped_at, hours::text, server_received_at, device_id, offline, note, clock_offset_s, flags
			from time_registration where %s
			order by started_at, id limit $%d`, where, len(args)+1), append(args, limit)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r RegistrationView
			var good, scrap string
			var hours *string
			if err := rows.Scan(&r.ID, &r.WorkerID, &r.Kind, &r.JobID, &r.BundleID, &r.AbsenceCodeID, &good, &scrap, &r.ScrapReasonID,
				&r.StartedAt, &r.StoppedAt, &hours, &r.ServerReceivedAt, &r.DeviceID, &r.Offline, &r.Note, &r.ClockOffsetSeconds, &r.Flags); err != nil {
				return err
			}
			if r.QtyGood, err = decimal.NewFromString(good); err != nil {
				return err
			}
			if r.QtyScrap, err = decimal.NewFromString(scrap); err != nil {
				return err
			}
			if hours != nil {
				h, err := decimal.NewFromString(*hours)
				if err != nil {
					return err
				}
				r.Hours = &h
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// WorkerDay is a calculated or approved worker-day with its pay lines.
type WorkerDay struct {
	WorkerID     string          `json:"workerId"`
	WorkerName   string          `json:"workerName"`
	WorkDate     string          `json:"workDate"`
	Hours        decimal.Decimal `json:"hours"`
	Flags        []string        `json:"flags"`
	Status       string          `json:"status"`
	TimeZone     string          `json:"timeZone"`
	CalculatedAt time.Time       `json:"calculatedAt"`
	ApprovedBy   *string         `json:"approvedBy"`
	ApprovedAt   *time.Time      `json:"approvedAt"`
	// Revision counts the supervisor revisions of an approved day (AC-25.8); its pay
	// lines then include the reversal lines.
	Revision  int        `json:"revision"`
	RevisedBy *string    `json:"revisedBy"`
	RevisedAt *time.Time `json:"revisedAt"`
	PayLines  []PayLine  `json:"payLines"`
}

// DayFilter selects worker-days for the approval list. WorkerID and Status are optional.
type DayFilter struct {
	WorkerID, Status, FromDate, ToDate string
	Limit                              int
}

// ListWorkerDays returns worker-days in [FromDate, ToDate] ordered by date and
// worker name, each with its pay lines.
func (s *Postgres) ListWorkerDays(ctx context.Context, f DayFilter) ([]WorkerDay, error) {
	p, _ := tenant.FromContext(ctx)
	out := []WorkerDay{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select d.worker_id::text, w.name, d.work_date::text, d.hours::text, d.flags, d.status, d.time_zone,
				d.calculated_at, d.approved_by, d.approved_at, d.revision, d.revised_by, d.revised_at
			from worker_day d join worker w on w.tenant_id = d.tenant_id and w.id = d.worker_id
			where d.work_date between $1::date and $2::date
			  and ($3 = '' or d.worker_id::text = $3) and ($4 = '' or d.status = $4)
			order by d.work_date, w.name, d.worker_id limit $5`, f.FromDate, f.ToDate, f.WorkerID, f.Status, f.Limit)
		if err != nil {
			return err
		}
		for rows.Next() {
			var d WorkerDay
			var hours string
			if err := rows.Scan(&d.WorkerID, &d.WorkerName, &d.WorkDate, &hours, &d.Flags, &d.Status, &d.TimeZone,
				&d.CalculatedAt, &d.ApprovedBy, &d.ApprovedAt, &d.Revision, &d.RevisedBy, &d.RevisedAt); err != nil {
				rows.Close()
				return err
			}
			if d.Hours, err = decimal.NewFromString(hours); err != nil {
				rows.Close()
				return err
			}
			out = append(out, d)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		// One query for the lines of every listed day (no per-day round trip).
		index := make(map[string]int, len(out))
		for i := range out {
			out[i].PayLines = []PayLine{}
			index[out[i].WorkerID+"|"+out[i].WorkDate] = i
		}
		lines, err := tx.Query(ctx, `select worker_id::text, work_date::text, pay_type, hours::text, rate_multiplier::text, amount::text, currency, revision, reversal_of is not null
			from pay_line where tenant_id = $1 and work_date between $2::date and $3::date and ($4 = '' or worker_id::text = $4)
			order by worker_id, work_date, revision, pay_type, id`, p.TenantID, f.FromDate, f.ToDate, f.WorkerID)
		if err != nil {
			return err
		}
		defer lines.Close()
		for lines.Next() {
			var worker, date, typ, hours, mult, amount, currency string
			var revision int
			var reversal bool
			if err := lines.Scan(&worker, &date, &typ, &hours, &mult, &amount, &currency, &revision, &reversal); err != nil {
				return err
			}
			i, ok := index[worker+"|"+date]
			if !ok {
				continue
			}
			l, err := parseLine(typ, hours, mult, amount, currency)
			if err != nil {
				return err
			}
			l.Revision, l.Reversal = revision, reversal
			out[i].PayLines = append(out[i].PayLines, l)
		}
		return lines.Err()
	})
	return out, err
}
