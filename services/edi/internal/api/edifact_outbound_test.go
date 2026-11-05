package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	ediev "github.com/BenjaminSRussell/passage/services/edi/internal/events"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
	"github.com/BenjaminSRussell/passage/services/edi/internal/transport"
)

func postOutbound(t *testing.T, h http.Handler, partner, set, document, key string, headers map[string]string) (store.OutboundResult, int, string) {
	t.Helper()
	hs := map[string]string{"Idempotency-Key": key}
	for k, v := range headers {
		hs[k] = v
	}
	rec := do(t, h, http.MethodPost, "/edi/v1/partners/"+partner+"/outbound", `{"transactionSet":"`+set+`","document":`+document+`}`, "edi.operate", hs)
	var out store.OutboundResult
	if rec.Code == http.StatusCreated || rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out, rec.Code, ""
	}
	return out, rec.Code, problemCode(t, rec)
}

// partnerContrl is the partner's CONTRL answering our outbound EDIFACT
// interchange: built by the partner from what it received, back to us.
func partnerContrl(t *testing.T, ours string, outcomes []domain.EdifactOutcome, ref string) string {
	t.Helper()
	ic, err := domain.ParseEdifact(ours)
	if err != nil {
		t.Fatalf("our interchange does not parse: %v\n%s", err, ours)
	}
	c, err := domain.BuildCONTRL(ic, outcomes, ref, time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := domain.SerializeEdifact(c, false)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// An EDIFACT DESADV and INVOIC go out on the partner's EDIFACT counter (the
// one its CONTRLs take), from our UNB identity and asking for a CONTRL; the
// partner's CONTRL is matched to them like a 997: accepted marks the message
// ACKED, a UCM 4 marks it FAILED, both publish message.acked, and a second
// CONTRL for the same interchange changes nothing.
func TestOutboundEdifact_DESADVAndINVOICMatchedByThePartnersCONTRL(t *testing.T) {
	h, pool := dbHandlerPool(t)
	p := createPartner(t, h, "eu", edifactPartnerBody("EU distributor", "EUDIST", "4012345000009", ""))
	// The CONTRL for this ORDERS takes EDIFACT reference 1.
	if res, code, pc := postEdifact(t, h, ordersEdifact(t)); code != http.StatusCreated || !strings.Contains(res.Contrl, "UNZ+1+1'") {
		t.Fatalf("ORDERS: %d %s %s", code, pc, res.Contrl)
	}

	desadv, code, pc := postOutbound(t, h, p.ID, "DESADV", asnDocument, "desadv-1", nil)
	if code != http.StatusCreated || desadv.Standard != "EDIFACT" || desadv.ControlNumber != "2" || desadv.X12 != "" || desadv.TransactionSet != "DESADV" {
		t.Fatalf("DESADV: %d %s %+v", code, pc, desadv)
	}
	sent, err := domain.ParseEdifact(desadv.Edifact)
	if err != nil {
		t.Fatal(err)
	}
	if sent.Sender != DefaultEdifactSender || sent.Recipient != (domain.EdifactParty{ID: "4012345000009", Qualifier: "14"}) || sent.Control != "2" ||
		!sent.AckRequest || !sent.Test || len(sent.Messages) != 1 || sent.Messages[0].Type != "DESADV" || sent.Messages[0].Ref != "1" {
		t.Fatalf("envelope %+v", sent)
	}
	// A replay returns the same interchange and takes no number.
	if again, code, _ := postOutbound(t, h, p.ID, "DESADV", asnDocument, "desadv-1", nil); code != http.StatusOK || again.InterchangeID != desadv.InterchangeID {
		t.Fatalf("replay: %d %+v", code, again)
	}
	invoic, code, pc := postOutbound(t, h, p.ID, "INVOIC", invoiceDocument, "invoic-1", nil)
	if code != http.StatusCreated || invoic.ControlNumber != "3" || !strings.Contains(invoic.Edifact, "UNH+1+INVOIC:D:96A:UN'BGM+380+") {
		t.Fatalf("INVOIC: %d %s %+v", code, pc, invoic)
	}
	d := getDetail(t, h, desadv.InterchangeID)
	if d.Direction != "OUT" || d.Standard != "EDIFACT" || d.Version != "UNOC:3" || d.ControlNumber != "2" || d.AckStatus != "PENDING" ||
		d.Messages[0].STControl != "1" || d.Messages[0].Status != "MAPPED" {
		t.Fatalf("detail %+v", d)
	}

	// The partner accepts the DESADV.
	res, code, pc := postEdifact(t, h, partnerContrl(t, desadv.Edifact, []domain.EdifactOutcome{{Ref: "1", Accepted: true}}, "900"))
	if code != http.StatusCreated || res.AckStatus != "ACCEPTED" || res.Contrl != "" || res.Messages[0].TransactionSet != "CONTRL" || res.Messages[0].Status != "POSTED" {
		t.Fatalf("CONTRL for the DESADV: %d %s %+v", code, pc, res)
	}
	d = getDetail(t, h, desadv.InterchangeID)
	if d.AckStatus != "ACCEPTED" || d.Messages[0].Status != "ACKED" {
		t.Fatalf("DESADV after its CONTRL: %+v", d)
	}
	// It rejects the INVOIC with a UCM 4.
	res, code, pc = postEdifact(t, h, partnerContrl(t, invoic.Edifact, []domain.EdifactOutcome{{Ref: "1", ErrorCode: domain.ContrlErrInvalidValue, SegmentPosition: 3}}, "901"))
	if code != http.StatusCreated || res.AckStatus != "ACCEPTED" || res.Messages[0].Status != "POSTED" {
		t.Fatalf("CONTRL for the INVOIC: %d %s %+v", code, pc, res)
	}
	d = getDetail(t, h, invoic.InterchangeID)
	if d.AckStatus != "REJECTED" || d.Messages[0].Status != "FAILED" || !strings.Contains(d.Messages[0].Error, "UCM 4, error 12") {
		t.Fatalf("INVOIC after its CONTRL: %+v", d)
	}
	var acked int
	if err := pool.QueryRow(context.Background(), `select count(*) from outbox where type = $1 and payload->'data'->>'interchange_id' in ($2, $3)
		and (payload->'data'->>'ack_status' = 'ACCEPTED' and payload->'data'->>'transaction_set' = 'DESADV'
		  or payload->'data'->>'ack_status' = 'REJECTED' and payload->'data'->>'transaction_set' = 'INVOIC')`,
		ediev.MessageAcked, desadv.InterchangeID, invoic.InterchangeID).Scan(&acked); err != nil || acked != 2 {
		t.Fatalf("%d message.acked events: %v", acked, err)
	}
	// A second CONTRL for the DESADV changes nothing and says so.
	res, _, _ = postEdifact(t, h, partnerContrl(t, desadv.Edifact, []domain.EdifactOutcome{{Ref: "1", ErrorCode: "12"}}, "902"))
	if res.Messages[0].Status != "POSTED" || !strings.Contains(res.Messages[0].Error, "already acknowledged") || getDetail(t, h, desadv.InterchangeID).Messages[0].Status != "ACKED" {
		t.Fatalf("second CONTRL: %+v", res)
	}
	// A CONTRL for an interchange we never sent is FAILED with EDI-0161.
	stray := strings.Replace(partnerContrl(t, desadv.Edifact, []domain.EdifactOutcome{{Ref: "1", Accepted: true}}, "903"), "UCI+2+", "UCI+77+", 1)
	if res, _, _ = postEdifact(t, h, stray); res.Messages[0].Status != "FAILED" || !strings.HasPrefix(res.Messages[0].Error, domain.CodeUnmatchedAck) ||
		res.AckStatus != "ACCEPTED_WITH_ERRORS" {
		t.Fatalf("stray CONTRL: %+v", res)
	}

	// Refusals: a partner without an EDIFACT identity, a document the 856
	// rules refuse, and a message type we do not send.
	x12Only := createPartner(t, h, "x12", `{"name":"X12 only","isaQualifier":"ZZ","isaId":"X12ONLY","environment":"TEST"}`)
	if _, code, pc := postOutbound(t, h, x12Only.ID, "DESADV", asnDocument, "desadv-2", nil); code != http.StatusConflict || pc != CodeNoEdifactID {
		t.Fatalf("no EDIFACT identity: %d %s", code, pc)
	}
	if _, code, pc := postOutbound(t, h, p.ID, "DESADV", strings.Replace(asnDocument, "376104250021234569", "376104250021234568", 1), "desadv-3", nil); code != http.StatusUnprocessableEntity || pc != CodeOutbound {
		t.Fatalf("bad SSCC: %d %s", code, pc)
	}
	if _, code, pc := postOutbound(t, h, p.ID, "ORDRSP", asnDocument, "ordrsp", nil); code != http.StatusBadRequest || pc != CodeValidation {
		t.Fatalf("ORDRSP: %d %s", code, pc)
	}
	// A refused document took no number: the next one is 4.
	if orders, code, pc := postOutbound(t, h, p.ID, "ORDERS", orderDocument, "orders-1", nil); code != http.StatusCreated || orders.ControlNumber != "4" ||
		!strings.Contains(orders.Edifact, "UNH+1+ORDERS:D:96A:UN'BGM+220+PO-OUT-1+9'") {
		t.Fatalf("ORDERS: %d %s %+v", code, pc, orders)
	}
}

const invoiceDocument = `{"number":"INV-1","date":"20261022","poNumber":"PO1234","poDate":"20261021",
"remitTo":{"name":"Seller","gln":"0614141000012"},"billTo":{"name":"Buyer","gln":"0614141000029"},"shipTo":{"name":"Dock","gln":"0614141000036"},
"lines":[{"lineNo":"1","quantity":"10","uom":"EA","unitPrice":"2.50","itemRefs":[{"qualifier":"BP","value":"ABC-1"}]}],"total":"25.00"}`

const orderDocument = `{"purpose":"00","orderType":"SA","poNumber":"PO-OUT-1","poDate":"20261105","shipToName":"Plant","shipToGln":"0614141000012",
"lines":[{"lineNo":"1","quantity":"5","uom":"EA","unitPrice":"3","itemRef":{"qualifier":"VP","value":"RAW-9"}}]}`

func getDetail(t *testing.T, h http.Handler, id string) store.InterchangeDetail {
	t.Helper()
	rec := do(t, h, http.MethodGet, "/edi/v1/interchanges/"+id, "", "edi.operate,edi.pii.view", nil)
	var d store.InterchangeDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("detail %d %s", rec.Code, rec.Body.String())
	}
	return d
}

// An outbound DESADV to a partner with an AS2 transport is queued, sent as
// application/edifact in a .edi delivery, becomes SENT on the MDN, and the
// partner's CONTRL coming back over AS2 marks it ACKED.
func TestTransportAS2_OutboundEdifactSentAndAnsweredByCONTRL(t *testing.T) {
	e := newTransportEnv(t)
	e.putProfile()
	e.setEdifact(domain.ContrlRequested)
	v := e.mustPutTransport(e.as2Config(nil))
	out, code, pc := postOutbound(t, e.h, e.partner, "DESADV", asnDocument, "desadv-as2", map[string]string{tenant.HeaderLegalEntity: srcLE})
	if code != http.StatusCreated || out.Delivery == nil || out.ControlNumber != "1" {
		t.Fatalf("DESADV: %d %s %+v", code, pc, out)
	}
	if n := e.count(`select count(*) from transport_delivery where payload_kind = 'DOCUMENT' and filename like 'DESADV\_1\_%.edi'`); n != 1 {
		t.Fatal("delivery file name")
	}
	e.dispatch(1)
	got := e.peer.messages()
	if len(got) != 1 || got[0].ContentType != transport.ContentEdifact || string(got[0].Payload) != out.Edifact {
		t.Fatalf("partner got %+v", got)
	}
	if d := e.detail(out.InterchangeID); d.Messages[0].Status != "SENT" {
		t.Fatalf("after the MDN: %+v", d)
	}
	c := e.partnerClient(v.AS2Inbound.URL, e.them)
	contrl := partnerContrl(t, out.Edifact, []domain.EdifactOutcome{{Ref: "1", Accepted: true}}, "70")
	if r, err := c.Send(context.Background(), transport.Message{ID: "contrl-70@distributor", ContentType: transport.ContentEdifact, Payload: []byte(contrl)}); err != nil ||
		!strings.HasSuffix(r.Disposition, "; processed") {
		t.Fatalf("CONTRL over AS2: %+v %v", r, err)
	}
	if d := e.detail(out.InterchangeID); d.AckStatus != "ACCEPTED" || d.Messages[0].Status != "ACKED" {
		t.Fatalf("after the CONTRL: %+v", d)
	}
	// A CONTRL is never answered.
	e.dispatch(0)
}
