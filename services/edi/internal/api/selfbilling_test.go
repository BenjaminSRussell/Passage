package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
)

const edifactDistributor = `{"name":"Demo distributor","isaQualifier":"ZZ","isaId":"DISTRIBUTOR","environment":"TEST","edifactId":"4012345000009","edifactQualifier":"14","edifactContrl":"NEVER"}`

func selfBilledSample(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/edifact/invoic-389-selfbilled.edi")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func receiveEdifactBody(t *testing.T, h http.Handler, body string) {
	t.Helper()
	rec := do(t, h, http.MethodPost, "/edi/v1/inbound/edifact?environment=TEST", body, "edi.operate", srcScope)
	if rec.Code != http.StatusCreated {
		t.Fatalf("receive: %d %s", rec.Code, rec.Body.String())
	}
}

// sendShipment generates the 856 of the shipment the fake serves, which registers what shipped for self-billing.
func sendShipment(t *testing.T, f *fakeSources, h http.Handler, partner, id, number string, shippedOn time.Time) {
	t.Helper()
	f.shipment.ID, f.shipment.ShipmentNumber, f.shipment.ActualShipDate = id, number, &shippedOn
	// Every shipment's containers have their own SSCCs, as real ones do: a container is notified to a partner once.
	for i := range f.shipment.Containers {
		f.shipment.Containers[i].SSCC = testSSCC(fmt.Sprintf("0034567%06d%04d", len(number)*1000+int(number[len(number)-1]), i))
	}
	if rec := fromSource(t, h, partner, "856", id); rec.Code != http.StatusCreated {
		t.Fatalf("856 %s: %d %s", number, rec.Code, rec.Body.String())
	}
}

func disputes(t *testing.T, h http.Handler, status string) []store.SelfBillDispute {
	t.Helper()
	rec := do(t, h, http.MethodGet, "/edi/v1/self-billing/disputes?status="+status, "", "edi.self-billing.reconcile", srcScope)
	var out struct {
		Items []store.SelfBillDispute `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("disputes: %d %s", rec.Code, rec.Body.String())
	}
	return out.Items
}

func selfBillSetup(t *testing.T) (*fakeSources, *API, http.Handler, string) {
	t.Helper()
	f := newFake()
	f.order.Lines[0].UnitPrice = decimal.NewFromInt(8)
	a, h, _, partner, _ := autoSetupWith(t, f, edifactDistributor)
	if rec := do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/self-billing", `{"taxRatePct":"8.5","waitDays":30}`, "edi.admin", srcScope); rec.Code != http.StatusOK {
		t.Fatalf("config: %d %s", rec.Code, rec.Body.String())
	}
	return f, a, h, partner
}

func TestSelfBilling_MatchedDocumentClaimsItsShipment(t *testing.T) {
	f, _, h, partner := selfBillSetup(t)
	sendShipment(t, f, h, partner, srcShipment, "SHP-0001", time.Now().AddDate(0, 0, -3))

	receiveEdifactBody(t, h, selfBilledSample(t))
	rec := do(t, h, http.MethodGet, "/edi/v1/self-billing/documents", "", "edi.self-billing.reconcile", srcScope)
	var docs struct {
		Items []store.SelfBillDocument `json:"items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &docs)
	if len(docs.Items) != 1 || docs.Items[0].Number != "SB-9001" || docs.Items[0].Status != "MATCHED" || docs.Items[0].MatchedLines != 1 {
		t.Fatalf("documents: %s", rec.Body.String())
	}
	if n := len(disputes(t, h, "OPEN")); n != 0 {
		t.Fatalf("a matched document opens no dispute, got %d", n)
	}
	// The message is POSTED, not left for the sweep.
	rec = do(t, h, http.MethodGet, "/edi/v1/messages?direction=IN&transactionSet=INVOIC", "", "edi.operate", srcScope)
	if !strings.Contains(rec.Body.String(), `"status":"POSTED"`) {
		t.Fatalf("message: %s", rec.Body.String())
	}
	// The same document number again is a duplicate: the message is parked, nothing is matched twice.
	receiveEdifactBody(t, h, strings.Replace(selfBilledSample(t), "SB0001", "SB0002", 2))
	var n int
	rec = do(t, h, http.MethodGet, "/edi/v1/self-billing/documents", "", "edi.self-billing.reconcile", srcScope)
	_ = json.Unmarshal(rec.Body.Bytes(), &docs)
	n = len(docs.Items)
	if n != 1 {
		t.Fatalf("a repeated document number must not be matched again: %d documents", n)
	}
	rec = do(t, h, http.MethodGet, "/edi/v1/messages?direction=IN&status=FAILED", "", "edi.operate", srcScope)
	if !strings.Contains(rec.Body.String(), `"INVOIC"`) {
		t.Fatalf("the duplicate must be parked FAILED: %s", rec.Body.String())
	}
}

func TestSelfBilling_PriceDifferenceAndUnmatchedLineOpenDisputes(t *testing.T) {
	f, _, h, partner := selfBillSetup(t)
	sendShipment(t, f, h, partner, srcShipment, "SHP-0001", time.Now().AddDate(0, 0, -3))
	other := "00000000-0000-0000-0000-0000000000b3"
	sendShipment(t, f, h, partner, other, "SHP-0002", time.Now().AddDate(0, 0, -2))

	// SB-9002 bills SHP-0002 at 9 instead of 8 (tax recomputed at their price, so only the price differs).
	priced := strings.NewReplacer("SB-9001", "SB-9002", "SB0001", "SB0002", "SHP-0001", "SHP-0002", "MOA+203:80.00", "MOA+203:90.00", "PRI+AAA:8", "PRI+AAA:9",
		"MOA+124:6.80", "MOA+124:7.65", "MOA+86:86.80", "MOA+86:97.65").Replace(selfBilledSample(t))
	receiveEdifactBody(t, h, priced)
	// SB-9003 bills a delivery note we never shipped.
	ghost := strings.NewReplacer("SB-9001", "SB-9003", "SB0001", "SB0003", "SHP-0001", "SHP-9999").Replace(selfBilledSample(t))
	receiveEdifactBody(t, h, ghost)

	open := disputes(t, h, "OPEN")
	kinds := map[string]store.SelfBillDispute{}
	for _, d := range open {
		kinds[d.Kind] = d
	}
	if len(open) != 2 || kinds["DIFFERENCE"].Code != domain.CodeSelfBillDifference || kinds["UNMATCHED"].Code != domain.CodeSelfBillUnmatched ||
		!strings.Contains(kinds["DIFFERENCE"].Detail, "unitPrice 9") || !strings.Contains(kinds["DIFFERENCE"].Detail, "ours 8") {
		t.Fatalf("disputes: %+v", open)
	}
	if kinds["DIFFERENCE"].ShipmentID == "" || kinds["UNMATCHED"].ShipmentID != "" {
		t.Fatalf("only the difference points at a shipment: %+v", open)
	}

	// Deciding the difference resolves its document; deciding twice is a conflict.
	resolve := "/edi/v1/self-billing/disputes/" + kinds["DIFFERENCE"].ID + ":resolve"
	if rec := do(t, h, http.MethodPost, resolve, `{"decision":"MAYBE"}`, "edi.self-billing.reconcile", srcScope); rec.Code != http.StatusBadRequest {
		t.Fatalf("decision: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, resolve, `{"decision":"ACCEPTED","note":"price change agreed by phone"}`, "edi.self-billing.reconcile", srcScope); rec.Code != http.StatusOK {
		t.Fatalf("resolve: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPost, resolve, `{"decision":"ACCEPTED"}`, "edi.self-billing.reconcile", srcScope); rec.Code != http.StatusConflict {
		t.Fatalf("twice: %d", rec.Code)
	}
	rec := do(t, h, http.MethodGet, "/edi/v1/self-billing/documents?status=RESOLVED", "", "edi.self-billing.reconcile", srcScope)
	if !strings.Contains(rec.Body.String(), "SB-9002") || strings.Contains(rec.Body.String(), "SB-9003") {
		t.Fatalf("resolved documents: %s", rec.Body.String())
	}
	if n := len(disputes(t, h, "OPEN")); n != 1 {
		t.Fatalf("the unmatched dispute stays open: %d", n)
	}
	// The operate duty cannot decide disputes.
	if rec := do(t, h, http.MethodPost, "/edi/v1/self-billing/disputes/"+kinds["UNMATCHED"].ID+":resolve", `{"decision":"REJECTED"}`, "edi.operate", srcScope); rec.Code != http.StatusForbidden {
		t.Fatalf("duty: %d", rec.Code)
	}
}

func TestSelfBilling_UnbilledShipmentIsDisputedOnceAfterTheWait(t *testing.T) {
	f, _, h, partner := selfBillSetup(t)
	sendShipment(t, f, h, partner, srcShipment, "SHP-0001", time.Now().AddDate(0, 0, -31))
	fresh := "00000000-0000-0000-0000-0000000000b4"
	sendShipment(t, f, h, partner, fresh, "SHP-0004", time.Now().AddDate(0, 0, -5))

	sweep := func() int {
		rec := do(t, h, http.MethodPost, "/edi/v1/self-billing:sweep", "", "edi.self-billing.reconcile", srcScope)
		var out struct {
			Unbilled []store.SelfBillDispute `json:"unbilled"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("sweep: %d %s", rec.Code, rec.Body.String())
		}
		return len(out.Unbilled)
	}
	if n := sweep(); n != 1 {
		t.Fatalf("one shipment is past 30 days, got %d", n)
	}
	if n := sweep(); n != 0 {
		t.Fatalf("a shipment is disputed once, got %d on the second sweep", n)
	}
	open := disputes(t, h, "OPEN")
	if len(open) != 1 || open[0].Kind != "UNBILLED" || open[0].Code != domain.CodeSelfBillUnbilled || !strings.Contains(open[0].Detail, "SHP-0001") {
		t.Fatalf("unbilled dispute: %+v", open)
	}
	// Accepting it closes the shipment as billed outside the system, so it is not disputed again.
	if rec := do(t, h, http.MethodPost, "/edi/v1/self-billing/disputes/"+open[0].ID+":resolve", `{"decision":"ACCEPTED","note":"billed by hand"}`, "edi.self-billing.reconcile", srcScope); rec.Code != http.StatusOK {
		t.Fatalf("resolve: %d", rec.Code)
	}
	if n := sweep(); n != 0 {
		t.Fatalf("closed shipment disputed again: %d", n)
	}
}

func TestSelfBilling_SweepMatchesAMessageThatMissedItsTradeProfile(t *testing.T) {
	f := newFake()
	f.order.Lines[0].UnitPrice = decimal.NewFromInt(8)
	a, h, pool, partner, _ := autoSetupWith(t, f, edifactDistributor)
	sendShipment(t, f, h, partner, srcShipment, "SHP-0001", time.Now().AddDate(0, 0, -3))
	// A message mapped while the match was unavailable (a database hiccup after receipt) waits as MAPPED; the sweep picks it up.
	receiveEdifactBody(t, h, selfBilledSample(t))
	if _, err := pool.Exec(context.Background(), `update message set status = 'MAPPED' where transaction_set = 'INVOIC'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `delete from self_billing_line; delete from self_billing_dispute; delete from self_billing_document;
		update self_billing_shipment set billed_by = null`); err != nil {
		t.Fatal(err)
	}
	done, err := a.ReconcileSelfBilled(store.SystemContext(context.Background(), "00000000-0000-0000-0000-000000000007"), 10)
	if err != nil || done != 1 {
		t.Fatalf("sweep matched %d, %v", done, err)
	}
}

func TestSelfBilling_ConfigValidation(t *testing.T) {
	_, _, h, partner := selfBillSetup(t)
	for _, body := range []string{`{"waitDays":500}`, `{"priceTolerance":"-1"}`, `{"taxRatePct":"101"}`, `{"quantityTolerance":"x"}`} {
		if rec := do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/self-billing", body, "edi.admin", srcScope); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", body, rec.Code)
		}
	}
	rec := do(t, h, http.MethodGet, "/edi/v1/partners/"+partner+"/self-billing", "", "edi.admin", srcScope)
	if !strings.Contains(rec.Body.String(), `"taxRatePct":"8.5"`) || !strings.Contains(rec.Body.String(), `"waitDays":30`) {
		t.Fatalf("get: %s", rec.Body.String())
	}
}

// The receipt and the sweep can both take up one message. The run that comes second finds the document the first one matched for
// this very message: it neither matches it again nor parks the message as a duplicate of itself.
func TestSelfBilling_SecondRunOverTheSameMessageIsANoOp(t *testing.T) {
	f := newFake()
	f.order.Lines[0].UnitPrice = decimal.NewFromInt(8)
	a, h, pool, partner, _ := autoSetupWith(t, f, edifactDistributor)
	sendShipment(t, f, h, partner, srcShipment, "SHP-0001", time.Now().AddDate(0, 0, -3))
	receiveEdifactBody(t, h, selfBilledSample(t))
	// The second run listed the message while it was still MAPPED.
	if _, err := pool.Exec(context.Background(), `update message set status = 'MAPPED' where transaction_set = 'INVOIC'`); err != nil {
		t.Fatal(err)
	}
	done, err := a.ReconcileSelfBilled(store.SystemContext(context.Background(), "00000000-0000-0000-0000-000000000007"), 10)
	if err != nil || done != 0 {
		t.Fatalf("second run: matched %d, %v", done, err)
	}
	var docs, failed int
	if err := pool.QueryRow(context.Background(), `select (select count(*) from self_billing_document), (select count(*) from message where status = 'FAILED')`).Scan(&docs, &failed); err != nil {
		t.Fatal(err)
	}
	if docs != 1 || failed != 0 {
		t.Fatalf("documents %d, parked messages %d; want 1 and 0", docs, failed)
	}
}

// The scheduled sweep finds the tenant with an overdue shipment through the cross-tenant function and opens its dispute once.
func TestSelfBilling_ScheduledSweepOpensTheUnbilledDisputeOnce(t *testing.T) {
	f := newFake()
	a, h, pool, partner, _ := autoSetup(t, f)
	do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/self-billing", `{"waitDays":30}`, "edi.admin", srcScope)
	sendShipment(t, f, h, partner, srcShipment, "SHP-0001", time.Now().UTC().AddDate(0, 0, -45))
	for i := 0; i < 2; i++ {
		if _, err := a.RunSelfBillSweep(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := pool.QueryRow(context.Background(), `select count(*) from self_billing_dispute where kind = 'UNBILLED'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("unbilled disputes: %d %v", n, err)
	}
	if tenants, err := a.store.SelfBillDueTenants(context.Background()); err != nil || len(tenants) != 0 {
		t.Fatalf("nothing is due after the sweep: %v %v", tenants, err)
	}
}

// testSSCC appends the GS1 mod-10 check digit to 17 digits.
func testSSCC(body string) string {
	sum := 0
	for i := 0; i < len(body); i++ {
		d := int(body[len(body)-1-i] - '0')
		if i%2 == 0 {
			d *= 3
		}
		sum += d
	}
	return body + string(rune('0'+(10-sum%10)%10))
}
