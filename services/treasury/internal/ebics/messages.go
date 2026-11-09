package ebics

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"
)

const (
	nsH004        = "urn:org:ebics:H004"
	nsDS          = "http://www.w3.org/2000/09/xmldsig#"
	nsS001        = "http://www.ebics.org/S001"
	algC14N       = "http://www.w3.org/TR/2001/REC-xml-c14n-20010315"
	algRSASHA256  = "http://www.w3.org/2001/04/xmldsig-more#rsa-sha256"
	algSHA256     = "http://www.w3.org/2001/04/xmlenc#sha256"
	authReference = "#xpointer(//*[@authenticate='true'])"
	xmlDecl       = `<?xml version="1.0" encoding="UTF-8"?>`
	authMarker    = "<!--AUTH-->"
	digestHole    = "@DIGEST@"
	sigHole       = "@SIG@"
	rootAttrs     = ` xmlns="` + nsH004 + `" xmlns:ds="` + nsDS + `" Version="H004" Revision="1"`
)

func esc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func nonce() (string, error) {
	n := make([]byte, 16)
	if _, err := rand.Read(n); err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(n)), nil
}

func timestamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

const authBlock = `<AuthSignature><ds:SignedInfo><ds:CanonicalizationMethod Algorithm="` + algC14N + `"></ds:CanonicalizationMethod>` +
	`<ds:SignatureMethod Algorithm="` + algRSASHA256 + `"></ds:SignatureMethod>` +
	`<ds:Reference URI="` + authReference + `"><ds:Transforms><ds:Transform Algorithm="` + algC14N + `"></ds:Transform></ds:Transforms>` +
	`<ds:DigestMethod Algorithm="` + algSHA256 + `"></ds:DigestMethod><ds:DigestValue>` + digestHole + `</ds:DigestValue></ds:Reference></ds:SignedInfo>` +
	`<ds:SignatureValue>` + sigHole + `</ds:SignatureValue></AuthSignature>`

// signRequest replaces the AuthSignature marker in an EBICS request with an X002
// authentication signature: the digest covers every authenticate="true"
// element in canonical form, and the key store signs the canonical SignedInfo.
func signRequest(ctx context.Context, keys Keys, doc string) ([]byte, error) {
	if !strings.Contains(doc, authMarker) {
		return nil, errors.New("request has no signature position")
	}
	doc = strings.Replace(doc, authMarker, authBlock, 1)
	root, err := parseXML([]byte(doc))
	if err != nil {
		return nil, err
	}
	covered := authenticated(root)
	if len(covered) == 0 {
		return nil, errors.New("request has no authenticate element to sign")
	}
	digest := sha256.Sum256(canonicalNodes(covered))
	doc = strings.Replace(doc, digestHole, b64(digest[:]), 1)
	if root, err = parseXML([]byte(doc)); err != nil {
		return nil, err
	}
	signedInfo := root.child("AuthSignature").child("SignedInfo")
	sig, err := keys.SignX002(ctx, canonicalNodes([]*xnode{signedInfo}))
	if err != nil {
		return nil, fmt.Errorf("sign request (X002): %w", err)
	}
	return []byte(strings.Replace(doc, sigHole, b64(sig), 1)), nil
}

var errAuthSignature = errors.New("ebics: authentication signature is invalid")

// verifyAuthSignature checks a message's AuthSignature against the sender's X002
// key. Only the exact algorithms and reference EBICS defines are accepted, and
// at least one element must be covered, so a signature over nothing never passes.
func verifyAuthSignature(root *xnode, pub *rsa.PublicKey) error {
	sig := root.child("AuthSignature")
	si := sig.child("SignedInfo")
	ref := si.child("Reference")
	if si == nil || ref == nil {
		return fmt.Errorf("%w: missing", errAuthSignature)
	}
	if si.child("CanonicalizationMethod").attr("Algorithm") != algC14N ||
		si.child("SignatureMethod").attr("Algorithm") != algRSASHA256 ||
		ref.attr("URI") != authReference ||
		ref.child("DigestMethod").attr("Algorithm") != algSHA256 {
		return fmt.Errorf("%w: unsupported algorithm", errAuthSignature)
	}
	covered := authenticated(root)
	if len(covered) == 0 {
		return fmt.Errorf("%w: nothing is authenticated", errAuthSignature)
	}
	want := sha256.Sum256(canonicalNodes(covered))
	got, err := base64.StdEncoding.DecodeString(ref.child("DigestValue").content())
	if err != nil || !bytes.Equal(got, want[:]) {
		return fmt.Errorf("%w: digest mismatch", errAuthSignature)
	}
	value, err := base64.StdEncoding.DecodeString(sig.child("SignatureValue").content())
	if err != nil {
		return fmt.Errorf("%w: signature value", errAuthSignature)
	}
	h := sha256.Sum256(canonicalNodes([]*xnode{si}))
	if rsa.VerifyPKCS1v15(pub, crypto.SHA256, h[:], value) != nil {
		return fmt.Errorf("%w: signature mismatch", errAuthSignature)
	}
	return nil
}

func rsaKeyValue(pub *rsa.PublicKey) string {
	return `<ds:RSAKeyValue><ds:Modulus>` + b64(pub.N.Bytes()) + `</ds:Modulus><ds:Exponent>` +
		b64(big.NewInt(int64(pub.E)).Bytes()) + `</ds:Exponent></ds:RSAKeyValue>`
}

func parseRSAKeyValue(info *xnode) (*rsa.PublicKey, error) {
	kv := info.path("PubKeyValue", "RSAKeyValue")
	n, err1 := base64.StdEncoding.DecodeString(kv.child("Modulus").content())
	e, err2 := base64.StdEncoding.DecodeString(kv.child("Exponent").content())
	if kv == nil || err1 != nil || err2 != nil || len(n) == 0 || len(e) == 0 || len(e) > 4 {
		return nil, errors.New("malformed RSA key value")
	}
	pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	if pub.N.BitLen() < 2048 || pub.N.BitLen() > 4096 || pub.E < 3 || pub.E%2 == 0 {
		return nil, fmt.Errorf("RSA key of %d bits with exponent %d is not acceptable", pub.N.BitLen(), pub.E)
	}
	return pub, nil
}

func unsecuredRequest(sub Subscriber, orderType, attribute, orderData string) string {
	return xmlDecl + `<ebicsUnsecuredRequest xmlns="` + nsH004 + `" xmlns:ds="` + nsDS + `" Version="H004" Revision="1">` +
		`<header authenticate="true"><static><HostID>` + esc(sub.HostID) + `</HostID><PartnerID>` + esc(sub.PartnerID) + `</PartnerID>` +
		`<UserID>` + esc(sub.UserID) + `</UserID><OrderDetails><OrderType>` + orderType + `</OrderType><OrderAttribute>` + attribute + `</OrderAttribute></OrderDetails>` +
		`<SecurityMedium>0200</SecurityMedium></static><mutable></mutable></header>` +
		`<body><DataTransfer><OrderData>` + orderData + `</OrderData></DataTransfer></body></ebicsUnsecuredRequest>`
}

func iniOrderData(sub Subscriber, a006 *rsa.PublicKey, now time.Time) string {
	return `<?xml version="1.0" encoding="UTF-8"?><SignaturePubKeyOrderData xmlns="` + nsS001 + `" xmlns:ds="` + nsDS + `">` +
		`<SignaturePubKeyInfo><PubKeyValue>` + rsaKeyValue(a006) + `<TimeStamp>` + timestamp(now) + `</TimeStamp></PubKeyValue>` +
		`<SignatureVersion>A006</SignatureVersion></SignaturePubKeyInfo><PartnerID>` + esc(sub.PartnerID) + `</PartnerID><UserID>` + esc(sub.UserID) + `</UserID></SignaturePubKeyOrderData>`
}

func hiaOrderData(sub Subscriber, x002, e002 *rsa.PublicKey) string {
	return `<?xml version="1.0" encoding="UTF-8"?><HIARequestOrderData xmlns="` + nsH004 + `" xmlns:ds="` + nsDS + `">` +
		`<AuthenticationPubKeyInfo><PubKeyValue>` + rsaKeyValue(x002) + `</PubKeyValue><AuthenticationVersion>X002</AuthenticationVersion></AuthenticationPubKeyInfo>` +
		`<EncryptionPubKeyInfo><PubKeyValue>` + rsaKeyValue(e002) + `</PubKeyValue><EncryptionVersion>E002</EncryptionVersion></EncryptionPubKeyInfo>` +
		`<PartnerID>` + esc(sub.PartnerID) + `</PartnerID><UserID>` + esc(sub.UserID) + `</UserID></HIARequestOrderData>`
}

func hpbRequest(sub Subscriber, nonceHex string, now time.Time) string {
	return xmlDecl + `<ebicsNoPubKeyDigestsRequest` + rootAttrs + `><header authenticate="true"><static><HostID>` + esc(sub.HostID) + `</HostID>` +
		`<Nonce>` + nonceHex + `</Nonce><Timestamp>` + timestamp(now) + `</Timestamp><PartnerID>` + esc(sub.PartnerID) + `</PartnerID><UserID>` + esc(sub.UserID) + `</UserID>` +
		`<OrderDetails><OrderType>HPB</OrderType><OrderAttribute>DZHNN</OrderAttribute></OrderDetails><SecurityMedium>0000</SecurityMedium></static><mutable></mutable></header>` +
		authMarker + `<body></body></ebicsNoPubKeyDigestsRequest>`
}

func userSignatureData(sub Subscriber, a006 []byte) string {
	return `<?xml version="1.0" encoding="UTF-8"?><UserSignatureData xmlns="` + nsS001 + `"><OrderSignatureData><SignatureVersion>A006</SignatureVersion>` +
		`<SignatureValue>` + b64(a006) + `</SignatureValue><PartnerID>` + esc(sub.PartnerID) + `</PartnerID><UserID>` + esc(sub.UserID) + `</UserID></OrderSignatureData></UserSignatureData>`
}

type orderSpec struct {
	Type      string
	Attribute string
	From, To  string
	Segments  int
}

func (o orderSpec) params() string {
	if o.From == "" {
		return "<StandardOrderParams></StandardOrderParams>"
	}
	return "<StandardOrderParams><DateRange><Start>" + esc(o.From) + "</Start><End>" + esc(o.To) + "</End></DateRange></StandardOrderParams>"
}

func digests(x002, e002 *rsa.PublicKey) string {
	return `<BankPubKeyDigests><Authentication Version="X002" Algorithm="` + algSHA256 + `">` + b64(KeyDigest(x002)) + `</Authentication>` +
		`<Encryption Version="E002" Algorithm="` + algSHA256 + `">` + b64(KeyDigest(e002)) + `</Encryption></BankPubKeyDigests>`
}

func initRequest(sub Subscriber, o orderSpec, nonceHex string, now time.Time, bankX002, bankE002 *rsa.PublicKey, body string) string {
	segs := ""
	if o.Segments > 0 {
		segs = "<NumSegments>" + strconv.Itoa(o.Segments) + "</NumSegments>"
	}
	return xmlDecl + `<ebicsRequest` + rootAttrs + `><header authenticate="true"><static><HostID>` + esc(sub.HostID) + `</HostID>` +
		`<Nonce>` + nonceHex + `</Nonce><Timestamp>` + timestamp(now) + `</Timestamp><PartnerID>` + esc(sub.PartnerID) + `</PartnerID><UserID>` + esc(sub.UserID) + `</UserID>` +
		`<Product Language="en">Passage Treasury</Product><OrderDetails><OrderType>` + o.Type + `</OrderType><OrderAttribute>` + o.Attribute + `</OrderAttribute>` + o.params() + `</OrderDetails>` +
		digests(bankX002, bankE002) + `<SecurityMedium>0000</SecurityMedium>` + segs + `</static><mutable><TransactionPhase>Initialisation</TransactionPhase></mutable></header>` +
		authMarker + `<body>` + body + `</body></ebicsRequest>`
}

func uploadInitBody(bankE002 *rsa.PublicKey, transactionKey, signatureData []byte) string {
	return `<DataTransfer><DataEncryptionInfo authenticate="true"><EncryptionPubKeyDigest Version="E002" Algorithm="` + algSHA256 + `">` + b64(KeyDigest(bankE002)) + `</EncryptionPubKeyDigest>` +
		`<TransactionKey>` + b64(transactionKey) + `</TransactionKey></DataEncryptionInfo><SignatureData authenticate="true">` + b64(signatureData) + `</SignatureData></DataTransfer>`
}

func phaseRequest(sub Subscriber, txID, phase string, segment int, last bool, body string) string {
	seg := ""
	if segment > 0 {
		attr := ""
		if last {
			attr = ` lastSegment="true"`
		}
		seg = `<SegmentNumber` + attr + `>` + strconv.Itoa(segment) + `</SegmentNumber>`
	}
	return xmlDecl + `<ebicsRequest` + rootAttrs + `><header authenticate="true"><static><HostID>` + esc(sub.HostID) + `</HostID><TransactionID>` + esc(txID) + `</TransactionID></static>` +
		`<mutable><TransactionPhase>` + phase + `</TransactionPhase>` + seg + `</mutable></header>` + authMarker + `<body>` + body + `</body></ebicsRequest>`
}

// reply is the part of a bank response the client acts on. Every field is read
// from an element the bank authenticated.
type reply struct {
	root        *xnode
	tech        string
	techText    string
	biz         string
	txID        string
	orderID     string
	numSegments int
	segment     int
	last        bool
}

func parseReply(root *xnode, wantRoot string) (reply, error) {
	if root.local != wantRoot || root.uri() != nsH004 {
		return reply{}, fmt.Errorf("unexpected response element %q", root.local)
	}
	header := root.child("header")
	if header == nil || header.attr("authenticate") != "true" {
		return reply{}, errors.New("response header is not authenticated")
	}
	r := reply{root: root}
	mut := header.child("mutable")
	r.tech, r.techText = mut.child("ReturnCode").content(), mut.child("ReportText").content()
	if r.tech == "" {
		return reply{}, errors.New("response has no technical return code")
	}
	r.txID = header.path("static", "TransactionID").content()
	r.orderID = mut.child("OrderID").content()
	if n := header.path("static", "NumSegments").content(); n != "" {
		v, err := strconv.Atoi(n)
		if err != nil || v < 1 || v > maxSegments {
			return reply{}, fmt.Errorf("NumSegments %q is not acceptable", n)
		}
		r.numSegments = v
	}
	if sn := mut.child("SegmentNumber"); sn != nil {
		v, err := strconv.Atoi(sn.content())
		if err != nil || v < 1 {
			return reply{}, errors.New("malformed SegmentNumber")
		}
		r.segment, r.last = v, sn.attr("lastSegment") == "true"
	}
	if body := root.child("body"); body != nil {
		if rc := body.child("ReturnCode"); rc != nil && rc.attr("authenticate") == "true" {
			r.biz = rc.content()
		}
	}
	return r, nil
}

const maxSegments = 2000

func orderDataOf(body *xnode) (string, error) {
	dt := body.child("DataTransfer")
	if dt == nil {
		return "", errors.New("response has no DataTransfer")
	}
	return strings.Map(func(c rune) rune {
		if c == ' ' || c == '\n' || c == '\r' || c == '\t' {
			return -1
		}
		return c
	}, dt.child("OrderData").content()), nil
}

func transactionKeyOf(root *xnode, bankE002Digest []byte) ([]byte, error) {
	info := root.path("body", "DataTransfer", "DataEncryptionInfo")
	if info == nil || info.attr("authenticate") != "true" {
		return nil, errors.New("response encryption info is not authenticated")
	}
	if d := info.child("EncryptionPubKeyDigest").content(); d != "" {
		got, err := base64.StdEncoding.DecodeString(d)
		if err != nil || !bytes.Equal(got, bankE002Digest) {
			return nil, errors.New("response is encrypted for another key")
		}
	}
	return base64.StdEncoding.DecodeString(info.child("TransactionKey").content())
}
