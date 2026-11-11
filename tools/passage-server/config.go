package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is %ProgramData%\Passage\passage.yaml. Every field can be overridden by a registry policy value (HKLM\SOFTWARE\Policies\Passage,
// Windows only) or by the MSI properties the installer writes into the file, so Intune, SCCM and GPO can manage it.
type Config struct {
	// Listen is the address of the gateway that serves the web app and /api. Default 0.0.0.0:8443.
	Listen string `yaml:"listen"`
	// TLS names a PEM certificate and key. Empty means plain HTTP on loopback only.
	TLS TLSConfig `yaml:"tls"`
	// DatabaseDSN is an admin-capable Postgres URL for migrations; per-service URLs are derived from it by database name.
	DatabaseDSN string `yaml:"pgDsn"`
	// AppDSN is the passage_app (NOBYPASSRLS) URL the services use. When empty, DatabaseDSN is used.
	AppDSN         string `yaml:"appDsn"`
	DatabasePrefix string `yaml:"databasePrefix"`
	OIDCIssuer     string `yaml:"oidcIssuer"`
	OIDCAudience   string `yaml:"oidcAudience"`
	OIDCJWKSURL    string `yaml:"oidcJwksUrl"`
	TenantID       string `yaml:"tenantId"`
	OPAURL         string `yaml:"opaUrl"`
	KafkaBootstrap string `yaml:"kafkaBootstrap"`
	TemporalHost   string `yaml:"temporalHostPort"`
	TemporalNS     string `yaml:"temporalNamespace"`
	// CloudProfile is passed to every child as PASSAGE_CLOUD_PROFILE (#599).
	CloudProfile string `yaml:"cloudProfile"`
	// RequireFIPS makes passage-server refuse to start unless crypto/fips140.Enabled() is true.
	RequireFIPS bool `yaml:"requireFips"`
	// InstallDir holds the service executables in bin\ and the optional runtimes in runtime\.
	InstallDir string `yaml:"installDir"`
	DataDir    string `yaml:"dataDir"`
	BasePort   int    `yaml:"basePort"`
	// Features turns optional MSI features on. The doctor command reports them.
	Features map[string]bool `yaml:"features"`
	// Services overrides or extends the services discovered in InstallDir\bin.
	Services []ServiceSpec `yaml:"services"`
}

// TLSConfig is a certificate in PEM form. Importing from the Windows certificate store is documented as a follow-up.
type TLSConfig struct {
	CertFile string `yaml:"certFile"`
	KeyFile  string `yaml:"keyFile"`
}

// ServiceSpec describes one supervised child process.
type ServiceSpec struct {
	Name string `yaml:"name"`
	// Exe is relative to InstallDir\bin unless absolute.
	Exe  string   `yaml:"exe"`
	Args []string `yaml:"args"`
	// Route is the /api/v1/<route> prefix the gateway sends here; it defaults to Name.
	Route string `yaml:"route"`
	// Feature, when set, names an optional feature that must be on for the service to run.
	Feature  string            `yaml:"feature"`
	Optional bool              `yaml:"optional"`
	Env      map[string]string `yaml:"env"`
	Disabled bool              `yaml:"disabled"`
	// DB is false for services that have no database.
	NoDB bool `yaml:"noDb"`
}

const (
	defaultListen   = "127.0.0.1:8443"
	defaultBasePort = 18100
	adminOffset     = 1000
)

// DefaultConfig returns the values used when passage.yaml omits a field.
func DefaultConfig() Config {
	return Config{
		Listen:         defaultListen,
		DatabasePrefix: "passage",
		OIDCAudience:   "passage-api",
		TemporalNS:     "default",
		BasePort:       defaultBasePort,
		Features:       map[string]bool{},
	}
}

// LoadConfig reads path (missing file means defaults) and then applies overrides, which the Windows build fills from registry policy.
func LoadConfig(path string, overrides map[string]string) (Config, error) {
	cfg := DefaultConfig()
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return cfg, fmt.Errorf("parse %s: %w", path, err)
		}
	case !errors.Is(err, os.ErrNotExist):
		return cfg, err
	}
	if err := cfg.mergeConfD(filepath.Join(filepath.Dir(path), "conf.d")); err != nil {
		return cfg, err
	}
	cfg.applyOverrides(overrides)
	if cfg.Features == nil {
		cfg.Features = map[string]bool{}
	}
	return cfg, cfg.Validate()
}

// mergeConfD appends the services listed in conf.d/*.yaml. The optional MSI features (node, python) drop their service lists
// there, so installing or removing a feature adds or removes its services without editing passage.yaml.
func (c *Config) mergeConfD(dir string) error {
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return err
	}
	sort.Strings(files)
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		var frag struct {
			Services []ServiceSpec `yaml:"services"`
		}
		if err := yaml.Unmarshal(data, &frag); err != nil {
			return fmt.Errorf("parse %s: %w", f, err)
		}
		c.Services = append(c.Services, frag.Services...)
	}
	return nil
}

// applyOverrides maps MSI property names (and the registry policy value names) onto the config.
func (c *Config) applyOverrides(o map[string]string) {
	set := func(dst *string, key string) {
		if v := strings.TrimSpace(o[key]); v != "" {
			*dst = v
		}
	}
	set(&c.DatabaseDSN, "PG_DSN")
	set(&c.AppDSN, "PG_APP_DSN")
	set(&c.OIDCIssuer, "OIDC_ISSUER")
	set(&c.OIDCAudience, "OIDC_AUDIENCE")
	set(&c.OIDCJWKSURL, "OIDC_JWKS_URL")
	set(&c.TenantID, "TENANT_ID")
	set(&c.OPAURL, "OPA_URL")
	set(&c.KafkaBootstrap, "KAFKA_BOOTSTRAP")
	set(&c.TemporalHost, "TEMPORAL_HOSTPORT")
	set(&c.Listen, "LISTEN")
	set(&c.CloudProfile, "CLOUD_PROFILE")
	set(&c.TLS.CertFile, "TLS_CERT_FILE")
	set(&c.TLS.KeyFile, "TLS_KEY_FILE")
	if v := o["REQUIRE_FIPS"]; v == "1" || strings.EqualFold(v, "true") {
		c.RequireFIPS = true
	}
	for _, f := range []string{"NODE", "PYTHON"} {
		if v, ok := o["FEATURE_"+f]; ok {
			if c.Features == nil {
				c.Features = map[string]bool{}
			}
			c.Features[strings.ToLower(f)] = v == "1" || strings.EqualFold(v, "true")
		}
	}
}

// Validate rejects a configuration the server could not run with. It does not touch the network: doctor does that.
func (c Config) Validate() error {
	if c.BasePort <= 0 || c.BasePort+adminOffset > 65000 {
		return fmt.Errorf("basePort %d is out of range", c.BasePort)
	}
	if (c.TLS.CertFile == "") != (c.TLS.KeyFile == "") {
		return errors.New("tls.certFile and tls.keyFile must be set together")
	}
	for _, p := range []struct{ name, v string }{{"oidcIssuer", c.OIDCIssuer}, {"opaUrl", c.OPAURL}, {"oidcJwksUrl", c.OIDCJWKSURL}} {
		if p.v == "" {
			continue
		}
		u, err := url.Parse(p.v)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("%s %q is not an http(s) URL", p.name, p.v)
		}
	}
	seen := map[string]bool{}
	for _, s := range c.Services {
		if s.Name == "" {
			return errors.New("a service entry has no name")
		}
		if seen[s.Name] {
			return fmt.Errorf("service %q is listed twice", s.Name)
		}
		seen[s.Name] = true
	}
	return nil
}

// ConfigPath is passage.yaml under the data directory, or PASSAGE_CONFIG when set.
func ConfigPath(getenv func(string) string) string {
	if p := getenv("PASSAGE_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(DefaultDataDir(getenv), "passage.yaml")
}

// DefaultDataDir is %ProgramData%\Passage on Windows and PASSAGE_DATA_DIR (or ./data) elsewhere.
func DefaultDataDir(getenv func(string) string) string {
	if d := getenv("PASSAGE_DATA_DIR"); d != "" {
		return d
	}
	if pd := getenv("ProgramData"); pd != "" {
		return filepath.Join(pd, "Passage")
	}
	return "data"
}

// Resolve fills InstallDir and DataDir, discovers service executables and returns the final, port-assigned process list.
func (c Config) Resolve(getenv func(string) string, listDir func(string) ([]string, error)) (Config, []Process, error) {
	if c.DataDir == "" {
		c.DataDir = DefaultDataDir(getenv)
	}
	if c.InstallDir == "" {
		exe, err := os.Executable()
		if err != nil {
			return c, nil, err
		}
		c.InstallDir = filepath.Dir(filepath.Dir(exe))
	}
	specs := map[string]ServiceSpec{}
	binDir := filepath.Join(c.InstallDir, "bin")
	files, err := listDir(binDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return c, nil, err
	}
	for _, f := range files {
		base := filepath.Base(f)
		name, ok := strings.CutSuffix(base, ".exe")
		if !ok || name == "" || name == "passage-server" {
			continue
		}
		specs[name] = ServiceSpec{Name: name, Exe: f}
	}
	for _, s := range c.Services {
		if base, ok := specs[s.Name]; ok && s.Exe == "" {
			s.Exe = base.Exe
		}
		specs[s.Name] = s
	}
	names := make([]string, 0, len(specs))
	for n, s := range specs {
		if s.Disabled || (s.Feature != "" && !c.Features[s.Feature]) {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	procs := make([]Process, 0, len(names))
	for i, n := range names {
		s := specs[n]
		exe := c.expand(s.Exe)
		for j, a := range s.Args {
			s.Args[j] = c.expand(a)
		}
		if exe == "" {
			return c, nil, fmt.Errorf("service %q has no exe", n)
		}
		if !filepath.IsAbs(exe) {
			exe = filepath.Join(binDir, exe)
		}
		route := s.Route
		if route == "" {
			route = routeOf(n)
		}
		p := Process{Name: n, Route: route, Exe: exe, Args: s.Args, Optional: s.Optional, Port: c.BasePort + i, AdminPort: c.BasePort + adminOffset + i}
		env, err := c.childEnv(s, p)
		if err != nil {
			return c, nil, err
		}
		p.Env = env
		procs = append(procs, p)
	}
	return c, procs, nil
}

// expand replaces {install} and {data} in a service's exe and args.
func (c Config) expand(v string) string {
	return strings.NewReplacer("{install}", c.InstallDir, "{data}", c.DataDir).Replace(v)
}

// routeOf maps a service name onto its gateway path segment; purchasing is served as procurement, as passage-dev does.
func routeOf(name string) string {
	if name == "purchasing" {
		return "procurement"
	}
	return name
}

// childEnv builds the environment of one child. Secrets (signing keys) are added by the caller from the key store.
func (c Config) childEnv(s ServiceSpec, p Process) (map[string]string, error) {
	env := map[string]string{
		"HTTP_ADDR":            fmt.Sprintf("127.0.0.1:%d", p.Port),
		"ADMIN_ADDR":           fmt.Sprintf("127.0.0.1:%d", p.AdminPort),
		"PASSAGE_SERVICE_NAME": p.Name,
		"MIGRATE_ON_START":     "true",
	}
	if !s.NoDB {
		dsn := c.AppDSN
		if dsn == "" {
			dsn = c.DatabaseDSN
		}
		if dsn != "" {
			u, err := databaseURL(dsn, c.DatabasePrefix+"_"+p.Name)
			if err != nil {
				return nil, err
			}
			env["DATABASE_URL"] = u
		}
	}
	if c.CloudProfile != "" {
		env["PASSAGE_CLOUD_PROFILE"] = c.CloudProfile
	}
	if c.KafkaBootstrap != "" {
		env["KAFKA_BOOTSTRAP"] = c.KafkaBootstrap
	}
	if c.TemporalHost != "" {
		env["TEMPORAL_HOSTPORT"] = c.TemporalHost
		env["TEMPORAL_NAMESPACE"] = c.TemporalNS
	}
	for k, v := range s.Env {
		env[k] = v
	}
	return env, nil
}

// databaseURL swaps the database name of a Postgres URL.
func databaseURL(dsn, db string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("pgDsn is not a postgres URL")
	}
	u.Path = "/" + db
	return u.String(), nil
}
