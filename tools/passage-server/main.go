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
	"path/filepath"
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
			_, _ = fmt.Fprintln(stderr, "usage: passage-server serve|migrate|doctor|services|wrap|version")
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
	case "wrap":
		return wrapCmd(args[1:], stderr, getenv)
	case "services":
		return servicesCmd(stdout, stderr, getenv)
	}
	_, _ = fmt.Fprintf(stderr, "passage-server: unknown command %q\n", args[0])
	return 2
}

// listedChild is one resolved child as `passage-server services` prints it. The database URLs are left out: they carry the
// owner's or passage_app's password.
type listedChild struct {
	Name      string            `json:"name"`
	Exe       string            `json:"exe"`
	Args      []string          `json:"args"`
	Env       map[string]string `json:"env"`
	Port      int               `json:"port"`
	AdminPort int               `json:"adminPort"`
	ReadyPath string            `json:"readyPath"`
	Optional  bool              `json:"optional"`
}

// servicesCmd prints, as JSON, the children serve would start with this configuration: the start command, ports and readiness
// path of each. deploy/windows/scripts/Test-ServiceBundles.ps1 starts the Node and Python services from this list, so the start
// commands it tests are the ones the conf.d files resolve to.
func servicesCmd(stdout, stderr io.Writer, getenv func(string) string) int {
	_, procs, err := loadConfig(getenv, ModeServe)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "FAIL config: %v\n", err)
		return 1
	}
	out := make([]listedChild, 0, len(procs))
	for _, p := range procs {
		env := map[string]string{}
		for k, v := range p.Env {
			if k != "DATABASE_URL" && k != "MIGRATE_DATABASE_URL" {
				env[k] = v
			}
		}
		ready := p.ReadyPath
		if ready == "" {
			ready = "/readyz"
		}
		out = append(out, listedChild{Name: p.Name, Exe: p.Exe, Args: p.Args, Env: env, Port: p.Port, AdminPort: p.AdminPort, ReadyPath: ready, Optional: p.Optional})
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
	return 0
}

func doctorCmd(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, procs, err := loadConfig(getenv, ModeServe)
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

// runFunc is what runs under a console or the service control manager.
type runFunc func(ctx context.Context, log *slog.Logger) error

func serveCmd(stderr io.Writer, getenv func(string) string) int {
	run := func(ctx context.Context, log *slog.Logger) error { return runServe(ctx, getenv, log) }
	return runMain(stderr, getenv, "Passage", filepath.Join(DefaultDataDir(getenv), "logs"), run)
}

// wrapCmd: passage-server wrap -service NAME [-logdir DIR] -- exe args...  runs one external program as the service NAME.
func wrapCmd(args []string, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("wrap", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("service", "", "Windows service name")
	logDir := fs.String("logdir", "", "directory for the program's log (default: <data>\\logs)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if *name == "" || len(rest) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: passage-server wrap -service NAME [-logdir DIR] -- program [args...]")
		return 2
	}
	dir := *logDir
	if dir == "" {
		dir = filepath.Join(DefaultDataDir(getenv), "logs")
	}
	run := func(ctx context.Context, log *slog.Logger) error {
		return runWrap(ctx, log, dir, *name, rest[0], rest[1:])
	}
	return runMain(stderr, getenv, *name, dir, run)
}

func runMain(stderr io.Writer, getenv func(string) string, serviceName, logDir string, run runFunc) int {
	if inServiceMode() {
		if err := runAsService(serviceName, logDir, run); err != nil {
			_, _ = fmt.Fprintln(stderr, "passage-server:", err)
			return 1
		}
		return 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log := slog.New(slog.NewJSONHandler(stderr, nil))
	if err := run(ctx, log); err != nil && !errors.Is(err, context.Canceled) {
		_, _ = fmt.Fprintln(stderr, "passage-server:", err)
		return 1
	}
	return 0
}
