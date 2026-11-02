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
	CodeLedgerPinned = "PAY-GL-004"
	sourceType       = "payroll-run"
	runColumns       = `id::text, legal_entity_id::text, pay_date::text, period_start::text, period_end::text, status, calculated_by, coalesce(finalized_by, ''), coalesce(ledger_id::text, ''), coalesce(journal_entry_id::text, ''), posting_lines`
	statementsOfRun  = `select worker_id::text, rule_set, hash, gross::text, net::text, employer_cost::text, lines from pay_statement where tenant_id = $1 and run_id = $2 order by worker_id`
	postedJournals   = `select posting_lines from pay_run where tenant_id = $1 and legal_entity_id = $2 and status = 'POSTED'`
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
	PostingLines   []domain.JournalLine
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
	var postingLines []byte
	err := tx.QueryRow(ctx, `select `+runColumns+` from pay_run where tenant_id = $1 and id = $2`, tenantID, id).
		Scan(&run.ID, &run.LegalEntityID, &run.PayDate, &run.PeriodStart, &run.PeriodEnd, &status, &run.CalculatedBy, &run.FinalizedBy, &run.LedgerID, &run.JournalEntryID, &postingLines)
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, httpx.NewProblem(http.StatusNotFound, CodeRunNotFound, "pay run not found")
	}
	if err != nil {
		return Run{}, err
	}
	run.Status = domain.RunStatus(status)
	if postingLines != nil {
		if err := json.Unmarshal(postingLines, &run.PostingLines); err != nil {
			return Run{}, err
		}
	}
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

// CreateRun stores a CALCULATED run with its statements. Keys are scoped to the caller and legal entity: a repeated Key with the same RequestHash returns the
// caller's stored run, the same Key with another hash is refused, and another caller's run is never read.
func (s *Postgres) CreateRun(ctx context.Context, in NewRun) (Run, error) {
	p := principal(ctx)
	var run Run
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		id := ids.New()
		tag, err := tx.Exec(ctx, `insert into pay_run (tenant_id, id, legal_entity_id, pay_date, period_start, period_end, status, calculated_by, idempotency_key, request_hash)
			values ($1, $2, $3, $4::date, $5::date, $6::date, $7, $8, $9, $10) on conflict (tenant_id, legal_entity_id, calculated_by, idempotency_key) do nothing`,
			p.TenantID, id, in.LegalEntityID, in.PayDate, in.PeriodStart, in.PeriodEnd, string(domain.RunCalculated), p.UserID, in.Key, in.RequestHash)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var storedID, storedHash string
			if err := tx.QueryRow(ctx, `select id::text, request_hash from pay_run where tenant_id = $1 and legal_entity_id = $2 and calculated_by = $3 and idempotency_key = $4`, p.TenantID, in.LegalEntityID, p.UserID, in.Key).Scan(&storedID, &storedHash); err != nil {
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

func (s *Postgres) postingJournal(ctx context.Context, legalEntityID string, statements []domain.Statement) ([]domain.JournalLine, error) {
	roles, err := domain.RunPosting(statements)
	if err != nil {
		return nil, err
	}
	profile, err := s.GetPostingProfile(ctx, legalEntityID)
	if err != nil {
		return nil, err
	}
	return domain.MapToAccounts(roles, profile)
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
// payroll-run:<id> and records the journal entry on the run. The journal and ledger are fixed on the run before the first ledger call, so a retry
// after a lost answer resends the identical entry whatever the posting profile or the requested ledger became since. A POSTED run is returned unchanged.
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
	if run.PostingLines == nil {
		journal, err := s.postingJournal(ctx, run.LegalEntityID, run.statements())
		if err != nil {
			return Run{}, err
		}
		if run, err = s.fixJournal(ctx, id, journal, ledgerID); err != nil {
			return Run{}, err
		}
	}
	if run.LedgerID != ledgerID {
		return Run{}, httpx.NewProblem(http.StatusConflict, CodeLedgerPinned, "this run's journal is already fixed against ledger "+run.LedgerID)
	}
	entryID, err := ledger.Post(ctx, journalEntry(run, run.LedgerID, run.PostingLines))
	if err != nil {
		code := ledgerclient.CodeOf(err)
		slog.ErrorContext(ctx, "payroll run posting refused by the ledger", "run", id, "code", code, "err", err, "correlationId", middleware.GetReqID(ctx))
		return Run{}, httpx.NewProblem(http.StatusBadGateway, CodeGLRefused, "the general ledger did not accept the payroll journal ("+code+")")
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `update pay_run set status = 'POSTED', journal_entry_id = $3 where tenant_id = $1 and id = $2 and status = 'FINALIZED'`,
			p.TenantID, id, entryID); err != nil {
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

func (s *Postgres) fixJournal(ctx context.Context, id string, journal []domain.JournalLine, ledgerID string) (Run, error) {
	p := principal(ctx)
	lines, err := json.Marshal(journal)
	if err != nil {
		return Run{}, err
	}
	var run Run
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `update pay_run set posting_lines = $3, ledger_id = $4 where tenant_id = $1 and id = $2 and status = 'FINALIZED' and posting_lines is null`,
			p.TenantID, id, lines, ledgerID); err != nil {
			return err
		}
		run, err = loadRun(ctx, tx, p.TenantID, id)
		return err
	})
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

// Reconcile compares, per payroll account, the journals stored on the POSTED runs of a legal entity (debits positive) with the ledger's net balance of that
// account. Difference is the ledger minus the register. The stored journals are used, so later posting-profile edits do not move the register.
func (s *Postgres) Reconcile(ctx context.Context, ledger ledgerclient.Ledger, legalEntityID, ledgerID string) (Reconciliation, error) {
	p := principal(ctx)
	out := Reconciliation{Accounts: []AccountRecon{}, Reconciled: true}
	var order []string
	register := map[string]decimal.Decimal{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, postedJournals, p.TenantID, legalEntityID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			var journal []domain.JournalLine
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &journal); err != nil {
				return err
			}
			out.Runs++
			for _, l := range journal {
				if _, ok := register[l.Account]; !ok {
					order = append(order, l.Account)
				}
				register[l.Account] = register[l.Account].Add(l.Amount)
			}
		}
		return rows.Err()
	})
	if err != nil {
		return Reconciliation{}, err
	}
	for _, account := range order {
		bal, err := ledger.Balance(ctx, ledgerclient.BalanceQuery{LegalEntityID: legalEntityID, LedgerID: ledgerID, Account: account})
		if err != nil {
			slog.ErrorContext(ctx, "payroll reconciliation cannot read the ledger balance", "account", account, "err", err, "correlationId", middleware.GetReqID(ctx))
			return Reconciliation{}, httpx.NewProblem(http.StatusBadGateway, CodeGLRefused, "the general ledger did not answer the balance read for "+account)
		}
		a := AccountRecon{Account: account, Register: register[account], GL: bal.Net()}
		a.Difference = a.GL.Sub(a.Register)
		out.Reconciled = out.Reconciled && a.Difference.IsZero()
		out.Accounts = append(out.Accounts, a)
	}
	return out, nil
}

// GetPostingProfile returns the stored GL account for each payroll account role of a legal entity.
func (s *Postgres) GetPostingProfile(ctx context.Context, legalEntityID string) (map[string]string, error) {
	profile := map[string]string{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select role, account from pay_posting_account where tenant_id = $1 and legal_entity_id = $2`, principal(ctx).TenantID, legalEntityID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var role, account string
			if err := rows.Scan(&role, &account); err != nil {
				return err
			}
			profile[role] = account
		}
		return rows.Err()
	})
	return profile, err
}

// SetPostingProfile stores the given role to GL account mappings for a legal entity and returns the whole profile.
func (s *Postgres) SetPostingProfile(ctx context.Context, legalEntityID string, accounts map[string]string) (map[string]string, error) {
	p := principal(ctx)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		for role, account := range accounts {
			if _, err := tx.Exec(ctx, `insert into pay_posting_account (tenant_id, legal_entity_id, role, account, updated_by) values ($1, $2, $3, $4, $5)
				on conflict (tenant_id, legal_entity_id, role) do update set account = excluded.account, updated_by = excluded.updated_by, updated_at = now()`,
				p.TenantID, legalEntityID, role, account, p.UserID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetPostingProfile(ctx, legalEntityID)
}
