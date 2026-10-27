package domain

import (
	"fmt"
	"testing"
)

func TestTagNumber_Format(t *testing.T) {
	cases := map[int64]string{1: "TAG-000001", 42: "TAG-000042", 1234567: "TAG-1234567"}
	for seq, want := range cases {
		if got := TagNumber(seq); got != want {
			t.Errorf("TagNumber(%d) = %s, want %s", seq, got, want)
		}
	}
}

func TestUseTag_Rules(t *testing.T) {
	open := CountTag{ID: "t1", Number: "TAG-000001", Status: TagOpen, Version: 1}
	site := Dimensions{DimSite: "S1", DimWarehouse: "W1"}
	cases := []struct {
		name      string
		tag       CountTag
		warehouse string
		product   string
		dims      Dimensions
		qty       string
		code      string
	}{
		{"open tag is used", open, "", "p1", site, "3", ""},
		{"zero count is valid (BR-16.15)", open, "", "p1", site, "0", ""},
		{"used tag rejected", CountTag{Number: "TAG-2", Status: TagUsed}, "", "p1", site, "1", CodeTagUsed},
		{"void tag rejected", CountTag{Number: "TAG-3", Status: TagVoid}, "", "p1", site, "1", CodeTagState},
		{"missing tag rejected", CountTag{Number: "TAG-4", Status: TagMissing}, "", "p1", site, "1", CodeTagState},
		{"transferred tag rejected", CountTag{Number: "TAG-5", Status: TagOpen, SheetID: "s"}, "", "p1", site, "1", CodeTagState},
		{"product required", open, "", "", site, "1", CodeInvalid},
		{"negative count rejected", open, "", "p1", site, "-1", CodeInvalid},
		{"more than six decimals rejected", open, "", "p1", site, "1.0000001", CodeInvalid},
		{"site mandatory", open, "", "p1", Dimensions{DimWarehouse: "W1"}, "1", CodeDimensionMandatory},
		{"reserved dimension rejected", open, "", "p1", Dimensions{DimSite: "S1", DimOwner: "V1"}, "1", CodeInvalid},
		{"unknown dimension rejected", open, "", "p1", Dimensions{DimSite: "S1", "shelf": "3"}, "1", CodeInvalid},
		{"serial counts at most one", open, "", "p1", Dimensions{DimSite: "S1", DimSerial: "SN1"}, "2", CodeSerialQty},
		{"other warehouse than the plan rejected", open, "W2", "p1", site, "1", CodeInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := UseTag(c.tag, c.warehouse, c.product, c.dims, d(c.qty), 6)
			if codeOf(err) != c.code {
				t.Fatalf("code = %q, want %q (err %v)", codeOf(err), c.code, err)
			}
			if c.code != "" {
				if got.Status != c.tag.Status {
					t.Fatalf("a rejected entry changed the status to %s", got.Status)
				}
				return
			}
			if got.Status != TagUsed || got.ProductID != c.product || !got.Qty.Equal(d(c.qty)) {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestUseTag_TakesThePlanWarehouseAndNormalizes(t *testing.T) {
	got, err := UseTag(CountTag{Number: "TAG-1", Status: TagOpen}, "W1", "p1", Dimensions{DimSite: " S1 ", DimBatch: ""}, d("4"), 6)
	if err != nil {
		t.Fatal(err)
	}
	if got.Dims.Canonical() != (Dimensions{DimSite: "S1", DimWarehouse: "W1"}).Canonical() {
		t.Fatalf("dims = %v, want site S1 and the plan's warehouse W1", got.Dims)
	}
}

func TestVoidTag_Rules(t *testing.T) {
	cases := []struct {
		name string
		tag  CountTag
		ok   bool
	}{
		{"open voids", CountTag{Status: TagOpen}, true},
		{"used voids before transfer", CountTag{Status: TagUsed}, true},
		{"used on sheet cannot void", CountTag{Status: TagUsed, SheetID: "s1"}, false},
		{"void cannot void again", CountTag{Status: TagVoid}, false},
		{"missing cannot void", CountTag{Status: TagMissing}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := VoidTag(c.tag)
			if (err == nil) != c.ok {
				t.Fatalf("err = %v, want ok=%v", err, c.ok)
			}
			if c.ok && got.Status != TagVoid {
				t.Fatalf("status = %s", got.Status)
			}
			if !c.ok && codeOf(err) != CodeTagState {
				t.Fatalf("code = %q", codeOf(err))
			}
		})
	}
}

// AC-16.13 in the domain: 50 tags, 48 used, 1 void, 1 open. The transfer is
// refused until the open tag is marked missing; then one line per product x
// dimension set carries the summed quantities and the void tag is left out.
func TestPlanTagTransfer_AC16_13(t *testing.T) {
	w1 := Dimensions{DimSite: "S1", DimWarehouse: "W1"}
	var tags []CountTag
	for i := 1; i <= 48; i++ {
		product := fmt.Sprintf("p%d", i%3)
		tags = append(tags, CountTag{ID: fmt.Sprintf("u%d", i), Status: TagUsed, ProductID: product, Dims: w1, Qty: d("2")})
	}
	tags = append(tags, CountTag{ID: "v", Status: TagVoid, ProductID: "p0", Dims: w1, Qty: d("100")}, CountTag{ID: "o", Status: TagOpen})

	if _, err := PlanTagTransfer(tags, false); codeOf(err) != CodeTagOpen {
		t.Fatalf("open tag must block the transfer: %v", err)
	}
	plan, err := PlanTagTransfer(tags, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.MissingIDs) != 1 || plan.MissingIDs[0] != "o" {
		t.Fatalf("missing = %v", plan.MissingIDs)
	}
	if len(plan.Lines) != 3 {
		t.Fatalf("lines = %d, want one per product", len(plan.Lines))
	}
	for i, l := range plan.Lines {
		if !l.Qty.Equal(d("32")) || len(l.TagIDs) != 16 {
			t.Fatalf("line %s = %s from %d tags, want 32 from 16", l.ProductID, l.Qty, len(l.TagIDs))
		}
		if i > 0 && plan.Lines[i-1].ProductID >= l.ProductID {
			t.Fatalf("lines are not in lock order: %s before %s", plan.Lines[i-1].ProductID, l.ProductID)
		}
	}
}

func TestPlanTagTransfer_SplitsByDimensionSetAndKeepsZeroCounts(t *testing.T) {
	tags := []CountTag{
		{ID: "a", Status: TagUsed, ProductID: "p1", Dims: Dimensions{DimSite: "S1", DimBatch: "B1"}, Qty: d("1")},
		{ID: "b", Status: TagUsed, ProductID: "p1", Dims: Dimensions{DimSite: "S1", DimBatch: "B2"}, Qty: d("0")},
	}
	plan, err := PlanTagTransfer(tags, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Lines) != 2 {
		t.Fatalf("lines = %+v, want one per batch (a zero count is a count)", plan.Lines)
	}
}

func TestPlanTagTransfer_NothingUsed(t *testing.T) {
	_, err := PlanTagTransfer([]CountTag{{ID: "x", Status: TagVoid}}, false)
	if codeOf(err) != CodeTagNoneUsed {
		t.Fatalf("code = %q", codeOf(err))
	}
}

func TestPlanTagTransfer_SkipsTransferred(t *testing.T) {
	tags := []CountTag{
		{ID: "a", Status: TagUsed, ProductID: "p1", Dims: Dimensions{DimSite: "S1"}, Qty: d("1"), SheetID: "s1"},
		{ID: "b", Status: TagUsed, ProductID: "p1", Dims: Dimensions{DimSite: "S1"}, Qty: d("4")},
	}
	plan, err := PlanTagTransfer(tags, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Lines) != 1 || !plan.Lines[0].Qty.Equal(d("4")) {
		t.Fatalf("lines = %+v", plan.Lines)
	}
}
