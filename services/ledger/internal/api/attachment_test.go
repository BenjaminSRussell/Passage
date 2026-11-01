package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
)

const attachmentTestID = "00000000-0000-4000-8000-0000000000d1"

type attachFake struct {
	put     []domain.Attachment
	isNew   bool
	deleted []string
	today   time.Time
}

func (f *fakeStore) ListBatchAttachments(context.Context, string) ([]domain.Attachment, error) {
	return []domain.Attachment{{ID: attachmentTestID, Kind: domain.KindNote, Note: "n"}}, f.err
}

func (f *fakeStore) ListEntryAttachments(context.Context, string) ([]domain.Attachment, error) {
	return []domain.Attachment{}, f.err
}

func (f *fakeStore) PutBatchAttachment(_ context.Context, batchID, id string, in domain.Attachment) (domain.Attachment, bool, error) {
	f.attach.put = append(f.attach.put, in)
	in.ID, in.BatchID = id, batchID
	return in, f.attach.isNew, f.err
}

func (f *fakeStore) DeleteBatchAttachment(_ context.Context, _, id string, today time.Time) error {
	f.attach.deleted = append(f.attach.deleted, id)
	f.attach.today = today
	return f.err
}

func TestPutBatchAttachment_MapsAndEnforcesDuty(t *testing.T) {
	fs := &fakeStore{}
	fs.attach.isNew = true
	h := server(fs)
	path := batchPath + "/attachments/" + attachmentTestID
	body := `{"kind":"attachment","name":"inv.pdf","documentType":"invoice_image","storeRef":"vouchers/inv.pdf","contentType":"application/pdf","sizeBytes":1200}`
	if rec := call(h, http.MethodPut, path, body, dutyRead, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("read duty must not write: %d", rec.Code)
	}
	rec := call(h, http.MethodPut, path, body, dutyJournalWrite, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	got := fs.attach.put[0]
	if got.Kind != "attachment" || got.Name != "inv.pdf" || got.DocumentType != "invoice_image" || got.StoreRef != "vouchers/inv.pdf" || got.SizeBytes != 1200 {
		t.Fatalf("not mapped: %+v", got)
	}
	fs.attach.isNew = false
	if rec := call(h, http.MethodPut, path, body, dutyJournalWrite, nil); rec.Code != http.StatusOK {
		t.Fatalf("replay: %d", rec.Code)
	}
	if rec := call(h, http.MethodPut, batchPath+"/attachments/nope", body, dutyJournalWrite, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", rec.Code)
	}
	if rec := call(h, http.MethodPut, path, `{"kind":"note","bogus":1}`, dutyJournalWrite, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", rec.Code)
	}
}

func TestBatchAttachment_RejectionsAndReads(t *testing.T) {
	fs := &fakeStore{err: &domain.RejectedError{Violations: []domain.Violation{{Code: domain.CodeAttachmentInvalid, Field: "note", Message: "x"}}}}
	h := server(fs)
	path := batchPath + "/attachments/" + attachmentTestID
	if rec := call(h, http.MethodPut, path, `{"kind":"note"}`, dutyJournalWrite, nil); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("violations are 422: %d", rec.Code)
	}
	fs.err = nil
	if rec := call(h, http.MethodGet, batchPath+"/attachments", "", dutyRead, nil); rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, "/v1/ledger/journal-entries/"+testBatch+"/attachments", "", dutyRead, nil); rec.Code != http.StatusOK {
		t.Fatalf("entry list: %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, batchPath+"/attachments", "", "", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("list needs the read duty: %d", rec.Code)
	}
}

func TestDeleteBatchAttachment_PassesTodayAndMaps204(t *testing.T) {
	fs := &fakeStore{}
	h := server(fs)
	path := batchPath + "/attachments/" + attachmentTestID
	if rec := call(h, http.MethodDelete, path, "", dutyJournalWrite, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	if len(fs.attach.deleted) != 1 || fs.attach.today.Year() != 2026 {
		t.Fatalf("not passed: %+v", fs.attach)
	}
	if rec := call(h, http.MethodDelete, batchPath+"/attachments/x", "", dutyJournalWrite, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", rec.Code)
	}
}
