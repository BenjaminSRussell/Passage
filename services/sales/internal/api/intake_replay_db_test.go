package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
	"github.com/BenjaminSRussell/passage/services/sales/migrations"
)

// TestPostgres_ReplayStoresOneOrder runs the replay against the database
// (PRD 5.27.10): a channel order refused for a listing is replayed, once the
// listing is lifted, by single and bulk replays at the same time. Exactly one
// order is stored under the sender's Idempotency-Key, the intake error is
// closed once with one resolved event and no replay is counted as a failure;
// the sender's own later retry with that key gets the same order, and a retry
// with that key and another body is refused instead of adding an order.
func TestPostgres_ReplayStoresOneOrder(t *testing.T) {
	admin := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := store.New(loadAppPool(t, admin))
	p := principal(dutyWrite)
	ctx := tenant.With(context.Background(), p)
	customer, product := ids.New(), ids.New()
	deny, err := st.CreateListing(ctx, store.ProductListing{LegalEntityID: testLE, ScopeType: domain.ScopeCustomer, ScopeKey: customer,
		ProductID: product, ListingType: domain.ListingDeny, CreatedBy: "listing-admin"})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"legalEntityId":"` + testLE + `","customerId":"` + customer + `","invoiceAccountId":"` + customer + `","currency":"USD",
		"requestedShipDate":"2027-03-04","channel":"EDI","lines":[{"productId":"` + product + `","quantity":"2","unit":"EA",
		"siteId":"` + ids.New() + `","warehouseId":"` + ids.New() + `","unitPrice":"10","requestedShipDate":"2027-03-04"}]}`
	key := map[string]string{idempotencyKey: "edi-" + ids.New()}
	if rec := serveBody(t, st, http.MethodPost, orders, body, p, key); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("refused for the listing: %d %s", rec.Code, rec.Body)
	}
	open, err := st.ListIntakeErrors(ctx, store.IntakeErrorFilter{LegalEntityID: testLE, Limit: 10})
	if err != nil || len(open) != 1 || !open[0].Replayable {
		t.Fatalf("the refusal is kept: %v %+v", err, open)
	}
	row := open[0]
	if err := st.DeleteListing(ctx, deny.ID); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	orderIDs := make(chan string, 8)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(bulk bool) {
			defer wg.Done()
			path := intakeErrors + "/" + row.ID + ":replay"
			if bulk {
				path = intakeErrors + ":replay"
			}
			rec := serveBody(t, st, http.MethodPost, path, "", p, nil)
			if rec.Code == http.StatusConflict {
				return // SLS-0007 (another replay is storing it) or SLS-0116 (already closed)
			}
			if rec.Code != http.StatusOK {
				t.Errorf("replay: %d %s", rec.Code, rec.Body)
				return
			}
			var one replayResult
			var many struct {
				Failed int            `json:"failed"`
				Items  []replayResult `json:"items"`
			}
			if bulk {
				_ = json.Unmarshal(rec.Body.Bytes(), &many)
			} else {
				_ = json.Unmarshal(rec.Body.Bytes(), &one)
				many.Items = []replayResult{one}
			}
			for _, it := range many.Items {
				if !it.Replayed || it.OrderID == nil {
					t.Errorf("a replay was refused: %s", rec.Body)
					continue
				}
				orderIDs <- *it.OrderID
			}
		}(i%3 == 2)
	}
	wg.Wait()
	close(orderIDs)
	distinct := map[string]bool{}
	for id := range orderIDs {
		distinct[id] = true
	}
	var stored int
	if err := admin.QueryRow(ctx, `select count(*) from sales_order where idempotency_key = $1`, key[idempotencyKey]).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 1 || len(distinct) != 1 {
		t.Fatalf("one order for one refusal: %d stored, replays answered %v", stored, distinct)
	}
	closed, err := st.GetIntakeError(ctx, row.ID)
	if err != nil || closed.Status != store.IntakeErrorResolvedStatus || closed.ReplayedOrderID == nil || !distinct[*closed.ReplayedOrderID] ||
		closed.Attempts != 1 || closed.ReplayCount != 1 || closed.Replayable {
		t.Fatalf("closed once with the order and no counted failure: %v %+v", err, closed)
	}
	var resolved int
	if err := admin.QueryRow(ctx, `select count(*) from outbox where aggregate_id = $1 and type = $2`, row.ID,
		string(salesevents.IntakeErrorResolved)).Scan(&resolved); err != nil || resolved != 1 {
		t.Fatalf("one resolved event: %v %d", err, resolved)
	}

	// The sender retries after the replay: the same body gets the replayed
	// order; another body under the same key is refused, not stored.
	rec := serveBody(t, st, http.MethodPost, orders, body, p, key)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), *closed.ReplayedOrderID) {
		t.Fatalf("the sender's retry finds the replayed order: %d %s", rec.Code, rec.Body)
	}
	changed := strings.Replace(body, `"quantity":"2"`, `"quantity":"3"`, 1)
	if rec := serveBody(t, st, http.MethodPost, orders, changed, p, key); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("another body under the replayed key: %d %s", rec.Code, rec.Body)
	}
	if err := admin.QueryRow(ctx, `select count(*) from sales_order where idempotency_key = $1`, key[idempotencyKey]).Scan(&stored); err != nil || stored != 1 {
		t.Fatalf("still one order: %v %d", err, stored)
	}
}
