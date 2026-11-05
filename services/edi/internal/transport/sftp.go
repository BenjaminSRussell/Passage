package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// SFTPConfig configures the SFTP connection to a partner. HostKeyFingerprint
// is the partner's host key as `ssh-keygen -lf` prints it (SHA256:...); host
// key checking cannot be skipped. Signer is the client key, supplied by the
// caller from its secret store (this package never reads key files).
type SFTPConfig struct {
	Addr               string
	User               string
	HostKeyFingerprint string
	Signer             ssh.Signer
	Timeout            time.Duration
}

// SFTPFiles is the file system surface the SFTP transport needs; SFTPClient
// implements it over SSH and tests may use any other implementation.
type SFTPFiles interface {
	Put(ctx context.Context, remotePath string, data []byte) error
	Rename(ctx context.Context, from, to string) error
	List(ctx context.Context, dir string) ([]string, error)
	Get(ctx context.Context, remotePath string) ([]byte, error)
}

// SFTP sends to OutDir and pulls from InDir. Upload writes "<name>.part" and
// renames it, so the partner never picks up a half-written file; a pulled
// file is moved to InDir/ArchiveDir (default "processed") after the handler
// accepts it, so a crash re-delivers rather than loses it.
type SFTP struct {
	Files      SFTPFiles
	OutDir     string
	InDir      string
	ArchiveDir string
	Now        func() time.Time
}

// Send uploads the payload atomically and returns the final remote path.
func (s SFTP) Send(ctx context.Context, m Message) (Receipt, error) {
	if err := m.validate(); err != nil {
		return Receipt{}, err
	}
	final := path.Join(s.OutDir, m.filename())
	if err := s.Files.Put(ctx, final+".part", m.Payload); err != nil {
		return Receipt{}, fmt.Errorf("sftp put: %w", err)
	}
	if err := s.Files.Rename(ctx, final+".part", final); err != nil {
		return Receipt{}, fmt.Errorf("sftp rename: %w", err)
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	return Receipt{Transport: "sftp", MessageID: m.ID, Reference: final, Disposition: "uploaded", At: now().UTC()}, nil
}

// Pull lists InDir in name order, skips ".part" files and directories the
// listing reports with a trailing slash, hands each file to handle and, when
// handle returns nil, moves it to the archive directory. It stops at the first
// error and reports how many files were handled.
func (s SFTP) Pull(ctx context.Context, handle func(name string, data []byte) error) (int, error) {
	names, err := s.Files.List(ctx, s.InDir)
	if err != nil {
		return 0, fmt.Errorf("sftp list: %w", err)
	}
	sort.Strings(names)
	archive := s.ArchiveDir
	if archive == "" {
		archive = "processed"
	}
	n := 0
	for _, name := range names {
		if strings.HasSuffix(name, ".part") || strings.HasSuffix(name, "/") || name == archive {
			continue
		}
		data, err := s.Files.Get(ctx, path.Join(s.InDir, name))
		if err != nil {
			return n, fmt.Errorf("sftp get %s: %w", name, err)
		}
		if err := handle(name, data); err != nil {
			return n, err
		}
		if err := s.Files.Rename(ctx, path.Join(s.InDir, name), path.Join(s.InDir, archive, name)); err != nil {
			return n, fmt.Errorf("sftp archive %s: %w", name, err)
		}
		n++
	}
	return n, nil
}

const defaultSFTPTimeout = 30 * time.Second

// SFTPClient is SFTPFiles over one SSH connection.
type SFTPClient struct {
	ssh     *ssh.Client
	sftp    *sftp.Client
	timeout time.Duration
}

// DialSFTP connects, verifies the host key against the pinned fingerprint and
// authenticates with the client key.
func DialSFTP(ctx context.Context, cfg SFTPConfig) (*SFTPClient, error) {
	pinned := strings.TrimSpace(cfg.HostKeyFingerprint)
	if !strings.HasPrefix(pinned, "SHA256:") || len(pinned) == len("SHA256:") || cfg.Signer == nil || cfg.User == "" || cfg.Addr == "" {
		return nil, errors.New("sftp: address, user, client key and a SHA256: host key fingerprint are all required")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultSFTPTimeout
	}
	conf := &ssh.ClientConfig{
		User: cfg.User,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(cfg.Signer)},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if got := ssh.FingerprintSHA256(key); got != pinned {
				return fmt.Errorf("sftp: host key %s does not match the pinned %s", got, pinned)
			}
			return nil
		},
		Timeout: timeout,
	}
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("sftp dial: %w", err)
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))
	sc, chans, reqs, err := ssh.NewClientConn(conn, cfg.Addr, conf)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("sftp handshake: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})
	client := ssh.NewClient(sc, chans, reqs)
	fs, err := sftp.NewClient(client)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("sftp session: %w", err)
	}
	return &SFTPClient{ssh: client, sftp: fs, timeout: timeout}, nil
}

// Close ends the SFTP session and the SSH connection.
func (c *SFTPClient) Close() error { return errors.Join(c.sftp.Close(), c.ssh.Close()) }

// within runs op and gives up when ctx ends or the timeout passes; the
// connection is then closed, because an SFTP request cannot be cancelled.
func (c *SFTPClient) within(ctx context.Context, op func() error) error {
	done := make(chan error, 1)
	go func() { done <- op() }()
	timer := time.NewTimer(c.timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = c.Close()
		return ctx.Err()
	case <-timer.C:
		_ = c.Close()
		return errors.New("sftp operation timed out")
	}
}

func (c *SFTPClient) Put(ctx context.Context, remotePath string, data []byte) error {
	return c.within(ctx, func() error {
		f, err := c.sftp.OpenFile(remotePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
		if err != nil {
			return err
		}
		if _, err := f.Write(data); err != nil {
			_ = f.Close()
			return err
		}
		return f.Close()
	})
}

// Rename moves a file, replacing the target (POSIX rename where the server
// supports it), and creates the target directory when it is missing.
func (c *SFTPClient) Rename(ctx context.Context, from, to string) error {
	return c.within(ctx, func() error {
		_ = c.sftp.MkdirAll(path.Dir(to))
		if err := c.sftp.PosixRename(from, to); err == nil {
			return nil
		}
		return c.sftp.Rename(from, to)
	})
}

func (c *SFTPClient) List(ctx context.Context, dir string) ([]string, error) {
	var names []string
	err := c.within(ctx, func() error {
		infos, err := c.sftp.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, fi := range infos {
			name := fi.Name()
			if fi.IsDir() {
				name += "/"
			}
			names = append(names, name)
		}
		return nil
	})
	return names, err
}

func (c *SFTPClient) Get(ctx context.Context, remotePath string) ([]byte, error) {
	var data []byte
	err := c.within(ctx, func() error {
		f, err := c.sftp.Open(remotePath)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		data, err = io.ReadAll(f)
		return err
	})
	return data, err
}
