package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/internal/sources"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
	"github.com/BenjaminSRussell/passage/services/edi/migrations"
)

const (
	srcLE       = "00000000-0000-0000-0000-0000000000e1"
	srcCustomer = "00000000-0000-0000-0000-0000000000c1"
	srcOrder    = "00000000-0000-0000-0000-0000000000a1"
	srcShipment = "00000000-0000-0000-0000-0000000000b1"
	srcInvoice  = "00000000-0000-0000-0000-0000000000b2"
)

type fakeSources struct {
	shipment domain.SourceShipment
	invoice  domain.SourceInvoice
	order    domain.SourceOrder
	err      error
	reads    []string
	entities []string
}

func (f *fakeSources) record(kind, le string) {
	f.reads = append(f.reads, kind)
	f.entities = append(f.entities, le)
}

func (f *fakeSources) Shipment(_ context.Context, le, _ string) (domain.SourceShipment, error) {
	f.record("shipment", le)
	return f.shipment, f.err
}

func (f *fakeSources) Invoice(_ context.Context, le, _ string) (domain.SourceInvoice, error) {
	f.record("invoice", le)
	return f.invoice, f.err
}

func (f *fakeSources) Order(_ context.Context, le, _ string) (domain.SourceOrder, error) {
	f.record("order", le)
	return f.order, f.err
}

func newFake() *fakeSources {
	shipped := time.Date(2026, 10, 20, 14, 35, 0, 0, time.UTC)
	return &fakeSources{
		order: domain.SourceOrder{ID: srcOrder, LegalEntityID: srcLE, CustomerID: srcCustomer, ExternalRef: "PO-1001",
			CreatedAt: time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC), Lines: []domain.SourceOrderLine{{ID: "ol-1", LineNo: 10, Unit: "EA"}}},
		shipment: domain.SourceShipment{ID: srcShipment, ShipmentNumber: "SHP-0001", LegalEntityID: srcLE, CustomerID: srcCustomer, OrderID: srcOrder,
			Status: "SHIPPED", ActualShipDate: &shipped,
			Lines: []domain.SourceShipLine{{ID: "sl-1", OrderLineID: "ol-1", ProductID: "prod-a", Unit: "EA"}},
			Containers: []domain.SourceContainer{{SSCC: "003456789012340019", Status: "SHIPPED", GrossKg: decimal.NewFromInt(100),
				Lines: []domain.SourceContainerLine{{ShipmentLineID: "sl-1", Qty: decimal.NewFromInt(10)}}}}},
		invoice: domain.SourceInvoice{ID: srcInvoice, LegalEntityID: srcLE, CustomerID: srcCustomer, Type: "SHIPMENT", Status: "POSTED",
			Number: "INV-1", InvoiceDate: "2026-10-21", PaymentTermDays: 30,
			Totals: domain.SourceInvoiceTotals{Net: decimal.RequireFromString("80"), Tax: decimal.RequireFromString("6.80"), Gross: decimal.RequireFromString("86.80")},
			Lines: []domain.SourceInvoiceLine{{LineNo: 1, Qty: decimal.NewFromInt(10), UnitPrice: decimal.NewFromInt(8), Net: decimal.NewFromInt(80),
				Tax: decimal.RequireFromString("6.80"), OrderID: srcOrder, OrderLineID: "ol-1", ProductID: "prod-a"}}},
	}
}

const profileBody = `{"legalEntityId":"` + srcLE + `","customerId":"` + srcCustomer + `","defaultScac":"ODFL",
"shipFrom":{"name":"Demo Manufacturer","gln":"0614141000012"},"shipTo":{"name":"Demo Distributor DC","gln":"0614141000029"},
"billTo":{"name":"Demo Distributor AP","gln":"0614141000036"},"remitTo":{"name":"Demo Manufacturer AR","gln":"0614141000043"}}`

func sourceHandler(t *testing.T, f *fakeSources) (http.Handler, *pgxpool.Pool, string) {
	t.Helper()
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	a := New(store.New(pool, 30))
	a.Sources = f
	h := a.Routes()
	rec := do(t, h, http.MethodPost, "/edi/v1/partners", distributor, "edi.admin", map[string]string{"Idempotency-Key": "p"})
	var p store.Partner
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || rec.Code != http.StatusCreated {
		t.Fatalf("partner: %d %s", rec.Code, rec.Body.String())
	}
	return h, pool, p.ID
}

// srcScope makes the caller a user of the profile's legal entity: the trade profile is legal-entity scoped (RLS, #486).
var srcScope = map[string]string{tenant.HeaderLegalEntity: srcLE}

func fromSource(t *testing.T, h http.Handler, partner, set, id string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"transactionSet":"` + set + `","sourceId":"` + id + `"}`
	return do(t, h, http.MethodPost, "/edi/v1/partners/"+partner+"/outbound:from-source", body, "edi.operate", srcScope)
}

func putProfile(t *testing.T, h http.Handler, partner, body string) {
	t.Helper()
	if rec := do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/trade-profile", body, "edi.admin", srcScope); rec.Code != http.StatusOK {
		t.Fatalf("profile: %d %s", rec.Code, rec.Body.String())
	}
}

func TestFromSource_856AndIdempotentPerSourceDocument(t *testing.T) {
	f := newFake()
	h, pool, partner := sourceHandler(t, f)
	putProfile(t, h, partner, profileBody)

	first := fromSource(t, h, partner, "856", srcShipment)
	if first.Code != http.StatusCreated {
		t.Fatalf("first: %d %s", first.Code, first.Body.String())
	}
	var res store.OutboundResult
	if err := json.Unmarshal(first.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ST*856*0001", "BSN*00*SHP-0001*20261020*1435", "PRF*PO-1001***20261015", "MAN*GM*003456789012340019", "SN1*10*10*EA", "LIN**VP*prod-a"} {
		if !strings.Contains(res.X12, want) {
			t.Errorf("856 lacks %q: %s", want, res.X12)
		}
	}
	if strings.Join(f.reads, ",") != "shipment,order" || f.entities[0] != srcLE || f.entities[1] != srcLE {
		t.Fatalf("reads must use the profile legal entity: %v %v", f.reads, f.entities)
	}

	again := fromSource(t, h, partner, "856", srcShipment)
	var replay store.OutboundResult
	if err := json.Unmarshal(again.Body.Bytes(), &replay); err != nil || again.Code != http.StatusOK || replay.InterchangeID != res.InterchangeID || replay.ControlNumber != res.ControlNumber {
		t.Fatalf("a repeat returns the recorded interchange: %d %s", again.Code, again.Body.String())
	}
	var interchanges, last int
	if err := pool.QueryRow(context.Background(), `select count(*), (select max(last_value) from control_number_counter) from interchange where direction = 'OUT'`).Scan(&interchanges, &last); err != nil {
		t.Fatal(err)
	}
	if interchanges != 1 || last != 1 {
		t.Fatalf("one interchange and one control number per source document: %d %d", interchanges, last)
	}

	f.shipment.ShipmentNumber = "SHP-0001B"
	if changed := fromSource(t, h, partner, "856", srcShipment); changed.Code != http.StatusUnprocessableEntity || problemCode(t, changed) != CodeIdempotencyReuse {
		t.Fatalf("a source changed after sending is refused: %d %s", changed.Code, changed.Body.String())
	}
}

func TestFromSource_810TakesTotalFromBilling(t *testing.T) {
	f := newFake()
	h, _, partner := sourceHandler(t, f)
	putProfile(t, h, partner, profileBody)
	rec := fromSource(t, h, partner, "810", srcInvoice)
	if rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{"BIG*20261021*INV-1*20261015*PO-1001***DI", "IT1*1*10*EA*8**VP*prod-a", "TDS*8680", "TXI*ST*6.80*8.5"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("810 lacks %q: %s", want, rec.Body.String())
		}
	}
	if strings.Join(f.reads, ",") != "invoice,order" {
		t.Fatalf("reads: %v", f.reads)
	}
}

// The same sources give the EDIFACT DESADV and INVOIC for a partner with a
// UNB identity, on its EDIFACT counter, idempotent per source document and
// message type.
func TestFromSource_DESADVAndINVOICForAnEdifactPartner(t *testing.T) {
	f := newFake()
	h, pool, partner := sourceHandler(t, f)
	putProfile(t, h, partner, profileBody)
	if rec := fromSource(t, h, partner, "DESADV", srcShipment); rec.Code != http.StatusConflict || problemCode(t, rec) != CodeNoEdifactID {
		t.Fatalf("without an EDIFACT identity: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := pool.Exec(context.Background(), `update trading_partner set edifact_id = '4012345000009', edifact_qualifier = '14', edifact_contrl = 'REQUESTED'`); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ set, id, control, want string }{
		{"DESADV", srcShipment, "1", "UNH+1+DESADV:D:96A:UN'BGM+351+SHP-0001+9'"},
		{"INVOIC", srcInvoice, "2", "UNH+1+INVOIC:D:96A:UN'BGM+380+INV-1+9'"},
		{"DESADV", srcShipment, "1", "UNH+1+DESADV:D:96A:UN'"}, // repeat: the recorded interchange
	} {
		rec := fromSource(t, h, partner, c.set, c.id)
		var res store.OutboundResult
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || (rec.Code != http.StatusCreated && rec.Code != http.StatusOK) ||
			res.Standard != "EDIFACT" || res.ControlNumber != c.control || !strings.Contains(res.Edifact, c.want) {
			t.Fatalf("%s: %d %s", c.set, rec.Code, rec.Body.String())
		}
	}
	// The X12 counter is untouched; the 856 still takes ISA13 1.
	if rec := fromSource(t, h, partner, "856", srcShipment); rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"controlNumber":"000000001"`) {
		t.Fatalf("856: %d %s", rec.Code, rec.Body.String())
	}
	if rec := fromSource(t, h, partner, "ORDERS", srcShipment); rec.Code != http.StatusBadRequest {
		t.Fatalf("ORDERS has no source: %d %s", rec.Code, rec.Body.String())
	}
}

func TestFromSource_RefusalsAndErrors(t *testing.T) {
	other := "00000000-0000-0000-0000-0000000000ff"
	cases := []struct {
		name   string
		mutate func(*fakeSources)
		set    string
		id     string
		status int
		code   string
	}{
		{"shipment of another legal entity", func(f *fakeSources) { f.shipment.LegalEntityID = other }, "856", srcShipment, 422, CodeSourceScope},
		{"invoice of another customer", func(f *fakeSources) { f.invoice.CustomerID = other }, "810", srcInvoice, 422, CodeSourceScope},
		{"shipment not shipped", func(f *fakeSources) { f.shipment.Status = "PACKED" }, "856", srcShipment, 409, CodeSourceNotFinal},
		{"invoice not posted", func(f *fakeSources) { f.invoice.Status = "PROPOSAL" }, "810", srcInvoice, 409, CodeSourceNotFinal},
		{"shipment partially shipped", func(f *fakeSources) { f.shipment.Status = "PARTIALLY_SHIPPED" }, "856", srcShipment, 422, CodeOutbound},
		{"invoice without lines", func(f *fakeSources) { f.invoice.Lines = nil }, "810", srcInvoice, 422, CodeOutbound},
		{"credit note", func(f *fakeSources) { f.invoice.Type = "CREDIT_NOTE" }, "810", srcInvoice, 422, CodeOutbound},
		{"credit note of another customer", func(f *fakeSources) { f.invoice.Type = "CREDIT_NOTE"; f.invoice.CustomerID = other }, "810", srcInvoice, 422, CodeSourceScope},
		{"draft invoice without lines", func(f *fakeSources) { f.invoice.Status = "PROPOSAL"; f.invoice.Lines = nil }, "810", srcInvoice, 409, CodeSourceNotFinal},
		{"invoice without sales order", func(f *fakeSources) { f.invoice.Lines[0].OrderID = "" }, "810", srcInvoice, 422, CodeOutbound},
		{"source not found", func(f *fakeSources) { f.err = sources.ErrNotFound }, "856", srcShipment, 404, CodeNotFound},
		{"source refuses the grant", func(f *fakeSources) { f.err = sources.ErrForbidden }, "810", srcInvoice, 403, CodeSourceForbidden},
		{"source down", func(f *fakeSources) { f.err = context.DeadlineExceeded }, "810", srcInvoice, 502, CodeSourceDown},
		{"set not sourced", func(*fakeSources) {}, "855", srcShipment, 400, CodeValidation},
		{"source id not a uuid", func(*fakeSources) {}, "856", "SHP-1", 400, CodeValidation},
	}
	for _, c := range cases {
		f := newFake()
		c.mutate(f)
		h, pool, partner := sourceHandler(t, f)
		putProfile(t, h, partner, profileBody)
		rec := fromSource(t, h, partner, c.set, c.id)
		if rec.Code != c.status || problemCode(t, rec) != c.code {
			t.Errorf("%s: %d %s", c.name, rec.Code, rec.Body.String())
		}
		if c.code == CodeSourceScope || c.code == CodeSourceNotFinal {
			for _, kind := range f.reads {
				if kind == "order" {
					t.Errorf("%s: the sales order must not be read before the source document passes scope and status", c.name)
				}
			}
			if c.code == CodeSourceScope && (strings.Contains(rec.Body.String(), "INV-") || strings.Contains(rec.Body.String(), "SHP-0")) {
				t.Errorf("%s: refusal names the source document: %s", c.name, rec.Body.String())
			}
		}
		var n int
		if err := pool.QueryRow(context.Background(), `select count(*) from interchange`).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s: a refused source records nothing: %d %v", c.name, n, err)
		}
	}
}

func TestFromSource_NeedsProfileSourcesAndOptInBodyRoute(t *testing.T) {
	f := newFake()
	h, _, partner := sourceHandler(t, f)
	if rec := fromSource(t, h, partner, "856", srcShipment); rec.Code != http.StatusConflict || problemCode(t, rec) != CodeNoTradeProfile || len(f.reads) != 0 {
		t.Fatalf("no profile: %d %s reads=%v", rec.Code, rec.Body.String(), f.reads)
	}
	if rec := do(t, h, http.MethodPost, "/edi/v1/partners/"+partner+"/outbound", `{"transactionSet":"856","document":{}}`, "edi.operate", map[string]string{"Idempotency-Key": "k"}); rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("the request-body route is off by default: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, "/edi/v1/partners/"+partner+"/outbound:from-source", `{"transactionSet":"856","sourceId":"`+srcShipment+`"}`, "edi.admin", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("edi.operate is required: %d", rec.Code)
	}

	bare := New(store.New(testkit.DB(t), 30))
	if rec := do(t, bare.Routes(), http.MethodPost, "/edi/v1/partners/"+partner+"/outbound:from-source", `{"transactionSet":"856","sourceId":"`+srcShipment+`"}`, "edi.operate", nil); rec.Code != http.StatusServiceUnavailable || problemCode(t, rec) != CodeSourceDown {
		t.Fatalf("no source services configured: %d %s", rec.Code, rec.Body.String())
	}
}

func TestTradeProfile_ValidatesAndIsPerTenant(t *testing.T) {
	h, _, partner := sourceHandler(t, newFake())
	if rec := do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/trade-profile", strings.Replace(profileBody, "0614141000012", "12", 1), "edi.admin", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad GLN: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/trade-profile", strings.Replace(profileBody, srcLE, "00000000-0000-0000-0000-0000000000e2", 1), "edi.admin", srcScope); rec.Code != http.StatusBadRequest {
		t.Fatalf("legal entity outside the caller's scope: %d %s", rec.Code, rec.Body.String())
	}
	missing := "00000000-0000-0000-0000-000000000099"
	if rec := do(t, h, http.MethodPut, "/edi/v1/partners/"+missing+"/trade-profile", profileBody, "edi.admin", srcScope); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown partner: %d", rec.Code)
	}
	putProfile(t, h, partner, profileBody)
	putProfile(t, h, partner, strings.Replace(profileBody, "ODFL", "UPSN", 1))
}
