package domain_test

import (
	"bytes"
	"encoding/csv"
	"log/slog"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
)

func check(number, amount, status string) domain.CheckPaymentDTO {
	return domain.CheckPaymentDTO{
		AccountNumber: "123456789012", CheckNumber: number, Amount: amount, Currency: "USD",
		Payee: "Acme Supply", IssueDate: "2026-10-01", Status: status,
	}
}

func readRows(t testing.TB, content []byte) [][]string {
	t.Helper()
	r := csv.NewReader(bytes.NewReader(content))
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func parseAll(t *testing.T, dtos ...domain.CheckPaymentDTO) []domain.CheckPayment {
	t.Helper()
	out := make([]domain.CheckPayment, 0, len(dtos))
	for _, dto := range dtos {
		c, err := domain.ParseCheckPayment(dto)
		if err != nil {
			t.Fatalf("parse %s: %v", dto.CheckNumber, err)
		}
		out = append(out, c)
	}
	return out
}

func TestGeneratePositivePay_RowsOrderAndTrailer(t *testing.T) {
	voided := check("1002", "40.00", domain.PaymentVoid)
	voided.Payee = "Beta, \"Quoted\" Ltd"
	file, err := domain.GeneratePositivePay(parseAll(t,
		check("1010", "10.50", domain.PaymentSent), voided, check("0999", "5", domain.PaymentSettledAtBank)))
	if err != nil {
		t.Fatal(err)
	}
	rows := readRows(t, file.Content)
	want := [][]string{
		{"account_number", "check_number", "amount", "payee_name", "issue_date", "void_flag"},
		{"123456789012", "0999", "5.00", "Acme Supply", "2026-10-01", "I"},
		{"123456789012", "1002", "40.00", "Beta, \"Quoted\" Ltd", "2026-10-01", "V"},
		{"123456789012", "1010", "10.50", "Acme Supply", "2026-10-01", "I"},
		{"TRAILER", "3", "15.50"},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows = %v", rows)
	}
	for i := range want {
		if strings.Join(rows[i], "|") != strings.Join(want[i], "|") {
			t.Errorf("row %d = %v, want %v", i, rows[i], want[i])
		}
	}
	if file.Count != 3 || !file.Total.Equal(decimal.RequireFromString("15.50")) || len(file.SHA256) != 64 {
		t.Errorf("file = %+v", file)
	}
}

func TestGeneratePositivePay_Deterministic(t *testing.T) {
	checks := parseAll(t, check("2", "1", domain.PaymentSent), check("1", "2", domain.PaymentSent))
	a, _ := domain.GeneratePositivePay(checks)
	b, _ := domain.GeneratePositivePay([]domain.CheckPayment{checks[1], checks[0]})
	if a.SHA256 != b.SHA256 {
		t.Error("input order changed output")
	}
}

func TestGeneratePositivePay_TrailerMatchesRowsProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(1, 20).Draw(rt, "n")
		dtos := make([]domain.CheckPaymentDTO, n)
		wantTotal := decimal.Zero
		for i := range dtos {
			cents := rapid.Int64Range(1, 10_000_000).Draw(rt, "cents")
			void := rapid.Bool().Draw(rt, "void")
			status := domain.PaymentSent
			if void {
				status = domain.PaymentVoid
			} else {
				wantTotal = wantTotal.Add(decimal.New(cents, -2))
			}
			dtos[i] = check(decimal.NewFromInt(int64(i+1)).String(), decimal.New(cents, -2).StringFixed(2), status)
		}
		file, err := domain.GeneratePositivePay(parseAll(t, dtos...))
		if err != nil {
			rt.Fatal(err)
		}
		rows := readRows(t, file.Content)
		trailer := rows[len(rows)-1]
		if trailer[1] != decimal.NewFromInt(int64(n)).String() || trailer[2] != wantTotal.StringFixed(2) || len(rows) != n+2 {
			rt.Fatalf("trailer %v rows %d, want count %d total %s", trailer, len(rows), n, wantTotal)
		}
	})
}

func TestGeneratePositivePay_RejectsUnissuedStatuses(t *testing.T) {
	for _, status := range []string{domain.PaymentCreated, domain.PaymentRejected, domain.PaymentReversed, "BOGUS", ""} {
		_, err := domain.ParseCheckPayment(check("1", "1.00", status))
		if err == nil || codeOf(t, err) != domain.CodeValidation {
			t.Errorf("status %q: want PAY-0001, got %v", status, err)
		}
	}
}

func TestGeneratePositivePay_RejectsBadSets(t *testing.T) {
	if _, err := domain.GeneratePositivePay(nil); err == nil || codeOf(t, err) != domain.CodeValidation {
		t.Errorf("empty: %v", err)
	}
	_, err := domain.GeneratePositivePay(parseAll(t, check("7", "1", domain.PaymentSent), check("007", "2", domain.PaymentSent)))
	if err == nil || codeOf(t, err) != domain.CodeValidation || strings.Contains(err.Error(), "123456789012") {
		t.Errorf("duplicate: %v", err)
	}
	euro := check("8", "1", domain.PaymentSent)
	euro.Currency = "EUR"
	if _, err := domain.GeneratePositivePay(parseAll(t, check("7", "1", domain.PaymentSent), euro)); err == nil || codeOf(t, err) != domain.CodeValidation {
		t.Errorf("mixed currency: %v", err)
	}
}

func TestParseCheckPayment_RejectsInvalidFields(t *testing.T) {
	mutate := func(f func(*domain.CheckPaymentDTO)) domain.CheckPaymentDTO {
		d := check("1", "1.00", domain.PaymentSent)
		f(&d)
		return d
	}
	for name, dto := range map[string]domain.CheckPaymentDTO{
		"formula =":      mutate(func(d *domain.CheckPaymentDTO) { d.Payee = "=SUM(A1)" }),
		"formula +":      mutate(func(d *domain.CheckPaymentDTO) { d.Payee = "+1" }),
		"formula -":      mutate(func(d *domain.CheckPaymentDTO) { d.Payee = "-1" }),
		"formula @":      mutate(func(d *domain.CheckPaymentDTO) { d.Payee = "@cmd" }),
		"blank payee":    mutate(func(d *domain.CheckPaymentDTO) { d.Payee = "  " }),
		"long payee":     mutate(func(d *domain.CheckPaymentDTO) { d.Payee = strings.Repeat("a", 141) }),
		"letter check":   mutate(func(d *domain.CheckPaymentDTO) { d.CheckNumber = "12a" }),
		"empty check":    mutate(func(d *domain.CheckPaymentDTO) { d.CheckNumber = "" }),
		"short account":  mutate(func(d *domain.CheckPaymentDTO) { d.AccountNumber = "123" }),
		"zero amount":    mutate(func(d *domain.CheckPaymentDTO) { d.Amount = "0" }),
		"negative":       mutate(func(d *domain.CheckPaymentDTO) { d.Amount = "-5" }),
		"exponent":       mutate(func(d *domain.CheckPaymentDTO) { d.Amount = "1e3" }),
		"over precision": mutate(func(d *domain.CheckPaymentDTO) { d.Amount = "10.005" }),
		"bad date":       mutate(func(d *domain.CheckPaymentDTO) { d.IssueDate = "2026-13-40" }),
		"us date":        mutate(func(d *domain.CheckPaymentDTO) { d.IssueDate = "10/01/2026" }),
		"lower currency": mutate(func(d *domain.CheckPaymentDTO) { d.Currency = "usd" }),
	} {
		if _, err := domain.ParseCheckPayment(dto); err == nil || codeOf(t, err) != domain.CodeValidation {
			t.Errorf("%s: want PAY-0001, got %v", name, err)
		}
	}
}

func TestGeneratePositivePay_LogMasksAccount(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	if _, err := domain.GeneratePositivePay(parseAll(t, check("1", "1.00", domain.PaymentSent))); err != nil {
		t.Fatal(err)
	}
	out := logs.String()
	if strings.Contains(out, "123456789012") || !strings.Contains(out, "****9012") {
		t.Errorf("log = %s", out)
	}
}

func TestMaskAccount(t *testing.T) {
	for in, want := range map[string]string{"123456789012": "****9012", "1234": "****", "": "****"} {
		if got := domain.MaskAccount(in); got != want {
			t.Errorf("%q = %q, want %q", in, got, want)
		}
	}
}
