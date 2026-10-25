package domain_test

import (
	"errors"
	"testing"

	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
)

func TestParseXmllintLine_MapsSchemaValidityLine(t *testing.T) {
	line := "pain001.xml:14: element InstdAmt: Schemas validity error : Element '{urn:iso:std:iso:20022:tech:xsd:pain.001.001.09}InstdAmt': '1.234' is not a valid value of the atomic type."
	got, ok := domain.ParseXmllintLine(line)
	if !ok {
		t.Fatal("schema validity line was not recognised")
	}
	if got.Path != "InstdAmt" || got.Reason != "Element '{urn:iso:std:iso:20022:tech:xsd:pain.001.001.09}InstdAmt': '1.234' is not a valid value of the atomic type." {
		t.Fatalf("unexpected mapping: %+v", got)
	}
	var e *domain.Error
	if !errors.As(got.AsError(), &e) || e.Code != "AP-FIL-002" || e.Detail != got.Path+" "+got.Reason {
		t.Fatalf("AsError = %#v", got.AsError())
	}
	if got.Error() != "AP-FIL-002: "+got.Path+" "+got.Reason {
		t.Fatalf("Error() = %q", got.Error())
	}
}

func TestParseXmllintLine_IgnoresOtherLines(t *testing.T) {
	for _, line := range []string{"", "pain001.xml validates", "pain001.xml fails to validate", "pain001.xml:3: parser error : Premature end of data"} {
		if got, ok := domain.ParseXmllintLine(line); ok {
			t.Fatalf("%q matched as %+v", line, got)
		}
	}
}
