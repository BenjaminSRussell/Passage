package notifyout

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/batch/internal/domain"
	"github.com/BenjaminSRussell/passage/services/batch/migrations"
)

type fakeSender struct {
	mu     sync.Mutex
	sent   []Message
	errs   []error
	onSend func(Message)
}

func (f *fakeSender) Send(_ context.Context, msg Message) error {
	f.mu.Lock()
	f.sent = append(f.sent, msg)
	var err error
	if len(f.errs) > 0 {
		err, f.errs = f.errs[0], f.errs[1:]
	}
	hook := f.onSend
	f.mu.Unlock()
	if hook != nil {
		hook(msg)
	}
	return err
}

func (f *fakeSender) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

type rig struct {
	admin, app  *pgxpool.Pool
	email, hook *fakeSender
	dist        *Distributor
	now         time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	r := &rig{admin: admin, app: app, email: &fakeSender{}, hook: &fakeSender{}, now: time.Now()}
	r.dist = &Distributor{
		Pool:    app,
		Senders: map[string]Sender{domain.ChannelEmail: r.email, domain.ChannelWebhook: r.hook},
		Now:     func() time.Time { return r.now },
	}
	return r
}

func (r *rig) seed(t *testing.T, tenantN int, channel, recipient string) string {
	t.Helper()
	id := ids.New()
	_, err := r.admin.Exec(context.Background(), `insert into notify_outbound_message
		(tenant_id, id, channel, recipient_address, subject, body_text, status)
		values ($1, $2, $3, nullif($4, ''), 'subject', 'body', 'queued')`, testkit.TenantID(tenantN), id, channel, recipient)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

type row struct {
	status    string
	attempts  int
	lastError string
	sentAt    *time.Time
	nextAt    *time.Time
}

func (r *rig) row(t *testing.T, id string) row {
	t.Helper()
	var got row
	err := r.admin.QueryRow(context.Background(), `select status, attempts, coalesce(last_error, ''), sent_at, next_attempt_at
		from notify_outbound_message where id = $1`, id).Scan(&got.status, &got.attempts, &got.lastError, &got.sentAt, &got.nextAt)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func (r *rig) makeDue(t *testing.T, id string) {
	t.Helper()
	if _, err := r.admin.Exec(context.Background(), `update notify_outbound_message set next_attempt_at = now() - interval '1 second' where id = $1`, id); err != nil {
		t.Fatal(err)
	}
}

func TestDistributorDeliversAcrossTenantsOnceAndLeavesUnconfiguredChannels(t *testing.T) {
	r := newRig(t)
	mail := r.seed(t, 1, domain.ChannelEmail, "a@example.test")
	otherTenantHook := r.seed(t, 2, domain.ChannelWebhook, "")
	inApp := r.seed(t, 1, domain.ChannelInApp, "")

	r.dist.Tick(context.Background())
	r.dist.Tick(context.Background())

	for _, id := range []string{mail, otherTenantHook} {
		if got := r.row(t, id); got.status != domain.MsgSent || got.attempts != 1 || got.sentAt == nil || got.lastError != "" {
			t.Fatalf("row %s = %+v", id, got)
		}
	}
	if got := r.row(t, inApp); got.status != domain.MsgQueued || got.attempts != 0 {
		t.Fatalf("in_app row touched: %+v", got)
	}
	if r.email.calls() != 1 || r.hook.calls() != 1 {
		t.Fatalf("calls email=%d webhook=%d, want 1 each", r.email.calls(), r.hook.calls())
	}
	if m := r.email.sent[0]; m.ID != mail || m.To != "a@example.test" || m.Subject != "subject" || m.Body != "body" {
		t.Fatalf("message = %+v", m)
	}
}

func TestDistributorRetriesWithBackoffThenFailsTerminally(t *testing.T) {
	r := newRig(t)
	boom := errors.New("smtp send: connection refused")
	r.email.errs = []error{boom, boom, boom, boom, boom, boom}
	id := r.seed(t, 1, domain.ChannelEmail, "a@example.test")

	r.dist.Tick(context.Background())
	got := r.row(t, id)
	if got.status != domain.MsgQueued || got.attempts != 1 || got.lastError != boom.Error() || got.nextAt == nil {
		t.Fatalf("after first failure = %+v", got)
	}
	if want := r.now.Add(time.Minute); got.nextAt.Sub(want).Abs() > time.Second {
		t.Fatalf("next attempt %s, want about %s", got.nextAt, want)
	}

	r.dist.Tick(context.Background())
	if r.email.calls() != 1 {
		t.Fatalf("row retried before its backoff elapsed: %d calls", r.email.calls())
	}

	for want := 2; want <= 5; want++ {
		r.makeDue(t, id)
		r.dist.Tick(context.Background())
		if got := r.row(t, id); got.attempts != want {
			t.Fatalf("attempts = %d, want %d", got.attempts, want)
		}
	}
	got = r.row(t, id)
	if got.status != domain.MsgFailed || got.nextAt != nil || got.lastError != boom.Error() {
		t.Fatalf("terminal row = %+v", got)
	}
	r.dist.Tick(context.Background())
	if r.email.calls() != 5 {
		t.Fatalf("sends = %d, want exactly 5 attempts", r.email.calls())
	}
}

func TestDistributorRecoversAfterTransientFailure(t *testing.T) {
	r := newRig(t)
	r.email.errs = []error{errors.New("temporary")}
	id := r.seed(t, 1, domain.ChannelEmail, "a@example.test")

	r.dist.Tick(context.Background())
	r.makeDue(t, id)
	r.dist.Tick(context.Background())

	if got := r.row(t, id); got.status != domain.MsgSent || got.attempts != 2 || got.lastError != "" {
		t.Fatalf("row = %+v", got)
	}
}

func TestDistributorFailsEmailWithoutRecipientAndNeverSends(t *testing.T) {
	r := newRig(t)
	id := r.seed(t, 1, domain.ChannelEmail, "")

	r.dist.Tick(context.Background())

	if got := r.row(t, id); got.status != domain.MsgFailed || got.lastError != noRecipientText {
		t.Fatalf("row = %+v", got)
	}
	if r.email.calls() != 0 {
		t.Fatal("sender called without a recipient")
	}
}

func TestDistributorTruncatesStoredError(t *testing.T) {
	r := newRig(t)
	r.email.errs = []error{errors.New(strings.Repeat("é", 2000))}
	id := r.seed(t, 1, domain.ChannelEmail, "a@example.test")

	r.dist.Tick(context.Background())

	if got := r.row(t, id); len([]rune(got.lastError)) != maxErrorRunes {
		t.Fatalf("last_error runes = %d", len([]rune(got.lastError)))
	}
}

func TestDistributorReplicasNeverClaimTheSameMessage(t *testing.T) {
	r := newRig(t)
	const total = 60
	for i := 0; i < total; i++ {
		r.seed(t, 1+i%3, domain.ChannelEmail, "a@example.test")
	}
	other := &fakeSender{}
	second := &Distributor{Pool: r.app, Senders: map[string]Sender{domain.ChannelEmail: other}, Concurrency: 3}
	r.dist.Concurrency = 3

	var wg sync.WaitGroup
	for _, d := range []*Distributor{r.dist, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 10 {
				d.Tick(context.Background())
			}
		}()
	}
	wg.Wait()

	seen := map[string]int{}
	for _, f := range []*fakeSender{r.email, other} {
		for _, m := range f.sent {
			seen[m.ID]++
		}
	}
	if len(seen) != total {
		t.Fatalf("delivered %d distinct messages, want %d", len(seen), total)
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("message %s sent %d times", id, n)
		}
	}
	if r.email.calls() == 0 || other.calls() == 0 {
		t.Fatalf("work not shared: %d / %d", r.email.calls(), other.calls())
	}
}

func TestDistributorFailsExpiredSendingLeaseInsteadOfResending(t *testing.T) {
	r := newRig(t)
	id := r.seed(t, 1, domain.ChannelEmail, "a@example.test")
	if _, err := r.admin.Exec(context.Background(), `update notify_outbound_message
		set status = 'sending', attempts = 1, next_attempt_at = now() - interval '1 minute' where id = $1`, id); err != nil {
		t.Fatal(err)
	}

	r.dist.Tick(context.Background())

	got := r.row(t, id)
	if got.status != domain.MsgFailed || !strings.Contains(got.lastError, "outcome unknown") || got.nextAt != nil {
		t.Fatalf("row = %+v", got)
	}
	if r.email.calls() != 0 {
		t.Fatal("an unconfirmed message was sent again")
	}
	if r.dist.unconfirmed.Load() != 1 {
		t.Fatalf("unconfirmed = %d", r.dist.unconfirmed.Load())
	}
}

func TestDistributorLeavesLiveSendingLeaseAlone(t *testing.T) {
	r := newRig(t)
	id := r.seed(t, 1, domain.ChannelEmail, "a@example.test")
	if _, err := r.admin.Exec(context.Background(), `update notify_outbound_message
		set status = 'sending', attempts = 1, next_attempt_at = now() + interval '1 minute' where id = $1`, id); err != nil {
		t.Fatal(err)
	}

	r.dist.Tick(context.Background())

	if got := r.row(t, id); got.status != domain.MsgSending || r.email.calls() != 0 {
		t.Fatalf("row = %+v calls = %d", got, r.email.calls())
	}
}

func TestDistributorStaleAttemptCannotOverwriteNewerState(t *testing.T) {
	r := newRig(t)
	id := r.seed(t, 1, domain.ChannelEmail, "a@example.test")
	r.email.onSend = func(Message) {
		_, err := r.admin.Exec(context.Background(), `update notify_outbound_message set status = 'failed', attempts = attempts + 1 where id = $1`, id)
		if err != nil {
			t.Error(err)
		}
	}

	r.dist.Tick(context.Background())

	if got := r.row(t, id); got.status != domain.MsgFailed || got.sentAt != nil || got.attempts != 2 {
		t.Fatalf("stale attempt overwrote the row: %+v", got)
	}
	if r.dist.recordErrs.Load() != 1 || r.dist.sentTotal.Load() != 0 {
		t.Fatalf("recordErrs=%d sent=%d", r.dist.recordErrs.Load(), r.dist.sentTotal.Load())
	}
}

func TestDistributorStaleClaimTokenCannotRecordAfterReclaim(t *testing.T) {
	r := newRig(t)
	id := r.seed(t, 1, domain.ChannelEmail, "a@example.test")
	r.email.onSend = func(Message) {
		_, err := r.admin.Exec(context.Background(), `update notify_outbound_message set claim_token = gen_random_uuid(), attempts = 0 where id = $1`, id)
		if err != nil {
			t.Error(err)
		}
	}

	r.dist.Tick(context.Background())

	if got := r.row(t, id); got.status != domain.MsgSending || got.sentAt != nil {
		t.Fatalf("stale claim recorded its result: %+v", got)
	}
	if r.dist.recordErrs.Load() != 1 {
		t.Fatalf("recordErrs = %d", r.dist.recordErrs.Load())
	}
}

func TestDistributorClaimsNoMoreThanItCanSendPerRound(t *testing.T) {
	r := newRig(t)
	r.dist.Concurrency = 2
	for range 5 {
		r.seed(t, 1, domain.ChannelEmail, "a@example.test")
	}
	rows, err := r.dist.claim(context.Background())
	if err != nil || len(rows) != 2 {
		t.Fatalf("claimed %d rows, %v; want 2", len(rows), err)
	}
	var queued int
	if err := r.admin.QueryRow(context.Background(), `select count(*) from notify_outbound_message where status = 'queued'`).Scan(&queued); err != nil || queued != 3 {
		t.Fatalf("queued = %d, %v", queued, err)
	}
	r.dist.Tick(context.Background())
	if r.email.calls() != 3 {
		t.Fatalf("Tick drained %d of the remaining 3", r.email.calls())
	}
}

func TestDistributorFailsPermanentErrorsWithoutRetry(t *testing.T) {
	r := newRig(t)
	r.email.errs = []error{permanentError{errors.New("smtp send: 550 no such user a@example.test")}}
	r.hook.errs = []error{errors.New("transient")}
	mail := r.seed(t, 1, domain.ChannelEmail, "a@example.test")
	hook := r.seed(t, 1, domain.ChannelWebhook, "")

	r.dist.Tick(context.Background())

	if got := r.row(t, mail); got.status != domain.MsgFailed || got.attempts != 1 {
		t.Fatalf("permanent failure = %+v", got)
	}
	if got := r.row(t, hook); got.status != domain.MsgQueued {
		t.Fatalf("transient failure = %+v", got)
	}
}

func TestClaimFunctionsIgnoreAShadowingMakeInterval(t *testing.T) {
	r := newRig(t)
	for _, sig := range []string{"secs integer", "secs double precision"} {
		if _, err := r.admin.Exec(context.Background(), `create function make_interval(`+sig+`) returns interval language sql as $$ select interval '0 seconds' $$`); err != nil {
			t.Fatal(err)
		}
	}
	id := r.seed(t, 1, domain.ChannelEmail, "a@example.test")

	rows, err := r.dist.claim(context.Background())
	if err != nil || len(rows) != 1 {
		t.Fatalf("claim = %d, %v", len(rows), err)
	}
	if got := r.row(t, id); got.nextAt == nil || time.Until(*got.nextAt) < time.Minute {
		t.Fatalf("a hijacked make_interval shortened the lease: %+v", got)
	}
}

func TestDistributorMetricsReportOutcomes(t *testing.T) {
	r := newRig(t)
	r.email.errs = []error{nil, errors.New("down")}
	r.seed(t, 1, domain.ChannelEmail, "a@example.test")
	r.seed(t, 1, domain.ChannelEmail, "b@example.test")

	r.dist.Tick(context.Background())

	rec := httptest.NewRecorder()
	r.dist.Metrics().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{"notify_distribute_claimed_total 2", "notify_distribute_sent_total 1", "notify_distribute_retried_total 1", "notify_distribute_failed_total 0", "notify_distribute_inflight 0"} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q:\n%s", want, body)
		}
	}
}

func TestDistributorSelfCheckSeesMigrationOwnerBypass(t *testing.T) {
	r := newRig(t)
	var ok bool
	if err := r.app.QueryRow(context.Background(), `select notify_distribute_can_scan()`).Scan(&ok); err != nil || !ok {
		t.Fatalf("can_scan = %v, %v", ok, err)
	}
}

func TestSendersForMapsConfiguredChannels(t *testing.T) {
	got := SendersFor(Config{SMTP: &SMTPConfig{Host: "h"}, Webhook: &WebhookConfig{URL: "https://x.test"}})
	if len(got) != 2 || got[domain.ChannelEmail] == nil || got[domain.ChannelWebhook] == nil {
		t.Fatalf("senders = %v", fmt.Sprint(got))
	}
	if len(SendersFor(Config{})) != 0 {
		t.Fatal("senders configured without adapters")
	}
}
