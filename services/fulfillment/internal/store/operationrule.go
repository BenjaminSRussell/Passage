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

// CodePostingDate is the code of a posting date that contradicts the
// warehouse's posting_date_source; CodeAutoPackDisabled the code of an
// auto-pack in a warehouse that turned it off.
const (
	CodePostingDate      = "FUL-0074"
	CodeAutoPackDisabled = "FUL-0075"
)

// OperationRuleStore is the posting and packing parameters per warehouse
// (section 5.28.13: posting_date_source, auto_pack_enabled,
// workbench_group_key_default; the warehouse time zone of BR-28.3).
type OperationRuleStore interface {
	ListOperationRules(ctx context.Context) ([]OperationRule, error)
	GetOperationRule(ctx context.Context, warehouseID string) (OperationRule, error)
	SaveOperationRule(ctx context.Context, warehouseID, legalEntityID string, version int, in OperationRuleInput, idem Idem) (OperationRule, error)
}

// OperationRuleInput is the value saved for a warehouse.
type OperationRuleInput struct {
	PostingDateSource        string
	TimeZone                 string
	AutoPackEnabled          bool
	WorkbenchGroupKeyDefault string
}

// OperationRule is the posting and packing parameters of one warehouse. A
// warehouse without a saved rule reads the defaults at version 0 (Saved
// false): MANUAL posting dates in UTC (what every post did before the rule
// existed, so the PRD's SHIP_TIMESTAMP is a deviation until a rule is saved),
// auto-pack on and the CUSTOMER workbench grouping.
type OperationRule struct {
	WarehouseID              string     `json:"warehouseId"`
	LegalEntityID            *string    `json:"legalEntityId"`
	PostingDateSource        string     `json:"postingDateSource"`
	TimeZone                 string     `json:"timeZone"`
	AutoPackEnabled          bool       `json:"autoPackEnabled"`
	WorkbenchGroupKeyDefault string     `json:"workbenchGroupKeyDefault"`
	Version                  int        `json:"version"`
	UpdatedAt                *time.Time `json:"updatedAt"`
}

// Saved reports whether the warehouse has a saved rule.
func (r OperationRule) Saved() bool { return r.Version > 0 }

func defaultOperationRule(warehouseID string) OperationRule {
	return OperationRule{WarehouseID: warehouseID, PostingDateSource: domain.PostingManual, TimeZone: "UTC",
		AutoPackEnabled: true, WorkbenchGroupKeyDefault: domain.GroupCustomer}
}

// ListOperationRules lists every warehouse the caller sees shipments or a
// saved setting, release rule or operation rule for (all legal-entity scoped),
// with its rule (the defaults when none is saved).
func (s *Postgres) ListOperationRules(ctx context.Context) ([]OperationRule, error) {
	out := []OperationRule{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select w.warehouse_id, coalesce(r.legal_entity_id, w.legal_entity_id),
				coalesce(r.posting_date_source, $1), coalesce(r.time_zone, $2), coalesce(r.auto_pack_enabled, true),
				coalesce(r.workbench_group_key_default, $3), coalesce(r.version, 0), r.updated_at
			from (select warehouse_id, min(legal_entity_id::text)::uuid as legal_entity_id
				from (select warehouse_id, legal_entity_id from shipment
					union select warehouse_id, legal_entity_id from warehouse_setting
					union select warehouse_id, legal_entity_id from warehouse_release_rule
					union select warehouse_id, legal_entity_id from warehouse_operation_rule) u
				group by warehouse_id) w
			left join warehouse_operation_rule r on r.warehouse_id = w.warehouse_id
			order by w.warehouse_id`, domain.PostingManual, "UTC", domain.GroupCustomer)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r OperationRule
			if err := rows.Scan(&r.WarehouseID, &r.LegalEntityID, &r.PostingDateSource, &r.TimeZone, &r.AutoPackEnabled, &r.WorkbenchGroupKeyDefault, &r.Version, &r.UpdatedAt); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// loadOperationRule reads the saved rule of a warehouse, or the defaults
// (version 0) with the legal entity the warehouse belongs to when none is saved.
func loadOperationRule(ctx context.Context, tx pgx.Tx, warehouseID string, lock bool) (OperationRule, error) {
	r := defaultOperationRule(warehouseID)
	q := `select legal_entity_id, posting_date_source, time_zone, auto_pack_enabled, workbench_group_key_default, version, updated_at
		from warehouse_operation_rule where warehouse_id = $1`
	if lock {
		q += ` for update`
	}
	err := tx.QueryRow(ctx, q, warehouseID).Scan(&r.LegalEntityID, &r.PostingDateSource, &r.TimeZone, &r.AutoPackEnabled, &r.WorkbenchGroupKeyDefault, &r.Version, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		r.LegalEntityID, err = warehouseLegalEntity(ctx, tx, warehouseID)
	}
	return r, err
}

// shipmentOperationRule is the rule a shipment's post and auto-pack follow:
// the saved rule of its warehouse when that rule belongs to the shipment's
// legal entity, else the defaults. Reading by the shipment's entity keeps a
// rule saved under another entity from applying, and makes the outcome the
// same whatever the caller's legal-entity scope (row-level security would
// hide such a rule from a scoped caller only).
func shipmentOperationRule(ctx context.Context, tx pgx.Tx, sh Shipment) (OperationRule, error) {
	r := defaultOperationRule(sh.WarehouseID)
	le := sh.LegalEntityID
	r.LegalEntityID = &le
	err := tx.QueryRow(ctx, `select posting_date_source, time_zone, auto_pack_enabled, workbench_group_key_default, version, updated_at
		from warehouse_operation_rule where warehouse_id = $1 and legal_entity_id = $2`, sh.WarehouseID, sh.LegalEntityID).
		Scan(&r.PostingDateSource, &r.TimeZone, &r.AutoPackEnabled, &r.WorkbenchGroupKeyDefault, &r.Version, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, nil
	}
	return r, err
}

// GetOperationRule reads the operation rule of one warehouse.
func (s *Postgres) GetOperationRule(ctx context.Context, warehouseID string) (OperationRule, error) {
	var r OperationRule
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		r, err = loadOperationRule(ctx, tx, warehouseID, false)
		return err
	})
	return r, err
}

// SaveOperationRule sets the operation rule of a warehouse of legalEntityID.
// version is the one the caller read (0 when none is saved); a stale version
// is refused, as is a legal entity other than the one the warehouse belongs to
// (FUL-0057). warehouseoperationrule.changed is written in the same transaction.
func (s *Postgres) SaveOperationRule(ctx context.Context, warehouseID, legalEntityID string, version int, in OperationRuleInput, idem Idem) (OperationRule, error) {
	p, _ := tenant.FromContext(ctx)
	var out OperationRule
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() (OperationRule, error) {
			// Serialise the first save of a warehouse, which has no row to lock yet.
			if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1 || '/operation-rule/' || $2, 0))`, p.TenantID, warehouseID); err != nil {
				return OperationRule{}, err
			}
			cur, err := loadOperationRule(ctx, tx, warehouseID, true)
			if err != nil {
				return cur, err
			}
			if cur.LegalEntityID != nil && *cur.LegalEntityID != legalEntityID {
				return cur, otherLegalEntity()
			}
			if cur.Version != version {
				return cur, httpx.NewProblem(http.StatusPreconditionFailed, CodeSettingVersion, "operation rule changed since it was read; reload and retry")
			}
			le := legalEntityID
			next := OperationRule{WarehouseID: warehouseID, LegalEntityID: &le, PostingDateSource: in.PostingDateSource, TimeZone: in.TimeZone,
				AutoPackEnabled: in.AutoPackEnabled, WorkbenchGroupKeyDefault: in.WorkbenchGroupKeyDefault}
			err = tx.QueryRow(ctx, `insert into warehouse_operation_rule (tenant_id, warehouse_id, legal_entity_id, posting_date_source, time_zone, auto_pack_enabled, workbench_group_key_default, version)
				values ($1, $2, $3, $4, $5, $6, $7, 1)
				on conflict (tenant_id, warehouse_id) do update set posting_date_source = excluded.posting_date_source, time_zone = excluded.time_zone,
					auto_pack_enabled = excluded.auto_pack_enabled, workbench_group_key_default = excluded.workbench_group_key_default,
					version = warehouse_operation_rule.version + 1, updated_at = now()
				returning version, updated_at`, p.TenantID, warehouseID, legalEntityID, in.PostingDateSource, in.TimeZone, in.AutoPackEnabled, in.WorkbenchGroupKeyDefault).Scan(&next.Version, &next.UpdatedAt)
			// A row of a legal entity outside the caller's scope is invisible
			// above; the upsert then trips its row-level security policy.
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "42501" {
				return cur, otherLegalEntity()
			}
			if err != nil {
				return cur, err
			}
			return next, enqueueFor(ctx, tx, "warehouse_operation_rule", warehouseID, fulevents.WarehouseOperationRuleChanged, p.TenantID, fulevents.WarehouseOperationRuleChangedData{
				WarehouseID: warehouseID, LegalEntityID: legalEntityID, PostingDateSource: next.PostingDateSource, TimeZone: next.TimeZone,
				AutoPackEnabled: next.AutoPackEnabled, WorkbenchGroupKeyDefault: next.WorkbenchGroupKeyDefault,
				PreviousPostingDateSource: cur.PostingDateSource, PreviousTimeZone: cur.TimeZone,
				PreviousAutoPackEnabled: cur.AutoPackEnabled, PreviousWorkbenchGroupKey: cur.WorkbenchGroupKeyDefault, Version: next.Version})
		})
		out = res
		return err
	})
	return out, err
}
