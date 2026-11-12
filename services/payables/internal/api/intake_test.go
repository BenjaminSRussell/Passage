package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/payables/internal/api"
	"github.com/BenjaminSRussell/passage/services/payables/internal/store"
	"github.com/BenjaminSRussell/passage/services/payables/migrations"
)

const intakeVendor = "0b1c2d3e-0000-4000-8000-0000000000cc"

func intakeBody(sourceRef, number, gross string) string {
	return `{"legalEntityId":"` + entityA + `","source":"CXML","sourceRef":"` + sourceRef + `","vendorId":"` + intakeVendor + `",
	"invoiceNumber":"` + number + `","invoiceDate":"2026-11-09","purchaseOrderId":"11111111-1111-4111-8111-111111111111","purchaseOrderNumber":"PO-1001",
	"currency":"USD","gross":"` + gross + `","lines":[{"lineNo":"1","item":"PUMP-SEAL","quantity":"4","unit":"EA","unitPrice":"12.50"},
	{"lineNo":"2","quantity":"1","unitPrice":"0.333"}]}`
}

// #35 (35-4): a cXML invoice received by edi is stored once per document, a resend replays, a changed resend and a second document
// with the vendor's same invoice number are refused, and an AP clerk reads it back with open-item.read.
func TestVendorInvoiceIntake_RealStore(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	r := httpx.NewRouter()
	api.New(fakeDB{}, fakeValidator{}, fixedNow).WithVendorPayments(store.New(app)).Register(r)
	edi := caller{duties: "payables.vendor-invoice.intake", user: "edi-cxml", entity: entityA}
	rec := edi.send(r, http.MethodPost, "/v1/payables/vendor-invoice-intake", intakeBody("msg-1", "INV-77", "54.33"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("intake: %d %s", rec.Code, rec.Body)
	}
	got := decode(t, rec)
	if got["net"] != "50.33" || got["gross"] != "54.33" || got["status"] != "RECEIVED" || got["receivedBy"] != "edi-cxml" {
		t.Fatalf("stored %v", got)
	}
	id := got["id"].(string)
	if rec := edi.send(r, http.MethodPost, "/v1/payables/vendor-invoice-intake", intakeBody("msg-1", "INV-77", "54.33")); rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("resend: %d %s", rec.Code, rec.Body)
	}
	wantProblem(t, edi.send(r, http.MethodPost, "/v1/payables/vendor-invoice-intake", intakeBody("msg-1", "INV-77", "60")), http.StatusConflict, api.CodeIntakeReplay)
	wantProblem(t, edi.send(r, http.MethodPost, "/v1/payables/vendor-invoice-intake", intakeBody("msg-2", "INV-77", "54.33")), http.StatusConflict, api.CodeIntakeDuplicate)

	clerk := caller{duties: "open-item.read", user: "ap-clerk", entity: entityA}
	if rec := clerk.send(r, http.MethodGet, "/v1/payables/vendor-invoice-intake/"+id, ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "PUMP-SEAL") {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}
	list := clerk.send(r, http.MethodGet, "/v1/payables/vendor-invoice-intake?vendorId="+intakeVendor, "")
	if list.Code != http.StatusOK || strings.Count(list.Body.String(), `"invoiceNumber"`) != 1 {
		t.Fatalf("list: %d %s", list.Code, list.Body)
	}
	other := caller{duties: "open-item.read", user: "ap-clerk", entity: entityB}
	wantProblem(t, other.send(r, http.MethodGet, "/v1/payables/vendor-invoice-intake/"+id, ""), http.StatusNotFound, "COM-0404")
	wantProblem(t, clerk.send(r, http.MethodPost, "/v1/payables/vendor-invoice-intake", intakeBody("msg-3", "INV-78", "1")), http.StatusForbidden, "AUTH-0003")
}

func TestVendorInvoiceIntake_Validation(t *testing.T) {
	r := httpx.NewRouter()
	api.New(fakeDB{}, fakeValidator{}, fixedNow).WithVendorInvoiceIntake(&store.Postgres{}).Register(r)
	edi := caller{duties: "payables.vendor-invoice.intake", user: "edi-cxml", entity: entityA}
	cases := map[string]string{
		"source":             strings.Replace(intakeBody("m", "I", "1"), `"CXML"`, `"EDI"`, 1),
		"vendorId":           strings.Replace(intakeBody("m", "I", "1"), intakeVendor, "v1", 1),
		"invoiceDate":        strings.Replace(intakeBody("m", "I", "1"), "2026-11-09", "09.11.2026", 1),
		"currency":           strings.Replace(intakeBody("m", "I", "1"), `"USD"`, `"usd"`, 1),
		"lines[0].quantity":  strings.Replace(intakeBody("m", "I", "1"), `"quantity":"4"`, `"quantity":"0"`, 1),
		"lines[1].unitPrice": strings.Replace(intakeBody("m", "I", "1"), `"unitPrice":"0.333"`, `"unitPrice":"-1"`, 1),
		"purpose":            strings.Replace(intakeBody("m", "I", "1"), `"currency"`, `"purpose":"refund","currency"`, 1),
	}
	for field, body := range cases {
		t.Run(field, func(t *testing.T) {
			got := wantProblem(t, edi.send(r, http.MethodPost, "/v1/payables/vendor-invoice-intake", body), http.StatusBadRequest, "COM-0001")
			errs, _ := got["errors"].(map[string]any)
			if _, ok := errs[field]; !ok {
				t.Fatalf("no error for %s: %v", field, got)
			}
		})
	}
}
