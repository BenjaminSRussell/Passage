package ledgerclient_test

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

func TestLedger_ReadsTheOwnerChartAndCurrencyWithTheLookupDuty(t *testing.T) {
	var verifier *tenant.ServiceVerifier
	var duties []string
	c, v := newClientAs(t, "close", func(w http.ResponseWriter, r *http.Request) {
		claims, err := verifier.Verify(r.Header.Get(tenant.HeaderServiceToken))
		if err != nil {
			t.Errorf("token: %v", err)
		}
		duties = claims.Duties
		if r.URL.Path != "/v1/ledger/ledgers/"+ledgerID {
			t.Errorf("path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"legalEntityId":"` + entityID + `","chartCode":"COA","currency":"USD","dimensionAttributes":["CC"]}`))
	})
	verifier = v
	info, err := c.Ledger(principalCtx(), entityID, ledgerID)
	if err != nil || info.LegalEntityID != entityID || info.ChartCode != "COA" || info.Currency != "USD" || len(info.DimensionAttributes) != 1 {
		t.Fatalf("info %+v err %v", info, err)
	}
	if len(duties) != 1 || duties[0] != ledgerclient.DutyLedgerRead {
		t.Fatalf("duties %v", duties)
	}
}

func TestLedger_ForbiddenAndMissingAreNotVisibleAndAnIncompleteBodyFails(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound} {
		c, _ := newClientAs(t, "close", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })
		if _, err := c.Ledger(principalCtx(), entityID, ledgerID); !errors.Is(err, ledgerclient.ErrNotVisible) {
			t.Errorf("status %d: %v", status, err)
		}
	}
	c, _ := newClientAs(t, "close", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"chartCode":"COA"}`)) })
	if _, err := c.Ledger(principalCtx(), entityID, ledgerID); err == nil || errors.Is(err, ledgerclient.ErrNotVisible) {
		t.Fatalf("a body without the owner must fail closed: %v", err)
	}
}

func TestAccounts_FollowsPagesAndKeepsTypeAndPostable(t *testing.T) {
	var cursors []string
	c, _ := newClientAs(t, "close", func(w http.ResponseWriter, r *http.Request) {
		cursors = append(cursors, r.URL.Query().Get("after"))
		if r.URL.Path != "/v1/ledger/charts/COA/accounts" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.URL.Query().Get("after") == "" {
			_, _ = w.Write([]byte(`{"items":[{"accountNumber":"1000","accountType":"asset","isPostable":true}],"nextAfter":"1000"}`))
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"accountNumber":"3000","accountType":"equity","isPostable":false}],"nextAfter":""}`))
	})
	accounts, err := c.Accounts(principalCtx(), entityID, "COA")
	if err != nil || len(accounts) != 2 || accounts["1000"].Type != "asset" || !accounts["1000"].Postable || accounts["3000"].Postable || len(cursors) != 2 {
		t.Fatalf("accounts %+v cursors %v err %v", accounts, cursors, err)
	}
}

func TestTrialBalance_AsksWithTheBalanceDutyAndReturnsNetPerDimension(t *testing.T) {
	var verifier *tenant.ServiceVerifier
	var duties []string
	var query map[string][]string
	c, v := newClientAs(t, "close", func(w http.ResponseWriter, r *http.Request) {
		claims, _ := verifier.Verify(r.Header.Get(tenant.HeaderServiceToken))
		duties, query = claims.Duties, r.URL.Query()
		_, _ = w.Write([]byte(`{"currency":"USD","rows":[{"account":"1000","debit":"150.00","credit":"50.00","dimensions":{"CC":"A","DEPT":""}},{"account":"4000","debit":"0","credit":"100.00"}]}`))
	})
	verifier = v
	tb, err := c.TrialBalance(principalCtx(), ledgerclient.TrialBalanceQuery{LegalEntityID: entityID, LedgerID: ledgerID, PeriodFrom: "2026-01", PeriodTo: "2026-12", GroupBy: []string{"CC", "DEPT"}})
	if err != nil || tb.Currency != "USD" || len(tb.Rows) != 2 {
		t.Fatalf("tb %+v err %v", tb, err)
	}
	if !tb.Rows[0].Net.Equal(decimal.NewFromInt(100)) || tb.Rows[0].Dimensions["CC"] != "A" || len(tb.Rows[0].Dimensions) != 1 || !tb.Rows[1].Net.Equal(decimal.NewFromInt(-100)) {
		t.Fatalf("rows %+v", tb.Rows)
	}
	if len(duties) != 1 || duties[0] != ledgerclient.DutyBalanceRead || query["ledger"][0] != ledgerID || query["period_from"][0] != "2026-01" || query["period_to"][0] != "2026-12" || query["group_by"][0] != "CC,DEPT" {
		t.Fatalf("duties %v query %v", duties, query)
	}
}

func TestTrialBalance_RejectsAnUnparseableAmount(t *testing.T) {
	c, _ := newClientAs(t, "close", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"currency":"USD","rows":[{"account":"1000","debit":"x","credit":"0"}]}`))
	})
	if _, err := c.TrialBalance(principalCtx(), ledgerclient.TrialBalanceQuery{LegalEntityID: entityID, LedgerID: ledgerID}); err == nil {
		t.Fatal("a garbled amount must not become a zero balance")
	}
}

func TestPost_CarriesDimensionsOnTheLine(t *testing.T) {
	var body string
	c, _ := newClientAs(t, "close", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"entries":[{"id":"e1"}]}`))
	})
	e := entry()
	e.Lines[0].Dimensions = map[string]string{"CC": "A"}
	if _, err := c.Post(principalCtx(), e); err != nil || !strings.Contains(body, `"dimensions":{"CC":"A"}`) {
		t.Fatalf("body %s err %v", body, err)
	}
}

func TestInProgress_OnlyTheIdempotencyConflictCode(t *testing.T) {
	if !ledgerclient.InProgress(&ledgerclient.Error{Status: http.StatusConflict, Code: ledgerclient.CodeInProgress}) {
		t.Fatal("in-progress conflict not recognised")
	}
	for _, err := range []error{
		&ledgerclient.Error{Status: http.StatusConflict, Code: ledgerclient.CodeIdempotencyConflict},
		&ledgerclient.Error{Status: http.StatusUnprocessableEntity, Code: ledgerclient.CodeInProgress},
		errors.New("x"), nil,
	} {
		if ledgerclient.InProgress(err) {
			t.Errorf("%v must not be in progress", err)
		}
	}
}
