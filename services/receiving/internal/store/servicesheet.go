package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

var ErrSheetNotFound = errors.New("service entry sheet not found")

const (
	sheetAggregate    = "serviceentrysheet"
	SelfBillAggregate = "ersselfbill"
)

type ServiceSheet struct {
	ID              string
	LegalEntityID   string
	VendorID        string
	PurchaseOrderID string
	Currency        string
	Status          string
	Total           decimal.Decimal
	Lines           []domain.SheetLine
	CreatedBy       string
	CreatedAt       time.Time
	DecidedBy       string
	DecidedAt       *time.Time
	RejectReason    string
	ReceiptID       string
	LedgerEntryID   string
}

type SelfBill struct {
	Number        string
	InvoiceID     string
	LegalEntityID string
	VendorID      string
	InvoiceNumber string
	ContentType   string
	Body          []byte
	TaxCode       string
	TaxAmount     decimal.Decimal
	Total         decimal.Decimal
	CreatedAt     time.Time
}

var ErrSelfBillNotFound = errors.New("self-billing document not found")

type SheetAcceptOutcome struct {
	Receipt       Receipt
	Amounts       []LineAmount
	LedgerEntryID string
	Events        []events.Event
	PostedEvent   func(number string) (events.Event, error)
}

type SheetAcceptFunc func(sheet ServiceSheet, allocated Receipt) (SheetAcceptOutcome, error)

type SheetDecisionFunc func(ServiceSheet) (events.Event, error)

func SheetCommitted(sheets []ServiceSheet) map[int]decimal.Decimal {
	committed := map[int]decimal.Decimal{}
	for _, s := range sheets {
		if s.Status == domain.SheetRejected {
			continue
		}
		for _, l := range s.Lines {
			committed[l.POLineNo] = committed[l.POLineNo].Add(l.Amount)
		}
	}
	return committed
}

func CheckSheetLimits(in ServiceSheet, existing []ServiceSheet, limits map[int]decimal.Decimal) error {
	committed := SheetCommitted(existing)
	for no, added := range SheetCommitted([]ServiceSheet{in}) {
		if committed[no].Add(added).GreaterThan(limits[no]) {
			return &domain.Error{Code: domain.CodeSheetOverLine, Detail: fmt.Sprintf("purchase order line %d: sheets would total %s against a line value of %s", no, committed[no].Add(added), limits[no])}
		}
	}
	return nil
}

func CheckSheetDecision(s ServiceSheet, by string) error {
	switch {
	case s.Status != domain.SheetPending:
		return &domain.Error{Code: domain.CodeSheetNotPending, Detail: fmt.Sprintf("service entry sheet %s is already %s", s.ID, s.Status)}
	case sameActor(s.CreatedBy, by):
		return &domain.Error{Code: domain.CodeSheetSelfAccept, Detail: "the user who entered the sheet cannot decide it"}
	}
	return nil
}

const sheetColumns = `id, legal_entity_id, vendor_id, purchase_order_id, currency, status, total, lines, created_by, created_at, coalesce(decided_by, ''), decided_at, coalesce(reject_reason, ''), coalesce(receipt_id::text, ''), coalesce(ledger_entry_id, '')`

func scanSheet(row pgx.Row) (ServiceSheet, error) {
	var s ServiceSheet
	var lines []byte
	if err := row.Scan(&s.ID, &s.LegalEntityID, &s.VendorID, &s.PurchaseOrderID, &s.Currency, &s.Status, &s.Total, &lines, &s.CreatedBy, &s.CreatedAt, &s.DecidedBy, &s.DecidedAt, &s.RejectReason, &s.ReceiptID, &s.LedgerEntryID); err != nil {
		return ServiceSheet{}, err
	}
	return s, json.Unmarshal(lines, &s.Lines)
}

func sheetsOfOrder(ctx context.Context, tx pgx.Tx, tenantID, purchaseOrderID string) ([]ServiceSheet, error) {
	rows, err := tx.Query(ctx, `SELECT `+sheetColumns+` FROM receiving_service_entry_sheet WHERE tenant_id = $1 AND purchase_order_id = $2 AND status <> 'REJECTED'`, tenantID, purchaseOrderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ServiceSheet
	for rows.Next() {
		s, err := scanSheet(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (s *Postgres) CreateSheet(ctx context.Context, in ServiceSheet, limits map[int]decimal.Decimal) (ServiceSheet, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return ServiceSheet{}, err
	}
	lines, err := json.Marshal(in.Lines)
	if err != nil {
		return ServiceSheet{}, err
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, strings.ToLower(tenantID+"|sheets|"+in.PurchaseOrderID)); err != nil {
			return err
		}
		existing, err := sheetsOfOrder(ctx, tx, tenantID, in.PurchaseOrderID)
		if err != nil {
			return err
		}
		if err := CheckSheetLimits(in, existing, limits); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO receiving_service_entry_sheet (tenant_id, id, legal_entity_id, vendor_id, purchase_order_id, currency, status, total, lines, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, 'PENDING', $7, $8, $9) RETURNING created_at`,
			tenantID, in.ID, in.LegalEntityID, in.VendorID, in.PurchaseOrderID, in.Currency, in.Total, lines, in.CreatedBy).Scan(&in.CreatedAt)
	})
	in.Status = domain.SheetPending
	return in, err
}

func (s *Postgres) GetSheet(ctx context.Context, id string) (ServiceSheet, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return ServiceSheet{}, err
	}
	var out ServiceSheet
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanSheet(tx.QueryRow(ctx, `SELECT `+sheetColumns+` FROM receiving_service_entry_sheet WHERE tenant_id = $1 AND id = $2`, tenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ServiceSheet{}, ErrSheetNotFound
	}
	return out, err
}

func (s *Postgres) DecideSheet(ctx context.Context, id, by string, accept bool, reason string, fn SheetDecisionFunc) (ServiceSheet, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return ServiceSheet{}, err
	}
	var out ServiceSheet
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		sheet, err := scanSheet(tx.QueryRow(ctx, `SELECT `+sheetColumns+` FROM receiving_service_entry_sheet WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSheetNotFound
		}
		if err != nil {
			return err
		}
		if err := CheckSheetDecision(sheet, by); err != nil {
			return err
		}
		sheet.Status, sheet.DecidedBy, sheet.RejectReason = domain.SheetRejected, by, reason
		if accept {
			sheet.Status, sheet.RejectReason = domain.SheetAccepted, ""
		}
		evt, err := fn(sheet)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `UPDATE receiving_service_entry_sheet SET status = $3, decided_by = $4, decided_at = now(), reject_reason = NULLIF($5, '')
			WHERE tenant_id = $1 AND id = $2 RETURNING decided_at`, tenantID, id, sheet.Status, by, sheet.RejectReason).Scan(&sheet.DecidedAt); err != nil {
			return err
		}
		out = sheet
		return outbox.Enqueue(ctx, tx, sheetAggregate, id, evt)
	})
	return out, err
}

func (s *Postgres) AcceptSheet(ctx context.Context, id, by string, fn SheetAcceptFunc) (ServiceSheet, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return ServiceSheet{}, err
	}
	var out ServiceSheet
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		sheet, err := scanSheet(tx.QueryRow(ctx, `SELECT `+sheetColumns+` FROM receiving_service_entry_sheet WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSheetNotFound
		}
		if err != nil {
			return err
		}
		if err := CheckSheetDecision(sheet, by); err != nil {
			return err
		}
		sheet.Status, sheet.DecidedBy, sheet.RejectReason = domain.SheetAccepted, by, ""
		allocated := Receipt{ID: ids.New()}
		outcome, err := fn(sheet, allocated)
		if err != nil {
			return err
		}
		number, err := allocateNumber(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		rec := outcome.Receipt
		if rec.ID == "" {
			rec.ID = allocated.ID
		}
		rec.Number = number
		if rec.ExchangeRate.Sign() == 0 {
			rec.ExchangeRate = decimal.NewFromInt(1)
		}
		postedBy := by
		if _, err := tx.Exec(ctx, `INSERT INTO receiving_receipt (tenant_id, id, legal_entity_id, vendor_id, purchase_order_id, packing_slip, state, receipt_date,
			receipt_number, currency, exchange_rate, posting_date, posted_at, ledger_entry_id, posted_by, source, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, 'POSTED', $7, $8, $9, $10, $7, now(), $11, $12, $13, now())`,
			tenantID, rec.ID, sheet.LegalEntityID, sheet.VendorID, sheet.PurchaseOrderID, rec.PackingSlip, rec.Date,
			number, sheet.Currency, rec.ExchangeRate, outcome.LedgerEntryID, postedBy, domain.ReceiptSourceService); err != nil {
			return err
		}
		amounts := map[int]LineAmount{}
		for _, a := range outcome.Amounts {
			amounts[a.LineNo] = a
		}
		var lineRows, grirRows [][]any
		for i, l := range rec.Lines {
			lineNo := l.LineNo
			if lineNo == 0 {
				lineNo = i + 1
			}
			a := amounts[lineNo]
			lineRows = append(lineRows, []any{tenantID, rec.ID, lineNo, sheet.LegalEntityID, l.ProductID, l.Qty, l.UnitPrice, sheet.Currency,
				l.POLineID, l.Unit, l.Qty, l.POLineNo, a.Amount, a.AmountAcct})
			grirRows = append(grirRows, []any{tenantID, ids.New(), sheet.LegalEntityID, rec.ID, lineNo, l.Qty, a.Amount, sheet.Currency, sheet.VendorID, l.POLineID})
		}
		if err := execBatch(ctx, tx, `INSERT INTO receiving_receipt_line (tenant_id, receipt_id, line_no, legal_entity_id, product_id, qty, unit_price, currency,
			po_line_id, unit, ordered_remaining, po_line_no, amount, amount_acct)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9, '')::uuid, NULLIF($10, ''), $11, NULLIF($12, 0), $13, $14)`, lineRows); err != nil {
			return err
		}
		if err := execBatch(ctx, tx, `INSERT INTO receiving_grir_open_item (tenant_id, id, legal_entity_id, receipt_id, line_no, received_qty, received_amount, currency, vendor_id, po_line_id, status)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NULLIF($10, '')::uuid, 'OPEN')`, grirRows); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `UPDATE receiving_service_entry_sheet SET status = 'ACCEPTED', decided_by = $3, decided_at = now(), reject_reason = NULL, receipt_id = $4, ledger_entry_id = $5
			WHERE tenant_id = $1 AND id = $2 RETURNING decided_at`, tenantID, id, by, rec.ID, outcome.LedgerEntryID).Scan(&sheet.DecidedAt); err != nil {
			return err
		}
		sheet.ReceiptID, sheet.LedgerEntryID = rec.ID, outcome.LedgerEntryID
		accepted := outcome.Events
		if outcome.PostedEvent != nil {
			postedEvt, err := outcome.PostedEvent(number)
			if err != nil {
				return err
			}
			accepted = append(slices.Clone(accepted), postedEvt)
		}
		for _, evt := range accepted {
			aggregate, aggregateID := sheetAggregate, id
			if evt.Type == "dev.passage.receiving.receipt.posted.v1" {
				aggregate, aggregateID = receiptAggregate, rec.ID
			}
			if err := outbox.Enqueue(ctx, tx, aggregate, aggregateID, evt); err != nil {
				return err
			}
		}
		if err := markPostingsDone(ctx, tx, tenantID, rec.ID, outcome.LedgerEntryID != ""); err != nil {
			return err
		}
		if err := markPostingsDone(ctx, tx, tenantID, id, outcome.LedgerEntryID != ""); err != nil {
			return err
		}
		out = sheet
		return nil
	})
	return out, err
}

func putSelfBill(ctx context.Context, tx pgx.Tx, tenantID string, in Invoice, build SelfBillBuild) error {
	var seq int
	if err := tx.QueryRow(ctx, `INSERT INTO receiving_self_bill_counter (tenant_id, legal_entity_id, last_no) VALUES ($1, $2, 1)
		ON CONFLICT (tenant_id, legal_entity_id) DO UPDATE SET last_no = receiving_self_bill_counter.last_no + 1 RETURNING last_no`, tenantID, in.LegalEntityID).Scan(&seq); err != nil {
		return err
	}
	doc, evt, err := build(SelfBillNumber(seq))
	if err != nil {
		return err
	}
	if doc.ContentType == "" {
		doc.ContentType = "application/pdf"
	}
	if _, err := tx.Exec(ctx, `INSERT INTO receiving_ers_self_bill (tenant_id, invoice_id, legal_entity_id, vendor_id, invoice_number, document_number, content_type, body, tax_code, tax_amount, total)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		tenantID, in.ID, in.LegalEntityID, in.VendorID, in.InvoiceNumber, doc.Number, doc.ContentType, doc.Body, doc.TaxCode, doc.TaxAmount, doc.Total); err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, SelfBillAggregate, in.ID, evt)
}

func SelfBillNumber(seq int) string { return fmt.Sprintf("SB-%08d", seq) }

func (s *Postgres) GetSelfBill(ctx context.Context, invoiceID string) (SelfBill, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return SelfBill{}, err
	}
	var out SelfBill
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT invoice_id, legal_entity_id, vendor_id, invoice_number, document_number, content_type, body, tax_code, tax_amount, total, created_at
			FROM receiving_ers_self_bill WHERE tenant_id = $1 AND invoice_id = $2`, tenantID, invoiceID).
			Scan(&out.InvoiceID, &out.LegalEntityID, &out.VendorID, &out.InvoiceNumber, &out.Number, &out.ContentType, &out.Body, &out.TaxCode, &out.TaxAmount, &out.Total, &out.CreatedAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return SelfBill{}, ErrSelfBillNotFound
	}
	return out, err
}
