package ebics

import (
	"context"
	"errors"
	"strings"
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
		Config: PollConfig{Tenants: []string{pollTenant}, Interval: time.Hour, OrderType: "C53", LookbackDays: 3}}
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
		"TREASURY_EBICS_POLL_INTERVAL": "15m", "TREASURY_EBICS_POLL_ORDER": "STA", "TREASURY_EBICS_POLL_LOOKBACK_DAYS": "7"}))
	if err != nil || !on || len(cfg.Tenants) != 1 || cfg.Interval != 15*time.Minute || cfg.OrderType != "STA" || cfg.LookbackDays != 7 {
		t.Fatalf("%+v %v %v", cfg, on, err)
	}
	for name, m := range map[string]map[string]string{
		"no tenants":     {"TREASURY_EBICS_POLL_WORKERS": "true"},
		"bad tenant":     {"TREASURY_EBICS_POLL_WORKERS": "true", "TREASURY_EBICS_POLL_TENANT_IDS": "nope"},
		"short interval": {"TREASURY_EBICS_POLL_WORKERS": "true", "TREASURY_EBICS_POLL_TENANT_IDS": pollTenant, "TREASURY_EBICS_POLL_INTERVAL": "5s"},
		"bad order":      {"TREASURY_EBICS_POLL_WORKERS": "true", "TREASURY_EBICS_POLL_TENANT_IDS": pollTenant, "TREASURY_EBICS_POLL_ORDER": "C52"},
		"bad lookback":   {"TREASURY_EBICS_POLL_WORKERS": "true", "TREASURY_EBICS_POLL_TENANT_IDS": pollTenant, "TREASURY_EBICS_POLL_LOOKBACK_DAYS": "90"},
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
	for _, row := range audit.rows {
		if row.Protocol != "ebics" || row.KeyName != "ebics-a006" || row.KeyVersion != 0 || row.BankAccountID != "ba-1" || row.OrderType != "CCT" {
			t.Fatalf("audit row %+v; it names the A006 key the order was signed with and no unused transit signature", row)
		}
	}
	if len(e.bank.orders) != 1 || string(e.bank.orders[0].data) != string(data) {
		t.Fatal("the bank did not receive the file")
	}
	if _, err := s.Send(context.Background(), bankconnect.File{BankAccountID: "ba-unknown", OrderType: "CCT", Data: data}); err == nil || len(audit.rows) != 2 {
		t.Fatalf("an account without a subscriber must fail before anything is audited or sent: %v", err)
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
