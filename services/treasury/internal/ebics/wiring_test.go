package ebics

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/bankconnect"
)

const pollTenant = "0190b7a0-0000-7000-8000-000000000001"

func (m *memStore) PollableSubscribers(context.Context) ([]Subscriber, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Subscriber
	for _, s := range m.subs {
		if s.State == StateReady && s.KeysConfirmed && s.ParkedCode == "" {
			out = append(out, s)
		}
	}
	return out, nil
}

func (m *memStore) TryLockSubscriber(_ context.Context, id string) (func(), bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.locked[id] {
		return nil, false, nil
	}
	if m.locked == nil {
		m.locked = map[string]bool{}
	}
	m.locked[id] = true
	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.locked, id)
	}, true, nil
}

func (m *memStore) SubscriberForAccount(_ context.Context, bankAccountID string) (Subscriber, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.subs {
		if s.BankAccountID == bankAccountID {
			return s, nil
		}
	}
	return Subscriber{}, errors.New("no subscriber for account")
}

type sinkLog struct {
	files      []string
	principals []tenant.Principal
	failOn     string
}

func (s *sinkLog) take(ctx context.Context, account, name string, _ []byte) error {
	p, _ := tenant.FromContext(ctx)
	s.principals = append(s.principals, p)
	if s.failOn != "" && name == s.failOn {
		return errors.New("cannot import")
	}
	s.files = append(s.files, account+"/"+name)
	return nil
}

func newPoller(e *env, sink *sinkLog) *Poller {
	return &Poller{Client: e.client, Lister: e.store, Sink: sink.take,
		Config: PollConfig{Tenants: []string{pollTenant}, Interval: time.Hour, OrderType: "C53"}}
}

func twoStatements(t *testing.T) []byte {
	return zipOf(t, map[string][]byte{"stmt-1.xml": []byte("<a/>"), "stmt-2.xml": []byte("<b/>")})
}

func TestPoller_ImportsStatementsOfReadySubscribersUnderTheirLegalEntity(t *testing.T) {
	e := newEnv(t, false)
	e.onboard()
	e.bank.served["C53"] = twoStatements(t)
	sink := &sinkLog{}
	n, err := newPoller(e, sink).Once(context.Background())
	if err != nil || n != 2 || len(sink.files) != 2 || sink.files[0] != "ba-1/stmt-1.xml" {
		t.Fatalf("n=%d err=%v files=%v", n, err, sink.files)
	}
	for _, p := range sink.principals {
		if p.TenantID != pollTenant || p.LegalEntityID != "le-1" || p.UserID != pollActor {
			t.Fatalf("import ran as %+v; it must run as the poller in the subscriber's tenant and legal entity", p)
		}
	}
	if len(e.bank.receipts) != 1 || e.bank.receipts[0] != "0" {
		t.Fatalf("receipts %v", e.bank.receipts)
	}
}

func TestPoller_TwoReplicasOnOneSubscriberRunOneSessionAndSendNoNegativeReceipt(t *testing.T) {
	e := newEnv(t, false)
	e.onboard()
	e.bank.served["C53"] = twoStatements(t)
	inSink, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	first := newPoller(e, &sinkLog{})
	first.Sink = func(context.Context, string, string, []byte) error {
		once.Do(func() { close(inSink) })
		<-release
		return nil
	}
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := first.Once(context.Background())
		done <- result{n, err}
	}()
	<-inSink
	requests := e.bank.requests
	second := &sinkLog{}
	if n, err := newPoller(e, second).Once(context.Background()); err != nil || n != 0 || len(second.files) != 0 || e.bank.requests != requests {
		t.Fatalf("the second replica must skip a subscriber that is being polled: n=%d err=%v files=%v bank requests +%d", n, err, second.files, e.bank.requests-requests)
	}
	close(release)
	if r := <-done; r.err != nil || r.n != 2 {
		t.Fatalf("first replica: %+v", r)
	}
	if len(e.bank.receipts) != 1 || e.bank.receipts[0] != "0" {
		t.Fatalf("receipts %v: exactly one positive receipt and no negative one", e.bank.receipts)
	}
	if n, err := newPoller(e, &sinkLog{}).Once(context.Background()); err != nil || n != 2 {
		t.Fatalf("the lock must be released after the poll: n=%d err=%v", n, err)
	}
}

func TestPoller_SkipsNewAndParkedSubscribersAndNeverContactsTheBank(t *testing.T) {
	e := newEnv(t, false)
	sink := &sinkLog{}
	p := newPoller(e, sink)
	if n, err := p.Once(context.Background()); err != nil || n != 0 || e.bank.requests != 0 {
		t.Fatalf("NEW subscriber: n=%d err=%v requests=%d", n, err, e.bank.requests)
	}
	e.onboard()
	e.bank.served["C53"] = twoStatements(t)
	e.bank.failNext("init:C53", CodeSignatureFailed, 1)
	if _, err := p.Once(context.Background()); err == nil || e.sub().ParkedCode != CodeSignatureFailed {
		t.Fatalf("a refusal that needs a person parks the subscriber: err=%v parked=%q", err, e.sub().ParkedCode)
	}
	before := e.bank.requests
	if n, err := p.Once(context.Background()); err != nil || n != 0 || e.bank.requests != before {
		t.Fatalf("parked: n=%d err=%v; the bank was contacted %d more times", n, err, e.bank.requests-before)
	}
	if _, err := e.client.Unpark(context.Background(), "sub-1", "treasurer"); err != nil {
		t.Fatal(err)
	}
	if n, err := p.Once(context.Background()); err != nil || n != 2 {
		t.Fatalf("after unpark: n=%d err=%v", n, err)
	}
}

func TestPoller_ParkedForLongerThanAnyWindow_NothingIsSkippedAfterUnpark(t *testing.T) {
	e := newEnv(t, false)
	e.onboard()
	today := e.client.now().UTC()
	for _, ago := range []int{40, 20, 9, 1} {
		day := today.AddDate(0, 0, -ago).Format("2006-01-02")
		e.bank.dated = append(e.bank.dated, datedStatement{day: day, name: "stmt-" + day + ".xml"})
	}
	sink := &sinkLog{}
	p := newPoller(e, sink)
	e.bank.failNext("init:C53", "091301", 1)
	if _, err := p.Once(context.Background()); err == nil || e.sub().ParkedCode == "" {
		t.Fatalf("the refusal must park the subscriber: err=%v parked=%q", err, e.sub().ParkedCode)
	}
	for range 3 {
		if n, err := p.Once(context.Background()); err != nil || n != 0 {
			t.Fatalf("parked: n=%d err=%v", n, err)
		}
	}
	if _, err := e.client.Unpark(context.Background(), "sub-1", "treasurer"); err != nil {
		t.Fatal(err)
	}
	if n, err := p.Once(context.Background()); err != nil || n != 4 || len(sink.files) != 4 {
		t.Fatalf("after unpark the bank's whole backlog must arrive: n=%d err=%v files=%v", n, err, sink.files)
	}
	for _, r := range e.bank.ranges {
		if r != ".." {
			t.Fatalf("a steady-state poll sent the date range %q", r)
		}
	}
	if n, err := p.Once(context.Background()); err != nil || n != 0 {
		t.Fatalf("receipted files must not come again: n=%d err=%v", n, err)
	}
}

func TestPoller_BackOffCodesWaitThenSucceed_ExhaustionIsRetriedNextTick(t *testing.T) {
	e := newEnv(t, false)
	e.onboard()
	e.bank.served["C53"] = twoStatements(t)
	sink := &sinkLog{}
	p := newPoller(e, sink)
	e.bank.failNext("init:C53", CodeInternalError, 2)
	if n, err := p.Once(context.Background()); err != nil || n != 2 || len(e.sleeps) != 2 {
		t.Fatalf("n=%d err=%v sleeps=%v", n, err, e.sleeps)
	}
	e.bank.failNext("init:C53", CodeInternalError, 100)
	if _, err := p.Once(context.Background()); err == nil || e.sub().ParkedCode != "" {
		t.Fatalf("exhausted back-off must report and not park: err=%v parked=%q", err, e.sub().ParkedCode)
	}
	e.bank.failNext("init:C53", CodeInternalError, 0)
	if n, err := p.Once(context.Background()); err != nil || n != 2 {
		t.Fatalf("next tick: n=%d err=%v", n, err)
	}
}

func TestPoller_NoDataIsNotAnError(t *testing.T) {
	e := newEnv(t, false)
	e.onboard()
	if n, err := newPoller(e, &sinkLog{}).Once(context.Background()); err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestLoadPollConfig(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if _, on, err := LoadPollConfig(env(nil)); on || err != nil {
		t.Fatalf("polling must be off by default: %v %v", on, err)
	}
	cfg, on, err := LoadPollConfig(env(map[string]string{"TREASURY_EBICS_POLL_WORKERS": "true", "TREASURY_EBICS_POLL_TENANT_IDS": pollTenant + "," + pollTenant,
		"TREASURY_EBICS_POLL_INTERVAL": "15m", "TREASURY_EBICS_POLL_ORDER": "STA"}))
	if err != nil || !on || len(cfg.Tenants) != 1 || cfg.Interval != 15*time.Minute || cfg.OrderType != "STA" {
		t.Fatalf("%+v %v %v", cfg, on, err)
	}
	for name, m := range map[string]map[string]string{
		"no tenants":     {"TREASURY_EBICS_POLL_WORKERS": "true"},
		"bad tenant":     {"TREASURY_EBICS_POLL_WORKERS": "true", "TREASURY_EBICS_POLL_TENANT_IDS": "nope"},
		"short interval": {"TREASURY_EBICS_POLL_WORKERS": "true", "TREASURY_EBICS_POLL_TENANT_IDS": pollTenant, "TREASURY_EBICS_POLL_INTERVAL": "5s"},
		"bad order":      {"TREASURY_EBICS_POLL_WORKERS": "true", "TREASURY_EBICS_POLL_TENANT_IDS": pollTenant, "TREASURY_EBICS_POLL_ORDER": "C52"},
	} {
		if _, _, err := LoadPollConfig(env(m)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestFetchStatements_ReceiptFollowsTheImport(t *testing.T) {
	e := newEnv(t, false)
	e.onboard()
	e.bank.served["C53"] = twoStatements(t)
	sink := &sinkLog{failOn: "stmt-1.xml"}
	n, err := e.client.FetchStatements(context.Background(), "sub-1", "C53", "", "", sink.take)
	if err == nil || n != 1 || len(e.bank.receipts) != 1 || e.bank.receipts[0] != "1" {
		t.Fatalf("n=%d err=%v receipts=%v; a refused import is acknowledged negatively so the bank keeps the data", n, err, e.bank.receipts)
	}
	sink.failOn = ""
	n, err = e.client.FetchStatements(context.Background(), "sub-1", "C53", "", "", sink.take)
	if err != nil || n != 2 || len(e.bank.receipts) != 2 || e.bank.receipts[1] != "0" {
		t.Fatalf("redelivery: n=%d err=%v receipts=%v", n, err, e.bank.receipts)
	}
}

func TestFetchStatements_CorruptArchiveIsAcknowledgedNegativelyAndRedeliveryWorks(t *testing.T) {
	e := newEnv(t, false)
	e.onboard()
	e.bank.served["C53"] = append([]byte("PK\x03\x04"), payload(200)...)
	sink := &sinkLog{}
	if _, err := e.client.FetchStatements(context.Background(), "sub-1", "C53", "", "", sink.take); err == nil || len(sink.files) != 0 {
		t.Fatalf("err=%v files=%v", err, sink.files)
	}
	if len(e.bank.receipts) != 1 || e.bank.receipts[0] != "1" {
		t.Fatalf("receipts %v", e.bank.receipts)
	}
	e.bank.served["C53"] = twoStatements(t)
	if n, err := e.client.FetchStatements(context.Background(), "sub-1", "C53", "", "", sink.take); err != nil || n != 2 {
		t.Fatalf("redelivery: n=%d err=%v", n, err)
	}
}

func TestAccountSender_UploadsThroughTheAccountsSubscriberAndAudits(t *testing.T) {
	e := newEnv(t, false)
	e.onboard()
	audit := &recordingAudit{}
	data := payload(300)
	s := AccountSender{Client: e.client, Accounts: e.store, Audit: audit}
	r, err := s.Send(context.Background(), bankconnect.File{LegalEntityID: "le-1", BankAccountID: "ba-1", Name: "pain001.xml", OrderType: "CCT", Data: data})
	if err != nil || r.Reference == "" {
		t.Fatalf("%v %+v", err, r)
	}
	if len(audit.rows) != 2 || audit.rows[0].Event != bankconnect.EventPrepared || audit.rows[1].Event != bankconnect.EventSent {
		t.Fatalf("audit %+v", audit.rows)
	}
	if audit.rows[1].KeyVersion != 1 {
		t.Fatalf("the sent row names the pinned A006 key version: %+v", audit.rows[1])
	}
	for _, row := range audit.rows {
		if row.Protocol != "ebics" || row.KeyName != "ebics-a006" || row.BankAccountID != "ba-1" || row.OrderType != "CCT" {
			t.Fatalf("audit row %+v; it names the A006 key and the pinned version the order was signed with", row)
		}
	}
	if len(e.bank.orders) != 1 || string(e.bank.orders[0].data) != string(data) {
		t.Fatal("the bank did not receive the file")
	}
	if _, err := s.Send(context.Background(), bankconnect.File{BankAccountID: "ba-unknown", OrderType: "CCT", Data: data}); err == nil || len(audit.rows) != 2 {
		t.Fatalf("an account without a subscriber must fail before anything is audited or sent: %v", err)
	}
	if _, err := s.Send(context.Background(), bankconnect.File{LegalEntityID: "le-other", BankAccountID: "ba-1", Name: "p.xml", OrderType: "CCT", Data: data}); !errors.Is(err, ErrLegalEntityMismatch) ||
		len(audit.rows) != 2 || len(e.bank.orders) != 1 {
		t.Fatalf("a file of another legal entity must not leave through this subscriber, nor be audited: %v", err)
	}
}

func TestAccountSender_ParkedSubscriberFailsAndIsAudited(t *testing.T) {
	e := newEnv(t, false)
	e.onboard()
	e.bank.failNext("init:CCT", CodeSignatureFailed, 1)
	audit := &recordingAudit{}
	s := AccountSender{Client: e.client, Accounts: e.store, Audit: audit}
	if _, err := s.Send(context.Background(), bankconnect.File{LegalEntityID: "le-1", BankAccountID: "ba-1", Name: "p.xml", OrderType: "CCT", Data: payload(50)}); err == nil {
		t.Fatal("a refused order must fail")
	}
	if len(audit.rows) != 2 || audit.rows[1].Event != bankconnect.EventFailed {
		t.Fatalf("audit %+v", audit.rows)
	}
}

func TestVaultKeys_OnboardingNeverReadsTheE002PrivateKey(t *testing.T) {
	e := newEnv(t, true)
	ctx := context.Background()
	if _, err := e.client.SendINI(ctx, "sub-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.client.SendHIA(ctx, "sub-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.client.Letter(ctx, "sub-1", "HIA"); err != nil {
		t.Fatal(err)
	}
	for _, c := range e.vault.calls {
		if strings.HasSuffix(c, "secret/data/passage/ebics-e002") {
			t.Fatalf("vault calls %v; HIA and letters must use the separately stored public key", e.vault.calls)
		}
	}
}

func TestLetter_RebuildsTheLettersOfSentOrdersOnly(t *testing.T) {
	e := newEnv(t, false)
	ctx := context.Background()
	sent, err := e.client.SendINI(ctx, "sub-1")
	if err != nil {
		t.Fatal(err)
	}
	again, err := e.client.Letter(ctx, "sub-1", "INI")
	if err != nil || again.Text != sent.Text || again.Hashes[UsageA006] == "" {
		t.Fatalf("%v %q", err, again.Text)
	}
	if _, err := e.client.Letter(ctx, "sub-1", "HPB"); !errors.Is(err, ErrUnknownLetter) {
		t.Fatalf("err %v", err)
	}
}

func TestUnpark_NeedsAParkedSubscriberAndAnActor(t *testing.T) {
	e := newEnv(t, false)
	e.onboard()
	ctx := context.Background()
	if _, err := e.client.Unpark(ctx, "sub-1", "treasurer"); !errors.Is(err, ErrBadState) {
		t.Fatalf("unparking a subscriber that is not parked: %v", err)
	}
	if _, err := e.client.Unpark(ctx, "sub-1", ""); err == nil {
		t.Fatal("an unpark without a named person must be refused")
	}
}

func TestChannels_EBICSIsSelectedByProtocolAndNothingFallsBack(t *testing.T) {
	e := newEnv(t, false)
	e.onboard()
	audit := &recordingAudit{}
	channels := bankconnect.Channels{bankconnect.ProtocolEBICS: AccountSender{Client: e.client, Accounts: e.store, Audit: audit}}
	f := bankconnect.File{LegalEntityID: "le-1", BankAccountID: "ba-1", Name: "p.xml", OrderType: "CCT", Data: payload(120)}
	if _, err := channels.Send(context.Background(), bankconnect.ProtocolEBICS, f); err != nil || len(e.bank.orders) != 1 || len(audit.rows) != 2 {
		t.Fatalf("err=%v orders=%d audit=%d", err, len(e.bank.orders), len(audit.rows))
	}
	if _, err := channels.Send(context.Background(), bankconnect.ProtocolSFTP, f); err == nil || len(e.bank.orders) != 1 {
		t.Fatalf("a channel that is not enabled must fail and send nothing: %v", err)
	}
}

func TestKeyVersion_PinnedAtInitialisationAndSignaturesIgnoreRotation(t *testing.T) {
	e := newEnv(t, true)
	ctx := context.Background()
	ini, err := e.client.SendINI(ctx, "sub-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.client.SendHIA(ctx, "sub-1"); err != nil {
		t.Fatal(err)
	}
	if sub := e.sub(); sub.A006Version != 1 || sub.X002Version != 1 {
		t.Fatalf("pinned versions a006=%d x002=%d", sub.A006Version, sub.X002Version)
	}
	e.bank.mu.Lock()
	e.bank.active = true
	e.bank.mu.Unlock()
	h, err := e.client.FetchBankKeys(ctx, "sub-1", "operator")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.client.ConfirmBankKeys(ctx, "sub-1", h.X002, h.E002, "treasurer"); err != nil {
		t.Fatal(err)
	}

	e.vault.rotateA006(testKeys(t)[3])
	if _, err := e.client.Upload(ctx, "sub-1", "CCT", payload(40)); err != nil {
		t.Fatalf("after a rotation in Vault the bank still holds version 1, and the order must still verify: %v", err)
	}
	e.vault.mu.Lock()
	versions := append([]int(nil), e.vault.signVersions...)
	e.vault.mu.Unlock()
	if len(versions) == 0 {
		t.Fatal("no signature reached Vault")
	}
	for _, v := range versions {
		if v != 1 {
			t.Fatalf("signed with key version %d; only the pinned version 1 may sign (%v)", v, versions)
		}
	}
	letter, err := e.client.Letter(ctx, "sub-1", "INI")
	if err != nil || letter.Hashes[UsageA006] != ini.Hashes[UsageA006] {
		t.Fatalf("the letter of a subscriber with a pinned key shows that version's hash, not the rotated key's: %v", err)
	}
}

func TestKeyVersion_AnUnpinnedKeyNeverSigns(t *testing.T) {
	e := newEnv(t, true)
	keys, err := e.client.Keys(Subscriber{A006Key: "ebics-a006", X002Key: "ebics-x002", E002KVPath: "passage/ebics-e002"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := keys.SignA006(context.Background(), make([]byte, 32)); err == nil {
		t.Fatal("signing with a key whose version was never pinned must be refused")
	}
	if len(e.vault.signVersions) != 0 {
		t.Fatal("the refusal must come before any call to Vault's sign endpoint")
	}
}

func TestLoadHTTPTimeout(t *testing.T) {
	env := func(v string) func(string) string { return func(string) string { return v } }
	if d, err := LoadHTTPTimeout(env("")); err != nil || d != 60*time.Second {
		t.Fatalf("default: %v %v", d, err)
	}
	if d, err := LoadHTTPTimeout(env("90s")); err != nil || d != 90*time.Second {
		t.Fatalf("90s: %v %v", d, err)
	}
	for _, bad := range []string{"soon", "0s", "500ms", "11m"} {
		if _, err := LoadHTTPTimeout(env(bad)); err == nil {
			t.Fatalf("%q must be refused", bad)
		}
	}
}
