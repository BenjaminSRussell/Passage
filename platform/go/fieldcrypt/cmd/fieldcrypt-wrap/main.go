// Command fieldcrypt-wrap makes a new fieldcrypt master key and prints its FIELDCRYPT_KEYS entry, wrapped by the
// Azure Key Vault or Managed HSM key named by FIELDCRYPT_AZURE_KEY_ID. The plain key is never printed or stored.
//
//	FIELDCRYPT_AZURE_KEY_ID=https://<vault>/keys/fieldcrypt-kek PASSAGE_CLOUD_PROFILE=gcc \
//	  go run ./fieldcrypt/cmd/fieldcrypt-wrap -id 2027-01
//
// Rotation: wrap a new key under a new id, append the entry to FIELDCRYPT_KEYS (keep the old entries, they still open
// old values), set FIELDCRYPT_CURRENT_KEY_ID to the new id, roll the services, then re-seal with the service's reseal job.
package main

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/BenjaminSRussell/passage/platform/fieldcrypt"
	"github.com/BenjaminSRussell/passage/platform/fieldcrypt/azurekv"
)

func main() {
	id := flag.String("id", "", "master key id, for example 2027-01")
	flag.Parse()
	if *id == "" {
		fmt.Fprintln(os.Stderr, "usage: fieldcrypt-wrap -id <key id>")
		os.Exit(2)
	}
	client, err := azurekv.FromEnv(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	entry, err := fieldcrypt.WrapEntry(ctx, *id, key, client)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Println(entry)
}
