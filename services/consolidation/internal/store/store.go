// Package store persists consolidation runs in Postgres. A run, its balances,
// eliminations and statement lines are inserted once in one transaction and
// never updated; a rerun is a new version. Every query filters on the caller's
// tenant_id and runs inside dbx.WithTenantTx, so row-level security scopes it
// too; the completion event goes to the outbox in the same transaction.
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/consolidation/internal/domain"
	coevents "github.com/BenjaminSRussell/passage/services/consolidation/internal/events"
)

const StatusCompleted = "completed"

type Postgres struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

type RunRecord struct {
	ID               string
	GroupID          string
	Period           string
	Currency         string
	Version          int
	Status           string
	InputsHash       string
	CreatedBy        string
	UnitCount        int
	EliminationCount int
	CTA              decimal.Decimal
	CreatedAt        time.Time
}

type RunDetail struct {
	RunRecord
	Balances []domain.Balance
	Entries  []domain.UnitEntry
}

type RunStatements struct {
	RunRecord
	Lines []domain.StatementLine
}

type RunFilter struct {
	GroupID string
	Period  string
}

type RunPage struct {
	Items []RunRecord
	Next  string
}

const runColumns = `id::text, group_id, period, currency, version, status, inputs_hash, created_by, unit_count, elimination_count, cta_amount::text, created_at`

func errRunNotFound(id string) error {
	return &domain.Error{Code: domain.CodeRunNotFound, Detail: "no consolidation run with id " + id}
}

func principal(ctx context.Context) (tenant.Principal, error) {
	pr, ok := tenant.FromContext(ctx)
	if !ok {
		return pr, errors.New("store: no principal in context")
	}
	return pr, nil
}

// CreateRun stores a computed run as the next version of its group, period and
// currency and publishes consol.run.completed in the same transaction. With an
// Idempotency-Key a retry returns the stored run and replayed=true; reusing the
// key with other inputs fails with CNS-0922.
func (s *Postgres) CreateRun(ctx context.Context, in domain.RunInput, res domain.RunResult, key string) (RunRecord, bool, error) {
	pr, err := principal(ctx)
	if err != nil {
		return RunRecord{}, false, err
	}
	createdBy := pr.UserID
	if createdBy == "" {
		createdBy = pr.Service
	}
	if createdBy == "" {
		return RunRecord{}, false, errors.New("store: principal has neither user nor service")
	}
	raw, err := in.Canonical()
	if err != nil {
		return RunRecord{}, false, err
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	var (
		rec      RunRecord
		replayed bool
	)
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		create := func() (RunRecord, error) { return insertRun(ctx, tx, pr.TenantID, createdBy, in, res, raw, hash) }
		if key == "" {
			var err error
			rec, err = create()
			return err
		}
		var err error
		rec, replayed, err = idempotent(ctx, tx, pr.TenantID, key, hash, create)
		return err
	})
	if err != nil {
		return RunRecord{}, false, wrap("create consolidation run", err)
	}
	return rec, replayed, nil
}

func insertRun(ctx context.Context, tx pgx.Tx, tenantID, createdBy string, in domain.RunInput, res domain.RunResult, raw []byte, hash string) (RunRecord, error) {
	lock := fmt.Sprintf("%s|%s|%s|%s", tenantID, in.GroupID, in.Period, in.Currency)
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, lock); err != nil {
		return RunRecord{}, err
	}
	var version int
	if err := tx.QueryRow(ctx, `select coalesce(max(version), 0) + 1 from consolidation_run
		where tenant_id = $1 and group_id = $2 and period = $3 and currency = $4`, tenantID, in.GroupID, in.Period, in.Currency).Scan(&version); err != nil {
		return RunRecord{}, err
	}
	id := ids.New()
	rec, err := scanRun(tx.QueryRow(ctx, `insert into consolidation_run (tenant_id, id, group_id, period, currency, version, status, inputs_hash, inputs,
			unit_count, elimination_count, cta_amount, created_by)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::numeric, $13)
		returning `+runColumns,
		tenantID, id, in.GroupID, in.Period, in.Currency, version, StatusCompleted, hash, raw,
		len(in.Units), len(res.Entries), res.CTA.String(), createdBy))
	if err != nil {
		return RunRecord{}, err
	}
	var batch pgx.Batch
	for i, b := range res.Balances {
		batch.Queue(`insert into consolidation_balance (tenant_id, run_id, seq, unit_id, account, amount) values ($1, $2, $3, $4, $5, $6::numeric)`,
			tenantID, id, i, b.UnitID, b.Account, b.Amount.String())
	}
	for i, e := range res.Entries {
		batch.Queue(`insert into consolidation_entry (tenant_id, run_id, seq, unit_id, rule_code, account, debit, credit) values ($1, $2, $3, $4, $5, $6, $7::numeric, $8::numeric)`,
			tenantID, id, i, e.UnitID, e.RuleCode, e.Account, e.Debit.String(), e.Credit.String())
	}
	for i, l := range res.Statements {
		batch.Queue(`insert into consolidation_statement_line (tenant_id, run_id, seq, statement, line_code, amount) values ($1, $2, $3, $4, $5, $6::numeric)`,
			tenantID, id, i, l.Statement, l.Code, l.Amount.String())
	}
	results := tx.SendBatch(ctx, &batch)
	for range batch.Len() {
		if _, err := results.Exec(); err != nil {
			_ = results.Close()
			return RunRecord{}, err
		}
	}
	if err := results.Close(); err != nil {
		return RunRecord{}, err
	}
	evt, err := events.New(coevents.RunCompleted, coevents.Source, rec.ID, tenantID, coevents.RunCompletedData{
		RunID: rec.ID, GroupID: rec.GroupID, Period: rec.Period, Version: rec.Version,
		Currency: rec.Currency, UnitCount: rec.UnitCount, EliminationCount: rec.EliminationCount,
		CTAAmount: rec.CTA.StringFixed(money.MinorUnits(rec.Currency)),
	})
	if err != nil {
		return RunRecord{}, err
	}
	return rec, outbox.Enqueue(ctx, tx, "consolidation_run", rec.ID, evt)
}

// ListRuns returns one page of the tenant's runs, newest first, after the cursor
// (the id of the last run of the previous page). limit must be at least 1.
func (s *Postgres) ListRuns(ctx context.Context, f RunFilter, after string, limit int) (RunPage, error) {
	pr, err := principal(ctx)
	if err != nil {
		return RunPage{}, err
	}
	page := RunPage{Items: []RunRecord{}}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+runColumns+` from consolidation_run
			where tenant_id = $1
			  and ($2::text = '' or group_id = $2)
			  and ($3::text = '' or period = $3)
			  and ($4::text = '' or (created_at, id) < (select created_at, id from consolidation_run where tenant_id = $1 and id = $4::uuid))
			order by created_at desc, id desc limit $5`, pr.TenantID, f.GroupID, f.Period, after, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			rec, err := scanRun(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, rec)
		}
		return rows.Err()
	})
	if err != nil {
		return RunPage{}, wrap("list consolidation runs", err)
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.Next = page.Items[limit-1].ID
	}
	return page, nil
}

// GetRun returns a run with its balances and eliminations, or CNS-0921.
func (s *Postgres) GetRun(ctx context.Context, id string) (RunDetail, error) {
	var d RunDetail
	err := s.readRun(ctx, id, func(tx pgx.Tx, rec RunRecord, tenantID string) error {
		d.RunRecord = rec
		brows, err := tx.Query(ctx, `select unit_id, account, amount::text from consolidation_balance where tenant_id = $1 and run_id = $2::uuid order by seq`, tenantID, id)
		if err != nil {
			return err
		}
		defer brows.Close()
		for brows.Next() {
			var b domain.Balance
			var amount string
			if err := brows.Scan(&b.UnitID, &b.Account, &amount); err != nil {
				return err
			}
			if b.Amount, err = decimal.NewFromString(amount); err != nil {
				return fmt.Errorf("store: balance %q: %w", amount, err)
			}
			d.Balances = append(d.Balances, b)
		}
		if err := brows.Err(); err != nil {
			return err
		}
		erows, err := tx.Query(ctx, `select unit_id, rule_code, account, debit::text, credit::text from consolidation_entry where tenant_id = $1 and run_id = $2::uuid order by seq`, tenantID, id)
		if err != nil {
			return err
		}
		defer erows.Close()
		for erows.Next() {
			var e domain.UnitEntry
			var debit, credit string
			if err := erows.Scan(&e.UnitID, &e.RuleCode, &e.Account, &debit, &credit); err != nil {
				return err
			}
			if e.Debit, err = decimal.NewFromString(debit); err != nil {
				return fmt.Errorf("store: debit %q: %w", debit, err)
			}
			if e.Credit, err = decimal.NewFromString(credit); err != nil {
				return fmt.Errorf("store: credit %q: %w", credit, err)
			}
			d.Entries = append(d.Entries, e)
		}
		return erows.Err()
	})
	if err != nil {
		return RunDetail{}, wrap("get consolidation run", err)
	}
	return d, nil
}

// GetStatements returns a run's statement lines, or CNS-0921.
func (s *Postgres) GetStatements(ctx context.Context, id string) (RunStatements, error) {
	var st RunStatements
	err := s.readRun(ctx, id, func(tx pgx.Tx, rec RunRecord, tenantID string) error {
		st.RunRecord = rec
		rows, err := tx.Query(ctx, `select statement, line_code, amount::text from consolidation_statement_line where tenant_id = $1 and run_id = $2::uuid order by seq`, tenantID, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l domain.StatementLine
			var amount string
			if err := rows.Scan(&l.Statement, &l.Code, &amount); err != nil {
				return err
			}
			if l.Amount, err = decimal.NewFromString(amount); err != nil {
				return fmt.Errorf("store: statement amount %q: %w", amount, err)
			}
			st.Lines = append(st.Lines, l)
		}
		return rows.Err()
	})
	if err != nil {
		return RunStatements{}, wrap("get consolidation statements", err)
	}
	return st, nil
}

func (s *Postgres) readRun(ctx context.Context, id string, fn func(tx pgx.Tx, rec RunRecord, tenantID string) error) error {
	pr, err := principal(ctx)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rec, err := scanRun(tx.QueryRow(ctx, `select `+runColumns+` from consolidation_run where tenant_id = $1 and id = $2::uuid`, pr.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return errRunNotFound(id)
		}
		if err != nil {
			return err
		}
		return fn(tx, rec, pr.TenantID)
	})
}

func scanRun(row pgx.Row) (RunRecord, error) {
	var rec RunRecord
	var cta string
	err := row.Scan(&rec.ID, &rec.GroupID, &rec.Period, &rec.Currency, &rec.Version, &rec.Status, &rec.InputsHash, &rec.CreatedBy,
		&rec.UnitCount, &rec.EliminationCount, &cta, &rec.CreatedAt)
	if err != nil {
		return RunRecord{}, err
	}
	if rec.CTA, err = decimal.NewFromString(cta); err != nil {
		return RunRecord{}, fmt.Errorf("store: cta %q: %w", cta, err)
	}
	return rec, nil
}

// idempotent runs a command at most once per (tenant, Idempotency-Key). The
// key is claimed first, so a concurrent request with the same key waits on the
// primary key and then replays; the response is stored in the same transaction.
func idempotent[T any](ctx context.Context, tx pgx.Tx, tenantID, key, hash string, run func() (T, error)) (out T, replayed bool, err error) {
	tag, err := tx.Exec(ctx, `insert into idempotency_keys (tenant_id, key, request_hash, response_status) values ($1, $2, $3, 0)
		on conflict (tenant_id, key) do nothing`, tenantID, key, hash)
	if err != nil {
		return out, false, err
	}
	if tag.RowsAffected() == 0 {
		var stored string
		var body []byte
		if err := tx.QueryRow(ctx, `select request_hash, response_body from idempotency_keys where tenant_id = $1 and key = $2`, tenantID, key).Scan(&stored, &body); err != nil {
			return out, false, err
		}
		if stored != hash {
			return out, false, &domain.Error{Code: domain.CodeIdempotencyReuse, Detail: "Idempotency-Key was already used with different inputs"}
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return out, false, fmt.Errorf("replay %s: %w", key, err)
		}
		return out, true, nil
	}
	if out, err = run(); err != nil {
		return out, false, err
	}
	body, err := json.Marshal(out)
	if err != nil {
		return out, false, err
	}
	_, err = tx.Exec(ctx, `update idempotency_keys set response_status = 201, response_body = $3 where tenant_id = $1 and key = $2`, tenantID, key, body)
	return out, false, err
}

func wrap(op string, err error) error {
	var de *domain.Error
	if errors.As(err, &de) {
		return de
	}
	return fmt.Errorf("%s: %w", op, err)
}
