package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
)

const holdColumns = `id, load_id, status, parties, created_by, created_at, coalesce(released_by, ''),
	coalesce(release_reason, ''), released_at, version`

// HoldFilter narrows the screening-hold list (Screening holds inquiry,
// §6.34.14). Empty fields match everything.
type HoldFilter struct {
	Status string
	LoadID string
}

func scanHold(row pgx.Row) (domain.ScreeningHold, error) {
	var h domain.ScreeningHold
	var parties []byte
	if err := row.Scan(&h.ID, &h.LoadID, &h.Status, &parties, &h.CreatedBy, &h.CreatedAt, &h.ReleasedBy,
		&h.ReleaseReason, &h.ReleasedAt, &h.Version); err != nil {
		return domain.ScreeningHold{}, err
	}
	if err := json.Unmarshal(parties, &h.Parties); err != nil {
		return domain.ScreeningHold{}, err
	}
	if h.Parties == nil {
		h.Parties = []domain.PartyScreening{}
	}
	return h, nil
}

// RecordScreeningHold holds a load whose screen decided HOLD (BR-34.17). A load
// has at most one OPEN hold: screening it again while held replaces the hold's
// party results with the latest ones and keeps it OPEN, since the hook never
// clears a hold on its own. The load must exist.
func (s *Postgres) RecordScreeningHold(ctx context.Context, loadID string, parties []domain.PartyScreening) (domain.ScreeningHold, error) {
	p, _ := tenant.FromContext(ctx)
	raw, err := json.Marshal(parties)
	if err != nil {
		return domain.ScreeningHold{}, err
	}
	var out domain.ScreeningHold
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from load where tenant_id = $1 and id = $2)`, p.TenantID, loadID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return NotFound("load")
		}
		var err error
		out, err = scanHold(tx.QueryRow(ctx, `insert into screening_hold (tenant_id, id, load_id, status, parties, created_by)
			values ($1, $2, $3, 'OPEN', $4::jsonb, $5)
			on conflict (tenant_id, load_id) where status = 'OPEN'
			do update set parties = excluded.parties, version = screening_hold.version + 1, updated_at = now()
			returning `+holdColumns,
			p.TenantID, ids.New(), loadID, string(raw), p.UserID))
		return err
	})
	return out, err
}

// GetScreeningHold returns one hold.
func (s *Postgres) GetScreeningHold(ctx context.Context, id string) (domain.ScreeningHold, error) {
	p, _ := tenant.FromContext(ctx)
	var h domain.ScreeningHold
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		h, err = scanHold(tx.QueryRow(ctx, `select `+holdColumns+` from screening_hold where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ScreeningHold{}, NotFound("screening hold")
	}
	return h, err
}

// ListScreeningHolds returns the newest holds first, at most ListLimit.
func (s *Postgres) ListScreeningHolds(ctx context.Context, f HoldFilter) ([]domain.ScreeningHold, error) {
	p, _ := tenant.FromContext(ctx)
	out := []domain.ScreeningHold{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+holdColumns+` from screening_hold
			where tenant_id = $1 and ($2::text = '' or status = $2) and ($3::text = '' or load_id::text = $3)
			order by created_at desc, id desc limit $4`, p.TenantID, f.Status, f.LoadID, ListLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			h, err := scanHold(rows)
			if err != nil {
				return err
			}
			out = append(out, h)
		}
		return rows.Err()
	})
	return out, err
}

// ReleaseScreeningHold releases a hold under a row lock (BR-34.17). The releasing
// user, the reason and the time stay on the row, which is the release log of
// AC-34.16. A hold with a BLOCKED party is refused (TMS-0133) and a hold released
// by someone else or for another reason is TMS-0122; repeating the same release
// returns the hold unchanged.
func (s *Postgres) ReleaseScreeningHold(ctx context.Context, id, reason string, at time.Time) (domain.ScreeningHold, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.ScreeningHold
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanHold(tx.QueryRow(ctx, `select `+holdColumns+` from screening_hold
			where tenant_id = $1 and id = $2 for update`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return NotFound("screening hold")
		}
		if err != nil {
			return err
		}
		next, changed, err := cur.Release(p.UserID, reason, at)
		if err != nil {
			return holdProblem(err)
		}
		if !changed {
			out = next
			return nil
		}
		if _, err := tx.Exec(ctx, `update screening_hold set status = $3, released_by = $4, release_reason = $5,
			released_at = $6, version = $7, updated_at = now()
			where tenant_id = $1 and id = $2`,
			p.TenantID, id, next.Status, next.ReleasedBy, next.ReleaseReason, *next.ReleasedAt, next.Version); err != nil {
			return err
		}
		out = next
		return nil
	})
	return out, err
}

func holdProblem(err error) error {
	var de *domain.Error
	if !errors.As(err, &de) {
		return err
	}
	status := http.StatusConflict
	if de.Code == domain.CodeScreeningHold {
		status = http.StatusUnprocessableEntity
	}
	return httpx.NewProblem(status, de.Code, de.Detail)
}

// screeningGate refuses a tender while the load has an OPEN screening hold
// (BR-34.17 "tender ... hold the load until released", AC-34.16).
func screeningGate(ctx context.Context, tx pgx.Tx, tenantID, loadID string) error {
	var holdID string
	err := tx.QueryRow(ctx, `select id from screening_hold where tenant_id = $1 and load_id = $2 and status = 'OPEN'`,
		tenantID, loadID).Scan(&holdID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeScreeningHold,
		"the load is held by screening hold "+holdID+"; a trade compliance officer must release it before tendering")
}
