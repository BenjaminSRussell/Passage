package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
)

const testStructure = "0190a000-0000-7000-8000-000000000010"

const structurePath = "/api/v1/ledger/ledgers/" + testLedger + "/account-structures/" + testStructure

func TestPutStructure_CreatedThenReplacedWithDefaults(t *testing.T) {
	fs := &fakeStore{created: true}
	h := server(fs)
	body := `{"name":"EXPENSE","version":1,"effectiveFrom":"2026-01-01","segments":["DEPT"],"accountFilter":["6000..6999"],
		"constraints":[{"segment":"DEPT","under":{"MAIN_ACCOUNT":"6010"},"allowed":["*"]}]}`
	rec := call(h, http.MethodPut, structurePath, body, dutyStructureWrite, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status %d: %s", rec.Code, rec.Body.String())
	}
	got := fs.putStructures[0]
	if got.ledgerID != testLedger || got.id != testStructure {
		t.Fatalf("addressed %s/%s", got.ledgerID, got.id)
	}
	if got.in.Status != domain.StructureDraft || got.in.Priority != 0 {
		t.Fatalf("status must default to draft and priority to 0: %+v", got.in)
	}
	if !got.in.EffectiveFrom.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("effectiveFrom %v", got.in.EffectiveFrom)
	}
	if len(got.in.Constraints) != 1 || got.in.Constraints[0].Under["MAIN_ACCOUNT"] != "6010" {
		t.Fatalf("constraints not passed through: %+v", got.in.Constraints)
	}
	view := decode(t, rec)
	if view["id"] != testStructure || view["ledgerId"] != testLedger || view["effectiveFrom"] != "2026-01-01" || view["status"] != "draft" {
		t.Fatalf("view: %v", view)
	}

	fs.created = false
	rec = call(h, http.MethodPut, structurePath, body, dutyStructureWrite, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("replace status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPutStructure_RequestErrors(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		body   string
		duties string
		status int
	}{
		{"missing duty", structurePath, `{"name":"A","version":1,"effectiveFrom":"2026-01-01"}`, dutyRead, http.StatusForbidden},
		{"malformed effectiveFrom", structurePath, `{"name":"A","version":1,"effectiveFrom":"01/01/2026"}`, dutyStructureWrite, http.StatusBadRequest},
		{"structure id not a UUID", "/api/v1/ledger/ledgers/" + testLedger + "/account-structures/abc", `{"name":"A","version":1,"effectiveFrom":"2026-01-01"}`, dutyStructureWrite, http.StatusBadRequest},
		{"ledger id not a UUID", "/api/v1/ledger/ledgers/nope/account-structures/" + testStructure, `{"name":"A","version":1,"effectiveFrom":"2026-01-01"}`, dutyStructureWrite, http.StatusNotFound},
		{"unknown field", structurePath, `{"name":"A","version":1,"effectiveFrom":"2026-01-01","colour":"red"}`, dutyStructureWrite, http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := &fakeStore{}
			rec := call(server(fs), http.MethodPut, tc.path, tc.body, tc.duties, nil)
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if len(fs.putStructures) != 0 {
				t.Fatal("a refused request must not reach the store")
			}
		})
	}
}

func TestPutStructure_StoreOutcomes(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"invalid definition", &domain.RejectedError{Violations: []domain.Violation{{Code: domain.CodeStructureInvalid, Field: "segments", Message: "x"}}},
			http.StatusUnprocessableEntity, domain.CodeStructureInvalid},
		{"locked version", httpx.NewProblem(http.StatusConflict, domain.CodeStructureLocked, "locked"), http.StatusConflict, domain.CodeStructureLocked},
		{"duplicate version", httpx.NewProblem(http.StatusConflict, domain.CodeStructureVersionExists, "exists"), http.StatusConflict, domain.CodeStructureVersionExists},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := &fakeStore{err: tc.err}
			rec := call(server(fs), http.MethodPut, structurePath, `{"name":"A","version":1,"effectiveFrom":"2026-01-01","status":"active"}`, dutyStructureWrite, nil)
			if rec.Code != tc.status || decode(t, rec)["code"] != tc.code {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestGetStructure_NotFoundAndFound(t *testing.T) {
	fs := &fakeStore{structures: []domain.Structure{{ID: testStructure, Name: "EXPENSE", Version: 2, Status: domain.StructureActive,
		EffectiveFrom: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), Segments: []string{"DEPT"}}}}
	h := server(fs)
	rec := call(h, http.MethodGet, structurePath, "", dutyRead, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if v := decode(t, rec); v["version"] != float64(2) || v["effectiveFrom"] != "2026-02-01" {
		t.Fatalf("view %v", v)
	}
	rec = call(h, http.MethodGet, "/api/v1/ledger/ledgers/"+testLedger+"/account-structures/not-a-uuid", "", dutyRead, nil)
	if rec.Code != http.StatusNotFound || decode(t, rec)["code"] != domain.CodeStructureNotFound {
		t.Fatalf("malformed id: %d %s", rec.Code, rec.Body.String())
	}
	rec = call(h, http.MethodGet, "/api/v1/ledger/ledgers/"+testLedger+"/account-structures/0190a000-0000-7000-8000-0000000000ff", "", dutyRead, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id: %d", rec.Code)
	}
}

func TestListStructures_ItemsAndLedgerNotFound(t *testing.T) {
	fs := &fakeStore{structures: []domain.Structure{{ID: testStructure, Name: "A", Version: 1, Status: domain.StructureDraft}}}
	rec := call(server(fs), http.MethodGet, "/api/v1/ledger/ledgers/"+testLedger+"/account-structures", "", dutyRead, nil)
	items, _ := decode(t, rec)["items"].([]any)
	if rec.Code != http.StatusOK || len(items) != 1 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	fs.err = httpx.NewProblem(http.StatusNotFound, "LEDGER_NOT_FOUND", "ledger not found")
	rec = call(server(fs), http.MethodGet, "/api/v1/ledger/ledgers/"+testLedger+"/account-structures", "", dutyRead, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestPutDimensionValue(t *testing.T) {
	path := "/api/v1/ledger/ledgers/" + testLedger + "/dimension-values/DEPT/D100"
	fs := &fakeStore{created: true}
	rec := call(server(fs), http.MethodPut, path, `{"name":"Sales","activeFrom":"2026-01-01","activeTo":null,"suspended":true}`, dutyDimensionWrite, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	in := fs.putValues[0]
	if in.Attribute != "DEPT" || in.Value != "D100" || !in.Suspended || in.ActiveFrom == nil || in.ActiveTo != nil {
		t.Fatalf("value passed down: %+v", in)
	}
	if v := decode(t, rec); v["activeFrom"] != "2026-01-01" || v["suspended"] != true {
		t.Fatalf("view %v", v)
	}

	fs = &fakeStore{}
	rec = call(server(fs), http.MethodPut, path, `{"name":"Sales","activeFrom":"soon","activeTo":"later"}`, dutyDimensionWrite, nil)
	errs, _ := decode(t, rec)["errors"].(map[string]any)
	if rec.Code != http.StatusBadRequest || errs["activeFrom"] == nil || errs["activeTo"] == nil || len(fs.putValues) != 0 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	rec = call(server(&fakeStore{}), http.MethodPut, path, `{"name":"Sales"}`, dutyRead, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing duty: %d", rec.Code)
	}

	fs = &fakeStore{err: &domain.RejectedError{Violations: []domain.Violation{{Code: domain.CodeDimensionInvalid, Field: "attribute", Message: "x"}}}}
	rec = call(server(fs), http.MethodPut, path, `{"name":"Sales"}`, dutyDimensionWrite, nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("store rejection: %d", rec.Code)
	}
}

func TestPostEntry_StructureViolationsCarryLineIndex(t *testing.T) {
	idx := 0
	fs := &fakeStore{err: &domain.RejectedError{Violations: []domain.Violation{{Code: domain.CodeDimensionRequired, Field: "lines.dimensions.DEPT", LineIndex: &idx, Message: "x"}}}}
	rec := call(server(fs), http.MethodPost, "/api/v1/ledger/journal-entries", entryBody, dutyPost, map[string]string{idempotencyHeader: "k-1"})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	m := decode(t, rec)
	vs, _ := m["violations"].([]any)
	if m["code"] != domain.CodeDimensionRequired || len(vs) != 1 || vs[0].(map[string]any)["lineIndex"] != float64(0) {
		t.Fatalf("body %v", m)
	}
}
