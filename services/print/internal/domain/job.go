package domain

import (
	"errors"
	"time"
)

// Job states. SENT (written, not acknowledged) is not used in this slice.
const (
	JobQueued  = "QUEUED"
	JobPrinted = "PRINTED"
	JobFailed  = "FAILED"
)

// Payload formats. ZPL jobs are rendered from the built-in label templates; PDF
// jobs carry a document produced elsewhere (pick list, count sheet, CoA, bill of
// lading) and print on PDF over IPP devices only (FR-32.13).
const (
	FormatZPL = "ZPL"
	FormatPDF = "PDF"

	// MaxDocumentBytes caps a PDF document in a job request. The request body is
	// limited to 4 MiB (httpx.Decode) and base64 grows the document by a third, so
	// 2 MiB leaves room for the encoding and the other fields.
	MaxDocumentBytes = 2 << 20
	// PDFDocumentTemplate is the template code recorded on PDF jobs, which have no
	// label template.
	PDFDocumentTemplate = "PDF_DOCUMENT"
)

// DeviceTypeFor returns the device type that prints a payload format: PDF_IPP for
// PDF, ZPL_II for everything else (every built-in template is ZPL).
func DeviceTypeFor(format string) string {
	if format == FormatPDF {
		return DevicePDFIPP
	}
	return DeviceZPL
}

// Job is one print request. ZPL is the payload: the rendered ZPL, or the PDF bytes
// when Format is PDF. It is stored but never returned to clients.
type Job struct {
	ID           string    `json:"id"`
	TemplateCode string    `json:"templateCode"`
	QueueCode    string    `json:"queueCode"`
	Format       string    `json:"format"`
	PrinterID    string    `json:"printerId,omitempty"`
	PrinterCode  string    `json:"printerCode,omitempty"`
	Status       string    `json:"status"`
	Attempts     int       `json:"attempts"`
	LastError    string    `json:"lastError,omitempty"`
	ReprintOf    *string   `json:"reprintOf,omitempty"`
	CreatedBy    string    `json:"createdBy"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
	ZPL          string    `json:"-"`
}

// ErrTerminal is returned when a job that is already PRINTED or FAILED is completed again.
var ErrTerminal = &Error{CodeTerminal, "print job is already completed"}

// Complete records the dispatch outcome: PRINTED on success, FAILED with the
// rule code otherwise. A job completes once.
func (j *Job) Complete(out Outcome, dispatchErr error) error {
	if j.Status != JobQueued {
		return ErrTerminal
	}
	j.Attempts = out.Attempts
	j.PrinterID = out.Printer.ID
	j.PrinterCode = out.Printer.Code
	j.Status = JobPrinted
	j.LastError = ""
	if dispatchErr != nil {
		j.Status = JobFailed
		j.LastError = ErrorCode(dispatchErr)
	}
	return nil
}

// Terminal reports whether the job has reached PRINTED or FAILED.
func (j Job) Terminal() bool { return j.Status == JobPrinted || j.Status == JobFailed }

// ErrorCode returns the rule code of err, or PRN-0000 for other errors.
func ErrorCode(err error) string {
	var de *Error
	if errors.As(err, &de) {
		return de.Code
	}
	return "PRN-0000"
}
