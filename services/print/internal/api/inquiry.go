package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
)

// printerStatuses serves GET /print/v1/printers/status (§6.32.14 printer status).
func (a *API) printerStatuses(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	items, err := a.store.PrinterStatuses(r.Context())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Items []domain.PrinterStatus `json:"items"`
	}{Items: items})
}

// purgeResult reports how many job log rows were removed and the cutoff used.
type purgeResult struct {
	Deleted int64     `json:"deleted"`
	Cutoff  time.Time `json:"cutoff"`
	Days    int       `json:"retentionDays"`
}

// purgeJobs serves POST /print/v1/jobs:purge. It removes the tenant's finished job log
// rows older than PRINT_JOB_RETENTION_DAYS (§6.32.19). Repeating the call is harmless:
// a second run finds nothing older than the cutoff, so the key is required but not stored.
func (a *API) purgeJobs(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	if _, ok := requireKey(w, r); !ok {
		return
	}
	cutoff := domain.RetentionCutoff(time.Now(), a.retentionDays)
	deleted, err := a.store.PurgeJobs(r.Context(), cutoff)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.purged.Add(deleted)
	httpx.JSON(w, http.StatusOK, purgeResult{Deleted: deleted, Cutoff: cutoff, Days: a.retentionDays})
}

// Metrics serves the Prometheus text format on the admin port. The print failure alert
// (§6.32.10, print failures > 5 per 15 min) reads print_jobs_failed_total.
func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# HELP print_jobs_printed_total Print jobs that a printer accepted; idempotent replays are not counted.\n# TYPE print_jobs_printed_total counter\nprint_jobs_printed_total %d\n", a.printed.Load())
		fmt.Fprintf(w, "# HELP print_jobs_failed_total Print jobs that failed with MOB-0111 or MOB-0112.\n# TYPE print_jobs_failed_total counter\nprint_jobs_failed_total %d\n", a.failed.Load())
		fmt.Fprintf(w, "# HELP print_jobs_purged_total Finished job log rows removed by the retention purge.\n# TYPE print_jobs_purged_total counter\nprint_jobs_purged_total %d\n", a.purged.Load())
	})
}
