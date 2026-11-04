package upstream

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
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
			"lines":[{"lineNo":1,"qty":"10.000000","net":"1000.00","productId":"` + product + `","packingSlipId":"ps1","orderLineId":"ol1","creditedQty":"2"}]}`))
	})
	inv, err := returnsOn(h).ReadInvoice(h.ctx, h.le, invoiceID)
	if err != nil || path != "/v1/billing/invoices/"+invoiceID || !inv.Returnable || inv.Number != "INV-1" || len(inv.Lines) != 1 ||
		!inv.Lines[0].Qty.Equal(decimal.NewFromInt(10)) || inv.Lines[0].PackingSlipID != "ps1" || !inv.Lines[0].CreditedQty.Equal(decimal.NewFromInt(2)) {
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
	site, warehouse := ids.New(), ids.New()
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request, h *harness) {
		switch r.URL.Path {
		case "/v1/fulfillment/packing-slips/ps1":
			if c := h.claims(t, r, "fulfillment"); c.Duties[0] != "fulfillment.packing-slip.read" {
				t.Errorf("duties %v", c.Duties)
			}
			_, _ = w.Write([]byte(`{"lines":[{"orderLineId":"ol1","qty":"4","costAmount":"240.00"},{"orderLineId":"other","qty":"1","costAmount":"999.00"}]}`))
		case "/v1/inventory/onhand":
			q := r.URL.Query()
			if q.Get("product") != "p1" || q.Get("groupBy") != "legal_entity,site,warehouse" || q.Get("site") != "" {
				t.Errorf("query %v", q)
			}
			// Inventory dimensions are free text: the same site written in upper case is the same site; another
			// warehouse, another legal entity and a non-UUID site are not.
			_, _ = w.Write([]byte(`{"items":[
				{"group":{"legal_entity":"` + h.le + `","site":"` + site + `","warehouse":"` + warehouse + `"},"physicalInvent":"10","postedValue":"750.00"},
				{"group":{"legal_entity":"` + h.le + `","site":"` + strings.ToUpper(site) + `","warehouse":"` + strings.ToUpper(warehouse) + `"},"physicalInvent":"10","postedValue":"850.00"},
				{"group":{"legal_entity":"` + h.le + `","site":"` + site + `","warehouse":"` + ids.New() + `"},"physicalInvent":"10","postedValue":"9999.00"},
				{"group":{"legal_entity":"` + ids.New() + `","site":"` + site + `","warehouse":"` + warehouse + `"},"physicalInvent":"10","postedValue":"9999.00"},
				{"group":{"legal_entity":"` + h.le + `","site":"MAIN","warehouse":"` + warehouse + `"},"physicalInvent":"10","postedValue":"9999.00"}]}`))
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
	cur, ok, err := r.CurrentUnitCost(h.ctx, h.le, "p1", site, warehouse)
	if err != nil || !ok || !cur.Equal(decimal.NewFromInt(80)) {
		t.Fatalf("current %s %v %v", cur, ok, err)
	}
	if _, ok, err := r.CurrentUnitCost(h.ctx, h.le, "p1", "not-a-uuid", warehouse); ok || err != nil {
		t.Fatalf("a site that is not a UUID has no current cost: %v %v", ok, err)
	}
}

func receiptJournal(le, key, product, qty, status, typ string) string {
	return `{"id":"j1","status":"` + status + `","journalType":"` + typ + `","legalEntityId":"` + le + `","version":3,
		"lines":[{"clientKey":"` + key + `","productId":"` + product + `","qty":"` + qty + `"}]}`
}

func TestReceiveReturn_UsesTheServiceOnlyReturnReceipt(t *testing.T) {
	var calls, keys []string
	var sent map[string]any
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request, h *harness) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		if c := h.claims(t, r, "inventory"); len(c.Duties) != 1 || c.Duties[0] != "inventory.return-receipt.post" {
			t.Errorf("duties %v", c.Duties)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &sent)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(receiptJournal(h.le, "rma-receive:l1", "p1", "4", "POSTED", "MOVEMENT")))
	})
	id, err := returnsOn(h).ReceiveReturn(h.ctx, store.ReturnReceipt{LineID: "l1", LegalEntityID: h.le, ProductID: "p1", SiteID: "s1", WarehouseID: "w1", Currency: "USD",
		Unit: "EA", PostingDate: "2027-03-01", Key: "rma-receive:l1", Quantity: decimal.NewFromInt(4), UnitCost: decimal.NewFromInt(60)})
	if err != nil || id != "j1" || len(calls) != 1 || calls[0] != "POST /internal/v1/inventory/return-receipts" || keys[0] != "rma-receive:l1" {
		t.Fatalf("id %q calls %v keys %v err %v", id, calls, keys, err)
	}
	dims, _ := sent["dimensions"].(map[string]any)
	if sent["qty"] != "4" || sent["costPrice"] != "60" || sent["enteredUnit"] != "EA" || sent["offsetAccountId"] != "5000" || dims["site"] != "s1" || dims["warehouse"] != "w1" {
		t.Fatalf("receipt sent %+v", sent)
	}
}

func TestReceiveReturn_RefusesAJournalThatIsNotThisReceipt(t *testing.T) {
	var answer string
	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request, _ *harness) {
		_, _ = w.Write([]byte(answer))
	})
	in := func(le string) store.ReturnReceipt {
		return store.ReturnReceipt{LegalEntityID: le, ProductID: "p1", Key: "k", Quantity: decimal.NewFromInt(4)}
	}
	for name, body := range map[string]string{
		"draft":             receiptJournal(h.le, "k", "p1", "4", "DRAFT", "MOVEMENT"),
		"adjustment":        receiptJournal(h.le, "k", "p1", "4", "POSTED", "ADJUSTMENT"),
		"other key":         receiptJournal(h.le, "other", "p1", "4", "POSTED", "MOVEMENT"),
		"other product":     receiptJournal(h.le, "k", "p2", "4", "POSTED", "MOVEMENT"),
		"other quantity":    receiptJournal(h.le, "k", "p1", "40", "POSTED", "MOVEMENT"),
		"other entity":      receiptJournal(ids.New(), "k", "p1", "4", "POSTED", "MOVEMENT"),
		"two lines":         `{"id":"j1","status":"POSTED","journalType":"MOVEMENT","legalEntityId":"` + h.le + `","lines":[{"clientKey":"k","productId":"p1","qty":"4"},{"clientKey":"x","productId":"p1","qty":"4"}]}`,
		"no journal at all": `{}`,
	} {
		answer = body
		if _, err := returnsOn(h).ReceiveReturn(h.ctx, in(h.le)); !errors.Is(err, ErrForeignReceipt) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	answer = receiptJournal(strings.ToUpper(h.le), "k", "p1", "4.000000", "POSTED", "MOVEMENT")
	if id, err := returnsOn(h).ReceiveReturn(h.ctx, in(h.le)); err != nil || id != "j1" {
		t.Fatalf("the same receipt in canonical forms: %q %v", id, err)
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
