package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// LagSource measures a region's replication lag in whole seconds from the real replication state.
type LagSource interface {
	Lag(ctx context.Context) (seconds int, err error)
	Close(ctx context.Context) error
}

const (
	// ModeStandby runs against the standby (a CNPG replica cluster) and reads how far replay is behind what it received.
	ModeStandby = "standby"
	// ModePrimary runs against the primary and reads pg_stat_replication for the standby clients.
	ModePrimary = "primary"

	// receiptMaxAgeSeconds is how recent the WAL receiver's last message from the primary must be. An idle primary still sends
	// keepalives well inside it (wal_sender_timeout / 2, 30 s by default).
	receiptMaxAgeSeconds = 90

	primaryLagSQL = `select count(*), coalesce(max(extract(epoch from coalesce(replay_lag, flush_lag, write_lag, interval '0'))), 0)
		from pg_stat_replication where application_name like $1`
)

type pgLag struct {
	conn    *pgx.Conn
	mode    string
	pattern string
}

// pgLagSource connects with OPS_LAG_DATABASE_URL; OPS_LAG_MODE is standby (default) or primary and
// OPS_LAG_APPLICATION_NAME is the LIKE pattern of the standby clients in primary mode (default %).
func pgLagSource(ctx context.Context, getenv func(string) string) (LagSource, error) {
	url := getenv("OPS_LAG_DATABASE_URL")
	if url == "" {
		return nil, errors.New("OPS_LAG_DATABASE_URL is required: a read-only connection to the cluster whose lag is reported")
	}
	mode := getenv("OPS_LAG_MODE")
	if mode == "" {
		mode = ModeStandby
	}
	if mode != ModeStandby && mode != ModePrimary {
		return nil, fmt.Errorf("OPS_LAG_MODE must be %s or %s", ModeStandby, ModePrimary)
	}
	pattern := getenv("OPS_LAG_APPLICATION_NAME")
	if pattern == "" {
		pattern = "%"
	}
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return nil, err
	}
	return &pgLag{conn: conn, mode: mode, pattern: pattern}, nil
}

func (p *pgLag) Close(ctx context.Context) error { return p.conn.Close(ctx) }

// failedSource stands in for a source that could not connect: every reading is that error, so the reporter reports the
// unknown value (and reconnects on the next interval) instead of exiting.
type failedSource struct{ err error }

func (f failedSource) Lag(context.Context) (int, error) { return 0, f.err }
func (failedSource) Close(context.Context) error        { return nil }

func (p *pgLag) Lag(ctx context.Context) (int, error) {
	if p.mode == ModePrimary {
		var clients int
		var secs float64
		if err := p.conn.QueryRow(ctx, primaryLagSQL, p.pattern).Scan(&clients, &secs); err != nil {
			return 0, err
		}
		if clients == 0 {
			// No standby is connected: that is not "caught up".
			return 0, errors.New("no standby is connected to this primary (pg_stat_replication is empty)")
		}
		return roundLag(secs), nil
	}
	var secs *float64
	if err := p.conn.QueryRow(ctx, standbyLagSQL).Scan(&secs); err != nil {
		return 0, err
	}
	if secs == nil {
		return 0, errors.New("this server is not a streaming standby (not in recovery, or its WAL receiver is not streaming and recently heard from the primary)")
	}
	return roundLag(*secs), nil
}

// "Everything received is replayed" means caught up only while the WAL receiver is streaming and recently heard from the primary:
// with the receiver disconnected, received == replayed is just a standby that stopped receiving, and the lag is unknown. Reading
// pg_stat_wal_receiver needs a role with pg_monitor (others see NULL columns, which also reads as unknown).
var standbyLagSQL = `select case
		when not pg_is_in_recovery() then null
		when not exists (select 1 from pg_stat_wal_receiver where status = 'streaming'
		                 and last_msg_receipt_time > now() - make_interval(secs => ` + strconv.Itoa(receiptMaxAgeSeconds) + `)) then null
		when pg_last_wal_receive_lsn() = pg_last_wal_replay_lsn() then 0
		else extract(epoch from now() - pg_last_xact_replay_timestamp()) end`

func roundLag(secs float64) int {
	if secs < 0 || math.IsNaN(secs) {
		return 0
	}
	if secs > 1<<30 {
		return 1 << 30
	}
	return int(math.Ceil(secs))
}

// reportOnce measures and reports. A measurement that fails reports unknownLag, a value far above any failover limit,
// so a broken reporter makes the failover refuse instead of leaving the last good, now stale, number in place.
func reportOnce(ctx context.Context, c *client, src LagSource, region string, unknownLag int) (int, error) {
	lag, err := src.Lag(ctx)
	if err != nil {
		slog.Warn("lag measurement failed; reporting the unknown-lag value", "region", region, "err", err, "reported", unknownLag)
		lag = unknownLag
	}
	if _, rerr := c.do(ctx, http.MethodPut, "/v1/ops/regions/"+esc(region)+"/lag", nil, map[string]any{"lagSeconds": lag}, nil); rerr != nil {
		return lag, rerr
	}
	return lag, err
}

func runLagReport(ctx context.Context, args []string, out io.Writer, getenv func(string) string, d deps) error {
	fs := flag.NewFlagSet("lag-report", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var region string
	var once bool
	var interval time.Duration
	var unknown int
	fs.StringVar(&region, "region", "", "the region whose lag is reported")
	fs.BoolVar(&once, "once", false, "report once and exit")
	fs.DurationVar(&interval, "interval", 30*time.Second, "time between reports")
	fs.IntVar(&unknown, "unknown-lag-s", 86400, "lag reported when the measurement fails (fails closed)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if region == "" {
		return errors.New("--region <name> is required")
	}
	if interval < time.Second {
		return errors.New("--interval must be at least 1s")
	}
	c, err := newClient(getenv, d.httpClient)
	if err != nil {
		return err
	}
	if d.lagSource == nil {
		return errors.New("no lag source configured")
	}
	var src LagSource
	defer func() {
		if src != nil {
			_ = src.Close(context.Background())
		}
	}()
	sleep := d.sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	for {
		// (Re)connect every interval while there is no working connection; a connect failure is a measurement failure.
		current := src
		if current == nil {
			opened, cerr := d.lagSource(ctx, getenv)
			if cerr != nil {
				current = failedSource{cerr}
			} else {
				src, current = opened, opened
			}
		}
		lag, err := reportOnce(ctx, c, current, region, unknown)
		if err != nil && src != nil {
			// the connection may be what broke: drop it and reconnect next time
			_ = src.Close(context.Background())
			src = nil
		}
		if err != nil {
			slog.Error("lag report", "region", region, "err", err)
		} else {
			fmt.Fprintf(out, "%s lag %d s\n", region, lag)
		}
		if once {
			return err
		}
		if err := sleep(ctx, interval); err != nil {
			return nil
		}
	}
}
