package api_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/api"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
)

type reauthFunc func(context.Context, string, domain.Reauth) error

func (f reauthFunc) Verify(ctx context.Context, actor string, auth domain.Reauth) error {
	return f(ctx, actor, auth)
}

type recordingAuditor struct {
	sigs []domain.Signature
	err  error
	ctx  context.Context
}

func (r *recordingAuditor) RecordSignature(ctx context.Context, sig domain.Signature) error {
	r.ctx = ctx
	if r.err != nil {
		return r.err
	}
	r.sigs = append(r.sigs, sig)
	return nil
}

func TestCreateOrganization(t *testing.T) {
	r, _ := newRouter(&fakeStore{}, approves)
	rec := do(r, http.MethodPost, "/v1/engineering/organizations", `{"legalEntityId":"ENG1","code":"ENG","name":"Engineering"}`, allDuties, true)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"code":"ENG"`) {
		t.Fatalf("create org = %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(r, http.MethodPost, "/v1/engineering/organizations", `{"legalEntityId":"ENG1","code":"ENG","name":"Engineering"}`, "engineering.read", true); rec.Code != http.StatusForbidden {
		t.Fatalf("missing write duty = %d", rec.Code)
	}
}

func TestNomenclatureNextOverflow(t *testing.T) {
	f := &fakeStore{wave: &wave09State{nomen: domain.Nomenclature{ID: ecoID, Code: "VNN", Separator: "-", Segments: []domain.NomenSegment{{Type: domain.SegConstant, Text: "V"}, {Type: domain.SegNumber, Width: 2}}}}}
	r, _ := newRouter(f, approves)
	rec := do(r, http.MethodGet, "/v1/engineering/nomenclatures/"+ecoID+":next?last=V-99", "", allDuties, true)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), domain.CodeNomenOverflow) {
		t.Fatalf("overflow = %d %s", rec.Code, rec.Body.String())
	}
}

const signedDocID = ecoID

func signableStore() *fakeStore {
	return &fakeStore{wave: &wave09State{
		docType: domain.DocumentType{Code: "DRW", Name: "Drawing", SignatureOnRelease: true, IndependentReview: true, SignatureMeaning: "Approved for release", Network: domain.DefaultStatusNetwork()},
		doc:     domain.Document{ID: signedDocID, TypeCode: "DRW", Number: "DRW-1", Version: "01", Status: domain.DocInReview, LastEditor: "rita", Originals: []domain.DocumentOriginal{{StorageKey: "a", SHA256: "aaa"}}},
	}}
}

func signRouter(f *fakeStore, reauth api.Reauthenticator) http.Handler {
	a := api.New(f, approves, func() time.Time { return today })
	if reauth != nil {
		a.WithReauth(reauth)
	}
	r := httpx.NewRouter()
	a.Register(r)
	return r
}

func TestSignDocument_DefaultReauthenticatorFailsClosed(t *testing.T) {
	f := signableStore()
	r := signRouter(f, nil)
	rec := do(r, http.MethodPost, "/v1/engineering/documents/"+signedDocID+":sign", `{"printedName":"Carol Kane"}`, allDuties, true, "X-Passage-User", "carol", "X-Passage-Reauth-Assertion", "anything")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), domain.CodeDocReauthUnavailable) {
		t.Fatalf("default sign = %d %s; want 503 %s", rec.Code, rec.Body.String(), domain.CodeDocReauthUnavailable)
	}
	if f.w().transits != 0 || len(f.w().sigs) != 0 {
		t.Fatalf("a refused re-authentication must write nothing: %+v", f.w())
	}
}

func TestSignDocument_ReauthIsBoundToTheRecord(t *testing.T) {
	f := signableStore()
	var seen domain.Reauth
	r := signRouter(f, reauthFunc(func(_ context.Context, actor string, auth domain.Reauth) error {
		seen = auth
		if auth.Assertion != "step-up-token" || auth.UserID != actor {
			return &domain.Error{Code: domain.CodeDocReauth, Detail: "no step-up"}
		}
		return nil
	}))
	path := "/v1/engineering/documents/" + signedDocID + ":sign"
	if rec := do(r, http.MethodPost, path, `{"printedName":"Carol Kane"}`, allDuties, true, "X-Passage-User", "carol"); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), domain.CodeDocReauth) {
		t.Fatalf("missing proof = %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(r, http.MethodPost, path, `{}`, "engineering.read,engineering.bom.write", true, "X-Passage-User", "carol", "X-Passage-Reauth-Assertion", "step-up-token"); rec.Code != http.StatusForbidden {
		t.Fatalf("missing sign duty = %d %s", rec.Code, rec.Body.String())
	}
	rec := do(r, http.MethodPost, path, `{"printedName":"Carol Kane","method":"password"}`, allDuties, true, "X-Passage-User", "carol", "X-Passage-Reauth-Assertion", "step-up-token")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"released"`) {
		t.Fatalf("sign = %d %s", rec.Code, rec.Body.String())
	}
	if seen.DocumentID != signedDocID || seen.RecordHash != domain.HashRecord(f.w().doc) {
		t.Fatalf("proof not bound to the record: %+v", seen)
	}
	sigs := f.w().sigs
	if len(sigs) != 1 || sigs[0].SignerID != "carol" || sigs[0].PrintedName != "Carol Kane" || sigs[0].Meaning != "Approved for release" || sigs[0].RecordID != signedDocID || sigs[0].OriginalsHash != seen.RecordHash {
		t.Fatalf("stored signatures = %+v; want one bound to the verified hash %s", sigs, seen.RecordHash)
	}
}

func TestSignDocument_OnlySignatureTransitions(t *testing.T) {
	f := signableStore()
	r := signRouter(f, reauthFunc(func(context.Context, string, domain.Reauth) error { return nil }))
	path := "/v1/engineering/documents/" + signedDocID + ":sign"
	for _, to := range []string{"cancelled", "in_work", "obsolete", "in_review"} {
		rec := do(r, http.MethodPost, path, `{"to":"`+to+`"}`, allDuties, true, "X-Passage-User", "carol")
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), domain.CodeDocNotSignable) {
			t.Fatalf("sign to %s = %d %s; want 422 %s", to, rec.Code, rec.Body.String(), domain.CodeDocNotSignable)
		}
	}
	if f.w().transits != 0 {
		t.Fatalf("a refused sign must not transition: %d", f.w().transits)
	}
	f.w().doc.Originals = nil
	if rec := do(r, http.MethodPost, path, `{}`, allDuties, true, "X-Passage-User", "carol"); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), domain.CodeDocNoOriginals) {
		t.Fatalf("sign without originals = %d %s", rec.Code, rec.Body.String())
	}
}

func TestAuditRelay_RetriesWithBackoffThenDelivers(t *testing.T) {
	sig := domain.Signature{ID: "s1", RecordID: "d1", SignerID: "carol"}
	f := &fakeStore{wave: &wave09State{audit: []store.AuditJob{{ID: "s1", TenantID: testkit.TenantID(1), UserID: "carol", Signature: sig}}}}
	aud := &recordingAuditor{err: errors.New("audit down")}
	relay := api.NewAuditRelay(f, aud)
	if n, err := relay.Drain(context.Background()); err != nil || n != 0 {
		t.Fatalf("failing delivery = %d, %v", n, err)
	}
	if f.w().failed["s1"] != 1 || f.w().retryIn != 10*time.Second || len(f.w().completed) != 0 {
		t.Fatalf("after first failure: attempts=%v retry=%s completed=%v", f.w().failed, f.w().retryIn, f.w().completed)
	}
	p, ok := tenant.FromContext(aud.ctx)
	if !ok || p.TenantID != testkit.TenantID(1) || p.UserID != "carol" {
		t.Fatalf("delivery context principal = %+v, %v", p, ok)
	}
	f.w().audit = []store.AuditJob{{ID: "s1", Attempts: 29, TenantID: testkit.TenantID(1), Signature: sig}}
	if _, err := relay.Drain(context.Background()); err != nil || f.w().retryIn != 15*time.Minute {
		t.Fatalf("backoff after 30 failures = %s, %v; want the 15 minute cap", f.w().retryIn, err)
	}
	aud.err = nil
	f.w().audit = []store.AuditJob{{ID: "s1", Attempts: 1, TenantID: testkit.TenantID(1), Signature: sig}}
	if n, err := relay.Drain(context.Background()); err != nil || n != 1 || len(aud.sigs) != 1 || len(f.w().completed) != 1 {
		t.Fatalf("recovered delivery = %d, %v, sigs=%d completed=%v", n, err, len(aud.sigs), f.w().completed)
	}
}

func TestImportEbom(t *testing.T) {
	r, _ := newRouter(&fakeStore{}, approves)
	body := `{"source":"cad","partNumber":"BIKE","revision":"A","lines":[{"findNumber":10,"partNumber":"WHEEL","quantity":"1"}]}`
	rec := do(r, http.MethodPost, "/v1/engineering/ebom-imports", body, allDuties, true, "X-Passage-User", "rita")
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"partNumber":"BIKE"`) {
		t.Fatalf("import = %d %s", rec.Code, rec.Body.String())
	}
}

func TestAcceptReleaseLineDuty(t *testing.T) {
	lineID := ecoID
	f := &fakeStore{wave: &wave09State{line: domain.ReleaseLine{ID: lineID, Status: domain.LineSent, LegalEntityID: "USMF"}}}
	r, _ := newRouter(f, approves)
	if rec := do(r, http.MethodPost, "/v1/engineering/release-lines/"+lineID+":accept", `{}`, "engineering.read,engineering.bom.write", true, "X-Passage-User", "bob"); rec.Code != http.StatusForbidden {
		t.Fatalf("missing accept duty = %d %s", rec.Code, rec.Body.String())
	}
	rec := do(r, http.MethodPost, "/v1/engineering/release-lines/"+lineID+":accept", `{}`, allDuties, true, "X-Passage-User", "bob")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"accepted"`) {
		t.Fatalf("accept = %d %s", rec.Code, rec.Body.String())
	}
}

func TestProcessPolicy(t *testing.T) {
	f := &fakeStore{wave: &wave09State{released: store.ReleasedProduct{ID: ecoID, ProductID: "P", State: domain.ReadinessPending}}}
	r, _ := newRouter(f, approves)
	rec := do(r, http.MethodGet, "/v1/engineering/released-products/"+ecoID+":process-policy?process=sales_order", "", allDuties, true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), domain.CodeLifecycleBlocked) {
		t.Fatalf("process policy = %d %s", rec.Code, rec.Body.String())
	}
}

func TestCreateEco_DocumentOnly(t *testing.T) {
	r, _ := newRouter(&fakeStore{}, approves)
	body := `{"number":"ECO-DOC","documents":[{"oldDocumentId":"` + oldVersion + `","newDocumentId":"` + newVersion + `"}]}`
	rec := do(r, http.MethodPost, ecos, body, allDuties, true, "X-Passage-User", "rita")
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"number":"ECO-DOC"`) {
		t.Fatalf("document-only eco = %d %s", rec.Code, rec.Body.String())
	}
}

func TestHTTPAuditorPostsChange(t *testing.T) {
	var gotPath, gotBody, gotToken string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotToken = r.URL.Path, r.Header.Get(tenant.HeaderServiceToken)
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(hs.Close)
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner("engineering", key)
	if err != nil {
		t.Fatal(err)
	}
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "carol", LegalEntityID: "00000000-0000-0000-0000-0000000000e1"})
	sig := domain.Signature{ID: "s1", RecordID: "d1", DocumentNumber: "DRW-1", DocumentVersion: "02", Transition: "released", SignerID: "carol", PrintedName: "Carol Kane", Meaning: "Approved for release", OriginalsHash: "abc123", SignedAt: time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)}
	if err := api.NewHTTPAuditor(hs.URL, signer).RecordSignature(ctx, sig); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"entity":"document"`, `"recordId":"d1"`, "Approved for release", "Carol Kane", "abc123", "DRW-1"} {
		if !strings.Contains(gotBody, want) {
			t.Fatalf("audit body %s lacks %q", gotBody, want)
		}
	}
	if gotPath != "/v1/audit/changes" || gotToken == "" {
		t.Fatalf("path=%s token=%q; want a signed call", gotPath, gotToken)
	}
}

func TestHTTPAuditorWithoutSignerRefusesInsteadOfSendingRawHeaders(t *testing.T) {
	called := false
	hs := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	t.Cleanup(hs.Close)
	err := api.NewHTTPAuditor(hs.URL, nil).RecordSignature(context.Background(), domain.Signature{ID: "s1"})
	if err == nil || called {
		t.Fatalf("unsigned audit call: err=%v called=%v; want an error and no request", err, called)
	}
}

func TestCreateDocument_RejectsClientEcoAndUnverifiableOriginals(t *testing.T) {
	f := &fakeStore{}
	r, _ := newRouter(f, approves)
	good := testkit.TenantID(1) + "/drawings/a.pdf"
	sha := strings.Repeat("a", 64)
	cases := map[string]string{
		"client eco":     `{"typeCode":"DRW","number":"N-1","ecoId":"` + ecoID + `"}`,
		"foreign tenant": `{"typeCode":"DRW","number":"N-1","originals":[{"storageKey":"` + testkit.TenantID(2) + `/a.pdf","sha256":"` + sha + `"}]}`,
		"path traversal": `{"typeCode":"DRW","number":"N-1","originals":[{"storageKey":"` + testkit.TenantID(1) + `/../x","sha256":"` + sha + `"}]}`,
		"hash not hex":   `{"typeCode":"DRW","number":"N-1","originals":[{"storageKey":"` + good + `","sha256":"nothex"}]}`,
		"duplicate key":  `{"typeCode":"DRW","number":"N-1","originals":[{"storageKey":"` + good + `","sha256":"` + sha + `"},{"storageKey":"` + good + `","sha256":"` + sha + `"}]}`,
		"negative size":  `{"typeCode":"DRW","number":"N-1","originals":[{"storageKey":"` + good + `","sha256":"` + sha + `","sizeBytes":-1}]}`,
	}
	for name, body := range cases {
		if rec := do(r, http.MethodPost, "/v1/engineering/documents", body, allDuties, true, "X-Passage-User", "rita"); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d %s; want 400", name, rec.Code, rec.Body.String())
		}
	}
	ok := `{"typeCode":"DRW","number":"N-1","originals":[{"storageKey":"` + good + `","sha256":"` + sha + `","sizeBytes":10}]}`
	if rec := do(r, http.MethodPost, "/v1/engineering/documents", ok, allDuties, true, "X-Passage-User", "rita"); rec.Code != http.StatusCreated {
		t.Fatalf("valid document = %d %s", rec.Code, rec.Body.String())
	}
	if f.w().doc.LastEditor != "rita" {
		t.Fatalf("last editor = %q; want the acting user", f.w().doc.LastEditor)
	}
}

func TestSetDocumentStructure_ValidatesChildren(t *testing.T) {
	r, _ := newRouter(&fakeStore{}, approves)
	path := "/v1/engineering/documents/" + ecoID + "/structure"
	for name, body := range map[string]string{
		"not a uuid": `{"children":["nope"]}`,
		"duplicate":  `{"children":["` + oldVersion + `","` + oldVersion + `"]}`,
	} {
		if rec := do(r, http.MethodPut, path, body, allDuties, true); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d %s; want 400", name, rec.Code, rec.Body.String())
		}
	}
	if rec := do(r, http.MethodPut, path, `{"children":["`+oldVersion+`"]}`, allDuties, true); rec.Code != http.StatusOK {
		t.Fatalf("valid structure = %d %s", rec.Code, rec.Body.String())
	}
}

func TestCreateReleaseSession_CreatorIsThePrincipal(t *testing.T) {
	f := &fakeStore{}
	r, _ := newRouter(f, approves)
	body := func(extra string) string {
		return `{"policyId":"` + ecoID + `","engineeringLeId":"ENG1","productId":"P","versionCode":"V-01","legalEntities":["USMF"]` + extra + `}`
	}
	if rec := do(r, http.MethodPost, "/v1/engineering/release-sessions", body(`,"createdBy":"someone-else"`), allDuties, true, "X-Passage-User", "rita"); rec.Code != http.StatusBadRequest {
		t.Fatalf("body createdBy = %d %s; want 400", rec.Code, rec.Body.String())
	}
	rec := do(r, http.MethodPost, "/v1/engineering/release-sessions", body(""), allDuties, true, "X-Passage-User", "rita")
	if rec.Code != http.StatusCreated || f.w().sessionIn.CreatedBy != "rita" || f.w().sessionIn.EngineeringLEID != "ENG1" {
		t.Fatalf("create = %d %s; input %+v", rec.Code, rec.Body.String(), f.w().sessionIn)
	}
	for name, bad := range map[string]string{
		"no engineering entity": `{"policyId":"` + ecoID + `","productId":"P","versionCode":"V-01","legalEntities":["USMF"]}`,
		"duplicate entities":    `{"policyId":"` + ecoID + `","engineeringLeId":"ENG1","productId":"P","versionCode":"V-01","legalEntities":["USMF","USMF"]}`,
		"unknown source":        `{"policyId":"` + ecoID + `","engineeringLeId":"ENG1","productId":"P","versionCode":"V-01","legalEntities":["USMF"],"source":"cron"}`,
	} {
		if rec := do(r, http.MethodPost, "/v1/engineering/release-sessions", bad, allDuties, true, "X-Passage-User", "rita"); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d %s; want 400", name, rec.Code, rec.Body.String())
		}
	}
	if rec := do(r, http.MethodPost, "/v1/engineering/release-sessions", body(""), allDuties, true); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no acting user = %d; want 401", rec.Code)
	}
}

func TestImportEbom_ValidatesLines(t *testing.T) {
	r, _ := newRouter(&fakeStore{}, approves)
	post := func(lines string) int {
		body := `{"source":"cad","partNumber":"BIKE","revision":"A","lines":` + lines + `}`
		return do(r, http.MethodPost, "/v1/engineering/ebom-imports", body, allDuties, true, "X-Passage-User", "rita").Code
	}
	for name, lines := range map[string]string{
		"negative quantity": `[{"findNumber":10,"partNumber":"W","quantity":"-2"}]`,
		"zero quantity":     `[{"findNumber":10,"partNumber":"W","quantity":"0"}]`,
		"missing quantity":  `[{"findNumber":10,"partNumber":"W"}]`,
		"duplicate find":    `[{"findNumber":10,"partNumber":"W","quantity":"1"},{"findNumber":10,"partNumber":"X","quantity":"1"}]`,
		"assembly as line":  `[{"findNumber":10,"partNumber":"BIKE","quantity":"1"}]`,
		"negative find":     `[{"findNumber":-1,"partNumber":"W","quantity":"1"}]`,
		"no lines":          `[]`,
	} {
		if code := post(lines); code != http.StatusBadRequest {
			t.Fatalf("%s = %d; want 400", name, code)
		}
	}
	var many strings.Builder
	many.WriteString("[")
	for i := 0; i < 5001; i++ {
		if i > 0 {
			many.WriteString(",")
		}
		many.WriteString(`{"partNumber":"P` + strconv.Itoa(i) + `","quantity":"1"}`)
	}
	many.WriteString("]")
	if code := post(many.String()); code != http.StatusBadRequest {
		t.Fatalf("5001 lines = %d; want the line cap", code)
	}
	if code := post(`[{"findNumber":10,"partNumber":"W","quantity":"1"},{"partNumber":"X","quantity":"2.5"}]`); code != http.StatusCreated {
		t.Fatalf("valid import = %d", code)
	}
}
