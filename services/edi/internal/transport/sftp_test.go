package transport

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

func newSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// startSFTPServer runs an in-process SSH server with the sftp subsystem on the
// real file system; only client may log in. It returns the address and the
// host key fingerprint.
func startSFTPServer(t *testing.T, client ssh.PublicKey) (string, string) {
	t.Helper()
	host := newSigner(t)
	conf := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if string(key.Marshal()) == string(client.Marshal()) {
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
							ok := r.Type == "subsystem" && len(r.Payload) > 4 && string(r.Payload[4:]) == "sftp"
							_ = r.Reply(ok, nil)
							if ok {
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

func TestSFTP_SendAndPullAgainstAnSFTPServer(t *testing.T) {
	key := newSigner(t)
	addr, print := startSFTPServer(t, key.PublicKey())
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
	key := newSigner(t)
	addr, print := startSFTPServer(t, key.PublicKey())
	wrong := SFTPConfig{Addr: addr, User: "edi", HostKeyFingerprint: ssh.FingerprintSHA256(newSigner(t).PublicKey()), Signer: key, Timeout: 5 * time.Second}
	if _, err := DialSFTP(context.Background(), wrong); err == nil || !strings.Contains(err.Error(), "does not match the pinned") {
		t.Fatalf("err = %v", err)
	}
	for _, pin := range []string{"", "SHA256:", strings.TrimPrefix(print, "SHA256:")} {
		if _, err := DialSFTP(context.Background(), SFTPConfig{Addr: addr, User: "edi", HostKeyFingerprint: pin, Signer: key}); err == nil {
			t.Fatalf("pin %q accepted", pin)
		}
	}
	if _, err := DialSFTP(context.Background(), SFTPConfig{Addr: addr, User: "edi", HostKeyFingerprint: print, Signer: newSigner(t), Timeout: 5 * time.Second}); err == nil {
		t.Fatal("an unknown client key logged in")
	}
}
