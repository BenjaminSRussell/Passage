package store

import (
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

// TestPostgres_AgreementUsageAndExpiryFilters: the list reports each
// agreement's used percent, soonest-expiring first with expiringBy, and keeps
// the agreements that used at least minUsedPct of their commitment.
func TestPostgres_AgreementUsageAndExpiryFilters(t *testing.T) {
	s, _, ctx := migrated(t)
	le := ids.New()
	now := time.Date(2027, 3, 10, 9, 0, 0, 0, time.UTC)
	june, may, dec := "2027-06-30", "2027-05-31", "2027-12-31"
	late := agreementFixture(t, s, ctx, le, 1000, nil, &june)
	soon := agreementFixture(t, s, ctx, le, 400, nil, &may)
	far := agreementFixture(t, s, ctx, le, 1000, nil, &dec)
	open := agreementFixture(t, s, ctx, le, 100, nil, nil)
	if _, _, err := s.ReleaseAgreement(ctx, late.ID, release(750, "r-late"), now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ReleaseAgreement(ctx, soon.ID, release(100, "r-soon"), now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ReleaseAgreement(ctx, open.ID, release(100, "r-open"), now); err != nil {
		t.Fatal(err)
	}

	all, err := s.ListAgreements(ctx, AgreementFilter{LegalEntityID: le, Limit: 50})
	if err != nil || len(all) != 4 {
		t.Fatalf("all: %d %v", len(all), err)
	}
	pct := map[string]string{}
	for _, a := range all {
		if a.UsedPct == nil {
			t.Fatalf("%s has no used percent", a.AgreementNumber)
		}
		pct[a.ID] = a.UsedPct.String()
	}
	if pct[late.ID] != "75" || pct[soon.ID] != "25" || pct[far.ID] != "0" || pct[open.ID] != "100" {
		t.Fatalf("used percent %v", pct)
	}

	expiring, err := s.ListAgreements(ctx, AgreementFilter{LegalEntityID: le, ExpiringBy: "2027-06-30", Limit: 50})
	if err != nil || len(expiring) != 2 || expiring[0].ID != soon.ID || expiring[1].ID != late.ID {
		t.Fatalf("expiring by June, soonest first (open-ended and December excluded): %+v %v", expiring, err)
	}
	if none, err := s.ListAgreements(ctx, AgreementFilter{LegalEntityID: le, ExpiringBy: "2027-04-01", Limit: 50}); err != nil || len(none) != 0 {
		t.Fatalf("nothing expires by April: %+v %v", none, err)
	}

	used, err := s.ListAgreements(ctx, AgreementFilter{LegalEntityID: le, MinUsedPct: "75", Limit: 50})
	if err != nil || len(used) != 2 {
		t.Fatalf("at least 75 percent used: %+v %v", used, err)
	}
	both, err := s.ListAgreements(ctx, AgreementFilter{LegalEntityID: le, MinUsedPct: "20.5", ExpiringBy: "2027-06-30", Limit: 50})
	if err != nil || len(both) != 2 || both[0].ID != soon.ID {
		t.Fatalf("both filters: %+v %v", both, err)
	}
}

// TestPostgres_AmountAgreementUsedPct: an AMOUNT agreement's used percent is the
// value released over the committed amount (50 units at 12.00 draw 600.00 of
// 1,000.00: 60 percent), and minUsedPct filters on that value, not on units.
func TestPostgres_AmountAgreementUsedPct(t *testing.T) {
	s, _, ctx := migrated(t)
	le := ids.New()
	now := time.Date(2027, 3, 10, 9, 0, 0, 0, time.UTC)
	a := amountAgreement(t, s, ctx, le, 1000)
	if _, _, err := s.ReleaseAgreement(ctx, a.ID, release(50, "r-amount"), now); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListAgreements(ctx, AgreementFilter{LegalEntityID: le, Limit: 50})
	if err != nil || len(got) != 1 || got[0].UsedPct == nil || got[0].UsedPct.String() != "60" {
		t.Fatalf("amount used percent: %+v %v", got, err)
	}
	if hit, err := s.ListAgreements(ctx, AgreementFilter{LegalEntityID: le, MinUsedPct: "60", Limit: 50}); err != nil || len(hit) != 1 {
		t.Fatalf("60 percent used matches 60: %+v %v", hit, err)
	}
	if miss, err := s.ListAgreements(ctx, AgreementFilter{LegalEntityID: le, MinUsedPct: "60.01", Limit: 50}); err != nil || len(miss) != 0 {
		t.Fatalf("60 percent used is below 60.01: %+v %v", miss, err)
	}
}
