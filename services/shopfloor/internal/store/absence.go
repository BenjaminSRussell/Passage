package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
)

// Absence code problem codes (see README).
const (
	CodeAbsenceNotFound = "SF-0145"
	CodeAbsenceInactive = "SF-0146"
	// CodeAbsenceTermsLocked refuses a change of paid or payMultiplier on a code that an
	// absence registration already names (409): recalculating or revising a past day must pay
	// it as it was registered. Create a new code and deactivate the old one instead.
	CodeAbsenceTermsLocked = "SF-0147"

	KindAbsence   = domain.KindAbsence
	absenceSource = "absencecode"
)

// AbsenceCode says whether absence time is paid and at what share of the worker's rate.
type AbsenceCode struct {
	ID            string          `json:"id"`
	Code          string          `json:"code"`
	Name          string          `json:"name"`
	Paid          bool            `json:"paid"`
	PayMultiplier decimal.Decimal `json:"payMultiplier"`
	Active        bool            `json:"active"`
	Version       int             `json:"version"`
	CreatedBy     string          `json:"createdBy"`
	CreatedAt     time.Time       `json:"createdAt"`
	UpdatedAt     time.Time       `json:"updatedAt"`
}

// AbsenceCodeInput is what a create or update writes. The code never changes after creation.
type AbsenceCodeInput struct {
	Code          string
	Name          string
	Paid          bool
	PayMultiplier decimal.Decimal
	Active        bool
}

func absenceNotFound(status int) error {
	return httpx.NewProblem(status, CodeAbsenceNotFound, "absence code not found")
}

const absenceColumns = `id::text, code, name, paid, pay_multiplier::text, active, version, created_by, created_at, updated_at`

func scanAbsenceCode(row pgx.Row) (AbsenceCode, error) {
	var a AbsenceCode
	var mult string
	if err := row.Scan(&a.ID, &a.Code, &a.Name, &a.Paid, &mult, &a.Active, &a.Version, &a.CreatedBy, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return AbsenceCode{}, err
	}
	var err error
	a.PayMultiplier, err = decimal.NewFromString(mult)
	return a, err
}

func absenceEvent(ctx context.Context, tx pgx.Tx, tenantID string, a AbsenceCode, by string) error {
	return enqueue(ctx, tx, absenceSource, a.ID, sfevents.AbsenceCodeSaved, tenantID, sfevents.AbsenceCodeSavedData{
		AbsenceCodeID: a.ID, Code: a.Code, Name: a.Name, Paid: a.Paid, PayMultiplier: a.PayMultiplier.String(),
		Active: a.Active, Version: a.Version, SavedBy: by})
}

// CreateAbsenceCode stores an absence code under the Idempotency-Key and publishes
// dev.passage.shopfloor.absencecode.saved.v1 in the same transaction. A code already used
// is SF-0142.
func (s *Postgres) CreateAbsenceCode(ctx context.Context, in AbsenceCodeInput, createdBy string, idem Idem) (AbsenceCode, error) {
	p, _ := tenant.FromContext(ctx)
	var out AbsenceCode
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanAbsenceCode(tx.QueryRow(ctx, `insert into absence_code (tenant_id, id, code, name, paid, pay_multiplier, active, created_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8) returning `+absenceColumns,
			p.TenantID, ids.New(), in.Code, in.Name, in.Paid, in.PayMultiplier.String(), in.Active, createdBy))
		if err != nil {
			return codeTaken(err)
		}
		if err := absenceEvent(ctx, tx, p.TenantID, out, createdBy); err != nil {
			return err
		}
		return remember(ctx, tx, idem, http.StatusCreated, out)
	})
	return out, err
}

// UpdateAbsenceCode replaces a code's name, pay terms and status when version is the stored
// version (SF-0143, 412) and bumps the version. The pay terms (paid, payMultiplier) cannot change
// once an absence registration names the code (SF-0147, 409), so a recalculated or revised day
// pays its absences on the terms they were registered under; name and status always can. The
// code row is locked first, and a new absence locks it for share (checkAbsenceCodes), so a
// registration cannot slip in between the check and the update.
func (s *Postgres) UpdateAbsenceCode(ctx context.Context, id string, version int, in AbsenceCodeInput, updatedBy string) (AbsenceCode, error) {
	p, _ := tenant.FromContext(ctx)
	var out AbsenceCode
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanAbsenceCode(tx.QueryRow(ctx, `select `+absenceColumns+` from absence_code where tenant_id = $1 and id = $2 for update`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return absenceNotFound(http.StatusNotFound)
		}
		if err != nil {
			return err
		}
		if cur.Version != version {
			return versionMismatch("absence code")
		}
		if cur.Paid != in.Paid || !cur.PayMultiplier.Equal(in.PayMultiplier) {
			var used bool
			if err := tx.QueryRow(ctx, `select exists (select 1 from time_registration where tenant_id = $1 and absence_code_id = $2)`, p.TenantID, id).Scan(&used); err != nil {
				return err
			}
			if used {
				return httpx.NewProblem(http.StatusConflict, CodeAbsenceTermsLocked, "absence code is in use: its paid flag and pay multiplier cannot change; create a new code")
			}
		}
		out, err = scanAbsenceCode(tx.QueryRow(ctx, `update absence_code set name = $3, paid = $4, pay_multiplier = $5, active = $6, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 returning `+absenceColumns,
			p.TenantID, id, in.Name, in.Paid, in.PayMultiplier.String(), in.Active))
		if err != nil {
			return err
		}
		return absenceEvent(ctx, tx, p.TenantID, out, updatedBy)
	})
	return out, err
}

// GetAbsenceCode returns an absence code, or SF-0145 (404).
func (s *Postgres) GetAbsenceCode(ctx context.Context, id string) (AbsenceCode, error) {
	var out AbsenceCode
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanAbsenceCode(tx.QueryRow(ctx, `select `+absenceColumns+` from absence_code where id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return absenceNotFound(http.StatusNotFound)
		}
		return err
	})
	return out, err
}

// ListAbsenceCodes returns absence codes by code, at most limit rows.
func (s *Postgres) ListAbsenceCodes(ctx context.Context, activeOnly bool, limit int) ([]AbsenceCode, error) {
	out := []AbsenceCode{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+absenceColumns+` from absence_code where ($1 = false or active) order by code, id limit $2`, activeOnly, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanAbsenceCode(rows)
			if err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}

// checkAbsenceCodes refuses an absence registration whose code does not exist (SF-0145, 422)
// or is inactive (SF-0146, 422); a replay of a stored registration stays a duplicate even after
// its code was deactivated. The code row is locked for share until the batch commits, so a
// concurrent change of its pay terms waits and then sees the new absence (SF-0147).
func checkAbsenceCodes(ctx context.Context, tx pgx.Tx, tenantID string, items []Registration) error {
	seen := map[string]bool{}
	for _, in := range items {
		if in.Kind != KindAbsence || in.AbsenceCodeID == nil || seen[*in.AbsenceCodeID] {
			continue
		}
		seen[*in.AbsenceCodeID] = true
		var active bool
		err := tx.QueryRow(ctx, `select active from absence_code where tenant_id = $1 and id = $2 for share`, tenantID, *in.AbsenceCodeID).Scan(&active)
		if errors.Is(err, pgx.ErrNoRows) {
			return absenceNotFound(http.StatusUnprocessableEntity)
		}
		if err != nil {
			return err
		}
		if !active {
			var stored bool
			if err := tx.QueryRow(ctx, `select exists (select 1 from time_registration where tenant_id = $1 and id = $2)`, tenantID, in.ID).Scan(&stored); err != nil {
				return err
			}
			if !stored {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeAbsenceInactive, "absence code is inactive")
			}
		}
	}
	return nil
}

// loadAbsences reads the closed absence registrations of a worker that start in [start, end)
// with their code terms (fixed once the code is in use, see UpdateAbsenceCode).
func loadAbsences(ctx context.Context, tx pgx.Tx, tenantID, workerID string, start, end time.Time) ([]domain.Absence, error) {
	rows, err := tx.Query(ctx, `select t.id::text, t.started_at, t.stopped_at, t.hours::text, c.paid, c.pay_multiplier::text
		from time_registration t join absence_code c on c.tenant_id = t.tenant_id and c.id = t.absence_code_id
		where t.tenant_id = $1 and t.worker_id = $2 and t.kind = 'absence' and t.stopped_at is not null
		  and t.started_at >= $3 and t.started_at < $4
		order by t.started_at, t.id`, tenantID, workerID, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Absence
	for rows.Next() {
		var a domain.Absence
		var hours, mult string
		if err := rows.Scan(&a.ID, &a.Start, &a.Stop, &hours, &a.Paid, &mult); err != nil {
			return nil, err
		}
		if a.Hours, err = decimal.NewFromString(hours); err != nil {
			return nil, err
		}
		if a.Multiplier, err = decimal.NewFromString(mult); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
