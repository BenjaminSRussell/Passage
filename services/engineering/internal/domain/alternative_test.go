package domain_test

import (
	"testing"

	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/shopspring/decimal"
)

func probLine(no int, comp, group, pct string) domain.BomLine {
	l := line(no, comp, 1)
	p := decimal.RequireFromString(pct)
	l.Alt = domain.Alternative{Group: group, Strategy: domain.AltProbability, ProbabilityPct: &p}
	return l
}

func prioLine(no int, comp, group string, prio int) domain.BomLine {
	l := line(no, comp, 1)
	l.Alt = domain.Alternative{Group: group, Strategy: domain.AltPriority, Priority: prio}
	return l
}

func TestCheckAlternatives(t *testing.T) {
	follow := line(3, "NEWCELL", 1)
	follow.Discontinuation = domain.Discontinuation{Type: domain.DiscSingle, FollowUpGroup: "A1", FollowUpComponent: "CELL-B"}
	badShape := line(1, "X", 1)
	badShape.Alt = domain.Alternative{Group: "A1"}
	tests := []struct {
		name  string
		lines []domain.BomLine
		want  bool
	}{
		{"no groups", []domain.BomLine{line(1, "A", 1)}, true},
		{"60 and 40", []domain.BomLine{probLine(1, "A", "A1", "60"), probLine(2, "B", "A1", "40")}, true},
		{"60 and 30 is refused", []domain.BomLine{probLine(1, "A", "A1", "60"), probLine(2, "B", "A1", "30")}, false},
		{"thirds do not sum to 100", []domain.BomLine{probLine(1, "A", "A1", "33.3333"), probLine(2, "B", "A1", "33.3333"), probLine(3, "C", "A1", "33.3333")}, false},
		{"thirds at the stored scale sum to exactly 100", []domain.BomLine{probLine(1, "A", "A1", "33.3333"), probLine(2, "B", "A1", "33.3333"), probLine(3, "C", "A1", "33.3334")}, true},
		{"trailing zeros past the scale are the same value", []domain.BomLine{probLine(1, "A", "A1", "60.000000"), probLine(2, "B", "A1", "40")}, true},
		// numeric(7,4) would round these to 33.3333 each on save, so the stored group would sum to 99.9999.
		{"a probability finer than the stored scale is refused", []domain.BomLine{probLine(1, "A", "A1", "33.33333"), probLine(2, "B", "A1", "33.33333"), probLine(3, "C", "A1", "33.33334")}, false},
		{"unique priorities", []domain.BomLine{prioLine(1, "A", "A1", 1), prioLine(2, "B", "A1", 2)}, true},
		{"duplicate priority", []domain.BomLine{prioLine(1, "A", "A1", 1), prioLine(2, "B", "A1", 1)}, false},
		{"one member", []domain.BomLine{prioLine(1, "A", "A1", 1)}, false},
		{"mixed strategies", []domain.BomLine{prioLine(1, "A", "A1", 1), probLine(2, "B", "A1", "100")}, false},
		{"group without strategy", []domain.BomLine{badShape}, false},
		{"member that is also the follow-up into its group", []domain.BomLine{prioLine(1, "CELL-A", "A1", 1), prioLine(2, "CELL-B", "A1", 2), follow}, false},
		{"follow-up that is not a member", []domain.BomLine{prioLine(1, "CELL-A", "A1", 1), prioLine(2, "CELL-C", "A1", 2), follow}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := domain.CheckAlternatives(tc.lines)
			if (err == nil) != tc.want {
				t.Fatalf("CheckAlternatives = %v; want ok=%v", err, tc.want)
			}
			if err != nil && !domain.HasCode(err, domain.CodeAltConflict) {
				t.Fatalf("code = %v; want %s", err, domain.CodeAltConflict)
			}
		})
	}
}

func TestValidateLineMeta_Discontinuation(t *testing.T) {
	l := line(1, "A", 1)
	l.Discontinuation = domain.Discontinuation{Group: "D1"}
	if err := domain.ValidateLineMeta(l); !domain.HasCode(err, domain.CodeBomLine) {
		t.Fatalf("no type = %v; want %s", err, domain.CodeBomLine)
	}
	l.Discontinuation = domain.Discontinuation{Type: domain.DiscSimul}
	if err := domain.ValidateLineMeta(l); err == nil {
		t.Fatal("simultaneous without a group was accepted")
	}
	l.Discontinuation = domain.Discontinuation{Type: domain.DiscSingle, FollowUpGroup: "A1"}
	if err := domain.ValidateLineMeta(l); err == nil {
		t.Fatal("follow-up group without a component was accepted")
	}
	l.Discontinuation = domain.Discontinuation{Type: domain.DiscSingle, FollowUpComponent: "B", DiscontinueOn: date("2026-06-01")}
	if err := domain.ValidateLineMeta(l); err != nil {
		t.Fatalf("valid discontinuation = %v", err)
	}
}

func TestExplode_AlternativesComeBackUnresolved(t *testing.T) {
	a, b := probLine(2, "CELL-A", "A1", "60"), probLine(3, "CELL-B", "A1", "40")
	b.Discontinuation = domain.Discontinuation{Type: domain.DiscSingle, FollowUpComponent: "CELL-C", DiscontinueOn: date("2027-01-01")}
	cat := catalogOf(version("v1", "PACK", "2026-01-01", "", line(1, "CASE", 1), a, b))
	got, err := domain.Explode(cat, request("PACK", 10, 3))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Root.Children) != 3 || got.Root.Children[1].Alt.Group != "A1" || got.Root.Children[2].Discontinuation.FollowUpComponent != "CELL-C" {
		t.Fatalf("children = %+v; want the group and discontinuation metadata on the lines", got.Root.Children)
	}
	if len(got.Alternatives) != 2 || got.Alternatives[0].ParentProductID != "PACK" || !got.Alternatives[1].Quantity.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("alternatives = %+v; want both members with quantities", got.Alternatives)
	}
	if len(got.Flat) != 1 || got.Flat[0].ProductID != "CASE" {
		t.Fatalf("flat = %+v; want only CASE (alternatives are not summed)", got.Flat)
	}
}

func TestUsage_ResolutionAndOverlap(t *testing.T) {
	prod := version("p", "BIKE", "2026-01-01", "", line(1, "WHEEL", 2))
	cost := version("c", "BIKE", "2026-01-01", "", line(1, "WHEEL", 3))
	cost.Usage = domain.UsageCosting
	universal := version("u", "TRIKE", "2026-01-01", "", line(1, "WHEEL", 1))
	universal.Usage = domain.UsageUniversal
	doc := version("d", "BIKE", "2026-01-01", "", line(1, "MANUAL", 1))
	doc.Category = domain.CategoryDocument
	cat := catalogOf(prod, cost, universal, doc)

	if got, err := domain.Explode(cat, request("BIKE", 1, 1)); err != nil || got.Flat[0].ProductID != "WHEEL" || !got.Flat[0].Quantity.Equal(decimal.NewFromInt(2)) {
		t.Fatalf("default usage = %+v, %v; want the production BOM (2 wheels), not costing or document", got, err)
	}
	req := request("BIKE", 1, 1)
	req.Usage = domain.UsageCosting
	if got, err := domain.Explode(cat, req); err != nil || !got.Flat[0].Quantity.Equal(decimal.NewFromInt(3)) {
		t.Fatalf("costing usage = %+v, %v; want 3 wheels", got, err)
	}
	if _, err := domain.Explode(cat, request("TRIKE", 1, 1)); err != nil {
		t.Fatalf("production request falls back to a universal BOM: %v", err)
	}
	req = request("BIKE", 1, 1)
	req.Usage = domain.UsageSales
	if _, err := domain.Explode(cat, req); !domain.HasCode(err, domain.CodeBomNoVersion) {
		t.Fatalf("sales usage with no sales or universal BOM = %v; want %s", err, domain.CodeBomNoVersion)
	}
	// Overlap is checked within a usage and category only.
	if err := domain.CheckNoOverlap([]domain.BomVersion{prod}, cost); err != nil {
		t.Fatalf("a costing BOM beside a production BOM overlaps: %v", err)
	}
	other := version("p2", "BIKE", "2026-02-01", "", line(1, "WHEEL", 1))
	if err := domain.CheckNoOverlap([]domain.BomVersion{prod}, other); !domain.HasCode(err, domain.CodeBomOverlap) {
		t.Fatalf("two production BOMs = %v; want %s", err, domain.CodeBomOverlap)
	}
	if err := domain.CheckDirectActivation([]domain.BomVersion{prod}, cost); err != nil {
		t.Fatalf("first costing BOM needs no ECO: %v", err)
	}
}

func TestExplode_SubAssemblyAndPhantomAlternativesStayOutOfFlat(t *testing.T) {
	subA, subB := probLine(2, "SUB-A", "A1", "60"), probLine(3, "SUB-B", "A1", "40")
	ph := prioLine(4, "KIT", "P1", 1)
	ph.LineType = domain.LinePhantom
	cat := catalogOf(
		version("v1", "PACK", "2026-01-01", "", line(1, "CASE", 1), subA, subB, ph, prioLine(5, "LOOSE", "P1", 2)),
		version("a", "SUB-A", "2026-01-01", "", line(1, "CELL", 2)),
		version("b", "SUB-B", "2026-01-01", "", line(1, "CELL", 3), line(2, "GLUE", 1)),
		version("k", "KIT", "2026-01-01", "", line(1, "SCREW", 4)),
	)
	got, err := domain.Explode(cat, request("PACK", 10, 5))
	if err != nil {
		t.Fatal(err)
	}
	// Summing both sub-assemblies' cells (20 + 30) or the kit's screws would count every alternative at once.
	if len(got.Flat) != 1 || got.Flat[0].ProductID != "CASE" || !got.Flat[0].Quantity.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("flat = %+v; want only CASE 10", got.Flat)
	}
	members := map[string]string{}
	for _, m := range got.Alternatives {
		members[m.ProductID] = m.Alt.Group + " " + m.Quantity.String()
	}
	if len(got.Alternatives) != 4 || members["SUB-A"] != "A1 10" || members["SUB-B"] != "A1 10" || members["KIT"] != "P1 10" || members["LOOSE"] != "P1 10" {
		t.Fatalf("alternatives = %+v; want SUB-A, SUB-B, KIT and LOOSE with their groups", got.Alternatives)
	}
	kids := got.Root.Children
	if len(kids) != 5 || kids[1].ProductID != "SUB-A" || len(kids[1].Children) != 1 || kids[3].ProductID != "KIT" || kids[3].Alt.Group != "P1" || len(kids[3].Children) != 1 {
		t.Fatalf("tree = %+v; want each member as a node, a grouped phantom too, with its components below", kids)
	}
}

func TestExplode_BomsWithoutGroupsAreUnchanged(t *testing.T) {
	ph := line(2, "KIT", 1)
	ph.LineType = domain.LinePhantom
	cat := catalogOf(
		version("v1", "PACK", "2026-01-01", "", line(1, "CASE", 1), ph),
		version("k", "KIT", "2026-01-01", "", line(1, "SCREW", 4)),
	)
	got, err := domain.Explode(cat, request("PACK", 2, 5))
	if err != nil || len(got.Alternatives) != 0 || len(got.Flat) != 2 || got.Flat[1].ProductID != "SCREW" || !got.Flat[1].Quantity.Equal(decimal.NewFromInt(8)) {
		t.Fatalf("explosion = %+v, %v; want CASE 2 and SCREW 8 with the phantom inlined and no alternatives", got, err)
	}
	if len(got.Root.Children) != 2 || got.Root.Children[1].ProductID != "SCREW" {
		t.Fatalf("tree = %+v; want the ungrouped phantom inlined", got.Root.Children)
	}
}

func TestActivatedEventCarriesUsageAndCategory(t *testing.T) {
	v := version("c", "BIKE", "2026-01-01", "", line(1, "WHEEL", 1))
	if e := v.ActivatedEvent(""); e.Usage != domain.UsageProduction || e.Category != domain.CategoryMaterial {
		t.Fatalf("default = %+v; want production material", e)
	}
	v.Usage = domain.UsageCosting
	if e := v.ActivatedEvent(""); e.Usage != domain.UsageCosting {
		t.Fatalf("costing = %+v", e)
	}
}
