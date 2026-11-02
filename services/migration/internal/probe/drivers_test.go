package probe_test

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/migration/internal/probe"
)

// Every registered engine makes its network connections through the dial function it is given (the egress guard's), to
// the host and port of the settings and nowhere else, and never puts the password in the error it returns.
func TestSQLDrivers_EveryConnectionGoesThroughTheGuardsDialer(t *testing.T) {
	for _, name := range probe.SQLDriverNames() {
		if name == "postgres" {
			continue // covered against a live server in probe_test.go
		}
		t.Run(name, func(t *testing.T) {
			drv, ok := probe.LookupSQLDriver(name)
			if !ok || drv.Connector == nil || drv.Begin == nil || drv.Inspect == nil || drv.Dialect.Param == nil || drv.Dialect.Quote == nil {
				t.Fatalf("incomplete registry entry: %+v", drv)
			}
			var mu sync.Mutex
			var dialled []string
			dial := func(_ context.Context, network, address string) (net.Conn, error) {
				mu.Lock()
				defer mu.Unlock()
				dialled = append(dialled, network+" "+address)
				return nil, errors.New("guard says no")
			}
			const pw = "p@ss/w:rd?#&'\" ;x"
			for _, mode := range []string{"disable", "require", "verify-full"} {
				conn, err := drv.Connector(probe.SQLSettings{Host: "erp.acme.example", Port: 4711, Database: "ERP", User: "svc", Password: pw, SSLMode: mode}, dial, nil)
				if err != nil {
					t.Fatalf("%s: connector: %v", mode, err)
				}
				db := sql.OpenDB(conn)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				err = db.PingContext(ctx)
				cancel()
				_ = db.Close()
				if err == nil || strings.Contains(err.Error(), pw) {
					t.Fatalf("%s: ping error %v", mode, err)
				}
			}
			if len(dialled) == 0 {
				t.Fatal("the driver never used the guard's dialer")
			}
			for _, d := range dialled {
				if d != "tcp erp.acme.example:4711" {
					t.Errorf("dialled %q", d)
				}
			}
		})
	}
	if _, err := mustDriver(t, "mysql").Connector(probe.SQLSettings{Host: "h", Port: 1, SSLMode: "bogus"}, nil, nil); err == nil {
		t.Error("an unknown sslmode built a connector")
	}
}

func mustDriver(t *testing.T, name string) probe.SQLDriver {
	t.Helper()
	d, ok := probe.LookupSQLDriver(name)
	if !ok {
		t.Fatalf("no %s driver", name)
	}
	return d
}

func TestSQLDrivers_DialectsQuoteAndParameterise(t *testing.T) {
	cases := map[string]struct{ param1, param3, quote string }{
		"postgres":  {"$1", "$3", `"a""b"`},
		"sqlserver": {"@p1", "@p3", "[a]]b]"},
		"mysql":     {"?", "?", "`a``b`"},
		"oracle":    {":1", ":3", `"a""b"`},
	}
	for name, want := range cases {
		d := mustDriver(t, name).Dialect
		raw := "a\"b"
		switch name {
		case "sqlserver":
			raw = "a]b"
		case "mysql":
			raw = "a`b"
		}
		if d.Param(1) != want.param1 || d.Param(3) != want.param3 || d.Quote(raw) != want.quote {
			t.Errorf("%s: %s %s %s", name, d.Param(1), d.Param(3), d.Quote(raw))
		}
	}
}
