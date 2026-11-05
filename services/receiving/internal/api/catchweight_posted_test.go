package api_test

import (
	"encoding/json"
	"testing"

	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

// receipt.posted carries the case count and the per-case spec of a
// catch-weight line beside its weight (#480), and nothing extra on a plain line.
func TestPost_ReceiptPostedCarriesCatchWeight(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	postedCatchReceipt(t, rg)
	var data struct {
		Lines []map[string]any `json:"lines"`
	}
	evt := rg.receiptPostedEvent(t)
	if err := json.Unmarshal(evt.Data, &data); err != nil || len(data.Lines) != 1 {
		t.Fatalf("receipt.posted data %s err=%v", evt.Data, err)
	}
	l := data.Lines[0]
	if l["qty"] != "102" || l["unit"] != "KG" || l["cw_qty"] != float64(10) || l["cw_nominal"] != "10" || l["cw_min"] != "9.8" || l["cw_max"] != "11" || l["amount"] == nil {
		t.Fatalf("catch-weight line %v", l)
	}

	plain := newRig(domain.PeriodOpen)
	plain.posted(t, "PS-P", 10, 4)
	var pd struct {
		Lines []map[string]any `json:"lines"`
	}
	if err := json.Unmarshal(plain.receiptPostedEvent(t).Data, &pd); err != nil || len(pd.Lines) == 0 {
		t.Fatalf("plain receipt.posted: %v", err)
	}
	if _, ok := pd.Lines[0]["cw_qty"]; ok {
		t.Fatalf("plain line carries cw_qty: %v", pd.Lines[0])
	}
}
