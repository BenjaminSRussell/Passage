package domain

import (
	"regexp"
	"time"
)

// Device types (FR-32.13). ZPL_II and PCL_6 are sent over raw TCP, PDF_IPP over IPP
// (internal/transport). Job dispatch offers a ZPL label to ZPL_II devices and a PDF
// document to PDF_IPP devices only (see Accepting and DeviceTypeFor). No job carries a
// PCL payload yet.
const (
	DeviceZPL    = "ZPL_II"
	DevicePDFIPP = "PDF_IPP"
	DevicePCL6   = "PCL_6"

	DefaultRawPort   = 9100 // raw TCP (JetDirect) for ZPL II and PCL 6
	DefaultIPPPort   = 631  // IPP (RFC 8011)
	FailoverAttempts = 2    // attempts per device before failover (FR-32.13)
)

// DefaultPort is the port a registration without one gets: 631 for IPP, 9100 otherwise.
func DefaultPort(deviceType string) int {
	if deviceType == DevicePDFIPP {
		return DefaultIPPPort
	}
	return DefaultRawPort
}

// Printer is an output device registered for a site queue.
type Printer struct {
	ID           string    `json:"id"`
	Code         string    `json:"code"`
	Name         string    `json:"name"`
	DeviceType   string    `json:"deviceType"`
	Dpi          int       `json:"dpi"`
	MediaSize    string    `json:"mediaSize"`
	Host         string    `json:"host"`
	Port         int       `json:"port"`
	QueueCode    string    `json:"queueCode"`
	FailoverRank int       `json:"failoverRank"`
	CreatedAt    time.Time `json:"createdAt"`
	// RetiredAt is set once the printer has left its queue (FR-32.13). A retired printer
	// is no longer offered jobs and is listed only on request.
	RetiredAt *time.Time `json:"retiredAt,omitempty"`
}

const codePattern = `^[A-Z0-9][A-Z0-9_-]{1,39}$`

// ValidCode reports whether s is a printer or queue code.
func ValidCode(s string) bool {
	ok, _ := regexp.MatchString(codePattern, s)
	return ok
}

// Validate returns field-level errors for a printer registration, or none.
func (p Printer) Validate() map[string]string {
	fields := map[string]string{}
	if !ValidCode(p.Code) {
		fields["code"] = "must be 2 to 40 characters of A-Z, 0-9, _ or -, starting with A-Z or 0-9"
	}
	if p.Name == "" || len([]rune(p.Name)) > 120 {
		fields["name"] = "must be 1 to 120 characters"
	}
	if p.DeviceType != DeviceZPL && p.DeviceType != DevicePDFIPP && p.DeviceType != DevicePCL6 {
		fields["deviceType"] = "must be ZPL_II, PDF_IPP or PCL_6"
	}
	if p.Dpi != 203 && p.Dpi != 300 && p.Dpi != 600 {
		fields["dpi"] = "must be 203, 300 or 600"
	}
	if !MediaForDevice(p.DeviceType, p.MediaSize) {
		fields["mediaSize"] = "must be a label size (2X1_IN, 3X1_IN, 4X2_IN, 4X6_IN) for ZPL_II, or a sheet size (A4, LETTER) for PDF_IPP and PCL_6"
	}
	if p.Host == "" || len(p.Host) > 253 {
		fields["host"] = "must be 1 to 253 characters"
	}
	if p.Port < 1 || p.Port > 65535 {
		fields["port"] = "must be 1 to 65535"
	}
	if !ValidCode(p.QueueCode) {
		fields["queueCode"] = "must be a valid queue code"
	}
	if p.FailoverRank < 1 {
		fields["failoverRank"] = "must be at least 1"
	}
	return fields
}

// Accepting returns the printers of queue that can print deviceType, keeping
// their failover order. A ZPL label never goes to a PDF or PCL device.
func Accepting(queue []Printer, deviceType string) []Printer {
	out := make([]Printer, 0, len(queue))
	for _, p := range queue {
		if p.DeviceType == deviceType {
			out = append(out, p)
		}
	}
	return out
}
