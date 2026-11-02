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
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
)

// CodeSettingVersion is returned when a warehouse setting changed since it was read.
const CodeSettingVersion = "conflict_version"

// RemainderStore is the short pick action (FR-28.8) part of the store: the
// warehouse setting and the backorder shipment.
type RemainderStore interface {
	ListWarehouseSettings(ctx context.Context) ([]WarehouseSetting, error)
	GetWarehouseSetting(ctx context.Context, warehouseID string) (WarehouseSetting, error)
	SaveWarehouseSetting(ctx context.Context, warehouseID, legalEntityID string, version int, action domain.ShortPickAction, idem Idem) (WarehouseSetting, error)
	BackorderRemainder(ctx context.Context, id string, version int, shipDate *time.Time, idem Idem) (BackorderResult, error)
}

// WarehouseSetting is the short pick action of one warehouse. A warehouse
// without a saved setting reads REMAINDER_BACKORDER (the default) at version 0. A warehouse belongs to
// one legal entity: LegalEntityID is the saved one, else the one of its
// shipments, and nil when neither is known.
type WarehouseSetting struct {
	WarehouseID     string                 `json:"warehouseId"`
	LegalEntityID   *string                `json:"legalEntityId"`
	ShortPickAction domain.ShortPickAction `json:"shortPickAction"`
	Version         int                    `json:"version"`
	UpdatedAt       *time.Time             `json:"updatedAt"`
}

// BackorderResult is the shipment after its remainder moved, and the new
// shipment that holds the remainder.
type BackorderResult struct {
	Shipment  Shipment `json:"shipment"`
	Backorder Shipment `json:"backorder"`
}

// ListWarehouseSettings lists every warehouse the caller sees shipments or a
// saved setting for (both legal-entity scoped), with its short pick action
// (REMAINDER_BACKORDER when none is saved).
func (s *Postgres) ListWarehouseSettings(ctx context.Context) ([]WarehouseSetting, error) {
	out := []WarehouseSetting{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select w.warehouse_id, coalesce(ws.legal_entity_id, w.legal_entity_id), coalesce(ws.short_pick_action, 'REMAINDER_BACKORDER'), coalesce(ws.version, 0), ws.updated_at
			from (select warehouse_id, min(legal_entity_id::text)::uuid as legal_entity_id
				from (select warehouse_id, legal_entity_id from shipment union select warehouse_id, legal_entity_id from warehouse_setting) u
				group by warehouse_id) w
			left join warehouse_setting ws on ws.warehouse_id = w.warehouse_id
			order by w.warehouse_id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var w WarehouseSetting
			var action string
			if err := rows.Scan(&w.WarehouseID, &w.LegalEntityID, &action, &w.Version, &w.UpdatedAt); err != nil {
				return err
			}
			w.ShortPickAction = domain.ShortPickAction(action)
			out = append(out, w)
		}
		return rows.Err()
	})
	return out, err
}

// loadWarehouseSetting reads the saved setting of a warehouse, or the default
// (version 0) with the legal entity the warehouse belongs to (its release rule
// or its shipments) when none is saved.
func loadWarehouseSetting(ctx context.Context, tx pgx.Tx, warehouseID string, lock bool) (WarehouseSetting, error) {
	w := WarehouseSetting{WarehouseID: warehouseID, ShortPickAction: domain.DefaultShortPickAction}
	q := `select legal_entity_id, short_pick_action, version, updated_at from warehouse_setting where warehouse_id = $1`
	if lock {
		q += ` for update`
	}
	var action string
	err := tx.QueryRow(ctx, q, warehouseID).Scan(&w.LegalEntityID, &action, &w.Version, &w.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		w.LegalEntityID, err = warehouseLegalEntity(ctx, tx, warehouseID)
		return w, err
	}
	if err != nil {
		return w, err
	}
	w.ShortPickAction = domain.ShortPickAction(action)
	return w, nil
}

// GetWarehouseSetting reads the short pick action of one warehouse.
func (s *Postgres) GetWarehouseSetting(ctx context.Context, warehouseID string) (WarehouseSetting, error) {
	var w WarehouseSetting
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		w, err = loadWarehouseSetting(ctx, tx, warehouseID, false)
		return err
	})
	return w, err
}

func otherLegalEntity() error {
	return httpx.NewProblem(http.StatusUnprocessableEntity, CodeWarehouseLegalEntity,
		"the warehouse belongs to another legal entity (its saved setting or its shipments name another one, or it is outside your legal-entity scope)")
}

// SaveWarehouseSetting sets the short pick action of a warehouse of
// legalEntityID. version is the version the caller read (0 for a warehouse
// without a saved setting); a stale version is refused, as is a legal entity
// other than the one of the saved setting, the warehouse's release rule or its
// shipments (FUL-0057). warehousesetting.changed is written in the same transaction.
func (s *Postgres) SaveWarehouseSetting(ctx context.Context, warehouseID, legalEntityID string, version int, action domain.ShortPickAction, idem Idem) (WarehouseSetting, error) {
	p, _ := tenant.FromContext(ctx)
	var out WarehouseSetting
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() (WarehouseSetting, error) {
			// Serialise the first save of a warehouse, which has no row to lock yet.
			if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1 || '/warehouse-setting/' || $2, 0))`, p.TenantID, warehouseID); err != nil {
				return WarehouseSetting{}, err
			}
			cur, err := loadWarehouseSetting(ctx, tx, warehouseID, true)
			if err != nil {
				return cur, err
			}
			if cur.LegalEntityID != nil && *cur.LegalEntityID != legalEntityID {
				return cur, otherLegalEntity()
			}
			if cur.Version != version {
				return cur, httpx.NewProblem(http.StatusPreconditionFailed, CodeSettingVersion, "warehouse setting changed since it was read; reload and retry")
			}
			le := legalEntityID
			next := WarehouseSetting{WarehouseID: warehouseID, LegalEntityID: &le, ShortPickAction: action, Version: version + 1}
			err = tx.QueryRow(ctx, `insert into warehouse_setting (tenant_id, warehouse_id, legal_entity_id, short_pick_action, version) values ($1, $2, $3, $4, 1)
				on conflict (tenant_id, warehouse_id) do update set short_pick_action = excluded.short_pick_action, version = warehouse_setting.version + 1, updated_at = now()
				returning version, updated_at`, p.TenantID, warehouseID, legalEntityID, string(action)).Scan(&next.Version, &next.UpdatedAt)
			// A row of a legal entity outside the caller's scope is invisible
			// above; the upsert then trips its row-level security policy.
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "42501" {
				return cur, otherLegalEntity()
			}
			if err != nil {
				return cur, err
			}
			return next, enqueueFor(ctx, tx, "warehouse_setting", warehouseID, fulevents.WarehouseSettingChanged, p.TenantID, fulevents.WarehouseSettingChangedData{
				WarehouseID: warehouseID, LegalEntityID: legalEntityID, ShortPickAction: string(action), PreviousShortAction: string(cur.ShortPickAction), Version: next.Version})
		})
		out = res
		return err
	})
	return out, err
}

// BackorderRemainder closes the open remainder of a shipment and moves it to a
// new RELEASED shipment of the same order and warehouse (FR-28.8, backorder
// shipment). shipDate plans the new shipment; nil means today. The rules are
// those of CloseRemainder.
func (s *Postgres) BackorderRemainder(ctx context.Context, id string, version int, shipDate *time.Time, idem Idem) (BackorderResult, error) {
	p, _ := tenant.FromContext(ctx)
	var out BackorderResult
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() (BackorderResult, error) {
			sh, err := loadShipment(ctx, tx, id)
			if err != nil {
				return BackorderResult{}, err
			}
			if err := checkVersion(sh, version); err != nil {
				return BackorderResult{}, err
			}
			date := dateOnly(time.Now())
			if shipDate != nil {
				date = dateOnly(*shipDate)
			}
			b, err := closeRemainderTx(ctx, tx, p.TenantID, &sh, &date)
			if err != nil {
				return BackorderResult{}, err
			}
			return BackorderResult{Shipment: sh, Backorder: *b}, nil
		})
		out = res
		return err
	})
	return out, err
}

// applyShortPickAction runs the warehouse's short pick action on a shipment
// that a packing slip has just left PARTIALLY_SHIPPED. Because a post needs
// every picked unit packed, the remainder is then exactly what the picks fell
// short of. KEEP_OPEN (and a
// shipment that cannot close, for example one with a line still packed) does
// nothing. It returns the action taken ("" for none) and the backorder shipment.
func applyShortPickAction(ctx context.Context, tx pgx.Tx, tenantID string, sh *Shipment) (domain.ShortPickAction, *Shipment, error) {
	if sh.Status != domain.StatusPartiallyShipped {
		return "", nil, nil
	}
	w, err := loadWarehouseSetting(ctx, tx, sh.WarehouseID, false)
	if err != nil {
		return "", nil, err
	}
	// A setting saved for another legal entity does not apply to this shipment.
	// An unsaved warehouse (version 0) takes the default for every shipment,
	// whatever legal entity its other shipments belong to.
	if w.ShortPickAction == domain.ActionKeepOpen || (w.Version > 0 && (w.LegalEntityID == nil || *w.LegalEntityID != sh.LegalEntityID)) {
		return "", nil, nil
	}
	// Leave a shipment that cannot close (a line is packed and waiting) alone;
	// the check writes nothing.
	if _, _, err := domain.CloseRemainder(sh.Status, lineQtys(*sh)); err != nil {
		return "", nil, nil
	}
	switch w.ShortPickAction {
	case domain.ActionCancelRemainder:
		_, err = closeRemainderTx(ctx, tx, tenantID, sh, nil)
		return w.ShortPickAction, nil, err
	case domain.ActionBackorder:
		// The new shipment keeps the planned date of the one it split from, or
		// today when that has passed.
		date := dateOnly(time.Now())
		if sh.PlannedShipDate.After(date) {
			date = dateOnly(sh.PlannedShipDate)
		}
		b, err := closeRemainderTx(ctx, tx, tenantID, sh, &date)
		return w.ShortPickAction, b, err
	}
	return "", nil, nil
}
