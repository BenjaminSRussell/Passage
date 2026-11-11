package store

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

func TestOpeningBalances_BatchIsAllOrNothingAndReplays(t *testing.T) {
	s, _ := newStore(t)
	ctx := ctxFor(1, "")
	le := ids.New()
	usd, err := s.CreateBankAccount(ctx, NewBankAccount{LegalEntityID: le, AccountCode: "OPS-USD", IBAN: "GB82WEST12345698765432", Currency: "USD", MainAccountID: ids.New()}, ids.New())
	if err != nil {
		t.Fatal(err)
	}
	eur, err := s.CreateBankAccount(ctx, NewBankAccount{LegalEntityID: le, AccountCode: "OPS-EUR", IBAN: "DE89370400440532013000", Currency: "EUR", MainAccountID: ids.New()}, ids.New())
	if err != nil {
		t.Fatal(err)
	}
	in := OpeningImport{LegalEntityID: le, BatchKey: "b1", AsOf: "2026-12-31", Items: []OpeningItem{
		{Reference: "B-1", BankAccountID: usd.ID, Currency: "USD", BalanceMinor: 1250075},
		{Reference: "B-2", BankAccountID: eur.ID, Currency: "EUR", BalanceMinor: -400}}}
	b, replayed, err := s.ImportOpeningBalances(ctx, in)
	if err != nil || replayed || b.ItemCount != 2 || b.TotalsMinor["USD"] != 1250075 || b.TotalsMinor["EUR"] != -400 {
		t.Fatalf("import = %+v %v %v", b, replayed, err)
	}
	if b2, replayed, err := s.ImportOpeningBalances(ctx, in); err != nil || !replayed || b2.ItemCount != 2 {
		t.Fatalf("replay = %+v %v %v", b2, replayed, err)
	}
	other := in
	other.Items = []OpeningItem{{Reference: "B-1", BankAccountID: usd.ID, Currency: "USD", BalanceMinor: 1}}
	if _, _, err := s.ImportOpeningBalances(ctx, other); problemCode(t, err) != CodeOpeningKeyConflict {
		t.Fatalf("same key, other content: %v", err)
	}
	again := OpeningImport{LegalEntityID: le, BatchKey: "b2", AsOf: "2026-12-31", Items: []OpeningItem{{Reference: "B-9", BankAccountID: usd.ID, Currency: "USD", BalanceMinor: 5}}}
	if _, _, err := s.ImportOpeningBalances(ctx, again); problemCode(t, err) != CodeOpeningAccountHeld {
		t.Fatalf("an account opened twice: %v", err)
	}
	wrong := OpeningImport{LegalEntityID: le, BatchKey: "b3", AsOf: "2026-12-31", Items: []OpeningItem{{Reference: "B-8", BankAccountID: ids.New(), Currency: "USD", BalanceMinor: 5}}}
	if _, _, err := s.ImportOpeningBalances(ctx, wrong); problemCode(t, err) != CodeOpeningReference {
		t.Fatalf("an unknown account: %v", err)
	}
	cur := OpeningImport{LegalEntityID: le, BatchKey: "b4", AsOf: "2026-12-31", Items: []OpeningItem{{Reference: "B-7", BankAccountID: usd.ID, Currency: "EUR", BalanceMinor: 5}}}
	if _, _, err := s.ImportOpeningBalances(ctx, cur); problemCode(t, err) != CodeOpeningReference {
		t.Fatalf("a currency mismatch: %v", err)
	}
	list, err := s.ListOpeningBatches(ctx, le, "", 10)
	if err != nil || len(list) != 1 || list[0].BatchKey != "b1" || list[0].TotalsMinor["USD"] != 1250075 {
		t.Fatalf("list = %+v %v", list, err)
	}
	if list, _ := s.ListOpeningBatches(ctx, ids.New(), "", 10); len(list) != 0 {
		t.Fatalf("another entity sees %+v", list)
	}
}
