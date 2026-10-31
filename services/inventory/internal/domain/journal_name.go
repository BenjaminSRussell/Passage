package domain

import (
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Journal name problem codes (3.16.18 catalog extension).
const (
	CodeJournalNameNotFound = "INV-JNLN-NOT-FOUND"
	CodeJournalNameUnusable = "INV-JNL-007" // the name is inactive, of another legal entity or of another journal type
	CodeApprovalRequired    = "INV-JNL-008" // the journal name requires the approval workflow before posting
	CodeJournalNameDup      = "INV-JNLN-001"
)

// MaxJournalNameDescription is the longest description of a journal name.
const MaxJournalNameDescription = 200

var (
	journalNameCode = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,19}$`)
	offsetAccount   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

// JournalName is a journal setup row (PRD 3.16.13).
type JournalName struct {
	ID               string
	LegalEntityID    string
	Code             string
	Description      string
	Type             JournalType
	RequiresApproval bool
	OffsetAccountID  string
	Active           bool
	Version          int
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Validate checks a journal name before it is stored.
func (n JournalName) Validate() error {
	if !journalNameCode.MatchString(n.Code) {
		return fail(CodeInvalid, "code must be 1 to 20 characters of A-Z, 0-9, _ or -")
	}
	if utf8.RuneCountInString(n.Description) > MaxJournalNameDescription {
		return fail(CodeInvalid, "description must be at most %d characters", MaxJournalNameDescription)
	}
	if !slices.Contains(KeyedJournalTypes, n.Type) {
		return fail(CodeInvalid, "journalType %q cannot be keyed", n.Type)
	}
	switch {
	case n.OffsetAccountID != "" && n.Type != JournalMovement:
		return fail(CodeInvalid, "only a MOVEMENT journal name takes an offset account")
	case n.OffsetAccountID != "" && !offsetAccount.MatchString(n.OffsetAccountID):
		return fail(CodeInvalid, "offsetAccountId must be an account id of 1 to 64 letters, digits, '.', '_' or '-'")
	}
	return nil
}

// CheckForJournal reports why a journal of the given legal entity and type may
// not be created under this name, or nil.
func (n JournalName) CheckForJournal(legalEntityID string, t JournalType) error {
	switch {
	case !n.Active:
		return fail(CodeJournalNameUnusable, "journal name %s is inactive", n.Code)
	case n.LegalEntityID != legalEntityID:
		return fail(CodeJournalNameUnusable, "journal name %s belongs to another legal entity", n.Code)
	case n.Type != t:
		return fail(CodeJournalNameUnusable, "journal name %s is for %s journals, not %s", n.Code, n.Type, t)
	}
	return nil
}

// CheckPostable reports whether a journal under this name may post in the given
// status: a name that requires approval refuses a post from DRAFT.
func (n JournalName) CheckPostable(s JournalStatus) error {
	if n.RequiresApproval && s != JournalApproved {
		return fail(CodeApprovalRequired, "journal name %s requires approval before posting; submit the journal and have it approved", n.Code)
	}
	return nil
}

// NormalizeJournalName trims the free-text fields and upper-cases the code.
func NormalizeJournalName(n JournalName) JournalName {
	n.Code = strings.ToUpper(strings.TrimSpace(n.Code))
	n.Description = strings.TrimSpace(n.Description)
	n.OffsetAccountID = strings.TrimSpace(n.OffsetAccountID)
	return n
}
