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

// Slice 4 fake-store methods. They record what the HTTP layer sent; the fields
// they use are declared on fakeStore in api_test.go.

func (f *fakeStore) GenerateTags(_ context.Context, planID, key string, count int) ([]domain.CountTag, bool, error) {
	f.tagKey, f.tagCount = key, count
	out := make([]domain.CountTag, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, domain.CountTag{ID: ids.New(), PlanID: planID, Number: domain.TagNumber(int64(i + 1)), Status: domain.TagOpen, Version: 1})
	}
	return out, key == "replay", nil
}

func (f *fakeStore) ListTags(_ context.Context, planID string, status domain.TagStatus) ([]domain.CountTag, error) {
	f.tagStatus = status
	return []domain.CountTag{
		{ID: ids.New(), PlanID: planID, Number: "TAG-000001", Status: domain.TagUsed, ProductID: ids.New(), Dims: domain.Dimensions{"site": "S1"}, Qty: decimal.RequireFromString("0"), Version: 2},
		{ID: ids.New(), PlanID: planID, Number: "TAG-000002", Status: domain.TagOpen, Version: 1},
	}, nil
}

func (f *fakeStore) UseTag(_ context.Context, tagID string, version int, productID string, dims domain.Dimensions, qty decimal.Decimal) (domain.CountTag, error) {
	if f.err != nil {
		return domain.CountTag{}, f.err
	}
	f.tagVersion, f.tagProduct, f.tagDims, f.tagQty = version, productID, dims, qty
	return domain.CountTag{ID: tagID, Number: "TAG-000001", Status: domain.TagUsed, ProductID: productID, Dims: dims, Qty: qty, Version: version + 1}, nil
}

func (f *fakeStore) VoidTag(_ context.Context, tagID string, version int) (domain.CountTag, error) {
	f.tagVersion = version
	return domain.CountTag{ID: tagID, Number: "TAG-000001", Status: domain.TagVoid, Version: version + 1}, nil
}

func (f *fakeStore) TransferTags(_ context.Context, planID, _ string, markOpenMissing, canApprove bool) (domain.CountSheet, bool, error) {
	f.transferMissing, f.transferApprove = markOpenMissing, canApprove
	return domain.CountSheet{ID: ids.New(), PlanID: planID, Status: domain.CountOpen, Version: 1}, false, nil
}

func (f *fakeStore) ReverseJournal(_ context.Context, id string, postingDate time.Time) (domain.Journal, bool, error) {
	if f.err != nil {
		return domain.Journal{}, false, f.err
	}
	f.reverseDate = postingDate
	return domain.Journal{ID: ids.New(), ReversesJournalID: id, Type: domain.JournalMovement, Status: domain.JournalDraft, Version: 1,
		PostingDate: postingDate, Currency: "USD"}, !f.reverseReplay, nil
}

func (f *fakeStore) CopyJournal(_ context.Context, sourceID string, postingDate time.Time, key string, mayOwnershipChange bool) (domain.Journal, bool, error) {
	if f.err != nil {
		return domain.Journal{}, false, f.err
	}
	f.copySource, f.copyDate, f.copyKey, f.copyConsignment = sourceID, postingDate, key, mayOwnershipChange
	return domain.Journal{ID: ids.New(), Type: domain.JournalMovement, Status: domain.JournalDraft, Version: 1, PostingDate: postingDate, Currency: "USD"}, !f.copyReplay, nil
}

func (f *fakeStore) RebuildOnHand(_ context.Context, repair bool) (store.RebuildReport, error) {
	f.repair = repair
	return store.RebuildReport{Checked: 3, DiffCount: 1, Repaired: repair,
		Diffs: []store.RebuildDiff{{LegalEntityID: ids.New(), ProductID: ids.New(), DimensionSetHash: "ab", Fields: []string{"posted_value"}}}}, nil
}

func (f *fakeStore) BackfillTransferSupply(_ context.Context, after string, limit int) (store.SupplyBackfillReport, error) {
	f.backfillAfter, f.backfillLimit = after, limit
	return store.SupplyBackfillReport{Checked: 2, Booked: 1, More: true, Next: "next-id"}, nil
}

func TestSlice4DutiesAreEnforcedPerRoute(t *testing.T) {
	h := newServer(t, &fakeStore{})
	id := ids.New()
	cases := []struct{ method, path, duty string }{
		{http.MethodPost, "/v1/inventory/count-plans/" + id + ":generate-tags", "inventory.read"},
		{http.MethodGet, "/v1/inventory/count-plans/" + id + "/tags", "inventory.count.write"},
		{http.MethodPost, "/v1/inventory/count-plans/" + id + ":transfer-tags", "inventory.count.approve"},
		{http.MethodPost, "/v1/inventory/count-tags/" + id + ":use", "inventory.read"},
		{http.MethodPost, "/v1/inventory/count-tags/" + id + ":void", "inventory.count.post"},
		{http.MethodPost, "/v1/inventory/journals/" + id + ":reverse", "inventory.journal.post"},
		{http.MethodPost, "/internal/v1/inventory/onhand:rebuild", "inventory.journal.post"},
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

func TestGenerateTagsValidatesCountAndKey(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	path := "/v1/inventory/count-plans/" + ids.New() + ":generate-tags"
	duties := []string{"inventory.count.write"}

	for _, body := range []string{`{"count":0}`, `{"count":1001}`} {
		rec := do(t, h, http.MethodPost, path, body, duties, key("k1"))
		if rec.Code != http.StatusBadRequest || decodeProblem(t, rec).Errors["count"] == "" {
			t.Fatalf("%s: status %d body %s, want 400 on count", body, rec.Code, rec.Body.String())
		}
	}
	if rec := do(t, h, http.MethodPost, path, `{"count":5}`, duties, nil); rec.Code != http.StatusBadRequest || decodeProblem(t, rec).Errors["Idempotency-Key"] == "" {
		t.Fatalf("no key: status %d, want 400 on Idempotency-Key", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, "/v1/inventory/count-plans/nope:generate-tags", `{"count":5}`, duties, key("k1")); rec.Code != http.StatusNotFound {
		t.Fatalf("bad plan id: status %d, want 404", rec.Code)
	}
	rec := do(t, h, http.MethodPost, path, `{"count":5}`, duties, key("k1"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []struct {
			TagNumber  string            `json:"tagNumber"`
			Status     string            `json:"status"`
			CountedQty *string           `json:"countedQty"`
			Dimensions map[string]string `json:"dimensions"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 5 || fs.tagCount != 5 || fs.tagKey != "k1" || body.Items[0].Status != "OPEN" || body.Items[0].TagNumber != "TAG-000001" {
		t.Fatalf("items = %+v count=%d key=%q", body.Items, fs.tagCount, fs.tagKey)
	}
	if body.Items[0].CountedQty != nil || body.Items[0].Dimensions == nil {
		t.Fatalf("an open tag has no count and an empty dimension object: %s", rec.Body.String())
	}
	if rec := do(t, h, http.MethodPost, path, `{"count":5}`, duties, key("replay")); rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: status %d, want 200 with Idempotent-Replayed", rec.Code)
	}
}

func TestListTagsFiltersByStatusAndShowsZeroCounts(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	base := "/v1/inventory/count-plans/" + ids.New() + "/tags"
	if rec := do(t, h, http.MethodGet, base+"?status=BOGUS", "", []string{"inventory.read"}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	rec := do(t, h, http.MethodGet, base+"?status=USED", "", []string{"inventory.read"}, nil)
	if rec.Code != http.StatusOK || fs.tagStatus != domain.TagUsed {
		t.Fatalf("status %d, filter %q", rec.Code, fs.tagStatus)
	}
	var body struct {
		Items []struct {
			CountedQty *string `json:"countedQty"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Items[0].CountedQty == nil || *body.Items[0].CountedQty != "0" || body.Items[1].CountedQty != nil {
		t.Fatalf("a used tag shows its zero count, an open tag none: %s", rec.Body.String())
	}
}

func TestUseTagNeedsIfMatchAndValidFields(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	path := "/v1/inventory/count-tags/" + ids.New() + ":use"
	duties := []string{"inventory.count.write"}
	product := ids.New()
	body := `{"productId":"` + product + `","dimensions":{"site":"S1","warehouse":"W1"},"countedQty":"3.5"}`

	if rec := do(t, h, http.MethodPost, path, body, duties, nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: status %d, want 428", rec.Code)
	}
	rec := do(t, h, http.MethodPost, path, `{"productId":"x","countedQty":"-1"}`, duties, map[string]string{"If-Match": `"1"`})
	p := decodeProblem(t, rec)
	if rec.Code != http.StatusBadRequest || p.Errors["productId"] == "" || p.Errors["countedQty"] == "" {
		t.Fatalf("status %d errors %v, want 400 on productId and countedQty", rec.Code, p.Errors)
	}
	rec = do(t, h, http.MethodPost, path, body, duties, map[string]string{"If-Match": `"2"`})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if fs.tagVersion != 2 || fs.tagProduct != product || !fs.tagQty.Equal(decimal.RequireFromString("3.5")) || fs.tagDims["warehouse"] != "W1" {
		t.Fatalf("store got version=%d product=%s qty=%s dims=%v", fs.tagVersion, fs.tagProduct, fs.tagQty, fs.tagDims)
	}
	if rec.Header().Get("ETag") != `"3"` {
		t.Fatalf("ETag = %q, want the new version", rec.Header().Get("ETag"))
	}
	var tag struct {
		Status     string `json:"status"`
		CountedQty string `json:"countedQty"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tag); err != nil || tag.Status != "USED" || tag.CountedQty != "3.5" {
		t.Fatalf("tag = %+v (%v)", tag, err)
	}
}

func TestUseTagPassesBusinessRuleProblemThrough(t *testing.T) {
	fs := &fakeStore{err: httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeTagUsed, "tag TAG-000001 has already been used")}
	h := newServer(t, fs)
	body := `{"productId":"` + ids.New() + `","dimensions":{"site":"S1"},"countedQty":"1"}`
	rec := do(t, h, http.MethodPost, "/v1/inventory/count-tags/"+ids.New()+":use", body, []string{"inventory.count.write"}, map[string]string{"If-Match": "1"})
	if rec.Code != http.StatusUnprocessableEntity || decodeProblem(t, rec).Code != domain.CodeTagUsed {
		t.Fatalf("status %d body %s, want 422 INV-TAG-001", rec.Code, rec.Body.String())
	}
}

func TestVoidTagNeedsIfMatch(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	path := "/v1/inventory/count-tags/" + ids.New() + ":void"
	if rec := do(t, h, http.MethodPost, path, "", []string{"inventory.count.write"}, nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: status %d, want 428", rec.Code)
	}
	rec := do(t, h, http.MethodPost, path, "", []string{"inventory.count.write"}, map[string]string{"If-Match": `"4"`})
	if rec.Code != http.StatusOK || fs.tagVersion != 4 || rec.Header().Get("ETag") != `"5"` {
		t.Fatalf("status %d version %d etag %q", rec.Code, fs.tagVersion, rec.Header().Get("ETag"))
	}
}

func TestTransferTagsPassesMarkMissingAndApprovalFromDuty(t *testing.T) {
	path := "/v1/inventory/count-plans/" + ids.New() + ":transfer-tags"
	cases := []struct {
		duties  []string
		approve bool
	}{
		{[]string{"inventory.count.write"}, false},
		{[]string{"inventory.count.write", "inventory.count.approve"}, true},
	}
	for _, c := range cases {
		fs := &fakeStore{}
		h := newServer(t, fs)
		rec := do(t, h, http.MethodPost, path, `{"markOpenMissing":true}`, c.duties, key("t1"))
		if rec.Code != http.StatusCreated {
			t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
		}
		if !fs.transferMissing || fs.transferApprove != c.approve {
			t.Fatalf("duties %v: store got markOpenMissing=%v canApprove=%v", c.duties, fs.transferMissing, fs.transferApprove)
		}
	}
	h := newServer(t, &fakeStore{})
	if rec := do(t, h, http.MethodPost, path, `{}`, []string{"inventory.count.write"}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("no key: status %d, want 400", rec.Code)
	}
}

func TestReverseJournalValidatesDateAndReplays(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	id := ids.New()
	path := "/v1/inventory/journals/" + id + ":reverse"
	duties := []string{"inventory.journal.write"}
	for _, body := range []string{`{}`, `{"postingDate":"30-09-2026"}`} {
		rec := do(t, h, http.MethodPost, path, body, duties, nil)
		if rec.Code != http.StatusBadRequest || decodeProblem(t, rec).Errors["postingDate"] == "" {
			t.Fatalf("%s: status %d, want 400 on postingDate", body, rec.Code)
		}
	}
	rec := do(t, h, http.MethodPost, path, `{"postingDate":"2026-09-30"}`, duties, nil)
	if rec.Code != http.StatusCreated || fs.reverseDate.Format("2006-01-02") != "2026-09-30" {
		t.Fatalf("status %d, date %s", rec.Code, fs.reverseDate)
	}
	var j struct {
		ReversesJournalID string `json:"reversesJournalId"`
		Status            string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &j); err != nil || j.ReversesJournalID != id || j.Status != "DRAFT" {
		t.Fatalf("reversal = %+v (%v)", j, err)
	}
	fs.reverseReplay = true
	rec = do(t, h, http.MethodPost, path, `{"postingDate":"2026-09-30"}`, duties, nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: status %d headers %v", rec.Code, rec.Header())
	}
	fs.err = httpx.NewProblem(http.StatusConflict, store.CodeJournalState, "only a POSTED journal can be reversed")
	if rec := do(t, h, http.MethodPost, path, `{"postingDate":"2026-09-30"}`, duties, nil); rec.Code != http.StatusConflict {
		t.Fatalf("draft original: status %d, want 409", rec.Code)
	}
}

func TestRebuildNeedsAdminDutyAndReportsDiffs(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	if rec := do(t, h, http.MethodPost, "/internal/v1/inventory/onhand:rebuild?repair=yes", "", []string{"inventory.admin"}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad repair flag: status %d, want 400", rec.Code)
	}
	rec := do(t, h, http.MethodPost, "/internal/v1/inventory/onhand:rebuild", "", []string{"inventory.admin"}, nil)
	if rec.Code != http.StatusOK || fs.repair {
		t.Fatalf("compare only: status %d repair=%v", rec.Code, fs.repair)
	}
	rec = do(t, h, http.MethodPost, "/internal/v1/inventory/onhand:rebuild?repair=true", "", []string{"inventory.admin"}, nil)
	if rec.Code != http.StatusOK || !fs.repair {
		t.Fatalf("repair: status %d repair=%v", rec.Code, fs.repair)
	}
	var body struct {
		Checked   int  `json:"checked"`
		DiffCount int  `json:"diffCount"`
		Repaired  bool `json:"repaired"`
		Diffs     []struct {
			LegalEntityID string   `json:"legalEntityId"`
			Fields        []string `json:"fields"`
		} `json:"diffs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Checked != 3 || body.DiffCount != 1 || !body.Repaired || len(body.Diffs) != 1 || body.Diffs[0].Fields[0] != "posted_value" || body.Diffs[0].LegalEntityID == "" {
		t.Fatalf("body = %+v", body)
	}
}

func TestCopyJournalNeedsKeyAndValidDateAndReplays(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	id := ids.New()
	path := "/v1/inventory/journals/" + id + ":copy"
	duties := []string{"inventory.journal.write"}
	if rec := do(t, h, http.MethodPost, path, `{}`, []string{"inventory.read"}, map[string]string{"Idempotency-Key": "k1"}); rec.Code != http.StatusForbidden {
		t.Fatalf("read-only duty: status %d, want 403", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, path, `{}`, duties, nil); rec.Code != http.StatusBadRequest || decodeProblem(t, rec).Errors["Idempotency-Key"] == "" {
		t.Fatalf("no key: status %d, want 400 on Idempotency-Key", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, path, `{"postingDate":"30-09-2026"}`, duties, map[string]string{"Idempotency-Key": "k1"}); rec.Code != http.StatusBadRequest || decodeProblem(t, rec).Errors["postingDate"] == "" {
		t.Fatalf("bad date: status %d, want 400 on postingDate", rec.Code)
	}
	rec := do(t, h, http.MethodPost, path, `{"postingDate":"2026-10-05"}`, duties, map[string]string{"Idempotency-Key": "k1"})
	if rec.Code != http.StatusCreated || fs.copySource != id || fs.copyKey != "k1" || fs.copyDate.Format("2006-01-02") != "2026-10-05" || fs.copyConsignment {
		t.Fatalf("copy: status %d source %s key %s date %s consignment %v", rec.Code, fs.copySource, fs.copyKey, fs.copyDate, fs.copyConsignment)
	}
	// The consignment duty travels to the store, which needs it for an OWNERSHIP_CHANGE source.
	rec = do(t, h, http.MethodPost, path, `{}`, []string{"inventory.journal.write", "inventory.consignment.write"}, map[string]string{"Idempotency-Key": "k4"})
	if rec.Code != http.StatusCreated || !fs.copyConsignment {
		t.Fatalf("copy with consignment duty: status %d consignment %v", rec.Code, fs.copyConsignment)
	}
	rec = do(t, h, http.MethodPost, path, `{}`, duties, map[string]string{"Idempotency-Key": "k2"})
	if rec.Code != http.StatusCreated || !fs.copyDate.IsZero() {
		t.Fatalf("no date keeps the source date: status %d date %s", rec.Code, fs.copyDate)
	}
	fs.copyReplay = true
	rec = do(t, h, http.MethodPost, path, `{}`, duties, map[string]string{"Idempotency-Key": "k1"})
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: status %d headers %v", rec.Code, rec.Header())
	}
	fs.err = httpx.NewProblem(http.StatusConflict, domain.CodeJournalNotReady, "a reversal cannot be copied")
	if rec := do(t, h, http.MethodPost, path, `{}`, duties, map[string]string{"Idempotency-Key": "k3"}); rec.Code != http.StatusConflict {
		t.Fatalf("uncopyable source: status %d, want 409", rec.Code)
	}
}
