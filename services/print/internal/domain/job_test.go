package domain_test

import (
	"errors"
	"testing"

	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
)

func TestJob_CompleteOnce(t *testing.T) {
	p := domain.Printer{ID: "p1", Code: "DOCK-1"}
	cases := []struct {
		name       string
		err        error
		wantStatus string
		wantErr    string
	}{
		{"accepted by printer", nil, domain.JobPrinted, ""},
		{"unreachable", &domain.Error{Code: domain.CodeUnreachable, Detail: "down"}, domain.JobFailed, domain.CodeUnreachable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			j := domain.Job{ID: "j1", Status: domain.JobQueued}
			if err := j.Complete(domain.Outcome{Printer: p, Attempts: 1}, c.err); err != nil {
				t.Fatal(err)
			}
			if j.Status != c.wantStatus || j.PrinterCode != "DOCK-1" || j.LastError != c.wantErr {
				t.Fatalf("job = %+v", j)
			}
			if !j.Terminal() {
				t.Fatal("completed job not terminal")
			}
			if err := j.Complete(domain.Outcome{}, nil); !errors.Is(err, domain.ErrTerminal) {
				t.Fatalf("second completion allowed: %v", err)
			}
		})
	}
}

func TestErrorCode_UnknownIsPRN0000(t *testing.T) {
	if got := domain.ErrorCode(errors.New("boom")); got != "PRN-0000" {
		t.Fatalf("got %s", got)
	}
}

func TestPrinter_Validate(t *testing.T) {
	ok := domain.Printer{Code: "DOCK-1", Name: "Dock 1", DeviceType: domain.DeviceZPL, Dpi: 203, Host: "10.0.4.20", Port: 9100, QueueCode: "DOCK", FailoverRank: 1}
	if f := ok.Validate(); len(f) != 0 {
		t.Fatalf("valid printer rejected: %v", f)
	}
	bad := ok
	bad.Dpi = 150
	bad.DeviceType = "LASER"
	bad.Port = 0
	bad.FailoverRank = 0
	f := bad.Validate()
	for _, field := range []string{"dpi", "deviceType", "port", "failoverRank"} {
		if _, ok := f[field]; !ok {
			t.Errorf("field %s not reported", field)
		}
	}
}
