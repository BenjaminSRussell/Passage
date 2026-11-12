package mfs

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"
)

// SimPLC stands in for a conveyor or AS/RS controller (dev, the demo plant and tests). It serves the
// telegram profile v1 over a listener: a MOVE is acknowledged and, after ArriveAfter, the load unit
// "arrives" at the destination with an ARRV telegram; a load unit listed in Faults gets an ERRR instead.
// It speaks through the same Link the connector uses, so sequence numbers, acknowledgements and resends
// behave as on the real wire. The telegram layout of a real vendor's PLC needs that PLC (#213).
type SimPLC struct {
	Name        string        // the PLC's station name (default "PLC")
	Host        string        // the connector's station name (default "WMS")
	ArriveAfter time.Duration // time between MOVE and ARRV (default 50 ms)
	// Faults maps a load unit to the fault code reported instead of an arrival.
	Faults map[string]string
	// DivertLane, when set, makes the arrival a DIVT to that lane (a sorter).
	DivertLane string

	mu     sync.Mutex
	moves  []Telegram
	sent   int
	failed int
}

// Moves returns the MOVE telegrams the PLC accepted, in order.
func (p *SimPLC) Moves() []Telegram {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Telegram(nil), p.moves...)
}

// Serve accepts connections until ctx ends and runs each as a PLC session.
func (p *SimPLC) Serve(ctx context.Context, ln net.Listener) error {
	go func() { <-ctx.Done(); ln.Close() }()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go p.session(ctx, c)
	}
}

func (p *SimPLC) session(ctx context.Context, c net.Conn) {
	defer c.Close()
	name, host := p.Name, p.Host
	if name == "" {
		name = "PLC"
	}
	if host == "" {
		host = "WMS"
	}
	after := p.ArriveAfter
	if after <= 0 {
		after = 50 * time.Millisecond
	}
	var link *Link
	link = NewLink(c, Config{Sender: name, Receiver: host, AckWait: time.Second, KeepAlive: 5 * time.Second,
		Handler: func(t Telegram) error {
			if t.Type != TypeMove {
				return nil
			}
			p.mu.Lock()
			p.moves = append(p.moves, t)
			p.mu.Unlock()
			go func() {
				time.Sleep(after)
				out := Telegram{LU: t.LU, Location: t.To}
				switch code, bad := p.Faults[t.LU]; {
				case bad:
					out.Type, out.Code, out.Text = TypeErrr, code, "SIM FAULT"
				case p.DivertLane != "":
					out.Type, out.Lane = TypeDivt, p.DivertLane
				default:
					out.Type = TypeArrv
				}
				// A NACK busy means the host could not take it yet: send the same telegram again, with the
				// same sequence number, as a PLC does.
				seq, err := link.Send(ctx, out)
				for i := 0; i < 200 && errors.Is(err, ErrNacked) && ctx.Err() == nil; i++ {
					time.Sleep(20 * time.Millisecond)
					_, err = link.SendSeq(ctx, out, seq)
				}
			}()
			return nil
		}})
	_ = link.Run(ctx)
}
