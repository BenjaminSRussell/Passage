package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
)

const (
	// oeeStreamPoll is how often the live tile stream recomputes the current bucket. It
	// keeps a tile within 5 s of the stored machine events (AC-25.4).
	oeeStreamPoll = 2 * time.Second
	// oeeStreamLife ends a stream before the router's request timeout cancels it; the
	// browser's EventSource reconnects after the retry hint, so a tile stays live.
	oeeStreamLife = httpx.RequestTimeout - 5*time.Second
	// oeeStreamRetryMs is the reconnect delay sent to the browser.
	oeeStreamRetryMs = 1000
)

// oeeTile is one event of the live OEE stream: the current bucket of one resource.
// Bucket is null while the bucket has no planned time (machine off, no known state,
// or wholly planned downtime) or the resource has no machine.
type oeeTile struct {
	ResourceID  string    `json:"resourceId"`
	BucketStart time.Time `json:"bucketStart"`
	BucketEnd   time.Time `json:"bucketEnd"`
	ComputedAt  time.Time `json:"computedAt"`
	Bucket      *oeeItem  `json:"bucket"`
}

// oeeStream serves the live OEE tile (FR-25.14, AC-25.4) as Server-Sent Events: an
// `oee` event with the resource's current bucket at once, then again whenever the
// bucket changes, recomputed from the stored machine events every poll. A new bucket
// period sends an event even when nothing else changed. The stream ends before the
// request timeout with a retry hint, so the browser reconnects.
func (a *API) oeeStream(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	resourceID := q.Get("resourceId")
	if !ids.Valid(resourceID) {
		fields["resourceId"] = "must be a UUID"
	}
	bucket := q.Get("bucket")
	if bucket == "" {
		bucket = "15m"
	}
	width, ok := oeeBuckets[bucket]
	if !ok {
		fields["bucket"] = "must be 5m, 15m or 60m"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	rc := http.NewResponseController(w)
	ctx := r.Context()
	// Compute once before the headers go out, so a store failure is still a problem response.
	first, err := a.oeeTile(ctx, resourceID, width)
	if err != nil {
		a.fail(w, r, "load oee stream", err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if _, err := fmt.Fprintf(w, "retry: %d\n\n", oeeStreamRetryMs); err != nil {
		return
	}
	poll, life := a.streamPoll, a.streamLife
	if poll <= 0 {
		poll = oeeStreamPoll
	}
	if life <= 0 {
		life = oeeStreamLife
	}
	end := time.NewTimer(life)
	defer end.Stop()
	tick := time.NewTicker(poll)
	defer tick.Stop()
	var last []byte
	tile := first
	for {
		raw, err := json.Marshal(tile)
		if err != nil {
			slog.ErrorContext(ctx, "encode oee tile", "err", err)
			return
		}
		if !bytes.Equal(withoutComputedAt(tile), last) {
			last = withoutComputedAt(tile)
			if _, err := fmt.Fprintf(w, "event: oee\ndata: %s\n\n", raw); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-end.C:
			return
		case <-tick.C:
		}
		if tile, err = a.oeeTile(ctx, resourceID, width); err != nil {
			if ctx.Err() == nil {
				slog.ErrorContext(ctx, "load oee stream", "resourceId", resourceID, "err", err)
				_, _ = fmt.Fprint(w, "event: error\ndata: {\"code\":\"SF-0500\"}\n\n")
				_ = rc.Flush()
			}
			return
		}
	}
}

// withoutComputedAt is the tile's identity for change detection: the same bucket
// recomputed later is not a change.
func withoutComputedAt(t oeeTile) []byte {
	t.ComputedAt = time.Time{}
	raw, _ := json.Marshal(t)
	return raw
}

// oeeTile computes the resource's bucket that holds now, exactly as GET oee does.
func (a *API) oeeTile(ctx context.Context, resourceID string, width time.Duration) (oeeTile, error) {
	now := a.now().UTC()
	from := now.Truncate(width)
	to := from.Add(width)
	tile := oeeTile{ResourceID: resourceID, BucketStart: from, BucketEnd: to, ComputedAt: now}
	streams, err := a.store.LoadOEEStreams(ctx, []string{resourceID}, from, to)
	if err != nil {
		return tile, err
	}
	s, ok := streams[resourceID]
	if !ok {
		return tile, nil
	}
	buckets, err := domain.BuildBuckets(withHorizon(s, now), from, to, width)
	if err != nil {
		return tile, err
	}
	for _, b := range buckets {
		item := toOEEItem(resourceID, b)
		tile.Bucket = &item
	}
	return tile, nil
}

func toOEEItem(rid string, b domain.OEEBucket) oeeItem {
	return oeeItem{
		ResourceID: rid, BucketStart: b.Start,
		PlannedMin: b.PlannedMinutes, RunMin: b.RunMinutes, DownMin: b.PlannedMinutes.Sub(b.RunMinutes),
		TotalCount: b.TotalCount, GoodCount: b.GoodCount,
		Availability: b.Availability, Performance: b.Performance, Quality: b.Quality, OEE: b.OEE.OEE,
		OverRate: b.OverRate, SourceEvents: b.SourceEvents,
	}
}
