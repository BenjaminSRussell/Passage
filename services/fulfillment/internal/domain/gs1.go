package domain

import (
	"errors"
	"fmt"
	"strings"
)

// GS1-128 application identifiers the pick workflow reads (FR-28.11).
// Variable-length values end at the FNC1 group separator (0x1D) or the end of the scan.
const fnc1 = '\x1d'

var (
	ErrGS1Parse   = errors.New("gs1: cannot parse scan")
	ErrUnknownAI  = errors.New("gs1: unknown application identifier")
	ErrGS1Value   = errors.New("gs1: value is invalid for its application identifier")
	ErrGS1Missing = errors.New("gs1: required identifier missing")
)

type aiSpec struct {
	fixed int // fixed length, or 0 when variable
	max   int // maximum length for variable values
}

var aiSpecs = map[string]aiSpec{
	"00": {fixed: 18},
	"01": {fixed: 14},
	"10": {max: 20},
	"17": {fixed: 6},
	"21": {max: 20},
	"30": {max: 8},
	"37": {max: 8},
}

// Scan is the result of parsing one GS1-128 scan.
type Scan struct {
	SSCC   string
	GTIN   string
	Batch  string
	Serial string
	Expiry string // YYMMDD as printed
	Count  int    // AI 30 or 37; 0 when absent
}

type aiValue struct{ ai, value string }

// ParseGS1 parses a GS1-128 scan in either the human-readable form
// "(01)00614141000012(10)LOT7(37)5" or the raw form with FNC1 separators.
func ParseGS1(raw string) (Scan, error) {
	var pairs []aiValue
	var err error
	if strings.HasPrefix(raw, "(") {
		pairs, err = splitHumanReadable(raw)
	} else {
		pairs, err = splitRaw(raw)
	}
	if err != nil {
		return Scan{}, err
	}
	var s Scan
	seen := map[string]string{}
	for _, p := range pairs {
		// An identifier may repeat only with the same value (GS1 General
		// Specifications 4.14).
		if prev, ok := seen[p.ai]; ok && prev != p.value {
			return Scan{}, fmt.Errorf("%w: AI %s appears twice with different values", ErrGS1Value, p.ai)
		}
		seen[p.ai] = p.value
		switch p.ai {
		case "00":
			if err := ValidSSCC(p.value); err != nil {
				return Scan{}, fmt.Errorf("%w: sscc %v", ErrGS1Value, err)
			}
			s.SSCC = p.value
		case "01":
			if err := ValidCheckedDigits(p.value); err != nil {
				return Scan{}, fmt.Errorf("%w: gtin %v", ErrGS1Value, err)
			}
			s.GTIN = p.value
		case "10":
			if !inCSet82(p.value) {
				return Scan{}, fmt.Errorf("%w: batch", ErrGS1Value)
			}
			s.Batch = p.value
		case "17":
			if !validYYMMDD(p.value) {
				return Scan{}, fmt.Errorf("%w: expiry", ErrGS1Value)
			}
			s.Expiry = p.value
		case "21":
			if !inCSet82(p.value) {
				return Scan{}, fmt.Errorf("%w: serial", ErrGS1Value)
			}
			s.Serial = p.value
		case "30", "37":
			if !allDigits(p.value) {
				return Scan{}, fmt.Errorf("%w: count", ErrGS1Value)
			}
			n := 0
			for _, c := range p.value {
				n = n*10 + int(c-'0')
			}
			s.Count = n
		}
	}
	return s, nil
}

// cset82 is the GS1 AI encodable character set 82, the only characters allowed
// in the batch (10) and serial (21) values.
const cset82 = "!\"%&'()*+,-./0123456789:;<=>?ABCDEFGHIJKLMNOPQRSTUVWXYZ_abcdefghijklmnopqrstuvwxyz"

func inCSet82(v string) bool {
	for i := 0; i < len(v); i++ {
		if strings.IndexByte(cset82, v[i]) < 0 {
			return false
		}
	}
	return true
}

// validYYMMDD checks a GS1 date: month 01 to 12, day 01 to the month's last day,
// or 00 for "end of month". The century is not encoded; within the GS1 sliding
// window (current year -49 to +50) a year is a leap year exactly when YY is
// divisible by 4.
func validYYMMDD(v string) bool {
	if len(v) != 6 || !allDigits(v) {
		return false
	}
	yy := int(v[0]-'0')*10 + int(v[1]-'0')
	mm := int(v[2]-'0')*10 + int(v[3]-'0')
	dd := int(v[4]-'0')*10 + int(v[5]-'0')
	if mm < 1 || mm > 12 {
		return false
	}
	last := 31
	switch mm {
	case 2:
		last = 28
		if yy%4 == 0 {
			last = 29
		}
	case 4, 6, 9, 11:
		last = 30
	}
	return dd <= last
}

// RequireGTIN returns the GTIN or ErrGS1Missing when the scan carries none.
func (s Scan) RequireGTIN() (string, error) {
	if s.GTIN == "" {
		return "", ErrGS1Missing
	}
	return s.GTIN, nil
}

func checkAI(ai string, value string) (aiSpec, error) {
	spec, ok := aiSpecs[ai]
	if !ok {
		return aiSpec{}, fmt.Errorf("%w: %s", ErrUnknownAI, ai)
	}
	if spec.fixed > 0 && len(value) != spec.fixed {
		return aiSpec{}, fmt.Errorf("%w: AI %s needs %d characters", ErrGS1Parse, ai, spec.fixed)
	}
	if spec.fixed == 0 && (value == "" || len(value) > spec.max) {
		return aiSpec{}, fmt.Errorf("%w: AI %s needs 1 to %d characters", ErrGS1Parse, ai, spec.max)
	}
	return spec, nil
}

func splitHumanReadable(raw string) ([]aiValue, error) {
	var out []aiValue
	for pos := 0; pos < len(raw); {
		if raw[pos] != '(' {
			return nil, fmt.Errorf("%w: expected '(' at %d", ErrGS1Parse, pos)
		}
		end := strings.IndexByte(raw[pos:], ')')
		if end < 0 {
			return nil, fmt.Errorf("%w: unclosed application identifier", ErrGS1Parse)
		}
		ai := raw[pos+1 : pos+end]
		pos += end + 1
		next := strings.IndexByte(raw[pos:], '(')
		value := raw[pos:]
		if next >= 0 {
			value = raw[pos : pos+next]
		}
		pos += len(value)
		if _, err := checkAI(ai, value); err != nil {
			return nil, err
		}
		out = append(out, aiValue{ai, value})
	}
	return out, nil
}

func splitRaw(raw string) ([]aiValue, error) {
	var out []aiValue
	for pos := 0; pos < len(raw); {
		if pos+2 > len(raw) {
			return nil, fmt.Errorf("%w: truncated identifier", ErrGS1Parse)
		}
		ai := raw[pos : pos+2]
		spec, ok := aiSpecs[ai]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrUnknownAI, ai)
		}
		pos += 2
		var value string
		if spec.fixed > 0 {
			if pos+spec.fixed > len(raw) {
				return nil, fmt.Errorf("%w: AI %s truncated", ErrGS1Parse, ai)
			}
			value = raw[pos : pos+spec.fixed]
			pos += spec.fixed
		} else {
			end := strings.IndexRune(raw[pos:], fnc1)
			if end < 0 {
				end = len(raw) - pos
			}
			value = raw[pos : pos+end]
			pos += end
		}
		if _, err := checkAI(ai, value); err != nil {
			return nil, err
		}
		out = append(out, aiValue{ai, value})
		if pos < len(raw) && raw[pos] == fnc1 {
			pos++
		}
	}
	return out, nil
}
