package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"

	"github.com/BenjaminSRussell/passage/services/edi/internal/cxmlmap"
	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/internal/sources"
)

const (
	exVendor = "00000000-0000-0000-0000-0000000000f1"
	exPO     = "00000000-0000-0000-0000-0000000000a9"
)

// exSources is the fake sources with a purchase order to send.
type exSources struct {
	*fakeSources
	po sources.PurchaseOrder
}

func (s *exSources) PurchaseOrder(_ context.Context, le, _ string) (sources.PurchaseOrder, error) {
	s.record("purchase-order", le)
	return s.po, nil
}

// exDownstream is purchasing and receiving in one fake.
type exDownstream struct {
	mu        sync.Mutex
	po        sources.PurchaseOrder
	confirmed []string
	asns      []sources.ASNRequest
	refuse    *sources.DownstreamRefusedError
	principal string
	duties    []string
	// payables, when set, is wired as the mapper's vendor-invoice intake.
	payables cxmlmap.Payables
}

func (d *exDownstream) note(ctx context.Context) {
	if p, ok := tenant.FromContext(ctx); ok {
		d.principal = p.UserID
		for duty := range p.Duties {
			d.duties = append(d.duties, duty)
		}
	}
}

func (d *exDownstream) PurchaseOrder(ctx context.Context, _, _ string) (sources.PurchaseOrder, error) {
	d.note(ctx)
	return d.po, nil
}

func (d *exDownstream) ConfirmPurchaseOrder(_ context.Context, _, id string, _ int, _ string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.refuse != nil {
		return d.refuse
	}
	d.confirmed = append(d.confirmed, id)
	return nil
}

func (d *exDownstream) CreateASN(_ context.Context, _, _ string, body sources.ASNRequest) (sources.ASNRef, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.refuse != nil {
		return sources.ASNRef{}, d.refuse
	}
	d.asns = append(d.asns, body)
	return sources.ASNRef{ID: "00000000-0000-0000-0000-0000000000a5", Status: "OPEN"}, nil
}

func poFixture() sources.PurchaseOrder {
	return sources.PurchaseOrder{ID: exPO, Number: "PO-1001", LegalEntityID: srcLE, VendorID: exVendor, Currency: "USD", ApprovalStatus: "APPROVED", RowVersion: 3, Total: "80.00",
		CreatedAt: "2026-10-15T09:00:00Z", Lines: []sources.PurchaseOrderLine{{LineNo: 1, ProductID: "prod-a", Description: "Pump seal kit", Qty: "10", Unit: "EA", UnitPrice: "8.00", DeliveryDate: "2026-11-20"}}}
}

func TestCXMLOutbound_OrderRequestIsSentAndRecorded(t *testing.T) {
	f := newFake()
	var posted []string
	network := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		posted = append(posted, string(raw))
		w.Header().Set("Content-Type", "text/xml")
		_, _ = io.WriteString(w, domain.CXMLResponse("resp-1", "2026-11-11T10:00:00Z", 200, "OK"))
	}))
	defer network.Close()
	a, h, pool, partner, _ := autoSetup(t, f)
	a.PunchoutHTTP = network.Client()
	ex := &exSources{fakeSources: f, po: poFixture()}
	a.Sources = ex
	if rec := do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/cxml", `{"networkIdentity":"AN-SUPPLIER","sharedSecret":"`+cxmlSecret+`"}`, "edi.admin", srcScope); rec.Code != http.StatusOK {
		t.Fatalf("cxml: %d %s", rec.Code, rec.Body.String())
	}
	send := func(docType, id string) *httptest.ResponseRecorder {
		return do(t, h, http.MethodPost, "/edi/v1/partners/"+partner+"/cxml:send", `{"documentType":"`+docType+`","sourceId":"`+id+`"}`, "edi.operate", srcScope)
	}
	// Without outbound settings the send is refused.
	if rec := send("OrderRequest", exPO); rec.Code != http.StatusConflict || problemCode(t, rec) != domain.CodeCXMLNotConfigured {
		t.Fatalf("no outbound settings: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/cxml/outbound", `{"outboundUrl":"http://x.example/cxml","ourIdentity":"AN-ME"}`, "edi.admin", srcScope); rec.Code != http.StatusBadRequest {
		t.Fatalf("an http URL is refused: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/cxml/outbound", `{"outboundUrl":"`+network.URL+`/cxml","ourIdentity":"AN-ME","vendorId":"`+exVendor+`"}`, "edi.admin", srcScope); rec.Code != http.StatusOK {
		t.Fatalf("outbound settings: %d %s", rec.Code, rec.Body.String())
	}
	rec := send("OrderRequest", exPO)
	if rec.Code != http.StatusCreated {
		t.Fatalf("order request: %d %s", rec.Code, rec.Body.String())
	}
	if len(posted) != 1 {
		t.Fatalf("posted %d", len(posted))
	}
	env, err := domain.ParseCXML([]byte(posted[0]))
	if err != nil || env.DocType != "OrderRequest" || env.From != "AN-ME" || env.To != "AN-SUPPLIER" {
		t.Fatalf("envelope: %+v %v", env, err)
	}
	for _, want := range []string{`orderID="PO-1001"`, `lineNumber="1"`, `quantity="10"`, `<Money currency="USD">8.00</Money>`, "<SupplierPartID>prod-a</SupplierPartID>"} {
		if !strings.Contains(posted[0], want) {
			t.Errorf("OrderRequest lacks %s: %s", want, posted[0])
		}
	}
	// The recorded copy keeps no shared secret, the order is remembered under its orderID, and the message is SENT.
	var raw, status string
	if err := pool.QueryRow(context.Background(), `select i.raw_payload, m.status from interchange i join message m on m.interchange_id = i.id where i.direction = 'OUT' and i.standard = 'CXML'`).Scan(&raw, &status); err != nil || status != "SENT" {
		t.Fatalf("recorded: %v %q", err, status)
	}
	var n int
	if err := pool.QueryRow(context.Background(), `select count(*) from cxml_order where order_id = 'PO-1001' and purchase_order_id = $1`, exPO).Scan(&n); err != nil || n != 1 {
		t.Fatalf("cxml_order: %d %v", n, err)
	}
	// A purchase order of another vendor, or one not approved, is not sent.
	ex.po.VendorID = "00000000-0000-0000-0000-0000000000f2"
	if rec := send("OrderRequest", exPO); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("another vendor: %d %s", rec.Code, rec.Body.String())
	}
	ex.po = poFixture()
	ex.po.ApprovalStatus = "DRAFT"
	if rec := send("OrderRequest", exPO); rec.Code != http.StatusConflict {
		t.Fatalf("draft: %d", rec.Code)
	}
	// The supplier-side documents come out as the documents the inbound codec reads: the same orderID, lines and quantities.
	f.order.Lines[0].Quantity = decimal.NewFromInt(10)
	f.invoice.Currency = "USD"
	for _, tc := range []struct{ docType, id, set string }{{"ConfirmationRequest", srcOrder, "ConfirmationRequest"}, {"ShipNoticeRequest", srcShipment, "ShipNoticeRequest"}, {"InvoiceDetailRequest", srcInvoice, "InvoiceDetailRequest"}} {
		before := len(posted)
		if rec := send(tc.docType, tc.id); rec.Code != http.StatusCreated {
			t.Fatalf("%s: %d %s", tc.docType, rec.Code, rec.Body.String())
		}
		doc := posted[before]
		env, err := domain.ParseCXML([]byte(doc))
		if err != nil || env.DocType != tc.set {
			t.Fatalf("%s envelope: %+v %v", tc.docType, env, err)
		}
		c, err := domain.MapCXMLDocument([]byte(doc), env)
		if err != nil || len(c.OrderIDs) != 1 || c.OrderIDs[0] != "PO-1001" || len(c.Lines) == 0 {
			t.Fatalf("%s does not read back: %+v %v", tc.docType, c, err)
		}
	}
	// A network that refuses the document: 502 with the recorded message FAILED.
	refusing := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, domain.CXMLResponse("resp-2", "2026-11-11T10:00:00Z", 403, "Forbidden"))
	}))
	defer refusing.Close()
	a.PunchoutHTTP = refusing.Client()
	do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/cxml/outbound", `{"outboundUrl":"`+refusing.URL+`/cxml","ourIdentity":"AN-ME","vendorId":"`+exVendor+`"}`, "edi.admin", srcScope)
	if rec := send("ConfirmationRequest", srcOrder); rec.Code != http.StatusBadGateway || problemCode(t, rec) != domain.CodeCXMLSendFailed {
		t.Fatalf("refused: %d %s", rec.Code, rec.Body.String())
	}
	if err := pool.QueryRow(context.Background(), `select count(*) from message where status = 'FAILED' and transaction_set = 'ConfirmationRequest'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("failed message: %d %v", n, err)
	}
}

// exInbound is the cXML partner with the mapper wired to the downstream fakes; orderKnown records PO-1001 as sent to the partner.
func exInbound(t *testing.T, orderKnown bool) (http.Handler, http.Handler, *pgxpool.Pool, string, string, *exDownstream, func()) {
	t.Helper()
	f := newFake()
	a, h, pool, partner, _ := autoSetup(t, f)
	a.AS2PublicURL = "https://as2.test"
	rec := do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/cxml", `{"networkIdentity":"AN-DEMO-DISTRIBUTOR","sharedSecret":"`+cxmlSecret+`"}`, "edi.admin", srcScope)
	var cfg cxmlConfigView
	if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("cxml: %d %s", rec.Code, rec.Body.String())
	}
	down := &exDownstream{po: poFixture()}
	// wire switches the mapper on; a document received before it is mapped by :post, one received after it is mapped on receipt.
	wire := func() { a.CXMLMapper = &cxmlmap.Mapper{Store: a.store, Purchasing: down, Receiving: down, Payables: down.payables} }
	if orderKnown {
		if _, err := pool.Exec(context.Background(), `insert into cxml_order (tenant_id, partner_id, order_id, purchase_order_id, legal_entity_id, vendor_id) values ($1,$2,'PO-1001',$3,$4,$5)`,
			testkit.TenantID(7), partner, exPO, srcLE, exVendor); err != nil {
			t.Fatal(err)
		}
	}
	return h, a.AS2Routes(), pool, partner, "/cxml/v1/" + cfg.EndpointID, down, wire
}

func messageID(t *testing.T, pool *pgxpool.Pool, set string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `select id::text from message where transaction_set = $1 and st_control in (select control_number from interchange where direction = 'IN')`, set).Scan(&id); err != nil {
		t.Fatalf("message %s: %v", set, err)
	}
	return id
}

func TestCXMLInbound_ConfirmationAndShipNoticeAreMappedOntoPurchasingAndReceiving(t *testing.T) {
	h, pub, pool, _, path, down, wire := exInbound(t, true)
	for _, name := range []string{"confirmation.xml", "ship-notice.xml", "invoice-detail.xml"} {
		if code, body := postCXML(pub, path, sample(t, name)); code != http.StatusOK {
			t.Fatalf("%s: %d %s", name, code, body)
		}
	}
	wire()
	post := func(set string) *httptest.ResponseRecorder {
		return do(t, h, http.MethodPost, "/edi/v1/messages/"+messageID(t, pool, set)+":post", "", "edi.operate", srcScope)
	}
	rec := post("ConfirmationRequest")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"PURCHASE_ORDER"`) || len(down.confirmed) != 1 || down.confirmed[0] != exPO {
		t.Fatalf("confirmation: %d %s %v", rec.Code, rec.Body.String(), down.confirmed)
	}
	if down.principal != "service:edi-cxml" {
		t.Fatalf("the downstream call ran as %q", down.principal)
	}
	// Posting again is a replay: nothing is confirmed twice.
	if rec := post("ConfirmationRequest"); rec.Code != http.StatusOK || len(down.confirmed) != 1 {
		t.Fatalf("repeat: %d %v", rec.Code, down.confirmed)
	}
	rec = post("ShipNoticeRequest")
	if rec.Code != http.StatusOK || len(down.asns) != 1 || down.asns[0].PurchaseOrderID != exPO || down.asns[0].TrackingNumber != "DIST-ASN-55" || down.asns[0].ShipDate != "2026-11-04" ||
		len(down.asns[0].Lines) != 1 || down.asns[0].Lines[0].POLineNo != 1 || down.asns[0].Lines[0].Qty != "10" {
		t.Fatalf("ship notice: %d %s %+v", rec.Code, rec.Body.String(), down.asns)
	}
	var status, kind string
	if err := pool.QueryRow(context.Background(), `select status, downstream_kind from message where transaction_set = 'ShipNoticeRequest'`).Scan(&status, &kind); err != nil || status != "POSTED" || kind != "ASN" {
		t.Fatalf("message: %s %s %v", status, kind, err)
	}
	// An invoice is recorded and parked with EDI-0038: payables matches it against receipts.
	rec = post("InvoiceDetailRequest")
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != domain.CodeCXMLInvoiceTarget {
		t.Fatalf("invoice: %d %s", rec.Code, rec.Body.String())
	}
}

func TestCXMLInbound_ParksWhatCannotBeMapped(t *testing.T) {
	// No purchase order was sent under the orderID.
	h, pub, pool, _, path, down, wire := exInbound(t, false)
	postCXML(pub, path, sample(t, "confirmation.xml"))
	wire()
	rec := do(t, h, http.MethodPost, "/edi/v1/messages/"+messageID(t, pool, "ConfirmationRequest")+":post", "", "edi.operate", srcScope)
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != domain.CodeCXMLUnknownOrder || len(down.confirmed) != 0 {
		t.Fatalf("unknown order: %d %s", rec.Code, rec.Body.String())
	}
	// A line that is not accepted needs the buyer.
	h, pub, pool, _, path, down, wire = exInbound(t, true)
	postCXML(pub, path, strings.Replace(sample(t, "confirmation.xml"), `<ConfirmationStatus type="accept"`, `<ConfirmationStatus type="reject"`, 1))
	wire()
	rec = do(t, h, http.MethodPost, "/edi/v1/messages/"+messageID(t, pool, "ConfirmationRequest")+":post", "", "edi.operate", srcScope)
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != domain.CodeCXMLNeedsReview || len(down.confirmed) != 0 {
		t.Fatalf("rejected line: %d %s", rec.Code, rec.Body.String())
	}
	// A backorder split (6 accepted, 4 backordered) and a confirmation that leaves out a line of the order are not confirmations.
	h, pub, pool, _, path, down, wire = exInbound(t, true)
	split := strings.Replace(sample(t, "confirmation.xml"), `<ConfirmationStatus type="accept" quantity="10" shipmentDate="2026-11-04T00:00:00-05:00"><UnitOfMeasure>EA</UnitOfMeasure></ConfirmationStatus>`,
		`<ConfirmationStatus type="accept" quantity="6"><UnitOfMeasure>EA</UnitOfMeasure></ConfirmationStatus><ConfirmationStatus type="backordered" quantity="4"><UnitOfMeasure>EA</UnitOfMeasure></ConfirmationStatus>`, 1)
	if split == sample(t, "confirmation.xml") {
		t.Fatal("the fixture was not changed")
	}
	postCXML(pub, path, split)
	wire()
	rec = do(t, h, http.MethodPost, "/edi/v1/messages/"+messageID(t, pool, "ConfirmationRequest")+":post", "", "edi.operate", srcScope)
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != domain.CodeCXMLNeedsReview || len(down.confirmed) != 0 {
		t.Fatalf("backorder split: %d %s", rec.Code, rec.Body.String())
	}
	h, pub, pool, _, path, down, wire = exInbound(t, true)
	down.po.Lines = append(down.po.Lines, sources.PurchaseOrderLine{LineNo: 2, ProductID: "prod-b", Qty: "5", Unit: "EA", UnitPrice: "3.00"})
	postCXML(pub, path, sample(t, "confirmation.xml"))
	wire()
	rec = do(t, h, http.MethodPost, "/edi/v1/messages/"+messageID(t, pool, "ConfirmationRequest")+":post", "", "edi.operate", srcScope)
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != domain.CodeCXMLNeedsReview || len(down.confirmed) != 0 {
		t.Fatalf("missing line: %d %s", rec.Code, rec.Body.String())
	}
	// Receiving refuses the notice (quantity over the open quantity): parked with its code, posted again once fixed.
	h, pub, pool, _, path, down, wire = exInbound(t, true)
	down.refuse = &sources.DownstreamRefusedError{Service: "receiving", Status: 422, Code: "SUP-ASN-001", Detail: "over the open quantity"}
	postCXML(pub, path, sample(t, "ship-notice.xml"))
	wire()
	id := messageID(t, pool, "ShipNoticeRequest")
	rec = do(t, h, http.MethodPost, "/edi/v1/messages/"+id+":post", "", "edi.operate", srcScope)
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != domain.CodeCXMLDownstream || !strings.Contains(rec.Body.String(), "SUP-ASN-001") {
		t.Fatalf("refused: %d %s", rec.Code, rec.Body.String())
	}
	down.refuse = nil
	if rec := do(t, h, http.MethodPost, "/edi/v1/messages/"+id+":post", "", "edi.operate", srcScope); rec.Code != http.StatusOK || len(down.asns) != 1 {
		t.Fatalf("posted again: %d %s", rec.Code, rec.Body.String())
	}
}

func TestCXMLInbound_FailedMessageIsReprocessed(t *testing.T) {
	h, pub, pool, _, path, _, _ := exInbound(t, true)
	// A ship notice with no shipmentID fails mapping at receipt: 400 to the network and FAILED in the monitor.
	broken := strings.Replace(sample(t, "ship-notice.xml"), `shipmentID="DIST-ASN-55"`, `shipmentID=""`, 1)
	if code, body := postCXML(pub, path, broken); code != http.StatusOK {
		t.Fatalf("receipt: %d %s", code, body)
	} else if st, _ := statusOf(t, body); st != 400 {
		t.Fatalf("the network is told 400: %s", body)
	}
	id := messageID(t, pool, "ShipNoticeRequest")
	var interchange string
	if err := pool.QueryRow(context.Background(), `select interchange_id::text from message where id = $1`, id).Scan(&interchange); err != nil {
		t.Fatal(err)
	}
	// Mapping is unchanged, so the reprocess leaves it FAILED; fix the stored payload (as a support tool would) and reprocess.
	rec := do(t, h, http.MethodPost, "/edi/v1/interchanges/"+interchange+":reprocess", "", "edi.operate", map[string]string{tenant.HeaderLegalEntity: srcLE, "Idempotency-Key": "r1"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"FAILED"`) {
		t.Fatalf("reprocess unchanged: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := pool.Exec(context.Background(), `update interchange set raw_payload = $2 where id = $1`, interchange, sample(t, "ship-notice.xml")); err != nil {
		t.Fatal(err)
	}
	rec = do(t, h, http.MethodPost, "/edi/v1/interchanges/"+interchange+":reprocess", "", "edi.operate", map[string]string{tenant.HeaderLegalEntity: srcLE, "Idempotency-Key": "r2"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"MAPPED"`) {
		t.Fatalf("reprocess fixed: %d %s", rec.Code, rec.Body.String())
	}
	var status string
	if err := pool.QueryRow(context.Background(), `select status from message where id = $1`, id).Scan(&status); err != nil || status != "MAPPED" {
		t.Fatalf("status %s %v", status, err)
	}
}

// A document received while the mapper is wired is put onto its service right after receipt, without anyone posting it.
func TestCXMLInbound_IsMappedOnReceipt(t *testing.T) {
	_, pub, pool, _, path, down, wire := exInbound(t, true)
	wire()
	if code, body := postCXML(pub, path, sample(t, "confirmation.xml")); code != http.StatusOK {
		t.Fatalf("receipt: %d %s", code, body)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var status string
		if err := pool.QueryRow(context.Background(), `select status from message where transaction_set = 'ConfirmationRequest'`).Scan(&status); err == nil && status == "POSTED" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the confirmation was not mapped on receipt")
		}
		time.Sleep(50 * time.Millisecond)
	}
	down.mu.Lock()
	defer down.mu.Unlock()
	if len(down.confirmed) != 1 {
		t.Fatalf("confirmed %v", down.confirmed)
	}
}

// The default client for partner URLs refuses loopback (SSRF): the network at 127.0.0.1 is never called.
func TestCXMLOutbound_DefaultClientRefusesLoopback(t *testing.T) {
	f := newFake()
	called := false
	network := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		_, _ = io.WriteString(w, domain.CXMLResponse("r", "2026-11-11T10:00:00Z", 200, "OK"))
	}))
	defer network.Close()
	a, h, _, partner, _ := autoSetup(t, f)
	a.Sources = &exSources{fakeSources: f, po: poFixture()}
	do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/cxml", `{"networkIdentity":"AN-SUPPLIER","sharedSecret":"`+cxmlSecret+`"}`, "edi.admin", srcScope)
	do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/cxml/outbound", `{"outboundUrl":"`+network.URL+`/cxml","ourIdentity":"AN-ME","vendorId":"`+exVendor+`"}`, "edi.admin", srcScope)
	rec := do(t, h, http.MethodPost, "/edi/v1/partners/"+partner+"/cxml:send", `{"documentType":"OrderRequest","sourceId":"`+exPO+`"}`, "edi.operate", srcScope)
	if rec.Code != http.StatusBadGateway || called {
		t.Fatalf("loopback network: %d called=%v %s", rec.Code, called, rec.Body.String())
	}
}

// A secret with XML-special characters is not stored in the clear.
func TestCXMLOutbound_StoredCopyKeepsNoSecret(t *testing.T) {
	f := newFake()
	const secret = "p@ss&word<123>-long-enough"
	var posted string
	network := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		posted = string(raw)
		_, _ = io.WriteString(w, domain.CXMLResponse("r", "2026-11-11T10:00:00Z", 200, "OK"))
	}))
	defer network.Close()
	a, h, pool, partner, _ := autoSetup(t, f)
	a.PunchoutHTTP = network.Client()
	a.Secrets = fakeSecrets{"net-secret": secret}
	a.Sources = &exSources{fakeSources: f, po: poFixture()}
	do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/cxml", `{"networkIdentity":"AN-SUPPLIER","sharedSecret":"`+cxmlSecret+`"}`, "edi.admin", srcScope)
	do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/cxml/outbound", `{"outboundUrl":"`+network.URL+`/cxml","ourIdentity":"AN-ME","outboundSecretRef":"net-secret","vendorId":"`+exVendor+`"}`, "edi.admin", srcScope)
	if rec := do(t, h, http.MethodPost, "/edi/v1/partners/"+partner+"/cxml:send", `{"documentType":"OrderRequest","sourceId":"`+exPO+`"}`, "edi.operate", srcScope); rec.Code != http.StatusCreated {
		t.Fatalf("send: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(posted, "p@ss&amp;word&lt;123&gt;-long-enough") {
		t.Fatalf("the network gets the secret: %s", posted)
	}
	var raw string
	if err := pool.QueryRow(context.Background(), `select raw_payload from interchange where direction = 'OUT' and standard = 'CXML'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "p@ss") || strings.Contains(raw, "word") || strings.Contains(raw, "123") || !strings.Contains(raw, "[redacted]") {
		t.Fatalf("the stored copy holds the secret: %s", raw)
	}
}

type fakeSecrets map[string]string

func (f fakeSecrets) Read(ref string) ([]byte, error) { return []byte(f[ref]), nil }
