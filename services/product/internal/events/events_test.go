package events_test

import (
	"testing"

	platformevents "github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/services/product/internal/events"
)

func TestEventTypes(t *testing.T) {
	for _, typ := range []string{
		events.ProductCreated, events.ProductUpdated, events.VariantCreated, events.ProductReleased,
		events.ReleasedUpdated, events.IdentifierAdded, events.ConfigurationSolved, events.RelationChanged,
	} {
		if !platformevents.ValidType(typ) {
			t.Fatalf("invalid event type %s", typ)
		}
	}
}
