package dbx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestServiceTokenReplayMigrationsMatchTheTemplate(t *testing.T) {
	for _, service := range []string{"ledger", "close", "numbering", "purchasing", "inventory", "fx", "product", "production", "payables", "party", "transport", "warehouse", "mobile"} {
		files, err := filepath.Glob("../../../services/" + service + "/migrations/*_service_token_replay.sql")
		if err != nil || len(files) != 1 {
			t.Fatalf("%s: want exactly one service_token_replay migration, got %v (%v)", service, files, err)
		}
		got, err := os.ReadFile(files[0])
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != ServiceTokenReplayDDL {
			t.Errorf("%s: %s differs from dbx.ServiceTokenReplayDDL", service, files[0])
		}
	}
}
