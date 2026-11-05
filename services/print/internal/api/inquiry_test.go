package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/print/internal/api"
	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
)

func printerStatuses(t *testing.T, h http.Handler) map[string]domain.PrinterStatus {
	t.Helper()
	rec := call(t, h, http.MethodGet, "/print/v1/printers/status", "", "print.job.submit", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Items []domain.PrinterStatus `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	byCode := map[string]domain.PrinterStatus{}
	for _, s := range body.Items {
		byCode[s.Code] = s
	}
	return byCode
}

func TestPrinterStatus_ReportsStateFromLastJob(t *testing.T) {
	sender := &fakeSender{down: map[string]bool{"DOCK-1": true}}
	h := router(newMemStore(), sender)
	seedDock(t, h)

	before := printerStatuses(t, h)
	if before["DOCK-1"].State != domain.StateIdle || before["DOCK-2"].State != domain.StateIdle || before["DOCK-1"].LastJobAt != nil {
		t.Fatalf("before any job: %+v", before)
	}

	call(t, h, http.MethodPost, "/print/v1/jobs", lpBody, "print.job.submit", "status-1")
	after := printerStatuses(t, h)
	if s := after["DOCK-2"]; s.State != domain.StateReady || s.QueueDepth != 0 || s.LastJobAt == nil {
		t.Fatalf("DOCK-2 = %+v, want READY after the failover print", s)
	}
	// DOCK-1 refused the label, but the job is recorded on DOCK-2, so DOCK-1 has no last job.
	if after["DOCK-1"].State != domain.StateIdle {
		t.Fatalf("DOCK-1 = %+v, want IDLE", after["DOCK-1"])
	}

	sender.down["DOCK-2"] = true
	call(t, h, http.MethodPost, "/print/v1/jobs", lpBody, "print.job.submit", "status-2")
	failed := printerStatuses(t, h)["DOCK-2"]
	if failed.State != domain.StateError || failed.LastError != domain.CodeUnreachable {
		t.Fatalf("DOCK-2 after failure = %+v, want ERROR with MOB-0111", failed)
	}
}

func TestPrinterStatus_RequiresPrintDuty(t *testing.T) {
	h := router(newMemStore(), &fakeSender{})
	if rec := call(t, h, http.MethodGet, "/print/v1/printers/status", "", "", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("no duty: status %d, want 403", rec.Code)
	}
}

func TestCreateJob_AttemptsPerDeviceOption(t *testing.T) {
	cases := []struct {
		attempts     int
		wantAttempts int
	}{
		{1, 2}, // PRINT_RETRY=0: one try on DOCK-1, then DOCK-2
		{4, 5}, // PRINT_RETRY=3: four tries on DOCK-1, then DOCK-2
		{0, 3}, // invalid value keeps the default of two tries per device
	}
	for _, c := range cases {
		sender := &fakeSender{down: map[string]bool{"DOCK-1": true}}
		h := router(newMemStore(), sender, api.WithAttemptsPerDevice(c.attempts))
		seedDock(t, h)
		j := decodeJob(t, call(t, h, http.MethodPost, "/print/v1/jobs", lpBody, "print.job.submit", "retry"))
		if j.Status != domain.JobPrinted || j.PrinterCode != "DOCK-2" || j.Attempts != c.wantAttempts {
			t.Fatalf("attempts option %d: job = %+v, want DOCK-2 after %d attempts", c.attempts, j, c.wantAttempts)
		}
	}
}

func TestPurgeJobs_RemovesOnlyFinishedRowsOlderThanRetention(t *testing.T) {
	ms := newMemStore()
	h := router(ms, &fakeSender{down: map[string]bool{}}, api.WithRetentionDays(1))
	seedDock(t, h)
	old := decodeJob(t, call(t, h, http.MethodPost, "/print/v1/jobs", lpBody, "print.job.submit", "purge-old"))
	fresh := decodeJob(t, call(t, h, http.MethodPost, "/print/v1/jobs", lpBody, "print.job.submit", "purge-fresh"))
	ms.backdate(old.ID, time.Now().AddDate(0, 0, -2))

	if rec := call(t, h, http.MethodPost, "/print/v1/jobs:purge", "", "print.job.submit", "purge-x"); rec.Code != http.StatusForbidden {
		t.Fatalf("purge without admin duty: status %d, want 403", rec.Code)
	}
	if rec := call(t, h, http.MethodPost, "/print/v1/jobs:purge", "", "print.admin", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("purge without key: status %d, want 400", rec.Code)
	}
	rec := call(t, h, http.MethodPost, "/print/v1/jobs:purge", "", "print.admin", "purge-1")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"deleted":1`) || !strings.Contains(rec.Body.String(), `"retentionDays":1`) {
		t.Fatalf("purge status %d body %s", rec.Code, rec.Body)
	}
	if rec := call(t, h, http.MethodGet, "/print/v1/jobs/"+old.ID, "", "print.job.submit", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("purged job still readable: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodGet, "/print/v1/jobs/"+fresh.ID, "", "print.job.submit", ""); rec.Code != http.StatusOK {
		t.Fatalf("fresh job purged: %d", rec.Code)
	}
	again := call(t, h, http.MethodPost, "/print/v1/jobs:purge", "", "print.admin", "purge-2")
	if again.Code != http.StatusOK || !strings.Contains(again.Body.String(), `"deleted":0`) {
		t.Fatalf("second purge status %d body %s", again.Code, again.Body)
	}
}

func TestPurgeJobs_DefaultRetentionKeepsRecentJobs(t *testing.T) {
	ms := newMemStore()
	h := router(ms, &fakeSender{down: map[string]bool{}})
	seedDock(t, h)
	old := decodeJob(t, call(t, h, http.MethodPost, "/print/v1/jobs", lpBody, "print.job.submit", "keep-old"))
	ms.backdate(old.ID, time.Now().AddDate(0, 0, -29))
	rec := call(t, h, http.MethodPost, "/print/v1/jobs:purge", "", "print.admin", "keep-1")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"deleted":0`) || !strings.Contains(rec.Body.String(), `"retentionDays":30`) {
		t.Fatalf("purge status %d body %s", rec.Code, rec.Body)
	}
}

func TestMetrics_CountsPrintedFailedAndPurged(t *testing.T) {
	ms := newMemStore()
	sender := &fakeSender{down: map[string]bool{}}
	a := api.New(ms, sender, api.WithRetentionDays(1))
	r := newRouterFor(a)
	seedDock(t, r)
	ok := decodeJob(t, call(t, r, http.MethodPost, "/print/v1/jobs", lpBody, "print.job.submit", "m-1"))
	call(t, r, http.MethodPost, "/print/v1/jobs", lpBody, "print.job.submit", "m-1") // replay: not counted
	sender.down["DOCK-1"], sender.down["DOCK-2"] = true, true
	call(t, r, http.MethodPost, "/print/v1/jobs", lpBody, "print.job.submit", "m-2")
	ms.backdate(ok.ID, time.Now().AddDate(0, 0, -2))
	call(t, r, http.MethodPost, "/print/v1/jobs:purge", "", "print.admin", "m-3")

	rec := httptestGet(a.Metrics(), "/metrics")
	for _, want := range []string{"print_jobs_printed_total 1\n", "print_jobs_failed_total 1\n", "print_jobs_purged_total 1\n"} {
		if !strings.Contains(rec, want) {
			t.Fatalf("metrics missing %q:\n%s", want, rec)
		}
	}
}
