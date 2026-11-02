package domain

import (
	"strings"
	"testing"
)

func TestValidSourceSystem(t *testing.T) {
	for s, want := range map[string]bool{
		"ax2012": true, "d365fo": true, "sap_ecc": true, "sap_s4": true,
		"custom_acme": true, "custom_a": true, "custom_acme_2": true, "custom_" + strings.Repeat("a", 40): true,
		"": false, "custom_": false, "custom_Acme": false, "custom_acme-2": false, "custom_" + strings.Repeat("a", 41): false,
		"acme": false, "oracle": false, "AX2012": false, " custom_acme": false, "custom_acme\n": false,
	} {
		if got := ValidSourceSystem(s); got != want {
			t.Errorf("ValidSourceSystem(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestValidateConnection(t *testing.T) {
	sqlOK := ConnectionProfile{Name: "Acme ERP", Kind: ConnSQL, SourceSystem: "custom_acme", Status: ConnActive,
		Config: map[string]any{"driver": "postgres", "host": "erp.acme.example", "port": float64(5432), "database": "erp", "user": "svc", "sslmode": "verify-full", "codepage": "windows-1252", "timezone": "Europe/Berlin"}}
	restOK := ConnectionProfile{Name: "API", Kind: ConnREST, SourceSystem: "sap_s4", Status: ConnActive,
		Config: map[string]any{"baseUrl": "https://api.acme.example/odata", "authType": "bearer", "pagingStyle": "cursor", "probePath": "/health"}}
	fileOK := ConnectionProfile{Name: "Drop", Kind: ConnFile, SourceSystem: "ax2012", Status: ConnDisabled,
		Config: map[string]any{"dropDir": "acme/in", "format": "csv", "delimiter": ";", "filePattern": "Cust*.csv", "codepage": "iso-8859-1"}}
	tests := []struct {
		name  string
		base  ConnectionProfile
		edit  func(*ConnectionProfile)
		field string
	}{
		{"valid sql", sqlOK, func(*ConnectionProfile) {}, ""},
		{"valid rest", restOK, func(*ConnectionProfile) {}, ""},
		{"valid file", fileOK, func(*ConnectionProfile) {}, ""},
		{"rest http without auth", restOK, func(c *ConnectionProfile) { c.Config = map[string]any{"baseUrl": "http://a.example"} }, ""},
		{"rest header auth", restOK, func(c *ConnectionProfile) {
			c.Config = map[string]any{"baseUrl": "https://a.example", "authType": "header", "headerName": "X-Api-Key"}
		}, ""},
		{"ipv6 host", sqlOK, func(c *ConnectionProfile) { c.Config["host"] = "fd00::5" }, ""},
		{"integer port as number", sqlOK, func(c *ConnectionProfile) { c.Config["port"] = float64(1433) }, ""},
		{"nil config of a kind with required keys", sqlOK, func(c *ConnectionProfile) { c.Config = nil }, "config.driver"},
		{"unknown kind", sqlOK, func(c *ConnectionProfile) { c.Kind = "ftp" }, "kind"},
		{"blank name", sqlOK, func(c *ConnectionProfile) { c.Name = "" }, "name"},
		{"padded name", sqlOK, func(c *ConnectionProfile) { c.Name = "x " }, "name"},
		{"long name", sqlOK, func(c *ConnectionProfile) { c.Name = strings.Repeat("x", 81) }, "name"},
		{"bad status", sqlOK, func(c *ConnectionProfile) { c.Status = "paused" }, "status"},
		{"bad source system", sqlOK, func(c *ConnectionProfile) { c.SourceSystem = "custom_Acme" }, "sourceSystem"},
		{"unsupported driver", sqlOK, func(c *ConnectionProfile) { c.Config["driver"] = "mssql" }, "config.driver"},
		{"missing database", sqlOK, func(c *ConnectionProfile) { delete(c.Config, "database") }, "config.database"},
		{"port zero", sqlOK, func(c *ConnectionProfile) { c.Config["port"] = float64(0) }, "config.port"},
		{"fractional port", sqlOK, func(c *ConnectionProfile) { c.Config["port"] = 5432.5 }, "config.port"},
		{"string port", sqlOK, func(c *ConnectionProfile) { c.Config["port"] = "5432" }, "config.port"},
		{"host with slash", sqlOK, func(c *ConnectionProfile) { c.Config["host"] = "/var/run/postgresql" }, "config.host"},
		{"host with port", sqlOK, func(c *ConnectionProfile) { c.Config["host"] = "erp.acme.example:1433" }, "config.host"},
		{"ipv6 host with zone", sqlOK, func(c *ConnectionProfile) { c.Config["host"] = "fe80::1%eth0" }, "config.host"},
		{"host with space", sqlOK, func(c *ConnectionProfile) { c.Config["host"] = "a b" }, "config.host"},
		{"bad sslmode", sqlOK, func(c *ConnectionProfile) { c.Config["sslmode"] = "prefer" }, "config.sslmode"},
		{"password", sqlOK, func(c *ConnectionProfile) { c.Config["password"] = "x" }, "config.password"},
		{"Password cased", sqlOK, func(c *ConnectionProfile) { c.Config["Password"] = "x" }, "config.Password"},
		{"api key", restOK, func(c *ConnectionProfile) { c.Config["apiKey"] = "x" }, "config.apiKey"},
		{"authorization", restOK, func(c *ConnectionProfile) { c.Config["authorization"] = "x" }, "config.authorization"},
		{"foreign key for kind", restOK, func(c *ConnectionProfile) { c.Config["host"] = "x" }, "config.host"},
		{"non-string value", sqlOK, func(c *ConnectionProfile) { c.Config["user"] = 5 }, "config.user"},
		{"control characters", sqlOK, func(c *ConnectionProfile) { c.Config["user"] = "a\nb" }, "config.user"},
		{"too long value", sqlOK, func(c *ConnectionProfile) { c.Config["user"] = strings.Repeat("u", 256) }, "config.user"},
		{"ftp url", restOK, func(c *ConnectionProfile) { c.Config["baseUrl"] = "ftp://a.example" }, "config.baseUrl"},
		{"relative url", restOK, func(c *ConnectionProfile) { c.Config["baseUrl"] = "/odata" }, "config.baseUrl"},
		{"url fragment", restOK, func(c *ConnectionProfile) { c.Config["baseUrl"] = "https://a.example/#x" }, "config.baseUrl"},
		{"bad auth type", restOK, func(c *ConnectionProfile) { c.Config["authType"] = "oauth2" }, "config.authType"},
		{"bad header name", restOK, func(c *ConnectionProfile) {
			c.Config = map[string]any{"baseUrl": "https://a.example", "authType": "header", "headerName": "a b"}
		}, "config.headerName"},
		{"bad paging style", restOK, func(c *ConnectionProfile) { c.Config["pagingStyle"] = "magic" }, "config.pagingStyle"},
		{"probe path with query", restOK, func(c *ConnectionProfile) { c.Config["probePath"] = "/h?x=1" }, "config.probePath"},
		{"probe path with dot-dot", restOK, func(c *ConnectionProfile) { c.Config["probePath"] = "/a/../b" }, "config.probePath"},
		{"absolute drop dir", fileOK, func(c *ConnectionProfile) { c.Config["dropDir"] = "/etc" }, "config.dropDir"},
		{"dot-dot drop dir", fileOK, func(c *ConnectionProfile) { c.Config["dropDir"] = "a/../../b" }, "config.dropDir"},
		{"parent drop dir", fileOK, func(c *ConnectionProfile) { c.Config["dropDir"] = ".." }, "config.dropDir"},
		{"dot drop dir", fileOK, func(c *ConnectionProfile) { c.Config["dropDir"] = "." }, "config.dropDir"},
		{"unclean drop dir", fileOK, func(c *ConnectionProfile) { c.Config["dropDir"] = "a//b" }, "config.dropDir"},
		{"backslash drop dir", fileOK, func(c *ConnectionProfile) { c.Config["dropDir"] = `a\b` }, "config.dropDir"},
		{"bad format", fileOK, func(c *ConnectionProfile) { c.Config["format"] = "xlsx" }, "config.format"},
		{"two-char delimiter", fileOK, func(c *ConnectionProfile) { c.Config["delimiter"] = ";;" }, "config.delimiter"},
		{"path in file pattern", fileOK, func(c *ConnectionProfile) { c.Config["filePattern"] = "../*.csv" }, "config.filePattern"},
		{"unknown codepage", fileOK, func(c *ConnectionProfile) { c.Config["codepage"] = "klingon" }, "config.codepage"},
		{"unknown timezone", sqlOK, func(c *ConnectionProfile) { c.Config["timezone"] = "Mars/Base" }, "config.timezone"},
		{"local timezone", sqlOK, func(c *ConnectionProfile) { c.Config["timezone"] = "Local" }, "config.timezone"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.base
			c.Config = map[string]any{}
			for k, v := range tc.base.Config {
				c.Config[k] = v
			}
			tc.edit(&c)
			bad := ValidateConnection(c)
			if tc.field == "" && len(bad) != 0 {
				t.Fatalf("unexpected %v", bad)
			}
			if tc.field != "" && bad[tc.field] == "" {
				t.Fatalf("want %s in %v", tc.field, bad)
			}
		})
	}
}

func TestSecretInvalidated_OnlyByTheSettingsTheSecretIsBoundTo(t *testing.T) {
	sql := map[string]any{"driver": "postgres", "host": "erp.acme.example", "port": float64(5432), "database": "erp", "user": "svc", "sslmode": "verify-full", "timezone": "UTC"}
	rest := map[string]any{"baseUrl": "https://api.acme.example", "authType": "bearer", "probePath": "/health", "pagingStyle": "cursor"}
	with := func(base map[string]any, k string, v any) map[string]any {
		out := map[string]any{}
		for key, val := range base {
			out[key] = val
		}
		if v == nil {
			delete(out, k)
		} else {
			out[k] = v
		}
		return out
	}
	for _, tc := range []struct {
		name   string
		kind   string
		before map[string]any
		after  map[string]any
		want   bool
	}{
		{"same sql", ConnSQL, sql, with(sql, "timezone", "Europe/Berlin"), false},
		{"sql host", ConnSQL, sql, with(sql, "host", "evil.example"), true},
		{"sql port", ConnSQL, sql, with(sql, "port", float64(6543)), true},
		{"sql port dropped", ConnSQL, sql, with(sql, "port", nil), true},
		{"sql user", ConnSQL, sql, with(sql, "user", "other"), true},
		{"sql database", ConnSQL, sql, with(sql, "database", "other"), true},
		{"sql sslmode downgrade", ConnSQL, sql, with(sql, "sslmode", "disable"), true},
		{"rest base url", ConnREST, rest, with(rest, "baseUrl", "https://evil.example"), true},
		{"rest auth type", ConnREST, rest, with(rest, "authType", "basic"), true},
		{"rest probe path and paging", ConnREST, rest, with(with(rest, "probePath", "/ping"), "pagingStyle", "page"), false},
		{"file never", ConnFile, map[string]any{"dropDir": "a"}, map[string]any{"dropDir": "b"}, false},
	} {
		if got := SecretInvalidated(tc.kind, tc.before, tc.after); got != tc.want {
			t.Errorf("%s: SecretInvalidated = %v, want %v", tc.name, got, tc.want)
		}
	}
}
