package stack

import (
	"context"
	"fmt"
	"net/http"
)

const (
	ChartCode  = "PASSAGE"
	LedgerCode = "PASSAGE"
	Currency   = "USD"
)

// Posting accounts receiving uses by default under PASSAGE_ALLOW_FAKES=1 (services/receiving/cmd/receiving/ports.go).
var seedAccounts = []struct{ Number, Name, Type string }{
	{"1400", "Inventory", "asset"},
	{"1570", "Input tax", "asset"},
	{"2000", "Vendor control", "liability"},
	{"2100", "Goods received not invoiced", "liability"},
	{"5200", "Purchase price variance", "expense"},
	{"6000", "Purchases expense", "expense"},
}

func (s *Stack) existingLedgerID() (string, error) {
	return s.queryOne("ledger", fmt.Sprintf(`SELECT id FROM ledger WHERE tenant_id = '%s' AND code = '%s'`, Tenant, LedgerCode))
}

// SeedLedger creates the ledger and chart accounts receiving posts to and returns the ledger id. It is safe to call
// again: an existing ledger is found in the ledger database itself.
func (s *Stack) SeedLedger(ctx context.Context) (string, error) {
	id, err := s.existingLedgerID()
	if err != nil {
		return "", err
	}
	admin := Client{Base: s.URL("ledger"), Principal: Principal{User: "seed", Duties: []string{"ledger.ledger.write", "ledger.account.write"}}}
	if id == "" {
		resp, err := admin.Must(ctx, http.MethodPost, "/v1/ledger/ledgers", map[string]any{
			"legalEntityId": LegalEntity, "code": LedgerCode, "name": "Passage dev ledger", "chartCode": ChartCode, "currency": Currency,
		}, nil, http.StatusCreated)
		if err != nil {
			return "", err
		}
		id = resp.String("id")
	}
	for _, a := range seedAccounts {
		if _, err := admin.Must(ctx, http.MethodPut, "/v1/ledger/charts/"+ChartCode+"/accounts/"+a.Number, map[string]any{"name": a.Name, "accountType": a.Type}, nil, http.StatusCreated, http.StatusPreconditionRequired); err != nil {
			return "", err
		}
	}
	return id, nil
}

// SeedNumbering creates the PARTY sequence party allocates its party numbers from.
func (s *Stack) SeedNumbering(ctx context.Context) error {
	admin := Client{Base: s.URL("numbering"), Principal: Principal{User: "seed", Duties: []string{"numbering.write"}}}
	resp, err := admin.Do(ctx, http.MethodPost, "/v1/numbering/sequences", map[string]any{
		"code": "PARTY", "name": "Party numbers", "format": "P-{######}", "legalEntityId": LegalEntity, "mode": "continuous",
	}, nil)
	if err != nil {
		return err
	}
	if resp.Status != http.StatusCreated && resp.Status != http.StatusConflict {
		return fmt.Errorf("create sequence PARTY: status %d: %s", resp.Status, resp.Body)
	}
	return nil
}

// GrantPurchaseOrderLimit gives worker a purchase order signing limit in USD unless purchasing already lists one.
// Purchasing refuses approval from anyone without one, and a limit is only written when a second person approves the
// request.
func GrantPurchaseOrderLimit(ctx context.Context, purchasingURL, worker string) error {
	requester := Client{Base: purchasingURL, Principal: Principal{User: "00000000-0000-0000-0000-0000000000b1", Duties: []string{"procurement.requisition.create", "procurement.purchase-order.inquire"}}}
	approver := Client{Base: purchasingURL, Principal: Principal{User: "00000000-0000-0000-0000-0000000000b2", Duties: []string{"procurement.signing-limit.approve"}}}
	existing, err := requester.Must(ctx, http.MethodGet, "/v1/procurement/signing-limits?legalEntityId="+LegalEntity+"&workerId="+worker+"&documentType=PURCHASE_ORDER", nil, nil, http.StatusOK)
	if err != nil {
		return err
	}
	if items, _ := existing.JSON()["items"].([]any); len(items) > 0 {
		return nil
	}
	req, err := requester.Must(ctx, http.MethodPost, "/v1/procurement/signing-limit-requests", map[string]any{
		"legalEntityId": LegalEntity, "workerId": worker, "documentType": "PURCHASE_ORDER", "currency": Currency,
		"spendingLimit": "1000000", "approvalLimit": "1000000", "reason": "dev seed",
	}, nil, http.StatusCreated)
	if err != nil {
		return err
	}
	_, err = approver.Must(ctx, http.MethodPost, "/v1/procurement/signing-limit-requests/"+req.String("id")+":approve", map[string]any{}, map[string]string{"If-Match": req.Header.Get("ETag")}, http.StatusOK)
	return err
}
