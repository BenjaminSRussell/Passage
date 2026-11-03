package domain_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

const linesDef = `{
  "eventType": "dev.passage.sales.order.*.v1",
  "xrefs": [{"name": "customer", "entity": "customer", "idPath": "data.customer_id"}],
  "decimalsAsNumbers": true,
  "mapping": {"entity": "SalesOrder", "fields": [
    {"source": "xref.customer", "target": "CustAccount", "type": "string", "required": true},
    {"source": "data.number", "target": "Header.CustomerRef", "type": "string", "required": true}
  ]},
  "collections": [{
    "name": "Lines", "path": "data.lines", "maxItems": 3,
    "xrefs": [{"name": "item", "entity": "product", "idPath": "product_id"}],
    "lookups": [
      {"name": "uom", "table": "uom", "keyPaths": ["unit"], "keyConstants": ["EA"], "quantityPath": "qty"},
      {"name": "price", "table": "prices", "keyPaths": ["parent.data.currency", "sku"], "optional": true}
    ],
    "fields": [
      {"source": "xref.item", "target": "ItemNo", "type": "string", "required": true},
      {"source": "index", "target": "LineNo", "type": "int"},
      {"source": "lookup.uom", "target": "Qty.Base", "type": "decimal", "scale": 3},
      {"source": "lookup.price", "target": "Price", "type": "decimal", "scale": 2},
      {"source": "parent.data.currency", "target": "Currency", "type": "string"}
    ]
  }]
}`

const (
	prodA = "11111111-1111-4111-8111-111111111111"
	prodB = "22222222-2222-4222-8222-222222222222"
)

type countingTables struct {
	fakeTables
	calls *int
}

func (c countingTables) LookupValues(ctx context.Context, table string, keys []string) (domain.LookupAnswer, error) {
	*c.calls++
	return c.fakeTables.LookupValues(ctx, table, keys)
}

func linesResolver(calls *int) keysAndTablesCounting {
	return keysAndTablesCounting{
		fakeKeys{"customer|" + custID: "C-1", "product|" + prodA: "P-A", "product|" + prodB: "P-B"},
		countingTables{fakeTables{
			"uom":    {Found: true, Kind: domain.LookupUoM, Version: 2, Values: map[string]string{"BX|EA": "12"}},
			"prices": {Found: true, Kind: domain.LookupPrice, Version: 5, Values: map[string]string{"USD|SKU-A": "3.5"}},
		}, calls},
	}
}

type keysAndTablesCounting struct {
	fakeKeys
	countingTables
}

func linesEvent(t *testing.T, lines string) map[string]any {
	t.Helper()
	return lookupEvent(t, `{"customer_id":"`+custID+`","number":"SO-9","currency":"USD","lines":`+lines+`}`)
}

func TestOutboundCollection_OneBodyCarriesEveryLine(t *testing.T) {
	def, err := domain.ParseOutboundDefinition([]byte(linesDef), 1)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	ev := linesEvent(t, `[{"product_id":"`+prodA+`","sku":"SKU-A","unit":"BX","qty":"2"},{"product_id":"`+prodB+`","sku":"SKU-B","unit":"BX","qty":"1.5"},{"product_id":"`+prodA+`","sku":"SKU-A","unit":"BX","qty":"1"}]`)
	got, err := def.Render(context.Background(), "custom_acme", "m-1", ev, linesResolver(&calls))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(got.Body)
	want := `{"CustAccount":"C-1","Header":{"CustomerRef":"SO-9"},"Lines":[` +
		`{"Currency":"USD","ItemNo":"P-A","LineNo":0,"Price":3.50,"Qty":{"Base":24.000}},` +
		`{"Currency":"USD","ItemNo":"P-B","LineNo":1,"Qty":{"Base":18.000}},` +
		`{"Currency":"USD","ItemNo":"P-A","LineNo":2,"Price":3.50,"Qty":{"Base":12.000}}]}`
	if string(raw) != want {
		t.Fatalf("body = %s\nwant   %s", raw, want)
	}
	// Each table is read once for the whole render, whatever the number of lines and keys.
	if calls != 2 {
		t.Fatalf("lookup reads = %d, want 2 (one per table)", calls)
	}
	seen := map[string]int{}
	for _, u := range got.Lookups {
		seen[u.Name] = u.Version
	}
	if seen["Lines.uom"] != 2 || seen["Lines.price"] != 5 || len(got.Lookups) != 2 {
		t.Fatalf("lookups = %+v", got.Lookups)
	}
	// The idempotency key and refs are the event's, not the lines'.
	if got.IdempotencyKey != domain.OutboundIdempotencyKey("m-1", "evt-1") {
		t.Fatal("idempotency key changed")
	}
}

func TestOutboundCollection_UnresolvedNamesTheLine(t *testing.T) {
	def, err := domain.ParseOutboundDefinition([]byte(linesDef), 1)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	ev := linesEvent(t, `[{"product_id":"`+prodA+`","sku":"SKU-A","unit":"BX","qty":"2"},{"product_id":"33333333-3333-4333-8333-333333333333","sku":"X","unit":"PAL","qty":"1"}]`)
	_, err = def.Render(context.Background(), "custom_acme", "m-1", ev, linesResolver(&calls))
	var u *domain.UnresolvedError
	if !errors.As(err, &u) {
		t.Fatalf("err = %v", err)
	}
	names := map[string]bool{}
	for _, r := range u.Refs {
		names[r.Name] = true
	}
	if !names["Lines[1].item"] || !names["Lines[1].uom"] || len(u.Refs) != 2 {
		t.Fatalf("refs = %+v", u.Refs)
	}
}

func TestOutboundCollection_ArrayShapeIsRefused(t *testing.T) {
	def, err := domain.ParseOutboundDefinition([]byte(linesDef), 1)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"empty_array":    `[]`,
		"too_many_items": `[{},{},{},{}]`,
		"not_an_object":  `[1]`,
		"not_an_array":   `"x"`,
	}
	for code, lines := range cases {
		calls := 0
		_, err := def.Render(context.Background(), "custom_acme", "m-1", linesEvent(t, lines), linesResolver(&calls))
		var rej *domain.RejectedError
		if code == "not_an_object" {
			if !errors.As(err, &rej) || rej.Rejects[0].Code != code {
				t.Fatalf("%s: %v", code, err)
			}
			continue
		}
		if !errors.As(err, &rej) || len(rej.Rejects) != 1 || rej.Rejects[0].Code != code {
			t.Fatalf("%s: %v", code, err)
		}
	}
	// No array at all.
	ev := lookupEvent(t, `{"customer_id":"`+custID+`","number":"SO-9"}`)
	var rej *domain.RejectedError
	calls := 0
	if _, err := def.Render(context.Background(), "custom_acme", "m-1", ev, linesResolver(&calls)); !errors.As(err, &rej) || rej.Rejects[0].Code != "missing_array" {
		t.Fatalf("missing: %v", err)
	}
	// An optional collection is left out when the event has no array, and sent empty when the array is empty.
	opt := strings.Replace(linesDef, `"maxItems": 3,`, `"maxItems": 3, "optional": true,`, 1)
	od, err := domain.ParseOutboundDefinition([]byte(opt), 1)
	if err != nil {
		t.Fatal(err)
	}
	got, err := od.Render(context.Background(), "custom_acme", "m-1", ev, linesResolver(&calls))
	if err != nil {
		t.Fatal(err)
	}
	if _, has := got.Body["Lines"]; has {
		t.Fatalf("optional array rendered: %+v", got.Body)
	}
	got, err = od.Render(context.Background(), "custom_acme", "m-1", linesEvent(t, `[]`), linesResolver(&calls))
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(got.Body["Lines"]); string(raw) != `[]` {
		t.Fatalf("optional empty array = %s, want []", raw)
	}
	// Without maxItems the limit is 200: 201 lines are refused, not truncated.
	dflt := strings.Replace(linesDef, `"maxItems": 3,`, ``, 1)
	dd, err := domain.ParseOutboundDefinition([]byte(dflt), 1)
	if err != nil {
		t.Fatal(err)
	}
	many := "[" + strings.TrimSuffix(strings.Repeat(`{"product_id":"`+prodA+`","sku":"SKU-A","unit":"BX","qty":"1"},`, domain.DefaultCollectionItems+1), ",") + "]"
	if _, err := dd.Render(context.Background(), "custom_acme", "m-1", linesEvent(t, many), linesResolver(&calls)); !errors.As(err, &rej) || rej.Rejects[0].Code != "too_many_items" {
		t.Fatalf("201 lines: %v", err)
	}
}

// countingKeys counts the cross-reference queries and the ids each one asks for.
type countingKeys struct {
	fakeKeys
	calls *int
}

func (c countingKeys) LegacyKeys(ctx context.Context, sourceSystem, entity string, ids []string) (map[string]string, error) {
	*c.calls++
	return c.fakeKeys.LegacyKeys(ctx, sourceSystem, entity, ids)
}

// The largest order a collection allows renders with one cross-reference query per xref and one read per lookup table,
// so the render cost does not grow with the number of lines (the hub waits 15 s for an answer).
func TestOutboundCollection_ThousandLinesCostOneQueryPerXrefAndTable(t *testing.T) {
	d := strings.Replace(linesDef, `"maxItems": 3,`, `"maxItems": 1000,`, 1)
	// The header reads the price table too: it shares the lines' read, so header and lines see one table version.
	d = strings.Replace(d, `"decimalsAsNumbers": true,`, `"decimalsAsNumbers": true, "lookups": [{"name": "first", "table": "prices", "keyPaths": ["data.currency"], "keyConstants": ["SKU-A"]}],`, 1)
	def, err := domain.ParseOutboundDefinition([]byte(d), 1)
	if err != nil {
		t.Fatal(err)
	}
	keys := fakeKeys{"customer|" + custID: "C-1"}
	prices := map[string]string{"USD|SKU-A": "3.5"}
	var lines []string
	for i := range domain.MaxCollectionItems {
		id := fmt.Sprintf("%08x-0000-4000-8000-000000000000", i)
		keys["product|"+id] = fmt.Sprintf("P-%d", i)
		prices[fmt.Sprintf("USD|SKU-%d", i)] = fmt.Sprintf("%d.25", i)
		lines = append(lines, fmt.Sprintf(`{"product_id":"%s","sku":"SKU-%d","unit":"BX","qty":"1"}`, id, i))
	}
	xrefCalls, tableCalls := 0, 0
	resolver := struct {
		countingKeys
		countingTables
	}{countingKeys{keys, &xrefCalls}, countingTables{fakeTables{
		"uom":    {Found: true, Kind: domain.LookupUoM, Version: 2, Values: map[string]string{"BX|EA": "12"}},
		"prices": {Found: true, Kind: domain.LookupPrice, Version: 5, Values: prices},
	}, &tableCalls}}
	got, err := def.Render(context.Background(), "custom_acme", "m-1", linesEvent(t, "["+strings.Join(lines, ",")+"]"), resolver)
	if err != nil {
		t.Fatal(err)
	}
	out := got.Body["Lines"].([]any)
	if len(out) != domain.MaxCollectionItems {
		t.Fatalf("lines = %d", len(out))
	}
	last := out[len(out)-1].(map[string]any)
	if last["ItemNo"] != "P-999" || last["Price"] != json.Number("999.25") {
		t.Fatalf("last line = %+v", last)
	}
	// One query for the header's customer, one for all 1000 lines' products; one read per table.
	if xrefCalls != 2 || tableCalls != 2 {
		t.Fatalf("xref queries = %d, table reads = %d, want 2 and 2", xrefCalls, tableCalls)
	}
	if len(got.Lookups) != 3 {
		t.Fatalf("lookups = %+v, want first, Lines.uom and Lines.price", got.Lookups)
	}
}

func TestOutboundCollection_ElementRejectsCarryTheIndex(t *testing.T) {
	d := strings.Replace(linesDef, `{"source": "index", "target": "LineNo", "type": "int"}`, `{"source": "sku", "target": "LineNo", "type": "int", "required": true}`, 1)
	def, err := domain.ParseOutboundDefinition([]byte(d), 1)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	ev := linesEvent(t, `[{"product_id":"`+prodA+`","sku":"7","unit":"BX","qty":"2"},{"product_id":"`+prodB+`","sku":"SKU-B","unit":"BX","qty":"1"}]`)
	_, err = def.Render(context.Background(), "custom_acme", "m-1", ev, linesResolver(&calls))
	var rej *domain.RejectedError
	if !errors.As(err, &rej) || len(rej.Rejects) == 0 || !strings.HasPrefix(rej.Rejects[0].Field, "Lines[1].") {
		t.Fatalf("err = %v", err)
	}
}

func TestOutboundCollection_DefinitionRefusals(t *testing.T) {
	cases := map[string]string{
		"collides with a mapping target": strings.Replace(linesDef, `"name": "Lines"`, `"name": "Header"`, 1),
		"duplicate name":                 strings.Replace(linesDef, `"collections": [{`, `"collections": [{"name":"Lines","path":"data.x","fields":[{"source":"a","target":"A","type":"string"}]},{`, 1),
		"bad name":                       strings.Replace(linesDef, `"name": "Lines"`, `"name": "lines..x"`, 1),
		"maxItems too big":               strings.Replace(linesDef, `"maxItems": 3`, `"maxItems": 5000`, 1),
		"reserved self":                  strings.Replace(linesDef, `"name": "item", "entity"`, `"name": "self", "entity"`, 1),
		"bad field":                      strings.Replace(linesDef, `"target": "ItemNo", "type": "string"`, `"target": "ItemNo", "type": "nonsense"`, 1),
		"item targets collide":           strings.Replace(linesDef, `"target": "Price"`, `"target": "Qty"`, 1),
		"bad unknown key":                strings.Replace(linesDef, `"maxItems": 3,`, `"limit": 3,`, 1),
	}
	for name, d := range cases {
		_, err := domain.ParseOutboundDefinition([]byte(d), 1)
		var de *domain.Error
		if !errors.As(err, &de) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	if _, err := domain.ParseOutboundDefinition([]byte(linesDef), 1); err != nil {
		t.Fatal(err)
	}
}

// A collection that names a subject lists one ref per element after the header's, with the position the hub pairs
// the legacy ERP's answer to; an element without a Passage id is unresolved rather than pushed unrecordable.
func TestOutboundCollection_SubjectRefsOfEveryLine(t *testing.T) {
	withSubject := strings.Replace(strings.Replace(linesDef, `"maxItems": 3,`, `"maxItems": 3, "subject": {"entity": "salesOrderLine", "idPath": "id"},`, 1),
		`"decimalsAsNumbers": true,`, `"subject": {"entity": "salesOrder", "idPath": "data.order_id"}, "decimalsAsNumbers": true,`, 1)
	def, err := domain.ParseOutboundDefinition([]byte(withSubject), 1)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	order := "44444444-4444-4444-8444-444444444444"
	l0, l1 := "55555555-5555-4555-8555-555555555555", "66666666-6666-4666-8666-666666666666"
	line := func(id string) string {
		return `{"id":` + id + `,"product_id":"` + prodA + `","sku":"SKU-A","unit":"BX","qty":"1"}`
	}
	event := func(lines string) map[string]any {
		return lookupEvent(t, `{"order_id":"`+order+`","customer_id":"`+custID+`","number":"SO-9","currency":"USD","lines":`+lines+`}`)
	}
	got, err := def.Render(context.Background(), "custom_acme", "m-1", event(`[`+line(`"`+l0+`"`)+`,`+line(`"`+l1+`"`)+`]`), linesResolver(&calls))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Refs) != 3 || got.Refs[0] != (domain.OutboundRef{Entity: "salesOrder", ID: order}) {
		t.Fatalf("refs = %+v", got.Refs)
	}
	for i, id := range []string{l0, l1} {
		r := got.Refs[i+1]
		if r.Entity != "salesOrderLine" || r.ID != id || r.Collection != "Lines" || r.Index == nil || *r.Index != i {
			t.Fatalf("line ref %d = %+v", i, r)
		}
	}
	_, err = def.Render(context.Background(), "custom_acme", "m-1", event(`[`+line(`"`+l0+`"`)+`,`+line(`"nope"`)+`,`+line(`null`)+`]`), linesResolver(&calls))
	var u *domain.UnresolvedError
	if !errors.As(err, &u) || len(u.Refs) != 2 || u.Refs[0].Name != "Lines[1].subject" || u.Refs[1].Name != "Lines[2].subject" {
		t.Fatalf("err = %v", err)
	}
	for name, d := range map[string]string{
		"bad subject entity": strings.Replace(withSubject, `"entity": "salesOrderLine"`, `"entity": "1 bad"`, 1),
		"bad subject path":   strings.Replace(withSubject, `"idPath": "id"`, `"idPath": "a b"`, 1),
	} {
		if _, err := domain.ParseOutboundDefinition([]byte(d), 1); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}
