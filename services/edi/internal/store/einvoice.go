package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	ediev "github.com/BenjaminSRussell/passage/services/edi/internal/events"
)

// EInvoice is one e-invoice of the store: the document built for one mandate from one billing invoice and what the network said.
type EInvoice struct {
	ID            string         `json:"id"`
	LegalEntityID string         `json:"legalEntityId"`
	InvoiceID     string         `json:"invoiceId"`
	Number        string         `json:"number"`
	Mandate       string         `json:"mandate"`
	Format        string         `json:"format"`
	DocumentType  string         `json:"documentType"`
	Status        string         `json:"status"`
	XML           string         `json:"xml,omitempty"`
	Issues        []domain.Issue `json:"issues"`
	Reference     string         `json:"reference,omitempty"`
	ResponseText  string         `json:"responseText,omitempty"`
	Version       int            `json:"version"`
	CreatedAt     time.Time      `json:"createdAt"`
	UpdatedAt     time.Time      `json:"updatedAt"`
}

// ErrEInvoiceState means the e-invoice is not in a status the action needs.
var ErrEInvoiceState = errors.New("edi: e-invoice is not in the right status")

// PutEInvoiceParty stores the seller (subject = the legal entity) or a buyer (subject = the customer) profile of a legal entity.
func (s *Store) PutEInvoiceParty(ctx context.Context, legalEntityID, role, subjectID string, party json.RawMessage) error {
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `insert into einvoice_party (tenant_id, legal_entity_id, role, subject_id, party) values ($1,$2,$3,$4,$5)
			on conflict (tenant_id, legal_entity_id, role, subject_id) do update set party = excluded.party, updated_at = now()`,
			tid, legalEntityID, role, subjectID, []byte(party))
		return err
	})
}

// GetEInvoiceParty returns a stored profile or ErrNotFound.
func (s *Store) GetEInvoiceParty(ctx context.Context, legalEntityID, role, subjectID string) (json.RawMessage, error) {
	var raw []byte
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `select party from einvoice_party where legal_entity_id = $1 and role = $2 and subject_id = $3`,
			legalEntityID, role, subjectID).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return raw, err
}

const einvoiceColumns = `select id::text, legal_entity_id::text, source_invoice_id::text, source_number, mandate, format, document_type, status,
	coalesce(xml, ''), issues, coalesce(submission_ref, ''), coalesce(response_text, ''), version, created_at, updated_at from einvoice`

func scanEInvoice(row pgx.Row, e *EInvoice) error {
	var issues []byte
	err := row.Scan(&e.ID, &e.LegalEntityID, &e.InvoiceID, &e.Number, &e.Mandate, &e.Format, &e.DocumentType, &e.Status, &e.XML, &issues,
		&e.Reference, &e.ResponseText, &e.Version, &e.CreatedAt, &e.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	e.Issues = []domain.Issue{}
	return json.Unmarshal(issues, &e.Issues)
}

// SaveEInvoice stores the built document of an invoice for a mandate: VALID with its XML, or INVALID with the blocking issues. An
// e-invoice already stored for the (invoice, mandate) is returned as it is (created false), except an INVALID one, which a new
// build replaces: that is how a missing party profile is repaired and the invoice built again.
func (s *Store) SaveEInvoice(ctx context.Context, e EInvoice) (EInvoice, bool, error) {
	out := e
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		issues, err := json.Marshal(e.Issues)
		if err != nil {
			return err
		}
		out.ID = ids.New()
		err = scanEInvoice(tx.QueryRow(ctx, `insert into einvoice (tenant_id, id, legal_entity_id, source_invoice_id, source_number, mandate, format, document_type, status, xml, issues)
			values ($1,$2,$3,$4,$5,$6,$7,$8,$9,nullif($10,''),$11)
			on conflict (tenant_id, legal_entity_id, source_invoice_id, mandate) do update set source_number = excluded.source_number, format = excluded.format,
			document_type = excluded.document_type, status = excluded.status, xml = excluded.xml, issues = excluded.issues, version = einvoice.version + 1, updated_at = now()
			where einvoice.status = 'INVALID'
			returning `+einvoiceReturning, tid, out.ID, e.LegalEntityID, e.InvoiceID, e.Number, e.Mandate, e.Format, e.DocumentType, e.Status, e.XML, issues), &out)
		if errors.Is(err, ErrNotFound) {
			// The conflict row was not INVALID: return what is stored.
			return scanEInvoice(tx.QueryRow(ctx, einvoiceColumns+` where legal_entity_id = $1 and source_invoice_id = $2 and mandate = $3`,
				e.LegalEntityID, e.InvoiceID, e.Mandate), &out)
		}
		if err != nil {
			return err
		}
		created = out.Version == 1
		return nil
	})
	return out, created, err
}

const einvoiceReturning = `id::text, legal_entity_id::text, source_invoice_id::text, source_number, mandate, format, document_type, status,
	coalesce(xml, ''), issues, coalesce(submission_ref, ''), coalesce(response_text, ''), version, created_at, updated_at`

// GetEInvoice returns one e-invoice or ErrNotFound.
func (s *Store) GetEInvoice(ctx context.Context, id string) (EInvoice, error) {
	var out EInvoice
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return scanEInvoice(tx.QueryRow(ctx, einvoiceColumns+` where id = $1`, id), &out)
	})
	return out, err
}

// ListEInvoices lists e-invoices newest first without their XML.
func (s *Store) ListEInvoices(ctx context.Context, status, mandate string, limit int) ([]EInvoice, error) {
	out := []EInvoice{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, einvoiceColumns+` where ($1 = '' or status = $1) and ($2 = '' or mandate = $2) order by created_at desc, id limit $3`, status, mandate, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e EInvoice
			if err := scanEInvoice(rows, &e); err != nil {
				return err
			}
			e.XML = ""
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

// MarkEInvoiceSubmitted records that the document was handed to its network (VALID or REJECTED to SUBMITTED) and publishes
// einvoice.submitted. The reference is the network's.
func (s *Store) MarkEInvoiceSubmitted(ctx context.Context, id, reference string) (EInvoice, error) {
	return s.moveEInvoice(ctx, id, []string{"VALID", "REJECTED"}, "SUBMITTED", reference, "", ediev.EInvoiceSubmitted)
}

// RecordEInvoiceResponse records the network's answer to a submitted document: ACCEPTED or REJECTED with its text.
func (s *Store) RecordEInvoiceResponse(ctx context.Context, id string, accepted bool, reference, text string) (EInvoice, error) {
	status, typ := "REJECTED", ediev.EInvoiceRejected
	if accepted {
		status, typ = "ACCEPTED", ediev.EInvoiceAccepted
	}
	return s.moveEInvoice(ctx, id, []string{"SUBMITTED"}, status, reference, text, typ)
}

func (s *Store) moveEInvoice(ctx context.Context, id string, from []string, to, reference, text, eventType string) (EInvoice, error) {
	var out EInvoice
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		if err := scanEInvoice(tx.QueryRow(ctx, einvoiceColumns+` where id = $1 for update`, id), &out); err != nil {
			return err
		}
		ok := false
		for _, f := range from {
			ok = ok || out.Status == f
		}
		if !ok {
			return ErrEInvoiceState
		}
		if reference == "" {
			reference = out.Reference
		}
		if err := scanEInvoice(tx.QueryRow(ctx, `update einvoice set status = $2, submission_ref = nullif($3,''), response_text = nullif($4,''), version = version + 1, updated_at = now()
			where id = $1 returning `+einvoiceReturning, id, to, reference, text), &out); err != nil {
			return err
		}
		return enqueueEvent(ctx, tx, tid, eventType, "einvoice", out.ID, ediev.EInvoiceData{EInvoiceID: out.ID, LegalEntityID: out.LegalEntityID,
			InvoiceID: out.InvoiceID, Number: out.Number, Mandate: out.Mandate, Reference: out.Reference, Reason: text})
	})
	return out, err
}
