// Package transport delivers rendered documents to printers. Raw TCP (port 9100)
// carries ZPL II and PCL 6 byte streams; IPP carries PDF documents. Router picks the
// transport by device type. The print service runs on the site network and is the
// only component that opens printer connections.
package transport

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
)

// TCP sends ZPL II or PCL 6 over a raw socket.
type TCP struct {
	Timeout time.Duration // dial and write deadline; zero means 2 s
}

// Send writes the payload to the printer. Success means the printer's socket accepted
// the bytes; it is not a print acknowledgement.
func (t TCP) Send(ctx context.Context, p domain.Printer, payload string) error {
	if p.DeviceType != domain.DeviceZPL && p.DeviceType != domain.DevicePCL6 {
		return fmt.Errorf("device type %s is not delivered by raw TCP", p.DeviceType)
	}
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(p.Host, strconv.Itoa(p.Port)))
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	_, err = io.WriteString(conn, payload)
	return err
}
