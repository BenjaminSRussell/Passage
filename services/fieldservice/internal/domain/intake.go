package domain

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Codes of email-to-case intake (FR-45.16, BR-45.13). CASE-MAIL-001 is the PRD's
// info message for a message attached to a case; the rest are this service's.
const (
	CodeMailAttached = "CASE-MAIL-001"
	CodeMailbox      = "CASE-MAIL-002"
	CodeMailboxCat   = "CASE-MAIL-003"
)

// Routes of an inbound message.
const (
	RouteAttached = "ATTACHED"
	RouteNewCase  = "NEW_CASE"
)

// caseToken matches a case token in a subject, such as "[CS-1042]" or "[CS-001042]".
var caseToken = regexp.MustCompile(`(?i)\[CS-(\d{1,12})\]`)

// CaseToken returns the case number named by the first [CS-n] token of a subject in
// the stored form "CS-%06d", so "[CS-1042]" names CS-001042 (BR-45.13).
func CaseToken(subject string) (string, bool) {
	m := caseToken.FindStringSubmatch(subject)
	if m == nil {
		return "", false
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || n < 1 {
		return "", false
	}
	return fmt.Sprintf("CS-%06d", n), true
}

// Automated reports whether a message is an automatic reply or notice, which never
// gets an acknowledgment (BR-45.13): Auto-Submitted other than "no" (RFC 3834), or
// an X-Autoreply header.
func Automated(autoSubmitted string, xAutoreply bool) bool {
	v := strings.ToLower(strings.TrimSpace(autoSubmitted))
	return xAutoreply || (v != "" && v != "no")
}

// ThreadCase is a case an inbound message may thread onto.
type ThreadCase struct {
	ID     string
	Number string
	Status string
}

// Threading is where one inbound message goes.
type Threading struct {
	Route string
	// Case is the open case the message is attached to (ATTACHED), or the closed case
	// the new case continues (NEW_CASE with reopened_from); nil for a fresh case.
	Case *ThreadCase
	// Acknowledge is true when the sender gets the one acknowledgment of a new case.
	Acknowledge bool
}

// Thread applies BR-45.13. byToken is the case named by a [CS-n] token in the
// subject and byReply the case already holding the message In-Reply-To names; a
// token wins over the reply chain. A message is attached to that case unless it is
// CLOSED (or CANCELED, which is final as well): then a new case is opened, linked to
// it by reopened_from. Without either a new case is opened in the mailbox's default
// category. Only a new case is acknowledged, and never for an automated message.
func Thread(byToken, byReply *ThreadCase, automated bool) Threading {
	target := byToken
	if target == nil {
		target = byReply
	}
	if target != nil && target.Status != GenericClosed && target.Status != GenericCanceled {
		return Threading{Route: RouteAttached, Case: target}
	}
	return Threading{Route: RouteNewCase, Case: target, Acknowledge: !automated}
}
