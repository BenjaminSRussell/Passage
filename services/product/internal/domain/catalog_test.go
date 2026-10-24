package domain_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDemoCatalogCounts(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "demo-catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cat struct {
		Masters []any `json:"masters"`
		Items   []any `json:"items"`
	}
	if err := json.Unmarshal(raw, &cat); err != nil {
		t.Fatal(err)
	}
	if len(cat.Masters) != 50 || len(cat.Items) != 500 {
		t.Fatalf("catalog masters=%d items=%d, want 50/500", len(cat.Masters), len(cat.Items))
	}
}
