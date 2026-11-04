package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
)

// partTable names the table of one kind of designer part. The names are
// constants of this file, never request input.
type partTable string

const (
	rowSetTable    partTable = "report_row_set"
	columnSetTable partTable = "report_column_set"
)

// listPart returns the bodies of every part of the tenant, by code.
func listPart[T any](ctx context.Context, s *Postgres, table partTable) ([]T, error) {
	out := []T{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select body from `+string(table)+` order by code`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var body []byte
			if err := rows.Scan(&body); err != nil {
				return err
			}
			var v T
			if err := json.Unmarshal(body, &v); err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}

// getPart returns one part and its revision, which the client echoes in If-Match.
func getPart[T any](ctx context.Context, s *Postgres, table partTable, noun, code string) (T, int, error) {
	var v T
	var rev int
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var body []byte
		err := tx.QueryRow(ctx, `select body, revision from `+string(table)+` where code=$1`, code).Scan(&body, &rev)
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(404, domain.CodePartNotFound, fmt.Sprintf("%s %s does not exist", noun, code))
		}
		if err != nil {
			return err
		}
		return json.Unmarshal(body, &v)
	})
	return v, rev, err
}

// savePart creates or replaces a part. Creating needs no If-Match; once the
// part exists, If-Match must carry the revision read (428 when missing, 412
// conflict_version when stale), the rule of SaveDraft and SaveSchedule.
func savePart(ctx context.Context, s *Postgres, table partTable, noun, code, updatedBy string, v any, ifMatch *int) (int, error) {
	var rev int
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		body, err := json.Marshal(v)
		if err != nil {
			return err
		}
		var current int
		err = tx.QueryRow(ctx, `select revision from `+string(table)+` where code=$1 for update`, code).Scan(&current)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			rev = 1
			_, err = tx.Exec(ctx, `insert into `+string(table)+`(tenant_id, id, code, revision, body, updated_by) values($1,$2,$3,1,$4,$5)`,
				tenantID(ctx), ids.New(), code, body, updatedBy)
			if isUniqueViolation(err) {
				return problem(409, CodeRevisionConflict, noun+" "+code+" was created concurrently")
			}
			return err
		case err != nil:
			return err
		}
		if ifMatch == nil {
			return problem(428, "precondition_required", "If-Match is required to replace "+noun+" "+code)
		}
		if *ifMatch != current {
			return problem(412, CodeRevisionConflict, fmt.Sprintf("%s revision is %d, not %d", noun, current, *ifMatch))
		}
		rev = current + 1
		_, err = tx.Exec(ctx, `update `+string(table)+` set body=$2, revision=$3, updated_by=$4, updated_at=now() where code=$1`,
			code, body, rev, updatedBy)
		return err
	})
	return rev, err
}

// ListRowSets returns every row set of the tenant, by code.
func (s *Postgres) ListRowSets(ctx context.Context) ([]domain.RowSet, error) {
	return listPart[domain.RowSet](ctx, s, rowSetTable)
}

// GetRowSet returns one row set and its revision.
func (s *Postgres) GetRowSet(ctx context.Context, code string) (domain.RowSet, int, error) {
	return getPart[domain.RowSet](ctx, s, rowSetTable, "row set", code)
}

// SaveRowSet creates or replaces a row set (FR-15.2) and returns its new revision.
func (s *Postgres) SaveRowSet(ctx context.Context, rs domain.RowSet, ifMatch *int) (domain.RowSet, int, error) {
	rev, err := savePart(ctx, s, rowSetTable, "row set", rs.Code, rs.UpdatedBy, rs, ifMatch)
	return rs, rev, err
}

// ListColumnSets returns every column set of the tenant, by code.
func (s *Postgres) ListColumnSets(ctx context.Context) ([]domain.ColumnSet, error) {
	return listPart[domain.ColumnSet](ctx, s, columnSetTable)
}

// GetColumnSet returns one column set and its revision.
func (s *Postgres) GetColumnSet(ctx context.Context, code string) (domain.ColumnSet, int, error) {
	return getPart[domain.ColumnSet](ctx, s, columnSetTable, "column set", code)
}

// SaveColumnSet creates or replaces a column set (FR-15.3) and returns its new revision.
func (s *Postgres) SaveColumnSet(ctx context.Context, cs domain.ColumnSet, ifMatch *int) (domain.ColumnSet, int, error) {
	rev, err := savePart(ctx, s, columnSetTable, "column set", cs.Code, cs.UpdatedBy, cs, ifMatch)
	return cs, rev, err
}
