package fieldcrypt_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/fieldcrypt"
)

func key(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

func keysWith(t *testing.T, current string, ids ...string) *fieldcrypt.Keys {
	t.Helper()
	m := map[string][]byte{}
	for i, id := range ids {
		m[id] = key(byte(i + 1))
	}
	k, err := fieldcrypt.NewKeys(current, m)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSealOpenRoundTripAndFreshNonce(t *testing.T) {
	k := keysWith(t, "k1", "k1")
	a, err := k.Seal("tenant-a", "bank", []byte("DE89370400440532013000"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := k.Seal("tenant-a", "bank", []byte("DE89370400440532013000"))
	if bytes.Equal(a, b) || bytes.Contains(a, []byte("DE89")) {
		t.Fatal("ciphertext must be randomised and hide the plaintext")
	}
	got, err := k.Open("tenant-a", "bank", a)
	if err != nil || string(got) != "DE89370400440532013000" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestOpenRefusesAnotherTenantFieldOrTamperedValue(t *testing.T) {
	k := keysWith(t, "k1", "k1")
	sealed, _ := k.Seal("tenant-a", "bank", []byte("secret"))
	if _, err := k.Open("tenant-b", "bank", sealed); !errors.Is(err, fieldcrypt.ErrOpen) {
		t.Errorf("other tenant: %v", err)
	}
	if _, err := k.Open("tenant-a", "name", sealed); !errors.Is(err, fieldcrypt.ErrOpen) {
		t.Errorf("other field: %v", err)
	}
	tampered := append([]byte(nil), sealed...)
	tampered[len(tampered)-1] ^= 1
	if _, err := k.Open("tenant-a", "bank", tampered); !errors.Is(err, fieldcrypt.ErrOpen) {
		t.Errorf("tampered: %v", err)
	}
	for _, short := range [][]byte{nil, {1}, {1, 9, 'x'}, {2, 0}} {
		if _, err := k.Open("tenant-a", "bank", short); !errors.Is(err, fieldcrypt.ErrOpen) {
			t.Errorf("malformed %v: %v", short, err)
		}
	}
	if _, err := k.Seal("", "bank", []byte("x")); err == nil {
		t.Error("a seal without a tenant must fail")
	}
}

func TestRotatedKeyStillOpensOldValues(t *testing.T) {
	old := keysWith(t, "k1", "k1")
	sealed, _ := old.Seal("tenant-a", "bank", []byte("secret"))
	rotated := keysWith(t, "k2", "k1", "k2")
	got, err := rotated.Open("tenant-a", "bank", sealed)
	if err != nil || string(got) != "secret" {
		t.Fatalf("got %q err %v", got, err)
	}
	fresh, _ := rotated.Seal("tenant-a", "bank", []byte("secret"))
	if fresh[2] != 'k' || fresh[3] != '2' {
		t.Fatalf("new values must use the current key id: %q", fresh[2:4])
	}
	if _, err := old.Open("tenant-a", "bank", fresh); !errors.Is(err, fieldcrypt.ErrOpen) {
		t.Fatalf("a keyring without k2 cannot open: %v", err)
	}
}

func TestNewKeysValidates(t *testing.T) {
	if _, err := fieldcrypt.NewKeys("k1", map[string][]byte{"k1": []byte("short")}); err == nil {
		t.Error("short key")
	}
	if _, err := fieldcrypt.NewKeys("missing", map[string][]byte{"k1": key(1)}); err == nil {
		t.Error("unknown current key")
	}
}

func TestFromEnv(t *testing.T) {
	enc := base64.StdEncoding.EncodeToString(key(7))
	get := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if k, err := fieldcrypt.FromEnv(get(nil)); k != nil || err != nil {
		t.Fatalf("unset = %v %v", k, err)
	}
	k, err := fieldcrypt.FromEnv(get(map[string]string{fieldcrypt.EnvKeys: "k7=" + enc, fieldcrypt.EnvCurrentID: "k7"}))
	if err != nil || k == nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "keys")
	_ = os.WriteFile(file, []byte("k7="+enc+"\nk8="+enc+"\n"), 0o600)
	if _, err := fieldcrypt.FromEnv(get(map[string]string{fieldcrypt.EnvKeysFile: file, fieldcrypt.EnvCurrentID: "k8"})); err != nil {
		t.Fatalf("mounted file: %v", err)
	}
	other := base64.StdEncoding.EncodeToString(key(8))
	both, err := fieldcrypt.FromEnv(get(map[string]string{fieldcrypt.EnvKeys: "k9=" + other, fieldcrypt.EnvKeysFile: file, fieldcrypt.EnvCurrentID: "k8"}))
	if err != nil {
		t.Fatalf("the mounted file must take precedence over the environment: %v", err)
	}
	sealed, _ := both.Seal("t", "f", []byte("x"))
	if string(sealed[2:4]) != "k8" {
		t.Fatalf("sealed with %q, want the file's key k8", sealed[2:4])
	}
	for name, env := range map[string]map[string]string{
		"bad base64":   {fieldcrypt.EnvKeys: "k=!!", fieldcrypt.EnvCurrentID: "k"},
		"duplicate":    {fieldcrypt.EnvKeys: "k=" + enc + ",k=" + enc, fieldcrypt.EnvCurrentID: "k"},
		"no current":   {fieldcrypt.EnvKeys: "k=" + enc},
		"missing file": {fieldcrypt.EnvKeysFile: filepath.Join(t.TempDir(), "nope")},
	} {
		if _, err := fieldcrypt.FromEnv(get(env)); err == nil {
			t.Errorf("%s must fail", name)
		}
	}
}

func TestMACIsKeyedPerTenantAndSeparateFromSeal(t *testing.T) {
	k := keysWith(t, "k1", "k1")
	a, err := k.MAC("tenant-a", []byte("subject|email"))
	if err != nil {
		t.Fatal(err)
	}
	again, _ := k.MAC("tenant-a", []byte("subject|email"))
	other, _ := k.MAC("tenant-b", []byte("subject|email"))
	diff, _ := k.MAC("tenant-a", []byte("subject|phone"))
	rotated, _ := keysWith(t, "k2", "k1", "k2").MAC("tenant-a", []byte("subject|email"))
	if len(a) != 32 || !bytes.Equal(a, again) {
		t.Fatal("mac must be 32 bytes and deterministic")
	}
	if bytes.Equal(a, other) || bytes.Equal(a, diff) || bytes.Equal(a, rotated) {
		t.Fatal("mac must differ across tenants, messages and master keys")
	}
	if _, err := k.MAC("", []byte("x")); err == nil {
		t.Fatal("mac without a tenant id must fail")
	}
}

func TestMACKnownAnswer(t *testing.T) {
	got, err := keysWith(t, "k1", "k1").MAC("tenant-a", []byte("subject|email"))
	if err != nil {
		t.Fatal(err)
	}
	if hex := fmt.Sprintf("%x", got); hex != "9d68c037adb7f1ab6a7e652dc822e16f9ef453aa924977cca7363d4b0ecaacd9" {
		t.Fatalf("mac drifted: %s", hex)
	}
}
