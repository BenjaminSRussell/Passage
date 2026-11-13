package eventing

import (
	"strings"
	"testing"
	"time"
)

func TestMetricsWriteHandleAndAgeHistograms(t *testing.T) {
	var m Metrics
	m.observe(30*time.Millisecond, 2*time.Second, true)
	m.observe(3*time.Millisecond, time.Hour, false) // a duplicate: handling time only
	m.observe(time.Millisecond, -time.Second, true) // producer clock ahead: counted as 0
	var b strings.Builder
	m.Write(&b, "c1")
	out := b.String()
	for _, want := range []string{
		"# TYPE event_consumer_handle_duration_seconds histogram",
		`event_consumer_handle_duration_seconds_bucket{consumer="c1",le="0.005"} 2`,
		`event_consumer_handle_duration_seconds_bucket{consumer="c1",le="0.05"} 3`,
		`event_consumer_handle_duration_seconds_count{consumer="c1"} 3`,
		`event_consumer_event_age_seconds_bucket{consumer="c1",le="0.1"} 1`,
		`event_consumer_event_age_seconds_bucket{consumer="c1",le="2.5"} 2`,
		`event_consumer_event_age_seconds_bucket{consumer="c1",le="+Inf"} 2`,
		`event_consumer_event_age_seconds_sum{consumer="c1"} 2`,
		`event_consumer_applied_total{consumer="c1"} 0`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("metrics lack %q:\n%s", want, out)
		}
	}
}
