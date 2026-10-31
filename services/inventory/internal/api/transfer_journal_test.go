package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
)

// A TRANSFER journal is created without an offset account and its lines carry
// the destination; the response returns toDimensions.
func TestTransferJournalCreateAndLines(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	body := `{"journalType":"TRANSFER","legalEntityId":"` + ids.New() + `","postingDate":"2026-09-01","currency":"USD"}`
	rec := do(t, h, http.MethodPost, "/v1/inventory/journals", body, []string{"inventory.journal.write"}, map[string]string{"Idempotency-Key": "t-1"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	withOffset := strings.Replace(body, `"currency"`, `"offsetAccountId":"5001","currency"`, 1)
	if rec := do(t, h, http.MethodPost, "/v1/inventory/journals", withOffset, []string{"inventory.journal.write"}, map[string]string{"Idempotency-Key": "t-2"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("offset on a transfer: %d", rec.Code)
	}
	line := `{"lines":[{"productId":"` + ids.New() + `","dimensions":{"site":"S1","location":"A1"},"toDimensions":{"site":"S1","location":"B7"},"qty":"3","enteredUnit":"EA"}]}`
	rec = do(t, h, http.MethodPost, "/v1/inventory/journals/"+ids.New()+"/lines:bulk", line, []string{"inventory.journal.write"}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("lines: %d %s", rec.Code, rec.Body.String())
	}
	if len(fs.lines) != 1 || fs.lines[0].ToDims[domain.DimLocation] != "B7" {
		t.Fatalf("store got %+v", fs.lines)
	}
	var out struct {
		Lines []struct {
			ToDimensions map[string]string `json:"toDimensions"`
		} `json:"lines"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Lines) != 1 || out.Lines[0].ToDimensions["location"] != "B7" {
		t.Fatalf("response %v %s", err, rec.Body.String())
	}
}
