package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/eam/internal/domain"
	eamevents "github.com/BenjaminSRussell/passage/services/eam/internal/events"
)

// Reading is one counter measurement (FR-44.3).
type Reading struct {
	ID        string    `json:"id"`
	CounterID string    `json:"counterId"`
	ReadingAt time.Time `json:"readingAt"`
	Value     float64   `json:"value"`
	Reset     bool      `json:"reset"`
	Source    string    `json:"source"`
	MessageID *string   `json:"messageId,omitempty"`
}

// ReadingInput is a reading as received from a user, the IoT bridge or a replay.
type ReadingInput struct {
	Value     float64
	ReadingAt time.Time
	Source    string
	Reset     bool
	MessageID string
}

// RecordReading stores a reading, updates the counter and its rolling usage,
// evaluates the counter's condition rules (BR-44.4) and reschedules the plan
// lines that depend on the counter (BR-44.1, event-driven). A repeated
// idempotency key or message id returns the stored reading with replayed = true.
//
// Readings may arrive out of order (IoT). A cumulative reading must not be
// below the reading before it in time, nor above the reading after it, unless
// it carries the reset flag (EAM-CTR-001). Only the newest reading moves the
// counter's last value.
func (s *Postgres) RecordReading(ctx context.Context, counterID string, in ReadingInput, key string) (Reading, bool, error) {
	var out Reading
	replayed := false
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		// Lock the counter first so concurrent readings of it (and their rule
		// evaluations) serialise.
		var assetID string
		var cumulative bool
		var last, avg float64
		var lastAt *time.Time
		err := tx.QueryRow(ctx, `select c.asset_id::text, c.cumulative, c.last_reading::float8, c.last_reading_at,
		    c.avg_daily_usage::float8
		  from counter c where c.tenant_id = $1 and c.id = $2 for update`, t, counterID).
			Scan(&assetID, &cumulative, &last, &lastAt, &avg)
		if err != nil {
			return notFound(err, "counter")
		}

		var prev Reading
		err = tx.QueryRow(ctx, `select id::text, counter_id::text, reading_at, value::float8, reset, source, message_id
		  from counter_reading where tenant_id = $1
		    and (idempotency_key = $2 or (counter_id = $3 and message_id = $4))
		  limit 1`, t, optStr(key), counterID, optStr(in.MessageID)).
			Scan(&prev.ID, &prev.CounterID, &prev.ReadingAt, &prev.Value, &prev.Reset, &prev.Source, &prev.MessageID)
		if err == nil {
			if prev.CounterID != counterID {
				return problem(422, CodeKeyReuse, "Idempotency-Key was already used for a reading of another counter")
			}
			out, replayed = prev, true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		if cumulative && !in.Reset {
			before := last
			if lastAt != nil && in.ReadingAt.Before(*lastAt) {
				// Out of order: compare with the neighbours in time.
				before = 0
				if err := tx.QueryRow(ctx, `select coalesce((select value::float8 from counter_reading
				    where tenant_id = $1 and counter_id = $2 and reading_at <= $3 order by reading_at desc limit 1), 0)`,
					t, counterID, in.ReadingAt).Scan(&before); err != nil {
					return err
				}
				var after *float64
				if err := tx.QueryRow(ctx, `select (select value::float8 from counter_reading
				    where tenant_id = $1 and counter_id = $2 and reading_at > $3 and not reset order by reading_at limit 1)`,
					t, counterID, in.ReadingAt).Scan(&after); err != nil {
					return err
				}
				if after != nil && in.Value > *after {
					return problem(422, domain.CodeNonMonotonic, fmt.Sprintf(
						"reading %g is above the later reading %g for cumulative counter; correct the value or the reading time", in.Value, *after))
				}
			}
			if err := domain.CheckReading(true, before, in.Value, false); err != nil {
				var re *domain.ReadingError
				if errors.As(err, &re) {
					return problem(422, re.Code, re.Detail)
				}
				return err
			}
		}

		out = Reading{ID: ids.New(), CounterID: counterID, ReadingAt: in.ReadingAt, Value: in.Value,
			Reset: in.Reset, Source: in.Source, MessageID: optStr(in.MessageID)}
		if _, err := tx.Exec(ctx, `insert into counter_reading (tenant_id, id, counter_id, reading_at, value, reset,
		    source, message_id, idempotency_key)
		  values ($1, $2, $3, $4, $5::numeric, $6, $7, $8, $9)`,
			t, out.ID, counterID, in.ReadingAt, numText(in.Value), in.Reset, in.Source,
			out.MessageID, optStr(key)); err != nil {
			return err
		}
		newest := lastAt == nil || !in.ReadingAt.Before(*lastAt)
		windowEnd := in.ReadingAt
		if !newest {
			windowEnd = *lastAt
		}
		if usage, ok, err := rollingUsage(ctx, tx, t, counterID, windowEnd); err != nil {
			return err
		} else if ok {
			avg = usage
		}
		if _, err := tx.Exec(ctx, `update counter set
		    last_reading = case when $6 then $3::numeric else last_reading end,
		    last_reading_at = case when $6 then $4::timestamptz else last_reading_at end,
		    avg_daily_usage = $5::numeric
		  where tenant_id = $1 and id = $2`,
			t, counterID, numText(in.Value), in.ReadingAt, numText(avg), newest); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, t, "counter", counterID, eamevents.CounterReadingLogged, eamevents.CounterReadingData{
			CounterID: counterID, AssetID: assetID, Value: in.Value,
			At: in.ReadingAt.UTC().Format(time.RFC3339Nano), Source: in.Source,
		}); err != nil {
			return err
		}
		if err := evaluateRules(ctx, tx, t, counterID, assetID, in.Value, in.ReadingAt); err != nil {
			return err
		}
		_, err = scheduleLines(ctx, tx, t, "and pl.counter_id = $2", []any{counterID}, s.now())
		return err
	})
	return out, replayed, err
}

// rollingUsage reads the counter's readings in the usage window that ends at
// end and returns the average daily usage (FR-44.3).
func rollingUsage(ctx context.Context, tx pgx.Tx, t, counterID string, end time.Time) (float64, bool, error) {
	rows, err := tx.Query(ctx, `select reading_at, value::float8, reset from counter_reading
	  where tenant_id = $1 and counter_id = $2 and reading_at >= $3 and reading_at <= $4
	  order by reading_at, created_at`, t, counterID, end.Add(-domain.UsageWindow), end)
	if err != nil {
		return 0, false, err
	}
	defer rows.Close()
	var points []domain.UsagePoint
	for rows.Next() {
		var p domain.UsagePoint
		if err := rows.Scan(&p.At, &p.Value, &p.Reset); err != nil {
			return 0, false, err
		}
		points = append(points, p)
	}
	if err := rows.Err(); err != nil {
		return 0, false, err
	}
	usage, ok := domain.RollingUsage(points)
	return usage, ok, nil
}

// ListReadings returns a counter's latest readings, newest first.
func (s *Postgres) ListReadings(ctx context.Context, counterID string, limit int) ([]Reading, error) {
	out := []Reading{}
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		if _, err := getCounter(ctx, tx, t, counterID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select id::text, counter_id::text, reading_at, value::float8, reset, source, message_id
		  from counter_reading where tenant_id = $1 and counter_id = $2 order by reading_at desc, id desc limit $3`,
			t, counterID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r Reading
			if err := rows.Scan(&r.ID, &r.CounterID, &r.ReadingAt, &r.Value, &r.Reset, &r.Source, &r.MessageID); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

type rule struct {
	id, name, operator string
	threshold          float64
	severity           int
	cooldown           int
	lastFired          *time.Time
}

// evaluateRules raises one deduplicated request per breached rule (BR-44.4).
func evaluateRules(ctx context.Context, tx pgx.Tx, t, counterID, assetID string, value float64, at time.Time) error {
	rows, err := tx.Query(ctx, `select id::text, name, operator, threshold::float8, severity, cooldown_minutes, last_fired_at
	  from condition_rule where tenant_id = $1 and counter_id = $2 and active order by id`, t, counterID)
	if err != nil {
		return err
	}
	var rules []rule
	for rows.Next() {
		var r rule
		if err := rows.Scan(&r.id, &r.name, &r.operator, &r.threshold, &r.severity, &r.cooldown, &r.lastFired); err != nil {
			rows.Close()
			return err
		}
		rules = append(rules, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range rules {
		if !domain.Breached(r.operator, r.threshold, value) {
			continue
		}
		key := domain.DedupeKey(assetID, r.id)
		var open bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from maintenance_request
		  where tenant_id = $1 and dedupe_key = $2 and status in ('NEW', 'TRIAGED'))`, t, key).Scan(&open); err != nil {
			return err
		}
		if !domain.ShouldRaise(open, r.lastFired, at, time.Duration(r.cooldown)*time.Minute) {
			continue
		}
		v := value
		ruleID := r.id
		req := Request{AssetID: assetID, Source: "IOT", Severity: r.severity}
		req.Description = fmt.Sprintf("%s breached: reading %g", r.name, value)
		req.ReadingValue = &v
		req.DedupeKey = &key
		req.ConditionRuleID = &ruleID
		if _, err := insertRequest(ctx, tx, t, req, ""); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update condition_rule set last_fired_at = $3 where tenant_id = $1 and id = $2`,
			t, r.id, at); err != nil {
			return err
		}
	}
	return nil
}
