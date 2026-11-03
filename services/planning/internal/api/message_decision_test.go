package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

const messagePath = "/v1/planning/messages/" + fgID

// messageStore records the decision it is asked for and answers with err.
type messageStore struct {
	fakeStore
	decided  store.MessageDecision
	id       string
	expected int
	calls    int
	replay   bool
	err      error
	status   string
	listed   string
}

func (m *messageStore) DecideMessage(_ context.Context, id string, expected int, d store.MessageDecision) (store.Message, bool, error) {
	m.calls++
	m.id, m.expected, m.decided = id, expected, d
	if m.err != nil {
		return store.Message{}, false, m.err
	}
	status := "dismissed"
	if d.Apply {
		status = "applied"
	}
	return store.Message{ID: id, Action: "postpone", Status: status, Version: expected + 1, DecisionNote: d.Note}, m.replay, nil
}

func (m *messageStore) ListMessages(_ context.Context, _, _, status, _ string, _ int) ([]store.Message, error) {
	m.listed = status
	return []store.Message{}, nil
}

func (m *messageStore) GetMessage(_ context.Context, id string) (store.Message, error) {
	return store.Message{ID: id, Action: "cancel", Status: "open", Version: 3}, nil
}

func TestDecideMessage_AppliesWithETagAndNote(t *testing.T) {
	st := &messageStore{}
	rec := doIfMatch(t, st, http.MethodPost, messagePath+":apply", `{"note":"vendor agreed"}`, `"1"`, dutyOrders)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` || !strings.Contains(rec.Body.String(), `"status":"applied"`) {
		t.Fatalf("apply: %d %s etag %q", rec.Code, rec.Body.String(), rec.Header().Get("ETag"))
	}
	if !st.decided.Apply || st.decided.Note != "vendor agreed" || st.id != fgID || st.expected != 1 {
		t.Fatalf("store got id %s expected %d decision %+v", st.id, st.expected, st.decided)
	}
}

func TestDecideMessage_DismissNeedsNoBody(t *testing.T) {
	st := &messageStore{}
	rec := doIfMatch(t, st, http.MethodPost, messagePath+":dismiss", ``, `"4"`, dutyOrders)
	if rec.Code != http.StatusOK || st.decided.Apply || !strings.Contains(rec.Body.String(), `"status":"dismissed"`) {
		t.Fatalf("dismiss: %d %s", rec.Code, rec.Body.String())
	}
}

// TestDecideMessage_EmptyChunkedBody: a request without Content-Length and with no body (a
// chunked transfer through a proxy) is a decision without a note, not a malformed body.
func TestDecideMessage_EmptyChunkedBody(t *testing.T) {
	st := &messageStore{}
	router := httpx.NewRouter()
	newRouter(st).Register(router)
	req := httptest.NewRequest(http.MethodPost, messagePath+":dismiss", strings.NewReader(""))
	req.ContentLength = -1
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-Duties", dutyOrders)
	req.Header.Set("If-Match", `"1"`)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || st.decided.Note != "" {
		t.Fatalf("empty chunked dismiss: %d %s", rec.Code, rec.Body.String())
	}
}

func TestDecideMessage_ForwardsIdempotencyKeyAndMarksReplay(t *testing.T) {
	st := &messageStore{replay: true}
	router := httpx.NewRouter()
	newRouter(st).Register(router)
	rec := doKeyed(t, router, messagePath+":dismiss", "key-1")
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" || st.decided.IdempotencyKey != "key-1" {
		t.Fatalf("replay: %d replayed %q key %q", rec.Code, rec.Header().Get("Idempotent-Replayed"), st.decided.IdempotencyKey)
	}
}

func TestDecideMessage_Refusals(t *testing.T) {
	long := strings.Repeat("x", 501)
	cases := []struct {
		name, path, body, ifMatch, duties string
		status                            int
		code                              string
	}{
		{"missing If-Match is 428", messagePath + ":apply", ``, "", dutyOrders, http.StatusPreconditionRequired, codePrecondition},
		{"read duty cannot decide", messagePath + ":apply", ``, `"1"`, "planning.read", http.StatusForbidden, "AUTH-0003"},
		{"firm duty cannot decide", messagePath + ":dismiss", ``, `"1"`, "planning.order.firm", http.StatusForbidden, "AUTH-0003"},
		{"unknown id is 404", "/v1/planning/messages/not-a-uuid:apply", ``, `"1"`, dutyOrders, http.StatusNotFound, store.CodeMessageNotFound},
		{"note over 500 characters", messagePath + ":apply", `{"note":"` + long + `"}`, `"1"`, dutyOrders, http.StatusBadRequest, codeValidation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &messageStore{}
			rec := doIfMatch(t, st, http.MethodPost, tc.path, tc.body, tc.ifMatch, tc.duties)
			if rec.Code != tc.status || problemOf(t, rec).Code != tc.code || st.calls != 0 {
				t.Fatalf("%d %s calls %d; want %d %s", rec.Code, rec.Body.String(), st.calls, tc.status, tc.code)
			}
		})
	}
}

func TestDecideMessage_StoreProblemsPassThrough(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		status int
		code   string
	}{
		"decided":       {httpx.NewProblem(http.StatusConflict, store.CodeMessageDecided, "decided"), http.StatusConflict, store.CodeMessageDecided},
		"stale version": {httpx.NewProblem(http.StatusConflict, store.CodeMessageStale, "stale"), http.StatusConflict, store.CodeMessageStale},
		"information":   {httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeMessageNotActionable, "info"), http.StatusUnprocessableEntity, store.CodeMessageNotActionable},
	} {
		t.Run(name, func(t *testing.T) {
			rec := doIfMatch(t, &messageStore{err: tc.err}, http.MethodPost, messagePath+":apply", ``, `"1"`, dutyOrders)
			if rec.Code != tc.status || problemOf(t, rec).Code != tc.code {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestGetMessage_CarriesETag(t *testing.T) {
	rec := doIfMatch(t, &messageStore{}, http.MethodGet, messagePath, ``, "", "planning.read")
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"3"` {
		t.Fatalf("get: %d etag %q", rec.Code, rec.Header().Get("ETag"))
	}
}

func TestListMessages_StatusFilter(t *testing.T) {
	st := &messageStore{}
	if rec := doIfMatch(t, st, http.MethodGet, "/v1/planning/messages?planCode=MAIN&status=dismissed", ``, "", "planning.read"); rec.Code != http.StatusOK || st.listed != "dismissed" {
		t.Fatalf("dismissed: %d listed %q", rec.Code, st.listed)
	}
	if rec := doIfMatch(t, st, http.MethodGet, "/v1/planning/messages?planCode=MAIN&status=done", ``, "", "planning.read"); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown status: %d", rec.Code)
	}
}

// doKeyed posts a decision with If-Match and an Idempotency-Key.
func doKeyed(t *testing.T, router http.Handler, path, key string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, nil)
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-Duties", dutyOrders)
	req.Header.Set("If-Match", `"1"`)
	req.Header.Set("Idempotency-Key", key)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}
