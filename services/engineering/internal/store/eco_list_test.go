package store_test

import (
	"slices"
	"testing"

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
