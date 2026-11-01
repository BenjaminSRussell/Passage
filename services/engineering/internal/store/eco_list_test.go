package store_test

import (
	"slices"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
)

func TestPostgres_ListEcos_FiltersPagesAndIsolatesTenants(t *testing.T) {
	s, _, ctx := newStore(t, 1)
	old := version("BIKE", domain.StatusActive, releaseDay.AddDate(-1, 0, 0), "WHEEL")
	next := version("BIKE", domain.StatusDraft, releaseDay, "WHEEL")
	for _, v := range []domain.BomVersion{old, next} {
		if _, err := s.CreateBomVersion(ctx, v, store.Idem{}, noCheck); err != nil {
			t.Fatal(err)
		}
	}
	replaces := []domain.Replacement{{OldVersionID: old.ID, NewVersionID: next.ID}}
	for _, n := range []string{"ECO-A", "ECO-B"} {
		if _, err := s.CreateEco(ctx, domain.Eco{ID: ids.New(), Number: n, State: domain.EcoSubmitted, Replaces: replaces}, store.Idem{}); err != nil {
			t.Fatal(err)
		}
	}
	var seen []string
	after := ""
	for {
		page, err := s.ListEcos(ctx, "", after, 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range page.Items {
			if len(e.Replaces) != 1 || e.Replaces[0].NewVersionID != next.ID {
				t.Fatalf("ECO %s replacements = %+v", e.Number, e.Replaces)
			}
			seen = append(seen, e.Number)
		}
		if page.NextID == "" {
			break
		}
		after = page.NextID
	}
	if !slices.Equal(seen, []string{"ECO-A", "ECO-B"}) {
		t.Fatalf("paged ECOs = %v; want ECO-A then ECO-B, each once", seen)
	}
	if page, err := s.ListEcos(ctx, "released", "", store.MaxPageSize); err != nil || len(page.Items) != 0 {
		t.Fatalf("released filter = %+v, %v; want none", page, err)
	}
	if page, err := s.ListEcos(ctx, "submitted", "", store.MaxPageSize); err != nil || len(page.Items) != 2 {
		t.Fatalf("submitted filter = %+v, %v; want 2", page, err)
	}
	if page, err := s.ListEcos(principalContext(2), "", "", store.MaxPageSize); err != nil || len(page.Items) != 0 {
		t.Fatalf("another tenant sees %+v, %v; want none", page, err)
	}
}

func TestPostgres_Eco_KeepsCreatorAndDecisionReason(t *testing.T) {
	s, _, ctx := newStore(t, 5)
	old := version("BIKE", domain.StatusActive, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), "WHEEL")
	next := version("BIKE", domain.StatusDraft, releaseDay, "WHEEL")
	for _, v := range []domain.BomVersion{old, next} {
		if _, err := s.CreateBomVersion(ctx, v, store.Idem{}, noCheck); err != nil {
			t.Fatal(err)
		}
	}
	eco := domain.Eco{ID: ids.New(), Number: "ECO-CB", State: domain.EcoSubmitted, CreatedBy: "rita", Replaces: []domain.Replacement{{OldVersionID: old.ID, NewVersionID: next.ID}}}
	if _, err := s.CreateEco(ctx, eco, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	record, err := s.GetEco(ctx, eco.ID)
	if err != nil || record.Eco.CreatedBy != "rita" {
		t.Fatalf("created_by = %q, %v", record.Eco.CreatedBy, err)
	}
	loaded := record.Eco
	if err := loaded.Decide(domain.EcoCancelled, "system:eco-timeout", "window closed"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseEco(ctx, loaded, domain.EcoSubmitted, domain.ReleasePlan{}); err != nil {
		t.Fatal(err)
	}
	after, err := s.GetEco(ctx, eco.ID)
	if err != nil || after.Eco.State != domain.EcoCancelled || after.Eco.DecidedBy != "system:eco-timeout" || after.Eco.DecisionReason != "window closed" || after.Eco.CreatedBy != "rita" {
		t.Fatalf("after cancel = %+v, %v", after.Eco, err)
	}
	self := record.Eco
	if err := self.Decide(domain.EcoApproved, "rita", ""); err == nil {
		t.Fatal("the creator must not be able to decide")
	}
}
