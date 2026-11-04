package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/api"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
	"github.com/BenjaminSRussell/passage/services/inventory/migrations"
)

const (
	receiptLE   = "00000000-0000-0000-0000-0000000000e1" // testkit.Ctx's legal entity
	receiptDuty = "inventory.return-receipt.post"
	receiptPath = "/internal/v1/inventory/return-receipts"
)

func receiptServer(t *testing.T) (http.Handler, *store.Postgres) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	r := chi.NewRouter()
	api.New(s, store.PostParams{InventoryAccount: "1300", AdjustmentAccount: "5100", OpeningAccount: "3900"}).Register(r)
	return r, s
}

func receiptBody(product, qty string) string {
	return `{"legalEntityId":"` + receiptLE + `","postingDate":"2027-03-02","currency":"USD","offsetAccountId":"1290","productId":"` + product +
		`","dimensions":{"site":"` + "0195f0a0-0000-7000-8000-0000000000a1" + `","warehouse":"0195f0a0-0000-7000-8000-0000000000b1"},"qty":"` + qty +
		`","enteredUnit":"EA","costPrice":"12.5"}`
}

func onHandOf(t *testing.T, s *store.Postgres, product string) decimal.Decimal {
	t.Helper()
	res, err := s.ListOnHand(testkit.Ctx(testkit.TenantID(1)), store.OnHandFilter{ProductID: product})
	if err != nil {
		t.Fatal(err)
	}
	total := decimal.Zero
	for _, r := range res.Rows {
		total = total.Add(r.PhysicalInvent)
	}
	return total
}

func TestReturnReceipt_PostsOnceAndReplays(t *testing.T) {
	h, s := receiptServer(t)
	product := ids.New()
	key := map[string]string{"Idempotency-Key": "rma-receive:" + ids.New()}

	if rec := do(t, h, http.MethodPost, receiptPath, receiptBody(product, "3"), []string{"inventory.journal.write", "inventory.journal.post"}, key); rec.Code != http.StatusForbidden {
		t.Fatalf("the general journal duties do not reach the route: %d %s", rec.Code, rec.Body.String())
	}
	rec := do(t, h, http.MethodPost, receiptPath, receiptBody(product, "3"), []string{receiptDuty}, key)
	if rec.Code != http.StatusCreated {
		t.Fatalf("receipt: %d %s", rec.Code, rec.Body.String())
	}
	var j struct {
		ID, Status, JournalType, LegalEntityID string
		Lines                                  []struct{ ClientKey, Qty string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &j); err != nil || j.Status != "POSTED" || j.JournalType != "MOVEMENT" || j.LegalEntityID != receiptLE ||
		len(j.Lines) != 1 || j.Lines[0].ClientKey != key["Idempotency-Key"] || j.Lines[0].Qty != "3" {
		t.Fatalf("journal %+v %v", j, err)
	}
	if got := onHandOf(t, s, product); !got.Equal(decimal.NewFromInt(3)) {
		t.Fatalf("on hand %s, want 3", got)
	}
	replay := do(t, h, http.MethodPost, receiptPath, receiptBody(product, "3"), []string{receiptDuty}, key)
	if replay.Code != http.StatusOK || replay.Header().Get("Idempotent-Replayed") != "true" || !strings.Contains(replay.Body.String(), j.ID) {
		t.Fatalf("replay: %d %s", replay.Code, replay.Body.String())
	}
	if got := onHandOf(t, s, product); !got.Equal(decimal.NewFromInt(3)) {
		t.Fatalf("on hand after replay %s, want 3", got)
	}
	if other := do(t, h, http.MethodPost, receiptPath, receiptBody(product, "4"), []string{receiptDuty}, key); other.Code != http.StatusUnprocessableEntity {
		t.Fatalf("the key with another quantity: %d %s", other.Code, other.Body.String())
	}
}

func TestReturnReceipt_Validation(t *testing.T) {
	h, _ := receiptServer(t)
	product := ids.New()
	key := map[string]string{"Idempotency-Key": "rma-receive:" + ids.New()}
	tests := []struct {
		name, body, field string
		headers           map[string]string
	}{
		{"no key", receiptBody(product, "3"), "Idempotency-Key", nil},
		{"zero quantity", receiptBody(product, "0"), "qty", key},
		{"no warehouse", strings.Replace(receiptBody(product, "3"), `,"warehouse":"0195f0a0-0000-7000-8000-0000000000b1"`, "", 1), "dimensions", key},
		{"no offset account", strings.Replace(receiptBody(product, "3"), `"offsetAccountId":"1290",`, "", 1), "offsetAccountId", key},
		{"bad product", receiptBody("p1", "3"), "productId", key},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, h, http.MethodPost, receiptPath, tc.body, []string{receiptDuty}, tc.headers)
			var p problemBody
			_ = json.Unmarshal(rec.Body.Bytes(), &p)
			if rec.Code != http.StatusBadRequest || p.Errors[tc.field] == "" {
				t.Fatalf("status %d, want 400 on %s: %s", rec.Code, tc.field, rec.Body.String())
			}
		})
	}
}

func TestReturnReceipt_KeySpaceCannotBeSquatted(t *testing.T) {
	h, s := receiptServer(t)
	product := ids.New()
	lineKey := "rma-receive:" + ids.New()
	body := `{"journalType":"MOVEMENT","legalEntityId":"` + receiptLE + `","postingDate":"2027-03-02","offsetAccountId":"1290","currency":"USD"}`
	rec := do(t, h, http.MethodPost, "/v1/inventory/journals", body, []string{"inventory.journal.write"}, map[string]string{"Idempotency-Key": "return-receipt:" + lineKey})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("the reserved key prefix must be refused on the public route: %d %s", rec.Code, rec.Body.String())
	}
	// A journal under the key that holds something else (planted below the API) is never trusted as the receipt.
	ctx := testkit.Ctx(testkit.TenantID(1))
	planted, _, err := s.CreateJournal(ctx, domain.Journal{LegalEntityID: receiptLE, Type: domain.JournalMovement,
		PostingDate: time.Date(2027, 3, 2, 0, 0, 0, 0, time.UTC), OffsetAccountID: "1290", Currency: "USD"}, "return-receipt:"+lineKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddJournalLines(ctx, planted.ID, []domain.JournalLine{{ClientKey: "other", ProductID: product,
		Dims: domain.Dimensions{"site": "S1", "warehouse": "W1"}, Qty: decimal.NewFromInt(50), EnteredQty: decimal.NewFromInt(50), EnteredUnit: "EA"}}); err != nil {
		t.Fatal(err)
	}
	rec = do(t, h, http.MethodPost, receiptPath, receiptBody(product, "3"), []string{receiptDuty}, map[string]string{"Idempotency-Key": lineKey})
	if rec.Code != http.StatusConflict {
		t.Fatalf("a foreign journal under the key: %d %s", rec.Code, rec.Body.String())
	}
	if got := onHandOf(t, s, product); !got.IsZero() {
		t.Fatalf("nothing may be posted: on hand %s", got)
	}
}
