package store_test

import (
	"slices"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
)

func newEcr(number string, products ...string) domain.Ecr {
	return domain.Ecr{ID: ids.New(), Number: number, Title: "Swap the wheel", Reason: "crack", Priority: "high", State: domain.EcrDraft, Products: products, RequestedBy: "rita"}
}

func TestPostgres_Ecr_LifecycleEventsAndReads(t *testing.T) {
	s, pool, ctx := newStore(t, 1)
	e := newEcr("ECR-1", "WHEEL", "BIKE")
	created, err := s.CreateEcr(ctx, e, store.Idem{Key: "ecr-key", Hash: "h"})
	if err != nil || created.State != domain.EcrDraft {
		t.Fatalf("create = %+v, %v", created, err)
	}
	if replay, err := s.CreateEcr(ctx, newEcr("ECR-1"), store.Idem{Key: "ecr-key", Hash: "h"}); err != nil || replay.ID != e.ID || !slices.Equal(replay.Products, []string{"BIKE", "WHEEL"}) {
		t.Fatalf("replay = %+v, %v; want the original with sorted products", replay, err)
	}
	if _, err := s.CreateEcr(ctx, e, store.Idem{Key: "ecr-key", Hash: "other"}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("reused key = %v", err)
	}
	if _, err := s.CreateEcr(ctx, newEcr("ECR-1", "X"), store.Idem{}); problemCode(err) != store.CodeDuplicate {
		t.Fatalf("duplicate number = %v", err)
	}
	if got := outboxTypes(t, pool, ctx, e.ID); len(got) != 0 {
		t.Fatalf("draft outbox = %v; want none", got)
	}

	steps := []func(*domain.Ecr) error{
		func(x *domain.Ecr) error { return x.Submit() },
		func(x *domain.Ecr) error { return x.StartReview("carol") },
		func(x *domain.Ecr) error { return x.Decide(domain.EcrApproved, "carol", "ok") },
	}
	cur := created
	for _, step := range steps {
		prev := cur.State
		if err := step(&cur); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveEcr(ctx, cur, prev); err != nil {
			t.Fatal(err)
		}
	}
	if got := outboxTypes(t, pool, ctx, e.ID); !slices.Equal(got, []string{"dev.passage.engineering.ecr.approved.v1", "dev.passage.engineering.ecr.submitted.v1"}) {
		t.Fatalf("outbox = %v", got)
	}
	got, err := s.GetEcr(ctx, e.ID)
	if err != nil || got.State != domain.EcrApproved || got.DecidedBy != "carol" || got.DecisionReason != "ok" || got.EcoID != "" {
		t.Fatalf("get = %+v, %v", got, err)
	}
	stale := created
	if err := stale.Submit(); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveEcr(ctx, stale, domain.EcrDraft); problemCode(err) != domain.CodeEcrState {
		t.Fatalf("stale save = %v; want %s", err, domain.CodeEcrState)
	}
	// Withdrawing a submitted request tells consumers it closed; a withdrawn draft was never announced.
	for _, from := range []domain.EcrState{domain.EcrDraft, domain.EcrSubmitted} {
		w := newEcr("ECR-W-"+string(from), "BIKE")
		if _, err := s.CreateEcr(ctx, w, store.Idem{}); err != nil {
			t.Fatal(err)
		}
		if from == domain.EcrSubmitted {
			if err := w.Submit(); err != nil {
				t.Fatal(err)
			}
			if err := s.SaveEcr(ctx, w, domain.EcrDraft); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Cancel("rita", "not needed"); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveEcr(ctx, w, from); err != nil {
			t.Fatal(err)
		}
		var want []string
		if from == domain.EcrSubmitted {
			want = []string{"dev.passage.engineering.ecr.cancelled.v1", "dev.passage.engineering.ecr.submitted.v1"}
		}
		if got := outboxTypes(t, pool, ctx, w.ID); !slices.Equal(got, want) {
			t.Fatalf("withdrawn from %s outbox = %v; want %v", from, got, want)
		}
	}
	if _, err := s.GetEcr(ctx, ids.New()); problemCode(err) != store.CodeEcrNotFound {
		t.Fatalf("missing = %v", err)
	}
	if _, err := s.GetEcr(principalContext(2), e.ID); problemCode(err) != store.CodeEcrNotFound {
		t.Fatalf("other tenant = %v; want not found", err)
	}
}

func TestPostgres_ListEcrs_FiltersAndPages(t *testing.T) {
	s, _, ctx := newStore(t, 1)
	for _, e := range []domain.Ecr{newEcr("ECR-A", "BIKE"), newEcr("ECR-B", "WHEEL"), newEcr("ECR-C", "BIKE", "WHEEL")} {
		if _, err := s.CreateEcr(ctx, e, store.Idem{}); err != nil {
			t.Fatal(err)
		}
	}
	var seen []string
	after := ""
	for {
		page, err := s.ListEcrs(ctx, store.EcrFilter{ProductID: "BIKE"}, after, 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range page.Items {
			seen = append(seen, e.Number)
		}
		if page.NextID == "" {
			break
		}
		after = page.NextID
	}
	if !slices.Equal(seen, []string{"ECR-A", "ECR-C"}) {
		t.Fatalf("BIKE ECRs = %v", seen)
	}
	if page, err := s.ListEcrs(ctx, store.EcrFilter{State: "approved"}, "", store.MaxPageSize); err != nil || len(page.Items) != 0 {
		t.Fatalf("approved = %+v, %v", page, err)
	}
	if page, err := s.ListEcrs(principalContext(2), store.EcrFilter{}, "", store.MaxPageSize); err != nil || len(page.Items) != 0 {
		t.Fatalf("other tenant = %+v, %v", page, err)
	}
}

func TestPostgres_CreateEco_FromApprovedEcr(t *testing.T) {
	s, _, ctx := newStore(t, 1)
	old := version("BIKE", domain.StatusActive, releaseDay.AddDate(-1, 0, 0), "WHEEL")
	next := version("BIKE", domain.StatusDraft, releaseDay, "WHEEL")
	for _, v := range []domain.BomVersion{old, next} {
		if _, err := s.CreateBomVersion(ctx, v, store.Idem{}, noCheck); err != nil {
			t.Fatal(err)
		}
	}
	replaces := []domain.Replacement{{OldVersionID: old.ID, NewVersionID: next.ID}}
	eco := func(number, ecr string) domain.Eco {
		return domain.Eco{ID: ids.New(), Number: number, State: domain.EcoSubmitted, CreatedBy: "rita", EcrID: ecr, Replaces: replaces}
	}
	ecr := newEcr("ECR-1", "BIKE")
	if _, err := s.CreateEcr(ctx, ecr, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateEco(ctx, eco("ECO-0", ecr.ID), store.Idem{}); !domain.HasCode(err, domain.CodeEcrNotApproved) {
		t.Fatalf("draft ECR source = %v; want %s", err, domain.CodeEcrNotApproved)
	}
	if _, err := s.CreateEco(ctx, eco("ECO-0", ids.New()), store.Idem{}); problemCode(err) != store.CodeEcrNotFound {
		t.Fatalf("unknown ECR = %v", err)
	}
	cur := ecr
	for _, step := range []func() error{cur.Submit, func() error { return cur.StartReview("carol") }, func() error { return cur.Decide(domain.EcrApproved, "carol", "") }} {
		prev := cur.State
		if err := step(); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveEcr(ctx, cur, prev); err != nil {
			t.Fatal(err)
		}
	}
	other := newEcr("ECR-2", "FRAME")
	if _, err := s.CreateEcr(ctx, other, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	created, err := s.CreateEco(ctx, eco("ECO-1", ecr.ID), store.Idem{})
	if err != nil || created.EcrID != ecr.ID {
		t.Fatalf("create ECO = %+v, %v", created, err)
	}
	if got, err := s.GetEcr(ctx, ecr.ID); err != nil || got.EcoID != created.ID {
		t.Fatalf("ECR after conversion = %+v, %v; want ecoId %s", got, err, created.ID)
	}
	if rec, err := s.GetEco(ctx, created.ID); err != nil || rec.Eco.EcrID != ecr.ID {
		t.Fatalf("GetEco = %+v, %v", rec.Eco, err)
	}
	if page, err := s.ListEcos(ctx, "", "", store.MaxPageSize); err != nil || len(page.Items) != 1 || page.Items[0].EcrID != ecr.ID {
		t.Fatalf("ListEcos = %+v, %v", page, err)
	}
	if _, err := s.CreateEco(ctx, eco("ECO-2", ecr.ID), store.Idem{}); !domain.HasCode(err, domain.CodeEcrConverted) {
		t.Fatalf("second ECO = %v; want %s", err, domain.CodeEcrConverted)
	}
	// A cancelled ECO frees the ECR for another.
	cancelled := created
	prev := cancelled.State
	if err := cancelled.Decide(domain.EcoCancelled, "carol", "retry"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseEco(ctx, cancelled, prev, domain.ReleasePlan{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetEcr(ctx, ecr.ID); got.EcoID != "" {
		t.Fatalf("ecoId after cancel = %q; want empty", got.EcoID)
	}
	if _, err := s.CreateEco(ctx, eco("ECO-3", ecr.ID), store.Idem{}); err != nil {
		t.Fatalf("ECO after cancelled one = %v", err)
	}
}

func TestPostgres_CreateEco_ProductMustBeOnTheEcr(t *testing.T) {
	s, _, ctx := newStore(t, 1)
	old := version("BIKE", domain.StatusActive, releaseDay.AddDate(-1, 0, 0), "WHEEL")
	next := version("BIKE", domain.StatusDraft, releaseDay, "WHEEL")
	for _, v := range []domain.BomVersion{old, next} {
		if _, err := s.CreateBomVersion(ctx, v, store.Idem{}, noCheck); err != nil {
			t.Fatal(err)
		}
	}
	ecr := newEcr("ECR-1", "FRAME")
	ecr.State = domain.EcrApproved
	if _, err := s.CreateEcr(ctx, ecr, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	eco := domain.Eco{ID: ids.New(), Number: "ECO-1", State: domain.EcoSubmitted, EcrID: ecr.ID, Replaces: []domain.Replacement{{OldVersionID: old.ID, NewVersionID: next.ID}}}
	if _, err := s.CreateEco(ctx, eco, store.Idem{}); !domain.HasCode(err, domain.CodeEcrProduct) {
		t.Fatalf("product off the ECR = %v; want %s", err, domain.CodeEcrProduct)
	}
}
