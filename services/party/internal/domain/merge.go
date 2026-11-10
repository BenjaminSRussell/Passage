package domain

import (
	"fmt"
	"time"
)

type Rule string

const (
	KeepWinner Rule = "keepWinner"
	KeepLoser  Rule = "keepLoser"
	Newest     Rule = "newest"
	NonEmpty   Rule = "nonEmpty"
)

var MergeFields = []string{
	"name", "org_legal_name", "org_trade_name", "org_registration_number", "org_industry_code",
	"person_given_name", "person_middle_name", "person_family_name", "person_prefix", "person_suffix",
	"person_gender", "language_code", "time_zone",
}

type Side struct {
	Values    map[string]string
	UpdatedAt time.Time
}

type RuleError struct{ Field, Reason string }

func (e *RuleError) Error() string {
	return fmt.Sprintf("survivorship rule for %q: %s", e.Field, e.Reason)
}

// ValidateRules refuses a survivorship rule for an unknown field or an unknown rule name.
func ValidateRules(rules map[string]Rule) error {
	known := make(map[string]bool, len(MergeFields))
	for _, f := range MergeFields {
		known[f] = true
	}
	for f, rule := range rules {
		if !known[f] {
			return &RuleError{Field: f, Reason: "unknown field"}
		}
		switch rule {
		case "", KeepWinner, KeepLoser, Newest, NonEmpty:
		default:
			return &RuleError{Field: f, Reason: fmt.Sprintf("unknown rule %q", rule)}
		}
	}
	return nil
}

func Survive(winner, loser Side, rules map[string]Rule) (map[string]string, error) {
	if err := ValidateRules(rules); err != nil {
		return nil, err
	}
	resolved := make(map[string]string, len(MergeFields))
	for _, f := range MergeFields {
		w, l := winner.Values[f], loser.Values[f]
		preferred := w
		switch rule := rules[f]; rule {
		case "", KeepWinner, NonEmpty:
		case KeepLoser:
			preferred = l
		case Newest:
			if loser.UpdatedAt.After(winner.UpdatedAt) {
				preferred = l
			}
		}
		if preferred == "" {
			preferred = w
		}
		if preferred == "" {
			preferred = l
		}
		resolved[f] = preferred
	}
	return resolved, nil
}

const (
	EventSource     = "/passage/party/party"
	AggregateParty  = "party"
	RoleCustomer    = "customer"
	PartyMergedType = "dev.passage.party.party.merged.v1"
)

type RoleIDMapping struct {
	Role   string `json:"role"`
	FromID string `json:"fromId"`
	ToID   string `json:"toId"`
}

type PartyMerged struct {
	MergeID         string          `json:"mergeId"`
	SurvivorPartyID string          `json:"survivorPartyId"`
	MergedPartyIDs  []string        `json:"mergedPartyIds"`
	RoleIDMap       []RoleIDMapping `json:"roleIdMap"`
	MergedAt        time.Time       `json:"mergedAt"`
	Survivor        PartySnapshot   `json:"survivor"`
}

type PartySnapshot struct {
	PartyNumber string `json:"partyNumber"`
	PartyType   string `json:"partyType"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Version     int    `json:"version"`
}

const (
	MergePending  = "pending"
	MergeApproved = "approved"
	MergeRejected = "rejected"
)

// MergeRequest is a merge awaiting, or past, its approval (#450). It is executed
// only on approval by a user other than RequestedBy.
type MergeRequest struct {
	ID              string          `json:"id"`
	SurvivorPartyID string          `json:"survivorPartyId"`
	MergedPartyID   string          `json:"mergedPartyId"`
	Rules           map[string]Rule `json:"rules"`
	Status          string          `json:"status"`
	RequestedBy     string          `json:"requestedBy"`
	DecidedBy       *string         `json:"decidedBy"`
	Reason          *string         `json:"reason"`
	MergeID         *string         `json:"mergeId"`
	RequestedAt     time.Time       `json:"requestedAt"`
	DecidedAt       *time.Time      `json:"decidedAt"`
	Replayed        bool            `json:"-"`
}
