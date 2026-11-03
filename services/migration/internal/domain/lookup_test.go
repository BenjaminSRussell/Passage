package domain_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

type fakeTables map[string]domain.LookupAnswer

func (f fakeTables) LookupValues(_ context.Context, table string, keys []string) (domain.LookupAnswer, error) {
	ans, ok := f[table]
	if !ok {
		return domain.LookupAnswer{}, nil
	}
	out := domain.LookupAnswer{Found: true, Archived: ans.Archived, Kind: ans.Kind, Version: ans.Version, Values: map[string]string{}}
	for _, k := range keys {
		if v, ok := ans.Values[k]; ok && !ans.Archived {
			out.Values[k] = v
		}
	}
	return out, nil
}

// keysAndTables resolves both the legacy keys and the lookup tables, as the store does.
type keysAndTables struct {
	fakeKeys
	fakeTables
}

const lookupOutboundDef = `{
  "eventType": "dev.passage.sales.order.*.v1",
  "xrefs": [{"name": "customer", "entity": "customer", "idPath": "data.customer_id"}],
  "lookups": [
    {"name": "unit", "table": "unit-codes", "keyPaths": ["data.uom"]},
    {"name": "factor", "table": "uom", "keyPaths": ["data.uom", "data.base_uom"], "default": "1"},
    {"name": "price", "table": "price-list", "keyPaths": ["data.sku", "data.currency"], "optional": true}
  ],
  "decimalsAsNumbers": true,
  "mapping": {"entity": "SalesOrder", "fields": [
    {"source": "xref.customer", "target": "CustAccount", "type": "string", "required": true},
    {"source": "lookup.unit", "target": "Line.Unit", "type": "string", "required": true},
    {"source": "lookup.factor", "target": "Line.Factor", "type": "decimal", "scale": 6},
    {"source": "lookup.price", "target": "Line.Price", "type": "decimal", "scale": 2}
  ]}
}`

func lookupEvent(t *testing.T, data string) map[string]any {
	t.Helper()
	ev, err := domain.DecodeEvent(json.RawMessage(`{"id":"evt-1","type":"dev.passage.sales.order.confirmed.v1","data":` + data + `}`))
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

func lookupResolver() keysAndTables {
	return keysAndTables{
		fakeKeys{"customer|" + custID: "C-1001"},
		fakeTables{
			"unit-codes": {Kind: domain.LookupEnum, Version: 3, Values: map[string]string{"kg": "KGM", "ea": "EA"}},
			"uom":        {Kind: domain.LookupUoM, Version: 1, Values: map[string]string{"kg|lb": "2.20462"}},
			"price-list": {Kind: domain.LookupPrice, Version: 2, Values: map[string]string{"SKU-1|EUR": "12.5"}},
		},
	}
}

func TestOutboundLookups_RenderEnumFactorAndPrice(t *testing.T) {
	def, err := domain.ParseOutboundDefinition([]byte(lookupOutboundDef), 1)
	if err != nil {
		t.Fatal(err)
	}
	ev := lookupEvent(t, `{"customer_id":"`+custID+`","uom":"kg","base_uom":"lb","sku":"SKU-1","currency":"EUR"}`)
	got, err := def.Render(context.Background(), "custom_acme", "m-1", ev, lookupResolver())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(got.Body)
	if string(raw) != `{"CustAccount":"C-1001","Line":{"Factor":2.204620,"Price":12.50,"Unit":"KGM"}}` {
		t.Fatalf("body = %s", raw)
	}
	// The render names the table versions it read, so a pushed body can be traced to its entries.
	if uses, _ := json.Marshal(got.Lookups); string(uses) != `[{"name":"unit","table":"unit-codes","version":3},{"name":"factor","table":"uom","version":1},{"name":"price","table":"price-list","version":2}]` {
		t.Fatalf("lookups = %s", uses)
	}

	// No entry for the factor falls back to its default; no price is left out (optional); the unit is required.
	ev = lookupEvent(t, `{"customer_id":"`+custID+`","uom":"ea","base_uom":"ea","sku":"SKU-9","currency":"EUR"}`)
	got, err = def.Render(context.Background(), "custom_acme", "m-1", ev, lookupResolver())
	raw, _ = json.Marshal(got.Body)
	if err != nil || string(raw) != `{"CustAccount":"C-1001","Line":{"Factor":1.000000,"Unit":"EA"}}` {
		t.Fatalf("defaults = %s %v", raw, err)
	}
}

func TestOutboundLookups_UnresolvedNamesTableKeyAndReason(t *testing.T) {
	def, err := domain.ParseOutboundDefinition([]byte(lookupOutboundDef), 1)
	if err != nil {
		t.Fatal(err)
	}
	r := lookupResolver()
	// An unknown code, with the legacy key missing as well: every problem is listed at once.
	ev := lookupEvent(t, `{"customer_id":"`+custID+`","uom":"box","base_uom":"lb"}`)
	_, err = def.Render(context.Background(), "custom_acme", "m-1", ev, r)
	var ue *domain.UnresolvedError
	if !errors.As(err, &ue) || len(ue.Refs) != 1 || ue.Refs[0] != (domain.UnresolvedRef{Name: "unit", Entity: "lookup:unit-codes", ID: "box", Reason: "the table has no entry for this key"}) {
		t.Fatalf("err = %v", err)
	}
	// A missing event field, a missing table, and an archived one.
	_, err = def.Render(context.Background(), "custom_acme", "m-1", lookupEvent(t, `{"customer_id":"`+custID+`"}`), r)
	if !errors.As(err, &ue) || !strings.Contains(ue.Refs[0].Reason, "no value at data.uom") {
		t.Fatalf("missing field: %v", err)
	}
	delete(r.fakeTables, "unit-codes")
	_, err = def.Render(context.Background(), "custom_acme", "m-1", lookupEvent(t, `{"customer_id":"`+custID+`","uom":"kg","base_uom":"lb"}`), r)
	if !errors.As(err, &ue) || ue.Refs[0].Reason != "no lookup table named unit-codes" {
		t.Fatalf("missing table: %v", err)
	}
	r.fakeTables["unit-codes"] = domain.LookupAnswer{Archived: true}
	_, err = def.Render(context.Background(), "custom_acme", "m-1", lookupEvent(t, `{"customer_id":"`+custID+`","uom":"kg","base_uom":"lb"}`), r)
	if !errors.As(err, &ue) || ue.Refs[0].Reason != "lookup table unit-codes is archived" {
		t.Fatalf("archived table: %v", err)
	}
	// A default or optional covers a missing entry, never a table that is missing or archived: the factor has a
	// default and the price is optional, yet both fail when their tables are gone.
	delete(r.fakeTables, "uom")
	r.fakeTables["price-list"] = domain.LookupAnswer{Archived: true}
	r.fakeTables["unit-codes"] = lookupResolver().fakeTables["unit-codes"]
	_, err = def.Render(context.Background(), "custom_acme", "m-1", lookupEvent(t, `{"customer_id":"`+custID+`","uom":"kg","base_uom":"lb","sku":"SKU-1","currency":"EUR"}`), r)
	if !errors.As(err, &ue) || len(ue.Refs) != 2 || ue.Refs[0].Reason != "no lookup table named uom" || ue.Refs[1].Reason != "lookup table price-list is archived" {
		t.Fatalf("missing tables behind a default and an optional: %v", err)
	}
	// A key part holding the separator never matches an entry, not even a one-part key against a two-part entry.
	_, err = def.Render(context.Background(), "custom_acme", "m-1", lookupEvent(t, `{"customer_id":"`+custID+`","uom":"kg|lb"}`), lookupResolver())
	if !errors.As(err, &ue) || ue.Refs[0].Name != "unit" || !strings.Contains(ue.Refs[0].Reason, "contains |") {
		t.Fatalf("separator in a one-part key: %v", err)
	}
	// A composite key part holding the separator never matches an entry (here the default covers the factor).
	_, err = def.Render(context.Background(), "custom_acme", "m-1", lookupEvent(t, `{"customer_id":"`+custID+`","uom":"kg","base_uom":"l|b"}`), lookupResolver())
	if err != nil {
		t.Fatalf("a default covers the factor: %v", err)
	}
}

func TestOutboundLookups_ResolverWithoutTablesIsRefused(t *testing.T) {
	def, err := domain.ParseOutboundDefinition([]byte(lookupOutboundDef), 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = def.Render(context.Background(), "custom_acme", "m-1", lookupEvent(t, `{"customer_id":"`+custID+`"}`), fakeKeys{})
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeOutboundInvalid {
		t.Fatalf("err = %v", err)
	}
}

func TestOutboundLookups_DefinitionIsValidated(t *testing.T) {
	base := func(lookup string) string {
		return `{"eventType":"dev.passage.sales.order.*.v1","lookups":[` + lookup + `],"mapping":{"entity":"E","fields":[{"source":"data.a","target":"A","type":"string"}]}}`
	}
	for name, lookup := range map[string]string{
		"bad name":      `{"name":"Unit","table":"t","keyPaths":["data.a"]}`,
		"bad table":     `{"name":"unit","table":"1 t","keyPaths":["data.a"]}`,
		"no key":        `{"name":"unit","table":"t","keyPaths":[]}`,
		"too many keys": `{"name":"unit","table":"t","keyPaths":["a","b","c","d"]}`,
		"bad path":      `{"name":"unit","table":"t","keyPaths":["data..a"]}`,
		"default+opt":   `{"name":"unit","table":"t","keyPaths":["data.a"],"default":"x","optional":true}`,
		"empty default": `{"name":"unit","table":"t","keyPaths":["data.a"],"default":""}`,
		"duplicate":     `{"name":"unit","table":"t","keyPaths":["data.a"]},{"name":"unit","table":"u","keyPaths":["data.a"]}`,
		"unknown field": `{"name":"unit","table":"t","keyPaths":["data.a"],"extra":1}`,
	} {
		if _, err := domain.ParseOutboundDefinition([]byte(base(lookup)), 1); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	nine := strings.Repeat(`{"name":"a","table":"t","keyPaths":["data.a"]},`, 9)
	if _, err := domain.ParseOutboundDefinition([]byte(base(strings.TrimSuffix(nine, ","))), 1); err == nil {
		t.Error("nine lookups accepted")
	}
	if _, err := domain.ParseOutboundDefinition([]byte(base(`{"name":"unit","table":"t","keyPaths":["data.a","data.b"],"default":"x"}`)), 1); err != nil {
		t.Errorf("valid lookup: %v", err)
	}
}

func TestParseLookupDefinition(t *testing.T) {
	for name, tc := range map[string]struct {
		def string
		ok  bool
	}{
		"enum":                {`{"kind":"enum","entries":{"kg":"KGM"}}`, true},
		"empty table":         {`{"kind":"enum","entries":{}}`, true},
		"uom":                 {`{"kind":"uom_conversion","entries":{"KG|LB":"2.20462"}}`, true},
		"price":               {`{"kind":"price_list","entries":{"SKU-1|EUR":"0","SKU-2":"12.50"}}`, true},
		"no kind":             {`{"entries":{"a":"b"}}`, false},
		"bad kind":            {`{"kind":"map","entries":{"a":"b"}}`, false},
		"unknown field":       {`{"kind":"enum","entries":{},"x":1}`, false},
		"empty key":           {`{"kind":"enum","entries":{"":"b"}}`, false},
		"padded key":          {`{"kind":"enum","entries":{" a":"b"}}`, false},
		"empty value":         {`{"kind":"enum","entries":{"a":""}}`, false},
		"long key":            {`{"kind":"enum","entries":{"` + strings.Repeat("k", 201) + `":"b"}}`, false},
		"empty key part":      {`{"kind":"enum","entries":{"a|":"b"}}`, false},
		"four key parts":      {`{"kind":"enum","entries":{"a|b|c|d":"b"}}`, false},
		"uom needs two units": {`{"kind":"uom_conversion","entries":{"KG":"2"}}`, false},
		"uom same unit":       {`{"kind":"uom_conversion","entries":{"KG|KG":"1"}}`, false},
		"uom zero factor":     {`{"kind":"uom_conversion","entries":{"KG|LB":"0"}}`, false},
		"uom text factor":     {`{"kind":"uom_conversion","entries":{"KG|LB":"two"}}`, false},
		"uom exponent":        {`{"kind":"uom_conversion","entries":{"KG|LB":"1e3"}}`, false},
		"uom json number":     {`{"kind":"uom_conversion","entries":{"KG|LB":2.2}}`, false},
		"uom exact factor":    {`{"kind":"uom_conversion","entries":{"OZ|KG":"0.028349523125"}}`, true},
		"uom factor scale":    {`{"kind":"uom_conversion","entries":{"OZ|KG":"0.0283495231251"}}`, false},
		"no entries":          {`{"kind":"enum"}`, false},
		"null entries":        {`{"kind":"enum","entries":null}`, false},
		"price negative":      {`{"kind":"price_list","entries":{"SKU":"-1"}}`, false},
		"price scale":         {`{"kind":"price_list","entries":{"SKU":"1.1234567"}}`, false},
		"price magnitude":     {`{"kind":"price_list","entries":{"SKU":"100000000000000"}}`, false},
	} {
		_, err := domain.ParseLookupDefinition([]byte(tc.def))
		if (err == nil) != tc.ok {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	tooMany := map[string]string{}
	for i := 0; i < 5001; i++ {
		tooMany[strings.Repeat("k", 1)+string(rune('a'+i%26))+strings.Repeat("x", i/26)] = "v"
	}
	raw, _ := json.Marshal(domain.LookupDefinition{Kind: "enum", Entries: tooMany})
	if _, err := domain.ParseLookupDefinition(raw); err == nil {
		t.Error("5001 entries accepted")
	}
}

// A unit conversion with keyConstants and a quantityPath converts the event quantity to the legacy unit exactly; equal
// units convert to themselves; a price list cannot convert; the mapping field rounds to its scale.
func TestOutboundLookups_ConvertQuantity(t *testing.T) {
	def, err := domain.ParseOutboundDefinition([]byte(`{
	  "eventType": "dev.passage.sales.order.*.v1",
	  "lookups": [
	    {"name": "qtyKg", "table": "uom", "keyPaths": ["data.unit"], "keyConstants": ["KG"], "quantityPath": "data.qty"},
	    {"name": "price", "table": "prices", "keyPaths": ["legalentityid", "data.currency", "data.sku"]}
	  ],
	  "mapping": {"entity": "SalesOrder", "fields": [
	    {"source": "lookup.qtyKg", "target": "Qty", "type": "decimal", "scale": 3},
	    {"source": "lookup.price", "target": "Price", "type": "decimal", "scale": 2}
	  ]}
	}`), 1)
	if err != nil {
		t.Fatal(err)
	}
	le := "6f1c1f9e-8c39-4c43-9d0e-6a8a1f2b3c4d"
	r := fakeTables{
		"uom":    {Kind: domain.LookupUoM, Version: 4, Values: map[string]string{"LB|KG": "0.45359237", "OZ|KG": "0.028349523125"}},
		"prices": {Kind: domain.LookupPrice, Version: 1, Values: map[string]string{le + "|EUR|SKU-1": "12.5", "other-le|EUR|SKU-1": "99"}},
	}
	render := func(data string) (map[string]any, error) {
		ev, err := domain.DecodeEvent(json.RawMessage(`{"id":"evt-1","type":"dev.passage.sales.order.confirmed.v1","legalentityid":"` + le + `","data":` + data + `}`))
		if err != nil {
			t.Fatal(err)
		}
		got, err := def.Render(context.Background(), "custom_acme", "m-1", ev, keysAndTables{fakeKeys{}, r})
		return got.Body, err
	}
	for data, want := range map[string]string{
		`{"unit":"LB","qty":"10","currency":"EUR","sku":"SKU-1"}`:    `{"Price":"12.50","Qty":"4.536"}`,
		`{"unit":"OZ","qty":16,"currency":"EUR","sku":"SKU-1"}`:      `{"Price":"12.50","Qty":"0.454"}`,
		`{"unit":"KG","qty":"2.5","currency":"EUR","sku":"SKU-1"}`:   `{"Price":"12.50","Qty":"2.500"}`,
		`{"unit":"LB","qty":"0.001","currency":"EUR","sku":"SKU-1"}`: `{"Price":"12.50","Qty":"0.000"}`,
	} {
		body, err := render(data)
		raw, _ := json.Marshal(body)
		if err != nil || string(raw) != want {
			t.Errorf("%s = %s %v, want %s", data, raw, err, want)
		}
	}
	var ue *domain.UnresolvedError
	for data, reason := range map[string]string{
		`{"unit":"G","qty":"1","currency":"EUR","sku":"SKU-1"}`:    "the table has no entry for this key",
		`{"unit":"LB","currency":"EUR","sku":"SKU-1"}`:             "the event has no quantity at data.qty",
		`{"unit":"LB","qty":"ten","currency":"EUR","sku":"SKU-1"}`: "the quantity at data.qty is not a decimal",
		`{"unit":"LB","qty":"1","currency":"USD","sku":"SKU-1"}`:   "the table has no entry for this key",
	} {
		if _, err := render(data); !errors.As(err, &ue) || ue.Refs[0].Reason != reason {
			t.Errorf("%s: %v, want %s", data, err, reason)
		}
	}
	// The price key is per legal entity: the event's legal entity is part of it.
	if _, err := render(`{"unit":"LB","qty":"1","currency":"USD","sku":"SKU-1"}`); !errors.As(err, &ue) || ue.Refs[0].ID != le+"|USD|SKU-1" {
		t.Errorf("price key = %v", err)
	}
	r["uom"] = domain.LookupAnswer{Kind: domain.LookupPrice, Version: 1, Values: map[string]string{"LB|KG": "1"}}
	if _, err := render(`{"unit":"LB","qty":"1","currency":"EUR","sku":"SKU-1"}`); !errors.As(err, &ue) || !strings.Contains(ue.Refs[0].Reason, "is not a unit conversion table") {
		t.Errorf("convert through a price list: %v", err)
	}
}

func TestOutboundLookups_KeyConstantsAndQuantityAreValidated(t *testing.T) {
	base := func(lookup string) string {
		return `{"eventType":"dev.passage.sales.order.*.v1","lookups":[` + lookup + `],"mapping":{"entity":"E","fields":[{"source":"data.a","target":"A","type":"string"}]}}`
	}
	for name, lookup := range map[string]string{
		"constant with separator": `{"name":"q","table":"t","keyPaths":["data.a"],"keyConstants":["K|G"]}`,
		"empty constant":          `{"name":"q","table":"t","keyPaths":["data.a"],"keyConstants":[""]}`,
		"padded constant":         `{"name":"q","table":"t","keyPaths":["data.a"],"keyConstants":[" KG"]}`,
		"four parts":              `{"name":"q","table":"t","keyPaths":["data.a","data.b"],"keyConstants":["X","Y"]}`,
		"constants only":          `{"name":"q","table":"t","keyPaths":[],"keyConstants":["X"]}`,
		"bad quantity path":       `{"name":"q","table":"t","keyPaths":["data.a"],"quantityPath":"data..q"}`,
	} {
		if _, err := domain.ParseOutboundDefinition([]byte(base(lookup)), 1); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := domain.ParseOutboundDefinition([]byte(base(`{"name":"q","table":"t","keyPaths":["data.a"],"keyConstants":["KG"],"quantityPath":"data.q"}`)), 1); err != nil {
		t.Errorf("valid conversion lookup: %v", err)
	}
}
