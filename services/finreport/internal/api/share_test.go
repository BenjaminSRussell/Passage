package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/config"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/store"
)

const (
	shareLibUUID  = "66666666-6666-6666-6666-666666666666"
	shareLinkUUID = "77777777-7777-7777-7777-777777777777"
	shareToken    = "0190f0a000007000800000000000000a.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
)

type shareStore struct {
	Store
	spec     store.ShareSpec
	created  int
	replay   bool
	err      error
	revID    string
	revIf    *int
	revBy    string
	listedID string
	redeemed string
	entry    store.LibraryEntry
}

func (s *shareStore) link() store.ShareLink {
	return store.ShareLink{ID: shareLinkUUID, LibraryID: shareLibUUID, Status: "active", CreatedBy: "u1",
		CreatedAt: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), ExpiresAt: time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC), Revision: 1}
}

func (s *shareStore) CreateShareLink(_ context.Context, spec store.ShareSpec) (store.ShareLink, error) {
	s.spec, s.created = spec, s.created+1
	l := s.link()
	if s.replay {
		l.Replayed = true
	} else {
		l.Token = shareToken
	}
	return l, s.err
}

func (s *shareStore) ListShareLinks(_ context.Context, id string) ([]store.ShareLink, error) {
	s.listedID = id
	return []store.ShareLink{s.link()}, s.err
}

func (s *shareStore) RevokeShareLink(_ context.Context, id string, ifMatch *int, actor string) (store.ShareLink, error) {
	s.revID, s.revIf, s.revBy = id, ifMatch, actor
	l := s.link()
	l.Status, l.Revision = "revoked", 2
	return l, s.err
}

func (s *shareStore) RedeemShareLink(_ context.Context, token string) (store.SharedFile, error) {
	s.redeemed = token
	if s.err != nil {
		return store.SharedFile{}, s.err
	}
	return store.SharedFile{LinkID: shareLinkUUID, Entry: s.entry, Content: []byte("a,b\n1,2\n")}, nil
}

const sharePath = "/v1/reporting/library/" + shareLibUUID + "/share-links"

func TestCreateShareLink_NeedsTheShareDutyAndAKeyAndShowsTheTokenOnce(t *testing.T) {
	st := &shareStore{}
	r := scheduleRouter(st)
	key := map[string]string{"Idempotency-Key": "k"}
	if w := call(r, http.MethodPost, sharePath, `{}`, key, DutyView, DutyHold); w.Code != http.StatusForbidden {
		t.Fatalf("without the duty: %d", w.Code)
	}
	if w := call(r, http.MethodPost, sharePath, `{}`, nil, DutyShare); w.Code != http.StatusBadRequest || problemCode(t, w) != "COM-0003" {
		t.Fatalf("without a key: %d %s", w.Code, w.Body.String())
	}
	if w := call(r, http.MethodPost, sharePath, `{"expiresInDays":3,"extra":1}`, key, DutyShare); w.Code != http.StatusBadRequest || st.created != 0 {
		t.Fatalf("unknown field: %d created %d", w.Code, st.created)
	}
	if w := call(r, http.MethodPost, "/v1/reporting/library/nope/share-links", `{}`, key, DutyShare); w.Code != http.StatusNotFound || st.created != 0 {
		t.Fatalf("bad library id: %d", w.Code)
	}
	w := call(r, http.MethodPost, sharePath, ``, key, DutyShare)
	if w.Code != http.StatusCreated || w.Header().Get("ETag") != `"1"` {
		t.Fatalf("create: %d %v %s", w.Code, w.Header(), w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["token"] != shareToken || got["path"] != "/v1/reporting/shared/"+shareToken || got["status"] != "active" {
		t.Fatalf("body %v: want the token and the download path", got)
	}
	if st.spec.LibraryID != shareLibUUID || st.spec.ExpiresInDays != 14 || st.spec.Actor != "u1" || st.spec.IdempotencyKey != "k" || len(st.spec.RequestHash) != 64 {
		t.Fatalf("spec %+v: want the default 14 days", st.spec)
	}
	first := st.spec.RequestHash

	call(r, http.MethodPost, sharePath, `{"expiresInDays":14}`, key, DutyShare)
	if st.spec.RequestHash != first {
		t.Error("asking for the default days explicitly must be the same request")
	}
	call(r, http.MethodPost, sharePath, `{"expiresInDays":30}`, key, DutyShare)
	if st.spec.ExpiresInDays != 30 || st.spec.RequestHash == first {
		t.Errorf("30 days: spec %+v", st.spec)
	}

	st.replay = true
	w = call(r, http.MethodPost, sharePath, ``, key, DutyShare)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "token") || strings.Contains(w.Body.String(), "path") {
		t.Fatalf("replay: %d %s: want 200 and no secret", w.Code, w.Body.String())
	}
	st.replay = false
	st.err = httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeShareDays, "days")
	if w := call(r, http.MethodPost, sharePath, `{"expiresInDays":0}`, key, DutyShare); w.Code != http.StatusUnprocessableEntity || problemCode(t, w) != store.CodeShareDays {
		t.Fatalf("store refusal: %d %s", w.Code, w.Body.String())
	}
}

func TestCreateShareLink_TheConfiguredLifetimeIsTheDefault(t *testing.T) {
	st := &shareStore{}
	r := chi.NewRouter()
	New(st).WithConfig(config.Reporting{ShareLinkExpiryDays: 30}).Register(r)
	call(r, http.MethodPost, sharePath, ``, map[string]string{"Idempotency-Key": "k"}, DutyShare)
	if st.spec.ExpiresInDays != 30 {
		t.Fatalf("days = %d, want REPORTING_SHARE_LINK_EXPIRY_DAYS (30)", st.spec.ExpiresInDays)
	}
}

func TestListAndRevokeShareLinks(t *testing.T) {
	st := &shareStore{}
	r := scheduleRouter(st)
	if w := call(r, http.MethodGet, sharePath, "", nil, DutyView); w.Code != http.StatusForbidden {
		t.Fatalf("list without the duty: %d", w.Code)
	}
	w := call(r, http.MethodGet, sharePath, "", nil, DutyShare)
	if w.Code != http.StatusOK || st.listedID != shareLibUUID || strings.Contains(w.Body.String(), "token") {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	path := "/v1/reporting/share-links/" + shareLinkUUID + ":revoke"
	if w := call(r, http.MethodPost, path, "", map[string]string{"If-Match": `"1"`}, DutyView); w.Code != http.StatusForbidden {
		t.Fatalf("revoke without the duty: %d", w.Code)
	}
	w = call(r, http.MethodPost, path, "", map[string]string{"If-Match": `"1"`}, DutyShare)
	if w.Code != http.StatusOK || w.Header().Get("ETag") != `"2"` || st.revID != shareLinkUUID || st.revIf == nil || *st.revIf != 1 || st.revBy != "u1" {
		t.Fatalf("revoke: %d %v id %q if %v by %q", w.Code, w.Header(), st.revID, st.revIf, st.revBy)
	}
	if w := call(r, http.MethodPost, path, "", nil, DutyShare); w.Code != http.StatusOK || st.revIf != nil {
		t.Fatalf("no If-Match reaches the store as nil (it answers 428): %d %v", w.Code, st.revIf)
	}
	for _, bad := range []string{"/v1/reporting/share-links/" + shareLinkUUID + ":delete", "/v1/reporting/share-links/" + shareLinkUUID, "/v1/reporting/share-links/nope:revoke"} {
		if w := call(r, http.MethodPost, bad, "", nil, DutyShare); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", bad, w.Code)
		}
	}
	st.err = httpx.NewProblem(http.StatusConflict, store.CodeShareRevoked, "revoked")
	if w := call(r, http.MethodPost, path, "", map[string]string{"If-Match": `"1"`}, DutyShare); w.Code != http.StatusConflict || problemCode(t, w) != store.CodeShareRevoked {
		t.Fatalf("revoked already: %d", w.Code)
	}
}

func TestSharedContent_NeedsNoIdentityAndIsNeverCached(t *testing.T) {
	st := &shareStore{entry: store.LibraryEntry{ID: shareLibUUID, FileName: "BS STD/2026.csv", ContentType: "text/csv; charset=utf-8", SHA256: strings.Repeat("ab", 32)}}
	r := New(st).PublicHandler(nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/reporting/shared/"+shareToken, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Body.String() != "a,b\n1,2\n" || st.redeemed != shareToken {
		t.Fatalf("download: %d %q token %q", w.Code, w.Body.String(), st.redeemed)
	}
	h := w.Header()
	if h.Get("Cache-Control") != "private, no-store" || h.Get("Referrer-Policy") != "no-referrer" || h.Get("X-Content-Type-Options") != "nosniff" ||
		h.Get("ETag") != `"`+strings.Repeat("ab", 32)+`"` || h.Get("X-Passage-Content-Sha256") != strings.Repeat("ab", 32) ||
		h.Get("Content-Disposition") != `attachment; filename="BS_STD_2026.csv"` || h.Get("Content-Type") != "text/csv; charset=utf-8" ||
		!strings.Contains(h.Get("Content-Security-Policy"), "sandbox") || h.Get("X-Frame-Options") != "DENY" {
		t.Fatalf("headers %v", h)
	}
	for _, c := range []struct {
		status int
		code   string
	}{{http.StatusNotFound, store.CodeShareNotFound}, {http.StatusGone, store.CodeShareGone}, {http.StatusInternalServerError, "COM-0500"}} {
		if c.status == http.StatusInternalServerError {
			st.err = context.DeadlineExceeded
		} else {
			st.err = httpx.NewProblem(c.status, c.code, "no")
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/reporting/shared/"+shareToken, nil))
		if w.Code != c.status || problemCode(t, w) != c.code || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Errorf("%d: got %d %s cache %q", c.status, w.Code, w.Body.String(), w.Header().Get("Cache-Control"))
		}
		// The problem names the route, not the caller's token.
		if strings.Contains(w.Body.String(), shareToken[33:]) || !strings.Contains(w.Body.String(), `"/v1/reporting/shared/{token}"`) {
			t.Errorf("%d: the problem echoes the token: %s", c.status, w.Body.String())
		}
	}
}

// The token is the credential and it is in the path: neither the access log,
// the download log nor the error log may contain it.
func TestSharedContent_NeverLogsTheToken(t *testing.T) {
	var logs strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(prev)

	st := &shareStore{entry: store.LibraryEntry{ID: shareLibUUID, FileName: "f.csv", ContentType: "text/csv", SHA256: strings.Repeat("ab", 32)}}
	r := New(st).PublicHandler(nil)
	for _, err := range []error{nil, httpx.NewProblem(http.StatusGone, store.CodeShareGone, "gone"), context.DeadlineExceeded} {
		st.err = err
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/reporting/shared/"+shareToken, nil))
	}
	out := logs.String()
	if strings.Contains(out, shareToken[33:]) || strings.Contains(out, shareToken[:32]) {
		t.Fatalf("the token is in the logs:\n%s", out)
	}
	if !strings.Contains(out, `"path":"/v1/reporting/shared/{token}"`) || !strings.Contains(out, `"linkId":"`+shareLinkUUID+`"`) {
		t.Fatalf("want the redacted access log and the download with its link id:\n%s", out)
	}
}

// The public handler serves the download alone: nothing else answers there,
// whatever the path, so mounting it outside the tenant middleware exposes no
// other route.
func TestPublicHandler_ServesNothingButTheDownload(t *testing.T) {
	r := New(&shareStore{}).PublicHandler(nil)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/v1/reporting/lag"},
		{http.MethodGet, "/v1/reporting/library"},
		{http.MethodGet, "/v1/reporting/shared/" + shareToken + "/x"},
		{http.MethodGet, "/v1/reporting/shared/../library"},
		{http.MethodPost, "/v1/reporting/shared/" + shareToken},
		{http.MethodGet, "/healthz"},
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(c.method, c.path, nil))
		if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 404 or 405", c.method, c.path, w.Code)
		}
	}
}

func TestSharedContent_IsNotOnTheAuthenticatedRouter(t *testing.T) {
	r := chi.NewRouter()
	New(&shareStore{}).Register(r)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/reporting/shared/"+shareToken, nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("Register must not mount the download: %d", w.Code)
	}
}
