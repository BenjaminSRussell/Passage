package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileBufferSurvivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "buffer.log")
	b, err := OpenFileBuffer(path, time.Hour, 100, t0)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		b.Push(t0, []byte(fmt.Sprintf(`{"n":%d}`, i)))
	}
	got, through := b.Peek(t0, 3)
	if len(got) != 3 {
		t.Fatalf("peeked %d", len(got))
	}
	b.Ack(through) // 1..3 delivered
	b.Push(t0, []byte("line one\nline two"))
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}

	again, err := OpenFileBuffer(path, time.Hour, 100, t0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if again.Len() != 3 {
		t.Fatalf("after restart %d events, want 3 (two unsent and the one with a newline)", again.Len())
	}
	rest, through := again.Peek(t0.Add(time.Minute), 10)
	if string(rest[0]) != `{"n":4}` || string(rest[1]) != `{"n":5}` || string(rest[2]) != "line one\nline two" {
		t.Fatalf("restored %q", rest)
	}
	// New pushes continue the ids, so an Ack of the restored position never removes them.
	again.Push(t0.Add(time.Minute), []byte(`{"n":7}`))
	again.Ack(through)
	if again.Len() != 1 {
		t.Fatalf("len %d after ack, want only the new event", again.Len())
	}
}

func TestFileBufferIgnoresATornLastLineAndAgesOutOldEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "buffer.log")
	b, _ := OpenFileBuffer(path, time.Hour, 100, t0)
	b.Push(t0, []byte(`{"old":true}`))
	b.Push(t0.Add(90*time.Minute), []byte(`{"new":true}`))
	b.Close()
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString("P 9 12") // the crash cut this line
	f.Close()
	again, err := OpenFileBuffer(path, time.Hour, 100, t0.Add(100*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := again.Peek(t0.Add(100*time.Minute), 10)
	if len(got) != 1 || string(got[0]) != `{"new":true}` {
		t.Fatalf("restored %q, want only the event inside the age limit", got)
	}
}

func TestFileBufferRespectsCapacityOnRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "buffer.log")
	b, _ := OpenFileBuffer(path, time.Hour, 100, t0)
	for i := 0; i < 10; i++ {
		b.Push(t0, []byte(fmt.Sprint(i)))
	}
	b.Close()
	again, _ := OpenFileBuffer(path, time.Hour, 4, t0)
	got, _ := again.Peek(t0, 10)
	if len(got) != 4 || string(got[0]) != "6" || again.Evicted() != 6 {
		t.Fatalf("restored %q evicted %d", got, again.Evicted())
	}
}

// A connector that is killed with events buffered and the server down delivers them after the restart.
func TestRuntimeDeliversBufferedEventsAfterARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "buffer.log")
	srv := newServer()
	srv.down = true
	state := &MemorySeqState{}
	b1, err := OpenFileBuffer(path, time.Hour, 1000, t0)
	if err != nil {
		t.Fatal(err)
	}
	r1, _ := simRuntime(t, srv, 1, Options{Buffer: b1, State: state})
	for i := 0; i < 4; i++ {
		r1.Ingest(Reading{Address: "sim/1/count", TS: t0, Number: float64(10 + i)})
	}
	if _, err := r1.Flush(context.Background()); err == nil {
		t.Fatal("the server is down; the flush should fail")
	}
	b1.Close() // the process ends here

	srv.down = false
	b2, err := OpenFileBuffer(path, time.Hour, 1000, t0)
	if err != nil {
		t.Fatal(err)
	}
	r2, _ := simRuntime(t, srv, 1, Options{Buffer: b2, State: state})
	n, err := r2.Flush(context.Background())
	if err != nil || n != 3 { // the first reading only sets the counter baseline
		t.Fatalf("flushed %d, %v; want 3", n, err)
	}
	if len(srv.stored) != 3 || b2.Len() != 0 {
		t.Fatalf("server stored %d, buffer %d", len(srv.stored), b2.Len())
	}
}
