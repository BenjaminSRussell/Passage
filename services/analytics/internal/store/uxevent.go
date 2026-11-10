package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/domain"
)

// AddUXEvents stores a prepared batch in one transaction (POST /analytics/ux-events).
func (s *Postgres) AddUXEvents(ctx context.Context, events []domain.UXEvent) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		batch := &pgx.Batch{}
		for _, e := range events {
			batch.Queue(`insert into ux_event (tenant_id, session_id, release, event_type, flow, step, route, field, query, duration_ms, occurred_at)
				values ($1, $2, $3, $4, nullif($5, ''), nullif($6, ''), nullif($7, ''), nullif($8, ''), nullif($9, ''), $10, $11)`,
				p.TenantID, e.SessionID, e.Release, e.Type, e.Flow, e.Step, e.Route, e.Field, e.Query, e.DurationMS, e.OccurredAt)
		}
		res := tx.SendBatch(ctx, batch)
		for range events {
			if _, err := res.Exec(); err != nil {
				_ = res.Close()
				return err
			}
		}
		return res.Close()
	})
}

// periodBounds turns the inclusive day range into [from, to+1 day).
func periodBounds(from, to time.Time) (time.Time, time.Time) {
	return from.UTC(), to.UTC().Add(24 * time.Hour)
}

// GetFunnel counts, for each step of the flow, the sessions that sent it in the
// period, and the median seconds from a session's first event to its last step
// (GET /analytics/ux-funnels). release narrows to one release when not empty.
func (s *Postgres) GetFunnel(ctx context.Context, flow, release string, from, to time.Time) (domain.Funnel, error) {
	lo, hi := periodBounds(from, to)
	counts := map[string]int{}
	var seconds *float64
	steps := domain.Funnels[flow]
	last := steps[len(steps)-1]
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select step, count(distinct session_id) from ux_event
			where `+ownTenant+` and event_type = 'funnel_step' and flow = $1
			  and occurred_at >= $2 and occurred_at < $3 and ($4 = '' or release = $4)
			group by step`, flow, lo, hi, release)
		if err != nil {
			return err
		}
		for rows.Next() {
			var step string
			var n int
			if err := rows.Scan(&step, &n); err != nil {
				rows.Close()
				return err
			}
			counts[step] = n
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `select percentile_cont(0.5) within group (order by secs)::float8 from (
				select extract(epoch from max(occurred_at) filter (where step = $5) - min(occurred_at)) as secs
				from ux_event
				where `+ownTenant+` and event_type = 'funnel_step' and flow = $1
				  and occurred_at >= $2 and occurred_at < $3 and ($4 = '' or release = $4)
				group by session_id
				having bool_or(step = $5)) t`, flow, lo, hi, release, last).Scan(&seconds)
	})
	if err != nil {
		return domain.Funnel{}, err
	}
	return domain.BuildFunnel(flow, from.Format(time.DateOnly), to.Format(time.DateOnly), release, counts, seconds), nil
}

// GetUXSignals returns the most frequent validation errors, searches that found
// nothing and abandoned forms in the period (GET /analytics/ux-signals).
func (s *Postgres) GetUXSignals(ctx context.Context, release string, from, to time.Time) (domain.UXSignals, error) {
	lo, hi := periodBounds(from, to)
	out := domain.UXSignals{
		From: from.Format(time.DateOnly), To: to.Format(time.DateOnly), Release: release,
		ValidationErrors: []domain.ValidationSignal{}, NoResultSearches: []domain.SearchSignal{}, AbandonedForms: []domain.AbandonedSignal{},
	}
	const where = ` from ux_event where ` + ownTenant + ` and event_type = $1 and occurred_at >= $2 and occurred_at < $3 and ($4 = '' or release = $4)`
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select route, field, count(*)`+where+` group by route, field order by count(*) desc, route, field limit 20`, domain.UXValidationError, lo, hi, release)
		if err != nil {
			return err
		}
		for rows.Next() {
			var v domain.ValidationSignal
			if err := rows.Scan(&v.Route, &v.Field, &v.Count); err != nil {
				rows.Close()
				return err
			}
			out.ValidationErrors = append(out.ValidationErrors, v)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `select query, count(*)`+where+` group by query order by count(*) desc, query limit 20`, domain.UXSearchNoResults, lo, hi, release)
		if err != nil {
			return err
		}
		for rows.Next() {
			var v domain.SearchSignal
			if err := rows.Scan(&v.Query, &v.Count); err != nil {
				rows.Close()
				return err
			}
			out.NoResultSearches = append(out.NoResultSearches, v)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `select route, count(*)`+where+` group by route order by count(*) desc, route limit 20`, domain.UXFormAbandoned, lo, hi, release)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v domain.AbandonedSignal
			if err := rows.Scan(&v.Route, &v.Count); err != nil {
				return err
			}
			out.AbandonedForms = append(out.AbandonedForms, v)
		}
		return rows.Err()
	})
	return out, err
}
