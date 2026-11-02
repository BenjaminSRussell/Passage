package store

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/domain"
	costevents "github.com/BenjaminSRussell/passage/services/productcost/internal/events"
)

func revaluation(oldStd, newStd string) domain.RevalPreview {
	return domain.Revalue([]domain.RevalLine{{ItemID: "I1", SiteID: "S1", Qty: decimal.NewFromInt(10),
		OldStd: decimal.RequireFromString(oldStd), NewStd: decimal.RequireFromString(newStd)}}, "USD")
}

func activation(versionID, key string, ledger ledgerclient.Ledger, pa PostingAccounts, le string) ActivationInput {
	preview := revaluation("1.00", "2.50")
	return ActivationInput{
		VersionID: versionID, Expected: 1, Key: key, Fingerprint: versionID, LedgerID: ids.Nil, PostingDate: "2026-01-02", Currency: "USD", Preview: preview,
		Post: func(ctx context.Context, activationID string) (string, error) {
			e := ledgerclient.Entry{LegalEntityID: le, LedgerID: ids.Nil, DocumentID: activationID, PostingDate: "2026-01-02", Currency: "USD",
				IdempotencyKey: "productcost.activation:" + versionID + ":1", SourceType: "StandardCostActivation"}
			for _, jl := range preview.Journal {
				account := pa.InventoryAccount
				if jl.Account == domain.AccountCostChangeVar {
					account = pa.CostChangeAccount
				}
				amount, credit := jl.Debit, false
				if !jl.Credit.IsZero() {
					amount, credit = jl.Credit, true
				}
				e.Lines = append(e.Lines, ledgerclient.Line{Account: account, Credit: credit, Amount: amount})
			}
			return ledger.Post(ctx, e)
		},
	}
}

func TestPostingAccounts_AreStoredPerLegalEntityAndIsolated(t *testing.T) {
	s, _, rls := setup(t)
	t1 := as(1, "")
	leA, leB := ids.New(), ids.New()
	if _, err := s.GetPostingAccounts(t1, leA); codeOf(err) != domain.CodePostingAccounts {
		t.Fatalf("none configured: %v", err)
	}
	if _, err := s.SetPostingAccounts(t1, PostingAccounts{LegalEntityID: leA, InventoryAccount: "1400", CostChangeAccount: "5900"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.SetPostingAccounts(t1, PostingAccounts{LegalEntityID: leA, InventoryAccount: "1410", CostChangeAccount: "5910"})
	if err != nil || got.UpdatedBy != "cost.accountant" {
		t.Fatalf("update: %+v %v", got, err)
	}
	if pa, err := s.GetPostingAccounts(t1, leA); err != nil || pa.InventoryAccount != "1410" || pa.CostChangeAccount != "5910" {
		t.Fatalf("read back %+v %v", pa, err)
	}
	if _, err := s.GetPostingAccounts(t1, leB); codeOf(err) != domain.CodePostingAccounts {
		t.Fatalf("another legal entity inherits accounts: %v", err)
	}
	if _, err := s.GetPostingAccounts(as(2, ""), leA); codeOf(err) != domain.CodePostingAccounts {
		t.Fatalf("another tenant reads the accounts: %v", err)
	}
	if !rls {
		t.Skipf("%s is needed to run as the application role", testkit.EnvAppURL)
	}
	if _, err := s.GetPostingAccounts(as(1, leB), leA); codeOf(err) != domain.CodePostingAccounts {
		t.Fatalf("legal-entity scope B reads A: %v", err)
	}
	if _, err := s.SetPostingAccounts(as(1, leB), PostingAccounts{LegalEntityID: leA, InventoryAccount: "X", CostChangeAccount: "Y"}); codeOf(err) != domain.CodeLegalEntityScope || statusOf(err) != http.StatusForbidden {
		t.Fatalf("legal-entity scope B writes A: %v", err)
	}
}

func TestActivation_PostsOnceAndReplaysByIdempotencyKey(t *testing.T) {
	s, admin, _ := setup(t)
	ctx := as(1, "")
	le := ids.New()
	pa := PostingAccounts{LegalEntityID: le, InventoryAccount: "1400-INV", CostChangeAccount: "5900-PPV"}
	if _, err := s.SetPostingAccounts(ctx, pa); err != nil {
		t.Fatal(err)
	}
	v, _, err := s.CreateVersion(ctx, spec(le, "STD-ACT"), "k-v")
	if err != nil {
		t.Fatal(err)
	}
	ledger := ledgerclient.NewFake()
	first, replayed, err := s.ActivateVersion(ctx, activation(v.ID, "k-act", ledger, pa, le))
	if err != nil || replayed {
		t.Fatalf("activate: replayed=%v err=%v", replayed, err)
	}
	if first.Version.Status != domain.VersionActive || first.Version.Version != 2 || first.Activation.JournalEntryID == "" || first.Activation.Total.String() != "15" {
		t.Fatalf("result %+v", first)
	}
	again, replayed, err := s.ActivateVersion(ctx, activation(v.ID, "k-act", ledger, pa, le))
	if err != nil || !replayed || again.Activation.ID != first.Activation.ID {
		t.Fatalf("replay %+v replayed=%v err=%v", again, replayed, err)
	}
	entries := ledger.Entries()
	if len(entries) != 1 || entries[0].ID != first.Activation.JournalEntryID {
		t.Fatalf("ledger entries %+v", entries)
	}
	for _, l := range entries[0].Lines {
		if l.Account != "1400-INV" && l.Account != "5900-PPV" {
			t.Errorf("journal line on %q, not a configured account", l.Account)
		}
	}
	var n int
	if err := admin.QueryRow(context.Background(), `select count(*) from standard_cost_activation where costing_version_id = $1`, v.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("activation rows %d err=%v", n, err)
	}
	if got := outboxTypes(t, admin, first.Activation.ID); len(got) != 1 || got[0] != costevents.StandardCostActivated {
		t.Fatalf("outbox %v", got)
	}
	if _, _, err := s.ActivateVersion(ctx, activation(v.ID, "k-other", ledger, pa, le)); codeOf(err) != domain.CodeVersionConflict {
		t.Fatalf("second activation under a new key with a stale counter: %v", err)
	}
	if len(ledger.Entries()) != 1 {
		t.Fatal("a refused activation posted")
	}
}

func TestActivation_RefusedJournalLeavesTheVersionUntouchedAndRetryable(t *testing.T) {
	s, admin, _ := setup(t)
	ctx := as(1, "")
	le := ids.New()
	pa := PostingAccounts{LegalEntityID: le, InventoryAccount: "1400-INV", CostChangeAccount: "5900-PPV"}
	v, _, err := s.CreateVersion(ctx, spec(le, "STD-REFUSED"), "k-v")
	if err != nil {
		t.Fatal(err)
	}
	ledger := ledgerclient.NewFake()
	refusal := &ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: ledgerclient.CodePeriodClosed}
	ledger.FailPosts(refusal)
	if _, _, err := s.ActivateVersion(ctx, activation(v.ID, "k-act", ledger, pa, le)); !errors.Is(err, refusal) {
		t.Fatalf("refused journal: %v", err)
	}
	got, err := s.GetVersion(ctx, v.ID)
	if err != nil || got.Status != domain.VersionOpen || got.Version != 1 || got.ActivatedAt != nil {
		t.Fatalf("version after a refused journal: %+v %v", got, err)
	}
	var n int
	if err := admin.QueryRow(context.Background(), `select count(*) from standard_cost_activation where costing_version_id = $1`, v.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("activation rows %d err=%v", n, err)
	}
	ledger.FailPosts(nil)
	res, replayed, err := s.ActivateVersion(ctx, activation(v.ID, "k-act", ledger, pa, le))
	if err != nil || replayed || res.Version.Status != domain.VersionActive || len(ledger.Entries()) != 1 {
		t.Fatalf("retry with the same key: %+v replayed=%v err=%v entries=%d", res, replayed, err, len(ledger.Entries()))
	}
}

func TestActivation_LegalEntityScopeAppliesToTheVersion(t *testing.T) {
	s, _, rls := setup(t)
	if !rls {
		t.Skipf("%s is needed to run as the application role", testkit.EnvAppURL)
	}
	t1 := as(1, "")
	leA, leB := ids.New(), ids.New()
	pa := PostingAccounts{LegalEntityID: leA, InventoryAccount: "1400-INV", CostChangeAccount: "5900-PPV"}
	v, _, err := s.CreateVersion(t1, spec(leA, "STD-A"), "k-a")
	if err != nil {
		t.Fatal(err)
	}
	ledger := ledgerclient.NewFake()
	if _, _, err := s.ActivateVersion(as(1, leB), activation(v.ID, "k-sneak", ledger, pa, leA)); codeOf(err) != domain.CodeVersionNotFound {
		t.Fatalf("scope B activates a version of A: %v", err)
	}
	if len(ledger.Entries()) != 0 {
		t.Fatal("a journal was posted for a version outside the caller's scope")
	}
}
