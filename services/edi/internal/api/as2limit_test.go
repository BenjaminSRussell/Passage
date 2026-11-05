package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func testLimiter(perMinute, burst, keys int) (*AS2Limiter, *fakeClock) {
	c := &fakeClock{t: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}
	l := NewAS2Limiter(perMinute, burst, keys)
	l.now = c.now
	return l, c
}

func TestAS2Limiter_BurstThenRateWithRetryAfter(t *testing.T) {
	l, c := testLimiter(60, 3, 0)
	for i := range 3 {
		if ok, _, _ := l.Allow("a"); !ok {
			t.Fatalf("request %d of the burst refused", i+1)
		}
	}
	ok, wait, first := l.Allow("a")
	if ok || !first || wait != time.Second {
		t.Fatalf("fourth: ok=%v wait=%v first=%v", ok, wait, first)
	}
	if ok, _, first := l.Allow("a"); ok || first {
		t.Fatalf("fifth: ok=%v first=%v (first refusal is logged once)", ok, first)
	}
	// Another endpoint has its own bucket.
	if ok, _, _ := l.Allow("b"); !ok {
		t.Fatal("other endpoint refused")
	}
	// One token a second at 60 a minute.
	c.t = c.t.Add(time.Second)
	if ok, _, _ := l.Allow("a"); !ok {
		t.Fatal("refilled token refused")
	}
	if ok, _, _ := l.Allow("a"); ok {
		t.Fatal("second token in the same second admitted")
	}
	// A slow rate gives a long Retry-After.
	slow, _ := testLimiter(2, 1, 0)
	slow.Allow("a")
	if _, wait, _ := slow.Allow("a"); wait != 30*time.Second {
		t.Fatalf("2 a minute: wait %v", wait)
	}
	if NewAS2Limiter(0, 10, 0) != nil {
		t.Fatal("0 a minute is no limiter")
	}
}

func TestAS2Limiter_MemoryIsBounded(t *testing.T) {
	l, _ := testLimiter(60, 2, 100)
	for i := range 1000 {
		l.Allow(fmt.Sprintf("k%d", i))
		if n := l.size(); n > 100 {
			t.Fatalf("%d buckets after %d keys", n, i+1)
		}
	}
	// Refilled buckets go first: at 60 a minute with a burst of 2 an idle
	// bucket is full again after 2s, so a bucket drained just now survives.
	l2, c := testLimiter(60, 2, 3)
	l2.Allow("idle1")
	l2.Allow("idle2")
	c.t = c.t.Add(2500 * time.Millisecond)
	l2.Allow("busy")
	l2.Allow("busy")
	c.t = c.t.Add(500 * time.Millisecond)
	l2.Allow("new")
	if _, ok := l2.buckets["busy"]; !ok || l2.size() != 2 {
		t.Fatalf("after the sweep: %v", l2.buckets)
	}
	if ok, _, _ := l2.Allow("busy"); ok {
		t.Fatal("the drained bucket was reset")
	}
}

func TestAS2Routes_RateLimitAnswers429BeforeAnyWork(t *testing.T) {
	a := New(nil) // no store: any work past the limiter would panic
	a.AS2Limit = NewAS2Limiter(60, 1, 0)
	endpoint := "0195f0a0-0000-7000-8000-0000000000aa"
	a.AS2Limit.Allow(endpoint) // the partner used its burst
	h := a.AS2Routes()
	for _, path := range []string{"/as2/v1/" + endpoint, "/as2/v1/" + endpoint + "/mdn"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader("x")))
		if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "1" {
			t.Fatalf("%s: %d Retry-After %q", path, rec.Code, rec.Header().Get("Retry-After"))
		}
	}
	// A malformed endpoint id gets no bucket and is a plain 404.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/as2/v1/not-an-id", strings.NewReader("x")))
	if rec.Code != http.StatusNotFound || a.AS2Limit.size() != 1 {
		t.Fatalf("malformed id: %d, %d buckets", rec.Code, a.AS2Limit.size())
	}
}
