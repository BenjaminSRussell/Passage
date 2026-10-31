package bankconnect

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
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
	pub    ed25519.PublicKey
	priv   ed25519.PrivateKey
	mu     sync.Mutex
	calls  int
	bodies []string
	status int
	srv    *httptest.Server
}

func newFakeVault(t *testing.T) *fakeVault {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	v := &fakeVault{t: t, pub: pub, priv: priv, status: http.StatusOK}
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
		Input string `json:"input"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	payload, err := base64.StdEncoding.DecodeString(in.Input)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(v.priv, payload))
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"signature": "vault:v3:" + sig, "key_version": 3}})
}

func (v *fakeVault) signer() VaultTransit {
	return VaultTransit{Addr: v.srv.URL, Token: "s.test-token"}
}

type fakeBank struct {
	t          *testing.T
	pub        ed25519.PublicKey
	mu         sync.Mutex
	uploads    []ebicsRequest
	verified   int
	returnCode string
	srv        *httptest.Server
}

func newFakeBank(t *testing.T, pub ed25519.PublicKey) *fakeBank {
	b := &fakeBank{t: t, pub: pub, returnCode: ebicsOK}
	b.srv = httptest.NewServer(http.HandlerFunc(b.serve))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *fakeBank) serve(w http.ResponseWriter, r *http.Request) {
	var req ebicsRequest
	if r.Method != http.MethodPost || xml.NewDecoder(r.Body).Decode(&req) != nil || req.Version != "H004" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	data, _ := base64.StdEncoding.DecodeString(req.OrderData)
	sig := strings.TrimPrefix(req.Signature, "vault:v3:")
	raw, _ := base64.StdEncoding.DecodeString(sig)
	b.mu.Lock()
	b.uploads = append(b.uploads, req)
	if ed25519.Verify(b.pub, data, raw) {
		b.verified++
	}
	code := b.returnCode
	b.mu.Unlock()
	w.Header().Set("Content-Type", "text/xml")
	_, _ = io.WriteString(w, `<ebicsResponse><header><static><TransactionID>TX-0001</TransactionID></static><mutable><ReturnCode>`+code+`</ReturnCode><ReportText>report</ReportText></mutable></header></ebicsResponse>`)
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
	up := bank.uploads[0]
	if up.HostID != "HOST1" || up.PartnerID != "PARTNER1" || up.UserID != "USER1" || up.OrderType != "CCT" {
		t.Fatalf("upload header = %+v", up)
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
	if err := json.Unmarshal([]byte(body), &in); err != nil || len(in) != 1 || in["input"] != base64.StdEncoding.EncodeToString(pain001.Data) {
		t.Fatalf("vault request body = %s", body)
	}
	if strings.Contains(body, base64.StdEncoding.EncodeToString(vault.priv)) {
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
