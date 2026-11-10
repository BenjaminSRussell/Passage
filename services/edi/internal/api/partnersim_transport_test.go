package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/edi/internal/delivery"
	ediev "github.com/BenjaminSRussell/passage/services/edi/internal/events"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
	"github.com/BenjaminSRussell/passage/services/edi/internal/transport"
	"github.com/BenjaminSRussell/passage/services/edi/internal/transport/transporttest"
)

// The partner simulator over the transports (AC-35.1): the same distributor,
// with its own X12 and EDIFACT codecs (partnersim_test.go), now trading over
// AS2 (X12) and SFTP (EDIFACT) with the service's listener, poller and
// dispatcher, so every transaction set the service maps or sends goes through
// an independent implementation in both directions: X12 820 and 214 in and
// out with 997s, TA1s both ways, EDIFACT ORDERS, DESADV and INVOIC in and out
// with CONTRLs both ways, and duplicates answered with TA1 025 and CONTRL 26.

// simDetail reads an interchange with the party names unmasked.
func simDetail(t *testing.T, e *transportEnv, id string) store.InterchangeDetail {
	t.Helper()
	rec := do(t, e.h, http.MethodGet, "/edi/v1/interchanges/"+id, "", "edi.operate,edi.pii.view", srcScope)
	var d store.InterchangeDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("detail: %d %s", rec.Code, rec.Body.String())
	}
	return d
}

func simInterchangeID(t *testing.T, e *transportEnv, direction, standard, control string) string {
	t.Helper()
	var id string
	if err := e.admin.QueryRow(context.Background(), `select id::text from interchange where direction = $1 and standard = $2
		and control_number = $3 and refusal is null`, direction, standard, control).Scan(&id); err != nil {
		t.Fatalf("%s %s interchange %s: %v", direction, standard, control, err)
	}
	return id
}

func simOutbound(t *testing.T, e *transportEnv, set, key, document string) store.OutboundResult {
	t.Helper()
	out, code, pc := postOutbound(t, e.h, e.partner, set, document, key, srcScope)
	if code != http.StatusCreated || out.Delivery == nil {
		t.Fatalf("outbound %s: %d %s %+v", set, code, pc, out)
	}
	return out
}

// simRemittance is the distributor's 820 group: 0001 pays INV-7001A and
// INV-6950 (less a 4.57 discount), 1043.82 in all; 0002 states 500.00 for
// lines of 450.00 and is rejected (BPR02 is not the RMR04 total).
func simRemittance(isa13 string) string {
	return simX12(isa13, nil, simGroup{fid: "RA", control: "501", sets: []simSet{
		{id: "820", control: "0001", body: []string{
			"BPR|C|1043.82|C|ACH||||||||||||20261106",
			"TRN|1|ACH-2026-1106",
			"CUR|PR|EUR",
			"N1|PR|Demo Distributor|92|4000001234561",
			"N1|PE|Demo Manufacturer|92|4006403333338",
			"ENT|1",
			"RMR|IV|INV-7001A||819.92|819.92|0.00",
			"DTM|003|20261024",
			"RMR|IV|INV-6950||223.90|228.47|4.57",
		}},
		{id: "820", control: "0002", body: []string{
			"BPR|C|500.00|C|CHK||||||||||||20261106",
			"TRN|1|CHK-9917",
			"N1|PR|Demo Distributor|92|4000001234561",
			"N1|PE|Demo Manufacturer|92|4006403333338",
			"RMR|IV|INV-6951||450.00|450.00|0.00",
		}},
	}})
}

// simStatus is the distributor's 214: its own truck collected and delivered
// a return.
func simStatus(isa13 string) string {
	return simX12(isa13, nil, simGroup{fid: "QM", control: "502", sets: []simSet{{id: "214", control: "0001", body: []string{
		"B10|PRO-55120|RET-SHIP-9|DSTR",
		"L11|BOL-RET-9|BM",
		"L11|RET-77|PO",
		"N1|SF|Distributor DC|92|4000001234561",
		"N1|ST|Plant Koeln|92|4006403333338",
		"LX|1",
		"AT7|AF|NS|||20261105|0715|LT",
		"MS1|Hamburg||DE",
		"LX|2",
		"AT7|D1|NS|||20261106|1130|LT",
		"MS1|Koeln||DE",
	}}}})
}

const simRemittanceOut = `{"traceNumber":"ACH-PSG-31","paymentDate":"20261107","method":"ACH","currency":"EUR","amount":"148.00",
"payer":{"name":"Demo Manufacturer","gln":"4006403333338"},"payee":{"name":"Demo Distributor","gln":"4000001234561"},
"items":[{"invoiceNumber":"CB-301","invoiceDate":"20261101","paid":"100.00","invoiced":"100.00","discount":"0"},
{"invoiceNumber":"CB-302","paid":"48.00","invoiced":"50.00","discount":"2.00"}]}`

func simStatusOut(pro string, delivered bool) string {
	events := `{"code":"AF","reason":"NS","date":"20261023","time":"0700","city":"Koeln","country":"DE"}`
	if delivered {
		events += `,{"code":"D1","reason":"NS","date":"20261024","time":"1015","city":"Hamburg","country":"DE"}`
	}
	return `{"proNumber":"` + pro + `","shipmentId":"SHIP-88001","scac":"DHLD","bol":"BOL-88001","poNumber":"PO-88001",
"shipFrom":{"name":"Plant Koeln","gln":"4006403333338"},"shipTo":{"name":"Distributor DC","gln":"4000001234561"},"events":[` + events + `]}`
}

func TestPartnerSim_X12RemittanceStatusAndTA1OverAS2(t *testing.T) {
	e := newTransportEnv(t)
	e.putProfile()
	v := e.mustPutTransport(e.as2Config(nil))
	c := e.partnerClient(v.AS2Inbound.URL, e.them)
	send := func(id, payload string) (transport.Receipt, error) {
		return c.Send(context.Background(), transport.Message{ID: id, ContentType: transport.ContentX12, Payload: []byte(payload)})
	}
	processed := func(id, payload string) {
		t.Helper()
		if r, err := send(id, payload); err != nil || !strings.HasSuffix(r.Disposition, "; processed") {
			t.Fatalf("%s: %+v %v", id, r, err)
		}
	}
	// fromUs runs the dispatcher and reads what reached the distributor's
	// AS2 server since the last call. Everything we send it, documents and
	// acknowledgments, comes from PASSAGE on one gap-free ISA13 sequence.
	seen, lastISA13 := 0, 0
	fromUs := func(want int) []simDoc {
		t.Helper()
		e.dispatch(want)
		got := e.peer.messages()[seen:]
		seen += len(got)
		var docs []simDoc
		for _, m := range got {
			d := simParse(t, string(m.Payload))
			lastISA13++
			if m.ContentType != transport.ContentX12 || strings.TrimSpace(d.isa[6]) != "PASSAGE" || strings.TrimSpace(d.isa[8]) != "DISTRIBUTOR" ||
				d.isa[13] != fmt.Sprintf("%09d", lastISA13) || d.isa[15] != "T" {
				t.Fatalf("from us: %s %v, want ISA13 %d", m.ContentType, d.isa, lastISA13)
			}
			docs = append(docs, d)
		}
		return docs
	}
	body := func(tx simTx) string {
		var lines []string
		for _, s := range tx.body {
			lines = append(lines, s.String())
		}
		return strings.Join(lines, " ")
	}
	// Acknowledgments of the distributor's interchanges come back in its
	// delimiters and version.
	ackOf := func(d simDoc, fid, group string) string {
		t.Helper()
		if d.el != "|" || d.isa[11] != "^" || d.isa[12] != "00501" || d.isa[16] != ":" || len(d.txs) != 1 || d.txs[0].set != "997" || d.txs[0].gsFID != "FA" {
			t.Fatalf("997 envelope: %v %+v", d.isa, d.txs)
		}
		if ak1 := d.txs[0].body[0]; ak1[1] != fid || ak1[2] != group {
			t.Fatalf("997 answers %v, want %s %s", ak1, fid, group)
		}
		return body(d.txs[0])
	}

	// 1. The distributor's 820 group: the clean remittance is mapped, the
	// one whose lines do not add up is rejected, AK9 P.
	processed("rem-501@distributor", simRemittance("501"))
	if got := ackOf(fromUs(1)[0], "RA", "501"); got != "AK1*RA*501 AK2*820*0001 AK5*A AK2*820*0002 AK5*R AK9*P*2*2*1" {
		t.Fatalf("997 for the 820s: %s", got)
	}
	d := simDetail(t, e, simInterchangeID(t, e, "IN", "X12", "000000501"))
	var rem struct {
		TraceNumber, PaymentDate, Method, Currency string
		Amount                                     decimal.Decimal
		Payer, Payee                               struct{ Name, GLN string }
		Items                                      []struct {
			InvoiceNumber, InvoiceDate string
			Paid, Invoiced, Discount   decimal.Decimal
		}
	}
	if err := json.Unmarshal(d.Messages[0].Canonical, &rem); err != nil {
		t.Fatal(err)
	}
	if d.AckStatus != "ACCEPTED_WITH_ERRORS" || d.Messages[0].Status != "MAPPED" || rem.TraceNumber != "ACH-2026-1106" || rem.Method != "ACH" ||
		rem.PaymentDate != "20261106" || rem.Currency != "EUR" || !rem.Amount.Equal(decimal.RequireFromString("1043.82")) ||
		rem.Payer.GLN != "4000001234561" || rem.Payee.Name != "Demo Manufacturer" || len(rem.Items) != 2 || rem.Items[0].InvoiceDate != "20261024" ||
		!rem.Items[1].Discount.Equal(decimal.RequireFromString("4.57")) || !rem.Items[1].Invoiced.Equal(decimal.RequireFromString("228.47")) {
		t.Fatalf("canonical remittance %+v (%s)", rem, d.AckStatus)
	}
	if m := d.Messages[1]; m.Status != "FAILED" || !strings.Contains(m.Error, "differs from the RMR04 total") {
		t.Fatalf("second 820: %+v", m)
	}

	// 2. The distributor's 214.
	processed("st-502@distributor", simStatus("502"))
	if got := ackOf(fromUs(1)[0], "QM", "502"); got != "AK1*QM*502 AK2*214*0001 AK5*A AK9*A*1*1*1" {
		t.Fatalf("997 for the 214: %s", got)
	}
	var status struct {
		ProNumber, ShipmentID, SCAC, BOL, PONumber string
		ShipFrom, ShipTo                           struct{ Name, GLN string }
		Events                                     []struct{ Code, Reason, Date, Time, City, State, Country string }
	}
	if err := json.Unmarshal(simDetail(t, e, simInterchangeID(t, e, "IN", "X12", "000000502")).Messages[0].Canonical, &status); err != nil {
		t.Fatal(err)
	}
	if status.ProNumber != "PRO-55120" || status.ShipmentID != "RET-SHIP-9" || status.SCAC != "DSTR" || status.BOL != "BOL-RET-9" || status.PONumber != "RET-77" ||
		status.ShipFrom.GLN != "4000001234561" || status.ShipTo.Name != "Plant Koeln" || len(status.Events) != 2 ||
		fmt.Sprint(status.Events[1]) != "{D1 NS 20261106 1130 Koeln  DE}" {
		t.Fatalf("canonical status %+v", status)
	}
	if n := e.count(`select count(*) from outbox where type = $1 and payload->'data'->>'transaction_set' in ('820', '214')`, ediev.MessageReceived); n != 3 {
		t.Fatalf("%d message.received events for the 820s and the 214", n)
	}
	// A load tender (204) is not a set the service maps: recorded FAILED and
	// rejected in the 997.
	processed("lt-510@distributor", simX12("510", nil, simGroup{fid: "SM", control: "510", sets: []simSet{{id: "204", control: "0001", body: []string{
		"B2||DSTR||LT-510||PP", "B2A|00", "S5|1|LD", "L3|412.4|G",
	}}}}))
	if got := ackOf(fromUs(1)[0], "SM", "510"); got != "AK1*SM*510 AK2*204*0001 AK5*R AK9*R*1*1*0" {
		t.Fatalf("997 for the 204: %s", got)
	}
	if m := simDetail(t, e, simInterchangeID(t, e, "IN", "X12", "000000510")).Messages[0]; m.Status != "FAILED" || !strings.Contains(m.Error, "204 is not supported") {
		t.Fatalf("204: %+v", m)
	}

	// 3. Our 820 and 214, read by the distributor.
	remOut := simOutbound(t, e, "820", "rem-out", simRemittanceOut)
	stOut := simOutbound(t, e, "214", "st-out", simStatusOut("PRO-PSG-9", false))
	docs := fromUs(2)
	byFID := map[string]simDoc{}
	for _, d := range docs {
		if d.el != "*" || d.isa[12] != "00401" || len(d.txs) != 1 {
			t.Fatalf("our document envelope %v", d.isa)
		}
		byFID[d.txs[0].gsFID] = d
	}
	rtx, stx := byFID["RA"].txs[0], byFID["QM"].txs[0]
	if rtx.set != "820" || stx.set != "214" {
		t.Fatalf("sets %s %s", rtx.set, stx.set)
	}
	// The distributor's own remittance check: BPR02 is the RMR04 total.
	bpr := simFind(rtx.body, "BPR")[0]
	paid := decimal.Zero
	var invoices []string
	for _, rmr := range simFind(rtx.body, "RMR") {
		paid = paid.Add(decimal.RequireFromString(rmr[4]))
		invoices = append(invoices, rmr[2]+"="+rmr[4]+"/"+rmr[5]+"-"+rmr[6])
	}
	if !decimal.RequireFromString(bpr[2]).Equal(paid) || bpr[2] != "148.00" || bpr[4] != "ACH" || bpr[16] != "20261107" ||
		simFind(rtx.body, "TRN")[0][2] != "ACH-PSG-31" || strings.Join(invoices, " ") != "CB-301=100.00/100.00-0.00 CB-302=48.00/50.00-2.00" ||
		simFind(rtx.body, "N1")[1].String() != "N1*PE*Demo Distributor*92*4000001234561" {
		t.Fatalf("820 as read by the distributor: %s", body(rtx))
	}
	var events []string
	for i, s := range stx.body {
		if s[0] == "LX" {
			at7 := stx.body[i+1]
			events = append(events, s[1]+":"+at7[1]+"/"+at7[2]+"@"+at7[5]+at7[6]+at7[7])
		}
	}
	if b10 := simFind(stx.body, "B10")[0]; b10.String() != "B10*PRO-PSG-9*SHIP-88001*DHLD" || strings.Join(events, " ") != "1:AF/NS@202610230700LT" ||
		len(simFind(stx.body, "L11")) != 2 {
		t.Fatalf("214 as read by the distributor: %s", body(stx))
	}
	// The distributor accepts the 820 with a 997, and answers the 214 with a
	// TA1 A next to a 997 accepting it with errors.
	processed("fa-503@distributor", simX12("503", nil, simGroup{fid: "FA", control: "503", sets: []simSet{
		simAckSet("RA", rtx.gsControl, []simAnswer{{"820", rtx.control, "A"}}, "A")}}))
	processed("fa-504@distributor", simX12("504", []string{"TA1|" + byFID["QM"].isa[13] + "|" + byFID["QM"].isa[9] + "|" + byFID["QM"].isa[10] + "|A|000"},
		simGroup{fid: "FA", control: "504", sets: []simSet{simAckSet("QM", stx.gsControl, []simAnswer{{"214", stx.control, "E"}}, "E")}}))

	// 4. A second 214 is refused at the interchange level with a TA1 R.
	stOut2 := simOutbound(t, e, "214", "st-out-2", simStatusOut("PRO-PSG-10", true))
	d2 := fromUs(1)[0]
	if d2.txs[0].set != "214" || len(simFind(d2.txs[0].body, "LX")) != 2 {
		t.Fatalf("second 214: %+v", d2.txs)
	}
	processed("ta1-505@distributor", simX12("505", []string{"TA1|" + d2.isa[13] + "|" + d2.isa[9] + "|" + d2.isa[10] + "|R|022"}))

	var states []string
	for _, id := range []string{remOut.InterchangeID, stOut.InterchangeID, stOut2.InterchangeID} {
		d := e.detail(id)
		states = append(states, d.Messages[0].TransactionSet+":"+d.AckStatus+":"+d.Messages[0].Status)
	}
	if want := "820:ACCEPTED:ACKED 214:ACCEPTED_WITH_ERRORS:ACKED 214:REJECTED:FAILED"; strings.Join(states, " ") != want {
		t.Fatalf("outbound states\n got %s\nwant %s", strings.Join(states, " "), want)
	}
	if m := e.detail(stOut2.InterchangeID).Messages[0]; !strings.Contains(m.Error, "022") {
		t.Fatalf("TA1 R note not on the message: %+v", m)
	}
	if n := e.count(`select count(*) from outbox where type = $1`, ediev.MessageAcked); n != 3 {
		t.Fatalf("%d message.acked events, want 3", n)
	}
	// 997s and TA1s are never answered.
	fromUs(0)

	// 5. The distributor sends its 820 interchange again in a new AS2
	// message: a duplicate, answered with a TA1 R 025 in its delimiters.
	if r, err := send("rem-501-again@distributor", simRemittance("501")); err != nil || !strings.Contains(r.Disposition, "duplicate-document") {
		t.Fatalf("duplicate: %+v %v", r, err)
	}
	ta1 := fromUs(1)[0]
	if ta1.el != "|" || ta1.isa[12] != "00501" || len(ta1.txs) != 0 || len(ta1.ta1) != 1 ||
		ta1.ta1[0].String() != "TA1*000000501*261106*0800*R*025" {
		t.Fatalf("TA1 for the duplicate: %v %v %+v", ta1.isa, ta1.ta1, ta1.txs)
	}

	// 6. An interchange whose IEA02 is not its ISA13: refused, TA1 R 001.
	broken := strings.Replace(simStatus("506"), "IEA|1|000000506", "IEA|1|000000599", 1)
	if _, err := send("st-506@distributor", broken); !errors.Is(err, transport.ErrRejected) {
		t.Fatalf("IEA02 mismatch: %v", err)
	}
	if ta1 := fromUs(1)[0]; len(ta1.ta1) != 1 || ta1.ta1[0].String() != "TA1*000000506*261106*0800*R*001" {
		t.Fatalf("TA1 for the broken envelope: %v", ta1.ta1)
	}
	fromUs(0)
}

// simOrderDocument is the manufacturer's purchase order to the distributor
// (EDIFACT ORDERS); line 2's description carries EDIFACT service characters.
const simOrderDocument = `{"purpose":"00","orderType":"SA","poNumber":"PO-MFG-501","poDate":"20261107","currency":"EUR",
"shipToName":"Plant Koeln","shipToGln":"4006403333338",
"lines":[{"lineNo":"1","quantity":"500","uom":"EA","unitPrice":"0.84","itemRef":{"qualifier":"VP","value":"BRK-7"}},
{"lineNo":"2","quantity":"40","uom":"EA","unitPrice":"9.90","itemRef":{"qualifier":"BP","value":"SEAL-1"},"description":"Seal kit: 40+ pcs, 'heavy'"}]}`

func TestPartnerSim_EdifactRoundTripOverSFTP(t *testing.T) {
	e := newTransportEnv(t)
	e.putProfile()
	e.setEdifact("REQUESTED")
	signer, keyPEM := transporttest.NewSSHKey(t)
	addr, fingerprint := transporttest.StartSFTPServer(t, transporttest.SFTPLogin{User: "edi", Key: signer.PublicKey()})
	host, port, _ := strings.Cut(addr, ":")
	e.secrets["sftp-distributor"] = keyPEM
	root := t.TempDir()
	out, in := filepath.Join(root, "out"), filepath.Join(root, "in")
	for _, dir := range []string{out, in} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	e.mustPutTransport(`{"kind":"SFTP","sftp":{"host":"` + host + `","port":` + port + `,"user":"edi","hostKeyFingerprint":"` + fingerprint + `",
		"authMethod":"key","credentialRef":"sftp-distributor","outboundDir":"` + out + `","inboundDir":"` + in + `","pollIntervalSeconds":60}}`)
	p := &delivery.Poller{Store: e.st, Secrets: e.secrets, Receive: e.a.ReceiveFromTransport}
	// put drops a file in the distributor's outbox (our inbound directory)
	// and has the poller collect it.
	put := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(in, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := e.admin.Exec(context.Background(), `update partner_transport set sftp_next_poll_at = now() - interval '1 second'`); err != nil {
			t.Fatal(err)
		}
		if n, err := p.RunOnce(context.Background()); err != nil || n != 1 {
			t.Fatalf("poll of %s handled %d: %v", name, n, err)
		}
	}
	// fromUs runs the dispatcher and reads the files it uploaded, by file
	// name. Every interchange we send the distributor comes from PASSAGE:ZZZ
	// in UNOC syntax 3, flagged as a test, on one gap-free reference sequence.
	seen, lastRef := map[string]bool{}, 0
	fromUs := func(want int) map[string]simEdiDoc {
		t.Helper()
		e.dispatch(want)
		files, err := os.ReadDir(out)
		if err != nil {
			t.Fatal(err)
		}
		docs := map[string]simEdiDoc{}
		var names []string
		for _, f := range files {
			if !seen[f.Name()] {
				names = append(names, f.Name())
			}
		}
		if len(names) != want {
			t.Fatalf("%d new files, want %d", len(names), want)
		}
		read := func(name string) simEdiDoc {
			raw, err := os.ReadFile(filepath.Join(out, name))
			if err != nil {
				t.Fatal(err)
			}
			return simEdiParse(t, string(raw))
		}
		// In the order we numbered them.
		sort.Slice(names, func(i, j int) bool {
			a, _ := strconv.Atoi(read(names[i]).unb.v(5, 1))
			b, _ := strconv.Atoi(read(names[j]).unb.v(5, 1))
			return a < b
		})
		for _, name := range names {
			seen[name] = true
			d := read(name)
			lastRef++
			if !strings.HasSuffix(name, ".edi") || d.unb.v(1, 1) != "UNOC" || d.unb.v(1, 2) != "3" || d.unb.v(2, 1)+":"+d.unb.v(2, 2) != "PASSAGE:ZZZ" ||
				d.unb.v(3, 1)+":"+d.unb.v(3, 2) != simGLN+":14" || d.unb.v(5, 1) != strconv.Itoa(lastRef) || d.unb.v(11, 1) != "1" || len(d.msgs) != 1 {
				t.Fatalf("from us: %s %v, want reference %d", name, d.unb, lastRef)
			}
			docs[name[:strings.Index(name, "_")]] = d
		}
		return docs
	}

	// 1. The distributor's interchange 5001: two ORDERS (the second counts
	// three lines in CNT for one LIN), a DESADV for a return with the GTIN in
	// LIN and an INVOIC for a service, with a decimal comma and released
	// service characters.
	gtin := simGS1Complete("400086214140")
	sscc := simGS1Complete("04012345000000001")
	orders := simMsg{ref: "1", typ: "ORDERS:D:96A:UN:EAN008", body: []string{
		"BGM+220+PO-EU-9001+9",
		"DTM+137:20261106:102",
		"DTM+2:20261113:102",
		"NAD+SU+4006403333338::9",
		"NAD+IV+" + simGLN + "::9++" + simEsc("Müller + Söhne 'Nord'"),
		"NAD+DP+4012345000030::9++Lager Süd",
		"CUX+2:EUR:9",
		"ALC+C++++FC:::Fracht",
		"MOA+23:12,50",
		"LIN+1++" + gtin + ":SRV",
		"IMD+F++:::" + simEsc("Schraube M8:20"),
		"QTY+21:100:PCE",
		"PRI+AAA:0,15",
		"LIN+2",
		"PIA+5+SKU-77:SA",
		"QTY+21:4,5:KGM",
		"PRI+AAA:12,00",
		"UNS+S",
		"CNT+2:2",
	}}
	badCount := simMsg{ref: "2", typ: "ORDERS:D:96A:UN:EAN008", body: []string{
		"BGM+220+PO-EU-9002+9", "DTM+137:20261106:102", "NAD+DP+4012345000030::9", "LIN+1++" + gtin + ":SRV",
		"QTY+21:1:PCE", "PRI+AAA:0,15", "UNS+S", "CNT+2:3",
	}}
	desadv := simMsg{ref: "3", typ: "DESADV:D:96A:UN:EAN007", body: []string{
		"BGM+351+DESP-EU-77+9",
		"DTM+137:20261107:102",
		"DTM+11:202611070600:203",
		"MEA+AAX+AAD+KGM:96,5",
		"RFF+BM:BOL-EU-1",
		"NAD+SF+" + simGLN + "::9++Distributor Lager",
		"NAD+ST+4006403333338::9++Plant Koeln",
		"TDT+20++++DHLD::182",
		"CPS+1",
		"PAC+1",
		"CPS+2+1",
		"PAC+1",
		"PCI+33E",
		"GIN+BJ+" + sscc,
		"LIN+1++" + gtin + ":SRV",
		"PIA+1+PUMP-200:IN",
		"QTY+12:2:EA",
		"RFF+ON:RET-77",
		"DTM+171:20261101:102",
		"CNT+2:1",
	}}
	invoic := simMsg{ref: "4", typ: "INVOIC:D:96A:UN:EAN008", body: []string{
		"BGM+380+INV-EU-301+9",
		"DTM+137:20261107:102",
		"RFF+ON:PO-MFG-77",
		"DTM+171:20261020:102",
		"NAD+PE+" + simGLN + "::9++Distributor",
		"NAD+IV+4006403333338::9++Demo Manufacturer",
		"NAD+DP+4006403333338::9++Plant Koeln",
		"ALC+C++++D240",
		"MOA+23:5,00",
		"LIN+1++" + gtin + ":SRV",
		"QTY+47:3:EA",
		"MOA+203:60,00",
		"PRI+AAA:20",
		"UNS+S",
		"CNT+2:1",
		"MOA+86:76,40",
		"TAX+7+VAT+++:::19",
		"MOA+124:11,40",
	}}
	interchange := simEdifact("5001", true, orders, badCount, desadv, invoic)
	put("eu-5001.edi", interchange)
	got := fromUs(1)
	contrl, ok := got["CONTRL"]
	if !ok || contrl.una != ":+,? '" || contrl.unb.v(9, 1) != "" || len(contrl.msgs) != 1 || contrl.msgs[0].typ != "CONTRL:2:2:UN" {
		t.Fatalf("CONTRL envelope: %q %v %+v", contrl.una, contrl.unb, contrl.msgs)
	}
	var lines []string
	for _, s := range contrl.msgs[0].body {
		lines = append(lines, s.String())
	}
	cntPos := strconv.Itoa(len(badCount.body) + 1) // UNH is 1
	if want := "UCI+5001+" + simGLN + ":14+PASSAGE:ZZZ+7 UCM+2+ORDERS:D:96A:UN:EAN008+4+29 UCS+" + cntPos + "+29 UCD+29+1:2"; strings.Join(lines, " ") != want {
		t.Fatalf("CONTRL\n got %s\nwant %s", strings.Join(lines, " "), want)
	}
	d := simDetail(t, e, simInterchangeID(t, e, "IN", "EDIFACT", "5001"))
	var statuses []string
	for _, m := range d.Messages {
		statuses = append(statuses, m.TransactionSet+"/"+m.STControl+"="+m.Status)
	}
	if strings.Join(statuses, " ") != "ORDERS/1=MAPPED ORDERS/2=FAILED DESADV/3=MAPPED INVOIC/4=MAPPED" {
		t.Fatalf("messages %v: %+v", statuses, d.Messages)
	}
	var order struct {
		PONumber, PODate, RequestedDelivery, VendorID, Currency string
		BillToName, BillToGLN, ShipToName, ShipToGLN            string
		Charges                                                 []struct{ Indicator, Code, Amount, Description string }
		Lines                                                   []struct {
			LineNo, Quantity, UOM, UnitPrice, Description string
			ItemRef                                       struct{ Qualifier, Value string }
		}
	}
	if err := json.Unmarshal(d.Messages[0].Canonical, &order); err != nil {
		t.Fatal(err)
	}
	if order.PONumber != "PO-EU-9001" || order.PODate != "20261106" || order.RequestedDelivery != "20261113" || order.VendorID != "4006403333338" ||
		order.Currency != "EUR" || order.BillToName != "Müller + Söhne 'Nord'" || order.ShipToName != "Lager Süd" ||
		len(order.Charges) != 1 || order.Charges[0].Amount != "12.50" || order.Charges[0].Description != "Fracht" || len(order.Lines) != 2 ||
		fmt.Sprint(order.Lines[0]) != "{1 100 PCE 0.15 Schraube M8:20 {EN "+gtin+"}}" || fmt.Sprint(order.Lines[1]) != "{2 4.5 KGM 12.00  {VP SKU-77}}" {
		t.Fatalf("canonical order %+v", order)
	}
	var asn struct {
		ShipmentID, ShipDate, ShipTime, SCAC, BOL, WeightUnit string
		GrossWeight                                           decimal.Decimal
		ShipFrom, ShipTo                                      struct{ Name, GLN string }
		Orders                                                []struct {
			PONumber, PODate string
			Packs            []struct {
				SSCC  string
				Items []struct {
					LineNo, UOM string
					Quantity    decimal.Decimal
					ItemRefs    []struct{ Qualifier, Value string }
				}
			}
		}
	}
	if err := json.Unmarshal(d.Messages[2].Canonical, &asn); err != nil {
		t.Fatal(err)
	}
	if asn.ShipmentID != "DESP-EU-77" || asn.ShipDate != "20261107" || asn.ShipTime != "0600" || asn.SCAC != "DHLD" || asn.BOL != "BOL-EU-1" ||
		asn.WeightUnit != "KG" || !asn.GrossWeight.Equal(decimal.RequireFromString("96.5")) || asn.ShipTo.GLN != "4006403333338" ||
		len(asn.Orders) != 1 || asn.Orders[0].PONumber != "RET-77" || asn.Orders[0].PODate != "20261101" || len(asn.Orders[0].Packs) != 1 ||
		asn.Orders[0].Packs[0].SSCC != sscc || fmt.Sprint(asn.Orders[0].Packs[0].Items[0].ItemRefs) != "[{EN "+gtin+"} {BP PUMP-200}]" {
		t.Fatalf("canonical ASN %+v", asn)
	}
	var inv struct {
		Number, PONumber, PODate string
		Total                    decimal.Decimal
		Lines                    []struct{ Quantity, UnitPrice decimal.Decimal }
		Taxes                    []struct {
			Type            string
			Amount, Percent decimal.Decimal
		}
		Charges []struct {
			Indicator, Code string
			Amount          decimal.Decimal
		}
	}
	if err := json.Unmarshal(d.Messages[3].Canonical, &inv); err != nil {
		t.Fatal(err)
	}
	if inv.Number != "INV-EU-301" || inv.PONumber != "PO-MFG-77" || inv.PODate != "20261020" || !inv.Total.Equal(decimal.RequireFromString("76.40")) ||
		len(inv.Lines) != 1 || !inv.Lines[0].Quantity.Equal(decimal.NewFromInt(3)) || len(inv.Taxes) != 1 || inv.Taxes[0].Type != "VA" ||
		!inv.Taxes[0].Amount.Equal(decimal.RequireFromString("11.40")) || len(inv.Charges) != 1 || !inv.Charges[0].Amount.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("canonical invoice %+v", inv)
	}
	if n := e.count(`select count(*) from outbox where type = $1 and payload->'data'->>'standard' = 'EDIFACT'`, ediev.MessageReceived); n != 4 {
		t.Fatalf("%d message.received events", n)
	}

	// 2. The same interchange in another file: a duplicate, CONTRL UCI 4 26.
	put("eu-5001-again.edi", interchange)
	dup := fromUs(1)["CONTRL"]
	if len(dup.msgs) != 1 || len(dup.msgs[0].body) != 1 || dup.msgs[0].body[0].String() != "UCI+5001+"+simGLN+":14+PASSAGE:ZZZ+4+26" {
		t.Fatalf("CONTRL for the duplicate: %+v", dup.msgs)
	}

	// 3. Our DESADV, INVOIC and ORDERS, read by the distributor.
	desOut := simOutbound(t, e, "DESADV", "desadv-sim", simASNDocument)
	invOut := simOutbound(t, e, "INVOIC", "invoic-sim", simInvoiceDocument("INV-7002"))
	ordOut := simOutbound(t, e, "ORDERS", "orders-sim", simOrderDocument)
	ours := fromUs(3)
	for typ, d := range ours {
		if d.unb.v(9, 1) != "1" || !strings.HasPrefix(d.msgs[0].typ, typ+":D:96A:UN") {
			t.Fatalf("%s: no CONTRL requested or wrong S009: %v %s", typ, d.unb, d.msgs[0].typ)
		}
	}
	// The distributor reads numbers with the decimal mark our UNA declares.
	num := func(d simEdiDoc, s string) decimal.Decimal {
		mark := "."
		if d.una != "" {
			mark = d.una[2:3]
		}
		return decimal.RequireFromString(strings.Replace(s, mark, ".", 1))
	}
	// DESADV: the packing hierarchy from CPS 7164/7166, SSCCs checked.
	des := ours["DESADV"]
	var shape, ssccs []string
	for _, s := range des.msgs[0].body {
		switch s.tag() {
		case "CPS":
			shape = append(shape, s.v(1, 1)+"<"+s.v(2, 1))
		case "GIN":
			if s.v(1, 1) != "BJ" || !simGS1(s.v(2, 1)) {
				t.Fatalf("GIN %v", s)
			}
			ssccs = append(ssccs, s.v(2, 1))
		}
	}
	var desQty []string
	for _, q := range simEdiFind(des.msgs[0].body, "QTY") {
		desQty = append(desQty, q.v(1, 2))
	}
	if strings.Join(shape, " ") != "1< 2<1 3<1" || strings.Join(ssccs, " ") != "040064030000001015 040064030000001022" ||
		strings.Join(desQty, " ") != "40 200" || simEdiFind(des.msgs[0].body, "CNT")[0].String() != "CNT+2:2" ||
		simEdiFind(des.msgs[0].body, "BGM")[0].String() != "BGM+351+SHIP-88001+9" || simEdiFind(des.msgs[0].body, "RFF")[0].String() != "RFF+ON:PO-88001" ||
		!num(des, simEdiFind(des.msgs[0].body, "MEA")[0].v(3, 2)).Equal(decimal.RequireFromString("412.4")) {
		t.Fatalf("DESADV as read by the distributor: shape %v SSCCs %v QTY %v", shape, ssccs, desQty)
	}
	// INVOIC: the distributor's own total check, lines (MOA 203 = QTY 47 x
	// PRI) + taxes (MOA 124) + charges (ALC C, MOA 23) - allowances.
	ib := ours["INVOIC"].msgs[0].body
	sum, total := decimal.Zero, decimal.Zero
	var qty decimal.Decimal
	for i, s := range ib {
		switch {
		case s.tag() == "QTY" && s.v(1, 1) == "47":
			qty = num(ours["INVOIC"], s.v(1, 2))
		case s.tag() == "PRI":
			if line := num(ours["INVOIC"], ib[i-1].v(1, 2)); ib[i-1].v(1, 1) != "203" || !line.Equal(qty.Mul(num(ours["INVOIC"], s.v(1, 2))).Round(2)) {
				t.Fatalf("line amount %v for QTY %s and %v", ib[i-1], qty, s)
			}
		case s.tag() == "MOA":
			amount := num(ours["INVOIC"], s.v(1, 2))
			switch s.v(1, 1) {
			case "203", "124", "23":
				sum = sum.Add(amount)
			case "204":
				sum = sum.Sub(amount)
			case "86":
				total = amount
			}
		}
	}
	if !total.Equal(sum) || !total.Equal(decimal.RequireFromString("819.92")) || simEdiFind(ib, "BGM")[0].String() != "BGM+380+INV-7002+9" {
		t.Fatalf("INVOIC total %s, the distributor's sum %s", total, sum)
	}
	// ORDERS: lines against CNT, service characters released.
	ob := ours["ORDERS"].msgs[0].body
	if len(simEdiFind(ob, "LIN")) != 2 || simEdiFind(ob, "CNT")[0].String() != "CNT+2:2" || simEdiFind(ob, "BGM")[0].String() != "BGM+220+PO-MFG-501+9" ||
		simEdiFind(ob, "IMD")[0].v(3, 4) != "Seal kit: 40+ pcs, 'heavy'" || simEdiFind(ob, "PIA")[1].String() != "PIA+5+SEAL-1:IN" ||
		simEdiFind(ob, "CUX")[0].String() != "CUX+2:EUR:9" {
		t.Fatalf("ORDERS as read by the distributor: %v", ob)
	}
	// The distributor answers all three in one interchange of CONTRLs: the
	// DESADV and INVOIC accepted, the ORDERS rejected (it does not stock
	// SEAL-1).
	put("eu-5002.edi", simEdifact("5002", false,
		simContrlFor("1", ours["DESADV"], nil), simContrlFor("2", ours["INVOIC"], nil),
		simContrlFor("3", ours["ORDERS"], map[string]string{"1": "12"})))
	var states []string
	for _, id := range []string{desOut.InterchangeID, invOut.InterchangeID, ordOut.InterchangeID} {
		d := e.detail(id)
		states = append(states, d.Messages[0].TransactionSet+":"+d.AckStatus+":"+d.Messages[0].Status)
	}
	if want := "DESADV:ACCEPTED:ACKED INVOIC:ACCEPTED:ACKED ORDERS:REJECTED:FAILED"; strings.Join(states, " ") != want {
		t.Fatalf("outbound states\n got %s\nwant %s", strings.Join(states, " "), want)
	}
	if m := e.detail(ordOut.InterchangeID).Messages[0]; !strings.Contains(m.Error, "UCM 4, error 12") {
		t.Fatalf("ORDERS error %q", m.Error)
	}
	if n := e.count(`select count(*) from outbox where type = $1`, ediev.MessageAcked); n != 3 {
		t.Fatalf("%d message.acked events, want 3", n)
	}
	// CONTRLs are never answered.
	e.dispatch(0)
}
