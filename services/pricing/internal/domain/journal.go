package domain

import (
	"fmt"
	"sort"

	"github.com/shopspring/decimal"
)

// JournalOp is one mass-update operation on a condition record (FR-26.9).
type JournalOp string

const (
	OpCreate JournalOp = "CREATE"
	OpUpdate JournalOp = "UPDATE"
	OpDelete JournalOp = "DELETE"
	// OpRestore re-activates a withdrawn record. Only reversal produces it; the API refuses it.
	OpRestore JournalOp = "RESTORE"
)

// Price journal lifecycle (BR-26.9).
const (
	JournalOpen      = "OPEN"
	JournalValidated = "VALIDATED"
	JournalPosted    = "POSTED"
	JournalReversed  = "REVERSED"
)

// Journal limits and error codes.
const (
	MaxJournalLines     = 100000
	CodeJournalInvalid  = "PRC-0005"
	CodeConflictVersion = "conflict_version"
)

// LineError is one validation failure on a journal line.
type LineError struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// JournalLine is one operation of a price journal. Record is the after-state for
// CREATE, UPDATE and RESTORE; AdjustPct (UPDATE only) instead raises or lowers the
// current amount by a percentage. Before and After are filled by ValidateJournal.
type JournalLine struct {
	No              int
	Op              JournalOp
	RecordID        string
	ExpectedVersion int
	Record          Record
	AdjustPct       decimal.Decimal
	Before          *Record
	After           *Record
	AfterVersion    int
	Errors          []LineError
}

// Journal is a mass change to condition records. It validates in full before it posts (FR-26.9).
type Journal struct {
	ID            string
	LegalEntityID string
	Name          string
	SmartRound    bool
	Lines         []JournalLine
}

// ValidateJournal checks every line against its own rules and against the records it
// would collide with. stored holds the current state of each record the journal names
// and of every ACTIVE record of the legal entity. It fills Before, After and Errors on
// each line and reports whether the whole journal is clean. Pure: same inputs, same output.
func ValidateJournal(j *Journal, stored map[string]Record, bands []SmartBand) bool {
	if len(j.Lines) == 0 {
		return false
	}
	touched := map[string]bool{}
	for i := range j.Lines {
		j.Lines[i].Before, j.Lines[i].After, j.Lines[i].Errors = nil, nil, nil
		if j.Lines[i].RecordID != "" && j.Lines[i].Op != OpCreate {
			touched[j.Lines[i].RecordID] = true
		}
	}
	// The collision pool: ACTIVE records the journal does not touch, bucketed by key.
	// Sorted IDs keep the reported conflict identical across runs.
	ids := make([]string, 0, len(stored))
	for id := range stored {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	pool := map[string][]Record{}
	for _, id := range ids {
		if r := stored[id]; r.Status == StatusActive && !touched[id] {
			k := keyIndex(r)
			pool[k] = append(pool[k], r)
		}
	}
	clean := true
	seen := map[string]int{}
	for i := range j.Lines {
		l := &j.Lines[i]
		// One record changes at most once per journal: a second line would be validated
		// against the version before the first and could only fail at post.
		if first, dup := seen[l.RecordID]; dup && l.RecordID != "" {
			l.Errors = []LineError{{Code: CodeValidation, Detail: fmt.Sprintf("record %s is already changed by line %d", l.RecordID, first)}}
			clean = false
			continue
		}
		seen[l.RecordID] = l.No
		after, errs := planLine(l, stored, j.SmartRound, bands)
		if len(errs) == 0 && after != nil && after.Status == StatusActive {
			if other, clash := Conflict(*after, pool[keyIndex(*after)]); clash {
				errs = append(errs, LineError{Code: CodeOverlap, Detail: fmt.Sprintf("overlaps active record %s", other.ID)})
			} else {
				pool[keyIndex(*after)] = append(pool[keyIndex(*after)], *after)
			}
		}
		l.Errors = errs
		if len(errs) > 0 {
			clean = false
			continue
		}
		l.After = after
	}
	return clean
}

// planLine works out the after-state of one line and its rule errors (not overlaps).
func planLine(l *JournalLine, stored map[string]Record, smart bool, bands []SmartBand) (*Record, []LineError) {
	if l.RecordID == "" {
		return nil, []LineError{{Code: CodeValidation, Detail: "record id is required"}}
	}
	if l.Op == OpCreate {
		rec := l.Record
		rec.ID, rec.Status, rec.Version = l.RecordID, StatusActive, 1
		return checkRecord(rec)
	}
	cur, ok := stored[l.RecordID]
	if !ok {
		return nil, []LineError{{Code: CodeValidation, Detail: fmt.Sprintf("record %s not found", l.RecordID)}}
	}
	before := cur
	l.Before = &before
	if cur.Version != l.ExpectedVersion {
		return nil, []LineError{{Code: CodeConflictVersion, Detail: fmt.Sprintf("record %s is at version %d, expected %d", cur.ID, cur.Version, l.ExpectedVersion)}}
	}
	switch l.Op {
	case OpUpdate:
		if cur.Status != StatusActive {
			return nil, []LineError{{Code: CodeValidation, Detail: fmt.Sprintf("record %s is %s; only ACTIVE records change", cur.ID, cur.Status)}}
		}
		rec := l.Record
		if !l.AdjustPct.IsZero() {
			rec = cur
			amount := cur.Amount.Mul(decimal.NewFromInt(100).Add(l.AdjustPct)).Shift(-2).RoundBank(6)
			if smart {
				amount = SmartRound(amount, bands)
			}
			rec.Amount = amount
		}
		// The campaign is part of the record's identity: a journal never moves a record
		// into or out of a campaign.
		rec.ID, rec.Status, rec.Version, rec.CampaignID = l.RecordID, StatusActive, cur.Version+1, cur.CampaignID
		return checkRecord(rec)
	case OpRestore:
		if cur.Status != StatusWithdrawn {
			return nil, []LineError{{Code: CodeValidation, Detail: fmt.Sprintf("record %s is %s; only WITHDRAWN records restore", cur.ID, cur.Status)}}
		}
		rec := l.Record
		rec.ID, rec.Status, rec.Version, rec.CampaignID = l.RecordID, StatusActive, cur.Version+1, cur.CampaignID
		return checkRecord(rec)
	case OpDelete:
		if cur.Status != StatusActive {
			return nil, []LineError{{Code: CodeValidation, Detail: fmt.Sprintf("record %s is %s; only ACTIVE records withdraw", cur.ID, cur.Status)}}
		}
		rec := cur
		rec.Status, rec.Version = StatusWithdrawn, cur.Version+1
		return &rec, nil
	}
	return nil, []LineError{{Code: CodeValidation, Detail: fmt.Sprintf("unknown operation %q", l.Op)}}
}

// checkRecord runs the record rules on an after-state and turns failures into line errors.
func checkRecord(rec Record) (*Record, []LineError) {
	if verr := ValidateRecord(rec); verr != nil {
		return nil, []LineError{{Code: verr.Code, Detail: fieldDetail(verr.Fields)}}
	}
	return &rec, nil
}

// fieldDetail renders field errors in a stable order.
func fieldDetail(fields map[string]string) string {
	names := make([]string, 0, len(fields))
	for n := range fields {
		names = append(names, n)
	}
	sort.Strings(names)
	out := ""
	for i, n := range names {
		if i > 0 {
			out += "; "
		}
		out += n + " " + fields[n]
	}
	return out
}

// keyIndex buckets records by every field sameKey compares, so overlap checks stay linear.
func keyIndex(r Record) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s",
		r.Type, r.CustomerKind, r.CustomerKey, r.ProductKind, r.ProductKey, r.Variant,
		r.Currency, r.Unit, r.FromQuantity.String(), r.CampaignID)
}

// Reverse builds the journal that undoes a posted journal (FR-26.9). Lines run in
// reverse order and restore the state each original line replaced. Expected versions
// are the versions the original produced, so a record changed since posting fails
// validation of the reversal instead of being overwritten.
func Reverse(orig Journal) Journal {
	rev := Journal{LegalEntityID: orig.LegalEntityID, SmartRound: orig.SmartRound}
	for i := len(orig.Lines) - 1; i >= 0; i-- {
		l := orig.Lines[i]
		nl := JournalLine{No: len(rev.Lines) + 1, RecordID: l.RecordID, ExpectedVersion: l.AfterVersion}
		switch l.Op {
		case OpCreate:
			nl.Op = OpDelete
		case OpUpdate:
			nl.Op, nl.Record = OpUpdate, *l.Before
		case OpDelete:
			nl.Op, nl.Record = OpRestore, *l.Before
		case OpRestore:
			nl.Op = OpDelete
		}
		rev.Lines = append(rev.Lines, nl)
	}
	return rev
}
