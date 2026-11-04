package store

import (
	"errors"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
)

func TestPostgres_GetAccountReadsOneRowWithItsTag(t *testing.T) {
	e, _ := setup(t)
	a, err := e.store.GetAccount(e.ctx, "STD", "2110")
	if err != nil || a.Name != "Vendor control" || a.ReconType != "vendor" || a.ChartCode != "STD" || a.LockVersion != 1 {
		t.Fatalf("get: %+v, %v", a, err)
	}
	if _, _, err := e.store.PutAccount(e.ctx, "STD", "2110", a.LockVersion, Account{Name: "Vendor control 2", Type: "liability", Postable: true, ReconType: "vendor"}); err != nil {
		t.Fatal(err)
	}
	if b, err := e.store.GetAccount(e.ctx, "STD", "2110"); err != nil || b.LockVersion != a.LockVersion+1 || b.Name != "Vendor control 2" {
		t.Fatalf("after replace: %+v, %v", b, err)
	}
	for _, c := range [][2]string{{"STD", "9999"}, {"OTH", "2110"}} {
		_, err := e.store.GetAccount(e.ctx, c[0], c[1])
		var p *httpx.Problem
		if !errors.As(err, &p) || p.Status != http.StatusNotFound || p.Code != "LEDGER_ACCOUNT_NOT_FOUND" {
			t.Fatalf("%v: want 404 LEDGER_ACCOUNT_NOT_FOUND, got %v", c, err)
		}
	}
	other := principalCtx(testkit.TenantID(2), e.le)
	if _, err := e.store.GetAccount(other, "STD", "2110"); err == nil {
		t.Fatal("another tenant read the account")
	}
}
