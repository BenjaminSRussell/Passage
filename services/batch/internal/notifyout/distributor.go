package notifyout

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/batch/internal/domain"
)

const (
	DefaultDistributeInterval = 5 * time.Second
	DefaultBatchSize          = 20
	DefaultConcurrency        = 4
	DefaultLease              = 2 * time.Minute

	markAttempts    = 3
	markTimeout     = 10 * time.Second
	maxErrorRunes   = 500
	noRecipientText = "no recipient address on the message"
)

func SendersFor(cfg Config) map[string]Sender {
	senders := map[string]Sender{}
	if cfg.SMTP != nil {
		senders[domain.ChannelEmail] = NewSMTPSender(*cfg.SMTP)
	}
	if cfg.Webhook != nil {
		senders[domain.ChannelWebhook] = NewWebhookSender(*cfg.Webhook, nil)
	}
	return senders
}

type claimed struct {
	tenantID  string
	id        string
	channel   string
	recipient string
	subject   string
	body      string
	attempts  int
}

// Distributor drains notify_outbound_message through the configured senders.
// A row is claimed with FOR UPDATE SKIP LOCKED (so replicas never take the same
// row), held in status sending under a lease, and moved to sent, back to queued
// with backoff, or to failed. A row whose lease expires while still sending
// has an unknown outcome and is failed rather than resent, so a crash after a
// successful send cannot produce a second delivery; operators retry it by hand.
type Distributor struct {
	Pool        *pgxpool.Pool
	Senders     map[string]Sender
	Interval    time.Duration
	BatchSize   int
	Concurrency int
	Lease       time.Duration
	Now         func() time.Time

	claimedTotal    atomic.Int64
	sentTotal       atomic.Int64
	retriedTotal    atomic.Int64
	failedTotal     atomic.Int64
	unconfirmed     atomic.Int64
	recordErrs      atomic.Int64
	scanErrs        atomic.Int64
	scanBlind       atomic.Int64
	inflight        atomic.Int64
	lastSuccessUnix atomic.Int64
}

func (d *Distributor) Run(ctx context.Context) {
	interval := d.Interval
	if interval <= 0 {
		interval = DefaultDistributeInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	d.selfCheck(ctx)
	for {
		d.Tick(ctx)
		select {
		case <-ctx.Done():
			slog.Info("notify distributor stopping")
			return
		case <-ticker.C:
		}
	}
}

func (d *Distributor) selfCheck(ctx context.Context) {
	var ok bool
	err := d.Pool.QueryRow(ctx, `select notify_distribute_can_scan()`).Scan(&ok)
	switch {
	case err != nil:
		slog.Error("notify distributor self-check failed", "err", err)
	case !ok:
		d.scanBlind.Store(1)
		slog.Error("notify distributor is blind: the owner of notify_claim_messages cannot bypass row-level security, so it will find no messages; migrate as a superuser or BYPASSRLS role")
	}
}

// Tick runs one failure sweep and one claim-and-deliver pass, returning when every claimed message is settled.
func (d *Distributor) Tick(ctx context.Context) {
	d.failUnconfirmed(ctx)
	if ctx.Err() != nil || len(d.Senders) == 0 {
		return
	}
	rows, err := d.claim(ctx)
	if err != nil {
		if ctx.Err() == nil {
			d.scanErrs.Add(1)
			slog.Error("notify distributor claim failed", "err", err)
		}
		return
	}
	d.claimedTotal.Add(int64(len(rows)))
	slots := make(chan struct{}, d.concurrency())
	var wg sync.WaitGroup
	for _, row := range rows {
		slots <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			d.inflight.Add(1)
			defer d.inflight.Add(-1)
			d.deliver(ctx, row)
		}()
	}
	wg.Wait()
}

func (d *Distributor) claim(ctx context.Context) ([]claimed, error) {
	channels := make([]string, 0, len(d.Senders))
	for channel := range d.Senders {
		channels = append(channels, channel)
	}
	dbRows, err := d.Pool.Query(ctx, `select tenant_id::text, id::text, channel, coalesce(recipient_address, ''), coalesce(subject, ''), coalesce(body_text, ''), attempts
		from notify_claim_messages($1, $2, $3)`, channels, int(d.lease().Seconds()), d.batchSize())
	if err != nil {
		return nil, fmt.Errorf("claim messages: %w", err)
	}
	defer dbRows.Close()
	var out []claimed
	for dbRows.Next() {
		var c claimed
		if err := dbRows.Scan(&c.tenantID, &c.id, &c.channel, &c.recipient, &c.subject, &c.body, &c.attempts); err != nil {
			return nil, fmt.Errorf("scan claimed message: %w", err)
		}
		out = append(out, c)
	}
	return out, dbRows.Err()
}

func (d *Distributor) failUnconfirmed(ctx context.Context) {
	dbRows, err := d.Pool.Query(ctx, `select tenant_id::text, id::text, channel, attempts from notify_fail_unconfirmed_messages($1)`, d.batchSize())
	if err != nil {
		if ctx.Err() == nil {
			d.scanErrs.Add(1)
			slog.Error("notify distributor could not fail unconfirmed messages", "err", err)
		}
		return
	}
	defer dbRows.Close()
	for dbRows.Next() {
		var tenantID, id, channel string
		var attempts int
		if err := dbRows.Scan(&tenantID, &id, &channel, &attempts); err != nil {
			slog.Error("notify distributor scan unconfirmed failed", "err", err)
			return
		}
		d.unconfirmed.Add(1)
		d.failedTotal.Add(1)
		slog.Error("notify message failed: delivery outcome unknown after lease expiry", "messageId", id, "tenantId", tenantID, "channel", channel, "attempt", attempts, "correlationId", "notify-"+id)
	}
}

func (d *Distributor) deliver(ctx context.Context, c claimed) {
	log := slog.With("messageId", c.id, "tenantId", c.tenantID, "channel", c.channel, "attempt", c.attempts, "correlationId", "notify-"+c.id)
	if c.channel == domain.ChannelEmail && c.recipient == "" {
		d.settleFailure(ctx, log, c, errors.New(noRecipientText), false)
		return
	}
	start := time.Now()
	err := d.Senders[c.channel].Send(ctx, Message{ID: c.id, To: c.recipient, Subject: c.subject, Body: c.body})
	if err != nil {
		d.settleFailure(ctx, log, c, err, true)
		return
	}
	if err := d.settle(ctx, c, markSent); err != nil {
		d.recordErrs.Add(1)
		log.Error("notify message was delivered but the result could not be recorded; it will be failed as unconfirmed, not resent", "err", err)
		return
	}
	d.sentTotal.Add(1)
	d.lastSuccessUnix.Store(d.now().Unix())
	log.Info("notify message sent", "durationMs", time.Since(start).Milliseconds())
}

func (d *Distributor) settleFailure(ctx context.Context, log *slog.Logger, c claimed, cause error, retryable bool) {
	next, more := domain.NextAttempt(c.attempts, d.now())
	retry := retryable && more
	text := truncate(cause.Error())
	err := d.settle(ctx, c, func(ctx context.Context, tx pgx.Tx, c claimed) (int64, error) {
		if retry {
			tag, err := tx.Exec(ctx, markRetryableSQL, c.tenantID, c.id, c.attempts, next, text)
			return tag.RowsAffected(), err
		}
		tag, err := tx.Exec(ctx, markTerminalSQL, c.tenantID, c.id, c.attempts, text)
		return tag.RowsAffected(), err
	})
	switch {
	case err != nil:
		d.recordErrs.Add(1)
		log.Error("notify message send failed and the failure could not be recorded", "err", cause, "recordErr", err)
	case retry:
		d.retriedTotal.Add(1)
		log.Warn("notify message send failed; will retry", "err", cause, "nextAttemptAt", next.UTC().Format(time.RFC3339))
	default:
		d.failedTotal.Add(1)
		log.Error("notify message failed permanently", "err", cause)
	}
}

const (
	markSentSQL = `update notify_outbound_message set status = 'sent', sent_at = now(), next_attempt_at = null, last_error = null
		where tenant_id = $1 and id = $2 and status = 'sending' and attempts = $3`
	markRetryableSQL = `update notify_outbound_message set status = 'queued', next_attempt_at = $4, last_error = $5
		where tenant_id = $1 and id = $2 and status = 'sending' and attempts = $3`
	markTerminalSQL = `update notify_outbound_message set status = 'failed', next_attempt_at = null, last_error = $4
		where tenant_id = $1 and id = $2 and status = 'sending' and attempts = $3`
)

func markSent(ctx context.Context, tx pgx.Tx, c claimed) (int64, error) {
	tag, err := tx.Exec(ctx, markSentSQL, c.tenantID, c.id, c.attempts)
	return tag.RowsAffected(), err
}

var errLeaseLost = errors.New("message is no longer held by this attempt")

func (d *Distributor) settle(ctx context.Context, c claimed, mark func(context.Context, pgx.Tx, claimed) (int64, error)) error {
	ctx = tenant.With(context.WithoutCancel(ctx), tenant.Principal{TenantID: c.tenantID, Service: "batch"})
	var err error
	for attempt := 1; attempt <= markAttempts; attempt++ {
		markCtx, cancel := context.WithTimeout(ctx, markTimeout)
		err = dbx.WithTenantTx(markCtx, d.Pool, func(tx pgx.Tx) error {
			n, err := mark(markCtx, tx, c)
			if err != nil {
				return err
			}
			if n == 0 {
				return errLeaseLost
			}
			return nil
		})
		cancel()
		if err == nil || errors.Is(err, errLeaseLost) {
			return err
		}
		time.Sleep(time.Duration(attempt) * 200 * time.Millisecond)
	}
	return err
}

func truncate(s string) string {
	if utf8.RuneCountInString(s) <= maxErrorRunes {
		return s
	}
	return string([]rune(s)[:maxErrorRunes])
}

func (d *Distributor) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *Distributor) batchSize() int {
	if d.BatchSize > 0 {
		return d.BatchSize
	}
	return DefaultBatchSize
}

func (d *Distributor) concurrency() int {
	if d.Concurrency > 0 {
		return d.Concurrency
	}
	return DefaultConcurrency
}

func (d *Distributor) lease() time.Duration {
	if d.Lease > 0 {
		return d.Lease
	}
	return DefaultLease
}

func (d *Distributor) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		for _, m := range []struct {
			name, kind, help string
			value            int64
		}{
			{"notify_distribute_inflight", "gauge", "Messages this replica is sending.", d.inflight.Load()},
			{"notify_distribute_claimed_total", "counter", "Messages this replica claimed.", d.claimedTotal.Load()},
			{"notify_distribute_sent_total", "counter", "Messages delivered and recorded as sent.", d.sentTotal.Load()},
			{"notify_distribute_retried_total", "counter", "Failed sends scheduled for retry with backoff.", d.retriedTotal.Load()},
			{"notify_distribute_failed_total", "counter", "Messages moved to the terminal failed state.", d.failedTotal.Load()},
			{"notify_distribute_unconfirmed_total", "counter", "Messages failed because their send lease expired with an unknown outcome.", d.unconfirmed.Load()},
			{"notify_distribute_record_errors_total", "counter", "Send results that could not be recorded.", d.recordErrs.Load()},
			{"notify_distribute_scan_errors_total", "counter", "Failed claim or sweep queries.", d.scanErrs.Load()},
			{"notify_distribute_scan_blind", "gauge", "1 when the cross-tenant claim cannot bypass row-level security.", d.scanBlind.Load()},
			{"notify_distribute_last_success_timestamp_seconds", "gauge", "Unix time of the last message recorded as sent.", d.lastSuccessUnix.Load()},
		} {
			_, _ = fmt.Fprintf(w, "# HELP %[1]s %[3]s\n# TYPE %[1]s %[2]s\n%[1]s %[4]d\n", m.name, m.kind, m.help, m.value)
		}
	})
}
