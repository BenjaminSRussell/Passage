package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

// reportCalls records what the short-pick and returnable aging reports asked the store.
type reportCalls struct {
	backorderFilter store.BackorderFilter
	backorders      []store.BackorderRow
	agingCustomer   string
	aging           []store.ReturnableAgingRow
}

// ListBackorders answers from canned rows in their order, honouring the cursor
// and the limit the handler asks for, and records the filter.
func (f *fakeStore) ListBackorders(_ context.Context, filter store.BackorderFilter) ([]store.BackorderRow, error) {
	f.reports.backorderFilter = filter
	out := []store.BackorderRow{}
	past := filter.AfterLineID == ""
	for _, r := range f.reports.backorders {
		if !past {
			past = r.ShipmentLineID == filter.AfterLineID
			continue
		}
		if len(out) == filter.Limit {
			break
		}
		out = append(out, r)
	}
	return out, nil
}

func (f *fakeStore) ReturnableAging(_ context.Context, customerID string) ([]store.ReturnableAgingRow, error) {
	f.reports.agingCustomer = customerID
	return f.reports.aging, nil
}

func backorderRow(status domain.ShipmentStatus, planned, picked string) store.BackorderRow {
	return store.BackorderRow{ShipmentLineID: ids.New(), ShipmentID: ids.New(), ShipmentNumber: "SH-0000001", Status: status,
		WarehouseID: ids.New(), OrderID: ids.New(), CustomerID: ids.New(), OrderLineID: ids.New(), ProductID: ids.New(), Unit: "EA",
		Planned: decimal.RequireFromString(planned), Picked: decimal.RequireFromString(picked), Shipped: decimal.RequireFromString(picked),
		ShortPickedAt: time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)}
}

type backorderBody struct {
	Items []struct {
		ShipmentLineID string `json:"shipmentLineId"`
		Status         string `json:"status"`
		QtyPlanned     string `json:"qtyPlanned"`
		QtyPicked      string `json:"qtyPicked"`
		QtyRemainder   string `json:"qtyRemainder"`
		Open           bool   `json:"open"`
		ShortPickedAt  string `json:"shortPickedAt"`
	} `json:"items"`
	NextCursor string `json:"nextCursor"`
}

// AC-28.4 on the report: 100 planned, 90 picked leaves 10 open; a cancelled
// shipment's remainder is closed.
func TestListBackorders_RemainderAndOpen(t *testing.T) {
	short := backorderRow(domain.StatusPartiallyShipped, "100", "90")
	cancelled := backorderRow(domain.StatusCancelled, "5", "2")
	fs := &fakeStore{reports: reportCalls{backorders: []store.BackorderRow{short, cancelled}}}
	wh := ids.New()
	rec := do(t, newServer(t, fs), http.MethodGet, "/v1/fulfillment/backorders?warehouseId="+wh+"&from=2026-10-01&to=2026-10-31&status=all", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	got := fs.reports.backorderFilter
	if got.WarehouseID != wh || !got.IncludeClosed || got.From.Format(dateLayout) != "2026-10-01" || got.To.Format(dateLayout) != "2026-10-31" || got.Limit != 101 {
		t.Fatalf("store filter = %+v", got)
	}
	var out backorderBody
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 2 || out.NextCursor != "" {
		t.Fatalf("page = %+v", out)
	}
	if first := out.Items[0]; first.QtyRemainder != "10" || !first.Open || first.QtyPlanned != "100" || first.QtyPicked != "90" || first.ShortPickedAt != "2026-10-21T09:00:00Z" {
		t.Fatalf("short pick row = %+v, want remainder 10 open", first)
	}
	if second := out.Items[1]; second.QtyRemainder != "3" || second.Open || second.Status != "CANCELLED" {
		t.Fatalf("cancelled row = %+v, want remainder 3 closed", second)
	}
}

func TestListBackorders_DefaultsToOpenAndPagesByCursor(t *testing.T) {
	rows := []store.BackorderRow{backorderRow(domain.StatusPicked, "4", "1"), backorderRow(domain.StatusPicked, "4", "2"), backorderRow(domain.StatusPicked, "4", "3")}
	fs := &fakeStore{reports: reportCalls{backorders: rows}}
	h := newServer(t, fs)
	rec := do(t, h, http.MethodGet, "/v1/fulfillment/backorders?limit=2", "", nil)
	var page backorderBody
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if fs.reports.backorderFilter.IncludeClosed || fs.reports.backorderFilter.WarehouseID != "" {
		t.Fatalf("default filter = %+v, want open backorders of every warehouse", fs.reports.backorderFilter)
	}
	if len(page.Items) != 2 || page.NextCursor != rows[1].ShipmentLineID {
		t.Fatalf("first page = %+v, want 2 rows and the second line as cursor", page)
	}
	rec = do(t, h, http.MethodGet, "/v1/fulfillment/backorders?limit=2&cursor="+page.NextCursor, "", nil)
	page = backorderBody{}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if fs.reports.backorderFilter.AfterLineID != rows[1].ShipmentLineID || len(page.Items) != 1 || page.Items[0].ShipmentLineID != rows[2].ShipmentLineID || page.NextCursor != "" {
		t.Fatalf("second page = %+v (filter %+v)", page, fs.reports.backorderFilter)
	}
}

func TestListBackorders_RejectsBadParameters(t *testing.T) {
	cases := map[string]string{
		"warehouseId=WH1":               "warehouseId",
		"status=closed":                 "status",
		"from=2026-13-01":               "from",
		"from=2026-10-31&to=2026-10-1":  "to",
		"from=2026-10-31&to=2026-10-01": "to",
		"cursor=abc":                    "cursor",
		"limit=0":                       "limit",
	}
	for query, field := range cases {
		fs := &fakeStore{}
		rec := do(t, newServer(t, fs), http.MethodGet, "/v1/fulfillment/backorders?"+query, "", nil)
		expectField(t, rec, rec.Body.String(), field)
	}
}

// agingServer mounts the API with a fixed today.
func agingServer(t *testing.T, fs *fakeStore, today time.Time) http.Handler {
	t.Helper()
	a := New(fs, Options{})
	a.SetClock(func() time.Time { return today })
	r := httpx.NewRouter()
	a.Routes(r)
	return r
}

func shipped(day string, qty int64) domain.RTPShipped {
	d, _ := time.Parse(dateLayout, day)
	return domain.RTPShipped{Date: d, Qty: decimal.NewFromInt(qty)}
}

type agingBody struct {
	AsOf           string `json:"asOf"`
	ChargeDays     int    `json:"chargeDays"`
	Count          int    `json:"count"`
	ChargeDueCount int    `json:"chargeDueCount"`
	Items          []struct {
		CustomerID        string  `json:"customerId"`
		ContainerType     string  `json:"containerType"`
		Balance           string  `json:"balance"`
		OldestOutstanding *string `json:"oldestOutstandingDate"`
		DaysOutstanding   int     `json:"daysOutstanding"`
		ChargeDue         bool    `json:"chargeDue"`
	} `json:"items"`
}

func TestReturnableAging_AgesFirstInFirstOutAndFlagsCharges(t *testing.T) {
	c1, c2 := ids.New(), ids.New()
	fs := &fakeStore{reports: reportCalls{aging: []store.ReturnableAgingRow{
		// 10 shipped on 2026-07-13 and 4 on 2026-10-11; holding 6 means 2 of the July pallets are still out.
		{ReturnableBalance: store.ReturnableBalance{CustomerID: c1, ContainerType: "PALLET", Balance: decimal.NewFromInt(6)},
			Shipped: []domain.RTPShipped{shipped("2026-10-11", 4), shipped("2026-07-13", 10)}},
		{ReturnableBalance: store.ReturnableBalance{CustomerID: c2, ContainerType: "CRATE", Balance: decimal.NewFromInt(4)},
			Shipped: []domain.RTPShipped{shipped("2026-10-11", 4)}},
	}}}
	h := agingServer(t, fs, time.Date(2026, 10, 21, 15, 0, 0, 0, time.UTC))
	rec := do(t, h, http.MethodGet, "/v1/fulfillment/returnable-packaging/aging?customerId="+c1, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if fs.reports.agingCustomer != c1 {
		t.Fatalf("store customer = %q", fs.reports.agingCustomer)
	}
	var out agingBody
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.AsOf != "2026-10-21" || out.ChargeDays != 90 || out.Count != 2 || out.ChargeDueCount != 1 {
		t.Fatalf("header = %+v, want today, 90 days, 2 balances, 1 due", out)
	}
	first := out.Items[0]
	if first.CustomerID != c1 || first.DaysOutstanding != 100 || !first.ChargeDue || first.OldestOutstanding == nil || *first.OldestOutstanding != "2026-07-13" {
		t.Fatalf("first = %+v, want the July pallets 100 days out and due", first)
	}
	if second := out.Items[1]; second.DaysOutstanding != 10 || second.ChargeDue {
		t.Fatalf("second = %+v, want 10 days and not due", second)
	}

	rec = do(t, h, http.MethodGet, "/v1/fulfillment/returnable-packaging/aging?chargeDays=365", "", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.ChargeDays != 365 || out.ChargeDueCount != 0 || out.Items[0].ChargeDue {
		t.Fatalf("with 365 days nothing is due: %+v", out)
	}

	rec = do(t, h, http.MethodGet, "/v1/fulfillment/returnable-packaging/aging?chargeDue=true&limit=1", "", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Count != 1 || len(out.Items) != 1 || out.Items[0].ContainerType != "PALLET" {
		t.Fatalf("chargeDue=true = %+v, want only the PALLET balance", out)
	}
}

func TestReturnableAging_RejectsBadParameters(t *testing.T) {
	cases := map[string]string{
		"customerId=C1":   "customerId",
		"chargeDays=29":   "chargeDays",
		"chargeDays=366":  "chargeDays",
		"chargeDays=abc":  "chargeDays",
		"chargeDue=maybe": "chargeDue",
		"limit=501":       "limit",
	}
	for query, field := range cases {
		rec := do(t, newServer(t, &fakeStore{}), http.MethodGet, "/v1/fulfillment/returnable-packaging/aging?"+query, "", nil)
		expectField(t, rec, rec.Body.String(), field)
	}
}

func TestReports_RequireWarehouseView(t *testing.T) {
	for _, path := range []string{"/v1/fulfillment/backorders", "/v1/fulfillment/returnable-packaging/aging"} {
		rec := do(t, newServer(t, &fakeStore{}), http.MethodGet, path, "", map[string]string{"X-Passage-Duties": "WarehousePick"})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s: status = %d body = %s, want 403", path, rec.Code, rec.Body.String())
		}
	}
}
