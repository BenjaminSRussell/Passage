package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/api"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
	"github.com/BenjaminSRussell/passage/services/inventory/migrations"
)

func openingServer(t *testing.T) http.Handler {
	h, _ := openingServerStore(t)
	return h
}

func openingServerStore(t *testing.T) (http.Handler, *store.Postgres) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	r := chi.NewRouter()
	api.New(s, store.PostParams{InventoryAccount: "1300", AdjustmentAccount: "5100", OpeningAccount: "3900"}).WithOpeningBatches(s).Register(r)
	return r, s
}

func openingBody(key string, items ...string) string {
	return `{"legalEntityId":"` + glEntity + `","batchKey":"` + key + `","postingDate":"2026-12-31","currency":"USD","items":[` + strings.Join(items, ",") + `]}`
}

func openingItem(ref, product, qty, cost string) string {
	return `{"reference":"` + ref + `","productId":"` + product + `","dimensions":{"site":"S1","warehouse":"W1"},"enteredUnit":"EA","qty":"` + qty + `","costPrice":"` + cost + `"}`
}

func TestOpeningBatchPostsOnceAndReplays(t *testing.T) {
	h := openingServer(t)
	imp, rd := []string{"inventory.import.write"}, []string{"inventory.import.read"}
	p1, p2 := ids.New(), ids.New()
	body := openingBody("b1", openingItem("L1", p1, "10", "2.5"), openingItem("L2", p2, "3", "1.1"))

	if rec := do(t, h, http.MethodPost, "/v1/inventory/opening-batches", body, rd, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("without the import duty: %d", rec.Code)
	}
	rec := do(t, h, http.MethodPost, "/v1/inventory/opening-batches", body, imp, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first send: %d %s", rec.Code, rec.Body)
	}
	var first struct {
		JournalID, Qty, Value string
		ItemCount             int
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil || first.ItemCount != 2 || first.Qty != "13.000000" || first.Value != "28.300000000000" {
		t.Fatalf("batch %+v %v %s", first, err, rec.Body)
	}
	rec = do(t, h, http.MethodPost, "/v1/inventory/opening-batches", body, imp, nil)
	var again struct{ JournalID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &again)
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" || again.JournalID != first.JournalID {
		t.Fatalf("resend: %d %s", rec.Code, rec.Body)
	}
	changed := openingBody("b1", openingItem("L1", p1, "11", "2.5"), openingItem("L2", p2, "3", "1.1"))
	if rec := do(t, h, http.MethodPost, "/v1/inventory/opening-batches", changed, imp, nil); rec.Code != http.StatusUnprocessableEntity || decodeProblem(t, rec).Code != store.CodeOpeningKeyConflict {
		t.Fatalf("changed content under the key: %d %s", rec.Code, rec.Body)
	}

	rec = do(t, h, http.MethodGet, "/v1/inventory/opening-batches?legal_entity="+glEntity, "", rd, nil)
	var list struct {
		Items []struct {
			BatchKey string
			Qty      string
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != http.StatusOK || len(list.Items) != 1 || list.Items[0].BatchKey != "b1" || list.Items[0].Qty != "13.000000" {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}

	// The stock is on hand after the import.
	rec = do(t, h, http.MethodGet, "/v1/inventory/onhand?product="+p1, "", []string{"inventory.read"}, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "10") {
		t.Fatalf("on hand: %d %s", rec.Code, rec.Body)
	}
}

func TestOpeningBatchNamesTheRowsItRefuses(t *testing.T) {
	h := openingServer(t)
	imp := []string{"inventory.import.write"}
	p := ids.New()
	body := openingBody("b2", openingItem("L1", p, "0", "1"), openingItem("L1", "not-a-uuid", "1", "1"), openingItem("L3", p, "1", "-1"))
	rec := do(t, h, http.MethodPost, "/v1/inventory/opening-batches", body, imp, nil)
	pb := decodeProblem(t, rec)
	if rec.Code != http.StatusUnprocessableEntity || pb.Code != "INV-OPB-001" || pb.Errors["items[1].qty"] == "" || pb.Errors["items[2].productId"] == "" ||
		pb.Errors["items[2].reference"] == "" || pb.Errors["items[3].costPrice"] == "" {
		t.Fatalf("refusal: %d %s", rec.Code, rec.Body)
	}
	other := strings.Replace(openingBody("b3", openingItem("L1", p, "1", "1")), glEntity, ids.New(), 1)
	if rec := do(t, h, http.MethodPost, "/v1/inventory/opening-batches", other, imp, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("other legal entity: %d %s", rec.Code, rec.Body)
	}
}

// A journal that already sits under the batch key with other lines is not the batch: it is neither posted nor recorded as it.
func TestOpeningBatchRefusesAForeignJournalUnderItsKey(t *testing.T) {
	h, s := openingServerStore(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "inventory.import.write")
	p2 := ids.New()
	j, _, err := s.CreateJournal(ctx, domain.Journal{LegalEntityID: glEntity, Type: domain.JournalOpeningBalance, PostingDate: time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC), Currency: "USD"}, "opening-batch:b9")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddJournalLines(ctx, j.ID, []domain.JournalLine{{ClientKey: "X", ProductID: p2, Dims: domain.Dimensions{"site": "S1", "warehouse": "W1"}, Qty: decimal.NewFromInt(500),
		EnteredQty: decimal.NewFromInt(500), EnteredUnit: "EA", CostPrice: decimal.NewFromInt(1), LineNo: 1}}); err != nil {
		t.Fatal(err)
	}
	rec := do(t, h, http.MethodPost, "/v1/inventory/opening-batches", openingBody("b9", openingItem("L1", p2, "10", "1")), []string{"inventory.import.write"}, nil)
	if rec.Code != http.StatusUnprocessableEntity || decodeProblem(t, rec).Code != store.CodeOpeningKeyConflict {
		t.Fatalf("foreign journal: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, http.MethodGet, "/v1/inventory/onhand?product="+p2, "", []string{"inventory.read"}, nil); strings.Contains(rec.Body.String(), "500") {
		t.Fatalf("the foreign journal was posted: %s", rec.Body)
	}
	// The refused call released what it reserved: another batch holding L1 goes through (#49).
	if rec := do(t, h, http.MethodPost, "/v1/inventory/opening-batches", openingBody("b11", openingItem("L1", p2, "10", "1")), []string{"inventory.import.write"}, nil); rec.Code != http.StatusCreated {
		t.Fatalf("L1 stayed reserved by the refused batch: %d %s", rec.Code, rec.Body)
	}
	// The prefix is reserved: nobody can create a journal under it through the journal API.
	create := `{"legalEntityId":"` + glEntity + `","journalType":"OPENING_BALANCE","postingDate":"2026-12-31","currency":"USD"}`
	if rec := do(t, h, http.MethodPost, "/v1/inventory/journals", create, []string{"inventory.journal.write"}, map[string]string{"Idempotency-Key": "opening-batch:b10"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("reserved key: %d %s", rec.Code, rec.Body)
	}
}

// A reference opens stock once per legal entity, across batches (#49): a second batch holding a line of the first is refused with
// the rows named, and nothing of it is posted; the first batch's resend still replays.
func TestOpeningBatchRefusesAReferenceAnotherBatchOpened(t *testing.T) {
	h := openingServer(t)
	imp := []string{"inventory.import.write"}
	p1, p2 := ids.New(), ids.New()
	first := openingBody("b1", openingItem("L1", p1, "10", "2.5"), openingItem("L2", p2, "3", "1.1"))
	if rec := do(t, h, http.MethodPost, "/v1/inventory/opening-batches", first, imp, nil); rec.Code != http.StatusCreated {
		t.Fatalf("first batch: %d %s", rec.Code, rec.Body)
	}
	second := openingBody("b2", openingItem("L9", p1, "1", "2.5"), openingItem("L2", p2, "3", "1.1"))
	rec := do(t, h, http.MethodPost, "/v1/inventory/opening-batches", second, imp, nil)
	pb := decodeProblem(t, rec)
	if rec.Code != http.StatusUnprocessableEntity || pb.Code != "INV-OPB-005" || !strings.Contains(pb.Errors["items[2].reference"], "b1") || pb.Errors["items[1].reference"] != "" {
		t.Fatalf("overlapping batch: %d %s", rec.Code, rec.Body)
	}
	// L9 was not left reserved by the refused batch: a batch with it alone goes through.
	if rec := do(t, h, http.MethodPost, "/v1/inventory/opening-batches", openingBody("b3", openingItem("L9", p1, "1", "2.5")), imp, nil); rec.Code != http.StatusCreated {
		t.Fatalf("the refused batch kept L9: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, http.MethodPost, "/v1/inventory/opening-batches", first, imp, nil); rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("resend of the first batch: %d %s", rec.Code, rec.Body)
	}
}
