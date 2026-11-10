package domain

import (
	"errors"
	"testing"
)

func TestParseAdvice_CSVGroupsRowsByPaymentInFileOrder(t *testing.T) {
	lines, err := ParseAdvice(AdviceCSV, "paymentRef,reference,amount\nCHK-9,INV-1001,600.00\nCHK-4,INV-2002,50.50\nCHK-9,INV-1002,400.00\n")
	if err != nil || len(lines) != 2 {
		t.Fatalf("lines %+v %v", lines, err)
	}
	if lines[0].PaymentRef != "CHK-9" || len(lines[0].References) != 2 || lines[0].References[1].Reference != "INV-1002" ||
		!lines[0].References[1].Amount.Equal(dec("400.00")) || lines[1].PaymentRef != "CHK-4" {
		t.Fatalf("grouping %+v", lines)
	}
}

func TestParseAdvice_JSON(t *testing.T) {
	lines, err := ParseAdvice(AdviceJSON, `{"lines":[{"paymentRef":"W-1","references":[{"reference":"INV-7","amount":"10.00"},{"reference":"INV-8","amount":"5"}]}]}`)
	if err != nil || len(lines) != 1 || len(lines[0].References) != 2 {
		t.Fatalf("lines %+v %v", lines, err)
	}
}

func TestParseAdvice_EDI820ReadsTRNAndRMR(t *testing.T) {
	edi := "ISA*00*          *00*          *ZZ*PAYER          *ZZ*US             *270101*1200*U*00401*000000001*0*P*>~" +
		"GS*RA*PAYER*US*20270101*1200*1*X*004010~ST*820*0001~BPR*C*1000*C*ACH*CCP~TRN*1*ACH-5501~" +
		"RMR*IV*INV-1001**600.00*600.00~RMR*IV*INV-1002**400.00*400.00~TRN*1*ACH-5502~RMR*IV*INV-2001**75.00*75.00~SE*9*0001~GE*1*1~IEA*1*000000001~"
	lines, err := ParseAdvice(AdviceEDI, edi)
	if err != nil || len(lines) != 2 {
		t.Fatalf("lines %+v %v", lines, err)
	}
	if lines[0].PaymentRef != "ACH-5501" || len(lines[0].References) != 2 || lines[0].References[0].Reference != "INV-1001" ||
		!lines[0].References[1].Amount.Equal(dec("400")) || lines[1].PaymentRef != "ACH-5502" {
		t.Fatalf("edi %+v", lines)
	}
}

func TestParseAdvice_MalformedFilesAreNamed(t *testing.T) {
	cases := map[string][2]string{
		"csv without a header column": {AdviceCSV, "paymentRef,amount\nA,1\n"},
		"csv with a bad amount":       {AdviceCSV, "paymentRef,reference,amount\nA,INV-1,abc\n"},
		"csv with a zero amount":      {AdviceCSV, "paymentRef,reference,amount\nA,INV-1,0\n"},
		"json without references":     {AdviceJSON, `{"lines":[{"paymentRef":"A","references":[]}]}`},
		"json that is not json":       {AdviceJSON, `nope`},
		"edi RMR before TRN":          {AdviceEDI, "ISA*00*~RMR*IV*INV-1**5*5~"},
		"edi that is not 820":         {AdviceEDI, "hello"},
		"empty":                       {AdviceCSV, "paymentRef,reference,amount\n"},
		"unknown format":              {"XML", "<a/>"},
	}
	for name, c := range cases {
		_, err := ParseAdvice(c[0], c[1])
		var ae *AdviceError
		if !errors.As(err, &ae) {
			t.Errorf("%s: got %v, want an AdviceError", name, err)
		}
	}
}
