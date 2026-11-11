package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/BenjaminSRussell/passage/services/identity"
)

// version is set at link time: -ldflags "-X main.version=1.2.3".
var version = "dev"

const logMaxBytes = 50 << 20

func loadConfig(getenv func(string) string) (Config, []Process, error) {
	cfg, err := LoadConfig(ConfigPath(getenv), platformOverrides())
	if err != nil {
		return cfg, nil, err
	}
	return cfg.Resolve(getenv, listFiles)
}

func listFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// serviceLogs sends each child's output to <data>\logs\<name>.log, rotated.
func serviceLogs(dataDir string) func(string) io.Writer {
	return func(name string) io.Writer {
		return &RotatingFile{Path: filepath.Join(dataDir, "logs", name+".log"), MaxBytes: logMaxBytes, Keep: 5}
	}
}

// runServe runs the supervisor and the gateway until ctx ends. extraLog, when set, also receives the server's own log (Event Log).
func runServe(ctx context.Context, getenv func(string) string, log *slog.Logger) error {
	cfg, procs, err := loadConfig(getenv)
	if err != nil {
		return err
	}
	if err := checkFIPSStartup(cfg, getenv); err != nil {
		return err
	}
	keys, err := LoadOrCreateKeys(filepath.Join(cfg.DataDir, "keys"), names(procs))
	if err != nil {
		return err
	}
	keys.Apply(procs)

	sup := NewSupervisor(ExecRunner{Logs: serviceLogs(cfg.DataDir)}, ReadyProbe(&http.Client{Timeout: 3 * time.Second}), log)
	guard, err := buildGuard(cfg)
	if err != nil {
		return err
	}
	handler := NewGateway(guard, upstreamsOf(procs), webFS(), sup.Health)
	server := &http.Server{Addr: cfg.Listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	if cfg.TLS.CertFile != "" {
		server.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}

	errs := make(chan error, 2)
	go func() { errs <- sup.Run(ctx, procs) }()
	go func() {
		log.Info("gateway listening", "addr", cfg.Listen, "tls", cfg.TLS.CertFile != "", "version", version)
		var err error
		if cfg.TLS.CertFile != "" {
			err = server.ListenAndServeTLS(cfg.TLS.CertFile, cfg.TLS.KeyFile)
		} else {
			err = server.ListenAndServe()
		}
		if !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()
	var result error
	select {
	case <-ctx.Done():
	case result = <-errs:
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
	if result == nil {
		select {
		case result = <-errs:
		case <-time.After(30 * time.Second):
		}
	}
	return result
}

// runMigrate starts every service once with MIGRATE_ON_START=true, waits until all are ready (each applies its migrations before it
// serves) and stops them. It is the controlled migration step of a major upgrade.
func runMigrate(ctx context.Context, getenv func(string) string, log *slog.Logger, timeout time.Duration) error {
	cfg, procs, err := loadConfig(getenv)
	if err != nil {
		return err
	}
	keys, err := LoadOrCreateKeys(filepath.Join(cfg.DataDir, "keys"), names(procs))
	if err != nil {
		return err
	}
	keys.Apply(procs)
	return migrateWith(ctx, NewSupervisor(ExecRunner{Logs: serviceLogs(cfg.DataDir)}, ReadyProbe(&http.Client{Timeout: 3 * time.Second}), log), procs, timeout)
}

func migrateWith(ctx context.Context, sup *Supervisor, procs []Process, timeout time.Duration) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sup.Policy.MaxRestarts = min(sup.Policy.MaxRestarts, 1) // a service that crashes while migrating is a failed migration, not a flap to ride out
	done := make(chan error, 1)
	go func() { done <- sup.Run(ctx, procs) }()
	readyCtx, cancelReady := context.WithTimeout(ctx, timeout)
	defer cancelReady()
	waitErr := waitStarted(readyCtx, sup)
	cancel()
	runErr := <-done
	if waitErr != nil {
		return fmt.Errorf("migration failed: %w", waitErr)
	}
	return runErr
}

// waitStarted waits until every non-optional process is healthy; it fails early when one reaches the failed state.
func waitStarted(ctx context.Context, sup *Supervisor) error {
	for {
		for _, st := range sup.Statuses() {
			if st.State == StateFailed && !st.Optional {
				return fmt.Errorf("%s failed to migrate or start: %s", st.Name, st.LastErr)
			}
		}
		if ready, _ := sup.Health(); ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.Join(errors.New("services did not become ready in time"), ctx.Err())
		case <-sup.After(500 * time.Millisecond):
		}
	}
}

func names(procs []Process) []string {
	out := make([]string, len(procs))
	for i, p := range procs {
		out[i] = p.Name
	}
	return out
}

func upstreamsOf(procs []Process) map[string]*url.URL {
	out := map[string]*url.URL{}
	for _, p := range procs {
		u, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", p.Port))
		out[p.Route] = u
	}
	return out
}

func buildGuard(cfg Config) (Guard, error) {
	verifier, err := identity.NewJWTVerifier(cfg.OIDCIssuer, cfg.OIDCAudience, cfg.TenantID, cfg.OIDCJWKSURL, nil)
	if err != nil {
		return nil, fmt.Errorf("jwt verifier: %w", err)
	}
	opa, err := identity.NewOPAClient(cfg.OPAURL, nil)
	if err != nil {
		return nil, fmt.Errorf("opa client: %w", err)
	}
	return func(next http.Handler) http.Handler {
		return verifier.Middleware(identity.RequireOPAForRequest(opa)(next))
	}, nil
}
