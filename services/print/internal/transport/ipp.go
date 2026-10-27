package transport

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
)

// IPP operation, delimiter and value tags (RFC 8011 §5.4.15, RFC 8010 §3.5).
const (
	opPrintJob         = 0x0002
	tagOperationAttrs  = 0x01
	tagEndOfAttributes = 0x03
	tagURI             = 0x45
	tagNameNoLang      = 0x42
	tagCharset         = 0x47
	tagNaturalLanguage = 0x48
	tagMimeMediaType   = 0x49

	ippContentType       = "application/ipp"
	ippDocumentFormatPDF = "application/pdf"
	ippRequestingUser    = "passage-print"
	ippResource          = "/ipp/print" // IPP Everywhere print resource
)

// IPP sends PDF documents to an IPP printer (RFC 8011 Print-Job over HTTP).
// Success means the printer returned a successful-ok status; the job may still
// fail on the device later, which the job log cannot see.
type IPP struct {
	Timeout time.Duration // request deadline; zero means 2 s
}

// Send posts a Print-Job for the PDF document to the printer's /ipp/print resource
// over plain HTTP on the site network (IPP over TLS is not configured on site printers).
func (i IPP) Send(ctx context.Context, p domain.Printer, document string) error {
	if p.DeviceType != domain.DevicePDFIPP {
		return fmt.Errorf("device type %s is not delivered by IPP", p.DeviceType)
	}
	timeout := i.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	endpoint := net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
	body := EncodePrintJob("ipp://"+endpoint+ippResource, ippRequestingUser, ippDocumentFormatPDF, 1, []byte(document))

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+endpoint+ippResource, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", ippContentType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	reply, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ipp http status %d", resp.StatusCode)
	}
	return CheckResponse(reply)
}

// EncodePrintJob builds an RFC 8010 request: version 1.1, Print-Job, the
// mandatory operation attributes, end-of-attributes, then the document bytes.
func EncodePrintJob(printerURI, user, format string, requestID int32, document []byte) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0x01, 0x01})
	_ = binary.Write(&buf, binary.BigEndian, uint16(opPrintJob))
	_ = binary.Write(&buf, binary.BigEndian, requestID)
	buf.WriteByte(tagOperationAttrs)
	writeAttr(&buf, tagCharset, "attributes-charset", "utf-8")
	writeAttr(&buf, tagNaturalLanguage, "attributes-natural-language", "en")
	writeAttr(&buf, tagURI, "printer-uri", printerURI)
	writeAttr(&buf, tagNameNoLang, "requesting-user-name", user)
	writeAttr(&buf, tagMimeMediaType, "document-format", format)
	buf.WriteByte(tagEndOfAttributes)
	buf.Write(document)
	return buf.Bytes()
}

func writeAttr(buf *bytes.Buffer, tag byte, name, value string) {
	buf.WriteByte(tag)
	_ = binary.Write(buf, binary.BigEndian, uint16(len(name)))
	buf.WriteString(name)
	_ = binary.Write(buf, binary.BigEndian, uint16(len(value)))
	buf.WriteString(value)
}

// CheckResponse reads the status-code of an IPP response. Codes 0x0000 to
// 0x00FF are successful; anything else is an error with the hex code.
func CheckResponse(reply []byte) error {
	if len(reply) < 8 {
		return errors.New("ipp response too short")
	}
	status := binary.BigEndian.Uint16(reply[2:4])
	if status > 0x00ff {
		return fmt.Errorf("ipp status 0x%04x", status)
	}
	return nil
}
