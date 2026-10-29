package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/domain"
)

const dashboardColumns = `dashboard_id, title, tiles, refresh_seconds, version, updated_at`

func scanDashboard(row pgx.Row) (domain.Dashboard, error) {
	var (
		d     domain.Dashboard
		tiles []byte
	)
	if err := row.Scan(&d.ID, &d.Title, &tiles, &d.RefreshSeconds, &d.Version, &d.UpdatedAt); err != nil {
		return d, err
	}
	d.UpdatedAt = utc(d.UpdatedAt)
	return d, json.Unmarshal(tiles, &d.Tiles)
}

// ListDashboards returns the tenant's dashboards by title.
func (s *Postgres) ListDashboards(ctx context.Context) ([]domain.Dashboard, error) {
	items := []domain.Dashboard{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+dashboardColumns+` from dashboard where `+ownTenant+`
			order by title, dashboard_id limit 500`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanDashboard(rows)
			if err != nil {
				return err
			}
			items = append(items, d)
		}
		return rows.Err()
	})
	return items, err
}

// GetDashboard returns one dashboard or ANA-0112.
func (s *Postgres) GetDashboard(ctx context.Context, id string) (domain.Dashboard, error) {
	var out domain.Dashboard
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		d, err := scanDashboard(tx.QueryRow(ctx, `select `+dashboardColumns+` from dashboard where `+ownTenant+` and dashboard_id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NotFound("dashboard " + id)
		}
		out = d
		return err
	})
	return out, err
}

// PutDashboard creates or replaces a dashboard (PUT /analytics/dashboards/{id}
// with ETag). The dashboard is prepared against catalog first, so a tile can
// only name a published metric. ifMatch is the version from If-Match, 0 when
// the header is absent: a new dashboard needs none and starts at version 1;
// replacing one requires If-Match (COM-0428) naming the current version
// (ANA-0113).
func (s *Postgres) PutDashboard(ctx context.Context, in domain.Dashboard, catalog []domain.Metric, ifMatch int) (domain.Dashboard, bool, error) {
	in, verr := domain.PrepareDashboard(in, catalog)
	if verr != nil {
		return domain.Dashboard{}, false, verr
	}
	tiles, err := json.Marshal(in.Tiles)
	if err != nil {
		return domain.Dashboard{}, false, err
	}
	p, _ := tenant.FromContext(ctx)
	var out domain.Dashboard
	created := false
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var current int
		err := tx.QueryRow(ctx, `select version from dashboard where `+ownTenant+` and dashboard_id = $1 for update`, in.ID).Scan(&current)
		exists := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		switch {
		case !exists && ifMatch != 0:
			return &domain.Error{Code: domain.CodeVersionConflict, Status: 412, Detail: "dashboard " + in.ID + " does not exist; send no If-Match to create it"}
		case exists && ifMatch == 0:
			return &domain.Error{Code: domain.CodePreconditionRequired, Status: 428, Detail: "If-Match is required to replace dashboard " + in.ID}
		case exists && ifMatch != current:
			return &domain.Error{Code: domain.CodeVersionConflict, Status: 412, Detail: "dashboard " + in.ID + " has changed; reload it and try again"}
		}
		if !exists {
			created = true
			out, err = scanDashboard(tx.QueryRow(ctx, `insert into dashboard (tenant_id, dashboard_id, title, tiles, refresh_seconds, version, updated_by)
				values ($1, $2, $3, $4, $5, 1, $6) returning `+dashboardColumns,
				p.TenantID, in.ID, in.Title, tiles, in.RefreshSeconds, p.UserID))
			return err
		}
		out, err = scanDashboard(tx.QueryRow(ctx, `update dashboard set title = $2, tiles = $3, refresh_seconds = $4,
			version = version + 1, updated_by = $5, updated_at = now()
			where `+ownTenant+` and dashboard_id = $1 returning `+dashboardColumns,
			in.ID, in.Title, tiles, in.RefreshSeconds, p.UserID))
		return err
	})
	return out, created, err
}
