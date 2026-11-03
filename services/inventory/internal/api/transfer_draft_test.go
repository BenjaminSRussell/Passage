package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
)

func (f *fakeStore) ConfirmTransfer(_ context.Context, id string, version int) (domain.TransferOrder, error) {
	f.transferVersion = version
	if f.err != nil {
		return domain.TransferOrder{}, f.err
	}
	return domain.TransferOrder{ID: id, Status: domain.TransferCreated, Version: version + 1}, nil
}

func (f *fakeStore) DeleteTransfer(_ context.Context, _ string, version int) error {
	f.transferVersion = version
	return f.err
}

func TestCreateTransferDraftAndDeliveryFields(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	body := strings.Replace(transferBody, `"shipDate"`, `"draft":true,"deliveryTerms":"FCA","modeOfDelivery":"ROAD","shipDate"`, 1)
	rec := do(t, h, http.MethodPost, transferOrdersPathForTest, body, []string{"inventory.transfer.write"}, map[string]string{"Idempotency-Key": "k-draft"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	in := fs.transferIn
	if in.Status != domain.TransferDraft || in.DeliveryTerms != "FCA" || in.ModeOfDelivery != "ROAD" {
		t.Fatalf("order passed to the store: %+v", in)
	}
	long := strings.Replace(transferBody, `"shipDate"`, `"deliveryTerms":"`+strings.Repeat("X", 21)+`","shipDate"`, 1)
	rec = do(t, h, http.MethodPost, transferOrdersPathForTest, long, []string{"inventory.transfer.write"}, map[string]string{"Idempotency-Key": "k-long"})
	if rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "deliveryTerms") {
		t.Fatalf("long delivery terms: %d %s", rec.Code, rec.Body.String())
	}
	plain := do(t, h, http.MethodPost, transferOrdersPathForTest, transferBody, []string{"inventory.transfer.write"}, map[string]string{"Idempotency-Key": "k-plain"})
	if plain.Code != http.StatusCreated || fs.transferIn.Status == domain.TransferDraft {
		t.Fatalf("an order without draft must not be a draft: %d %+v", plain.Code, fs.transferIn)
	}
}

func TestEditTransferPassesDeliveryFieldsAndClears(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	path := transferOrdersPathForTest + "/" + ids.New()
	rec := do(t, h, http.MethodPatch, path, `{"deliveryTerms":"DAP","modeOfDelivery":""}`, []string{"inventory.transfer.write"}, map[string]string{"If-Match": `"2"`})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	e := fs.transferEdit
	if e.DeliveryTerms == nil || *e.DeliveryTerms != "DAP" || e.ModeOfDelivery == nil || *e.ModeOfDelivery != "" {
		t.Fatalf("edit passed to the store: %+v", e)
	}
}

func TestConfirmAndDeleteNeedWriteDutyAndIfMatch(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	id := ids.New()
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, transferOrdersPathForTest + "/" + id + ":confirm"},
		{http.MethodDelete, transferOrdersPathForTest + "/" + id},
	} {
		if rec := do(t, h, c.method, c.path, "", []string{"inventory.transfer.post"}, map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusForbidden {
			t.Fatalf("%s post-only: status %d, want 403", c.path, rec.Code)
		}
		if rec := do(t, h, c.method, c.path, "", []string{"inventory.transfer.write"}, nil); rec.Code != http.StatusPreconditionRequired {
			t.Fatalf("%s no If-Match: status %d, want 428", c.path, rec.Code)
		}
	}
	rec := do(t, h, http.MethodPost, transferOrdersPathForTest+"/"+id+":confirm", "", []string{"inventory.transfer.write"}, map[string]string{"If-Match": `"6"`})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"7"` || fs.transferVersion != 6 || !strings.Contains(rec.Body.String(), `"CREATED"`) {
		t.Fatalf("confirm: %d etag %q %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	rec = do(t, h, http.MethodDelete, transferOrdersPathForTest+"/"+id, "", []string{"inventory.transfer.write"}, map[string]string{"If-Match": `"8"`})
	if rec.Code != http.StatusNoContent || fs.transferVersion != 8 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	fs.err = httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeTransferState, "shipped")
	rec = do(t, h, http.MethodDelete, transferOrdersPathForTest+"/"+id, "", []string{"inventory.transfer.write"}, map[string]string{"If-Match": `"8"`})
	if rec.Code != http.StatusUnprocessableEntity || decodeProblem(t, rec).Code != domain.CodeTransferState {
		t.Fatalf("delete refused: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodGet, transferOrdersPathForTest+"?status=DRAFT", "", []string{"inventory.read"}, nil); rec.Code != http.StatusOK {
		t.Fatalf("list by DRAFT: %d %s", rec.Code, rec.Body.String())
	}
}
