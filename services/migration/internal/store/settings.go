package store

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

// TenantSettings is the tenant's migration settings (FR-10.16). A tenant that never wrote them reads the defaults
// with Version 0 and no UpdatedAt.
type TenantSettings struct {
	// RecurringMinIntervalMin is `mig.recurring.min_interval_min`: the shortest gap, in minutes, a scheduled
	// project or binding may leave between two runs.
	RecurringMinIntervalMin int        `json:"recurringMinIntervalMin"`
	Version                 int        `json:"version"`
	UpdatedBy               string     `json:"updatedBy,omitempty"`
	UpdatedAt               *time.Time `json:"updatedAt,omitempty"`
}

func defaultSettings() TenantSettings {
	return TenantSettings{RecurringMinIntervalMin: domain.MinScheduleMinutes}
}

func (s *Postgres) GetTenantSettings(ctx context.Context) (TenantSettings, error) {
	p, _ := tenant.FromContext(ctx)
	out := defaultSettings()
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var at time.Time
		err := tx.QueryRow(ctx, `select recurring_min_interval_min, version, updated_by, updated_at from migration.tenant_setting where tenant_id = $1`, p.TenantID).
			Scan(&out.RecurringMinIntervalMin, &out.Version, &out.UpdatedBy, &at)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		out.UpdatedAt = &at
		return err
	})
	return out, err
}

// PutTenantSettings writes the settings under the version the caller read: 0 creates the row, n replaces version n
// (409 MIG-VERSION-CONFLICT otherwise, naming the current version).
func (s *Postgres) PutTenantSettings(ctx context.Context, version, minInterval int) (TenantSettings, error) {
	p, _ := tenant.FromContext(ctx)
	var out TenantSettings
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var at time.Time
		var err error
		if version == 0 {
			err = tx.QueryRow(ctx, `insert into migration.tenant_setting (tenant_id, recurring_min_interval_min, updated_by) values ($1, $2, $3)
				on conflict do nothing returning recurring_min_interval_min, version, updated_by, updated_at`, p.TenantID, minInterval, p.UserID).
				Scan(&out.RecurringMinIntervalMin, &out.Version, &out.UpdatedBy, &at)
		} else {
			err = tx.QueryRow(ctx, `update migration.tenant_setting set recurring_min_interval_min = $3, version = version + 1, updated_by = $4, updated_at = now()
				where tenant_id = $1 and version = $2 returning recurring_min_interval_min, version, updated_by, updated_at`, p.TenantID, version, minInterval, p.UserID).
				Scan(&out.RecurringMinIntervalMin, &out.Version, &out.UpdatedBy, &at)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			var cur int
			if e := tx.QueryRow(ctx, `select version from migration.tenant_setting where tenant_id = $1`, p.TenantID).Scan(&cur); e != nil && !errors.Is(e, pgx.ErrNoRows) {
				return e
			}
			return versionConflict(cur)
		}
		out.UpdatedAt = &at
		return err
	})
	return out, err
}

type memSettings struct {
	mu  sync.Mutex
	cur TenantSettings
}

func (m *Mem) st() *memSettings {
	m.stOnce.Do(func() { m.stState = &memSettings{cur: defaultSettings()} })
	return m.stState
}

func (m *Mem) GetTenantSettings(context.Context) (TenantSettings, error) {
	st := m.st()
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.cur, nil
}

func (m *Mem) PutTenantSettings(ctx context.Context, version, minInterval int) (TenantSettings, error) {
	st := m.st()
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.cur.Version != version {
		return TenantSettings{}, versionConflict(st.cur.Version)
	}
	now := time.Now().UTC()
	user := ""
	if p, ok := tenant.FromContext(ctx); ok {
		user = p.UserID
	}
	st.cur = TenantSettings{RecurringMinIntervalMin: minInterval, Version: version + 1, UpdatedBy: user, UpdatedAt: &now}
	return st.cur, nil
}
