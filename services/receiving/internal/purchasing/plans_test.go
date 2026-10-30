package purchasing

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

const plansJSON = `{"items":[
{"purchaseOrderId":"` + orderID + `","lineNo":1,"kind":"PERIODIC","settleWithoutReceipt":true,"lineAmount":"3000.00","currency":"USD","updatedAt":"2026-09-01T00:00:00Z",
 "rows":[{"seq":1,"dueDate":"2026-09-01","amount":"1000.00","status":"INVOICED"},{"seq":2,"dueDate":"2026-10-01","amount":"1000.50","status":"OPEN"}]},
{"purchaseOrderId":"` + orderID + `","lineNo":2,"kind":"PARTIAL","settleWithoutReceipt":false,"lineAmount":"10.00","currency":"USD",
 "rows":[{"seq":1,"dueDate":"2026-12-31","amount":"10.00","percent":"100","status":"OPEN"}]}]}`

func TestList_MapsPlansAndForwardsIdentity(t *testing.T) {
	client, got := serve(t, http.StatusOK, plansJSON)

	plans, err := client.List(middlewareCtx(testCtx(), "req-9"), orderID)

	if err != nil {
		t.Fatal(err)
	}
	if got.Method != http.MethodGet || got.URL.Path != "/v1/procurement/purchase-orders/"+orderID+"/invoicing-plans" {
		t.Fatalf("request %s %s", got.Method, got.URL.Path)
	}
	if got.Header.Get("X-Passage-Tenant") != tenantID || got.Header.Get("X-Passage-Legal-Entity") != legalEntityID || got.Header.Get("X-Request-Id") != "req-9" {
		t.Fatalf("headers %v", got.Header)
	}
	if len(plans) != 2 || plans[0].LineNo != 1 || !plans[0].SettleWithoutReceipt || plans[0].Currency != "USD" || plans[1].SettleWithoutReceipt {
		t.Fatalf("plans %+v", plans)
	}
	row := plans[0].Rows[1]
	if row.Seq != 2 || row.DueDate.Format("2006-01-02") != "2026-10-01" || row.Amount.String() != "1000.5" || row.Status != domain.PlanRowOpen || plans[0].Rows[0].Status != domain.PlanRowInvoiced {
		t.Fatalf("row %+v", row)
	}
}

func TestList_MapsStatusesAndRefusesUnusableBodies(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		is     error
	}{
		{"not found", http.StatusNotFound, `{}`, domain.ErrPurchaseOrderNotFound},
		{"forbidden", http.StatusForbidden, `{}`, domain.ErrPurchaseOrderForbidden},
		{"server error", http.StatusInternalServerError, `{}`, nil},
		{"not json", http.StatusOK, `<html>`, nil},
		{"bad due date", http.StatusOK, `{"items":[{"lineNo":1,"rows":[{"seq":1,"dueDate":"soon","amount":"1","status":"OPEN"}]}]}`, nil},
		{"row without a sequence", http.StatusOK, `{"items":[{"lineNo":1,"rows":[{"seq":0,"dueDate":"2026-01-01","amount":"1","status":"OPEN"}]}]}`, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := serve(t, tc.status, tc.body)

			plans, err := client.List(testCtx(), orderID)

			if err == nil || plans != nil || (tc.is != nil && !errors.Is(err, tc.is)) {
				t.Fatalf("plans %+v err %v", plans, err)
			}
		})
	}
}

func TestList_NeedsAPrincipal(t *testing.T) {
	client, _ := serve(t, http.StatusOK, plansJSON)

	if _, err := client.List(t.Context(), orderID); err == nil {
		t.Fatal("a list without a principal must not call purchasing")
	}
}

func TestPlansPathMatchesThePurchasingRouteTable(t *testing.T) {
	src, err := os.ReadFile("../../../purchasing/internal/api/api.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `inquire.Get("/purchase-orders/{id}/invoicing-plans", a.listInvoicingPlans)`) {
		t.Fatal("purchasing route table no longer lists invoicing plans; update domain.InvoicingPlansPathTmpl")
	}
	if domain.InvoicingPlansPathTmpl != "/v1/procurement/purchase-orders/%s/invoicing-plans" {
		t.Fatalf("path %s does not match the purchasing route", domain.InvoicingPlansPathTmpl)
	}
}
