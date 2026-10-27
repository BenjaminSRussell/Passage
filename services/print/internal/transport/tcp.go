// Package transport delivers rendered documents to printers. Only raw TCP
// (port 9100) ZPL delivery is built in this slice; the print service runs on
// the site network and is the only component that opens printer connections.
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

// TCP sends ZPL over a raw socket.
type TCP struct {
	Timeout time.Duration // dial and write deadline; zero means 2 s
}

// Send writes zpl to the printer. Success means the printer's socket accepted
// the bytes; it is not a print acknowledgement.
func (t TCP) Send(ctx context.Context, p domain.Printer, zpl string) error {
	if p.DeviceType != domain.DeviceZPL {
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
	_, err = io.WriteString(conn, zpl)
	return err
}
