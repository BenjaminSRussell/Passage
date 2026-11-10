// Package store persists the region topology and the failover log. The tables are platform state without a tenant.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/ops/internal/domain"
)

// Failover is one recorded failover: the validated plan and who asked for it and approved it.
type Failover struct {
	EventID     string      `json:"eventId"`
	Plan        domain.Plan `json:"plan"`
	RequestedBy string      `json:"requestedBy"`
	ApprovedBy  string      `json:"approvedBy,omitempty"`
	CreatedAt   time.Time   `json:"createdAt"`
}

// Store is the ops persistence port.
type Store interface {
	Regions(ctx context.Context) ([]domain.Region, error)
	// Seed registers the topology when no region exists yet; it does nothing otherwise.
	Seed(ctx context.Context, regions []domain.Region) error
	ReportLag(ctx context.Context, region string, lagSeconds int) (domain.Region, error)
	// Failover validates and applies a failover in one transaction. replay is true when eventID was already recorded:
	// the first outcome is returned and nothing changes.
	Failover(ctx context.Context, eventID string, req domain.FailoverRequest) (rec Failover, replay bool, err error)
	Rejoin(ctx context.Context, region string, resyncVerified bool) (domain.Region, error)
	Ping(ctx context.Context) error
}

// Postgres implements Store.
type Postgres struct{ pool *pgxpool.Pool }

// New wraps pool.
func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Ping checks the pool.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Problem maps a rule violation to an RFC 9457 problem; other errors pass through.
func Problem(err error) error {
	var rule *domain.Error
	if !errors.As(err, &rule) {
		return err
	}
	status := http.StatusConflict
	switch rule.Code {
	case domain.CodeRegionUnknown:
		status = http.StatusNotFound
	case domain.CodeLagExceeded, domain.CodeApprovalRequired:
		status = http.StatusUnprocessableEntity
	}
	return httpx.NewProblem(status, rule.Code, rule.Detail)
}

type queryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func listRegions(ctx context.Context, q queryer, lock bool) ([]domain.Region, error) {
	sql := `select name, role, lag_seconds, version from ops_region order by name`
	if lock {
		sql += ` for update`
	}
	rows, err := q.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Region
	for rows.Next() {
		var r domain.Region
		var role string
		if err := rows.Scan(&r.Name, &role, &r.LagSeconds, &r.Version); err != nil {
			return nil, err
		}
		r.Role = domain.Role(role)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Regions lists every region by name.
func (s *Postgres) Regions(ctx context.Context) ([]domain.Region, error) {
	return listRegions(ctx, s.pool, false)
}

// Seed inserts regions only into an empty table.
func (s *Postgres) Seed(ctx context.Context, regions []domain.Region) error {
	if err := domain.ValidateTopology(regions); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `lock table ops_region in share row exclusive mode`); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `select count(*) from ops_region`).Scan(&n); err != nil || n > 0 {
			return err
		}
		for _, r := range regions {
			if _, err := tx.Exec(ctx, `insert into ops_region (name, role, lag_seconds) values ($1, $2, $3)`, r.Name, string(r.Role), r.LagSeconds); err != nil {
				return err
			}
		}
		return nil
	})
}

// ReportLag records a passive region's replication lag.
func (s *Postgres) ReportLag(ctx context.Context, region string, lagSeconds int) (domain.Region, error) {
	var r domain.Region
	var role string
	err := s.pool.QueryRow(ctx, `update ops_region set lag_seconds = $2, version = version + 1, updated_at = now() where name = $1
		returning name, role, lag_seconds, version`, region, lagSeconds).Scan(&r.Name, &role, &r.LagSeconds, &r.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, Problem(&domain.Error{Code: domain.CodeRegionUnknown, Detail: "region " + region + " is not registered"})
	}
	r.Role = domain.Role(role)
	return r, err
}

// Failover plans and applies a failover in one transaction. Regions are locked in name order, so two requests
// serialize and the second sees the first's result; the one PRIMARY index backs BR-50.1 if a bug ever skips the check.
func (s *Postgres) Failover(ctx context.Context, eventID string, req domain.FailoverRequest) (Failover, bool, error) {
	var rec Failover
	var replay bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		regions, err := listRegions(ctx, tx, true)
		if err != nil {
			return err
		}
		var raw []byte
		var approved *string
		err = tx.QueryRow(ctx, `select event_id, requested_by, approved_by, created_at, plan from ops_failover where event_id = $1`, eventID).
			Scan(&rec.EventID, &rec.RequestedBy, &approved, &rec.CreatedAt, &raw)
		if err == nil {
			replay = true
			if approved != nil {
				rec.ApprovedBy = *approved
			}
			return json.Unmarshal(raw, &rec.Plan)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		plan, err := domain.PlanFailover(regions, req)
		if err != nil {
			return err
		}
		// Demote first: the unique index allows one PRIMARY at any instant.
		if _, err := tx.Exec(ctx, `update ops_region set role = 'FENCED', version = version + 1, updated_at = now() where name = $1`, plan.From); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update ops_region set role = 'PRIMARY', version = version + 1, updated_at = now() where name = $1`, plan.To); err != nil {
			return err
		}
		raw, err = json.Marshal(plan)
		if err != nil {
			return err
		}
		var approver any
		if plan.AcceptDataLoss {
			approver = req.Approver
		}
		rec = Failover{EventID: eventID, Plan: plan, RequestedBy: req.Caller}
		if plan.AcceptDataLoss {
			rec.ApprovedBy = req.Approver
		}
		return tx.QueryRow(ctx, `insert into ops_failover (event_id, from_region, to_region, accept_data_loss, lag_seconds, requested_by, approved_by, plan)
			values ($1, $2, $3, $4, $5, $6, $7, $8) returning created_at`,
			eventID, plan.From, plan.To, plan.AcceptDataLoss, plan.LagSeconds, req.Caller, approver, raw).Scan(&rec.CreatedAt)
	})
	return rec, replay, Problem(err)
}

// Rejoin returns a FENCED region to PASSIVE after a verified re-sync.
func (s *Postgres) Rejoin(ctx context.Context, region string, resyncVerified bool) (domain.Region, error) {
	var out domain.Region
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		regions, err := listRegions(ctx, tx, true)
		if err != nil {
			return err
		}
		var cur *domain.Region
		for i := range regions {
			if regions[i].Name == region {
				cur = &regions[i]
			}
		}
		if cur == nil {
			return &domain.Error{Code: domain.CodeRegionUnknown, Detail: "region " + region + " is not registered"}
		}
		next, err := domain.Rejoin(*cur, resyncVerified)
		if err != nil {
			return err
		}
		next.Version++
		out = next
		_, err = tx.Exec(ctx, `update ops_region set role = $2, version = $3, updated_at = now() where name = $1`, next.Name, string(next.Role), next.Version)
		return err
	})
	return out, Problem(err)
}
