package probe

import (
	"context"
	"crypto/tls"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"

	go_ora "github.com/sijms/go-ora/v2"
)

// oracleConnector uses github.com/sijms/go-ora/v2 (MIT, pure Go, no Oracle client): the service name is the profile's
// database. go-ora has no config struct, so the URL is made with its own BuildUrl, which escapes the user, password
// and service name; the host and port come from settings validated as a host name or address and a port number.
// Every network connection is made by dial, the egress guard's. Oracle quotes identifiers case-sensitively: give
// table and column names exactly as the catalog shows them (usually upper case).
func oracleConnector(s SQLSettings, dial Dial, tlsBase *tls.Config) (driver.Connector, error) {
	tlsCfg, err := sslConfig(s.SSLMode, s.Host, tlsBase)
	if err != nil {
		return nil, err
	}
	opts := map[string]string{"SSL": "false"}
	if tlsCfg != nil {
		opts["SSL"] = "true"
		if !tlsCfg.InsecureSkipVerify {
			opts["SSL Verify"] = "true"
		}
	}
	opts["CONNECT TIMEOUT"] = "10"
	c, ok := go_ora.NewConnector(go_ora.BuildUrl(s.Host, int(s.Port), s.Database, s.User, s.Password, opts)).(*go_ora.OracleConnector)
	if !ok {
		return nil, errors.New("unexpected connector")
	}
	c.Dialer(oracleDialer{dial})
	if tlsCfg != nil {
		c.WithTLSConfig(tlsCfg)
	}
	return c, nil
}

type oracleDialer struct{ dial Dial }

func (d oracleDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return d.dial(ctx, network, address)
}

// oracleBegin starts a transaction and makes it READ ONLY (it must be the transaction's first statement). If Oracle
// refuses that, the transaction is rolled back and nothing is read: a read that cannot be made read-only does not run.
func oracleBegin(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, "SET TRANSACTION READ ONLY"); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("make the transaction read-only: %w", err)
	}
	return tx, nil
}

func oracleInspect(ctx context.Context, db *sql.DB, res *Result) error {
	const user = `(select username from all_users where oracle_maintained = 'N')`
	return inspectCatalog(ctx, db, oracleBegin, res, catalogQueries{
		Version: `select version from product_component_version fetch first 1 rows only`,
		Schemas: `select username from all_users where oracle_maintained = 'N' order by 1 fetch first :1 rows only`,
		Tables: `select owner, table_name, 'BASE TABLE' from all_tables where owner in ` + user + `
			union all select owner, view_name, 'VIEW' from all_views where owner in ` + user + ` order by 1, 2 fetch first :1 rows only`,
		Columns: `select column_name, data_type, case when nullable = 'Y' then 1 else 0 end from all_tab_columns
			where owner = :1 and table_name = :2 order by column_id fetch first :3 rows only`,
	})
}
