package ebics

import (
	"bytes"
	"compress/zlib"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math/big"
)

const (
	aesKeyLen        = 16
	maxOrderDataSize = 50 << 20
)

// KeyDigest is the EBICS hash of an RSA public key: SHA-256 over the lowercase
// hexadecimal exponent, one space, and the lowercase hexadecimal modulus, both
// without leading zeros. Banks print it on the INI and HIA letters and in
// BankPubKeyDigests.
func KeyDigest(pub *rsa.PublicKey) []byte {
	sum := sha256.Sum256([]byte(big.NewInt(int64(pub.E)).Text(16) + " " + pub.N.Text(16)))
	return sum[:]
}

func compress(data []byte) ([]byte, error) {
	var b bytes.Buffer
	w := zlib.NewWriter(&b)
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func decompress(data []byte) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("order data is not zlib: %w", err)
	}
	defer r.Close()
	out, err := io.ReadAll(io.LimitReader(r, maxOrderDataSize+1))
	if err != nil {
		return nil, fmt.Errorf("order data is not zlib: %w", err)
	}
	if len(out) > maxOrderDataSize {
		return nil, fmt.Errorf("order data exceeds %d bytes", maxOrderDataSize)
	}
	return out, nil
}

// padX923 pads to the AES block with zero bytes and a final byte holding the
// pad length (ANSI X9.23, the padding E002 names). Unpadding reads only the
// final byte, so ISO 10126 padding from a bank is accepted too.
func padX923(data []byte) []byte {
	n := aes.BlockSize - len(data)%aes.BlockSize
	out := make([]byte, len(data)+n)
	copy(out, data)
	out[len(out)-1] = byte(n)
	return out
}

func unpad(data []byte) ([]byte, error) {
	if len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil, errors.New("ciphertext is not a whole number of AES blocks")
	}
	n := int(data[len(data)-1])
	if n < 1 || n > aes.BlockSize {
		return nil, errors.New("invalid padding")
	}
	return data[:len(data)-n], nil
}

func aesCBC(key, data []byte, encrypt bool) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	iv := make([]byte, aes.BlockSize)
	if encrypt {
		in := padX923(data)
		out := make([]byte, len(in))
		cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, in)
		return out, nil
	}
	if len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil, errors.New("ciphertext is not a whole number of AES blocks")
	}
	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, data)
	return unpad(out)
}

// encryptOrderData compresses data and encrypts it with a fresh AES-128 key
// (CBC, zero IV, as E002 prescribes), and wraps that key for the recipient with
// RSA PKCS#1 v1.5. The same key later encrypts the signature data.
func encryptOrderData(data []byte, recipient *rsa.PublicKey) (ciphertext, transactionKey, plainKey []byte, err error) {
	plainKey = make([]byte, aesKeyLen)
	if _, err = rand.Read(plainKey); err != nil {
		return nil, nil, nil, err
	}
	ciphertext, err = sealWithKey(plainKey, data)
	if err != nil {
		return nil, nil, nil, err
	}
	transactionKey, err = rsa.EncryptPKCS1v15(rand.Reader, recipient, plainKey)
	return ciphertext, transactionKey, plainKey, err
}

func sealWithKey(key, data []byte) ([]byte, error) {
	packed, err := compress(data)
	if err != nil {
		return nil, err
	}
	return aesCBC(key, packed, true)
}

func openWithKey(key, ciphertext []byte) ([]byte, error) {
	if len(key) != aesKeyLen {
		return nil, fmt.Errorf("transaction key is %d bytes, want %d", len(key), aesKeyLen)
	}
	packed, err := aesCBC(key, ciphertext, false)
	if err != nil {
		return nil, err
	}
	return decompress(packed)
}

// segments splits the base64 text of the encrypted order data into pieces of at
// most size characters; an empty payload still yields one segment.
func segments(encoded string, size int) []string {
	if size < 1 {
		size = 1
	}
	if encoded == "" {
		return []string{""}
	}
	var out []string
	for len(encoded) > size {
		out = append(out, encoded[:size])
		encoded = encoded[size:]
	}
	return append(out, encoded)
}

// orderDigest is the SHA-256 the A006 signature covers: the order data with
// CR, LF and Ctrl-Z removed (EBICS 2.5 section 14, signature of the order data;
// the open-source ebics-client-php strips CR and LF from XML orders before it
// hashes them the same way). The data that is uploaded keeps its line breaks.
func orderDigest(data []byte) []byte {
	kept := make([]byte, 0, len(data))
	for _, b := range data {
		if b != '\r' && b != '\n' && b != 0x1A {
			kept = append(kept, b)
		}
	}
	return sha256Sum(kept)
}

func sha256Sum(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}
