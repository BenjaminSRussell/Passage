package domain

import (
	"fmt"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/phiaudit"
)

// Break-glass review (#606, HIPAA emergency access). A read recorded with a purpose that starts with the reserved
// reason code BREAK-GLASS used a duty kept for emergencies; a second person reviews each one once.
const (
	// BreakGlassPurpose is the reserved reason code: "BREAK-GLASS" alone or followed by ":" or a space and the reason.
	BreakGlassPurpose = phiaudit.BreakGlassPurpose

	ReviewApproved = "APPROVED"
	ReviewFlagged  = "FLAGGED"

	// MaxReviewNote bounds the reviewer's note.
	MaxReviewNote = 1000

	// CodeReadNotFound is a review of a read the tenant's log does not hold.
	CodeReadNotFound = "AUD-0004"
	// CodeReviewRefused is a review that may not be made: not a break-glass read, already reviewed, or by the reader.
	CodeReviewRefused = "AUD-0005"
)

// IsBreakGlass reports whether purpose carries the reserved break-glass reason code (platform/go/phiaudit, so the
// services that record reads and this service agree).
func IsBreakGlass(purpose string) bool { return phiaudit.IsBreakGlass(purpose) }

// ValidateReview reports problems with a review's outcome and note: FLAGGED needs a note saying why.
func ValidateReview(outcome, note string) map[string]string {
	problems := map[string]string{}
	switch outcome {
	case ReviewApproved:
	case ReviewFlagged:
		if strings.TrimSpace(note) == "" {
			problems["note"] = "is required when the outcome is FLAGGED: say what was wrong with the access"
		}
	default:
		problems["outcome"] = "must be APPROVED or FLAGGED"
	}
	if len(note) > MaxReviewNote {
		problems["note"] = fmt.Sprintf("is at most %d characters", MaxReviewNote)
	}
	return problems
}

// ReviewRefusal is why reviewer may not review a read with readerID and purpose, or "" when the review may go ahead.
func ReviewRefusal(purpose, readerID, reviewerID string, alreadyReviewed bool) string {
	switch {
	case !IsBreakGlass(purpose):
		return "only break-glass reads (purpose " + BreakGlassPurpose + ") are reviewed"
	case alreadyReviewed:
		return "this read has already been reviewed"
	case readerID == reviewerID:
		return "the person who read the record cannot review their own break-glass access"
	}
	return ""
}
