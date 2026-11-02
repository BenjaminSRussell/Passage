package store

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/payroll/internal/domain"
)

const (
	Currency         = "USD"
	CodeRunNotFound  = "PAY-RUN-007"
	CodeKeyConflict  = "PAY-RUN-008"
	CodeGLRefused    = "PAY-GL-001"
	sourceType       = "payroll-run"
	runColumns       = `id::text, legal_entity_id::text, pay_date::text, period_start::text, period_end::text, status, calculated_by, coalesce(finalized_by, ''), coalesce(ledger_id::text, ''), coalesce(journal_entry_id::text, '')`
	statementsOfRun  = `select worker_id::text, rule_set, hash, gross::text, net::text, employer_cost::text, lines from pay_statement where tenant_id = $1 and run_id = $2 order by worker_id`
	postedStatements = `select s.lines, s.net::text from pay_statement s join pay_run r on r.tenant_id = s.tenant_id and r.id = s.run_id where s.tenant_id = $1 and r.legal_entity_id = $2 and r.status = 'POSTED'`
	lockRun          = `select status, calculated_by from pay_run where tenant_id = $1 and id = $2 for update`
)

type Postgres struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

type NewStatement struct {
	WorkerID  string
	RuleSet   string
	Hash      string
	Statement domain.Statement
}

type NewRun struct {
	LegalEntityID string
	PayDate       string
	PeriodStart   string
	PeriodEnd     string
	Key           string
	RequestHash   string
	Statements    []NewStatement
}

type StatementRow struct {
	WorkerID     string
	RuleSet      string
	Hash         string
	Gross        decimal.Decimal
	Net          decimal.Decimal
	EmployerCost decimal.Decimal
	Lines        []domain.Line
}

type Run struct {
	ID             string
	LegalEntityID  string
	PayDate        string
	PeriodStart    string
	PeriodEnd      string
	Status         domain.RunStatus
	CalculatedBy   string
	FinalizedBy    string
	LedgerID       string
	JournalEntryID string
	Statements     []StatementRow
}

func (r Run) Totals() (gross, net, employer decimal.Decimal) {
	for _, s := range r.Statements {
		gross, net, employer = gross.Add(s.Gross), net.Add(s.Net), employer.Add(s.EmployerCost)
	}
	return gross, net, employer
}

func (r Run) statements() []domain.Statement {
	out := make([]domain.Statement, len(r.Statements))
	for i, s := range r.Statements {
		out[i] = domain.Statement{Lines: s.Lines, Net: s.Net}
	}
	return out
}

func principal(ctx context.Context) tenant.Principal {
	p, _ := tenant.FromContext(ctx)
	return p
}

func loadRun(ctx context.Context, tx pgx.Tx, tenantID, id string) (Run, error) {
	var run Run
	var status string
	err := tx.QueryRow(ctx, `select `+runColumns+` from pay_run where tenant_id = $1 and id = $2`, tenantID, id).
		Scan(&run.ID, &run.LegalEntityID, &run.PayDate, &run.PeriodStart, &run.PeriodEnd, &status, &run.CalculatedBy, &run.FinalizedBy, &run.LedgerID, &run.JournalEntryID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, httpx.NewProblem(http.StatusNotFound, CodeRunNotFound, "pay run not found")
	}
	if err != nil {
		return Run{}, err
	}
	run.Status = domain.RunStatus(status)
	rows, err := tx.Query(ctx, statementsOfRun, tenantID, id)
	if err != nil {
		return Run{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var s StatementRow
		var gross, net, employer string
		var lines []byte
		if err := rows.Scan(&s.WorkerID, &s.RuleSet, &s.Hash, &gross, &net, &employer, &lines); err != nil {
			return Run{}, err
		}
		s.Gross, s.Net, s.EmployerCost = decimal.RequireFromString(gross), decimal.RequireFromString(net), decimal.RequireFromString(employer)
		if err := json.Unmarshal(lines, &s.Lines); err != nil {
			return Run{}, err
		}
		run.Statements = append(run.Statements, s)
	}
	return run, rows.Err()
}

func (s *Postgres) GetRun(ctx context.Context, id string) (Run, error) {
	var run Run
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		run, err = loadRun(ctx, tx, principal(ctx).TenantID, id)
		return err
	})
	return run, err
}

// CreateRun stores a CALCULATED run with its statements. A repeated Key with the same RequestHash returns the stored run; the same Key with another hash is refused.
func (s *Postgres) CreateRun(ctx context.Context, in NewRun) (Run, error) {
	p := principal(ctx)
	var run Run
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		id := ids.New()
		tag, err := tx.Exec(ctx, `insert into pay_run (tenant_id, id, legal_entity_id, pay_date, period_start, period_end, status, calculated_by, idempotency_key, request_hash)
			values ($1, $2, $3, $4::date, $5::date, $6::date, $7, $8, $9, $10) on conflict (tenant_id, idempotency_key) do nothing`,
			p.TenantID, id, in.LegalEntityID, in.PayDate, in.PeriodStart, in.PeriodEnd, string(domain.RunCalculated), p.UserID, in.Key, in.RequestHash)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var storedID, storedHash string
			if err := tx.QueryRow(ctx, `select id::text, request_hash from pay_run where tenant_id = $1 and idempotency_key = $2`, p.TenantID, in.Key).Scan(&storedID, &storedHash); err != nil {
				return err
			}
			if storedHash != in.RequestHash {
				return httpx.NewProblem(http.StatusConflict, CodeKeyConflict, "the Idempotency-Key was used with a different request")
			}
			run, err = loadRun(ctx, tx, p.TenantID, storedID)
			return err
		}
		for _, st := range in.Statements {
			lines, err := json.Marshal(st.Statement.Lines)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into pay_statement (tenant_id, run_id, worker_id, legal_entity_id, rule_set, hash, gross, net, employer_cost, lines)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
				p.TenantID, id, st.WorkerID, in.LegalEntityID, st.RuleSet, st.Hash, st.Statement.Gross, st.Statement.Net, st.Statement.EmployerCost, lines); err != nil {
				return err
			}
		}
		run, err = loadRun(ctx, tx, p.TenantID, id)
		return err
	})
	return run, err
}

// Advance moves a run to REVIEWED or FINALIZED under the lifecycle; the user who calculated a run cannot finalize it (BR-43.8).
func (s *Postgres) Advance(ctx context.Context, id string, to domain.RunStatus) (Run, error) {
	p := principal(ctx)
	var run Run
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var from, calculatedBy string
		if err := tx.QueryRow(ctx, lockRun, p.TenantID, id).Scan(&from, &calculatedBy); errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeRunNotFound, "pay run not found")
		} else if err != nil {
			return err
		}
		if err := domain.CheckTransition(domain.RunStatus(from), to); err != nil {
			return err
		}
		if to == domain.RunFinalized {
			if err := domain.CheckFinalize(calculatedBy, p.UserID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `update pay_run set finalized_by = $3 where tenant_id = $1 and id = $2`, p.TenantID, id, p.UserID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `update pay_run set status = $3 where tenant_id = $1 and id = $2`, p.TenantID, id, string(to)); err != nil {
			return err
		}
		var err error
		run, err = loadRun(ctx, tx, p.TenantID, id)
		return err
	})
	return run, err
}

func journalEntry(run Run, ledgerID string, journal []domain.JournalLine) ledgerclient.Entry {
	e := ledgerclient.Entry{LegalEntityID: run.LegalEntityID, LedgerID: ledgerID, DocumentID: run.ID, PostingDate: run.PayDate, Currency: Currency,
		IdempotencyKey: sourceType + ":" + run.ID, SourceType: sourceType}
	for _, l := range journal {
		if !l.Amount.IsZero() {
			e.Lines = append(e.Lines, ledgerclient.Line{Account: l.Account, Credit: l.Amount.IsNegative(), Amount: l.Amount.Abs()})
		}
	}
	return e
}

// PostRun sends the balanced run journal (gross, employer cost, tax and deduction liabilities, net pay clearing) to the ledger under the key
// payroll-run:<id> and records the journal entry on the run. A POSTED run is returned unchanged; a retry after a lost answer resends the same journal.
func (s *Postgres) PostRun(ctx context.Context, ledger ledgerclient.Ledger, id, ledgerID string) (Run, error) {
	p := principal(ctx)
	run, err := s.GetRun(ctx, id)
	if err != nil {
		return Run{}, err
	}
	if run.Status == domain.RunPosted {
		return run, nil
	}
	if err := domain.CheckTransition(run.Status, domain.RunPosted); err != nil {
		return Run{}, err
	}
	journal, err := domain.RunPosting(run.statements())
	if err != nil {
		return Run{}, err
	}
	entryID, err := ledger.Post(ctx, journalEntry(run, ledgerID, journal))
	if err != nil {
		code := ledgerclient.CodeOf(err)
		slog.ErrorContext(ctx, "payroll run posting refused by the ledger", "run", id, "code", code, "err", err, "correlationId", middleware.GetReqID(ctx))
		return Run{}, httpx.NewProblem(http.StatusBadGateway, CodeGLRefused, "the general ledger did not accept the payroll journal ("+code+")")
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `update pay_run set status = 'POSTED', ledger_id = $3, journal_entry_id = $4 where tenant_id = $1 and id = $2 and status = 'FINALIZED'`,
			p.TenantID, id, ledgerID, entryID); err != nil {
			return err
		}
		run, err = loadRun(ctx, tx, p.TenantID, id)
		return err
	})
	if err != nil {
		slog.ErrorContext(ctx, "payroll run posted to the ledger but not recorded", "run", id, "entryId", entryID, "err", err, "correlationId", middleware.GetReqID(ctx))
	}
	return run, err
}

type AccountRecon struct {
	Account    string
	Register   decimal.Decimal
	GL         decimal.Decimal
	Difference decimal.Decimal
}

type Reconciliation struct {
	Runs       int
	Accounts   []AccountRecon
	Reconciled bool
}

// Reconcile compares, per payroll account, the journal the POSTED runs of a legal entity imply (debits positive) with the ledger's net balance of that
// account. Difference is the ledger minus the register.
func (s *Postgres) Reconcile(ctx context.Context, ledger ledgerclient.Ledger, legalEntityID, ledgerID string) (Reconciliation, error) {
	p := principal(ctx)
	var statements []domain.Statement
	out := Reconciliation{Accounts: []AccountRecon{}, Reconciled: true}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, postedStatements, p.TenantID, legalEntityID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var lines []byte
			var net string
			if err := rows.Scan(&lines, &net); err != nil {
				return err
			}
			st := domain.Statement{Net: decimal.RequireFromString(net)}
			if err := json.Unmarshal(lines, &st.Lines); err != nil {
				return err
			}
			statements = append(statements, st)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `select count(*) from pay_run where tenant_id = $1 and legal_entity_id = $2 and status = 'POSTED'`, p.TenantID, legalEntityID).Scan(&out.Runs)
	})
	if err != nil {
		return Reconciliation{}, err
	}
	journal, err := domain.RunPosting(statements)
	if err != nil {
		return Reconciliation{}, err
	}
	for _, l := range journal {
		bal, err := ledger.Balance(ctx, ledgerclient.BalanceQuery{LegalEntityID: legalEntityID, LedgerID: ledgerID, Account: l.Account})
		if err != nil {
			slog.ErrorContext(ctx, "payroll reconciliation cannot read the ledger balance", "account", l.Account, "err", err, "correlationId", middleware.GetReqID(ctx))
			return Reconciliation{}, httpx.NewProblem(http.StatusBadGateway, CodeGLRefused, "the general ledger did not answer the balance read for "+l.Account)
		}
		a := AccountRecon{Account: l.Account, Register: l.Amount, GL: bal.Net()}
		a.Difference = a.GL.Sub(a.Register)
		out.Reconciled = out.Reconciled && a.Difference.IsZero()
		out.Accounts = append(out.Accounts, a)
	}
	return out, nil
}
