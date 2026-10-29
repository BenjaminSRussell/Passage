package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
)

// XrefQuery resolves legacy_xref rows either by the legacy record (System, Table and SourceID,
// optionally Company) or by the Passage record (PassageEntity and PassageID).
type XrefQuery struct {
	System        string
	Company       string
	Table         string
	SourceID      string
	PassageEntity string
	PassageID     string
}

// LookupXref returns the matching cross-references, at most MaxListLimit, ordered by company.
// The natural-key index serves the legacy lookup and legacy_xref_passage the Passage one.
func (s *Postgres) LookupXref(ctx context.Context, q XrefQuery) ([]domain.Xref, error) {
	out := []domain.Xref{}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var rows pgx.Rows
		var err error
		const cols = `xref_id::text, source_system, source_company, source_table, source_id, passage_entity, passage_id::text, run_id::text, created_at`
		if q.PassageID != "" {
			rows, err = tx.Query(ctx, `select `+cols+` from legacy_xref
				where tenant_id = current_setting('passage.tenant_id', true)::uuid and passage_entity = $1 and passage_id = $2::uuid
				order by source_system, source_company, source_table, source_id limit $3`, q.PassageEntity, q.PassageID, MaxListLimit)
		} else {
			rows, err = tx.Query(ctx, `select `+cols+` from legacy_xref
				where tenant_id = current_setting('passage.tenant_id', true)::uuid and source_system = $1 and source_table = $2 and source_id = $3
				  and ($4 = '' or source_company = $4)
				order by source_company limit $5`, q.System, q.Table, q.SourceID, q.Company, MaxListLimit)
		}
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var x domain.Xref
			var sys string
			if err := rows.Scan(&x.XrefID, &sys, &x.SourceCompany, &x.SourceTable, &x.SourceID, &x.PassageEntity, &x.PassageID, &x.RunID, &x.CreatedAt); err != nil {
				return err
			}
			x.SourceSystem = domain.SourceSystem(sys)
			out = append(out, x)
		}
		return rows.Err()
	})
	return out, err
}
