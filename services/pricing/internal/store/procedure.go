package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	pevents "github.com/BenjaminSRussell/passage/services/pricing/internal/events"
)

const procedureColumns = `id::text, legal_entity_id::text, name, version, status, revision, steps`

func scanProcedure(row pgx.Row) (domain.Procedure, error) {
	var p domain.Procedure
	var steps []byte
	if err := row.Scan(&p.ID, &p.LegalEntityID, &p.Name, &p.Version, &p.Status, &p.Revision, &steps); err != nil {
		return domain.Procedure{}, err
	}
	if err := json.Unmarshal(steps, &p.Steps); err != nil {
		return domain.Procedure{}, err
	}
	return p, nil
}

func procedureNotFound(id string) error {
	return httpx.NewProblem(http.StatusNotFound, domain.CodeProcedureNotFound, "no pricing procedure "+id)
}

// stepsJSON is the stored form of a procedure's steps; it is also how replays compare them.
func stepsJSON(steps []domain.ProcedureStep) (string, error) {
	if steps == nil {
		steps = []domain.ProcedureStep{}
	}
	b, err := json.Marshal(steps)
	return string(b), err
}

// lockProcedureName serializes version numbering and activation of one procedure
// (tenant, legal entity, name) with a transaction-scoped advisory lock.
func lockProcedureName(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, name string) error {
	_, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, tenantID+"|procedure|"+legalEntityID+"|"+name)
	return err
}

// lockProcedure reads one version FOR UPDATE.
func lockProcedure(ctx context.Context, tx pgx.Tx, id string) (domain.Procedure, error) {
	p, err := scanProcedure(tx.QueryRow(ctx, `select `+procedureColumns+` from pricing_procedure where id = $1 for update`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Procedure{}, procedureNotFound(id)
	}
	return p, err
}

// CreateProcedure stores a DRAFT version with the next version number for its legal
// entity and name (FR-26.1). A repeated Idempotency-Key returns the original version, or
// idempotency_key_reuse when the body differs.
func (s *Postgres) CreateProcedure(ctx context.Context, in domain.Procedure, idempotencyKey string) (domain.Procedure, error) {
	tp, _ := tenant.FromContext(ctx)
	steps, err := stepsJSON(in.Steps)
	if err != nil {
		return domain.Procedure{}, err
	}
	var out domain.Procedure
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockProcedureName(ctx, tx, tp.TenantID, in.LegalEntityID, in.Name); err != nil {
			return err
		}
		if idempotencyKey != "" {
			replay, err := scanProcedure(tx.QueryRow(ctx, `select `+procedureColumns+` from pricing_procedure where idempotency_key = $1`, idempotencyKey))
			if err == nil {
				got, _ := stepsJSON(replay.Steps)
				if replay.LegalEntityID != in.LegalEntityID || replay.Name != in.Name || got != steps {
					return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyUse, "Idempotency-Key was already used with a different request body")
				}
				out = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		var key *string
		if idempotencyKey != "" {
			key = &idempotencyKey
		}
		var err error
		out, err = scanProcedure(tx.QueryRow(ctx, `insert into pricing_procedure (tenant_id, id, legal_entity_id, name, version, status, steps, idempotency_key)
			values ($1, $2, $3, $4,
				(select coalesce(max(version), 0) + 1 from pricing_procedure where legal_entity_id = $3 and name = $4),
				'DRAFT', $5::jsonb, $6)
			returning `+procedureColumns,
			tp.TenantID, ids.New(), in.LegalEntityID, in.Name, steps, key))
		return err
	})
	return out, err
}

// GetProcedure reads one version. PRC-0042 when it does not exist or lies outside the
// caller's legal entities.
func (s *Postgres) GetProcedure(ctx context.Context, id string) (domain.Procedure, error) {
	var out domain.Procedure
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = scanProcedure(tx.QueryRow(ctx, `select `+procedureColumns+` from pricing_procedure where id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return procedureNotFound(id)
		}
		return err
	})
	return out, err
}

// ListProcedures returns versions of one legal entity (all the caller may see when empty),
// by name and newest version first.
func (s *Postgres) ListProcedures(ctx context.Context, legalEntityID string, limit int) ([]domain.Procedure, error) {
	if limit <= 0 || limit > MaxListLimit {
		limit = MaxListLimit
	}
	out := []domain.Procedure{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+procedureColumns+` from pricing_procedure
			where ($1::text = '' or legal_entity_id::text = $1) order by legal_entity_id, name, version desc limit $2`, legalEntityID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanProcedure(rows)
			if err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

// ReviseProcedure replaces the steps of a DRAFT version at the expected revision (412
// conflict_version otherwise). ACTIVE and SUPERSEDED versions are immutable (409 PRC-0008).
// A repeated Idempotency-Key replays the first response.
func (s *Postgres) ReviseProcedure(ctx context.Context, id string, revision int, steps []domain.ProcedureStep, idempotencyKey string) (domain.Procedure, error) {
	tp, _ := tenant.FromContext(ctx)
	body, err := stepsJSON(steps)
	if err != nil {
		return domain.Procedure{}, err
	}
	var out domain.Procedure
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lockProcedure(ctx, tx, id)
		if err != nil {
			return err
		}
		hash := commandHash("procedure-revise", id, strconv.Itoa(revision), body)
		if ok, err := replayJSON(ctx, tx, tp.TenantID, idempotencyKey, hash, &out); err != nil || ok {
			return err
		}
		if cur.Status != domain.ProcedureDraft {
			return RuleProblem(domain.ProcedureImmutable(cur))
		}
		if cur.Revision != revision {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "procedure revision does not match")
		}
		out, err = scanProcedure(tx.QueryRow(ctx, `update pricing_procedure set steps = $2::jsonb, revision = revision + 1
			where id = $1 returning `+procedureColumns, id, body))
		if err != nil {
			return err
		}
		return rememberJSON(ctx, tx, tp.TenantID, idempotencyKey, hash, out)
	})
	return out, err
}

// ActivateProcedure makes a DRAFT version ACTIVE and supersedes the ACTIVE version of the
// same legal entity and name, in one transaction with procedure.activated in the outbox.
// A non-DRAFT version is refused with 409 PRC-0008; a repeated Idempotency-Key replays
// the first response.
func (s *Postgres) ActivateProcedure(ctx context.Context, id string, idempotencyKey string) (domain.Procedure, error) {
	tp, _ := tenant.FromContext(ctx)
	var out domain.Procedure
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lockProcedure(ctx, tx, id)
		if err != nil {
			return err
		}
		hash := commandHash("procedure-activate", id)
		if ok, err := replayJSON(ctx, tx, tp.TenantID, idempotencyKey, hash, &out); err != nil || ok {
			return err
		}
		if cur.Status != domain.ProcedureDraft {
			return RuleProblem(domain.ProcedureImmutable(cur))
		}
		if err := lockProcedureName(ctx, tx, tp.TenantID, cur.LegalEntityID, cur.Name); err != nil {
			return err
		}
		var superseded string
		err = tx.QueryRow(ctx, `update pricing_procedure set status = 'SUPERSEDED'
			where legal_entity_id = $1 and name = $2 and status = 'ACTIVE' returning id::text`, cur.LegalEntityID, cur.Name).Scan(&superseded)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		out, err = scanProcedure(tx.QueryRow(ctx, `update pricing_procedure set status = 'ACTIVE', activated_at = now()
			where id = $1 returning `+procedureColumns, id))
		if err != nil {
			return err
		}
		if err := enqueueEvent(ctx, tx, "procedure", out.ID, pevents.ProcedureActivated, pevents.ProcedureActivatedData{
			ProcedureID: out.ID, LegalEntityID: out.LegalEntityID, Name: out.Name, Version: out.Version, SupersededID: superseded}); err != nil {
			return err
		}
		return rememberJSON(ctx, tx, tp.TenantID, idempotencyKey, hash, out)
	})
	return out, err
}
