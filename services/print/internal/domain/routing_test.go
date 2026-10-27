package domain_test

import (
	"testing"

	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
)

func TestResolveRoute_PrecedenceAndSequence(t *testing.T) {
	rules := []domain.RoutingRule{
		{ID: "default", DocumentType: "PICK_LIST", QueueCode: "OFFICE", Sequence: 1},
		{ID: "wh", DocumentType: "PICK_LIST", WarehouseID: "WH1", QueueCode: "WH", Sequence: 1},
		{ID: "zone", DocumentType: "PICK_LIST", Zone: "COLD", QueueCode: "COLD", Sequence: 5},
		{ID: "zone-2", DocumentType: "PICK_LIST", Zone: "COLD", QueueCode: "COLD2", Sequence: 2},
		{ID: "user", DocumentType: "PICK_LIST", UserID: "worker-7", QueueCode: "PERSONAL", Sequence: 9},
		{ID: "other-doc", DocumentType: "BOL", QueueCode: "BOL", Sequence: 1},
	}
	cases := []struct {
		name string
		doc  domain.Document
		want string
	}{
		{"user beats everything", domain.Document{Type: "PICK_LIST", UserID: "worker-7", Zone: "COLD", WarehouseID: "WH1"}, "PERSONAL"},
		{"zone beats warehouse", domain.Document{Type: "PICK_LIST", UserID: "other", Zone: "COLD", WarehouseID: "WH1"}, "COLD2"},
		{"lowest sequence wins within a tier", domain.Document{Type: "PICK_LIST", Zone: "COLD"}, "COLD2"},
		{"warehouse beats default", domain.Document{Type: "PICK_LIST", WarehouseID: "WH1"}, "WH"},
		{"document-type default", domain.Document{Type: "PICK_LIST", WarehouseID: "WH9"}, "OFFICE"},
		{"other document type is not matched", domain.Document{Type: "BOL", UserID: "worker-7"}, "BOL"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := domain.ResolveRoute(rules, c.doc)
			if !ok || got.QueueCode != c.want {
				t.Fatalf("route = %+v ok=%v, want queue %s", got, ok, c.want)
			}
		})
	}
}

func TestResolveRoute_NoMatch(t *testing.T) {
	rules := []domain.RoutingRule{{ID: "zone", DocumentType: "COA", Zone: "LAB", QueueCode: "LAB", Sequence: 1}}
	if _, ok := domain.ResolveRoute(rules, domain.Document{Type: "COA", Zone: "DOCK"}); ok {
		t.Fatal("a zone rule matched another zone")
	}
	if _, ok := domain.ResolveRoute(nil, domain.Document{Type: "COA"}); ok {
		t.Fatal("empty rule set resolved a route")
	}
}

func TestRoutingRule_Validate(t *testing.T) {
	cases := []struct {
		name   string
		rule   domain.RoutingRule
		fields []string
	}{
		{"valid default", domain.RoutingRule{DocumentType: "COA", QueueCode: "LAB", Sequence: 1}, nil},
		{"unknown document type", domain.RoutingRule{DocumentType: "INVOICE", QueueCode: "LAB", Sequence: 1}, []string{"documentType"}},
		{"bad queue code", domain.RoutingRule{DocumentType: "COA", QueueCode: "lab", Sequence: 1}, []string{"queueCode"}},
		{"sequence below one", domain.RoutingRule{DocumentType: "COA", QueueCode: "LAB"}, []string{"sequence"}},
		{"two scopes", domain.RoutingRule{DocumentType: "COA", QueueCode: "LAB", Sequence: 1, UserID: "u", Zone: "z"}, []string{"scope"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.rule.Validate()
			if len(got) != len(c.fields) {
				t.Fatalf("fields = %v, want %v", got, c.fields)
			}
			for _, f := range c.fields {
				if _, ok := got[f]; !ok {
					t.Fatalf("missing field error %s in %v", f, got)
				}
			}
		})
	}
}
