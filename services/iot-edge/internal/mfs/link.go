package mfs

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// Errors of the link.
var (
	// ErrNacked is returned by Send when the PLC refused the telegram; the code is in the message.
	ErrNacked = errors.New("mfs: telegram refused by the peer")
	// ErrNoAck is returned by Send when no acknowledgement arrived after every attempt.
	ErrNoAck = errors.New("mfs: no acknowledgement")
	// ErrLinkDown is returned when the peer was silent for longer than the keep-alive allows.
	ErrLinkDown = errors.New("mfs: link down, peer silent")
)

// Config tunes a Link. Zero values take the defaults.
type Config struct {
	Sender   string        // our 8-character station name (default "WMS")
	Receiver string        // the PLC's station name (default "PLC")
	AckWait  time.Duration // wait for an acknowledgement per attempt (default 2 s)
	Attempts int           // sends per telegram, the first included (default 3)
	// KeepAlive is the ALIV interval; the link is down after three silent intervals (default 10 s).
	KeepAlive time.Duration
	// Handler receives each new telegram from the PLC (ARRV, DIVT, ERRR) once. A returned error
	// answers NACK busy, and the PLC resends.
	Handler func(Telegram) error
	// OnGap is called with the number of sequence numbers skipped by the PLC.
	OnGap func(missing int)
}

func (c *Config) defaults() {
	if c.Sender == "" {
		c.Sender = "WMS"
	}
	if c.Receiver == "" {
		c.Receiver = "PLC"
	}
	if c.AckWait <= 0 {
		c.AckWait = 2 * time.Second
	}
	if c.Attempts < 1 {
		c.Attempts = 3
	}
	if c.KeepAlive <= 0 {
		c.KeepAlive = 10 * time.Second
	}
}

// Link is one reliable telegram session over a byte stream. Send is safe for concurrent use;
// Run reads the stream and must be running for Send to see acknowledgements.
type Link struct {
	conn net.Conn
	cfg  Config

	wmu sync.Mutex // serialises writes

	mu      sync.Mutex
	nextSeq int
	pending map[int]chan Telegram
	seen    map[int]bool // recently delivered peer sequences, for duplicate detection
	order   []int
	last    int // last peer sequence delivered, 0 before the first
	heard   time.Time
}

const seenWindow = 64

// NewLink wraps conn. The caller owns conn and closes it after Run returns.
func NewLink(conn net.Conn, cfg Config) *Link {
	cfg.defaults()
	return &Link{conn: conn, cfg: cfg, nextSeq: 1, pending: map[int]chan Telegram{}, seen: map[int]bool{}, heard: time.Now()}
}

func (l *Link) write(t Telegram) error {
	b, err := t.Encode()
	if err != nil {
		return err
	}
	l.wmu.Lock()
	defer l.wmu.Unlock()
	_, err = l.conn.Write(b)
	return err
}

func (l *Link) reply(t Telegram) error {
	t.Sender, t.Receiver = l.cfg.Sender, l.cfg.Receiver
	return l.write(t)
}

func (l *Link) take() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.nextSeq
	l.nextSeq++
	if l.nextSeq > MaxSeq {
		l.nextSeq = 1
	}
	return s
}

// Send writes t with the next sequence number and waits for the PLC to acknowledge it. An
// unanswered telegram is sent again with the same sequence (the PLC drops a repeat it already
// has), up to Attempts times. It returns the sequence used.
func (l *Link) Send(ctx context.Context, t Telegram) (int, error) {
	return l.SendSeq(ctx, t, l.take())
}

// SendSeq is Send with a sequence number the caller chose: a sender that was answered NACK busy sends the
// same telegram again under the same number, and the receiver delivers it once.
func (l *Link) SendSeq(ctx context.Context, t Telegram, seq int) (int, error) {
	t.Seq, t.Sender, t.Receiver = seq, l.cfg.Sender, l.cfg.Receiver
	if _, err := t.Encode(); err != nil {
		return 0, err
	}
	ch := make(chan Telegram, 1)
	l.mu.Lock()
	l.pending[t.Seq] = ch
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		delete(l.pending, t.Seq)
		l.mu.Unlock()
	}()
	for attempt := 0; attempt < l.cfg.Attempts; attempt++ {
		if err := l.write(t); err != nil {
			return t.Seq, err
		}
		timer := time.NewTimer(l.cfg.AckWait)
		select {
		case ans := <-ch:
			timer.Stop()
			if ans.Type == TypeNack {
				return t.Seq, fmt.Errorf("%w: code %s", ErrNacked, ans.Code)
			}
			return t.Seq, nil
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return t.Seq, ctx.Err()
		}
	}
	return t.Seq, fmt.Errorf("%w for seq %d after %d attempts", ErrNoAck, t.Seq, l.cfg.Attempts)
}

func (l *Link) remember(seq int) (fresh bool, missing int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen[seq] {
		return false, 0
	}
	if l.last != 0 {
		next := l.last + 1
		if next > MaxSeq {
			next = 1
		}
		if seq != next {
			missing = (seq - next + MaxSeq) % MaxSeq
		}
	}
	l.last = seq
	l.seen[seq] = true
	l.order = append(l.order, seq)
	if len(l.order) > seenWindow {
		delete(l.seen, l.order[0])
		l.order = l.order[1:]
	}
	return true, missing
}

func (l *Link) onTelegram(t Telegram) {
	switch t.Type {
	case TypeAckn, TypeNack:
		l.mu.Lock()
		ch := l.pending[t.RefSeq]
		l.mu.Unlock()
		if ch != nil {
			select {
			case ch <- t:
			default:
			}
		}
	case TypeAliv:
		_ = l.reply(Telegram{Type: TypeAckn, Seq: t.Seq, RefSeq: t.Seq})
	default:
		fresh, missing := l.remember(t.Seq)
		if missing > 0 && l.cfg.OnGap != nil {
			l.cfg.OnGap(missing)
		}
		if fresh && l.cfg.Handler != nil {
			if err := l.cfg.Handler(t); err != nil {
				// Not delivered: forget it so the PLC's resend is handled, and ask for one.
				l.mu.Lock()
				delete(l.seen, t.Seq)
				l.mu.Unlock()
				_ = l.reply(Telegram{Type: TypeNack, RefSeq: t.Seq, Code: NackBusy})
				return
			}
		}
		_ = l.reply(Telegram{Type: TypeAckn, RefSeq: t.Seq})
	}
}

// Run reads telegrams until ctx ends, the connection closes, or the peer is silent for three
// keep-alive intervals (ErrLinkDown). It sends ALIV every interval. Malformed frames are answered
// NACK and never end the run.
func (l *Link) Run(ctx context.Context) error {
	var fr Framer
	errc := make(chan error, 1)
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := l.conn.Read(buf)
			if n > 0 {
				l.mu.Lock()
				l.heard = time.Now()
				l.mu.Unlock()
				for _, body := range fr.Push(buf[:n]) {
					t, derr := Decode(body)
					if derr != nil {
						code := NackFormat
						if t.Type != "" && !isKnown(t.Type) {
							code = NackUnknown
						} else if errors.Is(derr, ErrTelegram) && t.Type != "" && isKnown(t.Type) && t.LU != "" && !ValidSSCC(t.LU) && t.LU != NoLU {
							code = NackLU
						}
						if t.Type != "" {
							_ = l.reply(Telegram{Type: TypeNack, RefSeq: t.Seq, Code: code})
						}
						continue
					}
					l.onTelegram(t)
				}
			}
			if err != nil {
				errc <- err
				return
			}
		}
	}()
	tick := time.NewTicker(l.cfg.KeepAlive)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errc:
			return err
		case <-tick.C:
			l.mu.Lock()
			silent := time.Since(l.heard)
			l.mu.Unlock()
			if silent > 3*l.cfg.KeepAlive {
				return ErrLinkDown
			}
			_ = l.write(Telegram{Type: TypeAliv, Seq: l.take(), Sender: l.cfg.Sender, Receiver: l.cfg.Receiver})
		}
	}
}

func isKnown(t string) bool {
	switch t {
	case TypeMove, TypeArrv, TypeDivt, TypeErrr, TypeAliv, TypeAckn, TypeNack:
		return true
	}
	return false
}
