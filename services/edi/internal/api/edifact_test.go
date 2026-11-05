package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/services/edi/internal/delivery"
	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	ediev "github.com/BenjaminSRussell/passage/services/edi/internal/events"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
	"github.com/BenjaminSRussell/passage/services/edi/internal/transport"
	"github.com/BenjaminSRussell/passage/services/edi/internal/transport/transporttest"
)

// ordersEdifact is testdata/edifact/orders-inbound.edi: one ORDERS from
// 4012345000009:14 to PASSAGE:ZZZ, interchange reference 42, CONTRL
// requested (UNB 0031 = 1).
func ordersEdifact(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../testdata/edifact/orders-inbound.edi")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// edifactRef rewrites the interchange reference (UNB 0020 and UNZ02) of the
// ORDERS fixture.
func edifactRef(t *testing.T, raw, ref string) string {
	t.Helper()
	return strings.Replace(strings.Replace(raw, "+261105:0900+42+", "+261105:0900+"+ref+"+", 1), "UNZ+1+42'", "UNZ+1+"+ref+"'", 1)
}

const euSender = "4012345000009:14"

func edifactPartnerBody(name, isaID, edifactID, contrl string) string {
	return `{"name":"` + name + `","isaQualifier":"ZZ","isaId":"` + isaID + `","environment":"TEST","edifactId":"` + edifactID +
		`","edifactQualifier":"14","edifactContrl":"` + contrl + `"}`
}

func createPartner(t *testing.T, h http.Handler, key, body string) store.Partner {
	t.Helper()
	rec := do(t, h, http.MethodPost, "/edi/v1/partners", body, "edi.admin", map[string]string{"Idempotency-Key": key})
	var p store.Partner
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || rec.Code != http.StatusCreated {
		t.Fatalf("partner: %d %s", rec.Code, rec.Body.String())
	}
	return p
}

func postEdifact(t *testing.T, h http.Handler, raw string) (receiveResponse, int, string) {
	t.Helper()
	rec := do(t, h, http.MethodPost, "/edi/v1/inbound/edifact?environment=TEST", raw, "edi.operate", nil)
	var res receiveResponse
	if rec.Code == http.StatusCreated {
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		return res, rec.Code, ""
	}
	return res, rec.Code, problemCode(t, rec)
}

func countRows(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestReceiveEdifact_ORDERSRecordedPublishedAndAnsweredWithCONTRL(t *testing.T) {
	h, pool := dbHandlerPool(t)
	p := createPartner(t, h, "eu", edifactPartnerBody("EU distributor", "EUDIST", "4012345000009", ""))
	if p.EdifactID != "4012345000009" || p.EdifactQualifier != "14" || p.EdifactContrl != domain.ContrlRequested {
		t.Fatalf("partner %+v", p)
	}
	res, code, _ := postEdifact(t, h, ordersEdifact(t))
	if code != http.StatusCreated || res.AckStatus != "ACCEPTED" || len(res.Messages) != 1 || res.Messages[0].TransactionSet != "ORDERS" ||
		res.Messages[0].STControl != "1" || res.Messages[0].Status != "MAPPED" || res.Ack997 != "" {
		t.Fatalf("%d %+v", code, res)
	}
	// The CONTRL goes back from the recipient to the sender, in the sender's
	// service characters, on the partner's first outbound EDIFACT reference.
	if !strings.HasPrefix(res.Contrl, "UNA:+.? 'UNB+UNOC:3+PASSAGE:ZZZ+"+euSender+"+") || !strings.Contains(res.Contrl, "+1'UNH+1+CONTRL:2:2:UN'UCI+42+"+euSender+"+PASSAGE:ZZZ+7'UNT+3+1'UNZ+1+1'") {
		t.Fatalf("CONTRL %s", res.Contrl)
	}
	rec := do(t, h, http.MethodGet, "/edi/v1/interchanges/"+res.InterchangeID, "", "edi.operate,edi.pii.view", nil)
	var d store.InterchangeDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("detail %d %s", rec.Code, rec.Body.String())
	}
	if d.Standard != "EDIFACT" || d.Version != "UNOC:3" || d.ControlNumber != "42" || d.Contrl != res.Contrl || d.AckPayload != "" ||
		!strings.Contains(string(d.Messages[0].Canonical), `"poNumber":"PO-EU-881"`) {
		t.Fatalf("detail %+v %s", d, d.Messages[0].Canonical)
	}
	if n := countRows(t, pool, `select count(*) from outbox where type = $1 and payload->'data'->>'standard' = 'EDIFACT'
		and payload->'data'->>'transaction_set' = 'ORDERS' and payload->'data'->>'control_number' = '42'`, ediev.MessageReceived); n != 1 {
		t.Fatalf("%d message.received events", n)
	}
	// Duplicate reference inside the window.
	if _, code, pc := postEdifact(t, h, ordersEdifact(t)); code != http.StatusConflict || pc != CodeDuplicateControl {
		t.Fatalf("duplicate: %d %s", code, pc)
	}
	// Unknown sender, and the right id with another qualifier.
	if _, code, pc := postEdifact(t, h, edifactRef(t, strings.Replace(ordersEdifact(t), euSender, "4099999999999:14", 1), "43")); code != http.StatusUnprocessableEntity || pc != CodeUnknownSender {
		t.Fatalf("unknown sender: %d %s", code, pc)
	}
	if _, code, pc := postEdifact(t, h, edifactRef(t, strings.Replace(ordersEdifact(t), euSender, "4012345000009:ZZZ", 1), "44")); code != http.StatusUnprocessableEntity || pc != CodeUnknownSender {
		t.Fatalf("other qualifier: %d %s", code, pc)
	}
	// Each route takes its own syntax only.
	if _, code, pc := postEdifact(t, h, sample850(t, "1")); code != http.StatusBadRequest || pc != CodeSyntax {
		t.Fatalf("X12 on the EDIFACT route: %d %s", code, pc)
	}
	if rec := do(t, h, http.MethodPost, "/edi/v1/inbound/x12?environment=TEST", edifactRef(t, ordersEdifact(t), "45"), "edi.operate", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("EDIFACT on the X12 route: %d", rec.Code)
	}
	// An envelope error is refused as a whole and nothing is written.
	if _, code, pc := postEdifact(t, h, strings.Replace(edifactRef(t, ordersEdifact(t), "46"), "UNZ+1+46'", "UNZ+2+46'", 1)); code != http.StatusBadRequest || pc != CodeSyntax {
		t.Fatalf("UNZ count: %d %s", code, pc)
	}
	if n := countRows(t, pool, `select count(*) from interchange where standard = 'EDIFACT'`); n != 1 {
		t.Fatalf("%d EDIFACT interchanges", n)
	}
	// The raw viewer reads X12 only.
	if rec := do(t, h, http.MethodGet, "/edi/v1/interchanges/"+res.InterchangeID+"/payload", "", "edi.operate", nil); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("payload: %d", rec.Code)
	}
}

// fourMessages is an interchange (reference 77) of four messages: a clean
// ORDERS, one whose CNT 2 is wrong, an ORDRSP (not supported), and a clean
// ORDERS whose UNT01 is wrong.
func fourMessages(t *testing.T) string {
	t.Helper()
	ic, err := domain.ParseEdifact(edifactRef(t, ordersEdifact(t), "77"))
	if err != nil {
		t.Fatal(err)
	}
	good := ic.Messages[0]
	badCNT := good
	badCNT.Ref, badCNT.Segments = "2", append([]domain.EdifactSegment(nil), good.Segments...)
	for i, s := range badCNT.Segments {
		if s.Tag == "CNT" {
			badCNT.Segments[i] = domain.Seg("CNT", "2:3")
		}
	}
	ordrsp := domain.EdifactMessage{Ref: "3", Type: "ORDRSP", Version: "D", Release: "96A", Agency: "UN", Segments: []domain.EdifactSegment{domain.Seg("BGM", "231", "R1")}}
	last := good
	last.Ref = "4"
	ic.Messages = []domain.EdifactMessage{good, badCNT, ordrsp, last}
	raw, err := domain.SerializeEdifact(ic, true)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Replace(raw, "UNT+21+4'", "UNT+22+4'", 1)
}

func TestReceiveEdifact_NegativeCONTRLRejectsEachMessageWithItsPlace(t *testing.T) {
	h, pool := dbHandlerPool(t)
	createPartner(t, h, "eu", edifactPartnerBody("EU distributor", "EUDIST", "4012345000009", "REQUESTED"))
	res, code, pc := postEdifact(t, h, fourMessages(t))
	if code != http.StatusCreated {
		t.Fatalf("%d %s", code, pc)
	}
	var statuses []string
	for _, m := range res.Messages {
		statuses = append(statuses, m.TransactionSet+"/"+m.STControl+"="+m.Status)
	}
	if res.AckStatus != "ACCEPTED_WITH_ERRORS" || strings.Join(statuses, ",") != "ORDERS/1=MAPPED,ORDERS/2=FAILED,ORDRSP/3=FAILED,ORDERS/4=FAILED" {
		t.Fatalf("%s %v", res.AckStatus, statuses)
	}
	want := "UCI+77+" + euSender + "+PASSAGE:ZZZ+7'" +
		"UCM+2+ORDERS:D:96A:UN:EAN008+4+29'UCS+20+29'UCD+29+1:2'" + // CNT 2 differs from the LIN count
		"UCM+3+ORDRSP:D:96A:UN+4+12'UCS+1+12'UCD+12+2:1'" + // message type not supported
		"UCM+4+ORDERS:D:96A:UN:EAN008+4+29'UCS+21+29'UCD+29+1:1'" // UNT01
	if !strings.Contains(res.Contrl, want) {
		t.Fatalf("CONTRL %s\nwant %s", res.Contrl, want)
	}
	if n := countRows(t, pool, `select count(*) from outbox where type = $1`, ediev.MessageFailed); n != 3 {
		t.Fatalf("%d message.failed events", n)
	}
	// A dry-run reprocess reads the stored EDIFACT and changes nothing.
	rec := do(t, h, http.MethodPost, "/edi/v1/interchanges/"+res.InterchangeID+":reprocess?dryRun=true", "", "edi.operate", nil)
	var rr store.ReprocessResult
	if err := json.Unmarshal(rec.Body.Bytes(), &rr); err != nil || rec.Code != http.StatusOK || len(rr.Messages) != 3 || rr.Changed != 0 ||
		!strings.Contains(rr.Messages[2].Error, "UNT01") {
		t.Fatalf("reprocess %d %s", rec.Code, rec.Body.String())
	}
}

func TestReceiveEdifact_CONTRLModesGroupsAndReceivedCONTRL(t *testing.T) {
	h, _ := dbHandlerPool(t)
	createPartner(t, h, "r", edifactPartnerBody("Requested", "REQ", "4012345000009", "REQUESTED"))
	createPartner(t, h, "a", edifactPartnerBody("Always", "ALW", "4012345000016", "ALWAYS"))
	createPartner(t, h, "n", edifactPartnerBody("Never", "NEV", "4012345000023", "NEVER"))
	if rec := do(t, h, http.MethodPost, "/edi/v1/partners", edifactPartnerBody("Twin", "TWIN", "4012345000009", ""), "edi.admin", map[string]string{"Idempotency-Key": "twin"}); rec.Code != http.StatusConflict {
		t.Fatalf("a second partner with the same UNB identity: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPost, "/edi/v1/partners", edifactPartnerBody("Bad", "BAD", "has space", "SOMETIMES"), "edi.admin", map[string]string{"Idempotency-Key": "bad"}); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "edifactId") || !strings.Contains(rec.Body.String(), "edifactContrl") {
		t.Fatalf("invalid identity: %d %s", rec.Code, rec.Body.String())
	}
	unrequested := func(sender, ref string) string {
		return strings.Replace(edifactRef(t, strings.Replace(ordersEdifact(t), euSender, sender, 1), ref), "+"+ref+"++++1'", "+"+ref+"'", 1)
	}
	for name, c := range map[string]struct {
		raw  string
		want bool
	}{
		"requested and asked":     {edifactRef(t, ordersEdifact(t), "10"), true},
		"requested and not asked": {unrequested(euSender, "11"), false},
		"always":                  {unrequested("4012345000016:14", "12"), true},
		"never":                   {edifactRef(t, strings.Replace(ordersEdifact(t), euSender, "4012345000023:14", 1), "13"), false},
	} {
		res, code, pc := postEdifact(t, h, c.raw)
		if code != http.StatusCreated || (res.Contrl != "") != c.want {
			t.Errorf("%s: %d %s contrl %q", name, code, pc, res.Contrl)
		}
	}
	// A functional group (UNG..UNE) is read and acknowledged.
	ic, err := domain.ParseEdifact(edifactRef(t, ordersEdifact(t), "14"))
	if err != nil {
		t.Fatal(err)
	}
	ic.Groups = []domain.EdifactGroup{{ID: "ORDERS", Sender: ic.Sender, Recipient: ic.Recipient, Date: "261105", Time: "0900", Ref: "G7", Agency: "UN", Version: "D", Release: "96A"}}
	ic.Messages[0].Group = "G7"
	grouped, err := domain.SerializeEdifact(ic, false)
	if err != nil {
		t.Fatal(err)
	}
	res, code, pc := postEdifact(t, h, grouped)
	if code != http.StatusCreated || res.Messages[0].Status != "MAPPED" || !strings.Contains(res.Contrl, "UCI+14+"+euSender+"+PASSAGE:ZZZ+7'UNT+3+1'") {
		t.Fatalf("grouped: %d %s %+v", code, pc, res)
	}
	// A CONTRL from the partner is recorded, never answered, and matches
	// nothing (no outbound EDIFACT exists).
	contrl, err := os.ReadFile("../../testdata/edifact/contrl-accepted.edi")
	if err != nil {
		t.Fatal(err)
	}
	fromPartner := strings.Replace(string(contrl), "UNB+UNOC:3+PASSAGE:ZZZ+"+euSender+"+", "UNB+UNOC:3+"+euSender+"+PASSAGE:ZZZ+", 1)
	res, code, pc = postEdifact(t, h, fromPartner)
	if code != http.StatusCreated || res.Contrl != "" || res.AckStatus != "ACCEPTED_WITH_ERRORS" || res.Messages[0].TransactionSet != "CONTRL" ||
		res.Messages[0].Status != "FAILED" || !strings.HasPrefix(res.Messages[0].Error, domain.CodeUnmatchedAck) {
		t.Fatalf("received CONTRL: %d %s %+v", code, pc, res)
	}
}

func (e *transportEnv) setEdifact(contrl string) {
	e.t.Helper()
	if _, err := e.admin.Exec(context.Background(), `update trading_partner set edifact_id = '4012345000009', edifact_qualifier = '14', edifact_contrl = $2
		where id = $1`, e.partner, contrl); err != nil {
		e.t.Fatal(err)
	}
}

func TestTransportAS2_EdifactInboundCONTRLAndRefusalAcks(t *testing.T) {
	e := newTransportEnv(t)
	e.putProfile()
	e.setEdifact(domain.ContrlRequested)
	v := e.mustPutTransport(e.as2Config(nil))
	c := e.partnerClient(v.AS2Inbound.URL, e.them)
	send := func(id, contentType, payload string) (transport.Receipt, error) {
		return c.Send(context.Background(), transport.Message{ID: id, ContentType: contentType, Payload: []byte(payload)})
	}
	processed := func(id, contentType, payload string) {
		t.Helper()
		if r, err := send(id, contentType, payload); err != nil || !strings.HasSuffix(r.Disposition, "; processed") {
			t.Fatalf("%s: %+v %v", id, r, err)
		}
	}
	refused := func(id, contentType, payload string) {
		t.Helper()
		if _, err := send(id, contentType, payload); !errors.Is(err, transport.ErrRejected) || !strings.Contains(err.Error(), "unexpected-processing-error") {
			t.Fatalf("%s: %v", id, err)
		}
	}
	lastAck := func(want string) {
		t.Helper()
		got := e.peer.messages()
		last := got[len(got)-1]
		if last.ContentType != want[:strings.Index(want, "|")] || !strings.Contains(string(last.Payload), want[strings.Index(want, "|")+1:]) {
			t.Fatalf("partner got %s %s, want %s", last.ContentType, last.Payload, want)
		}
	}
	recorded := func(where string) int {
		t.Helper()
		return e.count(`select count(*) from interchange where direction = 'IN' and ` + where)
	}

	// An ORDERS over AS2 is recorded and its CONTRL goes back over AS2.
	processed("eu-1@distributor", transport.ContentEdifact, ordersEdifact(t))
	if recorded(`standard = 'EDIFACT' and refusal is null`) != 1 {
		t.Fatal("not recorded")
	}
	e.dispatch(1)
	lastAck(transport.ContentEdifact + "|UCI+42+" + euSender + "+PASSAGE:ZZZ+7'")
	if n := e.count(`select count(*) from transport_delivery where payload_kind = 'ACK' and filename like 'CONTRL\_42\_%.edi'`); n != 1 {
		t.Fatal("CONTRL delivery file name")
	}
	processed("eu-2@distributor", transport.ContentEdifact, edifactRef(t, ordersEdifact(t), "43"))
	e.dispatch(1)
	// The same interchange in a new message is a duplicate.
	if r, err := send("eu-1-again@distributor", transport.ContentEdifact, ordersEdifact(t)); err != nil || !strings.Contains(r.Disposition, "duplicate-document") {
		t.Fatalf("duplicate: %+v %v", r, err)
	}

	// A syntax error with a readable UNB: error MDN and a CONTRL UCI 4 with
	// the code, over the partner's transport.
	badUNZ := strings.Replace(edifactRef(t, ordersEdifact(t), "44"), "UNZ+1+44'", "UNZ+2+44'", 1)
	refused("eu-3@distributor", transport.ContentEdifact, badUNZ)
	if recorded(`refusal like 'EDI-0003:%' and control_number = '44' and ack_status = 'REJECTED'`) != 1 {
		t.Fatal("refusal not recorded")
	}
	if n := e.count(`select count(*) from transport_inbound where ref = 'eu-3@distributor' and status = 'PARKED' and interchange_id is not null`); n != 1 {
		t.Fatal("source not parked with the refusal")
	}
	e.dispatch(1)
	lastAck(transport.ContentEdifact + "|UCI+44+" + euSender + "+PASSAGE:ZZZ+4+29'")
	// Sent again: refused again, answered once.
	refused("eu-3@distributor", transport.ContentEdifact, badUNZ)
	if recorded(`control_number = '44'`) != 1 {
		t.Fatal("a retransmission was answered twice")
	}
	e.dispatch(0)
	// The corrected interchange with the same reference is not a duplicate.
	processed("eu-4@distributor", transport.ContentEdifact, edifactRef(t, ordersEdifact(t), "44"))
	e.dispatch(1)

	// Sender refusals: another partner's UNB identity, and an unknown one.
	createPartner(t, e.h, "other-eu", edifactPartnerBody("Other EU", "OTHEREU", "4012345000016", ""))
	refused("eu-5@distributor", transport.ContentEdifact, edifactRef(t, strings.Replace(ordersEdifact(t), euSender, "4012345000016:14", 1), "45"))
	e.dispatch(1)
	lastAck(transport.ContentEdifact + "|UCI+45+4012345000016:14+PASSAGE:ZZZ+4+23'")
	refused("eu-6@distributor", transport.ContentEdifact, edifactRef(t, strings.Replace(ordersEdifact(t), euSender, "4099999999999:14", 1), "46"))
	e.dispatch(1)
	lastAck(transport.ContentEdifact + "|UCI+46+4099999999999:14+PASSAGE:ZZZ+4+23'")

	// An X12 syntax error: a negative 997 naming the bad trailer.
	badSE := strings.Replace(x12Control(t, "000000301"), "SE*6*0001~", "SE*7*0001~", 1)
	if badSE == x12Control(t, "000000301") {
		t.Fatal("sample changed shape")
	}
	refused("x-1@distributor", transport.ContentX12, badSE)
	e.dispatch(1)
	lastAck(transport.ContentX12 + "|AK1*PO*1~AK2*850*0001~AK5*R*4~AK9*R*1*1*0~")
	if recorded(`standard = 'X12' and refusal like 'EDI-0003:%'`) != 1 {
		t.Fatal("X12 refusal not recorded")
	}

	// No answer: a CONTRL the UNB does not ask for, a body with no envelope,
	// and anything not signed with the pinned key.
	before := recorded(`true`)
	refused("eu-7@distributor", transport.ContentEdifact, strings.Replace(strings.Replace(badUNZ, "+44++++1'", "+47'", 1), "UNZ+2+44'", "UNZ+2+47'", 1))
	refused("eu-8@distributor", transport.ContentEdifact, "UNB+UNOC:3+"+euSender+"'")
	forged := e.partnerClient(v.AS2Inbound.URL, transporttest.NewKeyPair(t, "DISTRIBUTOR", false))
	if _, err := forged.Send(context.Background(), transport.Message{ID: "eu-9@x", ContentType: transport.ContentEdifact, Payload: []byte(badUNZ)}); !errors.Is(err, transport.ErrRejected) ||
		!strings.Contains(err.Error(), "authentication-failed") {
		t.Fatalf("forged: %v", err)
	}
	if after := recorded(`true`); after != before {
		t.Fatalf("%d interchanges recorded for traffic that gets no answer", after-before)
	}
	e.dispatch(0)
	if got := len(e.peer.messages()); got != 7 {
		t.Fatalf("partner got %d acknowledgments", got)
	}
}

func TestTransportSFTP_EdifactAndRefusal(t *testing.T) {
	e := newTransportEnv(t)
	e.putProfile()
	e.setEdifact(domain.ContrlRequested)
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
	e.mustPutTransport(`{"kind":"SFTP","sftp":{"host":"` + host + `","port":` + port + `,"user":"edi","hostKeyFingerprint":"` + fingerprint + `",
		"authMethod":"key","credentialRef":"sftp-distributor","outboundDir":"` + out + `","inboundDir":"` + in + `","pollIntervalSeconds":60}}`)
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(in, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	badUNZ := strings.Replace(edifactRef(t, ordersEdifact(t), "50"), "UNZ+1+50'", "UNZ+2+50'", 1)
	write("a-orders.edi", ordersEdifact(t))
	write("b-bad-unz.edi", badUNZ)
	write("c-junk.edi", "hello")
	p := &delivery.Poller{Store: e.st, Secrets: e.secrets, Receive: e.a.ReceiveFromTransport}
	poll := func(want int) {
		t.Helper()
		if n, err := p.RunOnce(context.Background()); err != nil || n != want {
			t.Fatalf("poll handled %d, want %d: %v", n, want, err)
		}
	}
	poll(3)
	if n := e.count(`select count(*) from interchange where standard = 'EDIFACT' and refusal is null and control_number = '42'`); n != 1 {
		t.Fatal("ORDERS not recorded")
	}
	if n := e.count(`select count(*) from transport_inbound where ref = 'b-bad-unz.edi' and status = 'PARKED' and interchange_id is not null`); n != 1 {
		t.Fatal("refused file not parked with its interchange")
	}
	if n := e.count(`select count(*) from transport_inbound where ref = 'c-junk.edi' and status = 'PARKED' and interchange_id is null`); n != 1 {
		t.Fatal("junk not parked")
	}
	// Both CONTRLs are uploaded to the partner.
	e.dispatch(2)
	files, _ := os.ReadDir(out)
	got := map[string]string{}
	for _, f := range files {
		b, _ := os.ReadFile(filepath.Join(out, f.Name()))
		got[f.Name()[:10]] = string(b)
	}
	if len(files) != 2 || !strings.Contains(got["CONTRL_42_"], "UCI+42+"+euSender+"+PASSAGE:ZZZ+7'") ||
		!strings.Contains(got["CONTRL_50_"], "UCI+50+"+euSender+"+PASSAGE:ZZZ+4+29'") || !strings.HasSuffix(files[0].Name(), ".edi") {
		t.Fatalf("outbound dir %v %v", files, got)
	}
	// The partner puts the refused file back: parked again, not answered twice.
	write("b-bad-unz.edi", badUNZ)
	if _, err := e.admin.Exec(context.Background(), `update partner_transport set sftp_next_poll_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	poll(1)
	if n := e.count(`select count(*) from interchange where control_number = '50'`); n != 1 {
		t.Fatalf("%d interchanges for the re-sent file", n)
	}
	e.dispatch(0)
	if _, err := os.Stat(filepath.Join(in, "b-bad-unz.edi")); !os.IsNotExist(err) {
		t.Fatalf("the re-sent file is still in the inbox: %v", err)
	}
}
