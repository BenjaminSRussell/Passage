// Command passage-server is the single Windows binary of Passage: it supervises the Go services as child processes on loopback
// ports, runs the built-in gateway (JWT verification, OPA decision, path routing) and serves the embedded web app. It runs as a
// console program (serve) or, on Windows, as the "Passage" service the MSI installs. See docs/adr/0004-windows-packaging.md.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"
)

func main() {
	os.Exit(execute(os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}

func execute(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 {
		if inServiceMode() {
			args = []string{"serve"}
		} else {
			_, _ = fmt.Fprintln(stderr, "usage: passage-server serve|migrate|doctor|version")
			return 2
		}
	}
	switch args[0] {
	case "version":
		_, _ = fmt.Fprintf(stdout, "passage-server %s %s/%s fips-build=%v web=%v\n", version, runtime.GOOS, runtime.GOARCH, fipsBuilt(), webEmbedded)
		return 0
	case "doctor":
		return doctorCmd(args[1:], stdout, stderr, getenv)
	case "migrate":
		return migrateCmd(args[1:], stderr, getenv)
	case "serve":
		return serveCmd(stderr, getenv)
	}
	_, _ = fmt.Fprintf(stderr, "passage-server: unknown command %q\n", args[0])
	return 2
}

func doctorCmd(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, procs, err := loadConfig(getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "FAIL config: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	results := Doctor(ctx, cfg, procs, RealDeps(fipsEnabled, fipsBuilt))
	if *asJSON {
		_ = json.NewEncoder(stdout).Encode(results)
	} else {
		WriteResults(stdout, results)
	}
	if Failed(results) {
		return 1
	}
	return 0
}

func migrateCmd(args []string, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	timeout := fs.Duration("timeout", 10*time.Minute, "how long to wait for every service to migrate")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log := slog.New(slog.NewTextHandler(stderr, nil))
	if err := runMigrate(ctx, getenv, log, *timeout); err != nil {
		_, _ = fmt.Fprintln(stderr, "passage-server migrate:", err)
		return 1
	}
	_, _ = fmt.Fprintln(stderr, "migrations applied")
	return 0
}

func serveCmd(stderr io.Writer, getenv func(string) string) int {
	if inServiceMode() {
		if err := runAsService(getenv); err != nil {
			_, _ = fmt.Fprintln(stderr, "passage-server:", err)
			return 1
		}
		return 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log := slog.New(slog.NewJSONHandler(stderr, nil))
	if err := runServe(ctx, getenv, log); err != nil && !errors.Is(err, context.Canceled) {
		_, _ = fmt.Fprintln(stderr, "passage-server:", err)
		return 1
	}
	return 0
}
