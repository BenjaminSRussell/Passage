package ebics

import (
	"crypto/rsa"
	"encoding/hex"
	"math/big"
	"strings"
	"testing"
)

// W3C Canonical XML 1.0, section 3.3 (start and end tags), without the DOCTYPE
// whose attribute default the parser here does not apply.
const w3cExample33In = `<doc>
   <e1   />
   <e2   ></e2>
   <e3   name = "elem3"   id="elem3"   />
   <e4   name="elem4"   id="elem4"   ></e4>
   <e5 a:attr="out" b:attr="sorted" attr2="all" attr="I'm"
      xmlns:b="http://www.ietf.org"
      xmlns:a="http://www.w3.org"
      xmlns="http://www.uvic.ca"/>
   <e6 xmlns="" xmlns:a="http://www.w3.org">
      <e7 xmlns="http://www.ietf.org">
         <e8 xmlns="" xmlns:a="http://www.w3.org">
            <e9 xmlns="" xmlns:a="http://www.ietf.org"/>
         </e8>
      </e7>
   </e6>
</doc>`

const w3cExample33Out = `<doc>
   <e1></e1>
   <e2></e2>
   <e3 id="elem3" name="elem3"></e3>
   <e4 id="elem4" name="elem4"></e4>
   <e5 xmlns="http://www.uvic.ca" xmlns:a="http://www.w3.org" xmlns:b="http://www.ietf.org" attr="I'm" attr2="all" b:attr="sorted" a:attr="out"></e5>
   <e6 xmlns:a="http://www.w3.org">
      <e7 xmlns="http://www.ietf.org">
         <e8 xmlns="">
            <e9 xmlns:a="http://www.ietf.org"></e9>
         </e8>
      </e7>
   </e6>
</doc>`

func TestCanonicalize_W3CExample33(t *testing.T) {
	got, err := Canonicalize([]byte(w3cExample33In))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != w3cExample33Out {
		t.Fatalf("got:\n%s\nwant:\n%s", got, w3cExample33Out)
	}
}

func TestCanonicalize_EscapingAndCharacterReferences(t *testing.T) {
	in := `<?xml version="1.0"?><a y='"' x="1&#x9;2&#xA;3&amp;&lt;&quot;"><!-- gone -->&lt;&gt;&amp;&#xD;z<b/></a>`
	want := "<a x=\"1&#x9;2&#xA;3&amp;&lt;&quot;\" y=\"&quot;\">&lt;&gt;&amp;&#xD;z<b></b></a>"
	got, err := Canonicalize([]byte(in))
	if err != nil || string(got) != want {
		t.Fatalf("%q, %v", got, err)
	}
}

func TestCanonicalize_RejectsDoctypeAndProcessingInstructions(t *testing.T) {
	for _, in := range []string{`<!DOCTYPE a [<!ENTITY x "y">]><a>&x;</a>`, `<a><?pi data?></a>`, `<a:b/>`, `<a></b>`, `<a/><b/>`} {
		if _, err := Canonicalize([]byte(in)); err == nil {
			t.Errorf("accepted %q", in)
		}
	}
}

func TestAuthenticated_SelectsOutermostAuthenticateElementsInDocumentOrder(t *testing.T) {
	root, err := parseXML([]byte(`<r xmlns="urn:x"><h authenticate="true"><i authenticate="true">1</i></h><n>skip</n><d authenticate="true">2</d></r>`))
	if err != nil {
		t.Fatal(err)
	}
	got := string(canonicalNodes(authenticated(root)))
	want := `<h xmlns="urn:x" authenticate="true"><i authenticate="true">1</i></h><d xmlns="urn:x" authenticate="true">2</d>`
	if got != want {
		t.Fatalf("got %s", got)
	}
}

func TestKeyDigest_ExponentAndModulusAsLowercaseHexWithoutLeadingZeros(t *testing.T) {
	for n, want := range map[string]string{
		"abcdef0123456789": "50a254bec2452b8f3f1df32a414aa15c1f5ea7943e72d9ccc947a29e6de47bd6",
		"0a1b2c":           "01c3f3eefbee8fa3a8ae37922d7c2a84b4683615bbadc09475cb9960e756b8f1",
	} {
		mod, _ := new(big.Int).SetString(n, 16)
		if got := hex.EncodeToString(KeyDigest(&rsa.PublicKey{N: mod, E: 65537})); got != want {
			t.Errorf("modulus %s: %s, want %s", n, got, want)
		}
	}
	if got := FormatDigest([]byte{0x01, 0xab, 0xff}); got != "01 AB FF" {
		t.Fatalf("FormatDigest = %q", got)
	}
}

func TestOrderDataCodec_RoundTripPaddingAndLimits(t *testing.T) {
	keys := testKeys(t)
	for _, size := range []int{0, 1, 15, 16, 17, 5000} {
		data := []byte(strings.Repeat("a", size))
		ct, wrapped, plainKey, err := encryptOrderData(data, &keys[2].PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		if len(ct)%16 != 0 {
			t.Fatalf("size %d: ciphertext of %d bytes is not block aligned", size, len(ct))
		}
		key, err := (memoryKeys{e002: keys[2]}).DecryptE002(nil, wrapped)
		if err != nil || string(key) != string(plainKey) {
			t.Fatalf("size %d: key unwrap %v", size, err)
		}
		out, err := openWithKey(key, ct)
		if err != nil || string(out) != string(data) {
			t.Fatalf("size %d: %v", size, err)
		}
	}
	ct, _, key, _ := encryptOrderData([]byte("x"), &keys[2].PublicKey)
	ct[len(ct)-1] ^= 0xff
	if _, err := openWithKey(key, ct); err == nil {
		t.Fatal("corrupted ciphertext must not decode")
	}
	if _, err := openWithKey(key[:8], ct); err == nil {
		t.Fatal("a short key must be refused")
	}
	if _, err := decompress([]byte("not zlib")); err == nil {
		t.Fatal("non-zlib data must be refused")
	}
}

func TestSegments_SplitOnSizeAndKeepOneForEmpty(t *testing.T) {
	got := segments("abcdefg", 3)
	if strings.Join(got, "|") != "abc|def|g" {
		t.Fatalf("%v", got)
	}
	if got := segments("", 3); len(got) != 1 {
		t.Fatalf("%v", got)
	}
}

func TestReturnCodes_ConstantsMatchTheEBICS25Annex(t *testing.T) {
	for name, c := range map[string][2]string{
		"EBICS_OK":                            {CodeOK, "000000"},
		"EBICS_DOWNLOAD_POSTPROCESS_DONE":     {CodeDownloadDone, "011000"},
		"EBICS_DOWNLOAD_POSTPROCESS_SKIPPED":  {CodeDownloadSkipped, "011001"},
		"EBICS_NO_DOWNLOAD_DATA_AVAILABLE":    {CodeNoDownloadData, "090005"},
		"EBICS_INTERNAL_ERROR":                {CodeInternalError, "061099"},
		"EBICS_TX_RECOVERY_SYNC":              {CodeTxRecoverySync, "061101"},
		"EBICS_AUTHENTICATION_FAILED":         {CodeAuthFailed, "061001"},
		"EBICS_USER_UNKNOWN":                  {CodeUserUnknown, "091003"},
		"EBICS_BANK_PUBKEY_UPDATE_REQUIRED":   {CodeBankKeyUpdate, "091008"},
		"EBICS_TX_UNKNOWN_TXID":               {CodeTxUnknown, "091101"},
		"EBICS_TX_ABORT":                      {CodeTxAbort, "091102"},
		"EBICS_TX_MESSAGE_REPLAY":             {CodeTxReplay, "091103"},
		"EBICS_SIGNATURE_VERIFICATION_FAILED": {CodeSignatureFailed, "091301"},
	} {
		if c[0] != c[1] {
			t.Errorf("%s is %s in code, %s in the annex", name, c[0], c[1])
		}
	}
}

func TestClassify_CodesMapToBackOffParkOrProceed(t *testing.T) {
	for code, want := range map[string]Disposition{
		"000000": Proceed, "011000": Proceed, "011001": Proceed, "090005": NoData,
		"061099": BackOff, "061101": BackOff, "091101": BackOff, "091102": BackOff,
		"061001": Park, "091301": Park, "091008": Park, "091003": Park,
		"091103": Park, "091118": Park, "091012": Park, "999999": Park,
	} {
		if got := Classify(code); got != want {
			t.Errorf("%s: %s, want %s", code, got, want)
		}
	}
}
