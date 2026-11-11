package httpx

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Region fence (PSG-50, BR-50.2, OPS-1703). While this region is FENCED every request is refused with 503 and
// Retry-After, so a fenced region serves no reads or writes while DNS and the failover workflow move traffic.
//
// The fence is off unless one source is configured, so a service that sets nothing behaves exactly as before:
//
//	PASSAGE_REGION_FENCE_FILE  a file holding the region role (FENCED, PRIMARY, PASSIVE), for example the
//	                           passage-region ConfigMap's REGION_ROLE key mounted as a file. Missing file: not fenced.
//	PASSAGE_REGION_FENCE_URL   the ops service's /healthz/region of this region; its JSON body names the role.
//	PASSAGE_REGION_FENCE_INTERVAL  poll interval, default 5s (minimum 1s).
//
// The role is read by one background goroutine every interval and cached in an atomic, so a request never waits on
// the file or the network. A source that cannot be read keeps the last known state (not fenced at start): an
// unreadable source must not take a healthy region down. /healthz, /readyz, /healthz/region and /metrics stay
// reachable while fenced so probes, DNS checks and scrapes still work.
const (
	FenceFileEnv     = "PASSAGE_REGION_FENCE_FILE"
	FenceURLEnv      = "PASSAGE_REGION_FENCE_URL"
	FenceIntervalEnv = "PASSAGE_REGION_FENCE_INTERVAL"

	// CodeRegionFenced is OPS-1703: the region is fenced and serves nothing until the failover completes.
	CodeRegionFenced = "OPS-1703"
	// FenceRetryAfterSeconds is the Retry-After value of the 503.
	FenceRetryAfterSeconds = 30

	defaultFenceInterval = 5 * time.Second
	minFenceInterval     = time.Second
	fenceReadLimit       = 64 << 10
)

// FenceSource reads whether the region is fenced. It may block; the Fence calls it from a background goroutine only.
type FenceSource func(ctx context.Context) (fenced bool, err error)

// Fence caches a region's fenced state for the middleware.
type Fence struct {
	fenced atomic.Bool
	known  atomic.Bool
}

// NewFence builds a fence over src and polls it every interval (minimum 1s) until ctx ends. The first read happens
// before NewFence returns, bounded by a 2s timeout, so a pod that starts inside a fenced region fences at once.
func NewFence(ctx context.Context, src FenceSource, interval time.Duration) *Fence {
	if interval < minFenceInterval {
		interval = minFenceInterval
	}
	f := &Fence{}
	f.refresh(ctx, src)
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				f.refresh(ctx, src)
			}
		}
	}()
	return f
}

func (f *Fence) refresh(ctx context.Context, src FenceSource) {
	rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	fenced, err := src(rctx)
	if err != nil {
		if f.known.CompareAndSwap(false, true) {
			slog.Warn("region fence source unreadable; serving as not fenced until it can be read", "err", err)
		}
		return
	}
	f.known.Store(true)
	if f.fenced.Swap(fenced) != fenced {
		slog.Warn("region fence changed", "fenced", fenced)
	}
}

// Fenced reports the cached state.
func (f *Fence) Fenced() bool { return f != nil && f.fenced.Load() }

// FenceExempt reports whether path stays reachable in a fenced region.
func FenceExempt(path string) bool {
	switch path {
	case "/healthz", "/readyz", "/healthz/region", "/metrics":
		return true
	}
	return false
}

// Middleware answers 503 with Retry-After while the fence is on. A nil Fence passes everything through.
func (f *Fence) Middleware(next http.Handler) http.Handler {
	if f == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.fenced.Load() && !FenceExempt(r.URL.Path) {
			w.Header().Set("Retry-After", strconv.Itoa(FenceRetryAfterSeconds))
			Fail(w, r, NewProblem(http.StatusServiceUnavailable, CodeRegionFenced, "this region is fenced for a failover; retry against the new primary region"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RoleFenced maps a role string to the fenced state: only FENCED fences.
func RoleFenced(role string) bool { return strings.EqualFold(strings.TrimSpace(role), "FENCED") }

// FileFenceSource reads the role from a file. A missing file is "not fenced".
func FileFenceSource(path string) FenceSource {
	return func(context.Context) (bool, error) {
		b, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return RoleFenced(string(b)), nil
	}
}

// URLFenceSource reads {"role": "..."} from url. The ops /healthz/region answers 503 for a PASSIVE or fenced region, so
// the body is read whatever the status; a body without a role is an error (keep the last state).
func URLFenceSource(client *http.Client, url string) FenceSource {
	return func(ctx context.Context) (bool, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return false, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return false, err
		}
		defer func() { _ = resp.Body.Close() }()
		var body struct {
			Role string `json:"role"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, fenceReadLimit)).Decode(&body); err != nil {
			return false, err
		}
		if body.Role == "" {
			return false, io.ErrUnexpectedEOF
		}
		return RoleFenced(body.Role), nil
	}
}

// FenceFromEnv builds the fence the environment configures, or nil when none is set.
func FenceFromEnv(ctx context.Context, getenv func(string) string) *Fence {
	var src FenceSource
	switch {
	case getenv(FenceFileEnv) != "":
		src = FileFenceSource(getenv(FenceFileEnv))
	case getenv(FenceURLEnv) != "":
		src = URLFenceSource(&http.Client{Timeout: 2 * time.Second}, getenv(FenceURLEnv))
	default:
		return nil
	}
	interval := defaultFenceInterval
	if v := getenv(FenceIntervalEnv); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			interval = d
		}
	}
	return NewFence(ctx, src, interval)
}

var (
	processFenceOnce sync.Once
	processFence     *Fence
	fenceDisabled    atomic.Bool
)

// DisableFence opts the process out of the fence. The ops service calls it: it must answer in a fenced region to
// run the rejoin. Call it before the first router is built.
func DisableFence() { fenceDisabled.Store(true) }

// processFenceMiddleware is the fence every router of this process shares, built once from the environment.
func processFenceMiddleware(next http.Handler) http.Handler {
	if fenceDisabled.Load() {
		return next
	}
	processFenceOnce.Do(func() { processFence = FenceFromEnv(context.Background(), os.Getenv) })
	return processFence.Middleware(next)
}
