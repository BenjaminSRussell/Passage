package transport_test

import (
	"context"
	"io"
	"net"
	"strconv"
	"testing"

	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
	"github.com/BenjaminSRussell/passage/services/print/internal/transport"
)

func TestTCP_SendDeliversZPLBytes(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listener unavailable: %v", err)
	}
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			got <- ""
			return
		}
		defer conn.Close()
		b, _ := io.ReadAll(conn)
		got <- string(b)
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	p := domain.Printer{Code: "DOCK-1", DeviceType: domain.DeviceZPL, Host: "127.0.0.1", Port: port}
	if err := (transport.TCP{}).Send(context.Background(), p, "^XA^XZ"); err != nil {
		t.Fatal(err)
	}
	conn := <-got
	if conn != "^XA^XZ" {
		t.Fatalf("printer received %q", conn)
	}
}

func TestTCP_RefusesUnsupportedDeviceType(t *testing.T) {
	p := domain.Printer{DeviceType: domain.DevicePDFIPP, Host: "127.0.0.1", Port: 9100}
	if err := (transport.TCP{}).Send(context.Background(), p, "x"); err == nil {
		t.Fatal("PDF_IPP sent over raw TCP")
	}
}

func TestTCP_UnreachableReturnsError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listener unavailable: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	p := domain.Printer{DeviceType: domain.DeviceZPL, Host: "127.0.0.1", Port: port}
	if err := (transport.TCP{}).Send(context.Background(), p, "x"); err == nil {
		t.Fatalf("closed port %s accepted a job", strconv.Itoa(port))
	}
}
