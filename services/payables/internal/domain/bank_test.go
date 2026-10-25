package domain_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
)

func codeOf(t *testing.T, err error) string {
	t.Helper()
	var de *domain.Error
	if !errors.As(err, &de) {
		t.Fatalf("want *domain.Error, got %v", err)
	}
	return de.Code
}

func corruptDigits(valid string) []string {
	var out []string
	for i := 0; i < len(valid); i++ {
		if valid[i] < '0' || valid[i] > '9' {
			continue
		}
		flipped := (valid[i]-'0'+1)%10 + '0'
		out = append(out, valid[:i]+string(flipped)+valid[i+1:])
	}
	return out
}

func TestValidateIBAN_ValidSamples(t *testing.T) {
	for _, iban := range []string{
		"DE89370400440532013000", "FR1420041010050500013M02606", "GB29NWBK60161331926819",
		"CH9300762011623852957", "NL91ABNA0417164300", "de89 3704 0044 0532 0130 00",
	} {
		if err := domain.ValidateIBAN(iban); err != nil {
			t.Errorf("%s: %v", iban, err)
		}
	}
}

func TestValidateIBAN_Rejects(t *testing.T) {
	var cases []string
	for _, valid := range []string{"DE89370400440532013000", "FR1420041010050500013M02606", "GB29NWBK60161331926819", "CH9300762011623852957", "NL91ABNA0417164300"} {
		cases = append(cases, corruptDigits(valid)...)
	}
	cases = append(cases, "", "DE", "ZZ89370400440532013000", "DE8937040044053201300", "DE89370400440532013000X", "DE89-370400440532013000", "DEAB370400440532013000")
	for _, iban := range cases {
		err := domain.ValidateIBAN(iban)
		if err == nil {
			t.Errorf("%q accepted", iban)
			continue
		}
		var de *domain.Error
		errors.As(err, &de)
		if de.Code != domain.CodeRunInvalid || !strings.HasPrefix(de.Detail, domain.RuleIBAN+":") {
			t.Errorf("%q: got %v", iban, err)
		}
		if len(iban) > 8 && strings.Contains(de.Detail, iban) {
			t.Errorf("detail echoes the account: %s", de.Detail)
		}
	}
}

func TestValidateBIC(t *testing.T) {
	for _, bic := range []string{"DEUTDEFF", "DEUTDEFF500", "nwbkgb2l"} {
		if err := domain.ValidateBIC(bic); err != nil {
			t.Errorf("%s: %v", bic, err)
		}
	}
	for _, bic := range []string{"", "DEUTDEF", "DEUTDEFF5", "DEUTDEFF5000", "1EUTDEFF", "DEUT-EFF"} {
		err := domain.ValidateBIC(bic)
		if err == nil || codeOf(t, err) != domain.CodeRunInvalid || !strings.HasPrefix(err.Error(), "AP-RUN-002: bic:") {
			t.Errorf("%q: got %v", bic, err)
		}
	}
}

func TestValidateABA(t *testing.T) {
	valid := []string{"021000021", "121000358", "011401533", "091000019", "322271627"}
	for _, aba := range valid {
		if err := domain.ValidateABA(aba); err != nil {
			t.Errorf("%s: %v", aba, err)
		}
		for _, bad := range corruptDigits(aba) {
			err := domain.ValidateABA(bad)
			if err == nil || codeOf(t, err) != domain.CodeRunInvalid || !strings.HasPrefix(err.Error(), "AP-RUN-002: aba:") {
				t.Errorf("%s accepted or wrong error: %v", bad, err)
			}
		}
	}
	for _, bad := range []string{"", "02100002", "0210000211", "02100002X"} {
		if err := domain.ValidateABA(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestParsePaymentMethod(t *testing.T) {
	m, err := domain.ParsePaymentMethod(domain.PaymentMethodDTO{Type: "TRANSFER", Format: "PAIN001", CutoffTime: "14:30"})
	if err != nil || m.MinPayment.String() != "1" || m.MaxPayment != nil {
		t.Fatalf("defaults: %+v %v", m, err)
	}
	if format, err := m.MediaFormat(); err != nil || format != domain.FormatPain001 {
		t.Fatalf("media format: %q %v", format, err)
	}
	bad := []domain.PaymentMethodDTO{
		{Type: ""}, {Type: "BARTER", Format: "PAIN001"}, {Type: "ACH", Format: "PAIN001"}, {Type: "TRANSFER"},
		{Type: "CARD", Format: "PAIN001"}, {Type: "ACH", Format: "NACHA", MinPayment: "abc"},
		{Type: "ACH", Format: "NACHA", MinPayment: "NaN"}, {Type: "ACH", Format: "NACHA", MinPayment: "-1"},
		{Type: "ACH", Format: "NACHA", MinPayment: "10", MaxPayment: "5"}, {Type: "ACH", Format: "NACHA", CutoffTime: "25:00"},
	}
	for _, dto := range bad {
		if _, err := domain.ParsePaymentMethod(dto); err == nil || codeOf(t, err) != domain.CodeValidation {
			t.Errorf("%+v: got %v", dto, err)
		}
	}
}

func TestPaymentMethod_CardHasNoMediaFormat(t *testing.T) {
	m, err := domain.ParsePaymentMethod(domain.PaymentMethodDTO{Type: "CARD", MaxPayment: "5000"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.MediaFormat(); err == nil || codeOf(t, err) != domain.CodeValidation {
		t.Fatalf("got %v", err)
	}
}

func profile(variant, version string, overrides map[string]string) domain.BankProfileDTO {
	raw := map[string]json.RawMessage{}
	for k, v := range overrides {
		raw[k] = json.RawMessage(v)
	}
	return domain.BankProfileDTO{Variant: variant, Pain001Version: version, Overrides: raw}
}

func TestParseBankProfile_Defaults(t *testing.T) {
	for _, dto := range []domain.BankProfileDTO{profile("SEPA", "", nil), profile("SEPA", "", map[string]string{})} {
		p, err := domain.ParseBankProfile(dto)
		if err != nil {
			t.Fatal(err)
		}
		o := p.Overrides
		if p.Pain001Version != "001.001.09" || o.ChargeBearer != "SHAR" || !o.BatchBooking || o.Charset != "latin-sepa" || o.MaxUstrdLength != 140 || o.BICRequired {
			t.Fatalf("defaults: %+v", p)
		}
	}
}

func TestParseBankProfile_Overrides(t *testing.T) {
	p, err := domain.ParseBankProfile(profile("US", "001.001.09", map[string]string{
		"charge_bearer": `"DEBT"`, "batch_booking": `false`, "charset": `"ascii"`, "max_ustrd_length": `70`,
		"bic_required": `true`, "structured_remittance": `true`, "structured_address_required": `true`, "initiating_party_id": `"CUST1"`,
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := domain.Overrides{ChargeBearer: "DEBT", Charset: "ascii", MaxUstrdLength: 70, BICRequired: true, StructuredRemittance: true, StructuredAddressRequired: true, InitiatingPartyID: "CUST1"}
	if p.Overrides != want {
		t.Fatalf("got %+v", p.Overrides)
	}
}

func TestParseBankProfile_Rejects(t *testing.T) {
	many := map[string]string{}
	for i := 0; i < 21; i++ {
		many[fmt.Sprintf("charge_bearer%d", i)] = `"SHAR"`
	}
	cases := map[string]domain.BankProfileDTO{
		"unknown key":         profile("SEPA", "", map[string]string{"colour": `"red"`}),
		"21 keys":             profile("SEPA", "", many),
		"bad charge bearer":   profile("SEPA", "", map[string]string{"charge_bearer": `"XXXX"`}),
		"bad charset":         profile("SEPA", "", map[string]string{"charset": `"utf-8"`}),
		"ustrd 141":           profile("SEPA", "", map[string]string{"max_ustrd_length": "141"}),
		"ustrd zero":          profile("SEPA", "", map[string]string{"max_ustrd_length": "0"}),
		"string for bool":     profile("SEPA", "", map[string]string{"batch_booking": `"true"`}),
		"string for int":      profile("SEPA", "", map[string]string{"max_ustrd_length": `"70"`}),
		"long initiating id":  profile("SEPA", "", map[string]string{"initiating_party_id": `"` + strings.Repeat("A", 36) + `"`}),
		"legacy pain version": profile("SEPA", "001.001.03", nil),
		"unknown version":     profile("SEPA", "001.001.10", nil),
		"unknown variant":     profile("MX", "", nil),
		"empty variant":       profile("", "", nil),
	}
	for name, dto := range cases {
		if _, err := domain.ParseBankProfile(dto); err == nil || codeOf(t, err) != domain.CodeValidation {
			t.Errorf("%s: got %v", name, err)
		}
	}
	_, err := domain.ParseBankProfile(profile("SEPA", "001.001.03", nil))
	if !strings.Contains(err.Error(), "not supported in this slice") {
		t.Errorf("legacy message: %v", err)
	}
}

func TestParseBankProfile_TwentyKeysAreCounted(t *testing.T) {
	exactly := map[string]string{}
	for i := 0; i < 20; i++ {
		exactly[fmt.Sprintf("k%d", i)] = `1`
	}
	_, err := domain.ParseBankProfile(profile("SEPA", "", exactly))
	if err == nil || !strings.Contains(err.Error(), "unknown override key") {
		t.Fatalf("20 unknown keys must fail on the key, not the count: %v", err)
	}
}

func TestTransliterate(t *testing.T) {
	cases := []struct{ in, charset, want string }{
		{"Müller Straße GmbH", "latin-sepa", "Muller Strasse GmbH"},
		{"Æsir Ørsted A/S", "latin-sepa", "AEsir Orsted A/S"},
		{"Łódź Żubrówka Sp. z o.o.", "latin-sepa", "Lodz Zubrowka Sp. z o.o."},
		{"Françoise Dupré Œuvres", "ascii", "Francoise Dupre OEuvres"},
		{"José Peña Ñandú", "latin-sepa", "Jose Pena Nandu"},
		{"Smith & Sons", "ascii", "Smith & Sons"},
	}
	for _, c := range cases {
		got, err := domain.Transliterate(c.in, c.charset)
		if err != nil || got != c.want {
			t.Errorf("%q: got %q %v want %q", c.in, got, err, c.want)
		}
	}
}

func TestTransliterate_RejectsOutsideCharset(t *testing.T) {
	cases := []struct{ in, charset string }{
		{"株式会社 Example", "latin-sepa"}, {"Pay 🙂", "ascii"}, {"Smith & Sons", "latin-sepa"},
		{"éclair", "latin-sepa"}, {"", "latin-sepa"}, {"   ", "ascii"},
	}
	for _, c := range cases {
		got, err := domain.Transliterate(c.in, c.charset)
		if err == nil || got != "" || codeOf(t, err) != domain.CodeFileInvalid {
			t.Errorf("%q: got %q %v", c.in, got, err)
		}
	}
}
