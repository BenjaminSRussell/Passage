package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// importLog records what the vendor import methods received.
type importLog struct {
	catalogID string
	staged    store.ImportInput
	decisions store.ImportDecisions
	ifMatch   int
	action    string
	rowQuery  [3]any
	filter    store.ImportFilter
	err       error
}

func (f *fakeStore) StageVendorImport(_ context.Context, catalogID string, in store.ImportInput, _ store.Idem) (domain.CatalogImport, error) {
	f.imp.catalogID, f.imp.staged = catalogID, in
	return domain.CatalogImport{ID: ids.New(), CatalogID: catalogID, Status: domain.ImportStaged, TotalRows: len(in.Rows), Version: 1}, f.imp.err
}

func (f *fakeStore) GetCatalogImport(context.Context, string) (domain.CatalogImport, error) {
	return domain.CatalogImport{Status: domain.ImportStaged, Version: 2}, f.imp.err
}

func (f *fakeStore) ListCatalogImports(_ context.Context, fl store.ImportFilter, _ string, _ int) (store.Page[domain.CatalogImport], error) {
	f.imp.filter = fl
	return store.Page[domain.CatalogImport]{Items: []domain.CatalogImport{}}, f.imp.err
}

func (f *fakeStore) ListImportRows(_ context.Context, _, status string, after, limit int) (store.ImportRowPage, error) {
	f.imp.rowQuery = [3]any{status, after, limit}
	return store.ImportRowPage{Items: []domain.ImportRow{{RowNo: 5, Status: domain.RowError}}, Next: 5}, f.imp.err
}

func (f *fakeStore) DecideImportRows(_ context.Context, _ string, ifMatch int, d store.ImportDecisions, _ store.Idem) (domain.CatalogImport, error) {
	f.imp.ifMatch, f.imp.decisions = ifMatch, d
	return domain.CatalogImport{Version: ifMatch + 1}, f.imp.err
}

func (f *fakeStore) ActOnCatalogImport(_ context.Context, _ string, ifMatch int, action string, _ store.Idem) (domain.CatalogImport, error) {
	f.imp.ifMatch, f.imp.action = ifMatch, action
	return domain.CatalogImport{Version: ifMatch + 1}, f.imp.err
}

func (f *fakeStore) ListCatalogEntries(_ context.Context, id string, after, limit int) (store.CatalogEntryPage, error) {
	f.imp.catalogID, f.imp.rowQuery = id, [3]any{"", after, limit}
	return store.CatalogEntryPage{Items: []domain.CatalogEntry{{EntryNo: 201}}, Next: 201}, f.imp.err
}

const impVendor = "00000000-0000-7000-8000-0000000000a1"

func importBody(csv string) string {
	return `{"vendorId":"` + impVendor + `","format":"CSV","fileName":"gloves.csv","csv":` + jsonString(csv) + `}`
}

func jsonString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s) + `"`
}

// AC-17.13: of a 5-row file, rows lacking a unit stage as ERROR with the reason, the rest ACCEPTABLE.
func TestVendorImport_CSVStagesAcceptableAndErrorRows(t *testing.T) {
	h := newHarness(t)
	id := ids.New()
	csv := "Vendor part number,Name,Category,Unit,Unit price,Currency,Lead time days\n" +
		"G-1,Gloves,MRO,PR,4.25,USD,3\n" +
		"G-2,Goggles,MRO,,9.00,USD,3\n" +
		"G-3,Tape,MRO,RL,abc,USD,1\n" +
		"G-4,Wrap,PACKING,RL,7,usd,1\n" +
		"G-5,Oil,MRO,L,12,USD,two\n"
	rec := h.do(call{method: "POST", path: catPath + "/" + id + "/vendor-import", body: importBody(csv), duties: []string{dutyCatalogMaintain}, headers: idem("i1")})
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	in := h.fake.imp.staged
	if h.fake.imp.catalogID != id || in.VendorID != impVendor || in.Format != "CSV" || in.FileName != "gloves.csv" || len(in.Rows) != 5 {
		t.Fatalf("staged %+v", in)
	}
	want := []string{domain.RowAcceptable, domain.RowError, domain.RowError, domain.RowError, domain.RowError}
	for i, r := range in.Rows {
		if r.RowNo != i+1 || r.Status != want[i] {
			t.Errorf("row %d = %s (%s), want %s", r.RowNo, r.Status, r.Reason, want[i])
		}
	}
	if e := in.Rows[0].Entry; e == nil || e.UnitPrice.String() != "4.25" || e.VendorID != impVendor || e.LeadTimeDays != 3 || e.VendorPartNo != "G-1" {
		t.Errorf("row 1 entry = %+v", e)
	}
	for i, frag := range map[int]string{1: "unit is required", 2: "unitPrice must be a decimal", 3: "currency must be a three-letter", 4: "leadTimeDays must be a whole number"} {
		if !strings.Contains(in.Rows[i].Reason, frag) || strings.Contains(in.Rows[i].Reason, "row[0]") {
			t.Errorf("row %d reason %q lacks %q", i+1, in.Rows[i].Reason, frag)
		}
	}
	if in.Rows[1].Raw[domain.ColName] != "Goggles" {
		t.Errorf("raw kept = %v", in.Rows[1].Raw)
	}
}

func TestVendorImport_JSONRows(t *testing.T) {
	h := newHarness(t)
	body := `{"vendorId":"` + impVendor + `","format":"JSON","rows":[` + strings.Replace(catEntry, `"vendorId":"00000000-0000-7000-8000-0000000000a1",`, "", 1) + `]}`
	rec := h.do(call{method: "POST", path: catPath + "/" + ids.New() + "/vendor-import", body: body, duties: []string{dutyCatalogMaintain}, headers: idem("i2")})
	rows := h.fake.imp.staged.Rows
	if rec.Code != http.StatusCreated || len(rows) != 1 || rows[0].Status != domain.RowAcceptable || rows[0].Entry.LeadTimeDays != 3 {
		t.Fatalf("status %d rows %+v: %s", rec.Code, rows, rec.Body)
	}
}

func TestVendorImport_Refusals(t *testing.T) {
	h := newHarness(t)
	id := ids.New()
	path := catPath + "/" + id + "/vendor-import"
	ok := "name,category,unit,price,currency\na,b,EA,1,USD\n"
	for _, c := range []struct {
		name, body, code string
		duties           []string
		headers          map[string]string
		status           int
	}{
		{"no duty", importBody(ok), "", []string{dutyPurchaseOrderInquire}, idem("k"), http.StatusForbidden},
		{"no key", importBody(ok), codeIdemRequired, []string{dutyCatalogMaintain}, nil, http.StatusBadRequest},
		{"no vendor", `{"format":"CSV","csv":"x"}`, codeInvalid, []string{dutyCatalogMaintain}, idem("k"), http.StatusUnprocessableEntity},
		{"bad format", `{"vendorId":"` + impVendor + `","format":"XML"}`, codeInvalid, []string{dutyCatalogMaintain}, idem("k"), http.StatusUnprocessableEntity},
		{"csv and rows", `{"vendorId":"` + impVendor + `","format":"CSV","csv":"x","rows":[` + catEntry + `]}`, codeInvalid, []string{dutyCatalogMaintain}, idem("k"), http.StatusUnprocessableEntity},
		{"missing column", importBody("name,category,price,currency\na,b,1,USD\n"), domain.CodeImportRow, []string{dutyCatalogMaintain}, idem("k"), http.StatusUnprocessableEntity},
		{"unknown field", `{"vendorId":"` + impVendor + `","format":"CSV","csv":"x","extra":1}`, "COM-0001", []string{dutyCatalogMaintain}, idem("k"), http.StatusBadRequest},
	} {
		rec := h.do(call{method: "POST", path: path, body: c.body, duties: c.duties, headers: c.headers})
		if rec.Code != c.status || (c.code != "" && problemCode(t, rec) != c.code) {
			t.Errorf("%s: status %d code %q: %s", c.name, rec.Code, problemCode(t, rec), rec.Body)
		}
	}
	if h.fake.imp.staged.VendorID != "" {
		t.Error("a refused import reached the store")
	}
}

func TestCatalogImport_DecisionsActionsAndReads(t *testing.T) {
	h := newHarness(t)
	id := ids.New()
	hdr := map[string]string{"Idempotency-Key": "k", "If-Match": `"2"`}
	rec := h.do(call{method: "POST", path: "/v1/procurement/catalog-imports/" + id + "/decisions", body: `{"acceptAll":true,"reject":[4,9]}`,
		duties: []string{dutyCatalogMaintain}, headers: hdr})
	if rec.Code != http.StatusOK || !h.fake.imp.decisions.AcceptAll || len(h.fake.imp.decisions.Reject) != 2 || h.fake.imp.ifMatch != 2 || rec.Header().Get("ETag") != `"3"` {
		t.Errorf("decisions: %d %+v", rec.Code, h.fake.imp)
	}
	for name, body := range map[string]string{"empty": `{}`, "row zero": `{"accept":[0]}`, "both lists": `{"accept":[3],"reject":[3]}`} {
		rec := h.do(call{method: "POST", path: "/v1/procurement/catalog-imports/" + id + "/decisions", body: body, duties: []string{dutyCatalogMaintain}, headers: hdr})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	for _, action := range []string{"commit", "cancel"} {
		rec := h.do(call{method: "POST", path: "/v1/procurement/catalog-imports/" + id + ":" + action, duties: []string{dutyCatalogMaintain}, headers: hdr})
		if rec.Code != http.StatusOK || h.fake.imp.action != action {
			t.Errorf("%s: %d %+v", action, rec.Code, h.fake.imp)
		}
	}
	if rec := h.do(call{method: "POST", path: "/v1/procurement/catalog-imports/" + id + ":purge", duties: []string{dutyCatalogMaintain}, headers: hdr}); rec.Code != http.StatusNotFound {
		t.Errorf("unknown action: %d", rec.Code)
	}
	if rec := h.do(call{method: "POST", path: "/v1/procurement/catalog-imports/" + id + ":commit", duties: []string{dutyCatalogMaintain}, headers: idem("k")}); rec.Code != http.StatusPreconditionRequired {
		t.Errorf("missing If-Match: %d", rec.Code)
	}
	rec = h.do(call{method: "GET", path: "/v1/procurement/catalog-imports/" + id + "/rows?status=ERROR&cursor=2&limit=10", duties: []string{dutyPurchaseOrderInquire}})
	if rec.Code != http.StatusOK || h.fake.imp.rowQuery != [3]any{"ERROR", 2, 10} || !strings.Contains(rec.Body.String(), `"nextCursor":"5"`) {
		t.Errorf("rows: %d %v %s", rec.Code, h.fake.imp.rowQuery, rec.Body)
	}
	if rec := h.do(call{method: "GET", path: "/v1/procurement/catalog-imports/" + id + "/rows?status=NOPE", duties: []string{dutyCatalogMaintain}}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("bad row status: %d", rec.Code)
	}
	if rec := h.do(call{method: "GET", path: "/v1/procurement/catalog-imports?catalogId=" + id + "&status=STAGED", duties: []string{dutyCatalogMaintain}}); rec.Code != http.StatusOK || h.fake.imp.filter.CatalogID != id {
		t.Errorf("list: %d %+v", rec.Code, h.fake.imp.filter)
	}
	if rec := h.do(call{method: "GET", path: "/v1/procurement/catalog-imports/" + id, duties: []string{dutyRequisitionCreate}}); rec.Code != http.StatusForbidden {
		t.Errorf("a requester must not read imports: %d", rec.Code)
	}
}

// A large catalog is read a page at a time: the entry-number cursor and the limit
// reach the store, and a bad cursor or limit is refused.
func TestCatalogEntries_Paged(t *testing.T) {
	h := newHarness(t)
	id := ids.New()
	rec := h.do(call{method: "GET", path: catPath + "/" + id + "/entries?cursor=200&limit=100", duties: []string{dutyPurchaseOrderInquire}})
	if rec.Code != http.StatusOK || h.fake.imp.catalogID != id || h.fake.imp.rowQuery != [3]any{"", 200, 100} ||
		!strings.Contains(rec.Body.String(), `"nextCursor":"201"`) {
		t.Fatalf("entries: %d %v %s", rec.Code, h.fake.imp.rowQuery, rec.Body)
	}
	for _, q := range []string{"?cursor=x", "?cursor=-1", "?limit=0", "?limit=201"} {
		if rec := h.do(call{method: "GET", path: catPath + "/" + id + "/entries" + q, duties: []string{dutyCatalogMaintain}}); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}
	if rec := h.do(call{method: "GET", path: catPath + "/" + id + "/entries", duties: []string{dutyRequisitionCreate}}); rec.Code != http.StatusForbidden {
		t.Errorf("a requester reads catalog entries outside the picker: %d", rec.Code)
	}
}

// A vendor price the numeric(20,6) column cannot hold is a row ERROR, not a failed file.
func TestVendorImport_PriceTooLargeIsARowError(t *testing.T) {
	h := newHarness(t)
	csv := "name,category,unit,price,currency\nBig,MRO,EA,100000000000000,USD\nOk,MRO,EA,99999999999999.999999,USD\n"
	rec := h.do(call{method: "POST", path: catPath + "/" + ids.New() + "/vendor-import", body: importBody(csv), duties: []string{dutyCatalogMaintain}, headers: idem("p1")})
	rows := h.fake.imp.staged.Rows
	if rec.Code != http.StatusCreated || len(rows) != 2 || rows[0].Status != domain.RowError || !strings.Contains(rows[0].Reason, "unitPrice must be below") ||
		rows[1].Status != domain.RowAcceptable {
		t.Fatalf("status %d rows %+v", rec.Code, rows)
	}
}

// A vendor file of 50,000 rows fits the request body limit as CSV and as JSON rows;
// a body over it is 413.
func TestVendorImport_BodyLimit(t *testing.T) {
	var csv strings.Builder
	csv.WriteString("Vendor part number,Name,Description,Category,Unit,Unit price,Currency,Lead time days,Image URL\n")
	var rows strings.Builder
	for i := 0; i < domain.MaxImportRows; i++ {
		fmt.Fprintf(&csv, "VP-%06d,Nitrile gloves size L box of 100,Powder free nitrile examination gloves,MRO,BX,12.345678,USD,14,https://img.example.com/catalog/vp-%06d.jpg\n", i, i)
		if i > 0 {
			rows.WriteString(",")
		}
		fmt.Fprintf(&rows, `{"vendorPartNo":"VP-%06d","name":"Nitrile gloves size L box of 100","description":"Powder free nitrile examination gloves","category":"MRO","unit":"BX","unitPrice":"12.345678","currency":"USD","leadTimeDays":14,"imageUrl":"https://img.example.com/catalog/vp-%06d.jpg"}`, i, i)
	}
	asCSV, asJSON := importBody(csv.String()), `{"vendorId":"`+impVendor+`","format":"JSON","rows":[`+rows.String()+`]}`
	if len(asCSV) > maxImportBody || len(asJSON) > maxImportBody {
		t.Fatalf("a 50,000-row file is %d bytes as CSV and %d as JSON, over the %d limit", len(asCSV), len(asJSON), maxImportBody)
	}
	h := newHarness(t)
	big := `{"vendorId":"` + impVendor + `","format":"CSV","csv":"` + strings.Repeat("x", maxImportBody) + `"}`
	if rec := h.do(call{method: "POST", path: catPath + "/" + ids.New() + "/vendor-import", body: big, duties: []string{dutyCatalogMaintain}, headers: idem("b1")}); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("an oversized body: %d", rec.Code)
	}
}
