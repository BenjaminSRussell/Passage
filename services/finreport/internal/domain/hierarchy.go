package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Limits from FR-15.13.
const (
	MaxHierarchyDepth = 12
	MaxHierarchyNodes = 20000
	hierarchySample   = 5
)

// Subject is what a derived hierarchy classifies.
type Subject string

const (
	SubjectCategory  Subject = "main_account_category"
	SubjectAccount   Subject = "main_account"
	SubjectDimension Subject = "dimension_value"
)

// Rule is one membership rule of a leaf. Attribute selects a member attribute
// (such as a cost center's region); empty means the member's key (account
// number, category id or dimension value). A rule matches when the value is in
// Values or inside the inclusive range From..To (compared as text, like the
// account ranges of report rows). A value ending in "*" is a prefix wildcard:
// "61*" matches 6100 and 6199, and "*" alone matches every value.
type Rule struct {
	Attribute string   `json:"attribute,omitempty"`
	From      string   `json:"from,omitempty"`
	To        string   `json:"to,omitempty"`
	Values    []string `json:"values,omitempty"`
}

// HierarchyNode is a node of the tree. Only leaves carry membership rules.
type HierarchyNode struct {
	Code   string `json:"code"`
	Label  string `json:"label"`
	Parent string `json:"parent,omitempty"`
	Rules  []Rule `json:"rules,omitempty"`
}

// Hierarchy is a derived financial hierarchy (FR-15.13). Membership is derived
// from rules, not listed. Status and Version are set when published.
type Hierarchy struct {
	SchemaVersion      string  `json:"schemaVersion"`
	Code               string  `json:"code"`
	Name               string  `json:"name"`
	Version            int     `json:"version"`
	Status             string  `json:"status"`
	Subject            Subject `json:"subject"`
	DimensionAttribute string  `json:"dimensionAttribute,omitempty"`
	Strict             bool    `json:"strict"`
	EffectiveFrom      string  `json:"effectiveFrom,omitempty"`
	// OwnerUserID is notified about unassigned members (FR-15.13). The service
	// sets it to the user who first saves the hierarchy when the body omits it.
	OwnerUserID string          `json:"ownerUserId,omitempty"`
	Nodes       []HierarchyNode `json:"nodes"`
}

// Member is one account, category or dimension value that a hierarchy classifies.
type Member struct {
	ID         string            `json:"id"`
	Key        string            `json:"key"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// Membership is the materialized result for one version: the leaf of every
// assigned member and the sorted ids of unassigned members.
type Membership struct {
	Assigned   map[string]string `json:"assigned"`
	Unassigned []string          `json:"unassigned"`
}

// Validate checks the shape of a hierarchy: codes, subject, dates, the node
// count and depth, parent references, cycles and rules on leaves only.
func (h Hierarchy) Validate() []Issue {
	var out []Issue
	add := func(field, code, detail string) { out = append(out, Issue{Field: field, Code: code, Detail: detail}) }
	if !codePattern.MatchString(h.Code) {
		add("code", ShapeCode, "must be 2 to 30 characters: upper-case letters, digits or hyphens, starting with a letter")
	}
	if strings.TrimSpace(h.Name) == "" || len(h.Name) > 120 {
		add("name", ShapeCode, "must be 1 to 120 characters")
	}
	switch h.Subject {
	case SubjectCategory, SubjectAccount:
		if h.DimensionAttribute != "" {
			add("dimensionAttribute", ShapeCode, "only dimension_value hierarchies take a dimension attribute")
		}
	case SubjectDimension:
		if !ValidCode(h.DimensionAttribute) {
			add("dimensionAttribute", ShapeCode, "dimension_value hierarchies need a dimension attribute")
		}
	default:
		add("subject", ShapeCode, "must be main_account_category, main_account or dimension_value")
	}
	if h.EffectiveFrom != "" {
		if _, err := time.Parse("2006-01-02", h.EffectiveFrom); err != nil {
			add("effectiveFrom", ShapeCode, "must be a date YYYY-MM-DD")
		}
	}
	if len(h.Nodes) == 0 || len(h.Nodes) > MaxHierarchyNodes {
		add("nodes", CodeTooLarge, "must contain 1 to 20000 nodes")
		return out
	}
	parent := map[string]string{}
	for i, n := range h.Nodes {
		field := "nodes[" + fmt.Sprint(i) + "]"
		if !ValidCode(n.Code) {
			add(field+".code", ShapeCode, "must start with a letter and use letters, digits or underscores")
			continue
		}
		if _, dup := parent[n.Code]; dup {
			add(field+".code", ShapeCode, "node code "+n.Code+" is used twice")
		}
		parent[n.Code] = n.Parent
		if strings.TrimSpace(n.Label) == "" || len(n.Label) > 120 {
			add(field+".label", ShapeCode, "must be 1 to 120 characters")
		}
		for _, r := range n.Rules {
			if (r.From == "") != (r.To == "") || (len(r.Values) == 0 && r.From == "") {
				add(field+".rules", ShapeCode, "a rule needs values or a from..to range")
			}
			if r.From != "" && r.From > r.To {
				add(field+".rules", ShapeCode, "rule range from must not exceed to")
			}
		}
	}
	children := map[string]int{}
	for _, n := range h.Nodes {
		if p := n.Parent; p != "" {
			if _, ok := parent[p]; !ok {
				add("nodes."+n.Code, ShapeCode, "parent "+p+" is not a node of this hierarchy")
			}
			children[p]++
		}
	}
	for _, n := range h.Nodes {
		if children[n.Code] > 0 && len(n.Rules) > 0 {
			add("nodes."+n.Code, ShapeCode, "only leaves may carry membership rules")
		}
		if d := depthOf(n.Code, parent); d > MaxHierarchyDepth {
			add("nodes."+n.Code, CodeTooLarge, "hierarchy depth exceeds 12 levels or contains a cycle")
		}
	}
	return out
}

// depthOf counts the levels from a node to its root; it stops past the depth
// limit or the node count, so a cycle reports a depth beyond the limit.
func depthOf(code string, parent map[string]string) int {
	d := 1
	for cur := code; parent[cur] != ""; cur = parent[cur] {
		d++
		if d > MaxHierarchyDepth || d > len(parent) {
			return MaxHierarchyDepth + 1
		}
	}
	return d
}

// matches reports whether a member satisfies one rule.
func (r Rule) matches(m Member) bool {
	value := m.Key
	if r.Attribute != "" {
		value = m.Attributes[r.Attribute]
	}
	for _, v := range r.Values {
		if v == value || (strings.HasSuffix(v, "*") && strings.HasPrefix(value, strings.TrimSuffix(v, "*"))) {
			return true
		}
	}
	return r.From != "" && value >= r.From && value <= r.To
}

// Materialize derives the membership of the members. A member matching the
// rules of two leaves is an overlap (REPORT-0014); one matching none is
// unassigned. Member ids must be present and unique, and every member needs a
// key.
func (h Hierarchy) Materialize(members []Member) (Membership, error) {
	seen := map[string]bool{}
	sorted := make([]Member, len(members))
	copy(sorted, members)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	out := Membership{Assigned: map[string]string{}, Unassigned: []string{}}
	for _, m := range sorted {
		if m.ID == "" || seen[m.ID] {
			return Membership{}, &Error{Code: ShapeCode, Detail: "member ids must be present and unique: " + m.ID}
		}
		if m.Key == "" {
			return Membership{}, &Error{Code: ShapeCode, Detail: "member " + m.ID + " has no key"}
		}
		seen[m.ID] = true
		var hit []string
		for _, n := range h.Nodes {
			for _, r := range n.Rules {
				if r.matches(m) {
					hit = append(hit, n.Code)
					break
				}
			}
		}
		switch len(hit) {
		case 0:
			out.Unassigned = append(out.Unassigned, m.ID)
		case 1:
			out.Assigned[m.ID] = hit[0]
		default:
			return Membership{}, errf(CodeHierOverlap, "Member %s matches leaves %s and %s.", m.ID, hit[0], hit[1])
		}
	}
	return out, nil
}

// CheckPublish refuses to publish a strict hierarchy with unassigned members
// (REPORT-0013). Non-strict hierarchies publish; their unassigned members roll
// into an "Unassigned" node and raise a notification (BR-15.13).
func (h Hierarchy) CheckPublish(m Membership) *Error {
	if !h.Strict || len(m.Unassigned) == 0 {
		return nil
	}
	sample := m.Unassigned
	if len(sample) > hierarchySample {
		sample = sample[:hierarchySample]
	}
	return errf(CodeHierUnassigned, "%d members are not assigned to any leaf of %s: %s.",
		len(m.Unassigned), h.Code, strings.Join(sample, ", "))
}
