package domain

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

const (
	OpEmpty = "empty"
	OpEq    = "eq"
	OpNe    = "ne"
	OpIn    = "in"
	OpNotIn = "notIn"
	OpMatch = "match"

	maxRejectConditions = 50
	maxMatchPattern     = 200
	maxInValues         = 200
	maxReasonCode       = 64
)

var reasonCode = regexp.MustCompile(`^[A-Z][A-Z0-9]*(-[A-Z0-9]+)+$`)

// RejectWhen sends a row to the exception queue with a stated reason code when a mapped target meets the condition.
// Conditions run on the mapped output once every field rule has succeeded, and every matching condition adds its own reject.
type RejectWhen struct {
	Field  string   `json:"field"`
	Op     string   `json:"op"`
	Value  *string  `json:"value,omitempty"`
	Values []string `json:"values,omitempty"`
	Code   string   `json:"code"`
	re     *regexp.Regexp
}

func (c *RejectWhen) check(targets map[string]bool) *Error {
	invalid := func(why string) *Error {
		return &Error{CodeMapInvalid, "rejectWhen on " + c.Field + ": " + why}
	}
	if !targets[c.Field] {
		return &Error{CodeMapInvalid, "rejectWhen field " + c.Field + " is not a mapping target"}
	}
	if len(c.Code) > maxReasonCode || !reasonCode.MatchString(c.Code) || strings.HasPrefix(c.Code, "MIG-") {
		return invalid("code must be upper-case words joined by hyphens, such as CUST-NO-GROUP, at most 64 characters and not starting with MIG-")
	}
	switch c.Op {
	case OpEmpty:
		if c.Value != nil || len(c.Values) > 0 {
			return invalid("empty takes no value")
		}
	case OpEq, OpNe:
		if c.Value == nil || len(c.Values) > 0 {
			return invalid(c.Op + " takes value")
		}
	case OpIn, OpNotIn:
		if c.Value != nil || len(c.Values) == 0 || len(c.Values) > maxInValues {
			return invalid(c.Op + " takes 1 to 200 values")
		}
	case OpMatch:
		if c.Value == nil || len(c.Values) > 0 || len(*c.Value) > maxMatchPattern {
			return invalid("match takes a pattern of at most 200 characters")
		}
		re, err := regexp.Compile(*c.Value)
		if err != nil {
			return invalid("match pattern is not valid RE2")
		}
		c.re = re
	default:
		return unknownFn(c.Op)
	}
	return nil
}

// matches compares the string form of the mapped value; an absent target is empty, differs from any value and is in no list.
func (c RejectWhen) matches(out map[string]any) bool {
	text := ""
	if v, ok := out[c.Field]; ok {
		text = fmt.Sprint(v)
	}
	present := text != ""
	switch c.Op {
	case OpEmpty:
		return !present
	case OpEq:
		return present && text == *c.Value
	case OpNe:
		return !present || text != *c.Value
	case OpIn:
		return present && slices.Contains(c.Values, text)
	case OpNotIn:
		return !present || !slices.Contains(c.Values, text)
	case OpMatch:
		return present && c.re != nil && c.re.MatchString(text)
	}
	return false
}

func (m Mapping) rejectConditions(out map[string]any) []Reject {
	var rejects []Reject
	for _, c := range m.RejectWhen {
		if c.matches(out) {
			rejects = append(rejects, Reject{c.Field, c.Code, "matched rejectWhen " + c.Op})
		}
	}
	return rejects
}

func (m *Mapping) checkRejectWhen(targets map[string]bool) *Error {
	if len(m.RejectWhen) > maxRejectConditions {
		return &Error{CodeMapInvalid, "at most 50 rejectWhen conditions"}
	}
	for i := range m.RejectWhen {
		if e := m.RejectWhen[i].check(targets); e != nil {
			return e
		}
	}
	return nil
}
