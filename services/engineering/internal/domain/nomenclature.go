package domain

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	SegNumber   = "number"
	SegLetter   = "letter"
	SegConstant = "constant"
	SegDate     = "date"
)

// Nomenclature is FR-9.18 version numbering: ordered segments joined by Separator.
type Nomenclature struct {
	ID        string
	Code      string
	Separator string
	Segments  []NomenSegment
}

// NomenSegment is one part of a version code.
type NomenSegment struct {
	Type   string `json:"type"`
	Width  int    `json:"width,omitempty"`
	Start  int    `json:"start,omitempty"`
	SkipIO bool   `json:"skipIo,omitempty"`
	Format string `json:"format,omitempty"`
	Text   string `json:"text,omitempty"`
}

func (n Nomenclature) Validate() error {
	if n.Code == "" || len(n.Code) > 20 {
		return errf("COM-0001", "nomenclature code must be 1 to 20 characters")
	}
	if len(n.Separator) > 3 {
		return errf("COM-0001", "separator must be at most 3 characters")
	}
	if len(n.Segments) == 0 {
		return errf("COM-0001", "nomenclature needs at least one segment")
	}
	for _, s := range n.Segments {
		switch s.Type {
		case SegNumber:
			if s.Width < 1 || s.Width > 8 {
				return errf("COM-0001", "number width must be 1 to 8")
			}
		case SegLetter:
		case SegConstant:
			if s.Text == "" {
				return errf("COM-0001", "constant segment needs text")
			}
		case SegDate:
			if s.Format != "yyyyMMdd" && s.Format != "yyMM" {
				return errf("COM-0001", "date format must be yyyyMMdd or yyMM")
			}
		default:
			return errf("COM-0001", "unknown segment type %s", s.Type)
		}
	}
	return nil
}

// Next returns the next version code after last (empty last uses each segment's start).
// Date segments use releaseDate in that time's location (the engineering organization's zone).
func (n Nomenclature) Next(last string, releaseDate time.Time) (string, error) {
	if err := n.Validate(); err != nil {
		return "", err
	}
	parts, err := n.parse(last)
	if err != nil {
		return "", err
	}
	incremented := false
	out := make([]string, len(n.Segments))
	for i := len(n.Segments) - 1; i >= 0; i-- {
		s := n.Segments[i]
		switch s.Type {
		case SegConstant:
			out[i] = s.Text
		case SegDate:
			out[i] = formatNomenDate(s.Format, releaseDate)
		case SegNumber:
			next, overflow := nextNumber(parts[i], s, !incremented)
			if overflow {
				return "", errf(CodeNomenOverflow, "Version numbering exhausted for this nomenclature.")
			}
			out[i] = next
			if !incremented {
				incremented = true
			}
		case SegLetter:
			next := nextLetter(parts[i], s.SkipIO, !incremented)
			out[i] = next
			if !incremented {
				incremented = true
			}
		}
	}
	return strings.Join(out, n.Separator), nil
}

func (n Nomenclature) parse(last string) ([]string, error) {
	parts := make([]string, len(n.Segments))
	if last == "" {
		return parts, nil
	}
	rest := last
	for i, s := range n.Segments {
		if i > 0 && n.Separator != "" {
			if !strings.HasPrefix(rest, n.Separator) && rest != "" {
				return nil, errf("COM-0001", "version code %s does not match nomenclature %s", last, n.Code)
			}
			rest = strings.TrimPrefix(rest, n.Separator)
		}
		switch s.Type {
		case SegConstant:
			if !strings.HasPrefix(rest, s.Text) {
				return nil, errf("COM-0001", "version code %s does not match nomenclature %s", last, n.Code)
			}
			parts[i] = s.Text
			rest = rest[len(s.Text):]
		case SegNumber:
			w := s.Width
			if len(rest) < w {
				return nil, errf("COM-0001", "version code %s does not match nomenclature %s", last, n.Code)
			}
			parts[i] = rest[:w]
			rest = rest[w:]
		case SegDate:
			w := 8
			if s.Format == "yyMM" {
				w = 4
			}
			if len(rest) < w {
				return nil, errf("COM-0001", "version code %s does not match nomenclature %s", last, n.Code)
			}
			parts[i] = rest[:w]
			rest = rest[w:]
		case SegLetter:
			j := 0
			for j < len(rest) && unicode.IsLetter(rune(rest[j])) {
				j++
			}
			if j == 0 {
				return nil, errf("COM-0001", "version code %s does not match nomenclature %s", last, n.Code)
			}
			parts[i] = rest[:j]
			rest = rest[j:]
		}
	}
	if rest != "" {
		return nil, errf("COM-0001", "version code %s does not match nomenclature %s", last, n.Code)
	}
	return parts, nil
}

func nextNumber(cur string, s NomenSegment, increment bool) (string, bool) {
	start := s.Start
	if start <= 0 {
		start = 1
	}
	n := start - 1
	if cur != "" {
		parsed, err := strconv.Atoi(cur)
		if err != nil {
			n = start - 1
		} else {
			n = parsed
		}
	}
	if increment {
		n++
	} else if cur == "" {
		n = start
	}
	max := 1
	for i := 0; i < s.Width; i++ {
		max *= 10
	}
	if n >= max {
		return "", true
	}
	return fmt.Sprintf("%0*d", s.Width, n), false
}

func nextLetter(cur string, skipIO, increment bool) string {
	if cur == "" {
		return "A"
	}
	if !increment {
		return cur
	}
	runes := []rune(strings.ToUpper(cur))
	for i := len(runes) - 1; i >= 0; i-- {
		runes[i]++
		if skipIO && (runes[i] == 'I' || runes[i] == 'O') {
			runes[i]++
			if runes[i] == 'O' {
				runes[i]++
			}
		}
		if runes[i] <= 'Z' {
			return string(runes)
		}
		runes[i] = 'A'
	}
	return "A" + string(runes)
}

func formatNomenDate(format string, t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}
	if format == "yyMM" {
		return t.Format("0601")
	}
	return t.Format("20060102")
}
