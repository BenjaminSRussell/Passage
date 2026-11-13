package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
	"github.com/BenjaminSRussell/passage/services/edi/internal/taxrate"
)

// selfBilled810 is the customer's X12 810 billing itself for SHP-0001: 10 EA of prod-a at price, with the line tax and the total.
func selfBilled810(t *testing.T, number, price, tax, totalCents string) string {
	t.Helper()
	ic := domain.Interchange{
		Delims:   domain.Delims{Element: '*', Component: '>', Segment: '~'},
		Sender:   domain.Party{Qualifier: "ZZ", ID: "DISTRIBUTOR"},
		Receiver: domain.Party{Qualifier: "ZZ", ID: "MANUFACTURER"},
		Date:     "261111", Time: "0900", Version: "00401", Control: "000000081", Usage: "T",
		Groups: []domain.Group{{
			FunctionalID: "IN", Sender: "DISTRIBUTOR", Receiver: "MANUFACTURER",
			Date: "20261111", Time: "0900", Control: "81", Version: "004010",
			Transactions: []domain.Transaction{{SetID: "810", Control: "0081", Body: []domain.Segment{
				{ID: "BIG", Elements: []string{"20261111", number, "20261015", "PO-1001", "", "", "DI"}},
				{ID: "CUR", Elements: []string{"BY", "USD"}},
				{ID: "REF", Elements: []string{"PK", "SHP-0001"}},
				{ID: "IT1", Elements: []string{"10", "10", "EA", price, "", "VP", "prod-a"}},
				{ID: "TXI", Elements: []string{"VA", tax, "8.5"}},
				{ID: "TDS", Elements: []string{totalCents}},
				{ID: "CTT", Elements: []string{"1"}},
			}}},
		}},
	}
	raw, err := domain.Serialize(ic)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func documents(t *testing.T, h http.Handler) []store.SelfBillDocument {
	t.Helper()
	rec := do(t, h, http.MethodGet, "/edi/v1/self-billing/documents", "", "edi.self-billing.reconcile", srcScope)
	var out struct {
		Items []store.SelfBillDocument `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Items
}

// #35 (35-5): a customer's X12 810 is read as its self-billed invoice and matched against the shipment our 856 registered.
func TestSelfBilling_X12810IsMatchedLikeAnINVOIC389(t *testing.T) {
	f, _, h, partner := selfBillSetup(t)
	sendShipment(t, f, h, partner, srcShipment, "SHP-0001", time.Now().AddDate(0, 0, -3))
	rec := do(t, h, http.MethodPost, "/edi/v1/inbound/x12?environment=TEST", selfBilled810(t, "SB-8101", "8", "6.80", "8680"), "edi.operate", srcScope)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"ack997"`) {
		t.Fatalf("receive: %d %s", rec.Code, rec.Body.String())
	}
	docs := documents(t, h)
	if len(docs) != 1 || docs[0].Number != "SB-8101" || docs[0].Status != "MATCHED" || docs[0].MatchedLines != 1 {
		t.Fatalf("documents: %+v", docs)
	}
	if n := len(disputes(t, h, "OPEN")); n != 0 {
		t.Fatalf("a matched 810 opens no dispute, got %d", n)
	}
}

func TestMap810(t *testing.T) {
	inv, issues := domain.Map810(domain.Transaction{SetID: "810", Body: []domain.Segment{
		{ID: "BIG", Elements: []string{"20261111", "SB-1", "20261015", "PO-1", "", "", "CR"}},
		{ID: "CUR", Elements: []string{"BY", "EUR"}},
		{ID: "REF", Elements: []string{"BM", "BOL-7"}},
		{ID: "IT1", Elements: []string{"1", "2", "EA", "5.25", "", "VP", "p-1", "BP", "c-1"}},
		{ID: "REF", Elements: []string{"LI", "20"}},
		{ID: "TXI", Elements: []string{"VA", "2.10"}},
		{ID: "IT1", Elements: []string{"2", "1", "EA", "3"}},
		{ID: "REF", Elements: []string{"PK", "SHP-9"}},
		{ID: "TDS", Elements: []string{"1560"}},
		{ID: "TXI", Elements: []string{"VA", "0.00"}},
	}})
	if len(issues) != 0 {
		t.Fatalf("issues: %+v", issues)
	}
	if !inv.SelfBilled || !inv.CreditNote || inv.Currency != "EUR" || inv.Number != "SB-1" || inv.BOL != "BOL-7" || !inv.Total.Equal(decimal.RequireFromString("15.60")) ||
		len(inv.Lines) != 2 || inv.Lines[0].POLine != "20" || inv.Lines[0].DeliveryNote != "BOL-7" || inv.Lines[0].Tax == nil || !inv.Lines[0].Tax.Equal(decimal.RequireFromString("2.10")) ||
		len(inv.Lines[0].ItemRefs) != 2 || inv.Lines[1].POLine != "2" || inv.Lines[1].DeliveryNote != "SHP-9" || len(inv.Taxes) != 1 {
		t.Fatalf("invoice %+v", inv)
	}
	if _, issues := domain.Map810(domain.Transaction{SetID: "810", Body: []domain.Segment{{ID: "BIG", Elements: []string{"2026", "X"}}}}); len(issues) == 0 {
		t.Fatal("an 810 without date or lines is rejected")
	}
}

// fakeTax answers a fixed rate, or fails.
type fakeTax struct {
	rate  decimal.Decimal
	err   error
	calls []taxrate.Query
}

func (f *fakeTax) RatePct(_ context.Context, q taxrate.Query) (decimal.Decimal, error) {
	f.calls = append(f.calls, q)
	return f.rate, f.err
}

// #35 (35-5): with a tax country on the partner, the self-billed tax is checked at the rate the tax service determines on the
// document date, not the partner's typed rate; a tax outage leaves the message for the next run.
func TestSelfBilling_VATRateComesFromTheTaxService(t *testing.T) {
	f, a, h, partner := selfBillSetup(t) // partner rate 8.5
	if rec := do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/self-billing", `{"taxRatePct":"8.5","waitDays":30,"taxCountry":"DE","taxGroup":"DOM","itemTaxGroup":"STD"}`, "edi.admin", srcScope); rec.Code != http.StatusOK {
		t.Fatalf("config: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/self-billing", `{"taxCountry":"Germany"}`, "edi.admin", srcScope); rec.Code != http.StatusBadRequest {
		t.Fatalf("country: %d", rec.Code)
	}
	sendShipment(t, f, h, partner, srcShipment, "SHP-0001", time.Now().AddDate(0, 0, -3))
	tax := &fakeTax{err: errors.New("tax down")}
	a.SelfBillTax = tax
	// The customer charged 19% (15.20 on 80): at the partner's 8.5% that is a difference, at the tax service's 19% it matches.
	body := strings.NewReplacer("MOA+124:6.80", "MOA+124:15.20", "MOA+86:86.80", "MOA+86:95.20", "TAX+7+STT+++:::8.5", "TAX+7+VAT+++:::19").Replace(selfBilledSample(t))
	body = strings.NewReplacer("DTM+137:20261111:102'", "DTM+137:20261111:102'CUX+2:USD:4'", "UNT+21+1", "UNT+22+1").Replace(body)
	receiveEdifactBody(t, h, body)
	if n := len(documents(t, h)); n != 0 {
		t.Fatalf("with tax down nothing is matched: %d", n)
	}
	tax.err, tax.rate = nil, decimal.NewFromInt(19)
	if n, err := a.ReconcileSelfBilled(consumerCtx(t), 10); err != nil || n != 1 {
		t.Fatalf("reconcile: %d %v", n, err)
	}
	docs := documents(t, h)
	if len(docs) != 1 || docs[0].Status != "MATCHED" {
		t.Fatalf("documents: %+v disputes %+v", docs, disputes(t, h, "OPEN"))
	}
	q := tax.calls[len(tax.calls)-1]
	if q.Country != "DE" || q.TaxGroup != "DOM" || q.ItemTaxGroup != "STD" || q.Currency != "USD" || q.LegalEntityID != srcLE || q.Date.Format("20060102") != "20261111" {
		t.Fatalf("tax query %+v", q)
	}
}

// #35 (35-5): receiving an interchange matches only the self-billed messages it brought in; another pending message is left to the
// sweep instead of being matched inside someone else's receipt.
func TestSelfBilling_ReceiptMatchesOnlyItsOwnMessages(t *testing.T) {
	f, a, h, partner := selfBillSetup(t)
	if rec := do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/self-billing", `{"taxRatePct":"8.5","waitDays":30,"taxCountry":"US"}`, "edi.admin", srcScope); rec.Code != http.StatusOK {
		t.Fatalf("config: %d %s", rec.Code, rec.Body.String())
	}
	sendShipment(t, f, h, partner, srcShipment, "SHP-0001", time.Now().AddDate(0, 0, -3))
	tax := &fakeTax{err: errors.New("tax down")}
	a.SelfBillTax = tax
	body := strings.NewReplacer("DTM+137:20261111:102'", "DTM+137:20261111:102'CUX+2:USD:4'", "UNT+21+1", "UNT+22+1").Replace(selfBilledSample(t))
	receiveEdifactBody(t, h, body) // stays MAPPED: the tax service is down
	tax.err, tax.rate = nil, decimal.RequireFromString("8.5")
	rec := do(t, h, http.MethodPost, "/edi/v1/inbound/x12?environment=TEST", selfBilled810(t, "SB-8102", "8", "6.80", "8680"), "edi.operate", srcScope)
	if rec.Code != http.StatusCreated {
		t.Fatalf("receive: %d %s", rec.Code, rec.Body.String())
	}
	docs := documents(t, h)
	if len(docs) != 1 || docs[0].Number != "SB-8102" {
		t.Fatalf("the 810's receipt must match only the 810: %+v", docs)
	}
	if n, err := a.ReconcileSelfBilled(consumerCtx(t), 10); err != nil || n != 1 {
		t.Fatalf("the sweep picks up the INVOIC: %d %v", n, err)
	}
}

// #35 (35-5): when the tax service determines no rate (no tax code row, or its fallback) the partner's rate applies; it is not 0%.
func TestSelfBilling_NoTaxDeterminationKeepsThePartnersRate(t *testing.T) {
	f, a, h, partner := selfBillSetup(t) // partner rate 8.5, the sample bills 6.80 on 80
	if rec := do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/self-billing", `{"taxRatePct":"8.5","waitDays":30,"taxCountry":"US"}`, "edi.admin", srcScope); rec.Code != http.StatusOK {
		t.Fatalf("config: %d %s", rec.Code, rec.Body.String())
	}
	sendShipment(t, f, h, partner, srcShipment, "SHP-0001", time.Now().AddDate(0, 0, -3))
	a.SelfBillTax = &fakeTax{err: taxrate.ErrNoDetermination}
	body := strings.NewReplacer("DTM+137:20261111:102'", "DTM+137:20261111:102'CUX+2:USD:4'", "UNT+21+1", "UNT+22+1").Replace(selfBilledSample(t))
	receiveEdifactBody(t, h, body)
	docs := documents(t, h)
	if len(docs) != 1 || docs[0].Status != "MATCHED" {
		t.Fatalf("documents %+v, disputes %+v", docs, disputes(t, h, "OPEN"))
	}
}
