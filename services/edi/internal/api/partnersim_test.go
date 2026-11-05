package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	ediev "github.com/BenjaminSRussell/passage/services/edi/internal/events"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
)

// Partner simulator (AC-35.1, FR-35.12): the demo distributor's side of the
// EDI exchange. It reads and writes X12 with its own code, sharing nothing with
// internal/domain, so a round trip proves the engine's output is readable by an
// independent implementation and that the engine reads what a partner writes.
// The distributor sends 00501 with | ^ : ~ and CRLF; the engine answers in the
// partner's delimiters and sends its own documents in 00401 with * > ~.

type simSeg []string // [0] is the segment id

func (s simSeg) String() string { return strings.Join(s, "*") }

type simTx struct {
	set, control, gsFID, gsControl string
	body                           []simSeg
}

type simDoc struct {
	isa []string
	el  string
	txs []simTx
}

// simParse reads an interchange the way the distributor's translator does and
// checks every envelope count and control number itself.
func simParse(t *testing.T, raw string) simDoc {
	t.Helper()
	if len(raw) < 106 || raw[:3] != "ISA" {
		t.Fatalf("sim: not an ISA interchange: %q", raw)
	}
	el, comp, term := raw[3:4], raw[104:105], raw[105:106]
	isa := strings.Split(raw[:105], el)
	if len(isa) != 17 || isa[16] != comp {
		t.Fatalf("sim: ISA has %d elements", len(isa))
	}
	var segs []simSeg
	for _, piece := range strings.Split(raw[106:], term) {
		if piece = strings.Trim(piece, "\r\n"); piece != "" {
			segs = append(segs, strings.Split(piece, el))
		}
	}
	d := simDoc{isa: isa, el: el}
	groups := 0
	for i := 0; i < len(segs); i++ {
		switch segs[i][0] {
		case "GS":
			gs, txs := segs[i], 0
			for i++; segs[i][0] != "GE"; i++ {
				st := segs[i]
				tx := simTx{set: st[1], control: st[2], gsFID: gs[1], gsControl: gs[6]}
				for i++; segs[i][0] != "SE"; i++ {
					tx.body = append(tx.body, segs[i])
				}
				if se := segs[i]; se[1] != strconv.Itoa(len(tx.body)+2) || se[2] != tx.control {
					t.Fatalf("sim: SE %v does not close ST %s with %d segments", se, tx.control, len(tx.body)+2)
				}
				d.txs = append(d.txs, tx)
				txs++
			}
			if ge := segs[i]; ge[1] != strconv.Itoa(txs) || ge[2] != gs[6] {
				t.Fatalf("sim: GE %v does not close GS %s with %d sets", ge, gs[6], txs)
			}
			groups++
		case "IEA":
			if segs[i][1] != strconv.Itoa(groups) || segs[i][2] != isa[13] || i != len(segs)-1 {
				t.Fatalf("sim: IEA %v does not close ISA13 %s with %d groups", segs[i], isa[13], groups)
			}
		default:
			t.Fatalf("sim: unexpected %s outside a group", segs[i][0])
		}
	}
	return d
}

type simAnswer struct{ set, control, code string }

// sim997 is the distributor's functional acknowledgment of one of our groups,
// in the distributor's own 00501 delimiters. With no answers it acknowledges
// the group with AK1 and AK9 only.
func sim997(isa13, fid, group string, answers []simAnswer, ak9 string) string {
	body := []string{"AK1|" + fid + "|" + group}
	accepted := 0
	for _, a := range answers {
		body = append(body, "AK2|"+a.set+"|"+a.control, "AK5|"+a.code)
		if a.code != "R" {
			accepted++
		}
	}
	n := len(answers)
	if n == 0 {
		n, accepted = 1, 1
		if ak9 == "R" {
			accepted = 0
		}
	}
	body = append(body, fmt.Sprintf("AK9|%s|%d|%d|%d", ak9, n, n, accepted))
	lines := []string{
		fmt.Sprintf("ISA|00|%10s|00|%10s|ZZ|%-15s|ZZ|%-15s|261022|1015|^|00501|%09s|0|T|:", "", "", "DISTRIBUTOR", "PASSAGE", isa13),
		"GS|FA|DISTRIBUTOR|PASSAGE|20261022|1015|" + isa13 + "|X|005010",
		"ST|997|0001",
	}
	lines = append(lines, body...)
	lines = append(lines, fmt.Sprintf("SE|%d|0001", len(body)+2), "GE|1|"+isa13, fmt.Sprintf("IEA|1|%09s", isa13))
	return strings.Join(lines, "~\r\n") + "~\r\n"
}

// simGS1 checks a GS1 mod-10 check digit (digits at odd distance from the
// check digit weigh 3).
func simGS1(code string) bool {
	sum := 0
	for i := len(code) - 2; i >= 0; i-- {
		d := int(code[i] - '0')
		if (len(code)-2-i)%2 == 0 {
			d *= 3
		}
		sum += d
	}
	return int(code[len(code)-1]-'0') == (10-sum%10)%10
}

func simFind(body []simSeg, id string) []simSeg {
	var out []simSeg
	for _, s := range body {
		if s[0] == id {
			out = append(out, s)
		}
	}
	return out
}

// The distributor's two purchase orders in one group: PO-88001 is clean,
// PO-88002 states CTT 3 for one line (BR-35.1 reject). Segment counts by hand:
// 0001 has 7 body segments (SE 9), 0002 has 3 (SE 5).
var simPurchaseOrders = strings.Join([]string{
	"ISA|00|          |00|          |ZZ|DISTRIBUTOR    |ZZ|MANUFACTURER   |261021|0800|^|00501|000000101|0|T|:",
	"GS|PO|DISTRIBUTOR|MANUFACTURER|20261021|0800|101|X|005010",
	"ST|850|0001",
	"BEG|00|SA|PO-88001||20261021",
	"CUR|BY|EUR",
	"N1|ST|Demo Distributor DC|92|4000001234561",
	"PO1|1|40|EA|12.50||BP|PUMP-200|VP|HP-200",
	"PID|F||||Hydraulic pump",
	"PO1|2|250|EA|0.84||VP|BRK-7",
	"CTT|2",
	"SE|9|0001",
	"ST|850|0002",
	"BEG|00|SA|PO-88002||20261021",
	"PO1|1|5|EA|99.00||BP|SEAL-1",
	"CTT|3",
	"SE|5|0002",
	"GE|2|101",
	"IEA|1|000000101",
}, "~\r\n") + "~\r\n"

// AC-35.1 and the phase-6 flow: the demo distributor and the demo
// manufacturer exchange PO (850) and 997, PO acknowledgment (855), ship notice
// (856) and invoice (810), each answered by the distributor's 997 with A, E,
// R and a group-only A, and a retransmitted 997 is refused as a duplicate.
func TestPartnerSim_DistributorPOToASNToInvoiceRoundTrip(t *testing.T) {
	h, pool := dbHandlerPool(t)
	rec := do(t, h, http.MethodPost, "/edi/v1/partners", distributor, "edi.admin", map[string]string{"Idempotency-Key": "sim"})
	var partner store.Partner
	if err := json.Unmarshal(rec.Body.Bytes(), &partner); err != nil || rec.Code != http.StatusCreated {
		t.Fatalf("partner: %d %s", rec.Code, rec.Body.String())
	}
	receive := func(raw string) receiveResponse {
		t.Helper()
		rec := do(t, h, http.MethodPost, "/edi/v1/inbound/x12?environment=TEST", raw, "edi.operate", nil)
		var got receiveResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusCreated {
			t.Fatalf("receive: %d %s", rec.Code, rec.Body.String())
		}
		return got
	}
	send := func(set, key, document string) simDoc {
		t.Helper()
		rec := do(t, h, http.MethodPost, "/edi/v1/partners/"+partner.ID+"/outbound",
			`{"transactionSet":"`+set+`","document":`+document+`}`, "edi.operate", map[string]string{"Idempotency-Key": key})
		var out store.OutboundResult
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusCreated {
			t.Fatalf("send %s: %d %s", set, rec.Code, rec.Body.String())
		}
		return simParse(t, out.X12)
	}
	// Our envelope as the distributor reads it: from ZZ/PASSAGE, 00401, our next control number.
	checkEnvelope := func(d simDoc, set, fid, control string) simTx {
		t.Helper()
		got := []string{d.el, d.isa[5], strings.TrimSpace(d.isa[6]), d.isa[7], strings.TrimSpace(d.isa[8]), d.isa[12], d.isa[13], d.isa[15]}
		want := []string{"*", "ZZ", "PASSAGE", "ZZ", "DISTRIBUTOR", "00401", fmt.Sprintf("%09s", control), "T"}
		if strings.Join(got, " ") != strings.Join(want, " ") || len(d.txs) != 1 {
			t.Fatalf("%s envelope %v, want %v", set, got, want)
		}
		tx := d.txs[0]
		if tx.set != set || tx.gsFID != fid || tx.gsControl != control || tx.control != "0001" {
			t.Fatalf("%s group: %+v", set, tx)
		}
		return tx
	}

	// 1. 850 in, 997 out in the distributor's delimiters.
	got := receive(simPurchaseOrders)
	if got.AckStatus != "ACCEPTED_WITH_ERRORS" || len(got.Messages) != 2 || got.Messages[0].Status != "MAPPED" || got.Messages[1].Status != "FAILED" ||
		!strings.Contains(got.Messages[1].Error, "EDI-0103") {
		t.Fatalf("850 receipt: %+v", got)
	}
	ack := simParse(t, got.Ack997)
	if ack.el != "|" || ack.isa[11] != "^" || ack.isa[12] != "00501" || strings.TrimSpace(ack.isa[6]) != "MANUFACTURER" ||
		strings.TrimSpace(ack.isa[8]) != "DISTRIBUTOR" || ack.isa[13] != "000000001" || len(ack.txs) != 1 || ack.txs[0].gsFID != "FA" {
		t.Fatalf("997 envelope as the distributor reads it: %v %+v", ack.isa, ack.txs)
	}
	var lines []string
	for _, s := range ack.txs[0].body {
		lines = append(lines, s.String())
	}
	if want := "AK1*PO*101 AK2*850*0001 AK5*A AK2*850*0002 AK5*R AK9*P*2*2*1"; strings.Join(lines, " ") != want {
		t.Fatalf("997 body = %s, want %s", strings.Join(lines, " "), want)
	}
	detail := func(id string) store.InterchangeDetail {
		t.Helper()
		rec := do(t, h, http.MethodGet, "/edi/v1/interchanges/"+id, "", "edi.operate,edi.pii.view", nil)
		var d store.InterchangeDetail
		if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("detail: %d %s", rec.Code, rec.Body.String())
		}
		return d
	}
	var order struct {
		PONumber, Currency, ShipToGLN string
		Lines                         []struct {
			LineNo, Quantity, UOM, UnitPrice, Description string
			ItemRef                                       struct{ Qualifier, Value string }
		}
	}
	if err := json.Unmarshal(detail(got.InterchangeID).Messages[0].Canonical, &order); err != nil {
		t.Fatal(err)
	}
	if order.PONumber != "PO-88001" || order.Currency != "EUR" || order.ShipToGLN != "4000001234561" || len(order.Lines) != 2 ||
		order.Lines[0].Quantity != "40" || order.Lines[0].UnitPrice != "12.5" || order.Lines[0].ItemRef.Value != "PUMP-200" || order.Lines[0].Description != "Hydraulic pump" ||
		order.Lines[1].Quantity != "250" || order.Lines[1].UnitPrice != "0.84" || order.Lines[1].ItemRef.Qualifier != "VP" {
		t.Fatalf("canonical order: %+v", order)
	}

	// 2. 855: line 1 accepted, line 2 changed to 200; the distributor accepts it.
	poAck := send("855", "ack-1", `{"poNumber":"PO-88001","poDate":"20261021","ackDate":"20261022","ackType":"AC","lines":[
		{"lineNo":"1","orderedQty":"40","uom":"EA","status":"IA","ackQty":"40"},
		{"lineNo":"2","orderedQty":"250","uom":"EA","status":"IQ","ackQty":"200"}]}`)
	tx := checkEnvelope(poAck, "855", "PR", "2")
	bak := simFind(tx.body, "BAK")
	acks := simFind(tx.body, "ACK")
	if len(bak) != 1 || bak[0][1] != "00" || bak[0][2] != "AC" || bak[0][3] != "PO-88001" || bak[0][9] != "20261022" ||
		len(acks) != 2 || acks[0].String() != "ACK*IA*40*EA" || acks[1].String() != "ACK*IQ*200*EA" || simFind(tx.body, "CTT")[0].String() != "CTT*2" {
		t.Fatalf("855 as read by the distributor: %v", tx.body)
	}
	if r := receive(sim997("102", "PR", "2", []simAnswer{{"855", "0001", "A"}}, "A")); r.Messages[0].Status != "POSTED" || r.Ack997 != "" {
		t.Fatalf("997 for the 855: %+v", r)
	}

	// 3. 856: one order, two pallets; the distributor accepts with errors.
	asn := send("856", "asn-1", `{"shipmentId":"SHIP-88001","shipDate":"20261023","shipTime":"0700","scac":"DHLD","grossWeight":"412.4","weightUnit":"KG",
		"shipFrom":{"name":"Plant Köln","gln":"4006403333338"},"shipTo":{"name":"Distributor DC","gln":"4000001234561"},
		"orders":[{"poNumber":"PO-88001","poDate":"20261021","packs":[
		{"sscc":"040064030000001015","items":[{"lineNo":"1","quantity":"40","uom":"EA","itemRefs":[{"qualifier":"BP","value":"PUMP-200"}]}]},
		{"sscc":"040064030000001022","items":[{"lineNo":"2","quantity":"200","uom":"EA","itemRefs":[{"qualifier":"VP","value":"BRK-7"}]}]}]}]}`)
	tx = checkEnvelope(asn, "856", "SH", "3")
	// The distributor rebuilds the hierarchy from HL01/HL02/HL03.
	level := map[string]string{}
	var shape []string
	for _, hl := range simFind(tx.body, "HL") {
		level[hl[1]] = hl[3]
		shape = append(shape, hl[3]+"<"+level[hl[2]])
	}
	if strings.Join(shape, " ") != "S< O<S P<O I<P P<O I<P" || simFind(tx.body, "CTT")[0][1] != "6" {
		t.Fatalf("856 hierarchy %v, CTT %v", shape, simFind(tx.body, "CTT"))
	}
	man := simFind(tx.body, "MAN")
	sn1 := simFind(tx.body, "SN1")
	td1 := simFind(tx.body, "TD1")
	if len(man) != 2 || man[0][2] != "040064030000001015" || man[1][2] != "040064030000001022" || !simGS1(man[0][2]) || !simGS1(man[1][2]) ||
		len(sn1) != 2 || sn1[0][2] != "40" || sn1[1][2] != "200" || td1[0][2] != "2" || simFind(tx.body, "PRF")[0][1] != "PO-88001" {
		t.Fatalf("856 content: MAN %v SN1 %v TD1 %v", man, sn1, td1)
	}
	receive(sim997("103", "SH", "3", []simAnswer{{"856", "0001", "E"}}, "E"))

	// 4. 810: 40 x 12.50 + 200 x 0.84 = 668.00, VAT 19% 126.92, freight 25.00,
	// total 819.92. The distributor rejects the first one and accepts the
	// re-issued one with a group-only 997.
	invoice := func(number string) string {
		return `{"number":"` + number + `","date":"20261024","poNumber":"PO-88001","poDate":"20261021","terms":{"discountPct":"0"},
		"remitTo":{"name":"Demo Manufacturer","gln":"4006403333338"},"billTo":{"name":"Demo Distributor","gln":"4000001234561"},"shipTo":{"name":"DC","gln":"4000001234561"},
		"lines":[{"lineNo":"1","quantity":"40","uom":"EA","unitPrice":"12.50","itemRefs":[{"qualifier":"BP","value":"PUMP-200"}]},
		{"lineNo":"2","quantity":"200","uom":"EA","unitPrice":"0.84","itemRefs":[{"qualifier":"VP","value":"BRK-7"}]}],
		"taxes":[{"type":"ST","amount":"126.92","percent":"19"}],"charges":[{"indicator":"C","code":"D240","amount":"25.00"}],"total":"819.92"}`
	}
	for i, number := range []string{"INV-7001", "INV-7001A"} {
		inv := send("810", "inv-"+number, invoice(number))
		control := strconv.Itoa(4 + i)
		tx = checkEnvelope(inv, "810", "IN", control)
		// The distributor's own total check: lines (each rounded to the cent) + TXI + charges - allowances.
		sum := decimal.Zero
		for _, it1 := range simFind(tx.body, "IT1") {
			sum = sum.Add(decimal.RequireFromString(it1[2]).Mul(decimal.RequireFromString(it1[4])).Round(2))
		}
		for _, txi := range simFind(tx.body, "TXI") {
			sum = sum.Add(decimal.RequireFromString(txi[2]))
		}
		for _, sac := range simFind(tx.body, "SAC") {
			amt := decimal.New(int64(mustAtoi(t, sac[5])), -2)
			if sac[1] == "A" {
				amt = amt.Neg()
			}
			sum = sum.Add(amt)
		}
		tds := simFind(tx.body, "TDS")[0][1]
		if tds != "81992" || !decimal.New(int64(mustAtoi(t, tds)), -2).Equal(sum) || simFind(tx.body, "BIG")[0][2] != number {
			t.Fatalf("810 %s: TDS01 %s, distributor's sum %s", number, tds, sum)
		}
		if i == 0 {
			receive(sim997("104", "IN", control, []simAnswer{{"810", "0001", "R"}}, "R"))
		} else {
			receive(sim997("105", "IN", control, nil, "A"))
		}
	}

	// 5. The distributor retransmits its 997 for the 855: same ISA13, refused, nothing changes.
	if rec := do(t, h, http.MethodPost, "/edi/v1/inbound/x12?environment=TEST", sim997("102", "PR", "2", []simAnswer{{"855", "0001", "A"}}, "A"),
		"edi.operate", nil); rec.Code != http.StatusConflict || problemCode(t, rec) != CodeDuplicateControl {
		t.Fatalf("retransmitted 997: %d %s", rec.Code, rec.Body.String())
	}

	rec = do(t, h, http.MethodGet, "/edi/v1/interchanges?direction=OUT", "", "edi.operate", nil)
	var list struct {
		Items []store.InterchangeSummary `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Items) != 4 {
		t.Fatalf("outbound list: %d %s", rec.Code, rec.Body.String())
	}
	var states []string
	for i := len(list.Items) - 1; i >= 0; i-- { // newest first
		it := list.Items[i]
		d := detail(it.ID)
		states = append(states, fmt.Sprintf("%s:%s:%s", it.ControlNumber, d.AckStatus, d.Messages[0].Status))
	}
	if want := "000000002:ACCEPTED:ACKED 000000003:ACCEPTED_WITH_ERRORS:ACKED 000000004:REJECTED:FAILED 000000005:ACCEPTED:ACKED"; strings.Join(states, " ") != want {
		t.Fatalf("outbound states\n got %s\nwant %s", strings.Join(states, " "), want)
	}
	var acked int
	if err := pool.QueryRow(context.Background(), `select count(*) from outbox where type = $1`, ediev.MessageAcked).Scan(&acked); err != nil || acked != 4 {
		t.Fatalf("message.acked events = %d, want 4 (855, 856, both 810s): %v", acked, err)
	}
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("sim: %q is not an integer", s)
	}
	return n
}
