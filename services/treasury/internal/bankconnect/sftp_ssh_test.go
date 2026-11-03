package bankconnect

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type sshFixture struct {
	addr        string
	hostPrint   string
	clientKey   ssh.Signer
	rootDir     string
	rejectedKey bool
}

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

// newSSHFixture starts an in-process SSH server with the sftp subsystem served
// from the real file system under a temp dir; only the fixture's client key may log in.
func newSSHFixture(t *testing.T) *sshFixture {
	t.Helper()
	host, client := newSigner(t), newSigner(t)
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
			go serveSSH(conn, conf)
		}
	}()
	return &sshFixture{addr: ln.Addr().String(), hostPrint: ssh.FingerprintSHA256(host.PublicKey()), clientKey: client, rootDir: t.TempDir()}
}

func serveSSH(conn net.Conn, conf *ssh.ServerConfig) {
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
					srv, err := sftp.NewServer(ch)
					if err == nil {
						_ = srv.Serve()
						_ = srv.Close()
					}
				}
			}
		}()
	}
}

func (f *sshFixture) config() SSHConfig {
	return SSHConfig{Addr: f.addr, User: "bank", HostKeyFingerprint: f.hostPrint, Signer: f.clientKey, Timeout: 5 * time.Second}
}

func TestSSHClient_UploadsThroughSenderToAnSFTPServer(t *testing.T) {
	fx := newSSHFixture(t)
	client, err := DialSSH(context.Background(), fx.config())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	dir := fx.rootDir
	audit := &memoryAudit{}
	receipt, err := Sender{Signer: &fixedSigner{}, Transport: SFTP{Dir: dir, Client: client}, Audit: audit}.Send(context.Background(), pain001)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Reference != filepath.Join(dir, "pain001-0001.xml") {
		t.Fatalf("receipt = %+v", receipt)
	}
	got, err := os.ReadFile(receipt.Reference)
	if err != nil || string(got) != string(pain001.Data) {
		t.Fatalf("stored file = %q, %v", got, err)
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "pain001-0001.xml,pain001-0001.xml.sig" {
		t.Fatalf("server directory = %v; want the file and its signature, no .part", names)
	}
}

func TestDialSSH_RefusesAWrongHostKeyAndMissingPins(t *testing.T) {
	fx := newSSHFixture(t)
	wrong := fx.config()
	wrong.HostKeyFingerprint = ssh.FingerprintSHA256(newSigner(t).PublicKey())
	if _, err := DialSSH(context.Background(), wrong); err == nil || !strings.Contains(err.Error(), "does not match the pinned") {
		t.Fatalf("wrong fingerprint: err = %v", err)
	}
	none := fx.config()
	none.HostKeyFingerprint = ""
	if _, err := DialSSH(context.Background(), none); err == nil {
		t.Fatal("an empty fingerprint must be refused, not skip host-key checking")
	}
	for _, pin := range []string{"SHA256:", "MD5:aa:bb", strings.TrimPrefix(fx.hostPrint, "SHA256:")} {
		bad := fx.config()
		bad.HostKeyFingerprint = pin
		if _, err := DialSSH(context.Background(), bad); err == nil || strings.Contains(err.Error(), "does not match") {
			t.Fatalf("pin %q must be refused before dialling: %v", pin, err)
		}
	}
	stranger := fx.config()
	stranger.Signer = newSigner(t)
	if _, err := DialSSH(context.Background(), stranger); err == nil {
		t.Fatal("an unknown client key was accepted")
	}
}

func TestSSHClient_PutOnAClosedConnectionFails(t *testing.T) {
	fx := newSSHFixture(t)
	client, err := DialSSH(context.Background(), fx.config())
	if err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	if err := client.Put(context.Background(), filepath.Join(fx.rootDir, "x"), []byte("x")); err == nil {
		t.Fatal("Put on a closed connection succeeded")
	}
}

func TestDialSSH_TrimsWhitespaceAroundThePinnedFingerprint(t *testing.T) {
	fx := newSSHFixture(t)
	padded := fx.config()
	padded.HostKeyFingerprint = " " + fx.hostPrint + "\n"
	client, err := DialSSH(context.Background(), padded)
	if err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
}
