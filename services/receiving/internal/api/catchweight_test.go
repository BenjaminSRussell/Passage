package api_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

const catchSpec = `{"nominal":"10","min":"9.8","max":"11"}`

func catchLine(unit, qty, extra string) string {
	return fmt.Sprintf(`{"vendorId":%q,"purchaseOrderId":%q,"legalEntityId":%q,"packingSlip":"PS-CW","receiptDate":"2026-10-25","currency":"USD","lines":[{"poLineNo":1,"productId":%q,"overDeliveryPct":5,"unit":%q,"qty":%s,"locationId":%q%s}]}`,
		vendorID, poID, legalEntityID, productID, unit, qty, locationID, extra)
}

func TestCatchWeightReceipt_CapsWeightAgainstTheWeightUnitPOLine(t *testing.T) {
	cw := `,"cwQty":10,"catchWeight":` + catchSpec
	r, _ := newPurchasingRouter(t, poOverrides{unit: "KG", qty: "100", received: "0"})

	rec := doWith(r, http.MethodPost, receiptsPath, catchLine("KG", "106", cw), maintainer("k1"))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "RCV-OVR-001") {
		t.Fatalf("106 kg against 100 kg (+5%%) must trip over-delivery: %d %s", rec.Code, rec.Body)
	}
	rec = doWith(r, http.MethodPost, receiptsPath, catchLine("KG", "102", cw), maintainer("k2"))
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"cwQty":10`) || !strings.Contains(rec.Body.String(), `"qty":"102"`) {
		t.Fatalf("102 kg within the allowance must register with cwQty 10: %d %s", rec.Code, rec.Body)
	}
}

func TestCatchWeightReceipt_RefusesAPOLineThatIsNotAWeightUnit(t *testing.T) {
	r, _ := newPurchasingRouter(t, poOverrides{unit: "CS", qty: "10", received: "0"})
	rec := doWith(r, http.MethodPost, receiptsPath, catchLine("CS", "102", `,"cwQty":10,"catchWeight":`+catchSpec), maintainer("k1"))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), domain.CodeCatchWeightUnit) {
		t.Fatalf("a case-priced PO line cannot take a catch-weight receipt: %d %s", rec.Code, rec.Body)
	}
}

func TestCatchWeightReceipt_RefusesWeightOutsideTheBand(t *testing.T) {
	r, _ := newPurchasingRouter(t, poOverrides{unit: "KG", qty: "1000", received: "0"})
	rec := doWith(r, http.MethodPost, receiptsPath, catchLine("KG", "95", `,"cwQty":10,"catchWeight":`+catchSpec), maintainer("k1"))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), domain.CodeCatchWeight) {
		t.Fatalf("95 kg for 10 cases (min 9.8) must be refused: %d %s", rec.Code, rec.Body)
	}
}

func TestCatchWeightReceipt_ValidatesTheRequest(t *testing.T) {
	r, _ := newPurchasingRouter(t, poOverrides{unit: "KG", qty: "1000", received: "0"})
	tests := []struct{ name, extra, qty, field string }{
		{"cwQty without spec", `,"cwQty":10`, "102", "lines[0].cwQty"},
		{"spec without cwQty", `,"catchWeight":` + catchSpec, "102", "lines[0].cwQty"},
		{"cwQty zero", `,"cwQty":0,"catchWeight":` + catchSpec, "102", "lines[0].cwQty"},
		{"all-zero spec", `,"cwQty":10,"catchWeight":{"nominal":"0","min":"0","max":"0"}`, "102", "lines[0].catchWeight"},
		{"min above max", `,"cwQty":10,"catchWeight":{"nominal":"10","min":"12","max":"11"}`, "102", "lines[0].catchWeight"},
		{"nominal outside band", `,"cwQty":10,"catchWeight":{"nominal":"12","min":"9.8","max":"11"}`, "102", "lines[0].catchWeight"},
		{"seven decimals", `,"cwQty":10,"catchWeight":{"nominal":"10","min":"9.8000001","max":"11"}`, "102", "lines[0].catchWeight"},
		{"oversized spec", `,"cwQty":10,"catchWeight":{"nominal":"10","min":"9.8","max":"100000000000000000"}`, "102", "lines[0].catchWeight"},
		{"seven decimal weight", `,"cwQty":10,"catchWeight":` + catchSpec, "102.0000001", "lines[0].qty"},
		{"oversized cwQty", `,"cwQty":3000000000,"catchWeight":` + catchSpec, "102", "lines[0].cwQty"},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := doWith(r, http.MethodPost, receiptsPath, catchLine("KG", tc.qty, tc.extra), maintainer(fmt.Sprintf("v%d", i)))
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.field) {
				t.Fatalf("got %d %s, want 400 naming %s", rec.Code, rec.Body, tc.field)
			}
		})
	}
}

// postedCatchReceipt is a posted direct receipt of 10 cases at 102 kg against
// 100 kg ordered (+5%).
func postedCatchReceipt(t *testing.T, rg *rig) (receiptJSON, string) {
	t.Helper()
	body := strings.Replace(directBody("PS-CW", 100, 102, `,"cwQty":10,"catchWeight":`+catchSpec), `"unit":"EA"`, `"unit":"KG"`, 1)
	r := decode[receiptJSON](t, rg.call(http.MethodPost, receiptsPath, body, rg.maintain()), http.StatusCreated)
	posted := decode[receiptJSON](t, rg.call(http.MethodPost, receiptsPath+"/"+r.ID+":post", "", rg.poster(r.Version)), http.StatusOK)
	return posted, receiptsPath + "/" + r.ID + ":correct"
}

func TestDirectReceipt_CatchWeightNeedsAMassUnitWithoutAPOLine(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	rec := rg.call(http.MethodPost, receiptsPath, directBody("PS-CW", 100, 102, `,"cwQty":10,"catchWeight":`+catchSpec), rg.maintain())
	problemCode(t, rec, http.StatusUnprocessableEntity, domain.CodeCatchWeightUnit)
}

// TestCorrect_CatchWeightCaseCount: a short-case correction (9 of 10 cases,
// 92 kg) moves the count with the weight and BR-24.5 checks the new pair.
func TestCorrect_CatchWeightCaseCount(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	posted, path := postedCatchReceipt(t, rg)
	body := func(qty, cases string) string {
		return fmt.Sprintf(`{"lines":[{"lineNo":1,"qtyDelta":%s,"cwQtyDelta":%s}]}`, qty, cases)
	}
	problemCode(t, rg.call(http.MethodPost, path, `{"lines":[{"lineNo":1,"qtyDelta":-10}]}`, rg.poster(posted.Version)), http.StatusUnprocessableEntity, domain.CodeCatchWeight)
	problemCode(t, rg.call(http.MethodPost, path, body("-10", "-10"), rg.poster(posted.Version)), http.StatusUnprocessableEntity, domain.CodeInvalidReceipt)
	problemCode(t, rg.call(http.MethodPost, path, body("-30", "-1"), rg.poster(posted.Version)), http.StatusUnprocessableEntity, domain.CodeCatchWeight)
	if rec := rg.call(http.MethodPost, path, body("-10", "0"), rg.poster(posted.Version)); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "lines[0].cwQtyDelta") {
		t.Fatalf("zero case delta: %d %s", rec.Code, rec.Body)
	}
	if rec := rg.call(http.MethodPost, path, body(`"-10.0000001"`, "-1"), rg.poster(posted.Version)); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "lines[0].qtyDelta") {
		t.Fatalf("seven-decimal weight delta: %d %s", rec.Code, rec.Body)
	}
	got := rg.call(http.MethodPost, path, body("-10", "-1"), rg.poster(posted.Version))
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"qty":"92"`) || !strings.Contains(got.Body.String(), `"cwQty":9`) {
		t.Fatalf("9 cases at 92 kg is inside 88.2-99 and must correct: %d %s", got.Code, got.Body)
	}

	plain := newRig(domain.PeriodOpen)
	p := plain.posted(t, "PS-P", 10, 4)
	problemCode(t, plain.call(http.MethodPost, receiptsPath+"/"+p.ID+":correct", body("1", "1"), plain.poster(p.Version)), http.StatusUnprocessableEntity, domain.CodeInvalidReceipt)
}

func TestCorrect_CatchWeightReceiptKeepsTheBandAndTheOverDeliveryCap(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	posted, path := postedCatchReceipt(t, rg)
	delta := func(d int) string { return fmt.Sprintf(`{"lines":[{"lineNo":1,"qtyDelta":%d}]}`, d) }

	problemCode(t, rg.call(http.MethodPost, path, delta(-10), rg.poster(posted.Version)), http.StatusUnprocessableEntity, domain.CodeCatchWeight)
	problemCode(t, rg.call(http.MethodPost, path, delta(8), rg.poster(posted.Version)), http.StatusUnprocessableEntity, "RCV-OVR-001")
	got := decode[receiptJSON](t, rg.call(http.MethodPost, path, delta(3), rg.poster(posted.Version)), http.StatusOK)
	if got.Lines[0].Qty != "105" {
		t.Fatalf("105 kg is inside 98-110 and must correct: %+v", got.Lines[0])
	}
}
