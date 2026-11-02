package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/processmfg/internal/store"
)

const (
	ebrBatch     = "77777777-0000-4000-8000-0000000000b1"
	ebrRecord    = "77777777-0000-4000-8000-0000000000c1"
	ebrException = "77777777-0000-4000-8000-0000000000d1"
	ebrSignature = "77777777-0000-4000-8000-0000000000e1"
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
	version   int
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
	return store.Record{ID: id, Version: 1}, f.err
}

func (f *fakeStore) RaiseException(_ context.Context, id string, in store.NewException, version int, idem store.Idem) (store.Record, bool, error) {
	f.ebr.recordID, f.ebr.raise, f.ebr.version, f.gotIdem = id, in, version, idem
	return store.Record{ID: id, Version: version + 1}, !f.replay, f.err
}

func (f *fakeStore) DispositionException(_ context.Context, id, exception string, in store.ExceptionDisposition, version int, idem store.Idem) (store.Record, bool, error) {
	f.ebr.recordID, f.ebr.exception, f.ebr.dispose, f.ebr.version, f.gotIdem = id, exception, in, version, idem
	return store.Record{ID: id, Version: version + 1}, !f.replay, f.err
}

func (f *fakeStore) ApproveRecord(_ context.Context, id string, version int, idem store.Idem) (store.Record, bool, error) {
	f.ebr.recordID, f.ebr.version, f.gotIdem = id, version, idem
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

func TestRecord_CompileAndVersionedCommands(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
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
		body: `{"version":4,"disposition":"deviation-raised","comment":"DEV-12","signatureId":"` + ebrSignature + `"}`}.do(h)
	if disp.Code != http.StatusOK || f.ebr.exception != ebrException || f.ebr.dispose.Disposition != "deviation-raised" || f.ebr.dispose.SignatureID != ebrSignature || f.ebr.version != 4 {
		t.Fatalf("disposition %d %s %+v", disp.Code, disp.Body, f.ebr.dispose)
	}

	for _, step := range []string{"approve", "seal"} {
		r := call{method: "POST", path: recordsPath + "/" + ebrRecord + ":" + step, body: `{"version":5}`, duties: DutyBatchRecordApprove, headers: key("s-" + step)}.do(h)
		if r.Code != http.StatusOK || f.ebr.recordID != ebrRecord || f.ebr.version != 5 {
			t.Fatalf("%s %d %s", step, r.Code, r.Body)
		}
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
		{"disposition unknown", dispPath, `{"version":1,"disposition":"ignore","signatureId":"` + ebrSignature + `"}`, DutyBatchRecordWrite, 400},
		{"disposition without signature", dispPath, `{"version":1,"disposition":"accepted"}`, DutyBatchRecordWrite, 400},
		{"disposition bad exception id", exPath + "/x:disposition", `{"version":1,"disposition":"accepted","signatureId":"` + ebrSignature + `"}`, DutyBatchRecordWrite, 400},
		{"approve without version", recordsPath + "/" + ebrRecord + ":approve", `{}`, DutyBatchRecordApprove, 400},
		{"approve bad id", recordsPath + "/x:approve", `{"version":1}`, DutyBatchRecordApprove, 400},
		{"seal needs approve duty", recordsPath + "/" + ebrRecord + ":seal", `{"version":1}`, DutyBatchRecordWrite, 403},
		{"approve needs approve duty", recordsPath + "/" + ebrRecord + ":approve", `{"version":1}`, DutyBatchRecordWrite, 403},
		{"disposition needs the review duty", dispPath, `{"version":1,"disposition":"accepted","signatureId":"` + ebrSignature + `"}`, DutyRead, 403},
		{"approve duty alone does not decide", dispPath, `{"version":1,"disposition":"accepted","signatureId":"` + ebrSignature + `"}`, DutyBatchRecordApprove, 403},
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
