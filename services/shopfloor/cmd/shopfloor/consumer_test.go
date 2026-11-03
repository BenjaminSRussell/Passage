package main

import (
	"context"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/api"
)

// The consumer runs only as its own principal with the sync duty, never as another consumer's.
func TestProductionSeeded_NeedsOwnPrincipalWithSyncDuty(t *testing.T) {
	for name, table := range map[string]map[string][]string{
		"absent":         {"scheduling-downtime": {"scheduling.downtime.apply"}},
		"without duty":   {api.ProductionConsumerName: {"shopfloor.view"}},
		"duty elsewhere": {"scheduling-downtime": {api.DutyProductionSync}},
	} {
		if productionSeeded(table) {
			t.Errorf("%s: seeded", name)
		}
	}
	if !productionSeeded(map[string][]string{api.ProductionConsumerName: {api.DutyProductionSync}}) {
		t.Error("seeded principal not recognised")
	}
}

// An unknown command fails before anything connects; "consume" is the only one.
func TestRun_RefusesUnknownCommand(t *testing.T) {
	err := run(context.Background(), func(string) string { return "" }, []string{"shopfloor", "migrate"})
	if err == nil || !strings.Contains(err.Error(), `unknown command "migrate"`) {
		t.Fatalf("err = %v", err)
	}
	if err := run(context.Background(), func(string) string { return "" }, []string{"shopfloor", "consume"}); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("consume without a database = %v", err)
	}
}
