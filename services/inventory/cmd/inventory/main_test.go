package main

import "testing"

func env(overrides map[string]string) func(string) string {
	base := map[string]string{"DATABASE_URL": "postgres://x", "INV_INVENTORY_ACCOUNT": "1300",
		"INV_ADJUSTMENT_ACCOUNT": "5100", "INV_OPENING_CLEARING_ACCOUNT": "3900"}
	for k, v := range overrides {
		base[k] = v
	}
	return func(k string) string { return base[k] }
}

func TestLoadConfig_defaultsForbidNegative(t *testing.T) {
	c, err := loadConfig(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.negative.PhysicalAllowed || c.negative.FinancialAllowed {
		t.Fatal("INV_NEGATIVE_DEFAULT must default to forbid")
	}
	if c.rules.MaxLines != 10000 || c.post.InventoryAccount != "1300" {
		t.Fatalf("unexpected config %+v", c)
	}
}

func TestLoadConfig_rejects(t *testing.T) {
	tests := map[string]map[string]string{
		"missing database":    {"DATABASE_URL": ""},
		"bad max lines":       {"INV_JOURNAL_MAX_LINES": "10"},
		"bad negative policy": {"INV_NEGATIVE_DEFAULT": "maybe"},
		"missing account":     {"INV_INVENTORY_ACCOUNT": ""},
	}
	for name, o := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := loadConfig(env(o)); err == nil {
				t.Fatal("expected a configuration error")
			}
		})
	}
}

func TestLoadConfig_allowNegative(t *testing.T) {
	c, err := loadConfig(env(map[string]string{"INV_NEGATIVE_DEFAULT": "allow"}))
	if err != nil {
		t.Fatal(err)
	}
	if !c.post.Negative.PhysicalAllowed || !c.post.Negative.FinancialAllowed {
		t.Fatal("allow must enable physical negative in posting params")
	}
}
