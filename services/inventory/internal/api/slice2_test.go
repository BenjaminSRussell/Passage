package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

func key(k string) map[string]string { return map[string]string{"Idempotency-Key": k} }

func TestSlice2DutiesAreEnforcedPerRoute(t *testing.T) {
	h := newServer(t, &fakeStore{})
	id := ids.New()
	cases := []struct{ method, path, duty string }{
		{http.MethodGet, "/v1/inventory/transactions", "inventory.journal.write"},
		{http.MethodPost, "/v1/inventory/reservations", "inventory.read"},
		{http.MethodGet, "/v1/inventory/reservations", "inventory.reservation.write"},
		{http.MethodDelete, "/v1/inventory/reservations/" + id, "inventory.read"},
		{http.MethodPut, "/v1/inventory/batch-expiries/" + id + "/B1", "inventory.reservation.write"},
		{http.MethodPost, "/v1/inventory/blockings", "inventory.read"},
		{http.MethodGet, "/v1/inventory/blockings", "inventory.blocking.write"},
		{http.MethodPost, "/v1/inventory/blockings/" + id + ":release", "inventory.read"},
		{http.MethodPost, "/v1/inventory/blockings/" + id + ":block", "inventory.read"},
		{http.MethodPost, "/v1/inventory/count-plans", "inventory.count.post"},
		{http.MethodGet, "/v1/inventory/count-plans", "inventory.count.write"},
		{http.MethodPost, "/v1/inventory/count-plans/" + id + ":generate", "inventory.read"},
		{http.MethodGet, "/v1/inventory/count-sheets", "inventory.count.write"},
		{http.MethodGet, "/v1/inventory/count-sheets/" + id, "inventory.count.write"},
		{http.MethodPut, "/v1/inventory/count-sheets/" + id + "/lines/" + id, "inventory.count.post"},
		{http.MethodPost, "/v1/inventory/count-sheets/" + id + ":post", "inventory.count.write"},
	}
	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			rec := do(t, h, c.method, c.path, "{}", []string{c.duty}, map[string]string{"If-Match": "1", "Idempotency-Key": "k"})
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status %d, want 403", rec.Code)
			}
		})
	}
}

func TestReservationValidation(t *testing.T) {
	h := newServer(t, &fakeStore{})
	rec := do(t, h, http.MethodPost, "/v1/inventory/reservations",
		`{"legalEntityId":"x","productId":"x","qty":"1.0000001","demandType":"GIFT","policy":"RANDOM","shelfLifeMarginDays":400,"requiredDate":"soon"}`,
		[]string{"inventory.reservation.write"}, nil)
	p := decodeProblem(t, rec)
	if rec.Code != http.StatusBadRequest || p.Code != "COM-0001" {
		t.Fatalf("status %d code %s, want 400 COM-0001", rec.Code, p.Code)
	}
	for _, f := range []string{"legalEntityId", "productId", "qty", "demandType", "demandId", "policy", "shelfLifeMarginDays", "requiredDate", "Idempotency-Key"} {
		if p.Errors[f] == "" {
			t.Errorf("no field error for %s: %v", f, p.Errors)
		}
	}
	manual := fmt.Sprintf(`{"legalEntityId":%q,"productId":%q,"qty":"1","demandType":"MANUAL","demandId":"D1","policy":"MANUAL","dimensions":{"warehouse":"W1"}}`, ids.New(), ids.New())
	rec = do(t, h, http.MethodPost, "/v1/inventory/reservations", manual, []string{"inventory.reservation.write"}, key("r-1"))
	if p := decodeProblem(t, rec); rec.Code != http.StatusBadRequest || p.Errors["dimensions"] == "" {
		t.Fatalf("manual reservation without a site: status %d errors %v", rec.Code, p.Errors)
	}
}

func TestReservationPassesRequestAndReplays(t *testing.T) {
	product, entity := ids.New(), ids.New()
	fs := &fakeStore{reserveOut: store.ReserveResult{Shortfall: decimal.RequireFromString("2"), Reservations: []domain.Reservation{
		{ID: ids.New(), ProductID: product, Qty: decimal.NewFromInt(3), Policy: domain.PolicyFEFO, Status: domain.ReservationActive, AllocationNo: 1,
			Dims: domain.Dimensions{"site": "S1", "batch": "B1"}}}}}
	h := newServer(t, fs)
	body := fmt.Sprintf(`{"legalEntityId":%q,"productId":%q,"qty":"5","demandType":"SALES_ORDER","demandId":" SO-1 ","policy":"fefo",
		"requiredDate":"2026-10-01","shelfLifeMarginDays":7,"allowPartial":true,"dimensions":{"site":"S1"}}`, entity, product)
	rec := do(t, h, http.MethodPost, "/v1/inventory/reservations", body, []string{"inventory.reservation.write"}, key("r-2"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	in := fs.reserveIn
	if in.Policy != domain.PolicyFEFO || in.DemandID != "SO-1" || !in.Qty.Equal(decimal.NewFromInt(5)) || in.ShelfLifeMarginDays != 7 ||
		!in.AllowPartial || in.IdempotencyKey != "r-2" || in.Dims["site"] != "S1" || !in.RequiredDate.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("store got %+v", in)
	}
	var resp struct {
		Items []struct {
			Qty        string            `json:"qty"`
			Dimensions map[string]string `json:"dimensions"`
		} `json:"items"`
		Shortfall string `json:"shortfall"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != 1 || resp.Items[0].Qty != "3" || resp.Items[0].Dimensions["batch"] != "B1" || resp.Shortfall != "2" {
		t.Fatalf("response %+v", resp)
	}

	fs.reserveOut.Replayed = true
	rec = do(t, h, http.MethodPost, "/v1/inventory/reservations", body, []string{"inventory.reservation.write"}, key("r-2"))
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: status %d header %q", rec.Code, rec.Header().Get("Idempotent-Replayed"))
	}

	fs.err = httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeReserveShortfall, "only 1 available")
	rec = do(t, h, http.MethodPost, "/v1/inventory/reservations", body, []string{"inventory.reservation.write"}, key("r-3"))
	if rec.Code != http.StatusUnprocessableEntity || decodeProblem(t, rec).Code != domain.CodeReserveShortfall {
		t.Fatalf("shortfall: status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestReservationPolicyDefaultsToFIFOAndToday(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	body := fmt.Sprintf(`{"legalEntityId":%q,"productId":%q,"qty":"1","demandType":"MANUAL","demandId":"D"}`, ids.New(), ids.New())
	if rec := do(t, h, http.MethodPost, "/v1/inventory/reservations", body, []string{"inventory.reservation.write"}, key("r")); rec.Code != http.StatusCreated {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if fs.reserveIn.Policy != domain.PolicyFIFO || !fs.reserveIn.RequiredDate.Equal(time.Now().UTC().Truncate(24*time.Hour)) {
		t.Fatalf("policy %s required %s, want FIFO today", fs.reserveIn.Policy, fs.reserveIn.RequiredDate)
	}
}

func TestReservationListPagesAndRelease(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	product := ids.New()
	rec := do(t, h, http.MethodGet, "/v1/inventory/reservations?status=ACTIVE&limit=2&product="+product, "", []string{"inventory.read"}, nil)
	var page struct {
		Items      []json.RawMessage `json:"items"`
		NextCursor string            `json:"nextCursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if len(page.Items) != 2 || page.NextCursor == "" || fs.reservations.Limit != 3 || fs.reservations.ProductID != product {
		t.Fatalf("items %d cursor %q store filter %+v; want 2 items, a cursor and a lookahead of 3", len(page.Items), page.NextCursor, fs.reservations)
	}
	if rec := do(t, h, http.MethodGet, "/v1/inventory/reservations?status=LOST", "", []string{"inventory.read"}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown status: %d", rec.Code)
	}
	rec = do(t, h, http.MethodDelete, "/v1/inventory/reservations/nope", "", []string{"inventory.reservation.write"}, nil)
	if rec.Code != http.StatusNotFound || decodeProblem(t, rec).Code != domain.CodeReservationNotFound {
		t.Fatalf("bad id: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodDelete, "/v1/inventory/reservations/"+ids.New(), "", []string{"inventory.reservation.write"}, nil)
	if rec.Code != http.StatusOK || !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("release: %d %s", rec.Code, rec.Body.String())
	}
}

func TestBatchExpiryAcceptsDateOrNull(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	product := ids.New()
	rec := do(t, h, http.MethodPut, "/v1/inventory/batch-expiries/"+product+"/B-7", `{"expiryDate":"2027-01-31"}`, []string{"inventory.batch.write"}, nil)
	if rec.Code != http.StatusOK || fs.expiryBatch != "B-7" || fs.expiryProduct != product || fs.expiryDate == nil || fs.expiryDate.Format("2006-01-02") != "2027-01-31" {
		t.Fatalf("status %d store %s %s %v", rec.Code, fs.expiryProduct, fs.expiryBatch, fs.expiryDate)
	}
	rec = do(t, h, http.MethodPut, "/v1/inventory/batch-expiries/"+product+"/B-7", `{"expiryDate":null}`, []string{"inventory.batch.write"}, nil)
	if rec.Code != http.StatusOK || fs.expiryDate != nil {
		t.Fatalf("null expiry: status %d date %v", rec.Code, fs.expiryDate)
	}
	for _, body := range []string{`{}`, `{"expiryDate":"31/01/2027"}`, `{"expiryDate":5}`} {
		if rec := do(t, h, http.MethodPut, "/v1/inventory/batch-expiries/"+product+"/B-7", body, []string{"inventory.batch.write"}, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", body, rec.Code)
		}
	}
}

func TestTransactionInquiryFiltersAndPages(t *testing.T) {
	fs := &fakeStore{txRows: 10}
	h := newServer(t, fs)
	product := ids.New()
	rec := do(t, h, http.MethodGet, "/v1/inventory/transactions?product="+product+"&status=SOLD&sourceType=BLOCKING&from=2026-09-01&to=2026-09-30&limit=4",
		"", []string{"inventory.read"}, nil)
	var page struct {
		Items []struct {
			Qty                string `json:"qty"`
			IssueStatus        string `json:"issueStatus"`
			CostAmountPhysical string `json:"costAmountPhysical"`
		} `json:"items"`
		NextCursor string `json:"nextCursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	f := fs.txFilter
	if f.ProductID != product || f.Status != domain.StatusSold || f.SourceType != "BLOCKING" || f.Limit != 5 ||
		f.From.Format("2006-01-02") != "2026-09-01" || f.To.Format("2006-01-02") != "2026-09-30" {
		t.Fatalf("store filter %+v", f)
	}
	if len(page.Items) != 4 || page.NextCursor == "" || page.Items[0].CostAmountPhysical != "-6.5" || page.Items[0].IssueStatus != "SOLD" {
		t.Fatalf("page %+v", page)
	}
	rec = do(t, h, http.MethodGet, "/v1/inventory/transactions?status=SHIPPED&from=2026-09-30&to=2026-09-01&cursor=x&limit=0", "", []string{"inventory.read"}, nil)
	p := decodeProblem(t, rec)
	for _, field := range []string{"status", "to", "cursor", "limit"} {
		if p.Errors[field] == "" {
			t.Errorf("no field error for %s: %v", field, p.Errors)
		}
	}
}

func TestBlockingValidationAndCreate(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	rec := do(t, h, http.MethodPost, "/v1/inventory/blockings", `{"legalEntityId":"x","productId":"x","qty":"0","targetStatus":"AVAILABLE","reason":"BORED"}`,
		[]string{"inventory.blocking.write"}, nil)
	p := decodeProblem(t, rec)
	for _, f := range []string{"legalEntityId", "productId", "qty", "unit", "targetStatus", "reason", "Idempotency-Key"} {
		if p.Errors[f] == "" {
			t.Errorf("missing field error %s: %v", f, p.Errors)
		}
	}
	body := fmt.Sprintf(`{"legalEntityId":%q,"productId":%q,"dimensions":{"site":"S1","batch":"B1"},"qty":"4","unit":"EA",
		"targetStatus":"QUARANTINE","reason":"RECALL","note":" lot recall ","physicalDate":"2026-09-02"}`, ids.New(), ids.New())
	rec = do(t, h, http.MethodPost, "/v1/inventory/blockings", body, []string{"inventory.blocking.write"}, key("b-1"))
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("status %d etag %q body %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	in := fs.blockIn
	if in.Target != domain.StockQuarantine || in.Reason != "RECALL" || in.Unit != "EA" || in.Note != "lot recall" || in.Dims["batch"] != "B1" ||
		in.IdempotencyKey != "b-1" || in.PhysicalDate.Format("2006-01-02") != "2026-09-02" || !in.Qty.Equal(decimal.NewFromInt(4)) {
		t.Fatalf("store got %+v", in)
	}
	fs.blockReplayed = true
	if rec := do(t, h, http.MethodPost, "/v1/inventory/blockings", body, []string{"inventory.blocking.write"}, key("b-1")); rec.Code != http.StatusOK ||
		rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: %d", rec.Code)
	}
	fs.err = httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeBlockExceeds, "exceeds")
	if rec := do(t, h, http.MethodPost, "/v1/inventory/blockings", body, []string{"inventory.blocking.write"}, key("b-2")); rec.Code != http.StatusUnprocessableEntity ||
		decodeProblem(t, rec).Code != domain.CodeBlockExceeds {
		t.Fatalf("store rule not passed through: %d %s", rec.Code, rec.Body.String())
	}
}

func TestBlockingListAndRelease(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	if rec := do(t, h, http.MethodGet, "/v1/inventory/blockings?state=ACTIVE", "", []string{"inventory.read"}, nil); rec.Code != http.StatusOK || fs.blockings.State != "ACTIVE" {
		t.Fatalf("list: %d filter %+v", rec.Code, fs.blockings)
	}
	if rec := do(t, h, http.MethodGet, "/v1/inventory/blockings?state=OPEN", "", []string{"inventory.read"}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad state: %d", rec.Code)
	}
	path := "/v1/inventory/blockings/" + ids.New() + ":release"
	rec := do(t, h, http.MethodPost, path, `{"disposition":"release"}`, []string{"inventory.blocking.write"}, nil)
	if rec.Code != http.StatusPreconditionRequired || decodeProblem(t, rec).Code != "COM-0428" {
		t.Fatalf("no If-Match: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, path, `{"disposition":"release"}`, []string{"inventory.blocking.write"}, map[string]string{"If-Match": `"3"`})
	if rec.Code != http.StatusOK || fs.releaseVersion != 3 || fs.releaseDispo != "RELEASE" || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("release: %d version %d disposition %q etag %q", rec.Code, fs.releaseVersion, fs.releaseDispo, rec.Header().Get("ETag"))
	}
}

func TestBlockingScrapNeedsItsOwnDutyAndEscalateNeedsIfMatch(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	path := "/v1/inventory/blockings/" + ids.New() + ":release"
	hdr := map[string]string{"If-Match": `"2"`}
	rec := do(t, h, http.MethodPost, path, `{"disposition":"SCRAP"}`, []string{"inventory.blocking.write"}, hdr)
	if rec.Code != http.StatusForbidden || decodeProblem(t, rec).Code != domain.CodeScrapDuty || fs.releaseDispo != "" {
		t.Fatalf("scrap without the scrap duty: %d %s (store reached: %q)", rec.Code, rec.Body.String(), fs.releaseDispo)
	}
	rec = do(t, h, http.MethodPost, path, `{"disposition":"scrap"}`, []string{"inventory.blocking.write", "inventory.blocking.scrap"}, hdr)
	if rec.Code != http.StatusOK || fs.releaseDispo != "SCRAP" || fs.releaseVersion != 2 {
		t.Fatalf("scrap with the duty: %d disposition %q version %d", rec.Code, fs.releaseDispo, fs.releaseVersion)
	}
	block := "/v1/inventory/blockings/" + ids.New() + ":block"
	if rec := do(t, h, http.MethodPost, block, "", []string{"inventory.blocking.write"}, nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("escalate without If-Match: %d", rec.Code)
	}
	rec = do(t, h, http.MethodPost, block, "", []string{"inventory.blocking.write"}, map[string]string{"If-Match": `"5"`})
	if rec.Code != http.StatusOK || fs.releaseVersion != 5 || rec.Header().Get("ETag") != `"6"` {
		t.Fatalf("escalate: %d version %d etag %q", rec.Code, fs.releaseVersion, rec.Header().Get("ETag"))
	}
}

func TestBlockingExpectedReleaseDateIsValidated(t *testing.T) {
	h := newServer(t, &fakeStore{})
	body := fmt.Sprintf(`{"legalEntityId":%q,"productId":%q,"dimensions":{"site":"S1"},"qty":"1","unit":"EA","targetStatus":"QUARANTINE","reason":"QUALITY",
		"physicalDate":"2026-09-02","expectedReleaseDate":%%q}`, ids.New(), ids.New())
	for _, tc := range []struct{ date, field string }{{"someday", "expectedReleaseDate"}, {"2026-09-01", "expectedReleaseDate"}, {"2026-09-09", ""}} {
		rec := do(t, h, http.MethodPost, "/v1/inventory/blockings", fmt.Sprintf(body, tc.date), []string{"inventory.blocking.write"}, key("k-1"))
		if tc.field == "" {
			if rec.Code != http.StatusCreated {
				t.Fatalf("%s: %d %s", tc.date, rec.Code, rec.Body.String())
			}
			continue
		}
		if p := decodeProblem(t, rec); rec.Code != http.StatusBadRequest || p.Errors[tc.field] == "" {
			t.Fatalf("%s: %d %+v", tc.date, rec.Code, p)
		}
	}
}

func TestCountPlanDefaultsAndValidation(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	body := fmt.Sprintf(`{"legalEntityId":%q,"name":" Weekly A ","warehouse":"W1","currency":"USD","unit":"EA"}`, ids.New())
	rec := do(t, h, http.MethodPost, "/v1/inventory/count-plans", body, []string{"inventory.count.write"}, key("p-1"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if !fs.plan.TolerancePct.Equal(decimal.NewFromInt(5)) || !fs.plan.ToleranceValue.Equal(decimal.NewFromInt(100)) || fs.plan.Name != "Weekly A" ||
		fs.plan.Warehouse != "W1" || fs.planKey != "p-1" {
		t.Fatalf("store got %+v key %q; want the 3.16.13 defaults 5 / 100", fs.plan, fs.planKey)
	}
	rec = do(t, h, http.MethodPost, "/v1/inventory/count-plans", `{"legalEntityId":"x","currency":"usd","unit":"","tolerancePct":"120","toleranceValue":"-1"}`,
		[]string{"inventory.count.write"}, nil)
	p := decodeProblem(t, rec)
	for _, f := range []string{"legalEntityId", "name", "currency", "unit", "tolerancePct", "toleranceValue", "Idempotency-Key"} {
		if p.Errors[f] == "" {
			t.Errorf("missing field error %s: %v", f, p.Errors)
		}
	}
	if rec := do(t, h, http.MethodPost, "/v1/inventory/count-plans/"+ids.New()+":generate", "", []string{"inventory.count.write"}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("generate without a key: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, "/v1/inventory/count-plans/"+ids.New()+":generate", "", []string{"inventory.count.write"}, key("g-1")); rec.Code != http.StatusCreated || fs.sheetKey != "g-1" {
		t.Fatalf("generate: %d key %q", rec.Code, fs.sheetKey)
	}
}

func blindSheet() domain.CountSheet {
	return domain.CountSheet{ID: ids.New(), Status: domain.CountOpen, Blind: true, Version: 2, Lines: []domain.CountLine{
		{ID: ids.New(), LineNo: 1, ProductID: ids.New(), Expected: decimal.NewFromInt(50)}}}
}

func expectedOf(t *testing.T, body []byte) *string {
	t.Helper()
	var s struct {
		Lines []struct {
			ExpectedQty *string `json:"expectedQty"`
		} `json:"lines"`
	}
	if err := json.Unmarshal(body, &s); err != nil || len(s.Lines) != 1 {
		t.Fatalf("decode sheet: %v %s", err, body)
	}
	return s.Lines[0].ExpectedQty
}

// FR-16.9 blind count: counters do not see the book quantity while the sheet
// is open; approvers do, and everyone does once it is posted.
func TestBlindSheetHidesExpectedFromCounters(t *testing.T) {
	fs := &fakeStore{sheet: blindSheet()}
	h := newServer(t, fs)
	path := "/v1/inventory/count-sheets/" + fs.sheet.ID
	if e := expectedOf(t, do(t, h, http.MethodGet, path, "", []string{"inventory.read"}, nil).Body.Bytes()); e != nil {
		t.Fatalf("counter saw expected %s on a blind sheet", *e)
	}
	if e := expectedOf(t, do(t, h, http.MethodGet, path, "", []string{"inventory.read", "inventory.count.approve"}, nil).Body.Bytes()); e == nil || *e != "50" {
		t.Fatalf("approver should see expected 50, got %v", e)
	}
	fs.sheet.Status = domain.CountPosted
	if e := expectedOf(t, do(t, h, http.MethodGet, path, "", []string{"inventory.read"}, nil).Body.Bytes()); e == nil {
		t.Fatal("expected stays hidden after posting")
	}
}

func TestEnterCountNeedsIfMatchAndANonNegativeQuantity(t *testing.T) {
	fs := &fakeStore{sheet: blindSheet()}
	h := newServer(t, fs)
	path := "/v1/inventory/count-sheets/" + fs.sheet.ID + "/lines/" + fs.sheet.Lines[0].ID
	if rec := do(t, h, http.MethodPut, path, `{"countedQty":"46"}`, []string{"inventory.count.write"}, nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPut, path, `{"countedQty":"-1"}`, []string{"inventory.count.write"}, map[string]string{"If-Match": "2"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("negative count: %d", rec.Code)
	}
	rec := do(t, h, http.MethodPut, path, `{"countedQty":"46"}`, []string{"inventory.count.write"}, map[string]string{"If-Match": `"2"`})
	if rec.Code != http.StatusOK || fs.countVersion != 2 || !fs.counted.Equal(decimal.NewFromInt(46)) {
		t.Fatalf("status %d version %d counted %s", rec.Code, fs.countVersion, fs.counted)
	}
	if e := expectedOf(t, rec.Body.Bytes()); e != nil {
		t.Fatal("the count response must not reveal a blind expected quantity")
	}
}

// AC-16.6: approval comes from the caller's inventory.count.approve duty, and
// a refusal from the store reaches the client with its code.
func TestPostCountPassesApprovalFromDuty(t *testing.T) {
	fs := &fakeStore{sheet: blindSheet()}
	h := newServer(t, fs)
	path := "/v1/inventory/count-sheets/" + fs.sheet.ID + ":post"
	if rec := do(t, h, http.MethodPost, path, "", []string{"inventory.count.post"}, map[string]string{"If-Match": "2"}); rec.Code != http.StatusOK || fs.countCanApprove {
		t.Fatalf("poster without approve: status %d canApprove %v", rec.Code, fs.countCanApprove)
	}
	if fs.postedParams.AdjustmentAccount != "5100" || fs.countVersion != 2 {
		t.Fatalf("post params %+v version %d", fs.postedParams, fs.countVersion)
	}
	if rec := do(t, h, http.MethodPost, path, "", []string{"inventory.count.post", "inventory.count.approve"}, map[string]string{"If-Match": "2"}); rec.Code != http.StatusOK || !fs.countCanApprove {
		t.Fatalf("approver: status %d canApprove %v", rec.Code, fs.countCanApprove)
	}
	fs.err = httpx.NewProblem(http.StatusForbidden, domain.CodeCountApproval, "variance 8% exceeds 5%")
	if rec := do(t, h, http.MethodPost, path, "", []string{"inventory.count.post"}, map[string]string{"If-Match": "2"}); rec.Code != http.StatusForbidden ||
		decodeProblem(t, rec).Code != domain.CodeCountApproval {
		t.Fatalf("refusal: %d %s", rec.Code, rec.Body.String())
	}
}

func TestCountSheetListValidatesFilters(t *testing.T) {
	fs := &fakeStore{sheet: blindSheet()}
	h := newServer(t, fs)
	plan := ids.New()
	if rec := do(t, h, http.MethodGet, "/v1/inventory/count-sheets?status=OPEN&planId="+plan, "", []string{"inventory.read"}, nil); rec.Code != http.StatusOK ||
		fs.sheets.PlanID != plan || fs.sheets.Status != "OPEN" {
		t.Fatalf("list: %d filter %+v", rec.Code, fs.sheets)
	}
	if rec := do(t, h, http.MethodGet, "/v1/inventory/count-sheets?status=COUNTED&planId=x", "", []string{"inventory.read"}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad filters: %d", rec.Code)
	}
}
