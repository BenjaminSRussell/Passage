package store

import (
	"context"
	"errors"
	"net/http"

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

const CodeRMACreditInvalid = "AR-RMA-CREDIT"

// RMACredit is the credit note sales asks receivables to post (AC-30.5).
type RMACredit struct {
	RMAID, RMANumber, CustomerID, LegalEntityID, Currency, PostingDate, IdempotencyKey, LedgerID string
	Gross, Restocking, Net                                                                       decimal.Decimal
}

// RMACreditNote is the AR open item and journal produced from an RMA.
type RMACreditNote struct {
	OpenItemID       string          `json:"openItemId"`
	DocumentNumber   string          `json:"documentNumber"`
	JournalPostingID string          `json:"journalPostingId,omitempty"`
	Gross            decimal.Decimal `json:"gross"`
	Restocking       decimal.Decimal `json:"restocking"`
	Net              decimal.Decimal `json:"net"`
}

// WithLedger sets the journal poster used by RMA credits.
func (s *Postgres) WithLedger(l ledgerclient.Ledger) *Postgres {
	s.ledger = l
	return s
}

// PostRMACredit creates a CREDIT_NOTE open item (negative) and posts
// Dr 4000 gross / Cr 4200 restocking / Cr 1200 net (AC-30.5).
func (s *Postgres) PostRMACredit(ctx context.Context, in RMACredit) (RMACreditNote, error) {
	if in.Net.IsNegative() || in.Gross.IsNegative() || in.Restocking.IsNegative() || !in.Gross.Equal(in.Restocking.Add(in.Net)) {
		return RMACreditNote{}, httpx.NewProblem(http.StatusBadRequest, CodeRMACreditInvalid, "gross must equal restocking plus net, all non-negative")
	}
	if in.Net.IsZero() {
		return RMACreditNote{}, httpx.NewProblem(http.StatusBadRequest, CodeRMACreditInvalid, "net credit must be greater than 0")
	}
	p, _ := tenant.FromContext(ctx)
	doc := "CN-" + in.RMANumber
	var out RMACreditNote
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		item, err := loadOpenItemByDoc(ctx, tx, p.TenantID, in.LegalEntityID, doc)
		if err == nil {
			out = RMACreditNote{OpenItemID: item.ID, DocumentNumber: item.DocumentNumber, Gross: in.Gross, Restocking: in.Restocking, Net: in.Net}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		amount := in.Net.Neg()
		id := ids.New()
		row := tx.QueryRow(ctx, `insert into open_item (tenant_id, id, legal_entity_id, customer_id, source_type, document_number,
			document_date, due_date, currency, amount, open_amount, status)
			values ($1, $2, $3, $4, 'CREDIT_NOTE', $5, $6, $6, $7, $8, $8, 'OPEN')
			returning `+openItemColumns,
			p.TenantID, id, in.LegalEntityID, in.CustomerID, doc, in.PostingDate, in.Currency, amount.String())
		item, err = scanOpenItem(row)
		if err != nil {
			return err
		}
		journalID := ""
		if s.ledger != nil && in.LedgerID != "" {
			lines := []ledgerclient.Line{{Account: domain.AccountSalesReturns, Amount: in.Gross}}
			if in.Restocking.IsPositive() {
				lines = append(lines, ledgerclient.Line{Account: domain.AccountRestocking, Credit: true, Amount: in.Restocking, Text: "Restocking"})
			}
			lines = append(lines, ledgerclient.Line{Account: domain.AccountReceivable, Credit: true, Amount: in.Net})
			journalID, err = s.ledger.Post(ctx, ledgerclient.Entry{
				LegalEntityID: in.LegalEntityID, LedgerID: in.LedgerID, DocumentID: doc, PostingDate: in.PostingDate,
				Currency: in.Currency, IdempotencyKey: in.IdempotencyKey, SourceType: "rma", Lines: lines,
			})
			if err != nil {
				return err
			}
		}
		out = RMACreditNote{OpenItemID: item.ID, DocumentNumber: item.DocumentNumber, JournalPostingID: journalID,
			Gross: in.Gross, Restocking: in.Restocking, Net: in.Net}
		postings := []domain.Posting{{Account: domain.AccountSalesReturns, Debit: in.Gross}}
		if in.Restocking.IsPositive() {
			postings = append(postings, domain.Posting{Account: domain.AccountRestocking, Credit: in.Restocking})
		}
		postings = append(postings, domain.Posting{Account: domain.AccountReceivable, Credit: in.Net})
		jid := journalID
		var jptr *string
		if jid != "" {
			jptr = &jid
		}
		return enqueue(ctx, tx, "creditnote", item.ID, rcvevents.CreditNotePosted, p.TenantID, rcvevents.CreditNotePostedData{
			OpenItemID: item.ID, ReturnOrderID: in.RMAID, RMANumber: in.RMANumber, LegalEntityID: in.LegalEntityID,
			CustomerID: in.CustomerID, DocumentNumber: doc, Currency: in.Currency, Gross: in.Gross, Restocking: in.Restocking,
			Net: in.Net, JournalPostingID: jptr, Postings: postings,
		})
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		item, loadErr := s.lookupByDocument(ctx, in.LegalEntityID, doc)
		if loadErr == nil {
			return RMACreditNote{OpenItemID: item.ID, DocumentNumber: item.DocumentNumber, Gross: in.Gross, Restocking: in.Restocking, Net: in.Net}, nil
		}
	}
	return out, mapWriteErr(err)
}

func (s *Postgres) lookupByDocument(ctx context.Context, legalEntityID, doc string) (OpenItem, error) {
	p, _ := tenant.FromContext(ctx)
	var out OpenItem
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadOpenItemByDoc(ctx, tx, p.TenantID, legalEntityID, doc)
		return err
	})
	return out, err
}

func loadOpenItemByDoc(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, doc string) (OpenItem, error) {
	return scanOpenItem(tx.QueryRow(ctx, `select `+openItemColumns+` from open_item where tenant_id = $1 and legal_entity_id = $2 and document_number = $3`,
		tenantID, legalEntityID, doc))
}
