// Package store persists bank accounts, statements and forecast entries.
// Every query runs inside dbx.WithTenantTx, so row-level security scopes
// rows to the caller's tenant and legal entities.
package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
	tevents "github.com/BenjaminSRussell/passage/services/treasury/internal/events"
)

// Problem codes raised by the store (see README). TRS-0405, TRS-0406, TRS-0408
// and TRS-0494 come from the domain package.
const (
	CodeAccountNotFound   = "TRS-0400"
	CodeAccountLimit      = "TRS-0403"
	CodeAccountDuplicate  = "TRS-0404"
	CodeAccountState      = "TRS-0417"
	CodeAccountInactive   = "TRS-0412"
	CodeStatementNotFound = "TRS-0413"
	CodeVersionConflict   = "conflict_version"
	CodeIdempotencyReuse  = "idempotency_key_reuse"
	MaxStatementsListed   = 100
	MaxLinesPage          = 500
	uniqueViolation       = "23505"
	eventSource           = "/passage/treasury/treasury"
)

// BankAccount is the API view of a bank account. The IBAN itself is never
// stored or returned: only its hash (for lookups), a mask and the last four characters.
type BankAccount struct {
	ID            string    `json:"id"`
	LegalEntityID string    `json:"legalEntityId"`
	AccountCode   string    `json:"accountCode"`
	IBANMasked    string    `json:"ibanMasked"`
	IBANLast4     string    `json:"ibanLast4"`
	BIC           *string   `json:"bic,omitempty"`
	Currency      string    `json:"currency"`
	MainAccountID string    `json:"mainAccountId"`
	Status        string    `json:"status"`
	Version       int       `json:"version"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// NewBankAccount is a validated create request. IBAN is the normalized value and is used only to derive the hash and mask.
type NewBankAccount struct {
	LegalEntityID string
	AccountCode   string
	IBAN          string
	BIC           *string
	Currency      string
	MainAccountID string
}

// Statement is the stored header of an imported statement.
type Statement struct {
	ID             string    `json:"id"`
	BankAccountID  string    `json:"bankAccountId"`
	LegalEntityID  string    `json:"legalEntityId"`
	StatementID    string    `json:"statementId"`
	Sequence       *int      `json:"sequence,omitempty"`
	Format         string    `json:"format"`
	Currency       string    `json:"currency"`
	OpeningDate    string    `json:"openingDate"`
	OpeningBalance string    `json:"openingBalance"`
	ClosingDate    string    `json:"closingDate"`
	ClosingBalance string    `json:"closingBalance"`
	LineCount      int       `json:"lineCount"`
	CreatedAt      time.Time `json:"createdAt"`
}

// StatementLine is one entry without its raw source fragment, which stays in the database for audit.
type StatementLine struct {
	LineNo           int    `json:"lineNo"`
	BookingDate      string `json:"bookingDate"`
	ValueDate        string `json:"valueDate"`
	Amount           string `json:"amount"`
	Currency         string `json:"currency"`
	BTCCode          string `json:"btcCode,omitempty"`
	EndToEndRef      string `json:"endToEndRef,omitempty"`
	Reference        string `json:"reference,omitempty"`
	CounterpartyName string `json:"counterpartyName,omitempty"`
	Status           string `json:"status"`
}

// StatementImport is a parsed file ready to persist.
type StatementImport struct {
	BankAccountID string
	FileHash      string
	Parsed        domain.Statement
}

// Warning is a non-blocking finding of an import, such as a sequence gap (TRS-0407).
type Warning struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// ImportResult reports whether the call stored a new statement or replayed an
// existing one, and the warnings raised while storing a new one.
type ImportResult struct {
	Statement Statement
	Replayed  bool
	Warnings  []Warning
}

// ForecastEntry is a dated expected cash flow. Amount is signed: inflow positive.
type ForecastEntry struct {
	ID            string `json:"id"`
	LegalEntityID string `json:"legalEntityId"`
	SourceType    string `json:"sourceType"`
	SourceID      string `json:"sourceId"`
	ExpectedDate  string `json:"expectedDate"`
	Amount        string `json:"amount"`
	Currency      string `json:"currency"`
	Confidence    string `json:"confidence"`
}

// Store is the persistence surface the API depends on.
type Store interface {
	CreateBankAccount(ctx context.Context, in NewBankAccount, idempotencyKey string) (BankAccount, error)
	ListBankAccounts(ctx context.Context, legalEntityID string) ([]BankAccount, error)
	GetBankAccount(ctx context.Context, id string) (BankAccount, error)
	TransitionBankAccount(ctx context.Context, id string, version int, to domain.AccountStatus) (BankAccount, error)
	ImportStatement(ctx context.Context, in StatementImport) (ImportResult, error)
	GetStatement(ctx context.Context, id string) (Statement, error)
	ListStatements(ctx context.Context, bankAccountID string) ([]Statement, error)
	ListStatementLines(ctx context.Context, statementID string, afterLineNo, limit int) ([]StatementLine, error)
	CreateForecastEntry(ctx context.Context, in ForecastEntry, idempotencyKey string) (ForecastEntry, error)
	ListForecastEntries(ctx context.Context, legalEntityID, currency, from, to string) ([]ForecastEntry, error)
	Ping(ctx context.Context) error
}

// Postgres implements Store on pgx.
type Postgres struct {
	pool *pgxpool.Pool
}

// New returns a store over pool.
func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Ping checks the database connection for readiness.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// ibanHash is the lookup key for an IBAN: SHA-256 of the normalized value.
func ibanHash(iban string) []byte {
	sum := sha256.Sum256([]byte(domain.NormalizeIBAN(iban)))
	return sum[:]
}

const bankAccountColumns = "id, legal_entity_id, account_code, iban_masked, iban_last4, bic, currency, main_account_id, status, version, created_at, updated_at"

func scanBankAccount(row pgx.Row, extra ...any) (BankAccount, error) {
	var a BankAccount
	err := row.Scan(append([]any{&a.ID, &a.LegalEntityID, &a.AccountCode, &a.IBANMasked, &a.IBANLast4, &a.BIC, &a.Currency, &a.MainAccountID, &a.Status, &a.Version, &a.CreatedAt, &a.UpdatedAt}, extra...)...)
	return a, err
}

func notFound(code, detail string) error {
	return httpx.NewProblem(http.StatusNotFound, code, detail)
}

// CreateBankAccount stores a new Draft account. A repeated Idempotency-Key
// returns the original account; reusing it with another body is refused.
func (s *Postgres) CreateBankAccount(ctx context.Context, in NewBankAccount, idempotencyKey string) (BankAccount, error) {
	p, _ := tenant.FromContext(ctx)
	hash := ibanHash(in.IBAN)
	reqHash, err := requestHash("create-bank-account", in)
	if err != nil {
		return BankAccount{}, err
	}
	var out BankAccount
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = idempotent(ctx, tx, idempotencyKey, reqHash, func() (BankAccount, error) {
			var count int
			if err := tx.QueryRow(ctx, `select count(*) from bank_account where tenant_id = $1 and legal_entity_id = $2`, p.TenantID, in.LegalEntityID).Scan(&count); err != nil {
				return BankAccount{}, err
			}
			if count >= domain.MaxAccountsPerEntity {
				return BankAccount{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeAccountLimit, "a legal entity may hold at most 500 bank accounts")
			}
			return scanBankAccount(tx.QueryRow(ctx, `insert into bank_account (tenant_id, id, legal_entity_id, account_code, iban_hash, iban_masked, iban_last4, bic, currency, main_account_id)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) returning `+bankAccountColumns,
				p.TenantID, ids.New(), in.LegalEntityID, in.AccountCode, hash, domain.MaskIBAN(in.IBAN), domain.Last4(in.IBAN), in.BIC, in.Currency, in.MainAccountID))
		})
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return BankAccount{}, httpx.NewProblem(http.StatusConflict, CodeAccountDuplicate, "a bank account with this code or IBAN already exists")
	}
	return out, err
}

// ListBankAccounts returns the accounts of one legal entity, ordered by code.
func (s *Postgres) ListBankAccounts(ctx context.Context, legalEntityID string) ([]BankAccount, error) {
	p, _ := tenant.FromContext(ctx)
	items := []BankAccount{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+bankAccountColumns+` from bank_account where tenant_id = $1 and legal_entity_id = $2 order by account_code`, p.TenantID, legalEntityID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanBankAccount(rows)
			if err != nil {
				return err
			}
			items = append(items, a)
		}
		return rows.Err()
	})
	return items, err
}

// GetBankAccount returns one account or CodeAccountNotFound.
func (s *Postgres) GetBankAccount(ctx context.Context, id string) (BankAccount, error) {
	p, _ := tenant.FromContext(ctx)
	var out BankAccount
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanBankAccount(tx.QueryRow(ctx, `select `+bankAccountColumns+` from bank_account where tenant_id = $1 and id = $2`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(CodeAccountNotFound, "bank account not found")
		}
		return err
	})
	return out, err
}

// TransitionBankAccount moves an account to status `to` under an optimistic
// version check (FR-36.1 lifecycle Draft -> Active -> Inactive -> Closed).
func (s *Postgres) TransitionBankAccount(ctx context.Context, id string, version int, to domain.AccountStatus) (BankAccount, error) {
	p, _ := tenant.FromContext(ctx)
	var out BankAccount
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var status string
		var current int
		err := tx.QueryRow(ctx, `select status, version from bank_account where tenant_id = $1 and id = $2 for update`, p.TenantID, id).Scan(&status, &current)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(CodeAccountNotFound, "bank account not found")
		}
		if err != nil {
			return err
		}
		if current != version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "the bank account changed; reload and retry")
		}
		if !domain.CanTransition(domain.AccountStatus(status), to) {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeAccountState, "a "+status+" bank account cannot become "+string(to))
		}
		out, err = scanBankAccount(tx.QueryRow(ctx, `update bank_account set status = $4, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 and version = $3 returning `+bankAccountColumns, p.TenantID, id, version, string(to)))
		return err
	})
	return out, err
}

const statementColumns = "id, bank_account_id, legal_entity_id, statement_id, sequence, format, currency, opening_date::text, opening_amount::text, closing_date::text, closing_amount::text, line_count, created_at"

func scanStatement(row pgx.Row) (Statement, error) {
	var st Statement
	err := row.Scan(&st.ID, &st.BankAccountID, &st.LegalEntityID, &st.StatementID, &st.Sequence, &st.Format, &st.Currency, &st.OpeningDate, &st.OpeningBalance, &st.ClosingDate, &st.ClosingBalance, &st.LineCount, &st.CreatedAt)
	return st, err
}

// ImportStatement stores a parsed statement and its lines in one transaction
// and enqueues statement.imported. An identical file (same hash) or the same
// statement id with the same closing balance is replayed (TRS-0405); the same
// statement id with another closing balance is a conflict (TRS-0406). A
// concurrent import of the same file loses the unique-key race and retries once,
// so it replays the winner's statement.
func (s *Postgres) ImportStatement(ctx context.Context, in StatementImport) (ImportResult, error) {
	res, err := s.importStatement(ctx, in)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return s.importStatement(ctx, in)
	}
	return res, err
}

func (s *Postgres) importStatement(ctx context.Context, in StatementImport) (ImportResult, error) {
	p, _ := tenant.FromContext(ctx)
	var res ImportResult
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var accountStatus, accountCurrency, legalEntityID string
		var accountHash []byte
		err := tx.QueryRow(ctx, `select status, currency, legal_entity_id, iban_hash from bank_account where tenant_id = $1 and id = $2`, p.TenantID, in.BankAccountID).
			Scan(&accountStatus, &accountCurrency, &legalEntityID, &accountHash)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(CodeAccountNotFound, "bank account not found")
		}
		if err != nil {
			return err
		}
		if string(accountHash) != string(ibanHash(in.Parsed.AccountIBAN)) {
			return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeStatementAccount, "the statement belongs to another account")
		}
		// BR-36.8: a re-upload is answered with the stored statement before any
		// rule that may have changed since (for example the account being deactivated).
		if existing, err := scanStatement(tx.QueryRow(ctx, `select `+statementColumns+` from bank_statement where tenant_id = $1 and bank_account_id = $2 and file_hash = $3`, p.TenantID, in.BankAccountID, in.FileHash)); err == nil {
			res = ImportResult{Statement: existing, Replayed: true}
			return nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if existing, err := scanStatement(tx.QueryRow(ctx, `select `+statementColumns+` from bank_statement where tenant_id = $1 and bank_account_id = $2 and statement_id = $3`, p.TenantID, in.BankAccountID, in.Parsed.StatementID)); err == nil {
			if err := domain.Duplicate(decimal.RequireFromString(existing.ClosingBalance), in.Parsed.ClosingAmount); err != nil {
				return err
			}
			res = ImportResult{Statement: existing, Replayed: true}
			return nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if accountStatus != string(domain.AccountActive) {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeAccountInactive, "statements can only be imported into an active bank account")
		}
		if in.Parsed.Currency != accountCurrency {
			return &domain.Error{Code: domain.CodeStatementCurrency, Detail: "statement currency " + in.Parsed.Currency + " differs from account currency " + accountCurrency}
		}
		if err := domain.CheckBalances(in.Parsed); err != nil {
			return err
		}
		var warnings []Warning
		if in.Parsed.Sequence > 0 {
			var previous *int
			if err := tx.QueryRow(ctx, `select max(sequence) from bank_statement where tenant_id = $1 and bank_account_id = $2 and sequence < $3`,
				p.TenantID, in.BankAccountID, in.Parsed.Sequence).Scan(&previous); err != nil {
				return err
			}
			if w := domain.SequenceGap(previous, in.Parsed.Sequence); w != nil {
				warnings = append(warnings, Warning{Code: w.Code, Detail: w.Detail})
			}
		}
		st, err := scanStatement(tx.QueryRow(ctx, `insert into bank_statement (tenant_id, id, legal_entity_id, bank_account_id, statement_id, sequence, format, file_hash, currency,
			opening_date, opening_amount, closing_date, closing_amount, line_count)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) returning `+statementColumns,
			p.TenantID, ids.New(), legalEntityID, in.BankAccountID, in.Parsed.StatementID, optionalInt(in.Parsed.Sequence), in.Parsed.Format, in.FileHash, in.Parsed.Currency,
			in.Parsed.OpeningDate, in.Parsed.OpeningAmount.String(), in.Parsed.ClosingDate, in.Parsed.ClosingAmount.String(), len(in.Parsed.Lines)))
		if err != nil {
			return err
		}
		batch := &pgx.Batch{}
		for _, l := range in.Parsed.Lines {
			batch.Queue(`insert into bank_statement_line (tenant_id, statement_id, legal_entity_id, bank_account_id, line_no, booking_date, value_date, amount, currency,
				btc_code, end_to_end_ref, reference, counterparty_name, raw_fragment) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
				p.TenantID, st.ID, legalEntityID, in.BankAccountID, l.LineNo, l.BookingDate, l.ValueDate, l.Amount.String(), l.Currency,
				l.BTCCode, l.EndToEndRef, l.Reference, l.CounterpartyName, l.RawFragment)
		}
		br := tx.SendBatch(ctx, batch)
		for range in.Parsed.Lines {
			if _, err := br.Exec(); err != nil {
				_ = br.Close()
				return err
			}
		}
		if err := br.Close(); err != nil {
			return err
		}
		evt, err := events.New(tevents.StatementImported, eventSource, st.ID, p.TenantID, tevents.StatementImportedData{
			StatementID: st.ID, BankStatementRef: st.StatementID, BankAccountID: in.BankAccountID, LegalEntityID: legalEntityID,
			LineCount: len(in.Parsed.Lines), OpeningBalance: st.OpeningBalance, ClosingBalance: st.ClosingBalance,
		})
		if err != nil {
			return err
		}
		if err := outbox.Enqueue(ctx, tx, "statement", st.ID, evt); err != nil {
			return err
		}
		res = ImportResult{Statement: st, Warnings: warnings}
		return nil
	})
	return res, err
}

func optionalInt(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

// GetStatement returns the header of one statement.
func (s *Postgres) GetStatement(ctx context.Context, id string) (Statement, error) {
	p, _ := tenant.FromContext(ctx)
	var out Statement
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanStatement(tx.QueryRow(ctx, `select `+statementColumns+` from bank_statement where tenant_id = $1 and id = $2`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(CodeStatementNotFound, "statement not found")
		}
		return err
	})
	return out, err
}

// ListStatements returns the most recent statements of one bank account.
func (s *Postgres) ListStatements(ctx context.Context, bankAccountID string) ([]Statement, error) {
	p, _ := tenant.FromContext(ctx)
	items := []Statement{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+statementColumns+` from bank_statement where tenant_id = $1 and bank_account_id = $2 order by closing_date desc, statement_id limit $3`,
			p.TenantID, bankAccountID, MaxStatementsListed)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			st, err := scanStatement(rows)
			if err != nil {
				return err
			}
			items = append(items, st)
		}
		return rows.Err()
	})
	return items, err
}

// ListStatementLines returns lines after afterLineNo, at most limit (capped at MaxLinesPage).
func (s *Postgres) ListStatementLines(ctx context.Context, statementID string, afterLineNo, limit int) ([]StatementLine, error) {
	p, _ := tenant.FromContext(ctx)
	if limit <= 0 || limit > MaxLinesPage {
		limit = MaxLinesPage
	}
	items := []StatementLine{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select line_no, booking_date::text, value_date::text, amount::text, currency, btc_code, end_to_end_ref, reference, counterparty_name, status
			from bank_statement_line where tenant_id = $1 and statement_id = $2 and line_no > $3 order by line_no limit $4`, p.TenantID, statementID, afterLineNo, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l StatementLine
			if err := rows.Scan(&l.LineNo, &l.BookingDate, &l.ValueDate, &l.Amount, &l.Currency, &l.BTCCode, &l.EndToEndRef, &l.Reference, &l.CounterpartyName, &l.Status); err != nil {
				return err
			}
			items = append(items, l)
		}
		return rows.Err()
	})
	return items, err
}

const forecastColumns = "id, legal_entity_id, source_type, source_id, expected_date::text, amount::text, currency, confidence::text"

func scanForecast(row pgx.Row) (ForecastEntry, error) {
	var e ForecastEntry
	err := row.Scan(&e.ID, &e.LegalEntityID, &e.SourceType, &e.SourceID, &e.ExpectedDate, &e.Amount, &e.Currency, &e.Confidence)
	return e, err
}

// CreateForecastEntry stores a manual or source-fed expected cash flow. A
// repeated Idempotency-Key returns the original entry.
func (s *Postgres) CreateForecastEntry(ctx context.Context, in ForecastEntry, idempotencyKey string) (ForecastEntry, error) {
	p, _ := tenant.FromContext(ctx)
	reqHash, err := requestHash("create-forecast-entry", in)
	if err != nil {
		return ForecastEntry{}, err
	}
	var out ForecastEntry
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = idempotent(ctx, tx, idempotencyKey, reqHash, func() (ForecastEntry, error) {
			if in.SourceID == "" {
				in.SourceID = ids.New()
			}
			return scanForecast(tx.QueryRow(ctx, `insert into cash_forecast_entry (tenant_id, id, legal_entity_id, source_type, source_id, expected_date, amount, currency, confidence)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9) returning `+forecastColumns,
				p.TenantID, ids.New(), in.LegalEntityID, in.SourceType, in.SourceID, in.ExpectedDate, in.Amount, in.Currency, in.Confidence))
		})
		return err
	})
	return out, err
}

// ListForecastEntries returns entries of a legal entity dated from..to (inclusive), optionally for one currency.
func (s *Postgres) ListForecastEntries(ctx context.Context, legalEntityID, currency, from, to string) ([]ForecastEntry, error) {
	p, _ := tenant.FromContext(ctx)
	items := []ForecastEntry{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+forecastColumns+` from cash_forecast_entry where tenant_id = $1 and legal_entity_id = $2
			and ($3::text = '' or currency = $3) and expected_date between $4::date and $5::date order by expected_date, id`,
			p.TenantID, legalEntityID, currency, from, to)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e, err := scanForecast(rows)
			if err != nil {
				return err
			}
			items = append(items, e)
		}
		return rows.Err()
	})
	return items, err
}
