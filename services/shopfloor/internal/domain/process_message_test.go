package domain

import (
	"errors"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

var at = time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)

func strp(s string) *string { return &s }

func TestCategoryByCode_KnownCategoriesHaveDestinations(t *testing.T) {
	cases := map[string][]string{
		"phase-start":    {DestEBR, DestKafka},
		"consumption":    {DestJournal, DestEBR, DestKafka},
		"quality-result": {DestQuality, DestEBR, DestKafka},
		"deviation":      {DestEBR, DestQuality, DestKafka, DestWebhook},
		"order-status":   {DestKafka, DestWebhook},
		"custom":         {DestKafka},
	}
	for code, want := range cases {
		cat, ok := CategoryByCode(code)
		if !ok || cat.Code != code || len(cat.Destinations) != len(want) {
			t.Fatalf("%s: got %+v ok=%v", code, cat, ok)
		}
		for i := range want {
			if cat.Destinations[i] != want[i] {
				t.Errorf("%s destination %d = %s want %s", code, i, cat.Destinations[i], want[i])
			}
		}
	}
	if _, ok := CategoryByCode("shrug"); ok {
		t.Fatal("unknown category accepted")
	}
}

func TestFieldErrors_TableDriven(t *testing.T) {
	good := NewMessage{Source: SourceKiosk, SourceMessageID: "k-1", CategoryCode: "yield", OrderID: ids.New(), OccurredAt: at}
	cases := []struct {
		name   string
		mutate func(*NewMessage)
		field  string
	}{
		{"valid message", func(*NewMessage) {}, ""},
		{"unknown source", func(m *NewMessage) { m.Source = "fax" }, "source"},
		{"empty source message id", func(m *NewMessage) { m.SourceMessageID = "" }, "sourceMessageId"},
		{"unknown category", func(m *NewMessage) { m.CategoryCode = "shrug" }, "categoryCode"},
		{"order not a uuid", func(m *NewMessage) { m.OrderID = "PO-1" }, "orderId"},
		{"zero occurred at", func(m *NewMessage) { m.OccurredAt = time.Time{} }, "occurredAt"},
		{"duplicate characteristic name", func(m *NewMessage) {
			m.Values = []Characteristic{{Name: "t", Type: "numeric", Value: "1"}, {Name: "t", Type: "text", Value: "a"}}
		}, "values[1].name"},
		{"unknown characteristic type", func(m *NewMessage) { m.Values = []Characteristic{{Name: "t", Type: "color", Value: "red"}} }, "values[0].type"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := good
			c.mutate(&m)
			errs := m.FieldErrors()
			if c.field == "" {
				if len(errs) != 0 {
					t.Fatalf("unexpected errors %v", errs)
				}
				return
			}
			if _, ok := errs[c.field]; !ok {
				t.Fatalf("missing %s in %v", c.field, errs)
			}
		})
	}
}

func TestApplyLimits_TableDriven(t *testing.T) {
	open := Category{Code: "quality-result"}
	reject := Category{Code: "strict", RejectOutOfLimit: true}
	cases := []struct {
		name    string
		cat     Category
		in      Characteristic
		flagged bool
		code    string
	}{
		{"inside limits", open, Characteristic{Name: "t", Type: "numeric", Value: "11", Min: strp("10"), Max: strp("12")}, false, ""},
		{"above max flagged", open, Characteristic{Name: "t", Type: "numeric", Value: "13", Min: strp("10"), Max: strp("12")}, true, ""},
		{"below min flagged", open, Characteristic{Name: "t", Type: "numeric", Value: "9.99", Min: strp("10")}, true, ""},
		{"no limits never flags", open, Characteristic{Name: "t", Type: "numeric", Value: "1e6"}, false, ""},
		{"reject category refuses", reject, Characteristic{Name: "t", Type: "numeric", Value: "13", Max: strp("12")}, false, CodeOutOfLimitRejected},
		{"non-decimal numeric", open, Characteristic{Name: "t", Type: "numeric", Value: "hot"}, false, CodeInvalidValue},
		{"min above max", open, Characteristic{Name: "t", Type: "numeric", Value: "1", Min: strp("5"), Max: strp("2")}, false, CodeInvalidValue},
		{"boolean must be true or false", open, Characteristic{Name: "b", Type: "boolean", Value: "yes"}, false, CodeInvalidValue},
		{"boolean ok", open, Characteristic{Name: "b", Type: "boolean", Value: "true"}, false, ""},
		{"text cannot carry limits", open, Characteristic{Name: "s", Type: "text", Value: "x", Min: strp("1")}, false, CodeInvalidValue},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := ApplyLimits(c.cat, []Characteristic{c.in})
			if c.code != "" {
				var de *Error
				if !errors.As(err, &de) || de.Code != c.code {
					t.Fatalf("err = %v, want code %s", err, c.code)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error %v", err)
			}
			if out[0].OutOfLimit != c.flagged {
				t.Fatalf("OutOfLimit = %v want %v", out[0].OutOfLimit, c.flagged)
			}
		})
	}
}

func TestRetryDelay_MatchesPRDSchedule(t *testing.T) {
	want := []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour}
	for i, w := range want {
		got, ok := RetryDelay(i + 1)
		if !ok || got != w {
			t.Fatalf("delay after %d failures = %v %v, want %v", i+1, got, ok, w)
		}
	}
	if _, ok := RetryDelay(MaxDeliveryAttempts); ok {
		t.Fatal("a delay exists after the final attempt")
	}
}

func TestDeliveryReport_TableDriven(t *testing.T) {
	fresh := Delivery{Destination: DestEBR, Status: DeliveryPending}
	cases := []struct {
		name    string
		start   Delivery
		outcome string
		status  string
		attempt int
	}{
		{"first success", fresh, OutcomeDelivered, DeliveryDelivered, 0},
		{"first failure retries", fresh, OutcomeFailed, DeliveryPending, 1},
		{"fifth failure errors", Delivery{Status: DeliveryPending, Attempts: 4}, OutcomeFailed, DeliveryError, 5},
		{"success after failures", Delivery{Status: DeliveryPending, Attempts: 3}, OutcomeDelivered, DeliveryDelivered, 3},
		{"delivered is final", Delivery{Status: DeliveryDelivered, Attempts: 2, DeliveredAt: &at}, OutcomeFailed, DeliveryDelivered, 2},
		{"error stays error on failure", Delivery{Status: DeliveryError, Attempts: 5}, OutcomeFailed, DeliveryError, 5},
	}
	now := at
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, err := c.start.Report(c.outcome, strp("EBR offline"), now)
			if err != nil {
				t.Fatal(err)
			}
			if d.Status != c.status || d.Attempts != c.attempt {
				t.Fatalf("got status=%s attempts=%d, want %s/%d", d.Status, d.Attempts, c.status, c.attempt)
			}
			if d.Status == DeliveryPending && (d.NextAttemptAt == nil || !d.NextAttemptAt.After(now)) {
				t.Fatalf("pending without a later retry time: %+v", d)
			}
			if d.Status == DeliveryDelivered && d.DeliveredAt == nil {
				t.Fatal("delivered without deliveredAt")
			}
		})
	}
	if _, err := fresh.Report("maybe", nil, now); err == nil {
		t.Fatal("unknown outcome accepted")
	}
}

func TestDeliveryReprocess_OnlyFromError(t *testing.T) {
	if _, err := (Delivery{Status: DeliveryPending}).Reprocess(at); err == nil {
		t.Fatal("pending reprocessed")
	}
	d, err := (Delivery{Status: DeliveryError, Attempts: 5}).Reprocess(at)
	if err != nil || d.Status != DeliveryPending || d.Attempts != 0 {
		t.Fatalf("reprocess = %+v %v", d, err)
	}
}

func TestRollupStatus_TableDriven(t *testing.T) {
	d := func(s string) Delivery { return Delivery{Status: s} }
	cases := []struct {
		name string
		ds   []Delivery
		want string
	}{
		{"none", nil, MessageReceived},
		{"all pending", []Delivery{d(DeliveryPending), d(DeliveryPending)}, MessageReceived},
		{"all delivered", []Delivery{d(DeliveryDelivered), d(DeliveryDelivered)}, MessageDelivered},
		{"one delivered one pending", []Delivery{d(DeliveryDelivered), d(DeliveryPending)}, MessagePartial},
		{"one delivered one error", []Delivery{d(DeliveryDelivered), d(DeliveryError)}, MessagePartial},
		{"error and pending", []Delivery{d(DeliveryError), d(DeliveryPending)}, MessageReceived},
		{"all error", []Delivery{d(DeliveryError)}, MessageError},
	}
	for _, c := range cases {
		if got := RollupStatus(c.ds); got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}

// Property: a destination reaches error after exactly MaxDeliveryAttempts failures,
// its attempt count never exceeds the maximum, and a delivered destination never changes.
func TestPropertyDeliveryFailuresBoundedAndDeliveryFinal(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 12).Draw(t, "failures")
		d := Delivery{Destination: DestEBR, Status: DeliveryPending}
		for i := 0; i < n; i++ {
			next, err := d.Report(OutcomeFailed, nil, at)
			if err != nil {
				t.Fatal(err)
			}
			d = next
			if d.Attempts > MaxDeliveryAttempts {
				t.Fatalf("attempts %d exceed maximum", d.Attempts)
			}
		}
		if n >= MaxDeliveryAttempts && d.Status != DeliveryError {
			t.Fatalf("after %d failures status %s", n, d.Status)
		}
		if n < MaxDeliveryAttempts && d.Status != DeliveryPending {
			t.Fatalf("after %d failures status %s", n, d.Status)
		}
		ok, err := d.Report(OutcomeDelivered, nil, at)
		if err != nil {
			t.Fatal(err)
		}
		if ok.Status != DeliveryDelivered {
			t.Fatalf("success left status %s", ok.Status)
		}
		again, _ := ok.Report(OutcomeFailed, nil, at.Add(time.Hour))
		if again.Status != DeliveryDelivered {
			t.Fatalf("delivered destination changed to %s", again.Status)
		}
	})
}

// Property: the rolled-up status is delivered exactly when every destination is delivered.
func TestPropertyRollupDeliveredIffAllDelivered(t *testing.T) {
	statuses := []string{DeliveryPending, DeliveryDelivered, DeliveryError}
	rapid.Check(t, func(t *rapid.T) {
		ds := make([]Delivery, rapid.IntRange(1, 5).Draw(t, "n"))
		all := true
		for i := range ds {
			ds[i].Status = rapid.SampledFrom(statuses).Draw(t, "status")
			if ds[i].Status != DeliveryDelivered {
				all = false
			}
		}
		got := RollupStatus(ds)
		if (got == MessageDelivered) != all {
			t.Fatalf("statuses %+v rolled up to %s", ds, got)
		}
	})
}

func TestAnyOutOfLimit_FlagsTheMessage(t *testing.T) {
	values, err := ApplyLimits(Category{Code: "process-data"}, []Characteristic{
		{Name: "a", Type: "numeric", Value: "5", Min: strp("1"), Max: strp("9")},
		{Name: "b", Type: "text", Value: "ok"},
	})
	if err != nil || AnyOutOfLimit(values) {
		t.Fatalf("in-limit values flagged: %+v %v", values, err)
	}
	values, err = ApplyLimits(Category{Code: "process-data"}, []Characteristic{{Name: "a", Type: "numeric", Value: "9.01", Max: strp("9")}})
	if err != nil || !AnyOutOfLimit(values) {
		t.Fatalf("out-of-limit value not flagged: %+v %v", values, err)
	}
	if AnyOutOfLimit(nil) {
		t.Fatal("no values flagged")
	}
}
