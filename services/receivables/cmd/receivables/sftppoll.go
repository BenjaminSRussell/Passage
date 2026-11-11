package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/statementpoll"
)

const (
	sftpPollActor           = "system:receivables-sftp-poll"
	defaultSFTPPollInterval = 15 * time.Minute
	sftpPollTimeout         = 10 * time.Minute
)

// sftpSource is one bank inbox from RECEIVABLES_SFTP_SOURCES.
type sftpSource struct {
	TenantID           string `json:"tenantId"`
	LegalEntityID      string `json:"legalEntityId"`
	Addr               string `json:"addr"`
	User               string `json:"user"`
	HostKeyFingerprint string `json:"hostKeyFingerprint"`
	Inbox              string `json:"inbox"`
	Done               string `json:"done"`
	Failed             string `json:"failed"`
	AutoApply          bool   `json:"autoApply"`
}

// sftpDialer connects to a bank's server; the production one is statementpoll.DialSSH.
type sftpDialer func(ctx context.Context, cfg statementpoll.SSHConfig) (statementpoll.Client, func() error, error)

func dialSSH(ctx context.Context, cfg statementpoll.SSHConfig) (statementpoll.Client, func() error, error) {
	c, err := statementpoll.DialSSH(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	return c, c.Close, nil
}

// sftpPollJob polls the bank inboxes in RECEIVABLES_SFTP_SOURCES on a schedule (FR-30.5, 30-4: every 15 minutes by default,
// RECEIVABLES_SFTP_POLL_INTERVAL or RECEIVABLES_SFTP_POLL_AT): each camt.053 file in an inbox is imported like an uploaded one and
// moved to the source's done directory. The client key is read from RECEIVABLES_SFTP_KEY_FILE (a Secret mount); each server's
// host key is pinned by its SHA256 fingerprint. Off until sources are set.
type sftpPollJob struct {
	sched   schedule
	sources []sftpSource
	signer  ssh.Signer
	imp     statementpoll.Importer
	dial    sftpDialer
}

func newSFTPPollJob(getenv func(string) string, imp statementpoll.Importer) (*sftpPollJob, error) {
	raw := strings.TrimSpace(getenv("RECEIVABLES_SFTP_SOURCES"))
	if raw == "" {
		slog.Info("receivables SFTP poll disabled; RECEIVABLES_SFTP_SOURCES is not set")
		return nil, nil
	}
	var sources []sftpSource
	if err := json.Unmarshal([]byte(raw), &sources); err != nil {
		return nil, fmt.Errorf("RECEIVABLES_SFTP_SOURCES must be a JSON array of sources: %w", err)
	}
	if len(sources) == 0 {
		return nil, nil
	}
	for i, s := range sources {
		for name, v := range map[string]string{"addr": s.Addr, "user": s.User, "inbox": s.Inbox, "done": s.Done, "failed": s.Failed} {
			if strings.TrimSpace(v) == "" {
				return nil, fmt.Errorf("RECEIVABLES_SFTP_SOURCES[%d] needs %s", i, name)
			}
		}
		if !ids.Valid(s.TenantID) || !ids.Valid(s.LegalEntityID) {
			return nil, fmt.Errorf("RECEIVABLES_SFTP_SOURCES[%d] needs UUID tenantId and legalEntityId", i)
		}
		if !strings.HasPrefix(s.HostKeyFingerprint, "SHA256:") {
			return nil, fmt.Errorf("RECEIVABLES_SFTP_SOURCES[%d] needs the bank's hostKeyFingerprint (SHA256:...)", i)
		}
		if s.Inbox == s.Done || s.Inbox == s.Failed || s.Done == s.Failed {
			return nil, fmt.Errorf("RECEIVABLES_SFTP_SOURCES[%d] needs three different directories", i)
		}
	}
	keyFile := getenv("RECEIVABLES_SFTP_KEY_FILE")
	if keyFile == "" {
		return nil, errors.New("RECEIVABLES_SFTP_SOURCES needs RECEIVABLES_SFTP_KEY_FILE, the client private key")
	}
	pem, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("read RECEIVABLES_SFTP_KEY_FILE: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(pem)
	if err != nil {
		return nil, fmt.Errorf("RECEIVABLES_SFTP_KEY_FILE is not a usable private key: %w", err)
	}
	sched, err := parseSchedule(getenv, "RECEIVABLES_SFTP_POLL", defaultSFTPPollInterval)
	if err != nil {
		return nil, err
	}
	return &sftpPollJob{sched: sched, sources: sources, signer: signer, imp: imp, dial: dialSSH}, nil
}

func (j *sftpPollJob) loop(ctx context.Context) { j.sched.run(ctx, j.once) }

func (j *sftpPollJob) once(ctx context.Context) {
	for _, src := range j.sources {
		if err := j.pollOne(ctx, src); err != nil {
			slog.Error("receivables SFTP poll failed", "tenantId", src.TenantID, "legalEntityId", src.LegalEntityID, "addr", src.Addr, "err", err)
		}
	}
}

func (j *sftpPollJob) pollOne(ctx context.Context, src sftpSource) error {
	ctx, cancel := context.WithTimeout(tenant.With(ctx, tenant.Principal{TenantID: src.TenantID, LegalEntityID: src.LegalEntityID, UserID: sftpPollActor}), sftpPollTimeout)
	defer cancel()
	client, closeFn, err := j.dial(ctx, statementpoll.SSHConfig{Addr: src.Addr, User: src.User, HostKeyFingerprint: src.HostKeyFingerprint, Signer: j.signer})
	if err != nil {
		return err
	}
	defer func() { _ = closeFn() }()
	res, err := statementpoll.Poll(ctx, client, j.imp, statementpoll.Source{LegalEntityID: src.LegalEntityID, Inbox: src.Inbox, Done: src.Done, Failed: src.Failed, AutoApply: src.AutoApply})
	if res.Imported > 0 || res.Duplicates > 0 || res.Failed > 0 {
		slog.Info("receivables SFTP poll finished", "tenantId", src.TenantID, "legalEntityId", src.LegalEntityID, "imported", res.Imported,
			"duplicates", res.Duplicates, "failed", res.Failed, "payments", res.Payments, "applied", res.Applied, "more", res.More)
	}
	return err
}
