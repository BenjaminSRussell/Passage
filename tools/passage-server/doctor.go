package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Level is the outcome of one doctor check.
type Level string

const (
	LevelOK   Level = "ok"
	LevelWarn Level = "warn"
	LevelFail Level = "fail"
)

// Result is one doctor line.
type Result struct {
	Name   string `json:"name"`
	Level  Level  `json:"level"`
	Detail string `json:"detail"`
}

// DBInspector is the part of a Postgres session doctor needs. Tests fake it.
type DBInspector interface {
	// Role reports the connected role.
	Role(ctx context.Context) (name string, superuser, bypassRLS bool, err error)
	// Migrations counts rows in schema_migrations; exists is false when the table is missing.
	Migrations(ctx context.Context) (count int, exists bool, err error)
	Close(ctx context.Context) error
}

// Deps are the doctor's outside-world calls, replaceable in tests.
type Deps struct {
	OpenDB      func(ctx context.Context, dsn string) (DBInspector, error)
	HTTPGet     func(ctx context.Context, url string) (status int, body []byte, err error)
	Dial        func(ctx context.Context, hostport string) error
	Stat        func(path string) (os.FileInfo, error)
	LoadKeyPair func(cert, key string) error
	Listen      func(addr string) error
	FIPSEnabled func() bool
	FIPSBuilt   func() bool
	WebEmbedded bool
	ProbeWrite  func(dir string) error
	// ServiceRunning reports whether the Passage Windows service is running now (so its own listener is not a conflict).
	ServiceRunning func() bool
}

// RealDeps wires Deps to the network and file system.
func RealDeps(fipsEnabled, fipsBuilt func() bool) Deps {
	client := &http.Client{Timeout: 10 * time.Second}
	return Deps{
		OpenDB: func(ctx context.Context, dsn string) (DBInspector, error) {
			conn, err := pgx.Connect(ctx, dsn)
			if err != nil {
				return nil, err
			}
			return pgInspector{conn}, nil
		},
		HTTPGet: func(ctx context.Context, u string) (int, []byte, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
			if err != nil {
				return 0, nil, err
			}
			resp, err := client.Do(req)
			if err != nil {
				return 0, nil, err
			}
			defer func() { _ = resp.Body.Close() }()
			b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			return resp.StatusCode, b, err
		},
		Dial: func(ctx context.Context, hp string) error {
			d := net.Dialer{Timeout: 5 * time.Second}
			c, err := d.DialContext(ctx, "tcp", hp)
			if err != nil {
				return err
			}
			return c.Close()
		},
		Stat:        os.Stat,
		LoadKeyPair: func(c, k string) error { _, err := tls.LoadX509KeyPair(c, k); return err },
		Listen: func(addr string) error {
			l, err := net.Listen("tcp", addr)
			if err != nil {
				return err
			}
			return l.Close()
		},
		FIPSEnabled:    fipsEnabled,
		FIPSBuilt:      fipsBuilt,
		WebEmbedded:    webEmbedded,
		ServiceRunning: serviceRunning,
		ProbeWrite: func(dir string) error {
			f, err := os.CreateTemp(dir, ".doctor-*")
			if err != nil {
				return err
			}
			name := f.Name()
			_ = f.Close()
			return os.Remove(name)
		},
	}
}

type pgInspector struct{ conn *pgx.Conn }

func (p pgInspector) Role(ctx context.Context) (string, bool, bool, error) {
	var name string
	var super, bypass bool
	err := p.conn.QueryRow(ctx, `select rolname, rolsuper, rolbypassrls from pg_roles where rolname = current_user`).Scan(&name, &super, &bypass)
	return name, super, bypass, err
}

func (p pgInspector) Migrations(ctx context.Context) (int, bool, error) {
	var exists bool
	if err := p.conn.QueryRow(ctx, `select to_regclass('schema_migrations') is not null`).Scan(&exists); err != nil || !exists {
		return 0, false, err
	}
	var n int
	err := p.conn.QueryRow(ctx, `select count(*) from schema_migrations`).Scan(&n)
	return n, true, err
}

func (p pgInspector) Close(ctx context.Context) error { return p.conn.Close(ctx) }

// Doctor runs every check. It never stops at the first failure so one run lists everything an installer has to fix.
func Doctor(ctx context.Context, cfg Config, procs []Process, d Deps) []Result {
	var out []Result
	add := func(name string, level Level, format string, a ...any) {
		out = append(out, Result{Name: name, Level: level, Detail: fmt.Sprintf(format, a...)})
	}
	if err := cfg.Validate(); err != nil {
		add("config", LevelFail, "%v", err)
	} else {
		add("config", LevelOK, "passage.yaml is valid")
	}
	checkFIPS(cfg, d, add)
	checkWeb(d, add)
	checkListen(cfg, d, add)
	checkTLS(cfg, d, add)
	checkData(cfg, d, add)
	checkPostgres(ctx, cfg, procs, d, add)
	checkOIDC(ctx, cfg, d, add)
	checkHTTPDep(ctx, "opa", cfg.OPAURL, "/health", d, add)
	checkDial(ctx, "kafka", cfg.KafkaBootstrap, d, add)
	checkDial(ctx, "temporal", cfg.TemporalHost, d, add)
	checkServices(procs, d, add)
	checkFeatures(cfg, d, add)
	return out
}

type adder func(name string, level Level, format string, a ...any)

func checkFIPS(cfg Config, d Deps, add adder) {
	switch {
	case cfg.RequireFIPS && !d.FIPSEnabled():
		add("fips", LevelFail, "requireFips is set but crypto/fips140.Enabled() is false (use the FIPS build and do not set GODEBUG=fips140=off)")
	case d.FIPSBuilt() && !d.FIPSEnabled():
		add("fips", LevelFail, "this is a GOFIPS140 build but FIPS mode is off")
	case d.FIPSEnabled():
		add("fips", LevelOK, "FIPS 140-3 mode is active")
	default:
		add("fips", LevelOK, "FIPS mode not required")
	}
}

func checkWeb(d Deps, add adder) {
	if d.WebEmbedded {
		add("web", LevelOK, "web app is embedded")
	} else {
		add("web", LevelWarn, "this build has no embedded web app (built without -tags webdist); only /api is served")
	}
}

func checkListen(cfg Config, d Deps, add adder) {
	if err := d.Listen(cfg.Listen); err != nil {
		if d.ServiceRunning != nil && d.ServiceRunning() {
			add("listen", LevelWarn, "%s is in use by the running Passage service", cfg.Listen)
			return
		}
		add("listen", LevelFail, "cannot bind %s: %v", cfg.Listen, err)
		return
	}
	add("listen", LevelOK, "%s is free", cfg.Listen)
}

func checkTLS(cfg Config, d Deps, add adder) {
	if cfg.TLS.CertFile == "" {
		switch {
		case loopbackAddr(cfg.Listen):
			add("tls", LevelWarn, "no certificate configured; plain HTTP on loopback only")
		case cfg.AllowPlaintext:
			add("tls", LevelWarn, "ALLOW_PLAINTEXT is set: %s serves plain HTTP; put a TLS-terminating proxy in front", cfg.Listen)
		default:
			add("tls", LevelFail, "listen address %s is not loopback and no tls.certFile/tls.keyFile is set", cfg.Listen)
		}
		return
	}
	if err := d.LoadKeyPair(cfg.TLS.CertFile, cfg.TLS.KeyFile); err != nil {
		add("tls", LevelFail, "certificate does not load: %v", err)
		return
	}
	add("tls", LevelOK, "certificate %s loads", filepath.Base(cfg.TLS.CertFile))
}

func checkData(cfg Config, d Deps, add adder) {
	if cfg.DataDir == "" {
		add("data-dir", LevelFail, "no data directory")
		return
	}
	if err := d.ProbeWrite(cfg.DataDir); err != nil {
		add("data-dir", LevelFail, "%s is not writable by the service account: %v", cfg.DataDir, err)
		return
	}
	add("data-dir", LevelOK, "%s is writable", cfg.DataDir)
}

// checkPostgres checks both database roles: the owner (PG_DSN: creates databases, owns the tables, runs migrations) and passage_app
// (PG_APP_DSN: what every serving child connects as, which must not bypass row-level security or be an owner).
func checkPostgres(ctx context.Context, cfg Config, procs []Process, d Deps, add adder) {
	if cfg.DatabaseDSN == "" {
		add("postgres-owner", LevelFail, "no PG_DSN (pgDsn): the database owner URL used to create and migrate the service databases")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ownerRole := ""
	if cfg.DatabaseDSN != "" {
		db, err := d.OpenDB(ctx, cfg.DatabaseDSN)
		if err != nil {
			add("postgres-owner", LevelFail, "cannot connect with PG_DSN: %s", redact(err.Error(), cfg.DatabaseDSN))
		} else {
			name, super, _, err := db.Role(ctx)
			_ = db.Close(ctx)
			if err != nil {
				add("postgres-owner", LevelFail, "cannot read the owner role: %v", err)
			} else {
				ownerRole = name
				if super {
					add("postgres-owner", LevelWarn, "owner role %s is a superuser; a dedicated owner role with CREATEDB is safer", name)
				} else {
					add("postgres-owner", LevelOK, "owner role %s connected", name)
				}
			}
		}
	}
	if cfg.AppDSN == "" {
		add("postgres-role", LevelFail, "no PG_APP_DSN: the services must connect as the passage_app role (NOBYPASSRLS), not as the owner")
		return
	}
	db, err := d.OpenDB(ctx, cfg.AppDSN)
	if err != nil {
		add("postgres", LevelFail, "cannot connect with PG_APP_DSN: %s", redact(err.Error(), cfg.AppDSN))
		return
	}
	role, super, bypass, err := db.Role(ctx)
	_ = db.Close(ctx)
	switch {
	case err != nil:
		add("postgres-role", LevelFail, "cannot read the role: %v", err)
	case bypass || super:
		add("postgres-role", LevelFail, "role %s has BYPASSRLS or SUPERUSER: row-level security would not protect tenants; use the passage_app role (NOBYPASSRLS)", role)
	case role == ownerRole:
		add("postgres-role", LevelFail, "PG_APP_DSN and PG_DSN use the same role %s: the app role must not be the owner of the tables", role)
	default:
		add("postgres-role", LevelOK, "role %s is NOBYPASSRLS and not the owner", role)
	}
	add("postgres", LevelOK, "connected")
	pending := []string{}
	for _, p := range procs {
		url, ok := p.Env["DATABASE_URL"]
		if !ok {
			continue
		}
		sdb, err := d.OpenDB(ctx, url)
		if err != nil {
			pending = append(pending, p.Name+" (database missing)")
			continue
		}
		n, exists, err := sdb.Migrations(ctx)
		_ = sdb.Close(ctx)
		if err != nil || !exists || n == 0 {
			pending = append(pending, p.Name)
		}
	}
	if len(pending) > 0 {
		add("migrations", LevelWarn, "not migrated yet: %s; run passage-server migrate", strings.Join(pending, ", "))
	} else {
		add("migrations", LevelOK, "every service database is migrated")
	}
}

func checkOIDC(ctx context.Context, cfg Config, d Deps, add adder) {
	if cfg.OIDCIssuer == "" {
		add("oidc", LevelFail, "no OIDC_ISSUER configured")
		return
	}
	status, body, err := d.HTTPGet(ctx, strings.TrimRight(cfg.OIDCIssuer, "/")+"/.well-known/openid-configuration")
	if err != nil || status != http.StatusOK {
		add("oidc", LevelFail, "issuer metadata unreachable (status %d, err %v)", status, err)
		return
	}
	var meta struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	if err := json.Unmarshal(body, &meta); err != nil || meta.Issuer == "" {
		add("oidc", LevelFail, "issuer metadata is not valid discovery JSON")
		return
	}
	if strings.TrimRight(meta.Issuer, "/") != strings.TrimRight(cfg.OIDCIssuer, "/") {
		add("oidc", LevelFail, "issuer metadata says %q but OIDC_ISSUER is %q", meta.Issuer, cfg.OIDCIssuer)
		return
	}
	add("oidc", LevelOK, "issuer metadata ok, jwks %s", meta.JWKSURI)
}

func checkHTTPDep(ctx context.Context, name, base, path string, d Deps, add adder) {
	if base == "" {
		add(name, LevelFail, "no %s URL configured", name)
		return
	}
	status, _, err := d.HTTPGet(ctx, strings.TrimRight(base, "/")+path)
	if err != nil || status != http.StatusOK {
		add(name, LevelFail, "%s unreachable (status %d, err %v)", base, status, err)
		return
	}
	add(name, LevelOK, "%s healthy", base)
}

func checkDial(ctx context.Context, name, hostports string, d Deps, add adder) {
	if hostports == "" {
		add(name, LevelFail, "not configured")
		return
	}
	var bad []string
	for _, hp := range strings.Split(hostports, ",") {
		hp = strings.TrimSpace(hp)
		if err := d.Dial(ctx, hp); err != nil {
			bad = append(bad, hp+": "+err.Error())
		}
	}
	if len(bad) > 0 {
		add(name, LevelFail, "unreachable: %s", strings.Join(bad, "; "))
		return
	}
	add(name, LevelOK, "%s reachable", hostports)
}

func checkServices(procs []Process, d Deps, add adder) {
	if len(procs) == 0 {
		add("services", LevelFail, "no service executables found in the install directory's bin folder")
		return
	}
	var missing []string
	for _, p := range procs {
		if _, err := d.Stat(p.Exe); err != nil {
			missing = append(missing, p.Name)
		}
	}
	if len(missing) > 0 {
		add("services", LevelFail, "missing executables: %s", strings.Join(missing, ", "))
		return
	}
	add("services", LevelOK, "%d service executables present", len(procs))
}

func checkFeatures(cfg Config, d Deps, add adder) {
	runtimes := map[string]string{"node": filepath.Join("runtime", "node", "node.exe"), "python": filepath.Join("runtime", "python", "python.exe")}
	for _, f := range optionalFeatures {
		if !cfg.Features[f] {
			add("feature-"+f, LevelOK, "off: %s services and their screens are disabled", f)
			continue
		}
		if _, err := d.Stat(filepath.Join(cfg.InstallDir, runtimes[f])); err != nil {
			add("feature-"+f, LevelFail, "on, but the private runtime %s is missing", runtimes[f])
			continue
		}
		add("feature-"+f, LevelOK, "on: private runtime present")
	}
}

// redact removes the DSN's password from an error message.
func redact(msg, dsn string) string {
	if u, err := url.Parse(dsn); err == nil && u.User != nil {
		if pw, ok := u.User.Password(); ok && pw != "" {
			msg = strings.ReplaceAll(msg, pw, "****")
		}
	}
	return msg
}

// Failed reports whether any result is a failure.
func Failed(rs []Result) bool {
	for _, r := range rs {
		if r.Level == LevelFail {
			return true
		}
	}
	return false
}

// WriteResults prints one line per check: "ok|WARN|FAIL name: detail", which the MSI custom action copies into its log.
func WriteResults(w io.Writer, rs []Result) {
	for _, r := range rs {
		_, _ = fmt.Fprintf(w, "%-4s %s: %s\n", strings.ToUpper(string(r.Level)), r.Name, r.Detail)
	}
}
