package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/domain"
	analyticsevents "github.com/BenjaminSRussell/passage/services/analytics/internal/events"
	"github.com/BenjaminSRussell/passage/services/analytics/migrations"
)

const ruleOwner = "0193d6a0-0000-7000-8000-000000000001"

func vendorTaxRule() domain.DQRule {
	return domain.DQRule{RuleID: "vendor_tax_id", AssetFQN: "core.vendor", Type: domain.RuleCompleteness,
		Expression: "tax_id", ThresholdPct: domain.DefaultThreshold(), Severity: domain.SeverityHigh,
		OwnerUserID: ruleOwner, Status: domain.RuleActive}
}

func codeOf(err error) string {
	var de *domain.Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

// lastEvent returns the newest outbox payload of type typ decoded into data.
func lastEvent(t *testing.T, ctx context.Context, st *Postgres, typ string, data any) int {
	t.Helper()
	var (
		n       int
		payload []byte
	)
	if err := st.pool.QueryRow(ctx, `select count(*) over (), payload from outbox where type = $1 order by created_at desc limit 1`, typ).Scan(&n, &payload); err != nil {
		return 0
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(env.Data, data); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPutDQRule_CreateReplaceAndVersioning(t *testing.T) {
	st := New(migrated(t))
	ctx := ctxFor(1, "")
	now := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)

	created, isNew, err := st.PutDQRule(ctx, vendorTaxRule(), 0, false, now)
	if err != nil || !isNew || created.Version != 1 || created.MuteUntil != nil {
		t.Fatalf("create = %+v new=%v err=%v", created, isNew, err)
	}
	if _, _, err := st.PutDQRule(ctx, vendorTaxRule(), 0, false, now); codeOf(err) != domain.CodePreconditionRequired {
		t.Fatalf("replace without If-Match = %v, want %s", err, domain.CodePreconditionRequired)
	}
	if _, _, err := st.PutDQRule(ctx, vendorTaxRule(), 7, false, now); codeOf(err) != domain.CodeVersionConflict {
		t.Fatalf("stale If-Match = %v, want %s", err, domain.CodeVersionConflict)
	}
	other := vendorTaxRule()
	other.RuleID = "vendor_iban"
	if _, _, err := st.PutDQRule(ctx, other, 1, false, now); codeOf(err) != domain.CodeVersionConflict {
		t.Fatalf("If-Match on a missing rule = %v, want %s", err, domain.CodeVersionConflict)
	}

	changed := vendorTaxRule()
	changed.ThresholdPct = decimal.RequireFromString("99.50")
	replaced, isNew, err := st.PutDQRule(ctx, changed, 1, true, now)
	if err != nil || isNew || replaced.Version != 2 || !replaced.ThresholdPct.Equal(changed.ThresholdPct) {
		t.Fatalf("replace = %+v new=%v err=%v", replaced, isNew, err)
	}
	if replaced.MuteUntil == nil || !replaced.MuteUntil.Equal(now.AddDate(0, 0, domain.DQMuteDays)) {
		t.Fatalf("mute until = %v, want now + 7 days", replaced.MuteUntil)
	}
	// Muting again keeps the open window instead of extending it.
	again, _, err := st.PutDQRule(ctx, changed, 2, true, now.Add(48*time.Hour))
	if err != nil || !again.MuteUntil.Equal(*replaced.MuteUntil) {
		t.Fatalf("re-mute = %+v, %v", again.MuteUntil, err)
	}
	unmuted, _, err := st.PutDQRule(ctx, changed, 3, false, now)
	if err != nil || unmuted.MuteUntil != nil {
		t.Fatalf("unmute = %+v, %v", unmuted.MuteUntil, err)
	}

	invalid := vendorTaxRule()
	invalid.Severity = "CRITICAL"
	if _, _, err := st.PutDQRule(ctx, invalid, 4, false, now); codeOf(err) != domain.CodeValidation {
		t.Fatalf("invalid rule = %v, want %s", err, domain.CodeValidation)
	}

	if got, err := st.GetDQRule(ctxFor(2, ""), "vendor_tax_id"); codeOf(err) != domain.CodeNotFound {
		t.Fatalf("tenant 2 reads %+v, %v; want not found", got, err)
	}
	if rules, err := st.ListDQRules(ctxFor(2, ""), RuleFilter{}); err != nil || len(rules) != 0 {
		t.Fatalf("tenant 2 lists %+v, %v", rules, err)
	}
	if rules, err := st.ListDQRules(ctx, RuleFilter{Asset: "core.vendor"}); err != nil || len(rules) != 1 || rules[0].Version != 4 {
		t.Fatalf("list by asset = %+v, %v", rules, err)
	}
}

// AC-46.14 and BR-46.15: 3% of vendors lack a tax id under a 98% rule; the
// owner gets one task per day, the third consecutive failure escalates, a pass
// resets the streak, and a muted rule scores without notifying.
func TestRecordDQScore_StreakRoutingAndOutbox(t *testing.T) {
	st := New(migrated(t))
	ctx := ctxFor(1, "")
	day := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	if _, _, err := st.PutDQRule(ctx, vendorTaxRule(), 0, false, day); err != nil {
		t.Fatal(err)
	}

	first, err := st.RecordDQScore(ctx, "vendor_tax_id", day, 1000, 30)
	if err != nil {
		t.Fatal(err)
	}
	if first.Score.PassPct.String() != "97" || !first.Score.Below || first.Score.BelowStreak != 1 ||
		first.Routing != (domain.Routing{OwnerTask: true}) {
		t.Fatalf("first evaluation = %+v", first)
	}
	var data analyticsevents.DQScoredData
	if n := lastEvent(t, ctx, st, analyticsevents.DQScored, &data); n != 1 || !data.NotifyOwner || data.EscalateSteward ||
		data.TaskKey != domain.TaskKey("vendor_tax_id", "2026-10-21") || data.PassPct != "97" || data.ThresholdPct != "98" ||
		data.OwnerUserID != ruleOwner || data.Severity != domain.SeverityHigh {
		t.Fatalf("event %d = %+v", n, data)
	}

	// The same evaluation delivered twice is a replay: nothing new is written.
	replay, err := st.RecordDQScore(ctx, "vendor_tax_id", day, 1000, 30)
	if err != nil || !replay.Replayed || replay.Score.BelowStreak != 1 || outboxCount(t, st.pool) != 1 {
		t.Fatalf("replay = %+v, %v, outbox %d", replay, err, outboxCount(t, st.pool))
	}
	if _, err := st.RecordDQScore(ctx, "vendor_tax_id", day.Add(-time.Hour), 1000, 30); codeOf(err) != domain.CodeStateConflict {
		t.Fatalf("an older evaluation = %v, want %s", err, domain.CodeStateConflict)
	}

	// Hourly evaluations the same day replace the day's row but keep counting.
	// The owner's task was asked for once today; the third failure escalates
	// to the steward once, and the fourth tells nobody.
	second, _ := st.RecordDQScore(ctx, "vendor_tax_id", day.Add(time.Hour), 1000, 30)
	third, err := st.RecordDQScore(ctx, "vendor_tax_id", day.Add(2*time.Hour), 1000, 25)
	if err != nil || second.Score.BelowStreak != 2 || second.Routing != (domain.Routing{}) ||
		third.Score.BelowStreak != 3 || third.Routing != (domain.Routing{StewardEscalation: true}) {
		t.Fatalf("second = %+v third = %+v err %v", second, third, err)
	}
	data = analyticsevents.DQScoredData{}
	if n := lastEvent(t, ctx, st, analyticsevents.DQScored, &data); n != 3 || !data.EscalateSteward || data.NotifyOwner || data.TaskKey != "" || data.BelowStreak != 3 {
		t.Fatalf("event %d = %+v", n, data)
	}
	fourth, err := st.RecordDQScore(ctx, "vendor_tax_id", day.Add(3*time.Hour), 1000, 25)
	if err != nil || fourth.Score.BelowStreak != 4 || fourth.Routing != (domain.Routing{}) {
		t.Fatalf("fourth = %+v, %v", fourth, err)
	}
	// Still failing the next morning: a new day's owner task, no second escalation.
	nextDay, err := st.RecordDQScore(ctx, "vendor_tax_id", day.Add(20*time.Hour), 1000, 25)
	if err != nil || nextDay.Score.BelowStreak != 5 || nextDay.Routing != (domain.Routing{OwnerTask: true}) {
		t.Fatalf("next day = %+v, %v", nextDay, err)
	}
	data = analyticsevents.DQScoredData{}
	if lastEvent(t, ctx, st, analyticsevents.DQScored, &data); data.TaskKey != domain.TaskKey("vendor_tax_id", "2026-10-22") || data.EscalateSteward {
		t.Fatalf("next-day event = %+v", data)
	}
	history, err := st.ListDQScores(ctx, "core.vendor", "", 10)
	if err != nil || len(history) != 2 || history[1].FailedRows != 25 || history[1].PassPct.String() != "97.5" || history[1].BelowStreak != 4 {
		t.Fatalf("one row per rule and day, latest wins: %+v, %v", history, err)
	}

	// A passing evaluation resets the streak and notifies nobody.
	pass, err := st.RecordDQScore(ctx, "vendor_tax_id", day.Add(21*time.Hour), 1000, 5)
	if err != nil || pass.Score.Below || pass.Score.BelowStreak != 0 || pass.Routing != (domain.Routing{}) {
		t.Fatalf("pass = %+v, %v", pass, err)
	}
	if history, _ = st.ListDQScores(ctx, "", "vendor_tax_id", 10); len(history) != 2 || history[0].Date != "2026-10-22" || history[0].Below {
		t.Fatalf("history newest first = %+v", history)
	}

	// A muted rule still scores and counts, but does not notify.
	if _, _, err := st.PutDQRule(ctx, vendorTaxRule(), 1, true, day.AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}
	muted, err := st.RecordDQScore(ctx, "vendor_tax_id", day.AddDate(0, 0, 2), 1000, 500)
	if err != nil || !muted.Muted || muted.Score.BelowStreak != 1 || muted.Routing != (domain.Routing{}) {
		t.Fatalf("muted = %+v, %v", muted, err)
	}
	data = analyticsevents.DQScoredData{}
	if lastEvent(t, ctx, st, analyticsevents.DQScored, &data); data.NotifyOwner || !data.Muted || data.TaskKey != "" {
		t.Fatalf("muted event = %+v", data)
	}

	retired := vendorTaxRule()
	retired.Status = domain.RuleRetired
	if _, _, err := st.PutDQRule(ctx, retired, 2, false, day); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecordDQScore(ctx, "vendor_tax_id", day.AddDate(0, 0, 3), 10, 0); codeOf(err) != domain.CodeStateConflict {
		t.Fatalf("retired rule evaluation = %v, want %s", err, domain.CodeStateConflict)
	}
	if _, err := st.RecordDQScore(ctx, "no_such_rule", day, 10, 0); codeOf(err) != domain.CodeNotFound {
		t.Fatalf("unknown rule = %v, want %s", err, domain.CodeNotFound)
	}
}

// AC-46.15 and BR-46.14: a partition is VERIFIED only when counts and hashes
// match, a mismatch keeps both copies, and a dropped partition restores for 30 days.
func TestArchivePartition_VerifyDropRestore(t *testing.T) {
	st := New(migrated(t))
	ctx := ctxFor(1, "")
	now := time.Date(2026, 10, 25, 4, 0, 0, 0, time.UTC)

	p, err := st.RegisterArchivePartition(ctx, "ledger", "journal_line", "2024-01", false, false)
	if err != nil || p.Status != domain.ArchivePending || p.ID == "" {
		t.Fatalf("register = %+v, %v", p, err)
	}
	if again, err := st.RegisterArchivePartition(ctx, "ledger", "journal_line", "2024-01", false, false); err != nil || again.ID != p.ID {
		t.Fatalf("re-register = %+v, %v; want the same partition", again, err)
	}
	if _, err := st.RegisterArchivePartition(ctx, "ledger", "journal_line", "2024-13", false, false); codeOf(err) != domain.CodeValidation {
		t.Fatalf("bad period = %v", err)
	}
	if _, _, err := st.RestoreArchivePartition(ctx, p.ID, domain.DefaultRestoreTTLDays, now); codeOf(err) != domain.CodeStateConflict {
		t.Fatalf("restore of a PENDING partition = %v, want %s", err, domain.CodeStateConflict)
	}

	hash := domain.ContentHash([]string{"a", "b"})
	bad, err := st.RecordArchiveVerification(ctx, p.ID, 1000, 999, hash, hash, now)
	if codeOf(err) != domain.CodeArchiveVerify || bad.Status != domain.ArchivePending || bad.RowsArchive != 999 {
		t.Fatalf("mismatch = %+v, %v; want PENDING with counts stored and ANA-0110", bad, err)
	}
	if outboxCount(t, st.pool) != 0 {
		t.Fatal("a failed verification must not emit archive.partition.moved")
	}
	if _, err := st.DropArchivePartition(ctx, p.ID); codeOf(err) != domain.CodeStateConflict {
		t.Fatalf("drop before verification = %v", err)
	}

	good, err := st.RecordArchiveVerification(ctx, p.ID, 1000, 1000, hash, hash, now)
	if err != nil || good.Status != domain.ArchiveVerified || good.MovedAt == nil || !good.MovedAt.Equal(now) {
		t.Fatalf("verify = %+v, %v", good, err)
	}
	var moved analyticsevents.ArchivePartitionMovedData
	if n := lastEvent(t, ctx, st, analyticsevents.ArchivePartitionMoved, &moved); n != 1 || moved.Rows != 1000 ||
		moved.ContentHash != hash || moved.Table != "journal_line" || moved.Period != "2024-01" {
		t.Fatalf("moved event %d = %+v", n, moved)
	}

	dropped, err := st.DropArchivePartition(ctx, p.ID)
	if err != nil || dropped.Status != domain.ArchiveDropped {
		t.Fatalf("drop = %+v, %v", dropped, err)
	}
	restored, changed, err := st.RestoreArchivePartition(ctx, p.ID, domain.DefaultRestoreTTLDays, now)
	if err != nil || !changed || restored.Status != domain.ArchiveRestored || !restored.RestoreUntil.Equal(now.AddDate(0, 0, 30)) {
		t.Fatalf("restore = %+v changed=%v err=%v", restored, changed, err)
	}
	same, changed, err := st.RestoreArchivePartition(ctx, p.ID, domain.DefaultRestoreTTLDays, now.Add(time.Hour))
	if err != nil || changed || !same.RestoreUntil.Equal(*restored.RestoreUntil) {
		t.Fatalf("repeat restore = %+v changed=%v err=%v; want unchanged", same, changed, err)
	}
	later, changed, err := st.RestoreArchivePartition(ctx, p.ID, domain.DefaultRestoreTTLDays, now.AddDate(0, 0, 31))
	if err != nil || !changed || !later.RestoreUntil.Equal(now.AddDate(0, 0, 61)) {
		t.Fatalf("restore after expiry = %+v changed=%v err=%v", later, changed, err)
	}

	if _, err := st.GetArchivePartition(ctxFor(2, ""), p.ID); codeOf(err) != domain.CodeNotFound {
		t.Fatalf("tenant 2 reads partition: %v", err)
	}
	if _, err := st.GetArchivePartition(ctx, "not-a-uuid"); codeOf(err) != domain.CodeNotFound {
		t.Fatalf("malformed id = %v", err)
	}
	if _, err := st.RegisterArchivePartition(ctx, "audit", "audit_event", "2024-02", true, false); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListArchivePartitions(ctx, PartitionFilter{Status: domain.ArchiveRestored})
	if err != nil || len(list) != 1 || list[0].ID != p.ID {
		t.Fatalf("list RESTORED = %+v, %v", list, err)
	}
	if list, _ = st.ListArchivePartitions(ctx, PartitionFilter{Domain: "audit"}); len(list) != 1 || !list[0].LegalHold {
		t.Fatalf("list audit = %+v", list)
	}
}

func scheduleIn() domain.Schedule {
	return domain.Schedule{Name: "Weekly revenue", TargetType: domain.TargetDashboard, TargetID: "finance-overview",
		Cron: "0 6 * * 1", Timezone: "Europe/Berlin", Format: "PDF",
		Destination: domain.Destination{Type: domain.DestinationEmail, Targets: []string{"cfo@example.com"}}}
}

func TestCreateSchedule_IdempotencyAndNextRun(t *testing.T) {
	st := New(migrated(t))
	ctx := ctxFor(1, "")
	now := time.Date(2026, 1, 4, 12, 0, 0, 0, time.UTC)

	first, err := st.CreateSchedule(ctx, "key-1", "hash-a", scheduleIn(), now)
	if err != nil || first.Status != 201 || first.Replayed {
		t.Fatalf("create = %+v, %v", first, err)
	}
	var created domain.Schedule
	if err := json.Unmarshal(first.Body, &created); err != nil {
		t.Fatal(err)
	}
	// AC-46.10: 0 6 * * 1 Europe/Berlin next fires Monday 06:00 CET (05:00 UTC).
	if created.NextRunAt == nil || !created.NextRunAt.Equal(time.Date(2026, 1, 5, 5, 0, 0, 0, time.UTC)) ||
		created.Version != 1 || created.Destination.Targets[0] != "cfo@example.com" {
		t.Fatalf("created = %+v", created)
	}

	replay, err := st.CreateSchedule(ctx, "key-1", "hash-a", scheduleIn(), now)
	var replayed domain.Schedule
	if err != nil || !replay.Replayed || replay.Status != 201 || json.Unmarshal(replay.Body, &replayed) != nil ||
		replayed.ID != created.ID || !replayed.NextRunAt.Equal(*created.NextRunAt) {
		t.Fatalf("replay = %+v, %v; want the stored response", replay, err)
	}
	if list, _ := st.ListSchedules(ctx); len(list) != 1 {
		t.Fatalf("a replay must not insert a second schedule: %d", len(list))
	}
	if _, err := st.CreateSchedule(ctx, "key-1", "hash-b", scheduleIn(), now); codeOf(err) != domain.CodeIdempotencyReuse {
		t.Fatalf("key reuse = %v, want %s", err, domain.CodeIdempotencyReuse)
	}
	// The same key in another tenant is a different key.
	if other, err := st.CreateSchedule(ctxFor(2, ""), "key-1", "hash-b", scheduleIn(), now); err != nil || other.Replayed {
		t.Fatalf("tenant 2 with the same key = %+v, %v", other, err)
	}
	bad := scheduleIn()
	bad.Cron = "61 * * * *"
	if _, err := st.CreateSchedule(ctx, "key-2", "hash-c", bad, now); codeOf(err) != domain.CodeValidation {
		t.Fatalf("bad cron = %v", err)
	}

	list, err := st.ListSchedules(ctx)
	if err != nil || len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("list = %+v, %v; want only tenant 1's schedule", list, err)
	}
	got, err := st.GetSchedule(ctx, created.ID)
	if err != nil || got.Name != "Weekly revenue" || got.Format != "PDF" {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if _, err := st.GetSchedule(ctxFor(2, ""), created.ID); codeOf(err) != domain.CodeNotFound {
		t.Fatalf("tenant 2 reads tenant 1's schedule: %v", err)
	}
}

// The slice-2 tables isolate tenants through row-level security alone, as a
// non-owner application role sees them.
func TestSlice2Tables_RowLevelSecurity(t *testing.T) {
	if os.Getenv(testkit.EnvAppURL) == "" {
		t.Skipf("%s not set; skipping RLS test", testkit.EnvAppURL)
	}
	admin, app := testkit.DBWithApp(t)
	bg := context.Background()
	if err := dbx.Migrate(bg, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := New(app)
	now := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	if _, _, err := st.PutDQRule(ctxFor(1, ""), vendorTaxRule(), 0, false, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecordDQScore(ctxFor(1, ""), "vendor_tax_id", now, 100, 1); err != nil {
		t.Fatal(err)
	}
	// Every write path runs as passage_app, so the grants in 0003 are exercised.
	if _, _, err := st.PutDQRule(ctxFor(1, ""), vendorTaxRule(), 1, true, now); err != nil {
		t.Fatalf("replace rule as app role: %v", err)
	}
	if _, err := st.RecordDQScore(ctxFor(1, ""), "vendor_tax_id", now.Add(time.Hour), 100, 0); err != nil {
		t.Fatalf("re-score the same day as app role: %v", err)
	}
	part, err := st.RegisterArchivePartition(ctxFor(1, ""), "ledger", "journal_line", "2024-01", false, false)
	if err != nil {
		t.Fatal(err)
	}
	hash := domain.ContentHash([]string{"a", "b"})
	if _, err := st.RecordArchiveVerification(ctxFor(1, ""), part.ID, 2, 2, hash, hash, now); err != nil {
		t.Fatalf("verify as app role: %v", err)
	}
	if _, err := st.DropArchivePartition(ctxFor(1, ""), part.ID); err != nil {
		t.Fatalf("drop as app role: %v", err)
	}
	if _, _, err := st.RestoreArchivePartition(ctxFor(1, ""), part.ID, domain.DefaultRestoreTTLDays, now); err != nil {
		t.Fatalf("restore as app role: %v", err)
	}
	for range 2 {
		if _, err := st.CreateSchedule(ctxFor(1, ""), "k", "h", scheduleIn(), now); err != nil {
			t.Fatalf("create or replay schedule as app role: %v", err)
		}
	}
	for _, stmt := range []string{`delete from dq_rule`, `delete from dq_score`, `delete from archive_partition`, `update report_schedule set name = name`} {
		err := dbx.WithTenantTx(ctxFor(1, ""), app, func(tx pgx.Tx) error {
			_, err := tx.Exec(bg, stmt)
			return err
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("%q as app role = %v; want insufficient_privilege 42501", stmt, err)
		}
	}
	var outboxRows int
	if err := admin.QueryRow(bg, `select count(*) from outbox where tenant_id = $1`, testkit.TenantID(2)).Scan(&outboxRows); err != nil || outboxRows != 0 {
		t.Fatalf("tenant 2 owns %d outbox rows, %v", outboxRows, err)
	}
	for _, table := range []string{"dq_rule", "dq_score", "archive_partition", "report_schedule", "idempotency_keys"} {
		var mine, theirs int
		count := func(n int, dst *int) {
			if err := dbx.WithTenantTx(ctxFor(n, ""), app, func(tx pgx.Tx) error {
				return tx.QueryRow(bg, `select count(*) from `+table).Scan(dst)
			}); err != nil {
				t.Fatal(err)
			}
		}
		count(1, &mine)
		count(2, &theirs)
		if mine == 0 || theirs != 0 {
			t.Errorf("%s: tenant 1 sees %d rows, tenant 2 sees %d; want >0 and 0", table, mine, theirs)
		}
	}
	// A tenant-2 transaction cannot write a tenant-1 row.
	err = dbx.WithTenantTx(ctxFor(2, ""), app, func(tx pgx.Tx) error {
		_, err := tx.Exec(bg, `insert into dq_rule (tenant_id, rule_id, asset_fqn, rule_type, expression, severity, owner_user_id, status)
			values ($1, 'x_rule', 'core.vendor', 'COMPLETENESS', 'x', 'LOW', $2, 'ACTIVE')`, testkit.TenantID(1), ruleOwner)
		return err
	})
	if err == nil {
		t.Fatal("RLS must refuse a row for another tenant")
	}
}
