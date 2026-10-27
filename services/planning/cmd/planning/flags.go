package main

import (
	"fmt"
	"sort"
	"strings"
)

// Feature flags of PRD 4.22.10, read from PLANNING_FEATURE_FLAGS as a comma-separated
// list. An unknown name stops the service at startup, so a typo cannot silently
// leave a feature off.
const (
	flagDynamic             = "planning.dynamic"
	flagStatisticalForecast = "planning.statistical-forecast"
	flagMPSScope            = "planning.mps-scope"
)

// featureFlags returns the enabled flags, sorted and without duplicates. An empty
// value enables none. Nothing reads the set yet: the dynamic plan kind, the
// statistical forecast call and the MPS scope it will gate are not built.
func featureFlags(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	seen := map[string]bool{}
	var out []string
	for _, f := range strings.Split(raw, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue // a trailing or doubled comma names nothing
		}
		if !knownFlag(f) {
			return nil, fmt.Errorf("PLANNING_FEATURE_FLAGS: unknown flag %q", f)
		}
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out, nil
}

func knownFlag(f string) bool {
	switch f {
	case flagDynamic, flagStatisticalForecast, flagMPSScope:
		return true
	}
	return false
}
