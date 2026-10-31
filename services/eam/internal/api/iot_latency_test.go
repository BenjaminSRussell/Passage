package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/eam/internal/store"
	"github.com/BenjaminSRussell/passage/services/eam/migrations"
)

// TestIoTStream_BreachToRequestLatency covers AC-44.4 / ticket Proof 3 on a
// local stack: a replay of IoT readings at 10 readings/s through a real HTTP
// server (POST /v1/eam/counters/{id}/readings, source IOT) into Postgres. For
// every reading that breaches a rule whose (asset, rule) has no request yet,
// the latency runs from the reading's scheduled send time (so a backlog
// counts) until a separate connection sees both the IOT request and its
// request.created outbox row; p95 must be under 5 s. The stream opens with
// the AC-44.4 example (87 °C then 88 °C a minute later on a > 85 rule: one
// request, with 87), a reading of exactly 85 that must not breach, and an
// asset whose readings never breach. Expected requests come from scanning the
// stream with the rule operators (first value above each threshold), not from
// the engine. PASSAGE_EAM_IOT_READINGS sets the stream length (default 200,
// 20 s). Not covered: the wave-25 Kafka bridge (not built) and production
// latency.
func TestIoTStream_BreachToRequestLatency(t *testing.T) {
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	n := 200
	if v := os.Getenv("PASSAGE_EAM_IOT_READINGS"); v != "" {
		var err error
		if n, err = strconv.Atoi(v); err != nil || n < 10 {
			t.Fatalf("PASSAGE_EAM_IOT_READINGS=%q: want an integer >= 10", v)
		}
	}
	srv := httptest.NewServer(New(store.New(pool), nil).Routes())
	defer srv.Close()
	client := srv.Client()
	duties := "EamAssetMaintain,EamCounterRegister,EamInquire"
	post := func(path, body, key string, extra map[string]string) (int, []byte) {
		req, err := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers(duties) {
			req.Header.Set(k, v)
		}
		req.Header.Set("Idempotency-Key", key)
		for k, v := range extra {
			req.Header.Set(k, v)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		raw, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, raw
	}
	create := func(path, body, key string) string {
		t.Helper()
		status, raw := post(path, body, key, nil)
		if status != http.StatusCreated {
			t.Fatalf("%s: status %d, want 201: %s", path, status, raw)
		}
		var v struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		return v.ID
	}

	type rule struct {
		id, asset string
		threshold float64
	}
	type sensor struct {
		counter, asset string
		rules          []rule
	}
	// 10 assets; each has a bearing temperature (> 85 and > 95) and a vibration
	// sensor (> 7.1 mm/s). Asset 9 runs cool and smooth all through.
	const assets = 10
	var sensors []sensor
	site := testkit.TenantID(9)
	for a := 0; a < assets; a++ {
		k := strconv.Itoa(a)
		asset := create("/v1/eam/assets", `{"name":"Press `+k+`","assetType":"PRESS","siteId":"`+site+`","criticality":3}`, "asset-"+k)
		if st, raw := post("/v1/eam/assets/"+asset+"/activate", ``, "act-"+k, map[string]string{"If-Match": `"1"`}); st != http.StatusOK {
			t.Fatalf("activate: %d %s", st, raw)
		}
		temp := create("/v1/eam/assets/"+asset+"/counters", `{"name":"bearing temp","unit":"C","cumulative":false,"source":"IOT"}`, "temp-"+k)
		vib := create("/v1/eam/assets/"+asset+"/counters", `{"name":"vibration","unit":"mm/s","cumulative":false,"source":"IOT"}`, "vib-"+k)
		hot := create("/v1/eam/counters/"+temp+"/rules", `{"name":"bearing hot","operator":">","threshold":85,"severity":2}`, "hot-"+k)
		crit := create("/v1/eam/counters/"+temp+"/rules", `{"name":"bearing critical","operator":">","threshold":95,"severity":1}`, "crit-"+k)
		shake := create("/v1/eam/counters/"+vib+"/rules", `{"name":"vibration high","operator":">","threshold":7.1,"severity":3}`, "shake-"+k)
		sensors = append(sensors,
			sensor{temp, asset, []rule{{hot, asset, 85}, {crit, asset, 95}}},
			sensor{vib, asset, []rule{{shake, asset, 7.1}}})
	}

	// The stream: the AC-44.4 example and an exact-threshold reading first, then
	// a deterministic linear congruential sequence over every sensor.
	type msg struct {
		sensor int
		value  float64
	}
	stream := []msg{{0, 87}, {0, 88}, {2, 85}, {2, 85.1}}
	seed := uint32(44)
	for i := len(stream); i < n; i++ {
		seed = seed*1664525 + 1013904223
		s := int(seed>>8) % len(sensors)
		r := int(seed>>16) % 1000
		var v float64
		switch {
		case s >= 2*(assets-1): // asset 9: never breaches
			v = map[bool]float64{true: 60 + float64(r%200)/10, false: 1 + float64(r%50)/10}[s%2 == 0]
		case s%2 == 0: // temperature 70.0-99.9
			v = 70 + float64(r%300)/10
		default: // vibration 2.0-8.9
			v = 2 + float64(r%70)/10
		}
		stream = append(stream, msg{s, v})
	}

	// Expected: per rule, the first reading strictly above its threshold.
	firstBreach := map[string]int{}
	for i, m := range stream {
		for _, r := range sensors[m.sensor].rules {
			if _, ok := firstBreach[r.id]; !ok && m.value > r.threshold {
				firstBreach[r.id] = i
			}
		}
	}

	ctx := testkit.Ctx(testkit.TenantID(1))
	visible := func(ruleID string) (bool, *float64) {
		var reqs, events int
		var value *float64
		err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, `select count(*), max(reading_value)::float8 from maintenance_request
			  where condition_rule_id = $1 and source = 'IOT'`, ruleID).Scan(&reqs, &value); err != nil {
				return err
			}
			return tx.QueryRow(ctx, `select count(*) from outbox o join maintenance_request m on m.id::text = o.aggregate_id
			  where m.condition_rule_id = $1 and o.type = 'dev.passage.eam.request.created.v1'`, ruleID).Scan(&events)
		})
		if err != nil {
			t.Fatal(err)
		}
		return reqs == 1 && events == 1, value
	}

	base := time.Now().UTC().Truncate(time.Second).Add(-time.Duration(n) * time.Second)
	start := time.Now()
	var latencies []time.Duration
	for i, m := range stream {
		due := start.Add(time.Duration(i) * 100 * time.Millisecond)
		if d := time.Until(due); d > 0 {
			time.Sleep(d)
		}
		at := base.Add(time.Duration(i) * time.Second)
		if i == 1 {
			at = base.Add(time.Minute) // AC-44.4: the 88 °C reading a minute after 87 °C
		}
		body := fmt.Sprintf(`{"value":%g,"readingAt":"%s","source":"IOT","messageId":"m-%d"}`, m.value, at.Format(time.RFC3339Nano), i)
		if st, raw := post("/v1/eam/counters/"+sensors[m.sensor].counter+"/readings", body, "m-"+strconv.Itoa(i), nil); st != http.StatusCreated {
			t.Fatalf("reading %d: %d %s", i, st, raw)
		}
		for _, r := range sensors[m.sensor].rules {
			if firstBreach[r.id] != i || m.value <= r.threshold {
				continue
			}
			for {
				ok, _ := visible(r.id)
				if ok {
					latencies = append(latencies, time.Since(due))
					break
				}
				if time.Since(due) > 10*time.Second {
					t.Fatalf("reading %d (%g) on rule %s: no request and request.created event after 10 s", i, m.value, r.id)
				}
				time.Sleep(5 * time.Millisecond)
			}
		}
	}
	elapsed := time.Since(start)

	// Exactly one request per breached (asset, rule) with the first breaching
	// value; none for rules never breached.
	for _, s := range sensors {
		for _, r := range s.rules {
			ok, value := visible(r.id)
			i, breached := firstBreach[r.id]
			switch {
			case breached && (!ok || value == nil || *value != stream[i].value):
				t.Errorf("rule %s: want one request with %g and one request.created event, got ok=%v value=%v", r.id, stream[i].value, ok, value)
			case !breached:
				var reqs int
				if err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
					return tx.QueryRow(ctx, `select count(*) from maintenance_request where condition_rule_id = $1`, r.id).Scan(&reqs)
				}); err != nil {
					t.Fatal(err)
				}
				if reqs != 0 {
					t.Errorf("rule %s (never breached): %d requests, want 0", r.id, reqs)
				}
			}
		}
	}
	if _, value := visible(sensors[0].rules[0].id); value == nil || *value != 87 {
		t.Fatalf("AC-44.4: bearing hot request reading = %v, want 87 (88 within 60 min creates none)", value)
	}
	if _, ok := firstBreach[sensors[2].rules[0].id]; !ok || stream[firstBreach[sensors[2].rules[0].id]].value != 85.1 {
		t.Fatal("fixture: asset 1's first > 85 reading must be 85.1, not the exact 85")
	}
	if len(latencies) != len(firstBreach) {
		t.Fatalf("measured %d breaches, want %d", len(latencies), len(firstBreach))
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	pct := func(p float64) time.Duration {
		idx := int(float64(len(latencies))*p+0.999999) - 1
		if idx < 0 {
			idx = 0
		}
		return latencies[idx]
	}
	p50, p95, worst := pct(0.50), pct(0.95), latencies[len(latencies)-1]
	rules := 0
	for _, s := range sensors {
		rules += len(s.rules)
	}
	t.Logf("eam-iot-stream: %d readings in %s (%.1f/s), %d of %d rules breached, one request each; ingest->request+event p50 %s p95 %s max %s",
		len(stream), elapsed.Round(time.Millisecond), float64(len(stream))/elapsed.Seconds(), len(latencies), rules,
		p50.Round(time.Microsecond), p95.Round(time.Microsecond), worst.Round(time.Microsecond))
	if p95 >= 5*time.Second {
		t.Fatalf("p95 %s, want < 5 s (BR-44.4)", p95)
	}
	if rate := float64(len(stream)) / elapsed.Seconds(); rate < 9 {
		t.Fatalf("replay ran at %.1f readings/s, want about 10", rate)
	}
}
