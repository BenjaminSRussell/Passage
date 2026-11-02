package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/syncer"
)

const (
	syncPath      = "/v1/integration/sync-status"
	conflictsPath = "/v1/integration/sync-conflicts"
)

type syncBody struct {
	BindingID           string `json:"bindingId"`
	Entity              string `json:"entity"`
	ScheduleCron        string `json:"scheduleCron"`
	SourceOfTruth       string `json:"sourceOfTruth"`
	Paused              bool   `json:"paused"`
	Watermark           string `json:"watermark"`
	NextDueAt           string `json:"nextDueAt"`
	LastStatus          string `json:"lastStatus"`
	LastError           string `json:"lastError"`
	LastRunID           string `json:"lastRunId"`
	ConsecutiveFailures int    `json:"consecutiveFailures"`
	RowsLoaded          int    `json:"rowsLoaded"`
	RowsRejected        int    `json:"rowsRejected"`
	OpenConflicts       int    `json:"openConflicts"`
	Version             int    `json:"version"`
}

type syncRig struct {
	*liveRig
	sched   *syncer.Scheduler
	binding string
}

const partyPublish = `{"mapping":{"entity":"party","version":1,"fields":[{"source":"Name","target":"legalName","type":"string","required":true}],"tests":[{"name":"name","input":{"Name":"Acme"},"expect":{"legalName":"Acme"}}]}}`

// newSyncRig is a live rig with a scheduled migrate project and a party binding over a CSV drop with a timestamp cursor.
func newSyncRig(t *testing.T) *syncRig {
	t.Helper()
	l := newLiveRig(t)
	l.file("parties.csv", "id,Name,upd\nP1,Acme GmbH,2026-11-01T08:00:00Z\nP2,Beta AG,2026-11-01T09:00:00Z\n")
	if rec := do(l.r, http.MethodPost, specsPath, partyPublish, liveDuties+",migration.spec.write", true); rec.Code != http.StatusCreated {
		t.Fatalf("publish spec = %d %s", rec.Code, rec.Body)
	}
	rec := do(l.r, http.MethodPost, bindPath, `{"connectionProfileId":"`+l.profile.ID+`","entity":"party","keyFields":["id"],"cursorField":"upd","cursorType":"timestamp","source":{"file":"parties.csv"}}`, liveDuties, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("binding = %d %s", rec.Code, rec.Body)
	}
	var b struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &b)
	rec = do(l.r, http.MethodPatch, projectsPath+"/"+l.project.ID, `{"version":`+itoa(l.project.Version)+`,"scheduleCron":"*/15 * * * *"}`, liveDuties, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("schedule = %d %s", rec.Code, rec.Body)
	}
	return &syncRig{liveRig: l, sched: &syncer.Scheduler{Store: l.store, Runner: l.api.SyncRunner()}, binding: b.ID}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func (s *syncRig) status() syncBody {
	s.t.Helper()
	rec := do(s.r, http.MethodGet, syncPath+"/"+s.binding, "", liveDuties, true)
	var out syncBody
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		s.t.Fatalf("status = %d %s", rec.Code, rec.Body)
	}
	return out
}

func (s *syncRig) patch(body string) *httptest.ResponseRecorder {
	s.t.Helper()
	return do(s.r, http.MethodPatch, syncPath+"/"+s.binding, body, liveDuties, true)
}

// runNow makes the binding due at once and lets the scheduler take it.
func (s *syncRig) runNow() syncBody {
	s.t.Helper()
	cur := s.status()
	if rec := do(s.r, http.MethodPatch, syncPath+"/"+s.binding, `{"version":`+itoa(cur.Version)+`,"runNow":true}`, liveDuties, true); rec.Code != http.StatusOK {
		s.t.Fatalf("run now = %d %s", rec.Code, rec.Body)
	}
	s.sched.Tick(context.Background())
	return s.status()
}

func (s *syncRig) runs() []struct {
	ID     string
	Mode   string
	Counts map[string]int
} {
	s.t.Helper()
	rec := do(s.r, http.MethodGet, runsPath+"?bindingId="+s.binding, "", liveDuties, true)
	var out struct {
		Items []struct {
			ID     string
			Mode   string
			Counts map[string]int
		}
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out.Items
}

func TestSync_ScheduledDeltaRunsFromTheBindingsWatermarkAndOnlyAdvancesAfterTheLoadsCommit(t *testing.T) {
	s := newSyncRig(t)
	ctx := context.Background()

	// The first run has no watermark: a full read. Its watermark is the largest cursor it saw.
	s.sched.Tick(ctx)
	st := s.status()
	if st.LastStatus != "succeeded" || st.Watermark != "2026-11-01T09:00:00.000000000Z" || st.RowsLoaded != 2 || len(s.fakes.Parties) != 2 || st.ScheduleCron != "*/15 * * * *" {
		t.Fatalf("first run: %+v parties=%d", st, len(s.fakes.Parties))
	}
	if runs := s.runs(); len(runs) != 1 || runs[0].Mode != "delta" || runs[0].Counts["created"] != 2 {
		t.Fatalf("runs = %+v", runs)
	}
	if next, _ := time.Parse(time.RFC3339, st.NextDueAt); !next.After(time.Now()) || next.Minute()%15 != 0 {
		t.Fatalf("next due %q is not the next quarter hour", st.NextDueAt)
	}
	// Not due again until the next cron slot.
	s.sched.Tick(ctx)
	if len(s.runs()) != 1 {
		t.Fatal("a binding that is not due ran again")
	}

	// A delta reads only rows changed after the watermark: P2 edited, P3 new, P1 untouched.
	s.file("parties.csv", "id,Name,upd\nP1,Acme GmbH,2026-11-01T08:00:00Z\nP2,Beta Holding AG,2026-11-01T10:00:00Z\nP3,Gamma,2026-11-01T10:30:00Z\n")
	st = s.runNow()
	runs := s.runs()
	if st.Watermark != "2026-11-01T10:30:00.000000000Z" || len(runs) != 2 || runs[0].Counts["extracted"] != 2 || runs[0].Counts["updated"] != 1 || runs[0].Counts["created"] != 1 || len(s.fakes.Parties) != 3 {
		t.Fatalf("delta: %+v runs=%+v parties=%d", st, runs, len(s.fakes.Parties))
	}
	if x, _, _ := s.store.Xref().Get(ctx, domain.XrefKey{SourceSystem: "custom_acme", Entity: "party", SourceKey: "P2"}); s.fakes.Parties[x.TargetID].Body["legalName"] != "Beta Holding AG" {
		t.Fatal("the edited row must update the party it was loaded into")
	}
	// A row stamped with the watermark's own timestamp (committed after the last read) and one committed late with a
	// slightly earlier timestamp are both picked up: the delta reads from SyncLookback before the watermark, and the
	// rows read again are unchanged by content hash. The watermark never moves backwards.
	s.file("parties.csv", "id,Name,upd\nP1,Acme GmbH,2026-11-01T08:00:00Z\nP2,Beta Holding AG,2026-11-01T10:00:00Z\nP3,Gamma,2026-11-01T10:30:00Z\n"+
		"P6,Same Second,2026-11-01T10:30:00Z\nP7,Late Commit,2026-11-01T10:28:00Z\n")
	st = s.runNow()
	runs = s.runs()
	if st.Watermark != "2026-11-01T10:30:00.000000000Z" || runs[0].Counts["created"] != 2 || runs[0].Counts["unchanged"] != 1 || len(s.fakes.Parties) != 5 {
		t.Fatalf("late rows at the watermark: %+v runs=%+v parties=%d", st, runs[0], len(s.fakes.Parties))
	}
	// Editing the binding (here: its cursor) discards the watermark, so the next run is a full read again.
	rec := do(s.r, http.MethodGet, bindPath+"/"+s.binding, "", liveDuties, true)
	var bind struct{ Version int }
	_ = json.Unmarshal(rec.Body.Bytes(), &bind)
	if rec := do(s.r, http.MethodPatch, bindPath+"/"+s.binding, `{"version":`+itoa(bind.Version)+`,"cursorField":"upd","cursorType":"timestamp","keyFields":["id"]}`, liveDuties, true); rec.Code != http.StatusOK {
		t.Fatalf("edit binding = %d %s", rec.Code, rec.Body)
	}
	st = s.runNow()
	if runs = s.runs(); runs[0].Counts["extracted"] != 5 || st.Watermark != "2026-11-01T10:30:00.000000000Z" {
		t.Fatalf("after a binding edit the run must read everything: %+v %+v", st, runs[0])
	}
	if got := s.store.SyncEvents(); len(got) != 4 || got[3].Type != "dev.passage.migration.syncrun.completed.v1" {
		t.Fatalf("events = %+v", got)
	}

	// The target is down: the run fails, the watermark stays, so the next run reads the same rows again.
	s.file("parties.csv", "id,Name,upd\nP4,Delta,2026-11-01T11:00:00Z\n")
	s.fakes.Fail503["POST /v1/party/parties"] = 1000
	st = s.runNow()
	if st.LastStatus != "failed" || st.Watermark != "2026-11-01T10:30:00.000000000Z" || st.ConsecutiveFailures != 1 || st.LastError == "" {
		t.Fatalf("failed run: %+v", st)
	}
	if ev := s.store.SyncEvents(); ev[len(ev)-1].Type != "dev.passage.migration.syncrun.failed.v1" || !strings.Contains(string(ev[len(ev)-1].Data), `"watermark":"2026-11-01T10:30:00.000000000Z"`) {
		t.Fatalf("a failed run publishes sync-run failed with the watermark it kept: %s %s", ev[len(ev)-1].Type, ev[len(ev)-1].Data)
	}
	delete(s.fakes.Fail503, "POST /v1/party/parties")
	st = s.runNow()
	if st.LastStatus != "succeeded" || st.Watermark != "2026-11-01T11:00:00.000000000Z" || st.ConsecutiveFailures != 0 || len(s.fakes.Parties) != 6 {
		t.Fatalf("recovery: %+v parties=%d", st, len(s.fakes.Parties))
	}

	// Three failures in a row push the next attempt out (15 minutes and doubling) and the status shows it.
	s.file("parties.csv", "id,Name,upd\nP5,Epsilon,2026-11-01T12:00:00Z\n")
	s.fakes.Fail503["POST /v1/party/parties"] = 1000
	for i := 0; i < 3; i++ {
		st = s.runNow()
	}
	next, _ := time.Parse(time.RFC3339, st.NextDueAt)
	if st.ConsecutiveFailures != 3 || next.Before(time.Now().Add(14*time.Minute)) {
		t.Fatalf("backoff: %+v", st)
	}
}

func TestSync_SourceOfTruthPassageReportsConflictsAndResolutionsAreApplied(t *testing.T) {
	s := newSyncRig(t)
	ctx := context.Background()
	s.sched.Tick(ctx)
	cur := s.status()
	if rec := s.patch(`{"version":` + itoa(cur.Version) + `,"sourceOfTruth":"passage"}`); rec.Code != http.StatusOK {
		t.Fatalf("source of truth = %d %s", rec.Code, rec.Body.String())
	}

	// Legacy edits P1 and P2 and adds P3. The edits are held, the new row loads, nothing in Passage is overwritten.
	s.file("parties.csv", "id,Name,upd\nP1,Acme Legacy Name,2026-11-01T10:00:00Z\nP2,Beta Legacy Name,2026-11-01T10:05:00Z\nP3,Gamma,2026-11-01T10:30:00Z\n")
	st := s.runNow()
	if st.LastStatus != "partial" || st.OpenConflicts != 2 || len(s.fakes.Parties) != 3 || st.Watermark != "2026-11-01T10:30:00.000000000Z" {
		t.Fatalf("conflict run: %+v parties=%d", st, len(s.fakes.Parties))
	}
	x1, _, _ := s.store.Xref().Get(ctx, domain.XrefKey{SourceSystem: "custom_acme", Entity: "party", SourceKey: "P1"})
	if s.fakes.Parties[x1.TargetID].Body["legalName"] != "Acme GmbH" {
		t.Fatalf("Passage's record was overwritten: %v", s.fakes.Parties[x1.TargetID].Body)
	}
	runs := do(s.r, http.MethodGet, runsPath+"?bindingId="+s.binding, "", liveDuties, true)
	if !strings.Contains(runs.Body.String(), `"MIG-TARGET-DRIFT"`) || !strings.Contains(runs.Body.String(), `"conflicted":2`) {
		t.Fatalf("the run must report the held rows on MIG-TARGET-DRIFT: %s", runs.Body)
	}

	rec := do(s.r, http.MethodGet, conflictsPath+"?status=open&bindingId="+s.binding, "", liveDuties, true)
	var list struct {
		Items []struct {
			ID           string
			SourceKey    string
			LegacyFields map[string]any
			Version      int
		}
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Items) != 2 {
		t.Fatalf("conflicts = %d %s", rec.Code, rec.Body)
	}
	byKey := map[string]string{}
	for _, c := range list.Items {
		byKey[c.SourceKey] = c.ID
		if c.Version != 1 {
			t.Fatalf("a new conflict is version 1: %+v", c)
		}
		if c.LegacyFields["legalName"] == nil {
			t.Fatalf("conflict lacks the legacy values: %+v", c)
		}
	}

	// A resolution names the version it was decided on; without one it is refused, and so is one for a version a run
	// has replaced since. Resolving needs migration.run.
	if rec := do(s.r, http.MethodPost, conflictsPath+"/"+byKey["P1"]+":resolve", `{"resolution":"keep_passage"}`, liveDuties, true); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"version"`) {
		t.Fatalf("resolve without a version = %d %s", rec.Code, rec.Body)
	}
	if rec := do(s.r, http.MethodPost, conflictsPath+"/"+byKey["P1"]+":resolve", `{"resolution":"keep_passage","version":2}`, liveDuties, true); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "MIG-VERSION-CONFLICT") {
		t.Fatalf("resolve on a stale version = %d %s", rec.Code, rec.Body)
	}
	if rec := do(s.r, http.MethodPost, conflictsPath+"/"+byKey["P1"]+":resolve", `{"resolution":"keep_passage","version":1}`, "migration.project.read", true); rec.Code != http.StatusForbidden {
		t.Fatalf("resolve without migration.run = %d", rec.Code)
	}
	// Keep Passage for P1: closed, and not reported again while legacy says the same.
	if rec := do(s.r, http.MethodPost, conflictsPath+"/"+byKey["P1"]+":resolve", `{"resolution":"keep_passage","version":1}`, liveDuties, true); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"version":2`) {
		t.Fatalf("keep = %d %s", rec.Code, rec.Body)
	}
	if rec := do(s.r, http.MethodPost, conflictsPath+"/"+byKey["P1"]+":resolve", `{"resolution":"keep_passage","version":2}`, liveDuties, true); rec.Code != http.StatusConflict {
		t.Fatalf("a second resolve = %d", rec.Code)
	}
	if rec := do(s.r, http.MethodPost, conflictsPath+"/"+byKey["P1"]+":resolve", `{"resolution":"nonsense","version":2}`, liveDuties, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad resolution = %d", rec.Code)
	}
	// Accept legacy for P2: the legacy name is written into the Passage party under the recorded version.
	if rec := do(s.r, http.MethodPost, conflictsPath+"/"+byKey["P2"]+":resolve", `{"resolution":"accept_legacy","version":1}`, liveDuties, true); rec.Code != http.StatusOK {
		t.Fatalf("accept = %d %s", rec.Code, rec.Body)
	}
	x2, _, _ := s.store.Xref().Get(ctx, domain.XrefKey{SourceSystem: "custom_acme", Entity: "party", SourceKey: "P2"})
	if s.fakes.Parties[x2.TargetID].Body["legalName"] != "Beta Legacy Name" || x2.TargetVersion != 2 {
		t.Fatalf("accept legacy did not reach Passage: %+v %v", x2, s.fakes.Parties[x2.TargetID].Body)
	}
	// Re-reading the same legacy rows (no cursor movement: force a full read) reports nothing new.
	s.file("parties.csv", "id,Name,upd\nP1,Acme Legacy Name,2026-11-01T12:00:00Z\nP2,Beta Legacy Name,2026-11-01T12:00:00Z\nP3,Gamma,2026-11-01T12:00:00Z\n")
	st = s.runNow()
	if st.OpenConflicts != 0 || st.LastStatus != "succeeded" {
		t.Fatalf("resolved conflicts came back: %+v", st)
	}
	// A further legacy change to P1 is reported again.
	s.file("parties.csv", "id,Name,upd\nP1,Acme Newer Name,2026-11-01T13:00:00Z\n")
	if st = s.runNow(); st.OpenConflicts != 1 {
		t.Fatalf("a changed legacy row must be reported again: %+v", st)
	}
}

func TestSync_PausedBindingsAreSkippedAndPauseIsVersioned(t *testing.T) {
	s := newSyncRig(t)
	s.sched.Tick(context.Background())
	cur := s.status()
	if rec := s.patch(`{"version":` + itoa(cur.Version+5) + `,"paused":true}`); rec.Code != http.StatusConflict {
		t.Fatalf("stale version = %d", rec.Code)
	}
	if rec := s.patch(`{"paused":true}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing version = %d", rec.Code)
	}
	if rec := s.patch(`{"version":` + itoa(cur.Version) + `,"sourceOfTruth":"both"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad source of truth = %d", rec.Code)
	}
	if rec := s.patch(`{"version":` + itoa(cur.Version) + `,"paused":true,"runNow":true}`); rec.Code != http.StatusOK {
		t.Fatalf("pause = %d", rec.Code)
	}
	s.file("parties.csv", "id,Name,upd\nP9,Nine,2026-11-01T15:00:00Z\n")
	s.sched.Tick(context.Background())
	if len(s.runs()) != 1 || !s.status().Paused {
		t.Fatal("a paused binding ran")
	}
	// Pause needs the project duty; reading needs only migration.run.
	if rec := do(s.r, http.MethodPatch, syncPath+"/"+s.binding, `{"version":9,"paused":false}`, "migration.run,tenant.administer", true); rec.Code != http.StatusForbidden {
		t.Fatalf("pause without migration.project.write = %d", rec.Code)
	}
	if rec := do(s.r, http.MethodGet, syncPath, "", "migration.run,tenant.administer", true); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), s.binding) {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}
	if rec := do(s.r, http.MethodGet, syncPath+"?paused=true", "", liveDuties, true); !strings.Contains(rec.Body.String(), s.binding) {
		t.Fatalf("paused filter = %s", rec.Body)
	}
	if rec := do(s.r, http.MethodGet, syncPath+"?paused=maybe", "", liveDuties, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad filter = %d", rec.Code)
	}
}

func TestSync_AnActiveManualRunOfTheProjectBlocksTheScheduledRunWithoutPenalty(t *testing.T) {
	s := newSyncRig(t)
	if _, err := s.store.BeginProjectRun(context.Background(), s.project.ID, "00000000-0000-7000-8000-00000000beef"); err != nil {
		t.Fatal(err)
	}
	s.sched.Tick(context.Background())
	st := s.status()
	if len(s.runs()) != 0 || st.ConsecutiveFailures != 0 || st.LastRunID != "" || len(s.fakes.Parties) != 0 {
		t.Fatalf("one active run per project: %+v", st)
	}
	_ = s.store.EndProjectRun(context.Background(), s.project.ID, "00000000-0000-7000-8000-00000000beef")
	s.sched.Tick(context.Background())
	if len(s.runs()) != 1 {
		t.Fatal("the scheduled run must go ahead once the project is free")
	}
}

func TestSync_AMissingMappingSpecFailsTheRunWithAReason(t *testing.T) {
	s := newSyncRig(t)
	// A second binding for an entity that has no published spec.
	l := s.liveRig
	l.file("vendors.csv", "id,Party,Acct\nV1,P1,V-100\n")
	l.bind("vendor", "vendors.csv", `"id"`)
	// Both bindings are due and share the project, which runs one at a time (BR-10.2): whichever loses the project
	// claim on the first pass is released and runs on the second.
	s.sched.Tick(context.Background())
	s.sched.Tick(context.Background())
	rec := do(s.r, http.MethodGet, syncPath+"?failing=true", "", liveDuties, true)
	if !strings.Contains(rec.Body.String(), "no published mapping spec for vendor") {
		t.Fatalf("failing list = %s", rec.Body)
	}
}

func (s *syncRig) absent() (keys []string, status int) {
	s.t.Helper()
	rec := do(s.r, http.MethodGet, syncPath+"/"+s.binding+"/absent", "", liveDuties, true)
	var out struct {
		Items []struct{ SourceKey, TargetID string }
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	for _, it := range out.Items {
		if it.TargetID == "" {
			s.t.Fatalf("absent key %q has no target id", it.SourceKey)
		}
		keys = append(keys, it.SourceKey)
	}
	return keys, rec.Code
}

func TestSync_AKeyCheckReadsACursorBindingInFullAndListsKeysTheLegacySystemDeleted(t *testing.T) {
	s := newSyncRig(t)
	ctx := context.Background()
	s.sched.Tick(ctx) // the first run is a full read: nothing is missing yet
	if keys, code := s.absent(); code != http.StatusOK || len(keys) != 0 {
		t.Fatalf("absent after the first read = %v %d", keys, code)
	}
	st := struct {
		syncBody
		KeyCheckHours  int    `json:"keyCheckHours"`
		LastKeyCheckAt string `json:"lastKeyCheckAt"`
		AbsentCount    int    `json:"absentCount"`
	}{}
	get := func() {
		rec := do(s.r, http.MethodGet, syncPath+"/"+s.binding, "", liveDuties, true)
		st.AbsentCount = 0
		_ = json.Unmarshal(rec.Body.Bytes(), &st)
	}
	get()
	if st.KeyCheckHours != 24 || st.LastKeyCheckAt == "" {
		t.Fatalf("a full read counts as the key check: %+v", st)
	}

	// P2 is deleted in the legacy system. A delta cannot see it, so nothing is reported while no check is due.
	s.file("parties.csv", "id,Name,upd\nP1,Acme GmbH,2026-11-01T08:00:00Z\nP3,Gamma,2026-11-01T10:30:00Z\n")
	s.runNow()
	if keys, _ := s.absent(); len(keys) != 0 {
		t.Fatalf("a delta reported absent keys: %v", keys)
	}
	if got := s.runs()[0]; got.Counts["extracted"] != 1 {
		t.Fatalf("the second run must be a delta of one row: %+v", got)
	}

	// Asking for a key check makes the next run a full read: P2 is listed, nothing is deactivated in Passage.
	cur := s.status()
	if rec := s.patch(`{"version":` + itoa(cur.Version) + `,"keyCheckNow":true,"runNow":true}`); rec.Code != http.StatusOK {
		t.Fatalf("keyCheckNow = %d %s", rec.Code, rec.Body)
	}
	s.sched.Tick(ctx)
	if keys, _ := s.absent(); len(keys) != 1 || keys[0] != "P2" {
		t.Fatalf("absent = %v, want [P2]", keys)
	}
	get()
	if st.AbsentCount != 1 || st.LastKeyCheckAt == "" || len(s.fakes.Parties) != 3 {
		t.Fatalf("status after the check: %+v parties=%d", st, len(s.fakes.Parties))
	}
	ev := s.store.SyncEvents()
	last := ev[len(ev)-1]
	if last.Type != "dev.passage.migration.syncabsent.detected.v1" && ev[len(ev)-2].Type != "dev.passage.migration.syncabsent.detected.v1" {
		t.Fatalf("no syncabsent event among the last events: %s %s", ev[len(ev)-2].Type, last.Type)
	}

	// The same key still missing at the next full read is not announced again; a key that comes back leaves the list.
	before := len(s.store.SyncEvents())
	s.patch(`{"version":` + itoa(s.status().Version) + `,"keyCheckNow":true,"runNow":true}`)
	s.sched.Tick(ctx)
	if n := len(s.store.SyncEvents()); n != before+1 {
		t.Fatalf("a repeated absent key published %d extra events, want only the run event", n-before-1)
	}
	s.file("parties.csv", "id,Name,upd\nP1,Acme GmbH,2026-11-01T08:00:00Z\nP2,Beta AG,2026-11-01T09:00:00Z\nP3,Gamma,2026-11-01T10:30:00Z\n")
	s.patch(`{"version":` + itoa(s.status().Version) + `,"keyCheckNow":true,"runNow":true}`)
	s.sched.Tick(ctx)
	if keys, _ := s.absent(); len(keys) != 0 {
		t.Fatalf("a key that came back is still listed: %v", keys)
	}

	// The period is validated and 0 turns the check off.
	for _, bad := range []string{`-1`, `8761`} {
		if rec := s.patch(`{"version":` + itoa(s.status().Version) + `,"keyCheckHours":` + bad + `}`); rec.Code != http.StatusBadRequest {
			t.Fatalf("keyCheckHours %s = %d", bad, rec.Code)
		}
	}
	if rec := s.patch(`{"version":` + itoa(s.status().Version) + `,"keyCheckHours":0,"keyCheckNow":true}`); rec.Code != http.StatusOK {
		t.Fatalf("turn off = %d %s", rec.Code, rec.Body)
	}
	s.file("parties.csv", "id,Name,upd\nP1,Acme GmbH,2026-11-01T08:00:00Z\n")
	s.runNow()
	if keys, _ := s.absent(); len(keys) != 0 {
		t.Fatalf("a binding with the check off read in full: %v", keys)
	}
	if rec := do(s.r, http.MethodGet, syncPath+"/"+ids00+"/absent", "", liveDuties, true); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown binding = %d", rec.Code)
	}
	if rec := do(s.r, http.MethodGet, syncPath+"/"+s.binding+"/absent?cursor=zzz", "", liveDuties, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad cursor = %d", rec.Code)
	}
}

const ids00 = "00000000-0000-7000-8000-000000000099"
