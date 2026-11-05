package transport

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"time"

	_ "crypto/sha256"
	_ "crypto/sha512"
)

// A minimal CMS (RFC 5652) SignedData implementation for detached S/MIME
// signatures (application/pkcs7-signature), as AS2 (RFC 4130) uses them for
// messages and MDNs. Only what AS2 needs: one signer, signed attributes
// (content type, signing time, message digest), RSA PKCS #1 v1.5 or ECDSA.
// Built on encoding/asn1 so the service takes no new dependency.

var (
	oidData          = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}
	oidSignedData    = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
	oidContentType   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 3}
	oidMessageDigest = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 4}
	oidSigningTime   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 5}
	oidRSA           = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 1}
	oidECDSASHA256   = asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 3, 2}
	oidSHA256        = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}
	oidSHA384        = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 2}
	oidSHA512        = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 3}
)

// ErrSignature marks a signature that is malformed, does not match the
// content or was not made by the trusted certificate.
var ErrSignature = errors.New("transport: signature verification failed")

func sigErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrSignature, fmt.Sprintf(format, args...))
}

var digestByOID = map[string]crypto.Hash{oidSHA256.String(): crypto.SHA256, oidSHA384.String(): crypto.SHA384, oidSHA512.String(): crypto.SHA512}

// micalgOf is the multipart/signed micalg and the MDN MIC name of a digest.
var micalgOf = map[crypto.Hash]string{crypto.SHA256: "sha-256", crypto.SHA384: "sha-384", crypto.SHA512: "sha-512"}

type contentInfo struct {
	ContentType asn1.ObjectIdentifier
	Content     asn1.RawValue `asn1:"explicit,tag:0"`
}

type encapContentInfo struct {
	EContentType asn1.ObjectIdentifier
	EContent     asn1.RawValue `asn1:"optional,explicit,tag:0"`
}

type issuerAndSerial struct {
	Issuer asn1.RawValue
	Serial *big.Int
}

type signerInfo struct {
	Version            int
	SID                asn1.RawValue
	DigestAlgorithm    pkix.AlgorithmIdentifier
	SignedAttrs        asn1.RawValue `asn1:"optional,tag:0"`
	SignatureAlgorithm pkix.AlgorithmIdentifier
	Signature          []byte
	UnsignedAttrs      asn1.RawValue `asn1:"optional,tag:1"`
}

type signedData struct {
	Version          int
	DigestAlgorithms []pkix.AlgorithmIdentifier `asn1:"set"`
	EncapContentInfo encapContentInfo
	Certificates     asn1.RawValue `asn1:"optional,tag:0"`
	CRLs             asn1.RawValue `asn1:"optional,tag:1"`
	SignerInfos      []signerInfo  `asn1:"set"`
}

type attribute struct {
	Type   asn1.ObjectIdentifier
	Values asn1.RawValue
}

func attr(oid asn1.ObjectIdentifier, value any) ([]byte, error) {
	v, err := asn1.Marshal(value)
	if err != nil {
		return nil, err
	}
	return asn1.Marshal(attribute{Type: oid, Values: asn1.RawValue{Class: asn1.ClassUniversal, Tag: asn1.TagSet, IsCompound: true, Bytes: v}})
}

// setOf wraps DER encodings as a SET OF, sorted as DER requires.
func setOf(class, tag int, elems [][]byte) asn1.RawValue {
	sorted := append([][]byte(nil), elems...)
	sort.Slice(sorted, func(i, j int) bool { return bytes.Compare(sorted[i], sorted[j]) < 0 })
	return asn1.RawValue{Class: class, Tag: tag, IsCompound: true, Bytes: bytes.Join(sorted, nil)}
}

// SignDetached returns the DER CMS SignedData over content with SHA-256,
// without the content (detached) and with the signer certificate included.
func SignDetached(content []byte, cert *x509.Certificate, key crypto.Signer, at time.Time) ([]byte, error) {
	if cert == nil || key == nil {
		return nil, errors.New("transport: signing needs a certificate and key")
	}
	var sigAlg asn1.ObjectIdentifier
	switch key.Public().(type) {
	case *rsa.PublicKey:
		sigAlg = oidRSA
	case *ecdsa.PublicKey:
		sigAlg = oidECDSASHA256
	default:
		return nil, errors.New("transport: signing key must be RSA or ECDSA")
	}
	digest := crypto.SHA256.New()
	digest.Write(content)
	ct, err := attr(oidContentType, oidData)
	if err != nil {
		return nil, err
	}
	st, err := attr(oidSigningTime, at.UTC().Truncate(time.Second))
	if err != nil {
		return nil, err
	}
	md, err := attr(oidMessageDigest, digest.Sum(nil))
	if err != nil {
		return nil, err
	}
	attrs := setOf(asn1.ClassUniversal, asn1.TagSet, [][]byte{ct, st, md})
	toSign, err := asn1.Marshal(attrs)
	if err != nil {
		return nil, err
	}
	h := crypto.SHA256.New()
	h.Write(toSign)
	signature, err := key.Sign(rand.Reader, h.Sum(nil), crypto.SHA256)
	if err != nil {
		return nil, err
	}
	sid, err := asn1.Marshal(issuerAndSerial{Issuer: asn1.RawValue{FullBytes: cert.RawIssuer}, Serial: cert.SerialNumber})
	if err != nil {
		return nil, err
	}
	sha256Alg := pkix.AlgorithmIdentifier{Algorithm: oidSHA256, Parameters: asn1.NullRawValue}
	algParams := asn1.NullRawValue
	if sigAlg.Equal(oidECDSASHA256) {
		algParams = asn1.RawValue{}
	}
	sd := signedData{
		Version:          1,
		DigestAlgorithms: []pkix.AlgorithmIdentifier{sha256Alg},
		EncapContentInfo: encapContentInfo{EContentType: oidData},
		Certificates:     asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: cert.Raw},
		SignerInfos: []signerInfo{{
			Version: 1, SID: asn1.RawValue{FullBytes: sid}, DigestAlgorithm: sha256Alg,
			SignedAttrs:        asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: attrs.Bytes},
			SignatureAlgorithm: pkix.AlgorithmIdentifier{Algorithm: sigAlg, Parameters: algParams},
			Signature:          signature,
		}},
	}
	inner, err := asn1.Marshal(sd)
	if err != nil {
		return nil, err
	}
	return asn1.Marshal(contentInfo{ContentType: oidSignedData, Content: asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: inner}})
}

// VerifyDetached checks a DER (or BER-free) CMS SignedData over content
// against the trusted certificate: the signer must be that certificate (by
// issuer and serial), the signed message digest must equal the content's and
// the signature over the signed attributes must verify with its key. The
// certificates carried in the signature are never trusted on their own. It
// returns the digest algorithm, which the AS2 MIC must use.
func VerifyDetached(sig, content []byte, trusted *x509.Certificate) (crypto.Hash, error) {
	if trusted == nil {
		return 0, sigErr("no trusted certificate for the partner")
	}
	var ci contentInfo
	if rest, err := asn1.Unmarshal(sig, &ci); err != nil || len(rest) != 0 {
		return 0, sigErr("not a CMS ContentInfo: %v", err)
	}
	if !ci.ContentType.Equal(oidSignedData) {
		return 0, sigErr("content type %s is not signedData", ci.ContentType)
	}
	var sd signedData
	if _, err := asn1.Unmarshal(ci.Content.Bytes, &sd); err != nil {
		return 0, sigErr("signedData: %v", err)
	}
	if len(sd.EncapContentInfo.EContent.Bytes) != 0 {
		return 0, sigErr("signature is not detached")
	}
	for _, si := range sd.SignerInfos {
		var ias issuerAndSerial
		if _, err := asn1.Unmarshal(si.SID.FullBytes, &ias); err != nil {
			continue
		}
		if !bytes.Equal(ias.Issuer.FullBytes, trusted.RawIssuer) || ias.Serial.Cmp(trusted.SerialNumber) != 0 {
			continue
		}
		hash, ok := digestByOID[si.DigestAlgorithm.Algorithm.String()]
		if !ok {
			return 0, sigErr("digest algorithm %s is not supported", si.DigestAlgorithm.Algorithm)
		}
		if len(si.SignedAttrs.Bytes) == 0 {
			return 0, sigErr("signer info has no signed attributes")
		}
		var digest, contentType []byte
		rest := si.SignedAttrs.Bytes
		for len(rest) > 0 {
			var a attribute
			var err error
			if rest, err = asn1.Unmarshal(rest, &a); err != nil {
				return 0, sigErr("signed attribute: %v", err)
			}
			switch {
			case a.Type.Equal(oidMessageDigest):
				if _, err := asn1.Unmarshal(a.Values.Bytes, &digest); err != nil {
					return 0, sigErr("message digest: %v", err)
				}
			case a.Type.Equal(oidContentType):
				contentType = a.Values.Bytes
			}
		}
		want, _ := asn1.Marshal(oidData)
		if !bytes.Equal(contentType, want) {
			return 0, sigErr("signed content type is not id-data")
		}
		h := hash.New()
		h.Write(content)
		if !bytes.Equal(digest, h.Sum(nil)) {
			return 0, sigErr("message digest does not match the content")
		}
		signed, err := asn1.Marshal(asn1.RawValue{Class: asn1.ClassUniversal, Tag: asn1.TagSet, IsCompound: true, Bytes: si.SignedAttrs.Bytes})
		if err != nil {
			return 0, err
		}
		alg, err := x509Algorithm(trusted.PublicKeyAlgorithm, hash)
		if err != nil {
			return 0, err
		}
		if err := trusted.CheckSignature(alg, signed, si.Signature); err != nil {
			return 0, sigErr("%v", err)
		}
		return hash, nil
	}
	return 0, sigErr("no signer matches the partner certificate")
}

func x509Algorithm(pk x509.PublicKeyAlgorithm, h crypto.Hash) (x509.SignatureAlgorithm, error) {
	table := map[x509.PublicKeyAlgorithm]map[crypto.Hash]x509.SignatureAlgorithm{
		x509.RSA:   {crypto.SHA256: x509.SHA256WithRSA, crypto.SHA384: x509.SHA384WithRSA, crypto.SHA512: x509.SHA512WithRSA},
		x509.ECDSA: {crypto.SHA256: x509.ECDSAWithSHA256, crypto.SHA384: x509.ECDSAWithSHA384, crypto.SHA512: x509.ECDSAWithSHA512},
	}
	if a, ok := table[pk][h]; ok {
		return a, nil
	}
	return 0, sigErr("public key algorithm %v with %v is not supported", pk, h)
}
