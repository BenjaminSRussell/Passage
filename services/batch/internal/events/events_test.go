package events

import (
	"testing"

	platform "github.com/BenjaminSRussell/passage/platform/events"
)

func TestTypesAreValid(t *testing.T) {
	for _, typ := range []string{
		InstanceSubmitted, WorkItemAssigned, WorkItemEscalated, InstanceCompleted,
		BatchRunStarted, BatchRunCompleted, BatchRunFailed,
		MessageSent, MessageFailed, MessageBounced, TemplatePublished, EntityPublished,
	} {
		if !platform.ValidType(typ) {
			t.Fatalf("invalid type %s", typ)
		}
	}
}
