package domain_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

const outboundDef = `{
  "eventType": "dev.passage.sales.order.*.v1",
  "subject": {"entity": "salesOrder", "idPath": "data.order_id"},
  "xrefs": [{"name": "customer", "entity": "customer", "idPath": "data.customer_id"}],
  "decimalsAsNumbers": true,
  "mapping": {"entity": "SalesOrder", "fields": [
    {"source": "xref.customer", "target": "CustAccount", "type": "string", "required": true},
    {"source": "data.number", "target": "Header.CustomerRef", "type": "string", "required": true},
    {"source": "data.total", "target": "Header.Total", "type": "decimal", "scale": 2},
    {"source": "xref.self", "target": "ExistingNo", "type": "string"},
    {"rule": "const", "value": "PASSAGE", "target": "Origin", "type": "string"}
  ]}
}`

const (
	orderID = "0b7c1e52-4a7f-4c3e-9f51-2a4d6b8c9e01"
	custID  = "5f0e2d3c-1b4a-4e6f-8a7b-9c0d1e2f3a4b"
)

type fakeKeys map[string]string // entity|id -> legacy key

func (f fakeKeys) LegacyKeys(_ context.Context, _ string, entity string, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		if k, ok := f[entity+"|"+id]; ok {
			out[id] = k
		}
	}
	return out, nil
}

func orderEvent(t *testing.T) map[string]any {
	t.Helper()
	ev, err := domain.DecodeEvent(json.RawMessage(`{"specversion":"1.0","id":"evt-1","type":"dev.passage.sales.order.confirmed.v1","data":{"order_id":"0b7c1e52-4a7f-4c3e-9f51-2a4d6b8c9e01","customer_id":"5f0e2d3c-1b4a-4e6f-8a7b-9c0d1e2f3a4b","number":"SO-9","total":12.5}}`))
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

func TestOutbound_RenderNestsAndResolves(t *testing.T) {
	def, err := domain.ParseOutboundDefinition([]byte(outboundDef), 3)
	if err != nil {
		t.Fatal(err)
	}
	got, err := def.Render(context.Background(), "custom_acme", "m-1", orderEvent(t), fakeKeys{"customer|" + custID: "C-1001"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(got.Body)
	if string(raw) != `{"CustAccount":"C-1001","Header":{"CustomerRef":"SO-9","Total":12.50},"Origin":"PASSAGE"}` {
		t.Fatalf("body = %s", raw)
	}
	if len(got.Refs) != 1 || got.Refs[0] != (domain.OutboundRef{Entity: "salesOrder", ID: orderID}) {
		t.Fatalf("refs = %+v", got.Refs)
	}
	// A subject with a legacy key is an update: xref.self reaches the mapping.
	again, err := def.Render(context.Background(), "custom_acme", "m-1", orderEvent(t), fakeKeys{"customer|" + custID: "C-1001", "salesOrder|" + orderID: "SO-77"})
	if err != nil || again.Body["ExistingNo"] != "SO-77" {
		t.Fatalf("update render = %+v %v", again.Body, err)
	}
}

func TestOutbound_UnresolvedListsEveryRef(t *testing.T) {
	d := strings.Replace(outboundDef, `"xrefs": [`, `"xrefs": [{"name":"item","entity":"product","idPath":"data.item_id"},`, 1)
	def, err := domain.ParseOutboundDefinition([]byte(d), 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = def.Render(context.Background(), "custom_acme", "m-1", orderEvent(t), fakeKeys{})
	var u *domain.UnresolvedError
	if !errors.As(err, &u) || len(u.Refs) != 2 || u.Refs[0].Name != "item" || u.Refs[0].ID != "" || u.Refs[1].Name != "customer" || u.Refs[1].ID != custID {
		t.Fatalf("err = %v", err)
	}
}

func TestOutbound_IdempotencyKeyStableAcrossVersions(t *testing.T) {
	const m1, m2 = "6a1b2c3d-0000-4000-8000-000000000001", "6a1b2c3d-0000-4000-8000-000000000002"
	a := domain.OutboundIdempotencyKey(m1, "evt-1")
	if a != domain.OutboundIdempotencyKey(m1, "evt-1") || a == domain.OutboundIdempotencyKey(m2, "evt-1") || a == domain.OutboundIdempotencyKey(m1, "evt-2") {
		t.Fatalf("key %s must depend on exactly the mapping and the event", a)
	}
	if !strings.HasPrefix(a, "mig-ob-v1-") || len(a) != len("mig-ob-v1-")+64 {
		t.Fatalf("key %q is not the full SHA-256", a)
	}
	// A retry after the mapping was edited reaches the legacy ERP under the same key, so it cannot create a second record.
	v1, _ := domain.ParseOutboundDefinition([]byte(outboundDef), 1)
	v2, _ := domain.ParseOutboundDefinition([]byte(strings.Replace(outboundDef, `"target": "Origin"`, `"target": "Source"`, 1)), 2)
	r1, err1 := v1.Render(context.Background(), "custom_acme", m1, orderEvent(t), fakeKeys{"customer|" + custID: "C-1"})
	r2, err2 := v2.Render(context.Background(), "custom_acme", m1, orderEvent(t), fakeKeys{"customer|" + custID: "C-1"})
	if err1 != nil || err2 != nil || r1.IdempotencyKey != r2.IdempotencyKey || r1.Body["Origin"] != "PASSAGE" || r2.Body["Source"] != "PASSAGE" {
		t.Fatalf("v1 %+v %v, v2 %+v %v", r1, err1, r2, err2)
	}
}

func TestOutbound_SubjectMustBeAPassageID(t *testing.T) {
	def, _ := domain.ParseOutboundDefinition([]byte(outboundDef), 1)
	ev, _ := domain.DecodeEvent(json.RawMessage(`{"id":"e","type":"dev.passage.sales.order.confirmed.v1","data":{"order_id":"SO-9","customer_id":"` + custID + `","number":"n"}}`))
	_, err := def.Render(context.Background(), "custom_acme", "m", ev, fakeKeys{"customer|" + custID: "C-1"})
	var u *domain.UnresolvedError
	if !errors.As(err, &u) || len(u.Refs) != 1 || u.Refs[0].Name != "subject" || u.Refs[0].ID != "SO-9" {
		t.Fatalf("non-UUID subject = %v", err)
	}
}

// Events are untrusted input: hostile shapes are refused or rendered, never a panic.
func TestOutbound_HostileEvents(t *testing.T) {
	def, _ := domain.ParseOutboundDefinition([]byte(outboundDef), 1)
	deep := strings.Repeat(`{"a":`, 20000) + `1` + strings.Repeat(`}`, 20000)
	for name, raw := range map[string]string{
		"too large":     `{"id":"e","type":"t","data":"` + strings.Repeat("x", 1<<20) + `"}`,
		"too deep":      `{"id":"e","type":"t","data":` + deep + `}`,
		"id not string": `{"id":7,"type":"t"}`,
		"long id":       `{"id":"` + strings.Repeat("e", 257) + `","type":"t"}`,
	} {
		if ev, err := domain.DecodeEvent(json.RawMessage(raw)); err == nil {
			t.Errorf("%s: accepted %d keys", name, len(ev))
		}
	}
	for name, data := range map[string]string{
		"ids are objects": `{"order_id":{"x":1},"customer_id":[1,2]}`,
		"ids are numbers": `{"order_id":12,"customer_id":1e400}`,
		"data is a list":  `[1,2,3]`,
		"no data":         `null`,
	} {
		ev, err := domain.DecodeEvent(json.RawMessage(`{"id":"e","type":"dev.passage.sales.order.confirmed.v1","data":` + data + `}`))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := def.Render(context.Background(), "custom_acme", "m", ev, fakeKeys{}); err == nil {
			t.Errorf("%s: rendered without the customer and the order", name)
		}
	}
}

func TestOutbound_EventTypeMatching(t *testing.T) {
	for _, c := range []struct {
		pattern, typ string
		want         bool
	}{
		{"dev.passage.sales.order.*.v1", "dev.passage.sales.order.confirmed.v1", true},
		{"dev.passage.sales.order.*.v1", "dev.passage.sales.order.confirmed.v2", false},
		{"dev.passage.billing.invoice.**", "dev.passage.billing.invoice.posted.v1", true},
		{"dev.passage.billing.invoice.**", "dev.passage.billing.invoice", false},
		{"dev.passage.sales.order.confirmed.v1", "dev.passage.sales.order.confirmed.v1", true},
		{"dev.passage.sales.order.confirmed.v1", "dev.passage.sales.order.confirmed", false},
	} {
		if got := domain.MatchesEventType(c.pattern, c.typ); got != c.want {
			t.Errorf("%s ~ %s = %v", c.pattern, c.typ, got)
		}
	}
	def, _ := domain.ParseOutboundDefinition([]byte(outboundDef), 1)
	ev, _ := domain.DecodeEvent(json.RawMessage(`{"id":"e","type":"dev.passage.billing.invoice.posted.v1","data":{}}`))
	_, err := def.Render(context.Background(), "custom_acme", "m", ev, fakeKeys{})
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeOutboundMismatch {
		t.Fatalf("mismatch = %v", err)
	}
}

func TestOutbound_RejectsAndValidation(t *testing.T) {
	def, _ := domain.ParseOutboundDefinition([]byte(outboundDef), 1)
	ev, _ := domain.DecodeEvent(json.RawMessage(`{"id":"e","type":"dev.passage.sales.order.confirmed.v1","data":{"order_id":"0b7c1e52-4a7f-4c3e-9f51-2a4d6b8c9e01","customer_id":"5f0e2d3c-1b4a-4e6f-8a7b-9c0d1e2f3a4b"}}`))
	_, err := def.Render(context.Background(), "custom_acme", "m", ev, fakeKeys{"customer|" + custID: "C-1"})
	var rej *domain.RejectedError
	if !errors.As(err, &rej) || rej.Rejects[0].Code != domain.CodeMapRequired {
		t.Fatalf("missing required field = %v", err)
	}
	for name, mutate := range map[string]func(string) string{
		"bad pattern":      func(s string) string { return strings.Replace(s, "dev.passage.sales.order.*.v1", "dev.**.x", 1) },
		"reserved xref":    func(s string) string { return strings.Replace(s, `"name": "customer"`, `"name": "self"`, 1) },
		"bad subject path": func(s string) string { return strings.Replace(s, "data.order_id", "data..x", 1) },
		"unknown key": func(s string) string {
			return strings.Replace(s, `"decimalsAsNumbers"`, `"nope": 1, "decimalsAsNumbers"`, 1)
		},
		"colliding target": func(s string) string { return strings.Replace(s, `"target": "Origin"`, `"target": "Header"`, 1) },
		"empty segment": func(s string) string {
			return strings.Replace(s, `"target": "Origin"`, `"target": "Header..Origin"`, 1)
		},
	} {
		// A definition that could not build a body is refused when it is saved, not at the first push.
		if _, err := domain.ParseOutboundDefinition([]byte(mutate(outboundDef)), 1); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for _, bad := range []string{``, `[]`, `{"type":"x"}`, `{"id":"e"}`} {
		if _, err := domain.DecodeEvent(json.RawMessage(bad)); err == nil {
			t.Errorf("event %q accepted", bad)
		}
	}
}
