package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/api"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
	"github.com/BenjaminSRussell/passage/services/inventory/migrations"
)

func openingServer(t *testing.T) http.Handler {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	r := chi.NewRouter()
	api.New(s, store.PostParams{InventoryAccount: "1300", AdjustmentAccount: "5100", OpeningAccount: "3900"}).WithOpeningBatches(s).Register(r)
	return r
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
