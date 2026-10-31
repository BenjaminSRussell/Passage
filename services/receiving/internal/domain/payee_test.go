package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestPayeeBank_Normalize(t *testing.T) {
	tests := []struct {
		name    string
		in      PayeeBank
		want    PayeeBank
		wantErr bool
	}{
		{"valid IBAN is normalised", PayeeBank{IBAN: "de89 3704 0044 0532 0130 00", BIC: "cobadeff"}, PayeeBank{IBAN: "DE89370400440532013000", BIC: "COBADEFF"}, false},
		{"valid GB IBAN", PayeeBank{IBAN: "GB82WEST12345698765432"}, PayeeBank{IBAN: "GB82WEST12345698765432"}, false},
		{"IBAN with a bad checksum", PayeeBank{IBAN: "DE89370400440532013001"}, PayeeBank{}, true},
		{"IBAN that is too short", PayeeBank{IBAN: "DE89"}, PayeeBank{}, true},
		{"IBAN with a bad BIC", PayeeBank{IBAN: "GB82WEST12345698765432", BIC: "XX"}, PayeeBank{}, true},
		{"valid ABA and account", PayeeBank{RoutingNumber: "021000021", AccountNumber: "123456789"}, PayeeBank{RoutingNumber: "021000021", AccountNumber: "123456789"}, false},
		{"ABA with a bad checksum", PayeeBank{RoutingNumber: "021000022", AccountNumber: "123456789"}, PayeeBank{}, true},
		{"ABA with a short account", PayeeBank{RoutingNumber: "021000021", AccountNumber: "12"}, PayeeBank{}, true},
		{"IBAN and ABA together", PayeeBank{IBAN: "GB82WEST12345698765432", RoutingNumber: "021000021", AccountNumber: "123456789"}, PayeeBank{}, true},
		{"nothing", PayeeBank{}, PayeeBank{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.in.Normalize()
			var de *Error
			if tc.wantErr {
				if !errors.As(err, &de) || de.Code != CodeInvalidBank {
					t.Fatalf("err = %v, want %s", err, CodeInvalidBank)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

func TestPayeeBank_ErrorsNeverEchoTheAccount(t *testing.T) {
	secret := PayeeBank{IBAN: "DE89370400440532013001"}
	_, err := secret.Normalize()
	if err == nil || strings.Contains(err.Error(), "0532") {
		t.Fatalf("error %v must not carry the account number", err)
	}
}
