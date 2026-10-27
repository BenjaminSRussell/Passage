package domain

import (
	"errors"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// bai2Sample is a valid one-group, one-account lockbox file. Record counts:
// 49 counts 03..49 (4 records), 98 counts 02..98 (6), 99 counts 01..99 (8).
const bai2Sample = `01,BANKX,LOCKBOX,260101,1200,1,,/
02,LOCKBOX,BANKX,1,260101,1200,USD,2/
03,123456789,USD/
16,116,125000,0,REF1,INV-100,CHECK 4411/
16,999,500,0,REF2,,OTHER/
49,125500,4/
98,125500,1,6/
99,125500,1,8/
`

func TestParseBAI2_ValidSampleYieldsCheckCredits(t *testing.T) {
	f, err := ParseBAI2(bai2Sample)
	if err != nil {
		t.Fatalf("ParseBAI2: %v", err)
	}
	if len(f.Accounts) != 1 || f.Accounts[0].Number != "123456789" || f.Accounts[0].Currency != "USD" {
		t.Fatalf("accounts = %+v", f.Accounts)
	}
	checks := f.Checks(DefaultLockboxTypeCodes)
	if len(checks) != 1 {
		t.Fatalf("checks = %d, want 1: %+v", len(checks), checks)
	}
	c := checks[0]
	if !c.Amount.Equal(amt("1250.00")) || c.BankRef != "REF1" || c.CustomerRef != "INV-100" || c.Text != "CHECK 4411" || c.Line != 4 {
		t.Errorf("check = %+v", c)
	}
}

func TestParseBAI2_Rejections(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string // substring of the error, which carries the line number
	}{
		{"unknown record type", strings.Replace(bai2Sample, "16,999", "77,999", 1), "Line 5: malformed BAI2 record type 77"},
		{"amount is not cents", strings.Replace(bai2Sample, "125000", "1250.00", 1), "Line 4: malformed BAI2 record type 16"},
		{"funds type D with a bad count", strings.Replace(bai2Sample, "16,116,125000,0,REF1", "16,116,125000,D,X,REF1", 1), "Line 4: detail record 16 has malformed funds-type"},
		{"unknown funds type", strings.Replace(bai2Sample, "16,116,125000,0,REF1", "16,116,125000,Q,REF1", 1), "Line 4: detail record 16 has malformed funds-type"},
		{"continuation first", "88,TEXT/\n" + bai2Sample, "Line 1: file must start with record 01"},
		{"record not terminated", strings.Replace(bai2Sample, "02,LOCKBOX,BANKX,1,260101,1200,USD,2/", "02,LOCKBOX,BANKX,1,260101,1200,USD,2", 1), "Line 2: record is not terminated"},
		{"account count wrong", strings.Replace(bai2Sample, "49,125500,4/", "49,125500,5/", 1), "Line 6: account trailer 49"},
		{"group count wrong", strings.Replace(bai2Sample, "98,125500,1,6/", "98,125500,1,7/", 1), "Line 7: group trailer 98"},
		{"file count wrong", strings.Replace(bai2Sample, "99,125500,1,8/", "99,125500,1,9/", 1), "Line 8: file trailer 99"},
		{"missing trailer", strings.Replace(bai2Sample, "99,125500,1,8/\n", "", 1), "file has no trailer 99"},
		{"must start with 01", strings.Replace(bai2Sample, "01,BANKX,LOCKBOX,260101,1200,1,,/\n", "", 1), "Line 1: file must start with record 01"},
		{"record after trailer", bai2Sample + "16,116,100,0,X,Y,Z/\n", "Line 9: record after file trailer 99"},
		{"detail outside account", strings.Replace(bai2Sample, "03,123456789,USD/\n", "", 1), "Line 3: malformed BAI2 record type 16"},
		{"account without number", strings.Replace(bai2Sample, "03,123456789,USD/", "03,,USD/", 1), "Line 3: account identifier"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseBAI2(tc.content)
			if err == nil {
				t.Fatal("ParseBAI2 accepted a malformed file")
			}
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("error %T is not *ParseError", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestParseBAI2_ChecksFilterByTypeAndSign(t *testing.T) {
	content := strings.Replace(bai2Sample, "16,999,500,0,REF2,,OTHER/", "16,116,0,0,REF3,,ZERO/\n16,115,700,0,REF4,INV-7,LOCKBOX/", 1)
	content = strings.Replace(content, "49,125500,4/", "49,125500,5/", 1)
	content = strings.Replace(content, "98,125500,1,6/", "98,125500,1,7/", 1)
	content = strings.Replace(content, "99,125500,1,8/", "99,125500,1,9/", 1)
	f, err := ParseBAI2(content)
	if err != nil {
		t.Fatalf("ParseBAI2: %v", err)
	}
	checks := f.Checks([]string{"115", "116"})
	if len(checks) != 2 {
		t.Fatalf("checks = %+v, want the 1250.00 and 7.00 credits", checks)
	}
	if got := f.Checks([]string{"115"}); len(got) != 1 || !got[0].Amount.Equal(amt("7.00")) {
		t.Errorf("bank profile filter: %+v", got)
	}
}

func TestParseBAI2_ContinuationAppendsText(t *testing.T) {
	content := strings.Replace(bai2Sample, "16,116,125000,0,REF1,INV-100,CHECK 4411/", "16,116,125000,0,REF1,INV-100,CHECK 4411/\n88,INV-101 PART/", 1)
	content = strings.Replace(content, "49,125500,4/", "49,125500,5/", 1)
	content = strings.Replace(content, "98,125500,1,6/", "98,125500,1,7/", 1)
	content = strings.Replace(content, "99,125500,1,8/", "99,125500,1,9/", 1)
	f, err := ParseBAI2(content)
	if err != nil {
		t.Fatalf("ParseBAI2: %v", err)
	}
	if got := f.Accounts[0].Transactions[0].Text; got != "CHECK 4411 INV-101 PART" {
		t.Errorf("text = %q", got)
	}
}

func TestParseBAI2_FundsTypeDetailShiftsReferences(t *testing.T) {
	tests := []struct{ name, record string }{
		{"S: immediate, one-day and two-day amounts", "16,116,125000,S,100000,25000,0,REF1,INV-100,CHECK 4411"},
		{"V: value date and time", "16,116,125000,V,270305,1200,REF1,INV-100,CHECK 4411/"},
		{"D: two distributions", "16,116,125000,D,2,0,100000,1,25000,REF1,INV-100,CHECK 4411/"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			content := strings.Replace(bai2Sample, "16,116,125000,0,REF1,INV-100,CHECK 4411/", tc.record, 1)
			f, err := ParseBAI2(content)
			if err != nil {
				t.Fatalf("ParseBAI2: %v", err)
			}
			c := f.Checks(DefaultLockboxTypeCodes)
			if len(c) != 1 || c[0].BankRef != "REF1" || c[0].CustomerRef != "INV-100" || c[0].Text != "CHECK 4411" {
				t.Errorf("check = %+v, want references after the funds-type detail", c)
			}
		})
	}
}

func TestParseBAI2_ContinuationOfHeaderIsIgnored(t *testing.T) {
	content := strings.Replace(bai2Sample, "03,123456789,USD/", "03,123456789,USD/\n88,010,500000,,/", 1)
	content = strings.Replace(content, "49,125500,4/", "49,125500,5/", 1)
	content = strings.Replace(content, "98,125500,1,6/", "98,125500,1,7/", 1)
	content = strings.Replace(content, "99,125500,1,8/", "99,125500,1,9/", 1)
	f, err := ParseBAI2(content)
	if err != nil {
		t.Fatalf("ParseBAI2: %v", err)
	}
	if got := f.Accounts[0].Transactions[0].Text; got != "CHECK 4411" {
		t.Errorf("text = %q, want the 16 record's own text", got)
	}
}

func TestCreditTypeCode(t *testing.T) {
	for code, want := range map[string]bool{"115": true, "116": true, "399": true, "100": true, "475": false, "099": false, "11": false, "abc": false} {
		if got := CreditTypeCode(code); got != want {
			t.Errorf("CreditTypeCode(%q) = %v, want %v", code, got, want)
		}
	}
}

func TestParseBAI2_NeverPanics(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		content := rapid.String().Draw(t, "content")
		_, _ = ParseBAI2(content)
	})
	rapid.Check(t, func(t *rapid.T) {
		// Mutate the sample by dropping a random line; the parser must reject, never panic.
		lines := strings.Split(bai2Sample, "\n")
		i := rapid.IntRange(0, len(lines)-1).Draw(t, "drop")
		content := strings.Join(append(append([]string{}, lines[:i]...), lines[i+1:]...), "\n")
		if _, err := ParseBAI2(content); err == nil && i != len(lines)-1 && i != 0 {
			// Dropping an interior record must break a count or the trailer.
			t.Fatalf("dropping line %d still parsed: %q", i+1, content)
		}
	})
}
