package probe

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/testkit"
)

func plainDial(ctx context.Context, network, address string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, address)
}

// Settings land on the pgx struct verbatim, so DSN syntax in a host, database or user name is only a strange name,
// and the service's own PG* environment never reaches a customer connection.
func TestPostgresConfig_TakesSettingsVerbatimAndIgnoresTheServiceEnvironment(t *testing.T) {
	t.Setenv("PGOPTIONS", "-c search_path=attacker")
	t.Setenv("PGAPPNAME", "service")
	t.Setenv("PGTARGETSESSIONATTRS", "read-write")
	t.Setenv("PGHOST", "db.internal,db2.internal")
	t.Setenv("PGSSLMODE", "verify-full")
	t.Setenv("PGSSLNEGOTIATION", "direct")
	s := SQLSettings{Host: "erp.acme.example", Port: 6543, Database: "erp sslmode=disable host=evil.example", User: "svc' options='-c x=y",
		Password: "p@ss word&sslmode=disable", SSLMode: "disable"}
	cfg, err := postgresConfig(s, plainDial, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != s.Host || cfg.Port != 6543 || cfg.Database != s.Database || cfg.User != s.User || cfg.Password != s.Password {
		t.Fatalf("settings changed on the way: %+v", cfg.Config)
	}
	if cfg.TLSConfig != nil || len(cfg.Fallbacks) != 0 || cfg.ValidateConnect != nil || cfg.SSLNegotiation != "" {
		t.Fatalf("environment leaked: tls=%v fallbacks=%d validate=%v negotiation=%q", cfg.TLSConfig != nil, len(cfg.Fallbacks), cfg.ValidateConnect != nil, cfg.SSLNegotiation)
	}
	if len(cfg.RuntimeParams) != 1 || cfg.RuntimeParams["application_name"] != "passage-migration-probe" {
		t.Fatalf("runtime params = %v", cfg.RuntimeParams)
	}
	if addrs, _ := cfg.LookupFunc(context.Background(), "erp.acme.example"); len(addrs) != 1 || addrs[0] != "erp.acme.example" {
		t.Fatalf("pgx must hand the host name to the guard unresolved: %v", addrs)
	}
}

func TestSSLConfig_MapsLibpqModes(t *testing.T) {
	roots := x509.NewCertPool()
	base := &tls.Config{RootCAs: roots, ServerName: "ignored.example"}
	if c, err := sslConfig("disable", "h", base); err != nil || c != nil {
		t.Fatalf("disable = %v %v", c, err)
	}
	for _, mode := range []string{"", "require"} {
		c, err := sslConfig(mode, "h", base)
		if err != nil || c == nil || !c.InsecureSkipVerify || c.VerifyPeerCertificate != nil || c.MinVersion < tls.VersionTLS12 {
			t.Fatalf("%q = %+v %v", mode, c, err)
		}
	}
	ca, err := sslConfig("verify-ca", "h", base)
	if err != nil || !ca.InsecureSkipVerify || ca.VerifyPeerCertificate == nil {
		t.Fatalf("verify-ca = %+v %v", ca, err)
	}
	if err := ca.VerifyPeerCertificate(nil, nil); err == nil {
		t.Fatal("verify-ca must refuse a server without a certificate")
	}
	full, err := sslConfig("verify-full", "erp.acme.example", base)
	if err != nil || full.InsecureSkipVerify || full.ServerName != "erp.acme.example" || full.RootCAs != roots {
		t.Fatalf("verify-full = %+v %v", full, err)
	}
	if base.ServerName != "ignored.example" || base.InsecureSkipVerify {
		t.Fatal("the base configuration must not be modified")
	}
	if _, err := sslConfig("prefer", "h", nil); err == nil {
		t.Fatal("an unknown mode must fail")
	}
}

// The probe's transaction is read-only and carries the statement timeout.
func TestPostgresReadOnly_RefusesWritesAndBoundsStatements(t *testing.T) {
	url := os.Getenv(testkit.EnvURL)
	if url == "" {
		t.Skipf("%s not set; skipping database test", testkit.EnvURL)
	}
	pool := testkit.DB(t)
	var schema string
	if err := pool.QueryRow(context.Background(), `select current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `create table probe_target (id integer)`); err != nil {
		t.Fatal(err)
	}
	cc, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := postgresConnector(SQLSettings{Host: cc.Host, Port: cc.Port, Database: cc.Database, User: cc.User, Password: cc.Password, SSLMode: "disable"}, plainDial, nil)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(conn)
	defer db.Close()
	ctx := context.Background()
	tx, err := postgresReadOnly(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var readOnly, timeout string
	if err := tx.QueryRowContext(ctx, `select current_setting('transaction_read_only'), current_setting('statement_timeout')`).Scan(&readOnly, &timeout); err != nil {
		t.Fatal(err)
	}
	if readOnly != "on" || timeout != "5s" {
		t.Fatalf("read only = %q, statement timeout = %q", readOnly, timeout)
	}
	_, err = tx.ExecContext(ctx, `insert into `+pgx.Identifier{schema, "probe_target"}.Sanitize()+` values (1)`)
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "25006" {
		t.Fatalf("a write inside the probe must fail as read-only: %v", err)
	}
}

func TestScrub_RemovesEscapedFormsOfTheSecret(t *testing.T) {
	secret := `p@ss word/"x"`
	for _, msg := range []string{
		"failed: " + secret,
		"dsn postgres://u:p%40ss+word%2F%22x%22@h/db",
		"dsn postgres://u:p@ss%20word%2F%22x%22@h/db",
		`quoted "p@ss word/\"x\""`,
	} {
		if got := scrub(msg, secret); strings.Contains(got, "ss word") || strings.Contains(got, "ss+word") || strings.Contains(got, "ss%20word") {
			t.Errorf("scrub(%q) = %q", msg, got)
		}
	}
	if got := scrub(strings.Repeat("ä", 300), ""); len(got) > 400 || !strings.HasPrefix(got, "ää") || strings.ToValidUTF8(got, "?") != got {
		t.Fatalf("cap must keep valid UTF-8: %d bytes", len(got))
	}
}
