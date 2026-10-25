package domain

import (
	"regexp"
	"strings"
)

const CodeFileXSD = "AP-FIL-002"

var xmllintLine = regexp.MustCompile(`^.+?:\d+: element (\S+): Schemas validity error : (.+)$`)

type XSDError struct {
	Path   string
	Reason string
}

func (e XSDError) AsError() *Error {
	return &Error{Code: CodeFileXSD, Detail: e.Path + " " + e.Reason}
}

func (e XSDError) Error() string {
	return e.AsError().Error()
}

func ParseXmllintLine(line string) (XSDError, bool) {
	m := xmllintLine.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return XSDError{}, false
	}
	return XSDError{Path: m[1], Reason: m[2]}, true
}
