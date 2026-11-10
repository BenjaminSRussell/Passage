package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Supplier collaboration rules (#511, items 5 and 6).

const (
	RequestOnboarding    = "ONBOARDING"
	RequestProfileChange = "PROFILE_CHANGE"
	RequestOther         = "OTHER"

	RequestInvited   = "INVITED"
	RequestSubmitted = "SUBMITTED"
	RequestApproved  = "APPROVED"
	RequestRejected  = "REJECTED"
	RequestWithdrawn = "WITHDRAWN"

	DirectionFromVendor = "FROM_VENDOR"
	DirectionToVendor   = "TO_VENDOR"

	QuestionText   = "TEXT"
	QuestionYesNo  = "YESNO"
	QuestionChoice = "CHOICE"

	// MaxDocumentBytes bounds one stored document. The PRD asks for 25 MB, which needs the object store (not built).
	MaxDocumentBytes = 5 << 20
	maxAnswerRunes   = 2000
)

var (
	questionIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	docNoPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,39}$`)
	// documentTypes are the content types a vendor may exchange. Anything executable or active is refused.
	documentTypes = map[string]bool{
		"application/pdf": true, "image/png": true, "image/jpeg": true, "text/plain": true, "text/csv": true,
		"application/zip":    true,
		"application/msword": true, "application/vnd.ms-excel": true,
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document": true,
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":       true,
	}
)

// Question is one line of a vendor questionnaire.
type Question struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Options  []string `json:"options,omitempty"`
}

// ValidateQuestions checks a questionnaire's questions and returns the problems by field.
func ValidateQuestions(qs []Question) map[string]string {
	errs := map[string]string{}
	if len(qs) == 0 || len(qs) > 100 {
		errs["questions"] = "must hold 1 to 100 questions"
	}
	seen := map[string]bool{}
	for i, q := range qs {
		key := func(f string) string { return fmt.Sprintf("questions[%d].%s", i, f) }
		switch {
		case !questionIDPattern.MatchString(q.ID):
			errs[key("id")] = "must be lowercase letters, digits and underscore, starting with a letter"
		case seen[q.ID]:
			errs[key("id")] = "is used by an earlier question"
		}
		seen[q.ID] = true
		if n := utf8.RuneCountInString(strings.TrimSpace(q.Label)); n < 1 || n > 300 {
			errs[key("label")] = "must be 1 to 300 characters"
		}
		switch q.Type {
		case QuestionText, QuestionYesNo:
			if len(q.Options) > 0 {
				errs[key("options")] = "only a CHOICE question has options"
			}
		case QuestionChoice:
			if len(q.Options) < 2 || len(q.Options) > 20 {
				errs[key("options")] = "a CHOICE question needs 2 to 20 options"
			}
		default:
			errs[key("type")] = "must be TEXT, YESNO or CHOICE"
		}
	}
	return errs
}

// ValidateAnswers checks a vendor's answers against the questionnaire: every required question is answered, every answer
// belongs to a question and fits its type.
func ValidateAnswers(qs []Question, answers map[string]string) map[string]string {
	errs := map[string]string{}
	byID := map[string]Question{}
	for _, q := range qs {
		byID[q.ID] = q
	}
	for id := range answers {
		if _, ok := byID[id]; !ok {
			errs["answers."+id] = "is not a question of this questionnaire"
		}
	}
	for _, q := range qs {
		a := strings.TrimSpace(answers[q.ID])
		key := "answers." + q.ID
		switch {
		case a == "":
			if q.Required {
				errs[key] = "is required"
			}
		case utf8.RuneCountInString(a) > maxAnswerRunes:
			errs[key] = fmt.Sprintf("must be at most %d characters", maxAnswerRunes)
		case q.Type == QuestionYesNo && a != "YES" && a != "NO":
			errs[key] = "must be YES or NO"
		case q.Type == QuestionChoice && !containsString(q.Options, a):
			errs[key] = "must be one of the options"
		}
	}
	return errs
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// CanSubmit says whether a request in status may be submitted by its vendor.
func CanSubmit(kind, status string) bool {
	return status == RequestInvited || (kind != RequestOnboarding && status == "")
}

// CanDecide says whether a buyer may approve or reject a request in status.
func CanDecide(status string) bool { return status == RequestSubmitted }

// CanWithdraw says whether a vendor may withdraw its request.
func CanWithdraw(status string) bool { return status == RequestSubmitted || status == RequestInvited }

// Document is the validated content of an upload.
type Document struct {
	Title       string
	FileName    string
	ContentType string
	Content     []byte
	SHA256      string
}

// CheckDocument validates an upload and computes its digest. The file name is reduced to its base name.
func CheckDocument(title, fileName, contentType string, content []byte) (Document, map[string]string) {
	errs := map[string]string{}
	title = strings.TrimSpace(title)
	if n := utf8.RuneCountInString(title); n < 1 || n > 200 {
		errs["title"] = "must be 1 to 200 characters"
	}
	fileName = path.Base(strings.ReplaceAll(strings.TrimSpace(fileName), `\`, "/"))
	if fileName == "." || fileName == "/" || utf8.RuneCountInString(fileName) > 200 {
		errs["fileName"] = "must be a file name of at most 200 characters"
	}
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	if !documentTypes[contentType] {
		errs["contentType"] = "is not an accepted document type"
	}
	switch {
	case len(content) == 0:
		errs["content"] = "is required"
	case len(content) > MaxDocumentBytes:
		errs["content"] = fmt.Sprintf("must be at most %d bytes", MaxDocumentBytes)
	}
	sum := sha256.Sum256(content)
	return Document{Title: title, FileName: fileName, ContentType: contentType, Content: content, SHA256: hex.EncodeToString(sum[:])}, errs
}

// CheckDocNo validates a controlled document number.
func CheckDocNo(docNo string) bool { return docNoPattern.MatchString(docNo) }
