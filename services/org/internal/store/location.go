package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
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
	CodeLocationNotFound         = "ORG-LOC-NOT-FOUND"
	CodeLocationCodeDuplicate    = "ORG-LOC-CODE-DUPLICATE"
	CodeLocationWarehouseInvalid = "ORG-LOC-WAREHOUSE-INVALID"
	defaultLocationType          = "storage"
	locationColumns              = "id, legal_entity_id, warehouse_id, code, name, type, status, address_id, version, created_at, updated_at"
)

type Location struct {
	ID            string    `json:"id"`
	LegalEntityID string    `json:"legalEntityId"`
	WarehouseID   string    `json:"warehouseId"`
	Code          string    `json:"code"`
	Name          string    `json:"name"`
	Type          string    `json:"type"`
	Status        string    `json:"status"`
	AddressID     *string   `json:"addressId,omitempty"`
	Version       int       `json:"version"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type LocationPatch struct {
	Name      *string `json:"name"`
	Type      *string `json:"type"`
	Status    *string `json:"status"`
	AddressID *string `json:"addressId"`
}

type LocationFilter struct{ LegalEntityID, WarehouseID, Type, Status string }

type LocationPage struct {
	Items []Location
	Next  *SiteKey
}

func scanLocation(row pgx.Row, extra ...any) (Location, error) {
	var l Location
	err := row.Scan(append([]any{&l.ID, &l.LegalEntityID, &l.WarehouseID, &l.Code, &l.Name, &l.Type, &l.Status, &l.AddressID, &l.Version, &l.CreatedAt, &l.UpdatedAt}, extra...)...)
	return l, err
}

func locationNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeLocationNotFound, "location not found")
}

func locationWarehouseInvalid() error {
	bad := httpx.NewProblem(http.StatusUnprocessableEntity, CodeLocationWarehouseInvalid, "warehouse is unknown, inactive or outside the caller's scope")
	bad.Errors = map[string]string{"warehouseId": "must be an active warehouse visible to the caller"}
	return bad
}

func activeWarehouse(ctx context.Context, tx pgx.Tx, tenantID, id string) (Warehouse, bool, error) {
	warehouse, err := scanWarehouse(tx.QueryRow(ctx, `select `+warehouseColumns+` from warehouse where tenant_id = $1 and id = $2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Warehouse{}, false, nil
	}
	return warehouse, err == nil && warehouse.Status == "active", err
}

func mapLocationConstraint(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.Code {
	case uniqueViolation:
		return httpx.NewProblem(http.StatusConflict, CodeLocationCodeDuplicate, "a location with this code already exists in the warehouse")
	case foreignKeyViolation:
		return locationWarehouseInvalid()
	}
	return err
}

func (s *Postgres) CreateLocation(ctx context.Context, in Location, idempotencyKey string) (Location, error) {
	p, _ := tenant.FromContext(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var location Location
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			if err := lockIdempotencyKey(ctx, tx, "location", p.TenantID, *key); err != nil {
				return err
			}
			replay, err := scanLocation(tx.QueryRow(ctx, `select `+locationColumns+` from location where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key))
			if err == nil {
				location = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		warehouse, ok, err := activeWarehouse(ctx, tx, p.TenantID, in.WarehouseID)
		if err != nil {
			return err
		}
		if !ok {
			return locationWarehouseInvalid()
		}
		kind := in.Type
		if kind == "" {
			kind = defaultLocationType
		}
		var inserted bool
		location, err = scanLocation(tx.QueryRow(ctx, `insert into location (tenant_id, id, legal_entity_id, warehouse_id, code, name, type, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8)
			on conflict (tenant_id, idempotency_key) do update set version = location.version
			returning `+locationColumns+`, (xmax = 0)`, p.TenantID, ids.New(), warehouse.LegalEntityID, warehouse.ID, in.Code, in.Name, kind, key), &inserted)
		if err != nil || !inserted {
			return err
		}
		return enqueue(ctx, tx, "location", location.ID, orgevents.LocationCreated, p.TenantID,
			orgevents.LocationCreatedData{ID: location.ID, LegalEntityID: location.LegalEntityID, ParentID: &location.WarehouseID, Type: &location.Type})
	})
	if err != nil {
		return Location{}, mapLocationConstraint(err)
	}
	if key != nil && (location.WarehouseID != in.WarehouseID || location.Code != in.Code || location.Name != in.Name || (in.Type != "" && location.Type != in.Type)) {
		return Location{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
	}
	return location, nil
}

func (s *Postgres) GetLocation(ctx context.Context, id string) (Location, error) {
	p, _ := tenant.FromContext(ctx)
	var location Location
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		location, err = scanLocation(tx.QueryRow(ctx, `select `+locationColumns+` from location where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Location{}, locationNotFound()
	}
	return location, err
}

func (s *Postgres) ListLocations(ctx context.Context, f LocationFilter, after SiteKey, limit int) (LocationPage, error) {
	p, _ := tenant.FromContext(ctx)
	if after.ID == "" {
		after.ID = "00000000-0000-0000-0000-000000000000"
	}
	page := LocationPage{Items: []Location{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+locationColumns+` from location where tenant_id = $1 and (code, id) > ($2, $3::uuid)
			and ($4::text = '' or legal_entity_id::text = lower($4)) and ($5::text = '' or warehouse_id::text = lower($5))
			and ($6::text = '' or type = $6) and ($7::text = '' or status = $7)
			order by code, id limit $8`, p.TenantID, after.Code, after.ID, f.LegalEntityID, f.WarehouseID, f.Type, f.Status, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			location, err := scanLocation(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, location)
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

func (in LocationPatch) apply(location Location) (Location, []string) {
	var changed []string
	if in.Name != nil && *in.Name != location.Name {
		location.Name = *in.Name
		changed = append(changed, "name")
	}
	if in.Status != nil && *in.Status != location.Status {
		location.Status = *in.Status
		changed = append(changed, "status")
	}
	if in.Type != nil && *in.Type != location.Type {
		location.Type = *in.Type
		changed = append(changed, "type")
	}
	var addressChanged bool
	if location.AddressID, addressChanged = applyID(location.AddressID, in.AddressID); addressChanged {
		changed = append(changed, "address_id")
	}
	return location, changed
}

func (s *Postgres) UpdateLocation(ctx context.Context, id string, version int, in LocationPatch) (Location, error) {
	p, _ := tenant.FromContext(ctx)
	var location Location
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := scanLocation(tx.QueryRow(ctx, `select `+locationColumns+` from location where tenant_id = $1 and id = $2 for update`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return locationNotFound()
		}
		if err != nil {
			return err
		}
		location = current
		if current.Version != version {
			return httpx.NewProblem(http.StatusConflict, CodeVersionConflict, fmt.Sprintf("current version is %d", current.Version))
		}
		next, changed := in.apply(current)
		if len(changed) == 0 {
			return nil
		}
		if slices.Contains(changed, "address_id") && next.AddressID != nil {
			if err := checkOwnedAddress(ctx, tx, p.TenantID, "location", id, *next.AddressID, "addressId"); err != nil {
				return err
			}
		}
		location, err = scanLocation(tx.QueryRow(ctx, `update location set name = $3, type = $4, status = $5, address_id = $6, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 returning `+locationColumns, p.TenantID, id, next.Name, next.Type, next.Status, next.AddressID))
		if err != nil {
			return err
		}
		return enqueue(ctx, tx, "location", id, orgevents.LocationUpdated, p.TenantID, orgevents.LocationUpdatedData{ID: id, ChangedFields: changed, Version: location.Version})
	})
	if err != nil {
		return Location{}, err
	}
	return location, nil
}
