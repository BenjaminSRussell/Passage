package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/domain"
	analyticsevents "github.com/BenjaminSRussell/passage/services/analytics/internal/events"
)

const dqRuleColumns = `rule_id, asset_fqn, rule_type, expression, threshold_pct, severity, owner_user_id::text, status, mute_until, version`

const dqScoreColumns = `asset_fqn, rule_id, score_date::text, pass_pct, failed_rows, total_rows, below, below_streak`

func scanRule(row pgx.Row) (domain.DQRule, error) {
	var r domain.DQRule
	err := row.Scan(&r.RuleID, &r.AssetFQN, &r.Type, &r.Expression, &r.ThresholdPct, &r.Severity, &r.OwnerUserID,
		&r.Status, &r.MuteUntil, &r.Version)
	r.MuteUntil = utc(r.MuteUntil)
	return r, err
}

func scanScore(row pgx.Row) (domain.Score, error) {
	var s domain.Score
	err := row.Scan(&s.AssetFQN, &s.RuleID, &s.Date, &s.PassPct, &s.FailedRows, &s.TotalRows, &s.Below, &s.BelowStreak)
	return s, err
}

// RuleFilter narrows the scorecard rule list (§9.46.14: asset, rule type,
// owner). An empty field matches every rule.
type RuleFilter struct {
	Asset       string
	RuleType    string
	OwnerUserID string
}

// ListDQRules returns the tenant's rules matching f, ordered by asset and rule id.
func (s *Postgres) ListDQRules(ctx context.Context, f RuleFilter) ([]domain.DQRule, error) {
	items := []domain.DQRule{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+dqRuleColumns+` from dq_rule
			where `+ownTenant+` and ($1::text = '' or asset_fqn = $1) and ($2::text = '' or rule_type = $2)
				and ($3::text = '' or owner_user_id::text = $3)
			order by asset_fqn, rule_id`, f.Asset, f.RuleType, f.OwnerUserID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRule(rows)
			if err != nil {
				return err
			}
			items = append(items, r)
		}
		return rows.Err()
	})
	return items, err
}

// GetDQRule returns one rule or an ANA-0112 not-found error.
func (s *Postgres) GetDQRule(ctx context.Context, ruleID string) (domain.DQRule, error) {
	var out domain.DQRule
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		r, err := scanRule(tx.QueryRow(ctx, `select `+dqRuleColumns+` from dq_rule where `+ownTenant+` and rule_id = $1`, ruleID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NotFound("data-quality rule " + ruleID)
		}
		out = r
		return err
	})
	return out, err
}

// PutDQRule creates or replaces a rule (GET/PUT /catalog/dq-rules with ETag).
// ifMatch is the version from If-Match, 0 when the header is absent. A new
// rule needs no If-Match and starts at version 1; replacing an existing rule
// requires If-Match (COM-0428) naming the current version (ANA-0113). muted
// opens a 7-day mute window at now, keeps an open one, or clears it.
func (s *Postgres) PutDQRule(ctx context.Context, in domain.DQRule, ifMatch int, muted bool, now time.Time) (domain.DQRule, bool, error) {
	if err := in.Validate(); err != nil {
		return domain.DQRule{}, false, err
	}
	p, _ := tenant.FromContext(ctx)
	var out domain.DQRule
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		existing, err := scanRule(tx.QueryRow(ctx, `select `+dqRuleColumns+` from dq_rule where `+ownTenant+` and rule_id = $1 for update`, in.RuleID))
		exists := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		switch {
		case !exists && ifMatch != 0:
			return &domain.Error{Code: domain.CodeVersionConflict, Status: 412, Detail: "rule " + in.RuleID + " does not exist; send no If-Match to create it"}
		case exists && ifMatch == 0:
			return &domain.Error{Code: domain.CodePreconditionRequired, Status: 428, Detail: "If-Match is required to replace rule " + in.RuleID}
		case exists && ifMatch != existing.Version:
			return &domain.Error{Code: domain.CodeVersionConflict, Status: 412, Detail: "rule " + in.RuleID + " has changed; reload it and try again"}
		}
		var mute *time.Time
		switch {
		case muted && domain.IsMuted(existing.MuteUntil, now):
			mute = existing.MuteUntil
		case muted:
			until := domain.MuteUntil(now).UTC()
			mute = &until
		}
		if !exists {
			created = true
			out, err = scanRule(tx.QueryRow(ctx, `insert into dq_rule (tenant_id, rule_id, asset_fqn, rule_type, expression,
				threshold_pct, severity, owner_user_id, status, mute_until, version, updated_by)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 1, $11) returning `+dqRuleColumns,
				p.TenantID, in.RuleID, in.AssetFQN, in.Type, in.Expression, in.ThresholdPct.String(), in.Severity,
				in.OwnerUserID, in.Status, mute, p.UserID))
			return err
		}
		out, err = scanRule(tx.QueryRow(ctx, `update dq_rule set asset_fqn = $2, rule_type = $3, expression = $4,
			threshold_pct = $5, severity = $6, owner_user_id = $7, status = $8, mute_until = $9,
			version = version + 1, updated_by = $10, updated_at = now()
			where `+ownTenant+` and rule_id = $1 returning `+dqRuleColumns,
			in.RuleID, in.AssetFQN, in.Type, in.Expression, in.ThresholdPct.String(), in.Severity, in.OwnerUserID,
			in.Status, mute, p.UserID))
		return err
	})
	return out, created, err
}

// ScoreResult is one recorded evaluation and who it notifies.
type ScoreResult struct {
	Score   domain.Score
	Routing domain.Routing
	Muted   bool
	// Replayed is true when the same evaluation was recorded before; nothing
	// is written or emitted again, so Routing is empty.
	Replayed bool
}

// RecordDQScore stores one evaluation of an ACTIVE rule at evaluatedAt. The
// below-threshold streak continues from the rule's latest earlier evaluation
// (BR-46.15), a later evaluation the same day replaces that day's row, and
// dq.scored is written to the outbox in the same transaction with the
// owner-task and steward-escalation decision: the owner task is asked for once
// per rule and day, the steward escalation once per below-threshold run. Recording the same evaluatedAt
// again is a replay; an evaluation older than the latest one is refused.
func (s *Postgres) RecordDQScore(ctx context.Context, ruleID string, evaluatedAt time.Time, total, failed int64) (ScoreResult, error) {
	p, _ := tenant.FromContext(ctx)
	evaluatedAt = evaluatedAt.UTC().Truncate(time.Microsecond)
	date := evaluatedAt.Format(time.DateOnly)
	var out ScoreResult
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rule, err := scanRule(tx.QueryRow(ctx, `select `+dqRuleColumns+` from dq_rule where `+ownTenant+` and rule_id = $1 for update`, ruleID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NotFound("data-quality rule " + ruleID)
		}
		if err != nil {
			return err
		}
		if rule.Status != domain.RuleActive {
			return domain.StateConflict("rule " + ruleID + " is " + rule.Status + " and is not evaluated")
		}
		score, err := domain.Evaluate(rule, date, total, failed)
		if err != nil {
			return &domain.Error{Code: domain.CodeValidation, Status: 400, Detail: err.Error()}
		}
		var (
			prevStreak             int
			prevAt                 time.Time
			prevDate               string
			prevOwner, prevSteward bool
		)
		err = tx.QueryRow(ctx, `select below_streak, evaluated_at, score_date::text, owner_notified, steward_notified
			from dq_score where `+ownTenant+` and rule_id = $1 order by evaluated_at desc limit 1`, ruleID).
			Scan(&prevStreak, &prevAt, &prevDate, &prevOwner, &prevSteward)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		found := err == nil
		if found {
			switch {
			case prevAt.Equal(evaluatedAt):
				stored, err := scanScore(tx.QueryRow(ctx, `select `+dqScoreColumns+` from dq_score where `+ownTenant+` and rule_id = $1 and score_date = $2::date`, ruleID, date))
				if err != nil {
					return err
				}
				out = ScoreResult{Score: stored, Muted: domain.IsMuted(rule.MuteUntil, evaluatedAt), Replayed: true}
				return nil
			case prevAt.After(evaluatedAt):
				return domain.StateConflict("rule " + ruleID + " already has a later evaluation")
			}
		}
		score.BelowStreak = domain.NextBelowStreak(prevStreak, score.Below)
		muted := domain.IsMuted(rule.MuteUntil, evaluatedAt)
		sent := domain.Notified{
			OwnerToday:      found && prevDate == date && prevOwner,
			StewardInStreak: found && score.Below && prevStreak > 0 && prevSteward,
		}
		routing := domain.Route(score.Below, score.BelowStreak, muted, sent)
		ownerNotified := sent.OwnerToday || routing.OwnerTask
		stewardNotified := score.Below && (sent.StewardInStreak || routing.StewardEscalation)
		stored, err := scanScore(tx.QueryRow(ctx, `insert into dq_score (tenant_id, rule_id, score_date, asset_fqn, pass_pct,
			failed_rows, total_rows, below, below_streak, owner_notified, steward_notified, evaluated_at)
			values ($1, $2, $3::date, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			on conflict (tenant_id, rule_id, score_date) do update set asset_fqn = excluded.asset_fqn,
				pass_pct = excluded.pass_pct, failed_rows = excluded.failed_rows, total_rows = excluded.total_rows,
				below = excluded.below, below_streak = excluded.below_streak, owner_notified = excluded.owner_notified,
				steward_notified = excluded.steward_notified, evaluated_at = excluded.evaluated_at
			returning `+dqScoreColumns,
			p.TenantID, ruleID, date, rule.AssetFQN, score.PassPct.String(), failed, total, score.Below, score.BelowStreak,
			ownerNotified, stewardNotified, evaluatedAt))
		if err != nil {
			return err
		}
		out = ScoreResult{Score: stored, Routing: routing, Muted: muted}
		data := analyticsevents.DQScoredData{
			AssetFQN: stored.AssetFQN, RuleID: ruleID, Date: date, PassPct: stored.PassPct.String(),
			ThresholdPct: rule.ThresholdPct.String(), FailedRows: stored.FailedRows, TotalRows: stored.TotalRows,
			Severity: rule.Severity, OwnerUserID: rule.OwnerUserID, Below: stored.Below, BelowStreak: stored.BelowStreak,
			Muted: muted, NotifyOwner: out.Routing.OwnerTask, EscalateSteward: out.Routing.StewardEscalation,
		}
		if out.Routing.OwnerTask {
			data.TaskKey = domain.TaskKey(ruleID, date)
		}
		evt, err := events.New(analyticsevents.DQScored, eventSource, ruleID+"/"+date, p.TenantID, data)
		if err != nil {
			return err
		}
		return outbox.Enqueue(ctx, tx, "dq_rule", ruleID, evt)
	})
	return out, err
}

// ListDQScores returns score history newest first, filtered by asset and/or
// rule, at most limit rows.
func (s *Postgres) ListDQScores(ctx context.Context, asset, ruleID string, limit int) ([]domain.Score, error) {
	items := []domain.Score{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+dqScoreColumns+` from dq_score
			where `+ownTenant+` and ($1::text = '' or asset_fqn = $1) and ($2::text = '' or rule_id = $2)
			order by score_date desc, asset_fqn, rule_id limit $3`, asset, ruleID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			sc, err := scanScore(rows)
			if err != nil {
				return err
			}
			items = append(items, sc)
		}
		return rows.Err()
	})
	return items, err
}
