package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/api"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/purchasing"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store/memstore"
)

// receiptFixture is the catch-weight receipt.posted the inventory postings
// consumer is tested with (services/inventory/testdata/catchweight-flow, #480).
const (
	fixtureDir     = "../../../inventory/testdata/catchweight-flow/"
	receiptFixture = fixtureDir + "receipt-posted.json"
)

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestCatchWeightReceipt_PostedEventAndGRIRMatchTheInventoryContract follows a
// catch-weight receipt bound to a weight-unit PO line from registration to
// posting: 10 cases at 102 kg and 2.00/kg. receipt.posted has exactly the
// members of the fixture inventory books from, with the weight as qty, the
// cases as cw_qty, the per-kg unit_price, the amount 204 and the PO line's site
// and warehouse; the GR/IR open item carries the same cwQty beside the weight.
// The correction and the cancellation that follow write receipt.corrected
// with exactly the members of receipt-corrected.json and receipt-cancelled.json.
func TestCatchWeightReceipt_PostedEventAndGRIRMatchTheInventoryContract(t *testing.T) {
	raw, err := os.ReadFile(receiptFixture)
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	wantLine := want["lines"].([]any)[0].(map[string]any)
	product, site, warehouse, location := wantLine["product_id"].(string), wantLine["site_id"].(string), wantLine["warehouse_id"].(string), wantLine["location_id"].(string)

	po := fmt.Sprintf(`{"id":%q,"legalEntityId":%q,"vendorId":%q,"currency":"USD","approvalStatus":"APPROVED","documentStatus":"NONE","lines":[{"lineNo":1,"productId":%q,"unit":"KG","qty":"100","unitPrice":"2.00","priceUnit":"1","discountPct":"0","receivedQty":"0","invoicedQty":"0","status":"OPEN","siteId":%q,"warehouseId":%q}]}`,
		poID, legalEntityID, vendorID, product, site, warehouse)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(po)) }))
	t.Cleanup(srv.Close)
	st := memstore.New()
	a := api.New(st, api.Ports{
		Periods: domain.FakePeriods{Status: domain.PeriodOpen}, Ledger: &domain.FakeLedger{}, PurchaseOrders: purchasing.New(srv.URL, nil),
		Profile:       domain.PostingProfile{LedgerID: ledgerID, Inventory: "1400", GRIR: "2100", PPV: "5200", VendorControl: "2000", Expense: "6000", InputTax: "1570"},
		ResolveMinAge: time.Nanosecond, Tax: testTax, SelfBillParties: testParties,
	})
	h := httpx.NewRouter()
	a.Register(h)

	body := fmt.Sprintf(`{"vendorId":%q,"purchaseOrderId":%q,"legalEntityId":%q,"packingSlip":"PS-CW","receiptDate":%q,"currency":"USD","lines":[{"poLineNo":1,"productId":%q,"overDeliveryPct":5,"unit":"KG","qty":102,"locationId":%q,"cwQty":10,"catchWeight":%s}]}`,
		vendorID, poID, legalEntityID, want["posting_date"], product, location, catchSpec)
	reg := doWith(h, http.MethodPost, receiptsPath, body, maintainer("cw-reg"))
	if reg.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", reg.Code, reg.Body)
	}
	var r receiptJSON
	_ = json.Unmarshal(reg.Body.Bytes(), &r)
	post := doWith(h, http.MethodPost, receiptsPath+"/"+r.ID+":post", "", duty("receiving.receipt.post", "cw-post", "If-Match", fmt.Sprintf(`"%d"`, r.Version)))
	if post.Code != http.StatusOK {
		t.Fatalf("post: %d %s", post.Code, post.Body)
	}

	var got map[string]any
	for _, e := range st.Events() {
		if e.Type == api.ReceiptPostedType {
			if err := json.Unmarshal(e.Data, &got); err != nil {
				t.Fatal(err)
			}
		}
	}
	if got == nil {
		t.Fatal("no receipt.posted")
	}
	gotLine := got["lines"].([]any)[0].(map[string]any)
	if !reflect.DeepEqual(sortedKeys(got), sortedKeys(want)) || !reflect.DeepEqual(sortedKeys(gotLine), sortedKeys(wantLine)) {
		t.Fatalf("members differ from the fixture:\n got %v / %v\nwant %v / %v", sortedKeys(got), sortedKeys(gotLine), sortedKeys(want), sortedKeys(wantLine))
	}
	for _, k := range []string{"posting_date", "currency"} {
		if !reflect.DeepEqual(got[k], want[k]) {
			t.Errorf("%s = %v, fixture %v", k, got[k], want[k])
		}
	}
	for _, k := range []string{"line_no", "po_line_no", "product_id", "unit", "qty", "batch", "serials", "location_id", "inventory_status", "amount", "amount_acct",
		"unit_price", "site_id", "warehouse_id", "cw_qty", "cw_nominal", "cw_min", "cw_max"} {
		if !reflect.DeepEqual(gotLine[k], wantLine[k]) {
			t.Errorf("line %s = %v, fixture %v", k, gotLine[k], wantLine[k])
		}
	}

	grir := doWith(h, http.MethodGet, grirPath, "", duty("receiving.ap.inquire", "cw-grir"))
	var page struct {
		Items []struct {
			ReceiptID   string `json:"receiptId"`
			ReceivedQty string `json:"receivedQty"`
			UnitPrice   string `json:"unitPrice"`
			CWQty       *int   `json:"cwQty"`
		} `json:"items"`
	}
	if err := json.Unmarshal(grir.Body.Bytes(), &page); err != nil || grir.Code != http.StatusOK || len(page.Items) != 1 {
		t.Fatalf("grir %d %s", grir.Code, grir.Body)
	}
	if g := page.Items[0]; g.ReceiptID != r.ID || g.ReceivedQty != "102" || g.UnitPrice != "2" || g.CWQty == nil || *g.CWQty != 10 {
		t.Fatalf("GR/IR item %+v, want weight 102, 2 per kg and cwQty 10", g)
	}
	if strings.Contains(grir.Body.String(), `"cwQty":0`) {
		t.Fatal("cwQty 0 written")
	}

	// The correction (1 case and 10 kg short, on 2026-10-27) and the
	// cancellation of what is left (9 cases, 92 kg, on 2026-10-29) are the
	// receipt.corrected events inventory offsets the booked line with.
	var posted receiptJSON
	_ = json.Unmarshal(post.Body.Bytes(), &posted)
	corr := doWith(h, http.MethodPost, receiptsPath+"/"+r.ID+":correct", `{"postingDate":"2026-10-27","lines":[{"lineNo":1,"qtyDelta":-10,"cwQtyDelta":-1}]}`,
		duty("receiving.receipt.post", "cw-correct", "If-Match", fmt.Sprintf(`"%d"`, posted.Version)))
	if corr.Code != http.StatusOK {
		t.Fatalf("correct: %d %s", corr.Code, corr.Body)
	}
	var corrected receiptJSON
	_ = json.Unmarshal(corr.Body.Bytes(), &corrected)
	cancel := doWith(h, http.MethodPost, receiptsPath+"/"+r.ID+":cancel", `{"postingDate":"2026-10-29"}`,
		duty("receiving.receipt.post", "cw-cancel", "If-Match", fmt.Sprintf(`"%d"`, corrected.Version)))
	if cancel.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", cancel.Code, cancel.Body)
	}
	var adjustments []map[string]any
	for _, e := range st.Events() {
		if e.Type == "dev.passage.receiving.receipt.corrected.v1" {
			var m map[string]any
			if err := json.Unmarshal(e.Data, &m); err != nil {
				t.Fatal(err)
			}
			adjustments = append(adjustments, m)
		}
	}
	if len(adjustments) != 2 {
		t.Fatalf("%d receipt.corrected events, want the correction and the cancellation", len(adjustments))
	}
	for i, name := range []string{"receipt-corrected.json", "receipt-cancelled.json"} {
		raw, err := os.ReadFile(fixtureDir + name)
		if err != nil {
			t.Fatal(err)
		}
		var want map[string]any
		if err := json.Unmarshal(raw, &want); err != nil {
			t.Fatal(err)
		}
		got := adjustments[i]
		wantLine, gotLine := want["lines"].([]any)[0].(map[string]any), got["lines"].([]any)[0].(map[string]any)
		if !reflect.DeepEqual(sortedKeys(got), sortedKeys(want)) || !reflect.DeepEqual(sortedKeys(gotLine), sortedKeys(wantLine)) {
			t.Fatalf("%s: members differ from the fixture:\n got %v / %v\nwant %v / %v", name, sortedKeys(got), sortedKeys(gotLine), sortedKeys(want), sortedKeys(wantLine))
		}
		for _, k := range []string{"posting_date", "kind", "correction_seq", "currency"} {
			if !reflect.DeepEqual(got[k], want[k]) {
				t.Errorf("%s: %s = %v, fixture %v", name, k, got[k], want[k])
			}
		}
		if got["legal_entity_id"] != legalEntityID || got["receipt_id"] != r.ID {
			t.Errorf("%s: legal entity %v, receipt %v, want %s, %s", name, got["legal_entity_id"], got["receipt_id"], legalEntityID, r.ID)
		}
		for _, k := range []string{"line_no", "product_id", "qty_delta", "cw_qty_delta", "amount_delta", "amount_acct_delta"} {
			if !reflect.DeepEqual(gotLine[k], wantLine[k]) {
				t.Errorf("%s: line %s = %v, fixture %v", name, k, gotLine[k], wantLine[k])
			}
		}
	}
}
