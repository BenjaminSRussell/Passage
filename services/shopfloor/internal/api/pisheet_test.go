package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

func (f *fakeStore) CreatePISheet(context.Context, string, string, domain.RecipeDoc, string) (store.PISheet, error) {
	return store.PISheet{ID: edgeID, Status: "draft", Rev: 1}, nil
}
func (f *fakeStore) ListPISheets(context.Context, store.PISheetFilter) ([]store.PISheet, error) {
	return []store.PISheet{}, nil
}
func (f *fakeStore) GetPISheet(context.Context, string) (store.PISheet, error) {
	return store.PISheet{ID: edgeID, Doc: json.RawMessage(`{"procedure":{"name":"B","unitProcedures":[]}}`)}, nil
}
func (f *fakeStore) UpdatePISheet(context.Context, string, int, string, domain.RecipeDoc, string) (store.PISheet, error) {
	return store.PISheet{ID: edgeID, Rev: 2}, nil
}
func (f *fakeStore) ReleasePISheet(context.Context, string, int, string) (store.PISheet, error) {
	return store.PISheet{ID: edgeID, Status: "released"}, nil
}
func (f *fakeStore) NewPISheetVersion(context.Context, string, string) (store.PISheet, error) {
	return store.PISheet{ID: edgeID, Status: "draft", Version: 2}, nil
}
func (f *fakeStore) ObsoletePISheet(context.Context, string, int, string) (store.PISheet, error) {
	return store.PISheet{ID: edgeID, Status: "obsolete"}, nil
}
func (f *fakeStore) CreateControlRecipeFromPISheet(context.Context, string, store.ControlRecipeInput, string, store.Idem) (store.ControlRecipe, error) {
	return store.ControlRecipe{ID: edgeID, Status: "sent"}, nil
}

func sheetBody(code string, value float64) string {
	return fmt.Sprintf(`{"code":%q,"name":"Reactor charge","recipe":{"procedure":{"name":"Batch","unitProcedures":[{"name":"Reactor","operations":[{"name":"Heat","phases":[{"name":"Ramp","instructions":[
		{"seq":1,"category":"instruction-text","text":"Check the vessel is empty."},
		{"seq":2,"category":"process-data-request","text":"Record the vessel temperature.","characteristic":{"name":"Temperature","type":"numeric","unit":"degC","min":10,"max":30}},
		{"seq":3,"category":"process-parameter-setpoint","text":"Set the jacket.","value":%v,"characteristic":{"name":"Jacket","type":"numeric","unit":"degC","min":60,"max":90}}]}]}]}]}}}`, code, value)
}

// The designer's routes need their own duty, validate the document like a recipe, and refuse a bad code.
func TestPISheetRoutes_DutiesAndValidation(t *testing.T) {
	h := newRouter(&fakeStore{})
	path := "/v1/shopfloor/pi-sheets"
	if rec := call(t, h, http.MethodPost, path, sheetBody("CHARGE-1", 72), DutyView); rec.Code != http.StatusForbidden {
		t.Fatalf("create without the maintain duty: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodPost, path, sheetBody("CHARGE-1", 72), DutyPISheetMaintain); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	for name, tc := range map[string]struct{ body, field string }{
		"lower-case code":         {sheetBody("charge-1", 72), "code"},
		"setpoint over its limit": {sheetBody("CHARGE-2", 99), "value"},
		"no name":                 {strings.Replace(sheetBody("CHARGE-3", 72), `"name":"Reactor charge"`, `"name":""`, 1), "name"},
	} {
		rec := call(t, h, http.MethodPost, path, tc.body, DutyPISheetMaintain)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.field) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := call(t, h, http.MethodGet, path+"?status=nope", "", DutyView); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad status filter: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodGet, path+"?status=released", "", DutyView); rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	one := path + "/" + edgeID
	if rec := call(t, h, http.MethodGet, one, "", DutyView); rec.Code != http.StatusOK {
		t.Fatalf("get: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodGet, path+"/nope", "", DutyView); rec.Code != http.StatusNotFound {
		t.Fatalf("get of a bad id: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodPut, one, strings.Replace(sheetBody("X", 72), `"code":"X",`, `"rev":1,`, 1), DutyPISheetMaintain); rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, h, http.MethodPut, one, strings.Replace(sheetBody("X", 72), `"code":"X",`, ``, 1), DutyPISheetMaintain); rec.Code != http.StatusBadRequest {
		t.Fatalf("update without rev: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodPost, one+":release", `{}`, DutyPISheetMaintain); rec.Code != http.StatusBadRequest {
		t.Fatalf("release without rev: %d %s", rec.Code, rec.Body)
	}
	for _, op := range []string{"new-version"} {
		if rec := call(t, h, http.MethodPost, one+":"+op, ``, DutyView); rec.Code != http.StatusForbidden {
			t.Fatalf("%s with the view duty: %d", op, rec.Code)
		}
		if rec := call(t, h, http.MethodPost, one+":"+op, ``, DutyPISheetMaintain); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", op, rec.Code, rec.Body)
		}
	}
	from := "/v1/shopfloor/control-recipes:from-pi-sheet"
	body := fmt.Sprintf(`{"piSheetId":%q,"orderId":%q,"resourceId":%q}`, edgeID, recipeOrder, recipeResource)
	if rec := call(t, h, http.MethodPost, from, body, DutyPISheetMaintain); rec.Code != http.StatusForbidden {
		t.Fatalf("from-pi-sheet with the maintain duty: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodPost, from, body, DutyControlRecipeCreate); rec.Code != http.StatusCreated {
		t.Fatalf("from-pi-sheet: %d %s", rec.Code, rec.Body)
	}
	bad := fmt.Sprintf(`{"piSheetId":"x","orderId":%q,"resourceId":%q,"destination":"pcs-opcua"}`, recipeOrder, recipeResource)
	rec := call(t, h, http.MethodPost, from, bad, DutyControlRecipeCreate)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "piSheetId") || !strings.Contains(rec.Body.String(), "edgeId") {
		t.Fatalf("bad from-pi-sheet: %d %s", rec.Code, rec.Body)
	}
}

// The designer end to end on a real database: draft, edit, stale edit, release, no edit of a released sheet, a new
// version, only one released version at a time, and recipes made from released sheets only.
func TestPISheetLifecycleOnADatabase(t *testing.T) {
	e := newRecipeEnv(t)
	const duties = DutyPISheetMaintain + "," + DutyView + "," + DutyControlRecipeCreate
	send := func(method, path, body string) (*httptest.ResponseRecorder, store.PISheet) {
		rec := e.send(method, path, body, duties, "designer-1")
		var sh store.PISheet
		_ = json.Unmarshal(rec.Body.Bytes(), &sh)
		return rec, sh
	}
	rec, v1 := send(http.MethodPost, "/v1/shopfloor/pi-sheets", sheetBody("CHARGE-1", 72))
	if rec.Code != http.StatusCreated || v1.Version != 1 || v1.Status != "draft" || v1.Rev != 1 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if rec, _ := send(http.MethodPost, "/v1/shopfloor/pi-sheets", sheetBody("CHARGE-1", 72)); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), store.CodePISheetCode) {
		t.Fatalf("a second sheet with the code: %d %s", rec.Code, rec.Body)
	}
	one := "/v1/shopfloor/pi-sheets/" + v1.ID
	edit := strings.Replace(sheetBody("X", 75), `"code":"X",`, `"rev":1,`, 1)
	rec, v1 = send(http.MethodPut, one, edit)
	if rec.Code != http.StatusOK || v1.Rev != 2 || !strings.Contains(string(v1.Doc), "75") {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body)
	}
	if rec, _ := send(http.MethodPut, one, edit); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), store.CodePISheetRev) {
		t.Fatalf("a stale edit: %d %s", rec.Code, rec.Body)
	}
	// A draft makes no recipe.
	from := func(id string) *httptest.ResponseRecorder {
		return e.send(http.MethodPost, "/v1/shopfloor/control-recipes:from-pi-sheet", fmt.Sprintf(`{"piSheetId":%q,"orderId":%q,"resourceId":%q}`, id, recipeOrder, recipeResource), duties, "planner-1")
	}
	if rec := from(v1.ID); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), store.CodePISheetUnusable) {
		t.Fatalf("recipe from a draft: %d %s", rec.Code, rec.Body)
	}
	if rec, _ := send(http.MethodPost, one+":release", `{"rev":1}`); rec.Code != http.StatusConflict {
		t.Fatalf("release with a stale rev: %d %s", rec.Code, rec.Body)
	}
	rec, v1 = send(http.MethodPost, one+":release", `{"rev":2}`)
	if rec.Code != http.StatusOK || v1.Status != "released" || v1.ReleasedBy == nil || *v1.ReleasedBy != "designer-1" {
		t.Fatalf("release: %d %s", rec.Code, rec.Body)
	}
	if rec, _ := send(http.MethodPut, one, strings.Replace(edit, `"rev":1`, fmt.Sprintf(`"rev":%d`, v1.Rev), 1)); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), store.CodePISheetState) {
		t.Fatalf("edit of a released sheet: %d %s", rec.Code, rec.Body)
	}
	// A recipe made from the released sheet carries its document and its id.
	rec = from(v1.ID)
	var made store.ControlRecipe
	_ = json.Unmarshal(rec.Body.Bytes(), &made)
	if rec.Code != http.StatusCreated || made.Destination != "kiosk" || !strings.Contains(string(made.Recipe), "Check the vessel is empty.") {
		t.Fatalf("recipe from a released sheet: %d %s", rec.Code, rec.Body)
	}
	var linked string
	e.db(`select pi_sheet_id::text from control_recipe where id = '`+made.ID+`'`, &linked)
	if linked != v1.ID {
		t.Fatalf("the recipe remembers sheet %q, want %q", linked, v1.ID)
	}
	// A sheet with setpoints has no node: a process control recipe from it is refused, naming why.
	pcs := e.send(http.MethodPost, "/v1/shopfloor/control-recipes:from-pi-sheet", fmt.Sprintf(`{"piSheetId":%q,"orderId":%q,"resourceId":%q,"destination":"pcs-opcua","edgeId":%q}`, v1.ID, recipeOrder, recipeResource, e.node.ID), duties, "planner-1")
	if pcs.Code != http.StatusUnprocessableEntity || !strings.Contains(pcs.Body.String(), "node") {
		t.Fatalf("pcs recipe from a sheet with no nodes: %d %s", pcs.Code, pcs.Body)
	}

	// A new version is a draft copy; one draft per code; releasing it retires the earlier released version.
	rec, v2 := send(http.MethodPost, one+":new-version", "")
	if rec.Code != http.StatusOK || v2.Version != 2 || v2.Status != "draft" || v2.Code != "CHARGE-1" {
		t.Fatalf("new version: %d %s", rec.Code, rec.Body)
	}
	if rec, _ := send(http.MethodPost, one+":new-version", ""); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), store.CodePISheetCode) {
		t.Fatalf("a second draft: %d %s", rec.Code, rec.Body)
	}
	if rec, _ := send(http.MethodPost, "/v1/shopfloor/pi-sheets/"+v2.ID+":release", fmt.Sprintf(`{"rev":%d}`, v2.Rev)); rec.Code != http.StatusOK {
		t.Fatalf("release of version 2: %d %s", rec.Code, rec.Body)
	}
	_, old := send(http.MethodGet, one, "")
	if old.Status != "obsolete" {
		t.Fatalf("version 1 after version 2 was released: %s", old.Status)
	}
	if rec := from(old.ID); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("recipe from an obsolete sheet: %d", rec.Code)
	}
	// Obsolete retires a released sheet; the list filters by status and code.
	_, cur := send(http.MethodGet, "/v1/shopfloor/pi-sheets/"+v2.ID, "")
	if rec, got := send(http.MethodPost, "/v1/shopfloor/pi-sheets/"+v2.ID+":obsolete", fmt.Sprintf(`{"rev":%d}`, cur.Rev)); rec.Code != http.StatusOK || got.Status != "obsolete" {
		t.Fatalf("obsolete: %d %s", rec.Code, rec.Body)
	}
	var list struct{ Items []store.PISheet }
	lr := e.send(http.MethodGet, "/v1/shopfloor/pi-sheets?code=CHARGE-1&status=obsolete", "", duties, "designer-1")
	_ = json.Unmarshal(lr.Body.Bytes(), &list)
	if len(list.Items) != 2 || list.Items[0].Version != 2 {
		t.Fatalf("list: %s", lr.Body)
	}
}

// The sheet is read under a share lock in the transaction that makes the recipe: a recipe asked for while the
// sheet is being made obsolete waits for that change, then sees it and makes nothing.
func TestRecipeFromPISheetReadsTheSheetInItsTransaction(t *testing.T) {
	e := newRecipeEnv(t)
	const duties = DutyPISheetMaintain + "," + DutyView + "," + DutyControlRecipeCreate
	rec := e.send(http.MethodPost, "/v1/shopfloor/pi-sheets", sheetBody("LOCK-1", 72), duties, "designer-1")
	var sh store.PISheet
	_ = json.Unmarshal(rec.Body.Bytes(), &sh)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if rec := e.send(http.MethodPost, "/v1/shopfloor/pi-sheets/"+sh.ID+":release", `{"rev":1}`, duties, "designer-1"); rec.Code != http.StatusOK {
		t.Fatalf("release: %d %s", rec.Code, rec.Body)
	}
	ctx := context.Background()
	tx, err := e.admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `update pi_sheet set status = 'obsolete', rev = rev + 1 where id = $1`, sh.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- e.send(http.MethodPost, "/v1/shopfloor/control-recipes:from-pi-sheet", fmt.Sprintf(`{"piSheetId":%q,"orderId":%q,"resourceId":%q}`, sh.ID, recipeOrder, recipeResource), duties, "planner-1")
	}()
	select {
	case rec := <-done:
		t.Fatalf("the recipe did not wait for the sheet being changed: %d %s", rec.Code, rec.Body)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if rec := <-done; rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), store.CodePISheetUnusable) {
		t.Fatalf("a recipe from a sheet made obsolete meanwhile: %d %s", rec.Code, rec.Body)
	}
	var n int
	e.db(`select count(*) from control_recipe where pi_sheet_id = '`+sh.ID+`'`, &n)
	if n != 0 {
		t.Fatalf("%d recipes made from the obsolete sheet", n)
	}
}
