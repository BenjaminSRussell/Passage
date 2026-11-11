package statementpoll

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// The SFTP tests run against an in-process SSH server that serves the sftp subsystem from a temp directory, so the real client,
// host-key pin and file moves are exercised with no outside server.

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

type fixture struct {
	cfg  SSHConfig
	root string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	client, host := newSigner(t), newSigner(t)
	conf := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if string(key.Marshal()) == string(client.PublicKey().Marshal()) {
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
			go serve(conn, conf)
		}
	}()
	return &fixture{root: t.TempDir(), cfg: SSHConfig{Addr: ln.Addr().String(), User: "bank", HostKeyFingerprint: ssh.FingerprintSHA256(host.PublicKey()), Signer: client, Timeout: 5 * time.Second}}
}

func serve(conn net.Conn, conf *ssh.ServerConfig) {
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
}

func TestSSHClient_PollsAnInboxOverSFTP(t *testing.T) {
	fx := newFixture(t)
	for _, d := range []string{"in", "done", "failed"} {
		if err := os.Mkdir(filepath.Join(fx.root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(fx.root, "in", name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("stmt-1.xml", fmt.Sprintf(camtFile, 1, 1))
	write("junk.xml", "this is not xml")
	write("readme.txt", "leave me")
	if err := os.Mkdir(filepath.Join(fx.root, "in", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	client, err := DialSSH(context.Background(), fx.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	source := Source{LegalEntityID: "le-1", Inbox: filepath.Join(fx.root, "in"), Done: filepath.Join(fx.root, "done"), Failed: filepath.Join(fx.root, "failed")}
	imp := &fakeImporter{seen: map[string]bool{}}
	res, err := Poll(context.Background(), client, imp, source)
	if err != nil || res.Imported != 1 || res.Failed != 1 {
		t.Fatalf("poll = %+v, %v", res, err)
	}
	for _, want := range []string{"done/stmt-1.xml", "failed/junk.xml", "in/readme.txt", "in/sub"} {
		if _, err := os.Stat(filepath.Join(fx.root, want)); err != nil {
			t.Errorf("%s: %v", want, err)
		}
	}
	if _, err := os.Stat(filepath.Join(fx.root, "in", "stmt-1.xml")); err == nil {
		t.Error("the imported file is still in the inbox")
	}
	// The same name again (the bank re-sent it) is archived under another name, not over the first.
	write("stmt-1.xml", fmt.Sprintf(camtFile, 1, 1))
	if res, err := Poll(context.Background(), client, imp, source); err != nil || res.Duplicates != 1 {
		t.Fatalf("re-sent file: %+v, %v", res, err)
	}
	entries, _ := os.ReadDir(filepath.Join(fx.root, "done"))
	if len(entries) != 2 {
		t.Fatalf("done holds %d files, want the original and the re-sent one", len(entries))
	}
}

func TestDialSSH_PinsTheHostKeyAndNeedsItsInputs(t *testing.T) {
	fx := newFixture(t)
	wrong := fx.cfg
	wrong.HostKeyFingerprint = ssh.FingerprintSHA256(newSigner(t).PublicKey())
	if _, err := DialSSH(context.Background(), wrong); err == nil {
		t.Fatal("a host key that does not match the pin was accepted")
	}
	for name, mutate := range map[string]func(*SSHConfig){
		"no fingerprint": func(c *SSHConfig) { c.HostKeyFingerprint = "" },
		"no prefix":      func(c *SSHConfig) { c.HostKeyFingerprint = "abc" },
		"no key":         func(c *SSHConfig) { c.Signer = nil },
		"no user":        func(c *SSHConfig) { c.User = "" },
		"no address":     func(c *SSHConfig) { c.Addr = "" },
	} {
		cfg := fx.cfg
		mutate(&cfg)
		if _, err := DialSSH(context.Background(), cfg); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	stranger := fx.cfg
	stranger.Signer = newSigner(t)
	if _, err := DialSSH(context.Background(), stranger); err == nil {
		t.Fatal("a client key the server does not know was accepted")
	}
}
