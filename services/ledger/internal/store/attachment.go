package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	ledgerevents "github.com/BenjaminSRussell/passage/services/ledger/internal/events"
)

const attachmentAggregate = "batch_attachment"

// attachmentSelect joins the batch so retained-until follows its posting date.
const attachmentSelect = `select a.id::text, a.batch_id::text, a.kind, a.name, a.document_type, a.retention_class, a.store_ref,
		a.content_type, a.size_bytes, a.note, a.created_by, a.created_at, b.posted_at
	from batch_attachment a
	join journal_batch b on b.tenant_id = a.tenant_id and b.id = a.batch_id`

func scanAttachment(row pgx.Row) (domain.Attachment, error) {
	var a domain.Attachment
	var postedAt *time.Time
	if err := row.Scan(&a.ID, &a.BatchID, &a.Kind, &a.Name, &a.DocumentType, &a.RetentionClass, &a.StoreRef, &a.ContentType,
		&a.SizeBytes, &a.Note, &a.CreatedBy, &a.CreatedAt, &postedAt); err != nil {
		return a, err
	}
	if until, ok := retainedUntil(a, postedAt); ok {
		d := until.Format(time.DateOnly)
		a.RetainedUntil = &d
	}
	return a, nil
}

// retainedUntil is set for a file whose batch has been posted: the class's years from the posting date.
func retainedUntil(a domain.Attachment, postedAt *time.Time) (time.Time, bool) {
	if a.Kind != domain.KindAttachment || postedAt == nil {
		return time.Time{}, false
	}
	return domain.RetainedUntil(postedAt.UTC(), a.RetentionClass), true
}

func attachmentNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, domain.CodeAttachmentNotFound, "attachment not found")
}

func listAttachmentsTx(ctx context.Context, tx pgx.Tx, tenantID, batchID string) ([]domain.Attachment, error) {
	rows, err := tx.Query(ctx, attachmentSelect+` where a.tenant_id = $1 and a.batch_id = $2 order by a.created_at, a.id`, tenantID, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Attachment{}
	for rows.Next() {
		a, err := scanAttachment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListBatchAttachments returns the attachments and notes of a batch, oldest first.
func (s *Postgres) ListBatchAttachments(ctx context.Context, batchID string) ([]domain.Attachment, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return nil, err
	}
	var out []domain.Attachment
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `select count(*) from journal_batch where tenant_id = $1 and id = $2`, p.TenantID, batchID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return httpx.NewProblem(http.StatusNotFound, CodeBatchNotFound, "journal batch not found")
		}
		out, err = listAttachmentsTx(ctx, tx, p.TenantID, batchID)
		return err
	})
	return out, err
}

// ListEntryAttachments returns the attachments and notes of the batch that was posted as the entry
// (FR-11.18: they stay readable from the posted entry). An entry that did not come from a batch has none.
func (s *Postgres) ListEntryAttachments(ctx context.Context, entryID string) ([]domain.Attachment, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return nil, err
	}
	out := []domain.Attachment{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `select count(*) from journal_entry where tenant_id = $1 and id = $2`, p.TenantID, entryID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return httpx.NewProblem(http.StatusNotFound, "LEDGER_ENTRY_NOT_FOUND", "journal entry not found")
		}
		var batchID string
		err := tx.QueryRow(ctx, `select id::text from journal_batch where tenant_id = $1 and posted_entry_id = $2`, p.TenantID, entryID).Scan(&batchID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		out, err = listAttachmentsTx(ctx, tx, p.TenantID, batchID)
		return err
	})
	return out, err
}

// lockBatchForAttachment locks the batch row (serializing with post and cancel) and returns what the
// attachment rules need. Only an editable batch accepts new attachments.
func lockBatchForAttachment(ctx context.Context, tx pgx.Tx, tenantID, batchID string) (ledgerID, legalEntityID, status string, postedAt *time.Time, err error) {
	err = tx.QueryRow(ctx, `select ledger_id::text, legal_entity_id::text, status, posted_at from journal_batch
		where tenant_id = $1 and id = $2 for update`, tenantID, batchID).Scan(&ledgerID, &legalEntityID, &status, &postedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = httpx.NewProblem(http.StatusNotFound, CodeBatchNotFound, "journal batch not found")
	}
	return
}

// PutBatchAttachment adds an attachment or note under a client-chosen id. Repeating the request with
// the same content answers the stored row (created = false); other content under the id is 409
// LEDGER_ATTACHMENT_CONFLICT. A posted or cancelled batch is immutable (409 LEDGER_BATCH_STATE).
func (s *Postgres) PutBatchAttachment(ctx context.Context, batchID, id string, in domain.Attachment) (domain.Attachment, bool, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return domain.Attachment{}, false, err
	}
	in.Normalize()
	if vs := domain.ValidateAttachment(in, p.TenantID); len(vs) > 0 {
		return domain.Attachment{}, false, &domain.RejectedError{Violations: vs}
	}
	var out domain.Attachment
	created := false
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		ledgerID, le, status, _, err := lockBatchForAttachment(ctx, tx, p.TenantID, batchID)
		if err != nil {
			return err
		}
		cur, err := scanAttachment(tx.QueryRow(ctx, attachmentSelect+` where a.tenant_id = $1 and a.id = $2`, p.TenantID, id))
		if err == nil {
			if cur.BatchID != batchID || !domain.SameAttachment(cur, in) {
				return httpx.NewProblem(http.StatusConflict, domain.CodeAttachmentConflict, "an attachment with this id already exists with other content")
			}
			out = cur
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if status == domain.BatchPosted || status == domain.BatchCancelled {
			return httpx.NewProblem(http.StatusConflict, CodeBatchState,
				"a "+status+" batch is immutable; its attachments and notes can't be added")
		}
		limit := domain.MaxAttachmentsPerBatch
		if in.Kind == domain.KindNote {
			limit = domain.MaxNotesPerBatch
		}
		var n int
		if err := tx.QueryRow(ctx, `select count(*) from batch_attachment where tenant_id = $1 and batch_id = $2 and kind = $3`,
			p.TenantID, batchID, in.Kind).Scan(&n); err != nil {
			return err
		}
		if n >= limit {
			return httpx.NewProblem(http.StatusConflict, domain.CodeAttachmentLimit,
				fmt.Sprintf("a batch has at most %d %ss", limit, in.Kind))
		}
		if _, err := tx.Exec(ctx, `insert into batch_attachment (tenant_id, id, batch_id, legal_entity_id, kind, name, document_type,
				retention_class, store_ref, content_type, size_bytes, note, created_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
			p.TenantID, id, batchID, le, in.Kind, in.Name, in.DocumentType, in.RetentionClass, in.StoreRef, in.ContentType, in.SizeBytes,
			in.Note, p.UserID); err != nil {
			if isUnique(err) {
				return httpx.NewProblem(http.StatusConflict, domain.CodeAttachmentConflict, "an attachment with this id already exists")
			}
			return err
		}
		created = true
		out, err = scanAttachment(tx.QueryRow(ctx, attachmentSelect+` where a.tenant_id = $1 and a.id = $2`, p.TenantID, id))
		if err != nil {
			return err
		}
		return enqueueFor(ctx, tx, attachmentAggregate, id, ledgerevents.BatchAttachmentChanged, p.TenantID, ledgerevents.BatchAttachmentChangedData{
			BatchID: batchID, LedgerID: ledgerID, AttachmentID: id, Kind: in.Kind, Action: "added", Actor: p.UserID})
	})
	return out, created, err
}

// DeleteBatchAttachment removes an attachment or note. Before the batch is posted it can be removed
// freely; after posting a file is refused with 409 LEDGER_ATTACHMENT_RETAINED until its retention
// class has run, and notes are immutable (409 LEDGER_BATCH_STATE).
func (s *Postgres) DeleteBatchAttachment(ctx context.Context, batchID, id string, today time.Time) error {
	p, err := principalOf(ctx)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		ledgerID, _, status, postedAt, err := lockBatchForAttachment(ctx, tx, p.TenantID, batchID)
		if err != nil {
			return err
		}
		cur, err := scanAttachment(tx.QueryRow(ctx, attachmentSelect+` where a.tenant_id = $1 and a.batch_id = $2 and a.id = $3`, p.TenantID, batchID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return attachmentNotFound()
		}
		if err != nil {
			return err
		}
		if status == domain.BatchPosted {
			until, ok := retainedUntil(cur, postedAt)
			if !ok {
				return httpx.NewProblem(http.StatusConflict, CodeBatchState, "a posted batch is immutable; its notes can't be removed")
			}
			if today.UTC().Before(until) {
				return httpx.NewProblem(http.StatusConflict, domain.CodeAttachmentRetained,
					fmt.Sprintf("Attachment %s is retained until %s and cannot be deleted.", cur.Name, until.Format(time.DateOnly)))
			}
		}
		if _, err := tx.Exec(ctx, `delete from batch_attachment where tenant_id = $1 and id = $2`, p.TenantID, id); err != nil {
			return err
		}
		return enqueueFor(ctx, tx, attachmentAggregate, id, ledgerevents.BatchAttachmentChanged, p.TenantID, ledgerevents.BatchAttachmentChangedData{
			BatchID: batchID, LedgerID: ledgerID, AttachmentID: id, Kind: cur.Kind, Action: "removed", Actor: p.UserID})
	})
}
