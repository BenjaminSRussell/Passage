package mqtt

import (
	"errors"
	"net"
	"sync"
)

// Published is a message a client sent to the Broker.
type Published struct {
	Client  string
	Topic   string
	Payload []byte
	QoS     byte
}

// Broker is a small in-process MQTT 3.1.1 broker: CONNECT with an optional user name and password,
// SUBSCRIBE with wildcards, PUBLISH at QoS 0 and 1, retained messages and keep-alive. It exists so the
// connector's tests and the demo plant need no external broker. It is not a product: no sessions, no QoS 2,
// no will, no limits. A proof against a real broker is tracked in #213.
type Broker struct {
	// Username and Password, when set, are required to connect.
	Username, Password string

	mu       sync.Mutex
	ln       net.Listener
	clients  map[*brokerClient]bool
	retained map[string][]byte
	log      []Published
	wg       sync.WaitGroup
}

type brokerClient struct {
	id      string
	c       net.Conn
	wmu     sync.Mutex
	filters []string
	nextID  uint16
}

// Listen starts the broker on addr ("127.0.0.1:0" for a free port).
func (b *Broker) Listen(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.ln, b.clients, b.retained = ln, map[*brokerClient]bool{}, map[string][]byte{}
	b.mu.Unlock()
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			b.wg.Add(1)
			go func() { defer b.wg.Done(); b.serve(c) }()
		}
	}()
	return nil
}

// Addr is the address the broker listens on.
func (b *Broker) Addr() string { return b.ln.Addr().String() }

// Close stops the broker and drops every client.
func (b *Broker) Close() {
	b.mu.Lock()
	if b.ln != nil {
		b.ln.Close()
	}
	for c := range b.clients {
		c.c.Close()
	}
	b.mu.Unlock()
	b.wg.Wait()
}

// DropClients closes every client connection but keeps listening: a network outage for the clients.
func (b *Broker) DropClients() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for c := range b.clients {
		c.c.Close()
	}
}

// Received returns what clients published to the broker, in order.
func (b *Broker) Received() []Published {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]Published(nil), b.log...)
}

// Subscribers counts the clients with a subscription.
func (b *Broker) Subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for c := range b.clients {
		if len(c.filters) > 0 {
			n++
		}
	}
	return n
}

// Inject delivers a message to the subscribers as if a client had published it.
func (b *Broker) Inject(topic string, payload []byte, retain bool) {
	b.route(topic, payload, retain)
}

func (b *Broker) route(topic string, payload []byte, retain bool) {
	b.mu.Lock()
	if retain {
		if len(payload) == 0 {
			delete(b.retained, topic)
		} else {
			b.retained[topic] = append([]byte(nil), payload...)
		}
	}
	var to []*brokerClient
	for c := range b.clients {
		for _, f := range c.filters {
			if matches(f, topic) {
				to = append(to, c)
				break
			}
		}
	}
	b.mu.Unlock()
	for _, c := range to {
		c.send(topic, payload, false)
	}
}

func (c *brokerClient) send(topic string, payload []byte, retained bool) {
	body := putString(nil, topic)
	body = append(body, payload...)
	flags := byte(0)
	if retained {
		flags = 1
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, _ = c.c.Write(packet{kind: typePublish, flags: flags, body: body}.bytes())
}

func (b *Broker) serve(nc net.Conn) {
	defer nc.Close()
	p, err := readPacket(nc)
	if err != nil || p.kind != typeConnect {
		return
	}
	cl, code := b.connect(nc, p.body)
	if code != 0 {
		_, _ = nc.Write(packet{kind: typeConnack, body: []byte{0, code}}.bytes())
		return
	}
	cl.wmu.Lock()
	_, _ = nc.Write(packet{kind: typeConnack, body: []byte{0, 0}}.bytes())
	cl.wmu.Unlock()
	b.mu.Lock()
	b.clients[cl] = true
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.clients, cl)
		b.mu.Unlock()
	}()
	for {
		p, err := readPacket(nc)
		if err != nil {
			return
		}
		switch p.kind {
		case typePublish:
			qos := p.flags >> 1 & 3
			topic, rest, err := getString(p.body)
			if err != nil || qos > 1 {
				return
			}
			var id uint16
			if qos == 1 {
				if len(rest) < 2 {
					return
				}
				id, rest = uint16(rest[0])<<8|uint16(rest[1]), rest[2:]
			}
			payload := append([]byte(nil), rest...)
			b.mu.Lock()
			b.log = append(b.log, Published{Client: cl.id, Topic: topic, Payload: payload, QoS: qos})
			b.mu.Unlock()
			if qos == 1 {
				cl.wmu.Lock()
				_, _ = nc.Write(packet{kind: typePuback, body: putUint16(nil, id)}.bytes())
				cl.wmu.Unlock()
			}
			b.route(topic, payload, p.flags&1 == 1)
		case typeSubscribe:
			if len(p.body) < 2 {
				return
			}
			id, rest := p.body[:2], p.body[2:]
			var granted []byte
			var newFilters []string
			for len(rest) > 0 {
				var f string
				f, rest, err = getString(rest)
				if err != nil || len(rest) < 1 {
					return
				}
				rest = rest[1:]
				if !ValidFilter(f) {
					granted = append(granted, 0x80)
					continue
				}
				granted = append(granted, 0)
				newFilters = append(newFilters, f)
			}
			b.mu.Lock()
			cl.filters = append(cl.filters, newFilters...)
			var retained []Published
			for t, pl := range b.retained {
				for _, f := range newFilters {
					if matches(f, t) {
						retained = append(retained, Published{Topic: t, Payload: pl})
						break
					}
				}
			}
			b.mu.Unlock()
			cl.wmu.Lock()
			_, _ = nc.Write(packet{kind: typeSuback, body: append(append([]byte(nil), id...), granted...)}.bytes())
			cl.wmu.Unlock()
			for _, m := range retained {
				cl.send(m.Topic, m.Payload, true)
			}
		case typePingreq:
			cl.wmu.Lock()
			_, _ = nc.Write(packet{kind: typePingresp}.bytes())
			cl.wmu.Unlock()
		case typeDisconnect:
			return
		case typePuback:
		default:
			return
		}
	}
}

func (b *Broker) connect(nc net.Conn, body []byte) (*brokerClient, byte) {
	proto, rest, err := getString(body)
	if err != nil || proto != "MQTT" || len(rest) < 4 || rest[0] != 4 {
		return nil, 1 // unacceptable protocol version
	}
	flags := rest[1]
	rest = rest[4:]
	id, rest, err := getString(rest)
	if err != nil {
		return nil, 2
	}
	var user, pass string
	if flags&0x80 != 0 {
		if user, rest, err = getString(rest); err != nil {
			return nil, 4
		}
		if flags&0x40 != 0 {
			if pass, _, err = getString(rest); err != nil {
				return nil, 4
			}
		}
	}
	if b.Username != "" && (user != b.Username || pass != b.Password) {
		return nil, 4 // bad user name or password
	}
	if errors.Is(err, errMalformed) {
		return nil, 2
	}
	return &brokerClient{id: id, c: nc}, 0
}
