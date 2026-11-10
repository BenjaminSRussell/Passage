package inbox

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	DefaultSourceTimeout = 3 * time.Second
	DefaultConcurrency   = 4
	DefaultLimit         = 50
	MaxLimit             = 200
	MaxPerSource         = 100

	reasonTimeout    = "did not answer in time"
	reasonFailed     = "could not be read right now"
	reasonNotSetUp   = "is not set up"
	cursorFieldCount = 3
	cursorTimeLayout = time.RFC3339Nano
	cursorFieldSplit = "|"
)

var (
	ErrNotConfigured = errors.New("source is not configured")
	ErrBadCursor     = errors.New("cursor is not from this inbox")
)

type Item struct {
	ID        string    `json:"id"`
	Source    string    `json:"source"`
	Kind      string    `json:"kind"`
	Reason    string    `json:"reason"`
	Number    string    `json:"number"`
	Link      string    `json:"link"`
	Actions   []string  `json:"actions"`
	CreatedAt time.Time `json:"createdAt"`
}

type Fetched struct {
	Items []Item
	More  bool
}

type Source interface {
	Name() string
	Duty() string
	Fetch(ctx context.Context, p tenant.Principal, legalEntity string) (Fetched, error)
}

type Unavailable struct {
	Source string `json:"source"`
	Reason string `json:"reason"`
}

type Page struct {
	Items       []Item        `json:"items"`
	NextCursor  string        `json:"nextCursor,omitempty"`
	Unavailable []Unavailable `json:"unavailable"`
	Truncated   []string      `json:"truncated"`
}

type Aggregator struct {
	Sources       []Source
	SourceTimeout time.Duration
	Concurrency   int
	failures      atomic.Int64
}

func (a *Aggregator) Failures() int64 { return a.failures.Load() }

type outcome struct {
	fetched Fetched
	failure *Unavailable
}

func (a *Aggregator) Page(ctx context.Context, p tenant.Principal, legalEntity string, limit int, cursor string) (Page, error) {
	after, err := decodeCursor(cursor)
	if err != nil {
		return Page{}, err
	}
	entitled := slices.DeleteFunc(slices.Clone(a.Sources), func(s Source) bool { return !p.HasDuty(s.Duty()) })
	outcomes := make([]outcome, len(entitled))
	slots := make(chan struct{}, max(a.Concurrency, 1))
	var wg sync.WaitGroup
	for i, src := range entitled {
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer func() { <-slots; wg.Done() }()
			outcomes[i] = a.fetch(ctx, src, p, legalEntity)
		}()
	}
	wg.Wait()

	page := Page{Items: []Item{}, Unavailable: []Unavailable{}, Truncated: []string{}}
	var all []Item
	for i, o := range outcomes {
		switch {
		case o.failure != nil:
			page.Unavailable = append(page.Unavailable, *o.failure)
		default:
			all = append(all, o.fetched.Items...)
			if o.fetched.More {
				page.Truncated = append(page.Truncated, entitled[i].Name())
			}
		}
	}
	slices.SortFunc(all, compare)
	if after != nil {
		i := slices.IndexFunc(all, func(it Item) bool { return compare(it, *after) > 0 })
		if i < 0 {
			i = len(all)
		}
		all = all[i:]
	}
	if len(all) > limit {
		all = all[:limit]
		page.NextCursor = encodeCursor(all[limit-1])
	}
	page.Items = append(page.Items, all...)
	return page, nil
}

func (a *Aggregator) fetch(ctx context.Context, src Source, p tenant.Principal, legalEntity string) outcome {
	timeout := a.SourceTimeout
	if timeout <= 0 {
		timeout = DefaultSourceTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	fetched, err := src.Fetch(ctx, p, legalEntity)
	if err == nil {
		return outcome{fetched: fetched}
	}
	a.failures.Add(1)
	slog.WarnContext(ctx, "inbox source unavailable", "source", src.Name(), "err", err)
	reason := reasonFailed
	switch {
	case errors.Is(err, ErrNotConfigured):
		reason = reasonNotSetUp
	case errors.Is(err, context.DeadlineExceeded):
		reason = reasonTimeout
	}
	return outcome{failure: &Unavailable{Source: src.Name(), Reason: reason}}
}

func compare(a, b Item) int {
	if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
		return c
	}
	if c := strings.Compare(a.Source, b.Source); c != 0 {
		return c
	}
	return strings.Compare(a.ID, b.ID)
}

func encodeCursor(it Item) string {
	raw := strings.Join([]string{it.CreatedAt.UTC().Format(cursorTimeLayout), it.Source, it.ID}, cursorFieldSplit)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(cursor string) (*Item, error) {
	if cursor == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return nil, ErrBadCursor
	}
	parts := strings.SplitN(string(raw), cursorFieldSplit, cursorFieldCount)
	if len(parts) != cursorFieldCount {
		return nil, ErrBadCursor
	}
	at, err := time.Parse(cursorTimeLayout, parts[0])
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadCursor, err)
	}
	return &Item{CreatedAt: at, Source: parts[1], ID: parts[2]}, nil
}
