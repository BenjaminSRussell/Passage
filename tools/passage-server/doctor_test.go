package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

type fakeDB struct {
	super, bypass bool
	migrations    int
	exists        bool
}

func (f fakeDB) Role(context.Context) (string, bool, bool, error) {
	return "passage_app", f.super, f.bypass, nil
}
func (f fakeDB) Migrations(context.Context) (int, bool, error) { return f.migrations, f.exists, nil }
func (f fakeDB) Close(context.Context) error                   { return nil }

func goodDeps() Deps {
	return Deps{
		OpenDB: func(context.Context, string) (DBInspector, error) {
			return fakeDB{migrations: 12, exists: true}, nil
		},
		HTTPGet: func(_ context.Context, u string) (int, []byte, error) {
			if strings.HasSuffix(u, "openid-configuration") {
				return 200, []byte(`{"issuer":"https://id.example/realms/p","jwks_uri":"https://id.example/jwks"}`), nil
			}
			return 200, nil, nil
		},
		Dial:        func(context.Context, string) error { return nil },
		Stat:        func(string) (os.FileInfo, error) { return nil, nil },
		LoadKeyPair: func(string, string) error { return nil },
		Listen:      func(string) error { return nil },
		FIPSEnabled: func() bool { return false },
		FIPSBuilt:   func() bool { return false },
		WebEmbedded: true,
		ProbeWrite:  func(string) error { return nil },
	}
}

func goodConfig() (Config, []Process) {
	c := DefaultConfig()
	c.DatabaseDSN = "postgres://passage_app:pw@db:5432/postgres"
	c.OIDCIssuer = "https://id.example/realms/p"
	c.OPAURL = "http://opa:8181"
	c.KafkaBootstrap = "k1:9092,k2:9092"
	c.TemporalHost = "t:7233"
	c.DataDir = "d"
	c.InstallDir = "i"
	c.TLS = TLSConfig{CertFile: "c.pem", KeyFile: "k.pem"}
	return c, []Process{{Name: "party", Exe: "party.exe", Env: map[string]string{"DATABASE_URL": "postgres://x/passage_party"}}}
}

func find(rs []Result, name string) Result {
	for _, r := range rs {
		if r.Name == name {
			return r
		}
	}
	return Result{}
}

func TestDoctorAllGood(t *testing.T) {
	c, p := goodConfig()
	rs := Doctor(context.Background(), c, p, goodDeps())
	if Failed(rs) {
		var b bytes.Buffer
		WriteResults(&b, rs)
		t.Fatalf("unexpected failure:\n%s", b.String())
	}
}

func TestDoctorBadDSN(t *testing.T) {
	c, p := goodConfig()
	d := goodDeps()
	d.OpenDB = func(context.Context, string) (DBInspector, error) {
		return nil, errors.New(`password authentication failed for user "x" (pw)`)
	}
	rs := Doctor(context.Background(), c, p, d)
	r := find(rs, "postgres")
	if r.Level != LevelFail {
		t.Fatalf("postgres = %+v", r)
	}
	if strings.Contains(r.Detail, "(pw)") {
		t.Fatalf("password leaked into the doctor line: %s", r.Detail)
	}
}

func TestDoctorRejectsBypassRLSRole(t *testing.T) {
	c, p := goodConfig()
	d := goodDeps()
	d.OpenDB = func(context.Context, string) (DBInspector, error) {
		return fakeDB{bypass: true, migrations: 1, exists: true}, nil
	}
	r := find(Doctor(context.Background(), c, p, d), "postgres-role")
	if r.Level != LevelFail || !strings.Contains(r.Detail, "BYPASSRLS") {
		t.Fatalf("role = %+v", r)
	}
}

func TestDoctorUnreachableIssuer(t *testing.T) {
	c, p := goodConfig()
	d := goodDeps()
	d.HTTPGet = func(_ context.Context, u string) (int, []byte, error) {
		if strings.HasSuffix(u, "openid-configuration") {
			return 0, nil, errors.New("dial tcp: no such host")
		}
		return 200, nil, nil
	}
	if r := find(Doctor(context.Background(), c, p, d), "oidc"); r.Level != LevelFail {
		t.Fatalf("oidc = %+v", r)
	}
}

func TestDoctorIssuerMismatch(t *testing.T) {
	c, p := goodConfig()
	c.OIDCIssuer = "https://other.example/realms/p"
	d := goodDeps()
	d.HTTPGet = func(context.Context, string) (int, []byte, error) {
		return 200, []byte(`{"issuer":"https://id.example/realms/p"}`), nil
	}
	if r := find(Doctor(context.Background(), c, p, d), "oidc"); r.Level != LevelFail {
		t.Fatalf("oidc = %+v", r)
	}
}

func TestDoctorKafkaAndTemporalDown(t *testing.T) {
	c, p := goodConfig()
	d := goodDeps()
	d.Dial = func(context.Context, string) error { return errors.New("refused") }
	rs := Doctor(context.Background(), c, p, d)
	if find(rs, "kafka").Level != LevelFail || find(rs, "temporal").Level != LevelFail {
		t.Fatalf("results = %+v", rs)
	}
}

func TestDoctorMigrationsPendingIsAWarning(t *testing.T) {
	c, p := goodConfig()
	d := goodDeps()
	d.OpenDB = func(_ context.Context, dsn string) (DBInspector, error) {
		if strings.Contains(dsn, "passage_party") {
			return fakeDB{exists: false}, nil
		}
		return fakeDB{}, nil
	}
	r := find(Doctor(context.Background(), c, p, d), "migrations")
	if r.Level != LevelWarn || !strings.Contains(r.Detail, "party") {
		t.Fatalf("migrations = %+v", r)
	}
}

func TestDoctorFIPS(t *testing.T) {
	c, p := goodConfig()
	c.RequireFIPS = true
	if r := find(Doctor(context.Background(), c, p, goodDeps()), "fips"); r.Level != LevelFail {
		t.Fatalf("fips = %+v", r)
	}
	d := goodDeps()
	d.FIPSEnabled = func() bool { return true }
	if r := find(Doctor(context.Background(), c, p, d), "fips"); r.Level != LevelOK {
		t.Fatalf("fips = %+v", r)
	}
}

func TestDoctorTLSRequiredOffLoopback(t *testing.T) {
	c, p := goodConfig()
	c.TLS = TLSConfig{}
	c.Listen = "0.0.0.0:8443"
	if r := find(Doctor(context.Background(), c, p, goodDeps()), "tls"); r.Level != LevelFail {
		t.Fatalf("tls = %+v", r)
	}
}

func TestDoctorReportsFeatures(t *testing.T) {
	c, p := goodConfig()
	c.Features = map[string]bool{"node": true}
	d := goodDeps()
	d.Stat = func(path string) (os.FileInfo, error) {
		if strings.Contains(path, "node") {
			return nil, os.ErrNotExist
		}
		return nil, nil
	}
	rs := Doctor(context.Background(), c, p, d)
	if find(rs, "feature-node").Level != LevelFail {
		t.Fatalf("node feature = %+v", find(rs, "feature-node"))
	}
	if r := find(rs, "feature-python"); r.Level != LevelOK || !strings.Contains(r.Detail, "off") {
		t.Fatalf("python feature = %+v", r)
	}
}

func TestDoctorMissingExecutable(t *testing.T) {
	c, p := goodConfig()
	d := goodDeps()
	d.Stat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	if r := find(Doctor(context.Background(), c, p, d), "services"); r.Level != LevelFail {
		t.Fatalf("services = %+v", r)
	}
}

func TestWriteResultsFormat(t *testing.T) {
	var b bytes.Buffer
	WriteResults(&b, []Result{{Name: "x", Level: LevelFail, Detail: "bad"}})
	if got := b.String(); got != "FAIL x: bad\n" {
		t.Fatalf("got %q", got)
	}
}
