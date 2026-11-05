package transport

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/BenjaminSRussell/passage/services/edi/internal/transport/transporttest"
)

func TestSFTP_SendAndPullAgainstAnSFTPServer(t *testing.T) {
	key := transporttest.NewSigner(t)
	addr, print := transporttest.StartSFTPServer(t, transporttest.SFTPLogin{Key: key.PublicKey()})
	client, err := DialSFTP(context.Background(), SFTPConfig{Addr: addr, User: "edi", HostKeyFingerprint: print, Signer: key, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	root := t.TempDir()
	out, in := filepath.Join(root, "out"), filepath.Join(root, "in")
	for _, d := range []string{out, in} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tr := SFTP{Files: client, OutDir: out, InDir: in, Now: func() time.Time { return testAt }}
	r, err := tr.Send(context.Background(), x12Message("passage-000000042"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Reference != filepath.Join(out, "passage-000000042.x12") {
		t.Fatalf("receipt %+v", r)
	}
	got, err := os.ReadFile(r.Reference)
	if err != nil || string(got) != x12Payload {
		t.Fatalf("uploaded %q %v", got, err)
	}
	entries, _ := os.ReadDir(out)
	if len(entries) != 1 {
		t.Fatalf("out dir %v: the .part file must be renamed away", entries)
	}

	// Inbound: two partner files, one still being written.
	for name, body := range map[string]string{"b.edi": "UNB...2", "a.edi": "UNB...1", "c.edi.part": "half"} {
		if err := os.WriteFile(filepath.Join(in, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var seen []string
	n, err := tr.Pull(context.Background(), func(name string, data []byte) error {
		seen = append(seen, name+"="+string(data))
		return nil
	})
	if err != nil || n != 2 || strings.Join(seen, ",") != "a.edi=UNB...1,b.edi=UNB...2" {
		t.Fatalf("pulled %d %v %v", n, seen, err)
	}
	for _, name := range []string{"a.edi", "b.edi"} {
		if _, err := os.Stat(filepath.Join(in, "processed", name)); err != nil {
			t.Fatalf("%s not archived: %v", name, err)
		}
	}
	// A handler failure leaves the file in place for the next pull.
	if err := os.WriteFile(filepath.Join(in, "d.edi"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if n, err := tr.Pull(context.Background(), func(string, []byte) error { return errors.New("mapping failed") }); err == nil || n != 0 {
		t.Fatalf("%d %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(in, "d.edi")); err != nil {
		t.Fatal("a failed file must stay in the inbox")
	}
}

func TestDialSFTP_PinsTheHostKey(t *testing.T) {
	key := transporttest.NewSigner(t)
	addr, print := transporttest.StartSFTPServer(t, transporttest.SFTPLogin{Key: key.PublicKey()})
	wrong := SFTPConfig{Addr: addr, User: "edi", HostKeyFingerprint: ssh.FingerprintSHA256(transporttest.NewSigner(t).PublicKey()), Signer: key, Timeout: 5 * time.Second}
	if _, err := DialSFTP(context.Background(), wrong); err == nil || !strings.Contains(err.Error(), "does not match the pinned") {
		t.Fatalf("err = %v", err)
	}
	for _, pin := range []string{"", "SHA256:", strings.TrimPrefix(print, "SHA256:")} {
		if _, err := DialSFTP(context.Background(), SFTPConfig{Addr: addr, User: "edi", HostKeyFingerprint: pin, Signer: key}); err == nil {
			t.Fatalf("pin %q accepted", pin)
		}
	}
	if _, err := DialSFTP(context.Background(), SFTPConfig{Addr: addr, User: "edi", HostKeyFingerprint: print, Signer: transporttest.NewSigner(t), Timeout: 5 * time.Second}); err == nil {
		t.Fatal("an unknown client key logged in")
	}
}

func TestSFTP_PasswordLoginAndFileSizeLimit(t *testing.T) {
	addr, print := transporttest.StartSFTPServer(t, transporttest.SFTPLogin{User: "edi", Password: "s3cret"})
	if _, err := DialSFTP(context.Background(), SFTPConfig{Addr: addr, User: "edi", HostKeyFingerprint: print, Password: "wrong", Timeout: 5 * time.Second}); err == nil {
		t.Fatal("a wrong password logged in")
	}
	client, err := DialSFTP(context.Background(), SFTPConfig{Addr: addr, User: "edi", HostKeyFingerprint: print, Password: "s3cret", Timeout: 5 * time.Second, MaxFileBytes: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "big.edi"), []byte("123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ok.edi"), []byte("12345678"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get(context.Background(), filepath.Join(dir, "big.edi")); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("a file over the limit: %v", err)
	}
	if got, err := client.Get(context.Background(), filepath.Join(dir, "ok.edi")); err != nil || string(got) != "12345678" {
		t.Fatalf("%q %v", got, err)
	}
}
