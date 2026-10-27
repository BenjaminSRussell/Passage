package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

func (f *fakeStore) CreateTransfer(_ context.Context, key string, o domain.TransferOrder) (domain.TransferOrder, bool, error) {
	f.transferIn, f.transferKey = o, key
	if f.err != nil {
		return domain.TransferOrder{}, false, f.err
	}
	o.ID = ids.New()
	o.Number = "TO-TEST"
	o.Status = domain.TransferCreated
	o.Version = 1
	return o, key == "replay", nil
}

func (f *fakeStore) GetTransfer(_ context.Context, id string) (domain.TransferOrder, error) {
	if f.err != nil {
		return domain.TransferOrder{}, f.err
	}
	return domain.TransferOrder{ID: id, Status: domain.TransferCreated, Version: 3, ShipDate: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}, nil
}

func (f *fakeStore) ListTransfers(context.Context, string) ([]domain.TransferOrder, error) {
	return []domain.TransferOrder{{ID: ids.New(), Status: domain.TransferCreated, Version: 1}}, nil
}

func (f *fakeStore) ShipTransfer(_ context.Context, id string, version int, shipDate time.Time, neg domain.NegativePolicy) (domain.TransferOrder, error) {
	f.transferVersion, f.shipDate, f.transferNegative = version, shipDate, neg
	if f.err != nil {
		return domain.TransferOrder{}, f.err
	}
	return domain.TransferOrder{ID: id, Status: domain.TransferShipped, Version: version + 1, ShipDate: shipDate}, nil
}

func (f *fakeStore) ReceiveTransfer(_ context.Context, id string, version int, receipts []store.TransferReceipt, receiptDate time.Time, _ domain.NegativePolicy) (domain.TransferOrder, error) {
	f.transferVersion, f.receipts, f.receiptDate = version, receipts, receiptDate
	if f.err != nil {
		return domain.TransferOrder{}, f.err
	}
	return domain.TransferOrder{ID: id, Status: domain.TransferReceived, Version: version + 1}, nil
}

func (f *fakeStore) CancelTransfer(_ context.Context, id string, version int) (domain.TransferOrder, error) {
	f.transferVersion = version
	if f.err != nil {
		return domain.TransferOrder{}, f.err
	}
	return domain.TransferOrder{ID: id, Status: domain.TransferCancelled, Version: version + 1}, nil
}

func (f *fakeStore) ListInTransit(context.Context) ([]store.InTransitRow, error) {
	return []store.InTransitRow{{OrderID: ids.New(), Number: "TO-1", ProductID: ids.New(), FromWarehouse: "W1", Unit: "EA",
		ToWarehouse: "W2", TransitWarehouse: "T1", ShipDate: time.Now().UTC().AddDate(0, 0, -3), InTransit: decimal.NewFromInt(5), Closed: true}}, nil
}

func (f *fakeStore) RunABC(_ context.Context, in store.AbcRunInput) (domain.AbcRun, bool, error) {
	f.abcIn = in
	return domain.AbcRun{ID: ids.New(), LegalEntityID: in.LegalEntityID, Criterion: in.Criterion, Thresholds: in.Thresholds, ZeroClass: in.ZeroClass,
		Items: []domain.AbcRank{{AbcItem: domain.AbcItem{ProductID: ids.New(), Site: "S1", Value: decimal.NewFromInt(80)}, Class: "A",
			CumPct: decimal.RequireFromString("66.666666")}}}, false, nil
}

func (f *fakeStore) GetABCRun(_ context.Context, id string) (domain.AbcRun, error) {
	if f.err != nil {
		return domain.AbcRun{}, f.err
	}
	return domain.AbcRun{ID: id, Criterion: "VALUE", Thresholds: domain.DefaultAbcThresholds(), ZeroClass: "C"}, nil
}

func (f *fakeStore) ListABCRuns(context.Context) ([]store.AbcRunSummary, error) {
	return []store.AbcRunSummary{{Run: domain.AbcRun{ID: ids.New(), Criterion: "VALUE", Thresholds: domain.DefaultAbcThresholds(), ZeroClass: "C"},
		Counts: map[string]int{"A": 2, "B": 1, "C": 4, "N": 0}}}, nil
}

const (
	transferOrdersPathForTest = "/v1/inventory/transfer-orders"
	testEntity                = "00000000-0000-7000-8000-000000000001"
	testProduct               = "00000000-0000-7000-8000-000000000002"
)

const transferBody = `{"legalEntityId":"` + testEntity + `","fromWarehouseId":"W1","toWarehouseId":"W2","toSiteId":"S2",` +
	`"transitWarehouseId":"T1","shipDate":"2026-09-01","lines":[{"productId":"` + testProduct +
	`","dimensions":{"site":"S1","batch":"B1"},"unit":"EA","qty":"100","underdeliveryPct":"10"}]}`

func TestCreateTransferValidatesFields(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	body := `{"legalEntityId":"nope","fromWarehouseId":"","toWarehouseId":"W2","shipDate":"2026-13-01",` +
		`"lines":[{"productId":"x","unit":"ea","qty":"-1","overdeliveryPct":"lots"}]}`
	rec := do(t, h, http.MethodPost, transferOrdersPathForTest, body, []string{"inventory.transfer.write"}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body.String())
	}
	p := decodeProblem(t, rec)
	for _, f := range []string{"legalEntityId", "fromWarehouseId", "shipDate", "lines[0].productId", "lines[0].unit", "lines[0].qty",
		"lines[0].overdeliveryPct", "Idempotency-Key"} {
		if _, ok := p.Errors[f]; !ok {
			t.Errorf("missing field error for %s: %v", f, p.Errors)
		}
	}
	if fs.transferKey != "" {
		t.Fatal("an invalid request reached the store")
	}
}

func TestCreateTransferPassesOrderAndReplays(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	rec := do(t, h, http.MethodPost, transferOrdersPathForTest, transferBody, []string{"inventory.transfer.write"},
		map[string]string{"Idempotency-Key": "t-1"})
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("status %d ETag %q body %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	in := fs.transferIn
	if fs.transferKey != "t-1" || !in.UseTransit || in.TransitWarehouse != "T1" || in.ToSite != "S2" || in.FromWarehouse != "W1" ||
		len(in.Lines) != 1 || !in.Lines[0].Qty.Equal(decimal.NewFromInt(100)) || !in.Lines[0].UnderPct.Equal(decimal.NewFromInt(10)) ||
		!in.Lines[0].OverPct.IsZero() || in.Lines[0].Dims["batch"] != "B1" {
		t.Fatalf("store got %+v (key %q)", in, fs.transferKey)
	}
	rec = do(t, h, http.MethodPost, transferOrdersPathForTest, transferBody, []string{"inventory.transfer.write"},
		map[string]string{"Idempotency-Key": "replay"})
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: status %d headers %v", rec.Code, rec.Header())
	}
}

func TestCreateOneStepTransferDropsTransitWarehouse(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	body := `{"legalEntityId":"` + testEntity + `","fromWarehouseId":"W1","toWarehouseId":"W2","transitWarehouseId":"T1","useTransit":false,` +
		`"shipDate":"2026-09-01","lines":[{"productId":"` + testProduct + `","dimensions":{"site":"S1"},"unit":"EA","qty":"1"}]}`
	rec := do(t, h, http.MethodPost, transferOrdersPathForTest, body, []string{"inventory.transfer.write"}, map[string]string{"Idempotency-Key": "k"})
	if rec.Code != http.StatusCreated || fs.transferIn.UseTransit || fs.transferIn.TransitWarehouse != "" {
		t.Fatalf("status %d order %+v", rec.Code, fs.transferIn)
	}
}

func TestCreateTransferNeedsWriteDuty(t *testing.T) {
	h := newServer(t, &fakeStore{})
	rec := do(t, h, http.MethodPost, transferOrdersPathForTest, transferBody, []string{"inventory.read", "inventory.transfer.post"},
		map[string]string{"Idempotency-Key": "t-1"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", rec.Code)
	}
}

func TestShipNeedsPostDutyIfMatchAndDate(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	path := transferOrdersPathForTest + "/" + ids.New() + ":ship"
	if rec := do(t, h, http.MethodPost, path, "", []string{"inventory.transfer.write"}, map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusForbidden {
		t.Fatalf("write-only ship: status %d, want 403", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, path, "", []string{"inventory.transfer.post"}, nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: status %d, want 428", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, path, `{"shipDate":"tomorrow"}`, []string{"inventory.transfer.post"}, map[string]string{"If-Match": `"4"`}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad ship date: status %d, want 400", rec.Code)
	}
	rec := do(t, h, http.MethodPost, path, `{"shipDate":"2026-09-03"}`, []string{"inventory.transfer.post"}, map[string]string{"If-Match": `"4"`})
	if rec.Code != http.StatusOK || fs.transferVersion != 4 || fs.shipDate.Format("2006-01-02") != "2026-09-03" || rec.Header().Get("ETag") != `"5"` {
		t.Fatalf("status %d version %d date %v ETag %q body %s", rec.Code, fs.transferVersion, fs.shipDate, rec.Header().Get("ETag"), rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, path, "", []string{"inventory.transfer.post"}, map[string]string{"If-Match": `"4"`})
	if rec.Code != http.StatusOK || fs.shipDate.Format("2006-01-02") != time.Now().UTC().Format("2006-01-02") {
		t.Fatalf("empty body: status %d date %v, want today", rec.Code, fs.shipDate)
	}
}

func TestShipPassesStoreProblemThrough(t *testing.T) {
	fs := &fakeStore{err: httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeNegativePhysical, "short")}
	h := newServer(t, fs)
	rec := do(t, h, http.MethodPost, transferOrdersPathForTest+"/"+ids.New()+":ship", "", []string{"inventory.transfer.post"}, map[string]string{"If-Match": `"1"`})
	if rec.Code != http.StatusUnprocessableEntity || decodeProblem(t, rec).Code != domain.CodeNegativePhysical {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestReceiveValidatesAndPassesReceipts(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	path := transferOrdersPathForTest + "/" + ids.New() + ":receive"
	bad := `{"receiptDate":"2026-02-30","lines":[{"lineId":"x","qty":"-1"}]}`
	rec := do(t, h, http.MethodPost, path, bad, []string{"inventory.transfer.post"}, map[string]string{"If-Match": `"2"`})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body.String())
	}
	p := decodeProblem(t, rec)
	for _, f := range []string{"receiptDate", "lines[0].lineId", "lines[0].qty"} {
		if _, ok := p.Errors[f]; !ok {
			t.Errorf("missing field error for %s: %v", f, p.Errors)
		}
	}
	if rec := do(t, h, http.MethodPost, path, `{"lines":[]}`, []string{"inventory.transfer.post"}, map[string]string{"If-Match": `"2"`}); rec.Code != http.StatusBadRequest {
		t.Fatalf("no lines: status %d, want 400", rec.Code)
	}
	line := ids.New()
	good := `{"receiptDate":"2026-09-05","lines":[{"lineId":"` + line + `","qty":"95","closeShort":true}]}`
	rec = do(t, h, http.MethodPost, path, good, []string{"inventory.transfer.post"}, map[string]string{"If-Match": `"2"`})
	if rec.Code != http.StatusOK || fs.transferVersion != 2 || len(fs.receipts) != 1 || fs.receipts[0].LineID != line ||
		!fs.receipts[0].Qty.Equal(decimal.NewFromInt(95)) || !fs.receipts[0].CloseShort || fs.receiptDate.Format("2006-01-02") != "2026-09-05" {
		t.Fatalf("status %d receipts %+v date %v", rec.Code, fs.receipts, fs.receiptDate)
	}
}

func TestCancelNeedsWriteDutyAndIfMatch(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	path := transferOrdersPathForTest + "/" + ids.New() + ":cancel"
	if rec := do(t, h, http.MethodPost, path, "", []string{"inventory.transfer.post"}, map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusForbidden {
		t.Fatalf("post-only cancel: status %d, want 403", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, path, "", []string{"inventory.transfer.write"}, nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: status %d, want 428", rec.Code)
	}
	fs.err = httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeTransferCancel, "in transit")
	rec := do(t, h, http.MethodPost, path, "", []string{"inventory.transfer.write"}, map[string]string{"If-Match": `"3"`})
	if rec.Code != http.StatusUnprocessableEntity || decodeProblem(t, rec).Code != domain.CodeTransferCancel || fs.transferVersion != 3 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestGetAndListTransfers(t *testing.T) {
	h := newServer(t, &fakeStore{})
	if rec := do(t, h, http.MethodGet, transferOrdersPathForTest+"/not-a-uuid", "", []string{"inventory.read"}, nil); rec.Code != http.StatusNotFound ||
		decodeProblem(t, rec).Code != domain.CodeTransferNotFound {
		t.Fatalf("bad id: status %d", rec.Code)
	}
	rec := do(t, h, http.MethodGet, transferOrdersPathForTest+"/"+ids.New(), "", []string{"inventory.read"}, nil)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"3"` {
		t.Fatalf("get: status %d ETag %q", rec.Code, rec.Header().Get("ETag"))
	}
	if rec := do(t, h, http.MethodGet, transferOrdersPathForTest+"?status=LOST", "", []string{"inventory.read"}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad status filter: %d", rec.Code)
	}
	rec = do(t, h, http.MethodGet, transferOrdersPathForTest+"?status=CREATED", "", []string{"inventory.read"}, nil)
	var out struct {
		Items []struct {
			Status string `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK || len(out.Items) != 1 {
		t.Fatalf("list: status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestInTransitListsShippedNotReceived(t *testing.T) {
	h := newServer(t, &fakeStore{})
	rec := do(t, h, http.MethodGet, "/v1/inventory/transfers/in-transit", "", []string{"inventory.read"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Items []struct {
			DaysInTransit int    `json:"daysInTransit"`
			InTransitQty  string `json:"inTransitQty"`
			Unit          string `json:"unit"`
			ClosedShort   bool   `json:"closedShort"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 1 || out.Items[0].InTransitQty != "5" || out.Items[0].DaysInTransit != 3 || out.Items[0].Unit != "EA" || !out.Items[0].ClosedShort {
		t.Fatalf("items %+v", out.Items)
	}
}

func TestRunABCValidation(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	body := `{"legalEntityId":"` + ids.New() + `","criterion":"REVENUE","zeroClass":"X","thresholds":{"a":"80","b":"x","c":"5"}}`
	rec := do(t, h, http.MethodPost, "/v1/inventory/abc-runs", body, []string{"inventory.abc.write"}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body.String())
	}
	p := decodeProblem(t, rec)
	for _, f := range []string{"criterion", "zeroClass", "thresholds.b", "Idempotency-Key"} {
		if _, ok := p.Errors[f]; !ok {
			t.Errorf("missing field error for %s: %v", f, p.Errors)
		}
	}
	if fs.abcIn.IdempotencyKey != "" {
		t.Fatal("an invalid request reached the store")
	}
}

func TestRunABCDefaultsAndResponse(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	entity := ids.New()
	rec := do(t, h, http.MethodPost, "/v1/inventory/abc-runs", `{"legalEntityId":"`+entity+`"}`, []string{"inventory.abc.write"},
		map[string]string{"Idempotency-Key": "abc-1"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	in := fs.abcIn
	if in.LegalEntityID != entity || in.Criterion != "VALUE" || in.ZeroClass != "C" || !in.Thresholds.A.Equal(decimal.NewFromInt(80)) ||
		!in.Thresholds.B.Equal(decimal.NewFromInt(15)) || !in.Thresholds.C.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("store got %+v", in)
	}
	var out struct {
		ClassCounts map[string]int `json:"classCounts"`
		Items       []struct {
			CumPct string `json:"cumPct"`
			Class  string `json:"class"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.ClassCounts["A"] != 1 || len(out.Items) != 1 || out.Items[0].CumPct != "66.67" {
		t.Fatalf("response %+v", out)
	}
}

func TestABCRunsNeedDutyAndList(t *testing.T) {
	h := newServer(t, &fakeStore{})
	body := `{"legalEntityId":"` + ids.New() + `"}`
	if rec := do(t, h, http.MethodPost, "/v1/inventory/abc-runs", body, []string{"inventory.read"}, map[string]string{"Idempotency-Key": "abc-2"}); rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", rec.Code)
	}
	rec := do(t, h, http.MethodGet, "/v1/inventory/abc-runs", "", []string{"inventory.read"}, nil)
	var out struct {
		Items []struct {
			ClassCounts map[string]int `json:"classCounts"`
			Items       []any          `json:"items"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK || len(out.Items) != 1 ||
		out.Items[0].ClassCounts["C"] != 4 || out.Items[0].Items != nil {
		t.Fatalf("list: status %d body %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodGet, "/v1/inventory/abc-runs/nope", "", []string{"inventory.read"}, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: status %d", rec.Code)
	}
}
