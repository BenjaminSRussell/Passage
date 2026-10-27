package domain

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Template codes for the GS1 item and carton labels (roadmap §6.32.15). Both match a
// routable document type of the same name (BR-32.12).
const (
	TemplateItem   = "ITEM_LABEL"
	TemplateCarton = "GS1_CARTON_LABEL"
)

// fnc1 is the ZPL invocation code for the GS1 FNC1 character inside ^BC field data.
const fnc1 = ">8"

// aiSpec describes one GS1 application identifier the print service composes (BR-32.1).
type aiSpec struct {
	ai      string
	fixed   int  // fixed data length; 0 for variable-length
	max     int  // maximum length for variable-length AIs
	numeric bool // digits only
	mod10   bool // check digit verified (SSCC, GTIN)
	date    bool // YYMMDD (BR-32.10)
}

// aiTable lists the AIs used by the label templates, in the order they are composed:
// fixed-length elements first where the label allows it, so fewer FNC1 separators are needed.
var aiTable = []aiSpec{
	{ai: "00", fixed: 18, numeric: true, mod10: true},
	{ai: "01", fixed: 14, numeric: true, mod10: true},
	{ai: "10", max: 20},
	{ai: "17", fixed: 6, numeric: true, date: true},
	{ai: "21", max: 20},
	{ai: "400", max: 30},
	{ai: "420", max: 20},
}

// set82 is the GS1 AI encodable character set 82 without ">", which ZPL reserves for
// invocation codes such as FNC1 (>8). Space is not in set 82.
var set82 = regexp.MustCompile(`^[A-Za-z0-9!"%&'()*+,\-./:;<=?_]*$`)

func findAI(ai string) (aiSpec, bool) {
	for _, s := range aiTable {
		if s.ai == ai {
			return s, true
		}
	}
	return aiSpec{}, false
}

// validateValue checks one element value against its AI rules.
func validateValue(s aiSpec, value string) *Error {
	bad := func(msg string) *Error {
		return &Error{Code: CodeInvalid, Detail: fmt.Sprintf("AI %s: %s", s.ai, msg)}
	}
	if s.fixed > 0 && len(value) != s.fixed {
		return bad(fmt.Sprintf("must be %d characters", s.fixed))
	}
	if s.fixed == 0 && (value == "" || len(value) > s.max) {
		return bad(fmt.Sprintf("must be 1 to %d characters", s.max))
	}
	if s.numeric && !allDigits(value) {
		return bad("must be numeric")
	}
	if !set82.MatchString(value) {
		return bad("contains characters outside GS1 set 82")
	}
	if s.mod10 && !Mod10Valid(value) {
		return bad("check digit invalid")
	}
	if s.date {
		if err := checkYYMMDD(value); err != nil {
			return bad(err.Error())
		}
	}
	return nil
}

// checkYYMMDD validates a GS1 YYMMDD date. DD=00 means the last day of the month
// (BR-32.10); an invalid month or a day past the month end is an error. The century
// only matters for 29 February, and every year in 2000-2099 that is divisible by four
// is a leap year, so the year is read as 20YY.
func checkYYMMDD(v string) error {
	yy, _ := strconv.Atoi(v[0:2])
	mm, _ := strconv.Atoi(v[2:4])
	dd, _ := strconv.Atoi(v[4:6])
	if mm < 1 || mm > 12 {
		return fmt.Errorf("invalid month in %s", v)
	}
	lastDay := time.Date(2000+yy, time.Month(mm)+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if dd > lastDay {
		return fmt.Errorf("invalid day in %s", v)
	}
	return nil
}

// labelSpec lists the data fields a GS1 label template needs and the AI each one fills.
type labelSpec struct {
	required []string
	ais      map[string]string // AI -> field
}

var labelSpecs = map[string]labelSpec{
	TemplateItem: {
		required: []string{"gtin"},
		ais:      map[string]string{"01": "gtin", "10": "lot", "17": "expiry", "21": "serial"},
	},
	TemplateCarton: {
		required: []string{"sscc", "shipToName"},
		ais:      map[string]string{"00": "sscc", "400": "poNumber", "420": "shipToPostal"},
	},
}

// PrepareLabelData composes the GS1 element string for the item and carton labels and
// returns the template data with it under "gs1" (a caller-supplied "gs1" is replaced).
// Optional AI fields left empty are not encoded. Other templates pass through unchanged.
func PrepareLabelData(templateCode string, data map[string]string) (map[string]string, error) {
	spec, ok := labelSpecs[templateCode]
	if !ok {
		return data, nil
	}
	for _, f := range spec.required {
		if data[f] == "" {
			return nil, &Error{Code: CodeInvalid, Detail: fmt.Sprintf("field %s is required", f)}
		}
	}
	out := make(map[string]string, len(data)+1)
	for k, v := range data {
		out[k] = v
	}
	var elements []aiElement
	for _, ai := range aiTable {
		field, used := spec.ais[ai.ai]
		if !used || data[field] == "" {
			continue
		}
		if err := validateValue(ai, data[field]); err != nil {
			return nil, err
		}
		elements = append(elements, aiElement{spec: ai, value: data[field]})
	}
	out["gs1"] = joinElements(elements)
	return out, nil
}

type aiElement struct {
	spec  aiSpec
	value string
}

// joinElements concatenates AI elements, with FNC1 after every variable-length element
// that another element follows (GS1 General Specifications §7.8.5).
func joinElements(elements []aiElement) string {
	var b strings.Builder
	for i, el := range elements {
		b.WriteString(el.spec.ai)
		b.WriteString(el.value)
		if i < len(elements)-1 && el.spec.fixed == 0 {
			b.WriteString(fnc1)
		}
	}
	return b.String()
}

// aiLen returns the length of the AI at the start of s: 3 for 400 and 420, else 2.
func aiLen(s string) int {
	if len(s) >= 3 {
		if _, ok := findAI(s[:3]); ok {
			return 3
		}
	}
	return 2
}

// ParseGS1 splits a composed element string (FNC1 written as ">8") into AI -> value and
// validates every element. It guards the "gs1" template helper, so a label can only carry
// an element string the scanners will parse back (apps/web domain/gs1.ts).
func ParseGS1(s string) (map[string]string, error) {
	out := map[string]string{}
	pos := 0
	for _, seg := range strings.Split(s, fnc1) {
		if seg == "" {
			return nil, &Error{Code: CodeInvalid, Detail: fmt.Sprintf("empty element at position %d", pos)}
		}
		for seg != "" {
			unknown := &Error{Code: CodeInvalid, Detail: fmt.Sprintf("unknown GS1 application identifier at position %d", pos)}
			if len(seg) < 2 {
				return nil, unknown
			}
			n := aiLen(seg)
			ai := seg[:n]
			spec, ok := findAI(ai)
			if !ok {
				return nil, unknown
			}
			value := seg[n:]
			if spec.fixed > 0 {
				if len(value) < spec.fixed {
					return nil, &Error{Code: CodeInvalid, Detail: fmt.Sprintf("AI %s truncated", ai)}
				}
				value = value[:spec.fixed]
			}
			if err := validateValue(spec, value); err != nil {
				return nil, err
			}
			if _, dup := out[ai]; dup {
				return nil, &Error{Code: CodeInvalid, Detail: fmt.Sprintf("AI %s repeated", ai)}
			}
			out[ai] = value
			seg = seg[n+len(value):]
			pos += n + len(value)
		}
		pos += len(fnc1)
	}
	return out, nil
}

// Code 128 invocation codes inside ZPL ^BC field data (mode N).
const (
	startB  = ">:"
	startC  = ">;"
	switchB = ">6"
	switchC = ">5"
)

// gs1Code128 returns the ^BC field data for a GS1 element string (FNC1 written
// as ">8"): a start code, FNC1 (which marks the symbol as GS1-128), then the data.
// Digit runs are packed two per symbol in subset C and everything else goes in
// subset B, switching between them so the symbol stays short enough for a 4 in
// label (an SSCC, PO and postal code in subset B alone is over 4 in wide). A run
// switches to subset C when it has at least 4 digits at the end of the data or 6
// in the middle (ISO/IEC 15417 Annex E); an odd run leaves its first digit in B.
func gs1Code128(value string) string {
	tokens := splitFNC1(value)
	var b strings.Builder
	inC := digitRun(tokens, 0) >= 4 || (digitRun(tokens, 0) == len(tokens) && len(tokens)%2 == 0)
	if inC {
		b.WriteString(startC)
	} else {
		b.WriteString(startB)
	}
	b.WriteString(fnc1)
	for i := 0; i < len(tokens); {
		if tokens[i] == fnc1 {
			b.WriteString(fnc1)
			i++
			continue
		}
		run := digitRun(tokens, i)
		if inC {
			if run >= 2 {
				b.WriteString(tokens[i] + tokens[i+1])
				i += 2
				continue
			}
			b.WriteString(switchB)
			inC = false
			continue
		}
		if (run >= 4 && i+run == len(tokens)) || run >= 6 {
			if run%2 == 1 {
				b.WriteString(tokens[i])
				i++
			}
			b.WriteString(switchC)
			inC = true
			continue
		}
		b.WriteString(tokens[i])
		i++
	}
	return b.String()
}

// splitFNC1 splits an element string into single characters and FNC1 tokens.
func splitFNC1(value string) []string {
	var out []string
	for i := 0; i < len(value); {
		if strings.HasPrefix(value[i:], fnc1) {
			out = append(out, fnc1)
			i += len(fnc1)
			continue
		}
		out = append(out, value[i:i+1])
		i++
	}
	return out
}

// digitRun counts the digit tokens from i up to the next non-digit or FNC1.
func digitRun(tokens []string, i int) int {
	n := 0
	for ; i+n < len(tokens) && len(tokens[i+n]) == 1 && tokens[i+n][0] >= '0' && tokens[i+n][0] <= '9'; n++ {
	}
	return n
}
