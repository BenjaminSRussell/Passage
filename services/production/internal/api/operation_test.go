package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

func (f *fakeStore) ApplyOperationCommand(_ context.Context, c store.OperationCommand) (store.Order, store.OperationOutcome, error) {
	f.operCommands = append(f.operCommands, c)
	if f.operErr != nil {
		return store.Order{}, store.OperationOutcome{}, f.operErr
	}
	return store.Order{ID: c.OrderID, Version: c.IfMatch + 1}, f.operOutcome, nil
}

func operCall(path string, duties []string, headers map[string]string) call {
	return call{method: http.MethodPost, path: path, duties: duties, headers: headers}
}

func TestOperationCommand_PassesTheCommandToTheStore(t *testing.T) {
	f := &fakeStore{operOutcome: store.OperationOutcome{Changed: true}}
	a, h := newServer(t, f)
	id := ids.New()
	rec := do(h, operCall("/v1/production/orders/"+id+"/operations/20:reopen", []string{DutyJournalPost},
		map[string]string{"If-Match": `"4"`, idempotencyName: "reopen-1"}))
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"5"` || rec.Header().Get("Idempotent-Replayed") != "" {
		t.Fatalf("got %d ETag %q replayed %q: %s", rec.Code, rec.Header().Get("ETag"), rec.Header().Get("Idempotent-Replayed"), rec.Body.String())
	}
	if len(f.operCommands) != 1 {
		t.Fatalf("store called %d times", len(f.operCommands))
	}
	c := f.operCommands[0]
	if c.OrderID != id || c.OperNumber != 20 || c.Action != domain.OperReopen || c.IfMatch != 4 || c.Actor != "planner-1" ||
		c.IdempotencyKey != "reopen-1" || c.RequestHash == "" {
		t.Fatalf("command %+v", c)
	}
	if a.moved.Load() != 1 {
		t.Fatalf("moved counter %d, want 1", a.moved.Load())
	}
	// The hash names the operation and the command, so one key cannot serve another.
	do(h, operCall("/v1/production/orders/"+id+"/operations/20:finish", []string{DutyJournalPost},
		map[string]string{"If-Match": `"5"`, idempotencyName: "finish-1"}))
	do(h, operCall("/v1/production/orders/"+id+"/operations/30:finish", []string{DutyJournalPost},
		map[string]string{"If-Match": `"5"`, idempotencyName: "finish-2"}))
	if f.operCommands[1].RequestHash == c.RequestHash || f.operCommands[1].RequestHash == f.operCommands[2].RequestHash {
		t.Fatal("request hashes must differ by command and operation")
	}
}

func TestOperationCommand_ReplayAndNoOpHeaders(t *testing.T) {
	f := &fakeStore{operOutcome: store.OperationOutcome{Replayed: true}}
	a, h := newServer(t, f)
	rec := do(h, operCall("/v1/production/orders/"+ids.New()+"/operations/10:finish", []string{DutyJournalPost},
		map[string]string{"If-Match": `"2"`, idempotencyName: "k"}))
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" || a.replayed.Load() != 1 {
		t.Fatalf("replay: %d %q", rec.Code, rec.Header().Get("Idempotent-Replayed"))
	}
	f.operOutcome = store.OperationOutcome{}
	rec = do(h, operCall("/v1/production/orders/"+ids.New()+"/operations/10:finish", []string{DutyJournalPost},
		map[string]string{"If-Match": `"2"`, idempotencyName: "k2"}))
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "" || a.moved.Load() != 0 {
		t.Fatalf("no-op: %d %q moved %d", rec.Code, rec.Header().Get("Idempotent-Replayed"), a.moved.Load())
	}
}

func TestOperationCommand_RefusalsBeforeTheStore(t *testing.T) {
	id := ids.New()
	ok := map[string]string{"If-Match": `"1"`, idempotencyName: "k"}
	cases := []struct {
		name    string
		path    string
		duties  []string
		headers map[string]string
		body    string
		status  int
		code    string
	}{
		{"without the duty", "/v1/production/orders/" + id + "/operations/10:finish", []string{DutyWrite}, ok, "", http.StatusForbidden, "AUTH-0003"},
		{"unknown command", "/v1/production/orders/" + id + "/operations/10:split", []string{DutyJournalPost}, ok, "", http.StatusNotFound, unknownActionCode},
		{"no command", "/v1/production/orders/" + id + "/operations/10", []string{DutyJournalPost}, ok, "", http.StatusNotFound, unknownActionCode},
		{"operation zero", "/v1/production/orders/" + id + "/operations/0:finish", []string{DutyJournalPost}, ok, "", http.StatusNotFound, unknownActionCode},
		{"operation too large", "/v1/production/orders/" + id + "/operations/99999999999:finish", []string{DutyJournalPost}, ok, "", http.StatusNotFound, unknownActionCode},
		{"order id not a uuid", "/v1/production/orders/x/operations/10:finish", []string{DutyJournalPost}, ok, "", http.StatusNotFound, store.CodeOrderNotFound},
		{"no If-Match", "/v1/production/orders/" + id + "/operations/10:finish", []string{DutyJournalPost},
			map[string]string{idempotencyName: "k"}, "", http.StatusPreconditionRequired, "precondition_required"},
		{"no Idempotency-Key", "/v1/production/orders/" + id + "/operations/10:finish", []string{DutyJournalPost},
			map[string]string{"If-Match": `"1"`}, "", http.StatusBadRequest, validationCode},
		{"a body", "/v1/production/orders/" + id + "/operations/10:finish", []string{DutyJournalPost}, ok, `{"hours":"1"}`, http.StatusBadRequest, validationCode},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeStore{}
			_, h := newServer(t, f)
			rec := do(h, call{method: http.MethodPost, path: c.path, duties: c.duties, headers: c.headers, body: c.body})
			expectProblem(t, rec, c.status, c.code)
			if len(f.operCommands) != 0 {
				t.Fatal("the store must not be called")
			}
		})
	}
}

func TestOperationCommand_StoreProblemsPassThrough(t *testing.T) {
	f := &fakeStore{operErr: httpx.NewProblem(http.StatusConflict, domain.CodeOperationState, "not started")}
	a, h := newServer(t, f)
	rec := do(h, operCall("/v1/production/orders/"+ids.New()+"/operations/10:finish", []string{DutyJournalPost},
		map[string]string{"If-Match": `"1"`, idempotencyName: "k"}))
	expectProblem(t, rec, http.StatusConflict, domain.CodeOperationState)
	if a.refused.Load() != 1 {
		t.Fatalf("refused counter %d, want 1", a.refused.Load())
	}
}

func TestCreate_OrderQuantityRules(t *testing.T) {
	cases := []struct {
		name   string
		rules  map[string]any
		status int
		code   string
	}{
		{"within the rules", map[string]any{"minimumQty": "50", "maximumQty": "200", "multiple": "25"}, http.StatusCreated, ""},
		{"below the minimum", map[string]any{"minimumQty": "150"}, http.StatusUnprocessableEntity, domain.CodeOrderQuantity},
		{"above the maximum", map[string]any{"maximumQty": "99.5"}, http.StatusUnprocessableEntity, domain.CodeOrderQuantity},
		{"not a multiple", map[string]any{"multiple": "12"}, http.StatusUnprocessableEntity, domain.CodeOrderQuantity},
		{"zero rules do not apply", map[string]any{"minimumQty": "0", "maximumQty": "0", "multiple": "0"}, http.StatusCreated, ""},
		{"negative rule", map[string]any{"multiple": "-5"}, http.StatusBadRequest, validationCode},
		{"maximum below minimum", map[string]any{"minimumQty": "10", "maximumQty": "5"}, http.StatusUnprocessableEntity, domain.CodeInvalidInput},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeStore{}
			_, h := newServer(t, f)
			body := createBody(func(m map[string]any) {
				for k, v := range c.rules {
					m[k] = v
				}
			})
			rec := do(h, call{method: http.MethodPost, path: "/v1/production/orders", body: body, duties: []string{DutyWrite},
				headers: map[string]string{idempotencyName: "rules-" + c.name}})
			if c.code == "" {
				if rec.Code != c.status || len(f.created) != 1 {
					t.Fatalf("want %d and one create, got %d: %s", c.status, rec.Code, rec.Body.String())
				}
				return
			}
			expectProblem(t, rec, c.status, c.code)
			if len(f.created) != 0 {
				t.Fatal("a refused order must not reach the store")
			}
		})
	}
}

// A body without the rules hashes as it did before they existed, so a retry
// across the upgrade still replays.
func TestCreate_RulesDoNotChangeTheHashOfABodyWithoutThem(t *testing.T) {
	var req createRequest
	if err := json.Unmarshal([]byte(createBody(nil)), &req); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(req)
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	for _, name := range []string{"minimumQty", "maximumQty", "multiple"} {
		if _, ok := fields[name]; ok {
			t.Errorf("%s is part of the hashed body without a value", name)
		}
	}
}

func TestCreate_PassesEstimateOnCreate(t *testing.T) {
	f := &fakeStore{}
	a := New(f, Config{EstimateOnCreate: true})
	r := httpx.NewRouter()
	a.Register(r)
	rec := do(r, call{method: http.MethodPost, path: "/v1/production/orders", body: createBody(nil), duties: []string{DutyWrite},
		headers: map[string]string{idempotencyName: "estimate-1"}})
	if rec.Code != http.StatusCreated || len(f.created) != 1 || !f.created[0].EstimateOnCreate {
		t.Fatalf("got %d, created %d: %s", rec.Code, len(f.created), rec.Body.String())
	}
}

func TestPatch_OrderQuantityRules(t *testing.T) {
	id := ids.New()
	cases := []struct {
		name   string
		body   string
		status int
		code   string
	}{
		{"new quantity meets the multiple", `{"quantity":"120","multiple":"12"}`, http.StatusOK, ""},
		{"new quantity breaks the multiple", `{"quantity":"121","multiple":"12"}`, http.StatusUnprocessableEntity, domain.CodeOrderQuantity},
		{"new quantity above the maximum", `{"quantity":"500","maximumQty":"400"}`, http.StatusUnprocessableEntity, domain.CodeOrderQuantity},
		{"rules without a quantity", `{"deliveryDate":"2026-12-01","minimumQty":"5"}`, http.StatusBadRequest, validationCode},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeStore{}
			_, h := newServer(t, f)
			rec := do(h, call{method: http.MethodPatch, path: "/v1/production/orders/" + id, body: c.body, duties: []string{DutyWrite},
				headers: map[string]string{"If-Match": `"3"`}})
			if c.code == "" {
				if rec.Code != c.status || len(f.updates) != 1 {
					t.Fatalf("want %d, got %d: %s", c.status, rec.Code, rec.Body.String())
				}
				return
			}
			expectProblem(t, rec, c.status, c.code)
			if len(f.updates) != 0 {
				t.Fatal("a refused PATCH must not reach the store")
			}
		})
	}
}
