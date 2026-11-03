package inventory

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestBlocks(t *testing.T) {
	d := decimal.NewFromInt
	if Blocks(d(10), d(8), d(3)) != true || Blocks(d(10), d(8), d(2)) != false || Blocks(d(10), d(0), d(10)) != false {
		t.Fatal("Blocks does not refuse only the held quantity")
	}
}

func TestFromEnv(t *testing.T) {
	c, err := FromEnv(func(string) string { return "" })
	if c != nil || err != nil {
		t.Fatalf("unset URL: %v %v", c, err)
	}
	if _, err := FromEnv(func(k string) string {
		if k == "INVENTORY_URL" {
			return "http://inventory:8080"
		}
		return ""
	}); err == nil {
		t.Fatal("URL without a signing key must fail")
	}
}
