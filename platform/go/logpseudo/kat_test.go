package logpseudo_test

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/fieldcrypt"
	"github.com/BenjaminSRussell/passage/platform/logpseudo"
)

// Known answers shared with services/agents-ts/test/logging.test.ts and services/dispatch-py/tests/test_logpseudo.py:
// master key bytes 0..31 (base64 AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=), key id k1. If this changes, the TypeScript
// and Python loggers no longer produce the same pseudonym for the same person.
func TestKnownAnswers(t *testing.T) {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i)
	}
	ks, err := fieldcrypt.NewKeys("k1", map[string][]byte{"k1": k})
	if err != nil {
		t.Fatal(err)
	}
	for tenantID, want := range map[string]string{
		"11111111-1111-4111-8111-111111111111": "pn_81c1af9dc7db4ac7",
		"22222222-2222-4222-8222-222222222222": "pn_cc6b2b4cc9040118",
	} {
		if got := logpseudo.PseudonymFor(ks, tenantID, "Jane.Doe@Example.com"); got != want {
			t.Errorf("tenant %s: pseudonym = %s, want %s", tenantID, got, want)
		}
	}
}
