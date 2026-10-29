package jobs

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	PageOK     = "ok"
	PageFailed = "failed"
)

var ErrRunNotFound = errors.New("batch run not found")

type Page struct {
	No         int
	Cursor     string
	NextCursor string
	Items      int
	Succeeded  int
	Skipped    int
	Status     string
	Error      string
}

type Resume struct {
	NextPage int
	Cursor   string
	Done     bool
}

type PageStore interface {
	Resume(ctx context.Context, runID string) (Resume, error)
	Record(ctx context.Context, runID string, page Page) error
	Finish(ctx context.Context, runID, status, errMsg string) error
}

type PgPageStore struct{ pool *pgxpool.Pool }

func NewPgPageStore(pool *pgxpool.Pool) *PgPageStore { return &PgPageStore{pool: pool} }

func tenantID(ctx context.Context) (string, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return "", fmt.Errorf("batch pages: no principal in context")
	}
	return p.TenantID, nil
}

func (s *PgPageStore) Resume(ctx context.Context, runID string) (Resume, error) {
	tenantID, err := tenantID(ctx)
	if err != nil {
		return Resume{}, err
	}
	var out Resume
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var last int
		var next string
		err := tx.QueryRow(ctx, `
			select page_no, next_cursor from batch_run_pages
			where tenant_id = $1 and run_id = $2 and status = 'ok'
			order by page_no desc limit 1`, tenantID, runID).Scan(&last, &next)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		out = Resume{NextPage: last + 1, Cursor: next, Done: next == ""}
		return nil
	})
	if err != nil {
		return Resume{}, fmt.Errorf("read resume point: %w", err)
	}
	return out, nil
}

func (s *PgPageStore) Record(ctx context.Context, runID string, page Page) error {
	tenantID, err := tenantID(ctx)
	if err != nil {
		return err
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			insert into batch_run_pages (tenant_id, run_id, page_no, cursor, next_cursor, items, succeeded, skipped, status, error)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, nullif($10, ''))
			on conflict (tenant_id, run_id, page_no) do update
			set cursor = excluded.cursor, next_cursor = excluded.next_cursor, items = excluded.items,
			    succeeded = excluded.succeeded, skipped = excluded.skipped, status = excluded.status,
			    error = excluded.error, updated_at = now()
			where batch_run_pages.status <> 'ok'`,
			tenantID, runID, page.No, page.Cursor, page.NextCursor, page.Items, page.Succeeded, page.Skipped, page.Status, page.Error,
		); err != nil {
			return fmt.Errorf("upsert page: %w", err)
		}
		tag, err := tx.Exec(ctx, `
			update batch_run set rows_done = (
				select coalesce(sum(succeeded + skipped), 0) from batch_run_pages
				where tenant_id = $1 and run_id = $2 and status = 'ok')
			where tenant_id = $1 and id = $2`, tenantID, runID)
		if err != nil {
			return fmt.Errorf("update run progress: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrRunNotFound
		}
		return nil
	})
	return err
}

func (s *PgPageStore) Finish(ctx context.Context, runID, status, errMsg string) error {
	tenantID, err := tenantID(ctx)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			update batch_run set status = $3, ended_at = now(), error = nullif($4, '')
			where tenant_id = $1 and id = $2`, tenantID, runID, status, errMsg)
		if err != nil {
			return fmt.Errorf("finish run: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrRunNotFound
		}
		return nil
	})
}
