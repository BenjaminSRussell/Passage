package domain

import (
	"fmt"
	"sort"
	"strings"
)

// Export governance and read-access codes (FR-48.24, FR-48.25).
// SEC-0709 download denied; SEC-0710 reason required; SEC-0711 unknown channel or
// classification; SEC-0714 read-log retention out of range; SEC-0715 negative row count.
// SEC-0707 and SEC-0713 belong to break-glass (breakglass.go).
const (
	CodeExportDenied   = "SEC-0709"
	CodeExportReason   = "SEC-0710"
	CodeExportChannel  = "SEC-0711"
	CodeExportRows     = "SEC-0715"
	CodeReadLogRetain  = "SEC-0714"
	exportReasonMin    = 10
	bulkRowThreshold   = 10000
	readLogDefaultDays = 730
	readLogMinDays     = 90
	readLogMaxDays     = 3650
)

// ExportClassification is the data class of an exported result (FR-48.24).
type ExportClassification string

const (
	ClassPII          ExportClassification = "PII"
	ClassFinancial    ExportClassification = "FINANCIAL"
	ClassConfidential ExportClassification = "CONFIDENTIAL"
	ClassPublic       ExportClassification = "PUBLIC"
)

// ExportChannel is where the export leaves the system.
type ExportChannel string

const (
	ChannelGrid     ExportChannel = "GRID"
	ChannelExplorer ExportChannel = "EXPLORER"
	ChannelReport   ExportChannel = "REPORT"
	ChannelAPIBulk  ExportChannel = "API_BULK"
	ChannelMCPBulk  ExportChannel = "MCP_BULK"
)

// ExportAction is one control in an export policy (9.48.13 export.policy.<classification>).
type ExportAction string

const (
	ActionAllow     ExportAction = "ALLOW"
	ActionAudit     ExportAction = "AUDIT"
	ActionWatermark ExportAction = "WATERMARK"
	ActionReason    ExportAction = "REASON"
	ActionApproval  ExportAction = "APPROVAL"
	ActionDeny      ExportAction = "DENY"
)

// defaultExportPolicy is the shipped policy per classification (9.48.13). CONFIDENTIAL
// is an assumption of AUDIT only; the PRD does not set it.
var defaultExportPolicy = map[ExportClassification][]ExportAction{
	ClassPII:          {ActionReason, ActionWatermark},
	ClassFinancial:    {ActionAudit},
	ClassConfidential: {ActionAudit},
	ClassPublic:       {ActionAllow},
}

// ExportPolicy overrides the action set of the named classifications (role or tenant policy).
type ExportPolicy map[ExportClassification][]ExportAction

// ExportRequest asks whether a download may proceed.
type ExportRequest struct {
	Classification ExportClassification `json:"classification"`
	Channel        ExportChannel        `json:"channel"`
	Rows           int                  `json:"rows"`
	Reason         string               `json:"reason,omitempty"`
	Policy         ExportPolicy         `json:"policy,omitempty"`
}

// ExportDecision is the outcome of an export check. Every export is audited.
type ExportDecision struct {
	Allowed          bool           `json:"allowed"`
	Actions          []ExportAction `json:"actions"`
	RequiresReason   bool           `json:"requiresReason"`
	RequiresApproval bool           `json:"requiresApproval"`
	Watermark        bool           `json:"watermark"`
	Audit            bool           `json:"audit"`
}

// DecideExport applies the export policy. DENY refuses the download (SEC-0709). A
// REASON policy needs a reason of at least 10 characters (SEC-0710). An APPROVAL
// policy, or a bulk API/MCP read above 10,000 rows of a non-public class, holds the
// download until approved (Allowed false). Audit is always set.
func DecideExport(req ExportRequest) (ExportDecision, error) {
	actions, ok := defaultExportPolicy[req.Classification]
	if !ok {
		return ExportDecision{}, &Error{Code: CodeExportChannel, Detail: fmt.Sprintf("classification %q is not supported", req.Classification)}
	}
	switch req.Channel {
	case ChannelGrid, ChannelExplorer, ChannelReport, ChannelAPIBulk, ChannelMCPBulk:
	default:
		return ExportDecision{}, &Error{Code: CodeExportChannel, Detail: fmt.Sprintf("channel %q is not supported", req.Channel)}
	}
	if req.Rows < 0 {
		return ExportDecision{}, &Error{Code: CodeExportRows, Detail: "row count cannot be negative"}
	}
	if override, ok := req.Policy[req.Classification]; ok {
		for _, a := range override {
			if !validExportAction(a) {
				return ExportDecision{}, &Error{Code: CodeExportChannel, Detail: fmt.Sprintf("export action %q must be ALLOW, AUDIT, WATERMARK, REASON, APPROVAL or DENY", a)}
			}
		}
		actions = override
	}
	set := map[ExportAction]bool{}
	for _, a := range actions {
		set[a] = true
	}
	if req.Classification != ClassPublic && (req.Channel == ChannelAPIBulk || req.Channel == ChannelMCPBulk) && req.Rows > bulkRowThreshold {
		set[ActionApproval] = true
	}
	if set[ActionDeny] {
		return ExportDecision{}, &Error{Code: CodeExportDenied, Detail: fmt.Sprintf("download denied by export policy for %s data", req.Classification)}
	}
	if set[ActionReason] && len(strings.TrimSpace(req.Reason)) < exportReasonMin {
		return ExportDecision{}, &Error{Code: CodeExportReason, Detail: "the export policy requires a reason of at least 10 characters"}
	}
	out := ExportDecision{Actions: []ExportAction{}, RequiresReason: set[ActionReason], RequiresApproval: set[ActionApproval], Watermark: set[ActionWatermark], Audit: true}
	for a := range set {
		out.Actions = append(out.Actions, a)
	}
	sort.Slice(out.Actions, func(i, j int) bool { return out.Actions[i] < out.Actions[j] })
	out.Allowed = !out.RequiresApproval
	return out, nil
}

func validExportAction(a ExportAction) bool {
	switch a {
	case ActionAllow, ActionAudit, ActionWatermark, ActionReason, ActionApproval, ActionDeny:
		return true
	}
	return false
}

// ReadRule marks a field whose reads are logged (FR-48.25). FieldFQN is exact, or
// "*.name" to match any field with that last segment.
type ReadRule struct {
	FieldFQN       string               `json:"fieldFqn"`
	Classification ExportClassification `json:"classification"`
}

// DefaultReadRules are the default read-logged fields: national ids, bank accounts,
// salary, health, I-9 documents and date of birth (FR-48.25).
var DefaultReadRules = []ReadRule{
	{FieldFQN: "*.national_id", Classification: ClassPII},
	{FieldFQN: "*.bank_account", Classification: ClassFinancial},
	{FieldFQN: "*.salary", Classification: ClassFinancial},
	{FieldFQN: "*.health", Classification: ClassPII},
	{FieldFQN: "*.i9_document", Classification: ClassPII},
	{FieldFQN: "*.date_of_birth", Classification: ClassPII},
}

// ReadClassification is the result of classifying the fields of one read. It carries
// field names only; values are never logged.
type ReadClassification struct {
	Logged        bool     `json:"logged"`
	Fields        []string `json:"fields"`
	RetentionDays int      `json:"retentionDays"`
}

// ClassifyRead returns which requested fields the read-log policy covers. retentionDays
// 0 means the default of 730; otherwise it must be within 90..3650.
func ClassifyRead(fields []string, rules []ReadRule, retentionDays int) (ReadClassification, error) {
	if retentionDays == 0 {
		retentionDays = readLogDefaultDays
	}
	if retentionDays < readLogMinDays || retentionDays > readLogMaxDays {
		return ReadClassification{}, &Error{Code: CodeReadLogRetain, Detail: fmt.Sprintf("read-log retention %d days is outside %d..%d", retentionDays, readLogMinDays, readLogMaxDays)}
	}
	if rules == nil {
		rules = DefaultReadRules
	}
	out := ReadClassification{Fields: []string{}, RetentionDays: retentionDays}
	seen := map[string]bool{}
	for _, f := range fields {
		if seen[f] {
			continue
		}
		seen[f] = true
		for _, r := range rules {
			if readRuleMatches(r.FieldFQN, f) {
				out.Fields = append(out.Fields, f)
				break
			}
		}
	}
	sort.Strings(out.Fields)
	out.Logged = len(out.Fields) > 0
	return out, nil
}

func readRuleMatches(rule, field string) bool {
	if strings.HasPrefix(rule, "*.") {
		return field == rule[2:] || strings.HasSuffix(field, rule[1:])
	}
	return rule == field
}
