package domain

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

const (
	LikelyDuplicateWeight   = 12.0
	PossibleDuplicateWeight = 6.0
	ClassLikely             = "likely_duplicate"
	ClassPossible           = "possible"
	namePrefixLen           = 4
	strongJW                = 0.92
	weakJW                  = 0.85
)

type PartyRecord struct {
	ID      string
	Type    string
	Name    string
	TaxID   string
	Street  string
	Postal  string
	Country string
}

type Match struct {
	A, B   string
	Weight float64
	Class  string
}

type level struct{ m, u float64 }

func (l level) weight() float64 { return math.Log2(l.m / l.u) }

var (
	nameLevels    = [4]level{{0.80, 0.0002}, {0.15, 0.0008}, {0.04, 0.004}, {0.01, 0.995}}
	streetLevels  = [4]level{{0.60, 0.0005}, {0.25, 0.002}, {0.05, 0.01}, {0.10, 0.9877}}
	taxLevels     = [2]level{{0.90, 0.00001}, {0.10, 0.99999}}
	postalLevels  = [2]level{{0.90, 0.001}, {0.10, 0.999}}
	countryLevels = [2]level{{0.99, 0.30}, {0.01, 0.70}}
)

func Normalize(s string) string {
	words := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	return strings.Join(words, " ")
}

func normalizeStreet(s string) string {
	words := strings.Fields(Normalize(s))
	sort.Strings(words)
	return strings.Join(words, " ")
}

func JaroWinkler(a, b string) float64 {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 || len(rb) == 0 {
		return 0
	}
	if string(ra) == string(rb) {
		return 1
	}
	window := max(len(ra), len(rb))/2 - 1
	if window < 0 {
		window = 0
	}
	matchedA, matchedB := make([]bool, len(ra)), make([]bool, len(rb))
	matches := 0
	for i, c := range ra {
		for j := max(0, i-window); j <= min(len(rb)-1, i+window); j++ {
			if !matchedB[j] && rb[j] == c {
				matchedA[i], matchedB[j] = true, true
				matches++
				break
			}
		}
	}
	if matches == 0 {
		return 0
	}
	transpositions, j := 0, 0
	for i := range ra {
		if !matchedA[i] {
			continue
		}
		for !matchedB[j] {
			j++
		}
		if ra[i] != rb[j] {
			transpositions++
		}
		j++
	}
	m := float64(matches)
	jaro := (m/float64(len(ra)) + m/float64(len(rb)) + (m-float64(transpositions)/2)/m) / 3
	prefix := 0
	for prefix < min(4, min(len(ra), len(rb))) && ra[prefix] == rb[prefix] {
		prefix++
	}
	return jaro + float64(prefix)*0.1*(1-jaro)
}

func similarityWeight(levels [4]level, a, b string) float64 {
	switch similarity := JaroWinkler(a, b); {
	case a == b:
		return levels[0].weight()
	case similarity >= strongJW:
		return levels[1].weight()
	case similarity >= weakJW:
		return levels[2].weight()
	default:
		return levels[3].weight()
	}
}

func exactWeight(levels [2]level, a, b string) float64 {
	if a == b {
		return levels[0].weight()
	}
	return levels[1].weight()
}

func Score(a, b PartyRecord) Match {
	match := Match{A: a.ID, B: b.ID}
	if a.Type != b.Type {
		return match
	}
	if na, nb := Normalize(a.Name), Normalize(b.Name); na != "" && nb != "" {
		match.Weight += similarityWeight(nameLevels, na, nb)
	}
	if ta, tb := NormalizeTaxNumber(a.TaxID), NormalizeTaxNumber(b.TaxID); ta != "" && tb != "" {
		match.Weight += exactWeight(taxLevels, ta, tb)
	}
	if sa, sb := normalizeStreet(a.Street), normalizeStreet(b.Street); sa != "" && sb != "" {
		match.Weight += similarityWeight(streetLevels, sa, sb)
	}
	if pa, pb := Normalize(a.Postal), Normalize(b.Postal); pa != "" && pb != "" {
		match.Weight += exactWeight(postalLevels, pa, pb)
	}
	if ca, cb := strings.ToUpper(strings.TrimSpace(a.Country)), strings.ToUpper(strings.TrimSpace(b.Country)); ca != "" && cb != "" {
		match.Weight += exactWeight(countryLevels, ca, cb)
	}
	switch {
	case match.Weight >= LikelyDuplicateWeight:
		match.Class = ClassLikely
	case match.Weight >= PossibleDuplicateWeight:
		match.Class = ClassPossible
	}
	return match
}

func BlockKeys(p PartyRecord) []string {
	var keys []string
	if name := strings.ReplaceAll(Normalize(p.Name), " ", ""); name != "" {
		runes := []rune(name)
		keys = append(keys, "n:"+string(runes[:min(namePrefixLen, len(runes))]))
	}
	if tax := NormalizeTaxNumber(p.TaxID); tax != "" {
		keys = append(keys, "t:"+tax)
	}
	if street, postal := normalizeStreet(p.Street), Normalize(p.Postal); street != "" && postal != "" {
		keys = append(keys, "a:"+postal+"|"+street)
	}
	return keys
}

func FindCandidates(pool []PartyRecord, p PartyRecord) []Match {
	own := map[string]bool{}
	for _, key := range BlockKeys(p) {
		own[key] = true
	}
	var matches []Match
	for _, other := range pool {
		if other.ID == p.ID || !sharesBlock(own, other) {
			continue
		}
		if m := Score(p, other); m.Class != "" {
			matches = append(matches, m)
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Weight > matches[j].Weight })
	return matches
}

func sharesBlock(own map[string]bool, other PartyRecord) bool {
	for _, key := range BlockKeys(other) {
		if own[key] {
			return true
		}
	}
	return false
}

func FindDuplicatePairs(records []PartyRecord) []Match {
	blocks := map[string][]int{}
	for i, r := range records {
		for _, key := range BlockKeys(r) {
			blocks[key] = append(blocks[key], i)
		}
	}
	seen := map[[2]int]bool{}
	var matches []Match
	for _, members := range blocks {
		for x, i := range members {
			for _, j := range members[x+1:] {
				pair := [2]int{i, j}
				if seen[pair] {
					continue
				}
				seen[pair] = true
				if m := Score(records[i], records[j]); m.Class != "" {
					matches = append(matches, m)
				}
			}
		}
	}
	return matches
}
