package api

import (
	"fmt"
	"strings"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/dq"
)

// gateSuite puts the pack's data-quality suite on the load path (FR-49.11, D1c): the suite runs over the same extract as the
// Go validator, and a line the suite flags but the validator accepted is quarantined too (rule DOMAIN, the reason naming the
// suite's checks), so the suite gates the load rather than only documenting it. The validator stays the first judge: a line it
// quarantined keeps its own rule and reason. A later duplicate is the validator's to quarantine, so only the first line of a key
// is taken out of the accepted lines.
func gateSuite[L, A any](pack domain.Pack, lines []L, rows []dq.Row, sets map[string]map[string]bool, keyIn func(L) string, keyAcc func(A) string,
	accepted []A, quarantined []domain.Quarantined, hold func(A, string) domain.Quarantined) ([]A, []domain.Quarantined, error) {
	flagged, err := dq.Flagged(string(pack), rows, sets)
	if err != nil {
		return nil, nil, fmt.Errorf("data-quality suite %s: %w", pack, err)
	}
	if len(flagged) == 0 {
		return accepted, quarantined, nil
	}
	first := map[string]int{}
	for i, l := range lines {
		if _, ok := first[keyIn(l)]; !ok {
			first[keyIn(l)] = i
		}
	}
	drop := map[string][]string{}
	for i, checks := range flagged {
		k := keyIn(lines[i])
		if first[k] == i {
			drop[k] = checks
		}
	}
	kept := accepted[:0:0]
	for _, a := range accepted {
		checks, bad := drop[keyAcc(a)]
		if !bad {
			kept = append(kept, a)
			continue
		}
		quarantined = append(quarantined, hold(a, fmt.Sprintf("data-quality suite %s: %s", pack, strings.Join(checks, ", "))))
	}
	return kept, quarantined, nil
}

func quarantine(rec domain.Record, line any, reason string) domain.Quarantined {
	return domain.Quarantined{Record: rec, Rule: domain.RuleDomain, Reason: reason, Line: line}
}

func stagedKey(l domain.StagedLine) string { return l.LegacyID }

func holdStaged(l domain.StagedLine, why string) domain.Quarantined {
	return quarantine(l.Record(), l, why)
}
