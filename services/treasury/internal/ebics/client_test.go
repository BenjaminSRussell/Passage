package ebics

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/treasury/internal/bankconnect"
)

var backends = []struct {
	name  string
	vault bool
}{{"memory-keys", false}, {"vault-keys", true}}

func forBackends(t *testing.T, f func(t *testing.T, e *env)) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) { f(t, newEnv(t, b.vault)) })
	}
}

func payload(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return []byte(`<Document xmlns="urn:iso:std:iso:20022:tech:xsd:pain.001.001.03">` + string(bytes.ToValidUTF8(b, []byte("x"))) + `</Document>`)
}

func TestOnboarding_StateMachineAndBankKeyConfirmation(t *testing.T) {
	forBackends(t, func(t *testing.T, e *env) {
		ctx := context.Background()
		if _, err := e.client.SendHIA(ctx, "sub-1"); !errors.Is(err, ErrBadState) {
			t.Fatalf("HIA before INI: %v", err)
		}
		ini, err := e.client.SendINI(ctx, "sub-1")
		if err != nil || e.sub().State != StateINISent {
			t.Fatalf("INI: %v, state %s", err, e.sub().State)
		}
		if want := FormatDigest(KeyDigest(e.bank.userA006)); ini.Hashes[UsageA006] != want || !strings.Contains(ini.Text, want) {
			t.Fatalf("INI letter hash %q, bank computed %q", ini.Hashes[UsageA006], want)
		}
		if _, err := e.client.SendINI(ctx, "sub-1"); !errors.Is(err, ErrBadState) {
			t.Fatalf("second INI: %v", err)
		}
		hia, err := e.client.SendHIA(ctx, "sub-1")
		if err != nil || e.sub().State != StateHIASent {
			t.Fatalf("HIA: %v, state %s", err, e.sub().State)
		}
		if hia.Hashes[UsageX002] != FormatDigest(KeyDigest(e.bank.userX002)) || hia.Hashes[UsageE002] != FormatDigest(KeyDigest(e.bank.userE002)) {
			t.Fatalf("HIA letter hashes %v", hia.Hashes)
		}

		var re *ReturnError
		if _, err := e.client.FetchBankKeys(ctx, "sub-1"); !errors.As(err, &re) || re.Code != CodeAuthFailed || e.sub().State != StateHIASent {
			t.Fatalf("HPB before the bank activated the subscriber: %v, state %s", err, e.sub().State)
		}
		if e.sub().ParkedCode != "" {
			t.Fatal("a refused HPB during onboarding must not park the subscriber")
		}
		e.bank.mu.Lock()
		e.bank.active = true
		e.bank.mu.Unlock()
		h, err := e.client.FetchBankKeys(ctx, "sub-1")
		if err != nil || e.sub().State != StateKeysPending || e.sub().KeysConfirmed {
			t.Fatalf("HPB: %v, state %s", err, e.sub().State)
		}
		if h.X002 != FormatDigest(KeyDigest(&e.bank.x002.PublicKey)) || h.E002 != FormatDigest(KeyDigest(&e.bank.e002.PublicKey)) {
			t.Fatalf("HPB hashes %+v", h)
		}
		if _, err := e.client.Upload(ctx, "sub-1", "CCT", payload(10)); !errors.Is(err, ErrBadState) {
			t.Fatalf("upload before the bank key hashes are confirmed: %v", err)
		}

		other := FormatDigest(KeyDigest(&e.local.a006.PublicKey))
		if _, err := e.client.ConfirmBankKeys(ctx, "sub-1", h.X002, other, "treasurer"); !errors.Is(err, ErrKeyHashMismatch) || e.sub().State != StateKeysPending {
			t.Fatalf("wrong E002 hash: %v, state %s", err, e.sub().State)
		}
		if _, err := e.client.ConfirmBankKeys(ctx, "sub-1", h.X002, h.E002, ""); err == nil {
			t.Fatal("confirmation without a named person must be refused")
		}
		sub, err := e.client.ConfirmBankKeys(ctx, "sub-1", strings.ToLower(strings.ReplaceAll(h.X002, " ", ":")), h.E002, "treasurer")
		if err != nil || sub.State != StateReady || !sub.KeysConfirmed || sub.ConfirmedBy != "treasurer" || sub.ConfirmedAt == nil {
			t.Fatalf("confirm: %v, %+v", err, sub)
		}
	})
}

func TestOnboarding_TamperedBankKeyIsNeverActivated(t *testing.T) {
	e := newEnv(t, false)
	ctx := context.Background()
	_, _ = e.client.SendINI(ctx, "sub-1")
	_, _ = e.client.SendHIA(ctx, "sub-1")
	e.bank.active = true
	if _, err := e.client.FetchBankKeys(ctx, "sub-1"); err != nil {
		t.Fatal(err)
	}
	forged := FormatDigest(KeyDigest(&rsa.PublicKey{N: new(big.Int).Add(e.bank.x002.N, big.NewInt(2)), E: 65537}))
	if _, err := e.client.ConfirmBankKeys(ctx, "sub-1", forged, FormatDigest(KeyDigest(&e.bank.e002.PublicKey)), "treasurer"); !errors.Is(err, ErrKeyHashMismatch) {
		t.Fatalf("a hash that is not the downloaded key's must not confirm: %v", err)
	}
}

func TestUpload_SegmentedSignedEncryptedOrderReachesTheBank(t *testing.T) {
	forBackends(t, func(t *testing.T, e *env) {
		e.onboard()
		e.client.SegmentSize = 700
		data := payload(6000)
		before := e.bank.verifiedRequests
		r, err := e.client.Upload(context.Background(), "sub-1", "CCT", data)
		if err != nil {
			t.Fatal(err)
		}
		if len(e.bank.orders) != 1 || e.bank.orders[0].orderType != "CCT" || !bytes.Equal(e.bank.orders[0].data, data) {
			t.Fatalf("bank orders: %d", len(e.bank.orders))
		}
		if r.TransactionID == "" || r.OrderID == "" {
			t.Fatalf("receipt %+v", r)
		}
		if got := e.bank.verifiedRequests - before; got < 3 {
			t.Fatalf("the bank verified the AuthSignature of %d requests; a segmented upload sends an initialisation and several transfers", got)
		}
		if e.vault != nil && len(e.vault.calls) == 0 {
			t.Fatal("no key operation reached Vault")
		}
	})
}

func TestUpload_UnsupportedAndEmptyOrdersNeverReachTheBank(t *testing.T) {
	e := newEnv(t, false)
	e.onboard()
	before := e.bank.requests
	for _, ot := range []string{"BTU", "C53", "", "INI"} {
		if _, err := e.client.Upload(context.Background(), "sub-1", ot, payload(5)); !errors.Is(err, ErrUnsupportedOrder) {
			t.Errorf("upload order %q: %v", ot, err)
		}
	}
	if _, err := e.client.Upload(context.Background(), "sub-1", "CCT", nil); err == nil {
		t.Error("an empty file must be refused")
	}
	if _, err := e.client.Download(context.Background(), "sub-1", "BTD", "", ""); !errors.Is(err, ErrUnsupportedOrder) {
		t.Errorf("BTD: %v", err)
	}
	if e.bank.requests != before {
		t.Fatalf("bank saw %d requests it should not have", e.bank.requests-before)
	}
}

func zipOf(t *testing.T, files map[string][]byte) []byte {
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, name := range []string{"stmt-1.xml", "stmt-2.xml"} {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write(files[name])
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestDownload_SegmentedZipIsDecryptedUnpackedAndAcknowledged(t *testing.T) {
	forBackends(t, func(t *testing.T, e *env) {
		e.onboard()
		files := map[string][]byte{"stmt-1.xml": payload(3000), "stmt-2.xml": payload(2500)}
		e.bank.served["C53"] = zipOf(t, files)
		e.bank.segChars = 900
		got, err := e.client.Download(context.Background(), "sub-1", "C53", "2026-11-01", "2026-11-08")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0].Name != "stmt-1.xml" || !bytes.Equal(got[0].Data, files["stmt-1.xml"]) || !bytes.Equal(got[1].Data, files["stmt-2.xml"]) {
			t.Fatalf("downloaded %d files", len(got))
		}
		if len(e.bank.receipts) != 1 || e.bank.receipts[0] != "0" {
			t.Fatalf("receipts %v; a good download is acknowledged positively once", e.bank.receipts)
		}
	})
}

func TestDownload_PlainFileSingleSegmentAndNoData(t *testing.T) {
	e := newEnv(t, false)
	e.onboard()
	e.bank.served["HAC"] = []byte("<status/>")
	got, err := e.client.Download(context.Background(), "sub-1", "HAC", "", "")
	if err != nil || len(got) != 1 || string(got[0].Data) != "<status/>" {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := e.client.Download(context.Background(), "sub-1", "C52", "", ""); !errors.Is(err, ErrNoData) {
		t.Fatalf("no data: %v", err)
	}
	if e.sub().ParkedCode != "" {
		t.Fatal("an empty download is not a refusal")
	}
}

func TestDownload_UndecryptableDataIsAcknowledgedNegatively(t *testing.T) {
	e := newEnv(t, false)
	e.onboard()
	e.bank.served["C53"] = payload(100)
	e.bank.tamper = func(b []byte) []byte {
		i, j := bytes.Index(b, []byte("<OrderData>")), bytes.Index(b, []byte("</OrderData>"))
		return append(append(b[:i+len("<OrderData>"):i+len("<OrderData>")], []byte(b64(make([]byte, 32)))...), b[j:]...)
	}
	if _, err := e.client.Download(context.Background(), "sub-1", "C53", "", ""); err == nil {
		t.Fatal("garbled order data must fail")
	}
	if len(e.bank.receipts) != 1 || e.bank.receipts[0] != "1" {
		t.Fatalf("receipts %v; a failed download is acknowledged negatively so the bank keeps it", e.bank.receipts)
	}
}

func TestBackoff_RetriesBackOffCodesWithGrowingDelaysThenSucceeds(t *testing.T) {
	e := newEnv(t, false)
	e.onboard()
	e.bank.failNext("init:CCT", CodeInternalError, 2)
	if _, err := e.client.Upload(context.Background(), "sub-1", "CCT", payload(50)); err != nil {
		t.Fatal(err)
	}
	if len(e.sleeps) != 2 || e.sleeps[0] != 2*time.Second || e.sleeps[1] != 4*time.Second {
		t.Fatalf("sleeps %v", e.sleeps)
	}
	if len(e.bank.orders) != 1 {
		t.Fatalf("orders %d", len(e.bank.orders))
	}
}

func TestBackoff_ExhaustedRetriesFailWithoutParking(t *testing.T) {
	e := newEnv(t, false)
	e.onboard()
	e.bank.failNext("init:CCT", CodeInternalError, 100)
	_, err := e.client.Upload(context.Background(), "sub-1", "CCT", payload(50))
	var re *ReturnError
	if !errors.As(err, &re) || re.Disposition != BackOff || e.sub().ParkedCode != "" {
		t.Fatalf("err %v, parked %q", err, e.sub().ParkedCode)
	}
	if len(e.sleeps) != DefaultBackoff.Attempts-1 {
		t.Fatalf("sleeps %v", e.sleeps)
	}
}

func TestPark_RefusalNeedingAPersonParksUntilUnparked(t *testing.T) {
	for _, code := range []string{CodeSignatureFailed, CodeAuthFailed, CodeBankKeyUpdate, "091999"} {
		e := newEnv(t, false)
		e.onboard()
		e.bank.failNext("init:CCT", code, 1)
		_, err := e.client.Upload(context.Background(), "sub-1", "CCT", payload(50))
		var re *ReturnError
		if !errors.As(err, &re) || re.Disposition != Park || e.sub().ParkedCode != code {
			t.Fatalf("%s: err %v, parked %q", code, err, e.sub().ParkedCode)
		}
		before := e.bank.requests
		if _, err := e.client.Upload(context.Background(), "sub-1", "CCT", payload(50)); !errors.Is(err, ErrParked) {
			t.Fatalf("%s: upload while parked: %v", code, err)
		}
		if _, err := e.client.Download(context.Background(), "sub-1", "C53", "", ""); !errors.Is(err, ErrParked) {
			t.Fatalf("%s: download while parked: %v", code, err)
		}
		if e.bank.requests != before {
			t.Fatalf("%s: a parked subscriber still contacted the bank", code)
		}
		if _, err := e.client.Unpark(context.Background(), "sub-1", "treasurer"); err != nil {
			t.Fatal(err)
		}
		if _, err := e.client.Upload(context.Background(), "sub-1", "CCT", payload(50)); err != nil {
			t.Fatalf("%s: after unpark: %v", code, err)
		}
	}
}

func TestPark_StaleBankKeyIsRefusedByTheBankAndParked(t *testing.T) {
	e := newEnv(t, false)
	e.onboard()
	s := e.sub()
	stale := e.local.a006.PublicKey
	s.BankE002 = mustMarshalPKIX(t, &stale)
	if _, err := e.store.UpdateSubscriber(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	var re *ReturnError
	if _, err := e.client.Upload(context.Background(), "sub-1", "CCT", payload(50)); !errors.As(err, &re) || re.Code != CodeBankKeyUpdate || e.sub().ParkedCode != CodeBankKeyUpdate {
		t.Fatalf("err %v", err)
	}
}

func TestUpload_FinalSegmentBackOffIsAmbiguousAndNotResent(t *testing.T) {
	e := newEnv(t, false)
	e.onboard()
	e.client.SegmentSize = 300
	e.bank.failNext("transfer:last", CodeInternalError, 1)
	_, err := e.client.Upload(context.Background(), "sub-1", "CCT", payload(1200))
	if !errors.Is(err, bankconnect.ErrAmbiguous) {
		t.Fatalf("err %v", err)
	}
	if len(e.sleeps) != 0 || len(e.bank.txns) != 1 {
		t.Fatalf("sleeps %v, transactions %d; an upload whose last segment is unconfirmed must not be restarted", e.sleeps, len(e.bank.txns))
	}
}

func TestUpload_MidTransferBackOffRestartsTheWholeTransaction(t *testing.T) {
	e := newEnv(t, false)
	e.onboard()
	e.client.SegmentSize = 300
	e.bank.failNext("transfer:2", CodeTxAbort, 1)
	if _, err := e.client.Upload(context.Background(), "sub-1", "CCT", payload(1200)); err != nil {
		t.Fatal(err)
	}
	if len(e.bank.orders) != 1 || len(e.bank.txns) != 2 {
		t.Fatalf("orders %d, transactions %d", len(e.bank.orders), len(e.bank.txns))
	}
}

func TestResponses_ForgedOrTamperedAnswersAreRejected(t *testing.T) {
	t.Run("signed with another key", func(t *testing.T) {
		e := newEnv(t, false)
		e.onboard()
		e.bank.signWith = e.local.a006
		_, err := e.client.Upload(context.Background(), "sub-1", "CCT", payload(50))
		if !errors.Is(err, errAuthSignature) || len(e.bank.orders) != 0 {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("return code rewritten in transit", func(t *testing.T) {
		e := newEnv(t, false)
		e.onboard()
		e.bank.served["C53"] = payload(50)
		e.bank.tamper = func(b []byte) []byte {
			return bytes.Replace(b, []byte(`<ReturnCode authenticate="true">000000`), []byte(`<ReturnCode authenticate="true">011000`), 1)
		}
		if _, err := e.client.Download(context.Background(), "sub-1", "C53", "", ""); !errors.Is(err, errAuthSignature) {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("signature element stripped", func(t *testing.T) {
		e := newEnv(t, false)
		e.onboard()
		e.bank.tamper = func(b []byte) []byte {
			i, j := bytes.Index(b, []byte("<AuthSignature>")), bytes.Index(b, []byte("</AuthSignature>"))
			return append(b[:i:i], b[j+len("</AuthSignature>"):]...)
		}
		if _, err := e.client.Upload(context.Background(), "sub-1", "CCT", payload(50)); !errors.Is(err, errAuthSignature) {
			t.Fatalf("err %v", err)
		}
	})
}

func TestBank_RejectsRequestsSignedWithAnotherKey(t *testing.T) {
	e := newEnv(t, false)
	e.onboard()
	e.local.x002 = e.local.a006
	var re *ReturnError
	_, err := e.client.Upload(context.Background(), "sub-1", "CCT", payload(50))
	if !errors.As(err, &re) || re.Code != CodeAuthFailed || len(e.bank.orders) != 0 {
		t.Fatalf("err %v", err)
	}
}

func TestUpload_ConnectionRefusedIsADefiniteFailure(t *testing.T) {
	e := newEnv(t, false)
	e.onboard()
	s := e.sub()
	s.URL = "http://127.0.0.1:1"
	_, _ = e.store.UpdateSubscriber(context.Background(), s)
	_, err := e.client.Upload(context.Background(), "sub-1", "CCT", payload(50))
	if err == nil || errors.Is(err, bankconnect.ErrAmbiguous) {
		t.Fatalf("err %v", err)
	}
}

func TestTransport_SenderAuditsAnEBICSUpload(t *testing.T) {
	e := newEnv(t, false)
	e.onboard()
	audit := &recordingAudit{}
	data := payload(400)
	sender := bankconnect.Sender{Signer: stubSigner{}, Transport: Transport{Client: e.client, SubscriberID: "sub-1"}, Audit: audit}
	r, err := sender.Send(context.Background(), bankconnect.File{LegalEntityID: "le-1", BankAccountID: "ba-1", Name: "pain001.xml", OrderType: "CCT", KeyName: "ebics-a006", Data: data})
	if err != nil || r.Reference == "" {
		t.Fatalf("%v %+v", err, r)
	}
	if len(audit.rows) != 2 || audit.rows[0].Event != bankconnect.EventPrepared || audit.rows[1].Event != bankconnect.EventSent || audit.rows[1].Protocol != "ebics" || audit.rows[1].BankReference != r.Reference {
		t.Fatalf("audit %+v", audit.rows)
	}
	if len(e.bank.orders) != 1 || !bytes.Equal(e.bank.orders[0].data, data) {
		t.Fatal("bank did not get the file")
	}
}

type stubSigner struct{}

func (stubSigner) Sign(context.Context, string, []byte) (bankconnect.Signature, error) {
	return bankconnect.Signature{Bytes: []byte{1}, KeyVersion: 1}, nil
}

type recordingAudit struct{ rows []bankconnect.AuditEntry }

func (r *recordingAudit) RecordBankFile(_ context.Context, e bankconnect.AuditEntry) error {
	r.rows = append(r.rows, e)
	return nil
}

func TestSubscriberStore_StaleWriteIsRefused(t *testing.T) {
	e := newEnv(t, false)
	stale := e.sub()
	if _, err := e.store.UpdateSubscriber(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.UpdateSubscriber(context.Background(), stale); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("err %v", err)
	}
}

func TestSubscriber_RejectsUnsafeIdentifiers(t *testing.T) {
	for _, bad := range []string{"", "A B", "<x>", strings.Repeat("A", 36), "a/b"} {
		s := Subscriber{HostID: bad, PartnerID: "P", UserID: "U"}
		if s.Validate() == nil {
			t.Errorf("HostID %q accepted", bad)
		}
	}
}

func mustMarshalPKIX(t *testing.T, pub *rsa.PublicKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func TestFetchStatements_EachFileGoesToTheSinkAndOneRefusalDoesNotStopTheRest(t *testing.T) {
	e := newEnv(t, false)
	e.onboard()
	e.bank.served["C53"] = zipOf(t, map[string][]byte{"stmt-1.xml": []byte("<a/>"), "stmt-2.xml": []byte("<b/>")})
	var seen []string
	sink := func(_ context.Context, account, name string, data []byte) error {
		if account != "ba-1" {
			t.Errorf("sink got bank account %q; the subscriber's own account is expected", account)
		}
		seen = append(seen, name+":"+string(data))
		if name == "stmt-1.xml" {
			return errors.New("unparsable")
		}
		return nil
	}
	n, err := e.client.FetchStatements(context.Background(), "sub-1", "C53", "", "", sink)
	if n != 1 || err == nil || !strings.Contains(err.Error(), "stmt-1.xml") || len(seen) != 2 {
		t.Fatalf("taken %d, err %v, seen %v", n, err, seen)
	}
	if _, err := e.client.FetchStatements(context.Background(), "sub-1", "HAC", "", "", sink); !errors.Is(err, ErrUnsupportedOrder) {
		t.Fatalf("HAC carries no statements: %v", err)
	}
}
