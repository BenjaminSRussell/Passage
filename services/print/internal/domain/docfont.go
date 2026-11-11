package domain

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"slices"
	"strings"
	"unicode/utf16"

	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/sfnt"
)

// Business document fonts (issue #530). The documents print Go Regular and Go Bold, the
// TrueType fonts of the Go project (Bigelow & Holmes, BSD-style 3-clause licence, see
// services/print/third_party/go-fonts-LICENSE). They cover Latin with its extended letters
// (Polish, Czech, Hungarian, Turkish, ...), Greek and Cyrillic. Each PDF embeds a subset of
// the fonts as Type0 fonts with a CIDFontType2 descendant, Identity-H encoding and a
// ToUnicode CMap, so the text can be selected and searched. A character the fonts lack
// (for example Chinese, Japanese, Korean, Arabic or the rupee sign) is drawn as the
// replacement character U+FFFD, and the original text is kept as ActualText so that copying
// and searching still find it. Text is laid out left to right, one glyph per character,
// without kerning.
//
// The subset keeps the glyph ids of the full font (unused glyphs are emptied), so a CID is
// the glyph id and CIDToGIDMap is /Identity. Nothing in the output depends on the time or
// on randomness, and no stream is compressed, so the bytes depend only on this code and the
// pinned font data.

const (
	faceRegular = 0
	faceBold    = 1
)

// fontFace is one parsed TrueType font with the metrics the layout and the PDF need.
type fontFace struct {
	name      string // PostScript-style name without the subset tag
	font      *sfnt.Font
	tables    map[string][]byte
	numGlyphs int
	upem      float64
	advance   []uint16 // per glyph, font units
	bbox      [4]int16
	ascent    int16
	descent   int16
	capHeight int16
	stemV     int
	fallback  sfnt.GlyphIndex // the replacement character's glyph
}

var docFaces = [2]*fontFace{mustFace("GoRegular", goregular.TTF, 80), mustFace("GoBold", gobold.TTF, 140)}

func mustFace(name string, ttf []byte, stemV int) *fontFace {
	f, err := newFace(name, ttf, stemV)
	if err != nil {
		panic("document font " + name + ": " + err.Error())
	}
	return f
}

func newFace(name string, ttf []byte, stemV int) (*fontFace, error) {
	parsed, err := sfnt.Parse(ttf)
	if err != nil {
		return nil, err
	}
	tables, err := ttfTables(ttf)
	if err != nil {
		return nil, err
	}
	for _, tag := range []string{"head", "hhea", "hmtx", "maxp", "loca", "glyf", "cmap"} {
		if tables[tag] == nil {
			return nil, fmt.Errorf("no %s table", tag)
		}
	}
	head, hhea, hmtx := tables["head"], tables["hhea"], tables["hmtx"]
	if len(head) < 54 || len(hhea) < 36 || len(tables["maxp"]) < 6 {
		return nil, fmt.Errorf("short head, hhea or maxp table")
	}
	f := &fontFace{name: name, font: parsed, tables: tables, stemV: stemV}
	f.numGlyphs = int(be16(tables["maxp"][4:]))
	f.upem = float64(be16(head[18:]))
	for i := range f.bbox {
		f.bbox[i] = int16(be16(head[36+2*i:]))
	}
	f.ascent = int16(be16(hhea[4:]))
	f.descent = int16(be16(hhea[6:]))
	f.capHeight = f.ascent
	if os2 := tables["OS/2"]; len(os2) >= 90 && be16(os2) >= 2 {
		f.capHeight = int16(be16(os2[88:]))
	}
	nh := int(be16(hhea[34:]))
	if nh == 0 || len(hmtx) < 4*nh {
		return nil, fmt.Errorf("bad hmtx table")
	}
	f.advance = make([]uint16, f.numGlyphs)
	for g := range f.advance {
		if g < nh {
			f.advance[g] = be16(hmtx[4*g:])
		} else {
			f.advance[g] = f.advance[nh-1]
		}
	}
	var buf sfnt.Buffer
	f.fallback, _ = parsed.GlyphIndex(&buf, '�')
	return f, nil
}

func be16(b []byte) uint16 { return binary.BigEndian.Uint16(b) }
func be32(b []byte) uint32 { return binary.BigEndian.Uint32(b) }

// ttfTables reads the table directory of a single TrueType font.
func ttfTables(ttf []byte) (map[string][]byte, error) {
	if len(ttf) < 12 || be32(ttf) != 0x00010000 {
		return nil, fmt.Errorf("not a TrueType font")
	}
	n := int(be16(ttf[4:]))
	if len(ttf) < 12+16*n {
		return nil, fmt.Errorf("short table directory")
	}
	tables := make(map[string][]byte, n)
	for i := 0; i < n; i++ {
		rec := ttf[12+16*i:]
		off, length := int(be32(rec[8:])), int(be32(rec[12:]))
		if off < 0 || length < 0 || off+length > len(ttf) {
			return nil, fmt.Errorf("table %q is out of range", rec[:4])
		}
		tables[string(rec[:4])] = ttf[off : off+length]
	}
	return tables, nil
}

// glyph returns the glyf data of glyph g.
func (f *fontFace) glyph(g int) []byte {
	loca, glyf := f.tables["loca"], f.tables["glyf"]
	var start, end int
	if be16(f.tables["head"][50:]) == 0 {
		start, end = 2*int(be16(loca[2*g:])), 2*int(be16(loca[2*g+2:]))
	} else {
		start, end = int(be32(loca[4*g:])), int(be32(loca[4*g+4:]))
	}
	if start >= end || end > len(glyf) {
		return nil
	}
	return glyf[start:end]
}

// components lists the glyphs a composite glyph is built from.
func components(data []byte) []int {
	if len(data) < 10 || int16(be16(data)) >= 0 {
		return nil
	}
	var out []int
	for p := 10; p+4 <= len(data); {
		flags := be16(data[p:])
		out = append(out, int(be16(data[p+2:])))
		p += 4
		if flags&0x0001 != 0 { // ARG_1_AND_2_ARE_WORDS
			p += 4
		} else {
			p += 2
		}
		switch {
		case flags&0x0008 != 0: // WE_HAVE_A_SCALE
			p += 2
		case flags&0x0040 != 0: // WE_HAVE_AN_X_AND_Y_SCALE
			p += 4
		case flags&0x0080 != 0: // WE_HAVE_A_TWO_BY_TWO
			p += 8
		}
		if flags&0x0020 == 0 { // MORE_COMPONENTS
			break
		}
	}
	return out
}

// subset returns a TrueType font with the glyph ids of the full font in which only the
// given glyphs (with .notdef and the parts of composite glyphs) keep their outlines.
func (f *fontFace) subset(used []sfnt.GlyphIndex) []byte {
	keep := make([]bool, f.numGlyphs)
	var visit func(g int)
	visit = func(g int) {
		if g < 0 || g >= f.numGlyphs || keep[g] {
			return
		}
		keep[g] = true
		for _, c := range components(f.glyph(g)) {
			visit(c)
		}
	}
	visit(0)
	for _, g := range used {
		visit(int(g))
	}
	var glyf bytes.Buffer
	loca := make([]byte, 4*(f.numGlyphs+1))
	for g := 0; g < f.numGlyphs; g++ {
		binary.BigEndian.PutUint32(loca[4*g:], uint32(glyf.Len()))
		if keep[g] {
			glyf.Write(f.glyph(g))
			for glyf.Len()%4 != 0 {
				glyf.WriteByte(0)
			}
		}
	}
	binary.BigEndian.PutUint32(loca[4*f.numGlyphs:], uint32(glyf.Len()))

	head := slices.Clone(f.tables["head"])
	binary.BigEndian.PutUint32(head[8:], 0)  // checkSumAdjustment, set below
	binary.BigEndian.PutUint16(head[50:], 1) // long loca offsets
	post := make([]byte, 32)
	copy(post, f.tables["post"])
	binary.BigEndian.PutUint32(post, 0x00030000) // version 3: no glyph names

	tables := map[string][]byte{
		"head": head, "hhea": f.tables["hhea"], "maxp": f.tables["maxp"], "hmtx": f.tables["hmtx"],
		"loca": loca, "glyf": glyf.Bytes(), "cmap": f.tables["cmap"], "post": post,
	}
	for _, tag := range []string{"OS/2", "cvt ", "fpgm", "prep"} {
		if t := f.tables[tag]; t != nil {
			tables[tag] = t
		}
	}
	return writeTTF(tables)
}

func ttfChecksum(b []byte) uint32 {
	var sum uint32
	for i := 0; i < len(b); i += 4 {
		var w [4]byte
		copy(w[:], b[i:])
		sum += be32(w[:])
	}
	return sum
}

// writeTTF assembles tables into a font file with tables in tag order.
func writeTTF(tables map[string][]byte) []byte {
	tags := make([]string, 0, len(tables))
	for t := range tables {
		tags = append(tags, t)
	}
	slices.Sort(tags)
	n := len(tags)
	entrySelector := 0
	for 1<<(entrySelector+1) <= n {
		entrySelector++
	}
	searchRange := 16 << entrySelector
	var out bytes.Buffer
	hdr := make([]byte, 12+16*n)
	binary.BigEndian.PutUint32(hdr, 0x00010000)
	binary.BigEndian.PutUint16(hdr[4:], uint16(n))
	binary.BigEndian.PutUint16(hdr[6:], uint16(searchRange))
	binary.BigEndian.PutUint16(hdr[8:], uint16(entrySelector))
	binary.BigEndian.PutUint16(hdr[10:], uint16(16*n-searchRange))
	offset := len(hdr)
	headAt := 0
	for i, t := range tags {
		data := tables[t]
		rec := hdr[12+16*i:]
		copy(rec, t)
		binary.BigEndian.PutUint32(rec[4:], ttfChecksum(data))
		binary.BigEndian.PutUint32(rec[8:], uint32(offset))
		binary.BigEndian.PutUint32(rec[12:], uint32(len(data)))
		if t == "head" {
			headAt = offset
		}
		offset += (len(data) + 3) &^ 3
	}
	out.Write(hdr)
	for _, t := range tags {
		out.Write(tables[t])
		for out.Len()%4 != 0 {
			out.WriteByte(0)
		}
	}
	font := out.Bytes()
	binary.BigEndian.PutUint32(font[headAt+8:], 0xB1B0AFBA-ttfChecksum(font))
	return font
}

// glyphRun is text encoded for one face: the glyph ids and whether a character had to be
// replaced by the fallback glyph.
type glyphRun struct {
	glyphs  []sfnt.GlyphIndex
	missing bool
}

// docText encodes text for the faces and records which glyphs a document uses.
type docText struct {
	buf     sfnt.Buffer
	used    [2]map[sfnt.GlyphIndex]rune
	missing int // characters drawn with the fallback glyph
}

func newDocText() *docText {
	return &docText{used: [2]map[sfnt.GlyphIndex]rune{{}, {}}}
}

func (t *docText) glyphIndex(face int, r rune) (sfnt.GlyphIndex, bool) {
	g, err := docFaces[face].font.GlyphIndex(&t.buf, r)
	if err != nil || g == 0 {
		return docFaces[face].fallback, false
	}
	return g, true
}

// width is the advance width of s in points, without kerning.
func (t *docText) width(face int, size float64, s string) float64 {
	f := docFaces[face]
	units := 0
	for _, r := range s {
		g, _ := t.glyphIndex(face, r)
		units += int(f.advance[g])
	}
	return float64(units) * size / f.upem
}

// encode maps s to glyphs and marks them used.
func (t *docText) encode(face int, s string) glyphRun {
	var run glyphRun
	for _, r := range s {
		g, ok := t.glyphIndex(face, r)
		if !ok {
			run.missing = true
			t.missing++
			r = '�'
		}
		if prev, seen := t.used[face][g]; !seen || r < prev {
			t.used[face][g] = r
		}
		run.glyphs = append(run.glyphs, g)
	}
	return run
}

// hexGlyphs writes glyph ids as a PDF hex string of 2-byte codes.
func hexGlyphs(gs []sfnt.GlyphIndex) string {
	var b strings.Builder
	b.WriteByte('<')
	for _, g := range gs {
		fmt.Fprintf(&b, "%04X", uint16(g))
	}
	b.WriteByte('>')
	return b.String()
}

// utf16Hex is s as a PDF text string: UTF-16BE with a byte order mark, in hex.
func utf16Hex(s string) string {
	var b strings.Builder
	b.WriteString("<FEFF")
	for _, u := range utf16.Encode([]rune(s)) {
		fmt.Fprintf(&b, "%04X", u)
	}
	b.WriteByte('>')
	return b.String()
}

// pdfFontObjects returns the five objects of one embedded font, numbered from first:
// Type0 font, CIDFontType2, font descriptor, font file and ToUnicode CMap.
func pdfFontObjects(face int, used map[sfnt.GlyphIndex]rune, first int) []string {
	f := docFaces[face]
	gids := make([]sfnt.GlyphIndex, 0, len(used))
	for g := range used {
		gids = append(gids, g)
	}
	slices.Sort(gids)
	scale := func(v int) int {
		x := float64(v) * 1000 / f.upem
		if x < 0 {
			return int(x - 0.5)
		}
		return int(x + 0.5)
	}
	// A deterministic subset tag from the glyph set: six capital letters.
	h := fnv.New32a()
	h.Write([]byte(f.name))
	for _, g := range gids {
		_, _ = h.Write([]byte{byte(g >> 8), byte(g)})
	}
	sum := h.Sum32()
	tag := make([]byte, 6)
	for i := range tag {
		tag[i] = 'A' + byte(sum%26)
		sum /= 26
	}
	base := string(tag) + "+" + f.name

	var widths strings.Builder
	for _, g := range gids {
		fmt.Fprintf(&widths, "%d [%d] ", g, scale(int(f.advance[g])))
	}
	program := f.subset(gids)

	var cmap strings.Builder
	cmap.WriteString("/CIDInit /ProcSet findresource begin\n12 dict begin\nbegincmap\n")
	cmap.WriteString("/CIDSystemInfo << /Registry (Adobe) /Ordering (UCS) /Supplement 0 >> def\n")
	cmap.WriteString("/CMapName /Adobe-Identity-UCS def\n/CMapType 2 def\n")
	cmap.WriteString("1 begincodespacerange\n<0000> <FFFF>\nendcodespacerange\n")
	for i := 0; i < len(gids); i += 100 {
		chunk := gids[i:min(i+100, len(gids))]
		fmt.Fprintf(&cmap, "%d beginbfchar\n", len(chunk))
		for _, g := range chunk {
			u := utf16Hex(string(used[g]))
			fmt.Fprintf(&cmap, "<%04X> <%s>\n", uint16(g), u[5:len(u)-1])
		}
		cmap.WriteString("endbfchar\n")
	}
	cmap.WriteString("endcmap\nCMapName currentdict /CMap defineresource pop\nend\nend\n")

	flags := 32 // nonsymbolic
	if face == faceBold {
		flags |= 1 << 18 // force bold
	}
	return []string{
		fmt.Sprintf("<< /Type /Font /Subtype /Type0 /BaseFont /%s /Encoding /Identity-H /DescendantFonts [%d 0 R] /ToUnicode %d 0 R >>", base, first+1, first+4),
		fmt.Sprintf("<< /Type /Font /Subtype /CIDFontType2 /BaseFont /%s /CIDSystemInfo << /Registry (Adobe) /Ordering (Identity) /Supplement 0 >> /FontDescriptor %d 0 R /DW %d /W [%s] /CIDToGIDMap /Identity >>",
			base, first+2, scale(int(f.advance[0])), strings.TrimSpace(widths.String())),
		fmt.Sprintf("<< /Type /FontDescriptor /FontName /%s /Flags %d /FontBBox [%d %d %d %d] /ItalicAngle 0 /Ascent %d /Descent %d /CapHeight %d /StemV %d /FontFile2 %d 0 R >>",
			base, flags, scale(int(f.bbox[0])), scale(int(f.bbox[1])), scale(int(f.bbox[2])), scale(int(f.bbox[3])),
			scale(int(f.ascent)), scale(int(f.descent)), scale(int(f.capHeight)), f.stemV, first+3),
		fmt.Sprintf("<< /Length %d /Length1 %d >>\nstream\n%s\nendstream", len(program), len(program), program),
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", cmap.Len(), cmap.String()),
	}
}
