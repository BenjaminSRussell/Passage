package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
	"github.com/BenjaminSRussell/passage/services/shopfloor/migrations"
)

// iotEvent is one fixture event; see testdata/iot_10k_reference.py for the spec,
// which this function restates and the script re-derives independently.
func iotEvent(machine string, k int) (kind, state string, good, reject int) {
	switch {
	case k == 1:
		return "state", "RUNNING", 0, 0
	case k == 2500:
		return "state", "OFF", 0, 0
	}
	switch machine {
	case "M-1":
		if k%100 == 0 {
			return "count", "", 0, 1
		}
		return "count", "", 1, 0
	case "M-2":
		switch {
		case k == 1001:
			return "state", "DOWN", 0, 0
		case k >= 1002 && k <= 1500:
			return "alarm", "", 0, 0
		case k == 1501:
			return "state", "RUNNING", 0, 0
		case k%40 == 0:
			return "count", "", 1, 1
		}
		return "count", "", 2, 0
	case "M-3":
		if k%2 == 0 {
			return "measurement", "", 0, 0
		}
		if k%7 == 0 {
			return "count", "", 3, 1
		}
		return "count", "", 3, 0
	default: // M-4
		switch {
		case k == 2:
			return "state", "SETUP", 0, 0
		case k >= 3 && k <= 250:
			return "measurement", "", 0, 0
		case k == 251:
			return "state", "RUNNING", 0, 0
		case k%25 == 0:
			return "count", "", 0, 2
		}
		return "count", "", 1, 0
	}
}

// Wave 25 AC2 / AC-25.3 / AC-25.4 over HTTP and Postgres, minus the real broker:
// four simulated machines publish 10,000 events in one minute (2,500 each, every
// 24 ms), merged by time into 20 batches of 500. Batch 7 is lost in a broker
// restart, 8 and 9 arrive (gaps reported), the consumer re-reads from its last
// committed offset (5-9, so 7 arrives late, below the high-water mark), 14 is
// redelivered and 17 overtakes 16. Every (machine, seq) must be stored exactly
// once, duplicates dropped, and GET oee must reflect all of it right after the
// last batch. Expected numbers come from testdata/iot_10k_reference.py (python3 -I),
// which re-states the fixture and the BR-25.5 formula with exact fractions.
//
// What this does not prove: a real Kafka/MQTT broker, the consumer group, the SSE
// tile push, or a sustained multi-minute load. The timings are measured on the
// test database and logged; the bounds are the AC's (10k/min, 5 s).
func TestHTTP_IoT10kEventsBrokerRestart(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	h := newRouter(store.New(app))
	machines := []string{"M-1", "M-2", "M-3", "M-4"}
	ideal := map[string]string{"M-1": "0.02", "M-2": "0.01", "M-3": "0.02", "M-4": "0.025"}
	resource := map[string]string{}
	for _, m := range machines {
		resource[m] = ids.New()
		body := fmt.Sprintf(`{"machineId":%q,"resourceId":%q,"idealCycleSeconds":%q}`, m, resource[m], ideal[m])
		if rec := callWith(t, h, http.MethodPost, "/v1/shopfloor/machines", body, floorDuty, map[string]string{"Idempotency-Key": "m-" + m}); rec.Code != http.StatusCreated {
			t.Fatalf("machine %s: %d %s", m, rec.Code, rec.Body.String())
		}
	}

	// The simulated minute ends just before the ingest starts, as a live feed would.
	// Ingest judges andon age by its wall clock, so a backfill hours later would
	// raise an andon for M-2's 12 s stop (see the README's andon timing question).
	t0 := time.Now().UTC().Truncate(time.Second).Add(-61 * time.Second)
	batch := func(b int) string {
		var sb strings.Builder
		sb.WriteString(`{"events":[`)
		first := true
		for k := 125*b + 1; k <= 125*b+125; k++ {
			ts := t0.Add(time.Duration(k-1) * 24 * time.Millisecond).Format(time.RFC3339Nano)
			for _, m := range machines {
				kind, state, good, reject := iotEvent(m, k)
				if !first {
					sb.WriteByte(',')
				}
				first = false
				fmt.Fprintf(&sb, `{"machineId":%q,"seq":%d,"kind":%q,"ts":%q`, m, k, kind, ts)
				switch kind {
				case "count":
					fmt.Fprintf(&sb, `,"goodCount":%d,"rejectCount":%d`, good, reject)
				case "state":
					fmt.Fprintf(&sb, `,"state":%q`, state)
				case "measurement":
					fmt.Fprintf(&sb, `,"measurement":{"name":"temp_c","value":"%d.5","unit":"Cel"}`, 60+k%20)
				}
				sb.WriteByte('}')
			}
		}
		sb.WriteString(`]}`)
		return sb.String()
	}

	type outcome struct{ accepted, dropped, gaps int }
	// Hand-checked against iot_10k_reference.py: each batch is 125 seq of 4 machines.
	plan := []struct {
		batch int
		want  outcome
	}{
		{0, outcome{500, 0, 0}}, {1, outcome{500, 0, 0}}, {2, outcome{500, 0, 0}}, {3, outcome{500, 0, 0}},
		{4, outcome{500, 0, 0}}, {5, outcome{500, 0, 0}}, {6, outcome{500, 0, 0}},
		// Batch 7 is lost in the restart; 8 opens a gap 876-1000 on every machine.
		{8, outcome{500, 0, 4}}, {9, outcome{500, 0, 0}},
		// The consumer re-reads from its committed offset: 5, 6, 8, 9 are duplicates, 7 fills the gap.
		{5, outcome{0, 500, 0}}, {6, outcome{0, 500, 0}}, {7, outcome{500, 0, 0}}, {8, outcome{0, 500, 0}}, {9, outcome{0, 500, 0}},
		{10, outcome{500, 0, 0}}, {11, outcome{500, 0, 0}}, {12, outcome{500, 0, 0}}, {13, outcome{500, 0, 0}},
		{14, outcome{500, 0, 0}}, {14, outcome{0, 500, 0}}, {15, outcome{500, 0, 0}},
		// 17 overtakes 16: gap 2001-2125 on every machine, then 16 fills it.
		{17, outcome{500, 0, 4}}, {16, outcome{500, 0, 0}}, {18, outcome{500, 0, 0}}, {19, outcome{500, 0, 0}},
	}
	bodies := make([]string, len(plan))
	for i, p := range plan {
		bodies[i] = batch(p.batch)
	}
	var total outcome
	started := time.Now()
	for i, p := range plan {
		rec := call(t, h, http.MethodPost, "/v1/shopfloor/machine-events", bodies[i], floorDuty)
		var res store.IngestResult
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("delivery %d (batch %d): %d %s", i, p.batch, rec.Code, rec.Body.String())
		}
		got := outcome{res.Accepted, res.Dropped, len(res.Gaps)}
		if got != p.want {
			t.Fatalf("delivery %d (batch %d) = %+v, want %+v (gaps %+v)", i, p.batch, got, p.want, res.Gaps)
		}
		total.accepted += got.accepted
		total.dropped += got.dropped
		total.gaps += got.gaps
	}
	ingest := time.Since(started)
	if total != (outcome{10000, 2500, 8}) {
		t.Fatalf("totals = %+v, want 10000 accepted, 2500 dropped, 8 gaps", total)
	}
	t.Logf("ingested 12,500 deliveries (10,000 distinct events) in %s: %.0f distinct events/min", ingest, 10000/ingest.Minutes())
	if ingest > time.Minute {
		t.Fatalf("ingest took %s; 10,000 events must keep up with one simulated minute", ingest)
	}

	// The OEE read right after the last batch reflects every event (the 5 s budget
	// is measured from the last ingest response to the OEE response).
	readStart := time.Now()
	q := "/v1/shopfloor/oee?resourceIds=" + strings.Join([]string{resource["M-1"], resource["M-2"], resource["M-3"], resource["M-4"]}, ",") +
		"&from=" + t0.Format(time.RFC3339) + "&to=" + t0.Add(5*time.Minute).Format(time.RFC3339) + "&bucket=5m"
	rec := call(t, h, http.MethodGet, q, "", floorDuty)
	read := time.Since(readStart)
	var series itemsResponse[oeeItem]
	if err := json.Unmarshal(rec.Body.Bytes(), &series); err != nil || rec.Code != http.StatusOK || len(series.Items) != 4 {
		t.Fatalf("oee %d %s", rec.Code, rec.Body.String())
	}
	t.Logf("OEE over 10,000 events read in %s", read)
	if read > 5*time.Second {
		t.Fatalf("OEE read took %s, over the 5 s budget", read)
	}
	type wantOEE struct {
		planned, run, down string
		total, good        int64
		a, p, qual, oee    string
		over               bool
	}
	// Literals printed by `python3 -I testdata/iot_10k_reference.py`.
	want := map[string]wantOEE{
		"M-1": {"0.9996", "0.9996", "0", 2498, 2474, "1", "0.833", "0.9904", "0.825", false},
		"M-2": {"0.9996", "0.7996", "0.2", 3994, 3944, "0.7999", "0.8325", "0.9875", "0.6576", false},
		"M-3": {"0.9996", "0.9996", "0", 3926, 3747, "1", "1", "0.9544", "0.9544", true},
		"M-4": {"0.9996", "0.9", "0.0996", 2337, 2159, "0.9004", "1", "0.9238", "0.8318", true},
	}
	byResource := map[string]oeeItem{}
	for _, it := range series.Items {
		byResource[it.ResourceID] = it
	}
	for _, m := range machines {
		b, w := byResource[resource[m]], want[m]
		got := wantOEE{b.PlannedMin.String(), b.RunMin.String(), b.DownMin.String(), b.TotalCount, b.GoodCount,
			b.Availability.String(), b.Performance.String(), b.Quality.String(), b.OEE.String(), b.OverRate}
		if got != w || b.SourceEvents != 2500 {
			t.Errorf("%s bucket = %+v (source events %d), want %+v (2500)", m, got, b.SourceEvents, w)
		}
	}

	// M-2's DOWN at seq 1001 (T0+24 s) to RUNNING at seq 1501 (T0+36 s) is one closed
	// downtime, with no andon (12 s is under the 10-minute threshold).
	rec = call(t, h, http.MethodGet, "/v1/shopfloor/downtimes?resourceId="+resource["M-2"], "", floorDuty)
	var downs itemsResponse[store.Downtime]
	_ = json.Unmarshal(rec.Body.Bytes(), &downs)
	if len(downs.Items) != 1 || !downs.Items[0].StartedAt.Equal(t0.Add(24*time.Second)) || downs.Items[0].EndedAt == nil ||
		!downs.Items[0].EndedAt.Equal(t0.Add(36*time.Second)) || downs.Items[0].AndonRaised {
		t.Fatalf("M-2 downtimes %s", rec.Body.String())
	}
	rec = call(t, h, http.MethodGet, "/v1/shopfloor/machines", "", floorDuty)
	var ms itemsResponse[store.Machine]
	_ = json.Unmarshal(rec.Body.Bytes(), &ms)
	for _, m := range ms.Items {
		if m.LastSeq != 2500 {
			t.Fatalf("machine %s last seq %d, want 2500", m.Code, m.LastSeq)
		}
	}
}
