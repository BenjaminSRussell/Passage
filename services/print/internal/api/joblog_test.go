package api_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
)

func TestListJobs_NewestFirstWithFilters(t *testing.T) {
	sender := &fakeSender{down: map[string]bool{}}
	h := router(newMemStore(), sender)
	seedDock(t, h)
	first := decodeJob(t, call(t, h, http.MethodPost, "/print/v1/jobs", lpBody, "print.job.submit", "log-1"))
	sender.down["DOCK-1"], sender.down["DOCK-2"] = true, true
	failed := decodeJob(t, call(t, h, http.MethodPost, "/print/v1/jobs", lpBody, "print.job.submit", "log-2"))

	list := func(query string) []domain.Job {
		t.Helper()
		rec := call(t, h, http.MethodGet, "/print/v1/jobs"+query, "", "print.job.submit", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", query, rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), "^XA") {
			t.Fatal("ZPL leaked in the job log")
		}
		var body struct{ Items []domain.Job }
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Items
	}
	if all := list(""); len(all) != 2 || all[0].ID != failed.ID || all[1].ID != first.ID {
		t.Fatalf("log order = %+v", all)
	}
	if only := list("?status=FAILED"); len(only) != 1 || only[0].LastError != domain.CodeUnreachable {
		t.Fatalf("failed filter = %+v", only)
	}
	if byPrinter := list("?printerCode=DOCK-1"); len(byPrinter) != 1 || byPrinter[0].ID != first.ID {
		t.Fatalf("printer filter = %+v", byPrinter)
	}
	if one := list("?limit=1"); len(one) != 1 {
		t.Fatalf("limit = %d", len(one))
	}
}

func TestListJobs_RejectsBadFiltersAndNeedsDuty(t *testing.T) {
	h := router(newMemStore(), &fakeSender{down: map[string]bool{}})
	for _, c := range []struct{ query, field string }{
		{"?status=DONE", "status"},
		{"?queueCode=dock", "queueCode"},
		{"?limit=0", "limit"},
		{"?limit=501", "limit"},
	} {
		rec := call(t, h, http.MethodGet, "/print/v1/jobs"+c.query, "", "print.job.submit", "")
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"`+c.field+`"`) {
			t.Errorf("%s: %d %s", c.query, rec.Code, rec.Body)
		}
	}
	if rec := call(t, h, http.MethodGet, "/print/v1/jobs", "", "", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("no duty: %d", rec.Code)
	}
}

// A ZPL label never goes to a PDF device even when it ranks first in the queue.
func TestCreateJob_SkipsDevicesThatCannotPrintZPL(t *testing.T) {
	sender := &fakeSender{down: map[string]bool{}}
	h := router(newMemStore(), sender)
	pdf := `{"code":"OFFICE-PDF","name":"Office","deviceType":"PDF_IPP","dpi":600,"host":"10.0.7.1","port":631,"queueCode":"DOCK","failoverRank":1}`
	if rec := call(t, h, http.MethodPost, "/print/v1/printers", pdf, "print.admin", "pdf"); rec.Code != http.StatusCreated {
		t.Fatalf("register pdf: %d %s", rec.Code, rec.Body)
	}
	zpl := `{"code":"DOCK-Z","name":"Dock","deviceType":"ZPL_II","dpi":203,"host":"10.0.7.2","queueCode":"DOCK","failoverRank":2}`
	if rec := call(t, h, http.MethodPost, "/print/v1/printers", zpl, "print.admin", "zpl"); rec.Code != http.StatusCreated {
		t.Fatalf("register zpl: %d %s", rec.Code, rec.Body)
	}
	j := decodeJob(t, call(t, h, http.MethodPost, "/print/v1/jobs", lpBody, "print.job.submit", "zpl-only"))
	if j.Status != domain.JobPrinted || j.PrinterCode != "DOCK-Z" || j.Attempts != 1 || len(sender.sent) != 1 {
		t.Fatalf("job = %+v sent = %v", j, sender.sent)
	}
}

func TestReadyz_FailsClosedWhenDatabaseIsDown(t *testing.T) {
	s := newMemStore()
	h := router(s, &fakeSender{})
	get := func() int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		return rec.Code
	}
	if code := get(); code != http.StatusOK {
		t.Fatalf("ready: %d", code)
	}
	s.pingErr = errors.New("connection refused")
	if code := get(); code != http.StatusServiceUnavailable {
		t.Fatalf("database down: %d, want 503", code)
	}
}
