package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
	"github.com/BenjaminSRussell/passage/services/edi/migrations"
)

// dbHandler returns the routes over a migrated throwaway schema.
func dbHandler(t *testing.T) http.Handler {
	t.Helper()
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return New(store.New(pool, 30)).Routes()
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
	rec := do(t, h, http.MethodPost, "/edi/v1/partners", distributor, "EDI_ADMIN", key)
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
	rec = do(t, h, http.MethodPost, "/edi/v1/partners", strings.ReplaceAll(distributor, ",", ", "), "EDI_ADMIN", key)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), created.ID) {
		t.Fatalf("replay: %d %s", rec.Code, rec.Body.String())
	}
	// Same key, different body: 422 idempotency_key_reuse.
	rec = do(t, h, http.MethodPost, "/edi/v1/partners", strings.Replace(distributor, "Demo distributor", "Renamed", 1), "EDI_ADMIN", key)
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != CodeIdempotencyReuse {
		t.Fatalf("reuse: %d %s", rec.Code, rec.Body.String())
	}
	// New key, same ISA identity: 409.
	rec = do(t, h, http.MethodPost, "/edi/v1/partners", distributor, "EDI_ADMIN", map[string]string{"Idempotency-Key": "partner-2"})
	if rec.Code != http.StatusConflict || problemCode(t, rec) != CodePartnerExists {
		t.Fatalf("duplicate identity: %d %s", rec.Code, rec.Body.String())
	}

	rec = do(t, h, http.MethodGet, "/edi/v1/partners/"+created.ID, "", "EDI_ADMIN", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("get: %d %s etag=%q", rec.Code, rec.Body.String(), rec.Header().Get("ETag"))
	}
	rec = do(t, h, http.MethodGet, "/edi/v1/partners/not-a-uuid", "", "EDI_ADMIN", nil)
	if rec.Code != http.StatusNotFound || problemCode(t, rec) != CodeNotFound {
		t.Fatalf("bad id: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, "/edi/v1/partners?environment=TEST", "", "EDI_ADMIN", nil)
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

	rec := do(t, h, http.MethodPost, "/edi/v1/inbound/x12?environment=TEST", raw, "EDI_OPERATE", nil)
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != CodeUnknownSender {
		t.Fatalf("unknown sender: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPost, "/edi/v1/partners", distributor, "EDI_ADMIN", map[string]string{"Idempotency-Key": "p"}); rec.Code != http.StatusCreated {
		t.Fatalf("create partner: %d %s", rec.Code, rec.Body.String())
	}
	// The partner is in TEST; the default PROD environment does not resolve it.
	if rec := do(t, h, http.MethodPost, "/edi/v1/inbound/x12", raw, "EDI_OPERATE", nil); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("PROD lookup must not find a TEST partner: %d %s", rec.Code, rec.Body.String())
	}

	rec = do(t, h, http.MethodPost, "/edi/v1/inbound/x12?environment=TEST", raw, "EDI_OPERATE", nil)
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
	rec = do(t, h, http.MethodPost, "/edi/v1/inbound/x12?environment=TEST", raw, "EDI_OPERATE", nil)
	if rec.Code != http.StatusConflict || problemCode(t, rec) != CodeDuplicateControl {
		t.Fatalf("duplicate: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, "/edi/v1/interchanges", "", "EDI_OPERATE", nil)
	var list struct {
		Items []store.InterchangeSummary `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Items) != 1 {
		t.Fatalf("interchanges: %d %s", rec.Code, rec.Body.String())
	}
	if list.Items[0].PartnerName != "Demo distributor" || list.Items[0].MessageCount != 1 || list.Items[0].ControlNumber != "000000001" {
		t.Fatalf("interchange summary: %+v", list.Items[0])
	}

	rec = do(t, h, http.MethodGet, "/edi/v1/interchanges/"+got.InterchangeID, "", "EDI_OPERATE", nil)
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
	rec := do(t, h, http.MethodGet, "/edi/v1/interchanges?ackStatus=LOST&limit=500&partnerId=x", "", "EDI_OPERATE", nil)
	if rec.Code != http.StatusBadRequest || problemCode(t, rec) != CodeValidation {
		t.Fatalf("filters: %d %s", rec.Code, rec.Body.String())
	}
	for _, field := range []string{"ackStatus", "limit", "partnerId"} {
		if !strings.Contains(rec.Body.String(), `"`+field+`"`) {
			t.Errorf("missing field error %s: %s", field, rec.Body.String())
		}
	}
	if rec := do(t, h, http.MethodGet, "/edi/v1/interchanges", "", "EDI_ADMIN", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("EDI_ADMIN alone must not read the monitor: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/edi/v1/interchanges/0195f0a0-0000-7000-8000-0000000000aa", "", "EDI_OPERATE", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown interchange: %d %s", rec.Code, rec.Body.String())
	}
}
