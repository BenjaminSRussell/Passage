package fieldcrypt

import (
	"context"
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
	"time"

	"github.com/BenjaminSRussell/passage/platform/fieldcrypt/azurekv"
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

// WrappedPrefix marks a master key entry that is wrapped by a key-management service (#600):
// FIELDCRYPT_KEYS=id=wrapped:<base64 of the wrapped 32-byte key>.
const WrappedPrefix = "wrapped:"

// Unwrapper unwraps a wrapped master key, for example through Azure Key Vault or a Managed HSM (package azurekv).
type Unwrapper interface {
	Unwrap(ctx context.Context, wrapped []byte) ([]byte, error)
}

// FromEnv reads FIELDCRYPT_KEYS (or the mounted secret file named by FIELDCRYPT_KEYS_FILE) as
// comma-separated id=base64(32 bytes) pairs and FIELDCRYPT_CURRENT_KEY_ID; it returns nil, nil when unset.
// An entry written id=wrapped:<base64> is a master key wrapped by Azure Key Vault or a Managed HSM
// (FIELDCRYPT_AZURE_KEY_ID names the wrapping key); it is unwrapped once here and held in memory only.
func FromEnv(getenv func(string) string) (*Keys, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return FromEnvWith(ctx, getenv, nil)
}

// FromEnvWith is FromEnv with an explicit Unwrapper for wrapped entries; nil builds the Azure Key Vault client
// from the environment, and only when an entry is wrapped.
func FromEnvWith(ctx context.Context, getenv func(string) string, unwrap Unwrapper) (*Keys, error) {
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
		var key []byte
		var err error
		if wrapped, isWrapped := strings.CutPrefix(enc, WrappedPrefix); ok && isWrapped {
			if unwrap == nil {
				client, cerr := azurekv.FromEnv(getenv)
				if cerr != nil {
					return nil, fmt.Errorf("%s entry %q is wrapped: %w", EnvKeys, id, cerr)
				}
				unwrap = client
			}
			blob, derr := base64.StdEncoding.DecodeString(wrapped)
			if derr != nil {
				return nil, fmt.Errorf("%s entry %q is not id=wrapped:base64", EnvKeys, id)
			}
			if key, err = unwrap.Unwrap(ctx, blob); err != nil {
				return nil, fmt.Errorf("%s entry %q: unwrap: %w", EnvKeys, id, err)
			}
		} else {
			key, err = base64.StdEncoding.DecodeString(enc)
		}
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

// WrapEntry generates nothing itself: it wraps an existing master key and returns the FIELDCRYPT_KEYS entry for it.
func WrapEntry(ctx context.Context, id string, key []byte, wrap interface {
	Wrap(ctx context.Context, key []byte) ([]byte, error)
}) (string, error) {
	if _, err := NewKeys(id, map[string][]byte{id: key}); err != nil {
		return "", err
	}
	wrapped, err := wrap.Wrap(ctx, key)
	if err != nil {
		return "", err
	}
	return id + "=" + WrappedPrefix + base64.StdEncoding.EncodeToString(wrapped), nil
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

// KeyID returns the master key id a sealed value's envelope names, or false when sealed is not a v1
// envelope. It does not authenticate the value, so it only tells a re-seal job or a log line which key
// a value claims.
func KeyID(sealed []byte) (string, bool) {
	if len(sealed) < 2 || sealed[0] != envelopeV1 || len(sealed) < 2+int(sealed[1]) {
		return "", false
	}
	return string(sealed[2 : 2+int(sealed[1])]), true
}

func (k *Keys) Open(tenantID, field string, sealed []byte) ([]byte, error) {
	keyID, ok := KeyID(sealed)
	if !ok {
		return nil, ErrOpen
	}
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
