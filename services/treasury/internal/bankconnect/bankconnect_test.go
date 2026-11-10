package bankconnect

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeVault struct {
	t      *testing.T
	pub    *rsa.PublicKey
	priv   *rsa.PrivateKey
	mu     sync.Mutex
	calls  int
	bodies []string
	status int
	srv    *httptest.Server
}

func newFakeVault(t *testing.T) *fakeVault {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	v := &fakeVault{t: t, pub: &priv.PublicKey, priv: priv, status: http.StatusOK}
	v.srv = httptest.NewServer(http.HandlerFunc(v.serve))
	t.Cleanup(v.srv.Close)
	return v
}

func (v *fakeVault) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	v.mu.Lock()
	v.calls++
	v.bodies = append(v.bodies, string(raw))
	v.mu.Unlock()
	if r.Header.Get("X-Vault-Token") != "s.test-token" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if r.URL.Path != "/v1/transit/sign/bank-a/sha2-256" || r.Method != http.MethodPost {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if v.status != http.StatusOK {
		w.WriteHeader(v.status)
		return
	}
	var in struct {
		Input     string `json:"input"`
		Algorithm string `json:"signature_algorithm"`
		Salt      string `json:"salt_length"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || in.Algorithm != "pss" || in.Salt != "hash" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	payload, err := base64.StdEncoding.DecodeString(in.Input)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	digest := sha256.Sum256(payload)
	signed, err := rsa.SignPSS(rand.Reader, v.priv, crypto.SHA256, digest[:], &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	sig := base64.StdEncoding.EncodeToString(signed)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"signature": "vault:v3:" + sig, "key_version": 3}})
}

func (v *fakeVault) signer() VaultTransit {
	return VaultTransit{Addr: v.srv.URL, Token: "s.test-token"}
}

type stubBank struct {
	pub      *rsa.PublicKey
	uploads  int
	verified int
	err      error
}

func newFakeBank(_ *testing.T, pub *rsa.PublicKey) *stubBank { return &stubBank{pub: pub} }

func (b *stubBank) Protocol() string { return ProtocolEBICS }

func (b *stubBank) Send(_ context.Context, f File, sig Signature) (Receipt, error) {
	b.uploads++
	digest := sha256.Sum256(f.Data)
	if rsa.VerifyPSS(b.pub, crypto.SHA256, digest[:], sig.Bytes, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash}) == nil {
		b.verified++
	}
	if b.err != nil {
		return Receipt{}, b.err
	}
	return Receipt{Reference: "TX-0001"}, nil
}

func (b *stubBank) transport() *stubBank { return b }

type memoryAudit struct {
	rows      []AuditEntry
	failEvent string
}

func (m *memoryAudit) RecordBankFile(_ context.Context, e AuditEntry) error {
	if e.Event == m.failEvent {
		return errors.New("audit store down")
	}
	m.rows = append(m.rows, e)
	return nil
}

var pain001 = File{LegalEntityID: "le-1", BankAccountID: "ba-1", Name: "pain001-0001.xml", OrderType: "CCT", KeyName: "bank-a", Data: []byte("<Document>pain.001</Document>")}

func TestSender_SignsInVaultSendsOverEBICSAndAudits(t *testing.T) {
	vault := newFakeVault(t)
	bank := newFakeBank(t, vault.pub)
	audit := &memoryAudit{}
	receipt, err := Sender{Signer: vault.signer(), Transport: bank.transport(), Audit: audit}.Send(context.Background(), pain001)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Reference != "TX-0001" {
		t.Fatalf("receipt = %+v", receipt)
	}
	if bank.uploads != 1 || bank.verified != 1 {
		t.Fatalf("bank saw %d uploads, %d with a signature that verifies against the Vault key", bank.uploads, bank.verified)
	}
	sum := sha256.Sum256(pain001.Data)
	if len(audit.rows) != 2 || audit.rows[0].Event != EventPrepared || audit.rows[1].Event != EventSent {
		t.Fatalf("audit rows = %+v", audit.rows)
	}
	sent := audit.rows[1]
	if sent.SHA256 != hex.EncodeToString(sum[:]) || sent.SizeBytes != len(pain001.Data) || sent.KeyName != "bank-a" || sent.KeyVersion != 3 ||
		sent.BankReference != "TX-0001" || sent.Protocol != ProtocolEBICS || sent.FileName != "pain001-0001.xml" || sent.BankAccountID != "ba-1" || sent.FileID != audit.rows[0].FileID {
		t.Fatalf("sent audit row = %+v", sent)
	}
}

func TestVaultTransit_OnlyTokenKeyNameAndInputLeaveTheService(t *testing.T) {
	vault := newFakeVault(t)
	if _, err := vault.signer().Sign(context.Background(), "bank-a", pain001.Data); err != nil {
		t.Fatal(err)
	}
	body := vault.bodies[0]
	var in map[string]string
	if err := json.Unmarshal([]byte(body), &in); err != nil || len(in) != 3 || in["salt_length"] != "hash" || in["input"] != base64.StdEncoding.EncodeToString(pain001.Data) || in["signature_algorithm"] != "pss" {
		t.Fatalf("vault request body = %s", body)
	}
	if strings.Contains(body, base64.StdEncoding.EncodeToString(x509.MarshalPKCS1PrivateKey(vault.priv))) {
		t.Fatal("private key material present in the request")
	}
}

func TestVaultTransit_RejectsBadReplies(t *testing.T) {
	vault := newFakeVault(t)
	wrongToken := VaultTransit{Addr: vault.srv.URL, Token: "s.stolen"}
	if _, err := wrongToken.Sign(context.Background(), "bank-a", pain001.Data); err == nil {
		t.Fatal("a refused token must fail")
	}
	garbage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"signature":"plain","key_version":1}}`)
	}))
	t.Cleanup(garbage.Close)
	if _, err := (VaultTransit{Addr: garbage.URL, Token: "t"}).Sign(context.Background(), "bank-a", nil); err == nil {
		t.Fatal("a signature without the vault: prefix must fail")
	}
}

func TestVaultTransit_ReturnsRawSignatureBytes(t *testing.T) {
	vault := newFakeVault(t)
	sig, err := vault.signer().Sign(context.Background(), "bank-a", pain001.Data)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(pain001.Data)
	if len(sig.Bytes) != 256 || sig.KeyVersion != 3 || rsa.VerifyPSS(vault.pub, crypto.SHA256, digest[:], sig.Bytes, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash}) != nil {
		t.Fatalf("signature bytes do not verify as RSA-PSS SHA-256: %d bytes", len(sig.Bytes))
	}
}

func TestURLs_PlainHTTPOnlyToLoopback(t *testing.T) {
	for _, bad := range []string{"http://vault.example.com:8200", "http://10.0.0.5", "ftp://bank.example.com", "bank.example.com", ""} {
		if _, err := (VaultTransit{Addr: bad, Token: "t"}).Sign(context.Background(), "k", nil); err == nil || !strings.Contains(err.Error(), "url") {
			t.Errorf("vault %q: err = %v", bad, err)
		}
	}
}

func TestCheckURL_Table(t *testing.T) {
	cases := []struct {
		url      string
		dev      bool
		wantFail bool
	}{
		{"https://bank.example.com/ebics", false, false},
		{"https://93.184.216.34/ebics", false, false},
		{"https://[2606:2800:220:1::1]/ebics", false, false},
		{"https://203.0.113.9/ebics", false, true},
		{"http://bank.example.com/ebics", false, true},
		{"http://bank.example.com/ebics", true, true},
		{"https://localhost./ebics", false, true},
		{"https://bank.localhost/ebics", false, true},
		{"https://2130706433/ebics", false, true},
		{"https://0x7f.1/ebics", false, true},
		{"https://127.1/ebics", false, true},
		{"https://0177.0.0.1/ebics", false, true},
		{"https://bank.cafe/ebics", false, false},
		{"https://bank.example.com./ebics", false, false},
		{"https://[::ffff:0:0:1]/ebics", false, true},
		{"https://192.88.99.1/ebics", false, true},
		{"https://[2001:20::1]/ebics", false, true},
		{"https://127.0.0.1:8443/ebics", true, false},
		{"http://localhost:8080", false, true},
		{"http://127.0.0.1:8080", false, true},
		{"http://[::1]:8080", false, true},
		{"https://localhost/ebics", false, true},
		{"https://127.0.0.1/ebics", false, true},
		{"https://[::ffff:127.0.0.1]/ebics", false, true},
		{"https://10.0.0.5/ebics", false, true},
		{"https://172.16.0.1/ebics", false, true},
		{"https://192.168.1.1/ebics", false, true},
		{"https://169.254.169.254/latest", false, true},
		{"https://[fe80::1]/ebics", false, true},
		{"https://[fd00::1]/ebics", false, true},
		{"https://100.64.0.1/ebics", false, true},
		{"https://0.0.0.0/ebics", false, true},
		{"https://user:pw@bank.example.com/ebics", false, true},
		{"ftp://bank.example.com", false, true},
		{"", false, true},
		{"http://localhost:8080", true, false},
		{"http://bank.example.com/ebics", true, true},
		{"http://127.0.0.1:8080", true, false},
		{"http://[::1]:8080", true, false},
		{"http://10.0.0.5", true, true},
		{"https://10.0.0.5", true, true},
	}
	for _, c := range cases {
		err := checkURL(c.url, c.dev)
		if (err != nil) != c.wantFail {
			t.Errorf("CheckURL(%q) dev=%t: err = %v, want failure %t", c.url, c.dev, err, c.wantFail)
		}
	}
}

func TestGuardDial_AllowlistsGlobalUnicastOnly(t *testing.T) {
	cases := []struct {
		addr    string
		blocked bool
	}{
		{"93.184.216.34:443", false}, {"[2606:2800:220:1::1]:443", false}, {"[::ffff:93.184.216.34]:443", false},
		{"0.1.2.3:443", true}, {"10.0.0.1:443", true}, {"100.64.0.1:443", true}, {"127.0.0.1:443", true},
		{"169.254.169.254:443", true}, {"172.16.0.1:443", true}, {"192.0.0.9:443", true}, {"192.0.2.1:443", true},
		{"192.168.0.1:443", true}, {"198.18.0.1:443", true}, {"198.51.100.1:443", true}, {"203.0.113.1:443", true},
		{"224.0.0.1:443", true}, {"240.0.0.1:443", true}, {"255.255.255.255:443", true},
		{"[::]:443", true}, {"[::1]:443", true}, {"[::7f00:1]:443", true}, {"[fc00::1]:443", true}, {"[fd12::1]:443", true},
		{"[fe80::1]:443", true}, {"[2001:db8::1]:443", true}, {"[ff02::1]:443", true},
		{"[::ffff:127.0.0.1]:443", true}, {"[::ffff:10.0.0.1]:443", true},
		{"[64:ff9b::7f00:1]:443", true}, {"[64:ff9b::a00:1]:443", true}, {"[64:ff9b:1::7f00:1]:443", true}, {"[64:ff9b:1::a00:1]:443", true},
		{"[2002:7f00:1::]:443", true}, {"[2002:a00:1::]:443", true},
		{"192.88.99.1:443", true}, {"[::ffff:0:a00:1]:443", true}, {"[2001:20::1]:443", true},
		{"bank.example.com:443", true},
	}
	for _, c := range cases {
		if err := guardDial(false)("tcp", c.addr, nil); (err != nil) != c.blocked {
			t.Errorf("guardDial(%s) = %v, blocked want %t", c.addr, err, c.blocked)
		}
	}
	dev := guardDial(true)
	if dev("tcp", "127.0.0.1:443", nil) != nil || dev("tcp", "[::1]:443", nil) != nil {
		t.Error("loopback must pass in dev")
	}
	for _, a := range []string{"[64:ff9b::7f00:1]:443", "[2002:7f00:1::]:443", "10.0.0.1:443"} {
		if dev("tcp", a, nil) == nil {
			t.Errorf("%s must stay blocked in dev", a)
		}
	}
}

func TestBankHTTP_RefusesLoopbackPeerAndRedirects(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	closed := NewBankHTTP(5*time.Second, false)

	for _, target := range []string{srv.URL, strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)} {
		req, _ := http.NewRequest(http.MethodPost, target, strings.NewReader("<x/>"))
		if resp, err := closed.Do(req); err == nil {
			_ = resp.Body.Close()
			t.Fatalf("Do %s reached a loopback peer", target)
		}
		resp, err := closed.client.Post(target, "text/xml", strings.NewReader("<x/>"))
		var opErr *net.OpError
		if err == nil || !errors.As(err, &opErr) || opErr.Op != "dial" {
			t.Fatalf("the dialer must refuse %s as a failed dial (not reached): %v", target, err)
		}
		if resp != nil {
			_ = resp.Body.Close()
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("loopback server saw %d requests", hits.Load())
	}

	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader("<x/>"))
	resp, err := NewBankHTTP(5*time.Second, true).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusTemporaryRedirect || hits.Load() != 1 {
		t.Fatalf("redirect was followed: status %d, hits %d", resp.StatusCode, hits.Load())
	}
}

func TestSender_VaultFailureSendsNothingAndAuditsFailure(t *testing.T) {
	vault := newFakeVault(t)
	vault.status = http.StatusInternalServerError
	bank := newFakeBank(t, vault.pub)
	audit := &memoryAudit{}
	_, err := Sender{Signer: vault.signer(), Transport: bank.transport(), Audit: audit}.Send(context.Background(), pain001)
	if err == nil {
		t.Fatal("expected an error")
	}
	if bank.uploads != 0 {
		t.Fatal("an unsigned file reached the bank")
	}
	if len(audit.rows) != 2 || audit.rows[1].Event != EventFailed || audit.rows[1].Error == "" {
		t.Fatalf("audit rows = %+v", audit.rows)
	}
}

func TestSender_BankRefusalIsAuditedAsFailed(t *testing.T) {
	vault := newFakeVault(t)
	bank := newFakeBank(t, vault.pub)
	bank.err = errors.New("ebics upload refused: 091116")
	audit := &memoryAudit{}
	_, err := Sender{Signer: vault.signer(), Transport: bank.transport(), Audit: audit}.Send(context.Background(), pain001)
	if err == nil || !strings.Contains(err.Error(), "091116") {
		t.Fatalf("err = %v", err)
	}
	if len(audit.rows) != 2 || audit.rows[1].Event != EventFailed || audit.rows[1].BankReference != "" {
		t.Fatalf("audit rows = %+v", audit.rows)
	}
}

func TestSender_NoAuditNoSend(t *testing.T) {
	vault := newFakeVault(t)
	bank := newFakeBank(t, vault.pub)
	audit := &memoryAudit{failEvent: EventPrepared}
	if _, err := (Sender{Signer: vault.signer(), Transport: bank.transport(), Audit: audit}).Send(context.Background(), pain001); err == nil {
		t.Fatal("expected an error")
	}
	if vault.calls != 0 || bank.uploads != 0 {
		t.Fatalf("vault calls %d, bank uploads %d; nothing may happen without a durable audit row", vault.calls, bank.uploads)
	}
}

func TestSender_SentButClosingAuditLostReturnsReceiptAndError(t *testing.T) {
	vault := newFakeVault(t)
	bank := newFakeBank(t, vault.pub)
	audit := &memoryAudit{failEvent: EventSent}
	receipt, err := Sender{Signer: vault.signer(), Transport: bank.transport(), Audit: audit}.Send(context.Background(), pain001)
	if err == nil || receipt.Reference != "TX-0001" || !strings.Contains(err.Error(), "TX-0001") {
		t.Fatalf("receipt %+v, err %v", receipt, err)
	}
	if len(audit.rows) != 1 || audit.rows[0].Event != EventPrepared {
		t.Fatalf("audit rows = %+v", audit.rows)
	}
}

func TestSender_AmbiguousTransportFailureIsUnknownAndNotRetried(t *testing.T) {
	vault := newFakeVault(t)
	bank := newFakeBank(t, vault.pub)
	bank.err = fmt.Errorf("%w: status 502", ErrAmbiguous)
	audit := &memoryAudit{}
	_, err := Sender{Signer: vault.signer(), Transport: bank.transport(), Audit: audit}.Send(context.Background(), pain001)
	if !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("err = %v", err)
	}
	if bank.uploads != 1 || vault.calls != 1 {
		t.Fatalf("uploads %d, signs %d; an ambiguous failure must not be resent", bank.uploads, vault.calls)
	}
	if len(audit.rows) != 2 || audit.rows[1].Event != EventUnknown {
		t.Fatalf("audit rows = %+v", audit.rows)
	}
}

func TestSender_OutcomeRowSurvivesCancelledContext(t *testing.T) {
	vault := newFakeVault(t)
	bank := newFakeBank(t, vault.pub)
	ctx, cancel := context.WithCancel(context.Background())
	audit := &cancelCheckAudit{cancel: cancel}
	Sender{Signer: vault.signer(), Transport: bank.transport(), Audit: audit}.Send(ctx, pain001)
	if len(audit.rows) != 2 || audit.rows[1].Event == "" || audit.outcomeCtxErr != nil {
		t.Fatalf("rows %+v, outcome context error %v", audit.rows, audit.outcomeCtxErr)
	}
}

type cancelCheckAudit struct {
	rows          []AuditEntry
	cancel        context.CancelFunc
	outcomeCtxErr error
}

func (c *cancelCheckAudit) RecordBankFile(ctx context.Context, e AuditEntry) error {
	if e.Event != EventPrepared {
		c.outcomeCtxErr = ctx.Err()
	}
	c.rows = append(c.rows, e)
	if e.Event == EventPrepared {
		c.cancel()
	}
	return nil
}
