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

// CodeScheduleNotFound is raised when a distribution schedule code does not
// exist. Slice-local, like REPORT-0020.
const CodeScheduleNotFound = "REPORT-0019"

// ListSchedules returns every distribution schedule of the tenant, by code.
func (s *Postgres) ListSchedules(ctx context.Context) ([]domain.Schedule, error) {
	out := []domain.Schedule{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select body from report_schedule order by code`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var body []byte
			if err := rows.Scan(&body); err != nil {
				return err
			}
			var sc domain.Schedule
			if err := json.Unmarshal(body, &sc); err != nil {
				return err
			}
			out = append(out, sc)
		}
		return rows.Err()
	})
	return out, err
}

// GetSchedule returns one schedule and its revision, which the client echoes in
// If-Match on the next update.
func (s *Postgres) GetSchedule(ctx context.Context, code string) (domain.Schedule, int, error) {
	var sc domain.Schedule
	var rev int
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var body []byte
		err := tx.QueryRow(ctx, `select body, revision from report_schedule where code=$1`, code).Scan(&body, &rev)
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(404, CodeScheduleNotFound, fmt.Sprintf("schedule %s does not exist", code))
		}
		if err != nil {
			return err
		}
		return json.Unmarshal(body, &sc)
	})
	return sc, rev, err
}

// SaveSchedule creates or replaces a schedule (FR-15.8). The definition must
// have a published version (REPORT-0001), checked in the same transaction. The
// revision rule is the one of SaveDraft: creating needs no If-Match; once the
// schedule exists, If-Match must carry the revision read (428 when missing,
// 412 conflict_version when stale). The new revision is returned.
func (s *Postgres) SaveSchedule(ctx context.Context, sc domain.Schedule, ifMatch *int) (domain.Schedule, int, error) {
	var rev int
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var published bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from report_definition where code=$1 and status='published')`,
			sc.Definition).Scan(&published); err != nil {
			return err
		}
		if !published {
			return problem(422, CodeNotPublished, fmt.Sprintf("Definition %s has no published version.", sc.Definition))
		}
		body, err := json.Marshal(sc)
		if err != nil {
			return err
		}
		var current int
		err = tx.QueryRow(ctx, `select revision from report_schedule where code=$1 for update`, sc.Code).Scan(&current)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			rev = 1
			_, err = tx.Exec(ctx, `insert into report_schedule(tenant_id, id, code, definition_code, enabled, revision, body, updated_by)
				values($1,$2,$3,$4,$5,1,$6,$7)`, tenantID(ctx), ids.New(), sc.Code, sc.Definition, sc.Enabled, body, sc.UpdatedBy)
			if isUniqueViolation(err) {
				return problem(409, CodeRevisionConflict, "schedule "+sc.Code+" was created concurrently")
			}
			if err != nil {
				return err
			}
			return auditScheduleTx(ctx, tx, sc, rev)
		case err != nil:
			return err
		}
		if ifMatch == nil {
			return problem(428, "precondition_required", "If-Match is required to replace schedule "+sc.Code)
		}
		if *ifMatch != current {
			return problem(412, CodeRevisionConflict, fmt.Sprintf("schedule revision is %d, not %d", current, *ifMatch))
		}
		rev = current + 1
		_, err = tx.Exec(ctx, `update report_schedule set definition_code=$2, enabled=$3, body=$4, revision=$5, updated_by=$6, updated_at=now()
			where code=$1`, sc.Code, sc.Definition, sc.Enabled, body, rev, sc.UpdatedBy)
		if err != nil {
			return err
		}
		return auditScheduleTx(ctx, tx, sc, rev)
	})
	return sc, rev, err
}

// auditScheduleTx records a schedule save in the audit trail inside the save
// transaction.
func auditScheduleTx(ctx context.Context, tx pgx.Tx, sc domain.Schedule, rev int) error {
	return recordAuditTx(ctx, tx, domain.AuditEntry{Kind: domain.AuditScheduleSaved, ActorID: sc.UpdatedBy,
		ObjectRef: "schedule:" + sc.Code, Details: map[string]any{
			"code": sc.Code, "revision": rev, "definition": sc.Definition, "enabled": sc.Enabled,
			"formats": sc.Formats, "recipients": len(sc.Recipients),
		}})
}
