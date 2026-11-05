package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	ediev "github.com/BenjaminSRussell/passage/services/edi/internal/events"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
	"github.com/BenjaminSRussell/passage/services/edi/migrations"
)

// dbHandler returns the routes over a migrated throwaway schema.
func dbHandler(t *testing.T) http.Handler {
	t.Helper()
	h, _ := dbHandlerPool(t)
	return h
}

// dbHandlerPool also returns the pool so a test can read the outbox.
func dbHandlerPool(t *testing.T) (http.Handler, *pgxpool.Pool) {
	t.Helper()
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	a := New(store.New(pool, 30))
	a.AllowBodyOutbound = true
	return a.Routes(), pool
}

func do(t *testing.T, h http.Handler, method, path, body, duties string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(tenant.HeaderTenant, testkit.TenantID(7))
	req.Header.Set(tenant.HeaderDuties, duties)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const distributor = `{"name":"Demo distributor","isaQualifier":"ZZ","isaId":"DISTRIBUTOR","environment":"TEST"}`

func TestPartnerAPI_CreateReplayReuseAndRead(t *testing.T) {
	h := dbHandler(t)
	key := map[string]string{"Idempotency-Key": "partner-1"}
	rec := do(t, h, http.MethodPost, "/edi/v1/partners", distributor, "edi.admin", key)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var created store.Partner
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.AckSLAMinutes != 60 || created.Version != 1 {
		t.Fatalf("defaults: %+v", created)
	}

	// Same key, same body with different spacing: replay with 200 and the same partner.
	rec = do(t, h, http.MethodPost, "/edi/v1/partners", strings.ReplaceAll(distributor, ",", ", "), "edi.admin", key)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), created.ID) {
		t.Fatalf("replay: %d %s", rec.Code, rec.Body.String())
	}
	// Same key, different body: 422 idempotency_key_reuse.
	rec = do(t, h, http.MethodPost, "/edi/v1/partners", strings.Replace(distributor, "Demo distributor", "Renamed", 1), "edi.admin", key)
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != CodeIdempotencyReuse {
		t.Fatalf("reuse: %d %s", rec.Code, rec.Body.String())
	}
	// New key, same ISA identity: 409.
	rec = do(t, h, http.MethodPost, "/edi/v1/partners", distributor, "edi.admin", map[string]string{"Idempotency-Key": "partner-2"})
	if rec.Code != http.StatusConflict || problemCode(t, rec) != CodePartnerExists {
		t.Fatalf("duplicate identity: %d %s", rec.Code, rec.Body.String())
	}

	rec = do(t, h, http.MethodGet, "/edi/v1/partners/"+created.ID, "", "edi.admin", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("get: %d %s etag=%q", rec.Code, rec.Body.String(), rec.Header().Get("ETag"))
	}
	rec = do(t, h, http.MethodGet, "/edi/v1/partners/not-a-uuid", "", "edi.admin", nil)
	if rec.Code != http.StatusNotFound || problemCode(t, rec) != CodeNotFound {
		t.Fatalf("bad id: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, "/edi/v1/partners?environment=TEST", "", "edi.admin", nil)
	var list struct {
		Items []store.Partner `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Items) != 1 || list.Items[0].ID != created.ID {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
}

func TestInboundAPI_ReceiveAcknowledgeAndRejectDuplicate(t *testing.T) {
	h := dbHandler(t)
	raw := sample850(t, "1") // sender ZZ/DISTRIBUTOR, usage T

	rec := do(t, h, http.MethodPost, "/edi/v1/inbound/x12?environment=TEST", raw, "edi.operate", nil)
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != CodeUnknownSender {
		t.Fatalf("unknown sender: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPost, "/edi/v1/partners", distributor, "edi.admin", map[string]string{"Idempotency-Key": "p"}); rec.Code != http.StatusCreated {
		t.Fatalf("create partner: %d %s", rec.Code, rec.Body.String())
	}
	// The partner is in TEST; the default PROD environment does not resolve it.
	if rec := do(t, h, http.MethodPost, "/edi/v1/inbound/x12", raw, "edi.operate", nil); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("PROD lookup must not find a TEST partner: %d %s", rec.Code, rec.Body.String())
	}

	rec = do(t, h, http.MethodPost, "/edi/v1/inbound/x12?environment=TEST", raw, "edi.operate", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("receive: %d %s", rec.Code, rec.Body.String())
	}
	var got receiveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.AckStatus != "ACCEPTED" || len(got.Messages) != 1 || got.Messages[0].Status != "MAPPED" {
		t.Fatalf("receive response: %+v", got)
	}
	ack, err := domain.Parse(got.Ack997)
	if err != nil {
		t.Fatalf("997 does not parse: %v\n%s", err, got.Ack997)
	}
	if ack.Sender.ID != "MANUFACTURER" || ack.Receiver.ID != "DISTRIBUTOR" || ack.Control != "000000001" {
		t.Fatalf("997 envelope: %+v", ack)
	}
	ak9 := ack.Groups[0].Transactions[0].Body[3]
	if ak9.ID != "AK9" || ak9.Elements[0] != domain.AckAccepted {
		t.Fatalf("AK9 = %+v", ak9)
	}

	// AC-35.5: the same ISA13 again is refused and no second interchange appears.
	rec = do(t, h, http.MethodPost, "/edi/v1/inbound/x12?environment=TEST", raw, "edi.operate", nil)
	if rec.Code != http.StatusConflict || problemCode(t, rec) != CodeDuplicateControl {
		t.Fatalf("duplicate: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, "/edi/v1/interchanges", "", "edi.operate", nil)
	var list struct {
		Items []store.InterchangeSummary `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Items) != 1 {
		t.Fatalf("interchanges: %d %s", rec.Code, rec.Body.String())
	}
	if list.Items[0].PartnerName != "Demo distributor" || list.Items[0].MessageCount != 1 || list.Items[0].ControlNumber != "000000001" {
		t.Fatalf("interchange summary: %+v", list.Items[0])
	}

	rec = do(t, h, http.MethodGet, "/edi/v1/interchanges/"+got.InterchangeID, "", "edi.operate", nil)
	var detail store.InterchangeDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("detail: %d %s", rec.Code, rec.Body.String())
	}
	if detail.AckPayload != got.Ack997 || len(detail.Messages) != 1 || !strings.Contains(string(detail.Messages[0].Canonical), "PO1234") {
		t.Fatalf("detail = %+v", detail)
	}
	if strings.Contains(rec.Body.String(), "BEG*00*SA*PO1234") {
		t.Fatal("the raw inbound payload must not be returned (BR-35.12 masking belongs to the monitor)")
	}
}

func TestInterchangeAPI_ValidatesFiltersAndDuty(t *testing.T) {
	h := dbHandler(t)
	rec := do(t, h, http.MethodGet, "/edi/v1/interchanges?ackStatus=LOST&limit=500&partnerId=x", "", "edi.operate", nil)
	if rec.Code != http.StatusBadRequest || problemCode(t, rec) != CodeValidation {
		t.Fatalf("filters: %d %s", rec.Code, rec.Body.String())
	}
	for _, field := range []string{"ackStatus", "limit", "partnerId"} {
		if !strings.Contains(rec.Body.String(), `"`+field+`"`) {
			t.Errorf("missing field error %s: %s", field, rec.Body.String())
		}
	}
	if rec := do(t, h, http.MethodGet, "/edi/v1/interchanges", "", "edi.admin", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("edi.admin alone must not read the monitor: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/edi/v1/interchanges/0195f0a0-0000-7000-8000-0000000000aa", "", "edi.operate", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown interchange: %d %s", rec.Code, rec.Body.String())
	}
}

const asnDocument = `{"shipmentId":"LOAD42-SHIP7","shipDate":"20261021","shipTime":"0930","scac":"FDEG",
"grossWeight":"812.5","weightUnit":"KG","shipFrom":{"name":"Plant","gln":"0614141000012"},"shipTo":{"name":"Dock","gln":"0614141000029"},
"orders":[{"poNumber":"PO1234","poDate":"20261021","packs":[{"sscc":"376104250021234569","items":[
{"lineNo":"1","quantity":"10","uom":"EA","itemRefs":[{"qualifier":"BP","value":"ABC-1"}]}]}]}]}`

// AC-35.1 (one leg): an 850 comes in, an 856 goes out on the same gap-free
// control sequence as the 997, and the partner's 997 for it marks the 856
// ACKED with a message.acked event in the outbox.
func TestOutboundAPI_GenerateReplayAndMatchPartner997(t *testing.T) {
	h, pool := dbHandlerPool(t)
	rec := do(t, h, http.MethodPost, "/edi/v1/partners", distributor, "edi.admin", map[string]string{"Idempotency-Key": "p"})
	var partner store.Partner
	if err := json.Unmarshal(rec.Body.Bytes(), &partner); err != nil || rec.Code != http.StatusCreated {
		t.Fatalf("create partner: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPost, "/edi/v1/inbound/x12?environment=TEST", sample850(t, "1"), "edi.operate", nil); rec.Code != http.StatusCreated {
		t.Fatalf("receive 850: %d %s", rec.Code, rec.Body.String())
	}

	path := "/edi/v1/partners/" + partner.ID + "/outbound"
	body := `{"transactionSet":"856","document":` + asnDocument + `}`
	key := map[string]string{"Idempotency-Key": "asn-1"}
	rec = do(t, h, http.MethodPost, path, body, "edi.operate", key)
	if rec.Code != http.StatusCreated {
		t.Fatalf("outbound: %d %s", rec.Code, rec.Body.String())
	}
	var out store.OutboundResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	// The 997 for the 850 took 1; the 856 takes 2 (BR-35.5, gap-free per partner).
	if out.ControlNumber != "000000002" || out.TransactionSet != "856" {
		t.Fatalf("outbound result: %+v", out)
	}
	sent, err := domain.Parse(out.X12)
	if err != nil {
		t.Fatalf("outbound X12 does not parse: %v\n%s", err, out.X12)
	}
	if sent.Sender != DefaultSender || sent.Receiver.ID != "DISTRIBUTOR" || sent.Usage != "T" || sent.Groups[0].FunctionalID != "SH" || sent.Groups[0].Control != "2" {
		t.Fatalf("outbound envelope: %+v", sent)
	}

	// Replay returns the same interchange; another document under the key is refused.
	rec = do(t, h, http.MethodPost, path, strings.ReplaceAll(body, ",", ", "), "edi.operate", key)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), out.InterchangeID) {
		t.Fatalf("replay: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, path, strings.Replace(body, "LOAD42-SHIP7", "LOAD43", 1), "edi.operate", key)
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != CodeIdempotencyReuse {
		t.Fatalf("reuse: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPost, "/edi/v1/partners/0195f0a0-0000-7000-8000-0000000000aa/outbound", body, "edi.operate",
		map[string]string{"Idempotency-Key": "asn-2"}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown partner: %d %s", rec.Code, rec.Body.String())
	}

	rec = do(t, h, http.MethodGet, "/edi/v1/interchanges?direction=OUT", "", "edi.operate", nil)
	var list struct {
		Items []store.InterchangeSummary `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Items) != 1 || list.Items[0].AckStatus != "PENDING" || list.Items[0].Direction != "OUT" {
		t.Fatalf("outbound list: %d %s", rec.Code, rec.Body.String())
	}

	// The partner answers group 2 with a 997 accepting the 856.
	partner997, err := domain.Ack997(sent, sent.Groups[0], []domain.TxOutcome{{SetID: "856", Control: "0001", Code: domain.AckAccepted}},
		"000000077", "77", time.Date(2026, 10, 21, 13, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	raw997, err := domain.Serialize(partner997)
	if err != nil {
		t.Fatal(err)
	}
	rec = do(t, h, http.MethodPost, "/edi/v1/inbound/x12?environment=TEST", raw997, "edi.operate", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("receive 997: %d %s", rec.Code, rec.Body.String())
	}
	var got receiveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.AckStatus != "ACCEPTED" || got.Ack997 != "" || len(got.Messages) != 1 || got.Messages[0].Status != "POSTED" {
		t.Fatalf("a 997 is matched and never acknowledged: %+v", got)
	}

	rec = do(t, h, http.MethodGet, "/edi/v1/interchanges/"+out.InterchangeID, "", "edi.operate", nil)
	var detail store.InterchangeDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.AckStatus != "ACCEPTED" || len(detail.Messages) != 1 || detail.Messages[0].Status != "ACKED" || !strings.Contains(string(detail.Messages[0].Canonical), "376104250021234569") {
		t.Fatalf("outbound detail after 997: %+v", detail)
	}
	var payload string
	if err := pool.QueryRow(context.Background(), `select payload::text from outbox where type = $1`, ediev.MessageAcked).Scan(&payload); err != nil {
		t.Fatalf("message.acked event: %v", err)
	}
	if !strings.Contains(payload, out.MessageID) || !strings.Contains(payload, `"ack_status": "ACCEPTED"`) && !strings.Contains(payload, `"ack_status":"ACCEPTED"`) {
		t.Fatalf("message.acked payload: %s", payload)
	}

	// A 997 for a group we never sent is recorded as FAILED with EDI-0161.
	stray, _ := domain.Ack997(sent, domain.Group{FunctionalID: "SH", Control: "99", Version: "004010"},
		[]domain.TxOutcome{{SetID: "856", Control: "0001", Code: domain.AckAccepted}}, "000000078", "78", time.Now())
	rawStray, _ := domain.Serialize(stray)
	rec = do(t, h, http.MethodPost, "/edi/v1/inbound/x12?environment=TEST", rawStray, "edi.operate", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusCreated {
		t.Fatalf("stray 997: %d %s", rec.Code, rec.Body.String())
	}
	if got.AckStatus != "ACCEPTED_WITH_ERRORS" || got.Messages[0].Status != "FAILED" || !strings.Contains(got.Messages[0].Error, domain.CodeUnmatchedAck) {
		t.Fatalf("stray 997: %+v", got)
	}
}

// FR-35.10: a failed inbound 850 shows in the monitor, a dry run and then a
// reprocess map it again from the stored raw payload, and the SLA dashboard
// measures the partner's 997 latency on our 856.
func TestMonitorAPI_ReprocessAndSLA(t *testing.T) {
	h, pool := dbHandlerPool(t)
	rec := do(t, h, http.MethodPost, "/edi/v1/partners", distributor, "edi.admin", map[string]string{"Idempotency-Key": "p"})
	var partner store.Partner
	if err := json.Unmarshal(rec.Body.Bytes(), &partner); err != nil || rec.Code != http.StatusCreated {
		t.Fatalf("create partner: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, "/edi/v1/inbound/x12?environment=TEST", sample850(t, "1"), "edi.operate", nil)
	var received receiveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &received); err != nil || rec.Code != http.StatusCreated {
		t.Fatalf("receive: %d %s", rec.Code, rec.Body.String())
	}
	msgID := received.Messages[0].MessageID
	// Stand-in for a map that failed at receipt and has since been fixed.
	if _, err := pool.Exec(context.Background(), `update message set status = 'FAILED', error = 'EDI-0105: no item cross-reference', canonical = null where id = $1`, msgID); err != nil {
		t.Fatal(err)
	}

	rec = do(t, h, http.MethodGet, "/edi/v1/messages?status=FAILED&direction=IN&transactionSet=850&controlNumber=1", "", "edi.operate", nil)
	var page struct {
		Items      []store.MessageRecord `json:"items"`
		NextCursor string                `json:"nextCursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || rec.Code != http.StatusOK || len(page.Items) != 1 || page.Items[0].ID != msgID {
		t.Fatalf("monitor list: %d %s", rec.Code, rec.Body.String())
	}
	if page.NextCursor != "" || page.Items[0].PartnerName != "Demo distributor" || page.Items[0].InterchangeID != received.InterchangeID {
		t.Fatalf("monitor row: %+v cursor %q", page.Items[0], page.NextCursor)
	}

	path := "/edi/v1/interchanges/" + received.InterchangeID + ":reprocess"
	rec = do(t, h, http.MethodPost, path+"?dryRun=true", "", "edi.operate", nil)
	var res store.ReprocessResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || rec.Code != http.StatusOK || !res.DryRun || res.Changed != 1 || res.Messages[0].Status != "MAPPED" {
		t.Fatalf("dry run: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, "/edi/v1/messages/"+msgID, "", "edi.operate", nil)
	if !strings.Contains(rec.Body.String(), `"status":"FAILED"`) {
		t.Fatalf("a dry run must not write: %s", rec.Body.String())
	}

	rec = do(t, h, http.MethodPost, path, "", "edi.operate", map[string]string{"Idempotency-Key": "rp-1"})
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || rec.Code != http.StatusOK || res.DryRun || res.Changed != 1 {
		t.Fatalf("reprocess: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, "/edi/v1/messages/"+msgID, "", "edi.operate", nil)
	var m store.MessageRecord
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil || m.Status != "MAPPED" || m.Error != "" || !strings.Contains(string(m.Canonical), "PO1234") {
		t.Fatalf("message after reprocess: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "BEG*00") {
		t.Fatal("the raw payload must not be returned (BR-35.12)")
	}
	var mapped int
	if err := pool.QueryRow(context.Background(), `select count(*) from outbox where type = $1 and aggregate_id = $2 and payload->'data'->>'status' = 'MAPPED'`,
		ediev.MessageReceived, msgID).Scan(&mapped); err != nil || mapped != 2 {
		t.Fatalf("message.received MAPPED events for the message = %d (receipt and reprocess), err=%v", mapped, err)
	}
	if rec := do(t, h, http.MethodGet, "/edi/v1/messages/0195f0a0-0000-7000-8000-0000000000aa", "", "edi.operate", nil); rec.Code != http.StatusNotFound || problemCode(t, rec) != CodeNotFound {
		t.Fatalf("unknown message: %d %s", rec.Code, rec.Body.String())
	}

	// An outbound 856, answered by the partner's 997.
	rec = do(t, h, http.MethodPost, "/edi/v1/partners/"+partner.ID+"/outbound", `{"transactionSet":"856","document":`+asnDocument+`}`, "edi.operate",
		map[string]string{"Idempotency-Key": "asn-1"})
	var out store.OutboundResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusCreated {
		t.Fatalf("outbound: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPost, "/edi/v1/interchanges/"+out.InterchangeID+":reprocess", "", "edi.operate",
		map[string]string{"Idempotency-Key": "rp-2"}); rec.Code != http.StatusConflict || problemCode(t, rec) != CodeNotInbound {
		t.Fatalf("outbound reprocess: %d %s", rec.Code, rec.Body.String())
	}
	sent, err := domain.Parse(out.X12)
	if err != nil {
		t.Fatal(err)
	}
	partner997, err := domain.Ack997(sent, sent.Groups[0], []domain.TxOutcome{{SetID: "856", Control: "0001", Code: domain.AckAccepted}},
		"000000077", "77", time.Date(2026, 10, 21, 13, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	raw997, err := domain.Serialize(partner997)
	if err != nil {
		t.Fatal(err)
	}
	if rec := do(t, h, http.MethodPost, "/edi/v1/inbound/x12?environment=TEST", raw997, "edi.operate", nil); rec.Code != http.StatusCreated {
		t.Fatalf("receive 997: %d %s", rec.Code, rec.Body.String())
	}

	rec = do(t, h, http.MethodGet, "/edi/v1/partners/sla?days=7", "", "edi.operate", nil)
	var sla struct {
		Days  int      `json:"days"`
		Items []slaRow `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &sla); err != nil || rec.Code != http.StatusOK || sla.Days != 7 || len(sla.Items) != 1 {
		t.Fatalf("sla: %d %s", rec.Code, rec.Body.String())
	}
	// 850 + 856 + the partner's 997; nothing failed now; the 856 was acked within the SLA.
	if r := sla.Items[0]; r.PartnerID != partner.ID || r.Messages != 3 || r.Failed != 0 || r.FailureRatePct != 0 ||
		r.Outbound != 1 || r.Acked != 1 || r.LateAcks != 0 || r.OverdueAcks != 0 || r.P95AckMinutes == nil || *r.P95AckMinutes > 1 {
		t.Fatalf("sla row: %s", rec.Body.String())
	}
}

// receiveFixture posts one testdata/x12 interchange and returns the receipt.
func receiveFixture(t *testing.T, h http.Handler, name string) receiveResponse {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/x12/" + name)
	if err != nil {
		t.Fatal(err)
	}
	rec := do(t, h, http.MethodPost, "/edi/v1/inbound/x12?environment=TEST", string(raw), "edi.operate", nil)
	var out receiveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusCreated {
		t.Fatalf("receive %s: %d %s", name, rec.Code, rec.Body.String())
	}
	return out
}

// BR-35.12: the raw viewer and the canonical documents hide party names,
// addresses and contacts from operators without edi.pii.view.
func TestMonitorAPI_PayloadTreeAndPIIMasking(t *testing.T) {
	h := dbHandler(t)
	if rec := do(t, h, http.MethodPost, "/edi/v1/partners", distributor, "edi.admin", map[string]string{"Idempotency-Key": "p"}); rec.Code != http.StatusCreated {
		t.Fatalf("create partner: %d %s", rec.Code, rec.Body.String())
	}
	got := receiveFixture(t, h, "850-basic.x12")
	if got.Messages[0].Status != "MAPPED" {
		t.Fatalf("fixture 850 must map: %+v", got)
	}

	var view payloadView
	rec := do(t, h, http.MethodGet, "/edi/v1/interchanges/"+got.InterchangeID+"/payload", "", "edi.operate", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("payload: %d %s", rec.Code, rec.Body.String())
	}
	for _, secret := range []string{"ACME DC", "100 MAIN ST", "ANYTOWN", "JANE BUYER", "5551234"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("masked payload leaks %q: %s", secret, rec.Body.String())
		}
	}
	if !view.PIIMasked || view.Direction != "IN" || view.Tree.Name != "ISA" || !strings.Contains(rec.Body.String(), "0012345000007") {
		t.Fatalf("masked view: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "BEG*00") {
		t.Fatal("the raw text must never be returned")
	}
	rec = do(t, h, http.MethodGet, "/edi/v1/interchanges/"+got.InterchangeID+"/payload", "", "edi.operate,edi.pii.view", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil || view.PIIMasked || !strings.Contains(rec.Body.String(), "JANE BUYER") {
		t.Fatalf("unmasked view: %d %s", rec.Code, rec.Body.String())
	}

	// The canonical order on the message and on the interchange detail.
	msg := "/edi/v1/messages/" + got.Messages[0].MessageID
	rec = do(t, h, http.MethodGet, msg, "", "edi.operate", nil)
	if strings.Contains(rec.Body.String(), "ACME DC") || !strings.Contains(rec.Body.String(), `"shipToName":"***"`) ||
		!strings.Contains(rec.Body.String(), `"piiMasked":true`) || !strings.Contains(rec.Body.String(), "PO-88001") {
		t.Fatalf("masked message: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, msg, "", "edi.operate,edi.pii.view", nil)
	if !strings.Contains(rec.Body.String(), `"shipToName":"ACME DC"`) || !strings.Contains(rec.Body.String(), `"piiMasked":false`) {
		t.Fatalf("unmasked message: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, "/edi/v1/interchanges/"+got.InterchangeID, "", "edi.operate", nil)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "ACME DC") {
		t.Fatalf("masked interchange detail: %d %s", rec.Code, rec.Body.String())
	}

	if rec := do(t, h, http.MethodGet, "/edi/v1/interchanges/"+someID+"/payload", "", "edi.operate", nil); rec.Code != http.StatusNotFound || problemCode(t, rec) != CodeNotFound {
		t.Fatalf("unknown interchange: %d %s", rec.Code, rec.Body.String())
	}
}

// PRD §6.35.6: bulk reprocess with a dry run. Unknown and outbound
// interchanges are reported per item; the real run is idempotent.
func TestMonitorAPI_BulkReprocess(t *testing.T) {
	h, pool := dbHandlerPool(t)
	rec := do(t, h, http.MethodPost, "/edi/v1/partners", distributor, "edi.admin", map[string]string{"Idempotency-Key": "p"})
	var partner store.Partner
	if err := json.Unmarshal(rec.Body.Bytes(), &partner); err != nil || rec.Code != http.StatusCreated {
		t.Fatalf("create partner: %d %s", rec.Code, rec.Body.String())
	}
	fixed := receiveFixture(t, h, "850-basic.x12")
	broken := receiveFixture(t, h, "850-ctt-mismatch.x12")
	if broken.Messages[0].Status != "FAILED" {
		t.Fatalf("CTT mismatch must fail: %+v", broken)
	}
	fixedMsg := fixed.Messages[0].MessageID
	// Stand-in for a map that failed at receipt and has since been fixed.
	if _, err := pool.Exec(context.Background(), `update message set status = 'FAILED', error = 'EDI-0105: no item cross-reference', canonical = null where id = $1`, fixedMsg); err != nil {
		t.Fatal(err)
	}
	rec = do(t, h, http.MethodPost, "/edi/v1/partners/"+partner.ID+"/outbound", `{"transactionSet":"856","document":`+asnDocument+`}`, "edi.operate",
		map[string]string{"Idempotency-Key": "asn-1"})
	var out store.OutboundResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusCreated {
		t.Fatalf("outbound: %d %s", rec.Code, rec.Body.String())
	}
	list := []string{fixed.InterchangeID, someID, broken.InterchangeID, out.InterchangeID}
	body := bulkBody(t, list)

	check := func(name string, res store.BulkReprocessResult, dryRun bool) {
		t.Helper()
		if res.DryRun != dryRun || res.Requested != 4 || res.Changed != 1 || len(res.Items) != 4 {
			t.Fatalf("%s: %+v", name, res)
		}
		want := []struct {
			id, outcome string
			changed     int
		}{
			{fixed.InterchangeID, store.BulkReprocessed, 1},
			{someID, store.BulkNotFound, 0},
			{broken.InterchangeID, store.BulkReprocessed, 0}, // still fails with the same error
			{out.InterchangeID, store.BulkNotInbound, 0},
		}
		for i, w := range want {
			if it := res.Items[i]; it.InterchangeID != w.id || it.Outcome != w.outcome || it.Changed != w.changed {
				t.Fatalf("%s item %d = %+v, want %+v", name, i, it, w)
			}
		}
		if m := res.Items[0].Messages; len(m) != 1 || m[0].Status != "MAPPED" || m[0].PreviousError == "" {
			t.Fatalf("%s: fixed message outcome %+v", name, m)
		}
	}

	rec = do(t, h, http.MethodPost, "/edi/v1/interchanges:reprocess?dryRun=true", body, "edi.operate", nil)
	var res store.BulkReprocessResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("dry run: %d %s", rec.Code, rec.Body.String())
	}
	check("dry run", res, true)
	var status string
	if err := pool.QueryRow(context.Background(), `select status from message where id = $1`, fixedMsg).Scan(&status); err != nil || status != "FAILED" {
		t.Fatalf("a dry run must not write: status %q err %v", status, err)
	}

	key := map[string]string{"Idempotency-Key": "bulk-1"}
	rec = do(t, h, http.MethodPost, "/edi/v1/interchanges:reprocess", body, "edi.operate", key)
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("run: %d %s", rec.Code, rec.Body.String())
	}
	check("run", res, false)
	if err := pool.QueryRow(context.Background(), `select status from message where id = $1`, fixedMsg).Scan(&status); err != nil || status != "MAPPED" {
		t.Fatalf("after the run: status %q err %v", status, err)
	}

	// Replay returns the first result and publishes nothing more.
	rec = do(t, h, http.MethodPost, "/edi/v1/interchanges:reprocess", body, "edi.operate", key)
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("replay: %d %s", rec.Code, rec.Body.String())
	}
	check("replay", res, false)
	var mapped int
	if err := pool.QueryRow(context.Background(), `select count(*) from outbox where type = $1 and aggregate_id = $2 and payload->'data'->>'status' = 'MAPPED'`,
		ediev.MessageReceived, fixedMsg).Scan(&mapped); err != nil || mapped != 2 {
		t.Fatalf("message.received MAPPED events = %d (receipt and one reprocess), err=%v", mapped, err)
	}

	// The same key with another list is refused.
	rec = do(t, h, http.MethodPost, "/edi/v1/interchanges:reprocess", bulkBody(t, list[:2]), "edi.operate", key)
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != CodeIdempotencyReuse {
		t.Fatalf("reuse: %d %s", rec.Code, rec.Body.String())
	}
}
