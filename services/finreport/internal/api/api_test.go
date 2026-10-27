package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/store"
)

const (
	runUUID    = "11111111-1111-1111-1111-111111111111"
	ledgerUUID = "22222222-2222-2222-2222-222222222222"
)

// fakeStore records what the handlers pass and returns canned results. Methods
// a test does not set up fall through to the embedded nil interface and panic,
// which fails the test loudly.
type fakeStore struct {
	Store
	cell        domain.Cell
	runSpec     *store.RunSpec
	runReplayed bool
	runErr      error
	listLimit   int
	listDef     string
	imported    *domain.Definition
	overwrite   bool
	importHash  string
	importKey   string
	importErr   error
	replayed    bool
	saved       *domain.Definition
	ifMatch     *int
}

func (f *fakeStore) GetCell(context.Context, string, string, string) (domain.Cell, error) {
	return f.cell, nil
}

func (f *fakeStore) Lag(context.Context) ([]store.LagRow, error) { return nil, nil }

func (f *fakeStore) GetRun(_ context.Context, id string) (store.Run, error) {
	return store.Run{ID: id}, nil
}

func (f *fakeStore) CreateRun(_ context.Context, spec store.RunSpec) (store.Run, error) {
	f.runSpec = &spec
	return store.Run{ID: runUUID, Replayed: f.runReplayed}, f.runErr
}

func (f *fakeStore) ListRuns(_ context.Context, def string, limit int) ([]store.Run, error) {
	f.listDef, f.listLimit = def, limit
	return []store.Run{{ID: runUUID, DefinitionCode: "BS-STD"}}, nil
}

func (f *fakeStore) ListDefinitions(context.Context) ([]store.DefinitionSummary, error) {
	return []store.DefinitionSummary{{Code: "BS-STD", Name: "Balance sheet", Kind: "balance_sheet", LatestVersion: 2}}, nil
}

func (f *fakeStore) ImportDefinition(_ context.Context, def domain.Definition, overwrite bool, key, hash string) (domain.Definition, int, bool, error) {
	f.imported, f.overwrite, f.importKey, f.importHash = &def, overwrite, key, hash
	return def, 1, f.replayed, f.importErr
}

func (f *fakeStore) SaveDraft(_ context.Context, def domain.Definition, ifMatch *int) (domain.Definition, int, error) {
	f.saved, f.ifMatch = &def, ifMatch
	return def, 7, nil
}

func router(f *fakeStore) chi.Router {
	r := chi.NewRouter()
	New(f).Register(r)
	return r
}

func call(r http.Handler, method, path, body string, headers map[string]string, duties ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	p := tenant.Principal{TenantID: "00000000-0000-0000-0000-000000000001", UserID: "u1", Duties: map[string]struct{}{}}
	for _, d := range duties {
		p.Duties[d] = struct{}{}
	}
	req = req.WithContext(tenant.With(req.Context(), p))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func problemCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var p httpx.Problem
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatalf("body is not a problem: %s", w.Body.String())
	}
	return p.Code
}

const validDefinition = `{"schemaVersion":"1","code":"BS-STD","name":"Balance sheet","kind":"balance_sheet",
	"rows":[{"code":"CASH","kind":"account_range","label":"Cash","accountFrom":"1000","accountTo":"1099","normalBalance":"debit","format":{}}],
	"columns":[{"code":"C1","label":"Period","periodExpr":"P","currencyType":"acct"}]}`

func runBody(mutate string) string {
	base := map[string]string{
		"definition": `"BS-STD"`, "ledgerId": `"` + ledgerUUID + `"`, "currencyType": `"acct"`,
		"period": `{"year":2026,"no":3,"quarter":1}`, "calendar": `[{"year":2026,"no":3,"quarter":1}]`,
	}
	if mutate != "" {
		k, v, _ := strings.Cut(mutate, "=")
		base[k] = v
	}
	var parts []string
	for k, v := range base {
		parts = append(parts, `"`+k+`":`+v)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func TestCreateRun(t *testing.T) {
	key := map[string]string{"Idempotency-Key": "k1"}
	tests := []struct {
		name     string
		body     string
		headers  map[string]string
		duties   []string
		replayed bool
		want     int
		code     string
	}{
		{"missing key", runBody(""), nil, []string{DutyRun}, false, http.StatusBadRequest, "COM-0003"},
		{"missing duty", runBody(""), key, []string{DutyView}, false, http.StatusForbidden, "AUTH-0003"},
		{"ledger not a uuid", runBody(`ledgerId="L1"`), key, []string{DutyRun}, false, http.StatusUnprocessableEntity, "COM-0002"},
		{"bad currency type", runBody(`currencyType="eur"`), key, []string{DutyRun}, false, http.StatusUnprocessableEntity, "COM-0002"},
		{"calendar quarter out of range", runBody(`calendar=[{"year":2026,"no":3,"quarter":5}]`), key, []string{DutyRun}, false, http.StatusUnprocessableEntity, "COM-0002"},
		{"empty calendar", runBody(`calendar=[]`), key, []string{DutyRun}, false, http.StatusUnprocessableEntity, "COM-0002"},
		{"unknown field", runBody(`allowStale=true`), key, []string{DutyRun}, false, http.StatusBadRequest, "COM-0002"},
		{"created", runBody(""), key, []string{DutyRun}, false, http.StatusCreated, ""},
		{"replayed", runBody(""), key, []string{DutyRun}, true, http.StatusOK, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeStore{runReplayed: tc.replayed}
			w := call(router(f), http.MethodPost, "/api/v1/reporting/runs", tc.body, tc.headers, tc.duties...)
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.want, w.Body.String())
			}
			if tc.code != "" {
				if got := problemCode(t, w); got != tc.code {
					t.Fatalf("code %s, want %s", got, tc.code)
				}
				if f.runSpec != nil {
					t.Fatal("a rejected request must not reach the store")
				}
				return
			}
			if w.Header().Get("Location") != "/api/v1/reporting/runs/"+runUUID {
				t.Fatalf("Location = %q", w.Header().Get("Location"))
			}
			if f.runSpec.LedgerID != ledgerUUID || f.runSpec.IdempotencyKey != "k1" || f.runSpec.RequestedBy != "u1" || len(f.runSpec.RequestHash) != 64 {
				t.Fatalf("spec passed to the store = %+v", f.runSpec)
			}
		})
	}
}

func TestCreateRun_SameBodySameHash(t *testing.T) {
	f := &fakeStore{}
	r := router(f)
	key := map[string]string{"Idempotency-Key": "k1"}
	body := runBody("")
	call(r, http.MethodPost, "/api/v1/reporting/runs", body, key, DutyRun)
	first := f.runSpec.RequestHash
	call(r, http.MethodPost, "/api/v1/reporting/runs", body, key, DutyRun)
	if f.runSpec.RequestHash != first {
		t.Fatal("identical requests must hash identically so the key replays")
	}
	call(r, http.MethodPost, "/api/v1/reporting/runs", runBody(`currencyType="rpt"`), key, DutyRun)
	if f.runSpec.RequestHash == first {
		t.Fatal("a different request must hash differently so key reuse is detected")
	}
}

func TestRunReads_RejectNonUUIDWithoutStore(t *testing.T) {
	for _, path := range []string{"/api/v1/reporting/runs/R1", "/api/v1/reporting/runs/R1/cells", "/api/v1/reporting/runs/R1/cells/A/B/balances"} {
		w := call(router(&fakeStore{}), http.MethodGet, path, "", nil, DutyView)
		if w.Code != http.StatusNotFound || problemCode(t, w) != store.CodeRunNotFound {
			t.Fatalf("GET %s = %d %s, want 404 %s", path, w.Code, w.Body.String(), store.CodeRunNotFound)
		}
	}
}

func TestListRuns(t *testing.T) {
	f := &fakeStore{}
	w := call(router(f), http.MethodGet, "/api/v1/reporting/runs?definition=BS-STD&limit=25", "", nil, DutyView)
	if w.Code != http.StatusOK || f.listDef != "BS-STD" || f.listLimit != 25 || !strings.Contains(w.Body.String(), runUUID) {
		t.Fatalf("status %d def %q limit %d body %s", w.Code, f.listDef, f.listLimit, w.Body.String())
	}
	w = call(router(f), http.MethodGet, "/api/v1/reporting/runs?limit=999", "", nil, DutyView)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("limit above maximum = %d, want 400", w.Code)
	}
}

func TestListDefinitions(t *testing.T) {
	w := call(router(&fakeStore{}), http.MethodGet, "/api/v1/reporting/definitions", "", nil, DutyView)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"latestVersion":2`) {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	if w := call(router(&fakeStore{}), http.MethodGet, "/api/v1/reporting/definitions", "", nil); w.Code != http.StatusForbidden {
		t.Fatalf("without the view duty = %d, want 403", w.Code)
	}
}

func TestDrillBalances(t *testing.T) {
	path := "/api/v1/reporting/runs/" + runUUID + "/cells/CASH/C1/balances"
	t.Run("keys sum to the cell", func(t *testing.T) {
		f := &fakeStore{cell: domain.Cell{Row: "CASH", Column: "C1", Value: decimal.RequireFromString("15.75"), Keys: []domain.Key{
			{Account: "1000", Period: "2026-02", Amount: decimal.RequireFromString("5.25")},
			{Account: "1000", Period: "2026-03", Amount: decimal.RequireFromString("10.5")},
		}}}
		w := call(router(f), http.MethodGet, path, "", nil, DutyView)
		var out struct {
			Value     string `json:"value"`
			SumOfKeys string `json:"sumOfKeys"`
			Computed  bool   `json:"computed"`
			Keys      []struct {
				Amount string `json:"amount"`
			} `json:"keys"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != http.StatusOK {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
		if out.Value != "15.750000" || out.SumOfKeys != "15.750000" || len(out.Keys) != 2 || out.Keys[1].Amount != "10.500000" || out.Computed {
			t.Fatalf("drill = %+v", out)
		}
	})
	t.Run("computed cell has no keys", func(t *testing.T) {
		f := &fakeStore{cell: domain.Cell{Row: "GM", Column: "C1", Value: decimal.New(400, 0), Computed: true}}
		w := call(router(f), http.MethodGet, path, "", nil, DutyView)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"computed":true`) || !strings.Contains(w.Body.String(), `"keys":[]`) {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
	})
	t.Run("mismatch is never served", func(t *testing.T) {
		f := &fakeStore{cell: domain.Cell{Row: "CASH", Column: "C1", Value: decimal.New(10, 0), Keys: []domain.Key{{Amount: decimal.New(9, 0)}}}}
		w := call(router(f), http.MethodGet, path, "", nil, DutyView)
		if w.Code != http.StatusInternalServerError || problemCode(t, w) != "REPORT-0099" || strings.Contains(w.Body.String(), `"keys":`) {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
	})
}

func TestValidate_ReportsExpressionErrors(t *testing.T) {
	body := `{"schemaVersion":"1","name":"Bad","kind":"custom",
		"rows":[{"code":"R1","kind":"calculation","label":"x","normalBalance":"debit","expression":"R9 +"}],
		"columns":[{"code":"C1","label":"P","periodExpr":"P","currencyType":"acct"}]}`
	w := call(router(&fakeStore{}), http.MethodPost, "/api/v1/reporting/definitions/BADDEF:validate", body, nil, DutyMaintain)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		Valid  bool `json:"valid"`
		Issues []struct {
			Code string `json:"code"`
		} `json:"issues"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Valid || len(out.Issues) == 0 || out.Issues[0].Code != domain.CodeExpression {
		t.Fatalf("expected REPORT-0002 finding, got %+v", out)
	}
	if w := call(router(&fakeStore{}), http.MethodPost, "/api/v1/reporting/definitions/BADDEF:archive", body, nil, DutyMaintain); w.Code != http.StatusNotFound {
		t.Fatalf("unknown action = %d, want 404", w.Code)
	}
}

func TestPutDefinition(t *testing.T) {
	f := &fakeStore{}
	w := call(router(f), http.MethodPut, "/api/v1/reporting/definitions/OTHER", validDefinition, nil, DutyMaintain)
	if w.Code != http.StatusUnprocessableEntity || problemCode(t, w) != domain.CodeExpression || f.saved != nil {
		t.Fatalf("code mismatch = %d %s", w.Code, w.Body.String())
	}
	bad := strings.Replace(validDefinition, `"periodExpr":"P"`, `"periodExpr":"MTD"`, 1)
	if w := call(router(f), http.MethodPut, "/api/v1/reporting/definitions/BS-STD", bad, nil, DutyMaintain); w.Code != http.StatusUnprocessableEntity || f.saved != nil {
		t.Fatalf("invalid definition = %d %s", w.Code, w.Body.String())
	}
	if w := call(router(f), http.MethodPut, "/api/v1/reporting/definitions/BS-STD", validDefinition, map[string]string{"If-Match": "abc"}, DutyMaintain); w.Code != http.StatusBadRequest {
		t.Fatalf("malformed If-Match = %d", w.Code)
	}
	w = call(router(f), http.MethodPut, "/api/v1/reporting/definitions/BS-STD", validDefinition, map[string]string{"If-Match": `"3"`}, DutyMaintain)
	if w.Code != http.StatusOK || w.Header().Get("ETag") != `"7"` || f.ifMatch == nil || *f.ifMatch != 3 {
		t.Fatalf("save = %d etag %q ifMatch %v", w.Code, w.Header().Get("ETag"), f.ifMatch)
	}
}

func TestImportDefinition(t *testing.T) {
	path := "/api/v1/reporting/definitions/import"
	t.Run("unsupported schema", func(t *testing.T) {
		f := &fakeStore{}
		body := strings.Replace(validDefinition, `"schemaVersion":"1"`, `"schemaVersion":"9"`, 1)
		w := call(router(f), http.MethodPost, path, body, nil, DutyMaintain)
		if w.Code != http.StatusUnprocessableEntity || problemCode(t, w) != domain.CodeImportSchema || f.imported != nil {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
	})
	t.Run("duplicate code", func(t *testing.T) {
		f := &fakeStore{importErr: httpx.NewProblem(http.StatusConflict, store.CodeImportDuplicate, "exists")}
		w := call(router(f), http.MethodPost, path, validDefinition, nil, DutyMaintain)
		if w.Code != http.StatusConflict || problemCode(t, w) != store.CodeImportDuplicate || f.overwrite {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
	})
	t.Run("created then replayed", func(t *testing.T) {
		f := &fakeStore{}
		key := map[string]string{"Idempotency-Key": "imp-1"}
		w := call(router(f), http.MethodPost, path+"?overwrite=true", validDefinition, key, DutyMaintain)
		if w.Code != http.StatusCreated || !f.overwrite || f.importKey != "imp-1" || f.imported.Code != "BS-STD" {
			t.Fatalf("status %d overwrite %v key %q: %s", w.Code, f.overwrite, f.importKey, w.Body.String())
		}
		withOverwrite := f.importHash
		f.replayed = true
		if w := call(router(f), http.MethodPost, path, validDefinition, key, DutyMaintain); w.Code != http.StatusOK {
			t.Fatalf("replay = %d", w.Code)
		}
		if f.importHash == withOverwrite {
			t.Fatal("the overwrite flag must be part of the request hash")
		}
	})
}
