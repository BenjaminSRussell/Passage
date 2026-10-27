package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

const (
	// maxRollupSpan bounds one rollup call; the job runs every 15 minutes over
	// the last hour or so, and a backfill is a sequence of calls.
	maxRollupSpan = 24 * time.Hour
	// maxRollupResources bounds the resources of one call, explicit or all machines.
	maxRollupResources = 1000
)

type oeeRollupRequest struct {
	ResourceIDs []string `json:"resourceIds"`
	From        string   `json:"from"`
	To          string   `json:"to"`
	Bucket      string   `json:"bucket"`
}

// withHorizon bounds a stream whose history after the window is unknown at now:
// a RUNNING machine with no count since then is idle up to now (BR-25.6), and
// time after now is not scheduled.
func withHorizon(s domain.OEEStream, now time.Time) domain.OEEStream {
	if s.Horizon.IsZero() {
		s.Horizon = now
	}
	return s
}

// parseRollup validates the rollup window: RFC 3339 instants, the start on a
// bucket boundary (UTC), a whole number of buckets, at most 24 hours, and not
// ending after now (a bucket is stored once its time has passed).
func parseRollup(req oeeRollupRequest, now time.Time) (resourceIDs []string, from, to time.Time, width time.Duration, fields map[string]string) {
	fields = map[string]string{}
	seen := map[string]bool{}
	for _, id := range req.ResourceIDs {
		if !ids.Valid(id) {
			fields["resourceIds"] = "must be UUIDs"
			break
		}
		if !seen[id] {
			seen[id] = true
			resourceIDs = append(resourceIDs, id)
		}
	}
	if len(resourceIDs) > maxRollupResources {
		fields["resourceIds"] = fmt.Sprintf("must hold at most %d UUIDs", maxRollupResources)
	}
	from, errFrom := time.Parse(time.RFC3339, req.From)
	to, errTo := time.Parse(time.RFC3339, req.To)
	if errFrom != nil {
		fields["from"] = "must be an RFC 3339 timestamp"
	}
	if errTo != nil {
		fields["to"] = "must be an RFC 3339 timestamp"
	}
	bucket := req.Bucket
	if bucket == "" {
		bucket = "15m"
	}
	width, ok := oeeBuckets[bucket]
	if !ok {
		fields["bucket"] = "must be 5m, 15m or 60m"
	}
	if errFrom == nil && errTo == nil {
		from, to = from.UTC(), to.UTC()
		switch {
		case !to.After(from):
			fields["to"] = "must be after from"
		case to.Sub(from) > maxRollupSpan:
			fields["to"] = "window must be at most 24 hours"
		case to.After(now):
			fields["to"] = "must not be in the future"
		case ok && !from.Truncate(width).Equal(from):
			fields["from"] = "must start on a bucket boundary (UTC)"
		case ok && to.Sub(from)%width != 0:
			fields["to"] = "window must be a whole number of buckets"
		}
	}
	return resourceIDs, from, to, width, fields
}

// rollupOEE is the PRD job "OEE rollup" (every 15 minutes, FR-25.10): it
// computes the buckets of the window from machine events, as GET oee does, and
// upserts them by (resource, width, start). The platform scheduler calls it per
// tenant with a service identity; calling it again is safe, so it takes no
// Idempotency-Key. Without resourceIds it rolls up every registered machine.
func (a *API) rollupOEE(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req oeeRollupRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	now := a.now().UTC()
	resourceIDs, from, to, width, fields := parseRollup(req, now)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	ctx := r.Context()
	if len(resourceIDs) == 0 {
		machines, err := a.store.ListMachines(ctx, maxRollupResources)
		if err != nil {
			a.fail(w, r, "list machines for oee rollup", err)
			return
		}
		for _, m := range machines {
			resourceIDs = append(resourceIDs, m.ResourceID)
		}
	}
	minutes := int(width / time.Minute)
	rollup := store.OEERollup{ResourceIDs: resourceIDs, BucketMinutes: minutes, From: from, To: to}
	if len(resourceIDs) > 0 {
		streams, err := a.store.LoadOEEStreams(ctx, resourceIDs, from, to)
		if err != nil {
			a.fail(w, r, "load oee streams", err)
			return
		}
		for _, rid := range resourceIDs {
			s, ok := streams[rid]
			if !ok {
				continue
			}
			buckets, err := domain.BuildBuckets(withHorizon(s, now), from, to, width)
			if err != nil {
				a.fail(w, r, "build oee buckets", err)
				return
			}
			for _, b := range buckets {
				rollup.Rows = append(rollup.Rows, store.OEERow{ResourceID: rid, IdealCycleSeconds: s.IdealCycleSeconds, Bucket: b})
			}
		}
	}
	out, err := a.store.SaveOEEBuckets(ctx, rollup)
	if err != nil {
		a.fail(w, r, "save oee buckets", err)
		return
	}
	a.oeeChanged.Add(int64(out.Changed))
	a.oeeRemoved.Add(int64(out.Removed))
	httpx.JSON(w, http.StatusOK, out)
}
