package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/edi/internal/delivery"
	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	ediev "github.com/BenjaminSRussell/passage/services/edi/internal/events"
	"github.com/BenjaminSRussell/passage/services/edi/internal/transport"
	"github.com/BenjaminSRussell/passage/services/edi/internal/transport/transporttest"
)

// X12 refused at the interchange level gets a TA1 R with its TA105 code
// instead of a 997; a TA1 from the partner is matched to our outbound
// interchange (R rejects its messages, A is noted) and is never answered.
func TestTransportAS2_TA1ForEnvelopeErrorsAndPartnerTA1Matched(t *testing.T) {
	e := newTransportEnv(t)
	e.putProfile()
	v := e.mustPutTransport(e.as2Config(nil))
	c := e.partnerClient(v.AS2Inbound.URL, e.them)
	send := func(id, payload string) (transport.Receipt, error) {
		return c.Send(context.Background(), transport.Message{ID: id, ContentType: transport.ContentX12, Payload: []byte(payload)})
	}
	lastPayload := func() string {
		got := e.peer.messages()
		return string(got[len(got)-1].Payload)
	}

	// IEA missing and IEA02 wrong: refused, answered with TA1 R 023 and 001.
	full := x12Control(t, "000000201")
	noIEA := full[:strings.Index(full, "IEA*")]
	if _, err := send("x-1@distributor", noIEA); !errors.Is(err, transport.ErrRejected) {
		t.Fatalf("no IEA: %v", err)
	}
	e.dispatch(1)
	if p := lastPayload(); !strings.Contains(p, "~TA1*000000201*") || !strings.Contains(p, "*R*023~IEA*0*") || strings.Contains(p, "AK1") {
		t.Fatalf("TA1 %s", p)
	}
	badIEA := strings.Replace(x12Control(t, "000000202"), "IEA*1*000000202", "IEA*1*000000999", 1)
	if _, err := send("x-2@distributor", badIEA); !errors.Is(err, transport.ErrRejected) {
		t.Fatalf("IEA02: %v", err)
	}
	e.dispatch(1)
	if p := lastPayload(); !strings.Contains(p, "~TA1*000000202*") || !strings.Contains(p, "*R*001~") {
		t.Fatalf("TA1 %s", p)
	}
	if n := e.count(`select count(*) from interchange where refusal like 'EDI-0003:%' and ack_payload like '%TA1*%' and ack_status = 'REJECTED'`); n != 2 {
		t.Fatalf("%d refusals answered with a TA1", n)
	}
	var id string
	if err := e.admin.QueryRow(context.Background(), `select id::text from interchange where control_number = '000000201'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if d := e.detail(id); d.TA1 == "" || d.AckPayload != "" || d.Refusal == "" {
		t.Fatalf("refused detail %+v", d)
	}
	// The control numbers of the TA1s come from the partner's X12 counter.
	if n := e.count(`select count(*) from control_number_counter where standard = 'X12' and last_value = 2`); n != 1 {
		t.Fatal("TA1s took the X12 counter")
	}

	// Our 856 goes out as ISA13 3; the partner rejects it with a TA1 R.
	out := e.outbound("asn-ta1")
	if out.ControlNumber != "000000003" {
		t.Fatalf("outbound %+v", out)
	}
	e.dispatch(1)
	sent, err := domain.Parse(out.X12)
	if err != nil {
		t.Fatal(err)
	}
	theirs := func(code, note, control string) string {
		ta1, err := domain.BuildTA1(sent, code, note, control, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		raw, err := domain.Serialize(ta1)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	if r, err := send("ta1-1@distributor", theirs(domain.TA1Accepted, domain.TA1NoError, "000000501")); err != nil || !strings.HasSuffix(r.Disposition, "; processed") {
		t.Fatalf("TA1 A: %+v %v", r, err)
	}
	if d := e.detail(out.InterchangeID); d.AckStatus != "PENDING" || d.Messages[0].Status != "SENT" {
		t.Fatalf("a TA1 A changes nothing: %+v", d)
	}
	if r, err := send("ta1-2@distributor", theirs(domain.TA1Rejected, "024", "000000502")); err != nil || !strings.HasSuffix(r.Disposition, "; processed") {
		t.Fatalf("TA1 R: %+v %v", r, err)
	}
	if d := e.detail(out.InterchangeID); d.AckStatus != "REJECTED" || d.Messages[0].Status != "FAILED" || !strings.Contains(d.Messages[0].Error, "TA105 024") {
		t.Fatalf("after TA1 R: %+v", d)
	}
	if n := e.count(`select count(*) from outbox where type = $1 and payload->'data'->>'interchange_id' = $2 and payload->'data'->>'ack_status' = 'REJECTED'`,
		ediev.MessageAcked, out.InterchangeID); n != 1 {
		t.Fatalf("%d message.acked", n)
	}
	if n := e.count(`select count(*) from message where transaction_set = 'TA1' and status = 'POSTED'`); n != 2 {
		t.Fatalf("%d posted TA1s", n)
	}
	// A TA1 for nothing we sent is FAILED with EDI-0161; no TA1 is answered.
	stray := strings.Replace(theirs(domain.TA1Rejected, "024", "000000503"), "TA1*000000003*", "TA1*000000777*", 1)
	if _, err := send("ta1-3@distributor", stray); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`select count(*) from message where transaction_set = 'TA1' and status = 'FAILED' and error like 'EDI-0161:%'`); n != 1 {
		t.Fatal("stray TA1")
	}
	// A broken TA1 interchange is refused without a TA1 back.
	if _, err := send("ta1-4@distributor", strings.Replace(stray, "IEA*0*", "IEA*1*", 1)); !errors.Is(err, transport.ErrRejected) {
		t.Fatalf("broken TA1: %v", err)
	}
	e.dispatch(0)
}

// A duplicate interchange in a new SFTP file is archived and answered once
// with a TA1 R 025; the file put back again is not answered twice.
func TestTransportSFTP_DuplicateAnsweredWithTA1025(t *testing.T) {
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
	e.mustPutTransport(`{"kind":"SFTP","sftp":{"host":"` + host + `","port":` + port + `,"user":"edi","hostKeyFingerprint":"` + fingerprint + `",
		"authMethod":"key","credentialRef":"sftp-distributor","outboundDir":"` + out + `","inboundDir":"` + in + `","pollIntervalSeconds":60}}`)
	write := func(name string) {
		if err := os.WriteFile(filepath.Join(in, name), []byte(x12Control(t, "000000301")), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p := &delivery.Poller{Store: e.st, Secrets: e.secrets, Receive: e.a.ReceiveFromTransport}
	poll := func(want int) {
		t.Helper()
		if _, err := e.admin.Exec(context.Background(), `update partner_transport set sftp_next_poll_at = now() - interval '1 second'`); err != nil {
			t.Fatal(err)
		}
		if n, err := p.RunOnce(context.Background()); err != nil || n != want {
			t.Fatalf("poll handled %d, want %d: %v", n, want, err)
		}
	}
	write("a.x12")
	poll(1)
	write("b.x12") // the same interchange in another file
	poll(1)
	write("b.x12") // and that file again
	poll(1)
	e.dispatch(2) // the 997 for a.x12 and the TA1 for b.x12
	files, _ := os.ReadDir(out)
	var ta1s int
	for _, f := range files {
		b, _ := os.ReadFile(filepath.Join(out, f.Name()))
		if strings.HasPrefix(f.Name(), "TA1_000000301_") && strings.Contains(string(b), "TA1*000000301*") && strings.Contains(string(b), "*R*025~") {
			ta1s++
		}
	}
	if len(files) != 2 || ta1s != 1 {
		t.Fatalf("outbound dir %v", files)
	}
	if n := e.count(`select count(*) from interchange where control_number = '000000301' and refusal is null`); n != 1 {
		t.Fatalf("%d receipts", n)
	}
	if n := e.count(`select count(*) from transport_inbound where ref = 'b.x12' and status = 'DONE' and interchange_id is not null`); n != 1 {
		t.Fatal("the duplicate's file is not DONE with its refusal")
	}
	// The route still answers a duplicate with 409 and records nothing.
	if rec := do(t, e.h, http.MethodPost, "/edi/v1/inbound/x12?environment=TEST", x12Control(t, "000000301"), "edi.operate", nil); rec.Code != http.StatusConflict {
		t.Fatalf("route duplicate: %d", rec.Code)
	}
	if n := e.count(`select count(*) from interchange where control_number = '000000301'`); n != 2 {
		t.Fatalf("%d interchanges", n)
	}
}
