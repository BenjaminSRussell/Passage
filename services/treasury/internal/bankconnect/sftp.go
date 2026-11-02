package bankconnect

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"path"
	"strings"
)

const ProtocolSFTP = "sftp"

// SFTPClient is the file-transfer session the adapter needs. A production client
// wraps an SSH/SFTP library (not a dependency of this module yet); tests use an
// in-memory double. Put must return ErrSFTPUnreachable when it fails before any
// byte could have left, and any other error when the outcome is not known.
type SFTPClient interface {
	Put(ctx context.Context, remotePath string, data []byte) error
	Rename(ctx context.Context, from, to string) error
}

// ErrSFTPUnreachable marks a failure before the connection carried any data.
var ErrSFTPUnreachable = errors.New("sftp server unreachable")

// SFTP delivers a file to a bank's inbound directory. The signature goes first
// as <name>.sig, the data as <name>.part, and only the rename of the .part file to
// <name> makes it visible to the bank, so the bank never picks up half a file. A
// failure before the rename means the bank has nothing; a failed or unanswered
// rename is ambiguous, because the server may have applied it.
type SFTP struct {
	Dir    string
	Client SFTPClient
}

func (SFTP) Protocol() string { return ProtocolSFTP }

func (s SFTP) Send(ctx context.Context, f File, sig Signature) (Receipt, error) {
	if f.Name == "" || f.Name != path.Base(f.Name) || strings.HasPrefix(f.Name, ".") || strings.ContainsAny(f.Name, `\`) {
		return Receipt{}, fmt.Errorf("sftp upload refused: file name %q must be a plain name", f.Name)
	}
	dir := path.Clean("/" + s.Dir)
	final := path.Join(dir, f.Name)
	if err := ctx.Err(); err != nil {
		return Receipt{}, fmt.Errorf("sftp upload not started: %w", err)
	}
	if err := s.Client.Put(ctx, final+".sig", []byte(base64.StdEncoding.EncodeToString(sig.Bytes))); err != nil {
		return Receipt{}, fmt.Errorf("sftp upload not sent: %w", err)
	}
	if err := s.Client.Put(ctx, final+".part", f.Data); err != nil {
		return Receipt{}, fmt.Errorf("sftp upload not sent: %w", err)
	}
	if err := s.Client.Rename(ctx, final+".part", final); err != nil {
		return Receipt{}, fmt.Errorf("sftp upload: %w: %w", ErrAmbiguous, err)
	}
	return Receipt{Reference: final}, nil
}
