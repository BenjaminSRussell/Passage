package stack

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	Tenant      = "00000000-0000-0000-0000-000000000001"
	LegalEntity = "00000000-0000-0000-0000-0000000000e1"
	readyWait   = 90 * time.Second
)

type Config struct {
	Root     string
	AdminDB  string
	DBPrefix string
	RunDir   string
	BasePort int
	KeepDBs  bool
}

type service struct {
	name    string
	callers []string
	env     func(s *Stack) map[string]string
}

var services = []service{
	{name: "ledger", callers: []string{"receiving"}, env: func(*Stack) map[string]string {
		return map[string]string{"LEDGER_PERIOD_CHECK": "disabled"}
	}},
	{name: "numbering", callers: []string{"party"}},
	{name: "party", callers: []string{"receiving"}, env: func(s *Stack) map[string]string {
		return map[string]string{"NUMBERING_URL": s.URL("numbering")}
	}},
	{name: "purchasing", callers: []string{"receiving"}},
	{name: "receiving", env: func(s *Stack) map[string]string {
		return map[string]string{
			"PASSAGE_ALLOW_FAKES": "1",
			"LEDGER_URL":          s.URL("ledger"),
			"PURCHASING_URL":      s.URL("purchasing"),
			"PARTY_URL":           s.URL("party"),
			"POSTING_LEDGER_ID":   s.LedgerID,
		}
	}},
}

var startOrder = []string{"ledger", "numbering", "party", "purchasing", "receiving"}

type proc struct {
	name string
	cmd  *exec.Cmd
	done chan struct{}
	log  string
}

type Stack struct {
	cfg      Config
	LedgerID string
	ports    map[string]int
	admin    map[string]int
	seeds    map[string][]byte
	dbs      map[string]string
	procs    map[string]*proc
	mu       sync.Mutex
}

func New(cfg Config) (*Stack, error) {
	if cfg.Root == "" || cfg.AdminDB == "" {
		return nil, errors.New("stack: Root and AdminDB are required")
	}
	if cfg.DBPrefix == "" {
		cfg.DBPrefix = "passage"
	}
	if cfg.RunDir == "" {
		cfg.RunDir = filepath.Join(cfg.Root, ".run")
	}
	s := &Stack{cfg: cfg, ports: map[string]int{}, admin: map[string]int{}, seeds: map[string][]byte{}, dbs: map[string]string{}, procs: map[string]*proc{}}
	for i, svc := range services {
		seed := make([]byte, ed25519.SeedSize)
		if _, err := rand.Read(seed); err != nil {
			return nil, err
		}
		s.seeds[svc.name] = seed
		var err error
		if s.ports[svc.name], err = pickPort(cfg.BasePort, i*2); err != nil {
			return nil, err
		}
		if s.admin[svc.name], err = pickPort(cfg.BasePort, i*2+1); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func pickPort(base, offset int) (int, error) {
	if base > 0 {
		return base + offset, nil
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func (s *Stack) URL(name string) string { return fmt.Sprintf("http://127.0.0.1:%d", s.ports[name]) }

func (s *Stack) Names() []string { return append([]string(nil), startOrder...) }

func (s *Stack) DatabaseName(name string) string { return s.cfg.DBPrefix + "_" + name }

func (s *Stack) databaseURL(name string) (string, error) {
	u, err := url.Parse(s.cfg.AdminDB)
	if err != nil {
		return "", err
	}
	u.Path = "/" + s.DatabaseName(name)
	return u.String(), nil
}

func (s *Stack) psql(args ...string) error {
	out, err := exec.Command("psql", append([]string{s.cfg.AdminDB, "-v", "ON_ERROR_STOP=1", "-X", "-q"}, args...)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("psql: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (s *Stack) queryOne(service, query string) (string, error) {
	dbURL, err := s.databaseURL(service)
	if err != nil {
		return "", err
	}
	out, err := exec.Command("psql", dbURL, "-X", "-A", "-t", "-c", query).Output()
	if err != nil {
		return "", fmt.Errorf("psql %s: %w", service, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (s *Stack) ensureDatabase(name string, fresh bool) error {
	db := s.DatabaseName(name)
	if fresh {
		if err := s.psql("-c", fmt.Sprintf(`DROP DATABASE IF EXISTS "%s" WITH (FORCE)`, db)); err != nil {
			return err
		}
	}
	out, err := exec.Command("psql", s.cfg.AdminDB, "-X", "-A", "-t", "-c", fmt.Sprintf(`SELECT 1 FROM pg_database WHERE datname = '%s'`, db)).Output()
	if err != nil {
		return fmt.Errorf("psql: %w", err)
	}
	if strings.TrimSpace(string(out)) == "1" {
		return nil
	}
	return s.psql("-c", fmt.Sprintf(`CREATE DATABASE "%s"`, db))
}

func (s *Stack) Build(ctx context.Context) error {
	if err := os.MkdirAll(s.binDir(), 0o755); err != nil {
		return err
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(startOrder))
	for _, name := range startOrder {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.CommandContext(ctx, "go", "build", "-o", filepath.Join(s.binDir(), name), "./cmd/"+name)
			cmd.Dir = filepath.Join(s.cfg.Root, "services", name)
			if out, err := cmd.CombinedOutput(); err != nil {
				errs <- fmt.Errorf("build %s: %w\n%s", name, err, out)
			}
		}()
	}
	wg.Wait()
	close(errs)
	return <-errs
}

func (s *Stack) binDir() string { return filepath.Join(s.cfg.RunDir, "bin") }

func (s *Stack) publicKeys(target service) string {
	var parts []string
	for _, caller := range target.callers {
		pub := ed25519.NewKeyFromSeed(s.seeds[caller]).Public().(ed25519.PublicKey)
		parts = append(parts, caller+"="+base64.StdEncoding.EncodeToString(pub))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func (s *Stack) environ(svc service) ([]string, error) {
	dbURL, err := s.databaseURL(svc.name)
	if err != nil {
		return nil, err
	}
	env := map[string]string{
		"DATABASE_URL":                dbURL,
		"MIGRATE_ON_START":            "true",
		"HTTP_ADDR":                   fmt.Sprintf("127.0.0.1:%d", s.ports[svc.name]),
		"ADMIN_ADDR":                  fmt.Sprintf("127.0.0.1:%d", s.admin[svc.name]),
		"PASSAGE_SERVICE_NAME":        svc.name,
		"PASSAGE_SERVICE_SIGNING_KEY": base64.StdEncoding.EncodeToString(s.seeds[svc.name]),
	}
	if len(svc.callers) > 0 {
		env["PASSAGE_SERVICE_PUBLIC_KEYS"] = s.publicKeys(svc)
	}
	if svc.env != nil {
		for k, v := range svc.env(s) {
			env[k] = v
		}
	}
	out := append([]string(nil), os.Environ()...)
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out, nil
}

func (s *Stack) launch(ctx context.Context, svc service) error {
	env, err := s.environ(svc)
	if err != nil {
		return err
	}
	p, err := s.spawn(svc.name, "", env, filepath.Join(s.binDir(), svc.name))
	if err != nil {
		return err
	}
	return s.waitReady(ctx, svc.name, p)
}

// Spawn runs one more process under the stack's supervision: its output goes to the run directory's logs, FirstExit
// reports it, and Stop ends it.
func (s *Stack) Spawn(name, dir string, extraEnv map[string]string, command string, args ...string) error {
	env := append([]string(nil), os.Environ()...)
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}
	_, err := s.spawn(name, dir, env, command, args...)
	return err
}

func (s *Stack) spawn(name, dir string, env []string, command string, args ...string) (*proc, error) {
	if err := os.MkdirAll(filepath.Join(s.cfg.RunDir, "logs"), 0o755); err != nil {
		return nil, err
	}
	logPath := filepath.Join(s.cfg.RunDir, "logs", name+".log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(command, args...)
	cmd.Dir, cmd.Env = dir, env
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("start %s: %w", name, err)
	}
	p := &proc{name: name, cmd: cmd, done: make(chan struct{}), log: logPath}
	go func() {
		_ = cmd.Wait()
		_ = logFile.Close()
		close(p.done)
	}()
	s.mu.Lock()
	s.procs[name] = p
	s.mu.Unlock()
	return p, nil
}

func (s *Stack) waitReady(ctx context.Context, name string, p *proc) error {
	deadline := time.Now().Add(readyWait)
	for time.Now().Before(deadline) {
		select {
		case <-p.done:
			return fmt.Errorf("%s exited before it was ready:\n%s", name, s.LogTail(name))
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		resp, err := http.Get(s.URL(name) + "/readyz")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("%s was not ready within %s:\n%s", name, readyWait, s.LogTail(name))
}

func (s *Stack) LogTail(name string) string {
	raw, err := os.ReadFile(filepath.Join(s.cfg.RunDir, "logs", name+".log"))
	if err != nil {
		return err.Error()
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) > 25 {
		lines = lines[len(lines)-25:]
	}
	return strings.Join(lines, "\n")
}

// Start builds every service, gives each its own database, starts them in dependency order and seeds the ledger
// that receiving posts to.
func (s *Stack) Start(ctx context.Context, freshDatabases bool) error {
	if err := s.Build(ctx); err != nil {
		return err
	}
	byName := map[string]service{}
	for _, svc := range services {
		byName[svc.name] = svc
		if err := s.ensureDatabase(svc.name, freshDatabases); err != nil {
			return err
		}
	}
	for _, name := range startOrder {
		if name == "receiving" {
			id, err := s.SeedLedger(ctx)
			if err != nil {
				return fmt.Errorf("seed ledger: %w", err)
			}
			s.LedgerID = id
		}
		if name == "party" {
			if err := s.SeedNumbering(ctx); err != nil {
				return fmt.Errorf("seed numbering: %w", err)
			}
		}
		if err := s.launch(ctx, byName[name]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Stack) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.procs {
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Signal(os.Interrupt)
		}
	}
	for _, p := range s.procs {
		select {
		case <-p.done:
		case <-time.After(5 * time.Second):
			_ = p.cmd.Process.Kill()
			<-p.done
		}
	}
	if !s.cfg.KeepDBs {
		for _, svc := range services {
			_ = s.psql("-c", fmt.Sprintf(`DROP DATABASE IF EXISTS "%s" WITH (FORCE)`, s.DatabaseName(svc.name)))
		}
	}
}

// FirstExit reports the name of the first supervised process to exit.
func (s *Stack) FirstExit() <-chan string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(chan string, len(s.procs))
	for _, p := range s.procs {
		go func() { <-p.done; out <- p.name }()
	}
	return out
}
