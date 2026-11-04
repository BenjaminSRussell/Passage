package domain

import "testing"

func TestValidCategoryCode(t *testing.T) {
	for code, want := range map[string]bool{
		"phase-start": true, "pi-weigh-2": true, "9lives": true,
		"": false, "Upper": false, "-lead": false, "has space": false, "under_score": false,
		"abcdefghijabcdefghijabcdefghijabc": false, // 33 characters
	} {
		if got := ValidCategoryCode(code); got != want {
			t.Errorf("ValidCategoryCode(%q) = %v, want %v", code, got, want)
		}
	}
}

func TestCategoryInput_FieldErrors(t *testing.T) {
	ok := CategoryInput{Code: "pi-weigh", Name: "Weigh", Destinations: []string{DestEBR, DestKafka},
		Characteristics: []CharacteristicDef{{Name: "w", Type: "numeric", Min: sp("1"), Max: sp("2")}}}
	if f := ok.FieldErrors(true); len(f) != 0 {
		t.Fatalf("valid category: %v", f)
	}
	cases := map[string]struct {
		mut func(*CategoryInput)
		key string
	}{
		"code":    {func(c *CategoryInput) { c.Code = "Bad Code" }, "code"},
		"name":    {func(c *CategoryInput) { c.Name = "" }, "name"},
		"no dest": {func(c *CategoryInput) { c.Destinations = nil }, "destinations"},
		"unknown": {func(c *CategoryInput) { c.Destinations = []string{"fax"} }, "destinations"},
		"repeat":  {func(c *CategoryInput) { c.Destinations = []string{DestEBR, DestEBR} }, "destinations"},
		"bound":   {func(c *CategoryInput) { c.Characteristics[0].Min = sp("x") }, "characteristics[0].min"},
		"order":   {func(c *CategoryInput) { c.Characteristics[0].Min = sp("3") }, "characteristics[0].min"},
		"text limits": {func(c *CategoryInput) {
			c.Characteristics[0] = CharacteristicDef{Name: "t", Type: "text", Max: sp("1")}
		}, "characteristics[0].min"},
		"type": {func(c *CategoryInput) { c.Characteristics[0].Type = "date" }, "characteristics[0].type"},
		"dup name": {func(c *CategoryInput) {
			c.Characteristics = append(c.Characteristics, CharacteristicDef{Name: "w", Type: "text"})
		}, "characteristics[1].name"},
	}
	for name, c := range cases {
		in := ok
		in.Characteristics = append([]CharacteristicDef(nil), ok.Characteristics...)
		c.mut(&in)
		if f := in.FieldErrors(true); f[c.key] == "" {
			t.Errorf("%s: no error on %s in %v", name, c.key, f)
		}
	}
	// The code is not checked on update: it never changes.
	upd := ok
	upd.Code = ""
	if f := upd.FieldErrors(false); len(f) != 0 {
		t.Errorf("update with no code: %v", f)
	}
}

// BR-25.13: a definition governs the limits of the value it names, so a kiosk cannot widen them,
// and a category that rejects out-of-limit values refuses a value outside the defined limits.
func TestApplyLimits_DefinitionsGovernValues(t *testing.T) {
	cat := Category{Code: "pi-weigh", RejectOutOfLimit: true, Characteristics: []CharacteristicDef{
		{Name: "weight", Type: "numeric", Unit: sp("kg"), Min: sp("9.5"), Max: sp("10.5"), Required: true},
		{Name: "note", Type: "text"},
	}}
	out, err := ApplyLimits(cat, []Characteristic{{Name: "weight", Type: "numeric", Value: "10.0", Min: sp("0"), Max: sp("999")}, {Name: "extra", Type: "boolean", Value: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Unit == nil || *out[0].Unit != "kg" || *out[0].Min != "9.5" || *out[0].Max != "10.5" || out[0].OutOfLimit {
		t.Fatalf("definition not applied: %+v", out[0])
	}
	if out[1].Name != "extra" {
		t.Fatalf("a value with no definition passes through: %+v", out)
	}

	// The inline limits 0 to 999 would have accepted 11; the defined ones refuse it.
	_, err = ApplyLimits(cat, []Characteristic{{Name: "weight", Type: "numeric", Value: "11", Min: sp("0"), Max: sp("999")}})
	if e, ok := err.(*Error); !ok || e.Code != CodeOutOfLimitRejected {
		t.Fatalf("out of defined limits = %v, want %s", err, CodeOutOfLimitRejected)
	}
	// Without the reject flag the value is accepted and flagged.
	cat.RejectOutOfLimit = false
	out, err = ApplyLimits(cat, []Characteristic{{Name: "weight", Type: "numeric", Value: "11"}})
	if err != nil || !out[0].OutOfLimit {
		t.Fatalf("flagged = %+v, %v", out, err)
	}
	// Wrong type for a defined name, and a missing required value, are SF-0115.
	if _, err = ApplyLimits(cat, []Characteristic{{Name: "weight", Type: "text", Value: "heavy"}}); err == nil || err.(*Error).Code != CodeInvalidValue {
		t.Fatalf("wrong type = %v", err)
	}
	if _, err = ApplyLimits(cat, []Characteristic{{Name: "note", Type: "text", Value: "x"}}); err == nil || err.(*Error).Code != CodeInvalidValue {
		t.Fatalf("missing required = %v", err)
	}
}
