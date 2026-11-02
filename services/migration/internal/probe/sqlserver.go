package probe

import (
	"context"
	"crypto/tls"
	"database/sql"
	"database/sql/driver"
	"net"

	mssql "github.com/microsoft/go-mssqldb"
	"github.com/microsoft/go-mssqldb/msdsn"
)

// guardDialer hands every connection go-mssqldb makes to the egress guard. It is a mssql.HostDialer, so the driver
// passes the host name through instead of resolving it itself: the guard resolves once, checks every address and pins
// the connection (a second DNS answer cannot reach another one). UDP (the SQL Browser) is refused by the guard.
type guardDialer struct{ dial Dial }

func (d guardDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return d.dial(ctx, network, addr)
}

func (guardDialer) HostName() string { return "" }

var _ mssql.HostDialer = guardDialer{}

// sqlserverConfig builds the driver's own config struct from a fixed DSN skeleton plus the tenant's settings set on the
// struct, so no tenant text is ever parsed as connection-string syntax.
func sqlserverConfig(s SQLSettings, tlsBase *tls.Config) (msdsn.Config, error) {
	cfg, err := msdsn.Parse("sqlserver://placeholder?app+name=passage-migration-probe&connection+timeout=0&dial+timeout=10")
	if err != nil {
		return cfg, err
	}
	tlsCfg, err := sslConfig(s.SSLMode, s.Host, tlsBase)
	if err != nil {
		return cfg, err
	}
	cfg.Host, cfg.Port, cfg.Instance, cfg.Database, cfg.User, cfg.Password = s.Host, uint64(s.Port), "", s.Database, s.User, s.Password
	cfg.Protocols = []string{"tcp"}
	cfg.ProtocolParameters = map[string]interface{}{}
	cfg.ChangePassword, cfg.ServerSPN, cfg.Workstation = "", "", ""
	cfg.AppName, cfg.ColumnEncryption, cfg.MultiSubnetFailover = "passage-migration", false, false
	switch s.SSLMode {
	case "disable":
		cfg.Encryption, cfg.TLSConfig, cfg.TrustServerCertificate = msdsn.EncryptionDisabled, nil, false
	default:
		cfg.Encryption, cfg.TLSConfig = msdsn.EncryptionRequired, tlsCfg
		cfg.TrustServerCertificate = tlsCfg != nil && tlsCfg.InsecureSkipVerify
	}
	return cfg, nil
}

func sqlserverConnector(s SQLSettings, dial Dial, tlsBase *tls.Config) (driver.Connector, error) {
	cfg, err := sqlserverConfig(s, tlsBase)
	if err != nil {
		return nil, err
	}
	c := mssql.NewConnectorConfig(cfg)
	c.Dialer = guardDialer{dial}
	return c, nil
}

// sqlserverBegin: go-mssqldb refuses read-only transactions, so the read-only guarantee for SQL Server rests on the
// statement validator and the login's own permissions; the page query still runs under the caller's deadline.
func sqlserverBegin(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
	return db.BeginTx(ctx, nil)
}

func sqlserverInspect(ctx context.Context, db *sql.DB, res *Result) error {
	tx, err := sqlserverBegin(ctx, db)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.QueryRowContext(ctx, `select cast(serverproperty('ProductVersion') as nvarchar(64))`).Scan(&res.ServerVersion); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `select top (@p1) schema_name from information_schema.schemata
		where schema_name not in ('sys', 'INFORMATION_SCHEMA', 'guest') and schema_name not like 'db[_]%' order by 1`, maxSchemas+1)
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
	rows, err = tx.QueryContext(ctx, `select top (@p1) table_schema, table_name, table_type from information_schema.tables
		where table_schema not in ('sys', 'INFORMATION_SCHEMA') order by 1, 2`, maxTables+1)
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
	n := min(len(res.Tables), maxColumnTables)
	if len(res.Tables) > n {
		res.Truncated = true
	}
	for i := range res.Tables[:n] {
		crows, err := tx.QueryContext(ctx, `select top (@p3) column_name, data_type, case when is_nullable = 'YES' then 1 else 0 end
			from information_schema.columns where table_schema = @p1 and table_name = @p2 order by ordinal_position`,
			res.Tables[i].Schema, res.Tables[i].Name, maxColumnsPerTable+1)
		if err != nil {
			return err
		}
		for crows.Next() {
			var c Column
			var nullable int
			if err := crows.Scan(&c.Name, &c.Type, &nullable); err != nil {
				crows.Close()
				return err
			}
			c.Nullable = nullable == 1
			if len(res.Tables[i].Columns) >= maxColumnsPerTable {
				res.Truncated = true
			} else {
				res.Tables[i].Columns = append(res.Tables[i].Columns, c)
			}
		}
		crows.Close()
		if err := crows.Err(); err != nil {
			return err
		}
	}
	return nil
}
