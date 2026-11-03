package store

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/projects/internal/domain"
)

// CheckExpected applies the If-Match rule of the setup upserts. expected is the
// version the caller read; zero means no If-Match was sent. A missing row is
// created only without If-Match (412 otherwise); replacing an existing row
// needs If-Match (428) with the current version (412 when stale).
func CheckExpected(expected int, exists bool, current int) error {
	switch {
	case !exists && expected != 0:
		return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "the row does not exist; send no If-Match to create it")
	case exists && expected == 0:
		return httpx.NewProblem(http.StatusPreconditionRequired, CodePrecondition, "If-Match with the current version is required to replace this row")
	case exists && expected != current:
		return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "the row changed since it was read; re-read and retry")
	}
	return nil
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// lockedVersion reads and locks the version of one row; exists is false when
// there is no row. A concurrent first insert of the same key is caught by the
// primary key (see concurrentCreate).
func lockedVersion(ctx context.Context, tx pgx.Tx, query string, args ...any) (version int, exists bool, err error) {
	err = tx.QueryRow(ctx, query, args...).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	return version, err == nil, err
}

const groupColumns = `id, name, coalesce(default_project_type, ''), version, created_at, updated_at`

func scanGroup(row pgx.Row) (domain.ProjectGroup, error) {
	var g domain.ProjectGroup
	var ptype string
	err := row.Scan(&g.ID, &g.Name, &ptype, &g.Version, &g.CreatedAt, &g.UpdatedAt)
	g.DefaultProjectType = domain.ProjectType(ptype)
	return g, err
}

// UpsertProjectGroup creates or replaces a project group under CheckExpected.
func (s *Postgres) UpsertProjectGroup(ctx context.Context, g domain.ProjectGroup, expected int) (domain.ProjectGroup, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.ProjectGroup
	var created bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, exists, err := lockedVersion(ctx, tx, `select version from project_group where tenant_id = $1 and id = $2 for update`, p.TenantID, g.ID)
		if err != nil {
			return err
		}
		if err := CheckExpected(expected, exists, current); err != nil {
			return err
		}
		if exists {
			out, err = scanGroup(tx.QueryRow(ctx, `update project_group set name = $3, default_project_type = $4, version = version + 1, updated_at = now()
				where tenant_id = $1 and id = $2 returning `+groupColumns, p.TenantID, g.ID, g.Name, nullable(string(g.DefaultProjectType))))
			return err
		}
		out, err = scanGroup(tx.QueryRow(ctx, `insert into project_group (tenant_id, id, name, default_project_type)
			values ($1, $2, $3, $4) returning `+groupColumns, p.TenantID, g.ID, g.Name, nullable(string(g.DefaultProjectType))))
		created = err == nil
		return err
	})
	return out, created, concurrentCreate(err)
}

// concurrentCreate maps the primary-key violation of two first inserts racing
// on one key to the same 412 a stale If-Match gets: the loser re-reads.
func concurrentCreate(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "the row was created concurrently; re-read and retry")
	}
	return err
}

// ListProjectGroups returns every project group of the caller's tenant, by id.
func (s *Postgres) ListProjectGroups(ctx context.Context) ([]domain.ProjectGroup, error) {
	p, _ := tenant.FromContext(ctx)
	out := []domain.ProjectGroup{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+groupColumns+` from project_group where tenant_id = $1 order by id`, p.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			g, err := scanGroup(rows)
			if err != nil {
				return err
			}
			out = append(out, g)
		}
		return rows.Err()
	})
	return out, err
}

const profileColumns = `project_group, category, line_property, cost_account, wip_account, accrued_revenue_account,
	invoiced_revenue_account, on_account_account, coalesce(offset_account, ''), coalesce(cost_of_sales_account, ''),
	coalesce(loss_provision_account, ''), coalesce(loss_expense_account, ''), version`

func scanProfile(row pgx.Row) (domain.PostingProfile, error) {
	var pr domain.PostingProfile
	err := row.Scan(&pr.Key.ProjectGroup, &pr.Key.Category, &pr.Key.LineProperty, &pr.CostAccount, &pr.WIPAccount,
		&pr.AccruedRevenueAccount, &pr.InvoicedRevenueAccount, &pr.OnAccountAccount, &pr.OffsetAccount, &pr.CostOfSalesAccount,
		&pr.LossProvisionAccount, &pr.LossExpenseAccount, &pr.Version)
	return pr, err
}

// UpsertPostingProfile creates or replaces the posting profile of one key
// (project group, category, line property) under CheckExpected. Wildcards are
// stored as "*".
func (s *Postgres) UpsertPostingProfile(ctx context.Context, pr domain.PostingProfile, expected int) (domain.PostingProfile, bool, error) {
	p, _ := tenant.FromContext(ctx)
	k := pr.Key
	var out domain.PostingProfile
	var created bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, exists, err := lockedVersion(ctx, tx, `select version from posting_profile where tenant_id = $1 and project_group = $2
			and category = $3 and line_property = $4 for update`, p.TenantID, k.ProjectGroup, k.Category, k.LineProperty)
		if err != nil {
			return err
		}
		if err := CheckExpected(expected, exists, current); err != nil {
			return err
		}
		if exists {
			out, err = scanProfile(tx.QueryRow(ctx, `update posting_profile set cost_account = $5, wip_account = $6, accrued_revenue_account = $7,
				invoiced_revenue_account = $8, on_account_account = $9, offset_account = nullif($10, ''), cost_of_sales_account = nullif($11, ''),
				loss_provision_account = nullif($12, ''), loss_expense_account = nullif($13, ''), version = version + 1, updated_at = now()
				where tenant_id = $1 and project_group = $2 and category = $3 and line_property = $4
				returning `+profileColumns,
				p.TenantID, k.ProjectGroup, k.Category, k.LineProperty, pr.CostAccount, pr.WIPAccount,
				pr.AccruedRevenueAccount, pr.InvoicedRevenueAccount, pr.OnAccountAccount, pr.OffsetAccount, pr.CostOfSalesAccount,
				pr.LossProvisionAccount, pr.LossExpenseAccount))
			return err
		}
		out, err = scanProfile(tx.QueryRow(ctx, `insert into posting_profile (tenant_id, project_group, category, line_property, cost_account,
			wip_account, accrued_revenue_account, invoiced_revenue_account, on_account_account, offset_account, cost_of_sales_account,
			loss_provision_account, loss_expense_account)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, nullif($10, ''), nullif($11, ''), nullif($12, ''), nullif($13, '')) returning `+profileColumns,
			p.TenantID, k.ProjectGroup, k.Category, k.LineProperty, pr.CostAccount, pr.WIPAccount,
			pr.AccruedRevenueAccount, pr.InvoicedRevenueAccount, pr.OnAccountAccount, pr.OffsetAccount, pr.CostOfSalesAccount,
			pr.LossProvisionAccount, pr.LossExpenseAccount))
		created = err == nil
		return err
	})
	return out, created, concurrentCreate(err)
}

// ListPostingProfiles returns every posting profile of the caller's tenant, ordered by key.
func (s *Postgres) ListPostingProfiles(ctx context.Context) ([]domain.PostingProfile, error) {
	p, _ := tenant.FromContext(ctx)
	var out []domain.PostingProfile
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = loadProfiles(ctx, tx, p.TenantID)
		return err
	})
	return out, err
}
