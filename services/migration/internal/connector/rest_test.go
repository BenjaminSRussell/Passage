package connector_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/migration/internal/connector"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/egress"
)

// pretend returns a guard that treats erp.acme.test as 10.20.30.40 (allowlisted) while the bytes reach the local server.
func pretend(t *testing.T, allow string, local string) *egress.Guard {
	t.Helper()
	pol, err := egress.ParsePolicy(allow)
	if err != nil {
		t.Fatal(err)
	}
	return &egress.Guard{
		Policy:  pol,
		Timeout: 2 * time.Second,
		Resolver: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("10.20.30.40")}, nil
		},
		TestDialFunc: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, local)
		},
	}
}

func restEnv(t *testing.T, srv *httptest.Server) (connector.Env, string) {
	t.Helper()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	return connector.Env{Guard: pretend(t, "10.20.30.0/24", srv.Listener.Addr().String())}, "http://erp.acme.test:" + port
}

func restSpec(base string, source map[string]any, secret string, profile map[string]any) connector.Spec {
	p := map[string]any{"baseUrl": base, "authType": "none"}
	for k, v := range profile {
		p[k] = v
	}
	return connector.Spec{Kind: domain.ConnREST, Profile: p, Secret: secret, Source: source, KeyFields: []string{"id"}}
}

func extractAll(t *testing.T, env connector.Env, spec connector.Spec) []domain.Record {
	t.Helper()
	o, err := env.Open(spec)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer o.Close()
	e := &connector.Extractor{Bindings: map[string]connector.Binding{"customer": {
		Open: func(context.Context) (connector.Opened, error) { return o, nil }, KeyFields: spec.KeyFields, CursorField: spec.CursorField, CursorType: spec.CursorType,
	}}, PageSize: 3}
	recs, err := e.Extract(context.Background(), "customer")
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	return recs
}

func items(n int) []map[string]any {
	out := make([]map[string]any, n)
	for i := range out {
		out[i] = map[string]any{"id": i + 1, "name": "C" + strconv.Itoa(i+1), "changed": fmt.Sprintf("2026-03-%02dT10:00:00Z", i+1)}
	}
	return out
}

func TestREST_PagingStyles(t *testing.T) {
	all := items(7)
	mux := http.NewServeMux()
	slice := func(from, n int) []map[string]any { return all[min(from, len(all)):min(from+n, len(all))] }
	mux.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
		pg, _ := strconv.Atoi(r.URL.Query().Get("page"))
		n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"items": slice((pg-1)*n, n)}})
	})
	mux.HandleFunc("/offset", func(w http.ResponseWriter, r *http.Request) {
		off, _ := strconv.Atoi(r.URL.Query().Get("skip"))
		n, _ := strconv.Atoi(r.URL.Query().Get("take"))
		_ = json.NewEncoder(w).Encode(slice(off, n))
	})
	mux.HandleFunc("/cursor", func(w http.ResponseWriter, r *http.Request) {
		from, _ := strconv.Atoi(r.URL.Query().Get("after"))
		body := map[string]any{"rows": slice(from, 3)}
		if from+3 < len(all) {
			body["meta"] = map[string]any{"next": strconv.Itoa(from + 3)}
		}
		_ = json.NewEncoder(w).Encode(body)
	})
	var srv *httptest.Server
	mux.HandleFunc("/link", func(w http.ResponseWriter, r *http.Request) {
		from, _ := strconv.Atoi(r.URL.Query().Get("from"))
		if from+3 < len(all) {
			w.Header().Set("Link", fmt.Sprintf(`<http://erp.acme.test:%s/link?from=%d>; rel="next"`, strings.Split(srv.Listener.Addr().String(), ":")[1], from+3))
		}
		_ = json.NewEncoder(w).Encode(slice(from, 3))
	})
	mux.HandleFunc("/odata", func(w http.ResponseWriter, r *http.Request) {
		from, _ := strconv.Atoi(r.URL.Query().Get("from"))
		body := map[string]any{"value": slice(from, 3)}
		if from+3 < len(all) {
			body["@odata.nextLink"] = fmt.Sprintf("http://erp.acme.test:%s/odata?from=%d", strings.Split(srv.Listener.Addr().String(), ":")[1], from+3)
		}
		_ = json.NewEncoder(w).Encode(body)
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()
	env, base := restEnv(t, srv)

	cases := map[string]map[string]any{
		"page":   {"path": "/page", "recordsPath": "data.items", "paging": map[string]any{"style": "page", "pageSize": 3}},
		"offset": {"path": "/offset", "paging": map[string]any{"style": "offset", "pageSize": 3, "offsetParam": "skip", "limitParam": "take"}},
		"cursor": {"path": "/cursor", "recordsPath": "rows", "paging": map[string]any{"style": "cursor", "pageSize": 3, "cursorParam": "after", "cursorPath": "meta.next"}},
		"link":   {"path": "/link", "paging": map[string]any{"style": "link", "pageSize": 3}},
		"odata":  {"path": "/odata", "recordsPath": "value", "paging": map[string]any{"style": "odata", "pageSize": 3}},
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			recs := extractAll(t, env, restSpec(base, src, "", nil))
			if len(recs) != 7 || recs[0].Key != "1" || recs[6].Key != "7" {
				t.Fatalf("%s: got %d records %+v", name, len(recs), recs)
			}
			if recs[0].Fields["name"] != "C1" {
				t.Fatalf("fields %+v", recs[0].Fields)
			}
		})
	}
}

func TestREST_AuthSendsCredentialsInHeadersOnly(t *testing.T) {
	var gotAuth, gotKey, gotURL string
	var tokenHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		tokenHits.Add(1)
		rawID, rawSecret, _ := r.BasicAuth()
		id, _ := url.QueryUnescape(rawID) // RFC 6749 2.3.1: the id and secret are form-encoded before Basic
		secret, _ := url.QueryUnescape(rawSecret)
		_ = r.ParseForm()
		if id != "client-1" || secret != "s3cret/with+chars" || r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("scope") != "erp.read" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-abc", "token_type": "Bearer", "expires_in": 3600})
	})
	mux.HandleFunc("/c", func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotKey, gotURL = r.Header.Get("Authorization"), r.Header.Get("X-Api-Key"), r.URL.String()
		_ = json.NewEncoder(w).Encode(items(2))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	env, base := restEnv(t, srv)
	src := map[string]any{"path": "/c"}

	extractAll(t, env, restSpec(base, src, "tok-1", map[string]any{"authType": "bearer"}))
	if gotAuth != "Bearer tok-1" || strings.Contains(gotURL, "tok-1") {
		t.Fatalf("bearer: %q url %q", gotAuth, gotURL)
	}
	extractAll(t, env, restSpec(base, src, "pw", map[string]any{"authType": "basic", "username": "svc"}))
	if !strings.HasPrefix(gotAuth, "Basic ") {
		t.Fatalf("basic: %q", gotAuth)
	}
	extractAll(t, env, restSpec(base, src, "key-9", map[string]any{"authType": "header", "headerName": "X-Api-Key"}))
	if gotKey != "key-9" {
		t.Fatalf("header: %q", gotKey)
	}
	oauth := map[string]any{"authType": domain.AuthOAuth2, "authUrl": base + "/token", "clientId": "client-1", "scope": "erp.read"}
	extractAll(t, env, restSpec(base, src, "s3cret/with+chars", oauth))
	if gotAuth != "Bearer tok-abc" || tokenHits.Load() != 1 {
		t.Fatalf("oauth2: %q, token requests %d", gotAuth, tokenHits.Load())
	}

	// A wrong client secret fails without the secret in the error.
	o, err := env.Open(restSpec(base, src, "WRONG-SECRET", oauth))
	if err != nil {
		t.Fatal(err)
	}
	_, err = o.Read(context.Background(), connector.Query{Limit: 10})
	if err == nil || strings.Contains(err.Error(), "WRONG-SECRET") || !strings.Contains(err.Error(), "401") {
		t.Fatalf("wrong client secret: %v", err)
	}
	if _, err := env.Open(restSpec(base, src, "", map[string]any{"authType": "bearer"})); err == nil {
		t.Fatal("authenticated profile without a secret opened")
	}
}

func TestREST_RetriesTooManyRequestsAndRefusesRedirectsAndForeignLinks(t *testing.T) {
	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_ = json.NewEncoder(w).Encode(items(1))
	})
	mux.HandleFunc("/always", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTooManyRequests) })
	mux.HandleFunc("/redir", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/", http.StatusFound)
	})
	mux.HandleFunc("/foreign", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", `<http://169.254.169.254/steal>; rel="next"`)
		_ = json.NewEncoder(w).Encode(items(3))
	})
	mux.HandleFunc("/notjson", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<html>")) })
	mux.HandleFunc("/object", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"a":1}`)) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	env, base := restEnv(t, srv)

	var waited []time.Duration
	open := func(path string, extra map[string]any) *connector.Opened {
		src := map[string]any{"path": path}
		for k, v := range extra {
			src[k] = v
		}
		o, err := env.Open(restSpec(base, src, "", nil))
		if err != nil {
			t.Fatal(err)
		}
		o.Reader.(*connector.REST).Sleep = func(_ context.Context, d time.Duration) error { waited = append(waited, d); return nil }
		return &o
	}
	read := func(o *connector.Opened) (connector.Page, error) {
		return o.Read(context.Background(), connector.Query{Limit: 10})
	}
	if p, err := read(open("/slow", nil)); err != nil || len(p.Rows) != 1 || calls.Load() != 3 || len(waited) != 2 || waited[0] != 2*time.Second {
		t.Fatalf("429 retry: %v rows=%d calls=%d waited=%v", err, len(p.Rows), calls.Load(), waited)
	}
	if _, err := read(open("/always", nil)); err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("persistent 429: %v", err)
	}
	if _, err := read(open("/redir", nil)); err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("redirect: %v", err)
	}
	if _, err := read(open("/foreign", map[string]any{"paging": map[string]any{"style": "link"}})); err == nil || !strings.Contains(err.Error(), "another host") {
		t.Fatalf("foreign link: %v", err)
	}
	if _, err := read(open("/notjson", nil)); err == nil || !strings.Contains(err.Error(), "not JSON") {
		t.Fatalf("not json: %v", err)
	}
	if _, err := read(open("/object", nil)); err == nil || !strings.Contains(err.Error(), "recordsPath") {
		t.Fatalf("object without recordsPath: %v", err)
	}
}

func TestREST_ChangedSinceAndEgress(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.Query().Get("modified_after")
		_ = json.NewEncoder(w).Encode(items(5))
	}))
	defer srv.Close()
	env, base := restEnv(t, srv)
	spec := restSpec(base, map[string]any{"path": "/c", "changedSinceParam": "modified_after"}, "", nil)
	spec.CursorField, spec.CursorType = "changed", domain.CursorTimestamp
	o, err := env.Open(spec)
	if err != nil {
		t.Fatal(err)
	}
	p, err := o.Read(context.Background(), connector.Query{Limit: 10, Since: "2026-03-02T10:00:00.000000000Z"})
	if err != nil || seen != "2026-03-02T10:00:00.000000000Z" || len(p.Rows) != 5 || p.Watermark != "2026-03-05T10:00:00.000000000Z" {
		t.Fatalf("changed-since sent to the source: %v seen=%q rows=%d wm=%q", err, seen, len(p.Rows), p.Watermark)
	}
	// Without changedSinceParam the rows are filtered here.
	spec = restSpec(base, map[string]any{"path": "/c"}, "", nil)
	spec.CursorField, spec.CursorType = "changed", domain.CursorTimestamp
	o, _ = env.Open(spec)
	if p, err := o.Read(context.Background(), connector.Query{Limit: 10, Since: "2026-03-03T10:00:00.000000000Z"}); err != nil || len(p.Rows) != 2 {
		t.Fatalf("local delta filter: %v rows=%d", err, len(p.Rows))
	}

	// Not allowlisted: the guard refuses before any request.
	before := seen
	seen = ""
	denied := connector.Env{Guard: pretend(t, "10.99.0.0/16", srv.Listener.Addr().String())}
	o, _ = denied.Open(restSpec(base, map[string]any{"path": "/c"}, "", nil))
	if _, err := o.Read(context.Background(), connector.Query{Limit: 10}); err == nil || seen != "" {
		t.Fatalf("egress not enforced: err=%v seen=%q before=%q", err, seen, before)
	}
	if _, err := (connector.Env{}).Open(restSpec(base, map[string]any{"path": "/c"}, "", nil)); err == nil {
		t.Fatal("opened without a guard")
	}
}
