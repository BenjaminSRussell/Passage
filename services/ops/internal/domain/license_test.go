package domain

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"reflect"
	"testing"
	"time"
)

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

var testLicense = License{TenantID: "t1", IssuedAt: "2026-01-01", ExpiresAt: "2026-12-31", FullUsers: 100, OperationalUsers: 50, SelfServiceUsers: 500}

// The cases are the ones of docs/ops/tools/test_license_state.py.
func TestEvaluateLicense(t *testing.T) {
	for _, c := range []struct {
		name      string
		today     string
		active    int
		state     LicenseState
		days      int
		login     bool
		banner    bool
		codes     []string
		graceDays *int
	}{
		{"valid well before expiry", "2026-06-01", 90, LicenseValid, 213, true, false, []string{}, nil},
		{"the expiry day is still valid", "2026-12-31", 1, LicenseValid, 0, true, false, []string{"LIC-1801"}, nil},
		{"29 days before warns", "2026-12-02", 1, LicenseValid, 29, true, false, []string{"LIC-1801"}, nil},
		{"31 days before does not", "2026-11-30", 1, LicenseValid, 31, true, false, []string{}, nil},
		{"grace for 30 days after expiry", "2027-01-30", 1, LicenseGrace, -30, true, true, []string{"LIC-1802"}, nil},
		{"expired 31 days ago blocks login (AC-50.8)", "2027-01-31", 1, LicenseExpired, -31, false, false, []string{"LIC-1803"}, nil},
		{"over-use is grace before expiry", "2026-06-01", 101, LicenseGrace, 213, true, true, []string{"LIC-1802"}, nil},
		{"over-use near expiry warns and graces", "2026-12-20", 101, LicenseGrace, 11, true, true, []string{"LIC-1801", "LIC-1802"}, nil},
		{"exactly the licensed users is valid", "2026-06-01", 100, LicenseValid, 213, true, false, []string{}, nil},
		{"grace days 0 expires the next day", "2027-01-01", 1, LicenseExpired, -1, false, false, []string{"LIC-1803"}, ptr(0)},
		{"grace days 60", "2027-03-01", 1, LicenseGrace, -60, true, true, []string{"LIC-1802"}, ptr(60)},
	} {
		l := testLicense
		l.GraceDays = c.graceDays
		st, err := EvaluateLicense(l, c.active, day(c.today))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if st.State != c.state || st.DaysToExpiry != c.days || st.LoginAllowed != c.login || st.Banner != c.banner || !reflect.DeepEqual(st.Codes, c.codes) {
			t.Errorf("%s: got %+v", c.name, st)
		}
		if !st.AdminLoginAllowed || !st.ExportAllowed {
			t.Errorf("%s: admin sign-in and export are never blocked (BR-50.8)", c.name)
		}
	}
}

func ptr(n int) *int { return &n }

func TestParseLicenseRefusesBadPayloads(t *testing.T) {
	for name, payload := range map[string]string{
		"unknown field":  `{"tenantId":"t","issuedAt":"2026-01-01","expiresAt":"2026-12-31","fullUsers":1,"operationalUsers":0,"selfserviceUsers":0,"x":1}`,
		"bad date":       `{"tenantId":"t","issuedAt":"01/01/2026","expiresAt":"2026-12-31","fullUsers":1,"operationalUsers":0,"selfserviceUsers":0}`,
		"expiry first":   `{"tenantId":"t","issuedAt":"2026-12-31","expiresAt":"2026-01-01","fullUsers":1,"operationalUsers":0,"selfserviceUsers":0}`,
		"negative count": `{"tenantId":"t","issuedAt":"2026-01-01","expiresAt":"2026-12-31","fullUsers":-1,"operationalUsers":0,"selfserviceUsers":0}`,
		"grace over 60":  `{"tenantId":"t","issuedAt":"2026-01-01","expiresAt":"2026-12-31","fullUsers":1,"operationalUsers":0,"selfserviceUsers":0,"graceDays":61}`,
		"no tenant":      `{"issuedAt":"2026-01-01","expiresAt":"2026-12-31","fullUsers":1,"operationalUsers":0,"selfserviceUsers":0}`,
		"not json":       `licence`,
	} {
		var e *Error
		if _, err := ParseLicense([]byte(payload)); !errors.As(err, &e) || e.Code != CodeLicenseInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestVerifyLicenseChecksTheSignatureOverTheExactBytes(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"tenantId":"t1","issuedAt":"2026-01-01","expiresAt":"2026-12-31","fullUsers":100,"operationalUsers":50,"selfserviceUsers":500}`)
	sig := ed25519.Sign(priv, payload)
	l, err := VerifyLicense(pub, payload, sig)
	if err != nil || l.FullUsers != 100 || l.Grace() != 30 {
		t.Fatalf("%+v %v", l, err)
	}
	var e *Error
	if _, err := VerifyLicense(pub, append([]byte(" "), payload...), sig); !errors.As(err, &e) || e.Code != CodeLicenseSignature {
		t.Fatalf("a changed payload must fail LIC-1804: %v", err)
	}
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := VerifyLicense(otherPub, payload, sig); !errors.As(err, &e) || e.Code != CodeLicenseSignature {
		t.Fatalf("another key must fail: %v", err)
	}
	if _, err := VerifyLicense(nil, payload, sig); !errors.As(err, &e) || e.Code != CodeLicenseSignature {
		t.Fatalf("no key must fail: %v", err)
	}
}

func TestLicenseMessage(t *testing.T) {
	st := LicenseStatus{DaysToExpiry: 12}
	if got := LicenseMessage(CodeLicenseExpiring, testLicense, 0, st); got != "License expires in 12 days." {
		t.Fatal(got)
	}
	if got := LicenseMessage(CodeLicenseGrace, testLicense, 120, st); got != "License in grace period: 120 full users active, 100 licensed." {
		t.Fatal(got)
	}
}
