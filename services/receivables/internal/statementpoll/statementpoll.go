// Package statementpoll is the SFTP poll of bank statements (FR-30.5, 30-4): the bank drops camt.053 files in an inbox directory and
// the poller imports each one through the same path as POST /bank-statements/imports, then moves it to a done directory (or to a
// failed directory when it cannot be read), so a file is never imported twice and a bad file does not block the ones behind it.
package statementpoll

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

const (
	// MaxFileBytes bounds one statement file, as the upload route does.
	MaxFileBytes = 3 << 20
	// MaxFilesPerPoll bounds the files one poll takes, so a flooded inbox is worked off over several polls.
	MaxFilesPerPoll = 100
	extension       = ".xml"
)

// Client is the file server the bank's statements arrive on.
type Client interface {
	// List returns the names (not paths) of the regular files in dir.
	List(ctx context.Context, dir string) ([]string, error)
	Read(ctx context.Context, filePath string, maxBytes int) ([]byte, error)
	// Move renames a file; it fails when the target exists, so a file never overwrites an earlier one.
	Move(ctx context.Context, from, to string) error
}

// Importer is the store side: it records a parsed statement. *store.Postgres implements it.
type Importer interface {
	ImportBankStatement(ctx context.Context, in store.BankStatementImport) (store.BankStatementBatch, error)
	AutoApplyPayments(ctx context.Context, paymentIDs []string) (store.AutoApplyResult, error)
}

// Source is one inbox: the files in Inbox are imported into LegalEntityID, then moved to Done or Failed.
type Source struct {
	LegalEntityID string
	Inbox         string
	Done          string
	Failed        string
	// AutoApply matches and applies the payments each file created.
	AutoApply bool
}

// Result counts what one poll of one source did.
type Result struct {
	Imported   int  `json:"imported"`
	Duplicates int  `json:"duplicates"`
	Failed     int  `json:"failed"`
	Payments   int  `json:"payments"`
	Applied    int  `json:"applied"`
	More       bool `json:"more"`
}

// Poll imports the statement files waiting in the source's inbox, oldest name first. A file whose import fails for a reason that
// repeating cannot change (not XML, not camt.053, a malformed entry) goes to Failed; a file already imported (same SHA-256) goes to
// Done; a failure that may pass (the database or the file server) leaves the file in the inbox for the next poll and ends this one.
func Poll(ctx context.Context, c Client, imp Importer, src Source) (Result, error) {
	var res Result
	names, err := c.List(ctx, src.Inbox)
	if err != nil {
		return res, fmt.Errorf("list %s: %w", src.Inbox, err)
	}
	var files []string
	for _, n := range names {
		if strings.EqualFold(path.Ext(n), extension) && !strings.HasPrefix(n, ".") {
			files = append(files, n)
		}
	}
	sort.Strings(files)
	if len(files) > MaxFilesPerPoll {
		files, res.More = files[:MaxFilesPerPoll], true
	}
	for _, name := range files {
		from := path.Join(src.Inbox, name)
		outcome, imported, err := importFile(ctx, c, imp, src, from)
		switch outcome {
		case outcomeRetry:
			return res, fmt.Errorf("import %s: %w", from, err)
		case outcomeFailed:
			slog.WarnContext(ctx, "bank statement file refused", "file", from, "legalEntityId", src.LegalEntityID, "reason", err)
			res.Failed++
			if mvErr := moveUnique(ctx, c, from, src.Failed, name); mvErr != nil {
				return res, fmt.Errorf("move %s to %s: %w", from, src.Failed, mvErr)
			}
		case outcomeDuplicate:
			res.Duplicates++
			if mvErr := moveUnique(ctx, c, from, src.Done, name); mvErr != nil {
				return res, fmt.Errorf("move %s to %s: %w", from, src.Done, mvErr)
			}
		default:
			res.Imported++
			res.Payments += imported.PaymentCount
			if src.AutoApply && len(imported.Payments) > 0 {
				ids := make([]string, len(imported.Payments))
				for i, p := range imported.Payments {
					ids[i] = p.ID
				}
				applied, aerr := imp.AutoApplyPayments(ctx, ids)
				if aerr != nil {
					slog.WarnContext(ctx, "auto-apply after a statement import failed; the scheduled auto-apply picks the payments up", "file", from, "err", aerr)
				}
				res.Applied += applied.Applied
			}
			if mvErr := moveUnique(ctx, c, from, src.Done, name); mvErr != nil {
				return res, fmt.Errorf("move %s to %s: %w", from, src.Done, mvErr)
			}
		}
	}
	return res, nil
}

type outcome int

const (
	outcomeImported outcome = iota
	outcomeDuplicate
	outcomeFailed
	outcomeRetry
)

func importFile(ctx context.Context, c Client, imp Importer, src Source, from string) (outcome, store.BankStatementBatch, error) {
	content, err := c.Read(ctx, from, MaxFileBytes+1)
	if err != nil {
		return outcomeRetry, store.BankStatementBatch{}, err
	}
	if len(content) > MaxFileBytes {
		return outcomeFailed, store.BankStatementBatch{}, fmt.Errorf("the file is over %d bytes", MaxFileBytes)
	}
	stmt, err := domain.ParseCAMT053(content)
	if err != nil {
		var ce *domain.CAMTError
		if errors.As(err, &ce) {
			return outcomeFailed, store.BankStatementBatch{}, err
		}
		return outcomeRetry, store.BankStatementBatch{}, err
	}
	sum := sha256.Sum256(content)
	batch, err := imp.ImportBankStatement(ctx, store.BankStatementImport{LegalEntityID: src.LegalEntityID, FileSHA256: hex.EncodeToString(sum[:]),
		StatementCount: stmt.Statements, SkippedCount: stmt.Skipped, Credits: stmt.Credits})
	var p *httpx.Problem
	switch {
	case err == nil:
		return outcomeImported, batch, nil
	case errors.As(err, &p) && p.Code == store.CodeLockboxDuplicate:
		return outcomeDuplicate, batch, nil
	case errors.As(err, &p) && p.Status >= http.StatusBadRequest && p.Status < http.StatusInternalServerError:
		return outcomeFailed, batch, err
	}
	return outcomeRetry, batch, err
}

// moveUnique moves a file into dir under its own name, or, when a file of that name is already there (the bank re-sent a name), under
// a name with the time added, so the poll never stalls on a name clash and never replaces an archived file.
func moveUnique(ctx context.Context, c Client, from, dir, name string) error {
	if err := c.Move(ctx, from, path.Join(dir, name)); err == nil {
		return nil
	}
	ext := path.Ext(name)
	return c.Move(ctx, from, path.Join(dir, fmt.Sprintf("%s.%d%s", strings.TrimSuffix(name, ext), time.Now().UnixNano(), ext)))
}
