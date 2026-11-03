package store

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/bankconnect"
)

func startSFTPServer(t *testing.T, allowed ssh.PublicKey) (addr, hostPrint string) {
	t.Helper()
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	host, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}
	conf := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		if bytes.Equal(k.Marshal(), allowed.Marshal()) {
			return nil, nil
		}
		return nil, os.ErrPermission
	}}
	conf.AddHostKey(host)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(conn, conf)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					ch, creqs, err := nc.Accept()
					if err != nil {
						continue
					}
					go func() {
						for r := range creqs {
							_ = r.Reply(r.Type == "subsystem", nil)
							if r.Type == "subsystem" {
								if srv, err := sftp.NewServer(ch); err == nil {
									_ = srv.Serve()
									_ = srv.Close()
								}
							}
						}
					}()
				}
			}()
		}
	}()
	return ln.Addr().String(), ssh.FingerprintSHA256(host.PublicKey())
}

func TestKeyCustody_NoKeyBytesReachLogsOrTheDatabaseAcrossAFullSend(t *testing.T) {
	s, admin := newStore(t)
	le := ids.New()
	ctx := ctxFor(1, le)
	acct := activeAccount(t, s, ctx, le)

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	pemKey := string(pem.EncodeToMemory(block))
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	const token = "s.custody-test-token"
	vault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != token {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/v1/secret/data/treasury/sftp":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": map[string]string{"private_key": pemKey}}})
		case "/v1/transit/sign/bank-a/sha2-256":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"signature": "vault:v2:" + base64.StdEncoding.EncodeToString([]byte("detached")), "key_version": 2}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(vault.Close)

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	cfg, err := bankconnect.LoadVaultConfig(func(k string) string {
		return map[string]string{"VAULT_ADDR": vault.URL, "VAULT_TOKEN": token, "TREASURY_SIGNING_KEY_NAME": "bank-a", "TREASURY_SFTP_KEY_PATH": "secret/treasury/sftp"}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	transit, err := cfg.Transit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	clientKey, err := cfg.SFTPSigner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	addr, hostPrint := startSFTPServer(t, signer.PublicKey())
	conn, err := bankconnect.DialSSH(ctx, bankconnect.SSHConfig{Addr: addr, User: "bank", HostKeyFingerprint: hostPrint, Signer: clientKey, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	dir := t.TempDir()
	file := bankconnect.File{LegalEntityID: le, BankAccountID: acct.ID, Name: "pain001.xml", OrderType: "CCT", KeyName: cfg.SigningKeyName, Data: []byte("<Document/>")}
	if _, err := (bankconnect.Sender{Signer: transit, Transport: bankconnect.SFTP{Dir: dir, Client: conn}, Audit: s}).Send(ctx, file); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "pain001.xml")); err != nil {
		t.Fatalf("file not delivered: %v", err)
	}

	needles := []string{token, base64.StdEncoding.EncodeToString(priv.Seed()), base64.StdEncoding.EncodeToString(priv)}
	for _, line := range strings.Split(strings.TrimSpace(pemKey), "\n") {
		if len(line) >= 20 && !strings.HasPrefix(line, "-----") {
			needles = append(needles, line)
		}
	}
	if !strings.Contains(logs.String(), "sftp client key loaded") {
		t.Fatal("the key load was not logged, so the log scan proves nothing")
	}
	assertNoNeedles(t, "slog output", logs.String(), needles)

	tables, err := admin.Query(context.Background(), `select tablename from pg_tables where schemaname = current_schema()`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var n string
		if err := tables.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	tables.Close()
	if len(names) == 0 {
		t.Fatal("no tables scanned")
	}
	for _, n := range names {
		var dump string
		if err := admin.QueryRow(context.Background(), fmt.Sprintf(`select coalesce(string_agg(t::text, E'\n'), '') from %q t`, n)).Scan(&dump); err != nil {
			t.Fatalf("scan %s: %v", n, err)
		}
		assertNoNeedles(t, "table "+n, dump, needles)
	}
	var audited int
	if err := admin.QueryRow(context.Background(), `select count(*) from bank_file_audit where key_name = 'bank-a' and event in ('prepared','sent')`).Scan(&audited); err != nil || audited != 2 {
		t.Fatalf("audit rows = %d, %v; want prepared and sent", audited, err)
	}
}

func assertNoNeedles(t *testing.T, where, haystack string, needles []string) {
	t.Helper()
	for _, n := range needles {
		if n != "" && strings.Contains(haystack, n) {
			t.Errorf("%s contains key material or the vault token", where)
		}
	}
}
