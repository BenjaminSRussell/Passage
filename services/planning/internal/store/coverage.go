package store

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
)

const (
	// CodeCoverageExists is returned when a coverage group code is already in use.
	CodeCoverageExists = "PLAN-COVERAGE-EXISTS"
	// CodeCoverageNotFound is returned for an unknown coverage group code.
	CodeCoverageNotFound = "PLAN-COVERAGE-NOT-FOUND"

	coverageCodeConstraint        = "coverage_group_code_key"
	coverageIdempotencyConstraint = "coverage_group_idempotency_key"
)

// CoverageGroup is a stored coverage group (FR-22.2). Version is its ETag.
type CoverageGroup struct {
	ID      string
	Version int
	Spec    domain.CoverageGroupSpec
}

// CoverageCreate is one create command; IdempotencyKey and InputHash are
// empty when the caller sent no Idempotency-Key.
type CoverageCreate struct {
	IdempotencyKey string
	InputHash      string
	Spec           domain.CoverageGroupSpec
}

const coverageSelect = `select id::text, version, code, coverage_code, coverage_time_fence_days, negative_days, positive_days, period_days,
	freeze_fence_days, action_fence_days, futures_fence_days, firming_fence_days, auto_firm, generate_actions, generate_futures, safety_stock_policy
	from coverage_group`

func scanCoverageGroup(row pgx.Row) (CoverageGroup, error) {
	var g CoverageGroup
	s := &g.Spec
	err := row.Scan(&g.ID, &g.Version, &s.Code, &s.CoverageCode, &s.CoverageTimeFenceDays, &s.NegativeDays, &s.PositiveDays, &s.PeriodDays,
		&s.FreezeFenceDays, &s.ActionFenceDays, &s.FuturesFenceDays, &s.FirmingFenceDays, &s.AutoFirm, &s.GenerateActions, &s.GenerateFutures, &s.SafetyStockPolicy)
	return g, err
}

func coverageNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeCoverageNotFound, "coverage group not found")
}

// ListCoverageGroups returns the tenant's coverage groups ordered by code (FR-22.2).
func (s *Postgres) ListCoverageGroups(ctx context.Context, limit int) ([]CoverageGroup, error) {
	p, _ := tenant.FromContext(ctx)
	out := []CoverageGroup{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, coverageSelect+` where tenant_id = $1 order by code limit $2`, p.TenantID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			g, err := scanCoverageGroup(rows)
			if err != nil {
				return err
			}
			out = append(out, g)
		}
		return rows.Err()
	})
	return out, err
}

// GetCoverageGroup returns one coverage group by code.
func (s *Postgres) GetCoverageGroup(ctx context.Context, code string) (CoverageGroup, error) {
	p, _ := tenant.FromContext(ctx)
	var g CoverageGroup
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		g, err = scanCoverageGroup(tx.QueryRow(ctx, coverageSelect+` where tenant_id = $1 and code = $2`, p.TenantID, code))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return CoverageGroup{}, coverageNotFound()
	}
	return g, err
}

// CreateCoverageGroup stores a validated spec at version 1. A code already in
// use is 409 PLAN-COVERAGE-EXISTS. With an Idempotency-Key, a retry of the
// same body returns the group created by the first call (replayed true) and
// another body under the same key is 422.
func (s *Postgres) CreateCoverageGroup(ctx context.Context, req CoverageCreate) (CoverageGroup, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out CoverageGroup
	replayed := false
	var key, hash *string
	if req.IdempotencyKey != "" {
		key, hash = &req.IdempotencyKey, &req.InputHash
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			var stored string
			err := tx.QueryRow(ctx, `select input_hash from coverage_group where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key).Scan(&stored)
			if err == nil {
				if stored != *hash {
					return idempotencyReuse()
				}
				g, err := scanCoverageGroup(tx.QueryRow(ctx, coverageSelect+` where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key))
				out, replayed = g, true
				return err
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		g := req.Spec
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into coverage_group (tenant_id, id, code, coverage_code, coverage_time_fence_days, negative_days,
			positive_days, period_days, freeze_fence_days, action_fence_days, futures_fence_days, firming_fence_days, auto_firm,
			generate_actions, generate_futures, safety_stock_policy, idempotency_key, input_hash)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)`,
			p.TenantID, id, g.Code, g.CoverageCode, g.CoverageTimeFenceDays, g.NegativeDays, g.PositiveDays,
			g.PeriodDays, g.FreezeFenceDays, g.ActionFenceDays, g.FuturesFenceDays, g.FirmingFenceDays,
			g.AutoFirm, g.GenerateActions, g.GenerateFutures, g.SafetyStockPolicy, key, hash); err != nil {
			return err
		}
		out = CoverageGroup{ID: id, Version: 1, Spec: g}
		return nil
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		switch pgErr.ConstraintName {
		case coverageCodeConstraint:
			return CoverageGroup{}, false, httpx.NewProblem(http.StatusConflict, CodeCoverageExists, "a coverage group with code "+req.Spec.Code+" already exists")
		case coverageIdempotencyConstraint:
			// A concurrent create with the same key committed first.
			return CoverageGroup{}, false, idempotencyReuse()
		}
	}
	return out, replayed, err
}

// UpdateCoverageGroup replaces the settings of the coverage group spec.Code
// under If-Match: expectedVersion must be its current version (412 otherwise),
// and the version is bumped.
func (s *Postgres) UpdateCoverageGroup(ctx context.Context, expectedVersion int, spec domain.CoverageGroupSpec) (CoverageGroup, error) {
	p, _ := tenant.FromContext(ctx)
	var out CoverageGroup
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var id string
		var version int
		err := tx.QueryRow(ctx, `select id::text, version from coverage_group where tenant_id = $1 and code = $2 for update`, p.TenantID, spec.Code).
			Scan(&id, &version)
		if errors.Is(err, pgx.ErrNoRows) {
			return coverageNotFound()
		}
		if err != nil {
			return err
		}
		if version != expectedVersion {
			return versionConflict("coverage group", version)
		}
		if _, err := tx.Exec(ctx, `update coverage_group set coverage_code = $3, coverage_time_fence_days = $4, negative_days = $5,
			positive_days = $6, period_days = $7, freeze_fence_days = $8, action_fence_days = $9, futures_fence_days = $10,
			firming_fence_days = $11, auto_firm = $12, generate_actions = $13, generate_futures = $14, safety_stock_policy = $15, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`,
			p.TenantID, id, spec.CoverageCode, spec.CoverageTimeFenceDays, spec.NegativeDays, spec.PositiveDays, spec.PeriodDays,
			spec.FreezeFenceDays, spec.ActionFenceDays, spec.FuturesFenceDays, spec.FirmingFenceDays, spec.AutoFirm,
			spec.GenerateActions, spec.GenerateFutures, spec.SafetyStockPolicy); err != nil {
			return err
		}
		out = CoverageGroup{ID: id, Version: version + 1, Spec: spec}
		return nil
	})
	return out, err
}
