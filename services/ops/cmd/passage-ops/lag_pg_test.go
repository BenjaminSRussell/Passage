package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

// The real queries run against a plain (non-replicated) Postgres: they must be valid SQL, and they must not report an
// unreplicated server as caught up.
func TestPgLagSourceRefusesAServerWithoutReplication(t *testing.T) {
	url := os.Getenv("PASSAGE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PASSAGE_TEST_DATABASE_URL is not set")
	}
	for mode, want := range map[string]string{ModeStandby: "not a streaming standby", ModePrimary: "no standby is connected"} {
		env := map[string]string{"OPS_LAG_DATABASE_URL": url, "OPS_LAG_MODE": mode}
		src, err := pgLagSource(t.Context(), func(k string) string { return env[k] })
		if err != nil {
			t.Fatal(err)
		}
		_, lerr := src.Lag(t.Context())
		_ = src.Close(t.Context())
		if lerr == nil || !strings.Contains(lerr.Error(), want) {
			t.Fatalf("%s: err %v, want %q", mode, lerr, want)
		}
	}
	if _, err := pgLagSource(t.Context(), func(string) string { return "" }); err == nil {
		t.Fatal("no URL must fail")
	}
	if _, err := pgLagSource(t.Context(), func(k string) string {
		if k == "OPS_LAG_MODE" {
			return "sideways"
		}
		return url
	}); err == nil {
		t.Fatal("a bad mode must fail")
	}
}

// The standby query is run against stand-ins for the replication catalog (a schema that shadows pg_is_in_recovery, the WAL
// position functions and pg_stat_wal_receiver), because a real standby is not available here.
func TestStandbyLagNeedsAStreamingReceiverThatRecentlyHeardFromThePrimary(t *testing.T) {
	url := os.Getenv("PASSAGE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PASSAGE_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(context.Background()) }()
	schema := "mocklag_" + strings.ReplaceAll(ids.New(), "-", "")
	for _, q := range []string{
		"create schema " + schema,
		"create table " + schema + ".state (in_recovery boolean, status text, receipt_ago int, caught_up boolean, replay_ago int)",
		"create function " + schema + ".pg_is_in_recovery() returns boolean language sql as 'select in_recovery from " + schema + ".state'",
		"create function " + schema + ".pg_last_wal_receive_lsn() returns pg_lsn language sql as $$select '0/2'::pg_lsn$$",
		"create function " + schema + ".pg_last_wal_replay_lsn() returns pg_lsn language sql as 'select case when caught_up then ''0/2''::pg_lsn else ''0/1''::pg_lsn end from " + schema + ".state'",
		"create function " + schema + ".pg_last_xact_replay_timestamp() returns timestamptz language sql as 'select now() - replay_ago * interval ''1 second'' from " + schema + ".state'",
		"create view " + schema + ".pg_stat_wal_receiver as select status, now() - receipt_ago * interval '1 second' as last_msg_receipt_time from " + schema + ".state",
	} {
		if _, err := admin.Exec(ctx, q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	defer func() { _, _ = admin.Exec(context.Background(), "drop schema "+schema+" cascade") }()

	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.RuntimeParams["search_path"] = schema + ", pg_catalog"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	lag := &pgLag{conn: conn, mode: ModeStandby}

	set := func(inRecovery bool, status string, receiptAgo int, caughtUp bool, replayAgo int) {
		t.Helper()
		if _, err := admin.Exec(ctx, "delete from "+schema+".state"); err != nil {
			t.Fatal(err)
		}
		if _, err := admin.Exec(ctx, "insert into "+schema+".state values ($1, $2, $3, $4, $5)", inRecovery, status, receiptAgo, caughtUp, replayAgo); err != nil {
			t.Fatal(err)
		}
	}
	// streaming, recently heard, everything replayed: caught up
	set(true, "streaming", 5, true, 400)
	if n, err := lag.Lag(ctx); err != nil || n != 0 {
		t.Fatalf("caught up: %d %v", n, err)
	}
	// streaming and behind: the replay delay
	set(true, "streaming", 5, false, 42)
	if n, err := lag.Lag(ctx); err != nil || n < 42 || n > 44 {
		t.Fatalf("behind: %d %v", n, err)
	}
	// the receiver disconnected: received == replayed proves nothing, so it is unknown, not 0
	set(true, "stopped", 5, true, 400)
	if _, err := lag.Lag(ctx); err == nil || !strings.Contains(err.Error(), "not streaming") {
		t.Fatalf("a stopped receiver must read as unknown: %v", err)
	}
	// streaming per the catalog but nothing heard for minutes: unknown
	set(true, "streaming", 600, true, 400)
	if _, err := lag.Lag(ctx); err == nil {
		t.Fatal("a silent receiver must read as unknown")
	}
	// no receiver row at all
	set(true, "streaming", 5, true, 1)
	if _, err := admin.Exec(ctx, "delete from "+schema+".state"); err != nil {
		t.Fatal(err)
	}
	if _, err := lag.Lag(ctx); err == nil {
		t.Fatal("no receiver must read as unknown")
	}
	// not in recovery
	set(false, "streaming", 5, true, 1)
	if _, err := lag.Lag(ctx); err == nil {
		t.Fatal("a primary is not a standby")
	}
}
