// Package migrations builds the identity service's versioned migration set.
package migrations

import (
	"embed"
	"io/fs"
	"strings"
	"testing/fstest"

	"github.com/BenjaminSRussell/passage/platform/dbx"
)

//go:embed *.sql
var source embed.FS

// FS returns migrations with the shared platform RLS policy templates applied
// to every identity-owned tenant table.
func FS() fs.FS {
	body, err := source.ReadFile("0001_identity.sql")
	if err != nil {
		panic(err)
	}
	ddl := string(body)
	for _, table := range []string{"identity_tenants", "identity_users", "identity_legal_entities"} {
		policy := dbx.RLSPolicy(table)
		// A transaction-local custom GUC can reset to an empty string on a
		// pooled connection. Treat both that state and an unset GUC as NULL so
		// an unscoped query returns no rows instead of failing its UUID cast.
		policy = strings.ReplaceAll(policy,
			"current_setting('passage.tenant_id', true)::uuid",
			"nullif(current_setting('passage.tenant_id', true), '')::uuid")
		ddl += "\n" + policy
	}
	ddl += "\n" + dbx.LegalEntityScopePolicy("identity_legal_entities")
	return fstest.MapFS{
		"0001_identity.sql": &fstest.MapFile{Data: []byte(ddl)},
	}
}
