package purchasing

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

const (
	tenantID      = "00000000-0000-0000-0000-000000000001"
	legalEntityID = "00000000-0000-0000-0000-0000000000e1"
	orderID       = "00000000-0000-4000-8000-0000000000b1"
	userID        = "00000000-0000-4000-8000-0000000000c1"
)

const orderJSON = `{"id":"` + orderID + `","number":"PO-1","legalEntityId":"` + legalEntityID + `","vendorId":"v-1","currency":"USD","approvalStatus":"APPROVED","documentStatus":"CONFIRMED","versionNo":1,"rowVersion":3,
"lines":[{"lineNo":1,"productId":"p-1","unit":"EA","qty":"10","unitPrice":"250.00","priceUnit":"100","discountPct":"10","grossAmount":"25.00","discountAmount":"2.50","lineAmount":"22.50","taxAmount":"0.00","deliveryDate":"2026-09-01","priceSource":"MANUAL","receivedQty":"4","invoicedQty":"1","status":"OPEN"}]}`

func testCtx() context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, LegalEntityID: legalEntityID, UserID: userID, Duties: map[string]struct{}{"receiving.receipt.post": {}, "procurement.purchase-order.inquire": {}}})
}

func serve(t *testing.T, status int, body string) (*Client, *http.Request) {
	t.Helper()
	var got http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = *r.Clone(context.Background())
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, nil), &got
}

func TestGet_MapsOrderAndForwardsIdentity(t *testing.T) {
	client, got := serve(t, http.StatusOK, orderJSON)
	ctx := middlewareCtx(testCtx(), "req-7")
	po, err := client.Get(ctx, legalEntityID, orderID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Method != http.MethodGet || got.URL.Path != "/v1/procurement/purchase-orders/"+orderID {
		t.Fatalf("request %s %s", got.Method, got.URL.Path)
	}
	if got.Header.Get("X-Passage-Tenant") != tenantID || got.Header.Get("X-Passage-Legal-Entity") != legalEntityID || got.Header.Get("X-Passage-User") != userID ||
		got.Header.Get("X-Passage-Duties") != "procurement.purchase-order.inquire,receiving.receipt.post" || got.Header.Get("X-Request-Id") != "req-7" {
		t.Fatalf("headers %v", got.Header)
	}
	if po.ID != orderID || po.VendorID != "v-1" || po.Currency != "USD" || po.LegalEntityID != legalEntityID || po.ApprovalStatus != "APPROVED" || po.DocumentStatus != "CONFIRMED" || len(po.Lines) != 1 {
		t.Fatalf("order %+v", po)
	}
	l := po.Lines[0]
	if l.LineNo != 1 || l.ProductID != "p-1" || l.Unit != "EA" || l.Qty.String() != "10" || l.UnitPrice.String() != "250" || l.PriceUnit.String() != "100" || l.DiscountPct.String() != "10" || l.ReceivedQty.String() != "4" || l.InvoicedQty.String() != "1" || l.Status != "OPEN" {
		t.Fatalf("line %+v", l)
	}
	if l.NetUnitPrice().String() != "2.25" || l.QtyRemaining().String() != "9" {
		t.Fatalf("net unit price %s, remaining %s", l.NetUnitPrice(), l.QtyRemaining())
	}
}

func TestGet_RefusesAnOrderOfAnotherLegalEntity(t *testing.T) {
	client, _ := serve(t, http.StatusOK, strings.Replace(orderJSON, legalEntityID, "00000000-0000-0000-0000-0000000000e2", 1))
	if _, err := client.Get(testCtx(), legalEntityID, orderID); !errors.Is(err, domain.ErrPurchaseOrderForbidden) {
		t.Fatalf("err %v", err)
	}
}

func TestGet_LegalEntityScopeFailsClosed(t *testing.T) {
	other := "00000000-0000-0000-0000-0000000000e2"
	tests := []struct {
		name, scope string
		duties      map[string]struct{}
		allowed     bool
	}{
		{"star claim", "*", map[string]struct{}{tenant.DutyAllScope: {}}, false},
		{"empty scope without all-scope duty", "", nil, false},
		{"empty scope with all-scope duty", "", map[string]struct{}{tenant.DutyAllScope: {}}, true},
		{"list including the order entity", other + ", " + legalEntityID, nil, true},
		{"list excluding the order entity", other + ",00000000-0000-0000-0000-0000000000e3", nil, false},
		{"exact match", legalEntityID, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := serve(t, http.StatusOK, orderJSON)
			ctx := tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, LegalEntityID: tc.scope, Duties: tc.duties})
			_, err := client.Get(ctx, legalEntityID, orderID)
			if tc.allowed != (err == nil) || (!tc.allowed && !errors.Is(err, domain.ErrPurchaseOrderForbidden)) {
				t.Fatalf("scope %q: err %v", tc.scope, err)
			}
		})
	}
}

func TestGet_ForbiddenIsNotAnOutage(t *testing.T) {
	client, _ := serve(t, http.StatusForbidden, `{"code":"AUTH-0003"}`)
	if _, err := client.Get(testCtx(), legalEntityID, orderID); !errors.Is(err, domain.ErrPurchaseOrderForbidden) {
		t.Fatalf("err %v", err)
	}
}

func TestGet_OmitsLegalEntityHeaderWhenPrincipalHasNone(t *testing.T) {
	client, got := serve(t, http.StatusOK, orderJSON)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: tenantID})
	if _, err := client.Get(ctx, legalEntityID, orderID); !errors.Is(err, domain.ErrPurchaseOrderForbidden) {
		t.Fatalf("err %v", err)
	}
	for _, header := range []string{"X-Passage-Legal-Entity", "X-Passage-User", "X-Passage-Duties"} {
		if _, present := got.Header[header]; present {
			t.Fatalf("%s sent without a value: %v", header, got.Header)
		}
	}
}

func TestGet_UnknownOrderIsNotFound(t *testing.T) {
	client, _ := serve(t, http.StatusNotFound, `{"code":"PRC-PO-404"}`)
	_, err := client.Get(testCtx(), legalEntityID, orderID)
	if !errors.Is(err, domain.ErrPurchaseOrderNotFound) {
		t.Fatalf("err %v", err)
	}
}

func TestGet_Failures(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"server error", http.StatusInternalServerError, `{}`},
		{"unavailable", http.StatusServiceUnavailable, ``},
		{"bad request", http.StatusBadRequest, `{"code":"COM-0001"}`},
		{"truncated at 1 MiB", http.StatusOK, `{"id":"x","number":"` + strings.Repeat("a", 1<<20) + `"}`},
		{"discount above 100", http.StatusOK, `{"id":"x","lines":[{"lineNo":1,"qty":"1","unitPrice":"1","priceUnit":"1","discountPct":"101"}]}`},
		{"negative discount", http.StatusOK, `{"id":"x","lines":[{"lineNo":1,"qty":"1","unitPrice":"1","priceUnit":"1","discountPct":"-1"}]}`},
		{"line without price unit", http.StatusOK, `{"id":"x","lines":[{"lineNo":1,"qty":"1","unitPrice":"1"}]}`},
		{"empty 200", http.StatusOK, ``},
		{"non-json 200", http.StatusOK, `<html>`},
		{"200 without id", http.StatusOK, `{"lines":[]}`},
		{"200 bad decimal", http.StatusOK, `{"id":"x","lines":[{"qty":"abc"}]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := serve(t, tc.status, tc.body)
			_, err := client.Get(testCtx(), legalEntityID, orderID)
			if err == nil || errors.Is(err, domain.ErrPurchaseOrderNotFound) {
				t.Fatalf("err %v", err)
			}
		})
	}
}

func TestGet_TimeoutIsNotNotFound(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(func() { close(release); srv.Close() })
	_, err := New(srv.URL, &http.Client{Timeout: 50 * time.Millisecond}).Get(testCtx(), legalEntityID, orderID)
	if err == nil || errors.Is(err, domain.ErrPurchaseOrderNotFound) {
		t.Fatalf("err %v", err)
	}
}

func TestGet_NoPrincipalSendsNothing(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer srv.Close()
	if _, err := New(srv.URL, nil).Get(context.Background(), legalEntityID, orderID); err == nil || called {
		t.Fatalf("err %v called %v", err, called)
	}
}

func TestGet_EscapesTheOrderID(t *testing.T) {
	client, got := serve(t, http.StatusNotFound, `{}`)
	_, _ = client.Get(testCtx(), legalEntityID, "a/b")
	if got.URL.EscapedPath() != "/v1/procurement/purchase-orders/a%2Fb" {
		t.Fatalf("path %s", got.URL.EscapedPath())
	}
}

func TestNew_DefaultsToTwoSecondTimeout(t *testing.T) {
	if got := New("http://purchasing", nil).http.Timeout; got != 2*time.Second {
		t.Fatalf("timeout %v", got)
	}
}

func middlewareCtx(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, middleware.RequestIDKey, id)
}

func TestPathMatchesThePurchasingRouteTable(t *testing.T) {
	src, err := os.ReadFile("../../../purchasing/internal/api/api.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`r.Route("/v1/procurement"`, `lookup.Get("/purchase-orders/{id}", a.getOrder)`, `lookup := r.With(authz.RequireAnyDuty(dutyPurchaseOrderInquire, dutyPurchaseOrderLookup))`, `dutyPurchaseOrderLookup    = "` + dutyLookup + `"`} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("purchasing route table no longer contains %s; update domain.PurchaseOrderPathTmpl", want)
		}
	}
	if domain.PurchaseOrderPathTmpl != "/v1/procurement/purchase-orders/%s" {
		t.Fatalf("path %s does not match the purchasing route", domain.PurchaseOrderPathTmpl)
	}
}

func TestGet_DoesNotFollowRedirectsWithTheCallersIdentity(t *testing.T) {
	followed := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed = true }))
	t.Cleanup(target.Close)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)

	_, err := New(redirector.URL, nil).Get(testCtx(), legalEntityID, orderID)

	if err == nil || followed {
		t.Fatalf("redirect followed %v, err %v", followed, err)
	}
}

func signedLookup(t *testing.T, status int, body string) (*Client, *http.Request, *tenant.ServiceVerifier) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer, err := tenant.NewServiceSigner("receiving", priv)
	if err != nil {
		t.Fatal(err)
	}
	client, got := serve(t, status, body)
	return client.WithSigner(signer), got, tenant.NewServiceVerifier("purchasing", map[string]ed25519.PublicKey{"receiving": pub})
}

func scopedCtx(scope string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, LegalEntityID: scope, UserID: userID, Duties: map[string]struct{}{"receiving.receipt.post": {}}})
}

func TestSigned_OrderAndPlanReadsCarryOnlyTheLookupDutyAndNoUserHeaders(t *testing.T) {
	multi := scopedCtx(legalEntityID + ",00000000-0000-0000-0000-0000000000e2")
	for name, call := range map[string]func(*Client) error{
		"order": func(c *Client) error { _, err := c.Get(multi, legalEntityID, orderID); return err },
		"plans": func(c *Client) error { _, err := c.List(multi, legalEntityID, orderID); return err },
	} {
		client, got, verifier := signedLookup(t, http.StatusOK, orderJSON)
		if err := call(client); name == "order" && err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, h := range []string{tenant.HeaderTenant, tenant.HeaderLegalEntity, tenant.HeaderUser, tenant.HeaderDuties} {
			if got.Header.Get(h) != "" {
				t.Errorf("%s: %s must not be sent with a service token", name, h)
			}
		}
		claims, err := verifier.Verify(got.Header.Get(tenant.HeaderServiceToken))
		if err != nil || claims.Aud != "purchasing" || claims.Act != userID || claims.Le != legalEntityID || len(claims.Duties) != 1 || claims.Duties[0] != dutyLookup {
			t.Fatalf("%s: claims %+v err %v", name, claims, err)
		}
	}
}

func TestSigned_RefusesAnEntityOutsideTheUsersScope(t *testing.T) {
	client, got, _ := signedLookup(t, http.StatusOK, orderJSON)

	_, getErr := client.Get(scopedCtx(legalEntityID), "00000000-0000-0000-0000-0000000000e2", orderID)
	_, listErr := client.List(scopedCtx(legalEntityID), "00000000-0000-0000-0000-0000000000e2", orderID)

	if getErr == nil || listErr == nil || got.Method != "" {
		t.Fatalf("get %v list %v method %q: a token for an entity the user cannot act in must never be sent", getErr, listErr, got.Method)
	}
}

func TestSigned_UnknownEntityForAMultiEntityUserFallsBackToForwardingTheUser(t *testing.T) {
	client, got, _ := signedLookup(t, http.StatusOK, orderJSON)
	ctx := scopedCtx(legalEntityID + ",00000000-0000-0000-0000-0000000000e2")

	if _, err := client.Get(ctx, "", orderID); err != nil {
		t.Fatal(err)
	}
	if got.Header.Get(tenant.HeaderServiceToken) != "" || got.Header.Get(tenant.HeaderUser) != userID {
		t.Fatalf("evaluate has no entity to sign for; it forwards the user as before: %v", got.Header)
	}
}

func TestSigned_SingleEntityUserWithoutAnEntityArgumentStillSigns(t *testing.T) {
	client, got, verifier := signedLookup(t, http.StatusOK, orderJSON)

	if _, err := client.Get(scopedCtx(legalEntityID), "", orderID); err != nil {
		t.Fatal(err)
	}
	if claims, err := verifier.Verify(got.Header.Get(tenant.HeaderServiceToken)); err != nil || claims.Le != legalEntityID {
		t.Fatalf("claims %+v err %v", claims, err)
	}
}
