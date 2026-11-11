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
	"github.com/BenjaminSRussell/passage/platform/ids"
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

// RequestTTL is how long a failover request waits for its second approval: the two-person window of the failover runbook
// (failover.two_person_window_min, 5 minutes).
const RequestTTL = 5 * time.Minute

// Request is a failover that accepts data loss and waits for a second operator (OPS-1702). The approver is the verified
// caller of the approve route; the request body has no approver field.
type Request struct {
	ID          string     `json:"id"`
	EventID     string     `json:"eventId"`
	To          string     `json:"to"`
	LagSeconds  int        `json:"lagSeconds"`
	RequestedBy string     `json:"requestedBy"`
	Status      string     `json:"status"`
	ApprovedBy  string     `json:"approvedBy,omitempty"`
	ApprovedAt  *time.Time `json:"approvedAt,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	ExpiresAt   time.Time  `json:"expiresAt"`
}

// Outcome is the result of a failover call: the applied failover, or the request that now waits for approval.
// Replay is true when the event id was already recorded and nothing changed.
type Outcome struct {
	Failover *Failover
	Request  *Request
	Replay   bool
}

// Store is the ops persistence port.
type Store interface {
	Regions(ctx context.Context) ([]domain.Region, error)
	// Seed registers the topology when no region exists yet; it does nothing otherwise.
	Seed(ctx context.Context, regions []domain.Region) error
	ReportLag(ctx context.Context, region string, lagSeconds int) (domain.Region, error)
	// Failover validates and applies a failover in one transaction. A failover that needs data loss accepted is not
	// applied: it is recorded as a Request for a second operator (Approve). An eventID already recorded returns the first
	// outcome and changes nothing. req.Approver is ignored.
	Failover(ctx context.Context, eventID string, req domain.FailoverRequest) (Outcome, error)
	// Approve applies a pending request as approver, the verified caller, who must not be the requester. The plan is
	// made again under the region locks, so it reflects the topology and lag at approval time.
	Approve(ctx context.Context, requestID, approver string, maxLagSeconds int) (rec Failover, replay bool, err error)
	Request(ctx context.Context, requestID string) (Request, error)
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
	case domain.CodeRegionUnknown, domain.CodeRequestUnknown:
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
// A failover whose target lags past the limit, sent with acceptDataLoss, is recorded as a pending Request instead.
func (s *Postgres) Failover(ctx context.Context, eventID string, req domain.FailoverRequest) (Outcome, error) {
	var out Outcome
	req.Approver = ""
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		regions, err := listRegions(ctx, tx, true)
		if err != nil {
			return err
		}
		if rec, found, err := failoverByEvent(ctx, tx, eventID); err != nil || found {
			out = Outcome{Failover: &rec, Replay: found}
			return err
		}
		if r, found, err := requestBy(ctx, tx, `event_id = $1`, eventID, false); err != nil || found {
			out = Outcome{Request: &r, Replay: found}
			return err
		}
		plan, err := domain.PlanFailover(regions, req)
		if domain.IsApprovalRequired(err) {
			r, err := insertRequest(ctx, tx, eventID, req, regions)
			out = Outcome{Request: &r}
			return err
		}
		if err != nil {
			return err
		}
		rec, err := apply(ctx, tx, eventID, plan, req.Caller, "")
		out = Outcome{Failover: &rec}
		return err
	})
	return out, Problem(err)
}

// Approve applies a pending failover request with approver as the second operator.
func (s *Postgres) Approve(ctx context.Context, requestID, approver string, maxLagSeconds int) (Failover, bool, error) {
	var rec Failover
	var replay bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		regions, err := listRegions(ctx, tx, true)
		if err != nil {
			return err
		}
		r, found, err := requestBy(ctx, tx, `id::text = $1`, requestID, true)
		if err != nil {
			return err
		}
		if !found {
			return &domain.Error{Code: domain.CodeRequestUnknown, Detail: "failover request " + requestID + " does not exist"}
		}
		if approver == r.RequestedBy {
			return httpx.NewProblem(http.StatusForbidden, domain.CodeApprovalRequired, "the requester cannot approve their own failover request; a second operator must")
		}
		if r.Status == "APPROVED" {
			rec, _, err = failoverByEvent(ctx, tx, r.EventID)
			replay = true
			return err
		}
		var expired bool
		if err := tx.QueryRow(ctx, `select $1::timestamptz <= now()`, r.ExpiresAt).Scan(&expired); err != nil {
			return err
		}
		if expired {
			return &domain.Error{Code: domain.CodeRequestExpired, Detail: "failover request " + requestID + " expired; start a new failover"}
		}
		plan, err := domain.PlanFailover(regions, domain.FailoverRequest{To: r.To, AcceptDataLoss: true, Caller: r.RequestedBy, Approver: approver, MaxLagSeconds: maxLagSeconds})
		if err != nil {
			return err
		}
		if rec, err = apply(ctx, tx, r.EventID, plan, r.RequestedBy, approver); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `update ops_failover_request set status = 'APPROVED', approved_by = $2, approved_at = now() where id = $1`, r.ID, approver)
		return err
	})
	return rec, replay, Problem(err)
}

// Request reads one failover request.
func (s *Postgres) Request(ctx context.Context, requestID string) (Request, error) {
	r, found, err := requestBy(ctx, s.pool, `id::text = $1`, requestID, false)
	if err == nil && !found {
		err = &domain.Error{Code: domain.CodeRequestUnknown, Detail: "failover request " + requestID + " does not exist"}
	}
	return r, Problem(err)
}

type rowQueryer interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func failoverByEvent(ctx context.Context, q rowQueryer, eventID string) (Failover, bool, error) {
	var rec Failover
	var raw []byte
	var approved *string
	err := q.QueryRow(ctx, `select event_id, requested_by, approved_by, created_at, plan from ops_failover where event_id = $1`, eventID).
		Scan(&rec.EventID, &rec.RequestedBy, &approved, &rec.CreatedAt, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return Failover{}, false, nil
	}
	if err != nil {
		return Failover{}, false, err
	}
	if approved != nil {
		rec.ApprovedBy = *approved
	}
	return rec, true, json.Unmarshal(raw, &rec.Plan)
}

func requestBy(ctx context.Context, q rowQueryer, where, arg string, lock bool) (Request, bool, error) {
	sql := `select id::text, event_id, to_region, lag_seconds, requested_by, status, coalesce(approved_by, ''), approved_at, created_at, expires_at
		from ops_failover_request where ` + where
	if lock {
		sql += ` for update`
	}
	var r Request
	err := q.QueryRow(ctx, sql, arg).Scan(&r.ID, &r.EventID, &r.To, &r.LagSeconds, &r.RequestedBy, &r.Status, &r.ApprovedBy, &r.ApprovedAt, &r.CreatedAt, &r.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Request{}, false, nil
	}
	return r, err == nil, err
}

func insertRequest(ctx context.Context, tx pgx.Tx, eventID string, req domain.FailoverRequest, regions []domain.Region) (Request, error) {
	r := Request{ID: ids.New(), EventID: eventID, To: req.To, RequestedBy: req.Caller, Status: "PENDING"}
	for _, reg := range regions {
		if reg.Name == req.To {
			r.LagSeconds = reg.LagSeconds
		}
	}
	err := tx.QueryRow(ctx, `insert into ops_failover_request (id, event_id, to_region, lag_seconds, requested_by, expires_at)
		values ($1, $2, $3, $4, $5, now() + make_interval(secs => $6)) returning created_at, expires_at`,
		r.ID, eventID, r.To, r.LagSeconds, r.RequestedBy, RequestTTL.Seconds()).Scan(&r.CreatedAt, &r.ExpiresAt)
	return r, err
}

// apply fences the old primary, promotes the target and records the failover under eventID.
func apply(ctx context.Context, tx pgx.Tx, eventID string, plan domain.Plan, requestedBy, approvedBy string) (Failover, error) {
	// Demote first: the unique index allows one PRIMARY at any instant.
	if _, err := tx.Exec(ctx, `update ops_region set role = 'FENCED', version = version + 1, updated_at = now() where name = $1`, plan.From); err != nil {
		return Failover{}, err
	}
	if _, err := tx.Exec(ctx, `update ops_region set role = 'PRIMARY', version = version + 1, updated_at = now() where name = $1`, plan.To); err != nil {
		return Failover{}, err
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return Failover{}, err
	}
	rec := Failover{EventID: eventID, Plan: plan, RequestedBy: requestedBy, ApprovedBy: approvedBy}
	var approver any
	if approvedBy != "" {
		approver = approvedBy
	}
	err = tx.QueryRow(ctx, `insert into ops_failover (event_id, from_region, to_region, accept_data_loss, lag_seconds, requested_by, approved_by, plan)
		values ($1, $2, $3, $4, $5, $6, $7, $8) returning created_at`,
		eventID, plan.From, plan.To, plan.AcceptDataLoss, plan.LagSeconds, requestedBy, approver, raw).Scan(&rec.CreatedAt)
	return rec, err
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
