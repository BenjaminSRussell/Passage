package api

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/dq"
)

// The suite gates the load: a line the validator let through but the suite flags is quarantined with the suite's checks, and a
// later duplicate (the validator's to quarantine) never takes its first occurrence out of the accepted lines.
func TestGateSuite_QuarantinesWhatTheSuiteFlags(t *testing.T) {
	line := func(id, item, site, qty string) domain.OnhandLine {
		return domain.OnhandLine{LegacyID: id, Item: item, Site: site, Warehouse: "W1", Unit: "EA", Currency: "USD", Quantity: decimal.RequireFromString(qty), Value: decimal.NewFromInt(1)}
	}
	lines := []domain.OnhandLine{line("OH-1", "BOLT", "S1", "5"), line("OH-2", "BOLT", "S2", "0"), line("OH-1", "BOLT", "S3", "1")}
	items := map[string]struct{}{"BOLT": {}}
	// A drifted validator that accepted the zero quantity (OH-2) and quarantined the duplicate itself.
	accepted := []domain.OnhandLine{lines[0], lines[1]}
	quarantined := []domain.Quarantined{{Record: lines[2].Record(), Rule: domain.RuleUniqueness, Reason: "legacy id appears twice in the extract"}}
	kept, held, err := gateSuite(domain.PackOnhand, lines, dq.OnhandRows(lines), map[string]map[string]bool{"items": dq.SetOf(items), "iso4217": dq.ISO4217()},
		func(l domain.OnhandLine) string { return l.LegacyID }, func(l domain.OnhandLine) string { return l.LegacyID }, accepted, quarantined,
		func(l domain.OnhandLine, why string) domain.Quarantined { return quarantine(l.Record(), l, why) })
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 || kept[0].LegacyID != "OH-1" {
		t.Fatalf("kept = %+v, want OH-1 only", kept)
	}
	if len(held) != 2 || held[1].Record.LegacyID != "OH-2" || held[1].Rule != domain.RuleDomain || !strings.Contains(held[1].Reason, "data-quality suite onhand: quantity_positive") {
		t.Fatalf("quarantined = %+v", held)
	}
	if _, ok := held[1].Line.(domain.OnhandLine); !ok {
		t.Fatal("the quarantined line must keep the whole line so a release can correct it")
	}
	if _, _, err := gateSuite(domain.Pack("nope"), lines, nil, nil, func(l domain.OnhandLine) string { return l.LegacyID }, func(l domain.OnhandLine) string { return l.LegacyID }, accepted, quarantined,
		func(l domain.OnhandLine, why string) domain.Quarantined { return quarantine(l.Record(), l, why) }); err == nil {
		t.Fatal("a pack without a suite was let through")
	}
}
