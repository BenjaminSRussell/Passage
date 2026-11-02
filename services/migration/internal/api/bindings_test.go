package api_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/migration/internal/api"
	"github.com/BenjaminSRussell/passage/services/migration/internal/connector"
	"github.com/BenjaminSRussell/passage/services/migration/internal/egress"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
)

const (
	bindPath  = "/v1/integration/entity-bindings"
	custSpec  = specMapping
	acmeFile  = `{"name":"Acme drop","kind":"file","sourceSystem":"custom_acme","config":{"dropDir":"acme","format":"csv"}}`
	csvUpload = "id,Name,Group\n1,Acme,10\n2,,10\n3,Beta,99\n"
)

type bindBody struct {
	ID                  string         `json:"id"`
	ConnectionProfileID string         `json:"connectionProfileId"`
	Entity              string         `json:"entity"`
	Source              map[string]any `json:"source"`
	KeyFields           []string       `json:"keyFields"`
	MappingSpecID       string         `json:"mappingSpecId"`
	Status              string         `json:"status"`
	Version             int            `json:"version"`
}

type previewBody struct {
	OK      bool `json:"ok"`
	Failure *struct{ Code, Message string }
	Mapping *struct {
		Entity  string
		Version int
	}
	Rows []struct {
		Index     int
		SourceKey string
		Source    map[string]any
		Target    map[string]any
		Rejects   []struct{ Field, Code, Detail string }
	}
	Columns       []string
	More          bool
	Mapped        int
	Rejected      int
	DuplicateKeys []string
}

func bindRouter(t *testing.T, s store.Store, env *connector.Env) *chi.Mux {
	t.Helper()
	factory := func(context.Context, string) (api.Backend, error) { return api.Backend{}, api.ErrNotConfigured }
	r := httpx.NewRouter()
	a := api.New(s, factory).WithConnections(testKeys(t), &recordingProber{})
	if env != nil {
		a.WithExtraction(*env)
	}
	a.Register(r)
	return r
}

func newBinding(t *testing.T, r http.Handler, payload string) (bindBody, *httptest.ResponseRecorder) {
	t.Helper()
	rec := do(r, http.MethodPost, bindPath, payload, connWrite, true)
	var b bindBody
	_ = json.Unmarshal(rec.Body.Bytes(), &b)
	return b, rec
}

func fileBindingJSON(profile string) string {
	return `{"connectionProfileId":"` + profile + `","entity":"customer","keyFields":["id"],"source":{"file":"cust.csv"}}`
}

func TestBindings_CreateReplayGetListPatchAndRejections(t *testing.T) {
	s := store.NewMem()
	r := bindRouter(t, s, nil)
	profile := createConn(t, r, acmeFile)

	rec := do(r, http.MethodPost, bindPath, fileBindingJSON(profile.ID), connWrite, true, "Idempotency-Key", "b1")
	if rec.Code != http.StatusCreated || rec.Header().Get("Location") == "" {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	var b bindBody
	_ = json.Unmarshal(rec.Body.Bytes(), &b)
	if b.Version != 1 || b.Status != "active" || b.Entity != "customer" || b.Source["file"] != "cust.csv" {
		t.Fatalf("created %+v", b)
	}
	if again := do(r, http.MethodPost, bindPath, fileBindingJSON(profile.ID), connWrite, true, "Idempotency-Key", "b1"); again.Code != http.StatusOK {
		t.Fatalf("replay = %d %s", again.Code, again.Body)
	}
	if dup := do(r, http.MethodPost, bindPath, fileBindingJSON(profile.ID), connWrite, true); dup.Code != http.StatusConflict || !strings.Contains(dup.Body.String(), store.CodeBindingExists) {
		t.Fatalf("duplicate = %d %s", dup.Code, dup.Body)
	}
	if got := do(r, http.MethodGet, bindPath+"/"+b.ID, "", connReadOnly, true); got.Code != http.StatusOK {
		t.Fatalf("get = %d", got.Code)
	}
	if missing := do(r, http.MethodGet, bindPath+"/00000000-0000-7000-8000-00000000ffff", "", connReadOnly, true); missing.Code != http.StatusNotFound {
		t.Fatalf("missing = %d", missing.Code)
	}
	list := do(r, http.MethodGet, bindPath+"?connectionProfileId="+profile.ID+"&entity=customer&limit=1", "", connReadOnly, true)
	var page struct {
		Items      []bindBody `json:"items"`
		NextCursor string     `json:"nextCursor"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &page); err != nil || len(page.Items) != 1 || page.NextCursor != "" {
		t.Fatalf("list = %d %s", list.Code, list.Body)
	}
	for _, q := range []string{"?status=gone", "?cursor=zzz", "?limit=0"} {
		if rec := do(r, http.MethodGet, bindPath+q, "", connReadOnly, true); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d", q, rec.Code)
		}
	}

	patch := func(payload string) *httptest.ResponseRecorder {
		return do(r, http.MethodPatch, bindPath+"/"+b.ID, payload, connWrite, true)
	}
	if rec := patch(`{"status":"disabled"}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"version"`) {
		t.Fatalf("patch without version = %d %s", rec.Code, rec.Body)
	}
	up := patch(`{"version":1,"source":{"filePattern":"cust_*.csv"},"keyFields":["id","company"],"cursorField":"changed","cursorType":"timestamp"}`)
	if up.Code != http.StatusOK || !strings.Contains(up.Body.String(), `"version":2`) || !strings.Contains(up.Body.String(), "cust_*.csv") {
		t.Fatalf("patch = %d %s", up.Code, up.Body)
	}
	if stale := patch(`{"version":1,"status":"disabled"}`); stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), store.CodeVersionConflict) {
		t.Fatalf("stale = %d %s", stale.Code, stale.Body)
	}
	if bad := patch(`{"version":2,"source":{"table":"x"}}`); bad.Code != http.StatusBadRequest || !strings.Contains(bad.Body.String(), "source") {
		t.Fatalf("a sql source on a file profile = %d %s", bad.Code, bad.Body)
	}
	if bad := patch(`{"version":2,"status":"paused"}`); bad.Code != http.StatusBadRequest {
		t.Fatalf("bad status = %d", bad.Code)
	}
	if rec := do(r, http.MethodPatch, bindPath+"/"+b.ID, `{"version":2,"status":"disabled"}`, connReadOnly, true); rec.Code != http.StatusForbidden {
		t.Fatalf("patch without the write duty = %d", rec.Code)
	}
}

func TestBindings_CreateValidationAndDuties(t *testing.T) {
	r := bindRouter(t, store.NewMem(), nil)
	file := createConn(t, r, acmeFile)
	sqlConn := createConn(t, r, acmeSQL)
	_, custSpecRow := publish(t, r, custSpec)
	cases := []struct {
		name, payload, duties string
		tenant                bool
		want                  int
		contains              string
	}{
		{"no principal", fileBindingJSON(file.ID), connWrite, false, 401, "AUTH-0001"},
		{"read duty cannot create", fileBindingJSON(file.ID), connReadOnly, true, 403, "AUTH-0003"},
		{"unknown field", `{"connectionProfileId":"` + file.ID + `","entity":"customer","keyFields":["id"],"source":{"file":"a.csv"},"x":1}`, connWrite, true, 400, "COM-0001"},
		{"no profile", `{"entity":"customer","keyFields":["id"],"source":{"file":"a.csv"}}`, connWrite, true, 400, "connectionProfileId"},
		{"unknown profile", `{"connectionProfileId":"00000000-0000-7000-8000-00000000ffff","entity":"customer","keyFields":["id"],"source":{"file":"a.csv"}}`, connWrite, true, 422, store.CodeConnectionUnknown},
		{"bad entity", `{"connectionProfileId":"` + file.ID + `","entity":"Customer!","keyFields":["id"],"source":{"file":"a.csv"}}`, connWrite, true, 400, `"entity"`},
		{"no keys", `{"connectionProfileId":"` + file.ID + `","entity":"customer","keyFields":[],"source":{"file":"a.csv"}}`, connWrite, true, 400, `"keyFields"`},
		{"table on a file profile", `{"connectionProfileId":"` + file.ID + `","entity":"customer","keyFields":["id"],"source":{"table":"t"}}`, connWrite, true, 400, `"source"`},
		{"write query", `{"connectionProfileId":"` + sqlConn.ID + `","entity":"customer","keyFields":["id"],"source":{"query":"delete from t"}}`, connWrite, true, 400, "source.query"},
		{"two statements", `{"connectionProfileId":"` + sqlConn.ID + `","entity":"customer","keyFields":["id"],"source":{"query":"select 1; drop table t"}}`, connWrite, true, 400, "source.query"},
		{"spec id not a UUID", `{"connectionProfileId":"` + sqlConn.ID + `","entity":"customer","keyFields":["id"],"source":{"table":"t"},"mappingSpecId":"spec-1"}`, connWrite, true, 400, `"mappingSpecId":"must be a UUID"`},
		{"spec for another entity", `{"connectionProfileId":"` + sqlConn.ID + `","entity":"vendor","keyFields":["id"],"source":{"table":"t"},"mappingSpecId":"` + custSpecRow.ID + `"}`, connWrite, true, 400, "is a mapping for entity customer"},
		{"unknown spec", `{"connectionProfileId":"` + sqlConn.ID + `","entity":"customer","keyFields":["id"],"source":{"table":"t"},"mappingSpecId":"00000000-0000-7000-8000-00000000ffff"}`, connWrite, true, 422, "MIG-SPEC-UNKNOWN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(r, http.MethodPost, bindPath, tc.payload, tc.duties, tc.tenant)
			if rec.Code != tc.want || !strings.Contains(rec.Body.String(), tc.contains) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body, tc.want, tc.contains)
			}
		})
	}
	// The same checks hold when a binding's default spec is changed.
	b, rec := newBinding(t, r, `{"connectionProfileId":"`+sqlConn.ID+`","entity":"vendor","keyFields":["id"],"source":{"table":"v"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatal(rec.Body)
	}
	for payload, want := range map[string]string{
		`{"version":1,"mappingSpecId":"spec-1"}`:                 "must be a UUID",
		`{"version":1,"mappingSpecId":"` + custSpecRow.ID + `"}`: "is a mapping for entity customer",
	} {
		if rec := do(r, http.MethodPatch, bindPath+"/"+b.ID, payload, connWrite, true); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("patch %s = %d %s", payload, rec.Code, rec.Body)
		}
	}
	if rec := do(r, http.MethodPost, bindPath, fileBindingJSON(file.ID), connWrite, true, "X-Passage-Legal-Entity", testkit.TenantID(7)); rec.Code != http.StatusForbidden {
		t.Fatalf("legal-entity-scoped caller = %d", rec.Code)
	}
	// A disabled profile takes no new binding.
	if rec := do(r, http.MethodPatch, connPath+"/"+file.ID, `{"version":1,"status":"disabled"}`, connWrite, true); rec.Code != http.StatusOK {
		t.Fatal(rec.Body)
	}
	if rec := do(r, http.MethodPost, bindPath, fileBindingJSON(file.ID), connWrite, true); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), api.CodeProfileDisabled) {
		t.Fatalf("disabled profile = %d %s", rec.Code, rec.Body)
	}
}

func decodePreview(t *testing.T, rec *httptest.ResponseRecorder) previewBody {
	t.Helper()
	var p previewBody
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	return p
}

func TestBindings_PreviewAnUploadThroughAPublishedSpec(t *testing.T) {
	s := store.NewMem()
	r := bindRouter(t, s, &connector.Env{})
	_, spec := publish(t, r, custSpec)
	profile := createConn(t, r, acmeFile)
	b, rec := newBinding(t, r, `{"connectionProfileId":"`+profile.ID+`","entity":"customer","keyFields":["id"],"source":{"file":"cust.csv"},"mappingSpecId":"`+spec.ID+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatal(rec.Body)
	}
	up := `"upload":{"name":"cust.csv","contentBase64":"` + base64.StdEncoding.EncodeToString([]byte(csvUpload)) + `"}`

	rec = do(r, http.MethodPost, bindPath+"/"+b.ID+":preview", `{"limit":10,`+up+`}`, connWrite, true)
	p := decodePreview(t, rec)
	if rec.Code != http.StatusOK || !p.OK || len(p.Rows) != 3 || p.Mapped != 1 || p.Rejected != 2 || p.Mapping == nil || p.Mapping.Entity != "customer" || p.More {
		t.Fatalf("preview = %d %s", rec.Code, rec.Body)
	}
	if got := p.Rows[0]; got.SourceKey != "1" || got.Target["name"] != "Acme" || got.Target["segment"] != "retail" {
		t.Fatalf("first row %+v", got)
	}
	if got := p.Rows[1]; got.Target != nil || len(got.Rejects) == 0 || got.Rejects[0].Field != "name" {
		t.Fatalf("a row missing its required name: %+v", got)
	}
	if strings.Join(p.Columns, ",") != "Group,Name,id" {
		t.Fatalf("columns %v", p.Columns)
	}
	// The preview loads nothing: no run, no xref.
	if runs, _ := s.ListRuns(context.Background(), store.RunFilter{}); len(runs) != 0 {
		t.Fatalf("a preview wrote %d runs", len(runs))
	}

	// The limit holds and says there is more.
	rec = do(r, http.MethodPost, bindPath+"/"+b.ID+":preview", `{"limit":2,`+up+`}`, connWrite, true)
	if p := decodePreview(t, rec); len(p.Rows) != 2 || !p.More {
		t.Fatalf("limit: %s", rec.Body)
	}
	// An inline mapping replaces the spec; one for another entity is refused; failing mapping tests are 422.
	vendor := strings.Replace(custSpec, `"entity":"customer"`, `"entity":"vendor"`, 1)
	if rec := do(r, http.MethodPost, bindPath+"/"+b.ID+":preview", `{"mapping":`+vendor+`,`+up+`}`, connWrite, true); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "vendor") {
		t.Fatalf("other entity = %d %s", rec.Code, rec.Body)
	}
	failing := strings.Replace(custSpec, `"expect":{"name":"Acme"`, `"expect":{"name":"Nope"`, 1)
	if rec := do(r, http.MethodPost, bindPath+"/"+b.ID+":preview", `{"mapping":`+failing+`,`+up+`}`, connWrite, true); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("failing tests = %d %s", rec.Code, rec.Body)
	}
	// Without any mapping only the source rows show.
	rec = do(r, http.MethodPost, bindPath+"/"+b.ID+":preview", `{"mapping":null,"mappingSpecId":"",`+up+`}`, connWrite, true)
	if p := decodePreview(t, rec); !p.OK || p.Mapping == nil {
		// the binding's own spec still applies when the request names none
		t.Fatalf("default spec: %s", rec.Body)
	}

	for name, body := range map[string]string{
		"limit too big":   `{"limit":101,` + up + `}`,
		"bad base64":      `{"upload":{"name":"a.csv","contentBase64":"!!!"}}`,
		"path in name":    `{"upload":{"name":"../a.csv","contentBase64":"QQ=="}}`,
		"binding fields":  `{"entity":"vendor"}`,
		"unknown field":   `{"nope":1}`,
		"upload no bytes": `{"upload":{"name":"a.csv","contentBase64":""}}`,
	} {
		if rec := do(r, http.MethodPost, bindPath+"/"+b.ID+":preview", body, connWrite, true); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := do(r, http.MethodPost, bindPath+"/"+b.ID+":preview", `{}`, connReadOnly, true); rec.Code != http.StatusForbidden {
		t.Fatalf("preview without the write duty = %d", rec.Code)
	}
	// A file that is not CSV is a clean ok=false, not a 500.
	bad := `{"upload":{"name":"cust.csv","contentBase64":"` + base64.StdEncoding.EncodeToString([]byte("id,id\n1,2\n")) + `"}}`
	rec = do(r, http.MethodPost, bindPath+"/"+b.ID+":preview", bad, connWrite, true)
	if p := decodePreview(t, rec); rec.Code != http.StatusOK || p.OK || p.Failure == nil || !strings.Contains(p.Failure.Message, "repeats column") {
		t.Fatalf("bad file = %d %s", rec.Code, rec.Body)
	}
	// A disabled binding is not previewed.
	do(r, http.MethodPatch, bindPath+"/"+b.ID, `{"version":1,"status":"disabled"}`, connWrite, true)
	if rec := do(r, http.MethodPost, bindPath+"/"+b.ID+":preview", `{`+up+`}`, connWrite, true); rec.Code != http.StatusConflict {
		t.Fatalf("disabled binding = %d %s", rec.Code, rec.Body)
	}
}

func TestBindings_PreviewIsNotConfiguredWithoutExtraction(t *testing.T) {
	r := bindRouter(t, store.NewMem(), nil)
	profile := createConn(t, r, acmeFile)
	b, _ := newBinding(t, r, fileBindingJSON(profile.ID))
	rec := do(r, http.MethodPost, bindPath+"/"+b.ID+":preview", `{}`, connWrite, true)
	if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), api.CodeExtractNotConfig) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

// A draft preview checks a binding before it is saved; a rest preview opens the profile's sealed secret, sends it as a
// header only, goes through the egress guard, and never lets the secret reach a response or a log.
func TestBindings_DraftPreviewOfARestEndpointWithASealedSecret(t *testing.T) {
	logs := captureLogs(t)
	var gotAuth string
	mode := "ok"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if mode == "denied" {
			http.Error(w, "bad token "+sentinel, http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":1,"Name":"Acme","Group":"10"},{"id":2,"Name":"Beta","Group":"10"}]}`))
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	pol, _ := egress.ParsePolicy("10.20.30.0/24")
	guard := &egress.Guard{Policy: pol, Timeout: 2 * time.Second, TLS: &tls.Config{RootCAs: pool, ServerName: "example.com"},
		Resolver: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("10.20.30.40")}, nil
		},
		TestDialFunc: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
		}}
	r := bindRouter(t, store.NewMem(), &connector.Env{Guard: guard})
	base := "https://example.com:" + port
	open := createConn(t, r, `{"name":"Acme open","kind":"rest","sourceSystem":"custom_acme","config":{"baseUrl":"`+base+`","authType":"none"}}`)
	draft := `{"connectionProfileId":"` + open.ID + `","entity":"customer","keyFields":["id"],"source":{"path":"/c","recordsPath":"data"},"mapping":` + custSpec + `}`

	rec := do(r, http.MethodPost, bindPath+":preview", draft, connWrite, true)
	p := decodePreview(t, rec)
	if rec.Code != http.StatusOK || !p.OK || len(p.Rows) != 2 || p.Mapped != 2 || p.Rows[1].Target["segment"] != "retail" {
		t.Fatalf("draft preview = %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodPost, bindPath+":preview", strings.Replace(draft, `"/c"`, `"c"`, 1), connWrite, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid draft = %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodPost, bindPath+":preview", draft, connReadOnly, true); rec.Code != http.StatusForbidden {
		t.Fatalf("draft preview without the write duty = %d", rec.Code)
	}

	// An authenticated profile: the secret is opened and sent in a header, and kept out of failures and logs.
	secured := createConn(t, r, `{"name":"Acme secured","kind":"rest","sourceSystem":"custom_acme","config":{"baseUrl":"`+base+`","authType":"bearer"}}`)
	securedDraft := strings.Replace(draft, open.ID, secured.ID, 1)
	if rec := do(r, http.MethodPost, bindPath+":preview", securedDraft, connWrite, true); rec.Code != http.StatusOK || decodePreview(t, rec).OK {
		t.Fatalf("a profile without its secret must not preview: %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodPut, connPath+"/"+secured.ID+"/secret", `{"secret":"`+sentinel+`"}`, connWrite, true); rec.Code != http.StatusOK {
		t.Fatal(rec.Body)
	}
	if rec := do(r, http.MethodPost, bindPath+":preview", securedDraft, connWrite, true); !decodePreview(t, rec).OK || gotAuth != "Bearer "+sentinel {
		t.Fatalf("secured preview: %s auth=%q", rec.Body, gotAuth)
	}
	mode = "denied"
	rec = do(r, http.MethodPost, bindPath+":preview", securedDraft, connWrite, true)
	p = decodePreview(t, rec)
	if rec.Code != http.StatusOK || p.OK || p.Failure == nil || !strings.Contains(p.Failure.Message, "401") || strings.Contains(rec.Body.String(), sentinel) || strings.Contains(logs.String(), sentinel) {
		t.Fatalf("failed preview = %d %s", rec.Code, rec.Body)
	}
}
