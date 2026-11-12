package cloudprofile

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var dsnSSLMode = regexp.MustCompile(`(?:^|\s)sslmode\s*=\s*'?([a-z-]+)'?`)

// PostgresSSLMode returns the sslmode of a libpq connection string (URL or key=value form), or the PGSSLMODE
// environment value, or libpq's default "prefer" when neither names one.
func PostgresSSLMode(connString string, getenv func(string) string) string {
	if u, err := url.Parse(connString); err == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql") {
		if m := u.Query().Get("sslmode"); m != "" {
			return m
		}
	} else if m := dsnSSLMode.FindStringSubmatch(connString); m != nil {
		return m[1]
	}
	if m := getenv("PGSSLMODE"); m != "" {
		return m
	}
	return "prefer"
}

// CheckPostgresConn is the fail-closed check of a connection string against the profile (#602 #603): a profile whose
// postgresSslMode is verify-full (gcc, gcc-high, dod) refuses any other sslmode, so a database connection can never
// fall back to an unverified or unencrypted channel. Other profiles accept what the operator configured.
func (p *Profile) CheckPostgresConn(connString string, getenv func(string) string) error {
	if p.Deployment.PostgresSSLMode != "verify-full" {
		return nil
	}
	if mode := PostgresSSLMode(connString, getenv); mode != "verify-full" {
		return fmt.Errorf("cloudprofile: profile %s requires sslmode=verify-full on the Postgres connection, got sslmode=%s", p.Selector(), strings.TrimSpace(mode))
	}
	return nil
}
