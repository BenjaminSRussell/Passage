package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
)

// Downtime reason problem codes (see README).
const (
	CodeReasonNotFound = "SF-0158"
	CodeReasonInactive = "SF-0159"
	// CodeReasonCategory refuses coding a downtime with a category other than the reason's own.
	CodeReasonCategory = "SF-0160"

	downtimeReasonSource = "downtimereason"
)

// DowntimeReason is one entry of the downtime reason code master (FR-25.11).
type DowntimeReason struct {
	ID        string    `json:"id"`
	Code      string    `json:"code"`
	Name      string    `json:"name"`
	Category  string    `json:"category"`
	Active    bool      `json:"active"`
	Version   int       `json:"version"`
	CreatedBy string    `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// DowntimeReasonInput is what a create or update writes. The code never changes after creation.
type DowntimeReasonInput struct {
	Code     string
	Name     string
	Category string
	Active   bool
}

const reasonColumns = `id::text, code, name, category, active, version, created_by, created_at, updated_at`

func scanReason(row pgx.Row) (DowntimeReason, error) {
	var r DowntimeReason
	err := row.Scan(&r.ID, &r.Code, &r.Name, &r.Category, &r.Active, &r.Version, &r.CreatedBy, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

func reasonNotFound(status int) error {
	return httpx.NewProblem(status, CodeReasonNotFound, "downtime reason not found")
}

func reasonEvent(ctx context.Context, tx pgx.Tx, tenantID string, r DowntimeReason, by string) error {
	return enqueue(ctx, tx, downtimeReasonSource, r.ID, sfevents.DowntimeReasonSaved, tenantID, sfevents.DowntimeReasonSavedData{
		ReasonID: r.ID, Code: r.Code, Name: r.Name, Category: r.Category, Active: r.Active, Version: r.Version, SavedBy: by})
}

// CreateDowntimeReason stores a reason under the Idempotency-Key and publishes
// dev.passage.shopfloor.downtimereason.saved.v1 in the same transaction. A code already used is SF-0142.
func (s *Postgres) CreateDowntimeReason(ctx context.Context, in DowntimeReasonInput, createdBy string, idem Idem) (DowntimeReason, error) {
	p, _ := tenant.FromContext(ctx)
	var out DowntimeReason
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanReason(tx.QueryRow(ctx, `insert into downtime_reason (tenant_id, id, code, name, category, active, created_by)
			values ($1, $2, $3, $4, $5, $6, $7) returning `+reasonColumns,
			p.TenantID, ids.New(), in.Code, in.Name, in.Category, in.Active, createdBy))
		if err != nil {
			return codeTaken(err)
		}
		if err := reasonEvent(ctx, tx, p.TenantID, out, createdBy); err != nil {
			return err
		}
		return remember(ctx, tx, idem, http.StatusCreated, out)
	})
	return out, err
}

// UpdateDowntimeReason replaces a reason's name, category and status when version is the stored
// version (SF-0143, 412) and bumps the version. Downtimes already coded keep the category they
// were coded with.
func (s *Postgres) UpdateDowntimeReason(ctx context.Context, id string, version int, in DowntimeReasonInput, updatedBy string) (DowntimeReason, error) {
	p, _ := tenant.FromContext(ctx)
	var out DowntimeReason
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var cur int
		err := tx.QueryRow(ctx, `select version from downtime_reason where tenant_id = $1 and id = $2 for update`, p.TenantID, id).Scan(&cur)
		if errors.Is(err, pgx.ErrNoRows) {
			return reasonNotFound(http.StatusNotFound)
		}
		if err != nil {
			return err
		}
		if cur != version {
			return versionMismatch("downtime reason")
		}
		out, err = scanReason(tx.QueryRow(ctx, `update downtime_reason set name = $3, category = $4, active = $5, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 returning `+reasonColumns, p.TenantID, id, in.Name, in.Category, in.Active))
		if err != nil {
			return err
		}
		return reasonEvent(ctx, tx, p.TenantID, out, updatedBy)
	})
	return out, err
}

// GetDowntimeReason returns a reason, or SF-0158 (404).
func (s *Postgres) GetDowntimeReason(ctx context.Context, id string) (DowntimeReason, error) {
	p, _ := tenant.FromContext(ctx)
	var out DowntimeReason
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanReason(tx.QueryRow(ctx, `select `+reasonColumns+` from downtime_reason where tenant_id = $1 and id = $2`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return reasonNotFound(http.StatusNotFound)
		}
		return err
	})
	return out, err
}

// ListDowntimeReasons returns reasons by code, at most limit rows.
func (s *Postgres) ListDowntimeReasons(ctx context.Context, activeOnly bool, limit int) ([]DowntimeReason, error) {
	p, _ := tenant.FromContext(ctx)
	out := []DowntimeReason{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+reasonColumns+` from downtime_reason where tenant_id = $1 and ($2 = false or active) order by code, id limit $3`, p.TenantID, activeOnly, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanReason(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// checkDowntimeReason applies the reason master to coding a downtime and returns the category to
// store. A tenant with no reason defined keeps the free text (the category is the request's). Once
// it has one, the code must name an existing reason (SF-0158, 422) that is active (SF-0159, 422), and
// a category in the request must be the reason's own (SF-0160, 422); an empty category takes the
// reason's.
func checkDowntimeReason(ctx context.Context, tx pgx.Tx, tenantID, code, category string) (string, error) {
	var defined bool
	if err := tx.QueryRow(ctx, `select exists (select 1 from downtime_reason where tenant_id = $1)`, tenantID).Scan(&defined); err != nil {
		return "", err
	}
	if !defined {
		return category, nil
	}
	var reasonCategory string
	var active bool
	err := tx.QueryRow(ctx, `select category, active from downtime_reason where tenant_id = $1 and code = $2`, tenantID, code).Scan(&reasonCategory, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", reasonNotFound(http.StatusUnprocessableEntity)
	}
	if err != nil {
		return "", err
	}
	if !active {
		return "", httpx.NewProblem(http.StatusUnprocessableEntity, CodeReasonInactive, "downtime reason "+code+" is inactive")
	}
	if category != "" && category != reasonCategory {
		return "", httpx.NewProblem(http.StatusUnprocessableEntity, CodeReasonCategory, "downtime reason "+code+" is "+reasonCategory+", not "+category)
	}
	return reasonCategory, nil
}
