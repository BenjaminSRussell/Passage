package domain

import (
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/unicode/norm"
)

// Source formats for legacy text that is not ISO. A field names one with `format`; the format must suit the
// field type, and a value that does not match it is MIG-MAP-COERCE, never guessed.
const (
	FmtDateISO       = "YYYY-MM-DD"
	FmtDateDMYDot    = "DD.MM.YYYY"
	FmtDateMDYSlash  = "MM/DD/YYYY"
	FmtDateDMYSlash  = "DD/MM/YYYY"
	FmtDateCompact   = "YYYYMMDD"
	FmtTimeDMYDot    = "DD.MM.YYYY HH:mm:ss"
	FmtTimeMDYSlash  = "MM/DD/YYYY HH:mm:ss"
	FmtTimeDMYSlash  = "DD/MM/YYYY HH:mm:ss"
	FmtDecimalComma  = "decimal_comma"
	FmtDecimalPoint  = "decimal_point"
	FnCodepageNormal = "codepage_normalize"
)

var dateLayouts = map[string]string{
	FmtDateISO:      "2006-01-02",
	FmtDateDMYDot:   "02.01.2006",
	FmtDateMDYSlash: "01/02/2006",
	FmtDateDMYSlash: "02/01/2006",
	FmtDateCompact:  "20060102",
}

var timestampLayouts = map[string]string{
	FmtTimeDMYDot:   "02.01.2006 15:04:05",
	FmtTimeMDYSlash: "01/02/2006 15:04:05",
	FmtTimeDMYSlash: "02/01/2006 15:04:05",
}

var (
	commaDecimalRE = regexp.MustCompile(`^[+-]?(\d{1,3}(\.\d{3})+|\d+)(,\d+)?-?$`)
	pointDecimalRE = regexp.MustCompile(`^[+-]?(\d{1,3}(,\d{3})+|\d+)(\.\d+)?-?$`)
)

func (f FieldRule) checkFormat() *Error {
	if f.Format == "" {
		return nil
	}
	ok := false
	switch f.Type {
	case TypeDate:
		_, ok = dateLayouts[f.Format]
	case TypeTimestamp:
		_, ok = timestampLayouts[f.Format]
	case TypeDecimal:
		ok = f.Format == FmtDecimalComma || f.Format == FmtDecimalPoint
	}
	if !ok || f.Fn != "" {
		return &Error{CodeMapInvalid, "format is unknown, does not suit the type or is combined with fn for target " + f.Target}
	}
	return nil
}

// coerceFormatted handles a field with an explicit source format.
func (m Mapping) coerceFormatted(f FieldRule, text string) (any, *Warning, *Reject) {
	switch f.Type {
	case TypeDate:
		t, err := time.Parse(dateLayouts[f.Format], text)
		if err != nil {
			return nil, nil, badValue(f, CodeMapCoerce, "is not a "+f.Format+" date")
		}
		return t.Format(dateLayout), nil, nil
	case TypeTimestamp:
		t, err := time.ParseInLocation(timestampLayouts[f.Format], text, m.location())
		if err != nil {
			return nil, nil, badValue(f, CodeMapCoerce, "is not a "+f.Format+" timestamp")
		}
		return t.UTC().Format(time.RFC3339), nil, nil
	}
	norm, ok := normalizeDecimalText(f.Format, text)
	if !ok {
		return nil, nil, badValue(f, CodeMapCoerce, "is not a "+f.Format+" number")
	}
	return coerceDecimal(f, norm)
}

// normalizeDecimalText rewrites a grouped or comma decimal (and SAP's trailing minus) into plain `-1234.56`.
func normalizeDecimalText(format, text string) (string, bool) {
	var group, point string
	re := pointDecimalRE
	group, point = ",", "."
	if format == FmtDecimalComma {
		re, group, point = commaDecimalRE, ".", ","
	}
	if !re.MatchString(text) {
		return "", false
	}
	neg := strings.HasSuffix(text, "-")
	text = strings.TrimSuffix(text, "-")
	if neg && (strings.HasPrefix(text, "-") || strings.HasPrefix(text, "+")) {
		return "", false
	}
	text = strings.ReplaceAll(text, group, "")
	text = strings.Replace(text, point, ".", 1)
	if neg {
		text = "-" + text
	}
	return text, true
}

var asciiPunct = strings.NewReplacer(
	"\u2018", "'", "\u2019", "'", "\u201c", `"`, "\u201d", `"`,
	"\u2013", "-", "\u2014", "-", "\u2026", "...", "\u00a0", " ", "\ufeff", "",
)

// codepageNormalize cleans text that went through a legacy code page: bytes that are not valid UTF-8 are read as
// Windows-1252 (the AX 2012 non-Unicode code page, a superset of Latin-1's printable range), the result is put in
// Unicode NFC, control characters are dropped, typographic punctuation and non-breaking spaces become ASCII, and
// runs of white space collapse.
func codepageNormalize(s string) string {
	if !utf8.ValidString(s) {
		var b strings.Builder
		b.Grow(len(s))
		for i := 0; i < len(s); {
			r, n := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && n == 1 {
				r = charmap.Windows1252.DecodeByte(s[i])
			}
			b.WriteRune(r)
			i += n
		}
		s = b.String()
	}
	s = asciiPunct.Replace(norm.NFC.String(s))
	s = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}
