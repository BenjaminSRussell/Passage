package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	orgevents "github.com/BenjaminSRussell/passage/services/org/internal/events"
)

const (
	CodeWarehouseNotFound            = "ORG-WH-NOT-FOUND"
	CodeWarehouseCodeDuplicate       = "ORG-WH-CODE-DUPLICATE"
	CodeWarehouseSiteInvalid         = "ORG-WH-SITE-INVALID"
	CodeWarehouseLegalEntityMismatch = "ORG-WH-LE-MISMATCH"
	foreignKeyViolation              = "23503"
	defaultWarehouseType             = "standard"
	warehouseColumns                 = "id, legal_entity_id, site_id, code, name, type, time_zone, status, version, created_at, updated_at"
)

type Warehouse struct {
	ID            string    `json:"id"`
	LegalEntityID string    `json:"legalEntityId"`
	SiteID        string    `json:"siteId"`
	Code          string    `json:"code"`
	Name          string    `json:"name"`
	Type          string    `json:"type"`
	TimeZone      string    `json:"timeZone"`
	Status        string    `json:"status"`
	Version       int       `json:"version"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type WarehousePatch struct {
	SiteID   *string `json:"siteId"`
	Name     *string `json:"name"`
	Type     *string `json:"type"`
	TimeZone *string `json:"timeZone"`
	Status   *string `json:"status"`
}

type WarehouseFilter struct{ LegalEntityID, SiteID, Type, Status string }

type WarehousePage struct {
	Items []Warehouse
	Next  *SiteKey
}

func scanWarehouse(row pgx.Row, extra ...any) (Warehouse, error) {
	var w Warehouse
	err := row.Scan(append([]any{&w.ID, &w.LegalEntityID, &w.SiteID, &w.Code, &w.Name, &w.Type, &w.TimeZone, &w.Status, &w.Version, &w.CreatedAt, &w.UpdatedAt}, extra...)...)
	return w, err
}

func warehouseNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeWarehouseNotFound, "warehouse not found")
}

func warehouseSiteInvalid() error {
	bad := httpx.NewProblem(http.StatusUnprocessableEntity, CodeWarehouseSiteInvalid, "site is unknown, inactive or outside the caller's scope")
	bad.Errors = map[string]string{"siteId": "must be an active site visible to the caller"}
	return bad
}

func warehouseLegalEntityMismatch() error {
	bad := httpx.NewProblem(http.StatusUnprocessableEntity, CodeWarehouseLegalEntityMismatch, "Warehouse and site belong to different legal entities.")
	bad.Errors = map[string]string{"siteId": "must belong to the warehouse's legal entity"}
	return bad
}

func activeSite(ctx context.Context, tx pgx.Tx, tenantID, id string) (Site, bool, error) {
	site, err := scanSite(tx.QueryRow(ctx, `select `+siteColumns+` from site where tenant_id = $1 and id = $2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Site{}, false, nil
	}
	return site, err == nil && site.Status == "active", err
}

func mapWarehouseConstraint(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.Code {
	case uniqueViolation:
		return httpx.NewProblem(http.StatusConflict, CodeWarehouseCodeDuplicate, "a warehouse with this code already exists in the legal entity")
	case foreignKeyViolation:
		return warehouseLegalEntityMismatch()
	}
	return err
}

func (s *Postgres) CreateWarehouse(ctx context.Context, in Warehouse, idempotencyKey string) (Warehouse, error) {
	p, _ := tenant.FromContext(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var warehouse Warehouse
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			replay, err := scanWarehouse(tx.QueryRow(ctx, `select `+warehouseColumns+` from warehouse where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key))
			if err == nil {
				warehouse = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		site, ok, err := activeSite(ctx, tx, p.TenantID, in.SiteID)
		if err != nil {
			return err
		}
		if !ok {
			return warehouseSiteInvalid()
		}
		zone, kind := in.TimeZone, in.Type
		if zone == "" {
			zone = site.TimeZone
		}
		if kind == "" {
			kind = defaultWarehouseType
		}
		var inserted bool
		warehouse, err = scanWarehouse(tx.QueryRow(ctx, `insert into warehouse (tenant_id, id, legal_entity_id, site_id, code, name, type, time_zone, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			on conflict (tenant_id, idempotency_key) do update set version = warehouse.version
			returning `+warehouseColumns+`, (xmax = 0)`, p.TenantID, ids.New(), site.LegalEntityID, site.ID, in.Code, in.Name, kind, zone, key), &inserted)
		if err != nil || !inserted {
			return err
		}
		return enqueue(ctx, tx, "warehouse", warehouse.ID, orgevents.WarehouseCreated, p.TenantID,
			orgevents.WarehouseCreatedData{ID: warehouse.ID, LegalEntityID: warehouse.LegalEntityID, ParentID: &warehouse.SiteID, Type: &warehouse.Type})
	})
	if err != nil {
		return Warehouse{}, mapWarehouseConstraint(err)
	}
	if key != nil && (warehouse.SiteID != in.SiteID || warehouse.Code != in.Code || warehouse.Name != in.Name ||
		(in.Type != "" && warehouse.Type != in.Type) || (in.TimeZone != "" && warehouse.TimeZone != in.TimeZone)) {
		return Warehouse{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
	}
	return warehouse, nil
}

func (s *Postgres) GetWarehouse(ctx context.Context, id string) (Warehouse, error) {
	p, _ := tenant.FromContext(ctx)
	var warehouse Warehouse
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		warehouse, err = scanWarehouse(tx.QueryRow(ctx, `select `+warehouseColumns+` from warehouse where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Warehouse{}, warehouseNotFound()
	}
	return warehouse, err
}

func (s *Postgres) ListWarehouses(ctx context.Context, f WarehouseFilter, after SiteKey, limit int) (WarehousePage, error) {
	p, _ := tenant.FromContext(ctx)
	if after.ID == "" {
		after.ID = "00000000-0000-0000-0000-000000000000"
	}
	page := WarehousePage{Items: []Warehouse{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+warehouseColumns+` from warehouse where tenant_id = $1 and (code, id) > ($2, $3::uuid)
			and ($4::text = '' or legal_entity_id::text = lower($4)) and ($5::text = '' or site_id::text = lower($5))
			and ($6::text = '' or type = $6) and ($7::text = '' or status = $7)
			order by code, id limit $8`, p.TenantID, after.Code, after.ID, f.LegalEntityID, f.SiteID, f.Type, f.Status, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			warehouse, err := scanWarehouse(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, warehouse)
		}
		return rows.Err()
	})
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[limit-1]
		page.Next = &SiteKey{Code: last.Code, ID: last.ID}
	}
	return page, err
}

func (in WarehousePatch) apply(warehouse Warehouse) (Warehouse, []string) {
	var changed []string
	if in.Name != nil && *in.Name != warehouse.Name {
		warehouse.Name = *in.Name
		changed = append(changed, "name")
	}
	if in.SiteID != nil && *in.SiteID != warehouse.SiteID {
		warehouse.SiteID = *in.SiteID
		changed = append(changed, "site_id")
	}
	if in.Status != nil && *in.Status != warehouse.Status {
		warehouse.Status = *in.Status
		changed = append(changed, "status")
	}
	if in.TimeZone != nil && *in.TimeZone != warehouse.TimeZone {
		warehouse.TimeZone = *in.TimeZone
		changed = append(changed, "time_zone")
	}
	if in.Type != nil && *in.Type != warehouse.Type {
		warehouse.Type = *in.Type
		changed = append(changed, "type")
	}
	return warehouse, changed
}

func (s *Postgres) UpdateWarehouse(ctx context.Context, id string, version int, in WarehousePatch) (Warehouse, error) {
	p, _ := tenant.FromContext(ctx)
	var warehouse Warehouse
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := scanWarehouse(tx.QueryRow(ctx, `select `+warehouseColumns+` from warehouse where tenant_id = $1 and id = $2 for update`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return warehouseNotFound()
		}
		if err != nil {
			return err
		}
		warehouse = current
		if current.Version != version {
			return httpx.NewProblem(http.StatusConflict, CodeVersionConflict, fmt.Sprintf("current version is %d", current.Version))
		}
		if in.SiteID != nil && *in.SiteID != current.SiteID {
			site, ok, err := activeSite(ctx, tx, p.TenantID, *in.SiteID)
			if err != nil {
				return err
			}
			if !ok {
				return warehouseSiteInvalid()
			}
			if site.LegalEntityID != current.LegalEntityID {
				return warehouseLegalEntityMismatch()
			}
		}
		next, changed := in.apply(current)
		if len(changed) == 0 {
			return nil
		}
		warehouse, err = scanWarehouse(tx.QueryRow(ctx, `update warehouse set site_id = $3, name = $4, type = $5, time_zone = $6, status = $7, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 returning `+warehouseColumns, p.TenantID, id, next.SiteID, next.Name, next.Type, next.TimeZone, next.Status))
		if err != nil {
			return err
		}
		return enqueue(ctx, tx, "warehouse", id, orgevents.WarehouseUpdated, p.TenantID, orgevents.WarehouseUpdatedData{ID: id, ChangedFields: changed, Version: warehouse.Version})
	})
	if err != nil {
		return Warehouse{}, mapWarehouseConstraint(err)
	}
	return warehouse, nil
}
