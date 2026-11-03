package api_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/api"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
)

type recordingAuditor struct{ sigs []domain.Signature }

func (r *recordingAuditor) RecordSignature(_ context.Context, sig domain.Signature) error {
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

func TestSignDocument_ReauthAndAudit(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	docID := ecoID
	f := &fakeStore{wave: &wave09State{
		docType: domain.DocumentType{Code: "DRW", Name: "Drawing", SignatureOnRelease: true, IndependentReview: true, SignatureMeaning: "Approved for release", Network: domain.DefaultStatusNetwork()},
		doc:     domain.Document{ID: docID, TypeCode: "DRW", Number: "DRW-1", Version: "01", Status: domain.DocInReview, LastEditor: "rita", Originals: []domain.DocumentOriginal{{StorageKey: "a", SHA256: "aaa"}}},
	}}
	aud := &recordingAuditor{}
	a := api.New(f, approves, func() time.Time { return now }).WithAuditor(aud)
	r := httpx.NewRouter()
	a.Register(r)
	if rec := do(r, http.MethodPost, "/v1/engineering/documents/"+docID+":sign", `{"printedName":"Carol Kane"}`, allDuties, true, "X-Passage-User", "carol"); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), domain.CodeDocReauth) {
		t.Fatalf("missing reauth = %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(r, http.MethodPost, "/v1/engineering/documents/"+docID+":sign", `{"printedName":"Carol Kane"}`, "engineering.read,engineering.bom.write", true, "X-Passage-User", "carol", "X-Passage-Reauth-Assertion", "ok"); rec.Code != http.StatusForbidden {
		t.Fatalf("missing sign duty = %d %s", rec.Code, rec.Body.String())
	}
	rec := do(r, http.MethodPost, "/v1/engineering/documents/"+docID+":sign", `{"printedName":"Carol Kane","method":"password"}`, allDuties, true, "X-Passage-User", "carol", "X-Passage-Reauth-Assertion", "ok")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"released"`) {
		t.Fatalf("sign = %d %s", rec.Code, rec.Body.String())
	}
	if len(aud.sigs) != 1 || aud.sigs[0].SignerID != "carol" || aud.sigs[0].Meaning != "Approved for release" || aud.sigs[0].RecordID != docID || aud.sigs[0].SignedAt.IsZero() {
		t.Fatalf("audit = %+v", aud.sigs)
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
	var gotPath, gotBody string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(hs.Close)
	aud := api.NewHTTPAuditor(hs.URL, nil)
	err := aud.RecordSignature(context.Background(), domain.Signature{ID: "s1", RecordID: "d1", SignerID: "carol", Meaning: "Approved for release", SignedAt: time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)})
	if err != nil || gotPath != "/v1/audit/changes" || !strings.Contains(gotBody, `"entity":"document"`) || !strings.Contains(gotBody, `"recordId":"d1"`) || !strings.Contains(gotBody, "Approved for release") {
		t.Fatalf("audit post path=%s body=%s err=%v", gotPath, gotBody, err)
	}
}
