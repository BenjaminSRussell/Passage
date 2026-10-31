package domain

import (
	"path/filepath"
	"strings"
	"testing"
)

// Adversarial mutations of each fixture: every case changes one thing a
// regulator rejects and names the field the validator must report. The
// "accept" cases are legal variations the validator must not reject.

type statCase struct {
	name   string
	mutate func(t *testing.T, raw string) string
	field  string // substring of the expected FieldError.Field; "" means the result must be valid
	name2  string // file name override (FEC)
}

func replaceOnce(old, repl string) func(*testing.T, string) string {
	return func(t *testing.T, raw string) string {
		t.Helper()
		if !strings.Contains(raw, old) {
			t.Fatalf("fixture lacks %q", old)
		}
		return strings.Replace(raw, old, repl, 1)
	}
}

func chain(fs ...func(*testing.T, string) string) func(*testing.T, string) string {
	return func(t *testing.T, raw string) string {
		for _, f := range fs {
			raw = f(t, raw)
		}
		return raw
	}
}

func runStatCases(t *testing.T, pack string, cases []statCase) {
	t.Helper()
	fx := statutoryFixtures[pack]
	base := string(readFixture(t, fx.file))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name := filepath.Base(fx.file)
			if tc.name2 != "" {
				name = tc.name2
			}
			raw := base
			if tc.mutate != nil {
				raw = tc.mutate(t, base)
			}
			errs, err := ValidateStatutory(fx.report, name, []byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			if tc.field == "" {
				if len(errs) != 0 {
					t.Fatalf("legal variation rejected: %+v", errs)
				}
				return
			}
			for _, e := range errs {
				if strings.Contains(e.Field, tc.field) {
					return
				}
			}
			t.Fatalf("no error on %q: %+v", tc.field, errs)
		})
	}
}

// setFIRE overwrites Pub 1220 positions from.. of record rec (1-based) in a CR/LF file.
func setFIRE(rec, from int, value string) func(*testing.T, string) string {
	return func(t *testing.T, raw string) string {
		t.Helper()
		recs := strings.Split(raw, "\r\n")
		r := recs[rec-1]
		recs[rec-1] = r[:from-1] + value + r[from-1+len(value):]
		return strings.Join(recs, "\r\n")
	}
}

func TestFIRE1099NEC_Mutations(t *testing.T) {
	runStatCases(t, "US", []statCase{
		{name: "fixture as shipped", field: ""},
		{name: "C control total 1 off by one cent", mutate: setFIRE(6, 16, "000000000009985076"), field: "record 6 (C) 16-33"},
		{name: "C control total 4 dropped", mutate: setFIRE(6, 70, "000000000000000000"), field: "record 6 (C) 70-87"},
		{name: "C payee count wrong", mutate: setFIRE(6, 2, "00000002"), field: "record 6 (C) 2-9"},
		{name: "T payee count wrong", mutate: setFIRE(1, 296, "00000004"), field: "record 1 (T) 296-303"},
		{name: "sequence number skipped", mutate: setFIRE(4, 500, "00000005"), field: "record 4 (B) 500-507"},
		{name: "amount in a code the A record does not declare", mutate: setFIRE(3, 67, "000000010000"), field: "record 3 (B) 67-78"},
		{name: "negative NEC amount", mutate: setFIRE(3, 55, "-00000125000"), field: "record 3 (B) 55-66"},
		{name: "lower-case payee name", mutate: setFIRE(3, 288, "Rivera"), field: "record 3 (B) 1-750"},
		{name: "type of return 1099-MISC", mutate: setFIRE(2, 26, "A "), field: "record 2 (A) 26-27"},
		{name: "amount codes not ascending", mutate: setFIRE(2, 28, "41"), field: "record 2 (A) 28-45"},
		{name: "amount code not on 1099-NEC", mutate: setFIRE(2, 28, "17"), field: "record 2 (A) 28-45"},
		{name: "payee TIN one repeated digit", mutate: setFIRE(4, 12, "111111111"), field: "record 4 (B) 12-20"},
		{name: "payee TIN with a hyphen", mutate: setFIRE(4, 12, "87-043192"), field: "record 4 (B) 12-20"},
		{name: "payee ZIP of four digits", mutate: setFIRE(5, 490, "4321     "), field: "record 5 (B) 490-498"},
		{name: "blank field filled", mutate: setFIRE(3, 45, "X"), field: "record 3 (B) 45-54"},
		{name: "payment year differs from T", mutate: setFIRE(4, 2, "2024"), field: "record 4 (B) 2-5"},
		{name: "payee name missing", mutate: setFIRE(5, 288, strings.Repeat(" ", 40)), field: "record 5 (B) 288-327"},
		{name: "F number of A records wrong", mutate: setFIRE(7, 2, "00000002"), field: "record 7 (F) 2-9"},
		{name: "F zero field not zeros", mutate: setFIRE(7, 10, "1"), field: "record 7 (F) 10-30"},
		{name: "vendor indicator missing", mutate: setFIRE(1, 518, " "), field: "record 1 (T) 518-518"},
		{name: "record 749 positions", mutate: func(t *testing.T, raw string) string {
			return strings.Replace(raw, "\r\n", " \r\n", 1)
		}, field: "file"},
		{name: "LF line ends", mutate: func(t *testing.T, raw string) string {
			return strings.ReplaceAll(raw, "\r\n", "\n")
		}, field: "file"},
		{name: "C record missing", mutate: func(t *testing.T, raw string) string {
			recs := strings.Split(raw, "\r\n")
			return strings.Join(append(recs[:5:5], recs[6:]...), "\r\n")
		}, field: "record 6 (F) 1-1"},
		{name: "F record with all-zero A count is legal", mutate: setFIRE(7, 2, "00000000"), field: ""},
		{name: "F payee total left blank is legal", mutate: setFIRE(7, 50, "        "), field: ""},
	})
}

func TestUStVA_Mutations(t *testing.T) {
	runStatCases(t, "DE", []statCase{
		{name: "fixture as shipped", field: ""},
		{name: "Kz83 off by one cent", mutate: replaceOnce("<Kz83>4674.52</Kz83>", "<Kz83>4674.53</Kz83>"), field: "Kz83"},
		{name: "Kz83 missing", mutate: replaceOnce("<Kz83>4674.52</Kz83>", ""), field: "Kz83"},
		{name: "Zeitraum 13", mutate: replaceOnce("<Zeitraum>01</Zeitraum>", "<Zeitraum>13</Zeitraum>"), field: "Zeitraum"},
		{name: "Zeitraum 45", mutate: replaceOnce("<Zeitraum>01</Zeitraum>", "<Zeitraum>45</Zeitraum>"), field: "Zeitraum"},
		{name: "quarter 41 is legal", mutate: replaceOnce("<Zeitraum>01</Zeitraum>", "<Zeitraum>41</Zeitraum>"), field: ""},
		{name: "Steuernummer of 12 digits", mutate: replaceOnce("9198011310010", "919801131001"), field: "Steuernummer"},
		{name: "Steuernummer with an unknown Land prefix", mutate: replaceOnce("9198011310010", "9998011310010"), field: "Steuernummer"},
		{name: "Steuernummer fifth digit not 0", mutate: replaceOnce("9198011310010", "9198111310010"), field: "Steuernummer"},
		{name: "tax base with cents", mutate: replaceOnce("<Kz81>48250</Kz81>", "<Kz81>48250.50</Kz81>"), field: "Kz81"},
		{name: "comma decimal separator", mutate: replaceOnce("<Kz66>4711.38</Kz66>", "<Kz66>4711,38</Kz66>"), field: "Kz66"},
		{name: "three decimals", mutate: replaceOnce("<Kz66>4711.38</Kz66>", "<Kz66>4711.380</Kz66>"), field: "Kz66"},
		{name: "Kz35 without Kz36", mutate: replaceOnce("<Kz41>", "<Kz35>100</Kz35><Kz41>"), field: "Kz35"},
		{name: "Kz35 with Kz36 adds the tax to Kz83", mutate: chain(
			replaceOnce("<Kz41>", "<Kz35>100</Kz35><Kz36>10.70</Kz36><Kz41>"),
			replaceOnce("<Kz83>4674.52</Kz83>", "<Kz83>4685.22</Kz83>")), field: ""},
		{name: "unsupported Kennzahl", mutate: replaceOnce("<Kz41>", "<Kz10>1</Kz10><Kz41>"), field: "Kz10"},
		{name: "duplicate Kennzahl", mutate: replaceOnce("<Kz86>3120</Kz86>", "<Kz86>3120</Kz86><Kz86>3120</Kz86>"), field: "Kz86"},
		{name: "wrong art", mutate: replaceOnce(`art="UStVA"`, `art="LStA"`), field: "@art"},
		{name: "schema version of another year", mutate: replaceOnce(`version="2025"`, `version="2024"`), field: "@version"},
		{name: "repayment (negative Kz83) is legal", mutate: chain(
			replaceOnce("<Kz66>4711.38</Kz66>", "<Kz66>10000.00</Kz66>"),
			replaceOnce("<Kz83>4674.52</Kz83>", "<Kz83>-614.10</Kz83>")), field: ""},
		{name: "not XML", mutate: func(*testing.T, string) string { return "Kz81=48250" }, field: "xml"},
	})
}

func TestFEC_Mutations(t *testing.T) {
	runStatCases(t, "FR", []statCase{
		{name: "fixture as shipped", field: ""},
		{name: "entry not balanced", mutate: replaceOnce("|0,00|200,00|", "|0,00|200,01|"), field: "EcritureNum"},
		{name: "header column renamed", mutate: replaceOnce("|EcritureLib|", "|Libelle|"), field: "header"},
		{name: "a column missing on a line", mutate: replaceOnce("|Facture F2025-001|0,00|1000,00|", "|0,00|1000,00|"), field: "line 3"},
		{name: "impossible date", mutate: replaceOnce("VE000001|20250115|706000", "VE000001|20250230|706000"), field: "EcritureDate"},
		{name: "dot decimal separator", mutate: replaceOnce("|1200,00|0,00|A|", "|1200.00|0,00|A|"), field: "Debit"},
		{name: "debit and credit on one line", mutate: replaceOnce("|500,00|0,00|", "|500,00|1,00|"), field: "Debit"},
		{name: "currency amount without currency", mutate: replaceOnce("|540,00|USD", "|540,00|"), field: "Montantdevise"},
		{name: "lettering without date", mutate: replaceOnce("|B|20250205|20250121|", "|B||20250121|"), field: "EcritureLet"},
		{name: "account outside the chart", mutate: replaceOnce("|607000|", "|907000|"), field: "CompteNum"},
		{name: "lines out of validation order", mutate: replaceOnce("|20250211||\nBQ|Banque|BQ000002|20250210|411000", "|20250101||\nBQ|Banque|BQ000002|20250210|411000"), field: "ValidDate"},
		{name: "entry spans two journals", mutate: replaceOnce("VE|Ventes|VE000001|20250115|445710", "OD|Ventes|VE000001|20250115|445710"), field: "EcritureNum"},
		{name: "required label empty", mutate: replaceOnce("|Clients|C0001|DUPONT SARL|F2025-001|", "||C0001|DUPONT SARL|F2025-001|"), field: "CompteLib"},
		{name: "SIREN check digit wrong", name2: "732829321FEC20251231.txt", field: "fileName"},
		{name: "file name not SirenFECdate", name2: "fec-2025.txt", field: "fileName"},
		{name: "entry after the closing date", name2: "732829320FEC20250131.txt", field: "EcritureDate"},
		{name: "tab separator is legal", mutate: func(t *testing.T, raw string) string { return strings.ReplaceAll(raw, "|", "\t") }, field: ""},
		{name: "CR/LF line ends are legal", mutate: func(t *testing.T, raw string) string { return strings.ReplaceAll(raw, "\n", "\r\n") }, field: ""},
	})
}

func TestGSTR1_Mutations(t *testing.T) {
	runStatCases(t, "IN", []statCase{
		{name: "fixture as shipped", field: ""},
		{name: "IGST on an intra-state supply", mutate: replaceOnce(`"rt": 5, "iamt": 0,`, `"rt": 5, "iamt": 1,`), field: "iamt"},
		{name: "CGST and SGST differ", mutate: replaceOnce(`"camt": 300.00, "samt": 300.00, "csamt": 0 } }`, `"camt": 300.00, "samt": 299.00, "csamt": 0 } }`), field: "samt"},
		{name: "IGST short by more than a rupee", mutate: replaceOnce(`"rt": 18, "iamt": 18000.00, "camt": 0, "samt": 0, "csamt": 0 } }`, `"rt": 18, "iamt": 17998.00, "camt": 0, "samt": 0, "csamt": 0 } }`), field: "iamt"},
		{name: "IGST within a rupee is legal", mutate: chain(
			replaceOnce(`"rt": 18, "iamt": 18000.00, "camt": 0, "samt": 0, "csamt": 0 } }`, `"rt": 18, "iamt": 18000.80, "camt": 0, "samt": 0, "csamt": 0 } }`),
			replaceOnce(`"rt": 18, "txval": 100000.00, "iamt": 18000.00`, `"rt": 18, "txval": 100000.00, "iamt": 18000.80`)), field: ""},
		{name: "CGST and SGST on an inter-state supply", mutate: replaceOnce(`"iamt": 18000.00, "camt": 0, "samt": 0, "csamt": 0 } }`, `"iamt": 18000.00, "camt": 1, "samt": 1, "csamt": 0 } }`), field: "b2b[0].inv[0].itms[0]"},
		{name: "recipient GSTIN check character wrong", mutate: replaceOnce("29AAACR5055K1Z3", "29AAACR5055K1Z4"), field: "b2b[0].ctin"},
		{name: "supplier GSTIN check character wrong", mutate: replaceOnce(`"gstin": "27AAPFU0939F1ZV"`, `"gstin": "27AAPFU0939F1ZW"`), field: "gstin"},
		{name: "recipient is the supplier", mutate: replaceOnce(`"ctin": "27AABCS1429B1ZU"`, `"ctin": "27AAPFU0939F1ZV"`), field: "b2b[1].ctin"},
		{name: "rate that is not a slab", mutate: replaceOnce(`"rt": 12, "iamt"`, `"rt": 17, "iamt"`), field: "rt"},
		{name: "invoice dated after the period", mutate: replaceOnce(`"19-08-2025"`, `"01-09-2025"`), field: "idt"},
		{name: "invoice date in yyyy-mm-dd", mutate: replaceOnce(`"19-08-2025"`, `"2025-08-19"`), field: "idt"},
		{name: "period month 13", mutate: replaceOnce(`"fp": "082025"`, `"fp": "132025"`), field: "fp"},
		{name: "duplicate invoice number", mutate: replaceOnce("INV/25-26/0102", "INV/25-26/0101"), field: "inum"},
		{name: "invoice number of 17 characters", mutate: replaceOnce("INV/25-26/0102", "INV/25-26/0102XYZ"), field: "inum"},
		{name: "place of supply not a state", mutate: replaceOnce(`"pos": "29"`, `"pos": "39"`), field: "pos"},
		{name: "SEZ supply with CGST", mutate: replaceOnce(`"pos": "27",
          "rchrg": "N",
          "inv_typ": "R"`, `"pos": "27",
          "rchrg": "N",
          "inv_typ": "SEZWP"`), field: "b2b[1].inv[0].itms[0]"},
		{name: "HSN summary does not reconcile", mutate: replaceOnce(`"qty": 100, "rt": 5, "txval": 5000.00`, `"qty": 100, "rt": 5, "txval": 4000.00`), field: "hsn.data"},
		{name: "HSN code of five digits", mutate: replaceOnce(`"hsn_sc": "8471"`, `"hsn_sc": "84713"`), field: "hsn_sc"},
		{name: "section the validator does not cover", mutate: replaceOnce(`"fp": "082025",`, `"fp": "082025", "b2cs": [],`), field: "b2cs"},
		{name: "reverse charge flag not Y or N", mutate: replaceOnce(`"rchrg": "N"`, `"rchrg": "X"`), field: "rchrg"},
		{name: "invoice value below taxable value", mutate: replaceOnce(`"val": 10850.00`, `"val": 9000.00`), field: "val"},
	})
}

func TestCFDI40_Mutations(t *testing.T) {
	runStatCases(t, "MX", []statCase{
		{name: "fixture as shipped", field: ""},
		{name: "Total off by one cent", mutate: replaceOnce(`Total="40311.74"`, `Total="40311.75"`), field: "Total"},
		{name: "SubTotal not the sum of lines", mutate: replaceOnce(`SubTotal="34751.50"`, `SubTotal="34751.00"`), field: "SubTotal"},
		{name: "line Importe not Cantidad x ValorUnitario", mutate: replaceOnce(`Importe="3751.50" ObjetoImp`, `Importe="3750.00" ObjetoImp`), field: "Concepto[1]/Importe"},
		{name: "line IVA not Base x rate", mutate: replaceOnce(`Importe="600.24"`, `Importe="600.00"`), field: "Concepto[1]/Traslado[0]/Importe"},
		{name: "tax summary differs from the lines", mutate: replaceOnce(`Base="34751.50"`, `Base="34750.00"`), field: "Impuestos/Traslados"},
		{name: "total transferred tax wrong", mutate: replaceOnce(`TotalImpuestosTrasladados="5560.24"`, `TotalImpuestosTrasladados="5560.00"`), field: "TotalImpuestosTrasladados"},
		{name: "19-digit certificate number", mutate: replaceOnce(`NoCertificado="30001000000500003416"`, `NoCertificado="3000100000050000341"`), field: "NoCertificado"},
		{name: "version 3.3", mutate: replaceOnce(`Version="4.0"`, `Version="3.3"`), field: "Version"},
		{name: "receiver RFC malformed", mutate: replaceOnce(`Rfc="XOJI740919U48"`, `Rfc="XOJI741319U48"`), field: "Receptor/Rfc"},
		{name: "date with a time zone", mutate: replaceOnce(`Fecha="2025-08-05T10:15:00"`, `Fecha="2025-08-05T10:15:00Z"`), field: "Fecha"},
		{name: "rate without six decimals", mutate: replaceOnce(`TasaOCuota="0.160000" Importe="600.24"`, `TasaOCuota="0.16" Importe="600.24"`), field: "TasaOCuota"},
		{name: "subject to tax without line taxes", mutate: func(t *testing.T, raw string) string {
			start := strings.Index(raw, `<cfdi:Impuestos>`)
			end := strings.Index(raw, `</cfdi:Impuestos>`) + len(`</cfdi:Impuestos>`)
			return raw[:start] + raw[end:]
		}, field: "Concepto[0]/Impuestos"},
		{name: "MXN amount written with three decimals", mutate: replaceOnce(`Total="40311.74"`, `Total="40311.740"`), field: "Total"},
		{name: "MXN amount with a sub-cent", mutate: replaceOnce(`Total="40311.74"`, `Total="40311.741"`), field: "Total"},
		{name: "postal code of four digits", mutate: replaceOnce(`LugarExpedicion="64000"`, `LugarExpedicion="6400"`), field: "LugarExpedicion"},
		{name: "unknown UsoCFDI", mutate: replaceOnce(`UsoCFDI="G03"`, `UsoCFDI="Z99"`), field: "UsoCFDI"},
		{name: "wrong namespace (CFDI 3.3)", mutate: func(t *testing.T, raw string) string {
			return strings.ReplaceAll(raw, "http://www.sat.gob.mx/cfd/4", "http://www.sat.gob.mx/cfd/3")
		}, field: "xml"},
	})
}

func TestGST34_Mutations(t *testing.T) {
	runStatCases(t, "CA", []statCase{
		{name: "fixture as shipped", field: ""},
		{name: "line 105 not 103 + 104", mutate: replaceOnce("<line105>13125.40</line105>", "<line105>13000.00</line105>"), field: "line105"},
		{name: "line 108 not 106 + 107", mutate: replaceOnce("<line107>0.00</line107>", "<line107>10.00</line107>"), field: "line108"},
		{name: "line 109 off by one cent", mutate: replaceOnce("<line109>8914.85</line109>", "<line109>8914.86</line109>"), field: "line109"},
		{name: "refund (negative line 109) is legal", mutate: chain(
			replaceOnce("<line106>4210.55</line106>", "<line106>15000.00</line106>"),
			replaceOnce("<line108>4210.55</line108>", "<line108>15000.00</line108>"),
			replaceOnce("<line109>8914.85</line109>", "<line109>-1874.60</line109>")), field: ""},
		{name: "BN check digit wrong", mutate: replaceOnce("123456782RT0001", "123456783RT0001"), field: "businessNumber"},
		{name: "BN without the RT account", mutate: replaceOnce("123456782RT0001", "123456782RC0001"), field: "businessNumber"},
		{name: "line 101 with cents", mutate: replaceOnce("<line101>260000</line101>", "<line101>260000.50</line101>"), field: "line101"},
		{name: "line 103 with three decimals", mutate: replaceOnce("<line103>13000.00</line103>", "<line103>13000.001</line103>"), field: "line103"},
		{name: "period ends before it starts", mutate: replaceOnce(`end="2025-09-30"`, `end="2025-06-30"`), field: "reportingPeriod"},
		{name: "period longer than a year", mutate: replaceOnce(`end="2025-09-30"`, `end="2026-07-01"`), field: "reportingPeriod"},
		{name: "period date not a date", mutate: replaceOnce(`start="2025-07-01"`, `start="2025-02-30"`), field: "reportingPeriod"},
		{name: "line missing", mutate: replaceOnce("<line104>125.40</line104>", ""), field: "line104"},
		{name: "line past 109", mutate: replaceOnce("</GST34>", "<line110>0.00</line110></GST34>"), field: "line110"},
	})
}
