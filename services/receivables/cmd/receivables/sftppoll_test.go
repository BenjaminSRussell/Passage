package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/statementpoll"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

func writeKey(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func sourceJSON(t *testing.T, mutate func(*sftpSource)) string {
	t.Helper()
	s := sftpSource{TenantID: ids.New(), LegalEntityID: ids.New(), Addr: "sftp.bank.example:22", User: "ar", HostKeyFingerprint: "SHA256:abc",
		Inbox: "/in", Done: "/done", Failed: "/failed"}
	if mutate != nil {
		mutate(&s)
	}
	b, err := json.Marshal([]sftpSource{s})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type noopImporter struct{}

func (noopImporter) ImportBankStatement(context.Context, store.BankStatementImport) (store.BankStatementBatch, error) {
	return store.BankStatementBatch{}, nil
}
func (noopImporter) AutoApplyPayments(context.Context, []string) (store.AutoApplyResult, error) {
	return store.AutoApplyResult{}, nil
}

func TestSFTPPollJob_Config(t *testing.T) {
	key := writeKey(t)
	if j, err := newSFTPPollJob(env(nil), noopImporter{}); j != nil || err != nil {
		t.Fatalf("unset: %v %v", j, err)
	}
	good := env(map[string]string{"RECEIVABLES_SFTP_SOURCES": sourceJSON(t, nil), "RECEIVABLES_SFTP_KEY_FILE": key})
	if j, err := newSFTPPollJob(good, noopImporter{}); err != nil || j == nil || len(j.sources) != 1 || j.sched.interval.Minutes() != 15 {
		t.Fatalf("good config: %+v %v", j, err)
	}
	for name, m := range map[string]map[string]string{
		"not json":       {"RECEIVABLES_SFTP_SOURCES": "inbox", "RECEIVABLES_SFTP_KEY_FILE": key},
		"no key file":    {"RECEIVABLES_SFTP_SOURCES": sourceJSON(t, nil)},
		"unreadable key": {"RECEIVABLES_SFTP_SOURCES": sourceJSON(t, nil), "RECEIVABLES_SFTP_KEY_FILE": filepath.Join(t.TempDir(), "missing")},
		"no pin":         {"RECEIVABLES_SFTP_SOURCES": sourceJSON(t, func(s *sftpSource) { s.HostKeyFingerprint = "" }), "RECEIVABLES_SFTP_KEY_FILE": key},
		"bad tenant":     {"RECEIVABLES_SFTP_SOURCES": sourceJSON(t, func(s *sftpSource) { s.TenantID = "nope" }), "RECEIVABLES_SFTP_KEY_FILE": key},
		"one directory":  {"RECEIVABLES_SFTP_SOURCES": sourceJSON(t, func(s *sftpSource) { s.Done = s.Inbox }), "RECEIVABLES_SFTP_KEY_FILE": key},
		"no inbox":       {"RECEIVABLES_SFTP_SOURCES": sourceJSON(t, func(s *sftpSource) { s.Inbox = "" }), "RECEIVABLES_SFTP_KEY_FILE": key},
		"short interval": {"RECEIVABLES_SFTP_SOURCES": sourceJSON(t, nil), "RECEIVABLES_SFTP_KEY_FILE": key, "RECEIVABLES_SFTP_POLL_INTERVAL": "5s"},
	} {
		if _, err := newSFTPPollJob(env(m), noopImporter{}); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

type emptyServer struct{ listed []string }

func (e *emptyServer) List(_ context.Context, dir string) ([]string, error) {
	e.listed = append(e.listed, dir)
	return nil, nil
}
func (e *emptyServer) Read(context.Context, string, int) ([]byte, error) { return nil, nil }
func (e *emptyServer) Move(context.Context, string, string) error        { return nil }

func TestSFTPPollJob_PollsEachSourceUnderItsOwnPrincipalAndSurvivesADialFailure(t *testing.T) {
	key := writeKey(t)
	a, b := ids.New(), ids.New()
	leA, leB := ids.New(), ids.New()
	raw, _ := json.Marshal([]sftpSource{
		{TenantID: a, LegalEntityID: leA, Addr: "down:22", User: "ar", HostKeyFingerprint: "SHA256:x", Inbox: "/in-a", Done: "/d", Failed: "/f"},
		{TenantID: b, LegalEntityID: leB, Addr: "up:22", User: "ar", HostKeyFingerprint: "SHA256:y", Inbox: "/in-b", Done: "/d", Failed: "/f"},
	})
	job, err := newSFTPPollJob(env(map[string]string{"RECEIVABLES_SFTP_SOURCES": string(raw), "RECEIVABLES_SFTP_KEY_FILE": key}), noopImporter{})
	if err != nil || job == nil {
		t.Fatal(err)
	}
	server := &emptyServer{}
	var dialed []string
	job.dial = func(ctx context.Context, cfg statementpoll.SSHConfig) (statementpoll.Client, func() error, error) {
		dialed = append(dialed, cfg.Addr)
		if cfg.Addr == "down:22" {
			return nil, nil, errors.New("connection refused")
		}
		if p, _ := tenant.FromContext(ctx); p.TenantID != b || p.LegalEntityID != leB || p.UserID != sftpPollActor {
			t.Errorf("poll principal = %+v", p)
		}
		return server, func() error { return nil }, nil
	}
	job.once(context.Background())
	if len(dialed) != 2 || len(server.listed) != 1 || server.listed[0] != "/in-b" {
		t.Fatalf("dialed %v, listed %v: the failing source must not stop the other", dialed, server.listed)
	}
}
