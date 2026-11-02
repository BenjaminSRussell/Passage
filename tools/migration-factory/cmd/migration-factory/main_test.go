package main

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestReceivablesTarget(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	cases := map[string]struct {
		env        map[string]string
		wantErr    string
		configured bool
	}{
		"nothing configured starts the service": {env: map[string]string{}},
		"address and key for the factory":       {env: map[string]string{"RECEIVABLES_URL": "http://receivables:8080", "PASSAGE_SERVICE_NAME": "migration-factory", "PASSAGE_SERVICE_SIGNING_KEY": key}, configured: true},
		"address without a key":                 {env: map[string]string{"RECEIVABLES_URL": "http://receivables:8080", "PASSAGE_SERVICE_NAME": "migration-factory"}, wantErr: "no service signing key"},
		"key for another service":               {env: map[string]string{"RECEIVABLES_URL": "http://receivables:8080", "PASSAGE_SERVICE_NAME": "migration", "PASSAGE_SERVICE_SIGNING_KEY": key}, wantErr: `must be "migration-factory"`},
		"address without scheme":                {env: map[string]string{"RECEIVABLES_URL": "receivables:8080", "PASSAGE_SERVICE_NAME": "migration-factory", "PASSAGE_SERVICE_SIGNING_KEY": key}, wantErr: "RECEIVABLES_URL must be"},
		"address with credentials":              {env: map[string]string{"RECEIVABLES_URL": "http://u:p@receivables:8080", "PASSAGE_SERVICE_NAME": "migration-factory", "PASSAGE_SERVICE_SIGNING_KEY": key}, wantErr: "RECEIVABLES_URL must be"},
		"malformed key":                         {env: map[string]string{"RECEIVABLES_URL": "http://receivables:8080", "PASSAGE_SERVICE_SIGNING_KEY": "not-base64"}, wantErr: "PASSAGE_SERVICE_SIGNING_KEY"},
		"key without an address":                {env: map[string]string{"PASSAGE_SERVICE_NAME": "migration-factory", "PASSAGE_SERVICE_SIGNING_KEY": key}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r, err := receivablesTarget(env(c.env))
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("error %v, want it to contain %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if r.Configured() != c.configured {
				t.Fatalf("Configured() = %v, want %v", r.Configured(), c.configured)
			}
		})
	}
}
