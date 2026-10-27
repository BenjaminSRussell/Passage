package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/store"
)

func (f *fakeStore) CreateProgram(context.Context, string, string, store.NewProgram) (int, []byte, error) {
	f.calls++
	return http.StatusCreated, []byte(`{"id":"p"}`), nil
}

func (f *fakeStore) DistributePositions(context.Context, string, string, string, store.DistributeRequest) (int, []byte, error) {
	f.calls++
	return http.StatusOK, []byte(`{"positions":[]}`), nil
}

func (f *fakeStore) AssignObject(context.Context, string, string, string, store.AssignRequest) (int, []byte, error) {
	f.calls++
	return http.StatusOK, []byte(`{"positions":[]}`), nil
}

func (f *fakeStore) CarryForward(context.Context, string, string, string, int) (int, []byte, error) {
	f.calls++
	return http.StatusOK, []byte(`{"amount":"0"}`), nil
}

func (f *fakeStore) ListPrograms(_ context.Context, le string) ([]store.Program, error) {
	f.calls++
	f.lastFilter = le
	return []store.Program{}, nil
}

func (f *fakeStore) GetProgram(_ context.Context, id string) (store.ProgramView, error) {
	f.calls++
	if id != programUUID {
		return store.ProgramView{}, httpx.NewProblem(http.StatusNotFound, store.CodeProgramNotFound, "investment program not found")
	}
	return store.ProgramView{Program: store.Program{ID: id}}, nil
}

// programRecorder captures the store request of program commands.
type programRecorder struct {
	fakeStore
	distribute store.DistributeRequest
	assign     store.AssignRequest
	toYear     int
}

func (f *programRecorder) DistributePositions(_ context.Context, _, _, _ string, req store.DistributeRequest) (int, []byte, error) {
	f.calls++
	f.distribute = req
	return http.StatusOK, []byte(`{}`), nil
}

func (f *programRecorder) AssignObject(_ context.Context, _, _, _ string, req store.AssignRequest) (int, []byte, error) {
	f.calls++
	f.assign = req
	return http.StatusOK, []byte(`{}`), nil
}

func (f *programRecorder) CarryForward(_ context.Context, _, _, _ string, toYear int) (int, []byte, error) {
	f.calls++
	f.toYear = toYear
	return http.StatusOK, []byte(`{}`), nil
}

const (
	programEntity = "7d1c1f2e-0000-4000-8000-000000000001"
	programUUID   = "7d1c1f2e-0000-4000-8000-000000000002"
	positionUUID  = "7d1c1f2e-0000-4000-8000-000000000003"
)

func TestProgramCreate_ValidationDutyAndKey(t *testing.T) {
	path := "/v1/budget/investment-programs"
	good := `{"legalEntityId":"` + programEntity + `","code":"PRG-2026","name":"Plant capex","approvalYear":2026,"currency":"USD","budget":"5000000.00"}`
	cases := []struct {
		name   string
		body   string
		duties string
		key    string
		status int
		code   string
	}{
		{"valid", good, dutyProgram, "k1", http.StatusCreated, ""},
		{"missing key", good, dutyProgram, "", http.StatusBadRequest, "COM-0001"},
		{"view duty only", good, dutyView, "k2", http.StatusForbidden, "AUTH-0003"},
		{"year out of range", strings.Replace(good, `2026,`, `1800,`, 1), dutyProgram, "k3", http.StatusBadRequest, "COM-0001"},
		{"negative budget", strings.Replace(good, `"5000000.00"`, `"-1"`, 1), dutyProgram, "k4", http.StatusBadRequest, "COM-0001"},
		{"five decimals", strings.Replace(good, `"5000000.00"`, `"1.00001"`, 1), dutyProgram, "k5", http.StatusBadRequest, "COM-0001"},
		{"lower-case code", strings.Replace(good, `PRG-2026`, `prg`, 1), dutyProgram, "k6", http.StatusBadRequest, "COM-0001"},
		{"unknown field", strings.Replace(good, `"name"`, `"title"`, 1), dutyProgram, "k7", http.StatusBadRequest, "COM-0001"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := &fakeStore{}
			rec := serve(t, st, http.MethodPost, path, c.body, c.duties, c.key)
			if rec.Code != c.status {
				t.Fatalf("status %d, want %d (%s)", rec.Code, c.status, rec.Body.String())
			}
			if c.code != "" {
				if got := problemCode(t, rec); got != c.code {
					t.Errorf("code %q, want %q", got, c.code)
				}
				if st.calls != 0 {
					t.Errorf("store called on a rejected request")
				}
			}
		})
	}
}

func TestProgramDistribute_ValidatesAndPassesAllocations(t *testing.T) {
	path := "/v1/budget/investment-programs/" + programUUID + ":distribute"
	rejected := map[string]string{
		"duplicate codes":  `{"allocations":[{"positionCode":"P1","budget":"10"},{"positionCode":"P1","budget":"5"}]}`,
		"unknown category": `{"allocations":[{"positionCode":"P1","category":"Opex","budget":"10"}]}`,
		"no allocations":   `{"allocations":[]}`,
		"bad parent":       `{"parentPositionId":"x","allocations":[{"positionCode":"P1","budget":"10"}]}`,
		"negative":         `{"allocations":[{"positionCode":"P1","budget":"-10"}]}`,
		"lower-case code":  `{"allocations":[{"positionCode":"p1","budget":"10"}]}`,
	}
	for name, body := range rejected {
		st := &programRecorder{}
		if rec := serve(t, st, http.MethodPost, path, body, dutyProgram, "k"); rec.Code != http.StatusBadRequest || st.calls != 0 {
			t.Errorf("%s: status %d calls %d", name, rec.Code, st.calls)
		}
	}
	st := &programRecorder{}
	body := `{"parentPositionId":"` + positionUUID + `","allocations":[{"positionCode":"PLANT.1","category":"Contingency","budget":"4800000"},{"positionCode":"IT","budget":"0"}]}`
	if rec := serve(t, st, http.MethodPost, path, body, dutyProgram, "k"); rec.Code != http.StatusOK {
		t.Fatalf("valid distribute: status %d (%s)", rec.Code, rec.Body.String())
	}
	got := st.distribute
	if got.ParentPositionID != positionUUID || len(got.Allocations) != 2 || got.Allocations[0].PositionCode != "PLANT.1" ||
		got.Allocations[0].Category != "Contingency" || got.Allocations[0].Budget.String() != "4800000" || got.Allocations[1].Category != "" {
		t.Errorf("store got %+v", got)
	}
	if rec := serve(t, &programRecorder{}, http.MethodPost, "/v1/budget/investment-programs/not-a-uuid:distribute", body, dutyProgram, "k"); rec.Code != http.StatusNotFound ||
		problemCode(t, rec) != store.CodeProgramNotFound {
		t.Errorf("bad program id: status %d", rec.Code)
	}
}

func TestProgramAssign_Validation(t *testing.T) {
	path := "/v1/budget/investment-programs/" + programUUID + ":assign"
	good := `{"positionId":"` + positionUUID + `","objectType":"CapexRequest","objectRef":"CR-000123","plan":"250000","actual":"10000.50","commitment":"0"}`
	st := &programRecorder{}
	if rec := serve(t, st, http.MethodPost, path, good, dutyProgram, "k"); rec.Code != http.StatusOK {
		t.Fatalf("valid assign: status %d (%s)", rec.Code, rec.Body.String())
	}
	if st.assign.ObjectRef != "CR-000123" || st.assign.Actual.String() != "10000.5" || st.assign.PositionID != positionUUID {
		t.Errorf("store got %+v", st.assign)
	}
	for name, body := range map[string]string{
		"unknown object type": strings.Replace(good, "CapexRequest", "Asset", 1),
		"empty ref":           strings.Replace(good, "CR-000123", "", 1),
		"negative actual":     strings.Replace(good, `"10000.50"`, `"-1"`, 1),
		"bad position":        strings.Replace(good, positionUUID, "p", 1),
	} {
		st := &programRecorder{}
		if rec := serve(t, st, http.MethodPost, path, body, dutyProgram, "k"); rec.Code != http.StatusBadRequest || st.calls != 0 {
			t.Errorf("%s: status %d calls %d", name, rec.Code, st.calls)
		}
	}
}

func TestProgramCarryForward_PassesYear(t *testing.T) {
	path := "/v1/budget/investment-programs/" + programUUID + ":carry-forward"
	if rec := serve(t, &programRecorder{}, http.MethodPost, path, `{"toYear":1800}`, dutyProgram, "k"); rec.Code != http.StatusBadRequest {
		t.Errorf("toYear 1800: status %d", rec.Code)
	}
	st := &programRecorder{}
	if rec := serve(t, st, http.MethodPost, path, `{"toYear":2027}`, dutyProgram, "k2"); rec.Code != http.StatusOK || st.toYear != 2027 {
		t.Errorf("toYear 2027: status %d year %d", rec.Code, st.toYear)
	}
	if rec := serve(t, &programRecorder{}, http.MethodPost, path, `{"toYear":2027}`, dutyView, "k3"); rec.Code != http.StatusForbidden {
		t.Errorf("view duty: status %d", rec.Code)
	}
}

func TestProgramReads(t *testing.T) {
	st := &fakeStore{}
	if rec := serve(t, st, http.MethodGet, "/v1/budget/investment-programs?legalEntityId="+programEntity, "", dutyView, ""); rec.Code != http.StatusOK ||
		st.lastFilter != programEntity || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Errorf("list: status %d filter %q body %s", rec.Code, st.lastFilter, rec.Body.String())
	}
	if rec := serve(t, &fakeStore{}, http.MethodGet, "/v1/budget/investment-programs?legalEntityId=x", "", dutyView, ""); rec.Code != http.StatusBadRequest {
		t.Errorf("bad filter: status %d", rec.Code)
	}
	if rec := serve(t, &fakeStore{}, http.MethodGet, "/v1/budget/investment-programs/"+programUUID, "", dutyView, ""); rec.Code != http.StatusOK {
		t.Errorf("get: status %d", rec.Code)
	}
	rec := serve(t, &fakeStore{}, http.MethodGet, "/v1/budget/investment-programs/nope", "", dutyView, "")
	if rec.Code != http.StatusNotFound || problemCode(t, rec) != store.CodeProgramNotFound {
		t.Errorf("malformed id: status %d", rec.Code)
	}
	if rec := serve(t, &fakeStore{}, http.MethodGet, "/v1/budget/investment-programs", "", dutyProgram, ""); rec.Code != http.StatusForbidden {
		t.Errorf("list without budget.view: status %d", rec.Code)
	}
}
