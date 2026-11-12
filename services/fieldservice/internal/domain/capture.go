package domain

import (
	"fmt"
	"strings"
)

// Capture limits (PRD 8.45.6): a photo action carries at most five photos, each no more
// than 1,600 px on its long edge; a stored capture is at most MaxCaptureBytes.
const (
	MaxPhotosPerAction = 5
	MaxPhotoEdge       = 1600
	MaxSignatureEdge   = 2000
	MaxCaptureBytes    = 2 << 20
	MaxChecklistItems  = 40
)

// Codes of the capture endpoints.
const (
	// CodeCaptureInvalid refuses an upload that is not a PNG or JPEG of the allowed size.
	CodeCaptureInvalid = "SVC-CAP-001"
	// CodeCaptureUnknown rejects an action that names a capture the work order does not hold.
	CodeCaptureUnknown = "SVC-CAP-002"
	// CodeCaptureState refuses an upload for a work order that is not being worked on.
	CodeCaptureState = "SVC-CAP-003"
	// CodeCaptureGone answers a read of an image the retention job has deleted (410).
	CodeCaptureGone = "SVC-CAP-004"
	// CodeCaptureStoreDown is the 503 when the object store did not take or give the image; the upload can be repeated.
	CodeCaptureStoreDown = "SVC-CAP-005"
)

// CaptureRefPrefix starts the reference a capture is known by in the action log and on
// the work order (signatureRef); other references point into the object store.
const CaptureRefPrefix = "capture:"

// CaptureRef is the reference of the capture with the given id.
func CaptureRef(id string) string { return CaptureRefPrefix + strings.ToLower(id) }

// CaptureIDOf returns the capture id inside ref, if ref is a capture reference.
func CaptureIDOf(ref string) (string, bool) {
	id, ok := strings.CutPrefix(ref, CaptureRefPrefix)
	return id, ok
}

// CaptureKind is what a capture holds.
type CaptureKind string

const (
	CapturePhoto     CaptureKind = "PHOTO"
	CaptureSignature CaptureKind = "SIGNATURE"
)

// CheckImage validates the type and pixel size of an uploaded capture.
func CheckImage(kind CaptureKind, contentType string, width, height, size int) error {
	edge := MaxPhotoEdge
	switch kind {
	case CapturePhoto:
		if contentType != "image/png" && contentType != "image/jpeg" {
			return fmt.Errorf("a photo must be image/jpeg or image/png, not %s", contentType)
		}
	case CaptureSignature:
		if contentType != "image/png" {
			return fmt.Errorf("a signature must be image/png, not %s", contentType)
		}
		edge = MaxSignatureEdge
	default:
		return fmt.Errorf("kind must be PHOTO or SIGNATURE")
	}
	if size < 1 || size > MaxCaptureBytes {
		return fmt.Errorf("the image must be 1 byte to %d MB", MaxCaptureBytes>>20)
	}
	if width < 1 || height < 1 || width > edge || height > edge {
		return fmt.Errorf("the image is %d x %d px; its long edge must be at most %d px", width, height, edge)
	}
	return nil
}

// ChecklistItem is one step the technician ticks off on the visit (FR-45.8). A required
// item must be done before the work order can be completed (SVC-WO-003).
type ChecklistItem struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	Required bool   `json:"required"`
}

// ChecklistOpen returns the required items that are not done, in list order.
func ChecklistOpen(items []ChecklistItem, done map[string]bool) []ChecklistItem {
	var out []ChecklistItem
	for _, it := range items {
		if it.Required && !done[it.ID] {
			out = append(out, it)
		}
	}
	return out
}
