package events

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/events"
)

func TestStatementImported_MatchesNamingRule(t *testing.T) {
	want := events.Type(Context, "statement", "imported", 1)
	if StatementImported != want {
		t.Fatalf("StatementImported = %q, want %q", StatementImported, want)
	}
	if !events.ValidType(StatementImported) {
		t.Fatalf("%q is not a valid event type", StatementImported)
	}
}
