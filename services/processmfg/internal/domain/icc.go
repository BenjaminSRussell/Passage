package domain

import (
	"bytes"
	"encoding/binary"
	"math"
)

// SRGBProfile is an ICC v2.1 display (mntr) profile of sRGB IEC 61966-2.1,
// the DestOutputProfile of the PDF/A output intent. It is built here rather
// than shipped as a file: the D50-adapted sRGB primaries (IEC 61966-2.1 Annex
// F, Bradford-adapted as in the ICC's own sRGB profiles), the D50 white point
// and the sRGB tone curve sampled at 1024 points. The bytes are fixed.
var SRGBProfile = buildSRGBProfile()

const (
	iccHeaderSize = 128
	iccCurvePts   = 1024
	iccDescText   = "sRGB IEC61966-2.1"
	iccCopyright  = "No copyright, use freely"
)

func s15Fixed16(v float64) uint32 { return uint32(int32(math.Round(v * 65536))) }

func iccXYZ(x, y, z float64) []byte {
	b := make([]byte, 20)
	copy(b, "XYZ ")
	binary.BigEndian.PutUint32(b[8:], s15Fixed16(x))
	binary.BigEndian.PutUint32(b[12:], s15Fixed16(y))
	binary.BigEndian.PutUint32(b[16:], s15Fixed16(z))
	return b
}

// iccDesc is a v2 textDescriptionType: the ASCII description with its NUL,
// no Unicode and no ScriptCode description.
func iccDesc(s string) []byte {
	var b bytes.Buffer
	b.WriteString("desc\x00\x00\x00\x00")
	_ = binary.Write(&b, binary.BigEndian, uint32(len(s)+1))
	b.WriteString(s)
	b.WriteByte(0)
	b.Write(make([]byte, 4+4+2+1+67))
	return b.Bytes()
}

func iccText(s string) []byte {
	return append([]byte("text\x00\x00\x00\x00"+s), 0)
}

// iccCurve samples the sRGB transfer function (linear segment below 0.04045,
// then the 2.4 power) as a curveType.
func iccCurve() []byte {
	var b bytes.Buffer
	b.WriteString("curv\x00\x00\x00\x00")
	_ = binary.Write(&b, binary.BigEndian, uint32(iccCurvePts))
	for i := range iccCurvePts {
		x := float64(i) / (iccCurvePts - 1)
		y := x / 12.92
		if x > 0.04045 {
			y = math.Pow((x+0.055)/1.055, 2.4)
		}
		_ = binary.Write(&b, binary.BigEndian, uint16(math.Round(y*65535)))
	}
	return b.Bytes()
}

func buildSRGBProfile() []byte {
	curve := iccCurve()
	type tag struct {
		sig  string
		data []byte
	}
	// rTRC, gTRC and bTRC share one curve.
	tags := []tag{
		{"desc", iccDesc(iccDescText)},
		{"cprt", iccText(iccCopyright)},
		{"wtpt", iccXYZ(0.9642, 1.0, 0.8249)},
		{"rXYZ", iccXYZ(0.4360747, 0.2225045, 0.0139322)},
		{"gXYZ", iccXYZ(0.3850649, 0.7168786, 0.0971045)},
		{"bXYZ", iccXYZ(0.1430804, 0.0606169, 0.7141733)},
		{"rTRC", curve},
		{"gTRC", nil},
		{"bTRC", nil},
	}
	tableSize := 4 + 12*len(tags)
	var body bytes.Buffer
	table := make([]byte, tableSize)
	binary.BigEndian.PutUint32(table, uint32(len(tags)))
	var curveOffset, curveSize uint32
	for i, t := range tags {
		entry := table[4+12*i:]
		copy(entry, t.sig)
		if t.data == nil {
			binary.BigEndian.PutUint32(entry[4:], curveOffset)
			binary.BigEndian.PutUint32(entry[8:], curveSize)
			continue
		}
		for body.Len()%4 != 0 {
			body.WriteByte(0)
		}
		offset := uint32(iccHeaderSize + tableSize + body.Len())
		binary.BigEndian.PutUint32(entry[4:], offset)
		binary.BigEndian.PutUint32(entry[8:], uint32(len(t.data)))
		if t.sig == "rTRC" {
			curveOffset, curveSize = offset, uint32(len(t.data))
		}
		body.Write(t.data)
	}
	for body.Len()%4 != 0 {
		body.WriteByte(0)
	}
	header := make([]byte, iccHeaderSize)
	size := iccHeaderSize + tableSize + body.Len()
	binary.BigEndian.PutUint32(header[0:], uint32(size))
	binary.BigEndian.PutUint32(header[8:], 0x02100000) // version 2.1.0
	copy(header[12:], "mntr")
	copy(header[16:], "RGB ")
	copy(header[20:], "XYZ ")
	for i, v := range []uint16{2026, 1, 1, 0, 0, 0} { // creation date, fixed
		binary.BigEndian.PutUint16(header[24+2*i:], v)
	}
	copy(header[36:], "acsp")
	// Rendering intent 0 (perceptual); PCS illuminant D50.
	binary.BigEndian.PutUint32(header[68:], s15Fixed16(0.9642))
	binary.BigEndian.PutUint32(header[72:], s15Fixed16(1.0))
	binary.BigEndian.PutUint32(header[76:], s15Fixed16(0.8249))
	out := make([]byte, 0, size)
	out = append(out, header...)
	out = append(out, table...)
	return append(out, body.Bytes()...)
}
