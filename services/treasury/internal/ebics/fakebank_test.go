package ebics

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/treasury/internal/bankconnect"
)

var (
	keyOnce sync.Once
	keyPool []*rsa.PrivateKey
)

func testKeys(t *testing.T) []*rsa.PrivateKey {
	t.Helper()
	keyOnce.Do(func() {
		for i := 0; i < 6; i++ {
			k, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				panic(err)
			}
			keyPool = append(keyPool, k)
		}
	})
	return keyPool
}

type memoryKeys struct{ a006, x002, e002 *rsa.PrivateKey }

func (m memoryKeys) Public(_ context.Context, u KeyUsage) (*rsa.PublicKey, error) {
	switch u {
	case UsageA006:
		return &m.a006.PublicKey, nil
	case UsageX002:
		return &m.x002.PublicKey, nil
	case UsageE002:
		return &m.e002.PublicKey, nil
	}
	return nil, fmt.Errorf("usage %s", u)
}

func (m memoryKeys) SignA006(_ context.Context, orderHash []byte) ([]byte, error) {
	d := sha256.Sum256(orderHash)
	return rsa.SignPSS(rand.Reader, m.a006, crypto.SHA256, d[:], &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
}

func (m memoryKeys) SignX002(_ context.Context, signedInfo []byte) ([]byte, error) {
	d := sha256.Sum256(signedInfo)
	return rsa.SignPKCS1v15(rand.Reader, m.x002, crypto.SHA256, d[:])
}

func (m memoryKeys) DecryptE002(_ context.Context, wrapped []byte) ([]byte, error) {
	return rsa.DecryptPKCS1v15(rand.Reader, m.e002, wrapped)
}

type memStore struct {
	mu   sync.Mutex
	subs map[string]Subscriber
}

func (m *memStore) GetSubscriber(_ context.Context, id string) (Subscriber, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.subs[id]
	if !ok {
		return Subscriber{}, fmt.Errorf("subscriber %s not found", id)
	}
	return s, nil
}

func (m *memStore) UpdateSubscriber(_ context.Context, s Subscriber) (Subscriber, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.subs[s.ID].Version != s.Version {
		return Subscriber{}, ErrVersionConflict
	}
	s.Version++
	m.subs[s.ID] = s
	return s, nil
}

type bankFault struct {
	code      string
	remaining int
}

type bankTxn struct {
	id          string
	upload      bool
	orderType   string
	key         []byte
	a006        []byte
	numSegments int
	got         []string
	served      []string
}

type receivedOrder struct {
	orderType string
	data      []byte
}

// fakeBank is an in-process EBICS H004 bank. It checks every AuthSignature
// against the subscriber's X002 key, unwraps and decrypts uploads with its own
// E002 key, verifies the A006 signature over the reassembled order, serves
// downloads encrypted for the subscriber's E002 key in segments, and signs its
// answers with its own X002 key.
type fakeBank struct {
	srv      *httptest.Server
	hostID   string
	x002     *rsa.PrivateKey
	e002     *rsa.PrivateKey
	signWith *rsa.PrivateKey

	mu               sync.Mutex
	userA006         *rsa.PublicKey
	userX002         *rsa.PublicKey
	userE002         *rsa.PublicKey
	active           bool
	nextTx           int
	txns             map[string]*bankTxn
	orders           []receivedOrder
	served           map[string][]byte
	segChars         int
	faults           map[string]*bankFault
	requests         int
	verifiedRequests int
	receipts         []string
	tamper           func([]byte) []byte
}

func newFakeBank(t *testing.T, keys []*rsa.PrivateKey) *fakeBank {
	b := &fakeBank{hostID: "BANKHOST", x002: keys[3], e002: keys[4], txns: map[string]*bankTxn{}, served: map[string][]byte{}, faults: map[string]*bankFault{}, segChars: 1 << 20}
	b.srv = httptest.NewServer(http.HandlerFunc(b.serve))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *fakeBank) failNext(key, code string, times int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.faults[key] = &bankFault{code: code, remaining: times}
}

func (b *fakeBank) fault(key string) string {
	if f := b.faults[key]; f != nil && f.remaining > 0 {
		f.remaining--
		return f.code
	}
	return ""
}

func (b *fakeBank) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.requests++
	root, err := parseXML(raw)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var out []byte
	switch root.local {
	case "ebicsUnsecuredRequest":
		out = b.unsecured(root)
	case "ebicsNoPubKeyDigestsRequest":
		out = b.hpb(root)
	case "ebicsRequest":
		out = b.standard(root)
	default:
		http.Error(w, "unknown request", http.StatusBadRequest)
		return
	}
	if b.tamper != nil {
		out = b.tamper(out)
	}
	w.Header().Set("Content-Type", "text/xml")
	_, _ = w.Write(out)
}

func (b *fakeBank) keyMgmt(tech, biz, body string) []byte {
	return []byte(xmlDecl + `<ebicsKeyManagementResponse` + rootAttrs + `><header authenticate="true"><static></static><mutable><ReturnCode>` + tech + `</ReturnCode><ReportText>[` + tech + `]</ReportText></mutable></header>` +
		`<body>` + body + `<ReturnCode authenticate="true">` + biz + `</ReturnCode></body></ebicsKeyManagementResponse>`)
}

func (b *fakeBank) signed(tech, biz, static, mutable, body string) []byte {
	doc := xmlDecl + `<ebicsResponse` + rootAttrs + `><header authenticate="true"><static>` + static + `</static><mutable>` + mutable +
		`<ReturnCode>` + tech + `</ReturnCode><ReportText>[` + tech + `]</ReportText></mutable></header>` + authMarker +
		`<body>` + body + `<ReturnCode authenticate="true">` + biz + `</ReturnCode></body></ebicsResponse>`
	key := b.x002
	if b.signWith != nil {
		key = b.signWith
	}
	out, err := signRequest(context.Background(), memoryKeys{x002: key}, doc)
	if err != nil {
		panic(err)
	}
	return out
}

func unwrapOrder(s string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	return decompress(raw)
}

func (b *fakeBank) unsecured(root *xnode) []byte {
	order := root.path("header", "static", "OrderDetails", "OrderType").content()
	if code := b.fault(order); code != "" {
		return b.keyMgmt(code, "", "")
	}
	data, err := unwrapOrder(root.path("body", "DataTransfer", "OrderData").content())
	if err != nil {
		return b.keyMgmt("091010", "", "")
	}
	doc, err := parseXML(data)
	if err != nil {
		return b.keyMgmt("091010", "", "")
	}
	switch order {
	case "INI":
		b.userA006, err = parseRSAKeyValue(doc.child("SignaturePubKeyInfo"))
	case "HIA":
		if b.userX002, err = parseRSAKeyValue(doc.child("AuthenticationPubKeyInfo")); err == nil {
			b.userE002, err = parseRSAKeyValue(doc.child("EncryptionPubKeyInfo"))
		}
	default:
		return b.keyMgmt("091005", "", "")
	}
	if err != nil {
		return b.keyMgmt("091010", "", "")
	}
	return b.keyMgmt(CodeOK, CodeOK, "")
}

func (b *fakeBank) hpb(root *xnode) []byte {
	if code := b.fault("HPB"); code != "" {
		return b.keyMgmt(code, "", "")
	}
	if !b.active || b.userX002 == nil || verifyAuthSignature(root, b.userX002) != nil {
		return b.keyMgmt(CodeAuthFailed, "", "")
	}
	b.verifiedRequests++
	data := `<?xml version="1.0" encoding="UTF-8"?><HPBResponseOrderData xmlns="` + nsH004 + `" xmlns:ds="` + nsDS + `">` +
		`<AuthenticationPubKeyInfo><PubKeyValue>` + rsaKeyValue(&b.x002.PublicKey) + `</PubKeyValue><AuthenticationVersion>X002</AuthenticationVersion></AuthenticationPubKeyInfo>` +
		`<EncryptionPubKeyInfo><PubKeyValue>` + rsaKeyValue(&b.e002.PublicKey) + `</PubKeyValue><EncryptionVersion>E002</EncryptionVersion></EncryptionPubKeyInfo><HostID>` + b.hostID + `</HostID></HPBResponseOrderData>`
	enc, wrapped, _, err := encryptOrderData([]byte(data), b.userE002)
	if err != nil {
		panic(err)
	}
	return b.keyMgmt(CodeOK, CodeOK, `<DataTransfer><DataEncryptionInfo authenticate="true"><EncryptionPubKeyDigest Version="E002" Algorithm="`+algSHA256+`">`+b64(KeyDigest(b.userE002))+
		`</EncryptionPubKeyDigest><TransactionKey>`+b64(wrapped)+`</TransactionKey></DataEncryptionInfo><OrderData>`+b64(enc)+`</OrderData></DataTransfer>`)
}

func (b *fakeBank) refuse(code string) []byte { return b.signed(code, code, "", "", "") }

func (b *fakeBank) standard(root *xnode) []byte {
	header := root.child("header")
	if b.userX002 == nil || verifyAuthSignature(root, b.userX002) != nil {
		return b.refuse(CodeAuthFailed)
	}
	b.verifiedRequests++
	if header.path("static", "HostID").content() != b.hostID {
		return b.refuse("091011")
	}
	switch header.path("mutable", "TransactionPhase").content() {
	case "Initialisation":
		return b.initPhase(root, header)
	case "Transfer":
		return b.transferPhase(root, header)
	case "Receipt":
		tx := b.txns[header.path("static", "TransactionID").content()]
		if tx == nil {
			return b.refuse(CodeTxUnknown)
		}
		if code := b.fault("receipt"); code != "" {
			return b.refuse(code)
		}
		b.receipts = append(b.receipts, root.path("body", "TransferReceipt", "ReceiptCode").content())
		return b.signed(CodeOK, CodeDownloadDone, "", "<TransactionPhase>Receipt</TransactionPhase>", "")
	}
	return b.refuse("091010")
}

func (b *fakeBank) initPhase(root, header *xnode) []byte {
	digests := header.path("static", "BankPubKeyDigests")
	if digests.child("Authentication").content() != b64(KeyDigest(&b.x002.PublicKey)) || digests.child("Encryption").content() != b64(KeyDigest(&b.e002.PublicKey)) {
		return b.refuse(CodeBankKeyUpdate)
	}
	order := header.path("static", "OrderDetails", "OrderType").content()
	if code := b.fault("init:" + order); code != "" {
		return b.refuse(code)
	}
	b.nextTx++
	tx := &bankTxn{id: fmt.Sprintf("TX%04d", b.nextTx), orderType: order}
	b.txns[tx.id] = tx
	if _, ok := uploadOrders[order]; ok {
		info := root.path("body", "DataTransfer", "DataEncryptionInfo")
		wrapped, _ := base64.StdEncoding.DecodeString(info.child("TransactionKey").content())
		key, err := rsa.DecryptPKCS1v15(rand.Reader, b.e002, wrapped)
		if err != nil {
			return b.refuse("091004")
		}
		sealed, _ := base64.StdEncoding.DecodeString(root.path("body", "DataTransfer", "SignatureData").content())
		sigXML, err := openWithKey(key, sealed)
		if err != nil {
			return b.refuse("091004")
		}
		sd, err := parseXML(sigXML)
		if err != nil || sd.path("OrderSignatureData", "SignatureVersion").content() != "A006" {
			return b.refuse("091004")
		}
		tx.upload, tx.key = true, key
		tx.a006, _ = base64.StdEncoding.DecodeString(sd.path("OrderSignatureData", "SignatureValue").content())
		tx.numSegments, _ = strconv.Atoi(header.path("static", "NumSegments").content())
		return b.signed(CodeOK, CodeOK, "<TransactionID>"+tx.id+"</TransactionID>", "<TransactionPhase>Initialisation</TransactionPhase>", "")
	}
	data, ok := b.served[order]
	if !ok {
		return b.signed(CodeOK, CodeNoDownloadData, "", "", "")
	}
	enc, wrapped, _, err := encryptOrderData(data, b.userE002)
	if err != nil {
		panic(err)
	}
	tx.served = segments(b64(enc), b.segChars)
	return b.downloadReply(tx, 1, wrapped)
}

func (b *fakeBank) downloadReply(tx *bankTxn, n int, wrapped []byte) []byte {
	last := ""
	if n == len(tx.served) {
		last = ` lastSegment="true"`
	}
	phase := "Transfer"
	info := ""
	static := "<TransactionID>" + tx.id + "</TransactionID>"
	if n == 1 {
		phase = "Initialisation"
		static += "<NumSegments>" + strconv.Itoa(len(tx.served)) + "</NumSegments>"
		info = `<DataEncryptionInfo authenticate="true"><EncryptionPubKeyDigest Version="E002" Algorithm="` + algSHA256 + `">` + b64(KeyDigest(b.userE002)) +
			`</EncryptionPubKeyDigest><TransactionKey>` + b64(wrapped) + `</TransactionKey></DataEncryptionInfo>`
	}
	return b.signed(CodeOK, CodeOK, static, "<TransactionPhase>"+phase+"</TransactionPhase><SegmentNumber"+last+">"+strconv.Itoa(n)+"</SegmentNumber>",
		"<DataTransfer>"+info+"<OrderData>"+tx.served[n-1]+"</OrderData></DataTransfer>")
}

func (b *fakeBank) transferPhase(root, header *xnode) []byte {
	tx := b.txns[header.path("static", "TransactionID").content()]
	if tx == nil {
		return b.refuse(CodeTxUnknown)
	}
	seg, _ := strconv.Atoi(header.path("mutable", "SegmentNumber").content())
	last := header.path("mutable", "SegmentNumber").attr("lastSegment") == "true"
	if !tx.upload {
		if seg < 2 || seg > len(tx.served) {
			return b.refuse("091015")
		}
		return b.downloadReply(tx, seg, nil)
	}
	if seg != len(tx.got)+1 || last != (seg == tx.numSegments) {
		return b.refuse(CodeTxReplay)
	}
	key := "transfer:" + strconv.Itoa(seg)
	if last {
		key = "transfer:last"
	}
	if code := b.fault(key); code != "" {
		return b.refuse(code)
	}
	tx.got = append(tx.got, root.path("body", "DataTransfer", "OrderData").content())
	if !last {
		return b.signed(CodeOK, CodeOK, "", "<TransactionPhase>Transfer</TransactionPhase>", "")
	}
	enc, err := base64.StdEncoding.DecodeString(strings.Join(tx.got, ""))
	if err != nil {
		return b.refuse("091010")
	}
	plain, err := openWithKey(tx.key, enc)
	if err != nil {
		return b.refuse("091104")
	}
	once := sha256.Sum256([]byte(strings.NewReplacer("\r", "", "\n", "", "\x1a", "").Replace(string(plain))))
	twice := sha256.Sum256(once[:])
	if rsa.VerifyPSS(b.userA006, crypto.SHA256, twice[:], tx.a006, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash}) != nil {
		return b.refuse(CodeSignatureFailed)
	}
	b.orders = append(b.orders, receivedOrder{orderType: tx.orderType, data: plain})
	return b.signed(CodeOK, CodeOK, "", "<TransactionPhase>Transfer</TransactionPhase><OrderID>A"+tx.id+"</OrderID>", "")
}

type fakeVault struct {
	srv   *httptest.Server
	a006  *rsa.PrivateKey
	x002  *rsa.PrivateKey
	e002  *rsa.PrivateKey
	calls []string
	mu    sync.Mutex
}

func newFakeVault(t *testing.T, a006, x002, e002 *rsa.PrivateKey) *fakeVault {
	v := &fakeVault{a006: a006, x002: x002, e002: e002}
	v.srv = httptest.NewServer(http.HandlerFunc(v.serve))
	t.Cleanup(v.srv.Close)
	return v
}

func (v *fakeVault) serve(w http.ResponseWriter, r *http.Request) {
	v.mu.Lock()
	v.calls = append(v.calls, r.Method+" "+r.URL.Path)
	v.mu.Unlock()
	if r.Header.Get("X-Vault-Token") != "s.test" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	keyFor := map[string]*rsa.PrivateKey{"ebics-a006": v.a006, "ebics-x002": v.x002}
	p := strings.TrimPrefix(r.URL.Path, "/v1/")
	reply := func(data string) { _, _ = io.WriteString(w, `{"data":`+data+`}`) }
	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(p, "transit/keys/"):
		k := keyFor[strings.TrimPrefix(p, "transit/keys/")]
		if k == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
		pemText := strings.ReplaceAll(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), "\n", `\n`)
		reply(`{"type":"rsa-2048","exportable":false,"allow_plaintext_backup":false,"latest_version":1,"keys":{"1":{"public_key":"` + pemText + `"}}}`)
	case r.Method == http.MethodPost && strings.HasPrefix(p, "transit/sign/"):
		name := strings.TrimSuffix(strings.TrimPrefix(p, "transit/sign/"), "/sha2-256")
		k := keyFor[name]
		var in struct {
			Input, SignatureAlgorithm string
			SaltLength                string `json:"salt_length"`
		}
		raw, _ := io.ReadAll(r.Body)
		in.Input, in.SignatureAlgorithm = jsonField(raw, "input"), jsonField(raw, "signature_algorithm")
		input, _ := base64.StdEncoding.DecodeString(in.Input)
		d := sha256.Sum256(input)
		var sig []byte
		var err error
		switch in.SignatureAlgorithm {
		case "pss":
			sig, err = rsa.SignPSS(rand.Reader, k, crypto.SHA256, d[:], &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
		case "pkcs1v15":
			sig, err = rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, d[:])
		default:
			err = fmt.Errorf("algorithm %q", in.SignatureAlgorithm)
		}
		if k == nil || err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		reply(`{"signature":"vault:v1:` + base64.StdEncoding.EncodeToString(sig) + `","key_version":1}`)
	case r.Method == http.MethodGet && p == "secret/data/passage/ebics-e002":
		text := strings.ReplaceAll(string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: mustPKCS8(v.e002)})), "\n", `\n`)
		reply(`{"data":{"pem":"` + text + `"}}`)
	case r.Method == http.MethodGet && p == "secret/data/passage/ebics-e002-pub":
		der, _ := x509.MarshalPKIXPublicKey(&v.e002.PublicKey)
		text := strings.ReplaceAll(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), "\n", `\n`)
		reply(`{"data":{"pem":"` + text + `"}}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func mustPKCS8(k *rsa.PrivateKey) []byte {
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		panic(err)
	}
	return der
}

func jsonField(raw []byte, name string) string {
	i := bytes.Index(raw, []byte(`"`+name+`":"`))
	if i < 0 {
		return ""
	}
	rest := raw[i+len(name)+4:]
	return string(rest[:bytes.IndexByte(rest, '"')])
}

type env struct {
	t      *testing.T
	bank   *fakeBank
	client *Client
	store  *memStore
	vault  *fakeVault
	sleeps []time.Duration
	local  memoryKeys
}

func newEnv(t *testing.T, useVault bool) *env {
	keys := testKeys(t)
	e := &env{t: t, bank: newFakeBank(t, keys), store: &memStore{subs: map[string]Subscriber{}}, local: memoryKeys{a006: keys[0], x002: keys[1], e002: keys[2]}}
	e.store.subs["sub-1"] = Subscriber{ID: "sub-1", LegalEntityID: "le-1", BankAccountID: "ba-1", HostID: "BANKHOST", PartnerID: "PARTNER1", UserID: "USER1",
		URL: e.bank.srv.URL, A006Key: "ebics-a006", X002Key: "ebics-x002", E002KVPath: "passage/ebics-e002", State: StateNew}
	c := &Client{Store: e.store, Sleep: func(_ context.Context, d time.Duration) error { e.sleeps = append(e.sleeps, d); return nil }}
	if useVault {
		e.vault = newFakeVault(t, keys[0], keys[1], keys[2])
		vault := bankconnect.NewVault(bankconnect.VaultConfig{Addr: e.vault.srv.URL, Token: "s.test", SigningKeyNames: []string{"ebics-a006", "ebics-x002"}})
		c.Keys = func(s Subscriber) (Keys, error) {
			k, err := vault.EBICSKeys(bankconnect.EBICSKeyRefs{A006: s.A006Key, X002: s.X002Key, E002KVMount: "secret", E002KVPath: s.E002KVPath})
			return k, err
		}
	} else {
		c.Keys = func(Subscriber) (Keys, error) { return e.local, nil }
	}
	e.client = c
	return e
}

func (e *env) sub() Subscriber {
	s, _ := e.store.GetSubscriber(context.Background(), "sub-1")
	return s
}

func (e *env) onboard() {
	e.t.Helper()
	ctx := context.Background()
	if _, err := e.client.SendINI(ctx, "sub-1"); err != nil {
		e.t.Fatalf("INI: %v", err)
	}
	if _, err := e.client.SendHIA(ctx, "sub-1"); err != nil {
		e.t.Fatalf("HIA: %v", err)
	}
	e.bank.mu.Lock()
	e.bank.active = true
	e.bank.mu.Unlock()
	h, err := e.client.FetchBankKeys(ctx, "sub-1")
	if err != nil {
		e.t.Fatalf("HPB: %v", err)
	}
	if _, err := e.client.ConfirmBankKeys(ctx, "sub-1", h.X002, h.E002, "treasurer"); err != nil {
		e.t.Fatalf("confirm: %v", err)
	}
}
