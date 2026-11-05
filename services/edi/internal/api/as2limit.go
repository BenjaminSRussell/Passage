package api

import (
	"sync"
	"time"
)

// DefaultAS2LimitKeys bounds how many endpoint buckets the AS2 limiter keeps.
const DefaultAS2LimitKeys = 10000

// AS2Limiter is a token bucket per AS2 endpoint id for the AS2 listener
// (EDI_AS2_RATE_PER_MINUTE, EDI_AS2_RATE_BURST): each endpoint may send burst
// requests at once and perMinute on average. Memory is bounded: at most
// maxKeys buckets are kept, and when the map is full the buckets that have
// refilled (which hold no state a fresh bucket would not) are dropped first,
// otherwise one bucket chosen at random. Evicting a bucket only ever resets
// it to full, so a flood of made-up endpoint ids cannot lock a partner out;
// the edge rate limit per client address bounds that flood.
type AS2Limiter struct {
	mu        sync.Mutex
	rate      float64 // tokens per second
	burst     float64
	maxKeys   int
	now       func() time.Time
	buckets   map[string]*as2Bucket
	lastSweep time.Time
}

type as2Bucket struct {
	tokens  float64
	at      time.Time
	limited bool
}

// NewAS2Limiter returns a limiter of perMinute requests per endpoint with the
// given burst, or nil (no limit) when perMinute is not positive. maxKeys of
// zero or less is DefaultAS2LimitKeys.
func NewAS2Limiter(perMinute, burst, maxKeys int) *AS2Limiter {
	if perMinute <= 0 {
		return nil
	}
	if burst < 1 {
		burst = 1
	}
	if maxKeys <= 0 {
		maxKeys = DefaultAS2LimitKeys
	}
	return &AS2Limiter{rate: float64(perMinute) / 60, burst: float64(burst), maxKeys: maxKeys, now: time.Now,
		buckets: make(map[string]*as2Bucket)}
}

// Allow takes one token from key's bucket. When there is none it returns
// false with the wait until the next token; first is true on the first
// refusal since the bucket last admitted a request, for logging once.
func (l *AS2Limiter) Allow(key string) (ok bool, retryAfter time.Duration, first bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b := l.buckets[key]
	if b == nil {
		l.makeRoom(now)
		b = &as2Bucket{tokens: l.burst, at: now}
		l.buckets[key] = b
	}
	l.refill(b, now)
	if b.tokens >= 1 {
		b.tokens--
		b.limited = false
		return true, 0, false
	}
	first = !b.limited
	b.limited = true
	wait := time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
	if wait < time.Second {
		wait = time.Second
	}
	return false, wait, first
}

func (l *AS2Limiter) refill(b *as2Bucket, now time.Time) {
	if el := now.Sub(b.at).Seconds(); el > 0 {
		b.tokens = min(l.burst, b.tokens+el*l.rate)
		b.at = now
	}
}

// makeRoom keeps the map below maxKeys before a new bucket is added: at most
// once a second it drops every refilled bucket; if that frees nothing it
// drops one bucket (map order is random).
func (l *AS2Limiter) makeRoom(now time.Time) {
	if len(l.buckets) < l.maxKeys {
		return
	}
	if now.Sub(l.lastSweep) >= time.Second {
		l.lastSweep = now
		full := time.Duration(l.burst / l.rate * float64(time.Second))
		for k, b := range l.buckets {
			if now.Sub(b.at) >= full {
				delete(l.buckets, k)
			}
		}
	}
	for k := range l.buckets {
		if len(l.buckets) < l.maxKeys {
			return
		}
		delete(l.buckets, k)
	}
}

// size reports how many buckets are kept (tests).
func (l *AS2Limiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}
