package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
)

// Problem codes raised by the category store (FR-17.20).
const (
	CodeCategoryVendorNotFound  = "PRC-CAT-VENDOR-NOT-FOUND"
	CodeCategoryManagerNotFound = "PRC-CAT-MANAGER-NOT-FOUND"
	// CodeCategoryVendorExists: the vendor already has an assignment for the category.
	CodeCategoryVendorExists = "PRC-CAT-011"
	// CodeCategoryManagerExists: the worker already manages the category.
	CodeCategoryManagerExists = "PRC-CAT-012"
	// CodeCategoryVendorStale: If-Match is not the assignment's version.
	CodeCategoryVendorStale = "PRC-CAT-013"
)

// CategoryVendorFilter narrows ListCategoryVendors; empty fields do not filter.
type CategoryVendorFilter struct {
	LegalEntityID string
	Category      string
	VendorID      string
	Status        string
}

// CategoryManagerFilter narrows ListCategoryManagers; empty fields do not filter.
type CategoryManagerFilter struct {
	LegalEntityID string
	Category      string
	WorkerID      string
}

const categoryVendorColumns = `id, legal_entity_id, category, vendor_id, status, valid_from, valid_to, notes, row_version,
	coalesce(created_by::text, ''), created_at, coalesce(updated_by::text, ''), updated_at`

func scanCategoryVendor(row pgx.Row, c *domain.CategoryVendor) error {
	var from time.Time
	var to *time.Time
	if err := row.Scan(&c.ID, &c.LegalEntityID, &c.Category, &c.VendorID, &c.Status, &from, &to, &c.Notes, &c.Version,
		&c.CreatedBy, &c.CreatedAt, &c.UpdatedBy, &c.UpdatedAt); err != nil {
		return err
	}
	c.ValidFrom, c.ValidTo = day(from), dayOrNil(to)
	return nil
}

func loadCategoryVendor(ctx context.Context, tx pgx.Tx, id string) (domain.CategoryVendor, error) {
	p, _ := tenant.FromContext(ctx)
	var c domain.CategoryVendor
	err := scanCategoryVendor(tx.QueryRow(ctx, `select `+categoryVendorColumns+` from procurement_category_vendor
		where tenant_id = $1 and id = $2`, p.TenantID, id), &c)
	return c, notFound(err, CodeCategoryVendorNotFound, "category vendor assignment not found")
}

func categoryVendorData(c domain.CategoryVendor, by string) purchasingevents.CategoryVendorData {
	d := purchasingevents.CategoryVendorData{AssignmentID: c.ID, LegalEntityID: c.LegalEntityID, Category: c.Category,
		VendorID: c.VendorID, Status: c.Status, ValidFrom: c.ValidFrom.String(), Version: c.Version, ChangedBy: by}
	if c.ValidTo != nil {
		d.ValidTo = c.ValidTo.String()
	}
	return d
}

func categoryVendorExists(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" && pe.ConstraintName == "procurement_category_vendor_unique" {
		return httpx.NewProblem(http.StatusConflict, CodeCategoryVendorExists,
			"the vendor already has an assignment for this category; change that assignment instead")
	}
	return err
}

// CreateCategoryVendor assigns a vendor to a category (version 1) and writes
// categoryvendor.assigned.v1 in the same transaction. A replayed Idempotency-Key returns the
// assignment the first request created; a second assignment of the same vendor to the same
// category is 409 PRC-CAT-011.
func (s *Postgres) CreateCategoryVendor(ctx context.Context, in domain.CategoryVendor, idem Idem) (domain.CategoryVendor, error) {
	p, _ := tenant.FromContext(ctx)
	in.ID = ids.New()
	var out domain.CategoryVendor
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		existing, replay, err := claimKey(ctx, tx, idem, in.ID)
		if err != nil {
			return err
		}
		if replay {
			out, err = loadCategoryVendor(ctx, tx, existing)
			return err
		}
		if _, err := tx.Exec(ctx, `insert into procurement_category_vendor (tenant_id, id, legal_entity_id, category, vendor_id, status,
			valid_from, valid_to, notes, created_by, updated_by) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)`,
			p.TenantID, in.ID, in.LegalEntityID, in.Category, in.VendorID, in.Status, in.ValidFrom.Time, dayPtr(in.ValidTo), in.Notes,
			userOrNil(p.UserID)); err != nil {
			return categoryVendorExists(err)
		}
		if out, err = loadCategoryVendor(ctx, tx, in.ID); err != nil {
			return err
		}
		return enqueue(ctx, tx, "categoryvendor", out.ID, purchasingevents.CategoryVendorAssigned, categoryVendorData(out, p.UserID))
	})
	return out, err
}

// GetCategoryVendor returns one assignment.
func (s *Postgres) GetCategoryVendor(ctx context.Context, id string) (domain.CategoryVendor, error) {
	var out domain.CategoryVendor
	err := s.withTx(ctx, func(tx pgx.Tx) (err error) {
		out, err = loadCategoryVendor(ctx, tx, id)
		return err
	})
	return out, err
}

// ListCategoryVendors returns assignments newest first, after the cursor id.
func (s *Postgres) ListCategoryVendors(ctx context.Context, f CategoryVendorFilter, after string, limit int) (Page[domain.CategoryVendor], error) {
	p, _ := tenant.FromContext(ctx)
	limit = clampLimit(limit)
	items := []domain.CategoryVendor{}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+categoryVendorColumns+` from procurement_category_vendor
			where tenant_id = $1 and ($2::uuid is null or legal_entity_id = $2::uuid) and ($3 = '' or category = $3)
			and ($4::uuid is null or vendor_id = $4::uuid) and ($5 = '' or status = $5) and ($6::uuid is null or id < $6::uuid)
			order by id desc limit $7`, p.TenantID, nullable(f.LegalEntityID), f.Category, nullable(f.VendorID), f.Status, nullable(after), limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c domain.CategoryVendor
			if err := scanCategoryVendor(rows, &c); err != nil {
				return err
			}
			items = append(items, c)
		}
		return rows.Err()
	})
	if err != nil {
		return Page[domain.CategoryVendor]{}, err
	}
	return pageOf(items, limit, func(c domain.CategoryVendor) string { return c.ID }), nil
}

// UpdateCategoryVendor replaces the status, validity window and notes of an assignment under
// If-Match and writes categoryvendor.changed.v1. A stale version is 412 PRC-CAT-013.
func (s *Postgres) UpdateCategoryVendor(ctx context.Context, id string, ifMatch int, status string, from domain.Day, to *domain.Day, notes string) (domain.CategoryVendor, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.CategoryVendor
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		cur, err := loadCategoryVendor(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.Version != ifMatch {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeCategoryVendorStale,
				fmt.Sprintf("category assignment is at version %d, not %d; reload and retry", cur.Version, ifMatch))
		}
		// The version predicate closes the gap between the read above and this write: of two
		// changes made from the same version only the first applies.
		tag, err := tx.Exec(ctx, `update procurement_category_vendor set status = $3, valid_from = $4, valid_to = $5, notes = $6,
			updated_by = $7, updated_at = now(), row_version = row_version + 1 where tenant_id = $1 and id = $2 and row_version = $8`,
			p.TenantID, id, status, from.Time, dayPtr(to), notes, userOrNil(p.UserID), ifMatch)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeCategoryVendorStale,
				fmt.Sprintf("category assignment changed after version %d; reload and retry", ifMatch))
		}
		if out, err = loadCategoryVendor(ctx, tx, id); err != nil {
			return err
		}
		return enqueue(ctx, tx, "categoryvendor", id, purchasingevents.CategoryVendorChanged, categoryVendorData(out, p.UserID))
	})
	return out, err
}

// CategoryAssignments returns every assignment of the legal entity in the given categories
// (all statuses and windows), for the category access rule.
func (s *Postgres) CategoryAssignments(ctx context.Context, legalEntityID string, categories []string) ([]domain.CategoryVendor, error) {
	p, _ := tenant.FromContext(ctx)
	out := []domain.CategoryVendor{}
	if len(categories) == 0 {
		return out, nil
	}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+categoryVendorColumns+` from procurement_category_vendor
			where tenant_id = $1 and legal_entity_id = $2 and category = any($3) order by id`, p.TenantID, legalEntityID, categories)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c domain.CategoryVendor
			if err := scanCategoryVendor(rows, &c); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

const categoryManagerColumns = `id, legal_entity_id, category, worker_id, coalesce(created_by::text, ''), created_at`

func scanCategoryManager(row pgx.Row, m *domain.CategoryManager) error {
	return row.Scan(&m.ID, &m.LegalEntityID, &m.Category, &m.WorkerID, &m.CreatedBy, &m.CreatedAt)
}

func loadCategoryManager(ctx context.Context, tx pgx.Tx, id string) (domain.CategoryManager, error) {
	p, _ := tenant.FromContext(ctx)
	var m domain.CategoryManager
	err := scanCategoryManager(tx.QueryRow(ctx, `select `+categoryManagerColumns+` from procurement_category_manager
		where tenant_id = $1 and id = $2`, p.TenantID, id), &m)
	return m, notFound(err, CodeCategoryManagerNotFound, "category manager not found")
}

func categoryManagerData(m domain.CategoryManager, by string) purchasingevents.CategoryManagerData {
	return purchasingevents.CategoryManagerData{ManagerID: m.ID, LegalEntityID: m.LegalEntityID, Category: m.Category,
		WorkerID: m.WorkerID, ChangedBy: by}
}

// CreateCategoryManager makes a worker manager of a category and writes
// categorymanager.assigned.v1. A replayed Idempotency-Key returns the first row; the same
// worker twice on a category is 409 PRC-CAT-012.
func (s *Postgres) CreateCategoryManager(ctx context.Context, in domain.CategoryManager, idem Idem) (domain.CategoryManager, error) {
	p, _ := tenant.FromContext(ctx)
	in.ID = ids.New()
	var out domain.CategoryManager
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		existing, replay, err := claimKey(ctx, tx, idem, in.ID)
		if err != nil {
			return err
		}
		if replay {
			out, err = loadCategoryManager(ctx, tx, existing)
			return err
		}
		if _, err := tx.Exec(ctx, `insert into procurement_category_manager (tenant_id, id, legal_entity_id, category, worker_id, created_by)
			values ($1, $2, $3, $4, $5, $6)`, p.TenantID, in.ID, in.LegalEntityID, in.Category, in.WorkerID, userOrNil(p.UserID)); err != nil {
			var pe *pgconn.PgError
			if errors.As(err, &pe) && pe.Code == "23505" && pe.ConstraintName == "procurement_category_manager_unique" {
				return httpx.NewProblem(http.StatusConflict, CodeCategoryManagerExists, "the worker already manages this category")
			}
			return err
		}
		if out, err = loadCategoryManager(ctx, tx, in.ID); err != nil {
			return err
		}
		return enqueue(ctx, tx, "categorymanager", out.ID, purchasingevents.CategoryManagerAssigned, categoryManagerData(out, p.UserID))
	})
	return out, err
}

// ListCategoryManagers returns manager rows newest first, after the cursor id.
func (s *Postgres) ListCategoryManagers(ctx context.Context, f CategoryManagerFilter, after string, limit int) (Page[domain.CategoryManager], error) {
	p, _ := tenant.FromContext(ctx)
	limit = clampLimit(limit)
	items := []domain.CategoryManager{}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+categoryManagerColumns+` from procurement_category_manager
			where tenant_id = $1 and ($2::uuid is null or legal_entity_id = $2::uuid) and ($3 = '' or category = $3)
			and ($4::uuid is null or worker_id = $4::uuid) and ($5::uuid is null or id < $5::uuid)
			order by id desc limit $6`, p.TenantID, nullable(f.LegalEntityID), f.Category, nullable(f.WorkerID), nullable(after), limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m domain.CategoryManager
			if err := scanCategoryManager(rows, &m); err != nil {
				return err
			}
			items = append(items, m)
		}
		return rows.Err()
	})
	if err != nil {
		return Page[domain.CategoryManager]{}, err
	}
	return pageOf(items, limit, func(m domain.CategoryManager) string { return m.ID }), nil
}

// DeleteCategoryManager removes a manager row and writes categorymanager.removed.v1.
func (s *Postgres) DeleteCategoryManager(ctx context.Context, id string) error {
	p, _ := tenant.FromContext(ctx)
	return s.withTx(ctx, func(tx pgx.Tx) error {
		cur, err := loadCategoryManager(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from procurement_category_manager where tenant_id = $1 and id = $2`, p.TenantID, id); err != nil {
			return err
		}
		return enqueue(ctx, tx, "categorymanager", id, purchasingevents.CategoryManagerRemoved, categoryManagerData(cur, p.UserID))
	})
}
