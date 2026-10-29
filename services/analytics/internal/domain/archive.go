package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"time"
)

// Archive partition states (archive_partition.status, FR-46.18).
const (
	ArchivePending  = "PENDING"
	ArchiveVerified = "VERIFIED"
	ArchiveDropped  = "DROPPED"
	ArchiveRestored = "RESTORED"

	// CodeArchiveVerify is raised when the OLTP and archive copies disagree (BR-46.14).
	CodeArchiveVerify = "ANA-0110"
	// ArchiveIncidentSeverity is the severity of a verification incident.
	ArchiveIncidentSeverity = "Sev-3"

	DefaultResidenceMonths = 24
	MinResidenceMonths     = 6
	MaxResidenceMonths     = 120
	DefaultRestoreTTLDays  = 30
	MinRestoreTTLDays      = 1
	MaxRestoreTTLDays      = 90
	periodLayout           = "2006-01"
)

var (
	ErrLegalHold    = errors.New("archive: partition is under legal hold")
	ErrOpenPeriod   = errors.New("archive: partition belongs to an open fiscal period")
	ErrNotAged      = errors.New("archive: partition is younger than its residence period")
	ErrArchiveState = errors.New("archive: partition is not in a state that allows this action")
	ErrResidence    = errors.New("archive: residence months outside the allowed range")
	ErrRestoreTTL   = errors.New("archive: restore lifetime outside 1-90 days")
)

// ArchivePartition is one month of one OLTP table moved to the archive tier.
// Period is "YYYY-MM". Hashes are content digests from ContentHash.
type ArchivePartition struct {
	ID           string     `json:"partitionId"`
	Domain       string     `json:"domain"`
	Table        string     `json:"tableName"`
	Period       string     `json:"period"`
	RowsOLTP     int64      `json:"rowsOltp"`
	RowsArchive  int64      `json:"rowsArchive"`
	HashOLTP     string     `json:"contentHashOltp"`
	HashArchive  string     `json:"contentHashArchive"`
	Status       string     `json:"status"`
	LegalHold    bool       `json:"legalHold"`
	OpenPeriod   bool       `json:"openPeriod"`
	MovedAt      *time.Time `json:"movedAt,omitempty"`
	RestoreUntil *time.Time `json:"restoreUntil,omitempty"`
}

var archiveNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,62}$`)

// IsArchiveName reports whether s is a valid domain or table name.
func IsArchiveName(s string) bool { return archiveNamePattern.MatchString(s) }

// IsArchiveStatus reports whether s is an archive partition state.
func IsArchiveStatus(s string) bool {
	return slices.Contains([]string{ArchivePending, ArchiveVerified, ArchiveDropped, ArchiveRestored}, s)
}

// NewArchivePartition starts a PENDING partition after validating its period.
func NewArchivePartition(domain, table, period string) (ArchivePartition, *Error) {
	if _, err := time.Parse(periodLayout, period); err != nil {
		return ArchivePartition{}, &Error{Code: CodeValidation, Status: 400, Detail: "period must be YYYY-MM", Fields: map[string]string{"period": "must be YYYY-MM"}}
	}
	fields := map[string]string{}
	if !archiveNamePattern.MatchString(domain) {
		fields["domain"] = "lowercase letters, digits and underscores, 2-63 characters"
	}
	if !archiveNamePattern.MatchString(table) {
		fields["tableName"] = "lowercase letters, digits and underscores, 2-63 characters"
	}
	if len(fields) > 0 {
		return ArchivePartition{}, validation(fields)
	}
	return ArchivePartition{Domain: domain, Table: table, Period: period, Status: ArchivePending}, nil
}

// ValidateResidence checks a residence_months setting against the platform range
// and the wave-48 statutory minimum for the entity's country (§9.46.13).
func ValidateResidence(months, statutoryMin int) error {
	lo := max(MinResidenceMonths, statutoryMin)
	if months < lo || months > MaxResidenceMonths {
		return ErrResidence
	}
	return nil
}

// ReadyToMove reports whether the whole period plus residence_months has elapsed.
// A period 2024-01 with 24 months of residence is movable from 2026-02-01 on.
func ReadyToMove(period string, residenceMonths int, now time.Time) (bool, error) {
	start, err := time.Parse(periodLayout, period)
	if err != nil {
		return false, err
	}
	return !now.Before(start.AddDate(0, 1+residenceMonths, 0)), nil
}

// CheckMovable enforces the FR-46.18 exclusions: records under legal hold or in
// an open fiscal period never move, and the partition must have aged out.
func (p ArchivePartition) CheckMovable(now time.Time, residenceMonths int) error {
	switch {
	case p.Status != ArchivePending:
		return ErrArchiveState
	case p.LegalHold:
		return ErrLegalHold
	case p.OpenPeriod:
		return ErrOpenPeriod
	}
	ready, err := ReadyToMove(p.Period, residenceMonths, now)
	if err != nil {
		return err
	}
	if !ready {
		return ErrNotAged
	}
	return nil
}

// Verify compares the two copies after the move (BR-46.14). Equal row counts and
// equal, non-empty content hashes make the partition VERIFIED (moved at now) and
// safe to drop. Any mismatch records the counts, keeps the partition PENDING
// with both copies, and returns an ANA-0110 error for a Sev-3 incident.
func (p *ArchivePartition) Verify(rowsOLTP, rowsArchive int64, hashOLTP, hashArchive string, now time.Time) *Error {
	if p.Status != ArchivePending {
		return &Error{Code: CodeStateConflict, Status: 409, Detail: "partition is " + p.Status + ", not PENDING verification"}
	}
	p.RowsOLTP, p.RowsArchive = rowsOLTP, rowsArchive
	p.HashOLTP, p.HashArchive = hashOLTP, hashArchive
	if rowsOLTP != rowsArchive || hashOLTP == "" || hashOLTP != hashArchive {
		detail := "archive verification failed for " + p.Domain + "." + p.Table + " " + p.Period +
			": counts " + strconv.FormatInt(rowsOLTP, 10) + " vs " + strconv.FormatInt(rowsArchive, 10)
		if rowsOLTP == rowsArchive {
			detail += ", content hash differs"
		}
		return &Error{Code: CodeArchiveVerify, Status: 409, Detail: detail}
	}
	p.Status = ArchiveVerified
	moved := now.UTC()
	p.MovedAt = &moved
	return nil
}

// Drop removes the OLTP partition record. Only a VERIFIED partition can be
// dropped, and a legal hold placed after the move still blocks the drop.
func (p *ArchivePartition) Drop() error {
	if p.Status != ArchiveVerified {
		return ErrArchiveState
	}
	if p.LegalHold {
		return ErrLegalHold
	}
	p.Status = ArchiveDropped
	return nil
}

// Restore re-materialises a DROPPED partition in a side table for ttlDays. A
// restore whose lifetime has passed counts as DROPPED again, so it can be
// restored anew.
func (p *ArchivePartition) Restore(now time.Time, ttlDays int) error {
	if ttlDays < MinRestoreTTLDays || ttlDays > MaxRestoreTTLDays {
		return ErrRestoreTTL
	}
	if p.Status != ArchiveDropped && !p.RestoreExpired(now) {
		return ErrArchiveState
	}
	until := now.UTC().AddDate(0, 0, ttlDays)
	p.Status = ArchiveRestored
	p.RestoreUntil = &until
	return nil
}

// RestoreExpired reports whether a restored side table has passed its lifetime.
func (p ArchivePartition) RestoreExpired(now time.Time) bool {
	return p.Status == ArchiveRestored && p.RestoreUntil != nil && !now.Before(*p.RestoreUntil)
}

// ExpireRestore returns an expired RESTORED partition to DROPPED: the side
// table is removed and the archive copy remains the only one.
func (p *ArchivePartition) ExpireRestore(now time.Time) bool {
	if !p.RestoreExpired(now) {
		return false
	}
	p.Status = ArchiveDropped
	p.RestoreUntil = nil
	return true
}

// ContentHash is an order-independent digest of a partition's rows. Each row is
// hashed, the row digests are sorted (duplicates count), and the sorted list is
// hashed again. Two copies with the same rows hash equal regardless of order.
func ContentHash(rows []string) string {
	sums := make([]string, len(rows))
	for i, r := range rows {
		s := sha256.Sum256([]byte(r))
		sums[i] = hex.EncodeToString(s[:])
	}
	slices.Sort(sums)
	h := sha256.New()
	for _, s := range sums {
		h.Write([]byte(s))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}
