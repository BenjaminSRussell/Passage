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
