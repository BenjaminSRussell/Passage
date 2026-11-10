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
	fetcher    string
	unparker   string
	created    store.NewEBICSSubscriber
	createKey  string
	confirmErr error
	sendErr    error
	dropReads  bool
}

func (f *fakeEBICS) CreateEBICSSubscriber(_ context.Context, in store.NewEBICSSubscriber, key string) (ebics.Subscriber, error) {
	f.created, f.createKey = in, key
	f.sub = ebics.Subscriber{ID: testSubscriber, LegalEntityID: in.LegalEntityID, BankAccountID: in.BankAccountID, HostID: in.HostID, PartnerID: in.PartnerID, UserID: in.UserID, URL: in.URL, State: ebics.StateNew, Version: 1}
	return f.sub, nil
}

func (f *fakeEBICS) GetSubscriber(_ context.Context, id string) (ebics.Subscriber, error) {
	if id != testSubscriber || f.dropReads {
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

func (f *fakeEBICS) FetchBankKeys(_ context.Context, _, actor string) (ebics.BankKeyHashes, error) {
	f.fetcher = actor
	f.sub.State, f.sub.Version = ebics.StateKeysPending, f.sub.Version+1
	return ebics.BankKeyHashes{X002: "11", E002: "22"}, nil
}

func (f *fakeEBICS) ConfirmBankKeys(_ context.Context, _, _, _, by string, version int) (ebics.Subscriber, error) {
	if f.confirmErr != nil {
		return ebics.Subscriber{}, f.confirmErr
	}
	if version != f.sub.Version {
		return ebics.Subscriber{}, ebics.ErrVersionConflict
	}
	f.confirmer = by
	f.sub.State, f.sub.KeysConfirmed, f.sub.ConfirmedBy, f.sub.Version = ebics.StateReady, true, by, f.sub.Version+1
	return f.sub, nil
}

func (f *fakeEBICS) Unpark(_ context.Context, _, actor string, version int) (ebics.Subscriber, error) {
	if version != f.sub.Version {
		return ebics.Subscriber{}, ebics.ErrVersionConflict
	}
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

func problemFields(t *testing.T, rec *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var p struct {
		Code   string            `json:"code"`
		Errors map[string]string `json:"errors"`
	}
	decodeEBICS(t, rec, &p)
	if p.Code != "COM-0001" {
		t.Fatalf("validation problem code %q, want COM-0001 (%s)", p.Code, rec.Body)
	}
	return p.Errors
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
		"url": "https://ebics.bank.example/ebics"})
}

func asUser(duties string) map[string]string {
	return map[string]string{tenant.HeaderDuties: duties, tenant.HeaderUser: "treasurer-1"}
}

func asUserAt(duties, etag string) map[string]string {
	h := asUser(duties)
	h["If-Match"] = etag
	return h
}

var withKey = map[string]string{"Idempotency-Key": "key-1"}

func decodeEBICS(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
}

func TestEBICS_OnboardingThroughTheRoutes(t *testing.T) {
	h, fe := newEBICSServer(t)
	base := "/v1/treasury/ebics-subscribers"

	rec := call(h, http.MethodPost, base, createSubscriberBody(t), "application/json", withKey)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if fe.created.HostID != "BANKHOST" || fe.created.LegalEntityID != testLE || fe.createKey != "key-1" {
		t.Fatalf("create input: %+v", fe.created)
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
		if rec.Code != http.StatusOK || out.Subscriber == nil || out.Subscriber.State != step.state || out.Letter.Text != step.letter {
			t.Fatalf("%s: %d %s", step.path, rec.Code, rec.Body)
		}
	}

	rec = call(h, http.MethodPost, base+"/"+testSubscriber+":fetch-bank-keys", nil, "", asUser("treasury.ebics.keys"))
	var pending subscriberView
	decodeEBICS(t, rec, &pending)
	if rec.Code != http.StatusOK || pending.State != "BANK_KEYS_PENDING" || pending.KeysConfirmed {
		t.Fatalf("fetch: %d %s", rec.Code, rec.Body)
	}
	pendingETag := rec.Header().Get("ETag")
	if fe.fetcher != "treasurer-1" {
		t.Fatalf("the person who fetched the bank keys must be recorded from the request's user, got %q", fe.fetcher)
	}

	body := ebicsJSON(t, map[string]any{"x002Hash": "11", "e002Hash": "22", "confirmedBy": "somebody-else"})
	rec = call(h, http.MethodPost, base+"/"+testSubscriber+":confirm-bank-keys", body, "application/json", asUserAt("treasury.ebics.approve", rec.Header().Get("ETag")))
	if rec.Code == http.StatusOK {
		t.Fatal("unknown body fields must be refused")
	}
	body = ebicsJSON(t, map[string]any{"x002Hash": "11", "e002Hash": "22"})
	rec = call(h, http.MethodPost, base+"/"+testSubscriber+":confirm-bank-keys", body, "application/json", asUserAt("treasury.ebics.approve", pendingETag))
	var ready subscriberView
	decodeEBICS(t, rec, &ready)
	readyETag := rec.Header().Get("ETag")
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
	if rec = call(h, http.MethodGet, base+"/"+testSubscriber+"/letters/HPB", nil, "", nil); rec.Code != http.StatusBadRequest || problemFields(t, rec)["order"] == "" {
		t.Fatalf("an order without a letter: %d %s", rec.Code, rec.Body)
	}
	if rec = call(h, http.MethodPost, base+"/"+testSubscriber+":unpark", nil, "", asUserAt("treasury.ebics.approve", readyETag)); rec.Code != http.StatusOK || fe.unparker != "treasurer-1" {
		t.Fatalf("unpark: %d (actor %q)", rec.Code, fe.unparker)
	}
}

func TestEBICS_KeyNamesAreNeverTakenFromTheCaller(t *testing.T) {
	h, fe := newEBICSServer(t)
	for _, field := range []string{"a006KeyName", "x002KeyName", "e002KvPath"} {
		body := map[string]any{"legalEntityId": testLE, "bankAccountId": testMainAcct, "hostId": "BANKHOST", "partnerId": "PARTNER1", "userId": "USER1",
			"url": "https://ebics.bank.example/ebics", field: "ebics-another-tenants-key"}
		rec := call(h, http.MethodPost, "/v1/treasury/ebics-subscribers", ebicsJSON(t, body), "application/json", withKey)
		if rec.Code != http.StatusBadRequest || fe.created.HostID != "" {
			t.Errorf("%s supplied by the caller: %d %s (created %+v); the server derives key names from the tenant, legal entity and subscriber id", field, rec.Code, rec.Body, fe.created)
		}
	}
}

func TestEBICS_LetterOrderStillAnswersWhenTheReadBackFails(t *testing.T) {
	h, fe := newEBICSServer(t)
	fe.dropReads = true
	rec := call(h, http.MethodPost, "/v1/treasury/ebics-subscribers/"+testSubscriber+":send-ini", nil, "", nil)
	var out letterOrderResponse
	decodeEBICS(t, rec, &out)
	if rec.Code != http.StatusOK || out.Letter.Text != "INI letter" || out.Subscriber != nil {
		t.Fatalf("the order went to the bank, so the letter must be returned even if the subscriber cannot be re-read: %d %s", rec.Code, rec.Body)
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
		{ebics.ErrFourEyes, http.StatusForbidden, CodeEBICSFourEyes},
		{ebics.ErrVersionConflict, http.StatusPreconditionFailed, store.CodeVersionConflict},
	} {
		fe.confirmErr = tc.err
		rec := call(h, http.MethodPost, path, body(), "application/json", asUserAt("treasury.ebics.approve", `"1"`))
		if rec.Code != tc.status || problemCode(t, rec) != tc.code {
			t.Errorf("%v: %d %s", tc.err, rec.Code, rec.Body)
		}
	}
	rec := call(h, http.MethodPost, path, ebicsJSON(t, map[string]any{"x002Hash": "", "e002Hash": "22"}), "application/json", asUserAt("treasury.ebics.approve", `"1"`))
	if fields := problemFields(t, rec); rec.Code != http.StatusBadRequest || fields["x002Hash"] == "" || fields["e002Hash"] == "" {
		t.Errorf("empty hash: %d %s", rec.Code, rec.Body)
	}
	noUser := map[string]string{tenant.HeaderDuties: "treasury.ebics.approve", tenant.HeaderUser: "", "If-Match": `"1"`}
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
	rec := call(h, http.MethodPost, "/v1/treasury/ebics-subscribers", ebicsJSON(t, map[string]any{"legalEntityId": "x"}), "application/json", withKey)
	fields := problemFields(t, rec)
	if rec.Code != http.StatusBadRequest || fields["legalEntityId"] == "" || fields["bankAccountId"] == "" || fields["hostId"] == "" || fields["url"] == "" {
		t.Fatalf("validation: %d %s", rec.Code, rec.Body)
	}
	other := ebicsJSON(t, map[string]any{"legalEntityId": "0190b7a0-0000-7000-8000-0000000000ff", "bankAccountId": testMainAcct, "hostId": "H", "partnerId": "P", "userId": "U",
		"url": "https://x.example"})
	if rec = call(h, http.MethodPost, "/v1/treasury/ebics-subscribers", other, "application/json", withKey); rec.Code != http.StatusForbidden {
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

func TestEBICS_PreconditionsOnCreateConfirmAndUnpark(t *testing.T) {
	h, fe := newEBICSServer(t)
	base := "/v1/treasury/ebics-subscribers"
	if rec := call(h, http.MethodPost, base, createSubscriberBody(t), "application/json", nil); rec.Code != http.StatusBadRequest || problemFields(t, rec)["Idempotency-Key"] == "" || fe.created.HostID != "" {
		t.Fatalf("create without Idempotency-Key: %d %s", rec.Code, rec.Body)
	}
	fe.sub = ebics.Subscriber{ID: testSubscriber, State: ebics.StateKeysPending, ParkedCode: "091301", Version: 4}
	confirm := func() *bytes.Buffer { return ebicsJSON(t, map[string]any{"x002Hash": "11", "e002Hash": "22"}) }
	for _, tc := range []struct{ route, name string }{{":confirm-bank-keys", "confirm"}, {":unpark", "unpark"}} {
		body := func() *bytes.Buffer {
			if tc.name == "confirm" {
				return confirm()
			}
			return nil
		}
		approver := asUser("treasury.ebics.approve")
		if rec := call(h, http.MethodPost, base+"/"+testSubscriber+tc.route, body(), "application/json", approver); rec.Code != http.StatusPreconditionRequired || problemCode(t, rec) != CodePreconditionReq {
			t.Errorf("%s without If-Match: %d %s", tc.name, rec.Code, rec.Body)
		}
		if rec := call(h, http.MethodPost, base+"/"+testSubscriber+tc.route, body(), "application/json", asUserAt("treasury.ebics.approve", `"3"`)); rec.Code != http.StatusPreconditionFailed || problemCode(t, rec) != store.CodeVersionConflict {
			t.Errorf("%s with a stale If-Match: %d %s", tc.name, rec.Code, rec.Body)
		}
		if rec := call(h, http.MethodPost, base+"/"+testSubscriber+tc.route, body(), "application/json", asUserAt("treasury.ebics.approve", `"4"`)); rec.Code != http.StatusOK {
			t.Errorf("%s with the current If-Match: %d %s", tc.name, rec.Code, rec.Body)
		}
		fe.sub.Version = 4
	}
}
