package mqtt

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// Message is one publish received from the broker.
type Message struct {
	Topic    string
	Payload  []byte
	QoS      byte
	Retained bool
}

// ErrClosed is returned by calls on a connection that has ended.
var ErrClosed = errors.New("mqtt: connection closed")

// Options configures Dial.
type Options struct {
	Addr      string      // host:port
	TLS       *tls.Config // nil dials plain TCP (dev and the demo plant)
	ClientID  string
	Username  string
	Password  string
	KeepAlive time.Duration // default 30 s
	// OnMessage receives every publish for a subscription, in order, on the connection's reader.
	OnMessage func(Message)
	// Timeout bounds the dial, the CONNACK and each QoS 1 acknowledgement; default 10 s.
	Timeout time.Duration
}

// Conn is one connection to a broker.
type Conn struct {
	o      Options
	c      net.Conn
	wmu    sync.Mutex
	mu     sync.Mutex
	nextID uint16
	acks   map[uint16]chan []byte // packet id to the SUBACK/PUBACK body
	done   chan struct{}
	err    error
}

// Dial connects and waits for the CONNACK.
func Dial(ctx context.Context, o Options) (*Conn, error) {
	if o.KeepAlive <= 0 {
		o.KeepAlive = 30 * time.Second
	}
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Second
	}
	d := net.Dialer{Timeout: o.Timeout}
	var nc net.Conn
	var err error
	if o.TLS != nil {
		nc, err = (&tls.Dialer{NetDialer: &d, Config: o.TLS}).DialContext(ctx, "tcp", o.Addr)
	} else {
		nc, err = d.DialContext(ctx, "tcp", o.Addr)
	}
	if err != nil {
		return nil, fmt.Errorf("mqtt dial %s: %w", o.Addr, err)
	}
	c := &Conn{o: o, c: nc, acks: map[uint16]chan []byte{}, done: make(chan struct{})}
	body := putString(nil, "MQTT")
	flags := byte(0x02) // clean session
	body = append(body, 4)
	if o.Username != "" {
		flags |= 0x80
		if o.Password != "" {
			flags |= 0x40
		}
	}
	body = append(body, flags)
	body = putUint16(body, uint16(o.KeepAlive/time.Second))
	body = putString(body, o.ClientID)
	if o.Username != "" {
		body = putString(body, o.Username)
		if o.Password != "" {
			body = putString(body, o.Password)
		}
	}
	_ = nc.SetDeadline(time.Now().Add(o.Timeout))
	if _, err := nc.Write(packet{kind: typeConnect, body: body}.bytes()); err != nil {
		nc.Close()
		return nil, fmt.Errorf("mqtt connect: %w", err)
	}
	p, err := readPacket(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("mqtt connect: %w", err)
	}
	if p.kind != typeConnack || len(p.body) != 2 {
		nc.Close()
		return nil, fmt.Errorf("mqtt connect: %w", errMalformed)
	}
	if p.body[1] != 0 {
		nc.Close()
		return nil, fmt.Errorf("mqtt connect refused by the broker (code %d)", p.body[1])
	}
	_ = nc.SetDeadline(time.Time{})
	go c.read()
	go c.keepAlive()
	return c, nil
}

func (c *Conn) fail(err error) {
	c.mu.Lock()
	if c.err == nil {
		c.err = err
		close(c.done)
		c.c.Close()
	}
	c.mu.Unlock()
}

// Done is closed when the connection has ended; Err says why.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Err is the reason the connection ended, nil while it is open.
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *Conn) write(p packet) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.c.SetWriteDeadline(time.Now().Add(c.o.Timeout))
	if _, err := c.c.Write(p.bytes()); err != nil {
		c.fail(err)
		return err
	}
	return nil
}

func (c *Conn) read() {
	for {
		p, err := readPacket(c.c)
		if err != nil {
			c.fail(err)
			return
		}
		switch p.kind {
		case typePublish:
			qos := p.flags >> 1 & 3
			topic, rest, err := getString(p.body)
			if err != nil {
				c.fail(err)
				return
			}
			var id uint16
			if qos > 0 {
				if len(rest) < 2 {
					c.fail(errMalformed)
					return
				}
				id, rest = uint16(rest[0])<<8|uint16(rest[1]), rest[2:]
			}
			if c.o.OnMessage != nil {
				c.o.OnMessage(Message{Topic: topic, Payload: append([]byte(nil), rest...), QoS: qos, Retained: p.flags&1 == 1})
			}
			if qos == 1 {
				if err := c.write(packet{kind: typePuback, body: putUint16(nil, id)}); err != nil {
					return
				}
			}
		case typePuback, typeSuback, typeUnsuback:
			if len(p.body) < 2 {
				c.fail(errMalformed)
				return
			}
			id := uint16(p.body[0])<<8 | uint16(p.body[1])
			c.mu.Lock()
			ch := c.acks[id]
			delete(c.acks, id)
			c.mu.Unlock()
			if ch != nil {
				ch <- p.body[2:]
			}
		case typePingresp:
		}
	}
}

func (c *Conn) keepAlive() {
	t := time.NewTicker(c.o.KeepAlive / 2)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
			if c.write(packet{kind: typePingreq}) != nil {
				return
			}
		}
	}
}

// register reserves a packet id and the channel its acknowledgement arrives on.
func (c *Conn) register() (uint16, chan []byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return 0, nil, ErrClosed
	}
	for i := 0; i < 65535; i++ {
		c.nextID++
		if c.nextID == 0 {
			c.nextID = 1
		}
		if _, busy := c.acks[c.nextID]; !busy {
			ch := make(chan []byte, 1)
			c.acks[c.nextID] = ch
			return c.nextID, ch, nil
		}
	}
	return 0, nil, errors.New("mqtt: no free packet id")
}

func (c *Conn) await(ctx context.Context, id uint16, ch chan []byte) ([]byte, error) {
	t := time.NewTimer(c.o.Timeout)
	defer t.Stop()
	select {
	case b := <-ch:
		return b, nil
	case <-c.done:
		return nil, ErrClosed
	case <-ctx.Done():
	case <-t.C:
	}
	c.mu.Lock()
	delete(c.acks, id)
	c.mu.Unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, errors.New("mqtt: the broker did not acknowledge in time")
}

// Subscribe subscribes to the filters at the given QoS (0 or 1) and waits for the SUBACK.
func (c *Conn) Subscribe(ctx context.Context, qos byte, filters ...string) error {
	if len(filters) == 0 {
		return nil
	}
	id, ch, err := c.register()
	if err != nil {
		return err
	}
	body := putUint16(nil, id)
	for _, f := range filters {
		if !ValidFilter(f) {
			return fmt.Errorf("mqtt: %q is not a valid topic filter", f)
		}
		body = append(putString(body, f), qos)
	}
	if err := c.write(packet{kind: typeSubscribe, flags: 2, body: body}); err != nil {
		return err
	}
	granted, err := c.await(ctx, id, ch)
	if err != nil {
		return err
	}
	for _, g := range granted {
		if g == 0x80 {
			return errors.New("mqtt: the broker refused a subscription")
		}
	}
	return nil
}

// Publish sends a message. At QoS 1 it returns after the broker's PUBACK, so a nil error means the broker
// holds the message.
func (c *Conn) Publish(ctx context.Context, topic string, payload []byte, qos byte, retain bool) error {
	if topic == "" || containsAny(topic, "#+") {
		return fmt.Errorf("mqtt: %q is not a valid topic name", topic)
	}
	flags := qos << 1
	if retain {
		flags |= 1
	}
	body := putString(nil, topic)
	var id uint16
	var ch chan []byte
	if qos == 1 {
		var err error
		if id, ch, err = c.register(); err != nil {
			return err
		}
		body = putUint16(body, id)
	} else if err := c.Err(); err != nil {
		return ErrClosed
	}
	body = append(body, payload...)
	if err := c.write(packet{kind: typePublish, flags: flags, body: body}); err != nil {
		return err
	}
	if qos == 1 {
		_, err := c.await(ctx, id, ch)
		return err
	}
	return nil
}

// Close says goodbye and closes the connection.
func (c *Conn) Close() error {
	_ = c.write(packet{kind: typeDisconnect})
	c.fail(ErrClosed)
	return nil
}
