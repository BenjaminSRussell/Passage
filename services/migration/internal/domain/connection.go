package domain

import (
	"fmt"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
	_ "time/tzdata" // zone names must resolve in the distroless image
	"unicode/utf8"

	"golang.org/x/text/encoding/ianaindex"
)

// Source systems (EN-064): four legacy systems plus custom_<slug> for a customer's own ERP.
const SourceSystemHint = "must be ax2012, d365fo, sap_ecc, sap_s4 or custom_<name> (lowercase letters, digits and underscores, 1 to 40 after custom_)"

var (
	legacySystems = []string{"ax2012", "d365fo", "sap_ecc", "sap_s4"}
	customSystem  = regexp.MustCompile(`^custom_[a-z0-9_]{1,40}$`)
)

// ValidSourceSystem is the one place that decides which source system names exist; the database CHECKs on
// migration.run, migration.xref, migration.project and migration.connection_profile accept the same set.
func ValidSourceSystem(s string) bool {
	return slices.Contains(legacySystems, s) || customSystem.MatchString(s)
}

// AuthOAuth2 is the rest authType for OAuth 2.0 client credentials: config authUrl (the token endpoint), clientId and
// optionally scope; the sealed secret is the client secret.
const AuthOAuth2 = "oauth2_client"

// Connection profile kinds and statuses.
const (
	ConnSQL      = "sql"
	ConnREST     = "rest"
	ConnFile     = "file"
	ConnActive   = "active"
	ConnDisabled = "disabled"

	// MaxSecretBytes bounds the one secret of a profile (a password, token or key).
	MaxSecretBytes = 4096

	maxConfigText = 255
)

// SQLDrivers lists the database drivers this build links; internal/probe keeps the registry that opens them (a test
// keeps both lists equal). Oracle and mysql are the next entries.
var SQLDrivers = []string{"postgres", "sqlserver", "mysql", "oracle"}

// ConnectionProfile is the non-secret part of a connection profile.
type ConnectionProfile struct {
	Name         string
	Kind         string
	SourceSystem string
	Config       map[string]any
	Status       string
}

var (
	hostRe    = regexp.MustCompile(`^[A-Za-z0-9._:\-]{1,253}$`)
	headerRe  = regexp.MustCompile(`^[A-Za-z0-9\-]{1,64}$`)
	globRe    = regexp.MustCompile(`^[A-Za-z0-9._*?\-]{1,100}$`)
	secretKey = regexp.MustCompile(`(?i)pass|secret|token|credential|api_?key|private|bearer|authorization`)
)

var configKeys = map[string][]string{
	ConnSQL:  {"driver", "host", "port", "database", "user", "sslmode", "codepage", "timezone"},
	ConnREST: {"baseUrl", "authType", "username", "headerName", "authUrl", "clientId", "scope", "pagingStyle", "probePath", "codepage", "timezone"},
	ConnFile: {"dropDir", "format", "delimiter", "filePattern", "codepage", "timezone"},
}

// secretBound lists, per kind, the settings that decide where the secret is sent and how. Changing one of them clears
// the stored secret, so an editor cannot point a profile at another server and have :test hand it the existing
// password or token, nor reuse a token as a basic-auth password.
var secretBound = map[string][]string{
	ConnSQL:  {"driver", "host", "port", "database", "user", "sslmode"},
	ConnREST: {"baseUrl", "authType", "username", "headerName", "authUrl", "clientId", "scope"},
}

// SecretInvalidated reports whether a config change touches a setting the stored secret is bound to.
func SecretInvalidated(kind string, before, after map[string]any) bool {
	for _, k := range secretBound[kind] {
		if fmt.Sprint(before[k]) != fmt.Sprint(after[k]) {
			return true
		}
	}
	return false
}

func isIPv6(h string) bool {
	a, err := netip.ParseAddr(h)
	return err == nil && a.Is6() && a.Zone() == ""
}

// ValidateConnection returns a message per invalid field; an empty map means the profile is valid. The config holds
// non-secret settings only: a key that looks like a secret is refused and points to the secret endpoint.
func ValidateConnection(c ConnectionProfile) map[string]string {
	bad := map[string]string{}
	if n := strings.TrimSpace(c.Name); n == "" || utf8.RuneCountInString(n) > maxProjectName || n != c.Name {
		bad["name"] = "is required, at most 80 characters, without leading or trailing spaces"
	}
	if !ValidSourceSystem(c.SourceSystem) {
		bad["sourceSystem"] = SourceSystemHint
	}
	if c.Status != ConnActive && c.Status != ConnDisabled {
		bad["status"] = "must be active or disabled"
	}
	allowed, ok := configKeys[c.Kind]
	if !ok {
		bad["kind"] = "must be one of sql, rest, file"
		return bad
	}
	for k := range c.Config {
		switch {
		case secretKey.MatchString(k):
			bad["config."+k] = "secrets are not stored in config; set the secret with PUT .../secret"
		case !slices.Contains(allowed, k):
			bad["config."+k] = "is not a setting of a " + c.Kind + " connection (allowed: " + strings.Join(allowed, ", ") + ")"
		}
	}
	if len(bad) > 0 {
		return bad
	}
	str := func(k string, required bool) string {
		v, present := c.Config[k]
		if !present || v == nil || v == "" {
			if required {
				bad["config."+k] = "is required"
			}
			return ""
		}
		s, isStr := v.(string)
		switch {
		case !isStr:
			bad["config."+k] = "must be a string"
		case utf8.RuneCountInString(s) > maxConfigText || strings.TrimSpace(s) != s || strings.ContainsFunc(s, func(r rune) bool { return r < ' ' || r == 0x7f }):
			bad["config."+k] = "at most 255 characters, no control characters, no surrounding spaces"
		default:
			return s
		}
		return ""
	}
	oneOf := func(k string, required bool, values ...string) string {
		s := str(k, required)
		if s != "" && !slices.Contains(values, s) {
			bad["config."+k] = "must be one of " + strings.Join(values, ", ")
			return ""
		}
		return s
	}
	switch c.Kind {
	case ConnSQL:
		oneOf("driver", true, SQLDrivers...)
		if h := str("host", true); h != "" && (!hostRe.MatchString(h) || strings.HasPrefix(h, "/") || strings.Contains(h, ":") && !isIPv6(h)) {
			bad["config.host"] = "must be a host name or IP address (no scheme, path or port)"
		}
		if v, present := c.Config["port"]; present && v != nil {
			if f, isNum := v.(float64); !isNum || f != float64(int(f)) || f < 1 || f > 65535 {
				bad["config.port"] = "must be an integer from 1 to 65535"
			}
		}
		str("database", true)
		str("user", true)
		oneOf("sslmode", false, "disable", "require", "verify-ca", "verify-full")
	case ConnREST:
		auth := oneOf("authType", false, "none", "basic", "bearer", "header", AuthOAuth2)
		if raw := str("baseUrl", true); raw != "" {
			u, err := url.Parse(raw)
			switch {
			case err != nil || u.Host == "" || u.Hostname() == "":
				bad["config.baseUrl"] = "must be an absolute http(s) URL"
			case u.Scheme != "https" && u.Scheme != "http":
				bad["config.baseUrl"] = "must start with https:// (or http:// for an unauthenticated endpoint)"
			case u.User != nil || u.RawQuery != "" || u.Fragment != "":
				bad["config.baseUrl"] = "must not contain credentials, a query or a fragment; secrets are set separately"
			case u.Scheme == "http" && auth != "" && auth != "none":
				bad["config.baseUrl"] = "an authenticated endpoint must use https://"
			}
		}
		switch auth {
		case "basic":
			str("username", true)
		case AuthOAuth2:
			str("clientId", true)
			str("scope", false)
			if raw := str("authUrl", true); raw != "" {
				u, err := url.Parse(raw)
				if err != nil || u.Host == "" || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
					bad["config.authUrl"] = "must be an absolute https:// token endpoint URL without credentials, query or fragment"
				}
			}
		case "header":
			if h := str("headerName", true); h != "" && !headerRe.MatchString(h) {
				bad["config.headerName"] = "must be a header name (letters, digits and hyphens)"
			}
		}
		oneOf("pagingStyle", false, "none", "offset", "page", "cursor", "link")
		if p := str("probePath", false); p != "" && (!strings.HasPrefix(p, "/") || strings.Contains(p, "?") || strings.Contains(p, "..") || strings.Contains(p, "//")) {
			bad["config.probePath"] = "must be an absolute path without query or .. (for example /health)"
		}
	case ConnFile:
		if d := str("dropDir", true); d != "" && (path.IsAbs(d) || path.Clean(d) != d || d == "." || strings.HasPrefix(d, "../") || d == ".." || strings.Contains(d, "\\")) {
			bad["config.dropDir"] = "must be a clean relative path under the operator-mounted drop directory (no leading /, no ..)"
		}
		oneOf("format", true, "csv", "tsv", "json", "jsonl")
		if d := str("delimiter", false); d != "" && utf8.RuneCountInString(d) != 1 {
			bad["config.delimiter"] = "must be a single character"
		}
		if g := str("filePattern", false); g != "" && !globRe.MatchString(g) {
			bad["config.filePattern"] = "must be a file name pattern (letters, digits, . _ - * ?)"
		}
	}
	if cp := str("codepage", false); cp != "" {
		if enc, err := ianaindex.IANA.Encoding(cp); err != nil || enc == nil {
			bad["config.codepage"] = "must be a supported IANA character set name such as utf-8, windows-1252 or iso-8859-1"
		}
	}
	if tz := str("timezone", false); tz != "" {
		if _, err := time.LoadLocation(tz); err != nil || tz == "Local" {
			bad["config.timezone"] = "must be an IANA zone name such as Europe/Berlin or UTC"
		}
	}
	return bad
}
