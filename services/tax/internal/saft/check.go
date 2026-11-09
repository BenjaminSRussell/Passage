package saft

import (
	"encoding/xml"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
)

func bad(field, format string, a ...any) error {
	return &domain.Error{Code: domain.CodeValidation, Field: field, Detail: fmt.Sprintf(format, a...)}
}

func requiredText(field, v string) error {
	if strings.TrimSpace(v) == "" || len(v) > maxNameLength {
		return bad(field, "%s is required and at most %d characters", field, maxNameLength)
	}
	return nil
}

func CheckDocument(doc []byte) error {
	var f auditFile
	if err := xml.Unmarshal(doc, &f); err != nil {
		return bad("document", "not well-formed SAF-T XML: %v", err)
	}
	if f.XMLName.Space != Namespace {
		return bad("namespace", "namespace must be %s", Namespace)
	}
	customers, err := checkParties("Customers", f.MasterFiles.Customers.ids())
	if err != nil {
		return err
	}
	suppliers, err := checkParties("Suppliers", f.MasterFiles.Suppliers.ids())
	if err != nil {
		return err
	}
	if f.GLEntries == nil {
		return nil
	}
	from, errFrom := time.Parse(dateLayout, f.Header.SelectionCriteria.PeriodStart)
	to, errTo := time.Parse(dateLayout, f.Header.SelectionCriteria.PeriodEnd)
	if errFrom != nil || errTo != nil {
		return bad("SelectionCriteria", "PeriodStart and PeriodEnd must be dates")
	}
	return checkLedger(f.GLEntries, from, to, customers, suppliers)
}

type partyRow struct{ id, account, taxID, name string }

func (l *customerList) ids() (rows []partyRow) {
	if l != nil {
		for _, c := range l.Items {
			rows = append(rows, partyRow{c.CustomerID, c.AccountID, c.CustomerTaxID, c.CompanyName})
		}
	}
	return
}

func (l *supplierList) ids() (rows []partyRow) {
	if l != nil {
		for _, s := range l.Items {
			rows = append(rows, partyRow{s.SupplierID, s.AccountID, s.SupplierTaxID, s.CompanyName})
		}
	}
	return
}

func checkParties(section string, rows []partyRow) (map[string]bool, error) {
	seen := map[string]bool{}
	for _, r := range rows {
		for _, field := range [][2]string{{"ID", r.id}, {"AccountID", r.account}, {"TaxID", r.taxID}, {"CompanyName", r.name}} {
			if err := requiredText(section+"."+field[0], field[1]); err != nil {
				return nil, err
			}
		}
		if seen[r.id] {
			return nil, bad(section+".ID", "duplicate %s id %q", section, r.id)
		}
		seen[r.id] = true
	}
	return seen, nil
}

func checkLedger(gl *glEntries, from, to time.Time, customers, suppliers map[string]bool) error {
	entries := 0
	debit, credit := decimal.Zero, decimal.Zero
	transactionIDs := map[string]bool{}
	for _, j := range gl.Journals {
		if err := requiredText("Journal.JournalID", j.JournalID); err != nil {
			return err
		}
		if err := requiredText("Journal.Type", j.Type); err != nil {
			return err
		}
		for _, t := range j.Transactions {
			entries++
			d, c, err := checkTransaction(t, from, to, customers, suppliers)
			if err != nil {
				return err
			}
			if transactionIDs[t.TransactionID] {
				return bad("Transaction.TransactionID", "duplicate transaction id %q", t.TransactionID)
			}
			transactionIDs[t.TransactionID] = true
			debit, credit = debit.Add(d), credit.Add(c)
		}
	}
	declaredDebit, errDebit := decimal.NewFromString(gl.TotalDebit)
	declaredCredit, errCredit := decimal.NewFromString(gl.TotalCredit)
	switch {
	case gl.NumberOfEntries != entries:
		return bad("NumberOfEntries", "NumberOfEntries %d does not match the %d transactions", gl.NumberOfEntries, entries)
	case errDebit != nil || errCredit != nil:
		return bad("TotalDebit", "TotalDebit and TotalCredit must be decimals")
	case !declaredDebit.Equal(debit) || !declaredCredit.Equal(credit):
		return bad("TotalDebit", "declared totals %s/%s do not match the lines %s/%s", gl.TotalDebit, gl.TotalCredit, debit, credit)
	case !debit.Equal(credit):
		return bad("TotalDebit", "total debit %s differs from total credit %s", debit, credit)
	}
	return nil
}

func checkTransaction(t transactionXML, from, to time.Time, customers, suppliers map[string]bool) (decimal.Decimal, decimal.Decimal, error) {
	zero := decimal.Zero
	if err := requiredText("Transaction.TransactionID", t.TransactionID); err != nil {
		return zero, zero, err
	}
	date, err := time.Parse(dateLayout, t.TransactionDate)
	switch {
	case err != nil:
		return zero, zero, bad("Transaction.TransactionDate", "transaction %q date must be YYYY-MM-DD", t.TransactionID)
	case date.Before(from) || date.After(to):
		return zero, zero, bad("Transaction.TransactionDate", "transaction %q is outside the selection period", t.TransactionID)
	case t.Period < 1 || t.Period > 12 || t.Period != int(date.Month()):
		return zero, zero, bad("Transaction.Period", "transaction %q period %d does not match its date", t.TransactionID, t.Period)
	case len(t.Lines) < 2:
		return zero, zero, bad("Transaction.Line", "transaction %q needs at least two lines", t.TransactionID)
	}
	debit, credit := decimal.Zero, decimal.Zero
	records := map[string]bool{}
	for _, l := range t.Lines {
		d, c, err := checkLine(t.TransactionID, l, customers, suppliers)
		if err != nil {
			return zero, zero, err
		}
		if records[l.RecordID] {
			return zero, zero, bad("Line.RecordID", "transaction %q repeats record id %q", t.TransactionID, l.RecordID)
		}
		records[l.RecordID] = true
		debit, credit = debit.Add(d), credit.Add(c)
	}
	if !debit.Equal(credit) {
		return zero, zero, bad("Transaction", "transaction %q is unbalanced: debit %s, credit %s", t.TransactionID, debit, credit)
	}
	return debit, credit, nil
}

func checkLine(transactionID string, l lineXML, customers, suppliers map[string]bool) (decimal.Decimal, decimal.Decimal, error) {
	zero := decimal.Zero
	if err := requiredText("Line.RecordID", l.RecordID); err != nil {
		return zero, zero, err
	}
	if err := requiredText("Line.AccountID", l.AccountID); err != nil {
		return zero, zero, err
	}
	if (l.DebitAmount == "") == (l.CreditAmount == "") {
		return zero, zero, bad("Line.DebitAmount", "line %q of %q needs exactly one of DebitAmount and CreditAmount", l.RecordID, transactionID)
	}
	raw, side := l.DebitAmount, "DebitAmount"
	if raw == "" {
		raw, side = l.CreditAmount, "CreditAmount"
	}
	amount, err := decimal.NewFromString(raw)
	if err != nil || !amount.IsPositive() {
		return zero, zero, bad("Line."+side, "line %q of %q: %s must be a positive decimal", l.RecordID, transactionID, side)
	}
	if l.CustomerID != "" && !customers[l.CustomerID] {
		return zero, zero, bad("Line.CustomerID", "line %q of %q references unknown customer %q", l.RecordID, transactionID, l.CustomerID)
	}
	if l.SupplierID != "" && !suppliers[l.SupplierID] {
		return zero, zero, bad("Line.SupplierID", "line %q of %q references unknown supplier %q", l.RecordID, transactionID, l.SupplierID)
	}
	if side == "DebitAmount" {
		return amount, zero, nil
	}
	return zero, amount, nil
}
