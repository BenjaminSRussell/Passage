package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/tools/e2e/stack"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func startStack(t *testing.T) *stack.Stack {
	t.Helper()
	adminDB := os.Getenv("PASSAGE_TEST_DATABASE_URL")
	if adminDB == "" {
		t.Skip("PASSAGE_TEST_DATABASE_URL is not set: the flow test starts the real services against a Postgres database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	t.Cleanup(cancel)
	s, err := stack.New(stack.Config{Root: repoRoot(t), AdminDB: adminDB, DBPrefix: "e2e" + stack.NewKey()[:8], RunDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	if err := s.Start(ctx, true); err != nil {
		for _, name := range s.Names() {
			t.Logf("== %s log\n%s", name, s.LogTail(name))
		}
		t.Fatal(err)
	}
	return s
}

func must(t *testing.T, c stack.Client, method, path string, body any, headers map[string]string, want ...int) stack.Response {
	t.Helper()
	resp, err := c.Must(context.Background(), method, path, body, headers, want...)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func rejected(t *testing.T, c stack.Client, path string, body any, status int) string {
	t.Helper()
	resp, err := c.Do(context.Background(), http.MethodPost, path, body, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != status {
		t.Fatalf("POST %s: status %d, want %d: %s", path, resp.Status, status, resp.Body)
	}
	return resp.String("code")
}

var (
	buyer = stack.Principal{User: "buyer-1", Duties: []string{
		"party.write", "party.read",
		"procurement.purchase-order.maintain", "procurement.purchase-order.inquire",
		"receiving.receipt.maintain", "receiving.receipt.post", "receiving.vendor-invoice.post",
		"ledger.journal.read", "ledger.balance.read",
	}}
	approver = stack.Principal{User: "00000000-0000-0000-0000-0000000000a1", Duties: []string{"procurement.purchase-order.confirm", "procurement.purchase-order.inquire"}}
	clerk    = stack.Principal{User: "clerk-1", Duties: []string{"receiving.receipt.maintain", "ledger.journal.read"}}
)

type trialBalance struct {
	Balanced bool `json:"balanced"`
	Rows     []struct {
		Account string `json:"account"`
		Net     string `json:"net"`
	} `json:"rows"`
}

func (b trialBalance) net(account string) string {
	for _, r := range b.Rows {
		if r.Account == account {
			return r.Net
		}
	}
	return "absent"
}

type entry struct {
	DocumentID string `json:"documentId"`
	Source     struct {
		System string `json:"system"`
		Type   string `json:"type"`
	} `json:"source"`
	Totals struct {
		Debit  string `json:"debit"`
		Credit string `json:"credit"`
	} `json:"totals"`
	Lines []struct {
		Account     string `json:"account"`
		DebitCredit string `json:"debitCredit"`
		Amount      string `json:"amount"`
	} `json:"lines"`
}

func (e entry) amount(account, side string) string {
	for _, l := range e.Lines {
		if l.Account == account && l.DebitCredit == side {
			return l.Amount
		}
	}
	return "absent"
}

func decode[T any](t *testing.T, r stack.Response) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatalf("decode %s: %v", r.Body, err)
	}
	return out
}

func TestVendorPurchaseOrderReceiptInvoiceLedgerFlow(t *testing.T) {
	s := startStack(t)
	party := stack.Client{Base: s.URL("party"), Principal: buyer}
	purchasing := stack.Client{Base: s.URL("purchasing"), Principal: buyer}
	approving := stack.Client{Base: s.URL("purchasing"), Principal: approver}
	receiving := stack.Client{Base: s.URL("receiving"), Principal: buyer}
	clerkReceiving := stack.Client{Base: s.URL("receiving"), Principal: clerk}
	ledger := stack.Client{Base: s.URL("ledger"), Principal: buyer}

	group := must(t, party, http.MethodPost, "/v1/party/vendor-groups", map[string]any{"code": "TRADE", "name": "Trade vendors"}, nil, http.StatusCreated)
	partyRec := must(t, party, http.MethodPost, "/v1/party/parties", map[string]any{"partyType": "organization", "legalName": "Acme Supplies Ltd"}, nil, http.StatusCreated)
	if partyRec.String("partyNumber") != "P-000001" {
		t.Fatalf("party number %q, want the first PARTY sequence value from numbering", partyRec.String("partyNumber"))
	}
	vendor := must(t, party, http.MethodPost, "/v1/party/parties/"+partyRec.String("id")+"/vendor", map[string]any{
		"accountNumber": "V-1000", "vendorGroupId": group.String("id"), "currencyCode": stack.Currency,
	}, nil, http.StatusCreated)
	vendorID := vendor.String("id")
	must(t, party, http.MethodGet, "/v1/party/vendors/"+vendorID, nil, nil, http.StatusOK)

	today := time.Now().UTC().Format("2006-01-02")
	productID := "7d1c0b9e-3a55-4c1b-9d6e-0a1f6a1d0c11"
	if err := stack.GrantPurchaseOrderLimit(context.Background(), s.URL("purchasing"), approver.User); err != nil {
		t.Fatal(err)
	}
	po := must(t, purchasing, http.MethodPost, "/v1/procurement/purchase-orders", map[string]any{
		"legalEntityId": stack.LegalEntity, "vendorId": vendorID, "currency": stack.Currency,
		"lines": []map[string]any{{"productId": productID, "qty": "10", "unit": "EA", "unitPrice": "25.00", "deliveryDate": today}},
	}, nil, http.StatusCreated)
	poID := po.String("id")
	if po.String("approvalStatus") != "DRAFT" {
		t.Fatalf("new purchase order is %s, want DRAFT", po.String("approvalStatus"))
	}
	submitted := must(t, purchasing, http.MethodPost, "/v1/procurement/purchase-orders/"+poID+":submit", nil, map[string]string{"If-Match": po.Header.Get("ETag")}, http.StatusOK)
	approved := must(t, approving, http.MethodPost, "/v1/procurement/purchase-orders/"+poID+":approve", nil, map[string]string{"If-Match": submitted.Header.Get("ETag")}, http.StatusOK)
	confirmed := must(t, approving, http.MethodPost, "/v1/procurement/purchase-orders/"+poID+":confirm", nil, map[string]string{"If-Match": approved.Header.Get("ETag")}, http.StatusOK)
	if confirmed.String("approvalStatus") != "CONFIRMED" {
		t.Fatalf("purchase order is %s, want CONFIRMED", confirmed.String("approvalStatus"))
	}

	receipt := must(t, receiving, http.MethodPost, "/v1/receiving/receipts", map[string]any{
		"vendorId": vendorID, "purchaseOrderId": poID, "legalEntityId": stack.LegalEntity, "packingSlip": "PS-1", "receiptDate": today, "currency": stack.Currency,
		"lines": []map[string]any{{"poLineNo": 1, "productId": productID, "unit": "EA", "qty": "10", "locationId": "5b1f3c2e-7a44-4d0f-8a62-1c9b8d2e4f10"}},
	}, nil, http.StatusCreated)
	receiptID := receipt.String("id")
	posted := must(t, receiving, http.MethodPost, "/v1/receiving/receipts/"+receiptID+":post", map[string]any{}, map[string]string{"If-Match": receipt.Header.Get("ETag")}, http.StatusOK)
	if posted.String("state") != "POSTED" || posted.String("ledgerEntryId") == "" {
		t.Fatalf("receipt after posting: %s", posted.Body)
	}
	receiptEntry := decode[entry](t, must(t, ledger, http.MethodGet, "/v1/ledger/journal-entries/"+posted.String("ledgerEntryId"), nil, nil, http.StatusOK))
	if receiptEntry.amount("1400", "D") != "250.00" || receiptEntry.amount("2100", "C") != "250.00" {
		t.Fatalf("receipt entry should debit inventory and credit GR/IR 250.00: %+v", receiptEntry)
	}

	invoiceBody := func(number, qty string) map[string]any {
		return map[string]any{
			"vendorId": vendorID, "legalEntityId": stack.LegalEntity, "invoiceNumber": number, "invoiceDate": today, "postingDate": today, "currency": stack.Currency,
			"lines": []map[string]any{{"receiptId": receiptID, "receiptLineNo": 1, "qty": qty, "unitPrice": "25.00"}},
		}
	}
	if code := rejected(t, clerkReceiving, "/v1/receiving/invoices:post", invoiceBody("INV-DENIED", "10"), http.StatusForbidden); code != "AUTH-0003" {
		t.Fatalf("a user without receiving.vendor-invoice.post got %s, want AUTH-0003", code)
	}
	if code := rejected(t, receiving, "/v1/receiving/invoices:post", invoiceBody("INV-OVER", "11"), http.StatusUnprocessableEntity); code != "AP-QTY-001" {
		t.Fatalf("invoicing more than was received got %s, want AP-QTY-001", code)
	}
	invoice := must(t, receiving, http.MethodPost, "/v1/receiving/invoices:post", invoiceBody("INV-1001", "10"), nil, http.StatusCreated)
	if invoice.String("status") != "POSTED" || invoice.String("total") != "250.00" || invoice.String("ledgerEntryId") == "" {
		t.Fatalf("invoice after posting: %s", invoice.Body)
	}
	if code := rejected(t, receiving, "/v1/receiving/invoices:post", invoiceBody("inv-1001", "10"), http.StatusConflict); code != "AP-DUP-001" {
		t.Fatalf("a repeated invoice number got %s, want AP-DUP-001", code)
	}

	invoiceEntry := decode[entry](t, must(t, ledger, http.MethodGet, "/v1/ledger/journal-entries/"+invoice.String("ledgerEntryId"), nil, nil, http.StatusOK))
	if invoiceEntry.DocumentID != invoice.String("id") || invoiceEntry.Source.System != "receiving" || invoiceEntry.Source.Type != "vendorinvoice" {
		t.Fatalf("ledger entry does not point back at the invoice: %+v", invoiceEntry)
	}
	if invoiceEntry.amount("2100", "D") != "250.00" || invoiceEntry.amount("2000", "C") != "250.00" || invoiceEntry.Totals.Debit != invoiceEntry.Totals.Credit {
		t.Fatalf("invoice entry should clear GR/IR into vendor control for 250.00: %+v", invoiceEntry)
	}

	tb := decode[trialBalance](t, must(t, ledger, http.MethodGet, "/v1/ledger/balances?ledger="+s.LedgerID, nil, nil, http.StatusOK))
	if !tb.Balanced || tb.net("1400") != "250.00" || tb.net("2000") != "-250.00" || tb.net("2100") != "0.00" {
		t.Fatalf("trial balance after the flow: %+v", tb)
	}
}

func TestSetupRoutesRefuseUsersWithoutTheDuty(t *testing.T) {
	s := startStack(t)
	clerkLedger := stack.Client{Base: s.URL("ledger"), Principal: clerk}
	body := map[string]any{"legalEntityId": stack.LegalEntity, "code": "OTHER", "name": "Other", "chartCode": "OTHER", "currency": stack.Currency}
	if code := rejected(t, clerkLedger, "/v1/ledger/ledgers", body, http.StatusForbidden); code != "AUTH-0003" {
		t.Fatalf("creating a ledger without ledger.ledger.write got %s, want AUTH-0003", code)
	}
	if code := rejected(t, stack.Client{Base: s.URL("ledger")}, "/v1/ledger/ledgers", body, http.StatusUnauthorized); code != "AUTH-0001" {
		t.Fatalf("creating a ledger with no principal got %s, want AUTH-0001", code)
	}
}
