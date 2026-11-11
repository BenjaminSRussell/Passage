package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ServiceKeys holds the per-service Ed25519 seeds that sign service-to-service tokens. They are created on first start and
// kept in <data>\keys with owner-only permissions (on Windows, the directory inherits the service account's ACL).
type ServiceKeys map[string][]byte

// LoadOrCreateKeys reads or creates one seed file per service name.
func LoadOrCreateKeys(dir string, names []string) (ServiceKeys, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	keys := ServiceKeys{}
	for _, n := range names {
		p := filepath.Join(dir, n+".seed")
		b, err := os.ReadFile(p)
		switch {
		case err == nil:
			seed, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
			if derr != nil || len(seed) != ed25519.SeedSize {
				return nil, fmt.Errorf("signing key %s is corrupt", p)
			}
			keys[n] = seed
		case errors.Is(err, os.ErrNotExist):
			seed := make([]byte, ed25519.SeedSize)
			if _, err := rand.Read(seed); err != nil {
				return nil, err
			}
			if err := os.WriteFile(p, []byte(base64.StdEncoding.EncodeToString(seed)), 0o600); err != nil {
				return nil, err
			}
			keys[n] = seed
		default:
			return nil, err
		}
	}
	return keys, nil
}

// Apply adds each process's signing key and the public keys of its peers to its environment. Which peer may call which
// operation is decided by the grant table in platform/go/tenant, not by who holds a key.
func (k ServiceKeys) Apply(procs []Process) {
	names := make([]string, 0, len(k))
	for n := range k {
		names = append(names, n)
	}
	sort.Strings(names)
	for i := range procs {
		p := &procs[i]
		if p.Env == nil {
			p.Env = map[string]string{}
		}
		p.Env["PASSAGE_SERVICE_SIGNING_KEY"] = base64.StdEncoding.EncodeToString(k[p.Name])
		var pubs []string
		for _, n := range names {
			if n == p.Name {
				continue
			}
			pub := ed25519.NewKeyFromSeed(k[n]).Public().(ed25519.PublicKey)
			pubs = append(pubs, n+"="+base64.StdEncoding.EncodeToString(pub))
		}
		p.Env["PASSAGE_SERVICE_PUBLIC_KEYS"] = strings.Join(pubs, ",")
	}
}
