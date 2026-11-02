package domain

import (
	"encoding/hex"
	"hash/fnv"
	"math"
	"strconv"
	"time"
)

// Delta sync (integration track S4).
const (
	SourceLegacy  = "legacy"
	SourcePassage = "passage"

	SyncOK        = "succeeded"
	SyncPartial   = "partial"
	SyncFailed    = "failed"
	SyncAbandoned = "abandoned"

	ConflictOpen     = "open"
	ConflictKept     = "kept"
	ConflictAccepted = "accepted"

	ResolveAcceptLegacy = "accept_legacy"
	ResolveKeepPassage  = "keep_passage"

	// DefaultKeyCheckHours is how often a binding with a cursor is read in full to find rows the legacy system deleted.
	DefaultKeyCheckHours = 24
	// MaxKeyCheckHours is the longest key check period an operator may set (one year); 0 turns the check off.
	MaxKeyCheckHours = 8760

	// BackoffAfter is the number of consecutive failures after which a binding's next run is pushed out.
	BackoffAfter = 3
	// BackoffBase is the first delay once BackoffAfter failures are reached; each further failure doubles it.
	BackoffBase = 15 * time.Minute
	// BackoffCap is the longest delay a failing binding waits.
	BackoffCap = 24 * time.Hour
)

// SyncLookback is how far before its timestamp watermark a scheduled delta reads again. The extractors return rows
// strictly after the value they are given; reading from a little earlier also picks up rows that share the
// watermark's timestamp but were committed after the last read, and rows a long transaction committed late with an
// earlier timestamp. Rows read twice are unchanged by content hash and are not loaded again.
const SyncLookback = 5 * time.Minute

// DeltaSince is the value a scheduled delta passes to the extractor for a stored watermark: SyncLookback earlier for a
// timestamp, the predecessor for an integer or row version (so a row equal to the watermark is read again), and ""
// (read everything) for "" or a watermark that is not of the cursor type.
func DeltaSince(typ, watermark string) string {
	if watermark == "" {
		return ""
	}
	switch typ {
	case CursorTimestamp:
		t, err := time.Parse(time.RFC3339Nano, watermark)
		if err != nil {
			return ""
		}
		return t.Add(-SyncLookback).UTC().Format(cursorLayout)
	case CursorInt:
		n, err := strconv.ParseInt(watermark, 10, 64)
		if err != nil {
			return ""
		}
		if n == math.MinInt64 {
			return watermark
		}
		return strconv.FormatInt(n-1, 10)
	case CursorRowversion:
		b, err := hex.DecodeString(watermark)
		if err != nil || len(b) == 0 {
			return ""
		}
		for i := len(b) - 1; i >= 0; i-- {
			if b[i] > 0 {
				b[i]--
				break
			}
			if i == 0 {
				return watermark // all zero: nothing is smaller
			}
			b[i] = 0xff
		}
		return hex.EncodeToString(b)
	}
	return ""
}

// ValidSourceOfTruth reports whether s names the system that wins when both changed a record.
func ValidSourceOfTruth(s string) bool { return s == SourceLegacy || s == SourcePassage }

// SyncBackoff is how long a binding waits after its failures-th consecutive failure before it may run again, on top
// of its schedule: none below BackoffAfter, then 15 minutes doubling per failure up to 24 hours.
func SyncBackoff(failures int) time.Duration {
	if failures < BackoffAfter {
		return 0
	}
	d := BackoffBase
	for i := BackoffAfter; i < failures && d < BackoffCap; i++ {
		d *= 2
	}
	return min(d, BackoffCap)
}

// KeyCheckSlack is how much earlier than a full period after the last attempt a key check is already due. The attempt
// is stamped when its run ends, so without it a binding on a daily schedule with the default 24 hours would find the
// check a few seconds short of due at every other run and read in full only every second day. It covers a run's
// deadline plus a scheduler pass, and is at most a quarter of the period.
const KeyCheckSlack = 40 * time.Minute

// KeyCheckOffset is how long after its anchor a binding's first key check falls: a deterministic offset inside the
// period from a hash of the binding id, so bindings that were all added (or all migrated onto key checks) together do
// not all read their legacy source in full at the same moment.
func KeyCheckOffset(bindingID string, hours int) time.Duration {
	if hours <= 0 {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(bindingID))
	period := uint64(time.Duration(hours) * time.Hour / time.Second)
	return time.Duration(h.Sum64()%period) * time.Second
}

// KeyCheckState is what decides whether a binding's next scheduled run reads its source in full to find deleted rows.
type KeyCheckState struct {
	BindingID string
	// Hours is the period; 0 turns the check off.
	Hours int
	// LastAttempt is the last full read tried, failed or not (nil before the first).
	LastAttempt *time.Time
	// Force is an operator's request (keyCheckNow, keyCheckAcceptLarge) for the next run to be a full read.
	Force bool
	// Since anchors the first check: when the binding's sync state was created, or when key checks were introduced
	// for a binding that existed before.
	Since time.Time
}

// Due reports whether a run at now must read the source in full: the check is on and an operator asked for it, or the
// last attempt is (all but KeyCheckSlack) a period old, or there has been none and the binding's offset after Since
// has passed. Once due it stays due until a run attempts the check, whatever times the binding's schedule runs at;
// after the first attempt the period runs from the attempt, so the spread of first checks carries over.
func (k KeyCheckState) Due(now time.Time) bool {
	if k.Hours <= 0 {
		return false
	}
	if k.Force {
		return true
	}
	period := time.Duration(k.Hours) * time.Hour
	if k.LastAttempt != nil {
		return !k.LastAttempt.Add(period - min(KeyCheckSlack, period/4)).After(now)
	}
	return !k.Since.Add(KeyCheckOffset(k.BindingID, k.Hours)).After(now)
}

// KeyCheckVanishTooLarge reports whether a full read lists so many newly absent keys (more than half of the loaded keys
// that were still present, i.e. not listed absent already) that it is taken for a broken source rather than deletions,
// and needs an operator to accept it. Keys listed before are left out of both counts: they are already gone from the
// source and say nothing about whether this read is whole.
func KeyCheckVanishTooLarge(newlyAbsent, present int) bool {
	return present > 0 && newlyAbsent*2 > present
}

// ValidKeyCheckHours reports whether h is a period the service accepts (0 is off).
func ValidKeyCheckHours(h int) bool { return h >= 0 && h <= MaxKeyCheckHours }
