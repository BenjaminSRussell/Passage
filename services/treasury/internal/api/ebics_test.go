package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/ebics"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/store"
)

const testSubscriber = "0190b7a0-0000-7000-8000-0000000000a1"

type fakeEBICS struct {
	sub        ebics.Subscriber
	confirmer  string
	unparker   string
	created    store.NewEBICSSubscriber
	confirmErr error
	sendErr    error
}

func (f *fakeEBICS) CreateEBICSSubscriber(_ context.Context, in store.NewEBICSSubscriber) (ebics.Subscriber, error) {
	f.created = in
	f.sub = ebics.Subscriber{ID: testSubscriber, LegalEntityID: in.LegalEntityID, BankAccountID: in.BankAccountID, HostID: in.HostID, PartnerID: in.PartnerID, UserID: in.UserID, URL: in.URL, State: ebics.StateNew, Version: 1}
	return f.sub, nil
}

func (f *fakeEBICS) GetSubscriber(_ context.Context, id string) (ebics.Subscriber, error) {
	if id != testSubscriber {
		return ebics.Subscriber{}, httpx.NewProblem(http.StatusNotFound, store.CodeSubscriberNotFound, "EBICS subscriber not found")
	}
	return f.sub, nil
}

func (f *fakeEBICS) SendINI(context.Context, string) (ebics.Letter, error) {
	if f.sendErr != nil {
		return ebics.Letter{}, f.sendErr
	}
	f.sub.State, f.sub.Version = ebics.StateINISent, f.sub.Version+1
	return ebics.Letter{Text: "INI letter", Hashes: map[string]string{"A006": "AA BB"}}, nil
}

func (f *fakeEBICS) SendHIA(context.Context, string) (ebics.Letter, error) {
	f.sub.State, f.sub.Version = ebics.StateHIASent, f.sub.Version+1
	return ebics.Letter{Text: "HIA letter", Hashes: map[string]string{"X002": "CC", "E002": "DD"}}, nil
}

func (f *fakeEBICS) Letter(_ context.Context, _, order string) (ebics.Letter, error) {
	return ebics.Letter{Text: order + " letter", Hashes: map[string]string{}}, nil
}

func (f *fakeEBICS) FetchBankKeys(context.Context, string) (ebics.BankKeyHashes, error) {
	f.sub.State, f.sub.Version = ebics.StateKeysPending, f.sub.Version+1
	return ebics.BankKeyHashes{X002: "11", E002: "22"}, nil
}

func (f *fakeEBICS) ConfirmBankKeys(_ context.Context, _, _, _, by string) (ebics.Subscriber, error) {
	if f.confirmErr != nil {
		return ebics.Subscriber{}, f.confirmErr
	}
	f.confirmer = by
	f.sub.State, f.sub.KeysConfirmed, f.sub.ConfirmedBy, f.sub.Version = ebics.StateReady, true, by, f.sub.Version+1
	return f.sub, nil
}

func (f *fakeEBICS) Unpark(_ context.Context, _, actor string) (ebics.Subscriber, error) {
	f.unparker = actor
	f.sub.ParkedCode = ""
	return f.sub, nil
}

func newEBICSServer(t *testing.T) (http.Handler, *fakeEBICS) {
	t.Helper()
	fe := &fakeEBICS{}
	r := httpx.NewRouter()
	New(newFakeStore()).WithEBICS(fe).Register(r)
	return r, fe
}

func ebicsJSON(t *testing.T, v any) *bytes.Buffer {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewBuffer(b)
}

func createSubscriberBody(t *testing.T) *bytes.Buffer {
	return ebicsJSON(t, map[string]any{"legalEntityId": testLE, "bankAccountId": testMainAcct, "hostId": "BANKHOST", "partnerId": "PARTNER1", "userId": "USER1",
		"url": "https://ebics.bank.example/ebics", "a006KeyName": "ebics-a006", "x002KeyName": "ebics-x002", "e002KvPath": "passage/ebics-e002"})
}

func asUser(duties string) map[string]string {
	return map[string]string{tenant.HeaderDuties: duties, tenant.HeaderUser: "treasurer-1"}
}

func decodeEBICS(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
}

func TestEBICS_OnboardingThroughTheRoutes(t *testing.T) {
	h, fe := newEBICSServer(t)
	base := "/v1/treasury/ebics-subscribers"

	rec := call(h, http.MethodPost, base, createSubscriberBody(t), "application/json", nil)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if fe.created.A006Key != "ebics-a006" || fe.created.E002KVPath != "passage/ebics-e002" {
		t.Fatalf("key names were not passed through: %+v", fe.created)
	}
	var created subscriberView
	decodeEBICS(t, rec, &created)
	if created.State != "NEW" || created.ID != testSubscriber {
		t.Fatalf("%+v", created)
	}

	for _, step := range []struct{ path, state, letter string }{{":send-ini", "INI_SENT", "INI letter"}, {":send-hia", "HIA_SENT", "HIA letter"}} {
		rec = call(h, http.MethodPost, base+"/"+testSubscriber+step.path, nil, "", nil)
		var out letterOrderResponse
		decodeEBICS(t, rec, &out)
		if rec.Code != http.StatusOK || out.Subscriber.State != step.state || out.Letter.Text != step.letter {
			t.Fatalf("%s: %d %s", step.path, rec.Code, rec.Body)
		}
	}

	rec = call(h, http.MethodPost, base+"/"+testSubscriber+":fetch-bank-keys", nil, "", nil)
	var pending subscriberView
	decodeEBICS(t, rec, &pending)
	if rec.Code != http.StatusOK || pending.State != "BANK_KEYS_PENDING" || pending.KeysConfirmed {
		t.Fatalf("fetch: %d %s", rec.Code, rec.Body)
	}

	body := ebicsJSON(t, map[string]any{"x002Hash": "11", "e002Hash": "22", "confirmedBy": "somebody-else"})
	rec = call(h, http.MethodPost, base+"/"+testSubscriber+":confirm-bank-keys", body, "application/json", asUser("treasury.ebics.approve"))
	if rec.Code == http.StatusOK {
		t.Fatal("unknown body fields must be refused")
	}
	body = ebicsJSON(t, map[string]any{"x002Hash": "11", "e002Hash": "22"})
	rec = call(h, http.MethodPost, base+"/"+testSubscriber+":confirm-bank-keys", body, "application/json", asUser("treasury.ebics.approve"))
	var ready subscriberView
	decodeEBICS(t, rec, &ready)
	if rec.Code != http.StatusOK || ready.State != "READY" || ready.ConfirmedBy != "treasurer-1" || fe.confirmer != "treasurer-1" {
		t.Fatalf("confirm: %d %s (confirmer %q)", rec.Code, rec.Body, fe.confirmer)
	}

	if rec = call(h, http.MethodGet, base+"/"+testSubscriber, nil, "", nil); rec.Code != http.StatusOK {
		t.Fatalf("get: %d", rec.Code)
	}
	var letter letterView
	rec = call(h, http.MethodGet, base+"/"+testSubscriber+"/letters/HIA", nil, "", nil)
	decodeEBICS(t, rec, &letter)
	if rec.Code != http.StatusOK || letter.Order != "HIA" || letter.Text != "HIA letter" {
		t.Fatalf("letter: %d %s", rec.Code, rec.Body)
	}
	if rec = call(h, http.MethodGet, base+"/"+testSubscriber+"/letters/HPB", nil, "", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("an order without a letter: %d", rec.Code)
	}
	if rec = call(h, http.MethodPost, base+"/"+testSubscriber+":unpark", nil, "", asUser("treasury.ebics.approve")); rec.Code != http.StatusOK || fe.unparker != "treasurer-1" {
		t.Fatalf("unpark: %d (actor %q)", rec.Code, fe.unparker)
	}
}

func TestEBICS_EveryRouteNeedsItsDuty(t *testing.T) {
	h, _ := newEBICSServer(t)
	base := "/v1/treasury/ebics-subscribers"
	for _, tc := range []struct {
		method, path, duty, body string
	}{
		{http.MethodPost, base, "treasury.view", "create"},
		{http.MethodPost, base + "/" + testSubscriber + ":send-ini", "treasury.ebics.write,treasury.ebics.approve,treasury.view", ""},
		{http.MethodPost, base + "/" + testSubscriber + ":send-hia", "treasury.ebics.write,treasury.ebics.approve", ""},
		{http.MethodPost, base + "/" + testSubscriber + ":fetch-bank-keys", "treasury.ebics.write,treasury.ebics.approve", ""},
		{http.MethodPost, base + "/" + testSubscriber + ":confirm-bank-keys", "treasury.ebics.write,treasury.ebics.keys", "confirm"},
		{http.MethodPost, base + "/" + testSubscriber + ":unpark", "treasury.ebics.write,treasury.ebics.keys", ""},
		{http.MethodGet, base + "/" + testSubscriber, "treasury.ebics.write,treasury.ebics.keys,treasury.ebics.approve", ""},
		{http.MethodGet, base + "/" + testSubscriber + "/letters/INI", "treasury.ebics.write", ""},
	} {
		var body *bytes.Buffer
		switch tc.body {
		case "create":
			body = createSubscriberBody(t)
		case "confirm":
			body = ebicsJSON(t, map[string]any{"x002Hash": "11", "e002Hash": "22"})
		}
		rec := call(h, tc.method, tc.path, body, "application/json", asUser(tc.duty))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s with %q: status %d, want 403", tc.method, tc.path, tc.duty, rec.Code)
		}
	}
}

func TestEBICS_ConfirmationFailuresMapToProblems(t *testing.T) {
	h, fe := newEBICSServer(t)
	path := "/v1/treasury/ebics-subscribers/" + testSubscriber + ":confirm-bank-keys"
	body := func() *bytes.Buffer { return ebicsJSON(t, map[string]any{"x002Hash": "11", "e002Hash": "22"}) }
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{ebics.ErrKeyHashMismatch, http.StatusUnprocessableEntity, CodeEBICSHashMismatch},
		{ebics.ErrBadState, http.StatusConflict, CodeEBICSState},
		{ebics.ErrVersionConflict, http.StatusPreconditionFailed, store.CodeVersionConflict},
	} {
		fe.confirmErr = tc.err
		rec := call(h, http.MethodPost, path, body(), "application/json", asUser("treasury.ebics.approve"))
		if rec.Code != tc.status || problemCode(t, rec) != tc.code {
			t.Errorf("%v: %d %s", tc.err, rec.Code, rec.Body)
		}
	}
	rec := call(h, http.MethodPost, path, ebicsJSON(t, map[string]any{"x002Hash": "", "e002Hash": "22"}), "application/json", asUser("treasury.ebics.approve"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty hash: %d", rec.Code)
	}
	noUser := map[string]string{tenant.HeaderDuties: "treasury.ebics.approve", tenant.HeaderUser: ""}
	if rec = call(h, http.MethodPost, path, body(), "application/json", noUser); rec.Code != http.StatusForbidden {
		t.Errorf("a confirmation without a named user: %d", rec.Code)
	}
}

func TestEBICS_BankRefusalIsABadGatewayWithTheBankCode(t *testing.T) {
	h, fe := newEBICSServer(t)
	fe.sub.ID = testSubscriber
	fe.sendErr = &ebics.ReturnError{Code: "061001", Text: "EBICS_AUTHENTICATION_FAILED", Disposition: ebics.Park}
	rec := call(h, http.MethodPost, "/v1/treasury/ebics-subscribers/"+testSubscriber+":send-ini", nil, "", nil)
	if rec.Code != http.StatusBadGateway || problemCode(t, rec) != CodeEBICSBank {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	fe.sendErr = errors.Join(ebics.ErrParked)
	if rec = call(h, http.MethodPost, "/v1/treasury/ebics-subscribers/"+testSubscriber+":send-ini", nil, "", nil); rec.Code != http.StatusConflict || problemCode(t, rec) != CodeEBICSParked {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestEBICS_CreateValidationScopeAndDisabled(t *testing.T) {
	h, _ := newEBICSServer(t)
	rec := call(h, http.MethodPost, "/v1/treasury/ebics-subscribers", ebicsJSON(t, map[string]any{"legalEntityId": "x"}), "application/json", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("validation: %d", rec.Code)
	}
	other := ebicsJSON(t, map[string]any{"legalEntityId": "0190b7a0-0000-7000-8000-0000000000ff", "bankAccountId": testMainAcct, "hostId": "H", "partnerId": "P", "userId": "U",
		"url": "https://x.example", "a006KeyName": "a", "x002KeyName": "b", "e002KvPath": "c"})
	if rec = call(h, http.MethodPost, "/v1/treasury/ebics-subscribers", other, "application/json", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("a legal entity outside the caller's scope: %d", rec.Code)
	}
	off, _ := newTestServer(t)
	if rec = call(off, http.MethodGet, "/v1/treasury/ebics-subscribers/"+testSubscriber, nil, "", nil); rec.Code != http.StatusServiceUnavailable || problemCode(t, rec) != CodeEBICSOff {
		t.Fatalf("EBICS off: %d %s", rec.Code, rec.Body)
	}
	if rec = call(h, http.MethodGet, "/v1/treasury/ebics-subscribers/not-a-uuid", nil, "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}
}
