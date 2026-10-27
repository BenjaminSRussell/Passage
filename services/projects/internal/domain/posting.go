package domain

// Wildcard matches any value in a posting profile key.
const Wildcard = "*"

// ProfileKey addresses a posting profile (FR-41.5).
type ProfileKey struct {
	ProjectGroup string
	Category     string
	LineProperty string
}

// PostingProfile maps a key to the GL accounts a project posting uses.
type PostingProfile struct {
	Key                    ProfileKey
	CostAccount            string
	WIPAccount             string
	AccruedRevenueAccount  string
	InvoicedRevenueAccount string
	OnAccountAccount       string
}

// Resolve finds the profile for key with the PRD fallback order: the exact
// (group, category, property) row, then (group, *, *), then (*, *, *). When no
// row matches it returns PROJ-POST-001 naming the missing key.
func Resolve(profiles []PostingProfile, key ProfileKey) (PostingProfile, error) {
	tiers := []ProfileKey{
		key,
		{ProjectGroup: key.ProjectGroup, Category: Wildcard, LineProperty: Wildcard},
		{ProjectGroup: Wildcard, Category: Wildcard, LineProperty: Wildcard},
	}
	for _, want := range tiers {
		for _, p := range profiles {
			if p.Key == want {
				return p, nil
			}
		}
	}
	return PostingProfile{}, rule(CodePostingProfile,
		"No posting profile for project group %s, category %s, line property %s",
		key.ProjectGroup, key.Category, key.LineProperty)
}
