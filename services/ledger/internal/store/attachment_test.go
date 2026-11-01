package store

import (
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	ledgerevents "github.com/BenjaminSRussell/passage/services/ledger/internal/events"
)

func fileOf(e env, name string) domain.Attachment {
	return domain.Attachment{Kind: domain.KindAttachment, Name: name, DocumentType: "invoice_image", StoreRef: e.tenant + "/journal-batch/vouchers/" + name,
		ContentType: "application/pdf", SizeBytes: 4096}
}

func attachmentEvents(t *testing.T, e env, batchID string) int {
	t.Helper()
	var n int
	err := dbx.WithTenantTx(e.ctx, e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(e.ctx, `select count(*) from outbox where type = $1 and aggregate_type = 'batch_attachment'
			and payload->'data'->>'batchId' = $2`, ledgerevents.BatchAttachmentChanged, batchID).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// FR-11.11, FR-11.18: files and notes attach to a draft batch, are immutable once it is posted, are retained
// for their class from the posting date and stay readable from the posted entry.
func TestPostgres_BatchAttachmentsLifecycleAndRetention(t *testing.T) {
	e, _ := setup(t)
	putName(t, e, "GJ", JournalName{Active: true})
	b, err := e.store.CreateBatch(e.ctx, batchOf(e, "GJ", "125"))
	if err != nil {
		t.Fatal(err)
	}
	fileID, noteID, goneID := ids.New(), ids.New(), ids.New()

	a, created, err := e.store.PutBatchAttachment(e.ctx, b.ID, fileID, fileOf(e, "invoice.pdf"))
	if err != nil || !created || a.RetentionClass != "financial_record_10y" || a.RetainedUntil != nil || a.CreatedBy != "test-user" {
		t.Fatalf("add file: %+v created=%v err=%v", a, created, err)
	}
	// The same content again is the same row; other content under the id is a conflict.
	if again, created, err := e.store.PutBatchAttachment(e.ctx, b.ID, fileID, fileOf(e, "invoice.pdf")); err != nil || created || again.ID != fileID {
		t.Fatalf("replay: %+v created=%v err=%v", again, created, err)
	}
	if _, _, err := e.store.PutBatchAttachment(e.ctx, b.ID, fileID, fileOf(e, "other.pdf")); !isProblem(err, http.StatusConflict, domain.CodeAttachmentConflict) {
		t.Fatalf("changed content under the id: %v", err)
	}
	if _, _, err := e.store.PutBatchAttachment(e.ctx, b.ID, noteID, domain.Attachment{Kind: domain.KindNote, Note: "  Checked with the buyer "}); err != nil {
		t.Fatalf("add note: %v", err)
	}
	bad := fileOf(e, "x.exe")
	bad.ContentType = "application/x-msdownload"
	if _, _, err := e.store.PutBatchAttachment(e.ctx, b.ID, ids.New(), bad); len(rejectionCodes(err)) != 1 || rejectionCodes(err)[0] != domain.CodeAttachmentInvalid {
		t.Fatalf("invalid file: %v", err)
	}
	// The store reference cannot name another tenant's object or an arbitrary host.
	for _, ref := range []string{testkit.TenantID(2) + "/journal-batch/x.pdf", "https://169.254.169.254/latest/meta-data", "vouchers/x.pdf"} {
		f := fileOf(e, "x.pdf")
		f.StoreRef = ref
		if _, _, err := e.store.PutBatchAttachment(e.ctx, b.ID, ids.New(), f); len(rejectionCodes(err)) != 1 || rejectionCodes(err)[0] != domain.CodeAttachmentInvalid {
			t.Fatalf("store reference %q: %v", ref, err)
		}
	}
	if _, _, err := e.store.PutBatchAttachment(e.ctx, ids.New(), ids.New(), fileOf(e, "a.pdf")); !isProblem(err, http.StatusNotFound, CodeBatchNotFound) {
		t.Fatalf("unknown batch: %v", err)
	}

	// Before posting a file can be removed freely.
	if _, _, err := e.store.PutBatchAttachment(e.ctx, b.ID, goneID, fileOf(e, "draft.pdf")); err != nil {
		t.Fatal(err)
	}
	if err := e.store.DeleteBatchAttachment(e.ctx, b.ID, goneID, time.Now()); err != nil {
		t.Fatalf("delete before posting: %v", err)
	}
	if err := e.store.DeleteBatchAttachment(e.ctx, b.ID, goneID, time.Now()); !isProblem(err, http.StatusNotFound, domain.CodeAttachmentNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
	list, err := e.store.ListBatchAttachments(e.ctx, b.ID)
	if err != nil || len(list) != 2 || list[0].ID != fileID || list[1].Kind != domain.KindNote || list[1].Note != "Checked with the buyer" {
		t.Fatalf("list: %+v %v", list, err)
	}

	b, err = e.store.SubmitBatch(e.ctx, b.ID, 1, testToday)
	if err != nil {
		t.Fatal(err)
	}
	res, err := postBatch(e, e.ctx, b, "ATT-POST-1", true)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	entryID := res.Entries[0].ID

	// Posted: nothing is added, retained-until is derived, deletes are refused until the class has run.
	if _, _, err := e.store.PutBatchAttachment(e.ctx, b.ID, ids.New(), fileOf(e, "late.pdf")); !isProblem(err, http.StatusConflict, CodeBatchState) {
		t.Fatalf("add after posting: %v", err)
	}
	if again, created, err := e.store.PutBatchAttachment(e.ctx, b.ID, fileID, fileOf(e, "invoice.pdf")); err != nil || created || again.RetainedUntil == nil {
		t.Fatalf("replay after posting must still answer: %+v %v", again, err)
	}
	fromEntry, err := e.store.ListEntryAttachments(e.ctx, entryID)
	if err != nil || len(fromEntry) != 2 {
		t.Fatalf("readable from the posted entry: %+v %v", fromEntry, err)
	}
	want := time.Now().UTC().AddDate(10, 0, 0).Format(time.DateOnly)
	if fromEntry[0].RetainedUntil == nil || *fromEntry[0].RetainedUntil != want || fromEntry[1].RetainedUntil != nil {
		t.Fatalf("retainedUntil: file %v (want %s), note %v", fromEntry[0].RetainedUntil, want, fromEntry[1].RetainedUntil)
	}
	if err := e.store.DeleteBatchAttachment(e.ctx, b.ID, fileID, time.Now()); !isProblem(err, http.StatusConflict, domain.CodeAttachmentRetained) {
		t.Fatalf("delete during retention: %v", err)
	}
	if err := e.store.DeleteBatchAttachment(e.ctx, b.ID, noteID, time.Now().AddDate(20, 0, 0)); !isProblem(err, http.StatusConflict, CodeBatchState) {
		t.Fatalf("notes of a posted batch are immutable: %v", err)
	}
	if err := e.store.DeleteBatchAttachment(e.ctx, b.ID, fileID, time.Now().AddDate(10, 0, 2)); err != nil {
		t.Fatalf("delete after retention: %v", err)
	}
	// added x3 (file, note, draft file), removed x2 (draft file, file after retention).
	if n := attachmentEvents(t, e, b.ID); n != 5 {
		t.Fatalf("attachment events = %d", n)
	}
	// An entry that did not come from a batch has none; an unknown entry is 404.
	if _, err := e.store.ListEntryAttachments(e.ctx, ids.New()); !isProblem(err, http.StatusNotFound, "LEDGER_ENTRY_NOT_FOUND") {
		t.Fatalf("unknown entry: %v", err)
	}
}

func TestPostgres_BatchAttachmentLimitsAndCancelledBatch(t *testing.T) {
	e, _ := setup(t)
	putName(t, e, "GJ", JournalName{Active: true})
	b, err := e.store.CreateBatch(e.ctx, batchOf(e, "GJ", "10"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < domain.MaxAttachmentsPerBatch; i++ {
		if _, _, err := e.store.PutBatchAttachment(e.ctx, b.ID, ids.New(), fileOf(e, "f.pdf")); err != nil {
			t.Fatalf("file %d: %v", i, err)
		}
	}
	if _, _, err := e.store.PutBatchAttachment(e.ctx, b.ID, ids.New(), fileOf(e, "f51.pdf")); !isProblem(err, http.StatusConflict, domain.CodeAttachmentLimit) {
		t.Fatalf("51st file: %v", err)
	}
	// Notes have their own limit.
	if _, _, err := e.store.PutBatchAttachment(e.ctx, b.ID, ids.New(), domain.Attachment{Kind: domain.KindNote, Note: "ok"}); err != nil {
		t.Fatalf("note beside 50 files: %v", err)
	}
	if _, err := e.store.CancelBatch(e.ctx, b.ID, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.store.PutBatchAttachment(e.ctx, b.ID, ids.New(), domain.Attachment{Kind: domain.KindNote, Note: "late"}); !isProblem(err, http.StatusConflict, CodeBatchState) {
		t.Fatalf("cancelled batch: %v", err)
	}
}

// Another tenant's or legal entity's batch is invisible: RLS turns it into a not-found.
func TestPostgres_BatchAttachmentsAreTenantAndLegalEntityScoped(t *testing.T) {
	e, _ := setup(t)
	putName(t, e, "GJ", JournalName{Active: true})
	b, err := e.store.CreateBatch(e.ctx, batchOf(e, "GJ", "10"))
	if err != nil {
		t.Fatal(err)
	}
	id := ids.New()
	if _, _, err := e.store.PutBatchAttachment(e.ctx, b.ID, id, fileOf(e, "a.pdf")); err != nil {
		t.Fatal(err)
	}
	other := principalCtx(testkit.TenantID(2), "")
	if _, err := e.store.ListBatchAttachments(other, b.ID); !isProblem(err, http.StatusNotFound, CodeBatchNotFound) {
		t.Fatalf("other tenant list: %v", err)
	}
	if err := e.store.DeleteBatchAttachment(other, b.ID, id, time.Now()); !isProblem(err, http.StatusNotFound, CodeBatchNotFound) {
		t.Fatalf("other tenant delete: %v", err)
	}
	var n int
	err = dbx.WithTenantTx(other, e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(other, `select count(*) from batch_attachment`).Scan(&n)
	})
	if err != nil || n != 0 {
		t.Fatalf("RLS leak to another tenant: n=%d err=%v", n, err)
	}
	otherEntity := principalCtx(e.tenant, ids.New())
	if _, err := e.store.ListBatchAttachments(otherEntity, b.ID); !isProblem(err, http.StatusNotFound, CodeBatchNotFound) {
		t.Fatalf("other legal entity list: %v", err)
	}
	if _, _, err := e.store.PutBatchAttachment(otherEntity, b.ID, ids.New(), fileOf(e, "b.pdf")); !isProblem(err, http.StatusNotFound, CodeBatchNotFound) {
		t.Fatalf("other legal entity add: %v", err)
	}
	err = dbx.WithTenantTx(otherEntity, e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(otherEntity, `select count(*) from batch_attachment`).Scan(&n)
	})
	if err != nil || n != 0 {
		t.Fatalf("RLS leak to another legal entity: n=%d err=%v", n, err)
	}
}
