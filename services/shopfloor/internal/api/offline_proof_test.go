package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
	"github.com/BenjaminSRussell/passage/services/shopfloor/migrations"
)

type offlineAction struct {
	id, worker, kind, job string
	start, stop           int // device minutes after 06:53; stop < 0 means none
}

// Wave 25 AC3 / AC-25.5 over HTTP and Postgres, server side only (the kiosk PWA
// queue is not built): kiosk K1 is offline from 07:00 to 07:30 (true time) and its
// clock runs 7 minutes slow. It queues 40 actions for four workers and reconnects
// at 07:40 (device 07:33). Meanwhile, online: the supervisor finishes job J3 at
// 07:10, and W4 walks to kiosk K2 and starts J6 there at 07:20.
//
// K1 sends its queue in three chunks (device time order, each chunk reversed to
// show the server orders by client time) of 11, 8 and 21 actions. The chunk
// boundaries fall inside two of W4's start/stop pairs (J5 start | J5 stop, and
// J3 start | J3 stop), so a start reaches the server one request before its own
// stop. The first answer is lost, so K1 resends chunk 1, then sends chunks 2 and
// 3, then the whole queue once more from a second tab.
//
// Expected, derived by hand from the action table (true = device + 7 min; hours
// are minutes / 60 to 0.01: 7 min 0.12, 3 min 0.05, 2 min 0.03, 10 min 0.17, 4 min 0.07):
//   - 40 kiosk rows stored once each; every resend item is a duplicate.
//   - every kiosk time is shifted by +420 s and records clockOffsetSeconds 420.
//   - W1-W3 (i = 0, 1, 2), clocked 07:0i to 07:2(2+i): J1 0.12 h, J2 0.12 h,
//     J7 0.05 h (auto-stopped by the J8 start, SF-0002), J8 0.03 h, J9 0.03 h.
//   - W4: the J5 start (07:03, chunk 1) and the J3 start (07:13, chunk 2) are
//     older than K2's open J6 and arrive without their stops, so the server closes
//     each at 07:20 (SF-0002). When K1's own stops arrive in the next chunk, each
//     server stop is corrected to zero length (07:03-07:03 and 07:13-07:13,
//     0.00 h, still flagged auto-stopped), so J5 is 0.17 h and J3 0.07 h from K1's
//     stops only. The J3 start carries job-finished-conflict (SF-0004) and J3 stays
//     finished; J4, started at 07:17 and never stopped on K1, is closed at 07:20 by
//     K2's J6 start (0.05 h, auto-stopped); J6 stays open (K2's work is newer than
//     the replay). Before the fix J5 held 0.17 + 0.28 h and J3 0.07 + 0.12 h.
func TestHTTP_Offline30MinReplayWithConflicts(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	server := time.Date(2026, 10, 21, 7, 40, 0, 0, time.UTC)
	h := newRouterAt(store.New(app), func() time.Time { return server })
	duties := allDuty + "," + DutyJobSchedule
	key := func(k string) map[string]string { return map[string]string{"Idempotency-Key": k} }

	workers := map[string]string{}
	for i, name := range []string{"W1", "W2", "W3", "W4"} {
		body := fmt.Sprintf(`{"partyId":%q,"name":%q,"badgeId":"B-%s","costRate":"%d","currency":"USD"}`, jobID, name, name, 20+2*i)
		rec := callWith(t, h, http.MethodPost, "/v1/shopfloor/workers", body, duties, key("w-"+name))
		var w store.Worker
		if err := json.Unmarshal(rec.Body.Bytes(), &w); err != nil || rec.Code != http.StatusCreated {
			t.Fatalf("worker %s: %d %s", name, rec.Code, rec.Body.String())
		}
		workers[name] = w.ID
	}
	jobs := map[string]string{}
	for n := 1; n <= 9; n++ {
		id := fmt.Sprintf("0195f0a1-0000-7000-8000-0000000000%02d", n)
		jobs[fmt.Sprintf("J%d", n)] = id
		if rec := call(t, h, http.MethodPost, "/v1/shopfloor/jobs", strings.Replace(upsertBody, jobID, id, 1), duties); rec.Code != http.StatusCreated {
			t.Fatalf("plan J%d: %d %s", n, rec.Code, rec.Body.String())
		}
	}

	// Online during the outage: J3 finished at 07:10, W4 starts J6 on K2 at 07:20.
	finish := `{"feedbackId":"0195f0a1-0000-7000-8000-0000000000f3","finished":true,"checks":[{"code":"len","value":"11"}]}`
	if rec := call(t, h, http.MethodPost, "/v1/shopfloor/jobs/"+jobs["J3"]+":feedback", finish, duties); rec.Code != http.StatusCreated {
		t.Fatalf("finish J3: %d %s", rec.Code, rec.Body.String())
	}
	k2 := fmt.Sprintf(`{"items":[{"id":"0195f0a1-0000-7000-8000-0000000000e6","workerId":%q,"kind":"job-start","jobId":%q,"startedAt":"2026-10-21T07:20:00Z"}]}`,
		workers["W4"], jobs["J6"])
	if rec := call(t, h, http.MethodPost, "/v1/shopfloor/registrations", k2, duties); rec.Code != http.StatusOK {
		t.Fatalf("K2 start: %d %s", rec.Code, rec.Body.String())
	}

	// K1's queue. Device minute m is 06:53+m on K1's clock (07:00+m true).
	var queue []offlineAction
	n := 0
	add := func(worker, kind, job string, start, stop int) {
		n++
		queue = append(queue, offlineAction{fmt.Sprintf("0195f0a1-0000-7000-8000-%012d", 100+n), worker, kind, job, start, stop})
	}
	for i, w := range []string{"W1", "W2", "W3"} {
		add(w, "clock-in", "", i, -1)
		add(w, "job-start", "J1", 1+i, -1)
		add(w, "job-stop", "J1", 1+i, 8+i)
		add(w, "job-start", "J2", 8+i, -1)
		add(w, "job-stop", "J2", 8+i, 15+i)
		add(w, "job-start", "J7", 15+i, -1)
		add(w, "job-start", "J8", 18+i, -1)
		add(w, "job-stop", "J8", 18+i, 20+i)
		add(w, "job-start", "J9", 20+i, -1)
		add(w, "job-stop", "J9", 20+i, 22+i)
		add(w, "clock-out", "", 22+i, -1)
	}
	add("W4", "clock-in", "", 0, -1)
	add("W4", "job-start", "J5", 3, -1)
	add("W4", "job-stop", "J5", 3, 13)
	add("W4", "job-start", "J3", 13, -1)
	add("W4", "job-stop", "J3", 13, 17)
	add("W4", "job-start", "J4", 17, -1)
	add("W4", "clock-out", "", 30, -1)
	if len(queue) != 40 {
		t.Fatalf("queue holds %d actions, want 40", len(queue))
	}
	sort.SliceStable(queue, func(i, j int) bool { return queue[i].start < queue[j].start })

	device := time.Date(2026, 10, 21, 6, 53, 0, 0, time.UTC)
	dev := func(m int) string { return device.Add(time.Duration(m) * time.Minute).Format(time.RFC3339) }
	itemJSON := func(a offlineAction) string {
		s := fmt.Sprintf(`{"id":%q,"workerId":%q,"kind":%q,"startedAt":%q,"deviceId":"K1","offline":true`, a.id, workers[a.worker], a.kind, dev(a.start))
		if a.job != "" {
			s += fmt.Sprintf(`,"jobId":%q`, jobs[a.job])
		}
		if a.stop >= 0 {
			s += fmt.Sprintf(`,"stoppedAt":%q`, dev(a.stop))
		}
		return s + "}"
	}
	send := func(actions []offlineAction) registrationResponse {
		parts := make([]string, 0, len(actions))
		for i := len(actions) - 1; i >= 0; i-- {
			parts = append(parts, itemJSON(actions[i]))
		}
		body := fmt.Sprintf(`{"deviceSentAt":"2026-10-21T07:33:00Z","items":[%s]}`, strings.Join(parts, ","))
		rec := call(t, h, http.MethodPost, "/v1/shopfloor/registrations", body, duties)
		var out registrationResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("replay: %d %s", rec.Code, rec.Body.String())
		}
		if out.ClockOffsetSeconds == nil || *out.ClockOffsetSeconds != 420 || !out.ClockCorrected {
			t.Fatalf("clock offset %v corrected %v", out.ClockOffsetSeconds, out.ClockCorrected)
		}
		return out
	}
	status := func(out registrationResponse) map[string]int {
		m := map[string]int{}
		for _, it := range out.Items {
			m[it.Status]++
		}
		return m
	}

	// The boundaries must split W4's pairs; check that against the table rather
	// than trusting the indices.
	pairAt := func(i int, job string) bool {
		a, b := queue[i-1], queue[i]
		return a.worker == "W4" && a.kind == "job-start" && a.job == job && b.worker == "W4" && b.kind == "job-stop" && b.job == job
	}
	if !pairAt(11, "J5") || !pairAt(19, "J3") {
		t.Fatalf("chunk boundaries 11 and 19 do not split W4's J5 and J3 pairs: %v | %v", queue[10:12], queue[18:20])
	}
	chunks := [][]offlineAction{queue[:11], queue[11:19], queue[19:]}
	var deliveries []registrationResponse
	for i, c := range chunks {
		out := send(c)
		if s := status(out); s[store.StatusAccepted] != len(c) {
			t.Fatalf("chunk %d: %v", i+1, s)
		}
		deliveries = append(deliveries, out)
		if i == 0 {
			if s := status(send(c)); s[store.StatusDuplicate] != len(c) {
				t.Fatalf("chunk 1 resend: %v", s)
			}
		}
	}
	if s := status(send(queue)); s[store.StatusDuplicate] != 40 {
		t.Fatalf("full resend: %v", s)
	}

	list := func(worker string) []store.RegistrationView {
		rec := call(t, h, http.MethodGet, "/v1/shopfloor/registrations?workerId="+workers[worker]+"&from=2026-10-21&to=2026-10-21", "", duties)
		var out itemsResponse[store.RegistrationView]
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("list %s: %d %s", worker, rec.Code, rec.Body.String())
		}
		return out.Items
	}
	// Warnings across the first deliveries: SF-0002 for each J7 stopped by a J8
	// start (3), for W4's J5 and J3 closed at 07:20 before their own stops arrived
	// (2) and for W4's J4 closed at K2's J6 start (1); SF-0004 for W4's J3 start.
	warnings := map[string]int{}
	for _, out := range deliveries {
		for _, it := range out.Items {
			for _, w := range it.Warnings {
				warnings[w.Code]++
			}
		}
	}
	if warnings["SF-0002"] != 6 || warnings["SF-0004"] != 1 || len(warnings) != 2 {
		t.Fatalf("warnings = %v, want SF-0002 x6 and SF-0004 x1", warnings)
	}

	jobName := map[string]string{}
	for name, id := range jobs {
		jobName[id] = name
	}
	kiosk := map[string]bool{}
	for _, a := range queue {
		kiosk[a.id] = true
	}
	type row struct{ kind, job, from, to, hours, flags string }
	describe := func(r store.RegistrationView) row {
		out := row{kind: r.Kind, from: r.StartedAt.UTC().Format("15:04"), flags: strings.Join(r.Flags, "+")}
		if r.JobID != nil {
			out.job = jobName[*r.JobID]
		}
		if r.StoppedAt != nil {
			out.to = r.StoppedAt.UTC().Format("15:04")
		}
		if r.Hours != nil {
			out.hours = r.Hours.StringFixed(2)
		}
		return out
	}
	hm := func(m int) string { return time.Date(2026, 10, 21, 7, m, 0, 0, time.UTC).Format("15:04") }
	// True times = device + 7 min. Rows come back oldest first, ties by id (the
	// kiosk ids sort before the server-written ones).
	want := map[string][]row{}
	for i, w := range []string{"W1", "W2", "W3"} {
		want[w] = []row{
			{"clock-in", "", hm(i), "", "", ""},
			{"job-start", "J1", hm(1 + i), "", "", ""},
			{"job-stop", "J1", hm(1 + i), hm(8 + i), "0.12", ""},
			{"job-start", "J2", hm(8 + i), "", "", ""},
			{"job-stop", "J2", hm(8 + i), hm(15 + i), "0.12", ""},
			{"job-start", "J7", hm(15 + i), "", "", ""},
			{"job-stop", "J7", hm(15 + i), hm(18 + i), "0.05", "auto-stopped"},
			{"job-start", "J8", hm(18 + i), "", "", ""},
			{"job-stop", "J8", hm(18 + i), hm(20 + i), "0.03", ""},
			{"job-start", "J9", hm(20 + i), "", "", ""},
			{"job-stop", "J9", hm(20 + i), hm(22 + i), "0.03", ""},
			{"clock-out", "", hm(22 + i), "", "", ""},
		}
	}
	want["W4"] = []row{
		{"clock-in", "", hm(0), "", "", ""},
		{"job-start", "J5", hm(3), "", "", ""},
		{"job-stop", "J5", hm(3), hm(13), "0.17", ""},
		{"job-stop", "J5", hm(3), hm(3), "0.00", "auto-stopped"},
		{"job-start", "J3", hm(13), "", "", "job-finished-conflict"},
		{"job-stop", "J3", hm(13), hm(17), "0.07", ""},
		{"job-stop", "J3", hm(13), hm(13), "0.00", "auto-stopped"},
		{"job-start", "J4", hm(17), "", "", ""},
		{"job-stop", "J4", hm(17), hm(20), "0.05", "auto-stopped"},
		{"job-start", "J6", hm(20), "", "", ""},
		{"clock-out", "", hm(30), "", "", ""},
	}
	stored := 0
	for _, w := range []string{"W1", "W2", "W3", "W4"} {
		got := list(w)
		if len(got) != len(want[w]) {
			t.Fatalf("%s has %d rows, want %d", w, len(got), len(want[w]))
		}
		for i, r := range got {
			if d := describe(r); d != want[w][i] {
				t.Errorf("%s row %d = %+v, want %+v", w, i, d, want[w][i])
			}
			// Kiosk rows carry the +420 s correction; server-written and K2 rows do not.
			if corrected := r.ClockOffsetSeconds != nil && *r.ClockOffsetSeconds == 420; corrected != kiosk[r.ID] {
				t.Errorf("%s row %d (%s) offset %v, kiosk row %v", w, i, r.Kind, r.ClockOffsetSeconds, kiosk[r.ID])
			}
			if kiosk[r.ID] {
				stored++
			}
		}
	}
	if stored != 40 {
		t.Fatalf("kiosk rows stored = %d, want 40 (once each)", stored)
	}
	if rec := call(t, h, http.MethodGet, "/v1/shopfloor/jobs/"+jobs["J3"], "", duties); !strings.Contains(rec.Body.String(), `"status":"finished"`) {
		t.Fatalf("J3 after the conflicting replay: %s", rec.Body.String())
	}

	// W4's day from the synced clock punches: 07:00-07:30 is 0.50 h base at 26.00 = 13.00.
	rec := callWith(t, h, http.MethodPost, "/v1/shopfloor/pay:calculate",
		fmt.Sprintf(`{"workerId":%q,"from":"2026-10-21","to":"2026-10-21"}`, workers["W4"]), duties, key("calc-w4"))
	var calc struct {
		Days []struct {
			PayLines []payLineView
			Flags    []string
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &calc); err != nil || rec.Code != http.StatusOK || len(calc.Days) != 1 || len(calc.Days[0].PayLines) != 1 {
		t.Fatalf("calculate W4: %d %s", rec.Code, rec.Body.String())
	}
	if l := calc.Days[0].PayLines[0]; l.Type != "base" || l.Hours.StringFixed(2) != "0.50" || l.Amount.StringFixed(2) != "13.00" || len(calc.Days[0].Flags) != 0 {
		t.Fatalf("W4 day = %s", rec.Body.String())
	}
}
