package api

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/domain"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/store"
)

const (
	ebrBatch     = "77777777-0000-4000-8000-0000000000b1"
	ebrRecord    = "77777777-0000-4000-8000-0000000000c1"
	ebrException = "77777777-0000-4000-8000-0000000000d1"
)

// ebrCalls records what the handlers pass to the batch record methods.
type ebrCalls struct {
	profile   store.RecordProfile
	profFilt  store.ProfileFilter
	recFilt   store.RecordFilter
	batchID   string
	recordID  string
	exception string
	raise     store.NewException
	dispose   store.ExceptionDisposition
	sign      store.Signing
	version   int
	lockouts  map[string]domain.Lockout
	record    *store.Record
}

func (f *fakeStore) DefineRecordProfile(_ context.Context, in store.RecordProfile, idem store.Idem) (store.RecordProfile, bool, error) {
	f.ebr.profile, f.gotIdem = in, idem
	in.ID = ebrRecord
	return in, !f.replay, f.err
}

func (f *fakeStore) ListRecordProfiles(_ context.Context, fl store.ProfileFilter) ([]store.RecordProfile, error) {
	f.ebr.profFilt = fl
	return []store.RecordProfile{}, f.err
}

func (f *fakeStore) GetRecordProfile(_ context.Context, id string) (store.RecordProfile, error) {
	return store.RecordProfile{ID: id, Version: 1}, f.err
}

func (f *fakeStore) RetireRecordProfile(_ context.Context, id string, version int, idem store.Idem) (store.RecordProfile, bool, error) {
	f.ebr.version, f.gotIdem = version, idem
	return store.RecordProfile{ID: id, Version: version + 1}, !f.replay, f.err
}

func (f *fakeStore) CompileRecord(_ context.Context, batchID string, idem store.Idem) (store.Record, bool, error) {
	f.ebr.batchID, f.gotIdem = batchID, idem
	return store.Record{ID: ebrRecord, BatchID: batchID, Status: store.RecordInReview, Version: 1}, !f.replay, f.err
}

func (f *fakeStore) ListRecords(_ context.Context, fl store.RecordFilter) ([]store.Record, error) {
	f.ebr.recFilt = fl
	return []store.Record{}, f.err
}

func (f *fakeStore) GetRecord(_ context.Context, id string) (store.Record, error) {
	if f.ebr.record != nil {
		return *f.ebr.record, f.err
	}
	return store.Record{ID: id, Version: 1, Exceptions: []store.RecordException{{ID: ebrException, SourceType: "manual", SourceID: ebrException, Description: "Torn seal", Severity: "major"}}}, f.err
}

func (f *fakeStore) SignerLockout(_ context.Context, signer string) (domain.Lockout, error) {
	return f.ebr.lockouts[signer], nil
}

func (f *fakeStore) RecordReauthResult(_ context.Context, signer string, failed bool, now time.Time) (domain.Lockout, error) {
	if f.ebr.lockouts == nil {
		f.ebr.lockouts = map[string]domain.Lockout{}
	}
	l := f.ebr.lockouts[signer]
	if failed {
		l.Fail(now)
	} else {
		l.Succeed()
	}
	f.ebr.lockouts[signer] = l
	return l, nil
}

func (f *fakeStore) RaiseException(_ context.Context, id string, in store.NewException, version int, idem store.Idem) (store.Record, bool, error) {
	f.ebr.recordID, f.ebr.raise, f.ebr.version, f.gotIdem = id, in, version, idem
	return store.Record{ID: id, Version: version + 1}, !f.replay, f.err
}

func (f *fakeStore) DispositionException(_ context.Context, id, exception string, in store.ExceptionDisposition, version int, idem store.Idem) (store.Record, bool, error) {
	f.ebr.recordID, f.ebr.exception, f.ebr.dispose, f.ebr.version, f.gotIdem = id, exception, in, version, idem
	return store.Record{ID: id, Version: version + 1}, !f.replay, f.err
}

func (f *fakeStore) ApproveRecord(_ context.Context, id string, version int, sign store.Signing, idem store.Idem) (store.Record, bool, error) {
	f.ebr.recordID, f.ebr.version, f.ebr.sign, f.gotIdem = id, version, sign, idem
	return store.Record{ID: id, Status: store.RecordApproved, Version: version + 1}, !f.replay, f.err
}

func (f *fakeStore) SealRecord(_ context.Context, id string, version int, idem store.Idem) (store.Record, bool, error) {
	f.ebr.recordID, f.ebr.version, f.gotIdem = id, version, idem
	return store.Record{ID: id, Status: store.RecordSealed, Version: version + 1}, !f.replay, f.err
}

func TestProfile_DefineDefaultsToApprovalRequired(t *testing.T) {
	f := &fakeStore{}
	rec := call{method: "POST", path: profilesPath, body: `{"code":"EBR-1","itemId":"` + pickItem + `"}`, duties: DutyBatchRecordWrite, headers: key("p-1")}.do(newRouter(f))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	g := f.ebr.profile
	if g.Code != "EBR-1" || g.ItemID != pickItem || !g.ApprovalRequiredForRelease || g.LegalEntityID != entityID || f.gotIdem.Key != "p-1" {
		t.Fatalf("stored %+v", g)
	}
	off := call{method: "POST", path: profilesPath, body: `{"code":"EBR-2","itemId":"` + pickItem + `","approvalRequiredForRelease":false}`,
		duties: DutyBatchRecordWrite, headers: key("p-2")}.do(newRouter(f))
	if off.Code != http.StatusCreated || f.ebr.profile.ApprovalRequiredForRelease {
		t.Fatalf("explicit false: %d %+v", off.Code, f.ebr.profile)
	}
	f.replay = true
	again := call{method: "POST", path: profilesPath, body: `{"code":"EBR-1","itemId":"` + pickItem + `"}`, duties: DutyBatchRecordWrite, headers: key("p-1")}.do(newRouter(f))
	if again.Code != http.StatusOK || again.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay %d %v", again.Code, again.Header())
	}
}

func TestProfile_Rejects(t *testing.T) {
	tests := map[string]string{
		"lower-case code": `{"code":"ebr","itemId":"` + pickItem + `"}`,
		"no code":         `{"itemId":"` + pickItem + `"}`,
		"bad item":        `{"code":"EBR","itemId":"x"}`,
		"bad entity":      `{"code":"EBR","itemId":"` + pickItem + `","legalEntityId":"x"}`,
		"unknown field":   `{"code":"EBR","itemId":"` + pickItem + `","colour":"red"}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			rec := call{method: "POST", path: profilesPath, body: body, duties: DutyBatchRecordWrite, headers: key("k")}.do(newRouter(&fakeStore{}))
			if rec.Code != http.StatusBadRequest || problemOf(t, rec).Code != "COM-0001" {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
		})
	}
}

// signRouter mounts the API with reauth as the step-up verifier.
func signRouter(f *fakeStore, reauth Reauthenticator) http.Handler {
	r := httpx.NewRouter()
	New(f).WithReauth(reauth).Register(r)
	return r
}

func TestRecord_CompileAndVersionedCommands(t *testing.T) {
	f := &fakeStore{}
	h := signRouter(f, DevReauth{})
	rec := call{method: "POST", path: recordsPath, body: `{"batchId":"` + strings.ToUpper(ebrBatch) + `"}`, duties: DutyBatchRecordWrite, headers: key("c-1")}.do(h)
	if rec.Code != http.StatusOK || f.ebr.batchID != ebrBatch {
		t.Fatalf("compile %d %s batch %q", rec.Code, rec.Body, f.ebr.batchID)
	}
	if bad := (call{method: "POST", path: recordsPath, body: `{"batchId":"x"}`, duties: DutyBatchRecordWrite, headers: key("c-2")}.do(h)); bad.Code != http.StatusBadRequest {
		t.Fatalf("bad batch id %d", bad.Code)
	}

	exc := call{method: "POST", path: recordsPath + "/" + ebrRecord + "/exceptions", duties: DutyBatchRecordWrite, headers: key("x-1"),
		body: `{"version":3,"description":" Torn seal on drum 4 ","severity":"critical"}`}.do(h)
	if exc.Code != http.StatusOK || f.ebr.raise.Description != "Torn seal on drum 4" || f.ebr.raise.Severity != "critical" || f.ebr.version != 3 {
		t.Fatalf("raise %d %s %+v", exc.Code, exc.Body, f.ebr.raise)
	}
	dflt := call{method: "POST", path: recordsPath + "/" + ebrRecord + "/exceptions", duties: DutyBatchRecordWrite, headers: key("x-2"),
		body: `{"version":3,"description":"Foam"}`}.do(h)
	if dflt.Code != http.StatusOK || f.ebr.raise.Severity != "major" {
		t.Fatalf("default severity %d %+v", dflt.Code, f.ebr.raise)
	}

	disp := call{method: "POST", path: recordsPath + "/" + ebrRecord + "/exceptions/" + ebrException + ":disposition", duties: DutyBatchRecordWrite, headers: key("d-1"),
		body: `{"version":4,"disposition":"deviation-raised","comment":"DEV-12","printedName":" Rita Reviewer ","method":"password"}`}.do(h)
	rec0, _ := f.GetRecord(context.Background(), ebrRecord)
	want, _ := store.DispositionHash(rec0, ebrException, "deviation-raised", "DEV-12")
	d := f.ebr.dispose
	// The signature records the method the verifier proved, never the client's claim.
	if disp.Code != http.StatusOK || f.ebr.exception != ebrException || d.Disposition != "deviation-raised" || f.ebr.version != 4 ||
		d.Sign.PrintedName != "Rita Reviewer" || d.Sign.Method != domain.AuthDevUnverified || !bytes.Equal(d.Sign.RecordHash, want) {
		t.Fatalf("disposition %d %s %+v", disp.Code, disp.Body, d)
	}

	ap := call{method: "POST", path: recordsPath + "/" + ebrRecord + ":approve", body: `{"version":5,"printedName":"Quinn QA"}`, duties: DutyBatchRecordApprove, headers: key("s-approve")}.do(h)
	wantAp, _ := store.ApprovalHash(rec0)
	if ap.Code != http.StatusOK || f.ebr.recordID != ebrRecord || f.ebr.version != 5 || f.ebr.sign.PrintedName != "Quinn QA" || !bytes.Equal(f.ebr.sign.RecordHash, wantAp) {
		t.Fatalf("approve %d %s %+v", ap.Code, ap.Body, f.ebr.sign)
	}
	seal := call{method: "POST", path: recordsPath + "/" + ebrRecord + ":seal", body: `{"version":6}`, duties: DutyBatchRecordApprove, headers: key("s-seal")}.do(h)
	if seal.Code != http.StatusOK || f.ebr.version != 6 {
		t.Fatalf("seal %d %s", seal.Code, seal.Body)
	}
}

// Without a configured step-up verifier no batch record is signed (503).
func TestRecord_SigningFailsClosed(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	disp := call{method: "POST", path: recordsPath + "/" + ebrRecord + "/exceptions/" + ebrException + ":disposition", duties: DutyBatchRecordWrite, headers: key("d"),
		body: `{"version":1,"disposition":"accepted","printedName":"R"}`}.do(h)
	ap := call{method: "POST", path: recordsPath + "/" + ebrRecord + ":approve", body: `{"version":1,"printedName":"Q"}`, duties: DutyBatchRecordApprove, headers: key("a")}.do(h)
	for _, rec := range []*httptest.ResponseRecorder{disp, ap} {
		if rec.Code != http.StatusServiceUnavailable || problemOf(t, rec).Code != domain.CodeSignatureUnavailable {
			t.Fatalf("got %d %s", rec.Code, rec.Body)
		}
	}
	if f.ebr.dispose.Disposition != "" || f.ebr.sign.PrintedName != "" {
		t.Fatal("the store was called without a signature")
	}
}

type reauthFunc func(context.Context, string, domain.Reauth) (string, error)

func (fn reauthFunc) Verify(ctx context.Context, actor string, auth domain.Reauth) (string, error) {
	return fn(ctx, actor, auth)
}

// BR-24.14: three rejected re-authentications lock signing for 15 minutes; the
// verifier is not asked while locked, and a success afterwards clears the count.
func TestRecord_SigningLockout(t *testing.T) {
	f := &fakeStore{}
	now := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	good := false
	asked := 0
	var gotAuth domain.Reauth
	reauth := reauthFunc(func(_ context.Context, actor string, auth domain.Reauth) (string, error) {
		asked++
		gotAuth = auth
		if good {
			return "password+otp", nil
		}
		return "", &domain.Error{Code: domain.CodeSignatureRejected, Detail: "signature rejected: re-authentication failed"}
	})
	r := httpx.NewRouter()
	New(f).WithReauth(reauth).WithClock(func() time.Time { return now }).Register(r)
	approve := func(k string) *httptest.ResponseRecorder {
		return call{method: "POST", path: recordsPath + "/" + ebrRecord + ":approve", body: `{"version":1,"printedName":"Q"}`, duties: DutyBatchRecordApprove,
			headers: map[string]string{"Idempotency-Key": k, ReauthHeader: "assertion-" + k}}.do(r)
	}
	for i, want := range []int{http.StatusForbidden, http.StatusForbidden, http.StatusLocked, http.StatusLocked} {
		rec := approve(fmt.Sprint("a", i))
		if rec.Code != want {
			t.Fatalf("attempt %d: got %d %s, want %d", i+1, rec.Code, rec.Body, want)
		}
	}
	if asked != 3 || gotAuth.Assertion != "assertion-a2" || gotAuth.RecordID != ebrRecord || len(gotAuth.RecordHash) != 32 {
		t.Fatalf("verifier asked %d times, last %+v", asked, gotAuth)
	}
	good = true
	now = now.Add(domain.LockoutWindow + time.Second)
	if rec := approve("a-ok"); rec.Code != http.StatusOK || f.ebr.sign.Method != "password+otp" {
		t.Fatalf("after the lock: %d %s %+v", rec.Code, rec.Body, f.ebr.sign)
	}
}

func TestRecord_PDF(t *testing.T) {
	doc := `{"approvedAt":"2026-11-04T09:00:00Z","approvedBy":"qa-1","batchNumber":"L-1","content":{"batch":{"disposition":"quarantine"}},` +
		`"exceptions":[],"recordId":"` + ebrRecord + `","sealedAt":"2026-11-04T10:00:00Z","sealedBy":"qa-1","signatures":[],"signaturesVerified":false}`
	hash := store.HashHex(doc)
	f := &fakeStore{}
	f.ebr.record = &store.Record{ID: ebrRecord, BatchNumber: "L-1", Status: store.RecordSealed, SealedDocument: &doc, ContentHash: &hash}
	h := newRouter(f)
	rec := call{method: "GET", path: recordsPath + "/" + ebrRecord + "/pdf", duties: DutyRead}.do(h)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/pdf" || rec.Header().Get("X-Content-SHA256") != hash ||
		!strings.HasPrefix(body, "%PDF-1.7") || !strings.Contains(body, hash) || !strings.Contains(body, "Signatures verified: NO") ||
		!strings.Contains(body, "<pdfaid:part>2</pdfaid:part>") || !strings.Contains(body, "<xmp:CreateDate>2026-11-04T10:00:00Z</xmp:CreateDate>") || !strings.Contains(body, "/FontFile2") {
		t.Fatalf("pdf %d %v %.300s", rec.Code, rec.Header(), body)
	}
	f.ebr.record = &store.Record{ID: ebrRecord, Status: store.RecordApproved}
	if rec := (call{method: "GET", path: recordsPath + "/" + ebrRecord + "/pdf", duties: DutyRead}.do(h)); rec.Code != http.StatusConflict {
		t.Fatalf("unsealed pdf %d", rec.Code)
	}
}

func TestRecord_RejectsAndDuties(t *testing.T) {
	h := newRouter(&fakeStore{})
	exPath := recordsPath + "/" + ebrRecord + "/exceptions"
	dispPath := exPath + "/" + ebrException + ":disposition"
	tests := []struct {
		name, path, body, duty string
		status                 int
	}{
		{"exception without text", exPath, `{"version":1,"description":"  "}`, DutyBatchRecordWrite, 400},
		{"exception bad severity", exPath, `{"version":1,"description":"x","severity":"huge"}`, DutyBatchRecordWrite, 400},
		{"exception no version", exPath, `{"description":"x"}`, DutyBatchRecordWrite, 400},
		{"exception text too long", exPath, `{"version":1,"description":"` + strings.Repeat("x", 501) + `"}`, DutyBatchRecordWrite, 400},
		{"disposition unknown", dispPath, `{"version":1,"disposition":"ignore","printedName":"R"}`, DutyBatchRecordWrite, 400},
		{"disposition without printed name", dispPath, `{"version":1,"disposition":"accepted"}`, DutyBatchRecordWrite, 400},
		{"client signature id refused", dispPath, `{"version":1,"disposition":"accepted","printedName":"R","signatureId":"X"}`, DutyBatchRecordWrite, 400},
		{"approve without printed name", recordsPath + "/" + ebrRecord + ":approve", `{"version":1}`, DutyBatchRecordApprove, 400},
		{"disposition bad exception id", exPath + "/x:disposition", `{"version":1,"disposition":"accepted","printedName":"R"}`, DutyBatchRecordWrite, 400},
		{"approve without version", recordsPath + "/" + ebrRecord + ":approve", `{}`, DutyBatchRecordApprove, 400},
		{"approve bad id", recordsPath + "/x:approve", `{"version":1}`, DutyBatchRecordApprove, 400},
		{"seal needs approve duty", recordsPath + "/" + ebrRecord + ":seal", `{"version":1}`, DutyBatchRecordWrite, 403},
		{"approve needs approve duty", recordsPath + "/" + ebrRecord + ":approve", `{"version":1}`, DutyBatchRecordWrite, 403},
		{"disposition needs the review duty", dispPath, `{"version":1,"disposition":"accepted","printedName":"R"}`, DutyRead, 403},
		{"approve duty alone does not decide", dispPath, `{"version":1,"disposition":"accepted","printedName":"R"}`, DutyBatchRecordApprove, 403},
		{"compile needs write duty", recordsPath, `{"batchId":"` + ebrBatch + `"}`, DutyRead, 403},
		{"profile needs write duty", profilesPath, `{"code":"A","itemId":"` + pickItem + `"}`, DutyRead, 403},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := call{method: "POST", path: tc.path, body: tc.body, duties: tc.duty, headers: key("k")}.do(h)
			if rec.Code != tc.status {
				t.Fatalf("got %d %s, want %d", rec.Code, rec.Body, tc.status)
			}
		})
	}
}

func TestRecord_ReadsAndFilters(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	list := call{method: "GET", path: recordsPath + "?status=sealed&batchId=" + strings.ToUpper(ebrBatch) + "&itemId=" + pickItem + "&limit=5", duties: DutyRead}.do(h)
	if list.Code != http.StatusOK || f.ebr.recFilt.Status != "sealed" || f.ebr.recFilt.BatchID != ebrBatch || f.ebr.recFilt.ItemID != pickItem || f.ebr.recFilt.Limit != 5 {
		t.Fatalf("list %d %s %+v", list.Code, list.Body, f.ebr.recFilt)
	}
	if bad := (call{method: "GET", path: recordsPath + "?status=done", duties: DutyRead}.do(h)); bad.Code != http.StatusBadRequest {
		t.Fatalf("bad status %d", bad.Code)
	}
	if one := (call{method: "GET", path: recordsPath + "/" + ebrRecord, duties: DutyRead}.do(h)); one.Code != http.StatusOK {
		t.Fatalf("get %d", one.Code)
	}
	prof := call{method: "GET", path: profilesPath + "?active=true&itemId=" + pickItem, duties: DutyRead}.do(h)
	if prof.Code != http.StatusOK || !f.ebr.profFilt.ActiveOnly || f.ebr.profFilt.ItemID != pickItem {
		t.Fatalf("profiles %d %+v", prof.Code, f.ebr.profFilt)
	}
	if one := (call{method: "GET", path: profilesPath + "/" + ebrRecord, duties: DutyRead}.do(h)); one.Code != http.StatusOK {
		t.Fatalf("get profile %d", one.Code)
	}
	ret := call{method: "POST", path: profilesPath + "/" + ebrRecord + ":retire", body: `{"version":2}`, duties: DutyBatchRecordWrite, headers: key("r")}.do(h)
	if ret.Code != http.StatusOK || f.ebr.version != 2 {
		t.Fatalf("retire %d", ret.Code)
	}
}
