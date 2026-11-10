package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	rcvevents "github.com/BenjaminSRussell/passage/services/receivables/internal/events"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/taxclient"
)

const (
	CodeRMACreditInvalid  = "AR-RMA-CREDIT"
	CodeRMAInvoice        = "AR-RMA-INVOICE"
	CodeRMAUnavailable    = "AR-RMA-GL"
	CodeRMAParked         = "AR-RMA-PARKED"
	CodeRMACreditNotFound = "AR-RMA-NOT-FOUND"
	CodeRMACreditState    = "AR-RMA-STATE"

	rmaGLBackoffBase = time.Minute
	rmaGLBackoffMax  = time.Hour
	// RMASweepGrace is how old a PENDING credit must be before the sweeper posts it, so the sweep does not race
	// the request that just stored it (both would be safe, the ledger key and the POSTED guard dedupe, but one call is enough).
	RMASweepGrace = 2 * time.Minute
	// MaxRMASweepBatch caps the credits one sweep posts.
	MaxRMASweepBatch = 200
)

// RMACredit is the credit note sales asks receivables to post (AC-30.5). The
// customer, currency and ledger are never taken from the caller: they come
// from the invoice's open item and the legal entity's configured ledger.
type RMACredit struct {
	RMAID, RMANumber, LegalEntityID, InvoiceNumber, InvoiceID, PostingDate string
	Gross, Restocking, Net                                                 decimal.Decimal
	TaxLines                                                               []RMATaxLine
	InvoiceLines                                                           []RMAInvoiceLine
}

type RMATaxLine struct {
	Code   string          `json:"code"`
	Amount decimal.Decimal `json:"amount"`
}

// RMACreditNote is the AR open item and journal produced from an RMA.
type RMACreditNote struct {
	OpenItemID       string          `json:"openItemId"`
	DocumentNumber   string          `json:"documentNumber"`
	JournalPostingID string          `json:"journalPostingId"`
	CustomerID       string          `json:"customerId"`
	Currency         string          `json:"currency"`
	Gross            decimal.Decimal `json:"gross"`
	Restocking       decimal.Decimal `json:"restocking"`
	Net              decimal.Decimal `json:"net"`
	Tax              decimal.Decimal `json:"tax"`
}

// RMACreditView is a stored RMA credit as the operator sees it (list, retry).
type RMACreditView struct {
	ID               string          `json:"id"`
	RMAID            string          `json:"rmaId"`
	RMANumber        string          `json:"rmaNumber"`
	LegalEntityID    string          `json:"legalEntityId"`
	DocumentNumber   string          `json:"documentNumber"`
	CreditOpenItemID string          `json:"creditOpenItemId"`
	CustomerID       string          `json:"customerId"`
	Currency         string          `json:"currency"`
	PostingDate      string          `json:"postingDate"`
	Gross            decimal.Decimal `json:"gross"`
	Restocking       decimal.Decimal `json:"restocking"`
	Net              decimal.Decimal `json:"net"`
	Tax              decimal.Decimal `json:"tax"`
	Status           string          `json:"status"`
	JournalPostingID *string         `json:"journalPostingId,omitempty"`
	GLAttempts       int             `json:"glAttempts"`
	GLNextAttemptAt  *time.Time      `json:"glNextAttemptAt,omitempty"`
	GLParked         bool            `json:"glParked"`
	CreatedAt        time.Time       `json:"createdAt"`

	ReservationID        string `json:"reservationId,omitempty"`
	ReservationCommitted bool   `json:"reservationCommitted,omitempty"`
}

// RMACreditFilter narrows the operator list.
type RMACreditFilter struct {
	LegalEntityID string
	Status        string
	ParkedOnly    bool
	Limit         int
}

// RMASweepResult counts what one sweep did.
type RMASweepResult struct {
	Posted    int `json:"posted"`
	Failed    int `json:"failed"`
	Parked    int `json:"parked"`
	Committed int `json:"committed"`
}

type rmaCreditRow struct {
	id, legalEntityID, rmaID, rmaNumber, creditItemID, customerID, currency, postingDate, ledgerID, requestHash, status string
	journal                                                                                                             *string
	gross, restocking, net, tax                                                                                         decimal.Decimal
	attempts                                                                                                            int
	nextAttempt                                                                                                         *time.Time
	parked, taxRecorded, committed                                                                                      bool
	createdAt                                                                                                           time.Time
	billingInvoiceID, reservationID                                                                                     string
	lines                                                                                                               []RMAInvoiceLine
}

func (r rmaCreditRow) doc() string { return "CN-" + r.rmaNumber }

func (r rmaCreditRow) note() RMACreditNote {
	journal := ""
	if r.journal != nil {
		journal = *r.journal
	}
	return RMACreditNote{OpenItemID: r.creditItemID, DocumentNumber: r.doc(), JournalPostingID: journal, CustomerID: r.customerID,
		Currency: r.currency, Gross: r.gross, Restocking: r.restocking, Net: r.net, Tax: r.tax}
}

func (r rmaCreditRow) view() RMACreditView {
	return RMACreditView{ID: r.id, RMAID: r.rmaID, RMANumber: r.rmaNumber, LegalEntityID: r.legalEntityID, DocumentNumber: r.doc(),
		CreditOpenItemID: r.creditItemID, CustomerID: r.customerID, Currency: r.currency, PostingDate: r.postingDate,
		Gross: r.gross, Restocking: r.restocking, Net: r.net, Tax: r.tax, Status: r.status, JournalPostingID: r.journal,
		GLAttempts: r.attempts, GLNextAttemptAt: r.nextAttempt, GLParked: r.parked, CreatedAt: r.createdAt,
		ReservationID: r.reservationID, ReservationCommitted: r.committed}
}

const rmaCreditColumns = `id::text, legal_entity_id::text, rma_id::text, rma_number, credit_open_item_id::text, customer_id::text, currency::text,
	posting_date::text, ledger_id::text, request_hash, status, journal_posting_id, gross::text, restocking::text, net::text, tax::text, gl_attempts,
	gl_next_attempt_at, gl_parked, created_at, tax_recorded, coalesce(billing_invoice_id::text, ''), coalesce(reservation_lines::text, ''),
	coalesce(billing_reservation_id::text, ''), reservation_committed`

// WithLedger sets the journal poster used by RMA credits.
func (s *Postgres) WithLedger(l ledgerclient.Ledger) *Postgres {
	s.ledger = l
	return s
}

// WithRMALedgers pins, per legal entity, the ledger the RMA credit journal is posted to.
func (s *Postgres) WithRMALedgers(byLegalEntity map[string]string) *Postgres {
	s.rmaLedgers = byLegalEntity
	return s
}

func rmaInvalid(detail string) error {
	return httpx.NewProblem(http.StatusUnprocessableEntity, CodeRMACreditInvalid, detail)
}

func rmaParked() error {
	return httpx.NewProblem(http.StatusConflict, CodeRMAParked, "the ledger or billing refused this credit; it is parked until an operator retries it (POST /v1/receivables/rma-credits/{id}:retry)")
}

func rmaHash(in RMACredit) string {
	text := fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s|%s", in.RMAID, in.RMANumber, in.LegalEntityID, in.InvoiceNumber,
		in.PostingDate, in.Gross, in.Restocking, in.Net)
	for _, t := range in.TaxLines {
		text += fmt.Sprintf("|%s=%s", t.Code, t.Amount)
	}
	for _, l := range in.InvoiceLines {
		text += fmt.Sprintf("|line%d=%s", l.LineNo, l.Qty)
	}
	if len(in.TaxLines) > 0 || len(in.InvoiceLines) > 0 {
		text += "|invoice=" + in.InvoiceID
	}
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// ValidateRMAAmounts checks that gross is restocking plus net, all non-negative, net and gross positive.
func ValidateRMAAmounts(gross, restocking, net decimal.Decimal) error {
	switch {
	case gross.IsNegative() || restocking.IsNegative() || net.IsNegative():
		return rmaInvalid("gross, restocking and net must not be negative")
	case !net.IsPositive():
		return rmaInvalid("net credit must be greater than 0")
	case !gross.Equal(restocking.Add(net)):
		return rmaInvalid("gross must equal restocking plus net")
	}
	return nil
}

const MaxRMATaxLines = 20

func ValidateRMATax(lines []RMATaxLine) error {
	if len(lines) > MaxRMATaxLines {
		return rmaInvalid(fmt.Sprintf("a credit reverses at most %d tax codes", MaxRMATaxLines))
	}
	seen := map[string]bool{}
	for _, t := range lines {
		switch {
		case t.Code == "" || len(t.Code) > 20:
			return rmaInvalid("a tax line needs a tax code of 1 to 20 characters")
		case seen[t.Code]:
			return rmaInvalid("tax code " + t.Code + " is listed twice")
		case !t.Amount.IsPositive():
			return rmaInvalid("the tax reversed under " + t.Code + " must be greater than 0")
		}
		seen[t.Code] = true
	}
	return nil
}

func taxTotal(lines []RMATaxLine) decimal.Decimal {
	total := decimal.Zero
	for _, t := range lines {
		total = total.Add(t.Amount)
	}
	return total
}

// PostRMACredit records an RMA credit against the original invoice and posts Dr 4000 gross / Cr 4200 restocking /
// Cr 1200 net (AC-30.5). The credit is stored PENDING first; the journal is posted after that commit under the key
// rma-credit:<legal entity>:<document>, and the negative CREDIT_NOTE open item is created in the transaction that marks
// the credit POSTED, so AR never carries a credit without its 1200 entry. A failed post is retried with backoff (by a
// repeat of this call or by the sweeper) and parked when the ledger refuses it; an operator retries a parked credit,
// optionally with a new posting date.
//
// The credit is bounded by the invoice amount less the gross of the earlier RMA credits on it, not by its open amount:
// a paid or settled invoice is still returnable, and its credit leaves a credit balance for the customer.
func (s *Postgres) PostRMACredit(ctx context.Context, in RMACredit) (RMACreditNote, error) {
	if err := ValidateRMAAmounts(in.Gross, in.Restocking, in.Net); err != nil {
		return RMACreditNote{}, err
	}
	if err := ValidateRMATax(in.TaxLines); err != nil {
		return RMACreditNote{}, err
	}
	ledgerID := s.rmaLedgers[in.LegalEntityID]
	if s.ledger == nil || ledgerID == "" {
		return RMACreditNote{}, httpx.NewProblem(http.StatusServiceUnavailable, CodeRMAUnavailable, "the ledger or the legal entity's RMA ledger is not configured")
	}
	if err := s.checkReservable(in); err != nil {
		return RMACreditNote{}, err
	}
	var err error
	var taxAccounts map[string]string
	var billed map[string]decimal.Decimal
	tax := taxTotal(in.TaxLines)
	p, _ := tenant.FromContext(ctx)
	doc := "CN-" + in.RMANumber
	hash := rmaHash(in)
	var row rmaCreditRow
	reservationID, err := s.reserveBeforeInsert(ctx, p.TenantID, in, hash)
	if err != nil {
		return RMACreditNote{}, err
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		row, err = loadRMACredit(ctx, tx, `legal_entity_id = $2 and rma_id = $3`, p.TenantID, in.LegalEntityID, in.RMAID)
		if err == nil {
			if row.requestHash != hash {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "this RMA was already credited with different amounts or references")
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if taxAccounts, err = s.rmaTaxAccountsFor(in); err != nil {
			return err
		}
		if billed, err = s.billedTax(ctx, in); err != nil {
			return err
		}
		invoice, err := scanOpenItem(tx.QueryRow(ctx, `select `+openItemColumns+` from open_item
			where tenant_id = $1 and legal_entity_id = $2 and document_number = $3 and source_type = 'INVOICE' for update`,
			p.TenantID, in.LegalEntityID, in.InvoiceNumber))
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeRMAInvoice, "the original invoice is not an open item of this legal entity")
		}
		if err != nil {
			return err
		}
		// The invoice row lock above serializes RMA credits of one invoice, so this sum sees every earlier credit.
		var creditedText string
		if err := tx.QueryRow(ctx, `select coalesce(sum(gross + tax), 0)::text from rma_credit where tenant_id = $1 and invoice_open_item_id = $2`,
			p.TenantID, invoice.ID).Scan(&creditedText); err != nil {
			return err
		}
		credited := decimal.RequireFromString(creditedText)
		if left, value := invoice.Amount.Sub(credited), in.Gross.Add(tax); value.GreaterThan(left) {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeRMAInvoice,
				fmt.Sprintf("the return value %s exceeds what invoice %s still allows (amount %s, already returned by RMAs %s)",
					value, invoice.DocumentNumber, invoice.Amount, credited))
		}
		if err := checkTaxAgainstInvoice(ctx, tx, p.TenantID, invoice.ID, billed, in.TaxLines); err != nil {
			return err
		}
		var taken bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from open_item where tenant_id = $1 and legal_entity_id = $2 and document_number = $3)`,
			p.TenantID, in.LegalEntityID, doc).Scan(&taken); err != nil {
			return err
		}
		if taken {
			return httpx.NewProblem(http.StatusConflict, CodeRMACreditState, fmt.Sprintf("document number %s is already an open item of this legal entity", doc))
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into rma_credit (tenant_id, id, legal_entity_id, rma_id, rma_number, invoice_open_item_id, credit_open_item_id,
			customer_id, currency, posting_date, gross, restocking, net, tax, ledger_id, request_hash, status,
			billing_invoice_id, reservation_lines, billing_reservation_id)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, 'PENDING',
			nullif($17, '')::uuid, nullif($18, '')::jsonb, nullif($19, '')::uuid)`,
			p.TenantID, id, in.LegalEntityID, in.RMAID, in.RMANumber, invoice.ID, ids.New(), invoice.CustomerID, invoice.Currency,
			in.PostingDate, in.Gross.String(), in.Restocking.String(), in.Net.String(), tax.String(), ledgerID, hash,
			invoiceIDIfReserved(in), linesJSON(in.InvoiceLines), reservationID); err != nil {
			return err
		}
		for _, t := range in.TaxLines {
			if _, err := tx.Exec(ctx, `insert into rma_credit_tax (tenant_id, id, legal_entity_id, rma_credit_id, tax_code, tax_account, amount)
				values ($1, $2, $3, $4, $5, $6, $7)`, p.TenantID, ids.New(), in.LegalEntityID, id, t.Code, taxAccounts[t.Code], t.Amount.String()); err != nil {
				return err
			}
		}
		row, err = loadRMACredit(ctx, tx, `id = $2`, p.TenantID, id)
		return err
	})
	var pgErr *pgconn.PgError
	duplicate := errors.As(err, &pgErr) && pgErr.Code == uniqueViolation
	if err != nil && reservationID != "" && !duplicate {
		s.releaseUnclaimed(ctx, p.TenantID, in, reservationID)
	}
	if duplicate {
		return RMACreditNote{}, httpx.NewProblem(http.StatusConflict, CodeIdempotencyBusy, "a concurrent request is crediting this RMA; retry")
	}
	if err != nil {
		return RMACreditNote{}, mapWriteErr(err)
	}
	if row.status == "PENDING" {
		if row, err = s.postRMAJournal(ctx, p.TenantID, row, time.Now().UTC()); err != nil {
			return RMACreditNote{}, err
		}
	} else {
		_ = s.commitReservation(ctx, p.TenantID, row)
	}
	return row.note(), nil
}

// postRMAJournal posts a PENDING credit's journal and, in the transaction that marks it POSTED, opens its CREDIT_NOTE
// item and enqueues creditnote.posted. A concurrent post of the same credit (sales retry, sweeper) gets the same entry
// from the ledger key and finds the row already POSTED, so the item and the event are written once.
func (s *Postgres) postRMAJournal(ctx context.Context, tenantID string, row rmaCreditRow, now time.Time) (rmaCreditRow, error) {
	if row.parked {
		return row, rmaParked()
	}
	if row.nextAttempt != nil && row.nextAttempt.After(now) {
		return row, httpx.NewProblem(http.StatusServiceUnavailable, CodeRMAUnavailable, "the journal post is backing off after a failure; retry later")
	}
	row, err := s.ensureReserved(ctx, tenantID, row, now)
	if err != nil {
		return row, err
	}
	taxLines, err := s.loadRMATax(ctx, tenantID, row)
	if err != nil {
		return row, err
	}
	var journalID string
	if !row.taxRecorded {
		if err = s.recordTax(ctx, row, taxLines); err == nil {
			err = s.markTaxRecorded(ctx, tenantID, row.id)
		}
	}
	if err == nil {
		journalID, err = s.ledgerPost(ctx, row, taxLines)
	}
	if err != nil {
		parked := ledgerclient.Rejected(err) || taxclient.Rejected(err)
		next := now.Add(min(rmaGLBackoffBase<<min(row.attempts, 10), rmaGLBackoffMax))
		slog.ErrorContext(ctx, "rma credit journal not posted", "rma", row.rmaNumber, "attempts", row.attempts+1, "parked", parked, "err", err, "correlationId", middleware.GetReqID(ctx))
		s.recordRMAFailure(ctx, tenantID, row, next, parked)
		if parked {
			s.releaseReservation(ctx, row)
			return row, rmaParked()
		}
		return row, httpx.NewProblem(http.StatusServiceUnavailable, CodeRMAUnavailable, "the journal could not be posted; retry later")
	}
	var out rmaCreditRow
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update rma_credit set status = 'POSTED', journal_posting_id = $3, gl_next_attempt_at = null
			where tenant_id = $1 and id = $2 and status = 'PENDING'`, tenantID, row.id, journalID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			// Posted meanwhile by a concurrent call: its transaction opened the item and enqueued the event.
			out, err = loadRMACredit(ctx, tx, `id = $2`, tenantID, row.id)
			return err
		}
		if _, err := tx.Exec(ctx, `insert into open_item (tenant_id, id, legal_entity_id, customer_id, source_type, document_number,
			document_date, due_date, currency, amount, open_amount, status)
			values ($1, $2, $3, $4, 'CREDIT_NOTE', $5, $6, $6, $7, $8, $8, 'OPEN')`,
			tenantID, row.creditItemID, row.legalEntityID, row.customerID, row.doc(), row.postingDate, row.currency, row.net.Add(row.tax).Neg().String()); err != nil {
			return err
		}
		postings := []domain.Posting{{Account: domain.AccountSalesReturns, Debit: row.gross}}
		eventTax := make([]rcvevents.CreditNoteTax, 0, len(taxLines))
		for _, t := range taxLines {
			postings = append(postings, domain.Posting{Account: t.account, Debit: t.amount})
			eventTax = append(eventTax, rcvevents.CreditNoteTax{Code: t.code, Account: t.account, Amount: t.amount})
		}
		if row.restocking.IsPositive() {
			postings = append(postings, domain.Posting{Account: domain.AccountRestocking, Credit: row.restocking})
		}
		postings = append(postings, domain.Posting{Account: domain.AccountReceivable, Credit: row.net.Add(row.tax)})
		if err := enqueue(ctx, tx, "creditnote", row.creditItemID, rcvevents.CreditNotePosted, tenantID, rcvevents.CreditNotePostedData{
			OpenItemID: row.creditItemID, ReturnOrderID: row.rmaID, RMANumber: row.rmaNumber, LegalEntityID: row.legalEntityID,
			CustomerID: row.customerID, DocumentNumber: row.doc(), Currency: row.currency, Gross: row.gross, Restocking: row.restocking,
			Net: row.net, Tax: row.tax, TaxLines: eventTax, JournalPostingID: &journalID, Postings: postings,
		}); err != nil {
			return err
		}
		out = row
		out.status, out.journal, out.nextAttempt = "POSTED", &journalID, nil
		return nil
	})
	if err != nil {
		slog.ErrorContext(ctx, "rma credit posted to the ledger but not recorded; the next attempt replays the entry", "rma", row.rmaNumber,
			"journal", journalID, "err", err, "correlationId", middleware.GetReqID(ctx))
		return row, err
	}
	out.committed = len(out.lines) > 0 && s.commitReservation(ctx, tenantID, out) == nil
	return out, nil
}

func (s *Postgres) ledgerPost(ctx context.Context, row rmaCreditRow, taxLines []rmaTaxRow) (string, error) {
	if s.ledger == nil {
		return "", errors.New("ledger is not configured")
	}
	owner, err := s.ledger.LegalEntity(ctx, row.legalEntityID, row.ledgerID)
	if err != nil {
		return "", err
	}
	if owner != row.legalEntityID {
		return "", &ledgerclient.Error{Op: "ledger", Status: http.StatusUnprocessableEntity, Code: "ledger_legal_entity_mismatch"}
	}
	lines := []ledgerclient.Line{{Account: domain.AccountSalesReturns, Amount: row.gross}}
	for _, t := range taxLines {
		lines = append(lines, ledgerclient.Line{Account: t.account, Amount: t.amount, Text: "Tax reversal " + t.code})
	}
	if row.restocking.IsPositive() {
		lines = append(lines, ledgerclient.Line{Account: domain.AccountRestocking, Credit: true, Amount: row.restocking, Text: "Restocking"})
	}
	lines = append(lines, ledgerclient.Line{Account: domain.AccountReceivable, Credit: true, Amount: row.net.Add(row.tax)})
	return s.ledger.Post(ctx, ledgerclient.Entry{LegalEntityID: row.legalEntityID, LedgerID: row.ledgerID, DocumentID: row.doc(),
		PostingDate: row.postingDate, Currency: row.currency, IdempotencyKey: fmt.Sprintf("rma-credit:%s:%s", row.legalEntityID, row.doc()),
		SourceType: "rma", Lines: lines})
}

// RetryRMACredit is the operator path for a credit that is not POSTED: it clears the park and the backoff, optionally
// re-dates it (only a parked credit, whose last answer was a ledger refusal such as a closed period), and posts it now.
// The ledger key stays rma-credit:<legal entity>:<document>: the ledger keeps no entry for a refused post, and an entry it
// did keep under the key answers a re-dated post with an idempotency conflict, which parks the credit again instead of
// posting twice. The stored request hash is not changed, so sales' repeat of :credit still replays.
func (s *Postgres) RetryRMACredit(ctx context.Context, id, postingDate string) (RMACreditView, error) {
	p, _ := tenant.FromContext(ctx)
	var row rmaCreditRow
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		row, err = loadRMACredit(ctx, tx, `id = $2`, p.TenantID, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeRMACreditNotFound, "RMA credit not found")
		}
		if err != nil {
			return err
		}
		if row.status != "PENDING" {
			return httpx.NewProblem(http.StatusConflict, CodeRMACreditState, "the credit is already posted")
		}
		if postingDate != "" && postingDate != row.postingDate && !row.parked {
			return httpx.NewProblem(http.StatusConflict, CodeRMACreditState, "only a parked credit can be re-dated; this one is waiting for the ledger to answer")
		}
		if postingDate == "" {
			postingDate = row.postingDate
		}
		if _, err := tx.Exec(ctx, `update rma_credit set gl_parked = false, gl_next_attempt_at = null, posting_date = $3
			where tenant_id = $1 and id = $2`, p.TenantID, row.id, postingDate); err != nil {
			return err
		}
		row, err = loadRMACredit(ctx, tx, `id = $2`, p.TenantID, row.id)
		return err
	})
	if err != nil {
		return RMACreditView{}, err
	}
	posted, err := s.postRMAJournal(ctx, p.TenantID, row, time.Now().UTC())
	if err != nil {
		return RMACreditView{}, err
	}
	return posted.view(), nil
}

// ListRMACredits lists stored RMA credits newest first, for the operator to find PENDING and parked ones.
func (s *Postgres) ListRMACredits(ctx context.Context, f RMACreditFilter) ([]RMACreditView, error) {
	p, _ := tenant.FromContext(ctx)
	out := []RMACreditView{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+rmaCreditColumns+` from rma_credit
			where tenant_id = $1 and ($2 = '' or legal_entity_id = nullif($2, '')::uuid) and ($3 = '' or status = $3) and (not $4 or gl_parked)
			order by created_at desc, id desc limit $5`, p.TenantID, f.LegalEntityID, f.Status, f.ParkedOnly, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRMACredit(rows)
			if err != nil {
				return err
			}
			out = append(out, r.view())
		}
		return rows.Err()
	})
	return out, err
}

// SweepRMACredits posts the PENDING, unparked credits of the caller's legal-entity scope whose backoff has run out and
// that are older than RMASweepGrace, oldest first, at most limit of them. Each is posted on its own; a failure is
// recorded on the credit (backoff or park) and the sweep goes on.
func (s *Postgres) SweepRMACredits(ctx context.Context, now time.Time, limit int) (RMASweepResult, error) {
	p, _ := tenant.FromContext(ctx)
	if limit < 1 || limit > MaxRMASweepBatch {
		limit = MaxRMASweepBatch
	}
	var due []rmaCreditRow
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+rmaCreditColumns+` from rma_credit
			where tenant_id = $1 and status = 'PENDING' and not gl_parked and (gl_next_attempt_at is null or gl_next_attempt_at <= $2)
			and created_at <= $3 order by created_at, id limit $4`, p.TenantID, now, now.Add(-RMASweepGrace), limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRMACredit(rows)
			if err != nil {
				return err
			}
			due = append(due, r)
		}
		return rows.Err()
	})
	if err != nil {
		return RMASweepResult{}, err
	}
	var res RMASweepResult
	for _, r := range due {
		if s.ledger == nil {
			res.Failed++
			continue
		}
		_, err := s.postRMAJournal(ctx, p.TenantID, r, now)
		var prob *httpx.Problem
		switch {
		case err == nil:
			res.Posted++
		case errors.As(err, &prob) && prob.Code == CodeRMAParked:
			res.Parked++
		default:
			res.Failed++
		}
	}
	uncommitted, err := s.uncommittedRMACredits(ctx, p.TenantID, limit)
	if err != nil {
		return res, err
	}
	for _, r := range uncommitted {
		if s.commitReservation(ctx, p.TenantID, r) == nil {
			res.Committed++
		} else {
			res.Failed++
		}
	}
	return res, nil
}

// RMASweepLegalEntities lists the legal entities that hold PENDING, unparked RMA credits, so a tenant-wide sweeper
// can run SweepRMACredits once per legal entity under a principal scoped to it (the ledger post needs that scope).
func (s *Postgres) RMASweepLegalEntities(ctx context.Context) ([]string, error) {
	p, _ := tenant.FromContext(ctx)
	var out []string
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select distinct legal_entity_id::text from rma_credit where tenant_id = $1
			and ((status = 'PENDING' and not gl_parked) or (status = 'POSTED' and billing_reservation_id is not null and not reservation_committed))`, p.TenantID)
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

func loadRMACredit(ctx context.Context, tx pgx.Tx, where string, args ...any) (rmaCreditRow, error) {
	return scanRMACredit(tx.QueryRow(ctx, `select `+rmaCreditColumns+` from rma_credit where tenant_id = $1 and `+where+` for update`, args...))
}

func scanRMACredit(row rowScanner) (rmaCreditRow, error) {
	var r rmaCreditRow
	var gross, restocking, net, tax, lines string
	if err := row.Scan(&r.id, &r.legalEntityID, &r.rmaID, &r.rmaNumber, &r.creditItemID, &r.customerID, &r.currency, &r.postingDate, &r.ledgerID,
		&r.requestHash, &r.status, &r.journal, &gross, &restocking, &net, &tax, &r.attempts, &r.nextAttempt, &r.parked, &r.createdAt, &r.taxRecorded,
		&r.billingInvoiceID, &lines, &r.reservationID, &r.committed); err != nil {
		return rmaCreditRow{}, err
	}
	if lines != "" {
		if err := json.Unmarshal([]byte(lines), &r.lines); err != nil {
			return rmaCreditRow{}, err
		}
	}
	r.gross, r.restocking, r.net, r.tax = decimal.RequireFromString(gross), decimal.RequireFromString(restocking), decimal.RequireFromString(net), decimal.RequireFromString(tax)
	return r, nil
}
