package main

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestRun_RefusesBadConfiguration(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	db := map[string]string{"DATABASE_URL": "postgres://127.0.0.1:1/none"}
	for _, c := range []struct {
		args []string
		env  map[string]string
		want string
	}{
		{nil, nil, "DATABASE_URL is required"},
		{[]string{"-tenant", "x"}, db, "must be UUIDs"},
		{[]string{"-ledger-id", "x"}, db, "-ledger-id must be a UUID"},
		{[]string{"-ledger-id", defaultLegalEntity}, db, "needs LEDGER_URL"},
	} {
		err := run(context.Background(), c.args, env(c.env), io.Discard)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want %q", c.args, err, c.want)
		}
	}
}
