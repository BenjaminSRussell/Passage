package domain

import (
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Voucher attachments and notes on journal batches (FR-11.11, FR-11.18). The ledger keeps the
// metadata and the store reference (an object-storage key or an external DMS URI); the file
// bytes live in the wave 5 document store. Attachments become immutable when the batch is posted
// and are retained for their retention class counted from the posting date.
//
// The store reference, content type and size are asserted by the caller. The ledger never
// dereferences the reference; it only checks its shape so that a later reader (the document
// service, an export) cannot be pointed at another tenant's object or an arbitrary host: an
// object key must sit under the caller's tenant prefix ({tenant-id}/..., architecture §object
// storage) and a URI must be dms://{connector}/{document}, resolved by the tenant's configured
// DMS connector. Plain http(s) URLs are refused. The document service that issues the reference
// must verify the content type and size of the bytes it stores.
const (
	// CodeAttachmentInvalid is the 422 for a malformed attachment or note.
	CodeAttachmentInvalid = "LEDGER_ATTACHMENT_INVALID"
	// CodeAttachmentRetained is the 409 for deleting an attachment before its retention class expires.
	CodeAttachmentRetained = "LEDGER_ATTACHMENT_RETAINED"
	// CodeAttachmentNotFound is the 404 for an unknown attachment or note.
	CodeAttachmentNotFound = "LEDGER_ATTACHMENT_NOT_FOUND"
	// CodeAttachmentLimit is the 409 for the 51st attachment of a batch.
	CodeAttachmentLimit = "LEDGER_ATTACHMENT_LIMIT"
	// CodeAttachmentConflict is the 409 for re-using an attachment id with different content.
	CodeAttachmentConflict = "LEDGER_ATTACHMENT_CONFLICT"

	// KindAttachment is a file reference; KindNote is free text.
	KindAttachment = "attachment"
	KindNote       = "note"

	// MaxAttachmentBytes is the largest file (25 MB, FR-11.11).
	MaxAttachmentBytes = 25 * 1024 * 1024
	// MaxAttachmentsPerBatch counts files; notes are limited separately.
	MaxAttachmentsPerBatch = 50
	// MaxNotesPerBatch bounds the notes of one batch.
	MaxNotesPerBatch = 200
	// MaxNoteLength is the longest note in characters.
	MaxNoteLength = 2000

	// DefaultRetentionClass applies when a request names none (PRD attachment.default_retention_class).
	DefaultRetentionClass = "financial_record_10y"
	// DefaultDocumentType applies when a request names none.
	DefaultDocumentType = "other"
)

// DocumentTypes are the shipped wave 5 document types (FR-11.18).
var DocumentTypes = []string{"invoice_image", "approval_evidence", "supporting_calculation", "correspondence", "other"}

// RetentionClasses are the retention class codes, mapped to the statutory minimum in years
// (PRD retention: DE 10, US 7, UK 6, FR 10).
var RetentionClasses = []string{"financial_record_10y", "financial_record_7y", "financial_record_6y", "working_paper_2y"}

var retentionYears = map[string]int{
	"financial_record_10y": 10,
	"financial_record_7y":  7,
	"financial_record_6y":  6,
	"working_paper_2y":     2,
}

// ContentTypes is the allow-list of file types (virus scanning is inherited from wave 5).
var ContentTypes = []string{"application/pdf", "image/png", "image/jpeg", "image/tiff", "text/plain", "text/csv", "application/xml",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	"message/rfc822"}

var (
	attachmentNameRe = regexp.MustCompile(`^[^\x00-\x1f/\\]{1,200}$`)
	// storeKeySegmentRe is one segment of an object key (no empty, "." or ".." segments are allowed).
	storeKeySegmentRe = regexp.MustCompile(`^[A-Za-z0-9._=+-]{1,255}$`)
	// dmsURIRe is dms://{connector}/{document path}: no user info, port, query or fragment.
	dmsURIRe = regexp.MustCompile(`^dms://[a-z0-9][a-z0-9-]{0,62}/[A-Za-z0-9._~=+-]+(/[A-Za-z0-9._~=+-]+)*$`)
)

// MaxStoreRefLength bounds a store reference.
const MaxStoreRefLength = 1024

// RetainedUntil is the first day an attachment of the class may be deleted when its batch was
// posted on postedOn: the class's years later.
func RetainedUntil(postedOn time.Time, class string) time.Time {
	y := retentionYears[class]
	return time.Date(postedOn.Year()+y, postedOn.Month(), postedOn.Day(), 0, 0, 0, 0, time.UTC)
}

// Attachment is a file reference or a note on a journal batch. For a posted batch RetainedUntil
// is set from the posting date.
type Attachment struct {
	ID             string    `json:"id"`
	BatchID        string    `json:"batchId"`
	Kind           string    `json:"kind"`
	Name           string    `json:"name,omitempty"`
	DocumentType   string    `json:"documentType,omitempty"`
	RetentionClass string    `json:"retentionClass,omitempty"`
	RetainedUntil  *string   `json:"retainedUntil,omitempty"`
	StoreRef       string    `json:"storeRef,omitempty"`
	ContentType    string    `json:"contentType,omitempty"`
	SizeBytes      int64     `json:"sizeBytes,omitempty"`
	Note           string    `json:"note,omitempty"`
	CreatedBy      string    `json:"createdBy"`
	CreatedAt      time.Time `json:"createdAt"`
}

// Normalize trims text and fills the defaults of an attachment.
func (a *Attachment) Normalize() {
	a.Name = strings.TrimSpace(a.Name)
	a.Note = strings.TrimSpace(a.Note)
	a.StoreRef = strings.TrimSpace(a.StoreRef)
	if a.Kind == KindAttachment {
		if a.DocumentType == "" {
			a.DocumentType = DefaultDocumentType
		}
		if a.RetentionClass == "" {
			a.RetentionClass = DefaultRetentionClass
		}
	}
}

// ValidateAttachment checks an attachment or note of tenantID and returns every violation.
func ValidateAttachment(a Attachment, tenantID string) []Violation {
	var out []Violation
	bad := func(field, msg string) {
		out = append(out, Violation{Code: CodeAttachmentInvalid, Field: field, Message: msg})
	}
	switch a.Kind {
	case KindNote:
		if a.Note == "" || utf8.RuneCountInString(a.Note) > MaxNoteLength {
			bad("note", "a note has 1 to 2000 characters")
		}
		if a.Name != "" || a.StoreRef != "" || a.ContentType != "" || a.SizeBytes != 0 || a.DocumentType != "" || a.RetentionClass != "" {
			bad("kind", "a note carries only its text")
		}
	case KindAttachment:
		if !attachmentNameRe.MatchString(a.Name) {
			bad("name", "the file name has 1 to 200 characters and no path separators or control characters")
		}
		if !slices.Contains(DocumentTypes, a.DocumentType) {
			bad("documentType", "must be one of "+strings.Join(DocumentTypes, ", "))
		}
		if _, ok := retentionYears[a.RetentionClass]; !ok {
			bad("retentionClass", "must be one of "+strings.Join(RetentionClasses, ", "))
		}
		if !ValidStoreRef(a.StoreRef, tenantID) {
			bad("storeRef", "must be an object-storage key under the tenant prefix "+tenantID+"/ or a dms://{connector}/{document} URI")
		}
		if !slices.Contains(ContentTypes, a.ContentType) {
			bad("contentType", "is not an allowed content type")
		}
		if a.SizeBytes < 1 || a.SizeBytes > MaxAttachmentBytes {
			bad("sizeBytes", "a file is 1 byte to 25 MB")
		}
		if a.Note != "" {
			bad("note", "a file carries no note text; add a note instead")
		}
	default:
		bad("kind", "must be attachment or note")
	}
	return out
}

// ValidStoreRef reports whether ref is an object key under tenantID's prefix ({tenant-id}/...)
// or a dms://{connector}/{document} URI. Any other scheme (https, file, s3 with a bucket name) is
// refused, as are empty, "." and ".." key segments.
func ValidStoreRef(ref, tenantID string) bool {
	if ref == "" || len(ref) > MaxStoreRefLength {
		return false
	}
	if strings.HasPrefix(ref, "dms://") {
		return dmsURIRe.MatchString(ref) && !hasDotSegment(strings.Split(strings.TrimPrefix(ref, "dms://"), "/"))
	}
	if tenantID == "" || !strings.HasPrefix(ref, tenantID+"/") {
		return false
	}
	segs := strings.Split(strings.TrimPrefix(ref, tenantID+"/"), "/")
	for _, seg := range segs {
		if !storeKeySegmentRe.MatchString(seg) {
			return false
		}
	}
	return !hasDotSegment(segs)
}

func hasDotSegment(segs []string) bool {
	for _, seg := range segs {
		if seg == "." || seg == ".." {
			return true
		}
	}
	return false
}

// SameAttachment reports whether a replay of a PUT carries the stored content.
func SameAttachment(cur, in Attachment) bool {
	return cur.Kind == in.Kind && cur.Name == in.Name && cur.DocumentType == in.DocumentType && cur.RetentionClass == in.RetentionClass &&
		cur.StoreRef == in.StoreRef && cur.ContentType == in.ContentType && cur.SizeBytes == in.SizeBytes && cur.Note == in.Note
}
