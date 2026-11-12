package domain

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// EPC decoding (wave 32, FR-32.9). A tag carries a 96-bit GS1 EPC binary encoding (GS1 TDS); the warehouse decodes
// the schemes a warehouse reads: SGTIN-96 (a trade item with serial), SSCC-96 (a logistic unit, the pallet), SGLN-96
// (a location), GRAI-96 and GIAI-96 (returnable and individual assets), GSRN-96 (a service relation) and GDTI-96 (a
// document). Each gives the GS1 key the tag identifies with its check digit, so a read of a pallet's tag is the same
// SSCC the pallet's label and the receipt carry.

// Codes of RFID reads.
const (
	CodeEPCInvalid = "WMS-EPC-INVALID" // the tag data is not a 96-bit EPC of a known scheme
)

// EPC is a decoded tag.
type EPC struct {
	Scheme        string `json:"scheme"`
	Filter        int    `json:"filter"`
	Partition     int    `json:"partition"`
	CompanyPrefix string `json:"companyPrefix"`
	// Key is the GS1 key the tag identifies: the GTIN-14 of an SGTIN, the SSCC, GLN-13, GRAI-14, GSRN-18 or GDTI-13,
	// each with its check digit; the GIAI has none. Serial is the SGTIN, GRAI or GDTI serial, and the location
	// extension of an SGLN, in decimal.
	Key    string `json:"key"`
	Serial string `json:"serial,omitempty"`
	URN    string `json:"urn"`
}

type epcPartition struct{ cpBits, cpDigits, refBits, refDigits int }

var (
	sgtinPartitions = []epcPartition{{40, 12, 4, 1}, {37, 11, 7, 2}, {34, 10, 10, 3}, {30, 9, 14, 4}, {27, 8, 17, 5}, {24, 7, 20, 6}, {20, 6, 24, 7}}
	ssccPartitions  = []epcPartition{{40, 12, 18, 5}, {37, 11, 21, 6}, {34, 10, 24, 7}, {30, 9, 28, 8}, {27, 8, 31, 9}, {24, 7, 34, 10}, {20, 6, 38, 11}}
	sglnPartitions  = []epcPartition{{40, 12, 1, 0}, {37, 11, 4, 1}, {34, 10, 7, 2}, {30, 9, 11, 3}, {27, 8, 14, 4}, {24, 7, 17, 5}, {20, 6, 21, 6}}
	graiPartitions  = []epcPartition{{40, 12, 4, 0}, {37, 11, 7, 1}, {34, 10, 10, 2}, {30, 9, 14, 3}, {27, 8, 17, 4}, {24, 7, 20, 5}, {20, 6, 24, 6}}
	giaiPartitions  = []epcPartition{{40, 12, 42, 13}, {37, 11, 45, 14}, {34, 10, 48, 15}, {30, 9, 52, 16}, {27, 8, 55, 17}, {24, 7, 58, 18}, {20, 6, 62, 19}}
	gdtiPartitions  = []epcPartition{{40, 12, 1, 0}, {37, 11, 4, 1}, {34, 10, 7, 2}, {30, 9, 10, 3}, {27, 8, 14, 4}, {24, 7, 17, 5}, {20, 6, 21, 6}}
)

type epcScheme struct {
	name       string
	table      []epcPartition
	tail       int // bits after the reference: the serial or extension
	checkSpan  int // how the key is built, see decodeEPC
	urnPrefix  string
	hasSerial  bool
	serialName string
}

var epcSchemes = map[int]epcScheme{
	0x30: {name: "SGTIN-96", table: sgtinPartitions, tail: 38, urnPrefix: "sgtin", hasSerial: true},
	0x31: {name: "SSCC-96", table: ssccPartitions, tail: 0, urnPrefix: "sscc"},
	0x32: {name: "SGLN-96", table: sglnPartitions, tail: 41, urnPrefix: "sgln", hasSerial: true},
	0x33: {name: "GRAI-96", table: graiPartitions, tail: 38, urnPrefix: "grai", hasSerial: true},
	0x34: {name: "GIAI-96", table: giaiPartitions, tail: 0, urnPrefix: "giai"},
	0x2D: {name: "GSRN-96", table: ssccPartitions, tail: 0, urnPrefix: "gsrn"},
	0x2C: {name: "GDTI-96", table: gdtiPartitions, tail: 41, urnPrefix: "gdti", hasSerial: true},
}

func epcErr(format string, args ...any) error {
	return errorf(CodeEPCInvalid, format, args...)
}

func bitsOf(v *big.Int, from, width int) *big.Int {
	shifted := new(big.Int).Rsh(v, uint(96-from-width))
	return shifted.And(shifted, new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), uint(width)), big.NewInt(1)))
}

func zeroPad(v *big.Int, digits int) (string, bool) {
	s := v.String()
	if digits == 0 {
		return "", v.Sign() == 0
	}
	if len(s) > digits {
		return "", false
	}
	return strings.Repeat("0", digits-len(s)) + s, true
}

// DecodeEPC decodes the hexadecimal of a 96-bit EPC. Other lengths and schemes are WMS-EPC-INVALID.
func DecodeEPC(hex string) (EPC, error) {
	clean := strings.ToUpper(strings.TrimSpace(hex))
	if len(clean) != 24 {
		return EPC{}, epcErr("an EPC is 24 hexadecimal characters (96 bits), this has %d", len(clean))
	}
	v, ok := new(big.Int).SetString(clean, 16)
	if !ok {
		return EPC{}, epcErr("an EPC is hexadecimal")
	}
	header := int(bitsOf(v, 0, 8).Int64())
	sc, known := epcSchemes[header]
	if !known {
		return EPC{}, epcErr("EPC header 0x%02X is not a scheme this warehouse decodes (SGTIN, SSCC, SGLN, GRAI, GIAI, GSRN, GDTI, all 96-bit)", header)
	}
	filter := int(bitsOf(v, 8, 3).Int64())
	part := int(bitsOf(v, 11, 3).Int64())
	if part >= len(sc.table) {
		return EPC{}, epcErr("EPC partition %d is not defined for %s", part, sc.name)
	}
	p := sc.table[part]
	cp, okc := zeroPad(bitsOf(v, 14, p.cpBits), p.cpDigits)
	ref, okr := zeroPad(bitsOf(v, 14+p.cpBits, p.refBits), p.refDigits)
	if !okc || !okr {
		return EPC{}, epcErr("the company prefix or reference of the %s is out of range for its partition", sc.name)
	}
	out := EPC{Scheme: sc.name, Filter: filter, Partition: part, CompanyPrefix: cp}
	if sc.hasSerial {
		out.Serial = bitsOf(v, 14+p.cpBits+p.refBits, sc.tail).String()
	}
	check := func(body string) string { return body + strconv.Itoa(gs1Check(body)) }
	switch sc.name {
	case "SGTIN-96":
		out.Key = check(ref[:1] + cp + ref[1:])
		out.URN = fmt.Sprintf("urn:epc:id:sgtin:%s.%s.%s", cp, ref, out.Serial)
	case "SSCC-96":
		out.Key = check(ref[:1] + cp + ref[1:])
		out.URN = fmt.Sprintf("urn:epc:id:sscc:%s.%s", cp, ref)
	case "GSRN-96":
		out.Key = check(cp + ref)
		out.URN = fmt.Sprintf("urn:epc:id:gsrn:%s.%s", cp, ref)
	case "SGLN-96":
		out.Key = check(cp + ref)
		out.URN = fmt.Sprintf("urn:epc:id:sgln:%s.%s.%s", cp, ref, out.Serial)
	case "GRAI-96":
		out.Key = check("0" + cp + ref)
		out.URN = fmt.Sprintf("urn:epc:id:grai:%s.%s.%s", cp, ref, out.Serial)
	case "GDTI-96":
		out.Key = check(cp + ref)
		out.URN = fmt.Sprintf("urn:epc:id:gdti:%s.%s.%s", cp, ref, out.Serial)
	case "GIAI-96":
		out.Key = cp + ref
		out.URN = fmt.Sprintf("urn:epc:id:giai:%s.%s", cp, ref)
	}
	return out, nil
}

// EncodeEPC96 builds the 96-bit hexadecimal of an EPC from its scheme (the header byte), filter, company prefix digits,
// reference digits and serial. It exists for tests and for printing tags; a company prefix of 6 to 12 digits picks the
// partition. The reference is the item reference (with the indicator digit first for an SGTIN), the serial reference of
// an SSCC, the location reference, asset type, asset reference or document type.
func EncodeEPC96(header, filter int, companyPrefix, reference string, serial *big.Int) (string, error) {
	sc, ok := epcSchemes[header]
	if !ok {
		return "", epcErr("header 0x%02X is not a known scheme", header)
	}
	part := -1
	for i, p := range sc.table {
		if p.cpDigits == len(companyPrefix) {
			part = i
		}
	}
	if part < 0 {
		return "", epcErr("a company prefix of %d digits has no partition in %s", len(companyPrefix), sc.name)
	}
	p := sc.table[part]
	if len(reference) != p.refDigits || !allDigitsOrEmpty(reference) || !allDigits(companyPrefix) {
		return "", epcErr("the reference must be %d digits for a %d-digit company prefix", p.refDigits, p.cpDigits)
	}
	cp, _ := new(big.Int).SetString(companyPrefix, 10)
	ref := new(big.Int)
	if reference != "" {
		ref.SetString(reference, 10)
	}
	if ref.BitLen() > p.refBits {
		return "", epcErr("the reference does not fit %d bits", p.refBits)
	}
	v := big.NewInt(int64(header))
	push := func(x *big.Int, width int) {
		v.Lsh(v, uint(width))
		v.Or(v, x)
	}
	push(big.NewInt(int64(filter)), 3)
	push(big.NewInt(int64(part)), 3)
	push(cp, p.cpBits)
	push(ref, p.refBits)
	used := 14 + p.cpBits + p.refBits
	if sc.hasSerial {
		s := serial
		if s == nil {
			s = new(big.Int)
		}
		if s.Sign() < 0 || s.BitLen() > sc.tail {
			return "", epcErr("the serial does not fit %d bits", sc.tail)
		}
		push(s, sc.tail)
		used += sc.tail
	}
	v.Lsh(v, uint(96-used)) // reserved bits
	return fmt.Sprintf("%024X", v), nil
}

func allDigitsOrEmpty(s string) bool { return s == "" || allDigits(s) }
