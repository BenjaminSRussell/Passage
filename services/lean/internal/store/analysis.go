package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/lean/internal/domain"
)

// RuleActivity is what the kanban rule analysis reads (PRD 4.24.14): the rule,
// the cycles its cards completed in the period, and how many of its active
// cards are past their due time now.
type RuleActivity struct {
	Rule        Rule
	Cycles      []domain.Cycle
	OverdueOpen int
}

// PeriodWork is what the backflush of a costing period reads (FR-24.12): the
// period and, for every activity of its flow in sequence order, the jobs
// completed in the period and the quantity they processed.
type PeriodWork struct {
	Period     Period
	Activities []domain.ActivityWork
}

// recordCycle appends a completed cycle of card k (BR-24.6 Completed). It runs
// in the transaction that completes the card, so a cycle is recorded once per
// completion even when the card is later emptied and reused.
func recordCycle(ctx context.Context, tx pgx.Tx, kanbanID string) error {
	_, err := tx.Exec(ctx, `insert into kanban_cycle (tenant_id, id, kanban_id, rule_id, qty, planned_at, due, completed_at)
		select tenant_id, $2, id, rule_id, qty, planned_at, due, now() from kanban where id = $1`, kanbanID, ids.New())
	return err
}

// recordJobCompletion appends a completed job with what it reported, so the
// backflush keeps it after the card is reused and its jobs are cleared.
func recordJobCompletion(ctx context.Context, tx pgx.Tx, jobID string) error {
	_, err := tx.Exec(ctx, `insert into kanban_job_completion (tenant_id, id, job_id, kanban_id, activity_id, started_at, completed_at, reported_qty, scrap_qty)
		select tenant_id, $2, id, kanban_id, activity_id, started_at, completed_at, reported_qty, scrap_qty
		from kanban_job where id = $1 and state = 'completed'`, jobID, ids.New())
	return err
}

// RuleActivity returns a rule with the cycles its cards completed from the
// first instant of from to the last instant of to (UTC calendar days), and the
// number of its active cards that are past due now.
func (s *Postgres) RuleActivity(ctx context.Context, ruleID string, from, to time.Time) (RuleActivity, error) {
	var out RuleActivity
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if out.Rule, err = getRule(ctx, tx, ruleID, ""); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select planned_at, due, completed_at from kanban_cycle
			where rule_id = $1 and completed_at >= ($2::date)::timestamp at time zone 'UTC'
			  and completed_at < ($3::date + 1)::timestamp at time zone 'UTC'
			order by completed_at`, ruleID, from.Format(DateLayout), to.Format(DateLayout))
		if err != nil {
			return err
		}
		defer rows.Close()
		out.Cycles = []domain.Cycle{}
		for rows.Next() {
			var c domain.Cycle
			if err := rows.Scan(&c.PlannedAt, &c.Due, &c.CompletedAt); err != nil {
				return err
			}
			out.Cycles = append(out.Cycles, c)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `select count(*) from kanban
			where rule_id = $1 and state not in ('completed', 'emptied') and due < now()`, ruleID).Scan(&out.OverdueOpen)
	})
	return out, err
}

// PeriodWork returns a costing period of a flow with, per activity of the
// flow, the jobs completed within the period's days (UTC) and the quantity
// they processed (reported plus scrap). Activities without completions are
// listed with zero jobs.
func (s *Postgres) PeriodWork(ctx context.Context, flowID, periodID string) (PeriodWork, error) {
	var out PeriodWork
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if out.Period, err = getPeriod(ctx, tx, flowID, periodID, ""); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select a.id, a.sequence, a.name, a.kind, a.cycle_time_s,
				count(c.id), coalesce(sum(c.reported_qty + c.scrap_qty), 0)
			from flow_activity a
			left join kanban_job_completion c on c.activity_id = a.id
				and c.completed_at >= ($2::date)::timestamp at time zone 'UTC'
				and c.completed_at < ($3::date + 1)::timestamp at time zone 'UTC'
			where a.flow_id = $1
			group by a.id, a.sequence, a.name, a.kind, a.cycle_time_s
			order by a.sequence`, flowID, out.Period.PeriodStart, out.Period.PeriodEnd)
		if err != nil {
			return err
		}
		defer rows.Close()
		out.Activities = []domain.ActivityWork{}
		for rows.Next() {
			var w domain.ActivityWork
			var qty decimal.Decimal
			if err := rows.Scan(&w.ActivityID, &w.Sequence, &w.Name, &w.Kind, &w.CycleTimeS, &w.Jobs, &qty); err != nil {
				return err
			}
			w.Qty = qty
			out.Activities = append(out.Activities, w)
		}
		return rows.Err()
	})
	return out, err
}
