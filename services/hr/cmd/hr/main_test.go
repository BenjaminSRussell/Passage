package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"encoding/base64"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

var (
	fieldKey  = bytes.Repeat([]byte{7}, 32)
	legacyKey = bytes.Repeat([]byte{9}, 32)
)

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestSealerFromEnv_RefusesToStartWithoutUsableKeys(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{"no fieldcrypt keys", map[string]string{"HR_PII_KEY": b64(legacyKey)}, "FIELDCRYPT_KEYS or FIELDCRYPT_KEYS_FILE is required"},
		{"malformed fieldcrypt entry", map[string]string{"FIELDCRYPT_KEYS": "k1=short", "FIELDCRYPT_CURRENT_KEY_ID": "k1"}, "not id=base64"},
		{"current key id not configured", map[string]string{"FIELDCRYPT_KEYS": "k1=" + b64(fieldKey), "FIELDCRYPT_CURRENT_KEY_ID": "k2"}, "current key id"},
		{"missing keys file", map[string]string{"FIELDCRYPT_KEYS_FILE": filepath.Join(t.TempDir(), "absent"), "FIELDCRYPT_CURRENT_KEY_ID": "k1"}, "FIELDCRYPT_KEYS_FILE"},
		{"legacy key not base64", map[string]string{"FIELDCRYPT_KEYS": "k1=" + b64(fieldKey), "FIELDCRYPT_CURRENT_KEY_ID": "k1", "HR_PII_KEY": "!!"}, "HR_PII_KEY is not base64"},
		{"legacy key wrong size", map[string]string{"FIELDCRYPT_KEYS": "k1=" + b64(fieldKey), "FIELDCRYPT_CURRENT_KEY_ID": "k1", "HR_PII_KEY": b64([]byte("short"))}, "32 bytes"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := sealerFromEnv(envOf(tc.env)); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("got %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestSealerFromEnv_KeysFileWinsAndLegacyKeyOpensOldValues(t *testing.T) {
	file := filepath.Join(t.TempDir(), "keys")
	if err := os.WriteFile(file, []byte("k1="+b64(fieldKey)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"FIELDCRYPT_KEYS_FILE": file, "FIELDCRYPT_KEYS": "ignored=" + b64(legacyKey), "FIELDCRYPT_CURRENT_KEY_ID": "k1"}
	plain, err := sealerFromEnv(envOf(env))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := plain.Seal("tenant", "worker_pii.national_id", "123-45-6789")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := plain.Open("tenant", "worker_pii.national_id", sealed); err != nil || got != "123-45-6789" {
		t.Fatalf("Open = %q, %v", got, err)
	}

	block, _ := aes.NewCipher(legacyKey)
	aead, _ := cipher.NewGCM(block)
	nonce := bytes.Repeat([]byte{1}, aead.NonceSize())
	old := aead.Seal(nonce, nonce, []byte("987-65-4321"), nil)
	if _, err := plain.Open("tenant", "worker_pii.national_id", old); err == nil {
		t.Fatal("without HR_PII_KEY a legacy value must not open")
	}
	env["HR_PII_KEY"] = b64(legacyKey)
	withLegacy, err := sealerFromEnv(envOf(env))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := withLegacy.Open("tenant", "worker_pii.national_id", old); err != nil || got != "987-65-4321" {
		t.Fatalf("legacy Open = %q, %v", got, err)
	}
}

func TestReseal_ValidatesArgumentsBeforeConnecting(t *testing.T) {
	keys := map[string]string{"FIELDCRYPT_KEYS": "k1=" + b64(fieldKey), "FIELDCRYPT_CURRENT_KEY_ID": "k1"}
	tenantID := "00000000-0000-4000-8000-000000000001"
	tests := []struct {
		name    string
		env     map[string]string
		args    []string
		wantErr string
	}{
		{"no tenants", map[string]string{"DATABASE_URL": "postgres://x"}, nil, "usage: hr reseal-pii"},
		{"bad tenant id", map[string]string{"DATABASE_URL": "postgres://x"}, []string{tenantID, "acme"}, `tenant id "acme"`},
		{"nil tenant id", map[string]string{"DATABASE_URL": "postgres://x"}, []string{"00000000-0000-0000-0000-000000000000"}, "is not a UUID"},
		{"missing database url", keys, []string{tenantID}, "DATABASE_URL is required"},
		{"missing keys", map[string]string{"DATABASE_URL": "postgres://x"}, []string{tenantID}, "FIELDCRYPT_KEYS or FIELDCRYPT_KEYS_FILE is required"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := reseal(context.Background(), envOf(tc.env), tc.args); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("got %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestPHIReadAuditFromEnv(t *testing.T) {
	signer, err := tenant.NewServiceSigner("hr", ed25519.NewKeyFromSeed(bytes.Repeat([]byte{3}, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	const url = "http://audit.example.svc:8080"
	tests := []struct {
		name    string
		env     map[string]string
		signer  *tenant.ServiceSigner
		wantOn  bool
		wantErr string
	}{
		{"commercial is off", map[string]string{"PASSAGE_CLOUD_PROFILE": "commercial"}, nil, false, ""},
		{"unset is off", map[string]string{}, nil, false, ""},
		{"hipaa overlay is on", map[string]string{"PASSAGE_CLOUD_PROFILE": "commercial+hipaa", "AUDIT_URL": url}, signer, true, ""},
		{"hipaa overlay needs the audit url", map[string]string{"PASSAGE_CLOUD_PROFILE": "commercial+hipaa"}, signer, false, "AUDIT_URL is required"},
		{"hipaa overlay needs a signing key", map[string]string{"PASSAGE_CLOUD_PROFILE": "commercial+hipaa", "AUDIT_URL": url}, nil, false, "SIGNING_KEY"},
		{"switch forces it on", map[string]string{"PASSAGE_PHI_READ_AUDIT": "true", "AUDIT_URL": url}, signer, true, ""},
		{"switch forces it off", map[string]string{"PASSAGE_CLOUD_PROFILE": "commercial+hipaa", "PASSAGE_PHI_READ_AUDIT": "false"}, nil, false, ""},
		{"unknown profile fails closed", map[string]string{"PASSAGE_CLOUD_PROFILE": "nowhere+hipaa"}, signer, false, "unknown cloud profile"},
	}
	for _, c := range tests {
		rec, err := phiReadAuditFromEnv(envOf(c.env), c.signer)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%s: err = %v, want %q", c.name, err, c.wantErr)
			}
			continue
		}
		if err != nil || (rec != nil) != c.wantOn {
			t.Errorf("%s: recorder on = %v, err = %v, want on = %v", c.name, rec != nil, err, c.wantOn)
		}
	}
}

// TestSubcommandsLogThroughThePseudonymizer: reseal-pii and the tenant jobs never reach httpx.Serve, so the
// pseudonymizer is installed with the logger, before the subcommand is chosen (#605).
func TestSubcommandsLogThroughThePseudonymizer(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	var buf bytes.Buffer
	if err := setupLogging(&buf, envOf(map[string]string{"LOG_PSEUDONYMIZE": "true"})); err != nil {
		t.Fatal(err)
	}
	slog.Info("resealed", "tenant", "11111111-1111-4111-8111-111111111111", "email", "ann@example.com")
	if strings.Contains(buf.String(), "ann@example.com") || !strings.Contains(buf.String(), `"service":"hr"`) {
		t.Fatalf("log line: %s", buf.String())
	}
	if err := setupLogging(&buf, envOf(map[string]string{"PASSAGE_CLOUD_PROFILE": "nope"})); err == nil {
		t.Fatal("a bad profile must stop the command, not log raw values")
	}
}
