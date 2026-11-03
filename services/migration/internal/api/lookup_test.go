package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
)

const lkPath = "/v1/integration/lookup-tables"

const lkEnum = `{"kind":"enum","description":"Passage unit codes to AX","entries":{"kg":"KGM","ea":"EA"}}`

const lkOutboundDefinition = `{
  "eventType": "dev.passage.sales.order.*.v1",
  "subject": {"entity": "salesOrder", "idPath": "data.order_id"},
  "xrefs": [{"name": "customer", "entity": "customer", "idPath": "data.customer_id"}],
  "lookups": [{"name": "unit", "table": "unit-codes", "keyPaths": ["data.uom"]}],
  "mapping": {"entity": "SalesOrder", "fields": [
    {"source": "xref.customer", "target": "CustAccount", "type": "string", "required": true},
    {"source": "lookup.unit", "target": "Line.Unit", "type": "string", "required": true}
  ]}
}`

type lkBody struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Kind       string          `json:"kind"`
	Version    int             `json:"version"`
	Status     string          `json:"status"`
	EntryCount int             `json:"entryCount"`
	Definition json.RawMessage `json:"definition"`
}

func createLookup(t *testing.T, r http.Handler, name, def string) lkBody {
	t.Helper()
	rec := do(r, http.MethodPost, lkPath, `{"name":"`+name+`","definition":`+def+`}`, obWrite, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	var b lkBody
	_ = json.Unmarshal(rec.Body.Bytes(), &b)
	return b
}

func TestLookup_CRUDVersionsAndDuties(t *testing.T) {
	s := store.NewMem()
	r := obRouter(s, hubPrincipal(1))
	tbl := createLookup(t, r, "unit-codes", lkEnum)
	if tbl.Version != 1 || tbl.Kind != "enum" || tbl.Status != "active" || tbl.EntryCount != 2 {
		t.Fatalf("created = %+v", tbl)
	}

	// Idempotent create: the same key replays 200 with the same table; the same name under another key is 409.
	first := do(r, http.MethodPost, lkPath, `{"name":"x","definition":`+lkEnum+`}`, obWrite, true, "Idempotency-Key", "k1")
	again := do(r, http.MethodPost, lkPath, `{"name":"x","definition":`+lkEnum+`}`, obWrite, true, "Idempotency-Key", "k1")
	if first.Code != http.StatusCreated || again.Code != http.StatusOK || first.Body.String() != again.Body.String() {
		t.Fatalf("idempotent create = %d / %d", first.Code, again.Code)
	}
	if rec := do(r, http.MethodPost, lkPath, `{"name":"unit-codes","definition":`+lkEnum+`}`, obWrite, true); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "MIG-LOOKUP-NAME-EXISTS") {
		t.Fatalf("duplicate name = %d %s", rec.Code, rec.Body)
	}

	// Validation: name, kind, entries.
	for body, code := range map[string]string{
		`{"name":"1bad","definition":` + lkEnum + `}`: "COM-0001",
		`{"name":"ok"}`: "COM-0001",
		`{"name":"ok","definition":{"kind":"price_list","entries":{"sku":"-3"}}}`:         "MIG-LOOKUP-INVALID",
		`{"name":"ok","definition":{"kind":"uom_conversion","entries":{"KG|LB":"zero"}}}`: "MIG-LOOKUP-INVALID",
	} {
		rec := do(r, http.MethodPost, lkPath, body, obWrite, true)
		if rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), code) {
			t.Errorf("%s = %d %s", body, rec.Code, rec.Body)
		}
	}

	// Read needs migration.run, write needs migration.project.write.
	if rec := do(r, http.MethodGet, lkPath, "", "nothing.useful", true); rec.Code != http.StatusForbidden {
		t.Fatalf("list without duty = %d", rec.Code)
	}
	if rec := do(r, http.MethodPost, lkPath, `{"name":"y","definition":`+lkEnum+`}`, obRead, true); rec.Code != http.StatusForbidden {
		t.Fatalf("create with read duty = %d", rec.Code)
	}

	// Edit writes version 2 under the optimistic version; a stale version is 409; version 1 stays readable.
	patch := func(body string) *httptest.ResponseRecorder {
		return do(r, http.MethodPatch, lkPath+"/"+tbl.ID, body, obWrite, true)
	}
	rec := patch(`{"version":1,"definition":{"kind":"enum","entries":{"kg":"KGM","ea":"EA","box":"BX"}}}`)
	var v2 lkBody
	_ = json.Unmarshal(rec.Body.Bytes(), &v2)
	if rec.Code != http.StatusOK || v2.Version != 2 || v2.EntryCount != 3 {
		t.Fatalf("edit = %d %s", rec.Code, rec.Body)
	}
	if rec := patch(`{"version":1,"name":"renamed"}`); rec.Code != http.StatusConflict {
		t.Fatalf("stale edit = %d", rec.Code)
	}
	if rec := patch(`{"version":2,"definition":{"kind":"price_list","entries":{}}}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("kind change = %d %s", rec.Code, rec.Body)
	}
	if rec := patch(`{"name":"renamed"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("edit without version = %d", rec.Code)
	}
	old := do(r, http.MethodGet, lkPath+"/"+tbl.ID+"?version=1", "", obRead, true)
	var o lkBody
	_ = json.Unmarshal(old.Body.Bytes(), &o)
	if old.Code != http.StatusOK || o.Version != 1 || o.EntryCount != 2 {
		t.Fatalf("v1 = %d %s", old.Code, old.Body)
	}
	if rec := do(r, http.MethodGet, lkPath+"/"+tbl.ID+"?version=7", "", obRead, true); rec.Code != http.StatusNotFound {
		t.Fatalf("missing version = %d", rec.Code)
	}
	if rec := do(r, http.MethodGet, lkPath+"/"+tbl.ID+"?version=zero", "", obRead, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad version = %d", rec.Code)
	}
	vs := do(r, http.MethodGet, lkPath+"/"+tbl.ID+"/versions", "", obRead, true)
	if vs.Code != http.StatusOK || strings.Count(vs.Body.String(), `"checksum"`) != 2 {
		t.Fatalf("versions = %d %s", vs.Code, vs.Body)
	}

	// Archive, then only a reactivation is accepted; a rename after reactivation works.
	if rec := patch(`{"version":2,"status":"archived"}`); rec.Code != http.StatusOK {
		t.Fatalf("archive = %d %s", rec.Code, rec.Body)
	}
	if rec := patch(`{"version":3,"name":"renamed"}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "MIG-LOOKUP-ARCHIVED") {
		t.Fatalf("edit of archived = %d %s", rec.Code, rec.Body)
	}
	if rec := patch(`{"version":3,"status":"active","name":"renamed"}`); rec.Code != http.StatusOK {
		t.Fatalf("reactivate = %d %s", rec.Code, rec.Body)
	}

	// List with filters and a cursor.
	createLookup(t, r, "a-prices", `{"kind":"price_list","entries":{"SKU-1":"9.99"}}`)
	list := func(q string) (items []lkBody, next string) {
		rec := do(r, http.MethodGet, lkPath+q, "", obRead, true)
		if rec.Code != http.StatusOK {
			t.Fatalf("list %s = %d %s", q, rec.Code, rec.Body)
		}
		var out struct {
			Items      []lkBody `json:"items"`
			NextCursor string   `json:"nextCursor"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out.Items, out.NextCursor
	}
	if items, _ := list(""); len(items) != 3 || items[0].Name != "a-prices" || items[0].Definition != nil {
		t.Fatalf("list = %+v", items)
	}
	if items, _ := list("?kind=price_list"); len(items) != 1 {
		t.Fatalf("kind filter = %+v", items)
	}
	page, next := list("?limit=2")
	if len(page) != 2 || next == "" {
		t.Fatalf("page 1 = %+v %q", page, next)
	}
	if rest, _ := list("?limit=2&cursor=" + next); len(rest) != 1 {
		t.Fatalf("page 2 = %+v", rest)
	}
	for _, q := range []string{"?status=gone", "?kind=map", "?cursor=zz"} {
		if rec := do(r, http.MethodGet, lkPath+q, "", obRead, true); rec.Code != http.StatusBadRequest {
			t.Errorf("list %s = %d", q, rec.Code)
		}
	}
	if rec := do(r, http.MethodGet, lkPath+"/"+ids.New(), "", obRead, true); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id = %d", rec.Code)
	}
}

func TestLookup_RenderAndPreviewUseTheTable(t *testing.T) {
	s := store.NewMem()
	r := obRouter(s, hubPrincipal(1))
	rec := do(r, http.MethodPost, obPath, `{"name":"Orders","sourceSystem":"custom_acme","definition":`+lkOutboundDefinition+`}`, obWrite, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create mapping = %d %s", rec.Code, rec.Body)
	}
	var m obBody
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	cust, order := seedCustomer(t, s, "C-1001"), ids.New()
	event := func(uom string) string {
		return strings.Replace(hubEvent("evt-1", order, cust), `"number":"SO-9"`, `"number":"SO-9","uom":"`+uom+`"`, 1)
	}
	preview := func(uom string) map[string]any {
		rec := do(r, http.MethodPost, obPath+"/"+m.ID+":preview", `{"event":`+event(uom)+`}`, obRead, true)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}

	// No table yet: unresolved, naming the table and the key.
	v := preview("kg")
	un, _ := v["unresolved"].([]any)
	if v["status"] != "unresolved" || len(un) != 1 || un[0].(map[string]any)["entity"] != "lookup:unit-codes" || un[0].(map[string]any)["id"] != "kg" {
		t.Fatalf("no table = %+v", v)
	}
	if rec := hubCall(r, obPath+"/"+m.ID+":render", `{"event":`+event("kg")+`}`); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "MIG-XREF-UNRESOLVED") || !strings.Contains(rec.Body.String(), `"unit"`) {
		t.Fatalf("render without table = %d %s", rec.Code, rec.Body)
	}

	tbl := createLookup(t, r, "unit-codes", lkEnum)
	if v := preview("kg"); v["status"] != "ok" || v["body"].(map[string]any)["Line"].(map[string]any)["Unit"] != "KGM" {
		t.Fatalf("with table = %+v", v)
	}
	rec = hubCall(r, obPath+"/"+m.ID+":render", `{"event":`+event("ea")+`}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"Unit":"EA"`) || !strings.Contains(rec.Body.String(), `"lookups":[{"name":"unit","table":"unit-codes","version":1}]`) {
		t.Fatalf("render = %d %s", rec.Code, rec.Body)
	}
	if v := preview("box"); v["status"] != "unresolved" {
		t.Fatalf("unknown code = %+v", v)
	}

	// Editing the table changes the next render (the table is read live), archiving stops it resolving.
	if rec := do(r, http.MethodPatch, lkPath+"/"+tbl.ID, `{"version":1,"definition":{"kind":"enum","entries":{"box":"BX"}}}`, obWrite, true); rec.Code != http.StatusOK {
		t.Fatalf("edit = %d %s", rec.Code, rec.Body)
	}
	if v := preview("box"); v["status"] != "ok" || v["lookups"].([]any)[0].(map[string]any)["version"] != float64(2) {
		t.Fatalf("after edit = %+v", v)
	}
	if rec := do(r, http.MethodPatch, lkPath+"/"+tbl.ID, `{"version":2,"status":"archived"}`, obWrite, true); rec.Code != http.StatusOK {
		t.Fatalf("archive = %d", rec.Code)
	}
	if v := preview("box"); v["status"] != "unresolved" || !strings.Contains(v["detail"].(string), "archived") {
		t.Fatalf("archived = %+v", v)
	}

	// The hub token may render (it reads the table) but not manage tables.
	if rec := hubCall(r, lkPath, `{"name":"z","definition":`+lkEnum+`}`); rec.Code != http.StatusForbidden {
		t.Fatalf("hub create = %d", rec.Code)
	}
}

// The hub's render of an order with lines: one body carries every line, each with its own table value, the answer names
// the table read for the lines, and a line whose unit is not in the table is 422 naming that line.
func TestLookup_RenderMapsEveryOrderLine(t *testing.T) {
	s := store.NewMem()
	r := obRouter(s, hubPrincipal(1))
	def := strings.Replace(lkOutboundDefinition, `"lookups": [{"name": "unit", "table": "unit-codes", "keyPaths": ["data.uom"]}],`,
		`"collections": [{"name": "Lines", "path": "data.lines",
		  "lookups": [{"name": "unit", "table": "unit-codes", "keyPaths": ["uom"]}],
		  "fields": [{"source": "lookup.unit", "target": "Unit", "type": "string", "required": true}, {"source": "index", "target": "LineNo", "type": "int"}]}],`, 1)
	def = strings.Replace(def, `,
    {"source": "lookup.unit", "target": "Line.Unit", "type": "string", "required": true}`, ``, 1)
	rec := do(r, http.MethodPost, obPath, `{"name":"Orders","sourceSystem":"custom_acme","definition":`+def+`}`, obWrite, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create mapping = %d %s", rec.Code, rec.Body)
	}
	var m obBody
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	createLookup(t, r, "unit-codes", lkEnum)
	cust, order := seedCustomer(t, s, "C-1001"), ids.New()
	event := func(lines string) string {
		return strings.Replace(hubEvent("evt-1", order, cust), `"number":"SO-9"`, `"number":"SO-9","lines":`+lines, 1)
	}

	rec = hubCall(r, obPath+"/"+m.ID+":render", `{"event":`+event(`[{"uom":"kg"},{"uom":"ea"}]`)+`}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"Lines":[{"LineNo":0,"Unit":"KGM"},{"LineNo":1,"Unit":"EA"}]`) ||
		!strings.Contains(rec.Body.String(), `"lookups":[{"name":"Lines.unit","table":"unit-codes","version":1}]`) {
		t.Fatalf("render = %d %s", rec.Code, rec.Body)
	}
	rec = hubCall(r, obPath+"/"+m.ID+":render", `{"event":`+event(`[{"uom":"kg"},{"uom":"box"}]`)+`}`)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"Lines[1].unit"`) || strings.Contains(rec.Body.String(), `"Lines[0].unit"`) {
		t.Fatalf("unknown unit on line 1 = %d %s", rec.Code, rec.Body)
	}
	rec = hubCall(r, obPath+"/"+m.ID+":render", `{"event":`+event(`[]`)+`}`)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "MIG-OUTBOUND-REJECTED") || !strings.Contains(rec.Body.String(), "empty_array") {
		t.Fatalf("no lines = %d %s", rec.Code, rec.Body)
	}
}

func TestLookup_ImportCSVMergeReplaceAndExport(t *testing.T) {
	s := store.NewMem()
	r := obRouter(s, hubPrincipal(1))
	tbl := createLookup(t, r, "conv", `{"kind":"uom_conversion","entries":{"KG|LB":"2.2","KG|G":"1000"}}`)
	imp := func(body string, duties string) *httptest.ResponseRecorder {
		return do(r, http.MethodPost, lkPath+"/"+tbl.ID+":import", body, duties, true)
	}
	type resp struct {
		Table   lkBody `json:"table"`
		Written bool   `json:"written"`
		Summary struct{ Rows, Added, Changed, Unchanged, Removed int }
	}
	read := func(rec *httptest.ResponseRecorder) resp {
		var out resp
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}

	// Merge: LB|KG is new, KG|LB changes, KG|G stays because the file does not mention it.
	rec := imp(`{"version":1,"mode":"merge","hasHeader":true,"csv":"from,to,factor\nLB,KG,0.45359237\nKG,LB,2.20462262185\n"}`, obWrite)
	got := read(rec)
	if rec.Code != http.StatusOK || !got.Written || got.Table.Version != 2 || got.Table.EntryCount != 3 || got.Summary.Added != 1 || got.Summary.Changed != 1 || got.Summary.Removed != 0 {
		t.Fatalf("merge = %d %s", rec.Code, rec.Body)
	}

	// The same file again changes nothing: no version is written, and a stale version is still a conflict.
	rec = imp(`{"version":2,"mode":"merge","hasHeader":true,"csv":"from,to,factor\nLB,KG,0.45359237\n"}`, obWrite)
	got = read(rec)
	if rec.Code != http.StatusOK || got.Written || got.Table.Version != 2 || got.Summary.Unchanged != 1 {
		t.Fatalf("no-op import = %d %s", rec.Code, rec.Body)
	}
	if rec := imp(`{"version":1,"mode":"merge","csv":"LB,KG,0.45\n"}`, obWrite); rec.Code != http.StatusConflict {
		t.Fatalf("stale import = %d %s", rec.Code, rec.Body)
	}
	if rec := imp(`{"version":1,"mode":"merge","csv":"KG,LB,2.2\n"}`, obWrite); rec.Code != http.StatusConflict {
		t.Fatalf("stale no-op import = %d %s", rec.Code, rec.Body)
	}

	// Replace keeps only the file's entries.
	rec = imp(`{"version":2,"mode":"replace","csv":"KG,LB,2.2\n"}`, obWrite)
	got = read(rec)
	if rec.Code != http.StatusOK || got.Table.Version != 3 || got.Table.EntryCount != 1 || got.Summary.Removed != 2 || got.Summary.Changed != 1 {
		t.Fatalf("replace = %d %s", rec.Code, rec.Body)
	}

	// Problems name their lines; nothing is written; the duty and the request are checked.
	rec = imp(`{"version":3,"mode":"merge","csv":"KG,LB,0\nKG,KG,1\n"}`, obWrite)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "MIG-LOOKUP-INVALID") || !strings.Contains(rec.Body.String(), "line 2") {
		t.Fatalf("invalid csv = %d %s", rec.Code, rec.Body)
	}
	if rec := imp(`{"version":3,"mode":"merge","csv":"KG,LB,2\n"}`, obRead); rec.Code != http.StatusForbidden {
		t.Fatalf("import with read duty = %d", rec.Code)
	}
	for _, body := range []string{`{"mode":"merge","csv":"a,b"}`, `{"version":3,"mode":"append","csv":"a,b"}`, `{"version":3,"mode":"merge","csv":" "}`} {
		if rec := imp(body, obWrite); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d", body, rec.Code)
		}
	}
	if rec := do(r, http.MethodPost, lkPath+"/"+ids.New()+":import", `{"version":1,"mode":"merge","csv":"KG,LB,2"}`, obWrite, true); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown table = %d", rec.Code)
	}

	// Archived tables are not imported into.
	arch := do(r, http.MethodPatch, lkPath+"/"+tbl.ID, `{"version":3,"status":"archived"}`, obWrite, true)
	if arch.Code != http.StatusOK {
		t.Fatalf("archive = %d", arch.Code)
	}
	if rec := imp(`{"version":4,"mode":"merge","csv":"KG,G,1000\n"}`, obWrite); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "MIG-LOOKUP-ARCHIVED") {
		t.Fatalf("import into archived = %d %s", rec.Code, rec.Body)
	}

	// Export: current version, an older one, headers, and the duty.
	ex := do(r, http.MethodGet, lkPath+"/"+tbl.ID+"/entries.csv", "", obRead, true)
	if ex.Code != http.StatusOK || ex.Body.String() != "KG,LB,2.2\n" || !strings.HasPrefix(ex.Header().Get("Content-Type"), "text/csv") || !strings.Contains(ex.Header().Get("Content-Disposition"), `conv-v4.csv`) {
		t.Fatalf("export = %d %q %v", ex.Code, ex.Body, ex.Header())
	}
	if old := do(r, http.MethodGet, lkPath+"/"+tbl.ID+"/entries.csv?version=1", "", obRead, true); old.Code != http.StatusOK || old.Body.String() != "KG,G,1000\nKG,LB,2.2\n" {
		t.Fatalf("export v1 = %d %q", old.Code, old.Body)
	}
	for path, want := range map[string]int{
		lkPath + "/" + tbl.ID + "/entries.csv?version=x": http.StatusBadRequest,
		lkPath + "/" + tbl.ID + "/entries.csv?version=9": http.StatusNotFound,
		lkPath + "/" + ids.New() + "/entries.csv":        http.StatusNotFound,
	} {
		if rec := do(r, http.MethodGet, path, "", obRead, true); rec.Code != want {
			t.Errorf("%s = %d, want %d", path, rec.Code, want)
		}
	}
	if rec := do(r, http.MethodGet, lkPath+"/"+tbl.ID+"/entries.csv", "", "nothing.useful", true); rec.Code != http.StatusForbidden {
		t.Fatalf("export without duty = %d", rec.Code)
	}
}
