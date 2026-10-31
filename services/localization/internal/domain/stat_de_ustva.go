package domain

import (
	"bytes"
	"encoding/xml"
	"regexp"
	"sort"
	"strings"

	"github.com/shopspring/decimal"
)

// DE_UStVA: the Nutzdaten part of an ELSTER Umsatzsteuer-Voranmeldung
// (<Anmeldungssteuern art="UStVA">). ELSTER's XSD ships only inside ERiC,
// which needs a registration, so the rules below encode the published form
// (Vordruck USt 1 A) and the ELSTER conventions: Kennzahl tax bases in whole
// euros, tax amounts with cents and a period as the decimal separator,
// Zeitraum 01..12 (month) or 41..44 (quarter), the 13-digit ELSTER
// Steuernummer, Kz35 and Kz36 together, and Kz83 equal to the advance payment
// the return computes. A Kennzahl outside the supported set is reported, not
// guessed.

var (
	ustvaZeitraum = regexp.MustCompile(`^(0[1-9]|1[0-2]|4[1-4])$`)
	ustvaYear     = regexp.MustCompile(`^20\d{2}$`)
	ustvaStNr     = regexp.MustCompile(`^\d{13}$`)
)

// First two digits of the Bundesfinanzamtsnummer in the 13-digit ELSTER
// Steuernummer, per Land.
var ustvaLandPrefix = map[string]bool{
	"28": true,                         // Baden-Württemberg
	"91": true, "92": true, "93": true, // Bayern
	"11": true, "30": true, "24": true, "22": true, "26": true, "40": true, "23": true,
	"51": true, "52": true, "53": true, // Nordrhein-Westfalen
	"27": true, "10": true, "32": true, "31": true, "21": true, "41": true,
}

// Supported Kennzahlen: tax bases in whole euros, and tax amounts in cents.
var (
	ustvaBaseKz = map[string]bool{"Kz21": true, "Kz35": true, "Kz41": true, "Kz43": true, "Kz45": true, "Kz48": true, "Kz81": true, "Kz86": true}
	ustvaTaxKz  = map[string]bool{"Kz36": true, "Kz66": true, "Kz83": true}
)

type ustvaNode struct {
	XMLName xml.Name
	Value   string `xml:",chardata"`
}

type ustvaDoc struct {
	XMLName          xml.Name `xml:"Anmeldungssteuern"`
	Art              string   `xml:"art,attr"`
	Version          string   `xml:"version,attr"`
	Erstellungsdatum string   `xml:"Erstellungsdatum"`
	Steuerfall       struct {
		Anmeldung *struct {
			Nodes []ustvaNode `xml:",any"`
		} `xml:"Umsatzsteuervoranmeldung"`
	} `xml:"Steuerfall"`
}

// ValidateUStVA checks a UStVA against the rules above and returns every failure.
func ValidateUStVA(raw []byte) []FieldError {
	var errs fieldErrs
	var doc ustvaDoc
	if err := xml.NewDecoder(bytes.NewReader(raw)).Decode(&doc); err != nil {
		errs.add("xml", "%s", err)
		return errs
	}
	if doc.Art != "UStVA" {
		errs.add("Anmeldungssteuern/@art", "must be UStVA")
	}
	if _, ok := validDate("20060102", doc.Erstellungsdatum); !ok {
		errs.add("Erstellungsdatum", "must be a date YYYYMMDD")
	}
	if doc.Steuerfall.Anmeldung == nil {
		errs.add("Umsatzsteuervoranmeldung", "is required under Steuerfall")
		return errs
	}
	vals := map[string]string{}
	for _, n := range doc.Steuerfall.Anmeldung.Nodes {
		name := n.XMLName.Local
		if _, dup := vals[name]; dup {
			errs.add(name, "appears more than once")
		}
		vals[name] = strings.TrimSpace(n.Value)
	}
	if y := vals["Jahr"]; !ustvaYear.MatchString(y) {
		errs.add("Jahr", "%q must be a four-digit year", y)
	} else if !strings.HasPrefix(doc.Version, y) {
		errs.add("Anmeldungssteuern/@version", "%q must be the schema version of the year %s", doc.Version, y)
	}
	if z := vals["Zeitraum"]; !ustvaZeitraum.MatchString(z) {
		errs.add("Zeitraum", "%q must be 01 to 12 (month) or 41 to 44 (quarter)", z)
	}
	if s := vals["Steuernummer"]; !ustvaStNr.MatchString(s) || !ustvaLandPrefix[s[:2]] || s[4] != '0' {
		errs.add("Steuernummer", "%q must be the 13-digit ELSTER format (Land prefix, fifth digit 0)", s)
	}
	kz := map[string]decimal.Decimal{}
	names := make([]string, 0, len(vals))
	for name := range vals {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		v := vals[name]
		switch {
		case name == "Jahr" || name == "Zeitraum" || name == "Steuernummer":
		case ustvaBaseKz[name]:
			if d, ok := parseAmount(v, wholeNumber); ok {
				kz[name] = d
			} else {
				errs.add(name, "%q must be whole euros", v)
			}
		case ustvaTaxKz[name]:
			if d, ok := parseAmount(v, twoDecimals); ok {
				kz[name] = d
			} else {
				errs.add(name, "%q must be euros with at most two decimals and a period separator", v)
			}
		case strings.HasPrefix(name, "Kz"):
			errs.add(name, "Kennzahl not supported by this validator")
		default:
			errs.add(name, "unknown element")
		}
	}
	_, has35 := vals["Kz35"]
	_, has36 := vals["Kz36"]
	if has35 != has36 {
		errs.add("Kz35", "Kz35 and Kz36 are transmitted together")
	}
	if _, ok := vals["Kz83"]; !ok {
		errs.add("Kz83", "is required")
		return errs
	}
	if len(errs) > 0 {
		return errs
	}
	due := kz["Kz81"].Mul(decimal.RequireFromString("0.19")).
		Add(kz["Kz86"].Mul(decimal.RequireFromString("0.07"))).
		Add(kz["Kz36"]).Sub(kz["Kz66"])
	if !kz["Kz83"].Equal(due) {
		errs.add("Kz83", "is %s, the return computes %s (19%% of Kz81 + 7%% of Kz86 + Kz36 - Kz66)", kz["Kz83"], due.StringFixed(2))
	}
	return errs
}
