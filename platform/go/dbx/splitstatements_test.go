package dbx

import "testing"

func TestSplitStatements(t *testing.T) {
	got := splitStatements(NoTransactionMarker + "\n-- note;\nDROP INDEX CONCURRENTLY IF EXISTS a;\nCREATE INDEX CONCURRENTLY IF NOT EXISTS a\n  ON t (x);\n-- trailing\n")
	if len(got) != 2 || got[0] != NoTransactionMarker+"\n-- note;\nDROP INDEX CONCURRENTLY IF EXISTS a;" || got[1] != "CREATE INDEX CONCURRENTLY IF NOT EXISTS a\n  ON t (x);" {
		t.Fatalf("%q", got)
	}
}

func TestSplitStatements_DollarQuoted(t *testing.T) {
	got := splitStatements("DO $$\nBEGIN\n  EXECUTE 'x';\nEND\n$$;\nSELECT 1;\n")
	if len(got) != 2 || got[0] != "DO $$\nBEGIN\n  EXECUTE 'x';\nEND\n$$;" || got[1] != "SELECT 1;" {
		t.Fatalf("%q", got)
	}
}
