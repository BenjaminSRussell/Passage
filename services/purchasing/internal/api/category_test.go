package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// categoryLog is what the category methods of the fake hold and received.
type categoryLog struct {
	vendors  []domain.CategoryVendor
	managers []domain.CategoryManager
	created  *domain.CategoryVendor
	updated  struct {
		id, status, notes string
		ifMatch           int
		from              domain.Day
		to                *domain.Day
	}
	deleted     string
	lastVendors store.CategoryVendorFilter
	err         error
	assignCalls int
}

func (f *fakeStore) CreateCategoryVendor(_ context.Context, in domain.CategoryVendor, _ store.Idem) (domain.CategoryVendor, error) {
	in.ID, in.Version = ids.New(), 1
	f.categories.created = &in
	return in, f.categories.err
}

func (f *fakeStore) GetCategoryVendor(_ context.Context, id string) (domain.CategoryVendor, error) {
	for _, v := range f.categories.vendors {
		if v.ID == id {
			return v, nil
		}
	}
	return domain.CategoryVendor{}, httpx.NewProblem(http.StatusNotFound, store.CodeCategoryVendorNotFound, "not found")
}

func (f *fakeStore) ListCategoryVendors(_ context.Context, flt store.CategoryVendorFilter, _ string, _ int) (store.Page[domain.CategoryVendor], error) {
	f.categories.lastVendors = flt
	return store.Page[domain.CategoryVendor]{Items: append([]domain.CategoryVendor{}, f.categories.vendors...)}, f.categories.err
}

func (f *fakeStore) UpdateCategoryVendor(_ context.Context, id string, ifMatch int, status string, from domain.Day, to *domain.Day, notes string) (domain.CategoryVendor, error) {
	u := &f.categories.updated
	u.id, u.ifMatch, u.status, u.from, u.to, u.notes = id, ifMatch, status, from, to, notes
	cur, _ := f.GetCategoryVendor(context.Background(), id)
	cur.Status, cur.ValidFrom, cur.ValidTo, cur.Version = status, from, to, ifMatch+1
	return cur, f.categories.err
}

func (f *fakeStore) CategoryAssignments(_ context.Context, _ string, categories []string) ([]domain.CategoryVendor, error) {
	f.categories.assignCalls++
	var out []domain.CategoryVendor
	for _, v := range f.categories.vendors {
		for _, c := range categories {
			if v.Category == c {
				out = append(out, v)
			}
		}
	}
	return out, nil
}

func (f *fakeStore) CreateCategoryManager(_ context.Context, in domain.CategoryManager, _ store.Idem) (domain.CategoryManager, error) {
	in.ID = ids.New()
	f.categories.managers = append(f.categories.managers, in)
	return in, f.categories.err
}

func (f *fakeStore) ListCategoryManagers(context.Context, store.CategoryManagerFilter, string, int) (store.Page[domain.CategoryManager], error) {
	return store.Page[domain.CategoryManager]{Items: append([]domain.CategoryManager{}, f.categories.managers...)}, f.categories.err
}

func (f *fakeStore) DeleteCategoryManager(_ context.Context, id string) error {
	f.categories.deleted = id
	return f.categories.err
}

const (
	catVendorsPath  = "/v1/procurement/category-vendors"
	catManagersPath = "/v1/procurement/category-managers"
)

func TestCategoryVendor_CreateValidatesAndNeedsTheDuty(t *testing.T) {
	h := newHarness(t)
	vendor := ids.New()
	good := `{"legalEntityId":"` + leA + `","category":" IT hardware ","vendorId":"` + vendor + `","status":"APPROVED","validTo":"2026-12-31"}`
	maintain := []string{dutyCategoryMaintain}
	for _, tc := range []struct {
		name   string
		c      call
		status int
		code   string
	}{
		{"inquire duty cannot write", call{method: "POST", path: catVendorsPath, user: userA, duties: []string{dutyPurchaseOrderInquire}, body: good, headers: idem("k")}, 403, "AUTH-0003"},
		{"no Idempotency-Key", call{method: "POST", path: catVendorsPath, user: userA, duties: maintain, body: good}, 400, codeIdemRequired},
		{"bad status", call{method: "POST", path: catVendorsPath, user: userA, duties: maintain, headers: idem("k"),
			body: strings.Replace(good, "APPROVED", "MAYBE", 1)}, 422, codeInvalid},
		{"blank category", call{method: "POST", path: catVendorsPath, user: userA, duties: maintain, headers: idem("k"),
			body: strings.Replace(good, " IT hardware ", "  ", 1)}, 422, codeInvalid},
		{"window ends before it starts", call{method: "POST", path: catVendorsPath, user: userA, duties: maintain, headers: idem("k"),
			body: strings.Replace(good, `"validTo":"2026-12-31"`, `"validFrom":"2026-06-01","validTo":"2026-05-31"`, 1)}, 422, codeInvalid},
		{"vendor id not a UUID", call{method: "POST", path: catVendorsPath, user: userA, duties: maintain, headers: idem("k"),
			body: strings.Replace(good, vendor, "nope", 1)}, 422, codeInvalid},
		{"legal entity outside scope", call{method: "POST", path: catVendorsPath, user: userA, duties: maintain, headers: idem("k"),
			body: strings.Replace(good, leA, "00000000-0000-7000-8000-0000000001e2", 1)}, 403, codeScope},
	} {
		rec := h.do(tc.c)
		if rec.Code != tc.status || problemCode(t, rec) != tc.code {
			t.Errorf("%s: status %d code %s: %s", tc.name, rec.Code, problemCode(t, rec), rec.Body)
		}
	}
	rec := h.do(call{method: "POST", path: catVendorsPath, user: userA, duties: maintain, body: good, headers: idem("ok")})
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("create: status %d ETag %q: %s", rec.Code, rec.Header().Get("ETag"), rec.Body)
	}
	got := h.fake.categories.created
	if got == nil || got.Category != "IT hardware" || got.ValidFrom.String() != "2026-03-01" || got.ValidTo == nil || got.ValidTo.String() != "2026-12-31" {
		t.Errorf("store got %+v; want the trimmed category and validFrom defaulting to today", got)
	}
}

func TestCategoryVendor_ChangeNeedsIfMatchAndKeepsTheStart(t *testing.T) {
	h := newHarness(t)
	id := ids.New()
	h.fake.categories.vendors = []domain.CategoryVendor{{ID: id, LegalEntityID: leA, Category: "IT", VendorID: ids.New(), Status: domain.AssignmentApproved,
		ValidFrom: mustDay(t, "2026-01-15"), Version: 4}}
	path := catVendorsPath + "/" + id
	maintain := []string{dutyCategoryMaintain}
	if rec := h.do(call{method: "PUT", path: path, user: userA, duties: maintain, body: `{"status":"BLOCKED"}`}); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do(call{method: "PUT", path: path, user: userA, duties: maintain, body: `{"status":"NOPE"}`, headers: map[string]string{"If-Match": "4"}}); rec.Code != 422 {
		t.Fatalf("bad status: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do(call{method: "PUT", path: path, user: userA, duties: maintain, body: `{"status":"BLOCKED","validTo":"2026-01-01"}`, headers: map[string]string{"If-Match": "4"}}); rec.Code != 422 {
		t.Fatalf("validTo before the kept validFrom: %d %s", rec.Code, rec.Body)
	}
	rec := h.do(call{method: "PUT", path: path, user: userA, duties: maintain, body: `{"status":"BLOCKED","notes":" quality hold "}`, headers: map[string]string{"If-Match": "4"}})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"5"` {
		t.Fatalf("change: %d etag %s: %s", rec.Code, rec.Header().Get("ETag"), rec.Body)
	}
	if u := h.fake.categories.updated; u.id != id || u.ifMatch != 4 || u.status != "BLOCKED" || u.from.String() != "2026-01-15" || u.notes != "quality hold" {
		t.Errorf("store got %+v", u)
	}
	h.fake.categories.err = httpx.NewProblem(http.StatusPreconditionFailed, store.CodeCategoryVendorStale, "stale")
	if rec := h.do(call{method: "PUT", path: path, user: userA, duties: maintain, body: `{"status":"BLOCKED"}`, headers: map[string]string{"If-Match": "1"}}); rec.Code != 412 {
		t.Fatalf("stale: %d %s", rec.Code, rec.Body)
	}
}

func TestCategoryVendor_ListFiltersAndInquireMayRead(t *testing.T) {
	h := newHarness(t)
	vendor := ids.New()
	rec := h.do(call{method: "GET", path: catVendorsPath + "?legalEntityId=" + leA + "&category=IT&vendorId=" + vendor + "&status=BLOCKED", duties: []string{dutyPurchaseOrderInquire}})
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	if f := h.fake.categories.lastVendors; f.LegalEntityID != leA || f.Category != "IT" || f.VendorID != vendor || f.Status != "BLOCKED" {
		t.Errorf("filter = %+v", f)
	}
	for _, q := range []string{"?status=NOPE", "?vendorId=x", "?legalEntityId=x"} {
		if rec := h.do(call{method: "GET", path: catVendorsPath + q, duties: []string{dutyPurchaseOrderInquire}}); rec.Code != 422 {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}
	if rec := h.do(call{method: "GET", path: catVendorsPath, duties: []string{dutyRequisitionCreate}}); rec.Code != http.StatusForbidden {
		t.Errorf("a requester has no access: %d", rec.Code)
	}
}

func TestCategoryManager_CreateListDelete(t *testing.T) {
	h := newHarness(t)
	worker := ids.New()
	body := `{"legalEntityId":"` + leA + `","category":"IT","workerId":"` + worker + `"}`
	maintain := []string{dutyCategoryMaintain}
	if rec := h.do(call{method: "POST", path: catManagersPath, user: userA, duties: []string{dutyPurchaseOrderInquire}, body: body, headers: idem("m")}); rec.Code != 403 {
		t.Fatalf("inquire duty: %d", rec.Code)
	}
	if rec := h.do(call{method: "POST", path: catManagersPath, user: userA, duties: maintain, body: strings.Replace(body, worker, "bad", 1), headers: idem("m")}); rec.Code != 422 {
		t.Fatalf("bad worker: %d", rec.Code)
	}
	if rec := h.do(call{method: "POST", path: catManagersPath, user: userA, duties: maintain, body: body, headers: idem("m")}); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	rec := h.do(call{method: "GET", path: catManagersPath, duties: []string{dutyPurchaseOrderInquire}})
	var page pageBody[domain.CategoryManager]
	_ = json.Unmarshal(rec.Body.Bytes(), &page)
	if rec.Code != 200 || len(page.Items) != 1 || page.Items[0].WorkerID != worker {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	id := page.Items[0].ID
	if rec := h.do(call{method: "DELETE", path: catManagersPath + "/" + id, user: userA, duties: maintain}); rec.Code != http.StatusNoContent || h.fake.categories.deleted != id {
		t.Fatalf("delete: %d deleted %q", rec.Code, h.fake.categories.deleted)
	}
	h.fake.categories.err = httpx.NewProblem(http.StatusNotFound, store.CodeCategoryManagerNotFound, "gone")
	if rec := h.do(call{method: "DELETE", path: catManagersPath + "/" + id, user: userA, duties: maintain}); rec.Code != 404 {
		t.Fatalf("missing: %d", rec.Code)
	}
}

func TestPolicy_CategoryAccessModeIsOptionalAndValidated(t *testing.T) {
	h := newHarness(t)
	maintain := []string{dutyPolicyMaintain}
	flags := `"changeManagementDefault":true,"poFourEyes":false,"allowInvoiceBeforeReceipt":false`
	rec := h.do(call{method: "PUT", path: policyPath, user: userA, duties: maintain, body: `{` + flags + `,"categoryAccessMode":"loose"}`, headers: map[string]string{"If-Match": "0"}})
	if rec.Code != 422 || problemCode(t, rec) != codeInvalid {
		t.Fatalf("bad mode: %d %s", rec.Code, rec.Body)
	}
	h.fake.pol.saved = &domain.Policy{LegalEntityID: leA, ChangeManagementDefault: true, CategoryAccessMode: domain.AccessHard, Version: 2}
	rec = h.do(call{method: "PUT", path: policyPath, user: userA, duties: maintain, body: `{` + flags + `}`, headers: map[string]string{"If-Match": "2"}})
	if rec.Code != 200 || h.fake.pol.put.CategoryAccessMode != domain.AccessHard {
		t.Fatalf("an omitted mode keeps the saved HARD: %d %+v", rec.Code, h.fake.pol.put)
	}
	rec = h.do(call{method: "PUT", path: policyPath, user: userA, duties: maintain, body: `{` + flags + `,"categoryAccessMode":"OFF"}`, headers: map[string]string{"If-Match": "2"}})
	if rec.Code != 200 || h.fake.pol.put.CategoryAccessMode != domain.AccessOff {
		t.Fatalf("OFF: %d %+v", rec.Code, h.fake.pol.put)
	}
}

// accessFixture sets a policy mode and an IT category with one approved and one provisional vendor.
func accessFixture(h *harness, mode string) (approved, provisional, stranger string) {
	approved, provisional, stranger = ids.New(), ids.New(), ids.New()
	h.fake.pol.saved = &domain.Policy{LegalEntityID: leA, ChangeManagementDefault: true, CategoryAccessMode: mode, Version: 1}
	h.fake.categories.vendors = []domain.CategoryVendor{
		{ID: ids.New(), LegalEntityID: leA, Category: "IT hardware", VendorID: approved, Status: domain.AssignmentApproved, ValidFrom: domain.Day{Time: h.api.today().Time.AddDate(-1, 0, 0)}},
		{ID: ids.New(), LegalEntityID: leA, Category: "IT hardware", VendorID: provisional, Status: domain.AssignmentProvisional, ValidFrom: h.api.today()},
	}
	return
}

func draftRequisition(h *harness, vendor string) string {
	id := ids.New()
	h.fake.requisitions[id] = store.Requisition{ID: id, Number: "PR-00000042", LegalEntityID: leA, RequesterID: userA, Status: store.RequisitionDraft, Version: 1,
		Currency: "USD", Total: decimal.NewFromInt(7000), Lines: []store.RequisitionLine{
			{LineNo: 1, Category: "OFFICE", PreferredVendorID: vendor},
			{LineNo: 2, Category: "IT hardware", PreferredVendorID: vendor}}}
	return id
}

func submitRequisition(h *harness, id string) *httptest.ResponseRecorder {
	return h.do(call{method: "POST", path: "/v1/procurement/requisitions/" + id + ":submit", user: userA, duties: []string{dutyRequisitionCreate},
		headers: map[string]string{"Idempotency-Key": "s-" + id, "If-Match": `"1"`}})
}

// AC-17.12 (category access half): HARD refuses a requisition whose preferred vendor is not assigned
// to the category, before the store is asked to submit it.
func TestSubmitRequisition_HardRefusesAnUnassignedVendor(t *testing.T) {
	h := newHarness(t)
	approved, _, stranger := accessFixture(h, domain.AccessHard)
	rec := submitRequisition(h, draftRequisition(h, stranger))
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != domain.CodeCategoryAccess {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "PR-00000042 line 2") || h.fake.lastSubmit.version != 0 {
		t.Errorf("the problem must name the line and the store must not be called: %s (version %d)", rec.Body, h.fake.lastSubmit.version)
	}
	rec = submitRequisition(h, draftRequisition(h, approved))
	var out store.Requisition
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusOK || len(out.Warnings) != 0 {
		t.Fatalf("an assigned vendor submits cleanly: %d %s", rec.Code, rec.Body)
	}
}

func TestSubmitRequisition_SoftWarnsAndProvisionalWarns(t *testing.T) {
	h := newHarness(t)
	_, provisional, stranger := accessFixture(h, domain.AccessSoft)
	for vendor, code := range map[string]string{stranger: domain.CodeCategoryAccess, provisional: domain.CodeCategoryProvisional} {
		rec := submitRequisition(h, draftRequisition(h, vendor))
		var out store.Requisition
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if rec.Code != http.StatusOK || len(out.Warnings) != 1 || out.Warnings[0].Code != code || out.Warnings[0].LineNo != 2 {
			t.Errorf("vendor %s: %d %s; want one %s warning on line 2", vendor, rec.Code, rec.Body, code)
		}
	}
}

func TestSubmitRequisition_OffAndNoAssignmentsSkipTheCheck(t *testing.T) {
	h := newHarness(t)
	_, _, stranger := accessFixture(h, domain.AccessOff)
	if rec := submitRequisition(h, draftRequisition(h, stranger)); rec.Code != http.StatusOK || h.fake.categories.assignCalls != 0 {
		t.Fatalf("OFF: %d, assignment reads %d", rec.Code, h.fake.categories.assignCalls)
	}
	h.fake.pol.saved.CategoryAccessMode = domain.AccessHard
	h.fake.categories.vendors = nil
	if rec := submitRequisition(h, draftRequisition(h, stranger)); rec.Code != http.StatusOK {
		t.Fatalf("a category with no assignments is open even in HARD: %d %s", rec.Code, rec.Body)
	}
}

func TestConsolidate_HardRefusesAVendorOverrideOutsideTheCategory(t *testing.T) {
	h := newHarness(t)
	approved, _, stranger := accessFixture(h, domain.AccessHard)
	h.fake.sourcing = sourcing(t, leA, []fixtureLine{{approved, "", "2027-03-01"}, {approved, "", "2027-03-02"}})
	for i := range h.fake.sourcing {
		h.fake.sourcing[i].Category = "IT hardware"
	}
	override := func(i int) string {
		if i == 1 {
			return `,"vendorId":"` + stranger + `"`
		}
		return ""
	}
	rec := h.do(call{method: "POST", path: "/v1/procurement/requisitions:consolidate", duties: []string{dutyPurchaseOrderMaintain},
		body: consolidateJSON(h.fake.sourcing, override, ""), headers: idem("c1")})
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != domain.CodeCategoryAccess || len(h.fake.lastConsolidated) != 0 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	rec = h.do(call{method: "POST", path: "/v1/procurement/requisitions:consolidate", duties: []string{dutyPurchaseOrderMaintain},
		body: consolidateJSON(h.fake.sourcing, none, ""), headers: idem("c2")})
	if rec.Code != http.StatusCreated {
		t.Fatalf("assigned vendor: %d %s", rec.Code, rec.Body)
	}
}

func TestConsolidate_SoftReturnsWarnings(t *testing.T) {
	h := newHarness(t)
	_, _, stranger := accessFixture(h, domain.AccessSoft)
	h.fake.sourcing = sourcing(t, leA, []fixtureLine{{stranger, "", "2027-03-01"}})
	h.fake.sourcing[0].Category = "IT hardware"
	rec := h.do(call{method: "POST", path: "/v1/procurement/requisitions:consolidate", duties: []string{dutyPurchaseOrderMaintain},
		body: consolidateJSON(h.fake.sourcing, none, ""), headers: idem("c3")})
	var out consolidateResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusCreated || len(out.Warnings) != 1 || out.Warnings[0].Code != domain.CodeCategoryAccess {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestCreateOrder_HardChecksTheVendorAgainstTheRequisitionLineCategory(t *testing.T) {
	h := newHarness(t)
	approved, _, stranger := accessFixture(h, domain.AccessHard)
	h.fake.sourcing = sourcing(t, leA, []fixtureLine{{approved, "", "2027-03-01"}})
	h.fake.sourcing[0].Category = "IT hardware"
	line := h.fake.sourcing[0].ID
	body := func(vendor string) string {
		return `{"legalEntityId":"` + leA + `","vendorId":"` + vendor + `","currency":"USD","lines":[{"description":"Laptop","qty":"1","unit":"EA","unitPrice":"900",` +
			`"deliveryDate":"2026-03-10","requisitionLineId":"` + line + `"}]}`
	}
	rec := h.do(call{method: "POST", path: "/v1/procurement/purchase-orders", user: userA, duties: []string{dutyPurchaseOrderMaintain}, body: body(stranger), headers: idem("po1")})
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != domain.CodeCategoryAccess || len(h.fake.orders) != 0 {
		t.Fatalf("stranger: %d %s", rec.Code, rec.Body)
	}
	rec = h.do(call{method: "POST", path: "/v1/procurement/purchase-orders", user: userA, duties: []string{dutyPurchaseOrderMaintain}, body: body(approved), headers: idem("po2")})
	if rec.Code != http.StatusCreated {
		t.Fatalf("assigned vendor: %d %s", rec.Code, rec.Body)
	}
}

func directOrder(vendor, lines string) string {
	return `{"legalEntityId":"` + leA + `","vendorId":"` + vendor + `","currency":"USD","lines":[` + lines + `]}`
}

// A buyer cannot bypass HARD by raising the PO directly: a direct line that names a
// restricted category is checked against the PO vendor like a requisition line.
func TestCreateOrder_DirectLineCategoryIsChecked(t *testing.T) {
	h := newHarness(t)
	approved, provisional, stranger := accessFixture(h, domain.AccessHard)
	line := `{"description":"Laptop","category":" IT hardware ","qty":"1","unit":"EA","unitPrice":"900","deliveryDate":"2026-03-10"}`
	rec := h.do(call{method: "POST", path: "/v1/procurement/purchase-orders", user: userA, duties: []string{dutyPurchaseOrderMaintain},
		body: directOrder(stranger, line), headers: idem("d1")})
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != domain.CodeCategoryAccess || len(h.fake.orders) != 0 {
		t.Fatalf("stranger: %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "purchase order line 1") {
		t.Errorf("the refusal names the PO line: %s", rec.Body)
	}
	rec = h.do(call{method: "POST", path: "/v1/procurement/purchase-orders", user: userA, duties: []string{dutyPurchaseOrderMaintain},
		body: directOrder(approved, line), headers: idem("d2")})
	if rec.Code != http.StatusCreated || h.fake.orders[0].Lines[0].Category != "IT hardware" {
		t.Fatalf("approved: %d %s", rec.Code, rec.Body)
	}
	// PROVISIONAL passes HARD with a warning on the PO answer; SOFT returns the violation as one.
	rec = h.do(call{method: "POST", path: "/v1/procurement/purchase-orders", user: userA, duties: []string{dutyPurchaseOrderMaintain},
		body: directOrder(provisional, line), headers: idem("d3")})
	var out store.PurchaseOrder
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusCreated || len(out.Warnings) != 1 || out.Warnings[0].Code != domain.CodeCategoryProvisional {
		t.Fatalf("provisional: %d %s", rec.Code, rec.Body)
	}
	h.fake.pol.saved.CategoryAccessMode = domain.AccessSoft
	rec = h.do(call{method: "POST", path: "/v1/procurement/purchase-orders", user: userA, duties: []string{dutyPurchaseOrderMaintain},
		body: directOrder(stranger, line), headers: idem("d4")})
	out = store.PurchaseOrder{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusCreated || len(out.Warnings) != 1 || out.Warnings[0].Code != domain.CodeCategoryAccess || out.Warnings[0].LineNo != 1 {
		t.Fatalf("SOFT: %d %s", rec.Code, rec.Body)
	}
}

func TestCreateOrder_RequisitionLineFixesTheCategory(t *testing.T) {
	h := newHarness(t)
	approved, _, _ := accessFixture(h, domain.AccessHard)
	h.fake.sourcing = sourcing(t, leA, []fixtureLine{{approved, "", "2027-03-01"}})
	h.fake.sourcing[0].Category = "IT hardware"
	withCategory := func(c string) string {
		return `{"description":"Laptop","category":"` + c + `","qty":"1","unit":"EA","unitPrice":"900","deliveryDate":"2026-03-10","requisitionLineId":"` +
			h.fake.sourcing[0].ID + `"}`
	}
	rec := h.do(call{method: "POST", path: "/v1/procurement/purchase-orders", user: userA, duties: []string{dutyPurchaseOrderMaintain},
		body: directOrder(approved, withCategory("Office")), headers: idem("r1")})
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != codeInvalid || !strings.Contains(rec.Body.String(), "lines[0].category") {
		t.Fatalf("relabelled requisition line: %d %s", rec.Code, rec.Body)
	}
	rec = h.do(call{method: "POST", path: "/v1/procurement/purchase-orders", user: userA, duties: []string{dutyPurchaseOrderMaintain},
		body: directOrder(approved, withCategory("")), headers: idem("r2")})
	if rec.Code != http.StatusCreated || h.fake.orders[0].Lines[0].Category != "IT hardware" {
		t.Fatalf("blank takes the requisition line's category: %d %s %+v", rec.Code, rec.Body, h.fake.orders)
	}
}

// On a PATCH the vendor cannot change, existing lines keep their category (so a later
// BLOCKED assignment does not freeze the PO) and only the added lines are checked.
func TestEditOrder_ChecksAddedLinesAndKeepsCategories(t *testing.T) {
	h := newHarness(t)
	accessFixture(h, domain.AccessHard) // vendorA is not assigned to IT hardware
	po := storedPO(h, domain.POStatusDraft)
	po.Lines[2].Category = "IT hardware"
	h.fake.poByID[po.ID] = po
	edit := func(lines string) *httptest.ResponseRecorder {
		return h.do(call{method: "PATCH", path: poPath(po.ID), duties: []string{dutyPurchaseOrderMaintain},
			body: directOrder(vendorA, lines), headers: map[string]string{"If-Match": `"5"`}})
	}
	keep := `{"lineNo":3,"description":"Freight box","qty":"3","unit":"EA","unitPrice":"4","deliveryDate":"2026-04-01"}`
	if rec := edit(keep); rec.Code != http.StatusOK || h.fake.lastEdit.next.Lines[0].Category != "IT hardware" {
		t.Fatalf("an existing line keeps its category unchecked: %d %s", rec.Code, rec.Body)
	}
	moved := strings.Replace(keep, `"qty":"3"`, `"category":"Office","qty":"3"`, 1)
	if rec := edit(moved); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "lines[0].category") {
		t.Fatalf("an existing line cannot change category: %d %s", rec.Code, rec.Body)
	}
	added := keep + `,{"description":"Dock","category":"IT hardware","qty":"1","unit":"EA","unitPrice":"80","deliveryDate":"2026-04-01"}`
	if rec := edit(added); rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != domain.CodeCategoryAccess ||
		!strings.Contains(rec.Body.String(), "purchase order line 4") {
		t.Fatalf("an added line in a restricted category: %d %s", rec.Code, rec.Body)
	}
}

// Under HARD a direct line must name a product or a category; SOFT and OFF keep the old
// description-only line.
func TestCreateOrder_HardNeedsACategoryOnALineWithoutAProduct(t *testing.T) {
	h := newHarness(t)
	_, _, stranger := accessFixture(h, domain.AccessHard)
	bare := `{"description":"Consulting","qty":"1","unit":"EA","unitPrice":"900","deliveryDate":"2026-03-10"}`
	product := `{"productId":"` + productA + `","qty":"1","unit":"EA","unitPrice":"900","deliveryDate":"2026-03-10"}`
	rec := h.do(call{method: "POST", path: "/v1/procurement/purchase-orders", user: userA, duties: []string{dutyPurchaseOrderMaintain},
		body: directOrder(stranger, product+","+bare), headers: idem("b1")})
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != codeInvalid || !strings.Contains(rec.Body.String(), "lines[1].category") {
		t.Fatalf("HARD, bare line: %d %s", rec.Code, rec.Body)
	}
	rec = h.do(call{method: "POST", path: "/v1/procurement/purchase-orders", user: userA, duties: []string{dutyPurchaseOrderMaintain},
		body: directOrder(stranger, product), headers: idem("b2")})
	if rec.Code != http.StatusCreated {
		t.Fatalf("HARD, product line without a category is not checked: %d %s", rec.Code, rec.Body)
	}
	h.fake.pol.saved.CategoryAccessMode = domain.AccessSoft
	rec = h.do(call{method: "POST", path: "/v1/procurement/purchase-orders", user: userA, duties: []string{dutyPurchaseOrderMaintain},
		body: directOrder(stranger, bare), headers: idem("b3")})
	if rec.Code != http.StatusCreated {
		t.Fatalf("SOFT, bare line: %d %s", rec.Code, rec.Body)
	}
}
