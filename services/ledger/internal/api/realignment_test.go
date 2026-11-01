package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/services/ledger/internal/store"
)

type realignFake struct {
	created  []store.RealignmentInput
	actions  []string
	last     store.RealignmentAction
	replayed bool
}

func (f *fakeStore) CreateRealignment(_ context.Context, in store.RealignmentInput) (store.RealignmentRun, bool, error) {
	f.realign.created = append(f.realign.created, in)
	return store.RealignmentRun{ID: realignTestID, Status: "proposed", Version: 1}, f.realign.replayed, f.err
}

func (f *fakeStore) ListRealignments(context.Context, string) ([]store.RealignmentRun, error) {
	return []store.RealignmentRun{{ID: realignTestID, Status: "posted"}}, f.err
}

func (f *fakeStore) GetRealignment(_ context.Context, _, id string) (store.RealignmentRun, error) {
	return store.RealignmentRun{ID: id, Status: "proposed"}, f.err
}

func (f *fakeStore) act(name string, in store.RealignmentAction) (store.RealignmentRun, error) {
	f.realign.actions = append(f.realign.actions, name)
	f.realign.last = in
	return store.RealignmentRun{ID: in.RunID, Status: name}, f.err
}

func (f *fakeStore) PostRealignment(_ context.Context, in store.RealignmentAction) (store.RealignmentRun, error) {
	return f.act("posted", in)
}

func (f *fakeStore) ReverseRealignment(_ context.Context, in store.RealignmentAction) (store.RealignmentRun, error) {
	return f.act("reversed", in)
}

func (f *fakeStore) CancelRealignment(_ context.Context, in store.RealignmentAction) (store.RealignmentRun, error) {
	return f.act("cancelled", in)
}

const realignTestID = "00000000-0000-4000-8000-0000000000d1"

func TestRealignment_CreateMapsAndEnforcesDuty(t *testing.T) {
	fs := &fakeStore{}
	h := server(fs)
	path := "/v1/ledger/ledgers/" + testLedgerID + "/realignments"
	body := `{"filter":{"accountFrom":"6000","accountTo":"6999","periodFrom":"2026-01","periodTo":"2026-03"},"mapping":{"DEPT":{"D100":"D150"}}}`
	hdr := map[string]string{"Idempotency-Key": "ra-1"}
	if rec := call(h, http.MethodPost, path, body, dutyRead, hdr); rec.Code != http.StatusForbidden {
		t.Fatalf("needs the realign duty: %d", rec.Code)
	}
	if rec := call(h, http.MethodPost, path, body, dutyRealign, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("needs Idempotency-Key: %d", rec.Code)
	}
	rec := call(h, http.MethodPost, path, body, dutyRealign, hdr)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	got := fs.realign.created[0]
	if got.Filter.PeriodTo != "2026-03" || got.Mapping["DEPT"]["D100"] != "D150" || got.Key != "ra-1" || len(got.Hash) != 32 || got.Actor != "clerk-1" {
		t.Fatalf("not mapped: %+v", got)
	}
	fs.realign.replayed = true
	if rec := call(h, http.MethodPost, path, body, dutyRealign, hdr); rec.Code != http.StatusOK {
		t.Fatalf("replay answers 200: %d", rec.Code)
	}
	if rec := call(h, http.MethodPost, path, `{"filter":{},"unknown":1}`, dutyRealign, hdr); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, path, "", dutyRead, nil); rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, path+"/"+realignTestID, "", dutyRead, nil); rec.Code != http.StatusOK {
		t.Fatalf("get: %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, path+"/nope", "", dutyRead, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("get bad id: %d", rec.Code)
	}
}

func TestRealignment_Actions(t *testing.T) {
	fs := &fakeStore{}
	h := server(fs)
	base := "/v1/ledger/ledgers/" + testLedgerID + "/realignments/" + realignTestID
	hdr := map[string]string{"Idempotency-Key": "ra-2"}
	body := `{"reasonCode":"REORG","reasonComment":"cost centre merge"}`
	for _, a := range []struct{ action, want string }{{":post", "posted"}, {":reverse", "reversed"}} {
		if rec := call(h, http.MethodPost, base+a.action, body, dutyRead, hdr); rec.Code != http.StatusForbidden {
			t.Fatalf("%s needs the realign duty: %d", a.action, rec.Code)
		}
		if rec := call(h, http.MethodPost, base+a.action, `{}`, dutyRealign, hdr); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s needs a reason: %d", a.action, rec.Code)
		}
		if rec := call(h, http.MethodPost, base+a.action, body, dutyRealign, nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s needs Idempotency-Key: %d", a.action, rec.Code)
		}
		rec := call(h, http.MethodPost, base+a.action, body, dutyRealign, hdr)
		if rec.Code != http.StatusOK || decode(t, rec)["status"] != a.want {
			t.Fatalf("%s: %d %s", a.action, rec.Code, rec.Body.String())
		}
		if l := fs.realign.last; l.ReasonCode != "REORG" || l.ReasonComment != "cost centre merge" || l.RunID != realignTestID || l.Key != "ra-2" {
			t.Fatalf("%s not mapped: %+v", a.action, l)
		}
	}
	if rec := call(h, http.MethodPost, base+":cancel", `{}`, dutyRealign, nil); rec.Code != http.StatusOK || fs.realign.actions[len(fs.realign.actions)-1] != "cancelled" {
		t.Fatalf("cancel needs neither key nor reason: %d", rec.Code)
	}
}

func TestRealignment_ReversePostingDate(t *testing.T) {
	fs := &fakeStore{}
	h := server(fs)
	base := "/v1/ledger/ledgers/" + testLedgerID + "/realignments/" + realignTestID
	hdr := map[string]string{"Idempotency-Key": "ra-3"}
	if rec := call(h, http.MethodPost, base+":reverse", `{"reasonCode":"REORG","postingDate":"15.03.2026"}`, dutyRealign, hdr); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad posting date: %d", rec.Code)
	}
	if rec := call(h, http.MethodPost, base+":reverse", `{"reasonCode":"REORG","postingDate":"2026-03-15"}`, dutyRealign, hdr); rec.Code != http.StatusOK {
		t.Fatalf("reverse with a date: %d %s", rec.Code, rec.Body.String())
	}
	if d := fs.realign.last.PostingDate; d == nil || d.Format("2006-01-02") != "2026-03-15" {
		t.Fatalf("posting date not mapped: %v", d)
	}
	if rec := call(h, http.MethodPost, base+":post", `{"reasonCode":"REORG","postingDate":"2026-03-15"}`, dutyRealign, hdr); rec.Code != http.StatusOK || fs.realign.last.PostingDate != nil {
		t.Fatalf("post ignores the posting date: %d %v", rec.Code, fs.realign.last.PostingDate)
	}
}
