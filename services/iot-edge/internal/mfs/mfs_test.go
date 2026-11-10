package mfs_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/iot-edge/internal/mfs"
)

// sscc completes 17 digits with the GS1 mod-10 check digit.
func sscc(base string) string {
	sum := 0
	for i := 0; i < 17; i++ {
		d := int(base[i] - '0')
		if (16-i)%2 == 0 {
			sum += 3 * d
		} else {
			sum += d
		}
	}
	return base + string(rune('0'+(10-sum%10)%10))
}

var (
	luA = sscc("30123456789012345")
	luB = sscc("30123456789012346")
)

func TestValidSSCC(t *testing.T) {
	// The GS1 General Specifications example SSCC.
	if !mfs.ValidSSCC("376104250021234569") {
		t.Fatal("the GS1 example SSCC 376104250021234569 was refused")
	}
	for _, bad := range []string{"", "37610425002123456", "376104250021234560", "37610425002123456A", luA[:17] + "0"} {
		if bad == luA[:17]+"0" && luA[17] == '0' {
			continue
		}
		if mfs.ValidSSCC(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
	if !mfs.ValidSSCC(luA) || !mfs.ValidSSCC(luB) {
		t.Fatal("generated SSCCs refused")
	}
}

func roundTrip(t *testing.T, in mfs.Telegram) mfs.Telegram {
	t.Helper()
	b, err := in.Encode()
	if err != nil {
		t.Fatalf("encode %+v: %v", in, err)
	}
	var fr mfs.Framer
	bodies := fr.Push(b)
	if len(bodies) != 1 {
		t.Fatalf("framed %d bodies from %q", len(bodies), b)
	}
	out, err := mfs.Decode(bodies[0])
	if err != nil {
		t.Fatalf("decode %q: %v", b, err)
	}
	return out
}

func TestCodec_RoundTripOfEveryType(t *testing.T) {
	hdr := func(t mfs.Telegram) mfs.Telegram { t.Seq, t.Sender, t.Receiver = 42, "WMS", "PLC"; return t }
	for _, in := range []mfs.Telegram{
		hdr(mfs.Telegram{Type: mfs.TypeMove, LU: luA, From: "RECV-01", To: "ASRS-AISLE3", Priority: 2}),
		hdr(mfs.Telegram{Type: mfs.TypeArrv, LU: luA, Location: "SCAN-07"}),
		hdr(mfs.Telegram{Type: mfs.TypeDivt, LU: luB, Location: "SORT-1", Lane: "L12"}),
		hdr(mfs.Telegram{Type: mfs.TypeErrr, LU: luA, Code: "0042", Text: "JAM AT MERGE 4"}),
		hdr(mfs.Telegram{Type: mfs.TypeErrr, LU: mfs.NoLU, Code: "0007", Text: "E-STOP"}),
		hdr(mfs.Telegram{Type: mfs.TypeAliv}),
		hdr(mfs.Telegram{Type: mfs.TypeAckn, RefSeq: 41}),
		hdr(mfs.Telegram{Type: mfs.TypeNack, RefSeq: 41, Code: mfs.NackBusy}),
	} {
		if got := roundTrip(t, in); got != in {
			t.Errorf("%s: got %+v, want %+v", in.Type, got, in)
		}
	}
}

func TestCodec_FrameLayout(t *testing.T) {
	b, err := mfs.Telegram{Type: mfs.TypeArrv, Seq: 7, Sender: "PLC", Receiver: "WMS", LU: luA, Location: "SCAN-07"}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	want := "\x020055ARRV00007PLC     WMS     " + luA + "SCAN-07     \x03"
	if string(b) != want {
		t.Fatalf("frame\n got %q\nwant %q", b, want)
	}
}

func TestCodec_Refusals(t *testing.T) {
	cases := map[string]mfs.Telegram{
		"bad SSCC check digit": {Type: mfs.TypeArrv, LU: luA[:17] + "9", Location: "X"},
		"MOVE to itself":       {Type: mfs.TypeMove, LU: luA, From: "A", To: "A", Priority: 1},
		"MOVE priority 0":      {Type: mfs.TypeMove, LU: luA, From: "A", To: "B"},
		"location too long":    {Type: mfs.TypeArrv, LU: luA, Location: "THIRTEEN-CHRS"},
		"non printable":        {Type: mfs.TypeArrv, LU: luA, Location: "A\nB"},
		"ARRV with no unit":    {Type: mfs.TypeArrv, LU: mfs.NoLU, Location: "X"},
		"unknown type":         {Type: "ZZZZ"},
		"seq too large":        {Type: mfs.TypeAliv, Seq: mfs.MaxSeq + 1},
		"ERRR code not digits": {Type: mfs.TypeErrr, LU: luA, Code: "AB12"},
	}
	for name, in := range cases {
		if _, err := in.Encode(); !errors.Is(err, mfs.ErrTelegram) {
			t.Errorf("%s: err = %v, want ErrTelegram", name, err)
		}
	}
}

func TestDecode_Refusals(t *testing.T) {
	hdr := "ARRV00001PLC     WMS     "
	for name, body := range map[string]string{
		"short":             "ARRV",
		"bad seq":           "ARRVxxxxxPLC     WMS     " + luA + "X           ",
		"short payload":     hdr + luA,
		"trailing bytes":    hdr + luA + "SCAN-07     " + "extra",
		"bad SSCC":          hdr + luA[:17] + "9" + "SCAN-07     ",
		"unknown type":      "ZZZZ00001PLC     WMS     ",
		"bad MOVE priority": "MOVE00001WMS     PLC     " + luA + "A           B           x",
	} {
		if _, err := mfs.Decode([]byte(body)); !errors.Is(err, mfs.ErrTelegram) {
			t.Errorf("%s: err = %v, want ErrTelegram", name, err)
		}
	}
	// An unknown type still reports its header so the link can NACK its sequence.
	if got, _ := mfs.Decode([]byte("ZZZZ00009PLC     WMS     ")); got.Type != "ZZZZ" || got.Seq != 9 {
		t.Errorf("header of an unknown telegram = %+v", got)
	}
}

func frame(t *testing.T, tg mfs.Telegram) []byte {
	t.Helper()
	b, err := tg.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFramer_SplitJoinedAndNoise(t *testing.T) {
	a := frame(t, mfs.Telegram{Type: mfs.TypeArrv, Seq: 1, Sender: "PLC", Receiver: "WMS", LU: luA, Location: "S1"})
	b := frame(t, mfs.Telegram{Type: mfs.TypeAliv, Seq: 2, Sender: "PLC", Receiver: "WMS"})
	var fr mfs.Framer
	// Byte by byte: nothing until a frame completes.
	var got [][]byte
	for _, c := range append(append([]byte("noise"), a...), b...) {
		got = append(got, fr.Push([]byte{c})...)
	}
	if len(got) != 2 || fr.Dropped != len("noise") {
		t.Fatalf("got %d frames, dropped %d; want 2 and 5", len(got), fr.Dropped)
	}
	// Two frames in one read.
	if two := (&mfs.Framer{}).Push(append(append([]byte{}, a...), b...)); len(two) != 2 {
		t.Fatalf("joined frames = %d", len(two))
	}
}

func TestFramer_CorruptFrameCostsOnlyThatFrame(t *testing.T) {
	good := frame(t, mfs.Telegram{Type: mfs.TypeAliv, Seq: 3, Sender: "PLC", Receiver: "WMS"})
	for name, bad := range map[string]string{
		"length not digits": "\x02ab12" + strings.Repeat("x", 30) + "\x03",
		"length too small":  "\x020003abc\x03",
		"length too big":    "\x029999" + strings.Repeat("x", 40),
		"missing ETX":       "\x020025" + strings.Repeat("x", 25) + "Z",
	} {
		var fr mfs.Framer
		got := fr.Push([]byte(bad + string(good)))
		if len(got) != 1 {
			t.Errorf("%s: recovered %d frames, want the 1 good one", name, len(got))
		}
	}
}

// plc is a fake controller on the far end of a pipe.
type plc struct {
	conn net.Conn
	mu   sync.Mutex
	got  []mfs.Telegram
	fr   mfs.Framer
	// ack controls the answer to a host telegram: "" acknowledges, "silent" ignores it the
	// first time it is seen, "nack" refuses it.
	mode string
	seen map[int]int
}

func newPLC(conn net.Conn, mode string) *plc {
	p := &plc{conn: conn, mode: mode, seen: map[int]int{}}
	go p.read()
	return p
}

func (p *plc) read() {
	buf := make([]byte, 512)
	for {
		n, err := p.conn.Read(buf)
		if err != nil {
			return
		}
		for _, body := range p.fr.Push(buf[:n]) {
			tg, err := mfs.Decode(body)
			if err != nil {
				continue
			}
			p.mu.Lock()
			p.seen[tg.Seq]++
			count := p.seen[tg.Seq]
			if tg.Type == mfs.TypeMove && count == 1 {
				p.got = append(p.got, tg)
			}
			p.mu.Unlock()
			if tg.Type == mfs.TypeAckn || tg.Type == mfs.TypeNack || tg.Type == mfs.TypeAliv {
				continue
			}
			switch {
			case p.mode == "silent" && count == 1:
			case p.mode == "dead":
			case p.mode == "nack":
				p.send(mfs.Telegram{Type: mfs.TypeNack, Seq: 900 + tg.Seq, RefSeq: tg.Seq, Code: mfs.NackLU})
			default:
				p.send(mfs.Telegram{Type: mfs.TypeAckn, Seq: 900 + tg.Seq, RefSeq: tg.Seq})
			}
		}
	}
}

func (p *plc) send(t mfs.Telegram) {
	t.Sender, t.Receiver = "PLC", "WMS"
	b, err := t.Encode()
	if err == nil {
		_, _ = p.conn.Write(b)
	}
}

func (p *plc) moves() []mfs.Telegram {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]mfs.Telegram(nil), p.got...)
}

func startLink(t *testing.T, mode string, cfg mfs.Config) (*mfs.Link, *plc, <-chan error) {
	t.Helper()
	a, b := net.Pipe()
	cfg.AckWait = 40 * time.Millisecond
	l := mfs.NewLink(a, cfg)
	p := newPLC(b, mode)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx) }()
	t.Cleanup(func() { cancel(); _ = a.Close(); _ = b.Close() })
	return l, p, done
}

func TestLink_SendMoveIsAcknowledged(t *testing.T) {
	l, p, _ := startLink(t, "", mfs.Config{})
	mv, err := mfs.MoveFrom(mfs.TransportOrder{LoadUnit: luA, From: "RECV-01", To: "ASRS-3"})
	if err != nil {
		t.Fatal(err)
	}
	seq, err := l.Send(context.Background(), mv)
	if err != nil || seq != 1 {
		t.Fatalf("send = %d, %v", seq, err)
	}
	if m := p.moves(); len(m) != 1 || m[0].LU != luA || m[0].Priority != 5 || m[0].Sender != "WMS" {
		t.Fatalf("PLC got %+v", m)
	}
}

func TestLink_UnansweredTelegramIsResentWithTheSameSequenceAndDeliveredOnce(t *testing.T) {
	l, p, _ := startLink(t, "silent", mfs.Config{})
	mv, _ := mfs.MoveFrom(mfs.TransportOrder{LoadUnit: luA, From: "A", To: "B", Priority: 1})
	seq, err := l.Send(context.Background(), mv)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	p.mu.Lock()
	times := p.seen[seq]
	p.mu.Unlock()
	if times != 2 || len(p.moves()) != 1 {
		t.Fatalf("PLC saw seq %d %d times and %d distinct moves; want 2 and 1", seq, times, len(p.moves()))
	}
}

func TestLink_NoAckAfterEveryAttempt(t *testing.T) {
	l, p, _ := startLink(t, "dead", mfs.Config{Attempts: 2})
	mv, _ := mfs.MoveFrom(mfs.TransportOrder{LoadUnit: luA, From: "A", To: "B"})
	_, err := l.Send(context.Background(), mv)
	if !errors.Is(err, mfs.ErrNoAck) {
		t.Fatalf("err = %v, want ErrNoAck", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.seen[1] != 2 {
		t.Fatalf("attempts seen = %d, want 2", p.seen[1])
	}
}

func TestLink_PeerRefusalIsReported(t *testing.T) {
	l, _, _ := startLink(t, "nack", mfs.Config{})
	mv, _ := mfs.MoveFrom(mfs.TransportOrder{LoadUnit: luA, From: "A", To: "B"})
	_, err := l.Send(context.Background(), mv)
	if !errors.Is(err, mfs.ErrNacked) || !strings.Contains(err.Error(), mfs.NackLU) {
		t.Fatalf("err = %v, want ErrNacked with code %s", err, mfs.NackLU)
	}
}

func TestLink_InvalidTelegramNeverReachesTheWire(t *testing.T) {
	l, p, _ := startLink(t, "", mfs.Config{})
	_, err := l.Send(context.Background(), mfs.Telegram{Type: mfs.TypeMove, LU: "123", From: "A", To: "B", Priority: 1})
	if !errors.Is(err, mfs.ErrTelegram) {
		t.Fatalf("err = %v, want ErrTelegram", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.seen) != 0 {
		t.Fatalf("PLC saw %v", p.seen)
	}
}

type inbox struct {
	mu  sync.Mutex
	got []mfs.Telegram
	err error
	gap []int
}

func (i *inbox) handle(t mfs.Telegram) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.err != nil {
		return i.err
	}
	i.got = append(i.got, t)
	return nil
}

func (i *inbox) count() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return len(i.got)
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(2 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestLink_ArrivalDeliveredOnceAndAcknowledgedEachTime(t *testing.T) {
	in := &inbox{}
	var acks int32
	var amu sync.Mutex
	a, b := net.Pipe()
	l := mfs.NewLink(a, mfs.Config{Handler: in.handle, OnGap: func(m int) { in.mu.Lock(); in.gap = append(in.gap, m); in.mu.Unlock() }})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = l.Run(ctx) }()
	defer a.Close()
	defer b.Close()
	var fr mfs.Framer
	go func() {
		buf := make([]byte, 512)
		for {
			n, err := b.Read(buf)
			if err != nil {
				return
			}
			for _, body := range fr.Push(buf[:n]) {
				if tg, err := mfs.Decode(body); err == nil && tg.Type == mfs.TypeAckn {
					amu.Lock()
					acks++
					amu.Unlock()
				}
			}
		}
	}()
	arrv := frame(t, mfs.Telegram{Type: mfs.TypeArrv, Seq: 5, Sender: "PLC", Receiver: "WMS", LU: luA, Location: "SCAN-07"})
	for i := 0; i < 3; i++ { // the PLC repeats it because it missed our ACK
		if _, err := b.Write(arrv); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "three acknowledgements", func() bool { amu.Lock(); defer amu.Unlock(); return acks == 3 })
	if in.count() != 1 {
		t.Fatalf("handler saw %d telegrams, want 1", in.count())
	}
	// Seq 5 then 9: three telegrams are missing.
	_, _ = b.Write(frame(t, mfs.Telegram{Type: mfs.TypeDivt, Seq: 9, Sender: "PLC", Receiver: "WMS", LU: luB, Location: "SORT-1", Lane: "L1"}))
	waitFor(t, "the second telegram", func() bool { return in.count() == 2 })
	in.mu.Lock()
	defer in.mu.Unlock()
	if len(in.gap) != 1 || in.gap[0] != 3 {
		t.Fatalf("gaps = %v, want [3]", in.gap)
	}
}

func TestLink_HandlerFailureIsNackedAndTheResendIsHandled(t *testing.T) {
	in := &inbox{err: errors.New("outbox full")}
	a, b := net.Pipe()
	l := mfs.NewLink(a, mfs.Config{Handler: in.handle})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = l.Run(ctx) }()
	defer a.Close()
	defer b.Close()
	replies := make(chan mfs.Telegram, 4)
	var fr mfs.Framer
	go func() {
		buf := make([]byte, 512)
		for {
			n, err := b.Read(buf)
			if err != nil {
				return
			}
			for _, body := range fr.Push(buf[:n]) {
				if tg, err := mfs.Decode(body); err == nil {
					replies <- tg
				}
			}
		}
	}()
	arrv := frame(t, mfs.Telegram{Type: mfs.TypeArrv, Seq: 1, Sender: "PLC", Receiver: "WMS", LU: luA, Location: "S1"})
	_, _ = b.Write(arrv)
	if r := <-replies; r.Type != mfs.TypeNack || r.Code != mfs.NackBusy || r.RefSeq != 1 {
		t.Fatalf("reply = %+v, want NACK busy for 1", r)
	}
	in.mu.Lock()
	in.err = nil
	in.mu.Unlock()
	_, _ = b.Write(arrv)
	if r := <-replies; r.Type != mfs.TypeAckn || r.RefSeq != 1 {
		t.Fatalf("reply = %+v, want ACK for 1", r)
	}
	if in.count() != 1 {
		t.Fatalf("handler saw %d, want 1", in.count())
	}
}

func TestLink_MalformedTelegramsAreNackedAndTheLinkSurvives(t *testing.T) {
	a, b := net.Pipe()
	l := mfs.NewLink(a, mfs.Config{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx) }()
	defer a.Close()
	defer b.Close()
	replies := make(chan mfs.Telegram, 4)
	var fr mfs.Framer
	go func() {
		buf := make([]byte, 512)
		for {
			n, err := b.Read(buf)
			if err != nil {
				return
			}
			for _, body := range fr.Push(buf[:n]) {
				if tg, err := mfs.Decode(body); err == nil {
					replies <- tg
				}
			}
		}
	}()
	raw := func(body string) {
		l4 := "0000" + itoa(len(body))
		_, _ = b.Write([]byte("\x02" + l4[len(l4)-4:] + body + "\x03"))
	}
	raw("ZZZZ00010PLC     WMS     ")
	if r := <-replies; r.Type != mfs.TypeNack || r.Code != mfs.NackUnknown || r.RefSeq != 10 {
		t.Fatalf("unknown type reply = %+v", r)
	}
	raw("ARRV00011PLC     WMS     " + luA[:17] + "9" + "SCAN-07     ")
	if r := <-replies; r.Type != mfs.TypeNack || r.Code != mfs.NackLU || r.RefSeq != 11 {
		t.Fatalf("bad SSCC reply = %+v", r)
	}
	raw("ARRV00012PLC     WMS     " + luA)
	if r := <-replies; r.Type != mfs.TypeNack || r.Code != mfs.NackFormat || r.RefSeq != 12 {
		t.Fatalf("short payload reply = %+v", r)
	}
	select {
	case err := <-done:
		t.Fatalf("link ended: %v", err)
	default:
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}

func TestLink_SilentPeerTakesTheLinkDown(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	go func() { // drain what the host writes so its keep-alive does not block
		buf := make([]byte, 512)
		for {
			if _, err := b.Read(buf); err != nil {
				return
			}
		}
	}()
	l := mfs.NewLink(a, mfs.Config{KeepAlive: 10 * time.Millisecond})
	done := make(chan error, 1)
	go func() { done <- l.Run(context.Background()) }()
	select {
	case err := <-done:
		if !errors.Is(err, mfs.ErrLinkDown) {
			t.Fatalf("err = %v, want ErrLinkDown", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("link never went down")
	}
	_ = a.Close()
}

func TestEvents_MappingAndTransportOrder(t *testing.T) {
	at := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	e, err := mfs.EventFrom(mfs.Telegram{Type: mfs.TypeArrv, Seq: 5, Sender: "PLC", LU: luA, Location: "SCAN-07"}, at)
	if err != nil || e.Kind != mfs.KindArrived || e.LoadUnit != luA || e.Location != "SCAN-07" || e.Station != "PLC" || e.Seq != 5 {
		t.Fatalf("arrival = %+v, %v", e, err)
	}
	e, _ = mfs.EventFrom(mfs.Telegram{Type: mfs.TypeDivt, LU: luB, Location: "SORT-1", Lane: "L12"}, at)
	if e.Kind != mfs.KindDiverted || e.Lane != "L12" {
		t.Fatalf("divert = %+v", e)
	}
	e, _ = mfs.EventFrom(mfs.Telegram{Type: mfs.TypeErrr, LU: mfs.NoLU, Code: "0007", Text: "E-STOP"}, at)
	if e.Kind != mfs.KindFault || e.LoadUnit != "" || e.Code != "0007" {
		t.Fatalf("fault = %+v", e)
	}
	if _, err := mfs.EventFrom(mfs.Telegram{Type: mfs.TypeAliv}, at); err == nil {
		t.Fatal("ALIV mapped to an event")
	}
	if _, err := mfs.MoveFrom(mfs.TransportOrder{LoadUnit: "x", From: "A", To: "B"}); err == nil {
		t.Fatal("order with a bad load unit accepted")
	}
}
