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

func TestAll_EveryTypeFollowsTheNamingRule(t *testing.T) {
	want := map[string]string{
		StatementImported: events.Type(Context, "statement", "imported", 1),
		CheckVoided:       events.Type(Context, "check", "voided", 1),

		ReconciliationMatched:   events.Type(Context, "reconciliation", "matched", 1),
		ReconciliationUnmatched: events.Type(Context, "reconciliation", "unmatched", 1),
	}
	if len(All()) != len(want) {
		t.Fatalf("All() = %v, want %d types", All(), len(want))
	}
	for _, typ := range All() {
		if want[typ] != typ || !events.ValidType(typ) {
			t.Fatalf("%q does not follow dev.passage.<ctx>.<entity>.<verb>.v1", typ)
		}
	}
}
