package schema

import "testing"

func boolPtr(v bool) *bool { return &v }

func base() JSONSchema {
	return JSONSchema{
		Type:                 "object",
		AdditionalProperties: boolPtr(false),
		Required:             []string{"id", "amount"},
		Properties: map[string]JSONSchema{
			"id":     {Type: "string"},
			"amount": {Type: "number"},
			"status": {Type: "string", Enum: []any{"draft", "submitted", "approved"}},
		},
	}
}

func TestAcceptsAdditiveOptionalField(t *testing.T) {
	next := base()
	next.Properties["description"] = JSONSchema{Type: "string"}
	if err := AssertBackwardCompatible(base(), next); err != nil {
		t.Fatal(err)
	}
}

func TestRejectsBreakingChanges(t *testing.T) {
	t.Run("type change", func(t *testing.T) {
		next := base()
		next.Properties["amount"] = JSONSchema{Type: "string"}
		if err := AssertBackwardCompatible(base(), next); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("new required", func(t *testing.T) {
		next := base()
		next.Required = append(next.Required, "description")
		next.Properties["description"] = JSONSchema{Type: "string"}
		if err := AssertBackwardCompatible(base(), next); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("narrowed enum", func(t *testing.T) {
		next := base()
		next.Properties["status"] = JSONSchema{Type: "string", Enum: []any{"submitted", "approved"}}
		if err := AssertBackwardCompatible(base(), next); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("removed required field", func(t *testing.T) {
		next := base()
		delete(next.Properties, "amount")
		if err := AssertBackwardCompatible(base(), next); err == nil {
			t.Fatal("expected error")
		}
	})
}
