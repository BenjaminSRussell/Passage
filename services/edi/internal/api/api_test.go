package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
)

// sample850 builds a serialized 850 with one PO1 and CTT count ctt. Building it
// through domain.Serialize keeps the envelope counts correct by construction.
func sample850(t *testing.T, ctt string) string {
	t.Helper()
	ic := domain.Interchange{
		Delims:   domain.Delims{Element: '*', Component: '>', Segment: '~'},
		Sender:   domain.Party{Qualifier: "ZZ", ID: "DISTRIBUTOR"},
		Receiver: domain.Party{Qualifier: "ZZ", ID: "MANUFACTURER"},
		Date:     "261021", Time: "1200", Version: "00401", Control: "000000001", Usage: "T",
		Groups: []domain.Group{{
			FunctionalID: "PO", Sender: "DISTRIBUTOR", Receiver: "MANUFACTURER",
			Date: "20261021", Time: "1200", Control: "1", Version: "004010",
			Transactions: []domain.Transaction{{SetID: "850", Control: "0001", Body: []domain.Segment{
				{ID: "BEG", Elements: []string{"00", "SA", "PO1234", "", "20261021"}},
				{ID: "CUR", Elements: []string{"BY", "USD"}},
				{ID: "PO1", Elements: []string{"1", "10", "EA", "2.50", "", "BP", "ABC-1"}},
				{ID: "CTT", Elements: []string{ctt}},
			}}},
		}},
	}
	raw, err := domain.Serialize(ic)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func call(t *testing.T, method, path, body string, duties string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(tenant.HeaderTenant, testkit.TenantID(1))
	if duties != "" {
		req.Header.Set(tenant.HeaderDuties, duties)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	a := New(nil)
	a.AllowBodyOutbound = true
	a.Routes().ServeHTTP(rec, req)
	return rec
}

func problemCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var p struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("body is not a problem: %v (%s)", err, rec.Body.String())
	}
	return p.Code
}

func TestMapsTest_MapsValid850(t *testing.T) {
	body, _ := json.Marshal(map[string]string{"raw": sample850(t, "1")})
	rec := call(t, http.MethodPost, "/edi/v1/maps:test", string(body), "EDI_ADMIN", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Transactions []struct {
			TransactionSet string `json:"transactionSet"`
			Status         string `json:"status"`
			Order          struct {
				PONumber string `json:"poNumber"`
				Lines    []struct {
					Quantity string `json:"quantity"`
					ItemRef  struct {
						Qualifier string `json:"qualifier"`
						Value     string `json:"value"`
					} `json:"itemRef"`
				} `json:"lines"`
			} `json:"order"`
			Issues []domain.Issue `json:"issues"`
		} `json:"transactions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Transactions) != 1 {
		t.Fatalf("want 1 transaction, got %d", len(got.Transactions))
	}
	tx := got.Transactions[0]
	if tx.TransactionSet != "850" || tx.Status != "MAPPED" || len(tx.Issues) != 0 {
		t.Fatalf("unexpected result: %+v", tx)
	}
	if tx.Order.PONumber != "PO1234" || len(tx.Order.Lines) != 1 || tx.Order.Lines[0].Quantity != "10" {
		t.Fatalf("unexpected order: %+v", tx.Order)
	}
	if tx.Order.Lines[0].ItemRef.Qualifier != "BP" || tx.Order.Lines[0].ItemRef.Value != "ABC-1" {
		t.Fatalf("item reference not mapped: %+v", tx.Order.Lines[0].ItemRef)
	}
}

func TestMapsTest_CountMismatchIsRejected(t *testing.T) {
	body, _ := json.Marshal(map[string]string{"raw": sample850(t, "2")})
	rec := call(t, http.MethodPost, "/edi/v1/maps:test", string(body), "EDI_ADMIN", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), domain.CodeCountMismatch) || !strings.Contains(rec.Body.String(), `"status":"FAILED"`) {
		t.Fatalf("expected EDI-0103 and FAILED: %s", rec.Body.String())
	}
}

func TestMapsTest_RequiresDuty(t *testing.T) {
	rec := call(t, http.MethodPost, "/edi/v1/maps:test", `{"raw":"x"}`, "", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", rec.Code)
	}
}

func TestMapsTest_SyntaxErrorIsProblem(t *testing.T) {
	rec := call(t, http.MethodPost, "/edi/v1/maps:test", `{"raw":"not an interchange"}`, "EDI_ADMIN", nil)
	if rec.Code != http.StatusBadRequest || problemCode(t, rec) != CodeSyntax {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestCreatePartner_RequiresIdempotencyKey(t *testing.T) {
	body := `{"name":"Demo distributor","isaQualifier":"ZZ","isaId":"DISTRIBUTOR","environment":"TEST"}`
	rec := call(t, http.MethodPost, "/edi/v1/partners", body, "EDI_ADMIN", nil)
	if rec.Code != http.StatusBadRequest || problemCode(t, rec) != CodeIdempotencyKey {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestCreatePartner_ValidatesProfile(t *testing.T) {
	body := `{"name":"","isaQualifier":"z","isaId":"","environment":"STAGING"}`
	rec := call(t, http.MethodPost, "/edi/v1/partners", body, "EDI_ADMIN", map[string]string{"Idempotency-Key": "k1"})
	if rec.Code != http.StatusBadRequest || problemCode(t, rec) != CodeValidation {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	for _, field := range []string{"name", "isaQualifier", "isaId", "environment"} {
		if !strings.Contains(rec.Body.String(), `"`+field+`"`) {
			t.Errorf("missing field error %s: %s", field, rec.Body.String())
		}
	}
}

func TestCreatePartner_RejectsUnknownFields(t *testing.T) {
	body := `{"name":"x","isaQualifier":"ZZ","isaId":"A","environment":"TEST","surprise":1}`
	rec := call(t, http.MethodPost, "/edi/v1/partners", body, "EDI_ADMIN", map[string]string{"Idempotency-Key": "k2"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
}

func TestInbound_RejectsBadEnvironment(t *testing.T) {
	rec := call(t, http.MethodPost, "/edi/v1/inbound/x12?environment=QA", "ISA", "EDI_OPERATE", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
}

func TestInbound_SyntaxErrorBeforeStore(t *testing.T) {
	// A nil store proves the parse fails before any partner lookup.
	rec := call(t, http.MethodPost, "/edi/v1/inbound/x12", "GS*PO~", "EDI_OPERATE", nil)
	if rec.Code != http.StatusBadRequest || problemCode(t, rec) != CodeSyntax {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestMapsTest_UnsupportedSetIsReportedAsAnIssue(t *testing.T) {
	ic, err := domain.Parse(sample850(t, "1"))
	if err != nil {
		t.Fatal(err)
	}
	ic.Groups[0].Transactions[0].SetID = "810"
	raw, err := domain.Serialize(ic)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"raw": raw})
	rec := call(t, http.MethodPost, "/edi/v1/maps:test", string(body), "EDI_ADMIN", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Transactions []struct {
			Status string         `json:"status"`
			Issues []domain.Issue `json:"issues"`
		} `json:"transactions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	tx := got.Transactions[0]
	if strings.Contains(rec.Body.String(), `"order"`) {
		t.Fatalf("an unsupported set has no canonical order: %s", rec.Body.String())
	}
	if tx.Status != "FAILED" || len(tx.Issues) != 1 || tx.Issues[0].Code != CodeUnsupportedSet || !tx.Issues[0].Reject {
		t.Fatalf("unsupported 810 = %+v", tx)
	}
}

const outboundPath = "/edi/v1/partners/0195f0a0-0000-7000-8000-000000000001/outbound"

// The outbound route validates the key, the set and the document before it
// touches the store: a nil store proves every one of these fails first.
func TestOutbound_ValidatesBeforeStore(t *testing.T) {
	key := map[string]string{"Idempotency-Key": "k"}
	cases := []struct {
		name, path, body string
		headers          map[string]string
		status           int
		code             string
	}{
		{"no key", outboundPath, `{"transactionSet":"856","document":{}}`, nil, http.StatusBadRequest, CodeIdempotencyKey},
		{"bad partner id", "/edi/v1/partners/nope/outbound", `{"transactionSet":"856","document":{}}`, key, http.StatusNotFound, CodeNotFound},
		{"inbound-only set", outboundPath, `{"transactionSet":"850","document":{}}`, key, http.StatusBadRequest, CodeValidation},
		{"unknown request field", outboundPath, `{"transactionSet":"856","document":{},"x":1}`, key, http.StatusBadRequest, "COM-0001"},
		{"document fails the 856 rules", outboundPath, `{"transactionSet":"856","document":{"shipmentId":"L1"}}`, key, http.StatusUnprocessableEntity, CodeOutbound},
		{"unknown document field", outboundPath, `{"transactionSet":"810","document":{"number":"1","colour":"red"}}`, key, http.StatusUnprocessableEntity, CodeOutbound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := call(t, http.MethodPost, c.path, c.body, "EDI_OPERATE", c.headers)
			if rec.Code != c.status || problemCode(t, rec) != c.code {
				t.Fatalf("status %d body %s, want %d %s", rec.Code, rec.Body.String(), c.status, c.code)
			}
		})
	}
	if rec := call(t, http.MethodPost, outboundPath, `{}`, "EDI_ADMIN", key); rec.Code != http.StatusForbidden {
		t.Fatalf("EDI_ADMIN alone must not send documents: %d", rec.Code)
	}
}

func TestInterchanges_RejectsUnknownDirection(t *testing.T) {
	rec := call(t, http.MethodGet, "/edi/v1/interchanges?direction=SIDEWAYS", "", "EDI_OPERATE", nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"direction"`) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}
