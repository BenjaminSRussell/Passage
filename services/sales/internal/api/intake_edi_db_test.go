package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
	"github.com/BenjaminSRussell/passage/services/sales/migrations"
)

// TestPostgres_EDIPurchaseOrderIsOneSalesOrder runs the order edi builds from an
// inbound 850 (channel EDI, externalRef partner:PO number, Idempotency-Key the
// edi message id, only the service-only intake duty) against the database: it
// is stored once; the same message again, and the same PO in a second
// interchange (another message id), return that order; a PO resent with other
// lines is refused and kept in the intake error worklist, not stored twice.
func TestPostgres_EDIPurchaseOrderIsOneSalesOrder(t *testing.T) {
	admin := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := store.New(loadAppPool(t, admin))
	intake := principal(dutyIntake)
	customer, product := ids.New(), ids.New()
	externalRef := ids.New() + ":PO1234"
	site, warehouse := ids.New(), ids.New()
	body := func(qty string) string {
		return `{"legalEntityId":"` + testLE + `","customerId":"` + customer + `","invoiceAccountId":"` + customer + `","currency":"USD",
			"requestedShipDate":"2026-11-05","channel":"EDI","externalRef":"` + externalRef + `","lines":[{"productId":"` + product +
			`","quantity":"` + qty + `","unit":"EA","siteId":"` + site + `","warehouseId":"` + warehouse + `","unitPrice":"2.5","requestedShipDate":"2026-11-05"}]}`
	}
	same := body("10")

	first := serveBody(t, st, http.MethodPost, orders, same, intake, map[string]string{idempotencyKey: "msg-1"})
	var created struct {
		ID          string `json:"id"`
		Channel     string `json:"channel"`
		ExternalRef string `json:"externalRef"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &created); err != nil || first.Code != http.StatusCreated || created.ID == "" || created.Channel != "EDI" || created.ExternalRef != externalRef {
		t.Fatalf("first post: %d %s", first.Code, first.Body)
	}
	for _, key := range []string{"msg-1", "msg-2"} {
		again := serveBody(t, st, http.MethodPost, orders, same, intake, map[string]string{idempotencyKey: key})
		var got struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(again.Body.Bytes(), &got); err != nil || again.Code != http.StatusOK || got.ID != created.ID {
			t.Fatalf("repeat under %s must return the one order: %d %s", key, again.Code, again.Body)
		}
	}
	var stored int
	if err := admin.QueryRow(context.Background(), `select count(*) from sales_order where channel = 'EDI' and external_ref = $1`, externalRef).Scan(&stored); err != nil || stored != 1 {
		t.Fatalf("one stored order for the PO: %d %v", stored, err)
	}

	changed := body("11")
	refused := serveBody(t, st, http.MethodPost, orders, changed, intake, map[string]string{idempotencyKey: "msg-3"})
	if refused.Code != http.StatusConflict && refused.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a PO resent with other lines is refused: %d %s", refused.Code, refused.Body)
	}
	if err := admin.QueryRow(context.Background(), `select count(*) from sales_order where channel = 'EDI' and external_ref = $1`, externalRef).Scan(&stored); err != nil || stored != 1 {
		t.Fatalf("still one stored order: %d %v", stored, err)
	}
	open, err := st.ListIntakeErrors(tenant.With(context.Background(), intake), store.IntakeErrorFilter{LegalEntityID: testLE, Channel: "EDI", Limit: 10})
	if err != nil || len(open) == 0 || open[0].ExternalRef == nil || *open[0].ExternalRef != externalRef {
		t.Fatalf("the refusal is in the worklist: %v %+v", err, open)
	}

	lookup := serveBody(t, st, http.MethodPost, orders, same, principal("sales.order.lookup"), map[string]string{idempotencyKey: "msg-4"})
	if lookup.Code != http.StatusForbidden {
		t.Fatalf("the read duty cannot place orders: %d", lookup.Code)
	}
}
