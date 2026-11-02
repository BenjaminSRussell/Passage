package probe

import (
	"context"
	"crypto/tls"
	"database/sql"
	"database/sql/driver"
	"net"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"
)

// mysqlConnector builds the driver's own Config (never a DSN string made from tenant text) whose every network
// connection is made by dial, the egress guard's. github.com/go-sql-driver/mysql is MPL-2.0 and is used unmodified.
func mysqlConnector(s SQLSettings, dial Dial, tlsBase *tls.Config) (driver.Connector, error) {
	tlsCfg, err := sslConfig(s.SSLMode, s.Host, tlsBase)
	if err != nil {
		return nil, err
	}
	cfg := mysql.NewConfig()
	cfg.Net, cfg.Addr = "tcp", net.JoinHostPort(s.Host, strconv.Itoa(int(s.Port)))
	cfg.User, cfg.Passwd, cfg.DBName = s.User, s.Password, s.Database
	cfg.TLS, cfg.AllowFallbackToPlaintext = tlsCfg, false
	cfg.DialFunc = dial
	cfg.Timeout = 10 * time.Second
	cfg.ParseTime, cfg.Loc = true, time.UTC
	cfg.MultiStatements, cfg.AllowAllFiles, cfg.AllowCleartextPasswords, cfg.InterpolateParams = false, false, false, false
	return mysql.NewConnector(cfg)
}

// mysqlBegin starts a read-only transaction (START TRANSACTION READ ONLY) and, where the server supports it (MySQL 5.7.8
// and later; MariaDB spells it max_statement_time), a statement timeout.
func mysqlBegin(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	_, _ = tx.ExecContext(ctx, "SET SESSION MAX_EXECUTION_TIME = "+strconv.Itoa(statementTimeoutMs))
	return tx, nil
}

func mysqlInspect(ctx context.Context, db *sql.DB, res *Result) error {
	return inspectCatalog(ctx, db, mysqlBegin, res, catalogQueries{
		Version: `select version()`,
		Schemas: `select schema_name from information_schema.schemata
			where schema_name not in ('information_schema', 'mysql', 'performance_schema', 'sys') order by 1 limit ?`,
		Tables: `select table_schema, table_name, table_type from information_schema.tables
			where table_schema not in ('information_schema', 'mysql', 'performance_schema', 'sys') order by 1, 2 limit ?`,
		Columns: `select column_name, data_type, case when is_nullable = 'YES' then 1 else 0 end from information_schema.columns
			where table_schema = ? and table_name = ? order by ordinal_position limit ?`,
	})
}
