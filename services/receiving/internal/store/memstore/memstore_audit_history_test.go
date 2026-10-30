package memstore_test

import (
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store/memstore"
)

func TestMemstoreKeepsWhoAndOldAndNewForPolicyChanges(t *testing.T) {
	st := memstore.New()
	p := principal(1)
	ctx := ctxOf(p)
	evt := func(decimal.Decimal) (events.Event, error) { return policyEvent(t, p), nil }
	for _, c := range []struct{ rate, by string }{{"5", "alice"}, {"7.5", "bob"}} {
		if err := st.SetAuditRate(ctx, p.LegalEntityID, decimal.RequireFromString(c.rate), c.by, evt); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetErsVendor(ctx, p.LegalEntityID, vendorID, true, "carol", func(bool) (events.Event, error) { return policyEvent(t, p), nil }); err != nil {
		t.Fatal(err)
	}

	got, err := st.PolicyHistory(ctx)

	want := []memstore.PolicyChange{
		{Kind: "audit", LegalEntityID: p.LegalEntityID, Old: "0", New: "5", By: "alice"},
		{Kind: "audit", LegalEntityID: p.LegalEntityID, Old: "5", New: "7.5", By: "bob"},
		{Kind: "ers", LegalEntityID: p.LegalEntityID, Subject: vendorID, Old: "false", New: "true", By: "carol"},
	}
	if err != nil || len(got) != len(want) {
		t.Fatalf("history %+v err=%v", got, err)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if other, _ := st.PolicyHistory(ctxOf(principal(2))); len(other) != 0 {
		t.Fatalf("another tenant sees %+v", other)
	}
}
