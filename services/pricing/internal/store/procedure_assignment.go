package store

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	pevents "github.com/BenjaminSRussell/passage/services/pricing/internal/events"
)

// ProcedureAssignment says which pricing procedure a legal entity prices with in one sales
// channel (PRD 5.26.13, reference_procedure). An empty SalesChannel is the legal entity's
// default. ProcedureName names the procedure; the ACTIVE version of that name is used.
type ProcedureAssignment struct {
	LegalEntityID string
	SalesChannel  string
	ProcedureName string
	Version       int
	UpdatedBy     string
	UpdatedAt     *time.Time
}

const assignmentColumns = `legal_entity_id::text, sales_channel, procedure_name, version, updated_by, updated_at`

func scanAssignment(row pgx.Row) (ProcedureAssignment, error) {
	var a ProcedureAssignment
	err := row.Scan(&a.LegalEntityID, &a.SalesChannel, &a.ProcedureName, &a.Version, &a.UpdatedBy, &a.UpdatedAt)
	return a, err
}

func procedureNotActive(name string) error {
	return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeProcedureNotActive, "pricing procedure "+name+" has no active version")
}

// activeProcedure reads the ACTIVE version of a named procedure of a legal entity.
func activeProcedure(ctx context.Context, tx pgx.Tx, legalEntityID, name string) (*domain.Procedure, error) {
	p, err := scanProcedure(tx.QueryRow(ctx, `select `+procedureColumns+` from pricing_procedure
		where legal_entity_id = $1 and name = $2 and status = 'ACTIVE'`, legalEntityID, name))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, procedureNotActive(name)
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// SetProcedureAssignment sets the procedure a legal entity and sales channel price with.
// The procedure must have an ACTIVE version (422 PRC-0063). version is the stored version the
// caller read (0 when none was stored); a different one is 412 conflict_version. The change is
// announced as procedureassignment.changed in the same transaction. A repeated
// Idempotency-Key replays the first response.
func (s *Postgres) SetProcedureAssignment(ctx context.Context, in ProcedureAssignment, version int, idempotencyKey string) (ProcedureAssignment, error) {
	p, _ := tenant.FromContext(ctx)
	var out ProcedureAssignment
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		hash := commandHash("procedure-assignment-set", in.LegalEntityID, in.SalesChannel, in.ProcedureName, strconv.Itoa(version))
		if ok, err := replayJSON(ctx, tx, p.TenantID, idempotencyKey, hash, &out); err != nil || ok {
			return err
		}
		if _, err := activeProcedure(ctx, tx, in.LegalEntityID, in.ProcedureName); err != nil {
			return err
		}
		var q string
		args := []any{in.LegalEntityID, in.SalesChannel, in.ProcedureName, p.UserID, p.TenantID, version}
		if version == 0 {
			q = `insert into pricing_procedure_assignment (legal_entity_id, sales_channel, procedure_name, updated_by, tenant_id)
				values ($1, $2, $3, $4, $5) on conflict (tenant_id, legal_entity_id, sales_channel) do nothing returning ` + assignmentColumns
			args = args[:5]
		} else {
			q = `update pricing_procedure_assignment set procedure_name = $3, version = version + 1, updated_by = $4, updated_at = now()
				where tenant_id = $5 and legal_entity_id = $1 and sales_channel = $2 and version = $6 returning ` + assignmentColumns
		}
		var err error
		out, err = scanAssignment(tx.QueryRow(ctx, q, args...))
		// An insert that lost the race, or an update that matched no version, changed no row.
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "procedure assignment version does not match")
		}
		if err != nil {
			return err
		}
		if err := enqueueEvent(ctx, tx, "procedureassignment", in.LegalEntityID, pevents.ProcedureAssignmentChanged, assignmentEvent(out, false)); err != nil {
			return err
		}
		return rememberJSON(ctx, tx, p.TenantID, idempotencyKey, hash, out)
	})
	return out, err
}

func assignmentEvent(a ProcedureAssignment, cleared bool) pevents.ProcedureAssignmentChangedData {
	return pevents.ProcedureAssignmentChangedData{LegalEntityID: a.LegalEntityID, SalesChannel: a.SalesChannel,
		ProcedureName: a.ProcedureName, Cleared: cleared, Version: a.Version, UpdatedBy: a.UpdatedBy}
}

// ListProcedureAssignments returns the assignments of a legal entity, the default first.
func (s *Postgres) ListProcedureAssignments(ctx context.Context, legalEntityID string) ([]ProcedureAssignment, error) {
	out := []ProcedureAssignment{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+assignmentColumns+` from pricing_procedure_assignment
			where legal_entity_id = $1 order by sales_channel`, legalEntityID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanAssignment(rows)
			if err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}

// ClearProcedureAssignment removes an assignment at the expected version (412 conflict_version
// otherwise, also when none is stored) and announces it as procedureassignment.changed with
// cleared set. A repeated Idempotency-Key replays the first success.
func (s *Postgres) ClearProcedureAssignment(ctx context.Context, legalEntityID, salesChannel string, version int, idempotencyKey string) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		hash := commandHash("procedure-assignment-clear", legalEntityID, salesChannel, strconv.Itoa(version))
		var replay ProcedureAssignment
		if ok, err := replayJSON(ctx, tx, p.TenantID, idempotencyKey, hash, &replay); err != nil || ok {
			return err
		}
		out, err := scanAssignment(tx.QueryRow(ctx, `delete from pricing_procedure_assignment
			where tenant_id = $1 and legal_entity_id = $2 and sales_channel = $3 and version = $4 returning `+assignmentColumns,
			p.TenantID, legalEntityID, salesChannel, version))
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "procedure assignment version does not match")
		}
		if err != nil {
			return err
		}
		out.UpdatedBy = p.UserID
		if err := enqueueEvent(ctx, tx, "procedureassignment", legalEntityID, pevents.ProcedureAssignmentChanged, assignmentEvent(out, true)); err != nil {
			return err
		}
		return rememberJSON(ctx, tx, p.TenantID, idempotencyKey, hash, out)
	})
}

// DetermineProcedure picks the procedure a resolve prices with. A non-empty name selects the
// ACTIVE version of that procedure (422 PRC-0063 when it has none). Otherwise the sales
// channel's assignment applies, then the legal entity's default assignment; with neither the
// result is nil and the reference order runs.
func (s *Postgres) DetermineProcedure(ctx context.Context, legalEntityID, salesChannel, name string) (*domain.Procedure, error) {
	var out *domain.Procedure
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if name == "" {
			err := tx.QueryRow(ctx, `select procedure_name from pricing_procedure_assignment
				where legal_entity_id = $1 and sales_channel in ($2, '') order by sales_channel desc limit 1`,
				legalEntityID, salesChannel).Scan(&name)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
		}
		var err error
		out, err = activeProcedure(ctx, tx, legalEntityID, name)
		return err
	})
	return out, err
}
