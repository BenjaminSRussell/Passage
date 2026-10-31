package domain

import (
	"fmt"
	"slices"
	"strings"
	"time"
	_ "time/tzdata"
)

const (
	CodeMapUnknownFn   = "MIG-MAP-UNKNOWN-FN"
	CodeMapTestFail    = "MIG-MAP-TEST-FAIL"
	CodeMapEnumDefault = "MIG-MAP-ENUM-DEFAULT"
	CodePrecisionLoss  = "MAP-PRECISION-LOSS"
)

const (
	RuleCopy   = "copy"
	RuleConst  = "const"
	RuleConcat = "concat"

	FnAxDatetime     = "ax_datetime"
	FnAxNoYes        = "ax_noyes"
	FnSapDats        = "sap_dats"
	FnSapLeadZero    = "sap_lead_zero"
	maxLeadZeroWidth = 40
)

type Warning struct {
	Field  string `json:"field"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

type RowWarning struct {
	SourceKey string    `json:"sourceKey"`
	Warnings  []Warning `json:"warnings"`
}

type MappingTest struct {
	Name          string            `json:"name"`
	Input         map[string]any    `json:"input"`
	Expect        map[string]string `json:"expect,omitempty"`
	ExpectRejects []string          `json:"expectRejects,omitempty"`
}

type TestFailure struct{ Name, Reason string }

func (f FieldRule) check() *Error {
	invalid := func(detail string) *Error {
		return &Error{CodeMapInvalid, detail + " for target " + f.Target}
	}
	if f.Target == "" {
		return &Error{CodeMapInvalid, "every field needs a target"}
	}
	switch f.Type {
	case TypeString, TypeInt, TypeDecimal, TypeDate, TypeBool, TypeTimestamp:
	default:
		return invalid("unknown type")
	}
	switch f.Rule {
	case "", RuleCopy:
		if f.Source == "" || f.Value != nil || len(f.Sources) > 0 || f.Separator != "" {
			return invalid("every field needs source and target")
		}
	case RuleConst:
		if f.Value == nil || f.Source != "" || len(f.Sources) > 0 {
			return invalid("const takes a value and no source")
		}
	case RuleConcat:
		if len(f.Sources) < 2 || f.Source != "" || f.Value != nil || f.Type != TypeString {
			return invalid("concat takes at least two sources and type string")
		}
	default:
		return unknownFn(f.Rule)
	}
	if e := f.checkFormat(); e != nil {
		return e
	}
	if f.Fn != "" {
		if e := f.checkFn(); e != nil {
			return e
		}
	}
	if f.Scale != nil && (f.Type != TypeDecimal || *f.Scale < 0 || *f.Scale > maxDecimalScale) {
		return invalid("scale needs a decimal type and a value from 0 to 6")
	}
	return nil
}

func (f FieldRule) checkFn() *Error {
	want := map[string]string{FnAxDatetime: TypeTimestamp, FnAxNoYes: TypeBool, FnSapLeadZero: TypeString, FnSapDats: TypeDate, FnCodepageNormal: TypeString}
	if f.Fn == FnSapDats && f.TimeSource != "" {
		want[FnSapDats] = TypeTimestamp
	}
	wantType, known := want[f.Fn]
	if !known {
		return unknownFn(f.Fn)
	}
	if f.Type != wantType || (f.TimeSource != "" && f.Fn != FnSapDats) || (f.Width != 0) != (f.Fn == FnSapLeadZero) {
		return &Error{CodeMapInvalid, "function " + f.Fn + " has the wrong type or options for target " + f.Target}
	}
	if f.Fn == FnSapLeadZero && (f.Width < 1 || f.Width > maxLeadZeroWidth) {
		return &Error{CodeMapInvalid, "sap_lead_zero width must be 1 to 40 for target " + f.Target}
	}
	return nil
}

func unknownFn(name string) *Error {
	return &Error{CodeMapUnknownFn, "Unknown mapping function " + name + "."}
}

func (m Mapping) checkTests() *Error {
	names := map[string]bool{}
	for _, t := range m.Tests {
		if names[t.Name] {
			return &Error{CodeMapInvalid, "duplicate test name " + t.Name}
		}
		names[t.Name] = true
		if (t.Expect == nil) == (t.ExpectRejects == nil) {
			return &Error{CodeMapInvalid, "test " + t.Name + " needs exactly one of expect or expectRejects"}
		}
	}
	return nil
}

func (m Mapping) RunTests() []TestFailure {
	var failed []TestFailure
	for _, t := range m.Tests {
		out, _, rejects := m.ApplyWithWarnings(t.Input)
		codes := rejectCodes(rejects)
		if t.ExpectRejects != nil {
			if !slices.Equal(codes, t.ExpectRejects) {
				failed = append(failed, TestFailure{t.Name, fmt.Sprintf("expected rejects %v, got %v", t.ExpectRejects, codes)})
			}
			continue
		}
		if len(rejects) > 0 {
			failed = append(failed, TestFailure{t.Name, fmt.Sprintf("expected output, got rejects %v", codes)})
		} else if !sameOutput(out, t.Expect) {
			failed = append(failed, TestFailure{t.Name, "output differs from expect"})
		}
	}
	return failed
}

func rejectCodes(rejects []Reject) []string {
	codes := make([]string, 0, len(rejects))
	for _, r := range rejects {
		codes = append(codes, r.Code)
	}
	return codes
}

func sameOutput(out map[string]any, want map[string]string) bool {
	if len(out) != len(want) {
		return false
	}
	for k, v := range want {
		got, ok := out[k]
		if !ok || fmt.Sprint(got) != v {
			return false
		}
	}
	return true
}

func (f FieldRule) origin() string {
	if f.Rule == RuleConcat {
		return strings.Join(f.Sources, "+")
	}
	return f.Source
}

func (f FieldRule) text(row map[string]any) (string, bool, *Reject) {
	switch f.Rule {
	case RuleConst:
		return *f.Value, true, nil
	case RuleConcat:
		var parts []string
		for _, s := range f.Sources {
			text, present, scalar := sourceText(row[s])
			if !scalar {
				return "", false, &Reject{f.Target, CodeMapCoerce, s + " is not a scalar"}
			}
			if present {
				parts = append(parts, text)
			}
		}
		if len(parts) == 0 {
			return "", false, nil
		}
		return strings.Join(parts, f.Separator), true, nil
	}
	text, present, scalar := sourceText(row[f.Source])
	if !scalar {
		return "", false, &Reject{f.Target, CodeMapCoerce, f.Source + " is not a scalar"}
	}
	return text, present, nil
}

func badValue(f FieldRule, code, why string) *Reject {
	return &Reject{f.Target, code, f.origin() + " " + why}
}

func isNullMarker(fn, text string) bool {
	switch fn {
	case FnAxDatetime:
		return text == "1900-01-01" || text == "1900-01-01 00:00:00" || text == "1900-01-01T00:00:00"
	case FnSapDats:
		return text == "00000000" || text == "99991231"
	}
	return false
}

func (m Mapping) coerceFn(f FieldRule, text string, row map[string]any) (any, *Warning, *Reject) {
	switch f.Fn {
	case FnAxDatetime:
		t, err := time.ParseInLocation("2006-01-02 15:04:05", text, m.location())
		if err != nil {
			t, err = time.ParseInLocation("2006-01-02T15:04:05", text, m.location())
		}
		if err != nil {
			return nil, nil, badValue(f, CodeMapCoerce, "is not an AX datetime")
		}
		return t.UTC().Format(time.RFC3339), nil, nil
	case FnAxNoYes:
		switch strings.ToLower(text) {
		case "1", "yes":
			return true, nil, nil
		case "0", "no":
			return false, nil, nil
		}
		return nil, nil, badValue(f, CodeMapCoerce, "is not 0, 1, No or Yes")
	case FnSapDats:
		d, err := time.Parse("20060102", text)
		if err != nil {
			return nil, nil, badValue(f, CodeMapCoerce, "is not a SAP date")
		}
		if f.TimeSource == "" {
			return d.Format(dateLayout), nil, nil
		}
		tims, present, scalar := sourceText(row[f.TimeSource])
		if !scalar {
			return nil, nil, badValue(f, CodeMapCoerce, "time column is not a scalar")
		}
		if !present {
			tims = "000000"
		}
		tm, err := time.Parse("150405", tims)
		if err != nil {
			return nil, nil, badValue(f, CodeMapCoerce, "time column is not HHMMSS")
		}
		t := time.Date(d.Year(), d.Month(), d.Day(), tm.Hour(), tm.Minute(), tm.Second(), 0, m.location())
		return t.UTC().Format(time.RFC3339), nil, nil
	case FnCodepageNormal:
		return codepageNormalize(text), nil, nil
	case FnSapLeadZero:
		if strings.Trim(text, "0123456789") != "" {
			return text, nil, nil
		}
		if len(text) > f.Width {
			return nil, nil, badValue(f, CodeMapCoerce, "is longer than width")
		}
		return strings.Repeat("0", f.Width-len(text)) + text, nil, nil
	}
	return nil, nil, badValue(f, CodeMapInvalid, "has an unknown function")
}
