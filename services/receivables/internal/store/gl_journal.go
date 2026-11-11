package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
)

const (
	CodeJournalNotFound = "AR-JOURNAL-NOT-FOUND"
	CodeJournalState    = "AR-JOURNAL-STATE"
	CodeGLUnavailable   = "AR-GL-UNAVAILABLE"

	glBackoffBase = time.Minute
	glBackoffMax  = time.Hour
	// MaxJournalSweepBatch caps the journals one sweep posts.
	MaxJournalSweepBatch = 200
)

// Journal sources, the source_type of a gl_journal row.
const (
	JournalPaymentReceipt = "payment_receipt"
	JournalPaymentApply   = "payment_apply"
	JournalDunningFee     = "dunning_fee"
	JournalInterest       = "interest"
	JournalDownPayment    = "down_payment"
	JournalDeduction      = "deduction"
)

// journalLine is one stored line of an owed journal.
type journalLine struct {
	Account string          `json:"account"`
	Credit  bool            `json:"credit"`
	Amount  decimal.Decimal `json:"amount"`
	Text    string          `json:"text,omitempty"`
}

// journalSpec is a journal receivables owes the ledger, stored in the transaction of its cause.
type journalSpec struct {
	LegalEntityID, SourceType, SourceID, SourceKey, DocumentID, PostingDate, Currency string
	// BankRef names the bank account the cash came through (the statement's account id) so that its ledger account replaces 1000;
	// empty uses the legal entity's default for the currency.
	BankRef string
	Lines   []journalLine
}

func debit(account string, amount decimal.Decimal, text string) journalLine {
	return journalLine{Account: account, Amount: amount, Text: text}
}

func credit(account string, amount decimal.Decimal, text string) journalLine {
	return journalLine{Account: account, Credit: true, Amount: amount, Text: text}
}

// postingLines turns the domain postings of a down payment or a deduction into journal lines.
func postingLines(in []domain.Posting) []journalLine {
	out := make([]journalLine, 0, len(in))
	for _, p := range in {
		if p.Debit.IsPositive() {
			out = append(out, debit(p.Account, p.Debit, ""))
		}
		if p.Credit.IsPositive() {
			out = append(out, credit(p.Account, p.Credit, ""))
		}
	}
	return out
}

// enqueueJournal stores the journal inside tx. Lines with no amount are dropped; a journal with fewer than two lines is
// not stored. The (legal entity, source key) pair is unique, so a replayed cause never owes a second journal.
func enqueueJournal(ctx context.Context, tx pgx.Tx, tenantID string, j journalSpec) error {
	lines := make([]journalLine, 0, len(j.Lines))
	for _, l := range j.Lines {
		if l.Amount.IsPositive() {
			lines = append(lines, l)
		}
	}
	if len(lines) < 2 {
		return nil
	}
	bank := ""
	for i := range lines {
		if lines[i].Account != domain.AccountBank {
			continue
		}
		if bank == "" {
			var err error
			if bank, err = bankLedgerAccount(ctx, tx, tenantID, j.LegalEntityID, j.Currency, j.BankRef); err != nil {
				return err
			}
		}
		lines[i].Account = bank
	}
	raw, err := json.Marshal(lines)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `insert into gl_journal (tenant_id, id, legal_entity_id, source_type, source_id, source_key, document_id, posting_date, currency, lines)
		values ($1, $2, $3, $4, $5, $6, $7, $8::date, $9, $10)
		on conflict (tenant_id, legal_entity_id, source_key) do nothing`,
		tenantID, ids.New(), j.LegalEntityID, j.SourceType, j.SourceID, j.SourceKey, j.DocumentID, j.PostingDate, j.Currency, raw)
	return err
}

// receiptJournal owes the ledger the cash of a received payment: Dr the bank account (1000 unless the legal entity maps its bank
// accounts, see ar_bank_account) / Cr 1205 unapplied cash. Applying the
// payment moves it from 1205 to 1200 (applyJournal), so 1205 always equals the payments' unapplied amounts.
func receiptJournal(ctx context.Context, tx pgx.Tx, tenantID string, pay Payment) error {
	return enqueueJournal(ctx, tx, tenantID, journalSpec{LegalEntityID: pay.LegalEntityID, SourceType: JournalPaymentReceipt, SourceID: pay.ID,
		SourceKey: "receipt:" + pay.ID, DocumentID: "PAY-" + pay.ID, PostingDate: pay.ValueDate, Currency: pay.Currency, BankRef: deref(pay.BankAccountRef),
		Lines: []journalLine{debit(domain.AccountBank, pay.Amount, "Payment received"), credit(domain.AccountUnappliedCash, pay.Amount, "Unapplied cash")}})
}

// applyJournal owes the ledger one application of a payment: Dr 1205 for the cash applied, Dr 4100 for the discount
// taken and Dr 6100 for the write-off, Cr 1200 for what the settlements took off the items.
func applyJournal(ctx context.Context, tx pgx.Tx, tenantID string, pay Payment, allocs []domain.Allocation) error {
	cash, disc, off := decimal.Zero, decimal.Zero, decimal.Zero
	for _, a := range allocs {
		cash, disc, off = cash.Add(a.Amount), disc.Add(a.CashDiscount), off.Add(a.WriteOff)
	}
	return enqueueJournal(ctx, tx, tenantID, journalSpec{LegalEntityID: pay.LegalEntityID, SourceType: JournalPaymentApply, SourceID: pay.ID,
		SourceKey: fmt.Sprintf("apply:%s:%d", pay.ID, pay.Version), DocumentID: "PAY-" + pay.ID, PostingDate: today(), Currency: pay.Currency,
		Lines: []journalLine{
			debit(domain.AccountUnappliedCash, cash, "Payment applied"), debit(domain.AccountCashDiscount, disc, "Cash discount taken"),
			debit(domain.AccountWriteOff, off, "Write-off"), credit(domain.AccountReceivable, cash.Add(disc).Add(off), "Open items settled")}})
}

// downPaymentJournal owes the ledger the postings a down payment or deposit move carries on its event.
func downPaymentJournal(ctx context.Context, tx pgx.Tx, tenantID string, d DownPayment, step, date string, postings []domain.Posting) error {
	return enqueueJournal(ctx, tx, tenantID, journalSpec{LegalEntityID: d.LegalEntityID, SourceType: JournalDownPayment, SourceID: d.ID,
		SourceKey: "downpayment:" + d.ID + ":" + step, DocumentID: "DP-" + d.ID, PostingDate: date, Currency: d.Currency, Lines: postingLines(postings)})
}

func today() string { return time.Now().UTC().Format(dateLayout) }

// GLJournalView is an owed or posted journal as the operator sees it.
type GLJournalView struct {
	ID               string          `json:"id"`
	LegalEntityID    string          `json:"legalEntityId"`
	SourceType       string          `json:"sourceType"`
	SourceID         string          `json:"sourceId"`
	DocumentID       string          `json:"documentId"`
	PostingDate      string          `json:"postingDate"`
	Currency         string          `json:"currency"`
	Status           string          `json:"status"`
	JournalPostingID *string         `json:"journalPostingId,omitempty"`
	Attempts         int             `json:"attempts"`
	NextAttemptAt    *time.Time      `json:"nextAttemptAt,omitempty"`
	Parked           bool            `json:"parked"`
	LastError        *string         `json:"lastError,omitempty"`
	CreatedAt        time.Time       `json:"createdAt"`
	Debit            decimal.Decimal `json:"debit"`
	Lines            []GLJournalLine `json:"lines"`
}

// GLJournalLine is one line of a GLJournalView.
type GLJournalLine struct {
	Account string          `json:"account"`
	Side    string          `json:"side"`
	Amount  decimal.Decimal `json:"amount"`
	Text    string          `json:"text,omitempty"`
}

// GLJournalFilter narrows the operator list.
type GLJournalFilter struct {
	LegalEntityID, Status, SourceType string
	ParkedOnly                        bool
	Limit                             int
}

// JournalSweepResult counts what one pass did.
type JournalSweepResult struct {
	Posted int `json:"posted"`
	Failed int `json:"failed"`
	Parked int `json:"parked"`
}

const glJournalColumns = `id::text, legal_entity_id::text, source_type, source_id::text, document_id, posting_date::text, currency::text, status,
	journal_posting_id, attempts, next_attempt_at, parked, last_error, created_at, lines`

func scanGLJournal(row rowScanner) (GLJournalView, []journalLine, error) {
	var v GLJournalView
	var raw []byte
	if err := row.Scan(&v.ID, &v.LegalEntityID, &v.SourceType, &v.SourceID, &v.DocumentID, &v.PostingDate, &v.Currency, &v.Status,
		&v.JournalPostingID, &v.Attempts, &v.NextAttemptAt, &v.Parked, &v.LastError, &v.CreatedAt, &raw); err != nil {
		return GLJournalView{}, nil, err
	}
	var lines []journalLine
	if err := json.Unmarshal(raw, &lines); err != nil {
		return GLJournalView{}, nil, err
	}
	v.Lines = make([]GLJournalLine, 0, len(lines))
	v.Debit = decimal.Zero
	for _, l := range lines {
		side := "D"
		if l.Credit {
			side = "C"
		} else {
			v.Debit = v.Debit.Add(l.Amount)
		}
		v.Lines = append(v.Lines, GLJournalLine{Account: l.Account, Side: side, Amount: l.Amount, Text: l.Text})
	}
	return v, lines, nil
}

// WithGLLedgers pins, per legal entity, the ledger the receivables journals post to. An entity with no pin falls back
// to its RMA ledger.
func (s *Postgres) WithGLLedgers(byLegalEntity map[string]string) *Postgres {
	s.glLedgers = byLegalEntity
	return s
}

func (s *Postgres) journalLedger(legalEntityID string) string {
	if id := s.glLedgers[legalEntityID]; id != "" {
		return id
	}
	return s.rmaLedgers[legalEntityID]
}

// ListGLJournals lists journals of the caller's legal-entity scope, newest first.
func (s *Postgres) ListGLJournals(ctx context.Context, f GLJournalFilter) ([]GLJournalView, error) {
	if f.Limit < 1 || f.Limit > MaxPageSize {
		f.Limit = 100
	}
	out := []GLJournalView{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+glJournalColumns+` from gl_journal
			where ($1::text = '' or legal_entity_id::text = $1) and ($2::text = '' or status = $2) and ($3::text = '' or source_type = $3)
			and (not $4 or (parked and status = 'PENDING'))
			order by created_at desc, id limit $5`, f.LegalEntityID, f.Status, f.SourceType, f.ParkedOnly, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, _, err := scanGLJournal(rows)
			if err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}

// GLJournalLegalEntities lists the legal entities with journals due to post, so a tenant-wide worker can run
// PostDueJournals once per legal entity under a principal scoped to it.
func (s *Postgres) GLJournalLegalEntities(ctx context.Context) ([]string, error) {
	p, _ := tenant.FromContext(ctx)
	var out []string
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select distinct legal_entity_id::text from gl_journal where tenant_id = $1 and status = 'PENDING' and not parked`, p.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var le string
			if err := rows.Scan(&le); err != nil {
				return err
			}
			out = append(out, le)
		}
		return rows.Err()
	})
	return out, err
}

// PostDueJournals posts the unparked journals of the caller's legal-entity scope whose backoff has run out, oldest first,
// at most limit. Each is posted on its own; a failure is recorded on the journal (backoff, or park on a ledger refusal)
// and the pass goes on, so a refused journal never starves the ones behind it.
func (s *Postgres) PostDueJournals(ctx context.Context, now time.Time, limit int) (JournalSweepResult, error) {
	p, _ := tenant.FromContext(ctx)
	if limit < 1 || limit > MaxJournalSweepBatch {
		limit = MaxJournalSweepBatch
	}
	var due []GLJournalView
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+glJournalColumns+` from gl_journal
			where tenant_id = $1 and status = 'PENDING' and not parked and (next_attempt_at is null or next_attempt_at <= $2)
			order by created_at, id limit $3`, p.TenantID, now, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, _, err := scanGLJournal(rows)
			if err != nil {
				return err
			}
			due = append(due, v)
		}
		return rows.Err()
	})
	if err != nil {
		return JournalSweepResult{}, err
	}
	var res JournalSweepResult
	for _, v := range due {
		switch err := s.postJournal(ctx, p.TenantID, v, now); {
		case err == nil:
			res.Posted++
		case errors.Is(err, errJournalParked):
			res.Parked++
		default:
			res.Failed++
		}
	}
	return res, nil
}

var errJournalParked = errors.New("journal parked")

// postJournal posts one journal and records the outcome; it returns nil when the journal is POSTED, errJournalParked
// after a ledger refusal and another error otherwise.
func (s *Postgres) postJournal(ctx context.Context, tenantID string, v GLJournalView, now time.Time) error {
	ledgerID := s.journalLedger(v.LegalEntityID)
	if s.ledger == nil || ledgerID == "" {
		s.recordJournalFailure(ctx, tenantID, v, now, false, "the ledger or the legal entity's ledger is not configured")
		return errors.New("ledger not configured")
	}
	_, stored, err := s.loadJournal(ctx, tenantID, v.ID)
	if err != nil {
		return err
	}
	id, err := s.postJournalToLedger(ctx, v, ledgerID, stored)
	if err != nil {
		parked := ledgerclient.Rejected(err)
		slog.ErrorContext(ctx, "receivables journal not posted", "journal", v.ID, "source", v.SourceType, "attempts", v.Attempts+1, "parked", parked, "err", err, "correlationId", middleware.GetReqID(ctx))
		s.recordJournalFailure(ctx, tenantID, v, now, parked, err.Error())
		if parked {
			return errJournalParked
		}
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update gl_journal set status = 'POSTED', journal_posting_id = $3, posted_at = now(), next_attempt_at = null, last_error = null
			where tenant_id = $1 and id = $2 and status = 'PENDING'`, tenantID, v.ID, id)
		return err
	})
}

func (s *Postgres) loadJournal(ctx context.Context, tenantID, id string) (GLJournalView, []journalLine, error) {
	var v GLJournalView
	var lines []journalLine
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		v, lines, err = scanGLJournal(tx.QueryRow(ctx, `select `+glJournalColumns+` from gl_journal where tenant_id = $1 and id = $2`, tenantID, id))
		return err
	})
	return v, lines, err
}

func (s *Postgres) postJournalToLedger(ctx context.Context, v GLJournalView, ledgerID string, lines []journalLine) (string, error) {
	owner, err := s.ledger.LegalEntity(ctx, v.LegalEntityID, ledgerID)
	if err != nil {
		return "", err
	}
	if owner != v.LegalEntityID {
		return "", &ledgerclient.Error{Op: "ledger", Status: http.StatusUnprocessableEntity, Code: "ledger_legal_entity_mismatch"}
	}
	out := make([]ledgerclient.Line, 0, len(lines))
	for _, l := range lines {
		out = append(out, ledgerclient.Line{Account: l.Account, Credit: l.Credit, Amount: l.Amount, Text: l.Text})
	}
	return s.ledger.Post(ctx, ledgerclient.Entry{LegalEntityID: v.LegalEntityID, LedgerID: ledgerID, DocumentID: v.DocumentID,
		PostingDate: v.PostingDate, Currency: v.Currency, IdempotencyKey: fmt.Sprintf("gl-journal:%s:%s", v.LegalEntityID, v.ID),
		SourceType: "receivables-" + v.SourceType, Lines: out})
}

func (s *Postgres) recordJournalFailure(ctx context.Context, tenantID string, v GLJournalView, now time.Time, parked bool, msg string) {
	next := now.Add(min(glBackoffBase<<min(v.Attempts, 10), glBackoffMax))
	if len(msg) > 500 {
		msg = msg[:500]
	}
	detached := context.WithoutCancel(ctx)
	if err := dbx.WithTenantTx(detached, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(detached, `update gl_journal set attempts = attempts + 1, next_attempt_at = $3, parked = $4, last_error = $5
			where tenant_id = $1 and id = $2 and status = 'PENDING'`, tenantID, v.ID, next, parked, msg)
		return err
	}); err != nil {
		slog.ErrorContext(ctx, "receivables journal failure not recorded", "journal", v.ID, "err", err, "correlationId", middleware.GetReqID(ctx))
	}
}

// RetryGLJournal is the operator path for a journal that is not POSTED: it clears the park and the backoff, optionally
// re-dates it (a ledger refusal such as a closed period), and posts it now.
func (s *Postgres) RetryGLJournal(ctx context.Context, id, postingDate string) (GLJournalView, error) {
	p, _ := tenant.FromContext(ctx)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `select status from gl_journal where tenant_id = $1 and id = $2 for update`, p.TenantID, id).Scan(&status); err != nil {
			return notFound(err, CodeJournalNotFound, "journal not found")
		}
		if status != "PENDING" {
			return httpx.NewProblem(http.StatusConflict, CodeJournalState, "the journal is already posted")
		}
		_, err := tx.Exec(ctx, `update gl_journal set parked = false, next_attempt_at = null, posting_date = coalesce(nullif($3, '')::date, posting_date)
			where tenant_id = $1 and id = $2`, p.TenantID, id, postingDate)
		return err
	})
	if err != nil {
		return GLJournalView{}, mapWriteErr(err)
	}
	v, _, err := s.loadJournal(ctx, p.TenantID, id)
	if err != nil {
		return GLJournalView{}, err
	}
	if perr := s.postJournal(ctx, p.TenantID, v, time.Now().UTC()); perr != nil && !errors.Is(perr, errJournalParked) {
		return GLJournalView{}, httpx.NewProblem(http.StatusServiceUnavailable, CodeGLUnavailable, "the journal could not be posted; retry later")
	}
	v, _, err = s.loadJournal(ctx, p.TenantID, id)
	return v, err
}

// GLReconciliation compares the receivables sub-ledger with the ledger's control accounts (30-2): the open items against
// account 1200 and the unapplied payments against 1205.
type GLReconciliation struct {
	LegalEntityID        string          `json:"legalEntityId"`
	AsOf                 string          `json:"asOf"`
	Currency             string          `json:"currency"`
	OpenItemsNet         decimal.Decimal `json:"openItemsNet"`
	LedgerReceivable     decimal.Decimal `json:"ledgerReceivable"`
	ReceivableDifference decimal.Decimal `json:"receivableDifference"`
	UnappliedPayments    decimal.Decimal `json:"unappliedPayments"`
	LedgerUnapplied      decimal.Decimal `json:"ledgerUnapplied"`
	UnappliedDifference  decimal.Decimal `json:"unappliedDifference"`
	PendingJournals      int             `json:"pendingJournals"`
	ParkedJournals       int             `json:"parkedJournals"`
	Reconciled           bool            `json:"reconciled"`
}

// GLReconcile reads the ledger's 1200 and 1205 balances and compares them with the open items and unapplied payments of
// one legal entity and currency. Items loaded by the migration import carry no journal here (the ledger opening balance
// is the ledger pack's own migration), so an entity with imported items shows their open amount in the difference until
// that balance is loaded. reconciled needs both differences at zero and no journal waiting or parked.
func (s *Postgres) GLReconcile(ctx context.Context, legalEntityID, currency, asOf string) (GLReconciliation, error) {
	ledgerID := s.journalLedger(legalEntityID)
	if s.ledger == nil || ledgerID == "" {
		return GLReconciliation{}, httpx.NewProblem(http.StatusServiceUnavailable, CodeGLUnavailable, "the ledger or the legal entity's ledger is not configured")
	}
	out := GLReconciliation{LegalEntityID: legalEntityID, AsOf: asOf, Currency: currency}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var open, unapplied string
		if err := tx.QueryRow(ctx, `select coalesce(sum(open_amount), 0)::text from open_item
			where legal_entity_id = $1 and currency = $2 and status in ('OPEN','PARTIALLY_SETTLED')`, legalEntityID, currency).Scan(&open); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `select coalesce(sum(unapplied_amount), 0)::text from payment
			where legal_entity_id = $1 and currency = $2 and status in ('RECEIVED','SUGGESTED','ON_ACCOUNT')`, legalEntityID, currency).Scan(&unapplied); err != nil {
			return err
		}
		out.OpenItemsNet, out.UnappliedPayments = decimal.RequireFromString(open), decimal.RequireFromString(unapplied)
		return tx.QueryRow(ctx, `select count(*) filter (where not parked), count(*) filter (where parked) from gl_journal
			where legal_entity_id = $1 and currency = $2 and status = 'PENDING'`, legalEntityID, currency).Scan(&out.PendingJournals, &out.ParkedJournals)
	})
	if err != nil {
		return GLReconciliation{}, err
	}
	for _, acct := range []string{domain.AccountReceivable, domain.AccountUnappliedCash} {
		bal, err := s.ledger.Balance(ctx, ledgerclient.BalanceQuery{LegalEntityID: legalEntityID, LedgerID: ledgerID, Account: acct, PeriodTo: asOf})
		if err != nil {
			return GLReconciliation{}, httpx.NewProblem(http.StatusServiceUnavailable, CodeGLUnavailable, "the ledger balance could not be read; retry later")
		}
		if bal.Currency != "" && bal.Currency != currency {
			return GLReconciliation{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeGLUnavailable, "the ledger is kept in "+bal.Currency+", not "+currency)
		}
		if acct == domain.AccountReceivable {
			out.LedgerReceivable = bal.Net()
		} else {
			out.LedgerUnapplied = bal.Credit.Sub(bal.Debit)
		}
	}
	out.ReceivableDifference = out.OpenItemsNet.Sub(out.LedgerReceivable)
	out.UnappliedDifference = out.UnappliedPayments.Sub(out.LedgerUnapplied)
	out.Reconciled = out.ReceivableDifference.IsZero() && out.UnappliedDifference.IsZero() && out.PendingJournals == 0 && out.ParkedJournals == 0
	return out, nil
}
