package domain

import (
	"encoding/json"
	"reflect"
	"testing"

	"pgregory.net/rapid"
)

func mustDoc(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestDiff_QtyChangeOnLineZero(t *testing.T) {
	a := mustDoc(t, `{"lines":[{"lineNo":1,"qty":"100"}]}`)
	b := mustDoc(t, `{"lines":[{"lineNo":1,"qty":"110"}]}`)
	ops, err := Diff(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].Op != "replace" || ops[0].Path != "/lines/0/qty" || ops[0].Value != "110" {
		t.Fatalf("ops = %+v", ops)
	}
	got, err := Apply(a, ops)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := Normalize(b)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("apply = %v want %v", got, want)
	}
}

func TestDiff_IdenticalDocumentsGiveEmptyPatch(t *testing.T) {
	a := mustDoc(t, `{"x":[1,2,{"y":null}]}`)
	ops, err := Diff(a, a)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Fatalf("want no ops, got %+v", ops)
	}
}

func TestPatchOp_JSONShape(t *testing.T) {
	b, err := json.Marshal([]PatchOp{{Op: "remove", Path: "/a"}, {Op: "replace", Path: "/b", Value: nil}})
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"op":"remove","path":"/a"},{"op":"replace","path":"/b","value":null}]`
	if string(b) != want {
		t.Fatalf("json = %s want %s", b, want)
	}
}

func TestPointerEscaping(t *testing.T) {
	a := mustDoc(t, `{"a/b~c":1}`)
	b := mustDoc(t, `{"a/b~c":2}`)
	ops, err := Diff(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].Path != "/a~1b~0c" {
		t.Fatalf("ops = %+v", ops)
	}
	got, err := Apply(a, ops)
	if err != nil {
		t.Fatal(err)
	}
	want, err := Normalize(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("apply = %v", got)
	}
}

func TestApply_RejectsInvalidOps(t *testing.T) {
	doc := mustDoc(t, `{"lines":[{"qty":"1"}]}`)
	tests := []struct {
		name string
		op   PatchOp
	}{
		{"remove missing key", PatchOp{Op: "remove", Path: "/nope"}},
		{"replace missing key", PatchOp{Op: "replace", Path: "/nope", Value: 1}},
		{"index out of range", PatchOp{Op: "replace", Path: "/lines/4/qty", Value: "2"}},
		{"path without slash", PatchOp{Op: "replace", Path: "lines", Value: 1}},
		{"remove root", PatchOp{Op: "remove", Path: ""}},
		{"descend into scalar", PatchOp{Op: "replace", Path: "/lines/0/qty/deeper", Value: 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Apply(doc, []PatchOp{tc.op}); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

// genValue builds a random JSON document of bounded depth from a small key
// alphabet that includes characters needing pointer escaping.
func genValue(t *rapid.T, depth int) any {
	kind := rapid.IntRange(0, 4).Draw(t, "kind")
	if depth >= 3 {
		kind = kind % 3
	}
	switch kind {
	case 0:
		return rapid.IntRange(-5, 5).Draw(t, "int")
	case 1:
		return rapid.SampledFrom([]string{"", "a", "100", "x/y", "t~u"}).Draw(t, "str")
	case 2:
		return rapid.Bool().Draw(t, "bool")
	case 3:
		m := map[string]any{}
		n := rapid.IntRange(0, 3).Draw(t, "mapLen")
		for range n {
			k := rapid.SampledFrom([]string{"a", "b", "c", "x/y", "t~u"}).Draw(t, "key")
			m[k] = genValue(t, depth+1)
		}
		return m
	default:
		s := []any{}
		n := rapid.IntRange(0, 4).Draw(t, "sliceLen")
		for range n {
			s = append(s, genValue(t, depth+1))
		}
		return s
	}
}

func TestDiffApply_RoundTripProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		a := genValue(t, 0)
		b := genValue(t, 0)
		ops, err := Diff(a, b)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Apply(a, ops)
		if err != nil {
			t.Fatalf("apply failed: %v (ops %+v)", err, ops)
		}
		want, err := Normalize(b)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("apply(diff(a,b), a) = %v, want b = %v", got, want)
		}
	})
}
