// Package domain holds the segregation-of-duties (SoD) rules engine of the
// compliance service. It is pure: no I/O, and time only enters through the
// now parameters the callers pass in.
package domain

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	SeverityHigh   = "HIGH"
	SeverityMedium = "MEDIUM"
	SeverityLow    = "LOW"

	RuleActive  = "ACTIVE"
	RuleRetired = "RETIRED"

	StateOpen         = "OPEN"
	StateMitigated    = "MITIGATED"
	StateResolved     = "RESOLVED"
	StateAcceptedRisk = "ACCEPTED_RISK"

	DefaultMaxMitigationDays = 365
	MaxMitigationDaysCeiling = 730

	CodeValidation     = "SOD-1100"
	CodeBlocked        = "SOD-1101"
	CodeSelfApproval   = "SOD-1102"
	CodeNotMitigatable = "SOD-1103"
	CodeNotOpen        = "SOD-1104"
	CodeMissingField   = "SOD-1105"
	CodeExpiryTooLong  = "SOD-1106"
	CodeAcceptRisk     = "SOD-1107"
)

var ruleIDPattern = regexp.MustCompile(`^[A-Z0-9]+-\d{3}$`)

// Rule is `duty_a CONFLICTS duty_b`. Duties may be glob patterns (`*Post*`).
type Rule struct {
	RuleID            string `json:"ruleId"`
	Name              string `json:"name"`
	DutyA             string `json:"dutyA"`
	DutyB             string `json:"dutyB"`
	Severity          string `json:"severity"`
	RiskID            string `json:"riskId"`
	Description       string `json:"description,omitempty"`
	MitigationAllowed bool   `json:"mitigationAllowed"`
	MaxMitigationDays int    `json:"maxMitigationDays"`
	Status            string `json:"status"`
	Version           int    `json:"version"`
}

// Role is a named bundle of duties as a user would be assigned it.
type Role struct {
	Name   string   `json:"name"`
	Duties []string `json:"duties"`
}

// Conflict is a detected violation for one user and one rule.
type Conflict struct {
	UserID       string     `json:"userId"`
	RuleID       string     `json:"ruleId"`
	Severity     string     `json:"severity"`
	RiskID       string     `json:"riskId"`
	DutyA        string     `json:"dutyA"`
	DutyB        string     `json:"dutyB"`
	RolesA       []string   `json:"rolesA"`
	RolesB       []string   `json:"rolesB"`
	State        string     `json:"state"`
	MitigationID string     `json:"mitigationId,omitempty"`
	ExpiresAt    *time.Time `json:"expiresAt,omitempty"`
}

// Mitigation is a compensating control approved for one conflict.
type Mitigation struct {
	MitigationID string
	ControlRef   string
	Reason       string
	ApproverID   string
	ExpiresAt    time.Time
}

// Error carries a stable SoD message code (SOD-nnnn) and a human detail.
type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

func newErr(code, format string, args ...any) *Error {
	return &Error{Code: code, Detail: fmt.Sprintf(format, args...)}
}

// SeedRules returns the seed rules listed in FR-48.4 (eleven rule ids; the
// remaining rules of the 50-rule set come from the GRC import in a later slice).
func SeedRules() []Rule {
	return []Rule{
		seed("P2P-001", "Vendor maintenance vs payments", "MaintainVendors", "ProcessVendorPayments", SeverityHigh),
		seed("P2P-002", "PO creation vs PO approval", "CreatePurchaseOrders", "ApprovePurchaseOrders", SeverityHigh),
		seed("P2P-003", "Invoice posting vs invoice approval", "PostVendorInvoices", "ApproveVendorInvoices", SeverityHigh),
		seed("P2P-004", "Goods receipt vs invoice posting", "ReceiveGoods", "PostVendorInvoices", SeverityMedium),
		seed("O2C-001", "Customer maintenance vs cash posting", "MaintainCustomers", "PostCustomerPayments", SeverityHigh),
		seed("O2C-002", "Sales order entry vs credit approval", "CreateSalesOrders", "ApproveCreditLimits", SeverityHigh),
		seed("O2C-003", "Credit note issue vs cash posting", "IssueCreditNotes", "PostCustomerPayments", SeverityHigh),
		seed("GL-001", "Journal posting vs journal approval", "PostGeneralJournals", "ApproveGeneralJournals", SeverityHigh),
		seed("GL-002", "Chart maintenance vs journal posting", "MaintainChartOfAccounts", "PostGeneralJournals", SeverityMedium),
		seed("SEC-001", "User administration vs any posting duty", "MaintainUsersAndRoles", "*Post*", SeverityHigh),
		seed("HR-001", "Worker bank details vs payroll run", "MaintainWorkerBankDetails", "RunPayroll", SeverityHigh),
	}
}

func seed(id, name, a, b, sev string) Rule {
	return Rule{
		RuleID: id, Name: name, DutyA: a, DutyB: b, Severity: sev, RiskID: id,
		MitigationAllowed: true, MaxMitigationDays: DefaultMaxMitigationDays, Status: RuleActive, Version: 1,
	}
}

// ValidateRule checks the rule schema of FR-48.4.
func ValidateRule(r Rule) error {
	switch {
	case !ruleIDPattern.MatchString(r.RuleID):
		return newErr(CodeValidation, "rule id %q must look like P2P-001", r.RuleID)
	case r.DutyA == "" || r.DutyB == "":
		return newErr(CodeValidation, "rule %s needs two duties", r.RuleID)
	case r.DutyA == r.DutyB:
		return newErr(CodeValidation, "rule %s must relate two different duties", r.RuleID)
	case !validPattern(r.DutyA) || !validPattern(r.DutyB):
		return newErr(CodeValidation, "rule %s has a malformed duty pattern", r.RuleID)
	case r.Severity != SeverityHigh && r.Severity != SeverityMedium && r.Severity != SeverityLow:
		return newErr(CodeValidation, "rule %s severity %q is not HIGH, MEDIUM or LOW", r.RuleID, r.Severity)
	case r.RiskID == "":
		return newErr(CodeValidation, "rule %s needs a risk id", r.RuleID)
	case r.MaxMitigationDays < 1 || r.MaxMitigationDays > MaxMitigationDaysCeiling:
		return newErr(CodeValidation, "rule %s max mitigation days must be 1..%d", r.RuleID, MaxMitigationDaysCeiling)
	}
	return nil
}

func validPattern(p string) bool {
	_, err := path.Match(p, "")
	return err == nil
}

// Detect evaluates every active rule against the effective duties of the roles
// and returns one OPEN conflict per violated rule, ordered by rule id.
func Detect(userID string, rules []Rule, roles []Role) []Conflict {
	grants := map[string][]string{}
	for _, r := range roles {
		for _, d := range r.Duties {
			grants[d] = appendUnique(grants[d], r.Name)
		}
	}
	duties := make([]string, 0, len(grants))
	for d := range grants {
		duties = append(duties, d)
	}
	sort.Strings(duties)

	out := []Conflict{}
	for _, rule := range rules {
		if rule.Status != RuleActive {
			continue
		}
		ma := matching(rule.DutyA, duties)
		mb := matching(rule.DutyB, duties)
		a, b, ok := distinctPair(ma, mb)
		if !ok {
			continue
		}
		out = append(out, Conflict{
			UserID: userID, RuleID: rule.RuleID, Severity: rule.Severity, RiskID: rule.RiskID,
			DutyA: a, DutyB: b, RolesA: rolesGranting(ma, grants), RolesB: rolesGranting(mb, grants),
			State: StateOpen,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RuleID < out[j].RuleID })
	return out
}

// SimulateResult is the answer to "what would this assignment do?" before it is saved.
type SimulateResult struct {
	NewConflicts    []Conflict
	Blocked         bool
	BlockingRuleIDs []string
}

// Simulate reports the conflicts a proposed role assignment introduces on top
// of the current roles. known maps rule id to the state the service has
// recorded for this user (OPEN, MITIGATED or ACCEPTED_RISK; RESOLVED rows are
// ignored because the conflict would be raised again). With blockHigh set
// (FR-48.5, sod.block_high) the assignment is blocked when the resulting role
// set holds any HIGH conflict that is not MITIGATED: a new one, or an existing
// OPEN one the user already carries (AC-48.3).
func Simulate(userID string, rules []Rule, current, proposed []Role, known map[string]string, blockHigh bool) SimulateResult {
	before := map[string]bool{}
	for _, c := range Detect(userID, rules, current) {
		before[c.RuleID] = true
	}
	all := make([]Role, 0, len(current)+len(proposed))
	all = append(append(all, current...), proposed...)

	res := SimulateResult{NewConflicts: []Conflict{}, BlockingRuleIDs: []string{}}
	for _, c := range Detect(userID, rules, all) {
		if st := known[c.RuleID]; st == StateMitigated || st == StateAcceptedRisk {
			c.State = st
		}
		if !before[c.RuleID] {
			res.NewConflicts = append(res.NewConflicts, c)
		}
		if blockHigh && c.Severity == SeverityHigh && c.State == StateOpen {
			res.Blocked = true
			res.BlockingRuleIDs = append(res.BlockingRuleIDs, c.RuleID)
		}
	}
	return res
}

// RuleByID finds a rule in rules.
func RuleByID(rules []Rule, id string) (Rule, bool) {
	for _, r := range rules {
		if r.RuleID == id {
			return r, true
		}
	}
	return Rule{}, false
}

// Mitigate moves an OPEN conflict to MITIGATED. The approver must not be the
// conflicted user, the reason and control are mandatory, and the expiry must be
// in the future and within min(rule cap, tenant cap) days (BR-48.1, FR-48.5).
func (c *Conflict) Mitigate(rule Rule, m Mitigation, now time.Time, tenantCapDays int) error {
	switch {
	case c.State != StateOpen:
		return newErr(CodeNotOpen, "conflict %s is %s, not OPEN", c.RuleID, c.State)
	case !rule.MitigationAllowed:
		return newErr(CodeNotMitigatable, "rule %s does not allow mitigation", rule.RuleID)
	case strings.TrimSpace(m.ApproverID) == "":
		return newErr(CodeMissingField, "the approver is required")
	case strings.EqualFold(strings.TrimSpace(m.ApproverID), c.UserID):
		return newErr(CodeSelfApproval, "You cannot approve a mitigation for your own conflict.")
	case strings.TrimSpace(m.Reason) == "" || strings.TrimSpace(m.ControlRef) == "":
		return newErr(CodeMissingField, "a reason and a compensating control are required")
	}
	maxDays := rule.MaxMitigationDays
	if maxDays <= 0 {
		maxDays = DefaultMaxMitigationDays
	}
	if tenantCapDays > 0 && tenantCapDays < maxDays {
		maxDays = tenantCapDays
	}
	limit := now.Add(time.Duration(maxDays) * 24 * time.Hour)
	if !m.ExpiresAt.After(now) || m.ExpiresAt.After(limit) {
		return newErr(CodeExpiryTooLong, "mitigation expiry must be after now and at most %d days out", maxDays)
	}
	c.State = StateMitigated
	c.MitigationID = m.MitigationID
	expires := m.ExpiresAt.UTC()
	c.ExpiresAt = &expires
	return nil
}

// Expire reopens a MITIGATED conflict whose mitigation has lapsed. It reports
// whether a transition happened.
func (c *Conflict) Expire(now time.Time) bool {
	if c.State != StateMitigated || c.ExpiresAt == nil || now.Before(*c.ExpiresAt) {
		return false
	}
	c.State = StateOpen
	c.MitigationID = ""
	c.ExpiresAt = nil
	return true
}

// Resolve marks a conflict resolved because the conflicting role was removed.
// Any state but RESOLVED may be resolved: once the duties no longer coexist,
// a mitigation or a risk acceptance has nothing left to cover.
func (c *Conflict) Resolve() error {
	if c.State == StateResolved {
		return newErr(CodeNotOpen, "conflict %s is already RESOLVED", c.RuleID)
	}
	c.State = StateResolved
	c.MitigationID = ""
	c.ExpiresAt = nil
	return nil
}

// AcceptRisk records accepted risk with a reason. Only OPEN LOW and MEDIUM
// conflicts qualify (BR-48.1); the compliance.sod.admin duty is enforced by the API.
func (c *Conflict) AcceptRisk(reason string) error {
	switch {
	case c.State != StateOpen:
		return newErr(CodeNotOpen, "conflict %s is %s, not OPEN", c.RuleID, c.State)
	case c.Severity == SeverityHigh:
		return newErr(CodeAcceptRisk, "a HIGH conflict cannot be accepted as risk; mitigate it or remove a role")
	case strings.TrimSpace(reason) == "":
		return newErr(CodeMissingField, "a reason is required to accept the risk")
	}
	c.State = StateAcceptedRisk
	return nil
}

func matching(pattern string, duties []string) []string {
	var out []string
	for _, d := range duties {
		if ok, _ := path.Match(pattern, d); ok {
			out = append(out, d)
		}
	}
	return out
}

func distinctPair(as, bs []string) (string, string, bool) {
	for _, a := range as {
		for _, b := range bs {
			if a != b {
				return a, b, true
			}
		}
	}
	return "", "", false
}

func rolesGranting(duties []string, grants map[string][]string) []string {
	var out []string
	for _, d := range duties {
		for _, r := range grants[d] {
			out = appendUnique(out, r)
		}
	}
	sort.Strings(out)
	return out
}

func appendUnique(xs []string, v string) []string {
	for _, x := range xs {
		if x == v {
			return xs
		}
	}
	return append(xs, v)
}
