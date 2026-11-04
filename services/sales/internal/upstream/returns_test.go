package upstream

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

func returnsOn(h *harness) *Returns {
	u := h.client.productURL
	return NewReturns(h.client, u, u, u, u, "5000")
}

func TestReadInvoice_AsksBillingWithTheReadDutyAndProjectsLines(t *testing.T) {
	invoiceID, product := ids.New(), ids.New()
	var path string
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request, h *harness) {
		path = r.URL.Path
		if c := h.claims(t, r, "billing"); len(c.Duties) != 1 || c.Duties[0] != "billing.invoice.read" {
			t.Errorf("duties %v", c.Duties)
		}
		_, _ = w.Write([]byte(`{"id":"` + invoiceID + `","legalEntityId":"` + h.le + `","customerId":"c1","number":"INV-1","currency":"USD","status":"POSTED","type":"SHIPMENT",
			"lines":[{"lineNo":1,"qty":"10.000000","net":"1000.00","productId":"` + product + `","packingSlipId":"ps1","orderLineId":"ol1"}]}`))
	})
	inv, err := returnsOn(h).ReadInvoice(h.ctx, h.le, invoiceID)
	if err != nil || path != "/v1/billing/invoices/"+invoiceID || !inv.Returnable || inv.Number != "INV-1" || len(inv.Lines) != 1 ||
		!inv.Lines[0].Qty.Equal(decimal.NewFromInt(10)) || inv.Lines[0].PackingSlipID != "ps1" {
		t.Fatalf("invoice %+v %v (%s)", inv, err, path)
	}
}

func TestReadInvoice_NotFoundCreditNotesAndUnpostedAreNotReturnable(t *testing.T) {
	body := `{"id":"i","legalEntityId":"l","customerId":"c","number":"CN-1","currency":"USD","status":"POSTED","type":"CREDIT_NOTE","lines":[]}`
	status := http.StatusOK
	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request, _ *harness) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	inv, err := returnsOn(h).ReadInvoice(h.ctx, h.le, "i")
	if err != nil || inv.Returnable {
		t.Fatalf("a credit note is not returnable: %+v %v", inv, err)
	}
	body = `{"id":"i","number":"","status":"PROPOSAL","type":"SHIPMENT","lines":[]}`
	if inv, _ := returnsOn(h).ReadInvoice(h.ctx, h.le, "i"); inv.Returnable {
		t.Fatalf("a proposal is not returnable")
	}
	status = http.StatusNotFound
	if _, err := returnsOn(h).ReadInvoice(h.ctx, h.le, "i"); !errors.Is(err, store.ErrInvoiceNotFound) {
		t.Fatalf("404: %v", err)
	}
	status = http.StatusBadGateway
	var se *StatusError
	if _, err := returnsOn(h).ReadInvoice(h.ctx, h.le, "i"); !errors.As(err, &se) || se.Status != http.StatusBadGateway {
		t.Fatalf("502: %v", err)
	}
}

func TestOriginalAndCurrentUnitCost(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request, h *harness) {
		switch r.URL.Path {
		case "/v1/fulfillment/packing-slips/ps1":
			if c := h.claims(t, r, "fulfillment"); c.Duties[0] != "fulfillment.packing-slip.read" {
				t.Errorf("duties %v", c.Duties)
			}
			_, _ = w.Write([]byte(`{"lines":[{"orderLineId":"ol1","qty":"4","costAmount":"240.00"},{"orderLineId":"other","qty":"1","costAmount":"999.00"}]}`))
		case "/v1/inventory/onhand":
			if r.URL.Query().Get("product") != "p1" || r.URL.Query().Get("site") != "s1" || r.URL.Query().Get("warehouse") != "w1" {
				t.Errorf("query %v", r.URL.Query())
			}
			_, _ = w.Write([]byte(`{"items":[{"physicalInvent":"10","postedValue":"750.00"},{"physicalInvent":"10","postedValue":"850.00"}]}`))
		default:
			t.Errorf("path %s", r.URL.Path)
		}
	})
	r := returnsOn(h)
	cost, ok, err := r.OriginalUnitCost(h.ctx, h.le, "ps1", []string{"ol1"})
	if err != nil || !ok || !cost.Equal(decimal.NewFromInt(60)) {
		t.Fatalf("original %s %v %v", cost, ok, err)
	}
	if _, ok, _ := r.OriginalUnitCost(h.ctx, h.le, "ps1", []string{"missing"}); ok {
		t.Fatalf("no matching slip line means unknown cost")
	}
	cur, ok, err := r.CurrentUnitCost(h.ctx, h.le, "p1", "s1", "w1")
	if err != nil || !ok || !cur.Equal(decimal.NewFromInt(80)) {
		t.Fatalf("current %s %v %v", cur, ok, err)
	}
}

func TestReceiveReturn_CreatesFillsAndPostsOneKeyedJournal(t *testing.T) {
	var calls []string
	var keys []string
	var lineBody map[string][]map[string]any
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request, h *harness) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		switch r.URL.Path {
		case "/v1/inventory/journals":
			if h.claims(t, r, "inventory").Duties[0] != "inventory.journal.write" {
				t.Errorf("create duty")
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"j1","status":"DRAFT","version":1}`))
		case "/v1/inventory/journals/j1/lines:bulk":
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &lineBody)
			_, _ = w.Write([]byte(`{"id":"j1","status":"DRAFT","version":2}`))
		case "/v1/inventory/journals/j1:post":
			if r.Header.Get("If-Match") != `"2"` || h.claims(t, r, "inventory").Duties[0] != "inventory.journal.post" {
				t.Errorf("post precondition %q", r.Header.Get("If-Match"))
			}
			_, _ = w.Write([]byte(`{"id":"j1","status":"POSTED","version":3}`))
		default:
			t.Errorf("path %s", r.URL.Path)
		}
	})
	id, err := returnsOn(h).ReceiveReturn(h.ctx, store.ReturnReceipt{LineID: "l1", LegalEntityID: h.le, ProductID: "p1", SiteID: "s1", WarehouseID: "w1", Currency: "USD",
		Unit: "EA", PostingDate: "2027-03-01", Key: "rma-receive:l1", Quantity: decimal.NewFromInt(4), UnitCost: decimal.NewFromInt(60)})
	if err != nil || id != "j1" || len(calls) != 3 || keys[0] != "rma-receive:l1" {
		t.Fatalf("id %q calls %v keys %v err %v", id, calls, keys, err)
	}
	line := lineBody["lines"][0]
	if line["clientKey"] != "rma-receive:l1" || line["qty"] != "4" || line["costPrice"] != "60" || line["enteredUnit"] != "EA" {
		t.Fatalf("line sent %+v", line)
	}
}

func TestReceiveReturn_ReplayOfAPostedJournalStopsAfterCreate(t *testing.T) {
	n := 0
	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request, _ *harness) {
		n++
		_, _ = w.Write([]byte(`{"id":"j1","status":"POSTED","version":3}`))
	})
	id, err := returnsOn(h).ReceiveReturn(h.ctx, store.ReturnReceipt{Key: "k", Quantity: decimal.NewFromInt(1)})
	if err != nil || id != "j1" || n != 1 {
		t.Fatalf("replay %q calls %d %v", id, n, err)
	}
}

func TestPostRMACredit_SendsOnlyWhatReceivablesDerivesNothingFrom(t *testing.T) {
	var got map[string]string
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request, h *harness) {
		if r.URL.Path != "/v1/receivables/rma-credits" || h.claims(t, r, "receivables").Duties[0] != "ar.rmacredit.post" || r.Header.Get("Idempotency-Key") != "k1" {
			t.Errorf("request %s %v", r.URL.Path, r.Header)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"openItemId":"oi","documentNumber":"CN-RMA-1","journalPostingId":"je"}`))
	})
	note, err := returnsOn(h).PostRMACredit(h.ctx, store.ARCredit{RMAID: "r1", RMANumber: "RMA-1", LegalEntityID: h.le, InvoiceNumber: "INV-1", PostingDate: "2027-03-01",
		IdempotencyKey: "k1", Gross: decimal.NewFromInt(400), Restocking: decimal.NewFromInt(60), Net: decimal.NewFromInt(340)})
	if err != nil || note.OpenItemID != "oi" || note.JournalPostingID != "je" {
		t.Fatalf("note %+v %v", note, err)
	}
	if got["gross"] != "400.00" || got["net"] != "340.00" || got["invoiceNumber"] != "INV-1" || got["customerId"] != "" || got["ledgerId"] != "" {
		t.Fatalf("body %+v", got)
	}
}

func TestReturns_UnconfiguredPeersAnswerErrNotConfigured(t *testing.T) {
	h := newHarness(t, func(http.ResponseWriter, *http.Request, *harness) {})
	r := NewReturns(h.client, "", "", "", "", "")
	if _, err := r.ReadInvoice(h.ctx, h.le, "i"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("billing: %v", err)
	}
	if _, err := r.ReceiveReturn(h.ctx, store.ReturnReceipt{}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("inventory: %v", err)
	}
	if _, err := r.PostRMACredit(h.ctx, store.ARCredit{}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("receivables: %v", err)
	}
}
