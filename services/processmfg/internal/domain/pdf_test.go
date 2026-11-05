package domain

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

var testMeta = PDFMeta{Title: "Batch record L-1", Created: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)}

func testLines() []string {
	lines := []string{"Batch (L-1) \\ ok", "Grün 中", strings.Repeat("x", 250)}
	for i := 0; i < 130; i++ {
		lines = append(lines, fmt.Sprint("line ", i))
	}
	return lines
}

func TestRenderTextPDF_DeterministicPagedAndWellFormed(t *testing.T) {
	lines := testLines()
	a := RenderTextPDF(testMeta, lines)
	if !bytes.Equal(a, RenderTextPDF(testMeta, lines)) {
		t.Fatal("the same input rendered to different bytes")
	}
	s := string(a)
	if !strings.HasPrefix(s, "%PDF-1.7\n") || !strings.HasSuffix(s, "%%EOF\n") || !strings.Contains(s, "/Count 3") {
		t.Fatalf("header, trailer or page count wrong: %.200s", s)
	}
	// Parentheses and backslashes are escaped, Latin-1 is an octal escape and
	// anything beyond is '?'; the 250-character line is wrapped.
	if !strings.Contains(s, `(Batch \(L-1\) \\ ok)`) || !strings.Contains(s, `(Gr\374n ?)`) || strings.Contains(s, strings.Repeat("x", 111)) {
		t.Fatal("escaping or wrapping wrong")
	}
	// Every xref entry points at its object.
	m := regexp.MustCompile(`startxref\n(\d+)`).FindStringSubmatch(s)
	start, _ := strconv.Atoi(m[1])
	entries := strings.Split(strings.TrimSpace(s[start:strings.Index(s, "trailer")]), "\n")[3:]
	for i, e := range entries {
		off, _ := strconv.Atoi(e[:10])
		if !strings.HasPrefix(s[off:], fmt.Sprintf("%d 0 obj", i+1)) {
			t.Fatalf("xref entry %d points at %q", i+1, s[off:off+10])
		}
	}
	// Another date or text is another document ID.
	other := testMeta
	other.Created = other.Created.Add(time.Second)
	if trailerID(t, s) == trailerID(t, string(RenderTextPDF(other, lines))) || trailerID(t, s) == trailerID(t, string(RenderTextPDF(testMeta, lines[1:]))) {
		t.Fatal("document ID does not follow the content")
	}
}

// pdfObjects splits a rendering into its numbered objects, checking that each
// stream's /Length is its exact byte count.
func pdfObjects(t *testing.T, s string) map[int]string {
	t.Helper()
	out := map[int]string{}
	re := regexp.MustCompile(`(?s)(\d+) 0 obj\n(.*?)\nendobj\n`)
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		n, _ := strconv.Atoi(m[1])
		body := m[2]
		if i := strings.Index(body, "\nstream\n"); i >= 0 {
			lm := regexp.MustCompile(`/Length (\d+)`).FindStringSubmatch(body[:i])
			l, _ := strconv.Atoi(lm[1])
			data := body[i+len("\nstream\n"):]
			if !strings.HasSuffix(data, "\nendstream") || len(data)-len("\nendstream") != l {
				t.Fatalf("object %d: /Length %d, stream holds %d bytes", n, l, len(data)-len("\nendstream"))
			}
		}
		out[n] = body
	}
	return out
}

func streamData(t *testing.T, obj string) []byte {
	t.Helper()
	i := strings.Index(obj, "\nstream\n")
	data := []byte(strings.TrimSuffix(obj[i+len("\nstream\n"):], "\nendstream"))
	if !strings.Contains(obj[:i], "/FlateDecode") {
		return data
	}
	r, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func ref(t *testing.T, obj, key string) int {
	t.Helper()
	m := regexp.MustCompile(regexp.QuoteMeta(key) + ` \[?(\d+) 0 R`).FindStringSubmatch(obj)
	if m == nil {
		t.Fatalf("no %s in %.200s", key, obj)
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func trailerID(t *testing.T, s string) string {
	t.Helper()
	m := regexp.MustCompile(`/ID \[<([0-9a-f]{32})> <([0-9a-f]{32})>\]`).FindStringSubmatch(s[strings.LastIndex(s, "trailer"):])
	if m == nil || m[1] != m[2] {
		t.Fatalf("trailer has no document ID: %s", s[strings.LastIndex(s, "trailer"):])
	}
	return m[1]
}

// TestRenderTextPDF_PDFA2bStructure checks the PDF/A-2b requirements that can
// be read off the file without a validator (the full check is veraPDF, see
// TestRenderTextPDF_VeraPDF): PDF 1.7 header with a binary comment, no
// encryption, a document ID, XMP metadata naming PDF/A-2b and agreeing with
// the Info dictionary, an sRGB output intent with an embedded ICC profile,
// and only embedded TrueType fonts whose widths are the font program's.
func TestRenderTextPDF_PDFA2bStructure(t *testing.T) {
	s := string(RenderTextPDF(testMeta, testLines()))
	if !strings.HasPrefix(s, "%PDF-1.7\n%") || len(s) < 14 {
		t.Fatal("no PDF 1.7 header")
	}
	for _, c := range []byte(s[10:14]) {
		if c < 128 {
			t.Fatal("second header line is not a binary comment of four bytes above 127")
		}
	}
	for _, forbidden := range []string{"/Encrypt", "/JavaScript", "/JS ", "/Launch", "/EmbeddedFile", "/SMask", "/Transparency", "/Type1", "/OpenAction", "/AA "} {
		if strings.Contains(s, forbidden) {
			t.Fatalf("PDF/A forbids %s", forbidden)
		}
	}
	trailerID(t, s)
	trailer := s[strings.LastIndex(s, "trailer"):]
	objs := pdfObjects(t, s)
	catalog := objs[ref(t, trailer, "/Root")]
	info := objs[ref(t, trailer, "/Info")]

	// XMP: PDF/A-2b identification, title and producer as in Info.
	meta := objs[ref(t, catalog, "/Metadata")]
	if !strings.Contains(meta, "/Type /Metadata /Subtype /XML") || strings.Contains(meta[:strings.Index(meta, "stream")], "/Filter") {
		t.Fatalf("metadata stream %.120s", meta)
	}
	xmp := string(streamData(t, meta))
	for _, want := range []string{"<?xpacket begin=\"\xef\xbb\xbf\" id=\"W5M0MpCehiHzreSzNTczkc9d\"?>", "<pdfaid:part>2</pdfaid:part>", "<pdfaid:conformance>B</pdfaid:conformance>",
		`<rdf:li xml:lang="x-default">Batch record L-1</rdf:li>`, "<pdf:Producer>Passage processmfg</pdf:Producer>",
		"<xmp:CreateDate>2026-03-04T05:06:07Z</xmp:CreateDate>", "<xmp:ModifyDate>2026-03-04T05:06:07Z</xmp:ModifyDate>", `<?xpacket end="r"?>`} {
		if !strings.Contains(xmp, want) {
			t.Fatalf("XMP lacks %q:\n%s", want, xmp)
		}
	}
	if !strings.Contains(info, "/Title "+pdfTextString("Batch record L-1")) || !strings.Contains(info, "/Producer "+pdfTextString("Passage processmfg")) ||
		!strings.Contains(info, "/CreationDate (D:20260304050607+00'00')") || !strings.Contains(info, "/ModDate (D:20260304050607+00'00')") {
		t.Fatalf("Info does not match the XMP: %s", info)
	}

	// Output intent: GTS_PDFA1 with an RGB display profile.
	intent := objs[ref(t, catalog, "/OutputIntents")]
	if !strings.Contains(intent, "/Type /OutputIntent /S /GTS_PDFA1") || !strings.Contains(intent, "/OutputConditionIdentifier (sRGB IEC61966-2.1)") {
		t.Fatalf("output intent %s", intent)
	}
	profileObj := objs[ref(t, intent, "/DestOutputProfile")]
	if !strings.Contains(profileObj, "/N 3") {
		t.Fatal("ICC profile is not three-component")
	}
	icc := streamData(t, profileObj)
	if !bytes.Equal(icc, SRGBProfile) || int(binary.BigEndian.Uint32(icc)) != len(icc) || string(icc[36:40]) != "acsp" ||
		string(icc[12:16]) != "mntr" || string(icc[16:20]) != "RGB " || string(icc[20:24]) != "XYZ " || icc[8] != 2 {
		t.Fatalf("ICC header %q", icc[:40])
	}
	tags := binary.BigEndian.Uint32(icc[128:])
	seen := map[string]bool{}
	for i := range int(tags) {
		e := icc[132+12*i:]
		off, size := binary.BigEndian.Uint32(e[4:]), binary.BigEndian.Uint32(e[8:])
		if int(off+size) > len(icc) || off%4 != 0 {
			t.Fatalf("ICC tag %q at %d+%d outside the profile", e[:4], off, size)
		}
		seen[string(e[:4])] = true
	}
	for _, tag := range []string{"desc", "cprt", "wtpt", "rXYZ", "gXYZ", "bXYZ", "rTRC", "gTRC", "bTRC"} {
		if !seen[tag] {
			t.Fatalf("ICC profile lacks the %s tag a display profile needs", tag)
		}
	}

	// Fonts: every page font is an embedded TrueType whose widths are the
	// advances of the embedded program.
	f, err := sfnt.Parse(gomono.TTF)
	if err != nil {
		t.Fatal(err)
	}
	var buf sfnt.Buffer
	upem := int(f.UnitsPerEm())
	fonts := 0
	for n, obj := range objs {
		if !strings.Contains(obj, "/Type /Font ") {
			continue
		}
		fonts++
		if !strings.Contains(obj, "/Subtype /TrueType") || !strings.Contains(obj, "/Encoding /WinAnsiEncoding") {
			t.Fatalf("font %d is not a WinAnsi TrueType font: %.120s", n, obj)
		}
		desc := objs[ref(t, obj, "/FontDescriptor")]
		program := objs[ref(t, desc, "/FontFile2")]
		got := streamData(t, program)
		if !bytes.Equal(got, gomono.TTF) || !strings.Contains(program, fmt.Sprintf("/Length1 %d", len(gomono.TTF))) {
			t.Fatalf("font %d: embedded program is not the font, or /Length1 is wrong", n)
		}
		if !strings.Contains(desc, "/Flags 33") {
			t.Fatalf("font descriptor flags: %s", desc)
		}
		wm := regexp.MustCompile(`/FirstChar (\d+) /LastChar (\d+) /Widths \[([0-9 ]+)\]`).FindStringSubmatch(obj)
		first, _ := strconv.Atoi(wm[1])
		widths := strings.Fields(wm[3])
		for _, code := range []int{' ', 'A', 'x', '?', '\\', 0xfc} {
			g, err := f.GlyphIndex(&buf, rune(code))
			if err != nil || g == 0 {
				t.Fatalf("no glyph for %q", rune(code))
			}
			adv, _ := f.GlyphAdvance(&buf, g, fixed.Int26_6(upem<<6), font.HintingNone)
			want := (int(adv)*1000 + (upem<<6)/2) / (upem << 6)
			if widths[code-first] != strconv.Itoa(want) {
				t.Fatalf("width of %q is %s, the font program says %d", rune(code), widths[code-first], want)
			}
		}
	}
	if fonts != 1 {
		t.Fatalf("%d fonts, want 1", fonts)
	}
	for n, obj := range objs {
		if strings.Contains(obj, "/Type /Page ") && !strings.Contains(obj, "/Font << /F1 3 0 R >>") {
			t.Fatalf("page %d uses another font: %s", n, obj)
		}
	}
}

// TestRenderTextPDF_VeraPDF validates a rendering with veraPDF against the
// PDF/A-2b profile when VERAPDF names a veraPDF command line (for example a
// wrapper around java -cp greenfield-apps org.verapdf.apps.GreenfieldCliWrapper);
// the build has no validator otherwise, and the test is skipped.
func TestRenderTextPDF_VeraPDF(t *testing.T) {
	cmd := os.Getenv("VERAPDF")
	if cmd == "" {
		t.Skip("VERAPDF not set; the structural checks are TestRenderTextPDF_PDFA2bStructure")
	}
	path := filepath.Join(t.TempDir(), "record.pdf")
	if err := os.WriteFile(path, RenderTextPDF(testMeta, testLines()), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(cmd, "--flavour", "2b", "--format", "text", path).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "PASS ") {
		t.Fatalf("veraPDF did not pass the rendering (%v):\n%s", err, out)
	}
	t.Logf("%s", out)
}
