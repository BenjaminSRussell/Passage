package api

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	ediev "github.com/BenjaminSRussell/passage/services/edi/internal/events"
)

// inbound214 is the 214 golden turned around: the carrier (DISTRIBUTOR)
// reports the shipment status to us.
func inbound214(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/x12/214-status.x12")
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Replace(string(raw), "ZZ*PASSAGE        *ZZ*DISTRIBUTOR    ", "ZZ*DISTRIBUTOR    *ZZ*PASSAGE        ", 1)
	return strings.Replace(s, "GS*QM*PASSAGE*DISTRIBUTOR", "GS*QM*DISTRIBUTOR*PASSAGE", 1)
}

// The receipt maps an inbound 820 remittance and 214 shipment status onto
// their canonical documents, as it maps the 850: MAPPED with
// message.received and an accepting 997, or FAILED with message.failed and
// AK5 R when a rule breaks; party names are masked without edi.pii.view.
func TestInbound_820And214AreMappedPublishedAndAcknowledged(t *testing.T) {
	h, pool := dbHandlerPool(t)
	createPartner(t, h, "p", distributor)

	rem := receiveFixture(t, h, "820-inbound-00501.x12")
	if rem.AckStatus != "ACCEPTED" || len(rem.Messages) != 1 || rem.Messages[0].TransactionSet != "820" || rem.Messages[0].Status != "MAPPED" ||
		!strings.Contains(rem.Ack997, "AK1|RA|42") || !strings.Contains(rem.Ack997, "AK2|820|0001") || !strings.Contains(rem.Ack997, "AK9|A|1|1|1") {
		t.Fatalf("820: %+v", rem)
	}
	d := getDetail(t, h, rem.InterchangeID)
	if !strings.Contains(string(d.Messages[0].Canonical), `"traceNumber":"CHK-55120"`) || !strings.Contains(string(d.Messages[0].Canonical), `"name":"Demo Distributor"`) {
		t.Fatalf("820 canonical %s", d.Messages[0].Canonical)
	}
	rec := do(t, h, http.MethodGet, "/edi/v1/messages/"+rem.Messages[0].MessageID, "", "edi.operate", nil)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "Demo Distributor") || !strings.Contains(rec.Body.String(), domain.MaskedValue) {
		t.Fatalf("masked 820: %d %s", rec.Code, rec.Body.String())
	}

	st := do(t, h, http.MethodPost, "/edi/v1/inbound/x12?environment=TEST", inbound214(t), "edi.operate", nil)
	var status receiveResponse
	if err := json.Unmarshal(st.Body.Bytes(), &status); err != nil || st.Code != http.StatusCreated || status.Messages[0].Status != "MAPPED" ||
		!strings.Contains(status.Ack997, "AK1*QM*1~") {
		t.Fatalf("214: %d %s", st.Code, st.Body.String())
	}
	if !strings.Contains(string(getDetail(t, h, status.InterchangeID).Messages[0].Canonical), `"proNumber":"PRO778812"`) {
		t.Fatal("214 canonical")
	}
	if n := countRows(t, pool, `select count(*) from outbox where type = $1 and payload->'data'->>'transaction_set' in ('820', '214')
		and payload->'data'->>'status' = 'MAPPED'`, ediev.MessageReceived); n != 2 {
		t.Fatalf("%d message.received events", n)
	}

	// A remittance whose BPR02 is not the RMR04 total is rejected (AK5 R).
	raw, _ := os.ReadFile("../../testdata/x12/820-inbound-00501.x12")
	bad := strings.ReplaceAll(strings.Replace(string(raw), "BPR|C|250.00|", "BPR|C|260.00|", 1), "000000042", "000000043")
	rec = do(t, h, http.MethodPost, "/edi/v1/inbound/x12?environment=TEST", bad, "edi.operate", nil)
	var rejected receiveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &rejected); err != nil || rec.Code != http.StatusCreated || rejected.AckStatus != "REJECTED" ||
		rejected.Messages[0].Status != "FAILED" || !strings.Contains(rejected.Messages[0].Error, domain.CodeArithmetic) || !strings.Contains(rejected.Ack997, "AK5|R") {
		t.Fatalf("bad 820: %d %s", rec.Code, rec.Body.String())
	}
	if n := countRows(t, pool, `select count(*) from outbox where type = $1 and payload->'data'->>'transaction_set' = '820'`, ediev.MessageFailed); n != 1 {
		t.Fatalf("%d message.failed events", n)
	}

	// The map tester shows the canonical document of a set other than the 850.
	body, _ := json.Marshal(map[string]string{"raw": inbound214(t)})
	rec = do(t, h, http.MethodPost, "/edi/v1/maps:test", string(body), "edi.admin", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"document":{`) || !strings.Contains(rec.Body.String(), `"shipmentId":"SHP-0042"`) ||
		strings.Contains(rec.Body.String(), CodeUnsupportedSet) {
		t.Fatalf("maps:test 214: %d %s", rec.Code, rec.Body.String())
	}
}
