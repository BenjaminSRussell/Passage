package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestBuildEdifactOutbound_TypesAndRules(t *testing.T) {
	asn := `{"shipmentId":"S1","shipDate":"20261021","shipTime":"0930","scac":"FDEG","grossWeight":"8","weightUnit":"KG",
		"shipFrom":{"name":"Plant","gln":"0614141000012"},"shipTo":{"name":"Dock","gln":"0614141000029"},
		"orders":[{"poNumber":"PO1","poDate":"20261021","packs":[{"sscc":"376104250021234569","items":[
		{"lineNo":"1","quantity":"1","uom":"EA","itemRefs":[{"qualifier":"BP","value":"A"}]}]}]}]}`
	m, normalized, err := BuildEdifactOutbound("DESADV", json.RawMessage(asn))
	if err != nil || m.Type != "DESADV" || m.Version != "D" || m.Release != "96A" || !strings.Contains(string(normalized), `"shipmentId":"S1"`) {
		t.Fatalf("%+v %s %v", m, normalized, err)
	}
	ic, err := EdifactOutbound([]EdifactMessage{m}, EdifactParty{ID: "US", Qualifier: "ZZZ"}, EdifactParty{ID: "THEM", Qualifier: "14"}, 7, false,
		time.Date(2026, 11, 5, 9, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := SerializeEdifact(ic, false)
	if err != nil || !strings.HasPrefix(raw, "UNA:+.? 'UNB+UNOC:3+US:ZZZ+THEM:14+261105:0930+7++++1'UNH+1+DESADV:D:96A:UN'") {
		t.Fatalf("%s %v", raw, err)
	}
	// The 856 rules hold: a bad SSCC check digit is refused.
	if _, _, err := BuildEdifactOutbound("DESADV", json.RawMessage(strings.Replace(asn, "4569", "4568", 1))); !errors.Is(err, ErrOutbound) {
		t.Fatalf("bad SSCC: %v", err)
	}
	for _, bad := range []string{"ORDRSP", "856", ""} {
		if _, _, err := BuildEdifactOutbound(bad, json.RawMessage(asn)); !errors.Is(err, ErrOutbound) {
			t.Fatalf("%q: %v", bad, err)
		}
	}
	if _, _, err := BuildEdifactOutbound("INVOIC", json.RawMessage(`{"number":"1","colour":"red"}`)); !errors.Is(err, ErrOutbound) {
		t.Fatalf("unknown field: %v", err)
	}
}

func TestContrlAck_AnswerMessages(t *testing.T) {
	sent := []EdifactMessage{{Ref: "1", Type: "DESADV"}, {Ref: "2", Type: "INVOIC"}}
	status := func(as []ContrlAnswer) string {
		var out []string
		for _, a := range as {
			out = append(out, a.Ref+"="+string(a.Status))
		}
		return strings.Join(out, ",")
	}
	for name, c := range map[string]struct {
		ack   ContrlAck
		want  string
		notes int
	}{
		"UCI 7":                 {ContrlAck{Action: ContrlAcknowledged}, "1=ACCEPTED,2=ACCEPTED", 0},
		"UCI 7 with a UCM 4":    {ContrlAck{Action: ContrlAcknowledged, Messages: []ContrlMessage{{Ref: "2", Type: "INVOIC", Action: "4", ErrorCode: "12"}}}, "1=ACCEPTED,2=REJECTED", 0},
		"UCI 4":                 {ContrlAck{Action: ContrlRejected, ErrorCode: "18"}, "1=REJECTED,2=REJECTED", 0},
		"UCI 8":                 {ContrlAck{Action: ContrlReceived}, "1=ACCEPTED,2=ACCEPTED", 0},
		"UCM for another ref":   {ContrlAck{Action: ContrlAcknowledged, Messages: []ContrlMessage{{Ref: "9", Action: "4"}}}, "1=ACCEPTED,2=ACCEPTED", 1},
		"UCM with another type": {ContrlAck{Action: ContrlAcknowledged, Messages: []ContrlMessage{{Ref: "1", Type: "ORDERS", Action: "4"}}}, "1=REJECTED,2=ACCEPTED", 1},
		"UCM 7 is not a reject": {ContrlAck{Action: ContrlAcknowledged, Messages: []ContrlMessage{{Ref: "1", Action: "7"}}}, "1=ACCEPTED,2=ACCEPTED", 0},
	} {
		got, notes := c.ack.AnswerMessages(sent)
		if status(got) != c.want || len(notes) != c.notes {
			t.Errorf("%s: %s %v", name, status(got), notes)
		}
	}
}
