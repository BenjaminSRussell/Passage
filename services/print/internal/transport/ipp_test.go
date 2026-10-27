package transport_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
	"github.com/BenjaminSRussell/passage/services/print/internal/transport"
)

// decodeIPP reads an RFC 8010 request independently of the encoder under test.
func decodeIPP(t *testing.T, body []byte) (map[string]string, []byte) {
	t.Helper()
	if len(body) < 9 {
		t.Fatalf("request too short: %d bytes", len(body))
	}
	if body[0] != 0x01 || body[1] != 0x01 {
		t.Fatalf("version = %x.%x, want 1.1", body[0], body[1])
	}
	if op := binary.BigEndian.Uint16(body[2:4]); op != 0x0002 {
		t.Fatalf("operation = %#x, want Print-Job", op)
	}
	if body[8] != 0x01 {
		t.Fatalf("missing operation-attributes-tag, got %#x", body[8])
	}
	attrs := map[string]string{}
	pos := 9
	for pos < len(body) && body[pos] != 0x03 {
		pos++ // value tag
		nl := int(binary.BigEndian.Uint16(body[pos:]))
		pos += 2
		name := string(body[pos : pos+nl])
		pos += nl
		vl := int(binary.BigEndian.Uint16(body[pos:]))
		pos += 2
		attrs[name] = string(body[pos : pos+vl])
		pos += vl
	}
	if pos >= len(body) {
		t.Fatal("missing end-of-attributes-tag")
	}
	return attrs, body[pos+1:]
}

func TestEncodePrintJob_MatchesRFC8010Layout(t *testing.T) {
	doc := []byte("%PDF-1.7 test")
	body := transport.EncodePrintJob("ipp://10.0.0.5:631/ipp/print", "passage-print", "application/pdf", 7, doc)
	if got := binary.BigEndian.Uint32(body[4:8]); got != 7 {
		t.Fatalf("request-id = %d, want 7", got)
	}
	attrs, tail := decodeIPP(t, body)
	want := map[string]string{
		"attributes-charset":          "utf-8",
		"attributes-natural-language": "en",
		"printer-uri":                 "ipp://10.0.0.5:631/ipp/print",
		"requesting-user-name":        "passage-print",
		"document-format":             "application/pdf",
	}
	for k, v := range want {
		if attrs[k] != v {
			t.Errorf("attribute %s = %q, want %q", k, attrs[k], v)
		}
	}
	if !bytes.Equal(tail, doc) {
		t.Errorf("document tail = %q, want %q", tail, doc)
	}
}

func portOf(t *testing.T, raw string) int {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func pdfDevice(port int) domain.Printer {
	return domain.Printer{Code: "PDF-1", DeviceType: domain.DevicePDFIPP, Dpi: 300, Host: "127.0.0.1", Port: port, QueueCode: "Q-1", FailoverRank: 1}
}

func TestIPP_SendPostsPrintJobAndAcceptsOK(t *testing.T) {
	var gotType, gotPath string
	var gotAttrs map[string]string
	var gotDoc []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotType, gotPath = r.Header.Get("Content-Type"), r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotAttrs, gotDoc = decodeIPP(t, body)
		_, _ = w.Write([]byte{0x01, 0x01, 0x00, 0x00, 0, 0, 0, 1, 0x03})
	}))
	defer srv.Close()

	doc := "%PDF-1.4 pick list"
	err := transport.IPP{}.Send(context.Background(), pdfDevice(portOf(t, srv.URL)), doc)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotType != "application/ipp" || gotPath != "/ipp/print" {
		t.Fatalf("request = %s %s, want application/ipp /ipp/print", gotType, gotPath)
	}
	if gotAttrs["document-format"] != "application/pdf" {
		t.Errorf("document-format = %q", gotAttrs["document-format"])
	}
	if string(gotDoc) != doc {
		t.Errorf("document = %q, want %q", gotDoc, doc)
	}
}

func TestIPP_SendReportsClientErrorStatus(t *testing.T) {
	// 0x0406 client-error-document-format-not-supported.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte{0x01, 0x01, 0x04, 0x06, 0, 0, 0, 1, 0x03})
	}))
	defer srv.Close()

	err := transport.IPP{}.Send(context.Background(), pdfDevice(portOf(t, srv.URL)), "%PDF-1.4")
	if err == nil {
		t.Fatal("expected an error for a client-error status")
	}
}

func TestIPP_RejectsNonPDFDeviceType(t *testing.T) {
	p := domain.Printer{DeviceType: domain.DeviceZPL, Host: "127.0.0.1", Port: 631}
	if err := (transport.IPP{}).Send(context.Background(), p, "%PDF-1.4"); err == nil {
		t.Fatal("expected refusal for a ZPL device")
	}
}

func TestCheckResponse_StatusRange(t *testing.T) {
	ok := []byte{0x01, 0x01, 0x00, 0x01, 0, 0, 0, 1, 0x03}
	if err := transport.CheckResponse(ok); err != nil {
		t.Errorf("successful status 0x0001 rejected: %v", err)
	}
	if err := transport.CheckResponse([]byte{0x01, 0x01, 0x05, 0x00, 0, 0, 0, 1}); err == nil {
		t.Error("server-error status accepted")
	}
	if err := transport.CheckResponse([]byte{0x01}); err == nil {
		t.Error("short response accepted")
	}
}

func TestRouter_RefusesZPLPayloadOnPDFDevice(t *testing.T) {
	err := transport.Router{}.Send(context.Background(), pdfDevice(9), "^XA^FDLP^FS^XZ")
	if !errors.Is(err, transport.ErrWrongDocumentFormat) {
		t.Fatalf("err = %v, want ErrWrongDocumentFormat", err)
	}
}

func TestRouter_SendsPCLOverRawTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan []byte, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			got <- nil
			return
		}
		defer conn.Close()
		data, _ := io.ReadAll(conn)
		got <- data
	}()
	addr := ln.Addr().(*net.TCPAddr)
	p := domain.Printer{Code: "PCL-1", DeviceType: domain.DevicePCL6, Dpi: 600, Host: "127.0.0.1", Port: addr.Port, QueueCode: "Q-1", FailoverRank: 1}
	payload := "\x1bE\x1b&l1O"
	if err := (transport.Router{}).Send(context.Background(), p, payload); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if data := <-got; string(data) != payload {
		t.Fatalf("printer received %q, want %q", data, payload)
	}
}

func TestRouter_RefusesPDFPayloadOnLabelDevice(t *testing.T) {
	p := domain.Printer{Code: "ZPL-1", DeviceType: domain.DeviceZPL, Host: "127.0.0.1", Port: 9}
	if err := (transport.Router{}).Send(context.Background(), p, "%PDF-1.7"); !errors.Is(err, transport.ErrWrongDocumentFormat) {
		t.Fatalf("err = %v, want ErrWrongDocumentFormat", err)
	}
}

func TestRouter_SendsPDFOverIPP(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte{0x01, 0x01, 0x00, 0x00, 0, 0, 0, 1, 0x03})
	}))
	defer srv.Close()
	if err := (transport.Router{}).Send(context.Background(), pdfDevice(portOf(t, srv.URL)), "%PDF-1.7 bol"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if hits != 1 {
		t.Fatalf("IPP server hit %d times, want 1", hits)
	}
}

func TestIPP_SendReportsHTTPFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "busy", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	if err := (transport.IPP{}).Send(context.Background(), pdfDevice(portOf(t, srv.URL)), "%PDF-1.4"); err == nil {
		t.Fatal("HTTP 503 from the printer was accepted")
	}
}

func TestRouter_RefusesUnknownDeviceType(t *testing.T) {
	p := domain.Printer{DeviceType: "POSTSCRIPT", Host: "127.0.0.1", Port: 9}
	if err := (transport.Router{}).Send(context.Background(), p, "x"); err == nil {
		t.Fatal("unknown device type accepted")
	}
}
