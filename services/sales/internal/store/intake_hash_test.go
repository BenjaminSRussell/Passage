package store

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/sales/migrations"
)

// TestPostgres_IntakeHashOfOneTimeRequestIsKeyed covers the slice 20 follow-up:
// the request hash of a refused one-time request is an HMAC, not a plain
// digest a guessed buyer could be checked against; an ordinary request keeps
// its plain hash; a retry still raises attempts, also after the current key
// rotated; a service without keys uses a constant stand-in.
func TestPostgres_IntakeHashOfOneTimeRequestIsKeyed(t *testing.T) {
	s, pool, ctx := migrated(t)
	s1 := s.WithFieldCrypt(testKeys(t, "k1", "k1"))
	le := ids.New()
	now := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	body := []byte(`{"channel":"WEB","oneTime":{"name":"Ada Buyer","taxId":"DE123456789"}}`)
	digest := "plain-sha256-of-the-request"
	in := NewIntakeError{LegalEntityID: le, Channel: "WEB", ExternalRef: "PO-K", RequestHash: digest, ErrorCode: CodeProductNotListed,
		Message: "not listed", Payload: body, PersonalData: true}

	first, err := s1.RecordIntakeError(ctx, in, now)
	if err != nil || !strings.HasPrefix(first.RequestHash, "hmac:k1:") || strings.Contains(first.RequestHash, digest) {
		t.Fatalf("one-time hash is keyed: %v %+v", err, first)
	}
	again, err := s1.RecordIntakeError(ctx, in, now.Add(time.Hour))
	if err != nil || again.ID != first.ID || again.Attempts != 2 {
		t.Fatalf("a retry raises attempts: %v %+v", err, again)
	}
	// Another request under the same reference is its own row.
	other := in
	other.RequestHash = "another-digest"
	if o, err := s1.RecordIntakeError(ctx, other, now); err != nil || o.ID == first.ID {
		t.Fatalf("another body, another row: %v %+v", err, o)
	}
	// After a rotation the retry finds the row recorded under the old key.
	s2 := s.WithFieldCrypt(testKeys(t, "k2", "k1", "k2"))
	rotated, err := s2.RecordIntakeError(ctx, in, now.Add(2*time.Hour))
	if err != nil || rotated.ID != first.ID || rotated.Attempts != 3 || rotated.RequestHash != first.RequestHash {
		t.Fatalf("dedupe survives a key rotation: %v %+v", err, rotated)
	}
	var rows int
	if err := pool.QueryRow(ctx, `select count(*) from intake_error where legal_entity_id = $1`, le).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("rows: %v %d", err, rows)
	}
	// A new request under the new key is stored under it.
	in3 := in
	in3.ExternalRef = "PO-K3"
	if n, err := s2.RecordIntakeError(ctx, in3, now); err != nil || !strings.HasPrefix(n.RequestHash, "hmac:k2:") {
		t.Fatalf("new rows use the current key: %v %+v", err, n)
	}

	// A key dropped from FIELDCRYPT_KEYS cannot be recomputed: the retry opens
	// a row under the current key, and the next retry finds that row.
	s3 := s.WithFieldCrypt(testKeys(t, "k3", "k3"))
	dropped, err := s3.RecordIntakeError(ctx, in, now.Add(3*time.Hour))
	if err != nil || dropped.ID == first.ID || !strings.HasPrefix(dropped.RequestHash, "hmac:k3:") || dropped.Attempts != 1 {
		t.Fatalf("old key gone: %v %+v", err, dropped)
	}
	if again, err := s3.RecordIntakeError(ctx, in, now.Add(4*time.Hour)); err != nil || again.ID != dropped.ID || again.Attempts != 2 {
		t.Fatalf("retry under the new key: %v %+v", err, again)
	}
	s1 = s.WithFieldCrypt(testKeys(t, "k1", "k1"))

	// An ordinary request carries no buyer: its plain hash stays.
	ord := NewIntakeError{LegalEntityID: le, Channel: "WEB", ExternalRef: "PO-O", RequestHash: digest, ErrorCode: CodeProductNotListed, Message: "x", Payload: []byte(`{}`)}
	if o, err := s1.RecordIntakeError(ctx, ord, now); err != nil || o.RequestHash != digest {
		t.Fatalf("ordinary request keeps its hash: %v %+v", err, o)
	}
	// Without keys the digest is not stored at all.
	nk, err := New(s.pool).RecordIntakeError(ctx, in, now)
	if err != nil || nk.RequestHash != unkeyedIntakeHash {
		t.Fatalf("no keys: %v %+v", err, nk)
	}
}

// TestPostgres_IntakeKeyedHashMigrationClearsPlainDigests runs the 0035 data
// step over rows as the earlier slices wrote them: a plain digest that may
// cover a one-time buyer is cleared (sealed body, no kept body, or a clear body
// with a oneTime member); an ordinary request, a keyed hash and a cleared hash
// stay. The cleared rows keep distinct hashes, so the unique key still holds.
func TestPostgres_IntakeKeyedHashMigrationClearsPlainDigests(t *testing.T) {
	_, pool, ctx := migrated(t)
	tenantID, le := ids.New(), ids.New()
	add := func(ref, hash, payload string, sealed bool) string {
		id := ids.New()
		_, err := pool.Exec(ctx, `insert into intake_error (tenant_id, id, legal_entity_id, channel, external_ref, request_hash, error_code, message,
				request_payload, request_payload_sealed)
			values ($1, $2, $3, 'WEB', $4, $5, 'SLS-0001', 'm', nullif($6, '')::jsonb, case when $7 then '\x00'::bytea end)`,
			tenantID, id, le, ref, hash, payload, sealed)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	sealedRow := add("A", "plain", "", true)
	noBody := add("B", "plain", "", false)
	noBody2 := add("B", "plain-2", "", false)
	oneTime := add("C", "plain", `{"oneTime":{"name":"x"}}`, false)
	ordinary := add("D", "plain", `{"channel":"WEB"}`, false)
	keyed := add("E", "hmac:k1:abc", "", true)
	sql, err := migrations.FS.ReadFile("0035_intake_error_keyed_hash.sql")
	if err != nil {
		t.Fatal(err)
	}
	// Deployed, the step runs as the database owner, which is neither a
	// superuser nor exempt from row-level security; intake_error forces RLS
	// and a migration sets no tenant. Run it as such an owner when the
	// application role exists, so a step that silently matched no row fails.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if os.Getenv(testkit.EnvAppURL) != "" {
		if _, err := tx.Exec(ctx, `alter table intake_error owner to passage_app`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `set local role passage_app`); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ { // the step is safe to repeat
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var forced bool
	if err := pool.QueryRow(ctx, `select relforcerowsecurity from pg_class where oid = 'intake_error'::regclass`).Scan(&forced); err != nil || !forced {
		t.Fatalf("intake_error must force row-level security again: %v %v", err, forced)
	}
	hash := func(id string) string {
		var h string
		if err := pool.QueryRow(ctx, `select request_hash from intake_error where id = $1`, id).Scan(&h); err != nil {
			t.Fatal(err)
		}
		return h
	}
	for _, id := range []string{sealedRow, noBody, noBody2, oneTime} {
		if got := hash(id); got != "cleared:"+id {
			t.Fatalf("row %s should be cleared, has %q", id, got)
		}
	}
	if got := hash(ordinary); got != "plain" {
		t.Fatalf("an ordinary request keeps its hash: %q", got)
	}
	if got := hash(keyed); got != "hmac:k1:abc" {
		t.Fatalf("a keyed hash stays: %q", got)
	}
}
