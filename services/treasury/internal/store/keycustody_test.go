package store

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
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

type ambiguousTransport struct{}

func (ambiguousTransport) Protocol() string { return bankconnect.ProtocolSFTP }
func (ambiguousTransport) Send(context.Context, bankconnect.File, bankconnect.Signature) (bankconnect.Receipt, error) {
	return bankconnect.Receipt{}, fmt.Errorf("rename: %w", bankconnect.ErrAmbiguous)
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
		case "/v1/transit/keys/sftp-ed":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"type": "ed25519", "latest_version": 1, "keys": map[string]any{"1": map[string]string{"public_key": base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))}}}})
		case "/v1/transit/sign/sftp-ed":
			var in struct{ Input string }
			_ = json.NewDecoder(r.Body).Decode(&in)
			data, _ := base64.StdEncoding.DecodeString(in.Input)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"signature": "vault:v1:" + base64.StdEncoding.EncodeToString(ed25519.Sign(priv, data))}})
		case "/v1/transit/sign/bank-a/sha2-256":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"signature": "vault:v2:" + base64.StdEncoding.EncodeToString([]byte("detached")), "key_version": 2}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(vault.Close)

	var logs bytes.Buffer
	prevSlog, prevLog, prevErr := slog.Default(), log.Writer(), os.Stderr
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	log.SetOutput(&logs)
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = pw
	stderrDone := make(chan string)
	go func() { b, _ := io.ReadAll(pr); stderrDone <- string(b) }()
	restore := func() {
		slog.SetDefault(prevSlog)
		log.SetOutput(prevLog)
		os.Stderr = prevErr
	}
	t.Cleanup(restore)

	cfg, err := bankconnect.LoadVaultConfig(func(k string) string {
		return map[string]string{"VAULT_ADDR": vault.URL, "VAULT_TOKEN": token, "TREASURY_SIGNING_KEY_NAMES": "bank-a", "TREASURY_SFTP_KEY_NAME": "sftp-ed", "TREASURY_SFTP_KEY_VERSION": "1", "TREASURY_BANK_CHANNELS": "sftp"}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	v := bankconnect.NewVault(cfg)
	clientKey, err := v.SFTPSigner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(clientKey.PublicKey().Marshal()) != string(signer.PublicKey().Marshal()) {
		t.Fatal("the signer does not carry the transit public key")
	}
	addr, hostPrint := startSFTPServer(t, signer.PublicKey())
	conn, err := bankconnect.DialSSH(ctx, bankconnect.SSHConfig{Addr: addr, User: "bank", HostKeyFingerprint: hostPrint, Signer: clientKey, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("login through a transit-backed key: %v", err)
	}
	defer conn.Close()

	var errorText strings.Builder
	dir := t.TempDir()
	file := bankconnect.File{LegalEntityID: le, BankAccountID: acct.ID, Name: "pain001.xml", OrderType: "CCT", KeyName: "bank-a", Data: []byte("<Document/>")}
	if _, err := (bankconnect.Sender{Signer: v.Signer(), Transport: bankconnect.SFTP{Dir: dir, Client: conn}, Audit: s}).Send(ctx, file); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "pain001.xml")); err != nil {
		t.Fatalf("file not delivered: %v", err)
	}
	refused := file
	refused.KeyName = "not-allowed"
	_, err = (bankconnect.Sender{Signer: v.Signer(), Transport: bankconnect.SFTP{Dir: dir, Client: conn}, Audit: s}).Send(ctx, refused)
	if err == nil {
		t.Fatal("a key name off the allow-list was signed with")
	}
	errorText.WriteString(err.Error())
	_, err = (bankconnect.Sender{Signer: v.Signer(), Transport: ambiguousTransport{}, Audit: s}).Send(ctx, file)
	if !errors.Is(err, bankconnect.ErrAmbiguous) {
		t.Fatalf("ambiguous send: %v", err)
	}
	errorText.WriteString(err.Error())
	restore()
	_ = pw.Close()
	stderr := <-stderrDone

	seed := priv.Seed()
	needles := []string{token, base64.StdEncoding.EncodeToString(seed), base64.StdEncoding.EncodeToString(priv), base64.RawStdEncoding.EncodeToString(seed), hex.EncodeToString(seed), hex.EncodeToString(priv)}
	if !strings.Contains(logs.String(), "bound to vault transit") {
		t.Fatal("the key binding was not logged, so the log scan proves nothing")
	}
	assertNoNeedles(t, "slog and log output", logs.String(), needles)
	assertNoNeedles(t, "stderr", stderr, needles)
	assertNoNeedles(t, "error strings", errorText.String(), needles)

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
	rows, err := admin.Query(context.Background(), `select event from bank_file_audit`)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for rows.Next() {
		var e string
		_ = rows.Scan(&e)
		counts[e]++
	}
	rows.Close()
	if counts["prepared"] != 3 || counts["sent"] != 1 || counts["failed"] != 1 || counts["unknown"] != 1 {
		t.Fatalf("audit events = %v; want 3 prepared, 1 sent, 1 failed, 1 unknown", counts)
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
