package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/api"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/ledger"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/periods"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store/memstore"
)

const (
	grirAccount    = "2100"
	ledgerUUID     = "00000000-0000-0000-0000-0000000000a1"
	fiscalPeriodID = "00000000-0000-0000-0000-0000000000b1"
	randomLines    = 1000
)

var (
	profile    = domain.PostingProfile{LedgerID: ledgerUUID, Inventory: "1400", GRIR: grirAccount, PPV: "5200", VendorControl: "2000"}
	postingDay = time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
)

type recordingLedger struct {
	mu      sync.Mutex
	entries int
	grir    decimal.Decimal
}

func (l *recordingLedger) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Lines []struct {
			Account     string `json:"account"`
			DebitCredit string `json:"debitCredit"`
			Amount      struct{ Value string }
		} `json:"lines"`
	}
	if r.URL.Path != domain.LedgerPostPath || r.Header.Get("Idempotency-Key") == "" || json.NewDecoder(r.Body).Decode(&body) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries++
	for _, line := range body.Lines {
		if line.Account != grirAccount {
			continue
		}
		if line.DebitCredit == "D" {
			l.grir = l.grir.Add(decimal.RequireFromString(line.Amount.Value))
		}
		if line.DebitCredit == "C" {
			l.grir = l.grir.Sub(decimal.RequireFromString(line.Amount.Value))
		}
	}
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write([]byte(`{"journalEntryId":"je-1"}`))
}

func completePostings(t *testing.T, st *memstore.Store) {
	t.Helper()
	rows, err := st.ListPending(tenantCtx())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if _, err := st.ClaimPending(tenantCtx(), row.Key, 0); err != nil {
			t.Fatal(err)
		}
		if err := st.FinishPending(tenantCtx(), row.Key, "POSTED"); err != nil {
			t.Fatal(err)
		}
	}
}

func closeService(t *testing.T, status int, body string) *periods.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return periods.New(srv.URL, nil)
}

func tenantCtx() context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: "00000000-0000-0000-0000-000000000001"})
}

func TestPostInvoice_GrirNetsToZeroThroughRealLedgerAdapter(t *testing.T) {
	recorder := &recordingLedger{}
	ledgerSrv := httptest.NewServer(recorder)
	t.Cleanup(ledgerSrv.Close)
	ledgerClient := ledger.New(ledgerSrv.URL, nil)
	st := memstore.New()
	a := api.New(st, api.Ports{
		Periods: closeService(t, http.StatusOK, `{"allowed":true,"module":"ap","status":"open"}`),
		Ledger:  ledgerClient,
	})
	ctx := tenantCtx()

	seed := int64(96)
	if v, err := strconv.ParseInt(os.Getenv("RECEIVING_TEST_SEED"), 10, 64); err == nil {
		seed = v
	}
	t.Logf("seed %d", seed)
	rng := rand.New(rand.NewSource(seed))
	receipt := domain.Receipt{ID: "r-1", State: domain.Registered}
	for i := 0; i < randomLines; i++ {
		qty := decimal.NewFromInt(int64(1 + rng.Intn(500)))
		cents := int64(1 + rng.Intn(1_000_000))
		receipt.Lines = append(receipt.Lines, domain.ReceiptLine{
			LineNo: i + 1, ProductID: "p", Qty: qty, OrderedRemaining: qty,
			UnitPrice: money.Amount{Value: decimal.New(cents, -2), Currency: "USD"},
		})
	}
	_, got, err := receipt.Post(ctx, ledgerClient, profile)
	if err != nil {
		t.Fatal(err)
	}
	if recorder.grir.Sign() >= 0 {
		t.Fatalf("receipt left GR/IR at %s, want a credit balance", recorder.grir)
	}
	for i := range got {
		item := &got[i]
		price := receipt.Lines[i].UnitPrice.Value
		if rng.Intn(2) == 0 && item.ReceivedQty.Cmp(decimal.NewFromInt(2)) > 0 {
			first := item.ReceivedQty.Div(decimal.NewFromInt(3)).Floor()
			in := domain.VendorInvoice{ID: fmt.Sprintf("part-%d", i), LedgerID: ledgerUUID, FiscalPeriodID: fiscalPeriodID, PostingDate: postingDay, Currency: "USD", Lines: []domain.PostLine{{Item: item, Qty: first, UnitPrice: price}}}
			if _, err := a.PostInvoice(ctx, in, profile); err != nil {
				t.Fatal(err)
			}
			completePostings(t, st)
		}
		rest := item.ReceivedQty.Sub(item.InvoicedQty)
		in := domain.VendorInvoice{ID: fmt.Sprintf("final-%d", i), LedgerID: ledgerUUID, FiscalPeriodID: fiscalPeriodID, PostingDate: postingDay, Currency: "USD", Lines: []domain.PostLine{{Item: item, Qty: rest, UnitPrice: price}}}
		if _, err := a.PostInvoice(ctx, in, profile); err != nil {
			t.Fatal(err)
		}
		completePostings(t, st)
		if !item.OpenAmount().IsZero() {
			t.Fatalf("line %d open item %s, want 0", i+1, item.OpenAmount())
		}
	}
	if !recorder.grir.IsZero() {
		t.Fatalf("GR/IR balance recorded by the ledger server %s, want 0", recorder.grir)
	}
}

func TestPostInvoice_ClosedPeriodRejectedByRealPeriodsAdapter(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"closed", http.StatusUnprocessableEntity, `{"type":"about:blank","title":"Period closed","status":422,"code":"period_closed","serviceCode":"LEDGER-0013","instance":"/v1/close/period-statuses:check"}`},
		{"on hold", http.StatusForbidden, `{"type":"about:blank","title":"Period on hold","status":403,"code":"LEDGER-0014","serviceCode":"LEDGER-0014","instance":"/v1/close/period-statuses:check"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &recordingLedger{}
			ledgerSrv := httptest.NewServer(recorder)
			t.Cleanup(ledgerSrv.Close)
			a := api.New(memstore.New(), api.Ports{Periods: closeService(t, tc.status, tc.body), Ledger: ledger.New(ledgerSrv.URL, nil)})
			item := domain.GrirItem{ReceivedQty: decimal.NewFromInt(10), ReceivedAmount: money.Amount{Value: decimal.NewFromInt(50), Currency: "USD"}}
			in := domain.VendorInvoice{ID: "i-1", LedgerID: ledgerUUID, FiscalPeriodID: fiscalPeriodID, PostingDate: postingDay, Currency: "USD", Lines: []domain.PostLine{{Item: &item, Qty: decimal.NewFromInt(10), UnitPrice: decimal.NewFromInt(5)}}}

			_, err := a.PostInvoice(tenantCtx(), in, profile)

			var de *domain.Error
			if !errors.As(err, &de) || de.Code != domain.CodePeriodClosed {
				t.Fatalf("got %v, want problem code %s", err, domain.CodePeriodClosed)
			}
			if recorder.entries != 0 || !item.InvoicedQty.IsZero() {
				t.Fatalf("rejected invoice reached the ledger (%d entries) or mutated the item", recorder.entries)
			}
		})
	}
}

func TestPostInvoice_LedgerPeriodClosedMapsToProblemCode(t *testing.T) {
	ledgerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"code":"period_closed"}`))
	}))
	t.Cleanup(ledgerSrv.Close)
	a := api.New(memstore.New(), api.Ports{Periods: closeService(t, http.StatusOK, `{"allowed":true,"module":"ap","status":"open"}`), Ledger: ledger.New(ledgerSrv.URL, nil)})
	item := domain.GrirItem{ReceivedQty: decimal.NewFromInt(1), ReceivedAmount: money.Amount{Value: decimal.NewFromInt(5), Currency: "USD"}}
	in := domain.VendorInvoice{ID: "i-2", LedgerID: ledgerUUID, FiscalPeriodID: fiscalPeriodID, PostingDate: postingDay, Currency: "USD", Lines: []domain.PostLine{{Item: &item, Qty: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(5)}}}

	_, err := a.PostInvoice(tenantCtx(), in, profile)

	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodePeriodClosed {
		t.Fatalf("got %v, want problem code %s", err, domain.CodePeriodClosed)
	}
}

func TestPostInvoice_RequiresLedgerAndFiscalPeriod(t *testing.T) {
	a := api.New(memstore.New(), api.Ports{Periods: domain.FakePeriods{Status: domain.PeriodOpen}, Ledger: &domain.FakeLedger{}})
	item := domain.GrirItem{ReceivedQty: decimal.NewFromInt(1), ReceivedAmount: money.Amount{Value: decimal.NewFromInt(5), Currency: "USD"}}
	for name, in := range map[string]domain.VendorInvoice{
		"no ledger":        {ID: "i", FiscalPeriodID: fiscalPeriodID},
		"no fiscal period": {ID: "i", LedgerID: ledgerUUID},
	} {
		in.PostingDate, in.Currency = postingDay, "USD"
		in.Lines = []domain.PostLine{{Item: &item, Qty: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(5)}}
		_, err := a.PostInvoice(tenantCtx(), in, profile)
		var de *domain.Error
		if !errors.As(err, &de) || de.Code != domain.CodeInvalidReceipt {
			t.Fatalf("%s: got %v, want %s", name, err, domain.CodeInvalidReceipt)
		}
	}
}

func TestNew_PanicsWithoutPorts(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("api.New accepted nil ports")
		}
	}()
	api.New(memstore.New(), api.Ports{})
}
