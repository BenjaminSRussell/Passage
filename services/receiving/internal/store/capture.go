package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

var ErrCaptureNotFound = errors.New("invoice capture not found")

type Capture struct {
	ID             string
	LegalEntityID  string
	Source         string
	Status         string
	DocumentSHA256 string
	Reasons        []string
	CreatedBy      string
	CreatedAt      time.Time
	Invoice        domain.ExtractedInvoice
}

const captureColumns = `id, legal_entity_id, source, status, document_sha256, vendor_tax_id, invoice_number, invoice_date, currency, total, confidence, review_reasons, lines, created_by, created_at`

func scanCapture(row pgx.Row) (Capture, error) {
	var c Capture
	var reasons, lines []byte
	err := row.Scan(&c.ID, &c.LegalEntityID, &c.Source, &c.Status, &c.DocumentSHA256, &c.Invoice.VendorTaxID, &c.Invoice.InvoiceNumber, &c.Invoice.InvoiceDate,
		&c.Invoice.Currency, &c.Invoice.Total, &c.Invoice.Confidence, &reasons, &lines, &c.CreatedBy, &c.CreatedAt)
	if err != nil {
		return Capture{}, err
	}
	if err := json.Unmarshal(reasons, &c.Reasons); err != nil {
		return Capture{}, err
	}
	return c, json.Unmarshal(lines, &c.Invoice.Lines)
}

func (s *Postgres) CreateCapture(ctx context.Context, in Capture) (out Capture, created bool, err error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return Capture{}, false, err
	}
	reasons, err := json.Marshal(in.Reasons)
	if err != nil {
		return Capture{}, false, err
	}
	lines, err := json.Marshal(in.Invoice.Lines)
	if err != nil {
		return Capture{}, false, err
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO receiving_invoice_capture (tenant_id, id, legal_entity_id, source, status, document_sha256, vendor_tax_id, invoice_number, invoice_date, currency, total, confidence, review_reasons, lines, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15) ON CONFLICT (tenant_id, legal_entity_id, document_sha256) DO NOTHING`,
			tenantID, in.ID, in.LegalEntityID, in.Source, in.Status, in.DocumentSHA256, in.Invoice.VendorTaxID, in.Invoice.InvoiceNumber, in.Invoice.InvoiceDate,
			in.Invoice.Currency, in.Invoice.Total, in.Invoice.Confidence, reasons, lines, in.CreatedBy)
		if err != nil {
			return err
		}
		created = tag.RowsAffected() == 1
		out, err = scanCapture(tx.QueryRow(ctx, `SELECT `+captureColumns+` FROM receiving_invoice_capture WHERE tenant_id = $1 AND legal_entity_id = $2 AND document_sha256 = $3`,
			tenantID, in.LegalEntityID, in.DocumentSHA256))
		return err
	})
	return out, created, err
}

func (s *Postgres) GetCapture(ctx context.Context, id string) (Capture, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return Capture{}, err
	}
	var out Capture
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanCapture(tx.QueryRow(ctx, `SELECT `+captureColumns+` FROM receiving_invoice_capture WHERE tenant_id = $1 AND id = $2`, tenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Capture{}, ErrCaptureNotFound
	}
	return out, err
}

func (s *Postgres) FindCapture(ctx context.Context, legalEntityID, digest string) (Capture, bool, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return Capture{}, false, err
	}
	var out Capture
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanCapture(tx.QueryRow(ctx, `SELECT `+captureColumns+` FROM receiving_invoice_capture WHERE tenant_id = $1 AND legal_entity_id = $2 AND document_sha256 = $3`, tenantID, legalEntityID, digest))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Capture{}, false, nil
	}
	return out, err == nil, err
}

func (s *Postgres) ListCaptures(ctx context.Context, f CaptureFilter) ([]Capture, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	limit := f.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	limit = min(limit, maxListLimit)
	var cursorID any
	if !f.CursorCreatedAt.IsZero() {
		cursorID = f.CursorID
	}
	var out []Capture
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+captureColumns+` FROM receiving_invoice_capture c
			WHERE tenant_id = $1 AND ($2 = '' OR status = $2)
			AND NOT EXISTS (SELECT 1 FROM receiving_vendor_invoice i WHERE i.tenant_id = c.tenant_id AND i.legal_entity_id = c.legal_entity_id
				AND lower(i.invoice_number) = lower(c.invoice_number) AND i.invoice_date = c.invoice_date AND i.currency = c.currency)
			AND ($3::timestamptz IS NULL OR (created_at, id) < ($3::timestamptz, $4::uuid))
			ORDER BY created_at DESC, id DESC LIMIT $5`, tenantID, f.Status, nullTime(f.CursorCreatedAt), cursorID, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanCapture(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}
