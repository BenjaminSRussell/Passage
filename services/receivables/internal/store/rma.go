package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
)

const (
	CodeRMACreditInvalid = "AR-RMA-CREDIT"
	CodeRMAInvoice       = "AR-RMA-INVOICE"
	CodeRMAUnavailable   = "AR-RMA-GL"
	CodeRMAParked        = "AR-RMA-PARKED"

	rmaGLBackoffBase = time.Minute
	rmaGLBackoffMax  = time.Hour
)

// RMACredit is the credit note sales asks receivables to post (AC-30.5). The
// customer, currency and ledger are never taken from the caller: they come
// from the invoice's open item and the legal entity's configured ledger.
type RMACredit struct {
	RMAID, RMANumber, LegalEntityID, InvoiceNumber, PostingDate string
	Gross, Restocking, Net                                      decimal.Decimal
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
}

type rmaCreditRow struct {
	id, creditItemID, customerID, currency, postingDate, ledgerID, requestHash, status string
	journal                                                                            *string
	gross, restocking, net                                                             decimal.Decimal
	attempts                                                                           int
	nextAttempt                                                                        *time.Time
	parked                                                                             bool
}

const rmaCreditColumns = `id::text, credit_open_item_id::text, customer_id::text, currency::text, posting_date::text, ledger_id::text,
	request_hash, status, journal_posting_id, gross::text, restocking::text, net::text, gl_attempts, gl_next_attempt_at, gl_parked`

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

func rmaHash(in RMACredit) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s|%s", in.RMAID, in.RMANumber, in.LegalEntityID, in.InvoiceNumber,
		in.PostingDate, in.Gross, in.Restocking, in.Net)))
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

// PostRMACredit creates a CREDIT_NOTE open item (negative) against the original
// invoice and posts Dr 4000 gross / Cr 4200 restocking / Cr 1200 net (AC-30.5).
// The credit is recorded PENDING first and the journal is posted after that
// commit under the key rma-credit:<legal entity>:<document>; a failed post is
// retried with backoff and parked when the ledger refuses it.
func (s *Postgres) PostRMACredit(ctx context.Context, in RMACredit) (RMACreditNote, error) {
	if err := ValidateRMAAmounts(in.Gross, in.Restocking, in.Net); err != nil {
		return RMACreditNote{}, err
	}
	ledgerID := s.rmaLedgers[in.LegalEntityID]
	if s.ledger == nil || ledgerID == "" {
		return RMACreditNote{}, httpx.NewProblem(http.StatusServiceUnavailable, CodeRMAUnavailable, "the ledger or the legal entity's RMA ledger is not configured")
	}
	p, _ := tenant.FromContext(ctx)
	doc := "CN-" + in.RMANumber
	hash := rmaHash(in)
	var row rmaCreditRow
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		row, err = loadRMACredit(ctx, tx, p.TenantID, in.LegalEntityID, in.RMAID)
		if err == nil {
			if row.requestHash != hash {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "this RMA was already credited with different amounts or references")
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
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
		var creditedText string
		if err := tx.QueryRow(ctx, `select coalesce(sum(net), 0)::text from rma_credit where tenant_id = $1 and invoice_open_item_id = $2`,
			p.TenantID, invoice.ID).Scan(&creditedText); err != nil {
			return err
		}
		credited := decimal.RequireFromString(creditedText)
		if in.Gross.GreaterThan(invoice.Amount) || credited.Add(in.Net).GreaterThan(invoice.OpenAmount) {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeRMAInvoice,
				fmt.Sprintf("the credit exceeds what is open on invoice %s (open %s, already credited by RMAs %s)", invoice.DocumentNumber, invoice.OpenAmount, credited))
		}
		creditID := ids.New()
		if _, err := tx.Exec(ctx, `insert into open_item (tenant_id, id, legal_entity_id, customer_id, source_type, document_number,
			document_date, due_date, currency, amount, open_amount, status)
			values ($1, $2, $3, $4, 'CREDIT_NOTE', $5, $6, $6, $7, $8, $8, 'OPEN')`,
			p.TenantID, creditID, in.LegalEntityID, invoice.CustomerID, doc, in.PostingDate, invoice.Currency, in.Net.Neg().String()); err != nil {
			return err
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into rma_credit (tenant_id, id, legal_entity_id, rma_id, rma_number, invoice_open_item_id, credit_open_item_id,
			customer_id, currency, posting_date, gross, restocking, net, ledger_id, request_hash, status)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, 'PENDING')`,
			p.TenantID, id, in.LegalEntityID, in.RMAID, in.RMANumber, invoice.ID, creditID, invoice.CustomerID, invoice.Currency,
			in.PostingDate, in.Gross.String(), in.Restocking.String(), in.Net.String(), ledgerID, hash); err != nil {
			return err
		}
		row, err = loadRMACredit(ctx, tx, p.TenantID, in.LegalEntityID, in.RMAID)
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return RMACreditNote{}, httpx.NewProblem(http.StatusConflict, CodeIdempotencyBusy, "a concurrent request is crediting this RMA; retry")
	}
	if err != nil {
		return RMACreditNote{}, mapWriteErr(err)
	}
	if row.status == "PENDING" {
		if row, err = s.postRMAJournal(ctx, p.TenantID, in, doc, row); err != nil {
			return RMACreditNote{}, err
		}
	}
	return RMACreditNote{OpenItemID: row.creditItemID, DocumentNumber: doc, JournalPostingID: *row.journal, CustomerID: row.customerID,
		Currency: row.currency, Gross: row.gross, Restocking: row.restocking, Net: row.net}, nil
}

func (s *Postgres) postRMAJournal(ctx context.Context, tenantID string, in RMACredit, doc string, row rmaCreditRow) (rmaCreditRow, error) {
	now := time.Now().UTC()
	if row.parked {
		return row, httpx.NewProblem(http.StatusConflict, CodeRMAParked, "the ledger refused this credit's journal; it is parked for an operator")
	}
	if row.nextAttempt != nil && row.nextAttempt.After(now) {
		return row, httpx.NewProblem(http.StatusServiceUnavailable, CodeRMAUnavailable, "the journal post is backing off after a failure; retry later")
	}
	key := fmt.Sprintf("rma-credit:%s:%s", in.LegalEntityID, doc)
	journalID, err := s.ledgerPost(ctx, in, doc, key, row)
	if err != nil {
		parked := ledgerclient.Rejected(err)
		next := now.Add(min(rmaGLBackoffBase<<min(row.attempts, 10), rmaGLBackoffMax))
		slog.ErrorContext(ctx, "rma credit journal not posted", "rma", in.RMANumber, "attempts", row.attempts+1, "parked", parked, "err", err, "correlationId", middleware.GetReqID(ctx))
		if recErr := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx, `update rma_credit set gl_attempts = gl_attempts + 1, gl_next_attempt_at = $4, gl_parked = $5
				where tenant_id = $1 and legal_entity_id = $2 and rma_id = $3`, tenantID, in.LegalEntityID, in.RMAID, next, parked)
			return e
		}); recErr != nil {
			slog.ErrorContext(ctx, "rma credit attempt not recorded", "rma", in.RMANumber, "err", recErr, "correlationId", middleware.GetReqID(ctx))
		}
		if parked {
			return row, httpx.NewProblem(http.StatusConflict, CodeRMAParked, "the ledger refused this credit's journal; it is parked for an operator")
		}
		return row, httpx.NewProblem(http.StatusServiceUnavailable, CodeRMAUnavailable, "the journal could not be posted; retry later")
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `update rma_credit set status = 'POSTED', journal_posting_id = $4, gl_next_attempt_at = null
			where tenant_id = $1 and legal_entity_id = $2 and rma_id = $3 and status = 'PENDING'`, tenantID, in.LegalEntityID, in.RMAID, journalID); err != nil {
			return err
		}
		postings := []domain.Posting{{Account: domain.AccountSalesReturns, Debit: row.gross}}
		if row.restocking.IsPositive() {
			postings = append(postings, domain.Posting{Account: domain.AccountRestocking, Credit: row.restocking})
		}
		postings = append(postings, domain.Posting{Account: domain.AccountReceivable, Credit: row.net})
		return enqueue(ctx, tx, "creditnote", row.creditItemID, rcvevents.CreditNotePosted, tenantID, rcvevents.CreditNotePostedData{
			OpenItemID: row.creditItemID, ReturnOrderID: in.RMAID, RMANumber: in.RMANumber, LegalEntityID: in.LegalEntityID,
			CustomerID: row.customerID, DocumentNumber: doc, Currency: row.currency, Gross: row.gross, Restocking: row.restocking,
			Net: row.net, JournalPostingID: &journalID, Postings: postings,
		})
	})
	if err != nil {
		return row, err
	}
	row.status, row.journal = "POSTED", &journalID
	return row, nil
}

func (s *Postgres) ledgerPost(ctx context.Context, in RMACredit, doc, key string, row rmaCreditRow) (string, error) {
	owner, err := s.ledger.LegalEntity(ctx, in.LegalEntityID, row.ledgerID)
	if err != nil {
		return "", err
	}
	if owner != in.LegalEntityID {
		return "", &ledgerclient.Error{Op: "ledger", Status: http.StatusUnprocessableEntity, Code: "ledger_legal_entity_mismatch"}
	}
	lines := []ledgerclient.Line{{Account: domain.AccountSalesReturns, Amount: row.gross}}
	if row.restocking.IsPositive() {
		lines = append(lines, ledgerclient.Line{Account: domain.AccountRestocking, Credit: true, Amount: row.restocking, Text: "Restocking"})
	}
	lines = append(lines, ledgerclient.Line{Account: domain.AccountReceivable, Credit: true, Amount: row.net})
	return s.ledger.Post(ctx, ledgerclient.Entry{LegalEntityID: in.LegalEntityID, LedgerID: row.ledgerID, DocumentID: doc, PostingDate: row.postingDate,
		Currency: row.currency, IdempotencyKey: key, SourceType: "rma", Lines: lines})
}

func loadRMACredit(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, rmaID string) (rmaCreditRow, error) {
	var r rmaCreditRow
	var gross, restocking, net string
	err := tx.QueryRow(ctx, `select `+rmaCreditColumns+` from rma_credit where tenant_id = $1 and legal_entity_id = $2 and rma_id = $3 for update`,
		tenantID, legalEntityID, rmaID).Scan(&r.id, &r.creditItemID, &r.customerID, &r.currency, &r.postingDate, &r.ledgerID, &r.requestHash,
		&r.status, &r.journal, &gross, &restocking, &net, &r.attempts, &r.nextAttempt, &r.parked)
	if err != nil {
		return rmaCreditRow{}, err
	}
	r.gross, r.restocking, r.net = decimal.RequireFromString(gross), decimal.RequireFromString(restocking), decimal.RequireFromString(net)
	return r, nil
}
