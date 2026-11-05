package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"

	"github.com/BenjaminSRussell/passage/services/edi/internal/delivery"
	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	ediev "github.com/BenjaminSRussell/passage/services/edi/internal/events"
	"github.com/BenjaminSRussell/passage/services/edi/internal/secrets"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
	"github.com/BenjaminSRussell/passage/services/edi/internal/transport"
	"github.com/BenjaminSRussell/passage/services/edi/internal/transport/transporttest"
	"github.com/BenjaminSRussell/passage/services/edi/migrations"
)

// partnerAS2 is the trading partner's own AS2 software: an AS2 server that
// trusts our certificate and keeps what it receives.
type partnerAS2 struct {
	srv       *httptest.Server
	as2       *transport.AS2Server
	mu        sync.Mutex
	got       []transport.Message
	fail      error
	asyncMDNs []transport.AsyncMDN
}

func (p *partnerAS2) messages() []transport.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]transport.Message(nil), p.got...)
}

// transportEnv is the edi service over a migrated schema with the
// application role (so grants and row-level security apply), its AS2
// listener, the dispatcher, and one partner (DISTRIBUTOR) with a trade profile
// in legal entity srcLE.
type transportEnv struct {
	t       *testing.T
	a       *API
	h       http.Handler
	as2     *httptest.Server
	admin   *pgxpool.Pool
	app     *pgxpool.Pool
	st      *store.Store
	partner string
	us      transporttest.KeyPair
	them    transporttest.KeyPair
	secrets secrets.Map
	disp    *delivery.Dispatcher
	peer    *partnerAS2
}

func newTransportEnv(t *testing.T) *transportEnv {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	e := &transportEnv{t: t, admin: admin, app: app, st: store.New(app, 30),
		us: transporttest.NewKeyPair(t, "PASSAGE", false), them: transporttest.NewKeyPair(t, "DISTRIBUTOR", false)}
	e.secrets = secrets.Map{"as2-passage": e.us.PEM(t)}
	e.a = New(e.st)
	e.a.AllowBodyOutbound = true
	e.a.AllowInsecureAS2 = true // httptest servers are http
	e.a.Secrets = e.secrets
	e.as2 = httptest.NewServer(e.a.AS2Routes())
	t.Cleanup(e.as2.Close)
	e.a.AS2PublicURL = e.as2.URL
	e.h = e.a.Routes()
	e.disp = &delivery.Dispatcher{Store: e.st, Secrets: e.secrets, HTTP: http.DefaultClient, AS2PublicURL: e.as2.URL}
	rec := do(t, e.h, http.MethodPost, "/edi/v1/partners", distributor, "edi.admin", map[string]string{"Idempotency-Key": "p"})
	var p store.Partner
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || rec.Code != http.StatusCreated {
		t.Fatalf("partner: %d %s", rec.Code, rec.Body.String())
	}
	e.partner = p.ID
	e.peer = e.startPartner()
	return e
}

// startPartner runs the partner's AS2 server.
func (e *transportEnv) startPartner() *partnerAS2 {
	p := &partnerAS2{}
	p.as2 = &transport.AS2Server{
		Us: transport.AS2Identity{ID: "DISTRIBUTOR", Cert: e.them.Cert, Key: e.them.Key},
		Partner: func(from string) (transport.AS2Partner, bool) {
			return transport.AS2Partner{ID: "PASSAGE", Cert: e.us.Cert, RequireSigned: true}, from == "PASSAGE"
		},
		Deliver: func(_ context.Context, _ string, m transport.Message) error {
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.fail != nil {
				return p.fail
			}
			p.got = append(p.got, m)
			return nil
		},
	}
	p.srv = httptest.NewServer(p.as2)
	e.t.Cleanup(p.srv.Close)
	return p
}

func (e *transportEnv) putProfile() {
	e.t.Helper()
	putProfile(e.t, e.h, e.partner, profileBody)
}

func (e *transportEnv) as2Config(mutate func(*domain.AS2Settings)) string {
	cfg := domain.AS2Settings{PartnerAS2ID: "DISTRIBUTOR", OurAS2ID: "PASSAGE", URL: e.peer.srv.URL, PartnerCertificate: e.them.CertPEM(), SigningKeyRef: "as2-passage"}
	if mutate != nil {
		mutate(&cfg)
	}
	b, _ := json.Marshal(domain.PartnerTransport{Kind: domain.TransportAS2, AS2: &cfg})
	return string(b)
}

func (e *transportEnv) putTransport(body string, scope map[string]string) *httptest.ResponseRecorder {
	e.t.Helper()
	return do(e.t, e.h, http.MethodPut, "/edi/v1/partners/"+e.partner+"/transport", body, "edi.admin", scope)
}

func (e *transportEnv) mustPutTransport(body string) transportView {
	e.t.Helper()
	rec := e.putTransport(body, srcScope)
	var v transportView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil || rec.Code != http.StatusOK {
		e.t.Fatalf("put transport: %d %s", rec.Code, rec.Body.String())
	}
	return v
}

func (e *transportEnv) outbound(key string) store.OutboundResult {
	e.t.Helper()
	body := `{"transactionSet":"856","document":` + asnDocument + `}`
	rec := do(e.t, e.h, http.MethodPost, "/edi/v1/partners/"+e.partner+"/outbound", body, "edi.operate", map[string]string{"Idempotency-Key": key, tenant.HeaderLegalEntity: srcLE})
	var out store.OutboundResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusCreated {
		e.t.Fatalf("outbound: %d %s", rec.Code, rec.Body.String())
	}
	return out
}

func (e *transportEnv) detail(id string) store.InterchangeDetail {
	e.t.Helper()
	rec := do(e.t, e.h, http.MethodGet, "/edi/v1/interchanges/"+id, "", "edi.operate", srcScope)
	var d store.InterchangeDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil || rec.Code != http.StatusOK {
		e.t.Fatalf("detail: %d %s", rec.Code, rec.Body.String())
	}
	return d
}

func (e *transportEnv) dispatch(want int) {
	e.t.Helper()
	n, err := e.disp.RunOnce(context.Background())
	if err != nil || n != want {
		e.t.Fatalf("dispatch: %d attempts, want %d: %v", n, want, err)
	}
}

func (e *transportEnv) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.admin.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// x12Control rewrites sample850's ISA13/IEA02.
func x12Control(t *testing.T, control string) string {
	t.Helper()
	return strings.ReplaceAll(sample850(t, "1"), "000000001", control)
}

func TestTransportAPI_ConfigNeedsProfileStaysInEntityAndHoldsNoSecret(t *testing.T) {
	e := newTransportEnv(t)
	if rec := e.putTransport(e.as2Config(nil), srcScope); rec.Code != http.StatusConflict || problemCode(t, rec) != CodeNoTradeProfile {
		t.Fatalf("without a trade profile: %d %s", rec.Code, rec.Body.String())
	}
	e.putProfile()

	for name, c := range map[string]struct {
		body  string
		field string
	}{
		"private key as the ref":     {e.as2Config(func(a *domain.AS2Settings) { a.SigningKeyRef = "-----BEGIN PRIVATE KEY-----" }), "as2.signingKeyRef"},
		"key pasted as certificate":  {e.as2Config(func(a *domain.AS2Settings) { a.PartnerCertificate = string(e.them.PEM(t)) }), "as2.partnerCertificate"},
		"missing secret":             {e.as2Config(func(a *domain.AS2Settings) { a.SigningKeyRef = "nope" }), "as2.signingKeyRef"},
		"3DES":                       {e.as2Config(func(a *domain.AS2Settings) { a.EncryptionAlgorithm = "des-ede3-cbc" }), "as2.encryptionAlgorithm"},
		"URL with credentials":       {e.as2Config(func(a *domain.AS2Settings) { a.URL = "https://u:p@partner.example/as2" }), "as2.url"},
		"AS2 id with a line break":   {e.as2Config(func(a *domain.AS2Settings) { a.PartnerAS2ID = "A\r\nB" }), "as2.partnerAs2Id"},
		"SFTP settings on AS2":       {`{"kind":"AS2","sftp":{}}`, "sftp"},
		"unknown kind":               {`{"kind":"FTP"}`, "kind"},
		"SFTP password in the ref":   {`{"kind":"SFTP","sftp":{"host":"sftp.example","user":"edi","hostKeyFingerprint":"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","authMethod":"password","credentialRef":"hunter2!","outboundDir":"/out"}}`, "sftp.credentialRef"},
		"SFTP dir escaping the root": {`{"kind":"SFTP","sftp":{"host":"sftp.example","user":"edi","hostKeyFingerprint":"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","authMethod":"key","credentialRef":"k","outboundDir":"/out/../etc"}}`, "sftp.outboundDir"},
	} {
		rec := e.putTransport(c.body, srcScope)
		var p struct {
			Errors map[string]string `json:"errors"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		if rec.Code != http.StatusBadRequest || p.Errors[c.field] == "" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}

	v := e.mustPutTransport(e.as2Config(nil))
	if v.Kind != "AS2" || v.LegalEntityID != srcLE || v.AS2.SigningAlgorithm != "sha-256" || v.AS2.EncryptionAlgorithm != "none" || v.AS2.MDNMode != "sync" ||
		v.AS2Inbound == nil || v.AS2Inbound.URL != e.as2.URL+"/as2/v1/"+v.AS2Inbound.EndpointID || v.AS2Inbound.PartnerCertificateSHA256 != domain.CertificateFingerprint(e.them.Cert) ||
		v.Secrets["as2.signingKeyRef"].Status != "ok" || v.Version != 1 {
		t.Fatalf("view %+v", v)
	}
	again := e.mustPutTransport(e.as2Config(func(a *domain.AS2Settings) { a.SigningAlgorithm = "sha-512" }))
	if again.Version != 2 || again.AS2Inbound.EndpointID != v.AS2Inbound.EndpointID {
		t.Fatalf("an update keeps the endpoint: %+v", again)
	}
	rec := do(t, e.h, http.MethodGet, "/edi/v1/partners/"+e.partner+"/transport", "", "edi.admin", srcScope)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"signingAlgorithm":"sha-512"`) || strings.Contains(rec.Body.String(), "PRIVATE KEY") {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}

	// Another legal entity of the tenant neither sees nor repoints it.
	other := map[string]string{tenant.HeaderLegalEntity: "00000000-0000-0000-0000-0000000000e2"}
	if rec := do(t, e.h, http.MethodGet, "/edi/v1/partners/"+e.partner+"/transport", "", "edi.admin", other); rec.Code != http.StatusNotFound {
		t.Fatalf("other entity get: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.putTransport(`{"kind":"API"}`, other); rec.Code != http.StatusConflict {
		t.Fatalf("other entity put: %d %s", rec.Code, rec.Body.String())
	}
	// Nothing secret reached the database.
	var row string
	if err := e.admin.QueryRow(context.Background(), `select t::text from partner_transport t`).Scan(&row); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(row, "PRIVATE KEY") || !strings.Contains(row, "as2-passage") {
		t.Fatalf("stored row %s", row)
	}
	// The work index is not readable by the application role.
	err := dbx.WithTenantTx(testkit.Ctx(testkit.TenantID(7)), e.app, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `select * from transport_work`)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("transport_work readable by passage_app: %v", err)
	}
}

func TestTransportAS2_OutboundSyncMDNMarksSentAndPublishes(t *testing.T) {
	e := newTransportEnv(t)
	e.putProfile()
	e.mustPutTransport(e.as2Config(func(a *domain.AS2Settings) { a.SigningAlgorithm = "sha-384" }))
	out := e.outbound("asn-1")
	if out.Delivery == nil || out.Delivery.Status != "PENDING" || out.Delivery.Transport != "AS2" {
		t.Fatalf("delivery %+v", out.Delivery)
	}
	e.dispatch(1)
	got := e.peer.messages()
	if len(got) != 1 || string(got[0].Payload) != out.X12 || got[0].ID != out.Delivery.MessageID || got[0].ContentType != transport.ContentX12 {
		t.Fatalf("partner received %+v", got)
	}
	d := e.detail(out.InterchangeID)
	if len(d.Deliveries) != 1 || d.Deliveries[0].Status != "DELIVERED" || d.Deliveries[0].MDNSigned == nil || !*d.Deliveries[0].MDNSigned ||
		!strings.Contains(d.Deliveries[0].MDNDisposition, "processed") || d.Messages[0].Status != "SENT" {
		t.Fatalf("detail %+v", d)
	}
	if n := e.count(`select count(*) from outbox where type = $1 and aggregate_id = $2`, ediev.MessageSent, out.MessageID); n != 1 {
		t.Fatalf("%d message.sent events", n)
	}
	e.dispatch(0)
}

func TestTransportAS2_AsyncMDNOnlyFromThePartner(t *testing.T) {
	e := newTransportEnv(t)
	e.peer.as2.SendAsyncMDN = func(_ context.Context, m transport.AsyncMDN) error {
		e.peer.mu.Lock()
		defer e.peer.mu.Unlock()
		e.peer.asyncMDNs = append(e.peer.asyncMDNs, m)
		return nil
	}
	e.putProfile()
	v := e.mustPutTransport(e.as2Config(func(a *domain.AS2Settings) { a.MDNMode = "async" }))
	out := e.outbound("asn-async")
	e.dispatch(1)
	if d := e.detail(out.InterchangeID); d.Deliveries[0].Status != "AWAITING_MDN" || d.Messages[0].Status != "MAPPED" {
		t.Fatalf("detail %+v", d)
	}
	if len(e.peer.asyncMDNs) != 1 || e.peer.asyncMDNs[0].URL != v.AS2Inbound.MDNURL {
		t.Fatalf("async MDN %+v", e.peer.asyncMDNs)
	}
	mdn := e.peer.asyncMDNs[0]
	post := func(body []byte, hdr http.Header) int {
		req, _ := http.NewRequest(http.MethodPost, v.AS2Inbound.MDNURL, bytes.NewReader(body))
		for k, vs := range hdr {
			req.Header[k] = vs
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	tampered := bytes.Replace(mdn.Body, []byte("processed"), []byte("processeD"), 1)
	if code := post(tampered, mdn.Header); code != http.StatusBadRequest {
		t.Fatalf("tampered MDN: %d", code)
	}
	spoofed := mdn.Header.Clone()
	spoofed.Set("AS2-From", "SOMEONE")
	if code := post(mdn.Body, spoofed); code != http.StatusForbidden {
		t.Fatalf("other AS2-From: %d", code)
	}
	if d := e.detail(out.InterchangeID); d.Deliveries[0].Status != "AWAITING_MDN" {
		t.Fatalf("a refused MDN changed the delivery: %+v", d.Deliveries[0])
	}
	if code := post(mdn.Body, mdn.Header); code != http.StatusOK {
		t.Fatalf("partner MDN: %d", code)
	}
	if d := e.detail(out.InterchangeID); d.Deliveries[0].Status != "DELIVERED" || d.Messages[0].Status != "SENT" {
		t.Fatalf("detail %+v", d)
	}
	if code := post(mdn.Body, mdn.Header); code != http.StatusNotFound {
		t.Fatalf("second MDN: %d", code)
	}
}

func postAsyncMDN(t *testing.T, url string, m transport.AsyncMDN) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(m.Body))
	for k, vs := range m.Header {
		req.Header[k] = vs
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestTransportAS2_AwaitingMDNTimesOutIsSentAgainThenFails(t *testing.T) {
	e := newTransportEnv(t)
	e.peer.as2.SendAsyncMDN = func(_ context.Context, m transport.AsyncMDN) error {
		e.peer.mu.Lock()
		defer e.peer.mu.Unlock()
		e.peer.asyncMDNs = append(e.peer.asyncMDNs, m)
		return nil
	}
	e.disp.MDNTimeout, e.disp.MaxAttempts = time.Hour, 2
	e.putProfile()
	v := e.mustPutTransport(e.as2Config(func(a *domain.AS2Settings) { a.MDNMode = "async" }))
	out := e.outbound("asn-timeout")
	id := out.Delivery.ID
	exec := func(q string) {
		t.Helper()
		if _, err := e.admin.Exec(context.Background(), q, id); err != nil {
			t.Fatal(err)
		}
	}
	e.dispatch(1)
	d := e.detail(out.InterchangeID).Deliveries[0]
	if d.Status != "AWAITING_MDN" || d.MDNDueAt == nil || time.Until(*d.MDNDueAt) < 59*time.Minute || time.Until(*d.MDNDueAt) > 61*time.Minute {
		t.Fatalf("waiting %+v", d)
	}
	// The wait runs out: sent again later with the same Message-ID.
	exec(`update transport_delivery set mdn_due_at = now() - interval '1 second' where id = $1`)
	e.dispatch(0)
	d = e.detail(out.InterchangeID).Deliveries[0]
	if d.Status != "PENDING" || d.MDNTimeouts != 1 || d.MDNDueAt != nil || d.NextAttemptAt == nil || time.Until(*d.NextAttemptAt) < 30*time.Second ||
		!strings.Contains(d.LastError, "no asynchronous MDN within 1h0m0s") {
		t.Fatalf("after the first timeout %+v", d)
	}
	exec(`update transport_delivery set next_attempt_at = now() where id = $1`)
	e.dispatch(1)
	if got := e.peer.messages(); len(got) != 2 || got[0].ID != out.Delivery.MessageID || got[1].ID != out.Delivery.MessageID {
		t.Fatalf("partner received %+v", got)
	}
	if d = e.detail(out.InterchangeID).Deliveries[0]; d.Status != "AWAITING_MDN" || d.Attempts != 2 {
		t.Fatalf("sent again %+v", d)
	}
	// The second wait runs out with the attempts used up: FAILED, with an event.
	exec(`update transport_delivery set mdn_due_at = now() - interval '1 second' where id = $1`)
	e.dispatch(0)
	d = e.detail(out.InterchangeID).Deliveries[0]
	if d.Status != "FAILED" || d.MDNTimeouts != 2 || !strings.Contains(d.LastError, "after 2 attempts") {
		t.Fatalf("after the second timeout %+v", d)
	}
	if n := e.count(`select count(*) from outbox where type = $1 and aggregate_id = $2 and (payload->'data'->>'mdn_timeout')::boolean
		and (payload->'data'->>'mdn_timeouts')::int = 2`, ediev.DeliveryFailed, id); n != 1 {
		t.Fatalf("%d delivery.failed events", n)
	}
	if n := e.count(`select count(*) from transport_work where ref = $1`, id); n != 0 {
		t.Fatalf("%d work rows left for a FAILED delivery", n)
	}
	// An MDN arriving now finds nothing waiting; the operator resends.
	if code := postAsyncMDN(t, v.AS2Inbound.MDNURL, e.peer.asyncMDNs[1]); code != http.StatusNotFound {
		t.Fatalf("late MDN: %d", code)
	}
	if rec := do(t, e.h, http.MethodPost, "/edi/v1/interchanges/"+out.InterchangeID+":resend", "", "edi.operate", srcScope); rec.Code != http.StatusAccepted {
		t.Fatalf("resend: %d %s", rec.Code, rec.Body.String())
	}
}

// mdnSink is the partner's AS2 host: its AS2 server under /as2, its
// receiver of asynchronous MDNs under /mdn, and /redirect bouncing to /mdn.
type mdnSink struct {
	srv  *httptest.Server
	mu   sync.Mutex
	mdns []transport.AsyncMDN
}

func (s *mdnSink) received() []transport.AsyncMDN {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]transport.AsyncMDN(nil), s.mdns...)
}

func (e *transportEnv) startMDNSink() *mdnSink {
	s := &mdnSink{}
	mux := http.NewServeMux()
	mux.Handle("/as2", e.peer.as2)
	mux.HandleFunc("/mdn", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.mdns = append(s.mdns, transport.AsyncMDN{Header: r.Header.Clone(), Body: body})
	})
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/mdn", http.StatusFound) })
	s.srv = httptest.NewServer(mux)
	e.t.Cleanup(s.srv.Close)
	return s
}

func TestTransportAS2_AsyncMDNWeOweIsStoredWithTheReceiptAndSentByTheDispatcher(t *testing.T) {
	e := newTransportEnv(t)
	sink := e.startMDNSink()
	e.putProfile()
	v := e.mustPutTransport(e.as2Config(func(a *domain.AS2Settings) { a.URL = sink.srv.URL + "/as2" }))
	c := e.partnerClient(v.AS2Inbound.URL, e.them)
	send := func(id, payload, mdnURL string) transport.Receipt {
		t.Helper()
		c.AsyncMDNURL = mdnURL
		r, err := c.Send(context.Background(), transport.Message{ID: id, ContentType: transport.ContentX12, Payload: []byte(payload)})
		if err != nil || !r.Pending {
			t.Fatalf("%s: %+v %v", id, r, err)
		}
		return r
	}
	owed := func(id, status string) int {
		t.Helper()
		return e.count(`select count(*) from transport_mdn where original_message_id = $1 and status = $2 and legal_entity_id = $3`, id, status, srcLE)
	}
	check := func(m transport.AsyncMDN, id, mic string) transport.MDN {
		t.Helper()
		mdn, err := transport.ReadMDN(m.Header.Get("Content-Type"), m.Body, e.us.Cert)
		if err != nil || !mdn.Signed || mdn.OriginalMessageID != "<"+id+">" || (mic != "" && mdn.MIC != mic) {
			t.Fatalf("MDN for %s: %+v %v", id, mdn, err)
		}
		return mdn
	}

	// Recorded: the MDN is stored with the interchange and posted by the
	// dispatcher, not from the request.
	r := send("po-a1@distributor", x12Control(t, "000000201"), sink.srv.URL+"/mdn")
	if n := e.count(`select count(*) from transport_mdn m join interchange i on i.id = m.interchange_id
		where m.original_message_id = 'po-a1@distributor' and m.status = 'PENDING' and i.control_number = '000000201'`); n != 1 {
		t.Fatalf("%d MDNs stored with the receipt", n)
	}
	if len(sink.received()) != 0 {
		t.Fatal("MDN posted before the dispatcher ran")
	}
	e.dispatch(2) // the 997 and the MDN
	if got := sink.received(); len(got) != 1 || !check(got[0], "po-a1@distributor", r.SentMIC).Processed() {
		t.Fatalf("sink got %+v", got)
	}
	if owed("po-a1@distributor", "DELIVERED") != 1 || len(e.peer.messages()) != 1 {
		t.Fatal("MDN not DELIVERED or the 997 not sent")
	}

	// Refused (not X12): nothing recorded, the error MDN is still owed.
	send("po-a2@distributor", "UNB+UNOC:3+A+B'", sink.srv.URL+"/mdn")
	if n := e.count(`select count(*) from transport_mdn where original_message_id = 'po-a2@distributor' and interchange_id is null`); n != 1 {
		t.Fatalf("%d error MDNs", n)
	}
	e.dispatch(1)
	if got := sink.received(); len(got) != 2 || check(got[1], "po-a2@distributor", "").Processed() ||
		!strings.Contains(check(got[1], "po-a2@distributor", "").Disposition, "processed/error") {
		t.Fatalf("sink got %+v", got)
	}

	// A Receipt-Delivery-Option off the partner's AS2 host: processed, no MDN stored.
	send("po-a3@distributor", x12Control(t, "000000203"), "https://evil.example/mdn")
	if n := e.count(`select count(*) from transport_mdn where original_message_id = 'po-a3@distributor'`); n != 0 {
		t.Fatalf("%d MDNs for a foreign URL", n)
	}
	if n := e.count(`select count(*) from interchange where direction = 'IN'`); n != 2 {
		t.Fatalf("%d inbound interchanges", n)
	}
	e.dispatch(1) // its 997

	// Redirects are not followed: the attempt fails and backs off.
	send("po-a4@distributor", x12Control(t, "000000204"), sink.srv.URL+"/redirect")
	e.dispatch(2)
	var attempts int
	var lastErr string
	if err := e.admin.QueryRow(context.Background(), `select attempts, coalesce(last_error, '') from transport_mdn
		where original_message_id = 'po-a4@distributor' and status = 'PENDING' and next_attempt_at > now() + interval '30 seconds'`).Scan(&attempts, &lastErr); err != nil ||
		attempts != 1 || !strings.Contains(lastErr, "HTTP 302") {
		t.Fatalf("redirected MDN: %d %q %v", attempts, lastErr, err)
	}
	if len(sink.received()) != 2 {
		t.Fatal("the redirect was followed")
	}

	// The partner's AS2 URL moves to another host: an MDN still owed for
	// the old host is FAILED, not posted.
	send("po-a5@distributor", x12Control(t, "000000205"), sink.srv.URL+"/mdn")
	e.mustPutTransport(e.as2Config(func(a *domain.AS2Settings) { a.URL = "http://127.0.0.2:1/as2" }))
	e.dispatch(2) // the 997 (unreachable, retried) and the MDN
	if owed("po-a5@distributor", "FAILED") != 1 || len(sink.received()) != 2 {
		t.Fatalf("MDN for a moved partner: %d sent", len(sink.received()))
	}
}

func TestTransportAS2_RejectionRetryAndResend(t *testing.T) {
	e := newTransportEnv(t)
	e.putProfile()
	e.mustPutTransport(e.as2Config(nil))
	e.peer.fail = errors.New("partner store down")
	out := e.outbound("asn-rej")
	e.dispatch(1)
	d := e.detail(out.InterchangeID)
	if d.Deliveries[0].Status != "REJECTED" || !strings.Contains(d.Deliveries[0].MDNDisposition, "processed/error") || d.Messages[0].Status != "MAPPED" {
		t.Fatalf("detail %+v", d.Deliveries)
	}
	if rec := do(t, e.h, http.MethodPost, "/edi/v1/interchanges/"+out.InterchangeID+":resend", "", "edi.operate", map[string]string{tenant.HeaderLegalEntity: "00000000-0000-0000-0000-0000000000e2"}); rec.Code != http.StatusNotFound && rec.Code != http.StatusConflict {
		t.Fatalf("another entity resends: %d %s", rec.Code, rec.Body.String())
	}
	rec := do(t, e.h, http.MethodPost, "/edi/v1/interchanges/"+out.InterchangeID+":resend", "", "edi.operate", srcScope)
	var again store.DeliveryView
	if err := json.Unmarshal(rec.Body.Bytes(), &again); err != nil || rec.Code != http.StatusAccepted || again.MessageID == out.Delivery.MessageID {
		t.Fatalf("resend: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, e.h, http.MethodPost, "/edi/v1/interchanges/"+out.InterchangeID+":resend", "", "edi.operate", srcScope); rec.Code != http.StatusConflict {
		t.Fatalf("resend while one is pending: %d %s", rec.Code, rec.Body.String())
	}
	// The partner is unreachable: the attempt is retried later, not given up.
	e.peer.srv.Close()
	e.dispatch(1)
	d = e.detail(out.InterchangeID)
	if last := d.Deliveries[1]; last.Status != "PENDING" || last.Attempts != 1 || last.NextAttemptAt == nil || time.Until(*last.NextAttemptAt) < 30*time.Second || last.LastError == "" {
		t.Fatalf("retry %+v", last)
	}
	e.dispatch(0)
}

// as2Partner returns the partner's AS2 client to our endpoint.
func (e *transportEnv) partnerClient(endpointURL string, key transporttest.KeyPair) *transport.AS2Client {
	return &transport.AS2Client{Us: transport.AS2Identity{ID: "DISTRIBUTOR", Cert: key.Cert, Key: key.Key},
		Partner:          transport.AS2Partner{ID: "PASSAGE", URL: endpointURL, Cert: e.us.Cert},
		RequireSignedMDN: true}
}

func TestTransportAS2_InboundVerifiedRecordedAndAcknowledged(t *testing.T) {
	e := newTransportEnv(t)
	e.putProfile()
	v := e.mustPutTransport(e.as2Config(nil))
	c := e.partnerClient(v.AS2Inbound.URL, e.them)
	send := func(id, payload string) (transport.Receipt, error) {
		return c.Send(context.Background(), transport.Message{ID: id, ContentType: transport.ContentX12, Payload: []byte(payload)})
	}
	r, err := send("po-1@distributor", x12Control(t, "000000101"))
	if err != nil || !r.SignedMDN || !strings.HasSuffix(r.Disposition, "; processed") {
		t.Fatalf("%+v %v", r, err)
	}
	inbound := `select count(*) from interchange where direction = 'IN'`
	if n := e.count(inbound); n != 1 {
		t.Fatalf("%d inbound interchanges", n)
	}
	if n := e.count(`select count(*) from transport_inbound where transport = 'AS2' and ref = 'po-1@distributor' and status = 'DONE' and legal_entity_id = $1`, srcLE); n != 1 {
		t.Fatal("transport_inbound not DONE")
	}
	// The 997 goes back over AS2.
	e.dispatch(1)
	got := e.peer.messages()
	if len(got) != 1 || !strings.Contains(string(got[0].Payload), "ST*997*") {
		t.Fatalf("partner got %+v", got)
	}
	// A retransmission of the same message, and a new message repeating the
	// interchange, are duplicates: processed with a warning, nothing new.
	for _, id := range []string{"po-1@distributor", "po-1-again@distributor"} {
		r, err := send(id, x12Control(t, "000000101"))
		if err != nil || !strings.Contains(r.Disposition, "processed/warning: duplicate-document") {
			t.Fatalf("%s: %+v %v", id, r, err)
		}
	}
	// Signed by a key the profile does not pin: refused before parsing.
	forged := e.partnerClient(v.AS2Inbound.URL, transporttest.NewKeyPair(t, "DISTRIBUTOR", false))
	if _, err := forged.Send(context.Background(), transport.Message{ID: "po-2@x", ContentType: transport.ContentX12, Payload: []byte(x12Control(t, "000000102"))}); !errors.Is(err, transport.ErrRejected) || !strings.Contains(err.Error(), "authentication-failed") {
		t.Fatalf("forged: %v", err)
	}
	// A message the partner signed but whose ISA sender is another partner of the tenant.
	if rec := do(t, e.h, http.MethodPost, "/edi/v1/partners", `{"name":"Other","isaQualifier":"ZZ","isaId":"OTHER","environment":"TEST"}`, "edi.admin", map[string]string{"Idempotency-Key": "o"}); rec.Code != http.StatusCreated {
		t.Fatal(rec.Body.String())
	}
	if _, err := send("po-3@distributor", strings.ReplaceAll(x12Control(t, "000000103"), "DISTRIBUTOR    ", "OTHER          ")); !errors.Is(err, transport.ErrRejected) {
		t.Fatalf("other sender: %v", err)
	}
	// Garbage that is not X12.
	if _, err := send("po-4@distributor", "UNB+UNOC:3+A+B'"); !errors.Is(err, transport.ErrRejected) {
		t.Fatalf("not X12: %v", err)
	}
	if n := e.count(inbound); n != 1 {
		t.Fatalf("%d inbound interchanges after refusals", n)
	}
	// Wrong AS2-To, an unknown endpoint, and headers claiming a tenant change nothing.
	c.Partner.ID = "SOMEONE"
	if _, err := send("po-5@distributor", x12Control(t, "000000105")); err == nil || !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("wrong AS2-To: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, e.as2.URL+"/as2/v1/0195f0a0-0000-7000-8000-0000000000aa", strings.NewReader("x"))
	req.Header.Set(tenant.HeaderTenant, testkit.TenantID(7))
	req.Header.Set(tenant.HeaderDuties, "edi.operate")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown endpoint: %d", resp.StatusCode)
	}
	// The main API still requires a tenant: the AS2 routes are not on it.
	if rec := httptest.NewRecorder(); true {
		e.h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/as2/v1/"+v.AS2Inbound.EndpointID, strings.NewReader("x")))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("AS2 path on the main router: %d", rec.Code)
		}
	}

	// With encryption selected the partner must encrypt.
	e.mustPutTransport(e.as2Config(func(a *domain.AS2Settings) { a.EncryptionAlgorithm = "aes256-cbc" }))
	c.Partner.ID = "PASSAGE"
	if _, err := send("po-6@distributor", x12Control(t, "000000106")); !errors.Is(err, transport.ErrRejected) || !strings.Contains(err.Error(), "insufficient-message-security") {
		t.Fatalf("clear message to an encrypting profile: %v", err)
	}
	c.Encryption = transport.EncryptAES256
	if r, err := send("po-7@distributor", x12Control(t, "000000107")); err != nil || !strings.HasSuffix(r.Disposition, "; processed") {
		t.Fatalf("encrypted: %+v %v", r, err)
	}
	if n := e.count(inbound); n != 2 {
		t.Fatalf("%d inbound interchanges", n)
	}
}

func TestTransportSFTP_UploadPollArchiveParkAndBackOff(t *testing.T) {
	e := newTransportEnv(t)
	e.putProfile()
	signer, keyPEM := transporttest.NewSSHKey(t)
	addr, fingerprint := transporttest.StartSFTPServer(t, transporttest.SFTPLogin{User: "edi", Key: signer.PublicKey()})
	host, port, _ := strings.Cut(addr, ":")
	e.secrets["sftp-distributor"] = keyPEM
	root := t.TempDir()
	out, in := filepath.Join(root, "out"), filepath.Join(root, "in")
	for _, d := range []string{out, in} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	body := `{"kind":"SFTP","sftp":{"host":"` + host + `","port":` + port + `,"user":"edi","hostKeyFingerprint":"` + fingerprint + `",
		"authMethod":"key","credentialRef":"sftp-distributor","outboundDir":"` + out + `","inboundDir":"` + in + `","pollIntervalSeconds":60}}`
	e.mustPutTransport(body)

	// Outbound: the ASN is uploaded (written as .part, then renamed).
	asn := e.outbound("asn-sftp")
	e.dispatch(1)
	files, _ := os.ReadDir(out)
	if len(files) != 1 || !strings.HasPrefix(files[0].Name(), "856_"+asn.ControlNumber+"_") {
		t.Fatalf("outbound dir %v", files)
	}
	if b, _ := os.ReadFile(filepath.Join(out, files[0].Name())); string(b) != asn.X12 {
		t.Fatalf("uploaded %q", b)
	}
	if d := e.detail(asn.InterchangeID); d.Deliveries[0].Status != "DELIVERED" || d.Messages[0].Status != "SENT" {
		t.Fatalf("detail %+v", d.Deliveries)
	}

	// Inbound: an 850, a file that is not X12, one still uploading, and one
	// that fails once for a transient reason.
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(in, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a-po.x12", x12Control(t, "000000201"))
	write("b-junk.x12", "not an interchange")
	write("c-half.x12.part", "ISA*")
	write("d-flaky.x12", x12Control(t, "000000202"))
	flaky := 0
	p := &delivery.Poller{Store: e.st, Secrets: e.secrets, MaxAttempts: 2, Receive: func(ctx context.Context, partnerID string, src store.InboundSource, payload []byte) error {
		if src.Ref == "d-flaky.x12" {
			flaky++
			return errors.New("database briefly unavailable")
		}
		return e.a.ReceiveFromTransport(ctx, partnerID, src, payload)
	}}
	poll := func(want int) {
		t.Helper()
		n, err := p.RunOnce(context.Background())
		if err != nil || n != want {
			t.Fatalf("poll handled %d, want %d: %v", n, want, err)
		}
	}
	due := func() {
		t.Helper()
		if _, err := e.admin.Exec(context.Background(), `update partner_transport set sftp_next_poll_at = now() - interval '1 second'`); err != nil {
			t.Fatal(err)
		}
		if _, err := e.admin.Exec(context.Background(), `update transport_inbound set next_attempt_at = now() - interval '1 second' where status = 'FAILED'`); err != nil {
			t.Fatal(err)
		}
	}
	poll(2) // a-po recorded and archived, b-junk parked; d-flaky failed once
	inbox := func() []string {
		entries, _ := os.ReadDir(in)
		var names []string
		for _, en := range entries {
			names = append(names, en.Name())
		}
		return names
	}
	if got := strings.Join(inbox(), ","); got != "c-half.x12.part,d-flaky.x12,failed,processed" {
		t.Fatalf("inbox %s", got)
	}
	if n := e.count(`select count(*) from interchange where direction = 'IN'`); n != 1 {
		t.Fatalf("%d inbound", n)
	}
	if n := e.count(`select count(*) from transport_inbound where status = 'FAILED' and ref = 'd-flaky.x12' and attempts = 1 and next_attempt_at > now()`); n != 1 {
		t.Fatal("flaky file not backed off")
	}
	if n := e.count(`select count(*) from transport_inbound where status = 'PARKED' and ref = 'b-junk.x12'`); n != 1 {
		t.Fatal("junk not parked")
	}
	poll(0) // not due yet
	// The 997 for the 850 is uploaded next to the ASN.
	e.dispatch(1)
	files, _ = os.ReadDir(out)
	if len(files) != 2 || !strings.HasPrefix(files[0].Name(), "856_") || !strings.HasPrefix(files[1].Name(), "997_000000201_") {
		t.Fatalf("outbound dir %v", files)
	}
	// The same file again (the partner re-sent it) is archived without a new
	// interchange; the flaky file fails a second time and is parked.
	write("a-po.x12", x12Control(t, "000000201"))
	due()
	poll(2)
	if n := e.count(`select count(*) from interchange where direction = 'IN'`); n != 1 {
		t.Fatalf("%d inbound after a re-sent file", n)
	}
	if got := strings.Join(inbox(), ","); got != "c-half.x12.part,failed,processed" || flaky != 2 {
		t.Fatalf("inbox %s, flaky %d", got, flaky)
	}
	parked, _ := os.ReadDir(filepath.Join(in, "failed"))
	archived, _ := os.ReadDir(filepath.Join(in, "processed"))
	if len(parked) != 2 || len(archived) < 1 {
		t.Fatalf("parked %v archived %v", parked, archived)
	}
	// A server that cannot be reached backs the next poll off.
	e.secrets["sftp-distributor"] = []byte("not a key")
	due()
	if n, err := p.RunOnce(context.Background()); n != 0 || err != nil {
		t.Fatalf("%d %v", n, err)
	}
	if n := e.count(`select count(*) from partner_transport where sftp_poll_failures = 1 and sftp_next_poll_at > now() + interval '100 seconds'`); n != 1 {
		t.Fatal("failed poll not backed off")
	}
}
