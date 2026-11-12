package migrations

import (
	"io/fs"
	"strings"
	"testing"
)

// TestMigrationNumbersAreUnique: two branches that each add the next number merge into two files with the same prefix,
// and the apply order between them is then only alphabetical. Every NNNN_ prefix must be used once.
func TestMigrationNumbersAreUnique(t *testing.T) {
	names, err := fs.Glob(FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, name := range names {
		prefix, _, ok := strings.Cut(name, "_")
		if !ok {
			t.Errorf("%s has no NNNN_ prefix", name)
			continue
		}
		if other, dup := seen[prefix]; dup {
			t.Errorf("migration number %s is used by %s and %s", prefix, other, name)
		}
		seen[prefix] = name
	}
}
