package bankconnect

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// memorySFTP is a deterministic in-memory SFTP server: a flat map of remote paths.
// failPut and failRename inject an error on the n-th call of that operation.
type memorySFTP struct {
	files      map[string][]byte
	puts       int
	renames    int
	failPut    map[int]error
	failRename map[int]error
}

func newMemorySFTP() *memorySFTP { return &memorySFTP{files: map[string][]byte{}} }

func (m *memorySFTP) Put(_ context.Context, p string, data []byte) error {
	m.puts++
	if err := m.failPut[m.puts]; err != nil {
		return err
	}
	m.files[p] = append([]byte(nil), data...)
	return nil
}

func (m *memorySFTP) Rename(_ context.Context, from, to string) error {
	m.renames++
	if err := m.failRename[m.renames]; err != nil {
		return err
	}
	data, ok := m.files[from]
	if !ok {
		return fmt.Errorf("rename %s: no such file", from)
	}
	delete(m.files, from)
	m.files[to] = data
	return nil
}

func (m *memorySFTP) names() []string {
	out := make([]string, 0, len(m.files))
	for n := range m.files {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

type fixedSigner struct{ calls int }

func (s *fixedSigner) Sign(context.Context, string, []byte) (Signature, error) {
	s.calls++
	return Signature{Bytes: []byte("sig-bytes"), KeyVersion: 7}, nil
}

func TestSFTP_UploadsSignatureThenDataAndPublishesByRename(t *testing.T) {
	srv := newMemorySFTP()
	audit := &memoryAudit{}
	receipt, err := Sender{Signer: &fixedSigner{}, Transport: SFTP{Dir: "/inbound/pain", Client: srv}, Audit: audit}.Send(context.Background(), pain001)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Reference != "/inbound/pain/pain001-0001.xml" {
		t.Fatalf("receipt = %+v", receipt)
	}
	if got := strings.Join(srv.names(), ","); got != "/inbound/pain/pain001-0001.xml,/inbound/pain/pain001-0001.xml.sig" {
		t.Fatalf("server holds %s; want the final file and its signature, no .part", got)
	}
	if string(srv.files["/inbound/pain/pain001-0001.xml"]) != string(pain001.Data) {
		t.Fatal("stored data differs from the file")
	}
	if raw, _ := base64.StdEncoding.DecodeString(string(srv.files["/inbound/pain/pain001-0001.xml.sig"])); string(raw) != "sig-bytes" {
		t.Fatalf("signature file = %q", srv.files["/inbound/pain/pain001-0001.xml.sig"])
	}
	sum := sha256.Sum256(pain001.Data)
	if len(audit.rows) != 2 || audit.rows[1].Event != EventSent || audit.rows[1].Protocol != ProtocolSFTP ||
		audit.rows[1].SHA256 != hex.EncodeToString(sum[:]) || audit.rows[1].KeyVersion != 7 || audit.rows[1].BankReference != receipt.Reference {
		t.Fatalf("audit rows = %+v", audit.rows)
	}
}

func TestSFTP_FailureBeforeTheRenameLeavesNothingVisibleAndIsFailedNotUnknown(t *testing.T) {
	for name, srv := range map[string]*memorySFTP{
		"signature upload fails": {files: map[string][]byte{}, failPut: map[int]error{1: ErrSFTPUnreachable}},
		"data upload fails":      {files: map[string][]byte{}, failPut: map[int]error{2: errors.New("disk full")}},
	} {
		audit := &memoryAudit{}
		_, err := Sender{Signer: &fixedSigner{}, Transport: SFTP{Dir: "in", Client: srv}, Audit: audit}.Send(context.Background(), pain001)
		if err == nil || errors.Is(err, ErrAmbiguous) {
			t.Fatalf("%s: err = %v; want a definite failure", name, err)
		}
		if _, visible := srv.files["/in/pain001-0001.xml"]; visible {
			t.Fatalf("%s: the bank can see the file", name)
		}
		if len(audit.rows) != 2 || audit.rows[1].Event != EventFailed {
			t.Fatalf("%s: audit rows = %+v", name, audit.rows)
		}
	}
}

func TestSFTP_FailedRenameIsAmbiguousAndAuditedUnknown(t *testing.T) {
	srv := newMemorySFTP()
	srv.failRename = map[int]error{1: errors.New("connection reset")}
	audit := &memoryAudit{}
	_, err := Sender{Signer: &fixedSigner{}, Transport: SFTP{Dir: "in", Client: srv}, Audit: audit}.Send(context.Background(), pain001)
	if !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("err = %v; want ErrAmbiguous", err)
	}
	if len(audit.rows) != 2 || audit.rows[1].Event != EventUnknown {
		t.Fatalf("audit rows = %+v", audit.rows)
	}
}

func TestSFTP_RefusesPathsOutsideTheDirectory(t *testing.T) {
	for _, name := range []string{"", "../etc/passwd", "a/b.xml", ".hidden", `a\b.xml`, "/abs.xml"} {
		srv := newMemorySFTP()
		f := pain001
		f.Name = name
		if _, err := (SFTP{Dir: "in", Client: srv}).Send(context.Background(), f, Signature{Bytes: []byte("s")}); err == nil {
			t.Errorf("name %q was accepted", name)
		}
		if srv.puts != 0 {
			t.Errorf("name %q reached the server", name)
		}
	}
	srv := newMemorySFTP()
	if _, err := (SFTP{Dir: "../../root", Client: srv}).Send(context.Background(), pain001, Signature{Bytes: []byte("s")}); err != nil {
		t.Fatal(err)
	}
	for p := range srv.files {
		if !strings.HasPrefix(p, "/root/") {
			t.Errorf("path %s escaped the cleaned directory", p)
		}
	}
}

func TestSFTP_CancelledContextSendsNothing(t *testing.T) {
	srv := newMemorySFTP()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (SFTP{Dir: "in", Client: srv}).Send(ctx, pain001, Signature{Bytes: []byte("s")}); err == nil || srv.puts != 0 {
		t.Fatalf("err = %v, puts = %d", err, srv.puts)
	}
}
