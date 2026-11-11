package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
)

const cxmlSecret = "correct-horse-battery"

func sample(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/cxml/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// cxmlSetup configures the partner's cXML identity and returns the public handler and the partner's endpoint URL path.
func cxmlSetup(t *testing.T) (http.Handler, http.Handler, *pgxpool.Pool, string, string) {
	t.Helper()
	a, h, pool, partner, _ := autoSetup(t, newFake())
	a.AS2PublicURL = "https://as2.test"
	rec := do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/cxml", `{"networkIdentity":"AN-DEMO-DISTRIBUTOR","sharedSecret":"`+cxmlSecret+`"}`, "edi.admin", srcScope)
	if rec.Code != http.StatusOK {
		t.Fatalf("cxml config: %d %s", rec.Code, rec.Body.String())
	}
	var cfg cxmlConfigView
	_ = json.Unmarshal(rec.Body.Bytes(), &cfg)
	if cfg.URL != "https://as2.test/cxml/v1/"+cfg.EndpointID || strings.Contains(rec.Body.String(), cxmlSecret) {
		t.Fatalf("config view: %s", rec.Body.String())
	}
	return h, a.AS2Routes(), pool, partner, "/cxml/v1/" + cfg.EndpointID
}

func postCXML(h http.Handler, path, body string) (int, string) {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "text/xml")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func statusOf(t *testing.T, body string) (int, string) {
	t.Helper()
	code, text, err := domain.ParseCXMLStatus([]byte(body))
	if err != nil {
		t.Fatalf("not a cXML response: %v: %s", err, body)
	}
	return code, text
}

func outboxCount(t *testing.T, pool *pgxpool.Pool, typ string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `select count(*) from outbox where type = $1`, typ).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCXML_InboundDocumentsAreRecordedAndMapped(t *testing.T) {
	h, pub, pool, _, path := cxmlSetup(t)
	for _, name := range []string{"invoice-detail.xml", "ship-notice.xml", "confirmation.xml"} {
		code, body := postCXML(pub, path, sample(t, name))
		if code != http.StatusOK {
			t.Fatalf("%s: %d %s", name, code, body)
		}
		if st, text := statusOf(t, body); st != 200 {
			t.Fatalf("%s: status %d %s", name, st, text)
		}
	}
	var n int
	if err := pool.QueryRow(context.Background(), `select count(*) from interchange where standard = 'CXML' and direction = 'IN'`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("interchanges: %d %v", n, err)
	}
	if got := outboxCount(t, pool, "dev.passage.edi.message.received.v1"); got != 3 {
		t.Fatalf("message.received events: %d", got)
	}
	var canonical string
	if err := pool.QueryRow(context.Background(), `select canonical::text from message where transaction_set = 'InvoiceDetailRequest'`).Scan(&canonical); err != nil {
		t.Fatal(err)
	}
	var doc domain.CXMLCanonical
	if err := json.Unmarshal([]byte(canonical), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Number != "DIST-INV-7001" || doc.Gross != "86.80" || doc.Currency != "USD" || len(doc.OrderIDs) != 1 || doc.OrderIDs[0] != "PO-1001" || len(doc.Lines) != 1 ||
		doc.Lines[0].Item != "prod-a" || doc.Lines[0].UnitPrice != "8.00" {
		t.Fatalf("invoice canonical: %+v", doc)
	}
	// The monitor shows the cXML messages with their sets.
	rec := do(t, h, http.MethodGet, "/edi/v1/messages?direction=IN&transactionSet=ShipNoticeRequest", "", "edi.operate", srcScope)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ShipNoticeRequest") {
		t.Fatalf("monitor: %d %s", rec.Code, rec.Body.String())
	}
}

func TestCXML_PayloadIDReplayAndConflict(t *testing.T) {
	_, pub, pool, _, path := cxmlSetup(t)
	doc := sample(t, "invoice-detail.xml")
	first, firstBody := postCXML(pub, path, doc)
	if first != http.StatusOK {
		t.Fatal(firstBody)
	}
	// The same payload again, with surrounding whitespace: the first answer is returned and nothing is recorded again.
	_, replay := postCXML(pub, path, "\n"+doc+"\n")
	if st, _ := statusOf(t, replay); st != 200 {
		t.Fatalf("replay: %s", replay)
	}
	var n int
	_ = pool.QueryRow(context.Background(), `select count(*) from message where transaction_set = 'InvoiceDetailRequest'`).Scan(&n)
	if n != 1 || outboxCount(t, pool, "dev.passage.edi.message.received.v1") != 1 {
		t.Fatalf("a replay must record nothing: %d messages", n)
	}
	// The same payloadID with another invoice number: EDI-0135 and nothing recorded.
	_, conflict := postCXML(pub, path, strings.Replace(doc, "DIST-INV-7001", "DIST-INV-7002", 1))
	st, text := statusOf(t, conflict)
	if st != 409 || !strings.Contains(text, domain.CodeCXMLPayloadConflict) {
		t.Fatalf("conflict: %d %s", st, text)
	}
	_ = pool.QueryRow(context.Background(), `select count(*) from message where transaction_set = 'InvoiceDetailRequest'`).Scan(&n)
	if n != 1 {
		t.Fatalf("a conflict must record nothing: %d", n)
	}
}

func TestCXML_AuthenticationAndRefusals(t *testing.T) {
	_, pub, pool, _, path := cxmlSetup(t)
	doc := sample(t, "ship-notice.xml")

	if _, body := postCXML(pub, path, strings.Replace(doc, cxmlSecret, "wrong-secret-value", 1)); !strings.Contains(body, `code="401"`) {
		t.Fatalf("bad secret: %s", body)
	}
	if _, body := postCXML(pub, path, strings.ReplaceAll(doc, "AN-DEMO-DISTRIBUTOR", "AN-SOMEONE-ELSE")); !strings.Contains(body, `code="401"`) {
		t.Fatalf("bad identity: %s", body)
	}
	if code, _ := postCXML(pub, "/cxml/v1/00000000-0000-0000-0000-00000000dead", doc); code != http.StatusNotFound {
		t.Fatalf("unknown endpoint: %d", code)
	}
	if _, body := postCXML(pub, path, "<not-xml"); !strings.Contains(body, `code="400"`) {
		t.Fatalf("garbage: %s", body)
	}
	// An OrderRequest is ours to send, not the network's.
	order := strings.Replace(strings.Replace(doc, "<ShipNoticeRequest>", "<OrderRequest>", 1), "</ShipNoticeRequest>", "</OrderRequest>", 1)
	if _, body := postCXML(pub, path, order); !strings.Contains(body, `code="400"`) || !strings.Contains(body, "OrderRequest") {
		t.Fatalf("unsupported type: %s", body)
	}
	var n int
	_ = pool.QueryRow(context.Background(), `select count(*) from interchange`).Scan(&n)
	if n != 0 {
		t.Fatalf("refused payloads must record nothing: %d", n)
	}
}

func TestCXML_UnmappableDocumentIsRecordedFailedAndAnsweredTheSameOnReplay(t *testing.T) {
	h, pub, pool, _, path := cxmlSetup(t)
	bad := strings.Replace(sample(t, "invoice-detail.xml"), `invoiceID="DIST-INV-7001"`, `invoiceID=""`, 1)
	_, body := postCXML(pub, path, bad)
	if st, _ := statusOf(t, body); st != 400 {
		t.Fatalf("unmappable: %s", body)
	}
	_, again := postCXML(pub, path, bad)
	if st, _ := statusOf(t, again); st != 400 {
		t.Fatalf("replay of a refusal: %s", again)
	}
	var n int
	var status string
	if err := pool.QueryRow(context.Background(), `select count(*), min(status) from message where transaction_set = 'InvoiceDetailRequest'`).Scan(&n, &status); err != nil || n != 1 || status != "FAILED" {
		t.Fatalf("failed message: %d %s %v", n, status, err)
	}
	if outboxCount(t, pool, "dev.passage.edi.message.failed.v1") != 1 {
		t.Fatal("one message.failed event expected")
	}
	// The interchange list shows the CXML interchange rejected.
	rec := do(t, h, http.MethodGet, "/edi/v1/interchanges?direction=IN", "", "edi.operate", srcScope)
	if !strings.Contains(rec.Body.String(), `"standard":"CXML"`) || !strings.Contains(rec.Body.String(), `"ackStatus":"REJECTED"`) {
		t.Fatalf("interchanges: %s", rec.Body.String())
	}
}

func TestCXML_ConfigRotationKeepsTheURL(t *testing.T) {
	h, pub, _, partner, path := cxmlSetup(t)
	rec := do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/cxml", `{"networkIdentity":"AN-DEMO-DISTRIBUTOR","sharedSecret":"a-new-and-longer-secret"}`, "edi.admin", srcScope)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), strings.TrimPrefix(path, "/cxml/v1/")) {
		t.Fatalf("rotate: %d %s", rec.Code, rec.Body.String())
	}
	if _, body := postCXML(pub, path, sample(t, "confirmation.xml")); !strings.Contains(body, `code="401"`) {
		t.Fatalf("the old secret must stop working: %s", body)
	}
	newDoc := strings.Replace(sample(t, "confirmation.xml"), cxmlSecret, "a-new-and-longer-secret", 1)
	if _, body := postCXML(pub, path, newDoc); !strings.Contains(body, `code="200"`) {
		t.Fatalf("the new secret must work: %s", body)
	}
	if rec := do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/cxml", `{"networkIdentity":"","sharedSecret":"short"}`, "edi.admin", srcScope); rec.Code != http.StatusBadRequest {
		t.Fatalf("validation: %d", rec.Code)
	}
}
