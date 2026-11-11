package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/audit/internal/cdc"
	"github.com/BenjaminSRussell/passage/services/audit/internal/domain"
)

const recordedType = "dev.passage.audit.change.recorded.v1"

// Record is a queryable audit row.
type Record struct {
	ID         string    `json:"id"`
	Entity     string    `json:"entity"`
	RecordID   string    `json:"recordId"`
	Field      string    `json:"field"`
	OldValue   string    `json:"oldValue"`
	NewValue   string    `json:"newValue"`
	UserID     string    `json:"userId"`
	TraceID    string    `json:"traceId"`
	OccurredAt time.Time `json:"occurredAt"`
	Hash       string    `json:"hash"`
}

// Alert is an inbox item produced from a matching rule.
type Alert struct {
	ID       string `json:"id"`
	RuleID   string `json:"ruleId"`
	Entity   string `json:"entity"`
	RecordID string `json:"recordId"`
	Field    string `json:"field"`
	Message  string `json:"message"`
}

// AlertRule is a persisted matcher.
type AlertRule struct {
	ID     string `json:"id"`
	Entity string `json:"entity"`
	Field  string `json:"field"`
	Kind   string `json:"kind"`
}

// Store is the audit persistence port and the CDC consumer.
type Store interface {
	cdc.Consumer
	Query(ctx context.Context, entity, recordID string) ([]Record, error)
	CreateRule(ctx context.Context, rule AlertRule) (AlertRule, error)
	ListAlerts(ctx context.Context) ([]Alert, error)
	Ping(ctx context.Context) error
	// The PHI read-access log (#606): an append-only, hash-chained record of reads of protected health information.
	RecordRead(ctx context.Context, read domain.Read) (ReadRecord, error)
	ListReads(ctx context.Context, f ReadFilter) ([]ReadRecord, error)
	VerifyReads(ctx context.Context) (ReadChain, error)
	ExportReads(ctx context.Context, f ReadFilter, begin func(ReadChain) error, each func(ReadRecord) error) error
}

// Postgres implements Store.
type Postgres struct{ pool *pgxpool.Pool }

// New wraps pool.
func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Ping checks the pool.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Apply appends a change onto the tenant hash chain and fires matching alerts.
func (s *Postgres) Apply(ctx context.Context, change domain.Change) error {
	if fields := domain.Validate(change); len(fields) > 0 {
		p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request failed validation")
		p.Errors = fields
		return p
	}
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var prev string
		err := tx.QueryRow(ctx, `select hash from audit_entry where tenant_id=$1 order by seq desc limit 1`, p.TenantID).Scan(&prev)
		if errors.Is(err, pgx.ErrNoRows) {
			prev = domain.Genesis
			err = nil
		}
		if err != nil {
			return err
		}
		entry, err := domain.Link(prev, change)
		if err != nil {
			return err
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into audit_entry
			(tenant_id, id, entity, record_id, field, old_value, new_value, user_id, trace_id, occurred_at, prev_hash, hash)
			values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			p.TenantID, id, change.Entity, change.RecordID, change.Field, change.OldValue, change.NewValue, change.UserID, change.TraceID, change.OccurredAt, entry.PrevHash, entry.Hash); err != nil {
			return err
		}
		if err := fireAlerts(ctx, tx, p.TenantID, change); err != nil {
			return err
		}
		evt, err := events.New(recordedType, "passage/audit", change.RecordID, p.TenantID, map[string]any{
			"entity": change.Entity, "recordId": change.RecordID, "field": change.Field,
			"oldValue": change.OldValue, "newValue": change.NewValue, "userId": change.UserID, "traceId": change.TraceID,
		})
		if err != nil {
			return err
		}
		return outbox.Enqueue(ctx, tx, "change", id, evt)
	})
}

func fireAlerts(ctx context.Context, tx pgx.Tx, tenantID string, change domain.Change) error {
	rows, err := tx.Query(ctx, `select id, entity, field, kind from alert_rule where tenant_id=$1`, tenantID)
	if err != nil {
		return err
	}
	var rules []domain.AlertRule
	for rows.Next() {
		var rule domain.AlertRule
		if err := rows.Scan(&rule.ID, &rule.Entity, &rule.Field, &rule.Kind); err != nil {
			rows.Close()
			return err
		}
		rules = append(rules, rule)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, rule := range rules {
		if !domain.Match(rule, change) {
			continue
		}
		if _, err := tx.Exec(ctx, `insert into alert_inbox (tenant_id, id, rule_id, entity, record_id, field, message)
			values ($1,$2,$3,$4,$5,$6,$7)`, tenantID, ids.New(), rule.ID, change.Entity, change.RecordID, change.Field,
			change.Field+" changed from "+change.OldValue+" to "+change.NewValue); err != nil {
			return err
		}
	}
	return nil
}

// Query returns field changes for one record, oldest first.
func (s *Postgres) Query(ctx context.Context, entity, recordID string) ([]Record, error) {
	p, _ := tenant.FromContext(ctx)
	var out []Record
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id, entity, record_id, field, old_value, new_value, user_id, trace_id, occurred_at, hash
			from audit_entry where tenant_id=$1 and entity=$2 and record_id=$3 order by seq`, p.TenantID, entity, recordID)
		if err != nil {
			return err
		}
		defer rows.Close()
		out = []Record{}
		for rows.Next() {
			var rec Record
			if err := rows.Scan(&rec.ID, &rec.Entity, &rec.RecordID, &rec.Field, &rec.OldValue, &rec.NewValue, &rec.UserID, &rec.TraceID, &rec.OccurredAt, &rec.Hash); err != nil {
				return err
			}
			out = append(out, rec)
		}
		return rows.Err()
	})
	return out, err
}

// CreateRule stores an org-wide alert rule.
func (s *Postgres) CreateRule(ctx context.Context, rule AlertRule) (AlertRule, error) {
	p, _ := tenant.FromContext(ctx)
	if rule.Kind == "" {
		rule.Kind = domain.AlertChange
	}
	rule.ID = ids.New()
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into alert_rule (tenant_id, id, entity, field, kind) values ($1,$2,$3,$4,$5)`,
			p.TenantID, rule.ID, rule.Entity, rule.Field, rule.Kind)
		return err
	})
	return rule, err
}

// ListAlerts returns the inbox.
func (s *Postgres) ListAlerts(ctx context.Context) ([]Alert, error) {
	p, _ := tenant.FromContext(ctx)
	var out []Alert
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id, rule_id, entity, record_id, field, message from alert_inbox where tenant_id=$1 order by created_at`, p.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		out = []Alert{}
		for rows.Next() {
			var a Alert
			if err := rows.Scan(&a.ID, &a.RuleID, &a.Entity, &a.RecordID, &a.Field, &a.Message); err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}
