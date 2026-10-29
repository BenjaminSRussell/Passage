package domain

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Audit rule types beyond DUPLICATE (FR-48.14). Conditions and thresholds are FR-3.13
// decision-table rows in production; these functions are the evaluation kernel they call.
// DUPLICATE (audit_duplicate.go) runs through EvaluateAudit too.
const (
	AuditRuleKeyword        = "KEYWORD"
	AuditRuleListSearch     = "LIST_SEARCH"
	AuditRuleSampling       = "SAMPLING"
	AuditRuleThreshold      = "THRESHOLD"
	AuditRuleWeekendHoliday = "WEEKEND_HOLIDAY"
	AuditRuleAggregate      = "AGGREGATE"
)

// Audit rule codes.
const (
	CodeAuditRuleType   = "AUD-0711" // unknown rule type
	CodeAuditRuleConfig = "AUD-0712" // rule configuration incomplete or out of range
)

// Score of a finding. Every rule flags or does not flag a document, so a finding scores
// 100; THRESHOLD records the share of the limit used in its evidence.
const binaryScore = 100

// AuditDocument is the subset of an expense, vendor invoice, purchase order or journal
// the audit rules read. Texts holds the free-text fields searched by KEYWORD. Number and
// PostedCredit are read by DUPLICATE only, where PartyID is the vendor and Date the
// invoice date.
type AuditDocument struct {
	ID           string          `json:"id"`
	PartyID      string          `json:"partyId"`
	Category     string          `json:"category"`
	Amount       decimal.Decimal `json:"amount"`
	Currency     string          `json:"currency"`
	Date         time.Time       `json:"date"`
	Texts        []string        `json:"texts"`
	Number       string          `json:"number,omitempty"`
	PostedCredit bool            `json:"postedCredit,omitempty"`
}

// AuditSpec is the configuration of one rule. Only the fields of its Type are read.
type AuditSpec struct {
	Type      string                     `json:"type"`
	Keywords  []string                   `json:"keywords,omitempty"`
	WatchList []string                   `json:"watchList,omitempty"`
	Limits    map[string]decimal.Decimal `json:"limits,omitempty"`
	Currency  string                     `json:"currency,omitempty"`
	Holidays  []string                   `json:"holidays,omitempty"`
	Sample    *SampleSpec                `json:"sample,omitempty"`
	// Limit is the AGGREGATE limit per party per calendar month, in Currency.
	Limit decimal.Decimal `json:"limit,omitempty"`
}

// SampleSpec selects documents by a seeded draw: either Percent of documents (0-100) or
// every EveryK-th document of the id-sorted population. The same seed always selects the
// same documents (AC-48.13 reruns, audit.sampling_seed_policy PER_RUN_STORED).
type SampleSpec struct {
	Seed    string `json:"seed"`
	Percent int    `json:"percent,omitempty"`
	EveryK  int    `json:"everyK,omitempty"`
}

// Finding is one violation produced by EvaluateAudit before it is stored.
type Finding struct {
	Key        string            `json:"key"`
	RuleType   string            `json:"ruleType"`
	DocumentID string            `json:"documentId"`
	Score      int               `json:"score"`
	Evidence   map[string]string `json:"evidence"`
}

// normalizeWords folds case and punctuation so "Gift-Card" and "gift card" compare equal.
var nonWord = regexp.MustCompile(`[^a-z0-9]+`)

func normalizeWords(s string) string {
	return strings.TrimSpace(nonWord.ReplaceAllString(strings.ToLower(s), " "))
}

// KeywordHits returns the keywords found as whole words in any of texts, sorted and
// de-duplicated. Matching ignores case and punctuation.
func KeywordHits(texts []string, keywords []string) []string {
	var hay []string
	for _, t := range texts {
		hay = append(hay, " "+normalizeWords(t)+" ")
	}
	seen := map[string]bool{}
	var out []string
	for _, kw := range keywords {
		n := normalizeWords(kw)
		if n == "" || seen[n] {
			continue
		}
		for _, h := range hay {
			if strings.Contains(h, " "+n+" ") {
				seen[n] = true
				out = append(out, n)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// OnWatchList reports whether party matches an entry of the watch list (LIST_SEARCH),
// for example an employee name appearing as a vendor.
func OnWatchList(party string, watch []string) bool {
	n := normalizeWords(party)
	if n == "" {
		return false
	}
	for _, w := range watch {
		if normalizeWords(w) == n {
			return true
		}
	}
	return false
}

// ThresholdExceeded reports whether the amount is above the category limit. Amounts in a
// different currency than the limit are not compared; the caller reports them separately.
func ThresholdExceeded(amount decimal.Decimal, currency string, limit decimal.Decimal, limitCurrency string) (bool, decimal.Decimal) {
	if currency != limitCurrency || limit.IsZero() {
		return false, decimal.Zero
	}
	ratio := amount.Div(limit).Mul(decimal.NewFromInt(binaryScore))
	return amount.GreaterThan(limit), ratio.Round(0)
}

// OutsideBusinessCalendar reports whether date (its UTC calendar day) falls on a weekend
// or on a listed holiday, given as YYYY-MM-DD (WEEKEND_HOLIDAY).
func OutsideBusinessCalendar(date time.Time, holidays map[string]bool) bool {
	d := date.UTC()
	if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
		return true
	}
	return holidays[d.Format("2006-01-02")]
}

// SelectSample returns the ids chosen by the seeded sample. Percent mode keeps a document
// when its seeded hash falls under the percentage. EveryK mode keeps every k-th document
// of the id-sorted population, starting at an offset derived from the seed. Both are pure
// functions of (seed, population), so reruns select the same documents.
func SelectSample(ids []string, s SampleSpec) (map[string]bool, error) {
	if s.Seed == "" {
		return nil, &Error{Code: CodeAuditRuleConfig, Detail: "sampling needs a stored seed"}
	}
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	out := map[string]bool{}
	switch {
	case s.EveryK > 0:
		offset := int(seedHash(s.Seed, "offset") % uint64(s.EveryK))
		for i, id := range sorted {
			if i%s.EveryK == offset {
				out[id] = true
			}
		}
	case s.Percent > 0 && s.Percent <= 100:
		for _, id := range sorted {
			if int(seedHash(s.Seed, id)%100) < s.Percent {
				out[id] = true
			}
		}
	default:
		return nil, &Error{Code: CodeAuditRuleConfig, Detail: "sampling needs percent 1..100 or everyK > 0"}
	}
	return out, nil
}

func seedHash(seed, id string) uint64 {
	sum := sha256.Sum256([]byte(seed + "\x00" + id))
	return binary.BigEndian.Uint64(sum[:8])
}

// AggregateBreach is one party-month whose total exceeds the AGGREGATE limit.
type AggregateBreach struct {
	PartyID  string          `json:"partyId"`
	Period   string          `json:"period"`
	Total    decimal.Decimal `json:"total"`
	Currency string          `json:"currency"`
	DocIDs   []string        `json:"docIds"`
}

// AggregateByPartyMonth sums documents per party and calendar month (UTC) in currency and
// returns the groups above limit, sorted by party then period.
func AggregateByPartyMonth(docs []AuditDocument, currency string, limit decimal.Decimal) []AggregateBreach {
	type key struct{ party, period string }
	sums := map[key]*AggregateBreach{}
	for _, d := range docs {
		if d.Currency != currency {
			continue
		}
		k := key{d.PartyID, d.Date.UTC().Format("2006-01")}
		b, ok := sums[k]
		if !ok {
			b = &AggregateBreach{PartyID: k.party, Period: k.period, Total: decimal.Zero, Currency: currency}
			sums[k] = b
		}
		b.Total = b.Total.Add(d.Amount)
		b.DocIDs = append(b.DocIDs, d.ID)
	}
	var out []AggregateBreach
	for _, b := range sums {
		if b.Total.GreaterThan(limit) {
			sort.Strings(b.DocIDs)
			out = append(out, *b)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PartyID != out[j].PartyID {
			return out[i].PartyID < out[j].PartyID
		}
		return out[i].Period < out[j].Period
	})
	return out
}

// EvaluateAudit runs one rule over docs and returns its findings for policy (id, version).
// Findings are sorted by document id; the Key of each is idempotent on (policy version,
// document id), so a rerun creates no new violations (FR-48.14).
func EvaluateAudit(policyID string, version int, spec AuditSpec, docs []AuditDocument) ([]Finding, error) {
	var out []Finding
	add := func(d AuditDocument, score int, ev map[string]string) {
		out = append(out, Finding{
			Key: ViolationKey(policyID, version, d.ID), RuleType: spec.Type,
			DocumentID: d.ID, Score: score, Evidence: ev,
		})
	}
	switch spec.Type {
	case AuditRuleKeyword:
		if len(spec.Keywords) == 0 {
			return nil, &Error{Code: CodeAuditRuleConfig, Detail: "KEYWORD needs at least one keyword"}
		}
		for _, d := range docs {
			if hits := KeywordHits(d.Texts, spec.Keywords); len(hits) > 0 {
				add(d, binaryScore, map[string]string{"keywords": strings.Join(hits, ",")})
			}
		}
	case AuditRuleListSearch:
		if len(spec.WatchList) == 0 {
			return nil, &Error{Code: CodeAuditRuleConfig, Detail: "LIST_SEARCH needs a watch list"}
		}
		for _, d := range docs {
			if OnWatchList(d.PartyID, spec.WatchList) {
				add(d, binaryScore, map[string]string{"partyId": d.PartyID})
			}
		}
	case AuditRuleThreshold:
		if len(spec.Limits) == 0 || spec.Currency == "" {
			return nil, &Error{Code: CodeAuditRuleConfig, Detail: "THRESHOLD needs a currency and at least one category limit"}
		}
		for cat, limit := range spec.Limits {
			if !limit.IsPositive() {
				return nil, &Error{Code: CodeAuditRuleConfig, Detail: fmt.Sprintf("THRESHOLD limit for category %q must be positive", cat)}
			}
		}
		for _, d := range docs {
			limit, ok := spec.Limits[d.Category]
			if !ok {
				continue
			}
			if exceeded, pct := ThresholdExceeded(d.Amount, d.Currency, limit, spec.Currency); exceeded {
				add(d, binaryScore, map[string]string{"category": d.Category, "amount": d.Amount.String(), "limit": limit.String(), "pctOfLimit": pct.String()})
			}
		}
	case AuditRuleWeekendHoliday:
		hol := map[string]bool{}
		for _, h := range spec.Holidays {
			if _, err := time.Parse("2006-01-02", h); err != nil {
				return nil, &Error{Code: CodeAuditRuleConfig, Detail: fmt.Sprintf("holiday %q must be a YYYY-MM-DD date", h)}
			}
			hol[h] = true
		}
		for _, d := range docs {
			if OutsideBusinessCalendar(d.Date, hol) {
				add(d, binaryScore, map[string]string{"date": d.Date.UTC().Format("2006-01-02")})
			}
		}
	case AuditRuleSampling:
		if spec.Sample == nil {
			return nil, &Error{Code: CodeAuditRuleConfig, Detail: "SAMPLING needs a sample specification"}
		}
		ids := make([]string, 0, len(docs))
		for _, d := range docs {
			ids = append(ids, d.ID)
		}
		chosen, err := SelectSample(ids, *spec.Sample)
		if err != nil {
			return nil, err
		}
		for _, d := range docs {
			if chosen[d.ID] {
				add(d, binaryScore, map[string]string{"seed": spec.Sample.Seed})
			}
		}
	case AuditRuleAggregate:
		if !spec.Limit.IsPositive() || spec.Currency == "" {
			return nil, &Error{Code: CodeAuditRuleConfig, Detail: "AGGREGATE needs a positive limit and a currency"}
		}
		for _, b := range AggregateByPartyMonth(docs, spec.Currency, spec.Limit) {
			for _, id := range b.DocIDs {
				d := AuditDocument{ID: id}
				add(d, binaryScore, map[string]string{"partyId": b.PartyID, "period": b.Period, "total": b.Total.String()})
			}
		}
	case AuditRuleDuplicate:
		invs := make([]Invoice, 0, len(docs))
		byID := make(map[string]AuditDocument, len(docs))
		for _, d := range docs {
			byID[d.ID] = d
			invs = append(invs, Invoice{ID: d.ID, VendorID: d.PartyID, Number: d.Number, Amount: d.Amount,
				Currency: d.Currency, InvoiceDate: d.Date, PostedCredit: d.PostedCredit})
		}
		for _, v := range DuplicateViolations(FindDuplicates(invs)) {
			add(byID[v.DocumentID], binaryScore, map[string]string{"vendorId": v.VendorID, "duplicateOf": strings.Join(v.DuplicateOf, ",")})
		}
	default:
		return nil, &Error{Code: CodeAuditRuleType, Detail: fmt.Sprintf("audit rule type %q is not supported", spec.Type)}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DocumentID < out[j].DocumentID })
	return out, nil
}
