package transport

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // RSAES-OAEP default parameters (RFC 4055) use SHA-1; decrypt only
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"fmt"
)

// CMS EnvelopedData (RFC 5652 §6) for AS2 encryption (application/pkcs7-mime;
// smime-type=enveloped-data): one key-transport recipient with RSA, the
// content under AES-CBC. Encryption uses RSAES-PKCS1-v1_5 key transport,
// which every AS2 product reads; decryption also accepts RSAES-OAEP with
// default parameters. Only RSA recipients are supported.

var (
	oidEnvelopedData = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 3}
	oidRSAOAEP       = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 7}
	oidAES128CBC     = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 2}
	oidAES192CBC     = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 22}
	oidAES256CBC     = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 42}
)

// Encryption algorithms a partner profile may select.
const (
	EncryptNone   = "none"
	EncryptAES128 = "aes128-cbc"
	EncryptAES192 = "aes192-cbc"
	EncryptAES256 = "aes256-cbc"
)

var cipherOIDs = map[string]asn1.ObjectIdentifier{EncryptAES128: oidAES128CBC, EncryptAES192: oidAES192CBC, EncryptAES256: oidAES256CBC}

var cipherKeyLen = map[string]int{oidAES128CBC.String(): 16, oidAES192CBC.String(): 24, oidAES256CBC.String(): 32}

// ErrDecrypt marks an enveloped message we cannot open: not for our
// certificate, a wrong key, or a malformed or tampered envelope.
var ErrDecrypt = errors.New("transport: decryption failed")

type envelopedData struct {
	Version              int
	OriginatorInfo       asn1.RawValue   `asn1:"optional,tag:0"`
	RecipientInfos       []asn1.RawValue `asn1:"set"`
	EncryptedContentInfo encryptedContentInfo
	UnprotectedAttrs     asn1.RawValue `asn1:"optional,tag:1"`
}

type encryptedContentInfo struct {
	ContentType                asn1.ObjectIdentifier
	ContentEncryptionAlgorithm pkix.AlgorithmIdentifier
	EncryptedContent           asn1.RawValue `asn1:"optional,tag:0"`
}

type keyTransRecipientInfo struct {
	Version                int
	RID                    asn1.RawValue
	KeyEncryptionAlgorithm pkix.AlgorithmIdentifier
	EncryptedKey           []byte
}

// Encrypt returns the DER CMS EnvelopedData of content for the recipient's RSA
// certificate under the named AES-CBC algorithm.
func Encrypt(content []byte, recipient *x509.Certificate, alg string) ([]byte, error) {
	oid, ok := cipherOIDs[alg]
	if !ok {
		return nil, fmt.Errorf("transport: encryption algorithm %q is not %s, %s or %s", alg, EncryptAES128, EncryptAES192, EncryptAES256)
	}
	if recipient == nil {
		return nil, errors.New("transport: encryption needs the partner certificate")
	}
	pub, ok := recipient.PublicKey.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("transport: the partner certificate must hold an RSA key to encrypt to it")
	}
	key := make([]byte, cipherKeyLen[oid.String()])
	iv := make([]byte, aes.BlockSize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if _, err := rand.Read(iv); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	pad := aes.BlockSize - len(content)%aes.BlockSize
	plain := append(append([]byte(nil), content...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	sealed := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(sealed, plain)
	wrapped, err := rsa.EncryptPKCS1v15(rand.Reader, pub, key)
	if err != nil {
		return nil, err
	}
	rid, err := asn1.Marshal(issuerAndSerial{Issuer: asn1.RawValue{FullBytes: recipient.RawIssuer}, Serial: recipient.SerialNumber})
	if err != nil {
		return nil, err
	}
	ktri, err := asn1.Marshal(keyTransRecipientInfo{Version: 0, RID: asn1.RawValue{FullBytes: rid},
		KeyEncryptionAlgorithm: pkix.AlgorithmIdentifier{Algorithm: oidRSA, Parameters: asn1.NullRawValue}, EncryptedKey: wrapped})
	if err != nil {
		return nil, err
	}
	ivDER, err := asn1.Marshal(iv)
	if err != nil {
		return nil, err
	}
	ed := envelopedData{
		Version:        0,
		RecipientInfos: []asn1.RawValue{{FullBytes: ktri}},
		EncryptedContentInfo: encryptedContentInfo{
			ContentType:                oidData,
			ContentEncryptionAlgorithm: pkix.AlgorithmIdentifier{Algorithm: oid, Parameters: asn1.RawValue{FullBytes: ivDER}},
			EncryptedContent:           asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, Bytes: sealed},
		},
	}
	inner, err := asn1.Marshal(ed)
	if err != nil {
		return nil, err
	}
	return asn1.Marshal(contentInfo{ContentType: oidEnvelopedData, Content: asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: inner}})
}

func decErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrDecrypt, fmt.Sprintf(format, args...))
}

// Decrypt opens a CMS EnvelopedData (DER or BER) addressed to cert with its
// RSA key and returns the content.
func Decrypt(enveloped []byte, cert *x509.Certificate, key *rsa.PrivateKey) ([]byte, error) {
	if cert == nil || key == nil {
		return nil, decErr("no decryption key configured")
	}
	der, err := berToDER(enveloped)
	if err != nil {
		return nil, decErr("%v", err)
	}
	var ci contentInfo
	if rest, err := asn1.Unmarshal(der, &ci); err != nil || len(rest) != 0 {
		return nil, decErr("not a CMS ContentInfo")
	}
	if !ci.ContentType.Equal(oidEnvelopedData) {
		return nil, decErr("content type %s is not envelopedData", ci.ContentType)
	}
	var ed envelopedData
	if _, err := asn1.Unmarshal(ci.Content.Bytes, &ed); err != nil {
		return nil, decErr("envelopedData: %v", err)
	}
	eci := ed.EncryptedContentInfo
	keyLen, ok := cipherKeyLen[eci.ContentEncryptionAlgorithm.Algorithm.String()]
	if !ok {
		return nil, decErr("content encryption %s is not AES-CBC", eci.ContentEncryptionAlgorithm.Algorithm)
	}
	var iv []byte
	if _, err := asn1.Unmarshal(eci.ContentEncryptionAlgorithm.Parameters.FullBytes, &iv); err != nil || len(iv) != aes.BlockSize {
		return nil, decErr("AES-CBC parameters are not a 16-byte IV")
	}
	sealed := eci.EncryptedContent.Bytes
	if eci.EncryptedContent.IsCompound {
		if sealed, err = concatOctets(sealed); err != nil {
			return nil, decErr("encrypted content: %v", err)
		}
	}
	if len(sealed) == 0 || len(sealed)%aes.BlockSize != 0 {
		return nil, decErr("encrypted content is not whole AES blocks")
	}
	var cek []byte
	for _, raw := range ed.RecipientInfos {
		var ktri keyTransRecipientInfo
		if _, err := asn1.Unmarshal(raw.FullBytes, &ktri); err != nil {
			continue // not a key-transport recipient
		}
		if !ridMatches(ktri.RID, cert) {
			continue
		}
		switch {
		case ktri.KeyEncryptionAlgorithm.Algorithm.Equal(oidRSA):
			// A random key on padding failure keeps the PKCS #1 v1.5 path
			// constant-time (Bleichenbacher); the content then fails to open.
			cek = make([]byte, keyLen)
			if _, err := rand.Read(cek); err != nil {
				return nil, err
			}
			if err := rsa.DecryptPKCS1v15SessionKey(nil, key, ktri.EncryptedKey, cek); err != nil {
				return nil, decErr("key transport: %v", err)
			}
		case ktri.KeyEncryptionAlgorithm.Algorithm.Equal(oidRSAOAEP):
			if p := ktri.KeyEncryptionAlgorithm.Parameters; len(p.Bytes) != 0 {
				return nil, decErr("RSAES-OAEP with non-default parameters is not supported")
			}
			if cek, err = rsa.DecryptOAEP(sha1.New(), nil, key, ktri.EncryptedKey, nil); err != nil {
				return nil, decErr("key transport: %v", err)
			}
		default:
			return nil, decErr("key encryption %s is not RSA", ktri.KeyEncryptionAlgorithm.Algorithm)
		}
		break
	}
	if cek == nil {
		return nil, decErr("no recipient matches our certificate")
	}
	if len(cek) != keyLen {
		return nil, decErr("content key has the wrong length")
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, decErr("%v", err)
	}
	plain := make([]byte, len(sealed))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, sealed)
	pad := int(plain[len(plain)-1])
	if pad == 0 || pad > aes.BlockSize || pad > len(plain) || !bytes.Equal(plain[len(plain)-pad:], bytes.Repeat([]byte{byte(pad)}, pad)) {
		return nil, decErr("bad padding")
	}
	return plain[:len(plain)-pad], nil
}

// ridMatches compares a RecipientIdentifier (issuer and serial, or a [0]
// subject key identifier) with cert.
func ridMatches(rid asn1.RawValue, cert *x509.Certificate) bool {
	if rid.Class == asn1.ClassContextSpecific && rid.Tag == 0 {
		return len(cert.SubjectKeyId) > 0 && bytes.Equal(rid.Bytes, cert.SubjectKeyId)
	}
	var ias issuerAndSerial
	if _, err := asn1.Unmarshal(rid.FullBytes, &ias); err != nil {
		return false
	}
	return bytes.Equal(ias.Issuer.FullBytes, cert.RawIssuer) && ias.Serial.Cmp(cert.SerialNumber) == 0
}
