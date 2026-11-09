package saft

import (
	"encoding/xml"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

const maxPeriodYearSkew = 1

type DocumentError struct{ Section, Detail string }

func (e *DocumentError) Error() string { return "SAF-T " + e.Section + ": " + e.Detail }

func bad(section, format string, a ...any) error {
	return &DocumentError{Section: section, Detail: fmt.Sprintf(format, a...)}
}

func requiredText(section, v string) error {
	if strings.TrimSpace(v) == "" || len(v) > maxNameLength {
		return bad(section, "a required text is empty or longer than %d characters", maxNameLength)
	}
	return nil
}

func CheckDocument(doc []byte) error {
	var f auditFile
	if err := xml.Unmarshal(doc, &f); err != nil {
		return bad("document", "not well-formed XML")
	}
	if f.XMLName.Space != Namespace {
		return bad("namespace", "namespace must be %s", Namespace)
	}
	accounts, err := checkAccounts(f.MasterFiles.Accounts)
	if err != nil {
		return err
	}
	customers, err := checkParties("Customers", f.MasterFiles.Customers.rows())
	if err != nil {
		return err
	}
	suppliers, err := checkParties("Suppliers", f.MasterFiles.Suppliers.rows())
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
	return checkLedger(f.GLEntries, from, to, refs{accounts: accounts, customers: customers, suppliers: suppliers})
}

type refs struct{ accounts, customers, suppliers map[string]bool }

func checkAccounts(l *accountList) (map[string]bool, error) {
	seen := map[string]bool{}
	if l == nil {
		return seen, nil
	}
	for i, a := range l.Items {
		section := fmt.Sprintf("GeneralLedgerAccounts/Account[%d]", i)
		for _, v := range []string{a.AccountID, a.AccountDescription, a.AccountType} {
			if err := requiredText(section, v); err != nil {
				return nil, err
			}
		}
		if err := checkBalanceChoice(section+"/Opening", a.OpeningDebit, a.OpeningCredit); err != nil {
			return nil, err
		}
		if err := checkBalanceChoice(section+"/Closing", a.ClosingDebit, a.ClosingCredit); err != nil {
			return nil, err
		}
		if seen[a.AccountID] {
			return nil, bad(section, "duplicate AccountID")
		}
		seen[a.AccountID] = true
	}
	return seen, nil
}

func checkBalanceChoice(section, debit, credit string) error {
	if (debit == "") == (credit == "") {
		return bad(section, "needs exactly one debit or credit balance")
	}
	raw := debit + credit
	if v, err := decimal.NewFromString(raw); err != nil || v.IsNegative() {
		return bad(section, "balance must be a non-negative decimal")
	}
	return nil
}

type partyRow struct{ id, account, taxID, name string }

func (l *customerList) rows() (rows []partyRow) {
	if l != nil {
		for _, c := range l.Items {
			rows = append(rows, partyRow{c.CustomerID, c.AccountID, c.CustomerTaxID, c.CompanyName})
		}
	}
	return
}

func (l *supplierList) rows() (rows []partyRow) {
	if l != nil {
		for _, s := range l.Items {
			rows = append(rows, partyRow{s.SupplierID, s.AccountID, s.SupplierTaxID, s.CompanyName})
		}
	}
	return
}

func checkParties(section string, rows []partyRow) (map[string]bool, error) {
	seen := map[string]bool{}
	for i, r := range rows {
		at := fmt.Sprintf("%s[%d]", section, i)
		for _, v := range []string{r.id, r.account, r.taxID, r.name} {
			if err := requiredText(at, v); err != nil {
				return nil, err
			}
		}
		if seen[r.id] {
			return nil, bad(at, "duplicate id")
		}
		seen[r.id] = true
	}
	return seen, nil
}

func checkLedger(gl *glEntries, from, to time.Time, r refs) error {
	if len(r.accounts) == 0 {
		return bad("GeneralLedgerAccounts", "GeneralLedgerEntries needs the account list")
	}
	entries := 0
	debit, credit := decimal.Zero, decimal.Zero
	transactionIDs := map[string]bool{}
	for ji, j := range gl.Journals {
		section := fmt.Sprintf("Journal[%d]", ji)
		if err := requiredText(section, j.JournalID); err != nil {
			return err
		}
		if err := requiredText(section, j.Type); err != nil {
			return err
		}
		for ti, t := range j.Transactions {
			entries++
			d, c, err := checkTransaction(fmt.Sprintf("%s/Transaction[%d]", section, ti), t, from, to, r)
			if err != nil {
				return err
			}
			if transactionIDs[t.TransactionID] {
				return bad(section, "duplicate TransactionID")
			}
			transactionIDs[t.TransactionID] = true
			debit, credit = debit.Add(d), credit.Add(c)
		}
	}
	declaredDebit, errDebit := decimal.NewFromString(gl.TotalDebit)
	declaredCredit, errCredit := decimal.NewFromString(gl.TotalCredit)
	switch {
	case gl.NumberOfEntries != entries:
		return bad("GeneralLedgerEntries", "NumberOfEntries does not match the transactions")
	case errDebit != nil || errCredit != nil:
		return bad("GeneralLedgerEntries", "TotalDebit and TotalCredit must be decimals")
	case !declaredDebit.Equal(debit) || !declaredCredit.Equal(credit):
		return bad("GeneralLedgerEntries", "declared totals do not match the lines")
	case !debit.Equal(credit):
		return bad("GeneralLedgerEntries", "total debit differs from total credit")
	}
	return nil
}

func checkTransaction(section string, t transactionXML, from, to time.Time, r refs) (decimal.Decimal, decimal.Decimal, error) {
	zero := decimal.Zero
	if err := requiredText(section, t.TransactionID); err != nil {
		return zero, zero, err
	}
	date, err := time.Parse(dateLayout, t.TransactionDate)
	if err != nil {
		return zero, zero, bad(section, "TransactionDate must be YYYY-MM-DD")
	}
	if _, err := time.Parse(dateLayout, t.GLPostingDate); err != nil {
		return zero, zero, bad(section, "GLPostingDate must be YYYY-MM-DD")
	}
	if _, err := time.Parse(dateTimeLayout, t.SystemEntryDate); err != nil {
		return zero, zero, bad(section, "SystemEntryDate must be a date and time")
	}
	switch {
	case date.Before(from) || date.After(to):
		return zero, zero, bad(section, "the transaction is outside the selection period")
	case t.Period < 1 || t.Period > 12:
		return zero, zero, bad(section, "Period must be 1 to 12")
	case t.PeriodYear < date.Year()-maxPeriodYearSkew || t.PeriodYear > date.Year()+maxPeriodYearSkew:
		return zero, zero, bad(section, "PeriodYear is not within a year of the transaction date")
	case len(t.Lines) < 2:
		return zero, zero, bad(section, "a transaction needs at least two lines")
	}
	debit, credit := decimal.Zero, decimal.Zero
	records := map[string]bool{}
	for li, l := range t.Lines {
		d, c, err := checkLine(fmt.Sprintf("%s/Line[%d]", section, li), l, r)
		if err != nil {
			return zero, zero, err
		}
		if records[l.RecordID] {
			return zero, zero, bad(section, "duplicate RecordID")
		}
		records[l.RecordID] = true
		debit, credit = debit.Add(d), credit.Add(c)
	}
	if !debit.Equal(credit) {
		return zero, zero, bad(section, "the transaction is unbalanced")
	}
	return debit, credit, nil
}

func checkLine(section string, l lineXML, r refs) (decimal.Decimal, decimal.Decimal, error) {
	zero := decimal.Zero
	if err := requiredText(section, l.RecordID); err != nil {
		return zero, zero, err
	}
	if err := requiredText(section, l.AccountID); err != nil {
		return zero, zero, err
	}
	if !r.accounts[l.AccountID] {
		return zero, zero, bad(section, "AccountID is not in GeneralLedgerAccounts")
	}
	if (l.DebitAmount == nil) == (l.CreditAmount == nil) {
		return zero, zero, bad(section, "needs exactly one of DebitAmount and CreditAmount")
	}
	side, credit := l.DebitAmount, false
	if side == nil {
		side, credit = l.CreditAmount, true
	}
	amount, err := decimal.NewFromString(side.Amount)
	if err != nil || !amount.IsPositive() {
		return zero, zero, bad(section, "the amount must be a positive decimal")
	}
	if l.CustomerID != "" && !r.customers[l.CustomerID] {
		return zero, zero, bad(section, "CustomerID is not in Customers")
	}
	if l.SupplierID != "" && !r.suppliers[l.SupplierID] {
		return zero, zero, bad(section, "SupplierID is not in Suppliers")
	}
	if credit {
		return zero, amount, nil
	}
	return amount, zero, nil
}
