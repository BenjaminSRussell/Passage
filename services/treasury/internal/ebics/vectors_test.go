package ebics

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

// Fixture from github.com/andrew-svirin/ebics-client-php, tests/_fixtures/hpb.xml
// at commit a7e2502ce8996c146eef5325ee110cbdcca4f909 (MIT licence). It is an HPB
// request produced by a different implementation (PyFinTech 6.0.3, joonis new
// media) against an H004 bank, with the DigestValue that implementation computed
// over the authenticated header. The library's own test (AuthSignatureHandlerTest::
// testDigestValue) regenerates that DigestValue from the request, so the value is
// the digest of the inclusive C14N of <header authenticate="true">.
const pyFinTechHPB = `<?xml version='1.0' encoding='utf-8'?>
<ebicsNoPubKeyDigestsRequest xmlns:ds="http://www.w3.org/2000/09/xmldsig#" xmlns="urn:org:ebics:H004" Version="H004" Revision="1"><header authenticate="true"><static><HostID>CRSISOTB</HostID><Nonce>F88276D74C12FE082492573B6199B361</Nonce><Timestamp>2019-09-13T11:30:47Z</Timestamp><PartnerID>CRS04285</PartnerID><UserID>CRS04285</UserID><Product Language="de">PyFinTech 6.0.3, joonis new media</Product><OrderDetails><OrderType>HPB</OrderType><OrderAttribute>DZHNN</OrderAttribute></OrderDetails><SecurityMedium>0000</SecurityMedium></static><mutable/></header><AuthSignature><ds:SignedInfo><ds:CanonicalizationMethod Algorithm="http://www.w3.org/TR/2001/REC-xml-c14n-20010315"/><ds:SignatureMethod Algorithm="http://www.w3.org/2001/04/xmldsig-more#rsa-sha256"/><ds:Reference URI="#xpointer(//*[@authenticate='true'])"><ds:Transforms><ds:Transform Algorithm="http://www.w3.org/TR/2001/REC-xml-c14n-20010315"/></ds:Transforms><ds:DigestMethod Algorithm="http://www.w3.org/2001/04/xmlenc#sha256"/><ds:DigestValue>Qsej5kpR0Ej9rXej2r1u3grBVhsM75VEV7k3tYrGXdI=</ds:DigestValue></ds:Reference></ds:SignedInfo><ds:SignatureValue>AAAA</ds:SignatureValue></AuthSignature><body/></ebicsNoPubKeyDigestsRequest>`

func TestVector_AuthSignatureDigestMatchesAnotherImplementation(t *testing.T) {
	root, err := parseXML([]byte(pyFinTechHPB))
	if err != nil {
		t.Fatal(err)
	}
	nodes := authenticated(root)
	if len(nodes) != 1 || nodes[0].local != "header" {
		t.Fatalf("authenticated elements = %d; the xpointer selects the one header", len(nodes))
	}
	sum := sha256.Sum256(canonicalNodes(nodes))
	if got, want := base64.StdEncoding.EncodeToString(sum[:]), "Qsej5kpR0Ej9rXej2r1u3grBVhsM75VEV7k3tYrGXdI="; got != want {
		t.Fatalf("DigestValue %s, want %s from the PyFinTech fixture", got, want)
	}
}

// The A006 input convention is pinned by reading ebics-client-php (same commit):
// EbicsClient::createUploadTransaction hashes the order content with
// CryptService::hash (SHA-256, binary), passes that digest to
// UserSignatureHandlerV2::handleXml, which calls CryptService::encrypt for
// A_VERSION6, and that signs the digest with RSASSA-PSS, SHA-256, MGF1-SHA-256
// ($rsa->setHash('sha256'); setMGFHash('sha256'); sign($data)), which hashes it
// a second time. XmlData::getTrimmedContent removes CR and LF from XML before
// the hash and the upload. No fixture in that repository holds a plain key with
// a recorded A006 signature (keys.json is passphrase-encrypted and no signature
// value is recorded), so there is no byte-exact A006 signature vector offline;
// that vector is left to the sandbox run (#213).
func TestOrderDigest_RemovesLineBreaksAndCtrlZBeforeHashing(t *testing.T) {
	raw := []byte("<a>\r\n  <b/>\n</a>\x1a")
	want := sha256.Sum256([]byte("<a>  <b/></a>"))
	if got := orderDigest(raw); !bytes.Equal(got, want[:]) {
		t.Fatalf("orderDigest = %x, want %x", got, want)
	}
}
