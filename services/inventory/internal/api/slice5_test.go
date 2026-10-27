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

// Slice 5 fake-store methods; their fields are declared on fakeStore in api_test.go.

func (f *fakeStore) ValidateJournal(_ context.Context, id string, _ store.PostParams) (domain.Journal, error) {
	if f.err != nil {
		return domain.Journal{}, f.err
	}
	f.validated = id
	return domain.Journal{ID: id, Status: domain.JournalSubmitted, Version: 2, Lines: []domain.JournalLine{{}, {}}}, nil
}

func (f *fakeStore) TransitionJournal(_ context.Context, id string, version int, to domain.JournalStatus) (domain.Journal, error) {
	if f.err != nil {
		return domain.Journal{}, f.err
	}
	f.transitionTo, f.transitionVersion = to, version
	return domain.Journal{ID: id, Type: domain.JournalMovement, Status: to, Version: version + 1, Currency: "USD"}, nil
}

func consignmentOrder(o domain.ConsignmentOrder) domain.ConsignmentOrder {
	o.ID, o.Number, o.Status, o.Version = ids.New(), "CRO-0001", domain.ConsignmentCreated, 1
	for i := range o.Lines {
		o.Lines[i].ID = ids.New()
	}
	return o
}

func (f *fakeStore) CreateConsignment(_ context.Context, key string, o domain.ConsignmentOrder) (domain.ConsignmentOrder, bool, error) {
	f.consIn, f.consKey = o, key
	return consignmentOrder(o), key == "replay", nil
}

func (f *fakeStore) GetConsignment(_ context.Context, id string) (domain.ConsignmentOrder, error) {
	o := consignmentOrder(domain.ConsignmentOrder{VendorID: "V1", Site: "S1", Warehouse: "W1", Currency: "USD",
		Lines: []domain.ConsignmentLine{{LineNo: 1, ProductID: ids.New(), Unit: "EA", Qty: decimal.NewFromInt(10), ReceivedQty: decimal.NewFromInt(4),
			Price: decimal.RequireFromString("4")}}})
	o.ID = id
	return o, nil
}

func (f *fakeStore) ListConsignments(_ context.Context, flt store.ConsignmentFilter) ([]domain.ConsignmentOrder, error) {
	f.consFilter = flt
	return []domain.ConsignmentOrder{}, nil
}

func (f *fakeStore) ReceiveConsignment(_ context.Context, id string, version int, receipts []domain.ConsignmentReceipt, date time.Time) (domain.ConsignmentOrder, error) {
	if f.err != nil {
		return domain.ConsignmentOrder{}, f.err
	}
	f.consVersion, f.consReceipts, f.consDate = version, receipts, date
	o := consignmentOrder(domain.ConsignmentOrder{VendorID: "V1"})
	o.ID, o.Version, o.Status = id, version+1, domain.ConsignmentPartial
	return o, nil
}

func (f *fakeStore) ListConsignmentStock(_ context.Context, vendor string) ([]store.ConsignmentStockRow, error) {
	f.stockVendor = vendor
	price, value := decimal.RequireFromString("4"), decimal.RequireFromString("2000")
	oldest := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -10)
	return []store.ConsignmentStockRow{
		{LegalEntityID: ids.New(), VendorID: "V1", ProductID: ids.New(), DimensionSet: domain.Dimensions{"site": "S1", "owner": "V1"},
			Qty: decimal.NewFromInt(500), Price: &price, Currency: "USD", Value: &value, OldestReceipt: &oldest},
		{LegalEntityID: ids.New(), VendorID: "V2", ProductID: ids.New(), DimensionSet: domain.Dimensions{"site": "S1", "owner": "V2"},
			Qty: decimal.NewFromInt(3)},
	}, nil
}

func TestSlice5DutiesAreEnforcedPerRoute(t *testing.T) {
	h := newServer(t, &fakeStore{})
	id := ids.New()
	cases := []struct{ method, path, duty string }{
		{http.MethodPost, "/v1/inventory/journals/" + id + ":validate", "inventory.read"},
		{http.MethodPost, "/v1/inventory/journals/" + id + ":submit", "inventory.journal.post"},
		{http.MethodPost, "/v1/inventory/journals/" + id + ":approve", "inventory.journal.write"},
		{http.MethodPost, "/v1/inventory/journals/" + id + ":reject", "inventory.journal.write"},
		{http.MethodPost, "/v1/inventory/journals/" + id + ":reopen", "inventory.journal.post"},
		{http.MethodPost, "/v1/inventory/journals/" + id + ":cancel", "inventory.read"},
		{http.MethodPost, "/v1/inventory/consignment-orders", "inventory.journal.write"},
		{http.MethodGet, "/v1/inventory/consignment-orders", "inventory.consignment.write"},
		{http.MethodPost, "/v1/inventory/consignment-orders/" + id + ":receive", "inventory.read"},
		{http.MethodGet, "/v1/inventory/consignment/stock", "inventory.consignment.write"},
	}
	for _, c := range cases {
		rec := do(t, h, c.method, c.path, "{}", []string{c.duty}, map[string]string{"If-Match": "1"})
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s with %s: status %d, want 403", c.method, c.path, c.duty, rec.Code)
		}
	}
}

func TestWorkflowStepsSendTheirTargetAndVersion(t *testing.T) {
	steps := map[string]struct {
		duty string
		to   domain.JournalStatus
	}{
		"submit":  {"inventory.journal.write", domain.JournalSubmitted},
		"approve": {"inventory.journal.post", domain.JournalApproved},
		"reject":  {"inventory.journal.post", domain.JournalRejected},
		"reopen":  {"inventory.journal.write", domain.JournalDraft},
		"cancel":  {"inventory.journal.write", domain.JournalCancelled},
	}
	for step, c := range steps {
		t.Run(step, func(t *testing.T) {
			fs := &fakeStore{}
			h := newServer(t, fs)
			path := "/v1/inventory/journals/" + ids.New() + ":" + step
			if rec := do(t, h, http.MethodPost, path, "", []string{c.duty}, nil); rec.Code != http.StatusPreconditionRequired {
				t.Fatalf("no If-Match: status %d, want 428", rec.Code)
			}
			rec := do(t, h, http.MethodPost, path, "", []string{c.duty}, map[string]string{"If-Match": `"3"`})
			if rec.Code != http.StatusOK || fs.transitionTo != c.to || fs.transitionVersion != 3 || rec.Header().Get("ETag") != `"4"` {
				t.Fatalf("status %d to %s version %d ETag %q; want 200 %s 3 \"4\"", rec.Code, fs.transitionTo, fs.transitionVersion,
					rec.Header().Get("ETag"), c.to)
			}
		})
	}
}

func TestWorkflowStepPassesStateConflictThrough(t *testing.T) {
	fs := &fakeStore{err: httpx.NewProblem(http.StatusConflict, store.CodeJournalState, "a POSTED journal cannot move to SUBMITTED")}
	rec := do(t, newServer(t, fs), http.MethodPost, "/v1/inventory/journals/"+ids.New()+":submit", "", []string{"inventory.journal.write"},
		map[string]string{"If-Match": "1"})
	if rec.Code != http.StatusConflict || decodeProblem(t, rec).Code != store.CodeJournalState {
		t.Fatalf("status %d body %s, want 409 %s", rec.Code, rec.Body.String(), store.CodeJournalState)
	}
}

func TestValidateAnswersValidOrThePostProblem(t *testing.T) {
	fs := &fakeStore{}
	id := ids.New()
	rec := do(t, newServer(t, fs), http.MethodPost, "/v1/inventory/journals/"+id+":validate", "", []string{"inventory.journal.write"}, nil)
	var body struct {
		Valid     bool   `json:"valid"`
		JournalID string `json:"journalId"`
		LineCount int    `json:"lineCount"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil || !body.Valid || body.JournalID != id || body.LineCount != 2 || fs.validated != id {
		t.Fatalf("status %d body %s, want 200 valid for %s", rec.Code, rec.Body.String(), id)
	}
	fs.err = httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeOwnershipExceeds, "exceeds vendor-owned quantity")
	rec = do(t, newServer(t, fs), http.MethodPost, "/v1/inventory/journals/"+id+":validate", "", []string{"inventory.journal.write"}, nil)
	if rec.Code != http.StatusUnprocessableEntity || decodeProblem(t, rec).Code != domain.CodeOwnershipExceeds {
		t.Fatalf("status %d body %s, want 422 %s", rec.Code, rec.Body.String(), domain.CodeOwnershipExceeds)
	}
}

func TestOwnershipChangeJournalNeedsTheConsignmentDuty(t *testing.T) {
	body := `{"journalType":"OWNERSHIP_CHANGE","legalEntityId":"` + ids.New() + `","postingDate":"2026-10-01","currency":"USD"}`
	fs := &fakeStore{}
	h := newServer(t, fs)
	rec := do(t, h, http.MethodPost, "/v1/inventory/journals", body, []string{"inventory.journal.write"}, map[string]string{"Idempotency-Key": "k1"})
	if rec.Code != http.StatusForbidden || len(fs.keys) != 0 {
		t.Fatalf("without inventory.consignment.write: status %d, journals %d; want 403 and none", rec.Code, len(fs.keys))
	}
	rec = do(t, h, http.MethodPost, "/v1/inventory/journals", body, []string{"inventory.journal.write", "inventory.consignment.write"},
		map[string]string{"Idempotency-Key": "k1"})
	if rec.Code != http.StatusCreated || fs.keys["k1"].Type != domain.JournalOwnershipChange {
		t.Fatalf("with the duty: status %d body %s, want 201 OWNERSHIP_CHANGE", rec.Code, rec.Body.String())
	}
	withOffset := `{"journalType":"OWNERSHIP_CHANGE","legalEntityId":"` + ids.New() + `","postingDate":"2026-10-01","currency":"USD","offsetAccountId":"2190"}`
	rec = do(t, h, http.MethodPost, "/v1/inventory/journals", withOffset, []string{"inventory.journal.write", "inventory.consignment.write"},
		map[string]string{"Idempotency-Key": "k2"})
	if rec.Code != http.StatusBadRequest || decodeProblem(t, rec).Errors["offsetAccountId"] == "" {
		t.Fatalf("offset account on an ownership change: status %d body %s, want 400 offsetAccountId", rec.Code, rec.Body.String())
	}
}

func TestCreateConsignmentValidatesAndReplays(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	duty := []string{"inventory.consignment.write"}
	le, product := ids.New(), ids.New()
	good := `{"legalEntityId":"` + le + `","vendorId":" V1 ","siteId":"S1","warehouseId":"W1","requestedDate":"2026-10-01","currency":"USD",
		"lines":[{"productId":"` + product + `","unit":"EA","qty":"500","consignmentPrice":"4.00","dimensions":{"batch":"B1"}}]}`
	rec := do(t, h, http.MethodPost, "/v1/inventory/consignment-orders", good, duty, map[string]string{"Idempotency-Key": "c1"})
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("status %d ETag %q body %s, want 201 \"1\"", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	in := fs.consIn
	if fs.consKey != "c1" || in.VendorID != "V1" || in.Site != "S1" || in.Warehouse != "W1" || len(in.Lines) != 1 ||
		!in.Lines[0].Price.Equal(decimal.RequireFromString("4")) || in.Lines[0].Dims["batch"] != "B1" || !in.Lines[0].Qty.Equal(decimal.NewFromInt(500)) {
		t.Fatalf("store got %+v (key %s)", in, fs.consKey)
	}
	rec = do(t, h, http.MethodPost, "/v1/inventory/consignment-orders", good, duty, map[string]string{"Idempotency-Key": "replay"})
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: status %d, want 200 with Idempotent-Replayed", rec.Code)
	}
	bad := `{"legalEntityId":"x","vendorId":"","siteId":"S1","warehouseId":"","requestedDate":"soon","currency":"usd",
		"lines":[{"productId":"p","unit":"","qty":"-1","consignmentPrice":"-4"}]}`
	rec = do(t, h, http.MethodPost, "/v1/inventory/consignment-orders", bad, duty, nil)
	p := decodeProblem(t, rec)
	for _, f := range []string{"legalEntityId", "vendorId", "warehouseId", "requestedDate", "currency", "lines[0].productId", "lines[0].unit",
		"lines[0].qty", "lines[0].consignmentPrice", "Idempotency-Key"} {
		if p.Errors[f] == "" {
			t.Errorf("no error for %s: %v", f, p.Errors)
		}
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid order: status %d, want 400", rec.Code)
	}
}

func TestReceiveConsignmentNeedsIfMatchAndValidLines(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	duty := []string{"inventory.consignment.write"}
	path := "/v1/inventory/consignment-orders/" + ids.New() + ":receive"
	line := ids.New()
	body := `{"receiptDate":"2026-10-02","lines":[{"lineId":"` + line + `","qty":"120"}]}`
	if rec := do(t, h, http.MethodPost, path, body, duty, nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: status %d, want 428", rec.Code)
	}
	rec := do(t, h, http.MethodPost, path, body, duty, map[string]string{"If-Match": `"2"`})
	if rec.Code != http.StatusOK || fs.consVersion != 2 || len(fs.consReceipts) != 1 || fs.consReceipts[0].LineID != line ||
		!fs.consReceipts[0].Qty.Equal(decimal.NewFromInt(120)) || fs.consDate.Format("2006-01-02") != "2026-10-02" {
		t.Fatalf("status %d; store got version %d receipts %+v date %s", rec.Code, fs.consVersion, fs.consReceipts, fs.consDate)
	}
	rec = do(t, h, http.MethodPost, path, `{"lines":[{"lineId":"x","qty":"0"}]}`, duty, map[string]string{"If-Match": "2"})
	if p := decodeProblem(t, rec); rec.Code != http.StatusBadRequest || p.Errors["lines[0].lineId"] == "" || p.Errors["lines[0].qty"] == "" {
		t.Fatalf("invalid receipt: status %d body %s", rec.Code, rec.Body.String())
	}
	fs.err = httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeConsignmentOver, "exceeds the open quantity")
	rec = do(t, h, http.MethodPost, path, body, duty, map[string]string{"If-Match": "2"})
	if rec.Code != http.StatusUnprocessableEntity || decodeProblem(t, rec).Code != domain.CodeConsignmentOver {
		t.Fatalf("over-receipt: status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestConsignmentStockReportsAgeAndStatisticalValue(t *testing.T) {
	fs := &fakeStore{}
	rec := do(t, newServer(t, fs), http.MethodGet, "/v1/inventory/consignment/stock?vendor=V1", "", []string{"inventory.read"}, nil)
	var body struct {
		Items []struct {
			VendorID         string  `json:"vendorId"`
			Qty              string  `json:"qty"`
			ConsignmentPrice *string `json:"consignmentPrice"`
			StatisticalValue *string `json:"statisticalValue"`
			AgeDays          *int    `json:"ageDays"`
		} `json:"items"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil || len(body.Items) != 2 || fs.stockVendor != "V1" {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	first, second := body.Items[0], body.Items[1]
	if first.Qty != "500" || first.ConsignmentPrice == nil || *first.StatisticalValue != "2000" || first.AgeDays == nil || *first.AgeDays != 10 {
		t.Fatalf("first row %+v, want 500 at 4 = 2000, 10 days old", first)
	}
	if second.ConsignmentPrice != nil || second.StatisticalValue != nil || second.AgeDays != nil {
		t.Fatalf("a row without price or receipt must leave them null: %+v", second)
	}
}

func TestReservationCarriesTheDemandSegment(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	duty := []string{"inventory.reservation.write"}
	base := `"legalEntityId":"` + ids.New() + `","productId":"` + ids.New() + `","qty":"5","demandType":"SALES_ORDER","demandId":"SO-1","policy":"FIFO"`
	rec := do(t, h, http.MethodPost, "/v1/inventory/reservations", `{`+base+`,"segmentType":"sales_order","segmentRef":"SO-1","segmentFallback":false}`, duty,
		map[string]string{"Idempotency-Key": "r1"})
	if rec.Code != http.StatusCreated || fs.reserveIn.Segment != "SALES_ORDER:SO-1" || fs.reserveIn.SegmentFallback {
		t.Fatalf("status %d; store got segment %q fallback %v; want SALES_ORDER:SO-1 false", rec.Code, fs.reserveIn.Segment, fs.reserveIn.SegmentFallback)
	}
	rec = do(t, h, http.MethodPost, "/v1/inventory/reservations", `{`+base+`}`, duty, map[string]string{"Idempotency-Key": "r2"})
	if rec.Code != http.StatusCreated || fs.reserveIn.Segment != "" || !fs.reserveIn.SegmentFallback {
		t.Fatalf("no segment: store got %q fallback %v; want unsegmented with fallback default true", fs.reserveIn.Segment, fs.reserveIn.SegmentFallback)
	}
	for _, seg := range []string{`"segmentType":"PROJECT"`, `"segmentType":"NONE","segmentRef":"P1"`, `"segmentType":"WBS","segmentRef":"P1"`} {
		rec = do(t, h, http.MethodPost, "/v1/inventory/reservations", `{`+base+`,`+seg+`}`, duty, map[string]string{"Idempotency-Key": "r3"})
		if rec.Code != http.StatusBadRequest || decodeProblem(t, rec).Errors["segmentType"] == "" {
			t.Errorf("%s: status %d body %s, want 400 segmentType", seg, rec.Code, rec.Body.String())
		}
	}
}

func TestOnHandPassesOwnerAndSegmentFilters(t *testing.T) {
	fs := &fakeStore{}
	rec := do(t, newServer(t, fs), http.MethodGet, "/v1/inventory/onhand?owner=V1&segment=PROJECT:P1&groupBy=owner,segment", "", []string{"inventory.read"}, nil)
	if rec.Code != http.StatusOK || fs.onHand.Owner != "V1" || fs.onHand.Segment != "PROJECT:P1" || len(fs.onHand.GroupBy) != 2 {
		t.Fatalf("status %d; store got %+v", rec.Code, fs.onHand)
	}
}
