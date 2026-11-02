package bankconnect

import (
	"context"
	"encoding/base64"
	"fmt"
	"path"
	"strings"
)

const ProtocolSFTP = "sftp"

// SFTPClient is the file-transfer session the adapter needs. Put and Rename
// return an error only when the operation did not complete as asked. SFTP.Send
// treats a failed Put or a failed signature rename as a definite failure (the
// bank sees nothing yet) and a failed final rename as ambiguous.
type SFTPClient interface {
	Put(ctx context.Context, remotePath string, data []byte) error
	Rename(ctx context.Context, from, to string) error
}

// SFTP delivers a file to a bank's inbound directory. Both the signature
// (<name>.sig.part) and the data (<name>.part) are uploaded under temporary
// names. The signature is then renamed to <name>.sig and the data last to
// <name>, so the bank never sees the data file without its signature or half a
// file. A failure before the final rename means the bank has no data file; a
// failed or unanswered final rename is ambiguous, because the server may have
// applied it.
type SFTP struct {
	Dir    string
	Client SFTPClient
}

func (SFTP) Protocol() string { return ProtocolSFTP }

func (s SFTP) Send(ctx context.Context, f File, sig Signature) (Receipt, error) {
	if f.Name == "" || f.Name != path.Base(f.Name) || strings.HasPrefix(f.Name, ".") || strings.ContainsAny(f.Name, `\`) {
		return Receipt{}, fmt.Errorf("sftp upload refused: file name %q must be a plain name", f.Name)
	}
	final := path.Join(path.Clean("/"+s.Dir), f.Name)
	if err := ctx.Err(); err != nil {
		return Receipt{}, fmt.Errorf("sftp upload not started: %w", err)
	}
	steps := []func() error{
		func() error {
			return s.Client.Put(ctx, final+".sig.part", []byte(base64.StdEncoding.EncodeToString(sig.Bytes)))
		},
		func() error { return s.Client.Put(ctx, final+".part", f.Data) },
		func() error { return s.Client.Rename(ctx, final+".sig.part", final+".sig") },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return Receipt{}, fmt.Errorf("sftp upload not sent: %w", err)
		}
	}
	if err := s.Client.Rename(ctx, final+".part", final); err != nil {
		return Receipt{}, fmt.Errorf("sftp upload: %w: %w", ErrAmbiguous, err)
	}
	return Receipt{Reference: final}, nil
}
