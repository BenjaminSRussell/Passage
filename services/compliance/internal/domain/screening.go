package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// Screening codes (FR-48.19, BR-48.15, TRD-1xxx).
const (
	CodeScreenInput    = "TRD-1004" // subject, threshold or list entry invalid
	CodeDecisionReason = "TRD-1005" // decision without a reason or decider
	CodeHitState       = "TRD-1006" // decision on a hit that is not OPEN
)

// Screening defaults and bounds (9.48.13 screening.threshold).
const (
	DefaultScreeningThreshold = 0.88
	MinScreeningThreshold     = 0.70
	MaxScreeningThreshold     = 0.99
)

// Field weights of the composite score. Name carries most of the signal; the
// address adds to it when both sides have one. A country mismatch scales the
// score down because a same-named party in another country is usually a
// different party.
const (
	weightName    = 0.8
	weightAddress = 0.2
	countryFactor = 0.9
)

// HitState is the state of a screening hit (9.48.4 screening_hit).
type HitState string

const (
	HitOpen          HitState = "OPEN"
	HitFalsePositive HitState = "FALSE_POSITIVE"
	HitConfirmed     HitState = "CONFIRMED"
)

// Decision is the officer's action on an open hit.
type Decision string

const (
	DecisionClearFalsePositive Decision = "CLEAR_FALSE_POSITIVE"
	DecisionConfirm            Decision = "CONFIRM"
)

// Subject types (9.48.4 screening_hit.subject_type).
const (
	SubjectParty    = "PARTY"
	SubjectDocument = "DOCUMENT"
)

// ValidSubjectType reports whether t is PARTY or DOCUMENT.
func ValidSubjectType(t string) bool { return t == SubjectParty || t == SubjectDocument }

// ValidHitState reports whether s is a screening hit state.
func ValidHitState(s HitState) bool {
	return s == HitOpen || s == HitFalsePositive || s == HitConfirmed
}

// Subject is a party or document presented for screening.
type Subject struct {
	Type    string `json:"type"` // PARTY or DOCUMENT
	ID      string `json:"id"`
	Name    string `json:"name"`
	Address string `json:"address,omitempty"`
	Country string `json:"country,omitempty"` // ISO 3166 alpha-2
}

// ListEntry is one entry of a screening list (OFAC SDN, EU CFSP, UK OFSI, UN, BIS).
type ListEntry struct {
	ListCode string   `json:"listCode"`
	EntryID  string   `json:"entryId"`
	Version  int      `json:"version"`
	Name     string   `json:"name"`
	Aliases  []string `json:"aliases,omitempty"`
	Address  string   `json:"address,omitempty"`
	Country  string   `json:"country,omitempty"`
}

// Clearance is a false-positive whitelist entry per party and list entry (BR-48.15).
// It holds the list entry version and the subject fingerprint it was granted on.
type Clearance struct {
	SubjectID          string `json:"subjectId"`
	ListCode           string `json:"listCode"`
	EntryID            string `json:"entryId"`
	EntryVersion       int    `json:"entryVersion"`
	SubjectFingerprint string `json:"subjectFingerprint"`
	Reason             string `json:"reason"`
}

// Hit is a screening match at or above the threshold.
type Hit struct {
	ListCode      string   `json:"listCode"`
	EntryID       string   `json:"entryId"`
	EntryVersion  int      `json:"entryVersion"`
	Score         float64  `json:"score"`
	MatchedFields []string `json:"matchedFields"`
	State         HitState `json:"state"`
}

// foldRunes maps the common accented Latin letters, the Latin letters NFKD would
// not decompose, and Cyrillic to ASCII. It is the transliteration step of the
// matcher; other scripts are kept as they are and match only exactly.
var foldRunes = map[rune]string{
	'à': "a", 'á': "a", 'â': "a", 'ã': "a", 'ä': "a", 'å': "a", 'æ': "ae", 'ç': "c",
	'è': "e", 'é': "e", 'ê': "e", 'ë': "e", 'ì': "i", 'í': "i", 'î': "i", 'ï': "i",
	'ñ': "n", 'ò': "o", 'ó': "o", 'ô': "o", 'õ': "o", 'ö': "o", 'ø': "o", 'œ': "oe",
	'ß': "ss", 'ù': "u", 'ú': "u", 'û': "u", 'ü': "u", 'ý': "y", 'ÿ': "y", 'ł': "l",
	'đ': "d", 'ð': "d", 'þ': "th", 'ş': "s", 'ğ': "g", 'ı': "i", 'ř': "r", 'š': "s",
	'ž': "z", 'č': "c", 'ć': "c", 'ń': "n", 'ś': "s", 'ź': "z", 'ż': "z", 'ě': "e",
	'ů': "u", 'ő': "o", 'ű': "u", 'ē': "e", 'ī': "i", 'ō': "o", 'ū': "u", 'ā': "a",
	// Cyrillic (Russian, Ukrainian, Belarusian), romanized the way the OFAC and
	// UK OFSI lists spell names, so that "Иван Петров" meets "Ivan Petrov".
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'ґ': "g", 'д': "d", 'е': "e", 'ё': "e", 'є': "ye",
	'ж': "zh", 'з': "z", 'и': "i", 'і': "i", 'ї': "yi", 'й': "y", 'к': "k", 'л': "l", 'м': "m",
	'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u", 'ў': "u", 'ф': "f",
	'х': "kh", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "shch", 'ъ': "", 'ы': "y", 'ь': "", 'э': "e",
	'ю': "yu", 'я': "ya",
}

// Normalize lower-cases, transliterates and reduces a name or address to single-space
// separated ASCII tokens. Punctuation becomes a separator.
func Normalize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if rep, ok := foldRunes[r]; ok {
			b.WriteString(rep)
			continue
		}
		// Non-Latin letters are kept so that non-Latin list names still match exactly.
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// levenshtein returns the edit distance between two strings in runes.
func levenshtein(a, b []rune) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// ratio is 1 minus the edit distance over the longer length; two empty strings match.
func ratio(a, b string) float64 {
	ra, rb := []rune(a), []rune(b)
	longest := max(len(ra), len(rb))
	if longest == 0 {
		return 1
	}
	return 1 - float64(levenshtein(ra, rb))/float64(longest)
}

// TokenSetRatio compares two normalized strings on their token sets, so that
// word order and extra words (legal suffixes, middle names) matter less. It
// follows the usual token-set construction: the shared tokens are compared with
// each side's remainder and the best of the three ratios is kept.
//
// An empty side scores 0: without the guard the shared part and an empty
// remainder are both "", which would rate as a perfect match.
func TokenSetRatio(a, b string) float64 {
	ta, tb := tokenSet(a), tokenSet(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	var common, onlyA, onlyB []string
	for t := range ta {
		if tb[t] {
			common = append(common, t)
		} else {
			onlyA = append(onlyA, t)
		}
	}
	for t := range tb {
		if !ta[t] {
			onlyB = append(onlyB, t)
		}
	}
	sort.Strings(common)
	sort.Strings(onlyA)
	sort.Strings(onlyB)
	s1 := strings.Join(common, " ")
	s2 := strings.TrimSpace(s1 + " " + strings.Join(onlyA, " "))
	s3 := strings.TrimSpace(s1 + " " + strings.Join(onlyB, " "))
	return max(ratio(s1, s2), ratio(s1, s3), ratio(s2, s3))
}

func tokenSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, t := range strings.Fields(s) {
		out[t] = true
	}
	return out
}

// Score returns the composite match score of a subject against a list entry and
// the fields that contributed. The best name (primary or alias) wins; the address
// only counts when the name itself is a hit candidate on both sides.
func Score(s Subject, e ListEntry) (float64, []string) {
	subjName := Normalize(s.Name)
	best := 0.0
	for _, cand := range append([]string{e.Name}, e.Aliases...) {
		if r := TokenSetRatio(subjName, Normalize(cand)); r > best { // an empty alias scores 0
			best = r
		}
	}
	fields := []string{"name"}
	score := best
	if s.Address != "" && e.Address != "" {
		addr := TokenSetRatio(Normalize(s.Address), Normalize(e.Address))
		score = best*weightName + addr*weightAddress
		fields = append(fields, "address")
	}
	if s.Country != "" && e.Country != "" {
		if strings.EqualFold(s.Country, e.Country) {
			fields = append(fields, "country")
		} else {
			score *= countryFactor
		}
	}
	return round3(score), fields
}

// Fingerprint identifies the subject content a clearance was granted on. It covers
// the normalized name, address and country, not the subject id (BR-48.15).
func Fingerprint(s Subject) string {
	sum := sha256.Sum256([]byte(Normalize(s.Name) + "|" + Normalize(s.Address) + "|" + strings.ToUpper(s.Country)))
	return hex.EncodeToString(sum[:])
}

// IsCleared reports whether a clearance still applies: same subject, same list entry
// and version, and unchanged subject content (BR-48.15).
func IsCleared(c Clearance, s Subject, e ListEntry) bool {
	return c.SubjectID == s.ID && c.ListCode == e.ListCode && c.EntryID == e.EntryID &&
		c.EntryVersion == e.Version && c.SubjectFingerprint == Fingerprint(s)
}

// ScreenInput is the input of Screen.
type ScreenInput struct {
	Subject    Subject
	Entries    []ListEntry
	Threshold  float64
	Clearances []Clearance
}

// Screen returns the hits at or above the threshold, highest score first. Entries
// cleared for the same subject content are not reported again (BR-48.15).
func Screen(in ScreenInput) ([]Hit, error) {
	th := in.Threshold
	if th == 0 {
		th = DefaultScreeningThreshold
	}
	switch {
	case !ValidSubjectType(in.Subject.Type) || strings.TrimSpace(in.Subject.ID) == "" || Normalize(in.Subject.Name) == "":
		return nil, &Error{Code: CodeScreenInput, Detail: "subject needs a type (PARTY or DOCUMENT), an id and a name with letters or digits"}
	case th < MinScreeningThreshold || th > MaxScreeningThreshold:
		return nil, &Error{Code: CodeScreenInput, Detail: fmt.Sprintf("threshold %.2f must be between 0.70 and 0.99", th)}
	}
	hits := []Hit{}
	for _, e := range in.Entries {
		if e.ListCode == "" || e.EntryID == "" || e.Version < 1 || Normalize(e.Name) == "" {
			return nil, &Error{Code: CodeScreenInput, Detail: "every list entry needs a list code, an entry id, a version from 1 and a name"}
		}
		if cleared(in.Clearances, in.Subject, e) {
			continue
		}
		score, fields := Score(in.Subject, e)
		if score >= th {
			hits = append(hits, Hit{ListCode: e.ListCode, EntryID: e.EntryID, EntryVersion: e.Version,
				Score: score, MatchedFields: fields, State: HitOpen})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		if hits[i].ListCode != hits[j].ListCode {
			return hits[i].ListCode < hits[j].ListCode
		}
		return hits[i].EntryID < hits[j].EntryID
	})
	return hits, nil
}

func cleared(cs []Clearance, s Subject, e ListEntry) bool {
	for _, c := range cs {
		if IsCleared(c, s, e) {
			return true
		}
	}
	return false
}

// DecideHit applies an officer decision to an OPEN hit. A false positive needs a
// reason and a decider; the decider must not be empty, and the caller enforces that
// the decider holds TradeComplianceOfficer.
func DecideHit(state HitState, d Decision, reason, decidedBy string) (HitState, error) {
	if state != HitOpen {
		return state, &Error{Code: CodeHitState, Detail: fmt.Sprintf("hit is %s; only OPEN hits can be decided", state)}
	}
	if strings.TrimSpace(reason) == "" || strings.TrimSpace(decidedBy) == "" {
		return state, &Error{Code: CodeDecisionReason, Detail: "a decision needs a reason and the deciding user"}
	}
	switch d {
	case DecisionClearFalsePositive:
		return HitFalsePositive, nil
	case DecisionConfirm:
		return HitConfirmed, nil
	}
	return state, &Error{Code: CodeScreenInput, Detail: fmt.Sprintf("unknown decision %q", d)}
}

// ReleaseBlocked reports whether a document with these hits may not move to
// released, shipped or paid. Only hits cleared as false positive let it through.
func ReleaseBlocked(states []HitState) bool {
	for _, s := range states {
		if s != HitFalsePositive {
			return true
		}
	}
	return false
}

func round3(v float64) float64 {
	return float64(int(v*1000+0.5)) / 1000
}
