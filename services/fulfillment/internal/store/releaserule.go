package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
)

// Defaults of the release parameters of section 5.28.13, read for a warehouse
// without a saved rule.
const (
	DefaultReleaseHorizonDays = 2
	DefaultWaveMaxLines       = 200
	DefaultWaveMaxOrders      = 50
)

// ReleaseRuleStore is the release and wave parameters per warehouse (FR-28.1,
// FR-28.13): release_horizon_days, release_requires_full_atp, wave_max_lines
// and wave_max_orders.
type ReleaseRuleStore interface {
	ListReleaseRules(ctx context.Context) ([]ReleaseRule, error)
	GetReleaseRule(ctx context.Context, warehouseID string) (ReleaseRule, error)
	SaveReleaseRule(ctx context.Context, warehouseID, legalEntityID string, version int, in ReleaseRuleInput, idem Idem) (ReleaseRule, error)
}

// ReleaseRuleInput is the value saved for a warehouse.
type ReleaseRuleInput struct {
	HorizonDays    int
	RequireFullATP bool
	WaveMaxLines   int
	WaveMaxOrders  int
}

// ReleaseRule is the release and wave parameters of one warehouse. A warehouse
// without a saved rule reads the defaults at version 0 (Saved false): the
// process configuration for the horizon and the ATP rule (section 5.28.13's 2
// days and full ATP unless configured otherwise), 200 lines and 50 orders.
type ReleaseRule struct {
	WarehouseID    string     `json:"warehouseId"`
	LegalEntityID  *string    `json:"legalEntityId"`
	HorizonDays    int        `json:"horizonDays"`
	RequireFullATP bool       `json:"requireFullAtp"`
	WaveMaxLines   int        `json:"waveMaxLines"`
	WaveMaxOrders  int        `json:"waveMaxOrders"`
	Version        int        `json:"version"`
	UpdatedAt      *time.Time `json:"updatedAt"`
}

// Saved reports whether the warehouse has a saved rule.
func (r ReleaseRule) Saved() bool { return r.Version > 0 }

func (s *Postgres) defaultReleaseRule(warehouseID string) ReleaseRule {
	d := s.releaseDefaults
	return ReleaseRule{WarehouseID: warehouseID, HorizonDays: d.HorizonDays, RequireFullATP: d.RequireFullATP,
		WaveMaxLines: d.WaveMaxLines, WaveMaxOrders: d.WaveMaxOrders}
}

// ListReleaseRules lists every warehouse the caller sees shipments, a short
// pick setting or a saved rule for (all legal-entity scoped), with its rule
// (the defaults when none is saved).
func (s *Postgres) ListReleaseRules(ctx context.Context) ([]ReleaseRule, error) {
	out := []ReleaseRule{}
	d := s.releaseDefaults
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select w.warehouse_id, coalesce(rr.legal_entity_id, w.legal_entity_id),
				coalesce(rr.release_horizon_days, $1), coalesce(rr.release_requires_full_atp, $2),
				coalesce(rr.wave_max_lines, $3), coalesce(rr.wave_max_orders, $4), coalesce(rr.version, 0), rr.updated_at
			from (select warehouse_id, min(legal_entity_id::text)::uuid as legal_entity_id
				from (select warehouse_id, legal_entity_id from shipment
					union select warehouse_id, legal_entity_id from warehouse_setting
					union select warehouse_id, legal_entity_id from warehouse_release_rule) u
				group by warehouse_id) w
			left join warehouse_release_rule rr on rr.warehouse_id = w.warehouse_id
			order by w.warehouse_id`, d.HorizonDays, d.RequireFullATP, d.WaveMaxLines, d.WaveMaxOrders)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r ReleaseRule
			if err := rows.Scan(&r.WarehouseID, &r.LegalEntityID, &r.HorizonDays, &r.RequireFullATP, &r.WaveMaxLines, &r.WaveMaxOrders, &r.Version, &r.UpdatedAt); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// warehouseLegalEntity is the legal entity a warehouse belongs to as far as
// this service knows it: the one of its short pick setting, else of its
// release rule, else of its shipments; nil when none is visible to the caller.
func warehouseLegalEntity(ctx context.Context, tx pgx.Tx, warehouseID string) (*string, error) {
	var le *string
	err := tx.QueryRow(ctx, `select legal_entity_id from (
			select legal_entity_id, 1 as src from warehouse_setting where warehouse_id = $1
			union all select legal_entity_id, 2 from warehouse_release_rule where warehouse_id = $1
			union all (select legal_entity_id, 3 from shipment where warehouse_id = $1 limit 1)) k
		order by src limit 1`, warehouseID).Scan(&le)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return le, err
}

// loadReleaseRule reads the saved rule of a warehouse, or the defaults (version
// 0) with the legal entity the warehouse belongs to when none is saved.
func (s *Postgres) loadReleaseRule(ctx context.Context, tx pgx.Tx, warehouseID string, lock bool) (ReleaseRule, error) {
	r := s.defaultReleaseRule(warehouseID)
	q := `select legal_entity_id, release_horizon_days, release_requires_full_atp, wave_max_lines, wave_max_orders, version, updated_at
		from warehouse_release_rule where warehouse_id = $1`
	if lock {
		q += ` for update`
	}
	err := tx.QueryRow(ctx, q, warehouseID).Scan(&r.LegalEntityID, &r.HorizonDays, &r.RequireFullATP, &r.WaveMaxLines, &r.WaveMaxOrders, &r.Version, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		r.LegalEntityID, err = warehouseLegalEntity(ctx, tx, warehouseID)
	}
	return r, err
}

// GetReleaseRule reads the release rule of one warehouse.
func (s *Postgres) GetReleaseRule(ctx context.Context, warehouseID string) (ReleaseRule, error) {
	var r ReleaseRule
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		r, err = s.loadReleaseRule(ctx, tx, warehouseID, false)
		return err
	})
	return r, err
}

// SaveReleaseRule sets the release rule of a warehouse of legalEntityID.
// version is the one the caller read (0 when none is saved); a stale version
// is refused, as is a legal entity other than the one of the saved rule, the
// warehouse's short pick setting or its shipments (FUL-0057). warehousereleaserule.changed is written
// in the same transaction.
func (s *Postgres) SaveReleaseRule(ctx context.Context, warehouseID, legalEntityID string, version int, in ReleaseRuleInput, idem Idem) (ReleaseRule, error) {
	p, _ := tenant.FromContext(ctx)
	var out ReleaseRule
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() (ReleaseRule, error) {
			// Serialise the first save of a warehouse, which has no row to lock yet.
			if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1 || '/release-rule/' || $2, 0))`, p.TenantID, warehouseID); err != nil {
				return ReleaseRule{}, err
			}
			cur, err := s.loadReleaseRule(ctx, tx, warehouseID, true)
			if err != nil {
				return cur, err
			}
			if cur.LegalEntityID != nil && *cur.LegalEntityID != legalEntityID {
				return cur, otherLegalEntity()
			}
			if cur.Version != version {
				return cur, httpx.NewProblem(http.StatusPreconditionFailed, CodeSettingVersion, "release rule changed since it was read; reload and retry")
			}
			le := legalEntityID
			next := ReleaseRule{WarehouseID: warehouseID, LegalEntityID: &le, HorizonDays: in.HorizonDays, RequireFullATP: in.RequireFullATP,
				WaveMaxLines: in.WaveMaxLines, WaveMaxOrders: in.WaveMaxOrders}
			err = tx.QueryRow(ctx, `insert into warehouse_release_rule (tenant_id, warehouse_id, legal_entity_id, release_horizon_days, release_requires_full_atp, wave_max_lines, wave_max_orders, version)
				values ($1, $2, $3, $4, $5, $6, $7, 1)
				on conflict (tenant_id, warehouse_id) do update set release_horizon_days = excluded.release_horizon_days,
					release_requires_full_atp = excluded.release_requires_full_atp, wave_max_lines = excluded.wave_max_lines,
					wave_max_orders = excluded.wave_max_orders, version = warehouse_release_rule.version + 1, updated_at = now()
				returning version, updated_at`, p.TenantID, warehouseID, legalEntityID, in.HorizonDays, in.RequireFullATP, in.WaveMaxLines, in.WaveMaxOrders).Scan(&next.Version, &next.UpdatedAt)
			// A row of a legal entity outside the caller's scope is invisible
			// above; the upsert then trips its row-level security policy.
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "42501" {
				return cur, otherLegalEntity()
			}
			if err != nil {
				return cur, err
			}
			return next, enqueueFor(ctx, tx, "warehouse_release_rule", warehouseID, fulevents.WarehouseReleaseRuleChanged, p.TenantID, fulevents.WarehouseReleaseRuleChangedData{
				WarehouseID: warehouseID, LegalEntityID: legalEntityID, HorizonDays: next.HorizonDays, RequireFullATP: next.RequireFullATP,
				WaveMaxLines: next.WaveMaxLines, WaveMaxOrders: next.WaveMaxOrders,
				PreviousHorizonDays: cur.HorizonDays, PreviousRequireFullATP: cur.RequireFullATP,
				PreviousWaveMaxLines: cur.WaveMaxLines, PreviousWaveMaxOrders: cur.WaveMaxOrders, Version: next.Version})
		})
		out = res
		return err
	})
	return out, err
}
