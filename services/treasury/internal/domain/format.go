package domain

import (
	"bytes"
	"regexp"
)

var (
	mt940Start = regexp.MustCompile(`(?m)^:20:`)
	utf8BOM    = []byte{0xEF, 0xBB, 0xBF}
)

// ParseStatements detects the statement format from the content and parses it
// (FR-36.2): an XML document is camt.053, a leading "01," file header is BAI2,
// and a :20: tag at the start of a line is MT940. Anything else is TRS-0493.
// A UTF-8 byte order mark is dropped; line numbers in errors stay those of the file.
func ParseStatements(data []byte) ([]Statement, error) {
	body := bytes.TrimPrefix(data, utf8BOM)
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	switch {
	case bytes.HasPrefix(trimmed, []byte("<")):
		return ParseCAMT053(data)
	case bytes.HasPrefix(trimmed, []byte("01,")):
		return ParseBAI2(body)
	case mt940Start.Match(trimmed):
		return ParseMT940(body)
	default:
		return nil, formatError(0, "file is not camt.053, BAI2 or MT940")
	}
}
