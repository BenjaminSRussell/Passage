package probe

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net"
	"slices"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

// statementTimeoutMs bounds every probe statement on the source server, inside the probe's overall deadline.
const statementTimeoutMs = 5000

// Dial makes one network connection; the probe always passes the egress guard's DialContext.
type Dial func(ctx context.Context, network, address string) (net.Conn, error)

// SQLSettings are the settings of a sql profile as typed values plus its opened secret.
type SQLSettings struct {
	Host     string
	Port     uint16
	Database string
	User     string
	Password string
	SSLMode  string // disable, require (the default), verify-ca or verify-full
}

// SQLDriver is one database engine a sql profile can name in config.driver. The probe uses it today; the S2 extractors
// open their connections through the same entry.
type SQLDriver struct {
	DefaultPort uint16
	// Connector builds a database/sql connector from the driver's own config struct (never a connection string
	// formatted from tenant input) whose every network connection is made by dial. tlsBase carries the trusted roots.
	Connector func(s SQLSettings, dial Dial, tlsBase *tls.Config) (driver.Connector, error)
	// Inspect reports the server version, schemas, tables and the first tables' columns, each list bounded, without
	// reading row data, in a read-only transaction and under a statement timeout.
	Inspect func(ctx context.Context, db *sql.DB, res *Result) error
}

// sqlDrivers is the registry, keyed by config.driver; domain.SQLDrivers lists the same names for validation (a test
// keeps the two equal). The approved next entries are sqlserver (github.com/microsoft/go-mssqldb: a Config with a
// Dialer and NewConnectorConfig), oracle (github.com/sijms/go-ora/v2: a connector with a custom dialer) and mysql
// (github.com/go-sql-driver/mysql: Config with DialFunc and NewConnector).
var sqlDrivers = map[string]SQLDriver{
	"postgres": {DefaultPort: 5432, Connector: postgresConnector, Inspect: postgresInspect},
}

// SQLDriverNames lists the registered drivers in name order.
func SQLDriverNames() []string {
	names := make([]string, 0, len(sqlDrivers))
	for n := range sqlDrivers {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

func (p *Prober) sql(ctx context.Context, t Target, res *Result) error {
	if p.Guard == nil {
		return &failure{CodeEgressDenied, "outbound connections are not configured on this deployment"}
	}
	name := str(t.Config, "driver")
	drv, ok := sqlDrivers[name]
	if !ok {
		return &failure{CodeProbeFailed, "database driver " + name + " is not available in this build"}
	}
	if t.Secret == "" {
		return &failure{CodeNoSecret, "no secret is set for this connection; set the password first"}
	}
	s := SQLSettings{Host: str(t.Config, "host"), Port: drv.DefaultPort, Database: str(t.Config, "database"), User: str(t.Config, "user"),
		Password: t.Secret, SSLMode: str(t.Config, "sslmode")}
	if f, ok := t.Config["port"].(float64); ok && f >= 1 && f <= 65535 {
		s.Port = uint16(f)
	}
	conn, err := drv.Connector(s, p.Guard.DialContext, p.Guard.TLS)
	if err != nil {
		return &failure{CodeProbeFailed, "invalid connection settings"}
	}
	db := sql.OpenDB(conn)
	defer db.Close()
	db.SetMaxOpenConns(1)
	return drv.Inspect(ctx, db, res)
}

func postgresConnector(s SQLSettings, dial Dial, tlsBase *tls.Config) (driver.Connector, error) {
	cfg, err := postgresConfig(s, dial, tlsBase)
	if err != nil {
		return nil, err
	}
	return stdlib.GetConnector(*cfg), nil
}

func postgresConfig(s SQLSettings, dial Dial, tlsBase *tls.Config) (*pgx.ConnConfig, error) {
	// The connection string is a constant; every tenant setting goes onto the struct, so a host, database or user name
	// cannot add a parameter, and whatever PG* environment defaults pgx read are overwritten or cleared below.
	cfg, err := pgx.ParseConfig("sslmode=disable connect_timeout=10")
	if err != nil {
		return nil, err
	}
	tlsCfg, err := sslConfig(s.SSLMode, s.Host, tlsBase)
	if err != nil {
		return nil, err
	}
	cfg.Host, cfg.Port, cfg.Database, cfg.User, cfg.Password = s.Host, s.Port, s.Database, s.User, s.Password
	cfg.TLSConfig, cfg.SSLNegotiation = tlsCfg, ""
	cfg.Fallbacks, cfg.ValidateConnect, cfg.AfterConnect, cfg.AfterNetConnect = nil, nil, nil, nil
	cfg.KerberosSrvName, cfg.KerberosSpn, cfg.OAuthTokenProvider = "", "", nil
	cfg.RuntimeParams = map[string]string{"application_name": "passage-migration-probe"}
	// The guard resolves and pins; pgx must hand it the host name, not a pre-resolved address.
	cfg.LookupFunc = func(_ context.Context, host string) ([]string, error) { return []string{host}, nil }
	cfg.DialFunc = pgconn.DialFunc(dial)
	return cfg, nil
}

// sslConfig maps a libpq sslmode onto a TLS configuration: require encrypts without checking the certificate (as libpq
// does without a root certificate), verify-ca checks the chain against the trusted roots, verify-full also the host.
func sslConfig(mode, host string, base *tls.Config) (*tls.Config, error) {
	if mode == "disable" {
		return nil, nil
	}
	t := &tls.Config{}
	if base != nil {
		t = base.Clone()
	}
	t.MinVersion = max(t.MinVersion, tls.VersionTLS12)
	switch mode {
	case "", "require":
		t.InsecureSkipVerify = true
	case "verify-ca":
		roots := t.RootCAs // nil: the system roots
		t.InsecureSkipVerify = true
		t.VerifyPeerCertificate = func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("the server sent no certificate")
			}
			certs := make([]*x509.Certificate, len(raw))
			for i, b := range raw {
				c, err := x509.ParseCertificate(b)
				if err != nil {
					return err
				}
				certs[i] = c
			}
			inter := x509.NewCertPool()
			for _, c := range certs[1:] {
				inter.AddCert(c)
			}
			_, err := certs[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter})
			return err
		}
	case "verify-full":
		t.InsecureSkipVerify, t.ServerName = false, host
	default:
		return nil, errors.New("unknown sslmode")
	}
	return t, nil
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// postgresInspect runs in a read-only transaction with a transaction-local statement timeout (set_config, not a
// startup parameter, so a connection pooler in front of the server does not refuse the connection).
func postgresInspect(ctx context.Context, db *sql.DB, res *Result) error {
	tx, err := postgresReadOnly(ctx, db)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.QueryRowContext(ctx, `select version()`).Scan(&res.ServerVersion); err != nil {
		return err
	}
	return postgresCatalog(ctx, tx, res)
}

func postgresReadOnly(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `select set_config('statement_timeout', $1, true)`, strconv.Itoa(statementTimeoutMs)); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

const systemSchemas = `table_schema not in ('pg_catalog', 'information_schema') and table_schema not like 'pg\_toast%' and table_schema not like 'pg\_temp%'`

func postgresCatalog(ctx context.Context, q querier, res *Result) error {
	rows, err := q.QueryContext(ctx, `select schema_name from information_schema.schemata
		where schema_name not in ('pg_catalog', 'information_schema') and schema_name not like 'pg\_toast%' and schema_name not like 'pg\_temp%'
		order by 1 limit $1`, maxSchemas+1)
	if err != nil {
		return err
	}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			rows.Close()
			return err
		}
		res.Schemas = append(res.Schemas, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(res.Schemas) > maxSchemas {
		res.Schemas, res.Truncated = res.Schemas[:maxSchemas], true
	}
	rows, err = q.QueryContext(ctx, `select table_schema, table_name, table_type from information_schema.tables where `+systemSchemas+` order by 1, 2 limit $1`, maxTables+1)
	if err != nil {
		return err
	}
	for rows.Next() {
		var t Table
		if err := rows.Scan(&t.Schema, &t.Name, &t.Type); err != nil {
			rows.Close()
			return err
		}
		res.Tables = append(res.Tables, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(res.Tables) > maxTables {
		res.Tables, res.Truncated = res.Tables[:maxTables], true
	}
	if len(res.Tables) == 0 {
		return nil
	}
	// Columns for the first tables only: enough to show the shape without reading a whole catalogue.
	n := min(len(res.Tables), maxColumnTables)
	if len(res.Tables) > n {
		res.Truncated = true
	}
	var schemas, names []string
	for _, t := range res.Tables[:n] {
		schemas, names = append(schemas, t.Schema), append(names, t.Name)
	}
	rows, err = q.QueryContext(ctx, `select c.table_schema, c.table_name, c.column_name, c.data_type, c.is_nullable = 'YES'
		from information_schema.columns c
		join unnest($1::text[], $2::text[]) as w(s, n) on c.table_schema = w.s and c.table_name = w.n
		order by c.table_schema, c.table_name, c.ordinal_position limit $3`, schemas, names, n*maxColumnsPerTable+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var s, tn string
		var c Column
		if err := rows.Scan(&s, &tn, &c.Name, &c.Type, &c.Nullable); err != nil {
			return err
		}
		for i := range res.Tables[:n] {
			if res.Tables[i].Schema == s && res.Tables[i].Name == tn {
				if len(res.Tables[i].Columns) >= maxColumnsPerTable {
					res.Truncated = true
				} else {
					res.Tables[i].Columns = append(res.Tables[i].Columns, c)
				}
			}
		}
	}
	return rows.Err()
}
