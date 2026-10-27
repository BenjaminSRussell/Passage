package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

// DeliveredNotInvoiced records the filter and answers with the canned rows.
func (f *fakeStore) DeliveredNotInvoiced(_ context.Context, filter store.DNIFilter) ([]domain.DNIRow, error) {
	f.dniFilter = filter
	return f.dniRows, nil
}

func dniRow(number, currency, amount string, posted time.Time) domain.DNIRow {
	return domain.DNIRow{PackingSlipID: ids.New(), PackingSlipNumber: number, Currency: currency,
		Amount: decimal.RequireFromString(amount), PostingDate: posted}
}

type dniBody struct {
	LegalEntityID string            `json:"legalEntityId"`
	CustomerID    *string           `json:"customerId"`
	AsOf          string            `json:"asOf"`
	Account       string            `json:"account"`
	Totals        []domain.DNITotal `json:"totals"`
	LineCount     int               `json:"lineCount"`
	Lines         []struct {
		PackingSlipNumber string `json:"packingSlipNumber"`
		Amount            string `json:"amount"`
		DaysOutstanding   int    `json:"daysOutstanding"`
	} `json:"lines"`
}

func TestDeliveredNotInvoiced_TotalsPerCurrencyAndAging(t *testing.T) {
	le, customer := ids.New(), ids.New()
	fs := &fakeStore{dniRows: []domain.DNIRow{
		dniRow("PS-0000001", "USD", "125.00", time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)),
		dniRow("PS-0000002", "EUR", "40.10", time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)),
		dniRow("PS-0000003", "USD", "7.50", time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)),
	}}
	h := newServer(t, fs)
	rec := do(t, h, http.MethodGet, "/api/v1/fulfillment/delivered-not-invoiced?legalEntityId="+le+"&customerId="+customer+"&asOf=2026-10-21", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if fs.dniFilter.LegalEntityID != le || fs.dniFilter.CustomerID != customer || !fs.dniFilter.AsOf.Equal(time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("store filter = %+v", fs.dniFilter)
	}
	var out dniBody
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Account != "1405" || out.AsOf != "2026-10-21" || out.CustomerID == nil || *out.CustomerID != customer || out.LineCount != 3 {
		t.Fatalf("header = %+v", out)
	}
	if len(out.Totals) != 2 || out.Totals[0].Currency != "EUR" || out.Totals[1].Currency != "USD" ||
		out.Totals[1].Count != 2 || !out.Totals[1].Amount.Equal(decimal.RequireFromString("132.50")) {
		t.Fatalf("totals = %+v, want EUR then USD 2 slips 132.50", out.Totals)
	}
	if len(out.Lines) != 3 || out.Lines[0].DaysOutstanding != 3 || out.Lines[1].DaysOutstanding != 1 || out.Lines[2].DaysOutstanding != 0 {
		t.Fatalf("lines = %+v, want ages 3, 1, 0", out.Lines)
	}
}

func TestDeliveredNotInvoiced_LimitCutsLinesNotTotals(t *testing.T) {
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	fs := &fakeStore{dniRows: []domain.DNIRow{
		dniRow("PS-0000001", "USD", "10", day), dniRow("PS-0000002", "USD", "20", day), dniRow("PS-0000003", "USD", "30", day),
	}}
	rec := do(t, newServer(t, fs), http.MethodGet, "/api/v1/fulfillment/delivered-not-invoiced?legalEntityId="+ids.New()+"&limit=2", "", nil)
	var out dniBody
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if out.LineCount != 3 || len(out.Lines) != 2 || len(out.Totals) != 1 || !out.Totals[0].Amount.Equal(decimal.NewFromInt(60)) {
		t.Fatalf("lineCount=%d lines=%d totals=%+v, want 3, 2 and 60", out.LineCount, len(out.Lines), out.Totals)
	}
}

func TestDeliveredNotInvoiced_DefaultsToTodayAndEmptyLists(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	rec := do(t, h, http.MethodGet, "/api/v1/fulfillment/delivered-not-invoiced?legalEntityId="+ids.New(), "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var out dniBody
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.AsOf != time.Now().UTC().Format("2006-01-02") || out.CustomerID != nil || out.Totals == nil || out.Lines == nil || out.LineCount != 0 {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if fs.dniFilter.AsOf.Hour() != 0 || fs.dniFilter.CustomerID != "" {
		t.Fatalf("filter = %+v, want a date without clock and no customer", fs.dniFilter)
	}
}

func TestDeliveredNotInvoiced_Validation(t *testing.T) {
	le := ids.New()
	for name, query := range map[string]string{
		"legalEntityId": "",
		"customerId":    "?legalEntityId=" + le + "&customerId=nope",
		"asOf":          "?legalEntityId=" + le + "&asOf=21-10-2026",
		"limit":         "?legalEntityId=" + le + "&limit=0",
	} {
		t.Run(name, func(t *testing.T) {
			fs := &fakeStore{}
			rec := do(t, newServer(t, fs), http.MethodGet, "/api/v1/fulfillment/delivered-not-invoiced"+query, "", nil)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body = %s, want 400", rec.Code, rec.Body.String())
			}
			var p struct {
				Code   string            `json:"code"`
				Errors map[string]string `json:"errors"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &p)
			if p.Code != CodeValidation || p.Errors[name] == "" {
				t.Fatalf("problem = %s, want %s on %s", rec.Body.String(), CodeValidation, name)
			}
			if fs.dniFilter.LegalEntityID != "" {
				t.Fatal("store was called for an invalid request")
			}
		})
	}
}

func TestDeliveredNotInvoiced_NeedsWarehouseView(t *testing.T) {
	rec := do(t, newServer(t, &fakeStore{}), http.MethodGet, "/api/v1/fulfillment/delivered-not-invoiced?legalEntityId="+ids.New(), "",
		map[string]string{"X-Passage-Duties": "WarehousePick"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}
