package statementpoll

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

const defaultTimeout = 30 * time.Second

// SSHConfig configures the production SFTP client. HostKeyFingerprint is the bank's host key as `ssh-keygen -lf` prints it
// (SHA256:...); there is no way to skip host-key checking. Signer is the client key, supplied by the caller from its secret store.
type SSHConfig struct {
	Addr               string
	User               string
	HostKeyFingerprint string
	Signer             ssh.Signer
	Timeout            time.Duration
}

// SSHClient is a Client over one SSH connection.
type SSHClient struct {
	ssh     *ssh.Client
	sftp    *sftp.Client
	timeout time.Duration
}

// DialSSH connects, verifies the host key against the pinned fingerprint and authenticates with the client key.
func DialSSH(ctx context.Context, cfg SSHConfig) (*SSHClient, error) {
	pinned := strings.TrimSpace(cfg.HostKeyFingerprint)
	if !strings.HasPrefix(pinned, "SHA256:") || len(pinned) == len("SHA256:") || cfg.Signer == nil || cfg.User == "" || cfg.Addr == "" {
		return nil, errors.New("sftp: address, user, client key and a SHA256: host key fingerprint are all required")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
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
	return &SSHClient{ssh: client, sftp: fs, timeout: timeout}, nil
}

func (c *SSHClient) Close() error { return errors.Join(c.sftp.Close(), c.ssh.Close()) }

// within runs op and gives up when ctx ends or the timeout passes; the connection is then closed, because an SFTP request cannot
// be cancelled.
func (c *SSHClient) within(ctx context.Context, op func() error) error {
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

func (c *SSHClient) List(ctx context.Context, dir string) ([]string, error) {
	var out []string
	err := c.within(ctx, func() error {
		infos, err := c.sftp.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, i := range infos {
			if i.Mode().IsRegular() {
				out = append(out, i.Name())
			}
		}
		return nil
	})
	return out, err
}

func (c *SSHClient) Read(ctx context.Context, filePath string, maxBytes int) ([]byte, error) {
	var out []byte
	err := c.within(ctx, func() error {
		f, err := c.sftp.Open(filePath)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		out, err = io.ReadAll(io.LimitReader(f, int64(maxBytes)))
		return err
	})
	return out, err
}

// Move renames with the server's POSIX rename where there is one, which refuses nothing; it checks the target first so a file never
// replaces an earlier one of the same name.
func (c *SSHClient) Move(ctx context.Context, from, to string) error {
	return c.within(ctx, func() error {
		if _, err := c.sftp.Stat(to); err == nil {
			return fmt.Errorf("%s already exists", to)
		}
		return c.sftp.Rename(from, to)
	})
}
