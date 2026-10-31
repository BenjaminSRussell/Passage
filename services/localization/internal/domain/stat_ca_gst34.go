package domain

import (
	"bytes"
	"encoding/xml"
	"regexp"
	"strings"

	"github.com/shopspring/decimal"
)

// CA_GST34: the lines of the CRA GST/HST return (form GST34) for a NETFILE
// filing. CRA publishes no public XML schema for NETFILE, so the rules encode
// the form: a business number of nine digits passing its Luhn check digit with
// the RT program account (RT plus four digits), a reporting period whose end
// is not before its start and at most one year long, line 101 in whole
// dollars, lines 103 to 109 with cents, 105 = 103 + 104, 108 = 106 + 107 and
// 109 = 105 - 108 (negative is a refund). Lines past 109 are reported as not
// covered.

var gst34BN = regexp.MustCompile(`^(\d{9})RT\d{4}$`)

var gst34Lines = []string{"line101", "line103", "line104", "line105", "line106", "line107", "line108", "line109"}

type gst34Doc struct {
	XMLName         xml.Name `xml:"GST34"`
	BusinessNumber  string   `xml:"businessNumber"`
	ReportingPeriod *struct {
		Start string `xml:"start,attr"`
		End   string `xml:"end,attr"`
	} `xml:"reportingPeriod"`
	Nodes []ustvaNode `xml:",any"`
}

// ValidateGST34 checks a GST34 return against the rules above; it returns every failure.
func ValidateGST34(raw []byte) []FieldError {
	var errs fieldErrs
	var doc gst34Doc
	if err := xml.NewDecoder(bytes.NewReader(raw)).Decode(&doc); err != nil {
		errs.add("xml", "%s", err)
		return errs
	}
	if m := gst34BN.FindStringSubmatch(doc.BusinessNumber); m == nil || !luhnValid(m[1]) {
		errs.add("businessNumber", "%q must be a nine-digit BN with a valid check digit and an RT program account", doc.BusinessNumber)
	}
	if p := doc.ReportingPeriod; p == nil {
		errs.add("reportingPeriod", "is required")
	} else {
		start, okS := validDate("2006-01-02", p.Start)
		end, okE := validDate("2006-01-02", p.End)
		switch {
		case !okS || !okE:
			errs.add("reportingPeriod", "start and end must be dates YYYY-MM-DD")
		case end.Before(start):
			errs.add("reportingPeriod", "ends before it starts")
		case !end.Before(start.AddDate(1, 0, 0)):
			errs.add("reportingPeriod", "is longer than one year")
		}
	}
	known := map[string]bool{}
	for _, l := range gst34Lines {
		known[l] = true
	}
	v := map[string]decimal.Decimal{}
	seen := map[string]bool{}
	for _, n := range doc.Nodes {
		name, text := n.XMLName.Local, strings.TrimSpace(n.Value)
		switch {
		case !known[name]:
			errs.add(name, "line not covered by this validator (101 to 109)")
			continue
		case seen[name]:
			errs.add(name, "appears more than once")
		}
		seen[name] = true
		pattern, what := twoDecimals, "dollars with at most two decimals"
		if name == "line101" {
			pattern, what = wholeNumber, "whole dollars"
		}
		d, ok := parseAmount(text, pattern)
		if !ok {
			errs.add(name, "%q must be %s", text, what)
			continue
		}
		v[name] = d
	}
	complete := true
	for _, l := range gst34Lines {
		if !seen[l] {
			errs.add(l, "is required")
			complete = false
		}
	}
	if !complete || len(v) != len(gst34Lines) {
		return errs
	}
	if !v["line105"].Equal(v["line103"].Add(v["line104"])) {
		errs.add("line105", "must equal line 103 + line 104")
	}
	if !v["line108"].Equal(v["line106"].Add(v["line107"])) {
		errs.add("line108", "must equal line 106 + line 107")
	}
	if !v["line109"].Equal(v["line105"].Sub(v["line108"])) {
		errs.add("line109", "must equal line 105 - line 108")
	}
	return errs
}
