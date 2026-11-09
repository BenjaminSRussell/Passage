package bankconnect

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

var vaultKVPath = regexp.MustCompile(`^[A-Za-z0-9_-]+(/[A-Za-z0-9_-]+)*$`)

const (
	ebicsUsageA006 = "A006"
	ebicsUsageX002 = "X002"
	ebicsUsageE002 = "E002"
	minEBICSBits   = 2048
)

// EBICSKeyRefs names where a subscriber's three keys live. A006 and X002 are
// non-exportable transit keys (RSA), so signing happens inside Vault. E002 is
// a KV v2 secret holding the PEM private key: E002 wraps the transaction key
// with RSA PKCS#1 v1.5, which the transit engine cannot decrypt (it offers only
// OAEP), so the key is read into memory for each decryption and not kept.
type EBICSKeyRefs struct {
	A006, X002  string
	E002KVMount string
	E002KVPath  string
}

// VaultKeys is the Vault-backed EBICS key custody. It satisfies ebics.Keys; no
// method returns private key material.
type VaultKeys struct {
	vault *Vault
	refs  EBICSKeyRefs
}

// EBICSKeys returns the key custody for one subscriber. The transit key names
// must be on the signing allow-list and distinct, and the KV location must be
// well formed; none may be the SFTP login key.
func (v *Vault) EBICSKeys(r EBICSKeyRefs) (*VaultKeys, error) {
	if !v.cfg.Enabled() {
		return nil, errors.New("vault is not configured")
	}
	allowed := map[string]bool{}
	for _, n := range v.cfg.SigningKeyNames {
		allowed[n] = true
	}
	for _, n := range []string{r.A006, r.X002} {
		if !vaultName.MatchString(n) || !allowed[n] {
			return nil, fmt.Errorf("ebics key %q is not an allowed signing key", n)
		}
	}
	if r.A006 == r.X002 {
		return nil, errors.New("ebics A006 and X002 must be different keys")
	}
	if !vaultName.MatchString(r.E002KVMount) || !vaultKVPath.MatchString(r.E002KVPath) {
		return nil, errors.New("ebics E002 secret location is malformed")
	}
	return &VaultKeys{vault: v, refs: r}, nil
}

func (k *VaultKeys) token(ctx context.Context) (string, error) { return k.vault.Token(ctx) }

func (k *VaultKeys) sign(ctx context.Context, name string, input []byte, algorithm string) ([]byte, error) {
	tok, err := k.token(ctx)
	if err != nil {
		return nil, err
	}
	req := map[string]string{"input": base64.StdEncoding.EncodeToString(input), "signature_algorithm": algorithm}
	if algorithm == "pss" {
		req["salt_length"] = "hash"
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	var out struct {
		Data struct {
			Signature string `json:"signature"`
		} `json:"data"`
	}
	if err := k.vault.cfg.call(ctx, http.MethodPost, "/v1/transit/sign/"+name+"/sha2-256", tok, body, &out); err != nil {
		k.vault.dropOnForbidden(err)
		return nil, fmt.Errorf("vault sign: %w", err)
	}
	return parseVaultSignature(out.Data.Signature)
}

// SignA006 signs the SHA-256 digest of an order with RSASSA-PSS; Vault hashes
// the digest once more, which is what A006 prescribes.
func (k *VaultKeys) SignA006(ctx context.Context, orderHash []byte) ([]byte, error) {
	return k.sign(ctx, k.refs.A006, orderHash, "pss")
}

// SignX002 signs the canonical SignedInfo with RSASSA-PKCS1-v1_5 and SHA-256.
func (k *VaultKeys) SignX002(ctx context.Context, signedInfo []byte) ([]byte, error) {
	return k.sign(ctx, k.refs.X002, signedInfo, "pkcs1v15")
}

func (k *VaultKeys) Public(ctx context.Context, usage string) (*rsa.PublicKey, error) {
	switch usage {
	case ebicsUsageA006:
		return k.transitPublic(ctx, k.refs.A006)
	case ebicsUsageX002:
		return k.transitPublic(ctx, k.refs.X002)
	case ebicsUsageE002:
		priv, err := k.e002(ctx)
		if err != nil {
			return nil, err
		}
		return &priv.PublicKey, nil
	}
	return nil, fmt.Errorf("unknown ebics key usage %q", usage)
}

func (k *VaultKeys) transitPublic(ctx context.Context, name string) (*rsa.PublicKey, error) {
	tok, err := k.token(ctx)
	if err != nil {
		return nil, err
	}
	var out struct {
		Data struct {
			Type                 string `json:"type"`
			Exportable           bool   `json:"exportable"`
			AllowPlaintextBackup bool   `json:"allow_plaintext_backup"`
			LatestVersion        int    `json:"latest_version"`
			Keys                 map[string]struct {
				PublicKey string `json:"public_key"`
			} `json:"keys"`
		} `json:"data"`
	}
	if err := k.vault.cfg.call(ctx, http.MethodGet, "/v1/transit/keys/"+name, tok, nil, &out); err != nil {
		k.vault.dropOnForbidden(err)
		return nil, fmt.Errorf("vault read ebics public key: %w", err)
	}
	if out.Data.Exportable || out.Data.AllowPlaintextBackup {
		return nil, errors.New("ebics transit key must not be exportable or allow plaintext backup")
	}
	if !strings.HasPrefix(out.Data.Type, "rsa-") {
		return nil, fmt.Errorf("ebics transit key type %q is not RSA", out.Data.Type)
	}
	block, _ := pem.Decode([]byte(out.Data.Keys[strconv.Itoa(out.Data.LatestVersion)].PublicKey))
	if block == nil {
		return nil, errors.New("vault read ebics public key: malformed key")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	pub, ok := parsed.(*rsa.PublicKey)
	if err != nil || !ok || pub.N.BitLen() < minEBICSBits {
		return nil, errors.New("vault read ebics public key: not an RSA key of 2048 bits or more")
	}
	return pub, nil
}

// DecryptE002 unwraps an E002 transaction key. A wrapped key that fails PKCS#1
// padding yields a random key rather than an error, so the failure shows only
// as undecryptable data and the key store is no padding oracle.
func (k *VaultKeys) DecryptE002(ctx context.Context, wrapped []byte) ([]byte, error) {
	priv, err := k.e002(ctx)
	if err != nil {
		return nil, err
	}
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := rsa.DecryptPKCS1v15SessionKey(rand.Reader, priv, wrapped, key); err != nil {
		return nil, fmt.Errorf("unwrap transaction key: %w", err)
	}
	return key, nil
}

func (k *VaultKeys) e002(ctx context.Context) (*rsa.PrivateKey, error) {
	tok, err := k.token(ctx)
	if err != nil {
		return nil, err
	}
	var out struct {
		Data struct {
			Data struct {
				PEM string `json:"pem"`
			} `json:"data"`
		} `json:"data"`
	}
	if err := k.vault.cfg.call(ctx, http.MethodGet, "/v1/"+k.refs.E002KVMount+"/data/"+k.refs.E002KVPath, tok, nil, &out); err != nil {
		k.vault.dropOnForbidden(err)
		return nil, fmt.Errorf("vault read ebics E002 key: %w", err)
	}
	block, _ := pem.Decode([]byte(out.Data.Data.PEM))
	if block == nil {
		return nil, errors.New("vault read ebics E002 key: malformed PEM")
	}
	var priv *rsa.PrivateKey
	if parsed, perr := x509.ParsePKCS8PrivateKey(block.Bytes); perr == nil {
		priv, _ = parsed.(*rsa.PrivateKey)
	} else {
		priv, _ = x509.ParsePKCS1PrivateKey(block.Bytes)
	}
	if priv == nil || priv.N.BitLen() < minEBICSBits {
		return nil, errors.New("vault read ebics E002 key: not an RSA key of 2048 bits or more")
	}
	return priv, nil
}
