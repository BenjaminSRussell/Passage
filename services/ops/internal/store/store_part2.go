package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/ops/internal/domain"
)

// Step is one recorded step of a failover.
type Step struct {
	Order      int       `json:"order"`
	Action     string    `json:"action"`
	Region     string    `json:"region"`
	Status     string    `json:"status"`
	Detail     string    `json:"detail,omitempty"`
	Forced     bool      `json:"forced,omitempty"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
	ReportedBy string    `json:"reportedBy"`
}

// FailoverDetail is a failover with the steps recorded so far and whether every step of the plan is DONE.
type FailoverDetail struct {
	Failover
	Steps    []Step `json:"steps"`
	Complete bool   `json:"complete"`
}

// BreakGlass is one window (see migration 0004).
type BreakGlass struct {
	ID            string     `json:"id"`
	UserID        string     `json:"userId"`
	ReasonCode    string     `json:"reasonCode"`
	TicketRef     string     `json:"ticketRef"`
	GrantedAt     time.Time  `json:"grantedAt"`
	ExpiresAt     time.Time  `json:"expiresAt"`
	RevokedAt     *time.Time `json:"revokedAt,omitempty"`
	ReviewDueAt   time.Time  `json:"reviewDueAt"`
	ReviewedBy    string     `json:"reviewedBy,omitempty"`
	ReviewedAt    *time.Time `json:"reviewedAt,omitempty"`
	ReviewOutcome string     `json:"reviewOutcome,omitempty"`
	ReviewNote    string     `json:"reviewNote,omitempty"`
	Uses          int        `json:"uses"`
}

// Gauges are the platform state the ops metrics report.
type Gauges struct {
	FencedRegions       int
	FailoversInProgress int
	BreakGlassActive    int
	BreakGlassOverdue   int
}

// RecordStep records one step result of an existing failover. A step already recorded DONE is kept; a FAILED step is
// replaced by a later result, which is how a retry shows its success.
func (s *Postgres) RecordStep(ctx context.Context, eventID string, st Step, reportedBy string) error {
	if st.Forced {
		// A fence recorded without reaching the old region is a decision, not a fact: it needs a failover a second operator approved.
		var approver *string
		err := s.pool.QueryRow(ctx, `select approved_by from ops_failover where event_id = $1`, eventID).Scan(&approver)
		if errors.Is(err, pgx.ErrNoRows) {
			return Problem(&domain.Error{Code: domain.CodeRequestUnknown, Detail: "failover " + eventID + " does not exist"})
		}
		if err != nil {
			return err
		}
		if approver == nil || *approver == "" {
			return Problem(&domain.Error{Code: domain.CodeApprovalRequired, Detail: "fencing a region that cannot be reached needs a failover a second operator approved (start it with --accept-data-loss)"})
		}
	}
	// The step and, when it is the last one DONE, the failover.completed event commit together.
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into ops_failover_step (event_id, action, ord, region, status, detail, forced, started_at, finished_at, reported_by)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		on conflict (event_id, action) do update set ord = excluded.ord, region = excluded.region, status = excluded.status,
		  detail = excluded.detail, forced = excluded.forced, started_at = excluded.started_at, finished_at = excluded.finished_at, reported_by = excluded.reported_by
		where ops_failover_step.status <> 'DONE'`,
			eventID, st.Action, st.Order, st.Region, st.Status, st.Detail, st.Forced, st.StartedAt, st.FinishedAt, reportedBy); err != nil {
			return err
		}
		if st.Status != "DONE" {
			return nil
		}
		return s.completeFailover(ctx, tx, eventID, reportedBy)
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return Problem(&domain.Error{Code: domain.CodeRequestUnknown, Detail: "failover " + eventID + " does not exist"})
		}
		return err
	}
	return nil
}

// FailoverByEvent reads a failover with its recorded steps.
func (s *Postgres) FailoverByEvent(ctx context.Context, eventID string) (FailoverDetail, error) {
	rec, found, err := failoverByEvent(ctx, s.pool, eventID)
	if err != nil {
		return FailoverDetail{}, err
	}
	if !found {
		return FailoverDetail{}, Problem(&domain.Error{Code: domain.CodeRequestUnknown, Detail: "failover " + eventID + " does not exist"})
	}
	rows, err := s.pool.Query(ctx, `select ord, action, region, status, detail, forced, started_at, finished_at, reported_by
		from ops_failover_step where event_id = $1 order by ord`, eventID)
	if err != nil {
		return FailoverDetail{}, err
	}
	defer rows.Close()
	out := FailoverDetail{Failover: rec, Steps: []Step{}}
	done := 0
	for rows.Next() {
		var st Step
		if err := rows.Scan(&st.Order, &st.Action, &st.Region, &st.Status, &st.Detail, &st.Forced, &st.StartedAt, &st.FinishedAt, &st.ReportedBy); err != nil {
			return FailoverDetail{}, err
		}
		if st.Status == "DONE" {
			done++
		}
		out.Steps = append(out.Steps, st)
	}
	out.Complete = done >= len(rec.Plan.Steps) && len(rec.Plan.Steps) > 0
	return out, rows.Err()
}

const breakGlassCols = `g.id::text, g.user_id, g.reason_code, g.ticket_ref, g.granted_at, g.expires_at, g.revoked_at, g.review_due_at,
	coalesce(g.reviewed_by, ''), g.reviewed_at, coalesce(g.review_outcome, ''), g.review_note,
	(select count(*) from ops_break_glass_use u where u.grant_id = g.id)`

func scanBreakGlass(row pgx.Row) (BreakGlass, error) {
	var g BreakGlass
	err := row.Scan(&g.ID, &g.UserID, &g.ReasonCode, &g.TicketRef, &g.GrantedAt, &g.ExpiresAt, &g.RevokedAt, &g.ReviewDueAt,
		&g.ReviewedBy, &g.ReviewedAt, &g.ReviewOutcome, &g.ReviewNote, &g.Uses)
	return g, err
}

// OpenBreakGlass records a window for the user. A user holds at most one active window: opening a second while the
// first runs is refused, so an extension is a new, separately reviewed window after the first ends.
func (s *Postgres) OpenBreakGlass(ctx context.Context, w domain.BreakGlassWindow) (BreakGlass, error) {
	var out BreakGlass
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext('ops_break_glass:' || $1))`, w.UserID); err != nil {
			return err
		}
		var active bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from ops_break_glass where user_id = $1 and revoked_at is null and expires_at > now())`, w.UserID).Scan(&active); err != nil {
			return err
		}
		if active {
			return &domain.Error{Code: domain.CodeBreakGlassActive, Detail: "a break-glass window is already active for this operator"}
		}
		id := ids.New()
		// The window runs from the database clock, so a skewed caller cannot start it in the past or future.
		if _, err := tx.Exec(ctx, `insert into ops_break_glass (id, user_id, reason_code, ticket_ref, granted_at, expires_at, review_due_at)
			values ($1, $2, $3, $4, now(), now() + make_interval(mins => $5), now() + make_interval(mins => $5) + interval '3 days')`,
			id, w.UserID, w.ReasonCode, w.TicketRef, w.Minutes); err != nil {
			return err
		}
		g, err := scanBreakGlass(tx.QueryRow(ctx, `select `+breakGlassCols+` from ops_break_glass g where g.id = $1`, id))
		out = g
		return err
	})
	return out, Problem(err)
}

// ListBreakGlass lists windows newest first; unreviewedOnly keeps those awaiting a review.
func (s *Postgres) ListBreakGlass(ctx context.Context, unreviewedOnly bool) ([]BreakGlass, error) {
	q := `select ` + breakGlassCols + ` from ops_break_glass g`
	if unreviewedOnly {
		q += ` where g.reviewed_by is null`
	}
	rows, err := s.pool.Query(ctx, q+` order by g.granted_at desc limit 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BreakGlass{}
	for rows.Next() {
		g, err := scanBreakGlass(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// RevokeBreakGlass ends a window early. Only the operator who opened it may.
func (s *Postgres) RevokeBreakGlass(ctx context.Context, id, user string) (BreakGlass, error) {
	var out BreakGlass
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		g, err := scanBreakGlass(tx.QueryRow(ctx, `select `+breakGlassCols+` from ops_break_glass g where g.id::text = $1 for update of g`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return &domain.Error{Code: domain.CodeBreakGlassUnknown, Detail: "break-glass window not found"}
		}
		if err != nil {
			return err
		}
		if g.UserID != user {
			return httpx.NewProblem(http.StatusForbidden, domain.CodeBreakGlassRequired, "only the operator who opened a break-glass window can revoke it")
		}
		if _, err := tx.Exec(ctx, `update ops_break_glass set revoked_at = coalesce(revoked_at, now()) where id = $1`, g.ID); err != nil {
			return err
		}
		out, err = scanBreakGlass(tx.QueryRow(ctx, `select `+breakGlassCols+` from ops_break_glass g where g.id = $1`, g.ID))
		return err
	})
	return out, Problem(err)
}

// ReviewBreakGlass records the review. The reviewer is the verified caller and not the window's operator; a window is
// reviewed once and only after it has ended or been revoked.
func (s *Postgres) ReviewBreakGlass(ctx context.Context, id, reviewer, outcome, note string) (BreakGlass, error) {
	var out BreakGlass
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		g, err := scanBreakGlass(tx.QueryRow(ctx, `select `+breakGlassCols+` from ops_break_glass g where g.id::text = $1 for update of g`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return &domain.Error{Code: domain.CodeBreakGlassUnknown, Detail: "break-glass window not found"}
		}
		if err != nil {
			return err
		}
		if err := domain.ValidateReview(g.UserID, reviewer, outcome, note); err != nil {
			return err
		}
		if g.ReviewedBy != "" {
			return &domain.Error{Code: domain.CodeBreakGlassReview, Detail: "this window was already reviewed"}
		}
		var open bool
		if err := tx.QueryRow(ctx, `select revoked_at is null and expires_at > now() from ops_break_glass where id = $1`, g.ID).Scan(&open); err != nil {
			return err
		}
		if open {
			return &domain.Error{Code: domain.CodeBreakGlassReview, Detail: "the window is still open; review it after it ends or is revoked"}
		}
		if _, err := tx.Exec(ctx, `update ops_break_glass set reviewed_by = $2, reviewed_at = now(), review_outcome = $3, review_note = $4 where id = $1`,
			g.ID, reviewer, outcome, note); err != nil {
			return err
		}
		out, err = scanBreakGlass(tx.QueryRow(ctx, `select `+breakGlassCols+` from ops_break_glass g where g.id = $1`, g.ID))
		return err
	})
	return out, Problem(err)
}

// AuthoriseBreakGlass reports whether user holds an active window and, if so, records the use. The check and the log
// entry are one statement, so a use is never authorised without a record.
func (s *Postgres) AuthoriseBreakGlass(ctx context.Context, user, method, path string) (bool, error) {
	if user == "" {
		return false, nil
	}
	var id string
	err := s.pool.QueryRow(ctx, `with w as (
		  select id from ops_break_glass where user_id = $1 and revoked_at is null and expires_at > now() order by expires_at desc limit 1
		) insert into ops_break_glass_use (grant_id, method, path) select id, $2, $3 from w returning grant_id::text`, user, method, path).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// Gauges reads the counts behind the ops metrics.
func (s *Postgres) Gauges(ctx context.Context) (Gauges, error) {
	var g Gauges
	err := s.pool.QueryRow(ctx, `select
		(select count(*) from ops_region where role = 'FENCED'),
		(select count(*) from ops_failover f where
		   (select count(*) from ops_failover_step s where s.event_id = f.event_id and s.status = 'DONE') < jsonb_array_length(f.plan->'steps')
		   and (exists (select 1 from ops_failover_step s where s.event_id = f.event_id) or f.created_at > now() - interval '1 hour')),
		(select count(*) from ops_break_glass where revoked_at is null and expires_at > now()),
		(select count(*) from ops_break_glass where reviewed_by is null and review_due_at < now())`).Scan(
		&g.FencedRegions, &g.FailoversInProgress, &g.BreakGlassActive, &g.BreakGlassOverdue)
	return g, err
}
