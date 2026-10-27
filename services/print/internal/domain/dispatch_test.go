package domain_test

import (
	"errors"
	"fmt"
	"testing"

	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
)

func queueOf(codes ...string) []domain.Printer {
	out := make([]domain.Printer, 0, len(codes))
	for i, c := range codes {
		out = append(out, domain.Printer{ID: fmt.Sprintf("p-%d", i), Code: c})
	}
	return out
}

func TestDispatch_FailoverTable(t *testing.T) {
	down := map[string]bool{}
	send := func(p domain.Printer) error {
		if down[p.Code] {
			return errors.New("refused")
		}
		return nil
	}
	cases := []struct {
		name      string
		down      []string
		queue     []domain.Printer
		wantCode  string
		wantTries int
		wantFail  bool
		wantErr   string
	}{
		{"first device healthy", nil, queueOf("A", "B"), "A", 1, false, ""},
		{"first offline fails over after two attempts", []string{"A"}, queueOf("A", "B"), "B", 3, true, ""},
		{"all offline reports the last device tried", []string{"A", "B"}, queueOf("A", "B"), "B", 4, false, domain.CodeUnreachable},
		{"empty queue", nil, nil, "", 0, false, domain.CodeNoPrinter},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			down = map[string]bool{}
			for _, d := range c.down {
				down[d] = true
			}
			out, err := domain.Dispatch(c.queue, domain.FailoverAttempts, send)
			if c.wantErr != "" {
				var de *domain.Error
				if !errors.As(err, &de) || de.Code != c.wantErr {
					t.Fatalf("want %s, got %v", c.wantErr, err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if out.Printer.Code != c.wantCode || out.Attempts != c.wantTries || out.Failover != c.wantFail {
				t.Fatalf("got printer=%q attempts=%d failover=%v", out.Printer.Code, out.Attempts, out.Failover)
			}
		})
	}
}

// Property: with the first device offline, every job lands on a later device
// and the attempt count never exceeds the per-device budget times the queue size.
func TestDispatch_PropertyFailoverNeverLosesJobs(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(2, 5).Draw(t, "devices")
		codes := make([]string, n)
		for i := range codes {
			codes[i] = fmt.Sprintf("P%d", i)
		}
		queue := queueOf(codes...)
		offline := rapid.IntRange(0, n-2).Draw(t, "offlineCount")
		send := func(p domain.Printer) error {
			for i := 0; i < offline; i++ {
				if p.Code == codes[i] {
					return errors.New("offline")
				}
			}
			return nil
		}
		jobs := rapid.IntRange(1, 40).Draw(t, "jobs")
		for j := 0; j < jobs; j++ {
			out, err := domain.Dispatch(queue, domain.FailoverAttempts, send)
			if err != nil {
				t.Fatalf("job %d lost: %v", j, err)
			}
			if out.Printer.Code != codes[offline] {
				t.Fatalf("job %d printed on %s, want %s", j, out.Printer.Code, codes[offline])
			}
			if out.Attempts > domain.FailoverAttempts*(offline+1) {
				t.Fatalf("too many attempts: %d", out.Attempts)
			}
		}
	})
}
