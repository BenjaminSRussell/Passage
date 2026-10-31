package domain

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/shopspring/decimal"
)

// FR_FEC: the Fichier des écritures comptables of article A47 A-1 of the Livre
// des procédures fiscales, as a flat file. The rules: file name
// <SIREN>FEC<closing date AAAAMMJJ>; the 18 columns in order under their exact
// names, separated by a pipe or a tab; dates AAAAMMJJ; amounts with a comma
// decimal separator; the paired columns (auxiliary account, lettering,
// currency) filled together; lines in the chronological order of validation;
// and every entry (EcritureNum) balanced in one journal on one date.

var fecColumns = []string{
	"JournalCode", "JournalLib", "EcritureNum", "EcritureDate", "CompteNum", "CompteLib", "CompAuxNum",
	"CompAuxLib", "PieceRef", "PieceDate", "EcritureLib", "Debit", "Credit", "EcritureLet", "DateLet",
	"ValidDate", "Montantdevise", "Idevise",
}

const (
	fecJournalCode = iota
	fecJournalLib
	fecEcritureNum
	fecEcritureDate
	fecCompteNum
	fecCompteLib
	fecCompAuxNum
	fecCompAuxLib
	fecPieceRef
	fecPieceDate
	fecEcritureLib
	fecDebit
	fecCredit
	fecEcritureLet
	fecDateLet
	fecValidDate
	fecMontantdevise
	fecIdevise
)

var (
	fecFileName = regexp.MustCompile(`^(\d{9})FEC(\d{8})\.(txt|csv)$`)
	fecAmount   = regexp.MustCompile(`^-?\d+(,\d{1,2})?$`)
	fecAccount  = regexp.MustCompile(`^[1-8][0-9A-Z]{2,}$`)
	fecCurrency = regexp.MustCompile(`^[A-Z]{3}$`)
)

type fecEntry struct {
	journal, date  string
	debit, credit  decimal.Decimal
	firstLineField string
}

// ValidateFEC checks an FEC file named name; it returns every failure.
func ValidateFEC(name string, raw []byte) []FieldError {
	var errs fieldErrs
	closing := ""
	if m := fecFileName.FindStringSubmatch(name); m == nil {
		errs.add("fileName", "%q must be <SIREN>FEC<AAAAMMJJ>.txt", name)
	} else {
		if !luhnValid(m[1]) {
			errs.add("fileName", "SIREN %s fails its check digit", m[1])
		}
		if _, ok := validDate("20060102", m[2]); ok {
			closing = m[2]
		} else {
			errs.add("fileName", "closing date %s is not a date", m[2])
		}
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	if len(lines) < 2 {
		errs.add("file", "needs the header line and at least one entry line")
		return errs
	}
	sep := "|"
	if !strings.Contains(lines[0], "|") {
		sep = "\t"
	}
	if strings.Join(strings.Split(lines[0], sep), "|") != strings.Join(fecColumns, "|") {
		errs.add("header", "must be the 18 columns %s in this order", strings.Join(fecColumns, ", "))
		return errs
	}
	entries := map[string]*fecEntry{}
	var order []string
	lastValid := ""
	for i, line := range lines[1:] {
		ln := i + 2
		at := func(col int) string { return fmt.Sprintf("line %d %s", ln, fecColumns[col]) }
		f := strings.Split(line, sep)
		if len(f) != len(fecColumns) {
			errs.add(fmt.Sprintf("line %d", ln), "has %d columns, want %d", len(f), len(fecColumns))
			continue
		}
		for _, c := range []int{fecJournalCode, fecJournalLib, fecEcritureNum, fecEcritureDate, fecCompteNum, fecCompteLib, fecPieceRef, fecPieceDate, fecEcritureLib, fecValidDate} {
			if strings.TrimSpace(f[c]) == "" {
				errs.add(at(c), "is required")
			}
		}
		for _, c := range []int{fecEcritureDate, fecPieceDate, fecValidDate} {
			if f[c] != "" {
				if _, ok := validDate("20060102", f[c]); !ok {
					errs.add(at(c), "%q must be a date AAAAMMJJ", f[c])
				}
			}
		}
		if f[fecCompteNum] != "" && !fecAccount.MatchString(f[fecCompteNum]) {
			errs.add(at(fecCompteNum), "%q is not a chart-of-accounts number (class 1 to 8, at least three characters)", f[fecCompteNum])
		}
		pair := func(a, b int) {
			if (f[a] == "") != (f[b] == "") {
				errs.add(at(a), "%s and %s are filled together", fecColumns[a], fecColumns[b])
			}
		}
		pair(fecCompAuxNum, fecCompAuxLib)
		pair(fecEcritureLet, fecDateLet)
		pair(fecMontantdevise, fecIdevise)
		if f[fecDateLet] != "" {
			if _, ok := validDate("20060102", f[fecDateLet]); !ok {
				errs.add(at(fecDateLet), "%q must be a date AAAAMMJJ", f[fecDateLet])
			}
		}
		if f[fecMontantdevise] != "" && !fecAmount.MatchString(f[fecMontantdevise]) {
			errs.add(at(fecMontantdevise), "%q must be an amount with a comma decimal separator", f[fecMontantdevise])
		}
		if f[fecIdevise] != "" && !fecCurrency.MatchString(f[fecIdevise]) {
			errs.add(at(fecIdevise), "%q must be an ISO 4217 code", f[fecIdevise])
		}
		amount := func(c int) (decimal.Decimal, bool) {
			if !fecAmount.MatchString(f[c]) || strings.HasPrefix(f[c], "-") {
				errs.add(at(c), "%q must be a non-negative amount with a comma decimal separator", f[c])
				return decimal.Zero, false
			}
			return decimal.RequireFromString(strings.Replace(f[c], ",", ".", 1)), true
		}
		debit, okD := amount(fecDebit)
		credit, okC := amount(fecCredit)
		if okD && okC && debit.IsPositive() && credit.IsPositive() {
			errs.add(at(fecDebit), "a line carries a debit or a credit, not both")
		}
		if closing != "" && f[fecEcritureDate] > closing {
			errs.add(at(fecEcritureDate), "%s is after the closing date %s", f[fecEcritureDate], closing)
		}
		if f[fecValidDate] != "" && f[fecValidDate] < f[fecEcritureDate] {
			errs.add(at(fecValidDate), "validation %s precedes the entry date %s", f[fecValidDate], f[fecEcritureDate])
		}
		if f[fecValidDate] < lastValid {
			errs.add(at(fecValidDate), "lines must follow the chronological order of validation")
		}
		if f[fecValidDate] > lastValid {
			lastValid = f[fecValidDate]
		}
		e, seen := entries[f[fecEcritureNum]]
		if !seen {
			e = &fecEntry{journal: f[fecJournalCode], date: f[fecEcritureDate], firstLineField: at(fecEcritureNum)}
			entries[f[fecEcritureNum]] = e
			order = append(order, f[fecEcritureNum])
		} else if e.journal != f[fecJournalCode] || e.date != f[fecEcritureDate] {
			errs.add(at(fecEcritureNum), "entry %s spans journals or dates", f[fecEcritureNum])
		}
		e.debit = e.debit.Add(debit)
		e.credit = e.credit.Add(credit)
	}
	for _, num := range order {
		if e := entries[num]; !e.debit.Equal(e.credit) {
			errs.add(e.firstLineField, "entry %s is not balanced: debit %s, credit %s", num, e.debit.StringFixed(2), e.credit.StringFixed(2))
		}
	}
	return errs
}
