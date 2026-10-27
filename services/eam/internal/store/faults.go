package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/eam/internal/domain"
)

// FaultCatalog is one asset type's fault catalog with its version (the ETag of
// GET and PUT /fault-catalogs/{assetType}).
type FaultCatalog struct {
	AssetType string `json:"assetType"`
	Version   int    `json:"version"`
	domain.FaultCatalog
}

// faultKinds maps the stored kind to the catalog list it fills.
var faultKinds = []string{"SYMPTOM", "AREA", "TYPE", "CAUSE", "REMEDY"}

func kindList(c *domain.FaultCatalog, kind string) *[]domain.FaultCode {
	switch kind {
	case "SYMPTOM":
		return &c.Symptoms
	case "AREA":
		return &c.Areas
	case "TYPE":
		return &c.Types
	case "CAUSE":
		return &c.Causes
	default:
		return &c.Remedies
	}
}

// loadCatalog reads the catalog of an asset type in its designer order; ok is
// false when the type has none.
func loadCatalog(ctx context.Context, tx pgx.Tx, t, assetType string) (FaultCatalog, bool, error) {
	out := FaultCatalog{AssetType: assetType}
	err := tx.QueryRow(ctx, `select version from fault_catalog where tenant_id = $1 and asset_type = $2`, t, assetType).
		Scan(&out.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, false, nil
	}
	if err != nil {
		return out, false, err
	}
	for _, kind := range faultKinds {
		*kindList(&out.FaultCatalog, kind) = []domain.FaultCode{}
	}
	out.Combinations = []domain.FaultCombination{}
	rows, err := tx.Query(ctx, `select kind, code, text from fault_code where tenant_id = $1 and asset_type = $2
	  order by kind, position`, t, assetType)
	if err != nil {
		return out, false, err
	}
	for rows.Next() {
		var kind string
		var c domain.FaultCode
		if err := rows.Scan(&kind, &c.Code, &c.Text); err != nil {
			rows.Close()
			return out, false, err
		}
		list := kindList(&out.FaultCatalog, kind)
		*list = append(*list, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, false, err
	}
	rows, err = tx.Query(ctx, `select symptom_code, area_code, type_code from fault_combination
	  where tenant_id = $1 and asset_type = $2 order by position`, t, assetType)
	if err != nil {
		return out, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var c domain.FaultCombination
		if err := rows.Scan(&c.Symptom, &c.Area, &c.Type); err != nil {
			return out, false, err
		}
		out.Combinations = append(out.Combinations, c)
	}
	return out, true, rows.Err()
}

// CatalogSummary is one asset type's catalog in the catalog list.
type CatalogSummary struct {
	AssetType    string    `json:"assetType"`
	Version      int       `json:"version"`
	Codes        int       `json:"codes"`
	Combinations int       `json:"combinations"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// ListFaultCatalogs returns every asset type that has a catalog, by type.
func (s *Postgres) ListFaultCatalogs(ctx context.Context) ([]CatalogSummary, error) {
	out := []CatalogSummary{}
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		rows, err := tx.Query(ctx, `select c.asset_type, c.version, c.updated_at,
		    (select count(*) from fault_code k where k.tenant_id = c.tenant_id and k.asset_type = c.asset_type),
		    (select count(*) from fault_combination m where m.tenant_id = c.tenant_id and m.asset_type = c.asset_type)
		  from fault_catalog c where c.tenant_id = $1 order by c.asset_type`, t)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c CatalogSummary
			if err := rows.Scan(&c.AssetType, &c.Version, &c.UpdatedAt, &c.Codes, &c.Combinations); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// GetFaultCatalog returns the catalog of an asset type (404 when it has none).
func (s *Postgres) GetFaultCatalog(ctx context.Context, assetType string) (FaultCatalog, error) {
	var out FaultCatalog
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		c, ok, err := loadCatalog(ctx, tx, t, assetType)
		if err != nil {
			return err
		}
		if !ok {
			return problem(404, CodeNotFound, "no fault catalog for asset type "+assetType)
		}
		out = c
		return nil
	})
	return out, err
}

// PutFaultCatalog replaces the catalog of an asset type (FR-44.16). ifMatch is
// the version the designer read, 0 when the type has no catalog yet; any other
// value is 412 conflict_version. The caller validates the definition with
// domain.CheckCatalog. Registered faults keep their codes when a later version
// drops them.
func (s *Postgres) PutFaultCatalog(ctx context.Context, assetType string, in domain.FaultCatalog, ifMatch int) (FaultCatalog, error) {
	var out FaultCatalog
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		cur := 0
		err := tx.QueryRow(ctx, `select version from fault_catalog where tenant_id = $1 and asset_type = $2 for update`,
			t, assetType).Scan(&cur)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if cur != ifMatch {
			return problem(412, CodeVersion, "fault catalog version changed")
		}
		if cur == 0 {
			// A concurrent first PUT loses on the primary key.
			if _, err := tx.Exec(ctx, `insert into fault_catalog (tenant_id, asset_type, version) values ($1, $2, 1)`,
				t, assetType); err != nil {
				return err
			}
		} else {
			for _, q := range []string{
				`delete from fault_combination where tenant_id = $1 and asset_type = $2`,
				`delete from fault_code where tenant_id = $1 and asset_type = $2`,
				`update fault_catalog set version = version + 1, updated_at = now() where tenant_id = $1 and asset_type = $2`,
			} {
				if _, err := tx.Exec(ctx, q, t, assetType); err != nil {
					return err
				}
			}
		}
		for _, kind := range faultKinds {
			for i, c := range *kindList(&in, kind) {
				if _, err := tx.Exec(ctx, `insert into fault_code (tenant_id, asset_type, kind, code, text, position)
				  values ($1, $2, $3, $4, $5, $6)`, t, assetType, kind, c.Code, c.Text, i); err != nil {
					return err
				}
			}
		}
		for i, c := range in.Combinations {
			if _, err := tx.Exec(ctx, `insert into fault_combination (tenant_id, asset_type, symptom_code, area_code,
			    type_code, position) values ($1, $2, $3, $4, $5, $6)`, t, assetType, c.Symptom, c.Area, c.Type, i); err != nil {
				return err
			}
		}
		var ok bool
		out, ok, err = loadCatalog(ctx, tx, t, assetType)
		if err == nil && !ok {
			err = errors.New("fault catalog vanished inside its transaction")
		}
		return err
	})
	return out, err
}

// WorkOrderFault is the fault registered on a work order (FR-44.16).
type WorkOrderFault struct {
	ID string `json:"id"`
	domain.Fault
	Notes        *string   `json:"notes,omitempty"`
	RegisteredBy *string   `json:"registeredBy,omitempty"`
	RegisteredAt time.Time `json:"registeredAt"`
}

const faultColumns = `id::text, symptom_code, area_code, type_code, cause_code, remedy_code, notes, registered_by, registered_at`

func scanFault(row pgx.Row) (WorkOrderFault, error) {
	var f WorkOrderFault
	err := row.Scan(&f.ID, &f.Symptom, &f.Area, &f.Type, &f.Cause, &f.Remedy, &f.Notes, &f.RegisteredBy, &f.RegisteredAt)
	return f, err
}

// currentFault returns the newest fault registration of a work order, or nil.
func currentFault(ctx context.Context, tx pgx.Tx, t, woID string) (*WorkOrderFault, error) {
	f, err := scanFault(tx.QueryRow(ctx, `select `+faultColumns+` from work_order_fault
	  where tenant_id = $1 and work_order_id = $2 order by id desc limit 1`, t, woID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// RegisterFault records the fault of an open work order (FR-44.16). The codes
// must fit the catalog of the asset's type (EAM-FLT-002). A later registration
// supersedes an earlier one; a repeated key returns the work order as it is now.
func (s *Postgres) RegisterFault(ctx context.Context, woID string, in domain.Fault, notes, registeredBy, key string) (WorkOrderDetail, error) {
	var out WorkOrderDetail
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		cur, err := getWorkOrder(ctx, tx, t, woID, true)
		if err != nil {
			return err
		}
		if key != "" {
			var owner string
			err := tx.QueryRow(ctx, `select work_order_id::text from work_order_fault
			  where tenant_id = $1 and idempotency_key = $2`, t, key).Scan(&owner)
			if err == nil {
				if owner != woID {
					return problem(422, CodeKeyReuse, "Idempotency-Key was already used for a fault on another work order")
				}
				out, err = workOrderDetail(ctx, tx, t, cur)
				return err
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if !domain.Status(cur.Status).Open() {
			return problem(422, CodeWOTransition, "faults are registered on open work orders; this one is "+cur.Status)
		}
		asset, err := getAsset(ctx, tx, t, cur.AssetID, false)
		if err != nil {
			return err
		}
		cat, ok, err := loadCatalog(ctx, tx, t, asset.AssetType)
		if err != nil {
			return err
		}
		if !ok {
			return problem(422, CodeFaultInvalid, "asset type "+asset.AssetType+" has no fault catalog; define one first")
		}
		if err := domain.CheckFault(cat.FaultCatalog, in); err != nil {
			return problem(422, CodeFaultInvalid, err.Error()+" for asset type "+asset.AssetType)
		}
		if _, err := tx.Exec(ctx, `insert into work_order_fault (tenant_id, id, work_order_id, asset_type, symptom_code,
		    area_code, type_code, cause_code, remedy_code, notes, registered_by, registered_at, idempotency_key)
		  values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
			t, ids.New(), woID, asset.AssetType, in.Symptom, in.Area, in.Type, in.Cause, in.Remedy, optStr(notes),
			optStr(registeredBy), s.now(), optStr(key)); err != nil {
			return err
		}
		out, err = workOrderDetail(ctx, tx, t, cur)
		return err
	})
	return out, err
}
