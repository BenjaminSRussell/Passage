package domain

import "time"

type AccountMapping struct {
	Source        string
	Target        string
	EffectiveFrom time.Time
}

func ApplyMappings(lines []LineInput, mappings []AccountMapping, postingDate time.Time) []LineInput {
	latest := map[string]AccountMapping{}
	for _, m := range mappings {
		if m.EffectiveFrom.After(postingDate) {
			continue
		}
		if cur, ok := latest[m.Source]; !ok || m.EffectiveFrom.After(cur.EffectiveFrom) {
			latest[m.Source] = m
		}
	}
	out := make([]LineInput, len(lines))
	for i, l := range lines {
		if m, ok := latest[l.Account]; ok {
			l.Account = m.Target
		}
		out[i] = l
	}
	return out
}
