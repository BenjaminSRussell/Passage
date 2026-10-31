package fieldcrypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	EnvKeys      = "FIELDCRYPT_KEYS"
	EnvKeysFile  = "FIELDCRYPT_KEYS_FILE"
	EnvCurrentID = "FIELDCRYPT_CURRENT_KEY_ID"

	keySize       = 32
	envelopeV1    = 1
	maxKeyIDBytes = 64
	kdfInfo       = "passage.fieldcrypt.v1"
	macKDFInfo    = "passage.fieldcrypt.mac.v1"
)

var ErrOpen = errors.New("fieldcrypt: cannot open value")

type Keys struct {
	current string
	keys    map[string][]byte
}

func NewKeys(current string, keys map[string][]byte) (*Keys, error) {
	if _, ok := keys[current]; !ok || current == "" || len(current) > maxKeyIDBytes {
		return nil, errors.New("fieldcrypt: the current key id must name a configured key")
	}
	own := make(map[string][]byte, len(keys))
	for id, k := range keys {
		if len(k) != keySize || id == "" || len(id) > maxKeyIDBytes {
			return nil, fmt.Errorf("fieldcrypt: key %q must be %d bytes", id, keySize)
		}
		own[id] = append([]byte(nil), k...)
	}
	return &Keys{current: current, keys: own}, nil
}

// FromEnv reads FIELDCRYPT_KEYS (or the mounted secret file named by FIELDCRYPT_KEYS_FILE) as
// comma-separated id=base64(32 bytes) pairs and FIELDCRYPT_CURRENT_KEY_ID; it returns nil, nil when unset.
func FromEnv(getenv func(string) string) (*Keys, error) {
	raw := strings.TrimSpace(getenv(EnvKeys))
	if file := getenv(EnvKeysFile); file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", EnvKeysFile, err)
		}
		raw = strings.TrimSpace(string(b))
	}
	if raw == "" {
		return nil, nil
	}
	keys := map[string][]byte{}
	for _, entry := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' }) {
		id, enc, ok := strings.Cut(strings.TrimSpace(entry), "=")
		key, err := base64.StdEncoding.DecodeString(enc)
		if !ok || err != nil || len(key) != keySize {
			return nil, fmt.Errorf("%s entry %q is not id=base64 %d-byte key", EnvKeys, id, keySize)
		}
		if _, dup := keys[id]; dup {
			return nil, fmt.Errorf("%s repeats key id %s", EnvKeys, id)
		}
		keys[id] = key
	}
	return NewKeys(getenv(EnvCurrentID), keys)
}

func (k *Keys) aead(keyID, tenantID string) (cipher.AEAD, error) {
	master, ok := k.keys[keyID]
	if !ok || tenantID == "" {
		return nil, ErrOpen
	}
	data, err := hkdf.Key(sha256.New, master, []byte(tenantID), kdfInfo, keySize)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(data)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func additional(tenantID, field string) []byte { return []byte(tenantID + "\x00" + field) }

// Seal encrypts plain under a data key derived for tenantID from the current master key. The output is
// version | key id length | key id | nonce | ciphertext; tenant and field are bound as additional data.
func (k *Keys) Seal(tenantID, field string, plain []byte) ([]byte, error) {
	aead, err := k.aead(k.current, tenantID)
	if err != nil {
		return nil, errors.New("fieldcrypt: seal needs a tenant id")
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	out := append([]byte{envelopeV1, byte(len(k.current))}, k.current...)
	out = append(out, nonce...)
	return aead.Seal(out, nonce, plain, additional(tenantID, field)), nil
}

func (k *Keys) Open(tenantID, field string, sealed []byte) ([]byte, error) {
	if len(sealed) < 2 || sealed[0] != envelopeV1 || len(sealed) < 2+int(sealed[1]) {
		return nil, ErrOpen
	}
	keyID := string(sealed[2 : 2+int(sealed[1])])
	aead, err := k.aead(keyID, tenantID)
	if err != nil {
		return nil, ErrOpen
	}
	rest := sealed[2+int(sealed[1]):]
	if len(rest) < aead.NonceSize() {
		return nil, ErrOpen
	}
	plain, err := aead.Open(nil, rest[:aead.NonceSize()], rest[aead.NonceSize():], additional(tenantID, field))
	if err != nil {
		return nil, ErrOpen
	}
	return plain, nil
}

// MAC returns HMAC-SHA256(message) under a tenant key derived from the current master key with a purpose
// separate from Seal, so a pseudonym can neither be recomputed without the key nor decrypt anything.
func (k *Keys) MAC(tenantID string, message []byte) ([]byte, error) {
	return k.MACWithKey(k.current, tenantID, message)
}

func (k *Keys) CurrentID() string { return k.current }

// MACWithKey is MAC under a named master key, so a value tagged with its key id still verifies after rotation.
func (k *Keys) MACWithKey(keyID, tenantID string, message []byte) ([]byte, error) {
	if tenantID == "" {
		return nil, errors.New("fieldcrypt: mac needs a tenant id")
	}
	master, ok := k.keys[keyID]
	if !ok {
		return nil, ErrOpen
	}
	derived, err := hkdf.Key(sha256.New, master, []byte(tenantID), macKDFInfo, keySize)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, derived)
	mac.Write(message)
	return mac.Sum(nil), nil
}
