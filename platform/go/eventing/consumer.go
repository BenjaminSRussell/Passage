package eventing

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	DefaultMaxAttempts = 10
	baseBackoff        = time.Second
	maxBackoff         = 2 * time.Minute

	reasonDecode    = "decode"
	reasonPermanent = "permanent"
	reasonExhausted = "retries_exhausted"

	HeaderDeadLetterDetail = "x-dlq-detail"
	maxDetail              = 512
)

type Source interface {
	Poll(ctx context.Context) ([]Record, error)
	Commit(ctx context.Context, r Record) error
}

type DeadLetterSink interface {
	Send(ctx context.Context, topic string, r Record, reason string) error
}

type Handler interface {
	Handle(ctx context.Context, tx pgx.Tx, evt events.Event) error
}

type HandlerFunc func(ctx context.Context, tx pgx.Tx, evt events.Event) error

func (f HandlerFunc) Handle(ctx context.Context, tx pgx.Tx, evt events.Event) error {
	return f(ctx, tx, evt)
}

type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

func IsPermanent(err error) bool {
	var permanent permanentError
	return errors.As(err, &permanent)
}

type transientError struct{ err error }

func (e transientError) Error() string { return e.err.Error() }
func (e transientError) Unwrap() error { return e.err }

func IsTransient(err error) bool {
	var transient transientError
	return errors.As(err, &transient)
}

// Transient marks a failure that heals by itself (a database or network outage): the consumer retries it with back-off
// without the MaxAttempts limit instead of dead-lettering the event.
func Transient(err error) error {
	if err == nil {
		return nil
	}
	return transientError{err}
}

func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err}
}

type Config struct {
	Name            string
	Handlers        map[string]Handler
	DeadLetterTopic string
	Idempotent      bool
	MaxAttempts     int
	Backoff         func(attempt int) time.Duration
	Log             *slog.Logger
}

type Consumer struct {
	src     Source
	dlq     DeadLetterSink
	inbox   Inbox
	cfg     Config
	log     *slog.Logger
	metrics Metrics
}

func NewConsumer(src Source, dlq DeadLetterSink, inbox Inbox, cfg Config) (*Consumer, error) {
	if src == nil || dlq == nil || inbox == nil || cfg.Name == "" || cfg.DeadLetterTopic == "" || len(cfg.Handlers) == 0 {
		return nil, fmt.Errorf("source, dead-letter sink, inbox, name, dead-letter topic and handlers are required: %w", ErrInvalidArgument)
	}
	if _, known := tenant.ServicePrincipalTable()[cfg.Name]; !known {
		return nil, fmt.Errorf("%w: %q", tenant.ErrUnknownServicePrincipal, cfg.Name)
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = DefaultMaxAttempts
	}
	if cfg.Backoff == nil {
		cfg.Backoff = ExponentialBackoff
	}
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	return &Consumer{src: src, dlq: dlq, inbox: inbox, cfg: cfg, log: log.With("consumer", cfg.Name)}, nil
}

func ExponentialBackoff(attempt int) time.Duration {
	return min(baseBackoff<<min(attempt, 7), maxBackoff)
}

func (c *Consumer) Metrics() *Metrics { return &c.metrics }

func (c *Consumer) Run(ctx context.Context) error {
	for {
		records, err := c.src.Poll(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			c.metrics.pollFailures.Add(1)
			c.log.ErrorContext(ctx, "poll failed", "err", err)
			return fmt.Errorf("poll: %w", err)
		}
		for _, rec := range records {
			if err := c.process(ctx, rec); err != nil {
				return err
			}
		}
	}
}

func (c *Consumer) process(ctx context.Context, rec Record) error {
	log := c.log.With("topic", rec.Topic, "partition", rec.Partition, "offset", rec.Offset)
	evt, err := Decode(rec)
	if err != nil {
		log.ErrorContext(ctx, "undecodable record dead-lettered", "err", err)
		return c.park(ctx, rec, reasonDecode, err)
	}
	log = log.With("eventId", evt.ID, "eventType", evt.Type, "tenantId", evt.TenantID, "correlationId", evt.CorrelationID)
	handler, ok := c.cfg.Handlers[evt.Type]
	if !ok {
		c.metrics.skipped.Add(1)
		return c.commit(ctx, rec, log)
	}
	actor, err := tenant.ServicePrincipal(c.cfg.Name, evt.TenantID, "")
	if err != nil {
		log.ErrorContext(ctx, "service principal refused; event dead-lettered", "err", err)
		return c.park(ctx, rec, reasonPermanent, err)
	}
	actorCtx := tenant.With(ctx, actor)
	for attempt := 1; ; attempt++ {
		applied, err := c.apply(actorCtx, handler, evt)
		if err == nil {
			c.count(applied)
			log.InfoContext(ctx, "event handled", "applied", applied)
			return c.commit(ctx, rec, log)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if IsPermanent(err) {
			log.ErrorContext(ctx, "event failed permanently; dead-lettered", "err", err)
			return c.park(ctx, rec, reasonPermanent, err)
		}
		if attempt >= c.cfg.MaxAttempts && !IsTransient(err) {
			log.ErrorContext(ctx, "event failed after retries; dead-lettered", "err", err, "attempts", attempt)
			return c.park(ctx, rec, reasonExhausted, err)
		}
		c.metrics.retries.Add(1)
		log.WarnContext(ctx, "event failed; retrying", "err", err, "attempt", attempt)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.cfg.Backoff(attempt)):
		}
	}
}

func (c *Consumer) apply(ctx context.Context, handler Handler, evt events.Event) (bool, error) {
	if !c.cfg.Idempotent {
		return c.inbox.Once(ctx, c.cfg.Name, evt.ID, func(tx pgx.Tx) error { return handler.Handle(ctx, tx, evt) })
	}
	if seen, err := c.inbox.Seen(ctx, c.cfg.Name, evt.ID); err != nil || seen {
		return false, err
	}
	if err := handler.Handle(ctx, nil, evt); err != nil {
		return false, err
	}
	return true, c.inbox.Record(ctx, c.cfg.Name, evt.ID)
}

func (c *Consumer) count(applied bool) {
	if applied {
		c.metrics.applied.Add(1)
		return
	}
	c.metrics.duplicates.Add(1)
}

func (c *Consumer) park(ctx context.Context, rec Record, reason string, cause error) error {
	headers := make(map[string]string, len(rec.Headers)+1)
	for k, v := range rec.Headers {
		headers[k] = v
	}
	headers[HeaderDeadLetterDetail] = cause.Error()[:min(len(cause.Error()), maxDetail)]
	rec.Headers = headers
	if err := c.dlq.Send(ctx, c.cfg.DeadLetterTopic, rec, reason); err != nil {
		c.metrics.deadLetterFailures.Add(1)
		c.log.ErrorContext(ctx, "dead-letter send failed; stopping so the record is redelivered", "err", err, "topic", rec.Topic, "partition", rec.Partition, "offset", rec.Offset)
		return fmt.Errorf("dead-letter %s/%d@%d: %w", rec.Topic, rec.Partition, rec.Offset, err)
	}
	c.metrics.deadLettered.Add(1)
	return c.commit(ctx, rec, c.log)
}

func (c *Consumer) commit(ctx context.Context, rec Record, log *slog.Logger) error {
	if err := c.src.Commit(ctx, rec); err != nil {
		c.metrics.commitFailures.Add(1)
		log.ErrorContext(ctx, "offset commit failed", "err", err)
		return fmt.Errorf("commit %s/%d@%d: %w", rec.Topic, rec.Partition, rec.Offset, err)
	}
	return nil
}

type Metrics struct {
	applied, duplicates, skipped, retries, deadLettered, deadLetterFailures, commitFailures, pollFailures atomic.Int64
}

func (m *Metrics) Write(w io.Writer, name string) {
	counters := []struct {
		metric string
		value  *atomic.Int64
	}{
		{"applied", &m.applied}, {"duplicates", &m.duplicates}, {"skipped", &m.skipped}, {"retries", &m.retries},
		{"dead_lettered", &m.deadLettered}, {"dead_letter_failures", &m.deadLetterFailures},
		{"commit_failures", &m.commitFailures}, {"poll_failures", &m.pollFailures},
	}
	for _, c := range counters {
		_, _ = fmt.Fprintf(w, "# TYPE event_consumer_%s_total counter\nevent_consumer_%s_total{consumer=%q} %d\n", c.metric, c.metric, name, c.value.Load())
	}
}
