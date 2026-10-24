package domain_test

import (
	"testing"

	"github.com/BenjaminSRussell/passage/services/product/internal/domain"
	"github.com/shopspring/decimal"
)

func TestAttributeActivation(t *testing.T) {
	min := decimal.RequireFromString("0.2")
	max := decimal.RequireFromString("6.0")
	defs := []domain.AttributeType{
		{Code: "thread_pitch", Kind: "decimal", Min: &min, Max: &max, Required: true},
		{Code: "material", Kind: "enum", Enum: []string{"steel", "brass"}, Required: true},
	}
	err := domain.AssertActivationAttributes(domain.StateDraft, domain.StateActive, defs, map[string]string{"thread_pitch": "1.0"})
	if err == nil {
		t.Fatal("missing material must fail")
	}
	err = domain.AssertActivationAttributes(domain.StateDraft, domain.StateActive, defs, map[string]string{"thread_pitch": "7", "material": "steel"})
	if err == nil {
		t.Fatal("out of range pitch must fail")
	}
	if err := domain.AssertActivationAttributes(domain.StateDraft, domain.StateActive, defs, map[string]string{"thread_pitch": "1.25", "material": "steel"}); err != nil {
		t.Fatal(err)
	}
}

func TestUOMProductOverridesClass(t *testing.T) {
	product := []domain.Conversion{{From: "BOX", To: "EA", Factor: decimal.NewFromInt(12), ProductScoped: true}}
	class := []domain.Conversion{{From: "CASE", To: "EA", Factor: decimal.NewFromInt(24), Class: "count"}}
	got, err := domain.ConvertQuantity(product, class, "BOX", "CASE", decimal.NewFromInt(2), 6)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("2 BOX → CASE = %s, want 1", got)
	}
	got, err = domain.ConvertQuantity(nil, class, "CASE", "EA", decimal.NewFromInt(1), 6)
	if err != nil || !got.Equal(decimal.NewFromInt(24)) {
		t.Fatalf("class conversion %s %v", got, err)
	}
}

func TestSymmetricRelationAndSelf(t *testing.T) {
	types := domain.SeededRelationTypes()
	got, err := domain.AddRelation(nil, domain.Relation{TypeCode: "substitute", FromProductID: "A", ToProductID: "B"}, types, false)
	if err != nil || len(got) != 2 {
		t.Fatalf("create A→B: %v %+v", err, got)
	}
	got = domain.DeleteRelation(got, domain.Relation{TypeCode: "substitute", FromProductID: "B", ToProductID: "A"}, types)
	if len(got) != 0 {
		t.Fatalf("delete inverse left %v", got)
	}
	if _, err := domain.AddRelation(nil, domain.Relation{TypeCode: "substitute", FromProductID: "A", ToProductID: "A"}, types, false); err == nil {
		t.Fatal("self relation must fail")
	}
}

func TestServiceNotStocked(t *testing.T) {
	p := domain.Product{ProductType: domain.TypeService}
	if err := domain.AssertServiceNotStocked(p, domain.ItemModelGroup{Stocked: true}, "", ""); err == nil {
		t.Fatal("stocked service must fail")
	}
	if err := domain.AssertServiceNotStocked(p, domain.ItemModelGroup{Stocked: false}, "g", ""); err == nil {
		t.Fatal("tracking group on service must fail")
	}
	if got := domain.ServiceNumber("CAL", 1); got != "SVC-CAL-00001" {
		t.Fatalf("service number %s", got)
	}
}

func TestGroupChangeSkipsOnHand(t *testing.T) {
	items := make([]domain.GroupChangeItem, 100)
	onHand := map[string]decimal.Decimal{"r2": decimal.NewFromInt(5), "r5": decimal.NewFromInt(5), "r9": decimal.NewFromInt(5)}
	for i := 0; i < 100; i++ {
		items[i] = domain.GroupChangeItem{ReleasedProductID: "r" + itoa(i)}
	}
	got := domain.RunGroupChange(items, func(id string) decimal.Decimal { return onHand[id] })
	changed, skipped := 0, 0
	for _, r := range got {
		if r.Status == "changed" {
			changed++
		}
		if r.Status == "skipped" && r.Code == domain.CodeGrpChgOnHand {
			skipped++
		}
	}
	if changed != 97 || skipped != 3 {
		t.Fatalf("changed=%d skipped=%d", changed, skipped)
	}
}

func TestDocumentVersionCopiesLinksNotOriginals(t *testing.T) {
	prev := domain.DocumentInfoRecord{
		TypeCode: "DRW", DocNumber: "DRW-0000123", Part: "000", DocVersion: "00",
		Links:     []domain.DocumentLink{{TargetType: "product", TargetID: "p1"}, {TargetType: "bom", TargetID: "b1"}, {TargetType: "route", TargetID: "r1"}},
		Originals: []domain.DocumentOriginal{{StorageKey: "k", Text: "torque"}},
	}
	next := domain.NewDocumentVersion(prev, "01")
	if next.DocVersion != "01" || len(next.Links) != 3 || len(next.Originals) != 0 {
		t.Fatalf("%+v", next)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}
