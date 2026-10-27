package transport

import (
	"context"
	"errors"
	"strings"

	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
)

// ErrWrongDocumentFormat is returned when a payload cannot print on the device
// type. A ZPL label sent to a PDF device fails here, before any network call.
var ErrWrongDocumentFormat = errors.New("payload format does not match device type")

// pdfHeader starts every PDF file (ISO 32000-1 §7.5.2).
const pdfHeader = "%PDF-"

// Router sends a payload to a printer through the transport for its device type:
// ZPL II and PCL 6 over raw TCP, PDF over IPP. It satisfies the API's Sender and is
// the sender cmd/print wires in.
type Router struct {
	TCP TCP
	IPP IPP
}

// Send routes payload by p.DeviceType. A PDF device only accepts a payload that
// starts with the PDF header, and a ZPL device refuses one, so a document is never
// sent to a device that would print it as garbage.
func (r Router) Send(ctx context.Context, p domain.Printer, payload string) error {
	switch p.DeviceType {
	case domain.DeviceZPL, domain.DevicePCL6:
		if strings.HasPrefix(payload, pdfHeader) {
			return ErrWrongDocumentFormat
		}
		return r.TCP.Send(ctx, p, payload)
	case domain.DevicePDFIPP:
		if !strings.HasPrefix(payload, pdfHeader) {
			return ErrWrongDocumentFormat
		}
		return r.IPP.Send(ctx, p, payload)
	default:
		return errors.New("unknown device type " + p.DeviceType)
	}
}
