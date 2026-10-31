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
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
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

type bankUpload struct {
	namespace string
	fields    map[string]string
}

type fakeBank struct {
	t          *testing.T
	pub        *rsa.PublicKey
	mu         sync.Mutex
	uploads    []bankUpload
	verified   int
	returnCode string
	status     int
	srv        *httptest.Server
}

func newFakeBank(t *testing.T, pub *rsa.PublicKey) *fakeBank {
	b := &fakeBank{t: t, pub: pub, returnCode: ebicsOK, status: http.StatusOK}
	b.srv = httptest.NewServer(http.HandlerFunc(b.serve))
	t.Cleanup(b.srv.Close)
	return b
}

func parseH004(r io.Reader) (bankUpload, bool) {
	dec := xml.NewDecoder(r)
	up := bankUpload{fields: map[string]string{}}
	var path []string
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return up, len(path) == 0 && up.namespace != ""
		}
		if err != nil {
			return up, false
		}
		switch el := tok.(type) {
		case xml.StartElement:
			if len(path) == 0 {
				up.namespace = el.Name.Space
			}
			path = append(path, el.Name.Local)
		case xml.CharData:
			if txt := strings.TrimSpace(string(el)); txt != "" && len(path) > 0 {
				up.fields[strings.Join(path, "/")] += txt
			}
		case xml.EndElement:
			path = path[:len(path)-1]
		}
	}
}

func (b *fakeBank) serve(w http.ResponseWriter, r *http.Request) {
	up, ok := parseH004(r.Body)
	if r.Method != http.MethodPost || !ok || up.namespace != "urn:org:ebics:H004" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	data, _ := base64.StdEncoding.DecodeString(up.fields["ebicsRequest/body/DataTransfer/OrderData"])
	raw, _ := base64.StdEncoding.DecodeString(up.fields["ebicsRequest/body/SignatureData"])
	digest := sha256.Sum256(data)
	b.mu.Lock()
	b.uploads = append(b.uploads, up)
	if rsa.VerifyPSS(b.pub, crypto.SHA256, digest[:], raw, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash}) == nil {
		b.verified++
	}
	code, status := b.returnCode, b.status
	b.mu.Unlock()
	if status != http.StatusOK {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	_, _ = io.WriteString(w, `<ebicsResponse xmlns="urn:org:ebics:H004"><header><static><TransactionID>TX-0001</TransactionID></static><mutable><ReturnCode>`+code+`</ReturnCode><ReportText>report</ReportText></mutable></header></ebicsResponse>`)
}

func (b *fakeBank) transport() EBICS {
	return EBICS{URL: b.srv.URL, HostID: "HOST1", PartnerID: "PARTNER1", UserID: "USER1"}
}

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
	if len(bank.uploads) != 1 || bank.verified != 1 {
		t.Fatalf("bank saw %d uploads, %d with a signature that verifies against the Vault key", len(bank.uploads), bank.verified)
	}
	f := bank.uploads[0].fields
	if f["ebicsRequest/header/static/HostID"] != "HOST1" || f["ebicsRequest/header/static/PartnerID"] != "PARTNER1" ||
		f["ebicsRequest/header/static/UserID"] != "USER1" || f["ebicsRequest/header/static/OrderDetails/OrderType"] != "CCT" {
		t.Fatalf("upload header = %+v", f)
	}
	if strings.Contains(f["ebicsRequest/body/SignatureData"], "vault:") {
		t.Fatal("the vault: prefix reached the bank")
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
		if _, err := (EBICS{URL: bad}).Send(context.Background(), pain001, Signature{Bytes: []byte{1}}); err == nil || !strings.Contains(err.Error(), "url") {
			t.Errorf("bank %q: err = %v", bad, err)
		}
	}
	if checkURL("https://bank.example.com/ebics") != nil || checkURL("http://localhost:8080") != nil || checkURL("http://[::1]:8080") != nil {
		t.Fatal("https and loopback http must be accepted")
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
	if len(bank.uploads) != 0 {
		t.Fatal("an unsigned file reached the bank")
	}
	if len(audit.rows) != 2 || audit.rows[1].Event != EventFailed || audit.rows[1].Error == "" {
		t.Fatalf("audit rows = %+v", audit.rows)
	}
}

func TestSender_BankRefusalIsAuditedAsFailed(t *testing.T) {
	vault := newFakeVault(t)
	bank := newFakeBank(t, vault.pub)
	bank.returnCode = "091116"
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
	if vault.calls != 0 || len(bank.uploads) != 0 {
		t.Fatalf("vault calls %d, bank uploads %d; nothing may happen without a durable audit row", vault.calls, len(bank.uploads))
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
	bank.status = http.StatusBadGateway
	audit := &memoryAudit{}
	_, err := Sender{Signer: vault.signer(), Transport: bank.transport(), Audit: audit}.Send(context.Background(), pain001)
	if !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("err = %v", err)
	}
	if len(bank.uploads) != 1 || vault.calls != 1 {
		t.Fatalf("uploads %d, signs %d; an ambiguous failure must not be resent", len(bank.uploads), vault.calls)
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

func TestSender_ConnectionRefusedIsFailedNotUnknown(t *testing.T) {
	vault := newFakeVault(t)
	closed := httptest.NewServer(http.NotFoundHandler())
	addr := closed.URL
	closed.Close()
	audit := &memoryAudit{}
	_, err := Sender{Signer: vault.signer(), Transport: EBICS{URL: addr}, Audit: audit}.Send(context.Background(), pain001)
	if err == nil || errors.Is(err, ErrAmbiguous) {
		t.Fatalf("err = %v", err)
	}
	if len(audit.rows) != 2 || audit.rows[1].Event != EventFailed {
		t.Fatalf("audit rows = %+v", audit.rows)
	}
}
