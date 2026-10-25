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
	CodeOUNotFound           = "ORG-OU-NOT-FOUND"
	CodeOUCodeDuplicate      = "ORG-OU-CODE-DUPLICATE"
	CodeOULegalEntityInvalid = "ORG-OU-LEGAL-ENTITY-INVALID"
	costCenter               = "cost_center"
	operatingUnitColumns     = "id, type, code, name, default_legal_entity_id, manager_party_id, status, version, created_at, updated_at"
)

type OperatingUnit struct {
	ID                   string    `json:"id"`
	Type                 string    `json:"type"`
	Code                 string    `json:"code"`
	Name                 string    `json:"name"`
	DefaultLegalEntityID *string   `json:"defaultLegalEntityId,omitempty"`
	ManagerPartyID       *string   `json:"managerPartyId,omitempty"`
	Status               string    `json:"status"`
	Version              int       `json:"version"`
	CreatedAt            time.Time `json:"createdAt"`
	UpdatedAt            time.Time `json:"updatedAt"`
}

type OperatingUnitPatch struct {
	Name                 *string `json:"name"`
	Status               *string `json:"status"`
	DefaultLegalEntityID *string `json:"defaultLegalEntityId"`
	ManagerPartyID       *string `json:"managerPartyId"`
}

type OperatingUnitFilter struct {
	Type                 string
	DefaultLegalEntityID string
	Status               string
	HierarchyTypeID      string
	AsOf                 time.Time
}

type OperatingUnitKey struct{ Type, Code string }

type OperatingUnitPage struct {
	Items []OperatingUnit
	Next  *OperatingUnitKey
}

func scanOperatingUnit(row pgx.Row, extra ...any) (OperatingUnit, error) {
	var u OperatingUnit
	err := row.Scan(append([]any{&u.ID, &u.Type, &u.Code, &u.Name, &u.DefaultLegalEntityID, &u.ManagerPartyID, &u.Status, &u.Version, &u.CreatedAt, &u.UpdatedAt}, extra...)...)
	return u, err
}

func operatingUnitNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeOUNotFound, "operating unit not found")
}

func nilIfEmpty(s *string) *string {
	if s == nil || *s == "" {
		return nil
	}
	return s
}

func checkDefaultLegalEntity(ctx context.Context, tx pgx.Tx, tenantID, id string) error {
	_, ok, err := activeLegalEntity(ctx, tx, tenantID, id)
	if err != nil || ok {
		return err
	}
	p := httpx.NewProblem(http.StatusUnprocessableEntity, CodeOULegalEntityInvalid, "default legal entity is unknown or inactive")
	p.Errors = map[string]string{"defaultLegalEntityId": "must be an active legal entity visible to the caller"}
	return p
}

func (s *Postgres) CreateOperatingUnit(ctx context.Context, in OperatingUnit, idempotencyKey string) (OperatingUnit, error) {
	p, _ := tenant.FromContext(ctx)
	in.DefaultLegalEntityID, in.ManagerPartyID = nilIfEmpty(in.DefaultLegalEntityID), nilIfEmpty(in.ManagerPartyID)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var u OperatingUnit
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			replay, err := scanOperatingUnit(tx.QueryRow(ctx, `select `+operatingUnitColumns+` from operating_unit where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key))
			if err == nil {
				u = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if in.DefaultLegalEntityID != nil {
			if err := checkDefaultLegalEntity(ctx, tx, p.TenantID, *in.DefaultLegalEntityID); err != nil {
				return err
			}
		}
		var inserted bool
		var err error
		u, err = scanOperatingUnit(tx.QueryRow(ctx, `insert into operating_unit (tenant_id, id, type, code, name, default_legal_entity_id, manager_party_id, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8)
			on conflict (tenant_id, idempotency_key) do update set version = operating_unit.version
			returning `+operatingUnitColumns+`, (xmax = 0)`, p.TenantID, ids.New(), in.Type, in.Code, in.Name, in.DefaultLegalEntityID, in.ManagerPartyID, key), &inserted)
		if err != nil || !inserted {
			return err
		}
		return enqueue(ctx, tx, "operatingunit", u.ID, orgevents.OperatingUnitCreated, p.TenantID, orgevents.OperatingUnitCreatedData{ID: u.ID, Type: u.Type, Code: u.Code, Name: u.Name})
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return OperatingUnit{}, httpx.NewProblem(http.StatusConflict, CodeOUCodeDuplicate, "an operating unit with this type and code already exists")
	}
	if err == nil && key != nil && (u.Type != in.Type || u.Code != in.Code || u.Name != in.Name ||
		deref(u.DefaultLegalEntityID) != deref(in.DefaultLegalEntityID) || deref(u.ManagerPartyID) != deref(in.ManagerPartyID)) {
		return OperatingUnit{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
	}
	return u, err
}

func (s *Postgres) GetOperatingUnit(ctx context.Context, id string) (OperatingUnit, error) {
	p, _ := tenant.FromContext(ctx)
	var u OperatingUnit
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		u, err = scanOperatingUnit(tx.QueryRow(ctx, `select `+operatingUnitColumns+` from operating_unit where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return OperatingUnit{}, operatingUnitNotFound()
	}
	return u, err
}

func (s *Postgres) ListOperatingUnits(ctx context.Context, f OperatingUnitFilter, after OperatingUnitKey, limit int) (OperatingUnitPage, error) {
	p, _ := tenant.FromContext(ctx)
	page := OperatingUnitPage{Items: []OperatingUnit{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if f.HierarchyTypeID != "" {
			var found string
			if err := tx.QueryRow(ctx, `select id from hierarchy_type where tenant_id = $1 and id::text = lower($2)`, p.TenantID, f.HierarchyTypeID).Scan(&found); errors.Is(err, pgx.ErrNoRows) {
				return hierarchyTypeNotFound()
			} else if err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, `select `+operatingUnitColumns+` from operating_unit where tenant_id = $1 and (type, code) > ($2, $3)
			and ($4::text = '' or type = $4) and ($5::text = '' or default_legal_entity_id::text = lower($5)) and ($6::text = '' or status = $6)
			and ($8::text = '' or exists (select 1 from hierarchy_node n join hierarchy_version v on v.tenant_id = n.tenant_id and v.id = n.version_id
				where n.tenant_id = $1 and n.org_unit_id = operating_unit.id and n.org_unit_kind = 'operating_unit' and v.hierarchy_type_id::text = lower($8)
				and v.status in ('published', 'superseded') and v.effective_from <= $9::date and (v.effective_to is null or v.effective_to > $9::date)))
			order by type, code limit $7`, p.TenantID, after.Type, after.Code, f.Type, f.DefaultLegalEntityID, f.Status, limit+1, f.HierarchyTypeID, f.AsOf)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			u, err := scanOperatingUnit(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, u)
		}
		return rows.Err()
	})
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[limit-1]
		page.Next = &OperatingUnitKey{Type: last.Type, Code: last.Code}
	}
	return page, err
}

func applyID(current, next *string) (*string, bool) {
	if next == nil || *next == deref(current) {
		return current, false
	}
	return nilIfEmpty(next), true
}

func (in OperatingUnitPatch) apply(u OperatingUnit) (OperatingUnit, []string) {
	var changed []string
	var legalEntityChanged, managerChanged bool
	u.DefaultLegalEntityID, legalEntityChanged = applyID(u.DefaultLegalEntityID, in.DefaultLegalEntityID)
	u.ManagerPartyID, managerChanged = applyID(u.ManagerPartyID, in.ManagerPartyID)
	if legalEntityChanged {
		changed = append(changed, "default_legal_entity_id")
	}
	if managerChanged {
		changed = append(changed, "manager_party_id")
	}
	if in.Name != nil && *in.Name != u.Name {
		u.Name = *in.Name
		changed = append(changed, "name")
	}
	if in.Status != nil && *in.Status != u.Status {
		u.Status = *in.Status
		changed = append(changed, "status")
	}
	return u, changed
}

func (s *Postgres) UpdateOperatingUnit(ctx context.Context, id string, version int, in OperatingUnitPatch) (OperatingUnit, error) {
	p, _ := tenant.FromContext(ctx)
	var u OperatingUnit
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := scanOperatingUnit(tx.QueryRow(ctx, `select `+operatingUnitColumns+` from operating_unit where tenant_id = $1 and id = $2 for update`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return operatingUnitNotFound()
		}
		if err != nil {
			return err
		}
		u = current
		if current.Version != version {
			return httpx.NewProblem(http.StatusConflict, CodeVersionConflict, fmt.Sprintf("current version is %d", current.Version))
		}
		if in.ManagerPartyID != nil && *in.ManagerPartyID != "" && current.Type != costCenter {
			bad := httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request failed validation")
			bad.Errors = map[string]string{"managerPartyId": "is allowed only for cost_center units"}
			return bad
		}
		next, changed := in.apply(current)
		if len(changed) == 0 {
			return nil
		}
		if next.DefaultLegalEntityID != nil && deref(next.DefaultLegalEntityID) != deref(current.DefaultLegalEntityID) {
			if err := checkDefaultLegalEntity(ctx, tx, p.TenantID, *next.DefaultLegalEntityID); err != nil {
				return err
			}
		}
		u, err = scanOperatingUnit(tx.QueryRow(ctx, `update operating_unit set name = $3, status = $4, default_legal_entity_id = $5, manager_party_id = $6, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 returning `+operatingUnitColumns, p.TenantID, id, next.Name, next.Status, next.DefaultLegalEntityID, next.ManagerPartyID))
		if err != nil {
			return err
		}
		return enqueue(ctx, tx, "operatingunit", id, orgevents.OperatingUnitUpdated, p.TenantID, orgevents.OperatingUnitUpdatedData{ID: id, ChangedFields: changed, Version: u.Version})
	})
	if err != nil {
		return OperatingUnit{}, err
	}
	return u, nil
}
