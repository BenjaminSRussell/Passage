package fieldcrypt

import (
	"bytes"
	"testing"
)

func TestMACWithKey_OldKeyStillVerifiesAfterRotationAndUnknownKeyFails(t *testing.T) {
	old, _ := NewKeys("k1", map[string][]byte{"k1": bytes.Repeat([]byte{1}, 32)})
	rotated, _ := NewKeys("k2", map[string][]byte{"k1": bytes.Repeat([]byte{1}, 32), "k2": bytes.Repeat([]byte{2}, 32)})
	want, _ := old.MAC("t", []byte("m"))
	got, err := rotated.MACWithKey("k1", "t", []byte("m"))
	if err != nil || !bytes.Equal(want, got) || rotated.CurrentID() != "k2" {
		t.Fatalf("got %x err %v", got, err)
	}
	if cur, _ := rotated.MAC("t", []byte("m")); bytes.Equal(cur, want) {
		t.Fatal("the current key must produce a different MAC")
	}
	if _, err := old.MACWithKey("k2", "t", []byte("m")); err == nil {
		t.Fatal("an unknown key id must fail")
	}
}

func TestKeyID_NamesTheSealingKeyAndRejectsNonEnvelopes(t *testing.T) {
	rotated, _ := NewKeys("k2", map[string][]byte{"k1": bytes.Repeat([]byte{1}, 32), "k2": bytes.Repeat([]byte{2}, 32)})
	sealed, err := rotated.Seal("t", "f", []byte("v"))
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := KeyID(sealed); !ok || id != "k2" {
		t.Fatalf("KeyID = %q, %v; want k2", id, ok)
	}
	for name, raw := range map[string][]byte{"empty": nil, "one byte": {envelopeV1}, "wrong version": {2, 1, 'k'}, "truncated id": {envelopeV1, 5, 'k'}} {
		if id, ok := KeyID(raw); ok {
			t.Errorf("%s: KeyID = %q, want no envelope", name, id)
		}
	}
}
