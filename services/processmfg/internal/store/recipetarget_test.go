package store_test

import (
	"testing"

	"github.com/BenjaminSRussell/passage/services/processmfg/internal/store"
)

// A recipe target is per item and legal entity, replaced by a new put, invisible to another tenant and
// outside a caller's legal entity scope, and the order keeps the recipe it generated or why it did not.
func TestRecipeTargetsAndTheOrdersControlRecipeLink(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	res := "0195f0a0-0000-7000-8000-0000000000f1"
	edge := "0195f0a0-0000-7000-8000-0000000000f2"
	topic := "plant/r1/recipe"
	item := "0195f0a0-0000-7000-8000-0000000000a1"

	if _, found, err := s.GetRecipeTarget(ctx, entityA, item); err != nil || found {
		t.Fatalf("target before any put: found=%v err=%v", found, err)
	}
	put, err := s.PutRecipeTarget(ctx, store.RecipeTarget{LegalEntityID: entityA, ItemID: item, ResourceID: res, Destination: "kiosk"})
	if err != nil || put.Destination != "kiosk" || put.UpdatedBy != "planner-1" || put.EdgeID != nil {
		t.Fatalf("put: %+v %v", put, err)
	}
	put, err = s.PutRecipeTarget(ctx, store.RecipeTarget{LegalEntityID: entityA, ItemID: item, ResourceID: res, Destination: "pcs-mqtt", EdgeID: &edge, Topic: &topic})
	if err != nil || put.Destination != "pcs-mqtt" || *put.Topic != topic {
		t.Fatalf("replacing put: %+v %v", put, err)
	}
	if list, err := s.ListRecipeTargets(ctx, 50); err != nil || len(list) != 1 {
		t.Fatalf("list: %v %v", list, err)
	}
	// The table's checks hold even when the API is bypassed: a kiosk target has no edge, MQTT needs a topic.
	if _, err := s.PutRecipeTarget(ctx, store.RecipeTarget{LegalEntityID: entityA, ItemID: item, ResourceID: res, Destination: "kiosk", EdgeID: &edge}); err == nil {
		t.Fatal("a kiosk target with an edge node was stored")
	}
	if _, err := s.PutRecipeTarget(ctx, store.RecipeTarget{LegalEntityID: entityA, ItemID: item, ResourceID: res, Destination: "pcs-mqtt", EdgeID: &edge}); err == nil {
		t.Fatal("an MQTT target without a topic was stored")
	}
	if _, found, _ := s.GetRecipeTarget(ctxFor(2, ""), entityA, item); found {
		t.Fatal("another tenant sees the target")
	}
	if _, found, _ := s.GetRecipeTarget(ctxFor(1, entityB), entityA, item); found {
		t.Fatal("a caller scoped to another legal entity sees the target")
	}

	f, _, err := s.CreateFormula(ctx, ac241("DOUGH-RT"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	o, _, err := s.CreateBatchOrder(ctx, store.NewBatchOrder{LegalEntityID: entityA, FormulaID: f.ID, BatchSize: dec("1000")}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if link, err := s.GetControlRecipeLink(ctx, o.ID); err != nil || link.ControlRecipeID != nil || link.Error != nil {
		t.Fatalf("link before release: %+v %v", link, err)
	}
	reason := "the shopfloor service answered 503"
	if err := s.SetControlRecipe(ctx, o.ID, nil, &reason); err != nil {
		t.Fatal(err)
	}
	if link, _ := s.GetControlRecipeLink(ctx, o.ID); link.Error == nil || *link.Error != reason || link.ControlRecipeID != nil {
		t.Fatalf("failed link: %+v", link)
	}
	recipe := "0195f0a0-0000-7000-8000-0000000000c1"
	if err := s.SetControlRecipe(ctx, o.ID, &recipe, nil); err != nil {
		t.Fatal(err)
	}
	if link, _ := s.GetControlRecipeLink(ctx, o.ID); link.ControlRecipeID == nil || *link.ControlRecipeID != recipe || link.Error != nil {
		t.Fatalf("link after the retry: %+v", link)
	}
	if err := s.SetControlRecipe(ctx, "0195f0a0-0000-7000-8000-0000000000ff", &recipe, nil); err == nil {
		t.Fatal("a link on an order that does not exist")
	}

	if gone, err := s.DeleteRecipeTarget(ctx, entityA, item); err != nil || !gone {
		t.Fatalf("delete: %v %v", gone, err)
	}
	if gone, _ := s.DeleteRecipeTarget(ctx, entityA, item); gone {
		t.Fatal("deleted twice")
	}
}

// A rescale is allowed while the order has no control recipe (none generated, or the generation failed), and
// refused once the shopfloor service has one: that recipe would keep the old quantities and cannot be replaced.
func TestRescaleIsRefusedOnceTheControlRecipeIsSent(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	f, _, err := s.CreateFormula(ctx, ac241("DOUGH-RS"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	o, _, err := s.CreateBatchOrder(ctx, store.NewBatchOrder{LegalEntityID: entityA, FormulaID: f.ID, BatchSize: dec("1000")}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	reason := "the shopfloor service answered 503"
	if err := s.SetControlRecipe(ctx, o.ID, nil, &reason); err != nil {
		t.Fatal(err)
	}
	o, _, err = s.RescaleBatchOrder(ctx, o.ID, dec("1500"), o.Version, store.Idem{})
	if err != nil {
		t.Fatalf("rescale after a failed generation: %v", err)
	}
	recipe := "0195f0a0-0000-7000-8000-0000000000c2"
	if err := s.SetControlRecipe(ctx, o.ID, &recipe, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RescaleBatchOrder(ctx, o.ID, dec("2000"), o.Version, store.Idem{}); problemCode(err) != store.CodeRecipeSent {
		t.Fatalf("rescale with a recipe on the shop floor: %v", err)
	}
	if got, err := s.GetBatchOrder(ctx, o.ID); err != nil || !got.BatchSize.Equal(dec("1500")) {
		t.Fatalf("batch size after the refused rescale: %v %v", got.BatchSize, err)
	}
}
