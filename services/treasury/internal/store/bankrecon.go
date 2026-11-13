package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
)

const (
	CodeReconExists          = "TRS-0504"
	CodeReconNotFound        = "TRS-0505"
	CodeReconClosed          = "TRS-0506"
	CodeReconOpenLines       = "TRS-0507"
	CodeReopenLaterClosed    = "TRS-0508"
	CodeReopenFourEyes       = "TRS-0509"
	CodeReconNotClosed       = "TRS-0510"
	CodeLineNotFound         = "TRS-0511"
	CodeLineKind             = "TRS-0512"
	CodeLinePosting          = "TRS-0516"
	CodePostingPending       = "TRS-0517"
	CodeBalanceStater        = "TRS-0520"
	CodeMatchFromTransaction = "TRS-0521"
	CodeLineConflict         = "TRS-0522"
	CodeNothingToAbandon     = "TRS-0523"
	CodeAbandonOwn           = "TRS-0524"

	StateOpen     = "open"
	StateClosed   = "closed"
	StateReopened = "reopened"

	ClosedSQLState  = "TRC01"
	PendingSQLState = "TRC02"

	MaxReconciliationsListed = 100
)

type LineSummary struct {
	Lines          int    `json:"lines"`
	MatchedCount   int    `json:"matchedCount"`
	MatchedTotal   string `json:"matchedTotal"`
	ExcludedCount  int    `json:"excludedCount"`
	ExcludedTotal  string `json:"excludedTotal"`
	UnmatchedCount int    `json:"unmatchedCount"`
	UnmatchedTotal string `json:"unmatchedTotal"`
}

type Reconciliation struct {
	ID                  string      `json:"id"`
	LegalEntityID       string      `json:"legalEntityId"`
	BankAccountID       string      `json:"bankAccountId"`
	StatementID         string      `json:"statementId"`
	Currency            string      `json:"currency"`
	PeriodFrom          string      `json:"periodFrom"`
	PeriodTo            string      `json:"periodTo"`
	State               string      `json:"state"`
	OpeningBalance      string      `json:"openingBalance"`
	ClosingBalance      string      `json:"closingBalance"`
	BookBalance         string      `json:"bookBalance"`
	OutstandingAmount   string      `json:"outstandingAmount"`
	BookBalanceSource   string      `json:"bookBalanceSource"`
	BookBalanceStatedBy string      `json:"bookBalanceStatedBy"`
	BookBalanceStatedAt time.Time   `json:"bookBalanceStatedAt"`
	Difference          string      `json:"difference"`
	Summary             LineSummary `json:"summary"`
	ClosedBy            *string     `json:"closedBy,omitempty"`
	ClosedAt            *time.Time  `json:"closedAt,omitempty"`
	ReopenedBy          *string     `json:"reopenedBy,omitempty"`
	ReopenedAt          *time.Time  `json:"reopenedAt,omitempty"`
	ReopenReason        *string     `json:"reopenReason,omitempty"`
	Version             int         `json:"version"`
	CreatedBy           string      `json:"createdBy"`
	CreatedAt           time.Time   `json:"createdAt"`
	UpdatedAt           time.Time   `json:"updatedAt"`

	closing, book, outstanding, excluded decimal.Decimal

	Replayed bool `json:"-"`
}

type NewReconciliation struct {
	StatementID       string
	BookBalance       decimal.Decimal
	OutstandingAmount decimal.Decimal
	// FromOpening starts the account's first reconciliation from the opening book balance the migration factory loaded
	// (#49) instead of BookBalance.
	FromOpening bool
}

// Problems of a reconciliation that starts from the migrated opening balance.
const (
	CodeReconNoOpening    = "TRS-0606" // the account has no migrated opening balance
	CodeReconNotFirstOpen = "TRS-0607" // only the account's first reconciliation, of a statement closing on the cutover date, starts from it
)

type ReconciliationQuery struct {
	LegalEntityID string
	BankAccountID string
	State         string
	PeriodFrom    string
	PeriodTo      string
	Limit         int
	Cursor        string
}

type ReconciliationPage struct {
	Items      []Reconciliation `json:"items"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

const reconSelect = `select r.id, r.legal_entity_id, r.bank_account_id, r.statement_id, r.currency, r.period_from::text, r.period_to::text, r.state,
	r.opening_balance::text, r.closing_balance::text, r.book_balance::text, r.outstanding_amount::text, r.book_balance_stated_by, r.book_balance_stated_at,
	r.book_balance_source, r.closed_by, r.closed_at, r.reopened_by, r.reopened_at, r.reopen_reason, r.version, r.created_by, r.created_at, r.updated_at,
	s.lines, s.matched_count, s.matched_total::text, s.excluded_count, s.excluded_total::text, s.unmatched_count, s.unmatched_total::text
	from bank_reconciliation r
	cross join lateral (select count(*)::int as lines,
		(count(*) filter (where l.status in ('matched', 'created')))::int as matched_count,
		coalesce(sum(l.amount) filter (where l.status in ('matched', 'created')), 0) as matched_total,
		(count(*) filter (where l.status = 'excluded'))::int as excluded_count,
		coalesce(sum(l.amount) filter (where l.status = 'excluded'), 0) as excluded_total,
		(count(*) filter (where l.status not in ('matched', 'created', 'excluded')))::int as unmatched_count,
		coalesce(sum(l.amount) filter (where l.status not in ('matched', 'created', 'excluded')), 0) as unmatched_total
		from bank_statement_line l where l.tenant_id = r.tenant_id and l.statement_id = r.statement_id) s `

func scanReconciliation(row pgx.Row) (Reconciliation, error) {
	var r Reconciliation
	var opening, closing, book, outstanding string
	if err := row.Scan(&r.ID, &r.LegalEntityID, &r.BankAccountID, &r.StatementID, &r.Currency, &r.PeriodFrom, &r.PeriodTo, &r.State,
		&opening, &closing, &book, &outstanding, &r.BookBalanceStatedBy, &r.BookBalanceStatedAt,
		&r.BookBalanceSource, &r.ClosedBy, &r.ClosedAt, &r.ReopenedBy, &r.ReopenedAt, &r.ReopenReason, &r.Version, &r.CreatedBy, &r.CreatedAt, &r.UpdatedAt,
		&r.Summary.Lines, &r.Summary.MatchedCount, &r.Summary.MatchedTotal, &r.Summary.ExcludedCount, &r.Summary.ExcludedTotal,
		&r.Summary.UnmatchedCount, &r.Summary.UnmatchedTotal); err != nil {
		return r, err
	}
	var err error
	for _, p := range []struct {
		text string
		into *decimal.Decimal
	}{{closing, &r.closing}, {book, &r.book}, {outstanding, &r.outstanding}, {r.Summary.ExcludedTotal, &r.excluded}} {
		if *p.into, err = decimal.NewFromString(p.text); err != nil {
			return r, err
		}
	}
	places := int32(money.MinorUnits(r.Currency))
	format := func(text string) string { return decimal.RequireFromString(text).StringFixed(places) }
	r.OpeningBalance, r.ClosingBalance, r.BookBalance, r.OutstandingAmount = format(opening), format(closing), format(book), format(outstanding)
	r.Summary.MatchedTotal, r.Summary.ExcludedTotal, r.Summary.UnmatchedTotal = format(r.Summary.MatchedTotal), format(r.Summary.ExcludedTotal), format(r.Summary.UnmatchedTotal)
	r.Difference = domain.Difference(r.closing, r.book, r.outstanding.Add(r.excluded)).StringFixed(places)
	return r, nil
}

func closedProblem() error {
	return httpx.NewProblem(http.StatusConflict, CodeReconClosed, "the reconciliation of this statement is closed; reopen it before changing its lines or matches")
}

func postingProblem() error {
	return httpx.NewProblem(http.StatusConflict, CodeLinePosting, "a bank transaction is being created from this statement line; repeat that request to finish it")
}

func AsGuardError(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case ClosedSQLState:
			return closedProblem()
		case PendingSQLState:
			return postingProblem()
		}
	}
	return err
}

func scaleProblem(field string, places int32) error {
	p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
	p.Errors = map[string]string{field: "at most " + strconv.Itoa(int(places)) + " decimal places for this currency"}
	return p
}

const validationDetail = "request failed validation"

func recordReconEvent(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, reconciliationID, event, actor string, reason *string, detail map[string]string) error {
	var detailJSON []byte
	if detail != nil {
		var err error
		if detailJSON, err = json.Marshal(detail); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `insert into bank_reconciliation_event (tenant_id, id, legal_entity_id, reconciliation_id, event, actor, reason, detail)
		values ($1, $2, $3, $4, $5, $6, $7, $8)`, tenantID, ids.New(), legalEntityID, reconciliationID, event, actor, reason, detailJSON)
	return err
}

func loadReconciliation(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (Reconciliation, error) {
	if lock {
		var locked string
		err := tx.QueryRow(ctx, `select id::text from bank_reconciliation where tenant_id = $1 and id = $2 for update`, tenantID, id).Scan(&locked)
		if errors.Is(err, pgx.ErrNoRows) {
			return Reconciliation{}, notFound(CodeReconNotFound, "reconciliation not found")
		}
		if err != nil {
			return Reconciliation{}, err
		}
	}
	r, err := scanReconciliation(tx.QueryRow(ctx, reconSelect+`where r.tenant_id = $1 and r.id = $2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, notFound(CodeReconNotFound, "reconciliation not found")
	}
	return r, err
}

func lockAccountPeriods(ctx context.Context, tx pgx.Tx, tenantID, id string) error {
	var account string
	err := tx.QueryRow(ctx, `select bank_account_id::text from bank_reconciliation where tenant_id = $1 and id = $2`, tenantID, id).Scan(&account)
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound(CodeReconNotFound, "reconciliation not found")
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, "recon-periods:"+tenantID+":"+account)
	return err
}

func (s *Postgres) CreateReconciliation(ctx context.Context, in NewReconciliation, key string) (Reconciliation, error) {
	p, _ := tenant.FromContext(ctx)
	hash, err := requestHash("create-reconciliation", in)
	if err != nil {
		return Reconciliation{}, err
	}
	var out Reconciliation
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = idempotent(ctx, tx, key, hash, func() (Reconciliation, error) {
			var currency, legalEntityID string
			err := tx.QueryRow(ctx, `select currency, legal_entity_id::text from bank_statement where tenant_id = $1 and id = $2`, p.TenantID, in.StatementID).Scan(&currency, &legalEntityID)
			if errors.Is(err, pgx.ErrNoRows) {
				return Reconciliation{}, notFound(CodeStatementNotFound, "statement not found")
			}
			if err != nil {
				return Reconciliation{}, err
			}
			source := "stated"
			if in.FromOpening {
				book, err := openingBook(ctx, tx, p.TenantID, in.StatementID, currency)
				if err != nil {
					return Reconciliation{}, err
				}
				in.BookBalance, source = book, "opening"
			}
			if err := checkScale(currency, map[string]decimal.Decimal{"bookBalance": in.BookBalance, "outstandingAmount": in.OutstandingAmount}); err != nil {
				return Reconciliation{}, err
			}
			id := ids.New()
			_, err = tx.Exec(ctx, `insert into bank_reconciliation (tenant_id, id, legal_entity_id, bank_account_id, statement_id, currency, period_from, period_to,
				opening_balance, closing_balance, book_balance, outstanding_amount, book_balance_stated_by, created_by, book_balance_source)
				select tenant_id, $3, legal_entity_id, bank_account_id, id, currency, opening_date, closing_date, opening_amount, closing_amount, $4, $5, $6, $6, $7
				from bank_statement where tenant_id = $1 and id = $2`, p.TenantID, in.StatementID, id, in.BookBalance.String(), in.OutstandingAmount.String(), createdBy(p), source)
			var pg *pgconn.PgError
			if errors.As(err, &pg) && pg.Code == uniqueViolation {
				return Reconciliation{}, httpx.NewProblem(http.StatusConflict, CodeReconExists, "this statement already has a reconciliation")
			}
			if err != nil {
				return Reconciliation{}, err
			}
			if err := recordReconEvent(ctx, tx, p.TenantID, legalEntityID, id, "created", createdBy(p), nil,
				map[string]string{"bookBalance": in.BookBalance.String(), "outstandingAmount": in.OutstandingAmount.String(), "bookBalanceSource": source}); err != nil {
				return Reconciliation{}, err
			}
			return loadReconciliation(ctx, tx, p.TenantID, id, false)
		})
		return err
	})
	return out, err
}

// openingBook is the migrated opening book balance of the statement's account, for the account's first reconciliation of a
// statement that closes on the cutover date.
func openingBook(ctx context.Context, tx pgx.Tx, tenantID, statementID, currency string) (decimal.Decimal, error) {
	var account, closing string
	var asOfPtr *string
	var minorPtr *int64
	err := tx.QueryRow(ctx, `select s.bank_account_id::text, s.closing_date::text, o.as_of::text, o.balance_minor
		from bank_statement s left join bank_opening_balance o on o.tenant_id = s.tenant_id and o.bank_account_id = s.bank_account_id
		where s.tenant_id = $1 and s.id = $2`, tenantID, statementID).Scan(&account, &closing, &asOfPtr, &minorPtr)
	if err != nil {
		return decimal.Zero, err
	}
	if asOfPtr == nil || minorPtr == nil {
		return decimal.Zero, httpx.NewProblem(http.StatusUnprocessableEntity, CodeReconNoOpening, "the account has no opening balance from the migration; state the book balance")
	}
	asOf, minor := *asOfPtr, *minorPtr
	// The opening balance is the book balance at the end of the cutover day only: a statement closing later would need the book
	// movements posted since the cutover, which the opening balance does not hold.
	if closing != asOf {
		return decimal.Zero, httpx.NewProblem(http.StatusUnprocessableEntity, CodeReconNotFirstOpen, "the statement closes on "+closing+", not on the cutover date "+asOf+", so the opening balance is not the book balance on its closing date; state the book balance")
	}
	var earlier int
	if err := tx.QueryRow(ctx, `select count(*) from bank_reconciliation where tenant_id = $1 and bank_account_id = $2`, tenantID, account).Scan(&earlier); err != nil {
		return decimal.Zero, err
	}
	if earlier > 0 {
		return decimal.Zero, httpx.NewProblem(http.StatusUnprocessableEntity, CodeReconNotFirstOpen, "the account was reconciled before; only its first reconciliation starts from the opening balance, so state the book balance")
	}
	return decimal.New(minor, -int32(money.MinorUnits(currency))), nil
}

func checkScale(currency string, amounts map[string]decimal.Decimal) error {
	places := int32(money.MinorUnits(currency))
	for field, amount := range amounts {
		if !amount.Equal(amount.Round(places)) {
			return scaleProblem(field, places)
		}
	}
	return nil
}

func (s *Postgres) GetReconciliation(ctx context.Context, id string) (Reconciliation, error) {
	p, _ := tenant.FromContext(ctx)
	var out Reconciliation
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadReconciliation(ctx, tx, p.TenantID, id, false)
		return err
	})
	return out, err
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func encodeCursor(periodTo, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(periodTo + "|" + id))
}

func decodeCursor(cursor string) (periodTo, id string, ok bool) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", "", false
	}
	periodTo, id, ok = strings.Cut(string(raw), "|")
	if !ok || !ids.Valid(id) {
		return "", "", false
	}
	if _, err := time.Parse(time.DateOnly, periodTo); err != nil {
		return "", "", false
	}
	return periodTo, id, true
}

func (s *Postgres) ListReconciliations(ctx context.Context, q ReconciliationQuery) (ReconciliationPage, error) {
	p, _ := tenant.FromContext(ctx)
	if q.Limit <= 0 || q.Limit > MaxReconciliationsListed {
		q.Limit = MaxReconciliationsListed
	}
	var cursorTo, cursorID *string
	if q.Cursor != "" {
		to, id, ok := decodeCursor(q.Cursor)
		if !ok {
			p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
			p.Errors = map[string]string{"cursor": "not a cursor returned by this list"}
			return ReconciliationPage{}, p
		}
		cursorTo, cursorID = &to, &id
	}
	page := ReconciliationPage{Items: []Reconciliation{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, reconSelect+`where r.tenant_id = $1
			and ($2::uuid is null or r.legal_entity_id = $2) and ($3::uuid is null or r.bank_account_id = $3) and ($4::text is null or r.state = $4)
			and ($5::date is null or r.period_to >= $5) and ($6::date is null or r.period_from <= $6)
			and ($7::date is null or (r.period_to, r.id) < ($7, $8::uuid))
			order by r.period_to desc, r.id desc limit $9`,
			p.TenantID, nilIfEmpty(q.LegalEntityID), nilIfEmpty(q.BankAccountID), nilIfEmpty(q.State), nilIfEmpty(q.PeriodFrom), nilIfEmpty(q.PeriodTo),
			cursorTo, cursorID, q.Limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanReconciliation(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, r)
		}
		return rows.Err()
	})
	if len(page.Items) > q.Limit {
		page.Items = page.Items[:q.Limit]
		last := page.Items[q.Limit-1]
		page.NextCursor = encodeCursor(last.PeriodTo, last.ID)
	}
	return page, err
}

func (s *Postgres) SetReconciliationBalances(ctx context.Context, id string, version int, book, outstanding decimal.Decimal) (Reconciliation, error) {
	p, _ := tenant.FromContext(ctx)
	var out Reconciliation
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := loadReconciliation(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		if cur.State == StateClosed {
			return closedProblem()
		}
		if cur.Version != version {
			return versionMismatch()
		}
		if err := checkScale(cur.Currency, map[string]decimal.Decimal{"bookBalance": book, "outstandingAmount": outstanding}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update bank_reconciliation set book_balance = $3, outstanding_amount = $4, book_balance_stated_by = $5, book_balance_stated_at = now(),
			book_balance_source = 'stated', version = version + 1, updated_at = now() where tenant_id = $1 and id = $2`, p.TenantID, id, book.String(), outstanding.String(), createdBy(p)); err != nil {
			return err
		}
		if err := recordReconEvent(ctx, tx, p.TenantID, cur.LegalEntityID, id, "balances_set", createdBy(p), nil, map[string]string{
			"oldBookBalance": cur.book.String(), "newBookBalance": book.String(),
			"oldOutstandingAmount": cur.outstanding.String(), "newOutstandingAmount": outstanding.String(), "previouslyStatedBy": cur.BookBalanceStatedBy}); err != nil {
			return err
		}
		out, err = loadReconciliation(ctx, tx, p.TenantID, id, false)
		return err
	})
	return out, err
}

func (s *Postgres) CloseReconciliation(ctx context.Context, id string, version int) (Reconciliation, error) {
	p, _ := tenant.FromContext(ctx)
	var out Reconciliation
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockAccountPeriods(ctx, tx, p.TenantID, id); err != nil {
			return err
		}
		cur, err := loadReconciliation(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		if cur.State == StateClosed {
			cur.Replayed = true
			out = cur
			return nil
		}
		if cur.Version != version {
			return versionMismatch()
		}
		if cur.BookBalanceStatedBy == createdBy(p) {
			return httpx.NewProblem(http.StatusForbidden, CodeBalanceStater, "the person who last stated the book balance cannot close the reconciliation; another person must")
		}
		pendingRows, err := tx.Query(ctx, `select line_no from bank_line_transaction where tenant_id = $1 and statement_id = $2 and status in ('pending', 'conflict') order by line_no`,
			p.TenantID, cur.StatementID)
		if err != nil {
			return err
		}
		var pendingLines []string
		for pendingRows.Next() {
			var n int
			if err := pendingRows.Scan(&n); err != nil {
				pendingRows.Close()
				return err
			}
			pendingLines = append(pendingLines, strconv.Itoa(n))
		}
		pendingRows.Close()
		if err := pendingRows.Err(); err != nil {
			return err
		}
		if len(pendingLines) > 0 {
			problem := httpx.NewProblem(http.StatusConflict, CodePostingPending, "bank transactions from statement lines are still being posted or need a person; repeat their requests or abandon them")
			problem.Errors = map[string]string{"pendingLines": strings.Join(pendingLines, ",")}
			return problem
		}
		if cur.Summary.UnmatchedCount > 0 {
			return unprocessable(CodeReconOpenLines, strconv.Itoa(cur.Summary.UnmatchedCount)+" statement lines are neither matched nor excluded")
		}
		if err := domain.CheckClose(domain.ReconInProgress, cur.closing, cur.book, cur.outstanding.Add(cur.excluded)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update bank_reconciliation set state = 'closed', closed_by = $3, closed_at = now(), version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`, p.TenantID, id, createdBy(p)); err != nil {
			return err
		}
		if err := recordReconEvent(ctx, tx, p.TenantID, cur.LegalEntityID, id, "closed", createdBy(p), nil, nil); err != nil {
			return err
		}
		out, err = loadReconciliation(ctx, tx, p.TenantID, id, false)
		return err
	})
	return out, err
}

func (s *Postgres) ReopenReconciliation(ctx context.Context, id string, version int, reason string) (Reconciliation, error) {
	p, _ := tenant.FromContext(ctx)
	var out Reconciliation
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockAccountPeriods(ctx, tx, p.TenantID, id); err != nil {
			return err
		}
		cur, err := loadReconciliation(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		if cur.State != StateClosed {
			return unprocessable(CodeReconNotClosed, "only a closed reconciliation can be reopened")
		}
		if cur.Version != version {
			return versionMismatch()
		}
		if cur.ClosedBy != nil && *cur.ClosedBy == createdBy(p) {
			return httpx.NewProblem(http.StatusForbidden, CodeReopenFourEyes, "the person who closed the reconciliation cannot reopen it; another person must")
		}
		var later int
		if err := tx.QueryRow(ctx, `select count(*) from bank_reconciliation where tenant_id = $1 and bank_account_id = $2 and state = 'closed' and period_to >= $3::date and id <> $4`,
			p.TenantID, cur.BankAccountID, cur.PeriodTo, id).Scan(&later); err != nil {
			return err
		}
		if later > 0 {
			return httpx.NewProblem(http.StatusConflict, CodeReopenLaterClosed, "a later period of this bank account is closed; reopen the later period first")
		}
		if _, err := tx.Exec(ctx, `update bank_reconciliation set state = 'reopened', reopened_by = $3, reopened_at = now(), reopen_reason = $4, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`, p.TenantID, id, createdBy(p), reason); err != nil {
			return err
		}
		if err := recordReconEvent(ctx, tx, p.TenantID, cur.LegalEntityID, id, "reopened", createdBy(p), &reason, nil); err != nil {
			return err
		}
		out, err = loadReconciliation(ctx, tx, p.TenantID, id, false)
		return err
	})
	return out, err
}
