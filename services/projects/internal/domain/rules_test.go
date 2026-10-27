package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestTransition_AllowedMoves(t *testing.T) {
	cases := []struct {
		from, to Stage
		ok       bool
	}{
		{StageCreated, StageInProcess, true},
		{StageInProcess, StageOnHold, true},
		{StageOnHold, StageInProcess, true},
		{StageInProcess, StageFinished, true},
		{StageCreated, StageFinished, false},
		{StageCreated, StageOnHold, false},
		{StageFinished, StageInProcess, false},
		{StageOnHold, StageFinished, false},
	}
	for _, c := range cases {
		err := Transition(c.from, c.to)
		if (err == nil) != c.ok {
			t.Errorf("%s→%s: err = %v, want ok=%v", c.from, c.to, err, c.ok)
		}
		if err != nil {
			var de *Error
			if !errors.As(err, &de) || de.Code != CodeStage {
				t.Errorf("%s→%s: error code = %v, want %s", c.from, c.to, err, CodeStage)
			}
		}
	}
}

func TestCanFinish_ListsEveryBlocker(t *testing.T) {
	err := CanFinish(FinishBlockers{OpenJournals: 2, OpenProposals: 1, WIPBalance: "150.00"})
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeFinish {
		t.Fatalf("error = %v, want %s", err, CodeFinish)
	}
	want := "cannot be FINISHED: 2 open journals, 1 DRAFT or APPROVED proposals, non-zero WIP balance 150.00"
	if de.Detail != want {
		t.Fatalf("detail = %q, want %q", de.Detail, want)
	}
	if err := CanFinish(FinishBlockers{WIPBalance: "150.00", FinalCompletedCN: true}); err != nil {
		t.Fatalf("a posted COMPLETED_CONTRACT final estimate should allow finish: %v", err)
	}
	if err := CanFinish(FinishBlockers{WIPBalance: "0"}); err != nil {
		t.Fatalf("clear project should finish: %v", err)
	}
}

func TestValidateProject_Rules(t *testing.T) {
	customer := "00000000-0000-0000-0000-000000000001"
	valid := Project{ID: "PRJ-0001", LegalEntityID: customer, ProjectGroupID: "ENG", ProjectType: TypeFixedPrice,
		CustomerPartyID: &customer, Currency: "USD", StartDate: "2026-10-01"}
	if fields := ValidateProject(valid); len(fields) != 0 {
		t.Fatalf("valid project reported %v", fields)
	}
	end := "2026-09-30"
	internal := func(p *Project) { p.ProjectType = TypeInternal; p.CustomerPartyID = nil }
	cases := []struct {
		name   string
		mutate func(p *Project)
		field  string
	}{
		{"id too long", func(p *Project) { p.ID = "ABCDEFGHIJKLMNOPQRSTU" }, "id"},
		{"id lower case", func(p *Project) { p.ID = "prj" }, "id"},
		{"bad type", func(p *Project) { p.ProjectType = "HOBBY" }, "projectType"},
		{"no customer", func(p *Project) { p.CustomerPartyID = nil }, "customerPartyId"},
		{"internal needs no customer", internal, ""},
		{"bad currency", func(p *Project) { p.Currency = "US" }, "currency"},
		{"bad start", func(p *Project) { p.StartDate = "01/10/2026" }, "startDate"},
		{"end before start", func(p *Project) { p.EndDate = &end }, "endDate"},
		{"empty group", func(p *Project) { p.ProjectGroupID = "" }, "projectGroupId"},
	}
	for _, c := range cases {
		p := valid
		c.mutate(&p)
		fields := ValidateProject(p)
		if c.field == "" {
			if len(fields) != 0 {
				t.Errorf("%s: unexpected %v", c.name, fields)
			}
			continue
		}
		if _, ok := fields[c.field]; !ok {
			t.Errorf("%s: want error on %s, got %v", c.name, c.field, fields)
		}
	}
}

func TestResolve_FallbackOrder(t *testing.T) {
	exact := PostingProfile{Key: ProfileKey{"ENG", "LABOR", "CHARGEABLE"}, WIPAccount: "1400-EXACT"}
	groupWide := PostingProfile{Key: ProfileKey{"ENG", Wildcard, Wildcard}, WIPAccount: "1400-GROUP"}
	global := PostingProfile{Key: ProfileKey{Wildcard, Wildcard, Wildcard}, WIPAccount: "1400-GLOBAL"}
	profiles := []PostingProfile{global, groupWide, exact}

	cases := []struct {
		key  ProfileKey
		want string
	}{
		{ProfileKey{"ENG", "LABOR", "CHARGEABLE"}, "1400-EXACT"},
		{ProfileKey{"ENG", "TRAVEL", "CHARGEABLE"}, "1400-GROUP"},
		{ProfileKey{"OTHER", "LABOR", "NON_CHARGEABLE"}, "1400-GLOBAL"},
	}
	for _, c := range cases {
		got, err := Resolve(profiles, c.key)
		if err != nil || got.WIPAccount != c.want {
			t.Errorf("%+v: got %q err %v, want %q", c.key, got.WIPAccount, err, c.want)
		}
	}
}

func TestResolve_MissingKeyBlocksPosting(t *testing.T) {
	_, err := Resolve([]PostingProfile{{Key: ProfileKey{"ENG", "LABOR", "CHARGEABLE"}}}, ProfileKey{"ENG", "ITEM", "CHARGEABLE"})
	var de *Error
	if !errors.As(err, &de) || de.Code != CodePostingProfile {
		t.Fatalf("error = %v, want %s", err, CodePostingProfile)
	}
	if want := "No posting profile for project group ENG, category ITEM, line property CHARGEABLE"; de.Detail != want {
		t.Fatalf("detail = %q", de.Detail)
	}
}

func TestRetainedAmount_BR4110(t *testing.T) {
	got, err := RetainedAmount(decimal.RequireFromString("20000"), decimal.RequireFromString("10"), "USD")
	if err != nil || !got.Equal(decimal.RequireFromString("2000")) {
		t.Fatalf("retained = %s err %v, want 2000.00", got, err)
	}
	// Half-even at the minor unit: 0.125 → 0.12, 0.375 → 0.38.
	half, _ := RetainedAmount(decimal.RequireFromString("1.25"), decimal.RequireFromString("10"), "USD")
	if !half.Equal(decimal.RequireFromString("0.12")) {
		t.Fatalf("half-even 0.125 = %s, want 0.12", half)
	}
	if _, err := RetainedAmount(decimal.RequireFromString("1"), decimal.RequireFromString("101"), "USD"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("percent over 100 error = %v", err)
	}
}

func TestCheckRelease_EarlyReleaseNeedsOverride(t *testing.T) {
	inv := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	open := ReleaseState{Stage: StageInProcess, InvoiceDate: inv, OffsetDays: 30, AsOf: inv.AddDate(0, 0, 10)}
	if err := CheckRelease(TriggerProjectFinished, open, false); err == nil {
		t.Fatal("release before PROJECT_FINISHED must be refused")
	}
	if err := CheckRelease(TriggerProjectFinished, open, true); err != nil {
		t.Fatalf("override should allow release: %v", err)
	}
	if err := CheckRelease(TriggerDateOffset, open, false); err == nil {
		t.Fatal("offset of 30 days is not reached after 10 days")
	}
	later := open
	later.AsOf = inv.AddDate(0, 0, 30)
	if err := CheckRelease(TriggerDateOffset, later, false); err != nil {
		t.Fatalf("offset reached, release should pass: %v", err)
	}
}

func TestInterestCharge_BR4112(t *testing.T) {
	avg := decimal.RequireFromString("1000000")
	rate := decimal.RequireFromString("0.06")
	got, err := InterestCharge(avg, rate, 31, ACT365, "USD")
	if err != nil || !got.Equal(decimal.RequireFromString("5095.89")) {
		t.Fatalf("ACT/365 October = %s err %v, want 5095.89", got, err)
	}
	got360, _ := InterestCharge(avg, rate, 31, ACT360, "USD")
	if !got360.Equal(decimal.RequireFromString("5166.67")) {
		t.Fatalf("ACT/360 October = %s, want 5166.67", got360)
	}
	if _, err := InterestCharge(avg, rate, 31, "ACT_366", "USD"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unknown day count error = %v", err)
	}
}

func TestValidateCreate_SubProjectMayOmitInheritedFields(t *testing.T) {
	parent := "PRJ-1"
	sub := Project{ID: "PRJ-1-01", LegalEntityID: "00000000-0000-0000-0000-000000000009", ParentProjectID: &parent,
		ProjectType: TypeFixedPrice, Currency: "USD", StartDate: "2026-10-01"}
	if fields := ValidateCreate(sub); len(fields) != 0 {
		t.Fatalf("sub-project without group and customer reported %v", fields)
	}
	top := sub
	top.ParentProjectID = nil
	fields := ValidateCreate(top)
	if fields["projectGroupId"] == "" || fields["customerPartyId"] == "" {
		t.Fatalf("top-level project must name group and customer, got %v", fields)
	}
	sub.Currency = "usd"
	if ValidateCreate(sub)["currency"] == "" {
		t.Fatal("lower-case currency must be rejected")
	}
}

func TestInheritFromParent_FR411(t *testing.T) {
	le := "00000000-0000-0000-0000-000000000009"
	customer, other, contract := "00000000-0000-0000-0000-0000000000c1", "00000000-0000-0000-0000-0000000000c2", "00000000-0000-0000-0000-0000000000a1"
	parent := Project{ID: "PRJ-1", LegalEntityID: le, ProjectGroupID: "ENG", CustomerPartyID: &customer, ContractID: &contract}
	parentID := parent.ID

	got, err := InheritFromParent(Project{ID: "PRJ-1-01", LegalEntityID: le, ParentProjectID: &parentID}, parent)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProjectGroupID != "ENG" || got.CustomerPartyID == nil || *got.CustomerPartyID != customer || got.ContractID == nil || *got.ContractID != contract {
		t.Fatalf("inherited group/customer/contract = %q/%v/%v", got.ProjectGroupID, got.CustomerPartyID, got.ContractID)
	}

	got, err = InheritFromParent(Project{ID: "PRJ-1-02", LegalEntityID: le, ProjectGroupID: "R&D", ParentProjectID: &parentID}, parent)
	if err != nil || got.ProjectGroupID != "R&D" {
		t.Fatalf("group override: %q, %v", got.ProjectGroupID, err)
	}

	same := customer
	if _, err := InheritFromParent(Project{ID: "PRJ-1-03", LegalEntityID: le, CustomerPartyID: &same}, parent); err != nil {
		t.Fatalf("restating the parent's customer is allowed: %v", err)
	}

	var de *Error
	if _, err := InheritFromParent(Project{ID: "PRJ-1-04", LegalEntityID: le, CustomerPartyID: &other}, parent); !errors.As(err, &de) || de.Code != CodeParentConflict {
		t.Fatalf("changing the customer must fail with %s, got %v", CodeParentConflict, err)
	}
	if _, err := InheritFromParent(Project{ID: "PRJ-1-05", LegalEntityID: "00000000-0000-0000-0000-000000000008"}, parent); !errors.As(err, &de) || de.Code != CodeParentConflict {
		t.Fatalf("another legal entity must fail with %s, got %v", CodeParentConflict, err)
	}
}
