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
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
)

// Process category problem codes (see README).
const (
	CodeCategoryNotFound = "SF-0164"
	CodeCategoryInactive = "SF-0165"

	processCategorySource = "processcategory"
)

// ProcessCategory is one process message category: a tenant's row of the category master, or a
// built-in default (BuiltIn, no id or version) that no row overrides (FR-25.17).
type ProcessCategory struct {
	ID               string                     `json:"id,omitempty"`
	Code             string                     `json:"code"`
	Name             string                     `json:"name"`
	Destinations     []string                   `json:"destinations"`
	RejectOutOfLimit bool                       `json:"rejectOutOfLimit"`
	Characteristics  []domain.CharacteristicDef `json:"characteristics"`
	Active           bool                       `json:"active"`
	BuiltIn          bool                       `json:"builtIn"`
	Version          int                        `json:"version,omitempty"`
	CreatedBy        string                     `json:"createdBy,omitempty"`
	CreatedAt        *time.Time                 `json:"createdAt,omitempty"`
	UpdatedAt        *time.Time                 `json:"updatedAt,omitempty"`
}

const categoryColumns = `id::text, code, name, destinations, reject_out_of_limit, characteristics, active, version, created_by, created_at, updated_at`

func scanCategory(row pgx.Row) (ProcessCategory, error) {
	var c ProcessCategory
	var raw []byte
	var created, updated time.Time
	if err := row.Scan(&c.ID, &c.Code, &c.Name, &c.Destinations, &c.RejectOutOfLimit, &raw, &c.Active, &c.Version, &c.CreatedBy, &created, &updated); err != nil {
		return c, err
	}
	c.CreatedAt, c.UpdatedAt = &created, &updated
	c.Characteristics = []domain.CharacteristicDef{}
	return c, json.Unmarshal(raw, &c.Characteristics)
}

func categoryNotFound(status int) error {
	return httpx.NewProblem(status, CodeCategoryNotFound, "process category not found")
}

func defaultCategory(code string) (ProcessCategory, bool) {
	d, ok := domain.CategoryByCode(code)
	if !ok {
		return ProcessCategory{}, false
	}
	return ProcessCategory{Code: code, Name: code, Destinations: d.Destinations, RejectOutOfLimit: d.RejectOutOfLimit,
		Characteristics: []domain.CharacteristicDef{}, Active: true, BuiltIn: true}, true
}

func categoryEvent(ctx context.Context, tx pgx.Tx, tenantID string, c ProcessCategory, by string) error {
	defs := make([]sfevents.ProcessCategoryCharacter, 0, len(c.Characteristics))
	for _, d := range c.Characteristics {
		defs = append(defs, sfevents.ProcessCategoryCharacter{Name: d.Name, Type: d.Type, Unit: d.Unit, Min: d.Min, Max: d.Max, Required: d.Required})
	}
	return enqueue(ctx, tx, processCategorySource, c.ID, sfevents.ProcessCategorySaved, tenantID, sfevents.ProcessCategorySavedData{
		CategoryID: c.ID, Code: c.Code, Name: c.Name, Destinations: c.Destinations, RejectOutOfLimit: c.RejectOutOfLimit,
		Characteristics: defs, Active: c.Active, Version: c.Version, SavedBy: by})
}

func defsJSON(in []domain.CharacteristicDef) ([]byte, error) {
	if in == nil {
		in = []domain.CharacteristicDef{}
	}
	return json.Marshal(in)
}

// CreateProcessCategory stores a category under the Idempotency-Key and publishes
// dev.passage.shopfloor.processcategory.saved.v1 in the same transaction. A code already in the
// tenant's master is SF-0142; a row for a built-in code replaces that default from then on.
func (s *Postgres) CreateProcessCategory(ctx context.Context, in domain.CategoryInput, createdBy string, idem Idem) (ProcessCategory, error) {
	p, _ := tenant.FromContext(ctx)
	raw, err := defsJSON(in.Characteristics)
	if err != nil {
		return ProcessCategory{}, err
	}
	var out ProcessCategory
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanCategory(tx.QueryRow(ctx, `insert into process_category (tenant_id, id, code, name, destinations, reject_out_of_limit, characteristics, active, created_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9) returning `+categoryColumns,
			p.TenantID, ids.New(), in.Code, in.Name, in.Destinations, in.RejectOutOfLimit, raw, in.Active, createdBy))
		if err != nil {
			return codeTaken(err)
		}
		if err := categoryEvent(ctx, tx, p.TenantID, out, createdBy); err != nil {
			return err
		}
		return remember(ctx, tx, idem, http.StatusCreated, out)
	})
	return out, err
}

// UpdateProcessCategory replaces a category's name, destinations, flag, definitions and status
// when version is the stored version (SF-0143, 412) and bumps the version. Messages already
// stored keep the routing they were received with.
func (s *Postgres) UpdateProcessCategory(ctx context.Context, id string, version int, in domain.CategoryInput, updatedBy string) (ProcessCategory, error) {
	p, _ := tenant.FromContext(ctx)
	raw, err := defsJSON(in.Characteristics)
	if err != nil {
		return ProcessCategory{}, err
	}
	var out ProcessCategory
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var cur int
		err := tx.QueryRow(ctx, `select version from process_category where tenant_id = $1 and id = $2 for update`, p.TenantID, id).Scan(&cur)
		if errors.Is(err, pgx.ErrNoRows) {
			return categoryNotFound(http.StatusNotFound)
		}
		if err != nil {
			return err
		}
		if cur != version {
			return versionMismatch("process category")
		}
		out, err = scanCategory(tx.QueryRow(ctx, `update process_category set name = $3, destinations = $4, reject_out_of_limit = $5, characteristics = $6,
			active = $7, version = version + 1, updated_at = now() where tenant_id = $1 and id = $2 returning `+categoryColumns,
			p.TenantID, id, in.Name, in.Destinations, in.RejectOutOfLimit, raw, in.Active))
		if err != nil {
			return err
		}
		return categoryEvent(ctx, tx, p.TenantID, out, updatedBy)
	})
	return out, err
}

// GetProcessCategory returns a category of the tenant's master, or SF-0164 (404).
func (s *Postgres) GetProcessCategory(ctx context.Context, id string) (ProcessCategory, error) {
	p, _ := tenant.FromContext(ctx)
	var out ProcessCategory
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanCategory(tx.QueryRow(ctx, `select `+categoryColumns+` from process_category where tenant_id = $1 and id = $2`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return categoryNotFound(http.StatusNotFound)
		}
		return err
	})
	return out, err
}

// ListProcessCategories returns the tenant's categories by code followed by the built-in defaults
// that no row overrides (BuiltIn true), at most limit tenant rows. activeOnly drops inactive rows.
func (s *Postgres) ListProcessCategories(ctx context.Context, activeOnly bool, limit int) ([]ProcessCategory, error) {
	p, _ := tenant.FromContext(ctx)
	out := []ProcessCategory{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+categoryColumns+` from process_category where tenant_id = $1 and ($2 = false or active) order by code, id limit $3`, p.TenantID, activeOnly, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanCategory(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		// A built-in is overridden by any row of its code, active or not.
		overridden := map[string]bool{}
		all, err := tx.Query(ctx, `select code from process_category where tenant_id = $1 and code = any($2)`, p.TenantID, domain.DefaultCategoryCodes)
		if err != nil {
			return err
		}
		defer all.Close()
		for all.Next() {
			var code string
			if err := all.Scan(&code); err != nil {
				return err
			}
			overridden[code] = true
		}
		for _, code := range domain.DefaultCategoryCodes {
			if d, ok := defaultCategory(code); ok && !overridden[code] {
				out = append(out, d)
			}
		}
		return all.Err()
	})
	return out, err
}

// ResolveProcessCategory returns what a message of category code is checked and routed with: the
// tenant's row when it has one (an inactive row is SF-0165, 422), else the built-in default, else
// SF-0164 (422).
func (s *Postgres) ResolveProcessCategory(ctx context.Context, code string) (domain.Category, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.Category
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		c, err := scanCategory(tx.QueryRow(ctx, `select `+categoryColumns+` from process_category where tenant_id = $1 and code = $2`, p.TenantID, code))
		if errors.Is(err, pgx.ErrNoRows) {
			d, ok := domain.CategoryByCode(code)
			if !ok {
				return categoryNotFound(http.StatusUnprocessableEntity)
			}
			out = d
			return nil
		}
		if err != nil {
			return err
		}
		if !c.Active {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeCategoryInactive, "process category "+code+" is inactive")
		}
		out = domain.Category{Code: c.Code, Destinations: c.Destinations, RejectOutOfLimit: c.RejectOutOfLimit, Characteristics: c.Characteristics}
		return nil
	})
	return out, err
}
